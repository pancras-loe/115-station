package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// 接管 p115strmhelper 的分享 STRM：路径与修改时间不动，只换内容；没提取码又没登记的不接管
func TestShareAdopt(t *testing.T) {
	newTestDB(t, "shareadopt.db")
	db := model.DB
	h := &Handler{DB: db}
	root := t.TempDir()
	write := func(rel, content string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
		old := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		os.Chtimes(p, old, old)
		return p
	}
	helper := "http://mp:3000/api/v1/plugin/P115StrmHelper/redirect_url?share_code=sws9e623h88&receive_code=LGNB&id="
	e1 := write("某剧/Season 1/E01.strm", helper+"101")
	write("某剧/Season 1/E02.strm", helper+"102")
	write("别的/x.strm", "http://nas:6086/d/abcdefghijklmnopq") // 本账号的，不是分享
	write("无码/y.strm", "http://nas:6086/d/s~swsnocode1~9")     // 本站分享直链、没登记：没提取码

	files, scanned, other, err := scanShareAdopt(root)
	if err != nil || scanned != 4 || other != 1 || len(files) != 3 {
		t.Fatalf("扫描：%d 个分享 / 扫 %d / 其他 %d / %v", len(files), scanned, other, err)
	}
	groups := planShareAdopt(files, h.shareAdoptListed, h.shareAdoptKnown)
	if len(groups) != 2 || groups[0].ShareCode != "sws9e623h88" || groups[0].Files != 2 || groups[1].Problem == "" {
		t.Fatalf("分组：%+v", groups)
	}
	n, errs := h.applyShareAdopt(root, "http://nas:6086", "", false, files)
	if n != 2 || len(errs) != 1 {
		t.Fatalf("接管 %d 个，错误 %v", n, errs)
	}
	b, _ := os.ReadFile(e1)
	if string(b) != "http://nas:6086/d/s~sws9e623h88~101?/E01" {
		t.Fatalf("内容没换：%s", b)
	}
	if st, _ := os.Stat(e1); !st.ModTime().Equal(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("修改时间被改了：%v", st.ModTime())
	}
	var src model.ShareSource
	db.Where("share_code = ?", "sws9e623h88").First(&src)
	if !src.Adopted || src.ReceiveCode != "LGNB" || src.Files != 2 || src.LocalDir != "某剧" {
		t.Fatalf("登记的分享：%+v", src)
	}
	// 再接管一次：已登记的跳过
	if n, _ := h.applyShareAdopt(root, "http://nas:6086", "", false, files); n != 0 {
		t.Fatalf("重复接管了 %d 个", n)
	}

	// 之后「原样」检查：分享里的新文件照计划放，已有的留在原位（不按分享结构挪）
	r := &shareStrmRun{
		db: db, src: &src, cfg: shareStrmCfg{}, root: root, domain: "http://nas:6086", cookie: "ck",
		walk: func(string, string, string, int) ([]shareEntry, string, bool, error) {
			return []shareEntry{
				{ID: "101", Name: "E01.mkv", Dir: "某剧/S01", Size: 1 << 30},
				{ID: "103", Name: "E03.mkv", Dir: "某剧/S01", Size: 1 << 30},
			}, "某剧", false, nil
		},
		stop: func() bool { return false }, set: func(string, int, int, string) {}, now: time.Now,
	}
	res, _, err := r.run()
	if err != nil || res.Moved != 0 || res.Written != 2 {
		t.Fatalf("接管后检查：%+v %v", res, err)
	}
	if _, err := os.Stat(e1); err != nil {
		t.Fatal("接管来的文件被挪走了")
	}
}

