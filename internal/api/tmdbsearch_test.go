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
	got := rankTmdbCands(cands, tmdbSearchPlan{queries: []string{"3体", "三体"}})
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

func TestPlanTmdbSearch(t *testing.T) {
	cases := []struct {
		in, id, kind, year string
		first              string // 第一个搜索词（id 非空时不看）
	}{
		{"3体.2024.{tmdbid=108545}", "108545", "", "", ""},
		{"3体.2024.{[tmdbid=108545;type=tv]}", "108545", "tv", "", ""},
		{"Movie.1999.[tmdbid-603]", "603", "", "", ""},
		{"3体.2024", "", "", "2024", "3体"},
		{"三体", "", "", "", "三体"},
		{"3.Body.Problem.S01E02.2024.1080p.NF.WEB-DL.mkv", "", "", "2024", "3 Body Problem"},
		{"三体 3 Body Problem (2024)", "", "", "2024", "三体"},
	}
	for _, c := range cases {
		p := planTmdbSearch(c.in, nil)
		if p.id != c.id || p.kind != c.kind {
			t.Errorf("planTmdbSearch(%q) id=%q kind=%q，期望 %q %q", c.in, p.id, p.kind, c.id, c.kind)
			continue
		}
		if c.id != "" {
			continue
		}
		if p.year != c.year || len(p.queries) == 0 || p.queries[0] != c.first {
			t.Errorf("planTmdbSearch(%q) year=%q queries=%q，期望 year=%q 首个搜索词 %q", c.in, p.year, p.queries, c.year, c.first)
		}
	}
	// 替换规则同样生效：规则把片名换成带 id 标签的写法，就直接按编号查
	rules := []ReplaceRule{{From: "3体", To: "三体 {[tmdbid=108545;type=tv]}"}}
	if p := planTmdbSearch("3体.2024", rules); p.id != "108545" || p.kind != "tv" {
		t.Errorf("替换规则没生效：%+v", p)
	}
}

func TestRankTmdbCandsYearAndKind(t *testing.T) {
	cands := []manualCand{
		{ID: 1, MediaType: "movie", Title: "三体", Year: "2016", popularity: 99},
		{ID: 2, MediaType: "tv", Title: "三体", Year: "2023", popularity: 50},
		{ID: 108545, MediaType: "tv", Title: "三体", Year: "2024", popularity: 10},
	}
	got := rankTmdbCands(cands, tmdbSearchPlan{queries: []string{"三体"}, year: "2024"})
	if got[0].ID != 108545 {
		t.Errorf("年份对得上的应排第一，得到 %+v", got[0])
	}
	got = rankTmdbCands(cands[:2], tmdbSearchPlan{queries: []string{"三体"}, tv: true})
	if got[0].ID != 2 {
		t.Errorf("名字像剧集时剧集应排第一，得到 %+v", got[0])
	}
}
