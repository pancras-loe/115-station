package api

// ==================== Emby 反向代理（CMS 9096 同款） ====================
//
// 监听在 302 代理端口（6086）上的 /emby 路径，把请求转发给真正的 Emby 服务器。
// 客户端只需访问 http://NAS_IP:6086/emby/... 即可使用 Emby 全部功能，
// 播放本站 STRM 时由视频入口返回 CDN 302；客户端必须能够访问 115 CDN。
//
// 用法：
//   Emby 客户端里填的服务器地址 = http://NAS_IP:6086/emby
//   （而不是 http://NAS_IP:8096）
//
// 需要「系统配置 → EMBY管理」里填写 Emby 服务器地址（如 http://192.168.1.100:8096）

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	embyTargetMu  sync.RWMutex
	embyTargetURL string
	embyTargetAt  time.Time
)

// getEmbyTarget 获取 Emby 服务器地址（EMBY管理 配置，5 分钟缓存）。
// 读取顺序：yaml 配置文件（saveConfig 的实际落盘位置）→ 内存缓存 → 旧版 DB 表。
// 此前只认内存缓存+DB：容器重启后缓存清空、配置又在 yaml 里没人读，
// 导致已配置也显示"未配置"（测试连接走 Handler 的 yaml 感知读取所以正常）
func getEmbyTarget(db *gorm.DB, cfg *config.Config) string {
	embyTargetMu.RLock()
	if time.Since(embyTargetAt) < 5*time.Minute && embyTargetURL != "" {
		v := embyTargetURL
		embyTargetMu.RUnlock()
		return v
	}
	embyTargetMu.RUnlock()

	target := ""
	var cfgRaw struct {
		ServerURL string `json:"server_url"`
	}
	// 1) yaml 配置文件（前端保存 emby 配置的实际位置）
	if cfg != nil {
		if v := cfg.GetSetting("emby"); v != "" && parseJSON(v, &cfgRaw) == nil && cfgRaw.ServerURL != "" {
			target = strings.TrimRight(strings.TrimSpace(cfgRaw.ServerURL), "/")
		}
	}
	// 2) 保存时的内存缓存
	if y := embyConfigGet(); target == "" && y != "" {
		if parseJSON(y, &cfgRaw) == nil && cfgRaw.ServerURL != "" {
			target = strings.TrimRight(strings.TrimSpace(cfgRaw.ServerURL), "/")
		}
	}
	// 3) 旧版 DB 表回退
	if target == "" && db != nil {
		var s struct{ Value string }
		if err := db.Raw("SELECT value FROM settings WHERE `key` = 'emby' LIMIT 1").Scan(&s).Error; err == nil && s.Value != "" {
			if parseJSON(s.Value, &cfgRaw) == nil && cfgRaw.ServerURL != "" {
				target = strings.TrimRight(strings.TrimSpace(cfgRaw.ServerURL), "/")
			}
		}
	}

	embyTargetMu.Lock()
	embyTargetURL = target
	embyTargetAt = time.Now()
	embyTargetMu.Unlock()
	return target
}

// embyConfigYAML 从 yaml 配置读取的 emby 配置（由 UpdateEmbyConfig 更新）。
// 读写均需持 embyCfgMu（写侧为配置热更新，读侧在每次代理请求路径上）
var (
	embyConfigYAML string
	embyCfgMu      sync.RWMutex
)

func embyConfigGet() string {
	embyCfgMu.RLock()
	defer embyCfgMu.RUnlock()
	return embyConfigYAML
}

func embyConfigSet(v string) {
	embyCfgMu.Lock()
	embyConfigYAML = v
	embyCfgMu.Unlock()
}

// UpdateEmbyConfig 外部调用：更新 yaml 配置缓存（保存 emby 配置时触发）
func UpdateEmbyConfig(jsonStr string) {
	embyConfigSet(jsonStr)
	embyTargetMu.Lock()
	embyTargetURL = "" // 清缓存，下次重新读
	embyTargetAt = time.Time{}
	embyTargetMu.Unlock()
}

func parseJSON(data string, out interface{}) error {
	return json.Unmarshal([]byte(data), out)
}

// playbackInfoPathRe 匹配 Emby 播放信息接口（/Items/{id}/PlaybackInfo）
var playbackInfoPathRe = regexp.MustCompile(`(?i)^/items/([a-z0-9_-]+)/playbackinfo/?$`)

