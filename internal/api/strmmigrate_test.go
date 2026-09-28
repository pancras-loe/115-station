package api

import (
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

func readRel(t *testing.T, root, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func assertFiles(t *testing.T, root, dir string, exist, gone []string) {
	t.Helper()
	for _, n := range exist {
		if _, ok := readRel(t, root, dir+"/"+n); !ok {
			t.Errorf("应存在 %s", n)
		}
	}
	for _, n := range gone {
		if _, ok := readRel(t, root, dir+"/"+n); ok {
			t.Errorf("不应再有 %s", n)
		}
	}
}

func assertLedger(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	for fid, name := range want {
		var sf model.SyncedFile
		model.DB.Where("file_id = ?", fid).First(&sf)
		if sf.RelPath != dir+"/"+name {
			t.Errorf("%s 台账应为 %s，得到 %s", fid, name, sf.RelPath)
		}
	}
}

// 第一次迁移：文件与台账都是旧名
func TestMigrateStrmNames(t *testing.T) {
	const dir = "库/剧集/海绵宝宝/Season 1"
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "v1", RelPath: dir + "/E01.mp4.strm", Kind: "video"},
		{FileID: "v2", RelPath: dir + "/E02.mp4.strm", Kind: "video"},
		{FileID: "n2", RelPath: dir + "/E02.nfo", Kind: "asset"},  // 网盘带来的集 NFO，已同步下来
		{FileID: "v3", RelPath: dir + "/E03.strm", Kind: "video"}, // 已是新写法
		// 同目录同基名：X.strm 已被 mkv 那个占着，mp4 保留旧名，它的产物也不动
		{FileID: "xm", RelPath: dir + "/X.strm", Kind: "video"},
		{FileID: "xp", RelPath: dir + "/X.mp4.strm", Kind: "video"},
		// 本地文件已丢（上次改了文件没来得及改台账）：只补台账
		{FileID: "v4", RelPath: dir + "/E04.mp4.strm", Kind: "video"},
	})
	root := deepDelTestTree(t,
		dir+"/E01.mp4.strm", dir+"/E01.mp4.nfo", dir+"/E01.mp4-thumb.jpg",
		dir+"/E02.mp4.strm", dir+"/E02.mp4.nfo", dir+"/E02.nfo",
		dir+"/E03.strm", dir+"/X.strm", dir+"/X.mp4.strm", dir+"/X.mp4.nfo",
		dir+"/E04.strm",
	)
	oldNfo := filepath.Join(root, filepath.FromSlash(dir+"/E01.mp4.nfo"))
	model.DB.Create(&model.UploadMark{Path: oldNfo, Size: 1})

	st, err := migrateStrmNames(model.DB, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Strm != 3 || st.Failed != 0 {
		t.Fatalf("台账应迁 E01/E02/E04 三行且无失败，得到 %+v", st)
	}
	assertLedger(t, dir, map[string]string{"v1": "E01.strm", "v2": "E02.strm", "v3": "E03.strm",
		"xm": "X.strm", "xp": "X.mp4.strm", "v4": "E04.strm"})
	assertFiles(t, root, dir,
		[]string{"E01.strm", "E01.nfo", "E01-thumb.jpg", "E02.strm", "E02.nfo", "X.strm", "X.mp4.strm", "X.mp4.nfo"},
		// E02.mp4.nfo：网盘带来的 E02.nfo 已占着新名，旧名这份是重复的，删掉
		[]string{"E01.mp4.strm", "E01.mp4.nfo", "E01.mp4-thumb.jpg", "E02.mp4.strm", "E02.mp4.nfo"})
	var m model.UploadMark
	if model.DB.Where("path = ?", filepath.Join(root, filepath.FromSlash(dir+"/E01.nfo"))).First(&m).Error != nil {
		t.Error("回传标记应跟着改名，否则元数据回传会把它当新文件再传一遍")
	}

	// 再跑一遍什么也不做（幂等）
	if st, err := migrateStrmNames(model.DB, root); err != nil || st.touched() != 0 {
		t.Fatalf("第二遍应无事可做，得到 %+v err=%v", st, err)
	}
}

