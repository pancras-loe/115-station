package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 网盘文件页：片目里的单集「指定季集」 ====================
//
// 媒体库里平时只有片目目录能整理（filelibrary.go），可识别错一两集的情况整部重整修不好：
// 「蜡笔小新第二季-720.mp4」的 720 被当成分辨率，进了 Season 0（2026-10-04 现场）。
// 整部重整时兄弟集早已改成规范名，fillEpisodesFromSiblings 找不到同模板的兄弟，照样认不出；
// 何况为了 3 集把 873 集的 STRM 全部重写、整部再刮一遍也不划算。
//
// 这里让用户直接给勾选的视频指定季集（预填解析结果，认不出时拿末段数字当「推测」）：
//   - 判定（planEpisodePicks）只读台账，不动网盘：算新名与落点，按洗版策略逐集判定；
//   - 执行先洗版（旧版让位 / 判输的移「已存在」），其余交给 redoOrganizeWith ——
//     改名搬移、清旧 STRM、落盘、刮削、刷 Emby 与整部重整同一套，不另写入库路径。
//
// 只做剧集；所在片目必须带得出 TMDB 编号（目录名标签或整理记录），换片的用整部重新整理。

func init() {
	jobExecutors["libepisode"] = execLibEpisodeJob
}

// episodePick 用户给一个视频指定的季集
type episodePick struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// fileEpisodeMax 一次最多指定几集：这是修个别集用的，整季错了该用整部重新整理
const fileEpisodeMax = 50

// applyEpisodePicks 把指定的季集盖到解析结果上（重新整理的 remap 钩子里调用）
func applyEpisodePicks(eps map[string]*ParsedName, picks map[string]episodePick) {
	for fid, pk := range picks {
		if p := eps[fid]; p != nil {
			p.Season, p.Episode, p.EpisodeEnd, p.SeasonGuessed = pk.Season, pk.Episode, 0, false
		}
	}
}

// withoutPicked 去掉被指定季集的那几集（它们不参与全剧连续编号换算：用户给的就是季内集号）
func withoutPicked(eps map[string]*ParsedName, picks map[string]episodePick) map[string]*ParsedName {
	if len(picks) == 0 {
		return eps
	}
	out := make(map[string]*ParsedName, len(eps))
	for fid, p := range eps {
		if _, ok := picks[fid]; !ok {
			out[fid] = p
		}
	}
	return out
}

// libFileTitle 当前目录所在的片目（媒体库里、片目目录本身或它下面任意一层）
type libFileTitle struct {
	libName  string // 库名（台账前缀）
	titleRel string // 片目的库内相对路径
	titleCid string
	titleIdx int    // 片目在面包屑上的下标
	dirRel   string // 当前目录相对片目的路径（就在片目这一层时为空）
}

// titleName 片目目录名
func (t libFileTitle) titleName() string { return path.Base(t.titleRel) }

// source 整理记录的 Source：「片目名/子目录/」
func (t libFileTitle) source() string { return path.Join(t.titleName(), t.dirRel) + "/" }

// libTitleAround 面包屑末元素（当前目录）在不在某个片目里（纯函数）
func libTitleAround(chain []browseCrumb, roles map[string]string, layout libCategoryLayout) (libFileTitle, bool) {
	idx := chainRoleIndex(chain, roles, "library")
	if idx < 0 {
		return libFileTitle{}, false
	}
	parts := make([]string, 0, len(chain))
	for j := idx + 1; j < len(chain); j++ {
		parts = append(parts, chain[j].Name)
		rel := strings.Join(parts, "/")
		if !layout.isTitleRel(rel) {
			continue
		}
		var sub []string
		for _, c := range chain[j+1:] {
			sub = append(sub, c.Name)
		}
		return libFileTitle{libName: chain[idx].Name, titleRel: rel, titleCid: chain[j].Cid, titleIdx: j,
			dirRel: strings.Join(sub, "/")}, true
	}
	return libFileTitle{}, false
}

