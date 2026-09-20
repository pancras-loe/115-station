package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 302 代理服务 ====================

// downloadLinkCache 下载链接缓存 {pickcode -> {url, expiry}}
var downloadLinkCache = make(map[string]downloadCacheEntry)
var downloadCacheMu = sync.Mutex{}

type downloadCacheEntry struct {
	URL    string
	Expiry time.Time
}

// StartProxy 启动302代理服务（独立端口）
func StartProxy(db *gorm.DB, cfg *config.Config) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 定期清理过期下载链接缓存（每 10 分钟），防止内存只增不减
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanExpiredCache()
			case <-stopCh:
				return
			}
		}
	}()

	// 健康检查
	r.GET("/proxy/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "running", "port": cfg.ProxyPort})
	})

	// 企业微信机器人回调（无 JWT 鉴权：协议自带 Token 签名 + AES 加密验证）
	botHandler := &Handler{DB: db, Config: cfg}
	r.GET("/wecom/callback", botHandler.WecomCallback)
	r.POST("/wecom/callback", botHandler.WecomCallback)

	// 302 代理核心路由: /d/{pickcode} 或 /d/{pickcode}/{filename}
	r.GET("/d/:pickcode", func(c *gin.Context) {
		handleProxyRedirect(c, db, cfg)
	})
	r.GET("/d/:pickcode/*filename", func(c *gin.Context) {
		handleProxyRedirect(c, db, cfg)
	})

	// 按需离线播放端点: /ed2k/play/{id}（STRM 占位内容指向这里，边下边播）
	RegisterOfflinePlayRoutes(r, botHandler)

	// Emby 反代：客户端访问 http://ip:6086/emby 即可使用 Emby（CMS 9096 同款）
	registerEmbyProxy(r, db, cfg)

	log.Printf("302 直链代理已启动（端口 %d）", cfg.ProxyPort)
	if err := r.Run(":" + cfg.ProxyPortStr()); err != nil {
		log.Printf("代理服务启动失败: %v", err)
	}
}

// handleProxyRedirect 处理 302 重定向请求
// proxyRateLim 直链/中转限流：pickcode 是"知道即可用"的弱凭据，无鉴权端点
// 不能让公网无限速换取直链或全量中转（带宽 DoS 面）。20 次/分/IP 足够正常
// 播放（每次起播 1 次 302），能挡住遍历抓取
var (
	proxyRateMu        sync.Mutex
	proxyRateBkts      = map[string]*proxyBucket{}
	proxyRateLastSweep time.Time
)

type proxyBucket struct {
	tokens float64
	last   time.Time
}

