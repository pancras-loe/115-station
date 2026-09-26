package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 网盘文件页：整理所选条目 ====================
//
// 与自动整理同一条流水线（processEntry：识别 → 洗版 → 重命名 → 搬移 → 写 STRM → 刮削 → 刷 Emby），
// 只是扫描根换成所选条目所在的目录、只处理勾选的那几项。
// 指定了 TMDB 条目时跳过识别（ctx.forced，与「确认入库 / 重新指定」同一个口子），也就不会停下来等确认；
// 没指定时一切照整理配置走，开着人工确认就停在待确认。
//
// 媒体库里的条目不收：已入库的内容挪位置要走「重新整理」（它知道旧 STRM / 台账怎么收拾），
// 在这里重跑一遍流水线会把库内文件当成新素材，洗版判定撞上自己。

func init() {
	jobExecutors["orgpick"] = execFileOrganizeJob
}

// OrganizeFiles POST /files/organize → 入队，202
// body: {cid, chain, items:[{id,name,is_dir}], tmdb_id?, media_type?, label?}
func (h *Handler) OrganizeFiles(c *gin.Context) {
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
	if err := orgPickPrecheck(req.Chain, roles, req.Items); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.loadOrgConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := "整理" + fileJobTitle(req.Items)
	if req.TmdbID > 0 {
		title += " → " + pickLabel(pickReq{TmdbID: req.TmdbID, MediaType: req.MediaType, Label: req.Label})
	}
	fp := req.fileJobParams
	fp.Scrape = nil
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: "orgpick", Title: title, DedupeKey: fileJobDedupe(fp),
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{TmdbID: req.TmdbID, MediaType: req.MediaType, Files: &fp},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "整理")
}

// orgPickPrecheck 能不能整理这些条目（纯函数，入队时按前端面包屑查、执行时按网盘上的真实祖先链再查一遍）
func orgPickPrecheck(chain []browseCrumb, roles map[string]string, items []fileJobItem) error {
	if chainRoleIndex(chain, roles, "library") >= 0 {
		return errors.New("媒体库里的内容已经整理过：要改识别或位置，请到「任务中心 → 整理记录」对它「重新整理」")
	}
	for _, it := range items {
		if it.IsDir && roles[it.ID] != "" {
			return fmt.Errorf("「%s」是整理工作区目录（%s），不能被当作影视条目整理", it.Name, workspaceRoleText(roles[it.ID]))
		}
		if !it.IsDir && !videoExts[strings.ToLower(pathExt(it.Name))] {
			return fmt.Errorf("「%s」不是视频文件：散文件只能整理视频（字幕等会随同名视频一起搬）", it.Name)
		}
	}
	return nil
}

func workspaceRoleText(role string) string {
	switch role {
	case "library":
		return "媒体库"
	case "pending":
		return "待整理"
	case "share":
		return "转存"
	case "existing":
		return "已存在"
	case "redundant":
		return "冗余"
	}
	return role
}

func execFileOrganizeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	fp := p.Files
	if fp == nil || len(fp.Items) == 0 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	defer resetFileListCache()

	cfg, err := h.loadOrgConfig()
	if err != nil {
		return jobOutcome{}, err
	}
	tc, err := loadTmdbClient()
	if err != nil {
		return jobOutcome{}, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return jobOutcome{}, err
	}
	ops.suppress = true // 整理自产的网盘变更不该再被增量同步处理一遍（§6.8）

	// 执行时按网盘上的真实位置再核一遍：排队期间目录可能被挪进了媒体库
	chain, err := h.resolveBrowseChain(fp.Cid, fp.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return jobOutcome{}, errors.New("所选条目所在的目录已不存在（被删除或移动了），请刷新后重选")
		}
		return jobOutcome{}, err
	}
	roles := h.workspaceRoles()
	if err := orgPickPrecheck(chain, roles, fp.Items); err != nil {
		return jobOutcome{}, err
	}

	var forced *TmdbMedia
	if p.TmdbID > 0 {
		media, err := tc.getByTmdbID(p.TmdbID, p.MediaType == "tv")
		if err != nil || media == nil {
			return jobOutcome{}, fmt.Errorf("拉取 TMDB 条目 %s/%d 失败: %v", p.MediaType, p.TmdbID, err)
		}
		media.MediaType = p.MediaType
		forced = media
	}

	// 所在目录重新列一遍，只留勾选的：排队期间被别的任务搬走的就不在了
	top, err := listPendingTopLevel(ops, fp.Cid)
	if err != nil {
		return jobOutcome{}, fmt.Errorf("读取所在目录失败: %w", err)
	}
	entries, missing := pickEntries(top, fp.Items)

	orgStart := time.Now()
	ensureRenameTpl()
	rcfg := *cfg
	rcfg.Pending = fp.Cid // 扫描根 = 所在目录：散文件的字幕、同前缀的兄弟集都在这里找
	logFn := func(msg string) { log.Println("[整理] " + msg) }
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library)
	}
	sink := h.newOrgSink(cfg.Library)
	ctx := &orgCtx{ops: ops, cfg: &rcfg, tc: tc, rules: loadReplaceRules(), libAbs: libAbs, sink: sink,
		pruner: newDirPruner(ops, orgProtectedCids(cfg), logFn), onLog: logFn, forced: forced,
		held: loadAwaiting(), handled: map[string]bool{}}

	// 正在等人工确认的条目有自己的记录，在这里另起一次会留下两条互相打架的记录
	var held []string
	kept := entries[:0]
	for _, e := range entries {
		if ctx.held[e.Fid] != nil {
			held = append(held, e.Name)
			continue
		}
		kept = append(kept, e)
	}
	entries = kept

	var notes []string
	if len(missing) > 0 {
		notes = append(notes, fmt.Sprintf("%d 项已不在原目录（可能已被整理或挪走）：%s", len(missing), truncateStr(strings.Join(missing, "、"), 120)))
	}
	if len(held) > 0 {
		notes = append(notes, fmt.Sprintf("%d 项正在等人工确认，请到整理记录里处理：%s", len(held), truncateStr(strings.Join(held, "、"), 120)))
	}
	if len(entries) == 0 {
		return jobOutcome{}, errors.New(strings.Join(append([]string{"没有可以整理的条目"}, notes...), "；"))
	}

	guards := newOrgGuards(ops.cookie, fp.Cid, &rcfg)
	if forced != nil {
		log.Printf("[整理] ▶ 手动整理 %d 项 → 指定 %s (%s) [tmdb=%d]", len(entries), forced.Title, forced.Year, forced.TmdbID)
	} else {
		log.Printf("[整理] ▶ 手动整理 %d 项（自动识别）", len(entries))
	}
	var results []OrganizeResult
	successCount, done, canceled := 0, 0, false
	for i, e := range entries {
		if jobStopRequested() {
			canceled = true
			logFn(fmt.Sprintf("○ 已按要求停止：完成 %d/%d 项，其余留在原处", i, len(entries)))
			break
		}
		setJobProgress("整理", i, len(entries), truncateStr(e.Name, 60))
		results = append(results, processEntry(ctx, guards, e, 0, &successCount)...)
		done = i + 1
		time.Sleep(300 * time.Millisecond)
	}
	setJobProgress("刮削与刷新媒体库", done, progressKeep, "")
	ctx.pruner.flush()
	sink.flushScrape()
	sink.flushRefresh()
	finishOrganize(sink, results, orgStart)

	res, msg := summarizeOrganize(results)
	if len(results) == 0 {
		msg = "所选条目都被跳过了（见实时日志）"
	}
	if len(notes) > 0 {
		msg += "；" + strings.Join(notes, "；")
	}
	if canceled {
		msg = "已按要求停止；" + msg
	}
	return jobOutcome{Message: msg, Result: res, Canceled: canceled}, nil
}

// pickEntries 从目录的顶层条目里挑出勾选的（目录按自身 cid、文件按 fid 对），顺序跟勾选一致
func pickEntries(top []dirEntry, items []fileJobItem) (picked []dirEntry, missing []string) {
	byID := make(map[string]dirEntry, len(top))
	for _, e := range top {
		if e.IsDir {
			byID[e.Cid] = e
		}
		byID[e.Fid] = e
	}
	for _, it := range items {
		if e, ok := byID[it.ID]; ok && e.IsDir == it.IsDir {
			picked = append(picked, e)
			continue
		}
		missing = append(missing, it.Name)
	}
	return picked, missing
}
