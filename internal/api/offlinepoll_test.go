package api

import (
	"testing"
	"time"
)

func TestOfflineFastPollBudget(t *testing.T) {
	offlineFastPolls.Store(5) // 模拟新任务提交
	for i := 0; i < 5; i++ {
		if d := offlineNextPollDelay(); d != 10*time.Second {
			t.Fatalf("预算内第 %d 轮应为 10s，实际 %v", i+1, d)
		}
	}
	// 预算用尽 → 30 秒常规节奏，且持续维持
	for i := 0; i < 2; i++ {
		if d := offlineNextPollDelay(); d != 30*time.Second {
			t.Fatalf("预算用尽后应回 30s，实际 %v", d)
		}
	}
	// 排队→下载中 重新武装
	offlineArmFastPoll()
	if d := offlineNextPollDelay(); d != 10*time.Second {
		t.Fatalf("重新武装后应为 10s，实际 %v", d)
	}
}
