package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 深度删除 ====================
//
// 深度删除 = **本地 STRM 没了、网盘源文件还在** → 把网盘上的源文件也删掉。
//
// 在 Emby 里删掉一部片子，Emby 会连带删掉磁盘上的 .strm / .nfo / 海报，
// 但网盘上的源文件纹丝不动 —— 下一次全量同步又把 STRM 生成回来，条目原地复活。
// 想真删只能手动再去 115 客户端删一遍。这个文件补的就是这个单向缺口。
//
// 它是「失效 STRM」（orphan115.go）的镜像：
//   失效 STRM —— 网盘没了、本地还在 → 删本地
//   深度删除 —— 本地没了、网盘还在 → 删网盘
// 所以扫描 / 打标 / 预览 / 确认才删这套流程也照着那边来，用户看到的是对称的两张卡。
//
// ⚠️ 这是本仓库**第二个会真删用户网盘内容**的链路（第一个是 emptydir.go 的空目录
// 清理）。守卫一条都不能松，逐条见下面各函数的注释：
//  1. 默认关闭，开启后默认「只标记不删」；
//  2. 只删台账里有的 fid —— 不提供任何「用户传路径」的入口；
//  3. 媒体库根 / 某个库目录读不出来（挂载掉了）→ 整轮放弃，不是「那就都删了」；
//  4. 自动模式下超过条数/比例阈值 → 拒绝执行，只标记 + 告警；
//  5. 两轮确认：首轮只打标，下一轮仍缺失才进可删集合；
//  6. 删除走 ops.deleteFiles → /rb/delete，**进 115 回收站可还原**。
//
// 参考 qmediasync 的 Emby 联动删除与 p115strmhelper 的 mediasyncdel（见
// docs/115-station-notes/REFERENCES.md）。两家都要先建一张「媒体服务器条目 ↔
// 网盘文件」的映射表，我们的 SyncedFile 台账本来就是这张表，省掉了那一步。

const (
	// deepDelSampleLimit 预览返回多少条（前端只做抽样展示，不下发全量清单）
	deepDelSampleLimit = 50
	// deepDelScanDefault / deepDelScanMin 本地消失扫描的间隔。
	// 纯本地 os.Stat，一个 115 请求都不发，所以可以比增量轮询更密
	deepDelScanDefault = 5 * time.Minute
	deepDelScanMin     = time.Minute
	// deepDelMaxBatchDefault 自动模式单轮可删视频数上限。
	// 取 50 是因为一整季（含附属）通常在这个量级以内，而「整个库消失」这种
	// 挂载事故一定远远超过它 —— 阈值要卡在正常与灾难之间
	deepDelMaxBatchDefault = 50
	// deepDelMaxRatioDefault 自动模式单轮可删文件数占台账的比例上限
	deepDelMaxRatioDefault = 0.1
	// deepDelChunk 删除分批大小：一次塞太多 fid 的表单 115 会拒
	deepDelChunk = 100
	// deepDelKeepDays 流水保留天数。比整理记录长一倍：回收站里的东西
	// 用户往往过很久才想起来要还原
	deepDelKeepDays = 180
)

// deepDelCfg 深度删除配置，独立 setting key "deepdel"。
//
// 一开始塞在 setting "full" 的 deep_delete 字段下（图省事，界面也摆在全量同步页），
// 但那是摆错了：**深度删除与全量同步没有任何依赖关系**。失效 STRM 检测放在全量页
// 是因为标记就是在全量同步末尾打的、定时全量存在的意义就是刷新它；深度删除是
// 独立的本地扫描 + webhook，跟全量唯一的交集只是抢同一把锁——那是实现细节。
// 界面挪到自己的页签后，配置也必须跟着拆出来：同一个 key 被两个页面整存整取，
// 就是 incr.cron / incr.interval_sec 那个互相覆盖的坑。
//
// 指针字段是为了区分「没配过」和「显式填了零值」：dry_run 没配过要当 true
// （第一次开启强制预演），填了 false 才是真的关掉预演。
type deepDelCfg struct {
	Enabled      bool     `json:"enabled"`
	Mode         string   `json:"mode"`    // mark（只标记）/ auto（自动删除）
	DryRun       *bool    `json:"dry_run"` // 预演：只打日志不动手
	ScanInterval *int     `json:"scan_interval_sec"`
	MaxBatch     *int     `json:"max_batch"`
	MaxRatio     *float64 `json:"max_ratio"`
	PrunePanDirs *bool    `json:"prune_pan_dirs"`
	Notify       *bool    `json:"notify"`
}

