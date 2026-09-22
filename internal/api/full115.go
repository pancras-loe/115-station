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

// ==================== 任务运行历史（最近 5 次） ====================

type runRecord struct {
	Name    string `json:"name"`
	Start   string `json:"start"`
	Elapsed string `json:"elapsed"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

var (
	recentRunsMu sync.Mutex
	recentRuns   []runRecord
)

// RecordRun 记录一次任务运行（保留最近 5 次）
func RecordRun(name string, start time.Time, ok bool, messages ...string) {
	rec := runRecord{
		Name:    name,
		Start:   start.Format("01-02 15:04:05"),
		Elapsed: time.Since(start).Truncate(time.Second).String(),
		OK:      ok,
	}
	if len(messages) > 0 {
		rec.Message = messages[0]
	}
	recentRunsMu.Lock()
	defer recentRunsMu.Unlock()
	recentRuns = append(recentRuns, rec)
	if len(recentRuns) > 5 {
		recentRuns = recentRuns[len(recentRuns)-5:]
	}
}

// GetRecentRuns 返回最近运行记录（新的在前）
func GetRecentRuns() []runRecord {
	recentRunsMu.Lock()
	defer recentRunsMu.Unlock()
	out := make([]runRecord, len(recentRuns))
	copy(out, recentRuns)
	// 反转（新的在前）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
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
	taskName     string
	taskStart    time.Time
	taskProgress string // 当前阶段/进度描述（如 "整理 3/12：xxx"），前端轮询展示
)

// SetTaskProgress 更新任务进度描述（任务进行中由各执行器调用）
func SetTaskProgress(text string) {
	taskStateMu.Lock()
	taskProgress = text
	taskStateMu.Unlock()
}

func beginTask(name string) {
	taskStateMu.Lock()
	taskFailure = ""
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
	taskRunning, taskProgress = false, ""
	taskStateMu.Unlock()
	// 自动记录到运行历史
	RecordRun(name, start, failure == "", failure)
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
		Mode      string   `json:"mode"` // normal(默认) / fast
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Cid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：请填写 115 媒体库 cid"})
		return
	}

	// 同一时刻只允许一个全量同步。等一小会儿：正在跑的增量遍历
	// 看到有人排队会提前收工，通常几秒内就能让出锁
	if !taskMu.Acquire("全量同步", manualAcquireWait) {
		c.JSON(http.StatusConflict, gin.H{"error": busyErr()})
		return
	}
	defer taskMu.Unlock()
	beginTask("全量同步")
	defer endTask()

	sum, err := h.executeFullSync(fullParams{
		Cid: req.Cid, LocalPath: req.LocalPath,
		VideoExt: req.VideoExt, ImageExt: req.ImageExt, DataExt: req.DataExt, Mode: req.Mode,
	})
	if err != nil {
		var ce fullConfigErr
		if errors.As(err, &ce) {
			c.JSON(http.StatusBadRequest, gin.H{"error": ce.Error()})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":           "全量同步完成",
		"mode_used":         sum.ModeUsed,
		"scan_complete":     sum.ScanComplete,
		"orphans":           sum.Orphans,
		"elapsed":           sum.Elapsed,
		"total":             sum.Total,
		"created":           sum.Created,
		"existing":          sum.Existing,
		"assets_total":      sum.AssetsTotal,
		"assets_downloaded": sum.AssetsDownloaded,
		"assets_skipped":    sum.AssetsSkipped,
		"assets_failed":     sum.AssetsFailed,
	})
}

// executeFullSync 全量同步核心：递归遍历 cid 目录，视频生成 .strm，附属文件实体落盘。
// 附属文件 = 用户配置的图片后缀 + 数据文件后缀 + nfo（Emby/Jellyfin 标准元数据）；
// 不在过滤集合内的文件一律不同步。
// 调用方负责持有 taskMu 与 beginTask/endTask（HTTP 入口与 cron 调度器都要用）
func (h *Handler) executeFullSync(p fullParams) (*fullSummary, error) {
	if p.LocalPath == "" {
		p.LocalPath = defaultLocalPath
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

	// 获取媒体库根目录名（如"俱乐部"），作为 STRM 路径的第一层
	libName := ""
	cookie, _ := h.get115Cookie()
	if cookie != "" {
		if info, err := get115DirInfo(cookie, p.Cid); err == nil {
			libName = info.n
		}
	}

	log.Printf("[同步] ▶ 全量同步开始（媒体库 cid=%s → %s）", p.Cid, p.LocalPath)
	// 整理工作区不参与同步（见 orgSkipCids）；打印各槽位配置情况，配错配漏一眼可见
	skipCids, slots := h.orgSkipCids(p.Cid)
	vlog("[同步] ○ 整理工作区排除: %s（✗ 的槽位对应目录会被当成媒体同步，请到对应配置卡重新选择目录）", strings.Join(slots, " "))

	// 取清单（模式见 collectSyncFiles）；libName 作为 STRM 路径第一层
	var videos, assets []remoteFile
	modeUsed, complete, err := h.collectSyncFiles(ops, cookie, p.Mode, p.Cid, libName, &videos, &assets, filter, skipCids, SetTaskProgress)
	if err != nil {
		return nil, fmt.Errorf("遍历 115 目录失败: %w", err)
	}

	SetTaskProgress(fmt.Sprintf("落盘：视频 %d + 附属 %d", len(videos), len(assets)))
	st := applySyncResults(h.DB, ops, videos, assets, p.LocalPath, domain, format, keepExt, skipExist, "")

	// 失效 STRM 标记：台账里有、但本次扫描没见到的文件 = 网盘上已被删除。
	// 三个前提缺一不可——用户开了开关、清单完整、拿得到库名（台账按库名前缀分区）。
	// 只打标不删，删除由用户在 Strm 管理页看过预览后手动触发
	orphanTotal := 0
	if h.orphanDetectEnabled() {
		switch {
		case !complete:
			log.Printf("[同步] ○ 本次清单不完整，跳过失效 STRM 标记（避免把没取全的文件误判成已删除）")
		case libName == "":
			log.Printf("[同步] ○ 取不到媒体库根目录名，跳过失效 STRM 标记（台账按库名前缀区分不同媒体库）")
		default:
			seen := make(map[string]bool, len(videos)+len(assets))
			for _, f := range videos {
				seen[f.Fid] = true
			}
			for _, f := range assets {
				seen[f.Fid] = true
			}
			marked, cleared, total := markOrphans(h.DB, libName, seen)
			orphanTotal = total
			if marked+cleared+total > 0 {
				log.Printf("[同步] ○ 失效 STRM 标记：新增 %d，恢复 %d，当前共 %d 个待清理（Strm 管理页确认后删除）",
					marked, cleared, total)
			}
		}
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
	// 之后的增量同步只处理此后发生的新事件
	if cookieOnly, err := h.get115Cookie(); err == nil {
		if n, err := h.markEventsCoveredByFullSync(cookieOnly); err != nil {
			log.Printf("[同步] 标记生活事件已覆盖失败: %v", err)
		} else if n > 0 {
			log.Printf("[同步] 生活事件窗口已标记为已覆盖: %d 条（增量同步只处理此后新事件）", n)
		}
	}
	SetTaskProgress("")
	log.Printf("[同步] 全量同步完成（%s模式）：视频 %d 个（新增 STRM %d，已存在 %d，失败 %d），附属文件下载 %d 个，用时 %s",
		map[string]string{"fast": "快速", "normal": "标准"}[modeUsed],
		len(videos), st.StrmCreated, st.StrmExisting, st.StrmFailed, st.AssetsDownloaded,
		time.Since(fullStart).Truncate(time.Second))

	return &fullSummary{
		ModeUsed:         modeUsed,
		ScanComplete:     complete,
		Orphans:          orphanTotal,
		Elapsed:          time.Since(fullStart).Truncate(time.Second).String(),
		Total:            len(videos),
		Created:          st.StrmCreated,
		Existing:         st.StrmExisting,
		AssetsTotal:      len(assets),
		AssetsDownloaded: st.AssetsDownloaded,
		AssetsSkipped:    st.AssetsSkipped,
		AssetsFailed:     st.AssetsFailed,
	}, nil
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
	local := defaultLocalPath
	var fullCfg struct {
		LocalPath string `json:"local_path"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg) == nil && fullCfg.LocalPath != "" {
		local = fullCfg.LocalPath
	}
	go func() {
		dirs, files := 0, 0
		filepath.WalkDir(local, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if os.Chmod(p, 0o777) == nil {
					dirs++
				}
			} else {
				if os.Chmod(p, 0o666) == nil {
					files++
				}
			}
			return nil
		})
		if dirs+files > 0 {
			log.Printf("[系统] ○ 媒体目录宽松权限已应用: %d 个目录 / %d 个文件（Emby 可写入 poster/nfo）", dirs, files)
		}
	}()
}

