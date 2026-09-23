package api

// ==================== 观影（GuanYing）资源站接入 ====================
//
// 观影是服务端渲染的网盘资源索引站，全站两层门槛：
//  1. PoW 反爬：未携带通行 Cookie 的请求会拿到「Loading...」中间页，
//     需 GET /res/pow 取挑战 {N,x,t}，计算 y = x^(2^t) mod N（t 次平方），
//     POST 回 /res/pow 换取放行 Cookie（RSW 工作量证明，Go big.Int 可解）
//  2. 站内登录：搜索/详情仅登录用户可见，表单 POST /user/login
//     （username/password/cookietime；服务端可能校验验证码，失败时
//     请改用浏览器 Cookie 导入）
//
// 集成方式：账号密码登录（Cookie 持久化，站点会话过期自动重登）→
// 搜索页/详情页 HTML 里按网盘域名提取链接 → 115 链接一键转存入库。
// 站点常换域名（gying.in 已失效），base_url 可配置。

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const gyDefaultBase = "https://www.xn--wcv59z.com"

var gyUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// gyCfg 观影配置（setting key "guanying"）
type gyCfg struct {
	BaseURL  string            `json:"base_url"`
	Username string            `json:"username"`
	Password string            `json:"password"`
	Cookies  map[string]string `json:"cookies"` // 站点会话 + PoW 放行 Cookie
}

var (
	gyCfgMu sync.Mutex
	gyCfgV  *gyCfg
	gyCfgAt time.Time
)

// gyNormalizeBase 归一化站点地址：自动补协议头（用户填 xxx.com 也能用）
func gyNormalizeBase(raw string) string {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if b == "" {
		return gyDefaultBase
	}
	if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
		b = "https://" + b
	}
	return b
}

func loadGyCfg() *gyCfg {
	gyCfgMu.Lock()
	defer gyCfgMu.Unlock()
	if gyCfgV != nil && time.Since(gyCfgAt) < 10*time.Second {
		return gyCfgV
	}
	cfg := &gyCfg{BaseURL: gyDefaultBase}
	if v := settingValueCompat("guanying"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	cfg.BaseURL = gyNormalizeBase(cfg.BaseURL)
	if cfg.Cookies == nil {
		cfg.Cookies = map[string]string{}
	}
	gyCfgV = cfg
	gyCfgAt = time.Now()
	return cfg
}

func saveGyCfg(cfg *gyCfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("guanying", string(b)); err != nil {
		return err
	}
	gyCfgMu.Lock()
	gyCfgV = nil
	gyCfgMu.Unlock()
	return nil
}

// ==================== HTTP 客户端（Cookie Jar + PoW 自动过验证） ====================

// gyJar 极简 CookieJar：单站点，名字→值
type gyJar struct {
	mu sync.Mutex
	m  map[string]string
}

func (j *gyJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		j.m[c.Name] = c.Value
	}
}

func (j *gyJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*http.Cookie, 0, len(j.m))
	for k, v := range j.m {
		out = append(out, &http.Cookie{Name: k, Value: v})
	}
	return out
}

// gyClient 带 Jar 的 HTTP 客户端
func gyClient(jar *gyJar) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向过多")
			}
			return nil
		},
	}
}

var (
	reHTMLText = regexp.MustCompile(`<[^>]+>`)
)

// gyPanRules 网盘链接识别（域名分类）
var gyPanRules = []struct {
	pattern *regexp.Regexp
	pan     string
}{
	{regexp.MustCompile(`(?:115\.com|115cdn\.com|anxia\.com)/s/[a-zA-Z0-9_-]+`), "115"},
	{regexp.MustCompile(`pan\.baidu\.com/s/[a-zA-Z0-9_-]+`), "baidu"},
	{regexp.MustCompile(`pan\.xunlei\.com/s/[a-zA-Z0-9]+`), "xunlei"},
	{regexp.MustCompile(`drive\.uc\.cn/s/[a-zA-Z0-9]+`), "uc"},
	{regexp.MustCompile(`cloud\.189\.cn/[tw]/[a-zA-Z0-9]+`), "tianyi"},
	{regexp.MustCompile(`magnet:\?xt=urn:btih:[a-zA-Z0-9]{8,}`), "magnet"},
}

