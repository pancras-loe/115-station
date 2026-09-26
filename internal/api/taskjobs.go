package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 队列任务：重新整理 / 确认入库 ====================
//
// 入队时只做便宜的前置校验（记录在不在、状态对不对），明显错误的直接 400，
// 别让它进队列再失败；需要网络的（拉 TMDB 条目）留给执行时。
// 执行时**重读记录**，不信入队时的快照：排队期间记录可能已被别的任务改过

func init() {
	jobExecutors["redo"] = execRedoJob
	jobExecutors["confirm"] = execConfirmJob
}

// pickReq 指定 TMDB 条目的请求体。Label 是前端选中条目的「片名 (年份)」，只用于任务标题显示
type pickReq struct {
	TmdbID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
	Label     string `json:"label"`
}

func pickLabel(p pickReq) string {
	if l := strings.TrimSpace(p.Label); l != "" {
		return truncateStr(l, 60)
	}
	return fmt.Sprintf("%s/%d", p.MediaType, p.TmdbID)
}

// RedoOrganizeRecord POST /organize/records/:id/redo
// body: {"tmdb_id":123,"media_type":"movie|tv","label":"片名 (2024)"} → 入队，202
func (h *Handler) RedoOrganizeRecord(c *gin.Context) {
	var req pickReq
	if err := c.ShouldBindJSON(&req); err != nil || req.TmdbID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定正确的 TMDB 条目"})
		return
	}
	if req.MediaType != "movie" && req.MediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type 只能是 movie 或 tv"})
		return
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if err := redoPrecheck(&rec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind:      "redo",
		Title:     fmt.Sprintf("重新整理《%s》→ %s", shortTitle(rec.Source), pickLabel(req)),
		DedupeKey: fmt.Sprintf("record:%d", rec.ID),
		Source:    "web", Priority: jobPriorityManual,
		Params: jobParams{RecordIDs: []uint{rec.ID}, TmdbID: req.TmdbID, MediaType: req.MediaType},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "重新整理")
}

// redoPrecheck 重新整理的前置校验（入队时与执行时各查一次）
func redoPrecheck(rec *model.OrganizeRecord) error {
	if rec.Status == orgStatusAwaiting {
		// 待确认的文件还在待整理里原地没动，走确认入库（完整流水线），
		// 重新整理是给「已经整理过、位置不对」的条目用的
		return errors.New("待确认的条目请用「确认入库 / 重新指定」")
	}
	if len(unmarshalRecordFiles(rec.Files)) == 0 {
		return errors.New("这条记录没有登记任何文件，无法重新整理（只能删除记录）")
	}
	return nil
}

func execRedoJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	if len(p.RecordIDs) != 1 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, p.RecordIDs[0]).Error != nil {
		return jobOutcome{}, errors.New("记录已不存在（可能被删除了）")
	}
	if err := redoPrecheck(&rec); err != nil {
		return jobOutcome{}, err
	}
	if err := h.redoOrganize(&rec, p.TmdbID, p.MediaType); err != nil {
		return jobOutcome{}, err
	}
	return jobOutcome{Message: rec.Message}, nil
}

// ConfirmOrganizeRecord POST /organize/records/:id/confirm
// body 可空（按识别结果入库），或 {"tmdb_id":123,"media_type":"movie|tv","label":"…"} 改指定 → 入队，202
func (h *Handler) ConfirmOrganizeRecord(c *gin.Context) {
	var req pickReq
	_ = c.ShouldBindJSON(&req)
	if req.TmdbID > 0 && req.MediaType != "movie" && req.MediaType != "tv" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_type 只能是 movie 或 tv"})
		return
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if rec.Status != orgStatusAwaiting {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这条记录不在待确认状态（已处理过的请用「重新整理」）"})
		return
	}
	if req.TmdbID <= 0 && rec.TmdbID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这一条没有识别出来，请先搜索并指定 TMDB 条目"})
		return
	}
	target := strings.TrimSpace(rec.Title + " " + rec.Year)
	params := jobParams{RecordIDs: []uint{rec.ID}}
	if req.TmdbID > 0 {
		target = pickLabel(req)
		params.TmdbID, params.MediaType = req.TmdbID, req.MediaType
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind:      "confirm",
		Title:     fmt.Sprintf("确认入库《%s》→ %s", shortTitle(rec.Source), target),
		DedupeKey: fmt.Sprintf("record:%d", rec.ID),
		Source:    "web", Priority: jobPriorityManual, Params: params,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "确认入库")
}

