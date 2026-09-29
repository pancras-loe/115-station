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
//
// 队列分两条（taskqueue.go）：主队列（整理 / 同步 …，串行在 taskMu 上）与刮削队列（不拿 taskMu）。
// 两条同时在跑，所以「当前任务」按队列各存一份。主队列沿用下面这组包级函数（五十多个调用点不用改），
// 刮削队列的代码显式用 scrapeLane

// jobProgress 当前任务的进度
type jobProgress struct {
	Phase string `json:"phase,omitempty"` // 改名 / 搬移 / 落盘 / 刮削 …
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Label string `json:"label,omitempty"` // 当前条目
	// Sub 当前条目内部的进度（刮削一部剧：集 NFO 87/212）。顶层进度一变就清掉
	Sub *jobSubProgress `json:"sub,omitempty"`
}

// jobSubProgress 条目内的第二级进度
type jobSubProgress struct {
	Phase string `json:"phase,omitempty"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Label string `json:"label,omitempty"`
}

// jobLane 一条队列的「当前任务」：id、进度、停止请求
type jobLane struct {
	name string
	mu   sync.Mutex
	id   uint
	prog jobProgress
	stop bool // 用户请求停止（协作式：逐条循环在两条之间查）
}

var (
	mainLane   = &jobLane{name: "主队列"}
	scrapeLane = &jobLane{name: "刮削队列"}
	probeLane  = &jobLane{name: "探测队列"} // Emby 提前探测（embyprobejob.go）：只等探测 worker，不拿 taskMu
	jobLanes   = []*jobLane{mainLane, scrapeLane, probeLane}
)

// progressKeep 传给 setJobProgress 的 done/total 取这个值表示「不改」。
// 0 是合法进度（刚开始），不能拿来当「没传」（LitePan 的哨兵写法）
const progressKeep = -1

func (l *jobLane) begin(id uint) {
	l.mu.Lock()
	l.id, l.prog, l.stop = id, jobProgress{}, false
	l.mu.Unlock()
}

func (l *jobLane) end() jobProgress {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.prog
	p.Sub = nil // 结束后的快照只留顶层：「集剧照 87/212」对已结束的任务没有意义
	l.id, l.prog, l.stop = 0, jobProgress{}, false
	return p
}

// current 正在执行的任务 id 与进度快照（没有则 id=0）
func (l *jobLane) current() (uint, jobProgress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.prog
	if p.Sub != nil {
		s := *p.Sub
		p.Sub = &s
	}
	return l.id, p
}

// set 更新顶层进度，返回更新后的快照；没有任务在执行时 ok=false
func (l *jobLane) set(phase string, done, total int, label string) (jobProgress, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.id == 0 {
		return jobProgress{}, false
	}
	if phase != "" {
		l.prog.Phase = phase
	}
	if done != progressKeep {
		l.prog.Done = done
	}
	if total != progressKeep {
		l.prog.Total = total
	}
	l.prog.Label = label
	l.prog.Sub = nil
	return l.prog, true
}

// setSub 更新条目内的第二级进度；phase 为空 = 清掉
func (l *jobLane) setSub(phase string, done, total int, label string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.id == 0 {
		return
	}
	if phase == "" {
		l.prog.Sub = nil
		return
	}
	l.prog.Sub = &jobSubProgress{Phase: phase, Done: done, Total: total, Label: label}
}

func (l *jobLane) requestStop(id uint) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.id == 0 || l.id != id {
		return false
	}
	l.stop = true
	return true
}

// stopRequested 用户点了停止，或服务正在退出
func (l *jobLane) stopRequested() bool {
	select {
	case <-stopCh:
		return true
	default:
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stop
}

// ---- 主队列的包级入口（历史调用点）----

func beginJobProgress(id uint)    { mainLane.begin(id) }
func endJobProgress() jobProgress { return mainLane.end() }

// currentJob 主队列正在执行的任务 id 与进度快照（没有则 id=0）
func currentJob() (uint, jobProgress) { return mainLane.current() }

// liveJobProgress 任务 id 在任一条队列上正在执行时的实时进度
func liveJobProgress(id uint) (jobProgress, bool) {
	for _, l := range jobLanes {
		if cur, p := l.current(); cur != 0 && cur == id {
			return p, true
		}
	}
	return jobProgress{}, false
}

// setJobProgress 更新主队列当前任务的进度；没有任务在执行时（定时整理、增量轮询）是空操作。
// 同时写一份文字进度（TaskStatus）：机器人「状态」指令与队列面板的后台任务行读它
func setJobProgress(phase string, done, total int, label string) {
	p, ok := mainLane.set(phase, done, total, label)
	if !ok {
		return
	}
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
	mainLane.mu.Lock()
	if mainLane.id != 0 {
		mainLane.prog.Label = label
	}
	mainLane.mu.Unlock()
}

// requestJobStop 请求停止正在执行的任务 id（哪条队列上的都行）；不是它就返回 false
func requestJobStop(id uint) bool {
	for _, l := range jobLanes {
		if l.requestStop(id) {
			return true
		}
	}
	return false
}

// jobStopRequested 主队列的逐条循环在两条之间查：返回 true 就做完手上这条后收工。
// 不在单条 115 写操作中途打断，那会留下「网盘已搬、台账未写」的中间态（AGENTS.md §6.3）
func jobStopRequested() bool {
	mainLane.mu.Lock()
	defer mainLane.mu.Unlock()
	return mainLane.stop
}
