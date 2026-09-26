package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 网盘文件页（115 目录树） ====================
//
// 前端 /files 页：逐级浏览 115 网盘，勾选文件 / 文件夹后「刮削」或「整理」。
// 两个动作都入任务队列（filescrape.go / fileorganize.go），这里只管列目录与定位。
//
// 形态参考 MoviePilot 的文件管理（对存储里任意条目发起刮削 / 手动整理），
// 列目录走 pan115Ops（OpenAPI 优先、Cookie 回退，统一过 throttle115）。

// browseCrumb 面包屑的一级（网盘根不在链里）
type browseCrumb struct {
	Cid  string `json:"cid"`
	Name string `json:"name"`
}

// fileEntry 目录里的一项
type fileEntry struct {
	ID       string `json:"id"` // 文件是 fid，目录是它自己的 cid（整理 / 移动都用这个）
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size,omitempty"`
	PickCode string `json:"pickcode,omitempty"`
	// Root 这个目录本身是哪个工作区根（library / pending / share / existing / redundant），不是则空
	Root string `json:"root,omitempty"`
}

// fileListLimit 单个目录最多列多少项。每页 1000 多条、每页一次节流，
// 上万个文件的目录全列完要十几秒，浏览没必要，多出来的提示用户去 115 里找
const fileListLimit = 6000

type fileListCacheEntry struct {
	items     []fileEntry
	truncated bool
	expires   time.Time
}

var (
	fileListMu    sync.Mutex
	fileListCache = map[string]fileListCacheEntry{}
)

// fileListCacheTTL 浏览缓存。短一点：整理 / 刮削会改目录内容，任务结束时也会整张清掉
const fileListCacheTTL = 2 * time.Minute

// resetFileListCache 网盘内容被本页发起的任务改过之后清缓存
func resetFileListCache() {
	fileListMu.Lock()
	fileListCache = map[string]fileListCacheEntry{}
	fileListMu.Unlock()
}

