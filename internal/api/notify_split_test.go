package api

import (
	"strings"
	"testing"
)

func TestSplitWecomText(t *testing.T) {
	// 短消息不切
	if got := splitWecomText("你好"); len(got) != 1 || got[0] != "你好" {
		t.Fatalf("短消息不应切分: %v", got)
	}
	// 多行长消息：每段 ≤1800 字节，行不被拦腰切断，拼回等于原文
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, "25. 蜘蛛侠：纵横宇宙[国英多音轨+简繁英字幕].2023.BluRay.1080p 某某字幕组制作版本")
		lines = append(lines, "     15.14G | 做种 0 | 4月前")
	}
	msg := strings.Join(lines, "\n")
	parts := splitWecomText(msg)
	if len(parts) < 2 {
		t.Fatalf("长消息应切分成多段，实际 %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 1800 {
			t.Errorf("第 %d 段超限: %d 字节", i+1, len(p))
		}
	}
	if strings.Join(parts, "\n") != msg {
		t.Fatalf("切分后拼接应与原文一致")
	}
	// 单行超限：rune 边界硬切不产生乱码
	long := strings.Repeat("蜘蛛侠", 1000) // 9000 字节
	parts = splitWecomText(long)
	total := 0
	for _, p := range parts {
		if len(p) > 1800 {
			t.Errorf("硬切段超限: %d 字节", len(p))
		}
		total += len([]rune(p))
	}
	if total != 3000 {
		t.Errorf("硬切后字符数不一致: %d", total)
	}
}
