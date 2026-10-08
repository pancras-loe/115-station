package api

import (
	"fmt"
	"strings"
	"testing"

	"115-station/internal/model"
)

const gpPack = "成长的烦恼.Growing.Pains.S01-S07.1985-1991.野老相声.Bilibili.WEB-DL.2160p.DVD.Ai-Enhanced.AVC.AAC.2.0-LGNB@oSpecialCN"

func gpEpisode(season, ep int) remoteFile {
	return remoteFile{
		Fid:  fmt.Sprintf("v%d-%d", season, ep),
		Name: fmt.Sprintf("成长的烦恼.Growing.Pains.S%02dE%03d.1985.野老相声.Bilibili.WEB-DL.2160p.DVD.AVC.AAC.2.0-LGNB.mkv", season, ep),
		Path: fmt.Sprintf("%s/成长的烦恼.第%d季", gpPack, season),
		Size: 1 << 30,
	}
}

// 现场：多季合集整包只按主视频算一个季目录，179 个视频全进 Season 1
func TestPlaceEntryFilesSplitsSeasons(t *testing.T) {
	media := &TmdbMedia{TmdbID: 54, Title: "成长的烦恼", Year: "1985", MediaType: "tv"}
	special := remoteFile{Fid: "sp", Name: "成长的烦恼：希瓦家归来.Growing.Pains.Return.of.the.Seavers.2004.2160p.mkv",
		Path: gpPack, Size: 4 << 30}
	videos := []remoteFile{gpEpisode(1, 1), gpEpisode(2, 23), gpEpisode(7, 166), special}
	subs := []remoteFile{
		{Fid: "sub7", Name: strings.TrimSuffix(gpEpisode(7, 166).Name, ".mkv") + ".chs.ass"},
		{Fid: "orphan", Name: "字幕说明.srt"},
	}

	main := pickMainVideo(videos, nil)
	if main.Fid == "sp" {
		t.Fatal("样本视频不该取最大的特别篇：它搜不出剧集")
	}
	parsed := parseFileName(main.Name)
	eps := episodeParses(videos, nil, parsed)
	// 连续编号换算（这里直接给 TMDB 各季集数）
	remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts))
	if p := eps["v7-166"]; p.Season != 7 || p.Episode != 22 {
		t.Fatalf("S07E166 应换算为 S07E22，得到 S%02dE%02d", p.Season, p.Episode)
	}

	newPath := buildNewNameWithTemplate(media, parsed, main.Name)
	parts := strings.Split(newPath, "/")
	rootRel := libSubPath(categoryDir("tv", ""), parts[0])
	fallback := rootRel + "/" + parts[1]
	pl := placeEntryFiles(media, "", rootRel, fallback, videos, subs, eps)

	season := func(fid string) string { return pathBase(pl.relOf[fid]) }
	if season("v1-1") == season("v7-166") || season("v2-23") == season("v7-166") {
		t.Fatalf("各季应分开放：%v", pl.relOf)
	}
	if !strings.Contains(season("v7-166"), "7") {
		t.Fatalf("第 7 季放错了目录：%s", pl.relOf["v7-166"])
	}
	if pl.relOf["sp"] == pl.relOf["v1-1"] || !strings.HasPrefix(pl.relOf["sp"], rootRel+"/") {
		t.Fatalf("没有集号的特别篇应进标题目录下的特别篇目录，得到 %s", pl.relOf["sp"])
	}
	if pl.relOf["sub7"] != pl.relOf["v7-166"] {
		t.Fatalf("字幕要跟同名视频走：%s vs %s", pl.relOf["sub7"], pl.relOf["v7-166"])
	}
	if pl.relOf["orphan"] == "" {
		t.Fatal("认不出主人的字幕也要有落点")
	}
	if len(pl.dirs) != 4 {
		t.Fatalf("应有 3 个季目录 + 1 个特别篇目录，得到 %v", pl.dirs)
	}
}

