package api

// ==================== 浏览器出口对齐：时区 / 语言 / WebRTC IP 跟着出口 IP 走 ====================
//
// 2026-11-17 现场：RE0 勾了「通过代理访问」后，Cloudflare 验证框点了三次都没过——每点一次，验证框
// 转一阵又换成一个新的（日志里 challenges.cloudflare.com 的 rch/… 换了四个编号），点击是收到了的，
// 是 Cloudflare 判了不通过。当时浏览器的时区一直是容器的 TZ（Asia/Shanghai），出口却在代理节点那边；
// 语言没设（容器里没有 LANG，Chromium 按 en-US）；WebRTC 走 UDP、不经 HTTP 代理，报出去的是家里的
// 真实 IP。CloakBrowser 官方包装层的 geoip=True 做的就是把这几样对齐，README 排查一节写明
// 「时区 + 语言与出口 IP 对不上」是被拦最常见的三个原因之一（另两个是机房 IP、无头模式）。
//
// 做法对照官方 js/src/geoip.ts（MIT，只读）：
//   - 出口 IP 与国家读站点自己的 /cdn-cgi/trace，不问第三方的「我的 IP」：Clash 这类代理按域名分流，
//     问 ipinfo.io 走的节点不一定是访问 re0.me 的那个；trace 回的正是 Cloudflare 这一侧看到的 ip= / loc=。
//   - 时区按这个 IP 查（几个免费接口挨个试，回的 IP 对不上的不用）。
//   - 语言按国家取，表里没有的国家用 en-US（官方同款）。站点只认中文 / 英文界面，日语等其他语言
//     站点会退回默认语言，签到等按文字认按钮的地方中英文都认（re0web.go）。
//   - WebRTC IP 只在走代理时改：直连时本来就是这个 IP。
// 查不到就照旧：时区用容器的 TZ，不设语言与 WebRTC，日志里写一行。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// cloakGeo 浏览器这次线路的出口
type cloakGeo struct {
	IP       string
	Country  string // ISO 两位大写
	Timezone string
	Via      string // 时区是哪个接口查到的（日志用）
}

func (g cloakGeo) String() string {
	if g.IP == "" {
		return "未知"
	}
	s := g.IP
	if g.Country != "" {
		s += " " + g.Country
	}
	if g.Timezone != "" {
		s += " " + g.Timezone
	}
	return s
}

var (
	// 按 IP 查时区的接口（%s = IP），挨个试。都是不要 key 的免费接口，一次启动浏览器只查一次
	cloakGeoLookups = []string{
		"https://ipinfo.io/%s/json",
		"https://api.ip.sb/geoip/%s",
		"https://ipwho.is/%s",
		"http://ip-api.com/json/%s?fields=status,countryCode,timezone,query",
	}
	cloakGeoBudget = 12 * time.Second // 整个查询最多花多久：启动浏览器在等它
	cloakGeoEach   = 6 * time.Second
)

var cloakTZRe = regexp.MustCompile(`^(UTC|[A-Za-z]+(?:/[A-Za-z0-9_+\-]+){1,2})$`)

// cloakResolveGeo 按浏览器这次的线路查出口。siteBase 是站点根地址（https://re0.me），proxy 空 = 直连
func cloakResolveGeo(siteBase, proxy string) (cloakGeo, error) {
	var g cloakGeo
	tr := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err != nil || pu.Host == "" {
			return g, fmt.Errorf("代理地址认不出") // 别把地址打出来：可能带账号密码
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: cloakGeoEach}
	ctx, cancel := context.WithTimeout(context.Background(), cloakGeoBudget)
	defer cancel()

	trace := strings.TrimRight(siteBase, "/") + "/cdn-cgi/trace"
	body, err := cloakGeoGet(ctx, cl, trace)
	if err != nil {
		return g, fmt.Errorf("读 %s 失败: %v", trace, err)
	}
	g.IP, g.Country = parseCFTrace(body)
	if g.IP == "" {
		return g, fmt.Errorf("%s 里没有出口 IP", trace)
	}
	var errs []string
	for _, tpl := range cloakGeoLookups {
		if ctx.Err() != nil {
			break
		}
		lu := fmt.Sprintf(tpl, url.PathEscape(g.IP))
		host := lu
		if u, err := url.Parse(lu); err == nil {
			host = u.Host
		}
		raw, err := cloakGeoGet(ctx, cl, lu)
		if err != nil {
			errs = append(errs, host+": "+err.Error())
			continue
		}
		ip, cc, tz := parseGeoJSON(raw)
		if tz == "" || (ip != "" && !sameIP(ip, g.IP)) {
			errs = append(errs, host+": 回的不是这个 IP 或没有时区")
			continue
		}
		g.Timezone, g.Via = tz, host
		if g.Country == "" {
			g.Country = cc
		}
		return g, nil
	}
	if len(errs) == 0 {
		errs = append(errs, "超时")
	}
	return g, fmt.Errorf("查不到 %s 的时区（%s）", g.IP, strings.Join(errs, "；"))
}

