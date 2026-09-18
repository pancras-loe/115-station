package api

import (
	"testing"
)

func TestPickcodeOfDirectURL(t *testing.T) {
	if pc := pickcodeOfDirectURL("http://192.168.1.5:6086/d/abc123"); pc != "abc123" {
		t.Errorf("plain: %q", pc)
	}
	// ?/文件名 后缀被剥离（与改写前内联实现语义一致）
	if pc := pickcodeOfDirectURL("http://h:6086/d/abc123?/Movie.2023.mkv"); pc != "abc123" {
		t.Errorf("name hint: %q", pc)
	}
	// keepExt 的扩展名保留在 id 段（/d/ 端点自会处理）
	if pc := pickcodeOfDirectURL("http://h:6086/d/abc123.mkv?/Movie.mkv"); pc != "abc123.mkv" {
		t.Errorf("keepExt: %q", pc)
	}
	// 非 /d/ 链接（如 123 盘）不误取
	if pc := pickcodeOfDirectURL("http://h:6086/123/456789"); pc != "" {
		t.Errorf("non-/d/: %q", pc)
	}
	if pc := pickcodeOfDirectURL(""); pc != "" {
		t.Errorf("empty: %q", pc)
	}
}

func TestItemDetailPathRe(t *testing.T) {
	// 反代里 Request.URL.Path 是去掉 /emby 前缀后的路径（Director 用 c.Param("path")）
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/Users/abc123/Items/98765", true},
		{"/users/abc/items/1", true},
		{"/Users/abc/Items", false}, // 列表查询
		{"/Users/abc/Items/98765/Intros", false},
		{"/Users/abc/Items/98765/Similar", false},
		{"/Items/98765/PlaybackInfo", false},
		{"/Users/abc/Items/abc", false}, // 条目 id 必须纯数字
	} {
		if got := itemDetailPathRe.MatchString(c.path); got != c.want {
			t.Errorf("match(%q) = %v want %v", c.path, got, c.want)
		}
	}
}
