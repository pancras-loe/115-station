package api

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	if newFileScrapeWriter(nil, false, false).skip(d, "E02-thumb.jpg") {
		t.Error("缺失的文件不该跳过")
	}
	if newFileScrapeWriter(nil, true, false).skip(d, "E01-thumb.jpg") {
		t.Error("覆盖模式不该跳过")
	}
	fw := newFileScrapeWriter(nil, false, true)
	if !fw.skip(d, "E01-thumb.jpg") || fw.stat.Skipped != 1 {
		t.Errorf("手动刮削 writer 应跳过并计数：%+v", fw.stat)
	}
	if fw.skip(metaDest{CloudBase: "1"}, "E01-thumb.jpg") {
		t.Error("只有网盘落点时交给 put 判断")
	}
}

// 图床配置的几种写法都拼得出正确地址
func TestTmdbImageURL(t *testing.T) {
	want := "https://image.tmdb.org/t/p/original/a.jpg"
	for _, base := range []string{"", "https://image.tmdb.org", "https://image.tmdb.org/",
		"https://image.tmdb.org/t/p", "https://image.tmdb.org/t/p/w500", " https://image.tmdb.org/t/p/original/ "} {
		if got := tmdbImageURL(base, "original", "/a.jpg"); got != want {
			t.Errorf("base %q → %s", base, got)
		}
	}
	if got := tmdbImageURL("https://img.example.com/tmdb", "w780", "/b.jpg"); got != "https://img.example.com/tmdb/t/p/w780/b.jpg" {
		t.Errorf("自建镜像带路径前缀：%s", got)
	}
}

// 拉回来的图要核对：HTML 错误页、截断的 JPEG / PNG、超上限都按失败处理，
// 否则写下去大小 > 0，「只补缺失」永远把它当成已有
func TestReadImageBody(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 2000)...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 2000)...)
	cases := []struct {
		name string
		data []byte
		ok   bool
	}{
		{"完整 JPEG", append(append([]byte{}, jpeg...), 0xFF, 0xD9), true},
		{"JPEG 结束标记后带尾巴", append(append(append([]byte{}, jpeg...), 0xFF, 0xD9), make([]byte, 16)...), true},
		{"截断的 JPEG", jpeg, false},
		{"完整 PNG", append(append([]byte{}, png...), "\x00\x00\x00\x00IEND\xaeB`\x82"...), true},
		{"截断的 PNG", png, false},
		{"200 的 HTML 错误页", []byte("<!DOCTYPE html><html><body>502 Bad Gateway</body></html>"), false},
		{"空响应", nil, false},
	}
	for _, c := range cases {
		_, err := readImageBody(bytes.NewReader(c.data), 1<<20)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, 期望成功 = %v", c.name, err, c.ok)
		}
	}
	// 超上限：不许被 LimitReader 悄悄截成上限大小当成功
	big := append(append(append([]byte{}, jpeg...), make([]byte, 100)...), 0xFF, 0xD9)
	if _, err := readImageBody(bytes.NewReader(big), len(big)-1); err == nil {
		t.Error("超过上限应当失败")
	}
	if _, err := readImageBody(bytes.NewReader(big), len(big)); err != nil {
		t.Errorf("正好到上限不该失败: %v", err)
	}
}

// 写元数据走临时文件 + 改名：成功后不留临时文件，覆盖已有文件，权限与原来一致
func TestWriteMetaFileAtomic(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeMetaFile(dir, "poster.jpg", []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if wrote, err := writeMetaFile(dir, "poster.jpg", []byte("new"), false); err != nil || wrote {
		t.Fatalf("不覆盖时已有文件应跳过: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := writeMetaFile(dir, "poster.jpg", []byte("new"), true); err != nil || !wrote {
		t.Fatalf("覆盖模式应写入: wrote=%v err=%v", wrote, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "poster.jpg"))
	if string(b) != "new" {
		t.Errorf("内容 = %q, 期望 new", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("目录里应只剩 poster.jpg，实际 %d 个文件", len(ents))
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(dir, "poster.jpg")); st.Mode().Perm() != 0644 {
			t.Errorf("权限 = %v, 期望 0644", st.Mode().Perm())
		}
	}
	// 目录不存在：失败且不留东西
	if _, err := writeMetaFile(filepath.Join(dir, "gone"), "x.nfo", []byte("x"), true); err == nil {
		t.Error("目录不存在时应失败")
	}
}
