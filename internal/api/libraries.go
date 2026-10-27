package api

import (
	"fmt"
	"net/http"
	"path"
	"os"
	"path/filepath"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 媒体库卡片（2026-10-15，MULTI-LIB-SIMPLIFY-PLAN.md） ====================
//
// 「账号与媒体库 → 媒体库」按库一张卡片：库在哪（115 目录、本地目录）、从哪进（转存入口 = 绑定的转存子目录）、
// 哪些分类规则落进来（只读，改去二级分类）、Emby 有没有覆盖它。存储一概不动：
// full.extra_libs、staging_libs、分类 YAML 的 library 还是原来那三份，这里只是一个汇总读、一个合并写。
//
// 分类归属**只在二级分类里改**（§0.5：卡片上只剩一个分类名，条件、顺序看不到，用户会以为是在定义「什么是动漫」）。
// 卡片保存只在移除库时碰 YAML：删掉指向它的 library，否则整理到这一类会报错停下（categorylib.go）。
//
// GET 零 115 请求：库名与位置一律读路径缓存，缓存没有的退回配置里存的显示路径（选目录时带来的），
// 都没有就空着，界面显示 cid。

// libraryView 一张库卡片
type libraryView struct {
	Cid   string `json:"cid"`
	Name  string `json:"name"`  // 本地目录名 = 本地第一层（liblocal.go；没定过时是 115 目录名）；认不出为空
	Path  string `json:"path"`  // 网盘绝对路径；认不出为空
	Local string `json:"local"` // 本地目录（本地根 / 库名）；库名认不出为空
	// PanName 115 上现在的目录名（只给界面显示，和 Name 不同时提示一句）
	PanName string `json:"pan_name"`
	// LocalLocked 台账里已经有这个库的内容：本地目录名不能再改（librariessave.go 的 planLibLocals 同口径）
	LocalLocked bool `json:"local_locked"`
	// Account 附加账号的库（acctlibs.go）：账号 id（主号为 0）、账号名；Cid 是 a{账号}:{cid}，RawCid 是它在那个账号里的目录 id
	Account     uint   `json:"account"`
	AccountName string `json:"account_name,omitempty"`
	RawCid      string `json:"raw_cid,omitempty"`
	// LastSync 上一次全量里这个库的结果（libsyncstat.go）；没同步过为空
	LastSync *libSyncStat `json:"last_sync,omitempty"`
	Default  bool         `json:"default"` // 「默认放这里」的库（没写 library 的规则落进它），阶段 3 起不一定是首个库
	// First 首个媒体库（full.cid）：115 目录在「媒体库位置」里改、不能移除
	First bool `json:"first"`
	// Mode 谁来整理：organize（本站整理）/ self（我自己整理），Depth 自己整理的库片目在第几层（libopts.go）
	Mode  string `json:"mode"`
	Depth int    `json:"depth"`
	// Scrape 刮不刮：auto / manual / off（没设过的按老的全局开关换算出来的值，libopts.go）
	Scrape string `json:"scrape"`
	// Rules 落进这个库的分类规则（默认库：没写 library 的那些）
	Rules   []ruleLibRef     `json:"rules"`
	Inboxes []stagingLibView `json:"inboxes"`
	Emby    *libEmbyState    `json:"emby,omitempty"`
}

// libEmbyState 这个库在 Emby 里有没有被某个媒体库覆盖（只读，§3.7）
type libEmbyState struct {
	Covered bool `json:"covered"` // 某个 Emby 媒体库的目录就是它或它的上层
	Partial bool `json:"partial"` // 只覆盖了它下面的一部分（比如按分类建的 Emby 库，但有分类没建）
	// Missing 按分类建 Emby 库时还没进 Emby 的分类目录（能换算时是 Emby 路径，否则本地路径）
	Missing []string `json:"missing,omitempty"`
	// Unmapped 路径映射的本地一侧不包含这个库：Emby 看到的路径换不回本地，入库刷新、深删、对账都对不上
	Unmapped bool `json:"unmapped"`
	// Suggest 应该在 Emby 里添加的路径（按路径映射换算）；换算不出为空
	Suggest string `json:"suggest,omitempty"`
}

// libRenameFix 规则里写的库名认不出，但能认出是哪个库改了名（库在 115 上改名后配置里留的旧显示路径对得上）
type libRenameFix struct {
	From string `json:"from"` // YAML 里写的旧名
	To   string `json:"to"`   // 现在的库名
	Lib  string `json:"lib"`  // 库 cid
}

// libNameSource 一个库的名字与位置：本地名定过就用它（liblocal.go），网盘一侧路径缓存优先，其次配置里存的显示路径
type libNameSource struct {
	Cid      string
	Shown    string // 配置里存的显示路径（full.cid_path / extra_libs[].path），可能过时
	CachedOK bool
	Cached   model.PathCache
	Local    string // lib_opts 里定下的本地目录名；空 = 老行为，本地第一层跟 115 目录名走
	// Account 附加账号的库（acctlibs.go）：Cid 是 a{账号}:{cid}，Local 必有，没有路径缓存，网盘路径只给显示
	Account uint
	// Lib 媒体库表里的那一条（libstore.go）；老配置换算出来的没有 ID
	Lib model.Library
}

// libRootOf 名字与网盘路径。缓存里的是 115 现在的样子；显示路径是选目录那一刻的样子。
// Name 是本地第一层：定过本地名用本地名，否则就是 115 目录名
func (s libNameSource) libRootOf() libRoot {
	r := libRoot{Cid: s.Cid}
	if s.Account != acctMain {
		// Abs 留空：panDirOfLocal 据此不往附加账号的网盘上写
		r.Name, r.Account, r.Self = s.Local, s.Account, true
		if p := strings.TrimRight(strings.TrimSpace(s.Shown), "/"); p != "" {
			r.PanName = path.Base(p)
		}
		return r
	}
	if s.CachedOK {
		r.PanName, r.Abs = s.Cached.Name, s.Cached.Path
		if r.PanName == "" && r.Abs != "" {
			r.PanName = path.Base(r.Abs)
		}
	} else if p := strings.TrimRight(strings.TrimSpace(s.Shown), "/"); p != "" && strings.HasPrefix(p, "/") {
		r.PanName, r.Abs = path.Base(p), p
	}
	r.Name = r.PanName
	if s.Local != "" {
		r.Name = s.Local
	}
	return r
}

// staleName 配置里显示路径的目录名与 115 现在的名字不同时，返回旧名（库在 115 上改过名）。
// 定过本地名的库不算：规则 library 认的是本地名，115 上改名碰不到它
func (s libNameSource) staleName() string {
	if !s.CachedOK || s.Local != "" {
		return ""
	}
	p := strings.TrimRight(strings.TrimSpace(s.Shown), "/")
	if p == "" || !strings.HasPrefix(p, "/") {
		return ""
	}
	if old := path.Base(p); old != "" && old != s.Cached.Name {
		return old
	}
	return ""
}

// libNameSources 当前生效的各库（libstore.go 的 effectiveLibraries：媒体库表，表还空时按老配置换算），零 115 请求。
// fullRaw 只在表还空时用来换算（老调用方都传当前的 full 配置）
func libNameSources(fullRaw string) []libNameSource {
	libs := effectiveLibraries(func(k string) string {
		if k == "full" {
			return fullRaw
		}
		return settingValueCompat(k)
	})
	out := make([]libNameSource, 0, len(libs))
	for _, l := range libs {
		s := libNameSource{Cid: libKeyOf(l), Shown: l.Path, Local: l.Local, Account: l.Account, Lib: l}
		if l.Account == acctMain {
			s.Cached, s.CachedOK = lookupCachedRow(l.Cid)
		}
		out = append(out, s)
	}
	return out
}

// legacyLibNameSources 老配置（full / lib_opts / acct_libs）里的各库：迁移与表还空时的换算用，别的地方别再调
func legacyLibNameSources(fullRaw string) []libNameSource {
	cfg := parseFullCfg(fullRaw)
	shown := map[string]string{strings.TrimSpace(cfg.Cid): jsonStrField(fullRaw, "cid_path")}
	for _, e := range cfg.ExtraLibs {
		shown[strings.TrimSpace(e.Cid)] = e.Path
	}
	cids := cfg.libCids()
	opts := loadLibOpts()
	out := make([]libNameSource, 0, len(cids))
	for _, cid := range cids {
		s := libNameSource{Cid: cid, Shown: shown[cid], Local: strings.TrimSpace(opts.of(cid).Local)}
		s.Cached, s.CachedOK = lookupCachedRow(cid)
		out = append(out, s)
	}
	// 附加账号的库排在主号的库后面（acctlibs.go）
	for _, e := range loadAcctLibs() {
		out = append(out, libNameSource{Cid: e.key(), Shown: e.Path, Local: e.Local, Account: e.Account})
	}
	return out
}

// libRenameFixes 认不出的规则里，哪些写的正好是某个库改名前的名字（零请求：比的是配置里存的旧显示路径）
func libRenameFixes(orphans []ruleLibRef, sources []libNameSource) []libRenameFix {
	stale := map[string]libNameSource{}
	for _, s := range sources {
		if old := s.staleName(); old != "" {
			stale[old] = s
		}
	}
	seen := map[string]bool{}
	var out []libRenameFix
	for _, o := range orphans {
		v := strings.Trim(strings.TrimSpace(o.Library), "/")
		s, ok := stale[v]
		if !ok || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, libRenameFix{From: v, To: s.Cached.Name, Lib: s.Cid})
	}
	return out
}

