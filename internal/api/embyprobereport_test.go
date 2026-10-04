package api

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// startTestExtractWorker 测试里的探测 worker：和线上同一条 embyExtractPath，只是 cfg 用假 Emby。
// 先占掉 once，别让真 worker 在这个进程里被拉起来抢队列
func startTestExtractWorker(t *testing.T, cfg embyRefreshCfg) {
	t.Helper()
	embyExtractQ.once.Do(func() {})
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
			}
			if e, ok := embyExtractPop(); ok {
				embyExtractPath(cfg, *e)
				continue
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { close(done); <-finished })
}

// seedEmbyCfg 探测任务开头会检查「配没配 Emby」
func seedEmbyCfg(t *testing.T, url string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"server_url": url, "api_key": "k"})
	if err := model.DB.Save(&model.Setting{Key: "emby", Value: string(b)}).Error; err != nil {
		t.Fatal(err)
	}
}

func runProbeJob(t *testing.T, spec probeJobSpec) model.TaskJob {
	t.Helper()
	job, err := enqueueProbeJob(model.DB, spec)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: model.DB}
	h.runJob(&job, probeLane)
	return jobStatus(t, job.ID)
}

func withFastProbePoll(t *testing.T) {
	prev, prevDeb := probeJobPoll, embyExtractDebounce
	probeJobPoll = 5 * time.Millisecond
	t.Cleanup(func() { probeJobPoll, embyExtractDebounce = prev, prevDeb })
}

// 手动探测任务：全部失败 → 任务失败，每条带原因、次数、防抖过后才能重试的时间
func TestProbeJobAllFailed(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	f := newFakeExtractEmby(t, false, "ep1", "ep2")
	seedEmbyCfg(t, f.srv.URL)
	startTestExtractWorker(t, f.cfg())

	before := time.Now()
	got := runProbeJob(t, probeJobSpec{Title: "探《某剧》", Paths: []string{"/media/某剧"}})
	if got.Status != jobFailed {
		t.Fatalf("全部失败应为失败，实际 %s（%s）", got.Status, got.Message)
	}
	r := probeReportOf(&got)
	if r == nil || r.State != "done" || r.Planned != 2 || r.Failed != 2 || len(r.Items) != 2 {
		t.Fatalf("报告不对：%+v", r)
	}
	it := r.Items[0]
	if it.Label != "某剧 S01E01" || it.Kind != probeItemFailed || it.Err != "HTTP 500" || it.Attempts != 1 {
		t.Fatalf("失败条目要带称呼、原因与次数：%+v", it)
	}
	if it.RetryAt == nil || it.RetryAt.Before(before.Add(embyExtractDebounce-time.Second)) {
		t.Fatalf("要给出防抖过后能再手动请求的时间：%v", it.RetryAt)
	}
	if m, _ := embyExtractLoad("ep1"); m.Label != "某剧 S01E01" {
		t.Fatalf("记账要存条目称呼（全局失败清单用）：%+v", m)
	}

	// 防抖期内马上重试：不请求，报成跳过；防抖过了再重试：真的请求
	again := runProbeJob(t, probeJobSpec{Title: "重试", Paths: []string{"/media/某剧"}})
	if f.totalCalls() != 2 {
		t.Fatalf("防抖期内不能再请求，实际 %v", f.calls)
	}
	if r2 := probeReportOf(&again); again.Status != jobSuccess || r2 == nil || r2.Held != 2 || r2.Items[0].RetryAt == nil {
		t.Fatalf("防抖跳过要说清什么时候能再试：%s %+v", again.Status, r2)
	}
	embyExtractDebounce = 0
	runProbeJob(t, probeJobSpec{Title: "再重试", Paths: []string{"/media/某剧"}})
	if f.totalCalls() != 4 {
		t.Fatalf("防抖过了手动重试应真的请求（不受 24 小时限制），实际 %v", f.calls)
	}
}

// 全部成功 → 完成；已有媒体信息的不请求
func TestProbeJobSuccess(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	f := newFakeExtractEmby(t, true, "ep1")
	seedEmbyCfg(t, f.srv.URL)
	startTestExtractWorker(t, f.cfg())
	got := runProbeJob(t, probeJobSpec{Title: "探", Paths: []string{"/media/某剧"}})
	r := probeReportOf(&got)
	if got.Status != jobSuccess || r == nil || r.OK != 1 || len(r.Items) != 0 || r.State != "done" {
		t.Fatalf("全部成功：%s %+v", got.Status, r)
	}
	if f.calls["has"] != 0 || f.calls["iso"] != 0 {
		t.Fatalf("已有媒体信息 / 光盘结构不请求：%v", f.calls)
	}
}

