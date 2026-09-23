package api

// ==================== TMDB 候选校验 ====================
//
// 搜索结果不再「取第一条」：TMDB 的搜索是模糊的，查什么都会返回一页结果，
// 片名解析坏了（残留噪声词、截错位置）时第一条往往是毫不相干的热门条目，
// 此前照单全收 —— 识别「错」比识别「不出」代价大得多：文件会被搬进错误的片名目录，
// 洗版也会拿它去和别人的片子比。
//
// 现在每条候选都要和查询片名对得上才会被采用，对不上就判「未命中」，
// 交给识别链的后续几轮（清洗重试 / 中英拆分 / AI）或人工确认。
// 思路参考 MoviePilot（app/modules/themoviedb/tmdbapi.py 的 __compare_names：
// 标题/原名严格相等 → 别名/译名）与 openStrm（organize/identify.ts 的 scoreCandidate：
// 标题 +3、年份 +2/+1/-1），实现是自己写的。

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// tmdbStatusError TMDB 返回了 HTTP 错误码。单独成型是为了区分 404（条目不存在，
// 是确定的「没有」）与网络/限流这类瞬时错误（不能当成「没有」去移冗余）
type tmdbStatusError struct {
	Code int
	Body string
}

func (e *tmdbStatusError) Error() string { return fmt.Sprintf("TMDB HTTP %d: %s", e.Code, e.Body) }

func isTmdbNotFound(err error) bool {
	var se *tmdbStatusError
	return errors.As(err, &se) && se.Code == 404
}

// tmdbCand 搜索结果里的一条候选（电影与剧集的字段名不同，这里统一）
type tmdbCand struct {
	ID       int
	Title    string
	Original string
	Date     string
	GenreIDs []int
	Overview string
	Poster   string
	Backdrop string
	OrigLang string
	Vote     float64
}

func (c tmdbCand) year() string {
	if len(c.Date) >= 4 {
		return c.Date[:4]
	}
	return ""
}

func (c tmdbCand) String() string { return fmt.Sprintf("%s (%s) tmdb=%d", c.Title, c.year(), c.ID) }

// searchCands 调一次 /search/{kind}，把结果统一成 tmdbCand
func (tc *TmdbClient) searchCands(kind string, params map[string]string) ([]tmdbCand, error) {
	body, err := tc.get("/search/"+kind, params)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			ID               int     `json:"id"`
			Title            string  `json:"title"`
			Name             string  `json:"name"`
			OriginalTitle    string  `json:"original_title"`
			OriginalName     string  `json:"original_name"`
			ReleaseDate      string  `json:"release_date"`
			FirstAirDate     string  `json:"first_air_date"`
			GenreIDs         []int   `json:"genre_ids"`
			Overview         string  `json:"overview"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			OriginalLanguage string  `json:"original_language"`
			VoteAverage      float64 `json:"vote_average"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	cands := make([]tmdbCand, 0, len(out.Results))
	for _, r := range out.Results {
		c := tmdbCand{ID: r.ID, Title: r.Title, Original: r.OriginalTitle, Date: r.ReleaseDate,
			GenreIDs: r.GenreIDs, Overview: r.Overview, Poster: r.PosterPath, Backdrop: r.BackdropPath,
			OrigLang: r.OriginalLanguage, Vote: r.VoteAverage}
		if kind == "tv" {
			c.Title, c.Original, c.Date = r.Name, r.OriginalName, r.FirstAirDate
		}
		cands = append(cands, c)
	}
	return cands, nil
}

func (c tmdbCand) media(kind string, origCountry []string) *TmdbMedia {
	return &TmdbMedia{
		TmdbID: c.ID, Title: c.Title, OriginalTitle: c.Original, Year: c.year(), MediaType: kind,
		GenreIDs: c.GenreIDs, Overview: c.Overview, PosterPath: c.Poster, BackdropPath: c.Backdrop,
		OrigLanguage: c.OrigLang, OrigCountry: origCountry, VoteAverage: c.Vote,
	}
}

// ---------- 详情（别名 / 译名 / 各季首播）----------

// tmdbDetail 校验候选要用到的详情字段。一次请求带上 alternative_titles 与 translations，
// 选中之后取 origin_country 也复用这一份，不额外加请求
type tmdbDetail struct {
	OriginCountry []string
	Names         []string       // 别名 + 各语言译名
	Seasons       map[int]string // 季号 → 该季首播日期（仅剧集）
}

var (
	tmdbDetailMu    sync.Mutex
	tmdbDetailCache = map[string]tmdbDetailEntry{}
)

type tmdbDetailEntry struct {
	d  *tmdbDetail
	at time.Time
}

