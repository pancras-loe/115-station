package api

// ==================== 观影（GuanYing）资源站接入 ====================
//
// 观影是服务端渲染的网盘资源索引站，全站两层门槛：
//  1. PoW 反爬：未携带通行 Cookie 的请求会拿到「Loading...」中间页，
//     需 GET /res/pow 取挑战 {N,x,t}，计算 y = x^(2^t) mod N（t 次平方），
//     POST 回 /res/pow 换取放行 Cookie（RSW 工作量证明，Go big.Int 可解）
//  2. 站内登录：搜索/详情仅登录用户可见，表单 POST /user/login
//     （username/password/cookietime；账号输错过密码 / 风控时站点要点选验证码，
//     响应带 captcha，界面弹图让用户点一次，见下面「点选验证码」）
//
// 集成方式：账号密码登录（Cookie 持久化，站点会话过期自动重登）→
// 搜索页/详情页 HTML 里按网盘域名提取链接 → 115 链接一键转存入库。
// 站点常换域名（gying.in 已失效），base_url 可配置。

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// errGyCaptcha 站点要求点选验证码才能登录（同一账号输错过密码 / 风控时会要）。
// 这种验证码要人看图点字，后台自动重登过不了，只能由用户在设置页点一次
type errGyCaptcha struct{ msg string }

func (e *errGyCaptcha) Error() string { return e.msg }

// gyNeedCaptcha 登录响应里的 captcha 字段：站点前端 `if(r.captcha)` 就弹验证码，
// 值是数字还是字符串没定，按 JS 的真值认
func gyNeedCaptcha(raw json.RawMessage, msg string) bool {
	v := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if v != "" && v != "0" && v != "false" && v != "null" {
		return true
	}
	return strings.Contains(msg, "验证码")
}

