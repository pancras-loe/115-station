package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"115-station/internal/model"
)

func TestDirSyncBlock(t *testing.T) {
	roles := map[string]string{"L": "library", "R": "redundant"}
	cases := []struct {
		chain []browseCrumb
		ok    bool
	}{
		{nil, false},
		{[]browseCrumb{{"X", "别处"}}, false},
		{[]browseCrumb{{"L", "影视"}}, false}, // 库根：交给全量
		{[]browseCrumb{{"L", "影视"}, {"C", "剧集"}}, true},
		{[]browseCrumb{{"L", "影视"}, {"C", "剧集"}, {"T", "某剧"}}, true},
		{[]browseCrumb{{"L", "影视"}, {"R", "冗余"}, {"T", "某剧"}}, false}, // 库里的工作区
	}
	for i, c := range cases {
		if _, why := dirSyncBlock(c.chain, roles); (why == "") != c.ok {
			t.Errorf("#%d 判定不对：%q", i, why)
		}
	}
}

func TestDirSyncTargetOf(t *testing.T) {
	chain := []browseCrumb{{"0a", "网盘"}, {"L", "115名"}, {"C", "剧集"}, {"T", "新名"}}
	libs := []model.Library{{Cid: "L", Local: "资源库/影视", Account: acctMain}}
	got, err := dirSyncTargetOf(chain, 1, libs, "")
	if err != nil || got.prefix != "资源库/影视/剧集/新名" || got.libLocal != "资源库/影视" {
		t.Fatalf("本地位置不对：%+v %v", got, err)
	}
}

