package api

import (
	"testing"
	"time"
)

func TestCronMatch(t *testing.T) {
	// 2026-08-15 10:20:30 周六
	ts := time.Date(2026, 8, 15, 10, 20, 30, 0, time.Local)
	cases := []struct {
		expr string
		want bool
	}{
		{"*/10 8-23 * * *", true},  // 每10分钟，8-23点（10:20 命中）
		{"*/15 * * * *", false},    // 20 不被 15 整除
		{"30 * * * *", false},      // 分不对
		{"20 10 * * *", true},      // 分时精确命中
		{"* * 15 8 6", true},       // 日月周全命中（周六=6）
		{"* * * * 0", false},       // 周日不命中
		{"0 0 1 1 *", false},       // 元旦
		{"1,5,10-30 * * * *", true}, // 列表+范围：20 在 10-30
		{"bad expr", false},        // 非法表达式
		{"* * * *", false},         // 字段数不足
	}
	for _, c := range cases {
		if got := CronMatch(c.expr, ts); got != c.want {
			t.Errorf("CronMatch(%q, %v) = %v, want %v", c.expr, ts, got, c.want)
		}
	}
}
