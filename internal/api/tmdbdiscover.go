package api

import (
	"encoding/json"
	"net/http"
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

// TmdbDiscover GET /tmdb/discover?list=trending_day&page=1
// 不带 list 时只返回榜单清单（前端页签）
func (h *Handler) TmdbDiscover(c *gin.Context) {
	key := c.Query("list")
	if key == "" {
		lists := make([]gin.H, 0, len(tmdbDiscoverLists))
		for _, l := range tmdbDiscoverLists {
			lists = append(lists, gin.H{"key": l.key, "label": l.label})
		}
		c.JSON(http.StatusOK, gin.H{"lists": lists})
		return
	}
	var list *discoverList
	for i := range tmdbDiscoverLists {
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
	page = min(max(page, 1), tmdbDiscoverMaxPage)

	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error() + "（先在系统配置完成 TMDB 设置）"})
		return
	}
	// 语言进缓存键：改了 TMDB 语言设置，片名跟着换
	ck := key + "|" + strconv.Itoa(page) + "|" + tc.Language
	tmdbDiscoverMu.Lock()
	hit, ok := tmdbDiscoverCache[ck]
	tmdbDiscoverMu.Unlock()
	if ok && time.Since(hit.at) < tmdbDiscoverTTL {
		c.JSON(http.StatusOK, gin.H{"data": hit.items, "has_more": hit.hasMore})
		return
	}

	params := map[string]string{"page": strconv.Itoa(page), "include_adult": "false"}
	for k, v := range list.params {
		params[k] = v
	}
	body, err := tc.get(list.path, params)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 榜单读取失败: " + err.Error()})
		return
	}
	items, total, err := parseDiscoverPage(body, *list)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 榜单解析失败: " + err.Error()})
		return
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
	c.JSON(http.StatusOK, gin.H{"data": pg.items, "has_more": pg.hasMore})
}