func proxyRateAllow(ip string) bool {
	proxyRateMu.Lock()
	defer proxyRateMu.Unlock()
	now := time.Now()
	// 顺带清理 10 分钟未活跃的桶（防无界增长）
	if now.Sub(proxyRateLastSweep) > 10*time.Minute {
		for k, b := range proxyRateBkts {
			if now.Sub(b.last) > 10*time.Minute {
				delete(proxyRateBkts, k)
			}
		}
		proxyRateLastSweep = now
	}
	const rate = 20.0 / 60.0 // 每秒补充速率（20/分钟）
	b, ok := proxyRateBkts[ip]
	if !ok {
		b = &proxyBucket{tokens: 20, last: now} // 突发额度 20
		proxyRateBkts[ip] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > 20 {
			b.tokens = 20
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func handleProxyRedirect(c *gin.Context, db *gorm.DB, cfg *config.Config) {
	if !proxyRateAllow(c.ClientIP()) {
		c.String(http.StatusTooManyRequests, "too many requests")
		return
	}
	pickcode := c.Param("pickcode")
	if pickcode == "" {
		c.String(http.StatusBadRequest, "missing pickcode")
		return
	}
	// 兼容 /d/{pickcode}.{ext}?/{name} 形态：pickcode 段可能带文件后缀，
	// 115 pickcode 为纯字母数字，剥掉最后一个 "." 之后的部分即可
	if i := strings.LastIndex(pickcode, "."); i > 0 {
		pickcode = pickcode[:i]
	}
	// 旧版 STRM 用数字 fid 生成 /d/{fid}/...，查台账换回 pick_code
	if isAllDigits(pickcode) {
		var sf model.SyncedFile
		if err := db.Where("file_id = ?", pickcode).First(&sf).Error; err == nil && sf.PickCode != "" {
			pickcode = sf.PickCode
		}
	}

	servePickcodeDirect(c, db, cfg, pickcode)
}

// servePickcodeDirect 已知 pickcode 的出流公共路径（/d/ 302 与 /ed2k/play 共用）：
// 空 UA 走服务端中转，其余按请求 UA 签发直链 302（直链与 UA 绑定，缓存键含 UA）
func servePickcodeDirect(c *gin.Context, db *gorm.DB, cfg *config.Config, pickcode string) {
	reqUA := c.Request.UserAgent()
	vlog("302代理请求: pickcode=%s, UA=%s", pickcode, reqUA)

	// UA 缺失的播放器（部分安卓内核不发自定义 UA）：115 直链与 UA 绑定，
	// 空 UA 客户端拿到 302 后去 CDN 取流会被拒（浏览器正常、这类手机播不动）。
	// 改为服务端中转：用统一 UA 签发直链，把字节流转发给客户端（透传 Range）
	if strings.TrimSpace(reqUA) == "" {
		log.Printf("302代理: %s UA 为空，转服务端中转拉流", pickcode)
		streamVia(c, db, cfg, pickcode)
		return
	}

	// 缓存键含 UA：115 直链与签发 UA 绑定，Emby(Lavf) 与浏览器链不可混用
	cacheKey := pickcode + "|" + reqUA
	downloadCacheMu.Lock()
	cached, ok := downloadLinkCache[cacheKey]
	downloadCacheMu.Unlock()
	if ok && time.Now().Before(cached.Expiry) {
		c.Redirect(http.StatusFound, cached.URL)
		return
	}

	// 获取下载链接：按请求方 UA 签发（OpenAPI 优先，Cookie 回退）
	downloadURL, err := proxyDownloadURL(db, cfg, pickcode, reqUA)
	if err != nil {
		log.Printf("302代理获取下载链接失败: %v", err)
		c.String(http.StatusBadGateway, "获取下载链接失败: %v", err)
		return
	}

	if downloadURL == "" {
		c.String(http.StatusNotFound, "无法获取下载链接")
		return
	}

	// 缓存链接（30 分钟：115 直链约 1 小时有效，绑 UA+IP；取链要过
	// 节流+多通道回退，是起播最贵的一步，能命中就秒开）
	downloadCacheMu.Lock()
	downloadLinkCache[cacheKey] = downloadCacheEntry{
		URL:    downloadURL,
		Expiry: time.Now().Add(30 * time.Minute),
	}
	downloadCacheMu.Unlock()

	vlog("302代理重定向: %s -> %s", pickcode, downloadURL[:min(80, len(downloadURL))]+"...")
	c.Redirect(http.StatusFound, downloadURL)
}

// streamVia 服务端中转拉流：取直链并转发字节流。
// 仅用于不发 User-Agent 的播放器（302 对它们无效），透传 Range 支持拖动。
// 签发 UA 用浏览器 UA（ua115Download）：实测浏览器 UA 拿到的直链免 Cookie
// （f=1/2 型）；ua115Unified 拿到的是 f=3 型（强绑 Set-Cookie，矩阵也过不去）。
// 仍保留 Cookie 组合矩阵兜底，且免 Cookie 组合优先（快，避免播放器超时）
func streamVia(c *gin.Context, db *gorm.DB, cfg *config.Config, pickcode string) {
	rawURL, hdrs, err := proxyDownloadURLFull(db, cfg, pickcode, ua115Download)
	if err != nil || rawURL == "" {
		log.Printf("302代理中转取链失败: %v", err)
		c.String(http.StatusBadGateway, "获取下载链接失败: %v", err)
		return
	}
	loginCookie := proxyLoginCookie(db, cfg)
	setCookie := hdrs["Cookie"]

	fetch := func(cookie string) (*http.Response, error) {
		outReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, rawURL, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range hdrs {
			outReq.Header.Set(k, v)
		}
		if cookie != "" {
			outReq.Header.Set("Cookie", cookie)
		}
		if rng := c.Request.Header.Get("Range"); rng != "" {
			outReq.Header.Set("Range", rng)
		}
		return (&http.Client{}).Do(outReq) // 无整体超时：长视频流式传输
	}

	// 组合去重：免 Cookie 优先（浏览器型直链直接命中，不等重试）
	var combos []string
	add := func(s string) {
		for _, e := range combos {
			if e == s {
				return
			}
		}
		combos = append(combos, s)
	}
	add("")
	if setCookie != "" {
		add(setCookie)
		add(setCookie + "; " + loginCookie)
	}
	add(loginCookie)

	var resp *http.Response
	var lastErr error
	for _, ck := range combos {
		resp, lastErr = fetch(ck)
		if lastErr != nil {
			break // 网络层错误重试无意义
		}
		if resp.StatusCode < 400 {
			break
		}
		log.Printf("302代理中转: 上游 %d（换 Cookie 组合重试）", resp.StatusCode)
		resp.Body.Close()
		resp = nil
	}
	if lastErr != nil {
		log.Printf("302代理中转拉流失败: %v", lastErr)
		c.String(http.StatusBadGateway, "上游拉流失败: %v", lastErr)
		return
	}
	if resp == nil {
		log.Printf("302代理中转: 所有 Cookie 组合均被上游拒绝（no cookie value 等）")
		c.String(http.StatusBadGateway, "上游拒绝拉流")
		return
	}
	defer resp.Body.Close()
	log.Printf("302代理中转: 上游状态=%d ContentLength=%d Range=%q", resp.StatusCode, resp.ContentLength, c.Request.Header.Get("Range"))
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition"} {
		if v := resp.Header.Get(h); v != "" {
			c.Writer.Header().Set(h, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	flusher, _ := c.Writer.(http.Flusher)
	buf := make([]byte, 64*1024)
	sent := int64(0)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				log.Printf("302代理中转: 客户端断开（已转发 %d 字节）", sent)
				return // 客户端断开
			}
			sent += int64(n)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				log.Printf("302代理中转: 上游读取结束（已转发 %d 字节）: %v", sent, rerr)
			}
			return
		}
	}
}

// proxyLoginCookie 取登录 Cookie（配置文件优先，回退 Storage 表），
// 与 proxyDownloadURLFull 的 Cookie 通道同一套解析
func proxyLoginCookie(db *gorm.DB, cfg *config.Config) string {
	if ck, err := cfg.LoadCookie(); err == nil && ck != "" {
		return ck
	}
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err == nil {
		return storage.Cookie
	}
	return ""
}

// ua115Download 下载链路专用 UA（openStrm defaultUA 同款，浏览器 UA 签发的直链
// 在 CDN 侧校验更宽松；直链绑定时也要求下载携带同一 UA）
const ua115Download = "Mozilla/5.0 (iPhone; CPU iPhone OS 15_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/116.0.5845.89 Mobile/15E148 Safari/604.1"

// get115DownloadURL 通过 pickcode 获取 115 文件下载链接
// 首选 App 加密接口（pro.api/android/2.0/ufile/download，openStrm 同款），
// webapi files/download 在部分 Cookie 类型下只返回元数据不含链接。
// 返回的 headers 为 CDN 要求携带的请求头（下载 UA + 直链响应 Set-Cookie）
// reJSONURL 115 响应兜底提取直链（每次 302 播放路径上，预编译）
var reJSONURL = regexp.MustCompile(`"url"\s*:\s*"(https?://[^"]+)"`)

// dlFastState 取链通道粘滞：记住上次成功的通道/端点，粘滞期内直接优先用它，
// 消除每次起播都从 OpenAPI→Cookie→proapi→pro.api→webapi 全串行试一遍的
// 最坏路径（此前最坏 30~45 秒）。通道失败时立即清除粘滞回全序
type dlFastPref struct {
	kind  string // "open" / "app:proapi" / "app:proapi2" / "web"
	until time.Time
}

var (
	dlFastMu    sync.Mutex
	dlFastPrefV dlFastPref
)

func dlFastGet() (dlFastPref, bool) {
	dlFastMu.Lock()
	defer dlFastMu.Unlock()
	p := dlFastPrefV
	return p, p.kind != "" && time.Now().Before(p.until)
}

func dlFastSet(kind string, d time.Duration) {
	dlFastMu.Lock()
	dlFastPrefV = dlFastPref{kind: kind, until: time.Now().Add(d)}
	dlFastMu.Unlock()
}

func dlFastClear() {
	dlFastMu.Lock()
	dlFastPrefV = dlFastPref{}
	dlFastMu.Unlock()
}

// 取直链端点首选超时：快速失败比干等更值——备用端点/通道紧跟其后
const dlFirstTryTimeout = 5 * time.Second

func hostKey(host string) string {
	if strings.HasPrefix(host, "proapi.") {
		return "proapi"
	}
	if strings.HasPrefix(host, "pro.api.") {
		return "pro.api"
	}
	return host
}

func get115DownloadURL(pickcode, cookie, signUA string) (string, map[string]string, error) {
	if signUA == "" {
		signUA = ua115Download // 默认浏览器 UA（附属文件下载等自有场景）
	}
	// ---- 首选：App 加密接口（用签发 UA 请求，直链即绑定该 UA）----
	// 双端点轮询：proapi.115.com（p115client/OpenList 同款）与
	// pro.api.115.com（openStrm 同款）是独立风控的两台主机——实测一个
	// 被 410/证书降级时另一个常可用
	appErr := ""
	payload, _ := json.Marshal(map[string]string{"pick_code": pickcode})
	form := url.Values{"data": {encrypt115(payload)}}
	var body []byte
	var resp *http.Response
	var err error
	endpoints := []string{
		"https://proapi.115.com/android/2.0/ufile/download",
		"https://pro.api.115.com/android/2.0/ufile/download",
	}
	// 粘滞：上次成功的端点提到最前（粘滞期内省掉一次必败尝试）
	if pref, ok := dlFastGet(); ok && strings.HasPrefix(pref.kind, "app:") {
		stick := strings.TrimPrefix(pref.kind, "app:")
		for i, ep := range endpoints {
			if strings.Contains(ep, stick) && i > 0 {
				endpoints[0], endpoints[i] = endpoints[i], endpoints[0]
				break
			}
		}
	}
	seenErr := ""
	for _, ep := range endpoints {
		body, resp, err = post115FormResp(ep, form, cookie, signUA, dlFirstTryTimeout)
		if err == nil {
			break
		}
		seenErr += ep + " 请求失败: " + err.Error() + "；"
	}
	appErr += seenErr
	if appErr == "" {
		appErr = "未尝试"
	}
	if err != nil {
		// 双端点全失败：appErr 已含各自原因，走 webapi 回退
	} else {
		var env struct {
			State   json.RawMessage `json:"state"`
			ErrNo   int             `json:"errno"`
			ErrCode int             `json:"errcode"`
			Error   string          `json:"error"`
			Data    string          `json:"data"`
		}
		if jerr := json.Unmarshal(body, &env); jerr != nil {
			appErr = "响应非 JSON: " + truncateStr(string(body), 150)
		} else if !openStateOK(env.State) {
			appErr = fmt.Sprintf("state=%s error=%s", strings.TrimSpace(string(env.State)), env.Error)
		} else if env.Data == "" {
			appErr = "响应无加密数据: " + truncateStr(string(body), 150)
		} else {
			plain, derr := decrypt115(env.Data)
			if derr != nil {
				appErr = "解密失败: " + derr.Error()
			} else {
				var d struct {
					URL json.RawMessage `json:"url"`
				}
				var u string
				if json.Unmarshal(plain, &d) == nil {
					u = openParseDownloadURL(d.URL)
				}
				if u == "" {
					if m := reJSONURL.FindSubmatch(plain); m != nil {
						u = string(m[1])
					}
				}
				if u != "" {
					dlFastSet("app:"+hostKey(resp.Request.URL.Host), 10*time.Minute)
					// 收集直链响应下发的 Set-Cookie（CDN f=3 场景要求回带）
					var parts []string
					for _, ck := range resp.Cookies() {
						parts = append(parts, ck.Name+"="+ck.Value)
					}
					headers := map[string]string{"User-Agent": signUA}
					if len(parts) > 0 {
						headers["Cookie"] = strings.Join(parts, "; ")
					}
					return u, headers, nil
				}
				appErr = "解密后无链接: " + truncateStr(string(plain), 150)
			}
		}
	}

	// ---- 回退：GET https://webapi.115.com/files/download?pickcode=xxx ----
	dlFastClear() // app 通道整体失败：清粘滞回全序
	apiURL := "https://webapi.115.com/files/download"
	params := fmt.Sprintf("pickcode=%s", pickcode)
	fullURL := apiURL + "?" + params

	body, err = httpGet115(fullURL, nil, cookie, 15*time.Second)
	if err != nil {
		return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: %v", appErr, err)
	}

	var result struct {
		State bool `json:"state"`
		URL   struct {
			URL string `json:"url"`
		} `json:"url"`
		// 有些版本返回的格式不同
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		if !result.State {
			var e struct {
				Error  string `json:"error"`
				ErrNo  int    `json:"errno"`
				ErrNo2 int    `json:"errNo"`
			}
			_ = json.Unmarshal(body, &e)
			msg := e.Error
			if msg == "" {
				msg = "未知错误"
			}
			if e.ErrNo != 0 {
				msg += fmt.Sprintf("（errno=%d）", e.ErrNo)
			} else if e.ErrNo2 != 0 {
				msg += fmt.Sprintf("（errNo=%d）", e.ErrNo2)
			}
			return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: 拒绝: %s", appErr, msg)
		}
		if result.URL.URL != "" {
			return result.URL.URL, nil, nil
		}
		if result.Data.URL != "" {
			return result.Data.URL, nil, nil
		}
	}
	// 常规字段为空时用正则兜底提取任意位置的下载链接
	if m := reJSONURL.FindSubmatch(body); m != nil {
		return string(m[1]), nil, nil
	}
	return "", nil, fmt.Errorf("获取下载链接失败 [app接口]: %s；[webapi接口]: %s", appErr, truncateStr(string(body), 150))
}

// post115Form 带 Cookie 的 115 表单 POST（可指定 UA，空串表示发空 UA 头）
func post115Form(api string, form url.Values, cookie, ua string, timeout time.Duration) ([]byte, error) {
	body, _, err := post115FormResp(api, form, cookie, ua, timeout)
	return body, err
}

// post115FormResp 同 post115Form 但额外返回响应对象（读取 Set-Cookie 用）
func post115FormResp(api string, form url.Values, cookie, ua string, timeout time.Duration) ([]byte, *http.Response, error) {
	throttle115(api)
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// 115 专属客户端：pro.api 等节点证书缺 SAN，需容忍主机名不匹配（链仍校验）
	resp, err := client115(timeout).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	throttle115Done(api) // 节流锚点推进到本请求完成时刻
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return b, resp, nil
}

// 清理过期缓存（定期调用）
func cleanExpiredCache() {
	downloadCacheMu.Lock()
	defer downloadCacheMu.Unlock()
	now := time.Now()
	for k, v := range downloadLinkCache {
		if now.After(v.Expiry) {
			delete(downloadLinkCache, k)
		}
	}
}
