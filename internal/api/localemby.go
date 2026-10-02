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

// ==================== 本地文件页：卡片上的媒体信息（Emby 快照） ====================
//
// 片目详情的「媒体信息」是点开才查的（按路径逐个片目问 Emby），卡片墙上几百上千部不能这么问。
// 维护者选的做法：**打开本地文件页时**向 Emby 拉一次全库快照（分页读 Movie / Episode / Video 的媒体流），
// 按路径归到片目，记「条目数 / 还缺媒体信息的数」。平时不拉，没有后台定时任务。
// 只打 Emby，零 115 请求；一分钟内重复打开复用上一次，过期了先给旧的、后台重拉（kickLocalEmby）。
// 判据与提前探测同一个（hasMediaInfo / extractable）：光盘结构探测不了，不算缺。

// localEmbyStat 一个片目在 Emby 里的媒体信息计数
type localEmbyStat struct {
	Items int `json:"items"` // Emby 里这个片目的影视条目数
	Lack  int `json:"lack"`  // 其中还缺媒体信息、且能探测的
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
				log.Printf("[本地文件] ✗ 读取 Emby 媒体信息失败: %v", err)
			} else {
				s.stats, s.scanned, s.err = stats, scanned, ""
				log.Printf("[本地文件] ✓ Emby 媒体信息：%d 个条目，对上 %d 个片目，用时 %s",
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
		out[key] = st
	})
	if err != nil {
		return nil, 0, err
	}
	return out, scanned, nil
}

// walkLocalEmby 分页读 Emby 的影视条目，归得到台账片目的逐个交给 fn，返回读了多少条。
// 只读本地媒体库映射得到的那几个 Emby 库；一个都对不上时（映射没配 / 版本不返回 Locations）退回全服务器。
// 定时补全（metafill.go）也用它：要逐个条目看记账，光有计数不够
func walkLocalEmby(cfg embyRefreshCfg, root string, ledger map[string]*ledgerTitleEntry, fn func(key string, it embyExtractItem)) (int, error) {
	rootSlash := strings.TrimRight(filepath.ToSlash(root), "/")
	var parents []string
	for _, lib := range embyMediaFolders(cfg) {
		for _, loc := range lib.Locations {
			local := strings.TrimRight(embyPathToLocal(cfg.PathMapping, loc), "/")
			if local == rootSlash || strings.HasPrefix(local, rootSlash+"/") || strings.HasPrefix(rootSlash, local+"/") {
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
					pages[i], errs[i] = fetchLocalEmbyPage(cfg, parent, start+i*localEmbyPage)
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
					if key := localEmbyTitleKey(embyPathToLocal(cfg.PathMapping, it.Path), rootSlash, ledger); key != "" {
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

func fetchLocalEmbyPage(cfg embyRefreshCfg, parent string, start int) ([]embyExtractItem, error) {
	q := url.Values{
		"Recursive":        {"true"},
		"IncludeItemTypes": {"Movie,Episode,Video"},
		// MediaSources 会把媒体流再带一遍、页面体积翻倍，但少不了：多版本电影（同目录两个 .strm）
		// 在 Emby 里是一个条目两个版本，顶层 MediaStreams 只有主版本的，看不出另一个版本还没探
		"Fields":                 {"Path,MediaStreams,MediaSources"},
		"SortBy":                 {"DateCreated,SortName"}, // 翻页途中入库的新条目排在最后，不会挤得前面漏一条
		"SortOrder":              {"Ascending"},
		"StartIndex":             {strconv.Itoa(start)},
		"Limit":                  {strconv.Itoa(localEmbyPage)},
		"EnableTotalRecordCount": {"false"},
		"EnableImages":           {"false"},
		"EnableUserData":         {"false"},
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
	local = filepath.ToSlash(local)
	if rootSlash == "" || !strings.HasPrefix(local, rootSlash+"/") {
		return ""
	}
	segs := strings.Split(strings.TrimPrefix(local, rootSlash+"/"), "/")
	for n := 1; n < len(segs); n++ {
		if k := strings.Join(segs[:n], "/"); ledger[k] != nil {
			return k
		}
	}
	return ""
}

// LocalEmbyStats GET /local/titles/emby-stats?refresh=1：本地文件页打开时调一次，立即返回。
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
