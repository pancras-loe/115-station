package api

import (
	"path/filepath"
	"testing"

	"strmhub/internal/model"
)

// 刮削依赖台账，覆盖不同目录深度以及剧集聚合，避免遗漏片目。
func TestScanLedgerTitles(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	db, err := model.InitDB(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	files := []model.SyncedFile{
		{FileID: "movie", Kind: "video", RelPath: "媒体库/电影/动作/Z-测试电影-2024-[tmdb=123]/电影.mkv"},
		{FileID: "episode1", Kind: "video", RelPath: "媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]/Season 01/S01E01.mkv"},
		{FileID: "episode2", Kind: "video", RelPath: "媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]/Season 02/S02E01.mkv"},
		{FileID: "short", Kind: "video", RelPath: "电影/测试短路径-2023-[tmdb=789]/电影.mkv"},
		{FileID: "invalid", Kind: "video", RelPath: "电影.mkv"},
		{FileID: "other", Kind: "video", RelPath: "媒体库/其他/分类/未知/电影.mkv"},
		{FileID: "asset", Kind: "asset", RelPath: "媒体库/电影/动作/海报-2024-[tmdb=999]/poster.jpg"},
	}
	if err := db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	entries := scanLedgerTitles()
	if len(entries) != 3 {
		t.Fatalf("应聚合为三个片目，实际 %d: %+v", len(entries), entries)
	}
	for _, want := range []struct {
		key, title, year, kind string
		tmdb                   int
	}{
		{"媒体库/电影/动作/Z-测试电影-2024-[tmdb=123]", "测试电影", "2024", "movie", 123},
		{"媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]", "测试剧集", "2025", "tv", 456},
		{"电影/测试短路径-2023-[tmdb=789]", "测试短路径", "2023", "movie", 789},
	} {
		got := entries[want.key]
		if got == nil {
			t.Errorf("缺少片目 %s", want.key)
			continue
		}
		if got.Key != want.key || got.Title != want.title || got.Year != want.year || got.MediaType != want.kind || got.TmdbID != want.tmdb {
			t.Errorf("片目信息不符: %+v，预期 %+v", got, want)
		}
	}
}
