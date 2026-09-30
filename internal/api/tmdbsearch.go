package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/gin-gonic/gin"
)

// ==================== TMDB 手动搜索（整理记录改指定 / 网盘文件页 / 转存页） ====================
//
// 这是给人挑条目用的搜索，宁可多给几条让人挑，也不能漏：
//   - 电影、剧集分开搜（/search/movie + /search/tv）。原来只打一次 /search/multi，
//     它把人物、电影、剧集混在同一页按热度排，中文译名命中的条目常常挤不进前几名
//     （现场：搜「三体」找不到 Netflix 的 3 Body Problem，tv/108545）；
//   - 片名里的数字换成中文数字 / 反过来再搜一遍（「3体」↔「三体」）；
//   - TMDB 的电影和剧集是两套独立编号，同一个数字两边都可能有条目，
//     只填数字时两边都查、都列出来；写明类型（tv/108545、链接）就只查那一边；
//   - 输入不是编号时，和自动整理用同一套识别：替换规则 → parseFileName（摘 id 标签、
//     年份、季集号、发布标记）→ titleCandidates（中英双名拆开）。用户常把文件名 / 目录名
//     整个粘进来（「3体.2024.{tmdbid=108545}」），原样丢给 TMDB 一条也搜不到。

// tmdbSearchMax 最多返回几条候选
const tmdbSearchMax = 20

var (
	// reTmdbTypedID 带类型的编号：TMDB 链接（…/tv/108545-3-body-problem）、tv/108545、movie:603
	reTmdbTypedID = regexp.MustCompile(`(?i)(?:^|/)(movie|tv)\s*[/:：\-]\s*(\d{1,8})(?:\D|$)`)
	// reTmdbBareID 只有编号：108545、tmdbid-108545、tmdb:108545、{tmdbid=108545}、[tmdbid-108545]
	reTmdbBareID = regexp.MustCompile(`(?i)^[\[{(]?\s*(?:tmdb(?:id)?\s*[-:=：_]?\s*)?(\d{1,8})\s*[\]})]?$`)
)

// parseTmdbIDQuery 输入是不是在直接指定 TMDB 编号。kind 为空表示没写类型（电影剧集都要查）
func parseTmdbIDQuery(q string) (id, kind string, ok bool) {
	q = strings.TrimSpace(q)
	if m := reTmdbTypedID.FindStringSubmatch(q); m != nil {
		return m[2], strings.ToLower(m[1]), true
	}
	if m := reTmdbBareID.FindStringSubmatch(q); m != nil {
		return m[1], "", true
	}
	return "", "", false
}

const tmdbCnDigits = "零一二三四五六七八九"

// tmdbQueryVariants 除原词外还要搜的写法：中文片名里的阿拉伯数字 ↔ 中文数字。
// 只对含汉字的词做：英文片名里的数字（2001、Se7en）换成中文只会搜出噪音
func tmdbQueryVariants(q string) []string {
	hasHan := false
	for _, r := range q {
		if unicode.Is(unicode.Han, r) {
			hasHan = true
			break
		}
	}
	if !hasHan {
		return nil
	}
	cn := []rune(tmdbCnDigits)
	var toCn, toArabic strings.Builder
	for _, r := range q {
		switch n := slices.Index(cn, r); {
		case r >= '0' && r <= '9':
			toCn.WriteRune(cn[r-'0'])
			toArabic.WriteRune(r)
		case n >= 0:
			toCn.WriteRune(r)
			toArabic.WriteRune(rune('0' + n))
		default:
			toCn.WriteRune(r)
			toArabic.WriteRune(r)
		}
	}
	var out []string
	for _, v := range []string{toCn.String(), toArabic.String()} {
		if v != q && (len(out) == 0 || out[0] != v) {
			out = append(out, v)
		}
	}
	return out
}

