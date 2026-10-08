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
