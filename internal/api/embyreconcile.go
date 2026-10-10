package api

import (
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== Emby 与本地片目对账 ====================
//
// 总览的电影 / 剧集数读的是 Emby（/Items/Counts，全服务器），海报墙数的是台账片目
// （scanLedgerTitles：只认当前分类规则下的标题目录）。两个口径天然可能不一致，
// 用户看到「剧集 151 / 海报墙 134」只能干猜。这里把 Emby 的每个 Movie / Series 映射回本地，
// 逐个说清楚差在哪：不在本站目录下、Emby 残留、分类规则外、台账里没有、被拆成几个条目、类型不一致、
// 以及反方向「本地有、Emby 没有」。
//
// 只读：只打 Emby（几页 Movie + Series）与本地 os.Stat（只对没对上的条目），零 115 请求。
// 前提是路径映射对：一个都对不上时不出分桶（全会落进「不在本站目录下」，毫无意义），只给映射建议。

// embyReconItem Emby 的片目级条目（只要路径与年份）
type embyReconItem struct {
	ID             string `json:"Id"`
	Name           string `json:"Name"`
	Type           string `json:"Type"`
	Path           string `json:"Path"`
	ProductionYear int    `json:"ProductionYear"`
}

// embyReconEntry 分桶里的一条
type embyReconEntry struct {
	Name  string `json:"name"`
	Year  int    `json:"year,omitempty"`
	Path  string `json:"path,omitempty"`  // Emby 里的路径
	Local string `json:"local,omitempty"` // 换算到本地的路径
	Key   string `json:"key,omitempty"`   // 对上的台账片目
	Count int    `json:"count,omitempty"` // split：这个片目在 Emby 里有几个条目
}

// embyReconDir 分类规则外的条目按所在目录聚合：用户要做的是「把这个目录加进分类规则」
type embyReconDir struct {
	Dir     string   `json:"dir"` // 本地根下的相对路径
	Count   int      `json:"count"`
	Samples []string `json:"samples"`
}

// embyReconBucket 一个分桶：总数 + 至多 embyReconListMax 条明细
type embyReconBucket struct {
	Count int              `json:"count"`
	Items []embyReconEntry `json:"items"`
}

func (b *embyReconBucket) add(e embyReconEntry) {
	b.Count++
	if len(b.Items) < embyReconListMax {
		b.Items = append(b.Items, e)
	}
}

// embyReconSide 一种媒体类型（movie / tv）的对账结果。
// 恒等式：Emby − 台账 = Outside + Stale + Uncategorized + Unledgered + TypeMismatch + SplitExtra − Missing
type embyReconSide struct {
	Emby   int `json:"emby"`   // Emby 里这一类的条目数
	Ledger int `json:"ledger"` // 台账（海报墙）里这一类的片目数
	// Matched 对上了的片目数（去重后）
	Matched int `json:"matched"`
	// Outside 换算后不在本地媒体库根下：Emby 另挂的库，本站不管
	Outside embyReconBucket `json:"outside"`
	// Stale 在根下但本地已经没有了：Emby 没扫库清理的残留条目
	Stale embyReconBucket `json:"stale"`
	// Uncategorized 本地在、却不在当前任何分类目录下：海报墙不认它
	Uncategorized   embyReconBucket `json:"uncategorized"`
	UncategorizedBy []embyReconDir  `json:"uncategorized_dirs"`
	// Unledgered 落在分类目录下、本地在，但台账没有这个片目（不是本站同步 / 整理出来的 STRM）
	Unledgered embyReconBucket `json:"unledgered"`
	// TypeMismatch 对上的片目在台账里是另一种类型（Emby 认成剧集、台账算电影，或反过来）
	TypeMismatch embyReconBucket `json:"type_mismatch"`
	// Split 同一个片目在 Emby 里有多个条目；SplitExtra = 多出来的条目数
	Split      embyReconBucket `json:"split"`
	SplitExtra int             `json:"split_extra"`
	// Missing 台账有、Emby 里没有对应类型的条目
	Missing embyReconBucket `json:"missing"`
}

// embyReconcile 对账结果
type embyReconcile struct {
	MappingOK bool          `json:"mapping_ok"`
	Scanned   int           `json:"scanned"`
	Movie     embyReconSide `json:"movie"`
	TV        embyReconSide `json:"tv"`
}

const embyReconListMax = 100

// embyReconPathExists 换成本地路径后判断存在（可在测试里替换）
var embyReconPathExists = func(p string) bool {
	_, err := os.Stat(filepath.FromSlash(p))
	return err == nil
}

// reconcileEmby 纯函数：Emby 条目 × 台账片目 → 分桶。
// toLocal 把 Emby 路径换成本地路径；exists 判断本地路径在不在
func reconcileEmby(items []embyReconItem, ledger map[string]*ledgerTitleEntry, layout libCategoryLayout,
	rootSlash string, toLocal func(string) string, exists func(string) bool) embyReconcile {
	rootSlash = strings.TrimRight(filepath.ToSlash(rootSlash), "/")
	out := embyReconcile{Scanned: len(items)}
	side := func(t string) *embyReconSide {
		if t == "Series" {
			return &out.TV
		}
		return &out.Movie
	}
	for _, e := range ledger {
		if e.MediaType == "tv" {
			out.TV.Ledger++
		} else {
			out.Movie.Ledger++
		}
	}

	type hit struct{ items []embyReconItem }
	hits := map[string]map[string]*hit{"Movie": {}, "Series": {}} // 类型 → 片目 key → 条目
	uncatDirs := map[string]map[string]*embyReconDir{"Movie": {}, "Series": {}}
	for _, it := range items {
		if it.Type != "Movie" && it.Type != "Series" {
			continue
		}
		s := side(it.Type)
		s.Emby++
		local := strings.TrimRight(filepath.ToSlash(toLocal(it.Path)), "/")
		entry := embyReconEntry{Name: it.Name, Year: it.ProductionYear, Path: it.Path, Local: local}
		if local == "" || !strings.HasPrefix(local, rootSlash+"/") {
			s.Outside.add(entry)
			continue
		}
		dir := it.Type == "Series" // Series 的路径就是标题目录；Movie 的是视频文件
		if key := localEmbyTitleKeyOf(local, rootSlash, ledger, dir); key != "" {
			entry.Key = key
			want := "movie"
			if dir {
				want = "tv"
			}
			if ledger[key].MediaType != want {
				s.TypeMismatch.add(entry)
				continue
			}
			h := hits[it.Type][key]
			if h == nil {
				h = &hit{}
				hits[it.Type][key] = h
			}
			h.items = append(h.items, it)
			continue
		}
		if !exists(local) {
			s.Stale.add(entry)
			continue
		}
		rel := strings.TrimPrefix(local, rootSlash+"/")
		probe := rel
		if dir {
			probe += "/x" // titleOf 要的是标题目录下的文件
		}
		if _, _, _, _, ok := layout.titleOf(probe); ok {
			s.Unledgered.add(entry)
			continue
		}
		s.Uncategorized.add(entry)
		// 归到「标题目录的上一层」：那一层就是该加进分类规则的目录
		titleDir := rel
		if !dir {
			titleDir = path.Dir(rel)
		}
		parent := path.Dir(titleDir)
		if parent == "." {
			parent = ""
		}
		d := uncatDirs[it.Type][parent]
		if d == nil {
			d = &embyReconDir{Dir: parent}
			uncatDirs[it.Type][parent] = d
		}
		d.Count++
		if len(d.Samples) < 5 {
			d.Samples = append(d.Samples, path.Base(titleDir))
		}
	}

	for typ, byKey := range hits {
		s := side(typ)
		s.Matched = len(byKey)
		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h := byKey[k]
			if n := len(h.items); n > 1 {
				s.SplitExtra += n - 1
				s.Split.add(embyReconEntry{Name: h.items[0].Name, Year: h.items[0].ProductionYear, Key: k, Count: n})
			}
		}
		for _, d := range uncatDirs[typ] {
			s.UncategorizedBy = append(s.UncategorizedBy, *d)
		}
		sort.Slice(s.UncategorizedBy, func(i, j int) bool {
			a, b := s.UncategorizedBy[i], s.UncategorizedBy[j]
			if a.Count != b.Count {
				return a.Count > b.Count
			}
			return a.Dir < b.Dir
		})
	}

	// 反方向：台账有、Emby 没有对应类型的条目
	keys := make([]string, 0, len(ledger))
	for k := range ledger {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := ledger[k]
		typ, s := "Movie", &out.Movie
		if e.MediaType == "tv" {
			typ, s = "Series", &out.TV
		}
		if hits[typ][k] == nil {
			y, _ := strconv.Atoi(e.Year)
			s.Missing.add(embyReconEntry{Name: e.Title, Year: y, Key: k})
		}
	}

	out.MappingOK = out.Movie.Matched+out.TV.Matched+out.Movie.TypeMismatch.Count+out.TV.TypeMismatch.Count > 0 ||
		len(items) == 0 || len(ledger) == 0
	return out
}

