package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 端到端：假 TMDB（剧集 4 集已播）+ 假资源（一个分享、一个磁力、一个排除词）+ 假 115 分享接口。
// 第一轮：分享里只挑缺的 E02 E03，磁力补 E04；第二轮结算入库；离线失败后第三轮不再重试那条磁力
func TestSubRunnerEndToEnd(t *testing.T) {
	newTestDB(t, "subrun.db")
	subTestLayout(t)
	resetTmdbCaches()
	scrapeSeasonMu.Lock()
	scrapeSeasonCache = map[string]seasonCacheEntry{}
	scrapeSeasonMu.Unlock()

	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/500":
			fmt.Fprint(w, `{"status":"Returning Series","seasons":[{"season_number":1,"episode_count":4}]}`)
		case "/tv/500/season/1":
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"air_date":"2020-01-01"},{"episode_number":2,"air_date":"2020-01-08"},{"episode_number":3,"air_date":"2020-01-15"},{"episode_number":4,"air_date":"2020-01-22"},{"episode_number":5,"air_date":"2099-01-01"}]}`)
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	defer tmdb.Close()

	var received []url.Values
	pan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share/snap":
			switch r.URL.Query().Get("cid") {
			case "0":
				fmt.Fprint(w, `{"state":true,"data":{"shareinfo":{"share_title":"剧 S01"},"list":[{"cid":"300","n":"剧 S01","fc":"0"}]}}`)
			case "300":
				fmt.Fprint(w, `{"state":true,"data":{"list":[
					{"fid":"401","cid":"300","n":"剧.S01E01.mkv","s":10,"sha":"A1"},
					{"fid":"402","cid":"300","n":"剧.S01E02.mkv","s":10,"sha":"A2"},
					{"fid":"403","cid":"300","n":"剧.S01E03.mkv","s":10,"sha":"A3"},
					{"fid":"404","cid":"300","n":"剧.S01E03.chs.ass","s":1,"sha":"A4"}]}}`)
			}
		case "/share/receive":
			r.ParseForm()
			received = append(received, r.PostForm)
			fmt.Fprint(w, `{"state":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer pan.Close()
	orig := shareAPIOrigins
	shareAPIOrigins = []string{pan.URL}
	t.Cleanup(func() { shareAPIOrigins = orig })

	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	model.DB.Create(&model.Storage{Type: "115", Cookie: "UID=1"})
	subTestVideo("媒体库/剧集/剧.2020.{tmdbid=500}/Season 01/剧.S01E01.strm")
	invalidateLedgerTitles()

	sub := model.Subscription{TmdbID: 500, MediaType: "tv", Title: "剧", Year: "2020", Scope: subScopeAll, Follow: subFollowMissing, State: subStateActive}
	model.DB.Create(&sub)

	magnet := "magnet:?xt=urn:btih:4444444444444444444444444444444444444444"
	items := []ResourceItem{
		{Source: "pansou", Kind: "share115", Action: "transfer", Title: "剧 S01 1080p", URL: "https://115.com/s/sw1abc?password=x1y2", Relevant: true, Rank: -1},
		{Source: "tg", Kind: "magnet", Action: "offline", Title: "剧 S01E04 1080p", URL: magnet, Relevant: true, Rank: -1},
		{Source: "tg", Kind: "share115", Action: "transfer", Title: "剧 S01 枪版", URL: "https://115.com/s/sw2zzz", Relevant: true, Rank: -1},
	}
	var dirs []string
	var offlines []string
	nextLink := uint(100)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	r := &subRunner{
		h: h, cfg: defaultSubscribeCfg(), target: "T", cookie: "UID=1", re0Left: -1,
		tc:     &TmdbClient{APIKey: "k", APIURL: tmdb.URL, Language: "zh-CN", httpClient: tmdb.Client()},
		now:    func() time.Time { return now },
		search: func(*model.Subscription) ([]ResourceItem, string) { return append([]ResourceItem(nil), items...), "" },
		mkdir: func(parent, name string) (string, error) {
			dirs = append(dirs, name)
			return fmt.Sprintf("W%d", len(dirs)), nil
		},
		offline: func(link, target string) (int, string, uint) {
			offlines = append(offlines, link+"@"+target)
			nextLink++
			return 200, "ok", nextLink
		},
		rmdir: func(cid string) error {
			t.Errorf("提交都成功了，不该删包装目录 %s", cid)
			return nil
		},
	}

	// 第一轮
	item, ran := r.run(&sub, false)
	if !ran || len(item.Submitted) != 2 {
		t.Fatalf("第一轮应提交 2 条: %+v", item)
	}
	if len(received) != 1 || received[0].Get("file_id") != "402,403,404" || received[0].Get("cid") != "W1" {
		t.Fatalf("分享只该转缺的 E02 E03（带字幕）进包装目录: %v", received)
	}
	if len(dirs) != 2 || dirs[0] != "剧 (2020) S01 ·订阅1-1" || dirs[1] != "剧 (2020) S01 ·订阅1-2" {
		t.Fatalf("包装目录 = %v", dirs)
	}
	if len(offlines) != 1 || offlines[0] != magnet+"@W2" {
		t.Fatalf("离线 = %v", offlines)
	}
	model.DB.First(&sub, sub.ID)
	if sub.Missing != 0 || sub.Have != 1 || sub.Total != 4 || sub.NextCheckAt == nil || !sub.NextCheckAt.Equal(now.Add(subAfterSubmit)) {
		t.Fatalf("第一轮后的订阅: %+v", sub)
	}
	var atts []model.SubAttempt
	model.DB.Where("sub_id = ?", sub.ID).Order("id").Find(&atts)
	if len(atts) != 2 || atts[0].Episodes != `["S01E02","S01E03"]` || atts[1].Episodes != `["S01E04"]` {
		t.Fatalf("尝试记账: %+v", atts)
	}
	var link model.DownloadLink
	model.DB.First(&link, atts[0].LinkID)
	if link.Source != "订阅" || !strings.Contains(link.ResultNames, "订阅1-1") {
		t.Fatalf("来源链接: %+v", link)
	}

	// 第二轮：分享那条整理入库了（台账也有了），磁力还在下
	model.DB.Create(&model.OrganizeRecord{LinkID: atts[0].LinkID, Status: "success", TmdbID: 500, MediaType: "tv"})
	subTestVideo("媒体库/剧集/剧.2020.{tmdbid=500}/Season 01/剧.S01E02.strm")
	subTestVideo("媒体库/剧集/剧.2020.{tmdbid=500}/Season 01/剧.S01E03.strm")
	invalidateLedgerTitles()
	now = now.Add(time.Hour)
	if item, _ = r.run(&sub, false); len(item.Submitted) != 0 {
		t.Fatalf("第二轮不该再提交: %+v", item)
	}
	model.DB.First(&atts[0], atts[0].ID)
	if atts[0].Status != subAttemptIngested {
		t.Fatalf("分享应结算为入库: %+v", atts[0])
	}
	model.DB.First(&sub, sub.ID)
	if sub.Have != 3 || sub.Missing != 0 || sub.State != subStateActive {
		t.Fatalf("第二轮后的订阅: %+v", sub)
	}

	// 离线失败 → 第三轮 E04 又缺了，但那条磁力一天内不再试，也没别的可用 → 退避
	subMarkOfflineFailed(model.DB, atts[1].LinkID, "种子")
	now = now.Add(time.Hour)
	item, _ = r.run(&sub, false)
	if len(item.Submitted) != 0 || len(offlines) != 1 {
		t.Fatalf("失败过的磁力不该马上重试: %+v", item)
	}
	model.DB.First(&sub, sub.ID)
	if sub.Missing != 1 || sub.EmptyRounds != 1 || !sub.NextCheckAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("第三轮后的订阅: %+v", sub)
	}

	// 没到检查时间的定时任务直接跳过
	if _, ran := r.run(&sub, false); ran {
		t.Fatal("没到时间不该处理")
	}
}