// gyDetectPan 按域名识别网盘类型，非网盘链接返回空
func gyDetectPan(s string) string {
	for _, r := range gyPanRules {
		if r.pattern.MatchString(s) {
			return r.pan
		}
	}
	return ""
}

// gyIsPow 判断响应是否为 PoW 验证中间页
func gyIsPow(body string) bool {
	return strings.Contains(body, "powSolve") || strings.Contains(body, "pow.core")
}

// gyIsNoLogin 判断页面是否提示需要登录（访客标记：nologin 脚本 / 受限标题）
func gyIsNoLogin(body string) bool {
	return strings.Contains(body, "nologin-e02c") || strings.Contains(body, "请登录后继续") ||
		strings.Contains(body, "未登录，访问受限")
}

// gyPow 过 PoW 验证：取挑战→反复平算→提交换 Cookie
func gyPow(client *http.Client, base string) error {
	// 挑战与提交的 UA 必须和后续业务请求完全一致——站点把放行凭证绑定到
	// 浏览器指纹（UA 不一致时业务请求会报「浏览器验证已过期」）
	req, err := http.NewRequest(http.MethodGet, base+"/res/pow", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", gyUA)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("获取 PoW 挑战失败: %s", sanitizeWecomErr(err))
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	var ch struct {
		N string `json:"N"`
		X string `json:"x"`
		T int    `json:"t"`
	}
	if json.Unmarshal(raw, &ch) != nil || ch.N == "" || ch.X == "" || ch.T <= 0 {
		return fmt.Errorf("PoW 挑战解析失败: %s", truncateStr(string(raw), 100))
	}
	n, ok1 := new(big.Int).SetString(ch.N, 16)
	y, ok2 := new(big.Int).SetString(ch.X, 16)
	if !ok1 || !ok2 {
		return fmt.Errorf("PoW 挑战数值非法")
	}
	// y = x^(2^t) mod N：等价于连续 t 次平方取模（RSW 谜题，无法并行加速）
	for i := 0; i < ch.T; i++ {
		y.Mul(y, y)
		y.Mod(y, n)
	}

	form := url.Values{"y": {y.Text(16)}}
	req2, err := http.NewRequest(http.MethodPost, base+"/res/pow", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req2.Header.Set("User-Agent", gyUA)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := client.Do(req2)
	if err != nil {
		return fmt.Errorf("提交 PoW 结果失败: %s", sanitizeWecomErr(err))
	}
	raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 16<<10))
	resp2.Body.Close()
	var vr struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(raw2, &vr) != nil || !vr.Success {
		return fmt.Errorf("PoW 验证被拒: %s", truncateStr(string(raw2), 100))
	}
	return nil
}

// gyDo 带自动过 PoW 的页面请求：返回响应正文（字符串）
func gyDo(client *http.Client, base, method, path string, form url.Values) (string, error) {
	return gyDoReq(client, base, method, path, form, nil)
}

// gyDoReq 同 gyDo，允许附加请求头
func gyDoReq(client *http.Client, base, method, path string, form url.Values, hdrs map[string]string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var bodyReader io.Reader
		if form != nil {
			bodyReader = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, base+path, bodyReader)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", gyUA)
		// 注意：不要加 Accept/Accept-Language 等导航形态的请求头——站点的
		// 反爬把放行凭证绑定到请求头形态，验证时是 XHR 形态，带了浏览器
		// 导航头反而会被判「浏览器验证已过期」（实测确认）
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		body := string(raw)
		if gyIsPow(body) && attempt < 2 {
			if err := gyPow(client, base); err != nil {
				return "", err
			}
			continue // 过完验证重放原请求
		}
		return body, nil
	}
	return "", fmt.Errorf("PoW 验证后请求仍未通过")
}

