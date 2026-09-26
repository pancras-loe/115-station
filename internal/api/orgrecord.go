package api

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 整理记录与重新整理 ====================
//
// 整理干了什么此前只在实时日志里留一行，滚过就没了：识别错了、刮削失败了，
// 用户看不到也改不了。这里把每一次整理动作落成一条记录（失败与未识别同样留痕），
// 并提供「重新整理」——用户搜 TMDB 指定正确条目，按记录里的 fid 原地重做。
//
// 之所以能原地重做：115 的 fid 在移动/改名后不变，所以不管文件此刻在冗余、
// 已存在还是库里的错误目录下，凭 fid 都能直接改名 + 搬到正确位置，
// 不必先搬回待整理再走一遍完整流水线（省两轮写请求，也不会被下一轮定时整理抢跑）。

// organizeRecordKeepDays 记录保留天数（随每日 prune 清理）
const organizeRecordKeepDays = 90

// orgRecordDTO 列表/详情返回体：Files 展开成数组，前端不必再解一层 JSON；
// Link 是这批内容的来源链接（离线/分享提交的才有）
type orgRecordDTO struct {
	model.OrganizeRecord
	FileList []orgRecordFile     `json:"file_list"`
	Link     *model.DownloadLink `json:"link,omitempty"`
}

func toRecordDTO(r model.OrganizeRecord, links map[uint]*model.DownloadLink) orgRecordDTO {
	return orgRecordDTO{OrganizeRecord: r, FileList: unmarshalRecordFiles(r.Files), Link: links[r.LinkID]}
}

// recordDTO 单条记录的返回体（详情、确认、重新整理之后回给前端的那一条）
func (h *Handler) recordDTO(r model.OrganizeRecord) orgRecordDTO {
	return toRecordDTO(r, recordLinks(h.DB, []model.OrganizeRecord{r}))
}

// ListOrganizeRecords GET /organize/records?status=&type=&q=&page=&size=
func (h *Handler) ListOrganizeRecords(c *gin.Context) {
	q := h.DB.Model(&model.OrganizeRecord{})
	if st := strings.TrimSpace(c.Query("status")); st != "" && st != "all" {
		if st == "problem" {
			// 「需要处理」= 失败 + 未识别：用户最常看的就是这一档
			q = q.Where("status IN ?", []string{"failed", "unrecognized"})
		} else if st == "staged" {
			// 「已指定」= 暂存了指定、还没提交到队列的，跨状态
			q = q.Where("pending_tmdb_id > 0")
		} else {
			q = q.Where("status = ?", st)
		}
	}
	if mt := strings.TrimSpace(c.Query("type")); mt == "movie" || mt == "tv" {
		q = q.Where("media_type = ?", mt)
	}
	if kw := strings.TrimSpace(c.Query("q")); kw != "" {
		if id, err := strconv.Atoi(kw); err == nil && id > 0 {
			// 纯数字多半是 TMDB ID；片名里恰好是数字的（《1917》）也照样能按原名搜到
			like := "%" + kw + "%"
			q = q.Where("tmdb_id = ? OR source LIKE ? OR title LIKE ?", id, like, like)
		} else {
			// 也能按来源链接搜：粘一段磁力 hash / 分享码就能找到它下成了哪部片
			like := "%" + kw + "%"
			links := h.DB.Model(&model.DownloadLink{}).Select("id").Where("url LIKE ? OR name LIKE ?", like, like)
			q = q.Where("source LIKE ? OR title LIKE ? OR target_dir LIKE ? OR (link_id <> 0 AND link_id IN (?))",
				like, like, like, links)
		}
	}
	var total int64
	q.Count(&total)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	var rows []model.OrganizeRecord
	q.Order("created_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows)

	links := recordLinks(h.DB, rows)
	items := make([]orgRecordDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, toRecordDTO(r, links))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "size": size})
}

// GetOrganizeRecord GET /organize/records/:id
func (h *Handler) GetOrganizeRecord(c *gin.Context) {
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.recordDTO(rec)})
}

