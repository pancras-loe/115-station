package api

// ==================== PT 站签到：页面判定 ====================
//
// 判定规则读自 MoviePilot 官方插件「站点自动签到」plugins.v3/autosignin/result.py
// （visible_html / has_login_form / has_login_evidence / has_signin_evidence）与
// MoviePilot 本体 app/utils/site.py 的 is_logged_in、app/adapters/network/cloudflare.py 的
// under_challenge，Go 重写。原则（MP README 原话的意思）：
// HTTP 200、签到按钮不见了、跳回签到页、脚本或隐藏模板里的文案，都不能单独证明签到或登录成功。
// 认不出就退成「只确认登录」或失败，宁可少报成功，不把失败报成成功。

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ptPage 一个页面：原文 + 去掉不可见内容后的文档
type ptPage struct {
	raw     string
	visible *html.Node // 只剩看得见的内容
	full    *html.Node // 原样解析（认 Cloudflare 挑战页要看 title 与脚本容器）
}

func parsePtPage(src string) *ptPage {
	p := &ptPage{raw: src}
	p.full, _ = html.Parse(strings.NewReader(src))
	p.visible, _ = html.Parse(strings.NewReader(src))
	if p.visible != nil {
		ptStripInvisible(p.visible)
	}
	return p
}

var ptHiddenStyleRe = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden`)

// ptInvisible 这个节点看不见：脚本、样式、模板、注释、隐藏属性、行内隐藏样式。
// 站点模板里常有预先写好、默认藏着的「签到成功」，不去掉会误判（MP visible_html）
func ptInvisible(n *html.Node) bool {
	switch n.Type {
	case html.CommentNode:
		return true
	case html.ElementNode:
	default:
		return false
	}
	switch n.DataAtom {
	case atom.Head, atom.Script, atom.Style, atom.Template, atom.Noscript, atom.Textarea:
		return true
	}
	if _, ok := ptAttr(n, "hidden"); ok {
		return true
	}
	if v, _ := ptAttr(n, "aria-hidden"); strings.EqualFold(v, "true") {
		return true
	}
	if n.DataAtom == atom.Input {
		if v, _ := ptAttr(n, "type"); strings.EqualFold(v, "hidden") {
			return true
		}
	}
	if v, ok := ptAttr(n, "style"); ok && ptHiddenStyleRe.MatchString(v) {
		return true
	}
	return false
}

func ptStripInvisible(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if ptInvisible(c) {
			n.RemoveChild(c)
		} else {
			ptStripInvisible(c)
		}
		c = next
	}
}

func ptAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

// ptWalk 先序遍历元素节点；fn 返回 true 时停下
func ptWalk(n *html.Node, fn func(*html.Node) bool) bool {
	if n == nil {
		return false
	}
	if n.Type == html.ElementNode && fn(n) {
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if ptWalk(c, fn) {
			return true
		}
	}
	return false
}

// ptText 节点下全部文字
func ptText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	if n != nil {
		rec(n)
	}
	return b.String()
}

// ptCompact 去掉所有空白
func ptCompact(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// ptLabel 按钮 / 链接上的字：input 看 value，其余看文字
func ptLabel(n *html.Node) string {
	if n.DataAtom == atom.Input {
		v, _ := ptAttr(n, "value")
		return ptCompact(v)
	}
	return ptCompact(ptText(n))
}

func (p *ptPage) text() string    { return strings.Join(strings.Fields(ptText(p.visible)), " ") }
func (p *ptPage) compact() string { return ptCompact(ptText(p.visible)) }

// hasPasswordInput 可见页面里有密码框 = 要求登录 = Cookie 失效（MP has_login_form）
func (p *ptPage) hasPasswordInput() bool {
	return ptWalk(p.visible, func(n *html.Node) bool {
		if n.DataAtom != atom.Input {
			return false
		}
		v, _ := ptAttr(n, "type")
		return strings.EqualFold(v, "password")
	})
}

var (
	ptLogoutRe = regexp.MustCompile(`(?i)\b(?:logout|logoff|signout)\b`)
	// 用户面板类链接（MP is_logged_in）：只有登录后的导航条才有
	ptUserLinkRe = regexp.MustCompile(`(?i)logout|mybonus|usercp`)
)

// loggedIn 有登录证据：没有密码框，且有退出登录 / 用户面板这类只有登录后才出现的东西
func (p *ptPage) loggedIn() bool {
	if p.visible == nil || p.hasPasswordInput() {
		return false
	}
	return ptWalk(p.visible, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.A:
			for _, k := range []string{"href", "data-url", "onclick", "lay-on"} {
				if v, ok := ptAttr(n, k); ok && ptUserLinkRe.MatchString(v) {
					return true
				}
			}
			if v, _ := ptAttr(n, "id"); v == "myitem" {
				return true
			}
		case atom.Form:
			if v, _ := ptAttr(n, "action"); ptLogoutRe.MatchString(v) || strings.Contains(strings.ToLower(v), "logout") {
				return true
			}
		case atom.Div:
			if v, _ := ptAttr(n, "class"); v == "user-info-side" {
				return true
			}
		}
		role, _ := ptAttr(n, "role")
		if n.DataAtom == atom.A || n.DataAtom == atom.Button || role == "button" || n.Data == "shark-icon-button" {
			for _, k := range []string{"href", "action", "formaction", "data-url", "onclick", "lay-on"} {
				if v, ok := ptAttr(n, k); ok && ptLogoutRe.MatchString(v) {
					return true
				}
			}
			switch strings.ToLower(ptLabel(n)) {
			case "退出登录", "退出登錄", "登出", "logout", "signout":
				return true
			}
		}
		return false
	})
}

// Cloudflare / DDoS-Guard 挑战页（MP under_challenge 的标题与选择器）
var (
	ptChallengeTitles = []string{"just a moment...", "请稍候…", "ddos-guard"}
	ptChallengeIDs    = map[string]bool{"cf-challenge-running": true, "cf-please-wait": true, "challenge-spinner": true, "trk_jschal_js": true}
	ptChallengeClass  = []string{"ray_id", "attack-box"}
)

func (p *ptPage) challenge() bool {
	if p.full == nil {
		return false
	}
	return ptWalk(p.full, func(n *html.Node) bool {
		if n.DataAtom == atom.Title {
			t := strings.ToLower(strings.TrimSpace(ptText(n)))
			for _, c := range ptChallengeTitles {
				if t == c {
					return true
				}
			}
		}
		if v, _ := ptAttr(n, "id"); ptChallengeIDs[v] {
			return true
		}
		if v, _ := ptAttr(n, "class"); v != "" {
			for _, cls := range strings.Fields(v) {
				for _, c := range ptChallengeClass {
					if cls == c {
						return true
					}
				}
			}
		}
		return false
	})
}

var (
	// 明确失败的提示优先：「签到失败」「没有签到成功」
	ptSignFailRe = regexp.MustCompile(`(?:签到|簽到)(?:失败|失敗|未成功)|(?:未|没有|沒有)(?:签到|簽到)成功`)
	// 「今天尚未签到」整句
	ptNotYetRe = regexp.MustCompile(`^(?:您|你)?(?:今天|今日)?(?:尚未|还未|還未|未|没有|沒有)(?:签到|簽到)[！!。.]?$`)
	// 整个节点就是完成状态：「签到成功」「今日已签到」「已经打卡」
	ptDoneLabelRe = regexp.MustCompile(`^(?:签到成功|簽到成功|(?:今日|今天)?(?:已签到|已簽到|已经签到|已經簽到)|已经打卡)[！!。.]?$`)
	// NexusPHP 当前用户的签到结果；普通奖励说明、累计天数不算（MP has_signin_evidence）
	ptDoneTextRe = regexp.MustCompile(`(?:您|你)(?:今天|今日)(?:已(?:经)?签(?:到|过到)|已經簽到)(?:过|過|了)|` +
		`(?:本次|此次)(?:签到|簽到)(?:您)?(?:获得|獲得)(?:了)?\d|(?:签到|簽到)已得\d|` +
		`(?:今天|今日)(?:签到|簽到)(?:您|你)?(?:获得|獲得)(?:了)?\d+(?:点|點)魔力值`)
	ptDoneEnRe = regexp.MustCompile(`(?i)You have already attend, no refresh please\.|` +
		`You have already attended \d+ days, Continuous \d+ days, this time you will get \d+ bonus\.`)
	// 还在等人点的签到按钮
	ptPendingLabels = map[string]bool{"签到": true, "簽到": true, "立即签到": true, "立即簽到": true, "每日签到": true, "打卡": true, "checkin": true, "checkinnow": true}
)

// signFailed 页面上明说签到失败
func (p *ptPage) signFailed() bool { return ptSignFailRe.MatchString(p.compact()) }

// pendingButton 还有「签到 / 立即签到 / 打卡」提交按钮
func (p *ptPage) pendingButton() bool {
	return ptWalk(p.visible, func(n *html.Node) bool {
		isBtn := n.DataAtom == atom.Button
		if n.DataAtom == atom.Input {
			t, _ := ptAttr(n, "type")
			isBtn = strings.EqualFold(t, "submit") || strings.EqualFold(t, "button")
		}
		return isBtn && ptPendingLabels[strings.ToLower(ptLabel(n))]
	})
}

// signinEvidence 签到完成的证据；返回一小段原文给结果提示用
func (p *ptPage) signinEvidence() (bool, string) {
	if p.visible == nil || p.hasPasswordInput() || p.signFailed() || p.pendingButton() {
		return false, ""
	}
	notYet := ptWalk(p.visible, func(n *html.Node) bool { return ptNotYetRe.MatchString(ptCompact(ptText(n))) })
	if notYet {
		return false, ""
	}
	var label string
	ptWalk(p.visible, func(n *html.Node) bool {
		if n.DataAtom == atom.Input {
			if t, _ := ptAttr(n, "type"); !strings.EqualFold(t, "button") && !strings.EqualFold(t, "submit") {
				return false
			}
		}
		if l := ptLabel(n); ptDoneLabelRe.MatchString(l) {
			label = l
			return true
		}
		return false
	})
	if label != "" {
		return true, label
	}
	c := p.compact()
	if loc := ptDoneTextRe.FindStringIndex(c); loc != nil {
		return true, ptSnippet(c, loc[0], loc[1])
	}
	if m := ptDoneEnRe.FindString(p.text()); m != "" {
		return true, m
	}
	return false, ""
}

// hasText 可见正文或按钮上有这段字（各站专用流程用：AGSVPT 的「立即签到」、海胆的「已经打卡」
// 都是 input 按钮的 value，不在正文里）；页面明说失败时一律不算
func (p *ptPage) hasText(s string) bool {
	if p.signFailed() {
		return false
	}
	s = ptCompact(s)
	if strings.Contains(p.compact(), s) {
		return true
	}
	return ptWalk(p.visible, func(n *html.Node) bool {
		if n.DataAtom != atom.Input {
			return false
		}
		t, _ := ptAttr(n, "type")
		return (strings.EqualFold(t, "submit") || strings.EqualFold(t, "button")) && strings.Contains(ptLabel(n), s)
	})
}

// ptSnippet 命中位置往后多带一点（把「获得 N 魔力值」带全），最长 40 个字
func ptSnippet(s string, from, to int) string {
	end := to
	for n := 0; end < len(s) && n < 12; n++ {
		r, size := utf8.DecodeRuneInString(s[end:])
		if strings.ContainsRune("。！!，,；;", r) {
			break
		}
		end += size
	}
	out := s[from:end]
	if utf8.RuneCountInString(out) > 40 {
		out = string([]rune(out)[:40])
	}
	return out
}

// ptBrief 页面开头一小段可见文字，写进失败原因方便排查（不会含 Cookie：那只在请求头里）
func (p *ptPage) brief() string {
	t := p.text()
	if utf8.RuneCountInString(t) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	return t
}