// episodeRowAction 片目里的一个文件能不能「指定季集」：返回原因（空 = 能）。纯函数
func episodeRowAction(t libFileTitle, layout libCategoryLayout, name string, isDir bool) string {
	if isDir {
		return "片目里的子目录跟着片目走，请在片目那一层操作"
	}
	if !videoExts[strings.ToLower(pathExt(name))] {
		return "字幕、NFO、图片跟着同名视频走"
	}
	if layout[path.Dir(t.titleRel)] == "movie" {
		return "电影片目里的文件跟着片目走，请在片目那一层操作"
	}
	return ""
}

// guessEpisode 对话框的预填：先按正常整理的口径解析（替换规则 + 所在目录季号），
// 认不出集号时拿文件名末段数字当集号，标「推测」——单看一个名字时 480 / 720 会被当成分辨率排除
func guessEpisode(name, dir string, rules []ReplaceRule) (pk episodePick, guessed bool) {
	p := parseVideoInDir(remoteFile{Name: name, Path: dir}, rules, nil)
	pk.Season = p.Season
	if p.Episode > 0 {
		pk.Episode = p.Episode
		if p.SeasonGuessed {
			guessed = true
		}
		return pk, guessed
	}
	if _, d := lastDigitsTemplate(name); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			pk.Episode = n
		}
	}
	if pk.Season <= 0 {
		pk.Season = 1
	}
	return pk, true
}

// episodePlanItem 一个视频的判定结果（预览接口原样返回）
type episodePlanItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Guessed   bool   `json:"guessed,omitempty"`
	NewName   string `json:"new_name,omitempty"`
	TargetRel string `json:"target_rel,omitempty"`
	// Wash 落进去会怎样：new 新增集 / replace 洗掉库内旧版 / exists 库内更优、移「已存在」/
	// samefile 库内已有同一份 / coexist 与库内版本共存 / unchanged 位置与名字都没变 / conflict 目标已有同名文件
	Wash     string `json:"wash"`
	WashText string `json:"wash_text,omitempty"`

	plan *washPlan
}

// episodePlan 一次指定季集的完整判定
type episodePlan struct {
	layout *redoLayout
	items  []episodePlanItem
	st     *washStrategy
}

// episodeLedger 判定要读的台账（测试换成内存的）
type episodeLedger struct {
	inDir  func(targetRel string) []model.SyncedFile // 某个库内目录下的直接文件
	bySha1 func(sha1 string) []model.SyncedFile
}

