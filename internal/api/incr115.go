package api

import (
	"encoding/json"
	"errors"
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
	"gorm.io/gorm/clause"
)

// ==================== 生活事件增量 ====================
//
// 事件拉取层在 life115.go（端点选择、游标、分页、忽略类过滤、405 降级、开关门禁）。
// 这里只负责消费：把事件应用到本地 STRM 与台账。
//
// 115 的事件流本身缺「回收站还原」——还原了只是对应的删除事件消失，
// 不会补一条新增，所以还原过的内容只有全量整库扫描能补回来

// 关键操作类型（type 字段，数字或字符串均可能返回）
const (
	evUploadImage  = "upload_image_file" // 1 上传图片
	evUpload       = "upload_file"       // 2 上传文件/目录
	evMove         = "move_file"         // 6 移动文件/目录
	evReceive      = "receive_files"     // 14 接收文件（转存）
	evNewFolder    = "new_folder"        // 17 新增目录
	evCopyFolder   = "copy_folder"       // 18 复制目录（目录转存/复制，按目录新增处理）
	evFolderRename = "folder_rename"     // 20 目录改名
	evMoveImage    = "move_image_file"   // 5 移动图片（同移动处理）
	evDelete       = "delete_file"       // 22 删除文件/目录
	evCopy         = "copy_file"         // 23 复制文件
	evRename       = "file_rename"       // 24 文件改名
)

// lifeEvent 一条生活事件
type lifeEvent struct {
	ID       string `json:"id"`        // 115 事件 id（单调递增，增量游标/去重用）
	Type     string `json:"type"`      // 归一化后的操作类型
	FileID   string `json:"file_id"`   // 文件 id
	FileName string `json:"file_name"` // 文件名
	Cid      string `json:"cid"`       // 父目录 cid
	PickCode string `json:"-"`         // 事件自带的 pick_code（有则零遍历直推 strm）
	FileCat  string `json:"-"`         // file_category："0"=目录 "1"=文件
	Size     int64  `json:"size"`      // 文件大小
	Time     string `json:"time"`      // 发生时间
}

// normalizeEventType 把数字或字符串类型归一化为字符串
func normalizeEventType(v string) string {
	switch v {
	case "1", "upload_image_file":
		return evUploadImage
	case "2", "upload_file":
		return evUpload
	case "6", "move_file":
		return evMove
	case "14", "receive_files":
		return evReceive
	case "17", "new_folder":
		return evNewFolder
	case "18", "copy_folder":
		return evCopyFolder
	case "5", "move_image_file":
		return evMoveImage
	case "20", "folder_rename":
		return evFolderRename
	case "22", "delete_file":
		return evDelete
	case "23", "copy_file":
		return evCopy
	case "24", "file_rename":
		return evRename
	default:
		return v
	}
}