// gyLoggedIn 探测当前会话是否已登录。
// 访客页面的服务端标记：<title>未登录，访问受限</title> + nologin 脚本；
// （导航里的退出链接是前端 JS 渲染的，服务端 HTML 里没有，不能用作判定）
func gyLoggedIn(client *http.Client, base string) (bool, error) {
	body, err := gyDo(client, base, http.MethodGet, "/", nil)
	if err != nil {
		return false, err
	}
	return !gyIsNoLogin(body), nil
}

// gyLoginOnce 账号密码登录。
// 真实请求格式（从站点前端 loginm.post 逆向）：表单
// code=<验证码可空>&siteid=1&dosubmit=1&username=&password=&cookietime=10506240，
// 响应为 JSON {code, msg}（非 200=失败，msg 为站点原文，如「账号不存在」）
func gyLoginOnce(client *http.Client, base, username, password string) error {
	form := url.Values{
		"code":       {""},
		"siteid":     {"1"},
		"dosubmit":   {"1"},
		"username":   {username},
		"password":   {password},
		"cookietime": {"10506240"},
	}
	hdrs := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	body, err := gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
	if err != nil {
		return err
	}
	// 成功：JSON {code:200,...}；失败：code=403/其他，msg 为站点原文
	var jr struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal([]byte(body), &jr) == nil && jr.Msg != "" {
		if jr.Code == 200 || jr.Code == 0 {
			return nil
		}
		// 验证过期类报错 → 重过一次 PoW 再试登录
		if strings.Contains(jr.Msg, "验证") || strings.Contains(body, "powSolve") {
			if err := gyPow(client, base); err == nil {
				body, err = gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
				if err != nil {
					return err
				}
				if json.Unmarshal([]byte(body), &jr) == nil && jr.Msg != "" {
					if jr.Code == 200 || jr.Code == 0 {
						return nil
					}
					return fmt.Errorf("%s", jr.Msg)
				}
			}
		}
		return fmt.Errorf("%s", jr.Msg)
	}
	// 非 JSON 响应（异常情况）→ 回退会话探测
	ok, err := gyLoggedIn(client, base)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("登录未通过（站点响应异常）")
	}
	return nil
}

// gyEnsureClient 载入 Cookie 构建客户端
func gyEnsureClient(cfg *gyCfg) *http.Client {
	jar := &gyJar{m: map[string]string{}}
	for k, v := range cfg.Cookies {
		jar.m[k] = v
	}
	return gyClient(jar)
}

// ==================== 链接提取 ====================

// gyLink 一条网盘链接
type gyLink struct {
	URL  string `json:"url"`
	Code string `json:"code"`
	Pan  string `json:"pan"`
}

// gyHTMLUnescape 基本实体反转义
func gyHTMLUnescape(s string) string {
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'", "&apos;", "'")
	return r.Replace(s)
}

var (
	reScript   = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	reTitle    = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	reGyMagnet = regexp.MustCompile(`magnet:\?xt=urn:btih:[A-Za-z0-9]{16,}`)
)

// gyObjJSON 从页面内嵌脚本提取 _obj.KEY 的 JSON 串（站点是 CSR 架构，
// 所有数据以内嵌 JS 对象形式挂在 _obj.* 下；花括号配平扫描取整段）
func gyObjJSON(body, key string) (string, bool) {
	i := strings.Index(body, "_obj."+key+"=")
	if i < 0 {
		return "", false
	}
	start := strings.IndexByte(body[i:], '{')
	if start < 0 {
		return "", false
	}
	start += i
	depth, inStr, esc := 0, false, false
	for j := start; j < len(body); j++ {
		c := body[j]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : j+1], true
			}
		}
	}
	return "", false
}

// gyAnyStr JSON 值宽松转字符串（数字/字符串都可能）
func gyAnyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	default:
		return ""
	}
}

