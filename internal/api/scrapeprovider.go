package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== 刮削方式：本站刮削 / Emby 刮削 ====================
//
// 有的用户不用本站刮削，让 Emby 自己从 TMDB 拉元数据。此前本站不知道这一点：海报墙只看
// STRM 目录里的 NFO / 图片，Emby 默认把刮来的东西存在它自己的元数据目录里，于是整面墙都是
// 「未刮削」、一张海报都没有；媒体信息补全还会去补刮，写下的 NFO 优先级比 Emby 在线刮削高，
// 反过来把用户在 Emby 里调好的元数据盖掉。
//
// 选了 Emby 刮削（scrapeCfg.Provider = emby）后，本站一样元数据都不写，每个入口都在后端挡住，不只是界面置灰：
//   - 整理后刮削：newOrgSink 的 scrapeOn 为假，Emby 刷新照常由整理自己做（flushRefresh），入库确认与自动探测不受影响
//   - 增量同步后刮削：enqueueSyncScrape 不入队
//   - 媒体信息补全：补刮这一步跳过，探测照常
//   - 海报墙手动刮削：POST /local/scrape 返回 409
//   - 切换之前排下的刮削任务、任务中心「重试」：execScrapeJob 开头跳过（整理交过来的 Emby 刷新照刷）
//
// 轨道探测、演职人员补全不归这个开关管：前者让 Emby 提前读轨道，后者整条走 Emby API，
// 都与谁写 NFO 无关（演职人员只有「刮削后补全」这一项随刮削一起失效，因为不会再有刮削任务）。
//
// 海报墙在 Emby 模式下改看 Emby：海报 / 背景图取 Emby 的，状态按 Emby 认没认出条目分级（localemby.go）。

const (
	scrapeProviderStation = "station"
	scrapeProviderEmby    = "emby"
)

// errEmbyScrapes 被开关挡下时的统一说法
const errEmbyScrapes = "刮削方式为「Emby 刮削」，本站不写 NFO 与图片"

// provider 归一：只认 emby，其余（含老配置的空值）都是本站刮削
func (c scrapeCfg) provider() string {
	if c.Provider == scrapeProviderEmby {
		return scrapeProviderEmby
	}
	return scrapeProviderStation
}

// stationScrapes 本站负责刮削（默认）
func (c scrapeCfg) stationScrapes() bool { return c.provider() == scrapeProviderStation }

// SetScrapeProvider POST /scrape/provider {provider: station|emby}
func (h *Handler) SetScrapeProvider(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Provider != scrapeProviderStation && req.Provider != scrapeProviderEmby) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：provider 只能是 station 或 emby"})
		return
	}
	cfg := loadScrapeCfg()
	prev := cfg.provider()
	cfg.Provider = req.Provider
	if err := saveScrapeCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if prev != req.Provider {
		log.Printf("[影视刮削] ✓ 刮削方式：%s → %s", providerLabel(prev), providerLabel(req.Provider))
		forgetLocalTitles() // 卡片状态的口径跟着变
	}
	c.JSON(http.StatusOK, gin.H{"message": "已切换为" + providerLabel(req.Provider), "provider": req.Provider})
}

func providerLabel(p string) string {
	if p == scrapeProviderEmby {
		return "「Emby 刮削」"
	}
	return "「本站刮削」"
}

// ---- Emby 媒体库设置检查 ----
//
// 切到 Emby 刮削后，Emby 媒体库要是没开元数据下载器，就没有任何人去刮了；反过来本站刮削时，
// Emby 开着下载器会再联网刮一遍、Nfo 读取器没勾则根本不读本站写的 NFO（「刮削」页签那段设置说明）。
// 只读 Emby 的媒体库选项，不改：改 Emby 设置是用户的事，这里只把哪个库不对说出来

// embyVirtualFolder GET /Library/VirtualFolders 的一项（只取用得到的字段）
type embyVirtualFolder struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType"`
	Locations      []string `json:"Locations"`
	LibraryOptions struct {
		TypeOptions []struct {
			Type             string   `json:"Type"`
			MetadataFetchers []string `json:"MetadataFetchers"`
			ImageFetchers    []string `json:"ImageFetchers"`
		} `json:"TypeOptions"`
		DisabledLocalMetadataReaders []string `json:"DisabledLocalMetadataReaders"`
	} `json:"LibraryOptions"`
}

// embyLibCheck 一个媒体库的检查结果
type embyLibCheck struct {
	Name           string   `json:"name"`
	CollectionType string   `json:"collection_type,omitempty"`
	MetaFetchers   []string `json:"meta_fetchers"`
	ImageFetchers  []string `json:"image_fetchers"`
	// Known 读到了 Movie / Series 的下载器设置；读不到时不对下载器下结论
	Known     bool `json:"known"`
	NfoReader bool `json:"nfo_reader"`
	// EmbyIssues 用 Emby 刮削时的问题；StationIssues 用本站刮削时的问题。两种都给，界面切换前就能看到
	EmbyIssues    []string `json:"emby_issues,omitempty"`
	StationIssues []string `json:"station_issues,omitempty"`
}

