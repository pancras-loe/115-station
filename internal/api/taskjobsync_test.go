package api

import (
	"errors"
	"testing"

	"115-station/internal/model"
)

// 同键不同类型不合并：排着重新整理又点了深度删除，两件事都要做、依次做
func TestEnqueueDedupeByKind(t *testing.T) {
	newTestDB(t, "jobs_kind.db")
	a, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "重整", DedupeKey: "record:1"})
	b, _ := enqueueJob(model.DB, jobSpec{Kind: "deepdel", Title: "深删", DedupeKey: "record:1"})
	if a.ID == b.ID {
		t.Fatal("不同类型的任务不该互相覆盖")
	}
	c, _ := enqueueJob(model.DB, jobSpec{Kind: "full", Title: "全量", DedupeKey: "full"})
	d, _ := enqueueJob(model.DB, jobSpec{Kind: "full", Title: "全量（快速模式）", DedupeKey: "full"})
	if c.ID != d.ID {
		t.Fatal("连点两次全量应只排一次")
	}
	if got := jobStatus(t, c.ID); got.Title != "全量（快速模式）" {
		t.Fatalf("应以最后一次为准：%q", got.Title)
	}
}

// 完成回调：跑完 / 失败 / 排队中被取消都要回（机器人靠它回复结果），且只回一次
func TestJobHooks(t *testing.T) {
	newTestDB(t, "jobs_hooks.db")
	h := &Handler{DB: model.DB}
	withFakeExecutors(t, map[string]jobExecutor{
		"ok": func(*Handler, *model.TaskJob) (jobOutcome, error) {
			return jobOutcome{Message: "好了", Result: organizeJobResult{Awaiting: 2}}, nil
		},
		"bad": func(*Handler, *model.TaskJob) (jobOutcome, error) { return jobOutcome{}, errors.New("坏了") },
	})
	got := map[string]string{}
	for _, kind := range []string{"ok", "bad"} {
		job, _ := enqueueJob(model.DB, jobSpec{Kind: kind, Title: kind})
		k := kind
		onJobDone(job.ID, func(j model.TaskJob) { got[k] = j.Status + ":" + j.Message })
		h.runJob(&job)
		fireJobHooks(model.DB, job.ID) // 第二次不该再回
	}
	if got["ok"] != "success:好了" || got["bad"] != "failed:坏了" {
		t.Fatalf("回调结果不对：%v", got)
	}

	var ok model.TaskJob
	model.DB.Where("kind = ?", "ok").First(&ok)
	if d := toJobDTO(ok, nil); string(d.Result) != `{"success":0,"exists":0,"failed":0,"awaiting":2}` {
		t.Fatalf("结构化结果没落库：%s", d.Result)
	}
}

// 后台任务历史：非队列任务在 endTask 时补一行；空转轮次不留；队列任务不重复记
func TestBackgroundRunHistory(t *testing.T) {
	newTestDB(t, "jobs_bg.db")
	h := &Handler{DB: model.DB}

	beginTask("定时整理+增量")
	endTask()
	beginTask("定时整理+增量")
	markTaskIdle()
	endTask()
	beginTask("转存触发")
	failTask(errors.New("115 不通"))
	markTaskIdle() // 失败的轮次即使「没干活」也要留下原因
	endTask()

	var bg []model.TaskJob
	model.DB.Where("kind = ?", jobKindBackground).Order("id ASC").Find(&bg)
	if len(bg) != 2 || bg[0].Status != jobSuccess || bg[1].Status != jobFailed || bg[1].Message != "115 不通" {
		t.Fatalf("后台历史不对：%+v", bg)
	}

	withFakeExecutors(t, map[string]jobExecutor{
		"x": func(*Handler, *model.TaskJob) (jobOutcome, error) { return jobOutcome{Message: "ok"}, nil },
	})
	job, _ := enqueueJob(model.DB, jobSpec{Kind: "x", Title: "队列任务"})
	h.runJob(&job)
	var n int64
	model.DB.Model(&model.TaskJob{}).Where("kind = ?", jobKindBackground).Count(&n)
	if n != 2 {
		t.Fatalf("队列任务不该再补一行后台历史，现在 %d 行", n)
	}
}

func TestSummarizeOrganizeAndStoppable(t *testing.T) {
	r, msg := summarizeOrganize([]OrganizeResult{
		{Status: "success"}, {Status: "success"}, {Status: "exists"}, {Status: "failed"}, {Status: orgStatusAwaiting},
	})
	if r != (organizeJobResult{Success: 2, Exists: 1, Failed: 1, Awaiting: 1}) || msg == "" {
		t.Fatalf("整理汇总不对：%+v %q", r, msg)
	}
	if _, msg := summarizeOrganize(nil); msg != "待整理目录里没有需要处理的内容" {
		t.Fatalf("空整理的措辞不对：%q", msg)
	}

	cases := []struct {
		job  model.TaskJob
		want bool
	}{
		{model.TaskJob{Kind: "organize"}, true},
		{model.TaskJob{Kind: "confirm", Params: `{"record_ids":[1,2]}`}, true},
		{model.TaskJob{Kind: "confirm", Params: `{"record_ids":[1]}`}, false},
		{model.TaskJob{Kind: "redo", Params: `{"record_ids":[1]}`}, false},
		{model.TaskJob{Kind: "full"}, false},
	}
	for _, c := range cases {
		if got := jobStoppable(&c.job); got != c.want {
			t.Fatalf("%s %s 可停止应为 %v", c.job.Kind, c.job.Params, c.want)
		}
	}
}