func TestPlaceEntryFilesKeepsOldBehaviour(t *testing.T) {
	// 电影：视频字幕全进标题目录（与改造前一致）
	movie := &TmdbMedia{TmdbID: 603, Title: "The Matrix", Year: "1999", MediaType: "movie"}
	v := remoteFile{Fid: "m", Name: "The.Matrix.1999.1080p.mkv"}
	s := remoteFile{Fid: "s", Name: "The.Matrix.1999.1080p.chs.srt"}
	pl := placeEntryFiles(movie, "", "电影/The Matrix (1999)", "电影/The Matrix (1999)", []remoteFile{v}, []remoteFile{s}, nil)
	if pl.relOf["m"] != "电影/The Matrix (1999)" || pl.relOf["s"] != "电影/The Matrix (1999)" {
		t.Fatalf("电影落点变了：%v", pl.relOf)
	}

	// 整个剧集条目都没有集号：仍落在兜底目录，不塞进特别篇
	tv := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2020", MediaType: "tv"}
	only := remoteFile{Fid: "o", Name: "某剧.2020.1080p.mkv"}
	eps := map[string]*ParsedName{"o": parseFileName(only.Name)}
	pl = placeEntryFiles(tv, "", "剧集/某剧 (2020)", "剧集/某剧 (2020)/Season 01", []remoteFile{only}, nil, eps)
	if pl.relOf["o"] != "剧集/某剧 (2020)/Season 01" {
		t.Fatalf("全无集号时应落在兜底目录，得到 %s", pl.relOf["o"])
	}
}

func TestPickMainVideo(t *testing.T) {
	// 电影 + 花絮：带集号的不到两个，仍取最大的
	film := remoteFile{Fid: "f", Name: "Movie.2020.2160p.mkv", Size: 20 << 30}
	extra := remoteFile{Fid: "x", Name: "Movie.Featurette.E01.mkv", Size: 1 << 30}
	if got := pickMainVideo([]remoteFile{extra, film}, nil); got.Fid != "f" {
		t.Fatalf("应取正片，得到 %s", got.Name)
	}
}

// 现场：海绵宝宝 (1999)/Season 1/ 里集 NFO、季 NFO 全被搬进了标题目录，集缩略图当垃圾进了冗余。
// 约定（同 MoviePilot 刮削落点）：剧 → 标题目录，季 → 季目录，集 → 跟视频
func TestPlaceMetaThreeLevels(t *testing.T) {
	media := &TmdbMedia{TmdbID: 387, Title: "海绵宝宝", Year: "1999", MediaType: "tv"}
	rootRel := "动漫番剧/海绵宝宝 (1999)"
	ep := func(fid, name string) remoteFile { return remoteFile{Fid: fid, Name: name, Path: "Season 1"} }
	v1 := ep("v1", "海绵宝宝 - S01E01 - 第 1 集.mp4")
	v10 := ep("v10", "海绵宝宝 - S01E10 - 第 10 集.mp4")
	metas := []remoteFile{
		ep("n1", "海绵宝宝 - S01E01 - 第 1 集.nfo"),
		ep("t1", "海绵宝宝 - S01E01 - 第 1 集-thumb.jpg"),
		ep("n10", "海绵宝宝 - S01E10 - 第 10 集.nfo"),
		ep("sn", "season.nfo"),
		ep("sp", "poster.jpg"),           // 纯季目录里的通用图片 = 季海报
		ep("s1p", "season01-poster.jpg"), // 约定放标题目录
		ep("tv", "tvshow.nfo"),
	}
	for _, m := range metas {
		if k := classifyFile(m.Name); k != FileTypeNFO && k != FileTypeStdImage {
			t.Fatalf("%s 应算元数据，得到 %v", m.Name, k)
		}
	}
	videos := []remoteFile{v1, v10}
	eps := episodeParses(videos, nil, nil)
	pl := placeEntryFiles(media, "", rootRel, rootRel+"/Season 1", videos, nil, eps)
	pl.placeMeta(media, rootRel, videos, metas, nil)

	season := pl.relOf["v1"]
	if season == "" || season == rootRel {
		t.Fatalf("视频应进季目录，得到 %q", season)
	}
	for _, fid := range []string{"n1", "t1", "n10", "sn", "sp"} {
		if pl.relOf[fid] != season {
			t.Errorf("%s 应进季目录 %s，得到 %q", fid, season, pl.relOf[fid])
		}
	}
	for _, fid := range []string{"s1p", "tv"} {
		if _, ok := pl.relOf[fid]; ok {
			t.Errorf("%s 应留给标题目录，却分到了 %s", fid, pl.relOf[fid])
		}
	}
}

