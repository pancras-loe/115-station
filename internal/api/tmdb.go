package api

import (
	"encoding/json"
	"bytes"
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
	"strmhub/internal/model"
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
}

// loadTmdbClient 从数据库加载配置构建客户端
func loadTmdbClient() (*TmdbClient, error) {
	// 通过全局 DB 读取
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("TMDB 未配置")
	}
	if cfg.ApiKey == "" {
		return nil, fmt.Errorf("TMDB API 密钥未填写")
	}
	tc := &TmdbClient{
		APIKey:   cfg.ApiKey,
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
		return nil, fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, string(body))
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
		return nil, err
	}
	var d struct {
		ID            int      `json:"id"`
		Title         string   `json:"title"`
		Name          string   `json:"name"`
		OriginalTitle string   `json:"original_title"`
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
		TmdbID: d.ID, Title: title, OriginalTitle: d.OriginalTitle,
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

func (tc *TmdbClient) searchMovieUncached(query string, year string) (*TmdbMedia, error) {
	// 多策略搜索：TMDB year 参数是精确匹配（时区可能差一年），改为搜索后手动过滤
	// 策略1: query + language=zh-CN（优先搜中文标题）
	// 策略2: query 不带 language（搜原始/英文标题）
	for si, params := range []map[string]string{
		{"query": query, "language": "zh-CN"},
		{"query": query},
	} {
		body, err := tc.get("/search/movie", params)
		if err != nil {
			continue
		}
		var result struct {
			Results []struct {
				ID               int     `json:"id"`
				Title            string  `json:"title"`
				OriginalTitle    string  `json:"original_title"`
				ReleaseDate      string  `json:"release_date"`
				GenreIDs         []int   `json:"genre_ids"`
				Overview         string  `json:"overview"`
				PosterPath       string  `json:"poster_path"`
				BackdropPath     string  `json:"backdrop_path"`
				OriginalLanguage string  `json:"original_language"`
				VoteAverage      float64 `json:"vote_average"`
			} `json:"results"`
			TotalResults int `json:"total_results"`
		}
		if json.Unmarshal(body, &result) != nil || len(result.Results) == 0 {
			vlog("[整理] 搜索策略%d %q 无结果", si+1, query)
			continue
		}
		// 候选日志
		cands := make([]string, 0, 3)
		for _, c := range result.Results {
			if len(cands) >= 3 {
				break
			}
			cy := ""
			if len(c.ReleaseDate) >= 4 {
				cy = c.ReleaseDate[:4]
			}
			cands = append(cands, fmt.Sprintf("%s (%s) tmdb=%d", c.Title, cy, c.ID))
		}
		vlog("[整理] 搜索策略%d %q 候选: %s", si+1, query, strings.Join(cands, " | "))
		// 手动按年份过滤（差1年也接受，处理时区/上映年份差异）
		var pick = -1
		if year != "" {
			for i, c := range result.Results {
				cy := ""
				if len(c.ReleaseDate) >= 4 {
					cy = c.ReleaseDate[:4]
				}
				if cy == year {
					pick = i
					break
				}
			}
			// 精确年份没匹配到，允许差1年
			if pick < 0 {
				for i, c := range result.Results {
					cy := ""
					if len(c.ReleaseDate) >= 4 {
						cy = c.ReleaseDate[:4]
					}
					if cy != "" && absYear(cy, year) == 1 {
						pick = i
						break
					}
				}
			}
		}
		if pick < 0 {
			pick = 0 // 没有年份过滤或都没匹配，取第一个
		}
		r := result.Results[pick]
		yr := ""
		if len(r.ReleaseDate) >= 4 {
			yr = r.ReleaseDate[:4]
		}
	// 获取详情中的 origin_country
	origCountry, _ := tc.getMovieDetails(r.ID)
	return &TmdbMedia{
		TmdbID:       r.ID,
		Title:        r.Title,
		OriginalTitle: r.OriginalTitle,
		Year:         yr,
		MediaType:    "movie",
		GenreIDs:     r.GenreIDs,
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		OrigLanguage: r.OriginalLanguage,
		OrigCountry:  origCountry,
		VoteAverage:  r.VoteAverage,
		}, nil
	}
	vlog("[整理] 搜索 %q（年份=%s）所有策略均无结果", query, year)
	return nil, nil
}

func absYear(a, b string) int {
	ai, bi := 0, 0
	fmt.Sscanf(a, "%d", &ai)
	fmt.Sscanf(b, "%d", &bi)
	d := ai - bi
	if d < 0 {
		d = -d
	}
	return d
}

// getMovieDetails 获取电影详情（origin_country）
func (tc *TmdbClient) getMovieDetails(id int) ([]string, error) {
	body, err := tc.get(fmt.Sprintf("/movie/%d", id), nil)
	if err != nil {
		return nil, err
	}
	var detail struct {
		OriginCountry []string `json:"origin_country"`
	}
	json.Unmarshal(body, &detail)
	return detail.OriginCountry, nil
}

// SearchTV 搜索电视剧
func (tc *TmdbClient) SearchTV(query string, year string) (*TmdbMedia, error) {
	if m, e, ok := tmdbCacheGet("tv", query, year); ok {
		return m, e
	}
	m, e := tc.searchTVUncached(query, year)
	tmdbCachePut("tv", query, year, m, e)
	return m, e
}

func (tc *TmdbClient) searchTVUncached(query string, year string) (*TmdbMedia, error) {
	params := map[string]string{"query": query}
	if year != "" {
		params["first_air_date_year"] = year // TMDB 剧集搜索的年份参数
	}
	body, err := tc.get("/search/tv", params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []struct {
			ID               int     `json:"id"`
			Name             string  `json:"name"`
			OriginalName     string  `json:"original_name"`
			FirstAirDate     string  `json:"first_air_date"`
			GenreIDs         []int   `json:"genre_ids"`
			Overview         string  `json:"overview"`
			PosterPath       string  `json:"poster_path"`
			BackdropPath     string  `json:"backdrop_path"`
			OriginalLanguage string  `json:"original_language"`
			VoteAverage      float64 `json:"vote_average"`
		} `json:"results"`
		TotalResults int `json:"total_results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.TotalResults == 0 || len(result.Results) == 0 {
		vlog("[整理] 搜索 %q（TV，年份=%s）无结果", query, year)
		return nil, nil
	}
	cands := make([]string, 0, 3)
	for _, c := range result.Results {
		if len(cands) >= 3 {
			break
		}
		cy := ""
		if len(c.FirstAirDate) >= 4 {
			cy = c.FirstAirDate[:4]
		}
		cands = append(cands, fmt.Sprintf("%s (%s) tmdb=%d 评分%.1f", c.Name, cy, c.ID, c.VoteAverage))
	}
	vlog("[整理] 搜索 %q（TV）候选 %d 个: %s", query, result.TotalResults, strings.Join(cands, " | "))
	r := result.Results[0]
	resultYear := ""
	if len(r.FirstAirDate) >= 4 {
		resultYear = r.FirstAirDate[:4]
	}
	origCountry, _ := tc.getTVDetails(r.ID)
	return &TmdbMedia{
		TmdbID:       r.ID,
		Title:        r.Name,
		OriginalTitle: r.OriginalName,
		Year:         resultYear,
		MediaType:    "tv",
		GenreIDs:     r.GenreIDs,
		Overview:     r.Overview,
		PosterPath:   r.PosterPath,
		BackdropPath: r.BackdropPath,
		OrigLanguage: r.OriginalLanguage,
		OrigCountry:  origCountry,
		VoteAverage:  r.VoteAverage,
	}, nil
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

// getTVDetails 获取电视剧详情（origin_country）
func (tc *TmdbClient) getTVDetails(id int) ([]string, error) {
	body, err := tc.get(fmt.Sprintf("/tv/%d", id), nil)
	if err != nil {
		return nil, err
	}
	var detail struct {
		OriginCountry []string `json:"origin_country"`
	}
	json.Unmarshal(body, &detail)
	return detail.OriginCountry, nil
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
}

var (
	// 季集模式：S01E02, s01e02, 1x02
	reSeasonEpisode = regexp.MustCompile(`[Ss](\d{1,2})[Ee](\d{1,3})`)
	// 仅集数模式：EP01 / E01 / 第01集（无季信息，季缺省为 1）
	// 前后必须有分隔符或边界，避免误吃 "WALL.E.2008" 这类片名
	reEpisodeOnly = regexp.MustCompile(`(?:^|[\.\s_-])(?:[Ee][Pp]?|第)(\d{1,3})(?:[集話话])?(?:$|[\.\s_-])`)
	// 动漫字幕组命名的方括号集数：[01] / [01v2]（vN=修正版）。
	// 限 1-3 位数字：4 位会被 "[2001]" 这类年份方括号误伤
	reBracketEpisode = regexp.MustCompile(`(?:^|[\.\s_-])\[(\d{1,3})(?:[vV]\d+)?\]`)
	// 仅季：Season 1, 第一季
	reSeasonOnly = regexp.MustCompile(`[Ss](\d{1,2})\b`)
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

// parseFileName 从视频文件名解析标题、年份、季集等信息
func parseFileName(filename string) *ParsedName {
	// 去掉文件后缀
	name := filename
	if idx := strings.LastIndex(name, "."); idx > 0 {
		name = name[:idx]
	}
	// 先剥离发布站广告（【…】块/域名），再解析
	name = stripReleaseAds(name)

	result := &ParsedName{}

	// 检测季集
	if m := reSeasonEpisode.FindStringSubmatch(name); m != nil {
		result.IsTV = true
		result.Season, _ = strconv.Atoi(m[1])
		result.Episode, _ = strconv.Atoi(m[2])
	} else if m := reEpisodeOnly.FindStringSubmatch(name); m != nil {
		// EP01 / E01 / 第01集：无季信息，缺省第 1 季
		result.IsTV = true
		result.Season = 1
		result.Episode, _ = strconv.Atoi(m[1])
	} else if m := reBracketEpisode.FindStringSubmatch(name); m != nil {
		// 动漫字幕组命名 [01] / [01v2]：无季信息，缺省第 1 季。
		// 同时剥离开头的 [字幕组] 前缀——仅在确认是这种命名形态时才剥，
		// 防止误伤 "[REC].2007" 这类以方括号开头的电影名
		result.IsTV = true
		result.Season = 1
		result.Episode, _ = strconv.Atoi(m[1])
		name = regexp.MustCompile(`^\[[^\]]*\]\s*`).ReplaceAllString(name, "")
	} else if m := reSeasonOnly.FindStringSubmatch(name); m != nil {
		result.IsTV = true
		result.Season, _ = strconv.Atoi(m[1])
	}

	// 检测分辨率
	if m := reResolution.FindStringSubmatch(name); m != nil {
		result.Resolution = strings.ToUpper(m[1])
	}

	// 检测年份
	if m := reYear.FindStringSubmatch(name); m != nil {
		result.Year = m[1]
	}

	// 提取标题：从开头到第一个发布标记/季集/年份处截断
	title := name

	// 将分隔符 . _ 替换为空格
	title = strings.ReplaceAll(title, ".", " ")
	title = strings.ReplaceAll(title, "_", " ")

	// 在季集标记处截断
	if idx := reSeasonEpisode.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}
	// 在仅集数标记（EP01/E01/第01集）处截断，避免集号污染标题搜索
	if idx := reEpisodeOnly.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}
	// 在方括号集数（[01]/[01v2]）处截断，同时去掉后面的编码/语言标签
	if idx := reBracketEpisode.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}
	if !result.IsTV {
		if idx := reSeasonOnly.FindStringIndex(title); idx != nil {
			title = title[:idx[0]]
		}
	}

	// 在发布标记处截断
	if idx := reReleaseMarkers.FindStringIndex(title); idx != nil {
		title = title[:idx[0]]
	}

	// 在年份处截断
	if result.Year != "" {
		if idx := strings.Index(title, result.Year); idx > 0 {
			title = title[:idx]
		}
	}

	// 清理首尾空格和标点
	title = strings.Trim(title, " -.")
	// 合并多个空格
	title = regexp.MustCompile(`\s+`).ReplaceAllString(title, " ")

	result.Title = title
	return result
}

// recognizeFile 通过 TMDB 识别文件
var reTmdbID = regexp.MustCompile(`\[tmdb=(\d+)\]`)

func extractTmdbID(name string) int {
	if m := reTmdbID.FindStringSubmatch(name); m != nil {
		id, _ := strconv.Atoi(m[1])
		return id
	}
	return 0
}

func (tc *TmdbClient) recognize(parsed *ParsedName) (*TmdbMedia, error) {
	if parsed.Title == "" {
		return nil, fmt.Errorf("无法从文件名提取标题")
	}

	// movieThenTV：先电影后剧集（CMS 同款兜底顺序）
	movieThenTV := func(q string) (*TmdbMedia, error) {
		media, err := tc.SearchMovie(q, parsed.Year)
		if err != nil {
			return nil, err
		}
		if media != nil {
			return media, nil
		}
		return tc.SearchTV(q, parsed.Year)
	}

	// 第一轮：原始标题（剧集直接搜 TV）
	var media *TmdbMedia
	var err error
	if parsed.IsTV {
		media, err = tc.SearchTV(parsed.Title, parsed.Year)
	} else {
		media, err = movieThenTV(parsed.Title)
	}
	if err != nil || media != nil {
		return media, err
	}

	// 第 1.5 轮：年份搜索无结果 → 去掉年份宽搜索，按年份最近匹配
	// 场景：文件名写 2018 但 TMDB 记为 2017（跨年上映/首播）
	if parsed.Year != "" {
		var wide *TmdbMedia
		if parsed.IsTV {
			wide, err = tc.SearchTV(parsed.Title, "")
		} else {
			wide, err = tc.SearchMovie(parsed.Title, "")
		}
		if err == nil && wide != nil {
			// 如果唯一结果或年份差 ≤1 年，接受
			yearDiff := absYearDiff(wide.Year, parsed.Year)
			if yearDiff <= 1 {
				log.Printf("[整理] 年份宽搜索命中: %q 年份=%s（文件名=%s，差 %d 年）",
					wide.Title, wide.Year, parsed.Year, yearDiff)
				return wide, nil
			}
			// 如果多个结果，尝试找年份最接近的（在 SearchXxx 内已取第一个，
			// 这里只处理唯一结果的情况；多结果的精确匹配需要改 SearchXxx 返回列表）
			log.Printf("[整理] 年份宽搜索: 找到 %q 年份=%s，与文件名 %s 差 %d 年，跳过",
				wide.Title, wide.Year, parsed.Year, yearDiff)
		}
	}

	// 第二轮：清洗后的标题重试（去掉特殊字符/残留标记，压紧空白）
	cleaned := cleanSearchTitle(parsed.Title)
	if cleaned != "" && cleaned != parsed.Title {
		log.Printf("[整理] 首次搜索无结果，用清洗标题重试: %q → %q", parsed.Title, cleaned)
		if parsed.IsTV {
			media, err = tc.SearchTV(cleaned, parsed.Year)
		} else {
			media, err = movieThenTV(cleaned)
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
				media, err = tc.SearchTV(q, parsed.Year)
			} else {
				media, err = movieThenTV(q)
			}
			if err != nil || media != nil {
				return media, err
			}
		}
	}

	// 第三轮：GPT 兜底（配置了 GPT 识别时）——从原始文件名提取标题/年份再搜
	if gptCfg := loadGPTFallback(); gptCfg != nil {
		if ext := gptExtract(gptCfg, parsed.Title); ext != nil && ext.Title != "" && ext.Title != parsed.Title {
			log.Printf("[整理] GPT 兜底提取: %q → %q (%s)", parsed.Title, ext.Title, ext.Year)
			p2 := *parsed
			p2.Title = ext.Title
			p2.Year = ext.Year
			if p2.Year == "" {
				p2.Year = parsed.Year
			}
			if parsed.IsTV {
				return tc.SearchTV(p2.Title, parsed.Year)
			}
			return movieThenTV(p2.Title)
		}
	}
	return media, nil
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

// gptFallbackCfg GPT 识别配置（org-gpt 设置）
type gptFallbackCfg struct {
	URL   string
	Key   string
	Model string
}

var gptFallbackCfgCache struct {
	val *gptFallbackCfg
	at  time.Time
}

// loadGPTFallback 读取 GPT 兜底配置（5 分钟缓存；未配置返回 nil）
func loadGPTFallback() *gptFallbackCfg {
	if gptFallbackCfgCache.val != nil && time.Since(gptFallbackCfgCache.at) < 5*time.Minute {
		return gptFallbackCfgCache.val
	}
	v := settingValueCompat("org-gpt")
	var cfg struct {
		URL   string `json:"url"`
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	gptFallbackCfgCache.val = nil
	if v != "" && json.Unmarshal([]byte(v), &cfg) == nil && cfg.URL != "" && cfg.Key != "" {
		if cfg.Model == "" {
			cfg.Model = "gpt-4o-mini"
		}
		gptFallbackCfgCache.val = &gptFallbackCfg{URL: cfg.URL, Key: cfg.Key, Model: cfg.Model}
	}
	gptFallbackCfgCache.at = time.Now()
	return gptFallbackCfgCache.val
}

// gptExtract 用 GPT 从文件名提取 标题/年份
type gptExtractResult struct {
	Title string
	Year  string
}

func gptExtract(cfg *gptFallbackCfg, filename string) *gptExtractResult {
	if filename == "" {
		return nil
	}
	payload := map[string]interface{}{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "从影视文件名中提取标准标题和上映/开播年份。只输出 JSON：{\"title\":\"...\",\"year\":\"...\"}，找不到年份输出空字符串。"},
			{"role": "user", "content": filename},
		},
		"temperature": 0,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.URL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[整理] 调用失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[整理] HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 100))
		return nil
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Choices) == 0 {
		return nil
	}
	content := r.Choices[0].Message.Content
	m := regexp.MustCompile(`\{[^}]*\}`).FindString(content)
	if m == "" {
		return nil
	}
	var out gptExtractResult
	if json.Unmarshal([]byte(m), &out) != nil || out.Title == "" {
		return nil
	}
	return &out
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
