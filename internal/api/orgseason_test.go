package api

import (
	"fmt"
	"strings"
	"testing"
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
	pl := placeEntryFiles(media, "", rootRel, fallback, videos, subs, eps, nil)

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
	pl := placeEntryFiles(movie, "", "电影/The Matrix (1999)", "电影/The Matrix (1999)", []remoteFile{v}, []remoteFile{s}, nil, nil)
	if pl.relOf["m"] != "电影/The Matrix (1999)" || pl.relOf["s"] != "电影/The Matrix (1999)" {
		t.Fatalf("电影落点变了：%v", pl.relOf)
	}

	// 整个剧集条目都没有集号：仍落在兜底目录，不塞进特别篇
	tv := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2020", MediaType: "tv"}
	only := remoteFile{Fid: "o", Name: "某剧.2020.1080p.mkv"}
	eps := map[string]*ParsedName{"o": parseFileName(only.Name)}
	pl = placeEntryFiles(tv, "", "剧集/某剧 (2020)", "剧集/某剧 (2020)/Season 01", []remoteFile{only}, nil, eps, nil)
	if pl.relOf["o"] != "剧集/某剧 (2020)/Season 01" {
		t.Fatalf("全无集号时应落在兜底目录，得到 %s", pl.relOf["o"])
	}
}

func TestPlaceEntryFilesSubtitleAfterEnrich(t *testing.T) {
	// 视频被画质补全改过名，字幕还是旧基名：先按补全映射一次再认主人
	tv := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2020", MediaType: "tv"}
	v1 := remoteFile{Fid: "a", Name: "某剧.S01E01.1080p.mkv"}
	v2 := remoteFile{Fid: "b", Name: "某剧.S02E01.1080p.mkv"}
	sub := remoteFile{Fid: "s", Name: "某剧.S02E01.chs.ass"}
	enrich := map[string]string{"某剧.S02E01": "某剧.S02E01.1080p"}
	eps := episodeParses([]remoteFile{v1, v2}, nil, nil)
	pl := placeEntryFiles(tv, "", "剧集/某剧 (2020)", "剧集/某剧 (2020)/Season 01", []remoteFile{v1, v2}, []remoteFile{sub}, eps, enrich)
	if pl.relOf["s"] != pl.relOf["b"] || pl.relOf["s"] == pl.relOf["a"] {
		t.Fatalf("补全后的字幕应跟第 2 季那集：%v", pl.relOf)
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