// gyExtractTorrents 解析搜索页内嵌的 _obj.search：l 为并行数组
// （d=分类 bt/mv/tv，i=ID，title=名称，size/time/seeds 种子页专有）
func gyExtractTorrents(body string) []gin.H {
	objStr, ok := gyObjJSON(body, "search")
	if !ok {
		return []gin.H{}
	}
	var obj struct {
		L struct {
			D     []any `json:"d"`
			I     []any `json:"i"`
			Title []any `json:"title"`
			Size  []any `json:"size"`
			Time  []any `json:"time"`
			Seeds []any `json:"seeds"`
		} `json:"l"`
	}
	if json.Unmarshal([]byte(objStr), &obj) != nil {
		return []gin.H{}
	}
	out := []gin.H{}
	l := obj.L
	for i, idv := range l.I {
		id := gyAnyStr(idv)
		dir := "bt"
		if i < len(l.D) {
			if d := gyAnyStr(l.D[i]); d != "" {
				dir = d
			}
		}
		title := ""
		if i < len(l.Title) {
			title = gyAnyStr(l.Title[i])
		}
		if id == "" || title == "" {
			continue
		}
		item := gin.H{"path": "/" + dir + "/" + id, "title": title}
		if i < len(l.Size) {
			if v := gyAnyStr(l.Size[i]); v != "" {
				item["size"] = v
			}
		}
		if i < len(l.Time) {
			if v := gyAnyStr(l.Time[i]); v != "" {
				item["time"] = v
			}
		}
		if i < len(l.Seeds) {
			if v := gyAnyStr(l.Seeds[i]); v != "" {
				item["seeds"] = v
			}
		}
		out = append(out, item)
		if len(out) >= 25 {
			break
		}
	}
	return out
}

// ==================== HTTP 处理器 ====================

// GyGetConfig GET /guanying/config（回填用；密码原样回传便于编辑，站点凭据非高敏）
// 登录探测的粘性缓存：配置读取即时返回上次结果，过期后台刷新。
// 此前 GyGetConfig 同步探测站点，网络慢时页面输入框迟迟不回填
var (
	gyProbeMu sync.Mutex
	gyProbeOK bool
	gyProbeAt time.Time
)

func gyProbeLoginCached(cfg *gyCfg) bool {
	gyProbeMu.Lock()
	defer gyProbeMu.Unlock()
	if time.Since(gyProbeAt) >= 5*time.Minute {
		gyProbeAt = time.Now() // 防重复后台探测
		c := *cfg
		go func() {
			client := gyEnsureClient(&c)
			ok, _ := gyLoggedIn(client, c.BaseURL)
			gyProbeMu.Lock()
			gyProbeOK, gyProbeAt = ok, time.Now()
			gyProbeMu.Unlock()
		}()
	}
	return gyProbeOK
}

func (h *Handler) GyGetConfig(c *gin.Context) {
	cfg := loadGyCfg()
	loggedIn := len(cfg.Cookies) > 0 && gyProbeLoginCached(cfg)
	pw := ""
	if cfg.Password != "" {
		pw = settingMask // 脱敏：掩码回传，保存时回填旧值
	}
	c.JSON(http.StatusOK, gin.H{
		"base_url":    cfg.BaseURL,
		"username":    cfg.Username,
		"password":    pw,
		"logged_in":   loggedIn,
		"has_cookies": len(cfg.Cookies) > 0,
	})
}

// GyCheck GET /guanying/check：实时探测登录态（前端异步调用，不阻塞配置回填）
func (h *Handler) GyCheck(c *gin.Context) {
	cfg := loadGyCfg()
	loggedIn := false
	if len(cfg.Cookies) > 0 {
		client := gyEnsureClient(cfg)
		loggedIn, _ = gyLoggedIn(client, cfg.BaseURL)
	}
	gyProbeMu.Lock()
	gyProbeOK, gyProbeAt = loggedIn, time.Now()
	gyProbeMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"logged_in": loggedIn})
}

