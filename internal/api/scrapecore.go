package api

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ==================== 刮削核心 ====================
//
// 一个片目：TMDB 详情 → NFO（影片 / 剧 / 季 / 集）→ 图片（海报、背景、logo、季海报、集剧照）。
// 产物交给 metaWriter，写到哪里、覆不覆盖由 writer 决定。
//
// 日志口径（2026-09-28 起）：每个产物一行，写清从哪下的、多大、多久、落在哪 ——
// 此前一个片目只有一行汇总，图床配错、某几集剧照 404、探测失败都要翻代码才知道。
// 逐个产物的行走 vlog（跟「详细日志」开关），片目开头与结尾的汇总走 log。
// 已存在跳过的不逐个打：重刮一部两百集的剧就是四百行「已存在」，汇总里给个数足够。

// sharedStillMin 同一季里至少几集共用一张剧照才算占位图。
// 上下集共用一张（前后篇）是正常的，三集起才像是上传者拿同一张图糊弄过去
const sharedStillMin = 3

// scrapeSession 一次刮削任务：跨片目共享的依赖与图片缓存
type scrapeSession struct {
	tc   *TmdbClient
	opts fileScrapeOpts
	w    metaWriter
	rep  scrapeReporter
	// imgs 这次任务已下载过的图（尺寸 + 路径 → 内容）：季海报与主海报撞图、
	// 关掉占位剧照判定时几十集共用一张剧照，都只下一次
	imgs map[string]*fetchedImage
	// fetch 拉图的实现（测试换成桩）
	fetch func(imgPath, size string) ([]byte, string, error)
	// probe 探测的实现（测试换成桩）
	probe func(pickCode string) (*probeResult, bool, string)
}

type fetchedImage struct {
	data []byte
	url  string
	err  error
}

func newScrapeSession(tc *TmdbClient, opts fileScrapeOpts, w metaWriter, rep scrapeReporter) *scrapeSession {
	return &scrapeSession{tc: tc, opts: opts, w: w, rep: rep, imgs: map[string]*fetchedImage{},
		fetch: tmdbFetchImageSized, probe: probeCached}
}

// titleScrapeStat 一个片目的账单
type titleScrapeStat struct {
	Wrote       []string // 写入的文件名（汇总日志用）
	written     []string // 写入的本地绝对路径（片目被移走时回收，见 scrapeCompensate）
	Skipped     int      // 已存在、按「只补缺失」跳过
	Reused      int      // 复用本次任务已下载的图
	Placeholder int      // 判为占位剧照、没写的集
	Probed      int      // 实际探测（不含缓存命中）
	ProbeCached int
	Failed      int
	Gone        bool // 写入时发现目录已不在（片目刚被移走 / 删掉）
}

// summary 写入的文件名：集 NFO / 集剧照成批出现，按类归并成计数，免得一部剧刷一整屏
func (st *titleScrapeStat) summary() string {
	var fixed []string
	epNFO, epThumb := 0, 0
	for _, n := range st.Wrote {
		switch {
		case strings.HasSuffix(n, "-thumb.jpg"):
			epThumb++
		case strings.HasSuffix(n, ".nfo") && n != "tvshow.nfo" && n != "season.nfo" && n != "movie.nfo":
			epNFO++
		default:
			fixed = append(fixed, n)
		}
	}
	if epNFO > 0 {
		fixed = append(fixed, fmt.Sprintf("视频 NFO×%d", epNFO))
	}
	if epThumb > 0 {
		fixed = append(fixed, fmt.Sprintf("集剧照×%d", epThumb))
	}
	return strings.Join(fixed, " ")
}

// titleRun 刮一个片目时的状态
type titleRun struct {
	s  *scrapeSession
	t  scrapeTitle
	st *titleScrapeStat
	// tmdbID 自愈后的编号（目录名标记的查无条目时按标题搜到的那个）
	tmdbID  int
	eps     []scrapeEp
	seasons map[int]map[int]tmdbEpisodeInfo
}

// scrapeEp 片目里一个能解析出季集号的视频
type scrapeEp struct {
	v           scrapeVideo
	season, ep  int
	placeholder bool // 占位剧照：不写 -thumb.jpg
}