// firstStr 从多个候选字段名取第一个非空值
func firstStr(d map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v := fmt.Sprint(d[k]); v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

// dirInfo 目录自身的 cid/pid/名称
type dirInfo struct {
	cid, pid, n string
}

// get115DirInfo 查询目录自身的 cid/pid/名称。
// 改用祖先链实现（见 panpath.go）：一次请求顺带把整条链都捂进缓存，
// 比原先的 files/get_info 多拿到上面每一级。
// ⚠️ 仅对【目录】有效
func get115DirInfo(cookie, cid string) (dirInfo, error) {
	if row, ok := lookupCachedRow(cid); ok {
		return dirInfo{cid: row.FileID, pid: row.ParentID, n: row.Name}, nil
	}
	if _, err := resolveDirAbsFresh(cookie, cid); err != nil {
		return dirInfo{}, err
	}
	row, ok := lookupCachedRow(cid)
	if !ok {
		return dirInfo{}, errDirGone
	}
	return dirInfo{cid: row.FileID, pid: row.ParentID, n: row.Name}, nil
}

// get115RelPath 目录相对媒体库根的路径。不在库内返回 ok=false
func get115RelPath(cookie, cid, rootCid string) (string, bool, error) {
	if cid == rootCid {
		return "", true, nil
	}
	abs, err := resolveDirAbs(cookie, cid)
	if err != nil {
		return "", false, err
	}
	rootAbs, err := resolveDirAbs(cookie, rootCid)
	if err != nil {
		return "", false, err
	}
	rootAbs = strings.TrimSuffix(rootAbs, "/")
	if abs == rootAbs {
		return "", true, nil
	}
	if !strings.HasPrefix(abs, rootAbs+"/") {
		return "", false, nil // 不在媒体库内
	}
	return strings.TrimPrefix(abs, rootAbs+"/"), true, nil
}

// incrParams 增量同步参数（HTTP 与 cron 调度器共用）
type incrParams struct {
	Cid       string
	LocalPath string
	VideoExt  []string
	ImageExt  []string
	DataExt   []string
	Limit     int
}

// incrSummary 增量同步结果摘要
type incrSummary struct {
	Round            uint64 `json:"round"` // 轮次号，对应日志里的 [同步#N]
	EventsTotal      int    `json:"events_total"`
	EventsFresh      int    `json:"events_fresh"`
	EventsPending    int    `json:"events_pending"` // 本轮实际要处理的条数（含上轮遗留）
	Relevant         int    `json:"relevant"`
	Structural       int    `json:"structural"`
	Deleted          int    `json:"deleted"`
	Moved            int    `json:"moved"`
	Dirs             int    `json:"dirs"`
	Videos           int    `json:"videos"`
	StrmCreated      int    `json:"strm_created"`
	StrmExisting     int    `json:"strm_existing"` // 本地已有且内容一致，没动
	AssetsTotal      int    `json:"assets_total"`
	AssetsDownloaded int    `json:"assets_downloaded"`
	AssetsSkipped    int    `json:"assets_skipped"`
	AssetsFailed     int    `json:"assets_failed"`
	Ignored          int    `json:"ignored"`    // 非媒体库区域（待整理/已存在/冗余等）的事件
	Suppressed       int    `json:"suppressed"` // 整理自己产生、已由整理落盘的变更，本轮跳过
	Elapsed          string `json:"elapsed"`

	// ---- 回退目录遍历的账（排查「为什么这一轮跑了十几分钟」）----
	DirsShallow int `json:"dirs_shallow"` // 浅遍历目标数（只列本层）
	DirsDeep    int `json:"dirs_deep"`    // 深遍历目标数（递归整棵子树）
	DirsMerged  int `json:"dirs_merged"`  // 被上层目标覆盖而省掉的
	DirsVisited int `json:"dirs_visited"` // 实际访问到的目录总数
	ListCalls   int `json:"list_calls"`   // 列目录请求次数 ≈ 本轮耗时的秒数（全局 1 秒读节流）

	// ---- 跳过的目录：**临时**与**永久**必须分开 ----
	//
	// 只有临时失败才值得「整轮不消费、下轮重来」。改造前这三类混在
	// DirsSkipped 一个计数里，于是一条永远不可能成功的事件（比如指向
	// 媒体库外的目录）会把整批事件永久钉死，每 30 秒重放一次整轮遍历，
	// 直到 7 天后被 pruneSyncEvents 强杀。
	DirsSkipped  int `json:"dirs_skipped"`   // 临时：读不到，下轮重试（唯一会阻止消费的）
	DirsOutside  int `json:"dirs_outside"`   // 永久：目录不在媒体库内，本工具不管
	DirsGone     int `json:"dirs_gone"`      // 永久：目录已在网盘上删除
	DirsNoParent int `json:"dirs_no_parent"` // 永久：事件没带父目录，定位不了

	// ---- 本轮结局 ----
	Interrupted bool   `json:"interrupted"`          // 被让路中断（整理等着用锁）
	YieldedTo   string `json:"yielded_to,omitempty"` // 让给了谁
	Consumed    bool   `json:"consumed"`             // 事件是否已标记消费、游标是否推进
	NotConsumed string `json:"not_consumed,omitempty"`
	PendingLeft int64  `json:"pending_left"` // 收尾时数据库里还剩多少条 pending
	StallRounds int    `json:"stall_rounds"` // 连续多少轮没消费（>0 就是在重放）
}

// RunIncrementalSync 增量同步 HTTP 入口
// POST /sync/incremental  body: {"cid":"...","local_path":"...","video_ext":[],"image_ext":[],"data_ext":[],"limit":1000}
func (h *Handler) RunIncrementalSync(c *gin.Context) {
	var req struct {
		Cid       string   `json:"cid"`
		LocalPath string   `json:"local_path"`
		VideoExt  []string `json:"video_ext"`
		ImageExt  []string `json:"image_ext"`
		DataExt   []string `json:"data_ext"`
		Limit     int      `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 进任务队列（执行时再 normalizeIncrParams）：此前在请求里等锁同步跑，撞上后台任务就 409
	job, err := enqueueJob(h.DB, jobSpec{Kind: "incr", Title: "手动增量同步", DedupeKey: "incr",
		Source: "web", Priority: jobPriorityManual, Params: jobParams{Sync: &syncJobParams{
			Cid: req.Cid, LocalPath: req.LocalPath, VideoExt: req.VideoExt, ImageExt: req.ImageExt, DataExt: req.DataExt,
		}}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "增量同步")
}

func normalizeIncrParams(cid, localPath string, videoExt, imageExt, dataExt []string, limit int) incrParams {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if localPath == "" {
		localPath = defaultLocalPath
	}
	if cid == "" {
		cid = "0"
	}
	return incrParams{Cid: cid, LocalPath: localPath, VideoExt: videoExt, ImageExt: imageExt, DataExt: dataExt, Limit: limit}
}

// executeIncrementalSync 增量同步核心。
//
// **职责边界**：只处理 115 端的**外部变更** —— 手机/客户端上传、离线下载完成、
// 网页端的删除与改名。自动整理已经是一条龙流水线（识别 → 搬移 → 写 STRM →
// 刮削），它自己的产物不经过这里：整理做的每次 move/rename 都登记进
// EventSuppress，事件绕回来时在下面被跳过（peekSuppressed）。
//
// 两阶段（CMS 同款）：
// 阶段一：小批量分页拉取生活事件并落库去重（SyncEvent 表，事件 id 唯一，永不丢失）
// 阶段二：按时间正序应用事件——新增类定向重遍历受影响目录；
//
//	move/rename/delete 基于本地文件台账（SyncedFile）精确执行
func (h *Handler) executeIncrementalSync(p incrParams) (*incrSummary, error) {
	d, err := h.newIncrDeps()
	if err != nil {
		return &incrSummary{}, err
	}
	return h.executeIncrementalSyncWith(d, p)
}

// executeIncrementalSyncWith 增量同步主流程。外部依赖全部经 incrDeps 进出
// （见 incrdeps.go），测试传桩即可整体覆盖
func (h *Handler) executeIncrementalSyncWith(d incrDeps, p incrParams) (sum *incrSummary, err error) {
	defer SetTaskProgress("") // 结束清进度（含错误路径）
	sum = &incrSummary{}
	// 每一轮都记一份快照给状态页——包括熔断和失败的那些轮，
	// 「为什么没动静」正是要靠它们回答
	defer func() { noteIncrRound(sum, err) }()

	// 轮次号：日志里所有 [同步#N] 都属于这一轮。
	// 同一批内容连着出现在几个轮次号里 = 事件没消费掉在重放（见 incrtrace.go）
	lg := newIncrRound()
	sum.Round = lg.id
	incrStart := time.Now()

	// ---- 作用域计算与配置体检（先于事件拉取：配置错误时熔断，不消费任何事件）----
	// 媒体库根目录名（STRM 路径第一层）
	libName := d.dirName(p.Cid)
	libAbs := d.absPath(p.Cid)
	if libAbs == "" {
		// 媒体库 cid 无效/未配置（如全量同步配置缺 cid 时默认 "0"）：
		// 所有事件都会被判为 other 静默吞掉并标已消费 → STRM 永久缺失。
		// 熔断本轮，事件原样留待配置修正
		lg.infof("⚠⚠ 媒体库 cid=%s 解析不出绝对路径（未配置或已失效），增量同步中止（事件未消费）。请到「账号与媒体库」确认媒体库目录配置", p.Cid)
		return sum, fmt.Errorf("媒体库 cid 无效（%s），增量同步中止（事件未消费，修正配置后重试即可补上）", p.Cid)
	}
	var excludedAbs []string
	var orgCfgRaw struct {
		Pending   string `json:"pending"`
		Existing  string `json:"existing"`
		Redundant string `json:"redundant"`
	}
	if err := json.Unmarshal([]byte(d.setting("org-basic")), &orgCfgRaw); err != nil {
		lg.infof("○ 整理配置解析失败（使用默认值）: %v", err)
	}
	var shareCfgRaw struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(d.setting("share")), &shareCfgRaw)
	for _, cid := range []string{orgCfgRaw.Pending, orgCfgRaw.Existing, orgCfgRaw.Redundant, shareCfgRaw.Folder} {
		if cid != "" {
			if a := d.absPath(cid); a != "" {
				excludedAbs = append(excludedAbs, strings.TrimSuffix(a, "/"))
			}
		}
	}
	// 熔断体检：工作区（待整理/已存在/冗余/转存）若覆盖了整个媒体库
	//（如待整理选在库根或库的上层），排除区判定会吞掉所有库内事件。
	// 直接中止本次增量——事件一条都不拉取消费，修正配置后原样补上
	for _, ex := range excludedAbs {
		if libAbs != "" && strings.HasPrefix(strings.TrimSuffix(libAbs, "/")+"/", ex+"/") {
			lg.infof("⚠ 整理目录（%s）把整个媒体库都包含进去了，这样会误删文件，增量同步已暂停。请到设置里把待整理/已存在/冗余目录改到媒体库外面", ex)
			return sum, fmt.Errorf("配置错误：工作区目录 %s 覆盖了整个媒体库 %s，增量同步中止（事件未消费，修正配置后重试即可补上）", ex, libAbs)
		}
	}

	// 此处原有 3 秒「沉淀延迟」，是为「整理刚搬完文件、事件还没落库」准备的。
	// 整理改成一条龙自己落盘之后，增量只处理 115 端的外部变更（手机上传、
	// 离线下载、网页端删改）——这些事件被我们拉到时早就稳定了，没有上游要等
	SetTaskProgress("正在获取网盘最近的改动…")

	// ---- 阶段一：按游标拉一轮，落库去重 ----
	// 游标（事件 id）命中即停，日常一轮 1 次请求；DB 去重仍然保留作为第二道保险，
	// 游标万一出错也不会导致重复落盘。
	// 拉取失败重试：30 秒 × 3 次（网络抖动/瞬时风控不该让整轮作废，qmediasync 同款）
	d.ensureLifeGate()
	cur := parseLifeCursor(d.setting("incr-cursor"))
	var events []lifeEvent
	nextCur := cur
	{
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			evs, nc, err := d.lifeEvents(cur, p.Limit)
			if err == nil {
				events, nextCur, lastErr = evs, nc, nil
				break
			}
			lastErr = err
			lg.infof("事件拉取失败（第 %d/3 次）: %v", attempt, err)
			if attempt < 3 {
				time.Sleep(incrRetryDelay)
			}
		}
		if lastErr != nil {
			return sum, fmt.Errorf("拉取生活事件失败（已重试 3 次）: %w", lastErr)
		}
	}
	noteLifeRound(len(events))
	sum.EventsTotal = len(events)

	batch := make([]model.SyncEvent, 0, len(events))
	for _, ev := range events {
		if ev.ID == "" {
			continue
		}
		ts, _ := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64)
		// pick_code 与 file_category 一起落库：此前 pick_code 只在内存 map 里活一轮，
		// 上轮中断残留的事件重新消费时它已经丢了，零遍历优化白白失效
		batch = append(batch, model.SyncEvent{
			EventID: ev.ID, Type: ev.Type, FileID: ev.FileID,
			FileName: ev.FileName, Cid: ev.Cid, Size: ev.Size, EventTime: ts,
			PickCode: ev.PickCode, FileCat: ev.FileCat,
		})
	}
	// 只有真正新插入的行才进 pending——此前是把整页都塞进去，
	// 同页已 applied 的历史事件会跟着被重放（见 insertSyncEvents 注释）
	pending := insertSyncEvents(h.DB, batch)
	sum.EventsFresh = len(pending)

	// 恢复上轮中断遗留的 pending 事件：拉取中途失败/进程重启后已落库的事件
	// 会永久滞留 pending，无人再消费。
	// 这条查询会把本轮刚插入的行一起捞出来（它们状态也是 pending），必须去重
	var stale []model.SyncEvent
	h.DB.Where("status = ?", "pending").Order("event_time").Find(&stale)
	pending = mergePendingEvents(stale, pending)
	sum.EventsPending = len(pending)
	// 溯源第一行：本轮要处理的事件是**从哪来的**。
	// 新增 0 条、待处理一大批 = 在重放上轮没消费掉的积压，不是网盘上真有这么多变化
	if len(pending) > 0 {
		lg.infof("▶ 本轮网盘变动：接口拉到 %d 条，其中新事件 %d 条，加上遗留共 %d 条待处理",
			sum.EventsTotal, sum.EventsFresh, len(pending))
	}

	// 事件按时间正序应用（接口返回最新在前）
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].EventTime < pending[j].EventTime })

	SetTaskProgress(fmt.Sprintf("应用事件：%d 条", len(pending)))
	// ---- 阶段二：应用事件 ----
	filter := &syncFilter{
		videoExts: buildExtSet(p.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, p.ImageExt...), p.DataExt...)),
	}
	filter.assetExts[".nfo"] = true
	isMedia := func(name string) bool {
		ext := strings.ToLower(path.Ext(name))
		return filter.videoExts[ext] || filter.assetExts[ext]
	}

	scopeOf := func(cid string) string {
		if cid == "" || cid == "0" {
			return "unknown"
		}
		abs := d.absPath(cid)
		if abs == "" {
			return "unknown"
		}
		abs = strings.TrimSuffix(abs, "/")
		// 排除区判断必须先于媒体库：待整理/已存在/冗余通常建在媒体库根目录内部，
		// 先判 library 会把它们整个吞进"library"作用域，导致冗余目录被重遍历生成 STRM
		for _, ex := range excludedAbs {
			if strings.HasPrefix(abs+"/", ex+"/") {
				return "excluded"
			}
		}
		if libAbs != "" && strings.HasPrefix(abs+"/", strings.TrimSuffix(libAbs, "/")+"/") {
			return "library"
		}
		return "other"
	}

	// localRelOf 网盘绝对路径 → 本地相对路径（含库名前缀）；不在库内返回 false
	localRelOf := func(panAbs string) (string, bool) {
		base := strings.TrimSuffix(libAbs, "/")
		if panAbs == base {
			return libName, true
		}
		if base == "" || !strings.HasPrefix(panAbs, base+"/") {
			return "", false
		}
		return path.Join(libName, strings.TrimPrefix(panAbs, base+"/")), true
	}

	// 删除与移动旧路径单独收集，避免新增目录或同步根覆盖实际清理范围。
	var deletedPaths []string
	var relocatedPaths []string
	// 库内改名/移动命中台账的 fid：这些文件在库里本来就有，重建出来的 STRM
	// 只是换了个名字，Emby 随后推回来的 library.new 不是新片入库
	renameEchoFids := map[string]bool{}
	// relocateDir 目录改名/移动：本地跟着搬。oldPanAbs 来自路径缓存，
	// 拿不到就返回 false 让调用方回退重遍历
	relocateDir := func(ev model.SyncEvent, oldPanAbs string) bool {
		if oldPanAbs == "" || ev.FileName == "" {
			return false
		}
		parent := d.absPath(ev.Cid)
		if parent == "" {
			return false
		}
		oldRel, ok1 := localRelOf(oldPanAbs)
		newRel, ok2 := localRelOf(strings.TrimSuffix(parent, "/") + "/" + ev.FileName)
		if !ok1 || !ok2 {
			return false
		}
		if !h.relocateLocalDir(oldRel, newRel, p.LocalPath) {
			return false
		}
		newAbs := filepath.Join(p.LocalPath, filepath.FromSlash(newRel))
		deletedPaths = append(deletedPaths, filepath.Join(p.LocalPath, filepath.FromSlash(oldRel)))
		relocatedPaths = append(relocatedPaths, newAbs)
		// 目录只是换了名字/位置，里面还是原来那批片子：现在就打标记，
		// 免得 Emby 的实时监控抢在本轮收尾之前把 library.new 推回来
		markEmbyRenamed(newAbs)
		return true
	}

	// 本轮受影响的最浅目录（Emby 定向刷新用，传库根=全刷）。
	// 各处传入的都必须是【含库名前缀】的本地相对路径，
	// 否则刷新路径会少一层、指到一个不存在的目录上
	shallowest := ""
	noteShallow := func(rel string) {
		if rel == "" {
			return
		}
		if shallowest == "" || len(rel) < len(shallowest) {
			shallowest = rel
		}
	}

	// 本轮命中抑制表的 fid：事件成功消费后才把这些标记清掉
	var suppressedHits []string

	// ---- 回退目录遍历的目标集合 ----
	//
	// 遍历是整条链路上最贵的动作（每列一次目录 = 一次 115 请求 + 1 秒节流），
	// 所以「遍历多大范围」直接决定这一轮跑多久、把任务锁攥多久。
	// 目标分两种，判据是**这条事件的内容到底在哪一层**：
	//
	//   浅（deep=false）：文件级事件。事件的 cid 就是那个文件的父目录，
	//     文件一定在这一层，没有任何理由往下钻。改造前一律深遍历，于是
	//     「往 影视/剧集 里丢了一个文件」这种事件会把整个 剧集 分类
	//     （成百上千个目录）重扫一遍，一轮几十分钟锁不放。
	//   深（deep=true）：目录级事件（新建目录/整目录转存/目录改名移动回退），
	//     内容全在子树里，必须递归。注意这里取的是**目录自己的 file_id**，
	//     不是它的父目录 cid —— 改造前目录改名回退遍历的是父目录，
	//     父目录要是分类目录甚至库根，一次改名就等于全库重扫。
	type fallbackTarget struct {
		cid    string
		deep   bool
		reason string // 溯源：哪条事件把它带进来的
	}
	dirSet := map[string]*fallbackTarget{}
	// 零遍历清单：事件自带 pick_code 时直接用事件数据生成 strm，
	// 不再重遍历受影响目录（CMS 同款；无 pick_code 的事件回退 dirSet 遍历）
	type preciseFile struct {
		ev model.SyncEvent
	}
	var precise []preciseFile
	// addFallback 登记一个回退遍历目标。why 只用于日志溯源
	addFallback := func(cid string, deep bool, ev model.SyncEvent, why string) {
		if cid == "" || cid == "0" {
			// 事件没带父目录：定位不了，而且**永远**定位不了。
			// 改造前它会以空 cid 进遍历队列，随后被判成「不在媒体库内」计进
			// DirsSkipped，把整批事件永久钉死在重放里——这是最隐蔽的一条
			sum.DirsNoParent++
			lg.infof("○ 事件里没有可用的目录 id，定位不了，已跳过（类型=%s 文件=%s file_id=%s 父目录=%q%s）。"+
				"不影响本轮其它事件；如发现媒体库缺内容，跑一次全量同步即可补齐",
				ev.Type, ev.FileName, ev.FileID, ev.Cid, why)
			return
		}
		if t := dirSet[cid]; t != nil {
			t.deep = t.deep || deep // 同一目录既有文件级又有目录级事件：按深的算
			return
		}
		dirSet[cid] = &fallbackTarget{
			cid: cid, deep: deep,
			reason: fmt.Sprintf("%s/%s%s", ev.Type, ev.FileName, why),
		}
	}

	// ---- 让路 ----
	//
	// 这一轮从头到尾都攥着任务互斥锁，而它有三段都可能很长：
	// 逐条事件推导路径（未命中缓存就是一次 115 请求 + 1 秒节流）、
	// 零遍历落盘（附属文件要取直链下载）、回退目录遍历。
	// 任何一段里只要有别的任务在排队，就地收工：增量是幂等的，
	// 没消费完的事件下一轮原样重来；而整理错过这把锁要等 10 分钟起步
	yieldReason := func() string {
		who, ok := taskMu.YieldRequested()
		if !ok {
			return ""
		}
		return who
	}
	yieldNow := func(stage string) bool {
		why := yieldReason()
		if why == "" {
			return false
		}
		sum.Interrupted, sum.YieldedTo = true, why
		lg.infof("⏸ 让路给 %s（中断于%s，事件保持待处理，下轮原样重来）", why, stage)
		return true
	}

	for i, ev := range pending {
		if i%50 == 0 {
			SetTaskProgress(fmt.Sprintf("处理网盘变化 %d/%d 条…", i+1, len(pending)))
		}
		if yieldNow(fmt.Sprintf("第 %d/%d 条事件", i+1, len(pending))) {
			break
		}
		// 路径缓存维护必须在抑制检查【之前】：网盘侧的事实已经变了，
		// 与本地怎么处理无关。整理自产的目录搬移也会绕回来，那些事件下面会被跳过，
		// 跳过前不更新缓存的话，缓存就永久停在旧路径上——已搬进冗余的目录
		// 还被算在媒体库里，守卫与作用域判定跟着一起错
		movedFrom := ""
		if ev.FileID != "" {
			switch {
			case ev.Type == evFolderRename || (ev.FileCat == "0" && (ev.Type == evMove || ev.Type == evMoveImage)):
				if parent := d.absPath(ev.Cid); parent != "" && ev.FileName != "" {
					if old, ok := d.dirMoved(ev.FileID, strings.TrimSuffix(parent, "/")+"/"+ev.FileName); ok {
						movedFrom = old
					}
				}
			case ev.Type == evDelete && ev.FileCat == "0":
				d.dirGone(ev.FileID)
			}
		}

		// 整理自产的 move/rename：STRM 早在整理时就落好了，绕回来的事件直接跳过。
		// 只查不删——标记要留到本轮事件真的标成 applied 之后再清（见收尾处），
		// 否则中途放弃重来时标记已经没了，整理的产物会被当成外部变更处理掉
		if ev.FileID != "" && peekSuppressed(ev.FileID) {
			sum.Suppressed++
			suppressedHits = append(suppressedHits, ev.FileID)
			continue
		}
		switch ev.Type {
		case evUpload, evReceive, evCopy:
			// 作用域过滤：冗余/已存在等整理工作区的事件不监控
			sc := scopeOf(ev.Cid)
			if sc == "excluded" || sc == "other" {
				sum.Ignored++
				sum.Structural++
				continue
			}
			if isMedia(ev.FileName) {
				sum.Relevant++
				if ev.PickCode != "" && ev.Cid != "" && ev.FileID != "" {
					precise = append(precise, preciseFile{ev: ev})
				} else {
					// 文件级：只看父目录这一层
					addFallback(ev.Cid, false, ev, "（事件没带 pick_code）")
				}
			} else if ev.FileID != "" && ev.Cid == "" {
				// 没带父目录的非媒体条目，通常是整目录上传：按目录自身递归
				addFallback(ev.FileID, true, ev, "（按目录上传处理）")
				sum.Relevant++
			}
		case evNewFolder, evCopyFolder:
			// 作用域过滤：冗余/已存在等目录新增不监控
			if sc := scopeOf(ev.Cid); sc == "excluded" || sc == "other" {
				sum.Ignored++
				sum.Structural++
				continue
			}
			// 目录新增/复制（含整目录转存）：按目录自身加入受影响集合，内容在子树里 → 深
			if ev.FileID != "" {
				addFallback(ev.FileID, true, ev, "")
				sum.Relevant++
			}
		case evDelete:
			// 作用域过滤：待整理/已存在/冗余等非媒体库区域的删除不监控
			switch scopeOf(ev.Cid) {
			case "excluded", "other":
				sum.Ignored++
				continue
			case "library":
				// 精确删除：台账 → 路径推导（支持整目录删除与无台账的旧文件）
				if removed := h.removeSyncedItem(d, ev, p.Cid, libName, p.LocalPath, false, false); removed != "" {
					deletedPaths = append(deletedPaths, removed)
					sum.Deleted++
				}
			default: // unknown（cid=0 等）：仅按台账名称匹配，静默处理
				if removed := h.removeSyncedItem(d, ev, p.Cid, libName, p.LocalPath, true, false); removed != "" {
					deletedPaths = append(deletedPaths, removed)
					sum.Deleted++
				} else {
					sum.Ignored++
				}
			}
			sum.Structural++
		case evMove, evMoveImage, evRename:
			// 目录整体移动：本地目录直接搬过去 + 台账换前缀，零 115 请求。
			// 台账按 file_id 存的是文件行，目录的 fid 不在里面，
			// 走 removeSyncedItem 永远清不掉旧树（改造前就是这样）
			if ev.FileCat == "0" && relocateDir(ev, movedFrom) {
				sum.Moved++
				if newRel, ok := localRelOf(strings.TrimSuffix(d.absPath(ev.Cid), "/") + "/" + ev.FileName); ok {
					noteShallow(newRel)
				}
				sum.Structural++
				continue
			}
			// 移动/改名：清理旧位置只按台账精确匹配（事件的 Cid/FileName 均为
			// 新位置信息，模糊删除会误删库内同名字幕树），新位置精确重建或回退遍历
			if removed := h.removeSyncedItem(d, ev, p.Cid, libName, p.LocalPath, true, true); removed != "" {
				deletedPaths = append(deletedPaths, removed)
				sum.Moved++
				// 旧位置在台账里 = 这个文件本来就在库内，只是改了名/挪了地方。
				// 没命中台账的是「从库外搬进来」，那才是真入库
				if ev.FileID != "" {
					renameEchoFids[ev.FileID] = true
				}
				// 新位置能当场推出来就先标上：直推落盘在下面还会标一次（幂等），
				// 但事件没带 pick_code 而落到回退遍历时，那里就没人标了
				if base, ok, err := d.relPath(ev.Cid, p.Cid); err == nil && ok && ev.FileName != "" {
					newRel := path.Join(libName, base, ev.FileName)
					if ev.FileCat != "0" && isMedia(ev.FileName) {
						newRel += ".strm"
					}
					markEmbyRenamed(filepath.Join(p.LocalPath, filepath.FromSlash(newRel)))
				}
			}
			if ev.Cid != "" && scopeOf(ev.Cid) == "library" {
				switch {
				case ev.FileCat == "0" && ev.FileID != "":
					// 目录搬过来了但本地跟不动（缓存里没有旧路径）：
					// 要重扫的是**这个目录自己**在新位置的内容，不是它的父目录。
					// 改造前这里落到 fallbackDir(ev.Cid)，父目录是分类目录时
					// 一次目录改名就触发一次整分类重扫
					addFallback(ev.FileID, true, ev, "（本地跟不动，重扫新位置）")
				case ev.PickCode != "" && ev.FileID != "" && isMedia(ev.FileName):
					precise = append(precise, preciseFile{ev: ev}) // 移入媒体库：事件直推重建
				default:
					addFallback(ev.Cid, false, ev, "（移入媒体库）")
				}
			}
			sum.Structural++
		case evFolderRename:
			// 目录改名。路径缓存已在循环开头按子树重定位过（不再整表清空）
			if ev.Cid != "" {
				if sc := scopeOf(ev.Cid); sc == "excluded" || sc == "other" {
					sum.Ignored++
					sum.Structural++
					continue
				}
				// 本地目录直接改名 + 台账换前缀；旧路径拿不到才回退重遍历，
				// 那种情况下旧名子树会残留，交给失效 STRM 检测
				if relocateDir(ev, movedFrom) {
					sum.Moved++
					if newRel, ok := localRelOf(strings.TrimSuffix(d.absPath(ev.Cid), "/") + "/" + ev.FileName); ok {
						noteShallow(newRel)
					}
				} else {
					// 同上：重扫改名后的这个目录本身，不是它的父目录
					addFallback(ev.FileID, true, ev, "（改名后本地跟不动，重扫新位置）")
				}
			}
			sum.Structural++
		default:
			sum.Structural++
			lg.vlogf("○ 未处理的事件: 类型=%s 文件=%s", ev.Type, ev.FileName)
		}
	}

	// ---- 零遍历落盘：事件自带 pick_code 的精确处理（无目录遍历） ----
	preciseDone := 0 // 实际处理掉的条数（中途让路时会少于 len(precise)），账单要报真数
	{
		domain, format, keepExt, skipExist := d.strmConfig()
		for i, pf := range precise {
			// 事件循环里已经让路了就不再开工；落盘中途也随时可以停
			if sum.Interrupted || yieldNow(fmt.Sprintf("直推第 %d/%d 个文件", i+1, len(precise))) {
				break
			}
			ev := pf.ev
			base, ok, err := d.relPath(ev.Cid, p.Cid)
			if err != nil || !ok {
				addFallback(ev.Cid, false, ev, "（直推时推导不出路径）") // 回退目录遍历
				continue
			}
			rel := path.Join(libName, base, ev.FileName)
			f := remoteFile{
				Fid:      ev.FileID,
				Name:     ev.FileName,
				Path:     path.Join(libName, base),
				Size:     ev.Size,
				PickCode: ev.PickCode,
			}
			preciseDone++
			ext := strings.ToLower(path.Ext(ev.FileName))
			switch {
			case filter.videoExts[ext]:
				wrote, err := writeStrm(p.LocalPath, domain, format, keepExt, skipExist, f)
				if err != nil {
					lg.infof("零遍历 strm 失败 %s: %v", rel, err)
					addFallback(ev.Cid, false, ev, "（直推写 strm 失败）")
					continue
				}
				upsertSyncedFile(h.DB, f, rel+".strm", "video")
				strmAbs := filepath.Join(p.LocalPath, filepath.FromSlash(rel+".strm"))
				if renameEchoFids[ev.FileID] {
					markEmbyRenamed(strmAbs)
				} else if wrote {
					markEmbyFreshAdded(strmAbs)
				}
				if wrote {
					sum.StrmCreated++
				} else {
					sum.StrmExisting++
				}
				sum.Videos++
				noteShallow(path.Join(libName, base))
			case filter.assetExts[ext]:
				switch err := d.downloadAsset(f, p.LocalPath); {
				case err == nil:
					upsertSyncedFile(h.DB, f, rel, "asset")
					sum.AssetsDownloaded++
				case errors.Is(err, errAssetExists):
					// 本地已有实体：只补台账，不重复下载
					upsertSyncedFile(h.DB, f, rel, "asset")
					sum.AssetsSkipped++
				default:
					sum.AssetsFailed++
				}
				noteShallow(path.Join(libName, base))
			}
		}
		if len(precise) > 0 {
			lg.vlogf("零遍历模式: 事件直推 %d 个文件（回退目录遍历 %d 个）", len(precise), len(dirSet))
		}
	}

	// ---- 受影响目录：定位相对路径 + 祖先去重 ----
	//
	// 这里的分类是整条链路的正确性关键：定位失败分**临时**与**永久**两种，
	// 只有临时失败才该让整轮不消费、下轮重来（见 incrSummary 上的注释）
	type targetDir struct {
		cid, base, reason string
		deep              bool
	}
	var targets []targetDir
	for cid, ft := range dirSet {
		// 定位本身就要打 115（缓存没命中时一次一秒），同样要能让路
		if sum.Interrupted || yieldNow("定位受影响目录") {
			break
		}
		base, ok, err := d.relPath(cid, p.Cid)
		switch {
		case errors.Is(err, errDirGone):
			// 永久：目录被删掉之后残留的定位请求，重试永远不会成功。
			// 判据是 errDirGone —— 新接口对已删除的 cid 不报错，
			// 是 fetch115Ancestors 自己校验末元素 cid 得出的结论
			sum.DirsGone++
			lg.infof("○ 网盘目录已被删除，跳过相关变化（cid=%s 来源=%s）", cid, ft.reason)
			continue
		case err != nil:
			// 临时：网络抖动 / 瞬时风控。只有这一类值得整轮不消费、下轮重来
			sum.DirsSkipped++
			lg.infof("⚠ 暂时读不到网盘目录位置（cid=%s 来源=%s）: %v", cid, ft.reason, err)
			continue
		case !ok:
			// 永久：目录在媒体库外（转存区、别的网盘目录……），本工具本来就不管它。
			// 改造前这一条是**静默**计进 DirsSkipped 的，于是一条这样的事件
			// 就能把整批事件永久钉死：每 30 秒重放一遍整轮遍历，
			// 直到 7 天后被 pruneSyncEvents 强杀。现在它只记账、不阻塞消费
			sum.DirsOutside++
			lg.infof("○ 目录不在媒体库内，不处理（cid=%s 网盘路径=%s 媒体库=%s 来源=%s）",
				cid, orUnknownPath(d.absPath(cid)), libAbs, ft.reason)
			continue
		}
		targets = append(targets, targetDir{cid: cid, base: base, deep: ft.deep, reason: ft.reason})
	}
	// 浅路径在前；同一路径上深遍历在前（深的能覆盖浅的，反过来不行）
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].base != targets[j].base {
			return targets[i].base < targets[j].base
		}
		return targets[i].deep && !targets[j].deep
	})
	var uniqTargets []targetDir
	for _, t := range targets {
		covered := false
		for _, u := range uniqTargets {
			switch {
			case u.deep && (u.base == "" || t.base == u.base || strings.HasPrefix(t.base, u.base+"/")):
				covered = true // 上层在做深遍历，子目录不必再单独跑一趟
			case !u.deep && !t.deep && t.base == u.base:
				covered = true // 两个浅目标落在同一个目录
			}
			if covered {
				break
			}
		}
		if covered {
			sum.DirsMerged++
			continue
		}
		uniqTargets = append(uniqTargets, t)
	}
	for _, t := range uniqTargets {
		if t.deep {
			sum.DirsDeep++
		} else {
			sum.DirsShallow++
		}
		if t.deep && t.base == "" {
			lg.infof("⚠ 本轮有一个深遍历目标就是媒体库根，这一趟等于整库重扫（来源=%s）", t.reason)
		}
	}
	// 溯源清单：要遍历哪些目录、各自是被哪条事件带进来的、是浅还是深。
	// 排查「这一轮为什么跑了十几分钟」看这一段就够了
	walkKind := func(deep bool) string {
		if deep {
			return "深"
		}
		return "浅"
	}
	switch {
	case len(uniqTargets) == 1:
		t := uniqTargets[0]
		lg.infof("回退遍历 1 个目录：[%s] %s ← %s", walkKind(t.deep), orRootLabel(t.base), t.reason)
	case len(uniqTargets) > 1:
		lg.infof("回退遍历 %d 个目录（深 %d / 浅 %d，另有 %d 个被上层目标覆盖）：",
			len(uniqTargets), sum.DirsDeep, sum.DirsShallow, sum.DirsMerged)
		for i, t := range uniqTargets {
			if i >= incrTargetLogMax {
				lg.infof("    …另有 %d 个目录未逐一列出", len(uniqTargets)-incrTargetLogMax)
				break
			}
			lg.infof("    [%s] %s ← %s", walkKind(t.deep), orRootLabel(t.base), t.reason)
		}
	}

	// ---- 逐目录遍历并立即落盘 ----
	//
	// 整轮最贵的一段：每列一次目录 = 一次 115 请求 + 1 秒节流。
	// walkCtl.abort 让它在**每次发请求之前**都能停下来给排队的任务让路
	domain, format, keepExt, skipExist := d.strmConfig()
	for _, t := range uniqTargets {
		if sum.Interrupted || yieldNow("目录遍历前") {
			break
		}
		noteShallow(path.Join(libName, t.base)) // 必须带库名，与零遍历那条保持一致
		ctl := &walkCtl{tag: lg.tag, abort: yieldReason}
		if !t.deep {
			ctl.maxDepth = 1 // 浅遍历：只列这一层，不下钻
		}
		tStart := time.Now()
		var videos, assets []remoteFile
		walkOnce := func() error {
			videos, assets = nil, nil
			return d.walkDir(t.cid, path.Join(libName, t.base), &videos, &assets, filter, ctl)
		}
		var aborted errWalkAborted
		err := walkOnce()
		// errDirGone 是永久失败（遍历途中目录被删了），重试与「下轮重来」都没有意义，
		// 按临时失败处理会把整批事件永久钉死（见 incrSummary 上的分类注释）
		if err != nil && !errors.As(err, &aborted) && !errors.Is(err, errDirGone) {
			lg.infof("遍历目录失败 %s: %v，%v 后重试一次", orRootLabel(t.base), err, incrRetryDelay)
			time.Sleep(incrRetryDelay)
			err = walkOnce()
		}
		sum.DirsVisited += ctl.dirs
		sum.ListCalls += ctl.pages
		switch {
		case errors.As(err, &aborted):
			sum.Interrupted, sum.YieldedTo = true, aborted.reason
			lg.infof("⏸ 让路给 %s，中断于 %s（本轮已列 %d 次目录；事件保持待处理，下轮重来）",
				aborted.reason, aborted.dir, ctl.pages)
		case errors.Is(err, errDirGone):
			sum.DirsGone++
			lg.infof("○ 遍历途中目录已被删除，跳过 %s（cid=%s 来源=%s）", orRootLabel(t.base), t.cid, t.reason)
			continue
		case err != nil:
			lg.infof("⚠ 遍历目录重试仍失败 %s: %v —— 本轮事件保留，下轮自动重试", orRootLabel(t.base), err)
			sum.DirsSkipped++
			continue
		}
		st := d.applyResults(videos, assets, p.LocalPath, domain, format, keepExt, skipExist, t.base)
		if st.StrmCreated > 0 {
			markEmbyFreshAdded(filepath.Join(p.LocalPath, filepath.FromSlash(path.Join(libName, t.base))))
		}
		sum.Dirs++
		sum.Videos += len(videos)
		sum.StrmCreated += st.StrmCreated
		sum.StrmExisting += st.StrmExisting
		sum.AssetsTotal += len(assets)
		sum.AssetsDownloaded += st.AssetsDownloaded
		sum.AssetsSkipped += st.AssetsSkipped
		sum.AssetsFailed += st.AssetsFailed
		// 逐目录结果：「新增」与「已存在」必须分开报。改造前这行打的是
		// 扫到的视频总数且一律叫「新增」，重复遍历时满屏「新增视频 N 个」，
		// 看着就像在全盘重建。
		// 中断的那一趟只扫了一半，已列到的照常落盘（幂等），但必须标出来，
		// 否则下一轮同一个目录又出现一遍会显得像重放
		partial := ""
		if sum.Interrupted {
			partial = "（中断，未扫完）"
		}
		lg.infof("%s%s：扫到视频 %d 个（新增 STRM %d，已存在 %d），附属下载 %d 个｜列目录 %d 次，耗时 %s",
			orRootLabel(t.base), partial, len(videos), st.StrmCreated, st.StrmExisting, st.AssetsDownloaded,
			ctl.pages, time.Since(tStart).Truncate(time.Second))
		if ctl.depthCut > 0 {
			lg.vlogf("    （浅遍历：没有下钻的子目录 %d 个）", ctl.depthCut)
		}
		if t.deep && ctl.dirs >= incrDeepWalkWarn {
			lg.infof("⚠ 这一趟深遍历扫了 %d 个目录、列了 %d 次目录（每次要等节流），耗时 %s。触发它的是：%s。"+
				"范围明显过大的话，通常是网盘那边对一个大目录做了整体改名/移动",
				ctl.dirs, ctl.pages, time.Since(tStart).Truncate(time.Second), t.reason)
		}
		if sum.Interrupted {
			break
		}
	}

	SetTaskProgress(fmt.Sprintf("收尾：目录 %d，STRM %d", sum.Dirs, sum.StrmCreated))

	// ---- 消费判定 ----
	//
	// 「消费」= 把本轮事件标成 applied 并推进游标。不消费的代价是下一轮
	// 原样重来（STRM 写入是 upsert、删除幂等，重复处理无副作用），
	// 所以宁可不消费也不能漏内容 —— 但**不消费的理由必须是会自己好转的**，
	// 否则就是一个每 30 秒重放一次的死循环。
	// 永久性跳过（不在库内 / 已删除 / 没带父目录）只记账，不阻止消费。
	switch {
	case sum.DirsSkipped > 0:
		sum.NotConsumed = fmt.Sprintf("%d 个网盘目录暂时读不到", sum.DirsSkipped)
	case sum.Interrupted:
		sum.NotConsumed = "给 " + sum.YieldedTo + " 让路，本轮没跑完"
	default:
		sum.Consumed = true
	}

	now := time.Now()
	if sum.Consumed {
		ids := make([]string, 0, len(pending))
		for _, ev := range pending {
			ids = append(ids, ev.EventID)
		}
		if len(ids) > 0 {
			h.DB.Model(&model.SyncEvent{}).Where("event_id IN ?", ids).
				Updates(map[string]interface{}{"status": "applied", "applied_at": now})
		}
		// 事件已落定，现在才能清掉抑制标记：同一个 fid 之后被用户真的手动移动时
		// 必须能正常处理，标记不清就会把那次真实变更也吞了
		unmarkSuppressed(suppressedHits...)
		d.saveSetting("incr-last", fmt.Sprint(now.Unix()))
		// 游标只在本轮真的全部消费完之后才推进：没消费的那批事件下轮还要重来，
		// 游标跟着不动才补得回来
		d.saveSetting("incr-cursor", encodeLifeCursor(nextCur))
	}

	// 定向刷新：传本轮受影响的最浅子目录（传库根会命中所有库=全刷）。
	// 刷新与通知跟消费判定无关 —— 已经落盘的内容要让 Emby 看见，
	// 哪怕这一轮是被让路中断的
	refreshBase := p.LocalPath
	if shallowest != "" {
		refreshBase = filepath.Join(p.LocalPath, filepath.FromSlash(shallowest))
	}
	if sum.StrmCreated+sum.AssetsDownloaded > 0 {
		d.notifyRefresh(refreshBase)
	}
	// 目录整体搬迁没有重新生成 STRM，也需要让 Emby 发现新位置。
	for _, movedPath := range dedupeStrings(relocatedPaths) {
		d.notifyRefresh(movedPath)
	}
	// 删除/移动要单独报一次「删除」：Emby 侧的条目不会因为文件没了自己消失，
	// 不通知的话网盘删了片子、strm 也删了，Emby 里条目还在，点进去播放 404。
	// 与新增分开发是因为删除场景要先把目标上移到还存在的父目录（见 notifyEmbyDeleted）
	if len(deletedPaths) > 0 {
		d.notifyDeleted(dedupeStrings(deletedPaths)...)
	}

	sum.Elapsed = time.Since(incrStart).Truncate(time.Second).String()

	// ---- 本轮账单 ----
	//
	// 一行说清这一轮干了什么、贵在哪、最后有没有消费掉。空转轮次照旧静默
	// （30 秒一轮，不能刷屏），但只要动过手就一定留一行 —— 出问题时
	// 把相邻几个轮次号的账单排在一起，是重放还是真有变化一眼就分得出来
	h.DB.Model(&model.SyncEvent{}).Where("status = ?", "pending").Count(&sum.PendingLeft)
	// 停滞检测放在账单之前：账单要报「已经连续几轮没消费」（见 incrtrace.go）
	if len(pending) > 0 || !sum.Consumed {
		sum.StallRounds = noteIncrRoundOutcome(lg, sum.Consumed, sum.NotConsumed, sum.PendingLeft)
	}
	if len(pending) > 0 || sum.ListCalls > 0 {
		outcome := "事件已消费，游标已推进"
		if !sum.Consumed {
			outcome = fmt.Sprintf("事件未消费（%s），下轮原样重来；已连续 %d 轮", sum.NotConsumed, sum.StallRounds)
		}
		perm := ""
		if n := sum.DirsOutside + sum.DirsGone + sum.DirsNoParent; n > 0 {
			perm = fmt.Sprintf("｜永久跳过 %d（库外 %d/已删 %d/无目录 id %d）",
				n, sum.DirsOutside, sum.DirsGone, sum.DirsNoParent)
		}
		bill := fmt.Sprintf("本轮账单：事件 拉取%d/新增%d/处理%d · 直推 %d · 回退目录 %d（深%d 浅%d，访问 %d 个，列目录 %d 次）"+
			" · 删 %d · 移改 %d · STRM 新增 %d/已存在 %d · 附属 下载%d/跳过%d/失败%d%s · 耗时 %s · %s · 剩余待处理 %d 条",
			sum.EventsTotal, sum.EventsFresh, sum.EventsPending, preciseDone,
			sum.DirsShallow+sum.DirsDeep, sum.DirsDeep, sum.DirsShallow, sum.DirsVisited, sum.ListCalls,
			sum.Deleted, sum.Moved, sum.StrmCreated, sum.StrmExisting,
			sum.AssetsDownloaded, sum.AssetsSkipped, sum.AssetsFailed, perm,
			sum.Elapsed, outcome, sum.PendingLeft)
		// 平平无奇的轮次（全是整理自产、或本来就无关的事件）只在详细日志里留账：
		// 30 秒一轮，不能每轮都往日志里塞一条长行。
		// 但只要**花了 115 请求**或者**没消费掉**，这一行就必须出现——
		// 「为什么一直在轮询/为什么反复扫同样的目录」全靠它回答
		if sum.ListCalls > 0 || !sum.Consumed || sum.StallRounds > 0 ||
			sum.DirsSkipped+sum.DirsOutside+sum.DirsGone+sum.DirsNoParent > 0 {
			lg.infof("%s", bill)
		} else {
			lg.vlogf("%s", bill)
		}
	}

	// 完成汇总（大白话）：只要本轮真的处理了变化就给一条结论。
	// 此前的术语行（媒体相关/结构性/非库区忽略…）普通用户读不懂，
	// 且"处理了但全部无关"的轮次会整行消失，让人以为卡死没处理
	var parts []string
	if sum.StrmCreated > 0 {
		parts = append(parts, fmt.Sprintf("新增视频文件 %d 个", sum.StrmCreated))
	}
	if sum.AssetsDownloaded > 0 {
		parts = append(parts, fmt.Sprintf("下载字幕/封面 %d 个", sum.AssetsDownloaded))
	}
	if sum.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("清理已删除内容 %d 项", sum.Deleted))
	}
	if sum.Moved > 0 {
		parts = append(parts, fmt.Sprintf("跟随网盘移动/改名 %d 项", sum.Moved))
	}
	detail := "均与媒体库无关，无需改动本地文件"
	if len(parts) > 0 {
		detail = strings.Join(parts, "，")
	}
	// 完成摘要只在真的动了媒体库时输出：定时任务每 10 分钟一轮，
	// "均与媒体库无关"的空转轮次（占绝大多数）静默不刷屏
	if len(parts) > 0 {
		ignoredNote := ""
		if sum.Ignored > 0 {
			ignoredNote = fmt.Sprintf("（另有 %d 条整理目录内变动已忽略）", sum.Ignored)
		}
		lg.infof("✓ 增量同步完成：%s%s。用时 %s", detail, ignoredNote, sum.Elapsed)
	}
	// 整理自产的变更单独报一行：它们的 STRM 在整理时就已经落好，这里跳过是正常的，
	// 不说清楚会让人以为增量把变更漏了
	if sum.Suppressed > 0 {
		lg.infof("○ 已跳过自产变更 %d 条（整理时已生成 STRM）", sum.Suppressed)
	}
	return sum, nil
}

// absPathOf 目录绝对路径（如 /整理/已存在）；取不到返回 ""。
// 网盘根（cid 为空或 "0"）同样返回 ""——调用方一律按「判不了」处理，
// 这是改造前就有的约定，别改成 "/"
func absPathOf(cookie, cid string) string {
	if cid == "" || cid == "0" {
		return ""
	}
	p, err := resolveDirAbs(cookie, cid)
	if err != nil {
		return ""
	}
	return p
}

// absPathOfFresh 同上，但绕过缓存强制重查。
//
// 给「拿错路径会删错/搬错文件」的判定用。为什么不是所有地方都用它：
// 作用域判定（scopeOf）是每条事件一次的热路径，全走 Fresh 等于每条事件一个请求，
// 比改造前还慢。缓存的正确性由 pan115Ops 的失效钩子（move/rename/delete）
// 与目录改名事件共同保证，Fresh 只留给低频且后果严重的地方
func absPathOfFresh(cookie, cid string) string {
	if cid == "" || cid == "0" {
		return ""
	}
	p, err := resolveDirAbsFresh(cookie, cid)
	if err != nil {
		return ""
	}
	return p
}

// relocateLocalDir 网盘目录改名/移动后，本地目录跟着搬 + 台账整棵子树换前缀。零 115 请求。
//
// 改造前这里只是「重遍历新位置」，旧名子树原样留在本地等失效 STRM 检测来收 ——
// 而目录【移动】更糟：台账按 file_id 索引存的是文件行，目录的 fid 根本不在里面，
// removeSyncedItem 找不到、返回 false，旧树就永远留着了。
//
// 拿不到旧路径（缓存里没有）时返回 false，调用方回退重遍历 —— 不猜。
// SQL 用 length(?) 而不是 Go 的 len()：Go 数字节、SQLite 数字符，中文路径下对不上
func (h *Handler) relocateLocalDir(oldRel, newRel, localRoot string) bool {
	if oldRel == "" || newRel == "" || oldRel == newRel {
		return false
	}
	oldAbs := filepath.Join(localRoot, filepath.FromSlash(oldRel))
	newAbs := filepath.Join(localRoot, filepath.FromSlash(newRel))
	if st, err := os.Stat(oldAbs); err != nil || !st.IsDir() {
		return false // 本地没有这棵树
	}
	if _, err := os.Stat(newAbs); err == nil {
		return false // 目标已存在，不覆盖，交给重遍历
	}
	if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return false
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		log.Printf("[同步] ○ 本地目录改名失败（改用重新遍历）: %s → %s: %v", oldRel, newRel, err)
		return false
	}
	h.DB.Model(&model.SyncedFile{}).
		Where("rel_path = ? OR rel_path LIKE ?", oldRel, oldRel+"/%").
		Update("rel_path", gorm.Expr("? || substr(rel_path, length(?) + 1)", newRel, oldRel))
	log.Printf("[同步] ✓ 跟随网盘改名: %s → %s", oldRel, newRel)
	return true
}

// removeSyncedFile 按文件 id 从台账定位并删除本地文件（仅删除本工具生成过的文件）
// 返回清理成功的本地路径，供调用方通知 Emby；空串表示未清理。
func (h *Handler) removeSyncedFile(fileID, localRoot string) string {
	if fileID == "" {
		return ""
	}
	var sf model.SyncedFile
	if err := h.DB.Where("file_id = ?", fileID).First(&sf).Error; err != nil {
		return "" // 台账无记录（从未同步过），无需处理
	}
	full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		vlog("[同步] 清理失败 %s: %v", full, err)
		return ""
	}
	h.DB.Delete(&sf)
	vlog("[同步] 已清理: %s", sf.RelPath)
	return full
}

// removeSyncedItem 清理 move/rename/delete 事件的旧位置，三级定位：
//  1. 台账按 file_id 精确匹配（本工具生成且已登记的文件）
//  2. 路径推导：解析父目录相对路径 + 事件文件名；目标是目录则整树删除
//     （覆盖"删除整个影视目录"及台账启用前同步的历史文件）
//  3. 台账按文件名模糊匹配兜底（父目录已被连带删除导致路径推导失败时）
//
// removeSyncedItem 清理本地同步产物（strm/附属实体+台账行）。
// ledgerOnly=true 时只允许按台账 file_id 精确删除：move/rename 事件的
// Cid 是【新】父目录、FileName 是【新】名，路径推导与按名模糊兜底
// （LIKE %/名/% 整树删、全盘同名删）都会指向错误目标——工作区里与库内
// 同名的文件（重复转存同名片名极常见）会被误删媒体库 STRM 树。
// delete 事件（Cid=被删位置）才允许全级联
// 返回实际清理路径，保留台账/兜底定位的结果，避免通知时重新猜测目录。
func (h *Handler) removeSyncedItem(d incrDeps, ev model.SyncEvent, rootCid, libName, localRoot string, quiet, ledgerOnly bool) string {
	// 1) 台账精确匹配
	if removed := h.removeSyncedFile(ev.FileID, localRoot); removed != "" {
		return removed
	}
	if ledgerOnly {
		if !quiet {
			log.Printf("[同步] ○ 移动/改名无台账记录，跳过清理: %s（file_id=%s）", ev.FileName, ev.FileID)
		}
		return ""
	}
	// 事件所在目录的本地相对路径（**含库名前缀**）。
	// 台账里的 rel_path 一律带库名（applySyncResults 写的是 path.Join(f.Path, …)，
	// 而 f.Path = path.Join(libName, base)），推导时漏掉这一层就永远匹配不上——
	// 改造前第 2 级就是这么废掉的，活儿全落到了第 4 级的全库按名搜索上
	dirRel, dirOK := "", false
	if ev.Cid != "" {
		if base, ok, err := d.relPath(ev.Cid, rootCid); err == nil && ok {
			dirRel, dirOK = path.Join(libName, base), true
		}
	}

	// 2) 路径推导
	if dirOK && ev.FileName != "" {
		{
			rel := path.Join(dirRel, ev.FileName)
			local := filepath.Join(localRoot, filepath.FromSlash(rel))
			// 目录：整树删除（strm/附属全在树内），并清理台账
			if st, err := os.Stat(local); err == nil && st.IsDir() {
				if err := os.RemoveAll(local); err != nil {
					log.Printf("[同步] 删除本地目录失败 %s: %v", rel, err)
					return ""
				}
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", rel, rel+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] 目录删除-执行成功: %s", rel)
				return local
			}
			// 文件：strm 与附属实体两种形态
			for _, cand := range []struct{ rel, suffix string }{{rel, ".strm"}, {rel, ""}} {
				full := filepath.Join(localRoot, filepath.FromSlash(cand.rel)) + cand.suffix
				if _, err := os.Stat(full); err == nil {
					if err := os.Remove(full); err != nil {
						log.Printf("[同步] 删除本地文件失败 %s: %v", cand.rel+cand.suffix, err)
						return ""
					}
					h.DB.Where("rel_path = ?", cand.rel+cand.suffix).Delete(&model.SyncedFile{})
					vlog("[同步] 已清理: %s", cand.rel+cand.suffix)
					return full
				}
			}
		}
	}
	// 3) 台账按文件名模糊兜底
	if ev.FileName != "" {
		var sfs []model.SyncedFile
		h.DB.Where("rel_path = ? OR rel_path = ?", ev.FileName+".strm", ev.FileName).Find(&sfs)
		// 进一步按文件名后缀精确过滤（rel_path 最后一段必须完全等于）
		for _, sf := range sfs {
			if path.Base(sf.RelPath) == ev.FileName+".strm" || path.Base(sf.RelPath) == ev.FileName {
				full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
				if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
					continue
				}
				h.DB.Delete(&sf)
				vlog("[同步] 已清理: %s", sf.RelPath)
				return full
			}
		}
		// 目录事件：台账中出现过该名称路径段的，整树删除。
		//
		// ⚠️ 这里只能删**事件真正指向的那个目录**。改造前取的是「最浅前缀」：
		// 网盘上删掉 影视/剧集/动漫番剧（本地没同步过这条路径，第 2 级落空）后，
		// 台账里 影视/动漫番剧/… 的行同样匹配 "%/动漫番剧/%"，最浅前缀算成
		// 影视/动漫番剧，把用户整个番剧库的 STRM 树删了。
		//
		// 推得出父目录（dirOK）时，候选只认 dirRel/FileName 这一条；
		// 推不出时（父目录被连带删除）退回按名匹配，但只接受唯一候选 ——
		// 同名目录在库里有两处及以上就放弃，漏删交给失效 STRM 检测
		var segs []model.SyncedFile
		h.DB.Where("rel_path LIKE ? OR rel_path LIKE ?", "%/"+ev.FileName+"/%", "%/"+ev.FileName).Limit(200).Find(&segs)
		want := ""
		if dirOK {
			want = path.Join(dirRel, ev.FileName)
		}
		prefixes := map[string]bool{}
		for _, sf := range segs {
			parts := strings.Split(sf.RelPath, "/")
			for i, part := range parts {
				if part == ev.FileName {
					prefix := strings.Join(parts[:i+1], "/")
					if want == "" || prefix == want {
						prefixes[prefix] = true
					}
					break
				}
			}
		}
		bestPrefix := ""
		if len(prefixes) == 1 {
			for p := range prefixes {
				bestPrefix = p
			}
		} else if len(prefixes) > 1 && !quiet {
			log.Printf("[同步] ○ 台账里有 %d 处同名目录 %q，无法确定删哪一处，跳过", len(prefixes), ev.FileName)
		}
		if bestPrefix != "" {
			full := filepath.Join(localRoot, filepath.FromSlash(bestPrefix))
			if err := os.RemoveAll(full); err == nil {
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", bestPrefix, bestPrefix+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地目录已清理: %s", bestPrefix)
				return full
			}
		}
	}
	// 4) 本地磁盘按名搜索兜底：目录精确名匹配取最浅层整树删除；文件匹配 实体/strm 两种形态。
	//
	// ⚠️ 只在【本次事件推导出的那个目录】子树内搜。改造前是从 localRoot 整库扫，
	// 万级库是秒级 IO，更要命的是按裸文件名全盘匹配后 RemoveAll——
	// 重复片名在媒体库里极常见。推不出范围就放弃：宁可漏删留给失效 STRM 检测，
	// 也不能误删
	if ev.FileName != "" && dirOK {
		searchRoot := filepath.Join(localRoot, filepath.FromSlash(dirRel))
		var hitDir, hitFile string
		filepath.WalkDir(searchRoot, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if name == ev.FileName {
				if d.IsDir() {
					if hitDir == "" || len(p) < len(hitDir) {
						hitDir = p
					}
				} else {
					hitFile = p
				}
			} else if name == ev.FileName+".strm" {
				hitFile = p
			}
			return nil
		})
		if hitDir != "" {
			if err := os.RemoveAll(hitDir); err == nil {
				rel, _ := filepath.Rel(localRoot, hitDir)
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", filepath.ToSlash(rel), filepath.ToSlash(rel)+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地目录已清理: %s", rel)
				return hitDir
			}
		}
		if hitFile != "" {
			if err := os.Remove(hitFile); err == nil {
				rel, _ := filepath.Rel(localRoot, hitFile)
				h.DB.Where("rel_path = ?", filepath.ToSlash(rel)).Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地文件已清理: %s", rel)
				return hitFile
			}
		}
	}
	if !quiet {
		if !dirOK {
			log.Printf("[同步] ○ 定位不到 %s 所在的网盘目录，跳过本地搜索（避免全库按名误删）", ev.FileName)
		} else {
			log.Printf("[同步] ○ 本地未找到对应文件: %s", ev.FileName)
		}
	}
	return ""
}

// insertSyncEvents 批量插入生活事件（OnConflict DoNothing），返回**真正新插入**的那些行。
//
// 为什么不是只返回条数：调用方拿条数无从区分新旧，只能把整页事件都当新的去消费。
// 一页里只要有 1 条新事件，同页那些早已 applied 的历史事件就会被重放一遍——
// 重放一条 delete 事件不只是白跑：台账行那时已经没了，removeSyncedItem 会落到
// 路径推导那一级，把用户后来重新上传的同名文件删掉
func insertSyncEvents(db *gorm.DB, batch []model.SyncEvent) []model.SyncEvent {
	if db == nil || len(batch) == 0 {
		return nil
	}
	ids := make([]string, 0, len(batch))
	for _, se := range batch {
		ids = append(ids, se.EventID)
	}
	var existing []string
	db.Model(&model.SyncEvent{}).Where("event_id IN ?", ids).Pluck("event_id", &existing)
	ex := map[string]bool{}
	for _, id := range existing {
		ex[id] = true
	}
	fresh := make([]model.SyncEvent, 0, len(batch))
	dupInBatch := map[string]bool{}
	for _, se := range batch {
		if ex[se.EventID] || dupInBatch[se.EventID] {
			continue
		}
		dupInBatch[se.EventID] = true
		fresh = append(fresh, se)
	}
	if len(fresh) == 0 {
		return nil
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&fresh, 200).Error; err != nil {
		return nil
	}
	return fresh
}

// mergePendingEvents 合并「上轮遗留的 pending 行」与「本轮新插入的行」，按 event_id 去重。
// 两边必然重叠：本轮刚插入的行状态就是 pending，会被 stale 那条查询一起捞出来，
// 不去重就是同一个事件在一轮里被应用两次
func mergePendingEvents(stale, fresh []model.SyncEvent) []model.SyncEvent {
	if len(stale) == 0 {
		return fresh
	}
	seen := make(map[string]bool, len(fresh))
	for _, ev := range fresh {
		seen[ev.EventID] = true
	}
	merged := make([]model.SyncEvent, 0, len(stale)+len(fresh))
	for _, ev := range stale {
		if seen[ev.EventID] {
			continue
		}
		seen[ev.EventID] = true
		merged = append(merged, ev)
	}
	return append(merged, fresh...)
}
