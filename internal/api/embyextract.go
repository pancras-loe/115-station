package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

// ==================== Emby 提前探测（入库后让 Emby 把媒体信息提取好）====================
//
// STRM 第一次播放慢，一半慢在 Emby 手里还没有这个文件的轨道信息，要在 PlaybackInfo 里
// 现场 ffprobe 一遍远端文件（经本站 302 到 115 CDN，常常好几秒）。提取过一次 Emby 就存进
// 自己的库，之后再播不再探测。所以入库后替用户先调一次 PlaybackInfo，第一次播放就和第二次一样快
// （另一半慢在取直链，详情页预取已经做了，见 embyproxy.go）。
//
// 此前本站自己 ffprobe、把轨道写进 NFO 的 streamdetails —— Emby / Jellyfin 导入 NFO 不读那一段，
// 播放时照样自己探测、还把 NFO 整份重写（2026-09-29 维护者现场对照两份 NFO 确认），已删除。
//
// 做法对照：
//   - LitePan embyproxy/media_info.go「补全媒体信息」：列出缺媒体信息（视频 + 音轨不足两条）的
//     Movie / Episode / Video，逐个 POST /Items/{id}/PlaybackInfo；ISO / BDMV 跳过
//   - qmediasync controllers/emby.go（enable_extract_media_info）：收到 library.new 后对电影 / 单集调一次
//     PlaybackInfo。它的更新说明提醒过并发开多了会把 115 请求占满、别的任务全卡住 —— 所以这里一次只探一个
//
// 开关复用影视刮削的「轨道探测」（scrape.probe_streams / 刮削任务的 Probe）。两个入口：
//   - 入库确认之后（embyVerifyIngest 查到条目的那一刻）：全局开关，整理 / 增量 / 全量进来的都算
//   - 本地文件页手动刮削结束时（execScrapeJob）：这一次任务的开关，对刮到的片目补探；
//     所选视频超过 100 个时要用户确认两次（ScrapeLocalTitles）
// 已经有媒体信息的条目一律不碰：每次探测都是一次 115 直链请求
//
// ⚠️ 防重复探测（2026-09-29 维护者明确要求：重复探测等于重复取 115 直链，有风控风险）。
// 同一个条目会从好几条路进队列：整理后刮削结束一次、入库确认一次（整理刷新一次、增量再刷一次就是两次），
// 片目目录与单集 .strm 又是两条不同的路径。只靠「已有媒体信息就跳过」挡不住失败 / 超时的条目 ——
// 它们每来一次就会再探一次。所以另有三道闸：
//  1. 按 Emby 条目 id 记账（EmbyExtractMark，落库，重启不丢）：发请求之前先记一次尝试，
//     同一条目最多试 embyExtractMaxAttempts 次，两次之间至少隔 embyExtractRetryAfter，成功即删账
//  2. 连续 embyExtractBreakAfter 个条目失败（含超时）就整体暂停 embyExtractBreakPause ——
//     多半是 115 风控或 Emby 出了问题，再探只会火上浇油
//  3. 整理后自动刮削不再排队（入库确认那条入口已经覆盖了它的片目），见 execScrapeJob

// embyExtractGap 两次探测之间的间隔。每次探测都会经 302 取一次 115 直链，和整理、同步共用风控额度
var embyExtractGap = 3 * time.Second // var：测试里调短

// embyExtractTimeout 一次 PlaybackInfo 最多等多久。远端 STRM 的 ffprobe 慢的时候要几十秒，
// 超时也记一次尝试、计入连续失败：Emby 那边多半还在提取，马上再发等于同一个文件被探两遍
const embyExtractTimeout = 2 * time.Minute

// embyExtractMaxAttempts 同一个 Emby 条目最多请求几次探测（含超时）；embyExtractRetryAfter 两次之间的最短间隔。
// 超时算一次：Emby 那边多半还在探，马上再发等于同一个文件被探两遍
const (
	embyExtractMaxAttempts = 2
	embyExtractRetryAfter  = 24 * time.Hour
)

// 连续失败熔断
const embyExtractBreakAfter = 3

var embyExtractBreakPause = 30 * time.Minute // var：测试里调短

// embyExtractQueueMax 排队上限。积压太多说明 Emby 那边出了问题（或一次勾了整库刮削），
// 超出的丢掉（下次刮削还会再排），免得内存里挂着一条永远跑不完的队列
const embyExtractQueueMax = 5000