// checkEmbyLib 纯函数：按媒体库选项判断两种刮削方式下各有什么问题。
// 下载器只看 Movie / Series 两种类型（季、集跟着剧走），合在一起去重
func checkEmbyLib(lib embyVirtualFolder) embyLibCheck {
	out := embyLibCheck{Name: lib.Name, CollectionType: lib.CollectionType, NfoReader: true,
		MetaFetchers: []string{}, ImageFetchers: []string{}}
	meta, img := map[string]bool{}, map[string]bool{}
	for _, t := range lib.LibraryOptions.TypeOptions {
		if t.Type != "Movie" && t.Type != "Series" {
			continue
		}
		out.Known = true
		for _, f := range t.MetadataFetchers {
			meta[f] = true
		}
		for _, f := range t.ImageFetchers {
			img[f] = true
		}
	}
	for f := range meta {
		out.MetaFetchers = append(out.MetaFetchers, f)
	}
	for f := range img {
		out.ImageFetchers = append(out.ImageFetchers, f)
	}
	sort.Strings(out.MetaFetchers)
	sort.Strings(out.ImageFetchers)
	for _, r := range lib.LibraryOptions.DisabledLocalMetadataReaders {
		if strings.EqualFold(r, "Nfo") {
			out.NfoReader = false
		}
	}
	if out.Known {
		if len(out.MetaFetchers) == 0 {
			out.EmbyIssues = append(out.EmbyIssues, "没有启用元数据下载器（TheMovieDb 等），Emby 不会去刮削")
		}
		if len(out.ImageFetchers) == 0 {
			out.EmbyIssues = append(out.EmbyIssues, "没有启用图像获取器，Emby 不会下载海报")
		}
		if len(out.MetaFetchers) > 0 {
			out.StationIssues = append(out.StationIssues, "启用了元数据下载器（"+strings.Join(out.MetaFetchers, "、")+"），Emby 会自己再刮一遍")
		}
		if len(out.ImageFetchers) > 0 {
			out.StationIssues = append(out.StationIssues, "启用了图像获取器（"+strings.Join(out.ImageFetchers, "、")+"），Emby 会自己再下一遍图")
		}
	}
	if !out.NfoReader {
		out.StationIssues = append(out.StationIssues, "没勾 Nfo 元数据读取器，本站写的 NFO Emby 不读")
	}
	return out
}

// embyLocUnderRoot Emby 媒体库的一个路径映射回本地后，与本地媒体库根有没有包含关系（任一方向）。
// rootSlash 是 / 分隔、去掉结尾 / 的本地根
func embyLocUnderRoot(pathMapping, loc, rootSlash string) bool {
	local := strings.TrimRight(filepath.ToSlash(embyPathToLocal(pathMapping, loc)), "/")
	return local != "" && (local == rootSlash || strings.HasPrefix(local, rootSlash+"/") || strings.HasPrefix(rootSlash, local+"/"))
}

// ScrapeEmbyCheck GET /scrape/emby-check：本地媒体库对应的 Emby 媒体库，各自的元数据设置合不合当前刮削方式
func (h *Handler) ScrapeEmbyCheck(c *gin.Context) {
	cfg, ok := loadEmbyRefreshCfg()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	root := strings.TrimRight(filepath.ToSlash(localMediaRoot()), "/")
	libs, err := embyVirtualFolders(cfg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"configured": true, "error": err.Error()})
		return
	}
	out := []embyLibCheck{}
	for _, lib := range libs {
		hit := root == "" // 没配本地根：全列出来，总比什么都不说强
		for _, loc := range lib.Locations {
			if !hit && embyLocUnderRoot(cfg.PathMapping, loc, root) {
				hit = true
			}
		}
		if hit {
			out = append(out, checkEmbyLib(lib))
		}
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "libraries": out, "unmatched": len(out) == 0 && len(libs) > 0})
}

// embyVirtualFolders 媒体库列表连同选项。用旧的 GET /Library/VirtualFolders：/Query 那个分页版不带 LibraryOptions 的全部字段
func embyVirtualFolders(cfg embyRefreshCfg) ([]embyVirtualFolder, error) {
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Library/VirtualFolders", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("连不上 Emby：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取 Emby 媒体库失败：HTTP %d（需要管理员 API 密钥）", resp.StatusCode)
	}
	var libs []embyVirtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		return nil, fmt.Errorf("Emby 媒体库响应无法解析：%v", err)
	}
	return libs, nil
}
