package api

import (
	"strings"
	"testing"
	"time"
)

// 抢不到锁时必须说得出在等谁、等了多久。
// 「任务正在进行中」这句话本身不解决任何问题——用户看不出是该等 5 秒还是 20 分钟
func TestSyncLockDescribesHolder(t *testing.T) {
	l := newSyncLock()
	if got := l.Describe(); got != "空闲" {
		t.Fatalf("没人持有时应报空闲，实得 %q", got)
	}
	if !l.TryLock("定时整理") {
		t.Fatal("空闲时该抢得到")
	}
	defer l.Unlock()
	if got := l.Describe(); !strings.Contains(got, "定时整理") || !strings.Contains(got, "已运行") {
		t.Fatalf("描述里要有持有者和已运行时长，实得 %q", got)
	}
	if l.TryLock("另一个任务") {
		t.Fatal("已被持有时不该抢到")
	}
}

// 有人在排队时，30 秒一轮的增量轮询必须主动不抢。
//
// 没有这道礼让，增量会在整理刚放开锁的瞬间又把锁抢回去：
// 整理每分钟才回来一次、转存守望者每 5 分钟一次，抢不过 30 秒一轮的增量
func TestSyncLockPoliteYieldsToWaiters(t *testing.T) {
	l := newSyncLock()
	if !l.TryLock("增量轮询") {
		t.Fatal("空闲时该抢得到")
	}

	got := make(chan bool, 1)
	go func() { got <- l.Acquire("定时整理", 2*time.Second) }()
	waitFor(t, func() bool { w, _ := l.Waiters(); return len(w) > 0 })

	// 让路信号：持有方据此提前收工
	who, ok := l.YieldRequested()
	if !ok || !strings.Contains(who, "定时整理") {
		t.Fatalf("应报出等待方，实得 %q ok=%v", who, ok)
	}
	l.Unlock()

	if !<-got {
		t.Fatal("等待方应拿到锁")
	}
	// 等待方持锁期间，增量的礼让抢锁必须失败
	if l.TryLockPolite("增量轮询") {
		t.Fatal("锁被别人持有时不该抢到")
	}
	l.Unlock()

	// 空闲且无人排队 → 礼让抢锁照常成功
	if !l.TryLockPolite("增量轮询") {
		t.Fatal("空闲无人排队时应抢得到")
	}
	l.Unlock()
}

// 等不到就是等不到：Acquire 超时返回 false，不能把调用方永久挂住
func TestSyncLockAcquireTimesOut(t *testing.T) {
	l := newSyncLock()
	if !l.TryLock("长任务") {
		t.Fatal("空闲时该抢得到")
	}
	defer l.Unlock()

	start := time.Now()
	if l.Acquire("手动整理", 50*time.Millisecond) {
		t.Fatal("锁没释放，不该拿到")
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("应该真的等满超时再返回")
	}
	// 超时的等待方要从队列里摘掉，否则会永久压着持有方让路
	if w, _ := l.Waiters(); len(w) != 0 {
		t.Fatalf("超时后不该残留等待者，实得 %v", w)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