// embyExtractEntry 队列里的一条：一个 Emby 路径（片目目录或单个 .strm）+ 排它进来的任务
// （手动刮削；入库确认等没有任务的为空）。同一路径被几个任务排过就挂几个，结果逐个写回（embyprobereport.go）
type embyExtractEntry struct {
	path string
	jobs []uint
}

var embyExtractQ = struct {
	mu     sync.Mutex
	queue  []*embyExtractEntry          // 先进先出
	queued map[string]*embyExtractEntry // 排着的路径，去重用
	wake   chan struct{}
	once   sync.Once
}{queued: map[string]*embyExtractEntry{}, wake: make(chan struct{}, 1)}

// embyExtractEnabled 入库确认后要不要提前探测（全局开关：影视刮削「轨道探测」）
func embyExtractEnabled() bool { return loadScrapeCfg().ProbeStreams }

// queueEmbyExtract 把一批 Emby 路径排进提前探测队列（去重），worker 第一次用到时才起
func queueEmbyExtract(embyPaths ...string) { queueEmbyExtractFor(0, embyPaths...) }

// queueEmbyExtractFor 同上，探测结果写回任务 jobID（0 = 不写回）。
// 路径已经在排的，把任务挂到那一条上，不另排
func queueEmbyExtractFor(jobID uint, embyPaths ...string) {
	q := &embyExtractQ
	q.mu.Lock()
	added, forJob, dropped := 0, 0, 0
	for _, p := range embyPaths {
		if p == "" {
			continue
		}
		if e := q.queued[p]; e != nil {
			if jobID != 0 && !containsUint(e.jobs, jobID) {
				e.jobs = append(e.jobs, jobID)
				forJob++
			}
			continue
		}
		if len(q.queue) >= embyExtractQueueMax {
			dropped++
			continue
		}
		e := &embyExtractEntry{path: p}
		if jobID != 0 {
			e.jobs = []uint{jobID}
			forJob++
		}
		q.queued[p] = e
		q.queue = append(q.queue, e)
		added++
	}
	// 路径数在解锁前记上：否则 worker 可能先探完一条，报告里「已处理」比「总数」还多
	if forJob > 0 {
		probeReportQueued(jobID, forJob)
	}
	q.mu.Unlock()
	if dropped > 0 {
		log.Printf("[Emby探测] ⚠ 队列已满（%d），丢弃 %d 个路径 —— 下次入库 / 刮削时会再排", embyExtractQueueMax, dropped)
		if jobID != 0 {
			probeReportQueued(jobID, 1)
			probeReportItem([]uint{jobID}, jobProbeItem{Kind: probeItemError,
				Err: fmt.Sprintf("探测队列已满，%d 个片目没排进去（下次刮削会再排）", dropped)})
			probeReportPathDone([]uint{jobID})
		}
	}
	if added == 0 {
		return
	}
	q.once.Do(func() { go embyExtractWorker() })
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// embyExtractRunning 正在处理的路径与正在探测的 Emby 条目 id（片目详情显示「排队中 / 探测中」用）。
// 路径出队后要逐个探完底下的条目才算完，这期间它的其余条目仍算排队中。
// jobs / label 给任务中心：哪些任务的探测正在进行、正在探哪一集
var embyExtractRunning struct {
	sync.Mutex
	path, id, label string
	jobs            []uint
}

func setEmbyExtractRunning(id, label string) {
	embyExtractRunning.Lock()
	embyExtractRunning.id, embyExtractRunning.label = id, label
	embyExtractRunning.Unlock()
}

func setEmbyExtractRunningPath(p string, jobs []uint) {
	embyExtractRunning.Lock()
	embyExtractRunning.path, embyExtractRunning.jobs = p, jobs
	embyExtractRunning.Unlock()
}

func embyExtractRunningLabel() string {
	embyExtractRunning.Lock()
	defer embyExtractRunning.Unlock()
	return embyExtractRunning.label
}

func containsUint(xs []uint, x uint) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// embyExtractJobState 任务的探测还在不在进行：running 正在探它的路径、queued 还有路径在排、
// "" 都不在了（探完了，或者服务重启过、内存里的队列没了）
func embyExtractJobState(jobID uint) string {
	embyExtractRunning.Lock()
	running := containsUint(embyExtractRunning.jobs, jobID)
	embyExtractRunning.Unlock()
	if running {
		return "running"
	}
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.queue {
		if containsUint(e.jobs, jobID) {
			return "queued"
		}
	}
	return ""
}

// embyExtractQueueLen 排着的路径数（任务中心显示）
func embyExtractQueueLen() int {
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue)
}

