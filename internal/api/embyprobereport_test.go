package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"115-station/internal/model"
)

// drainExtractQueue 不起 worker，把队列里的逐条交给 embyExtractPath（和 worker 同一条路）
func drainExtractQueue(cfg embyRefreshCfg) {
	embyExtractQ.once.Do(func() {})
	for {
		e, ok := embyExtractPop()
		if !ok {
			return
		}
		embyExtractPath(cfg, e.path, e.jobs...)
	}
}

func newDoneJob(t *testing.T, title string) model.TaskJob {
	t.Helper()
	job := model.TaskJob{Kind: jobKindScrape, Title: title, Status: jobSuccess}
	if err := model.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

// 探测失败要回到发起它的刮削任务上：原因、下次最早什么时候能再试，任务从「完成」改成「部分失败」
func TestProbeReportFailureMarksJobPartial(t *testing.T) {
	resetExtractState(t)
	f := newFakeExtractEmby(t, false, "ep1", "ep2")
	job := newDoneJob(t, "刮削《某剧》")

	before := time.Now()
	queueEmbyExtractFor(job.ID, "/media/某剧")
	drainExtractQueue(f.cfg())

	got := jobStatus(t, job.ID)
	if got.Status != jobPartial {
		t.Fatalf("探测失败后任务应为部分失败，实际 %s", got.Status)
	}
	r := probeReportOf(&got)
	if r == nil || r.State != "done" || r.Paths != 1 || r.Finished != 1 || r.Failed != 2 || len(r.Items) != 2 {
		t.Fatalf("报告不对：%+v", r)
	}
	it := r.Items[0]
	if it.Label != "某剧 S01E01" || it.Kind != probeItemFailed || it.Err != "HTTP 500" || it.Attempts != 1 || it.Final {
		t.Fatalf("失败条目要带称呼、原因与次数：%+v", it)
	}
	if it.RetryAt == nil || it.RetryAt.Before(before.Add(embyExtractRetryAfter-time.Minute)) {
		t.Fatalf("要给出下次最早能再试的时间（约 %s 后）：%v", embyExtractRetryAfter, it.RetryAt)
	}
	if m, _ := embyExtractLoad("ep1"); m.Label != "某剧 S01E01" {
		t.Fatalf("记账要存条目称呼（全局失败清单用）：%+v", m)
	}

	// 第二个任务又排到同一片目：冷却期内不请求，报成「跳过」并带上次原因与时间，不算这次任务失败
	job2 := newDoneJob(t, "刮削《某剧》again")
	queueEmbyExtractFor(job2.ID, "/media/某剧")
	drainExtractQueue(f.cfg())
	if f.totalCalls() != 2 {
		t.Fatalf("冷却期内不能再请求，实际 %v", f.calls)
	}
	got2 := jobStatus(t, job2.ID)
	r2 := probeReportOf(&got2)
	if got2.Status != jobSuccess || r2 == nil || r2.Held != 2 || r2.Failed != 0 {
		t.Fatalf("冷却中跳过不算失败：%s %+v", got2.Status, r2)
	}
	if h := r2.Items[0]; h.Kind != probeItemHeld || h.Err != "HTTP 500" || h.RetryAt == nil {
		t.Fatalf("跳过的条目要说清上次原因与下次时间：%+v", h)
	}
}

// 成功的只计数，任务保持「完成」
func TestProbeReportSuccessKeepsSuccess(t *testing.T) {
	resetExtractState(t)
	f := newFakeExtractEmby(t, true, "ep1")
	job := newDoneJob(t, "刮削《某剧》")
	queueEmbyExtractFor(job.ID, "/media/某剧")
	drainExtractQueue(f.cfg())
	got := jobStatus(t, job.ID)
	r := probeReportOf(&got)
	if got.Status != jobSuccess || r == nil || r.OK != 1 || len(r.Items) != 0 || r.State != "done" {
		t.Fatalf("全部成功：%s %+v", got.Status, r)
	}
}

// Emby 里还没入库：说出来，不算失败；查 Emby 出错：算失败（此前两种都只进详细日志）
func TestProbeReportMissingAndError(t *testing.T) {
	resetExtractState(t)
	inEmby := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("Path") != "" && inEmby:
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{{"Id": "dir1", "Type": "Folder", "Path": q.Get("Path")}}})
		case q.Get("Path") != "":
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		default: // 列片目下的条目：Emby 出错
			http.Error(w, "down", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}

	job := newDoneJob(t, "a")
	queueEmbyExtractFor(job.ID, "/media/新片")
	drainExtractQueue(cfg)
	got := jobStatus(t, job.ID)
	if r := probeReportOf(&got); got.Status != jobSuccess || r == nil || r.Missing != 1 || r.Items[0].Kind != probeItemMissing {
		t.Fatalf("还没入库：%s %+v", got.Status, r)
	}

	inEmby = true
	job2 := newDoneJob(t, "b")
	queueEmbyExtractFor(job2.ID, "/media/新片")
	drainExtractQueue(cfg)
	got2 := jobStatus(t, job2.ID)
	r := probeReportOf(&got2)
	if got2.Status != jobPartial || r == nil || r.State != "done" || r.Errors != 1 || r.Items[0].Err != "查询 Emby 条目失败: HTTP 500" {
		t.Fatalf("查 Emby 出错要写回原因并标部分失败：%s %+v", got2.Status, r)
	}
}

// 路径没探完、队列里也没有了（服务重启）：显示「中断」，不能一直挂着「排队中」
func TestProbeReportLostAfterRestart(t *testing.T) {
	resetExtractState(t)
	job := newDoneJob(t, "a")
	b, _ := json.Marshal(jobProbeReport{Paths: 3, Finished: 1})
	model.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("probe", string(b))
	got := jobStatus(t, job.ID)
	if r := probeReportOf(&got); r == nil || r.State != "lost" {
		t.Fatalf("应判为中断：%+v", r)
	}
}
