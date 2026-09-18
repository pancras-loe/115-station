package api

// ==================== 木咖（不太灵系影视管理系统）接入 ====================
//
// web5.mukaku.com（镜像域名 web1-web5，站点关闭某域名时可在配置换）：
// 豆瓣/IMDb 元数据库 + 每片目的种子/网盘资源（VIP 可见）。
// 前端是 Vue SPA，接口 /prod/api/v1/* 免签名，参数 app_id/identity 为
// 打包时写死的常量；access_token 同时走 query 参数与 Bearer 头。
//
//	GET  getCaptcha                     → {data:{key,img:"data:image/jpeg;base64,.."}}
//	POST login {username,password,code} → {data:{access_token}}（code=图形验证码）
//	GET  getVideoList?sb=<关键词>&page=&limit= → 搜索（匿名可用）
//	GET  getVideoDetail?id=             → 详情；资源列表（seed_name/link/code）仅 VIP 的
//	                                     响应里带，字段名未知 → 递归扫描 link 形态提取
//
// 资源 link 分类处理：115 分享→自动转存；磁力/ed2k→离线下载；其他网盘→打开。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	mukakuDefaultBase = "https://web5.mukaku.com"
	mukakuAppID       = "83768d9ad4"
	mukakuIdentity    = "23734adac0301bccdcb107c4aa21f96c"
	mukakuPageSize    = 20
)

// mukakuCfg 配置（setting key "mukaku"）
type mukakuCfg struct {
	BaseURL     string `json:"base_url"`     // 镜像域名
	AccessToken string `json:"access_token"` // 浏览器登录后粘贴，或验证码登录取得
	Username    string `json:"username"`     // 验证码登录用（可选记住）
	Password    string `json:"password"`
	AppID       string `json:"app_id"` // 站点换构建后可覆盖
	Identity    string `json:"identity"`
	TokenAt     string `json:"token_at"` // token 更新时间（展示）
}

var (
	mukakuCfgMu sync.Mutex
	mukakuCfgV  *mukakuCfg
	mukakuCfgAt time.Time
)

func loadMukakuCfg() *mukakuCfg {
	mukakuCfgMu.Lock()
	defer mukakuCfgMu.Unlock()
	if mukakuCfgV != nil && time.Since(mukakuCfgAt) < 10*time.Second {
		return mukakuCfgV
	}
	cfg := &mukakuCfg{BaseURL: mukakuDefaultBase}
	if v := settingValueCompat("mukaku"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = mukakuDefaultBase
	}
	if cfg.AppID == "" {
		cfg.AppID = mukakuAppID
	}
	if cfg.Identity == "" {
		cfg.Identity = mukakuIdentity
	}
	mukakuCfgV = cfg
	mukakuCfgAt = time.Now()
	return cfg
}

func saveMukakuCfg(cfg *mukakuCfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("mukaku", string(b)); err != nil {
		return err
	}
	mukakuCfgMu.Lock()
	mukakuCfgV = nil
	mukakuCfgMu.Unlock()
	return nil
}

// mukakuHTTP 包级复用客户端（连接池共享）
var mukakuHTTP = &http.Client{Timeout: 25 * time.Second}

// mukakuAPI 带 app_id/identity/access_token 的站点 API 请求
func mukakuAPI(cfg *mukakuCfg, method, path string, params url.Values, body any) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("app_id", cfg.AppID)
	params.Set("identity", cfg.Identity)
	full := cfg.BaseURL + "/prod/api/v1/" + path + "?" + params.Encode()

	var rd io.Reader
	var reqBody []byte
	if body != nil {
		reqBody, _ = json.Marshal(body)
		rd = strings.NewReader(string(reqBody))
	}
	req, err := http.NewRequest(method, full, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	if body != nil {
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	}
	if cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	}
	resp, err := mukakuHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("站点返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(raw), 100))
	}
	var env struct {
		Code    int             `json:"code"`
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil, fmt.Errorf("响应解析失败: %s", truncateStr(string(raw), 100))
	}
	if env.Code != 200 || !env.Success {
		return nil, fmt.Errorf("%s", strings.TrimSpace(env.Message))
	}
	return env.Data, nil
}

// MukakuGetConfig GET /mukaku/config
func (h *Handler) MukakuGetConfig(c *gin.Context) {
	cfg := loadMukakuCfg()
	c.JSON(http.StatusOK, gin.H{
		"base_url":  cfg.BaseURL,
		"username":  cfg.Username,
		"has_token": cfg.AccessToken != "",
		"token_at":  cfg.TokenAt,
	})
}

