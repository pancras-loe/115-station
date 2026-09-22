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
	deepDelChunk    = 100
	deepDelKeepDays = 180
)

type deepDelCfg struct {
	Enabled      bool  `json:"enabled"`
	PrunePanDirs *bool `json:"prune_pan_dirs"`
	Notify       *bool `json:"notify"`
}

func (c deepDelCfg) prunePanDirs() bool { return c.PrunePanDirs == nil || *c.PrunePanDirs }
func (c deepDelCfg) notify() bool       { return c.Notify == nil || *c.Notify }
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

	// 空目录清理放在删除之后：目录要空了才判得出来，而目录本身不会因为删文件消失，
	// 逐级按名字查 cid 这条路删完照样走得通
	if cfg.prunePanDirs() {
		res.PanDirs = h.pruneDeepDelDirs(ops, h.loadFullSyncCfg().Cid, rows)
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

// deepDelDirTargets 本次要检查的网盘目录：键是相对同步根的目录路径（如「电影/流浪地球-2019」），
// 值表示这一级是否**直接**装过被删的文件（叶子目录），false 是它上面的祖先。
//
// rel_path 的第一层就是同步根自己的目录名（walk115Dir 的 basePath 就是它），
// 去掉之后剩下的正是网盘上同一棵树的相对路径 —— 本地与网盘共用一套目录结构，
// 不必猜也不必额外请求
func deepDelDirTargets(rows []model.SyncedFile) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
		i := strings.Index(rel, "/")
		if i <= 0 {
			continue
		}
		dir := path.Dir(rel[i+1:])
		if dir == "." || dir == "/" || dir == "" {
			continue // 文件直接躺在同步根下，父目录就是库根，不清
		}
		out[dir] = true
		// 一路往上收到库根为止：删完一季 → 季目录空 → 标题目录也空，
		// 本地侧 removeEmptyParents 同样是一路删到 root 的，两边保持一致
		for d := path.Dir(dir); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if _, ok := out[d]; !ok {
				out[d] = false
			}
		}
	}
	return out
}

// resolveDeepDelDirCids 把相对同步根的目录路径逐级列目录解析成 cid。
//
// **为什么不查 PathCache**：那张表只在解析生活事件的祖先链时才写入，全量同步建起来的
// 媒体库目录压根没进去过。此前按 path 反查缓存、查不到就跳过的做法在这条链路上几乎必然落空，
// 结果就是网盘上的影片目录、季目录删空了却一直留着（用户实测如此）。
// 换成逐级列目录：判断「空不空」本来就得列一次目录，顺路把 cid 查出来不多花几个请求，
// 而且拿到的是当下的真实结构，不存在缓存陈旧删错目录的风险。
func resolveDeepDelDirCids(ops dirIO, rootCid string, rels []string) map[string]string {
	cids := map[string]string{"": rootCid}
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted) // 字典序天然是父目录在前，父解析完子才有得查
	for _, rel := range sorted {
		cur := ""
		for _, seg := range strings.Split(rel, "/") {
			parent := cids[cur]
			cur = path.Join(cur, seg)
			if c, ok := cids[cur]; ok {
				if c == "" {
					break // 上一轮就查不到，同层的别再列一遍
				}
				continue
			}
			c, err := findChildDirCid(ops, parent, seg)
			if err != nil {
				log.Printf("[深度删除] ○ 读不出目录内容，跳过空目录清理: %s（%v）", cur, err)
			}
			cids[cur] = c
			if c == "" {
				break
			}
		}
	}
	return cids
}

// findChildDirCid 在 parent 下按名字找子目录（翻页找全：一层条目超一页时只看第一页会漏）
func findChildDirCid(ops dirIO, parent, name string) (string, error) {
	if parent == "" {
		return "", nil
	}
	offset := 0
	for {
		entries, total, err := ops.listEntries(parent, offset)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if fmt.Sprint(e["f"]) == "0" && fmt.Sprint(e["n"]) == name {
				return fmt.Sprint(e["cid"]), nil
			}
		}
		offset += len(entries)
		if len(entries) == 0 || offset >= total {
			return "", nil
		}
	}
}

// pruneDeepDelDirs 清理因删除而空下来的网盘目录，返回删除数量。
//
// 分两种处理，界线就是「文件原本在哪一级」：
//   - 叶子目录（影片目录 / 季目录）走 pruneEmptyDirTree —— 整棵子树没有任何文件才删，
//     顺带把剩下的空季目录一起收掉；
//   - 再往上的祖先（分类目录「电影」「剧集」）只接受**自己完全为空**，绝不递归进去，
//     否则清理分类目录时会遍历兄弟影片，把无关的空目录也删了。
func (h *Handler) pruneDeepDelDirs(ops dirIO, rootCid string, rows []model.SyncedFile) int {
	if rootCid == "" || rootCid == "0" {
		return 0
	}
	targets := deepDelDirTargets(rows)
	if len(targets) == 0 {
		return 0
	}
	rels := make([]string, 0, len(targets))
	for rel := range targets {
		rels = append(rels, rel)
	}
	cids := resolveDeepDelDirCids(ops, rootCid, rels)

	guard := map[string]bool{rootCid: true, "0": true}
	if cfg, err := h.loadOrgConfig(); err == nil && cfg != nil {
		for _, cid := range orgProtectedCids(cfg) {
			if cid != "" {
				guard[cid] = true
			}
		}
	}
	// 深的先处理：季目录删掉之后，标题目录才可能跟着空
	sort.Slice(rels, func(i, j int) bool { return strings.Count(rels[i], "/") > strings.Count(rels[j], "/") })
	onLog := func(msg string) { log.Printf("[深度删除] %s", msg) }
	seen := map[string]bool{}
	removed := 0
	for _, rel := range rels {
		cid := cids[rel]
		if cid == "" || guard[cid] || seen[cid] {
			continue
		}
		seen[cid] = true
		if targets[rel] {
			n, _ := pruneEmptyDirTree(ops, cid, guard, 0, rel, onLog)
			removed += n
			continue
		}
		if pruneDeepDelEmptyDir(ops, cid, rel) {
			removed++
		}
	}
	return removed
}

// pruneDeepDelEmptyDir 祖先目录（分类目录那一层）专用：只删**自己一个子项都没有**的目录，
// 绝不递归下去 —— 旧实现拿递归清理器检查「电影」父目录时会深入所有兄弟影片，
// 把无关的空影片目录也删了。
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

	if !taskMu.Acquire("深度删除", manualAcquireWait) {
		c.JSON(http.StatusConflict, gin.H{"error": busyErr()})
		return
	}
	defer taskMu.Unlock()

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
