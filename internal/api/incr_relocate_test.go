package api

import (
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

// mkTree 在本地造一棵已同步过的目录树（文件 + 台账行）
func mkTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		model.DB.Create(&model.SyncedFile{FileID: rel, RelPath: rel, Kind: "video"})
	}
}

func ledgerPaths(t *testing.T) []string {
	t.Helper()
	var rows []model.SyncedFile
	model.DB.Order("rel_path").Find(&rows)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RelPath)
	}
	return out
}

// 网盘目录改名 → 本地目录跟着改名，整棵子树的台账换前缀。零 115 请求。
//
// 改造前这里只是「重遍历新位置」，旧名子树原样留在本地等失效 STRM 检测来收
func TestRelocateLocalDirRenames(t *testing.T) {
	newTestDB(t, "relocate_rename.db")
	root := t.TempDir()
	mkTree(t, root,
		"媒体库/剧集/旧名/Season 01/E01.mkv.strm",
		"媒体库/剧集/旧名/Season 01/E02.mkv.strm",
		"媒体库/剧集/旧名/poster.jpg",
	)
	h := &Handler{DB: model.DB}

	if !h.relocateLocalDir("媒体库/剧集/旧名", "媒体库/剧集/新名", root) {
		t.Fatal("改名应成功")
	}

	if _, err := os.Stat(filepath.Join(root, "媒体库", "剧集", "旧名")); !os.IsNotExist(err) {
		t.Fatal("旧目录应已不存在")
	}
	if _, err := os.Stat(filepath.Join(root, "媒体库", "剧集", "新名", "Season 01", "E01.mkv.strm")); err != nil {
		t.Fatalf("新位置应有文件: %v", err)
	}
	want := []string{
		"媒体库/剧集/新名/Season 01/E01.mkv.strm",
		"媒体库/剧集/新名/Season 01/E02.mkv.strm",
		"媒体库/剧集/新名/poster.jpg",
	}
	got := ledgerPaths(t)
	if len(got) != len(want) {
		t.Fatalf("台账行数不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("台账第 %d 行未换前缀:\n实得 %s\n期望 %s", i, got[i], want[i])
		}
	}
}

// 换前缀不能误伤名字前缀相同的兄弟目录：「旧名」改名不该动到「旧名2」
func TestRelocateLocalDirPrefixBoundary(t *testing.T) {
	newTestDB(t, "relocate_prefix.db")
	root := t.TempDir()
	mkTree(t, root,
		"媒体库/剧集/旧名/E01.mkv.strm",
		"媒体库/剧集/旧名2/E01.mkv.strm",
	)
	h := &Handler{DB: model.DB}

	if !h.relocateLocalDir("媒体库/剧集/旧名", "媒体库/剧集/新名", root) {
		t.Fatal("改名应成功")
	}
	for _, p := range ledgerPaths(t) {
		if p == "媒体库/剧集/新名2/E01.mkv.strm" {
			t.Fatal("「旧名2」被误伤了")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "媒体库", "剧集", "旧名2", "E01.mkv.strm")); err != nil {
		t.Fatalf("「旧名2」应原封不动: %v", err)
	}
}

