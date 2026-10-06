package api

import (
	"strings"
	"testing"
)

// 越狱第 0 季（节选）：9 是 2026-10-06 现场那一集
func prisonBreakS00(lang string) (map[int]tmdbEpisodeInfo, error) {
	if lang == "en-US" {
		return map[int]tmdbEpisodeInfo{
			1:  {Name: "Behind the Walls", AirDate: "2005-08-01"},
			9:  {Name: "The Final Break", AirDate: "2009-07-21"},
			10: {Name: "Episode 10"},
		}, nil
	}
	return map[int]tmdbEpisodeInfo{
		1:  {Name: "第 1 集"},
		2:  {Name: "特别篇", AirDate: "2006-08-01"},
		9:  {Name: "最后一越", AirDate: "2009-07-21"},
		10: {Name: "越狱"},
	}, nil
}

func parseAll(names map[string]string) map[string]*ParsedName {
	eps := map[string]*ParsedName{}
	for fid, n := range names {
		eps[fid] = parseVideoInDir(remoteFile{Fid: fid, Name: n}, nil, nil)
	}
	return eps
}

func TestMatchSpecialTitles(t *testing.T) {
	names := map[string]string{
		"sp": "越狱特别篇：最后一越.Prison.Break.The.Final.Break.2009.Bluray.REMUX.1080p.SDR.x264.AVC.DTS-HD.MA.5.1-LGNB.mkv",
		"e1": "Prison.Break.S01E01.1080p.mkv",
	}
	eps := parseAll(names)
	hits := matchSpecialTitles(eps, names, []string{"越狱", "Prison Break"}, prisonBreakS00)
	if len(hits) != 1 || hits[0].Episode != 9 {
		t.Fatalf("hits = %+v", hits)
	}
	if p := eps["sp"]; p.Season != 0 || p.Episode != 9 || !isSpecialEpisode(p) {
		t.Fatalf("sp = S%dE%d", p.Season, p.Episode)
	}
	if p := eps["e1"]; p.Season != 1 || p.Episode != 1 {
		t.Fatalf("正片被动了：S%dE%d", p.Season, p.Episode)
	}
}

func TestMatchSpecialTitlesEnglishFallback(t *testing.T) {
	names := map[string]string{"sp": "Prison.Break.The.Final.Break.2009.1080p.mkv"}
	eps := parseAll(names)
	var langs []string
	hits := matchSpecialTitles(eps, names, []string{"越狱", "Prison Break"}, func(l string) (map[int]tmdbEpisodeInfo, error) {
		langs = append(langs, l)
		return prisonBreakS00(l)
	})
	if len(hits) != 1 || hits[0].Episode != 9 || len(langs) != 2 {
		t.Fatalf("hits = %+v langs = %v", hits, langs)
	}
}

func TestMatchSpecialTitlesGuards(t *testing.T) {
	cases := map[string]string{
		"泛称集名不认":   "越狱特别篇.2006.1080p.mkv",
		"集名就是剧名不认": "越狱.花絮.mkv",
		"年份对不上不认":  "越狱.最后一越.2015.mkv",
	}
	for why, n := range cases {
		names := map[string]string{"x": n}
		eps := parseAll(names)
		if hits := matchSpecialTitles(eps, names, []string{"越狱", "Prison Break"}, prisonBreakS00); len(hits) != 0 {
			t.Errorf("%s：%s → %+v", why, n, hits)
		}
	}

	// 两个文件对上同一集：都不认（会改成同一个名字）
	names := map[string]string{"a": "越狱.最后一越.1080p.mkv", "b": "越狱.最后一越.2160p.mkv"}
	eps := parseAll(names)
	if hits := matchSpecialTitles(eps, names, []string{"越狱"}, prisonBreakS00); len(hits) != 0 {
		t.Errorf("同一集两个文件：%+v", hits)
	}

	// 已有明写 S00E09 的文件：这一集不再分配
	names = map[string]string{"a": "越狱.S00E09.mkv", "b": "越狱.最后一越.mkv"}
	eps = parseAll(names)
	if hits := matchSpecialTitles(eps, names, []string{"越狱"}, prisonBreakS00); len(hits) != 0 {
		t.Errorf("集号已被占：%+v", hits)
	}

	// 两集集名一样长：不认
	names = map[string]string{"a": "某剧.甲乙丙丁.丁丙乙甲.mkv"}
	eps = parseAll(names)
	tie := func(string) (map[int]tmdbEpisodeInfo, error) {
		return map[int]tmdbEpisodeInfo{1: {Name: "甲乙丙丁"}, 2: {Name: "丁丙乙甲"}}, nil
	}
	if hits := matchSpecialTitles(eps, names, []string{"某剧"}, tie); len(hits) != 0 {
		t.Errorf("平手：%+v", hits)
	}
}

