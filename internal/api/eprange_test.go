package api

import "testing"

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
		{mk("剧 - 第1集.mkv", "剧 - 第2集.mkv"), "S01E01-E02"},  // 中文集数命名同样解析
	}
	for i, c := range cases {
		got := episodeRangeStr(c.files)
		if got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}