// scrapeTitleMeta 刮一个片目，返回账单
func (s *scrapeSession) scrapeTitleMeta(t scrapeTitle) *titleScrapeStat {
	r := &titleRun{s: s, t: t, st: &titleScrapeStat{}, tmdbID: t.TmdbID, seasons: map[int]map[int]tmdbEpisodeInfo{}}
	if t.Kind == "tv" {
		for _, v := range t.Videos {
			if season, ep := scrapeEpisodeNo(v.Name); ep > 0 {
				r.eps = append(r.eps, scrapeEp{v: v, season: season, ep: ep})
			}
		}
	}
	r.run()
	s.rep.sub("", 0, 0, "")
	return r.st
}

func (r *titleRun) logv(format string, args ...any) {
	vlog("[影视刮削]   "+format, args...)
}

// fail 记一处错误：进任务结果，也打日志
func (r *titleRun) fail(format string, args ...any) {
	r.st.Failed++
	r.s.rep.errf("%s: "+format, append([]any{r.t.Title}, args...)...)
}

// skip 这个产物按「只补缺失」会被跳过吗（跳过就计数，调用方不再拉图 / 探测）
func (r *titleRun) skip(d metaDest, name string) bool {
	if sk, ok := r.s.w.(metaSkipper); ok && sk.skip(d, name) {
		r.st.Skipped++
		return true
	}
	return false
}

// put 写一个产物；how 是日志里「怎么来的」那一截
func (r *titleRun) put(d metaDest, name string, data []byte, how string) bool {
	wrote, err := r.s.w.put(d, name, data)
	switch {
	case errors.Is(err, errMetaDirGone):
		if !r.st.Gone {
			r.logv("○ %s 所在目录已不在（片目可能刚被整理 / 删除挪走），本部后续产物都会跳过", name)
		}
		r.st.Gone = true
		return false
	case err != nil:
		r.fail("写 %s 失败 %v", name, err)
		return false
	case !wrote:
		r.st.Skipped++
		return false
	}
	r.st.Wrote = append(r.st.Wrote, name)
	if d.Local != "" {
		r.st.written = append(r.st.written, filepath.Join(d.Local, name))
	}
	r.logv("✎ %s%s → %s", name, how, metaDestText(d))
	return true
}

func metaDestText(d metaDest) string {
	if d.Local != "" {
		return d.Local
	}
	return "网盘 " + d.CloudRel
}

// fetchImage 拉一张图，同一次任务里同尺寸同路径只下一次
func (r *titleRun) fetchImage(imgPath, size string) (img *fetchedImage, reused bool, took time.Duration) {
	key := size + imgPath
	if img, ok := r.s.imgs[key]; ok {
		return img, true, 0
	}
	start := time.Now()
	data, url, err := r.s.fetch(imgPath, size)
	img = &fetchedImage{data: data, url: url, err: err}
	if err == nil {
		// 只缓存成功的：失败多半是图床临时不通，下一个片目遇到同一张图该再试
		r.s.imgs[key] = img
	}
	return img, false, time.Since(start)
}

// fetchPut 先问 writer 要不要，再拉图写入；已有的不拉
func (r *titleRun) fetchPut(d metaDest, imgPath, size, name string) {
	if imgPath == "" {
		r.logv("○ %s：TMDB 上没有这张图", name)
		return
	}
	if r.skip(d, name) {
		return
	}
	img, reused, took := r.fetchImage(imgPath, size)
	if img.err != nil {
		r.fail("拉图失败 %s ← %s：%v", name, img.url, img.err)
		return
	}
	how := fmt.Sprintf(" ← %s（%s，%s）", img.url, humanBytes(len(img.data)), took.Round(100*time.Millisecond))
	if reused {
		r.st.Reused++
		how = fmt.Sprintf(" ← 复用本次已下载的 %s", img.url)
	}
	r.put(d, name, img.data, how)
}

// probe 探测一个视频的轨道（开关关着返回 nil）。失败记错误，NFO 照写只是不带 streamdetails
func (r *titleRun) probe(v scrapeVideo) *probeResult {
	if !r.s.opts.Probe {
		return nil
	}
	if v.PickCode == "" {
		r.logv("○ 探测 %s：台账里没有 pickcode，跳过", v.Name)
		return nil
	}
	start := time.Now()
	p, cached, perr := r.s.probe(v.PickCode)
	if p == nil {
		r.fail("轨道探测失败 %s：%s（NFO 照写，不含 streamdetails）", v.Name, truncateStr(perr, 160))
		return nil
	}
	if cached {
		r.st.ProbeCached++
		r.logv("▣ 探测 %s：%s（缓存）", v.Name, probeBrief(p))
	} else {
		r.st.Probed++
		r.logv("▣ 探测 %s：%s（%s）", v.Name, probeBrief(p), time.Since(start).Round(100*time.Millisecond))
	}
	return p
}