// workspaceRoles 工作区根目录 cid → 角色。整理配置不全时缺的那几个就不在表里
func (h *Handler) workspaceRoles() map[string]string {
	out := map[string]string{}
	var fullCfg struct {
		Cid string `json:"cid"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg)
	var basic OrgConfig
	_ = json.Unmarshal([]byte(h.getSettingValue("org-basic")), &basic)
	add := func(cid, role string) {
		if cid != "" && cid != "0" {
			if _, ok := out[cid]; !ok {
				out[cid] = role
			}
		}
	}
	add(fullCfg.Cid, "library")
	add(basic.Pending, "pending")
	add(h.shareFolderCid(), "share")
	add(basic.Existing, "existing")
	add(basic.Redundant, "redundant")
	return out
}

// ListFiles115 GET /files/115?cid=0[&refresh=1] → 目录内容（文件夹在前）+ 工作区根
func (h *Handler) ListFiles115(c *gin.Context) {
	cid := strings.TrimSpace(c.Query("cid"))
	if cid == "" {
		cid = "0"
	}
	roles := h.workspaceRoles()
	reply := func(items []fileEntry, truncated bool) {
		c.JSON(http.StatusOK, gin.H{"cid": cid, "data": items, "truncated": truncated, "roots": roles})
	}
	if c.Query("refresh") != "1" {
		fileListMu.Lock()
		e, ok := fileListCache[cid]
		fileListMu.Unlock()
		if ok && time.Now().Before(e.expires) {
			reply(withRoles(e.items, roles), e.truncated)
			return
		}
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	items, truncated, err := listFileEntries(ops, cid, fileListLimit)
	if err != nil {
		if errors.Is(err, errDirGone) {
			c.JSON(http.StatusNotFound, gin.H{"error": "目录已不存在（可能被删除或移动了），请返回上一级刷新"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取目录失败: " + err.Error()})
		return
	}
	fileListMu.Lock()
	if len(fileListCache) > 300 {
		fileListCache = map[string]fileListCacheEntry{}
	}
	fileListCache[cid] = fileListCacheEntry{items: items, truncated: truncated, expires: time.Now().Add(fileListCacheTTL)}
	fileListMu.Unlock()
	reply(withRoles(items, roles), truncated)
}

// withRoles 给目录项标上工作区角色（拷贝一份，缓存里的不改：配置可能中途改过）
func withRoles(items []fileEntry, roles map[string]string) []fileEntry {
	out := make([]fileEntry, len(items))
	for i, it := range items {
		if it.IsDir {
			it.Root = roles[it.ID]
		}
		out[i] = it
	}
	return out
}

// entryLister 列目录一页（pan115Ops 与测试桩都实现它）
type fileEntryLister interface {
	listEntries(cid string, offset int) ([]map[string]interface{}, int, error)
}

// listFileEntries 翻页列出目录全部条目，超过 limit 截断
func listFileEntries(ops fileEntryLister, cid string, limit int) ([]fileEntry, bool, error) {
	var items []fileEntry
	offset := 0
	truncated := false
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, false, err
		}
		for _, d := range raw {
			items = append(items, toFileEntry(d))
		}
		offset += len(raw)
		if len(raw) == 0 || offset >= count {
			break
		}
		if offset >= limit {
			truncated = true
			break
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return items[i].Name < items[j].Name
	})
	return items, truncated, nil
}

// toFileEntry webapi 形态的条目 → fileEntry。目录自身 id 在 cid 字段（webapi）
// 或已被 toWebapiMap 放进 cid（OpenAPI），文件用 fid
func toFileEntry(d map[string]interface{}) fileEntry {
	e := fileEntry{Name: nilSprint(d["n"]), IsDir: fmt.Sprint(d["f"]) == "0"}
	if e.IsDir {
		e.ID = nilSprint(d["cid"])
		if e.ID == "" {
			e.ID = nilSprint(d["fid"])
		}
		return e
	}
	e.ID = nilSprint(d["fid"])
	e.PickCode = nilSprint(d["pc"])
	if s, ok := d["s"].(float64); ok {
		e.Size = int64(s)
	}
	return e
}

// resolveBrowseChain 目录的祖先链（根在前、末元素是它自己；不含网盘根）。
//
// Cookie 通道一次请求拿整条链，是事实来源（顺带识别「cid 已失效、115 返回根目录」）；
// OpenAPI 独立模式取不到链时退回前端逐级点进来的面包屑 —— 那也是从网盘根一路点进来的，
// 只是可能过时，所以末元素必须对得上 cid
func (h *Handler) resolveBrowseChain(cid string, hint []browseCrumb) ([]browseCrumb, error) {
	if cid == "" || cid == "0" {
		return nil, nil
	}
	if cookie, err := h.get115Cookie(); err == nil && cookie != "" {
		chain, err := fetch115Ancestors(cookie, cid)
		if err != nil {
			return nil, err
		}
		out := make([]browseCrumb, 0, len(chain))
		for _, a := range chain {
			if a.cid == "" || a.cid == "0" {
				continue
			}
			out = append(out, browseCrumb{Cid: a.cid, Name: a.name})
		}
		if len(out) == 0 || out[len(out)-1].Cid != cid {
			return nil, errDirGone
		}
		return out, nil
	}
	if len(hint) == 0 || hint[len(hint)-1].Cid != cid {
		return nil, errors.New("当前 115 通道解析不出目录位置，请从网盘根目录重新点进来再操作")
	}
	return hint, nil
}

// chainZone 链上离得最近的工作区根：返回角色与它在链上的下标；不在任何工作区里返回 ("", -1)
func chainZone(chain []browseCrumb, roles map[string]string) (string, int) {
	for i := len(chain) - 1; i >= 0; i-- {
		if r := roles[chain[i].Cid]; r != "" {
			return r, i
		}
	}
	return "", -1
}

// chainRoleIndex 某个角色的根在链上的下标（不在链上 -1）
func chainRoleIndex(chain []browseCrumb, roles map[string]string, role string) int {
	for i, c := range chain {
		if roles[c.Cid] == role {
			return i
		}
	}
	return -1
}

// fileJobItem 前端勾选的一项
type fileJobItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	// PickCode 文件的 pickcode（刮削散文件时探测轨道用；目录没有）
	PickCode string `json:"pickcode,omitempty"`
}

// fileJobParams 网盘文件页发起的任务参数（刮削 / 整理共用）
type fileJobParams struct {
	Cid    string          `json:"cid"`             // 所选条目所在目录
	Chain  []browseCrumb   `json:"chain,omitempty"` // 前端面包屑：Cookie 通道取不到祖先链时兜底
	Items  []fileJobItem   `json:"items"`
	Scrape *fileScrapeOpts `json:"scrape,omitempty"`
}

// fileJobRequest 两个入队接口的请求体
type fileJobRequest struct {
	fileJobParams
	TmdbID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
	Label     string `json:"label"`
}

// fileJobMaxItems 一次最多勾多少项：再多就该去用自动整理 / 全量刮削了
const fileJobMaxItems = 200

// validate 入队前的便宜校验（不发网络请求）
func (r *fileJobRequest) validate(roles map[string]string) error {
	r.Cid = strings.TrimSpace(r.Cid)
	if r.Cid == "" {
		r.Cid = "0"
	}
	if len(r.Items) == 0 {
		return errors.New("请先勾选要处理的文件或文件夹")
	}
	if len(r.Items) > fileJobMaxItems {
		return fmt.Errorf("一次最多处理 %d 项", fileJobMaxItems)
	}
	for _, it := range r.Items {
		if strings.TrimSpace(it.ID) == "" || strings.TrimSpace(it.Name) == "" {
			return errors.New("条目参数不完整，请刷新目录后重试")
		}
	}
	if r.TmdbID > 0 && r.MediaType != "movie" && r.MediaType != "tv" {
		return errors.New("media_type 只能是 movie 或 tv")
	}
	if r.TmdbID < 0 {
		return errors.New("TMDB 编号不正确")
	}
	if n := len(r.Chain); n > 0 && r.Chain[n-1].Cid != r.Cid {
		r.Chain = nil // 面包屑与目录对不上就不用它，执行时再按 Cookie 通道查
	}
	return nil
}

// fileJobTitle 任务标题里的条目摘要
func fileJobTitle(items []fileJobItem) string {
	if len(items) == 1 {
		return "《" + shortTitle(items[0].Name) + "》"
	}
	return fmt.Sprintf("《%s》等 %d 项", shortTitle(items[0].Name), len(items))
}

// fileJobDedupe 同一目录、同一批条目排着时只排一次
func fileJobDedupe(p fileJobParams) string {
	ids := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		ids = append(ids, it.ID)
	}
	sort.Strings(ids)
	return truncateStr(p.Cid+":"+strings.Join(ids, ","), 240)
}
