package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var embyTokenPattern = regexp.MustCompile(`(?i)(?:^|[,\s])Token\s*=\s*"([^"]+)"`)
var embyMediaRoute = regexp.MustCompile(`(?i)^/(videos|audio|items)/([a-z0-9_-]+)/([^/]+)(?:/.*)?$`)

func queryFold(q url.Values, key string) string {
	for k, v := range q {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func embyClientToken(req *http.Request) string {
	for _, name := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if token := req.Header.Get(name); token != "" {
			return token
		}
	}
	for _, name := range []string{"Authorization", "X-Emby-Authorization"} {
		v := req.Header.Get(name)
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
		if match := embyTokenPattern.FindStringSubmatch(v); len(match) > 1 {
			return match[1]
		}
	}
	for _, name := range []string{"api_key", "X-Emby-Token"} {
		if token := queryFold(req.URL.Query(), name); token != "" {
			return token
		}
	}
	return ""
}

func embyStreamURL(req *http.Request, itemID, sourceID string) string {
	q := url.Values{"Static": {"true"}}
	if sourceID != "" {
		q.Set("MediaSourceId", sourceID)
	}
	if token := embyClientToken(req); token != "" {
		q.Set("api_key", token)
	}
	if id := embyPlaybackUserID(req); id != "" {
		q.Set("UserId", id)
	}
	// 参考 MediaWarp/playbackInfo.go：相对地址由客户端沿用访问协议和反代前缀，
	// 不读取不可信的 X-Forwarded-Host/Proto，也不把外部 CDN 改成本机。
	return "/Videos/" + url.PathEscape(itemID) + "/stream?" + q.Encode()
}

func mappedPlaybackPath(cfg *config.Config, p string) string {
	if cfg != nil {
		var settings struct {
			PathMapping string `json:"path_mapping"`
		}
		if json.Unmarshal([]byte(cfg.GetSetting("emby")), &settings) == nil {
			return embyPathToLocal(settings.PathMapping, p)
		}
	}
	return p
}

func playbackRelativePath(db *gorm.DB, cfg *config.Config, p string) string {
	root := (&Handler{DB: db, Config: cfg}).orphanLocalRoot()
	local := mappedPlaybackPath(cfg, p)
	// 两边可能来自不同操作系统；统一分隔符后再清理，防止同名影片误匹配。
	return relPathFromLocal(path.Clean(strings.ReplaceAll(root, "\\", "/")), path.Clean(strings.ReplaceAll(local, "\\", "/")))
}

// 返回的 managed 即使解析失败也保留，阻止本站媒体悄悄回源中转。
// 只认配置域名、已登记 pickcode 或媒体根内的 STRM，不按任意 URL 中的 /d/ 猜归属。
func resolveEmbyPlaybackSource(db *gorm.DB, cfg *config.Config, sourcePath, requestHost string) (pc string, managed bool, err error) {
	isStrm := !strings.HasPrefix(sourcePath, "http://") && !strings.HasPrefix(sourcePath, "https://") && strings.HasSuffix(strings.ToLower(sourcePath), ".strm")
	direct := sourcePath
	if isStrm {
		rel := playbackRelativePath(db, cfg, sourcePath)
		managed = rel != ""
		direct = readStrmDirectURL(db, cfg, sourcePath)
		if direct == "" && managed && db != nil {
			var sf model.SyncedFile
			if db.Where("rel_path = ?", rel).First(&sf).Error == nil && sf.PickCode != "" {
				pc, err = playbackPickcode(db, sf.PickCode)
				return pc, true, err
			}
		}
		if direct == "" {
			if managed {
				return "", true, fmt.Errorf("STRM 不可读且未命中同步台账")
			}
			return "", false, nil
		}
	}
	u, parseErr := url.Parse(direct)
	if parseErr != nil {
		return "", managed, parseErr
	}
	domain, _, _ := readStrmLinkConfig(db, cfg)
	base, _ := url.Parse(domain)
	if (base != nil && base.Host != "" && strings.EqualFold(u.Host, base.Host)) ||
		(requestHost != "" && strings.EqualFold(u.Host, requestHost)) {
		managed = true
	}
	id := pickcodeOfDirectURL(direct)
	if id == "" {
		if managed {
			return "", true, fmt.Errorf("STRM 不是可解析的 115 播放入口")
		}
		return "", false, nil
	}
	if !managed && db != nil {
		var count int64
		if db.Model(&model.SyncedFile{}).Where("pick_code = ? OR file_id = ?", id, id).Count(&count).Error == nil && count > 0 {
			managed = true
		}
	}
	if !managed {
		return "", false, nil
	}
	pc, err = playbackPickcode(db, id)
	return pc, true, err
}

type embyPlaybackMediaSource struct {
	ID               string `json:"Id"`
	Path             string `json:"Path"`
	IsInfiniteStream bool   `json:"IsInfiniteStream"`
}

type embyPlaybackItem struct {
	ID           string                    `json:"Id"`
	Path         string                    `json:"Path"`
	PlayAccess   string                    `json:"PlayAccess"`
	CanDownload  *bool                     `json:"CanDownload"`
	MediaSources []embyPlaybackMediaSource `json:"MediaSources"`
}

// 所有查询只使用当前客户端凭据，不使用配置中的管理员 API key。
// 不跟随重定向，避免凭据外发，以及错误地址意外变成媒体流下载。
func embyPlaybackJSON(req *http.Request, target *url.URL, apiPath, token string, out any) int {
	endpoint, err := url.Parse(apiPath)
	if err != nil {
		return http.StatusBadGateway
	}
	u := *target
	u.Path = strings.TrimRight(target.Path, "/") + endpoint.Path
	u.RawPath, u.RawQuery, u.Fragment = "", endpoint.RawQuery, ""
	r, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return http.StatusBadGateway
	}
	r.Header.Set("X-Emby-Token", token)
	r.Header.Set("User-Agent", req.UserAgent())
	r.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return http.StatusBadGateway
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return resp.StatusCode
		}
		return http.StatusBadGateway
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) != nil {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

func selectEmbyPlaybackSource(item embyPlaybackItem, sourceID string) (embyPlaybackMediaSource, bool) {
	if sourceID != "" {
		var selected embyPlaybackMediaSource
		matches := 0
		for _, source := range item.MediaSources {
			if strings.TrimPrefix(source.ID, "mediasource_") == strings.TrimPrefix(sourceID, "mediasource_") {
				selected = source
				matches++
			}
		}
		return selected, matches == 1
	}
	if len(item.MediaSources) == 1 {
		return item.MediaSources[0], true
	}
	if len(item.MediaSources) == 0 {
		return embyPlaybackMediaSource{Path: item.Path}, true
	}
	return embyPlaybackMediaSource{}, false
}

func handleEmbyPlayback(c *gin.Context, db *gorm.DB, cfg *config.Config, target *url.URL, routePath string) bool {
	m := embyMediaRoute.FindStringSubmatch(routePath)
	if len(m) == 0 || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
		return false
	}
	action := strings.ToLower(m[3])
	resource := strings.ToLower(m[1])
	isDownload := resource == "items" && action == "download"
	if resource == "items" && !isDownload {
		return false
	}
	baseAction := strings.SplitN(action, ".", 2)[0]
	if !isDownload {
		switch baseAction {
		case "stream", "original", "master", "main", "hls", "hls1", "live":
		default:
			return false // 字幕、缩略图等继续交给普通反代。
		}
	}
	token := embyClientToken(c.Request)
	if token == "" {
		c.String(http.StatusUnauthorized, "缺少 Emby 播放凭据")
		return true
	}
	var user struct {
		ID     string `json:"Id"`
		Policy struct {
			EnableMediaPlayback      *bool `json:"EnableMediaPlayback"`
			EnableContentDownloading *bool `json:"EnableContentDownloading"`
		} `json:"Policy"`
	}
	userID := embyPlaybackUserID(c.Request)
	userPath := "/Users/Me" // Jellyfin 及提供该兼容接口的服务器。
	if userID != "" {
		userPath = "/Users/" + url.PathEscape(userID)
	}
	status := embyPlaybackJSON(c.Request, target, userPath, token, &user)
	if status != http.StatusOK {
		c.String(status, "Emby 用户认证失败")
		return true
	}
	if user.ID == "" || (userID != "" && user.ID != userID) || user.Policy.EnableMediaPlayback == nil || !*user.Policy.EnableMediaPlayback ||
		(isDownload && user.Policy.EnableContentDownloading != nil && !*user.Policy.EnableContentDownloading) {
		c.String(http.StatusForbidden, "该用户不允许播放或下载")
		return true
	}
	var item embyPlaybackItem
	status = embyPlaybackJSON(c.Request, target, "/Users/"+url.PathEscape(user.ID)+"/Items/"+url.PathEscape(m[2])+"?Fields=Path,MediaSources", token, &item)
	if status != http.StatusOK {
		c.String(status, "无法读取该用户的媒体条目")
		return true
	}
	// 旧版本返回 PlayAccess，新版 BaseItemDto 已不保证该字段；新版以
	// 用户库接口的 401/403/404 和已校验的用户播放策略为准，不用缺字段误拒绝。
	if (item.PlayAccess != "" && !strings.EqualFold(item.PlayAccess, "Full")) || (isDownload && item.CanDownload != nil && !*item.CanDownload) {
		c.String(http.StatusForbidden, "该用户无权播放此条目")
		return true
	}
	source, ok := selectEmbyPlaybackSource(item, queryFold(c.Request.URL.Query(), "MediaSourceId"))
	if !ok {
		c.String(http.StatusBadRequest, "媒体源不存在或不唯一")
		return true
	}
	if source.IsInfiniteStream {
		return false
	}
	if source.Path == "" {
		source.Path = item.Path
	}
	pc, managed, err := resolveEmbyPlaybackSource(db, cfg, source.Path, c.Request.Host)
	if !managed && len(item.MediaSources) <= 1 {
		// Emby 可能已经把 STRM 展开为 URL；用条目的本地路径再确认归属。
		pc, managed, err = resolveEmbyPlaybackSource(db, cfg, item.Path, c.Request.Host)
	}
	if !managed {
		vlog("[播放] ○ 非本站媒体，交给 Emby")
		return false
	}
	if err != nil {
		log.Printf("[播放] ✗ 本站媒体解析失败，未回源中转")
		c.String(http.StatusBadGateway, "无法解析本站 STRM，请检查媒体路径映射和同步台账")
		return true
	}
	if baseAction == "master" || baseAction == "main" || baseAction == "hls" || baseAction == "hls1" || baseAction == "live" ||
		strings.HasSuffix(action, ".m3u8") || strings.EqualFold(queryFold(c.Request.URL.Query(), "Static"), "false") || queryFold(c.Request.URL.Query(), "TranscodeReasons") != "" {
		c.String(http.StatusUnsupportedMediaType, "115 直连不支持服务器转码，请使用支持原文件格式的播放器")
		return true
	}
	servePickcodeDirect(c, db, cfg, pc)
	return true
}