// fetchEmbyReconItems 读全服务器的 Movie / Series（总览的 /Items/Counts 也是全服务器，口径要对齐）
func fetchEmbyReconItems(cfg embyRefreshCfg) ([]embyReconItem, error) {
	var all []embyReconItem
	for start := 0; start < localEmbyMaxItems; start += localEmbyPage {
		q := url.Values{
			"Recursive":              {"true"},
			"IncludeItemTypes":       {"Movie,Series"},
			"Fields":                 {"Path,ProductionYear"},
			"SortBy":                 {"DateCreated,SortName"},
			"SortOrder":              {"Ascending"},
			"StartIndex":             {strconv.Itoa(start)},
			"Limit":                  {strconv.Itoa(localEmbyPage)},
			"EnableTotalRecordCount": {"false"},
			"EnableImages":           {"false"},
			"EnableUserData":         {"false"},
		}
		var page struct {
			Items []embyReconItem `json:"Items"`
		}
		if err := embyGetJSON(cfg.ServerURL, cfg.APIKey, "/Items", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if len(page.Items) < localEmbyPage {
			break
		}
	}
	return all, nil
}

// ---- 路径映射推算 ----

// embyRootEvidence 推算依据：Emby 媒体库里的这个目录，对上了本地的那个目录
type embyRootEvidence struct {
	Location string `json:"location"`
	Local    string `json:"local"`
}

// suggestEmbyRoot 纯函数：按 Emby 媒体库的目录推算「本地媒体库根在 Emby 里叫什么」。
//
// 做法是拿 Emby 目录的尾巴去本地根下找：Emby 的 /mnt/strm/剧集 → 本地 /media/strm/剧集 存在 →
// Emby 根就是 /mnt/strm。尾巴取能对上的最长一段（本地若是 根/115/剧集，Emby 的 /data/115/剧集 推出 /data）。
// 各个媒体库各推一个，取票数最多的（同票取对上段数多的）；别的硬盘上的库对不上本地任何目录，不投票。
// 一个都没对上时退一步：Emby 目录名与本地根同名（媒体库直接挂的就是根）就认它。
func suggestEmbyRoot(locations []string, localRoot string, isDir func(string) bool) (string, []embyRootEvidence) {
	localRoot = strings.TrimRight(filepath.ToSlash(localRoot), "/")
	if localRoot == "" {
		return "", nil
	}
	type cand struct {
		votes, depth int
		ev           []embyRootEvidence
	}
	cands := map[string]*cand{}
	var order []string
	vote := func(root string, depth int, ev embyRootEvidence) {
		c := cands[root]
		if c == nil {
			c = &cand{}
			cands[root] = c
			order = append(order, root)
		}
		c.votes++
		c.depth += depth
		c.ev = append(c.ev, ev)
	}
	for _, raw := range locations {
		loc := strings.TrimRight(strings.ReplaceAll(raw, "\\", "/"), "/")
		segs := strings.Split(loc, "/")
		for k := len(segs) - 1; k >= 1; k-- {
			prefix := strings.Join(segs[:len(segs)-k], "/")
			if strings.Trim(prefix, "/") == "" {
				continue // Emby 根成了「/」：映射存不下这种值，也不太可能是真的
			}
			suffix := strings.Join(segs[len(segs)-k:], "/")
			if local := localRoot + "/" + suffix; isDir(local) {
				vote(prefix, k, embyRootEvidence{Location: loc, Local: local})
				break
			}
		}
	}
	if len(cands) == 0 {
		base := path.Base(localRoot)
		for _, raw := range locations {
			loc := strings.TrimRight(strings.ReplaceAll(raw, "\\", "/"), "/")
			if loc != "" && path.Base(loc) == base {
				vote(loc, 0, embyRootEvidence{Location: loc, Local: localRoot})
			}
		}
	}
	best := ""
	for _, r := range order {
		c := cands[r]
		if b := cands[best]; best == "" || c.votes > b.votes || (c.votes == b.votes && c.depth > b.depth) {
			best = r
		}
	}
	if best == "" {
		return "", nil
	}
	return best, cands[best].ev
}

func embyReconIsDir(p string) bool {
	st, err := os.Stat(filepath.FromSlash(p))
	return err == nil && st.IsDir()
}

// embyPathSuggestion 当前映射与推算结果（两个接口共用）
func embyPathSuggestion(cfg embyRefreshCfg, rootSlash string) gin.H {
	_, current := embyPathRoots(cfg.PathMapping)
	libs, err := embyVirtualFolders(cfg)
	if err != nil {
		return gin.H{"local_root": rootSlash, "current": current, "error": err.Error()}
	}
	var locs []string
	type libInfo struct {
		Name      string   `json:"name"`
		Locations []string `json:"locations"`
		UnderRoot bool     `json:"under_root"` // 按当前映射，这个库在不在本地媒体库根下
	}
	libOut := make([]libInfo, 0, len(libs))
	covered := 0
	for _, lib := range libs {
		li := libInfo{Name: lib.Name, Locations: lib.Locations}
		for _, loc := range lib.Locations {
			locs = append(locs, loc)
			if embyLocUnderRoot(cfg.PathMapping, loc, rootSlash) {
				li.UnderRoot = true
			}
		}
		if li.UnderRoot {
			covered++
		}
		libOut = append(libOut, li)
	}
	suggest, ev := suggestEmbyRoot(locs, rootSlash, embyReconIsDir)
	if ev == nil {
		ev = []embyRootEvidence{}
	}
	return gin.H{
		"local_root": rootSlash, "current": current, "suggest": suggest, "evidence": ev,
		"libraries": libOut, "covered": covered,
	}
}

// EmbyPathSuggest GET /emby/path-suggest：按 Emby 媒体库目录推算「Emby 媒体库目录」该填什么。
// 只给建议不保存：映射改错了 302 播放与删除联动都会出错，由用户在设置页确认
func (h *Handler) EmbyPathSuggest(c *gin.Context) {
	cfg, ok := loadEmbyRefreshCfg()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	root := strings.TrimRight(filepath.ToSlash(localMediaRoot()), "/")
	out := embyPathSuggestion(cfg, root)
	out["configured"] = true
	c.JSON(http.StatusOK, out)
}

// LocalTitlesReconcile GET /local/titles/reconcile：Emby 与海报墙的电影 / 剧集数逐项对账
func (h *Handler) LocalTitlesReconcile(c *gin.Context) {
	cfg, ok := loadEmbyRefreshCfg()
	root := strings.TrimRight(filepath.ToSlash(localMediaRoot()), "/")
	if !ok || root == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	items, err := fetchEmbyReconItems(cfg)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取 Emby 条目失败：" + err.Error()})
		return
	}
	res := reconcileEmby(items, scanLedgerTitlesCached(), loadLibCategoryLayout(), root,
		func(p string) string { return embyPathToLocal(cfg.PathMapping, p) }, embyReconPathExists)
	out := gin.H{"configured": true, "local_root": root, "result": res}
	if !res.MappingOK {
		out["mapping"] = embyPathSuggestion(cfg, root)
	}
	c.JSON(http.StatusOK, out)
}