// manualCand 一条候选（字段名与前端 TmdbCandidate 对齐）
type manualCand struct {
	ID         int     `json:"id"`
	MediaType  string  `json:"media_type"`
	Title      string  `json:"title"`
	Year       string  `json:"year"`
	Poster     string  `json:"poster"`
	Vote       float64 `json:"vote,omitempty"`
	Overview   string  `json:"overview,omitempty"`
	// Original 原名：影视转存按它判断资源标题是不是这部片（英文资源名多用原名）
	Original   string  `json:"original_title,omitempty"`
	popularity float64
}

// tmdbRawItem 搜索结果 / 详情里共用的字段
type tmdbRawItem struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	PosterPath    string  `json:"poster_path"`
	VoteAverage   float64 `json:"vote_average"`
	Overview      string  `json:"overview"`
	Popularity    float64 `json:"popularity"`
}

func (r tmdbRawItem) cand(kind string) manualCand {
	title, orig, date := r.Title, r.OriginalTitle, r.ReleaseDate
	if kind == "tv" {
		title, orig, date = r.Name, r.OriginalName, r.FirstAirDate
	}
	year := ""
	if len(date) >= 4 {
		year = date[:4]
	}
	return manualCand{
		ID: r.ID, MediaType: kind, Title: title, Year: year, Poster: r.PosterPath,
		Vote: r.VoteAverage, Overview: r.Overview, Original: orig, popularity: r.Popularity,
	}
}

// tmdbNorm 比较片名用：去标点空白、转小写
func tmdbNorm(s string) string {
	return strings.ToLower(strings.ReplaceAll(cleanSearchTitle(s), " ", ""))
}

// tmdbSearchPlan 一次手动搜索要做什么：id 非空就直查编号，否则按 queries 搜片名
type tmdbSearchPlan struct {
	id, kind string // kind 为空 = 电影剧集都查
	queries  []string
	year     string // 名字里的年份：只用来排序，不当过滤条件（TMDB 的年份与发布名常差一年）
	tv       bool   // 名字里有季集号等剧集特征
}

// tmdbSearchQueryMax 片名最多搜几种写法（每种电影剧集各一次请求）
const tmdbSearchQueryMax = 4

// planTmdbSearch 把输入框里的东西换算成搜索计划
func planTmdbSearch(q string, rules []ReplaceRule) tmdbSearchPlan {
	if id, kind, ok := parseTmdbIDQuery(q); ok {
		return tmdbSearchPlan{id: id, kind: kind}
	}
	name := applyReplaceRules(q, rules)
	p := parseFileName(name)
	if p.TmdbID > 0 {
		return tmdbSearchPlan{id: strconv.Itoa(p.TmdbID), kind: p.TmdbKind}
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = cleanSearchTitle(name)
	}
	if title == "" {
		title = q
	}
	plan := tmdbSearchPlan{year: p.Year, tv: p.IsTV}
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[s] && len(plan.queries) < tmdbSearchQueryMax {
			seen[s] = true
			plan.queries = append(plan.queries, s)
		}
	}
	cands := titleCandidates(title)
	for _, t := range cands {
		add(t)
	}
	for _, t := range cands {
		for _, v := range tmdbQueryVariants(t) {
			add(v)
		}
	}
	return plan
}

