package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== 按来源分库：转存子目录绑定媒体库（2026-10-13） ====================
//
// 分类规则只看 TMDB 条目（类型、国家、语言…），看不出文件是不是 4K，按内容分库做得到、
// 按来源分库做不到。LitePan / MoviePilot 的用户习惯是「4K 订阅转存到 /转存/4K，这个目录对应 /4K库」。
//
// 做法：转存目录下的**直接子目录**可以绑一个媒体库（setting staging_libs）。
// 整理时把绑定子目录当成一个**独立的扫描根**（不是容器目录：容器会把「4K」当成识别的路径上下文），
// 选库优先级 来源子目录 > 分类规则的 library > 主库（同 MoviePilot get_dir：先按来源，再按类型与类别）。
// 分类目录照常按规则算，只是库换了。
//
// 选库认的是 cfg.Pending（当前扫描根）：确认入库时 rcfg.Pending = rec.SourceCid、
// 网盘文件页 rcfg.Pending = 所在目录，都自然沿用来源，不用另外传。
//
// 只支持一层子目录（维护者定的），也不许绑定转存目录本身。
// 子目录同时是工作区根：不被当条目整理、不被空目录清理删掉（workspaceRootCids / orgProtectedCids）。

// stagingLibsKey setting key：JSON 数组 [{cid, name, lib}]
const stagingLibsKey = "staging_libs"

// stagingLib 一个绑定了媒体库的转存子目录。Lib 存 cid 不存库名：这张表是界面里选出来的，
// 不像分类规则 YAML 要给人改；cid 不会因为在 115 上改名而失效
type stagingLib struct {
	Cid  string `json:"cid"`
	Name string `json:"name"` // 子目录名，界面回显与日志用（保存时按 115 刷新）
	Lib  string `json:"lib"`  // 媒体库 cid（主库或附加库）
}

// loadStagingLibs 读配置；解析不了当作没有（等于都进转存目录根的老行为）
func loadStagingLibs() []stagingLib {
	raw := strings.TrimSpace(settingValueCompat(stagingLibsKey))
	if raw == "" {
		return nil
	}
	var out []stagingLib
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("[整理] ✗ 转存子目录绑定配置解析失败，按没有处理: %v", err)
		return nil
	}
	return out
}

// stagingLibMap 子目录 cid → 绑定
func stagingLibMap(list []stagingLib) map[string]stagingLib {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]stagingLib, len(list))
	for _, s := range list {
		if s.Cid != "" {
			out[s.Cid] = s
		}
	}
	return out
}

// sourceLib 当前扫描根绑定的媒体库。没绑定返回 ok=false；绑定的库已移出配置时返回错误，
// 不退回主库或规则（同第 2 期：退回去等于把片子放进用户没指定的库）
func sourceLibOf(cfg *OrgConfig) (cid string, ok bool, err error) {
	s, bound := cfg.SubLibs[cfg.Pending]
	if !bound || cfg.Pending == "" {
		return "", false, nil
	}
	if cfg.SelfLibs[s.Lib] {
		return "", true, fmt.Errorf("转存子目录「%s」绑定的媒体库（cid=%s）已改成自己整理，本站不往里整理：到「账号与媒体库 → 媒体库」改绑或解绑", s.Name, s.Lib)
	}
	if !recordLibOK(cfg, s.Lib) || s.Lib == "" {
		return "", true, fmt.Errorf("转存子目录「%s」绑定的媒体库（cid=%s）已不在媒体库配置里：到「发现与订阅 → 设置 → 转存目录」改绑，或把库加回附加媒体库", s.Name, s.Lib)
	}
	return s.Lib, true, nil
}

// stagingEntry 转存目录里一个待整理的顶层条目，连同它所在的扫描根（转存目录本身或绑定子目录）
type stagingEntry struct {
	dirEntry
	Pending string
}

// orgLister 整理引擎列目录（一层），测试换成假的
func orgLister(ops *pan115Ops) func(cid string) ([]dirEntry, error) {
	return func(cid string) ([]dirEntry, error) { return listPendingTopLevel(ops, cid) }
}