func cloakGeoGet(ctx context.Context, cl *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// parseCFTrace 读 /cdn-cgi/trace 的 ip= 与 loc=（XX / T1 这种不是国家的不认）
func parseCFTrace(b []byte) (ip, country string) {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			if net.ParseIP(v) != nil {
				ip = v
			}
		case "loc":
			country = normCountry(v)
		}
	}
	return ip, country
}

// parseGeoJSON 几个接口的返回格式不一样，按字段名认：
// ipinfo {ip,country,timezone}；ip.sb {ip,country_code,timezone}；ipwho.is {ip,country_code,timezone:{id}}；
// ip-api {query,countryCode,timezone}
func parseGeoJSON(b []byte) (ip, country, tz string) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return "", "", ""
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return strings.TrimSpace(s)
	}
	if v, ok := m["success"].(bool); ok && !v {
		return "", "", ""
	}
	if s := str("status"); s != "" && s != "success" {
		return "", "", ""
	}
	ip = str("ip")
	if ip == "" {
		ip = str("query")
	}
	for _, k := range []string{"country_code", "countryCode", "country"} {
		if c := normCountry(str(k)); c != "" {
			country = c
			break
		}
	}
	switch v := m["timezone"].(type) {
	case string:
		tz = strings.TrimSpace(v)
	case map[string]any:
		tz, _ = v["id"].(string)
	}
	if !cloakTZRe.MatchString(tz) {
		tz = ""
	}
	return ip, country, tz
}

func normCountry(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s == "XX" || s == "T1" || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return ""
	}
	return s
}

func sameIP(a, b string) bool {
	x, y := net.ParseIP(a), net.ParseIP(b)
	return x != nil && y != nil && x.Equal(y)
}

// cloakCountryLocale 国家 → 浏览器语言（代理出口常见的国家；表里没有的用 en-US，官方同款）
var cloakCountryLocale = map[string]string{
	"CN": "zh-CN", "HK": "zh-HK", "TW": "zh-TW", "MO": "zh-MO",
	"SG": "en-SG", "JP": "ja-JP", "KR": "ko-KR", "MY": "ms-MY", "TH": "th-TH", "VN": "vi-VN",
	"ID": "id-ID", "PH": "en-PH", "IN": "hi-IN",
	"US": "en-US", "CA": "en-CA", "GB": "en-GB", "IE": "en-IE", "AU": "en-AU", "NZ": "en-NZ", "ZA": "en-ZA",
	"DE": "de-DE", "AT": "de-AT", "CH": "de-CH", "FR": "fr-FR", "BE": "fr-BE", "LU": "fr-LU",
	"NL": "nl-NL", "IT": "it-IT", "ES": "es-ES", "PT": "pt-PT", "BR": "pt-BR", "MX": "es-MX", "AR": "es-AR",
	"RU": "ru-RU", "UA": "uk-UA", "PL": "pl-PL", "CZ": "cs-CZ", "RO": "ro-RO", "HU": "hu-HU", "BG": "bg-BG",
	"SE": "sv-SE", "NO": "nb-NO", "DK": "da-DK", "FI": "fi-FI", "TR": "tr-TR", "IL": "he-IL", "AE": "ar-AE",
	"SA": "ar-SA", "KZ": "ru-KZ",
}

// cloakGeoArgs 按出口算浏览器启动参数（不带前导 --）。geo 没查到的项不设；时区查不到退回容器的 TZ
func cloakGeoArgs(g cloakGeo, viaProxy bool, envTZ string) map[string]string {
	out := map[string]string{}
	if tz := firstNonEmpty(g.Timezone, strings.TrimSpace(envTZ)); tz != "" {
		out["fingerprint-timezone"] = tz
	}
	if g.Country != "" {
		loc := cloakCountryLocale[g.Country]
		if loc == "" {
			loc = "en-US"
		}
		out["lang"] = loc
		out["fingerprint-locale"] = loc
	}
	if viaProxy && g.IP != "" {
		out["fingerprint-webrtc-ip"] = g.IP
	}
	return out
}
