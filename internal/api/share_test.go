package api

import "testing"

func TestExtractShareCode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://115.com/s/abc123?password=xy12", "abc123"},
		{"https://115.com/s/-swVq_9H", "-swVq_9H"},
		{"https://115cdn.com/s/AbC789", "AbC789"},
		{"https://anxia.com/s/xyz999", "xyz999"},
		{"https://115.com/s/abc123", "abc123"},
		{"https://pan.quark.cn/s/1a2b3c", "https://pan.quark.cn/s/1a2b3c"}, // 非 115 域原样返回
		{"", ""},
	}
	for _, c := range cases {
		if got := extractShareCode(c.in); got != c.want {
			t.Errorf("extractShareCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIs115ShareLink(t *testing.T) {
	yes := []string{
		"https://115.com/s/abc123?password=x",
		"https://115cdn.com/s/abc123",
		"https://anxia.com/s/abc123#cd",
	}
	no := []string{
		"https://pan.quark.cn/s/abc",
		"https://cloud.189.cn/t/abc",
		"magnet:?xt=urn:btih:abc",
		"https://115.com/file/abc",
	}
	for _, s := range yes {
		if !is115ShareLink(s) {
			t.Errorf("is115ShareLink(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if is115ShareLink(s) {
			t.Errorf("is115ShareLink(%q) = true, want false", s)
		}
	}
}

func TestReSharePass(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://115.com/s/abc123?password=xy12", "xy12"},
		{"https://115.com/s/abc123#xy12", "xy12"},
		{"https://115.com/s/abc123", ""},
	}
	for _, c := range cases {
		m := reSharePass.FindStringSubmatch(c.in)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("reSharePass(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
