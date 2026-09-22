package api

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ==================== 增量同步的溯源与停滞检测 ====================
//
// 增量是 30 秒一轮的后台循环，出问题时用户看到的现象永远是同一句：
// 「日志里一直在同步，重复了好几次」。而光看日志根本分不出三件事：
//
//  1. 网盘上真的有这么多变化
//  2. 同一批事件**没消费掉**，每轮原样重放（游标不推进、事件永远 pending）
//  3. 一条事件带出了一次**超大范围**的目录遍历
//
// 这三件事的处理办法完全不同，所以必须在日志里直接区分开：
//
//   - 每轮一个轮次号 [同步#N]：同一批内容连着出现在 #41 #42 #43，就是重放
//   - 每轮一条「本轮账单」：事件怎么来的、遍历了几个目录、列了多少次目录、
//     最后有没有消费掉
//   - 连续多轮没消费时主动报警，并说清是哪一类原因卡住的

var incrRoundSeq atomic.Uint64

// incrLog 一轮增量的日志前缀
type incrLog struct {
	id  uint64
	tag string
}

func newIncrRound() incrLog {
	id := incrRoundSeq.Add(1)
	return incrLog{id: id, tag: fmt.Sprintf("[同步#%d]", id)}
}

func (l incrLog) infof(format string, a ...interface{}) {
	log.Printf(l.tag+" "+format, a...)
}

// vlogf 详细日志模式才输出（逐目录遍历这类过程性日志）
func (l incrLog) vlogf(format string, a ...interface{}) {
	if verboseLogging() {
		log.Printf(l.tag+" "+format, a...)
	}
}

// ==================== 停滞（重放）检测 ====================

var (
	incrStallMu     sync.Mutex
	incrStallRounds int
	incrStallSince  time.Time
	incrStallReason string
)

// noteIncrRoundOutcome 记一轮的消费结果。
//
// consumed=false 表示这一轮的事件没被标记已应用、游标也没推进——
// 这在设计上是对的（正确性优先，下轮重来），但**如果原因是永久性的，
// 它就会变成一个每 30 秒重放一次的死循环**。改造前这里没有任何计数，
// 用户只能看着日志反复刷同样的目录，无从判断。
func noteIncrRoundOutcome(lg incrLog, consumed bool, reason string, pending int64) int {
	incrStallMu.Lock()
	defer incrStallMu.Unlock()
	if consumed {
		if incrStallRounds > 0 {
			lg.infof("✓ 积压的网盘变动已消费完（此前连续 %d 轮未消费，起于 %s）",
				incrStallRounds, incrStallSince.Format("01-02 15:04:05"))
		}
		incrStallRounds, incrStallSince, incrStallReason = 0, time.Time{}, ""
		return 0
	}
	if incrStallRounds == 0 {
		incrStallSince = time.Now()
	}
	incrStallRounds++
	incrStallReason = reason
	// 3 轮（约 1.5 分钟）起报，之后每 20 轮（约 10 分钟）复读一次，不刷屏
	if incrStallRounds == 3 || (incrStallRounds > 3 && incrStallRounds%20 == 0) {
		log.Printf("[同步] ⚠⚠ 已连续 %d 轮没能消费掉这批网盘变动（起于 %s，当前积压 %d 条事件）。"+
			"原因：%s。这批事件每轮都会原样重放一遍——日志里反复出现同样的目录就是这么来的。"+
			"到「Strm 管理 → 增量同步」看状态卡，或按日志里的轮次号 [同步#N] 对比两轮内容是否一致",
			incrStallRounds, incrStallSince.Format("01-02 15:04:05"), pending, reason)
	}
	return incrStallRounds
}

// incrStallSnapshot 停滞状态快照（状态页用）
func incrStallSnapshot() (rounds int, since time.Time, reason string) {
	incrStallMu.Lock()
	defer incrStallMu.Unlock()
	return incrStallRounds, incrStallSince, incrStallReason
}

// ==================== 日志里的小标签 ====================

const (
	// incrTargetLogMax 回退遍历目标清单最多逐条列几个，多了只报总数
	incrTargetLogMax = 12
	// incrDeepWalkWarn 一次深遍历扫到这么多目录就告警：
	// 每个目录至少一次列目录请求、每次请求至少 1 秒节流，
	// 200 个目录 ≈ 3 分钟锁不放，这种范围基本不是「一次增量」该干的事
	incrDeepWalkWarn = 200
)

// orRootLabel 空相对路径就是媒体库根，日志里必须说出来 ——
// 打成空字符串的话，「遍历了整个媒体库」这件事在日志里看不见
func orRootLabel(base string) string {
	if base == "" {
		return "(媒体库根)"
	}
	return base
}

func orUnknownPath(abs string) string {
	if abs == "" {
		return "(解析不出)"
	}
	return abs
}