// planEpisodePicks 纯判定：算新名与落点，再逐集按洗版策略判定。不发 115 请求、不动本地。
// files 是勾选的视频及其同名附属（Dir 填「片目名/子目录」，与整理记录同口径）；
// picks 必须给每个视频一项
func planEpisodePicks(media *TmdbMedia, category string, t libFileTitle, files []orgRecordFile,
	picks map[string]episodePick, rules []ReplaceRule, st *washStrategy, lg episodeLedger) (*episodePlan, error) {
	for _, f := range files {
		if f.Kind != "video" {
			continue
		}
		pk, ok := picks[f.Fid]
		if !ok || pk.Episode <= 0 || pk.Season < 0 || pk.Season > 99 || pk.Episode > 9999 {
			return nil, fmt.Errorf("「%s」的季集不正确（季 0-99、集 1-9999）", f.Name)
		}
	}
	// 与执行时 redoOrganizeWith 同样的入参（记录的 Source、替换规则），预览出来的名字才和真改的一致
	layout, err := planRedoLayoutWith(media, category, files, t.source(), rules, func(eps map[string]*ParsedName) {
		applyEpisodePicks(eps, picks)
	})
	if err != nil {
		return nil, err
	}
	if strings.Trim(layout.rootRel, "/") != strings.Trim(t.titleRel, "/") {
		return nil, fmt.Errorf("按当前的重命名模板与分类规则，这部剧应该在「%s」，不是现在的「%s」。"+
			"请先对整部片目点「重新整理」，再来指定季集", layout.rootRel, t.titleRel)
	}

	own := map[string]bool{} // 勾选的文件自己的台账行不算「库内版本」：它们正是要搬的这几个
	for _, f := range files {
		own[f.Fid] = true
	}
	notOwn := func(rows []model.SyncedFile) []model.SyncedFile {
		out := make([]model.SyncedFile, 0, len(rows))
		for _, sf := range rows {
			if !own[sf.FileID] {
				out = append(out, sf)
			}
		}
		return out
	}
	curRel := strings.Trim(path.Join(t.titleRel, t.dirRel), "/")
	ep := &episodePlan{layout: layout, st: st}
	for rel, gfs := range layout.groups {
		for _, g := range gfs {
			if g.Kind != "video" {
				continue
			}
			var orig orgRecordFile
			for _, f := range files {
				if f.Fid == g.Fid {
					orig = f
				}
			}
			pk := picks[g.Fid]
			it := episodePlanItem{ID: g.Fid, Name: orig.Name, Season: pk.Season, Episode: pk.Episode,
				NewName: g.Name, TargetRel: rel}
			if rel == curRel && g.Name == orig.Name {
				it.Wash, it.WashText = "unchanged", "位置与文件名都没变，只重建 STRM 与元数据"
				ep.items = append(ep.items, it)
				continue
			}
			libFiles := notOwn(lg.inDir(rel))
			var same []model.SyncedFile
			if orig.Sha1 != "" {
				same = notOwn(lg.bySha1(orig.Sha1))
			}
			var lines []string
			capture := func(s string) { lines = append(lines, s) }
			var plan washPlan
			if st != nil {
				plan = decideWash(media, g.Name, orig.Sha1, rel, st, libFiles, same, capture)
			} else {
				plan = washNoStrategy(g.Name, same, capture)
			}
			plan.targetDir = rel
			for _, l := range lines {
				log.Printf("[整理] %s", l)
			}
			switch plan.decision {
			case washReplaced:
				it.Wash, it.WashText = "replace", "洗版：库内旧版 "+shortLogName(plan.oldName)+" 让位"
				p := plan
				it.plan = &p
			case washNotBetter:
				it.Wash, it.WashText = "exists", "库内已有更优版本，移到「已存在」"
			case washSameFile:
				it.Wash, it.WashText = "samefile", "库内已有同一份文件，移到「已存在」"
			default:
				it.Wash, it.WashText = "new", "新增集"
				for _, sf := range libFiles {
					if ledgerIsVideo(sf) && sameWashEpisode(g.Name, ledgerName(sf)) {
						it.Wash, it.WashText = "coexist", "与库内 "+shortLogName(ledgerName(sf))+" 共存"
						break
					}
				}
			}
			// 不论策略怎么判，同名文件不能落进同一个目录（没配策略 / 共存时会走到这里）
			if it.Wash == "new" || it.Wash == "coexist" {
				for _, sf := range libFiles {
					if ledgerIsVideo(sf) && (ledgerName(sf) == baseName(g.Name) || ledgerName(sf) == g.Name) {
						it.Wash, it.WashText = "conflict", "目标位置已有同名文件 "+path.Base(sf.RelPath)
						break
					}
				}
			}
			ep.items = append(ep.items, it)
		}
	}
	sort.Slice(ep.items, func(i, j int) bool { return ep.items[i].Name < ep.items[j].Name })
	return ep, nil
}

// ---- 请求 ----

// fileEpisodeRequest 预览与提交的请求体
type fileEpisodeRequest struct {
	fileJobParams
}

// episodeContext 预览与执行共用的准备：定位片目、取 TMDB 条目、洗版策略
type episodeContext struct {
	title    libFileTitle
	media    *TmdbMedia
	category string
	st       *washStrategy
}

// prepareEpisodeContext 只发 TMDB 请求，不发 115 请求（chain 由调用方给）
func (h *Handler) prepareEpisodeContext(chain []browseCrumb, items []fileJobItem) (*episodeContext, error) {
	if len(items) == 0 {
		return nil, errors.New("请先勾选要指定季集的视频")
	}
	if len(items) > fileEpisodeMax {
		return nil, fmt.Errorf("一次最多指定 %d 集；整季都错了请对整部片目「重新整理」", fileEpisodeMax)
	}
	layout := loadLibCategoryLayout()
	t, ok := libTitleAround(chain, h.workspaceRoles(), layout)
	if !ok {
		return nil, errors.New("只能给媒体库片目里的视频指定季集")
	}
	for _, it := range items {
		if why := episodeRowAction(t, layout, it.Name, it.IsDir); why != "" {
			return nil, fmt.Errorf("「%s」不能指定季集：%s", it.Name, why)
		}
	}
	tmdbID := h.titleTmdbOf(t)
	if tmdbID <= 0 {
		return nil, errors.New("片目目录名里没有 TMDB 编号、也找不到它的整理记录：请先对整部片目「重新整理」")
	}
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	media, err := tc.getByTmdbID(tmdbID, true)
	if err != nil {
		return nil, fmt.Errorf("拉取 TMDB 条目失败: %w", err)
	}
	if media == nil {
		return nil, fmt.Errorf("TMDB 上找不到剧集 %d：这部片目可能是电影，电影没有季集", tmdbID)
	}
	media.MediaType = "tv"
	ensureRenameTpl()
	category := classifyMedia(media)
	return &episodeContext{title: t, media: media, category: category, st: matchWashStrategy("tv", category)}, nil
}