func (c deepDelCfg) auto() bool   { return c.Mode == "auto" }
func (c deepDelCfg) dryRun() bool { return c.DryRun == nil || *c.DryRun }
func (c deepDelCfg) prunePanDirs() bool {
	return c.PrunePanDirs == nil || *c.PrunePanDirs
}
func (c deepDelCfg) notify() bool { return c.Notify == nil || *c.Notify }

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

// interval 扫描间隔。显式填 0 = 关掉后台扫描，只留界面上的手动按钮（逃生门）
func (c deepDelCfg) interval() time.Duration {
	if c.ScanInterval == nil {
		return deepDelScanDefault
	}
	if *c.ScanInterval <= 0 {
		return 0
	}
	if d := time.Duration(*c.ScanInterval) * time.Second; d > deepDelScanMin {
		return d
	}
	return deepDelScanMin
}

// loadDeepDelCfg 读配置。走 getSettingValue 与 loadFullSyncCfg / orphanDetectEnabled
// 保持同一条读取路径，两处读法不一致会出现「配置改了但调度器没看见」这类
// 只在某种部署形态下复现的偏差
func (h *Handler) loadDeepDelCfg() deepDelCfg {
	var cfg deepDelCfg
	_ = json.Unmarshal([]byte(h.getSettingValue("deepdel")), &cfg)
	return cfg
}

// ---- 扫描 ----

// vanishScan 一轮本地消失扫描的结果
type vanishScan struct {
	Marked  int                // 本轮新打上标记的
	Cleared int                // 本地文件又回来了、标记被撤掉的
	Total   int                // 当前候选总数（含本轮新标记的）
	Ledger  int                // 台账总行数（算比例用）
	Ready   []model.SyncedFile // 上一轮就标过、这轮仍然缺失 —— 唯一可删的集合
}

// scanVanished 比对台账与本地磁盘，标出「本地已不存在、网盘源文件仍在」的行。
// 纯本地 IO，不发任何 115 请求。
//
// 返回 error 一律代表「本轮放弃」：这里判断错的代价是把整个网盘媒体库删进回收站，
// 所以只要有一丝读不出来的迹象就整轮不做，而不是按「读不到 = 文件没了」继续。
func scanVanished(db *gorm.DB, root string) (vanishScan, error) {
	var out vanishScan
	if db == nil {
		return out, fmt.Errorf("数据库未就绪")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return out, fmt.Errorf("未配置本地媒体库根目录")
	}
	// 根读不出来 = 挂载掉了。继续走下去，整个台账都会被判成「本地已删」，
	// 自动模式下就是一口气把网盘媒体库删空
	if _, err := os.ReadDir(root); err != nil {
		return out, fmt.Errorf("媒体库根目录读不出来（%s）: %w", root, err)
	}

	var rows []model.SyncedFile
	if err := db.Select("id", "file_id", "rel_path", "kind", "size", "orphan_at", "vanish_at").
		Find(&rows).Error; err != nil {
		return out, fmt.Errorf("读取台账失败: %w", err)
	}
	out.Ledger = len(rows)
	if err := checkLibRoots(root, rows); err != nil {
		return out, err
	}

	now := time.Now()
	var toMark, toClear []uint
	for _, r := range rows {
		if r.RelPath == "" {
			continue
		}
		// 已被判为失效 STRM 的行：网盘那边源文件也没了，没有可删的东西。
		// 不排掉的话会拿着一个早不存在的 fid 去调删除接口，白白换一个报错
		if r.OrphanAt != nil {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(r.RelPath))
		_, err := os.Stat(full)
		switch {
		case err == nil:
			if r.VanishAt != nil {
				toClear = append(toClear, r.ID)
			}
			continue
		case !os.IsNotExist(err):
			// 权限/IO 错误不等于文件没了，跳过这一条（不撤标记也不新增标记）
			log.Printf("[深度删除] ○ 跳过读不出来的本地文件 %s: %v", r.RelPath, err)
			continue
		}
		out.Total++
		if r.VanishAt == nil {
			toMark = append(toMark, r.ID)
			continue
		}
		// 上一轮就标过、这轮仍然缺失 —— 两轮确认通过
		out.Ready = append(out.Ready, r)
	}

	// 分批更新：sqlite 对单条语句的参数个数有上限，万级库一次 IN 会炸
	for _, batch := range chunkIDs(toClear, 400) {
		db.Model(&model.SyncedFile{}).Where("id IN ?", batch).Update("vanish_at", nil)
	}
	for _, batch := range chunkIDs(toMark, 400) {
		db.Model(&model.SyncedFile{}).Where("id IN ?", batch).Update("vanish_at", now)
	}
	out.Marked, out.Cleared = len(toMark), len(toClear)
	return out, nil
}