// 失败清单里点名重试：条目在 Emby 里没了就清掉记账
func TestProbeJobItemGone(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
	}))
	t.Cleanup(srv.Close)
	seedEmbyCfg(t, srv.URL)
	startTestExtractWorker(t, embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"})
	model.DB.Create(&model.EmbyExtractMark{ItemID: "old", Attempts: 2, LastAt: time.Now().Add(-48 * time.Hour), Label: "旧剧 S01E01"})

	got := runProbeJob(t, probeJobSpec{Title: "重试", Items: []string{"old"}})
	r := probeReportOf(&got)
	if r == nil || r.Missing != 1 || r.Items[0].Label != "旧剧 S01E01" {
		t.Fatalf("条目没了要说出来：%+v", r)
	}
	if _, ok := embyExtractLoad("old"); ok {
		t.Fatal("Emby 里没了的条目，失败记账要清掉")
	}
}

// 查 Emby 出错：写回原因、任务失败（此前只进详细日志）
func TestProbeJobEmbyError(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("Path") != "" {
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{{"Id": "dir1", "Type": "Folder", "Path": q.Get("Path")}}})
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	seedEmbyCfg(t, srv.URL)
	startTestExtractWorker(t, embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"})
	got := runProbeJob(t, probeJobSpec{Title: "探", Paths: []string{"/media/新片"}})
	r := probeReportOf(&got)
	if got.Status != jobFailed || r == nil || r.Errors != 1 || r.Items[0].Err != "查询 Emby 条目失败: HTTP 500" {
		t.Fatalf("查 Emby 出错要写回原因并失败：%s %+v", got.Status, r)
	}
}

// 停止：排着的路径摘掉，任务记成已取消
func TestProbeJobCancelDropsQueued(t *testing.T) {
	resetExtractState(t)
	embyExtractQ.once.Do(func() {}) // 不起 worker：路径一直排着
	for {
		if _, ok := embyExtractPop(); !ok {
			break
		}
	}
	queueEmbyExtract("/media/自动")
	queueEmbyExtractFor(7, "/media/自动", "/media/手动")
	cancelEmbyExtractJob(7)
	defer forgetEmbyExtractCancel(7)
	var got []embyExtractEntry
	for {
		e, ok := embyExtractPop()
		if !ok {
			break
		}
		got = append(got, *e)
	}
	// 自动入口也排过的留下（改回自动规则），只有手动任务排的拿掉
	if len(got) != 1 || got[0].path != "/media/自动" || got[0].manual || len(got[0].jobs) != 0 {
		t.Fatalf("停止后队列不对：%+v", got)
	}
}

// 重新整理登记过的片目：入库确认到它时从自动入口摘出来、另建手动探测任务
func TestSplitRedoProbes(t *testing.T) {
	resetExtractState(t)
	seedEmbyCfg(t, "http://emby")
	registerRedoProbe("/media/剧集/某剧", "某剧", true)
	auto := splitRedoProbes([]string{"/media/剧集/某剧/Season 01/S01E01.strm", "/media/电影/别的"})
	if len(auto) != 1 || auto[0] != "/media/电影/别的" {
		t.Fatalf("只摘出登记过的片目：%v", auto)
	}
	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindProbe).Find(&jobs)
	if len(jobs) != 1 || decodeJobParams(&jobs[0]).Probe.Paths[0] != "/media/剧集/某剧" || jobs[0].Priority != jobPriorityManual {
		t.Fatalf("应按片目建一个手动探测任务：%+v", jobs)
	}
	// 用过就清：之后同一片目的入库确认走自动入口
	if auto := splitRedoProbes([]string{"/media/剧集/某剧/Season 01/S01E02.strm"}); len(auto) != 1 {
		t.Fatalf("登记只用一次：%v", auto)
	}
}

// 路径没探完任务就结束了（服务重启）：显示「中断」
func TestProbeReportLostAfterRestart(t *testing.T) {
	resetExtractState(t)
	job := model.TaskJob{Kind: jobKindProbe, Title: "a", Status: jobInterrupted}
	model.DB.Create(&job)
	b, _ := json.Marshal(jobProbeReport{Paths: 3, Finished: 1})
	model.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("probe", string(b))
	got := jobStatus(t, job.ID)
	if r := probeReportOf(&got); r == nil || r.State != "lost" {
		t.Fatalf("应判为中断：%+v", r)
	}
}

