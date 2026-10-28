package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 播放方式：302 直连 / 本机中转 ====================
//
// 设计见 docs/115-station-notes/PROXY-PLAY-PLAN.md。默认 302（老部署零变化）；用户可以把全局改成中转，
// 或者只给某几个 Emby 用户中转（Flowpan 按 Emby 用户设播放策略的思路）。
// 这是用户自己选的显式开关：302 失败照旧明确报错，**不许**退化成中转（1be05e2 删掉的正是那种隐式退化）。

const (
	playbackKey      = "playback"
	playModeRedirect = "redirect"
	playModeProxy    = "proxy"

	playbackDefaultConns = 3
	playbackMaxConns     = 16
)

// playbackCfg setting key "playback"。单独一个 key：STRM 直链配置与 emby 那两个页面整存整取，塞进去会互相覆盖
type playbackCfg struct {
	Mode      string            `json:"mode"`      // 全局默认：redirect / proxy
	Users     map[string]string `json:"users"`     // Emby 用户 id → redirect / proxy；没列的跟随全局
	MaxConns  int               `json:"max_conns"` // 每个 115 账号同时连 CDN 的中转连接上限
	Transcode bool              `json:"transcode"` // 中转的播放允许 Emby 转码（实验，默认关）
}

func (c *playbackCfg) normalize() {
	if c.Mode != playModeProxy {
		c.Mode = playModeRedirect
	}
	if c.MaxConns < 1 {
		c.MaxConns = playbackDefaultConns
	}
	if c.MaxConns > playbackMaxConns {
		c.MaxConns = playbackMaxConns
	}
	users := map[string]string{}
	for id, m := range c.Users {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && (m == playModeRedirect || m == playModeProxy) {
			users[id] = m
		}
	}
	c.Users = users
}

// anyProxy 有没有任何播放会走中转（全局或某个用户）
func (c playbackCfg) anyProxy() bool {
	if c.Mode == playModeProxy {
		return true
	}
	for _, m := range c.Users {
		if m == playModeProxy {
			return true
		}
	}
	return false
}

// 每次起播都要读：缓存 30 秒，保存时失效
var playbackCfgCache struct {
	sync.Mutex
	cfg *playbackCfg
	at  time.Time
}

func loadPlaybackCfg() playbackCfg {
	playbackCfgCache.Lock()
	defer playbackCfgCache.Unlock()
	if playbackCfgCache.cfg != nil && time.Since(playbackCfgCache.at) < 30*time.Second {
		c := *playbackCfgCache.cfg
		return c
	}
	var c playbackCfg
	if v := settingValueCompat(playbackKey); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	c.normalize()
	playbackCfgCache.cfg, playbackCfgCache.at = &c, time.Now()
	return c
}

func forgetPlaybackCfg() {
	playbackCfgCache.Lock()
	playbackCfgCache.cfg = nil
	playbackCfgCache.Unlock()
}

// playReq 一次播放请求里判定要用的事实
type playReq struct {
	EmbyUser       string // 经本站 Emby 反代、核对过的 Emby 用户 id；其余入口为空
	FromEmbyServer bool   // 请求方就是配置的 Emby 服务器（转码 / 提前探测 / 神医 ffprobe）
}

// playProxy 这一次播放走不走中转（纯函数）。规则按先后：
//  1. Emby 服务器自己来拉 → 302：字节一样落在家里那台机器上，不必多绕本站一跳；提前探测的判据也不变
//  2. 经本站 Emby 反代、该用户单独设过 → 用它的
//  3. 否则全局
func playProxy(c playbackCfg, r playReq) bool {
	if r.FromEmbyServer {
		return false
	}
	if r.EmbyUser != "" {
		if m, ok := c.Users[strings.ToLower(r.EmbyUser)]; ok {
			return m == playModeProxy
		}
	}
	return c.Mode == playModeProxy
}

// ---- 哪些 Emby 用户最近经本站反代播放过（内存，重启清零） ----
// 按用户设置只对走 6086/emby 的客户端生效；界面据此标出「没走本站反代」的用户，免得用户以为设了就管用

var embyViaProxySeen = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func markEmbyViaProxy(userID string) {
	if userID == "" {
		return
	}
	embyViaProxySeen.Lock()
	if len(embyViaProxySeen.m) >= 4096 {
		clear(embyViaProxySeen.m)
	}
	embyViaProxySeen.m[strings.ToLower(userID)] = time.Now()
	embyViaProxySeen.Unlock()
}

