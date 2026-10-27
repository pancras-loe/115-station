package api

import (
	"os"
	"testing"
)

// TestMain 老测试按 full.local_path 把本地根指到临时目录（mediaroot.go 的 mediaRootLegacyForTests）
func TestMain(m *testing.M) {
	mediaRootLegacyForTests = true
	// 插件日志别写到盘根的 /logs
	dir, err := os.MkdirTemp("", "plugin-logs-")
	if err == nil {
		pluginLogDir = dir
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
