package api

// ==================== 观影门户（默认 6688） ====================
//
// 独立于管理后台的公开门户：海报墙 + 分类浏览 + 网页直接播放。
// 播放走 6086 的 302 直链（浏览器能播的编码直出：MP4/H.264；
// MKV/H.265 视浏览器能力而定）。海报经本服务代理并落盘缓存，
// 局网客户端无需访问 TMDB。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var portalCfg *config.Config

// StartPortal 启动观影门户（独立端口，公开访问）。
// 可选 PIN 码（系统配置 → 门户访问）：设置后所有路由须携带有效会话 cookie，
// 未通过者拿到 PIN 输入页——公网部署下门户默认全裸（直链/海报/播放全开放）
func StartPortal(cfg *config.Config) {
	portalCfg = cfg
	r := gin.New()
	r.Use(gin.Recovery(), portalAuthGuard)

	r.GET("/", portalPage)
	r.GET("/api/portal/nav", portalNav)
	r.GET("/api/portal/list", portalList)
	r.GET("/api/portal/detail", portalDetail)
	r.GET("/api/portal/trending", portalTrending)
	r.POST("/api/portal/played", portalPlayed)
	r.GET("/poster/*path", portalPoster)
	r.GET("/api/portal/sub", portalSub)
	r.GET("/api/portal/probe", portalProbe)
	r.GET("/api/portal/hls/:sid/*file", portalHLSServe)
	r.GET("/api/portal/hls", portalHLSServe)
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/svg+xml", []byte("<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><text y='13' font-size='13'>🎬</text></svg>"))
	})
	r.POST("/api/portal/hls/start", portalHLS)
	r.GET("/api/portal/estr", portalExtractSub)
	r.GET("/api/portal/embyplay", portalEmbyPlay)
	r.Any("/api/portal/emby/*path", embyProxy)
	// PIN 会话（guard 下方注册：通过 guard 后才会到达）
	r.POST("/api/portal/auth", portalAuthLogin)

	go portalBackfillWorker()
	go hlsCleaner()
	addr := ":" + fmt.Sprint(cfg.PortalPort)
	log.Printf("观影门户已启动（端口 %d）", cfg.PortalPort)
	if err := r.Run(addr); err != nil {
		log.Printf("门户启动失败（端口 %d 被占用？）: %v", cfg.PortalPort, err)
	}
}

// ==================== 门户访问认证（与管理后台同一套账号密码） ====================
//
// 首次访问任意门户页面 → 未携带有效会话 cookie 时返回登录页；
// 登录成功下发 JWT cookie（与 6060 同一 JWTSecret、同一 auth.yaml 账号），
// 30 天有效。改密码（环境变量）后门户会话同样需要重新登录。

// portalSessionCookie 会话 cookie 名
const portalSessionCookie = "portal_session"

// portalAuthGuard 门户门卫：无有效会话时页面请求回登录页、API 请求回 401
func portalAuthGuard(c *gin.Context) {
	if c.Request.URL.Path == "/api/portal/auth" {
		c.Next() // 登录端点自身放行
		return
	}
	if _, err := c.Cookie(portalSessionCookie); err == nil && portalSessionValid(c) {
		c.Next()
		return
	}
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth_required"})
		c.Abort()
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(portalLoginHTML))
	c.Abort()
}

// portalSessionValid 校验会话 cookie 是否为有效 JWT
func portalSessionValid(c *gin.Context) bool {
	tok, err := c.Cookie(portalSessionCookie)
	if err != nil || tok == "" {
		return false
	}
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(portalCfg.JWTSecret), nil
	})
	return err == nil
}

