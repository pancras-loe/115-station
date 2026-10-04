package api

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// scrapeImgWorkers 一个片目内同时拉几张图。
// 此前逐张串行，一部两百集的综艺光集剧照就要几分钟。图床是 TMDB 的 CDN（或用户配的反代），
// 不碰 115，没有风控顾虑；参考项目都没对图床限速（qmediasync 是 5 个片目并行、片目内串行）。
// 不开更多：走代理时一窝蜂打出去容易被限流，反而更慢（imgcache.go 同一个考虑，上限 6）。
// 只在片目内并行，片目之间仍一个一个来：事后收拾（scrapeCompensate）与进度都按片目算
const scrapeImgWorkers = 4

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
	imgs   map[string]*fetchedImage
	imgsMu sync.Mutex // 片目内并发拉图时护着 imgs
	// fetch 拉图的实现（测试换成桩）；会被并发调用
	fetch func(imgPath, size string) ([]byte, string, error)
}

type fetchedImage struct {
	data []byte
	url  string
	err  error
}

func newScrapeSession(tc *TmdbClient, opts fileScrapeOpts, w metaWriter, rep scrapeReporter) *scrapeSession {
	return &scrapeSession{tc: tc, opts: opts, w: w, rep: rep, imgs: map[string]*fetchedImage{},
		fetch: tmdbFetchImageSized}
}

// titleScrapeStat 一个片目的账单
type titleScrapeStat struct {
	Wrote       []string // 写入的文件名（汇总日志用）
	written     []string // 写入的本地绝对路径（片目被移走时回收，见 scrapeCompensate）
	Skipped     int      // 已存在、按「只补缺失」跳过
	Reused      int      // 复用本次任务已下载的图
	Placeholder int      // 判为占位剧照、没写的集
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
	end         int  // 双集文件的结束集号，单集为 0（剧照只取开始那集）
	placeholder bool // 占位剧照：不写 -thumb.jpg
}