// checkLibRoots 库根探针：台账 rel_path 的第一层就是媒体库目录名（见 writeStrm
// 与 collectSyncFiles 传的 basePath）。某个库的目录整个消失，绝大多数情况是
// 挂载掉线或本地路径配置被改了，而不是用户把一整个库删干净了 —— 后者也轮不到
// 后台自动判定。命中就整轮放弃。
func checkLibRoots(root string, rows []model.SyncedFile) error {
	libs := map[string]bool{}
	for _, r := range rows {
		rel := strings.Trim(filepath.ToSlash(r.RelPath), "/")
		if i := strings.Index(rel, "/"); i > 0 {
			libs[rel[:i]] = true
		}
	}
	for lib := range libs {
		if _, err := os.Stat(filepath.Join(root, lib)); err != nil {
			return fmt.Errorf("媒体库目录「%s」读不出来（%v）—— 挂载或本地路径配置有问题，本轮放弃", lib, err)
		}
	}
	return nil
}

// deepDelOverLimit 阈值守卫。**只在自动模式下调用**：
// 用户在界面上点确认时已经看过预览清单了，那时再拦就是拦着人干正事。
//
// 挂载掉线、路径映射改错、Emby 重装重扫，这些事故的共同形态是「一口气消失一大片」，
// 量级和正常的「删一部片子/一整季」差着数量级，阈值卡的就是这个差距。
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
	DryRun  bool
	Title   string
}

