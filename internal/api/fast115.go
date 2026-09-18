package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 快速全量同步（Cookie 通道） ====================
//
// 普通模式逐目录 DFS，一个目录一次请求，串行节流下万级媒体库要跑几十分钟。
// 快速模式改用两个「整棵子树一次拿完」的接口，把请求数压到个位数：
//
//	① GET {webapi}/files?cid=..&show_dir=0     递归返回子树内所有文件（1150/页）
//	   —— show_dir=0 是递归开关：带它返回整棵子树的文件，不带（show_dir=1）
//	      则是普通的目录视图，只有直接子项
//	② GET proapi.115.com/app/chrome/downfolders 递归返回子树内所有目录（5000/页）
//	   —— 115 客户端自用端点，参数是目录的 pickcode，可由 cid 本地换算（见 pickcode115.go）
//
// ① 的条目只带父目录 cid 不带路径，② 正好补上 cid→(名字,父id) 映射，
// 两边在内存里拼出完整路径。1 万文件的库约 10 次请求。
//
// 代价：downfolders 是非公开端点，115 若改动会失效——调用方需要能降级回普通模式。

const (
	// fastFilePageSize 递归文件列表分页大小（115 上限 1150）
	fastFilePageSize = 1150
	// fastDirPageSize downfolders 分页大小（115 上限 5000）
	fastDirPageSize = 5000
	// fastMaxDirPages downfolders 最多翻多少页（500 万目录，防御性上限）
	fastMaxDirPages = 1000
	// fastMaxDepth 路径重建时向上回溯的最大层数（防御目录环）
	fastMaxDepth = 64
)

// fastDirNode 目录表的一项
type fastDirNode struct {
	name     string
	parentID string
}

