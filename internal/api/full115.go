package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 任务运行历史 ====================
//
// 此前是内存里的最近 5 条（recentRuns），重启即丢。现在历史统一落 TaskJob 表：
// 队列任务由 worker 自己写；还不进队列的后台任务（定时整理、转存触发 …）在 endTask 时补一行
// kind=background，队列面板的「最近结束」与机器人「状态」指令都读这张表。

// recordBackgroundRun 后台任务跑完落一行历史。空转的轮次（markTaskIdle）不留：
// 定时整理默认 10 分钟一轮、多数什么都没干，全记下来面板就被刷满了
func recordBackgroundRun(name string, start time.Time, failure string) {
	if model.DB == nil || name == "" {
		return
	}
	now := time.Now()
	st, msg := jobSuccess, "完成"
	if failure != "" {
		st, msg = jobFailed, failure
	}
	model.DB.Create(&model.TaskJob{
		Kind: jobKindBackground, Title: name, Priority: jobPriorityBackground, Source: "auto",
		Status: st, Message: truncateStr(msg, 480), CreatedAt: start, StartedAt: &start, FinishedAt: &now,
	})
}

// recentFinishedJobs 最近结束的 n 个任务（新的在前）
func recentFinishedJobs(n int) []model.TaskJob {
	var rows []model.TaskJob
	if model.DB != nil {
		model.DB.Where("status IN ?", jobFinishedStatuses).Order("id DESC").Limit(n).Find(&rows)
	}
	return rows
}

// stopCh 进程退出信号（关闭后所有后台协程停止）。
// ShutdownWorkers 优雅退出时调用：停后台轮询 + 立即冲刷防抖队列里的
// 入库通知（15~120 秒防抖窗口内的通知在直接杀进程时全部丢失）
var stopCh = make(chan struct{})

// ShutdownWorkers 停止全部后台 worker 并冲刷待发通知（main 收到 SIGTERM 时调用）
func ShutdownWorkers() {
	select {
	case <-stopCh:
		// 已关闭（幂等）
	default:
		close(stopCh)
	}
	// 队列里正在跑的批量任务做完手上这条就收工；来不及收尾的下次启动标成「中断」
	if id, _ := currentJob(); id != 0 {
		requestJobStop(id)
	}
	FlushMediaNotif()
}

// defaultLocalPath 本地媒体库默认根目录
const defaultLocalPath = "/media"

// ==================== 全量同步 ====================

// taskState 当前任务状态（供前端展示与按钮禁用，含 cron 触发的任务）
var (
	taskStateMu  sync.Mutex
	taskRunning  bool
	taskFailure  string
	taskIdle     bool // 本轮什么都没干（markTaskIdle），结束时不留历史
	taskName     string
	taskStart    time.Time
	taskProgress string // 当前阶段/进度描述（如 "整理 3/12：xxx"），前端轮询展示
)

// SetTaskProgress 更新任务进度描述（任务进行中由各执行器调用）。
// 队列里的任务在跑时，同一句话也写进它的结构化进度（当前条目），已有调用点不用改
func SetTaskProgress(text string) {
	setTaskProgressText(text)
	setJobLabel(text)
}

func setTaskProgressText(text string) {
	taskStateMu.Lock()
	taskProgress = text
	taskStateMu.Unlock()
}

func beginTask(name string) {
	taskStateMu.Lock()
	taskFailure, taskIdle = "", false
	taskRunning, taskName, taskStart, taskProgress = true, name, time.Now(), ""
	taskStateMu.Unlock()
}

// 失败原因必须随任务历史回传，否则后台触发的整理只能靠日志排查。
func failTask(err error) {
	if err == nil {
		return
	}
	taskStateMu.Lock()
	if taskRunning {
		taskFailure = err.Error()
	}
	taskStateMu.Unlock()
}

func endTask() {
	taskStateMu.Lock()
	name := taskName
	start := taskStart
	failure := taskFailure
	idle := taskIdle
	taskRunning, taskProgress = false, ""
	taskStateMu.Unlock()
	// 队列任务的历史由 worker 自己写（它此刻还登记着当前任务）；其余的是后台任务，补一行
	if id, _ := currentJob(); id == 0 && !idle {
		recordBackgroundRun(name, start, failure)
	}
}

// markTaskIdle 当前后台任务本轮空转：结束时不留历史（失败的照样留）
func markTaskIdle() {
	taskStateMu.Lock()
	if taskFailure == "" {
		taskIdle = true
	}
	taskStateMu.Unlock()
}

// TaskStatus 当前任务状态快照（含进度描述）
func TaskStatus() (bool, string, time.Time, string) {
	taskStateMu.Lock()
	defer taskStateMu.Unlock()
	return taskRunning, taskName, taskStart, taskProgress
}

// fullParams 全量同步参数（HTTP 入口与 cron 调度器共用）
type fullParams struct {
	Cid       string
	LocalPath string
	VideoExt  []string
	ImageExt  []string
	DataExt   []string
	Mode      string // normal(默认) / fast
	// AcctOnly 只同步附加账号的库（acctlibs.go 的定时补同步、新加库后的首次同步）：不碰主号的库，也不标主号的事件窗口
	AcctOnly bool
}

// fullSummary 全量同步结果摘要
type fullSummary struct {
	ModeUsed         string // 实际使用的模式：选了 fast 但端点不可用时会降级为 normal
	ScanComplete     bool   // 本次清单是否完整；false 时跳过失效 STRM 标记
	Orphans          int    // 当前待清理的失效 STRM 数
	Elapsed          string
	Total            int
	Created          int // 真正新写/改写的 strm
	Existing         int // 本地已有且内容一致，没动（与 Created 分开报，见 writeStrm）
	AssetsTotal      int
	AssetsDownloaded int
	AssetsSkipped    int
	AssetsFailed     int
	Libs             int      // 同步了几个媒体库
	Failed           []string // 没同步成功的库（多个库时一个失败不拦别的）
}

