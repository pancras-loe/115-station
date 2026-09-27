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
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 网盘文件页：媒体库内的片目（整理 / 移出） ====================
//
// 媒体库里只有「片目目录」（分类目录的下一层，见 ledger.go 的 libCategoryLayout）能整理和移动，
// 更深的季目录、单集只能刮削，更浅的库根 / 分类目录什么都不能动 —— 一点就是整个分类。
//
// 整理：不走新文件的整理流水线（洗版查重会撞上它自己、把它搬进「已存在」，旧 STRM 也没人收拾），
// 而是现场列出片目里的文件、建一条整理记录，交给「重新整理」（redoOrganize）：
// 它先算布局再动网盘、按 fid 清旧产物、落点没变就原地刷新。
// 做法对照 MoviePilot 的手动整理：库内文件按「目标路径」找回整理历史再重整；
// 我们按片目新建一条记录，旧记录里的同一批 fid 由 dropClaimedFiles 规则自然让出。
//
// 移动：只能移到 冗余 / 已存在 / 待整理 三个工作区根下。移出媒体库时本地产物要自己收拾
// （整理类写操作登记了事件抑制，增量同步不会替我们清，§6.8），顺序见 cleanupMovedTitle。

func init() {
	jobExecutors["libredo"] = execLibRedoJob
	jobExecutors["filemove"] = execFileMoveJob
}

// isCategoryOrAncestor 这个库内相对路径是分类目录，或是某个多级分类的上级（电视剧 之于 电视剧/日番）
func (l libCategoryLayout) isCategoryOrAncestor(rel string) bool {
	if _, ok := l[rel]; ok {
		return true
	}
	for c := range l {
		if strings.HasPrefix(c, rel+"/") {
			return true
		}
	}
	return false
}

// isTitleRel 库内相对路径（不含库名）是不是片目目录：父目录是分类，自己不是分类也不是分类的上级
func (l libCategoryLayout) isTitleRel(rel string) bool {
	rel = strings.Trim(rel, "/")
	parent := path.Dir(rel)
	if rel == "" || parent == "." {
		return false
	}
	if _, ok := l[parent]; !ok {
		return false
	}
	return !l.isCategoryOrAncestor(rel)
}

// libTitleOf 勾选的条目是不是媒体库里的片目目录：是则返回库名与库内相对路径（不含库名）
func libTitleOf(chain []browseCrumb, roles map[string]string, layout libCategoryLayout, it fileJobItem) (libName, rel string, ok bool) {
	idx := chainRoleIndex(chain, roles, "library")
	if idx < 0 || !it.IsDir {
		return "", "", false
	}
	libName, rel = itemLibRel(chain, idx, it)
	return libName, rel, layout.isTitleRel(rel)
}

// libCategories 当前分类目录列表（前端据此判断哪一行是片目目录）
func libCategories(l libCategoryLayout) []string {
	out := make([]string, 0, len(l))
	for c := range l {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ---- 媒体库内整理 ----

// enqueueLibRedo 媒体库里的片目目录「整理」→ 入队（OrganizeFiles 按位置分派过来）
func (h *Handler) enqueueLibRedo(c *gin.Context, req fileJobRequest, roles map[string]string) {
	if len(req.Items) != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "媒体库里的片目一次只能整理一部"})
		return
	}
	it := req.Items[0]
	if _, _, ok := libTitleOf(req.Chain, roles, loadLibCategoryLayout(), it); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "媒体库里只有分类目录下的片目目录能整理"})
		return
	}
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := "重新整理《" + shortTitle(it.Name) + "》"
	if req.TmdbID > 0 {
		title += " → " + pickLabel(pickReq{TmdbID: req.TmdbID, MediaType: req.MediaType, Label: req.Label})
	}
	fp := req.fileJobParams
	fp.Scrape, fp.Target = nil, ""
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: "libredo", Title: title, DedupeKey: "libtitle:" + it.ID,
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{TmdbID: req.TmdbID, MediaType: req.MediaType, Files: &fp},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "整理")
}

func execLibRedoJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	fp := p.Files
	if fp == nil || len(fp.Items) != 1 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	it := fp.Items[0]
	defer resetFileListCache()

	tc, err := loadTmdbClient()
	if err != nil {
		return jobOutcome{}, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return jobOutcome{}, err
	}
	chain, err := h.resolveBrowseChain(fp.Cid, fp.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return jobOutcome{}, errors.New("片目所在的目录已不存在（被删除或移动了），请刷新后重选")
		}
		return jobOutcome{}, err
	}
	layout := loadLibCategoryLayout()
	_, rel, ok := libTitleOf(chain, h.workspaceRoles(), layout, it)
	if !ok {
		return jobOutcome{}, errors.New("它已经不是媒体库里的片目目录了（被挪走，或分类策略改过），请刷新后重选")
	}

	// 片目里的文件以网盘为准现场列一遍：台账可能过时，fid / pickcode / sha1 都要最新的
	setJobProgress("读取片目", 0, 0, it.Name)
	files, err := collectDirFiles(ops, it.ID, it.Name)
	if err != nil {
		return jobOutcome{}, fmt.Errorf("读取片目失败: %w", err)
	}
	rec := libTitleRecord(it, fp.Cid, rel, layout[path.Dir(rel)], files)
	if rec.VideoCount == 0 {
		return jobOutcome{}, errors.New("片目里没有视频文件")
	}

	tmdbID, mediaType := p.TmdbID, p.MediaType
	if tmdbID <= 0 {
		// 自动：目录名里的编号标签最硬（parseFileName 摘 {tmdbid=…} 等），没有再按片名搜；
		// 结果与现状一致时 redoOrganize 判为原地刷新，只重建 STRM 与元数据
		parsed := parseFileName(it.Name)
		parsed.IsTV = parsed.IsTV || rec.MediaType == "tv"
		media, err := tc.recognize(parsed)
		if err != nil || media == nil {
			return jobOutcome{}, errors.New("识别不出 TMDB 条目：请改用「指定 TMDB 条目」")
		}
		tmdbID, mediaType = media.TmdbID, media.MediaType
		if mediaType == "" {
			mediaType = rec.MediaType
		}
	}

	if id, _ := currentJob(); id != 0 {
		rec.JobID = id
	}
	if err := h.DB.Create(&rec).Error; err != nil {
		return jobOutcome{}, fmt.Errorf("建立整理记录失败: %w", err)
	}
	log.Printf("[整理] ▶ 媒体库片目重新整理《%s》（%s，%d 个文件）", it.Name, rel, len(files))
	if err := h.redoOrganize(&rec, tmdbID, mediaType); err != nil {
		h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(withJobID(map[string]interface{}{
			"status": "failed", "stage": "move", "message": truncateStr("媒体库片目重新整理失败: "+err.Error(), 480),
		}))
		return jobOutcome{}, err
	}
	return jobOutcome{Message: rec.Message, Result: map[string]any{"record_id": rec.ID}}, nil
}

// libTitleRecord 为库内片目建一条整理记录（纯函数）：状态记为「已入库」、目标就是它现在的位置，
// 这样重新整理才能判断「落点没变 → 原地刷新」，旧编号也能用来清理认错的媒体库条目
func libTitleRecord(it fileJobItem, parentCid, rel, mediaType string, files []remoteFile) model.OrganizeRecord {
	title, year, tmdb := parseTitleDir(it.Name)
	rec := model.OrganizeRecord{
		BatchID: time.Now().Format("20060102150405"),
		Source:  it.Name + "/", SourceKind: "dir", SourceCid: parentCid,
		Status: "success", Message: "媒体库片目（网盘文件页发起的整理）",
		TmdbID: tmdb, Title: title, Year: year, MediaType: mediaType,
		Category: path.Dir(rel), TargetDir: rel, TargetCid: it.ID,
	}
	rfs := make([]orgRecordFile, 0, len(files))
	hasEpisode := false
	for _, f := range files {
		kind := recordFileKind(f.Name)
		rfs = append(rfs, orgRecordFile{Fid: f.Fid, Name: f.Name, Dir: f.Path, Kind: kind,
			PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1})
		if kind == "video" {
			rec.VideoCount++
			rec.TotalSize += f.Size
			if parseFileName(f.Name).Episode > 0 {
				hasEpisode = true
			}
		}
	}
	if rec.MediaType == "" { // 电影与剧集同名分类：按文件判断
		rec.MediaType = map[bool]string{true: "tv", false: "movie"}[hasEpisode]
	}
	rec.Files = marshalRecordFiles(rfs)
	return rec
}

