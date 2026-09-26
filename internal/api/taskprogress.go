package api

import (
	"fmt"
	"sync"
)

// ==================== 任务进度（结构化）====================
//
// 此前进度只有一个全局字符串 taskProgress，重新整理全程还一次都没报过 ——
// 用户点完只能看着按钮转圈。现在队列里正在执行的任务有一份结构化进度
// （阶段、完成数/总数、当前条目），队列面板据此画进度条。
//
// 结构借鉴 LitePan internal/strm/progress.go 的 liveScanProgress（只读参考，未复制代码）：
// 进度放内存、列表接口合并返回，结束时才落库一次，不为每一步写数据库。

// jobProgress 当前任务的进度
type jobProgress struct {
	Phase string `json:"phase,omitempty"` // 改名 / 搬移 / 落盘 / 刮削 …
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Label string `json:"label,omitempty"` // 当前条目
}

var (
	curJobMu   sync.Mutex
	curJobID   uint
	curJobProg jobProgress
	curJobStop bool // 用户请求停止（协作式：逐条循环在两条之间查）
)

// progressKeep 传给 setJobProgress 的 done/total 取这个值表示「不改」。
// 0 是合法进度（刚开始），不能拿来当「没传」（LitePan 的哨兵写法）
const progressKeep = -1

func beginJobProgress(id uint) {
	curJobMu.Lock()
	curJobID, curJobProg, curJobStop = id, jobProgress{}, false
	curJobMu.Unlock()
}

func endJobProgress() jobProgress {
	curJobMu.Lock()
	defer curJobMu.Unlock()
	p := curJobProg
	curJobID, curJobProg, curJobStop = 0, jobProgress{}, false
	return p
}

// currentJob 正在执行的任务 id 与进度快照（没有则 id=0）
func currentJob() (uint, jobProgress) {
	curJobMu.Lock()
	defer curJobMu.Unlock()
	return curJobID, curJobProg
}

// setJobProgress 更新当前任务的进度；没有任务在执行时（定时整理、增量轮询）是空操作。
// 同时写一份旧式文字进度，/sync/status 与 TaskStatusBar 照常能看到
func setJobProgress(phase string, done, total int, label string) {
	curJobMu.Lock()
	if curJobID == 0 {
		curJobMu.Unlock()
		return
	}
	if phase != "" {
		curJobProg.Phase = phase
	}
	if done != progressKeep {
		curJobProg.Done = done
	}
	if total != progressKeep {
		curJobProg.Total = total
	}
	curJobProg.Label = label
	p := curJobProg
	curJobMu.Unlock()

	text := p.Phase
	if p.Total > 0 {
		text += fmt.Sprintf(" %d/%d", p.Done, p.Total)
	}
	if p.Label != "" {
		text += "：" + p.Label
	}
	setTaskProgressText(text)
}

// setJobLabel 只改当前条目（SetTaskProgress 的旧调用点走这里，阶段与计数保持不变）
func setJobLabel(label string) {
	curJobMu.Lock()
	if curJobID != 0 {
		curJobProg.Label = label
	}
	curJobMu.Unlock()
}

// requestJobStop 请求停止正在执行的任务 id；不是它就返回 false
func requestJobStop(id uint) bool {
	curJobMu.Lock()
	defer curJobMu.Unlock()
	if curJobID == 0 || curJobID != id {
		return false
	}
	curJobStop = true
	return true
}

// jobStopRequested 逐条循环在两条之间查：返回 true 就做完手上这条后收工。
// 不在单条 115 写操作中途打断，那会留下「网盘已搬、台账未写」的中间态（AGENTS.md §6.3）
func jobStopRequested() bool {
	curJobMu.Lock()
	defer curJobMu.Unlock()
	return curJobStop
}