// expandStagingEntries 转存目录的顶层清单 → 要整理的条目：工作区根跳过，绑定子目录展开成它里面一层。
// 子目录列不出来只记日志、跳过这个子目录（下一轮再来），不影响别的条目
func expandStagingEntries(list func(cid string) ([]dirEntry, error), cfg *OrgConfig, top []dirEntry, onLog func(string)) []stagingEntry {
	excluded := cfg.workspaceRootCids()
	var out []stagingEntry
	for _, e := range top {
		if e.IsDir {
			if s, ok := cfg.SubLibs[e.Cid]; ok {
				inner, err := list(e.Cid)
				if err != nil {
					onLog(fmt.Sprintf("✗ 读取转存子目录「%s」失败，本轮跳过: %v", s.Name, err))
					continue
				}
				for _, c := range inner {
					if c.IsDir && excluded[c.Cid] {
						continue
					}
					out = append(out, stagingEntry{dirEntry: c, Pending: e.Cid})
				}
				continue
			}
			if excluded[e.Cid] {
				onLog(fmt.Sprintf("○ 跳过整理工作区目录: %s/", e.Name))
				continue
			}
		}
		out = append(out, stagingEntry{dirEntry: e, Pending: cfg.Pending})
	}
	return out
}

// stagingWork 展开后的条目各自配好上下文：绑定子目录里的用 withPending 换扫描根，
// sink / pruner / held / handled 与根目录共用。等人工确认的条目去掉
func stagingWork(ctx *orgCtx, entries []stagingEntry) ([]dirEntry, []*orgCtx) {
	ctxs := map[string]*orgCtx{ctx.cfg.Pending: ctx}
	var es []dirEntry
	var cs []*orgCtx
	for _, se := range entries {
		c := ctxs[se.Pending]
		if c == nil {
			sub := *ctx.cfg
			sub.Pending = se.Pending
			c = ctx.withPending(&sub)
			ctxs[se.Pending] = c
		}
		if len(c.dropHeld([]dirEntry{se.dirEntry})) == 0 {
			continue
		}
		if se.IsDir && (subWrapperHeld(se.Fid) || subWrapperHeld(se.Cid)) {
			continue // 订阅这一轮还在往里转存，任务结束统一整理（subhold.go）
		}
		es = append(es, se.dirEntry)
		cs = append(cs, c)
	}
	return es, cs
}

// countStagingUnheld 转存目录里还有几个待整理的条目（守望者用）：绑定子目录本身不算，算它里面的。
// 否则空的「4K」子目录永远算一个条目，守望者会以为整理后仍未清空、一路计数到熔断。
// 代价是每个绑定子目录多列一次（读接口，走节流）
func countStagingUnheld(list func(cid string) ([]map[string]interface{}, error), entries []map[string]interface{}, subs map[string]stagingLib) (int, error) {
	if len(subs) == 0 {
		return countUnheld(entries), nil
	}
	var rest []map[string]interface{}
	n := 0
	for _, d := range entries {
		if fmt.Sprint(d["f"]) == "0" {
			if _, ok := subs[nilSprint(d["cid"])]; ok {
				inner, err := list(nilSprint(d["cid"]))
				if err != nil {
					return 0, err
				}
				n += countUnheld(inner)
				continue
			}
		}
		rest = append(rest, d)
	}
	return n + countUnheld(rest), nil
}

// listAllEntries 列一个目录的全部直接子项（分页列完）
func listAllEntries(ops *pan115Ops, cid string) ([]map[string]interface{}, error) {
	var out []map[string]interface{}
	offset := 0
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
		if len(raw) == 0 || offset+len(raw) >= count {
			return out, nil
		}
		offset += len(raw)
	}
}

// stagingTarget 提交时选的子目录 → 转存到哪个目录。空 / "root" 返回空：交给转存核心函数自己回落转存目录
// （没配转存目录的报错也由它们给，与原来一致）。只认绑定表里的子目录
func (h *Handler) stagingTarget(sub string) (string, error) {
	sub = strings.TrimSpace(sub)
	// 媒体库表（V2）：选的是库（lib:<id>），放进它的转存文件夹；没选按默认转存库（xfertarget.go）
	if libs := loadLibraries(); len(libs) > 0 {
		l, err := h.xferLib(sub, libs)
		if err != nil {
			return "", err
		}
		return xferTargetOf(l), nil
	}
	if sub == "" || sub == "root" {
		return "", nil
	}
	if s, ok := stagingLibMap(loadStagingLibs())[sub]; ok {
		return s.Cid, nil
	}
	return "", fmt.Errorf("所选子目录（cid=%s）不在转存子目录的绑定表里，可能已被移除，请重新选择", sub)
}

