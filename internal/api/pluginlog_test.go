package api

import (
	"os"
	"strings"
	"testing"
)

// 一行进插件自己的文件，带前缀与时间；超过上限切一份，读的时候旧的在前
func TestPluginLogAppendAndRotate(t *testing.T) {
	old := pluginLogDir
	pluginLogDir = t.TempDir()
	defer func() { pluginLogDir = old }()

	lg := pluginLog("ptsignin", "PT签到")
	lg.Printf("✓ %s：%s", "站A", "签到成功")
	lines := readPluginLog("ptsignin", 10)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "[PT签到] ✓ 站A：签到成功") {
		t.Fatalf("got %q", lines)
	}
	if len(lines[0]) < 20 || lines[0][4] != '/' {
		t.Fatalf("缺时间: %q", lines[0])
	}

	// 撑到上限再写一行：当前那份变成 .1，新行进新文件
	big := strings.Repeat("x", pluginLogMaxBytes)
	if err := os.WriteFile(pluginLogPath("ptsignin"), []byte("old\n"+big+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lg.Printf("新的一行")
	if _, err := os.Stat(pluginLogPath("ptsignin") + ".1"); err != nil {
		t.Fatalf("没切: %v", err)
	}
	lines = readPluginLog("ptsignin", 3)
	if len(lines) != 3 || lines[0] != "old" || lines[1] != big || !strings.HasSuffix(lines[2], "[PT签到] 新的一行") {
		t.Fatalf("got %d 行, 末行 %q", len(lines), lines[len(lines)-1])
	}
	lines = readPluginLog("ptsignin", 1)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "新的一行") {
		t.Fatalf("只取最后 1 行: %q", lines)
	}
}

// 同一插件的几个前缀写进同一个文件，别的插件不串
func TestPluginLogKeysShareFile(t *testing.T) {
	old := pluginLogDir
	pluginLogDir = t.TempDir()
	defer func() { pluginLogDir = old }()

	plogAdopt.Printf("a")
	plogStrmRebase.Printf("b")
	plogRbClean.Printf("c")
	got := readPluginLog("strmmigrate", 10)
	if len(got) != 2 || !strings.Contains(got[0], "[STRM接管] a") || !strings.Contains(got[1], "[STRM替换地址] b") {
		t.Fatalf("got %q", got)
	}
	for _, l := range []pluginLogger{plogCheckin, plogPtSignin, plogCoverGen, plogRbClean, plogOffClean, plogDirClean,
		plogAli2115, plogAdopt, plogStrmRebase, plogAliDirClean, plogAliRbClean} {
		if !pluginLogKeys[l.key] {
			t.Fatalf("%s 不在 pluginLogKeys 里，接口读不到", l.key)
		}
	}
}
