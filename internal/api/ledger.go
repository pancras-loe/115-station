package api

import (
	"115-station/internal/model"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ledgerTitleEntry 从台账聚合出的一个标题条目
type ledgerTitleEntry struct {
	Key       string // 标题目录的相对路径（含库名前缀，详情查询用）
	Title     string
	Year      string
	TmdbID    int
	MediaType string
	Category  string // 分类目录（库内相对路径，如 电影、电视剧/日番）
	// LibName Key 的第一段库名；老台账不带库名的两段式路径为空。换算网盘相对路径时剥掉它
	LibName string
	Videos  int // 台账里的视频数（海报墙的卡片显示）
	// LastAt 片目里最近一个视频首次入账的时间（海报墙「最近入库」、媒体信息补全的先后）。
	// 取 CreatedAt 不取 UpdatedAt：全量同步对每个文件都 upsert 一遍、刷新 updated_at，
	// 拿它排序的话手动全量一次，所有片目的「入库时间」都变成那次全量的时刻
	LastAt time.Time
}

// dirKey 片目所在目录（含库名前缀）：普通片目就是 Key，平铺片目是它所在的分类目录
func (e *ledgerTitleEntry) dirKey() string { return titleDirKey(e.Key) }

// solo 平铺片目（一个视频一部）
func (e *ledgerTitleEntry) solo() bool { return isSoloTitleKey(e.Key) }

// ---- 平铺片目 ----
//
// 有的用户不给每部电影单独建目录，视频直接放在分类目录里（2026-10-12 用户反馈：
// 影视/LGNB电影/LGNB中国香港电影/*.mkv）。Emby 把这样的每个文件当成一部电影，
// 海报墙此前要求「分类/标题目录/视频」，这些一部都不认，两边数量差出一大截。
//
// 现在电影分类目录里直接放着的视频，每个算一部：片目 key 就是这个 STRM 的相对路径
// （以 .strm 结尾，和目录形态的 key 撞不上）。这样的片目没有自己的目录，产物一律按视频名
// 写在它旁边（<视频名>.nfo、<视频名>-poster.jpg、<视频名>-fanart.jpg），这也是 Emby 在
// 一个目录放多部电影时认的写法；目录里的 poster.jpg / movie.nfo 是整个分类目录的，不算这一部的，
// 刮削也绝不写、不收它们。只认电影：剧集平铺 Emby 自己也认不成剧。
// 整理不往平铺目录里放（落盘仍是「分类/标题目录」），这里只管把已有的认出来。

// isSoloTitleKey key（或路径最后一段）是平铺片目
func isSoloTitleKey(key string) bool { return strings.HasSuffix(strings.ToLower(key), ".strm") }

// titleDirKey 片目 key → 所在目录：平铺片目取上一层，其余原样
func titleDirKey(key string) string {
	if isSoloTitleKey(key) {
		if d := path.Dir(key); d != "." {
			return d
		}
		return ""
	}
	return key
}

// soloTitleStem 平铺片目的视频名（STRM 去掉 .strm）
func soloTitleStem(key string) string {
	b := path.Base(key)
	return b[:len(b)-len(".strm")]
}

// titleKeyName 片目 key 的名字部分：目录名，或平铺片目的视频名
func titleKeyName(key string) string {
	if isSoloTitleKey(key) {
		return soloTitleStem(key)
	}
	return path.Base(key)
}

// soloAllowed 分类目录里直接放着的这个视频能不能单独算一部：电影分类可以，剧集分类不行；
// 电影 / 剧集同名的分类按文件名判断，带集号的不算
func soloAllowed(mediaType, strmName string) bool {
	switch mediaType {
	case "movie":
		return true
	case "tv":
		return false
	}
	return parseFileName(soloTitleStem(strmName)).Episode == 0
}

// soloTitleParse 平铺片目的片名 / 年份 / TMDB 编号（从视频名解析）
func soloTitleParse(stem string) (title, year string, tmdb int) {
	p := parseFileName(stem)
	title, year, tmdb = p.Title, p.Year, p.TmdbID
	if title == "" {
		title = stem
	}
	return
}

// 台账扫描缓存：刮削共享同一份结果（30 秒 TTL），
// 避免每次请求都全表扫描 SyncedFile。缓存条目视为只读，调用方不得修改。
var (
	ledgerTitlesMu    sync.Mutex
	ledgerTitlesCache map[string]*ledgerTitleEntry
	ledgerTitlesAt    time.Time
)

// forgetLedgerTitles 丢掉台账聚合缓存（片目认法变了：分类规则、库的整理方式）
func forgetLedgerTitles() {
	ledgerTitlesMu.Lock()
	ledgerTitlesCache = nil
	ledgerTitlesMu.Unlock()
}

func scanLedgerTitlesCached() map[string]*ledgerTitleEntry {
	ledgerTitlesMu.Lock()
	defer ledgerTitlesMu.Unlock()
	if ledgerTitlesCache != nil && time.Since(ledgerTitlesAt) < 30*time.Second {
		return ledgerTitlesCache
	}
	ledgerTitlesCache = scanLedgerTitles()
	ledgerTitlesAt = time.Now()
	return ledgerTitlesCache
}

// ---- 库内目录布局：分类目录的下一层是标题目录 ----
//
// 整理落盘是 库名/<分类名>/<标题目录>/…（organize.go 的 categoryDir + 重命名模板第一段），
// 分类名就是二级分类 YAML 里写的名字，可以多级（电视剧/日番），也可以平铺（动漫番剧、综艺）。
// 所以「哪一层是标题目录」只能按当前的分类规则算，不能按深度写死 ——
// 此前写死成 库名/电影|剧集/分类/标题，平铺配置（电影/*、动漫番剧/*）下
// 片目全认错：电影的标题目录被当成分类、文件被当成标题目录，动漫番剧 / 综艺整类被跳过。
//
// 只认当前配置：改过分类策略之后，旧分类目录下的片不再被当成片目（与网盘文件页口径一致）。

// libCategoryLayout 片目认在哪一层：
//   - cats：分类目录（库内相对路径）→ 媒体类型（movie / tv；两边都有同名分类时为空）。本站整理的库按它认，
//     不看规则指向哪个库（规则改过库的老片还留在原库里，照样要认得出）；
//   - self：自己整理的库（libopts.go）：库名 → 片目在库下第几层。这些库不按分类认，按用户声明的层级认。
type libCategoryLayout struct {
	cats map[string]string
	self map[string]int
	// libs 媒体库表里的库（libstore.go，2026-10-20）：本地目录可以多级，按整段最长前缀认库，
	// 选了分类方案的按那套方案的分类目录认片目，没选的按层级（Depth）认。认不出库的路径退回上面两项（老台账）
	libs []layoutLib
}

// layoutLib 一个库在片目认定上的样子
type layoutLib struct {
	local string            // 本地目录（媒体根下，归一过）
	cats  map[string]string // 分类方案的分类目录（库内相对路径）→ 媒体类型；nil = 不分类
	depth int               // 不分类时片目在第几层（≥1）
	colls map[string]bool   // 合集目录（库内相对路径，model.Library.Collections）
}

// catLayout 只有分类表的布局（测试与不涉及自己整理的库的地方用）
func catLayout(cats map[string]string) libCategoryLayout {
	return libCategoryLayout{cats: cats}
}

// catType 分类目录的媒体类型；不是分类目录 ok=false
// 写了系列字段的分类下面的「X系列」也算分类目录（catLookup）
func (l libCategoryLayout) catType(rel string) (string, bool) {
	return catLookup(l.cats, rel)
}

// selfDepth 自己整理的库的片目层级；不是自己整理的库 ok=false
func (l libCategoryLayout) selfDepth(lib string) (int, bool) {
	d, ok := l.self[lib]
	return d, ok
}

// loadLibCategoryLayout 按当前分类规则算出所有分类目录，再带上自己整理的库。
// 直接读表不走 classifyRules 的缓存：调用方（台账扫描）自己有 30 秒缓存。零 115 请求
func loadLibCategoryLayout() libCategoryLayout {
	var rules []model.CategoryRule
	if model.DB != nil {
		model.DB.Where("media_type IN ?", []string{"movie", "tv"}).Find(&rules)
	}
	l := buildLibCategoryLayout(rules)
	l.self = selfLibDepths(settingValueCompat("full"), loadLibOpts())
	// 按库认（libstore.go）：每个库用自己那套方案的分类目录
	byScheme := map[uint][]model.CategoryRule{}
	for _, r := range rules {
		byScheme[r.SchemeID] = append(byScheme[r.SchemeID], r)
	}
	l.libs = layoutLibsOf(effectiveLibraries(nil), byScheme)
	return l
}

// layoutLibsOf 媒体库 → 片目认定（纯函数）。选了方案却一条规则都没有（方案被删了）按不分类认
func layoutLibsOf(libs []model.Library, byScheme map[uint][]model.CategoryRule) []layoutLib {
	out := make([]layoutLib, 0, len(libs))
	for _, lib := range libs {
		ll := layoutLib{local: normLibLocal(lib.Local), depth: lib.Depth}
		if ll.local == "" {
			continue
		}
		if ll.depth < 1 {
			ll.depth = 1
		}
		if lib.SchemeID != 0 && len(byScheme[lib.SchemeID]) > 0 {
			ll.cats = buildLibCategoryLayout(byScheme[lib.SchemeID]).cats
		}
		for _, c := range libCollections(lib) {
			if ll.colls == nil {
				ll.colls = map[string]bool{}
			}
			ll.colls[c] = true
		}
		out = append(out, ll)
	}
	return out
}

// libOf 本地相对路径落在哪个库（整段最长前缀）。返回库、库目录占几段
func (l libCategoryLayout) libOf(relPath string) (layoutLib, int, bool) {
	rel := normLibLocal(relPath)
	best, bestN := layoutLib{}, -1
	for _, lib := range l.libs {
		if rel == lib.local || strings.HasPrefix(rel, lib.local+"/") {
			if n := strings.Count(lib.local, "/") + 1; n > bestN {
				best, bestN = lib, n
			}
		}
	}
	return best, bestN, bestN > 0
}

// buildLibCategoryLayout 纯函数部分：规则 → 分类目录表。
// 某个媒体类型一条规则都没有时，整理会落到 电影/未分类 或 剧集/未分类（classifyMedia 的兜底）
func buildLibCategoryLayout(rules []model.CategoryRule) libCategoryLayout {
	out := map[string]string{}
	has := map[string]bool{}
	add := func(dir, mediaType string) {
		if dir = libSubPath(dir); dir == "" {
			return
		}
		if prev, ok := out[dir]; ok && prev != mediaType {
			out[dir] = "" // movie 与 tv 都有这个分类名：类型要按文件判断
			return
		}
		out[dir] = mediaType
	}
	for _, r := range rules {
		add(r.Name, r.MediaType)
		has[r.MediaType] = true
		if catRuleHasSeries(&r) {
			// 系列子目录是整理时按命中的值现起的名字，列不全：记一个标记，查的时候认「分类/X系列」（catLookup）
			k := catSeriesMarkKey(r.Name)
			if prev, ok := out[k]; ok && prev != r.MediaType {
				out[k] = ""
			} else {
				out[k] = r.MediaType
			}
		}
	}
	for _, mt := range []string{"movie", "tv"} {
		if !has[mt] {
			add(libSubPath(mediaTypeCategory(mt), "未分类"), mt)
		}
	}
	return libCategoryLayout{cats: out}
}

// titleOf 台账相对路径（含文件名）落在哪个标题目录下。
// 路径首段一般是库名；老台账有不带库名的，先按带库名算，对不上再按不带算。
// 分类有多级且互相嵌套时（电视剧 与 电视剧/日番 都是分类）取最长匹配。
// 返回标题目录的相对路径（台账口径，含库名前缀）、标题目录名、分类目录与其媒体类型
//
// 视频直接放在电影分类目录里（平铺，见 isSoloTitleKey）时，这个视频自己就是一部：
// key 是它的相对路径，titleDir 是去掉 .strm 的视频名
//
// 首段是自己整理的库时只按它声明的层级认（selfTitleOf），不再套分类表，也不退回「不带库名」那一档
func (l libCategoryLayout) titleOf(relPath string) (key, titleDir, category, mediaType string, ok bool) {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	// 媒体库表里的库：按它自己的方案（或层级）认，不再套全局的分类表、不退回「不带库名」
	if lib, n, hit := l.libOf(relPath); hit {
		if len(segs) <= n {
			return "", "", "", "", false
		}
		if lib.cats == nil {
			key, titleDir, category, mediaType, ok = selfTitleOfAt(segs, n, lib.depth)
		} else {
			key, titleDir, category, mediaType, ok = catTitleOf(segs, []int{n}, func(rel string) (string, bool) { return catLookup(lib.cats, rel) })
		}
		if ok && len(lib.colls) > 0 {
			return descendCollections(segs, n, lib.colls, key, titleDir, category, mediaType)
		}
		return key, titleDir, category, mediaType, ok
	}
	if depth, self := l.selfDepth(segs[0]); self && len(segs) > 1 {
		return selfTitleOf(segs, depth)
	}
	return catTitleOf(segs, []int{1, 0}, l.catType)
}

// descendCollections 按层级 / 分类认出的片目是用户标记的合集目录时，往下再认一层（可以套好几层）：
// 合集里的子目录各算一部（下面的 Season 目录照常归它），直接放在合集里的视频各算一部平铺片目（带集号的不算，同 selfTitleOfAt），
// 合集里的其他文件（以前当成一部刮出来的 NFO / 图片）不属于任何片目。
// 子目录的媒体类型不知道（返回空，scanLedgerTitles 按文件判），分类沿用合集所在的分类。
// 思路来自 Jellyfin 的混放目录（一个目录里认出好几部就各算各的）与 [boxset] 标记，只是改成用户标记、不推断
func descendCollections(segs []string, libSegs int, colls map[string]bool, key, titleDir, category, mediaType string) (string, string, string, string, bool) {
	for !isSoloTitleKey(key) {
		ks := strings.Count(key, "/") + 1
		if ks <= libSegs || !colls[strings.Join(segs[libSegs:ks], "/")] {
			break
		}
		switch rest := len(segs) - ks; {
		case rest >= 2:
			key, titleDir, mediaType = strings.Join(segs[:ks+1], "/"), segs[ks], ""
		case rest == 1 && isSoloTitleKey(segs[ks]) && soloAllowed("", segs[ks]):
			return strings.Join(segs, "/"), soloTitleStem(segs[ks]), category, "movie", true
		default:
			return "", "", "", "", false
		}
	}
	return key, titleDir, category, mediaType, true
}

// isCollectionKey 这个片目 key（含库目录）是不是标记过的合集目录
func (l libCategoryLayout) isCollectionKey(key string) bool {
	lib, n, hit := l.libOf(key)
	if !hit || len(lib.colls) == 0 {
		return false
	}
	segs := strings.Split(strings.Trim(key, "/"), "/")
	return len(segs) > n && lib.colls[strings.Join(segs[n:], "/")]
}

// collectionOf 片目所在的合集目录（key 口径，含库目录）；不在合集里返回空
func (l libCategoryLayout) collectionOf(key string) string {
	if dir := path.Dir(strings.Trim(key, "/")); dir != "." && l.isCollectionKey(dir) {
		return dir
	}
	return ""
}

// catTitleOf 按分类目录认片目：offs 是分类目录从第几段开始（库目录占的段数；老台账不带库名时是 0）
func catTitleOf(segs []string, offs []int, catType func(string) (string, bool)) (key, titleDir, category, mediaType string, ok bool) {
	last := segs[len(segs)-1]
	for _, off := range offs {
		// 平铺：分类段一直到文件的上一层，比任何「分类/标题目录」都长，先看它
		if n := len(segs) - 1 - off; n >= 1 && isSoloTitleKey(last) {
			cat := strings.Join(segs[off:off+n], "/")
			if mt, hit := catType(cat); hit && soloAllowed(mt, last) {
				return strings.Join(segs, "/"), soloTitleStem(last), cat, "movie", true
			}
		}
		best := -1
		// 标题目录之下至少还有一个文件：分类段最多到 len-2
		for n := 1; off+n <= len(segs)-2; n++ {
			if _, hit := catType(strings.Join(segs[off:off+n], "/")); hit {
				best = n
			}
		}
		if best < 0 {
			continue
		}
		category = strings.Join(segs[off:off+best], "/")
		mt, _ := catType(category)
		return strings.Join(segs[:off+best+1], "/"), segs[off+best], category, mt, true
	}
	return "", "", "", "", false
}

// selfTitleOf 自己整理的库：segs[0] 是库名，片目在库下第 depth 层，中间各层是分类（深度 1 时没有分类）。
// 媒体类型不知道（返回空，由 scanLedgerTitles 按文件判：有一集带集号就是剧集）。
// 视频正好落在片目那一层（比如 库/欧美电影/敦刻尔克.strm）按平铺电影算一部，带集号的不算；
// 比片目层还浅的视频不属于任何片目
func selfTitleOf(segs []string, depth int) (key, titleDir, category, mediaType string, ok bool) {
	return selfTitleOfAt(segs, 1, depth)
}

// selfTitleOfAt 同 selfTitleOf，库目录占 off 段（媒体库表里的库本地目录可以多级）
func selfTitleOfAt(segs []string, off, depth int) (key, titleDir, category, mediaType string, ok bool) {
	n := len(segs) - off // 库目录之后还有几段（含文件名）
	switch {
	case n > depth:
		return strings.Join(segs[:off+depth], "/"), segs[off+depth-1], strings.Join(segs[off:off+depth-1], "/"), "", true
	case n == depth && isSoloTitleKey(segs[len(segs)-1]) && soloAllowed("", segs[len(segs)-1]):
		return strings.Join(segs, "/"), soloTitleStem(segs[len(segs)-1]), strings.Join(segs[off:off+depth-1], "/"), "movie", true
	}
	return "", "", "", "", false
}

// scanLedgerTitles 全量扫描台账，按「标题目录」聚合（库名/<分类>/<标题目录>/…）
func scanLedgerTitles() map[string]*ledgerTitleEntry {
	layout := loadLibCategoryLayout()
	var sfs []model.SyncedFile
	model.DB.Where("kind = ?", "video").Find(&sfs)
	return aggregateLedgerTitles(layout, sfs)
}

// aggregateLedgerTitles 台账视频行按片目聚合（分享 STRM 进海报墙也走它，sharestrmposter.go）
func aggregateLedgerTitles(layout libCategoryLayout, sfs []model.SyncedFile) map[string]*ledgerTitleEntry {
	out := map[string]*ledgerTitleEntry{}
	// 同名分类两边都有时按文件判断：片目里有一集带集号就是剧集
	undecided := map[string]bool{}
	for _, sf := range sfs {
		key, titleDir, category, mediaType, ok := layout.titleOf(sf.RelPath)
		if !ok {
			continue
		}
		e, seen := out[key]
		if !seen {
			title, year, tmdb := parseTitleDir(titleDir)
			if isSoloTitleKey(key) {
				// 平铺的是视频文件名（常带画质、压制组），按文件名解析，目录名那套会把画质串当成片名
				title, year, tmdb = soloTitleParse(titleDir)
			}
			e = &ledgerTitleEntry{
				Key: key, Title: title, Year: year, TmdbID: tmdb,
				MediaType: mediaType, Category: category,
			}
			// key = [库名/]分类/标题：段数比分类多两段说明带着库名。自己整理的库首段一定是库名（深度 1 时没有分类段）。
			// 媒体库表里的库：库名就是它的本地目录（可以多级）
			segs := strings.Split(key, "/")
			if lib, _, hit := layout.libOf(key); hit {
				e.LibName = lib.local
			} else if _, self := layout.selfDepth(segs[0]); self || len(segs) == strings.Count(category, "/")+3 {
				e.LibName = segs[0]
			}
			out[key] = e
			if mediaType == "" {
				e.MediaType = "movie"
				undecided[key] = true
			}
		}
		if undecided[key] && parseFileName(strings.TrimSuffix(path.Base(sf.RelPath), ".strm")).Episode > 0 {
			e.MediaType = "tv"
			delete(undecided, key)
		}
		e.Videos++
		if sf.CreatedAt.After(e.LastAt) {
			e.LastAt = sf.CreatedAt
		}
	}
	return out
}

var (
	reTitleDirYear    = regexp.MustCompile(`(?:19|20)\d{2}`)
	reTitleDirLetter  = regexp.MustCompile(`^[A-Z\d]-`)
	reTitleDirSepRuns = regexp.MustCompile(`([-. _])[-. _]+`)
)

// titleDirYearAt 最右侧一个「前后是分隔符或括号」的四位年份的位置，没有返回 -1。
// 边界手工判断而不写进正则：正则会把两个年份之间共用的那个分隔符算进前一个匹配，
// 「1917-2019」里的 2019 就再也匹配不上了（此前的实现正是这样，取到的是 1917）
func titleDirYearAt(s string) (int, int) {
	isSep := func(r rune) bool { return strings.ContainsRune("-. _([)]（）", r) }
	start, end := -1, -1
	for _, m := range reTitleDirYear.FindAllStringIndex(s, -1) {
		before, _ := utf8.DecodeLastRuneInString(s[:m[0]])
		after, _ := utf8.DecodeRuneInString(s[m[1]:])
		if (m[0] == 0 || isSep(before)) && (m[1] == len(s) || isSep(after)) {
			start, end = m[0], m[1]
		}
	}
	return start, end
}

// parseTitleDir 解析标题目录名 → (片名, 年份, TMDB 编号)：
//
//	"流浪地球.2019.{tmdbid=535167}"  → (流浪地球, 2019, 535167)   默认重命名模板
//	"流浪地球 (2019) [tmdbid=535167]" → (流浪地球, 2019, 535167)   Emby 风格
//	"Z-重器-2026-[tmdb=291856]"       → (重器, 2026, 291856)       首字母分组的老模板
//
// 编号标签交给识别环节同一个 takeTags 摘（[tmdbid=…] / [tmdb=…] / {tmdb-…} / {[tmdbid=…;type=tv]} 都认）。
// 此前只认 [tmdb=…]，而默认模板渲染出来的是 {tmdbid=…}：用默认模板的库，
// 刮削一个片目都找不到（它只刮带编号的），年份也不认括号
func parseTitleDir(dir string) (title, year string, tmdb int) {
	tag, rest := takeTags(dir)
	tmdb = tag.TmdbID
	// 取最右侧的年份（取最左会把片名本身是年份的 "1917-2019" 剜成 "2019"）
	if ys, ye := titleDirYearAt(rest); ys >= 0 {
		year = rest[ys:ye]
		before, after := rest[:ys], rest[ye:]
		// 包着年份的括号一起摘掉，不然片名尾巴上挂着一对空括号
		for _, p := range [][2]string{{"(", ")"}, {"[", "]"}, {"（", "）"}} {
			if strings.HasSuffix(before, p[0]) && strings.HasPrefix(after, p[1]) {
				before, after = strings.TrimSuffix(before, p[0]), strings.TrimPrefix(after, p[1])
				break
			}
		}
		rest = before + after
	}
	title = reTitleDirSepRuns.ReplaceAllString(rest, "$1") // 摘掉年份留下的 ".." / "--"
	title = reTitleDirLetter.ReplaceAllString(title, "")
	title = strings.Trim(title, "- _.[]()（）")
	if title == "" {
		title = dir
	}
	return
}
