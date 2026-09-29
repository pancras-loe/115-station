package api

import (
	"strings"
	"testing"
)

// 集数区间格式化：S01E01-E12 / 多段 / 跨季 / 空
func TestEpisodeRangeStr(t *testing.T) {
	mk := func(names ...string) []remoteFile {
		out := make([]remoteFile, 0, len(names))
		for _, n := range names {
			out = append(out, remoteFile{Name: n})
		}
		return out
	}
	cases := []struct {
		files []remoteFile
		want  string
	}{
		{mk("剧 - S01E01.1080p.mkv", "剧 - S01E02.1080p.mkv", "剧 - S01E03.1080p.mkv"), "S01E01-E03"},
		{mk("剧 - S01E01.mkv", "剧 - S01E02.mkv", "剧 - S01E04.mkv", "剧 - S01E05.mkv", "剧 - S01E06.mkv"), "S01E01-E02,E04-E06"},
		{mk("剧 S01E01.mkv", "剧 S02E01.mkv"), "S01E01 S02E01"},
		{mk("电影.1080p.mkv"), ""},
		{mk("剧 - 第1集.mkv", "剧 - 第2集.mkv"), "S01E01-E02"}, // 中文集数命名同样解析
	}
	for i, c := range cases {
		got := episodeRangeStr(c.files)
		if got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

// 双集文件：S04E01-02 / E01-E02 / 第1-2集 带结束集号；跨度不是 2 的、像分辨率年份的都不算
func TestParseEpisodePair(t *testing.T) {
	cases := []struct {
		name            string
		season, ep, end int
	}{
		{"越狱.Prison.Break.S04E01-02.2008.BluRay.mkv", 4, 1, 2},
		{"Prison.Break.S04E01-E02.1080p.mkv", 4, 1, 2},
		{"Prison.Break.S04E01E02.1080p.mkv", 4, 1, 2},
		{"Prison.Break.s04e09-e10.mkv", 4, 9, 10},
		{"剧.E01-E02.1080p.mkv", 1, 1, 2},
		{"庆余年第二季 第5-6集.mp4", 2, 5, 6},
		{"剧.S01E01-E03.mkv", 1, 1, 0},   // 跨度超过 2：按 MoviePilot 口径退回单集
		{"剧.S01E03-02.mkv", 1, 3, 0},    // 倒序
		{"剧.S01E01-1080p.mkv", 1, 1, 0}, // 分辨率
		{"剧.S01E05-2008.mkv", 1, 5, 0},  // 年份
		{"剧.S01E01.2008.mkv", 1, 1, 0},
		{"剧.S01E01-02 {[eo=+10]}.mkv", 1, 11, 12}, // 集偏移两端一起挪
	}
	for _, c := range cases {
		p := parseFileName(c.name)
		if p.Season != c.season || p.Episode != c.ep || p.EpisodeEnd != c.end {
			t.Errorf("%s: 得到 S%dE%d-%d，期望 S%dE%d-%d", c.name, p.Season, p.Episode, p.EpisodeEnd, c.season, c.ep, c.end)
		}
	}
	if p := parseFileName("越狱.Prison.Break.S04E01-02.2008.BluRay.mkv"); p.Title != "越狱 Prison Break" || p.Year != "2008" {
		t.Errorf("片名 / 年份被区间带歪：%q %q", p.Title, p.Year)
	}
}

func TestSeasonEpisodeTagPair(t *testing.T) {
	ctx := buildRenameContext(&TmdbMedia{Title: "越狱", MediaType: "tv"}, parseFileName("Prison.Break.S04E01-02.mkv"), "Prison.Break.S04E01-02.mkv")
	if got := ctx.seasonEpisode(); got != "S04E01-E02" {
		t.Fatalf("season_episode 得到 %q", got)
	}
	if got := buildNewName(&TmdbMedia{Title: "越狱", MediaType: "tv", TmdbID: 1, Year: "2005"},
		parseFileName("Prison.Break.S04E01-02.mkv"), ".mkv"); !strings.HasSuffix(got, "Season 04/越狱 - S04E01-E02.mkv") {
		t.Fatalf("默认命名得到 %q", got)
	}
}

func TestEpisodeRangeStrPair(t *testing.T) {
	files := []remoteFile{{Name: "剧.S01E01-E02.mkv"}, {Name: "剧.S01E02.mkv"}, {Name: "剧.S01E03.mkv"}}
	if got := episodeRangeStr(files); got != "S01E01-E03" {
		t.Fatalf("得到 %q", got)
	}
}

func TestEpisodeNFOPair(t *testing.T) {
	b, err := marshalEpisodeNFO([]nfoEpisode{{Season: 4, Episode: 1, Title: "A"}, {Season: 4, Episode: 2, Title: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Count(s, "<?xml") != 1 || strings.Count(s, "<episodedetails>") != 2 ||
		!strings.Contains(s, "<episode>2</episode>") {
		t.Fatalf("多集 NFO:\n%s", s)
	}
}

func TestSameWashEpisodePair(t *testing.T) {
	if !sameWashEpisode("越狱 - S04E01-E02 - 2160p.mkv", "Prison.Break.S04E01-02.1080p.mkv") {
		t.Fatal("同一个双集文件的两种写法应视为同一集")
	}
	// 保守：双集文件与单集文件互不替换
	if sameWashEpisode("剧.S04E02.2160p.mkv", "剧.S04E01-E02.1080p.mkv") ||
		sameWashEpisode("剧.S04E01.2160p.mkv", "剧.S04E01-E02.1080p.mkv") {
		t.Fatal("双集与单集不能互相顶掉")
	}
}

func TestRemapAbsEpisodesPair(t *testing.T) {
	eps := epsOf(7, 145, 164)
	eps["pair"] = &ParsedName{Season: 7, Episode: 165, EpisodeEnd: 166, IsTV: true}
	remapAbsEpisodes(eps, growingPainsCounts, seqNums(growingPainsCounts))
	if p := eps["pair"]; p.Episode != 21 || p.EpisodeEnd != 22 {
		t.Fatalf("S07E165-166 应换成 E21-E22，得到 E%d-E%d", p.Episode, p.EpisodeEnd)
	}
}

func TestEpisodeNameFromTMDB(t *testing.T) {
	names := map[int]string{1: "逃出生天", 2: "第 2 集", 3: "非法/字符?"}
	calls := 0
	old := episodeNameLookup
	episodeNameLookup = func(tvID, season, ep int) string {
		calls++
		if tvID != 2288 || season != 4 {
			return ""
		}
		return names[ep]
	}
	defer func() { episodeNameLookup = old }()
	media := &TmdbMedia{Title: "越狱", MediaType: "tv", TmdbID: 2288}
	tpl := "{title}.{season_episode}<.{episode_name}>{ext}"

	if got := buildRenameContext(media, parseFileName("Prison.Break.S04E01.mkv"), "Prison.Break.S04E01.mkv").ApplyTemplate(tpl); got != "越狱.S04E01.逃出生天.mkv" {
		t.Errorf("单集得到 %q", got)
	}
	// 第 2 集是 TMDB 占位名，只留第 1 集的
	if got := buildRenameContext(media, parseFileName("Prison.Break.S04E01-02.mkv"), "Prison.Break.S04E01-02.mkv").ApplyTemplate(tpl); got != "越狱.S04E01-E02.逃出生天.mkv" {
		t.Errorf("双集得到 %q", got)
	}
	if got := buildRenameContext(media, parseFileName("Prison.Break.S04E02.mkv"), "Prison.Break.S04E02.mkv").ApplyTemplate(tpl); got != "越狱.S04E02.mkv" {
		t.Errorf("占位集名应当留空，得到 %q", got)
	}
	if got := buildRenameContext(media, parseFileName("Prison.Break.S04E03.mkv"), "Prison.Break.S04E03.mkv").ApplyTemplate(tpl); strings.ContainsAny(got, "/?") {
		t.Errorf("集名里的非法字符要清掉，得到 %q", got)
	}
	// 模板没用到集名就不查 TMDB
	calls = 0
	buildRenameContext(media, parseFileName("Prison.Break.S04E01.mkv"), "x.mkv").ApplyTemplate("{title}.{season_episode}{ext}")
	if calls != 0 {
		t.Errorf("模板不含 {episode_name} 却查了 %d 次", calls)
	}
}
