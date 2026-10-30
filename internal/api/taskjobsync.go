package api

import (
	"errors"
	"fmt"
	"strings"

	"115-station/internal/model"
)

// ==================== 队列任务：整理 / 全量 / 增量 / 忽略 / 深度删除 ====================
//
// 这几个入口此前都在 HTTP 请求里 taskMu.Acquire 等 20 秒再同步跑完（阶段 3，见
// TASK-QUEUE-PLAN.md §7）。执行体原样复用 executeOrganize / executeFullSync /
// executeIncrementalSync / ignoreAwaiting / deepDeleteRecord，这里只做参数解码与结果措辞

func init() {
	jobExecutors["organize"] = execOrganizeJob
	jobExecutors["full"] = execFullJob
	jobExecutors["incr"] = execIncrJob
	jobExecutors["ignore"] = execIgnoreJob
	jobExecutors["deepdel"] = execDeepDelJob
	jobExecutors["transfer"] = execTransferJob
}

// organizeJobResult 手动整理的结构化结果：前端据此在有待确认条目时跳到记录页
type organizeJobResult struct {
	Success  int `json:"success"`
	Exists   int `json:"exists"`
	Failed   int `json:"failed"`
	Awaiting int `json:"awaiting"`
	Held     int `json:"held,omitempty"` // 本轮因为等确认没动的条目
}

// summarizeOrganize 整理结果 → 计数与一句话（纯函数，便于单测）
func summarizeOrganize(details []OrganizeResult, held []heldSkip) (organizeJobResult, string) {
	var r organizeJobResult
	for _, d := range details {
		switch d.Status {
		case "success":
			r.Success++
		case "exists":
			r.Exists++
		case orgStatusAwaiting:
			r.Awaiting++
		default:
			r.Failed++
		}
	}
	r.Held = len(held)
	if len(details) == 0 {
		if len(held) > 0 {
			return r, "没有新内容可整理；" + heldSkipText(held)
		}
		return r, "待整理目录里没有需要处理的内容"
	}
	msg := fmt.Sprintf("成功 %d · 已存在 %d · 失败 %d", r.Success, r.Exists, r.Failed)
	if r.Awaiting > 0 {
		msg += fmt.Sprintf(" · 待确认 %d（到整理记录里确认）", r.Awaiting)
	}
	if len(held) > 0 {
		msg += "；另有 " + heldSkipText(held)
	}
	return r, msg
}

// noOrganizeLibOutcome 一个本站整理的库都没有（libopts.go）：后台触发的按空转收掉、不留失败记录，
// 手动点的报清楚原因。ok=false 表示照常往下跑
func noOrganizeLibOutcome(job *model.TaskJob) (jobOutcome, error, bool) {
	if !noOrganizeLibNow() {
		return jobOutcome{}, nil, false
	}
	if job.Priority == jobPriorityBackground {
		return jobOutcome{Idle: true, Message: errNoOrganizeLib.Error()}, nil, true
	}
	return jobOutcome{}, errNoOrganizeLib, true
}

func execOrganizeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	if out, err, stop := noOrganizeLibOutcome(job); stop {
		return out, err
	}
	_, details, held, err := h.executeOrganizeHeld()
	if err != nil {
		return jobOutcome{}, err
	}
	res, msg := summarizeOrganize(details, held)
	out := jobOutcome{Message: msg, Result: res}
	if decodeJobParams(job).Scheduled {
		// 定时整理：独立增量轮询关着时在这里串一轮增量（逃生门下的老行为）；
		// 开着的话它每 30 秒自己跑，这里再跑纯属重复请求 115
		fresh := 0
		if h.loadIncrInterval() <= 0 {
			setJobProgress("增量同步", 0, 0, "")
			sum, ierr := h.executeIncrementalSync(h.incrParamsFromConfig())
			if ierr != nil {
				return jobOutcome{}, fmt.Errorf("整理完成（%s），增量同步失败: %w", msg, ierr)
			}
			fresh = sum.EventsFresh
			markIncrRun()
		}
		// 空转：没有任何条目、增量也没有新事件
		out.Idle = len(details) == 0 && fresh == 0
	}
	if jobStopRequested() {
		out.Canceled = true
		out.Message = "已按要求停止；" + msg
	}
	return out, nil
}

func execFullJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).Sync
	if p == nil || (p.Cid == "" && !p.AcctOnly) {
		return jobOutcome{}, errors.New("任务参数错误：缺少 115 媒体库 cid")
	}
	if p.AcctOnly && len(loadAcctLibs()) == 0 {
		return jobOutcome{Idle: true}, nil // 排队期间附加账号的库都被移除了
	}
	setJobProgress("全量同步", progressKeep, progressKeep, "")
	sum, err := h.executeFullSync(fullParams{
		Cid: p.Cid, LocalPath: p.LocalPath,
		VideoExt: p.VideoExt, ImageExt: p.ImageExt, DataExt: p.DataExt, Mode: p.Mode, AcctOnly: p.AcctOnly,
	})
	if err != nil {
		return jobOutcome{}, err
	}
	parts := []string{fmt.Sprintf("视频 %d 个（新增 STRM %d，已存在 %d），附属 %d 个（下载 %d，跳过 %d，失败 %d）",
		sum.Total, sum.Created, sum.Existing, sum.AssetsTotal, sum.AssetsDownloaded, sum.AssetsSkipped, sum.AssetsFailed)}
	// 快速模式失败会被降级，不能让用户以为自己跑的是快速模式
	if p.Mode == "fast" && sum.ModeUsed == "normal" {
		parts = append(parts, "快速模式不可用，已自动降级为标准模式")
	}
	// 只有打开了失效 STRM 检测，「清单不完整」才有后果（跳过标记）；关着时说了只会让人困惑
	if !sum.ScanComplete && h.orphanDetectEnabled() {
		parts = append(parts, "清单不完整，已跳过失效 STRM 标记")
	}
	if sum.Orphans > 0 {
		parts = append(parts, fmt.Sprintf("失效 STRM %d 个待清理", sum.Orphans))
	}
	if sum.Libs > 1 {
		parts = append([]string{fmt.Sprintf("%d 个媒体库", sum.Libs)}, parts...)
	}
	if len(sum.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个库没同步成功（%s）", len(sum.Failed), strings.Join(sum.Failed, "；")))
	}
	return jobOutcome{Message: strings.Join(parts, "；"), Partial: len(sum.Failed) > 0, Result: map[string]any{
		"mode_used": sum.ModeUsed, "scan_complete": sum.ScanComplete, "orphans": sum.Orphans,
		"libs": sum.Libs, "failed": sum.Failed,
	}}, nil
}

func execIncrJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	var p incrParams
	if s := decodeJobParams(job).Sync; s != nil {
		p = normalizeIncrParams(s.Cid, s.LocalPath, s.VideoExt, s.ImageExt, s.DataExt, 0)
	} else {
		p = h.incrParamsFromConfig()
	}
	if p.Cid == "" || p.Cid == "0" {
		return jobOutcome{}, errors.New("未配置 115 媒体库目录")
	}
	setJobProgress("增量同步", progressKeep, progressKeep, "")
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		return jobOutcome{}, err
	}
	markIncrRun() // 手动跑的也算一轮，让路窗口别再为它等
	msg := fmt.Sprintf("事件 %d，视频 %d，新增 STRM %d（已存在 %d），附属下载 %d（%s）",
		sum.EventsTotal, sum.Videos, sum.StrmCreated, sum.StrmExisting, sum.AssetsDownloaded, sum.Elapsed)
	if sum.EventsTotal == 0 {
		msg += incrQueuedRoundsNote(roundHist.busySince(job.CreatedAt), sum.Round)
	}
	return jobOutcome{Message: msg}, nil
}

// incrQueuedRoundsNote 手动增量排队期间，30 秒一轮的自动轮询多半已经把事件拉走处理完了，
// 轮到手动这一轮只剩空转、回报全是 0，和日志里那一轮对不上（2026-10-29 现场：
// 机器人「同步」入队后 #29 新增 1 个 STRM，机器人回「事件 0」）。把那几轮的结果补进回报
func incrQueuedRoundsNote(rounds []incrSummary, self uint64) string {
	var ids []string
	var events, created, assets int
	for _, r := range rounds {
		if r.Round == self || r.EventsPending == 0 {
			continue
		}
		ids = append(ids, fmt.Sprintf("#%d", r.Round))
		events += r.EventsPending
		created += r.StrmCreated
		assets += r.AssetsDownloaded
	}
	if len(ids) == 0 {
		return ""
	}
	return fmt.Sprintf("；排队期间自动轮询（同步%s）已处理：事件 %d，新增 STRM %d，附属下载 %d",
		strings.Join(ids, " "), events, created, assets)
}

// jobRecord 单条记录类任务取记录（执行时重读，不信入队时的快照）
func jobRecord(h *Handler, job *model.TaskJob) (model.OrganizeRecord, error) {
	var rec model.OrganizeRecord
	p := decodeJobParams(job)
	if len(p.RecordIDs) != 1 {
		return rec, errors.New("任务参数错误")
	}
	if h.DB.First(&rec, p.RecordIDs[0]).Error != nil {
		return rec, errors.New("记录已不存在（可能被删除了）")
	}
	return rec, nil
}

func execIgnoreJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	rec, err := jobRecord(h, job)
	if err != nil {
		return jobOutcome{}, err
	}
	if rec.Status != orgStatusAwaiting {
		return jobOutcome{}, errors.New("这条记录已不在待确认状态（排队期间被处理了）")
	}
	setJobProgress("移到冗余", progressKeep, progressKeep, shortTitle(rec.Source))
	msg, err := h.ignoreAwaiting(&rec)
	if err != nil {
		return jobOutcome{}, err
	}
	return jobOutcome{Message: msg}, nil
}

func execDeepDelJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	rec, err := jobRecord(h, job)
	if err != nil {
		return jobOutcome{}, err
	}
	setJobProgress("删除网盘源文件", progressKeep, progressKeep, shortTitle(firstNonEmpty(rec.Title, rec.Source)))
	msg, err := h.deepDeleteRecord(rec)
	if err != nil {
		return jobOutcome{}, err
	}
	return jobOutcome{Message: msg}, nil
}

// execTransferJob 转存 / 离线下载完成后的「整理转存目录 + 增量收尾」
func execTransferJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	if out, err, stop := noOrganizeLibOutcome(job); stop {
		return out, err
	}
	organized, sum, err := h.runTransferOrganize()
	if err != nil {
		return jobOutcome{}, err
	}
	msg := fmt.Sprintf("整理 %d 个条目；增量新增 STRM %d、附属 %d", organized, sum.StrmCreated, sum.AssetsDownloaded)
	return jobOutcome{Message: msg, Idle: organized == 0 && sum.StrmCreated+sum.AssetsDownloaded == 0}, nil
}
