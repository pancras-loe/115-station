package api

// ==================== RE0（影视资料与分享社区）OpenAPI 接入 ====================
//
// 官方文档：https://re0.me/manager/api-docs（公开 ZIP：/downloads/openapi-docs/re0-openapi-docs.zip）
// 接入模型：OpenAPI 应用（X-API-Key = 应用 Secret）+ OAuth 用户授权（Bearer 用户 Token）：
//
//	1. 在 RE0「个人面板 → 我的应用」创建应用并等站方审核通过（公开且用户 6+，或长期 v 用户）
//	2. 115-Station 配置 client_id / Secret
//	3. 点「授权」跳转 re0.me/openapi/authorize → 回调 /api/re0/oauth/callback 换取用户 Token
//	4. 搜索 = TMDB 片名 → tmdb_id → GET /api/open/resources/{type}/{tmdb_id}
//	5. 解锁 = POST /api/open/resources/unlock → full_url(115 分享链接) → 现有分享转存引擎
//
// Access Token 过期自动用 Refresh Token 刷新一次并重放；Refresh 失效提示重新授权。
//
// 2026-10 按新版文档（docs/RE0 OpenAPI 文档.md）对齐：
//   - 包括 ping 在内，所有 /api/open/* 都要用户 Token，ping 还要 meta 权限；
//     没授权时 ping 回 OPENAPI_USER_REQUIRED，正好拿它区分「Secret 对不对」与「授权没授权」
//   - OPENAPI_REAUTH_REQUIRED / INVALID_OPENAPI_USER_TOKEN 刷新也救不回来：清掉 Token、提示重新授权
//   - 换 Token 的返回不再带 user，用户名改由授权后读一次 /me 拿
//   - 2026-08-24 起只对有效 V / 长期 V 用户开放，403 时把这一条说出来

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const re0DefaultBase = "https://re0.me"

// re0Scope 申请的权限：meta（ping 检查）+ query（查资源、文件预览、/me）+ unlock（解锁）+ write（每日签到）。
// write 还能管分享，本站只用它签到
const re0Scope = "meta query unlock write"

// re0LegacyScope 2026-10 之前授权的 Token 没记 scope，当时申请的就是这两项
const re0LegacyScope = "query unlock"

// re0Cfg RE0 配置（setting key "re0"）
type re0Cfg struct {
	BaseURL      string `json:"base_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenExp     int64  `json:"token_exp"`     // Access Token 过期时间（unix 秒）
	AuthorizedAs string `json:"authorized_as"` // 授权用户展示名
	RedirectURI  string `json:"redirect_uri"`  // 上次授权的回调地址（code 交换必须一致）
	Scope        string `json:"scope"`         // 这次授权实际给的权限（空格分隔）；老 Token 为空，按 re0LegacyScope 算
}

// grantedScopes 当前 Token 拿到的权限
func (c *re0Cfg) grantedScopes() map[string]bool {
	sc := c.Scope
	if sc == "" {
		sc = re0LegacyScope
	}
	out := map[string]bool{}
	for _, f := range strings.Fields(sc) {
		out[f] = true
	}
	return out
}

// missingScopes 本站要用、这次授权却没给的权限（没授权时为空）：非空就该提示重新授权
func (c *re0Cfg) missingScopes() []string {
	if c.AccessToken == "" {
		return nil
	}
	have := c.grantedScopes()
	var out []string
	for _, f := range strings.Fields(re0Scope) {
		if !have[f] {
			out = append(out, f)
		}
	}
	return out
}

// re0ScopeOf 换 Token / 刷新返回的权限：scope 字符串优先，没有就拼 scopes 数组
func re0ScopeOf(scope string, scopes []string) string {
	if s := strings.TrimSpace(scope); s != "" {
		return s
	}
	return strings.Join(scopes, " ")
}

var (
	re0CfgMu sync.Mutex
	re0CfgV  *re0Cfg
	re0CfgAt time.Time
)

func re0NormalizeBase(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" {
		return re0DefaultBase
	}
	if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
		b = "https://" + b
	}
	return b
}

func loadRe0Cfg() *re0Cfg {
	re0CfgMu.Lock()
	defer re0CfgMu.Unlock()
	if re0CfgV != nil && time.Since(re0CfgAt) < 10*time.Second {
		return re0CfgV
	}
	cfg := &re0Cfg{BaseURL: re0DefaultBase}
	if v := settingValueCompat("re0"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	cfg.BaseURL = re0NormalizeBase(cfg.BaseURL)
	re0CfgV = cfg
	re0CfgAt = time.Now()
	return cfg
}

func saveRe0Cfg(cfg *re0Cfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("re0", string(b)); err != nil {
		return err
	}
	re0CfgMu.Lock()
	re0CfgV = nil
	re0CfgAt = time.Time{}
	re0CfgMu.Unlock()
	return nil
}

// ==================== HTTP 客户端（envelope 解析 + 自动刷新） ====================

var re0HTTP = &http.Client{Timeout: 20 * time.Second}

// re0Envelope OpenAPI 统一响应格式（response-format.md）
type re0Envelope struct {
	Success     bool            `json:"success"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Description string          `json:"description"`
	RetryAfter  int             `json:"retry_after_seconds"`
	Data        json.RawMessage `json:"data"`
}

