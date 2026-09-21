package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

// 深度删除只处理 Emby 事件命中的台账或用户指定的整理记录，不扫描全库缺失文件。
// 默认关闭；所有网盘删除仍经过节流、自产事件抑制与路径缓存失效钩子。
const (
	deepDelMaxBatchDefault = 50
	deepDelMaxRatioDefault = 0.1
	deepDelChunk           = 100
	deepDelKeepDays        = 180
)

type deepDelCfg struct {
	Enabled      bool     `json:"enabled"`
	MaxBatch     *int     `json:"max_batch"`
	MaxRatio     *float64 `json:"max_ratio"`
	PrunePanDirs *bool    `json:"prune_pan_dirs"`
	Notify       *bool    `json:"notify"`
}

func (c deepDelCfg) prunePanDirs() bool { return c.PrunePanDirs == nil || *c.PrunePanDirs }
func (c deepDelCfg) notify() bool       { return c.Notify == nil || *c.Notify }
func (c deepDelCfg) maxBatch() int {
	if c.MaxBatch == nil || *c.MaxBatch <= 0 {
		return deepDelMaxBatchDefault
	}
	return *c.MaxBatch
}
func (c deepDelCfg) maxRatio() float64 {
	if c.MaxRatio == nil || *c.MaxRatio <= 0 {
		return deepDelMaxRatioDefault
	}
	return *c.MaxRatio
}
func (h *Handler) loadDeepDelCfg() deepDelCfg {
	var cfg deepDelCfg
	_ = json.Unmarshal([]byte(h.getSettingValue("deepdel")), &cfg)
	return cfg
}

// 库根检查保留为独立守卫；任何库目录不可访问都不能当成文件被删除。
func checkLibRoots(root string, rows []model.SyncedFile) error {
	libs := map[string]bool{}
	for _, r := range rows {
		rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
		if i := strings.Index(rel, "/"); i > 0 {
			libs[rel[:i]] = true
		}
	}
	for lib := range libs {
		if info, err := os.Stat(filepath.Join(root, lib)); err != nil {
			return fmt.Errorf("媒体库目录「%s」读不出来（%v）—— 挂载或本地路径配置有问题，本轮放弃", lib, err)
		} else if !info.IsDir() {
			return fmt.Errorf("媒体库路径不是目录: %s", lib)
		}
	}
	return nil
}

func deepDelOverLimit(cfg deepDelCfg, videos, files, ledger int) (bool, string) {
	if n := cfg.maxBatch(); videos > n {
		return true, fmt.Sprintf("本轮有 %d 个视频待删，超过单轮上限 %d", videos, n)
	}
	if r := cfg.maxRatio(); ledger > 0 {
		got := float64(files) / float64(ledger)
		if got > r {
			return true, fmt.Sprintf("本轮待删 %d 个文件，占台账 %.1f%%（共 %d 条），超过上限 %.0f%%",
				files, got*100, ledger, r*100)
		}
	}
	return false, ""
}

// countKinds 拆出视频数与附属数（阈值按视频数卡，比例按文件总数算）
func countKinds(rows []model.SyncedFile) (videos, assets int) {
	for _, r := range rows {
		if r.Kind == "video" {
			videos++
		} else {
			assets++
		}
	}
	return
}

// ---- 执行 ----

// deepDelResult 一次深度删除的结果
type deepDelResult struct {
	Videos  int
	Assets  int
	Fids    int
	PanDirs int
	Title   string
}