func TestDirRenameRoots(t *testing.T) {
	pairs := [][2]string{
		{"影视/剧集/旧名/Season 01", "影视/剧集/新名/Season 01"},
		{"影视/剧集/旧名/Season 02", "影视/剧集/新名/Season 02"},
		{"影视/剧集/旧名", "影视/剧集/新名"},
		{"影视/剧集/新名", "影视/剧集/新名"}, // 没动的
	}
	got := dirRenameRoots(pairs, "影视", "影视/剧集/新名")
	want := []dirMove{{"影视/剧集/旧名", "影视/剧集/新名"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}

	// 只同步了下面的季目录：上一层这次没遍历全，不整个搬
	if got := dirRenameRoots(pairs[:1], "影视", "影视/剧集/新名/Season 01"); len(got) != 0 {
		t.Fatalf("新位置在同步目录之外不该搬：%v", got)
	}
	// 子目录改名（Season 1 → Season 01）
	got = dirRenameRoots([][2]string{{"影视/剧集/A/Season 1", "影视/剧集/A/Season 01"}}, "影视", "影视/剧集/A")
	if !reflect.DeepEqual(got, []dirMove{{"影视/剧集/A/Season 1", "影视/剧集/A/Season 01"}}) {
		t.Fatalf("got %v", got)
	}
	// 一个旧目录拆进两个新目录：不搬
	got = dirRenameRoots([][2]string{{"影视/剧集/A", "影视/剧集/B"}, {"影视/剧集/A", "影视/剧集/C"}}, "影视", "影视/剧集")
	if len(got) != 0 {
		t.Fatalf("拆开的不该搬：%v", got)
	}
	// 两个旧目录并进一个新目录：不搬
	got = dirRenameRoots([][2]string{{"影视/剧集/A", "影视/剧集/C"}, {"影视/剧集/B", "影视/剧集/C"}}, "影视", "影视/剧集")
	if len(got) != 0 {
		t.Fatalf("合并的不该搬：%v", got)
	}
	// 跨库 / 库目录本身：不搬
	got = dirRenameRoots([][2]string{{"别的库/A", "影视/A"}, {"影视", "影视2"}}, "影视", "影视")
	if len(got) != 0 {
		t.Fatalf("跨库的不该搬：%v", got)
	}
}

func dsWrite(t *testing.T, root, rel string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dsExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// 网盘上整个片目改了名、没有改名事件：按 fid 认出来，本地目录整个搬过去（连刮削产物），不另写一份
func TestApplyDirSyncRelocatesRenamedTitle(t *testing.T) {
	newTestDB(t, "dirsync_relocate.db")
	root := t.TempDir()
	for _, rel := range []string{"影视/剧集/旧名/Season 01/E01.strm", "影视/剧集/旧名/Season 01/E02.strm"} {
		dsWrite(t, root, rel)
	}
	dsWrite(t, root, "影视/剧集/旧名/tvshow.nfo") // 刮削产物，不在台账里
	model.DB.Create(&model.SyncedFile{FileID: "f1", PickCode: "pc1", RelPath: "影视/剧集/旧名/Season 01/E01.strm", Kind: "video"})
	model.DB.Create(&model.SyncedFile{FileID: "f2", PickCode: "pc2", RelPath: "影视/剧集/旧名/Season 01/E02.strm", Kind: "video"})
	h := &Handler{DB: model.DB}

	videos := []remoteFile{
		{Fid: "f1", PickCode: "pc1", Name: "E01.mkv", Path: "影视/剧集/新名/Season 01"},
		{Fid: "f2", PickCode: "pc2", Name: "E02.mkv", Path: "影视/剧集/新名/Season 01"},
	}
	r := h.applyDirSync(nil, root, "影视", "影视/剧集/新名", videos, nil)

	if len(r.moved) != 1 || r.moved[0].from != "影视/剧集/旧名" {
		t.Fatalf("应整目录搬一次：%+v", r.moved)
	}
	if dsExists(root, "影视/剧集/旧名") {
		t.Fatal("旧目录应已搬走")
	}
	for _, rel := range []string{"影视/剧集/新名/Season 01/E01.strm", "影视/剧集/新名/tvshow.nfo"} {
		if !dsExists(root, rel) {
			t.Fatalf("新位置缺 %s", rel)
		}
	}
	want := []string{"影视/剧集/新名/Season 01/E01.strm", "影视/剧集/新名/Season 01/E02.strm"}
	if got := ledgerPaths(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("台账：%v", got)
	}
	if len(r.newStrms) != 0 {
		t.Fatalf("改名搬过来的不算新片：%v", r.newStrms)
	}
}

// 本地新位置已经有了（之前别的途径写过一部分），搬不了：逐个文件处理，旧位置删掉、搬空的旧片目收掉
func TestApplyDirSyncPerFileFallback(t *testing.T) {
	newTestDB(t, "dirsync_perfile.db")
	root := t.TempDir()
	dsWrite(t, root, "影视/剧集/旧名/Season 01/E01.strm")
	dsWrite(t, root, "影视/剧集/旧名/Season 01/E01.nfo")
	dsWrite(t, root, "影视/剧集/旧名/tvshow.nfo")
	dsWrite(t, root, "影视/剧集/新名/占位.txt")
	dsWrite(t, root, "影视/剧集/别的剧/Season 01/E01.strm")
	model.DB.Create(&model.SyncedFile{FileID: "f1", PickCode: "pc1", RelPath: "影视/剧集/旧名/Season 01/E01.strm", Kind: "video"})
	model.DB.Create(&model.SyncedFile{FileID: "o1", PickCode: "po", RelPath: "影视/剧集/别的剧/Season 01/E01.strm", Kind: "video"})
	h := &Handler{DB: model.DB}

	r := h.applyDirSync(nil, root, "影视", "影视/剧集/新名",
		[]remoteFile{{Fid: "f1", PickCode: "pc1", Name: "E01.mkv", Path: "影视/剧集/新名/Season 01"}}, nil)

	if len(r.moved) != 0 || r.movedFiles != 1 {
		t.Fatalf("应逐个文件处理：moved=%v files=%d", r.moved, r.movedFiles)
	}
	if !dsExists(root, "影视/剧集/新名/Season 01/E01.strm") {
		t.Fatal("新位置应写出 STRM")
	}
	if dsExists(root, "影视/剧集/旧名") {
		t.Fatal("搬空的旧片目（只剩元数据）应收掉")
	}
	if !dsExists(root, "影视/剧集/别的剧/Season 01/E01.strm") {
		t.Fatal("别的片目不能动")
	}
}

// 台账里在这个目录下、这次没遍历到的：打开失效检测时标失效，不直接删
func TestApplyDirSyncMarksUnseen(t *testing.T) {
	newTestDB(t, "dirsync_orphan.db")
	root := t.TempDir()
	dsWrite(t, root, "影视/电影/A/A.strm")
	dsWrite(t, root, "影视/电影/A/B.strm")
	model.DB.Create(&model.SyncedFile{FileID: "a", PickCode: "pa", RelPath: "影视/电影/A/A.strm", Kind: "video"})
	model.DB.Create(&model.SyncedFile{FileID: "b", PickCode: "pb", RelPath: "影视/电影/A/B.strm", Kind: "video"})
	model.DB.Create(&model.SyncedFile{FileID: "z", PickCode: "pz", RelPath: "影视/电影/AB/Z.strm", Kind: "video"})
	h := &Handler{DB: model.DB}

	r := h.applyDirSync(nil, root, "影视", "影视/电影/A",
		[]remoteFile{{Fid: "a", PickCode: "pa", Name: "A.mkv", Path: "影视/电影/A"}}, nil)
	if r.orphanSkipped != 1 || r.orphans != 0 {
		t.Fatalf("检测关着只报数：%+v", r)
	}
	if !dsExists(root, "影视/电影/A/B.strm") {
		t.Fatal("不该直接删")
	}

	model.DB.Create(&model.Setting{Key: "full", Value: `{"detect_orphans":true}`})
	r = h.applyDirSync(nil, root, "影视", "影视/电影/A",
		[]remoteFile{{Fid: "a", PickCode: "pa", Name: "A.mkv", Path: "影视/电影/A"}}, nil)
	if r.orphans != 1 {
		t.Fatalf("应标 1 个失效：%+v", r)
	}
	var z model.SyncedFile
	model.DB.Where("file_id = ?", "z").First(&z)
	if z.OrphanAt != nil {
		t.Fatal("「AB」不在「A」下面，不能被标")
	}
}
