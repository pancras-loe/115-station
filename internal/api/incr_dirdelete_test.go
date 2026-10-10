package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

// ==================== 网盘删目录：按目录自己的 id 认位置 ====================
//
// 2026-10-10 现场：网盘上删掉整理建的「成长的烦恼/Season 0」，删除事件的父目录
// 定位不到媒体库内，退回按名字找 —— 库里每部剧都有 Season 0，认不出是哪一个，
// 静默跳过，本地 13 个 STRM 一直留着。

// 事件父目录不可用（cid=0），缓存里记着这个目录的位置：只删这一部的 Season 0
func TestIncrDirDeleteByCachedPath(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_dirdel_cached.db")
	mkTree(t, p.LocalPath,
		"媒体库/剧集/X/Season 0/特别篇.strm",
		"媒体库/剧集/X/Season 1/E01.strm",
		"媒体库/剧集/Y/Season 0/特别篇.strm",
	)
	d.cachedAbs["s0"] = "/影视/剧集/X/Season 0"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evDelete, FileID: "s0", Cid: "0", FileName: "Season 0", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Deleted != 1 {
		t.Fatalf("应删掉 1 个目录，实得 Deleted=%d", sum.Deleted)
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, "媒体库", "剧集", "X", "Season 0")); !os.IsNotExist(err) {
		t.Fatalf("X 的 Season 0 应已删除: %v", err)
	}
	for _, keep := range []string{"媒体库/剧集/X/Season 1/E01.strm", "媒体库/剧集/Y/Season 0/特别篇.strm"} {
		if _, err := os.Stat(filepath.Join(p.LocalPath, filepath.FromSlash(keep))); err != nil {
			t.Fatalf("%s 不该被删: %v", keep, err)
		}
	}
	got := ledgerPaths(t)
	want := []string{"媒体库/剧集/X/Season 1/E01.strm", "媒体库/剧集/Y/Season 0/特别篇.strm"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("台账应只清掉被删目录下的行，实得 %v", got)
	}
	if len(d.panDeleted) != 1 {
		t.Fatalf("网盘真删了，应推一次删除通知，实得 %v", d.panDeleted)
	}
}

// 缓存里的名字和事件对不上（目录后来改过名、缓存没跟上）：不按缓存删
func TestIncrDirDeleteIgnoresStaleCachedName(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_dirdel_stale.db")
	mkTree(t, p.LocalPath,
		"媒体库/剧集/X/Season 0/特别篇.strm",
		"媒体库/剧集/Y/Season 0/特别篇.strm",
	)
	d.cachedAbs["s0"] = "/影视/剧集/X/Specials"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evDelete, FileID: "s0", Cid: "0", FileName: "Season 0", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Deleted != 0 {
		t.Fatalf("名字对不上不该删，实得 Deleted=%d", sum.Deleted)
	}
	if len(ledgerPaths(t)) != 2 {
		t.Fatalf("两部剧的 Season 0 都该留着，实得 %v", ledgerPaths(t))
	}
}

// 缓存说这个目录在整理工作区里：不动媒体库
func TestIncrDirDeleteInWorkspaceIgnored(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incr_dirdel_ws.db")
	d.settings["org-basic"] = `{"pending":"w1"}`
	d.abs["w1"] = "/影视/待整理"
	mkTree(t, p.LocalPath, "媒体库/剧集/X/Season 0/特别篇.strm")
	d.cachedAbs["s0"] = "/影视/待整理/X/Season 0"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evDelete, FileID: "s0", Cid: "0", FileName: "Season 0", FileCat: "0", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Deleted != 0 || sum.Ignored != 1 {
		t.Fatalf("工作区里的删除应忽略，实得 Deleted=%d Ignored=%d", sum.Deleted, sum.Ignored)
	}
	if len(ledgerPaths(t)) != 1 {
		t.Fatal("媒体库里的同名目录不该被删")
	}
}

// 遍历时列到的子目录要记进路径缓存，删除时才认得出
func TestWalkRemembersSubdirPaths(t *testing.T) {
	newTestDB(t, "walk_remember.db")
	l := seasonTree()
	f := &syncFilter{videoExts: buildExtSet([]string{".mkv"}), assetExts: map[string]bool{}}
	var videos []remoteFile
	if err := walk115DirCtl(l, "cat", "库/剧集", &videos, nil, f, nil, &walkCtl{panAbs: "/影视/剧集"}); err != nil {
		t.Fatal(err)
	}
	for cid, want := range map[string]string{"show": "/影视/剧集/某剧", "s1": "/影视/剧集/某剧/Season 01"} {
		if got, ok := lookupDirAbsAnyAge(cid); !ok || got != want {
			t.Fatalf("%s 应记成 %s，实得 %q（%v）", cid, want, got, ok)
		}
	}
}

// 过了保鲜期的缓存行照样拿得到（删除专用），失效时也一起清掉
func TestLookupDirAbsAnyAgeAndForget(t *testing.T) {
	newTestDB(t, "pathcache_anyage.db")
	old := time.Now().Add(-30 * 24 * time.Hour)
	model.DB.Create(&model.PathCache{FileID: "aa-s0", Name: "Season 0", Path: "/影视/剧集/X/Season 0", UpdatedAt: old})
	model.DB.Model(&model.PathCache{}).Where("file_id = ?", "aa-s0").Update("updated_at", old)

	if _, ok := lookupCachedAbs("aa-s0"); ok {
		t.Fatal("普通查询应按保鲜期判过期")
	}
	if got, ok := lookupDirAbsAnyAge("aa-s0"); !ok || got != "/影视/剧集/X/Season 0" {
		t.Fatalf("删除专用查询不看保鲜期，实得 %q", got)
	}
	forgetDirSubtree("aa-s0")
	if _, ok := lookupDirAbsAnyAge("aa-s0"); ok {
		t.Fatal("过期行也要随失效清掉")
	}
}

// 整理建目录时父目录位置在缓存里：记下新目录；不在就不记（不为它发请求）
func TestRememberDirAt(t *testing.T) {
	newTestDB(t, "remember_dir_at.db")
	rememberDirPaths([]model.PathCache{{FileID: "lib", Name: "影视", Path: "/影视"}})

	rememberDirAt("lib", "剧集/X/Season 0", "s0")
	if got, _ := lookupDirAbsAnyAge("s0"); got != "/影视/剧集/X/Season 0" {
		t.Fatalf("应记成 /影视/剧集/X/Season 0，实得 %q", got)
	}
	rememberDirAt("unknown-parent", "Season 1", "rda-s1")
	if _, ok := lookupDirAbsAnyAge("rda-s1"); ok {
		t.Fatal("父目录位置不在缓存里时不该记")
	}
}
