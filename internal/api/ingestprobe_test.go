package api

import (
	"strings"
	"testing"

	"115-station/internal/model"
)

// lastIngestProbeJob 最近一个入库后建的探测任务
func lastIngestProbeJob(t *testing.T) model.TaskJob {
	t.Helper()
	var j model.TaskJob
	if err := model.DB.Where("kind = ? AND source = ?", jobKindProbe, probeSourceIngest).Order("id DESC").First(&j).Error; err != nil {
		t.Fatalf("没建出入库探测任务: %v", err)
	}
	return j
}

// 入库后的自动探测建成任务：按自动规则（第二次 24 小时内不再请求），结果写进任务
func TestIngestProbeJobAutoRule(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	f := newFakeExtractEmby(t, false, "ep1")
	seedEmbyCfg(t, f.srv.URL)
	startTestExtractWorker(t, f.cfg())

	enqueueIngestProbes([]ingestProbeGroup{{name: "某剧", key: "/media/某剧", paths: []string{"/media/某剧"}}})
	job := lastIngestProbeJob(t)
	if p := decodeJobParams(&job).Probe; p == nil || !p.Auto || job.Priority != jobPriorityBackground {
		t.Fatalf("应是后台优先级、自动规则的探测任务：%+v", job)
	}
	h := &Handler{DB: model.DB}
	h.runJob(&job, probeLane)
	if f.totalCalls() != 1 {
		t.Fatalf("第一次应请求 1 次，实际 %v", f.calls)
	}
	if got := jobStatus(t, job.ID); got.Status != jobFailed {
		t.Fatalf("唯一一集失败应记失败，实际 %s（%s）", got.Status, got.Message)
	}

	// 马上又入库一次：自动规则 24 小时内不再请求；什么都没请求就不留行
	enqueueIngestProbes([]ingestProbeGroup{{name: "某剧", key: "/media/某剧", paths: []string{"/media/某剧"}}})
	job2 := lastIngestProbeJob(t)
	h.runJob(&job2, probeLane)
	if f.totalCalls() != 1 {
		t.Fatalf("自动规则下 24 小时内不能再请求，实际 %v", f.calls)
	}
	var n int64
	model.DB.Model(&model.TaskJob{}).Where("id = ?", job2.ID).Count(&n)
	if n != 0 {
		t.Fatalf("一个都没请求的入库探测任务不该留在历史里")
	}
}

// 停止入库探测任务：排着的路径一并拿掉，不再请求
func TestIngestProbeJobCancel(t *testing.T) {
	resetExtractState(t)
	withFastProbePoll(t)
	f := newFakeExtractEmby(t, true, "ep1")
	seedEmbyCfg(t, f.srv.URL)

	enqueueIngestProbes([]ingestProbeGroup{{name: "某剧", key: "/media/某剧", paths: []string{"/media/某剧"}}})
	job := lastIngestProbeJob(t)
	// 没跑 execProbeJob，它收尾时清的内存状态这里自己清：下个用例换新库，任务 id 又从 1 开始
	t.Cleanup(func() {
		forgetEmbyExtractCancel(job.ID)
		forgetEmbyExtractAutoJob(job.ID)
		probeReports.Lock()
		delete(probeReports.m, job.ID)
		probeReports.Unlock()
	})
	queueEmbyExtractJob(job.ID, true, "/media/某剧")
	cancelEmbyExtractJob(job.ID)
	if embyExtractQueueLen() != 0 {
		t.Fatalf("停掉后排着的路径应拿掉，队列里还有 %d 个", embyExtractQueueLen())
	}
	if f.totalCalls() != 0 {
		t.Fatalf("不该请求：%v", f.calls)
	}
}

// 同一部剧排着没开始时又进来一批新集：路径取并集，不能把上一批覆盖掉
func TestIngestProbeJobMergesPaths(t *testing.T) {
	resetExtractState(t)
	enqueueIngestProbes([]ingestProbeGroup{{name: "某剧", key: "/media/某剧", paths: []string{"/media/某剧/S01E01.strm"}}})
	enqueueIngestProbes([]ingestProbeGroup{{name: "某剧", key: "/media/某剧", paths: []string{"/media/某剧/S01E02.strm", "/media/某剧/S01E01.strm"}}})
	var jobs []model.TaskJob
	model.DB.Where("kind = ? AND source = ?", jobKindProbe, probeSourceIngest).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("应合并成一个任务：%d 个", len(jobs))
	}
	if p := decodeJobParams(&jobs[0]).Probe.Paths; len(p) != 2 {
		t.Fatalf("路径应取并集：%v", p)
	}
	if !strings.Contains(jobs[0].Title, "新入库 2 个") {
		t.Fatalf("标题应按合并后的数量重写：%s", jobs[0].Title)
	}
}