// fullConfigErr 配置类失败（拿不到可用的 115 通道等）。HTTP 入口据此回 400 而不是 502——
// 这类错误重试多少次结果都一样，得让用户回去改配置
type fullConfigErr struct{ err error }

func (e fullConfigErr) Error() string { return e.err.Error() }

// RunFullSync 全量同步 HTTP 入口
// POST /sync/full  body: {"cid":"...","local_path":"...","video_ext":["mp4"],"image_ext":["jpg"],"data_ext":["ass"]}
func (h *Handler) RunFullSync(c *gin.Context) {
	var req struct {
		Cid       string   `json:"cid"`
		LocalPath string   `json:"local_path"`
		VideoExt  []string `json:"video_ext"`
		ImageExt  []string `json:"image_ext"`
		DataExt   []string `json:"data_ext"`
		Mode      string   `json:"mode"` // fast(界面默认) / normal
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Cid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：请填写 115 媒体库 cid"})
		return
	}

	title := "全量同步"
	if req.Mode == "fast" {
		title = "全量同步（快速模式）"
	}
	// 进任务队列：此前在请求里等 20 秒拿锁再同步跑完，全量动辄几十分钟，页面只能一直转
	job, err := enqueueJob(h.DB, jobSpec{Kind: "full", Title: title, DedupeKey: "full",
		Source: "web", Priority: jobPriorityManual, Params: jobParams{Sync: &syncJobParams{
			Cid: req.Cid, LocalPath: req.LocalPath,
			VideoExt: req.VideoExt, ImageExt: req.ImageExt, DataExt: req.DataExt, Mode: req.Mode,
		}}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "全量同步")
}

// executeFullSync 全量同步核心：递归遍历 cid 目录，视频生成 .strm，附属文件实体落盘。
// 附属文件 = 用户配置的图片后缀 + 数据文件后缀 + nfo（Emby/Jellyfin 标准元数据）；
// 不在过滤集合内的文件一律不同步。
// 调用方负责持有 taskMu 与 beginTask/endTask（HTTP 入口与 cron 调度器都要用）
func (h *Handler) executeFullSync(p fullParams) (*fullSummary, error) {
	// 本地根固定（mediaroot.go）：排队时带着的旧路径一律不认（单元测试显式给的临时目录除外）
	if !mediaRootLegacyForTests || p.LocalPath == "" {
		p.LocalPath = mediaRoot()
	}
	if prob := mediaRootProblem(); prob != "" {
		return nil, fullConfigErr{fmt.Errorf("%s", prob)}
	}
	fullStart := time.Now()

	// 读取 STRM 直链配置
	domain, format, keepExt, skipExist := h.getStrmConfig()

	// 构造统一操作通道（OpenAPI 优先，Cookie 回退）
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, fullConfigErr{err}
	}

	// 过滤器：视频组生成 strm；附属组 = 图片后缀 ∪ 数据后缀 ∪ .nfo（始终包含）
	filter := &syncFilter{
		videoExts: buildExtSet(p.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, p.ImageExt...), p.DataExt...)),
	}
	filter.assetExts[".nfo"] = true

	// 多个媒体库逐个同步（libroots.go）：p.Cid 是这次指定的主库，其余库取自配置；
	// 附加账号的库排在最后（acctlibs.go），各用各账号的通道。
	// 一个库失败不拦别的库；有库失败时不标记事件窗口（见下），那个库的变化留给增量补
	// 库清单：媒体库表（libstore.go）。表还空（迁移前）时退回老配置：这次的主库 + full.extra_libs + acct_libs
	var targets []fullTarget
	if libs := loadLibraries(); len(libs) > 0 {
		for _, l := range libs {
			if l.Account == acctMain && !p.AcctOnly {
				targets = append(targets, fullTarget{cid: l.Cid, local: l.Local})
			}
		}
		for _, l := range libs {
			if l.Account != acctMain {
				targets = append(targets, fullTarget{acct: l.Account, cid: l.Cid, local: l.Local})
			}
		}
	} else {
		if !p.AcctOnly {
			for _, cid := range libCidsOf(p.Cid, h.loadFullSyncCfg().ExtraLibs) {
				targets = append(targets, fullTarget{cid: cid})
			}
		}
		for _, e := range loadAcctLibs() {
			targets = append(targets, fullTarget{acct: e.Account, cid: e.Cid, local: e.Local})
		}
	}
	mainLibs := 0
	for _, t := range targets {
		if t.acct == acctMain {
			mainLibs++
		}
	}
	cookie, _ := h.get115Cookie()
	var videos, assets int
	var st applyStats
	modeUsed, complete := p.Mode, true
	orphanTotal := 0
	var failed []string
	seenName := map[string]string{}
	// 每个库这次的结果，结束时（含提前返回）写给媒体库卡片看（libsyncstat.go）
	stats := map[string]libSyncStat{}
	defer func() {
		h.saveLibSyncStats(stats)
		h.saveFullLastScan(p, stats)
	}()
	for i, t := range targets {
		if len(targets) > 1 {
			SetTaskProgress(fmt.Sprintf("媒体库 %d/%d", i+1, len(targets)))
		}
		libStart := time.Now()
		libOps, libCookie := ops, cookie
		if t.acct != acctMain {
			o, err := h.opsFor(t.acct)
			if err != nil {
				log.Printf("[同步] ✗ 附加账号的库「%s」同步失败: %v", t.local, err)
				failed = append(failed, fmt.Sprintf("%s：%v", t.local, err))
				stats[libSyncStatKey(t)] = libSyncStatOf(fullLibResult{}, err, libStart)
				continue
			}
			libOps, libCookie = o, o.cookie
		}
		r, err := h.fullSyncLib(libOps, libCookie, p, t, len(targets) > 1, filter, seenName, domain, format, keepExt, skipExist)
		stats[libSyncStatKey(t)] = libSyncStatOf(r, err, libStart)
		if err != nil {
			if len(targets) == 1 {
				return nil, err
			}
			log.Printf("[同步] ✗ 媒体库 %s 同步失败，继续同步其他库: %v", firstNonEmpty(r.name, "cid="+t.cid), err)
			failed = append(failed, fmt.Sprintf("%s：%v", firstNonEmpty(r.name, "cid="+t.cid), err))
			continue
		}
		videos += r.videos
		assets += r.assets
		st.StrmCreated += r.st.StrmCreated
		st.StrmExisting += r.st.StrmExisting
		st.StrmFailed += r.st.StrmFailed
		st.AssetsDownloaded += r.st.AssetsDownloaded
		st.AssetsSkipped += r.st.AssetsSkipped
		st.AssetsFailed += r.st.AssetsFailed
		if r.modeUsed == "normal" {
			modeUsed = "normal" // 有一个库降级就按降级报
		}
		complete = complete && r.complete
		orphanTotal += r.orphans
	}
	if len(targets) == 0 {
		return nil, fullConfigErr{fmt.Errorf("未配置 115 媒体库目录")}
	}
	if len(failed) == len(targets) {
		return nil, fmt.Errorf("%d 个媒体库全部同步失败：%s", len(failed), strings.Join(failed, "；"))
	}
	if modeUsed != "fast" {
		modeUsed = "normal"
	}

	totalNew := st.StrmCreated + st.AssetsDownloaded
	if totalNew > 0 {
		// 全量传的是媒体库根，会把根下面每个库都整库扫一遍（万级库很贵），
		// 所以做成开关且默认关 —— p115strmhelper、qmediasync 的同类开关同样默认关
		if h.fullRefreshEmbyEnabled() {
			h.notifyEmbyRefresh(p.LocalPath)
		} else {
			log.Printf("[同步] ○ 未通知 Emby 刷新：「全量后刷新 Emby」开关没开（整库扫描很贵，默认关；" +
				"需要就到 Strm 管理 → 全量同步 打开）")
		}
	}
	// 全量已覆盖一切：把事件窗口内的生活事件标记为已处理，
	// 之后的增量同步只处理此后发生的新事件。
	// 有库没同步成功时不标：那个库窗口内的变化还得靠增量补上
	if mainLibs == 0 {
		// 只同步了附加账号的库：主号的生活事件一条都没覆盖到，不能标
	} else if len(failed) > 0 {
		log.Printf("[同步] ○ 有 %d 个媒体库没同步成功，生活事件窗口不标记为已覆盖（留给增量处理）", len(failed))
	} else if cookieOnly, err := h.get115Cookie(); err == nil {
		if n, err := h.markEventsCoveredByFullSync(cookieOnly); err != nil {
			log.Printf("[同步] 标记生活事件已覆盖失败: %v", err)
		} else if n > 0 {
			log.Printf("[同步] 生活事件窗口已标记为已覆盖: %d 条（增量同步只处理此后新事件）", n)
		}
	}
	if len(targets) > mainLibs {
		h.markAcctLibsSynced(time.Now()) // 附加账号的库刚同步过：定时补同步从现在算起
	}
	SetTaskProgress("")
	log.Printf("[同步] 全量同步完成（%s模式）：视频 %d 个（新增 STRM %d，已存在 %d，失败 %d），附属文件下载 %d 个，用时 %s",
		map[string]string{"fast": "快速", "normal": "标准"}[modeUsed],
		videos, st.StrmCreated, st.StrmExisting, st.StrmFailed, st.AssetsDownloaded,
		time.Since(fullStart).Truncate(time.Second))

	return &fullSummary{
		ModeUsed:         modeUsed,
		ScanComplete:     complete,
		Orphans:          orphanTotal,
		Elapsed:          time.Since(fullStart).Truncate(time.Second).String(),
		Total:            videos,
		Created:          st.StrmCreated,
		Existing:         st.StrmExisting,
		AssetsTotal:      assets,
		AssetsDownloaded: st.AssetsDownloaded,
		AssetsSkipped:    st.AssetsSkipped,
		AssetsFailed:     st.AssetsFailed,
		Libs:             len(targets),
		Failed:           failed,
	}, nil
}

