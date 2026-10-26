package api

// ==================== PT 站签到：请求与各站流程 ====================
//
// 通用流程与各站专用流程读自 MoviePilot 官方插件「站点自动签到」
// plugins.v3/autosignin/__init__.py（__signin_base / __login_base）与 sites/*.py，Go 重写。
// 专用流程一律用站点自己配置的地址拼路径，不写死域名：站点换域名时改地址就行。
//
// **都没用真账号跑过**（我们没有这些站的账号），第一次跑看日志 [PT签到]。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"

	"115-station/internal/model"
)

// 内置 UA：站点没设 UA 时用。cf_clearance 绑定 UA，开了 Cloudflare 的站要填自己浏览器的
const ptDefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

const (
	ptTimeout = 30 * time.Second
	ptMaxBody = 4 << 20
)

// 结果
const (
	ptResOK    = "ok"    // 签到成功（含今天已经签过）
	ptResLogin = "login" // 只确认登录：保号有效，签到没确认
	ptResFail  = "fail"
)

// 失败原因分类
const (
	ptWhyCookie  = "cookie"  // Cookie 失效
	ptWhyToken   = "token"   // API Token 失效
	ptWhyCF      = "cf"      // 被 Cloudflare / DDoS-Guard 拦下
	ptWhyNet     = "net"     // 连不上 / 超时
	ptWhyHTTP    = "http"    // 状态码不对
	ptWhyUnknown = "unknown" // 页面认不出
	ptWhyConfig  = "config"  // 没填凭据之类
)

