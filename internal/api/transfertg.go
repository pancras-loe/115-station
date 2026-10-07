package api

// ==================== 影视转存 · TG 频道搜索 ====================
//
// 抓 Telegram 频道的公开网页版 t.me/s/<频道>?q=<片名>（tgsearch.go 的解析引擎），
// 不用登录 TG 账号。p115strmhelper 的 TgSearcher 也是这条路（只读思路）。
//
// 频道清单存在 setting "tgsearch" 的 channels 里。
//
// 频道消息噪音大（同一条消息常挂好几个链接、标题里夹着表情和「名称：」），
// 片名比对交给 resNormalize 统一做；这里只负责抓、拆、去重。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// tgResMaxChannels 一次搜索最多查几个频道：每个频道一次 t.me 请求，太多既慢又容易被限流
const tgResMaxChannels = 20

// tgResConcurrency 同时抓几个频道：有人在页面上等着，放开到 3 个并发，但不全开
const tgResConcurrency = 3

// tgResChannels 频道清单：每行一个，@xxx / xxx / https://t.me/xxx 都认，去重保序。
// 也认 p115strmhelper 导出的 JSON（[{"name":"显示名","id":"频道用户名"}]，
// 它拼的是 t.me/s/{id}），从那边搬过来不用手抄
func tgResChannels(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range tgResChannelTokens(raw) {
		ch := tgChannelName(line)
		if ch == "" || seen[strings.ToLower(ch)] {
			continue
		}
		seen[strings.ToLower(ch)] = true
		out = append(out, ch)
	}
	return out
}

// tgChannelName 从链接/@名/裸名解析频道名（https://t.me/xxx → xxx）
func tgChannelName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "t.me/"); i >= 0 {
		s = s[i+5:]
		s = strings.TrimPrefix(s, "s/")
	}
	s = strings.TrimPrefix(s, "@")
	if j := strings.IndexAny(s, "?#/"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

// tgResChannelTokens 拆出一个个频道写法。不是合法 JSON 就当普通文本拆
func tgResChannelTokens(raw string) []string {
	if t := strings.TrimSpace(raw); strings.HasPrefix(t, "[") {
		var list []map[string]any
		if json.Unmarshal([]byte(t), &list) == nil {
			var ids []string
			for _, it := range list {
				switch v := it["id"].(type) {
				case string:
					ids = append(ids, v)
				case float64: // 纯数字 id 被 JSON 解成浮点
					ids = append(ids, strconv.FormatInt(int64(v), 10))
				}
			}
			return ids
		}
	}
	return strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == '，' || r == ' ' })
}

func tgResReady() string {
	if len(tgResChannels(loadTgSearchChannels())) == 0 {
		return "未设置要搜的频道"
	}
	return ""
}

// loadTgSearchChannels 读频道清单（不经 Handler：来源的 ready 是包级函数）
func loadTgSearchChannels() string {
	var c tgSearchCfg
	if v := settingValueCompat("tgsearch"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c.Channels
}

func tgResSearch(h *Handler, q resQuery) (resResult, error) {
	channels := tgResChannels(loadTgSearchChannels())
	if len(channels) > tgResMaxChannels {
		channels = channels[:tgResMaxChannels]
	}
	r, failed, err := tgResSearchChannels(channels, q.Title)
	if err == nil && len(r) == 0 && q.OrigTitle != "" && titleKey(q.OrigTitle) != titleKey(q.Title) {
		r, failed, err = tgResSearchChannels(channels, q.OrigTitle)
	}
	if err != nil {
		return resResult{}, err
	}
	note := ""
	if len(failed) > 0 {
		note = fmt.Sprintf("%d 个频道没抓到：%s", len(failed), strings.Join(failed, "、"))
	}
	return resResult{Items: r, Note: note}, nil
}

// tgResSearchChannels 并发抓各频道，一条消息里的每个链接各算一条资源，按链接去重。
// 全部频道都失败才算出错（多半是连不上 t.me），部分失败写进说明
func tgResSearchChannels(channels []string, keyword string) ([]ResourceItem, []string, error) {
	type res struct {
		ch    string
		items []tgItem
		err   error
	}
	results := make([]res, len(channels))
	sem := make(chan struct{}, tgResConcurrency)
	var wg sync.WaitGroup
	for i, ch := range channels {
		wg.Add(1)
		go func(i int, ch string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items, err := tgSearchChannel(ch, keyword)
			results[i] = res{ch: ch, items: items, err: err}
		}(i, ch)
	}
	wg.Wait()

	var out []ResourceItem
	var failed []string
	var firstErr error
	seen := map[string]bool{}
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r.ch)
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		for _, it := range r.items {
			out = append(out, tgMessageResources(it, seen)...)
		}
	}
	if len(failed) == len(channels) && firstErr != nil {
		log.Printf("[影视转存] ✗ TG 频道全部抓取失败: %v", firstErr)
		return nil, nil, fmt.Errorf("连不上 t.me（%v）；国内网络需在「系统配置 → 代理」里设代理", firstErr)
	}
	return out, failed, nil
}

// reTgTitleLead 频道消息标题常见的「名称：」「片名：」前缀
var reTgTitleLead = regexp.MustCompile(`^\s*(?:资源)?(?:名称|片名|标题)\s*[:：]\s*`)

// tgMessageResources 一条频道消息 → 若干资源（每个链接一条）
func tgMessageResources(it tgItem, seen map[string]bool) []ResourceItem {
	title := strings.TrimSpace(reTgTitleLead.ReplaceAllString(it.Title, ""))
	if title == "" {
		title = it.Title
	}
	// 话题标签（#4K #杜比视界）只拿来补画质标签，不进显示标题
	preset := resTagsOf(title + " " + strings.Join(it.Tags, " "))
	size := ""
	if m := reResSize.FindString(it.Content); m != "" && resParseSize(m) >= 100<<20 {
		size = strings.TrimSpace(m)
	}
	var out []ResourceItem
	for _, l := range it.Links {
		link := trimLinkTail(l.URL)
		if seen[link] {
			continue
		}
		seen[link] = true
		ri := ResourceItem{
			Source: "tg", Title: title, URL: link, Size: size, Tags: preset,
			Time: it.Date, TimeUnix: resParseTime(it.Date),
		}
		ri.Kind, ri.Action, ri.Pan = resKindOf(link, "")
		if ri.Kind == "share115" {
			ri.Code = it.Pass
		}
		if it.Channel != "" {
			ri.Via = "@" + it.Channel
		}
		out = append(out, ri)
	}
	return out
}

// ==================== 频道配置 ====================

// TgSearchGetConfig GET /tgsearch/config —— 影视转存「来源设置」里的频道清单
func (h *Handler) TgSearchGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"channels": h.loadTgSearchCfg().Channels})
}

// TgSearchSaveConfig POST /tgsearch/config {channels}
// 只改频道清单：同一个 setting 里还有订阅自动转存用的 target_cid / organize，不能被整份覆盖
func (h *Handler) TgSearchSaveConfig(c *gin.Context) {
	var req struct {
		Channels string `json:"channels"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	list := tgResChannels(req.Channels)
	cfg := h.loadTgSearchCfg()
	cfg.Channels = strings.Join(list, "\n")
	h.saveTgSearchCfg(cfg)
	log.Printf("[影视转存] ✓ TG 频道清单已保存（%d 个）", len(list))
	c.JSON(http.StatusOK, gin.H{"message": "已保存", "channels": cfg.Channels})
}
