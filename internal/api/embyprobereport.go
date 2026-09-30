package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ==================== Emby 提前探测的结果回报（任务中心）====================
//
// 探测由单 worker 慢慢跑（embyextract.go），此前结果只进日志与片目详情，用户看不到失败原因、
// 也不知道什么时候能再试。现在：
//   - 手动探测任务（embyprobejob.go）排进来的路径带着任务 id，每个条目的结果（成功 / 失败 / 防抖跳过 /
//     Emby 里还没有）写回那个任务（TaskJob.Probe，单独一列），任务详情里逐条显示；
//   - 任务中心另有全局视图（GET /tasks/probe）：队列、正在探哪一集、熔断暂停到几点、
//     记账里所有没成功的条目 —— 入库确认排进来的自动探测没有任务可挂，靠这里看，也能在这里手动重试
//
// 「什么时候能再试」一律由 embyProbeStateOf 按记账算，与片目详情、worker 的放行判定同一套。

const (
	probeItemFailed  = "failed"  // 这次请求了，失败 / 超时
	probeItemHeld    = "held"    // 刚请求过（防抖）或自动次数用完，这次没请求
	probeItemMissing = "missing" // Emby 里还没有这个片目 / 条目
	probeItemError   = "error"   // 整条路径没法处理（查 Emby 失败、没配 Emby、队列满）
)

// probeReportMaxItems 报告里最多列几条（完整的在日志里）
const probeReportMaxItems = 100

// jobProbeItem 一个没探成的条目
type jobProbeItem struct {
	Label    string     `json:"label"`
	Kind     string     `json:"kind"`
	Err      string     `json:"err,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"` // 防抖没过：这个时间之后才能再手动请求
	// AutoStopped 自动入口的次数已用完：入库后不会再自动探，只能手动
	AutoStopped bool `json:"auto_stopped,omitempty"`
}

