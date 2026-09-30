package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 挑补刮的片目：刮全的、本地没了的不要；补刮过、缺的没变的冷却期内不要；缺的变了或过了冷却期照补。
// 最近入库的在前，超出上限的留到下次
func TestPlanMetaFillScrape(t *testing.T) {
	now := time.Now()
	ts := []localTitle{
		{Key: "ok", Status: "ok", LastAt: now},
		{Key: "gone", Status: "miss", Missing: true, LastAt: now},
		{Key: "old", Status: "partial", Lack: []string{"背景图"}, LastAt: now.Add(-3 * time.Hour)},
		{Key: "new", Status: "miss", Lack: []string{"NFO", "海报"}, LastAt: now.Add(-time.Hour)},
		{Key: "held", Status: "partial", Lack: []string{"背景图"}, LastAt: now},
		{Key: "changed", Status: "partial", Lack: []string{"3 集 NFO"}, LastAt: now.Add(-2 * time.Hour)},
		{Key: "expired", Status: "partial", Lack: []string{"背景图"}, LastAt: now.Add(-4 * time.Hour)},
	}
	marks := map[string]model.MetaFillMark{
		"held":    {Key: "held", Lack: "partial:背景图", TriedAt: now.Add(-24 * time.Hour)},
		"changed": {Key: "changed", Lack: "partial:1 集 NFO", TriedAt: now.Add(-24 * time.Hour)},
		"expired": {Key: "expired", Lack: "partial:背景图", TriedAt: now.Add(-metaFillRetryAfter - time.Hour)},
	}
	pick, lacking, held := planMetaFillScrape(ts, marks, now, 10)
	var keys []string
	for _, p := range pick {
		keys = append(keys, p.Key)
	}
	if strings.Join(keys, ",") != "new,changed,old,expired" || lacking != 5 || held != 1 {
		t.Fatalf("pick=%v lacking=%d held=%d", keys, lacking, held)
	}
	if pick, _, _ := planMetaFillScrape(ts, marks, now, 2); len(pick) != 2 || pick[1].Key != "changed" {
		t.Fatalf("单次上限：%+v", pick)
	}
}

// 探测按条目点名、按上限截断：上限是准的，一部几百集的剧不会整部挤进来
func TestPlanMetaFillProbe(t *testing.T) {
	now := time.Now()
	items := []metaFillProbeItem{
		{Key: "a", ID: "a2", Label: "A S01E02", lastAt: now.Add(-time.Hour)},
		{Key: "b", ID: "b1", Label: "B", lastAt: now},
		{Key: "a", ID: "a1", Label: "A S01E01", lastAt: now.Add(-time.Hour)},
	}
	got := planMetaFillProbe(items, 2)
	if len(got) != 2 || got[0].ID != "b1" || got[1].ID != "a1" {
		t.Fatalf("%+v", got)
	}
}

// 定时补全建的探测任务按自动规则放行：自动次数用完的不探、24 小时内请求过的不再探；
// 同样的条目手动任务照探（只看防抖）
func TestProbeJobAutoRule(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	f := newFakeExtractEmby(t, false, "ep1", "ep2")
	seedEmbyCfg(t, f.srv.URL)
	startTestExtractWorker(t, f.cfg())
	model.DB.Create(&model.EmbyExtractMark{ItemID: "ep1", Attempts: embyExtractMaxAttempts,
		LastAt: time.Now().Add(-48 * time.Hour), LastErr: "HTTP 500"})

	got := runProbeJob(t, probeJobSpec{Title: "补全", Paths: []string{"/media/某剧"}, Auto: true})
	if f.calls["ep1"] != 0 || f.calls["ep2"] != 1 {
		t.Fatalf("自动规则：次数用完的不探、没探过的探一次，实际 %v", f.calls)
	}
	if r := probeReportOf(&got); r == nil || r.Held != 1 || r.Failed != 1 {
		t.Fatalf("报告：%+v", r)
	}
	if embyExtractIsAutoJob(got.ID) {
		t.Fatal("任务结束后要清掉自动规则标记")
	}

	// 第二天之前又跑一次：ep2 刚请求过，自动规则下不再请求
	runProbeJob(t, probeJobSpec{Title: "补全", Paths: []string{"/media/某剧"}, Auto: true})
	if f.totalCalls() != 1 {
		t.Fatalf("24 小时内不许再自动请求，实际 %v", f.calls)
	}

	// 手动任务不看次数与 24 小时，只防抖
	embyExtractDebounce = 0
	runProbeJob(t, probeJobSpec{Title: "手动", Paths: []string{"/media/某剧"}})
	if f.calls["ep1"] != 1 || f.calls["ep2"] != 2 {
		t.Fatalf("手动任务照探，实际 %v", f.calls)
	}
}

