package api

// ==================== RE0（影视资料与分享社区）OpenAPI 接入 ====================
//
// 官方文档：https://re0.me/manager/api-docs（公开 ZIP：/downloads/openapi-docs/re0-openapi-docs.zip）
// 接入模型：OpenAPI 应用（X-API-Key = 应用 Secret）+ OAuth 用户授权（Bearer 用户 Token）：
//
//	1. 在 RE0「个人面板 → 我的应用」创建应用并等站方审核通过（公开且用户 6+，或长期 v 用户）
//	2. StrmHub 配置 client_id / Secret
//	3. 点「授权」跳转 re0.me/openapi/authorize → 回调 /api/re0/oauth/callback 换取用户 Token
//	4. 搜索 = TMDB 片名 → tmdb_id → GET /api/open/resources/{type}/{tmdb_id}
//	5. 解锁 = POST /api/open/resources/unlock → full_url(115 分享链接) → 现有分享转存引擎
//
// Access Token 过期自动用 Refresh Token 刷新一次并重放；Refresh 失效提示重新授权。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const re0DefaultBase = "https://re0.me"

// re0Scope 业务所需 scope：查询资源 + 解锁
const re0Scope = "query unlock"

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
	RetryAfter  int
}

func (e *re0Err) Error() string {
	msg := e.Message
	if e.Description != "" && e.Description != e.Message {
		msg += "：" + e.Description
	}
	if e.Code != "" {
		msg = fmt.Sprintf("[%s] %s", e.Code, msg)
	}
	return msg
}

// re0IsMetaPath meta 类接口（ping/quota/usage）只需应用 Secret，不要求用户 Token
func re0IsMetaPath(path string) bool {
	return strings.HasPrefix(path, "/api/open/ping") ||
		strings.HasPrefix(path, "/api/open/quota") ||
		strings.HasPrefix(path, "/api/open/usage")
}

// re0Call 调用 /api/open/* 业务接口：自动带 X-API-Key + Bearer，
// 收到 OPENAPI_REFRESH_REQUIRED / 401 时刷新 Token 重放一次
func re0Call(h *Handler, cfg *re0Cfg, method, path string, query url.Values, body any, out any) error {
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if !re0IsMetaPath(path) {
			if err := re0EnsureToken(cfg); err != nil {
				return err
			}
		}
		env, err := re0DoOnce(cfg, method, path, query, body)
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
		// Token 失效：刷新后重放一次
		if (env.Code == "OPENAPI_REFRESH_REQUIRED" || env.Code == "OPENAPI_USER_REQUIRED" || env.Code == "401") && attempt == 0 && cfg.RefreshToken != "" {
			if rerr := re0Refresh(cfg); rerr != nil {
				return rerr
			}
			continue
		}
		return &re0Err{Code: env.Code, Message: env.Message, Description: env.Description}
	}
	return fmt.Errorf("RE0 请求未通过")
}

