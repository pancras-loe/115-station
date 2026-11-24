package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ==================== 指定目录同步 STRM（网盘文件页「同步 STRM」） ====================
//
// 2026-11-24 用户反馈：在 115 上给一个文件夹改了名，增量没反应，全量又太慢。
// 增量对目录改名事件（folder_rename）本来有处理，但 p115client 的生活事件文档写着
// 「115 并没有收集 复制文件 和 文件改名 的事件」，本站也从没用真实改名事件验证过 ——
// 事件不来，增量什么都做不了。所以给一个手动入口：只遍历用户点名的这个目录。
//
// 改名后的关键是按 fid 认（115 上改名 / 移动 fid 不变）：
//   - 台账里同一个 fid 的旧位置和这次遍历到的位置不在同一个目录 → 推出目录级改名
//     （`dirRenameRoots`，去掉两边共同的结尾几级），本地整个目录跟着搬（`relocateLocalDir`，
//     连同台账里没有的刮削产物一起），不在旁边再写一份；
//   - 搬不了的（本地新位置已经有了、跨库）逐个文件处理：新位置照常写，旧位置的 STRM 与集 NFO / 剧照删掉，
//     删空的旧目录收掉（`purgeStaleTitleDir` 的守卫：还有 STRM / 台账行就不动）；
//   - 台账里在这个目录下、这次没遍历到的：按失效 STRM 标记（同全量，打开了检测才标），由用户确认后删。
//
// 只读 115（列目录 + 附属文件取直链），不对网盘做任何写操作。任务 kind=dirsync，主队列、拿 taskMu。
// 只做主号的库：网盘文件页本身只浏览主号。

// dirSyncJobParams 入队参数。位置在执行时按 Cookie 通道重新查一次（排队期间目录可能又改了名），
// Chain 只在查不到祖先链时兜底（OpenAPI 独立模式）
type dirSyncJobParams struct {
	Cid   string        `json:"cid"`
	Name  string        `json:"name"`
	Chain []browseCrumb `json:"chain,omitempty"`
}

func init() {
	jobExecutors["dirsync"] = execDirSyncJob
}

// dirSyncTarget 一个目录在本地对应的位置
type dirSyncTarget struct {
	libCid   string
	libLocal string // 库的本地目录（STRM 第一层，可以多级）
	prefix   string // 这个目录的本地相对路径（含库目录）
}

// dirSyncBlock 这个目录能不能按目录同步（纯函数）。chain 是这个目录自己的祖先链（末元素是它）。
// 能的话返回库在链上的下标
func dirSyncBlock(chain []browseCrumb, roles map[string]string) (int, string) {
	if len(chain) == 0 {
		return -1, "网盘根目录不在任何媒体库里"
	}
	idx := -1
	for i, c := range chain {
		if roles[c.Cid] == "library" {
			idx = i // 取最深的那个：库之间不互相包含，保险起见
		}
	}
	if idx < 0 {
		return -1, "只能同步媒体库里的目录"
	}
	if idx == len(chain)-1 {
		return -1, "这是媒体库根目录：整库请用「Strm 管理 → 全量」，选这个库同步"
	}
	for _, c := range chain[idx+1:] {
		if r := roles[c.Cid]; r != "" {
			return -1, "这个目录在整理工作区（" + workspaceRoleText(r) + "）里，不生成 STRM"
		}
	}
	return idx, ""
}

// dirSyncTargetOf 按祖先链算本地位置。库的本地目录取媒体库表；表里没有（老部署）按 libDirName
func dirSyncTargetOf(chain []browseCrumb, idx int, libs []model.Library, cookie string) (dirSyncTarget, error) {
	libCid := chain[idx].Cid
	local := ""
	for _, l := range libs {
		if l.Cid == libCid && l.Account == acctMain {
			local = normLibLocal(l.Local)
			break
		}
	}
	if local == "" {
		local = libDirName(cookie, libCid)
	}
	if local == "" {
		return dirSyncTarget{}, errors.New("读不出媒体库的本地目录名，请到「账号与媒体库 → 媒体库」检查")
	}
	parts := []string{local}
	for _, c := range chain[idx+1:] {
		parts = append(parts, c.Name)
	}
	return dirSyncTarget{libCid: libCid, libLocal: local, prefix: path.Join(parts...)}, nil
}