// gyLoginOnce 账号密码登录。
// 真实请求格式（从站点前端 loginm.post 逆向）：表单
// code=<验证码>&siteid=1&dosubmit=1&username=&password=&cookietime=10506240，
// 响应为 JSON {code, msg, captcha?}（非 200=失败，msg 为站点原文，如「账号不存在」）。
// code 是点选验证码的结果串（gyCaptchaCheck 核过的那一串），不要求验证码时为空；
// 响应带 captcha 时返回 *errGyCaptcha，交给界面弹验证码
func gyLoginOnce(client *http.Client, base, username, password, code string) error {
	form := url.Values{
		"code":       {code},
		"siteid":     {"1"},
		"dosubmit":   {"1"},
		"username":   {username},
		"password":   {password},
		"cookietime": {"10506240"},
	}
	hdrs := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	type loginResp struct {
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		Captcha json.RawMessage `json:"captcha"`
	}
	judge := func(jr loginResp) error {
		if jr.Code == 200 || jr.Code == 0 {
			return nil
		}
		if gyNeedCaptcha(jr.Captcha, jr.Msg) {
			return &errGyCaptcha{msg: jr.Msg}
		}
		return fmt.Errorf("%s", jr.Msg)
	}
	body, err := gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
	if err != nil {
		return err
	}
	var jr loginResp
	if json.Unmarshal([]byte(body), &jr) == nil && jr.Msg != "" {
		err := judge(jr)
		// 浏览器验证过期类报错 → 重过一次 PoW 再试登录（验证码那种不算，重试也是同一个结果）
		var ce *errGyCaptcha
		if err != nil && !errors.As(err, &ce) && (strings.Contains(jr.Msg, "验证") || strings.Contains(body, "powSolve")) {
			if gyPow(client, base) == nil {
				body, err2 := gyDoReq(client, base, http.MethodPost, "/user/login", form, hdrs)
				if err2 != nil {
					return err2
				}
				var jr2 loginResp
				if json.Unmarshal([]byte(body), &jr2) == nil && jr2.Msg != "" {
					return judge(jr2)
				}
			}
		}
		return err
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

// ==================== 点选验证码 ====================
//
// 站点前端 captcha-*.js：POST /res/captcha/2 {webp} 取图 {type, img(base64), text}，
// 用户在 350×200 的画布上依次点 text 里的字，结果串「x1,y1-x2,y2-…;宽;高」先
// POST /res/captcha/2 {do:check, info} 核对（code=200 才算过），再作为登录表单的 code 提交。
// 验证码绑定在会话 Cookie 上，所以取图、核对、登录必须用同一个 Jar：
// 弹验证码那次登录的 Jar 暂存在这里，用户点完再接着用

const (
	gyCaptchaPath = "/res/captcha/2"
	gyCaptchaW    = 350 // 站点 PC 端画布尺寸，坐标按它换算
	gyCaptchaH    = 200
)

var (
	gyPendMu  sync.Mutex
	gyPendJar *gyJar
	gyPendAt  time.Time
)

func gyPendSet(jar *gyJar) {
	gyPendMu.Lock()
	gyPendJar, gyPendAt = jar, time.Now()
	gyPendMu.Unlock()
}

// gyPendGet 取暂存的会话；超过 10 分钟的作废（站点那边的验证码早过期了）
func gyPendGet() *gyJar {
	gyPendMu.Lock()
	defer gyPendMu.Unlock()
	if gyPendJar == nil || time.Since(gyPendAt) > 10*time.Minute {
		return nil
	}
	return gyPendJar
}

// gyCaptchaImg 一张验证码：Img 是 data URL，Text 是要依次点的字
type gyCaptchaImg struct {
	Img  string `json:"img"`
	Text string `json:"text"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

func gyCaptchaFetch(client *http.Client, base string) (*gyCaptchaImg, error) {
	hdrs := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	body, err := gyDoReq(client, base, http.MethodPost, gyCaptchaPath, url.Values{"webp": {""}}, hdrs)
	if err != nil {
		return nil, err
	}
	var r struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Img  string `json:"img"`
	}
	if json.Unmarshal([]byte(body), &r) != nil || r.Img == "" || r.Text == "" {
		// 前端对取图失败的提示就是这句
		return nil, fmt.Errorf("取验证码失败（请求太频繁，请稍后再试）: %s", truncateStr(body, 100))
	}
	if r.Type == "" {
		r.Type = "jpeg"
	}
	return &gyCaptchaImg{Img: "data:image/" + r.Type + ";base64," + r.Img, Text: r.Text, W: gyCaptchaW, H: gyCaptchaH}, nil
}

// gyCaptchaInfo 把界面点的坐标（已换算到 350×200）拼成站点要的结果串
func gyCaptchaInfo(points [][2]int) string {
	xy := make([]string, len(points))
	for i, p := range points {
		xy[i] = strconv.Itoa(p[0]) + "," + strconv.Itoa(p[1])
	}
	return strings.Join(xy, "-") + ";" + strconv.Itoa(gyCaptchaW) + ";" + strconv.Itoa(gyCaptchaH)
}

func gyCaptchaCheck(client *http.Client, base, info string) error {
	hdrs := map[string]string{"X-Requested-With": "XMLHttpRequest"}
	body, err := gyDoReq(client, base, http.MethodPost, gyCaptchaPath, url.Values{"do": {"check"}, "info": {info}}, hdrs)
	if err != nil {
		return err
	}
	var r struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(body), &r) != nil || r.Code != 200 {
		return &errGyCaptcha{msg: "未点中正确区域，请重试"}
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

// GyLogin POST /guanying/login {base_url?, username, password, captcha?}
// 保存凭据 → 过 PoW → 表单登录 → 会话 Cookie 持久化。
// 站点要验证码时回 409 {need_captcha, captcha:{img,text,w,h}}，界面让用户点完，
// 带 captcha（按 350×200 换算好的坐标）再调一次，沿用弹验证码那次的会话
func (h *Handler) GyLogin(c *gin.Context) {
	var req struct {
		BaseURL  string   `json:"base_url"`
		Username string   `json:"username"`
		Password string   `json:"password"`
		Captcha  [][2]int `json:"captcha"`
	}
	_ = c.ShouldBindJSON(&req)
	// 请求里没带的一律用已保存的：前端「登录」按钮先保存配置再调这里，
	// 此前它发的是空请求体，这里又要求账号密码必填，于是永远回「请填写账号和密码」
	cfg := loadGyCfg()
	if strings.TrimSpace(req.BaseURL) != "" {
		cfg.BaseURL = gyNormalizeBase(req.BaseURL)
	}
	if u := strings.TrimSpace(req.Username); u != "" {
		cfg.Username = u
	}
	if req.Password != "" && req.Password != settingMask { // 掩码 = 配置页没改动，用已存密码
		cfg.Password = req.Password
	}
	if cfg.Username == "" || cfg.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写账号和密码"})
		return
	}

	jar := &gyJar{m: map[string]string{}}
	code := ""
	if len(req.Captcha) > 0 {
		if jar = gyPendGet(); jar == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "验证码已过期，请重新点登录"})
			return
		}
		code = gyCaptchaInfo(req.Captcha)
	}
	client := gyClient(jar)
	err := error(nil)
	if code != "" {
		err = gyCaptchaCheck(client, cfg.BaseURL, code)
	}
	if err == nil {
		err = gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password, code)
	}
	var ce *errGyCaptcha
	if errors.As(err, &ce) {
		img, ierr := gyCaptchaFetch(client, cfg.BaseURL)
		if ierr != nil {
			log.Printf("[观影] ✗ 登录要验证码，但%v", ierr)
			c.JSON(http.StatusBadGateway, gin.H{"error": ierr.Error()})
			return
		}
		gyPendSet(jar)
		log.Printf("[观影] ○ 登录要验证码（%s），等用户点选", ce.msg)
		c.JSON(http.StatusConflict, gin.H{"error": ce.msg, "need_captcha": true, "captcha": img})
		return
	}
	if err != nil {
		log.Printf("[观影] ✗ 登录失败: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	gyPendSet(nil)
	cfg.Cookies = jar.m
	if err := saveGyCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存会话失败: " + err.Error()})
		return
	}
	log.Printf("[观影] ✓ 登录成功（%s）", cfg.Username)
	c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
}

// GyCaptcha POST /guanying/captcha：验证码框里「换一张」，沿用弹验证码那次的会话取新图
// （重新点登录也能换，但那会多算一次登录失败）
func (h *Handler) GyCaptcha(c *gin.Context) {
	jar := gyPendGet()
	if jar == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码已过期，请重新点登录"})
		return
	}
	img, err := gyCaptchaFetch(gyClient(jar), loadGyCfg().BaseURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	gyPendSet(jar)
	c.JSON(http.StatusOK, img)
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
	if err := gyLoginOnce(client, cfg.BaseURL, cfg.Username, cfg.Password, ""); err != nil {
		var ce *errGyCaptcha
		if errors.As(err, &ce) {
			return fmt.Errorf("观影要求验证码（%s），请到「发现与订阅 → 设置 → 观影」手动登录一次", ce.msg)
		}
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

// ==================== 按影片查资源 ====================
//
// 种子搜索（type=4）是按关键词搜全站种子名：「希望」会把《马萨卡的孩子：舞出希望》
// 的种子一起搜出来，和选定的 TMDB 条目对不上。站点本身是「先搜影片、再看这部片的资源」，
// 所以改成同样两步：type=0 搜影片（条目带类型与年份）→ 挑和 TMDB 条目对得上的那一部 →
// /res/downurl/{类型}/{id} 读这部片的磁力与网盘清单（JSON，一次请求）。
// 做法参考 fish2018/pansou 的 plugin/gying（只读思路）

// gyTitle 站内一部影片（搜索页 _obj.search.l 的一行）
type gyTitle struct {
	D     string // mv 电影 / tv 剧集 / ac 动漫
	ID    string
	Title string // 站内片名，常是「中文名 原名」
	Year  string
}

// gyDetail /res/downurl 的返回；各字段都是并行数组
type gyDetail struct {
	Code     int `json:"code"`
	Downlist struct {
		IMDB string `json:"imdb"`
		List struct {
			M []any `json:"m"` // 磁力 hash
			T []any `json:"t"` // 资源名
			S []any `json:"s"` // 大小
			N []any `json:"n"` // 更新时间（「昨天」「3天前」这类相对时间）
		} `json:"list"`
	} `json:"downlist"`
	Panlist struct {
		Name  []any `json:"name"`
		P     []any `json:"p"`   // 提取码
		URL   []any `json:"url"` // 链接
		Time  []any `json:"time"`
		TName []any `json:"tname"` // 网盘类型名
	} `json:"panlist"`
}

// gyGet 带 PoW 与会话失效重登一次的 GET
func gyGet(path string) (string, error) {
	cfg := loadGyCfg()
	client := gyEnsureClient(cfg)
	body, err := gyDo(client, cfg.BaseURL, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("连接观影失败: %w（站点换域名时请更新站点地址）", err)
	}
	if !gyIsNoLogin(body) && !gyCode403(body) {
		return body, nil
	}
	if err := gyAutoLogin(cfg); err != nil {
		return "", fmt.Errorf("观影需要登录（站点设置里填写账号密码，或登录已失效）: %w", err)
	}
	client = gyEnsureClient(cfg)
	body, err = gyDo(client, cfg.BaseURL, http.MethodGet, path, nil)
	if err != nil {
		return "", fmt.Errorf("重登后连接观影失败: %w", err)
	}
	if gyIsNoLogin(body) || gyCode403(body) {
		return "", fmt.Errorf("重登后仍未通过（账号可能被封或需要验证码），请检查账号")
	}
	return body, nil
}

// gyCode403 详情接口未登录时回 JSON {code:403}
func gyCode403(body string) bool {
	b := strings.TrimSpace(body)
	if !strings.HasPrefix(b, "{") {
		return false
	}
	var r struct {
		Code int `json:"code"`
	}
	return json.Unmarshal([]byte(b), &r) == nil && r.Code == 403
}

// gyParseTitles 解析影片搜索页内嵌的 _obj.search
func gyParseTitles(body string) []gyTitle {
	objStr, ok := gyObjJSON(body, "search")
	if !ok {
		return nil
	}
	var obj struct {
		L struct {
			D     []any `json:"d"`
			I     []any `json:"i"`
			Title []any `json:"title"`
			Year  []any `json:"year"`
		} `json:"l"`
	}
	if json.Unmarshal([]byte(objStr), &obj) != nil {
		return nil
	}
	l := obj.L
	var out []gyTitle
	for i := range l.I {
		t := gyTitle{ID: gyAnyStr(l.I[i]), D: gyAt(l.D, i), Title: strings.TrimSpace(gyAt(l.Title, i)), Year: gyAt(l.Year, i)}
		if t.Year == "0" {
			t.Year = ""
		}
		if t.ID == "" || t.Title == "" || t.D == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func gyAt(a []any, i int) string {
	if i < len(a) {
		return gyAnyStr(a[i])
	}
	return ""
}

// gySearchTitles 搜站内影片（type=0 全部影视，mode=2 适中）
func gySearchTitles(query string) ([]gyTitle, error) {
	body, err := gyGet("/search?q=" + url.QueryEscape(query) + "&type=0&mode=2")
	if err != nil {
		return nil, err
	}
	list := gyParseTitles(body)
	if len(list) == 0 {
		title := ""
		if m := reTitle.FindStringSubmatch(body); m != nil {
			title = gyHTMLUnescape(strings.TrimSpace(m[1]))
		}
		log.Printf("[观影] ○ 影片搜索未解析出条目（q=%s）: len=%d title=%q", query, len(body), title)
	}
	return list, nil
}

// gyFetchDetail 读一部影片的资源清单
func gyFetchDetail(t gyTitle) (*gyDetail, error) {
	body, err := gyGet("/res/downurl/" + url.PathEscape(t.D) + "/" + url.PathEscape(t.ID))
	if err != nil {
		return nil, err
	}
	var d gyDetail
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &d); err != nil {
		log.Printf("[观影] ✗ 资源清单不是 JSON（%s/%s, len=%d）: %s", t.D, t.ID, len(body), truncateStr(body, 120))
		return nil, fmt.Errorf("读取观影资源清单失败（站点可能已改版，请把这条日志发给开发者）")
	}
	return &d, nil
}

var reGyHash = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[A-Za-z2-7]{32}$`)

// gyDetailItems 资源清单 → 统一的资源条目：磁力直接拼好链接（不用再进详情页），
// 网盘链接按链接形态定类型（115 分享可直接转存）
func gyDetailItems(d *gyDetail) []ResourceItem {
	var out []ResourceItem
	l := d.Downlist.List
	for i := range l.M {
		hash := strings.TrimSpace(gyAnyStr(l.M[i]))
		if !reGyHash.MatchString(hash) {
			continue
		}
		name := strings.TrimSpace(gyAt(l.T, i))
		link := "magnet:?xt=urn:btih:" + strings.ToLower(hash)
		if name != "" {
			link += "&dn=" + url.QueryEscape(name)
		}
		tm := strings.TrimSpace(gyAt(l.N, i))
		out = append(out, ResourceItem{
			Source: "gy", Kind: "magnet", Action: "offline", Title: firstNonEmptyStr(name, hash), URL: link,
			Size: strings.TrimSpace(gyAt(l.S, i)), Time: tm, TimeUnix: resParseTime(tm),
		})
	}
	p := d.Panlist
	for i := range p.URL {
		raw := strings.TrimSpace(gyAt(p.URL, i))
		if raw == "" {
			continue
		}
		code := strings.TrimSpace(gyAt(p.P, i))
		ri := ResourceItem{Source: "gy", Title: firstNonEmptyStr(strings.TrimSpace(gyAt(p.Name, i)), raw), URL: raw, Code: code}
		ri.Kind, ri.Action, ri.Pan = resKindOf(raw, "")
		if tm := strings.TrimSpace(gyAt(p.Time, i)); tm != "" {
			ri.Time, ri.TimeUnix = tm, resParseTime(tm)
		}
		out = append(out, ri)
	}
	return out
}

// gyPickTitle 挑和 TMDB 条目对得上的那一部：类型要对（电影只认 mv，剧集认 tv / ac），
// 片名相等优先于包含（站内片名常是「中文名 原名」，按空格拆开逐段也比一次），
// 两边都有年份时差一年以上的不要（豆瓣按首映、TMDB 按上映，常差一年）。
// 一部都对不上就不挑，免得把别的片的资源列出来
func gyPickTitle(q resQuery, list []gyTitle) *gyTitle {
	best, bestScore := -1, 0
	qy, _ := strconv.Atoi(q.Year)
	for i, t := range list {
		switch q.Type {
		case "movie":
			if t.D != "mv" {
				continue
			}
		case "tv":
			if t.D != "tv" && t.D != "ac" {
				continue
			}
		}
		names := append([]string{t.Title}, strings.Fields(t.Title)...)
		lvl := titleNone
		for _, n := range q.allNames() {
			nk := titleKey(n)
			if nk == "" {
				continue
			}
			if l := titleLevel(nk, names...); l > lvl {
				lvl = l
			}
		}
		if lvl == titleNone {
			continue
		}
		score := lvl * 10
		if ty, _ := strconv.Atoi(t.Year); qy > 0 && ty > 0 {
			switch diff := ty - qy; {
			case diff == 0:
				score += 5
			case diff == 1 || diff == -1:
				score += 2
			default:
				continue
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return nil
	}
	return &list[best]
}