// portalAuthLogin POST /api/portal/auth {"username","password"} → 校验与
// 管理后台一致的账号密码，成功下发会话 cookie
func portalAuthLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&req)
	// 防爆破：与管理后台共用 loginGuard（portal: 前缀区分来源）。
	// 门户与后台同一套凭据，不能让公网在这里无限速试密码
	key := "portal:" + c.ClientIP()
	if remain := loginGuardCheck(key); remain > 0 {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "失败次数过多，请 " + remain.Truncate(time.Second).String() + " 后再试"})
		return
	}
	if !portalCfg.VerifyAuth(strings.TrimSpace(req.Username), req.Password) {
		loginGuardFail(key)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}
	loginGuardPass(key)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"scope": "portal",
		"exp":   time.Now().Add(30 * 24 * time.Hour).Unix(),
	})
	signed, _ := token.SignedString([]byte(portalCfg.JWTSecret))
	c.SetCookie(portalSessionCookie, signed, 30*24*3600, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// portalLoginHTML 门户登录页（暗色，与门户风格一致；账号密码与管理后台相同）
const portalLoginHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>StrmHub · 登录</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f1115;color:#e5eaf3;font-family:system-ui,-apple-system,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif}
.card{background:#1a1e27;border:1px solid #2a3040;border-radius:14px;padding:36px 30px;width:min(92vw,360px);text-align:center}
h1{font-size:18px;margin:0 0 6px}p{color:#8b93a7;font-size:13px;margin:0 0 22px}
input{width:100%;box-sizing:border-box;padding:12px 14px;border-radius:8px;border:1px solid #2a3040;background:#12151d;color:#e5eaf3;font-size:15px;outline:none;margin-bottom:12px}
input:focus{border-color:#7c3aed}
button{width:100%;margin-top:6px;padding:12px;border:none;border-radius:8px;background:#7c3aed;color:#fff;font-size:15px;cursor:pointer}
button:hover{background:#8b5cf6}
.err{color:#ef4444;font-size:13px;min-height:18px;margin-top:10px}
</style></head><body>
<div class="card"><h1>🎬 观影门户</h1><p>请登录（与管理后台相同的账号密码）</p>
<form id="f">
<input id="u" placeholder="账号" autocomplete="username" autofocus>
<input id="p" type="password" placeholder="密码" autocomplete="current-password">
<button type="submit">登 录</button><div class="err" id="err"></div>
</form></div>
<script>
document.getElementById('f').addEventListener('submit',async e=>{
  e.preventDefault();
  const r=await fetch('/api/portal/auth',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({username:document.getElementById('u').value.trim(),password:document.getElementById('p').value})});
  if(r.ok){location.reload()}else{try{const d=await r.json();document.getElementById('err').textContent=d.error||'登录失败'}catch(_){document.getElementById('err').textContent='登录失败'}}
});
</script></body></html>`

// portalTitleEntry 从台账聚合出的一个标题条目
type portalTitleEntry struct {
	Key       string // 标题目录的相对路径（详情查询用）
	Title     string
	Year      string
	TmdbID    int
	MediaType string
	Category  string
	LastAt    time.Time
}

// 台账扫描缓存：门户列表/详情/排行榜/回填共享同一份结果（30 秒 TTL），
// 避免每次请求都全表扫描 SyncedFile。缓存条目视为只读，调用方不得修改。
var (
	portalLedgerMu    sync.Mutex
	portalLedgerCache map[string]*portalTitleEntry
	portalLedgerAt    time.Time
)

func portalScanLedgerCached() map[string]*portalTitleEntry {
	portalLedgerMu.Lock()
	defer portalLedgerMu.Unlock()
	if portalLedgerCache != nil && time.Since(portalLedgerAt) < 30*time.Second {
		return portalLedgerCache
	}
	portalLedgerCache = portalScanLedger()
	portalLedgerAt = time.Now()
	return portalLedgerCache
}

// portalScanLedger 全量扫描台账，按"标题目录"聚合（库名/电影|剧集/分类/标题目录/…）
func portalScanLedger() map[string]*portalTitleEntry {
	var sfs []model.SyncedFile
	model.DB.Where("kind = ?", "video").Find(&sfs)
	out := map[string]*portalTitleEntry{}
	for _, sf := range sfs {
		segs := strings.Split(strings.Trim(sf.RelPath, "/"), "/")
		if len(segs) < 3 {
			continue
		}
		var mediaType, category, titleDir string
		if len(segs) >= 4 {
			mediaType, category, titleDir = segs[1], segs[2], segs[3]
		} else {
			mediaType, category, titleDir = segs[0], "", segs[1]
		}
		if mediaType != "电影" && mediaType != "剧集" {
			continue
		}
		key := strings.Join(segs[:4], "/")
		e, ok := out[key]
		if !ok {
			title, year, tmdb := parseTitleDir(titleDir)
			e = &portalTitleEntry{
				Key: key, Title: title, Year: year, TmdbID: tmdb,
				MediaType: map[string]string{"电影": "movie", "剧集": "tv"}[mediaType],
				Category:  category,
			}
			out[key] = e
		}
		if sf.UpdatedAt.After(e.LastAt) {
			e.LastAt = sf.UpdatedAt
		}
	}
	return out
}

// parseTitleDir 解析标题目录名："Z-重器-2026-[tmdb=291856]" → (重器, 2026, 291856)
func parseTitleDir(dir string) (title, year string, tmdb int) {
	tmdb = 0
	if m := regexp.MustCompile(`\[tmdb=(\d+)\]`).FindStringSubmatch(dir); m != nil {
		tmdb, _ = strconv.Atoi(m[1])
	}
	// 取最右侧的年份（目录名惯例是 标题-年份-[tmdb=x]；取最左会把
	// 片名本身是年份的 "1917-2019" 剜成 "2019"）
	year = ""
	reYear := regexp.MustCompile(`(?:^|[-. ])((?:19|20)\d{2})(?:$|[-. ])`)
	if ms := reYear.FindAllStringSubmatchIndex(dir, -1); len(ms) > 0 {
		last := ms[len(ms)-1]
		year = dir[last[2]:last[3]]
	}
	title = dir
	title = regexp.MustCompile(`\[tmdb=\d+\]`).ReplaceAllString(title, "")
	if year != "" {
		if i := strings.LastIndex(title, year); i >= 0 {
			title = title[:i] + title[i+len(year):]
		}
	}
	title = regexp.MustCompile(`^[A-Z\d]-`).ReplaceAllString(title, "")
	title = strings.Trim(title, "- _.[]")
	if title == "" {
		title = dir
	}
	return
}

// langNames / countryNames 常用 TMDB 代码 → 中文名（筛选器展示用，未收录原样显示）
var langNames = map[string]string{
	"zh": "中文", "en": "英语", "ja": "日语", "ko": "韩语", "th": "泰语",
	"fr": "法语", "de": "德语", "ru": "俄语", "es": "西班牙语", "it": "意大利语",
	"pt": "葡萄牙语", "hi": "印地语", "tl": "菲律宾语", "ar": "阿拉伯语", "sv": "瑞典语",
	"da": "丹麦语", "no": "挪威语", "nl": "荷兰语", "pl": "波兰语", "tr": "土耳其语",
	"he": "希伯来语", "fa": "波斯语", "id": "印尼语", "vi": "越南语", "ms": "马来语",
}

var countryNames = map[string]string{
	"CN": "中国大陆", "HK": "中国香港", "TW": "中国台湾", "US": "美国", "GB": "英国",
	"JP": "日本", "KR": "韩国", "FR": "法国", "DE": "德国", "TH": "泰国", "IN": "印度",
	"ES": "西班牙", "IT": "意大利", "CA": "加拿大", "AU": "澳大利亚", "RU": "俄罗斯",
	"BR": "巴西", "MX": "墨西哥", "SE": "瑞典", "DK": "丹麦", "NO": "挪威", "NL": "荷兰",
	"BE": "比利时", "NZ": "新西兰", "IE": "爱尔兰", "SG": "新加坡", "MY": "马来西亚",
	"PH": "菲律宾", "ID": "印度尼西亚", "VN": "越南", "TR": "土耳其", "PL": "波兰",
	"CZ": "捷克", "AR": "阿根廷", "ZA": "南非", "IL": "以色列", "IR": "伊朗", "GR": "希腊",
}

func langName(code string) string {
	if n, ok := langNames[code]; ok {
		return n
	}
	return code
}

func countryName(code string) string {
	if n, ok := countryNames[code]; ok {
		return n
	}
	return code
}

// portalFacets 聚合某媒体类型的可选筛选值（国家/语言/类型，按条目数降序）。
// 多值字段（国家/类型）拆开计数
type facetCounter struct {
	code, name string
	n          int
}

func facetList(m map[string]*facetCounter) []map[string]interface{} {
	all := make([]facetCounter, 0, len(m))
	for _, v := range m {
		all = append(all, *v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].n > all[j].n })
	out := make([]map[string]interface{}, 0, len(all))
	for _, c := range all {
		out = append(out, map[string]interface{}{"code": c.code, "name": c.name, "count": c.n})
	}
	return out
}

func portalFacets(mt string) (countries, langs, genres []map[string]interface{}) {
	cCount := map[string]*facetCounter{}
	lCount := map[string]*facetCounter{}
	gCount := map[string]*facetCounter{}
	var mls []model.MediaLibrary
	model.DB.Where("media_type = ?", mt).Find(&mls)
	for _, m := range mls {
		for _, code := range strings.Split(m.OrigCountry, ",") {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			if c, ok := cCount[code]; ok {
				c.n++
			} else {
				cCount[code] = &facetCounter{code: code, name: countryName(code), n: 1}
			}
		}
		for _, code := range strings.Split(m.OrigLanguage, ",") {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			if c, ok := lCount[code]; ok {
				c.n++
			} else {
				lCount[code] = &facetCounter{code: code, name: langName(code), n: 1}
			}
		}
		for _, name := range strings.Split(m.Genres, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if c, ok := gCount[name]; ok {
				c.n++
			} else {
				gCount[name] = &facetCounter{code: name, name: name, n: 1}
			}
		}
	}
	return facetList(cCount), facetList(lCount), facetList(gCount)
}

// portalNav 分类导航（按台账实际内容聚合）
func portalNav(c *gin.Context) {
	entries := portalScanLedgerCached()
	counts := map[string]map[string]int{}
	for _, e := range entries {
		if counts[e.MediaType] == nil {
			counts[e.MediaType] = map[string]int{}
		}
		if e.Category != "" {
			counts[e.MediaType][e.Category]++
		}
	}
	nav := map[string][]map[string]interface{}{}
	facets := map[string]map[string]interface{}{}
	for _, mt := range []string{"movie", "tv"} {
		for name, n := range counts[mt] {
			nav[mt] = append(nav[mt], map[string]interface{}{"name": name, "count": n})
		}
		sort.Slice(nav[mt], func(i, j int) bool { return nav[mt][i]["name"].(string) < nav[mt][j]["name"].(string) })
		cs, ls, gs := portalFacets(mt)
		facets[mt] = gin.H{"countries": cs, "langs": ls, "genres": gs}
	}
	c.JSON(http.StatusOK, gin.H{"nav": nav, "facets": facets})
}

// portalList 媒体列表（全量台账聚合；元数据从整理记录合并）
func portalList(c *gin.Context) {
	mt := c.Query("type")
	if mt != "movie" && mt != "tv" {
		mt = "movie"
	}
	page := 1
	fmt.Sscanf(c.Query("page"), "%d", &page)
	if page < 1 {
		page = 1
	}
	size := 36
	if v, e := strconv.Atoi(c.Query("size")); e == nil && v > 0 && v <= 48 {
		size = v
	}
	cat := c.Query("cat")
	kw := strings.TrimSpace(c.Query("q"))
	fCountry := strings.TrimSpace(c.Query("country"))
	fLang := strings.TrimSpace(c.Query("lang"))
	fGenre := strings.TrimSpace(c.Query("genre"))

	type enriched struct {
		e      *portalTitleEntry
		poster string
		vote   float64
		ml     model.MediaLibrary
	}
	// 元数据批量合并（避免逐条查库）
	metaByID := map[int]model.MediaLibrary{}
	if mls := ([]model.MediaLibrary)(nil); true {
		model.DB.Where("media_type = ?", mt).Find(&mls)
		for _, m := range mls {
			if m.TmdbID > 0 {
				metaByID[m.TmdbID] = m
			}
		}
	}
	entries := make([]enriched, 0)
	for _, e := range portalScanLedgerCached() {
		if e.MediaType != mt {
			continue
		}
		if cat != "" && e.Category != cat {
			continue
		}
		if kw != "" && !strings.Contains(e.Title, kw) {
			continue
		}
		en := enriched{e: e}
		if e.TmdbID > 0 {
			if ml, ok := metaByID[e.TmdbID]; ok {
				en.poster = ml.PosterPath
				en.vote = ml.VoteAverage
				en.ml = ml
			}
		}
		// 多维筛选：条目无元数据时无法匹配，选中筛选即排除
		if fCountry != "" && !strings.Contains(","+en.ml.OrigCountry+",", ","+fCountry+",") {
			continue
		}
		if fLang != "" && !strings.Contains(","+en.ml.OrigLanguage+",", ","+fLang+",") {
			continue
		}
		if fGenre != "" && !strings.Contains(","+en.ml.Genres+",", ","+fGenre+",") {
			continue
		}
		entries = append(entries, en)
	}
	switch c.DefaultQuery("sort", "recent") {
	case "rating":
		sort.Slice(entries, func(i, j int) bool { return entries[i].vote > entries[j].vote })
	case "title":
		sort.Slice(entries, func(i, j int) bool { return entries[i].e.Title < entries[j].e.Title })
	default:
		sort.Slice(entries, func(i, j int) bool { return entries[i].e.LastAt.After(entries[j].e.LastAt) })
	}
	total := len(entries)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	items := []gin.H{}
	for _, en := range entries[start:end] {
		items = append(items, gin.H{"key": en.e.Key, "title": en.e.Title, "year": en.e.Year,
			"media_type": en.e.MediaType, "category": en.e.Category, "tmdb_id": en.e.TmdbID,
			"poster_path": en.poster, "vote_average": en.vote})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "size": size, "items": items})
}

// portalDetail 详情 + 播放文件清单（key = 标题目录路径）
func portalDetail(c *gin.Context) {
	key := strings.Trim(c.Query("key"), "/")
	if key == "" || strings.Contains(key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var sfs []model.SyncedFile
	model.DB.Where("rel_path LIKE ?", key+"/%").Limit(500).Find(&sfs)
	if len(sfs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	segs := strings.Split(key, "/")
	title, year, tmdb := parseTitleDir(segs[len(segs)-1])
	mediaType := "tv"
	if len(segs) >= 2 && segs[1] == "电影" {
		mediaType = "movie"
	}
	category := ""
	if len(segs) >= 3 {
		category = segs[2]
	}
	var ml model.MediaLibrary
	haveML := tmdb > 0 && model.DB.Where("tmdb_id = ? AND media_type = ?", tmdb, mediaType).First(&ml).Error == nil
	if !haveML && tmdb > 0 {
		ml = model.MediaLibrary{TmdbID: tmdb, Title: title, Year: year, MediaType: mediaType, Category: category}
		portalBackfillTMDB(&ml)
		haveML = ml.PosterPath != ""
	}
	poster, vote, overview, backdrop := "", float64(0), "", ""
	country, langs2, genres := "", "", ""
	if haveML {
		poster, vote, overview, backdrop = ml.PosterPath, ml.VoteAverage, ml.Overview, ml.BackdropPath
		if ml.OrigCountry != "" {
			var cs []string
			for _, code := range strings.Split(ml.OrigCountry, ",") {
				cs = append(cs, countryName(strings.TrimSpace(code)))
			}
			country = strings.Join(cs, " / ")
		}
		if ml.OrigLanguage != "" {
			langs2 = langName(strings.Split(ml.OrigLanguage, ",")[0])
		}
		genres = ml.Genres
		if ml.Title != "" {
			title = ml.Title
		}
		if ml.Year != "" {
			year = ml.Year
		}
	}
	// 观看次数（排行榜同源统计）
	var stat model.PortalStat
	views := int64(0)
	if model.DB.Where("key = ?", key).First(&stat).Error == nil {
		views = stat.Views
	}
	// 相关推荐：同类型同分类最近入库（排除自己），海报/评分从整理记录合并
	related := []gin.H{}
	{
		metaByID := map[int]model.MediaLibrary{}
		var mls []model.MediaLibrary
		model.DB.Where("media_type = ?", mediaType).Find(&mls)
		for _, m := range mls {
			if m.TmdbID > 0 {
				metaByID[m.TmdbID] = m
			}
		}
		type relEnt struct {
			e      *portalTitleEntry
			poster string
			vote   float64
		}
		var rels []relEnt
		for _, e2 := range portalScanLedgerCached() {
			if e2.Key == key || e2.MediaType != mediaType || e2.Category != category {
				continue
			}
			r := relEnt{e: e2}
			if e2.TmdbID > 0 {
				if ml2, ok := metaByID[e2.TmdbID]; ok {
					r.poster, r.vote = ml2.PosterPath, ml2.VoteAverage
				}
			}
			rels = append(rels, r)
		}
		sort.Slice(rels, func(i, j int) bool { return rels[i].e.LastAt.After(rels[j].e.LastAt) })
		if len(rels) > 12 {
			rels = rels[:12]
		}
		for _, r := range rels {
			related = append(related, gin.H{"key": r.e.Key, "title": r.e.Title, "year": r.e.Year,
				"poster_path": r.poster, "vote_average": r.vote})
		}
	}
	base302 := portal302Base(c)
	files := []gin.H{}
	subs := []gin.H{}
	for _, sf := range sfs {
		if sf.PickCode == "" {
			continue
		}
		base := strings.ToLower(filepath.Ext(sf.RelPath))
		if sf.Kind == "video" {
			files = append(files, gin.H{
				"name": filepath.Base(sf.RelPath),
				"size": sf.Size,
				"url":  base302 + "/d/" + sf.PickCode,
				"pick": sf.PickCode,
			})
		} else if base == ".srt" || base == ".ass" || base == ".ssa" || base == ".vtt" {
			name := filepath.Base(sf.RelPath)
			label := name
			if strings.Contains(name, ".chs") || strings.Contains(name, ".zh") || strings.Contains(name, "简") || strings.Contains(name, "chs") {
				label = "中文（" + name + "）"
			} else if strings.Contains(name, ".eng") || strings.Contains(name, ".en") || strings.Contains(name, "英文") {
				label = "英文（" + name + "）"
			}
			subs = append(subs, gin.H{"name": name, "label": label, "pick": sf.PickCode})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"media": gin.H{"title": title, "year": year, "media_type": mediaType, "category": category,
			"poster_path": poster, "vote_average": vote, "overview": overview, "backdrop_path": backdrop,
			"country": country, "language": langs2, "genres": genres},
		"files": files, "subs": subs, "views": views, "related": related,
	})
}

// portalPlayed 播放计数（前端开始播放时上报，每部每次会话只计一次）。
// 周/月/年计数跨期自动清零重计——单行 upsert，无明细表膨胀
func portalPlayed(c *gin.Context) {
	var req struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Key == "" || strings.Contains(req.Key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	now := time.Now()
	isoYear, isoWeek := now.ISOWeek()
	week := fmt.Sprintf("%d-W%02d", isoYear, isoWeek)
	month := now.Format("2006-01")
	year := now.Format("2006")
	var stat model.PortalStat
	if model.DB.Where("key = ?", req.Key).First(&stat).Error != nil {
		// 新行：从台账补标题/类型
		e, ok := portalScanLedgerCached()[req.Key]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "条目不存在"})
			return
		}
		stat = model.PortalStat{Key: req.Key, MediaType: e.MediaType, Title: e.Title,
			WeekStart: week, MonthStart: month, YearStart: year}
	} else {
		// 跨期清零（比较字符串即可：ISO 周标签单调）
		if stat.WeekStart != week {
			stat.WeekViews, stat.WeekStart = 0, week
		}
		if stat.MonthStart != month {
			stat.MonthViews, stat.MonthStart = 0, month
		}
		if stat.YearStart != year {
			stat.YearViews, stat.YearStart = 0, year
		}
		if stat.Title == "" {
			if e, ok := portalScanLedgerCached()[req.Key]; ok {
				stat.Title, stat.MediaType = e.Title, e.MediaType
			}
		}
	}
	stat.Views++
	stat.WeekViews++
	stat.MonthViews++
	stat.YearViews++
	stat.LastAt = now
	model.DB.Save(&stat)
	c.JSON(http.StatusOK, gin.H{"views": stat.Views})
}

// portalTrending 排行榜：周/月/年播放次数排序；无人播放时回退最近入库
// （新装库/冷启动也有内容可看）。返回 Top 30
func portalTrending(c *gin.Context) {
	period := c.DefaultQuery("period", "week")
	if period != "week" && period != "month" && period != "year" {
		period = "week"
	}
	stats := map[string]model.PortalStat{}
	var rows []model.PortalStat
	model.DB.Find(&rows)
	for _, s := range rows {
		stats[s.Key] = s
	}
	// 元数据合并
	metaByID := map[int]model.MediaLibrary{}
	var mls []model.MediaLibrary
	model.DB.Find(&mls)
	for _, m := range mls {
		if m.TmdbID > 0 {
			metaByID[m.TmdbID] = m
		}
	}
	type item struct {
		e      *portalTitleEntry
		period int64
		total  int64
		poster string
		vote   float64
		ov     string
	}
	items := []item{}
	for k, e := range portalScanLedgerCached() {
		s := stats[k]
		pv := s.Views
		switch period {
		case "week":
			pv = s.WeekViews
		case "month":
			pv = s.MonthViews
		case "year":
			pv = s.YearViews
		}
		it := item{e: e, period: pv, total: s.Views}
		if e.TmdbID > 0 {
			if ml, ok := metaByID[e.TmdbID]; ok {
				it.poster, it.vote, it.ov = ml.PosterPath, ml.VoteAverage, ml.Overview
			}
		}
		items = append(items, it)
	}
	// 主排序：期内播放次数；回退排序：最近入库（播放数并列或全 0 时）
	sort.Slice(items, func(i, j int) bool {
		if items[i].period != items[j].period {
			return items[i].period > items[j].period
		}
		return items[i].e.LastAt.After(items[j].e.LastAt)
	})
	// 无任何播放记录 → 完全按最近入库展示（榜面不至于空）
	if len(items) > 0 && items[0].period == 0 {
		sort.Slice(items, func(i, j int) bool { return items[i].e.LastAt.After(items[j].e.LastAt) })
	}
	if len(items) > 30 {
		items = items[:30]
	}
	out := []gin.H{}
	for i, it := range items {
		out = append(out, gin.H{"rank": i + 1, "key": it.e.Key, "title": it.e.Title, "year": it.e.Year,
			"poster_path": it.poster, "vote_average": it.vote, "overview": it.ov,
			"period_views": it.period, "views": it.total})
	}
	c.JSON(http.StatusOK, gin.H{"period": period, "items": out})
}

// portal302Base 直链基地址：用"当前访问门户的主机 + 302 代理端口"。
// 浏览器已能访问门户即说明该主机可达；STRM 配置的域名常是内网/媒体服务器视角地址，浏览器未必可达。
func portal302Base(c *gin.Context) string {
	host := c.Request.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return fmt.Sprintf("http://%s:%d", host, portalCfg.ProxyPort)
}

// portalBackfillWorker 后台元数据补全：为台账里有 tmdb id 但缺海报/评分的条目
// 逐个从 TMDB 拉详情并落库（限速 3 秒一个，新内容 5 分钟后重扫）
func portalBackfillWorker() {
	time.Sleep(10 * time.Second) // 等服务完全就绪
	for {
		done := 0
		tc, tcErr := loadTmdbClient()
		for _, e := range portalScanLedgerCached() {
			if e.TmdbID <= 0 {
				// 老内容目录名没有 tmdb 标记：按标题+年份搜索一次补 ID
				if tcErr != nil || e.Title == "" {
					continue
				}
				var media *TmdbMedia
				if e.MediaType == "tv" {
					media, _ = tc.SearchTV(e.Title, e.Year)
				} else {
					media, _ = tc.SearchMovie(e.Title, e.Year)
				}
				if media == nil || media.TmdbID == 0 {
					continue
				}
				e.TmdbID = media.TmdbID
			}
			// 一次查询：found 决定补建还是更新（此前连续两次完全相同的查询）。
			// 评分也纳入回填范围（接管原 dashboard StartPosterBackfill 的职责，
			// 消除两个 worker 对同一张表的双倍 TMDB 请求）
			var ml model.MediaLibrary
			found := model.DB.Where("tmdb_id = ? AND media_type = ?", e.TmdbID, e.MediaType).First(&ml).Error == nil
			if found {
				need := ml.PosterPath == "" || ml.Overview == "" || ml.BackdropPath == "" || ml.Genres == "" || ml.VoteAverage == 0
				if !need {
					continue
				}
			}
			if !found {
				ml = model.MediaLibrary{TmdbID: e.TmdbID, Title: e.Title, Year: e.Year,
					MediaType: e.MediaType, Category: e.Category, TargetPath: e.Key}
			} else if ml.TargetPath == "" {
				ml.TargetPath = e.Key
			}
			portalBackfillTMDB(&ml)
			model.DB.Save(&ml)
			done++
			select {
			case <-stopCh:
				return
			case <-time.After(3 * time.Second): // 限速，避免 TMDB 配额
			}
			if done >= 200 {
				break
			}
		}
		sleep := 5 * time.Minute
		if done > 0 {
			sleep = 30 * time.Second // 还有活干就快点回来
		}
		select {
		case <-stopCh:
			return // 进程退出：回填循环跟着停（不再打 TMDB）
		case <-time.After(sleep):
		}
	}
}

// portalBackfillTMDB 从 TMDB 补海报/评分/简介（回填失败静默）
func portalBackfillTMDB(m *model.MediaLibrary) {
	tc, err := loadTmdbClient()
	if err != nil {
		return
	}
	kind := "movie"
	if m.MediaType == "tv" {
		kind = "tv"
	}
	body, err := tc.get(fmt.Sprintf("/%s/%d", kind, m.TmdbID), map[string]string{"language": "zh-CN"})
	if err != nil {
		return
	}
	var d struct {
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		Overview     string  `json:"overview"`
		VoteAverage  float64 `json:"vote_average"`
		Genres       []struct {
			Name string `json:"name"`
		} `json:"genres"`
		OriginalLanguage string   `json:"original_language"`
		OriginCountry    []string `json:"origin_country"`
	}
	if json.Unmarshal(body, &d) != nil {
		return
	}
	if d.PosterPath != "" {
		m.PosterPath = d.PosterPath
	}
	if d.BackdropPath != "" {
		m.BackdropPath = d.BackdropPath // 详情页沉浸头图
	}
	if d.Overview != "" {
		m.Overview = d.Overview
	}
	// 类型名（zh-CN，如 动作,科幻）+ 语言/国家：门户多维筛选的数据源
	if len(d.Genres) > 0 {
		names := make([]string, 0, len(d.Genres))
		for _, g := range d.Genres {
			if g.Name != "" {
				names = append(names, g.Name)
			}
		}
		m.Genres = strings.Join(names, ",")
	}
	if d.OriginalLanguage != "" && m.OrigLanguage == "" {
		m.OrigLanguage = d.OriginalLanguage
	}
	if len(d.OriginCountry) > 0 && m.OrigCountry == "" {
		m.OrigCountry = strings.Join(d.OriginCountry, ",")
	}
	m.VoteAverage = d.VoteAverage
	model.DB.Save(m)
}

// portalPoster 海报代理：TMDB 图片经本服务转发并缓存（局网客户端免翻墙）
func portalPoster(c *gin.Context) {
	serveTMDBPoster(c, portalCfg.DataDir)
}

// serveTMDBPoster TMDB 海报服务端代理（带 7 天磁盘缓存；portalCfg 来自门户，
// 管理后台仪表盘复用同一逻辑，DataDir 由调用方传入）
// ipIsPublic 公网地址判定（回环/私网/链路本地/组播/未指定全拒）
func ipIsPublic(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast())
}

// avCoverClient AV 封面专用客户端：DialContext 在连接层校验目标 IP 为
// 公网地址——DNS 解析后的实际连接目标才作数（防 DNS rebinding 式 SSRF），
// 重定向的每一跳同样经过该 Transport
var avCoverClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip != nil && !ipIsPublic(ip) {
				return nil, fmt.Errorf("SSRF 防护：拒绝内网地址 %s", host)
			}
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	},
}

func serveTMDBPoster(c *gin.Context, dataDir string) {
	p := strings.TrimPrefix(c.Param("path"), "/")
	if p == "" || strings.Contains(p, "..") {
		c.String(http.StatusBadRequest, "bad path")
		return
	}
	cacheDir := filepath.Join(dataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0755)
	// ---- AV 封面分支（MetaTube 刮削结果，PosterPath = "av:<完整URL>"）----
	// 直接代理原始封面 URL（只缓存到 /data/posters，不写媒体目录），规则与 TMDB 相同。
	// 本端点无鉴权（门户海报），SSRF 防护：仅 http(s) + 连接层校验目标为公网地址
	if strings.HasPrefix(p, "av:") {
		coverURL := strings.TrimPrefix(p, "av:")
		if u, err := url.Parse(coverURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			avPosterPlaceholder(c)
			return
		}
		ah := sha1.Sum([]byte(p))
		avCache := filepath.Join(cacheDir, hex.EncodeToString(ah[:8])+filepath.Ext(coverURL))
		if st, err := os.Stat(avCache); err == nil && st.Size() > 0 && time.Since(st.ModTime()) < 7*24*time.Hour {
			c.Header("Cache-Control", "public, max-age=604800")
			c.File(avCache)
			return
		}
		resp, err := avCoverClient.Get(coverURL)
		if err != nil {
			log.Printf("[海报] ✗ AV 封面拉取失败 %s: %v", coverURL, err)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && len(body) > 100 {
				_ = os.WriteFile(avCache, body, 0644)
				ct := resp.Header.Get("Content-Type")
				if ct == "" {
					ct = "image/jpeg"
				}
				c.Header("Cache-Control", "public, max-age=604800")
				c.Data(http.StatusOK, ct, body)
				return
			}
			log.Printf("[海报] ✗ AV 封面拉取失败 HTTP %d: %s", resp.StatusCode, coverURL)
		}
		avPosterPlaceholder(c)
		return
	}
	h := sha1.Sum([]byte(p))
	cacheFile := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+filepath.Ext(p))
	if st, err := os.Stat(cacheFile); err == nil && st.Size() > 0 && time.Since(st.ModTime()) < 7*24*time.Hour {
		c.Header("Cache-Control", "public, max-age=604800")
		c.File(cacheFile)
		return
	}
	// 服务端拉取，多级回退：配置图片域名(走代理) → 配置域名直连 → 官方域名直连。
	// 此前单次失败即静默回占位图，配置了不可达的图片域名时所有海报永远空白
	buildClient := func(withProxy bool) *http.Client {
		client := &http.Client{Timeout: 10 * time.Second}
		if withProxy {
			if pu := getProxyURL(); pu != "" {
				if pr, err := parseProxyURL(pu); err == nil {
					client.Transport = &http.Transport{Proxy: pr}
				}
			}
		}
		return client
	}
	candidates := []struct {
		base      string
		withProxy bool
	}{
		{tmdbImageBase(), true},
		{tmdbImageBase(), false},
		{"https://image.tmdb.org", false},
	}
	var data []byte
	var lastCT string
	var lastErr string
	for i, cand := range candidates {
		if i > 0 && cand.base == candidates[i-1].base && cand.withProxy == candidates[i-1].withProxy {
			continue
		}
		u := cand.base + "/t/p/w500/" + strings.TrimPrefix(p, "/")
		resp, err := buildClient(cand.withProxy).Get(u)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) < 100 {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			continue
		}
		data, lastCT = body, resp.Header.Get("Content-Type")
		break
	}
	if len(data) == 0 {
		// 三条链路都失败：日志写明原因（占位图避免卡片裂图）
		log.Printf("[海报] ✗ 拉取失败 %s：配置域名与官方域名均不可达，最后错误: %s", p, lastErr)
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/gif", []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
		return
	}
	_ = os.WriteFile(cacheFile, data, 0644)
	if lastCT == "" {
		lastCT = "image/jpeg"
	}
	c.Header("Cache-Control", "public, max-age=604800")
	c.Data(http.StatusOK, lastCT, data)
}

// avPosterPlaceholder AV 封面占位图（1x1 透明 GIF，避免卡片裂图）
func avPosterPlaceholder(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/gif", []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
}

// portalSub 字幕文本代理：服务端按 pick_code 取 115 直链拉字幕并加 CORS 头返回
// （浏览器直接 fetch 302/115 会因跨域失败，必须经服务端中转）
func portalSub(c *gin.Context) {
	pick := c.Query("pick")
	if pick == "" || len(pick) > 64 {
		c.String(http.StatusBadRequest, "bad pick")
		return
	}
	urlStr, err := proxyDownloadURL(model.DB, portalCfg, pick, c.Request.UserAgent())
	if err != nil || urlStr == "" {
		c.String(http.StatusBadGateway, "获取字幕链接失败")
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(urlStr)
	if err == nil {
		defer resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		c.String(http.StatusBadGateway, "拉取字幕失败")
		return
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	c.Header("Access-Control-Allow-Origin", "*")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
}

// portalPage 门户页面（内嵌单文件，暗色影院风）
func portalPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(portalHTML))
}

const portalHTML = `<!DOCTYPE html>
<html lang="zh-CN" data-theme="light">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>StrmHub 影院</title>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js" onerror="var d=document.createElement('script');d.src='https://unpkg.com/hls.js@1/dist/hls.min.js';d.onerror=function(){window.noHls=true};document.head.appendChild(d)"></script>
<style>
:root[data-theme=light]{--bg:#f5f6f8;--card:#fff;--text:#1f2328;--dim:#6a737d;--acc:#2563eb;--bd:#e4e7eb;--hover:#f0f3f6;--mask:rgba(0,0,0,.55)}
:root[data-theme=dark]{--bg:#0d1117;--card:#161b22;--text:#e6edf3;--dim:#8b949e;--acc:#4f8cff;--bd:#21262d;--hover:#1c2129;--mask:rgba(0,0,0,.75)}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--bg);color:var(--text);font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;transition:background .2s}
button{font-family:inherit}
header{position:sticky;top:0;z-index:50;background:var(--card);padding:12px 24px;display:flex;align-items:center;gap:14px;border-bottom:1px solid var(--bd)}
header h1{font-size:19px;background:linear-gradient(90deg,#2563eb,#7c3aed);-webkit-background-clip:text;background-clip:text;color:transparent;cursor:pointer;flex:none}
.tabs{display:flex;gap:4px}
.tab{padding:6px 16px;border-radius:20px;cursor:pointer;font-size:14px;color:var(--dim)}
.tab.on{background:var(--acc);color:#fff}
#theme{border:1px solid var(--bd);background:var(--card);color:var(--text);border-radius:20px;padding:6px 14px;cursor:pointer;font-size:14px;flex:none}
#kw{width:200px;background:var(--bg);border:1px solid var(--bd);border-radius:20px;padding:8px 16px;color:var(--text);font-size:14px;outline:none;margin-left:auto}
#kw:focus{border-color:var(--acc)}
#content{padding:0 24px 40px;max-width:1500px;margin:0 auto}
.row{margin-top:26px}
.rh{display:flex;align-items:baseline;gap:12px;margin-bottom:12px}
.rh h2{font-size:18px}
.rh .more-link{font-size:13px;color:var(--dim);cursor:pointer}
.rh .more-link:hover{color:var(--acc)}
.strip{display:flex;gap:14px;overflow-x:auto;padding-bottom:8px;scrollbar-width:thin}
.strip .card{flex:0 0 148px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(148px,1fr));gap:14px;margin-top:16px}
.card{background:var(--card);border-radius:10px;overflow:hidden;cursor:pointer;transition:transform .15s;border:1px solid var(--bd);position:relative}
.card:hover{transform:translateY(-4px)}
.card img{width:100%;aspect-ratio:2/3;object-fit:cover;display:block;background:var(--hover)}
.card .ph{width:100%;aspect-ratio:2/3;display:flex;align-items:center;justify-content:center;font-size:36px;background:var(--hover)}
.card .info{padding:8px 10px}
.card .t{font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.card .y{font-size:11px;color:var(--dim);margin-top:2px;display:flex;align-items:center;gap:6px}
.rate{color:#f59e0b;font-size:11px}
.badge-new{position:absolute;top:8px;left:8px;background:#ef4444;color:#fff;font-size:10px;padding:2px 7px;border-radius:9px}
.prog{position:absolute;left:0;right:0;bottom:0;height:3px;background:rgba(255,255,255,.3)}
.prog i{display:block;height:100%;background:var(--acc)}
.pct{position:absolute;left:0;right:0;bottom:6px;font-size:10px;color:#fff;text-shadow:0 1px 2px rgba(0,0,0,.8);padding-left:6px}
.filters{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin-top:14px}
.chips{display:flex;gap:8px;overflow-x:auto;flex:1;scrollbar-width:none;padding:2px 0}
.chips::-webkit-scrollbar{display:none}
.chip{flex:none;padding:5px 14px;border-radius:16px;font-size:13px;background:var(--card);color:var(--dim);cursor:pointer;border:1px solid var(--bd)}
.chip.on{color:#fff;background:var(--acc);border-color:var(--acc)}
select{background:var(--card);color:var(--text);border:1px solid var(--bd);border-radius:16px;padding:5px 10px;font-size:13px;outline:none}
.listtitle{font-size:20px;margin-top:22px}
.empt{color:var(--dim);text-align:center;padding:60px 0}
.loadmore{text-align:center;padding:16px;color:var(--dim);cursor:pointer;font-size:14px}
.loadmore:hover{color:var(--acc)}
/* ===== 播放页 ===== */
.playwrap{display:flex;gap:16px;margin-top:14px;align-items:flex-start}
.pleft{flex:1;min-width:0}
#player{width:100%;background:#000;aspect-ratio:16/9;position:relative;border-radius:10px;overflow:hidden}
#player video{width:100%;height:100%}
#pbar{position:absolute;left:0;right:0;bottom:0;display:flex;align-items:center;gap:8px;padding:8px 12px;background:linear-gradient(transparent,rgba(0,0,0,.82));transition:opacity .2s;z-index:4}
#pbar.hide{opacity:0;pointer-events:none}
#pbar .ib{background:none;border:none;color:#e5e7eb;font-size:12px;cursor:pointer;padding:5px 8px;border-radius:6px;display:flex;align-items:center;gap:4px}
#pbar .ib:hover{background:rgba(255,255,255,.12)}
#pbar .ib.on{color:#93c5fd}
.pt{color:#e5e7eb;font-size:12px;flex:none;width:100px;text-align:center}
#seek{flex:1;height:4px;background:rgba(255,255,255,.22);border-radius:2px;cursor:pointer;position:relative}
#seekcur{position:absolute;left:0;top:0;bottom:0;background:var(--acc);border-radius:2px;width:0}
#seekcur i{position:absolute;right:-5px;top:50%;transform:translateY(-50%);width:10px;height:10px;border-radius:50%;background:#fff}
#pmenu{position:absolute;right:12px;bottom:52px;background:rgba(17,24,39,.96);border-radius:10px;padding:6px;display:none;z-index:6;max-height:240px;overflow-y:auto;min-width:130px}
#pmenu div{color:#cbd5e1;font-size:13px;padding:7px 14px;border-radius:6px;cursor:pointer;white-space:nowrap}
#pmenu div:hover{background:rgba(255,255,255,.1)}
#pmenu div.on{color:#93c5fd;font-weight:600}
#pmenu .tip{color:#6b7280;cursor:default;font-size:12px}
#pmenu .tip:hover{background:none}
.ptitle{font-size:18px;font-weight:600;margin-top:12px}
.pmeta{font-size:13px;color:var(--dim);margin-top:4px}
.fail{margin-top:10px;font-size:13px;color:var(--dim);display:none;line-height:2;background:var(--card);border:1px solid var(--bd);border-radius:10px;padding:12px 16px}
.fail input{width:60%;background:var(--bg);border:1px solid var(--bd);border-radius:8px;padding:8px 10px;color:var(--text);font-size:12px}
.pright{width:300px;flex:none;background:var(--card);border:1px solid var(--bd);border-radius:10px;max-height:calc(75vh + 60px);overflow-y:auto;padding:12px}
/* 移动端控制条：换行收纳次要控件、加大触摸目标、进度条加高可拖动 */
@media (max-width:768px){
  #pbar{flex-wrap:wrap;padding:6px 8px 10px;gap:2px 4px}
  #pbar .ib{padding:8px 10px;font-size:14px}
  #seek{order:-1;flex-basis:100%;height:22px;display:flex;align-items:center;margin-bottom:2px}
  #seek::before{content:'';display:block;width:100%;height:5px;background:rgba(255,255,255,.22);border-radius:3px}
  #seekcur{height:5px;top:50%;transform:translateY(-50%)}
  #seekcur i{width:16px;height:16px;right:-8px}
  .pt{width:auto;font-size:11px;padding:0 4px}
  #pbar .ib.hide-m{display:none}
}
.pright h3{font-size:14px;margin-bottom:8px;color:var(--dim)}
.epi{display:flex;align-items:center;gap:8px;padding:9px 10px;border-radius:8px;cursor:pointer;font-size:13px}
.epi:hover{background:var(--hover)}
.epi.on{background:rgba(37,99,235,.12);color:var(--acc)}
.epi .nm{flex:1;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.epi .sz{color:var(--dim);font-size:11px;flex:none}
.epi .don{color:var(--acc);font-size:11px;flex:none}
@media(max-width:900px){.playwrap{flex-direction:column}.pright{width:100%;max-height:280px}}
/* ===== 详情独立页 ===== */
.dhero{margin:0 -24px;height:300px;background:linear-gradient(135deg,#1e293b,#475569);background-size:cover;background-position:center;position:relative;overflow:hidden}
.dhero .dgrad{position:absolute;inset:0;background:linear-gradient(180deg,rgba(0,0,0,.05) 30%,var(--bg))}
.dback{position:absolute;top:16px;left:20px;z-index:3;background:rgba(0,0,0,.45);color:#fff;border:none;border-radius:18px;padding:7px 18px;cursor:pointer;font-size:13px}
.dback:hover{background:rgba(0,0,0,.65)}
.dwrap{display:flex;gap:24px;max-width:1100px;margin:-100px auto 0;padding:0 24px;position:relative;z-index:2}
.dposter{flex:none;width:180px}
.dposter img{width:100%;border-radius:10px;aspect-ratio:2/3;object-fit:cover;box-shadow:0 12px 32px rgba(0,0,0,.4);background:var(--card)}
.dposter .ph{width:100%;aspect-ratio:2/3;display:flex;align-items:center;justify-content:center;font-size:42px;background:var(--card);border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,.4)}
.dinfo{flex:1;min-width:0;padding-top:104px}
.dinfo h2{font-size:26px;line-height:1.3}
.ovfull{font-size:14px;line-height:1.9;color:var(--dim);margin-top:16px}
.dsec{max-width:1100px;margin:30px auto 0;padding:0 24px}
.eplist2{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:8px}
/* ===== 排行榜 ===== */
.tlist{display:flex;flex-direction:column;gap:10px;margin-top:16px;max-width:1100px}
.trow{display:flex;gap:14px;align-items:flex-start;background:var(--card);border:1px solid var(--bd);border-radius:12px;padding:12px 14px;cursor:pointer;transition:transform .15s}
.trow:hover{transform:translateY(-2px)}
.trank{flex:none;width:36px;text-align:center;font-size:20px;font-weight:700;font-style:italic;color:var(--dim);padding-top:14px}
.trank.top{color:#f59e0b;font-size:24px}
.tposter{flex:none;width:64px;aspect-ratio:2/3;object-fit:cover;border-radius:6px;background:var(--hover)}
.ph2{display:flex;align-items:center;justify-content:center;font-size:20px}
.tinfo{flex:1;min-width:0}
.tt{font-size:15px;font-weight:600}
.tv{font-size:12px;color:var(--dim);margin-top:4px}
.tov{font-size:12px;color:var(--dim);margin-top:6px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
@media(max-width:600px){
 .dwrap{flex-direction:column;margin-top:-60px;gap:14px}
 .dposter{width:130px}
 .dinfo{padding-top:0}
 .dhero{height:200px;margin:0 -12px}
 .dinfo h2{font-size:20px}
}
/* 详情弹窗 */
#mask{position:fixed;inset:0;background:var(--mask);z-index:100;display:none;align-items:center;justify-content:center;padding:24px}
#modal{background:var(--card);border-radius:14px;max-width:860px;width:100%;max-height:92vh;overflow-y:auto;border:1px solid var(--bd);position:relative}
.dhead{display:flex;gap:20px;padding:24px}
.dhead img{width:180px;border-radius:8px;aspect-ratio:2/3;object-fit:cover;background:var(--hover);flex:none}
.dbody{flex:1;min-width:0}
.dbody h2{font-size:22px;margin-bottom:6px}
.meta{color:var(--dim);font-size:13px;margin-bottom:4px}
.badges span{display:inline-block;padding:2px 10px;border-radius:12px;font-size:12px;background:rgba(37,99,235,.12);color:var(--acc);margin:6px 6px 0 0}
.ov{font-size:13px;line-height:1.8;color:var(--dim);margin-top:10px;max-height:130px;overflow-y:auto}
.playbtns{margin-top:14px;display:flex;gap:10px;flex-wrap:wrap}
.play{padding:10px 28px;background:linear-gradient(90deg,#2563eb,#7c3aed);border:none;border-radius:22px;color:#fff;font-size:15px;cursor:pointer}
.ghost{padding:10px 18px;background:var(--card);border:1px solid var(--bd);border-radius:22px;color:var(--text);font-size:13px;cursor:pointer}
.close{position:absolute;top:10px;right:10px;z-index:5;font-size:16px;color:var(--dim);cursor:pointer;padding:4px 10px;background:var(--card);border-radius:50%;border:1px solid var(--bd)}
@media(max-width:600px){
 #content{padding:0 12px 40px}.grid{grid-template-columns:repeat(3,1fr);gap:10px}.strip .card{flex:0 0 110px}
 .dhead{flex-direction:column}.dhead img{width:130px}#kw{width:110px}header{padding:10px 12px;gap:8px}header h1{font-size:16px}
}
</style>
</head>
<body>
<header>
  <h1 onclick="nav('#/home')">🎬 StrmHub 影院</h1>
  <div class="tabs">
    <div class="tab" data-t="movie" onclick="goType('movie')">电影</div>
    <div class="tab" data-t="tv" onclick="goType('tv')">剧集</div>
    <div class="tab" data-t="trending" onclick="goTrending()">排行榜</div>
  </div>
  <input id="kw" placeholder="搜索片名…">
  <button id="theme" onclick="toggleTheme()">🌙</button>
</header>
<div id="content"></div>

<input type="file" id="subfile" accept=".srt,.ass,.ssa,.vtt" style="display:none" onchange="localSub(this)">
<script>
const $=id=>document.getElementById(id);
let view='home',curType='movie',curCat='',curSort='recent',curPage=1;
let curCountry='',curLang='',curGenre='';
let curMedia=null,curFiles=[],curSubs=[],curKey='',curDetail=null,curFileIdx=0,curFileUrl='';
let curRate=1,progTimer=null,subOff=true,curSubIdx=-1;

/* ---------- 主题 ---------- */
function applyTheme(t){document.documentElement.dataset.theme=t;$('theme').textContent=t==='dark'?'☀️':'🌙';try{localStorage.setItem('pt',t)}catch(e){}}
function toggleTheme(){applyTheme(document.documentElement.dataset.theme==='dark'?'light':'dark')}
(function(){var t='light';try{t=localStorage.getItem('pt')||'light'}catch(e){}applyTheme(t)})();

/* ---------- 进度记忆 ---------- */
function getProg(){try{return JSON.parse(localStorage.getItem('pprog')||'{}')}catch(e){return{}}}
function setProg(k,v){var p=getProg();p[k]=v;try{localStorage.setItem('pprog',JSON.stringify(p))}catch(e){}}
function delProg(k){var p=getProg();delete p[k];try{localStorage.setItem('pprog',JSON.stringify(p))}catch(e){}}
function recentProg(){return Object.values(getProg()).sort((a,b)=>b.ts-a.ts).slice(0,12)}

/* ---------- 路由（hash） ---------- */
function nav(h){if(location.hash===h)route();else location.hash=h}
window.addEventListener('hashchange',route);
async function route(){
  const h=location.hash||'#/home';
  stopProg();
  if(h.startsWith('#/play')){
    const q=new URLSearchParams(h.slice(7));
    await renderPlay(q.get('key')||'', parseInt(q.get('i')||'0'), parseFloat(q.get('p')||'0'));
  }else if(h.startsWith('#/detail')){
    const q=new URLSearchParams(h.slice(9));
    await renderDetail(q.get('key')||'');
  }else if(h.startsWith('#/trending')){
    renderTrending();
  }else if(view==='home'){renderHome()}
  else if(view==='search'){renderSearch()}
  else{renderList()}
}

/* ---------- 数据 ---------- */
async function fetchList(o){
  const u=new URL('/api/portal/list',location);u.searchParams.set('type',o.type||curType);
  if(o.cat)u.searchParams.set('cat',o.cat);if(o.q)u.searchParams.set('q',o.q);
  if(o.country)u.searchParams.set('country',o.country);if(o.lang)u.searchParams.set('lang',o.lang);
  if(o.genre)u.searchParams.set('genre',o.genre);
  u.searchParams.set('sort',o.sort||'recent');u.searchParams.set('page',o.page||1);
  return (await fetch(u)).json()
}
async function fetchNav(){return (await fetch('/api/portal/nav')).json().nav||{}}

/* ---------- 卡片 ---------- */
function cardHTML(m,extra){
  const poster=m.poster_path?('<img loading="lazy" src="/poster'+m.poster_path+'">'):'<div class="ph">🎬</div>';
  const rate=m.vote_average>0?('<span class="rate">★ '+m.vote_average.toFixed(1)+'</span>'):'';
  return '<div class="card" onclick="openDetail(\''+escA(m.key)+'\')">'+(extra&&extra.isNew?'<span class="badge-new">新</span>':'')+poster+
    (extra&&extra.pct!==undefined?'<div class="prog"><i style="width:'+extra.pct+'%"></i></div><div class="pct">看到 '+extra.pct+'%</div>':'')+
    '<div class="info"><div class="t">'+esc(m.title)+'</div><div class="y">'+(m.year||'')+' '+rate+'</div></div></div>'
}
function esc(s){return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;')}
function escA(s){return String(s||'').replace(/'/g,"\\'")}

/* ---------- 首页 ---------- */
function goHome(){view='home';nav('#/home')}
function goType(t){curType=t;view='list';curCat='';curSort='recent';curPage=1;renderTabs(t);nav('#/list');renderList()}
function goCat(t,c){curType=t;view='list';curCat=c;curSort='recent';curPage=1;renderTabs(t);nav('#/list');renderList()}
function renderTabs(on){document.querySelectorAll('.tab').forEach(x=>x.classList.toggle('on',x.dataset.t===on))}
let kwTimer;
$('kw').oninput=()=>{clearTimeout(kwTimer);kwTimer=setTimeout(()=>{
  if(!$('kw').value.trim()){if(view==='search')nav('#/');return} // 清空关键词回首页（此前只能点 logo）
  view='search';nav('#/search');renderSearch()},350)};

async function renderHome(){
  const c=$('content');c.innerHTML='<div class="empt">加载中…</div>';renderTabs('');
  const nav2=await fetchNav();
  const [rc,TR,pp]=await Promise.all([
    fetchList({type:curType,sort:'recent',size:12}),
    fetchList({type:curType,sort:'rating',size:12}),
    Promise.resolve(recentProg())
  ]);
  let h='';
  const cats=(nav2[curType]||[]).slice(0,10);
  if(cats.length){
    h+='<div class="row"><div class="rh"><h2>分类</h2></div><div class="chips">'+
      cats.map(x=>'<div class="chip" onclick="goCat(\''+curType+'\',\''+escA(x.name)+'\')">'+x.name+' · '+x.count+'</div>').join('')+'</div></div>'
  }
  if(pp.length){
    h+='<div class="row"><div class="rh"><h2>继续观看</h2></div><div class="strip">'+
      pp.map(x=>'<div class="card" onclick="playResume(\''+escA(x.key)+'\','+x.fileIdx+')">'+
        (x.poster?('<img src="/poster'+x.poster+'">'):'<div class="ph">🎬</div>')+
        '<div class="prog"><i style="width:'+x.pct+'%"></i></div><div class="pct">'+x.pct+'%</div>'+
        '<div class="info"><div class="t">'+esc(x.title)+'</div><div class="y">'+esc(x.epName||'')+'</div></div></div>').join('')+'</div></div>'
  }
  if((rc.items||[]).length){
    h+='<div class="row"><div class="rh"><h2>最近入库</h2><span class="more-link" onclick="goType(\''+curType+'\')">查看全部 ›</span></div><div class="strip">'+
      rc.items.map(m=>cardHTML(m,{isNew:true})).join('')+'</div></div>'
  }
  if((TR.items||[]).length){
    h+='<div class="row"><div class="rh"><h2>评分最高</h2><span class="more-link" onclick="sortByRating()">按评分浏览 ›</span></div><div class="strip">'+
      TR.items.map(m=>cardHTML(m)).join('')+'</div></div>'
  }
  if(!h)h='<div class="empt">媒体库还是空的——完成一次全量同步后，这里会出现你的全部影视</div>';
  c.innerHTML=h
}
function sortByRating(){view='list';curSort='rating';curPage=1;renderTabs(curType);nav('#/list');renderList()}

/* ---------- 列表 ---------- */
async function renderList(){
  const c=$('content');
  const nav2=await fetchNav();
  const cats=nav2[curType]||[];
  let h='<div class="listtitle">'+(curType==='movie'?'电影':'剧集')+(curCat?' · '+curCat:'')+'</div>';
  h+='<div class="filters"><div class="chips"><div class="chip'+(curCat===''?' on':'')+'" onclick="goCat(\''+curType+'\',\'\')">全部</div>'+
    cats.map(x=>'<div class="chip'+(curCat===x.name?' on':'')+'" onclick="goCat(\''+curType+'\',\''+escA(x.name)+'\')">'+x.name+'</div>').join('')+'</div>'+
    '<select onchange="curSort=this.value;curPage=1;renderList()"><option value="recent"'+(curSort==='recent'?' selected':'')+'>最近入库</option><option value="rating"'+(curSort==='rating'?' selected':'')+'>评分最高</option><option value="title"'+(curSort==='title'?' selected':'')+'>名称</option></select></div>';
  h+='<div class="grid" id="lgrid"></div><div class="loadmore" id="more" onclick="loadMore()">加载更多</div>';
  // 国家/语言/类型多维筛选（facet 来自 /nav，含条目计数）
  const fc=(nav2.facets||{})[curType]||{};
  const opt=(list,cur,label)=>'<select onchange="setFacet(\''+label+'\',this.value)"><option value="">'+label+'</option>'+
    (list||[]).map(x=>'<option value="'+escA(x.code)+'"'+(cur===x.code?' selected':'')+'>'+esc(x.name)+' ('+x.count+')</option>').join('')+'</select>';
  h+='<div class="filters"><div class="chips">'+opt(fc.countries,curCountry,'国家')+opt(fc.langs,curLang,'语言')+opt(fc.genres,curGenre,'类型')+
    ((curCountry||curLang||curGenre)?'<div class="chip" onclick="clearFacets()">清除筛选</div>':'')+'</div></div>';
  c.innerHTML=h;
  paintGrid(await fetchList({cat:curCat,sort:curSort,page:1,country:curCountry,lang:curLang,genre:curGenre}))
}
async function loadMore(){curPage++;paintGrid(await fetchList({cat:curCat,sort:curSort,page:curPage,country:curCountry,lang:curLang,genre:curGenre}),true)}
function setFacet(label,v){if(label==='国家')curCountry=v;else if(label==='语言')curLang=v;else curGenre=v;curPage=1;renderList()}
function clearFacets(){curCountry='';curLang='';curGenre='';curPage=1;renderList()}
function paintGrid(d,append){
  const g=$('lgrid');
  if(!append)g.innerHTML='';
  (d.items||[]).forEach(m=>{const div=document.createElement('div');div.innerHTML=cardHTML(m);g.appendChild(div.firstChild)});
  $('more').style.display=(d.page*d.size<d.total)?'':'none';
  if(!append&&!(d.items||[]).length)g.innerHTML='<div class="empt" style="grid-column:1/-1">没有符合条件的影视</div>'
}
async function renderSearch(){
  const q=$('kw').value.trim();const c=$('content');renderTabs('');
  c.innerHTML='<div class="listtitle">搜索：'+esc(q)+'</div><div class="grid" id="lgrid"></div>';
  const [a,b]=await Promise.all([fetchList({type:'movie',q}),fetchList({type:'tv',q})]);
  const g=$('lgrid');(a.items||[]).concat(b.items||[]).forEach(m=>{const div=document.createElement('div');div.innerHTML=cardHTML(m);g.appendChild(div.firstChild)});
  if(!(a.items||[]).length&&!(b.items||[]).length)g.innerHTML='<div class="empt" style="grid-column:1/-1">未找到相关影视</div>'
}

/* ---------- 详情（独立页：沉浸头图 + 选集 + 相关推荐） ---------- */
function openDetail(key){nav('#/detail?key='+encodeURIComponent(key))}
async function renderDetail(key){
  const c=$('content');renderTabs('');
  if(!key){c.innerHTML='<div class="empt">缺少参数</div>';return}
  c.innerHTML='<div class="empt">加载中…</div>';window.scrollTo(0,0);
  let d=null;
  try{d=await(await fetch('/api/portal/detail?key='+encodeURIComponent(key))).json()}catch(e){}
  if(!d||!d.media){c.innerHTML='<div class="empt">条目已失效或加载失败，请返回列表刷新</div>';return}
  curMedia=d.media;curFiles=d.files||[];curSubs=d.subs||[];curKey=key;curDetail=d;
  const m=curMedia;
  const bg=m.backdrop_path?('/poster'+m.backdrop_path):(m.poster_path?('/poster'+m.poster_path):'');
  let h='<div class="dpage">';
  h+='<div class="dhero"'+(bg?(' style="background-image:url('+bg+')"'):'')+'><div class="dgrad"></div><button class="dback" onclick="history.back()">‹ 返回</button></div>';
  h+='<div class="dwrap">';
  h+='<div class="dposter">'+(m.poster_path?('<img src="/poster'+m.poster_path+'">'):'<div class="ph">🎬</div>')+'</div>';
  h+='<div class="dinfo">';
  h+='<h2>'+esc(m.title)+(m.year?'（'+m.year+'）':'')+'</h2>';
  h+='<div class="meta">'+(m.media_type==='tv'?'剧集':'电影')+(m.category?' · '+m.category:'')+' · '+(curFiles.length||0)+' 个视频'+((d.views||0)>0?' · '+d.views+' 次观看':'')+'</div>';
  h+='<div class="badges">'+(m.vote_average>0?'<span>★ '+m.vote_average.toFixed(1)+'</span>':'')+(m.country?'<span>'+m.country+'</span>':'')+(m.language?'<span>'+m.language+'</span>':'')+
    (m.genres?m.genres.split(',').map(g=>'<span>'+esc(g)+'</span>').join(''):'')+(curSubs.length?'<span>'+curSubs.length+' 个字幕</span>':'')+(m.year?'<span>'+m.year+'</span>':'')+'</div>';
  h+='<div class="playbtns"><button class="play" onclick="playNow(0)">▶ 播放</button><button class="ghost" onclick="copyLink0()">复制直链</button></div>';
  h+='<div class="ovfull">'+esc(m.overview||'暂无简介')+'</div>';
  h+='</div></div>';
  if(curFiles.length>1){
    h+='<div class="dsec"><div class="rh"><h2>选集（'+curFiles.length+'）</h2></div><div class="eplist2">'+
      curFiles.map((f,i)=>'<div class="epi" onclick="playNow('+i+')"><span class="nm">'+esc(f.name)+'</span><span class="sz">'+(f.size>0?(f.size/1073741824).toFixed(1)+'G':'')+'</span></div>').join('')+'</div></div>'
  }
  if((d.related||[]).length){
    h+='<div class="dsec"><div class="rh"><h2>相关推荐</h2></div><div class="grid">'+
      d.related.map(r=>cardHTML(r)).join('')+'</div></div>'
  }
  h+='</div>';
  c.innerHTML=h;
}
function playNow(i){nav('#/play?key='+encodeURIComponent(curKey)+'&i='+i)}

/* ---------- 排行榜（周/月/年） ---------- */
let trPeriod='week';
function goTrending(){nav('#/trending')}
async function renderTrending(){
  const c=$('content');renderTabs('trending');
  const seg=(v,label)=>'<div class="chip'+(trPeriod===v?' on':'')+'" onclick="trPeriod=&apos;'+v+'&apos;;renderTrending()">'+label+'</div>';
  let h='<div class="listtitle">排行榜</div><div class="filters"><div class="chips">'+
    seg('week','周榜')+seg('month','月榜')+seg('year','年榜')+'</div></div>';
  let d=null;
  try{d=await(await fetch('/api/portal/trending?period='+trPeriod)).json()}catch(e){}
  if(!d||!(d.items||[]).length){c.innerHTML=h+'<div class="empt">暂无数据</div>';return}
  h+='<div class="tlist">'+d.items.map(it=>{
    const pvTxt=it.period_views>0?((trPeriod==='week'?'本周':(trPeriod==='month'?'本月':'本年'))+it.period_views+' 次播放'):'新入库';
    return '<div class="trow" onclick="openDetail(&apos;'+escA(it.key)+'&apos;)">'+
      '<div class="trank'+(it.rank<=3?' top':'')+'">'+it.rank+'</div>'+
      (it.poster_path?('<img class="tposter" loading="lazy" src="/poster'+it.poster_path+'">'):'<div class="tposter ph2">🎬</div>')+
      '<div class="tinfo"><div class="tt">'+esc(it.title)+(it.year?'（'+it.year+'）':'')+'</div>'+
      '<div class="tv">'+pvTxt+(it.views>0?' · 累计 '+it.views:'')+(it.vote_average>0?' · ★ '+it.vote_average.toFixed(1):'')+'</div>'+
      '<div class="tov">'+esc(it.overview||'')+'</div></div></div>'
  }).join('')+'</div>';
  c.innerHTML=h;
}
function playResume(key,i){nav('#/play?key='+encodeURIComponent(key)+'&i='+i+'&p=resume')}
function copyLink0(){if(curFiles[0])copyTxt(curFiles[0].url)}

/* ---------- 播放页 ---------- */
async function renderPlay(key,idx,pct){
  const c=$('content');renderTabs('');
  if(!curKey||curKey!==key){await openDetailData(key)}
  reportPlayed(key);
  if(!curFiles.length){c.innerHTML='<div class="empt">该条目暂无视频文件</div>';return}
  if(idx>=curFiles.length)idx=0;
  const f=curFiles[idx];
  const saved=getProg()[key+'#'+idx];
  const startPct=(pct==='resume'&&saved)?saved.pct:0;
  c.innerHTML=
  '<div class="playwrap">'+
   '<div class="pleft">'+
    '<div id="player">'+
      '<video id="video" playsinline></video>'+
      '<div id="pmenu"></div>'+
      '<div id="pbar">'+
        '<button class="ib" id="pp" onclick="pp()" title="空格 播放/暂停">'+svgPlay()+'</button>'+
        '<button class="ib hide-m" onclick="seekBy(-10)" title="← 快退10秒">'+svgRew()+'</button>'+
        '<button class="ib hide-m" onclick="seekBy(10)" title="→ 快进10秒">'+svgFwd()+'</button>'+
        '<span class="pt" id="ptime">00:00 / 00:00</span>'+
        '<div id="seek" onclick="seekTo(event)"><div id="seekcur"><i></i></div></div>'+
        '<button class="ib" id="spd" onclick="spdMenu()">倍速 '+curRate+'x</button>'+
        '<button class="ib" id="sub" onclick="subMenu()">字幕</button>'+
        '<button class="ib" onclick="audMenu()">音轨</button>'+
        '<button class="ib hide-m" onclick="voltoggle()" title="↑↓ 静音">'+svgVol()+'</button>'+
        '<button class="ib" onclick="toggleFS()" title="全屏">'+svgFS()+'</button>'+
      '</div>'+
    '</div>'+
    '<div class="ptitle">'+esc(curMedia?curMedia.title:'')+(curMedia&&curMedia.year?'（'+curMedia.year+'）':'')+'</div>'+
    '<div class="pmeta" id="pnow">'+esc(f.name)+'</div>'+
    '<div class="fail" id="fail"><b id="hlserrmsg" style="color:#f87171"></b>播放失败：浏览器不支持该视频格式（MKV/H.265 常见）。<br>直链：<input id="faillink" readonly> <button class="ghost" onclick="copyTxt($(\'faillink\').value);this.textContent=\'已复制\'">复制</button><br>推荐用直链配合 PotPlayer/VLC/nPlayer 观看（支持内嵌音轨字幕），或在 Emby 客户端播放（支持转码）。</div>'+
   '</div>'+
   '<div class="pright"><h3>'+(curMedia&&curMedia.media_type==='tv'?'选集':'版本/文件')+'</h3><div id="eplist">'+
    curFiles.map((x,i)=>{
      const p=getProg()[key+'#'+i];
      return '<div class="epi'+(i===idx?' on':'')+'" id="epi-'+i+'" onclick="nav(\'#/play?key=\'+encodeURIComponent(curKey)+\'&i=\'+i+\'&p=resume\')">'+
        '<span class="nm">'+esc(x.name)+'</span>'+
        (p?'<span class="don">'+p.pct+'%</span>':'')+
        '<span class="sz">'+(x.size>0?(x.size/1073741824).toFixed(1)+'G':'')+'</span></div>'
    }).join('')+
   '</div></div>'+
  '</div>';
  startPlay(idx,startPct)
}
/* 播放计数上报：每部每次浏览器会话只计一次（切集/重进不重复计） */
function reportPlayed(key){
  try{if(sessionStorage.getItem('pv:'+key))return;sessionStorage.setItem('pv:'+key,'1')}catch(e){return}
  try{fetch('/api/portal/played',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key:key})})}catch(e){}
}
async function openDetailData(key){
  const d=await(await fetch('/api/portal/detail?key='+encodeURIComponent(key))).json();
  curMedia=d.media;curFiles=d.files||[];curSubs=d.subs||[];curKey=key;curDetail=d
}
let curProbe=null,curProbePick='',hls=null,curAudioRel=0,hlsRetried=false;
let curEngine='direct'; // emby | ffmpeg | direct
let embyPlay=null, embyAudio=-1, embySub=-1;
/* ---- 播放调试（控制台输出；?debug=1 或 sessionStorage.pdbg=1 开启）---- */
let PDBG=(()=>{try{if(new URLSearchParams(location.search).get('debug')==='1'){sessionStorage.setItem('pdbg','1');return true}return sessionStorage.getItem('pdbg')==='1'}catch(e){return false}})();
function pdbg(...a){if(PDBG)console.log('%c[播放]','color:#7c3aed;font-weight:bold',...a)}
let DBGSTAGES=[];
function dbgStage(name,ms,extra){DBGSTAGES.push(name+' '+Math.round(ms)+'ms'+(extra?' - '+extra:''));dbgRender()}
function dbgRender(){
  let el=$('dbgbar');
  if(!el){el=document.createElement('div');el.id='dbgbar';el.style.cssText='font-size:12px;color:var(--dim);background:var(--card);border:1px dashed var(--bd);border-radius:8px;padding:8px 12px;margin-top:8px;line-height:1.9';const pt=$('pnow');pt&&pt.parentNode&&pt.parentNode.insertBefore(el,pt.nextSibling)}
  el.innerHTML='<b>\u23f1 播放计时'+(curEngine?'（引擎='+curEngine+'）':'')+'</b><br>'+DBGSTAGES.join('<br>');
}
window.pdbg=()=>{PDBG=true;try{sessionStorage.setItem('pdbg','1')}catch(e){};dbgRender();console.log('调试已开启：播放后看播放器下方计时面板')};
async function tlog(label,fn){const t0=performance.now();pdbg(label,'…');try{const r=await fn();pdbg(label,'✓',(performance.now()-t0).toFixed(0)+'ms');return r}catch(e){pdbg(label,'✗',(performance.now()-t0).toFixed(0)+'ms',e);throw e}}
async function startPlay(idx,startPct,forceHLS,audioRel){
  const f=curFiles[idx];curFileIdx=idx;curFileUrl=f.url;
  const v=$('video');
  const t0=performance.now();
  DBGSTAGES=[];hlsRetried=false;
  const he0=$('hlserrmsg');if(he0)he0.textContent='';
  pdbg('=== 开始播放 ===',f.name,'startPct='+startPct);
  curAudioRel=audioRel||0;
  if(hls){hls.destroy();hls=null}
  curProbe=null;curSid='';embyPlay=null;embyAudio=-1;embySub=-1;
  // 引擎优先级：① 浏览器直出（302 直连 115，零服务器消耗）→
  // ② Emby 转码（秒拖/内嵌轨道/自动转码）→ ③ 门户自带 ffmpeg 转封装
  const ext=(f.name.split('.').pop()||'').toLowerCase();
  const direct=['mp4','webm','m4v','mov'].includes(ext)&&!forceHLS;
  if(direct){
    curEngine='direct';
    $('pnow').textContent=f.name+'（302 直连）';
    pdbg('引擎=直连(302)，src=',f.url);
    v.src=f.url;
    v.onloadedmetadata=()=>{dbgStage('直连出画',performance.now()-t0,'时长'+Math.round(v.duration)+'s');if(startPct>0&&v.duration)v.currentTime=v.duration*startPct/100};
    v.playbackRate=curRate;
    v.onerror=()=>{const E=['','中止','网络错误','解码失败','格式不支持'];dbgStage('错误',performance.now()-t0,E[v.error.code]||('code '+v.error.code));$('fail').style.display='block';$('faillink').value=f.url};
    v.play().then(()=>{$('pp').innerHTML=SVGNS.pause}).catch(e=>{if(e&&e.name!=='AbortError')console.warn('play:',e.name)});
    v.onplay=()=>{$('pp').innerHTML=SVGNS.pause};
    v.onpause=()=>{$('pp').innerHTML=SVGNS.play};
    subOff=true;curSubIdx=-1;
    [...v.querySelectorAll('track')].forEach(t=>t.remove());
    clearInterval(progTimer);progTimer=setInterval(()=>saveProgress(false),5000);
    let t;const pb=$('pbar'),pl=$('player');
    pl.onmousemove=pl.ontouchstart=()=>{pb.classList.remove('hide');clearTimeout(t);t=setTimeout(()=>{if(!$('video').paused)pb.classList.add('hide')},2600)};
    pl.onmouseleave=()=>{if(!$('video').paused)pb.classList.add('hide')}
    return
  }
  // MKV 等浏览器放不了：Emby 引擎（H.265 等不兼容编码由 Emby 转码）
  const okEmby=await startEmby(f,startPct);
  if(okEmby){v.playbackRate=curRate;return}
  // Emby 不可用：门户 ffmpeg 转码（H.265 自动转 H.264）
  if(window.noHls||typeof Hls==='undefined'){
    $('fail').style.display='block';$('faillink').value=f.url;
    v.src=f.url;
    probeTracks(f);
  }else{
    // 编码判定：优先 ffprobe 实测（文件名猜 265/hevc 常漏判，漏判=copy 出
    // H.265 分片浏览器播不了）；探测失败退回文件名猜测
    if(!curProbe||curProbePick!==f.pick){await probeTracks(f)}
    const vcod=(((curProbe||{}).video||[])[0]||{}).codec||'';
    const isH265=/hevc|265|av1|vp9/i.test(vcod)||/265|hevc/i.test(f.name);
    if(isH265){
      dbgStage('H.265 转码',0,'Emby 不可用，ffmpeg 转码 H.265→H.264（'+(vcod||'文件名判定')+'）');
      $('pnow').textContent=f.name+'（H.265 转码中，首次加载稍慢）';
    }
    await startHLS(f,audioRel||0,isH265?'transcode':'copy');
  }
  v.playbackRate=curRate;
  v.onerror=()=>{const E=['','中止','网络错误','解码失败','格式不支持'];dbgStage('错误',performance.now()-t0,E[v.error.code]||('code '+v.error.code));$('fail').style.display='block';$('faillink').value=f.url};
  v.onloadedmetadata=()=>{if(startPct>0&&v.duration)v.currentTime=v.duration*startPct/100};
  v.play().then(()=>{$('pp').innerHTML=SVGNS.pause}).catch(e=>{if(e&&e.name!=='AbortError')console.warn('play:',e.name)});
  v.onplay=()=>{$('pp').innerHTML=SVGNS.pause};
  v.onpause=()=>{$('pp').innerHTML=SVGNS.play};
  subOff=true;curSubIdx=-1;
  [...v.querySelectorAll('track')].forEach(t=>t.remove());
  clearInterval(progTimer);progTimer=setInterval(()=>saveProgress(false),5000);
  // 控制条自动隐藏
  let t;const pb=$('pbar'),pl=$('player');
  pl.onmousemove=pl.ontouchstart=()=>{pb.classList.remove('hide');clearTimeout(t);t=setTimeout(()=>{if(!$('video').paused)pb.classList.add('hide')},2600)};
  pl.onmouseleave=()=>{if(!$('video').paused)pb.classList.add('hide')}
}
/* Emby 引擎：master.m3u8 全长播放列表（拖动秒跳），音轨/字幕服务端切换 */
async function startEmby(f,startPct){
  const t0=performance.now();
  try{
    const d=await tlog('Emby匹配',()=>fetch('/api/portal/embyplay?key='+encodeURIComponent(curKey)+'&f='+encodeURIComponent(f.name)).then(r=>r.json()));
    dbgStage('Emby 匹配',performance.now()-t0);
    if(!d||!d.found){
      pdbg('Emby 不可用：',d&&d.reason);
      dbgStage('Emby 未命中',performance.now()-t0,d&&d.reason?d.reason:'未知原因');
      $('pnow').textContent=f.name+(d&&d.reason?'（'+d.reason+'，走直连/转封装）':'');
      return false
    }
    embyPlay=d;curEngine='emby';
    pdbg('Emby 命中：item='+d.item_id,'音轨 '+d.audio.length+' 个','字幕 '+d.subs.length+' 个',(performance.now()-t0).toFixed(0)+'ms');
    $('pnow').textContent=f.name+'（Emby 引擎）';
    const v=$('video');
    let u=d.url;
    if(embyAudio>=0)u+='&AudioStreamIndex='+embyAudio;
    if(embySub>=0)u+='&SubtitleStreamIndex='+embySub;
    // PlaySessionId：Emby 转码会话标识（缺了分片请求报 400）
    const psid='portal-'+Date.now()+'-'+Math.random().toString(36).slice(2,8);
    u+='&PlaySessionId='+psid+'&VideoCodec=h264&AudioCodec=aac,mp3&TranscodingMaxAudioChannels=2&SegmentContainer=ts';
    pdbg('Emby m3u8：',u);
    // 转码首段要等 Emby 起 ffmpeg（H.265 弱机 10~40 秒），默认 20 秒分片超时
    // 会掐断请求（服务端日志表现为 context canceled），放宽到 90 秒
    hls=new Hls({maxBufferLength:30,maxMaxBufferLength:60,
      manifestLoadTimeOut:30*1000,manifestLoadingTimeOut:30*1000,manifestLoadingMaxRetry:4,
      levelLoadTimeOut:30*1000,levelLoadingTimeOut:30*1000,levelLoadingMaxRetry:4,
      fragLoadingTimeOut:90*1000,fragLoadingMaxRetry:8});
    hls.loadSource(u);
    hls.attachMedia(v);
    let firstFrag=false,manifestAt=0;
    hls.on(Hls.Events.MANIFEST_PARSED,()=>{manifestAt=performance.now();dbgStage('播放列表就绪',manifestAt-t0)});
    hls.on(Hls.Events.FRAG_LOADED,(e,dd)=>{if(!firstFrag){firstFrag=true;dbgStage('首分片下载',performance.now()-t0)}});
    hls.on(Hls.Events.ERROR,(e,data)=>{
      pdbg('HLS 错误：',data.type,data.details,data.fatal?'(fatal)':'',data.response&&('HTTP '+data.response.code));
      if(data.fatal){
        const he=$('hlserrmsg');if(he)he.textContent='Emby 引擎播放失败：'+data.details+'（'+data.type+'）';
        $('fail').style.display='block';$('faillink').value=f.url
      }
    });
    $('pnow').textContent=f.name+'（Emby 引擎，转码启动中…）';
    v.onloadedmetadata=()=>{dbgStage('出画（可播）',performance.now()-t0,'时长'+Math.round(v.duration)+'s');if(startPct>0&&v.duration)v.currentTime=v.duration*startPct/100};
    return true
  }catch(e){pdbg('Emby 异常：',e.message);return false}
}
async function startHLS(f,audioRel,vc){
  const v=$('video');const t0=performance.now();
  curEngine='ffmpeg';
  $('pnow').textContent=f.name+'（转封装中…）';
  pdbg('引擎=ffmpeg 转封装');
  try{
    const d=await tlog('转封装会话启动',()=>fetch('/api/portal/hls/start',{method:'POST',body:JSON.stringify({pick:f.pick,a:audioRel,vc:vc||'copy'})}).then(r=>r.json()));
    if(d.error){throw new Error(d.error)}
    curSid=d.sid;
    pdbg('会话 sid='+d.sid,'m3u8='+d.m3u8);
    // 等 playlist 真正产出分片（ffmpeg 切出首段后 m3u8 才含 #EXTINF）：
    // 急着 attach 会让 hls.js 拿到 404/空列表进入内部异常状态
    //（典型症状：控制台 startTime undefined + play() AbortError，播放无声卡死）
    let ready=false,lastErr='';
    for(let i=0;i<35;i++){
      try{
        const r=await fetch(d.m3u8);
        if(r.ok){
          const t=await r.text();
          if(t.indexOf('#EXTINF')>=0){ready=true;break}
          lastErr='列表为空';
        }else lastErr='HTTP '+r.status;
      }catch(e2){lastErr=e2.message}
      await new Promise(res=>setTimeout(res,1000));
    }
    if(!ready){throw new Error('转封装未产出分片（'+(lastErr||'超时')+'）——ffmpeg 可能已退出，请查看服务端日志')}
    dbgStage('转封装列表就绪',performance.now()-t0);
    hls=new Hls({maxBufferLength:30});
    hls.loadSource(d.m3u8);
    hls.attachMedia(v);
    let firstFrag=false;
    hls.on(Hls.Events.MANIFEST_PARSED,()=>dbgStage('转封装列表就绪',performance.now()-t0));
    hls.on(Hls.Events.FRAG_LOADED,(e,dd)=>{if(!firstFrag){firstFrag=true;dbgStage('转封装首分片',performance.now()-t0)}});
    hls.on(Hls.Events.ERROR,(e,data)=>{
      pdbg('HLS 错误：',data.type,data.details,data.fatal?'(fatal)':'');
      if(data.fatal){
        // copy（无损转封装）播放失败：源多半是探测漏判的 H.265，自动升级
        // 转码重试一次；已是转码仍失败才放弃
        if(vc!=='transcode'&&!hlsRetried){
          hlsRetried=true;
          dbgStage('转封装失败',0,data.details+'，自动升级转码重试');
          $('pnow').textContent=f.name+'（自动切换转码重试…）';
          if(hls){hls.destroy();hls=null}
          startHLS(f,audioRel,'transcode');
          return
        }
        const he=$('hlserrmsg');if(he)he.textContent='播放流错误：'+data.details+'（'+data.type+'）';
        $('fail').style.display='block';$('faillink').value=f.url
      }
    });
    $('pnow').textContent=f.name;
  }catch(e){
    pdbg('转封装失败：',e.message);
    const he=$('hlserrmsg');if(he)he.textContent='服务端转封装启动失败：'+e.message;
    $('fail').style.display='block';$('faillink').value=f.url;
    $('pnow').textContent=f.name+'（'+e.message+'）';
    v.src=f.url;
  }
}
/* 轨道探测：有 ffmpeg 时返回内嵌音轨/字幕 */
async function probeTracks(f){
  if(!f.pick)return;
  curProbe=null;curProbePick=f.pick; // 失效旧探测（切文件后不能沿用上一集的轨道）
  try{
    const d=await(await fetch('/api/portal/probe?pick='+f.pick)).json();
    if(!d.error)curProbe=d;
  }catch(e){}
}
function stopProg(){clearInterval(progTimer);saveProgress(true);
  if(hls){hls.destroy();hls=null}}
window.addEventListener('beforeunload',()=>saveProgress(true));
function saveProgress(final){
  if(!curKey||!curFileUrl)return;
  const v=$('video');if(!v||!v.duration)return;
  const pct=Math.min(100,Math.round(v.currentTime/v.duration*100));
  if(pct>=95){delProg(curKey+'#'+curFileIdx);return}
  setProg(curKey+'#'+curFileIdx,{key:curKey,fileIdx:curFileIdx,title:curMedia?curMedia.title:'',epName:(curFiles[curFileIdx]||{}).name||'',poster:curMedia?curMedia.poster_path:'',pct:pct,ts:Date.now()});
  if(final)clearInterval(progTimer)
}
/* 扁平线性 SVG 图标（16px，描边风格） */
const SVGNS={
play:'<svg width="15" height="15" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>',
pause:'<svg width="15" height="15" viewBox="0 0 16 16" fill="currentColor"><rect x="3.5" y="2.5" width="3" height="11" rx="1"/><rect x="9.5" y="2.5" width="3" height="11" rx="1"/></svg>',
rew:'<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M8.5 3.5 4 8l4.5 4.5M13 3.5 8.5 8l4.5 4.5"/></svg>',
fwd:'<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M7.5 3.5 12 8l-4.5 4.5M3 3.5 7.5 8 3 12.5"/></svg>',
vol:'<svg width="15" height="15" viewBox="0 0 16 16" fill="currentColor"><path d="M2.5 6v4h2.5L8.5 13V3L5 6z"/><path d="M10.5 5.5a3.5 3.5 0 010 5" fill="none" stroke="currentColor" stroke-width="1.4"/></svg>',
fs:'<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M6 2.5H2.5V6M10 2.5h3.5V6M6 13.5H2.5V10M10 13.5h3.5V10"/></svg>'
};
function svgPlay(){return $('video')&&!$('video').paused?SVGNS.pause:SVGNS.play}
function svgRew(){return SVGNS.rew}
function svgFwd(){return SVGNS.fwd}
function svgVol(){return SVGNS.vol}
function svgFS(){return SVGNS.fs}
function pp(){const v=$('video');if(v.paused){v.play()}else{v.pause()}}
function seekBy(d){const v=$('video');v.currentTime=Math.max(0,v.currentTime+d)}
function seekTo(ev){const v=$('video');if(!v.duration)return;const r=$('seek').getBoundingClientRect();v.currentTime=(ev.clientX-r.left)/r.width*v.duration}
/* 进度条拖动（Pointer Events：按下即预览时间、拖动实时 seek、抬起结束） */
let seekDrag=false;
(function(){const el=$('seek');if(!el)return;
  const pct=ev=>{const r=el.getBoundingClientRect();return Math.min(1,Math.max(0,(ev.clientX-r.left)/r.width))};
  el.addEventListener('pointerdown',ev=>{seekDrag=true;el.setPointerCapture(ev.pointerId);const v=$('video');if(v.duration)v.currentTime=pct(ev)*v.duration});
  el.addEventListener('pointermove',ev=>{if(!seekDrag)return;const v=$('video');if(v.duration)v.currentTime=pct(ev)*v.duration});
  el.addEventListener('pointerup',()=>{seekDrag=false});
  el.addEventListener('pointercancel',()=>{seekDrag=false});
})();
function toggleFS(){
  const p=$('player'),v=$('video');
  if(document.fullscreenElement){document.exitFullscreen();return}
  if(p.requestFullscreen){p.requestFullscreen();return}
  // iOS Safari 不支持元素全屏：回退到视频原生全屏（此前点了没反应）
  if(v&&v.webkitEnterFullscreen){v.webkitEnterFullscreen();return}
  if(v&&v.webkitSupportsFullscreen){v.webkitEnterFullscreen()}
}
function voltoggle(){const v=$('video');v.muted=!v.muted}
function closeMenu(){$('pmenu').style.display='none';$('pmenu').dataset.on=''}
function spdMenu(){
  const m=$('pmenu');
  if(m.dataset.on==='spd'){closeMenu();return}
  m.dataset.on='spd';
  const rates=[0.5,0.75,1,1.25,1.5,2,2.5,3];
  m.innerHTML=rates.map(r=>'<div class="'+(r===curRate?'on':'')+'" onclick="setRate('+r+')">'+r+'x'+(r===1?'（正常）':'')+'</div>').join('');
  m.style.display='block'
}
function setRate(r){curRate=r;$('video').playbackRate=r;$('spd').textContent='倍速 '+r+'x';closeMenu()}
/* 字幕：外挂（srt/ass/vtt）+ 本地字幕文件 */
function subMenu(){
  const m=$('pmenu');
  if(m.dataset.on==='sub'){closeMenu();return}
  m.dataset.on='sub';
  // Emby 引擎：内嵌+外挂字幕统一由 Emby 服务端烧录/渲染
  if(curEngine==='emby'&&embyPlay){
    let h2='<div class="'+(embySub<0?'on':'')+'" onclick="switchEmbySub(-1)">关闭字幕</div>';
    if(embyPlay.subs&&embyPlay.subs.length){
      h2+='<div class="tip">字幕（Emby 服务端处理）：</div>';
      embyPlay.subs.forEach(x=>{
        h2+='<div class="'+(embySub===x.index?'on':'')+'" onclick="switchEmbySub('+x.index+')">'+esc((x.external?'外挂 · ':'内嵌 · ')+x.label)+'</div>'
      })
    }else{h2+='<div class="tip">该集没有可用字幕轨</div>'}
    m.innerHTML=h2;m.style.display='block';return
  }
  let h='<div class="'+(subOff?'on':'')+'" onclick="setSub(-1)">关闭字幕</div>';
  // 内嵌字幕（ffmpeg 提取）
  if(curProbe&&curProbe.subs&&curProbe.subs.length){
    h+='<div class="tip">内嵌字幕：</div>';
    curProbe.subs.forEach(x=>{
      const label=(x.language||'未知')+(x.title?' · '+x.title:'');
      h+='<div onclick="setEsub(\''+x.rel+'\',\''+escA(label)+'\')">内嵌 · '+esc(label)+'</div>'
    })
  }
  // 外挂字幕
  if(curSubs.length){
    h+='<div class="tip">外挂字幕：</div>';
    curSubs.forEach((x,i)=>{h+='<div class="'+(!subOff&&curSubIdx===1000+i?'on':'')+'" onclick="setSub('+i+')">'+esc(x.label)+'</div>'})
  }
  h+='<div onclick="$(\'subfile\').click()">本地字幕文件…</div>';
  if(!curSubs.length&&!(curProbe&&curProbe.subs&&curProbe.subs.length))h+='<div class="tip">未识别到字幕</div>'+h;
  m.innerHTML=h;m.style.display='block'
}
async function setEsub(rel,label){
  closeMenu();
  const f=curFiles[curFileIdx];
  try{
    const r=await fetch('/api/portal/estr?pick='+f.pick+'&s='+rel);
    if(!r.ok)throw new Error(await r.text());
    const vtt=await r.text();
    subOff=false;curSubIdx=rel;
    mountVTT(vtt);
    toast2('已挂载字幕：'+label)
  }catch(e){alert('内嵌字幕提取失败：'+e.message)}
}
async function setSub(i){
  const v=$('video');
  [...v.querySelectorAll('track')].forEach(t=>t.remove());
  if(i<0){subOff=true;curSubIdx=-1}
  else{
    subOff=false;curSubIdx=i;
    try{
      const txt=await(await fetch('/api/portal/sub?pick='+curSubs[i].pick)).text();
      mountVTT(toVTT(txt,curSubs[i].name))
    }catch(e){alert('字幕加载失败：'+e.message)}
  }
  closeMenu()
}
function localSub(inp){
  const f=inp.files[0];if(!f)return;
  const r=new FileReader();
  r.onload=()=>{subOff=false;mountVTT(toVTT(r.result,f.name));closeMenu()};
  r.readAsText(f,'utf-8');
  inp.value=''
}
function mountVTT(vttText){
  const v=$('video');
  [...v.querySelectorAll('track')].forEach(t=>t.remove());
  const url=URL.createObjectURL(new Blob([vttText],{type:'text/vtt'}));
  const t=document.createElement('track');
  t.kind='subtitles';t.src=url;t.srclang='zh';t.default=true;
  v.appendChild(t);
  setTimeout(()=>{if(v.textTracks[0])v.textTracks[0].mode='showing'},50)
}
function toVTT(txt,name){
  if(/\.vtt$/i.test(name))return txt;
  if(/\.ass$|\.ssa$/i.test(name)){
    let out=['WEBVTT'];
    const lines=txt.split(/\r?\n/);
    for(const ln of lines){
      if(ln.indexOf('Dialogue:')!==0)continue;
      const parts=ln.substring(9).split(',');
      if(parts.length<10)continue;
      const t1=assT(parts[1]),t2=assT(parts[2]),text=parts.slice(9).join(',').replace(/\{[^}]*\}/g,'').replace(/\\N/g,' ');
      if(text.trim())out.push(t1+' --> '+t2+'\n'+text)
    }
    return out.join('\n')
  }
  return 'WEBVTT\n\n'+txt.replace(/\r+/g,'').replace(/^(\d+)\n/gm,'').replace(/,/g,'.')
}
function assT(t){
  const m=t.trim().split(':');
  const sec=m[2].split('.');
  return m[0].padStart(2,'0')+':'+m[1]+':'+sec[0].padStart(2,'0')+'.'+(sec[1]||'0').padEnd(3,'0')
}
/* 音轨：内嵌音轨切换（转封装模式重启会话）；直出模式回退提示 */
function audMenu(){
  const m=$('pmenu');
  if(m.dataset.on==='aud'){closeMenu();return}
  m.dataset.on='aud';
  // Emby 引擎：内嵌音轨服务端切换
  if(curEngine==='emby'&&embyPlay&&embyPlay.audio&&embyPlay.audio.length){
    let h2='<div class="tip">内嵌音轨：</div>';
    embyPlay.audio.forEach(a=>{
      h2+='<div class="'+(embyAudio===a.index?'on':'')+'" onclick="switchEmbyAudio('+a.index+')">'+esc(a.label)+'</div>'
    });
    m.innerHTML=h2;m.style.display='block';return
  }
  let h='';
  const f=curFiles[curFileIdx];
  if(curProbe&&curProbe.audio&&curProbe.audio.length){
    h+='<div class="tip">内嵌音轨：</div>';
    curProbe.audio.forEach(a=>{
      const label=(a.language||'未知')+(a.title?' · '+a.title:'')+' ('+a.codec+(a.channels?a.channels+'ch':'')+')';
      h+='<div class="'+(a.rel===curAudioRel?'on':'')+'" onclick="switchAudio('+a.rel+')">'+esc(label)+'</div>'
    });
  }else if(f&&['mp4','webm','m4v','mov'].includes((f.name.split('.').pop()||'').toLowerCase())){
    h+='<div class="tip">该文件是浏览器直出模式，切换音轨需转封装</div><div onclick="forceHLSAudio(0)">切到转封装模式</div>';
  }else{
    h+='<div class="tip">未识别到内嵌音轨</div>';
  }
  if(curFiles.length>1){
    h+='<div class="tip">切换文件/版本：</div>';
    curFiles.forEach((x,i)=>{h+='<div class="'+(i===curFileIdx?'on':'')+'" onclick="nav(\'#/play?key=\'+encodeURIComponent(curKey)+\'&i=\'+i+\'&p=resume\')">'+esc(x.name)+'</div>'})
  }
  m.innerHTML=h;m.style.display='block'
}
async function switchAudio(rel){
  closeMenu();
  const f=curFiles[curFileIdx];
  const pct=$('video').duration?Math.round($('video').currentTime/$('video').duration*100):0;
  await startHLS(f,rel);
  curAudioRel=rel;
  if(pct>0&&$('video').duration)$('video').currentTime=$('video').duration*pct/100
}
async function forceHLSAudio(rel){await switchAudio(rel)}
async function switchEmbyAudio(idx){
  closeMenu();
  const v=$('video');
  const pct=v.duration?Math.round(v.currentTime/v.duration*100):0;
  if(hls){hls.destroy();hls=null}
  embyAudio=idx;
  await startEmby(curFiles[curFileIdx],pct)
}
async function switchEmbySub(idx){ // idx=-1 关闭
  closeMenu();
  const v=$('video');
  const pct=v.duration?Math.round(v.currentTime/v.duration*100):0;
  if(hls){hls.destroy();hls=null}
  embySub=idx;
  subOff=idx<0;
  await startEmby(curFiles[curFileIdx],pct)
}
/* 快捷键 */
document.addEventListener('keydown',e=>{
  if(location.hash.indexOf('#/play')!==0)return;
  if(e.target.tagName==='INPUT')return;
  const v=$('video');if(!v.src)return;
  if(e.code==='Space'){e.preventDefault();pp()}
  else if(e.key==='ArrowLeft'){seekBy(-5)}
  else if(e.key==='ArrowRight'){seekBy(5)}
  else if(e.key==='ArrowUp'){v.volume=Math.min(1,v.volume+.1)}
  else if(e.key==='ArrowDown'){v.volume=Math.max(0,v.volume-.1)}
  else if(e.key==='+'||e.key==='='){const rs=[0.5,0.75,1,1.25,1.5,2,2.5,3];const i=rs.indexOf(curRate);if(i<rs.length-1)setRate(rs[i+1])}
  else if(e.key==='-'){const rs=[0.5,0.75,1,1.25,1.5,2,2.5,3];const i=rs.indexOf(curRate);if(i>0)setRate(rs[i-1])}
});
setInterval(()=>{
  const v=$('video');if(!v||!v.src||!v.duration)return;
  const tot=(curProbe&&curProbe.duration&&curProbe.duration>v.duration)?curProbe.duration:v.duration;
  $('seekcur').style.width=(v.currentTime/tot*100)+'%';
  $('ptime').textContent=fmtT(v.currentTime)+' / '+fmtT(tot)
},500);
function fmtT(s){s=Math.floor(s);const m=Math.floor(s/60),ss=s%60;const h=Math.floor(m/60);return (h?h+':':'')+String(h?m%60:m).padStart(2,'0')+':'+String(ss).padStart(2,'0')}
function copyTxt(t){if(navigator.clipboard)navigator.clipboard.writeText(t).then(()=>{toast2('已复制')});else{const i=$('faillink');if(i){i.value=t;i.select();document.execCommand('copy')}}}
function toast2(m){const d=document.createElement('div');d.textContent=m;d.style.cssText='position:fixed;top:60px;left:50%;transform:translateX(-50%);background:#111827;color:#fff;padding:8px 18px;border-radius:20px;font-size:13px;z-index:999';document.body.appendChild(d);setTimeout(()=>d.remove(),1600)}

/* ---------- 启动 ---------- */
if(PDBG)dbgRender();
route();
</script>
</body>
</html>`