// ptResult 一个站一次的结果
type ptResult struct {
	Result  string `json:"result"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

func ptOK(msg string) ptResult    { return ptResult{Result: ptResOK, Message: msg} }
func ptLogin(msg string) ptResult { return ptResult{Result: ptResLogin, Message: msg} }
func ptFail(why, msg string) ptResult {
	return ptResult{Result: ptResFail, Reason: why, Message: msg}
}

// done 今天这个站算做完了（签到成功或至少确认了登录）
func (r ptResult) done() bool { return r.Result == ptResOK || r.Result == ptResLogin }

// retryable 只有网络错误与站点 5xx 值得过一会儿再试；Cookie 失效、被拦下再试也一样
func (r ptResult) retryable() bool {
	return r.Result == ptResFail && (r.Reason == ptWhyNet || (r.Reason == ptWhyHTTP && strings.Contains(r.Message, "HTTP 5")))
}

// ptClient 一个站的请求出口
type ptClient struct {
	site *model.PtSite
	c    *http.Client
}

// ptHTTPClient 可替换：测试里换成指向 httptest 的客户端
var ptHTTPClient = func(proxy string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if proxy != "" {
		if p, err := parseProxyURL(proxy); err == nil {
			tr.Proxy = p
		}
	}
	return &http.Client{Timeout: ptTimeout, Transport: tr}
}

func newPtClient(s *model.PtSite) *ptClient {
	proxy := ""
	if s.Proxy {
		proxy = getProxyURL()
	}
	return &ptClient{site: s, c: ptHTTPClient(proxy)}
}

// ptResp 一次请求的结果（正文已转成 UTF-8）
type ptResp struct {
	status int
	body   string
	header http.Header
}

// do 发请求。Cookie 放请求头：Go 跟随同主机跳转时会带上（海胆 signin.php 302 回首页那一下 MP 丢了 Cookie，这里不会）
func (pc *ptClient) do(method, rawURL string, body io.Reader, hdr map[string]string) (*ptResp, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	ua := strings.TrimSpace(pc.site.UA)
	if ua == "" {
		ua = ptDefaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if c := strings.TrimSpace(pc.site.Cookie); c != "" {
		req.Header.Set("Cookie", c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := pc.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, ptMaxBody))
	if err != nil {
		return nil, err
	}
	// 老站有 GBK / Big5：按 Content-Type 与 meta charset 转 UTF-8
	text := string(raw)
	if r, err := charset.NewReader(bytes.NewReader(raw), res.Header.Get("Content-Type")); err == nil {
		if b, err := io.ReadAll(r); err == nil {
			text = string(b)
		}
	}
	return &ptResp{status: res.StatusCode, body: text, header: res.Header}, nil
}

func (pc *ptClient) get(u string) (*ptResp, error) { return pc.do(http.MethodGet, u, nil, nil) }

func (pc *ptClient) postForm(u string, form url.Values, referer string) (*ptResp, error) {
	hdr := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if referer != "" {
		hdr["Referer"] = referer
	}
	return pc.do(http.MethodPost, u, strings.NewReader(form.Encode()), hdr)
}

// ptNetFail 网络错误写成人话
func ptNetFail(err error) ptResult {
	var ne net.Error
	if ok := asNetErr(err, &ne); ok && ne.Timeout() {
		return ptFail(ptWhyNet, "连接超时，站点打不开或需要代理")
	}
	return ptFail(ptWhyNet, "打不开站点："+ptShortErr(err))
}

func asNetErr(err error, target *net.Error) bool {
	for e := err; e != nil; {
		if n, ok := e.(net.Error); ok {
			*target = n
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// ptShortErr 错误原文里不带完整地址（url.Error 会把整个 URL 带进来，没必要）
func ptShortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// fetchPage 取一个页面并先过一遍「打不开 / 被拦 / 状态码不对 / Cookie 失效」；
// 出问题时 bad 不为空，调用方直接返回它
func (pc *ptClient) fetchPage(r *ptResp, err error, what string) (*ptPage, *ptResult) {
	if err != nil {
		f := ptNetFail(err)
		return nil, &f
	}
	p := parsePtPage(r.body)
	if r.header.Get("Cf-Mitigated") != "" || p.challenge() {
		f := ptFail(ptWhyCF, what+"被 Cloudflare 拦下：填你复制 Cookie 那个浏览器的 UA，Cookie 里要带 cf_clearance")
		return nil, &f
	}
	if r.status != http.StatusOK {
		f := ptFail(ptWhyHTTP, fmt.Sprintf("%s返回 HTTP %d", what, r.status))
		return nil, &f
	}
	if strings.TrimSpace(r.body) == "" {
		f := ptFail(ptWhyUnknown, what+"是空的")
		return nil, &f
	}
	if p.hasPasswordInput() {
		f := ptFail(ptWhyCookie, "Cookie 已失效（页面要求输入密码），重新登录站点后再复制一次")
		return nil, &f
	}
	return p, nil
}

// ==================== 登录确认（保号 / 签到认不出时的退路） ====================

// ptCheckLogin 访问首页确认登录（Rousi 走它的接口）
func ptCheckLogin(pc *ptClient) ptResult {
	if ptHandlerOf(pc.site) == ptHandRousi {
		return ptRousiLogin(pc)
	}
	if strings.TrimSpace(pc.site.Cookie) == "" {
		return ptFail(ptWhyConfig, "没填 Cookie")
	}
	r, err := pc.get(ptRootOf(pc.site.BaseURL))
	p, bad := pc.fetchPage(r, err, "首页")
	if bad != nil {
		return *bad
	}
	if !p.loggedIn() {
		return ptFail(ptWhyUnknown, "首页没认出登录状态："+p.brief())
	}
	return ptLogin("首页确认已登录")
}

// ==================== 签到 ====================

// ptSignin 按处理器签一次，不含「认不出时退回确认登录」（在 ptSigninOne 里）
func ptSignin(pc *ptClient) ptResult {
	h := ptHandlerOf(pc.site)
	if h != ptHandRousi && strings.TrimSpace(pc.site.Cookie) == "" {
		return ptFail(ptWhyConfig, "没填 Cookie")
	}
	switch h {
	case ptHandPttime:
		return ptSignPttime(pc)
	case ptHandAgsv:
		return ptSignAgsv(pc)
	case ptHandBtschool:
		return ptSignBtschool(pc)
	case ptHandHaidan:
		return ptSignHaidan(pc)
	case ptHandHdarea:
		return ptSignHdarea(pc)
	case ptHandTTG:
		return ptSignTTG(pc)
	case ptHandRousi:
		return ptSignRousi(pc)
	case ptHandKeep, ptHandRe0:
		return ptFail(ptWhyConfig, "这个站暂不支持签到")
	}
	return ptSignNexus(pc)
}

// ptAttendanceURL 签到页地址：站点地址本身带 attendance.php 就用它
func ptAttendanceURL(base string) string {
	if strings.Contains(strings.ToLower(base), "attendance.php") {
		return base
	}
	return ptJoin(base, "attendance.php")
}

// ptSignNexus 通用：GET attendance.php，按页面判定（MP __signin_base）
func ptSignNexus(pc *ptClient) ptResult {
	r, err := pc.get(ptAttendanceURL(pc.site.BaseURL))
	p, bad := pc.fetchPage(r, err, "签到页")
	if bad != nil {
		return *bad
	}
	if !p.loggedIn() {
		return ptFail(ptWhyUnknown, "签到页没认出登录状态："+p.brief())
	}
	if ok, snip := p.signinEvidence(); ok {
		return ptOK(ptOKMsg(snip))
	}
	return ptLogin("签到页没认出签到结果，已确认登录")
}

func ptOKMsg(snip string) string {
	if snip == "" || snip == "签到成功" {
		return "签到成功"
	}
	return "签到成功：" + snip
}

// ptSignPttime PT时间：GET attendance.php 返回只有「签到成功」几个字的页面，没有导航条（MP pttime.py）
func ptSignPttime(pc *ptClient) ptResult {
	r, err := pc.get(ptAttendanceURL(pc.site.BaseURL))
	p, bad := pc.fetchPage(r, err, "签到页")
	if bad != nil {
		return *bad
	}
	if p.hasText("签到成功") {
		return ptOK("签到成功")
	}
	if ok, snip := p.signinEvidence(); ok {
		return ptOK(ptOKMsg(snip))
	}
	if p.loggedIn() {
		return ptLogin("签到页没认出签到结果，已确认登录")
	}
	return ptFail(ptWhyUnknown, "签到页认不出："+p.brief())
}

var ptAgsvDoneRe = regexp.MustCompile(`签到成功|签到已得\d+|本次签到获得\d+`)

// ptSignAgsv AGSVPT：attendance.php 是表单页，GET 不签；有「立即签到」按钮才 POST，再核结果（MP agsvpt.py）
func ptSignAgsv(pc *ptClient) ptResult {
	att := ptAttendanceURL(pc.site.BaseURL)
	r, err := pc.get(att)
	p, bad := pc.fetchPage(r, err, "签到页")
	if bad != nil {
		return *bad
	}
	done := func(p *ptPage) bool {
		ok, _ := p.signinEvidence()
		return ok || (!p.signFailed() && ptAgsvDoneRe.MatchString(p.compact()))
	}
	if !p.hasText("立即签到") {
		if done(p) {
			return ptOK("今日已签到")
		}
		if p.loggedIn() {
			return ptLogin("签到页没有「立即签到」按钮，也没认出签到结果")
		}
		return ptFail(ptWhyUnknown, "签到页认不出："+p.brief())
	}
	r, err = pc.postForm(att, url.Values{}, att)
	p2, bad := pc.fetchPage(r, err, "提交签到")
	if bad != nil {
		return *bad
	}
	if done(p2) {
		return ptOK("签到成功")
	}
	// 提交后可能 302 回签到页没带结果：再读一次签到页核对
	r, err = pc.get(att)
	if p3, bad := pc.fetchPage(r, err, "签到页"); bad == nil && !p3.hasText("立即签到") && done(p3) {
		return ptOK("签到成功")
	}
	return ptFail(ptWhyUnknown, "提交签到后没确认到签到成功")
}

// ptSignBtschool 学校：首页已经写着签过就是签过；否则访问 index.php?action=addbonus，
// 到账提示「今天签到您获得 X 点魔力值」只在这一次响应里出现（MP btschool.py）
func ptSignBtschool(pc *ptClient) ptResult {
	r, err := pc.get(ptRootOf(pc.site.BaseURL))
	p, bad := pc.fetchPage(r, err, "首页")
	if bad != nil {
		return *bad
	}
	if !p.loggedIn() {
		return ptFail(ptWhyUnknown, "首页没认出登录状态："+p.brief())
	}
	if ok, _ := p.signinEvidence(); ok {
		return ptOK("今日已签到")
	}
	r, err = pc.get(ptJoin(pc.site.BaseURL, "index.php?action=addbonus"))
	p2, bad := pc.fetchPage(r, err, "签到")
	if bad != nil {
		return *bad
	}
	if ok, snip := p2.signinEvidence(); ok && p2.loggedIn() {
		return ptOK(ptOKMsg(snip))
	}
	return ptLogin("签到后没看到到账提示，已确认登录")
}

// ptSignHaidan 海胆之家：GET signin.php（会跳回首页），再读 index.php，出现「已经打卡」按钮才算（MP haidan.py）
func ptSignHaidan(pc *ptClient) ptResult {
	r, err := pc.get(ptJoin(pc.site.BaseURL, "signin.php"))
	if _, bad := pc.fetchPage(r, err, "打卡页"); bad != nil && bad.Reason != ptWhyHTTP && bad.Reason != ptWhyUnknown {
		return *bad
	}
	r, err = pc.get(ptJoin(pc.site.BaseURL, "index.php"))
	p, bad := pc.fetchPage(r, err, "首页")
	if bad != nil {
		return *bad
	}
	if p.hasText("已经打卡") {
		return ptOK("已经打卡")
	}
	if p.loggedIn() {
		return ptLogin("打卡后首页没看到「已经打卡」，已确认登录")
	}
	return ptFail(ptWhyUnknown, "首页认不出："+p.brief())
}

// ptSignHdarea 高清视界：POST sign_in.php action=sign_in，返回一句话（MP hdarea.py）
func ptSignHdarea(pc *ptClient) ptResult {
	u := ptJoin(pc.site.BaseURL, "sign_in.php")
	r, err := pc.postForm(u, url.Values{"action": {"sign_in"}}, ptRootOf(pc.site.BaseURL))
	p, bad := pc.fetchPage(r, err, "签到接口")
	if bad != nil {
		return *bad
	}
	c := p.compact()
	switch {
	case p.hasText("此次签到您获得"):
		return ptOK(ptOKMsg(ptSnippet(c, strings.Index(c, "此次签到"), strings.Index(c, "此次签到")+len("此次签到您获得"))))
	case p.hasText("请不要重复签到"):
		return ptOK("今日已签到")
	case len(r.body) < 2000 && strings.Contains(r.body, "login.php"):
		return ptFail(ptWhyCookie, "Cookie 已失效（签到接口让去登录），重新登录站点后再复制一次")
	}
	return ptFail(ptWhyUnknown, "签到接口返回认不出："+p.brief())
}

var (
	ptTTGTsRe  = regexp.MustCompile(`signed_timestamp:\s*"(\d{10})"`)
	ptTTGTokRe = regexp.MustCompile(`signed_token:\s*"([^"]*)"`)
)

