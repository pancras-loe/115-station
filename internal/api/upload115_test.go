package api

import "testing"

func TestParseMonitorUploadCfgDefaultsDisabled(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"dir":"/media"}`, `not-json`} {
		cfg := parseMonitorUploadCfg(raw)
		if cfg.Enabled {
			t.Fatalf("配置 %q 不应默认允许上传", raw)
		}
	}
}

func TestParseMonitorUploadCfgRequiresExplicitEnable(t *testing.T) {
	cfg := parseMonitorUploadCfg(`{"enabled":true,"dir":" /media ","target":" 123 "}`)
	if !cfg.Enabled {
		t.Fatal("显式开启后应允许上传")
	}
	if cfg.Dir != "/media" || cfg.Target != "123" {
		t.Fatalf("目录配置未正确清理: %+v", cfg)
	}
}
