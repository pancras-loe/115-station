package api

// TG 频道抓取引擎（订阅管理的底层，Web 搜索入口已删除）：
// 抓取 Telegram 频道公开网页预版 https://t.me/s/<频道>?q=<关键词>，
// 解析消息卡片，供订阅检查逐频道检索新资源。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	htmlpkg "golang.org/x/net/html"
)

type tgSearchCfg struct {
	Channels string `json:"channels"` // 每行一个频道名（@xxx 或 xxx）
	Target   string `json:"target_cid"`
	Organize bool   `json:"organize"`
}

func (h *Handler) loadTgSearchCfg() *tgSearchCfg {
	c := &tgSearchCfg{Organize: true}
	if v := h.Config.GetSetting("tgsearch"); v != "" {
		_ = json.Unmarshal([]byte(v), c)
	}
	return c
}

func (h *Handler) saveTgSearchCfg(c *tgSearchCfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("tgsearch", string(b))
}

// TgSearchGetConfig GET /tgsearch/config

// TgSearchSaveConfig POST /tgsearch/config

// ==================== 搜索 ====================

type tgLink struct {
	URL  string `json:"url"`
	Type string `json:"type"` // 115/magnet/ed2k
}

type tgItem struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Channel string   `json:"channel"`
	MsgID   int64    `json:"msg_id,omitempty"`
	Date    string   `json:"date"`
	Image   string   `json:"image,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Links   []tgLink `json:"links"`
	Main    tgLink   `json:"main"`
	Pass    string   `json:"pass,omitempty"`
}

var (
	tgCloudRes = []struct {
		re *regexp.Regexp
		t  string
	}{
		// 115 链接保留 ?password=/# 参数（分享密码常直接带在链接上）
		{regexp.MustCompile(`https?://(?:115\.com|115cdn\.com|anxia\.com)/s/[A-Za-z0-9_-]+(?:\?[^"'\s<>]*)?(?:#[^"'\s<>]*)?`), "115"},
		{regexp.MustCompile(`magnet:\?xt=urn:btih:[0-9a-zA-Z]{8,}`), "magnet"},
		{regexp.MustCompile(`ed2k://\|file\|[^|<>\s]+\|\d+\|[0-9a-fA-F]{32}\|/?`), "ed2k"},
	}
	tgPassRe      = regexp.MustCompile(`(?i)(?:提取码|访问码|密码|口令)\s*[:：]?\s*([0-9a-zA-Z]{4})`)
	tgPassURLOrRe = regexp.MustCompile(`[?&](?:password|pwd|code)=([0-9a-zA-Z]+)`)
	tgTimeRe      = regexp.MustCompile(`<time[^>]*datetime="([^"]+)"`)
	tgPostRe      = regexp.MustCompile(`data-post="([^"/]+)/(\d+)"`)
	tgPhotoRe     = regexp.MustCompile(`url\('(https?://[^']+)'\)`)
	tgHrefRe      = regexp.MustCompile(`href="([^"]+)"`)
	tgTagRe       = regexp.MustCompile(`#([^#\s<>"']+)`)
	tgBrSplit     = regexp.MustCompile(`<br\s*/?>`)
	tgStripTag    = regexp.MustCompile(`<[^>]+>`)
	tgWsRe        = regexp.MustCompile(`[ \t]+`)
)

// TgSearchSearch GET /tgsearch/search?keyword=xxx

// tgSearchChannel 抓取并解析单个频道
func tgSearchChannel(channel, keyword string) ([]tgItem, error) {
	api := "https://t.me/s/" + url.PathEscape(channel) + "?q=" + url.QueryEscape(keyword)
	body, err := tgHTTPGet(api, 25*time.Second)
	if err != nil {
		return nil, err
	}
	return tgParseChannel(body, channel, keyword), nil
}

