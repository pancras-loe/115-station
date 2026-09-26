package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 7 季共 166 集（与《成长的烦恼》的总集数一致，各季分配是测试用的）
var growingPainsCounts = map[int]int{0: 2, 1: 22, 2: 22, 3: 24, 4: 24, 5: 26, 6: 26, 7: 22}

func epsOf(season int, from, to int) map[string]*ParsedName {
	m := map[string]*ParsedName{}
	for e := from; e <= to; e++ {
		m[fmt.Sprintf("s%de%d", season, e)] = &ParsedName{Season: season, Episode: e, IsTV: true}
	}
	return m
}

func merge(ms ...map[string]*ParsedName) map[string]*ParsedName {
	out := map[string]*ParsedName{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// seqNums TMDB 这一季是普通的 1..n 编号
func seqNums(counts map[int]int) func(int) (map[int]bool, error) {
	return func(s int) (map[int]bool, error) {
		out := map[int]bool{}
		for e := 1; e <= counts[s]; e++ {
			out[e] = true
		}
		return out, nil
	}
}

// applied 实际换了的季（去掉只是报原因的）
func applied(rs []absRemap) []absRemap {
	var out []absRemap
	for _, r := range rs {
		if r.Skip == "" {
			out = append(out, r)
		}
	}
	return out
}

func TestRemapAbsEpisodesWholeSeries(t *testing.T) {
	// 第 1、2 季相邻（资源自己的编号接得上），第 7 季单独在（只能按 TMDB 累计）
	eps := merge(epsOf(1, 1, 22), epsOf(2, 23, 44), epsOf(7, 145, 166))
	got := applied(remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts)))
	if len(got) != 2 || got[0] != (absRemap{Season: 2, Offset: 22, Count: 22, By: "相邻季"}) ||
		got[1] != (absRemap{Season: 7, Offset: 144, Count: 22, By: "TMDB 各季集数"}) {
		t.Fatalf("换算结果 %+v", got)
	}
	if p := eps["s7e166"]; p.Season != 7 || p.Episode != 22 {
		t.Fatalf("S07E166 应换成 S07E22，得到 S%02dE%02d", p.Season, p.Episode)
	}
	if p := eps["s2e23"]; p.Episode != 1 {
		t.Fatalf("S02E23 应换成 S02E01，得到 E%02d", p.Episode)
	}
	if p := eps["s1e22"]; p.Episode != 22 {
		t.Fatalf("第 1 季不换，得到 E%02d", p.Episode)
	}
}

func TestRemapAbsEpisodesLeavesRelative(t *testing.T) {
	eps := merge(epsOf(2, 1, 22), epsOf(7, 1, 22))
	if got := remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts)); len(got) != 0 {
		t.Fatalf("季内编号不该换算，也不该报原因: %+v", got)
	}
}

func TestRemapAbsEpisodesTmdbNativeAbsolute(t *testing.T) {
	// 海贼王式：TMDB 第 21 季自己就是从 892 编起，文件 S21E900 是对的
	counts := map[int]int{21: 194}
	for s := 1; s <= 20; s++ {
		counts[s] = 44 // 20 季共 880
	}
	counts[20] = 55 // 凑成前 20 季 891
	native := func(s int) (map[int]bool, error) {
		out := map[int]bool{}
		for e := 892; e <= 1085; e++ {
			out[e] = true
		}
		return out, nil
	}
	eps := epsOf(21, 892, 900)
	got := remapAbsEpisodes(eps, counts, native)
	if len(applied(got)) != 0 || len(got) != 1 || !strings.Contains(got[0].Skip, "海贼王") {
		t.Fatalf("TMDB 季内绝对编号不该换算，且要说明原因: %+v", got)
	}
	if eps["s21e900"].Episode != 900 {
		t.Fatal("集号被改了")
	}
}

func TestRemapAbsEpisodesNeedsWholeSeason(t *testing.T) {
	// 同一季里有一集说不通（E03 不在累计区间里）：整季不换，不各换各的
	eps := merge(epsOf(3, 45, 50), epsOf(3, 3, 3))
	if got := applied(remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts))); len(got) != 0 {
		t.Fatalf("混着两种编号不该换算: %+v", got)
	}
}

func TestRemapAbsEpisodesUnsure(t *testing.T) {
	cases := []struct {
		name   string
		counts map[int]int
		nums   func(int) (map[int]bool, error)
		eps    map[string]*ParsedName
	}{
		{"前面缺一季的集数", map[int]int{1: 22, 3: 24}, seqNums(growingPainsCounts), epsOf(3, 45, 50)},
		{"TMDB 季详情取不到", growingPainsCounts, func(int) (map[int]bool, error) { return nil, errors.New("timeout") }, epsOf(7, 145, 166)},
		{"超出全剧总集数", growingPainsCounts, seqNums(growingPainsCounts), epsOf(7, 160, 170)},
		// 第 1 季只有 10 集时，S02E11-E24 两种读法都说得通、且没有一集超过本季 24 集：不动
		{"两种读法都成立", map[int]int{1: 10, 2: 24}, seqNums(map[int]int{1: 10, 2: 24}), epsOf(2, 11, 24)},
	}
	for _, c := range cases {
		if got := applied(remapAbsEpisodes(c.eps, c.counts, c.nums)); len(got) != 0 {
			t.Errorf("%s：不该换算，得到 %+v", c.name, got)
		}
	}
}

// 现场：TMDB 各季集数和资源的分法差了几集（这里第 3 季 TMDB 记 24、资源是 26），
// 只按 TMDB 累计的话第 3 季往后全对不上 —— 整包都在时按相邻季的编号接续来换
func TestRemapAbsEpisodesAdjacentSeasonsBeatTmdbSplit(t *testing.T) {
	release := []int{22, 22, 26, 22, 26, 26, 22} // 资源各季集数，共 166
	var parts []map[string]*ParsedName
	from := 1
	for i, n := range release {
		parts = append(parts, epsOf(i+1, from, from+n-1))
		from += n
	}
	eps := merge(parts...)
	got := remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts))
	if a := applied(got); len(a) != 6 {
		t.Fatalf("第 2-7 季都该换算，得到 %+v", got)
	}
	if p := eps["s7e166"]; p.Episode != 22 {
		t.Fatalf("S07E166 应为 S07E22，得到 E%d", p.Episode)
	}
	if p := eps["s3e70"]; p.Episode != 26 {
		t.Fatalf("S03E70 应为 S03E26，得到 E%d", p.Episode)
	}
}

func TestRemapAbsEpisodesPartialRelativeNotChained(t *testing.T) {
	// 残缺的季内编号首尾正好接得上（S01E01-E04 + S02E05-E10），但没有一集超出第 2 季：不换
	eps := merge(epsOf(1, 1, 4), epsOf(2, 5, 10))
	if got := applied(remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts))); len(got) != 0 {
		t.Fatalf("不该换算: %+v", got)
	}
}

func TestRemapAbsEpisodesExplainsMismatch(t *testing.T) {
	// 单独一季、TMDB 累计对不上：不换，但要在日志里说清楚为什么
	got := remapAbsEpisodes(epsOf(5, 90, 115), growingPainsCounts, seqNums(growingPainsCounts))
	if len(got) != 1 || got[0].Skip == "" || !strings.Contains(got[0].Skip, "对不上") {
		t.Fatalf("应报出原因: %+v", got)
	}
}
