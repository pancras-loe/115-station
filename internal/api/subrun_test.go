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

// 端到端：假 TMDB（剧集 4 集已播）+ 假资源（一个分享、一个合集磁力、一个单集磁力、一个排除词）+ 假 115 分享接口。
// 第一轮：分享里只挑缺的 E02 E03，合集磁力补 E04，单集磁力按「只下合集包」不下；第二轮结算入库；
// 离线失败后第三轮不再重试那条磁力（再提交要再扣一次离线配额）
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
		{Source: "tg", Kind: "magnet", Action: "offline", Title: "剧 S01 E03-E04 1080p", URL: magnet, Relevant: true, Rank: -1},
		{Source: "tg", Kind: "magnet", Action: "offline", Title: "剧 S01E04 2160p", URL: "magnet:?xt=urn:btih:6666666666666666666666666666666666666666", Relevant: true, Rank: -1},
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
	if item.Skipped != "跳过 1 条资源（单集磁力 1）" {
		t.Fatalf("单集磁力应按策略跳过并说明: %q", item.Skipped)
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
		t.Fatalf("失败过的磁力不该再试: %+v", item)
	}
	model.DB.First(&atts[1], atts[1].ID)
	if atts[1].Status != subAttemptFailed || atts[1].RetryAt != nil {
		t.Fatalf("115 报离线失败的不设重试: %+v", atts[1])
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

// 离线的几道闸：只下合集包、一轮一个、新集先等分享、每月上限、115 配额保留
func TestSubOfflineGates(t *testing.T) {
	newTestDB(t, "suboffline.db")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	e := func(n int) epKey { return epKey{S: 1, E: n} }
	old := now.AddDate(0, 0, -30)
	ev := subEval{Missing: []epKey{e(1), e(2), e(3), e(4)}, MissingAir: map[epKey]time.Time{
		e(1): old, e(2): old, e(3): old, e(4): now.Add(-2 * time.Hour), // E04 刚播
	}}
	mag := func(title, hash string) ResourceItem {
		return ResourceItem{Source: "tg", Kind: "magnet", Action: "offline", Title: title,
			URL: "magnet:?xt=urn:btih:" + strings.Repeat(hash, 40), Relevant: true, Rank: -1}
	}
	items := []ResourceItem{mag("剧 S01 E01-E02", "a"), mag("剧 S01 E03-E04", "b"), mag("剧 S01E03", "c")}

	type res struct {
		offlines []string
		item     subRunItem
	}
	run := func(subID uint, ev subEval, quotaLeft int) res {
		var out res
		r := &subRunner{
			h: &Handler{DB: model.DB}, cfg: defaultSubscribeCfg(), target: "T", re0Left: -1,
			now:    func() time.Time { return now },
			search: func(*model.Subscription) ([]ResourceItem, string) { return append([]ResourceItem(nil), items...), "" },
			mkdir:  func(parent, name string) (string, error) { return "W", nil },
			offline: func(link, target string) (int, string, uint) {
				out.offlines = append(out.offlines, link)
				return 200, "ok", uint(len(out.offlines))
			},
			quota: func() (int, int, error) { return quotaLeft, 100, nil },
		}
		sub := &model.Subscription{ID: subID, TmdbID: int(subID), MediaType: "tv", Title: "剧", Scope: subScopeAll}
		r.searchAndSubmit(sub, ev, &out.item)
		return out
	}

	// 能补的集多的先下（E01-E02 两集都播够了；E03-E04 只有 E03 播够），一轮只下一个，单集磁力不下
	got := run(1, ev, 50)
	if len(got.offlines) != 1 || !strings.Contains(got.offlines[0], "aaaa") {
		t.Fatalf("应只下 E01-E02 那个合集: %v", got.offlines)
	}
	if got.item.Skipped != "跳过 2 条资源（一轮只下一个 1、单集磁力 1）" {
		t.Fatalf("跳过说明: %q", got.item.Skipped)
	}

	// 只缺刚播的 E04：先等分享
	young := subEval{Missing: []epKey{e(4)}, MissingAir: ev.MissingAir}
	if got = run(2, young, 50); len(got.offlines) != 0 || !strings.Contains(got.item.Skipped, "新集先等分享") {
		t.Fatalf("刚播的集不该下离线: %+v", got)
	}

	// 115 配额低于保留值：不下
	if got = run(3, ev, 5); len(got.offlines) != 0 || !strings.Contains(got.item.Skipped, "115 离线配额只剩 5") {
		t.Fatalf("配额不足不该下: %+v", got)
	}

	// 本月用到上限（默认 30，上面已经提交过 1 个）：不下
	for i := 0; i < 29; i++ {
		model.DB.Create(&model.SubAttempt{SubID: 99, Kind: "offline", LinkID: uint(1000 + i), Status: subAttemptIngested, CreatedAt: now})
	}
	// 被 115 拒掉的提交没扣配额，不算
	model.DB.Create(&model.SubAttempt{SubID: 99, Kind: "offline", Status: subAttemptFailed, CreatedAt: now})
	if n := subOfflineUsedThisMonth(model.DB, now); n != 30 {
		t.Fatalf("本月用量应是 30: %d", n)
	}
	if got = run(4, ev, 50); len(got.offlines) != 0 || !strings.Contains(got.item.Skipped, "到了上限") {
		t.Fatalf("到了每月上限不该下: %+v", got)
	}
}

func TestSubCreatedText(t *testing.T) {
	tv := &model.Subscription{Title: "三体", Year: "2023", MediaType: "tv", Scope: subScopeSeason, Season: 1, Follow: subFollowMissing}
	ev := subEval{Have: 3, Total: 10, Missing: make([]epKey, 7)}
	if s := subCreatedText(tv, ev, nil); s != "订阅《三体》（2023）：第 1 季 · 补缺集\n已有 3 / 10 集，缺 7 集，开始找资源" {
		t.Fatalf("剧集: %q", s)
	}
	next := time.Date(2026, 10, 12, 0, 0, 0, 0, time.Local)
	if s := subCreatedText(tv, subEval{Have: 10, Total: 10, NextAt: next}, nil); s != "订阅《三体》（2023）：第 1 季 · 补缺集\n已播的 10 集都有了，下一集 10-12 播出后开始找" {
		t.Fatalf("不缺: %q", s)
	}
	mv := &model.Subscription{Title: "片", MediaType: "movie"}
	if s := subCreatedText(mv, subEval{Total: 0, Unaired: 1, NextAt: next}, nil); s != "订阅《片》：电影\n还没到发行日期，10-12 开始找" {
		t.Fatalf("电影: %q", s)
	}
}
