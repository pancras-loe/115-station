package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 海报墙：卡片上的媒体信息（Emby 快照） ====================
//
// 片目详情的「媒体信息」是点开才查的（按路径逐个片目问 Emby），卡片墙上几百上千部不能这么问。
// 维护者选的做法：**打开海报墙时**向 Emby 拉一次全库快照（分页读 Movie / Episode / Video 的媒体流），
// 按路径归到片目，记「条目数 / 还缺媒体信息的数」。平时不拉，没有后台定时任务。
// 只打 Emby，零 115 请求；一分钟内重复打开复用上一次，过期了先给旧的、后台重拉（kickLocalEmby）。
// 判据与提前探测同一个（hasMediaInfo / extractable）：光盘结构探测不了，不算缺。

// localEmbyStat 一个片目在 Emby 里的媒体信息计数，以及片目级条目（电影的 Movie、剧集的 Series）的概况
type localEmbyStat struct {
	Items int `json:"items"` // Emby 里这个片目的影视条目数
	Lack  int `json:"lack"`  // 其中还缺媒体信息、且能探测的
	// ItemID 片目级条目：卡片海报 / 详情背景图从它取（/embyimg），「刷新元数据」也打它。空 = Emby 没把它认成影视条目
	ItemID   string `json:"item_id,omitempty"`
	Tmdb     string `json:"tmdb,omitempty"`
	Imdb     string `json:"imdb,omitempty"`
	Poster   bool   `json:"poster,omitempty"`
	Backdrop bool   `json:"backdrop,omitempty"`
	// refresh 片目级条目的全部 id：多版本电影（同目录两个 .strm）在 Emby 里是几个 Movie 条目，刷新元数据要逐个刷
	refresh []string
}

// addTitleItem 记一个片目级条目。多版本电影有几个，挑有海报的那个当门面
func (st *localEmbyStat) addTitleItem(it embyExtractItem) {
	if it.ID == "" {
		return
	}
	st.refresh = append(st.refresh, it.ID)
	poster := it.ImageTags["Primary"] != ""
	if st.ItemID == "" || (poster && !st.Poster) {
		st.ItemID, st.Poster, st.Backdrop = it.ID, poster, len(it.BackdropImageTags) > 0
	}
	if st.Tmdb == "" {
		st.Tmdb = providerID(it.ProviderIds, "Tmdb")
	}
	if st.Imdb == "" {
		st.Imdb = providerID(it.ProviderIds, "Imdb")
	}
}

// gradeByEmby 纯函数：刮削方式为 Emby 时，卡片状态改按 Emby 分级（本地有没有 NFO 不再说明任何事）。
// ok = 认出了条目（有 TMDB / IMDb 编号）且海报、背景图都有；partial = 缺一部分，或认成的 TMDB 条目和台账对不上；
// miss = Emby 里没有这个片目的影视条目（还没入库，或只被当成了普通文件夹）
func gradeByEmby(t *localTitle, st *localEmbyStat) {
	t.Lack, t.Soft, t.Mismatch = nil, nil, ""
	if st == nil || st.ItemID == "" {
		t.Status = "miss"
		return
	}
	if st.Tmdb == "" && st.Imdb == "" {
		t.Lack = append(t.Lack, "TMDB 编号")
	}
	if !st.Poster {
		t.Lack = append(t.Lack, "海报")
	}
	if !st.Backdrop {
		t.Lack = append(t.Lack, "背景图")
	}
	// 台账的编号来自目录名 {tmdbid=…}，是整理时认定的；Emby 认成别的就是刮错了，比缺图要紧，排第一
	if t.TmdbID > 0 && st.Tmdb != "" && st.Tmdb != strconv.Itoa(t.TmdbID) {
		t.Mismatch = st.Tmdb
	}
	t.Status = "ok"
	if len(t.Lack) > 0 || t.Mismatch != "" {
		t.Status = "partial"
	}
}

var localEmbySnap struct {
	mu      sync.Mutex
	running bool // 后台正在拉
	again   bool // 拉的途中又被要求强制刷新：拉完再来一轮（途中的探测结果可能没赶上）
	at      time.Time
	stats   map[string]localEmbyStat
	scanned int
	err     string
}

