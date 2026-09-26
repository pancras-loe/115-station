package api

import (
	"fmt"
	"net/http"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 暂存指定、统一提交 ====================
//
// 用户要的是「把多条调整好再统一执行」：先在各条记录上暂存一个 TMDB 条目，
// 勾选后一次提交进任务队列。形态是 LitePan「生成计划 → 逐条编辑 → 整份执行」的轻量版
// （internal/mediaorganize/service.go 的 UpdatePlanAction / applyPlanRunner，只读参考，未复制代码）。
// 暂存落在 OrganizeRecord 上，刷新 / 换设备都还在（决策见 TASK-QUEUE-PLAN.md §8.1）

// SetRecordPending PUT /organize/records/:id/pending
// body: {"tmdb_id":123,"media_type":"movie|tv","label":"片名 (2024)"}；tmdb_id 缺省或为 0 = 撤销暂存
func (h *Handler) SetRecordPending(c *gin.Context) {
	var req pickReq
	_ = c.ShouldBindJSON(&req)
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if req.TmdbID <= 0 {
		clearPending(h.DB, rec.ID)
		c.JSON(http.StatusOK, gin.H{"message": "已撤销暂存的指定"})
		return
	}
	if req.MediaType != "movie" && req.MediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type 只能是 movie 或 tv"})
		return
	}
	if rec.Status != orgStatusAwaiting {
		// 暂存时就把「提交了也跑不了」的挡掉，别等提交时才发现
		if err := redoPrecheck(&rec); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	label := pickLabel(req)
	h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(map[string]interface{}{
		"pending_tmdb_id": req.TmdbID, "pending_media_type": req.MediaType, "pending_label": label,
	})
	c.JSON(http.StatusOK, gin.H{"message": "已暂存：" + label + "。勾选后点「提交到队列」统一执行"})
}

// submitPlan 一次提交怎么拆
type submitPlan struct {
	staged     []model.OrganizeRecord // 暂存了指定的：一条一个任务
	confirmIDs []uint                 // 没暂存、已识别的待确认：合成一个批量确认
	reasons    []string               // 跳过的原因（去重，给用户看）
}

// planSubmit 纯函数：按记录状态与暂存情况拆分一次提交。
//   - 暂存了指定的：待确认的按指定确认入库，其余按指定重新整理，一条记录一个任务
//     （每条的指定各不相同，也方便单独看结果、单独重试）；
//   - 没暂存、但已识别的待确认：合成一个批量确认任务（共用一次刮削与 Emby 刷新）；
//   - 其余跳过并说明原因
func planSubmit(recs []model.OrganizeRecord) submitPlan {
	var p submitPlan
	seen := map[string]bool{}
	skip := func(why string) {
		if !seen[why] {
			seen[why] = true
			p.reasons = append(p.reasons, why)
		}
	}
	for i := range recs {
		r := recs[i]
		switch {
		case r.PendingTmdbID > 0 && r.Status == orgStatusAwaiting:
			p.staged = append(p.staged, r)
		case r.PendingTmdbID > 0:
			if err := redoPrecheck(&r); err != nil {
				skip(err.Error())
				continue
			}
			p.staged = append(p.staged, r)
		case r.Status == orgStatusAwaiting && r.TmdbID > 0:
			p.confirmIDs = append(p.confirmIDs, r.ID)
		case r.Status == orgStatusAwaiting:
			skip("未识别的待确认条目要先指定 TMDB 条目")
		default:
			skip("已整理过的记录要先「指定」新的 TMDB 条目")
		}
	}
	return p
}

// SubmitOrganizeRecords POST /organize/records/submit  body: {"ids":[1,2,3]} → 202
func (h *Handler) SubmitOrganizeRecords(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要提交的记录"})
		return
	}
	if len(req.IDs) > jobMaxBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多提交 %d 条", jobMaxBatch)})
		return
	}
	var recs []model.OrganizeRecord
	h.DB.Where("id IN ?", req.IDs).Order("created_at ASC, id ASC").Find(&recs)
	plan := planSubmit(recs)
	if len(plan.staged) == 0 && len(plan.confirmIDs) == 0 {
		msg := "所选记录没有可提交的内容：先「指定」TMDB 条目，或选择已识别的待确认条目"
		if len(plan.reasons) > 0 {
			msg += "（" + plan.reasons[0] + "）"
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	var jobs []model.TaskJob
	for i := range plan.staged {
		rec := &plan.staged[i]
		pick := pickReq{TmdbID: rec.PendingTmdbID, MediaType: rec.PendingMediaType, Label: rec.PendingLabel}
		var job model.TaskJob
		var err error
		if rec.Status == orgStatusAwaiting {
			job, err = h.enqueueConfirm(rec, pick)
		} else {
			job, err = h.enqueueRedo(rec, pick)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		jobs = append(jobs, job)
	}
	if len(plan.confirmIDs) > 0 {
		job, err := enqueueJob(h.DB, jobSpec{
			Kind:   "confirm",
			Title:  fmt.Sprintf("批量确认入库 %d 条", len(plan.confirmIDs)),
			Source: "web", Priority: jobPriorityManual,
			Params: jobParams{RecordIDs: plan.confirmIDs},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		jobs = append(jobs, job)
	}

	// 预计耗时取这批里排得最靠后的那个
	queued := queuedJobs(h.DB)
	var eta time.Duration
	ids := make([]uint, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
		if _, e := queuePosition(queued, j.ID); e > eta {
			eta = e
		}
	}
	msg := fmt.Sprintf("已提交 %d 条到任务队列", len(plan.staged)+len(plan.confirmIDs))
	if skipped := len(req.IDs) - len(plan.staged) - len(plan.confirmIDs); skipped > 0 {
		msg += fmt.Sprintf("，%d 条跳过", skipped)
		if len(plan.reasons) > 0 {
			msg += "（" + plan.reasons[0] + "）"
		}
	}
	if eta >= time.Minute {
		msg += fmt.Sprintf("，预计约 %d 分钟跑完", int(eta.Round(time.Minute).Minutes()))
	}
	c.JSON(http.StatusAccepted, gin.H{"message": msg, "job_ids": ids, "eta_sec": int(eta.Seconds())})
}
