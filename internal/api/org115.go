package api

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

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
	if msg := mediaRootProblem(); msg != "" {
		return nil, nil, fmt.Errorf("未执行：%s", msg)
	}
	orgStart := time.Now()
	log.Printf("[整理] ▶ 开始整理 %s", time.Now().Format("15:04:05"))
	steps := []gin.H{}

	// 一个库一轮（orgconfigv2.go）：放进哪个库的转存文件夹就整理进哪个库，用那个库所在账号的通道
	runs, problems, err := h.organizeRuns()
	if err != nil {
		return append(steps, gin.H{"step": "整理", "status": "跳过", "message": err.Error()}), nil, err
	}
	for _, p := range problems {
		steps = append(steps, gin.H{"step": "整理", "status": "失败", "message": p})
	}
	var all []OrganizeResult
	successCount := 0
	logFn := func(msg string) { log.Println(msg) }
	for _, run := range runs {
		if jobStopRequested() {
			break
		}
		if run.lib.ID != 0 && len(runs) > 1 {
			log.Printf("[整理] ▶ 媒体库「%s」", run.lib.Name)
		}
		sink := h.newOrgSinkFor(run.cfg)
		results, ok := runOrganizeEngine(run.ops, run.cfg, sink, logFn)
		successCount += ok
		// 刮削与 Emby 刷新按片目聚合，一部剧只做一次
		if n := sink.strmTotal(); n > 0 {
			steps = append(steps, gin.H{"step": "STRM 生成", "status": "完成",
				"message": fmt.Sprintf("%s整理时直接生成 %d 个 STRM（无需等待增量同步）", libStepPrefix(run.lib), n)})
		}
		sink.flushScrape()
		sink.flushRefresh()
		finishOrganize(sink, results, orgStart)
		all = append(all, results...)
	}

	existsCount, failedCount, awaitingCount := 0, 0, 0
	for _, r := range all {
		switch r.Status {
		case "exists":
			existsCount++
		case "failed":
			failedCount++
		case orgStatusAwaiting:
			awaitingCount++
		}
	}
	msg := fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", len(all), successCount, existsCount, failedCount)
	if awaitingCount > 0 {
		msg += fmt.Sprintf("，待人工确认 %d", awaitingCount)
	}
	steps = append(steps, gin.H{"step": "整理", "status": "完成", "message": msg})
	if len(runs) == 0 && len(problems) > 0 {
		return steps, all, fmt.Errorf("%s", strings.Join(problems, "；"))
	}
	return steps, all, nil
}

// libStepPrefix 多个库时步骤摘要前面带上库名
func libStepPrefix(lib model.Library) string {
	if lib.Name == "" {
		return ""
	}
	return "「" + lib.Name + "」"
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
	if text := orgOutcomeNotifyText(sink.recordsSnapshot(), time.Now()); text != "" {
		go NotifyMessage("📋 整理结果", text)
	}
}

// orgOutcomeSeen 已推过的「没入库」结果，按 来源 fid + 状态 + 原因 去重。
// 失败的条目留在待整理里，定时整理每一轮都会再失败一次，不去重就是每 10 分钟一条
var orgOutcomeSeen = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

const (
	orgOutcomeQuiet = 6 * time.Hour // 同一条结果多久内不重复推
	orgOutcomeMax   = 10            // 一条消息最多列几部，其余只报数
)

// orgOutcomeNotifyText 本轮**没入库**的条目（已存在 / 未识别 / 失败 / 待确认）汇成一条消息。
//
// 入库成功的每部片各有一张卡片（notifyMediaStoredFull），其余结果此前一条都不推：
// 转存后内容全被判成已存在，用户只看到转存成功，之后再无下文，
// 不翻日志不知道走到了哪一步（现场：武林外传重复转存，80 集移进「已存在」）
func orgOutcomeNotifyText(recs []*model.OrganizeRecord, now time.Time) string {
	orgOutcomeSeen.Lock()
	defer orgOutcomeSeen.Unlock()
	for k, t := range orgOutcomeSeen.at {
		if now.Sub(t) > orgOutcomeQuiet {
			delete(orgOutcomeSeen.at, k)
		}
	}
	var lines []string
	n := 0
	for _, r := range recs {
		if r == nil || r.Status == "success" {
			continue
		}
		// 洗版待确认 / 同集多份停下时各自另推一条带按钮的（notifyWashHold / notifyDupHold），这里再列就重复了
		if r.Status == orgStatusAwaiting && (r.HoldWash || r.HoldDup) {
			continue
		}
		key := r.SourceFid + "|" + r.Status + "|" + r.Message
		if r.SourceFid != "" {
			if _, ok := orgOutcomeSeen.at[key]; ok {
				continue
			}
			orgOutcomeSeen.at[key] = now
		}
		n++
		if n > orgOutcomeMax {
			continue
		}
		name := strings.TrimSuffix(r.Source, "/")
		if r.Title != "" {
			name = r.Title
			if r.Year != "" {
				name += " (" + r.Year + ")"
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s\n   %s", orgOutcomeLabel(r), truncateStr(name, 60), truncateStr(r.Message, 120)))
	}
	if n == 0 {
		return ""
	}
	if n > orgOutcomeMax {
		lines = append(lines, fmt.Sprintf("… 另有 %d 条", n-orgOutcomeMax))
	}
	lines = append(lines, "详情见 任务中心 → 整理记录")
	return strings.Join(lines, "\n")
}

func orgOutcomeLabel(r *model.OrganizeRecord) string {
	switch r.Status {
	case "exists":
		if r.Stage == orgStageWash {
			return "○ 不洗版 ·" // 判输没换，不是同一份文件已存在
		}
		return "○ 已存在 ·"
	case "unrecognized":
		return "? 未识别 ·"
	case orgStatusAwaiting:
		return "⏸ 待确认 ·"
	}
	return "✗ 失败 ·"
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

// RunOrganizePipeline POST /organize/pipeline → 入任务队列，202。
// 连点几次只排一次（DedupeKey=organize）；结果见任务队列与整理记录
func (h *Handler) RunOrganizePipeline(c *gin.Context) {
	// 配置缺失当场报：别让一个注定失败的任务去排队
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.organizeReady(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "organize", Title: "手动整理", DedupeKey: "organize",
		Source: "web", Priority: jobPriorityManual})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "整理")
}
