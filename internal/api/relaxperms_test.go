package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 权限已经对的树一个都不许碰：chmod 会发 IN_ATTRIB，Emby 实时监控会把整库排进刷新队列
func TestRelaxMediaPermsSkipsAlreadyRelaxed(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "剧集", "某剧 (2020)", "Season 1")
	if err := os.MkdirAll(sub, 0o777); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "某剧 - S01E01.strm")
	if err := os.WriteFile(f, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	// 先跑一遍把 umask 造成的偏差补齐，第二遍必须零改动
	relaxMediaPerms(root)
	if d, n := relaxMediaPerms(root); d+n != 0 {
		t.Fatalf("权限已正确时仍改了 %d 个目录 / %d 个文件", d, n)
	}
}

func TestRelaxMediaPermsFixesWrongMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 chmod 只有只读位，造不出 0644")
	}
	root := t.TempDir()
	f := filepath.Join(root, "a.strm")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	d, n := relaxMediaPerms(root)
	if d != 1 || n != 1 {
		t.Fatalf("want 1 目录 1 文件, got %d / %d", d, n)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o666 {
		t.Fatalf("文件权限 %v", st.Mode().Perm())
	}
}