func embyExtractRunningID() string {
	embyExtractRunning.Lock()
	defer embyExtractRunning.Unlock()
	return embyExtractRunning.id
}

// embyExtractQueuedFor 队列里有没有与这条路径相关的（同一路径、它的上级或下级）。
// 队列存的是路径不是条目：片目目录排进来时，底下每一集都算「排队中」
func embyExtractQueuedFor(embyPath string) bool {
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/") }
	want := norm(embyPath)
	if want == "" {
		return false
	}
	embyExtractRunning.Lock()
	cur := embyExtractRunning.path
	embyExtractRunning.Unlock()
	if cur != "" && embyPathRelated(norm(cur), want) {
		return true
	}
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.queue {
		if embyPathRelated(norm(e.path), want) {
			return true
		}
	}
	return false
}

func embyExtractPop() (*embyExtractEntry, bool) {
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return nil, false
	}
	e := q.queue[0]
	q.queue = q.queue[1:]
	delete(q.queued, e.path)
	return e, true
}

// embyExtractWorker 串行消费队列：一次只探一个条目，条目之间隔 embyExtractGap
func embyExtractWorker() {
	for {
		e, ok := embyExtractPop()
		if !ok {
			select {
			case <-stopCh:
				return
			case <-embyExtractQ.wake:
			}
			continue
		}
		cfg, ok := loadEmbyRefreshCfg()
		if !ok {
			probeReportItem(e.jobs, jobProbeItem{Label: embyPathBase(e.path), Kind: probeItemError, Err: "没有配置 Emby"})
			probeReportPathDone(e.jobs)
			continue
		}
		if !embyExtractPath(cfg, e.path, e.jobs...) {
			return
		}
	}
}

// embyExtractPath 探测一条路径下缺媒体信息的条目；返回 false 表示服务要退出了。
// 单条出错（含 panic）只丢这一条，worker 不能死：它只在第一次排队时拉起。
// jobs 是排它进来的任务：每个条目的结果（成功 / 失败 / 近期请求过 / 还没入库）写回它们
func embyExtractPath(cfg embyRefreshCfg, p string, jobs ...uint) (alive bool) {
	alive = true
	setEmbyExtractRunningPath(p, jobs)
	defer setEmbyExtractRunningPath("", nil)
	// 先于 recover 注册、因此在它之后执行：panic 了这条路径也算处理完，报告不会一直挂在「探测中」
	defer probeReportPathDone(jobs)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Emby探测] ✗ 处理 %s 异常: %v", p, r)
			probeReportItem(jobs, jobProbeItem{Label: embyPathBase(p), Kind: probeItemError, Err: fmt.Sprintf("处理异常: %v", r)})
		}
	}()
	items, found, err := embyExtractTargets(cfg, p)
	if err != nil {
		// 以前只进详细日志：用户只看到「已排进提前探测」，之后再没下文
		log.Printf("[Emby探测] ✗ 查 %s 的条目失败: %v", p, err)
		probeReportItem(jobs, jobProbeItem{Label: embyPathBase(p), Kind: probeItemError, Err: "查询 Emby 条目失败: " + err.Error()})
		return
	}
	if !found {
		probeReportItem(jobs, jobProbeItem{Label: embyPathBase(p), Kind: probeItemMissing,
			Err: "Emby 里查不到这个片目（还没扫描入库、路径映射不对，或 Emby 连不上）；入库确认后会自动再排"})
		return
	}
	var todo []embyExtractItem
	held := 0
	for _, it := range items {
		if ok, why := embyExtractAllowed(it.ID, time.Now()); !ok {
			held++
			vlog("[Emby探测] 跳过 %s：%s", it.label(), why)
			probeReportItem(jobs, probeHeldItem(it))
			continue
		}
		todo = append(todo, it)
	}
	if len(todo) == 0 {
		if held > 0 {
			vlog("[Emby探测] %s：%d 个条目近期已请求过探测或已达次数上限，不重复探测", embyPathBase(p), held)
		}
		return
	}
	note := ""
	if held > 0 {
		note = fmt.Sprintf("（另 %d 个近期已请求过，不重复）", held)
	}
	log.Printf("[Emby探测] ▶ %s：%d 个条目还没有媒体信息，逐个提前探测%s", embyPathBase(p), len(todo), note)
	for _, it := range todo {
		// 记账在发请求之前：请求发出去就算一次，哪怕随后超时、进程被杀
		if ok, _ := embyExtractClaim(it.ID, it.label(), time.Now()); !ok {
			probeReportItem(jobs, probeHeldItem(it))
			continue
		}
		setEmbyExtractRunning(it.ID, it.label())
		ok, errMsg := embyExtractOne(cfg, it)
		setEmbyExtractRunning("", "")
		embyExtractSettle(it.ID, ok, errMsg)
		if ok {
			probeReportOK(jobs)
		} else {
			item := probeHeldItem(it) // 记账刚写过：带上失败原因与下次什么时候能再试
			item.Kind = probeItemFailed
			probeReportItem(jobs, item)
		}
		pause := embyExtractGap
		if ok {
			embyExtractFails = 0
		} else if embyExtractFails++; embyExtractFails >= embyExtractBreakAfter {
			log.Printf("[Emby探测] ⚠ 连续 %d 个条目探测失败，暂停 %s（可能是 115 风控或 Emby 异常），排队中的稍后继续",
				embyExtractFails, embyExtractBreakPause)
			embyExtractFails = 0
			pause = embyExtractBreakPause
			setEmbyExtractPausedUntil(time.Now().Add(pause))
		}
		select {
		case <-stopCh:
			return false
		case <-time.After(pause):
		}
		setEmbyExtractPausedUntil(time.Time{})
	}
	return
}

