package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

// 历史只列已结束的；状态筛选不影响各档条数，类型 / 来源 / 关键词影响
func TestQueryJobHistory(t *testing.T) {
	newTestDB(t, "jobs_history.db")
	now := time.Now()
	mk := func(kind, status, source, title, msg string) {
		j := model.TaskJob{Kind: kind, Status: status, Source: source, Title: title, Message: msg}
		if status != jobQueued && status != jobRunning {
			j.FinishedAt = &now
		}
		if err := model.DB.Create(&j).Error; err != nil {
			t.Fatal(err)
		}
	}
	mk("organize", jobSuccess, "cron", "定时整理", "成功 3")
	mk("organize", jobFailed, "cron", "定时整理", "网盘风控")
	mk("redo", jobSuccess, "web", "重新整理《沙丘》", "")
	mk("full", jobInterrupted, "web", "全量同步", "")
	mk("redo", jobQueued, "web", "排队中", "")
	mk("redo", jobRunning, "web", "执行中", "")

	jobs, total, counts := queryJobHistory(model.DB, jobHistoryFilter{})
	if total != 4 || len(jobs) != 4 || counts["all"] != 4 {
		t.Fatalf("应只有 4 条已结束的：total=%d len=%d counts=%v", total, len(jobs), counts)
	}
	if jobs[0].Title != "全量同步" {
		t.Fatalf("应按 id 倒序：%q", jobs[0].Title)
	}

	jobs, total, counts = queryJobHistory(model.DB, jobHistoryFilter{Status: jobFailed})
	if total != 1 || jobs[0].Message != "网盘风控" {
		t.Fatalf("状态筛选不对：%d %+v", total, jobs)
	}
	if counts[jobSuccess] != 2 || counts[jobFailed] != 1 || counts[jobInterrupted] != 1 || counts["all"] != 4 {
		t.Fatalf("各档条数不该受状态筛选影响：%v", counts)
	}

	_, total, counts = queryJobHistory(model.DB, jobHistoryFilter{Kind: "organize"})
	if total != 2 || counts["all"] != 2 || counts[jobSuccess] != 1 {
		t.Fatalf("类型筛选应同时作用于条数：%d %v", total, counts)
	}
	_, total, _ = queryJobHistory(model.DB, jobHistoryFilter{Source: "web"})
	if total != 2 {
		t.Fatalf("来源筛选：%d", total)
	}
	_, total, _ = queryJobHistory(model.DB, jobHistoryFilter{Q: "风控"})
	if total != 1 {
		t.Fatalf("关键词应匹配 message：%d", total)
	}
	jobs, total, _ = queryJobHistory(model.DB, jobHistoryFilter{Page: 2, Size: 3})
	if total != 4 || len(jobs) != 1 {
		t.Fatalf("分页：total=%d len=%d", total, len(jobs))
	}
}

// 任务涉及的记录 = 写了 job_id 的 ∪ 参数里点名的；和其他条件组合时 OR 不能漏出括号
func TestJobRecordsScope(t *testing.T) {
	newTestDB(t, "jobs_records.db")
	recs := []model.OrganizeRecord{
		{Source: "a", Status: "success", JobID: 7},
		{Source: "b", Status: "failed", JobID: 7},
		{Source: "c", Status: "success"},           // 被参数点名（忽略 / 深删不写 job_id）
		{Source: "d", Status: "success", JobID: 8}, // 别的任务
		{Source: "e", Status: "success"},
	}
	for i := range recs {
		model.DB.Create(&recs[i])
	}
	job, _ := enqueueJob(model.DB, jobSpec{Kind: "ignore", Title: "x", Params: jobParams{RecordIDs: []uint{recs[2].ID}}})
	model.DB.Model(&job).Update("id", 7)
	job.ID = 7

	list, total := jobRecords(model.DB, &job)
	if total != 3 || len(list) != 3 {
		t.Fatalf("应有 a b c 三条：%d %+v", total, list)
	}

	var n int64
	jobRecordsScope(model.DB.Model(&model.OrganizeRecord{}), &job).Where("status = ?", "success").Count(&n)
	if n != 2 {
		t.Fatalf("与状态筛选组合应是 a c 两条，实际 %d（OR 没加括号会把别的记录漏进来）", n)
	}

	// 没有参数的任务只按 job_id
	other := model.TaskJob{ID: 8}
	if _, total := jobRecords(model.DB, &other); total != 1 {
		t.Fatalf("任务 8 应只有 d：%d", total)
	}
}

// 队列里写的整理记录带上当前任务；写回原记录（确认入库）时覆盖成新任务
func TestOrgSinkNoteStampsJobID(t *testing.T) {
	newTestDB(t, "jobs_stamp.db")
	sink := &orgSink{}

	sink.note(&model.OrganizeRecord{Source: "队列外", Status: "success"})

	beginJobProgress(11)
	sink.note(&model.OrganizeRecord{Source: "队列里", Status: "success"})
	endJobProgress()

	var outside, inside model.OrganizeRecord
	model.DB.Where("source = ?", "队列外").First(&outside)
	model.DB.Where("source = ?", "队列里").First(&inside)
	if outside.JobID != 0 || inside.JobID != 11 {
		t.Fatalf("job_id 不对：队列外 %d，队列里 %d", outside.JobID, inside.JobID)
	}

	beginJobProgress(12)
	sink.reuse = &awaitingRef{id: inside.ID, created: inside.CreatedAt}
	sink.note(&model.OrganizeRecord{Source: "队列里", Status: "success"})
	m := withJobID(map[string]interface{}{})
	endJobProgress()

	var again model.OrganizeRecord
	model.DB.First(&again, inside.ID)
	if again.JobID != 12 {
		t.Fatalf("写回原记录时应覆盖成最近的任务：%d", again.JobID)
	}
	if m["job_id"] != uint(12) {
		t.Fatalf("withJobID 应带上当前任务：%v", m)
	}
	if m := withJobID(map[string]interface{}{}); len(m) != 0 {
		t.Fatalf("不在队列里时不该写 job_id：%v", m)
	}
}

func TestJobParamsSummary(t *testing.T) {
	got := jobParamsSummary(jobParams{RecordIDs: []uint{1, 2}, TmdbID: 42, MediaType: "tv",
		Sync: &syncJobParams{Cid: "100", LocalPath: "/strm"}, Scheduled: true})
	want := []string{"涉及 2 条整理记录", "指定 TMDB 42（剧集）", "网盘目录 cid 100", "本地目录 /strm", "定时触发"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项：%q ≠ %q", i, got[i], want[i])
		}
	}
	if s := jobParamsSummary(jobParams{}); len(s) != 0 {
		t.Fatalf("空参数应无摘要：%v", s)
	}
}
