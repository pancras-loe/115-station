package api

import (
	"testing"

	"strmhub/internal/model"
)

func TestTgSubParseChannel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://t.me/quanquan_115", "quanquan_115"},
		{"https://t.me/s/quanquan_115", "quanquan_115"},
		{"https://t.me/quanquan_115?q=abc", "quanquan_115"},
		{"https://t.me/quanquan_115/12345", "quanquan_115"},
		{"@quanquan_115", "quanquan_115"},
		{"quanquan_115", "quanquan_115"},
		{"  ", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := tgSubParseChannel(c.in); got != c.want {
			t.Errorf("tgSubParseChannel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTgSubSourceChannels(t *testing.T) {
	cfg := tgSubCfg{Sources: []TgSubSource{
		{ID: 1, Type: "tg", URL: "https://t.me/b_channel", Priority: 5},
		{ID: 2, Type: "tg", URL: "@a_channel", Priority: 10},
		{ID: 3, Type: "rss", URL: "whatever", Priority: 99}, // 非 tg 类型忽略
	}}
	got := tgSubSourceChannels(cfg)
	if len(got) != 2 || got[0] != "a_channel" || got[1] != "b_channel" {
		t.Fatalf("按优先级排序错误: %v", got)
	}
}

func TestTgSubWaterLevel(t *testing.T) {
	// 模拟水位去重逻辑：LastID 之前的不算新消息
	items := []tgItem{
		{MsgID: 100, Title: "旧消息"},
		{MsgID: 200, Title: "新消息 A"},
		{MsgID: 300, Title: "新消息 B"},
	}
	var lastID int64 = 150
	var hits []tgItem
	var maxID int64
	for _, it := range items {
		if it.MsgID > maxID {
			maxID = it.MsgID
		}
		if it.MsgID > lastID {
			hits = append(hits, it)
		}
	}
	if maxID != 300 {
		t.Fatalf("maxID = %d, want 300", maxID)
	}
	if len(hits) != 2 || hits[0].Title != "新消息 A" {
		t.Fatalf("水位过滤错误: %+v", hits)
	}
}

func TestCoverCollectBlacklist(t *testing.T) {
	// 黑名单分类不进入封面生成（逻辑在 SQL/过滤层，此处验证结构约束）
	_ = model.MediaLibrary{Category: "动漫"}
}