// embyExtractFails 连续失败的条目数（只有 worker 一个 goroutine 读写）
var embyExtractFails int

// embyExtractPaused 熔断暂停到什么时候（零值 = 没暂停）：任务中心显示「暂停至 HH:MM」
var embyExtractPaused struct {
	sync.Mutex
	until time.Time
}

func setEmbyExtractPausedUntil(t time.Time) {
	embyExtractPaused.Lock()
	embyExtractPaused.until = t
	embyExtractPaused.Unlock()
}

func embyExtractPausedUntil() (time.Time, bool) {
	embyExtractPaused.Lock()
	defer embyExtractPaused.Unlock()
	t := embyExtractPaused.until
	return t, !t.IsZero() && time.Now().Before(t)
}

// ---- 按条目记账 ----

func embyExtractLoad(id string) (model.EmbyExtractMark, bool) {
	var m model.EmbyExtractMark
	return m, model.DB.Where("item_id = ?", id).First(&m).Error == nil
}

func embyExtractStore(m model.EmbyExtractMark) {
	if err := model.DB.Save(&m).Error; err != nil {
		log.Printf("[Emby探测] ✗ 记账失败 %s: %v", m.ItemID, err)
	}
}

// embyExtractAllowed 这个条目现在能不能请求探测（只看，不记账）
func embyExtractAllowed(id string, now time.Time) (bool, string) {
	if model.DB == nil {
		return false, "数据库未就绪"
	}
	m, ok := embyExtractLoad(id)
	if !ok {
		return true, ""
	}
	if m.Attempts >= embyExtractMaxAttempts {
		return false, fmt.Sprintf("已请求过 %d 次都没成功（上次：%s），不再自动探测", m.Attempts, m.LastErr)
	}
	if since := now.Sub(m.LastAt); since < embyExtractRetryAfter {
		return false, fmt.Sprintf("%s 前刚请求过，%s 后才允许再试",
			since.Round(time.Minute), (embyExtractRetryAfter - since).Round(time.Minute))
	}
	return true, ""
}

// embyExtractClaim 发请求前记一次尝试；不允许时返回 false。
// label 是条目的称呼，任务中心列失败清单用。只有 worker 一个 goroutine 调它，查与写之间不会插进别人
func embyExtractClaim(id, label string, now time.Time) (bool, string) {
	if model.DB == nil {
		return false, "数据库未就绪，记不了账就不探测"
	}
	if ok, why := embyExtractAllowed(id, now); !ok {
		return false, why
	}
	m, _ := embyExtractLoad(id)
	m.ItemID, m.Attempts, m.LastAt, m.LastErr = id, m.Attempts+1, now, "请求中"
	if label != "" {
		m.Label = truncateStr(label, 250)
	}
	embyExtractStore(m)
	return true, ""
}