// re0Err 业务错误（带站方原始 code，便于前端精准提示）
type re0Err struct {
	Code        string
	Message     string
	Description string
	Status      int // HTTP 状态码
}

func (e *re0Err) Error() string {
	msg := e.Message
	if e.Description != "" && e.Description != e.Message {
		msg += "：" + e.Description
	}
	if e.Code != "" {
		msg = fmt.Sprintf("[%s] %s", e.Code, msg)
	}
	if hint := re0ErrHint(e.Code, e.Status); hint != "" {
		msg += "（" + hint + "）"
	}
	return msg
}

// re0ErrHint 站方错误码 → 用户该做什么。站方文案多是英文或只说现象，这里补上去哪儿改
func re0ErrHint(code string, status int) string {
	switch code {
	case "MISSING_API_KEY", "INVALID_API_KEY", "DISABLED_API_KEY", "EXPIRED_API_KEY":
		return "应用 Secret 无效、被停用或已过期，去 RE0「我的应用」核对"
	case "SCOPE_NOT_ALLOWED":
		return "RE0 应用没开通这组接口，去 RE0「我的应用」申请"
	case "USER_SCOPE_NOT_ALLOWED":
		return "授权时没包含这项权限，请在 RE0 设置里重新授权"
	case "IP_NOT_ALLOWED":
		return "本站出口 IP 不在应用白名单里，RE0 设置里可查出口 IP"
	case "IP_COUNT_EXCEEDED":
		return "出口 IP 变化太频繁，应用进入冷却"
	case "INSUFFICIENT_POINTS":
		return "积分不足"
	case "RISK_BLOCKED", "USER_TIER_NOT_ALLOWED":
		return ""
	}
	if status == http.StatusForbidden {
		// 2026-08-24 起 OpenAPI 只对有效 V / 长期 V 开放，站方没给专门的错误码
		return "RE0 OpenAPI 只对有效 V / 长期 V 用户开放，请确认账号的 V 状态"
	}
	return ""
}

// re0NeedsReauth 刷新也救不回来、只能重新走 OAuth 的错误
func re0NeedsReauth(code string) bool {
	return code == "OPENAPI_REAUTH_REQUIRED" || code == "INVALID_OPENAPI_USER_TOKEN"
}

// errRe0Reauth 授权失效：Token 已清掉，界面上显示为未授权
var errRe0Reauth = errors.New("RE0 授权已失效，请在「影视转存 → 设置 → RE0」重新授权")