// fullTarget 全量要同步的一个库：主号的库只有 cid；附加账号的库带账号与本地名（acctlibs.go）
type fullTarget struct {
	acct  uint
	cid   string
	local string
}

// fullLibResult 一个媒体库的全量结果
type fullLibResult struct {
	name           string
	modeUsed       string
	complete       bool
	videos, assets int
	st             applyStats
	orphans        int
}

// fullSyncLib 同步一个媒体库：取清单 → 落盘 → 失效 STRM 标记（只标这个库的台账）。
// seenName 记已经同步过的库名：同名的两个库会落进同一棵本地目录树，后一个拒绝
func (h *Handler) fullSyncLib(ops *pan115Ops, cookie string, p fullParams, t fullTarget, multi bool, filter *syncFilter,
	seenName map[string]string, domain, format string, keepExt, skipExist bool) (fullLibResult, error) {
	var r fullLibResult
	cid := t.cid
	// 媒体库的本地目录名（如"俱乐部"），作为 STRM 路径的第一层。定过本地名就不再问 115（liblocal.go）；
	// 附加账号的库本地名必填，不问 115；媒体库表里的库都带本地目录（可以多级）
	if t.local != "" {
		r.name = normLibLocal(t.local)
	} else {
		r.name = libDirName(cookie, cid)
	}
	if multi {
		if r.name == "" {
			return r, fmt.Errorf("读不出媒体库目录名（可能已删除，或暂时连不上 115）")
		}
		if prev, ok := seenName[libLocalKey(r.name)]; ok {
			return r, fmt.Errorf("与媒体库 cid=%s 的本地目录名都是「%s」，会落进同一个本地目录，请到「账号与媒体库 → 媒体库」给其中一个改本地目录名", prev, r.name)
		}
		seenName[libLocalKey(r.name)] = cid
	}

	log.Printf("[同步] ▶ 全量同步开始（媒体库 %s cid=%s → %s）", firstNonEmpty(r.name, "?"), cid, p.LocalPath)
	// 整理工作区不参与同步（见 orgSkipCids）；打印各槽位配置情况，配错配漏一眼可见。
	// 工作区都在主号上，附加账号的库不用排除
	var skipCids map[string]bool
	if t.acct == acctMain {
		var slots []string
		skipCids, slots = h.orgSkipCids(cid)
		vlog("[同步] ○ 整理工作区排除: %s（✗ 的槽位对应目录会被当成媒体同步，请到对应配置卡重新选择目录）", strings.Join(slots, " "))
	}

	// 取清单（模式见 collectSyncFiles）；库名作为 STRM 路径第一层
	var videos, assets []remoteFile
	modeUsed, complete, err := h.collectSyncFiles(ops, cookie, p.Mode, cid, r.name, &videos, &assets, filter, skipCids, SetTaskProgress)
	if err != nil {
		return r, fmt.Errorf("遍历 115 目录失败: %w", err)
	}
	r.modeUsed, r.complete, r.videos, r.assets = modeUsed, complete, len(videos), len(assets)
	if t.acct != acctMain {
		// 附加账号的文件：直链与台账都带账号（acctlibs.go）
		for i := range videos {
			videos[i].Acct = t.acct
		}
		for i := range assets {
			assets[i].Acct = t.acct
		}
	}

	SetTaskProgress(fmt.Sprintf("落盘：视频 %d + 附属 %d", len(videos), len(assets)))
	r.st = applySyncResults(h.DB, ops, videos, assets, p.LocalPath, domain, format, keepExt, skipExist, "")

	// 失效 STRM 标记：台账里有、但本次扫描没见到的文件 = 网盘上已被删除。
	// 三个前提缺一不可——用户开了开关、清单完整、拿得到库名（台账按库名前缀分区，
	// 多个库时正是这个前缀让 A 库的全量不去碰 B 库的台账）。
	// 只打标不删，删除由用户在 Strm 管理页看过预览后手动触发
	if h.orphanDetectEnabled() {
		switch {
		case !complete:
			log.Printf("[同步] ○ 本次清单不完整，跳过失效 STRM 标记（避免把没取全的文件误判成已删除）")
		case r.name == "":
			log.Printf("[同步] ○ 取不到媒体库根目录名，跳过失效 STRM 标记（台账按库名前缀区分不同媒体库）")
		default:
			seen := make(map[string]bool, len(videos)+len(assets))
			for _, f := range videos {
				seen[f.ledgerFid()] = true
			}
			for _, f := range assets {
				seen[f.ledgerFid()] = true
			}
			marked, cleared, total := markOrphans(h.DB, r.name, seen)
			r.orphans = total
			if marked+cleared+total > 0 {
				log.Printf("[同步] ○ 失效 STRM 标记（%s）：新增 %d，恢复 %d，当前共 %d 个待清理（Strm 管理页确认后删除）",
					r.name, marked, cleared, total)
			}
		}
	}
	return r, nil
}

