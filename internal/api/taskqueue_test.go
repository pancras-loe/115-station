package api

import (
	"errors"
	"testing"
	"time"

	"115-station/internal/model"
)

// withFakeExecutors 把执行器换成假的，测试结束还原
func withFakeExecutors(t *testing.T, m map[string]jobExecutor) {
	t.Helper()
	prev := jobExecutors
	jobExecutors = m
	t.Cleanup(func() { jobExecutors = prev })
}

func jobStatus(t *testing.T, id uint) model.TaskJob {
	t.Helper()
	var j model.TaskJob
	if err := model.DB.First(&j, id).Error; err != nil {
		t.Fatal(err)
	}
	return j
}

// 同一条记录排队期间改了几次指定：只留一条，参数以最后一次为准
func TestEnqueueJobDedupe(t *testing.T) {
	newTestDB(t, "jobs_dedupe.db")
	a, err := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "A", DedupeKey: "record:1",
		Params: jobParams{RecordIDs: []uint{1}, TmdbID: 10, MediaType: "movie"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "B", DedupeKey: "record:1",
		Params: jobParams{RecordIDs: []uint{1}, TmdbID: 20, MediaType: "tv"}})
	if a.ID != b.ID {
		t.Fatalf("同键排队中的任务应被覆盖，却新建了一条：%d vs %d", a.ID, b.ID)
	}
	if q := queuedJobs(model.DB); len(q) != 1 {
		t.Fatalf("排队中应只有 1 条，实际 %d", len(q))
	}
	got := jobStatus(t, a.ID)
	if p := decodeJobParams(&got); p.TmdbID != 20 || p.MediaType != "tv" || got.Title != "B" {
		t.Fatalf("参数应以最后一次为准：%+v %q", p, got.Title)
	}

	// 已经在跑的不受影响：新提交排在后面
	model.DB.Model(&model.TaskJob{}).Where("id = ?", a.ID).Update("status", jobRunning)
	c, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "C", DedupeKey: "record:1",
		Params: jobParams{RecordIDs: []uint{1}, TmdbID: 30}})
	if c.ID == a.ID {
		t.Fatal("运行中的任务不应被覆盖")
	}
}

// 排队顺序：手动优先于后台，同优先级先来先跑；位置与预计耗时按记录条数累计
func TestQueueOrderAndPosition(t *testing.T) {
	newTestDB(t, "jobs_order.db")
	bg, _ := enqueueJob(model.DB, jobSpec{Kind: "confirm", Title: "后台", Priority: jobPriorityBackground,
		Params: jobParams{RecordIDs: []uint{1}}})
	m1, _ := enqueueJob(model.DB, jobSpec{Kind: "confirm", Title: "手动1", Params: jobParams{RecordIDs: []uint{2, 3, 4}}})
	m2, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "手动2", Params: jobParams{RecordIDs: []uint{5}}})

	q := queuedJobs(model.DB)
	if len(q) != 3 || q[0].ID != m1.ID || q[1].ID != m2.ID || q[2].ID != bg.ID {
		t.Fatalf("顺序不对：%v", []uint{q[0].ID, q[1].ID, q[2].ID})
	}
	next, ok := nextQueuedJob(model.DB)
	if !ok || next.ID != m1.ID {
		t.Fatalf("下一个应是 手动1，实际 %d", next.ID)
	}
	pos, eta := queuePosition(q, m2.ID)
	if pos != 2 || eta != 4*jobItemEstimate {
		t.Fatalf("手动2 应排第 2、预计 4 条的耗时，实际 %d / %s", pos, eta)
	}
	if pos, _ := queuePosition(q, 9999); pos != 0 {
		t.Fatal("不在队列里的任务位置应为 0")
	}
}

// 执行结果：成功 / 失败 / panic / 中途停止 各落到对应状态，panic 不能带走 worker
func TestRunJobOutcomes(t *testing.T) {
	newTestDB(t, "jobs_run.db")
	h := &Handler{DB: model.DB}
	withFakeExecutors(t, map[string]jobExecutor{
		"ok": func(*Handler, *model.TaskJob) (jobOutcome, error) {
			setJobProgress("落盘", 1, 2, "x")
			return jobOutcome{Message: "好了"}, nil
		},
		"bad":  func(*Handler, *model.TaskJob) (jobOutcome, error) { return jobOutcome{}, errors.New("坏了") },
		"boom": func(*Handler, *model.TaskJob) (jobOutcome, error) { panic("炸了") },
		"half": func(*Handler, *model.TaskJob) (jobOutcome, error) {
			return jobOutcome{Message: "停了", Canceled: true}, nil
		},
	})
	cases := map[string]string{"ok": jobSuccess, "bad": jobFailed, "boom": jobFailed, "half": jobCanceled}
	for kind, want := range cases {
		job, _ := enqueueJob(model.DB, jobSpec{Kind: kind, Title: kind})
		h.runJob(&job)
		got := jobStatus(t, job.ID)
		if got.Status != want {
			t.Fatalf("%s：状态应为 %s，实际 %s（%s）", kind, want, got.Status, got.Message)
		}
		if got.StartedAt == nil || got.FinishedAt == nil {
			t.Fatalf("%s：开始 / 结束时间没有写", kind)
		}
		if _, _, held := taskMu.Holder(); held {
			t.Fatalf("%s：跑完没有放锁", kind)
		}
	}
	var ok model.TaskJob
	model.DB.Where("kind = ?", "ok").First(&ok)
	if d := toJobDTO(ok, nil); d.Progress == nil || d.Progress.Phase != "落盘" || d.Progress.Done != 1 {
		t.Fatalf("结束时的进度快照没有落库：%+v", d.Progress)
	}
	if id, _ := currentJob(); id != 0 {
		t.Fatal("跑完应清掉当前任务")
	}
}

