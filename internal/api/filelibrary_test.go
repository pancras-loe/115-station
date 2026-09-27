package api

import (
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

// 片目目录 = 分类目录的下一层；分类目录本身、多级分类的上级、更深的季目录都不是
func TestIsTitleRel(t *testing.T) {
	l := buildLibCategoryLayout([]model.CategoryRule{
		{MediaType: "movie", Name: "电影"},
		{MediaType: "tv", Name: "动漫番剧"},
		{MediaType: "tv", Name: "电视剧/日番"},
		{MediaType: "tv", Name: "电视剧"},
	})
	for rel, want := range map[string]bool{
		"电影/流浪地球.2019.{tmdbid=535167}": true,
		"动漫番剧/葬送的芙莉莲":                  true,
		"电视剧/繁花":                       true,
		"电视剧/日番/孤独摇滚":                  true,
		"电视剧/日番":                       false, // 是分类
		"电影":                           false, // 分类目录本身
		"动漫番剧/葬送的芙莉莲/Season 01":        false, // 季目录
		"旧分类/某片":                       false, // 不在当前分类策略里
		"":                             false,
	} {
		if got := l.isTitleRel(rel); got != want {
			t.Errorf("isTitleRel(%q) = %v，预期 %v", rel, got, want)
		}
	}
}

func TestMoveItemBlock(t *testing.T) {
	roles := map[string]string{"100": "library", "200": "pending", "300": "redundant", "400": "existing"}
	l := buildLibCategoryLayout([]model.CategoryRule{{MediaType: "movie", Name: "电影"}})
	inCat := []browseCrumb{{"100", "影视"}, {"101", "电影"}}
	title := fileJobItem{ID: "102", Name: "流浪地球", IsDir: true}

	if r := moveItemBlock(inCat, roles, l, title, "redundant"); r != "" {
		t.Fatalf("片目目录应能移出媒体库: %s", r)
	}
	if r := moveItemBlock([]browseCrumb{{"100", "影视"}}, roles, l, fileJobItem{ID: "101", Name: "电影", IsDir: true}, "redundant"); r == "" {
		t.Fatal("分类目录不能移动")
	}
	if r := moveItemBlock(append(inCat, browseCrumb{"102", "流浪地球"}), roles, l, fileJobItem{ID: "f", Name: "a.mkv"}, "pending"); r == "" {
		t.Fatal("片目里的文件不能单独移出媒体库")
	}
	if r := moveItemBlock(nil, roles, l, fileJobItem{ID: "300", Name: "冗余", IsDir: true}, "pending"); r == "" {
		t.Fatal("工作区根目录不能移动")
	}
	if r := moveItemBlock([]browseCrumb{{"300", "冗余"}}, roles, l, fileJobItem{ID: "301", Name: "某片", IsDir: true}, "redundant"); r == "" {
		t.Fatal("已经在目标工作区里的不用移")
	}
	if r := moveItemBlock([]browseCrumb{{"300", "冗余"}}, roles, l, fileJobItem{ID: "302", Name: "b.mkv"}, "pending"); r != "" {
		t.Fatalf("媒体库外的文件可以移: %s", r)
	}
}

func TestLibTitleRecord(t *testing.T) {
	it := fileJobItem{ID: "t1", Name: "狂飙.2023.{tmdbid=207468}", IsDir: true}
	files := []remoteFile{
		{Fid: "a", Name: "狂飙.S01E01.mkv", Path: it.Name + "/Season 01", Size: 10, PickCode: "pa"},
		{Fid: "b", Name: "狂飙.S01E02.mkv", Path: it.Name + "/Season 01", Size: 20},
		{Fid: "c", Name: "poster.jpg", Path: it.Name},
	}
	rec := libTitleRecord(it, "cat", "剧集/"+it.Name, "", files)
	if rec.TmdbID != 207468 || rec.Title != "狂飙" || rec.Year != "2023" {
		t.Fatalf("旧编号 / 片名要从目录名取（重新整理据此判断是否认错了片）: %+v", rec)
	}
	if rec.MediaType != "tv" {
		t.Fatalf("同名分类时按集号判断类型: %s", rec.MediaType)
	}
	// 落点就是它现在的位置：重新整理才能判断「没变 → 原地刷新」
	if rec.Status != "success" || rec.TargetCid != "t1" || rec.TargetDir != "剧集/"+it.Name || rec.Category != "剧集" {
		t.Fatalf("记录目标不符: %+v", rec)
	}
	if rec.VideoCount != 2 || rec.TotalSize != 30 {
		t.Fatalf("视频计数不符: %d / %d", rec.VideoCount, rec.TotalSize)
	}
	fs := unmarshalRecordFiles(rec.Files)
	if len(fs) != 3 || fs[0].Dir != it.Name+"/Season 01" || fs[2].Kind == "video" {
		t.Fatalf("文件清单要带所在子目录（季号靠它）: %+v", fs)
	}
}

func TestMovedTitleLocalDir(t *testing.T) {
	root := t.TempDir()
	if _, ok := movedTitleLocalDir(root, "影视/电影"); ok {
		t.Fatal("少于 库名/分类/片目 三段不删")
	}
	if _, ok := movedTitleLocalDir(root, "影视/../../x"); ok {
		t.Fatal("算出来跑到本地根外面的不删")
	}
	if _, ok := movedTitleLocalDir("", "影视/电影/某片"); ok {
		t.Fatal("没有本地根不删")
	}
	if d, ok := movedTitleLocalDir(root, "影视/电影/某片"); !ok || d != filepath.Join(root, "影视", "电影", "某片") {
		t.Fatalf("d=%s ok=%v", d, ok)
	}
}

// 移出媒体库：只清这一部（同前缀的「狂飙 2」不动），台账、本地、库存台账、整理记录一起收拾
func TestCleanupMovedTitle(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{{MediaType: "tv", Name: "剧集"}}, []model.SyncedFile{
		{FileID: "a", Kind: "video", RelPath: "影视/剧集/狂飙/Season 01/E01.mkv.strm"},
		{FileID: "b", Kind: "asset", RelPath: "影视/剧集/狂飙/Season 01/E01.chs.ass"},
		{FileID: "c", Kind: "video", RelPath: "影视/剧集/狂飙 2/Season 01/E01.mkv.strm"},
	})
	model.DB.Create(&[]model.MediaLibrary{
		{TmdbID: 1, MediaType: "tv", TargetPath: "剧集/狂飙/Season 01/E01.mkv"},
		{TmdbID: 2, MediaType: "tv", TargetPath: "剧集/狂飙 2/Season 01/E01.mkv"},
	})
	model.DB.Create(&[]model.OrganizeRecord{
		{Status: "success", TargetCid: "cid-kb", Message: "旧"},
		{Status: "success", TargetCid: "cid-other", Message: "别的"},
	})
	root := deepDelTestTree(t,
		"影视/剧集/狂飙/Season 01/E01.mkv.strm",
		"影视/剧集/狂飙/tvshow.nfo", // 刮削写的，不在台账里，也要清
		"影视/剧集/狂飙 2/Season 01/E01.mkv.strm",
	)

	if n := cleanupMovedTitle(root, "影视", "剧集/狂飙", "cid-kb", "冗余"); n != 2 {
		t.Fatalf("应清掉 2 行台账，实际 %d", n)
	}
	var left []model.SyncedFile
	model.DB.Find(&left)
	if len(left) != 1 || left[0].FileID != "c" {
		t.Fatalf("同前缀的兄弟片目不该被动: %+v", left)
	}
	if _, err := os.Stat(filepath.Join(root, "影视", "剧集", "狂飙")); !os.IsNotExist(err) {
		t.Fatalf("本地片目目录应整个删掉: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "影视", "剧集", "狂飙 2", "Season 01", "E01.mkv.strm")); err != nil {
		t.Fatalf("兄弟片目的本地文件被误删: %v", err)
	}
	var libs []model.MediaLibrary
	model.DB.Find(&libs)
	if len(libs) != 1 || libs[0].TmdbID != 2 {
		t.Fatalf("库存台账只清这一部: %+v", libs)
	}
	var recs []model.OrganizeRecord
	model.DB.Order("id").Find(&recs)
	// target_cid 要清空：否则之后对这条记录「重新整理」会被判成原地刷新，文件留在冗余里
	if recs[0].Stage != "moved" || recs[0].Message != "已移出媒体库 → 冗余" || recs[0].TargetCid != "" || recs[1].Message != "别的" {
		t.Fatalf("整理记录更新不符: %+v", recs)
	}
}