// ---- 日志分级：simple 模式静默过程性日志（目录遍历/搜索/302/播放改写），只留关键节点与异常 ----
var (
	cfgGlobal *config.Config // SetupRoutes 注入，供无 Handler 上下文的日志分级读取配置

	logVerboseMu  sync.Mutex
	logVerboseVal = true
	logVerboseAt  time.Time
)

// verboseLogging 当前是否详细模式（30 秒缓存；log-level=simple 时为简洁）
func verboseLogging() bool {
	logVerboseMu.Lock()
	defer logVerboseMu.Unlock()
	if time.Since(logVerboseAt) < 30*time.Second {
		return logVerboseVal
	}
	logVerboseAt = time.Now()
	level := ""
	if cfgGlobal != nil {
		level = cfgGlobal.GetSetting("log-level")
	}
	if level == "" {
		var sRow model.Setting
		if model.DB != nil && model.DB.Where("`key` = ?", "log-level").First(&sRow).Error == nil {
			level = sRow.Value
		}
	}
	logVerboseVal = level != "simple"
	return logVerboseVal
}

// vlog 过程性日志：仅详细模式输出
func vlog(format string, args ...interface{}) {
	if verboseLogging() {
		log.Printf(format, args...)
	}
}

// RelaxedMediaPerms 媒体卷宽松权限模式：新建目录 0777、文件 0666（umask 只减不增，
// 实际落地常见为 0755/0644），并在启动时对存量树补 chmod——Emby 等容器需往媒体
// 目录写 poster/nfo（此前 0755 导致其 Permission denied，回退只存内部库）
// RelaxedMediaPerms 对存量媒体树补宽松权限（幂等，异步执行不阻塞启动）
func (h *Handler) RelaxedMediaPerms() {
	local := mediaRoot() // mediaroot.go
	go func() {
		dirs, files := relaxMediaPerms(local)
		if dirs+files > 0 {
			log.Printf("[系统] ○ 媒体目录宽松权限已应用: %d 个目录 / %d 个文件（Emby 可写入 poster/nfo）", dirs, files)
		}
	}()
}

