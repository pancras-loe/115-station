package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 预看分享：标题写着单集的排在「剧名 4K」前面，但后者其实是整季包。
// 先列文件再排，整季包一次转完，单集分享不用再转；一集都补不上的当场记 useless；每个分享只列一次
func TestSubPreviewShares(t *testing.T) {
	newTestDB(t, "subpreview.db")
	subTestLayout(t)
	resetTmdbCaches()
	scrapeSeasonMu.Lock()
	scrapeSeasonCache = map[string]seasonCacheEntry{}
	scrapeSeasonMu.Unlock()

	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/600":
			fmt.Fprint(w, `{"status":"Returning Series","seasons":[{"season_number":1,"episode_count":4}]}`)
		case "/tv/600/season/1":
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"air_date":"2020-01-01"},{"episode_number":2,"air_date":"2020-01-08"},{"episode_number":3,"air_date":"2020-01-15"},{"episode_number":4,"air_date":"2020-01-22"}]}`)
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	defer tmdb.Close()

	snaps := map[string]int{}
	var received []url.Values
	pan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share/snap":
			code := r.URL.Query().Get("share_code")
			snaps[code]++
			switch code {
			case "pvone": // 标题写单集，里面也只有 E02
				fmt.Fprint(w, `{"state":true,"data":{"list":[{"fid":"501","cid":"0","n":"剧.S01E02.mkv","s":10,"sha":"B2"}]}}`)
			case "pvpack": // 标题看不出，其实是整季
				fmt.Fprint(w, `{"state":true,"data":{"list":[
					{"fid":"601","cid":"0","n":"剧.S01E01.mkv","s":10,"sha":"C1"},
					{"fid":"602","cid":"0","n":"剧.S01E02.mkv","s":10,"sha":"C2"},
					{"fid":"603","cid":"0","n":"剧.S01E03.mkv","s":10,"sha":"C3"},
					{"fid":"604","cid":"0","n":"剧.S01E04.mkv","s":10,"sha":"C4"}]}}`)
			case "pvnone": // 只有库里已有的 E01
				fmt.Fprint(w, `{"state":true,"data":{"list":[{"fid":"701","cid":"0","n":"剧.S01E01.mkv","s":10,"sha":"D1"}]}}`)
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
	subTestVideo("媒体库/剧集/剧.2020.{tmdbid=600}/Season 01/剧.S01E01.strm")
	invalidateLedgerTitles()

	sub := model.Subscription{TmdbID: 600, MediaType: "tv", Title: "剧", Year: "2020", Scope: subScopeAll, Follow: subFollowMissing, State: subStateActive}
	model.DB.Create(&sub)

	share := func(title, code string) ResourceItem {
		return ResourceItem{Source: "pansou", Kind: "share115", Action: "transfer", Title: title, URL: "https://115.com/s/" + code, Relevant: true, Rank: 0}
	}
	items := []ResourceItem{share("剧 S01E02", "pvone"), share("剧 S01", "pvnone"), share("剧 4K", "pvpack")}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	var dirs []string
	r := &subRunner{
		h: h, cfg: defaultSubscribeCfg(), target: "T", cookie: "UID=1", re0Left: -1,
		tc:     &TmdbClient{APIKey: "k", APIURL: tmdb.URL, Language: "zh-CN", httpClient: tmdb.Client()},
		now:    func() time.Time { return now },
		search: func(*model.Subscription) ([]ResourceItem, string) { return append([]ResourceItem(nil), items...), "" },
		mkdir: func(parent, name string) (string, error) {
			dirs = append(dirs, name)
			return fmt.Sprintf("W%d", len(dirs)), nil
		},
		offline: func(string, string) (int, string, uint) { t.Error("不该离线"); return 500, "", 0 },
		rmdir:   func(string) error { return nil },
	}

	item, ran := r.run(&sub, false)
	if !ran || len(item.Submitted) != 1 {
		t.Fatalf("整季包一条就够: %+v", item)
	}
	if len(received) != 1 || received[0].Get("file_id") != "602,603,604" {
		t.Fatalf("应只从整季包转 E02–E04: %v", received)
	}
	if want := map[string]int{"pvone": 1, "pvpack": 1, "pvnone": 1}; !reflect.DeepEqual(snaps, want) {
		t.Fatalf("每个分享只列一次（转存用预看的结果）: %v", snaps)
	}
	var atts []model.SubAttempt
	model.DB.Where("sub_id = ?", sub.ID).Order("id").Find(&atts)
	byHash := map[string]string{}
	for _, a := range atts {
		byHash[a.Hash] = a.Status
	}
	if len(atts) != 2 || byHash[subResHash(items[1])] != subAttemptUseless || byHash[subResHash(items[2])] != subAttemptInflight {
		t.Fatalf("没用的当场记 useless、整季包在路上、单集那条没碰: %+v", atts)
	}

	// 关掉预看：回到按标题排，单集分享先转
	resetSubShareWalks()
	model.DB.Where("sub_id = ?", sub.ID).Delete(&model.SubAttempt{})
	received, snaps = nil, map[string]int{}
	r.cfg.SharePreviewMax = 0
	sub.NextCheckAt = nil
	r.run(&sub, true)
	if len(received) != 2 || received[0].Get("file_id") != "501" {
		t.Fatalf("不预看时按标题排，单集先转: %v", received)
	}
}

// 能补的集数一样时：补到更接近最新一集的在前，再看资源里集数多的
func TestSortSubCandidatesReachSpan(t *testing.T) {
	cs := []subCand{
		{Hash: "short", Covers: 2, Reach: epKey{1, 3}, Span: 2},
		{Hash: "long", Covers: 2, Reach: epKey{1, 3}, Span: 12},
		{Hash: "newest", Covers: 2, Reach: epKey{1, 4}, Span: 2},
		{Hash: "more", Covers: 3, Reach: epKey{1, 3}},
	}
	for i := range cs {
		cs[i].Item = ResourceItem{Kind: "share115", Action: "transfer"}
	}
	sortSubCandidates(cs)
	var got []string
	for _, c := range cs {
		got = append(got, c.Hash)
	}
	if want := []string{"more", "newest", "long", "short"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("顺序 = %v, want %v", got, want)
	}
}