// MukakuSaveConfig POST /mukaku/config {base_url, access_token?, username?, password?}
// access_token 传空串表示清除；不传（null）保持不变
func (h *Handler) MukakuSaveConfig(c *gin.Context) {
	var req struct {
		BaseURL     string  `json:"base_url"`
		AccessToken *string `json:"access_token"`
		Username    *string `json:"username"`
		Password    *string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadMukakuCfg()
	base := strings.TrimSpace(req.BaseURL)
	if base != "" {
		if !strings.Contains(base, "://") {
			base = "https://" + base
		}
		newBase := strings.TrimRight(base, "/")
		if newBase != cfg.BaseURL && cfg.AccessToken != "" {
			// 站点地址变更：旧站 token 作废（web1-web5 镜像同源，但换了部署就不可信）
			cfg.AccessToken = ""
			cfg.TokenAt = ""
		}
		cfg.BaseURL = newBase
	}
	if req.AccessToken != nil {
		cfg.AccessToken = strings.TrimSpace(*req.AccessToken)
		cfg.TokenAt = time.Now().Format("2006-01-02 15:04")
	}
	if req.Username != nil {
		cfg.Username = strings.TrimSpace(*req.Username)
	}
	if req.Password != nil {
		cfg.Password = *req.Password
	}
	if err := saveMukakuCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[不太灵影视] ✓ 配置已保存（token %v）", cfg.AccessToken != "")
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// MukakuCaptcha GET /mukaku/captcha → {key, img}（img 为 data:image base64）
func (h *Handler) MukakuCaptcha(c *gin.Context) {
	cfg := loadMukakuCfg()
	data, err := mukakuAPI(cfg, http.MethodGet, "getCaptcha", nil, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取验证码失败: " + err.Error()})
		return
	}
	var out struct {
		Key string `json:"key"`
		Img string `json:"img"`
	}
	if json.Unmarshal(data, &out) != nil || out.Img == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "验证码响应异常"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": out.Key, "img": out.Img})
}

// MukakuLogin POST /mukaku/login {username, password, code}
// code=图形验证码；成功后 access_token 持久化（后续搜索资源用）
func (h *Handler) MukakuLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
		Key      string `json:"key"` // getCaptcha 返回的验证码会话 key
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写用户名和密码"})
		return
	}
	if req.Key == "" || req.Code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先获取并输入图形验证码"})
		return
	}
	cfg := loadMukakuCfg()
	if cfg.Username == "" || cfg.Username != req.Username {
		cfg.Username = req.Username
	}
	cfg.Password = req.Password
	data, err := mukakuAPI(cfg, http.MethodPost, "login", nil, map[string]string{
		"username": req.Username,
		"password": req.Password,
		"code":     req.Code,
		"key":      req.Key,
	})
	if err != nil {
		saveMukakuCfg(cfg)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录失败: " + err.Error()})
		return
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &out) != nil || out.AccessToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "登录响应未包含 token"})
		return
	}
	cfg.AccessToken = out.AccessToken
	cfg.TokenAt = time.Now().Format("2006-01-02 15:04")
	if err := saveMukakuCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[不太灵影视] ✓ 账号 %s 登录成功", req.Username)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

// MukakuSearch GET /mukaku/search?kw=
// 站内标题搜索（匿名可用）。TMDB 选片后用标准标题作为关键词。
func (h *Handler) MukakuSearch(c *gin.Context) {
	kw := strings.TrimSpace(c.Query("kw"))
	if kw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入搜索关键词"})
		return
	}
	cfg := loadMukakuCfg()
	params := url.Values{"sb": {kw}, "page": {"1"}, "limit": {fmt.Sprint(mukakuPageSize)}}
	data, err := mukakuAPI(cfg, http.MethodGet, "getVideoList", params, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var list struct {
		Data []mukakuVideo `json:"data"`
	}
	if json.Unmarshal(data, &list) != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "搜索响应解析失败"})
		return
	}
	items := make([]gin.H, 0, len(list.Data))
	for _, v := range list.Data {
		items = append(items, gin.H{
			"id":      v.ID,
			"title":   v.Title,
			"otitle":  v.Otitle,
			"year":    v.Years,
			"quality": v.Quality,
			"doub":    v.DoubScore,
			"imdb":    v.IMDBScore,
			"image":   v.Image,
		})
	}
	log.Printf("[不太灵影视] ✓ 搜索「%s」：%d 条", kw, len(items))
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// mukakuVideo 搜索结果条目
type mukakuVideo struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Otitle    string `json:"otitle"`
	Years     string `json:"years"`
	Quality   string `json:"quality"`
	DoubScore string `json:"doub_score"`
	IMDBScore string `json:"IMDB_score"`
	Image     string `json:"image"`
}

// MukakuResources GET /mukaku/resources?id=
// 影片资源列表（seed_name/link/code）。资源仅 VIP 登录态可见；响应里
// 资源字段名未公开，做形态无关扫描：递归找带 link 的对象数组。
func (h *Handler) MukakuResources(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 id"})
		return
	}
	cfg := loadMukakuCfg()
	if cfg.AccessToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "资源仅对 VIP 可见：请先在配置里粘贴 access_token 或用验证码登录"})
		return
	}
	params := url.Values{"id": {id}}
	data, err := mukakuAPI(cfg, http.MethodGet, "getVideoDetail", params, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	resources, raw := mukakuScanResources(data)
	log.Printf("[不太灵影视] ✓ 影片 %s 资源：%d 条", id, len(resources))
	c.JSON(http.StatusOK, gin.H{"data": resources, "raw_found": raw})
}

// mukakuScanResources 递归扫描详情响应，收集带 link 字段的对象
// （磁力/网盘分享，含 seed_name/code 描述），形态无关
func mukakuScanResources(data json.RawMessage) ([]gin.H, bool) {
	var root any
	if json.Unmarshal(data, &root) != nil {
		return nil, false
	}
	var out []gin.H
	seen := map[string]bool{}
	// 深度上限 20：响应有 8MB LimitReader 兜底，但深嵌套 JSON 仍可能打爆
	// 递归栈，限定层数防御
	var walk func(node any, depth int)
	walk = func(node any, depth int) {
		if depth > 20 {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			link, _ := v["link"].(string)
			if link != "" && !seen[link] {
				seen[link] = true
				name, _ := v["seed_name"].(string)
				code := ""
				if c, ok := v["code"].(string); ok && c != "null" {
					code = c
				}
				out = append(out, gin.H{
					"seed_name": name,
					"link":      link,
					"code":      code,
					"action":    pansouActionFor(link),
				})
			}
			for _, child := range v {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		}
	}
	walk(root, 0)
	return out, len(out) > 0
}
