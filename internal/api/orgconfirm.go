package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 人工确认 ====================
//
// 「基础配置 → 人工确认」打开后，整理识别完就停下：登记一条 awaiting 记录、
// 文件原地不动（不搬、不改名、不查重、不洗版）。用户在整理记录里点「确认入库」，
// 或者搜 TMDB 改指定一个条目，才接着走后半条流水线。
//
// 停在识别之后、任何网盘写操作之前：识别错了的代价只是一条待确认记录，
// 不再是「文件已经按错名搬进库、STRM 和刮削都落了」然后靠重新整理去收拾。
// 例外是识别前就做掉的广告/超小视频分流——那些是垃圾，与识别结果无关。
//
// 确认时复用 processDir / processSingleFile 本体（ctx.forced 跳过识别），
// 而不是另写一条入库路径：洗版、去重、补全、落盘、刮削全部和自动整理同一套逻辑。

// orgStatusAwaiting 整理记录「待确认」状态
const orgStatusAwaiting = "awaiting"

// awaitingRef 一条待确认记录的定位信息（整理跑的时候按 fid 查它）
type awaitingRef struct {
	id      uint
	created time.Time
	manual  bool // 用户改指定过 TMDB 条目（写回记录的 ManualTmdb）
	ai      bool // AI 判定停下的（OrganizeRecord.HoldAI）：「人工确认」开关关着也不自动接手
	fids    []string
}

// loadAwaiting 所有待确认记录，按 fid 索引：记录自身的 SourceFid 与登记的每个文件都算，
// 散文件的待确认记录挂着同前缀的其他集，它们作为顶层条目时同样要被认出来
func loadAwaiting() map[string]*awaitingRef {
	out := map[string]*awaitingRef{}
	if model.DB == nil {
		return out
	}
	var rows []model.OrganizeRecord
	model.DB.Select("id, created_at, source_fid, files, hold_ai").Where("status = ?", orgStatusAwaiting).Find(&rows)
	for _, r := range rows {
		ref := &awaitingRef{id: r.ID, created: r.CreatedAt, ai: r.HoldAI}
		if r.SourceFid != "" {
			ref.fids = append(ref.fids, r.SourceFid)
		}
		for _, f := range unmarshalRecordFiles(r.Files) {
			if f.Fid != "" {
				ref.fids = append(ref.fids, f.Fid)
			}
		}
		for _, fid := range ref.fids {
			out[fid] = ref
		}
	}
	return out
}

// dropHeld 顶层条目里去掉正在等人工确认的（开关开着时）。静默：定时整理每 10 分钟一轮，
// 每轮给同一批待确认条目各打一行日志没有意义，记录页里看得到。
// 开关关掉之后不过滤——processEntry 会接手，把结果写回那条待确认记录
func (c *orgCtx) dropHeld(entries []dirEntry) []dirEntry {
	if len(c.held) == 0 {
		return entries
	}
	out := entries[:0]
	for _, e := range entries {
		// AI 判定停下的不管开关怎样都跳过：接手就是重新识别、再调一次模型、再停回来
		if ref := c.held[e.Fid]; ref == nil || (!c.cfg.ManualConfirm && !ref.ai) {
			out = append(out, e)
		}
	}
	return out
}

// adoptAwaiting 开关关掉后，自动整理接手一条待确认的条目：结果写回那条记录
func (c *orgCtx) adoptAwaiting(ref *awaitingRef) {
	c.sink.reuse = ref
	for _, fid := range ref.fids {
		delete(c.held, fid)
	}
}

// releaseAwaiting 接手结束。没落成任何记录（比如容器目录、非视频）时那条待确认已经没有意义，删掉，
// 否则它会一直挂在记录页上、而对应的条目早就不在原处了
func (c *orgCtx) releaseAwaiting(ref *awaitingRef) {
	if c.sink.reuse == ref {
		c.sink.reuse = nil
		model.DB.Delete(&model.OrganizeRecord{}, ref.id)
	}
}