// 目标已存在时不覆盖：宁可回退重遍历，也不能把用户已有的树盖掉
func TestRelocateLocalDirRefusesExistingTarget(t *testing.T) {
	newTestDB(t, "relocate_exists.db")
	root := t.TempDir()
	mkTree(t, root, "媒体库/A/x.strm", "媒体库/B/y.strm")
	h := &Handler{DB: model.DB}

	if h.relocateLocalDir("媒体库/A", "媒体库/B", root) {
		t.Fatal("目标已存在时不该搬")
	}
	if _, err := os.Stat(filepath.Join(root, "媒体库", "B", "y.strm")); err != nil {
		t.Fatalf("目标目录内容应原封不动: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "媒体库", "A", "x.strm")); err != nil {
		t.Fatalf("源目录也不该被动: %v", err)
	}
}

// 本地根本没有这棵树（从没同步过）→ 返回 false 让调用方回退重遍历
func TestRelocateLocalDirMissingSource(t *testing.T) {
	newTestDB(t, "relocate_missing.db")
	h := &Handler{DB: model.DB}
	if h.relocateLocalDir("媒体库/不存在", "媒体库/新名", t.TempDir()) {
		t.Fatal("源目录不存在时应返回 false")
	}
}

// ==================== 事件链路 ====================

// 目录改名事件：走本地搬迁，不触发目录重遍历
func TestIncrFolderRenameRelocates(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_folderrename.db")
	mkTree(t, p.LocalPath, "媒体库/剧集/旧名/E01.mkv.strm")

	// 缓存里记着这个目录原来的位置
	d.cachedAbs["dir-1"] = "/影视/剧集/旧名"
	d.abs["d1"] = "/影视/剧集" // 事件的父目录
	d.rel["d1"] = "剧集"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evFolderRename, FileID: "dir-1", Cid: "d1",
			FileName: "新名", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moved != 1 {
		t.Fatalf("应记一次搬迁，实得 Moved=%d", sum.Moved)
	}
	if d.walkCalls != 0 {
		t.Fatalf("本地搬迁成功就不该再重遍历，实际遍历了 %d 次", d.walkCalls)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "新名", "E01.mkv.strm")); err != nil {
		t.Fatalf("本地目录应已改名: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "旧名")); !os.IsNotExist(err) {
		t.Fatal("旧名子树不该残留")
	}
}

// 缓存里没有旧路径时回退重遍历，且**什么都不删** —— 不猜
func TestIncrFolderRenameFallsBackWhenOldPathUnknown(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_folderrename_fb.db")
	mkTree(t, p.LocalPath, "媒体库/剧集/旧名/E01.mkv.strm")

	d.abs["d1"] = "/影视/剧集"
	d.rel["d1"] = "剧集"
	d.rel["dir-1"] = "剧集/新名" // 改名后这个目录自己的新位置
	// 故意不给 cachedAbs["dir-1"]
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evFolderRename, FileID: "dir-1", Cid: "d1",
			FileName: "新名", FileCat: "0", Time: "100"},
	}}

	if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
		t.Fatal(err)
	}
	if d.walkCalls != 1 {
		t.Fatalf("应回退重遍历一次，实得 %d", d.walkCalls)
	}
	// 重扫的必须是**改名的这个目录自己**，不是它的父目录。
	// 改造前回退遍历的是父目录 d1（这里是「剧集」这一整个分类），
	// 一次目录改名就等于整个分类重扫，几百个目录 × 1 秒节流
	if got := d.walked[0]; got.cid != "dir-1" || got.base != "媒体库/剧集/新名" || !got.deep {
		t.Fatalf("应深遍历改名后的目录自身，实得 %+v", got)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "旧名", "E01.mkv.strm")); err != nil {
		t.Fatalf("拿不到旧路径时不该动本地文件: %v", err)
	}
}

// 目录【移动】同样走本地搬迁。
// 改造前这条路是断的：台账按 file_id 存的是文件行，目录的 fid 不在里面，
// removeSyncedItem 找不到就返回 false，旧树永远留着
func TestIncrDirMoveRelocates(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_dirmove.db")
	mkTree(t, p.LocalPath, "媒体库/待归类/某剧/E01.mkv.strm")

	d.cachedAbs["dir-1"] = "/影视/待归类/某剧"
	d.abs["d2"] = "/影视/剧集/国产剧"
	d.rel["d2"] = "剧集/国产剧"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evMove, FileID: "dir-1", Cid: "d2",
			FileName: "某剧", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moved != 1 {
		t.Fatalf("应记一次搬迁，实得 Moved=%d", sum.Moved)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "国产剧", "某剧", "E01.mkv.strm")); err != nil {
		t.Fatalf("本地目录应已搬到新位置: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "待归类", "某剧")); !os.IsNotExist(err) {
		t.Fatal("旧位置不该残留")
	}
}

// 被抑制的事件跳过本地动作，但**路径缓存照样更新** ——
// 整理搬移目录产生的正是这类事件，跳过前不更新缓存，缓存就永久停在旧路径上
func TestIncrSuppressedDirMoveStillUpdatesCache(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_suppress_cache.db")
	mkTree(t, p.LocalPath, "媒体库/待归类/某剧/E01.mkv.strm")

	markSuppressed("move", []string{"dir-1"})
	d.cachedAbs["dir-1"] = "/影视/待归类/某剧"
	d.abs["d2"] = "/影视/冗余"
	d.rel["d2"] = "冗余"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evMove, FileID: "dir-1", Cid: "d2",
			FileName: "某剧", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Suppressed != 1 {
		t.Fatalf("应被抑制，实得 Suppressed=%d", sum.Suppressed)
	}
	if sum.Moved != 0 {
		t.Fatal("被抑制的事件不该产生本地动作")
	}
	// 关键：缓存必须已经指向新位置
	if got := d.cachedAbs["dir-1"]; got != "/影视/冗余/某剧" {
		t.Fatalf("抑制不该妨碍缓存更新，实得 %q", got)
	}
	// 本地文件原样（整理自己已经落过盘了）
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "待归类", "某剧", "E01.mkv.strm")); err != nil {
		t.Fatalf("被抑制的事件不该动本地文件: %v", err)
	}
}

// 目录删除事件要把那棵子树的路径缓存清掉
func TestIncrDirDeleteDropsCache(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_dirdelete.db")
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evDelete, FileID: "dir-1", Cid: "d1",
			FileName: "某剧", FileCat: "0", Time: "100"},
	}}

	if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
		t.Fatal(err)
	}
	if len(d.goneDirs) != 1 || d.goneDirs[0] != "dir-1" {
		t.Fatalf("应清掉被删目录的缓存，实得 %v", d.goneDirs)
	}
}