// titleTmdbOf 片目的 TMDB 编号：目录名标签优先，其次是落点就是它的整理记录
func (h *Handler) titleTmdbOf(t libFileTitle) int {
	if _, _, id := parseTitleDir(t.titleName()); id > 0 {
		return id
	}
	var rec model.OrganizeRecord
	if h.DB.Where("target_cid = ? AND tmdb_id > 0 AND media_type = ?", t.titleCid, "tv").
		Order("id DESC").First(&rec).Error == nil {
		return rec.TmdbID
	}
	return 0
}

// libEpisodeLedger 真台账
func libEpisodeLedger(libName string) episodeLedger {
	return episodeLedger{
		inDir: func(rel string) []model.SyncedFile { return libraryFilesOf(rel, libName) },
		bySha1: func(sha1 string) []model.SyncedFile {
			var rows []model.SyncedFile
			model.DB.Where("sha1 = ? AND sha1 != '' AND orphan_at IS NULL", sha1).Find(&rows)
			return rows
		},
	}
}

// PreviewFileEpisodes POST /files/library/episodes/preview → 预填季集 + 新名 + 洗版判定（不发 115 请求）
// body: {cid, chain, items, episodes?}。episodes 里没有的视频按文件名预填
func (h *Handler) PreviewFileEpisodes(c *gin.Context) {
	var req fileEpisodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if n := len(req.Chain); n == 0 || req.Chain[n-1].Cid != req.Cid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前位置未知，请从根目录重新点进来"})
		return
	}
	ctx, err := h.prepareEpisodeContext(req.Chain, req.Items)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rules := loadReplaceRules()
	dir := path.Join(ctx.title.titleName(), ctx.title.dirRel)
	picks := map[string]episodePick{}
	guessed := map[string]bool{}
	files := make([]orgRecordFile, 0, len(req.Items))
	for _, it := range req.Items {
		pk, ok := req.Episodes[it.ID]
		if !ok {
			pk, guessed[it.ID] = guessEpisode(it.Name, dir, rules)
		}
		picks[it.ID] = pk
		f := orgRecordFile{Fid: it.ID, Name: it.Name, Dir: dir, Kind: "video", PickCode: it.PickCode}
		var sf model.SyncedFile // 列目录不带 sha1，判「同一份文件」用台账里它自己那行的
		if h.DB.Where("file_id = ?", it.ID).First(&sf).Error == nil {
			f.Sha1 = sf.Sha1
		}
		files = append(files, f)
	}
	out := gin.H{"title": ctx.media.Title, "year": ctx.media.Year, "title_rel": ctx.title.titleRel,
		"strategy": ctx.st != nil}
	items := make([]episodePlanItem, 0, len(files))
	ep, err := planEpisodePicks(ctx.media, ctx.category, ctx.title, files, picks, rules, ctx.st, libEpisodeLedger(ctx.title.libName))
	if err != nil {
		// 季集填错了也要把预填值还给前端，让用户能改
		out["error"] = err.Error()
		for _, it := range req.Items {
			pk := picks[it.ID]
			items = append(items, episodePlanItem{ID: it.ID, Name: it.Name, Season: pk.Season, Episode: pk.Episode, Guessed: guessed[it.ID]})
		}
	} else {
		for _, it := range ep.items {
			it.Guessed = guessed[it.ID]
			items = append(items, it)
		}
	}
	out["items"] = items
	c.JSON(http.StatusOK, out)
}