const (
	// 过了这个时间再打开页面，先给上一份、后台重拉（不再让页面等着）
	localEmbyTTL = time.Minute
	// 只要 Path 与 MediaStreams，一条一两 KB；500 条一页，万集的库二十来页
	localEmbyPage     = 500
	localEmbyParallel = 3      // 一次并发拉几页：串行时大库要等半分钟以上，再多又怕压着 Emby
	localEmbyMaxItems = 200000 // 防守：Emby 不认 StartIndex 时别无限翻页
)

// localEmbyStats 当前快照（只读）；从没拉过时为 nil
func localEmbyStats() map[string]localEmbyStat {
	localEmbySnap.mu.Lock()
	defer localEmbySnap.mu.Unlock()
	return localEmbySnap.stats
}

// kickLocalEmby 需要时在后台拉一份快照，返回此刻是否正在拉。
// 2026-10 起不再在请求里同步拉：大库一次要读几十页带媒体流的条目，页面每过一分钟再打开就要干等。
// 现在有旧快照就先用旧的（卡片立即带角标），新的拉完前端再原地刷新。失败时保留上一份，只记错误
func kickLocalEmby(cfg embyRefreshCfg, root string, force bool) bool {
	s := &localEmbySnap
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		if force {
			s.again = true
		}
		return true
	}
	fresh := s.stats != nil && s.err == "" && time.Since(s.at) < localEmbyTTL
	if fresh && !force {
		return false
	}
	s.running = true
	go func() {
		for {
			began := time.Now()
			stats, scanned, err := fetchLocalEmby(cfg, root, scanLedgerTitlesCached())
			s.mu.Lock()
			s.at = time.Now()
			if err != nil {
				s.err = err.Error()
				log.Printf("[海报墙] ✗ 读取 Emby 媒体信息失败: %v", err)
			} else {
				s.stats, s.scanned, s.err = stats, scanned, ""
				log.Printf("[海报墙] ✓ Emby 媒体信息：%d 个条目，对上 %d 个片目，用时 %s",
					scanned, len(stats), time.Since(began).Round(100*time.Millisecond))
			}
			if !s.again {
				s.running = false
				s.mu.Unlock()
				return
			}
			s.again = false
			s.mu.Unlock()
		}
	}()
	return true
}

// fetchLocalEmby 分页读 Emby 的影视条目，按路径归到台账片目
func fetchLocalEmby(cfg embyRefreshCfg, root string, ledger map[string]*ledgerTitleEntry) (map[string]localEmbyStat, int, error) {
	out := map[string]localEmbyStat{}
	scanned, err := walkLocalEmby(cfg, root, ledger, func(key string, it embyExtractItem) {
		st := out[key]
		st.Items++
		if it.needsProbe(cfg.PathMapping) {
			st.Lack++
		}
		if it.Type == "Movie" {
			st.addTitleItem(it)
		}
		out[key] = st
	})
	if err != nil {
		return nil, 0, err
	}
	// 剧集的片目级条目是 Series（路径就是标题目录）：另读一遍，一部剧一条，比集少得多
	if _, err := walkLocalEmbyTypes(cfg, root, ledger, "Series", "Path,ProviderIds", true, func(key string, it embyExtractItem) {
		st := out[key]
		st.addTitleItem(it)
		out[key] = st
	}); err != nil {
		return nil, 0, err
	}
	return out, scanned, nil
}

// walkLocalEmby 分页读 Emby 的影视条目，归得到台账片目的逐个交给 fn，返回读了多少条。
// 只读本地媒体库映射得到的那几个 Emby 库；一个都对不上时（映射没配 / 版本不返回 Locations）退回全服务器。
// 定时补全（metafill.go）也用它：要逐个条目看记账，光有计数不够
func walkLocalEmby(cfg embyRefreshCfg, root string, ledger map[string]*ledgerTitleEntry, fn func(key string, it embyExtractItem)) (int, error) {
	// MediaSources 会把媒体流再带一遍、页面体积翻倍，但少不了：多版本电影（同目录两个 .strm）
	// 在 Emby 里是一个条目两个版本，顶层 MediaStreams 只有主版本的，看不出另一个版本还没探
	return walkLocalEmbyTypes(cfg, root, ledger, "Movie,Episode,Video", "Path,MediaStreams,MediaSources,ProviderIds", false, fn)
}