// orgSkipCids 整理工作区（待整理/已存在/冗余/转存目录）的 cid 集合，
// 同步遍历时跳过这些子树——同步媒体库根目录时，工作区里等待处理的
// 内容不应生成 STRM。rootCid 自身不计入（同步目标就是工作区时照常执行）。
// 第二个返回值为各槽位的配置情况（"待整理:✓/✗" 列表，供日志诊断配错配漏）
func (h *Handler) orgSkipCids(rootCid string) (map[string]bool, []string) {
	skip := map[string]bool{}
	var orgSkip struct {
		Pending   string `json:"pending"`
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
		{"待整理", orgSkip.Pending}, {"已存在", orgSkip.Existing},
		{"冗余", orgSkip.Redundant}, {"转存目录", shareSkip.Folder},
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
}

// applySyncResults 对遍历结果执行落盘：视频生成 strm，附属文件下载（已存在跳过），
// 全部登记到 SyncedFile 台账（move/delete 事件精确执行的依据）
func applySyncResults(db *gorm.DB, ops *pan115Ops, videos, assets []remoteFile, localPath, domain, format string, keepExt, skipExist bool, dirLabel string) (st applyStats) {
	// 视频：先全部生成 STRM，成功的收集后批量 upsert（此前逐条独立写事务，
	// 万级视频全量同步即万次写）
	videoRows := make([]model.SyncedFile, 0, len(videos))
	for _, f := range videos {
		wrote, err := writeStrm(localPath, domain, format, keepExt, skipExist, f)
		if err != nil {
			log.Printf("[同步] 生成 STRM 失败: %s/%s: %v", f.Path, f.Name, err)
			st.StrmFailed++
			continue
		}
		if wrote {
			st.StrmCreated++
		} else {
			st.StrmExisting++
		}
		videoRows = append(videoRows, model.SyncedFile{
			FileID: f.Fid, PickCode: f.PickCode,
			RelPath: path.Join(f.Path, f.Name+".strm"), Kind: "video", Size: f.Size, Sha1: f.Sha1,
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
	sf := model.SyncedFile{FileID: f.Fid, PickCode: f.PickCode, RelPath: relPath, Kind: kind, Size: f.Size, Sha1: f.Sha1}
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pick_code", "rel_path", "kind", "size", "sha1", "updated_at"}),
	}).Create(&sf)
}

// upsertSyncedFiles 批量 upsert 台账（file_id 冲突更新，分批 200 条一次事务）
func upsertSyncedFiles(db *gorm.DB, sfs []model.SyncedFile) {
	if db == nil || len(sfs) == 0 {
		return
	}
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pick_code", "rel_path", "kind", "size", "sha1", "updated_at"}),
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
	return rename115Batch(cookie, map[string]string{fid: newName})
}

// rename115Batch 批量重命名：一次接口调用改多个文件。
// 逐个调用时每个文件都要过一遍 API 限流（3 秒/次），24 集的重命名
// 仅等待就要 70+ 秒；batch_rename 本就支持多文件表单，合并为一次调用
func rename115Batch(cookie string, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	form := url.Values{}
	for fid, name := range names {
		form.Set("files_new_name["+fid+"]", name)
	}
	body, err := httpPostForm115("https://webapi.115.com/files/batch_rename", form, cookie, 30*time.Second)
	if err != nil {
		return err
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &r) == nil && !r.State {
		return fmt.Errorf("批量重命名被拒: %s", r.Error)
	}
	return nil
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
func writeStrm(localRoot, domain, format string, keepExt, skipExist bool, f remoteFile) (wrote bool, err error) {
	base := strings.TrimRight(domain, "/")
	idPart := f.PickCode
	if keepExt {
		idPart += pathExt(f.Name)
	}
	var streamURL string
	if format == "pick_code" {
		streamURL = fmt.Sprintf("%s/d/%s", base, idPart)
	} else {
		streamURL = fmt.Sprintf("%s/d/%s?/%s", base, idPart, f.Name)
	}

	// 本地目录：保持网盘目录结构
	dir := filepath.Join(localRoot, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return false, err
	}

	strmName := f.Name + ".strm"
	strmPath := filepath.Join(dir, strmName)

	// 已存在：配置了跳过就跳过；没配跳过但内容一模一样，写了也是原样，同样算没动
	if old, err := os.ReadFile(strmPath); err == nil {
		if skipExist || string(old) == streamURL {
			return false, nil
		}
	}

	if err := os.WriteFile(strmPath, []byte(streamURL), 0o666); err != nil {
		return false, err
	}
	return true, nil
}