// DeleteOrganizeRecord DELETE /organize/records/:id （只删记录，不动任何文件）
func (h *Handler) DeleteOrganizeRecord(c *gin.Context) {
	if err := h.DB.Delete(&model.OrganizeRecord{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除该条记录（网盘与本地文件不受影响）"})
}

// ClearOrganizeRecords POST /organize/records/clear  body: {"status":"all|success|exists|problem"}
func (h *Handler) ClearOrganizeRecords(c *gin.Context) {
	var req struct {
		Status string `json:"status"`
	}
	_ = c.ShouldBindJSON(&req)
	q := h.DB.Session(&gorm.Session{AllowGlobalUpdate: true})
	switch req.Status {
	case "", "all":
	case "problem":
		q = q.Where("status IN ?", []string{"failed", "unrecognized"})
	default:
		q = q.Where("status = ?", req.Status)
	}
	res := q.Delete(&model.OrganizeRecord{})
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清空 %d 条记录", res.RowsAffected), "removed": res.RowsAffected})
}

// pruneOrganizeRecords 清理过期整理记录（随 pruneSyncEvents 每日一次）
func pruneOrganizeRecords() {
	if model.DB == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -organizeRecordKeepDays)
	res := model.DB.Where("created_at < ?", cutoff).Delete(&model.OrganizeRecord{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[系统] ○ 清理 %d 条 %d 天前的整理记录", res.RowsAffected, organizeRecordKeepDays)
	}
}

// redoOrganize 原地重整理：按记录里的 fid 把文件改名 + 搬到指定 TMDB 条目对应的目录，
// 清掉旧的本地产物，重新落 STRM 并刮削。就地更新 rec
func (h *Handler) redoOrganize(rec *model.OrganizeRecord, tmdbID int, mediaType string) error {
	prevID, prevType := rec.TmdbID, rec.MediaType // 判断这次是不是改了指定（改了才记进识别记忆）
	files := unmarshalRecordFiles(rec.Files)
	if len(files) == 0 {
		return fmt.Errorf("这条记录没有登记任何文件，无法重新整理（只能删除记录）")
	}
	cfg, err := h.loadOrgConfig()
	if err != nil {
		return err
	}
	tc, err := loadTmdbClient()
	if err != nil {
		return err
	}
	// 进度分六步报给队列面板：此前重新整理全程不报进度，用户只能看着按钮转
	setJobProgress("拉取 TMDB 条目", 0, redoSteps, fmt.Sprintf("%s/%d", mediaType, tmdbID))
	media, err := tc.getByTmdbID(tmdbID, mediaType == "tv")
	if err != nil {
		return fmt.Errorf("拉取 TMDB 条目失败: %w", err)
	}
	if media == nil {
		return fmt.Errorf("TMDB 上找不到 %s/%d 这个条目", mediaType, tmdbID)
	}
	media.MediaType = mediaType
	ensureRenameTpl()

	ops, err := h.newPan115Ops()
	if err != nil {
		return err
	}
	ops.suppress = true
	sink := h.newOrgSink(cfg.Library)
	pruner := newDirPruner(ops, orgProtectedCids(cfg), nil)

	oldTargetDir := rec.TargetDir
	if oldTargetDir == "" {
		oldTargetDir = "（此前未入库）"
	}
	log.Printf("[整理] ▶ 重新整理《%s》：%s (%s) → %s (%s) [tmdb=%d→%d]",
		rec.Source, orDash(rec.Title), orDash(rec.Year), media.Title, media.Year, rec.TmdbID, tmdbID)
	if len(files) == 1 {
		log.Printf("[整理] ▣ 涉及 1 个文件 %s，旧位置 %s", files[0].Name, oldTargetDir)
	} else {
		log.Printf("[整理] ▣ 涉及 %d 个文件（%s），旧位置 %s", len(files), recordKindSummary(files), oldTargetDir)
	}

	// ---- 1) 先把新布局算出来 ----
	// 顺序很重要：破坏性动作（删本地产物、动网盘）必须排在计算之后。
	// 此前先删本地再算，中途任何一步失败都会让用户落得「STRM 没了还报错」
	category := classifyMedia(media)
	plan, err := planRedoLayout(media, category, files, rec.Source)
	if err != nil {
		return err
	}
	rootRel, renames, groups, metaFiles := plan.rootRel, plan.renames, plan.groups, plan.metaFiles

	// 原地刷新：选了跟现在一样的 TMDB 条目（目标目录没变、也不需要改名）。
	// 这种是冲着「刮削失败了重来一次 / 换了 STRM 域名要重写」来的，
	// 网盘那边一个字节都不用动 —— 把文件移动到它已经在的目录，
	// 115 的行为没有保证，而且白等一轮写限流、附属文件还要重下一遍
	inPlace := isInPlaceRedo(rec, rootRel, renames)
	if inPlace {
		log.Printf("[整理] ▣ 目标与现状一致（%s），按原地刷新处理：不动网盘，只重建 STRM 与元数据", rootRel)
	} else {
		log.Printf("[整理] ▣ 新目标目录: %s（分类 %s，共 %d 个落点）", rootRel, orDash(category), len(groups))
	}

	// 手动重整理是「修一下」的意思：STRM 一律按当前配置重写，
	// 不受「已存在则跳过」影响（否则换了直链域名也刷不动）
	sink.skipExist = false

	// ---- 2) 原地改名 + 搬移（原地刷新时整段跳过）----
	rootCid := rec.TargetCid
	setJobProgress("改名与搬移", 1, redoSteps, rootRel)
	if !inPlace {
		// 只改名不换目录的重整理（换了模板、补回了原名里的画质）同样不该搬动：
		// 把文件移动到它已经在的目录对 115 没有保证，失败还会让整次重整理作废
		plan.settled = settledGroups(sink, plan.groups)
		if err := redoRelocate(ops, cfg, files, plan, &rootCid); err != nil {
			return err
		}
	}

	// ---- 3) 回滚旧产出：落点变了的才删，没变的留着让 commit 覆盖 ----
	setJobProgress("清理旧的本地产物", 2, redoSteps, "")
	keep := map[string]string{}
	for rel, gfs := range groups {
		for _, f := range gfs {
			suffix := ""
			if f.Kind == "video" {
				suffix = ".strm"
			}
			keep[f.Fid] = path.Join(sink.libRel(rel), f.Name) + suffix
		}
	}
	for _, f := range metaFiles {
		keep[f.Fid] = path.Join(sink.libRel(rootRel), f.Name)
	}
	oldFids := make([]string, 0, len(files))
	for _, f := range files {
		oldFids = append(oldFids, f.Fid)
	}
	droppedPaths, keptLocal := dropLocalByFidsExcept(sink.localRoot, oldFids, keep)
	if rec.TmdbID > 0 && rec.TmdbID != tmdbID {
		// 认错了片才清掉旧的媒体库条目；同一个 tmdb 重整理时 recordMedia 会更新它
		h.DB.Where("tmdb_id = ? AND media_type = ?", rec.TmdbID, rec.MediaType).Delete(&model.MediaLibrary{})
	}
	switch {
	case len(droppedPaths) > 0:
		log.Printf("[整理] ○ 已清理旧的本地产物 %d 个（根目录 %s）:", len(droppedPaths), sink.localRoot)
		logList("[整理]     - %s", droppedPaths)
	case keptLocal > 0:
		log.Printf("[整理] ○ 落点未变，%d 个本地文件原地覆盖", keptLocal)
	default:
		log.Printf("[整理] ○ 本地没有该记录的旧产物需要清理")
	}

	// ---- 4) 落盘 + 刮削 ----
	strmTotal, videoTotal := 0, 0
	var newFiles []orgRecordFile
	var totalSize int64
	committed := 0
	for rel, gfs := range groups {
		committed++
		setJobProgress("写 STRM 与附属", 3, redoSteps, fmt.Sprintf("%s（%d/%d）", rel, committed, len(groups)))
		var vs, as []remoteFile
		for _, f := range gfs {
			rf := remoteFile{Fid: f.Fid, Name: f.Name, PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}
			if f.Kind == "video" {
				vs = append(vs, rf)
				videoTotal++
				totalSize += f.Size
			} else {
				as = append(as, rf)
			}
			nf := f
			nf.Kind = recordFileKind(f.Name)
			newFiles = append(newFiles, nf)
		}
		sc, dl := sink.commit(ops, media, rootRel, rel, vs, as)
		strmTotal += sc
		log.Printf("[整理] ✓ 落盘 %s：STRM %d 个、附属 %d 个 → %s", rel, sc, dl,
			filepath.Join(sink.localRoot, filepath.FromSlash(sink.libRel(rel))))
	}
	// NFO / 封面单独提交一次：它们落在标题目录，而剧集的 groups 全是季目录，
	// 挂在任何一个季分组上都不对（挂不上就等于本地永远没有元数据）
	if len(metaFiles) > 0 {
		metaRF := make([]remoteFile, 0, len(metaFiles))
		for _, f := range metaFiles {
			metaRF = append(metaRF, remoteFile{Fid: f.Fid, Name: f.Name, PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1})
			newFiles = append(newFiles, f)
		}
		_, dl := sink.commit(ops, media, rootRel, rootRel, nil, metaRF)
		log.Printf("[整理] ✓ 落盘 NFO/封面 %d 个 → %s", dl,
			filepath.Join(sink.localRoot, filepath.FromSlash(sink.libRel(rootRel))))
	}
	// ---- 5) 收拾旧位置：文件都搬走了，原来的目录是空壳 ----
	// 此前重新整理只搬文件，旧的错名标题目录和源目录一直留在网盘里，
	// Emby 扫出来就是一排没有剧集的空条目。
	// 只删确认为空的（pruneEmptyDirTree 里有守卫）：同一部剧分批入过库时
	// 旧目录里还留着别的集数，那种情况下整棵都不会动
	if rec.TargetCid != "" && rec.TargetCid != rootCid {
		// 上次入库的（错的）标题目录，含其下季目录
		pruner.mark(rec.TargetCid, oldTargetDir)
	}
	if rec.SourceKind == "dir" && rec.SourceFid != "" {
		// 当初的源目录，多半在冗余里躺着
		pruner.mark(rec.SourceFid, strings.TrimSuffix(rec.Source, "/"))
	}
	setJobProgress("清理空目录", 4, redoSteps, "")
	pruner.flush()

	setJobProgress("刮削与刷新媒体库", 5, redoSteps, rootRel)
	sink.flushScrape()
	sink.flushRefresh()
	setJobProgress("", redoSteps, redoSteps, "")

	// ---- 6) 更新记录 + 修正 MediaLibrary ----
	recordMedia(media, category, plan.sampleVideoPath())
	rec.Status = "success"
	rec.Stage = ""
	rec.Message = fmt.Sprintf("已按手动指定的 TMDB 条目重新整理 → %s", rootRel)
	rec.TmdbID = media.TmdbID
	rec.Title = media.Title
	rec.Year = media.Year
	rec.MediaType = media.MediaType
	rec.PosterPath = media.PosterPath
	rec.Category = category
	rec.TargetDir = rootRel
	rec.TargetCid = rootCid
	rec.Files = marshalRecordFiles(newFiles)
	rec.VideoCount = videoTotal
	rec.TotalSize = totalSize
	rec.StrmCreated = strmTotal
	rec.ManualTmdb = true
	rec.RedoCount++
	if err := h.DB.Save(rec).Error; err != nil {
		return fmt.Errorf("整理已完成但记录更新失败: %w", err)
	}
	log.Printf("[整理] ✅ 重新整理完成：%s (%s) → %s（本地根 %s），视频 %d 个，生成 STRM %d 个",
		media.Title, media.Year, rootRel, sink.localRoot, videoTotal, strmTotal)
	// 选了和原来不同的条目 = 自动识别在这个名字上错了，记下人工结论
	if tmdbID != prevID || media.MediaType != prevType {
		rememberRecognition(rec.RecogKey, rec.Source, media)
	}
	return nil
}

// redoSteps 重新整理报给队列面板的总步数（拉条目 / 改名搬移 / 清旧产物 / 落盘 / 清空目录 / 刮削刷新）
const redoSteps = 6

// orDash 空值显示为破折号（日志里空串会让人以为是漏打了）
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// findOrigName 从记录快照里取某个 fid 的原文件名（批量改名失败时回退用）
func findOrigName(files []orgRecordFile, fid string) (string, bool) {
	for _, f := range files {
		if f.Fid == fid {
			return f.Name, true
		}
	}
	return "", false
}

// redoLayout 重新整理的目标布局：文件该改成什么名、落到哪个目录
type redoLayout struct {
	rootRel   string                     // 标题目录（库内相对，刮削与 Emby 刷新的单位）
	renames   map[string]string          // fid → 新文件名
	groups    map[string][]orgRecordFile // 落点相对路径 → 该目录下的视频与字幕
	metaFiles []orgRecordFile            // NFO / 封面：一律进标题目录
	settled   map[string]bool            // 落点 → 文件已经就在那儿了（只改名，不搬动）
}

// sampleVideoPath 取一个代表性视频的库内路径（写进 MediaLibrary.TargetPath）。
// 必须是**文件**路径：去重与洗版都按 path.Dir 反推所在目录，存目录会被再削一层，
// 洗版于是退到二级分类层，拿整类影片当「同一部片的旧版本」比较并搬走
func (l *redoLayout) sampleVideoPath() string {
	rels := make([]string, 0, len(l.groups))
	for rel := range l.groups {
		rels = append(rels, rel)
	}
	sort.Strings(rels) // map 无序，多季重整理时别每次换一个代表
	for _, rel := range rels {
		for _, f := range l.groups[rel] {
			if f.Kind == "video" {
				return rel + "/" + f.Name
			}
		}
	}
	return l.rootRel
}

// recordOrigNames fid → 重命名之前的原始文件名（取不到就退回当前名）。
//
// 整理时登记的 Orig 是首选。老记录没有这个字段，单视频记录还能从
// rec.Source 捞回来（散文件整理的 Source 就是原文件名）——扩展名对得上
// 才认，目录整理的 Source 是目录名，不能拿来当文件名用
func recordOrigNames(files []orgRecordFile, srcName string) map[string]string {
	out := make(map[string]string, len(files))
	soleVideo, videos := "", 0
	for _, f := range files {
		if f.Orig != "" {
			out[f.Fid] = f.Orig
		} else {
			out[f.Fid] = f.Name
		}
		if f.Kind == "video" {
			videos++
			if f.Orig == "" {
				soleVideo = f.Fid
			}
		}
	}
	if videos == 1 && soleVideo != "" && srcName != "" && !strings.Contains(srcName, "/") &&
		strings.EqualFold(pathExt(srcName), pathExt(out[soleVideo])) {
		out[soleVideo] = srcName
	}
	return out
}

// planRedoLayout 纯计算：给定 TMDB 条目与记录里的文件清单，算出重整理的目标布局。
// 不碰网盘也不碰本地磁盘，便于单测覆盖——重整理最容易出错的就是这段路径推导
func planRedoLayout(media *TmdbMedia, category string, files []orgRecordFile, srcName string) (*redoLayout, error) {
	out := &redoLayout{renames: map[string]string{}, groups: map[string][]orgRecordFile{}}
	newBaseOf := map[string]string{} // 视频旧基名 → 新基名（字幕跟随用）
	videos := 0
	origOf := recordOrigNames(files, srcName)

	for _, f := range files {
		if f.Kind != "video" {
			continue
		}
		// 季集按**当前**文件名解析（它已经是规范名），但模板里的资源变量
		// 要拿**原始**文件名算：画质/编码只存在于原名里，上一次重命名没能
		// 认出来的（粘连写法、模板没带这些字段）就永远回不来了
		parsed := parseFileName(f.Name)
		newPath := buildNewNameWithTemplate(media, parsed, origOf[f.Fid])
		if newPath == "" {
			continue
		}
		base := categoryDir(media.MediaType, category)
		if out.rootRel == "" {
			out.rootRel = libSubPath(base, strings.SplitN(newPath, "/", 2)[0])
		}
		newName := pathBase(newPath)
		if newName != "" && newName != f.Name {
			out.renames[f.Fid] = newName
			newBaseOf[baseName(f.Name)] = baseName(newName)
		}
		nf := f
		nf.Name = newName
		mediaRel := libSubPath(base, pathDir(newPath))
		out.groups[mediaRel] = append(out.groups[mediaRel], nf)
		videos++
	}
	if out.rootRel == "" || videos == 0 {
		return nil, fmt.Errorf("这条记录里没有可识别的视频文件，无法重新整理")
	}

	for _, f := range files {
		switch f.Kind {
		case "subtitle":
			nf := f
			fb := baseName(f.Name)
			for oldB, newB := range newBaseOf {
				if fb == oldB || strings.HasPrefix(fb, oldB+".") {
					if n := newB + strings.TrimPrefix(fb, oldB) + pathExt(f.Name); n != f.Name {
						nf.Name = n
						out.renames[f.Fid] = n
					}
					break
				}
			}
			// 放到同基名视频所在的分组；对不上（历史改过名）就跟第一个分组走，
			// 总比落在标题目录里让播放器找不到强
			placed := false
			for rel, gfs := range out.groups {
				for _, v := range gfs {
					if v.Kind == "video" && strings.HasPrefix(baseName(nf.Name), baseName(v.Name)) {
						out.groups[rel] = append(out.groups[rel], nf)
						placed = true
						break
					}
				}
				if placed {
					break
				}
			}
			if !placed {
				for rel := range out.groups {
					out.groups[rel] = append(out.groups[rel], nf)
					break
				}
			}
		case "meta":
			out.metaFiles = append(out.metaFiles, f)
		}
	}
	return out, nil
}

// logListMax 逐条列举的上限。一部 40 集的剧会列出几十行，一次操作就把
// 实时日志页刷满了——上限与日志级别无关，详细模式也照样截断
const logListMax = 20

// logSink 日志出口（测试可替换，用来断言截断行为）
var logSink = log.Printf

// logList 逐条打印，超过上限时截断并提示总数
func logList(format string, items []string) {
	for i, it := range items {
		if i >= logListMax {
			logSink("[整理]     …另有 %d 条（完整清单见整理记录详情）", len(items)-logListMax)
			return
		}
		logSink(format, it)
	}
}

// recordKindSummary 记录里文件清单的分类计数（与 fileKindSummary 同口径，
// 只是数据源换成整理记录的 orgRecordFile）
func recordKindSummary(files []orgRecordFile) string {
	rf := make([]remoteFile, 0, len(files))
	for _, f := range files {
		rf = append(rf, remoteFile{Name: f.Name})
	}
	return fileKindSummary(rf)
}

// settledGroups 哪些落点的文件已经就在那儿了（按本地台账里的目录判断：
// 台账行与网盘路径同源，整理与增量都按它落盘）。
// 查不到台账行、或目录对不上的一律按「要搬」处理 —— 宁可多搬一次
func settledGroups(sink *orgSink, groups map[string][]orgRecordFile) map[string]bool {
	out := make(map[string]bool, len(groups))
	for rel, gfs := range groups {
		want, all := sink.libRel(rel), len(gfs) > 0
		for _, f := range gfs {
			var sf model.SyncedFile
			if f.Fid == "" || model.DB.Where("file_id = ?", f.Fid).First(&sf).Error != nil ||
				pathDir(sf.RelPath) != want {
				all = false
				break
			}
		}
		out[rel] = all
	}
	return out
}

// redoRelocate 重新整理的网盘侧动作：批量改名 + 按落点分组搬移。
// rootCid 出参给调用方（记录里要存新的标题目录 cid）。
// 原地刷新（目标与现状一致）时调用方整段跳过 —— 把文件移动到它已经在的目录
// 对 115 是未定义行为，白等一轮写限流不说，失败还会让整次重整理作废
func redoRelocate(ops *pan115Ops, cfg *OrgConfig, files []orgRecordFile, plan *redoLayout, rootCid *string) error {
	rootRel, renames, groups, metaFiles := plan.rootRel, plan.renames, plan.groups, plan.metaFiles

	if len(renames) > 0 {
		if len(renames) == 1 {
			for fid, newName := range renames {
				old, _ := findOrigName(files, fid)
				log.Printf("[整理] ▣ 重命名 %s → %s", old, newName)
			}
		} else {
			example := ""
			for fid, newName := range renames {
				old, _ := findOrigName(files, fid)
				example = fmt.Sprintf("%s → %s", old, newName)
				break
			}
			log.Printf("[整理] ▣ 需重命名 %d 个文件（例: %s）", len(renames), example)
		}
		renamed, err := ops.renameBatch(renames)
		if err != nil {
			log.Printf("[整理] ○ 重新整理批量改名未全部完成（成功 %d、保持原名 %d）: %v",
				len(renamed), len(renames)-len(renamed), err)
			// 只有失败项目仍用原名；此前分批中途失败会把已成功项目也误判成原名，
			// 随后生成的 STRM 就会指向不存在的旧文件名。
			for rel, gfs := range groups {
				for i := range gfs {
					if _, ok := renamed[gfs[i].Fid]; ok {
						continue
					}
					if orig, ok := findOrigName(files, gfs[i].Fid); ok {
						groups[rel][i].Name = orig
					}
				}
			}
		}
	}

	cidOfRoot, err := ops.ensurePath(cfg.Library, rootRel)
	if err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}
	*rootCid = cidOfRoot

	for rel, gfs := range groups {
		cid := cidOfRoot
		if rel != rootRel {
			cid, err = ops.ensurePath(cfg.Library, rel)
			if err != nil {
				return fmt.Errorf("创建目录 %s 失败: %w", rel, err)
			}
		}
		if plan.settled[rel] {
			log.Printf("[整理] ○ %s 的文件本来就在目标目录，只改名不搬动", rel)
			continue
		}
		fids := make([]string, 0, len(gfs))
		for _, f := range gfs {
			fids = append(fids, f.Fid)
		}
		if err := ops.moveFiles(cid, fids); err != nil {
			return fmt.Errorf("移动文件到 %s 失败: %w", rel, err)
		}
		if len(gfs) == 1 {
			log.Printf("[整理] ✓ 已移动 %s → %s（cid=%s）", gfs[0].Name, rel, cid)
		} else {
			log.Printf("[整理] ✓ 已移动 %d 个文件（%s）→ %s（cid=%s）", len(fids), recordKindSummary(gfs), rel, cid)
		}
	}

	if len(metaFiles) > 0 {
		fids := make([]string, 0, len(metaFiles))
		names := make([]string, 0, len(metaFiles))
		for _, f := range metaFiles {
			fids = append(fids, f.Fid)
			names = append(names, f.Name)
		}
		if err := ops.moveFiles(cidOfRoot, fids); err != nil {
			log.Printf("[整理] ○ NFO/封面移动失败（不影响入库）: %v", err)
		} else {
			log.Printf("[整理] ✓ 已移动 NFO/封面 %d 个 → %s（%s）", len(fids), rootRel, strings.Join(names, ", "))
		}
	}
	return nil
}

// isInPlaceRedo 判断这次重整理是不是「原地刷新」：用户选了跟现在一样的 TMDB 条目，
// 算出来的目标目录与现状完全一致、也没有文件要改名。
//
// 四个条件缺一不可：
//   - 上次真的入库成功（失败/未识别的记录，文件还在冗余里，必须搬）
//   - 记录里存了旧的目标目录与 cid（拿不到就没法确认现状，老实搬一遍）
//   - 新算出的目标目录与旧的一致
//   - 没有文件需要改名（改了名就不是"没变"）
func isInPlaceRedo(rec *model.OrganizeRecord, rootRel string, renames map[string]string) bool {
	return rec.Status == "success" &&
		rec.TargetDir != "" && rec.TargetCid != "" &&
		rec.TargetDir == rootRel &&
		len(renames) == 0
}