// ---- 接口 ----

// stagingLibView 界面上的一行：带上库名与是否失效
type stagingLibView struct {
	stagingLib
	LibName string `json:"lib_name"`
	Problem string `json:"problem,omitempty"`
}

// libChoice 可绑定的媒体库
type libChoice struct {
	Cid     string `json:"cid"`
	Name    string `json:"name"`
	Primary bool   `json:"primary"`
}

// libChoices 主库 + 附加库，名字走路径缓存（命中不打接口）
func (h *Handler) libChoices() []libChoice {
	cids := h.loadFullSyncCfg().libCids()
	cookie, _ := h.get115Cookie()
	out := make([]libChoice, 0, len(cids))
	for i, cid := range cids {
		name := libLocalName(cid)
		if name == "" {
			if row, ok := lookupCachedRow(cid); ok {
				name = row.Name
			} else {
				name = libDirName(cookie, cid)
			}
		}
		out = append(out, libChoice{Cid: cid, Name: name, Primary: i == 0})
	}
	return out
}

// stagingLibViews 绑定表 + 每行状态。零 115 请求：子目录在不在转存目录下看路径缓存，
// 缓存里没有的不下结论
func stagingLibViews(list []stagingLib, libs []libChoice, root string) []stagingLibView {
	names := map[string]string{}
	for _, l := range libs {
		names[l.Cid] = l.Name
	}
	out := make([]stagingLibView, 0, len(list))
	for _, s := range list {
		v := stagingLibView{stagingLib: s, LibName: names[s.Lib]}
		if _, ok := names[s.Lib]; !ok {
			v.Problem = "绑定的媒体库已不在媒体库配置里，这个子目录里的条目整理时会报错停下"
		} else if row, ok := lookupCachedRow(s.Cid); ok && root != "" && row.ParentID != root {
			v.Problem = "这个子目录已经不在转存目录下（换过转存目录或在网盘上挪走了），整理不会再扫它"
		}
		out = append(out, v)
	}
	return out
}

// GetStagingLibs GET /staging/libs
func (h *Handler) GetStagingLibs(c *gin.Context) {
	// 媒体库表（V2）：「转存到」选的是库，一个要整理的库一项（值 lib:<id>），不再有转存子目录绑定
	if all := loadLibraries(); len(all) > 0 {
		t := h.xferTargetsNow()
		items := []gin.H{}
		for _, v := range t["libs"].([]xferLibView) {
			name := v.Name
			if t["multi_account"].(bool) && v.AccountName != "" {
				name += "（" + v.AccountName + "）"
			}
			items = append(items, gin.H{"cid": v.Value, "name": name, "lib": v.Value, "lib_name": name, "account": v.Account})
		}
		c.JSON(http.StatusOK, gin.H{"root": "", "items": items, "libs": []libChoice{}, "v2": true,
			"default": t["default"], "need_pick": t["need_pick"]})
		return
	}
	libs := h.libChoices()
	list := loadStagingLibs()
	c.JSON(http.StatusOK, gin.H{
		"root":  h.shareFolderCid(),
		"items": stagingLibViews(list, libs, h.shareFolderCid()),
		"libs":  libs,
	})
}

// normStagingLibs 校验整份绑定表：子目录不重复、库在配置里、子目录是转存目录的直接子目录。
// dirOf 查目录（父目录与名字）；只为新增或改过的子目录调用，没变的零请求
func normStagingLibs(in []stagingLib, root string, libCids []string, old map[string]stagingLib,
	dirOf func(cid string) (dirInfo, error)) ([]stagingLib, error) {
	if root == "" {
		return nil, errors.New("先配置转存目录")
	}
	okLib := map[string]bool{}
	for _, l := range libCids {
		okLib[l] = true
	}
	seen := map[string]bool{}
	out := make([]stagingLib, 0, len(in))
	for _, s := range in {
		s.Cid, s.Lib = strings.TrimSpace(s.Cid), strings.TrimSpace(s.Lib)
		if s.Cid == "" {
			return nil, errors.New("子目录不能为空")
		}
		if s.Cid == root {
			return nil, errors.New("不能绑定转存目录本身，只能绑定它下面的子目录")
		}
		if seen[s.Cid] {
			return nil, fmt.Errorf("子目录「%s」绑定了两次", s.Name)
		}
		seen[s.Cid] = true
		if !okLib[s.Lib] {
			return nil, fmt.Errorf("子目录「%s」选的媒体库不在媒体库配置里", s.Name)
		}
		if prev, ok := old[s.Cid]; ok && prev.Name != "" {
			s.Name = prev.Name // 没变的子目录不再问 115
		} else {
			info, err := dirOf(s.Cid)
			if err != nil {
				return nil, fmt.Errorf("读不到子目录「%s」：%v", s.Name, err)
			}
			if info.pid != root {
				return nil, fmt.Errorf("「%s」不是转存目录的直接子目录（只支持一层）", firstNonEmpty(info.n, s.Name))
			}
			s.Name = info.n
		}
		out = append(out, s)
	}
	return out, nil
}