// itemDetailPathRe 匹配条目详情接口（GET /Users/{uid}/Items/{id}）——
// 用户打开详情页 = 即将播放的强意图信号，此时预取直链比 PlaybackInfo
// 时再取多争取几秒到几十秒（StrmAssistant「把探测挪出起播关键路径」
// 思路在反代层的落点）
var itemDetailPathRe = regexp.MustCompile(`(?i)^/users/[^/]+/items/\d+$`)

// pickcodeOfDirectURL 从本机 /d/ 直链 URL 提取 pickcode（非 /d/ 链接返回空）
func pickcodeOfDirectURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.HasPrefix(u.Path, "/d/") {
		return ""
	}
	return normalizePlaybackID(strings.TrimPrefix(u.Path, "/d/"))
}

func embyResponsePath(req *http.Request) string {
	if p := req.Header.Get("X-Station-Path"); p != "" {
		return p
	}
	return req.URL.Path
}

// rewritePlaybackInfo 直连改写中间件（MediaWarp/CMS 同款思路）：
// 拦截 PlaybackInfo 响应，把 strm 媒体源从本地文件改写成其内容指向的
// 视频入口并禁止服务器转码——播放器收到 CDN 302 后直接取流，绕开
// Emby 服务器转码（转码需要服务器从 strm 拉流重编码，容器网络/码率
// 限制等问题都会让它失败）
func rewritePlaybackInfo(db *gorm.DB, cfg *config.Config) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.Request == nil || resp.StatusCode != http.StatusOK {
			return nil
		}
		match := playbackInfoPathRe.FindStringSubmatch(embyResponsePath(resp.Request))
		if len(match) == 0 {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		restore := func() { resp.Body = io.NopCloser(bytes.NewReader(body)) }
		clientHost := ""
		if resp.Request != nil {
			clientHost = resp.Request.Header.Get("X-Original-Host")
		}

		var root map[string]interface{}
		if json.Unmarshal(body, &root) != nil {
			restore()
			return nil
		}
		if code := root["ErrorCode"]; code != nil && code != "" {
			restore()
			return nil
		}
		rememberEmbyResponseUser(resp.Request)
		sources, _ := root["MediaSources"].([]interface{})
		changed, rewritten := false, 0
		for _, s := range sources {
			ms, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			strmPath, _ := ms["Path"].(string)
			if strmPath == "" {
				continue
			}
			if infinite, _ := ms["IsInfiniteStream"].(bool); infinite {
				continue
			}
			pc, managed, _ := resolveEmbyPlaybackSource(db, cfg, strmPath, clientHost)
			if !managed {
				continue
			}
			id, _ := ms["Id"].(string)
			streamURL := embyStreamURL(resp.Request, match[1], id)
			if pc != "" {
				prefetchPickcodeLink(db, cfg, pc, resp.Request.UserAgent())
			}
			// 参考 MediaWarp 的流入口接管：让客户端请求本站的 stream，
			// 由实际播放请求的 UA 换链。DirectStream 此时返回 302，不传媒体字节。
			ms["Path"] = streamURL
			ms["DirectStreamUrl"] = streamURL
			ms["Protocol"] = "Http"
			ms["IsRemote"] = true
			ms["SupportsDirectPlay"] = false
			ms["SupportsDirectStream"] = true
			ms["SupportsTranscoding"] = false
			ms["AddApiKeyToDirectStreamUrl"] = false
			for _, key := range []string{"TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol", "RequiredHttpHeaders"} {
				delete(ms, key)
			}
			// URL 已变成 stream，容器必须从原始媒体源读取，不能误判成 strm。
			if container := directURLContainer(strmPath); container != "" && container != "strm" {
				ms["Container"] = container
			}
			vlog("[播放] ✓ 播放地址已交给 302 视频入口")
			changed = true
			rewritten++
		}
		// 拦截摘要：无论是否改写都留痕，排查"改写没生效"时一眼可见
		vlog("[Emby直连] PlaybackInfo 拦截: 媒体源 %d 个，改写 %d 个", len(sources), rewritten)
		if !changed {
			restore()
			return nil
		}
		out, err := json.Marshal(&root)
		if err != nil {
			restore()
			return nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		resp.Header.Del("Content-Encoding")
		return nil
	}
}

// 详情页和播放信息的预取共用正式播放解析器；实际播放器 UA 改变时重新取链。
func prefetchPickcodeLink(db *gorm.DB, cfg *config.Config, pickcode, ua string) {
	resolver := playbackLinks
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_, _ = resolver.resolve(ctx, db, cfg, pickcode, ua)
	}()
}