// 转存这份：台账里有同一个 sha1 的收掉，全收完删掉分享
func TestShareTransferSweep(t *testing.T) {
	newTestDB(t, "sharexfer.db")
	db := model.DB
	h := &Handler{DB: db}
	root := t.TempDir()
	withShareRoot(t, root)
	now := time.Now()
	src := model.ShareSource{ShareCode: "swsxfer01", LocalDir: "片", Transferred: &now}
	db.Create(&src)
	for i, sha := range []string{"AA", "BB"} {
		rel := "片/E0" + string(rune('1'+i)) + ".strm"
		os.MkdirAll(filepath.Join(root, "片"), 0o755)
		os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("x"), 0o644)
		db.Create(&model.ShareFile{SourceID: src.ID, FileID: string(rune('1' + i)), Sha1: sha, RelPath: rel, Kind: "video"})
	}
	db.Create(&model.SyncedFile{FileID: "f1", Sha1: "aa", RelPath: "库/片/E01.strm", Kind: "video"})
	h.shareTransferSweep()
	if _, err := os.Stat(filepath.Join(root, "片", "E01.strm")); !os.IsNotExist(err) {
		t.Fatal("入库了的要收掉")
	}
	if _, err := os.Stat(filepath.Join(root, "片", "E02.strm")); err != nil {
		t.Fatal("没入库的要留着")
	}
	db.Create(&model.SyncedFile{FileID: "f2", Sha1: "BB", RelPath: "库/片/E02.strm", Kind: "video"})
	h.shareTransferSweep()
	var n int64
	db.Model(&model.ShareSource{}).Count(&n)
	if n != 0 {
		t.Fatal("全入库了分享应删掉")
	}
}

func TestShareScrapeTitleAndAcct(t *testing.T) {
	newTestDB(t, "sharescrape.db")
	db := model.DB
	files := []model.ShareFile{
		{RelPath: "剧集/某剧 (2025)/Season 01/E01.strm", Kind: "video"},
		{RelPath: "剧集/某剧 (2025)/Season 01/E02.strm", Kind: "video", Blocked: true},
		{RelPath: "剧集/某剧 (2025)/tvshow.nfo", Kind: "asset"},
		{RelPath: "剧集/别的/E01.strm", Kind: "video"},
	}
	media := &TmdbMedia{TmdbID: 7, Title: "某剧", Year: "2025", MediaType: "tv"}
	tt, ok := shareScrapeTitle("/m/分享", "分享", media, "剧集/某剧 (2025)", files)
	if !ok || tt.Kind != "tv" || tt.Key != "分享/剧集/某剧 (2025)" || len(tt.Videos) != 1 || tt.Videos[0].Name != "E01" {
		t.Fatalf("刮削片目：%+v", tt)
	}
	if !strings.HasSuffix(filepath.ToSlash(tt.Videos[0].Dir.Local), "某剧 (2025)/Season 01") {
		t.Fatalf("视频目录：%s", tt.Videos[0].Dir.Local)
	}
	if _, ok := shareScrapeTitle("/m", "分享", nil, "x", files); ok {
		t.Fatal("没认出不刮")
	}
	// 中转按分享登记的账号占名额
	db.Create(&model.ShareSource{ShareCode: "swsacct01", LocalDir: "a", Account: 3})
	if got := shareAcctOf(db, "s~swsacct01~1"); got != 3 {
		t.Fatalf("账号：%d", got)
	}
	if got := shareAcctOf(db, "s~swsnone01~1"); got != acctMain {
		t.Fatalf("没登记按主号：%d", got)
	}
}

// 找资源 / 链接转存「生成 STRM」：开关关着拒绝；换出来不是 115 分享（磁力）也拒绝，不退成转存或离线
func TestSubmitResourceStrm(t *testing.T) {
	newTestDB(t, "sharesubmit.db")
	h := &Handler{DB: model.DB}
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	if _, err := h.submitResource(resSubmitReq{URL: magnet, Strm: true}, ""); err == nil || !strings.Contains(err.Error(), "没开") {
		t.Fatalf("开关关着：%v", err)
	}
	model.DB.Create(&model.Setting{Key: "sharestrm", Value: `{"enabled":true}`})
	if _, err := h.submitResource(resSubmitReq{URL: magnet, Strm: true}, ""); err == nil || !strings.Contains(err.Error(), "不是 115 分享") {
		t.Fatalf("磁力：%v", err)
	}
}