// walkLocalEmbyTypes 同 walkLocalEmby，条目类型与字段可选。dir = 条目路径本身就是片目目录（Series），
// 归片目时连它自己也算；视频条目的路径是文件，只往上找
func walkLocalEmbyTypes(cfg embyRefreshCfg, root string, ledger map[string]*ledgerTitleEntry, types, fields string, dir bool, fn func(key string, it embyExtractItem)) (int, error) {
	rootSlash := strings.TrimRight(filepath.ToSlash(root), "/")
	var parents []string
	for _, lib := range embyMediaFolders(cfg) {
		for _, loc := range lib.Locations {
			if embyLocUnderRoot(cfg.PathMapping, loc, rootSlash) {
				parents = append(parents, lib.ID)
				break
			}
		}
	}
	if len(parents) == 0 {
		parents = []string{""}
	}
	scanned := 0
	for _, parent := range parents {
		// 一批并发拉 localEmbyParallel 页，按页序交给 fn（fn 不必是并发安全的），
		// 内存里最多压着一批；哪页不满就是最后一页，同批后面的空页多拉一次无妨
		for start := 0; start < localEmbyMaxItems; start += localEmbyPage * localEmbyParallel {
			pages := make([][]embyExtractItem, localEmbyParallel)
			errs := make([]error, localEmbyParallel)
			var wg sync.WaitGroup
			for i := range pages {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					pages[i], errs[i] = fetchLocalEmbyPage(cfg, parent, start+i*localEmbyPage, types, fields)
				}(i)
			}
			wg.Wait()
			last := false
			for i, items := range pages {
				if errs[i] != nil {
					return 0, errs[i]
				}
				for _, it := range items {
					scanned++
					if key := localEmbyTitleKeyOf(embyPathToLocal(cfg.PathMapping, it.Path), rootSlash, ledger, dir); key != "" {
						fn(key, it)
					}
				}
				if len(items) < localEmbyPage {
					last = true
					break
				}
			}
			if last {
				break
			}
		}
	}
	return scanned, nil
}

