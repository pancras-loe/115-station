package api

import "testing"

func TestParseTmdbIDQuery(t *testing.T) {
	cases := []struct {
		in, id, kind string
		ok           bool
	}{
		{"108545", "108545", "", true},
		{"tmdbid-108545", "108545", "", true},
		{"tmdb:108545", "108545", "", true},
		{"{tmdbid=108545}", "108545", "", true},
		{"[tmdbid-108545]", "108545", "", true},
		{"tv/108545", "108545", "tv", true},
		{"movie:603", "603", "movie", true},
		{"https://www.themoviedb.org/tv/108545-3-body-problem?language=zh-CN", "108545", "tv", true},
		{"https://www.themoviedb.org/movie/603", "603", "movie", true},
		{"三体", "", "", false},
		{"2001太空漫游", "", "", false},
		{"1917", "1917", "", true}, // 纯数字一律按编号查：片名是数字的用户可以加个年份或别的字
	}
	for _, c := range cases {
		id, kind, ok := parseTmdbIDQuery(c.in)
		if id != c.id || kind != c.kind || ok != c.ok {
			t.Errorf("parseTmdbIDQuery(%q) = %q %q %v，期望 %q %q %v", c.in, id, kind, ok, c.id, c.kind, c.ok)
		}
	}
}

func TestTmdbQueryVariants(t *testing.T) {
	cases := map[string][]string{
		"3体":     {"三体"},
		"三体":     {"3体"},
		"Se7en":  nil, // 不含汉字不变形
		"流浪地球":   nil,
		"流浪地球2":  {"流浪地球二"},
		"第2季 三体": {"第二季 三体", "第2季 3体"},
	}
	for in, want := range cases {
		got := tmdbQueryVariants(in)
		if len(got) != len(want) {
			t.Errorf("tmdbQueryVariants(%q) = %q，期望 %q", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("tmdbQueryVariants(%q) = %q，期望 %q", in, got, want)
				break
			}
		}
	}
}

func TestRankTmdbCands(t *testing.T) {
	cands := []manualCand{
		{ID: 1, MediaType: "tv", Title: "三体动画", popularity: 50},
		{ID: 108545, MediaType: "tv", Title: "三体", original: "3 Body Problem", popularity: 90},
		{ID: 2, MediaType: "movie", Title: "无关条目", popularity: 999},
		{ID: 108545, MediaType: "tv", Title: "三体", popularity: 90}, // 两个搜索词各搜到一次
		{ID: 108545, MediaType: "movie", Title: "同号电影", popularity: 1},
	}
	got := rankTmdbCands(cands, []string{"3体", "三体"})
	if len(got) != 4 {
		t.Fatalf("去重后应剩 4 条，得到 %d 条", len(got))
	}
	if got[0].ID != 108545 || got[0].MediaType != "tv" {
		t.Errorf("片名完全相同的应排第一，得到 %+v", got[0])
	}
	if got[1].ID != 1 {
		t.Errorf("包含搜索词的应排在无关条目前面，得到 %+v", got[1])
	}
}
