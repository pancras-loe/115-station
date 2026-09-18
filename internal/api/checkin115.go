package api

// ==================== 115 每日签到 ====================
//
// 接口（p115client user_points_sign 同款）：
//   GET  https://proapi.115.com/android/2.0/user/points_sign  查今日签到状态
//   POST https://proapi.115.com/android/2.0/user/points_sign  签到
//        token = sha1("{user_id}-Points_Sign@#115-{t}")，token_time = t
//   奖励：每日签到得积分（连续签到有加成），入口在 115 App「积分中心」。
//   注意：该接口按 Cookie 通道走（与全站同款统一 UA，保持会话一致性）。
//
// 调度：cron 表达式（与增量同步同一套解析），到点执行；签到内部失败重试
// 3 次（间隔 3 秒），结果推送企微/TG 通知。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const sign115API = "https://proapi.115.com/android/2.0/user/points_sign"

const checkin115DefaultCron = "0 8 * * *"

// 签到配置（setting key "115checkin"）
type checkin115Cfg struct {
	Enabled      bool   `json:"enabled"`
	Cron         string `json:"cron"`           // 执行计划（5 段 cron）
	LastRun      string `json:"last_run"`       // 最近一次触发的分钟（去重用，yyyy-mm-dd hh:mm）
	LastDone     string `json:"last_done"`      // 最近签到成功的日期 yyyy-mm-dd
	LastResult   string `json:"last_result"`    // 最近一次执行结果文案
	LastResultAt string `json:"last_result_at"` // 最近一次执行时间
}

var (
	checkin115Mu sync.Mutex
	checkin115V  *checkin115Cfg
	checkin115At time.Time
	checkinRunMu sync.Mutex
)

func loadCheckin115Cfg() *checkin115Cfg {
	checkin115Mu.Lock()
	defer checkin115Mu.Unlock()
	if checkin115V != nil && time.Since(checkin115At) < 10*time.Second {
		return checkin115V
	}
	cfg := &checkin115Cfg{Cron: checkin115DefaultCron}
	if v := settingValueCompat("115checkin"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = checkin115DefaultCron
	}
	checkin115V = cfg
	checkin115At = time.Now()
	return cfg
}

func saveCheckin115Cfg(cfg *checkin115Cfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("115checkin", string(b)); err != nil {
		return err
	}
	checkin115Mu.Lock()
	checkin115V = cfg
	checkin115At = time.Now()
	checkin115Mu.Unlock()
	return nil
}

// checkin115CronValid 校验 cron 表达式（借助下次执行时间解析，解析不出即无效）
func checkin115CronValid(expr string) bool {
	return !nextCronTime(expr, time.Now()).IsZero()
}

