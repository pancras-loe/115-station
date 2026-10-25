package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== TMDB 榜单（影视转存「找资源」页的趋势 / 热门） ====================
//
// 形态参考 MoviePilot 的「推荐」（app/chain/recommend.py 的 tmdb_trending / tmdb_movies / tmdb_tvs，
// 只看思路）：几个固定榜单铺成海报墙，点一部就进选片后的找资源流程。
//
// 只接 TMDB，不接豆瓣：豆瓣榜单要用逆向出来的 frodo 接口密钥签名，条目也没有 TMDB 编号，
// 得按片名年份反查（MediaSync115 为此写了上千行映射与缓存），反查认错一次，
// 后面的找资源、订阅就全挂在错的条目上。TMDB 榜单的条目自带编号，和搜索出来的候选是同一种东西。

// discoverList 一个榜单：TMDB 端点 + 固定参数
type discoverList struct {
	key, label string
	path       string
	kind       string // movie / tv；空 = 结果里自带 media_type（trending/all）
	params     map[string]string
}

// tmdbDiscoverLists 榜单顺序即前端页签顺序。
// 地区榜单用 discover 按热度排，并排除纪录片 / 新闻 / 脱口秀（tmdbMinorGenres），否则国产剧前排全是访谈节目
var tmdbDiscoverLists = []discoverList{
	{key: "trending_day", label: "今日趋势", path: "/trending/all/day"},
	{key: "trending_week", label: "本周趋势", path: "/trending/all/week"},
	{key: "movie_popular", label: "热门电影", path: "/movie/popular", kind: "movie"},
	{key: "tv_popular", label: "热门剧集", path: "/tv/popular", kind: "tv"},
	{key: "movie_now", label: "正在热映", path: "/movie/now_playing", kind: "movie"},
	{key: "tv_air", label: "正在播出", path: "/tv/on_the_air", kind: "tv"},
	{key: "tv_cn", label: "国产剧", path: "/discover/tv", kind: "tv", params: map[string]string{
		"with_origin_country": "CN", "sort_by": "popularity.desc", "without_genres": "16,99,10763,10767",
	}},
	{key: "tv_kr", label: "韩剧", path: "/discover/tv", kind: "tv", params: map[string]string{
		"with_origin_country": "KR", "sort_by": "popularity.desc", "without_genres": "16,99,10763,10767",
	}},
	{key: "anime_jp", label: "日本动画", path: "/discover/tv", kind: "tv", params: map[string]string{
		"with_origin_country": "JP", "with_genres": "16", "sort_by": "popularity.desc",
	}},
}

// ---- 按条件筛选（list=filter）：电影 / 剧集 + 国家地区 + 题材，走 /discover/{movie|tv} 按热度排 ----

// discoverOpt 筛选项的一个选项（value 原样进 TMDB 参数）
type discoverOpt struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// tmdbDiscoverCountries 地区：按 with_origin_country 筛（制片国家，电影剧集都认）
var tmdbDiscoverCountries = []discoverOpt{
	{"CN", "中国大陆"}, {"HK", "中国香港"}, {"TW", "中国台湾"}, {"US", "美国"}, {"GB", "英国"},
	{"JP", "日本"}, {"KR", "韩国"}, {"TH", "泰国"}, {"IN", "印度"}, {"FR", "法国"},
	{"DE", "德国"}, {"ES", "西班牙"}, {"IT", "意大利"}, {"CA", "加拿大"}, {"AU", "澳大利亚"},
	{"RU", "俄罗斯"},
}

// tmdbDiscoverGenres 题材：电影与剧集的 TMDB 类型表不一样（剧集的动作冒险、科幻奇幻是合在一起的），分开列。
// 纪录片 / 新闻 / 脱口秀不列：榜单会丢掉 minor 条目（parseDiscoverPage），选了也是空的
var tmdbDiscoverGenres = map[string][]discoverOpt{
	"movie": {
		{"28", "动作"}, {"12", "冒险"}, {"16", "动画"}, {"35", "喜剧"}, {"80", "犯罪"},
		{"18", "剧情"}, {"10751", "家庭"}, {"14", "奇幻"}, {"36", "历史"}, {"27", "恐怖"},
		{"10402", "音乐"}, {"9648", "悬疑"}, {"10749", "爱情"}, {"878", "科幻"}, {"53", "惊悚"},
		{"10752", "战争"}, {"37", "西部"},
	},
	"tv": {
		{"10759", "动作冒险"}, {"16", "动画"}, {"35", "喜剧"}, {"80", "犯罪"}, {"18", "剧情"},
		{"10751", "家庭"}, {"10762", "儿童"}, {"9648", "悬疑"}, {"10764", "真人秀"},
		{"10765", "科幻奇幻"}, {"10766", "肥皂剧"}, {"10768", "战争政治"}, {"37", "西部"},
	},
}