// ptTTGSigned 首页上绿色的「已签到」（MP ttg.py 的 <b style="color:green;">已签到</b>）
func ptTTGSigned(p *ptPage) bool {
	found := false
	ptWalk(p.visible, func(n *html.Node) bool {
		if n.DataAtom != atom.B {
			return false
		}
		st, _ := ptAttr(n, "style")
		if strings.Contains(strings.ReplaceAll(strings.ToLower(st), " ", ""), "color:green") && ptCompact(ptText(n)) == "已签到" {
			found = true
		}
		return found
	})
	return found
}

// ptSignTTG 听听歌：首页取页面脚本里的 signed_timestamp / signed_token，POST signed.php（MP ttg.py）
func ptSignTTG(pc *ptClient) ptResult {
	root := ptRootOf(pc.site.BaseURL)
	r, err := pc.get(root)
	p, bad := pc.fetchPage(r, err, "首页")
	if bad != nil {
		return *bad
	}
	if ptTTGSigned(p) {
		return ptOK("今日已签到")
	}
	ts, tok := ptTTGTsRe.FindStringSubmatch(r.body), ptTTGTokRe.FindStringSubmatch(r.body)
	if ts == nil || tok == nil {
		if p.loggedIn() {
			return ptLogin("首页没找到签到参数，已确认登录")
		}
		return ptFail(ptWhyUnknown, "首页没认出登录状态："+p.brief())
	}
	r, err = pc.postForm(ptJoin(root, "signed.php"), url.Values{"signed_timestamp": {ts[1]}, "signed_token": {tok[1]}}, root)
	p2, bad := pc.fetchPage(r, err, "签到接口")
	if bad != nil {
		return *bad
	}
	switch {
	case p2.hasText("您已连续签到"):
		return ptOK("签到成功：" + ptSnippet(p2.compact(), strings.Index(p2.compact(), "您已连续签到"), strings.Index(p2.compact(), "您已连续签到")))
	case p2.hasText("您今天已签到过"):
		return ptOK("今日已签到")
	}
	return ptFail(ptWhyUnknown, "签到接口返回认不出："+p2.brief())
}