// season 某季的集信息（每个片目每季只打一次 TMDB，结果带日志）
func (r *titleRun) season(n int) map[int]tmdbEpisodeInfo {
	if m, ok := r.seasons[n]; ok {
		return m
	}
	start := time.Now()
	m, cached, err := r.s.tc.tmdbSeasonEpisodes(r.tmdbID, n)
	switch {
	case err != nil:
		r.fail("TMDB 第 %d 季集信息失败 %v（集 NFO 只有季集号，没有集剧照）", n, err)
	case cached:
		r.logv("GET /tv/%d/season/%d：%d 集（缓存）", r.tmdbID, n, len(m))
	default:
		r.logv("GET /tv/%d/season/%d：%d 集（%s）", r.tmdbID, n, len(m), time.Since(start).Round(100*time.Millisecond))
	}
	r.seasons[n] = m
	return m
}

func (r *titleRun) run() {
	t, s := r.t, r.s
	kind := t.Kind
	// images 随详情一次带回（clearlogo / landscape 从这里挑），不多打一次 TMDB。
	// 不写 include_image_language 时 TMDB 只回 zh 与无语言的图，英文 logo 就挑不到了
	params := map[string]string{"language": "zh-CN", "append_to_response": "credits,images",
		"include_image_language": "zh,en,null"}
	s.rep.sub("详情", 0, 0, "")
	start := time.Now()
	body, err := s.tc.get("/"+kind+"/"+strconv.Itoa(r.tmdbID), params)
	if err != nil {
		// 详情 404 = 目录名标记的 tmdb id 查无条目（整理时匹配错/条目已删）：
		// 回退按标题+年份搜一次，自愈错误 ID
		var alt *TmdbMedia
		if kind == "tv" {
			alt, _ = s.tc.SearchTV(t.Title, t.Year)
		} else {
			alt, _ = s.tc.SearchMovie(t.Title, t.Year)
		}
		if alt == nil || alt.TmdbID == 0 {
			r.fail("TMDB 详情失败 %v（id=%d，按标题搜索也未命中）", err, r.tmdbID)
			return
		}
		log.Printf("[影视刮削] ○ %s: 标记 id=%d 查无详情，按标题匹配到 id=%d，已自愈", t.Title, r.tmdbID, alt.TmdbID)
		body, err = s.tc.get("/"+kind+"/"+strconv.Itoa(alt.TmdbID), params)
		if err != nil {
			r.fail("TMDB 详情失败 %v", err)
			return
		}
		// NFO 的 uniqueid 与逐集信息都要用自愈后的 id：沿用旧 id 的话 NFO 里写的仍是
		// 查无条目的那个，剧集每季的集信息也会全部拉空
		r.tmdbID = alt.TmdbID
	}
	r.logv("GET /%s/%d 详情（%s）", kind, r.tmdbID, time.Since(start).Round(100*time.Millisecond))

	if s.opts.WriteNFO {
		if kind == "movie" {
			r.movieNFO(body)
		} else {
			r.tvNFO(body)
		}
	}
	if s.opts.WriteImages && !s.rep.stopped() && !r.st.Gone {
		r.images(body)
	}
}

// ---- NFO ----