func TestSubSchedulerTick(t *testing.T) {
	newTestDB(t, "subsched.db")
	h := &Handler{DB: model.DB}
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	model.DB.Create(&model.Subscription{TmdbID: 1, MediaType: "tv", State: subStateActive, NextCheckAt: &past})
	model.DB.Create(&model.Subscription{TmdbID: 2, MediaType: "tv", State: subStateActive, NextCheckAt: &future})
	model.DB.Create(&model.Subscription{TmdbID: 3, MediaType: "tv", State: subStatePaused, NextCheckAt: &past})
	model.DB.Create(&model.Subscription{TmdbID: 4, MediaType: "movie", State: subStateStalled})
	subSchedulerTick(h, now)
	subSchedulerTick(h, now) // 排着的取并集，不另建
	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindSubscribe).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("应只有一个订阅任务: %d", len(jobs))
	}
	p := decodeJobParams(&jobs[0])
	if p.Subs == nil || len(p.Subs.IDs) != 2 || p.Subs.IDs[0] == p.Subs.IDs[1] {
		t.Fatalf("应只挑到期的追更中 / 长期找不到: %+v", p.Subs)
	}
}

// 提交失败：刚建的包装目录要收掉，不然守望者会反复整理一个空目录直到熔断
func TestSubRunnerDropsWrapperOnFailure(t *testing.T) {
	newTestDB(t, "subrun-fail.db")
	var removed []string
	r := &subRunner{
		h: &Handler{DB: model.DB}, cfg: defaultSubscribeCfg(), target: "T", re0Left: -1, now: time.Now,
		mkdir:   func(parent, name string) (string, error) { return "W1", nil },
		rmdir:   func(cid string) error { removed = append(removed, cid); return nil },
		offline: func(link, target string) (int, string, uint) { return 502, "115 说不行", 0 },
	}
	sub := &model.Subscription{ID: 1, MediaType: "movie", Title: "片"}
	c := subCand{Item: ResourceItem{Source: "tg", Kind: "magnet", Action: "offline", Title: "片 2024", URL: "magnet:?xt=urn:btih:5555555555555555555555555555555555555555"}, Hash: "5555", Covers: 1}
	att, covered := r.tryOne(sub, c, map[epKey]bool{{}: true})
	if att.Status != subAttemptFailed || att.RetryAt == nil || len(covered) != 0 {
		t.Fatalf("提交失败应记失败、可重试: %+v", att)
	}
	if len(removed) != 1 || removed[0] != "W1" {
		t.Fatalf("应删掉空包装目录: %v", removed)
	}
}
