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

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 302 代理服务 ====================

// 播放缓存由 playbackLinkResolver 管理，按规范化 pickcode 与实际 UA 区分。

type downloadCacheEntry struct {
	URL    string
	Expiry time.Time
}

// StartProxy 启动302代理服务（独立端口）
func StartProxy(db *gorm.DB, cfg *config.Config) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies(nil)

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

	registerDirectPlaybackRoutes(r, db, cfg)

	// Emby 反代：客户端访问 http://ip:6086/emby 即可使用 Emby（CMS 9096 同款）
	registerEmbyProxy(r, db, cfg)

	log.Printf("302 直链代理已启动（端口 %d）", cfg.ProxyPort)
	if err := r.Run(":" + cfg.ProxyPortStr()); err != nil {
		log.Printf("代理服务启动失败: %v", err)
	}
}

// handleProxyRedirect 处理 302 重定向请求
// proxyRateLim 直链限流：pickcode 是"知道即可用"的弱凭据，无鉴权端点
// 不能让公网无限速换取直链（请求 DoS 面）。20 次/分/IP 足够正常
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

	servePickcodeDirect(c, db, cfg, pickcode)
}

// servePickcodeDirect 只换链并返回 302。播放器（含空 UA）直接向 CDN 取流，
// 失败明确报错，不再以占用服务器带宽的方式掩盖兼容性问题。
func servePickcodeDirect(c *gin.Context, db *gorm.DB, cfg *config.Config, pickcode string) {
	u, err := playbackLinks.resolve(c.Request.Context(), db, cfg, pickcode, c.Request.UserAgent())
	if err != nil {
		log.Printf("[播放] ✗ 取链失败，未启用中转")
		c.String(http.StatusBadGateway, "无法获取可直接播放的地址，请检查账号、播放器 UA 与直链通道")
		return
	}
	vlog("[播放] ✓ 返回 CDN 302")
	playbackRedirect(c.Writer, c.Request, u)
}

func registerDirectPlaybackRoutes(r gin.IRouter, db *gorm.DB, cfg *config.Config) {
	for _, route := range []string{"/d/:pickcode", "/d/:pickcode/*filename"} {
		r.Handle(http.MethodGet, route, func(c *gin.Context) { handleProxyRedirect(c, db, cfg) })
		r.Handle(http.MethodHead, route, func(c *gin.Context) { handleProxyRedirect(c, db, cfg) })
	}
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
		signUA = ua115Download
	}
	return get115DownloadURLForUA(pickcode, cookie, signUA)
}

// 播放专用入口必须保留空 UA，后台文件下载仍可使用默认 UA。
func get115DownloadURLForUA(pickcode, cookie, signUA string) (string, map[string]string, error) {
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

	body, err = httpGet115Full(fullURL, nil, cookie, signUA, 15*time.Second, map[string]string{"User-Agent": signUA})
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
	playbackLinks.mu.Lock()
	defer playbackLinks.mu.Unlock()
	now := time.Now()
	for key, entry := range playbackLinks.cache {
		if !now.Before(entry.Expiry) {
			delete(playbackLinks.cache, key)
		}
	}
}