func (r *titleRun) movieNFO(body []byte) {
	t := r.t
	var d struct {
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Overview      string  `json:"overview"`
		VoteAverage   float64 `json:"vote_average"`
		ReleaseDate   string  `json:"release_date"`
		Runtime       int     `json:"runtime"`
		IMDbID        string  `json:"imdb_id"`
		Genres        []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Credits struct {
			Cast []struct {
				Name      string `json:"name"`
				Character string `json:"character"`
			} `json:"cast"`
			Crew []struct {
				Job  string `json:"job"`
				Name string `json:"name"`
			} `json:"crew"`
		} `json:"credits"`
		ProductionCompanies []struct {
			Name string `json:"name"`
		} `json:"production_companies"`
	}
	if json.Unmarshal(body, &d) != nil {
		r.fail("详情解析失败")
		return
	}
	nfo := nfoMovie{
		Title: d.Title, OriginalTitle: d.OriginalTitle, Plot: d.Overview,
		Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
		Year:    dateYear(d.ReleaseDate), Premiered: d.ReleaseDate,
		Runtime: d.Runtime,
		UniqueIDs: []nfoUniqueID{
			{Type: "tmdb", Default: true, Value: strconv.Itoa(r.tmdbID)},
			{Type: "imdb", Value: d.IMDbID},
		},
		TmdbID: strconv.Itoa(r.tmdbID), IMDbID: d.IMDbID,
	}
	for _, g := range d.Genres {
		nfo.Genres = append(nfo.Genres, g.Name)
	}
	for _, c := range d.Credits.Crew {
		if c.Job == "Director" {
			nfo.Directors = append(nfo.Directors, c.Name)
		}
	}
	for _, cc := range d.Credits.Cast {
		if cc.Name == "" {
			continue
		}
		nfo.Actors = append(nfo.Actors, nfoActor{Name: cc.Name, Role: cc.Character})
	}
	for _, pc := range d.ProductionCompanies {
		nfo.Studios = append(nfo.Studios, pc.Name)
	}
	names := videoNFONames(t.Videos)
	for i, name := range names {
		if r.s.rep.stopped() || r.st.Gone {
			return
		}
		r.s.rep.sub("影片 NFO", i, len(names), name)
		// 与视频同名的 NFO 放在视频旁边；没有视频时兜底的 movie.nfo 放标题目录
		dest := t.Dir
		if i < len(t.Videos) {
			dest = t.Videos[i].Dir
		}
		if r.skip(dest, name) {
			continue
		}
		nfo.Fileinfo = nil
		how := ""
		if i < len(t.Videos) {
			if p := r.probe(t.Videos[i]); p != nil {
				nfo.Fileinfo = nfoFileInfoFrom(p)
				how = "（含轨道信息）"
			}
		}
		if b, err := marshalNFO(nfo); err == nil {
			r.put(dest, name, b, how)
		}
	}
}