// 多季整包：各季的 season.nfo 跟自己那季；剧顶层的 poster.jpg 是剧海报，留标题目录
func TestPlaceMetaMultiSeason(t *testing.T) {
	media := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2020", MediaType: "tv"}
	rootRel := "剧集/某剧 (2020)"
	v1 := remoteFile{Fid: "a", Name: "E01.mkv", Path: "某剧/Season 1"}
	v2 := remoteFile{Fid: "b", Name: "E01.mkv", Path: "某剧/Season 2"}
	metas := []remoteFile{
		{Fid: "sn1", Name: "season.nfo", Path: "某剧/Season 1"},
		{Fid: "sn2", Name: "season.nfo", Path: "某剧/Season 2"},
		{Fid: "e2", Name: "E01.nfo", Path: "某剧/Season 2"}, // 两季都叫 E01：按基名认主人时取到哪个都行，但必须进某个季目录
		{Fid: "p", Name: "poster.jpg", Path: "某剧"},
	}
	videos := []remoteFile{v1, v2}
	eps := episodeParses(videos, nil, nil)
	pl := placeEntryFiles(media, "", rootRel, rootRel+"/Season 1", videos, nil, eps)
	pl.placeMeta(media, rootRel, videos, metas, nil)
	if pl.relOf["a"] == pl.relOf["b"] {
		t.Fatalf("两季应分开：%v", pl.relOf)
	}
	if pl.relOf["sn1"] != pl.relOf["a"] || pl.relOf["sn2"] != pl.relOf["b"] {
		t.Errorf("season.nfo 应跟同目录那季：%v", pl.relOf)
	}
	if r := pl.relOf["e2"]; r != pl.relOf["a"] && r != pl.relOf["b"] {
		t.Errorf("集 NFO 应进季目录，得到 %q", r)
	}
	if _, ok := pl.relOf["p"]; ok {
		t.Errorf("剧顶层的 poster.jpg 应留标题目录，得到 %s", pl.relOf["p"])
	}
}

// 电影不分级：NFO / 封面都在标题目录（视频本来就在那）
func TestPlaceMetaMovieUntouched(t *testing.T) {
	movie := &TmdbMedia{TmdbID: 603, Title: "The Matrix", Year: "1999", MediaType: "movie"}
	v := remoteFile{Fid: "m", Name: "The.Matrix.1999.mkv"}
	n := remoteFile{Fid: "n", Name: "The.Matrix.1999.nfo"}
	pl := placeEntryFiles(movie, "", "电影/The Matrix (1999)", "电影/The Matrix (1999)", []remoteFile{v}, nil, nil)
	pl.placeMeta(movie, "电影/The Matrix (1999)", []remoteFile{v}, []remoteFile{n}, nil)
	if _, ok := pl.relOf["n"]; ok {
		t.Fatalf("电影的 NFO 不该另分落点：%v", pl.relOf)
	}
}

func TestAssetOwnerLongest(t *testing.T) {
	bases := []string{"Show.E1", "Show.E10"}
	for name, want := range map[string]int{
		"Show.E1":        0,
		"Show.E1.chs":    0,
		"Show.E10.chs":   1,
		"Show.E10-thumb": 1,
		"Show.E1-thumb":  0,
		"Show.E2":        -1,
		"tvshow":         -1,
	} {
		if got := assetOwner(name, bases); got != want {
			t.Errorf("assetOwner(%q) = %d，预期 %d", name, got, want)
		}
	}
}

