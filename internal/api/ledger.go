package api

import (
	"regexp"
	"strconv"
	"strings"
	"strmhub/internal/model"
	"sync"
	"time"
)

// ledgerTitleEntry 从台账聚合出的一个标题条目
type ledgerTitleEntry struct {
	Key       string // 标题目录的相对路径（详情查询用）
	Title     string
	Year      string
	TmdbID    int
	MediaType string
	Category  string
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

// scanLedgerTitles 全量扫描台账，按"标题目录"聚合（库名/电影|剧集/分类/标题目录/…）
func scanLedgerTitles() map[string]*ledgerTitleEntry {
	var sfs []model.SyncedFile
	model.DB.Where("kind = ?", "video").Find(&sfs)
	out := map[string]*ledgerTitleEntry{}
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
		titleDepth := 4
		if len(segs) == 3 {
			titleDepth = 2
		}
		key := strings.Join(segs[:titleDepth], "/")
		e, ok := out[key]
		if !ok {
			title, year, tmdb := parseTitleDir(titleDir)
			e = &ledgerTitleEntry{
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