// SubmitFileEpisodes POST /files/library/episodes → 入队（libepisode），202
// body: {cid, chain, items, episodes}：每个视频都要给季集
func (h *Handler) SubmitFileEpisodes(c *gin.Context) {
	var req fileEpisodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if n := len(req.Chain); n == 0 || req.Chain[n-1].Cid != req.Cid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前位置未知，请从根目录重新点进来"})
		return
	}
	for _, it := range req.Items {
		pk, ok := req.Episodes[it.ID]
		if !ok || pk.Episode <= 0 || pk.Season < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "「" + it.Name + "」还没填季集"})
			return
		}
	}
	ctx, err := h.prepareEpisodeContext(req.Chain, req.Items)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.loadOrgConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fp := req.fileJobParams
	fp.Target = ""
	title := "指定季集《" + shortTitle(ctx.media.Title) + "》"
	if len(fp.Items) == 1 {
		pk := fp.Episodes[fp.Items[0].ID]
		title += fmt.Sprintf(" S%02dE%02d", pk.Season, pk.Episode)
	} else {
		title += fmt.Sprintf(" %d 集", len(fp.Items))
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: "libepisode", Title: title, DedupeKey: "libep:" + fileJobDedupe(fp),
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{Files: &fp},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "指定季集")
}

// ---- 执行 ----

func execLibEpisodeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	fp := p.Files
	if fp == nil || len(fp.Items) == 0 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	defer resetFileListCache()

	ops, err := h.newPan115Ops()
	if err != nil {
		return jobOutcome{}, err
	}
	ops.suppress = true // 整理自产：洗版让位、移「已存在」都登记事件抑制（§6.8）
	cfg, err := h.loadOrgConfig()
	if err != nil {
		return jobOutcome{}, err
	}
	chain, err := h.resolveBrowseChain(fp.Cid, fp.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return jobOutcome{}, errors.New("文件所在的目录已不存在（被删除或移动了），请刷新后重选")
		}
		return jobOutcome{}, err
	}
	ctx, err := h.prepareEpisodeContext(chain, fp.Items)
	if err != nil {
		return jobOutcome{}, err
	}
	t := ctx.title

	// 文件以网盘为准现场列一遍（fid / pickcode / sha1 都要最新的），只要这一层的
	setJobProgress("读取目录", 0, 0, path.Join(t.titleRel, t.dirRel))
	dir := path.Join(t.titleName(), t.dirRel)
	all, err := collectDirFiles(ops, fp.Cid, dir)
	if err != nil {
		return jobOutcome{}, fmt.Errorf("读取目录失败: %w", err)
	}
	files, missing := episodeFilesOf(all, dir, fp.Items)
	if len(missing) > 0 {
		return jobOutcome{}, fmt.Errorf("这些文件已不在原目录里，请刷新后重选：%s", strings.Join(missing, "、"))
	}

	recFiles := make([]orgRecordFile, 0, len(files))
	for _, f := range files {
		recFiles = append(recFiles, f.orgRecordFile)
	}
	ep, err := planEpisodePicks(ctx.media, ctx.category, t, recFiles, fp.Episodes, loadReplaceRules(), ctx.st, libEpisodeLedger(t.libName))
	if err != nil {
		return jobOutcome{}, err
	}
	var conflicts []string
	for _, it := range ep.items {
		if it.Wash == "conflict" {
			conflicts = append(conflicts, it.Name+"（"+it.WashText+"）")
		}
	}
	if len(conflicts) > 0 {
		return jobOutcome{}, fmt.Errorf("网盘一个文件都没动：%s", strings.Join(conflicts, "；"))
	}
	log.Printf("[整理] ▶ 指定季集《%s》%s：%d 个视频", ctx.media.Title, path.Join(t.titleRel, t.dirRel), len(ep.items))

	// 洗版：判输的（连同同名附属）整批移「已存在」，赢了的先让旧版整批让位，其余照常入库
	var plans []*washPlan
	rejected := map[string]bool{}
	var rejectFiles []orgRecordFile
	for _, it := range ep.items {
		switch it.Wash {
		case "replace":
			plans = append(plans, it.plan)
		case "exists", "samefile":
			rejected[it.ID] = true
		}
	}
	accepted := make([]orgRecordFile, 0, len(files))
	for _, f := range files {
		if rejected[f.Fid] || rejected[f.owner] {
			rejectFiles = append(rejectFiles, f.orgRecordFile)
		} else {
			accepted = append(accepted, f.orgRecordFile)
		}
	}
	onLog := func(s string) { log.Printf("[整理] %s", s) }
	if len(plans) > 0 {
		setJobProgress("洗版：旧版让位", 0, 0, "")
		if err := applyWashPlans(ops, cfg, ctx.media, ctx.st, plans, onLog, notifyEmbyDeleted); err != nil {
			return jobOutcome{}, fmt.Errorf("洗版旧版让位失败，网盘上的新文件没动: %w", err)
		}
	}
	pruneDirs := map[string]string{}
	if fp.Cid != t.titleCid {
		pruneDirs[fp.Cid] = path.Join(t.titleRel, t.dirRel) // 搬空了的季目录（如放错的 Season 0）
	}
	var msgs []string
	if len(rejectFiles) > 0 {
		if err := h.rejectEpisodeFiles(ops, cfg, ctx, rejectFiles); err != nil {
			return jobOutcome{}, err
		}
		n := 0
		for _, f := range rejectFiles {
			if f.Kind == "video" {
				n++
			}
		}
		msgs = append(msgs, fmt.Sprintf("%d 集库内已有更优版本或同一份文件，已移到 已存在/%s", n, t.titleName()))
	}

	var recordID uint
	if hasVideo(accepted) {
		picks := map[string]episodePick{}
		var labels []string
		for _, it := range ep.items {
			if !rejected[it.ID] {
				picks[it.ID] = fp.Episodes[it.ID]
				labels = append(labels, fmt.Sprintf("S%02dE%02d", it.Season, it.Episode))
			}
		}
		rec := episodeRecord(ctx, accepted)
		if id, _ := currentJob(); id != 0 {
			rec.JobID = id
		}
		if err := h.DB.Create(&rec).Error; err != nil {
			return jobOutcome{}, fmt.Errorf("建立整理记录失败: %w", err)
		}
		recordID = rec.ID
		msg := "已指定季集 " + strings.Join(labels, "、")
		if len(labels) > 6 {
			msg = fmt.Sprintf("已指定季集 %s 等 %d 集", strings.Join(labels[:6], "、"), len(labels))
		}
		if err := h.redoOrganizeWith(&rec, ctx.media.TmdbID, "tv", &redoOpts{
			episodes: picks, pruneDirs: pruneDirs, message: msg + " → " + t.titleRel,
		}); err != nil {
			h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).Updates(withJobID(map[string]interface{}{
				"status": "failed", "stage": "move", "message": truncateStr("指定季集失败: "+err.Error(), 480),
			}))
			return jobOutcome{}, err
		}
		msgs = append([]string{msg}, msgs...)
	} else if len(pruneDirs) > 0 {
		pruner := newDirPruner(ops, orgProtectedCids(cfg), nil)
		for cid, label := range pruneDirs {
			pruner.mark(cid, label)
		}
		pruner.flush()
	}
	invalidateLedgerTitles()
	res := map[string]any{}
	if recordID > 0 {
		res["record_id"] = recordID
	}
	return jobOutcome{Message: strings.Join(msgs, "；"), Result: res}, nil
}

// episodeFile 勾选的视频或它的同名附属；owner 是附属跟着的那个视频
type episodeFile struct {
	orgRecordFile
	owner string
}

