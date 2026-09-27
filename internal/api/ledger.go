package api

import (
	"115-station/internal/model"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ledgerTitleEntry 从台账聚合出的一个标题条目
type ledgerTitleEntry struct {
	Key       string // 标题目录的相对路径（含库名前缀，详情查询用）
	Title     string
	Year      string
	TmdbID    int
	MediaType string
	Category  string // 分类目录（库内相对路径，如 电影、电视剧/日番）
	LastAt    time.Time
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