// runDeepDelete 执行深度删除：删网盘源文件 → 清本地产物与台账 → 清网盘空目录 → 留痕 → 通知。
//
// 顺序不能换。先删网盘、后清台账：反过来先清台账的话，网盘删除一旦失败，
// 后续同一事件重试就无法通过台账重新定位这批文件。
func (h *Handler) runDeepDelete(rows []model.SyncedFile, reason string) (deepDelResult, error) {
	cfg := h.loadDeepDelCfg()
	res := deepDelResult{}
	if len(rows) == 0 {
		return res, nil
	}
	root := h.orphanLocalRoot()

	fids := make([]string, 0, len(rows))
	rels := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		if r.FileID == "" || seen[r.FileID] {
			continue
		}
		seen[r.FileID] = true
		fids = append(fids, r.FileID)
		rels = append(rels, r.RelPath)
	}
	res.Videos, res.Assets = countKinds(rows)
	res.Fids = len(fids)
	res.Title = deepDelTitle(rels)
	if len(fids) == 0 {
		return res, nil
	}

	ops, err := h.newPan115Ops()
	if err != nil {
		return res, fmt.Errorf("115 通道不可用: %w", err)
	}
	// 自产的删除事件要抑制：绕回来的生活事件会拿这个 fid 再走一遍整树删本地，
	// 而本地产物下面马上就自己清掉了（对齐整理链路 executeOrganize 的做法）
	ops.suppress = true

	// 父目录路径要在删之前算：删完之后 PathCache 里那棵子树可能已经被失效钩子清掉了
	var panPaths []string
	if cfg.prunePanDirs() {
		panPaths = h.deepDelParentPaths(rows)
	}

	for _, batch := range chunkStrings(fids, deepDelChunk) {
		if err := ops.deleteFiles(batch); err != nil {
			h.noteDeepDelete(reason, res, rels, fids, "failed", "删除网盘文件失败: "+err.Error())
			return res, fmt.Errorf("删除网盘文件失败: %w", err)
		}
	}
	log.Printf("[深度删除] ○ 已删除 %d 个网盘源文件（在 115 回收站，可还原）", len(fids))

	// 本地侧：strm/附属 + 台账行 + 本地空目录。Emby 通常已经把文件删掉了，
	// 这里主要是收尾台账，顺带清掉 Emby 没删干净的附属文件
	removed := dropLocalByFids(root, fids)
	if len(removed) > 0 {
		log.Printf("[深度删除] ○ 顺带清理本地残留 %d 个", len(removed))
	}
	h.cleanDeepDelMarks(rels)
	// dropLocalByFids 已发送删除通知，避免重复刷新 Emby。

	if cfg.prunePanDirs() {
		res.PanDirs = h.pruneDeepDelDirs(ops, panPaths)
	}

	log.Printf("[深度删除] ✓ %s：删除网盘文件 %d 个（视频 %d / 附属 %d），空目录 %d 个",
		res.Title, res.Fids, res.Videos, res.Assets, res.PanDirs)
	h.noteDeepDelete(reason, res, rels, fids, "done", "")
	if cfg.notify() {
		go NotifyMessage("🗑️ 深度删除", fmt.Sprintf(
			"%s\n已删除网盘源文件 %d 个（视频 %d / 附属 %d）\n文件在 115 回收站里，可还原",
			res.Title, res.Fids, res.Videos, res.Assets))
	}
	return res, nil
}

// cleanDeepDelMarks 清掉元数据回传的上传指纹。
// dropLocalByFids 已经把台账行删了，但 UploadMark 是按本地绝对路径存的另一张表，
// 留着不致命（下次同名文件出现会因 mtime/size 不匹配重传），顺手清掉更干净
func (h *Handler) cleanDeepDelMarks(rels []string) {
	root := h.orphanLocalRoot()
	paths := make([]string, 0, len(rels))
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		paths = append(paths, filepath.Join(root, filepath.FromSlash(rel)))
	}
	for _, batch := range chunkStrings(paths, 200) {
		h.DB.Where("path IN ?", batch).Delete(&model.UploadMark{})
	}
}

// deepDelParentPaths 待清理的网盘空目录候选（绝对路径，从深到浅）。
//
// **零额外 115 请求**：台账只有 rel_path，但 rel_path 的第一层就是同步根的目录名，
// 而同步根的网盘绝对路径 absPathOf 走的是 PathCache 缓存。两者一拼就是文件的
// 网盘绝对路径，再取父目录即可。拿不到就返回空 —— 宁可在网盘上留个空目录，不猜。
func (h *Handler) deepDelParentPaths(rows []model.SyncedFile) []string {
	fullCfg := h.loadFullSyncCfg()
	if fullCfg.Cid == "" || fullCfg.Cid == "0" {
		return nil
	}
	cookie, err := h.get115Cookie()
	if err != nil || cookie == "" {
		return nil
	}
	rootAbs := absPathOf(cookie, fullCfg.Cid)
	if rootAbs == "" {
		log.Printf("[深度删除] ○ 取不到媒体库的网盘绝对路径，跳过空目录清理")
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
		i := strings.Index(rel, "/")
		if i <= 0 {
			continue // 直接躺在库根下的文件，父目录就是库根，不清
		}
		dir := path.Dir(rel[i+1:])
		// 一路往上收到库根为止：删完一季 → 季目录空 → 标题目录也空，
		// 本地侧 removeEmptyParents 同样是一路删到 root 的，两边保持一致。
		// 工作区根与媒体库根由 pruneDeepDelDirs 的 protected 兜底，永远删不掉
		for dir != "." && dir != "/" && dir != "" {
			abs := path.Join(rootAbs, dir)
			if abs != rootAbs && !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
			dir = path.Dir(dir)
		}
	}
	return out
}