// holdForConfirm 人工确认模式：识别到此为止，登记待确认记录，文件原地不动。
// media 为 nil 表示没识别出来，需要用户手动指定；reason 是这种情况下给用户看的原因
func (c *orgCtx) holdForConfirm(source, fid, kind string, media *TmdbMedia, parsed *ParsedName,
	sample string, files []orgRecordFile, reason string) OrganizeResult {
	rec := &model.OrganizeRecord{
		Source: source, SourceFid: fid, SourceKind: kind, SourceCid: c.cfg.Pending,
		Status: orgStatusAwaiting, Stage: "confirm", Files: marshalRecordFiles(files),
	}
	for _, f := range files {
		if f.Kind == "video" {
			rec.VideoCount++
			rec.TotalSize += f.Size
		}
	}
	res := OrganizeResult{FileName: source, Status: orgStatusAwaiting}
	if media != nil {
		category := classifyMedia(media)
		rec.TmdbID, rec.Title, rec.Year, rec.MediaType, rec.PosterPath =
			media.TmdbID, media.Title, media.Year, media.MediaType, media.PosterPath
		rec.Category = category
		// 预览落点：与入库同一套模板算，用户确认前就能看出分类和命名对不对
		if newPath := buildNewNameWithTemplate(media, parsed, sample); newPath != "" {
			rec.TargetDir = libSubPath(categoryDir(media.MediaType, category), strings.SplitN(newPath, "/", 2)[0])
		}
		rec.Message = "识别完成，等待人工确认后入库"
		if reason != "" {
			rec.Message = reason // AI 判定停下的：说清楚是几分、为什么停
		}
		res.TmdbID, res.Title, res.Year, res.MediaType, res.Category =
			media.TmdbID, media.Title, media.Year, media.MediaType, category
		c.onLog(fmt.Sprintf("⏸ %s → %s (%s)，等待人工确认", shortLogName(source), media.Title, media.Year))
	} else {
		rec.Message = reason
		c.onLog(fmt.Sprintf("⏸ %s - %s，留在原处等待人工指定", shortLogName(source), reason))
	}
	res.Message = rec.Message
	c.sink.note(rec)
	// 本轮后面的顶层条目里还有这些文件（同前缀的其他集），别再当新条目识别一遍
	ref := &awaitingRef{id: rec.ID, created: rec.CreatedAt, ai: rec.HoldAI}
	for _, f := range files {
		ref.fids = append(ref.fids, f.Fid)
	}
	ref.fids = append(ref.fids, fid)
	for _, id := range ref.fids {
		c.held[id] = ref
	}
	return res
}

// awaitingFidSet 待确认条目的 fid 集合（转存守望者判断「目录里还有没有活」用）
func awaitingFidSet() map[string]bool {
	out := map[string]bool{}
	for fid := range loadAwaiting() {
		out[fid] = true
	}
	return out
}

// ---- 确认入库 ----