// detailOf 取详情（30 分钟缓存）。条目不存在返回 (nil, nil)
func (tc *TmdbClient) detailOf(kind string, id int) (*tmdbDetail, error) {
	key := fmt.Sprintf("%s/%d", kind, id)
	tmdbDetailMu.Lock()
	if e, ok := tmdbDetailCache[key]; ok && time.Since(e.at) < 30*time.Minute {
		tmdbDetailMu.Unlock()
		return e.d, nil
	}
	tmdbDetailMu.Unlock()

	body, err := tc.get("/"+key, map[string]string{"append_to_response": "alternative_titles,translations"})
	if err != nil {
		if isTmdbNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw struct {
		OriginCountry     []string `json:"origin_country"`
		AlternativeTitles struct {
			Titles []struct {
				Title string `json:"title"`
			} `json:"titles"` // 电影
			Results []struct {
				Title string `json:"title"`
			} `json:"results"` // 剧集
		} `json:"alternative_titles"`
		Translations struct {
			Translations []struct {
				Data struct {
					Title string `json:"title"`
					Name  string `json:"name"`
				} `json:"data"`
			} `json:"translations"`
		} `json:"translations"`
		Seasons []struct {
			SeasonNumber int    `json:"season_number"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	d := &tmdbDetail{OriginCountry: raw.OriginCountry, Seasons: map[int]string{}}
	for _, t := range raw.AlternativeTitles.Titles {
		d.Names = append(d.Names, t.Title)
	}
	for _, t := range raw.AlternativeTitles.Results {
		d.Names = append(d.Names, t.Title)
	}
	for _, t := range raw.Translations.Translations {
		d.Names = append(d.Names, t.Data.Title, t.Data.Name)
	}
	for _, s := range raw.Seasons {
		d.Seasons[s.SeasonNumber] = s.AirDate
	}
	tmdbDetailMu.Lock()
	if len(tmdbDetailCache) > 2000 {
		tmdbDetailCache = map[string]tmdbDetailEntry{}
	}
	tmdbDetailCache[key] = tmdbDetailEntry{d: d, at: time.Now()}
	tmdbDetailMu.Unlock()
	return d, nil
}

// ---------- 片名比对 ----------

// titleKey 比对用的片名归一化：全角转半角、转小写、只留字母和数字。
// "Dune: Part Two" / "dune part two" / "ＤＵＮＥ　Part.Two" 都归成 "duneparttwo"
func titleKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E { // 全角 ASCII
			r -= 0xFEE0
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// 片名比对等级
const (
	titleNone  = 0
	titleLoose = 1 // 一方包含另一方
	titleExact = 2 // 归一化后相等
)

// titleLevel 查询片名与一组候选名的最佳比对等级
func titleLevel(queryKey string, names ...string) int {
	best := titleNone
	for _, n := range names {
		k := titleKey(n)
		if k == "" || queryKey == "" {
			continue
		}
		if k == queryKey {
			return titleExact
		}
		if looseTitleMatch(queryKey, k) {
			best = titleLoose
		}
	}
	return best
}

// looseTitleMatch 包含关系。较短的一方太短时不算：单个汉字、"up" 这种两三个字母
// 会命中一大片无关片名（"up" ⊂ "upgrade"）
func looseTitleMatch(a, b string) bool {
	short, long := a, b
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	minLen := 4
	for _, r := range short {
		if r > unicode.MaxASCII { // 含中日韩文字：两个字就有足够信息量
			minLen = 2
			break
		}
	}
	if len([]rune(short)) < minLen {
		return false
	}
	return strings.Contains(long, short)
}

// ---------- 候选选择 ----------

// tmdbPick 候选选择的输入
type tmdbPick struct {
	kind   string // movie / tv
	query  string
	year   string
	season int // 剧集：文件里的季号。>1 时 year 是「这一季」的年份，不是首播年份
}

// seasonYearMode 非首季剧集：文件名里的年份多半是该季播出年份（The.Boys.S04E01.2024），
// 拿它比首播年份（2019）必然对不上，改为到详情里核对该季的首播日期
func (p tmdbPick) seasonYearMode() bool { return p.kind == "tv" && p.season > 1 && p.year != "" }

type scoredCand struct {
	c     tmdbCand
	level int
	score int
	how   string
}

// yearScore 年份加减分：同年 +2，差一年 +1（跨年上映/时区），差更多 -1
func yearScore(want, got string) int {
	if want == "" || got == "" {
		return 0
	}
	switch absYearDiff(want, got) {
	case 0:
		return 2
	case 1:
		return 1
	default:
		return -1
	}
}

// choose 从一页候选里挑出和查询片名对得上的那一条；都对不上返回 nil。
//
// 三道关，逐道放宽：
//  1. 标题 / 原名归一化后相等；
//  2. 前 5 名的别名、译名相等（文件名用的是第三种语言的译名，如罗马音、英文名）；
//  3. 包含关系 —— 只在年份也对得上（或文件名没年份）时接受。
//
// 同一关内按分数排：标题相等 +3、包含 +1，年份见 yearScore；同分保持 TMDB 的相关度顺序。
// 详情请求失败且最终没选出结果时返回错误，不能把网络抖动当成「确定没有」
// （调用方会缓存「没有」并据此把文件移进冗余）
func (tc *TmdbClient) choose(p tmdbPick, cands []tmdbCand) (*tmdbCand, *tmdbDetail, error) {
	qk := titleKey(p.query)
	var detailErr error
	detail := func(id int) *tmdbDetail {
		d, err := tc.detailOf(p.kind, id)
		if err != nil && detailErr == nil {
			detailErr = err
		}
		return d
	}

	scored := make([]scoredCand, 0, len(cands))
	for _, c := range cands {
		cy := c.year()
		s := scoredCand{c: c, level: titleLevel(qk, c.Title, c.Original)}
		if p.seasonYearMode() {
			// 剧不可能在开播之前就播到第 N 季
			if cy != "" && absYearDiff(cy, p.year) > 1 && cy > p.year {
				continue
			}
		} else {
			s.score += yearScore(p.year, cy)
		}
		if s.level == titleExact {
			s.score += 3
		} else if s.level == titleLoose {
			s.score++
		}
		scored = append(scored, s)
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	// 非首季：到详情里核对这一季的首播年份，对上的加分；该季根本不存在的减分
	seasonCheck := func(list []scoredCand) {
		if !p.seasonYearMode() {
			return
		}
		for i := range list {
			if i >= 3 {
				break
			}
			d := detail(list[i].c.ID)
			if d == nil {
				continue
			}
			if air, ok := d.Seasons[p.season]; ok {
				list[i].score += yearScore(p.year, pickYearPrefix(air))
			} else {
				list[i].score--
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	}
	finish := func(s scoredCand, how string) (*tmdbCand, *tmdbDetail, error) {
		vlog("[整理] 候选采用: %s（%s，得分 %d）", s.c, how, s.score)
		c := s.c
		return &c, detail(c.ID), nil
	}

	// 第一关：标题 / 原名相等
	var exact []scoredCand
	for _, s := range scored {
		if s.level == titleExact {
			exact = append(exact, s)
		}
	}
	if len(exact) > 0 {
		seasonCheck(exact)
		return finish(exact[0], "片名相等")
	}

	// 第二关：别名 / 译名相等（只看前 5 名，每条一次详情请求，有缓存）
	var alias []scoredCand
	for i, s := range scored {
		if i >= 5 {
			break
		}
		d := detail(s.c.ID)
		if d == nil || titleLevel(qk, d.Names...) != titleExact {
			continue
		}
		s.score += 3
		alias = append(alias, s)
	}
	if len(alias) > 0 {
		seasonCheck(alias)
		return finish(alias[0], "别名相等")
	}

	// 第三关：包含关系，年份必须也对得上
	for _, s := range scored {
		if s.level != titleLoose {
			continue
		}
		if !p.seasonYearMode() && p.year != "" && absYearDiff(p.year, s.c.year()) > 1 {
			continue
		}
		return finish(s, "片名相近、年份相符")
	}

	if len(cands) > 0 {
		top := make([]string, 0, 3)
		for i, c := range cands {
			if i >= 3 {
				break
			}
			top = append(top, c.String())
		}
		vlog("[整理] 搜索 %q（%s，年份=%s）有 %d 个候选，但片名都对不上，不采用: %s",
			p.query, p.kind, p.year, len(cands), strings.Join(top, " | "))
	}
	return nil, nil, detailErr
}

func pickYearPrefix(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return ""
}

// searchPick 按 attempts 依次搜索、逐页校验，第一条对得上的即返回
func (tc *TmdbClient) searchPick(p tmdbPick, attempts []map[string]string) (*TmdbMedia, error) {
	var firstErr error
	for _, params := range attempts {
		cands, err := tc.searchCands(p.kind, params)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(cands) == 0 {
			vlog("[整理] 搜索 %q（%s，参数 %v）无结果", p.query, p.kind, params)
			continue
		}
		c, d, err := tc.choose(p, cands)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if c != nil {
			var country []string
			if d != nil {
				country = d.OriginCountry
			}
			return c.media(p.kind, country), nil
		}
	}
	return nil, firstErr
}
