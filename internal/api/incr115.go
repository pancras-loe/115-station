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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 生活事件增量 ====================
//
// 接口：P115Client.life_behavior_detail_app
//   GET https://proapi.115.com/{app}/behavior/detail  （app 默认 android，Cookie 认证）
//   参数：type（省略=全部）、limit（≤1000）、offset、date（可选 'YYYY-MM-DD'）
//   注意：有风控风险，两次调用间隔 ≥5 秒；缺少「回收站还原」事件

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

// fetch115LifeEvents 拉取 115 生活事件（type 为空则拉全部类型）
// 响应结构：{state, data: {count, list: [...]}}，事件字段 type/file_id/file_name/cid/file_size/update_time
func fetch115LifeEvents(cookie string, limit, offset int, typ string) ([]lifeEvent, error) {
	query := url.Values{
		"limit":  {fmt.Sprint(limit)},
		"offset": {fmt.Sprint(offset)},
	}
	if typ != "" {
		query.Set("type", typ)
	}
	body, err := httpGet115(behaviorAPI, query, cookie, 20*time.Second)
	if err != nil {
		return nil, err
	}
	var result struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			// 注意：count 在响应中是字符串（"118262"），声明为 int 会导致整体解析失败；
			// 当前不需要该值，故意不解析
			List []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析生活事件失败: %s", truncateStr(string(body), 150))
	}
	if !result.State {
		msg := result.Error
		if msg == "" {
			msg = "state=false"
		}
		return nil, fmt.Errorf("拉取生活事件被拒: %s", msg)
	}
	events := make([]lifeEvent, 0, len(result.Data.List))
	for _, d := range result.Data.List {
		ev := lifeEvent{
			ID:       firstStr(d, "id"),
			Type:     normalizeEventType(fmt.Sprint(d["type"])),
			FileID:   firstStr(d, "file_id", "fid"),
			FileName: firstStr(d, "file_name", "n", "name"),
			Cid:      firstStr(d, "cid", "pid", "parent_id"),
			PickCode: firstStr(d, "pick_code", "pickcode", "pc"),
			Time:     firstStr(d, "update_time", "time", "create_time"),
		}
		if s, ok := d["file_size"].(float64); ok {
			ev.Size = int64(s)
		}
		events = append(events, ev)
	}
	return events, nil
}

// get115DirInfo 查询目录自身的 cid/pid/名称（webapi files/get_info，data 为数组取首项）
type dirInfo struct {
	cid, pid, n string
}