// 明写的 S00Exx 放在季目录里：不能被目录的季号盖掉，也不能跟主视频的季
func TestExplicitS00KeepsSeasonZero(t *testing.T) {
	main := &ParsedName{Season: 5}
	p := parseVideoInDir(remoteFile{Name: "越狱.S00E09.mkv", Path: "越狱/Season 05"}, nil, main)
	if p.Season != 0 || p.Episode != 9 {
		t.Fatalf("S%dE%d", p.Season, p.Episode)
	}
	ctx := &RenameContext{Parsed: p}
	if got := ctx.seasonEpisode(); got != "S00E09" {
		t.Fatalf("seasonEpisode = %q", got)
	}
	if s, e := scrapeEpisodeNo("越狱 - S00E09 - 最后一越.mkv"); s != 0 || e != 9 {
		t.Fatalf("刮削季集 = S%dE%d", s, e)
	}
}

// 认出集号的特别篇进特别篇目录，不跟正片季目录
func TestPlaceSpecialEpisode(t *testing.T) {
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	videos := []remoteFile{{Fid: "e1", Name: "S01E01.mkv"}, {Fid: "sp", Name: "最后一越.mkv"}}
	eps := map[string]*ParsedName{
		"e1": {Season: 1, Episode: 1, IsTV: true},
		"sp": {Season: 0, Episode: 9, IsTV: true},
	}
	rootRel := libSubPath(categoryDir("tv", ""), "越狱 (2005)")
	pl := placeEntryFiles(media, "", rootRel, rootRel+"/Season 01", videos, nil, eps)
	if pl.relOf["sp"] == pl.relOf["e1"] || pl.relOf["sp"] == rootRel {
		t.Fatalf("特别篇落点 %q，正片 %q", pl.relOf["sp"], pl.relOf["e1"])
	}
}

// 散文件：认出集号的特别篇进特别篇目录、按集模板改名；正片照旧进季目录
func TestSingleFileTargetSpecial(t *testing.T) {
	prev := renameTpl
	renameTpl = defaultRenameConfig()
	t.Cleanup(func() { renameTpl = prev })
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	name := "越狱特别篇：最后一越.Prison.Break.The.Final.Break.2009.1080p.mkv"
	sp := &ParsedName{Season: 0, Episode: 9, IsTV: true}
	newPath, targetDir, rootRel := singleFileTarget(media, "", sp, name)
	if targetDir == rootRel || !strings.HasPrefix(targetDir, rootRel+"/") {
		t.Fatalf("特别篇应在标题目录下的特别篇目录：targetDir=%q rootRel=%q", targetDir, rootRel)
	}
	if !strings.Contains(pathBase(newPath), "S00E09") {
		t.Fatalf("应按集模板改名成 S00E09：%q", newPath)
	}

	ep := &ParsedName{Season: 1, Episode: 1, IsTV: true}
	_, epDir, epRoot := singleFileTarget(media, "", ep, "越狱.S01E01.mkv")
	if epRoot != rootRel || epDir == targetDir || !strings.HasPrefix(epDir, rootRel+"/") {
		t.Fatalf("正片落点 %q（标题目录 %q，特别篇 %q）", epDir, epRoot, targetDir)
	}
}