// re0DoOnce 单次 HTTP 调用并解析 envelope（429 带回 Retry-After）
func re0DoOnce(cfg *re0Cfg, method, path string, query url.Values, body any) (*re0Envelope, error) {
	full := strings.TrimRight(cfg.BaseURL, "/") + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, full, rd)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("连接 RE0 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env re0Envelope
	if json.Unmarshal(raw, &env) != nil {
		return nil, fmt.Errorf("RE0 响应异常（HTTP %d）: %s", resp.StatusCode, truncateStr(string(raw), 120))
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
		return &re0Envelope{Success: false, Code: env.Code, Message: env.Message, Description: env.Description}, fmt.Errorf("RE0 限流中，约 %d 秒后可重试", ra)
	}
	return &env, nil
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

// re0Refresh 用 Refresh Token 换新 Access Token（官方建议：失败则重新走 OAuth 授权）
func re0Refresh(cfg *re0Cfg) error {
	if cfg.RefreshToken == "" {
		return fmt.Errorf("RE0 授权已失效，请重新授权")
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
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Success {
		return fmt.Errorf("RE0 授权已失效，请重新授权（%s %s）", out.Code, out.Message)
	}
	if out.Data.RefreshToken != "" {
		cfg.RefreshToken = out.Data.RefreshToken
	}
	cfg.AccessToken = out.Data.AccessToken
	if out.Data.ExpiresIn > 0 {
		cfg.TokenExp = time.Now().Unix() + out.Data.ExpiresIn
	}
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
// 前端传 StrmHub 公网地址 + /api/re0/oauth/callback（RE0 应用支持动态回调）
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
			[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权失败</h3><p>state 无效或已过期（10 分钟内有效），请回 StrmHub 重新点「授权」。</p></body>`))
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
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			User         struct {
				Nickname string `json:"nickname"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.Success || out.Data.AccessToken == "" {
		msg := out.Message
		if msg == "" {
			msg = truncateStr(string(raw), 150)
		}
		c.Data(http.StatusBadGateway, "text/html; charset=utf-8",
			[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✗ RE0 授权失败</h3><p>`+htmlEscape(msg)+`</p></body>`))
		return
	}
	cfg.AccessToken = out.Data.AccessToken
	if out.Data.RefreshToken != "" {
		cfg.RefreshToken = out.Data.RefreshToken
	}
	if out.Data.ExpiresIn > 0 {
		cfg.TokenExp = time.Now().Unix() + out.Data.ExpiresIn
	}
	if out.Data.User.Nickname != "" {
		cfg.AuthorizedAs = out.Data.User.Nickname
	} else if out.Data.User.Username != "" {
		cfg.AuthorizedAs = out.Data.User.Username
	}
	cfg.RedirectURI = redirectURI
	if err := saveRe0Cfg(cfg); err != nil {
		log.Printf("[RE0] ○ 授权 Token 落盘失败: %v", err)
	}
	log.Printf("[RE0] ✓ OAuth 授权完成（用户: %s）", cfg.AuthorizedAs)
	c.Data(http.StatusOK, "text/html; charset=utf-8",
		[]byte(`<!DOCTYPE html><meta charset="utf-8"><body style="font-family:system-ui;text-align:center;padding-top:60px"><h3>✓ RE0 授权成功</h3><p>已绑定用户 `+htmlEscape(cfg.AuthorizedAs)+`，请关闭此页返回 StrmHub。</p></body>`))
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

// Re0Check GET /re0/check —— 应用 Ping + 授权状态
func (h *Handler) Re0Check(c *gin.Context) {
	cfg := loadRe0Cfg()
	if cfg.ClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未配置应用 Secret"})
		return
	}
	var ping struct {
		Message string `json:"message"`
		Name    string `json:"name"`
	}
	appErr := re0Call(h, cfg, http.MethodGet, "/api/open/ping", nil, nil, &ping)
	if appErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "应用校验失败: " + appErr.Error()})
		return
	}
	out := gin.H{"app_ok": true, "app_name": ping.Name, "authorized": false}
	if cfg.AccessToken != "" {
		var me struct {
			Nickname string `json:"nickname"`
			Username string `json:"username"`
			VIP      any    `json:"vip"`
		}
		if err := re0Call(h, cfg, http.MethodGet, "/api/open/me", nil, nil, &me); err == nil {
			out["authorized"] = true
			if me.Nickname != "" {
				out["user"] = me.Nickname
			} else {
				out["user"] = me.Username
			}
		}
	}
	c.JSON(http.StatusOK, out)
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

// re0TmdbCandidates 复用 TMDB multi-search 把片名解析成候选条目
func re0TmdbCandidates(h *Handler, query string) []gin.H {
	tc, err := loadTmdbClient()
	if err != nil {
		return nil
	}
	body, err := tc.get("/search/multi", map[string]string{"query": query, "include_adult": "false"})
	if err != nil {
		return nil
	}
	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			PosterPath   string  `json:"poster_path"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &result) != nil {
		return nil
	}
	items := make([]gin.H, 0, 6)
	for _, r := range result.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue
		}
		title := r.Title
		date := r.ReleaseDate
		if title == "" {
			title = r.Name
		}
		if date == "" {
			date = r.FirstAirDate
		}
		year := ""
		if len(date) >= 4 {
			year = date[:4]
		}
		items = append(items, gin.H{
			"id": r.ID, "media_type": r.MediaType, "title": title,
			"year": year, "poster": r.PosterPath, "vote": r.VoteAverage,
		})
		if len(items) >= 5 {
			break
		}
	}
	return items
}

// Re0Search GET /re0/search?query=xxx
// 片名 → TMDB 候选 → 每个候选查 RE0 资源列表（带解锁积分/状态）
func (h *Handler) Re0Search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	cfg := loadRe0Cfg()
	if cfg.ClientSecret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置 RE0 应用（client_id / Secret）"})
		return
	}
	candidates := re0TmdbCandidates(h, q)
	if len(candidates) == 0 {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{}, "hint": "TMDB 未匹配到影视条目"})
		return
	}
	out := make([]gin.H, 0, len(candidates))
	var firstErr error
	gotAny := false
	for _, cand := range candidates {
		mediaType, _ := cand["media_type"].(string)
		id := strconv.Itoa(cand["id"].(int))
		var resources []re0Resource
		err := re0Call(h, cfg, http.MethodGet, "/api/open/resources/"+mediaType+"/"+id, nil, nil, &resources)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			resources = nil
		} else if len(resources) > 0 {
			gotAny = true
		}
		out = append(out, gin.H{
			"id": cand["id"], "media_type": mediaType, "title": cand["title"],
			"year": cand["year"], "poster": cand["poster"], "vote": cand["vote"],
			"resources": resources,
			"resources_err": func() string {
				if err != nil {
					return err.Error()
				}
				return ""
			}(),
		})
	}
	// 所有候选都没拿到资源且至少一次查询报错 → 把原因带给前端（如应用未获批）
	hint := ""
	if !gotAny && firstErr != nil {
		hint = "资源查询失败: " + firstErr.Error()
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "hint": hint})
}

