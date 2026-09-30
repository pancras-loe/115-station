package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 台账片目带出库名与视频数：本地文件页的卡片计数、上传时换算网盘相对路径都靠它
func TestScanLedgerTitlesLibNameAndVideos(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "movie", Name: "电影"},
		{MediaType: "tv", Name: "剧集/国产剧"},
	}, []model.SyncedFile{
		{FileID: "m", Kind: "video", RelPath: "影视/电影/流浪地球 (2019)/流浪地球.strm"},
		{FileID: "e1", Kind: "video", RelPath: "影视/剧集/国产剧/狂飙 (2023)/Season 01/狂飙.S01E01.strm"},
		{FileID: "e2", Kind: "video", RelPath: "影视/剧集/国产剧/狂飙 (2023)/Season 01/狂飙.S01E02.strm"},
		// 老台账不带库名
		{FileID: "old", Kind: "video", RelPath: "电影/老片 (2001)/老片.strm"},
	})
	got := scanLedgerTitles()
	check := func(key, lib string, videos int) {
		t.Helper()
		e := got[key]
		if e == nil {
			t.Fatalf("缺少片目 %s: %v", key, got)
		}
		if e.LibName != lib || e.Videos != videos {
			t.Fatalf("%s: LibName=%q Videos=%d，预期 %q %d", key, e.LibName, e.Videos, lib, videos)
		}
	}
	check("影视/电影/流浪地球 (2019)", "影视", 1)
	check("影视/剧集/国产剧/狂飙 (2023)", "影视", 2)
	check("电影/老片 (2001)", "", 1)
}

// 列表一次查表按片目分组台账行：key 与 scanLedgerTitles 对得上，取舍与 scrapeDirVideoRows 一致
func TestLedgerVideoRowsByTitle(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{{MediaType: "tv", Name: "剧集"}}, []model.SyncedFile{
		{FileID: "e1", Kind: "video", PickCode: "p1", RelPath: "影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E01.strm"},
		{FileID: "e2", Kind: "video", PickCode: "p2", RelPath: "影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E02.strm"},
		{FileID: "np", Kind: "video", RelPath: "影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E03.strm"}, // 没 pickcode
		{FileID: "sub", Kind: "subtitle", PickCode: "p4", RelPath: "影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E01.chs.ass"},
	})
	got := ledgerVideoRowsByTitle()
	rows := got["影视/剧集/狂飙 (2023)"]
	if len(got) != 1 || len(rows) != 2 || rows[0].FileID != "e1" || rows[1].FileID != "e2" {
		t.Fatalf("rows: %+v", got)
	}
}

