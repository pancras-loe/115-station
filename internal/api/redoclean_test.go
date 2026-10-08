package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

func writeTestFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 删 STRM 时它的集 NFO / 剧照一起删；同目录别的集、前缀相近的另一集、台账里的文件都不碰
// （2026-10-04 现场：只删了 STRM，旧季目录里留着一排 NFO 和剧照，目录永远删不掉）
func TestDropLocalByFidsRemovesStrmCompanions(t *testing.T) {
	newTestDB(t, "companions.db")
	root := t.TempDir()
	season := "库/动漫/旧剧/Season 1"
	writeTestFile(t, filepath.Join(root, season, "剧.S01E1.strm"))
	writeTestFile(t, filepath.Join(root, season, "剧.S01E1.nfo"))
	writeTestFile(t, filepath.Join(root, season, "剧.S01E1-thumb.jpg"))
	writeTestFile(t, filepath.Join(root, season, "剧.S01E10.nfo"))    // 前缀相近的另一集
	writeTestFile(t, filepath.Join(root, season, "剧.S01E1.chs.ass")) // 不是刮削产物
	model.DB.Create(&model.SyncedFile{FileID: "e1", Kind: "video", RelPath: season + "/剧.S01E1.strm"})
	model.DB.Create(&model.SyncedFile{FileID: "sub", Kind: "subtitle", RelPath: season + "/剧.S01E1.chs.ass"})

	removed, _ := dropLocalByFidsQuiet(root, []string{"e1"}, nil)
	if len(removed) != 1 {
		t.Fatalf("应删 1 个 STRM，实际 %v", removed)
	}
	for _, gone := range []string{"剧.S01E1.strm", "剧.S01E1.nfo", "剧.S01E1-thumb.jpg"} {
		if exists(filepath.Join(root, season, gone)) {
			t.Fatalf("%s 应已删除", gone)
		}
	}
	for _, kept := range []string{"剧.S01E10.nfo", "剧.S01E1.chs.ass"} {
		if !exists(filepath.Join(root, season, kept)) {
			t.Fatalf("%s 不该被删", kept)
		}
	}
}

// 旧标题目录只剩刮削产物时整棵收掉；还有 STRM / 台账行 / 未知文件就不动（或留目录）
func TestPurgeStaleTitleDir(t *testing.T) {
	newTestDB(t, "purge.db")
	root := t.TempDir()
	title := "库/动漫/旧剧 (1992)"
	full := filepath.Join(root, filepath.FromSlash(title))
	seed := func() {
		writeTestFile(t, filepath.Join(full, "tvshow.nfo"))
		writeTestFile(t, filepath.Join(full, "poster.jpg"))
		writeTestFile(t, filepath.Join(full, "Season 1", "season.nfo"))
		writeTestFile(t, filepath.Join(full, "Season 1", "剧.S01E01.nfo"))
	}

	t.Run("只剩元数据：整棵收掉", func(t *testing.T) {
		seed()
		dir, n := purgeStaleTitleDir(root, title)
		if dir != full || n != 4 || exists(full) {
			t.Fatalf("应整棵收掉，dir=%q n=%d exists=%v", dir, n, exists(full))
		}
		if !exists(filepath.Join(root, "库", "动漫")) {
			t.Fatal("分类目录不该被删")
		}
	})

	t.Run("还有 STRM：不动", func(t *testing.T) {
		seed()
		writeTestFile(t, filepath.Join(full, "Season 2", "剧.S02E01.strm"))
		if dir, n := purgeStaleTitleDir(root, title); dir != "" || n != 0 || !exists(filepath.Join(full, "tvshow.nfo")) {
			t.Fatalf("有 STRM 时不该动，dir=%q n=%d", dir, n)
		}
		os.RemoveAll(full)
	})

	t.Run("台账里还有行：不动", func(t *testing.T) {
		seed()
		model.DB.Create(&model.SyncedFile{FileID: "m1", Kind: "meta", RelPath: title + "/poster.jpg"})
		if dir, _ := purgeStaleTitleDir(root, title); dir != "" || !exists(filepath.Join(full, "poster.jpg")) {
			t.Fatalf("台账有行时不该动，dir=%q", dir)
		}
		model.DB.Where("file_id = ?", "m1").Delete(&model.SyncedFile{})
		os.RemoveAll(full)
	})

	t.Run("有未知文件：只删元数据，留目录", func(t *testing.T) {
		seed()
		writeTestFile(t, filepath.Join(full, "用户的笔记.txt"))
		dir, n := purgeStaleTitleDir(root, title)
		if dir != "" || n != 4 || !exists(filepath.Join(full, "用户的笔记.txt")) {
			t.Fatalf("未知文件要留着，dir=%q n=%d", dir, n)
		}
		os.RemoveAll(full)
	})

	t.Run("分类目录 / 越界：拒绝", func(t *testing.T) {
		writeTestFile(t, filepath.Join(root, "库", "x.nfo"))
		for _, bad := range []string{"库", "", "../外面", "/"} {
			if dir, n := purgeStaleTitleDir(root, bad); dir != "" || n != 0 {
				t.Fatalf("%q 不该被收，dir=%q n=%d", bad, dir, n)
			}
		}
		if !exists(filepath.Join(root, "库", "x.nfo")) {
			t.Fatal("库目录下的文件被误删")
		}
	})
}

// 本站删了一集集 STRM / 旧标题目录，Emby 报回来的是 Series / Season：按路径包含关系认作回声
func TestEmbySelfDeletedRelated(t *testing.T) {
	embySelfDelMu.Lock()
	embySelfDel = map[string]embySelfDelMark{}
	embySelfDelMu.Unlock()
	markEmbySelfDeleted("/media/动漫/旧剧/Season 4/剧.S04E01.strm")
	if !embySelfDeletedRelated("/media/动漫/旧剧") || !embySelfDeletedRelated(`\media\动漫\旧剧\Season 4\`) {
		t.Fatal("剧集 / 季目录应认作回声")
	}
	if embySelfDeletedRelated("/media/动漫/别的剧") {
		t.Fatal("无关路径不该认")
	}
	if embySelfDeleted("/media/动漫/旧剧") {
		t.Fatal("精确比对不该变宽")
	}
}

// 回查放弃的路径，Emby 入库事件（Series 路径）到了能摘出来，只摘一次
func TestTakeEmbyLateIngest(t *testing.T) {
	embyLateIngest.Lock()
	embyLateIngest.m = map[string]time.Time{}
	embyLateIngest.Unlock()
	markEmbyLateIngest([]string{"/media/动漫/新剧/Season 4/剧.S04E01.strm", "/media/电影/别的片/片.strm"})
	got := takeEmbyLateIngest("/media/动漫/新剧")
	if len(got) != 1 || got[0] != "/media/动漫/新剧/Season 4/剧.S04E01.strm" {
		t.Fatalf("摘出不符: %v", got)
	}
	if again := takeEmbyLateIngest("/media/动漫/新剧"); len(again) != 0 {
		t.Fatalf("不该摘第二次: %v", again)
	}
}