// readStrmDirectURL 读取 strm 文件内容（第一行的直链 URL）。
// PlaybackInfo 里的 Path 是 Emby 侧路径，先按「本地路径映射」换算再读
func readStrmDirectURL(db *gorm.DB, cfg *config.Config, embyPath string) string {
	local := embyPath
	if cfg != nil {
		var embyCfg struct {
			PathMapping string `json:"path_mapping"`
		}
		if json.Unmarshal([]byte(cfg.GetSetting("emby")), &embyCfg) == nil {
			local = embyPathToLocal(embyCfg.PathMapping, embyPath)
		}
	}
	for _, cand := range []string{local, embyPath} {
		if cand == "" {
			continue
		}
		f, err := os.Open(cand)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 64<<10))
		f.Close()
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		if i := strings.IndexAny(line, "\r\n"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}

// directURLContainer 从直链 URL 提取容器格式：
// /d/{pc}.mkv?/名字.mkv → mkv；?/ 后的文件名兜底
func directURLContainer(u string) string {
	pathPart := u
	if i := strings.Index(u, "?"); i >= 0 {
		if q := u[i:]; strings.HasPrefix(q, "?/") && len(q) > 2 {
			if e := strings.TrimPrefix(filepath.Ext(q[2:]), "."); e != "" {
				return strings.ToLower(e)
			}
		}
		pathPart = u[:i]
	}
	if e := strings.TrimPrefix(filepath.Ext(pathPart), "."); e != "" {
		return strings.ToLower(e)
	}
	return ""
}

// readStrmLinkConfig 读取 STRM 直链配置（域名/格式/保留后缀；yaml 优先 DB 回退）。
// 反代侧没有 Handler，所以自己取值；解析用与生成侧同一个 parseStrmConfig
func readStrmLinkConfig(db *gorm.DB, cfg *config.Config) (domain, format string, keepExt bool) {
	raw := ""
	if cfg != nil {
		raw = cfg.GetSetting("strm")
	}
	if raw == "" && db != nil {
		var s model.Setting
		if err := db.Where("`key` = ?", "strm").First(&s).Error; err == nil {
			raw = s.Value
		}
	}
	domain, format, keepExt, _ = parseStrmConfig(raw)
	return
}

// registerEmbyProxy 在 gin 引擎上注册 Emby 反代路由
// prefetchOnItemDetail 详情页预取中间件：客户端打开条目详情
// （GET /Users/{uid}/Items/{id}）即视为即将播放的意图信号，此刻后台预取
// 直链（用详情请求的 UA，与随后的播放请求同设备同 UA，缓存直接命中）。
// 用户浏览详情到按下播放之间的几秒到几十秒里取链已完成，PlaybackInfo
// 时刻的预取退化为纯缓存命中；幂等（已热/在途不重复取）
func prefetchOnItemDetail(db *gorm.DB, cfg *config.Config) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.Request == nil || resp.StatusCode != http.StatusOK || resp.Request.Method != http.MethodGet {
			return nil
		}
		if !itemDetailPathRe.MatchString(embyResponsePath(resp.Request)) {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		var d struct {
			Path string `json:"Path"`
		}
		if json.Unmarshal(body, &d) != nil || d.Path == "" {
			return nil
		}
		rememberEmbyResponseUser(resp.Request)
		if !strings.HasSuffix(strings.ToLower(d.Path), ".strm") {
			return nil // 本地文件/音频等非 strm 条目
		}
		pc, managed, err := resolveEmbyPlaybackSource(db, cfg, d.Path, resp.Request.Header.Get("X-Original-Host"))
		if managed && err == nil && pc != "" {
			prefetchPickcodeLink(db, cfg, pc, resp.Request.Header.Get("User-Agent"))
			vlog("[Emby直连] ○ 详情页预取直链: %s", truncateStr(d.Path, 70))
		}
		return nil
	}
}

// embyModifyResponse 组合中间件：详情页预取 + PlaybackInfo 直连改写
func embyModifyResponse(db *gorm.DB, cfg *config.Config) func(*http.Response) error {
	prefetch := prefetchOnItemDetail(db, cfg)
	rewrite := rewritePlaybackInfo(db, cfg)
	return func(resp *http.Response) error {
		if err := prefetch(resp); err != nil {
			return err
		}
		return rewrite(resp)
	}
}

