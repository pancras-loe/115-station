package api

import (
	"encoding/json"
	"fmt"
	"log"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"115-station/internal/model"
)

// ==================== TMDB 客户端 ====================

// TmdbClient 封装 TMDB API 调用
type TmdbClient struct {
	APIKey     string
	APIURL     string
	ImageURL   string
	Language   string
	ProxyURL   string
	httpClient *http.Client
}

// TmdbMedia TMDB 识别结果
type TmdbMedia struct {
	TmdbID      int                `json:"tmdb_id"`
	Title       string             `json:"title"`
	OriginalTitle string           `json:"original_title"`
	Year        string             `json:"year"`
	MediaType   string             `json:"media_type"` // movie, tv
	GenreIDs    []int              `json:"genre_ids"`
	Overview    string             `json:"overview"`
	PosterPath  string             `json:"poster_path"`
	BackdropPath string           `json:"backdrop_path"`
	OrigLanguage string           `json:"original_language"`
	OrigCountry []string          `json:"origin_country"`
	VoteAverage float64           `json:"vote_average"`
	// TV 专属
	SeasonNum   int                `json:"season_number,omitempty"`
	EpisodeNum  int                `json:"episode_number,omitempty"`

	// 识别出处（整理记录上的标注与 AI 判定的确认策略用，不对外输出）：
	// Via 为 ai_title（模型改写片名后搜中）/ ai_pick（模型从候选里选中），其余为空
	Via     string `json:"-"`
	AIScore int    `json:"-"` // 0-100，见 aiScore
	AINote  string `json:"-"` // 打分依据，给人看的一句话
	// matchHow 候选校验在哪一关通过（choose 的 how），AI 打分要看它
	matchHow string
}

// loadTmdbClient 从数据库加载配置构建客户端
func loadTmdbClient() (*TmdbClient, error) {
	// 通过全局 DB 读取
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("缺少 TMDB 配置，请前往「系统配置 → TMDB 配置」填写 API 密钥并测试连接")
	}
	if strings.TrimSpace(cfg.ApiKey) == "" {
		return nil, fmt.Errorf("缺少 TMDB API 密钥，请前往「系统配置 → TMDB 配置」填写并测试连接")
	}
	tc := &TmdbClient{
		APIKey:   strings.TrimSpace(cfg.ApiKey),
		APIURL:   normalizeTMDBBase(cfg.ApiUrl),
		ImageURL: strings.TrimRight(cfg.ImageApiUrl, "/"),
		Language: cfg.Language,
	}
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		tc.ProxyURL = cfg.ProxyUrl
	}
	if tc.ProxyURL == "" {
		// 回退全局代理（系统配置 → 代理）：TMDB 配置卡没有独立的代理输入项，
		// 此前只有 TG 通知/GitHub 检查/海报抓取走全局代理、识别链不走——
		// 无直连环境下识别静默失败（USAGE 文档声称代理覆盖 TMDB 与实际不符）
		tc.ProxyURL = getProxyURL()
	}
	tc.httpClient = &http.Client{Timeout: 15 * time.Second}
	// 接线到 Transport——ProxyURL 字段此前只赋值从未使用，代理配置从未生效
	if tc.ProxyURL != "" {
		if pu, perr := parseProxyURL(tc.ProxyURL); perr == nil {
			tc.httpClient.Transport = &http.Transport{Proxy: pu}
		} else {
			log.Printf("[TMDB] ○ 代理地址无效（忽略，走直连）: %s: %v", tc.ProxyURL, perr)
		}
	}
	return tc, nil
}

// normalizeTMDBBase 规范化 TMDB API 地址：去尾斜杠、补 /3 版本前缀
// TMDB 所有接口都在 /3 下（如 /3/search/movie），漏写前缀会 404
func normalizeTMDBBase(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return "https://api.themoviedb.org/3"
	}
	if !strings.HasSuffix(u, "/3") {
		u += "/3"
	}
	return u
}

