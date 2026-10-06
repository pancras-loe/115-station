package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 任务队列 ====================
//
// 改造前手动操作（重新整理、确认入库）都是「HTTP 请求里 taskMu.Acquire 等 20 秒 → 同步跑完」：
// 撞上增量或定时整理就转圈 20 秒再报 409；一次只能改一条，下一条得等上一条的请求返回；
// 跑的过程中前端只看得到按钮在转。
//
// 现在：提交入队立即返回，一个常驻 worker 串行执行，每个任务有状态、结构化进度与结果。
// 锁仍然只有 taskMu，串行语义不变 —— worker 只是它的又一个使用者，每个任务单独拿放锁。
//
// 刮削单独走一条队列（scrapeLane，2026-09-28 起）：一部几百集的综艺刮一遍要几十分钟，
// 此前它挂在整理任务末尾、占着 taskMu，这段时间整理 / 同步 / 确认入库全在排队。
// 刮削只写本地元数据（勾了上传才碰网盘，且走 pan115Ops 的节流与抑制），不需要和整理互斥；
// 与整理动同一个片目时的冲突由刮削自己收拾（localscrape.go 的 scrapeCompensate），主队列不等它。
//
// 形态借鉴 LitePan internal/automation/service_run.go 的 submitRun / endRun
// （提交即返回、有任务在跑就排队、跑完取下一个；只读参考，未复制代码）。
// 设计全文见 docs/115-station-notes/TASK-QUEUE-PLAN.md

const (
	jobQueued      = "queued"
	jobRunning     = "running"
	jobSuccess     = "success"
	jobPartial     = "partial" // 跑完了，但有一部分没做成（刮削有片目 / 产物失败、提前探测有条目失败）
	jobFailed      = "failed"
	jobCanceled    = "canceled"
	jobInterrupted = "interrupted"

	jobPriorityManual     = 0
	jobPriorityBackground = 1

	// jobMaxBatch 单次提交上限：与整理记录页每页最多 100 条对齐，「全选本页」天然不超
	jobMaxBatch = 100
)

// jobFinishedStatuses 已结束的状态（清理、重试用）
var jobFinishedStatuses = []string{jobSuccess, jobPartial, jobFailed, jobCanceled, jobInterrupted}

// 做成变量只为让测试不必真等，生产行为不变
var (
	// jobItemEstimate 每条记录的粗估耗时（2~4 次 115 写请求 × ≥3 秒写节流，加 TMDB 与刮削），只用于给用户一个量级
	jobItemEstimate = 15 * time.Second
	// jobAcquireSlice worker 等锁的单次时长。等不到不失败，查一眼任务还在不在队列里再接着等
	jobAcquireSlice = 30 * time.Second
	// jobIdlePoll 队列为空时多久回来看一眼（入队会主动唤醒，这只是兜底）
	jobIdlePoll = 30 * time.Second
	// incrWindowPoll / incrWindowMax 让路窗口里多久看一次增量跑没跑、最多等多久
	incrWindowPoll = time.Second
	incrWindowMax  = 90 * time.Second
)