// re0Call 调用 /api/open/* 业务接口：带 X-API-Key + Bearer（ping 也要，见文件头），
// 收到 OPENAPI_REFRESH_REQUIRED 时刷新 Token 重放一次；要重新授权的清掉 Token
func re0Call(h *Handler, cfg *re0Cfg, method, path string, query url.Values, body any, out any) error {
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := re0EnsureToken(cfg); err != nil {
			return err
		}
		env, status, err := re0DoOnce(cfg, method, path, query, body)
		if err != nil {
			return err
		}
		if env.Success {
			if out != nil && len(env.Data) > 0 {
				if err := json.Unmarshal(env.Data, out); err != nil {
					return fmt.Errorf("RE0 响应解析失败: %v", err)
				}
			}
			return nil
		}
		switch {
		case env.Code == "OPENAPI_REFRESH_REQUIRED" && attempt == 0 && cfg.RefreshToken != "":
			if rerr := re0Refresh(cfg); rerr != nil {
				return rerr
			}
			continue
		case re0NeedsReauth(env.Code):
			re0ClearAuth(cfg, env.Code)
			return errRe0Reauth
		case env.Code == "OPENAPI_USER_REQUIRED":
			// 没带 Token：刷新没用（站方认不出是谁），只能授权
			return fmt.Errorf("尚未授权 RE0（先在配置里完成 OAuth 授权）")
		}
		return &re0Err{Code: env.Code, Message: env.Message, Description: env.Description, Status: status}
	}
	return fmt.Errorf("RE0 请求未通过")
}

// re0ClearAuth 授权失效：清掉 Token 落盘，界面显示未授权，后台任务别再拿它反复撞
func re0ClearAuth(cfg *re0Cfg, why string) {
	cfg.AccessToken, cfg.RefreshToken, cfg.TokenExp, cfg.Scope = "", "", 0, ""
	if err := saveRe0Cfg(cfg); err != nil {
		log.Printf("[RE0] ○ 清除失效授权落盘失败: %v", err)
	}
	log.Printf("[RE0] ✗ 授权已失效（%s），需要重新授权", why)
}

// re0DoOnce 单次 HTTP 调用并解析 envelope（429 带回 Retry-After）。返回 HTTP 状态码
func re0DoOnce(cfg *re0Cfg, method, path string, query url.Values, body any) (*re0Envelope, int, error) {
	full := strings.TrimRight(cfg.BaseURL, "/") + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, full, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-API-Key", cfg.ClientSecret)
	if cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := re0HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连接 RE0 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env re0Envelope
	if json.Unmarshal(raw, &env) != nil {
		return nil, resp.StatusCode, fmt.Errorf("RE0 响应异常（HTTP %d）: %s", resp.StatusCode, truncateStr(string(raw), 120))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		ra := env.RetryAfter
		if ra == 0 {
			if v, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
				ra = v
			}
		}
		if ra == 0 {
			ra = 60
		}
		return &env, resp.StatusCode, fmt.Errorf("RE0 限流中（%s），约 %d 秒后可重试", firstNonEmpty(env.Description, env.Message, env.Code), ra)
	}
	return &env, resp.StatusCode, nil
}

// re0EnsureToken Token 剩余寿命不足 3 分钟时提前刷新
func re0EnsureToken(cfg *re0Cfg) error {
	if cfg.AccessToken == "" {
		return fmt.Errorf("尚未授权 RE0（先在配置里完成 OAuth 授权）")
	}
	if cfg.RefreshToken == "" || cfg.TokenExp == 0 || cfg.TokenExp-time.Now().Unix() > 180 {
		return nil
	}
	return re0Refresh(cfg)
}

// re0TokenResp 换 Token / 刷新 Token 的返回
type re0TokenResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    int64    `json:"expires_in"`
		Scope        string   `json:"scope"`
		Scopes       []string `json:"scopes"`
		// 新版文档里换 Token 不再返回 user，老返回还带着就顺手用
		User struct {
			Nickname string `json:"nickname"`
			Username string `json:"username"`
		} `json:"user"`
	} `json:"data"`
}

// apply 把新 Token 写进配置（不落盘）
func (t *re0TokenResp) apply(cfg *re0Cfg) {
	cfg.AccessToken = t.Data.AccessToken
	if t.Data.RefreshToken != "" {
		cfg.RefreshToken = t.Data.RefreshToken
	}
	if t.Data.ExpiresIn > 0 {
		cfg.TokenExp = time.Now().Unix() + t.Data.ExpiresIn
	}
	if sc := re0ScopeOf(t.Data.Scope, t.Data.Scopes); sc != "" {
		cfg.Scope = sc
	}
}