// 等锁期间被取消：拿到锁后不执行，也不把状态改回 running
func TestRunJobCanceledWhileWaiting(t *testing.T) {
	newTestDB(t, "jobs_cancel.db")
	h := &Handler{DB: model.DB}
	ran := false
	withFakeExecutors(t, map[string]jobExecutor{
		"x": func(*Handler, *model.TaskJob) (jobOutcome, error) { ran = true; return jobOutcome{}, nil },
	})
	prev := jobAcquireSlice
	jobAcquireSlice = 20 * time.Millisecond
	t.Cleanup(func() { jobAcquireSlice = prev })

	if !taskMu.TryLock("测试占锁") {
		t.Fatal("锁被别人占着")
	}
	job, _ := enqueueJob(model.DB, jobSpec{Kind: "x", Title: "x"})
	done := make(chan struct{})
	go func() { h.runJob(&job); close(done) }()
	time.Sleep(50 * time.Millisecond)
	model.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("status", jobCanceled)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		taskMu.Unlock()
		t.Fatal("取消后 worker 应停止等锁")
	}
	taskMu.Unlock()
	if ran {
		t.Fatal("已取消的任务不应执行")
	}
	if got := jobStatus(t, job.ID); got.Status != jobCanceled {
		t.Fatalf("状态应保持 canceled，实际 %s", got.Status)
	}
}

// 重启：running → interrupted（不自动重跑），queued 保留
func TestRecoverInterruptedJobs(t *testing.T) {
	newTestDB(t, "jobs_recover.db")
	r, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "跑到一半"})
	q, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "还没开始"})
	model.DB.Model(&model.TaskJob{}).Where("id = ?", r.ID).Update("status", jobRunning)

	recoverInterruptedJobs(model.DB)
	if got := jobStatus(t, r.ID); got.Status != jobInterrupted || got.FinishedAt == nil {
		t.Fatalf("运行中的应标成中断，实际 %s", got.Status)
	}
	if got := jobStatus(t, q.ID); got.Status != jobQueued {
		t.Fatalf("排队中的应保留，实际 %s", got.Status)
	}
}

// 让路窗口：增量超过两个周期没跑才让；让的时候等到增量跑完一轮就继续，等不到也有上限
func TestIncrYieldWindow(t *testing.T) {
	now := time.Now()
	if needIncrWindow(now, now.Add(-50*time.Second), 30*time.Second) {
		t.Fatal("没超过两个周期不该让路")
	}
	if !needIncrWindow(now, now.Add(-61*time.Second), 30*time.Second) {
		t.Fatal("超过两个周期应当让路")
	}
	if needIncrWindow(now, now.Add(-time.Hour), 0) {
		t.Fatal("增量轮询关着时不该让路")
	}

	prevPoll, prevMax := incrWindowPoll, incrWindowMax
	incrWindowPoll, incrWindowMax = 5*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { incrWindowPoll, incrWindowMax = prevPoll, prevMax })

	// 增量在窗口里跑完一轮 → 立即继续
	lastIncrRun.Store(time.Now().Add(-time.Hour).UnixNano())
	go func() { time.Sleep(30 * time.Millisecond); markIncrRun() }()
	start := time.Now()
	waitIncrWindow(10 * time.Millisecond)
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("增量跑完后应立即继续，却等了 %s", el)
	}

	// 增量一直不来 → 等满上限就走，不能卡死队列
	lastIncrRun.Store(time.Now().Add(-time.Hour).UnixNano())
	start = time.Now()
	waitIncrWindow(10 * time.Millisecond)
	if el := time.Since(start); el < 20*time.Millisecond || el > time.Second {
		t.Fatalf("等待时长不对：%s", el)
	}
}

// 进度的哨兵：传 progressKeep 的字段保持原值，0 是合法进度
func TestJobProgressKeep(t *testing.T) {
	beginJobProgress(7)
	t.Cleanup(func() { endJobProgress() })
	setJobProgress("落盘", 2, 5, "a")
	setJobProgress("", progressKeep, progressKeep, "b")
	if _, p := currentJob(); p.Phase != "落盘" || p.Done != 2 || p.Total != 5 || p.Label != "b" {
		t.Fatalf("哨兵字段不该被覆盖：%+v", p)
	}
	setJobProgress("", 0, progressKeep, "")
	if _, p := currentJob(); p.Done != 0 || p.Total != 5 {
		t.Fatalf("0 是合法进度：%+v", p)
	}
	if requestJobStop(8) || !requestJobStop(7) || !jobStopRequested() {
		t.Fatal("停止请求只能作用于当前任务")
	}
}
