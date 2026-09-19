package api

import (
	"net/http"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 增量同步状态页 ====================
//
// 改造之后增量走 30 秒独立轮询，而且空转轮次刻意静默、也不占「当前任务」
// 那个位置 —— 好处是日志干净，代价是「为什么没同步」这个问题只能翻日志猜。
// 尤其是「115 生活」事件开关的门禁结论，此前只活在内存里，界面上根本看不到。
//
// 这里把判断需要的东西一次给全：门禁、当前通道、游标、上一轮结果、积压量。

var (
	lastRoundMu  sync.Mutex
	lastRoundAt  time.Time
	lastRoundSum *incrSummary
	lastRoundErr string
)

// noteIncrRound 记一轮增量的结果。每一轮都记，包括熔断与失败的那些 ——
// 状态页要回答的正是「为什么没动静」
func noteIncrRound(sum *incrSummary, err error) {
	lastRoundMu.Lock()
	defer lastRoundMu.Unlock()
	lastRoundAt = time.Now()
	lastRoundSum = sum
	lastRoundErr = ""
	if err != nil {
		lastRoundErr = err.Error()
	}
}

// IncrStatus 增量同步状态
// GET /sync/incr-status
func (h *Handler) IncrStatus(c *gin.Context) {
	gateOK, gateMsg, gateAt := lifeGateStatus()
	gate := gin.H{"ok": gateOK, "message": gateMsg}
	if !gateAt.IsZero() {
		gate["checked_at"] = gateAt.Format("01-02 15:04:05")
	} else {
		gate["message"] = "尚未体检（第一轮增量跑起来后才会检查）"
	}

	f := &lifeFetcher{load: h.getSettingValue}
	endpoint := "主通道"
	if f.currentApp() == "web" {
		endpoint = "备用通道（主通道被限流，24 小时内不再尝试）"
	}

	cur := parseLifeCursor(h.getSettingValue("incr-cursor"))
	cursor := gin.H{"from_id": cur.FromID}
	if cur.FromTime > 0 {
		cursor["from_time"] = time.Unix(cur.FromTime, 0).Format("01-02 15:04:05")
	}

	var pendingEvents, pathRows int64
	h.DB.Model(&model.SyncEvent{}).Where("status = ?", "pending").Count(&pendingEvents)
	h.DB.Model(&model.PathCache{}).Count(&pathRows)

	lastRoundMu.Lock()
	round := gin.H{}
	if !lastRoundAt.IsZero() {
		round["at"] = lastRoundAt.Format("01-02 15:04:05")
		round["error"] = lastRoundErr
		if lastRoundSum != nil {
			round["summary"] = lastRoundSum
		}
	}
	lastRoundMu.Unlock()

	interval := int(h.loadIncrInterval() / time.Second)

	c.JSON(http.StatusOK, gin.H{
		"life_gate":      gate,
		"endpoint":       endpoint,
		"cursor":         cursor,
		"last_round":     round,
		"pending_events": pendingEvents,
		"path_cache":     pathRows,
		"interval_sec":   interval,
	})
}

// IncrProbe 事件流探针：只拉不处理，看看通不通、拿得到什么。
// POST /sync/incr-probe
//
// 不碰游标、不落库、不动本地文件 —— 纯读，随便点
func (h *Handler) IncrProbe(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 115 账号：" + err.Error()})
		return
	}
	f := &lifeFetcher{
		cookie: cookie,
		load:   h.getSettingValue,
		save: func(k, v string) {
			if h.Config != nil {
				h.Config.SaveSetting(k, v)
			}
		},
	}
	// 顺手把「115 生活」开关打开：关着的话下面一定拉到空，
	// 而用户看到「0 条」是分不清「没有变动」还是「开关没开」的
	if err := enable115Life(cookie); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "打开「115 生活」事件开关失败：" + err.Error()})
		return
	}
	evs, _, err := f.page(10, 0)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "拉取事件失败：" + err.Error()})
		return
	}
	list := make([]gin.H, 0, len(evs))
	for _, ev := range evs {
		at := ""
		if ts := parseUnixStr(ev.Time); ts > 0 {
			at = time.Unix(ts, 0).Format("01-02 15:04:05")
		}
		kind := "文件"
		if ev.FileCat == "0" {
			kind = "目录"
		}
		list = append(list, gin.H{
			"id": ev.ID, "type": ev.Type, "name": ev.FileName, "kind": kind, "at": at,
			"ignored": lifeIgnoreTypes[ev.Type],
		})
	}
	msg := "事件流正常"
	if len(evs) == 0 {
		msg = "接口通，但最近没有任何事件。若确定刚在网盘上动过文件，请检查 115 客户端里的「生活」是否被关掉"
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "events": list})
}
