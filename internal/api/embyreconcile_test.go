package api

import (
	"strings"
	"testing"
)

func TestSuggestEmbyRoot(t *testing.T) {
	dirs := map[string]bool{
		"/media/strm/115":      true,
		"/media/strm/115/电影":   true,
		"/media/strm/115/剧集":   true,
		"/media/strm/115/纪录片": true,
		"/media/strm/电影":      true, // 干扰：根下也有个同名目录，最长匹配要压过它
	}
	isDir := func(p string) bool { return dirs[p] }

	cases := []struct {
		name string
		locs []string
		want string
	}{
		{"按最长尾巴对上", []string{"/mnt/strm/115/电影", "/mnt/strm/115/剧集"}, "/mnt/strm"},
		{"别的硬盘上的库不投票", []string{"/mnt/strm/115/剧集", "/volume2/家庭视频", "/mnt/strm/115/纪录片"}, "/mnt/strm"},
		{"Windows 风格", []string{`D:\strm\115\电影`}, "D:/strm"},
		{"媒体库挂的就是根：按目录名兜底", []string{"/data/strm"}, "/data/strm"},
		{"完全对不上", []string{"/volume2/家庭视频"}, ""},
		{"Emby 根不能是 /", []string{"/115"}, ""},
	}
	for _, c := range cases {
		got, ev := suggestEmbyRoot(c.locs, "/media/strm", isDir)
		if got != c.want {
			t.Errorf("%s: got %q want %q (ev=%v)", c.name, got, c.want, ev)
		}
		if got != "" && len(ev) == 0 {
			t.Errorf("%s: 有建议却没给依据", c.name)
		}
	}
}

func TestReconcileEmby(t *testing.T) {
	layout := libCategoryLayout{"电影": "movie", "剧集": "tv"}
	ledger := map[string]*ledgerTitleEntry{
		"115/电影/阿凡达 (2009)": {Key: "115/电影/阿凡达 (2009)", Title: "阿凡达", Year: "2009", MediaType: "movie"},
		"115/电影/双版本 (2020)": {Key: "115/电影/双版本 (2020)", Title: "双版本", MediaType: "movie"},
		"115/剧集/三体 (2023)":  {Key: "115/剧集/三体 (2023)", Title: "三体", MediaType: "tv"},
		"115/剧集/拆开的剧":       {Key: "115/剧集/拆开的剧", Title: "拆开的剧", MediaType: "tv"},
		"115/剧集/Emby没有":      {Key: "115/剧集/Emby没有", Title: "Emby没有", MediaType: "tv"},
		"115/剧集/认成电影":        {Key: "115/剧集/认成电影", Title: "认成电影", MediaType: "tv"},
	}
	items := []embyReconItem{
		{Type: "Movie", Name: "阿凡达", Path: "/mnt/strm/115/电影/阿凡达 (2009)/阿凡达.strm"},
		{Type: "Movie", Name: "双版本", Path: "/mnt/strm/115/电影/双版本 (2020)/a.strm"},
		{Type: "Movie", Name: "双版本", Path: "/mnt/strm/115/电影/双版本 (2020)/b.strm"},
		{Type: "Movie", Name: "认成电影", Path: "/mnt/strm/115/剧集/认成电影/x.strm"},
		{Type: "Series", Name: "三体", Path: "/mnt/strm/115/剧集/三体 (2023)"},
		{Type: "Series", Name: "拆开的剧", Path: "/mnt/strm/115/剧集/拆开的剧/第一季"},
		{Type: "Series", Name: "拆开的剧", Path: "/mnt/strm/115/剧集/拆开的剧/第二季"},
		{Type: "Series", Name: "纪录A", Path: "/mnt/strm/115/纪录片/纪录A"},
		{Type: "Series", Name: "纪录B", Path: "/mnt/strm/115/纪录片/纪录B"},
		{Type: "Series", Name: "已删", Path: "/mnt/strm/115/剧集/已删"},
		{Type: "Series", Name: "没进台账", Path: "/mnt/strm/115/剧集/没进台账"},
		{Type: "Series", Name: "别的盘", Path: "/volume2/剧/别的盘"},
		{Type: "Folder", Name: "忽略", Path: "/mnt/strm/115"},
	}
	toLocal := func(p string) string { return strings.Replace(p, "/mnt/strm", "/media/strm", 1) }
	exists := func(p string) bool {
		return strings.HasSuffix(p, "/纪录A") || strings.HasSuffix(p, "/纪录B") || strings.HasSuffix(p, "/没进台账")
	}
	r := reconcileEmby(items, ledger, layout, "/media/strm/", toLocal, exists)

	if !r.MappingOK {
		t.Fatal("映射是对的")
	}
	tv := r.TV
	check := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %d want %d", name, got, want)
		}
	}
	check("tv.emby", tv.Emby, 8)
	check("tv.ledger", tv.Ledger, 4)
	check("tv.matched", tv.Matched, 2)
	check("tv.outside", tv.Outside.Count, 1)
	check("tv.stale", tv.Stale.Count, 1)
	check("tv.uncategorized", tv.Uncategorized.Count, 2)
	check("tv.unledgered", tv.Unledgered.Count, 1)
	check("tv.split_extra", tv.SplitExtra, 1)
	check("tv.missing", tv.Missing.Count, 2) // Emby没有 + 认成电影（Emby 里没有它的 Series）
	if len(tv.UncategorizedBy) != 1 || tv.UncategorizedBy[0].Dir != "115/纪录片" || tv.UncategorizedBy[0].Count != 2 {
		t.Errorf("分类规则外按目录聚合: %+v", tv.UncategorizedBy)
	}
	// 恒等式：差值能被各桶完整解释
	explain := tv.Outside.Count + tv.Stale.Count + tv.Uncategorized.Count + tv.Unledgered.Count +
		tv.TypeMismatch.Count + tv.SplitExtra - tv.Missing.Count
	check("tv 差值可解释", explain, tv.Emby-tv.Ledger)

	mv := r.Movie
	check("movie.emby", mv.Emby, 4)
	check("movie.matched", mv.Matched, 2)
	check("movie.type_mismatch", mv.TypeMismatch.Count, 1)
	check("movie.split_extra", mv.SplitExtra, 1)
	check("movie.missing", mv.Missing.Count, 0)
	explain = mv.Outside.Count + mv.Stale.Count + mv.Uncategorized.Count + mv.Unledgered.Count +
		mv.TypeMismatch.Count + mv.SplitExtra - mv.Missing.Count
	check("movie 差值可解释", explain, mv.Emby-mv.Ledger)
}

func TestReconcileEmbyMappingBroken(t *testing.T) {
	ledger := map[string]*ledgerTitleEntry{"115/电影/A": {Key: "115/电影/A", MediaType: "movie"}}
	items := []embyReconItem{{Type: "Movie", Name: "A", Path: "/mnt/strm/115/电影/A/a.strm"}}
	// 映射没配：Emby 路径原样当本地路径，对不上根
	r := reconcileEmby(items, ledger, libCategoryLayout{"电影": "movie"}, "/media/strm",
		func(p string) string { return p }, func(string) bool { return false })
	if r.MappingOK {
		t.Fatal("一个都对不上应判映射有问题")
	}
}
