package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 媒体服务器类型（2026-10-28，设计见 docs/115-station-notes/MEDIA-SERVER-PLAN.md）。
//
// Emby 与 Jellyfin 同源，八成接口一样，所以共用 emby*.go 那一套客户端，只在分叉处按类型走：
// 列媒体库（/Library/VirtualFolders/Query）、列用户（/Users/Query）、按路径 / 按 TMDB 编号找条目
// （Jellyfin 的 /Items 不认 Path、AnyProviderIdEquals）、IsVirtualItem、建库的 trickplay 选项。
// 做不了的功能查 mediaServerUnsupported，入口处直接拒绝、不发请求。
//
// 类型存在 setting `emby` 的 kind 字段（沿用老键，免得 25 个文件跟着改）：
// 空 / auto = 读 /System/Info/Public 的 ProductName 自动认（Jellyfin 是「Jellyfin Server」），
// 老部署不用动；用户也可以手动指定
const (
	msEmby     = "emby"
	msJellyfin = "jellyfin"
)

// mediaServerAutoDetect 测试里关掉：几百个用例的假 Emby 不认 /System/Info/Public，
// 多一个请求会撞上它们「意外路径就 Fatal」的守卫
var mediaServerAutoDetect = true

type msDetected struct {
	kind string
	at   time.Time
	ok   bool
}

// msDetectCache 按服务器地址记自动识别的结果：认出来的进程内一直用，失败的 2 分钟后再试
var msDetectCache sync.Map

const msDetectRetry = 2 * time.Minute

// configuredMediaKind 用户手动指定的类型；没指定返回空
func configuredMediaKind() string {
	var cfg struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal([]byte(settingValueCompat("emby")), &cfg)
	switch k := strings.ToLower(strings.TrimSpace(cfg.Kind)); k {
	case msEmby, msJellyfin:
		return k
	}
	return ""
}

// mediaServerKind base 这台媒体服务器是哪家。认不出一律按 Emby（老行为）
func mediaServerKind(base string) string {
	if k := configuredMediaKind(); k != "" {
		return k
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || !mediaServerAutoDetect {
		return msEmby
	}
	if v, ok := msDetectCache.Load(base); ok {
		d := v.(msDetected)
		if d.ok || time.Since(d.at) < msDetectRetry {
			return d.kind
		}
	}
	kind, ok := detectMediaServer(base)
	msDetectCache.Store(base, msDetected{kind: kind, at: time.Now(), ok: ok})
	return kind
}

// detectMediaServer 读公开的 /System/Info/Public（两家都不要鉴权）
func detectMediaServer(base string) (kind string, ok bool) {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/System/Info/Public")
	if err != nil {
		return msEmby, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return msEmby, false
	}
	var info struct {
		ProductName string `json:"ProductName"`
	}
	if json.NewDecoder(resp.Body).Decode(&info) != nil {
		return msEmby, false
	}
	return kindOfProduct(info.ProductName), true
}

func kindOfProduct(name string) string {
	if strings.Contains(strings.ToLower(name), "jellyfin") {
		return msJellyfin
	}
	return msEmby
}

// embyIsJellyfin 已配置的那台是不是 Jellyfin
func embyIsJellyfin() bool {
	cfg, ok := loadEmbyRefreshCfg()
	return ok && mediaServerKind(cfg.ServerURL) == msJellyfin
}

// isJellyfinAt 调用方手里已经有地址时用它（测试连接时表单里的地址可能还没保存）
func isJellyfinAt(base string) bool { return mediaServerKind(base) == msJellyfin }

func mediaServerName(kind string) string {
	if kind == msJellyfin {
		return "Jellyfin"
	}
	return "Emby"
}

// 媒体服务器做不了的功能。key 与前端 utils/mediaServer.ts 的 MS_FEATURES 一一对应
const (
	msFeatPreProbe    = "pre_probe"   // 提前探测（轨道探测）
	msFeatProbeLog    = "probe_log"   // 探测失败时读服务器日志找原因
	msFeatDeepDelete  = "deep_delete" // Emby 删条目 → 删网盘
	msFeatPeopleMerge = "people_merge"
)

// mediaServerUnsupported 这台服务器做不了的功能 → 原因（给界面与入口拒绝时用）
func mediaServerUnsupported(kind string) map[string]string {
	if kind != msJellyfin {
		return map[string]string{}
	}
	return map[string]string{
		// MediaSourceManager.GetPlaybackMediaSources：路径以 .strm 结尾的条目每次 PlaybackInfo 都强制
		// FullRefresh + 远程探测，提前探过播放时照样再探
		msFeatPreProbe: "Jellyfin 每次播放 STRM 都会自己重新探测一次，提前探测没有用，只会多取一次 115 直链",
		msFeatProbeLog: "Jellyfin 的日志接口与 Emby 不同，读不了 ffprobe 的报错",
		// Jellyfin 没有内置 Webhook，插件的删除事件能不能带文件路径还没核实
		msFeatDeepDelete: "Jellyfin 没有内置的删除事件，还不支持",
		// 要删人物条目，没在 Jellyfin 上验证过
		msFeatPeopleMerge: "还没在 Jellyfin 上验证过，暂不开放",
	}
}

// mediaServerBlock 当前服务器做不了 feat 时返回原因
func mediaServerBlock(feat string) string {
	cfg, ok := loadEmbyRefreshCfg()
	if !ok {
		return ""
	}
	return mediaServerUnsupported(mediaServerKind(cfg.ServerURL))[feat]
}

func preProbeBlockErr() error {
	if why := mediaServerBlock(msFeatPreProbe); why != "" {
		return errors.New(why)
	}
	return nil
}

// MediaServerCaps GET /mediaserver/caps —— 当前媒体服务器是哪家、哪些功能用不了。
// 没配地址时 kind 为空，界面照 Emby 显示
func (h *Handler) MediaServerCaps(c *gin.Context) {
	base, _, ok := h.embyServerInfo()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"kind": "", "name": "Emby", "unsupported": map[string]string{}})
		return
	}
	kind := mediaServerKind(base)
	c.JSON(http.StatusOK, gin.H{
		"kind":        kind,
		"name":        mediaServerName(kind),
		"configured":  configuredMediaKind(),
		"unsupported": mediaServerUnsupported(kind),
	})
}

// embyUserList 列用户：Emby 是 /Users/Query 回 {Items:[…]}；Jellyfin 没有它，GET /Users 直接回数组
// （UserController.cs，isDisabled 参数同名），用户对象的 Id / Name / Policy 字段两家一样
func embyUserList[T any](base, key string, q url.Values) ([]T, error) {
	if isJellyfinAt(base) {
		var out []T
		err := embyGetJSON(base, key, "/Users", q, &out)
		return out, err
	}
	var page struct {
		Items []T `json:"Items"`
	}
	err := embyGetJSON(base, key, "/Users/Query", q, &page)
	return page.Items, err
}

// embyRealOnly 只要真有文件的条目（剧集库里「已排播但没有文件」的占位集不算）：
// Emby 用 IsVirtualItem=false；Jellyfin 不认它（ItemsController 参数表里没有，会被忽略），用 IsMissing=false
func embyRealOnly(base string, q url.Values) url.Values {
	if isJellyfinAt(base) {
		q.Set("IsMissing", "false")
	} else {
		q.Set("IsVirtualItem", "false")
	}
	return q
}
