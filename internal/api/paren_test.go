package api

import (
	"strings"
	"testing"
)

func cleanParens(s string) string {
	s = parenValueRe.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "(.)", "")
	return s
}

func TestParenCleanup(t *testing.T) {
	cases := []struct{ in, want string }{
		{"(.DTS)", ".DTS"},
		{"(.BluRay)", ".BluRay"},
		{"(.)", ""},
		{"(.)(.)", ""},
		{"(.)(.)(.1080p)", ".1080p"},
		{"1080p(.BluRay)(.DTS)", "1080p.BluRay.DTS"},
		{"(.DTS-HD.MA.5.1)", ".DTS-HD.MA.5.1"},
		{".1080p.BluRay.DTS.mkv", ".1080p.BluRay.DTS.mkv"},
		{"白头山.2019.1080p(.)(.)(.BluRay)(.).REMUX(.DTS).mkv", "白头山.2019.1080p.BluRay.REMUX.DTS.mkv"},
	}
	for i, c := range cases {
		got := cleanParens(c.in)
		if got != c.want {
			t.Errorf("case %d: %q → %q, want %q", i, c.in, got, c.want)
		}
	}
}
