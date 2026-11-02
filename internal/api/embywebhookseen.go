package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== Emby Webhook 收到过什么 ====================
//
// 深度删除靠 Emby 推删除事件，可 Emby 的 REST 接口查不到它的 Webhook 是怎么配的
// （见 docs/emby-api：通知服务里没有 Webhook 的配置），没配、没勾删除事件、token 填错，
// 本站看起来都是「什么也没收到」。所以只能反过来记：最近一次收到任何事件、收到删除事件、
// 收到 Emby 的测试事件、因为 token / 格式不对被拒，各是什么时候。
// 「Strm 管理 → 深度删除」据此提醒用户去 Emby 里补配置（2026-10-23）。
//
// 存 setting（重启不丢）。播放类事件一分钟能来好几条，「任何事件」最多 10 分钟写一次；
// 删除、测试、被拒每次都写 —— 它们稀少，而且正是要回答的问题

const embyWebhookSeenKey = "emby-webhook-seen"

type embyWebhookSeen struct {
	Any       int64  `json:"any,omitempty"`    // 最近一次收到任何事件（unix 秒）
	Delete    int64  `json:"delete,omitempty"` // 删除事件（library.deleted / deep.delete …）
	Test      int64  `json:"test,omitempty"`   // Emby 里点「测试」发来的
	RejectAt  int64  `json:"reject_at,omitempty"`
	RejectWhy string `json:"reject_why,omitempty"`
}

var webhookSeen struct {
	sync.Mutex
	v       embyWebhookSeen
	loaded  bool
	savedAt time.Time
}

const webhookSeenSaveEvery = 10 * time.Minute

// noteEmbyWebhook 记一笔。kind：any / delete / test / reject（why 只给 reject）。
// delete / test 也算「收到过事件」
func (h *Handler) noteEmbyWebhook(kind, why string) {
	webhookSeen.Lock()
	defer webhookSeen.Unlock()
	h.loadWebhookSeenLocked()
	now := time.Now()
	ts := now.Unix()
	urgent := true
	switch kind {
	case "reject":
		webhookSeen.v.RejectAt, webhookSeen.v.RejectWhy = ts, why
	case "delete":
		webhookSeen.v.Delete, webhookSeen.v.Any = ts, ts
	case "test":
		webhookSeen.v.Test, webhookSeen.v.Any = ts, ts
	default:
		webhookSeen.v.Any = ts
		urgent = now.Sub(webhookSeen.savedAt) >= webhookSeenSaveEvery
	}
	if !urgent || h.Config == nil {
		return
	}
	b, _ := json.Marshal(webhookSeen.v)
	if err := h.Config.SaveSetting(embyWebhookSeenKey, string(b)); err == nil {
		webhookSeen.savedAt = now
	}
}

func (h *Handler) loadWebhookSeenLocked() {
	if webhookSeen.loaded {
		return
	}
	webhookSeen.loaded = true
	if raw := h.getSettingValue(embyWebhookSeenKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &webhookSeen.v)
	}
}

// embyWebhookToken 消息配置里的 webhook token：优先专用字段（界面自动生成），兼容旧版存在完整 URL 里的
func (h *Handler) embyWebhookToken() string {
	var cfg struct {
		Webhook string `json:"webhook"`
		Token   string `json:"token"`
	}
	if v := h.getSettingValue("emby-notify"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	if t := strings.TrimSpace(cfg.Token); t != "" {
		return t
	}
	if cfg.Webhook != "" {
		if u, err := url.Parse(cfg.Webhook); err == nil {
			return u.Query().Get("token")
		}
	}
	return ""
}

// embyPlayNotifyOn 播放 / 暂停 / 停止事件要不要推通知（emby-notify 的 play_notify，默认关）。
// 原来只要 Emby 里勾了播放事件就推，一部片拖一下进度就是好几条，不需要的用户嫌吵；
// 和 token 存在同一个 key 里，前端保存时两项一起带上
func (h *Handler) embyPlayNotifyOn() bool {
	var cfg struct {
		PlayNotify bool `json:"play_notify"`
	}
	if v := h.getSettingValue("emby-notify"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg.PlayNotify
}

// EmbyWebhookStatus GET /emby/webhook-status：token 生成了没有、最近收到过什么
func (h *Handler) EmbyWebhookStatus(c *gin.Context) {
	webhookSeen.Lock()
	h.loadWebhookSeenLocked()
	v := webhookSeen.v
	webhookSeen.Unlock()
	rfc := func(ts int64) string {
		if ts <= 0 {
			return ""
		}
		return time.Unix(ts, 0).Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, gin.H{
		"token_set":  h.embyWebhookToken() != "",
		"any":        rfc(v.Any),
		"delete":     rfc(v.Delete),
		"test":       rfc(v.Test),
		"reject_at":  rfc(v.RejectAt),
		"reject_why": v.RejectWhy,
	})
}
