package api

// ==================== 影视转存：按影片聚合各资源站 ====================
//
// 原来四个资源站（观影 / 盘搜 / 不太灵 / RE0）各占一个页签，找一部片要搜四遍，
// 每个站的结果长得都不一样：观影是种子、盘搜是各家网盘分享、不太灵要先挑站内影片、
// RE0 要花积分解锁。这里把它们收成一个模型：
//
//	选定 TMDB 条目 → 前端对每个来源各发一次 GET /transfer/resources/:source
//	→ 统一成 ResourceItem（类型 / 画质标签 / 是否相关 / 洗版偏好 / 是否提交过）
//	→ 用户点哪条都走 POST /transfer/submit
//
// 按来源分开请求而不是一个大接口：盘搜一次要好几秒，合在一起会拖住其他来源，
// 分开之后谁先回来谁先显示。
//
// 思路参考 MediaSync115（`/{type}/{tmdb_id}/resources` 按优先级跑全部来源、
// 资源标题解析画质标签）与 p115strmhelper（TG 结果先按片名归一化比对再返回），
// 只读思路、没有照搬代码。
//
// 约束（改之前先看）：
//   - 提交一律走 submitResource：分享转存 / 离线下载的核心函数自己登记来源链接
//     （dlLinkRecord），这里不要再另起一条提交通道；
//   - removedCloudResource 在 resNormalize 统一过一遍，新接来源不用各自记着过滤；
//   - RE0 解锁花积分：只有前端二次确认后带 confirm=true 才解锁，机器人不走解锁。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resQuery 一次按片搜索的条件。TmdbID 为 0 表示跳过了 TMDB、直接拿关键词搜：
// 这时没有东西可比对，结果一律当作相关
type resQuery struct {
	TmdbID    int
	Type      string // movie / tv；跳过 TMDB 时为空
	Title     string
	OrigTitle string
	Year      string
	names     []string // TMDB 别名与各语言译名（相关性判定用）
}

// resTags 从资源标题解析出来的画质标签（ParseResourceInfo，与洗版、重命名同一套）
type resTags struct {
	Pix    string `json:"pix,omitempty"`
	Type   string `json:"type,omitempty"`
	Effect string `json:"effect,omitempty"`
	Video  string `json:"video,omitempty"`
	Audio  string `json:"audio,omitempty"`
	Team   string `json:"team,omitempty"`
	Season string `json:"season,omitempty"` // S01 / S01-S03 / 全集
	Zh     bool   `json:"zh,omitempty"`     // 标题写明带中文字幕
}