// 同一路径被手动任务与定时补全的任务同时排着：停掉手动的，剩下的改回自动规则，路径不能丢
func TestProbeJobCancelManualKeepsAutoJob(t *testing.T) {
	resetExtractState(t)
	embyExtractQ.once.Do(func() {})
	for {
		if _, ok := embyExtractPop(); !ok {
			break
		}
	}
	queueEmbyExtractJob(11, false, "/media/甲")
	queueEmbyExtractJob(12, true, "/media/甲", "/media/乙")
	defer forgetEmbyExtractAutoJob(12)
	if e := embyExtractQ.queued["/media/甲"]; e == nil || !e.manual {
		t.Fatalf("有手动任务就按手动规则：%+v", e)
	}
	if e := embyExtractQ.queued["/media/乙"]; e == nil || e.manual || e.auto {
		t.Fatalf("只有定时补全排的：不是手动、也不算入库确认：%+v", e)
	}
	cancelEmbyExtractJob(11)
	defer forgetEmbyExtractCancel(11)
	var got []embyExtractEntry
	for {
		e, ok := embyExtractPop()
		if !ok {
			break
		}
		got = append(got, *e)
	}
	if len(got) != 2 || got[0].path != "/media/甲" || got[0].manual || len(got[0].jobs) != 1 || got[0].jobs[0] != 12 {
		t.Fatalf("停掉手动任务后应改回自动规则、留给定时补全的任务：%+v", got)
	}
}