// confirmPick 用户改指定的 TMDB 条目
type confirmPick struct {
	TmdbID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

// confirmAwaiting 按确认结果把一批待确认条目走完后半条流水线。
// pick 非空 = 用户改指定（只用于单条）。返回每条记录处理后的最新状态
func (h *Handler) confirmAwaiting(recs []model.OrganizeRecord, pick *confirmPick) ([]model.OrganizeRecord, error) {
	cfg, err := h.loadOrgConfig()
	if err != nil {
		return nil, err
	}
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, err
	}
	ops.suppress = true
	ensureRenameTpl()
	logFn := func(msg string) { log.Println("[整理] " + msg) }
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library)
	}
	sink := h.newOrgSink(cfg.Library)
	pruner := newDirPruner(ops, orgProtectedCids(cfg), logFn)
	rules := loadReplaceRules()

	out := make([]model.OrganizeRecord, 0, len(recs))
	for i := range recs {
		// 用户在队列里点了停止：做完上一条就收工，已完成的照常生效（刮削 / 刷新在循环后统一冲刷）
		if jobStopRequested() {
			log.Printf("[整理] ○ 批量确认已按要求停止：完成 %d/%d", i, len(recs))
			break
		}
		rec := recs[i]
		SetTaskProgress(fmt.Sprintf("确认整理 %d/%d：%s", i+1, len(recs), truncateStr(rec.Source, 40)))
		setJobProgress("确认入库", i, len(recs), truncateStr(rec.Source, 60))
		tmdbID, mediaType := rec.TmdbID, rec.MediaType
		if pick != nil {
			tmdbID, mediaType = pick.TmdbID, pick.MediaType
		}
		media, err := tc.getByTmdbID(tmdbID, mediaType == "tv")
		if err != nil || media == nil {
			msg := fmt.Sprintf("拉取 TMDB 条目 %s/%d 失败", mediaType, tmdbID)
			if err != nil {
				msg += ": " + err.Error()
			}
			// 拉不到条目不改记录状态：多半是网络问题，用户稍后再点一次即可
			if len(recs) == 1 {
				return nil, errors.New(msg)
			}
			rec.Message = msg
			out = append(out, rec)
			continue
		}
		media.MediaType = mediaType

		rcfg := *cfg
		if rec.SourceCid != "" {
			rcfg.Pending = rec.SourceCid // 散文件的字幕等附件要回原来的扫描根里找
		}
		ctx := &orgCtx{ops: ops, cfg: &rcfg, tc: tc, rules: rules, libAbs: libAbs, sink: sink,
			pruner: pruner, onLog: logFn, forced: media,
			held: map[string]*awaitingRef{}, handled: map[string]bool{}}
		sink.reuse = &awaitingRef{id: rec.ID, created: rec.CreatedAt, manual: tmdbID != rec.TmdbID}
		log.Printf("[整理] ▶ 人工确认《%s》→ %s (%s) [tmdb=%d]", rec.Source, media.Title, media.Year, tmdbID)

		autoID, autoType, key := rec.TmdbID, rec.MediaType, rec.RecogKey
		msg := confirmOne(ctx, &rec)
		// 改了指定（或本来就没识别出来、由人指定）= 人给出的结论，记进识别记忆
		if msg == "" && (tmdbID != autoID || mediaType != autoType) {
			rememberRecognition(key, rec.Source, media)
		}
		if msg != "" {
			sink.reuse = nil
			h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(withJobID(map[string]interface{}{
				"status": "failed", "stage": "confirm", "message": msg,
			}))
			log.Printf("[整理] ✗ 人工确认《%s》失败: %s", rec.Source, msg)
		} else if sink.reuse != nil {
			// 流水线一条记录都没写（理论上不会）：别让它一直停在待确认
			sink.reuse = nil
			h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(withJobID(map[string]interface{}{
				"status": "failed", "stage": "confirm", "message": "确认后没有产生任何整理结果，请查看实时日志",
			}))
		}
		var fresh model.OrganizeRecord
		if h.DB.First(&fresh, rec.ID).Error == nil {
			out = append(out, fresh)
		} else {
			out = append(out, rec)
		}
	}
	setJobProgress("刮削与刷新媒体库", len(out), len(recs), "")
	pruner.flush()
	sink.flushScrape()
	sink.flushRefresh()
	SetTaskProgress("")
	return out, nil
}

// confirmOne 让一条待确认记录走完后半条流水线；返回非空 = 连开始都没开始的原因
// （源目录没了之类），流水线内部的失败由它自己记在记录上
func confirmOne(ctx *orgCtx, rec *model.OrganizeRecord) string {
	if rec.SourceKind == "dir" {
		name := strings.TrimSuffix(rec.Source, "/")
		files, err := collectDirFiles(ctx.ops, rec.SourceFid, name)
		switch {
		case errors.Is(err, errDirGone):
			return "源目录已不存在（可能被移动或删除），这条记录可以直接删掉"
		case err != nil:
			return "读取源目录失败，稍后再试: " + err.Error()
		case len(files) == 0:
			return "源目录已经空了，这条记录可以直接删掉"
		}
		processDir(ctx, dirEntry{Fid: rec.SourceFid, Cid: rec.SourceFid, Name: name, IsDir: true}, files)
		return ""
	}

	var main *orgRecordFile
	var others []remoteFile
	for i, f := range unmarshalRecordFiles(rec.Files) {
		if f.Kind != "video" {
			continue
		}
		if main == nil && (f.Fid == rec.SourceFid || i == 0) {
			ff := f
			main = &ff
			continue
		}
		others = append(others, remoteFile{Fid: f.Fid, Name: f.Name, PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1})
	}
	if main == nil {
		return "记录里没有登记视频文件，无法确认"
	}
	result, newRec := processSingleFile(ctx, remoteFile{Fid: main.Fid, Name: main.Name,
		PickCode: main.PickCode, Size: main.Size, Sha1: main.Sha1})
	if result.Status != "success" {
		return ""
	}
	organizeSiblings(ctx, newRec, result, others)
	return ""
}