// ResourceItem 各来源统一后的一条资源
type ResourceItem struct {
	Source string `json:"source"` // gy / pansou / tg / mukaku / re0
	Kind   string `json:"kind"`   // share115 / magnet / ed2k / pan（其他网盘）
	Pan    string `json:"pan,omitempty"`
	// Action 点这条会发生什么：transfer=115 分享转存 / offline=离线下载 /
	// open=其他网盘，打开原链接手动处理 / unlock=RE0 先解锁（花积分）再转存
	Action string `json:"action"`
	Title  string `json:"title"`
	Via    string `json:"via,omitempty"`  // 来源里更细的出处：TG 的频道名
	URL    string `json:"url,omitempty"`  // 直接可用的链接；观影按关键词搜到的种子要先去详情页取磁力、RE0 要先解锁，这两种为空
	Code   string `json:"code,omitempty"` // 提取码
	Ref    string `json:"ref,omitempty"`  // 需要服务端再换一次的引用：观影种子详情页路径（只有跳过 TMDB 时）/ RE0 slug

	Size      string `json:"size,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Seeds     int    `json:"seeds,omitempty"`
	Time      string `json:"time,omitempty"`
	TimeUnix  int64  `json:"time_unix,omitempty"`

	Points *int `json:"points,omitempty"` // RE0 解锁要花的积分
	Owned  bool `json:"owned,omitempty"`  // RE0 已经解锁过（再解锁不扣积分）

	Tags     resTags `json:"tags"`
	Relevant bool    `json:"relevant"`
	// Rank 命中洗版策略第几条优先级规则（0 最优），-1 表示没有命中任何一条
	Rank int `json:"rank"`
	// SubmittedAt 这条链接之前提交过（来源链接台账里有），提交时间
	SubmittedAt int64 `json:"submitted_at,omitempty"`

	// files 癫影不解锁就给的文件清单（订阅付费解锁前按它核对缺集，subrun.go），不下发前端
	files []string
}

// resResult 一个来源的搜索结果；Note 是给人看的附加说明（如不太灵匹配到了哪部站内影片）
type resResult struct {
	Items []ResourceItem
	Note  string
}

// resSource 一个资源来源
type resSource struct {
	Key   string
	Label string
	// ready 能不能搜：空串 = 可以，否则是原因（没登录、没配置…）
	ready  func() string
	search func(h *Handler, q resQuery) (resResult, error)
}

// resSources 来源清单，顺序即界面上的默认顺序
var resSources = []resSource{
	{Key: "gy", Label: "观影", ready: gyReady, search: gyResSearch},
	{Key: "pansou", Label: "盘搜", ready: func() string { return "" }, search: pansouResSearch},
	{Key: "tg", Label: "TG 频道", ready: tgResReady, search: tgResSearch},
	{Key: "mukaku", Label: "不太灵", ready: mukakuReady, search: mukakuResSearch},
	{Key: "re0", Label: "RE0", ready: re0Ready, search: re0ResSearch},
	{Key: "dian", Label: "癫影", ready: dianReadyReason, search: dianResSearch},
}

// resPaidSource 要花积分解锁的来源（Action=unlock）：RE0 / 癫影。
// 两个站的积分各算各的，订阅的自动解锁上限与预算也分开（subscribeCfg）
func resPaidSource(src string) bool { return src == "re0" || src == "dian" }

// resByTmdb 按 TMDB 编号列资源的来源：列出来的本来就是这一部，相关性与同名检查都放行
func resByTmdb(src string) bool { return src == "re0" || src == "dian" }

func resSourceOf(key string) *resSource {
	for i := range resSources {
		if resSources[i].Key == key {
			return &resSources[i]
		}
	}
	return nil
}

// ==================== 来源开关（setting "transfer_hub"） ====================

type transferHubCfg struct {
	Disabled []string `json:"disabled"` // 关掉的来源：搜片时不去请求
}

func loadTransferHubCfg() transferHubCfg {
	var cfg transferHubCfg
	if v := settingValueCompat("transfer_hub"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg
}

func (c transferHubCfg) enabled(key string) bool {
	for _, d := range c.Disabled {
		if d == key {
			return false
		}
	}
	return true
}

// ==================== 各来源适配 ====================

func gyReady() string {
	cfg := loadGyCfg()
	if len(cfg.Cookies) == 0 && (cfg.Username == "" || cfg.Password == "") {
		return "未登录观影账号"
	}
	return ""
}

func gyResSearch(h *Handler, q resQuery) (resResult, error) {
	if q.TmdbID == 0 {
		return gyResSearchKeyword(q)
	}
	list, err := gySearchTitles(q.Title)
	if err != nil {
		return resResult{}, err
	}
	t := gyPickTitle(q, list)
	if t == nil && q.OrigTitle != "" && titleKey(q.OrigTitle) != titleKey(q.Title) {
		more, err := gySearchTitles(q.OrigTitle)
		if err != nil {
			return resResult{}, err
		}
		list = append(list, more...)
		t = gyPickTitle(q, list)
	}
	if t == nil {
		if len(list) == 0 {
			return resResult{}, nil
		}
		return resResult{Note: fmt.Sprintf("站内搜到 %d 部影片，但类型、片名、年份都对不上", len(list))}, nil
	}
	d, err := gyFetchDetail(*t)
	if err != nil {
		return resResult{}, err
	}
	note := "站内影片：" + t.Title
	if t.Year != "" {
		note += "（" + t.Year + "）"
	}
	out := gyDetailItems(d)
	if len(out) == 0 {
		note += "，暂无资源"
	}
	return resResult{Items: out, Note: note}, nil
}

// gyResSearchKeyword 跳过 TMDB 时没有影片可认，退回按关键词搜站内种子
func gyResSearchKeyword(q resQuery) (resResult, error) {
	items, body, err := gySearchTorrents(q.Title, "")
	if err != nil {
		return resResult{}, err
	}
	if len(items) == 0 {
		// 0 条时把页面特征打进日志：空壳 / 受限 / 正常页一眼区分（站点改版时远程定位用）
		title := ""
		if m := reTitle.FindStringSubmatch(body); m != nil {
			title = gyHTMLUnescape(strings.TrimSpace(m[1]))
		}
		log.Printf("[观影] ○ 搜索未解析出条目（q=%s）: len=%d title=%q noLogin=%v", q.Title, len(body), title, gyIsNoLogin(body))
	}
	var out []ResourceItem
	for _, it := range items {
		title, _ := it["title"].(string)
		path, _ := it["path"].(string)
		size, _ := it["size"].(string)
		tm, _ := it["time"].(string)
		seeds, _ := it["seeds"].(string)
		n, _ := strconv.Atoi(strings.TrimSpace(seeds))
		out = append(out, ResourceItem{
			Source: "gy", Kind: "magnet", Action: "offline", Title: title, Ref: path,
			Size: size, Seeds: n, Time: tm, TimeUnix: resParseTime(tm),
		})
	}
	return resResult{Items: out}, nil
}

func pansouResSearch(h *Handler, q resQuery) (resResult, error) {
	items, err := pansouSearchItems(q.Title)
	if err != nil {
		return resResult{}, err
	}
	var out []ResourceItem
	for _, it := range items {
		ri := ResourceItem{
			Source: "pansou", Title: firstNonEmptyStr(it.Note, it.URL), URL: it.URL, Code: it.Password,
			TimeUnix: resParseTime(it.Datetime),
		}
		if ri.TimeUnix > 0 {
			ri.Time = time.Unix(ri.TimeUnix, 0).Format("2006-01-02")
		}
		ri.Kind, ri.Action, ri.Pan = resKindOf(it.URL, it.CloudType)
		out = append(out, ri)
	}
	return resResult{Items: out}, nil
}

func mukakuReady() string {
	if loadMukakuCfg().AccessToken == "" {
		return "未设置 VIP Token（资源仅 VIP 可见）"
	}
	return ""
}

func mukakuResSearch(h *Handler, q resQuery) (resResult, error) {
	videos, err := mukakuSearchVideos(q.Title)
	if err == nil && len(videos) == 0 && q.OrigTitle != "" && titleKey(q.OrigTitle) != titleKey(q.Title) {
		videos, err = mukakuSearchVideos(q.OrigTitle)
	}
	if err != nil {
		return resResult{}, err
	}
	v := mukakuPickVideo(q, videos)
	if v == nil {
		if len(videos) == 0 {
			return resResult{}, nil
		}
		return resResult{Note: fmt.Sprintf("站内搜到 %d 部影片，但片名、年份都对不上", len(videos))}, nil
	}
	raw, err := mukakuVideoResources(v.ID)
	if err != nil {
		return resResult{}, err
	}
	var out []ResourceItem
	for _, r := range raw {
		link, _ := r["link"].(string)
		name, _ := r["seed_name"].(string)
		code, _ := r["code"].(string)
		ri := ResourceItem{Source: "mukaku", Title: firstNonEmptyStr(name, link), URL: link, Code: code}
		ri.Kind, ri.Action, ri.Pan = resKindOf(link, "")
		out = append(out, ri)
	}
	note := "站内影片：" + v.Title
	if v.Years != "" {
		note += "（" + v.Years + "）"
	}
	if len(out) == 0 {
		note += "，没有读到资源（站方仅对 VIP 开放资源列表，确认 Token 仍有效）"
	}
	return resResult{Items: out, Note: note}, nil
}

// mukakuPickVideo 站内搜出来的是影片列表，挑和 TMDB 条目对得上的那一部：
// 片名相等优先于包含，年份相符再加分。一部都对不上就不挑，免得把别的片的资源列出来。
// 跳过 TMDB 时没有比对依据，取第一部
func mukakuPickVideo(q resQuery, videos []mukakuVideo) *mukakuVideo {
	if len(videos) == 0 {
		return nil
	}
	if q.TmdbID == 0 {
		return &videos[0]
	}
	best, bestScore := -1, 0
	for i, v := range videos {
		lvl := titleNone
		for _, n := range q.allNames() {
			if l := titleLevel(titleKey(n), v.Title, v.Otitle); l > lvl {
				lvl = l
			}
		}
		if lvl == titleNone {
			continue
		}
		score := lvl * 10
		if q.Year != "" && strings.HasPrefix(strings.TrimSpace(v.Years), q.Year) {
			score += 5
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return nil
	}
	return &videos[best]
}

func re0Ready() string {
	cfg := loadRe0Cfg()
	switch re0Channel(cfg) {
	case "openapi":
		return ""
	case "web":
		return re0BrowserBlocked("找资源时发现 RE0 浏览器没装")
	}
	return "未配置 RE0（在设置里填账号密码）"
}

func re0ResSearch(h *Handler, q resQuery) (resResult, error) {
	if q.TmdbID == 0 || (q.Type != "movie" && q.Type != "tv") {
		return resResult{Note: "RE0 按 TMDB 条目查资源，跳过 TMDB 时不可用"}, nil
	}
	list, err := re0ListResources(h, q.Type, q.TmdbID)
	if err != nil {
		return resResult{}, err
	}
	var out []ResourceItem
	for _, r := range list {
		// 标题太短时把分辨率 / 来源拼进去：画质标签是按标题解析的
		title := strings.TrimSpace(strings.Join(append(append([]string{r.Title}, r.VideoResolution...), r.Source...), " "))
		ri := ResourceItem{
			Source: "re0", Kind: "pan", Pan: r.PanType, Action: "unlock", Title: title, Ref: r.Slug,
			Size: r.ShareSize, Points: r.UnlockPoints, Owned: r.IsUnlocked,
			TimeUnix: resParseTime(r.CreatedAt),
		}
		switch strings.ToLower(r.PanType) {
		case "115":
			ri.Kind, ri.Pan = "share115", ""
		case "ed2k", "magnet":
			// RE0 把 ED2K 资源放在 115 那一组里，解锁出来是 ed2k 链接，走 115 离线（resolveResourceLink）
			ri.Kind, ri.Pan = strings.ToLower(r.PanType), ""
		}
		if ri.TimeUnix > 0 {
			ri.Time = time.Unix(ri.TimeUnix, 0).Format("2006-01-02")
		}
		for _, sub := range r.SubtitleLang {
			if strings.Contains(sub, "中") || strings.Contains(strings.ToLower(sub), "chi") || strings.Contains(strings.ToLower(sub), "zh") {
				ri.Tags.Zh = true
			}
		}
		out = append(out, ri)
	}
	return resResult{Items: out}, nil
}

// dianResSearch 癫影：按 TMDB 条目列分享。剧集一次给全部季，季集由站点标注（写进标题给覆盖估计用）
func dianResSearch(h *Handler, q resQuery) (resResult, error) {
	if q.TmdbID == 0 || (q.Type != "movie" && q.Type != "tv") {
		return resResult{Note: "癫影按 TMDB 条目查资源，跳过 TMDB 时不可用"}, nil
	}
	list, err := dianListShares(q.Type, q.TmdbID)
	if err != nil {
		return resResult{}, err
	}
	out := make([]ResourceItem, 0, len(list))
	for _, s := range list {
		out = append(out, dianItemOf(s))
	}
	return resResult{Items: out}, nil
}

// dianItemOf 一条癫影分享 → ResourceItem。都要先解锁（Action=unlock），离线分享解锁出来是磁力 / ed2k
func dianItemOf(s dianShare) ResourceItem {
	ri := ResourceItem{
		Source: "dian", Kind: "share115", Action: "unlock", Title: s.title(), Ref: dianRef(s),
		Size: s.TotalSizeHuman, SizeBytes: s.TotalSizeBytes, Points: s.UnlockCost, Owned: s.owned(),
		TimeUnix: resParseTime(s.CreatedAt), files: s.FileList,
	}
	if s.offline() {
		ri.Kind = "magnet"
		if strings.EqualFold(s.OfflineType, "ed2k") {
			ri.Kind = "ed2k"
		}
	}
	if ri.TimeUnix > 0 {
		ri.Time = time.Unix(ri.TimeUnix, 0).Format("2006-01-02")
	}
	if zh, _ := s.TagDecoded["chn_sub"].(bool); zh || s.HasChineseSub {
		ri.Tags.Zh = true
	}
	return ri
}

// offline 提交后是不是 115 离线（扣离线配额、只能整包下）：磁力 / ed2k，
// 包括 RE0 要先解锁的 ED2K 资源（Action 是 unlock，解锁出来的是 ed2k 链接）
func (it ResourceItem) offline() bool {
	return it.Action == "offline" || (it.Action == "unlock" && (it.Kind == "ed2k" || it.Kind == "magnet"))
}

// ==================== 归一：类型 / 标签 / 相关性 / 排序 ====================

// resKindOf 按链接形态定类型与动作。panHint 是来源自己给的网盘类型（盘搜的 cloud_type）
func resKindOf(link, panHint string) (kind, action, pan string) {
	switch classifyLink(link) {
	case "share":
		return "share115", "transfer", ""
	case "magnet":
		return "magnet", "offline", ""
	case "ed2k":
		return "ed2k", "offline", ""
	}
	if panHint == "" || panHint == "115" {
		panHint = resPanOfHost(link)
	}
	return "pan", "open", panHint
}

// resPanOfHost 其他网盘按域名认个名字，只为界面上的类型标签
func resPanOfHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, p := range []struct{ domain, pan string }{
		{"baidu.com", "baidu"}, {"xunlei.com", "xunlei"}, {"uc.cn", "uc"}, {"189.cn", "tianyi"},
		{"139.com", "mobile"}, {"weiyun.com", "weiyun"}, {"lanzou", "lanzou"},
	} {
		if strings.Contains(host, p.domain) {
			return p.pan
		}
	}
	return ""
}

var (
	reResZh     = regexp.MustCompile(`(?i)中字|中文字幕|简繁|简体|繁体|简中|繁中|双语|中英|官中|内封|内嵌|\bchs\b|\bcht\b`)
	reResSeason = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})(?:\s*-\s*s?(\d{1,2}))?(?:e\d|[^a-z0-9]|$)`)
	reResSeaCn  = regexp.MustCompile(`第\s*([0-9一二三四五六七八九十]{1,3})\s*(?:-|至|~)?\s*([0-9一二三四五六七八九十]{0,3})\s*季`)
	reResFull   = regexp.MustCompile(`(?i)全集|合集|全\d+季|complete`)
	reResYear   = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
	reResSize   = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(T|G|M|K)(?:i?B)?(?:[^A-Za-z]|$)`)
	reResAgo    = regexp.MustCompile(`([\d.]+)\s*(秒|分钟|小时|天|周|个?月|年)前?`)
)

// resTagsOf 标题 → 画质标签
func resTagsOf(title string) resTags {
	ri := ParseResourceInfo(title)
	t := resTags{
		Pix: ri.Pix, Type: ri.Type, Effect: ri.Effect, Video: ri.VideoEncode,
		Audio: ri.AudioEncode, Team: ri.Team, Zh: reResZh.MatchString(title),
	}
	if t.Pix == "" {
		// 中文资源名常写「4K」「1080P」夹在汉字中间，\b 边界认不出来
		lower := strings.ToLower(title)
		switch {
		case strings.Contains(lower, "2160") || strings.Contains(lower, "4k"):
			t.Pix = "2160p"
		case strings.Contains(lower, "1080"):
			t.Pix = "1080p"
		case strings.Contains(lower, "720p"):
			t.Pix = "720p"
		}
	}
	t.Season = resSeasonOf(title)
	return t
}

// resMergeTags 以 base 为准，空着的字段用 extra 补
func resMergeTags(base, extra resTags) resTags {
	fill := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	fill(&base.Pix, extra.Pix)
	fill(&base.Type, extra.Type)
	fill(&base.Effect, extra.Effect)
	fill(&base.Video, extra.Video)
	fill(&base.Audio, extra.Audio)
	fill(&base.Team, extra.Team)
	fill(&base.Season, extra.Season)
	base.Zh = base.Zh || extra.Zh
	return base
}

// resSeasonOf 标题里写的季：S01 / S01-S03 / 全集。只给剧集的季筛选用，认不出就空着
func resSeasonOf(title string) string {
	if m := reResSeason.FindStringSubmatch(title); m != nil {
		a, _ := strconv.Atoi(m[1])
		if m[2] != "" {
			if b, _ := strconv.Atoi(m[2]); b > a {
				return fmt.Sprintf("S%02d-S%02d", a, b)
			}
		}
		return fmt.Sprintf("S%02d", a)
	}
	if m := reResSeaCn.FindStringSubmatch(title); m != nil {
		a := cnOrArabic(m[1])
		if b := cnOrArabic(m[2]); b > a {
			return fmt.Sprintf("S%02d-S%02d", a, b)
		}
		if a > 0 {
			return fmt.Sprintf("S%02d", a)
		}
	}
	if reResFull.MatchString(title) {
		return "全集"
	}
	return ""
}

// cnOrArabic 「12」「十二」都转成 12；认不出返回 0
func cnOrArabic(s string) int {
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	n, cur := 0, 0
	for _, r := range s {
		if r == '十' {
			if cur == 0 {
				cur = 1
			}
			n += cur * 10
			cur = 0
			continue
		}
		cur = digits[r]
	}
	return n + cur
}

// resParseSize 「10.49G」「1.5 GB」→ 字节数
func resParseSize(s string) int64 {
	m := reResSize.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	mul := map[string]float64{"T": 1 << 40, "G": 1 << 30, "M": 1 << 20, "K": 1 << 10}[strings.ToUpper(m[2])]
	return int64(f * mul)
}

// resParseTime 各来源的时间写法：RFC3339、日期、「3 小时前」→ unix 秒；认不出返回 0
func resParseTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if t.Year() < 2000 { // 盘搜对没有时间的条目回 0001-01-01
				return 0
			}
			return t.Unix()
		}
	}
	switch s { // 观影的更新时间
	case "今天", "刚刚":
		return time.Now().Unix()
	case "昨天":
		return time.Now().Add(-24 * time.Hour).Unix()
	case "前天":
		return time.Now().Add(-48 * time.Hour).Unix()
	}
	if m := reResAgo.FindStringSubmatch(s); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		unit := map[string]float64{"秒": 1, "分钟": 60, "小时": 3600, "天": 86400, "周": 7 * 86400, "月": 30 * 86400, "个月": 30 * 86400, "年": 365 * 86400}[m[2]]
		if unit > 0 {
			return time.Now().Add(-time.Duration(f*unit) * time.Second).Unix()
		}
	}
	return 0
}

// allNames 比对用的全部片名：标题、原名、TMDB 别名与译名
func (q resQuery) allNames() []string {
	return append([]string{q.Title, q.OrigTitle}, q.names...)
}

// resNameUsable 归一化后的片名够不够长，能不能拿来做「包含」判断。
// 与 looseTitleMatch 同一个门槛：两个汉字、四个字母。《Up》《Her》这种短英文名
// 拿去包含比对会命中一大片无关资源
func resNameUsable(key string) bool {
	n := len([]rune(key))
	for _, r := range key {
		if r > unicode.MaxASCII {
			return n >= 2
		}
	}
	return n >= 4
}

// resRelevant 资源标题是不是这部片。盘搜、观影都是拿片名做关键词搜，同名、合集、
// 沾边的内容都会混进来（p115strmhelper 对 TG 结果也做了同样的比对）。
//   - 任一可用片名（标题 / 原名 / 别名 / 译名）归一化后被资源标题包含，才算这部片；
//   - 电影再看年份：标题里写了年份、且没有一个与 TMDB 年份相差 1 年以内的，不算；
//     剧集各季年份不同，不看年份；
//   - 没有任何可用片名（片名太短）时无从判断，一律算相关，交给人挑。
func resRelevant(q resQuery, title string) bool {
	if q.TmdbID == 0 {
		return true
	}
	key := titleKey(title)
	checked, matched := false, false
	for _, n := range q.allNames() {
		nk := titleKey(n)
		if !resNameUsable(nk) {
			continue
		}
		checked = true
		if strings.Contains(key, nk) {
			matched = true
			break
		}
	}
	if !checked {
		return true
	}
	if !matched {
		return false
	}
	if q.Type != "movie" || q.Year == "" {
		return true
	}
	want, err := strconv.Atoi(q.Year)
	if err != nil {
		return true
	}
	years := reResYear.FindAllStringSubmatch(title, -1)
	if len(years) == 0 {
		return true
	}
	for _, y := range years {
		if got, _ := strconv.Atoi(y[1]); got-want <= 1 && want-got <= 1 {
			return true
		}
	}
	return false
}

// resWashRank 按洗版策略的优先级规则给资源排序：命中第几条（0 最优），都没命中 -1。
// 用户在洗版 YAML 里写过「更想要哪种版本」，找资源时正好拿来用。
// 搜索时还不知道会整理进哪个分类，先找不限分类的策略，没有就退到同类型的第一条
func resWashRank(mediaType, title string) int {
	st := washStrategyForType(mediaType)
	if st == nil {
		return -1
	}
	return washTier(title, st.PriorityLevel)
}

// washStrategyForType 还不知道分类时用哪条洗版策略：不限分类的，没有就同类型的第一条
func washStrategyForType(mediaType string) *washStrategy {
	if st := matchWashStrategy(mediaType, ""); st != nil {
		return st
	}
	for _, s := range washStrategyCache() {
		if s.MediaType == "" || s.MediaType == mediaType {
			return &s
		}
	}
	return nil
}

// resNormalize 统一补齐标签、相关性、洗版排名，并过滤掉进不了 115 的资源
func resNormalize(q resQuery, items []ResourceItem) []ResourceItem {
	out := items[:0]
	for _, it := range items {
		// 其他网盘（百度 / 迅雷 / UC …）只能打开原链接手动处理，本站转存不了，
		// 维护者要求不列（2026-10-14）；RE0 上非 115 的资源解锁了也一样没用
		if it.Kind == "pan" || removedCloudResource(it.Pan, it.URL) {
			continue
		}
		// 来源自己给的标签（RE0 的字幕语言、TG 的话题标签）只补标题里解析不出来的
		it.Tags = resMergeTags(resTagsOf(it.Title), it.Tags)
		if it.SizeBytes == 0 {
			if it.Size == "" {
				// 盘搜、不太灵不给大小，标题里常写着「[12.3G]」
				if m := reResSize.FindString(it.Title); m != "" && resParseSize(m) >= 100<<20 {
					it.Size = strings.TrimRight(strings.TrimSpace(m), "[]()（）|/")
				}
			}
			it.SizeBytes = resParseSize(it.Size)
		}
		it.Relevant = resByTmdb(it.Source) || resRelevant(q, it.Title) // RE0 / 癫影本来就是按 TMDB 条目查的
		it.Rank = resWashRank(q.Type, it.Title)
		out = append(out, it)
	}
	return out
}

// resMarkSubmitted 来源链接台账里有的，标上提交时间（只查库，不请求 115）
func resMarkSubmitted(db *gorm.DB, items []ResourceItem) {
	if db == nil {
		return
	}
	idx := map[string][]int{}
	var hashes []string
	for i, it := range items {
		if it.URL == "" {
			continue
		}
		hash := linkHashOf(it.URL)
		if hash == "" {
			continue
		}
		if _, ok := idx[hash]; !ok {
			hashes = append(hashes, hash)
		}
		idx[hash] = append(idx[hash], i)
	}
	if len(hashes) == 0 {
		return
	}
	var rows []model.DownloadLink
	db.Where("hash IN ?", hashes).Find(&rows)
	for _, r := range rows {
		for _, i := range idx[r.Hash] {
			if at := r.CreatedAt.Unix(); at > items[i].SubmittedAt {
				items[i].SubmittedAt = at
			}
		}
	}
}

// resPixScore 分辨率排序分
func resPixScore(pix string) int {
	switch {
	case strings.HasPrefix(pix, "4320"):
		return 5
	case strings.HasPrefix(pix, "2160"):
		return 4
	case strings.HasPrefix(pix, "1080"):
		return 3
	case strings.HasPrefix(pix, "720"):
		return 2
	case pix != "":
		return 1
	}
	return 0
}

// resSortDefault 默认顺序（前端「推荐」排序同口径，见 webui 的 transferSort.ts）：
// 相关的在前 → 能直接处理的在前（打开原链接的沉底）→ 洗版规则命中越靠前越好 →
// 分辨率 → 做种 → 时间新的在前
func resSortDefault(items []ResourceItem) {
	rank := func(r int) int {
		if r < 0 {
			return 1 << 20
		}
		return r
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Relevant != b.Relevant {
			return a.Relevant
		}
		if (a.Action == "open") != (b.Action == "open") {
			return b.Action == "open"
		}
		if rank(a.Rank) != rank(b.Rank) {
			return rank(a.Rank) < rank(b.Rank)
		}
		if pa, pb := resPixScore(a.Tags.Pix), resPixScore(b.Tags.Pix); pa != pb {
			return pa > pb
		}
		if a.Seeds != b.Seeds {
			return a.Seeds > b.Seeds
		}
		return a.TimeUnix > b.TimeUnix
	})
}

// ==================== 搜索入口（网页与机器人共用） ====================

// resQueryFor 补上 TMDB 别名与译名（30 分钟缓存，见 detailOf）。取不到不影响搜索，只是相关性判定少几个名字
func resQueryFor(q resQuery) resQuery {
	if q.TmdbID == 0 || (q.Type != "movie" && q.Type != "tv") {
		return q
	}
	if tc, err := loadTmdbClient(); err == nil {
		if d, err := tc.detailOf(q.Type, q.TmdbID); err == nil && d != nil {
			q.names = d.Names
		}
	}
	return q
}

// resCache 同一部片短时间内重复打开（点错了退回来、换个筛选）不再打一遍资源站
var (
	resCacheMu sync.Mutex
	resCache   = map[string]resCacheEntry{}
)

type resCacheEntry struct {
	r  resResult
	at time.Time
}

const resCacheTTL = 10 * time.Minute

// searchResources 搜一个来源并归一。refresh=true 绕过缓存
func searchResources(h *Handler, key string, q resQuery, refresh bool) (resResult, error) {
	src := resSourceOf(key)
	if src == nil {
		return resResult{}, fmt.Errorf("未知的来源：%s", key)
	}
	if why := src.ready(); why != "" {
		return resResult{}, fmt.Errorf("%s", why)
	}
	ck := fmt.Sprintf("%s|%s|%d|%s|%s", key, q.Type, q.TmdbID, q.Title, q.Year)
	if !refresh {
		resCacheMu.Lock()
		e, ok := resCache[ck]
		resCacheMu.Unlock()
		if ok && time.Since(e.at) < resCacheTTL {
			r := resResult{Items: append([]ResourceItem(nil), e.r.Items...), Note: e.r.Note}
			resMarkSubmitted(h.DB, r.Items) // 提交状态每次现查：刚提交完回来要能看到
			return r, nil
		}
	}
	q = resQueryFor(q)
	r, err := src.search(h, q)
	if err != nil {
		return resResult{}, err
	}
	r.Items = resNormalize(q, r.Items)
	resSortDefault(r.Items)
	resCacheMu.Lock()
	if len(resCache) > 200 {
		resCache = map[string]resCacheEntry{}
	}
	resCache[ck] = resCacheEntry{r: resResult{Items: append([]ResourceItem(nil), r.Items...), Note: r.Note}, at: time.Now()}
	resCacheMu.Unlock()
	resMarkSubmitted(h.DB, r.Items)
	log.Printf("[影视转存] ▣ %s「%s」：%d 条", src.Label, q.Title, len(r.Items))
	return r, nil
}

// ==================== 提交（网页与机器人共用） ====================

// resSubmitReq 提交一条资源。Source 为空表示用户直接贴的链接
type resSubmitReq struct {
	Source  string `json:"source"`
	Action  string `json:"action"`
	URL     string `json:"url"`
	Code    string `json:"code"`
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	Confirm bool   `json:"confirm"` // RE0 解锁花积分：界面二次确认后才带 true
	// Points 列表上标的解锁积分（nil = 不知道）。只用来在日志里写清楚花没花积分：
	// 网页通道里免费资源同样要点「确认解锁」，光看点没点按钮分不出来
	Points *int `json:"points,omitempty"`
	// Subdir 转存到哪个绑定了媒体库的转存子目录（cid）；空 = 转存目录本身（stagingsub.go）
	Subdir string `json:"subdir"`
}

type resSubmitResult struct {
	Message string `json:"message"`
	OpenURL string `json:"open_url,omitempty"` // 非 115 的链接：交给用户自己打开
	Code    string `json:"code,omitempty"`
}

// submitResource 统一提交出口。submitter 写进来源链接台账的「经谁提交」：
// 网页上为空，取来源名（观影 / 盘搜…）；机器人传「机器人」。
// 转存目录里的内容一律交给自动整理：分享转存 / 离线下载的核心函数都带 organize=true，
// 关掉也没意义 —— 守望者每分钟看一次转存目录，有东西就入队整理
func (h *Handler) submitResource(req resSubmitReq, submitter string) (resSubmitResult, error) {
	label := submitter
	if label == "" {
		label = "web"
		if src := resSourceOf(req.Source); src != nil {
			label = src.Label
		}
	}
	// 目标目录先于解锁解析：RE0 解锁会花积分，子目录失效要在花之前报出来
	target, err := h.stagingTarget(req.Subdir)
	if err != nil {
		return resSubmitResult{}, err
	}
	rl, err := h.resolveResourceLink(req)
	if err != nil {
		return resSubmitResult{}, err
	}

	switch rl.Action {
	case "transfer":
		// 直接贴的链接（req.Source 为空）才可能分享出去；从资源站选的不分享（shareout.go）
		msg, ok, fail, err := h.shareReceiveOpt(rl.URL, rl.Code, target, label, true, req.Source == "", false)
		if rl.Unlocked {
			if err != nil {
				// 积分已经花了：把链接交回去，用户还能自己转存
				return resSubmitResult{Message: "已解锁，但转存失败：" + err.Error(), OpenURL: rl.URL, Code: rl.Code}, nil
			}
			if ok == 0 && fail == 0 {
				return resSubmitResult{Message: "已解锁，但分享为空", OpenURL: rl.URL, Code: rl.Code}, nil
			}
			return resSubmitResult{Message: "已解锁并转存：" + msg}, nil
		}
		if err != nil {
			return resSubmitResult{}, err
		}
		if ok == 0 && fail == 0 {
			return resSubmitResult{Message: "分享为空，没有可转存的内容"}, nil
		}
		return resSubmitResult{Message: msg + "，完成后自动整理入库"}, nil

	case "offline":
		if len(rl.More) > 0 {
			// 癫影 / RE0 一条离线资源常是一批 ed2k（一集一条）：逐条提交，一条一个离线任务。
			// 之前提交过的（115 报任务已存在）跳过、接着提交别的 —— 原来第一条撞上就整批返回，后面几集一条没提交
			return h.submitOfflineBatch(append([]string{rl.URL}, rl.More...), target, label), nil
		}
		if status, msg := h.offlineSubmitCore(rl.URL, target, label, true); status != http.StatusOK {
			if rl.Unlocked {
				// 积分已经花了：把链接交回去，用户还能自己提交离线
				return resSubmitResult{Message: "已解锁，但提交离线失败：" + msg, OpenURL: rl.URL}, nil
			}
			return resSubmitResult{}, fmt.Errorf("%s", msg)
		}
		if rl.Unlocked {
			return resSubmitResult{Message: "已解锁并提交 115 离线下载，完成后自动整理入库"}, nil
		}
		return resSubmitResult{Message: "已提交 115 离线下载，完成后自动整理入库"}, nil

	case "open":
		if rl.Unlocked {
			return resSubmitResult{Message: "已解锁，不是 115 分享，请手动打开", OpenURL: rl.URL, Code: rl.Code}, nil
		}
		return resSubmitResult{Message: "请手动打开链接", OpenURL: rl.URL, Code: rl.Code}, nil
	}
	return resSubmitResult{}, fmt.Errorf("不支持的操作：%s", rl.Action)
}

// submitOfflineBatch 一次解锁出的一批离线链接逐条提交（只在解锁之后用：积分已经花了，失败的链接要交回去）
func (h *Handler) submitOfflineBatch(links []string, target, label string) resSubmitResult {
	var failed []string
	ok, dup := 0, 0
	for _, l := range links {
		switch status, msg := h.offlineSubmitCore(l, target, label, true); status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			dup++
			log.Printf("[资源] ○ 115 上已有这条离线任务，跳过（%s）", truncateStr(l, 60))
		default:
			failed = append(failed, l)
			log.Printf("[资源] ✗ 离线提交失败（%s）：%s", truncateStr(l, 60), msg)
		}
	}
	parts := []string{fmt.Sprintf("已解锁 %d 条链接", len(links))}
	if ok > 0 {
		parts = append(parts, fmt.Sprintf("提交 %d 个 115 离线任务", ok))
	}
	if dup > 0 {
		parts = append(parts, fmt.Sprintf("%d 条之前提交过、115 上已有任务", dup))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d 条提交失败（链接附在下面，可以手动提交）", len(failed)))
		return resSubmitResult{Message: strings.Join(parts, "，"), OpenURL: strings.Join(failed, "\n")}
	}
	msg := strings.Join(parts, "，")
	if ok > 0 {
		msg += "，完成后自动整理入库"
	}
	return resSubmitResult{Message: msg}
}

// resLink 一条资源最终落到的链接
type resLink struct {
	Action   string // transfer = 115 分享 / offline = 离线下载 / open = 交给用户自己打开
	URL      string
	Code     string   // 分享提取码
	Unlocked bool     // RE0 刚解锁出来的：积分可能已经花了，后续失败也要把链接交回去
	More     []string // 癫影 / RE0 一条离线资源解锁出好几条链接：URL 之外的那些（一条一个离线任务）
}

// resolveResourceLink 把一条资源换成能直接用的链接：直接贴的链接按形态分流、观影详情页取磁力、RE0 解锁。
// 网页 / 机器人提交（submitResource）与订阅共用，别各写一份。
// RE0 解锁会扣积分，只认 req.Confirm=true：网页是用户二次确认过的，订阅是用户在配置里事先同意了积分上限、
// 并由订阅自己核过上限与预算之后才带上 —— 这个函数不替调用方做积分判断
func (h *Handler) resolveResourceLink(req resSubmitReq) (resLink, error) {
	link, code := strings.TrimSpace(req.URL), strings.TrimSpace(req.Code)
	if link != "" && removedCloudResource("", link) {
		return resLink{}, fmt.Errorf("本站不支持该网盘的链接")
	}
	action := req.Action
	if req.Source == "" {
		// 直接贴的链接：按链接形态分流
		switch classifyLink(link) {
		case "share":
			action = "transfer"
		case "magnet", "ed2k", "http", "ftp":
			action = "offline"
		default:
			return resLink{}, fmt.Errorf("认不出这个链接：支持 115 分享、磁力、ed2k、HTTP")
		}
	}

	switch action {
	case "transfer":
		if code == "" {
			link, code = splitShareLink(link)
		} else if u, _ := splitShareLink(link); u != "" {
			link = u
		}
		if link == "" {
			return resLink{}, fmt.Errorf("认不出 115 分享链接")
		}
		return resLink{Action: "transfer", URL: link, Code: code}, nil

	case "offline":
		if link == "" && req.Source == "gy" {
			magnet, _, err := gyFetchMagnet(req.Ref)
			if err != nil {
				return resLink{}, err
			}
			link = magnet
		}
		if link == "" {
			return resLink{}, fmt.Errorf("缺少下载链接")
		}
		return resLink{Action: "offline", URL: link}, nil

	case "unlock":
		if !resPaidSource(req.Source) || req.Ref == "" {
			return resLink{}, fmt.Errorf("参数错误")
		}
		if !req.Confirm {
			return resLink{}, fmt.Errorf("解锁会消耗%s积分，请确认后再提交", resSourceOf(req.Source).Label)
		}
		if req.Source == "dian" {
			u, err := dianUnlock(req.Ref)
			if err != nil {
				return resLink{}, fmt.Errorf("解锁失败: %w", err)
			}
			if u.Share != "" {
				l, c := splitShareLink(u.Share)
				if l == "" {
					l = u.Share
				}
				return resLink{Action: "transfer", URL: l, Code: firstNonEmpty(c, u.Code), Unlocked: true}, nil
			}
			switch classifyLink(u.Links[0]) {
			case "magnet", "ed2k", "http", "ftp":
				return resLink{Action: "offline", URL: u.Links[0], More: u.Links[1:], Unlocked: true}, nil
			}
			return resLink{Action: "open", URL: strings.Join(u.Links, "\n"), Unlocked: true}, nil
		}
		links, ucode, err := re0UnlockSlug(h, req.Ref, req.Points)
		if err != nil {
			return resLink{}, fmt.Errorf("解锁失败: %w", err)
		}
		unlocked := links[0]
		switch classifyLink(unlocked) {
		case "magnet", "ed2k":
			return resLink{Action: "offline", URL: unlocked, More: links[1:], Unlocked: true}, nil
		}
		if !is115ShareLink(unlocked) {
			return resLink{Action: "open", URL: unlocked, Code: ucode, Unlocked: true}, nil
		}
		return resLink{Action: "transfer", URL: unlocked, Code: ucode, Unlocked: true}, nil

	case "open":
		return resLink{Action: "open", URL: link, Code: code}, nil
	}
	return resLink{}, fmt.Errorf("不支持的操作：%s", action)
}

// ==================== HTTP ====================

// TransferSources GET /transfer/sources —— 来源清单（能不能用、开没开）与转存目录
func (h *Handler) TransferSources(c *gin.Context) {
	cfg := loadTransferHubCfg()
	list := make([]gin.H, 0, len(resSources))
	for _, s := range resSources {
		list = append(list, gin.H{"key": s.Key, "label": s.Label, "enabled": cfg.enabled(s.Key), "reason": s.ready()})
	}
	var share struct {
		Folder     string `json:"folder"`
		FolderPath string `json:"folder_path"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &share)
	// V2 下转存目录在库上（xfertarget.go），老的 share.folder 迁移时不自动设、多半是空的：
	// 照读它的话库里明明设好了转存文件夹，找资源页还提示「还没设置转存目录」
	if libs := loadLibraries(); len(libs) > 0 {
		share.Folder, share.FolderPath = "", ""
		if l, err := h.xferLib("", libs); err == nil {
			share.Folder = xferTargetOf(l)
			share.FolderPath = firstNonEmpty(l.StagingPath, "cid "+l.StagingCid)
		} else if errors.Is(err, errXferNeedLib) {
			// 多个库、没设默认：每次转存时选，不算没设置
			share.FolderPath = "转存时选库"
		}
	}
	c.JSON(http.StatusOK, gin.H{"sources": list, "folder": share.Folder, "folder_path": share.FolderPath})
}