// scrapeTitleMeta 刮一个片目，返回账单
func (s *scrapeSession) scrapeTitleMeta(t scrapeTitle) *titleScrapeStat {
	r := &titleRun{s: s, t: t, st: &titleScrapeStat{}, tmdbID: t.TmdbID, seasons: map[int]map[int]tmdbEpisodeInfo{}}
	if t.Kind == "tv" {
		for _, v := range t.Videos {
			if season, ep, end := scrapeEpisodeSpan(v.Name); ep > 0 {
				r.eps = append(r.eps, scrapeEp{v: v, season: season, ep: ep, end: end})
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

// movieArtNames 电影片目级的几张图。多版本时改按视频名写（<视频名>-poster.jpg，见 multiVersion）
var movieArtNames = map[string]bool{"poster.jpg": true, "fanart.jpg": true, "clearlogo.png": true, "landscape.jpg": true}

// multiVersion 电影片目里有两个及以上视频。
// 视频名不以目录名开头时（我们的重命名模板正是如此），Emby 把同目录的几个视频当成几部独立的电影，
// 目录级的 poster.jpg / fanart.jpg 不知道归谁、一律不用，只认按视频名配对的 <视频名>-poster.jpg。
// 2026-09-30《夏洛特烦恼》两个版本现场：NFO 按视频名写所以生效，图全是目录级的，Emby 里一张都没有
func (r *titleRun) multiVersion() bool {
	return r.t.Kind != "tv" && len(r.t.Videos) >= 2
}

// skipPerVideo 电影的片目级图：Emby 已按视频名另存了一份（<视频名>-poster.jpg）就算有。
// 多版本电影 Emby 存图用这个名字并删掉我们的 poster.jpg（见 perVideoImage），
// 不认的话每次刮削补一张、Emby 刷新删一张，来回折腾；
// 多版本变回单版本（洗版删掉一个）时，留下那个视频的按名图同样算数，不必再下一张目录级的
func (r *titleRun) skipPerVideo(name string) bool {
	if r.t.Kind == "tv" || !movieArtNames[name] {
		return false
	}
	kind := strings.TrimSuffix(name, filepath.Ext(name))
	for _, v := range r.t.Videos {
		for _, ext := range []string{".jpg", ".png"} {
			if r.skip(v.Dir, v.Name+"-"+kind+ext) {
				return true
			}
		}
	}
	return false
}

// localArt 多版本要按视频名补的图，本地有没有现成的一份可以直接拿来：
// 目录级的那张（单版本时刮的，后来又进了一个版本），或别的版本已有的按名图（Emby 自己存的也算）。
// 有就不去图床再下一遍。强制覆盖时不用：用户要的就是重新拉
func (r *titleRun) localArt(name string) (data []byte, from string) {
	if r.s.opts.Force {
		return nil, ""
	}
	kind, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	type cand struct{ dir, name string }
	cands := []cand{{r.t.Dir.Local, name}}
	for _, v := range r.t.Videos {
		cands = append(cands, cand{v.Dir.Local, v.Name + "-" + kind + ext})
	}
	for _, c := range cands {
		if c.dir == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(c.dir, c.name)); err == nil && len(b) > 0 {
			return b, c.name
		}
	}
	return nil, ""
}

// retireDirArt 多版本的每个视频都有了自己的那张之后，目录级的同一张就没用了（Emby 不认，
// 还会把它当成「已刮过」挡住只补缺失）。交给 writer 收掉：它知道哪些是网盘镜像、不能删
func (r *titleRun) retireDirArt(name string) {
	rt, ok := r.s.w.(metaRetirer)
	if !ok || r.t.Dir.Local == "" {
		return
	}
	kind, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	for _, v := range r.t.Videos {
		if v.Dir.Local == "" || !localMetaExists(v.Dir.Local, v.Name+"-"+kind+ext) {
			return
		}
	}
	if rt.retire(r.t.Dir, name) {
		r.logv("✂ %s：各版本都有按视频名的一份了，收掉目录级的这张 → %s", name, r.t.Dir.Local)
	}
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

// imgReq 要拉的一张图（TMDB 路径 + 尺寸档）
type imgReq struct{ path, size string }

func (q imgReq) key() string { return q.size + q.path }

// imgGot 一张图的拉取结果。img 为 nil 表示任务被停、没去拉
type imgGot struct {
	img    *fetchedImage
	reused bool
	took   time.Duration
}

// fetchImages 并发拉一批图（至多 scrapeImgWorkers 张同时），结果与 reqs 一一对应。
// 同一次任务里同尺寸同路径只下一次：本任务早先下过的直接复用，这一批里重复的只下第一张。
// 只管下载，不写文件、不记错 —— 写入与日志由调用方按原顺序串行做，writer 与账单都不是并发安全的，
// 日志顺序也跟着稳定
func (r *titleRun) fetchImages(phase string, reqs []imgReq, labels []string) []imgGot {
	out := make([]imgGot, len(reqs))
	first := map[string]int{} // key → 这一批里第一次出现的下标
	var jobs []int
	var dups [][2]int // {重复的下标, 第一次出现的下标}
	r.s.imgsMu.Lock()
	for i, q := range reqs {
		if img, ok := r.s.imgs[q.key()]; ok {
			out[i] = imgGot{img: img, reused: true}
			continue
		}
		if j, ok := first[q.key()]; ok {
			dups = append(dups, [2]int{i, j})
			continue
		}
		first[q.key()] = i
		jobs = append(jobs, i)
	}
	r.s.imgsMu.Unlock()

	done := len(reqs) - len(jobs)
	var progMu sync.Mutex
	if len(jobs) > 0 {
		r.s.rep.sub(phase, done, len(reqs), "")
	}
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(scrapeImgWorkers, len(jobs)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				if r.s.rep.stopped() {
					continue // 排空队列，不再发请求
				}
				q := reqs[i]
				start := time.Now()
				data, url, err := r.s.fetch(q.path, q.size)
				if imgFetchTimedOut(err) && !r.s.rep.stopped() {
					// 图床偶尔整批卡住几秒（2026-10-04 现场：一千多张剧照里 4 张超时，任务就成了「部分失败」），超时的再试一次
					data, url, err = r.s.fetch(q.path, q.size)
				}
				img := &fetchedImage{data: data, url: url, err: err}
				if err == nil {
					// 只缓存成功的：失败多半是图床临时不通，下一个片目遇到同一张图该再试
					r.s.imgsMu.Lock()
					r.s.imgs[q.key()] = img
					r.s.imgsMu.Unlock()
				}
				out[i] = imgGot{img: img, took: time.Since(start)}
				progMu.Lock()
				done++
				r.s.rep.sub(phase, done, len(reqs), labels[i])
				progMu.Unlock()
			}
		}()
	}
	for _, i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()

	for _, d := range dups {
		if g := out[d[1]]; g.img != nil {
			// 第一张拉失败的，重复的那几张跟着算失败（各记一处错），不算复用
			out[d[0]] = imgGot{img: g.img, reused: g.img.err == nil}
		}
	}
	return out
}

// settleImage 一张图拉完之后：失败记一处错误；成功返回日志里「怎么来的」那一截
func (r *titleRun) settleImage(name string, g imgGot) (how string, ok bool) {
	switch {
	case g.img == nil:
		return "", false
	case g.img.err != nil:
		r.fail("拉图失败 %s ← %s：%v", name, g.img.url, g.img.err)
		return "", false
	case g.reused:
		r.st.Reused++
		return fmt.Sprintf(" ← 复用本次已下载的 %s", g.img.url), true
	}
	return fmt.Sprintf(" ← %s（%s，%s）", g.img.url, humanBytes(len(g.img.data)), g.took.Round(100*time.Millisecond)), true
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
			Cast []tmdbCastMember `json:"cast"`
			Crew []tmdbCrewMember `json:"crew"`
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
	nfo.Directors = nfoCrewNames(d.Credits.Crew, crewIsDirector)
	nfo.Writers = nfoCrewNames(d.Credits.Crew, crewIsWriter)
	nfo.Actors = nfoActorsOf(d.Credits.Cast)
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
		if b, err := marshalNFO(nfo); err == nil {
			r.put(dest, name, b, "")
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
			Cast []tmdbCastMember `json:"cast"`
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
	nfo.Actors = nfoActorsOf(d.Credits.Cast)
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
	// TMDB 集信息（标题/首播/简介/剧照）。
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
		var nfos []nfoEpisode
		var titles []string
		for n := e.ep; n == e.ep || n <= e.end; n++ {
			ep := r.season(e.season)[n]
			epNFO := nfoEpisode{
				Season: e.season, Episode: n, Title: ep.Name, Aired: ep.AirDate, Plot: ep.Overview,
				UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(r.tmdbID)}},
			}
			if ep.VoteAverage > 0 {
				epNFO.Ratings = []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: ep.VoteAverage}}
			}
			if ep.StillPath != "" {
				epNFO.Thumb = tmdbImageBase() + "/t/p/w500" + ep.StillPath
			}
			nfos = append(nfos, epNFO)
			if ep.Name != "" {
				titles = append(titles, ep.Name)
			}
		}
		tag := fmt.Sprintf("E%02d", e.ep)
		if e.end > e.ep {
			tag += fmt.Sprintf("-E%02d", e.end)
		}
		how := fmt.Sprintf("（S%02d%s %s）", e.season, tag, strings.Join(titles, " / "))
		if b, err := marshalEpisodeNFO(nfos); err == nil {
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
	// 先问 writer 要不要（已有的不拉），要的一起并发拉，再按原顺序写
	var reqs []imgReq
	var names []string
	var dests []metaDest
	multi := r.multiVersion()
	var retire []string
	for _, img := range images {
		if img[0] == "" {
			r.logv("○ %s：TMDB 上没有这张图", img[1])
			continue
		}
		if multi && movieArtNames[img[1]] {
			// 多版本：每个视频一张 <视频名>-poster.jpg。本地已有的（目录级那张、别的版本那张）
			// 直接拷过去，都没有才下载 —— 几个版本要下的是同一张图，fetchImages 按路径合并，只下一次
			kind, ext := strings.TrimSuffix(img[1], filepath.Ext(img[1])), filepath.Ext(img[1])
			for _, v := range t.Videos {
				name := v.Name + "-" + kind + ext
				if r.skip(v.Dir, name) {
					continue
				}
				if data, from := r.localArt(img[1]); data != nil {
					if r.put(v.Dir, name, data, " ← 沿用本地 "+from+"（不重新下载）") {
						r.st.Reused++
					}
					continue
				}
				reqs = append(reqs, imgReq{path: img[0], size: "original"})
				names = append(names, name)
				dests = append(dests, v.Dir)
			}
			retire = append(retire, img[1])
			continue
		}
		if r.skip(t.Dir, img[1]) || r.skipPerVideo(img[1]) {
			continue
		}
		reqs = append(reqs, imgReq{path: img[0], size: "original"})
		names = append(names, img[1])
		dests = append(dests, t.Dir)
	}
	got := r.fetchImages("图片", reqs, names)
	for i, g := range got {
		if r.s.rep.stopped() || r.st.Gone {
			return
		}
		if how, ok := r.settleImage(names[i], g); ok {
			r.put(dests[i], names[i], g.img.data, how)
		}
	}
	if r.s.rep.stopped() || r.st.Gone {
		return
	}
	for _, name := range retire {
		r.retireDirArt(name)
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

	// 先把要写的都下下来（并发），判定二要看内容；一季几百张 w780 剧照几十 MB，放得下
	type got struct {
		e    *scrapeEp
		img  *fetchedImage
		how  string
		hash string
	}
	var want []*scrapeEp
	var reqs []imgReq
	var names []string
	for _, e := range todo {
		name := e.v.Name + "-thumb.jpg"
		if e.placeholder {
			r.st.Placeholder++
			continue
		}
		still := stillOf(e)
		if still == "" {
			r.logv("○ %s：TMDB 上这一集没有剧照", name)
			continue
		}
		want = append(want, e)
		reqs = append(reqs, imgReq{path: still, size: "w780"})
		names = append(names, name)
	}
	res := r.fetchImages("集剧照", reqs, names)
	if r.s.rep.stopped() || r.st.Gone {
		return
	}
	var fetched []got
	for i, g := range res {
		how, ok := r.settleImage(names[i], g)
		if !ok {
			continue
		}
		fetched = append(fetched, got{e: want[i], img: g.img, how: how, hash: fmt.Sprintf("%x", sha1.Sum(g.img.data))})
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

// imgFetchTimedOut 拉图超时（连接 / 等响应头）。只有超时值得当场重试：404 之类再拉也一样
func imgFetchTimedOut(err error) bool {
	var ne net.Error
	return err != nil && errors.As(err, &ne) && ne.Timeout()
}
