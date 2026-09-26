package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"115-station/internal/model"
)

// 任务中心（docs/115-station-notes/TASK-CENTER-PLAN.md）：历史分页、任务详情里的相关记录与参数摘要。
// 顶栏轮询的 GET /tasks 不动 —— 它 2 秒一轮，历史查询不该压在它身上

// jobHistoryFilter 历史列表的筛选条件；空串表示不限
type jobHistoryFilter struct {
	Status string
	Kind   string
	Source string
	Q      string
	Page   int
	Size   int
}

// jobHistoryMaxSize 单页上限
const jobHistoryMaxSize = 100

// queryJobHistory 已结束的任务，按 id 倒序分页。
// counts 是各结束状态的条数，给筛选栏角标用：套用类型 / 来源 / 关键词，但不套用状态本身，
// 否则选中「失败」后其余几档全变成 0
func queryJobHistory(db *gorm.DB, f jobHistoryFilter) ([]model.TaskJob, int64, map[string]int64) {
	base := db.Model(&model.TaskJob{}).Where("status IN ?", jobFinishedStatuses)
	if f.Kind != "" {
		base = base.Where("kind = ?", f.Kind)
	}
	if f.Source != "" {
		base = base.Where("source = ?", f.Source)
	}
	if kw := strings.TrimSpace(f.Q); kw != "" {
		like := "%" + kw + "%"
		base = base.Where("(title LIKE ? OR message LIKE ?)", like, like)
	}

	counts := map[string]int64{"all": 0}
	for _, st := range jobFinishedStatuses {
		counts[st] = 0
	}
	var rows []struct {
		Status string
		N      int64
	}
	base.Session(&gorm.Session{}).Select("status, count(*) AS n").Group("status").Scan(&rows)
	for _, r := range rows {
		counts[r.Status] += r.N
		counts["all"] += r.N
	}

	q := base.Session(&gorm.Session{})
	if f.Status != "" && f.Status != "all" {
		q = q.Where("status = ?", f.Status)
	}
	var total int64
	q.Count(&total)
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > jobHistoryMaxSize {
		f.Size = 20
	}
	var jobs []model.TaskJob
	q.Order("id DESC").Offset((f.Page - 1) * f.Size).Limit(f.Size).Find(&jobs)
	return jobs, total, counts
}

// jobRecordsScope 某个任务涉及的整理记录：整理时写下的 job_id，并上任务参数里点名的记录。
// 忽略、深度删除不经过 orgSink，不会写 job_id，只能靠参数里的 record_ids 找回来
func jobRecordsScope(q *gorm.DB, job *model.TaskJob) *gorm.DB {
	if ids := decodeJobParams(job).RecordIDs; len(ids) > 0 {
		return q.Where("(job_id = ? OR id IN ?)", job.ID, ids)
	}
	return q.Where("job_id = ?", job.ID)
}

// withJobID 直接改记录状态的地方（确认失败、人工忽略）顺手记上当前任务，
// 与 orgSink.note 写入的 job_id 同一语义：最近一次处理它的任务
func withJobID(m map[string]interface{}) map[string]interface{} {
	if id, _ := currentJob(); id != 0 {
		m["job_id"] = id
	}
	return m
}

// jobRecordBrief 任务详情里的记录行：只要列表显示的几个字段
type jobRecordBrief struct {
	ID        uint   `json:"id"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Year      string `json:"year"`
	MediaType string `json:"media_type"`
	TmdbID    int    `json:"tmdb_id"`
}

// jobRecordLimit 详情里最多列多少条，更多的去整理记录页签按任务筛选
const jobRecordLimit = 50

func jobRecords(db *gorm.DB, job *model.TaskJob) ([]jobRecordBrief, int64) {
	q := jobRecordsScope(db.Model(&model.OrganizeRecord{}), job)
	var total int64
	q.Session(&gorm.Session{}).Count(&total)
	out := []jobRecordBrief{}
	q.Session(&gorm.Session{}).Order("id DESC").Limit(jobRecordLimit).
		Select("id, source, status, title, year, media_type, tmdb_id").Scan(&out)
	return out, total
}

// jobParamsSummary 参数里对人有意义的部分。原始 Params 不直接给前端：
// 里面的字段名是给执行器看的，而且以后加字段不该连带改界面
func jobParamsSummary(p jobParams) []string {
	var out []string
	if n := len(p.RecordIDs); n > 0 {
		out = append(out, fmt.Sprintf("涉及 %d 条整理记录", n))
	}
	if p.TmdbID > 0 {
		t := fmt.Sprintf("指定 TMDB %d", p.TmdbID)
		switch p.MediaType {
		case "movie":
			t += "（电影）"
		case "tv":
			t += "（剧集）"
		}
		out = append(out, t)
	}
	if s := p.Sync; s != nil {
		if s.Cid != "" {
			out = append(out, "网盘目录 cid "+s.Cid)
		}
		if s.LocalPath != "" {
			out = append(out, "本地目录 "+s.LocalPath)
		}
		if s.Mode != "" {
			out = append(out, "模式 "+s.Mode)
		}
	}
	if p.Scheduled {
		out = append(out, "定时触发")
	}
	return out
}

// taskJobDetail 详情返回体
type taskJobDetail struct {
	taskJobDTO
	Records     []jobRecordBrief `json:"records"`
	RecordTotal int64            `json:"record_total"`
	Summary     []string         `json:"params_summary,omitempty"`
}

// ListTaskHistory GET /tasks/history?status=&kind=&source=&q=&page=&size=
func (h *Handler) ListTaskHistory(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	f := jobHistoryFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Kind:   strings.TrimSpace(c.Query("kind")),
		Source: strings.TrimSpace(c.Query("source")),
		Q:      c.Query("q"),
		Page:   page,
		Size:   size,
	}
	jobs, total, counts := queryJobHistory(h.DB, f)
	items := make([]taskJobDTO, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, toJobDTO(j, nil))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "counts": counts})
}
