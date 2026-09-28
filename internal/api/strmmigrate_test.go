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

func TestMigrateStrmNames(t *testing.T) {
	const dir = "库/剧集/海绵宝宝/Season 1"
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "v1", RelPath: dir + "/E01.mp4.strm", Kind: "video"},
		{FileID: "v2", RelPath: dir + "/E02.mp4.strm", Kind: "video"},
		{FileID: "n2", RelPath: dir + "/E02.nfo", Kind: "asset"},  // 网盘带来的集 NFO，已同步下来
		{FileID: "v3", RelPath: dir + "/E03.strm", Kind: "video"}, // 已是新写法
		// 同目录同基名：X.strm 已被 mkv 那个占着，mp4 保留旧名
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
		t.Fatalf("应迁 E01/E02/E04 三个且无失败，得到 %+v", st)
	}
	want := map[string]string{"v1": "E01.strm", "v2": "E02.strm", "v3": "E03.strm", "xm": "X.strm", "xp": "X.mp4.strm", "v4": "E04.strm"}
	for fid, name := range want {
		var sf model.SyncedFile
		model.DB.Where("file_id = ?", fid).First(&sf)
		if sf.RelPath != dir+"/"+name {
			t.Errorf("%s 台账应为 %s，得到 %s", fid, name, sf.RelPath)
		}
	}
	for _, rel := range []string{"E01.strm", "E01.nfo", "E01-thumb.jpg", "E02.strm", "E02.nfo", "X.mp4.strm", "X.mp4.nfo"} {
		if _, ok := readRel(t, root, dir+"/"+rel); !ok {
			t.Errorf("应存在 %s", rel)
		}
	}
	for _, rel := range []string{"E01.mp4.strm", "E01.mp4.nfo", "E01.mp4-thumb.jpg", "E02.mp4.strm"} {
		if _, ok := readRel(t, root, dir+"/"+rel); ok {
			t.Errorf("旧名 %s 应已改走", rel)
		}
	}
	// 网盘带来的 E02.nfo 已占着新名：不覆盖，刮削写的旧名那份留着
	if _, ok := readRel(t, root, dir+"/E02.mp4.nfo"); !ok {
		t.Error("新名已被网盘镜像占着时，旧名配套文件应原样保留")
	}
	var m model.UploadMark
	if model.DB.Where("path = ?", filepath.Join(root, filepath.FromSlash(dir+"/E01.nfo"))).First(&m).Error != nil {
		t.Error("回传标记应跟着改名，否则元数据回传会把它当新文件再传一遍")
	}

	// 再跑一遍什么也不做（幂等）
	if st, err := migrateStrmNames(model.DB, root); err != nil || st.Strm != 0 || st.Companions != 0 {
		t.Fatalf("第二遍应无事可做，得到 %+v err=%v", st, err)
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
	t.Cleanup(func() { strmMigrateQuiet.paths = nil })
	if !strmMigrateEcho("/media/库/A.mkv.strm") || !strmMigrateEcho("/media/库/A.strm") {
		t.Fatal("迁移动过的路径应算回声")
	}
	if strmMigrateEcho("/media/库/B.strm") {
		t.Fatal("别的路径不该被静默")
	}
}
