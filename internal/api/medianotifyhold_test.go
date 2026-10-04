package api

import (
	"testing"
	"time"
)

// 整理一轮期间的入库通知只攒着、同一部剧并成一张卡片，收尾后才排冲刷
// （2026-10-04 现场：蜡笔小新 4 季各发了一条）
func TestHoldMediaNotifDefersFlush(t *testing.T) {
	reset := func() {
		mediaNotif.mu.Lock()
		if mediaNotif.timer != nil {
			mediaNotif.timer.Stop()
		}
		mediaNotif.items, mediaNotif.timer, mediaNotif.holds = nil, nil, 0
		mediaNotif.firstAt = time.Time{}
		mediaNotif.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	release := holdMediaNotif()
	QueueMediaNotif(mediaNotifEntry{Title: "蜡笔小新", Kind: "剧集", Source: "organize", Episodes: "S01"})
	QueueMediaNotif(mediaNotifEntry{Title: "蜡笔小新", Kind: "剧集", Source: "organize", Episodes: "S02"})
	mediaNotif.mu.Lock()
	n, timer := len(mediaNotif.items), mediaNotif.timer
	mediaNotif.mu.Unlock()
	if n != 1 || timer != nil {
		t.Fatalf("整理期间应合并且不排冲刷，实际 items=%d timer=%v", n, timer != nil)
	}
	release()
	release() // 重复调用无害
	mediaNotif.mu.Lock()
	timer, holds := mediaNotif.timer, mediaNotif.holds
	mediaNotif.mu.Unlock()
	if timer == nil || holds != 0 {
		t.Fatalf("收尾后应排上冲刷，实际 timer=%v holds=%d", timer != nil, holds)
	}
}
