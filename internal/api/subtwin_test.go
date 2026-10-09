package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"115-station/internal/model"
)

// 2026-10-08 现场：订阅《凡人修仙传》(2020，动画)，同名的还有 2025 年 30 集的真人剧
func TestSubTwinVerdict(t *testing.T) {
	anime := subIdentity{Anime: true, Year: "2020", Twins: []subTwin{{ID: 999, Kind: "tv", Title: "凡人修仙传", Year: "2025", Episodes: 30}}}
	cases := []struct {
		title string
		id    subIdentity
		ok    bool
	}{
		{"凡人修仙传 真人版.全集打包.2160p.60fps.HD国语中字无水印.mp4", anime, false}, // 现场那一条
		{"凡人修仙传 4K 全集", anime, false},                              // 看不出是哪一部
		{"凡人修仙传 2025 4K", anime, false},                            // 2025 两部都在播，不算证据
		{"凡人修仙传 动漫 4K", anime, true},                               // 写明动漫
		{"凡人修仙传 2160p 更新至194集", anime, true},                       // 集号超出真人版的 30 集
		{"凡人修仙传 第190-194集", anime, true},
		{"凡人修仙传 2020 1080p", anime, true}, // 只属于动画的首播年份
		{"凡人修仙传 第25集", anime, false},      // 两部都有第 25 集
		// 没有同名的：只拦写明了另一种形态的
		{"凡人修仙传 真人版 4K", subIdentity{Anime: true, Year: "2020"}, false},
		{"凡人修仙传 4K", subIdentity{Anime: true, Year: "2020"}, true},
		// 订阅真人剧、同名的是动画
		{"凡人修仙传 动画版 4K", subIdentity{Year: "2025", Twins: []subTwin{{Title: "凡人修仙传", Year: "2020", Anime: true, Episodes: 194}}}, false},
		{"凡人修仙传 真人版 4K", subIdentity{Year: "2025", Twins: []subTwin{{Title: "凡人修仙传", Year: "2020", Anime: true, Episodes: 194}}}, true},
		{"凡人修仙传 4K", subIdentity{Year: "2025", Twins: []subTwin{{Title: "凡人修仙传", Year: "2020", Anime: true, Episodes: 194}}}, false},
		// 同名那部集数不知道：集号不能当证据
		{"凡人修仙传 更新至194集", subIdentity{Anime: true, Year: "2020", Twins: []subTwin{{Title: "凡人修仙传", Year: "2025"}}}, false},
	}
	for _, c := range cases {
		ok, why := subTwinVerdict(c.title, c.id, resCoverageOf(c.title))
		if ok != c.ok {
			t.Errorf("%q = %v（%s），want %v", c.title, ok, why, c.ok)
		}
	}
}

func TestSubIdentityOf(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/tv":
			fmt.Fprint(w, `{"results":[
				{"id":106449,"name":"凡人修仙传","original_name":"凡人修仙传","first_air_date":"2020-07-25","genre_ids":[16,10759]},
				{"id":999,"name":"凡人修仙传","original_name":"凡人修仙传","first_air_date":"2025-07-27","genre_ids":[18]},
				{"id":555,"name":"凡人修仙传之外海风云","first_air_date":"2023-01-01","genre_ids":[16]}]}`)
		case "/search/movie":
			fmt.Fprint(w, `{"results":[]}`)
		case "/tv/999":
			fmt.Fprint(w, `{"status":"Ended","seasons":[{"season_number":0,"episode_count":2},{"season_number":1,"episode_count":30}]}`)
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	resetTmdbCaches()
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}
	sub := &model.Subscription{TmdbID: 106449, MediaType: "tv", Title: "凡人修仙传", Year: "2020"}
	id, err := subIdentityOf(tc, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Anime || len(id.Twins) != 1 || id.Twins[0].ID != 999 || id.Twins[0].Anime || id.Twins[0].Episodes != 30 {
		t.Fatalf("应认出自己是动画、同名的只有 2025 真人版 30 集（外海风云片名不同不算）: %+v", id)
	}
}

// 挑资源：真人版的磁力与看不出是哪一部的分享都不要，原因写明
func TestPlanSubCandidatesTwin(t *testing.T) {
	sub := &model.Subscription{ID: 1, MediaType: "tv", Title: "凡人修仙传"}
	missing := []epKey{{S: 1, E: 190}, {S: 1, E: 191}}
	item := func(action, title, hash string) ResourceItem {
		r := ResourceItem{Source: "tg", Kind: "share115", Action: action, Title: title, URL: "https://115.com/s/" + hash, Relevant: true, Rank: 0, Tags: resTagsOf(title)}
		if action == "offline" {
			r.Kind, r.URL = "magnet", "magnet:?xt=urn:btih:"+hash
		}
		return r
	}
	items := []ResourceItem{
		item("offline", "凡人修仙传 真人版.全集打包.2160p.60fps.HD国语中字无水印.mp4", "aaa"),
		item("transfer", "凡人修仙传 4K 全集", "bbb"),
		item("transfer", "凡人修仙传 更新至191集 4K", "ccc"),
	}
	id := subIdentity{Anime: true, Year: "2020", Twins: []subTwin{{ID: 999, Kind: "tv", Title: "凡人修仙传", Year: "2025", Episodes: 30}}}
	skipped := map[string]int{}
	cands, _ := planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: time.Now(), Re0Left: -1, Identity: &id, Skipped: skipped})
	if len(cands) != 1 || cands[0].Hash != "ccc" {
		t.Fatalf("只该留下写了 191 集的: %+v", cands)
	}
	if skipped["标题写着真人版，订阅的是动画"] != 1 || skipped["同名的还有《凡人修仙传》(2025)，标题看不出是哪一部"] != 1 {
		t.Fatalf("跳过原因: %v", skipped)
	}
}

// RE0 是按 TMDB 编号列的资源，不过同名检查：标题再看不出是哪一部也是这一部
func TestPlanSubCandidatesTwinSkipsRe0(t *testing.T) {
	sub := &model.Subscription{ID: 1, MediaType: "tv", Title: "凡人修仙传"}
	missing := []epKey{{S: 1, E: 190}}
	zero := 0
	items := []ResourceItem{
		{Source: "re0", Kind: "share115", Action: "unlock", Title: "凡人修仙传 4K 全集", Ref: "slug1", Relevant: true, Points: &zero},
		{Source: "tg", Kind: "share115", Action: "transfer", Title: "凡人修仙传 4K 全集", URL: "https://115.com/s/bbb", Relevant: true},
	}
	id := subIdentity{Anime: true, Year: "2020", Twins: []subTwin{{ID: 999, Kind: "tv", Title: "凡人修仙传", Year: "2025", Episodes: 30}}}
	cands, _ := planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: time.Now(), Re0Left: -1, Identity: &id})
	if len(cands) != 1 || cands[0].Item.Source != "re0" {
		t.Fatalf("RE0 的该留下、TG 的照旧跳过: %+v", cands)
	}
}