// ---- 忽略 ----

// IgnoreOrganizeRecord POST /organize/records/:id/ignore
// 不要这一条：和自动整理「未识别」同样的去向——移到冗余，记录改成未识别
// （之后仍可以在记录上「重新整理」捞回来）
func (h *Handler) IgnoreOrganizeRecord(c *gin.Context) {
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if rec.Status != orgStatusAwaiting {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这条记录不在待确认状态"})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "ignore",
		Title:     fmt.Sprintf("忽略《%s》", shortTitle(rec.Source)),
		DedupeKey: fmt.Sprintf("record:%d", rec.ID), Source: "web", Priority: jobPriorityManual,
		Params: jobParams{RecordIDs: []uint{rec.ID}}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "忽略")
}

// ignoreAwaiting 忽略一条待确认记录：移到冗余，记录改成未识别。调用方持有 taskMu
func (h *Handler) ignoreAwaiting(rec *model.OrganizeRecord) (string, error) {
	cfg, err := h.loadOrgConfig()
	if err != nil {
		return "", err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return "", err
	}
	ops.suppress = true
	msg := ""
	if rec.SourceKind == "dir" {
		if err := ops.moveFiles(cfg.Redundant, []string{rec.SourceFid}); err != nil {
			return "", fmt.Errorf("移到冗余失败: %w", err)
		}
		msg = "已人工忽略，目录已移到冗余"
	} else {
		pending := cfg.Pending
		if rec.SourceCid != "" {
			pending = rec.SourceCid
		}
		var fids []string
		var videos []string
		for _, f := range unmarshalRecordFiles(rec.Files) {
			fids = append(fids, f.Fid)
			if f.Kind == "video" {
				videos = append(videos, f.Name)
			}
		}
		holdingDir := sourceHoldingDir(rec.Source, "")
		holdingCid, err := moveToHoldingDir(ops, cfg.Redundant, holdingDir, fids)
		if err != nil {
			return "", fmt.Errorf("移到冗余失败: %w", err)
		}
		// 字幕等附件跟着走，别在待整理里留一堆孤儿
		for _, v := range videos {
			moveSiblingAttachments(ops, pending, baseName(v), "", holdingCid, false, func(string) {})
		}
		msg = "已人工忽略，已移到 冗余/" + holdingDir
	}
	h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(withJobID(map[string]interface{}{
		"status": "unrecognized", "stage": "confirm", "message": msg,
	}))
	log.Printf("[整理] ○ 人工忽略《%s》：%s", rec.Source, msg)
	return msg, nil
}

// ---- 统计 ----

// OrganizeRecordStats GET /organize/records/stats → 各状态条数（筛选栏的角标）
func (h *Handler) OrganizeRecordStats(c *gin.Context) {
	var rows []struct {
		Status string
		N      int64
	}
	h.DB.Model(&model.OrganizeRecord{}).Select("status, count(*) AS n").Group("status").Scan(&rows)
	out := map[string]int64{"all": 0, "problem": 0, orgStatusAwaiting: 0,
		"success": 0, "exists": 0, "failed": 0, "unrecognized": 0}
	for _, r := range rows {
		out[r.Status] += r.N
		out["all"] += r.N
		if r.Status == "failed" || r.Status == "unrecognized" {
			out["problem"] += r.N
		}
	}
	var staged int64
	h.DB.Model(&model.OrganizeRecord{}).Where("pending_tmdb_id > 0").Count(&staged)
	out["staged"] = staged
	c.JSON(http.StatusOK, gin.H{"data": out})
}
