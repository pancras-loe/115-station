package api

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ==================== 任务互斥锁（带持有者与让路）====================
//
// 增量同步、自动整理、全量同步、洗版、深度删除全部串行在这一把锁上：
// 它们动的是同一棵 115 目录树和同一棵本地 STRM 树，并发跑必然互相打架。
//
// 改造前这里是一把裸 sync.Mutex，各处 TryLock 抢：谁抢到算谁的，抢不到的
//   - 转存触发：直接丢弃，不重试
//   - 转存守望者：罚 5 分钟冷却
//   - 手动点按钮：弹一句无信息量的「任务正在进行中」
//
// 增量提频到 30 秒、而一轮增量又可能递归遍历整棵分类树跑十几分钟之后，
// 这个模型的结果是整理被饿死。用户侧的现象是「转存完几个小时都不入库」
// 「手动点整理永远提示有任务在跑」——一轮增量 R 分钟只留 30 秒空窗，
// 5 分钟重试一次撞上空窗的概率只有个位数百分比。
//
// 现在这把锁多了两样东西：
//
//   - **持有者**：谁在跑、跑了多久。抢不到时日志与接口报错都能说清在等谁
//   - **让路**：等待方登记让路请求，持有方（目前是增量的目录遍历）在循环里
//     主动查询并提前收工。增量是幂等的、没消费完的事件下一轮原样重来，
//     让它给整理让路的代价最小

// syncLock 带持有者信息与让路信号的互斥锁
type syncLock struct {
	sem chan struct{} // 容量 1 的信号量：用 channel 才能做带超时的等待

	mu      sync.Mutex
	owner   string
	since   time.Time
	waiters []string  // 正在排队的任务名（= 让路信号）
	yieldAt time.Time // 最早一个等待者开始等的时间
}

func newSyncLock() *syncLock { return &syncLock{sem: make(chan struct{}, 1)} }

// taskMu 全局任务互斥锁
var taskMu = newSyncLock()

func (l *syncLock) take(owner string) {
	l.mu.Lock()
	l.owner, l.since = owner, time.Now()
	l.mu.Unlock()
}

func (l *syncLock) addWaiter(owner string) {
	l.mu.Lock()
	if len(l.waiters) == 0 {
		l.yieldAt = time.Now()
	}
	l.waiters = append(l.waiters, owner)
	l.mu.Unlock()
}

func (l *syncLock) removeWaiter(owner string) {
	l.mu.Lock()
	for i, w := range l.waiters {
		if w == owner {
			l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
			break
		}
	}
	if len(l.waiters) == 0 {
		l.yieldAt = time.Time{}
	}
	l.mu.Unlock()
}

// TryLock 立刻抢一次，抢不到不等
func (l *syncLock) TryLock(owner string) bool {
	select {
	case l.sem <- struct{}{}:
		l.take(owner)
		return true
	default:
		return false
	}
}

// TryLockPolite 同 TryLock，但**有人在排队时主动不抢**。
//
// 给 30 秒一轮的增量轮询用：没有这道礼让，增量在整理刚放开锁的瞬间
// 又把锁抢回去，登记了让路的整理照样抢不到——让路就白做了
func (l *syncLock) TryLockPolite(owner string) bool {
	l.mu.Lock()
	queued := len(l.waiters)
	l.mu.Unlock()
	if queued > 0 {
		return false
	}
	return l.TryLock(owner)
}

// Acquire 登记让路请求并最多等 wait，拿到返回 true。
// wait <= 0 时只登记不等（下一次调用就有机会抢到）
func (l *syncLock) Acquire(owner string, wait time.Duration) bool {
	if l.TryLock(owner) {
		return true
	}
	l.addWaiter(owner)
	defer l.removeWaiter(owner)
	if wait <= 0 {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case l.sem <- struct{}{}:
		l.take(owner)
		return true
	case <-timer.C:
		return false
	case <-stopCh:
		return false
	}
}

// Unlock 释放。只有持有者该调用；重复调用是安全的空操作
func (l *syncLock) Unlock() {
	l.mu.Lock()
	l.owner, l.since = "", time.Time{}
	l.mu.Unlock()
	select {
	case <-l.sem:
	default:
	}
}

// Holder 当前持有者与已持有时长
func (l *syncLock) Holder() (string, time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owner == "" {
		return "", 0, false
	}
	return l.owner, time.Since(l.since), true
}

// Describe 「定时整理（已运行 3m20s）」，空闲时返回「空闲」。
// 所有「抢不到锁」的日志与接口报错都用它，不再输出无信息量的「任务进行中」
func (l *syncLock) Describe() string {
	owner, dur, ok := l.Holder()
	if !ok {
		return "空闲"
	}
	return fmt.Sprintf("%s（已运行 %s）", owner, dur.Truncate(time.Second))
}

// Waiters 正在排队的任务名与最早一个等了多久
func (l *syncLock) Waiters() ([]string, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waiters) == 0 {
		return nil, 0
	}
	return append([]string(nil), l.waiters...), time.Since(l.yieldAt)
}

// YieldRequested 是否有别的任务在排队等锁。
// 持有方在长循环里查询，返回 true 就该尽快收工放锁；
// 返回值是等待者描述，用于日志说明「让给了谁」
func (l *syncLock) YieldRequested() (string, bool) {
	waiters, waited := l.Waiters()
	if len(waiters) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s（已等 %s）", strings.Join(waiters, "、"), waited.Truncate(time.Second)), true
}
