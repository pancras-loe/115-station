package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

func shareOrgEntries() []shareEntry {
	mb := int64(1 << 20)
	return []shareEntry{
		{ID: "d1", Name: "某剧 2025 全集", IsDir: true},
		{ID: "d2", Name: "S1", Dir: "某剧 2025 全集", IsDir: true},
		{ID: "1", Name: "某剧.S01E01.1080p.WEB-DL.mkv", Dir: "某剧 2025 全集/S1", Size: 900 * mb, Sha1: "A1"},
		{ID: "2", Name: "某剧.S01E02.1080p.WEB-DL.mkv", Dir: "某剧 2025 全集/S1", Size: 900 * mb, Sha1: "A2"},
		{ID: "3", Name: "某剧.S01E01.1080p.WEB-DL.chs.ass", Dir: "某剧 2025 全集/S1", Size: 100},
		{ID: "4", Name: "tvshow.nfo", Dir: "某剧 2025 全集", Size: 100},
	}
}

func TestPlanShareOrganizeTV(t *testing.T) {
	newTestDB(t, "shareorg.db") // 命名模板读设置；别的测试可能改过缓存的模板，下面只核结构、不核具体写法
	plans, _ := planShareFiles(shareOrgEntries(), sharePlanOpts{LocalDir: "某剧 2025 全集", Assets: true})
	media := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2025", MediaType: "tv"}
	out, _, err := planShareOrganize(plans, media, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	rel := map[string]string{}
	for _, p := range out {
		rel[p.Entry.ID] = p.Rel
	}
	if len(rel) != 4 {
		t.Fatalf("应 4 个文件都有位置：%v", rel)
	}
	for _, id := range []string{"1", "2"} {
		if !strings.HasPrefix(rel[id], "剧集/") || !strings.Contains(rel[id], "/Season ") || !strings.HasSuffix(rel[id], ".strm") {
			t.Errorf("视频 %s 的位置不对：%s", id, rel[id])
		}
	}
	if rel["1"] == rel["2"] {
		t.Fatal("两集算到同一个位置")
	}
	// 字幕跟视频同目录、同基名开头
	vid := strings.TrimSuffix(rel["1"], ".strm")
	if !strings.HasPrefix(rel["3"], vid) || !strings.HasSuffix(rel["3"], ".ass") {
		t.Errorf("字幕没跟着视频：视频 %s 字幕 %s", rel["1"], rel["3"])
	}
	// tvshow.nfo 在标题目录（季目录的上一级）
	if !strings.HasSuffix(rel["4"], "/tvshow.nfo") || strings.Contains(rel["4"], "Season") {
		t.Errorf("tvshow.nfo 位置：%s", rel["4"])
	}
}

// 同一集两份（国语 / 粤语）：都留，按 #A #B 区分
func TestPlanShareOrganizeDup(t *testing.T) {
	newTestDB(t, "shareorgdup.db")
	mb := int64(1 << 20)
	entries := []shareEntry{
		{ID: "1", Name: "某剧.S01E01.国语.mkv", Size: 900 * mb, Sha1: "X1"},
		{ID: "2", Name: "某剧.S01E01.粤语.mkv", Size: 800 * mb, Sha1: "X2"},
	}
	plans, _ := planShareFiles(entries, sharePlanOpts{LocalDir: "x"})
	out, _, err := planShareOrganize(plans, &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2025", MediaType: "tv"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Rel == out[1].Rel {
		t.Fatalf("同一集两份要分开：%+v", out)
	}
}

// 本地整理：认出 → 按模板放并记下编号；之后按编号取；认不出按原样放
func TestShareStrmOrganizeRun(t *testing.T) {
	newTestDB(t, "shareorgrun.db")
	db := model.DB
	root := t.TempDir()
	src := model.ShareSource{ShareCode: "swsorg01", LocalDir: "某剧 2025 全集", Layout: shareLayoutOrganize}
	db.Create(&src)
	searches, byIDs := 0, 0
	media := &TmdbMedia{TmdbID: 77, Title: "某剧", Year: "2025", MediaType: "tv"}
	deps := &shareOrgDeps{
		recognize: func(p *ParsedName) (*TmdbMedia, error) {
			searches++
			if p.Title != "某剧" {
				t.Errorf("识别用的片名：%q", p.Title)
			}
			return media, nil
		},
		byID:     func(id int, tv bool) (*TmdbMedia, error) { byIDs++; return media, nil },
		classify: func(*TmdbMedia) string { return "" },
	}
	r := &shareStrmRun{
		db: db, src: &src, cfg: shareStrmCfg{Assets: true}, root: root, domain: "http://nas", cookie: "ck",
		walk: func(string, string, string, int) ([]shareEntry, string, bool, error) {
			return shareOrgEntries(), "某剧 2025 全集", false, nil
		},
		download: func(string, string, string, string) (shareDownInfo, error) { return shareDownInfo{URL: "u"}, nil },
		fetch:    func(string, int64) ([]byte, error) { return []byte("x"), nil },
		stop:     func() bool { return false }, set: func(string, int, int, string) {},
		now: time.Now, org: deps,
	}
	res, _, err := r.run()
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 2 {
		t.Fatalf("写入：%+v", res)
	}
	var got model.ShareSource
	db.First(&got, src.ID)
	if got.TmdbID != 77 || got.MediaType != "tv" || got.MediaTitle != "某剧 (2025)" || got.Category != "剧集" {
		t.Fatalf("认出的条目没记下：%+v", got)
	}
	var f model.ShareFile
	db.Where("file_id = ?", "1").First(&f)
	if !strings.HasPrefix(f.RelPath, "剧集/") {
		t.Fatalf("位置：%s", f.RelPath)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.RelPath))); err != nil {
		t.Fatal("STRM 没写到整理后的位置")
	}

	// 第二次：按编号取，不再按名字搜
	before := searches
	r.src = &got
	if _, _, err := r.run(); err != nil {
		t.Fatal(err)
	}
	if searches != before {
		t.Fatal("认出过的分享不该再按名字搜")
	}

	// 认不出：按原样放在分享目录下，原因记下来
	src2 := model.ShareSource{ShareCode: "swsorg02", LocalDir: "看不懂", Layout: shareLayoutOrganize}
	db.Create(&src2)
	r.src = &src2
	deps.recognize = func(*ParsedName) (*TmdbMedia, error) { return nil, nil }
	r.walk = func(string, string, string, int) ([]shareEntry, string, bool, error) {
		return []shareEntry{{ID: "9", Name: "unknown.mkv", Size: 900 << 20}}, "unknown", false, nil
	}
	if _, _, err := r.run(); err != nil {
		t.Fatal(err)
	}
	got = model.ShareSource{}
	db.First(&got, src2.ID)
	if got.RecogNote == "" || got.TmdbID != 0 {
		t.Fatalf("认不出要写原因：%+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "看不懂", "unknown.strm")); err != nil {
		t.Fatal("认不出的要按原样放")
	}

	// TMDB 连不上：什么都不动、记出错
	deps.recognize = func(*ParsedName) (*TmdbMedia, error) { return nil, os.ErrDeadlineExceeded }
	if _, _, err := r.run(); err == nil {
		t.Fatal("TMDB 连不上应报错")
	}
}
