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
}

// organizeJobResult 手动整理的结构化结果：前端据此在有待确认条目时跳到记录页
type organizeJobResult struct {
	Success  int `json:"success"`
	Exists   int `json:"exists"`
	Failed   int `json:"failed"`
	Awaiting int `json:"awaiting"`
}

// summarizeOrganize 整理结果 → 计数与一句话（纯函数，便于单测）
func summarizeOrganize(details []OrganizeResult) (organizeJobResult, string) {
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
	if len(details) == 0 {
		return r, "待整理目录里没有需要处理的内容"
	}
	msg := fmt.Sprintf("成功 %d · 已存在 %d · 失败 %d", r.Success, r.Exists, r.Failed)
	if r.Awaiting > 0 {
		msg += fmt.Sprintf(" · 待确认 %d（到整理记录里确认）", r.Awaiting)
	}
	return r, msg
}

func execOrganizeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	_, details, err := h.executeOrganize()
	if err != nil {
		return jobOutcome{}, err
	}
	res, msg := summarizeOrganize(details)
	out := jobOutcome{Message: msg, Result: res}
	if jobStopRequested() {
		out.Canceled = true
		out.Message = "已按要求停止；" + msg
	}
	return out, nil
}

func execFullJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).Sync
	if p == nil || p.Cid == "" {
		return jobOutcome{}, errors.New("任务参数错误：缺少 115 媒体库 cid")
	}
	setJobProgress("全量同步", progressKeep, progressKeep, "")
	sum, err := h.executeFullSync(fullParams{
		Cid: p.Cid, LocalPath: p.LocalPath,
		VideoExt: p.VideoExt, ImageExt: p.ImageExt, DataExt: p.DataExt, Mode: p.Mode,
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
	return jobOutcome{Message: strings.Join(parts, "；"), Result: map[string]any{
		"mode_used": sum.ModeUsed, "scan_complete": sum.ScanComplete, "orphans": sum.Orphans,
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
	return jobOutcome{Message: fmt.Sprintf("事件 %d，视频 %d，新增 STRM %d（已存在 %d），附属下载 %d（%s）",
		sum.EventsTotal, sum.Videos, sum.StrmCreated, sum.StrmExisting, sum.AssetsDownloaded, sum.Elapsed)}, nil
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