// episodeFilesOf 从目录列表里挑出勾选的视频（只认这一层），以及跟着它们命名的字幕 / 集 NFO / 剧照。
// 返回找不到的视频名。纯函数
func episodeFilesOf(all []remoteFile, dir string, items []fileJobItem) (files []episodeFile, missing []string) {
	byFid := map[string]remoteFile{}
	for _, f := range all {
		if f.Path == dir {
			byFid[f.Fid] = f
		}
	}
	picked := map[string]bool{}
	var stems []struct{ fid, stem string }
	for _, it := range items {
		f, ok := byFid[it.ID]
		if !ok {
			missing = append(missing, it.Name)
			continue
		}
		picked[f.Fid] = true
		files = append(files, episodeFile{orgRecordFile: orgRecordFile{Fid: f.Fid, Name: f.Name, Dir: f.Path,
			Kind: "video", PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}, owner: f.Fid})
		stems = append(stems, struct{ fid, stem string }{f.Fid, baseName(f.Name)})
	}
	// 长的基名先配：「X - 1」与「X - 12」同在时，「X - 12.ass」归后者
	sort.Slice(stems, func(i, j int) bool { return len(stems[i].stem) > len(stems[j].stem) })
	for _, f := range all {
		if f.Path != dir || picked[f.Fid] || videoExts[strings.ToLower(pathExt(f.Name))] {
			continue
		}
		b := baseName(f.Name)
		for _, s := range stems {
			if b == s.stem || strings.HasPrefix(f.Name, s.stem+".") || strings.HasPrefix(f.Name, s.stem+"-") {
				files = append(files, episodeFile{orgRecordFile: orgRecordFile{Fid: f.Fid, Name: f.Name, Dir: f.Path,
					Kind: recordFileKind(f.Name), PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}, owner: s.fid})
				break
			}
		}
	}
	return files, missing
}

// rejectEpisodeFiles 洗版判输的几集：整批移「已存在/片目名」，清掉它们的本地 STRM 与台账、通知 Emby，
// 再留一条「已存在」的整理记录（与正常整理判输时同口径）
func (h *Handler) rejectEpisodeFiles(ops *pan115Ops, cfg *OrgConfig, ctx *episodeContext, files []orgRecordFile) error {
	fids := make([]string, 0, len(files))
	videos := 0
	for _, f := range files {
		fids = append(fids, f.Fid)
		if f.Kind == "video" {
			videos++
		}
	}
	holding := ctx.title.titleName()
	if _, err := moveToHoldingDir(ops, cfg.Existing, holding, fids); err != nil {
		return fmt.Errorf("移到已存在失败: %w", err)
	}
	localRoot := localMediaRoot()
	removed, _ := dropLocalByFidsQuiet(localRoot, fids, nil)
	if len(removed) > 0 {
		go notifyEmbyDeleted(absUnder(localRoot, removed)...)
	}
	msg := fmt.Sprintf("指定季集后洗版判输，%d 个视频已移到 已存在/%s", videos, holding)
	log.Printf("[整理] ○ %s", msg)
	rec := model.OrganizeRecord{
		BatchID: time.Now().Format("20060102150405"),
		Source:  ctx.title.source(), SourceKind: "dir",
		Status: "exists", Message: msg,
		TmdbID: ctx.media.TmdbID, Title: ctx.media.Title, Year: ctx.media.Year, MediaType: "tv",
		PosterPath: ctx.media.PosterPath, Category: ctx.category, TargetDir: ctx.title.titleRel,
		Files: marshalRecordFiles(files), VideoCount: videos,
	}
	if id, _ := currentJob(); id != 0 {
		rec.JobID = id
	}
	h.DB.Create(&rec)
	return nil
}

// episodeRecord 为指定季集的那几集建一条整理记录：状态记「已入库」、目标就是所在片目，
// 重新整理据此在片目里改名搬移（不换标题目录），旧编号与新编号相同不会清媒体库条目
func episodeRecord(ctx *episodeContext, files []orgRecordFile) model.OrganizeRecord {
	rec := model.OrganizeRecord{
		BatchID: time.Now().Format("20060102150405"),
		Source:  ctx.title.source(), SourceKind: "dir",
		Status: "success", Message: "媒体库单集（网盘文件页指定季集）",
		TmdbID: ctx.media.TmdbID, Title: ctx.media.Title, Year: ctx.media.Year, MediaType: "tv",
		PosterPath: ctx.media.PosterPath, Category: ctx.category,
		TargetDir: ctx.title.titleRel, TargetCid: ctx.title.titleCid,
	}
	for _, f := range files {
		if f.Kind == "video" {
			rec.VideoCount++
			rec.TotalSize += f.Size
		}
	}
	rec.Files = marshalRecordFiles(files)
	return rec
}

func hasVideo(files []orgRecordFile) bool {
	for _, f := range files {
		if f.Kind == "video" {
			return true
		}
	}
	return false
}
