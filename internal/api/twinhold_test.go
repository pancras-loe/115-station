package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"
)

// choose：片名相等、得分也一样的不止一条时记下另外几条；年份分得出来、或另一条是空壳时不记
func TestChooseRecordsTwins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`) // 详情：choose 采用后取一次，内容这里用不上
	}))
	defer srv.Close()
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}

	cands := []tmdbCand{
		{ID: 106449, Title: "凡人修仙传", Original: "凡人修仙传", Date: "2020-07-25", Poster: "/a.jpg"},
		{ID: 999, Title: "凡人修仙传", Original: "凡人修仙传", Date: "2025-07-27", Poster: "/b.jpg"},
		{ID: 555, Title: "凡人修仙传", Date: "2019-01-01"}, // 空壳：没海报也没简介
		{ID: 777, Title: "凡人修仙传之外海风云", Date: "2023-01-01", Poster: "/c.jpg"},
	}
	c, _, how, err := tc.choose(tmdbPick{kind: "tv", query: "凡人修仙传"}, cands)
	if err != nil || c == nil || how != chooseHowExact {
		t.Fatalf("应过第一关: %+v %s %v", c, how, err)
	}
	if c.ID != 106449 || len(c.twins) != 1 || c.twins[0].ID != 999 || c.twins[0].Year != "2025" {
		t.Fatalf("没年份：应取相关度第一的、记下 2025 那部（空壳与片名不同的不算）: %+v", c)
	}
	if m := c.media("tv", nil); len(m.Twins) != 1 {
		t.Fatalf("同名信息要带进识别结果: %+v", m)
	}

	c, _, _, _ = tc.choose(tmdbPick{kind: "tv", query: "凡人修仙传", year: "2025"}, cands)
	if c == nil || c.ID != 999 || len(c.twins) != 0 {
		t.Fatalf("年份分得出来就不算同名同分: %+v", c)
	}
	// 年份跟谁都对不上：一样分不出来
	c, _, _, _ = tc.choose(tmdbPick{kind: "tv", query: "凡人修仙传", year: "2010"}, cands)
	if c == nil || len(c.twins) != 1 {
		t.Fatalf("年份都对不上也是同名同分: %+v", c)
	}
}

// Oasis.2002（李沧东《绿洲》，中文标题「绿洲」、原名「오아시스」）：搜「Oasis」有一串片名相等的同名片，
// 年份都不对。真正那部别名是 Oasis，排在前 5 名以外、或者搜索结果给的是重映日期，都要认出来
func TestChooseSameYearAliasBeyondTop5(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/26955":
			fmt.Fprint(w, `{"release_date":"2002-08-15","alternative_titles":{"titles":[{"title":"Oasis"}]}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}

	same := func(real tmdbCand, at int) []tmdbCand {
		var cands []tmdbCand
		for i, y := range []string{"2024", "2023", "1955", "2022", "2020", "2017", "2013"} {
			cands = append(cands, tmdbCand{ID: 100 + i, Title: "Oasis", Date: y + "-01-01", Poster: "/p.jpg"})
		}
		return append(cands[:at], append([]tmdbCand{real}, cands[at:]...)...)
	}
	p := tmdbPick{kind: "movie", query: "Oasis", year: "2002"}

	c, _, how, _ := tc.choose(p, same(tmdbCand{ID: 26955, Title: "绿洲", Original: "오아시스", Date: "2002-08-15"}, 6))
	if c == nil || c.ID != 26955 || len(c.twins) != 0 {
		t.Fatalf("排在第 7 名的同年别名相等的应采用: %+v %s", c, how)
	}
	c, _, _, _ = tc.choose(p, same(tmdbCand{ID: 26955, Title: "绿洲", Original: "오아시스", Date: "2020-11-06"}, 2))
	if c == nil || c.ID != 26955 || c.year() != "2002" {
		t.Fatalf("搜索结果是重映日期时应按详情的首映认，年份写 2002: %+v", c)
	}
	// 重映日期又在片名不相等的里面排 5 名以外：不再逐条查详情，照旧同名停下
	cands := same(tmdbCand{ID: 1, Title: "x"}, 0)
	for i := 2; i <= 5; i++ {
		cands = append(cands, tmdbCand{ID: i, Title: fmt.Sprintf("x%d", i)})
	}
	cands = append(cands, tmdbCand{ID: 26955, Title: "绿洲", Original: "오아시스", Date: "2020-11-06"})
	c, _, _, _ = tc.choose(p, cands)
	if c == nil || c.ID == 26955 || len(c.twins) == 0 {
		t.Fatalf("应停成同名: %+v", c)
	}
}