// SyncDirStrm POST /files/115/sync-strm {cid, name, chain} → 入队同步这个目录的 STRM
func (h *Handler) SyncDirStrm(c *gin.Context) {
	var req dirSyncJobParams
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Cid) == "" || req.Cid == "0" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：请选一个媒体库里的目录"})
		return
	}
	req.Cid = strings.TrimSpace(req.Cid)
	if n := len(req.Chain); n > 0 && req.Chain[n-1].Cid != req.Cid {
		req.Chain = nil
	}
	// 入队前按面包屑便宜地判一次（不发请求），执行时再按网盘上的真实位置判
	if req.Chain != nil {
		if _, why := dirSyncBlock(req.Chain, h.workspaceRoles()); why != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": why})
			return
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" && len(req.Chain) > 0 {
		name = req.Chain[len(req.Chain)-1].Name
	}
	req.Name = name
	job, err := enqueueJob(h.DB, jobSpec{Kind: "dirsync", Title: "同步 STRM《" + shortTitle(firstNonEmpty(name, req.Cid)) + "》",
		DedupeKey: "dirsync:" + req.Cid, Source: "web", Priority: jobPriorityManual,
		Params: jobParams{DirSync: &req}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "目录同步")
}

func execDirSyncJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).DirSync
	if p == nil || p.Cid == "" {
		return jobOutcome{}, errors.New("任务参数错误：缺少目录")
	}
	r, err := h.executeDirSync(*p)
	if err != nil {
		return jobOutcome{}, err
	}
	parts := []string{fmt.Sprintf("「%s」视频 %d 个（新增 STRM %d，已存在 %d", r.prefix, r.videos, r.st.StrmCreated, r.st.StrmExisting)}
	if r.st.StrmFailed > 0 {
		parts[0] += fmt.Sprintf("，失败 %d", r.st.StrmFailed)
	}
	parts[0] += fmt.Sprintf("），附属 %d 个（下载 %d）", r.assets, r.st.AssetsDownloaded)
	for _, m := range r.moved {
		parts = append(parts, fmt.Sprintf("本地目录跟着改名：%s → %s", m.from, m.to))
	}
	if r.movedFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d 个文件换了位置，旧位置已清掉", r.movedFiles))
	}
	switch {
	case r.orphans > 0:
		parts = append(parts, fmt.Sprintf("网盘上已经没有的 %d 个已标成失效 STRM，到「Strm 管理」确认后删除", r.orphans))
	case r.orphanSkipped > 0:
		parts = append(parts, fmt.Sprintf("台账里还有 %d 个网盘上已经没有的（失效 STRM 检测没开，没有标记）", r.orphanSkipped))
	}
	return jobOutcome{Message: strings.Join(parts, "；"), Partial: r.st.StrmFailed+r.st.AssetsFailed > 0, Result: map[string]any{
		"prefix": r.prefix, "videos": r.videos, "created": r.st.StrmCreated, "moved_dirs": len(r.moved),
		"moved_files": r.movedFiles, "orphans": r.orphans,
	}}, nil
}

// dirSyncResult 一次目录同步的结果
type dirSyncResult struct {
	prefix         string
	videos, assets int
	st             applyStats
	moved          []dirMove
	movedFiles     int
	orphans        int // 这次标成失效的（含以前就标着的）
	orphanSkipped  int // 检测没开，台账里还有、网盘上已经没有的
	deleted        []string
	refresh        []string
	newStrms       []string
}

