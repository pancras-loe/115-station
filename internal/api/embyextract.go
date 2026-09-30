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
	"os"
	"path"
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
// 开关复用影视刮削的「轨道探测」（scrape.probe_streams / 刮削任务的 Probe）。入口分两类，规则不同：
//   - 自动：入库确认之后（embyVerifyIngest 查到条目的那一刻），全局开关，整理 / 增量 / 全量进来的都算
//     排的是所在片目目录（整部探），片目之上的目录不探，见 embyExtractTitleTargets
//   - 手动：用户这一次明确要探 —— 片目详情点「提前探测」、本地文件页刮削勾了「轨道探测」、
//     重新整理（带刮削且开着探测）。手动的一律建一个「Emby 提前探测」任务（kind=probe，
//     单独一条探测队列，embyprobejob.go），任务中心看得到进度、能停、失败了能重试
// 已经有媒体信息的条目一律不碰：每次探测都是一次 115 直链请求
//
// ⚠️ 防重复探测（2026-09-29 维护者明确要求：重复探测等于重复取 115 直链，有风控风险）。
// 同一个条目会从好几条路进队列：入库确认（整理刷新一次、增量再刷一次就是两次）、
// 片目目录与单集 .strm 又是两条不同的路径。只靠「已有媒体信息就跳过」挡不住失败 / 超时的条目 ——
// 它们每来一次就会再探一次。所以：
//  1. 按 Emby 条目 id 记账（EmbyExtractMark，落库，重启不丢）：发请求之前先记一次尝试，成功即删账。
//     自动入口：同一条目最多 embyExtractMaxAttempts 次、两次之间至少隔 embyExtractRetryAfter，
//     用完就再也不自动探（此前 30 天后清账重来，维护者认为多此一举：两次都失败，第三次多半也是白取直链）。
//     手动入口：不看次数与 24 小时，只防抖 —— 同一条目 embyExtractDebounce 内不重复请求
//     （连点、刮削与入库确认同时排到同一片目、上一次超时 Emby 可能还在探）。手动的尝试同样记账，
//     所以手动失败过的条目，自动入口也会少探一次
//  2. 连续 embyExtractBreakAfter 个条目失败（含超时）就整体暂停 embyExtractBreakPause ——
//     多半是 115 风控或 Emby 出了问题，再探只会火上浇油。手动的也等
//  3. 整理后自动刮削不再排队（入库确认那条入口已经覆盖了它的片目），见 execScrapeJob

// embyExtractGap 两次探测之间的间隔。每次探测都会经 302 取一次 115 直链，和整理、同步共用风控额度
var embyExtractGap = 3 * time.Second // var：测试里调短

// embyExtractTimeout 一次 PlaybackInfo 最多等多久。远端 STRM 的 ffprobe 慢的时候要几十秒，
// 超时也记一次尝试、计入连续失败：Emby 那边多半还在提取，马上再发等于同一个文件被探两遍
const embyExtractTimeout = 2 * time.Minute

// embyExtractMaxAttempts 自动入口同一个 Emby 条目最多请求几次探测（含超时、含手动的那几次）；
// embyExtractRetryAfter 自动入口两次之间的最短间隔
const (
	embyExtractMaxAttempts = 2
	embyExtractRetryAfter  = 24 * time.Hour
)

// embyExtractDebounce 手动入口的防抖：同一条目上次请求之后多久内不再发。
// 比单次超时（2 分钟）长一截：超时的那次 Emby 多半还在探，马上再发等于探两遍
var embyExtractDebounce = 5 * time.Minute // var：测试里调短

// 连续失败熔断
const embyExtractBreakAfter = 3

var embyExtractBreakPause = 30 * time.Minute // var：测试里调短

// embyExtractQueueMax 排队上限。积压太多说明 Emby 那边出了问题（或一次勾了整库刮削），
// 超出的丢掉（下次刮削还会再排），免得内存里挂着一条永远跑不完的队列
const embyExtractQueueMax = 5000

