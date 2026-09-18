package api

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 整理→同步闭环 ====================

// executeOrganize 整理核心（HTTP 与 cron 调度器共用）：
// 加载配置 → 整理引擎 → 可选对影视库执行全量同步
// 返回步骤摘要与错误（错误时 steps 里带原因）
func (h *Handler) executeOrganize(syncAfter bool) ([]gin.H, []OrganizeResult, error) {
	orgStart := time.Now()
	log.Printf("[整理] ▶ 开始整理 %s", time.Now().Format("15:04:05"))
	steps := []gin.H{}

	orgCfg, err := h.loadOrgConfig()
	if err != nil {
		return append(steps, gin.H{"step": "整理", "status": "跳过", "message": err.Error()}), nil, err
	}

	ops, err := h.newPan115Ops()
	if err != nil {
		return append(steps, gin.H{"step": "整理", "status": "失败", "message": err.Error()}), nil, err
	}

	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngine(ops, orgCfg, logFn)

	// 转存目录兜底扫描：磁力/离线下载完成时间不可控（提交 60 秒后的自动触发
	// 可能扑空），手动/定时整理时顺带扫一遍转存目录，保证迟到内容最终被整理。
	// 转存目录在待整理子树内（会被上面的扫描覆盖）或与媒体库重叠时跳过
	if shareCid := h.shareFolderCid(); shareCid != "" && shareCid != orgCfg.Pending && !h.dirOverlapWithLibrary(shareCid, orgCfg.Library) && !h.dirInside(shareCid, orgCfg.Pending) {
		// 只在转存目录有内容时才扫描并打日志（空转静默；非空时引擎会打"发现 N 个条目"）
		if ops2, err := h.newPan115Ops(); err == nil {
			if entries, _, lerr := ops2.listEntries(shareCid, 0); lerr == nil && len(entries) > 0 {
				log.Printf("[整理] ▶ 顺带扫描转存目录（%d 个条目）", len(entries))
				shareCfg := *orgCfg
				shareCfg.Pending = shareCid
				shareResults, shareOK := runOrganizeEngineWithConfig(ops, &shareCfg, logFn)
				orgResults = append(orgResults, shareResults...)
				successCount += shareOK
			}
		}
	}

	totalFiles := len(orgResults)
	existsCount := 0
	failedCount := 0
	for _, r := range orgResults {
		if r.Status == "exists" {
			existsCount++
		}
		if r.Status == "failed" {
			failedCount++
		}
	}

	steps = append(steps, gin.H{"step": "整理", "status": "完成", "message": fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)})
	strmTotal, strmCreated := 0, 0

	// 整理后同步：走增量（入库产生的移动事件会精确触发新目录遍历）。
	// 此前这里做全库遍历——手动整理收尾要重走整棵媒体库树（3 秒/目录），
	// 全库重建本就是全量同步的职责，日常入库用增量足够且秒级完成
	if syncAfter {
		p := h.incrParamsFromConfig()
		sum, err := h.executeIncrementalSync(p)
		if err != nil {
			steps = append(steps, gin.H{"step": "STRM 同步", "status": "失败", "message": err.Error()})
			return steps, orgResults, nil
		}
		strmTotal, strmCreated = sum.Videos, sum.StrmCreated
		steps = append(steps, gin.H{"step": "STRM 同步", "status": "完成",
			"message": fmt.Sprintf("新事件 %d，视频 %d（生成 STRM %d），附属下载 %d", sum.EventsFresh, sum.Videos, sum.StrmCreated, sum.AssetsDownloaded)})
		if sum.StrmCreated+sum.AssetsDownloaded > 0 {
			h.notifyEmbyRefresh(p.LocalPath)
		}
	}

	// 消息通知
	if successCount > 0 {
		var titles []string
		for _, r := range orgResults {
			if r.Status == "success" {
				line := r.Title
				if r.Year != "" {
					line += " (" + r.Year + ")"
				}
				if r.Category != "" {
					line += " [" + r.Category + "]"
				}
				titles = append(titles, line)
			}
		}
		NotifyMessage(
			fmt.Sprintf("整理完成，新增 %d 部", successCount),
			strings.Join(titles, "\n"),
		)
	}
	// 按部汇总（一部剧的 52 个文件归并为一行）
	showSet := map[string]bool{}
	var showLines []string
	for _, r := range orgResults {
		if r.TmdbID == 0 || r.Status != "success" {
			continue
		}
		key := fmt.Sprintf("%d-%s", r.TmdbID, r.TargetDir)
		if showSet[key] {
			continue
		}
		showSet[key] = true
		line := fmt.Sprintf("%s (%s) → %s", r.Title, r.Year, r.TargetDir)
		showLines = append(showLines, line)
	}
	if len(showLines) > 0 {
		log.Printf("[整理] 本次入库 %d 部:\n  %s", len(showLines), strings.Join(showLines, "\n  "))
	}

	// 空转静默：无任何产出时不打完成汇总（定时任务每 10 分钟一轮）
	if totalFiles > 0 {
		log.Printf("[整理] ✅ 整理完成（耗时 %s · 共 %d 项（成功 %d，已存在 %d，失败 %d），STRM 同步 %s",
			time.Since(orgStart).Truncate(time.Second), totalFiles, successCount, existsCount, failedCount,
			map[bool]string{true: fmt.Sprintf("已执行（%d 视频，生成 %d STRM）", strmTotal, strmCreated), false: "未执行"}[syncAfter])
	}
	return steps, orgResults, nil
}

// RunOrganizePipeline 整理→同步闭环 HTTP 入口
// POST /organize/pipeline  body: {"sync_after": true}
func (h *Handler) RunOrganizePipeline(c *gin.Context) {
	var req struct {
		SyncAfter bool `json:"sync_after"`
	}
	c.ShouldBindJSON(&req)

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("自动整理")
	defer endTask()

	steps, details, _ := h.executeOrganize(req.SyncAfter)
	// 按部归并（前端一行一部）
	showSet := map[string]bool{}
	var shows []gin.H
	for _, r := range details {
		if r.TmdbID == 0 || r.Status != "success" {
			continue
		}
		key := fmt.Sprintf("%d-%s", r.TmdbID, r.TargetDir)
		if showSet[key] {
			continue
		}
		showSet[key] = true
		shows = append(shows, gin.H{"title": r.Title, "year": r.Year, "category": r.Category, "target": r.TargetDir})
	}
	c.JSON(http.StatusOK, gin.H{"steps": steps, "details": details, "shows": shows, "message": "整理执行完成"})
}

