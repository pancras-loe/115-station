package api

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 整理流水线 ====================
//
// 识别 → 分类 → 洗版 → 重命名 → 搬移 → 写 STRM / 下附属 → 刮削 → 刷 Emby，
// 一条单向链跑完，不再把产物交给增量同步从生活事件里反推。
//
// 整理用的 ops 打开了 suppress：自己做的每一次 move/rename 都登记进事件抑制表，
// 事件绕一圈回来时被增量同步 pop 掉跳过（p115strmhelper 的 pantransfercacher 同款）。

// executeOrganize 整理核心（HTTP 与 cron 调度器共用）：
// 加载配置 → 整理引擎（自带落盘）→ 刮削 → Emby 刷新
// 返回步骤摘要与错误（错误时 steps 里带原因）
func (h *Handler) executeOrganize() (stepsOut []gin.H, detailsOut []OrganizeResult, runErr error) {
	defer func() { failTask(runErr) }()
	if _, err := loadTmdbClient(); err != nil {
		return nil, nil, fmt.Errorf("未执行：%w", err)
	}
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
	ops.suppress = true // 整理自产的网盘变更不该再被增量同步处理一遍

	sink := h.newOrgSink(orgCfg.Library)
	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngine(ops, orgCfg, sink, logFn)

	// 转存目录兜底扫描：磁力/离线下载完成时间不可控（提交 60 秒后的自动触发
	// 可能扑空），手动/定时整理时顺带扫一遍转存目录，保证迟到内容最终被整理。
	//
	// 先列目录（1 次读请求），空的就到此为止 —— 两个重叠守卫要爬目录链算绝对路径，
	// 而绝大多数轮次转存目录是空的，没必要为「没活干」先付那几次请求。
	// 守卫仍然排在**任何整理动作之前**，安全性不变
	if shareCid := h.shareFolderCid(); shareCid != "" && shareCid != orgCfg.Pending {
		if entries, _, lerr := ops.listEntries(shareCid, 0); lerr == nil && len(entries) > 0 {
			// 转存目录在待整理子树内（会被上面的扫描覆盖）或与媒体库重叠时跳过
			if h.dirOverlapWithLibrary(shareCid, orgCfg.Library) || h.dirInside(shareCid, orgCfg.Pending) {
				log.Printf("[整理] ○ 转存目录与媒体库/待整理目录重叠，跳过兜底扫描")
			} else {
				log.Printf("[整理] ▶ 顺带扫描转存目录（%d 个条目）", len(entries))
				shareCfg := *orgCfg
				shareCfg.Pending = shareCid
				shareResults, shareOK := runOrganizeEngineWithConfig(ops, &shareCfg, sink, logFn)
				orgResults = append(orgResults, shareResults...)
				successCount += shareOK
			}
		}
	}

	totalFiles := len(orgResults)
	existsCount, failedCount, awaitingCount := 0, 0, 0
	for _, r := range orgResults {
		switch r.Status {
		case "exists":
			existsCount++
		case "failed":
			failedCount++
		case orgStatusAwaiting:
			awaitingCount++
		}
	}
	msg := fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)
	if awaitingCount > 0 {
		msg += fmt.Sprintf("，待人工确认 %d", awaitingCount)
	}
	steps = append(steps, gin.H{"step": "整理", "status": "完成", "message": msg})

	// ---- 收尾：刮削与 Emby 刷新按片目聚合，一部剧只做一次 ----
	strmCreated := sink.strmTotal()
	if strmCreated > 0 {
		steps = append(steps, gin.H{"step": "STRM 生成", "status": "完成",
			"message": fmt.Sprintf("整理时直接生成 %d 个 STRM（无需等待增量同步）", strmCreated)})
	}
	sink.flushScrape()
	sink.flushRefresh()

	finishOrganize(sink, orgResults, orgStart)
	return steps, orgResults, nil
}

// finishOrganize 整理收尾：按部汇总 + 完成行（都只进日志）。
//
// 两个整理入口（扫待整理目录的 executeOrganize、扫转存目录的
// executeOrganizeWithConfig）共用这一段。各写各的时候，同一件事按触发来源
// 会打出两种格式，而且转存那边不报入库片单 ——
// 2026-09-21 洗版验证的日志里两种格式各出现了一次。
//
// 这里不再推「整理完成，新增 N 部」那条消息：每部片入库时各自有一张卡片
// （封面 + 画质 + 集数），同一批内容推两遍纯属打扰
func finishOrganize(sink *orgSink, results []OrganizeResult, start time.Time) {
	// 按部汇总（一部剧的 52 个文件归并为一行）
	showSet := map[string]bool{}
	var showLines []string
	for _, r := range results {
		if r.TmdbID == 0 || r.Status != "success" {
			continue
		}
		key := fmt.Sprintf("%d-%s", r.TmdbID, r.TargetDir)
		if showSet[key] {
			continue
		}
		showSet[key] = true
		showLines = append(showLines, fmt.Sprintf("%s (%s) → %s", r.Title, r.Year, r.TargetDir))
	}
	if len(showLines) > 0 {
		log.Printf("[整理] 本次入库 %d 部:\n  %s", len(showLines), strings.Join(showLines, "\n  "))
	}
	// 空转静默：无任何产出时不打完成汇总（定时任务每 10 分钟一轮）
	if len(results) == 0 {
		return
	}
	log.Printf("[整理] ✅ 整理完成（耗时 %s · %s）",
		time.Since(start).Truncate(time.Second), organizeSummaryLine(sink, results))
}

// organizeSummaryLine 完成行里的计数。以落盘台账（sink）为准，
// 一条都没落盘时用引擎结果补上，不让完成行拖个空尾巴
func organizeSummaryLine(sink *orgSink, results []OrganizeResult) string {
	if line := sink.summaryLine(); line != "" {
		return line
	}
	ok, exists, fail, awaiting := 0, 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "success":
			ok++
		case "exists":
			exists++
		case orgStatusAwaiting:
			awaiting++
		default:
			fail++
		}
	}
	line := fmt.Sprintf("成功 %d（生成 STRM %d）· 已存在 %d · 失败 %d", ok, sink.strmTotal(), exists, fail)
	if awaiting > 0 {
		line += fmt.Sprintf(" · 待确认 %d", awaiting)
	}
	return line
}

// RunOrganizePipeline 整理流水线 HTTP 入口
// POST /organize/pipeline
func (h *Handler) RunOrganizePipeline(c *gin.Context) {
	// 等一会儿再说忙：30 秒一轮的增量轮询占着锁的概率不低，
	// 一抢不到就弹「任务进行中」的话，用户点十次有五次是白点
	if !taskMu.Acquire("手动整理", manualAcquireWait) {
		c.JSON(http.StatusConflict, gin.H{"error": busyErr()})
		return
	}
	defer taskMu.Unlock()
	beginTask("自动整理")
	defer endTask()

	steps, details, err := h.executeOrganize()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
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
