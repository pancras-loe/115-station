package api

// 企微机器人「观影」搜索链路（带会话状态的多轮交互）：
//   观影 <片名> → TMDB 多结果选单 → 回复序号选片
//   → 观影站搜种子（标注 大小/做种/中字/4K 等分类）→ 回复序号选资源
//   → 详情页提取磁力 → 提交 115 离线下载（完成后自动整理入库）
// 会话按企微用户隔离，5 分钟过期；序号回复只在有会话时拦截，
// 不影响既有指令（如 "搜索 x"）。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type wecomTmdbHit struct {
	ID     int
	Type   string // movie / tv
	Title  string
	Year   string
	Vote   float64
	Poster string // TMDB poster_path（/xx.jpg）
}

type wecomGySession struct {
	Stage    string // movie=待选片名 resource=待选种子
	Keyword  string
	Movies   []wecomTmdbHit
	Torrents []gin.H
	At       time.Time
}

const wecomGySessionTTL = 5 * time.Minute

var (
	wecomGyMu       sync.Mutex
	wecomGySessions = map[string]*wecomGySession{}
)

func wecomGySessionGet(user string) *wecomGySession {
	wecomGyMu.Lock()
	defer wecomGyMu.Unlock()
	s, ok := wecomGySessions[user]
	if !ok || time.Since(s.At) > wecomGySessionTTL {
		delete(wecomGySessions, user)
		return nil
	}
	return s
}

func wecomGySessionSet(user string, s *wecomGySession) {
	wecomGyMu.Lock()
	defer wecomGyMu.Unlock()
	for u, v := range wecomGySessions {
		if time.Since(v.At) > wecomGySessionTTL {
			delete(wecomGySessions, u)
		}
	}
	wecomGySessions[user] = s
}

