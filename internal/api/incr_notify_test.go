package api

import (
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

// 覆盖台账精确删除、目录推导与父目录未知时的台账兜底，通知必须采用实际删掉的位置。
func TestIncrDeletionNotifiesActualPaths(t *testing.T) {
	for _, mode := range []string{"file", "directory", "unknown-parent"} {
		t.Run(mode, func(t *testing.T) {
			h, d, p := newIncrTestEnv(t, "incr_notify_"+mode+".db")
			d.names["lib"] = "影视"
			d.abs["movies"] = "/影视/电影"
			d.rel["movies"] = "电影"
			rel := "影视/电影/海洋奇缘.2026/movie.mkv.strm"
			full := filepath.Join(p.LocalPath, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("strm"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := h.DB.Create(&model.SyncedFile{FileID: "movie-file", RelPath: rel, Kind: "video"}).Error; err != nil {
				t.Fatal(err)
			}
			ev := lifeEvent{ID: "delete-event", Type: evDelete, FileID: "movie-dir", Cid: "movies", FileName: "海洋奇缘.2026", FileCat: "0", Time: "100"}
			want := filepath.Dir(full)
			if mode == "file" {
				ev.FileID, ev.FileName, ev.FileCat = "movie-file", "movie.mkv", "1"
				want = full
			} else if mode == "unknown-parent" {
				ev.Cid = "0"
			}
			// 同轮在另一个库新增文件，删除通知不能被新增刷新的目录覆盖。
			d.pages = [][]lifeEvent{{ev}}
			if mode != "unknown-parent" {
				d.pages[0] = append(d.pages[0], lifeEvent{ID: "upload-event", Type: evUpload, FileID: "new-file", Cid: "d1", FileName: "new.mkv", PickCode: "pc-new", Time: "101"})
			}
			sum, err := h.executeIncrementalSyncWith(d, p)
			if err != nil {
				t.Fatal(err)
			}
			if sum.Deleted != 1 || len(d.deleted) != 1 || d.deleted[0] != want {
				t.Fatalf("删除通知应为 %q，实际 %v，删除数 %d", want, d.deleted, sum.Deleted)
			}
			if _, err := os.Stat(full); !os.IsNotExist(err) {
				t.Fatalf("文件未删除: %v", err)
			}
		})
	}
}

func TestIncrMoveNotifiesOldDirectory(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_notify_move.db")
	mkTree(t, p.LocalPath, "媒体库/旧目录/movie.mkv.strm")
	d.cachedAbs["dir-1"] = "/影视/旧目录"
	d.pages = [][]lifeEvent{{{ID: "move-event", Type: evMove, FileID: "dir-1", Cid: "d1", FileName: "新目录", FileCat: "0", Time: "100"}}}
	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(p.LocalPath, "媒体库", "旧目录")
	if sum.Moved != 1 || len(d.deleted) != 1 || d.deleted[0] != want {
		t.Fatalf("应通知搬迁前的目录 %q，实际 %v，移动数 %d", want, d.deleted, sum.Moved)
	}
	newPath := filepath.Join(p.LocalPath, "媒体库", "剧集", "X", "新目录")
	if len(d.refreshed) != 1 || d.refreshed[0] != newPath {
		t.Fatalf("搬迁后还应发现新目录 %q，实际 %v", newPath, d.refreshed)
	}
}

// 在网盘上手动给一集改个名：本地 STRM 跟着换名字，Emby 扫完推回来的
// library.new 是同一集的回声，必须标记出来，否则用户改一次名就收一条假「入库」
func TestIncrRenameMarksEmbyEcho(t *testing.T) {
	resetEmbyChangeMarks()
	h, d, p := newIncrTestEnv(t, "incr_notify_rename.db")
	rel := "媒体库/剧集/X/旧名.mkv.strm"
	mkTree(t, p.LocalPath, rel)
	h.DB.Model(&model.SyncedFile{}).Where("rel_path = ?", rel).Update("file_id", "f1")

	d.pages = [][]lifeEvent{{{ID: "rename-1", Type: evRename, FileID: "f1", Cid: "d1",
		FileName: "新名.mkv", FileCat: "1", PickCode: "pc-1", Time: "100"}}}
	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moved != 1 || sum.StrmCreated != 1 {
		t.Fatalf("应是一次改名重建，实得 移改 %d、新增 STRM %d", sum.Moved, sum.StrmCreated)
	}
	seriesDir := filepath.Join(p.LocalPath, "媒体库", "剧集", "X")
	if !embyRenameEcho(filepath.Join(seriesDir, "新名.mkv.strm")) {
		t.Fatal("改名后的 STRM 路径没被认成回声")
	}
	// 实测 Emby 这时推的是剧集条目（路径是剧集目录），不是那个 .strm
	if !embyRenameEcho(seriesDir) {
		t.Fatal("改名所在的剧集目录没被认成回声")
	}
	if embyRenameEcho(filepath.Join(p.LocalPath, "媒体库", "剧集", "别的剧")) {
		t.Fatal("别的剧集不该被抑制")
	}
}

// 同一轮里既有改名回声、又真的来了新集：目录级的入库事件必须照常通知，
// 只有改名出来的那条路径本身算回声
func TestIncrRenameEchoKeepsRealAdds(t *testing.T) {
	resetEmbyChangeMarks()
	h, d, p := newIncrTestEnv(t, "incr_notify_rename_mixed.db")
	rel := "媒体库/剧集/X/旧名.mkv.strm"
	mkTree(t, p.LocalPath, rel)
	h.DB.Model(&model.SyncedFile{}).Where("rel_path = ?", rel).Update("file_id", "f1")

	d.pages = [][]lifeEvent{{
		{ID: "rename-1", Type: evRename, FileID: "f1", Cid: "d1",
			FileName: "新名.mkv", FileCat: "1", PickCode: "pc-1", Time: "100"},
		{ID: "upload-1", Type: evUpload, FileID: "f2", Cid: "d1",
			FileName: "新集.mkv", FileCat: "1", PickCode: "pc-2", Time: "101"},
	}}
	if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
		t.Fatal(err)
	}
	seriesDir := filepath.Join(p.LocalPath, "媒体库", "剧集", "X")
	if embyRenameEcho(seriesDir) {
		t.Fatal("这一轮真的新增了一集，剧集条目的入库通知不能被吞")
	}
	if !embyRenameEcho(filepath.Join(seriesDir, "新名.mkv.strm")) {
		t.Fatal("改名出来的那条路径仍是回声")
	}
}