func touch(t *testing.T, p string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectLocalTitle(t *testing.T) {
	root := t.TempDir()
	// 剧：只认 tvshow.nfo，集 NFO 不算剧级元数据
	touch(t, filepath.Join(root, "影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E01.nfo"), "x")
	touch(t, filepath.Join(root, "影视/剧集/狂飙 (2023)/Poster.JPG"), "x")
	// 电影：与视频同基名的 NFO 就算
	touch(t, filepath.Join(root, "影视/电影/流浪地球 (2019)/流浪地球.nfo"), "x")
	touch(t, filepath.Join(root, "影视/电影/流浪地球 (2019)/folder.jpg"), "x")
	// 空海报文件不算（下载中断留下的）
	touch(t, filepath.Join(root, "影视/电影/空图 (2020)/poster.jpg"), "")
	// 多版本电影：Emby 把 poster.jpg 换成了「视频名-poster.jpg」
	touch(t, filepath.Join(root, "影视/电影/夏洛特烦恼 (2015)/夏洛特烦恼.1080p.nfo"), "x")
	touch(t, filepath.Join(root, "影视/电影/夏洛特烦恼 (2015)/夏洛特烦恼.1080p-poster.jpg"), "x")
	// 只有季海报不算片目海报
	touch(t, filepath.Join(root, "影视/剧集/漫长的季节 (2023)/season01-poster.jpg"), "x")

	cases := []struct {
		key, kind   string
		nfo, poster bool
		missing     bool
	}{
		{"影视/剧集/狂飙 (2023)", "tv", false, true, false},
		{"影视/电影/流浪地球 (2019)", "movie", true, true, false},
		{"影视/电影/空图 (2020)", "movie", false, false, false},
		{"影视/电影/夏洛特烦恼 (2015)", "movie", true, true, false},
		{"影视/剧集/漫长的季节 (2023)", "tv", false, false, false},
		{"影视/电影/不存在 (2021)", "movie", false, false, true},
	}
	for _, c := range cases {
		got := inspectLocalTitle(root, &ledgerTitleEntry{Key: c.key, MediaType: c.kind})
		if got.HasNFO != c.nfo || got.HasPoster != c.poster || got.Missing != c.missing {
			t.Errorf("%s: nfo=%v poster=%v missing=%v", c.key, got.HasNFO, got.HasPoster, got.Missing)
		}
	}
}

func TestFilterLocalTitles(t *testing.T) {
	now := time.Now()
	all := []localTitle{
		{Key: "a", Title: "狂飙", Year: "2023", MediaType: "tv", Status: "ok", LastAt: now.Add(-time.Hour), TmdbID: 207468},
		{Key: "b", Title: "流浪地球", Year: "2019", MediaType: "movie", Status: "miss", LastAt: now},
		{Key: "c", Title: "繁花", Year: "2023", MediaType: "tv", Status: "partial", LastAt: now.Add(-2 * time.Hour)},
	}
	keys := func(ts []localTitle) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Key)
		}
		return out
	}

	got, st := filterLocalTitles(all, localTitleQuery{})
	if !reflect.DeepEqual(keys(got), []string{"b", "a", "c"}) {
		t.Fatalf("默认按最近入库排: %v", keys(got))
	}
	if st != (localTitleStats{All: 3, OK: 1, Partial: 1, Miss: 1, Movie: 1, TV: 2}) {
		t.Fatalf("stats = %+v", st)
	}

	// 选了剧集：状态计数只算剧集，类型计数不受类型筛选影响
	got, st = filterLocalTitles(all, localTitleQuery{MediaType: "tv"})
	if !reflect.DeepEqual(keys(got), []string{"a", "c"}) || st != (localTitleStats{All: 2, OK: 1, Partial: 1, Movie: 1, TV: 2}) {
		t.Fatalf("tv: %v %+v", keys(got), st)
	}
	// 选了状态：类型计数只算这个状态
	_, st = filterLocalTitles(all, localTitleQuery{Status: "miss"})
	if st.Movie != 1 || st.TV != 0 || st.All != 3 {
		t.Fatalf("miss: %+v", st)
	}

	// 年份倒序，同年按 key 稳定
	got, _ = filterLocalTitles(all, localTitleQuery{Sort: "year_desc"})
	if !reflect.DeepEqual(keys(got), []string{"a", "c", "b"}) {
		t.Fatalf("year_desc: %v", keys(got))
	}
	// 关键词：片名 / TMDB 编号
	if got, _ = filterLocalTitles(all, localTitleQuery{Keyword: "地球"}); !reflect.DeepEqual(keys(got), []string{"b"}) {
		t.Fatalf("keyword: %v", keys(got))
	}
	if got, _ = filterLocalTitles(all, localTitleQuery{Keyword: "207468"}); !reflect.DeepEqual(keys(got), []string{"a"}) {
		t.Fatalf("tmdb keyword: %v", keys(got))
	}

	// Emby 快照：计数挂到卡片上（不改缓存里的原切片），「缺媒体信息」收窄结果
	emby := map[string]localEmbyStat{"a": {Items: 10, Lack: 3}, "b": {Items: 1}}
	got, st = filterLocalTitles(all, localTitleQuery{Emby: emby})
	if st.ProbeLack != 1 || len(got) != 3 || got[1].Emby == nil || got[1].Emby.Lack != 3 || got[2].Emby != nil {
		t.Fatalf("emby: %+v %+v", st, got)
	}
	if all[0].Emby != nil {
		t.Fatalf("不能改缓存里的卡片")
	}
	got, st = filterLocalTitles(all, localTitleQuery{Emby: emby, Probe: "lack"})
	if !reflect.DeepEqual(keys(got), []string{"a"}) || st.All != 1 || st.TV != 1 || st.ProbeLack != 1 {
		t.Fatalf("probe=lack: %v %+v", keys(got), st)
	}
	// 还没有快照时「缺媒体信息」不生效，免得一进页面列表是空的
	if got, _ = filterLocalTitles(all, localTitleQuery{Probe: "lack"}); len(got) != 3 {
		t.Fatalf("无快照: %v", keys(got))
	}
}

// Emby 条目路径映射回本地后逐级往上找片目；库外的、只到分类目录的都不算
func TestLocalEmbyTitleKey(t *testing.T) {
	ledger := map[string]*ledgerTitleEntry{
		"影视/剧集/国产剧/狂飙 (2023)": {}, "影视/电影/流浪地球 (2019)": {},
	}
	cases := map[string]string{
		"/media/影视/剧集/国产剧/狂飙 (2023)/Season 01/狂飙.S01E01.strm": "影视/剧集/国产剧/狂飙 (2023)",
		"/media/影视/电影/流浪地球 (2019)/流浪地球.strm":                   "影视/电影/流浪地球 (2019)",
		"/media/影视/电影/别的 (2020)/别的.strm":                         "",
		"/other/影视/电影/流浪地球 (2019)/流浪地球.strm":                   "",
		"/media2/影视/电影/流浪地球 (2019)/流浪地球.strm":                  "",
	}
	for in, want := range cases {
		if got := localEmbyTitleKey(in, "/media", ledger); got != want {
			t.Errorf("%s → %q，预期 %q", in, got, want)
		}
	}
}

func TestUnderRoot(t *testing.T) {
	root := t.TempDir()
	if p, ok := underRoot(root, "影视/电影/流浪地球 (2019)"); !ok || p != filepath.Join(root, "影视", "电影", "流浪地球 (2019)") {
		t.Fatalf("正常路径: %q %v", p, ok)
	}
	for _, bad := range []string{"../etc", "影视/../../x", "影视/./x", "a\\..\\b", "", "影视//x"} {
		if _, ok := underRoot(root, bad); ok {
			t.Errorf("%q 不该放行", bad)
		}
	}
}

func TestLocalPosterSig(t *testing.T) {
	h := &Handler{Config: &config.Config{JWTSecret: "s1"}}
	h2 := &Handler{Config: &config.Config{JWTSecret: "s2"}}
	if h.localPosterSig("a") == h.localPosterSig("b") {
		t.Fatal("不同 key 签名应不同")
	}
	if h.localPosterSig("a") == h2.localPosterSig("a") {
		t.Fatal("不同密钥签名应不同")
	}
}

func TestPosterThumbShrinks(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 1500))
	for y := 0; y < 1500; y += 10 {
		for x := 0; x < 1000; x += 10 {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := posterThumb(p, 400)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 600 {
		t.Fatalf("缩略图尺寸 %v", b)
	}
}
