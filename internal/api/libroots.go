package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"115-station/internal/model"
)

// ==================== 多个 115 媒体库（2026-10-12，第 1 期：同步） ====================
//
// 原来只有一个媒体库（full.cid），现在可以再加几个（full.extra_libs）。
// 主库仍是 full.cid：老配置不用迁移，回退到旧版本也只是少同步几个库；
// 第 2 期整理按分类规则分库时，没指定库的规则也落在主库。
//
// 本地这一侧天生就支持多库：STRM 路径是 LocalPath/<库的本地目录名>/分类/片目，
// 台账 key、海报墙、Emby 路径映射都已经按「第一段是库名」处理。所以多库的硬约束只有一条：
// **本地目录名不能重名**，否则两个库落进同一棵本地目录树（保存时 checkWorkspaceSlots / planLibrariesSave 拦，
// 同步时 resolveLibRoots 再拦一道）。本地目录名没定过的库沿用 115 目录名（liblocal.go），
// 这种库在网盘上事后改名照样会撞上。
//
// 读库清单一律走这里（fullSyncCfg.libCids / libCidsOf），别再自己 Unmarshal full 取 cid。

// libRootEntry full.extra_libs 的一项
type libRootEntry struct {
	Cid  string `json:"cid"`
	Path string `json:"path,omitempty"` // 只为界面回显，同步以 cid 为准
}

// libCidsOf 主库 + 附加库的 cid：去空、去网盘根、去重，主库在前
func libCidsOf(primary string, extras []libRootEntry) []string {
	seen := map[string]bool{}
	var out []string
	add := func(cid string) {
		cid = strings.TrimSpace(cid)
		if cid == "" || cid == "0" || seen[cid] {
			return
		}
		seen[cid] = true
		out = append(out, cid)
	}
	add(primary)
	for _, e := range extras {
		add(e.Cid)
	}
	return out
}

// libCids 这份配置里的全部媒体库（主库在前）
func (c fullSyncCfg) libCids() []string { return libCidsOf(c.Cid, c.ExtraLibs) }

// otherLibCids 附加媒体库
func (c fullSyncCfg) otherLibCids() []string {
	all := c.libCids()
	if len(all) > 0 && all[0] == strings.TrimSpace(c.Cid) {
		return all[1:]
	}
	return all
}

