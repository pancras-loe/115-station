package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	return &Config{ConfigDir: dir, DataDir: dir}
}

// 损坏的 setting.yaml：保存必须中止且原文件一字不动——
// 此前会拿空表覆盖，把其他所有配置一键清空
func TestSaveSettingCorruptFileNoOverwrite(t *testing.T) {
	c := newTestConfig(t)
	corrupt := "org-basic: '{\"pending\":\"1\"}\n  bad: [unclosed"
	if err := os.WriteFile(c.SettingFile(), []byte(corrupt), 0644); err != nil {
		t.Fatal(err)
	}
	err := c.SaveSetting("cd2", `{"a":1}`)
	if err == nil {
		t.Fatal("损坏文件上保存应报错")
	}
	if !strings.Contains(err.Error(), "中止保存") {
		t.Errorf("错误信息应说明已中止: %v", err)
	}
	b, _ := os.ReadFile(c.SettingFile())
	if string(b) != corrupt {
		t.Errorf("原文件被改动：\n%q\n%q", corrupt, string(b))
	}
}

// 正常路径：保存保留其他键
func TestSaveSettingKeepsOtherKeys(t *testing.T) {
	c := newTestConfig(t)
	if err := c.SaveSetting("org-basic", `{"pending":"1"}`); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveSetting("cd2", `{"endpoint":"h"}`); err != nil {
		t.Fatal(err)
	}
	if v := c.GetSetting("org-basic"); v != `{"pending":"1"}` {
		t.Errorf("org-basic 丢失: %q", v)
	}
	if v := c.GetSetting("cd2"); v != `{"endpoint":"h"}` {
		t.Errorf("cd2 丢失: %q", v)
	}
	// 文件不存在时首存成功
	c2 := &Config{ConfigDir: filepath.Join(t.TempDir()), DataDir: ""}
	if err := c2.SaveSetting("k", "v"); err != nil {
		t.Fatalf("首次保存: %v", err)
	}
	if c2.GetSetting("k") != "v" {
		t.Error("首存读取失败")
	}
}