func (h *Handler) executeDirSync(p dirSyncJobParams) (dirSyncResult, error) {
	if prob := mediaRootProblem(); prob != "" {
		return dirSyncResult{}, errors.New(prob)
	}
	localRoot := mediaRoot()
	setJobProgress("定位目录", progressKeep, progressKeep, "")
	chain, err := h.resolveBrowseChain(p.Cid, p.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return dirSyncResult{}, errors.New("目录已不存在（可能被删除或移动了），请回网盘文件页刷新后重选")
		}
		return dirSyncResult{}, fmt.Errorf("查目录位置失败: %w", err)
	}
	idx, why := dirSyncBlock(chain, h.workspaceRoles())
	if why != "" {
		return dirSyncResult{}, errors.New(why)
	}
	cookie, _ := h.get115Cookie()
	t, err := dirSyncTargetOf(chain, idx, loadLibraries(), cookie)
	if err != nil {
		return dirSyncResult{}, err
	}
	skipCids, _ := h.orgSkipCids(t.libCid)
	if skipCids[p.Cid] {
		return dirSyncResult{}, errors.New("这个目录是整理工作区，不生成 STRM")
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return dirSyncResult{}, err
	}
	cfg := h.loadFullSyncCfg()
	filter := &syncFilter{
		videoExts: buildExtSet(cfg.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, cfg.ImageExt...), cfg.DataExt...)),
	}
	filter.assetExts[".nfo"] = true

	log.Printf("[同步] ▶ 指定目录同步开始：%s（cid=%s）", t.prefix, p.Cid)
	var videos, assets []remoteFile
	// 只走标准模式：快速模式拉的是整棵树的导出，一个目录用不着
	if _, _, err := h.collectSyncFiles(ops, cookie, "normal", p.Cid, t.prefix, &videos, &assets, filter, skipCids,
		func(s string) { setJobProgress(s, progressKeep, progressKeep, "") }); err != nil {
		return dirSyncResult{}, fmt.Errorf("遍历 115 目录失败: %w", err)
	}
	setJobProgress(fmt.Sprintf("落盘：视频 %d + 附属 %d", len(videos), len(assets)), progressKeep, progressKeep, "")
	r := h.applyDirSync(ops, localRoot, t.libLocal, t.prefix, videos, assets)

	// 本次新写的（不是改名搬过来的）按库的刮削方式自动刮，刷新交给刮削任务（同增量）
	prefixAbs := filepath.Join(localRoot, filepath.FromSlash(r.prefix))
	handedOff := enqueueSyncScrape(h.DB, r.newStrms, prefixAbs)
	if !handedOff && len(r.refresh) > 0 {
		notifyEmbyPaths(dedupeStrings(r.refresh), embyRefreshAdded)
	}
	if len(r.deleted) > 0 {
		notifyEmbyDeleted(dedupeStrings(r.deleted)...)
	}
	log.Printf("[同步] ✓ 指定目录同步完成：%s，视频 %d（新增 STRM %d，已存在 %d），附属下载 %d，目录跟着改名 %d 个，文件换位置 %d 个，失效 %d",
		r.prefix, r.videos, r.st.StrmCreated, r.st.StrmExisting, r.st.AssetsDownloaded, len(r.moved), r.movedFiles, r.orphans)
	return r, nil
}

