package api

import "testing"

func TestTitleFirstLetter(t *testing.T) {
	cases := map[string]string{
		"巴啦啦小魔仙之彩虹心石": "B",
		"老夫子之小水虎传奇":   "L",
		"Iron Man":     "I",
		"三傻大闹宝莱坞":     "S",
		"2001太空漫游":    "#",
		"シン・エヴァ":      "0",
	}
	for title, want := range cases {
		if got := titleFirstLetter(title); got != want {
			t.Errorf("titleFirstLetter(%q) = %q, want %q", title, got, want)
		}
	}
}
