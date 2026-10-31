package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 分类方案改过之后，早先整理进「纪录片/」（当时是电影分类）的纪录电影，现在落在同名的剧集分类里
// （2026-10-30 用户反馈）：海报墙按电影认、标存疑；刮削不拿电影编号按剧集去「自愈」成同名剧集

func TestMovieInTVCategory(t *testing.T) {
	layout := buildLibCategoryLayout([]model.CategoryRule{
		{MediaType: "movie", Name: "电影/纪录片"},
		{MediaType: "tv", Name: "纪录片"},
	})
	sfs := []model.SyncedFile{
		{RelPath: "影视/纪录片/窗外是蓝星.2025.{tmdbid=1539351}/窗外是蓝星.2025.2160p.WEB-DL.strm"},
		// 真剧集：季目录 + 集号
		{RelPath: "影视/纪录片/地球脉动 (2006)/Season 01/地球脉动 S01E01.strm"},
		// 只有一集、也在季目录里：还是剧集
		{RelPath: "影视/纪录片/某单集纪录片 (2020)/Season 01/某单集纪录片 S01E01.strm"},
		// 两个视频的不猜
		{RelPath: "影视/纪录片/两段 (2021)/上.strm"},
		{RelPath: "影视/纪录片/两段 (2021)/下.strm"},
	}
	got := aggregateLedgerTitles(layout, sfs)
	want := map[string]struct {
		mt  string
		off bool
	}{
		"影视/纪录片/窗外是蓝星.2025.{tmdbid=1539351}": {"movie", true},
		"影视/纪录片/地球脉动 (2006)":                 {"tv", false},
		"影视/纪录片/某单集纪录片 (2020)":               {"tv", false},
		"影视/纪录片/两段 (2021)":                   {"tv", false},
	}
	for k, w := range want {
		e := got[k]
		if e == nil {
			t.Fatalf("没认出片目 %s", k)
		}
		if e.MediaType != w.mt || e.KindOff != w.off {
			t.Errorf("%s: 类型 %s / 放错 %v，期望 %s / %v", k, e.MediaType, e.KindOff, w.mt, w.off)
		}
	}
}

func kindOffTmdb(t *testing.T) *TmdbClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/1539351":
			w.Write([]byte(`{"id":1539351,"title":"窗外是蓝星","original_title":"窗外是蓝星"}`))
		case "/search/tv":
			if strings.Contains(r.URL.Query().Get("query"), "何以当归") {
				w.Write([]byte(`{"results":[{"id":294730,"name":"何以当归之味","original_name":"何以当归之味","first_air_date":"2025-03-01"}]}`))
				return
			}
			w.Write([]byte(`{"results":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"status_code":34}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}
}

func kindOffRun(tc *TmdbClient, title, year string, id int) (*titleRun, *fakeScrapeReporter) {
	rep := &fakeScrapeReporter{}
	sess := newScrapeSession(tc, fileScrapeOpts{WriteNFO: true}, newFileScrapeWriter(nil, false, false), rep)
	r := &titleRun{s: sess, st: &titleScrapeStat{}, tmdbID: id, seasons: map[int]map[int]tmdbEpisodeInfo{},
		t: scrapeTitle{Kind: "tv", Title: title, Year: year, TmdbID: id}}
	return r, rep
}

// 剧集分类里的电影编号：说清楚是放错了分类，不按标题去搜
func TestScrapeKindOffReportsMisplaced(t *testing.T) {
	r, rep := kindOffRun(kindOffTmdb(t), "窗外是蓝星", "2025", 1539351)
	r.run()
	if r.st.Failed != 1 || len(rep.errs) != 1 || !strings.Contains(rep.errs[0], "放错了分类") {
		t.Fatalf("应报放错分类：%v", rep.errs)
	}
	if r.tmdbID != 1539351 {
		t.Errorf("编号被换成了 %d", r.tmdbID)
	}
}

// 按标题搜到的条目片名不同：不替换（原来会悄悄刮成别的剧）
func TestScrapeSelfHealNeedsSameTitle(t *testing.T) {
	r, rep := kindOffRun(kindOffTmdb(t), "何以当归", "2025", 1263063)
	r.run()
	if r.tmdbID != 1263063 {
		t.Fatalf("不该换成 %d", r.tmdbID)
	}
	if r.st.Failed != 1 || len(r.st.Wrote) != 0 {
		t.Fatalf("应失败且不写：failed=%d wrote=%v errs=%v", r.st.Failed, r.st.Wrote, rep.errs)
	}
}