// pruneDeepDelDirs 清理因删除而变空的网盘目录。
// cid 从 PathCache 按路径反查（Path 上有索引），查不到就跳过 —— 不为此新增 115 请求。
func (h *Handler) pruneDeepDelDirs(ops dirIO, panPaths []string) int {
	var protected []string
	if cfg, err := h.loadOrgConfig(); err == nil && cfg != nil {
		protected = orgProtectedCids(cfg)
	}
	protected = append(protected, h.loadFullSyncCfg().Cid, "0")
	guard := map[string]bool{}
	for _, cid := range protected {
		guard[cid] = true
	}
	// 多个季目录必须先于共同父目录检查，否则父目录只检查一次时会漏掉收尾。
	paths := append([]string(nil), panPaths...)
	sort.Slice(paths, func(i, j int) bool { return strings.Count(paths[i], "/") > strings.Count(paths[j], "/") })
	seen := map[string]bool{}
	removed := 0
	for _, abs := range paths {
		var row model.PathCache
		if h.DB.Where("path = ?", abs).First(&row).Error != nil {
			continue
		}
		if row.FileID == "" || seen[row.FileID] || guard[row.FileID] {
			continue
		}
		seen[row.FileID] = true
		if pruneDeepDelEmptyDir(ops, row.FileID, abs) {
			removed++
		}
	}
	return removed
}

// 这里只检查本次文件的祖先链，不复用整理的递归清理器。
// 旧实现检查「电影」父目录时会深入所有兄弟影片，甚至删除无关的空影片目录。
func pruneDeepDelEmptyDir(ops dirIO, cid, label string) bool {
	if cid == "" || cid == "0" {
		return false
	}
	entries, total, err := ops.listEntries(cid, 0)
	if err != nil || len(entries) != 0 || total != 0 {
		return false
	}
	if err := ops.deleteFiles([]string{cid}); err != nil {
		log.Printf("[深度删除] ✗ 空目录清理失败 %s: %v", label, err)
		return false
	}
	log.Printf("[深度删除] ○ 已删除空文件夹: %s（cid=%s，在 115 回收站，可还原）", label, cid)
	return true
}

// noteDeepDelete 落一条流水。删除进的是回收站，用户事后要还原就得靠这里的 fid
func (h *Handler) noteDeepDelete(reason string, res deepDelResult, rels, fids []string, status, msg string) {
	relJSON, _ := json.Marshal(rels)
	fidJSON, _ := json.Marshal(fids)
	rec := model.DeepDeleteRecord{
		Reason: reason, Title: res.Title,
		RelPaths: string(relJSON), Fids: string(fidJSON),
		VideoCnt: res.Videos, AssetCnt: res.Assets, PanDirs: res.PanDirs,
		Status: status, Message: truncateStr(msg, 480),
	}
	if err := h.DB.Create(&rec).Error; err != nil {
		log.Printf("[深度删除] ✗ 流水写入失败: %v", err)
	}
}

// deepDelTitle 从相对路径里推一个给人看的片名。
// 目录约定是 库名/电影|剧集/分类/标题目录/…（与 ledger.go 的 scanLedgerTitles 同构）
func deepDelTitle(rels []string) string {
	titles := map[string]bool{}
	var first string
	for _, rel := range rels {
		segs := strings.Split(strings.Trim(filepath.ToSlash(rel), "/"), "/")
		var dir string
		switch {
		case len(segs) >= 4:
			dir = segs[3]
		case len(segs) >= 2:
			dir = segs[1]
		default:
			continue
		}
		t, year, _ := parseTitleDir(dir)
		if year != "" {
			t += " (" + year + ")"
		}
		if t != "" && !titles[t] {
			titles[t] = true
			if first == "" {
				first = t
			}
		}
	}
	switch {
	case first == "":
		return fmt.Sprintf("%d 个文件", len(rels))
	case len(titles) > 1:
		return fmt.Sprintf("%s 等 %d 项", first, len(titles))
	default:
		return first
	}
}

// chunkStrings 把字符串列表切成固定大小的批次（chunkIDs 的 string 版）
func chunkStrings(ss []string, size int) [][]string {
	var out [][]string
	for len(ss) > size {
		out = append(out, ss[:size])
		ss = ss[size:]
	}
	if len(ss) > 0 {
		out = append(out, ss)
	}
	return out
}

// pruneDeepDeleteRecords 清理过期流水（随 pruneSyncEvents 每日一次）
func pruneDeepDeleteRecords() {
	if model.DB == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -deepDelKeepDays)
	res := model.DB.Where("created_at < ?", cutoff).Delete(&model.DeepDeleteRecord{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[系统] ○ 清理 %d 条 %d 天前的深度删除流水", res.RowsAffected, deepDelKeepDays)
	}
}