// embyReverseProxy 构造反代。stripPrefix 非空时把请求路径重写成它
// （/emby/*path 那条路由要剥掉 /emby 前缀；根路径那条原样透传）。
//
// 两条路由**必须共用这一个构造函数**：此前根路径那条是另写的一份，
// 既没挂 ModifyResponse 也没删 Accept-Encoding、没设 X-Original-Host，
// 于是客户端把服务器地址填成 http://ip:6086/（首页和文档都这么教）时，
// PlaybackInfo 直连改写与详情页预取全都不生效 —— 播放照样走 Emby 转码
func embyReverseProxy(db *gorm.DB, cfg *config.Config, targetURL *url.URL, stripPrefix func() string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// 客户端访问 6086 用的地址，必须在覆盖 req.Host 之前取
			clientHost := req.Host
			req.Host = targetURL.Host
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			if stripPrefix != nil {
				// 剥掉 /emby 前缀后转发（客户端访问 /emby/Users/… → Emby 的 /Users/…）
				p := stripPrefix()
				if !strings.HasPrefix(p, "/") {
					p = "/" + p
				}
				req.URL.Path = p
				req.URL.RawPath = ""
			}
			req.Header.Set("X-Station-Path", req.URL.Path)
			req.URL.Path = strings.TrimRight(targetURL.Path, "/") + req.URL.Path
			// 直连改写需要读取 JSON 响应体，禁用压缩传输
			req.Header.Del("Accept-Encoding")
			// 记录客户端访问 6086 用的地址（含端口），直连改写用它拼 URL——
			// 客户端能连上这个地址访问 Emby，就一定能连上它取直链流
			req.Header.Set("X-Original-Host", clientHost)
		},
		FlushInterval:  -1, // 流式响应立即刷新（视频播放需要）
		ModifyResponse: embyModifyResponse(db, cfg),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[Emby反代] 转发失败: %v", err)
			http.Error(w, "Emby 服务器无法连接: "+err.Error(), http.StatusBadGateway)
		},
	}
}

func registerEmbyProxy(r *gin.Engine, db *gorm.DB, cfg *config.Config) {
	r.Any("/emby/*path", func(c *gin.Context) {
		target := getEmbyTarget(db, cfg)
		if target == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "未配置 Emby 服务器地址，请在「系统配置 → EMBY管理」填写"})
			return
		}

		targetURL, err := url.Parse(target)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Emby 服务器地址无效: " + err.Error()})
			return
		}
		if handleEmbyPlayback(c, db, cfg, targetURL, c.Param("path")) {
			return
		}
		c.Request, err = prepareEmbyPlaybackRequest(c.Request, c.Param("path"))
		if err != nil {
			c.String(http.StatusBadRequest, "播放请求体读取失败")
			return
		}
		embyReverseProxy(db, cfg, targetURL, func() string { return c.Param("path") }).
			ServeHTTP(c.Writer, c.Request)
	})

	// 6086 根路径：非媒体/非API请求 → 反代到 Emby（CMS 9096 同款行为）
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// /d/ 开头是 302 服务已处理；/emby/ 已注册；其余全部反代 Emby
		if strings.HasPrefix(p, "/d/") || strings.HasPrefix(p, "/emby") || strings.HasPrefix(p, "/proxy/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// 反代到 Emby
		target := getEmbyTarget(db, cfg)
		if target == "" {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>115-Station</title></head><body style="font-family:system-ui;max-width:640px;margin:60px auto;padding:0 20px"><h2>115-Station 302 代理</h2><p style="color:#e74c3c">未配置 Emby 服务器地址，暂无法反代</p><p>请在「系统配置 → EMBY管理」填写 Emby 服务器地址（如 http://192.168.1.100:8096）后刷新本页。</p><hr style="border:none;border-top:1px solid #eee"><p>本端口提供两个服务：</p><ul><li><b>Emby 反代</b>：<code>http://本机IP:6086/</code> 与 <code>/emby</code> — 配置后直接打开即 Emby</li><li><b>302 直连</b>：<code>http://本机IP:6086/d/文件ID</code> — strm 播放地址（自动生成）</li></ul><p>管理后台在 <code>http://本机IP:6060</code></p></body></html>`)
			return
		}
		targetURL, err := url.Parse(target)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Emby 服务器地址无效: " + err.Error()})
			return
		}
		// 路径原样透传（客户端把服务器填成 http://ip:6086/ 时走这条），
		// 直连改写与预取与 /emby 那条完全一致
		if handleEmbyPlayback(c, db, cfg, targetURL, c.Request.URL.Path) {
			return
		}
		c.Request, err = prepareEmbyPlaybackRequest(c.Request, c.Request.URL.Path)
		if err != nil {
			c.String(http.StatusBadRequest, "播放请求体读取失败")
			return
		}
		embyReverseProxy(db, cfg, targetURL, nil).ServeHTTP(c.Writer, c.Request)
	})
}
