package api

import (
	"testing"

	"strmhub/internal/cd2"
)

func TestCd2PathHelpers(t *testing.T) {
	if got := cd2NormPath("/a/b/"); got != "/a/b" {
		t.Errorf("norm: %q", got)
	}
	if got := cd2NormPath(""); got != "/" {
		t.Errorf("norm empty: %q", got)
	}
	// 前缀判定：子路径含，兄弟路径不含
	if !cd2HasPrefix("/媒体/电影", "/媒体") || !cd2HasPrefix("/媒体", "/媒体") {
		t.Error("hasPrefix should match subtree")
	}
	if cd2HasPrefix("/媒体2/x", "/媒体") {
		t.Error("hasPrefix must not match sibling prefix")
	}
	if !cd2HasPrefix("/任意", "/") {
		t.Error("root / contains all")
	}
	// 拼接：跳过空段
	if got := cd2Join("媒体", "", "/电影/", ""); got != "/媒体/电影" {
		t.Errorf("join: %q", got)
	}
}

func TestCd2SameKeySet(t *testing.T) {
	a := map[string]bool{"电影": true, "剧集": true}
	if !cd2SameKeySet(a, map[string]bool{"剧集": true, "电影": true}) {
		t.Error("同集合应相等")
	}
	if cd2SameKeySet(a, map[string]bool{"电影": true}) {
		t.Error("缺键不相等")
	}
	if cd2SameKeySet(a, map[string]bool{"电影": true, "剧集": true, "动漫": true}) {
		t.Error("多键不相等")
	}
	if cd2SameKeySet(map[string]bool{}, map[string]bool{}) {
		t.Error("空集合不判等（无指纹意义）")
	}
}

func TestCd2DeriveMount(t *testing.T) {
	// 常规：目标根 = 挂载名 + 115 库完整路径
	m, err := cd2DeriveMount("/115网盘/影视库/媒体库", "/影视库/媒体库")
	if err != nil || m != "/115网盘" {
		t.Errorf("derive: %q err=%v", m, err)
	}
	// 库在 115 根下（单层）
	m, err = cd2DeriveMount("/cloud/媒体库", "/媒体库")
	if err != nil || m != "/cloud" {
		t.Errorf("single seg: %q err=%v", m, err)
	}
	// 目标根本身就是 115 根（库路径=根）不合法
	if _, err := cd2DeriveMount("/115网盘", ""); err == nil {
		t.Error("空库路径应报错")
	}
	// 目标根与库路径不对应（指向了别的目录）
	if _, err := cd2DeriveMount("/115网盘/电影", "/影视库/媒体库"); err == nil {
		t.Error("不对应应报错")
	}
	// 尾段相同但中间不同（恰好同名）也应报错
	if _, err := cd2DeriveMount("/a/媒体库", "/b/媒体库"); err == nil {
		t.Error("同尾段不同路径应报错")
	}
}

func TestCd2GrpcTarget(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://1.2.3.4:19798", "1.2.3.4:19798"},
		{"https://nas.local:9900", "nas.local:9900"},
		{"nas.local", "nas.local:19798"},
		{"1.2.3.4:9900", "1.2.3.4:9900"},
	}
	for _, c := range cases {
		got, err := cd2GrpcTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("cd2GrpcTarget(%q) = %q,%v want %q", c.in, got, err, c.want)
		}
	}
	if _, err := cd2GrpcTarget("  "); err == nil {
		t.Error("empty endpoint should error")
	}
}

func TestCd2DupExists(t *testing.T) {
	ex := []cd2.File{
		{Name: "A.mkv", Size: 100, Sha1: "aa11"},
		{Name: "B.mkv", Size: 200},
	}
	// 同 SHA1（文件名/大小都不同也算重复）
	if !cd2DupExists(ex, cd2.File{Name: "C.mkv", Size: 999, Sha1: "AA11"}, "C.mkv") {
		t.Error("sha1 match (case-insensitive) should be dup")
	}
	// 整理后同名同大小（网盘不提供 SHA1 的兜底）
	if !cd2DupExists(ex, cd2.File{Name: "old.mkv", Size: 200}, "B.mkv") {
		t.Error("same name+size should be dup")
	}
	// 同名不同大小：可能只是同片不同版本，交给洗版判定，不算重复
	if cd2DupExists(ex, cd2.File{Name: "old.mkv", Size: 201}, "B.mkv") {
		t.Error("same name diff size should not be dup")
	}
	// 全新文件
	if cd2DupExists(ex, cd2.File{Name: "D.mkv", Size: 300, Sha1: "ff99"}, "D.mkv") {
		t.Error("no match")
	}
	// 一方无 SHA1 不误判
	if cd2DupExists(ex, cd2.File{Name: "A.mkv", Size: 100}, "Z.mkv") {
		t.Error("one-side missing sha1 must not match by sha1")
	}
}