// DeepDeleteOrganizeRecord 按整理记录深度删除。POST /organize/records/:id/deep-delete
//
// 与事件联动不同：事件的前提是「本地文件已经没了」，这里是用户指定一条
// 整理记录，把它整理出来的东西彻底删掉**（整理识别错了、片源不想要了，想连带
// 网盘一起清干净重来）。所以两轮确认与量级阈值都不适用 —— 用户点的是具体某一行，
// 范围由那条记录自己的文件清单界定，不存在「误判一大片」的形态。
//
// 仍然守住的：**只删台账里有的 fid**。记录里的 fid 是整理自己写下的（`OrganizeRecord.Files`，
// fid 在 115 上移动改名后不变），再与台账对一遍，对不上就拒绝 —— 那说明这些文件
// 压根没进过媒体库（整理失败/未识别，东西还在待整理里），不该从这里删。
func (h *Handler) DeepDeleteOrganizeRecord(c *gin.Context) {
	var legacy struct {
		DryRun bool `json:"dry_run"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&legacy); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效请求"})
			return
		}
		if legacy.DryRun {
			c.JSON(http.StatusBadRequest, gin.H{"error": "预演已移除，请刷新页面后操作"})
			return
		}
	}

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "同步任务进行中，请稍后再试"})
		return
	}
	defer fullSyncMu.Unlock()

	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	rows, total, err := deepDelRowsForRecord(h.DB, rec)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "这些文件不在媒体库台账里，深度删除不处理：整理失败或未识别的内容还在待整理目录，请到 115 里直接处理",
		})
		return
	}

	res, err := h.runDeepDelete(rows, "manual_record")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 记录本身留着（它是流水，删了就查不到这次整理发生过什么），但要标一笔 ——
	// 否则用户回头看到一条 success 记录，点「重新整理」却发现文件早没了
	note := strings.TrimSpace(rec.Message + " ｜ 已深度删除：网盘源文件在 115 回收站")
	h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).
		Update("message", truncateStr(strings.TrimPrefix(note, "｜ "), 480))
	msg := fmt.Sprintf("已删除《%s》的网盘源文件 %d 个（视频 %d / 附属 %d），在 115 回收站可还原",
		rec.Title, res.Fids, res.Videos, res.Assets)
	c.JSON(http.StatusOK, gin.H{
		"message": msg, "removed": res.Fids,
		"videos": res.Videos, "assets": res.Assets, "pan_dirs": res.PanDirs,
		// 台账里查不到的那些 fid：整理时落过盘、后来被移走或删掉了，如实报出来
		"skipped": total - len(rows),
	})
}

// deepDelRowsForRecord 整理记录 → 可删的台账行，以及记录里一共有几个 fid。
//
// **守卫在这里**：记录里的 fid 只用来查台账，查得到的才删。查不到说明这些文件
// 没进过媒体库（整理失败/未识别，东西还在待整理里）或早被移走了，都不该从这里删。
// 抽成函数是为了能单测这条判断 —— handler 里要真 115 通道，测不了。
func deepDelRowsForRecord(db *gorm.DB, rec model.OrganizeRecord) (rows []model.SyncedFile, total int, err error) {
	var fids []string
	for _, f := range unmarshalRecordFiles(rec.Files) {
		if f.Fid != "" {
			fids = append(fids, f.Fid)
		}
	}
	if len(fids) == 0 {
		return nil, 0, fmt.Errorf("这条记录没有留下文件信息，无法定位要删什么")
	}
	for _, batch := range chunkStrings(fids, 400) {
		var part []model.SyncedFile
		if e := db.Where("file_id IN ?", batch).Find(&part).Error; e != nil {
			return nil, len(fids), fmt.Errorf("读取台账失败: %w", e)
		}
		rows = append(rows, part...)
	}
	return rows, len(fids), nil
}

// ListDeepDeleteRecords 深度删除流水。GET /sync/deep-delete/records
func (h *Handler) ListDeepDeleteRecords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	var total int64
	h.DB.Model(&model.DeepDeleteRecord{}).Count(&total)
	var rows []model.DeepDeleteRecord
	h.DB.Order("created_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows)

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		var rels []string
		_ = json.Unmarshal([]byte(r.RelPaths), &rels)
		items = append(items, gin.H{
			"id": r.ID, "reason": r.Reason, "title": r.Title,
			"video_cnt": r.VideoCnt, "asset_cnt": r.AssetCnt, "pan_dirs": r.PanDirs,
			"status": r.Status, "message": r.Message,
			"rel_paths":  rels,
			"created_at": r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "size": size})
}
