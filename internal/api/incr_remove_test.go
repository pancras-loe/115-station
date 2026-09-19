package api

import (
	"os"
	"path/filepath"
	"testing"

	"strmhub/internal/model"
)

// ==================== removeSyncedItem 的四级兜底 ====================
//
// 台账里的 rel_path 一律带库名前缀。第 2 级「路径推导」此前漏了这一层，
// 永远匹配不上，活儿全落到第 4 级的全库按名搜索上 ——
// 而那一级是按裸文件名全盘匹配后 RemoveAll，重复片名在媒体库里极常见。

// 第 2 级：台账里没有记录（台账启用前同步的历史文件），靠路径推导删掉
func TestRemoveSyncedItemLevel2UsesLibPrefix(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "remove_level2.db")
	rel := "媒体库/剧集/X/片.mkv.strm"
	abs := filepath.Join(p.LocalPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("x"), 0o644)

	ev := model.SyncEvent{Type: evDelete, FileID: "无台账", Cid: "d1", FileName: "片.mkv"}
	if !h.removeSyncedItem(d, ev, p.Cid, "媒体库", p.LocalPath, true, false) {
		t.Fatal("应能通过路径推导定位并删除")
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatal("文件应已被删除")
	}
}

// 第 2 级：整目录删除。这条分支此前是死的 —— 推导出的路径少一层库名，
// os.Stat 永远失败，根本走不到 RemoveAll
func TestRemoveSyncedItemLevel2DeletesDirectory(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "remove_level2_dir.db")
	newTestDBRows(t,
		"媒体库/剧集/X/某剧/E01.mkv.strm",
		"媒体库/剧集/X/某剧/E02.mkv.strm",
	)
	for _, rel := range []string{"媒体库/剧集/X/某剧/E01.mkv.strm", "媒体库/剧集/X/某剧/E02.mkv.strm"} {
		abs := filepath.Join(p.LocalPath, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte("x"), 0o644)
	}

	ev := model.SyncEvent{Type: evDelete, FileID: "dir-x", Cid: "d1", FileName: "某剧", FileCat: "0"}
	if !h.removeSyncedItem(d, ev, p.Cid, "媒体库", p.LocalPath, true, false) {
		t.Fatal("整目录删除应成功")
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "X", "某剧")); !os.IsNotExist(err) {
		t.Fatal("目录应已整棵删除")
	}
	var n int64
	model.DB.Model(&model.SyncedFile{}).Count(&n)
	if n != 0 {
		t.Fatalf("台账应一并清掉，还剩 %d 行", n)
	}
}

// 第 4 级只在推导出的子树内搜：别的剧集目录下的同名文件绝不能被误删。
// 重复片名在媒体库里极常见，这是改造前全库扫描最危险的地方
func TestRemoveSyncedItemLevel4StaysInSubtree(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "remove_level4_scope.db")

	// 事件指向的目录（d1 = 剧集/X），但文件在更深一层，第 2 级的精确路径匹配不到
	inScope := filepath.Join(p.LocalPath, "媒体库", "剧集", "X", "Season 01", "片.mkv.strm")
	os.MkdirAll(filepath.Dir(inScope), 0o755)
	os.WriteFile(inScope, []byte("x"), 0o644)

	// 另一棵子树下的同名文件 —— 不该被碰
	outScope := filepath.Join(p.LocalPath, "媒体库", "电影", "别的片", "片.mkv.strm")
	os.MkdirAll(filepath.Dir(outScope), 0o755)
	os.WriteFile(outScope, []byte("x"), 0o644)

	ev := model.SyncEvent{Type: evDelete, FileID: "无台账", Cid: "d1", FileName: "片.mkv"}
	h.removeSyncedItem(d, ev, p.Cid, "媒体库", p.LocalPath, true, false)

	if _, err := os.Stat(outScope); err != nil {
		t.Fatalf("范围外的同名文件被误删了: %v", err)
	}
}

// 推不出目录（父目录也被删了）→ 放弃，一个字节都不能动。
// 宁可漏删留给失效 STRM 检测
func TestRemoveSyncedItemGivesUpWithoutScope(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "remove_noscope.db")
	abs := filepath.Join(p.LocalPath, "媒体库", "剧集", "X", "片.mkv.strm")
	os.MkdirAll(filepath.Dir(abs), 0o755)
	os.WriteFile(abs, []byte("x"), 0o644)

	// cid 解析不出相对路径
	ev := model.SyncEvent{Type: evDelete, FileID: "无台账", Cid: "未知目录", FileName: "片.mkv"}
	if h.removeSyncedItem(d, ev, p.Cid, "媒体库", p.LocalPath, true, false) {
		t.Fatal("推不出范围时不该报告删除成功")
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("推不出范围时不该动任何文件: %v", err)
	}
}

// 第 1 级（台账精确匹配）依然优先，且不受范围限制影响
func TestRemoveSyncedItemLevel1LedgerFirst(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "remove_level1.db")
	rel := "媒体库/剧集/X/片.mkv.strm"
	abs := filepath.Join(p.LocalPath, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(abs), 0o755)
	os.WriteFile(abs, []byte("x"), 0o644)
	model.DB.Create(&model.SyncedFile{FileID: "f-1", RelPath: rel, Kind: "video"})

	ev := model.SyncEvent{Type: evDelete, FileID: "f-1", Cid: "d1", FileName: "片.mkv"}
	if !h.removeSyncedItem(d, ev, p.Cid, "媒体库", p.LocalPath, true, false) {
		t.Fatal("台账有记录时应直接命中第 1 级")
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatal("文件应已被删除")
	}
}

// newTestDBRows 往台账里塞几行（本地文件由调用方自己造）
func newTestDBRows(t *testing.T, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		model.DB.Create(&model.SyncedFile{FileID: rel, RelPath: rel, Kind: "video"})
	}
}