// embyExtractSettle 记下结果：成功删账（之后靠「已有媒体信息」跳过），失败留账挡住重复探测
func embyExtractSettle(id string, ok bool, errMsg string) {
	if ok {
		model.DB.Where("item_id = ?", id).Delete(&model.EmbyExtractMark{})
		return
	}
	if m, found := embyExtractLoad(id); found {
		m.LastErr = truncateStr(errMsg, 200)
		embyExtractStore(m)
	}
}

// pruneEmbyExtractMarks 30 天前的失败记账清掉：那时候文件多半已经洗版换过，值得再给一次机会
func pruneEmbyExtractMarks() {
	if model.DB == nil {
		return
	}
	model.DB.Where("last_at < ?", time.Now().AddDate(0, 0, -30)).Delete(&model.EmbyExtractMark{})
}

// embyStream PlaybackInfo / Items 返回里的一条轨道。
// 探测只看 Type；其余字段给本地文件页的片目详情显示（localdetail.go），同一次 /Items 请求顺带解出来
type embyStream struct {
	Type              string  `json:"Type"`
	Codec             string  `json:"Codec"`
	Profile           string  `json:"Profile"`
	Language          string  `json:"Language"`
	DisplayLanguage   string  `json:"DisplayLanguage"`
	DisplayTitle      string  `json:"DisplayTitle"`
	Title             string  `json:"Title"`
	Width             int     `json:"Width"`
	Height            int     `json:"Height"`
	BitRate           int64   `json:"BitRate"`
	BitDepth          int     `json:"BitDepth"`
	AverageFrameRate  float64 `json:"AverageFrameRate"`
	VideoRange        string  `json:"VideoRange"`
	ExtendedVideoType string  `json:"ExtendedVideoType"`
	Channels          int     `json:"Channels"`
	ChannelLayout     string  `json:"ChannelLayout"`
	IsDefault         bool    `json:"IsDefault"`
	IsForced          bool    `json:"IsForced"`
	IsExternal        bool    `json:"IsExternal"`
}

// embyExtractItem 一个要提前探测的影视条目
type embyExtractItem struct {
	ID                string       `json:"Id"`
	Name              string       `json:"Name"`
	Type              string       `json:"Type"`
	Path              string       `json:"Path"`
	SeriesName        string       `json:"SeriesName"`
	IndexNumber       int          `json:"IndexNumber"`
	ParentIndexNumber int          `json:"ParentIndexNumber"`
	RunTimeTicks      int64        `json:"RunTimeTicks"`
	MediaStreams      []embyStream `json:"MediaStreams"`
	MediaSources      []struct {
		Path         string       `json:"Path"`
		Container    string       `json:"Container"`
		Size         int64        `json:"Size"`
		Bitrate      int64        `json:"Bitrate"`
		MediaStreams []embyStream `json:"MediaStreams"`
	} `json:"MediaSources"`
}

// label 日志里怎么称呼它：剧集带剧名与季集号
func (it embyExtractItem) label() string {
	if strings.EqualFold(it.Type, "Episode") && it.SeriesName != "" {
		return fmt.Sprintf("%s S%02dE%02d", it.SeriesName, it.ParentIndexNumber, it.IndexNumber)
	}
	return it.Name
}

// embyStreamsComplete 视频 + 音轨至少两条才算提取过（LitePan 同一判据）：
// 只有字幕、或者只有一条视频流，都说明 Emby 没真正读过这个文件
func embyStreamsComplete(streams []embyStream) bool {
	n := 0
	for _, s := range streams {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "Subtitle") {
			n++
		}
	}
	return n >= 2
}

func (it embyExtractItem) hasMediaInfo() bool {
	if embyStreamsComplete(it.MediaStreams) {
		return true
	}
	for _, s := range it.MediaSources {
		if embyStreamsComplete(s.MediaStreams) {
			return true
		}
	}
	return false
}

// extractable 光盘结构（ISO / BDMV / VIDEO_TS）Emby 探测不了，探了也是白占一次 115 直链
func (it embyExtractItem) extractable() bool {
	paths := []string{it.Path}
	for _, s := range it.MediaSources {
		paths = append(paths, s.Path)
	}
	for _, p := range paths {
		p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
		p = strings.TrimSuffix(p, ".strm")
		if strings.HasSuffix(p, ".iso") || strings.Contains(p, "/bdmv/") || strings.Contains(p, "/video_ts/") {
			return false
		}
	}
	return true
}