// embyExtractItemPrefix 队列里「直接点名一个 Emby 条目」的写法（任务中心失败清单里单条重试），
// 其余是 Emby 路径（片目目录或单个 .strm）
const embyExtractItemPrefix = "item:"

// embyExtractEntry 队列里的一条：一个 Emby 路径（或 item:条目id）+ 排它进来的探测任务。
// manual = 有手动入口排过它（按手动规则放行）；auto = 自动入口也排过（手动任务全停了还得按自动规则探完）
type embyExtractEntry struct {
	path   string
	jobs   []uint
	manual bool
	auto   bool
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

// queueEmbyExtract 自动入口：把一批 Emby 路径排进提前探测队列（去重），worker 第一次用到时才起
func queueEmbyExtract(embyPaths ...string) { queueEmbyExtractFor(0, embyPaths...) }

// queueEmbyExtractFor jobID ≠ 0 表示手动入口（探测任务）：按手动规则放行、结果写回那个任务。
// 路径已经在排的，把任务挂到那一条上，不另排
func queueEmbyExtractFor(jobID uint, embyPaths ...string) {
	q := &embyExtractQ
	q.mu.Lock()
	added, forJob, dropped := 0, 0, 0
	for _, p := range embyPaths {
		if p == "" {
			continue
		}
		e := q.queued[p]
		if e == nil {
			if len(q.queue) >= embyExtractQueueMax {
				dropped++
				continue
			}
			e = &embyExtractEntry{path: p}
			q.queued[p] = e
			q.queue = append(q.queue, e)
			added++
		}
		if jobID == 0 {
			e.auto = true
			continue
		}
		e.manual = true
		if !containsUint(e.jobs, jobID) {
			e.jobs = append(e.jobs, jobID)
			forJob++
		}
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
				Err: fmt.Sprintf("探测队列已满，%d 个片目没排进去，稍后重试这个任务", dropped)})
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

// embyExtractCanceled 用户停掉的探测任务：排着的路径摘掉它，正在探的路径探完手上这一集就收
var embyExtractCanceled = struct {
	sync.Mutex
	m map[uint]bool
}{m: map[uint]bool{}}

// cancelEmbyExtractJob 停掉一个探测任务：排着的条目只剩它的就整条拿掉（自动入口也排过的留下，改回自动规则）
func cancelEmbyExtractJob(jobID uint) {
	embyExtractCanceled.Lock()
	embyExtractCanceled.m[jobID] = true
	embyExtractCanceled.Unlock()
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.queue[:0]
	for _, e := range q.queue {
		if i := indexUint(e.jobs, jobID); i >= 0 {
			e.jobs = append(e.jobs[:i:i], e.jobs[i+1:]...)
			if len(e.jobs) == 0 {
				e.manual = false
				if !e.auto {
					delete(q.queued, e.path)
					continue
				}
			}
		}
		kept = append(kept, e)
	}
	q.queue = kept
}

// forgetEmbyExtractCancel 任务结束后清掉停止标记
func forgetEmbyExtractCancel(jobID uint) {
	embyExtractCanceled.Lock()
	delete(embyExtractCanceled.m, jobID)
	embyExtractCanceled.Unlock()
}

// embyExtractAllCanceled 这些任务是不是全被停了（空列表不算）
func embyExtractAllCanceled(jobs []uint) bool {
	if len(jobs) == 0 {
		return false
	}
	embyExtractCanceled.Lock()
	defer embyExtractCanceled.Unlock()
	for _, id := range jobs {
		if !embyExtractCanceled.m[id] {
			return false
		}
	}
	return true
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

func containsUint(xs []uint, x uint) bool { return indexUint(xs, x) >= 0 }

func indexUint(xs []uint, x uint) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

// embyExtractJobState 任务的探测进行到哪：running 正在探它的路径（label 是正在探的那一集）、
// queued 还在排（ahead = 前面还有几条路径）、"" 都不在了（探完了，或者服务重启过、内存里的队列没了）
func embyExtractJobState(jobID uint) (state, label string, ahead int) {
	embyExtractRunning.Lock()
	running := containsUint(embyExtractRunning.jobs, jobID)
	label = embyExtractRunning.label
	busy := embyExtractRunning.path != ""
	embyExtractRunning.Unlock()
	if running {
		return "running", label, 0
	}
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, e := range q.queue {
		if containsUint(e.jobs, jobID) {
			if busy {
				i++ // 正在探的那条（别人的）也算排在前面
			}
			return "queued", "", i
		}
	}
	return "", "", 0
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

// 队列里与一条路径相关的条目按什么规则排着（embyProbeStateOf 的 queue 参数）
const (
	probeQueuedNone   = ""
	probeQueuedAuto   = "auto"
	probeQueuedManual = "manual"
)

// embyExtractQueuedFor 队列里有没有与这条路径相关的（同一路径、它的上级或下级），按哪种规则排着
// （有一条手动的就算手动）。队列存的是路径不是条目：片目目录排进来时，底下每一集都算「排队中」
func embyExtractQueuedFor(embyPath string) string {
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/") }
	want := norm(embyPath)
	if want == "" {
		return probeQueuedNone
	}
	mode := probeQueuedNone
	see := func(e embyExtractEntry) {
		if e.path == "" || strings.HasPrefix(e.path, embyExtractItemPrefix) || !embyPathRelated(norm(e.path), want) {
			return
		}
		if e.manual {
			mode = probeQueuedManual
		} else if mode == probeQueuedNone {
			mode = probeQueuedAuto
		}
	}
	embyExtractRunning.Lock()
	cur := embyExtractEntry{path: embyExtractRunning.path, manual: len(embyExtractRunning.jobs) > 0}
	embyExtractRunning.Unlock()
	see(cur)
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.queue {
		see(*e)
	}
	return mode
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
		if !embyExtractPath(cfg, *e) {
			return
		}
	}
}

// embyExtractPath 探测一条路径下缺媒体信息的条目；返回 false 表示服务要退出了。
// 单条出错（含 panic）只丢这一条，worker 不能死：它只在第一次排队时拉起。
// e.jobs 是排它进来的探测任务：每个条目的结果（成功 / 失败 / 防抖跳过 / 还没入库）写回它们
func embyExtractPath(cfg embyRefreshCfg, e embyExtractEntry) (alive bool) {
	alive = true
	p, jobs := e.path, e.jobs
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
	var (
		items []embyExtractItem
		found bool
		err   error
	)
	if id, ok := strings.CutPrefix(p, embyExtractItemPrefix); ok {
		items, found, err = embyExtractTargetByID(cfg, id)
		if err == nil && !found {
			// Emby 里已经没有这个条目（删了、洗版换了新条目）：失败记账留着只会一直挂在清单里
			label := embyExtractMarkLabel(id)
			model.DB.Where("item_id = ?", id).Delete(&model.EmbyExtractMark{})
			probeReportItem(jobs, jobProbeItem{Label: label, Kind: probeItemMissing,
				Err: "Emby 里已经没有这个条目（删除或洗版换了新条目），已移除它的失败记录"})
			return
		}
	} else {
		items, found, err = embyExtractTargets(cfg, p)
	}
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
	manual := e.manual
	var todo []embyExtractItem
	held := 0
	for _, it := range items {
		if ok, why := embyExtractAllowed(it.ID, time.Now(), manual); !ok {
			held++
			vlog("[Emby探测] 跳过 %s：%s", it.label(), why)
			probeReportItem(jobs, probeHeldItem(it))
			continue
		}
		todo = append(todo, it)
	}
	probeReportPlanned(jobs, len(todo))
	if len(todo) == 0 {
		if held > 0 {
			vlog("[Emby探测] %s：%d 个条目刚请求过或自动探测次数已用完，不重复探测", embyPathBase(p), held)
		}
		return
	}
	note := ""
	if held > 0 {
		note = fmt.Sprintf("（另 %d 个刚请求过或次数已用完，不重复）", held)
	}
	mode := "自动"
	if manual {
		mode = "手动"
	}
	log.Printf("[Emby探测] ▶ %s：%d 个条目还没有媒体信息，逐个提前探测（%s）%s", embyPathBase(p), len(todo), mode, note)
	seen := map[string]string{} // 这一轮探过的版本 → 失败原因（成功为空）
	sent := false               // 这一轮发过请求：下一个请求前要隔开
	defer setEmbyExtractRunning("", "")
	for i, it := range todo {
		// 间隔放在记账之前：记账（LastErr=「请求中」）到发请求之间不能空着 3 秒 ——
		// 那几秒里条目既不算「正在探」，失败清单就把它当成「请求中断（服务重启）」列出来，
		// 探一集闪一条（2026-09-30 现场）。停服也不会留下一笔没发出去的尝试
		if sent {
			if !embyExtractWait(embyExtractGap) {
				return false
			}
			sent = false
		}
		if manual && embyExtractAllCanceled(jobs) {
			if !e.auto {
				n := len(todo) - i
				log.Printf("[Emby探测] ○ %s：任务已停止，剩下 %d 个条目不探", embyPathBase(p), n)
				probeReportCanceled(jobs, n)
				return
			}
			// 自动入口也排过它：剩下的按自动规则接着探
			manual = false
		}
		// 记账在发请求之前：请求发出去就算一次，哪怕随后超时、进程被杀。
		// 多版本条目记一笔账、逐个版本各发一次（每个版本各取一次 115 直链，不算重复）
		if ok, _ := embyExtractClaim(it.ID, it.label(), time.Now(), manual); !ok {
			probeReportItem(jobs, probeHeldItem(it))
			continue
		}
		// 记了账就算「正在探」，一直到结果落账（多版本之间的间隔也算）
		setEmbyExtractRunning(it.ID, it.label())
		ok, errMsg, brk := true, "", false
		srcs := it.probeSources(cfg.PathMapping)
		for j, src := range srcs {
			k := embySourceKey(src)
			if r, done := seen[k]; done && k != "" {
				// 同一个版本在这一轮里已经探过（Emby 把它另列成了一个条目）：结果照搬，不再发请求
				if r != "" {
					ok, errMsg = false, joinProbeErr(errMsg, r)
				}
				continue
			}
			if sent {
				if !embyExtractWait(embyExtractGap) {
					return false
				}
			}
			sent = true
			setEmbyExtractRunning(it.ID, it.sourceLabel(src))
			one, msg := embyExtractOne(cfg, it, src)
			if one {
				msg = ""
			} else {
				if msg == "" {
					msg = "失败"
				}
				if len(it.MediaSources) > 1 {
					msg = strings.TrimSuffix(embyPathBase(src.Path), ".strm") + "：" + msg
				}
				ok, errMsg = false, joinProbeErr(errMsg, msg)
			}
			seen[k] = msg
			if one {
				embyExtractFails = 0
			} else if embyExtractFails++; embyExtractFails >= embyExtractBreakAfter {
				// 熔断：这个条目剩下的版本不探了（算这次失败），先落账再暂停 ——
				// 暂停放在落账之前的话，这 30 分钟里它一直挂着「请求中 / 探测中」
				brk = true
				if j < len(srcs)-1 {
					errMsg = joinProbeErr(errMsg, "连续失败熔断，其余版本没探")
				}
				break
			}
		}
		embyExtractSettle(it.ID, ok, errMsg)
		setEmbyExtractRunning("", "")
		if ok {
			probeReportOK(jobs)
		} else {
			item := probeHeldItem(it) // 记账刚写过：带上失败原因与什么时候能再试
			item.Kind = probeItemFailed
			probeReportItem(jobs, item)
		}
		if brk {
			log.Printf("[Emby探测] ⚠ 连续 %d 个条目探测失败，暂停 %s（可能是 115 风控或 Emby 异常），排队中的稍后继续",
				embyExtractFails, embyExtractBreakPause)
			embyExtractFails = 0
			setEmbyExtractPausedUntil(time.Now().Add(embyExtractBreakPause))
			if !embyExtractWait(embyExtractBreakPause) {
				return false
			}
			sent = false // 暂停已经隔开了
		}
	}
	if sent && !embyExtractWait(embyExtractGap) {
		return false
	}
	return
}

func joinProbeErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// embyExtractWait 两次探测之间的间隔（熔断暂停也走它）；返回 false 表示服务要退出了
func embyExtractWait(d time.Duration) bool {
	select {
	case <-stopCh:
		return false
	case <-time.After(d):
	}
	setEmbyExtractPausedUntil(time.Time{})
	return true
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

// embyExtractMarkLabel 记账里存的称呼，老记账没有就用条目 id
func embyExtractMarkLabel(id string) string {
	if m, ok := embyExtractLoad(id); ok && m.Label != "" {
		return m.Label
	}
	return "Emby 条目 " + id
}

// embyExtractAllowed 这个条目现在能不能请求探测（只看，不记账）。manual = 手动入口，只看防抖
func embyExtractAllowed(id string, now time.Time, manual bool) (bool, string) {
	if model.DB == nil {
		return false, "数据库未就绪"
	}
	m, ok := embyExtractLoad(id)
	if !ok {
		return true, ""
	}
	since := now.Sub(m.LastAt)
	if manual {
		if since < embyExtractDebounce {
			return false, fmt.Sprintf("%s 前刚请求过，%s 后才能再手动请求",
				since.Round(time.Second), (embyExtractDebounce - since).Round(time.Second))
		}
		return true, ""
	}
	if m.IgnoredAt != nil {
		return false, "已在任务中心忽略，不再自动探测，可以手动探测"
	}
	if m.Attempts >= embyExtractMaxAttempts {
		return false, fmt.Sprintf("已请求过 %d 次都没成功（上次：%s），不再自动探测，可以手动重试", m.Attempts, m.LastErr)
	}
	if since < embyExtractRetryAfter {
		return false, fmt.Sprintf("%s 前刚请求过，%s 后才允许自动再试",
			since.Round(time.Minute), (embyExtractRetryAfter - since).Round(time.Minute))
	}
	return true, ""
}

// embyExtractPending 记了账、结果还没回来时 LastErr 的占位
const embyExtractPending = "请求中"

// embyExtractClaim 发请求前记一次尝试；不允许时返回 false。
// label 是条目的称呼，任务中心列失败清单用。只有 worker 一个 goroutine 调它，查与写之间不会插进别人
func embyExtractClaim(id, label string, now time.Time, manual bool) (bool, string) {
	if model.DB == nil {
		return false, "数据库未就绪，记不了账就不探测"
	}
	if ok, why := embyExtractAllowed(id, now, manual); !ok {
		return false, why
	}
	m, _ := embyExtractLoad(id)
	m.ItemID, m.Attempts, m.LastAt, m.LastErr = id, m.Attempts+1, now, embyExtractPending
	// 能走到这里的忽略条目只可能是手动请求：用户点名要探，再失败就该回到清单里
	m.IgnoredAt = nil
	if label != "" {
		m.Label = truncateStr(label, 250)
	}
	embyExtractStore(m)
	return true, ""
}

// embyExtractSettle 记下结果：成功删账（之后靠「已有媒体信息」跳过），失败留账挡住重复探测。
// 失败记账不再定期清理：自动入口用完两次就不再探，要再试走手动
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
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Path              string            `json:"Path"`
	SeriesName        string            `json:"SeriesName"`
	IndexNumber       int               `json:"IndexNumber"`
	ParentIndexNumber int               `json:"ParentIndexNumber"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	MediaStreams      []embyStream      `json:"MediaStreams"`
	MediaSources      []embyMediaSource `json:"MediaSources"`
}

// embyMediaSource 条目的一个版本。同一片目目录里放两个 .strm（两个版本），Emby 会把它们合成
// 一个条目、MediaSources 里各一份，媒体信息也是各探各的
type embyMediaSource struct {
	ID           string       `json:"Id"`
	Path         string       `json:"Path"`
	Container    string       `json:"Container"`
	Size         int64        `json:"Size"`
	Bitrate      int64        `json:"Bitrate"`
	MediaStreams []embyStream `json:"MediaStreams"`
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

func (it embyExtractItem) hasMediaInfo() bool { return len(it.lackingSources()) == 0 }

// lackingSources 还缺媒体信息的版本。
// 多版本条目要**每个版本**都有才算：2026-09-29 现场，双版本电影只按条目 id 探一次，
// Emby 只探了其中一个版本，另一个一直显示「未探测」，再怎么手动探也还是只探那一个。
// 单版本（或没带 MediaSources）沿用原判据：顶层与版本里任一份齐了就算有。
// 没带 MediaSources 时返回一个空 ID 的版本，表示「不点名版本、探整个条目」
func (it embyExtractItem) lackingSources() []embyMediaSource {
	if len(it.MediaSources) <= 1 {
		if embyStreamsComplete(it.MediaStreams) {
			return nil
		}
		if len(it.MediaSources) == 0 {
			return []embyMediaSource{{Path: it.Path}}
		}
		if embyStreamsComplete(it.MediaSources[0].MediaStreams) {
			return nil
		}
		return []embyMediaSource{it.MediaSources[0]}
	}
	var out []embyMediaSource
	for _, s := range it.MediaSources {
		if embyStreamsComplete(s.MediaStreams) {
			continue
		}
		// 顶层轨道是主版本的：Emby 有时只把它写在顶层
		if s.Path != "" && s.Path == it.Path && embyStreamsComplete(it.MediaStreams) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sourceLabel 多版本时日志 / 报告里带上是哪个版本（文件名）
func (it embyExtractItem) sourceLabel(s embyMediaSource) string {
	if len(it.MediaSources) <= 1 || s.Path == "" {
		return it.label()
	}
	return it.label() + "（" + strings.TrimSuffix(embyPathBase(s.Path), ".strm") + "）"
}

// embySourceKey 同一轮里认版本用：多版本条目若被 Emby 同时列成两个条目，别把同一个版本探两次
func embySourceKey(s embyMediaSource) string {
	if s.Path != "" {
		return "p:" + strings.ReplaceAll(s.Path, "\\", "/")
	}
	if id := strings.TrimPrefix(s.ID, "mediasource_"); id != "" {
		return "i:" + id
	}
	return ""
}

// probeSources 真正要探的版本：还缺媒体信息、且不是光盘结构。
// 光盘结构（ISO / BDMV / VIDEO_TS）Emby 探测不了，探了也是白占一次 115 直链（LitePan 同样跳过）。
// 按版本判：同一片目一个 ISO 版本一个 mkv 版本时，mkv 那个照探
func (it embyExtractItem) probeSources(pathMapping string) []embyMediaSource {
	var out []embyMediaSource
	for _, s := range it.lackingSources() {
		if !it.discSource(s, pathMapping) {
			out = append(out, s)
		}
	}
	return out
}

// needsProbe 条目里还有要探的版本
func (it embyExtractItem) needsProbe(pathMapping string) bool {
	return len(it.probeSources(pathMapping)) > 0
}

// discSource 这个版本是不是光盘结构。
//
// 只看 Emby 给的路径不够：2026-09-28 起 STRM 名不带视频扩展名（disc.iso → disc.strm，见 strmname.go），
// 条目路径上已经没有 .iso；版本路径是 Emby 扫库时记下的直链，STRM 改写后到它重扫之前还是旧的。
// 所以再读一次本地 STRM 里的直链（零 115 请求）—— ISO 的直链一律带 .iso（writeStrmNamed）
func (it embyExtractItem) discSource(s embyMediaSource, pathMapping string) bool {
	paths := []string{s.Path}
	if len(it.MediaSources) <= 1 && it.Path != s.Path {
		paths = append(paths, it.Path) // 单版本：条目路径就是这个版本的 .strm
	}
	for _, p := range paths {
		if p != "" && (discPath(p) || strmFileIsDisc(embyPathToLocal(pathMapping, p))) {
			return true
		}
	}
	return false
}

// discPath 一条路径或直链是不是光盘结构：
// xxx.iso / xxx.iso.strm（旧命名）/ …/BDMV/… / …/VIDEO_TS/…，
// 以及直链 /d/{pickcode}.iso[?/原文件名] 与 /d/{pickcode}?/原文件名.iso
func discPath(p string) bool {
	p = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/")))
	if strings.Contains(p, "/bdmv/") || strings.Contains(p, "/video_ts/") {
		return true
	}
	if strings.HasSuffix(strings.TrimSuffix(p, ".strm"), ".iso") {
		return true
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return strings.HasSuffix(p[:i], ".iso")
	}
	return false
}

// strmFileIsDisc 读本地 .strm 里的直链判断。不是 .strm、读不到的一律按「不是」处理（照常探）
func strmFileIsDisc(local string) bool {
	if !strings.EqualFold(path.Ext(local), ".strm") {
		return false
	}
	f, err := os.Open(filepath.FromSlash(local))
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4096) // 直链一行，远不到这么长
	n, _ := io.ReadFull(f, buf)
	return discPath(string(buf[:n]))
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
		if it.ID != "" && it.needsProbe(cfg.PathMapping) {
			todo = append(todo, it)
		}
	}
	return todo, true, nil
}

// embyExtractTitleTargets 入库确认到的路径 → 自动探测的目标：一律归到所在的片目目录，整部探。
//
// 入库确认拿来回查的路径有两种，直接排进探测队列两种都不对：
//   - 整理点名的落盘样本（每轮最多 embyVerifySample 个 .strm）：一部 20 集的剧只会探到 3 集，
//     其余的第一次播放还得 Emby 现场探；
//   - 没点名时的刷新目标（全量同步的同步根、迁移时的整个媒体库）：直接排进去就是递归探整片库，
//     一次全量就是成千上万次 115 直链请求。
//
// 所以按当前分类规则（libCategoryLayout.titleOf，与本地文件页的片目同口径）归到片目目录；
// 在片目之上的目录（分类 / 媒体库 / 同步根）一律不自动探；不属于任何片目的单个 .strm（手机上传直接丢进分类目录）只探它自己
func embyExtractTitleTargets(cfg embyRefreshCfg, embyPaths []string) []string {
	return extractTitleTargets(embyPaths, localMediaRoot(), loadLibCategoryLayout(),
		func(p string) string { return embyPathToLocal(cfg.PathMapping, p) },
		func(local string) string { return embyPathOf(cfg, local) })
}

// extractTitleTargets 纯函数部分（测试用假映射）
func extractTitleTargets(embyPaths []string, localRoot string, layout libCategoryLayout,
	toLocal, toEmby func(string) string) (out []string) {
	root := ""
	if localRoot != "" {
		root = strings.TrimRight(filepath.ToSlash(filepath.Clean(localRoot)), "/")
	}
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var skipped []string
	for _, p := range embyPaths {
		local := strings.TrimRight(filepath.ToSlash(toLocal(p)), "/")
		isFile := strings.EqualFold(path.Ext(local), ".strm")
		if root != "" && strings.HasPrefix(local, root+"/") {
			probe := local[len(root)+1:]
			if !isFile {
				probe += "/_" // titleOf 要求标题目录之下还有一段
			}
			if key, _, _, _, ok := layout.titleOf(probe); ok {
				add(toEmby(filepath.FromSlash(root + "/" + key)))
				continue
			}
		}
		if isFile {
			add(p)
			continue
		}
		skipped = append(skipped, p)
	}
	if len(skipped) > 0 {
		log.Printf("[Emby探测] ○ %d 个入库确认的目录在片目之上（分类 / 媒体库 / 同步根），不自动探测整片库：%s",
			len(skipped), truncateStr(strings.Join(skipped, "、"), 200))
	}
	return out
}

// embyExtractTargetByID 点名一个 Emby 条目（失败清单里重试）：found = Emby 里还有它；
// 已经有媒体信息 / 光盘结构的不返回（同 embyExtractTargets）
func embyExtractTargetByID(cfg embyRefreshCfg, id string) (todo []embyExtractItem, found bool, err error) {
	q := url.Values{
		"Ids":                    {id},
		"Fields":                 {"MediaStreams,MediaSources,Path"},
		"EnableTotalRecordCount": {"false"},
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Items []embyExtractItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false, err
	}
	for _, it := range out.Items {
		if it.ID != id {
			continue
		}
		found = true
		if it.hasMediaInfo() {
			// 已经有了（在 Emby 里播过一次）：失败记账没用了
			model.DB.Where("item_id = ?", id).Delete(&model.EmbyExtractMark{})
		} else if it.needsProbe(cfg.PathMapping) {
			todo = append(todo, it)
		}
	}
	return todo, found, nil
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
//
// 多版本条目点名 MediaSourceId：不点名时 Emby 只探它自己挑的那个版本，另一个永远缺媒体信息
func embyExtractOne(cfg embyRefreshCfg, it embyExtractItem, src embyMediaSource) (ok bool, errMsg string) {
	start := time.Now()
	q := url.Values{"api_key": {cfg.APIKey}}
	multi := len(it.MediaSources) > 1 && src.ID != ""
	if multi {
		q.Set("MediaSourceId", src.ID)
	}
	label := it.sourceLabel(src)
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/Items/"+url.PathEscape(it.ID)+"/PlaybackInfo?"+q.Encode(), nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := embyExtractClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			log.Printf("[Emby探测] ○ %s：等了 %s 还没返回，Emby 可能仍在提取（不会马上重试）", label, embyExtractTimeout)
			return false, "超时"
		}
		log.Printf("[Emby探测] ✗ %s：请求失败 %v", label, err)
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		log.Printf("[Emby探测] ✗ %s：HTTP %d", label, resp.StatusCode)
		return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	var info struct {
		MediaSources []embyMediaSource `json:"MediaSources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	took := time.Since(start).Round(100 * time.Millisecond)
	streams := playbackSourceStreams(info.MediaSources, src, multi)
	if !embyStreamsComplete(streams) {
		log.Printf("[Emby探测] ○ %s：Emby 返回了，但没提取到音视频轨道（%s）—— 看 Emby 日志里这条 STRM 的 ffprobe 报错",
			label, took)
		return false, "没提取到音视频轨道"
	}
	log.Printf("[Emby探测] ✓ %s：%s（%s）", label, embyStreamsBrief(streams), took)
	return true, ""
}

// playbackSourceStreams PlaybackInfo 返回里点名那个版本的轨道。
// 点了名却找不到它（Emby 版本不认 MediaSourceId 时会把全部版本都吐回来），按版本路径再认一次，
// 都认不出就算没探到 —— 拿另一个版本的轨道报成功，正是这次要修的毛病
func playbackSourceStreams(sources []embyMediaSource, src embyMediaSource, multi bool) []embyStream {
	if !multi {
		if len(sources) > 0 {
			return sources[0].MediaStreams
		}
		return nil
	}
	want := strings.TrimPrefix(src.ID, "mediasource_")
	for _, s := range sources {
		if strings.TrimPrefix(s.ID, "mediasource_") == want {
			return s.MediaStreams
		}
	}
	for _, s := range sources {
		if src.Path != "" && s.Path == src.Path {
			return s.MediaStreams
		}
	}
	return nil
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
