package api

// ==================== RE0 每日签到 ====================
//
// POST /api/open/checkin（write 权限，见 docs/RE0 OpenAPI 文档.md）：普通模式随机 4–10 积分。
// 当天已签到仍回 200，checked_in=false、points=0，所以「已签过」也算这一天完成。
// 文档里的 is_gambler（高波动、可能扣分）不开放：订阅的自动解锁靠积分，不拿它赌。
//
// 调度：不用 cron，每 10 分钟看一次，08:00 之后、今天还没签成就签；失败一小时后再试。
// 这样重启、断网、错过整点都不会漏一天，也不会在 0 点一过就和所有人一起挤。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// re0CheckinCfg setting key "re0checkin"：与 re0 配置分开存，签到结果落盘不会和 Token 刷新互相覆盖
type re0CheckinCfg struct {
	Enabled      bool   `json:"enabled"`
	LastDone     string `json:"last_done"`      // 最近签成（或确认已签）的日期 yyyy-mm-dd
	LastResult   string `json:"last_result"`    // 最近一次执行结果文案
	LastResultAt string `json:"last_result_at"` // 最近一次执行时间
}

const (
	re0CheckinHour  = 8                // 每天几点之后签
	re0CheckinRetry = time.Hour        // 失败多久后再试
	re0CheckinTick  = 10 * time.Minute // 调度间隔
)

var (
	re0CheckinMu    sync.Mutex // 配置读写
	re0CheckinRunMu sync.Mutex // 同一时间只签一次（定时与手动撞上）
	re0CheckinTried atomic.Int64 // 定时上次失败的时间（unix 秒；内存即可，重启后马上再试一次无妨）
)

func loadRe0CheckinCfg() re0CheckinCfg {
	re0CheckinMu.Lock()
	defer re0CheckinMu.Unlock()
	var cfg re0CheckinCfg
	if v := settingValueCompat("re0checkin"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	return cfg
}

// updateRe0CheckinCfg 读-改-写：结果落盘与界面开关同时发生时不丢字段
func updateRe0CheckinCfg(fn func(*re0CheckinCfg)) error {
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	re0CheckinMu.Lock()
	defer re0CheckinMu.Unlock()
	var cfg re0CheckinCfg
	if v := settingValueCompat("re0checkin"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	return notifyConfigSource.SaveSetting("re0checkin", string(b))
}

// re0CheckinResult 签到返回
type re0CheckinResult struct {
	CheckedIn bool   `json:"checked_in"`
	Message   string `json:"message"`
	Points    int    `json:"points"`
}

// re0CheckinReady 能不能签：授权了、而且授权里有 write。不能就返回原因
func re0CheckinReady(cfg *re0Cfg) string {
	switch {
	case cfg.ClientSecret == "":
		return "未配置 RE0 应用"
	case cfg.AccessToken == "":
		return "未授权 RE0 账号"
	case !cfg.grantedScopes()["write"]:
		return "授权里没有签到需要的 write 权限，请在 RE0 设置里重新授权一次"
	}
	return ""
}

// runRe0Checkin 签一次。ok = 今天已经签上了（含本来就签过）。notify = 定时触发，结果推通知
func (h *Handler) runRe0Checkin(notify bool) (ok bool, msg string) {
	re0CheckinRunMu.Lock()
	defer re0CheckinRunMu.Unlock()
	now := time.Now()
	defer func() {
		if ok {
			log.Printf("[RE0签到] ✓ %s", msg)
		} else {
			log.Printf("[RE0签到] ✗ %s", msg)
		}
		if err := updateRe0CheckinCfg(func(c *re0CheckinCfg) {
			c.LastResult, c.LastResultAt = msg, now.Format("2006-01-02 15:04")
			if ok {
				c.LastDone = now.Format("2006-01-02")
			}
		}); err != nil {
			log.Printf("[RE0签到] ○ 结果落盘失败: %v", err)
		}
		if notify {
			title := "RE0 签到失败"
			if ok {
				title = "RE0 签到成功"
			}
			NotifyMessage(title, msg)
		}
	}()

	cfg := loadRe0Cfg()
	if why := re0CheckinReady(cfg); why != "" {
		return false, why
	}
	var res re0CheckinResult
	if err := re0Call(h, cfg, http.MethodPost, "/api/open/checkin", nil, map[string]bool{"is_gambler": false}, &res); err != nil {
		return false, "签到失败：" + err.Error()
	}
	switch {
	case res.CheckedIn:
		msg = fmt.Sprintf("签到成功，获得 %d 积分", res.Points)
	case strings.Contains(res.Message, "已签") || res.Points == 0:
		// 当天已签：站方仍回 200，checked_in=false
		msg = firstNonEmpty(res.Message, "今日已签到")
	default:
		return false, firstNonEmpty(res.Message, "签到未成功")
	}
	if me, err := re0FetchMe(h, cfg); err == nil && me.Points != nil {
		msg += fmt.Sprintf("，当前积分 %d", *me.Points)
	}
	return true, msg
}

// re0CheckinDue 定时这一刻该不该签（纯函数，测试用）
func re0CheckinDue(c re0CheckinCfg, now, lastFail time.Time) bool {
	if !c.Enabled || c.LastDone == now.Format("2006-01-02") || now.Hour() < re0CheckinHour {
		return false
	}
	return lastFail.IsZero() || now.Sub(lastFail) >= re0CheckinRetry
}

// StartRe0CheckinScheduler 每 10 分钟看一次该不该签
func StartRe0CheckinScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(re0CheckinTick)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.re0CheckinTick()
		}
	}()
}

func (h *Handler) re0CheckinTick() {
	now := time.Now()
	var lastFail time.Time
	if t := re0CheckinTried.Load(); t > 0 {
		lastFail = time.Unix(t, 0)
	}
	if !re0CheckinDue(loadRe0CheckinCfg(), now, lastFail) {
		return
	}
	// 授权缺权限 / 未授权：不推通知，界面上看得到原因
	if why := re0CheckinReady(loadRe0Cfg()); why != "" {
		re0CheckinTried.Store(now.Unix())
		return
	}
	if ok, _ := h.runRe0Checkin(true); ok {
		re0CheckinTried.Store(0)
	} else {
		re0CheckinTried.Store(now.Unix())
	}
}

// ==================== HTTP 接口 ====================

// Re0CheckinGet GET /re0/checkin → 开关 + 最近结果（不请求 RE0）
func (h *Handler) Re0CheckinGet(c *gin.Context) {
	cfg := loadRe0CheckinCfg()
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"last_done":      cfg.LastDone,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"done_today":     cfg.LastDone == time.Now().Format("2006-01-02"),
		"hour":           re0CheckinHour,
		"blocked":        re0CheckinReady(loadRe0Cfg()),
	})
}

// Re0CheckinSave POST /re0/checkin {enabled}
func (h *Handler) Re0CheckinSave(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := updateRe0CheckinCfg(func(cfg *re0CheckinCfg) { cfg.Enabled = req.Enabled }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	re0CheckinTried.Store(0)
	log.Printf("[配置] RE0 每日签到：%v", req.Enabled)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// Re0CheckinRun POST /re0/checkin/run → 立即签到
func (h *Handler) Re0CheckinRun(c *gin.Context) {
	ok, msg := h.runRe0Checkin(false)
	if !ok {
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}