// libEmbyCoverage 一个库的本地目录在 Emby 里的覆盖情况。locations 是各 Emby 媒体库的目录（Emby 路径），
// cats 是落进这个库的分类目录（相对库根，即分类规则名）。
// 很多人在 Emby 里按分类各建一个库（电影 / 剧集 / 动漫），没有一个 Emby 库是这个库目录本身：
// 只要每个分类目录都被某个 Emby 库覆盖，就算覆盖了，别再提示「整库新建」（2026-10-15 现场误报）。
// 纯函数：路径映射的换算都走 embyPathToLocal / embyPathRoots，与入库刷新、深删同一套
func libEmbyCoverage(pathMapping string, locations []string, libLocal string, cats []string) libEmbyState {
	var st libEmbyState
	lib := strings.TrimRight(filepath.ToSlash(libLocal), "/")
	if lib == "" {
		return st
	}
	locals := make([]string, 0, len(locations))
	for _, loc := range locations {
		if local := strings.TrimRight(filepath.ToSlash(embyPathToLocal(pathMapping, loc)), "/"); local != "" {
			locals = append(locals, local)
		}
	}
	coveredBy := func(dir string) bool {
		for _, local := range locals {
			if local == dir || strings.HasPrefix(dir, local+"/") {
				return true
			}
		}
		return false
	}
	st.Covered = coveredBy(lib)
	if !st.Covered {
		for _, local := range locals {
			if strings.HasPrefix(local, lib+"/") {
				st.Partial = true
			}
		}
	}
	_, embyRoot := embyPathRoots(pathMapping)
	mapped := strings.Contains(pathMapping, "#") && embyRoot != ""
	toEmby := func(dir string) (string, bool) { return localToEmbyPath(pathMapping, dir) }
	if st.Partial && len(cats) > 0 {
		seen := map[string]bool{}
		var missing []string
		for _, c := range cats {
			if c = libSubPath(c); c == "" || seen[c] {
				continue
			}
			seen[c] = true
			dir := lib + "/" + c
			if coveredBy(dir) {
				continue
			}
			if e, ok := toEmby(dir); ok {
				dir = e
			}
			missing = append(missing, dir)
		}
		if len(seen) > 0 && len(missing) == 0 {
			st.Covered, st.Partial = true, false
		} else {
			st.Missing = missing
		}
	}
	if e, ok := toEmby(lib); ok {
		if mapped || !st.Covered {
			st.Suggest = e
		}
	} else {
		st.Unmapped = true
	}
	return st
}

