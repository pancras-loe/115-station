package api

import (
	"testing"

	"strmhub/internal/model"
)

// 缺集计算：段合并、段过多时汇总为数量
func TestEpisodeMissingFormat(t *testing.T) {
	if _, err := model.InitDB("file:epmiss_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer func() { model.DB = nil }()

	mk := func(names ...string) []remoteFile {
		out := make([]remoteFile, 0, len(names))
		for _, n := range names {
			out = append(out, remoteFile{Name: n})
		}
		return out
	}
	// 不测 TMDB 网络；SeasonEpisodeCount 走不通时 miss 为空
	// 这里验证纯本地路径：无 tv id / movie 不出缺集
	_, m1 := episodeRangeWithMissing(mk("剧 - S01E01.mkv", "剧 - S01E02.mkv"), &TmdbMedia{MediaType: "movie", TmdbID: 1})
	if m1 != "" {
		t.Errorf("movie should have no missing, got %q", m1)
	}
	_, m2 := episodeRangeWithMissing(mk("剧 - S01E01.mkv"), &TmdbMedia{MediaType: "tv"})
	if m2 != "" {
		t.Errorf("tv without tmdb id should have no missing, got %q", m2)
	}
	r3, m3 := episodeRangeWithMissing(nil, nil)
	if r3 != "" || m3 != "" {
		t.Errorf("nil inputs: %q %q", r3, m3)
	}
}