// tgHTTPGet 带浏览器 UA 的 GET；配置了代理则走代理
func tgHTTPGet(rawURL string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// tgParseChannel 解析 t.me/s 页面：按消息卡片分段，逐段提取
func tgParseChannel(pageHTML, channel, keyword string) []tgItem {
	const marker = `tgme_widget_message_wrap`
	var items []tgItem
	pos := 0
	for {
		start := strings.Index(pageHTML[pos:], marker)
		if start < 0 {
			break
		}
		start += pos
		end := strings.Index(pageHTML[start+len(marker):], marker)
		if end < 0 {
			end = len(pageHTML)
		} else {
			end += start + len(marker)
		}
		seg := pageHTML[start:end]
		pos = start + len(marker)

		it := tgParseMessage(seg, channel)
		if it == nil || len(it.Links) == 0 {
			continue
		}
		// 主链接：115 优先
		it.Main = it.Links[0]
		for _, l := range it.Links {
			if l.Type == "115" {
				it.Main = l
				break
			}
		}
		items = append(items, *it)
	}
	return items
}

// tgParseMessage 解析单条消息卡片
func tgParseMessage(seg, channel string) *tgItem {
	inner, ok := tgExtractMessageText(seg)
	if !ok {
		return nil
	}
	it := &tgItem{Channel: channel}
	if m := tgPostRe.FindStringSubmatch(seg); m != nil {
		it.Channel = m[1]
		it.MsgID, _ = strconv.ParseInt(m[2], 10, 64)
	}
	if m := tgTimeRe.FindStringSubmatch(seg); m != nil {
		if t, err := time.Parse(time.RFC3339, m[1]); err == nil {
			it.Date = t.Local().Format("2006-01-02 15:04")
		} else {
			it.Date = m[1]
		}
	}
	if m := tgPhotoRe.FindStringSubmatch(seg); m != nil {
		it.Image = m[1]
	}
	// 链接：锚点 href + 文本裸链
	seen := map[string]bool{}
	addLink := func(raw string) {
		raw = htmlpkg.UnescapeString(raw)
		for _, cr := range tgCloudRes {
			for _, l := range cr.re.FindAllString(raw, -1) {
				if !seen[l] {
					seen[l] = true
					it.Links = append(it.Links, tgLink{URL: l, Type: cr.t})
				}
			}
		}
	}
	for _, m := range tgHrefRe.FindAllStringSubmatch(inner, -1) {
		addLink(m[1])
	}
	addLink(inner)
	// 话题标签
	for _, m := range tgTagRe.FindAllStringSubmatch(inner, -1) {
		it.Tags = append(it.Tags, m[1])
	}
	if len(it.Tags) > 6 {
		it.Tags = it.Tags[:6]
	}

	// 标题 = 首个 <br> 前的文本；其余为内容
	parts := tgBrSplit.Split(inner, 2)
	it.Title = tgText(parts[0])
	if len(parts) > 1 {
		it.Content = tgText(parts[1])
	}
	if it.Title == "" {
		it.Title = tgText(inner)
	}
	it.Title = strings.TrimSpace(it.Title)
	if it.Title == "" {
		return nil
	}
	// 提取码：先看文本关键词，再从 115 链接参数（?password=）里取
	for _, src := range []string{it.Title, it.Content} {
		if m := tgPassRe.FindStringSubmatch(src); m != nil {
			it.Pass = m[1]
			break
		}
	}
	if it.Pass == "" {
		for _, l := range it.Links {
			if l.Type != "115" {
				continue
			}
			if m := tgPassURLOrRe.FindStringSubmatch(l.URL); m != nil {
				it.Pass = m[1]
				break
			}
		}
	}
	return it
}

// tgExtractMessageText 定位 js-message_text 所在 div 的内部 HTML（div 深度配对）
func tgExtractMessageText(seg string) (string, bool) {
	i := strings.Index(seg, "js-message_text")
	if i < 0 {
		return "", false
	}
	start := strings.LastIndex(seg[:i], "<div")
	if start < 0 {
		return "", false
	}
	innerStart := strings.Index(seg[start:], ">")
	if innerStart < 0 {
		return "", false
	}
	innerStart += start + 1
	depth := 1
	j := innerStart
	for j < len(seg) && depth > 0 {
		openIdx := strings.Index(seg[j:], "<div")
		closeIdx := strings.Index(seg[j:], "</div>")
		switch {
		case closeIdx < 0:
			return seg[innerStart:], true
		case openIdx >= 0 && openIdx < closeIdx:
			depth++
			j += openIdx + 4
		default:
			depth--
			if depth == 0 {
				return seg[innerStart : j+closeIdx], true
			}
			j += closeIdx + 6
		}
	}
	return seg[innerStart:], true
}

// tgText 去 HTML 标签 + 解析实体 + 压缩空白
func tgText(s string) string {
	s = tgStripTag.ReplaceAllString(s, " ")
	s = htmlpkg.UnescapeString(s)
	s = tgWsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// tgFilterDedup 标题/内容关键词过滤（含紧凑匹配）+ 主链接去重
func tgFilterDedup(items []tgItem, keyword string) []tgItem {
	key := strings.ToLower(keyword)
	compact := func(s string) string {
		s = strings.ToLower(s)
		return regexp.MustCompile(`[\s\p{P}]+`).ReplaceAllString(s, "")
	}
	ck := compact(keyword)
	seen := map[string]bool{}
	var out []tgItem
	for _, it := range items {
		if key != "" {
			lt := strings.ToLower(it.Title + " " + it.Content)
			if !strings.Contains(lt, key) && !(len(ck) >= 2 && strings.Contains(compact(lt), ck)) {
				continue
			}
		}
		if seen[it.Main.URL] {
			continue
		}
		seen[it.Main.URL] = true
		out = append(out, it)
	}
	return out
}

// ==================== 转存 ====================

// TgSearchSave POST /tgsearch/save {url, pass}

// chromeUA 桌面 Chrome UA（站点对 Go 默认 UA 有 WAF 指纹拦截时使用）
const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// trimLinkTail 去掉链接尾部被 HTML/正文带入的标点
func trimLinkTail(s string) string {
	return strings.TrimRight(s, ".,;。，；）】」\"'#")
}
