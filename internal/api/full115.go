package api

import (
	"encoding/json"
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

	"strmhub/internal/config"
	"strmhub/internal/model"

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
}

var (
	recentRunsMu sync.Mutex
	recentRuns   []runRecord
)

// RecordRun 记录一次任务运行（保留最近 5 次）
func RecordRun(name string, start time.Time, ok bool) {
	rec := runRecord{
		Name:    name,
		Start:   start.Format("01-02 15:04:05"),
		Elapsed: time.Since(start).Truncate(time.Second).String(),
		OK:      ok,
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

// fullSyncMu 全量同步互斥：防止重复点击导致两个同步并发互相干扰
var fullSyncMu sync.Mutex

// taskState 当前任务状态（供前端展示与按钮禁用，含 cron 触发的任务）
var (
	taskStateMu  sync.Mutex
	taskRunning  bool
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
	taskRunning, taskName, taskStart, taskProgress = true, name, time.Now(), ""
	taskStateMu.Unlock()
}

func endTask() {
	taskStateMu.Lock()
	name := taskName
	start := taskStart
	taskRunning, taskProgress = false, ""
	taskStateMu.Unlock()
	// 自动记录到运行历史
	RecordRun(name, start, true)
}

// TaskStatus 当前任务状态快照（含进度描述）
func TaskStatus() (bool, string, time.Time, string) {
	taskStateMu.Lock()
	defer taskStateMu.Unlock()
	return taskRunning, taskName, taskStart, taskProgress
}

// RunFullSync 执行全量同步：递归遍历 cid 目录，视频生成 .strm，附属文件实体落盘
// 附属文件 = 用户配置的图片后缀 + 数据文件后缀 + nfo（Emby/Jellyfin 标准元数据）；
// 不在过滤集合内的文件一律不同步
// POST /sync/full  body: {"cid":"...","local_path":"...","video_ext":["mp4"],"image_ext":["jpg"],"data_ext":["ass"]}
func (h *Handler) RunFullSync(c *gin.Context) {
	var req struct {
		Cid       string   `json:"cid"`
		LocalPath string   `json:"local_path"`
		VideoExt  []string `json:"video_ext"`
		ImageExt  []string `json:"image_ext"`
		DataExt   []string `json:"data_ext"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Cid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：请填写 115 媒体库 cid"})
		return
	}
	if req.LocalPath == "" {
		req.LocalPath = defaultLocalPath
	}

	// 同一时刻只允许一个全量同步
	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("全量同步")
	defer endTask()
	fullStart := time.Now()

	// 读取 STRM 直链配置
	domain, format, keepExt, skipExist := h.getStrmConfig()

	// 构造统一操作通道（OpenAPI 优先，Cookie 回退）
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 过滤器：视频组生成 strm；附属组 = 图片后缀 ∪ 数据后缀 ∪ .nfo（始终包含）
	filter := &syncFilter{
		videoExts: buildExtSet(req.VideoExt),
		assetExts: buildExtSet(append(append([]string{}, req.ImageExt...), req.DataExt...)),
	}
	filter.assetExts[".nfo"] = true

	// 获取媒体库根目录名（如"俱乐部"），作为 STRM 路径的第一层
	libName := ""
	if cookie, err := h.get115Cookie(); err == nil {
		if info, err := get115DirInfo(cookie, req.Cid); err == nil {
			libName = info.n
		}
	}

	log.Printf("[同步] ▶ 全量同步开始（媒体库 cid=%s → %s）", req.Cid, req.LocalPath)
	// 整理工作区不参与同步（见 orgSkipCids）；打印各槽位配置情况，配错配漏一眼可见
	skipCids, slots := h.orgSkipCids(req.Cid)
	vlog("[同步] ○ 整理工作区排除: %s（✗ 的槽位对应目录会被当成媒体同步，请到对应配置卡重新选择目录）", strings.Join(slots, " "))

	// 递归遍历，basePath 加上库名使 STRM 路径包含该层
	SetTaskProgress("正在遍历 115 媒体库（已发现视频可在日志查看）…")
	var videos, assets []remoteFile
	if err := walk115Dir(ops, req.Cid, libName, &videos, &assets, filter, skipCids); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "遍历 115 目录失败: " + err.Error()})
		return
	}

	SetTaskProgress(fmt.Sprintf("落盘：视频 %d + 附属 %d", len(videos), len(assets)))
	strmCreated, downloaded, skipped, failed := applySyncResults(h.DB, ops, videos, assets, req.LocalPath, domain, format, keepExt, skipExist, "")

	totalNew := strmCreated + downloaded
	if totalNew > 0 {
		h.notifyEmbyRefresh(req.LocalPath)
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
	log.Printf("[同步] 全量同步完成：视频 %d 个（生成 STRM %d），附属文件下载 %d 个，用时 %s",
		len(videos), strmCreated, downloaded, time.Since(fullStart).Truncate(time.Second))

	c.JSON(http.StatusOK, gin.H{
		"message":           "全量同步完成",
		"elapsed":           time.Since(fullStart).Truncate(time.Second).String(),
		"total":             len(videos),
		"created":           strmCreated,
		"assets_total":      len(assets),
		"assets_downloaded": downloaded,
		"assets_skipped":    skipped,
		"assets_failed":     failed,
	})
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

// orphanSafeExts 孤儿清理只碰这些后缀（strm 与附属），其他文件一律不动
var orphanSafeExts = map[string]bool{
	".strm": true, ".srt": true, ".ass": true, ".ssa": true, ".sub": true,
	".vtt": true, ".smi": true, ".nfo": true, ".jpg": true, ".jpeg": true,
	".png": true, ".webp": true,
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

// applySyncResults 对遍历结果执行落盘：视频生成 strm，附属文件下载（已存在跳过），
// 全部登记到 SyncedFile 台账（move/delete 事件精确执行的依据）
func applySyncResults(db *gorm.DB, ops *pan115Ops, videos, assets []remoteFile, localPath, domain, format string, keepExt, skipExist bool, dirLabel string) (strmCreated, downloaded, skipped, failed int) {
	// 视频：先全部生成 STRM，成功的收集后批量 upsert（此前逐条独立写事务，
	// 万级视频全量同步即万次写）
	videoRows := make([]model.SyncedFile, 0, len(videos))
	for _, f := range videos {
		if err := writeStrm(localPath, domain, format, keepExt, skipExist, f); err != nil {
			log.Printf("[同步] 生成 STRM 失败: %s/%s: %v", f.Path, f.Name, err)
			continue
		}
		strmCreated++
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
			failed++
			log.Printf("[同步] 附属文件失败: %s/%s: %v", r.f.Path, r.f.Name, r.err)
		case r.status == "skip":
			skipped++
		default:
			downloaded++
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
	if v := h.Config.GetSetting(key); v != "" {
		return v
	}
	var s model.Setting
	if err := h.DB.Where("key = ?", key).First(&s).Error; err == nil {
		return s.Value
	}
	return ""
}

// markEventsCoveredByFullSync 全量同步完成后调用：
// 把事件窗口内的生活事件直接落库并标记为已处理（全量已覆盖一切，无需增量再处理）
func (h *Handler) markEventsCoveredByFullSync(cookie string) (int, error) {
	count := 0
	offset := 0
	for {
		events, err := fetch115LifeEvents(cookie, 30, offset, "")
		if err != nil {
			return count, err
		}
		fresh := 0
		for _, ev := range events {
			if ev.ID == "" {
				continue
			}
			ts, _ := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64)
			now := time.Now()
			se := model.SyncEvent{
				EventID: ev.ID, Type: ev.Type, FileID: ev.FileID,
				FileName: ev.FileName, Cid: ev.Cid, Size: ev.Size,
				EventTime: ts, Status: "applied", AppliedAt: &now,
			}
			res := h.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&se)
			if res.Error == nil && res.RowsAffected > 0 {
				fresh++
				count++
			}
		}
		if fresh == 0 || count >= 1000 {
			break
		}
		offset += len(events)
		if len(events) < 30 {
			break
		}
	}
	return count, nil
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

// getStrmConfig 读取 STRM 直链配置
func (h *Handler) getStrmConfig() (domain, format string, keepExt, exist bool) {
	domain = "http://172.17.0.1:6086"
	format = "pick_code_name"
	keepExt = true
	exist = false // false=覆盖
	var s model.Setting
	if err := h.DB.Where("key = ?", "strm").First(&s).Error; err != nil {
		return
	}
	var cfg struct {
		Domain  string `json:"domain"`
		Format  string `json:"format"`
		KeepExt any    `json:"keep_ext"`
		Exist   string `json:"exist"`
	}
	if json.Unmarshal([]byte(s.Value), &cfg) == nil {
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
			exist = true // skip=true 表示跳过已存在
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
func writeStrm(localRoot, domain, format string, keepExt, skipExist bool, f remoteFile) error {
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
		return err
	}

	strmName := f.Name + ".strm"
	strmPath := filepath.Join(dir, strmName)

	// 如果配置为跳过已存在，且文件已存在则跳过
	if skipExist {
		if _, err := os.Stat(strmPath); err == nil {
			return nil
		}
	}

	return os.WriteFile(strmPath, []byte(streamURL), 0o666)
}