// 现场：冗余/Season 1 —— 容器里拆出来的季目录要带上剧名那一层
func TestRedundantEntryRel(t *testing.T) {
	if got := redundantEntryRel(dirEntry{Name: "Season 1", Parent: "海绵宝宝 (1999)"}); got != "海绵宝宝 (1999)/Season 1" {
		t.Errorf("得到 %q", got)
	}
	if got := redundantEntryRel(dirEntry{Name: "某剧.S01"}); got != "某剧.S01" {
		t.Errorf("顶层条目不该多出一层，得到 %q", got)
	}
}

// 容器目录（海绵宝宝 (1999)/Season N）的剧级元数据：子条目认成同一部、有一季入库成功才搬
func TestContainerMetaTarget(t *testing.T) {
	ok1 := &model.OrganizeRecord{Status: "success", SourceKind: "dir", TmdbID: 387, MediaType: "tv",
		TargetDir: "动漫番剧/海绵宝宝 (1999)", TargetCid: "c1"}
	ok2 := *ok1
	fail := &model.OrganizeRecord{Status: "failed", Stage: "recognize"} // TMDB 超时，没有 tmdb
	exists := &model.OrganizeRecord{Status: "exists", SourceKind: "dir", TmdbID: 387, MediaType: "tv"}
	other := &model.OrganizeRecord{Status: "success", SourceKind: "dir", TmdbID: 1, MediaType: "tv",
		TargetDir: "剧集/别的剧", TargetCid: "c2"}

	if got := containerMetaTarget([]*model.OrganizeRecord{fail, ok1, &ok2, exists}); got != ok1 {
		t.Fatalf("同一部、有成功的：应取第一条成功记录，得到 %+v", got)
	}
	if got := containerMetaTarget([]*model.OrganizeRecord{ok1, other}); got != nil {
		t.Fatalf("容器里是两部不同的片，不该搬：%+v", got)
	}
	if got := containerMetaTarget([]*model.OrganizeRecord{exists, fail}); got != nil {
		t.Fatalf("没有入库成功的（标题目录未必存在），不该搬：%+v", got)
	}
	if got := containerMetaTarget(nil); got != nil {
		t.Fatal("没有记录不该搬")
	}
}

func TestSplitContainerMeta(t *testing.T) {
	remaining := []dirEntry{
		{Fid: "d", Name: "Season 11", IsDir: true},
		{Fid: "tv", Name: "tvshow.nfo"},
		{Fid: "p", Name: "poster.jpg"},
		{Fid: "sp", Name: "season01-poster.jpg"},
		{Fid: "sn", Name: "season.nfo"},
		{Fid: "ad", Name: "广告.txt"},
		{Fid: "shot", Name: "截图.jpg"},
	}
	adopt, clash := splitContainerMeta(remaining, map[string]bool{"poster.jpg": true})
	ids := func(es []dirEntry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.Fid)
		}
		return strings.Join(s, ",")
	}
	if got := ids(adopt); got != "tv,sp" {
		t.Errorf("搬进标题目录的应为 tv,sp，得到 %s", got)
	}
	if got := ids(clash); got != "p" {
		t.Errorf("与标题目录重名的应为 p，得到 %s", got)
	}
}

// 文件名最后一段数字是广告域名（www.5266ys.com）：01–09 带前导零认得出集号，10–30 认不出，
// 要靠同模板的兄弟补。此前只比最后一段数字，模板对不上，21 集全进了 Season 0（2026-10-08 现场）
func TestEpisodeParsesSiblingTemplateNotLastDigits(t *testing.T) {
	var vids []remoteFile
	for i := 1; i <= 30; i++ {
		n := fmt.Sprintf("%d.2160p.60fps.HD国语中字无水印[最新电影www.5266ys.com].mp4", i)
		if i < 10 {
			n = "0" + n
		}
		vids = append(vids, remoteFile{Fid: fmt.Sprint(i), Name: n, Path: "凡人修仙传.2160p.60fps/" + n})
	}
	got := episodeParses(vids, nil, nil)
	for i := 1; i <= 30; i++ {
		p := got[fmt.Sprint(i)]
		if p == nil || p.Season != 1 || p.Episode != i {
			t.Fatalf("第 %d 个应认成 S01E%02d: %+v", i, i, p)
		}
	}
}