// 失败清单：第一次探、结果还没回来的条目不算失败，不列出；失败过再重试的列出并显示探测中；
// 记着「请求中」却没人在探的（服务重启过）显示中断（2026-09-30 现场：探一集清单里闪一条「请求中断」）
func TestProbeFailListSkipsFirstInFlight(t *testing.T) {
	resetExtractState(t)
	now := time.Now()
	for _, m := range []model.EmbyExtractMark{
		{ItemID: "fresh", Label: "新集", Attempts: 1, LastAt: now, LastErr: embyExtractPending},
		{ItemID: "stale", Label: "断掉的", Attempts: 1, LastAt: now.Add(-time.Hour), LastErr: embyExtractPending},
		{ItemID: "old", Label: "失败过", Attempts: 1, LastAt: now.Add(-2 * time.Hour), LastErr: "HTTP 500"},
	} {
		embyExtractStore(m)
	}
	list := func() (rows []probeFailRow, total int) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/tasks/probe", nil)
		(&Handler{DB: model.DB}).EmbyProbeStatus(c)
		var out struct {
			Fails []probeFailRow `json:"fails"`
			Total int            `json:"fail_total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Fails, out.Total
	}
	byID := func(rows []probeFailRow) map[string]probeFailRow {
		m := map[string]probeFailRow{}
		for _, r := range rows {
			m[r.ItemID] = r
		}
		return m
	}

	setEmbyExtractRunning("fresh", "新集")
	t.Cleanup(func() { setEmbyExtractRunning("", "") })
	rows, total := list()
	got := byID(rows)
	if _, ok := got["fresh"]; ok || total != 2 || len(rows) != 2 {
		t.Fatalf("第一次探、还在探的不该进失败清单：total=%d %+v", total, rows)
	}
	if got["stale"].LastErr != "请求中断（服务重启）" {
		t.Fatalf("没人在探的「请求中」要显示中断：%+v", got["stale"])
	}

	// 失败过的条目再探：列出、显示探测中
	embyExtractClaim("old", "失败过", now, true)
	setEmbyExtractRunning("old", "失败过")
	got = byID(func() []probeFailRow { r, _ := list(); return r }())
	if r := got["old"]; !r.Running || r.LastErr != embyExtractPending {
		t.Fatalf("失败过再重试的要列出并显示探测中：%+v", r)
	}
	if _, ok := got["fresh"]; !ok {
		t.Fatal("不在探了的「请求中」要列出来（中断）")
	}
}

// 指定季集登记的是那几个 .strm：入库确认到它们时只为它们建任务，不整部探；
// 同一批文件的入库确认再来（迟到的 Emby 入库事件）也不落进自动入口
func TestSplitRedoProbesFiles(t *testing.T) {
	resetExtractState(t)
	seedEmbyCfg(t, "http://emby")
	files := []string{"/media/剧集/某剧/Season 02/某剧.S02E720.strm", "/media/剧集/某剧/Season 02/某剧.S02E480.strm"}
	registerRedoProbeFiles(files, "某剧", true)
	auto := splitRedoProbes([]string{files[0], "/media/剧集/某剧/Season 03/某剧.S03E01.strm"})
	if len(auto) != 1 || auto[0] != "/media/剧集/某剧/Season 03/某剧.S03E01.strm" {
		t.Fatalf("同片目的其他集不该被摘走：%v", auto)
	}
	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindProbe).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("应建一个探测任务：%+v", jobs)
	}
	if p := decodeJobParams(&jobs[0]).Probe.Paths; len(p) != 2 || strings.Contains(strings.Join(p, ","), "Season 03") {
		t.Fatalf("只探登记的两集：%v", p)
	}
	// 迟到的入库确认：照样摘掉（不整部探），也不再建第二个任务
	if auto := splitRedoProbes([]string{files[1]}); len(auto) != 0 {
		t.Fatalf("迟到的确认不该落进自动入口：%v", auto)
	}
	var n int64
	model.DB.Model(&model.TaskJob{}).Where("kind = ?", jobKindProbe).Count(&n)
	if n != 1 {
		t.Fatalf("不该重复建任务：%d", n)
	}
}