// jobParams 任务参数（各类型共用一个结构，用不到的字段留空）
type jobParams struct {
	RecordIDs []uint `json:"record_ids,omitempty"`
	TmdbID    int    `json:"tmdb_id,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	// Sync 全量 / 手动增量的同步参数
	Sync *syncJobParams `json:"sync,omitempty"`
	// Scheduled 定时整理（cron 触发）：独立增量轮询关着时顺带跑一轮增量（逃生门下的老行为）
	Scheduled bool `json:"scheduled,omitempty"`
	// Files 网盘文件页勾选的条目（整理所选 / 移动，filebrowser.go）
	Files *fileJobParams `json:"files,omitempty"`
	// Local 本地文件页勾选的片目（刮削，localscrape.go）
	Local *localScrapeParams `json:"local,omitempty"`
	// Probe 手动 Emby 提前探测（embyprobejob.go）
	Probe *probeJobParams `json:"probe,omitempty"`
	// StrictDup 库内同集多份体检发起的重新整理：同名并排的也停下来问（orgdupscan.go）
	StrictDup bool `json:"strict_dup,omitempty"`
}

// syncJobParams 全量 / 增量同步的请求参数（与 /sync/full、/sync/incremental 的请求体同构）
type syncJobParams struct {
	Cid       string   `json:"cid"`
	LocalPath string   `json:"local_path"`
	VideoExt  []string `json:"video_ext"`
	ImageExt  []string `json:"image_ext"`
	DataExt   []string `json:"data_ext"`
	Mode      string   `json:"mode,omitempty"`
}

func decodeJobParams(job *model.TaskJob) jobParams {
	var p jobParams
	_ = json.Unmarshal([]byte(job.Params), &p)
	return p
}

// jobSpec 入队请求
type jobSpec struct {
	Kind      string
	Title     string
	DedupeKey string
	Source    string
	Priority  int
	Params    jobParams
	// Merge 同键已有排队中的任务时怎么合并参数（默认以新的为准）。
	// 整理后刮削要并集：上一轮整理的片目还没刮，不能被这一轮覆盖掉
	Merge func(prev, next jobParams) (jobParams, string)
}

// jobOutcome 执行结果
type jobOutcome struct {
	Message  string
	Canceled bool // 用户中途停止（已完成的部分照常生效）
	Result   any  // 给前端的结构化结果（如整理后有几项待确认），落 TaskJob.Result
	// Idle 这一轮什么都没干（定时整理 10 分钟一轮、多数空转）：跑完直接删行，不进历史。
	// 失败的不算空转，照样留下原因
	Idle bool
	// Partial 跑完了但有一部分失败：记成「部分失败」而不是「完成」，原因放在 Result 里。
	// 此前刮削错了几十处也显示「完成」，用户不点开详情根本不知道
	Partial bool
}

// jobKindBackground 后台任务（定时整理、转存触发 …）跑完留下的历史行。
// 它们还不进队列（TASK-QUEUE-PLAN.md §7 阶段 4），但要在队列面板的「最近结束」里看得到，
// 取代原来只在内存里存 5 条的 recentRuns
const jobKindBackground = "background"

// jobStoppable 运行中能不能请求停止：只有「逐条处理」的任务能在两条之间停下。
// 单条的重新整理 / 确认本身就是一条；全量、增量的遍历中途停下没有意义（下一轮原样重来）
func jobStoppable(job *model.TaskJob) bool {
	switch job.Kind {
	case "organize":
		return true
	case "confirm":
		return len(decodeJobParams(job).RecordIDs) > 1
	case "scrape":
		// 刮削逐个片目、片目内逐个文件，随时能停（写了一半的片目下次「只补缺失」接着补）
		return true
	case jobKindProbe:
		// 探测逐个条目，正在请求的那一集探完就停
		return true
	case jobKindPerson:
		// 逐个人物处理，手上这个写完就停
		return true
	case "orgpick":
		// 网盘文件页勾选的一批：整理逐个条目，两个之间能停
		if f := decodeJobParams(job).Files; f != nil {
			return len(f.Items) > 1
		}
		return false
	}
	return false
}

// ---- 完成回调 ----
//
// 机器人指令入队后要在跑完时回一句话。回调只存在内存里：服务重启后排队中的任务照常执行，
// 只是不会再回复（任务历史里看得到结果）

var (
	jobHooksMu sync.Mutex
	jobHooks   = map[uint][]func(model.TaskJob){}
)

// onJobDone 登记任务结束（完成 / 失败 / 取消）时的回调
func onJobDone(id uint, fn func(model.TaskJob)) {
	jobHooksMu.Lock()
	jobHooks[id] = append(jobHooks[id], fn)
	jobHooksMu.Unlock()
}

func fireJobHooks(db *gorm.DB, id uint) {
	jobHooksMu.Lock()
	fns := jobHooks[id]
	delete(jobHooks, id)
	jobHooksMu.Unlock()
	if len(fns) == 0 {
		return
	}
	var job model.TaskJob
	if db.First(&job, id).Error != nil {
		return
	}
	for _, fn := range fns {
		fn(job)
	}
}

// jobExecutor 执行一个任务。返回 error = 失败，error 文本即失败原因
type jobExecutor func(h *Handler, job *model.TaskJob) (jobOutcome, error)

// jobExecutors 各类型任务的执行器（见 taskjobs.go）。做成变量是为了测试能换成假执行器
var jobExecutors = map[string]jobExecutor{}

// jobQueueMu 入队去重与「取下一个」互斥：否则两次并发提交同一条记录会各建一行
var jobQueueMu sync.Mutex

// jobWakes 每条队列一个唤醒信号
var jobWakes = map[*jobLane]chan struct{}{
	mainLane:   make(chan struct{}, 1),
	scrapeLane: make(chan struct{}, 1),
	probeLane:  make(chan struct{}, 1),
	personLane: make(chan struct{}, 1),
}

// jobKindScrape 走刮削队列的任务类型
const jobKindScrape = "scrape"

// laneOfKind 任务类型 → 队列
func laneOfKind(kind string) *jobLane {
	switch kind {
	case jobKindScrape, jobKindMetaFill:
		// 媒体信息补全只扫描、几十秒，扫完建的刮削任务正好排在它后面
		return scrapeLane
	case jobKindProbe:
		return probeLane
	case jobKindPerson:
		return personLane
	}
	return mainLane
}

// laneWhere 取某条队列任务的查询条件
func laneWhere(db *gorm.DB, l *jobLane) *gorm.DB {
	switch l {
	case scrapeLane:
		return db.Where("kind IN ?", []string{jobKindScrape, jobKindMetaFill})
	case probeLane:
		return db.Where("kind = ?", jobKindProbe)
	case personLane:
		return db.Where("kind = ?", jobKindPerson)
	}
	return db.Where("kind NOT IN ?", []string{jobKindScrape, jobKindMetaFill, jobKindProbe, jobKindPerson})
}

func wakeJobWorker() {
	for _, ch := range jobWakes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// enqueueJob 入队。同 DedupeKey、同类型已有排队中的任务 → 覆盖它的参数与标题
// （同一条记录改了几次指定，以最后一次为准；连点两次「全量同步」只排一次），不新增；
// 已经在跑的不受影响，新任务排在后面。类型不同（排着重新整理又点了深度删除）不合并，依次执行
func enqueueJob(db *gorm.DB, spec jobSpec) (model.TaskJob, error) {
	params, _ := json.Marshal(spec.Params)
	jobQueueMu.Lock()
	defer jobQueueMu.Unlock()

	var job model.TaskJob
	if spec.DedupeKey != "" &&
		db.Where("dedupe_key = ? AND kind = ? AND status = ?", spec.DedupeKey, spec.Kind, jobQueued).First(&job).Error == nil {
		if spec.Priority > job.Priority {
			// 排着一个手动整理、定时整理又命中了：手动的那个已经涵盖，原样保留（标题、参数都不动）
			return job, nil
		}
		prev := decodeJobParams(&job)
		job.Kind, job.Title, job.Params = spec.Kind, spec.Title, string(params)
		if spec.Merge != nil {
			merged, title := spec.Merge(prev, spec.Params)
			b, _ := json.Marshal(merged)
			job.Params, job.Title = string(b), title
		}
		if spec.Priority < job.Priority {
			// 排着一个定时整理、用户又手动点了整理：合并成一个，按手动的优先级排
			job.Priority, job.Source = spec.Priority, spec.Source
		}
		if err := db.Save(&job).Error; err != nil {
			return job, err
		}
		wakeJobWorker()
		return job, nil
	}
	job = model.TaskJob{
		Kind: spec.Kind, Title: spec.Title, DedupeKey: spec.DedupeKey,
		Source: spec.Source, Priority: spec.Priority, Params: string(params), Status: jobQueued,
	}
	if err := db.Create(&job).Error; err != nil {
		return job, err
	}
	wakeJobWorker()
	return job, nil
}

// queuedJobs 所有排队中的任务，按执行顺序
func queuedJobs(db *gorm.DB) []model.TaskJob {
	var rows []model.TaskJob
	db.Where("status = ?", jobQueued).Order("priority ASC, id ASC").Find(&rows)
	return rows
}

// nextQueuedJob 某条队列下一个该执行的任务
func nextQueuedJob(db *gorm.DB, l *jobLane) (model.TaskJob, bool) {
	jobQueueMu.Lock()
	defer jobQueueMu.Unlock()
	var job model.TaskJob
	err := laneWhere(db, l).Where("status = ?", jobQueued).Order("priority ASC, id ASC").First(&job).Error
	return job, err == nil
}

// jobItems 任务涉及几条记录（算预计耗时用）
func jobItems(job *model.TaskJob) int {
	if n := len(decodeJobParams(job).RecordIDs); n > 0 {
		return n
	}
	return 1
}

// queuePosition 任务在它那条队列里排第几（从 1 起）与预计多久后跑完（含它自己）；不在排队返回 0。
// 两条队列各排各的：排着的刮削不挡整理，算位置时也不该把它们混在一起数
func queuePosition(queued []model.TaskJob, id uint) (int, time.Duration) {
	var lane *jobLane
	for i := range queued {
		if queued[i].ID == id {
			lane = laneOfKind(queued[i].Kind)
			break
		}
	}
	if lane == nil {
		return 0, 0
	}
	items, pos := 0, 0
	for i := range queued {
		if laneOfKind(queued[i].Kind) != lane {
			continue
		}
		pos++
		items += jobItems(&queued[i])
		if queued[i].ID == id {
			return pos, time.Duration(items) * jobItemEstimate
		}
	}
	return 0, 0
}

// ---- worker ----

// lastIncrRun 增量轮询上一次真正跑完的时间（UnixNano）。worker 据此判断要不要让路（§3.5）
var lastIncrRun atomic.Int64

func markIncrRun() { lastIncrRun.Store(time.Now().UnixNano()) }

// StartTaskWorker 启动任务队列 worker。启动前把上次没跑完的任务标成「中断」
func StartTaskWorker(h *Handler) {
	recoverInterruptedJobs(h.DB)
	markIncrRun() // 刚启动时别立刻判定「增量很久没跑」
	go h.taskWorkerLoop(mainLane)
	go h.taskWorkerLoop(scrapeLane)
	go h.taskWorkerLoop(probeLane)
	go h.taskWorkerLoop(personLane)
	if n := len(queuedJobs(h.DB)); n > 0 {
		log.Printf("[队列] ○ 启动时有 %d 个排队中的任务，稍后依次执行", n)
	}
}

// recoverInterruptedJobs 服务重启时还标着 running 的任务一律改成 interrupted。
// **不自动重跑**：中断点可能在搬移之后、落盘之前，重跑之前应该让用户看一眼。
// 排队中的保留：它们还没开始，不存在中间态
func recoverInterruptedJobs(db *gorm.DB) {
	now := time.Now()
	res := db.Model(&model.TaskJob{}).Where("status = ?", jobRunning).Updates(map[string]interface{}{
		"status": jobInterrupted, "finished_at": now,
		"message": "服务重启时中断，请检查该条目的整理记录后重新提交",
	})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[队列] ⚠ %d 个任务在上次退出时被中断，已标记，不会自动重跑", res.RowsAffected)
	}
}

func (h *Handler) taskWorkerLoop(l *jobLane) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		job, ok := nextQueuedJob(h.DB, l)
		if !ok {
			select {
			case <-jobWakes[l]:
			case <-stopCh:
				return
			case <-time.After(jobIdlePoll):
			}
			continue
		}
		h.runJob(&job, l)
		// 让路窗口只有主队列需要：刮削队列不拿 taskMu，挡不住增量
		if l == mainLane && len(queuedJobs(h.DB)) > 0 {
			waitIncrWindow(h.loadIncrInterval())
		}
	}
}

// acquireForJob 等锁。等不到不算失败：队列里的任务就是要执行的，等后台任务跑完即可。
// 每等一段查一眼任务还在不在队列里（等锁期间可能被用户取消）
func acquireForJob(db *gorm.DB, job *model.TaskJob) bool {
	for {
		select {
		case <-stopCh:
			return false
		default:
		}
		var st model.TaskJob
		if db.Select("status").First(&st, job.ID).Error != nil || st.Status != jobQueued {
			return false
		}
		if taskMu.Acquire(job.Title, jobAcquireSlice) {
			return true
		}
	}
}

// runJob 执行一个任务：拿锁（仅主队列）→ 翻成 running → 执行 → 写结果
func (h *Handler) runJob(job *model.TaskJob, l *jobLane) {
	if l == mainLane {
		if !acquireForJob(h.DB, job) {
			return
		}
		defer taskMu.Unlock()
	}

	// 抢到锁后再原子地从 queued 翻成 running：等锁期间被取消的在这里落空
	started := time.Now()
	res := h.DB.Model(&model.TaskJob{}).Where("id = ? AND status = ?", job.ID, jobQueued).
		Updates(map[string]interface{}{"status": jobRunning, "started_at": started})
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	job.Status, job.StartedAt = jobRunning, &started
	log.Printf("[队列] ▶ 开始：%s", job.Title)

	// beginTask / endTask 是 taskMu 持有者的全局状态（机器人「状态」、后台历史），刮削队列不碰
	if l == mainLane {
		beginTask(job.Title)
	}
	l.begin(job.ID)
	out, err := execJob(h, job)
	if l == mainLane {
		if err != nil {
			failTask(err)
		}
		endTask() // 要在清当前任务之前：endTask 据此知道这是队列任务、不另记一行后台历史
	}
	prog := l.end()

	finished := time.Now()
	status, msg := jobSuccess, out.Message
	switch {
	case err != nil:
		status, msg = jobFailed, err.Error()
	case out.Canceled:
		status = jobCanceled
	case out.Partial:
		status = jobPartial
	}
	progJSON, _ := json.Marshal(prog)
	upd := map[string]interface{}{
		"status": status, "message": truncateStr(msg, 480), "progress": string(progJSON), "finished_at": finished,
	}
	if out.Result != nil {
		if b, e := json.Marshal(out.Result); e == nil {
			upd["result"] = string(b)
		}
	}
	defer fireJobHooks(h.DB, job.ID)
	if out.Idle && status == jobSuccess {
		// 空转的后台轮次不留行：否则 10 分钟一条「什么都没干」把面板和历史刷满
		h.DB.Delete(&model.TaskJob{}, job.ID)
		vlog("[队列] ○ 空转，不留记录：%s", job.Title)
		return
	}
	h.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Updates(upd)
	if status == jobFailed && job.Priority == jobPriorityBackground {
		// 后台任务同一个原因反复失败（没配待整理目录、115 掉线 …）只留最新一条，
		// 否则定时整理每 10 分钟添一行一模一样的失败
		h.DB.Where("id <> ? AND kind = ? AND title = ? AND status = ? AND message = ?",
			job.ID, job.Kind, job.Title, jobFailed, upd["message"]).Delete(&model.TaskJob{})
	}
	elapsed := finished.Sub(started).Truncate(time.Second)
	switch status {
	case jobSuccess:
		log.Printf("[队列] ✓ 完成（%s）：%s", elapsed, job.Title)
	case jobPartial:
		log.Printf("[队列] ⚠ 部分失败（%s）：%s - %s", elapsed, job.Title, msg)
	case jobCanceled:
		log.Printf("[队列] ○ 已停止（%s）：%s - %s", elapsed, job.Title, msg)
	default:
		log.Printf("[队列] ✗ 失败（%s）：%s - %s", elapsed, job.Title, msg)
	}
}

// execJob 调执行器；panic 兜底成失败，别让一个任务把 worker 整条协程带走
func execJob(h *Handler, job *model.TaskJob) (out jobOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[队列] ✗ 任务 panic 已恢复: %v", r)
			err = fmt.Errorf("任务异常中止: %v", r)
		}
	}()
	exec := jobExecutors[job.Kind]
	if exec == nil {
		return out, fmt.Errorf("不支持的任务类型 %q", job.Kind)
	}
	return exec(h, job)
}

// needIncrWindow 增量是否已经太久没跑（超过两个轮询周期）
func needIncrWindow(now, last time.Time, interval time.Duration) bool {
	return interval > 0 && now.Sub(last) > 2*interval
}

// waitIncrWindow 让路窗口：增量不能被队列饿死。
//
// worker 在等锁时登记为等待者，增量轮询 TryLockPolite 看到有人排队就不抢 ——
// 一批 50 条重新整理跑下来，增量会整段停摆。所以每跑完一个任务，worker 放锁、
// 不登记等待，若增量已超过两个周期没跑，就等到它跑完一轮（或等满一个周期多一点）再继续
func waitIncrWindow(interval time.Duration) {
	last := lastIncrRun.Load()
	if !needIncrWindow(time.Now(), time.Unix(0, last), interval) {
		return
	}
	wait := interval + 15*time.Second
	if wait > incrWindowMax {
		wait = incrWindowMax
	}
	vlog("[队列] ○ 增量已 %s 没跑，先让它跑一轮再继续（最多等 %s）",
		time.Since(time.Unix(0, last)).Truncate(time.Second), wait)
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if lastIncrRun.Load() != last {
			return
		}
		select {
		case <-stopCh:
			return
		case <-time.After(incrWindowPoll):
		}
	}
}

// pruneTaskJobs 清理已结束的任务（随每日清理一次）：成功 / 取消留 7 天，部分失败 / 失败 / 中断留 30 天
func pruneTaskJobs() {
	if model.DB == nil {
		return
	}
	now := time.Now()
	res1 := model.DB.Where("status IN ? AND created_at < ?", []string{jobSuccess, jobCanceled}, now.AddDate(0, 0, -7)).
		Delete(&model.TaskJob{})
	res2 := model.DB.Where("status IN ? AND created_at < ?", []string{jobPartial, jobFailed, jobInterrupted}, now.AddDate(0, 0, -30)).
		Delete(&model.TaskJob{})
	if n := res1.RowsAffected + res2.RowsAffected; n > 0 {
		log.Printf("[系统] ○ 清理 %d 条已结束的队列任务", n)
	}
}

// ---- HTTP ----

// taskJobDTO 列表返回体：运行中的带实时进度，排队中的带位置与预计耗时
type taskJobDTO struct {
	model.TaskJob
	RecordIDs []uint          `json:"record_ids,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Probe     *jobProbeReport `json:"probe,omitempty"` // 任务结束后排进 Emby 提前探测的结果（embyprobereport.go）
	Follows   []jobFollow     `json:"follows,omitempty"`
	Stoppable bool            `json:"stoppable,omitempty"`
	Progress  *jobProgress    `json:"progress,omitempty"`
	Position  int             `json:"position,omitempty"`
	EtaSec    int             `json:"eta_sec,omitempty"`
}