// runDeepDelete 执行深度删除：删网盘源文件 → 清本地产物与台账 → 清网盘空目录 → 留痕 → 通知。
//
// 顺序不能换。先删网盘、后清台账：反过来先清台账的话，网盘删除一旦失败，
// 这批文件就再也没人记得要删了（台账行没了 = 下一轮扫描根本看不见它们）。
func (h *Handler) runDeepDelete(rows []model.SyncedFile, reason string, dryRun bool) (deepDelResult, error) {
	cfg := h.loadDeepDelCfg()
	res := deepDelResult{DryRun: dryRun}
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

	if dryRun {
		for _, rel := range rels {
			log.Printf("[深度删除] ○ 预演：将删除网盘源文件 %s", rel)
		}
		log.Printf("[深度删除] ○ 预演结束：共 %d 个文件（视频 %d / 附属 %d），未做任何改动", len(fids), res.Videos, res.Assets)
		h.noteDeepDelete(reason, res, rels, fids, "dry_run", "预演，未执行任何删除")
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
	panPaths := h.deepDelParentPaths(rows)

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
func (h *Handler) pruneDeepDelDirs(ops *pan115Ops, panPaths []string) int {
	if len(panPaths) == 0 {
		return 0
	}
	var protected []string
	if orgCfg, err := h.loadOrgConfig(); err == nil && orgCfg != nil {
		protected = orgProtectedCids(orgCfg)
	}
	protected = append(protected, h.loadFullSyncCfg().Cid)

	p := newDirPruner(ops, protected, func(s string) { log.Printf("[深度删除] %s", s) })
	marked := 0
	for _, abs := range panPaths {
		var row model.PathCache
		if h.DB.Where("path = ?", abs).First(&row).Error != nil {
			continue // 缓存里没有，跳过。补一次祖先链请求不值得
		}
		p.mark(row.FileID, abs)
		marked++
	}
	if marked == 0 {
		log.Printf("[深度删除] ○ 待清理的目录都不在路径缓存里，跳过空目录清理")
		return 0
	}
	return p.flush()
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

// ---- 后台扫描 ----

// startDeepDelScanner 本地消失扫描的独立轮询。
// 间隔每轮重读，改配置不必重启（与 startIncrPoller 同构）
func (h *Handler) startDeepDelScanner() {
	go func() {
		for {
			wait := h.loadDeepDelCfg().interval()
			if wait <= 0 {
				wait = time.Minute // 关闭状态下也每分钟回来看一眼配置改没改
			}
			select {
			case <-time.After(wait):
			case <-stopCh:
				return
			}
			h.runDeepDelScanTick()
		}
	}()
}

// runDeepDelScanTick 一轮扫描（+ 自动模式下的删除）。
//
// ⚠️ 必须和全量/增量互斥：**全量同步会把被删的 STRM 重新生成回来**（源文件还在网盘），
// 不加锁的话这轮判定就踩在半轮全量的中间状态上，一会儿判有一会儿判无。
// 抢不到锁直接跳过，下一轮再来（照 runIncrPollTick 的写法）。
func (h *Handler) runDeepDelScanTick() {
	cfg := h.loadDeepDelCfg()
	if !cfg.Enabled || cfg.interval() <= 0 {
		return
	}
	h.runDeepDelScan(cfg)
}

// runDeepDelScanNow webhook 加速通道用的即时入口（deepdelemby.go）。
// 与定时轮询的区别只有一个：**不看 interval**。把间隔填 0 是关掉「后台定时扫描」，
// 而 webhook 是用户在 Emby 里的明确动作触发的，不属于后台轮询
func (h *Handler) runDeepDelScanNow() {
	cfg := h.loadDeepDelCfg()
	if !cfg.Enabled {
		return
	}
	if !h.runDeepDelScan(cfg) {
		// 标记还在，下一轮定时扫描会接着处理 —— 但不说一声的话，
		// 用户看到的就是「点了深度删除，网盘没动静，日志里也没话」
		log.Printf("[深度删除] ○ 同步任务占用中，本次不立即执行，标记已留下（下一轮扫描会接手）")
	}
}

// runDeepDelScan 返回是否真的跑了（false = 抢不到锁）
func (h *Handler) runDeepDelScan(cfg deepDelCfg) bool {
	if !fullSyncMu.TryLock() {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[深度删除] ✗ 扫描 panic 已恢复: %v", r)
		}
		fullSyncMu.Unlock()
	}()

	scan, err := scanVanished(h.DB, h.orphanLocalRoot())
	if err != nil {
		log.Printf("[深度删除] ✗ 本轮放弃: %v", err)
		return true
	}
	// 空转轮次完全静默（5 分钟一轮，静默才不刷屏）
	if scan.Marked > 0 || scan.Cleared > 0 {
		log.Printf("[深度删除] ○ 扫描：新增本地已删 %d 个，本地已恢复 %d 个，当前候选 %d 个",
			scan.Marked, scan.Cleared, scan.Total)
	}
	if len(scan.Ready) == 0 {
		return true
	}
	if !cfg.auto() {
		// 有货但模式是「只标记」：说一声，否则用户以为功能压根没生效
		log.Printf("[深度删除] ○ %d 个条目已确认待删，但当前是「只标记」模式 —— "+
			"到「Strm 管理 → 全量同步 → 深度删除」确认后执行", len(scan.Ready))
		return true
	}

	videos, assets := countKinds(scan.Ready)
	if over, why := deepDelOverLimit(cfg, videos, videos+assets, scan.Ledger); over {
		log.Printf("[深度删除] ✗ 自动删除被阈值拦下：%s", why)
		h.noteDeepDelete("local_scan", deepDelResult{Videos: videos, Assets: assets}, nil, nil, "rejected", why)
		if cfg.notify() {
			go NotifyMessage("⚠️ 深度删除已拦截", why+
				"\n多半是挂载掉线或路径配置变了。标记已保留，确认无误后到「Strm 管理 → 全量同步」手动执行。")
		}
		return true
	}
	if _, err := h.runDeepDelete(scan.Ready, "local_scan", cfg.dryRun()); err != nil {
		log.Printf("[深度删除] ✗ 自动删除失败: %v", err)
	}
	return true
}

// ---- 接口 ----

// ListDeepDelete 深度删除预览。GET /sync/deep-delete
//
// 开着的时候顺手跑一轮扫描再返回：这趟是纯本地 os.Stat，不发 115 请求，
// 用户打开页面看到的就该是此刻的真实状态，而不是上一轮轮询留下的快照。
// 锁被占用（全量在跑）时退回只读已有标记，并告诉前端这次没扫。
func (h *Handler) ListDeepDelete(c *gin.Context) {
	cfg := h.loadDeepDelCfg()
	scanned := false
	scanErr := ""
	if cfg.Enabled {
		if fullSyncMu.TryLock() {
			_, err := scanVanished(h.DB, h.orphanLocalRoot())
			fullSyncMu.Unlock()
			if err != nil {
				scanErr = err.Error()
			} else {
				scanned = true
			}
		}
	}

	var total, ledger int64
	h.DB.Model(&model.SyncedFile{}).Where("vanish_at IS NOT NULL AND orphan_at IS NULL").Count(&total)
	h.DB.Model(&model.SyncedFile{}).Count(&ledger)

	var rows []model.SyncedFile
	h.DB.Where("vanish_at IS NOT NULL AND orphan_at IS NULL").
		Order("rel_path").Limit(deepDelSampleLimit).Find(&rows)
	sample := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		marked := ""
		if r.VanishAt != nil {
			marked = r.VanishAt.Format("2006-01-02 15:04")
		}
		sample = append(sample, gin.H{
			"rel_path": r.RelPath, "kind": r.Kind, "size": r.Size, "marked_at": marked,
		})
	}

	ratio := 0.0
	if ledger > 0 {
		ratio = float64(total) / float64(ledger)
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":      cfg.Enabled,
		"mode":         cfg.Mode,
		"dry_run":      cfg.dryRun(),
		"scanned":      scanned,
		"scan_error":   scanErr,
		"total":        total,
		"ledger_total": ledger,
		"ratio":        ratio,
		"sample":       sample,
		"sample_limit": deepDelSampleLimit,
	})
}