// rankTmdbCands 去重后排序：片名（或原名）与搜索词完全相同的在前，其次是包含的；
// 同档里年份对得上的在前，再是类型对得上的（名字像剧集时剧集在前），最后按热度
func rankTmdbCands(cands []manualCand, plan tmdbSearchPlan) []manualCand {
	norms := make([]string, 0, len(plan.queries))
	for _, q := range plan.queries {
		if n := tmdbNorm(q); n != "" {
			norms = append(norms, n)
		}
	}
	tier := func(c manualCand) int {
		best := 0
		for _, t := range []string{tmdbNorm(c.Title), tmdbNorm(c.Original)} {
			if t == "" {
				continue
			}
			for _, q := range norms {
				if t == q {
					return 2
				}
				if strings.Contains(t, q) {
					best = 1
				}
			}
		}
		return best
	}
	score := func(c manualCand) int {
		n := tier(c) * 4 // 片名档位压过年份与类型
		if plan.year != "" && c.Year == plan.year {
			n += 2
		}
		if plan.tv && c.MediaType == "tv" {
			n++
		}
		return n
	}
	seen := map[string]bool{}
	out := make([]manualCand, 0, len(cands))
	tiers := map[string]int{}
	for _, c := range cands {
		k := c.MediaType + ":" + strconv.Itoa(c.ID)
		if c.ID == 0 || seen[k] {
			continue
		}
		seen[k] = true
		tiers[k] = score(c)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := tiers[out[i].MediaType+":"+strconv.Itoa(out[i].ID)], tiers[out[j].MediaType+":"+strconv.Itoa(out[j].ID)]
		if ti != tj {
			return ti > tj
		}
		return out[i].popularity > out[j].popularity
	})
	if len(out) > tmdbSearchMax {
		out = out[:tmdbSearchMax]
	}
	return out
}

// TmdbSearchMulti GET /tmdb/search?query=xxx
// 片名 → 电影 + 剧集候选；编号 / 链接 → 直查详情
func (h *Handler) TmdbSearchMulti(c *gin.Context) {
	q := strings.TrimSpace(c.Query("query"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入影视名称"})
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error() + "（先在系统配置完成 TMDB 设置）"})
		return
	}

	plan := planTmdbSearch(q, loadReplaceRules())
	if id, kind := plan.id, plan.kind; id != "" {
		kinds := []string{"tv", "movie"}
		if kind != "" {
			kinds = []string{kind}
		}
		found := make([]*manualCand, len(kinds))
		var wg sync.WaitGroup
		for i, k := range kinds {
			wg.Add(1)
			go func(i int, k string) {
				defer wg.Done()
				body, err := tc.get("/"+k+"/"+id, nil)
				if err != nil {
					return // 404：这一边没有这个编号
				}
				var d tmdbRawItem
				if json.Unmarshal(body, &d) != nil || d.ID == 0 {
					return
				}
				cd := d.cand(k)
				found[i] = &cd
			}(i, k)
		}
		wg.Wait()
		items := make([]manualCand, 0, 2)
		for _, f := range found {
			if f != nil {
				items = append(items, *f)
			}
		}
		// 两边都有时热门的那个排前面；类型标签在候选卡片上，挑错了一眼能看出来
		sort.SliceStable(items, func(i, j int) bool { return items[i].popularity > items[j].popularity })
		resp := gin.H{"data": items}
		switch {
		case len(items) == 0:
			resp["hint"] = "未找到该 TMDB ID 对应的影视条目"
		case len(items) == 2:
			resp["hint"] = "这个编号在电影和剧集下各有一个条目，请按类型挑选（也可以输入 tv/" + id + " 或粘贴 TMDB 链接只查一边）"
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	type job struct{ query, kind string }
	var jobs []job
	for _, qq := range plan.queries {
		jobs = append(jobs, job{qq, "tv"}, job{qq, "movie"})
	}
	results := make([][]manualCand, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			body, err := tc.get("/search/"+j.kind, map[string]string{"query": j.query, "include_adult": "false"})
			if err != nil {
				errs[i] = err
				return
			}
			var r struct {
				Results []tmdbRawItem `json:"results"`
			}
			if err := json.Unmarshal(body, &r); err != nil {
				errs[i] = err
				return
			}
			for _, it := range r.Results {
				results[i] = append(results[i], it.cand(j.kind))
			}
		}(i, j)
	}
	wg.Wait()

	var all []manualCand
	failed := 0
	for i := range jobs {
		if errs[i] != nil {
			failed++
			continue
		}
		all = append(all, results[i]...)
	}
	if failed == len(jobs) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 搜索失败: " + errs[0].Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rankTmdbCands(all, plan)})
}