// get 发送 GET 请求到 TMDB API
func (tc *TmdbClient) get(endpoint string, params map[string]string) ([]byte, error) {
	u := tc.APIURL + endpoint
	v := url.Values{}
	v.Set("api_key", tc.APIKey)
	v.Set("language", tc.Language)
	for k, val := range params {
		v.Set(k, val)
	}
	fullURL := u + "?" + v.Encode()

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := tc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &tmdbStatusError{Code: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

// SearchMovie 搜索电影
// tmdbSearchCache 搜索结果缓存（同 query+year 5 分钟内不重复外呼）。
// 识别链有 movie→TV→目录名→清洗→拆分多轮重试，同一标题会被搜多次
var (
	tmdbSearchMu    sync.Mutex
	tmdbSearchCache = map[string]tmdbCacheEntry{}
)

type tmdbCacheEntry struct {
	media *TmdbMedia
	err   error
	at    time.Time
}

func tmdbCacheGet(kind, q, year string) (*TmdbMedia, error, bool) {
	tmdbSearchMu.Lock()
	defer tmdbSearchMu.Unlock()
	e, ok := tmdbSearchCache[kind+"|"+q+"|"+year]
	if ok && time.Since(e.at) < 5*time.Minute {
		return e.media, e.err, true
	}
	return nil, nil, false
}

// tmdbCachePut 只缓存成功结果与"确定无结果"（media==nil 且 err==nil 表示
// 已查过且无匹配）；瞬时网络错误不缓存——此前 error 也缓存 5 分钟，一次
// 抖动会让同一查询持续失败并短路识别链的全部重试
func tmdbCachePut(kind, q, year string, media *TmdbMedia, err error) {
	if err != nil {
		return
	}
	tmdbSearchMu.Lock()
	defer tmdbSearchMu.Unlock()
	if len(tmdbSearchCache) > 2000 {
		tmdbSearchCache = map[string]tmdbCacheEntry{}
	}
	tmdbSearchCache[kind+"|"+q+"|"+year] = tmdbCacheEntry{media: media, err: err, at: time.Now()}
}

func (tc *TmdbClient) getByTmdbID(id int, isTV bool) (*TmdbMedia, error) {
	if id <= 0 {
		return nil, nil
	}
	kind := "movie"
	if isTV {
		kind = "tv"
	}
	body, err := tc.get(fmt.Sprintf("/%s/%d", kind, id), nil)
	if err != nil {
		if isTmdbNotFound(err) {
			return nil, nil // 条目不存在是确定的「没有」，调用方按 nil 处理
		}
		return nil, err
	}
	var d struct {
		ID            int      `json:"id"`
		Title         string   `json:"title"`
		Name          string   `json:"name"`
		OriginalTitle string   `json:"original_title"`
		OriginalName  string   `json:"original_name"`
		ReleaseDate   string   `json:"release_date"`
		FirstAirDate  string   `json:"first_air_date"`
		Genres        []struct{ ID int `json:"id"` } `json:"genres"`
		Overview      string   `json:"overview"`
		PosterPath    string   `json:"poster_path"`
		BackdropPath  string   `json:"backdrop_path"`
		OriginalLanguage string `json:"original_language"`
		OriginCountry []string `json:"origin_country"`
		VoteAverage   float64  `json:"vote_average"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	if d.ID == 0 {
		return nil, nil
	}
	title := d.Title
	if title == "" {
		title = d.Name
	}
	// 剧集的原名字段叫 original_name，不是 original_title（与 title/name 同样分家）。
	// 漏了这条回退，「重新整理」按 id 拉详情时 {en_title} 恒为空 —— 自动整理走搜索
	// 接口有原名、重整理没有，同一部剧两条路径改出来的文件名会不一样
	origTitle := d.OriginalTitle
	if origTitle == "" {
		origTitle = d.OriginalName
	}
	year := ""
	date := d.ReleaseDate
	if date == "" {
		date = d.FirstAirDate
	}
	if len(date) >= 4 {
		year = date[:4]
	}
	mediaType := "movie"
	if isTV {
		mediaType = "tv"
	}
	genreIDs := make([]int, 0, len(d.Genres))
	for _, g := range d.Genres {
		genreIDs = append(genreIDs, g.ID)
	}
	return &TmdbMedia{
		TmdbID: d.ID, Title: title, OriginalTitle: origTitle,
		Year: year, MediaType: mediaType, GenreIDs: genreIDs,
		Overview: d.Overview, PosterPath: d.PosterPath, BackdropPath: d.BackdropPath,
		OrigLanguage: d.OriginalLanguage, OrigCountry: d.OriginCountry,
		VoteAverage: d.VoteAverage,
	}, nil
}

func (tc *TmdbClient) SearchMovie(query string, year string) (*TmdbMedia, error) {
	if m, e, ok := tmdbCacheGet("movie", query, year); ok {
		return m, e
	}
	m, e := tc.searchMovieUncached(query, year)
	tmdbCachePut("movie", query, year, m, e)
	return m, e
}

// searchMovieUncached 先不带年份搜（TMDB 的 year 参数是精确过滤，文件名年份差一年就整页落空），
// 年份只参与候选打分；这一页里对不上，再带 year 参数搜一次 —— 片名太常见时
// 正确条目可能不在不带年份那一页里。每一页都要过 choose 的片名校验，不再取第一条
func (tc *TmdbClient) searchMovieUncached(query string, year string) (*TmdbMedia, error) {
	var attempts []map[string]string
	for _, lang := range []string{"zh-CN", tc.Language} {
		if lang == "" || (len(attempts) > 0 && lang == attempts[0]["language"]) {
			continue
		}
		attempts = append(attempts, map[string]string{"query": query, "language": lang})
	}
	if year != "" {
		attempts = append(attempts, map[string]string{"query": query, "language": "zh-CN", "year": year})
	}
	m, err := tc.searchPick(tmdbPick{kind: "movie", query: query, year: year}, attempts)
	if m == nil && err == nil {
		vlog("[整理] 搜索 %q（电影，年份=%s）所有策略均无可采用的结果", query, year)
	}
	return m, err
}

// SearchTV 搜索电视剧
func (tc *TmdbClient) SearchTV(query string, year string) (*TmdbMedia, error) {
	return tc.searchTVSeason(query, year, 0)
}

// searchTVSeason season 是文件里的季号（不知道就传 0）。
// 季号 >1 时文件名里的年份是这一季的年份，不能拿去当首播年份过滤（见 tmdbPick.seasonYearMode）
func (tc *TmdbClient) searchTVSeason(query, year string, season int) (*TmdbMedia, error) {
	key := query
	if season > 1 {
		key = fmt.Sprintf("%s|S%d", query, season)
	}
	if m, e, ok := tmdbCacheGet("tv", key, year); ok {
		return m, e
	}
	p := tmdbPick{kind: "tv", query: query, year: year, season: season}
	var attempts []map[string]string
	if year != "" && !p.seasonYearMode() {
		attempts = append(attempts, map[string]string{"query": query, "first_air_date_year": year})
	}
	attempts = append(attempts, map[string]string{"query": query})
	m, e := tc.searchPick(p, attempts)
	if m == nil && e == nil {
		vlog("[整理] 搜索 %q（TV，年份=%s，季=%d）无可采用的结果", query, year, season)
	}
	tmdbCachePut("tv", key, year, m, e)
	return m, e
}

// SeasonEpisodeCount 某季总集数（TMDB season 详情；失败返回 0 不影响主流程）
func (tc *TmdbClient) SeasonEpisodeCount(tvID, season int) int {
	if tvID <= 0 || season <= 0 {
		return 0
	}
	body, err := tc.get(fmt.Sprintf("/tv/%d/season/%d", tvID, season), nil)
	if err != nil {
		return 0
	}
	var out struct {
		Episodes []struct{} `json:"episodes"`
	}
	if json.Unmarshal(body, &out) != nil {
		return 0
	}
	return len(out.Episodes)
}

// ==================== 文件名解析 ====================

// ParsedName 从文件名解析出的信息
type ParsedName struct {
	Title      string
	Year       string
	Season     int
	Episode    int
	IsTV       bool
	Resolution string // 1080p, 2160p 等
	Quality    string // 完整画质串（1080p.WEB-DL.AAC2.0.H.264 等，由调用方填充）
	// 名字里写明的 TMDB 条目（[tmdbid=123] 等标签），识别时直接按 id 取，不走搜索。
	// TmdbKind 只有 {[tmdbid=1;type=tv]} 这种写法才带，其余为空，由识别时判断类型
	TmdbID   int
	TmdbKind string
	// SeasonGuessed 季号是缺省填的 1（文件名只有集号）。目录名上明写了季号时以目录名为准
	SeasonGuessed bool
	// Source / Context 原始文件名与所在各级目录（由近及远），只给 AI 增强识别看：
	// 解析器截错、丢掉的信息，模型能从原文里自己读出来
	Source  string
	Context []string
}

var (
	// 季集模式：S01E02, s01e02, 1x02
	reSeasonEpisode = regexp.MustCompile(`[Ss](\d{1,2})[Ee](\d{1,3})`)
	// 仅集数模式：EP01 / E01 / 第01集（无季信息，季缺省为 1）
	// 前后必须有分隔符或边界，避免误吃 "WALL.E.2008" 这类片名
	reEpisodeOnly = regexp.MustCompile(`(?:^|[\.\s_-])(?:[Ee][Pp]?|第)(\d{1,3})(?:[集話话])?(?:$|[\.\s_-])`)
	// 动漫字幕组命名的方括号集数：[01] / [01v2]（vN=修正版）。
	// 限 1-3 位数字：4 位会被 "[2001]" 这类年份方括号误伤
	// 前面可以紧挨着另一个方括号：[组][片名][01]
	reBracketEpisode = regexp.MustCompile(`(?:^|[\.\s_\]】-])[\[【](\d{1,3})(?:[vV]\d+)?[\]】]`)
	// 仅季：Season 1, 第一季
	// 两侧必须是分隔符。此前不设边界、而且只认不截，"The.Boys.S04.2160p" 的片名成了 "The Boys S04"
	reSeasonOnly = regexp.MustCompile(`(?:^|[\s._\-\[])[Ss](\d{1,2})(?:$|[\s._\-\]])`)
	// 年份：(2023) 或 .2023. 或空格2023空格
	reYear = regexp.MustCompile(`[\(\.\s_-](19\d{2}|20\d{2})(?:[\)\.\s_-]|$)`)
	// 分辨率
	reResolution = regexp.MustCompile(`(?i)(4K|2160P|1080[PI]|720P|480P)`)
	// 发布组常见标记（用于截断标题）
	reReleaseMarkers = regexp.MustCompile(`(?i)[\.\s_-](BluRay|BDRip|BRRip|DVDRip|WEBRip|WEB-DL|HDTV|REMUX|CAM|TS|TC|R5|HDRip|HC|HQ|PROPER|REPACK|iNTERNAL|LIMITED|UNRATED|DC|EXTENDED|UNCUT|DUBBED|SUBBED|DUAL|MULTi|MULTIAUDIO|RETAIL|COMPLETE|FINAL|REMASTERED|IMAX|3D|HSBS|HOU|DOVi|Dolby|Atmos|TrueHD|DTS|DDP|DD\+?|AAC|AC3|x264|x265|h264|h265|AVC|HEVC|10bit|SDR|HDR|\d{3,4}p|10-Bit)`)
	// 发布组后缀（-GROUP）
)

// reAdBracketBlock / reAdDomain 发布站广告：全角括号块与域名。
// 域名匹配收紧为 www. 前缀或至少两段标签（如 web.4k688.com）——单段+TLD
// 会把 "Call.Me.By.Your.Name"、"Best.of.Me" 这类片名当域名剜掉，标题残缺
var (
	reAdBracketBlock = regexp.MustCompile(`【[^【】]*】`)
	reAdDomain       = regexp.MustCompile(`(?i)(\bwww\.[a-z0-9][a-z0-9-]{1,15}\.(com|net|org|cc|xyz|info|vip|top|me|tv)\b|\b([a-z0-9][a-z0-9-]{1,15}\.){2,}(com|net|org|cc|xyz|info|vip|top|me|tv)\b)`)
)

// stripReleaseAds 剥离文件名/目录名中的发布站广告（【高清影视之家发布
// www.SSDSSE.com】块与裸域名），清理残留分隔符。放在 parseFileName 最前，
// 保证标题提取和搜索都不被广告前缀污染
func stripReleaseAds(name string) string {
	name = reAdBracketBlock.ReplaceAllString(name, " ")
	name = reAdDomain.ReplaceAllString(name, " ")
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return strings.Trim(name, " -_.@")
}

// parseFileName 从视频文件名（或目录名）解析标题、年份、季集等信息。
//
// 片名 = 原名开头到「最早出现的那个标记」为止：季集号、年份、发布标记（1080p/BluRay…）。
// 所有标记都在同一个 name 上找位置，最后按最小位置截一刀；中间只做等长替换（. _ → 空格），
// 位置不会错开。季集号的写法覆盖了中文（第二季 / 第05集 / 全39集）和动漫字幕组
// （[组][片名][01]、片名 - 01 [1080p]），规则思路参考 MoviePilot metavideo.py / metaanime.py
// 与 LitePan rules/regex.go，实现是自己写的
func parseFileName(filename string) *ParsedName {
	result := &ParsedName{}
	// id 标签要在去后缀之前摘：目录名 "Movie.1999.[tmdbid=603]" 没有扩展名，
	// 最后一个点后面的标签会被当成后缀一起剪掉
	name := filename
	tag, name := takeTags(name)
	result.TmdbID, result.TmdbKind = tag.TmdbID, tag.Kind
	defer applyNameTag(result, tag)
	name = trimMediaExt(name)
	// 先剥离发布站广告（【…】块/域名），再解析
	name = stripReleaseAds(name)
	// 「全39集 / 共39集」是整季打包的标志：记下是剧集，再和其他中文噪声词一起剥掉
	if reCnTotalEpisodes.MatchString(name) {
		result.IsTV = true
	}
	name = stripCnNoise(name)

	// 目录里的 01.mp4：只有集号，片名留空交给调用方用目录名补
	if m := reBareEpisodeName.FindStringSubmatch(name); m != nil {
		result.IsTV, result.Season, result.SeasonGuessed = true, 1, true
		result.Episode, _ = strconv.Atoi(m[1])
		return result
	}
	name = stripAnimeGroup(name)

	cut := len(name)
	mark := func(i int) {
		if i >= 0 && i < cut {
			cut = i
		}
	}

	if loc := reSeasonEpisode.FindStringSubmatchIndex(name); loc != nil {
		result.Season, _ = strconv.Atoi(name[loc[2]:loc[3]])
		result.Episode, _ = strconv.Atoi(name[loc[4]:loc[5]])
		mark(loc[0])
	} else {
		if s, at := findSeason(name); s > 0 {
			result.Season = s
			mark(at)
		}
		if e, at := findEpisode(name); e > 0 {
			result.Episode = e
			mark(at)
			if result.Season == 0 {
				// 没写季号：缺省第 1 季，但记下是猜的 —— 目录名上写了「第二季」时要让目录名说了算
				result.Season, result.SeasonGuessed = 1, true
			}
		}
	}
	if result.Season > 0 || result.Episode > 0 {
		result.IsTV = true
	}

	// 检测分辨率
	if m := reResolution.FindStringSubmatch(name); m != nil {
		result.Resolution = strings.ToUpper(m[1])
	}

	// 检测年份。按年份在名字里的位置截，而不是在标题里找第一个同样的数字
	// （2046.2004 的片名本身就是个年份样的数字）
	yearPos := -1
	result.Year, yearPos = pickYear(name)
	if yearPos > 0 {
		mark(yearPos)
	}
	// 发布标记
	if idx := reReleaseMarkers.FindStringIndex(name); idx != nil {
		mark(idx[0])
	}

	title := name[:cut]
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")
	// 清理首尾空格和标点。尾部的左括号是年份被截走后剩下的（"Movie (1999)" → "Movie ("）；
	// 开头的圆括号不动，"(500) Days of Summer" 的括号是片名的一部分。
	// 方括号两头都剥：字幕组命名的片名常整个包在方括号里（[Sousou no Frieren][01]）
	title = strings.TrimRight(title, " -.([（【")
	title = strings.Trim(title, " -.[]【】")
	// 合并多个空格
	title = reMultiSpace.ReplaceAllString(title, " ")

	result.Title = title
	return result
}

var reMultiSpace = regexp.MustCompile(`\s+`)

// applyNameTag 标签里的季号 / 集偏移最后套上去，盖过从名字里解析出来的
func applyNameTag(p *ParsedName, t nameTag) {
	if t.Season > 0 {
		p.Season, p.SeasonGuessed, p.IsTV = t.Season, false, true
	}
	if t.EpOffset != 0 && p.Episode > 0 && p.Episode+t.EpOffset > 0 {
		p.Episode += t.EpOffset
	}
	if t.Kind == "tv" {
		p.IsTV = true
	}
}

// ---------- 季集号 ----------

const cnDigits = `零〇一二两三四五六七八九十百`

var (
	// 第二季 / 第2季（可以和片名粘在一起：庆余年第二季）
	reCnSeason = regexp.MustCompile(`第\s*([0-9]{1,2}|[` + cnDigits + `]{1,3})\s*季`)
	// Season 2 / Season.02
	reEnSeason = regexp.MustCompile(`(?i)(?:^|[\s._\-\[(])Season[\s._]*(\d{1,2})(?:$|[\s._\-\])])`)
	// 第05集 / 第十二话（可以和片名粘在一起：庆余年第二季第05集）
	reCnEpisode = regexp.MustCompile(`第\s*([0-9]{1,4}|[` + cnDigits + `]{1,6})\s*[集话話回期]`)
	// 全39集 / 共39集
	reCnTotalEpisodes = regexp.MustCompile(`[全共]\s*([0-9]{1,4}|[` + cnDigits + `]{1,6})\s*[集话話期]`)
	// 字幕组「片名 - 01 [1080p]」：前后都要有空格包着的短横线，集号后面是结尾、空格或括号
	reAnimeDashEpisode = regexp.MustCompile(`\s-\s(\d{1,4})(?:[vV]\d+)?(?:$|[\s\[(（【.])`)
	// 片名结尾的裸集号「鬼灭之刃 刀匠村篇 03」的候选数字
	reTrailingNumber = regexp.MustCompile(`(?:^|[\s._\-])(\d{1,3})(?:[vV]\d)?(?:[\s._\-]|$)`)
	// 整个名字只有集号：01 / 01v2（4 位留给年份）
	reBareEpisodeName = regexp.MustCompile(`^\s*(\d{1,3})(?:[vV]\d+)?\s*$`)
	// 裸数字紧跟在日期后面（快乐大本营.2019.03.15）不是集号
	reDateBefore = regexp.MustCompile(`\d{4}[\s._\-](?:\d{1,2}[\s._\-])?$`)
)

// findSeason 季号及其位置：第二季 → Season 2 → S02（没写集号的季包）
func findSeason(name string) (int, int) {
	if loc := reCnSeason.FindStringSubmatchIndex(name); loc != nil {
		if n := parseCnNumber(name[loc[2]:loc[3]]); n > 0 {
			return n, loc[0]
		}
	}
	if loc := reEnSeason.FindStringSubmatchIndex(name); loc != nil {
		n, _ := strconv.Atoi(name[loc[2]:loc[3]])
		return n, loc[0]
	}
	if loc := reSeasonOnly.FindStringSubmatchIndex(name); loc != nil {
		n, _ := strconv.Atoi(name[loc[2]:loc[3]])
		return n, loc[0]
	}
	return 0, -1
}

// findEpisode 集号及其位置，按可信度依次试：
// EP01 / E01 / 第01集 → 粘连的中文集号 → [01] → 「 - 01 」→ 片名末尾的裸集号
func findEpisode(name string) (int, int) {
	if loc := reEpisodeOnly.FindStringSubmatchIndex(name); loc != nil {
		n, _ := strconv.Atoi(name[loc[2]:loc[3]])
		return n, loc[0]
	}
	if loc := reCnEpisode.FindStringSubmatchIndex(name); loc != nil {
		if n := parseCnNumber(name[loc[2]:loc[3]]); n > 0 {
			return n, loc[0]
		}
	}
	if loc := reBracketEpisode.FindStringSubmatchIndex(name); loc != nil {
		n, _ := strconv.Atoi(name[loc[2]:loc[3]])
		return n, loc[0]
	}
	if loc := reAnimeDashEpisode.FindStringSubmatchIndex(name); loc != nil {
		if n, ok := plausibleEpisode(name[loc[2]:loc[3]]); ok {
			return n, loc[0]
		}
	}
	return trailingEpisode(name)
}

// plausibleEpisode 排除长得像年份、分辨率的数字
func plausibleEpisode(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	if len(s) == 4 && n >= 1900 && n <= 2099 {
		return 0, false
	}
	switch n {
	case 480, 576, 720, 1080, 1440, 2160:
		return 0, false
	}
	return n, true
}

// trailingEpisode 片名后面的裸集号：「鬼灭之刃 刀匠村篇 03」「西游记 12 [1080P]」。
//
// 裸数字最容易和续集编号撞（Toy Story 3、流浪地球 2），所以只认两种：
// 带前导零的（03），或紧跟在中文后面的两三位数（MoviePilot 的经验：中文名后面跟的
// 非年份数字极有可能是集）。数字后面必须是名字结尾、括号或发布标记，
// 夹在片名中间的数字不算
func trailingEpisode(name string) (int, int) {
	for _, loc := range reTrailingNumber.FindAllStringSubmatchIndex(name, -1) {
		digits := name[loc[2]:loc[3]]
		rest := strings.TrimLeft(name[loc[3]:], "vV0123456789")
		restTrim := strings.TrimLeft(rest, " ._-")
		tail := restTrim == "" || strings.ContainsAny(restTrim[:1], "[【(（")
		if !tail {
			if m := reReleaseMarkers.FindStringIndex(rest); m != nil && m[0] == 0 {
				tail = true
			}
		}
		if !tail || reDateBefore.MatchString(name[:loc[2]]) {
			continue
		}
		n, ok := plausibleEpisode(digits)
		if !ok {
			continue
		}
		before := []rune(strings.TrimRight(name[:loc[2]], " ._-"))
		afterCJK := len(before) > 0 && isCJKRune(before[len(before)-1])
		if strings.HasPrefix(digits, "0") || (afterCJK && len(digits) >= 2) {
			return n, loc[2]
		}
	}
	return 0, -1
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// parseCnNumber 阿拉伯数字或中文数字（一 / 十二 / 二十三 / 一百零五 / 两）
func parseCnNumber(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	digit := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur := 0, 0
	for _, r := range s {
		switch r {
		case '十':
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		case '百':
			if cur == 0 {
				cur = 1
			}
			total += cur * 100
			cur = 0
		default:
			d, ok := digit[r]
			if !ok {
				return 0
			}
			cur = d
		}
	}
	return total + cur
}

// ---------- 动漫字幕组 ----------

var reLeadingBracket = regexp.MustCompile(`^\s*[\[【][^\]】]*[\]】]\s*`)

// stripAnimeGroup 剥开头的 [字幕组]。只在确认是字幕组命名形态（有 [01] 或「 - 01 」集号）、
// 且剥掉之后集号前面还剩片名时才剥：
// "[REC].2007" 是电影名、"[葬送的芙莉莲][01]" 的第一个方括号就是片名，都不能剥
func stripAnimeGroup(name string) string {
	loc := reLeadingBracket.FindStringIndex(name)
	if loc == nil {
		return name
	}
	rest := name[loc[1]:]
	at := -1
	if l := reBracketEpisode.FindStringIndex(rest); l != nil {
		at = l[0]
	} else if l := reAnimeDashEpisode.FindStringIndex(rest); l != nil {
		at = l[0]
	}
	if at <= 0 || strings.Trim(rest[:at], " ._-[]【】") == "" {
		return name
	}
	return rest
}

// ---------- 中文噪声词 ----------

var (
	// 方括号/圆括号里带这些关键词的整块都是说明，不是片名：[国日多音轨+中文字幕]、(国语中字)
	reCnNoiseBracket = regexp.MustCompile(`[\[【(（][^\]】)）]*?(?:字幕|内封|外挂|内嵌|国语|粤语|双语|中字|双字|音轨|配音|国配|台配|简繁|繁简|中英|[全共]\s*[0-9` + cnDigits + `]+\s*[集话話期])[^\]】)）]*?[\]】)）]`)
	// 长词：信息量足够，出现在哪都剥（可能和片名粘在一起：流浪地球国语中字）
	reCnNoiseLong = regexp.MustCompile(`国语中字|国粤双语|国英双语|国日双语|中英双字|中日双字|中英字幕|简繁中字|简体中字|繁体中字|中文字幕|双语字幕|内封字幕|内嵌字幕|外挂字幕|特效字幕|国语配音|粤语中字|简繁内封|杜比视界|杜比全景声|无水印|未删减版|无删减版|导演剪辑版|蓝光原盘|修复版|★?[0-9一二三四五六七八九十]{0,3}月?新番★?`)
	// 短词：只在自成一段时才剥（前后是分隔符或括号），免得切到片名里的字
	reCnNoiseShort = regexp.MustCompile(`(^|[\s._\-\[\]【】()（）+&])(?:中字|双字|国语|粤语|国配|台配|双语|内封|外挂|高清|超清|蓝光|原盘|合集|全集|完整版|未删减|无删减|连载|完结|中英|简繁|简中|繁中|官译|特效|日剧|美剧|韩剧|英剧|泰剧|国产剧|电视剧|动漫|动画|[全共]\s*[0-9` + cnDigits + `]+\s*[集话話期])+([\s._\-\[\]【】()（）+&]|$)`)
)

// reCnNoiseBracketWord 方括号块里除「全N集」之外的说明关键词
var reCnNoiseBracketWord = regexp.MustCompile(`字幕|内封|外挂|内嵌|国语|粤语|双语|中字|双字|音轨|配音|国配|台配|简繁|繁简|中英`)

// stripNoiseBracket 处理一个命中 reCnNoiseBracket 的括号块。
// 带字幕/音轨这类词的块整块是说明，直接剥；只因为「全N集」命中的块常常把片名也包在里面
// （[仁心俱乐部.全40集].2025…），整块剥掉片名就没了，只剩年份后面的平台名去搜 ——
// 这种只剥「全N集」本身，其余内容留下
func stripNoiseBracket(block string) string {
	if reCnNoiseBracketWord.MatchString(block) {
		return " "
	}
	rs := []rune(block)
	inner := string(rs[1 : len(rs)-1])
	inner = reCnTotalEpisodes.ReplaceAllString(inner, " ")
	return " " + strings.Trim(inner, " ._-") + " "
}

// stripCnNoise 剥中文说明词（国语中字、全39集、高清…）。
// 不剥的话它们留在片名里：「狂飙 全39集 国语中字」整串搜不到，中英拆分又会挑出更长的
// 「集 国语中字」去搜。词表思路来自 MoviePilot metavideo.py 的 _name_nostring_re
// 与 LitePan 的 cnQualityTagRe
func stripCnNoise(name string) string {
	name = reCnNoiseBracket.ReplaceAllStringFunc(name, stripNoiseBracket)
	name = reCnNoiseLong.ReplaceAllString(name, " ")
	// 相邻的两个短词共用中间的分隔符，一遍替换只能吃掉一个，循环到不再变化
	for i := 0; i < 5; i++ {
		next := reCnNoiseShort.ReplaceAllString(name, "$1$2")
		if next == name {
			break
		}
		name = next
	}
	return strings.Trim(name, " ._-")
}

// ---------- 扩展名 ----------

var reMediaExt = regexp.MustCompile(`(?i)^(mkv|mp4|avi|ts|m2ts|mts|iso|rmvb|rm|wmv|flv|mov|m4v|webm|mpg|mpeg|vob|3gp|strm|srt|ass|ssa|sub|idx|sup|vtt|nfo|jpg|jpeg|png|webp|gif|bmp|mka|flac|mp3|aac)$`)

// trimMediaExt 只剥认识的扩展名。目录名没有扩展名，按「最后一个点」一刀切的话
// "Blade.Runner.2049.2017" 会被剪成 "Blade.Runner.2049"，年份就丢了
func trimMediaExt(name string) string {
	if idx := strings.LastIndex(name, "."); idx > 0 && reMediaExt.MatchString(name[idx+1:]) {
		return name[:idx]
	}
	return name
}

// pickYear 取文件名里的年份及其位置（位置指年份数字的起点，没有则 -1）。
//
// 片名本身可能带四位数（Blade.Runner.2049.2017、2046.2004），所以取发布标记
// （1080p/BluRay/x264…）之前**最后一个**年份，前面的留在片名里（MoviePilot
// metavideo.py 的 __init_year 同样是后出现的年份覆盖前面的）。发布标记之后的数字多半
// 是发布组名（-2020Group），不参与；标记前一个都没有时才退回标记后的第一个，与旧行为一致。
// 超过明年的「年份」不可能是上映年份（2049），一律当作片名的一部分
func pickYear(name string) (string, int) {
	type hit struct {
		year string
		pos  int
	}
	var hits []hit
	// reYear 两侧都要吃一个分隔符，FindAll 会漏掉紧挨着的第二个（".2049.2017" 的 2017），
	// 所以逐个找、每次从上一个年份数字之后继续
	for off := 0; off < len(name); {
		loc := reYear.FindStringSubmatchIndex(name[off:])
		if loc == nil {
			break
		}
		hits = append(hits, hit{name[off+loc[2] : off+loc[3]], off + loc[2]})
		off += loc[3]
	}
	maxYear := time.Now().Year() + 1
	cut := len(name)
	if m := reReleaseMarkers.FindStringIndex(name); m != nil {
		cut = m[0]
	}
	best := hit{pos: -1}
	for _, h := range hits {
		if y, _ := strconv.Atoi(h.year); y > maxYear {
			continue
		}
		if h.pos < cut {
			best = h
		} else if best.pos < 0 {
			best = h
			break
		}
	}
	return best.year, best.pos
}

// 名字里的识别标签。兼容 Emby 的 [tmdbid=123]、Jellyfin 的 [tmdbid-123]、Plex 的 {tmdb-123}，
// 以及 MoviePilot 的 {[tmdbid=123;type=tv;s=2]}。
//
// 花括号写法是给「识别规则」用的：替换规则把某个片名换成「片名 {[tmdbid=…;type=tv;s=2;eo=-12]}」，
// 就能直接指定条目、季号和集数偏移（MoviePilot 自定义识别词的「替换 + 集偏移」那一套）。
// 里面的各项都可以单独写：只写 s=2 就是只改季号，照常按片名搜
var (
	reTagBraced = regexp.MustCompile(`\{\[([^\]]*)\]\}`)
	reTmdbTag   = regexp.MustCompile(`(?i)[\[{]\s*tmdb(?:id)?\s*[=\-:]\s*(\d+)\s*[\]}]`)
)

// nameTag 从名字里摘出来的识别标签
type nameTag struct {
	TmdbID   int
	Kind     string // movie / tv，没写为空
	Season   int    // s=2：强制季号
	EpOffset int    // eo=-12：集号偏移（跨季连续编号的番剧，第二季从 13 集开始编）
}

// takeTags 摘出名字里的识别标签，返回标签与去掉标签后的名字
func takeTags(name string) (nameTag, string) {
	var t nameTag
	// 花括号写法可以有多段（id 一段、季号一段），逐段收
	for {
		m := reTagBraced.FindStringSubmatchIndex(name)
		if m == nil {
			break
		}
		known := false
		for _, kv := range strings.Split(name[m[2]:m[3]], ";") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch k {
			case "tmdbid", "tmdb":
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					t.TmdbID, known = n, true
				}
			case "type":
				switch strings.ToLower(v) {
				case "tv":
					t.Kind, known = "tv", true
				case "movie", "movies":
					t.Kind, known = "movie", true
				}
			case "s":
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					t.Season, known = n, true
				}
			case "eo":
				if n, err := strconv.Atoi(strings.TrimPrefix(v, "+")); err == nil {
					t.EpOffset, known = n, true
				}
			}
		}
		if !known {
			break // 不是我们认识的标签，原样留着
		}
		name = strings.TrimSpace(name[:m[0]] + name[m[1]:])
	}
	if t.TmdbID == 0 {
		if m := reTmdbTag.FindStringSubmatchIndex(name); m != nil {
			if id, _ := strconv.Atoi(name[m[2]:m[3]]); id > 0 {
				t.TmdbID = id
				name = strings.TrimSpace(name[:m[0]] + name[m[1]:])
			}
		}
	}
	return t, name
}

// extractTmdbID 名字里的 TMDB id 标签（没有返回 0）
func extractTmdbID(name string) (int, string) {
	t, _ := takeTags(name)
	return t.TmdbID, t.Kind
}

func (tc *TmdbClient) recognize(parsed *ParsedName) (*TmdbMedia, error) {
	// 名字里写明了 TMDB id：最硬的证据，直接按 id 取；取不到再退回按片名搜
	if parsed.TmdbID > 0 {
		media, err := tc.recognizeByTag(parsed)
		if err != nil || media != nil {
			return media, err
		}
	}
	// 人工指定过的同名内容：直接用人工的结论（recogmemory.go）
	if media, err := tc.recognizeByMemory(parsed); err != nil || media != nil {
		return media, err
	}
	if parsed.Title == "" {
		return nil, fmt.Errorf("无法从文件名提取标题")
	}

	// 剧集带上季号：非首季时文件名里的年份是这一季的，不能当首播年份用（searchTVSeason）
	searchTV := func(q, year string) (*TmdbMedia, error) {
		return tc.searchTVSeason(q, year, parsed.Season)
	}
	// movieThenTV：先电影后剧集（CMS 同款兜底顺序）。
	// 年份单独传——AI 增强识别那一轮用的是模型给的年份，不是文件名里解析出来的。
	movieThenTV := func(q, year string) (*TmdbMedia, error) {
		media, err := tc.SearchMovie(q, year)
		if err != nil {
			return nil, err
		}
		if media != nil {
			return media, nil
		}
		return tc.SearchTV(q, year)
	}

	// 第一轮：原始标题（剧集直接搜 TV）。中英双名先分别用中文名、英文名搜，最后才用整串：
	// TMDB 对「骗不了人的男人 Softie Conman」这种混合串整体几乎搜不到
	// （MoviePilot _prepare_search_names 同样是中文名 → 英文名依次试）。
	// 年份对不上的情况（跨年上映、首播差一年）已经在搜索内部处理：年份只参与候选打分，
	// 原来单独的「去掉年份宽搜索」一轮不再需要
	var media *TmdbMedia
	var err error
	for _, q := range titleCandidates(parsed.Title) {
		if parsed.IsTV {
			media, err = searchTV(q, parsed.Year)
		} else {
			media, err = movieThenTV(q, parsed.Year)
		}
		if err != nil || media != nil {
			return media, err
		}
	}

	// 第二轮：清洗后的标题重试（去掉特殊字符/残留标记，压紧空白）
	cleaned := cleanSearchTitle(parsed.Title)
	if cleaned != "" && cleaned != parsed.Title {
		log.Printf("[整理] 首次搜索无结果，用清洗标题重试: %q → %q", parsed.Title, cleaned)
		if parsed.IsTV {
			media, err = tc.SearchTV(cleaned, parsed.Year)
		} else {
			media, err = movieThenTV(cleaned, parsed.Year)
		}
		if err != nil || media != nil {
			return media, err
		}
	}

	// 第二点五轮：中英混合标题拆分搜索。
	// 场景："骗不了人的男人 Softie Conman"——TMDB 对混合串整体匹配不到，
	// 中文名或英文名单独搜索才能命中
	if cjk, latin := splitCJKLatin(parsed.Title); cjk != "" && latin != "" {
		for _, q := range []string{cjk, latin} {
			log.Printf("[整理] 混合标题拆分搜索: %q（原 %q）", q, parsed.Title)
			if parsed.IsTV {
				media, err = searchTV(q, parsed.Year)
			} else {
				media, err = movieThenTV(q, parsed.Year)
			}
			if err != nil || media != nil {
				return media, err
			}
		}
	}

	// 第三轮：AI 增强识别（开关开着才走）。给模型原始文件名与目录，改写片名再搜，
	// 搜不中再让它从候选里挑；详见 airecogflow.go
	if aiCfg := loadAIRecognizeCfg(); aiCfg != nil {
		return tc.recognizeByAI(aiCfg, parsed)
	}
	return media, nil
}

// recognizeByTag 按名字里的 TMDB id 标签取条目。
//
// 电影和剧集的 id 是两套编号，同一个数字两边往往都有条目（movie/1399 与 tv/1399 是两部片），
// 所以类型要先定下来：标签写了 type 就用它；有季集号的当剧集；都没有就两边都取，
// 拿片名和年份去比，谁对得上用谁。比不出高下宁可放弃标签、退回按片名搜，
// 也不猜一个类型 —— 猜错了整部片会被搬进另一部片的目录（MoviePilot 的
// _disambiguate_by_meta 也是比不出就不认）
func (tc *TmdbClient) recognizeByTag(parsed *ParsedName) (*TmdbMedia, error) {
	id := parsed.TmdbID
	kinds := []bool{false, true} // isTV
	switch {
	case parsed.TmdbKind == "tv", parsed.TmdbKind == "" && parsed.IsTV:
		kinds = []bool{true}
	case parsed.TmdbKind == "movie":
		kinds = []bool{false}
	}
	var found []*TmdbMedia
	for _, isTV := range kinds {
		m, err := tc.getByTmdbID(id, isTV)
		if err != nil {
			return nil, err
		}
		if m != nil {
			found = append(found, m)
		}
	}
	if len(found) == 1 {
		log.Printf("[整理] 按 id 标签识别: tmdb=%d → %s (%s) [%s]", id, found[0].Title, found[0].Year, found[0].MediaType)
		return found[0], nil
	}
	if len(found) == 0 {
		log.Printf("[整理] ○ id 标签 tmdb=%d 在 TMDB 上找不到，改按片名识别", id)
		return nil, nil
	}
	score := func(m *TmdbMedia) int {
		s := titleLevel(titleKey(parsed.Title), m.Title, m.OriginalTitle) * 3
		return s + yearScore(parsed.Year, m.Year)
	}
	sm, st := score(found[0]), score(found[1])
	if sm != st {
		pick := found[0]
		if st > sm {
			pick = found[1]
		}
		log.Printf("[整理] 按 id 标签识别: tmdb=%d 电影/剧集都有条目，按片名年份判为 %s (%s) [%s]",
			id, pick.Title, pick.Year, pick.MediaType)
		return pick, nil
	}
	log.Printf("[整理] ○ id 标签 tmdb=%d 电影「%s」与剧集「%s」都有条目，无法判断是哪一个，改按片名识别（标签可写成 {[tmdbid=%d;type=tv]} 指明类型）",
		id, found[0].Title, found[1].Title, id)
	return nil, nil
}

// titleCandidates 片名的搜索候选：中英双名拆成「中文名、英文名、整串」，其余只有整串。
//
// 按空格分词，含中文的词整个归中文名 —— 「流浪地球2 The Wandering Earth II」的 2
// 跟着中文走，拆成「流浪地球2」而不是「流浪地球」（那是另一部片）。
// 和下面的 splitCJKLatin 不同：那个按字符切、取最长段，只在前几轮都落空时兜底用。
// 分词归属的做法参考 openStrm organize/parse-name.ts 的 titleCandidates
func titleCandidates(title string) []string {
	var cjk, latin []string
	for _, w := range strings.Fields(title) {
		if strings.IndexFunc(w, isCJKRune) >= 0 {
			cjk = append(cjk, w)
		} else {
			latin = append(latin, w)
		}
	}
	if len(cjk) == 0 || len(latin) == 0 {
		return []string{title}
	}
	l := strings.Join(latin, " ")
	letters := 0
	for _, r := range l {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			letters++
		}
	}
	// 英文部分至少要有两个字母才算一个名字。「流浪地球 2」的 2 是续集编号，
	// 拆开来单搜「流浪地球」搜到的是另一部片
	if letters < 2 {
		return []string{title}
	}
	return []string{strings.Join(cjk, " "), l, title}
}

// splitCJKLatin 把中英混合标题拆成中文名与英文名（各自取最长连续段）。
// "骗不了人的男人[国日多音轨+中文字幕] Softie Conman"
//   → ("骗不了人的男人", "Softie Conman")
func splitCJKLatin(title string) (cjk, latin string) {
	isCJK := func(r rune) bool {
		return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
	}
	isLatin := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '\'' || r == '’' || r == ':' || r == '!' || r == '-' || r == '&' || r == '.'
	}
	var curCJK, curLatin, bestCJK, bestLatin []rune
	flush := func() {
		if len(curCJK) > len(bestCJK) {
			bestCJK = append([]rune(nil), curCJK...)
		}
		if len(curLatin) > len(bestLatin) {
			bestLatin = append([]rune(nil), curLatin...)
		}
		curCJK, curLatin = curCJK[:0], curLatin[:0]
	}
	for _, r := range title {
		switch {
		case isCJK(r):
			if len(curLatin) > 0 {
				flush()
			}
			curCJK = append(curCJK, r)
		case isLatin(r):
			if len(curCJK) > 0 {
				flush()
			}
			curLatin = append(curLatin, r)
		case r == ' ' || r == '·':
			// 空格归入当前段，不打断连续性
			if len(curCJK) > 0 {
				curCJK = append(curCJK, r)
			} else if len(curLatin) > 0 {
				curLatin = append(curLatin, r)
			}
		default:
			flush()
		}
	}
	flush()
	cjk = strings.TrimSpace(string(bestCJK))
	latin = strings.TrimSpace(string(bestLatin))
	// 拉丁段必须含字母（排除 "2022"/"1080p" 这类纯数字残留）
	if !strings.ContainsFunc(latin, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) {
		latin = ""
	}
	return cjk, latin
}

// cleanSearchTitle 清洗搜索标题：仅保留中文/字母/数字/空格，压紧空白
func cleanSearchTitle(title string) string {
	var b []rune
	for _, r := range title {
		switch {
		case r >= 0x4e00 && r <= 0x9fff, // 汉字
			r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, r)
		case r == ' ' || r == '　' || r == '.' || r == '-' || r == '_' || r == ':' || r == '：':
			b = append(b, ' ')
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(string(b)), " "))
}

// absYearDiff 计算两个年份字符串的绝对差值（解析失败返回 999）
func absYearDiff(a, b string) int {
	ai, err1 := strconv.Atoi(strings.TrimSpace(a))
	bi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return 999
	}
	d := ai - bi
	if d < 0 {
		return -d
	}
	return d
}

// ==================== TMDB 搜索接口（影视转存等页面用） ====================

// regexpPureDigits 纯数字输入视为 TMDB ID 直查
var regexpPureDigits = regexp.MustCompile(`^\d{1,8}$`)

// TmdbSearchMulti GET /tmdb/search?query=xxx
// 影视名称 → TMDB 条目列表（movie/tv）；纯数字按 TMDB ID 直查详情
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
	// 纯数字 → 按 TMDB ID 直查详情（先电影后剧集）
	if regexpPureDigits.MatchString(q) {
		for _, kind := range []string{"movie", "tv"} {
			body, err := tc.get("/"+kind+"/"+q, nil)
			if err != nil {
				continue
			}
			var d struct {
				ID          int    `json:"id"`
				Title       string `json:"title"`
				Name        string `json:"name"`
				ReleaseDate string `json:"release_date"`
				FirstAir    string `json:"first_air_date"`
				PosterPath  string `json:"poster_path"`
			}
			if json.Unmarshal(body, &d) != nil || d.ID == 0 {
				continue
			}
			title := d.Title
			date := d.ReleaseDate
			if title == "" {
				title = d.Name
			}
			if date == "" {
				date = d.FirstAir
			}
			year := ""
			if len(date) >= 4 {
				year = date[:4]
			}
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{{
				"id": d.ID, "media_type": kind, "title": title,
				"year": year, "poster": d.PosterPath,
			}}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{}, "hint": "未找到该 TMDB ID 对应的影视条目"})
		return
	}
	body, err := tc.get("/search/multi", map[string]string{"query": q, "include_adult": "false"})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 搜索失败: " + err.Error()})
		return
	}
	var result struct {
		Results []struct {
			ID           int     `json:"id"`
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			PosterPath   string  `json:"poster_path"`
			VoteAverage  float64 `json:"vote_average"`
			Overview     string  `json:"overview"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &result) != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 响应解析失败"})
		return
	}
	items := make([]gin.H, 0, len(result.Results))
	for _, r := range result.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue // multi-search 会混入 person
		}
		title := r.Title
		date := r.ReleaseDate
		if title == "" {
			title = r.Name
		}
		if date == "" {
			date = r.FirstAirDate
		}
		year := ""
		if len(date) >= 4 {
			year = date[:4]
		}
		items = append(items, gin.H{
			"id": r.ID, "media_type": r.MediaType, "title": title,
			"year": year, "poster": r.PosterPath,
			"vote": r.VoteAverage, "overview": r.Overview,
		})
		if len(items) >= 12 {
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// TmdbImg GET /tmdb/img?path=/xx.jpg&size=w154
// 海报代理：走 TMDB 配置的代理设置拉图（国内直连 image.tmdb.org 常不通）
func (h *Handler) TmdbImg(c *gin.Context) {
	p := c.Query("path")
	if !strings.HasPrefix(p, "/") {
		c.Status(http.StatusBadRequest)
		return
	}
	size := c.Query("size")
	if size == "" {
		size = "w154"
	}
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil || cfg.ImageApiUrl == "" {
		c.Status(http.StatusNotFound)
		return
	}
	base := strings.TrimRight(cfg.ImageApiUrl, "/")
	if !strings.HasSuffix(base, "/t/p") {
		base += "/t/p" // 配置里一般只填到域名，海报尺寸挂在 /t/p 下
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/"+size+p, nil)
	client := &http.Client{Timeout: 15 * time.Second}
	// 优先 TMDB 配置的代理，其次全局代理（与海报抓取同策略）
	proxyURL := getProxyURL()
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	if proxyURL != "" {
		if pu, perr := parseProxyURL(proxyURL); perr == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Status(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.DataFromReader(http.StatusOK, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}