// sign115Status 查询今日签到状态（is_sign_today: 1=已签）
func sign115Status(cookie string) (signed bool, err error) {
	body, err := httpGet115UA(sign115API, nil, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return false, err
	}
	var out struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			IsSignToday int `json:"is_sign_today"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil {
		return false, fmt.Errorf("响应解析失败: %s", truncateStr(string(body), 100))
	}
	if !out.State {
		return false, fmt.Errorf("%s", out.Error)
	}
	return out.Data.IsSignToday == 1, nil
}

// run115CheckinOnce 执行一次签到：查状态 → 签到（重试 3 次，间隔 3 秒）。
// notify=true 时把结果推送企微/TG（定时触发用；手动触发走界面返回）。
func (h *Handler) run115CheckinOnce(notify bool) (bool, string) {
	checkinRunMu.Lock()
	defer checkinRunMu.Unlock()

	cookie, err := h.get115Cookie()
	if err != nil || cookie == "" {
		msg := "未绑定 115 账号，无法签到"
		log.Printf("[115签到] ✗ %s", msg)
		return false, msg
	}

	// 1. 今日是否已签
	if signed, err := sign115Status(cookie); err == nil && signed {
		msg := "今日已签到，无需重复签到"
		log.Printf("[115签到] ○ %s", msg)
		return true, msg
	}

	// 2. 执行签到（带 token 防重放，失败重试）
	uid := cookieUserID(cookie)
	ok := false
	var msg string
	for attempt := 1; attempt <= 3; attempt++ {
		t := time.Now().Unix()
		sum := sha1.Sum([]byte(fmt.Sprintf("%d-Points_Sign@#115-%d", uid, t)))
		rbody, err := httpPostForm115(sign115API, url.Values{
			"token":      {hex.EncodeToString(sum[:])},
			"token_time": {fmt.Sprint(t)},
		}, cookie, 15*time.Second)
		if err != nil {
			msg = "请求失败: " + err.Error()
		} else {
			var r struct {
				State bool   `json:"state"`
				Error string `json:"error"`
				Data  struct {
					ContinuousDay int `json:"continuous_day"`
					PointsNum     int `json:"points_num"`
				} `json:"data"`
			}
			if json.Unmarshal(rbody, &r) == nil {
				switch {
				case r.State:
					ok = true
					msg = fmt.Sprintf("签到成功，连续签到 %d 天，获得 %d 积分", r.Data.ContinuousDay, r.Data.PointsNum)
				case strings.Contains(r.Error, "已签到"):
					ok = true
					msg = "今日已签到，无需重复签到"
				default:
					msg = r.Error
				}
			} else {
				msg = "响应解析失败: " + truncateStr(string(rbody), 100)
			}
		}
		if ok {
			break
		}
		if attempt < 3 {
			log.Printf("[115签到] ○ 第 %d/3 次失败: %s，3 秒后重试", attempt, msg)
			time.Sleep(3 * time.Second)
		}
	}

	if ok {
		log.Printf("[115签到] ✓ %s", msg)
	} else {
		log.Printf("[115签到] ✗ %s", msg)
	}
	if notify {
		title := "115 签到失败"
		if ok {
			title = "115 签到成功"
		}
		NotifyMessage(title, msg)
	}
	return ok, msg
}

// ==================== 调度器 ====================

// Start115CheckinScheduler 分钟级 tick：cron 命中时执行每日签到
func Start115CheckinScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.checkinTick()
		}
	}()
	log.Println("[调度] 115 签到调度器已启动（cron 触发）")
}

func (h *Handler) checkinTick() {
	cfg := loadCheckin115Cfg()
	if !cfg.Enabled {
		return
	}
	now := time.Now()
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return // 本分钟已触发过
	}
	if !CronMatch(cfg.Cron, now) {
		return
	}
	// cfg 是共享缓存指针，不能原地写字段（与 HTTP 读侧构成竞态）：
	// 拷贝后落盘，save 会刷新缓存
	nc := *cfg
	nc.LastRun = minute
	_ = saveCheckin115Cfg(&nc)
	ok, _ := h.run115CheckinOnce(true)
	if ok {
		cfg = loadCheckin115Cfg()
		nc = *cfg
		nc.LastDone = now.Format("2006-01-02")
		_ = saveCheckin115Cfg(&nc)
	}
}

// ==================== HTTP 接口 ====================

// Checkin115GetConfig GET /115checkin/config → 配置 + 今日签到状态（实时查询）
func (h *Handler) Checkin115GetConfig(c *gin.Context) {
	cfg := loadCheckin115Cfg()
	signedToday := false
	statusErr := ""
	if cookie, err := h.get115Cookie(); err == nil && cookie != "" {
		if signed, serr := sign115Status(cookie); serr == nil {
			signedToday = signed
		} else if serr.Error() != "" {
			statusErr = serr.Error()
		}
	} else {
		statusErr = "未绑定 115 账号"
	}
	next := ""
	if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
		next = t.Format("01-02 15:04")
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"cron":           cfg.Cron,
		"next_run":       next,
		"last_done":      cfg.LastDone,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"signed_today":   signedToday,
		"status_error":   statusErr,
	})
}

// Checkin115SaveConfig POST /115checkin/config {enabled, cron}
func (h *Handler) Checkin115SaveConfig(c *gin.Context) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Cron    string `json:"cron"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = checkin115DefaultCron
	}
	if !checkin115CronValid(cronExpr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 0 8 * * *）"})
		return
	}
	cfg := loadCheckin115Cfg()
	cfg.Enabled = req.Enabled
	cfg.Cron = cronExpr
	if err := saveCheckin115Cfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[配置] 115 签到：%v，cron %s", cfg.Enabled, cfg.Cron)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// Checkin115Run POST /115checkin/run → 立即签到
func (h *Handler) Checkin115Run(c *gin.Context) {
	ok, msg := h.run115CheckinOnce(false)
	if !ok {
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}