func toJobDTO(job model.TaskJob, queued []model.TaskJob) taskJobDTO {
	d := taskJobDTO{TaskJob: job, RecordIDs: decodeJobParams(&job).RecordIDs}
	if job.Result != "" && json.Valid([]byte(job.Result)) {
		d.Result = json.RawMessage(job.Result)
	}
	d.Probe = probeReportOf(&job)
	d.Follows = jobFollowsOf(d.Result)
	d.Stoppable = job.Status == jobQueued || (job.Status == jobRunning && jobStoppable(&job))
	switch job.Status {
	case jobRunning:
		if p, ok := liveJobProgress(job.ID); ok {
			d.Progress = &p
		}
	case jobQueued:
		pos, eta := queuePosition(queued, job.ID)
		d.Position, d.EtaSec = pos, int(eta.Seconds())
	default:
		if job.Progress != "" {
			var p jobProgress
			if json.Unmarshal([]byte(job.Progress), &p) == nil {
				d.Progress = &p
			}
		}
	}
	return d
}

// jobFollow 任务结束时另建的后续任务（手动刮削 → Emby 提前探测；媒体信息补全 → 刮削 + 探测）。
// 刮削自己确实完了，但探测多半还在跑：列表里只写「完成」，用户会以为整件事做完了（2026-09-30 反馈）
type jobFollow struct {
	ID     uint   `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// jobFollowsOf 结果里带 follow_job / follow_jobs 的，查一下那些任务现在的状态（任务已被清理的不返回：
// 后台建的刮削什么都没写时整行删掉）
func jobFollowsOf(result json.RawMessage) []jobFollow {
	if len(result) == 0 || model.DB == nil {
		return nil
	}
	var r struct {
		FollowJob  uint   `json:"follow_job"`
		FollowJobs []uint `json:"follow_jobs"`
	}
	if json.Unmarshal(result, &r) != nil {
		return nil
	}
	ids := r.FollowJobs
	if r.FollowJob != 0 {
		ids = append([]uint{r.FollowJob}, ids...)
	}
	var out []jobFollow
	for _, id := range ids {
		var j model.TaskJob
		if id == 0 || model.DB.Select("id", "kind", "title", "status").First(&j, id).Error != nil {
			continue
		}
		out = append(out, jobFollow{ID: j.ID, Kind: j.Kind, Title: j.Title, Status: j.Status})
	}
	return out
}

// queuedReply 入队接口的统一回复：202 + 任务 id、排第几、大概要等多久
func (h *Handler) queuedReply(c *gin.Context, job model.TaskJob, what string) {
	pos, eta := queuePosition(queuedJobs(h.DB), job.ID)
	msg := what + "已加入任务队列"
	if pos > 1 {
		msg += fmt.Sprintf("（第 %d 位）", pos)
	}
	if eta >= time.Minute {
		msg += fmt.Sprintf("，预计约 %d 分钟", int(eta.Round(time.Minute).Minutes()))
	}
	c.JSON(http.StatusAccepted, gin.H{"message": msg, "job_id": job.ID, "position": pos, "eta_sec": int(eta.Seconds())})
}

// ListTaskJobs GET /tasks?limit=  → 运行中 + 全部排队中 + 最近结束的 limit 条（默认 30）
func (h *Handler) ListTaskJobs(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	if limit < 1 || limit > 200 {
		limit = 30
	}
	queued := queuedJobs(h.DB)
	var running []model.TaskJob
	h.DB.Where("status = ?", jobRunning).Order("id ASC").Find(&running)
	var done []model.TaskJob
	h.DB.Where("status IN ?", jobFinishedStatuses).Order("id DESC").Limit(limit).Find(&done)

	items := make([]taskJobDTO, 0, len(running)+len(queued)+len(done))
	for _, j := range running {
		items = append(items, toJobDTO(j, queued))
	}
	for _, j := range queued {
		items = append(items, toJobDTO(j, queued))
	}
	for _, j := range done {
		items = append(items, toJobDTO(j, queued))
	}
	// 锁占用方：排队的任务在等谁（后台整理 / 增量），面板上要说清楚
	lock := gin.H{"busy": false}
	if owner, dur, ok := taskMu.Holder(); ok {
		lock = gin.H{"busy": true, "holder": owner, "held_sec": int(dur.Seconds())}
		if _, _, _, prog := TaskStatus(); prog != "" {
			lock["progress"] = prog
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"data": items, "running": len(running), "queued": len(queued), "lock": lock,
	})
}

// GetTaskJob GET /tasks/:id → 任务本身 + 涉及的整理记录（最多 50 条）+ 参数摘要（任务中心详情）
func (h *Handler) GetTaskJob(c *gin.Context) {
	var job model.TaskJob
	if h.DB.First(&job, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	d := taskJobDetail{taskJobDTO: toJobDTO(job, queuedJobs(h.DB)), Summary: jobParamsSummary(decodeJobParams(&job))}
	d.Records, d.RecordTotal = jobRecords(h.DB, &job)
	c.JSON(http.StatusOK, gin.H{"data": d})
}

// CancelTaskJob POST /tasks/:id/cancel：排队中的直接取消；运行中的请求停止（做完手上这条再退）
func (h *Handler) CancelTaskJob(c *gin.Context) {
	var job model.TaskJob
	if h.DB.First(&job, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	switch job.Status {
	case jobQueued:
		now := time.Now()
		res := h.DB.Model(&model.TaskJob{}).Where("id = ? AND status = ?", job.ID, jobQueued).
			Updates(map[string]interface{}{"status": jobCanceled, "message": "已取消（未执行）", "finished_at": now})
		if res.RowsAffected == 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "任务刚刚开始执行，请刷新后再试"})
			return
		}
		fireJobHooks(h.DB, job.ID)
		c.JSON(http.StatusOK, gin.H{"message": "已取消"})
	case jobRunning:
		if !jobStoppable(&job) {
			// 单条的重新整理 / 确认本身就是一条，中途打断只会留下搬了一半的中间态
			c.JSON(http.StatusBadRequest, gin.H{"error": "这个任务执行中无法中途停止，请等它跑完"})
			return
		}
		if !requestJobStop(job.ID) {
			c.JSON(http.StatusConflict, gin.H{"error": "任务已经结束"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "已请求停止，当前这一条处理完即退出"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务已经结束"})
	}
}

// RetryTaskJob POST /tasks/:id/retry：部分失败 / 失败 / 中断 / 取消的按原参数重新入队。
// 部分失败的刮削重试一遍只会补上缺的（除非当初勾了强制覆盖）
func (h *Handler) RetryTaskJob(c *gin.Context) {
	var job model.TaskJob
	if h.DB.First(&job, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if job.Status == jobQueued || job.Status == jobRunning || job.Status == jobSuccess {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有部分失败、失败、中断或已取消的任务可以重试"})
		return
	}
	if jobExecutors[job.Kind] == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "后台任务不能在这里重试，等它下一轮自动运行"})
		return
	}
	if job.Kind == jobKindProbe {
		// 防抖期内重试等于什么都不做（条目全被跳过），直接说清楚要等到什么时候
		if at := probeRetryReadyAt(probeReportOf(&job), time.Now()); at != nil {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf(
				"失败的视频刚请求过探测，%s 之后才能重试（同一视频 %d 分钟内不重复请求，避免重复取 115 直链）",
				at.Format("15:04:05"), int(embyExtractDebounce.Minutes()))})
			return
		}
	}
	params := decodeJobParams(&job)
	if params.Probe != nil {
		// 定时补全建的探测任务按自动规则放行；用户点了重试就是手动要探（与失败清单里的「重试」同口径），
		// 否则失败过的条目 24 小时内全被跳过，重试等于什么都没做
		params.Probe.Auto = false
	}
	nj, err := enqueueJob(h.DB, jobSpec{
		Kind: job.Kind, Title: job.Title, DedupeKey: job.DedupeKey,
		Source: "web", Priority: jobPriorityManual, Params: params,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, nj, "")
}

// ClearTaskJobs POST /tasks/clear：清掉已结束的任务（不影响排队中与运行中的）
func (h *Handler) ClearTaskJobs(c *gin.Context) {
	res := h.DB.Where("status IN ?", jobFinishedStatuses).Delete(&model.TaskJob{})
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清理 %d 个已结束的任务", res.RowsAffected)})
}

// shortTitle 标题里的来源名截短（目录名动辄上百字）
func shortTitle(s string) string {
	return truncateStr(strings.TrimSuffix(s, "/"), 40)
}