// re0Refresh 用 Refresh Token 换新 Access Token（官方建议：失败则重新走 OAuth 授权）
func re0Refresh(cfg *re0Cfg) error {
	if cfg.RefreshToken == "" {
		return errRe0Reauth
	}
	body := map[string]string{"refresh_token": cfg.RefreshToken}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/api/public/openapi/oauth/refresh", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := re0HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("刷新 RE0 Token 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out re0TokenResp
	if json.Unmarshal(raw, &out) != nil {
		return fmt.Errorf("刷新 RE0 Token 失败（HTTP %d）: %s", resp.StatusCode, truncateStr(string(raw), 120))
	}
	if !out.Success || out.Data.AccessToken == "" {
		// Refresh Token 失效 / 撤销 / 过期：只能重新授权。别的错误（限流、站方故障）留着 Token 下次再试
		if re0NeedsReauth(out.Code) || resp.StatusCode == http.StatusUnauthorized {
			re0ClearAuth(cfg, firstNonEmpty(out.Code, "refresh 401"))
			return errRe0Reauth
		}
		return fmt.Errorf("刷新 RE0 Token 失败：%s %s", out.Code, out.Message)
	}
	out.apply(cfg)
	if err := saveRe0Cfg(cfg); err != nil {
		log.Printf("[RE0] ○ Token 刷新后落盘失败: %v", err)
	}
	return nil
}

// ==================== OAuth 授权流程 ====================

// re0StateStore state → redirect_uri（防 CSRF + code 交换一致性），10 分钟有效
var (
	re0StateMu  sync.Mutex
	re0StateMap = map[string]re0StateEntry{}
)

type re0StateEntry struct {
	RedirectURI string
	ExpiresAt   time.Time
}

func re0StatePut(state, redirectURI string) {
	re0StateMu.Lock()
	defer re0StateMu.Unlock()
	// 顺带清理过期项
	for k, v := range re0StateMap {
		if time.Now().After(v.ExpiresAt) {
			delete(re0StateMap, k)
		}
	}
	re0StateMap[state] = re0StateEntry{RedirectURI: redirectURI, ExpiresAt: time.Now().Add(10 * time.Minute)}
}

func re0StateTake(state string) (string, bool) {
	re0StateMu.Lock()
	defer re0StateMu.Unlock()
	e, ok := re0StateMap[state]
	if !ok {
		return "", false
	}
	delete(re0StateMap, state)
	if time.Now().After(e.ExpiresAt) {
		return "", false
	}
	return e.RedirectURI, true
}

// Re0OAuthStart GET /re0/oauth/start?redirect_uri=...
// 前端传浏览器当前访问的地址 + /api/re0/oauth/callback：回跳只经过用户自己的浏览器，
// 内网地址也行，不需要公网；前提是应用的「固定 Redirect URI 白名单」留空（动态回调）。
func (h *Handler) Re0OAuthStart(c *gin.Context) {
	cfg := loadRe0Cfg()
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写 client_id 和应用 Secret 并保存"})
		return
	}
	redirectURI := strings.TrimSpace(c.Query("redirect_uri"))
	if !strings.HasPrefix(redirectURI, "http://") && !strings.HasPrefix(redirectURI, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "回调地址必须是完整的 http(s) URL"})
		return
	}
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 state 失败"})
		return
	}
	state := hex.EncodeToString(stateBytes)
	re0StatePut(state, redirectURI)
	authorizeURL := fmt.Sprintf("%s/openapi/authorize?client_id=%s&redirect_uri=%s&scope=%s&state=%s&response_mode=redirect",
		strings.TrimRight(cfg.BaseURL, "/"),
		url.QueryEscape(cfg.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(re0Scope),
		url.QueryEscape(state),
	)
	c.JSON(http.StatusOK, gin.H{"authorize_url": authorizeURL})
}