// ---- 移动到 冗余 / 已存在 / 待整理 ----

// fileMoveTargets 允许的移动目标（工作区角色）
var fileMoveTargets = map[string]bool{"redundant": true, "existing": true, "pending": true}

// fileMoveMax 一次最多移动多少项：每项都要清本地产物，媒体库里的还要通知 Emby
const fileMoveMax = 50

// MoveFiles POST /files/move → 入队，202
// body: {cid, chain, items:[{id,name,is_dir}], target: redundant|existing|pending}
func (h *Handler) MoveFiles(c *gin.Context) {
	var req fileJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	roles := h.workspaceRoles()
	if err := req.validate(roles); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Items) > fileMoveMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多移动 %d 项，请分批", fileMoveMax)})
		return
	}
	if _, err := roleCid(roles, req.Target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 面包屑说得清位置时当场校验，别排一次队再失败
	layout := loadLibCategoryLayout()
	for _, it := range req.Items {
		if reason := moveItemBlock(req.Chain, roles, layout, it, req.Target); reason != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("「%s」%s", it.Name, reason)})
			return
		}
	}
	fp := req.fileJobParams
	fp.Scrape = nil
	job, err := enqueueJob(h.DB, jobSpec{
		Kind:      "filemove",
		Title:     "移动" + fileJobTitle(req.Items) + " → " + workspaceRoleText(req.Target),
		DedupeKey: fileJobDedupe(fp) + ">" + req.Target,
		Source:    "web", Priority: jobPriorityManual,
		Params: jobParams{Files: &fp},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "移动")
}

// roleCid 工作区角色 → cid
func roleCid(roles map[string]string, role string) (string, error) {
	if !fileMoveTargets[role] {
		return "", errors.New("只能移动到 冗余 / 已存在 / 待整理")
	}
	for cid, r := range roles {
		if r == role {
			return cid, nil
		}
	}
	return "", fmt.Errorf("还没有配置%s目录（自动整理 → 基础配置）", workspaceRoleText(role))
}

// moveItemBlock 这一项为什么不能移（空 = 能移）。纯函数，入队与执行各查一次
func moveItemBlock(chain []browseCrumb, roles map[string]string, layout libCategoryLayout, it fileJobItem, target string) string {
	if roles[it.ID] != "" {
		return "是整理工作区目录，不能移动"
	}
	if zone, _ := chainZone(chain, roles); zone == target {
		return "已经在" + workspaceRoleText(target) + "目录里了"
	}
	if chainRoleIndex(chain, roles, "library") >= 0 {
		if _, _, ok := libTitleOf(chain, roles, layout, it); !ok {
			return "不是片目目录：媒体库里只有分类目录下的片目目录能移动"
		}
	}
	return ""
}

// fileMoveResult 移动任务的结果（任务详情里显示）
type fileMoveResult struct {
	Moved   []string `json:"moved"`
	Skipped []string `json:"skipped,omitempty"`
	Cleaned int      `json:"cleaned"` // 移出媒体库时清掉的本地台账行
}

func execFileMoveJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	fp := decodeJobParams(job).Files
	if fp == nil || len(fp.Items) == 0 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	defer resetFileListCache()

	ops, err := h.newPan115Ops()
	if err != nil {
		return jobOutcome{}, err
	}
	// 搬出媒体库这一步必须登记事件抑制：本地产物由这里收拾，
	// 增量同步若再按「目录被移走」处理一遍，会把刚清的台账与别处的同名路径搅在一起
	ops.suppress = true
	chain, err := h.resolveBrowseChain(fp.Cid, fp.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return jobOutcome{}, errors.New("所选条目所在的目录已不存在（被删除或移动了），请刷新后重选")
		}
		return jobOutcome{}, err
	}
	roles := h.workspaceRoles()
	targetCid, err := roleCid(roles, fp.Target)
	if err != nil {
		return jobOutcome{}, err
	}
	layout := loadLibCategoryLayout()
	targetText := workspaceRoleText(fp.Target)

	// 目标目录里已有的名字：同名就不移，交给 115 处理重名（报错、改名还是合并）都说不准
	setJobProgress("检查目标目录", 0, len(fp.Items), targetText)
	existing, _, err := listFileEntries(ops, targetCid, fileListLimit)
	if err != nil {
		return jobOutcome{}, fmt.Errorf("读取%s目录失败: %w", targetText, err)
	}
	taken := map[string]bool{}
	for _, e := range existing {
		taken[e.Name] = true
	}
	// 工作区根目录的祖先不能移：那等于把整理工作区整个搬走（Cookie 通道拿得到祖先链时才查得了）
	rootAncestors := h.workspaceRootAncestors(roles)
	held := loadAwaiting()

	res := fileMoveResult{}
	type libMove struct {
		it           fileJobItem
		libName, rel string
	}
	var ids []string
	var libs []libMove
	for _, it := range fp.Items {
		reason := moveItemBlock(chain, roles, layout, it, fp.Target)
		switch {
		case reason != "":
		case rootAncestors[it.ID]:
			reason = "包含整理工作区目录，不能移动"
		case held[it.ID] != nil:
			reason = "正在等人工确认：请先在整理记录里确认或忽略"
		case taken[it.Name]:
			reason = targetText + "目录里已有同名的文件或文件夹"
		}
		if reason != "" {
			res.Skipped = append(res.Skipped, it.Name+"："+reason)
			continue
		}
		taken[it.Name] = true // 这一批里两项同名：只移第一项
		ids = append(ids, it.ID)
		if libName, rel, ok := libTitleOf(chain, roles, layout, it); ok {
			libs = append(libs, libMove{it: it, libName: libName, rel: rel})
		}
		res.Moved = append(res.Moved, it.Name)
	}
	for _, s := range res.Skipped {
		log.Printf("[移动] ○ 跳过 %s", s)
	}
	if len(ids) == 0 {
		return jobOutcome{Result: res}, errors.New("没有可以移动的条目：" + strings.Join(res.Skipped, "；"))
	}

	// 网盘一次移完（写接口 3 秒节流，逐项移会白等），成功之后才动本地
	setJobProgress("移动", 0, len(ids), targetText)
	if err := ops.moveFiles(targetCid, ids); err != nil {
		return jobOutcome{Result: res}, fmt.Errorf("移动失败（网盘与本地都没有改动）: %w", err)
	}
	log.Printf("[移动] ✓ %d 项 → %s：%s", len(ids), targetText, truncateStr(strings.Join(res.Moved, "、"), 200))

	localRoot := localMediaRoot()
	for i, lm := range libs {
		setJobProgress("清理本地产物", i, len(libs), lm.it.Name)
		res.Cleaned += cleanupMovedTitle(localRoot, lm.libName, lm.rel, lm.it.ID, targetText)
	}
	setJobProgress("", len(ids), len(ids), "")

	msg := fmt.Sprintf("已移动 %d 项到%s", len(ids), targetText)
	if len(libs) > 0 {
		msg += fmt.Sprintf("（其中 %d 部移出媒体库，本地 STRM 与元数据已清理）", len(libs))
	}
	if fp.Target == "pending" {
		msg += "；下一轮自动整理会重新识别它们"
	}
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf("；%d 项未移动：%s", len(res.Skipped), truncateStr(strings.Join(res.Skipped, "；"), 200))
	}
	return jobOutcome{Message: msg, Result: res}, nil
}