// relaxMediaPerms 只给权限不对的条目 chmod，返回实际改了几个。
//
// ⚠️ 别改回「每个都 chmod 一遍」：Linux 上权限没变的 chmod 照样发 inotify IN_ATTRIB，
// Emby 的实时监控把整棵树每一集都排进刷新队列。2026-10-08 现场：容器重启后几秒内
// 「剧集」「动漫番剧」上万条 will be refreshed，Emby 的刷新队列堵了十三分钟，
// 期间整理提交的入库刷新一直排在后面，凡人修仙传 189 集晚了 11 分钟才入库
func relaxMediaPerms(root string) (dirs, files int) {
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		want := os.FileMode(0o666)
		if d.IsDir() {
			want = 0o777
		}
		// os.Stat 跟随软链，与 os.Chmod 改的是同一个对象
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() == want {
			return nil
		}
		if os.Chmod(p, want) == nil {
			if d.IsDir() {
				dirs++
			} else {
				files++
			}
		}
		return nil
	})
	return dirs, files
}

// orgSkipCids 整理工作区（转存/待整理、已存在、冗余）的 cid 集合，
// 同步遍历时跳过这些子树——同步媒体库根目录时，工作区里等待处理的
// 内容不应生成 STRM。rootCid 自身不计入（同步目标就是工作区时照常执行）。
// 第二个返回值为各槽位的配置情况（"待整理:✓/✗" 列表，供日志诊断配错配漏）
func (h *Handler) orgSkipCids(rootCid string) (map[string]bool, []string) {
	skip := map[string]bool{}
	var orgSkip struct {
		Existing  string `json:"existing"`
		Redundant string `json:"redundant"`
	}
	var shareSkip struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("org-basic")), &orgSkip)
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareSkip)
	slots := make([]string, 0, 4)
	for _, s := range []struct{ name, cid string }{
		{"转存/待整理", shareSkip.Folder}, {"已存在", orgSkip.Existing},
		{"冗余", orgSkip.Redundant},
	} {
		mark := "✗"
		if s.cid != "" {
			if s.cid == rootCid {
				// 配成了同步根本身：引擎不把同步根当工作区，实际不会跳过
				mark = "=同步根(不生效)"
			} else {
				mark = "✓"
				skip[s.cid] = true
			}
		}
		slots = append(slots, s.name+":"+mark)
	}
	return skip, slots
}

// assetDLWorkers 附属文件并发下载线程数（CDN 下载不占 API 限额，CMS 同款思路）
const assetDLWorkers = 5

// applyStats 一批落盘的结果。
//
// 刻意把「新写的」和「本来就有的」分开：两者混在一个 strmCreated 里之后，
// 任何一次重复遍历都会报成一堆「新增」，日志、完成汇总、Emby 刷新
// 全都跟着误触发（见 writeStrm 的注释）
type applyStats struct {
	StrmCreated      int // 真正新写/改写的 strm
	StrmExisting     int // 本地已有且内容一致，没动
	StrmFailed       int
	AssetsDownloaded int
	AssetsSkipped    int
	AssetsFailed     int

	// Written 真正新写/改写的 strm 的相对路径（台账口径，含库名前缀）。同步后自动刮削按它找片目
	Written []string
}

// applySyncResults 对遍历结果执行落盘：视频生成 strm，附属文件下载（已存在跳过），
// 全部登记到 SyncedFile 台账（move/delete 事件精确执行的依据）
func applySyncResults(db *gorm.DB, ops *pan115Ops, videos, assets []remoteFile, localPath, domain, format string, keepExt, skipExist bool, dirLabel string) (st applyStats) {
	// 视频：先全部生成 STRM，成功的收集后批量 upsert（此前逐条独立写事务，
	// 万级视频全量同步即万次写）
	videoRows := make([]model.SyncedFile, 0, len(videos))
	// 本批已占用的 STRM 路径 → fid。台账在循环结束后才批量写，同一批里同目录同基名的
	// 两个视频（X.mkv / X.mp4）strmNameFor 都查不到对方，要在这里让后来的退回旧写法
	claimed := map[string]string{}
	for _, f := range videos {
		name := strmNameFor(f)
		if owner, ok := claimed[path.Join(f.Path, name)]; ok && owner != f.ledgerFid() {
			name = legacyStrmNameOf(f.Name)
		}
		claimed[path.Join(f.Path, name)] = f.ledgerFid()
		strmRel, wrote, err := writeStrmNamed(localPath, domain, format, keepExt, skipExist, f, name)
		if err != nil {
			log.Printf("[同步] 生成 STRM 失败: %s/%s: %v", f.Path, f.Name, err)
			st.StrmFailed++
			continue
		}
		if wrote {
			st.StrmCreated++
			st.Written = append(st.Written, strmRel)
		} else {
			st.StrmExisting++
		}
		videoRows = append(videoRows, model.SyncedFile{
			FileID: f.ledgerFid(), PickCode: f.playID(), Account: f.Acct,
			RelPath: strmRel, Kind: "video", Size: f.Size, Sha1: f.Sha1,
		})
	}
	upsertSyncedFiles(db, videoRows)

	// 附属文件：生产者串行取直链（守 API 间隔），worker 池并发下载
	type assetJob struct {
		f    remoteFile
		url  string
		hdrs map[string]string
	}
	type assetRes struct {
		f      remoteFile
		status string
		err    error
	}
	jobs := make(chan assetJob)
	resCh := make(chan assetRes, len(assets))
	var wg sync.WaitGroup
	for i := 0; i < assetDLWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				data, err := downloadAssetBytes(j.url, j.hdrs, ops.cookieForDL())
				if err != nil {
					resCh <- assetRes{f: j.f, err: err}
					continue
				}
				st, err := writeAssetBytes(j.f, localPath, data)
				resCh <- assetRes{f: j.f, status: st, err: err}
			}
		}()
	}
	for i, f := range assets {
		if i%20 == 0 && i > 0 {
			log.Printf("[同步] 附属文件进度: %d/%d", i, len(assets))
		}
		dst := filepath.Join(localPath, filepath.FromSlash(f.Path), f.Name)
		if _, err := os.Stat(dst); err == nil {
			resCh <- assetRes{f: f, status: "skip"}
			upsertSyncedFile(db, f, path.Join(f.Path, f.Name), "asset")
			continue
		}
		u, hdrs, err := ops.downloadURLFull(f.PickCode, "")
		if err != nil {
			resCh <- assetRes{f: f, err: err}
			continue
		}
		jobs <- assetJob{f: f, url: u, hdrs: hdrs}
	}
	close(jobs)
	wg.Wait()
	close(resCh)
	for r := range resCh {
		switch {
		case r.err != nil:
			st.AssetsFailed++
			log.Printf("[同步] 附属文件失败: %s/%s: %v", r.f.Path, r.f.Name, r.err)
		case r.status == "skip":
			st.AssetsSkipped++
		default:
			st.AssetsDownloaded++
			upsertSyncedFile(db, r.f, path.Join(r.f.Path, r.f.Name), "asset")
		}
	}
	return
}

