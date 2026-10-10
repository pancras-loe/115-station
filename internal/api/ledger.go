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
	LastAt  time.Time
}

// 台账扫描缓存：刮削共享同一份结果（30 秒 TTL），
// 避免每次请求都全表扫描 SyncedFile。缓存条目视为只读，调用方不得修改。
var (
	ledgerTitlesMu    sync.Mutex
	ledgerTitlesCache map[string]*ledgerTitleEntry
	ledgerTitlesAt    time.Time
)

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

// libCategoryLayout 分类目录（库内相对路径）→ 媒体类型（movie / tv；两边都有同名分类时为空）
type libCategoryLayout map[string]string

// loadLibCategoryLayout 按当前分类规则算出所有分类目录。
// 直接读表不走 classifyRules 的缓存：调用方（台账扫描）自己有 30 秒缓存
func loadLibCategoryLayout() libCategoryLayout {
	var rules []model.CategoryRule
	if model.DB != nil {
		model.DB.Where("media_type IN ?", []string{"movie", "tv"}).Find(&rules)
	}
	return buildLibCategoryLayout(rules)
}

// buildLibCategoryLayout 纯函数部分：规则 → 分类目录表。
// 某个媒体类型一条规则都没有时，整理会落到 电影/未分类 或 剧集/未分类（classifyMedia 的兜底）
func buildLibCategoryLayout(rules []model.CategoryRule) libCategoryLayout {
	out := libCategoryLayout{}
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
	}
	for _, mt := range []string{"movie", "tv"} {
		if !has[mt] {
			add(libSubPath(mediaTypeCategory(mt), "未分类"), mt)
		}
	}
	return out
}

// titleOf 台账相对路径（含文件名）落在哪个标题目录下。
// 路径首段一般是库名；老台账有不带库名的，先按带库名算，对不上再按不带算。
// 分类有多级且互相嵌套时（电视剧 与 电视剧/日番 都是分类）取最长匹配。
// 返回标题目录的相对路径（台账口径，含库名前缀）、标题目录名、分类目录与其媒体类型
func (l libCategoryLayout) titleOf(relPath string) (key, titleDir, category, mediaType string, ok bool) {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	for _, off := range []int{1, 0} {
		best := -1
		// 标题目录之下至少还有一个文件：分类段最多到 len-2
		for n := 1; off+n <= len(segs)-2; n++ {
			if _, hit := l[strings.Join(segs[off:off+n], "/")]; hit {
				best = n
			}
		}
		if best < 0 {
			continue
		}
		category = strings.Join(segs[off:off+best], "/")
		return strings.Join(segs[:off+best+1], "/"), segs[off+best], category, l[category], true
	}
	return "", "", "", "", false
}

// scanLedgerTitles 全量扫描台账，按「标题目录」聚合（库名/<分类>/<标题目录>/…）
func scanLedgerTitles() map[string]*ledgerTitleEntry {
	layout := loadLibCategoryLayout()
	var sfs []model.SyncedFile
	model.DB.Where("kind = ?", "video").Find(&sfs)
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
			e = &ledgerTitleEntry{
				Key: key, Title: title, Year: year, TmdbID: tmdb,
				MediaType: mediaType, Category: category,
			}
			// key = [库名/]分类/标题：段数比分类多两段说明带着库名
			if segs := strings.Split(key, "/"); len(segs) == strings.Count(category, "/")+3 {
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
		if sf.UpdatedAt.After(e.LastAt) {
			e.LastAt = sf.UpdatedAt
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