// GySaveConfig POST /guanying/config {base_url, username, password}
// 仅保存接入信息，不触发登录（登录走 /guanying/login）
func (h *Handler) GySaveConfig(c *gin.Context) {
	var req struct {
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadGyCfg()
	oldPass := cfg.Password
	cfg.BaseURL = gyNormalizeBase(req.BaseURL)
	cfg.Username = strings.TrimSpace(req.Username)
	cfg.Password = req.Password
	if req.Password == settingMask {
		cfg.Password = oldPass // 掩码回传 = 未改动，保持旧值
	}
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 接入配置已保存（%s）", cfg.BaseURL)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// GyLogin POST /guanying/login {base_url?, username, password}
// 保存凭据 → 过 PoW → 表单登录 → 会话 Cookie 持久化
func (h *Handler) GyLogin(c *gin.Context) {
	var req struct {
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写账号和密码"})
		return
	}
	cfg := loadGyCfg()
	if b := gyNormalizeBase(req.BaseURL); b != "" {
		cfg.BaseURL = b
	}
	cfg.Username = strings.TrimSpace(req.Username)
	cfg.Password = req.Password
	if req.Password == settingMask {
		cfg.Password = loadGyCfg().Password // 配置页掩码未改动直接点登录：用已存密码
	}

	jar := &gyJar{m: map[string]string{}}
	client := gyClient(jar)
	if err := gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password); err != nil {
		log.Printf("[观影] ✗ 登录失败: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	cfg.Cookies = jar.m
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存会话失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 登录成功（%s）", cfg.Username)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

// GyLogout POST /guanying/logout → 清空会话
func (h *Handler) GyLogout(c *gin.Context) {
	cfg := loadGyCfg()
	cfg.Cookies = map[string]string{}
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ○ 已退出登录")
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

// gyAutoLogin Cookie 失效时用存储的凭据自动重登
func gyAutoLogin(cfg *gyCfg) error {
	if cfg.Username == "" || cfg.Password == "" {
		return fmt.Errorf("未保存观影账号，请先登录")
	}
	jar := &gyJar{m: map[string]string{}}
	client := gyClient(jar)
	if err := gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password); err != nil {
		return err
	}
	cfg.Cookies = jar.m
	return saveGyCfg(cfg)
}

// GySearch GET /guanying/search?query=xxx → 解析种子搜索结果（type=4）。
// 注意用裸请求（不带导航头）：站点对导航请求回完整渲染页，对 XHR/裸请求
// 回「Loading...」壳——壳里内嵌的 _obj.search 就是本页全部数据（JSON）
// gySearchTorrents 搜索观影种子（会话失效自动重登一次）。
// 返回种子列表和原始页面（调试用）
func gySearchTorrents(query, zy string) ([]gin.H, string, error) {
	cfg := loadGyCfg()
	client := gyEnsureClient(cfg)
	searchPath := "/search?q=" + url.QueryEscape(query) + "&type=4"
	if zy != "" {
		searchPath += "&ziyuan=" + url.QueryEscape(zy)
	}
	body, err := gyDo(client, cfg.BaseURL, http.MethodGet, searchPath, nil)
	if err != nil {
		return nil, "", fmt.Errorf("连接观影失败: %w（站点换域名时请更新站点地址）", err)
	}
	if gyIsNoLogin(body) {
		if err := gyAutoLogin(cfg); err != nil {
			return nil, body, fmt.Errorf("观影需要登录（站点设置里填写账号密码，或登录已失效）: %w", err)
		}
		client = gyEnsureClient(cfg)
		body, err = gyDo(client, cfg.BaseURL, http.MethodGet, searchPath, nil)
		if err != nil {
			return nil, "", fmt.Errorf("重登后连接观影失败: %w", err)
		}
		if gyIsNoLogin(body) {
			return nil, body, fmt.Errorf("重登后仍未通过（账号可能被封或需要验证码），请检查账号")
		}
	}
	return gyExtractTorrents(body), body, nil
}

// gyFetchMagnet 种子详情页提取磁力链接与标题（一次请求；会话失效自动重登一次）
func gyFetchMagnet(path string) (string, string, error) {
	cfg := loadGyCfg()
	client := gyEnsureClient(cfg)
	body, err := gyDo(client, cfg.BaseURL, http.MethodGet, path, nil)
	if err != nil {
		return "", "", fmt.Errorf("连接观影失败: %w", err)
	}
	if gyIsNoLogin(body) {
		if err := gyAutoLogin(cfg); err != nil {
			return "", "", fmt.Errorf("登录已失效: %w", err)
		}
		client = gyEnsureClient(cfg)
		body, err = gyDo(client, cfg.BaseURL, http.MethodGet, path, nil)
		if err != nil {
			return "", "", fmt.Errorf("重登后连接观影失败: %w", err)
		}
	}
	title := ""
	if objStr, ok := gyObjJSON(body, "d"); ok {
		var d struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(objStr), &d) == nil {
			title = d.Title
		}
	}
	if m := reGyMagnet.FindStringSubmatch(body); m != nil {
		return m[0], title, nil
	}
	log.Printf("[观影] ✗ 详情页未找到磁力链接（path=%s, len=%d）", path, len(body))
	return "", "", fmt.Errorf("详情页未找到磁力链接（站点可能已改版，请把这条日志发给开发者）")
}

func (h *Handler) GySearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	zy := strings.TrimSpace(c.Query("zy")) // 资源分类：空=全部，中字720P/4K/原盘…
	items, body, err := gySearchTorrents(q, zy)
	if err != nil {
		if strings.Contains(err.Error(), "观影需要登录") || strings.Contains(err.Error(), "重登后仍未通过") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	// 0 条时附带页面特征：前端显示出来，方便远程定位（空壳/受限/正常页一眼区分）
	debug := gin.H{}
	if len(items) == 0 {
		title := ""
		if m := reTitle.FindStringSubmatch(body); m != nil {
			title = gyHTMLUnescape(strings.TrimSpace(m[1]))
		}
		debug = gin.H{
			"page_len": len(body),
			"title":    title,
			"nologin":  gyIsNoLogin(body),
		}
		log.Printf("[观影] ○ 搜索未解析出条目（q=%s）: len=%d title=%q noLogin=%v", q, len(body), title, gyIsNoLogin(body))
	} else {
		log.Printf("[观影] ▣ 搜索「%s」: %d 条种子", q, len(items))
	}
	// 分类分组只在「全部」查询时返回（zy:{中字720P:3, 4K:7, ...}）
	zyGroups := gin.H{}
	if zy == "" {
		if objStr, ok := gyObjJSON(body, "search"); ok {
			var obj struct {
				Zy map[string]any `json:"zy"`
			}
			if json.Unmarshal([]byte(objStr), &obj) == nil && obj.Zy != nil {
				zyGroups = gin.H(obj.Zy)
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "zy": zyGroups, "debug": debug})
}

// GyResources GET /guanying/resources?path=/bt/XXXX → 种子详情页提取磁力链接
func (h *Handler) GyResources(c *gin.Context) {
	p := strings.TrimSpace(c.Query("path"))
	if p == "" || !strings.HasPrefix(p, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少条目路径"})
		return
	}
	magnet, title, err := gyFetchMagnet(p)
	if err != nil {
		if strings.Contains(err.Error(), "登录已失效") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		}
		return
	}
	log.Printf("[观影] ▣ 提取到磁力链接（%s）", truncateStr(title, 40))
	c.JSON(http.StatusOK, gin.H{"magnet": magnet, "title": title})
}

// GyOffline POST /guanying/offline {magnet} → 提交 115 离线下载
// （完成后由离线监视器自动整理入库）
func (h *Handler) GyOffline(c *gin.Context) {
	var req struct {
		Magnet string `json:"magnet"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !strings.HasPrefix(req.Magnet, "magnet:?") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少有效的磁力链接"})
		return
	}
	if err := h.submitOfflineLink(req.Magnet, "观影"); err != nil {
		log.Printf("[观影] ✗ 离线提交失败: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "离线提交失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 已提交 115 离线下载")
	c.JSON(http.StatusOK, gin.H{"message": "已提交 115 离线下载", "note": "下载完成后自动整理入库"})
}
