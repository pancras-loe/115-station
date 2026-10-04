package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"115-station/internal/model"
)

// ==================== 手动 Emby 提前探测任务（kind=probe）====================
//
// 用户明确要探的时候（片目详情「提前探测」、本地文件页刮削勾了「轨道探测」、重新整理带刮削且开着探测、
// 任务中心失败清单里点「重试」），建一个任务放进单独的探测队列（probeLane）：
//   - 任务中心看得到进度（第几集、成功 / 失败几个、熔断暂停到几点），能停，失败了能按原参数重试；
//   - 真正发请求的仍是 embyextract.go 那一个 worker（一次一个、间隔 3 秒、共用记账与熔断），
//     任务只是把路径排进去（带「手动」标记）然后等结果 —— 不另起第二个探测者；
//   - 手动规则：不看自动入口的次数与 24 小时，只防抖（embyExtractDebounce）。
//
// 探测队列不拿 taskMu、也不占刮削队列：一个任务等探测可能要几十分钟（熔断一次就是 30 分钟），
// 放在刮削队列里会把后面的刮削全挡住。

const jobKindProbe = "probe"

func init() { jobExecutors[jobKindProbe] = execProbeJob }

// probeJobParams 探测任务的参数：Emby 路径（片目目录 / .strm），或直接点名的 Emby 条目 id（失败清单重试）
type probeJobParams struct {
	Key   string   `json:"key,omitempty"` // 本地片目 key（片目详情据此找到正在进行的任务）
	Paths []string `json:"paths,omitempty"`
	Items []string `json:"items,omitempty"`
	// Auto 按自动规则放行（定时补全建的任务，metafill.go）：同一条目最多 2 次、间隔 24 小时，
	// 不是手动规则的「只防抖」—— 定时任务每晚都跑，按手动规则就是每晚把探不成的再请求一遍
	Auto bool `json:"auto,omitempty"`
}

type probeJobSpec struct {
	Title, Source, DedupeKey, Key string
	Paths, Items                  []string
	Auto                          bool
	Priority                      int // 零值 = jobPriorityManual
}

// enqueueProbeJob 建一个手动探测任务。同一个片目排着没开始的，合并成一个（DedupeKey 同键覆盖）
func enqueueProbeJob(db *gorm.DB, s probeJobSpec) (model.TaskJob, error) {
	if len(s.Paths)+len(s.Items) == 0 {
		return model.TaskJob{}, errors.New("没有要探测的条目")
	}
	if s.Source == "" {
		s.Source = "web"
	}
	return enqueueJob(db, jobSpec{
		Kind: jobKindProbe, Title: s.Title, DedupeKey: s.DedupeKey, Source: s.Source, Priority: s.Priority,
		Params: jobParams{Probe: &probeJobParams{Key: s.Key, Paths: s.Paths, Items: s.Items, Auto: s.Auto}},
	})
}

// activeProbeJobFor 这个片目有没有排着 / 正在跑的探测任务（片目详情显示「进度在任务中心」用）
func activeProbeJobFor(key string) uint {
	if key == "" || model.DB == nil {
		return 0
	}
	var job model.TaskJob
	if model.DB.Select("id").Where("kind = ? AND dedupe_key = ? AND status IN ?", jobKindProbe, "probe:"+key,
		[]string{jobQueued, jobRunning}).Order("id DESC").First(&job).Error != nil {
		return 0
	}
	return job.ID
}

// probeJobPoll 等探测结果时多久看一次
var probeJobPoll = time.Second // var：测试里调短

func execProbeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).Probe
	if p == nil || len(p.Paths)+len(p.Items) == 0 {
		return jobOutcome{}, errors.New("任务参数错误：没有要探测的条目")
	}
	if _, ok := loadEmbyRefreshCfg(); !ok {
		return jobOutcome{}, errors.New("没有配置 Emby")
	}
	targets := append([]string(nil), p.Paths...)
	for _, id := range p.Items {
		targets = append(targets, embyExtractItemPrefix+id)
	}
	defer forgetEmbyExtractCancel(job.ID)
	defer forgetEmbyExtractAutoJob(job.ID)
	queueEmbyExtractJob(job.ID, p.Auto, targets...)

	stopped, gone := false, 0
	var r jobProbeReport
	for {
		r = probeReportLive(job.ID)
		if r.Paths > 0 && r.Finished >= r.Paths {
			break
		}
		state, label, ahead := embyExtractJobState(job.ID)
		if state == "" {
			// 队列里没有它了、报告却没收尾：worker 出了意外。给两轮宽限（收尾那一下可能正好卡在中间）
			if gone++; gone > 2 {
				break
			}
		} else {
			gone = 0
		}
		if !stopped && probeLane.stopRequested() {
			stopped = true
			cancelEmbyExtractJob(job.ID)
		}
		probeJobProgress(r, state, label, ahead)
		select {
		case <-stopCh:
			return jobOutcome{Message: "服务退出，探测没有做完"}, errors.New("服务退出，探测没有做完")
		case <-time.After(probeJobPoll):
		}
	}
	probeLane.set("", progressKeep, progressKeep, "")

	failed := r.Failed + r.Errors
	msg := probeJobMessage(r, p.Auto)
	switch {
	case stopped:
		return jobOutcome{Message: "已按要求停止；" + msg, Canceled: true}, nil
	case r.Paths == 0 || r.Finished < r.Paths:
		return jobOutcome{}, errors.New("探测没有正常结束（worker 异常），请重试；" + msg)
	case failed > 0 && r.OK == 0:
		return jobOutcome{}, errors.New(msg)
	}
	// 入库后自动建的任务一个都没请求（都有媒体信息 / 自动次数用完）就不进历史：每次入库都留一行空任务是噪音
	idle := job.Source == probeSourceIngest && r.OK+r.Failed+r.Errors+r.Canceled == 0
	return jobOutcome{Message: msg, Partial: failed > 0, Idle: idle}, nil
}

// probeJobProgress 任务进度：请求了几个 / 这次要请求几个、正在探哪一集；还在排队就说前面还有几个片目
func probeJobProgress(r jobProbeReport, state, label string, ahead int) {
	done := r.OK + r.Failed + r.Canceled
	switch {
	case state == "queued":
		probeLane.set("排队", done, r.Planned, fmt.Sprintf("前面还有 %d 个片目在探测", ahead))
	default:
		lbl := label
		if t, ok := embyExtractPausedUntil(); ok {
			lbl = fmt.Sprintf("连续失败，暂停到 %s", t.Format("15:04"))
		}
		probeLane.set("探测", done, r.Planned, lbl)
	}
	sub := fmt.Sprintf("成功 %d · 失败 %d", r.OK, r.Failed+r.Errors)
	if r.Held > 0 {
		sub += fmt.Sprintf(" · 跳过 %d", r.Held)
	}
	probeLane.setSub(fmt.Sprintf("片目 %d/%d", r.Finished, r.Paths), 0, 0, sub)
}

// probeJobMessage 结果一句话
func probeJobMessage(r jobProbeReport, autoRule bool) string {
	var parts []string
	if r.OK+r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("请求探测 %d 个视频：成功 %d、失败 %d", r.OK+r.Failed, r.OK, r.Failed))
	}
	if r.Held > 0 {
		if autoRule {
			parts = append(parts, fmt.Sprintf("%d 个自动探测次数已用完或 24 小时内请求过，跳过", r.Held))
		} else {
			parts = append(parts, fmt.Sprintf("%d 个刚请求过，防抖跳过", r.Held))
		}
	}
	if r.Canceled > 0 {
		parts = append(parts, fmt.Sprintf("%d 个因停止没探", r.Canceled))
	}
	if r.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d 个片目 Emby 里查不到", r.Missing))
	}
	if r.Errors > 0 {
		parts = append(parts, fmt.Sprintf("%d 处出错", r.Errors))
	}
	if r.OK+r.Failed == 0 && r.Held == 0 && r.Missing == 0 && r.Errors == 0 && r.Canceled == 0 {
		return "全部视频都已有媒体信息，没有需要探测的"
	}
	return strings.Join(parts, "；")
}

// ---- 失败清单里的「重试」 ----