// TransferSaveSources POST /transfer/sources {disabled:[...]}
func (h *Handler) TransferSaveSources(c *gin.Context) {
	var req transferHubCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var keep []string
	for _, d := range req.Disabled {
		if resSourceOf(d) != nil {
			keep = append(keep, d)
		}
	}
	req.Disabled = keep
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting("transfer_hub", string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	// 打开 RE0 参与搜索、又配了网页登录：检查本地有没有浏览器，没有就下（已有 / 下载中不重复）
	re0On := true
	for _, d := range req.Disabled {
		re0On = re0On && d != "re0"
	}
	if re0On && re0WebConfigured(loadRe0Cfg()) {
		ensureCloak("启用 RE0 来源")
	}
	if !slices.Contains(req.Disabled, "dian") && dianConfigured(loadDianCfg()) {
		ensureCloak("启用癫影来源")
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// TransferResources GET /transfer/resources/:source?tmdb_id=&type=&title=&original_title=&year=&refresh=1
// 一个来源的搜索结果。来源不可用（没登录 / 没配置）回 200 + unavailable，
// 让前端在来源条上写明原因，而不是当成搜索失败弹错误
func (h *Handler) TransferResources(c *gin.Context) {
	key := c.Param("source")
	src := resSourceOf(key)
	if src == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未知的来源"})
		return
	}
	q := resQuery{
		Type: strings.TrimSpace(c.Query("type")), Title: strings.TrimSpace(c.Query("title")),
		OrigTitle: strings.TrimSpace(c.Query("original_title")), Year: strings.TrimSpace(c.Query("year")),
	}
	q.TmdbID, _ = strconv.Atoi(c.Query("tmdb_id"))
	if q.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少片名"})
		return
	}
	if why := src.ready(); why != "" {
		c.JSON(http.StatusOK, gin.H{"items": []ResourceItem{}, "unavailable": why})
		return
	}
	r, err := searchResources(h, key, q, c.Query("refresh") == "1")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if r.Items == nil {
		r.Items = []ResourceItem{}
	}
	c.JSON(http.StatusOK, gin.H{"items": r.Items, "note": r.Note})
}

// TransferSubmit POST /transfer/submit
func (h *Handler) TransferSubmit(c *gin.Context) {
	var req resSubmitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Source != "" && resSourceOf(req.Source) == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未知的来源"})
		return
	}
	if req.URL == "" && req.Ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写链接"})
		return
	}
	r, err := h.submitResource(req, "")
	if errors.Is(err, errXferNeedLib) {
		// 多个库、没设默认：界面弹「转存到哪个库」再提交一次（xfertarget.go）
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "need_lib": true})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