// Re0OAuthCallback GET /api/re0/oauth/callback?code=...&state=...（浏览器回跳，公开路由）
func (h *Handler) Re0OAuthCallback(c *gin.Context) {
	state := c.Query("state")
	code := c.Query("code")
	redirectURI, ok := re0StateTake(state)
	if !ok {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8",
			[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权失败</h3><p>state 无效或已过期（10 分钟内有效），请回 115-Station 重新点「授权」。</p></body>`))
		return
	}
	if code == "" {
		c.Data(http.StatusBadRequest, "text/html; charset=utf-8",
			[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权未完成</h3><p>授权页未返回授权码，请重试。</p></body>`))
		return
	}
	cfg := loadRe0Cfg()
	// 授权码换 Token
	body := map[string]string{"grant_type": "authorization_code", "code": code, "redirect_uri": redirectURI}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/api/public/openapi/oauth/token", bytes.NewReader(b))
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", []byte("授权失败: "+err.Error()))
		return
	}
	req.Header.Set("X-API-Key", cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := re0HTTP.Do(req)
	if err != nil {
		c.Data(http.StatusBadGateway, "text/html; charset=utf-8", []byte("授权失败: "+err.Error()))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out re0TokenResp
	if json.Unmarshal(raw, &out) != nil || !out.Success || out.Data.AccessToken == "" {
		msg := out.Message
		if msg == "" {
			msg = truncateStr(string(raw), 150)
		}
		c.Data(http.StatusBadGateway, "text/html; charset=utf-8",
			[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权失败</h3><p>`+htmlEscape(msg)+`</p></body>`))
		return
	}
	// 重新授权换了账号：旧的权限记录不能沿用
	cfg.Scope = ""
	out.apply(cfg)
	if cfg.Scope == "" {
		cfg.Scope = re0Scope // 返回里没写权限：按这次申请的算
	}
	cfg.AuthorizedAs = firstNonEmpty(out.Data.User.Nickname, out.Data.User.Username)
	if cfg.AuthorizedAs == "" {
		// 新版换 Token 不带用户信息，读一次 /me
		if me, err := re0FetchMe(h, cfg); err == nil {
			cfg.AuthorizedAs = me.Name()
		} else if errors.Is(err, errRe0Reauth) {
			c.Data(http.StatusBadGateway, "text/html; charset=utf-8",
				[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权失败</h3><p>刚换到的 Token 被 RE0 拒绝，请重试授权。</p></body>`))
			return
		} else {
			log.Printf("[RE0] ○ 授权后读用户信息失败: %v", err)
		}
	}
	cfg.RedirectURI = redirectURI
	if err := saveRe0Cfg(cfg); err != nil {
		log.Printf("[RE0] ○ 授权 Token 落盘失败: %v", err)
	}
	log.Printf("[RE0] ✓ OAuth 授权完成（用户: %s）", cfg.AuthorizedAs)
	c.Data(http.StatusOK, "text/html; charset=utf-8",
		[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✓ RE0 授权成功</h3><p>已绑定用户 `+htmlEscape(cfg.AuthorizedAs)+`，请关闭此页返回 115-Station。</p></body>`))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

// ==================== 配置与状态 ====================

// Re0GetConfig GET /re0/config
func (h *Handler) Re0GetConfig(c *gin.Context) {
	cfg := loadRe0Cfg()
	secret := cfg.ClientSecret
	if secret != "" {
		secret = settingMask
	}
	authorized := cfg.AccessToken != "" && (cfg.TokenExp == 0 || cfg.TokenExp > time.Now().Unix())
	c.JSON(http.StatusOK, gin.H{
		"base_url":      cfg.BaseURL,
		"client_id":     cfg.ClientID,
		"client_secret": secret,
		"authorized":    authorized,
		"authorized_as": cfg.AuthorizedAs,
		// 授权给的权限不够本站用（2026-10 之前的授权没有 meta / write）：要重新点一次「授权」
		"missing_scopes": cfg.missingScopes(),
	})
}

// Re0SaveConfig POST /re0/config {base_url, client_id, client_secret}
func (h *Handler) Re0SaveConfig(c *gin.Context) {
	var req struct {
		BaseURL      string `json:"base_url"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadRe0Cfg()
	cfg.BaseURL = re0NormalizeBase(req.BaseURL)
	cfg.ClientID = strings.TrimSpace(req.ClientID)
	if s := strings.TrimSpace(req.ClientSecret); s != "" && s != settingMask {
		cfg.ClientSecret = s
	}
	if err := saveRe0Cfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[RE0] ✓ 配置已保存（%s）", cfg.BaseURL)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// Re0Check GET /re0/check —— 应用 Secret 是否有效 + 授权状态 + 账号信息（/me）
//
// 新版 ping 也要用户 Token：没授权时站方回 OPENAPI_USER_REQUIRED，说明 Secret 是认的，
// 回 INVALID_API_KEY 之类才是 Secret 不对。授权了就读 /me（query 权限，老授权也有），
// 有 meta 权限时再 ping 一次拿应用名
func (h *Handler) Re0Check(c *gin.Context) {
	cfg := loadRe0Cfg()
	if cfg.ClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置应用 Secret"})
		return
	}
	out := gin.H{"app_ok": false, "authorized": false}
	if cfg.AccessToken == "" {
		env, status, err := re0DoOnce(cfg, http.MethodGet, "/api/open/ping", nil, nil)
		switch {
		case err != nil:
			c.JSON(http.StatusBadGateway, gin.H{"error": "应用校验失败: " + err.Error()})
		case env.Success || env.Code == "OPENAPI_USER_REQUIRED" || env.Code == "INVALID_OPENAPI_USER_TOKEN":
			out["app_ok"] = true
			out["message"] = "应用 Secret 有效，尚未授权账号"
			c.JSON(http.StatusOK, out)
		default:
			e := &re0Err{Code: env.Code, Message: env.Message, Description: env.Description, Status: status}
			c.JSON(http.StatusBadGateway, gin.H{"error": "应用校验失败: " + e.Error()})
		}
		return
	}
	me, err := re0FetchMe(h, cfg)
	if err != nil {
		if errors.Is(err, errRe0Reauth) {
			out["app_ok"] = true
			out["message"] = err.Error()
			c.JSON(http.StatusOK, out)
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取授权账号失败: " + err.Error()})
		return
	}
	out["app_ok"], out["authorized"] = true, true
	out["user"], out["level"], out["points"], out["banned"] = me.Name(), me.Level, me.Points, me.Banned
	if name := me.Name(); name != "" && name != cfg.AuthorizedAs {
		cfg.AuthorizedAs = name
		if err := saveRe0Cfg(cfg); err != nil {
			log.Printf("[RE0] ○ 用户名落盘失败: %v", err)
		}
	}
	missing := cfg.missingScopes()
	out["missing_scopes"] = missing
	if cfg.grantedScopes()["meta"] {
		var ping struct {
			Name string `json:"name"`
		}
		if err := re0Call(h, cfg, http.MethodGet, "/api/open/ping", nil, nil, &ping); err == nil {
			out["app_name"] = ping.Name
		}
	}
	msg := "授权有效：" + me.Name()
	if me.Points != nil {
		msg += fmt.Sprintf("，积分 %d", *me.Points)
	}
	if len(missing) > 0 {
		msg += "；授权缺少 " + strings.Join(missing, " / ") + " 权限，重新授权一次才能用签到等功能"
	}
	out["message"] = msg
	c.JSON(http.StatusOK, out)
}

// re0Me /api/open/me 的用户信息。文档只列了「用户 ID、level、用户名、头像、封禁状态、points」，
// 字段名按惯例取，取不到就空着
type re0Me struct {
	ID       any    `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Level    any    `json:"level"`
	Avatar   string `json:"avatar"`
	Points   *int   `json:"-"`
	Banned   bool   `json:"-"`
}

// Name 展示名：昵称优先
func (m *re0Me) Name() string { return firstNonEmpty(m.Nickname, m.Username) }

// re0FetchMe 读当前授权用户。points / 封禁状态的类型站方没写死（数字或字符串、bool 或 0/1），宽松解析
func re0FetchMe(h *Handler, cfg *re0Cfg) (*re0Me, error) {
	var raw json.RawMessage
	if err := re0Call(h, cfg, http.MethodGet, "/api/open/me", nil, nil, &raw); err != nil {
		return nil, err
	}
	return parseRe0Me(raw)
}

func parseRe0Me(raw []byte) (*re0Me, error) {
	var me re0Me
	if err := json.Unmarshal(raw, &me); err != nil {
		return nil, fmt.Errorf("RE0 用户信息解析失败: %v", err)
	}
	var loose map[string]any
	json.Unmarshal(raw, &loose)
	if n, ok := looseInt(loose["points"]); ok {
		me.Points = &n
	}
	for _, k := range []string{"is_banned", "banned", "is_ban"} {
		switch v := loose[k].(type) {
		case bool:
			me.Banned = me.Banned || v
		case float64:
			me.Banned = me.Banned || v != 0
		}
	}
	return &me, nil
}

// looseInt 数字或数字字符串 → int
func looseInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n, true
		}
	}
	return 0, false
}

// Re0EgressIP GET /re0/egress-ip —— 本站访问 RE0 时的出口 IP（填应用的「预期服务端出口 IP」用）
//
// 读站点自己的 Cloudflare `/cdn-cgi/trace`，而不是通用的「查我的 IP」服务：
// 那是 RE0 边缘实际看到的地址（IPv4 还是 IPv6、走没走 HTTPS_PROXY 都一致）。
// 走同一个 re0HTTP，否则查出来的和真正发请求的不是同一条出口。
// 站点不在 Cloudflare 后面时退回 cloudflare.com 的 trace，结果可能与实际出口的 IP 版本不同。
func (h *Handler) Re0EgressIP(c *gin.Context) {
	cfg := loadRe0Cfg()
	sources := []string{strings.TrimRight(cfg.BaseURL, "/") + "/cdn-cgi/trace", "https://www.cloudflare.com/cdn-cgi/trace"}
	var lastErr error
	for i, src := range sources {
		ip, err := re0TraceIP(src)
		if err != nil {
			lastErr = err
			continue
		}
		c.JSON(http.StatusOK, gin.H{"ip": ip, "ipv6": strings.Contains(ip, ":"), "exact": i == 0})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "查询出口 IP 失败: " + lastErr.Error()})
}

func re0TraceIP(u string) (string, error) {
	resp, err := re0HTTP.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return parseTraceIP(string(raw))
}

// parseTraceIP 从 trace 的 `key=value` 行里取 ip=，并确认是合法 IP（防止拿到挑战页之类的 HTML）
func parseTraceIP(body string) (string, error) {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("响应里没有 ip 字段")
}

// ==================== 搜索与解锁 ====================

// re0Resource RE0 资源条目（endpoints.md GET /resources/:type/:tmdb_id）
type re0Resource struct {
	Slug            string   `json:"slug"`
	Title           string   `json:"title"`
	PanType         string   `json:"pan_type"`
	ShareSize       string   `json:"share_size"`
	VideoResolution []string `json:"video_resolution"`
	Source          []string `json:"source"`
	SubtitleLang    []string `json:"subtitle_language"`
	UnlockPoints    *int     `json:"unlock_points"`
	IsUnlocked      bool     `json:"is_unlocked"`
	CreatedAt       string   `json:"created_at"`
}

// re0ListResources 按 TMDB 条目查 RE0 资源列表（带解锁积分 / 是否已解锁）
func re0ListResources(h *Handler, mediaType string, tmdbID int) ([]re0Resource, error) {
	cfg := loadRe0Cfg()
	var resources []re0Resource
	err := re0Call(h, cfg, http.MethodGet, "/api/open/resources/"+mediaType+"/"+strconv.Itoa(tmdbID), nil, nil, &resources)
	return resources, err
}

// re0UnlockSlug 解锁一条资源，拿到分享链接与访问码。已解锁过的再调一次不扣积分（already_owned）
func re0UnlockSlug(h *Handler, slug string) (link, code string, err error) {
	cfg := loadRe0Cfg()
	var data struct {
		URL          string `json:"url"`
		AccessCode   string `json:"access_code"`
		FullURL      string `json:"full_url"`
		AlreadyOwned bool   `json:"already_owned"`
	}
	if err := re0Call(h, cfg, http.MethodPost, "/api/open/resources/unlock", nil,
		map[string]string{"slug": slug}, &data); err != nil {
		return "", "", err
	}
	link = data.FullURL
	if link == "" {
		link = data.URL
	}
	log.Printf("[RE0] ✦ 解锁: %s → %s（已拥有 %v）", truncateStr(slug, 16), truncateStr(link, 60), data.AlreadyOwned)
	return link, data.AccessCode, nil
}

// ==================== 文件列表预览（不解锁、不花积分） ====================
//
// GET /api/open/resources/file-list/:slug（2026-08-19 起）：只给文件名 / 路径 / 大小，不给链接。
// 订阅拿它在付费解锁前核对分享里到底有没有缺的集（subpick.go），比只看资源标题准。
// 站方缓存 12 小时；本站再缓存一层，订阅每轮都会碰到同一批资源，没必要每轮都问

const (
	re0PreviewTTL    = 6 * time.Hour
	re0PreviewErrTTL = 30 * time.Minute
)

// re0Preview 一条资源的文件预览
type re0Preview struct {
	Title   string       // 分享标题
	Entries []shareEntry // 文件（ID 是本地编的序号，只给 pickShareEpisodes 区分用，不能拿去转存）
	Invalid string       // 站方检测资源已失效时的说明（此时 Entries 为空）
}

var re0PreviewCache = struct {
	sync.Mutex
	m map[string]re0PreviewHit
}{m: map[string]re0PreviewHit{}}

type re0PreviewHit struct {
	p   *re0Preview
	err error
	at  time.Time
}

// re0PreviewFiles 预览一条资源的文件列表（带缓存）。预览没开 / 这个网盘不支持预览时返回错误，调用方退回按标题判
func re0PreviewFiles(h *Handler, slug string) (*re0Preview, error) {
	now := time.Now()
	re0PreviewCache.Lock()
	if hit, ok := re0PreviewCache.m[slug]; ok {
		ttl := re0PreviewTTL
		if hit.err != nil {
			ttl = re0PreviewErrTTL
		}
		if now.Sub(hit.at) < ttl {
			re0PreviewCache.Unlock()
			return hit.p, hit.err
		}
	}
	re0PreviewCache.Unlock()

	var raw json.RawMessage
	err := re0Call(h, loadRe0Cfg(), http.MethodGet, "/api/open/resources/file-list/"+url.PathEscape(slug), nil, nil, &raw)
	var p *re0Preview
	if err == nil {
		p, err = parseRe0Preview(raw)
	}
	// 限流 / 授权问题不缓存：过一会儿或重新授权后就该能用
	if err == nil || !(strings.Contains(err.Error(), "限流") || errors.Is(err, errRe0Reauth)) {
		re0PreviewCache.Lock()
		for k, v := range re0PreviewCache.m {
			if now.Sub(v.at) > re0PreviewTTL {
				delete(re0PreviewCache.m, k)
			}
		}
		re0PreviewCache.m[slug] = re0PreviewHit{p: p, err: err, at: now}
		re0PreviewCache.Unlock()
	}
	return p, err
}

// parseRe0Preview 预览返回 → 分享树。文档没写死 path 是所在目录还是含文件名的全路径，两种都认；
// size 可能是字节数也可能是「1.2 GB」
func parseRe0Preview(raw []byte) (*re0Preview, error) {
	var data struct {
		ShareTitle      string `json:"share_title"`
		ResultType      string `json:"result_type"`
		ValidateStatus  any    `json:"resource_validate_status"`
		ValidateMessage string `json:"resource_validate_message"`
		Files           []struct {
			Name      string          `json:"name"`
			Path      string          `json:"path"`
			Size      json.RawMessage `json:"size"`
			Extension string          `json:"extension"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("RE0 文件预览解析失败: %v", err)
	}
	p := &re0Preview{Title: data.ShareTitle}
	for i, f := range data.Files {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			continue
		}
		if ext := strings.TrimPrefix(strings.TrimSpace(f.Extension), "."); ext != "" && !strings.Contains(name, ".") {
			name += "." + ext
		}
		dir := strings.Trim(strings.ReplaceAll(f.Path, "\\", "/"), "/")
		if dir == name {
			dir = ""
		} else if strings.HasSuffix(dir, "/"+name) {
			dir = strings.TrimSuffix(dir, "/"+name)
		}
		p.Entries = append(p.Entries, shareEntry{ID: "re0p" + strconv.Itoa(i), Name: name, Dir: dir, Size: re0SizeOf(f.Size)})
	}
	if len(p.Entries) == 0 {
		p.Invalid = firstNonEmpty(data.ValidateMessage, "预览里没有文件")
		if data.ResultType == "validation" && data.ValidateMessage == "" {
			p.Invalid = "资源检测为失效"
		}
	}
	return p, nil
}

// re0SizeOf 数字 / 数字字符串按字节，其他字符串按「1.2 GB」解析
func re0SizeOf(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int64(n)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			return v
		}
		return resParseSize(s)
	}
	return 0
}
