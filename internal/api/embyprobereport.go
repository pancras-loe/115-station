package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ==================== Emby 提前探测的结果回报（任务中心）====================
//
// 提前探测在刮削任务结束之后才由单 worker 慢慢跑（embyextract.go），此前结果只进日志与片目详情：
// 任务中心显示「完成」，探测失败了、近期请求过被跳过了、Emby 还没入库，用户都看不到，
// 更不知道什么时候会再试。现在：
//   - 刮削任务排进来的路径带着任务 id，每个条目的结果写回那个任务（TaskJob.Probe），
//     有条目失败时把任务从「完成」改成「部分失败」
//   - 任务中心「当前状态」另有全局视图（GET /tasks/probe）：队列、正在探哪一集、熔断暂停到几点、
//     记账里所有没成功的条目与下次能再试的时间 —— 入库确认排进来的探测没有任务可挂，靠这里看
//
// 「下次什么时候再试」一律由 embyProbeStateOf 按记账算，与片目详情、worker 的放行判定同一套。

const (
	probeItemFailed  = "failed"  // 这次请求了，失败 / 超时
	probeItemHeld    = "held"    // 近期请求过或次数用完，这次没请求
	probeItemMissing = "missing" // Emby 里还没有这个片目
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
	RetryAt  *time.Time `json:"retry_at,omitempty"` // 最早什么时候会再探（Final 时是记账过期、再给一次机会的时间）
	Final    bool       `json:"final,omitempty"`    // 次数用完，不再自动探测
}

// jobProbeReport 一个任务排进来的探测结果
type jobProbeReport struct {
	Paths     int            `json:"paths"`    // 排进来的片目（路径）数
	Finished  int            `json:"finished"` // 已处理完的路径
	OK        int            `json:"ok"`
	Failed    int            `json:"failed"`
	Held      int            `json:"held"`
	Missing   int            `json:"missing"`
	Errors    int            `json:"errors"`
	Items     []jobProbeItem `json:"items,omitempty"`
	More      int            `json:"more,omitempty"` // 超出 probeReportMaxItems 没列的
	UpdatedAt time.Time      `json:"updated_at"`

	// 以下读取时现算，不落库
	State       string     `json:"state,omitempty"` // queued / running / done / lost
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

var probeReports = struct {
	sync.Mutex
	m map[uint]*jobProbeReport
}{m: map[uint]*jobProbeReport{}}

// probeReportLoad 取内存里的报告；没有就从库里接着上次的（同一任务分两次排进来）
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

// probeReportUpdate 改一份报告并落库；有失败时把任务从「完成」改成「部分失败」。
// 路径全部处理完就从内存里拿掉
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
			if r.Failed+r.Errors > 0 {
				// 任务早就结束了（探测排在它之后）：只动「完成」的，取消 / 失败的保持原样
				if res := model.DB.Model(&model.TaskJob{}).Where("id = ? AND status = ?", id, jobSuccess).
					Update("status", jobPartial); res.RowsAffected > 0 {
					log.Printf("[Emby探测] ⚠ 任务 #%d 有条目探测失败，标为部分失败（任务中心详情里有原因与下次重试时间）", id)
				}
			}
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

// probeHeldItem 按记账说清这个条目为什么没探 / 探失败了、什么时候会再试
func probeHeldItem(it embyExtractItem) jobProbeItem {
	out := jobProbeItem{Label: it.label(), Kind: probeItemHeld}
	m, ok := embyExtractLoad(it.ID)
	if !ok {
		return out
	}
	st := embyProbeStateOf(false, true, &m, false, false, time.Now())
	out.Err, out.Attempts, out.RetryAt, out.Final = st.LastErr, st.Attempts, st.RetryAt, st.State == "exhausted"
	return out
}

// probeReportOf 任务详情 / 列表里显示的探测结果；没排过探测的返回 nil
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
	default:
		r.State = embyExtractJobState(job.ID)
		if r.State == "" {
			// 路径没探完、队列里也没有它了：服务重启过，内存里的队列丢了
			r.State = "lost"
		}
	}
	if r.State == "queued" || r.State == "running" {
		if t, ok := embyExtractPausedUntil(); ok {
			r.PausedUntil = &t
		}
	}
	return &r
}

// ---- 全局视图 ----

// probeFailRow 记账里一个没成功的条目
type probeFailRow struct {
	ItemID   string     `json:"item_id"`
	Label    string     `json:"label"`
	Attempts int        `json:"attempts"`
	LastErr  string     `json:"last_err"`
	LastAt   time.Time  `json:"last_at"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	Final    bool       `json:"final,omitempty"`
	Running  bool       `json:"running,omitempty"`
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
			"max_attempts": embyExtractMaxAttempts,
			"retry_hours":  int(embyExtractRetryAfter.Hours()),
			"prune_days":   int(embyMarkPruneAfter.Hours() / 24),
		},
	}
	if t, ok := embyExtractPausedUntil(); ok {
		out["paused_until"] = t
	}
	var marks []model.EmbyExtractMark
	var total int64
	if h.DB != nil {
		h.DB.Model(&model.EmbyExtractMark{}).Count(&total)
		h.DB.Order("last_at DESC").Limit(probeFailListMax).Find(&marks)
	}
	running, now := embyExtractRunningID(), time.Now()
	rows := make([]probeFailRow, 0, len(marks))
	for i := range marks {
		m := marks[i]
		st := embyProbeStateOf(false, true, &m, false, m.ItemID == running, now)
		row := probeFailRow{ItemID: m.ItemID, Label: m.Label, Attempts: m.Attempts, LastErr: m.LastErr, LastAt: m.LastAt,
			RetryAt: st.RetryAt, Final: st.State == "exhausted", Running: st.State == "running"}
		if row.LastErr == "请求中" && !row.Running {
			// 发出请求后没等到结果服务就退出了
			row.LastErr = "请求中断（服务重启）"
		}
		rows = append(rows, row)
	}
	out["fails"], out["fail_total"] = rows, total
	c.JSON(http.StatusOK, out)
}