// jobProbeReport 一个探测任务的结果
type jobProbeReport struct {
	Paths     int            `json:"paths"`    // 排进来的片目（路径）数
	Finished  int            `json:"finished"` // 已处理完的路径
	Planned   int            `json:"planned"`  // 这次要请求的视频数（逐个片目查到后累加）
	OK        int            `json:"ok"`
	Failed    int            `json:"failed"`
	Held      int            `json:"held"`
	Missing   int            `json:"missing"`
	Errors    int            `json:"errors"`
	Canceled  int            `json:"canceled,omitempty"` // 任务停止后没探的
	Items     []jobProbeItem `json:"items,omitempty"`
	More      int            `json:"more,omitempty"` // 超出 probeReportMaxItems 没列的
	UpdatedAt time.Time      `json:"updated_at"`

	// 以下读取时现算，不落库
	State       string     `json:"state,omitempty"` // queued / running / done / canceled / lost
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

var probeReports = struct {
	sync.Mutex
	m map[uint]*jobProbeReport
}{m: map[uint]*jobProbeReport{}}

// probeReportLoad 取内存里的报告；没有就从库里接着上次的
func probeReportLoad(id uint) *jobProbeReport {
	if r := probeReports.m[id]; r != nil {
		return r
	}
	r := &jobProbeReport{}
	if model.DB != nil {
		var job model.TaskJob
		if model.DB.Select("probe").First(&job, id).Error == nil && job.Probe != "" {
			_ = json.Unmarshal([]byte(job.Probe), r)
		}
	}
	probeReports.m[id] = r
	return r
}

// probeReportLive 当前的报告（拷贝）：探测任务的执行器轮询它
func probeReportLive(id uint) jobProbeReport {
	probeReports.Lock()
	defer probeReports.Unlock()
	if r := probeReports.m[id]; r != nil {
		return *r
	}
	var r jobProbeReport
	if model.DB != nil {
		var job model.TaskJob
		if model.DB.Select("probe").First(&job, id).Error == nil && job.Probe != "" {
			_ = json.Unmarshal([]byte(job.Probe), &r)
		}
	}
	return r
}

// probeReportUpdate 改一份报告并落库；路径全部处理完就从内存里拿掉
func probeReportUpdate(jobs []uint, fn func(r *jobProbeReport)) {
	if len(jobs) == 0 {
		return
	}
	probeReports.Lock()
	defer probeReports.Unlock()
	for _, id := range jobs {
		r := probeReportLoad(id)
		fn(r)
		r.UpdatedAt = time.Now()
		if model.DB != nil {
			b, _ := json.Marshal(r)
			model.DB.Model(&model.TaskJob{}).Where("id = ?", id).Update("probe", string(b))
		}
		if r.Paths > 0 && r.Finished >= r.Paths {
			delete(probeReports.m, id)
		}
	}
}

func probeReportQueued(jobID uint, n int) {
	probeReportUpdate([]uint{jobID}, func(r *jobProbeReport) { r.Paths += n })
}

func probeReportPathDone(jobs []uint) {
	probeReportUpdate(jobs, func(r *jobProbeReport) { r.Finished++ })
}

func probeReportPlanned(jobs []uint, n int) {
	if n > 0 {
		probeReportUpdate(jobs, func(r *jobProbeReport) { r.Planned += n })
	}
}

func probeReportCanceled(jobs []uint, n int) {
	probeReportUpdate(jobs, func(r *jobProbeReport) { r.Canceled += n })
}

func probeReportOK(jobs []uint) {
	probeReportUpdate(jobs, func(r *jobProbeReport) { r.OK++ })
}

func probeReportItem(jobs []uint, it jobProbeItem) {
	probeReportUpdate(jobs, func(r *jobProbeReport) {
		switch it.Kind {
		case probeItemFailed:
			r.Failed++
		case probeItemHeld:
			r.Held++
		case probeItemMissing:
			r.Missing++
		default:
			r.Errors++
		}
		if len(r.Items) < probeReportMaxItems {
			r.Items = append(r.Items, it)
		} else {
			r.More++
		}
	})
}

// probeHeldItem 按记账说清这个条目为什么没探 / 探失败了、什么时候能再手动请求
func probeHeldItem(it embyExtractItem) jobProbeItem {
	out := jobProbeItem{Label: it.label(), Kind: probeItemHeld}
	m, ok := embyExtractLoad(it.ID)
	if !ok {
		return out
	}
	st := embyProbeStateOf(false, true, &m, probeQueuedNone, false, time.Now())
	out.Err, out.Attempts, out.RetryAt, out.AutoStopped = st.LastErr, st.Attempts, st.ManualAt, st.State == "exhausted"
	return out
}

// probeReportOf 任务详情 / 列表里显示的探测结果；不是探测任务（或还没开始）返回 nil
func probeReportOf(job *model.TaskJob) *jobProbeReport {
	if job.Probe == "" {
		return nil
	}
	var r jobProbeReport
	if json.Unmarshal([]byte(job.Probe), &r) != nil {
		return nil
	}
	switch {
	case r.Finished >= r.Paths:
		r.State = "done"
	case job.Status == jobRunning:
		r.State, _, _ = embyExtractJobState(job.ID)
		if r.State == "" {
			r.State = "running" // 收尾中
		}
	case job.Status == jobCanceled:
		r.State = "canceled"
	default:
		// 没探完任务就结束了：服务重启过，内存里的队列丢了
		r.State = "lost"
	}
	if r.State == "queued" || r.State == "running" {
		if t, ok := embyExtractPausedUntil(); ok {
			r.PausedUntil = &t
		}
	}
	return &r
}

// probeRetryReadyAt 失败 / 防抖跳过的条目最晚什么时候全部能再手动请求；都能了返回 nil
func probeRetryReadyAt(r *jobProbeReport, now time.Time) *time.Time {
	if r == nil {
		return nil
	}
	var latest *time.Time
	for _, it := range r.Items {
		if (it.Kind == probeItemFailed || it.Kind == probeItemHeld) && it.RetryAt != nil && it.RetryAt.After(now) {
			if latest == nil || it.RetryAt.After(*latest) {
				latest = it.RetryAt
			}
		}
	}
	return latest
}

// ---- 全局视图 ----

// probeFailRow 记账里一个没成功的条目
type probeFailRow struct {
	ItemID      string     `json:"item_id"`
	Label       string     `json:"label"`
	Attempts    int        `json:"attempts"`
	LastErr     string     `json:"last_err"`
	LastAt      time.Time  `json:"last_at"`
	AutoRetryAt *time.Time `json:"auto_retry_at,omitempty"` // 自动入口冷却中：最早什么时候会再自动试（前提是再次入库确认）
	AutoStopped bool       `json:"auto_stopped,omitempty"`  // 自动入口次数用完
	ManualAt    *time.Time `json:"manual_at,omitempty"`     // 防抖没过：这个时间之后才能手动重试
	Running     bool       `json:"running,omitempty"`
	Queued      bool       `json:"queued,omitempty"`
}

// probeFailListMax 全局失败清单最多列几条
const probeFailListMax = 100

// EmbyProbeStatus GET /tasks/probe → Emby 提前探测的全局状态：队列、正在探的、熔断、没成功的条目
func (h *Handler) EmbyProbeStatus(c *gin.Context) {
	_, embyOK := loadEmbyRefreshCfg()
	out := gin.H{
		"enabled": embyExtractEnabled(),
		"emby":    embyOK,
		"queue":   embyExtractQueueLen(),
		"running": embyExtractRunningLabel(),
		"limits": gin.H{
			"max_attempts":     embyExtractMaxAttempts,
			"retry_hours":      int(embyExtractRetryAfter.Hours()),
			"debounce_minutes": int(embyExtractDebounce.Minutes()),
			"break_after":      embyExtractBreakAfter,
			"break_minutes":    int(embyExtractBreakPause.Minutes()),
		},
	}
	if t, ok := embyExtractPausedUntil(); ok {
		out["paused_until"] = t
	}
	var marks []model.EmbyExtractMark
	var total, ignored int64
	if h.DB != nil {
		h.DB.Model(&model.EmbyExtractMark{}).Where("ignored_at IS NULL").Count(&total)
		h.DB.Model(&model.EmbyExtractMark{}).Where("ignored_at IS NOT NULL").Count(&ignored)
		h.DB.Where("ignored_at IS NULL").Order("last_at DESC").Limit(probeFailListMax).Find(&marks)
	}
	running, now := embyExtractRunningID(), time.Now()
	rows := make([]probeFailRow, 0, len(marks))
	for i := range marks {
		m := marks[i]
		st := embyProbeStateOf(false, true, &m, embyExtractQueuedItem(m.ItemID), m.ItemID == running, now)
		row := probeFailRow{ItemID: m.ItemID, Label: m.Label, Attempts: m.Attempts, LastErr: m.LastErr, LastAt: m.LastAt,
			AutoRetryAt: st.RetryAt, AutoStopped: st.State == "exhausted", ManualAt: st.ManualAt,
			Running: st.State == "running", Queued: st.State == "queued"}
		if row.LastErr == "请求中" && !row.Running {
			// 发出请求后没等到结果服务就退出了
			row.LastErr = "请求中断（服务重启）"
		}
		rows = append(rows, row)
	}
	out["fails"], out["fail_total"], out["ignored_total"] = rows, total, ignored
	c.JSON(http.StatusOK, out)
}

// IgnoreEmbyProbe POST /tasks/probe/ignore {item_ids | all} → 把失败条目从清单里拿掉。
// 只打 IgnoredAt 标记、不删记账（见 model.EmbyExtractMark.IgnoredAt）；正在探 / 排着的跳过，
// 否则刚点完忽略它又被手动请求清掉标记，界面上看着像没生效
func (h *Handler) IgnoreEmbyProbe(c *gin.Context) {
	var req struct {
		ItemIDs []string `json:"item_ids"`
		All     bool     `json:"all"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (!req.All && len(req.ItemIDs) == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	q := h.DB.Where("ignored_at IS NULL")
	if !req.All {
		q = q.Where("item_id IN ?", req.ItemIDs)
	}
	var marks []model.EmbyExtractMark
	q.Find(&marks)
	running, now := embyExtractRunningID(), time.Now()
	var ids []string
	busy := 0
	for i := range marks {
		m := marks[i]
		if m.ItemID == running || embyExtractQueuedItem(m.ItemID) != probeQueuedNone {
			busy++
			continue
		}
		ids = append(ids, m.ItemID)
	}
	if len(ids) > 0 {
		if err := h.DB.Model(&model.EmbyExtractMark{}).Where("item_id IN ?", ids).Update("ignored_at", now).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	msg := fmt.Sprintf("已忽略 %d 个", len(ids))
	if busy > 0 {
		msg += fmt.Sprintf("，另 %d 个正在探测或排队中，没动", busy)
	}
	log.Printf("[Emby探测] ○ 任务中心忽略 %d 个失败条目（跳过进行中 %d 个）", len(ids), busy)
	c.JSON(http.StatusOK, gin.H{"message": msg, "ignored": len(ids)})
}

// UnignoreEmbyProbe POST /tasks/probe/unignore → 撤销全部忽略：回到失败清单，自动探测按原来的次数与间隔照常判定
func (h *Handler) UnignoreEmbyProbe(c *gin.Context) {
	res := h.DB.Model(&model.EmbyExtractMark{}).Where("ignored_at IS NOT NULL").Update("ignored_at", nil)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": res.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已恢复 %d 个", res.RowsAffected)})
}