// 不带年份那页只选出同名的：再按年份过滤搜一次，选得出没有同名的就用它
func TestSearchPickTwinsTryYearFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search/movie" && r.URL.Query().Get("year") == "2002":
			fmt.Fprint(w, `{"results":[{"id":26955,"title":"绿洲","original_title":"오아시스","release_date":"2002-08-15"}]}`)
		case r.URL.Path == "/search/movie":
			fmt.Fprint(w, `{"results":[{"id":1,"title":"Oasis","release_date":"2024-01-01","poster_path":"/a"},`+
				`{"id":2,"title":"Oasis","release_date":"2023-01-01","poster_path":"/b"}]}`)
		case r.URL.Path == "/movie/26955":
			fmt.Fprint(w, `{"release_date":"2002-08-15","alternative_titles":{"titles":[{"title":"Oasis"}]}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}

	m, err := tc.SearchMovie("Oasis", "2002")
	if err != nil || m == nil || m.TmdbID != 26955 || len(m.Twins) != 0 {
		t.Fatalf("按年份过滤应认出 2002 那部: %+v %v", m, err)
	}
	// 年份过滤那页也认不出：还是同名停下，返回第一页的结果
	m, err = tc.SearchMovie("Oasis", "1999")
	if err != nil || m == nil || m.TmdbID != 1 || len(m.Twins) != 1 {
		t.Fatalf("应退回同名的结果: %+v %v", m, err)
	}
}

// 同名停下：记录标 HoldTwin、原因列出几部；「人工确认」关着也不自动接手；确认之后标记还在
func TestTwinHoldSticky(t *testing.T) {
	newTestDB(t, "twin_hold.db")
	ensureRenameTpl()
	ctx := newConfirmCtx(false)
	media := &TmdbMedia{TmdbID: 106449, Title: "凡人修仙传", Year: "2020", MediaType: "tv",
		Twins: []tmdbTwin{{ID: 999, Title: "凡人修仙传", Year: "2025"}}}
	reason := ctx.twinHold(media)
	if !strings.Contains(reason, "同名的有 2 部") || !strings.Contains(reason, "(2025) tmdb=999") {
		t.Fatalf("原因 = %q", reason)
	}
	name := "凡人修仙传.2160p.60fps"
	ctx.holdForConfirm(name+"/", "d1", "dir", media, parseFileName(name), "01.mp4", nil, reason)

	var rec model.OrganizeRecord
	model.DB.First(&rec)
	if !rec.HoldTwin || rec.Status != orgStatusAwaiting || rec.TmdbID != 106449 {
		t.Fatalf("记录没标成同名停下: %+v", rec)
	}
	held := loadAwaiting()
	if held["d1"] == nil || !held["d1"].twin {
		t.Fatalf("重新加载后丢了同名标记: %+v", held["d1"])
	}
	off := newConfirmCtx(false)
	off.held = held
	if got := off.dropHeld([]dirEntry{{Fid: "d1"}, {Fid: "other"}}); len(got) != 1 || got[0].Fid != "other" {
		t.Fatalf("开关关着也应跳过同名停下的条目: %+v", got)
	}

	// 人工确认（forced）不再停
	forced := newConfirmCtx(false)
	forced.forced = media
	if r := forced.twinHold(media); r != "" {
		t.Fatalf("人工确认时不该再停: %q", r)
	}

	// 确认写回原记录：同名标记留着（之后重新整理改指定也不写识别记忆）
	sink := &orgSink{batchID: "b3", jobs: map[string]scrapeJob{}}
	sink.reuse = &awaitingRef{id: rec.ID, created: rec.CreatedAt, twin: true}
	sink.note(&model.OrganizeRecord{Source: name + "/", Status: "success", TmdbID: 999})
	model.DB.First(&rec, rec.ID)
	if rec.Status != "success" || rec.TmdbID != 999 || !rec.HoldTwin {
		t.Fatalf("确认后应保留同名标记: %+v", rec)
	}
}

// 「同名待确认」关掉后照旧取第一个，不停
func TestTwinHoldSwitchOff(t *testing.T) {
	ctx := newConfirmCtx(false)
	ctx.cfg.NoTwinHold = true
	media := &TmdbMedia{TmdbID: 1, Title: "X", Twins: []tmdbTwin{{ID: 2, Title: "X"}}}
	if r := ctx.twinHold(media); r != "" || ctx.sink.recog.holdTwin {
		t.Fatalf("开关关着不该停: %q", r)
	}
}