// parseFullCfg 解析 full 配置原文；解析不了按零值（没配）处理
func parseFullCfg(raw string) fullSyncCfg {
	var cfg fullSyncCfg
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

// libRoot 解析过的一个媒体库
type libRoot struct {
	Cid  string
	Name string // 本地第一层：定过本地目录名用它，否则是 115 目录名（liblocal.go）
	// PanName 115 上现在的目录名，只给界面显示；认库、拼本地路径一律用 Name
	PanName string
	Abs     string // 网盘绝对路径
	// Self 自己整理的库（libopts.go）：本站不往里整理，分类规则选到它就报错
	Self bool
	// Default 「默认放这里」的库：没写 library 的规则落进它（libOpts.fallbackOf）。
	// 一个都没标时按第一个算（老调用方与测试只给首个库在前的清单）
	Default bool
	// Account 附加账号的库（acctlibs.go）：Cid 是 a{账号}:{cid}、Abs 为空、一定是 Self。
	// 本站不往它的网盘上写任何东西，panDirOfLocal 对它返回空
	Account uint
}

// resolveLibRoots 把库清单解析成名字与网盘路径（都走路径缓存，命中不打接口）。
//
// 只有一个库时沿用老行为：名字取不到也照常返回（库名为空的老部署照旧能用）。
// 多个库时名字是区分它们的唯一依据，取不到或重名一律报错，调用方据此停手，
// 不能把 B 库的内容按 A 库的位置去找
func resolveLibRoots(dirName, absPath func(cid string) string, cids []string) ([]libRoot, error) {
	out := make([]libRoot, 0, len(cids))
	byName := map[string]string{}
	for _, cid := range cids {
		r := libRoot{Cid: cid, Name: dirName(cid), Abs: absPath(cid)}
		if len(cids) > 1 {
			if r.Name == "" || r.Abs == "" {
				return nil, fmt.Errorf("媒体库目录（cid=%s）读不出名字或位置（可能已删除，或暂时连不上 115）", cid)
			}
			if prev, ok := byName[libLocalKey(r.Name)]; ok {
				return nil, fmt.Errorf("两个媒体库的本地目录名都是「%s」（cid=%s 与 %s），会落进同一个本地目录，请到「账号与媒体库 → 媒体库」给其中一个改本地目录名", r.Name, prev, cid)
			}
			byName[libLocalKey(r.Name)] = cid
		}
		out = append(out, r)
	}
	return out, nil
}

// libRootNamed 按本地第一层的库名找库。只有一个库时不看名字（老台账可能不带库名、或库改过名）
func libRootNamed(roots []libRoot, name string) (libRoot, bool) {
	if len(roots) == 1 {
		return roots[0], true
	}
	for _, r := range roots {
		if r.Name == name {
			return r, true
		}
	}
	return libRoot{}, false
}

// splitLibRel 本地相对路径（第一段是库名）→ 库名 + 库内路径
func splitLibRel(rel string) (lib, rest string) {
	rel = strings.Trim(strings.ReplaceAll(rel, `\`, "/"), "/")
	// 库的本地目录可以多级（电影/主号，MULTI-ACCOUNT-V2-PLAN.md §10.1）：先按媒体库表整段最长前缀认，
	// 认不出（表还空、或不在任何库里）再按第一段（老口径，老库的本地目录都是一段）
	if l, r, ok := libOfLocalRel(loadLibraries(), rel); ok {
		return normLibLocal(l.Local), r
	}
	lib, rest, _ = strings.Cut(rel, "/")
	return lib, rest
}

// resolveConfiguredLibRoots 当前配置的全部媒体库，按 Cookie 解析
func (h *Handler) resolveConfiguredLibRoots(cookie string) ([]libRoot, error) {
	// 媒体库表（libstore.go）：主号的库在前，名字就是本地目录（可以多级）
	libs := effectiveLibraries(h.getSettingValue)
	var cids []string
	localOf := map[string]string{}
	var extras []acctLibEntry
	for _, l := range libs {
		if l.Account != acctMain {
			extras = append(extras, acctLibEntry{Account: l.Account, Cid: l.Cid, Path: l.Path, Local: l.Local})
			continue
		}
		cids = append(cids, l.Cid)
		localOf[l.Cid] = l.Local
	}
	if len(cids) == 0 && len(extras) == 0 {
		return nil, fmt.Errorf("未配置媒体库目录")
	}
	roots, err := resolveLibRoots(func(cid string) string { return firstNonEmpty(localOf[cid], libDirName(cookie, cid)) },
		func(cid string) string { return absPathOf(cookie, cid) }, cids)
	if err != nil {
		return roots, err
	}
	// 别的账号的库（acctlibs.go）：只认本地名，没有网盘路径；本地名不能和别的库撞
	taken := map[string]string{}
	for _, r := range roots {
		if r.Name != "" {
			taken[libLocalKey(r.Name)] = r.Cid
		}
	}
	for _, r := range acctLibRoots(extras) {
		if prev, ok := taken[libLocalKey(r.Name)]; ok {
			return nil, fmt.Errorf("附加账号的库「%s」和 cid=%s 的库本地目录名相同，会落进同一个本地目录", r.Name, prev)
		}
		taken[libLocalKey(r.Name)] = r.Cid
		roots = append(roots, r)
	}
	markLibRoles(roots, loadLibOpts())
	return roots, nil
}

// markLibRoles 按 lib_opts 标出自己整理的库与「默认放这里」的库
func markLibRoles(roots []libRoot, opts libOpts) {
	cids := make([]string, len(roots))
	for i, r := range roots {
		cids[i] = r.Cid
	}
	fb := opts.fallbackOf(cids)
	for i := range roots {
		roots[i].Self = opts.of(roots[i].Cid).self()
		roots[i].Default = roots[i].Cid == fb
	}
}

// rawLibCid 库根在 115 上的目录 cid：别的账号的库 Cid 存的是 a{账号}:{cid}（acctLibKey）
func rawLibCid(r libRoot) string {
	if r.Account == acctMain {
		return r.Cid
	}
	if _, cid, ok := strings.Cut(r.Cid, ":"); ok {
		return cid
	}
	return r.Cid
}

// panDirOfLocal 本地相对目录（第一段是库名）→ 网盘绝对路径。
// 第一段对上哪个库就用哪个库；都对不上（监控目录不是本地媒体根、老台账不带库名）退回主库、整段照用，
// 与只有一个库时的老行为一致。按整段比库名：原来按字符串前缀剥，「影视2/…」会被当成「影视」剥成「2/…」
func panDirOfLocal(roots []libRoot, relDir string) string {
	relDir = strings.Trim(strings.ReplaceAll(relDir, `\`, "/"), "/")
	if relDir == "." {
		relDir = ""
	}
	if len(roots) == 0 {
		return ""
	}
	root, rest := roots[0], relDir
	lib, r := splitLibRel(relDir)
	for _, x := range roots {
		if x.Name != "" && x.Name == lib {
			root, rest = x, r
			break
		}
	}
	if root.Account != acctMain {
		return "" // 附加账号的库：本站不往它的网盘上写（acctlibs.go），调用方按「没有目标目录」跳过
	}
	abs := strings.TrimSuffix(root.Abs, "/")
	if abs == "" {
		return ""
	}
	if rest != "" {
		abs += "/" + rest
	}
	return abs
}

// ==================== 第 2 期：分类规则指定媒体库 ====================
//
// 分类规则 YAML 里一类可以写 library: <库的本地目录名或 cid>，这一类整理进那个库；不写进主库。
// 写目录名而不是 cid：YAML 是给人看、给人改的，cid 一串数字看不出是哪个库。
// 认的是本地目录名（libRoot.Name，liblocal.go）：定下来之后 115 上改名不会让规则认不出库
// 认不出（库被移出配置、改了名）一律报错停下，不退回主库 —— 退回去等于把片子放进用户没指定的库，
// 事后要挪还得重新整理一遍

// pickRuleLib 规则里的库值 → 库 cid。libs 第一个是主库；库值为空返回主库。
// 先按 cid 认，再按目录名认（目录名不唯一时报错，保存时 checkWorkspaceSlots 已经拦过同名）
func pickRuleLib(val string, libs []libRoot) (string, error) {
	val = strings.TrimSpace(val)
	if len(libs) == 0 {
		return "", fmt.Errorf("未配置媒体库目录")
	}
	if val == "" {
		for _, l := range libs {
			if l.Default {
				return l.Cid, nil
			}
		}
		if libs[0].Self {
			return "", errNoOrganizeLib
		}
		return libs[0].Cid, nil
	}
	for _, l := range libs {
		if l.Cid == val {
			return selfLibGuard(val, l.Cid, libs)
		}
	}
	found := ""
	for _, l := range libs {
		if l.Name != "" && l.Name == strings.Trim(val, "/") {
			if found != "" {
				return "", fmt.Errorf("分类规则指定的媒体库「%s」有不止一个同名目录", val)
			}
			found = l.Cid
		}
	}
	if found == "" {
		return "", fmt.Errorf("分类规则指定的媒体库「%s」不在媒体库配置里（到「账号与媒体库」添加附加媒体库，或改分类规则的 library）", val)
	}
	return selfLibGuard(val, found, libs)
}

// selfLibGuard 选中的库是自己整理的库就报错：本站不往里整理，退回默认库也不对（用户没让放那儿）
func selfLibGuard(val, cid string, libs []libRoot) (string, error) {
	for _, l := range libs {
		if l.Cid == cid && l.Self {
			return "", fmt.Errorf("分类规则指定的媒体库「%s」是自己整理的库，本站不往里整理（改分类规则的 library，或在「账号与媒体库」把它改成本站整理）", strings.Trim(val, "/"))
		}
	}
	return cid, nil
}

// recordLibCid 整理记录的 LibCid：主库记空（老记录都是空，口径一致），附加库记 cid
func recordLibCid(cid string) string {
	if cid == "" || cid == primaryLibCidCompat() {
		return ""
	}
	return cid
}

// primaryLibCidCompat 当前配置的主库 cid（不挂 Handler 的地方用）
func primaryLibCidCompat() string {
	return strings.TrimSpace(parseFullCfg(settingValueCompat("full")).Cid)
}

// ruleLibCid 分类规则的库值 → 库 cid（网盘文件页等没有整理上下文的地方用）。
// 没写就是主库，不发任何请求；写了才解析各库的名字（走路径缓存）
func (h *Handler) ruleLibCid(val string) (string, error) {
	cfg := h.loadFullSyncCfg()
	if strings.TrimSpace(val) == "" {
		if fb := loadLibOpts().fallbackOf(cfg.libCids()); fb != "" {
			return fb, nil
		}
		return "", errNoOrganizeLib
	}
	cookie, _ := h.get115Cookie()
	roots, err := h.resolveConfiguredLibRoots(cookie)
	if err != nil {
		return "", err
	}
	return pickRuleLib(val, roots)
}

// checkRuleLibraries 分类规则里写的 library 都要对得上一个媒体库（保存分类规则时调用）。
// 没有规则写 library 时零请求
func (h *Handler) checkRuleLibraries(rows []model.CategoryRule) error {
	var vals []string
	seen := map[string]bool{}
	for _, r := range rows {
		if v := strings.TrimSpace(r.Library); v != "" && !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	cookie, _ := h.get115Cookie()
	roots, err := h.resolveConfiguredLibRoots(cookie)
	if err != nil {
		return fmt.Errorf("分类规则写了 library，但认不出各媒体库：%v", err)
	}
	for _, v := range vals {
		if _, err := pickRuleLib(v, roots); err != nil {
			return err
		}
	}
	return nil
}