// 2026-09-28 现场：台账已迁成新名，本地又从备份拷回了旧名文件
func TestMigrateStrmNamesAfterRestoreFromBackup(t *testing.T) {
	const dir = "影视/剧集/越狱.2005.{tmdbid=2288}/Season 1"
	const e1 = "越狱.Prison Break.S01E01.1080p.BluRay.SDR.x264.REMUX.DTS-HD"
	const e2 = "越狱.Prison Break.S01E02.1080p.BluRay.SDR.x264.REMUX.DTS-HD"
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "v1", RelPath: dir + "/" + e1 + ".strm", Kind: "video"},
		{FileID: "v2", RelPath: dir + "/" + e2 + ".strm", Kind: "video"},
		{FileID: "n1", RelPath: dir + "/" + e1 + ".nfo", Kind: "asset"},
	})
	root := deepDelTestTree(t,
		dir+"/"+e1+".mkv.strm", dir+"/"+e1+".mkv-thumb.jpg", dir+"/"+e1+".nfo", dir+"/"+e1+".mkv.nfo",
		dir+"/"+e2+".mkv.strm", dir+"/"+e2+".mkv-thumb.jpg",
	)
	st, err := migrateStrmNames(model.DB, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Strm != 0 || st.Restored != 2 || st.Failed != 0 {
		t.Fatalf("台账不用动，本地两个旧名 STRM 应改回，得到 %+v", st)
	}
	assertFiles(t, root, dir,
		[]string{e1 + ".strm", e1 + "-thumb.jpg", e1 + ".nfo", e2 + ".strm", e2 + "-thumb.jpg"},
		[]string{e1 + ".mkv.strm", e1 + ".mkv-thumb.jpg", e1 + ".mkv.nfo", e2 + ".mkv.strm", e2 + ".mkv-thumb.jpg"})
	assertLedger(t, dir, map[string]string{"v1": e1 + ".strm", "v2": e2 + ".strm"})
}

// 迁完之后 Emby 替旧条目补存的 xxx.mkv-thumb.jpg：复查时收拾
func TestReconcileStrmArtifactsLateThumb(t *testing.T) {
	const dir = "库/剧/Season 1"
	deepDelTestDB(t, []model.SyncedFile{{FileID: "v1", RelPath: dir + "/E01.strm", Kind: "video"}})
	root := deepDelTestTree(t, dir+"/E01.strm", dir+"/E01-thumb.jpg", dir+"/E01.mkv-thumb.jpg", dir+"/E01.mkv.nfo")
	st := strmMigrateStats{libDirs: map[string]bool{}}
	if err := reconcileStrmArtifacts(model.DB, root, &st); err != nil {
		t.Fatal(err)
	}
	if st.Stale != 1 || st.Companions != 1 {
		t.Fatalf("重复的缩略图应删、NFO 应改名，得到 %+v", st)
	}
	assertFiles(t, root, dir, []string{"E01.strm", "E01-thumb.jpg", "E01.nfo"}, []string{"E01.mkv-thumb.jpg", "E01.mkv.nfo"})
}

func TestOldNamedArtifact(t *testing.T) {
	stems := []string{"Show.E1", "Show.E1.part2"}
	for name, want := range map[string]string{
		"Show.E1.mkv.strm":       "Show.E1|.strm",
		"Show.E1.mkv-thumb.jpg":  "Show.E1|-thumb.jpg",
		"Show.E1.MP4.nfo":        "Show.E1|.nfo",
		"Show.E1.part2.mkv.nfo":  "Show.E1.part2|.nfo", // 更长的基名是主人
		"Show.E1.chs.ass":        "",                   // 不是旧名产物（新名字幕）
		"Show.E1.nfo":            "",
		"Show.E1.mkv.part3.strm": "", // 别的视频的 STRM
	} {
		stem, _, tail, ok := oldNamedArtifact(name, stems)
		got := ""
		if ok {
			got = stem + "|" + tail
		}
		if got != want {
			t.Errorf("oldNamedArtifact(%q) = %q，预期 %q", name, got, want)
		}
	}
}

// 挂载没就绪（台账里的 STRM 本地一个都看不到）：不迁，下次启动再说
func TestMigrateStrmNamesRefusesMissingMount(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{{FileID: "v1", RelPath: "库/片/A.mkv.strm", Kind: "video"}})
	root := t.TempDir()
	if _, err := migrateStrmNames(model.DB, root); err == nil {
		t.Fatal("本地一个文件都没有时应拒绝迁移")
	}
	var sf model.SyncedFile
	model.DB.Where("file_id = ?", "v1").First(&sf)
	if sf.RelPath != "库/片/A.mkv.strm" {
		t.Fatalf("拒绝时台账不该动，得到 %s", sf.RelPath)
	}
}

func TestStrmMigrateEcho(t *testing.T) {
	armStrmMigrateQuiet([]string{"/media/库/A.mkv.strm"}, []string{"/media/库/A.strm"})
	armStrmMigrateQuiet([]string{"/media/库/B.mkv.strm"}, nil) // 复查追加，不能冲掉前一批
	t.Cleanup(func() { strmMigrateQuiet.paths = nil })
	if !strmMigrateEcho("/media/库/A.mkv.strm") || !strmMigrateEcho("/media/库/A.strm") || !strmMigrateEcho("/media/库/B.mkv.strm") {
		t.Fatal("迁移动过的路径应算回声")
	}
	if strmMigrateEcho("/media/库/C.strm") {
		t.Fatal("别的路径不该被静默")
	}
}