func discoverOptOK(opts []discoverOpt, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

// discoverFilterList 把筛选条件拼成一个临时榜单；参数只认清单里有的，别的值一律拒收（也就不会拿用户输入去拼 TMDB 请求）
func discoverFilterList(kind, country, genre string) (discoverList, error) {
	if kind != "movie" && kind != "tv" {
		return discoverList{}, errors.New("类型只能是 movie / tv")
	}
	if country != "" && !discoverOptOK(tmdbDiscoverCountries, country) {
		return discoverList{}, errors.New("未知的地区: " + country)
	}
	if genre != "" && !discoverOptOK(tmdbDiscoverGenres[kind], genre) {
		return discoverList{}, errors.New("未知的题材: " + genre)
	}
	params := map[string]string{"sort_by": "popularity.desc", "without_genres": "99,10763,10767"}
	if country != "" {
		params["with_origin_country"] = country
	}
	if genre != "" {
		params["with_genres"] = genre
	}
	return discoverList{
		key:  "filter:" + kind + ":" + country + ":" + genre,
		path: "/discover/" + kind, kind: kind, params: params,
	}, nil
}

// tmdbDiscoverMaxPage 最多翻几页：榜单越往后越冷门，翻到第十页（200 条）早就不是「热门」了
const tmdbDiscoverMaxPage = 10

// tmdbDiscoverTTL 榜单缓存：趋势一天一变，半小时内反复切页签不再外呼
const tmdbDiscoverTTL = 30 * time.Minute

type discoverPage struct {
	items   []manualCand
	hasMore bool
	at      time.Time
}

var (
	tmdbDiscoverMu    sync.Mutex
	tmdbDiscoverCache = map[string]discoverPage{}
)

// discoverRaw 榜单接口的一条：trending/all 里混着人物，靠 media_type 分开
type discoverRaw struct {
	tmdbRawItem
	MediaType string `json:"media_type"`
}

// parseDiscoverPage 把一页榜单换成候选：丢掉人物、纪录片 / 脱口秀 / 没海报的（minor）。
// 搜索时 minor 只是排后面（偶尔真要找），榜单是拿来逛的，混进来只是噪音
func parseDiscoverPage(body []byte, l discoverList) ([]manualCand, int, error) {
	var r struct {
		Results    []discoverRaw `json:"results"`
		TotalPages int           `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, 0, err
	}
	out := make([]manualCand, 0, len(r.Results))
	for _, it := range r.Results {
		kind := l.kind
		if kind == "" {
			kind = it.MediaType
		}
		if kind != "movie" && kind != "tv" {
			continue
		}
		c := it.cand(kind)
		if c.ID == 0 || c.minor {
			continue
		}
		out = append(out, c)
	}
	return out, r.TotalPages, nil
}

// ---- 排序（sort=vote / date）----
//
// TMDB 的 trending / popular / now_playing 这些端点不收 sort_by；discover 收，但按评分排的是全库，
// 出来的是几票的冷门片，已经不是「这个榜单」了。所以排序一律在榜单本身的前几页里排：
// 先按榜单原来的顺序取前 tmdbDiscoverSortPool 页，去重后整体重排，再按每页 20 条切给前端。
// 意思是「本周趋势里评分最高的」，而不是「TMDB 上评分最高的」。

// tmdbDiscoverSortPool 排序时取榜单前几页（100 部左右）。再往后越冷门，排进来反而把热门的挤下去
const tmdbDiscoverSortPool = 5

// tmdbDiscoverPageSize 排序后每页切几条（同 TMDB 一页的条数，前端翻页手感不变）
const tmdbDiscoverPageSize = 20

// tmdbDiscoverMinVotes 评分人数不到这个数的按评分排时放到后面：刚上线的片三五个人打了 9 分，排第一没意义
const tmdbDiscoverMinVotes = 20

// discoverSortOK 认得的排序；空 = 榜单原顺序（热度）
func discoverSortOK(s string) bool {
	return s == "" || s == "vote" || s == "date"
}

// sortDiscoverPool 按 vote（评分高到低，人数太少的垫底）或 date（上映 / 首播新到旧，没日期的垫底）重排。
// 稳定排序：分数一样的保持榜单原来的先后
func sortDiscoverPool(items []manualCand, by string) {
	switch by {
	case "vote":
		slices.SortStableFunc(items, func(a, b manualCand) int {
			fa, fb := a.voteCount >= tmdbDiscoverMinVotes, b.voteCount >= tmdbDiscoverMinVotes
			if fa != fb {
				if fa {
					return -1
				}
				return 1
			}
			return cmp.Compare(b.Vote, a.Vote)
		})
	case "date":
		slices.SortStableFunc(items, func(a, b manualCand) int {
			if (a.date == "") != (b.date == "") {
				if a.date == "" {
					return 1
				}
				return -1
			}
			return cmp.Compare(b.date, a.date) // YYYY-MM-DD 按字符串比就是按时间比
		})
	}
}

// discoverFetch 读榜单的一页（原顺序），半小时缓存
func discoverFetch(tc *TmdbClient, list discoverList, key string, page int) (discoverPage, error) {
	// 语言进缓存键：改了 TMDB 语言设置，片名跟着换
	ck := key + "|" + strconv.Itoa(page) + "|" + tc.Language
	tmdbDiscoverMu.Lock()
	hit, ok := tmdbDiscoverCache[ck]
	tmdbDiscoverMu.Unlock()
	if ok && time.Since(hit.at) < tmdbDiscoverTTL {
		return hit, nil
	}

	params := map[string]string{"page": strconv.Itoa(page), "include_adult": "false"}
	for k, v := range list.params {
		params[k] = v
	}
	body, err := tc.get(list.path, params)
	if err != nil {
		return discoverPage{}, errors.New("TMDB 榜单读取失败: " + err.Error())
	}
	items, total, err := parseDiscoverPage(body, list)
	if err != nil {
		return discoverPage{}, errors.New("TMDB 榜单解析失败: " + err.Error())
	}
	pg := discoverPage{items: items, hasMore: page < min(total, tmdbDiscoverMaxPage), at: time.Now()}
	tmdbDiscoverMu.Lock()
	for k, v := range tmdbDiscoverCache { // 顺手清过期的，榜单键就几十个，不另起清理
		if time.Since(v.at) >= tmdbDiscoverTTL {
			delete(tmdbDiscoverCache, k)
		}
	}
	tmdbDiscoverCache[ck] = pg
	tmdbDiscoverMu.Unlock()
	return pg, nil
}

// discoverSorted 排序后的第 page 页：取前 tmdbDiscoverSortPool 页（第一页之后并发读，各页走同一份缓存），
// 去重、重排、切页。第一页之外读失败的页直接略过，少几部不影响排序结果能看
func discoverSorted(tc *TmdbClient, list discoverList, key string, page int, by string) ([]manualCand, bool, error) {
	first, err := discoverFetch(tc, list, key, 1)
	if err != nil {
		return nil, false, err
	}
	pages := []discoverPage{first}
	if first.hasMore {
		rest := make([]discoverPage, tmdbDiscoverSortPool-1)
		var wg sync.WaitGroup
		for i := range rest {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if pg, err := discoverFetch(tc, list, key, i+2); err == nil {
					rest[i] = pg
				}
			}(i)
		}
		wg.Wait()
		pages = append(pages, rest...)
	}
	var pool []manualCand
	seen := map[string]bool{}
	for _, pg := range pages {
		for _, c := range pg.items {
			k := c.MediaType + ":" + strconv.Itoa(c.ID)
			if !seen[k] { // 翻页期间排名会变，同一部可能在两页各出现一次
				seen[k] = true
				pool = append(pool, c)
			}
		}
	}
	sortDiscoverPool(pool, by)
	lo := min((page-1)*tmdbDiscoverPageSize, len(pool))
	hi := min(lo+tmdbDiscoverPageSize, len(pool))
	return pool[lo:hi], hi < len(pool), nil
}

// TmdbDiscover GET /tmdb/discover?list=trending_day&page=1&sort=vote
// list=filter 时按 kind / country / genre 筛选（discoverFilterList）；sort 见 sortDiscoverPool，空 = 原顺序。
// 不带 list 时只返回榜单清单（前端页签）与筛选项
func (h *Handler) TmdbDiscover(c *gin.Context) {
	key := c.Query("list")
	if key == "" {
		lists := make([]gin.H, 0, len(tmdbDiscoverLists))
		for _, l := range tmdbDiscoverLists {
			lists = append(lists, gin.H{"key": l.key, "label": l.label})
		}
		c.JSON(http.StatusOK, gin.H{"lists": lists, "countries": tmdbDiscoverCountries, "genres": tmdbDiscoverGenres})
		return
	}
	sortBy := c.Query("sort")
	if !discoverSortOK(sortBy) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未知的排序: " + sortBy})
		return
	}
	var list *discoverList
	if key == "filter" {
		l, err := discoverFilterList(c.Query("kind"), c.Query("country"), c.Query("genre"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		list, key = &l, l.key
	}
	for i := 0; list == nil && i < len(tmdbDiscoverLists); i++ {
		if tmdbDiscoverLists[i].key == key {
			list = &tmdbDiscoverLists[i]
			break
		}
	}
	if list == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未知的榜单: " + key})
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))

	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error() + "（先在系统配置完成 TMDB 设置）"})
		return
	}
	if sortBy != "" {
		page = min(max(page, 1), tmdbDiscoverSortPool)
		items, more, err := discoverSorted(tc, *list, key, page, sortBy)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": items, "has_more": more})
		return
	}
	page = min(max(page, 1), tmdbDiscoverMaxPage)
	pg, err := discoverFetch(tc, *list, key, page)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": pg.items, "has_more": pg.hasMore})
}