// applyDirSync 遍历结果落盘（不发 115 读目录请求；附属文件要下载时取直链）。
// 顺序：按 fid 认出目录改名 → 本地目录跟着搬 → 照常落盘 → 收掉还留在旧位置的单个文件 → 失效标记
func (h *Handler) applyDirSync(ops *pan115Ops, localRoot, libLocal, prefix string, videos, assets []remoteFile) dirSyncResult {
	r := dirSyncResult{prefix: prefix, videos: len(videos), assets: len(assets)}
	all := make([]remoteFile, 0, len(videos)+len(assets))
	all = append(append(all, videos...), assets...)
	fids := make([]string, 0, len(all))
	for _, f := range all {
		fids = append(fids, f.ledgerFid())
	}

	// 1. 目录级改名：台账里的旧目录 → 这次遍历到的目录
	before := h.ledgerRelsOf(fids)
	var pairs [][2]string
	for _, f := range all {
		if old, ok := before[f.ledgerFid()]; ok {
			pairs = append(pairs, [2]string{path.Dir(old), f.Path})
		}
	}
	for _, m := range dirRenameRoots(pairs, libLocal, prefix) {
		if h.relocateLocalDir(m.from, m.to, localRoot) {
			r.moved = append(r.moved, m)
			abs := filepath.Join(localRoot, filepath.FromSlash(m.to))
			markEmbyRenamed(abs)
			r.refresh = append(r.refresh, abs)
			r.deleted = append(r.deleted, filepath.Join(localRoot, filepath.FromSlash(m.from)))
		}
	}
	if len(r.moved) > 0 {
		before = h.ledgerRelsOf(fids)
	}

	// 2. 照常落盘：已经搬到位的内容一致，算「已存在」
	domain, format, keepExt, skipExist := h.getStrmConfig()
	r.st = applySyncResults(h.DB, ops, videos, assets, localRoot, domain, format, keepExt, skipExist, "")

	// 3. 还在旧位置的单个文件：新位置写好了（台账已换成新路径），旧的删掉
	after := h.ledgerRelsOf(fids)
	// 台账里原来就有的 fid 不是新片（改名搬过来、或换了直链配置重写的），同步后自动刮削不收
	echo := map[string]bool{}
	type staleDir struct{ dir, stop string }
	var staleDirs []staleDir
	for _, fid := range fids {
		old, had := before[fid]
		now, ok := after[fid]
		if had && ok {
			echo[now] = true
		}
		if !had || !ok || old == now {
			continue
		}
		if h.ledgerRelTaken(old) {
			continue // 旧路径已被别的文件占着（同基名换写法等），不删
		}
		abs := filepath.Join(localRoot, filepath.FromSlash(old))
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			log.Printf("[同步] ○ 清理旧位置失败 %s: %v", old, err)
			continue
		}
		dropStrmCompanions(localRoot, old)
		r.movedFiles++
		r.deleted = append(r.deleted, abs)
		markEmbyRenamed(filepath.Join(localRoot, filepath.FromSlash(now)))
		// 往上收到改了名的那一级为止（「剧/A/Season 1」→「剧/B/Season 1」收到「剧/A」）；只是文件改名就只看它所在的目录
		oldDir := path.Dir(old)
		stop, _ := renameRootOf(oldDir, path.Dir(now))
		if stop == "" {
			stop = oldDir
		}
		staleDirs = append(staleDirs, staleDir{oldDir, stop})
	}
	libAbs := filepath.Join(localRoot, filepath.FromSlash(libLocal))
	done := map[string]bool{}
	for _, sd := range staleDirs {
		// 只收这次搬空的旧目录：purgeStaleTitleDir 只在没有 STRM、台账里也没有行时才删元数据
		if done[sd.dir] || !strings.HasPrefix(sd.stop, libLocal+"/") {
			continue
		}
		done[sd.dir] = true
		for d := sd.dir; ; d = path.Dir(d) {
			purgeStaleTitleDir(localRoot, d)
			if d == sd.stop || !strings.HasPrefix(d, sd.stop+"/") {
				break
			}
		}
		removeEmptyParents(filepath.Join(localRoot, filepath.FromSlash(sd.dir)), libAbs)
	}
	if r.movedFiles > 0 {
		r.refresh = append(r.refresh, filepath.Join(localRoot, filepath.FromSlash(prefix)))
	}
	if r.st.StrmCreated+r.st.AssetsDownloaded > 0 {
		r.refresh = append(r.refresh, filepath.Join(localRoot, filepath.FromSlash(prefix)))
	}
	for _, rel := range r.st.Written {
		if !echo[rel] {
			r.newStrms = append(r.newStrms, rel)
		}
	}

	// 4. 台账里在这个目录下、这次没见到的：同全量，打开检测才标失效，删除由用户确认
	seen := make(map[string]bool, len(fids))
	for _, fid := range fids {
		seen[fid] = true
	}
	if h.orphanDetectEnabled() {
		_, _, r.orphans = markOrphans(h.DB, prefix, seen)
	} else {
		r.orphanSkipped = h.ledgerUnseenUnder(prefix, seen)
	}
	return r
}

// dirMove 一次目录级改名（本地相对路径，含库目录）
type dirMove struct{ from, to string }

