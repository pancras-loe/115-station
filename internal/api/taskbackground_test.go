package api

import (
	"errors"
	"testing"
	"time"

	"115-station/internal/model"
)

// 空转的后台轮次跑完不留行；失败的不算空转
func TestIdleJobDropped(t *testing.T) {
	newTestDB(t, "jobs_idle.db")
	h := &Handler{DB: model.DB}
	withFakeExecutors(t, map[string]jobExecutor{
		"idle": func(*Handler, *model.TaskJob) (jobOutcome, error) {
			return jobOutcome{Message: "没活", Idle: true}, nil
		},
		"busy": func(*Handler, *model.TaskJob) (jobOutcome, error) { return jobOutcome{Message: "整理了 3 个"}, nil },
	})
	idle, _ := enqueueJob(model.DB, jobSpec{Kind: "idle", Title: "定时整理", Priority: jobPriorityBackground})
	busy, _ := enqueueJob(model.DB, jobSpec{Kind: "busy", Title: "定时整理", Priority: jobPriorityBackground})
	h.runJob(&idle)
	h.runJob(&busy)
	var n int64
	model.DB.Model(&model.TaskJob{}).Where("id = ?", idle.ID).Count(&n)
	if n != 0 {
		t.Fatal("空转轮次应当删行")
	}
	if got := jobStatus(t, busy.ID); got.Status != jobSuccess {
		t.Fatalf("干了活的轮次要留下：%s", got.Status)
	}
}

// 后台任务同一个原因反复失败只留最新一条；原因不同、或是手动任务，都照留
func TestBackgroundFailureCollapsed(t *testing.T) {
	newTestDB(t, "jobs_collapse.db")
	h := &Handler{DB: model.DB}
	reason := "未配置待整理文件夹"
	withFakeExecutors(t, map[string]jobExecutor{
		"organize": func(*Handler, *model.TaskJob) (jobOutcome, error) { return jobOutcome{}, errors.New(reason) },
	})
	run := func(title string, prio int) uint {
		j, _ := enqueueJob(model.DB, jobSpec{Kind: "organize", Title: title, Priority: prio})
		h.runJob(&j)
		return j.ID
	}
	run("定时整理", jobPriorityBackground)
	run("定时整理", jobPriorityBackground)
	last := run("定时整理", jobPriorityBackground)
	reason = "115 掉线"
	run("定时整理", jobPriorityBackground)
	reason = "未配置待整理文件夹"
	run("手动整理", jobPriorityManual)
	run("手动整理", jobPriorityManual)

	var rows []model.TaskJob
	model.DB.Where("title = ? AND message = ?", "定时整理", "未配置待整理文件夹").Find(&rows)
	if len(rows) != 1 || rows[0].ID != last {
		t.Fatalf("同因失败应只留最新一条：%d 条", len(rows))
	}
	var n int64
	model.DB.Model(&model.TaskJob{}).Where("title = ?", "定时整理").Count(&n)
	if n != 2 {
		t.Fatalf("不同原因的失败要分别留下，实际 %d 条", n)
	}
	model.DB.Model(&model.TaskJob{}).Where("title = ?", "手动整理").Count(&n)
	if n != 2 {
		t.Fatalf("手动任务的失败不合并，实际 %d 条", n)
	}
}

// 排着手动整理时定时整理命中：保持手动的标题、参数与优先级
func TestBackgroundDoesNotOverrideManual(t *testing.T) {
	newTestDB(t, "jobs_nooverride.db")
	m, _ := enqueueJob(model.DB, jobSpec{Kind: "organize", Title: "手动整理", DedupeKey: "organize",
		Source: "web", Priority: jobPriorityManual})
	b, _ := enqueueJob(model.DB, jobSpec{Kind: "organize", Title: "定时整理", DedupeKey: "organize",
		Source: "cron", Priority: jobPriorityBackground, Params: jobParams{Scheduled: true}})
	got := jobStatus(t, m.ID)
	if b.ID != m.ID || got.Title != "手动整理" || got.Priority != jobPriorityManual || decodeJobParams(&got).Scheduled {
		t.Fatalf("手动任务被后台触发覆盖了：%+v", got)
	}
}

// 守望者状态机：有内容就入队；跑完仍未清空计一次失败并冷却；连续 3 次熔断 30 分钟；清空则复位
func TestTransferWatchDecide(t *testing.T) {
	newTestDB(t, "watch.db")
	h := &Handler{DB: model.DB}
	w := &transferWatch{}
	now := time.Now()

	w.decide(h, 0, now)
	if w.lastJob != 0 || len(queuedJobs(model.DB)) != 0 {
		t.Fatal("没内容不该入队")
	}
	w.decide(h, 2, now)
	q := queuedJobs(model.DB)
	if w.lastJob == 0 || len(q) != 1 || q[0].Kind != "transfer" || q[0].Priority != jobPriorityBackground {
		t.Fatalf("有内容应入一个后台优先级的转存整理：%+v", q)
	}
	if !transferJobActive(model.DB) {
		t.Fatal("排着的转存整理应被认出来（守望者据此不再列目录）")
	}

	finish := func() { model.DB.Model(&model.TaskJob{}).Where("id = ?", w.lastJob).Update("status", jobSuccess) }
	for i := 1; i <= 2; i++ {
		finish()
		w.decide(h, 1, now) // 跑完仍有内容 = 失败
		if w.failCount != i || !w.retryAfter.After(now) {
			t.Fatalf("第 %d 次未清空：failCount=%d retryAfter=%v", i, w.failCount, w.retryAfter)
		}
		w.decide(h, 1, now) // 冷却结束后重新入队（decide 本身不看冷却，冷却由 tick 挡）
	}
	finish()
	w.decide(h, 1, now)
	if !w.pauseUntil.After(now.Add(29*time.Minute)) || w.failCount != 0 {
		t.Fatalf("连续 3 次应熔断 30 分钟：pause=%v fail=%d", w.pauseUntil, w.failCount)
	}

	w = &transferWatch{failCount: 2}
	w.decide(h, 1, now)
	model.DB.Model(&model.TaskJob{}).Where("id = ?", w.lastJob).Update("status", jobSuccess)
	w.decide(h, 0, now)
	if w.failCount != 0 || w.lastJob != 0 {
		t.Fatal("清空后应复位失败计数")
	}
}