func fetchLocalEmbyPage(cfg embyRefreshCfg, parent string, start int, types, fields string) ([]embyExtractItem, error) {
	q := url.Values{
		"Recursive":              {"true"},
		"IncludeItemTypes":       {types},
		"Fields":                 {fields},
		"SortBy":                 {"DateCreated,SortName"}, // 翻页途中入库的新条目排在最后，不会挤得前面漏一条
		"SortOrder":              {"Ascending"},
		"StartIndex":             {strconv.Itoa(start)},
		"Limit":                  {strconv.Itoa(localEmbyPage)},
		"EnableTotalRecordCount": {"false"},
		// 只要海报 / 背景图的标签（判断有没有图），一种一张
		"EnableImageTypes": {"Primary,Backdrop"},
		"ImageTypeLimit":   {"1"},
		"EnableUserData":   {"false"},
	}
	if parent != "" {
		q.Set("ParentId", parent)
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Items []embyExtractItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// localEmbyTitleKey 纯函数：Emby 条目映射回本地的路径 → 台账片目 key（逐级往上找，找不到为空）
func localEmbyTitleKey(local, rootSlash string, ledger map[string]*ledgerTitleEntry) string {
	return localEmbyTitleKeyOf(local, rootSlash, ledger, false)
}

// localEmbyTitleKeyOf 同 localEmbyTitleKey；dir = 路径本身是目录（Series），连它自己也算
func localEmbyTitleKeyOf(local, rootSlash string, ledger map[string]*ledgerTitleEntry, dir bool) string {
	local = strings.TrimRight(filepath.ToSlash(local), "/")
	if rootSlash == "" || !strings.HasPrefix(local, rootSlash+"/") {
		return ""
	}
	segs := strings.Split(strings.TrimPrefix(local, rootSlash+"/"), "/")
	last := len(segs) - 1
	if dir {
		last = len(segs)
	}
	for n := 1; n <= last; n++ {
		if k := strings.Join(segs[:n], "/"); ledger[k] != nil {
			return k
		}
	}
	return ""
}

// LocalEmbyStats GET /local/titles/emby-stats?refresh=1：海报墙打开时调一次，立即返回。
// refreshing=true 表示后台正在拉，前端隔一会儿再问（不带 refresh），拉完重读列表
func (h *Handler) LocalEmbyStats(c *gin.Context) {
	cfg, configured := loadEmbyRefreshCfg()
	root := localMediaRoot()
	if !configured || root == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	refreshing := kickLocalEmby(cfg, root, c.Query("refresh") == "1")
	s := &localEmbySnap
	s.mu.Lock()
	defer s.mu.Unlock()
	lacking := 0
	for _, st := range s.stats {
		if st.Lack > 0 {
			lacking++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"configured": true, "ready": s.stats != nil, "refreshing": refreshing, "at": s.at, "error": s.err,
		"scanned": s.scanned, "titles": len(s.stats), "lacking": lacking,
	})
}

// ---- 让 Emby 刷新元数据（刮削方式为 Emby 时，海报墙用它代替「刮削」）----

// localEmbyRefreshMax 一次最多刷多少部：每部一个请求，Emby 自己排队慢慢刮
const localEmbyRefreshMax = 500

// LocalTitlesEmbyRefresh POST /local/titles/emby-refresh {keys, replace}
//
// 对片目级条目（电影的 Movie、剧集的 Series）发 POST /Items/{id}/Refresh，Recursive 连带季与集。
// replace = 替换全部元数据与图片（认错了条目、想推倒重刮时用）；不勾只补缺。
// 刷新本身是 Emby 后台做的，这里只是把请求排进去，几十秒到几分钟后才看得到结果
func (h *Handler) LocalTitlesEmbyRefresh(c *gin.Context) {
	var req struct {
		Keys    []string `json:"keys"`
		Replace bool     `json:"replace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	keys := normalizeTitleKeys(req.Keys)
	switch {
	case len(keys) == 0:
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有选择片目"})
		return
	case len(keys) > localEmbyRefreshMax:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多刷新 %d 部", localEmbyRefreshMax)})
		return
	}
	cfg, ok := loadEmbyRefreshCfg()
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Emby"})
		return
	}
	stats := localEmbyStats()
	if stats == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "还没读到 Emby 的媒体库信息，稍等几秒再试"})
		return
	}
	var refreshed, failed int
	var missing []string
	for _, k := range keys {
		st := stats[k]
		if len(st.refresh) == 0 {
			missing = append(missing, k)
			continue
		}
		for _, id := range st.refresh {
			if embyRefreshMetadata(cfg, id, req.Replace) {
				refreshed++
			} else {
				failed++
			}
		}
	}
	mode := "补缺"
	if req.Replace {
		mode = "替换全部"
	}
	log.Printf("[海报墙] ✓ 让 Emby 刷新元数据（%s）：%d 个条目，失败 %d，Emby 里找不到 %d 部", mode, refreshed, failed, len(missing))
	msg := fmt.Sprintf("已让 Emby 刷新 %d 个条目，Emby 在后台刮削，稍后点刷新查看", refreshed)
	if failed > 0 {
		msg += fmt.Sprintf("；%d 个请求失败（见日志）", failed)
	}
	if len(missing) > 0 {
		msg += fmt.Sprintf("；%d 部在 Emby 里没有对应的影视条目，先让 Emby 扫描入库", len(missing))
	}
	status := http.StatusOK
	if refreshed == 0 {
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"message": msg, "error": msg, "refreshed": refreshed, "failed": failed, "missing": missing})
}

// embyRefreshMetadata 让 Emby 重新刮一个条目。和 embyRefreshItem（入库刷新，Default 模式只核对文件）不同，
// 这里要的就是联网刮削：FullRefresh；replace 再把已有的元数据与图片全部换掉
func embyRefreshMetadata(cfg embyRefreshCfg, itemID string, replace bool) bool {
	r := strconv.FormatBool(replace)
	q := url.Values{
		"Recursive":           {"true"},
		"MetadataRefreshMode": {"FullRefresh"},
		"ImageRefreshMode":    {"FullRefresh"},
		"ReplaceAllMetadata":  {r},
		"ReplaceAllImages":    {r},
	}
	resp, err := embyRequest(http.MethodPost, cfg.ServerURL, cfg.APIKey, "/Items/"+url.PathEscape(itemID)+"/Refresh", q, nil)
	if err != nil {
		log.Printf("[海报墙] ✗ 刷新元数据请求失败 %s: %v", itemID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[海报墙] ✗ 刷新元数据 HTTP %d（条目 %s）", resp.StatusCode, itemID)
		return false
	}
	return true
}