// Re0Unlock POST /re0/unlock {media_type, tmdb_id, slug, transfer}
// 解锁拿 115 分享链接；transfer=true 且为 115 链接时直接进分享转存引擎
func (h *Handler) Re0Unlock(c *gin.Context) {
	var req struct {
		MediaType string `json:"media_type"`
		TmdbID    int    `json:"tmdb_id"`
		Slug      string `json:"slug"`
		Transfer  bool   `json:"transfer"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误（需要 slug）"})
		return
	}
	cfg := loadRe0Cfg()
	var data struct {
		URL          string `json:"url"`
		AccessCode   string `json:"access_code"`
		FullURL      string `json:"full_url"`
		AlreadyOwned bool   `json:"already_owned"`
	}
	if err := re0Call(h, cfg, http.MethodPost, "/api/open/resources/unlock", nil,
		map[string]string{"slug": req.Slug}, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解锁失败: " + err.Error()})
		return
	}
	link := data.FullURL
	if link == "" {
		link = data.URL
	}
	out := gin.H{
		"slug": req.Slug, "url": link, "access_code": data.AccessCode,
		"already_owned": data.AlreadyOwned, "transferred": false,
	}
	// 115 链接自动进分享转存引擎（整理收尾由转存流程自理）
	if req.Transfer && data.FullURL != "" && is115ShareLink(data.FullURL) {
		msg, ok, fail, err := h.shareReceiveCore(data.FullURL, data.AccessCode, "", true)
		if err != nil {
			out["transfer_error"] = err.Error()
		} else {
			out["transferred"] = ok > 0
			out["transfer_msg"] = fmt.Sprintf("%s（成功 %d，失败 %d）", msg, ok, fail)
		}
	}
	log.Printf("[RE0] ✦ 解锁: %s → %s", truncateStr(req.Slug, 16), truncateStr(link, 60))
	c.JSON(http.StatusOK, out)
}