// 端到端：没刮全的片目建刮削任务（只补缺失、不上传、不探测）、冷却期的跳过；
// Emby 里缺媒体信息、自动规则下能探的点名建探测任务（自动规则）；本身零 115 请求
func TestMetaFillJobEndToEnd(t *testing.T) {
	root := t.TempDir()
	ledgerTestDB(t, []model.CategoryRule{{MediaType: "movie", Name: "电影"}}, []model.SyncedFile{
		{FileID: "a", Kind: "video", PickCode: "pa", RelPath: "影视/电影/新片 (2024)/新片.strm"},
		{FileID: "b", Kind: "video", PickCode: "pb", RelPath: "影视/电影/全了 (2020)/全了.strm"},
		{FileID: "c", Kind: "video", PickCode: "pc", RelPath: "影视/电影/缺背景 (2021)/缺背景.strm"},
	})
	for _, n := range []string{"新片 (2024)/新片.strm", "全了 (2020)/全了.strm", "全了 (2020)/全了.nfo",
		"全了 (2020)/poster.jpg", "全了 (2020)/fanart.jpg", "缺背景 (2021)/缺背景.strm", "缺背景 (2021)/缺背景.nfo",
		"缺背景 (2021)/poster.jpg"} {
		touch(t, filepath.Join(root, "影视", "电影", filepath.FromSlash(n)), "x")
	}
	rootSlash := filepath.ToSlash(root)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Library/VirtualFolders/Query":
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		case r.URL.Path == "/Items" && r.URL.Query().Get("Recursive") == "true":
			if r.URL.Query().Get("StartIndex") != "0" {
				json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "m1", "Name": "新片", "Type": "Movie", "Path": rootSlash + "/影视/电影/新片 (2024)/新片.strm"},
				{"Id": "m2", "Name": "全了", "Type": "Movie", "Path": rootSlash + "/影视/电影/全了 (2020)/全了.strm"},
				{"Id": "m3", "Name": "缺背景", "Type": "Movie", "Path": rootSlash + "/影视/电影/缺背景 (2021)/缺背景.strm",
					"MediaStreams": []map[string]string{{"Type": "Video"}, {"Type": "Audio"}}},
				{"Id": "x", "Name": "库外", "Type": "Movie", "Path": "/elsewhere/x.strm"},
			}})
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	full, _ := json.Marshal(map[string]string{"local_path": root})
	emby, _ := json.Marshal(map[string]string{"server_url": srv.URL, "api_key": "k"})
	model.DB.Save(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Save(&model.Setting{Key: "emby", Value: string(emby)})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k"})
	// 缺背景：上次补刮过、缺的还是背景图 → 冷却期内不再刮
	model.DB.Create(&model.MetaFillMark{Key: "影视/电影/缺背景 (2021)", Lack: "partial:背景图", TriedAt: time.Now().Add(-time.Hour)})
	// 全了：探过两次都失败，自动规则下不再探
	model.DB.Create(&model.EmbyExtractMark{ItemID: "m2", Attempts: embyExtractMaxAttempts, LastAt: time.Now().Add(-72 * time.Hour)})

	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	forgetLocalTitles()
	out, err := execMetaFillJob(h, &model.TaskJob{Kind: jobKindMetaFill, Source: "cron", Priority: jobPriorityBackground})
	if err != nil {
		t.Fatal(err)
	}
	res := out.Result.(metaFillResult)
	if res.ScrapeLack != 2 || res.ScrapeHeld != 1 || res.ScrapeQueued != 1 ||
		res.ProbeLack != 2 || res.ProbeHeld != 1 || res.ProbeQueued != 1 || len(res.FollowJobs) != 2 || out.Idle {
		t.Fatalf("结果：%+v idle=%v（%s）", res, out.Idle, out.Message)
	}

	var sj, pj model.TaskJob
	model.DB.First(&sj, res.FollowJobs[0])
	model.DB.First(&pj, res.FollowJobs[1])
	sp, pp := decodeJobParams(&sj).Local, decodeJobParams(&pj).Probe
	if sj.Kind != jobKindScrape || sj.Priority != jobPriorityBackground || sp == nil ||
		strings.Join(sp.Keys, ",") != "影视/电影/新片 (2024)" || sp.Scrape.Force || sp.Scrape.Upload || sp.Scrape.Probe {
		t.Fatalf("刮削任务：%+v %+v", sj, sp)
	}
	if pj.Kind != jobKindProbe || pp == nil || !pp.Auto || strings.Join(pp.Items, ",") != "m1" || len(pp.Paths) != 0 {
		t.Fatalf("探测任务要按条目点名、自动规则：%+v %+v", pj, pp)
	}
	var m model.MetaFillMark
	if model.DB.First(&m, "key = ?", "影视/电影/新片 (2024)").Error != nil || !strings.HasPrefix(m.Lack, "miss:") {
		t.Fatalf("交给刮削的片目要记账：%+v", m)
	}

	// 只开补刮、缺的片目都在冷却期：定时任务什么都没建，不留行
	b, _ := json.Marshal(metaFillCfg{Scrape: true, Cron: "0 4 * * *", MaxTitles: 10, MaxProbe: 10})
	h.Config.SaveSetting(metaFillSetting, string(b))
	forgetLocalTitles()
	out, err = execMetaFillJob(h, &model.TaskJob{Kind: jobKindMetaFill, Source: "cron", Priority: jobPriorityBackground})
	if err != nil || !out.Idle || len(out.Result.(metaFillResult).FollowJobs) != 0 {
		t.Fatalf("没东西可补应空转：%+v %v", out, err)
	}
}
