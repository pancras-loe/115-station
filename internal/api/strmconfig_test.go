package api

import (
	"testing"

	"115-station/internal/config"
)

// getStrmConfig 必须从 setting.yaml 读（前端保存就写在那儿）。
// 早先它只查 DB，库里永远没有这行 → 用户填的直连域名一律被写死的默认值顶掉
func TestGetStrmConfigReadsSettingFile(t *testing.T) {
	h := &Handler{Config: &config.Config{ConfigDir: t.TempDir(), DataDir: t.TempDir()}}

	// 没存过：回落默认值（302 代理端口，不是管理后台端口）
	if domain, _, _, _ := h.getStrmConfig(); domain != "http://172.17.0.1:6086" {
		t.Errorf("未配置时应回落默认域名，得到 %q", domain)
	}

	if err := h.Config.SaveSetting("strm", `{"domain":"http://nas.local:6086","format":"pick_code","keep_ext":"false","exist":"skip"}`); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	domain, format, keepExt, skipExist := h.getStrmConfig()
	if domain != "http://nas.local:6086" {
		t.Errorf("域名应取配置值，得到 %q", domain)
	}
	if format != "pick_code" || keepExt || !skipExist {
		t.Errorf("其余字段解析不符: format=%q keepExt=%v skipExist=%v", format, keepExt, skipExist)
	}
}

// keep_ext 历史上存过布尔也存过字符串，两种都得认
func TestParseStrmConfigKeepExtBothTypes(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want bool
	}{
		{`{"keep_ext":true}`, true},
		{`{"keep_ext":false}`, false},
		{`{"keep_ext":"true"}`, true},
		{`{"keep_ext":"false"}`, false},
		{`{}`, true},     // 缺字段 = 默认保留
		{`坏 JSON`, true}, // 解析失败也回默认，不能把 strm 写成空域名
	} {
		if _, _, keepExt, _ := parseStrmConfig(c.raw); keepExt != c.want {
			t.Errorf("parseStrmConfig(%q) keepExt=%v, want %v", c.raw, keepExt, c.want)
		}
	}
	if domain, _, _, _ := parseStrmConfig(`坏 JSON`); domain != "http://172.17.0.1:6086" {
		t.Errorf("坏 JSON 应回落默认域名，得到 %q", domain)
	}
}