// dirRenameRoots 由「台账旧目录 → 这次遍历到的目录」推出该整个搬的目录（纯函数）。
//
// 每一对去掉两边共同的结尾几级（「剧/A/Season 1」→「剧/B/Season 1」得「剧/A」→「剧/B」），
// 剩下的就是改了名的那一级。只认：两边都在同一个库里且不是库目录本身、新位置在这次同步的目录里面
// （往上的不认：那一层这次没遍历全，搬了会把没见过的东西一起搬走）；
// 一个旧目录对上几个新目录、或几个旧目录并进同一个新目录的，都不搬，交给逐个文件处理。
// 嵌套的只留最浅的（搬了上层，下层已经跟着到位）
func dirRenameRoots(pairs [][2]string, libLocal, prefix string) []dirMove {
	inLib := func(p string) bool { return strings.HasPrefix(p, libLocal+"/") }
	inPrefix := func(p string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
	to := map[string]string{}
	bad := map[string]bool{}
	for _, pr := range pairs {
		oldDir, newDir := pr[0], pr[1]
		if oldDir == newDir {
			continue
		}
		from, dst := renameRootOf(oldDir, newDir)
		if from == "" || dst == "" || !inLib(from) || !inLib(dst) || !inPrefix(dst) {
			continue
		}
		if prev, ok := to[from]; ok && prev != dst {
			bad[from] = true
		}
		to[from] = dst
	}
	// 几个旧目录并进同一个新目录：搬第一个之后后面的目标已存在，干脆都不搬
	byDst := map[string]int{}
	for from, dst := range to {
		if !bad[from] {
			byDst[dst]++
		}
	}
	var out []dirMove
	for from, dst := range to {
		if !bad[from] && byDst[dst] == 1 {
			out = append(out, dirMove{from: from, to: dst})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.Count(out[i].from, "/"), strings.Count(out[j].from, "/"); a != b {
			return a < b
		}
		return out[i].from < out[j].from
	})
	var kept []dirMove
	for _, m := range out {
		nested := false
		for _, k := range kept {
			if strings.HasPrefix(m.from, k.from+"/") || strings.HasPrefix(m.to, k.to+"/") {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, m)
		}
	}
	return kept
}

// renameRootOf 两个目录去掉共同的结尾几级，剩下的就是改了名 / 挪了位置的那一级（相同时返回空）
func renameRootOf(oldDir, newDir string) (string, string) {
	if oldDir == newDir {
		return "", ""
	}
	o, n := strings.Split(oldDir, "/"), strings.Split(newDir, "/")
	for len(o) > 0 && len(n) > 0 && o[len(o)-1] == n[len(n)-1] {
		o, n = o[:len(o)-1], n[:len(n)-1]
	}
	return strings.Join(o, "/"), strings.Join(n, "/")
}

// ledgerRelsOf 主号台账里这些 fid 现在的相对路径
func (h *Handler) ledgerRelsOf(fids []string) map[string]string {
	out := make(map[string]string, len(fids))
	for i := 0; i < len(fids); i += 400 {
		end := i + 400
		if end > len(fids) {
			end = len(fids)
		}
		var rows []model.SyncedFile
		h.DB.Select("file_id", "rel_path").Where("account = ? AND file_id IN ?", acctMain, fids[i:end]).Find(&rows)
		for _, r := range rows {
			out[r.FileID] = r.RelPath
		}
	}
	return out
}

// ledgerRelTaken 台账里有没有行占着这个路径
func (h *Handler) ledgerRelTaken(rel string) bool {
	var n int64
	h.DB.Model(&model.SyncedFile{}).Where("rel_path = ?", rel).Count(&n)
	return n > 0
}

// ledgerUnseenUnder 台账里在这个目录下、这次没见到的行数（失效检测关着时只报个数）
func (h *Handler) ledgerUnseenUnder(prefix string, seen map[string]bool) int {
	var rows []model.SyncedFile
	h.DB.Select("file_id").Where(`rel_path LIKE ? ESCAPE '\'`, likeEscape(prefix)+"/%").Find(&rows)
	n := 0
	for _, r := range rows {
		if !seen[r.FileID] {
			n++
		}
	}
	return n
}