// localToEmbyPath 本地路径（/ 分隔）换成 Emby 里该填的路径；路径映射的本地一侧不包含它时换算不出、返回 false。
// 和 mapLocalToEmbyPath 的区别：那个换不出时原样返回（刷新通知拿本地路径试一次也无妨），
// 这里要告诉用户「换不出」—— Emby 建库、覆盖检查拿原样的本地路径去填就是错的
func localToEmbyPath(pathMapping, dir string) (string, bool) {
	localRoot, embyRoot := embyPathRoots(pathMapping)
	if !strings.Contains(pathMapping, "#") || embyRoot == "" {
		return dir, true // 没配映射：两边看到的是同一个路径
	}
	lr := strings.TrimRight(localRoot, "/")
	switch {
	case dir == lr:
		return embyRoot, true
	case lr != "" && strings.HasPrefix(dir, lr+"/"):
		return embyRoot + dir[len(lr):], true
	}
	return "", false
}

// localSubdirs 本地目录下一层的子目录名（跳过 . 开头的，如 .actors）；读不了返回 nil
func localSubdirs(abs string) []string {
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// librariesView GET /libraries 的全部内容
type librariesView struct {
	Libs        []libraryView  `json:"libs"`
	OrphanRules []ruleLibRef   `json:"orphan_rules"`
	RenameFixes []libRenameFix `json:"rename_fixes"`
	// NamesKnown 各库名字都认得出（缓存或配置里有）。认不出时按名字写的规则会被误算成「认不出的库」，
	// 界面据此不下结论（同 libleft.go：认不准宁可不说）
	NamesKnown  bool   `json:"names_known"`
	StagingRoot string `json:"staging_root"`
	LocalRoot   string `json:"local_root"`
	// Emby 整体状态：没配 / 读失败时给原因，库卡片上就不显示覆盖情况
	EmbyConfigured bool   `json:"emby_configured"`
	EmbyError      string `json:"emby_error,omitempty"`
}

// buildLibrariesView 组装库卡片。embyLocs 为 nil 表示没读 Emby（没配或读失败）
func buildLibrariesView(sources []libNameSource, rows []model.CategoryRule, bindings []stagingLib,
	stagingRoot, localRoot, pathMapping string, embyLocs []string, opts libOpts) librariesView {
	roots := make([]libRoot, len(sources))
	choices := make([]libChoice, len(sources))
	known := true
	for i, s := range sources {
		roots[i] = s.libRootOf()
		choices[i] = libChoice{Cid: s.Cid, Name: roots[i].Name, Primary: i == 0}
		if roots[i].Name == "" {
			known = false
		}
	}
	markLibRoles(roots, opts) // 没写 library 的规则归「默认放这里」的库，不一定是首个库
	byLib, orphans := rulesByLib(rows, roots)
	if !known {
		orphans = nil // 名字认不全时按名字写的规则认不出是正常的，不报
	}
	views := stagingLibViews(bindings, choices, stagingRoot)
	inboxes := map[string][]stagingLibView{}
	for _, v := range views {
		inboxes[v.Lib] = append(inboxes[v.Lib], v)
	}
	root := strings.TrimRight(filepath.ToSlash(localRoot), "/")
	out := librariesView{
		Libs:        make([]libraryView, 0, len(sources)),
		OrphanRules: orphans,
		RenameFixes: libRenameFixes(orphans, sources),
		NamesKnown:  known,
		StagingRoot: stagingRoot,
		LocalRoot:   root,
	}
	for i, r := range roots {
		v := libraryView{Cid: r.Cid, Name: r.Name, PanName: r.PanName, Path: r.Abs, Default: r.Default, First: i == 0, Mode: "organize",
			Rules: byLib[r.Cid], Inboxes: inboxes[r.Cid]}
		if r.Name != "" && root != "" {
			v.Local = root + "/" + r.Name
		}
		if r.Account != acctMain {
			v.Account, v.Path = r.Account, sources[i].Shown
			_, v.RawCid, _ = splitAcctLibKey(r.Cid)
		}
		if v.Rules == nil {
			v.Rules = []ruleLibRef{}
		}
		if v.Inboxes == nil {
			v.Inboxes = []stagingLibView{}
		}
		if embyLocs != nil && v.Local != "" {
			cats := make([]string, 0, len(v.Rules))
			for _, rr := range v.Rules {
				cats = append(cats, rr.Name)
			}
			st := libEmbyCoverage(pathMapping, embyLocs, v.Local, cats)
			v.Emby = &st
		}
		out.Libs = append(out.Libs, v)
	}
	if out.OrphanRules == nil {
		out.OrphanRules = []ruleLibRef{}
	}
	if out.RenameFixes == nil {
		out.RenameFixes = []libRenameFix{}
	}
	return out
}

// librariesNow 当前状态（GET 与 PUT 的响应共用）。零 115 请求；Emby 配了才读一次它的媒体库列表
func (h *Handler) librariesNow() librariesView {
	var rows []model.CategoryRule
	if h.DB != nil {
		h.DB.Where("media_type IN ? AND scheme_id = ?", []string{"movie", "tv"}, primarySchemeID()).Order("priority ASC").Find(&rows)
	}
	cfg, embyOK := loadEmbyRefreshCfg()
	var locs []string
	embyErr := ""
	if embyOK {
		if folders, err := embyVirtualFolders(cfg); err != nil {
			embyErr = err.Error()
		} else {
			locs = []string{}
			for _, f := range folders {
				locs = append(locs, f.Locations...)
			}
		}
	}
	v := buildLibrariesView(libNameSources(h.getSettingValue("full")), rows, loadStagingLibs(),
		h.shareFolderCid(), localMediaRoot(), cfg.PathMapping, locs, loadLibOpts())
	v.EmbyConfigured, v.EmbyError = embyOK, embyErr
	// 「115 上改了名」的修复只对还没定本地名的老库有意义：按老配置认（媒体库表里的库本地名都定了）
	if fixes := libRenameFixes(v.OrphanRules, legacyLibNameSources(h.getSettingValue("full"))); len(fixes) > 0 {
		v.RenameFixes = fixes
	}
	opts := loadLibOpts()
	legacy := loadLegacyLibScrape()
	syncStats := parseLibSyncStats(h.getSettingValue(libSyncStatsKey))
	for i := range v.Libs {
		v.Libs[i].Scrape = opts.of(v.Libs[i].Cid).Scrape
		if v.Libs[i].Scrape == "" {
			v.Libs[i].Scrape = legacy
		}
		if o := opts.of(v.Libs[i].Cid); o.self() {
			v.Libs[i].Mode, v.Libs[i].Depth = libModeSelf, o.depth()
		}
		v.Libs[i].LocalLocked = ledgerHasLib(v.Libs[i].Name)
		if st, ok := syncStats[v.Libs[i].Cid]; ok {
			v.Libs[i].LastSync = &st
		}
		if a := v.Libs[i].Account; a != acctMain && h.DB != nil {
			var s model.Storage
			if h.DB.Select("name", "user_name").First(&s, a).Error == nil {
				v.Libs[i].AccountName = acctLabel(s)
			} else {
				v.Libs[i].AccountName = fmt.Sprintf("已删除的账号 %d", a)
			}
		}
	}
	return v
}

// GetLibraries GET /libraries
func (h *Handler) GetLibraries(c *gin.Context) {
	c.JSON(http.StatusOK, h.librariesNow())
}