// RetryEmbyProbe POST /tasks/probe/retry {item_ids}：对记账里没成功的条目建一个手动探测任务
func (h *Handler) RetryEmbyProbe(c *gin.Context) {
	var req struct {
		ItemIDs []string `json:"item_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ItemIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if _, ok := loadEmbyRefreshCfg(); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有配置 Emby"})
		return
	}
	if len(req.ItemIDs) > probeFailListMax {
		req.ItemIDs = req.ItemIDs[:probeFailListMax]
	}
	var marks []model.EmbyExtractMark
	h.DB.Where("item_id IN ?", req.ItemIDs).Find(&marks)
	now, running := time.Now(), embyExtractRunningID()
	var ids []string
	var soonest *time.Time
	label := ""
	for i := range marks {
		m := marks[i]
		st := embyProbeStateOf(false, true, &m, embyExtractQueuedItem(m.ItemID), m.ItemID == running, now)
		if !st.manualOK() {
			if st.ManualAt != nil && (soonest == nil || st.ManualAt.Before(*soonest)) {
				soonest = st.ManualAt
			}
			continue
		}
		ids = append(ids, m.ItemID)
		label = m.Label
	}
	if len(ids) == 0 {
		msg := "这些条目正在探测或已经不在失败清单里了"
		if soonest != nil {
			msg = fmt.Sprintf("刚请求过探测，%s 之后才能再请求（防抖 %d 分钟，避免重复取 115 直链）",
				soonest.Format("15:04:05"), int(embyExtractDebounce.Minutes()))
		}
		c.JSON(http.StatusConflict, gin.H{"error": msg})
		return
	}
	title := fmt.Sprintf("Emby 提前探测：重试 %d 个视频", len(ids))
	if len(ids) == 1 && label != "" {
		title = fmt.Sprintf("Emby 提前探测：重试 %s", truncateStr(label, 60))
	}
	job, err := enqueueProbeJob(h.DB, probeJobSpec{Title: title, Source: "web", Items: ids})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "探测任务")
}

// embyExtractQueuedItem 这个条目是不是已经被点名排着（item:id 形式，只有手动任务会这样排）
func embyExtractQueuedItem(id string) string {
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.queued[embyExtractItemPrefix+id] != nil {
		return probeQueuedManual
	}
	return probeQueuedNone
}

// ---- 重新整理后的手动探测 ----
//
// 重新整理（带刮削且开着探测）是用户这一次明确的动作，按手动规则探。但它要等 Emby 把新落盘的
// 片目扫进去才探得了：先把片目登记在这里，等入库确认（embyVerifyIngest）确认到这个片目时，
// 把它从自动入口里摘出来，另建一个探测任务。原地刷新（没有新写出 STRM、不会触发入库确认）的直接建任务

type manualProbeIntent struct {
	title string
	until time.Time
	// files 非空 = 只探这几个 .strm（网盘文件页「指定季集」）：入库确认碰到它们时只为它们建任务，
	// 不整部探。这种登记命中后不删、留到过期（fired 防重复建任务）：同一批文件的入库确认还会再来
	// （迟到的 Emby 入库事件），删了就落进自动入口，被归到片目整部探（2026-10-04 现场：改 3 集，
	// 「重新整理后」与「入库后」两个任务各排了蜡笔小新整部 1568 集）
	files []string
	fired bool
}

var manualProbeIntents = struct {
	sync.Mutex
	m map[string]manualProbeIntent // Emby 片目目录（或「files:」+ 文件清单）→ 登记
}{m: map[string]manualProbeIntent{}}

// manualProbeIntentTTL 登记多久有效：入库确认要排在刮削之后，刮削队列里排着别的任务时可能要等一阵
const manualProbeIntentTTL = 6 * time.Hour

// registerRedoProbe 重新整理完登记这个片目。landed = 这次写出了新 STRM（会有入库确认）；
// 没写出新 STRM 的原地刷新 Emby 里本来就有它，直接建任务
func registerRedoProbe(localTitleDir, title string, landed bool) {
	cfg, ok := loadEmbyRefreshCfg()
	if !ok || localTitleDir == "" {
		return
	}
	dir := embyPathOf(cfg, localTitleDir)
	if !landed {
		enqueueRedoProbe(dir, title)
		return
	}
	manualProbeIntents.Lock()
	manualProbeIntents.m[dir] = manualProbeIntent{title: title, until: time.Now().Add(manualProbeIntentTTL)}
	manualProbeIntents.Unlock()
}

// registerRedoProbeFiles 指定季集完登记这几个 .strm（本地路径）：入库确认到它们时只探它们。
// landed=false（没写出新 STRM，不会有入库确认）直接建任务
func registerRedoProbeFiles(localFiles []string, title string, landed bool) {
	cfg, ok := loadEmbyRefreshCfg()
	if !ok || len(localFiles) == 0 {
		return
	}
	files := make([]string, 0, len(localFiles))
	for _, f := range localFiles {
		files = append(files, embyPathOf(cfg, f))
	}
	sort.Strings(files)
	if !landed {
		enqueueRedoProbePaths("probe-redo:files:"+strings.Join(files, "|"), title, files)
		return
	}
	manualProbeIntents.Lock()
	manualProbeIntents.m["files:"+strings.Join(files, "|")] = manualProbeIntent{
		title: title, until: time.Now().Add(manualProbeIntentTTL), files: files}
	manualProbeIntents.Unlock()
}

func enqueueRedoProbe(embyDir, title string) {
	enqueueRedoProbePaths("probe-redo:"+embyDir, title, []string{embyDir})
}

func enqueueRedoProbePaths(dedupe, title string, paths []string) {
	if model.DB == nil {
		return
	}
	job, err := enqueueProbeJob(model.DB, probeJobSpec{
		Title:     fmt.Sprintf("Emby 提前探测《%s》（重新整理后）", truncateStr(title, 60)),
		Source:    "redo",
		DedupeKey: truncateStr(dedupe, 240),
		Paths:     paths,
	})
	if err != nil {
		log.Printf("[Emby探测] ✗ 重新整理后的探测任务入队失败: %v", err)
		return
	}
	log.Printf("[Emby探测] ○ 重新整理后按手动规则探测《%s》（任务 #%d）", title, job.ID)
}

// splitRedoProbes 入库确认到的路径里，属于登记过的重新整理片目的摘出来（各建一个探测任务），其余照常走自动入口
func splitRedoProbes(embyPaths []string) (auto []string) {
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/") }
	now := time.Now()
	manualProbeIntents.Lock()
	hit := map[string]manualProbeIntent{}
	for dir, in := range manualProbeIntents.m {
		if now.After(in.until) {
			delete(manualProbeIntents.m, dir)
		}
	}
	var fileHits []manualProbeIntent
	firedNow := map[string]bool{}
	for _, p := range embyPaths {
		matched := false
		for key, in := range manualProbeIntents.m {
			if len(in.files) > 0 {
				for _, f := range in.files {
					if embyPathRelated(norm(f), norm(p)) {
						matched = true
						break
					}
				}
				if matched {
					if !in.fired && !firedNow[key] {
						firedNow[key] = true
						fileHits = append(fileHits, in)
					}
					break
				}
				continue
			}
			if embyPathRelated(norm(key), norm(p)) {
				hit[key], matched = in, true
				break
			}
		}
		if !matched {
			auto = append(auto, p)
		}
	}
	for dir := range hit {
		delete(manualProbeIntents.m, dir)
	}
	for key := range firedNow {
		in := manualProbeIntents.m[key]
		in.fired = true
		manualProbeIntents.m[key] = in
	}
	manualProbeIntents.Unlock()
	for dir, in := range hit {
		enqueueRedoProbe(dir, in.title)
	}
	for _, in := range fileHits {
		enqueueRedoProbePaths("probe-redo:files:"+strings.Join(in.files, "|"), in.title, in.files)
	}
	return auto
}

// titleLocalDir 片目在本地媒体库里的绝对路径
func (s *orgSink) titleLocalDir(rootRel string) string {
	return filepath.Join(s.localRoot, filepath.FromSlash(s.libRel(rootRel)))
}

// probeSourceIngest 入库确认后自动建的探测任务（TaskJob.Source）
const probeSourceIngest = "ingest"

// enqueueIngestProbes 入库确认后的自动探测：一个片目一个任务、按自动规则放行（同一条目最多 2 次、间隔 24 小时）。
// 此前直接把路径塞进 worker、不挂任务 —— 一部 1665 集的番剧要探一个多小时，任务中心里看不到进度、
// 也没地方停（2026-10-04 现场）。放行规则不变，只是多了一个看得见、停得下的壳。
// 建不了任务（没库 / 入队失败）退回原来的无任务入口，探测本身不能因此丢掉
func enqueueIngestProbes(targets []string) {
	for _, p := range targets {
		if model.DB == nil {
			queueEmbyExtract(p)
			continue
		}
		name := embyPathBase(strings.TrimPrefix(p, embyExtractItemPrefix))
		job, err := enqueueProbeJob(model.DB, probeJobSpec{
			Title:     fmt.Sprintf("Emby 提前探测《%s》（入库后）", truncateStr(name, 60)),
			Source:    probeSourceIngest,
			DedupeKey: "probe-ingest:" + p,
			Paths:     []string{p},
			Auto:      true,
			Priority:  jobPriorityBackground,
		})
		if err != nil {
			log.Printf("[Emby探测] ✗ 入库后的探测任务入队失败，改走后台队列（任务中心看不到进度）: %v", err)
			queueEmbyExtract(p)
			continue
		}
		log.Printf("[Emby探测] ○ 入库后自动探测《%s》（任务 #%d，自动规则，任务中心可看进度、可停止）", name, job.ID)
	}
}