// TransferOwned GET /transfer/owned?keys=movie:1,tv:2 —— 哪些条目已经整理进库（只查本地台账）
func (h *Handler) TransferOwned(c *gin.Context) {
	out := gin.H{}
	var movieIDs, tvIDs []int
	for _, k := range strings.Split(c.Query("keys"), ",") {
		kind, id, ok := strings.Cut(strings.TrimSpace(k), ":")
		n, err := strconv.Atoi(id)
		if !ok || err != nil || n <= 0 {
			continue
		}
		switch kind {
		case "movie":
			movieIDs = append(movieIDs, n)
		case "tv":
			tvIDs = append(tvIDs, n)
		}
	}
	for kind, ids := range map[string][]int{"movie": movieIDs, "tv": tvIDs} {
		if len(ids) == 0 {
			continue
		}
		var rows []model.MediaLibrary
		h.DB.Where("media_type = ? AND tmdb_id IN ?", kind, ids).Find(&rows)
		for _, r := range mediaLibOwned(h.DB, rows) {
			out[fmt.Sprintf("%s:%d", kind, r.TmdbID)] = gin.H{"title": r.Title, "category": r.Category, "at": r.CreatedAt.Unix()}
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// ==================== 机器人 ====================

// resBotLabel 机器人列表里的一行：[来源·类型] 标题 | 画质 | 大小 | 做种 | 时间。
// withSource：几个来源混在一个列表里时带上来源名
func resBotLabel(it ResourceItem, width int, withSource bool) string {
	kind := map[string]string{"share115": "115 分享", "magnet": "磁力", "ed2k": "ed2k"}[it.Kind]
	if kind == "" {
		kind = pansouTypeLabel(it.Pan)
		if kind == "" {
			kind = "其他网盘"
		}
	}
	if withSource {
		src := botSourceLabel(it.Source)
		if it.Via != "" {
			src = "TG" + it.Via // 频道名比「TG 频道」有用：TG@频道
		}
		kind = src + "·" + kind
	}
	line := "[" + kind + "] " + truncateStr(it.Title, width)
	var meta []string
	if q := resTagLine(it.Tags); q != "" {
		meta = append(meta, q)
	}
	if it.Size != "" {
		meta = append(meta, it.Size)
	}
	if it.Seeds > 0 {
		meta = append(meta, fmt.Sprintf("做种 %d", it.Seeds))
	}
	if it.Code != "" {
		meta = append(meta, "提取码 "+it.Code)
	}
	if it.Time != "" {
		meta = append(meta, it.Time)
	}
	if resPaidSource(it.Source) {
		switch {
		case it.Owned:
			meta = append(meta, "已解锁")
		case it.Points != nil && *it.Points == 0:
			meta = append(meta, "免费")
		case it.Points != nil:
			meta = append(meta, fmt.Sprintf("%d 积分·网页解锁", *it.Points))
		}
	}
	if it.SubmittedAt > 0 {
		meta = append(meta, "提交过")
	}
	if len(meta) > 0 {
		line += " | " + strings.Join(meta, " | ")
	}
	return line
}

// resTagLine 画质标签连成一串：4K·DV·中字
func resTagLine(t resTags) string {
	var parts []string
	switch resPixScore(t.Pix) {
	case 5:
		parts = append(parts, "8K")
	case 4:
		parts = append(parts, "4K")
	case 3:
		parts = append(parts, "1080P")
	case 2:
		parts = append(parts, "720P")
	}
	if t.Effect != "" {
		parts = append(parts, t.Effect)
	}
	if strings.EqualFold(t.Type, "REMUX") {
		parts = append(parts, "原盘")
	}
	if t.Zh {
		parts = append(parts, "中字")
	}
	return strings.Join(parts, "·")
}

// re0BotFree 机器人能不能直接解锁（RE0 / 癫影）：解锁过的、或者明确 0 积分的（积分不知道的不算）
func re0BotFree(it ResourceItem) bool {
	return resPaidSource(it.Source) && (it.Owned || (it.Points != nil && *it.Points == 0))
}

// botSubmitResource 机器人选中一条资源后的处理，回复文案按结果分；返回是否提交成功。
// paid：用户在机器人里确认过花积分（只有 RE0 付费解锁用得到）
func (h *Handler) botSubmitResource(it ResourceItem, reply func(...string), paid bool) bool {
	if it.Action == "open" {
		out := "此资源不是 115 分享，请手动打开：\n" + it.URL
		if it.Code != "" {
			out += "\n提取码：" + it.Code
		}
		reply(out)
		return true
	}
	switch it.Action {
	case "transfer":
		reply("⏳ 正在转存 115…")
	case "offline":
		if it.URL == "" {
			reply("⏳ 提取磁力链接并提交 115 离线下载…")
		} else {
			reply("⏳ 提交 115 离线下载…")
		}
	case "unlock":
		then := "转存 115…"
		if it.offline() {
			then = "提交 115 离线下载…"
		}
		src := botSourceLabel(it.Source)
		switch {
		case re0BotFree(it):
			reply("⏳ 解锁" + src + "（免费 / 之前解锁过，不花积分）并" + then)
		case paid:
			reply("⏳ 解锁" + src + "（花积分）并" + then)
		default:
			// 没经过确认屏走到这里（不该发生）：不花积分
			reply("这条" + src + "资源解锁要花积分，请重新点它，确认后再解锁。")
			return false
		}
	}
	// 免费 / 解锁过的、或用户在确认屏点了确认的，才带 confirm（resolveResourceLink 只认带 confirm 的解锁请求）
	r, err := h.submitResource(resSubmitReq{Source: it.Source, Action: it.Action, URL: it.URL, Code: it.Code, Ref: it.Ref, Title: it.Title,
		Confirm: it.Action == "unlock" && (re0BotFree(it) || paid), Points: it.Points}, "机器人")
	if err != nil {
		if it.Action == "transfer" {
			reply("✗ 转存失败（部分内容可能已转存，请先看一眼网盘转存目录再决定是否重试）：" + err.Error())
			return false
		}
		reply("✗ 提交失败：" + err.Error())
		return false
	}
	reply("✓ " + r.Message)
	return true
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