// ---- Rousi Pro：JSON 接口 + API Token（MP rousipro.py） ----

type ptRousiResp struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
}

func (pc *ptClient) rousi(method, path string, body any, auth map[string]string) (int, ptRousiResp, error) {
	var rd io.Reader
	hdr := map[string]string{"Accept": "application/json, text/plain, */*"}
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	for k, v := range auth {
		hdr[k] = v
	}
	r, err := pc.do(method, ptJoin(pc.site.BaseURL, path), rd, hdr)
	var out ptRousiResp
	if err != nil {
		return 0, out, err
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	return r.status, out, nil
}

func ptRousiToken(pc *ptClient) string { return strings.TrimSpace(pc.site.Token) }

func ptSignRousi(pc *ptClient) ptResult {
	tok := ptRousiToken(pc)
	if tok == "" {
		return ptFail(ptWhyConfig, "没填 API Token")
	}
	st, res, err := pc.rousi(http.MethodPost, "api/points/attendance", map[string]string{"mode": "fixed"}, map[string]string{"api-token": tok})
	if err != nil {
		return ptNetFail(err)
	}
	msg := res.Message
	if msg == "" {
		msg = res.Msg
	}
	switch {
	case st == http.StatusOK && res.Code != nil && *res.Code == 0:
		return ptOK("签到成功")
	case st == http.StatusBadRequest && res.Code != nil && *res.Code == 1 && ptRousiAlready(msg):
		return ptOK("今日已签到")
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return ptFail(ptWhyToken, "API Token 无效或没有权限，去站点「控制面板 → API」重新生成")
	case st >= 500:
		return ptFail(ptWhyHTTP, fmt.Sprintf("签到接口返回 HTTP %d", st))
	}
	if msg != "" {
		return ptFail(ptWhyUnknown, "签到接口：「"+msg+"」")
	}
	return ptFail(ptWhyHTTP, fmt.Sprintf("签到接口返回 HTTP %d", st))
}

// ptRousiAlready 重复签到的提示（业务码 1 还得看文案，别把别的 400 当成已签）
func ptRousiAlready(msg string) bool {
	if msg == "" {
		return false
	}
	ok, _ := parsePtPage("<p>" + msg + "</p>").signinEvidence()
	return ok || strings.Contains(msg, "已签到") || strings.Contains(msg, "已经签到")
}

func ptRousiLogin(pc *ptClient) ptResult {
	tok := ptRousiToken(pc)
	if tok == "" {
		return ptFail(ptWhyConfig, "没填 API Token")
	}
	auth := tok
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		auth = "Bearer " + auth
	}
	st, res, err := pc.rousi(http.MethodGet, "api/v1/profile", nil, map[string]string{"Authorization": auth})
	if err != nil {
		return ptNetFail(err)
	}
	switch {
	case st == http.StatusOK && res.Code != nil && *res.Code == 0:
		return ptLogin("API Token 有效")
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return ptFail(ptWhyToken, "API Token 无效或没有权限，去站点「控制面板 → API」重新生成")
	}
	return ptFail(ptWhyHTTP, fmt.Sprintf("个人信息接口返回 HTTP %d", st))
}