// get115DirInfo 查询目录自身的 cid/pid/名称
func get115DirInfo(cookie, cid string) (dirInfo, error) {
	body, err := httpGet115UA("https://webapi.115.com/files/get_info",
		url.Values{"file_id": {cid}}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return dirInfo{}, err
	}
	var r struct {
		State bool `json:"state"`
		Data  []struct {
			Cid string `json:"cid"`
			Pid string `json:"pid"`
			N   string `json:"n"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || !r.State || len(r.Data) == 0 {
		return dirInfo{}, fmt.Errorf("获取目录信息失败: %s", truncateStr(string(body), 120))
	}
	d := r.Data[0]
	return dirInfo{cid: d.Cid, pid: d.Pid, n: d.N}, nil
}

// get115RelPath 从 cid 逐级向上爬父目录链至 rootCid，返回相对路径（如 电影/香港动画/xxx）
// 爬到网盘根仍未遇到 rootCid 说明不在媒体库内，返回 ok=false；memo 缓存减少重复查询
func get115RelPath(cookie, cid, rootCid string, memo map[string]dirInfo) (string, bool, error) {
	if cid == rootCid {
		return "", true, nil
	}
	var parts []string
	cur := cid
	for i := 0; i < 64; i++ { // 深度保险
		info, ok := memo[cur]
		if !ok {
			var err error
			info, err = get115DirInfo(cookie, cur)
			if err != nil {
				return "", false, err
			}
			memo[cur] = info
		}
		if cur == rootCid {
			// 逆序拼接：parts 是从受影响目录向上收集的
			for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
				parts[l], parts[r] = parts[r], parts[l]
			}
			return path.Join(parts...), true, nil
		}
		parts = append(parts, info.n)
		if info.pid == "" || info.pid == "0" {
			return "", false, nil // 到达网盘根，不在媒体库内
		}
		if info.pid == rootCid {
			for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
				parts[l], parts[r] = parts[r], parts[l]
			}
			return path.Join(parts...), true, nil
		}
		cur = info.pid
	}
	return "", false, nil
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
	EventsTotal      int    `json:"events_total"`
	EventsFresh      int    `json:"events_fresh"`
	Relevant         int    `json:"relevant"`
	Structural       int    `json:"structural"`
	Deleted          int    `json:"deleted"`
	Moved            int    `json:"moved"`
	Dirs             int    `json:"dirs"`
	DirsSkipped      int    `json:"dirs_skipped"`
	Videos           int    `json:"videos"`
	StrmCreated      int    `json:"strm_created"`
	AssetsTotal      int    `json:"assets_total"`
	AssetsDownloaded int    `json:"assets_downloaded"`
	AssetsSkipped    int    `json:"assets_skipped"`
	AssetsFailed     int    `json:"assets_failed"`
	Ignored          int    `json:"ignored"` // 非媒体库区域（待整理/已存在/冗余等）的事件
	Elapsed          string `json:"elapsed"`
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
	p := normalizeIncrParams(req.Cid, req.LocalPath, req.VideoExt, req.ImageExt, req.DataExt, req.Limit)

	if !fullSyncMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "任务正在进行中，请等待完成后再试"})
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("增量同步")
	defer endTask()

	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "增量同步完成", "summary": sum})
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

// executeIncrementalSync 增量同步核心（CMS 两阶段模式）：
// 阶段一：小批量分页拉取生活事件并落库去重（SyncEvent 表，事件 id 唯一，永不丢失）
// 阶段二：按时间正序应用事件——新增类定向重遍历受影响目录；
//
//	move/rename/delete 基于本地文件台账（SyncedFile）精确执行
func (h *Handler) executeIncrementalSync(p incrParams) (*incrSummary, error) {
	defer SetTaskProgress("") // 结束清进度（含错误路径）
	sum := &incrSummary{}
	cookie, err := h.get115Cookie()
	if err != nil {
		return sum, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return sum, err
	}

	incrStart := time.Now()

	// ---- 作用域计算与配置体检（先于事件拉取：配置错误时熔断，不消费任何事件）----
	memo := map[string]dirInfo{}
	// 媒体库根目录名（STRM 路径第一层）
	libName := ""
	if info, err := get115DirInfo(cookie, p.Cid); err == nil {
		libName = info.n
	}
	libAbs := absPathOf(cookie, p.Cid, memo)
	if libAbs == "" {
		// 媒体库 cid 无效/未配置（如全量同步配置缺 cid 时默认 "0"）：
		// 所有事件都会被判为 other 静默吞掉并标已消费 → STRM 永久缺失。
		// 熔断本轮，事件原样留待配置修正
		log.Printf("[同步] ⚠⚠ 媒体库 cid=%s 解析不出绝对路径（未配置或已失效），增量同步中止（事件未消费）。请到「全量同步」确认媒体库目录配置", p.Cid)
		return sum, fmt.Errorf("媒体库 cid 无效（%s），增量同步中止（事件未消费，修正配置后重试即可补上）", p.Cid)
	}
	var excludedAbs []string
	var orgCfgRaw struct {
		Pending   string `json:"pending"`
		Existing  string `json:"existing"`
		Redundant string `json:"redundant"`
	}
	if err := json.Unmarshal([]byte(h.getSettingValue("org-basic")), &orgCfgRaw); err != nil {
		log.Printf("[同步] ○ 整理配置解析失败（使用默认值）: %v", err)
	}
	var shareCfgRaw struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareCfgRaw)
	for _, cid := range []string{orgCfgRaw.Pending, orgCfgRaw.Existing, orgCfgRaw.Redundant, shareCfgRaw.Folder} {
		if cid != "" {
			if a := absPathOf(cookie, cid, memo); a != "" {
				excludedAbs = append(excludedAbs, strings.TrimSuffix(a, "/"))
			}
		}
	}
	// 熔断体检：工作区（待整理/已存在/冗余/转存）若覆盖了整个媒体库
	//（如待整理选在库根或库的上层），排除区判定会吞掉所有库内事件。
	// 直接中止本次增量——事件一条都不拉取消费，修正配置后原样补上
	for _, ex := range excludedAbs {
		if libAbs != "" && strings.HasPrefix(strings.TrimSuffix(libAbs, "/")+"/", ex+"/") {
			log.Printf("[同步] ⚠ 整理目录（%s）把整个媒体库都包含进去了，这样会误删文件，增量同步已暂停。请到设置里把待整理/已存在/冗余目录改到媒体库外面", ex)
			return sum, fmt.Errorf("配置错误：工作区目录 %s 覆盖了整个媒体库 %s，增量同步中止（事件未消费，修正配置后重试即可补上）", ex, libAbs)
		}
	}

	// 沉淀延迟：等上游转存/移动操作完成，避免拿到中间状态（CMS 同款）
	SetTaskProgress("正在获取网盘最近的改动…")
	time.Sleep(3 * time.Second)

	// ---- 阶段一：小批量分页拉取，落库去重，直到追平（本页无新事件）----
	// 拉取失败重试：30 秒 × 3 次（网络抖动/瞬时风控不应让整轮作废，QMediaSync 同款）
	fetchWithRetry := func(offset int) ([]lifeEvent, error) {
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			evs, err := fetch115LifeEvents(cookie, 30, offset, "")
			if err == nil {
				return evs, nil
			}
			lastErr = err
			log.Printf("[同步] 事件拉取失败（第 %d/3 次）: %v", attempt, err)
			if attempt < 3 {
				time.Sleep(30 * time.Second)
			}
		}
		return nil, lastErr
	}
	var pending []model.SyncEvent
	pickByEvent := map[string]string{} // 事件 id → pick_code（落库结构不含，本轮内存携带）
	offset := 0
	for {
		events, err := fetchWithRetry(offset)
		if err != nil {
			return sum, fmt.Errorf("拉取生活事件失败（已重试 3 次）: %w", err)
		}
		sum.EventsTotal += len(events)
		fresh := 0
		batch := make([]model.SyncEvent, 0, len(events))
		for _, ev := range events {
			if ev.ID == "" {
				continue
			}
			ts, _ := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64)
			batch = append(batch, model.SyncEvent{
				EventID: ev.ID, Type: ev.Type, FileID: ev.FileID,
				FileName: ev.FileName, Cid: ev.Cid, Size: ev.Size, EventTime: ts,
			})
			if ev.PickCode != "" {
				pickByEvent[ev.ID] = ev.PickCode // 消费端按新事件 id 取，多记无害
			}
		}
		// 批量落库（此前逐条 Create：千级事件即千次独立写事务）。
		// 新事件判定改为预查已存在的 event_id（批量插入拿不到单条 RowsAffected）
		if n := insertSyncEvents(h.DB, batch); n > 0 {
			fresh += n
			dupInBatch := map[string]bool{}
			for _, se := range batch {
				if !dupInBatch[se.EventID] {
					dupInBatch[se.EventID] = true
					pending = append(pending, se)
				}
			}
		}
		batch = batch[:0]
		sum.EventsFresh += fresh
		if fresh > 0 {
		}
		if fresh == 0 || sum.EventsFresh >= p.Limit {
			break // 已追平或达到单次上限
		}
		offset += len(events)
		if len(events) < 30 {
			break
		}
	}

	// 恢复上轮中断遗留的 pending 事件：此前只处理"本轮新插入"的行，
	// 拉取中途失败/进程重启后已落库的事件永久滞留 pending，无人再消费
	var stale []model.SyncEvent
	h.DB.Where("status = ?", "pending").Order("event_time").Find(&stale)
	if len(stale) > 0 {
		pending = append(stale, pending...)
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
		abs := absPathOf(cookie, cid, memo)
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

	dirSet := map[string]bool{}
	// 零遍历清单：事件自带 pick_code 时直接用事件数据生成 strm，
	// 不再重遍历受影响目录（CMS 同款；无 pick_code 的事件回退 dirSet 遍历）
	type preciseFile struct {
		ev model.SyncEvent
	}
	var precise []preciseFile
	fallbackDir := func(cid string) { dirSet[cid] = true }

	for i, ev := range pending {
		if i%50 == 0 {
			SetTaskProgress(fmt.Sprintf("处理网盘变化 %d/%d 条…", i+1, len(pending)))
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
				if pickByEvent[ev.EventID] != "" && ev.Cid != "" && ev.FileID != "" {
					precise = append(precise, preciseFile{ev: ev})
				} else {
					fallbackDir(ev.Cid)
				}
			} else if ev.FileID != "" && ev.Cid == "" {
				fallbackDir(ev.FileID)
				sum.Relevant++
			}
		case evNewFolder, evCopyFolder:
			// 作用域过滤：冗余/已存在等目录新增不监控
			if sc := scopeOf(ev.Cid); sc == "excluded" || sc == "other" {
				sum.Ignored++
				sum.Structural++
				continue
			}
			// 目录新增/复制（含整目录转存）：按目录自身加入受影响集合
			if ev.FileID != "" {
				dirSet[ev.FileID] = true
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
				if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, false, false) {
					sum.Deleted++
				}
			default: // unknown（cid=0 等）：仅按台账名称匹配，静默处理
				if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, true, false) {
					sum.Deleted++
				} else {
					sum.Ignored++
				}
			}
			sum.Structural++
		case evMove, evMoveImage, evRename:
			// 移动/改名：清理旧位置只按台账精确匹配（事件的 Cid/FileName 均为
			// 新位置信息，模糊删除会误删库内同名字幕树），新位置精确重建或回退遍历
			if h.removeSyncedItem(ev, cookie, p.Cid, p.LocalPath, memo, true, true) {
				sum.Moved++
			}
			if ev.Cid != "" && scopeOf(ev.Cid) == "library" {
				if pickByEvent[ev.EventID] != "" && ev.FileID != "" && isMedia(ev.FileName) {
					precise = append(precise, preciseFile{ev: ev}) // 移入媒体库：事件直推重建
				} else {
					fallbackDir(ev.Cid)
				}
			}
			sum.Structural++
		case evFolderRename:
			// 目录改名：目录结构已变，路径缓存整体失效
			invalidateDirAbsCache()
			// 重遍历父目录重建；旧名子树可能残留，交由后续清理功能
			if ev.Cid != "" {
				if sc := scopeOf(ev.Cid); sc == "excluded" || sc == "other" {
					sum.Ignored++
					sum.Structural++
					continue
				}
				dirSet[ev.Cid] = true
			}
			sum.Structural++
		default:
			sum.Structural++
			vlog("[同步] ○ 未处理的事件: 类型=%s 文件=%s", ev.Type, ev.FileName)
		}
	}

	// 本轮受影响的最浅目录（Emby 定向刷新用，传库根=全刷）；
	// 零遍历直推与目录遍历两条路径共同维护
	shallowest := ""
	noteShallow := func(base string) {
		if shallowest == "" || len(base) < len(shallowest) {
			shallowest = base
		}
	}

	// ---- 零遍历落盘：事件自带 pick_code 的精确处理（无目录遍历） ----
	{
		domain, format, keepExt, skipExist := h.getStrmConfig()
		for _, pf := range precise {
			ev := pf.ev
			base, ok, err := get115RelPath(cookie, ev.Cid, p.Cid, memo)
			if err != nil || !ok {
				fallbackDir(ev.Cid) // 路径推导失败：回退目录遍历
				continue
			}
			rel := path.Join(libName, base, ev.FileName)
			f := remoteFile{
				Fid:      ev.FileID,
				Name:     ev.FileName,
				Path:     path.Join(libName, base),
				Size:     ev.Size,
				PickCode: pickByEvent[ev.EventID],
			}
			ext := strings.ToLower(path.Ext(ev.FileName))
			switch {
			case filter.videoExts[ext]:
				if err := writeStrm(p.LocalPath, domain, format, keepExt, skipExist, f); err != nil {
					log.Printf("[同步] 零遍历 strm 失败 %s: %v", rel, err)
					fallbackDir(ev.Cid)
					continue
				}
				upsertSyncedFile(h.DB, f, rel+".strm", "video")
				sum.StrmCreated++
				sum.Videos++
				noteShallow(path.Join(libName, base))
			case filter.assetExts[ext]:
				dst := filepath.Join(p.LocalPath, filepath.FromSlash(rel))
				if _, serr := os.Stat(dst); serr == nil {
					upsertSyncedFile(h.DB, f, rel, "asset")
					sum.AssetsSkipped++
				} else if u, hdrs, uerr := ops.downloadURLFull(f.PickCode, ""); uerr == nil {
					if data, derr := downloadAssetBytes(u, hdrs, ops.cookieForDL()); derr == nil {
						if _, werr := writeAssetBytes(f, p.LocalPath, data); werr == nil {
							upsertSyncedFile(h.DB, f, rel, "asset")
							sum.AssetsDownloaded++
						} else {
							sum.AssetsFailed++
						}
					} else {
						sum.AssetsFailed++
					}
				} else {
					sum.AssetsFailed++
				}
				noteShallow(path.Join(libName, base))
			}
		}
		if len(precise) > 0 {
			vlog("[同步] 零遍历模式: 事件直推 %d 个文件（回退目录遍历 %d 个）", len(precise), len(dirSet))
		}
	}

	// 受影响目录：定位相对路径 + 祖先去重
	type targetDir struct{ cid, base string }
	var targets []targetDir
	for cid := range dirSet {
		base, ok, err := get115RelPath(cookie, cid, p.Cid, memo)
		if err != nil {
			// 目录已不存在（800001）：目录被删除后残留的定位请求是永久性失败，
			// 重试永远不会成功、还会让水位永远不推进。按"已解决"跳过
			if strings.Contains(err.Error(), "目录不存在") || strings.Contains(err.Error(), "800001") {
				log.Printf("[同步] 网盘目录已被删除，跳过相关变化")
				continue
			}
			log.Printf("[同步] 暂时无法获取网盘目录位置（cid=%s）: %v", cid, err)
			sum.DirsSkipped++
			continue
		}
		if !ok {
			sum.DirsSkipped++ // 不在媒体库路径下
			continue
		}
		targets = append(targets, targetDir{cid: cid, base: base})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].base < targets[j].base })
	var uniqTargets []targetDir
	dirsMerged := 0
	for _, t := range targets {
		covered := false
		for _, u := range uniqTargets {
			if u.base == "" || t.base == u.base || strings.HasPrefix(t.base, u.base+"/") {
				covered = true
				break
			}
		}
		if covered {
			dirsMerged++ // 子目录被上层目录的遍历覆盖，无需单独处理
			continue
		}
		uniqTargets = append(uniqTargets, t)
	}

	// 逐目录遍历并立即落盘
	domain, format, keepExt, skipExist := h.getStrmConfig()
	for _, t := range uniqTargets {
		noteShallow(t.base)
		var videos, assets []remoteFile
		if err := walk115Dir(ops, t.cid, path.Join(libName, t.base), &videos, &assets, filter, nil); err != nil {
			log.Printf("[同步] 遍历目录失败 %s: %v，30 秒后重试一次", t.base, err)
			time.Sleep(30 * time.Second)
			if err := walk115Dir(ops, t.cid, path.Join(libName, t.base), &videos, &assets, filter, nil); err != nil {
				log.Printf("[同步] 遍历目录重试仍失败 %s: %v，跳过", t.base, err)
				sum.DirsSkipped++
				continue
			}
		}
		sc, dl, sk, fl := applySyncResults(h.DB, ops, videos, assets, p.LocalPath, domain, format, keepExt, skipExist, t.base)
		sum.Dirs++
		sum.Videos += len(videos)
		sum.StrmCreated += sc
		sum.AssetsTotal += len(assets)
		sum.AssetsDownloaded += dl
		sum.AssetsSkipped += sk
		sum.AssetsFailed += fl
		log.Printf("[同步] %s：新增视频 %d 个，附属文件下载 %d 个", t.base, len(videos), dl)
	}

	SetTaskProgress(fmt.Sprintf("收尾：目录 %d，STRM %d", sum.Dirs, sum.StrmCreated))
	// 标记事件已应用 + 更新水位。
	// 有目录遍历重试后仍失败（DirsSkipped>0）时绝不标记：被标记的事件永久
	// 不再处理，对应 STRM 就永久缺失了。整轮不消费（下轮全量重做——
	// STRM 写入是 upsert、删除幂等，重复处理无副作用，正确性优先）
	if sum.DirsSkipped > 0 {
		log.Printf("[同步] ⚠ 有 %d 个网盘目录暂时读取失败，下轮会自动重试这些内容", sum.DirsSkipped)
		return sum, nil
	}
	now := time.Now()
	ids := make([]string, 0, len(pending))
	for _, ev := range pending {
		ids = append(ids, ev.EventID)
	}
	if len(ids) > 0 {
		h.DB.Model(&model.SyncEvent{}).Where("event_id IN ?", ids).
			Updates(map[string]interface{}{"status": "applied", "applied_at": now})
	}
	h.Config.SaveSetting("incr-last", fmt.Sprint(now.Unix()))

	if sum.StrmCreated+sum.AssetsDownloaded+sum.Deleted+sum.Moved > 0 {
		// 定向刷新：传本轮受影响的最浅子目录（传库根会命中所有库=全刷）
		refreshBase := p.LocalPath
		if shallowest != "" {
			refreshBase = filepath.Join(p.LocalPath, filepath.FromSlash(shallowest))
		}
		h.notifyEmbyRefresh(refreshBase)
	}
	sum.Elapsed = time.Since(incrStart).Truncate(time.Second).String()

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
		log.Printf("[同步] ✓ 增量同步完成：%s%s。用时 %s", detail, ignoredNote, sum.Elapsed)
	}
	// 整理后自动刮削：本轮真的动了媒体库才触发（开关在影视刮削配置里）
	if len(parts) > 0 {
		h.scrapeAutoTrigger()
	}
	return sum, nil
}

// dirAbsCache cid→绝对路径缓存：守卫/作用域每轮都要爬目录链
// （每层一次 API × 3 秒限流），冷启动一趟就是十来秒。TTL 10 分钟；
// 目录改名/移动事件发生时整体失效（见 invalidateDirAbsCache）
var (
	dirAbsMu    sync.Mutex
	dirAbsCache = map[string]dirAbsEntry{}
)

type dirAbsEntry struct {
	path string
	at   time.Time
}

// invalidateDirAbsCache 目录结构变化后调用（目录改名/移动），清空缓存
func invalidateDirAbsCache() {
	dirAbsMu.Lock()
	dirAbsCache = map[string]dirAbsEntry{}
	dirAbsMu.Unlock()
}

// absPathOf 爬到网盘根，返回目录绝对路径（如 /整理/已存在），失败返回空。
// 结果进 dirAbsCache（命中免爬链）
func absPathOf(cookie, cid string, memo map[string]dirInfo) string {
	if cid == "" || cid == "0" {
		return ""
	}
	dirAbsMu.Lock()
	if e, ok := dirAbsCache[cid]; ok && time.Since(e.at) < 10*time.Minute {
		p := e.path
		dirAbsMu.Unlock()
		return p
	}
	dirAbsMu.Unlock()
	var parts []string
	cur := cid
	for i := 0; i < 64; i++ {
		info, ok := memo[cur]
		if !ok {
			var err error
			info, err = get115DirInfo(cookie, cur)
			if err != nil {
				return ""
			}
			memo[cur] = info
		}
		parts = append(parts, info.n)
		if info.pid == "" || info.pid == "0" {
			break
		}
		cur = info.pid
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	result := "/" + strings.Join(parts, "/")
	dirAbsMu.Lock()
	dirAbsCache[cid] = dirAbsEntry{path: result, at: time.Now()}
	dirAbsMu.Unlock()
	return result
}

// removeSyncedFile 按文件 id 从台账定位并删除本地文件（仅删除本工具生成过的文件）
func (h *Handler) removeSyncedFile(fileID, localRoot string) bool {
	if fileID == "" {
		return false
	}
	var sf model.SyncedFile
	if err := h.DB.Where("file_id = ?", fileID).First(&sf).Error; err != nil {
		return false // 台账无记录（从未同步过），无需处理
	}
	full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		vlog("[同步] 清理失败 %s: %v", full, err)
		return false
	}
	h.DB.Delete(&sf)
	vlog("[同步] 已清理: %s", sf.RelPath)
	return true
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
func (h *Handler) removeSyncedItem(ev model.SyncEvent, cookie, rootCid, localRoot string, memo map[string]dirInfo, quiet, ledgerOnly bool) bool {
	// 1) 台账精确匹配
	if ev.FileID != "" && h.removeSyncedFile(ev.FileID, localRoot) {
		return true
	}
	if ledgerOnly {
		if !quiet {
			log.Printf("[同步] ○ 移动/改名无台账记录，跳过清理: %s（file_id=%s）", ev.FileName, ev.FileID)
		}
		return false
	}
	// 2) 路径推导
	if ev.Cid != "" && ev.FileName != "" {
		if base, ok, err := get115RelPath(cookie, ev.Cid, rootCid, memo); err == nil && ok {
			rel := path.Join(base, ev.FileName)
			local := filepath.Join(localRoot, filepath.FromSlash(rel))
			// 目录：整树删除（strm/附属全在树内），并清理台账
			if st, err := os.Stat(local); err == nil && st.IsDir() {
				if err := os.RemoveAll(local); err != nil {
					log.Printf("[同步] 删除本地目录失败 %s: %v", rel, err)
					return false
				}
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", rel, rel+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] 目录删除-执行成功: %s", rel)
				return true
			}
			// 文件：strm 与附属实体两种形态
			for _, cand := range []struct{ rel, suffix string }{{rel, ".strm"}, {rel, ""}} {
				full := filepath.Join(localRoot, filepath.FromSlash(cand.rel)) + cand.suffix
				if _, err := os.Stat(full); err == nil {
					if err := os.Remove(full); err != nil {
						log.Printf("[同步] 删除本地文件失败 %s: %v", cand.rel+cand.suffix, err)
						return false
					}
					h.DB.Where("rel_path = ?", cand.rel+cand.suffix).Delete(&model.SyncedFile{})
					vlog("[同步] 已清理: %s", cand.rel+cand.suffix)
					return true
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
				return true
			}
		}
		// 目录事件：台账中出现过该名称路径段的，按最浅前缀整树删除
		var segs []model.SyncedFile
		h.DB.Where("rel_path LIKE ? OR rel_path LIKE ?", "%/"+ev.FileName+"/%", "%/"+ev.FileName).Limit(50).Find(&segs)
		bestPrefix := ""
		for _, sf := range segs {
			parts := strings.Split(sf.RelPath, "/")
			for i, part := range parts {
				if part == ev.FileName {
					prefix := strings.Join(parts[:i+1], "/")
					if bestPrefix == "" || len(prefix) < len(bestPrefix) {
						bestPrefix = prefix
					}
					break
				}
			}
		}
		if bestPrefix != "" {
			full := filepath.Join(localRoot, filepath.FromSlash(bestPrefix))
			if err := os.RemoveAll(full); err == nil {
				h.DB.Where("rel_path = ? OR rel_path LIKE ?", bestPrefix, bestPrefix+"/%").Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地目录已清理: %s", bestPrefix)
				return true
			}
		}
	}
	// 4) 本地磁盘按名搜索兜底（父目录与台账均不可用时）：
	//    目录精确名匹配取最浅层整树删除；文件匹配 实体/strm 两种形态
	if ev.FileName != "" {
		var hitDir, hitFile string
		filepath.WalkDir(localRoot, func(p string, d os.DirEntry, err error) error {
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
				return true
			}
		}
		if hitFile != "" {
			if err := os.Remove(hitFile); err == nil {
				rel, _ := filepath.Rel(localRoot, hitFile)
				h.DB.Where("rel_path = ?", filepath.ToSlash(rel)).Delete(&model.SyncedFile{})
				log.Printf("[同步] ✓ 本地文件已清理: %s", rel)
				return true
			}
		}
	}
	if !quiet {
		log.Printf("[同步] ○ 本地未找到对应文件: %s", ev.FileName)
	}
	return false
}

// insertSyncEvents 批量插入生活事件（OnConflict DoNothing），返回新插入条数
func insertSyncEvents(db *gorm.DB, batch []model.SyncEvent) int {
	if db == nil || len(batch) == 0 {
		return 0
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
		return 0
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&fresh, 200).Error; err != nil {
		return 0
	}
	return len(fresh)
}