// upsertSyncedFile 登记本地文件台账（file_id 唯一）
func upsertSyncedFile(db *gorm.DB, f remoteFile, relPath, kind string) {
	if db == nil || f.Fid == "" {
		return
	}
	sf := model.SyncedFile{FileID: f.ledgerFid(), PickCode: f.playID(), RelPath: relPath, Kind: kind, Size: f.Size, Sha1: f.Sha1, Account: f.Acct}
	if ledgerFidTaken(db, []model.SyncedFile{sf})[sf.FileID] {
		return
	}
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pick_code", "rel_path", "kind", "size", "sha1", "account", "updated_at"}),
	}).Create(&sf)
}

// upsertSyncedFiles 批量 upsert 台账（file_id 冲突更新，分批 200 条一次事务）
func upsertSyncedFiles(db *gorm.DB, sfs []model.SyncedFile) {
	if db == nil || len(sfs) == 0 {
		return
	}
	if taken := ledgerFidTaken(db, sfs); len(taken) > 0 {
		keep := sfs[:0:0]
		for _, sf := range sfs {
			if !taken[sf.FileID] {
				keep = append(keep, sf)
			}
		}
		if sfs = keep; len(sfs) == 0 {
			return
		}
	}
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pick_code", "rel_path", "kind", "size", "sha1", "account", "updated_at"}),
	}).CreateInBatches(&sfs, 200)
}

// writeAssetBytes 把附属文件内容写到本地（.part 临时文件原子改名）
func writeAssetBytes(f remoteFile, localRoot string, data []byte) (string, error) {
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	dst := filepath.Join(dir, f.Name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, data, 0o666); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return "download", nil
}

// getSettingValue 读取配置：yaml 优先，数据库回退（兼容旧数据）
// 前端 saveConfig 保存到 yaml，早期版本保存到 DB，两处都要能读到
func (h *Handler) getSettingValue(key string) string {
	if h.Config != nil {
		if v := h.Config.GetSetting(key); v != "" {
			return v
		}
	}
	if h.DB == nil {
		return ""
	}
	var s model.Setting
	if err := h.DB.Where("key = ?", key).First(&s).Error; err == nil {
		return s.Value
	}
	return ""
}

// fullRefreshEmbyEnabled 全量同步结束后是否通知 Emby 刷新（setting「full」.refresh_emby，默认关）。
//
// 增量同步不受这个开关管：它传的是本轮真正变动的最浅目录，刷新范围小得多，
// 不通知反而会让「网盘删了片子 Emby 里条目还在」这个坑一直在
func (h *Handler) fullRefreshEmbyEnabled() bool {
	var cfg struct {
		RefreshEmby bool `json:"refresh_emby"`
	}
	return json.Unmarshal([]byte(h.getSettingValue("full")), &cfg) == nil && cfg.RefreshEmby
}

// markEventsCoveredByFullSync 全量同步完成后调用：整库扫描已经覆盖了一切，
// 事件窗口里的东西不用增量再处理一遍。
//
// 改造前是把最近 1000 条事件逐条插成 applied（千次独立写事务 + 34 次请求）。
// 有了游标之后只剩两件事：把还没消费的事件标掉，再把游标推到最新那条
func (h *Handler) markEventsCoveredByFullSync(cookie string) (int, error) {
	now := time.Now()
	res := h.DB.Model(&model.SyncEvent{}).Where("status = ?", "pending").
		Updates(map[string]interface{}{"status": "applied", "applied_at": now})
	covered := int(res.RowsAffected)

	f := &lifeFetcher{
		cookie: cookie,
		load:   h.getSettingValue,
		save: func(k, v string) {
			if h.Config != nil {
				h.Config.SaveSetting(k, v)
			}
		},
	}
	evs, _, err := f.page(1, 0)
	if err != nil {
		return covered, err
	}
	if len(evs) > 0 && evs[0].ID != "" && h.Config != nil {
		ts, _ := strconv.ParseInt(strings.TrimSpace(evs[0].Time), 10, 64)
		h.Config.SaveSetting("incr-cursor", encodeLifeCursor(lifeCursor{FromID: evs[0].ID, FromTime: ts}))
	}
	return covered, nil
}