// embyExtractTargets 一条 Emby 路径下还缺媒体信息的影视条目。
// 路径可能是单个 .strm（整理落盘点名回查的就是它，条目是 Episode / Movie），
// 也可能是片目目录（条目是 Folder / Series，要往下找）。found = Emby 里有这条路径的条目
func embyExtractTargets(cfg embyRefreshCfg, embyPath string) (todo []embyExtractItem, found bool, err error) {
	found, items, err := embyMediaItemsAt(cfg, embyPath)
	if err != nil {
		return nil, found, err
	}
	if !found {
		// 还没入库：刮削结束时排进来的新片目常见，入库确认那条入口会再排一次
		vlog("[Emby探测] %s 在 Emby 里还没有条目，跳过", embyPath)
		return nil, false, nil
	}
	for _, it := range items {
		if it.ID != "" && !it.hasMediaInfo() && it.extractable() {
			todo = append(todo, it)
		}
	}
	return todo, true, nil
}

// embyMediaItemsAt 一条 Emby 路径下的全部影视条目（Movie / Episode / Video），带轨道信息。
// found = 路径上有 Emby 条目（没有说明还没入库，或路径映射不对）
func embyMediaItemsAt(cfg embyRefreshCfg, embyPath string) (found bool, items []embyExtractItem, err error) {
	hits := embyItemsByPath(cfg, embyPath)
	if len(hits) == 0 {
		return false, nil, nil
	}
	hit := pickMediaHit(hits)
	q := url.Values{
		"Fields":                 {"MediaStreams,MediaSources,Path"},
		"IncludeItemTypes":       {"Movie,Episode,Video"},
		"EnableTotalRecordCount": {"false"},
	}
	switch strings.ToLower(hit.Type) {
	case "movie", "episode", "video":
		q.Set("Ids", hit.ID)
	default:
		q.Set("ParentId", hit.ID)
		q.Set("Recursive", "true")
		q.Set("Limit", "2000")
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		return true, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Items []embyExtractItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return true, nil, err
	}
	return true, out.Items, nil
}

func embyPathBase(p string) string {
	return filepath.Base(strings.ReplaceAll(p, "\\", "/"))
}

// embyExtractClient PlaybackInfo 专用：embyRequest 的 20 秒不够远端 STRM 探测一次
var embyExtractClient = &http.Client{Timeout: embyExtractTimeout}

// embyExtractOne POST /Items/{id}/PlaybackInfo，让 Emby 探测并存下这个条目的媒体信息。
// 直接打 Emby 本身而不是本站反代：反代会拦 PlaybackInfo 做直连改写和直链预取，这里都用不上
func embyExtractOne(cfg embyRefreshCfg, it embyExtractItem) (ok bool, errMsg string) {
	start := time.Now()
	q := url.Values{"api_key": {cfg.APIKey}}
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/Items/"+url.PathEscape(it.ID)+"/PlaybackInfo?"+q.Encode(), nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := embyExtractClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			log.Printf("[Emby探测] ○ %s：等了 %s 还没返回，Emby 可能仍在提取（不会马上重试）", it.label(), embyExtractTimeout)
			return false, "超时"
		}
		log.Printf("[Emby探测] ✗ %s：请求失败 %v", it.label(), err)
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		log.Printf("[Emby探测] ✗ %s：HTTP %d", it.label(), resp.StatusCode)
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	var info struct {
		MediaSources []struct {
			MediaStreams []embyStream `json:"MediaStreams"`
		} `json:"MediaSources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	took := time.Since(start).Round(100 * time.Millisecond)
	var streams []embyStream
	if len(info.MediaSources) > 0 {
		streams = info.MediaSources[0].MediaStreams
	}
	if !embyStreamsComplete(streams) {
		log.Printf("[Emby探测] ○ %s：Emby 返回了，但没提取到音视频轨道（%s）—— 看 Emby 日志里这条 STRM 的 ffprobe 报错",
			it.label(), took)
		return false, "没提取到音视频轨道"
	}
	log.Printf("[Emby探测] ✓ %s：%s（%s）", it.label(), embyStreamsBrief(streams), took)
	return true, ""
}

// embyStreamsBrief 视频 1 · 音轨 4 · 字幕 2
func embyStreamsBrief(streams []embyStream) string {
	n := map[string]int{}
	for _, s := range streams {
		n[strings.ToLower(s.Type)]++
	}
	out := fmt.Sprintf("视频 %d · 音轨 %d", n["video"], n["audio"])
	if n["subtitle"] > 0 {
		out += fmt.Sprintf(" · 字幕 %d", n["subtitle"])
	}
	return out
}
