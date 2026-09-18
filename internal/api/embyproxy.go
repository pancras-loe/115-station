package api

// ==================== Emby 反向代理（CMS 9096 同款） ====================
//
// 监听在 302 代理端口（6086）上的 /emby 路径，把请求转发给真正的 Emby 服务器。
// 客户端只需访问 http://NAS_IP:6086/emby/... 即可使用 Emby 全部功能，
// 播放 strm 时的 302 跳转由服务器侧完成——客户端不需要能访问 strm 直连地址。
//
// 用法：
//   Emby 客户端里填的服务器地址 = http://NAS_IP:6086/emby
//   （而不是 http://NAS_IP:8096）
//
// 需要「系统配置 → EMBY管理」里填写 Emby 服务器地址（如 http://192.168.1.100:8096）

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"

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
	if target == "" {
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
var playbackInfoPathRe = regexp.MustCompile(`(?i)^/items/[^/]+/playbackinfo$`)

// itemDetailPathRe 匹配条目详情接口（GET /Users/{uid}/Items/{id}）——
// 用户打开详情页 = 即将播放的强意图信号，此时预取直链比 PlaybackInfo
// 时再取多争取几秒到几十秒（StrmAssistant「把探测挪出起播关键路径」
// 思路在反代层的落点）
var itemDetailPathRe = regexp.MustCompile(`(?i)^/users/[^/]+/items/\d+$`)

// resolveStrmURLCached strm 路径 → 直链 URL：优先读 strm 文件；容器路径
// 不一致读不到时用同步台账反查。结果进 5 分钟缓存（详情页预取与
// PlaybackInfo 改写共用，重复读文件/查台账浪费）
func resolveStrmURLCached(db *gorm.DB, cfg *config.Config, strmPath string) string {
	strmURLCacheMu.Lock()
	if e, ok := strmURLCache[strmPath]; ok && time.Since(e.at) < 5*time.Minute {
		u := e.url
		strmURLCacheMu.Unlock()
		return u
	}
	strmURLCacheMu.Unlock()
	u := readStrmDirectURL(db, cfg, strmPath)
	if u == "" {
		u = directURLFromLedger(db, cfg, filepath.Base(strmPath))
	}
	if u != "" {
		strmURLCacheMu.Lock()
		if len(strmURLCache) >= 4096 {
			type kv struct {
				k string
				t time.Time
			}
			var all []kv
			for k, v := range strmURLCache {
				all = append(all, kv{k, v.at})
			}
			sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
			for _, e := range all[:len(all)/4] {
				delete(strmURLCache, e.k)
			}
		}
		strmURLCache[strmPath] = strmURLCacheEntry{url: u, at: time.Now()}
		strmURLCacheMu.Unlock()
	}
	return u
}

// pickcodeOfDirectURL 从本机 /d/ 直链 URL 提取 pickcode（非 /d/ 链接返回空）
func pickcodeOfDirectURL(u string) string {
	i := strings.LastIndex(u, "/d/")
	if i <= 0 {
		return ""
	}
	pc := strings.TrimLeft(u[i+3:], "/")
	if j := strings.IndexAny(pc, "?#"); j > 0 {
		pc = pc[:j]
	}
	return pc
}

// rewritePlaybackInfo 直连改写中间件（MediaWarp/CMS 同款思路）：
// 拦截 PlaybackInfo 响应，把 strm 媒体源从本地文件改写成其内容指向的
// 直链 URL 并强制直连播放——播放器直接从 115 CDN 取流，彻底绕开
// Emby 服务器转码（转码需要服务器从 strm 拉流重编码，容器网络/码率
// 限制等问题都会让它失败）
func rewritePlaybackInfo(db *gorm.DB, cfg *config.Config) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.Request == nil || resp.StatusCode != http.StatusOK {
			return nil
		}
		if !playbackInfoPathRe.MatchString(resp.Request.URL.Path) {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil
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
			var directURL string
			isStrm := strings.HasSuffix(strings.ToLower(strmPath), ".strm")
			if strings.HasPrefix(strmPath, "http://") || strings.HasPrefix(strmPath, "https://") {
				// 部分 Emby 版本已把 strm 展开成 URL 放在 Path，直接使用
				directURL = strmPath
			} else if isStrm {
				directURL = resolveStrmURLCached(db, cfg, strmPath)
			}
			if directURL == "" {
				if isStrm {
					log.Printf("[Emby直连] ○ 无法解析 strm（文件不可读且台账未命中，检查媒体路径映射与同步台账）: %s", strmPath)
				} else {
					log.Printf("[Emby直连] ○ 媒体源非 strm/URL（Path=%s），不改写", truncateStr(strmPath, 90))
				}
				continue
			}
			// 直链地址按客户端访问地址改写（strm 里的旧域名/端口不影响播放）
			directURL = normalizeDirectURL(db, cfg, directURL, clientHost)
			// 起播预热线程：strm 指向本机 /d/ 端点时，此刻就已知道 pickcode
			// 与客户端 UA——立即后台取直链填缓存。播放器拿到响应、起播、
			// 再请求 /d/ 时（几百毫秒后）缓存已热，取链的 ~1s 从起播关键
			// 路径上彻底消失（幂等：/d/ 命中缓存时无任何额外动作）
			if pc := pickcodeOfDirectURL(directURL); pc != "" {
				playerUA := resp.Request.Header.Get("User-Agent")
				prefetchPickcodeLink(db, cfg, pc, playerUA)
			}
			ms["Path"] = directURL
			ms["Protocol"] = "Http"
			ms["SupportsDirectPlay"] = true
			// DirectStream 必须关闭：它是"Emby 服务器拉流再转发"模式，
			// 服务器容器访问 CDN 受限时必挂（App 还会因此退回转码 500）。
			// 只留 DirectPlay，逼播放器自己直连 115 CDN
			ms["SupportsDirectStream"] = false
			ms["SupportsTranscoding"] = false
			delete(ms, "TranscodingUrl")
			if c := directURLContainer(directURL); c != "" {
				ms["Container"] = c
			}
			vlog("[Emby直连] ✦ 改写播放信息: %s → 直连播放（绕过服务器转码）", truncateStr(strmPath, 70))
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

// prefetchPrefilling 预热去重（同一 pickcode+UA 并发 PlaybackInfo 只取一次）
var (
	prefetchMu    sync.Mutex
	prefetchDoing = map[string]bool{}
)

// prefetchPickcodeLink 后台预热直链缓存：与 /d/ 命中同一份缓存，
// 幂等且不打断播放（起播关键路径外完成 115 取链）
func prefetchPickcodeLink(db *gorm.DB, cfg *config.Config, pickcode, ua string) {
	if pickcode == "" {
		return
	}
	key := pickcode + "|" + ua
	downloadCacheMu.Lock()
	cached, ok := downloadLinkCache[key]
	downloadCacheMu.Unlock()
	if ok && time.Now().Before(cached.Expiry) {
		return // 已热
	}
	prefetchMu.Lock()
	if prefetchDoing[key] {
		prefetchMu.Unlock()
		return
	}
	prefetchDoing[key] = true
	prefetchMu.Unlock()
	go func() {
		defer func() {
			prefetchMu.Lock()
			delete(prefetchDoing, key)
			prefetchMu.Unlock()
		}()
		u, err := proxyDownloadURL(db, cfg, pickcode, ua)
		if err != nil || u == "" {
			return // 失败不阻塞：/d/ 收到真实请求时再取
		}
		downloadCacheMu.Lock()
		downloadLinkCache[key] = downloadCacheEntry{URL: u, Expiry: time.Now().Add(30 * time.Minute)}
		downloadCacheMu.Unlock()
	}()
}

// strmURLCache strm 路径→直链解析缓存（每次播放都会触发 PlaybackInfo
// 改写，重复读文件/查台账浪费；strm 内容极少变化，5 分钟 TTL）
var (
	strmURLCacheMu sync.Mutex
	strmURLCache   = map[string]strmURLCacheEntry{}
)

type strmURLCacheEntry struct {
	url string
	at  time.Time
}

// readStrmDirectURL 读取 strm 文件内容（第一行的直链 URL）。
// PlaybackInfo 里的 Path 是 Emby 侧路径，先按「本地路径映射」换算再读
func readStrmDirectURL(db *gorm.DB, cfg *config.Config, embyPath string) string {
	local := embyPath
	if cfg != nil {
		var embyCfg struct {
			PathMapping string `json:"path_mapping"`
		}
		if json.Unmarshal([]byte(cfg.GetSetting("emby")), &embyCfg) == nil && embyCfg.PathMapping != "" {
			parts := strings.SplitN(embyCfg.PathMapping, "#", 2)
			if len(parts) == 2 {
				localPart, embyPart := strings.TrimRight(parts[0], "/"), strings.TrimRight(parts[1], "/")
				if embyPart != "" && strings.HasPrefix(embyPath, embyPart+"/") {
					local = localPart + embyPath[len(embyPart):]
				}
			}
		}
	}
	for _, cand := range []string{local, embyPath} {
		if cand == "" {
			continue
		}
		data, err := os.ReadFile(cand)
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

// directURLFromLedger 同步台账反查直链：按 strm 文件名（xxx.mkv.strm）
// 查 SyncedFile 的 pick_code，按 STRM 直链配置拼 URL。
// Emby 与 StrmHub 容器的媒体路径不一致导致文件读不到时的兜底
func directURLFromLedger(db *gorm.DB, cfg *config.Config, strmBase string) string {
	if db == nil || strmBase == "" {
		return ""
	}
	var sf model.SyncedFile
	if err := db.Where("rel_path = ? OR rel_path LIKE ?", strmBase, "%/"+strmBase).First(&sf).Error; err != nil {
		return ""
	}
	if sf.PickCode == "" {
		return ""
	}
	domain, format, keepExt := readStrmLinkConfig(db, cfg)
	if domain == "" {
		return ""
	}
	// rel_path 形如 "俱乐部/…/xxx.mkv.strm"，原文件名 = 去掉 .strm
	origName := strings.TrimSuffix(strmBase, ".strm")
	ext := strings.ToLower(filepath.Ext(origName))
	idPart := sf.PickCode
	if keepExt {
		idPart += ext
	}
	base := strings.TrimRight(domain, "/")
	if format == "pick_code" {
		return base + "/d/" + idPart
	}
	return base + "/d/" + idPart + "?/" + origName
}

// readStrmLinkConfig 读取 STRM 直链配置（域名/格式/保留后缀；yaml 优先 DB 回退）
func readStrmLinkConfig(db *gorm.DB, cfg *config.Config) (domain, format string, keepExt bool) {
	domain, format, keepExt = "http://172.17.0.1:6086", "pick_code_name", true
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
	if raw == "" {
		return
	}
	var c struct {
		Domain  string `json:"domain"`
		Format  string `json:"format"`
		KeepExt any    `json:"keep_ext"`
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		return
	}
	if c.Domain != "" {
		domain = c.Domain
	}
	if c.Format != "" {
		format = c.Format
	}
	switch v := c.KeepExt.(type) {
	case bool:
		keepExt = v
	case string:
		keepExt = v == "true"
	}
	return
}

// normalizeDirectURL 把直链 URL 的协议+主机替换成播放器可达的地址。
// 优先用客户端访问 6086 时的 Host（X-Original-Host，Director 记录）——
// 客户端怎么连上的 Emby 就怎么取流，公网/内网/localhost 访问都能播，
// strm 里残留任何旧域名（如 172.17.0.1:6060）都无所谓；
// 无 Host 信息时回退到 STRM 配置的直链域名
func normalizeDirectURL(db *gorm.DB, cfg *config.Config, u, clientHost string) string {
	var base *url.URL
	if clientHost != "" {
		base = &url.URL{Scheme: "http", Host: clientHost}
	} else {
		domain, _, _ := readStrmLinkConfig(db, cfg)
		var err error
		base, err = url.Parse(strings.TrimRight(domain, "/"))
		if err != nil || base.Host == "" {
			return u
		}
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	if parsed.Scheme == base.Scheme && parsed.Host == base.Host {
		return u
	}
	parsed.Scheme = base.Scheme
	parsed.Host = base.Host
	out := parsed.String()
	vlog("[Emby直连] ○ 直链地址改写: %s → %s", truncateStr(u, 60), truncateStr(out, 60))
	return out
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
		if !itemDetailPathRe.MatchString(resp.Request.URL.Path) {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		var d struct {
			Path string `json:"Path"`
		}
		if json.Unmarshal(body, &d) != nil || d.Path == "" {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Path), ".strm") {
			return nil // 本地文件/音频等非 strm 条目
		}
		u := resolveStrmURLCached(db, cfg, d.Path)
		if pc := pickcodeOfDirectURL(u); pc != "" {
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

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.Host = targetURL.Host
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
				// 保留 /emby 前缀之后完整路径（Emby 的 API 路径本身以 /emby 开头的直接透传，
				// 客户端访问 /emby/Users/... → 转发到 Emby 的 /emby/Users/...）
				req.URL.Path = c.Param("path")
				// 确保路径以 / 开头
				if !strings.HasPrefix(req.URL.Path, "/") {
					req.URL.Path = "/" + req.URL.Path
				}
				// 直连改写需要读取 JSON 响应体，禁用压缩传输
				req.Header.Del("Accept-Encoding")
				// 记录客户端访问 6086 用的地址（含端口），直连改写用它拼 URL——
				// 客户端能连上这个地址访问 Emby，就一定能连上它取直链流
				req.Header.Set("X-Original-Host", c.Request.Host)
			},
			FlushInterval:  -1, // 流式响应立即刷新（视频播放需要）
			ModifyResponse: embyModifyResponse(db, cfg),
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[Emby反代] 转发失败: %v", err)
				http.Error(w, "Emby 服务器无法连接: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(c.Writer, c.Request)
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
			c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>StrmHub</title></head><body style="font-family:system-ui;max-width:640px;margin:60px auto;padding:0 20px"><h2>StrmHub 302 代理</h2><p style="color:#e74c3c">未配置 Emby 服务器地址，暂无法反代</p><p>请在「系统配置 → EMBY管理」填写 Emby 服务器地址（如 http://192.168.1.100:8096）后刷新本页。</p><hr style="border:none;border-top:1px solid #eee"><p>本端口提供两个服务：</p><ul><li><b>Emby 反代</b>：<code>http://本机IP:6086/</code> 与 <code>/emby</code> — 配置后直接打开即 Emby</li><li><b>302 直连</b>：<code>http://本机IP:6086/d/文件ID</code> — strm 播放地址（自动生成）</li></ul><p>管理后台在 <code>http://本机IP:6060</code></p></body></html>`)
			return
		}
		targetURL, _ := url.Parse(target)
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.Host = targetURL.Host
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("[Emby反代] 转发失败: %v", err)
				http.Error(w, "Emby 服务器无法连接: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	})
}