// rename115 重命名网盘文件（单个；批量场景用 rename115Batch）
func rename115(cookie, fid, newName string) error {
	_, err := rename115Batch(cookie, map[string]string{fid: newName})
	return err
}

// rename115Post 测试时替换请求出口；正式环境始终走带全局节流的 httpPostForm115。
var rename115Post = httpPostForm115

// 参数错误若来自接口整体变化而不是某个坏文件，无限二分会把一次失败放大成
// 数百次写请求。32 次足够把 223 项按约 10 项一组探明，也覆盖定位单个坏项。
const rename115MaxAdaptiveRequests = 32

// rename115Batch 批量重命名。返回真正已经改名成功的项目；后续批次失败时，
// 调用方必须只按这部分新名字落盘，不能把已经改过的文件又当成原名。
// 逐个调用时每个文件都要过一遍 API 限流（3 秒/次），24 集的重命名
// 仅等待就要 70+ 秒。p115client 的 fs_rename 支持整批提交，update_name 默认
// batch_size=1000，因此不能凭一次 223 项失败臆定固定上限；只有服务端明确报
// 「参数错误」时才二分重试，既能适应隐藏的数量约束，也能定位单个坏参数。
func rename115Batch(cookie string, names map[string]string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	fids := make([]string, 0, len(names))
	for fid := range names {
		fids = append(fids, fid)
	}
	sort.Strings(fids) // map 遍历无序；二分边界固定后失败结果和测试才可复现
	attempts := 0
	return rename115BatchPart(cookie, names, fids, &attempts)
}

func rename115BatchPart(cookie string, names map[string]string, fids []string, attempts *int) (map[string]string, error) {
	if *attempts >= rename115MaxAdaptiveRequests {
		return nil, fmt.Errorf("参数错误拆分已达 %d 次请求上限，停止继续尝试", rename115MaxAdaptiveRequests)
	}
	*attempts = *attempts + 1
	form := url.Values{}
	for _, fid := range fids {
		form.Set("files_new_name["+fid+"]", names[fid])
	}
	body, err := rename115Post("https://webapi.115.com/files/batch_rename", form, cookie, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var r struct {
		State  bool              `json:"state"`
		Error  string            `json:"error"`
		ErrMsg string            `json:"errMsg"`
		Data   map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("响应无法解析: %s", truncateStr(string(body), 150))
	}
	if r.State {
		// p115client.update_name 也是以 data 中返回的 id→名字作为成功清单，
		// 不能只看 state=true 就把整批都算成功。
		renamed := make(map[string]string, len(r.Data))
		for _, fid := range fids {
			if _, ok := r.Data[fid]; ok {
				renamed[fid] = names[fid]
			}
		}
		if len(renamed) != len(fids) {
			return renamed, fmt.Errorf("成功响应只确认了 %d/%d 个文件", len(renamed), len(fids))
		}
		return renamed, nil
	}
	msg := r.Error
	if msg == "" {
		msg = r.ErrMsg
	}
	if msg == "" {
		msg = "未知错误"
	}
	// 参数错误既可能是整包触发隐藏约束，也可能是其中一个文件名有问题。
	// 二分后成功的半边立刻记账，失败半边继续缩小；鉴权/风控等错误绝不拆分，
	// 否则会在同一个坏状态下制造大量 115 请求。
	if len(fids) > 1 && rename115IsParamError(msg) {
		mid := len(fids) / 2
		left, leftErr := rename115BatchPart(cookie, names, fids[:mid], attempts)
		right, rightErr := rename115BatchPart(cookie, names, fids[mid:], attempts)
		renamed := make(map[string]string, len(left)+len(right))
		for fid, name := range left {
			renamed[fid] = name
		}
		for fid, name := range right {
			renamed[fid] = name
		}
		if leftErr == nil && rightErr == nil {
			return renamed, nil
		}
		return renamed, fmt.Errorf("拆分重试后仍有 %d/%d 个文件失败: %w",
			len(fids)-len(renamed), len(fids), errors.Join(leftErr, rightErr))
	}
	if len(fids) == 1 {
		return nil, fmt.Errorf("文件 %s → %s 被拒: %s", fids[0], truncateStr(names[fids[0]], 100), msg)
	}
	return nil, fmt.Errorf("批量重命名被拒: %s", msg)
}

func rename115IsParamError(msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	return strings.Contains(msg, "参数") || strings.Contains(msg, "param") || strings.Contains(msg, "argument")
}

// getStrmConfig 读取 STRM 直链配置。
// 配置由前端 SaveSetting 写进 setting.yaml，只读 DB 的旧写法永远读不到
func (h *Handler) getStrmConfig() (domain, format string, keepExt, skipExist bool) {
	return parseStrmConfig(h.getSettingValue("strm"))
}

// parseStrmConfig 解析 STRM 直链配置 JSON。空串/坏 JSON/缺字段都回落到默认值。
// 302 反代侧（readStrmLinkConfig）拿的是同一份配置，两边必须解析出同样的结果，
// 否则 strm 里写的地址和播放时改写出来的地址会对不上——所以只留这一份实现
func parseStrmConfig(raw string) (domain, format string, keepExt, skipExist bool) {
	domain = "http://172.17.0.1:6086"
	format = "pick_code_name"
	keepExt = true
	skipExist = false // false=覆盖
	if raw == "" {
		return
	}
	var cfg struct {
		Domain  string `json:"domain"`
		Format  string `json:"format"`
		KeepExt any    `json:"keep_ext"`
		Exist   string `json:"exist"`
	}
	if json.Unmarshal([]byte(raw), &cfg) == nil {
		if cfg.Domain != "" {
			domain = cfg.Domain
		}
		if cfg.Format != "" {
			format = cfg.Format
		}
		switch v := cfg.KeepExt.(type) {
		case bool:
			keepExt = v
		case string:
			keepExt = v == "true"
		}
		if cfg.Exist == "skip" {
			skipExist = true // skip=true 表示跳过已存在
		}
	}
	return
}

// writeStrm 生成单个 .strm 文件
// URL 形态（CMS 同款，代理端按 pickcode 查文件）：
//
//	pick_code      {domain}/d/{pickcode}[.ext]
//	pick_code_name {domain}/d/{pickcode}[.ext]?/{原文件名}
//
// 「保留文件后缀」= pickcode 段是否带 .ext（播放器据 URL 后缀识别容器格式）；
// ?/ 之后的文件名仅供播放器展示与识别，代理忽略查询串
// writeStrm 落一个 .strm。返回 wrote=true 表示**内容真的变了**（新建或改写）。
//
// 为什么要把「写了没有」报出去：改造前它只返回 error，跳过已存在也返回 nil，
// 调用方照样 strmCreated++。于是任何一次重复遍历都会把整棵树的老文件
// 全部报成「新增视频 N 个」，还连带触发一次 Emby 刷新——用户看到的
// 「全盘 strm 一直在重读重建」有一大半是这个计数造成的错觉。
// 判据不用 skipExist：关掉「跳过已存在」时内容一致的重写也不是新增
//
// strmRel 是实际写成的 STRM 相对路径（相对 localRoot）：文件名由 strmNameFor 决定，
// 调用方登记台账、通知 Emby 一律用它，不要自己拼
func writeStrm(localRoot, domain, format string, keepExt, skipExist bool, f remoteFile) (strmRel string, wrote bool, err error) {
	return writeStrmNamed(localRoot, domain, format, keepExt, skipExist, f, strmNameFor(f))
}

// writeStrmNamed 同 writeStrm，文件名由调用方定（同一批里去重同名视频时用）
func writeStrmNamed(localRoot, domain, format string, keepExt, skipExist bool, f remoteFile, strmName string) (strmRel string, wrote bool, err error) {
	streamURL := strmURLFor(domain, format, keepExt, f)

	// 本地目录：保持网盘目录结构
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", false, err
	}

	strmRel = path.Join(f.Path, strmName)
	strmPath := filepath.Join(dir, strmName)

	// 已存在：配置了跳过就跳过；没配跳过但内容一模一样，写了也是原样，同样算没动。
	// 例外：直链还没带 .iso 的 ISO 照写 —— 存量补 .iso 靠全量同步走到这里（strmiso.go）
	if old, err := os.ReadFile(strmPath); err == nil {
		if string(old) == streamURL || (skipExist && !strmMissingISOExt(string(old), f.Name)) {
			return strmRel, false, nil
		}
	}

	if err := os.WriteFile(strmPath, []byte(streamURL), 0o666); err != nil {
		return "", false, err
	}
	markFreshStrm(strmPath) // 入库确认后只探新写出的（embyfresh.go）
	return strmRel, true, nil
}

