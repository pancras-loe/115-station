package api

import (
	"reflect"
	"testing"

	"115-station/internal/model"
)

// 影片 NFO 必须与视频同名（Emby 自己刮削出来就是这个名字），
// 固定名 movie.nfo 只在台账查不到视频行时兜底
func TestMovieNFONames(t *testing.T) {
	cases := []struct {
		name string
		rows []model.SyncedFile
		want []string
	}{
		{
			name: "保留扩展名的 strm",
			rows: []model.SyncedFile{{RelPath: "影视/电影/海洋奇缘.2026/海洋奇缘.Moana.2026.2160p.mkv.strm"}},
			want: []string{"海洋奇缘.Moana.2026.2160p.mkv.nfo"},
		},
		{
			name: "不保留扩展名的 strm",
			rows: []model.SyncedFile{{RelPath: "影视/电影/某片.2020/某片.2020.1080p.strm"}},
			want: []string{"某片.2020.1080p.nfo"},
		},
		{
			name: "同一片目两个版本各写各的",
			rows: []model.SyncedFile{
				{RelPath: "影视/电影/某片.2020/某片.2020.2160p.mkv.strm"},
				{RelPath: "影视/电影/某片.2020/某片.2020.1080p.mkv.strm"},
			},
			want: []string{"某片.2020.2160p.mkv.nfo", "某片.2020.1080p.mkv.nfo"},
		},
		{
			name: "台账里没有视频行 → 兜底固定名",
			rows: nil,
			want: []string{"movie.nfo"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := movieNFONames(c.rows); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("movieNFONames = %v, want %v", got, c.want)
			}
		})
	}
}

// 兜底回传引擎此前只认三个固定名，与视频同名的影片 NFO 和逐集 NFO 一个都传不上去
func TestIsMetadataUploadFile(t *testing.T) {
	yes := []string{
		"poster.jpg", "fanart.jpg", "Banner.jpg", "clearlogo.png", "landscape.jpg",
		"movie.nfo", "tvshow.nfo", "season.nfo",
		"海洋奇缘.Moana.2026.2160p.mkv.nfo", "某剧.S01E01.1080p.mkv.NFO",
	}
	for _, n := range yes {
		if !isMetadataUploadFile(n) {
			t.Errorf("%s 应该回传", n)
		}
	}
	no := []string{"某片.mkv.strm", "随手截图.jpg", "logo.png", "说明.txt"}
	for _, n := range no {
		if isMetadataUploadFile(n) {
			t.Errorf("%s 不该回传", n)
		}
	}
}

// clearlogo / landscape 从 TMDB images 里按语言优先级挑：中文 > 英文 > 无语言
func TestPickTMDBImage(t *testing.T) {
	s := func(v string) *string { return &v }
	imgs := []tmdbImage{
		{FilePath: "/null.png", Lang: nil, VoteAverage: 9},
		{FilePath: "/en-low.png", Lang: s("en"), VoteAverage: 5},
		{FilePath: "/en-high.png", Lang: s("en"), VoteAverage: 6},
		{FilePath: "/zh.svg", Lang: s("zh"), VoteAverage: 10},
	}
	// 中文只有 SVG，要 PNG 时退到英文里评分高的
	if got := pickTMDBImage(imgs, []string{"zh", "en", ""}, ".png"); got != "/en-high.png" {
		t.Errorf("logo = %q", got)
	}
	if got := pickTMDBImage(imgs, []string{"zh", "en"}, ""); got != "/zh.svg" {
		t.Errorf("不限扩展名应取中文：%q", got)
	}
	// 只有无语言的图：landscape 不该拿它充数
	if got := pickTMDBImage(imgs[:1], []string{"zh", "en"}, ""); got != "" {
		t.Errorf("无语言的背景不该当 landscape：%q", got)
	}
	if got := pickTMDBImage(imgs[:1], []string{"zh", "en", ""}, ".png"); got != "/null.png" {
		t.Errorf("无语言 logo 兜底：%q", got)
	}
	// 同分比宽度
	tie := []tmdbImage{{FilePath: "/a.jpg", Lang: s("zh"), Width: 1280}, {FilePath: "/b.jpg", Lang: s("zh"), Width: 3840}}
	if got := pickTMDBImage(tie, []string{"zh"}, ""); got != "/b.jpg" {
		t.Errorf("同分应取更宽的：%q", got)
	}
}

// season.nfo 只写进「整个目录都是这一季」的季目录
func TestScrapeSeasonDirs(t *testing.T) {
	title := metaDest{Local: "/lib/越狱"}
	s1 := metaDest{Local: "/lib/越狱/Season 1"}
	s2 := metaDest{Local: "/lib/越狱/Season 2"}
	mixed := metaDest{Local: "/lib/越狱/合集"}
	s3a := metaDest{Local: "/lib/越狱/Season 3"}
	s3b := metaDest{Local: "/lib/越狱/S3 另一版"}
	vs := []scrapeVideo{
		{Name: "越狱.S01E01.1080p", Dir: s1},
		{Name: "越狱.S01E02.1080p", Dir: s1},
		{Name: "越狱.S02E01.1080p", Dir: s2},
		{Name: "越狱.S04E01.1080p", Dir: title}, // 平铺在标题目录
		{Name: "越狱.S05E01.1080p", Dir: mixed},
		{Name: "越狱.S06E01.1080p", Dir: mixed},
		{Name: "越狱.S03E01.1080p", Dir: s3a},
		{Name: "越狱.S03E02.1080p", Dir: s3b},
		{Name: "花絮", Dir: s2}, // 没集号的不参与
	}
	got := scrapeSeasonDirs(vs, title)
	want := map[int]metaDest{1: s1, 2: s2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scrapeSeasonDirs = %v, want %v", got, want)
	}
}

// 已有的图片不再拉：本地存在且不覆盖时 skip，覆盖模式照拉
func TestMetaSkipper(t *testing.T) {
	dir := t.TempDir()
	d := metaDest{Local: dir}
	if _, err := writeMetaFile(dir, "E01-thumb.jpg", []byte("x"), true); err != nil {
		t.Fatal(err)
	}
	if !(localMetaWriter{}).skip(d, "E01-thumb.jpg") {
		t.Error("已有文件应跳过")
	}
	if (localMetaWriter{}).skip(d, "E02-thumb.jpg") {
		t.Error("缺失的文件不该跳过")
	}
	if (localMetaWriter{force: true}).skip(d, "E01-thumb.jpg") {
		t.Error("覆盖模式不该跳过")
	}
	if !(localMetaWriter{}).skip(metaDest{}, "x.jpg") {
		t.Error("没有本地落点时本地 writer 写不了，应跳过")
	}
	fw := newFileScrapeWriter(nil, false, true)
	if !fw.skip(d, "E01-thumb.jpg") || fw.stat.Skipped != 1 {
		t.Errorf("手动刮削 writer 应跳过并计数：%+v", fw.stat)
	}
	if fw.skip(metaDest{CloudBase: "1"}, "E01-thumb.jpg") {
		t.Error("只有网盘落点时交给 put 判断")
	}
}