func (r *titleRun) tvNFO(body []byte) {
	t := r.t
	var d struct {
		Name         string  `json:"name"`
		OriginalName string  `json:"original_name"`
		Overview     string  `json:"overview"`
		VoteAverage  float64 `json:"vote_average"`
		FirstAirDate string  `json:"first_air_date"`
		Genres       []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Networks []struct {
			Name string `json:"name"`
		} `json:"networks"`
		Credits struct {
			Cast []struct {
				Name      string `json:"name"`
				Character string `json:"character"`
			} `json:"cast"`
		} `json:"credits"`
		Seasons []struct {
			SeasonNumber int    `json:"season_number"`
			Name         string `json:"name"`
			Overview     string `json:"overview"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
	}
	if json.Unmarshal(body, &d) != nil {
		r.fail("详情解析失败")
		return
	}
	nfo := nfoTVShow{
		Title: d.Name, OriginalTitle: d.OriginalName, Plot: d.Overview,
		Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
		Year:    dateYear(d.FirstAirDate), Premiered: d.FirstAirDate,
		UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(r.tmdbID)}},
		TmdbID:    strconv.Itoa(r.tmdbID),
	}
	for _, g := range d.Genres {
		nfo.Genres = append(nfo.Genres, g.Name)
	}
	for _, nw := range d.Networks {
		nfo.Studios = append(nfo.Studios, nw.Name)
	}
	// 演员取 credits.cast。此前取的是 created_by（主创）当演员写，剧的演员表基本是空的
	for _, cc := range d.Credits.Cast {
		if cc.Name != "" {
			nfo.Actors = append(nfo.Actors, nfoActor{Name: cc.Name, Role: cc.Character})
		}
	}
	r.s.rep.sub("剧 / 季 NFO", 0, 0, "tvshow.nfo")
	if !r.skip(t.Dir, "tvshow.nfo") {
		if b, err := marshalNFO(nfo); err == nil {
			r.put(t.Dir, "tvshow.nfo", b, "")
		}
	}
	// 季级 NFO：季名 / 简介 / 首播都在详情的 seasons 里，不用再按季请求
	seasonDirs := scrapeSeasonDirs(t.Videos, t.Dir)
	for _, sn := range d.Seasons {
		dest, ok := seasonDirs[sn.SeasonNumber]
		if !ok || r.skip(dest, "season.nfo") {
			continue
		}
		b, err := marshalNFO(nfoSeason{
			Title: sn.Name, Plot: sn.Overview, Premiered: sn.AirDate,
			Year: dateYear(sn.AirDate), SeasonNumber: sn.SeasonNumber,
		})
		if err == nil {
			r.put(dest, "season.nfo", b, fmt.Sprintf("（第 %d 季）", sn.SeasonNumber))
		}
	}
	// 集级 NFO：每集与 STRM 同基名（xxx.strm → xxx.nfo）落在集文件旁，
	// TMDB 集信息（标题/首播/简介/剧照）+ 开了探测时的轨道 streamdetails。
	// 解析不出集号的集文件跳过（tvshow.nfo 与海报仍正常生成）
	for i, e := range r.eps {
		if r.s.rep.stopped() || r.st.Gone {
			return
		}
		name := e.v.Name + ".nfo"
		r.s.rep.sub("集 NFO", i, len(r.eps), name)
		if r.skip(e.v.Dir, name) {
			continue
		}
		ep := r.season(e.season)[e.ep]
		epNFO := nfoEpisode{
			Season: e.season, Episode: e.ep, Title: ep.Name, Aired: ep.AirDate, Plot: ep.Overview,
			UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(r.tmdbID)}},
		}
		if ep.VoteAverage > 0 {
			epNFO.Ratings = []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: ep.VoteAverage}}
		}
		if ep.StillPath != "" {
			epNFO.Thumb = tmdbImageBase() + "/t/p/w500" + ep.StillPath
		}
		how := fmt.Sprintf("（S%02dE%02d %s）", e.season, e.ep, ep.Name)
		if p := r.probe(e.v); p != nil {
			epNFO.Fileinfo = nfoFileInfoFrom(p)
			how = fmt.Sprintf("（S%02dE%02d %s，含轨道信息）", e.season, e.ep, ep.Name)
		}
		if b, err := marshalNFO(epNFO); err == nil {
			r.put(e.v.Dir, name, b, how)
		}
	}
}

// ---- 图片 ----

func (r *titleRun) images(body []byte) {
	t := r.t
	var d struct {
		PosterPath   string `json:"poster_path"`
		BackdropPath string `json:"backdrop_path"`
		Seasons      []struct {
			SeasonNumber int    `json:"season_number"`
			PosterPath   string `json:"poster_path"`
		} `json:"seasons"`
		Images struct {
			Logos     []tmdbImage `json:"logos"`
			Backdrops []tmdbImage `json:"backdrops"`
		} `json:"images"`
	}
	if json.Unmarshal(body, &d) != nil {
		return
	}
	images := [][2]string{
		{d.PosterPath, "poster.jpg"},
		{d.BackdropPath, "fanart.jpg"},
		// clearlogo 只要 PNG：TMDB 的 logo 有一部分是 SVG，Emby 不认
		{pickTMDBImage(d.Images.Logos, []string{"zh", "en", ""}, ".png"), "clearlogo.png"},
		// landscape 是带片名字样的横图，只从有语言的背景里挑；
		// 无语言的背景就是 fanart 那张，挑不到宁可不写，别复制一份 fanart 充数
		{pickTMDBImage(d.Images.Backdrops, []string{"zh", "en"}, ""), "landscape.jpg"},
	}
	if t.Kind == "tv" {
		have := map[int]bool{}
		for _, e := range r.eps {
			have[e.season] = true
		}
		for _, sn := range d.Seasons {
			// 只写本地有的季：TMDB 上 20 季的剧库里只有 1 季，没必要把 20 张季海报都拉下来
			if sn.PosterPath == "" || (len(have) > 0 && !have[sn.SeasonNumber]) {
				continue
			}
			images = append(images, [2]string{sn.PosterPath, fmt.Sprintf("season%02d-poster.jpg", sn.SeasonNumber)})
		}
	}
	for i, img := range images {
		if r.s.rep.stopped() || r.st.Gone {
			return
		}
		r.s.rep.sub("图片", i, len(images), img[1])
		r.fetchPut(t.Dir, img[0], "original", img[1])
	}
	if t.Kind == "tv" {
		r.episodeStills()
	}
}

// episodeStills 集剧照：xxx.strm → xxx-thumb.jpg 落在集文件旁，Emby 按这个名字配对成集缩略图。
//
// 占位剧照（opts.SkipSharedStills）：综艺在 TMDB 上常见几十集挂同一张图，或者每集各传一份
// 内容相同的图。两道判定都**只在同一季内**比 —— 不同季共用一张定妆照不稀奇，
// 而一季里三集以上撞图基本就是占位：
//  1. TMDB 同一季里 still_path 被 ≥ sharedStillMin 集共用 → 这些集连下载都省了；
//  2. 下载下来内容 sha1 相同的 ≥ sharedStillMin 张 → 不写（路径不同、内容一样，只能下完才知道）。
//
// 参考项目（LitePan strmscrape/write_tv.go、MoviePilot themoviedb/scraper.py）都是有 still_path 就下，
// 没有这层判定；这里是自己加的
func (r *titleRun) episodeStills() {
	skipShared := r.s.opts.SkipSharedStills
	// 先判已有再请求季信息，已刮过的剧整季零请求
	var todo []*scrapeEp
	for i := range r.eps {
		e := &r.eps[i]
		if r.skip(e.v.Dir, e.v.Name+"-thumb.jpg") {
			continue
		}
		todo = append(todo, e)
	}
	if len(todo) == 0 {
		return
	}
	stillOf := func(e *scrapeEp) string { return r.season(e.season)[e.ep].StillPath }

	// 判定一：同一季 TMDB 上的 still_path 共用计数（按 TMDB 全季算，不只算本地缺图的这几集）
	if skipShared {
		shared := map[int]map[string]bool{}
		for _, e := range todo {
			if _, done := shared[e.season]; done {
				continue
			}
			count := map[string]int{}
			for _, info := range r.season(e.season) {
				if info.StillPath != "" {
					count[info.StillPath]++
				}
			}
			shared[e.season] = map[string]bool{}
			for p, n := range count {
				if n >= sharedStillMin {
					shared[e.season][p] = true
					r.logv("○ 第 %d 季有 %d 集共用同一张剧照（TMDB %s），按占位图处理，这些集不写集剧照",
						e.season, n, p)
				}
			}
		}
		for _, e := range todo {
			if shared[e.season][stillOf(e)] {
				e.placeholder = true
			}
		}
	}

	// 先把要写的都下下来，判定二要看内容；一季几百张 w780 剧照几十 MB，放得下
	type got struct {
		e    *scrapeEp
		img  *fetchedImage
		how  string
		hash string
	}
	var fetched []got
	n := 0
	for _, e := range todo {
		n++
		if r.s.rep.stopped() || r.st.Gone {
			return
		}
		name := e.v.Name + "-thumb.jpg"
		r.s.rep.sub("集剧照", n-1, len(todo), name)
		if e.placeholder {
			r.st.Placeholder++
			continue
		}
		still := stillOf(e)
		if still == "" {
			r.logv("○ %s：TMDB 上这一集没有剧照", name)
			continue
		}
		img, reused, took := r.fetchImage(still, "w780")
		if img.err != nil {
			r.fail("拉图失败 %s ← %s：%v", name, img.url, img.err)
			continue
		}
		how := fmt.Sprintf(" ← %s（%s，%s）", img.url, humanBytes(len(img.data)), took.Round(100*time.Millisecond))
		if reused {
			r.st.Reused++
			how = fmt.Sprintf(" ← 复用本次已下载的 %s", img.url)
		}
		fetched = append(fetched, got{e: e, img: img, how: how, hash: fmt.Sprintf("%x", sha1.Sum(img.data))})
	}

	// 判定二：同一季内容相同的
	dup := map[string]bool{} // 季号 + hash
	if skipShared {
		count := map[string]int{}
		for _, g := range fetched {
			count[fmt.Sprintf("%d/%s", g.e.season, g.hash)]++
		}
		logged := map[string]bool{}
		for _, g := range fetched {
			k := fmt.Sprintf("%d/%s", g.e.season, g.hash)
			if count[k] >= sharedStillMin {
				dup[k] = true
				if !logged[k] {
					logged[k] = true
					r.logv("○ 第 %d 季有 %d 集的剧照内容完全相同（如 %s），按占位图处理，不写",
						g.e.season, count[k], g.img.url)
				}
			}
		}
	}
	for _, g := range fetched {
		if r.st.Gone {
			return
		}
		if dup[fmt.Sprintf("%d/%s", g.e.season, g.hash)] {
			r.st.Placeholder++
			continue
		}
		r.put(g.e.v.Dir, g.e.v.Name+"-thumb.jpg", g.img.data, g.how)
	}
	r.s.rep.sub("集剧照", len(todo), len(todo), "")
}

// humanBytes 1234567 → 1.2MB
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