// ConfirmOrganizeRecords POST /organize/records/confirm  body: {"ids":[1,2,3]}
// 批量按识别结果入库 → 一个任务（同一批共用一次刮削与 Emby 刷新）。没识别出来的跳过
func (h *Handler) ConfirmOrganizeRecords(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要确认的记录"})
		return
	}
	if len(req.IDs) > jobMaxBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多提交 %d 条", jobMaxBatch)})
		return
	}
	var ids []uint
	h.DB.Model(&model.OrganizeRecord{}).Where("id IN ? AND status = ? AND tmdb_id > 0", req.IDs, orgStatusAwaiting).
		Order("created_at ASC, id ASC").Pluck("id", &ids)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "所选记录里没有可直接确认的条目（未识别的要先指定 TMDB 条目）"})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind:   "confirm",
		Title:  fmt.Sprintf("批量确认入库 %d 条", len(ids)),
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{RecordIDs: ids},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	what := fmt.Sprintf("%d 条确认入库", len(ids))
	if skipped := len(req.IDs) - len(ids); skipped > 0 {
		what = fmt.Sprintf("%d 条确认入库（%d 条未识别或已处理，已跳过）", len(ids), skipped)
	}
	h.queuedReply(c, job, what)
}

func execConfirmJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	if len(p.RecordIDs) == 0 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	var recs []model.OrganizeRecord
	h.DB.Where("id IN ? AND status = ?", p.RecordIDs, orgStatusAwaiting).
		Order("created_at ASC, id ASC").Find(&recs)
	if len(recs) == 0 {
		return jobOutcome{}, errors.New("所选记录都已不在待确认状态（可能已被处理或删除）")
	}
	var pick *confirmPick
	if p.TmdbID > 0 {
		if len(recs) != 1 {
			return jobOutcome{}, errors.New("改指定只能用于单条记录")
		}
		pick = &confirmPick{TmdbID: p.TmdbID, MediaType: p.MediaType}
	} else {
		kept := recs[:0]
		for _, r := range recs {
			if r.TmdbID > 0 {
				kept = append(kept, r)
			}
		}
		if recs = kept; len(recs) == 0 {
			return jobOutcome{}, errors.New("没有识别结果，请先指定 TMDB 条目")
		}
	}

	out, err := h.confirmAwaiting(recs, pick)
	if err != nil {
		return jobOutcome{}, err
	}
	ok := 0
	for _, r := range out {
		if r.Status == "success" {
			ok++
		}
	}
	if len(out) == 0 {
		return jobOutcome{Message: "已按要求停止，没有处理任何条目", Canceled: true}, nil
	}
	stopped := len(out) < len(recs)
	if len(p.RecordIDs) == 1 {
		r := out[0]
		if r.Status != "success" {
			return jobOutcome{}, errors.New(orDash(r.Message))
		}
		return jobOutcome{Message: fmt.Sprintf("《%s》已入库", r.Title)}, nil
	}
	msg := fmt.Sprintf("已确认 %d 条：入库 %d", len(out), ok)
	if n := len(out) - ok; n > 0 {
		msg += fmt.Sprintf("，另有 %d 条已存在或失败（见整理记录）", n)
	}
	if stopped {
		msg += fmt.Sprintf("；已按要求停止，剩余 %d 条未处理", len(recs)-len(out))
	}
	return jobOutcome{Message: msg, Canceled: stopped}, nil
}