// fetch115DownFolders 拉取整棵子树的目录表（cid → 名字+父id）
// dirPickcode 为子树根目录的 pickcode，由 dirPickcode115 本地算出
func fetch115DownFolders(cookie, dirPickcode string) (map[string]fastDirNode, error) {
	dirs := make(map[string]fastDirNode, 1024)
	for page := 1; page <= fastMaxDirPages; page++ {
		query := url.Values{
			"pickcode": {dirPickcode},
			"page":     {strconv.Itoa(page)},
			"per_page": {strconv.Itoa(fastDirPageSize)},
		}
		body, err := httpGet115UA("https://proapi.115.com/app/chrome/downfolders", query, cookie, ua115Unified(), 60*time.Second)
		if err != nil {
			return nil, fmt.Errorf("拉取目录表失败（第 %d 页）: %w", page, err)
		}
		var resp struct {
			State json.RawMessage `json:"state"`
			Error string          `json:"error"`
			Data  struct {
				List []struct {
					Fid string `json:"fid"`
					Fn  string `json:"fn"`
					Pid string `json:"pid"`
				} `json:"list"`
				HasNextPage bool `json:"has_next_page"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("解析目录表失败: %s", truncateStr(string(body), 200))
		}
		if !openStateOK(resp.State) {
			return nil, fmt.Errorf("115 拒绝目录表请求: %s", firstNonEmpty(resp.Error, truncateStr(string(body), 120)))
		}
		for _, d := range resp.Data.List {
			if d.Fid == "" {
				continue
			}
			dirs[d.Fid] = fastDirNode{name: d.Fn, parentID: d.Pid}
		}
		if !resp.Data.HasNextPage {
			return dirs, nil
		}
	}
	return nil, fmt.Errorf("目录表页数超过上限 %d，疑似接口异常", fastMaxDirPages)
}

// fetch115FlatPage 拉取一页递归文件列表（show_dir=0 → 整棵子树的文件）
// 返回条目、子树内文件总数、命中的镜像域名
func fetch115FlatPage(cookie, cid string, offset int) ([]map[string]interface{}, int, string, error) {
	query := url.Values{
		"aid":      {"1"},
		"cid":      {cid},
		"show_dir": {"0"}, // 递归开关：整棵子树的文件
		"o":        {"user_ptime"},
		"asc":      {"0"},
		"offset":   {strconv.Itoa(offset)},
		"limit":    {strconv.Itoa(fastFilePageSize)},
		"format":   {"json"},
	}
	var lastErr error
	for _, origin := range webapiFileOrigins {
		body, err := httpGet115UA(origin+"/files", query, cookie, ua115Unified(), 30*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		var result struct {
			State bool                     `json:"state"`
			Error string                   `json:"error"`
			Data  []map[string]interface{} `json:"data"`
			Count int                      `json:"count"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("解析递归文件列表失败: %s", truncateStr(string(body), 200))
			continue
		}
		if !result.State {
			lastErr = fmt.Errorf("115 返回错误: %s", result.Error)
			// Cookie 失效换镜像也没用
			if strings.Contains(result.Error, "登录") || strings.Contains(result.Error, "acc") {
				return nil, 0, "", lastErr
			}
			continue
		}
		return result.Data, result.Count, origin, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有镜像域名均不可用")
	}
	return nil, 0, "", lastErr
}

// list115SubtreeFast 快速模式取清单：递归列全部文件 + 整棵目录表，内存里拼路径。
// 参数与 walk115Dir 对齐，调用方可直接替换。
// basePath 为媒体库根目录名（作为 STRM 路径第一层）；assets 为 nil 时只收视频。
//
// 第一个返回值表示本次清单是否「完整」——翻页短缺、祖先链断裂都会置 false。
// 孤儿清理拿它当闸：清单不完整时算出来的「网盘已删除」可能是假的，一删就是真丢数据。
func list115SubtreeFast(cookie, rootCid, basePath string, videos, assets *[]remoteFile, f *syncFilter, skipCids map[string]bool) (bool, error) {
	// ① 首页：一次拿到总数 + 第一批数据 + 一个 pickcode（用来推不动点）
	first, total, origin, err := fetch115FlatPage(cookie, rootCid, 0)
	if err != nil {
		return false, fmt.Errorf("递归文件列表失败: %w", err)
	}
	if total == 0 || len(first) == 0 {
		log.Printf("[同步] ○ 快速模式：子树内没有文件（cid=%s）", rootCid)
		// 空库是个完整而合法的结果：调用方据此可以把整个台账判成孤儿
		return true, nil
	}
	vlog("[同步] 快速模式：子树内共 %d 个文件（%s）", total, origin)

	// ② 由任意一个文件 pickcode 反推账号不动点，算出根目录 pickcode（零请求）
	dirPickcode, err := rootDirPickcode(rootCid, first)
	if err != nil {
		return false, err
	}

	// ③ 整棵子树的目录表
	dirs, err := fetch115DownFolders(cookie, dirPickcode)
	if err != nil {
		return false, err
	}
	log.Printf("[同步] ○ 快速模式：目录表 %d 个目录，文件 %d 个（共约 %d 次请求）",
		len(dirs), total, 2+(total-1)/fastFilePageSize)

	// ④ 逐页收集（首页已在手），按 fid 去重：翻页期间有文件增删会导致条目错位
	seen := make(map[string]bool, total)
	res := &fastResolver{root: rootCid, base: basePath, dirs: dirs, skip: skipCids, cache: map[string]string{}}
	collect := func(entries []map[string]interface{}) {
		for _, d := range entries {
			rf, ok := fastEntryToFile(d)
			if !ok || seen[rf.Fid] {
				continue
			}
			seen[rf.Fid] = true
			relDir, ok := res.pathOf(fmt.Sprint(d["cid"]))
			if !ok {
				continue // 祖先链断了或命中排除目录，pathOf 已计数
			}
			rf.Path = relDir
			switch ext := strings.ToLower(path.Ext(rf.Name)); {
			case len(f.videoExts) > 0 && f.videoExts[ext]:
				if videos != nil {
					*videos = append(*videos, rf)
				}
			case len(f.assetExts) > 0 && f.assetExts[ext]:
				rf.IsAsset = true
				if assets != nil {
					*assets = append(*assets, rf)
				}
			}
		}
	}
	collect(first)
	for offset := len(first); offset < total; {
		entries, count, _, err := fetch115FlatPage(cookie, rootCid, offset)
		if err != nil {
			return false, fmt.Errorf("递归文件列表失败（offset=%d）: %w", offset, err)
		}
		if len(entries) == 0 {
			log.Printf("[同步] 快速模式：offset=%d 返回空页，提前结束（已收 %d/%d）", offset, len(seen), total)
			break
		}
		collect(entries)
		offset += len(entries)
		if count > 0 && count != total {
			// 同步期间库有增删，以首次总数为准继续翻完，去重兜底
			vlog("[同步] 快速模式：总数由 %d 变为 %d（同步期间库有变动）", total, count)
		}
	}

	if res.skipped > 0 {
		vlog("[同步] ○ 快速模式：按整理工作区排除 %d 个文件", res.skipped)
	}
	complete := true
	if res.orphan > 0 {
		log.Printf("[同步] ⚠ 快速模式：%d 个文件的父目录不在目录表中，已跳过（目录表可能不完整）", res.orphan)
		complete = false
	}
	if len(seen) < total {
		log.Printf("[同步] ⚠ 快速模式：实收 %d 个文件，接口报告 %d 个（差额可能来自翻页期间的库变动）", len(seen), total)
		complete = false
	}
	return complete, nil
}

// rootDirPickcode 从首页任意一个文件的 pickcode 推出账号不动点，再算根目录 pickcode
func rootDirPickcode(rootCid string, entries []map[string]interface{}) (string, error) {
	cid, err := strconv.ParseUint(strings.TrimSpace(rootCid), 10, 64)
	if err != nil {
		return "", fmt.Errorf("媒体库 cid 非法: %q", rootCid)
	}
	for _, d := range entries {
		pc := strings.TrimSpace(fmt.Sprint(d["pc"]))
		if pc == "" || pc == "<nil>" {
			continue
		}
		sp, err := stablePoint115(pc)
		if err != nil {
			continue // 这条推不出来换下一条
		}
		return dirPickcode115(cid, sp)
	}
	return "", fmt.Errorf("首页 %d 个条目中没有可用的 pickcode，无法推导目录 pickcode", len(entries))
}

// fastEntryToFile 把 webapi 条目转成 remoteFile（Path 留空，由调用方补）
func fastEntryToFile(d map[string]interface{}) (remoteFile, bool) {
	fid := strings.TrimSpace(fmt.Sprint(d["fid"]))
	name := strings.TrimSpace(fmt.Sprint(d["n"]))
	if fid == "" || fid == "<nil>" || name == "" || name == "<nil>" {
		return remoteFile{}, false
	}
	var size int64
	if s, ok := d["s"].(float64); ok {
		size = int64(s)
	}
	sha1 := fmt.Sprint(d["sha"])
	if sha1 == "<nil>" {
		sha1 = ""
	}
	return remoteFile{Fid: fid, Name: name, Size: size, PickCode: fmt.Sprint(d["pc"]), Sha1: sha1}, true
}

// fastResolver 由目录表把父目录 cid 解析成相对路径，逐级缓存
type fastResolver struct {
	root    string
	base    string
	dirs    map[string]fastDirNode
	skip    map[string]bool
	cache   map[string]string // cid → 相对路径；命中排除目录时不入缓存
	skipped int
	orphan  int
}

// pathOf 返回该目录对应的本地相对路径；命中整理工作区或祖先链断裂时返回 false
func (r *fastResolver) pathOf(cid string) (string, bool) {
	cid = strings.TrimSpace(cid)
	if cid == "" || cid == "<nil>" {
		r.orphan++
		return "", false
	}
	if p, ok := r.cache[cid]; ok {
		return p, true
	}
	// 自底向上回溯到子树根，沿途任一层命中排除目录即整条丢弃
	var names []string
	cur := cid
	for depth := 0; cur != r.root; depth++ {
		if depth >= fastMaxDepth {
			r.orphan++
			return "", false
		}
		if r.skip[cur] {
			r.skipped++
			return "", false
		}
		node, ok := r.dirs[cur]
		if !ok {
			r.orphan++
			return "", false
		}
		names = append(names, node.name)
		cur = node.parentID
	}
	rel := r.base
	for i := len(names) - 1; i >= 0; i-- {
		rel = path.Join(rel, names[i])
	}
	r.cache[cid] = rel
	return rel, true
}

// firstNonEmpty 返回第一个非空串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ==================== 模式选择 ====================

// fastSyncAvailable 快速模式是否可用。downfolders 只在 Cookie 域有，
// 开放平台没有对应端点；启用 OpenAPI 的账号一律走标准模式（不混用通道）
func (h *Handler) fastSyncAvailable() (bool, string) {
	if oc := h.getOpen115(); oc != nil && oc.authorized() {
		return false, "已启用 OPENAPI 通道，全量同步使用标准模式"
	}
	if ck, err := h.get115Cookie(); err != nil || strings.TrimSpace(ck) == "" {
		return false, "尚未绑定 115 Cookie"
	}
	return true, ""
}

// collectSyncFiles 按模式取全量清单，返回 (实际使用的模式, 清单是否完整, 错误)。
// 快速模式失败时自动降级：downfolders 是非公开端点，115 改动了也不该让整轮同步白跑。
// progress 为阶段提示回调，只有持有任务状态槽的调用方才传——否则会串写
// 别人正在展示的进度（整理库扫描跑在后台 goroutine 里，不持有任务槽）
func (h *Handler) collectSyncFiles(
	ops *pan115Ops, cookie, mode, cid, libName string,
	videos, assets *[]remoteFile, f *syncFilter, skipCids map[string]bool,
	progress func(string),
) (string, bool, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if mode == "fast" {
		if ok, reason := h.fastSyncAvailable(); !ok {
			log.Printf("[同步] ○ 已选快速模式但不可用（%s），改用标准模式", reason)
		} else {
			progress("快速模式：拉取递归文件列表与目录表…")
			// 只收视频的调用方（整理库扫描）会传 assets=nil，不能无脑解引用
			v, a := snapshotFiles(videos), snapshotFiles(assets)
			complete, err := list115SubtreeFast(cookie, cid, libName, videos, assets, f, skipCids)
			if err == nil {
				return "fast", complete, nil
			}
			// 降级前把半成品还原，避免与标准模式的结果叠加出重复条目
			restoreFiles(videos, v)
			restoreFiles(assets, a)
			log.Printf("[同步] ⚠ 快速模式失败（%v），降级为标准模式重跑", err)
		}
	}
	progress("正在遍历 115 媒体库（已发现视频可在日志查看）…")
	// 标准模式是全有全无的：任一目录列失败就直接返回错误，所以无错即完整
	if err := walk115Dir(ops, cid, libName, videos, assets, f, skipCids); err != nil {
		return "normal", false, err
	}
	return "normal", true, nil
}

// SyncCapabilities 前端据此决定是否显示「快速模式」选项
// GET /sync/capabilities
func (h *Handler) SyncCapabilities(c *gin.Context) {
	ok, reason := h.fastSyncAvailable()
	c.JSON(http.StatusOK, gin.H{"fast_available": ok, "reason": reason})
}

// snapshotFiles / restoreFiles 快速模式失败时回滚半成品；指针为 nil 表示调用方不收这一类
func snapshotFiles(p *[]remoteFile) []remoteFile {
	if p == nil {
		return nil
	}
	return *p
}

func restoreFiles(p *[]remoteFile, v []remoteFile) {
	if p != nil {
		*p = v
	}
}

// fullSyncMode 读用户在全量同步卡选的模式。整理库扫描等没有独立开关的
// 全库遍历共用这个选择——用户已经在那张卡上看过风控提示并做了决定
func (h *Handler) fullSyncMode() string {
	var cfg struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("full")), &cfg)
	if cfg.Mode == "fast" {
		return "fast"
	}
	return "normal"
}
