package api

import (
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

// 2026-10-06 现场：越狱 S04E22 两份同名，网盘上删一份、另一份改名，增量只删了 STRM，
// 「X.mkv.nfo」「X.mkv-thumb.jpg」留在本地。增量删 STRM 时连集 NFO / 剧照一起删，
// 同目录另一个视频（新写法 X.strm）的那一套不碰
func TestIncrRemoveSyncedFileDropsCompanions(t *testing.T) {
	newTestDB(t, "incr_companion.db")
	root := t.TempDir()
	season := "影视/剧集/越狱/Season 4"
	x := "越狱.S04E22.REMUX"
	for _, n := range []string{x + ".strm", x + ".nfo", x + "-thumb.jpg",
		x + ".mkv.strm", x + ".mkv.nfo", x + ".mkv-thumb.jpg"} {
		writeTestFile(t, filepath.Join(root, season, n))
	}
	model.DB.Create(&model.SyncedFile{FileID: "a", Kind: "video", RelPath: season + "/" + x + ".strm"})
	model.DB.Create(&model.SyncedFile{FileID: "b", Kind: "video", RelPath: season + "/" + x + ".mkv.strm"})

	h := &Handler{DB: model.DB}
	if got := h.removeSyncedFile("b", root); got == "" {
		t.Fatal("应删掉 b 的 STRM")
	}
	for _, gone := range []string{x + ".mkv.strm", x + ".mkv.nfo", x + ".mkv-thumb.jpg"} {
		if exists(filepath.Join(root, season, gone)) {
			t.Fatalf("%s 应已删除", gone)
		}
	}
	for _, kept := range []string{x + ".strm", x + ".nfo", x + "-thumb.jpg"} {
		if !exists(filepath.Join(root, season, kept)) {
			t.Fatalf("%s 是另一份的，不该删", kept)
		}
	}
}