func embyViaProxyAt(userID string) time.Time {
	embyViaProxySeen.Lock()
	defer embyViaProxySeen.Unlock()
	return embyViaProxySeen.m[strings.ToLower(userID)]
}

// ---- 接口 ----

// PlaybackGetConfig GET /playback/config
func (h *Handler) PlaybackGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, loadPlaybackCfg())
}

// PlaybackSaveConfig POST /playback/config：整份覆盖（只有这一个页面写这个 key）
func (h *Handler) PlaybackSaveConfig(c *gin.Context) {
	var req playbackCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := checkPlaybackCfg(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.normalize()
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting(playbackKey, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	forgetPlaybackCfg()
	n := 0
	for _, m := range req.Users {
		if m == playModeProxy {
			n++
		}
	}
	logf := map[string]string{playModeRedirect: "302 直连", playModeProxy: "本机中转"}
	vlog("[播放] ✓ 播放方式已保存：默认%s，%d 个 Emby 用户单独中转，每账号最多 %d 个连接，转码 %v",
		logf[req.Mode], n, req.MaxConns, req.Transcode)
	c.JSON(http.StatusOK, req)
}

// checkPlaybackCfg 只拦写错的值；空值由 normalize 补默认
func checkPlaybackCfg(c playbackCfg) error {
	if c.Mode != "" && c.Mode != playModeRedirect && c.Mode != playModeProxy {
		return fmt.Errorf("播放方式只能是 302 直连或本机中转")
	}
	if c.MaxConns != 0 && (c.MaxConns < 1 || c.MaxConns > playbackMaxConns) {
		return fmt.Errorf("同时连接上限要在 1 到 %d 之间", playbackMaxConns)
	}
	for id, m := range c.Users {
		if strings.TrimSpace(id) == "" || (m != playModeRedirect && m != playModeProxy) {
			return fmt.Errorf("用户的播放方式只能是 302 直连或本机中转")
		}
	}
	return nil
}

type playbackEmbyUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Admin    bool   `json:"admin"`
	Disabled bool   `json:"disabled"`
	LastSeen string `json:"last_seen,omitempty"` // Emby 记的最近活动
	ViaProxy string `json:"via_proxy,omitempty"` // 最近一次经本站反代播放（本站内存，重启清零）
}

// PlaybackEmbyUsers GET /playback/emby-users：列出 Emby 用户给「按 Emby 用户」用（只读 Emby，零 115 请求）
func (h *Handler) PlaybackEmbyUsers(c *gin.Context) {
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false, "users": []playbackEmbyUser{}})
		return
	}
	type embyUser struct {
		ID               string `json:"Id"`
		Name             string `json:"Name"`
		LastActivityDate string `json:"LastActivityDate"`
		Policy           struct {
			IsAdministrator bool `json:"IsAdministrator"`
			IsDisabled      bool `json:"IsDisabled"`
		} `json:"Policy"`
	}
	var page struct{ Items []embyUser }
	var err error
	if page.Items, err = embyUserList[embyUser](base, key, url.Values{}); err != nil {
		c.JSON(http.StatusOK, gin.H{"configured": true, "error": err.Error(), "users": []playbackEmbyUser{}})
		return
	}
	users := make([]playbackEmbyUser, 0, len(page.Items))
	for _, u := range page.Items {
		if u.ID == "" {
			continue
		}
		pu := playbackEmbyUser{ID: strings.ToLower(u.ID), Name: u.Name, Admin: u.Policy.IsAdministrator,
			Disabled: u.Policy.IsDisabled, LastSeen: u.LastActivityDate}
		if at := embyViaProxyAt(u.ID); !at.IsZero() {
			pu.ViaProxy = at.Format(time.RFC3339)
		}
		rememberEmbyUserName(u.ID, u.Name)
		users = append(users, pu)
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "users": users})
}

// Emby 用户名（中转状态里显示「谁在看」）：读用户列表与播放鉴权时顺手记下
var embyUserNames = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func rememberEmbyUserName(id, name string) {
	if id == "" || name == "" {
		return
	}
	embyUserNames.Lock()
	if len(embyUserNames.m) >= 4096 {
		clear(embyUserNames.m)
	}
	embyUserNames.m[strings.ToLower(id)] = name
	embyUserNames.Unlock()
}

func embyUserName(id string) string {
	embyUserNames.Lock()
	defer embyUserNames.Unlock()
	return embyUserNames.m[strings.ToLower(id)]
}
