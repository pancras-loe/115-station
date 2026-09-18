package api

import (
	"strings"
	"testing"
)

func TestParenVerify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"白头山.2019.1080p(.)(.)(.BluRay)(.).REMUX(.DTS).mkv", "白头山.2019.1080p.BluRay.REMUX.DTS.mkv"},
		{"白头山.2019.1080p(.BluRay)(.DTS).mkv", "白头山.2019.1080p.BluRay.DTS.mkv"},
	}
	for i, c := range tests {
		r := parenValueRe.ReplaceAllString(c.in, "$1")
		for strings.Contains(r, "(.)") {
			r = strings.ReplaceAll(r, "(.)", "")
		}
		if r != c.want {
			t.Errorf("case %d:\n  in:  %s\n  got:  %s\n  want: %s", i, c.in, r, c.want)
		}
	}
}