// RunDeepDelete 执行深度删除。POST /sync/deep-delete/run  body: {"dry_run":true}
//
// 不接受任何「要删哪些路径」的入参：删什么完全由台账 + 本轮扫描决定。
// 用户能控制的只有「删不删」，不是「删哪个」—— 传路径的接口等于把守卫全绕过去了。
//
// 阈值只拦自动模式，这里不拦：用户已经在界面上看过清单并确认了。
func (h *Handler) RunDeepDelete(c *gin.Context) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&req)

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "同步任务进行中，请稍后再试"})
		return
	}
	defer fullSyncMu.Unlock()

	// 执行前重扫一遍：从打开页面到点确认之间文件可能又回来了（Emby 重新刮削、
	// 用户手动恢复），拿旧标记直接删就是删错
	scan, err := scanVanished(h.DB, h.orphanLocalRoot())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(scan.Ready) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "没有可删除的条目（新标记的要等下一轮确认）", "removed": 0})
		return
	}
	res, err := h.runDeepDelete(scan.Ready, "manual", req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	msg := fmt.Sprintf("深度删除完成：网盘源文件 %d 个（视频 %d / 附属 %d）", res.Fids, res.Videos, res.Assets)
	if res.DryRun {
		msg = fmt.Sprintf("预演完成：将删除 %d 个网盘源文件（视频 %d / 附属 %d），未做任何改动", res.Fids, res.Videos, res.Assets)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": msg, "dry_run": res.DryRun, "removed": res.Fids,
		"videos": res.Videos, "assets": res.Assets, "pan_dirs": res.PanDirs,
	})
}

// DeepDeleteOrganizeRecord 按整理记录深度删除。POST /organize/records/:id/deep-delete
//
// 与触发器 A/B 不同源：那两个的前提是「本地文件已经没了」，这里是**用户指定一条
// 整理记录，把它整理出来的东西彻底删掉**（整理识别错了、片源不想要了，想连带
// 网盘一起清干净重来）。所以两轮确认与量级阈值都不适用 —— 用户点的是具体某一行，
// 范围由那条记录自己的文件清单界定，不存在「误判一大片」的形态。
//
// 仍然守住的：**只删台账里有的 fid**。记录里的 fid 是整理自己写下的（`OrganizeRecord.Files`，
// fid 在 115 上移动改名后不变），再与台账对一遍，对不上就拒绝 —— 那说明这些文件
// 压根没进过媒体库（整理失败/未识别，东西还在待整理里），不该从这里删。
func (h *Handler) DeepDeleteOrganizeRecord(c *gin.Context) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = c.ShouldBindJSON(&req)

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

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "同步任务进行中，请稍后再试"})
		return
	}
	defer fullSyncMu.Unlock()

	res, err := h.runDeepDelete(rows, "manual_record", req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !res.DryRun {
		// 记录本身留着（它是流水，删了就查不到这次整理发生过什么），但要标一笔 ——
		// 否则用户回头看到一条 success 记录，点「重新整理」却发现文件早没了
		note := strings.TrimSpace(rec.Message + " ｜ 已深度删除：网盘源文件在 115 回收站")
		h.DB.Model(&model.OrganizeRecord{}).Where("id = ?", rec.ID).
			Update("message", truncateStr(strings.TrimPrefix(note, "｜ "), 480))
	}
	msg := fmt.Sprintf("已删除《%s》的网盘源文件 %d 个（视频 %d / 附属 %d），在 115 回收站可还原",
		rec.Title, res.Fids, res.Videos, res.Assets)
	if res.DryRun {
		msg = fmt.Sprintf("预演：将删除《%s》的 %d 个网盘源文件（视频 %d / 附属 %d），未做任何改动",
			rec.Title, res.Fids, res.Videos, res.Assets)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": msg, "dry_run": res.DryRun, "removed": res.Fids,
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