// strmURLFor 这个视频的 STRM 内容（本站直链）。写 STRM 与接管已有 STRM（adopt.go）共用这一份拼法
func strmURLFor(domain, format string, keepExt bool, f remoteFile) string {
	base := strings.TrimRight(domain, "/")
	idPart := f.playID() // 附加账号的文件是 {账号}~{pickcode}（acctlibs.go）
	if keepExt || isISOName(f.Name) {
		// ISO 不看开关一律带 .iso：STRM 名不带扩展名之后，直链是本地唯一认得出光盘镜像的地方（strmiso.go）
		idPart += pathExt(f.Name)
	}
	if format == "pick_code" {
		return fmt.Sprintf("%s/d/%s", base, idPart)
	}
	// 「#」要转义：同集多份保留时的「xxx#A.mkv」（orgdup.go），不转的话 # 之后被当成 URL 片段截掉
	return fmt.Sprintf("%s/d/%s?/%s", base, idPart, strings.ReplaceAll(f.Name, "#", "%23"))
}

// ledgerFidTaken 这些行里，fid 已经被**别的账号**的台账行占着的（返回这些 fid）。
// 115 的 fid 是全局编号（model.SyncedFile），正常不会撞；真撞上了不覆盖别的账号的行、记一行日志，
// 宁可这个文件这次不进台账，也不能把两个账号的文件串成一行（MULTI-ACCOUNT-V2-PLAN.md §10）
func ledgerFidTaken(db *gorm.DB, sfs []model.SyncedFile) map[string]bool {
	byAcct := map[uint][]string{}
	for _, sf := range sfs {
		byAcct[sf.Account] = append(byAcct[sf.Account], sf.FileID)
	}
	taken := map[string]bool{}
	for acct, fids := range byAcct {
		for _, batch := range chunkStrings(fids, 500) {
			var rows []model.SyncedFile
			db.Select("file_id", "account", "rel_path").Where("file_id IN ? AND account <> ?", batch, acct).Find(&rows)
			for _, r := range rows {
				taken[r.FileID] = true
				log.Printf("[同步] ✗ fid %s 已经是账号 %d 的文件（%s），账号 %d 的同一个 fid 没写进台账：115 的 fid 跨账号撞了，请反馈", r.FileID, r.Account, r.RelPath, acct)
			}
		}
	}
	return taken
}