// workspaceRootAncestors 所有工作区根目录的祖先 cid（不含根自身）。取不到祖先链的就不查
func (h *Handler) workspaceRootAncestors(roles map[string]string) map[string]bool {
	out := map[string]bool{}
	cookie, err := h.get115Cookie()
	if err != nil || cookie == "" {
		return out
	}
	for cid := range roles {
		chain, err := fetch115Ancestors(cookie, cid)
		if err != nil {
			continue
		}
		for _, a := range chain {
			if a.cid != cid {
				out[a.cid] = true
			}
		}
	}
	return out
}

// cleanupMovedTitle 片目已从网盘媒体库移走之后收拾本地，返回清掉的台账行数。
//
// 顺序有讲究：**先删台账，再删本地文件，最后通知 Emby**。本地 STRM 一删，Emby 会发 library.deleted，
// 开着深度删除的话它按台账匹配网盘源文件 —— 台账还在就会把刚移出去的片子从网盘删掉。
func cleanupMovedTitle(localRoot, libName, rel, titleCid, targetText string) int {
	key := strings.Trim(libName+"/"+rel, "/")
	var rows []model.SyncedFile
	model.DB.Where(`rel_path LIKE ? ESCAPE '\'`, likeEscape(key)+"/%").Find(&rows)
	if len(rows) > 0 {
		ids := make([]uint, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		model.DB.Where("id IN ?", ids).Delete(&model.SyncedFile{})
	}
	invalidateLedgerTitles()

	if dir, ok := movedTitleLocalDir(localRoot, key); ok {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[移动] ✗ 清理本地目录失败 %s: %v", dir, err)
		} else {
			removeEmptyParents(filepath.Dir(dir), localRoot)
			go notifyEmbyDeleted(dir)
		}
	}
	// 仪表盘的库存台账（MediaLibrary.TargetPath 是库内相对路径，不含库名）
	model.DB.Where(`target_path LIKE ? ESCAPE '\'`, likeEscape(rel)+"/%").Delete(&model.MediaLibrary{})
	// 把它整理进来的那些记录：标明已经移出去了，免得记录页还说它在媒体库里。
	// target_cid 一并清空：留着的话，之后对这条记录「重新整理」会被 isInPlaceRedo 判成原地刷新，
	// 只在本地重写 STRM、不把文件搬回媒体库；清空后它会按模板重建目录、把文件搬回来
	model.DB.Model(&model.OrganizeRecord{}).Where("target_cid = ?", titleCid).Updates(withJobID(map[string]interface{}{
		"stage": "moved", "message": "已移出媒体库 → " + targetText, "target_cid": "",
	}))
	log.Printf("[移动] ○ 已清理《%s》的本地产物（台账 %d 行）", path.Base(rel), len(rows))
	return len(rows)
}

// movedTitleLocalDir 片目在本地媒体库的目录；路径不在本地根的真子孙里（算错了）就不删
func movedTitleLocalDir(localRoot, key string) (string, bool) {
	if localRoot == "" || len(strings.Split(key, "/")) < 3 { // 库名 / 分类 / 片目，至少三段
		return "", false
	}
	root := filepath.Clean(localRoot)
	dir := filepath.Clean(filepath.Join(root, filepath.FromSlash(key)))
	if !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}

// invalidateLedgerTitles 台账片目缓存作废（台账行被整片删掉之后）
func invalidateLedgerTitles() {
	ledgerTitlesMu.Lock()
	ledgerTitlesCache = nil
	ledgerTitlesMu.Unlock()
}
