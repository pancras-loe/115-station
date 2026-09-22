package api

import "testing"

func TestSplitShareLink(t *testing.T) {
	cases := []struct {
		in   string
		url  string
		code string
	}{
		{"https://115.com/s/abc123?password=xyz9", "https://115.com/s/abc123?password=xyz9", "xyz9"},
		{"https://115.com/s/abc123 提取码：ab12", "https://115.com/s/abc123", "ab12"},
		{"https://115.com/s/abc123 提取码:ab12", "https://115.com/s/abc123", "ab12"},
		{"https://115.com/s/abc123 ab12cd", "https://115.com/s/abc123", "ab12cd"},
		{"我用115网盘分享了文件\n链接：https://115.com/s/abc123?password=xy89", "https://115.com/s/abc123?password=xy89", "xy89"},
		{"[打开分享](https://115.com/s/abc123?password=xy89)", "https://115.com/s/abc123?password=xy89", "xy89"},
		{"链接：https://115cdn.com/s/abc_123\n访问码： xy89", "https://115cdn.com/s/abc_123", "xy89"},
		{"https://anxia.com/s/abc-123#xy89", "https://anxia.com/s/abc-123#xy89", "xy89"},
		{"https://115.com/s/abc123", "https://115.com/s/abc123", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		u, code := splitShareLink(c.in)
		if u != c.url || code != c.code {
			t.Errorf("splitShareLink(%q) = (%q, %q), want (%q, %q)", c.in, u, code, c.url, c.code)
		}
	}
}

func TestClassifyLinkBare(t *testing.T) {
	if classifyLink("magnet:?xt=urn:btih:abc") != "magnet" {
		t.Error("magnet not classified")
	}
	if classifyLink("https://115.com/s/abc123") != "share" {
		t.Error("share not classified")
	}
	if classifyLink("你好") != "" {
		t.Error("plain text should not classify")
	}
}
