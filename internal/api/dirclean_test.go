package api

import (
	"strings"
	"testing"
)

func TestCheckDirCleanTarget(t *testing.T) {
	g := dirGuard{
		strict:   map[string]string{"10": "媒体库「电影」", "20": "转存/待整理文件夹"},
		rootOnly: map[string]string{"30": "冗余文件夹"},
	}
	// 受保护目录各自的祖先链：/影视(5)/电影(10)、/StrmStation(7)/转存(20)、/StrmStation(7)/冗余(30)
	chains := guardChains{
		"10": {"0", "5", "10"},
		"20": {"0", "7", "20"},
		"30": {"0", "7", "30"},
	}
	cases := []struct {
		cid   string
		chain []string
		want  string // 空 = 允许
	}{
		{"0", []string{"0"}, "网盘根"},
		{"99", []string{"0", "99"}, ""},          // 普通目录
		{"30", []string{"0", "7", "30"}, ""},     // 冗余本身可以清
		{"10", []string{"0", "5", "10"}, "这是媒体库"},
		{"11", []string{"0", "5", "10", "11"}, "在媒体库「电影」里面"},
		{"21", []string{"0", "7", "20", "21"}, "在转存/待整理文件夹里面"},
		{"31", []string{"0", "7", "30", "31"}, ""}, // 冗余里面的子目录：可以
		{"5", []string{"0", "5"}, "里面有媒体库"},
		{"7", []string{"0", "7"}, "里面有"}, // 冗余 / 转存的上级
		{"99", []string{"0", "98"}, "核对不了"}, // 链的末元素不是自己
		{"99", nil, "核对不了"},
	}
	for _, c := range cases {
		err := checkDirCleanTarget(c.cid, c.chain, g, chains)
		if c.want == "" {
			if err != nil {
				t.Errorf("cid=%s 应允许，得到 %v", c.cid, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("cid=%s 期望含「%s」，得到 %v", c.cid, c.want, err)
		}
	}
}

func TestNormalizeDirCleanDirs(t *testing.T) {
	accts := []uint{0, 3}
	out, err := normalizeDirCleanDirs([]dirCleanDir{
		{Account: 0, Cid: " 100 ", Path: "/云下载"},
		{Account: 0, Cid: "100"},                 // 重复
		{Account: 3, Cid: "100", Path: "/云下载"}, // 别的账号的同 cid 不算重复
	}, accts)
	if err != nil || len(out) != 2 || out[0].Cid != "100" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, bad := range []dirCleanDir{
		{Account: 0, Cid: "0"},
		{Account: 0, Cid: ""},
		{Account: 0, Cid: "1,2"},
		{Account: 9, Cid: "100"},
	} {
		if _, err := normalizeDirCleanDirs([]dirCleanDir{bad}, accts); err == nil {
			t.Errorf("%+v 应被拒绝", bad)
		}
	}
	many := make([]dirCleanDir, dirCleanMaxDirs+1)
	for i := range many {
		many[i] = dirCleanDir{Cid: string(rune('1'+i%9)) + strings.Repeat("0", i)}
	}
	if _, err := normalizeDirCleanDirs(many, accts); err == nil {
		t.Error("超过上限应被拒绝")
	}
}

func TestDirCleanSummary(t *testing.T) {
	msg, ok := dirCleanSummary([]dirCleanResult{
		{Account: "主号", Path: "/云下载", Dirs: 2, Files: 3, Size: 1 << 30, Deleted: 5},
		{Account: "主号", Path: "/冗余"},
	}, false)
	if !ok || !strings.Contains(msg, "/云下载：删进回收站 5 / 5 项") || !strings.Contains(msg, "/冗余：本来就是空的") {
		t.Fatalf("%v %s", ok, msg)
	}
	msg, ok = dirCleanSummary([]dirCleanResult{
		{Account: "主号", Path: "/a", Files: 1},
		{Account: "副号", Path: "/b", Error: "在媒体库里面，不能清空"},
	}, true)
	if ok || !strings.Contains(msg, "「主号」/a：会删掉 0 个文件夹、1 个文件") || !strings.Contains(msg, "「副号」/b：✗ 在媒体库里面") {
		t.Fatalf("%v %s", ok, msg)
	}
}