// wecomTmdbMulti TMDB 多结果搜索（纯数字按 ID 直查详情）
func (h *Handler) wecomTmdbMulti(q string) ([]wecomTmdbHit, error) {
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	type hit struct {
		ID     int     `json:"id"`
		Type   string  `json:"media_type"`
		Title  string  `json:"title"`
		Name   string  `json:"name"`
		Date   string  `json:"release_date"`
		First  string  `json:"first_air_date"`
		Vote   float64 `json:"vote_average"`
		Poster string  `json:"poster_path"`
	}
	toHit := func(id int, kind, title, name, date, first string, vote float64, poster string) wecomTmdbHit {
		t, d := title, date
		if t == "" {
			t = name
		}
		if d == "" {
			d = first
		}
		year := ""
		if len(d) >= 4 {
			year = d[:4]
		}
		return wecomTmdbHit{ID: id, Type: kind, Title: t, Year: year, Vote: vote, Poster: poster}
	}
	if regexpPureDigits.MatchString(q) {
		for _, kind := range []string{"movie", "tv"} {
			body, err := tc.get("/"+kind+"/"+q, nil)
			if err != nil {
				continue
			}
			var d hit
			if json.Unmarshal(body, &d) != nil || d.ID == 0 {
				continue
			}
			return []wecomTmdbHit{toHit(d.ID, kind, d.Title, d.Name, d.Date, d.First, d.Vote, d.Poster)}, nil
		}
		return nil, nil
	}
	// multi 搜索按热度排序，热门片名常被电影刷屏；
	// 再单独拉一次剧集搜索，按「2 部电影 + 1 部剧集」穿插，保证剧集有露出
	body, err := tc.get("/search/multi", map[string]string{"query": q, "include_adult": "false"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Results []hit `json:"results"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, nil
	}
	conv := func(it hit) wecomTmdbHit {
		return toHit(it.ID, it.Type, it.Title, it.Name, it.Date, it.First, it.Vote, it.Poster)
	}
	seen := map[int]bool{}
	var movies, tvs []wecomTmdbHit
	for _, it := range r.Results {
		if it.Type != "movie" && it.Type != "tv" {
			continue // multi-search 会混入 person
		}
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		if it.Type == "tv" {
			tvs = append(tvs, conv(it))
		} else {
			movies = append(movies, conv(it))
		}
	}
	// 剧集条目不足时单独补一次 /search/tv
	tvInList := 0
	for _, m := range tvs {
		if m.ID != 0 {
			tvInList++
		}
	}
	if tvInList < 2 {
		if tvb, err := tc.get("/search/tv", map[string]string{"query": q, "include_adult": "false"}); err == nil {
			var tr struct {
				Results []hit `json:"results"`
			}
			if json.Unmarshal(tvb, &tr) == nil {
				for _, it := range tr.Results {
					if seen[it.ID] {
						continue
					}
					seen[it.ID] = true
					tvs = append(tvs, conv(it))
					if len(tvs) >= 4 {
						break
					}
				}
			}
		}
	}
	// 穿插：每 2 部电影后插 1 部剧集
	var out []wecomTmdbHit
	mi, ti := 0, 0
	for mi < len(movies) || ti < len(tvs) {
		for k := 0; k < 2 && mi < len(movies); k++ {
			out = append(out, movies[mi])
			mi++
		}
		if ti < len(tvs) {
			out = append(out, tvs[ti])
			ti++
		}
		if len(out) >= 8 {
			break
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out, nil
}

// gyBotTags 从种子标题提取分类标签（中字/4K/1080P/原盘/HDR/杜比/HEVC）
func gyBotTags(title string) string {
	lower := strings.ToLower(title)
	var tags []string
	add := func(cond bool, s string) {
		if cond {
			tags = append(tags, s)
		}
	}
	add(strings.Contains(lower, "中字") || strings.Contains(lower, "简体") || strings.Contains(lower, "繁体") || strings.Contains(lower, "chs") || strings.Contains(lower, "cht"), "中字")
	add(strings.Contains(lower, "4k") || strings.Contains(lower, "2160"), "4K")
	add(strings.Contains(lower, "1080"), "1080P")
	add(strings.Contains(lower, "原盘") || strings.Contains(lower, "remux"), "原盘")
	add(strings.Contains(lower, "杜比") || strings.Contains(lower, "dolby"), "杜比")
	add(regexp.MustCompile(`dv|dolby vision|dovi`).MatchString(lower), "DV")
	add(strings.Contains(lower, "hdr"), "HDR")
	add(strings.Contains(lower, "hevc") || strings.Contains(lower, "h265") || strings.Contains(lower, "h.265") || strings.Contains(lower, "x265"), "HEVC")
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, "·") + "]"
}

func voteSuffix(v float64) string {
	if v > 0 {
		return fmt.Sprintf(" · %.1f分", v)
	}
	return ""
}

// gyBotBucket 种子主分类桶（用于分组展示）
func gyBotBucket(title string) string {
	lower := strings.ToLower(title)
	has := func(ss ...string) bool {
		for _, s := range ss {
			if strings.Contains(lower, s) {
				return true
			}
		}
		return false
	}
	zh := has("中字", "简体", "繁体", "chs", "cht")
	uhd := has("4k", "2160")
	fhd := has("1080")
	remux := has("原盘", "remux")
	switch {
	case zh && uhd:
		return "中字 4K"
	case zh && fhd:
		return "中字 1080P"
	case zh:
		return "中字"
	case remux:
		return "原盘"
	case uhd:
		return "4K"
	case fhd:
		return "1080P"
	}
	return "其他"
}

var gyBotBucketOrder = []string{"中字 4K", "中字 1080P", "中字", "原盘", "4K", "1080P", "其他"}

// gyBotGroupedList 分组输出种子列表（编号为全局连续，回复序号即可）
func gyBotGroupedList(torrents []gin.H) []string {
	var lines []string
	n := 0
	for _, bucket := range gyBotBucketOrder {
		var idx []int
		for i, t := range torrents {
			title, _ := t["title"].(string)
			if gyBotBucket(title) == bucket {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("【%s】共 %d 条", bucket, len(idx)))
		for _, i := range idx {
			n++
			t := torrents[i]
			title, _ := t["title"].(string)
			size, _ := t["size"].(string)
			seeds, _ := t["seeds"].(string)
			tm, _ := t["time"].(string)
			line := fmt.Sprintf("%d. %s", n, truncateStr(title, 58))
			var meta []string
			if size != "" {
				meta = append(meta, size)
			}
			if seeds != "" {
				meta = append(meta, "做种 "+seeds)
			}
			if tm != "" {
				meta = append(meta, tm)
			}
			if len(meta) > 0 {
				line += "\n     " + strings.Join(meta, " | ")
			}
			lines = append(lines, line)
		}
	}
	if n < len(torrents) {
		for i := n; i < len(torrents); i++ {
			title, _ := torrents[i]["title"].(string)
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, truncateStr(title, 58)))
		}
	}
	return lines
}

// wecomTmdbSendPickList TMDB 搜索并下发选片单（观影/盘搜机器人共用）：
// 图文海报卡片（标题带序号），企微未配置图文时回退纯文本列表。
// 返回命中条目（nil=失败或无结果，调用方不落会话）
func (h *Handler) wecomTmdbSendPickList(keyword string, reply func(...string)) []wecomTmdbHit {
	movies, err := h.wecomTmdbMulti(keyword)
	if err != nil {
		reply("✗ TMDB 搜索失败: " + err.Error() + "（确认系统配置里 TMDB 可用）")
		return nil
	}
	if len(movies) == 0 {
		reply("TMDB 未找到「" + keyword + "」")
		return nil
	}
	typName := func(kind string) string {
		if kind == "tv" {
			return "剧集"
		}
		return "电影"
	}
	lines := []string{fmt.Sprintf("TMDB 搜索「%s」，回复序号选片：", keyword)}
	var cards []NewsArticle
	for i, m := range movies {
		meta := fmt.Sprintf("(%s) [%s]", m.Year, typName(m.Type))
		if m.Vote > 0 {
			meta += fmt.Sprintf(" %.1f分", m.Vote)
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s", i+1, m.Title, meta))
		a := NewsArticle{
			Title: fmt.Sprintf("%d. %s (%s)", i+1, m.Title, m.Year),
			Desc:  typName(m.Type) + voteSuffix(m.Vote),
			Link:  fmt.Sprintf("https://www.themoviedb.org/%s/%d", m.Type, m.ID),
		}
		if m.Poster != "" {
			a.PicURL = tmdbImageBase() + "/t/p/w300" + m.Poster
		}
		cards = append(cards, a)
	}
	lines = append(lines, "（回复 1-"+strconv.Itoa(len(movies))+" 选择，5 分钟内有效）")
	if !NotifyMessageNews(cards) {
		reply(lines...)
	}
	return movies
}

// wecomHandleGySearch 「观影 <片名>」：TMDB 搜索并下发选单
func (h *Handler) wecomHandleGySearch(user, keyword string, reply func(...string)) {
	movies := h.wecomTmdbSendPickList(keyword, reply)
	if movies == nil {
		return
	}
	wecomGySessionSet(user, &wecomGySession{Stage: "movie", Keyword: keyword, Movies: movies, At: time.Now()})
}

// wecomHandleGyPick 会话进行中收到序号：按阶段分流（选片 → 选种子 → 离线）
func (h *Handler) wecomHandleGyPick(user string, n int, reply func(...string)) {
	s := wecomGySessionGet(user)
	if s == nil {
		reply("当前没有进行中的观影搜索，发送「观影 <片名>」开始。")
		return
	}
	s.At = time.Now()
	switch s.Stage {
	case "movie":
		if n < 1 || n > len(s.Movies) {
			reply(fmt.Sprintf("序号超出范围（1-%d）", len(s.Movies)))
			return
		}
		m := s.Movies[n-1]
		reply("⏳ 正在观影站搜索「" + m.Title + "」…")
		torrents, _, err := gySearchTorrents(m.Title, "")
		if err != nil {
			reply("✗ 观影搜索失败: " + err.Error())
			return
		}
		if len(torrents) == 0 {
			reply("观影站没有找到「" + m.Title + "」的资源，可重新「观影 <其他片名>」")
			return
		}
		lines := []string{fmt.Sprintf("「%s」观影资源 %d 条（序号连续，回复序号提交 115 离线）：", m.Title, len(torrents))}
		lines = append(lines, gyBotGroupedList(torrents)...)
		s.Stage = "resource"
		s.Keyword = m.Title
		s.Torrents = torrents
		reply(lines...)
	case "resource":
		if n < 1 || n > len(s.Torrents) {
			reply(fmt.Sprintf("序号超出范围（1-%d）", len(s.Torrents)))
			return
		}
		path, _ := s.Torrents[n-1]["path"].(string)
		title, _ := s.Torrents[n-1]["title"].(string)
		reply("⏳ 提取磁力链接中…")
		magnet, _, err := gyFetchMagnet(path)
		if err != nil {
			reply("✗ " + err.Error())
			return
		}
		if err := h.submitOfflineLink(magnet, "机器人"); err != nil {
			reply("✗ 离线提交失败: " + err.Error())
			return
		}
		reply("✓ 已提交 115 离线下载：", truncateStr(title, 60), "下载完成后自动整理入库并通知。")
	}
}