// inboxSelfLibErr 转存入口不能绑到自己整理的库（libopts.go：本站不往里整理）
func inboxSelfLibErr(items []stagingLib, self map[string]bool) error {
	for _, s := range items {
		if self[strings.TrimSpace(s.Lib)] {
			return fmt.Errorf("子目录「%s」选的媒体库是自己整理的库，本站不往里整理，不能做转存入口", s.Name)
		}
	}
	return nil
}

// SaveStagingLibs PUT /staging/libs：整份替换
func (h *Handler) SaveStagingLibs(c *gin.Context) {
	var req struct {
		Items []stagingLib `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := inboxSelfLibErr(req.Items, loadLibOpts().selfCids()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cookie, _ := h.get115Cookie()
	out, err := normStagingLibs(req.Items, h.shareFolderCid(), h.loadFullSyncCfg().libCids(),
		stagingLibMap(loadStagingLibs()), func(cid string) (dirInfo, error) {
			if cookie == "" {
				return dirInfo{}, errors.New("未登录 115")
			}
			return get115DirInfo(cookie, cid)
		})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	b, _ := json.Marshal(out)
	if err := h.Config.SaveSetting(stagingLibsKey, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[整理] ✓ 转存子目录绑定已保存：%d 个", len(out))
	libs := h.libChoices()
	c.JSON(http.StatusOK, gin.H{"root": h.shareFolderCid(), "items": stagingLibViews(out, libs, h.shareFolderCid()), "libs": libs})
}

// StagingSubdirs GET /staging/subdirs：转存目录下现有的子目录（选绑定用）
func (h *Handler) StagingSubdirs(c *gin.Context) {
	root := h.shareFolderCid()
	if root == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "先配置转存目录"})
		return
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	top, err := listPendingTopLevel(ops, root)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取转存目录失败: " + err.Error()})
		return
	}
	type sub struct {
		Cid  string `json:"cid"`
		Name string `json:"name"`
	}
	out := []sub{}
	for _, e := range top {
		if e.IsDir {
			out = append(out, sub{Cid: e.Cid, Name: e.Name})
		}
	}
	c.JSON(http.StatusOK, gin.H{"dirs": out})
}

// MkStagingSubdir POST /staging/subdirs：在转存目录下新建子目录（绑定前建）。
// 转存目录不在媒体库里，增量不管它，mkdir 不用登记事件抑制
func (h *Handler) MkStagingSubdir(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "子目录名不能为空，也不能带斜杠"})
		return
	}
	root := h.shareFolderCid()
	if root == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "先配置转存目录"})
		return
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cid, err := ops.mkdir(root, name)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "新建子目录失败: " + err.Error()})
		return
	}
	log.Printf("[整理] ✓ 转存目录下新建子目录「%s」", name)
	c.JSON(http.StatusOK, gin.H{"cid": cid, "name": name})
}

// stagingSubOptions 提交时可选的子目录（找资源 / 链接转存 / 订阅的下拉）
func stagingSubOptions() []stagingLib {
	list := loadStagingLibs()
	if list == nil {
		return []stagingLib{}
	}
	return list
}

// stagingTopCount 目录列表里不算绑定子目录的条目数（不看待确认，秒传试探只关心「有没有新东西」）
func stagingTopCount(entries []map[string]interface{}, subs map[string]stagingLib) int {
	n := 0
	for _, d := range entries {
		if fmt.Sprint(d["f"]) == "0" {
			if _, ok := subs[nilSprint(d["cid"])]; ok {
				continue
			}
		}
		n++
	}
	return n
}
