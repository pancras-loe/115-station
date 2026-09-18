package api

import (
	"testing"

	"strmhub/internal/model"
)

// TestUpsertSyncedFiles 批量 upsert：插入 → 同 file_id 更新 → 批间去重
func TestUpsertSyncedFiles(t *testing.T) {
	if _, err := model.InitDB("file:upsert_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer func() { model.DB = nil }()

	rows := []model.SyncedFile{
		{FileID: "v1", PickCode: "pc1", RelPath: "lib/movie/a/one.strm", Kind: "video", Size: 100, Sha1: "aa"},
		{FileID: "v2", PickCode: "pc2", RelPath: "lib/movie/a/two.strm", Kind: "video", Size: 200, Sha1: "bb"},
	}
	upsertSyncedFiles(model.DB, rows)
	var n int64
	model.DB.Model(&model.SyncedFile{}).Count(&n)
	if n != 2 {
		t.Fatalf("首批应插入 2 行，实得 %d", n)
	}

	// 同 file_id 再 upsert：更新而非新增（pick_code/size 变化生效）
	rows2 := []model.SyncedFile{
		{FileID: "v1", PickCode: "pc1x", RelPath: "lib/movie/a/one.strm", Kind: "video", Size: 300, Sha1: "aa"},
		{FileID: "v3", PickCode: "pc3", RelPath: "lib/movie/a/three.strm", Kind: "video", Size: 400, Sha1: "cc"},
	}
	upsertSyncedFiles(model.DB, rows2)
	model.DB.Model(&model.SyncedFile{}).Count(&n)
	if n != 3 {
		t.Fatalf("更新+新增后应为 3 行，实得 %d", n)
	}
	var v1 model.SyncedFile
	if err := model.DB.Where("file_id = ?", "v1").First(&v1).Error; err != nil {
		t.Fatal(err)
	}
	if v1.PickCode != "pc1x" || v1.Size != 300 {
		t.Fatalf("v1 应被更新为 pc1x/300，实得 %s/%d", v1.PickCode, v1.Size)
	}

	// 批内重复 file_id：靠 SQLite ON CONFLICT 不炸即可（最终一致）
	upsertSyncedFiles(model.DB, []model.SyncedFile{
		{FileID: "v1", PickCode: "pc1y", RelPath: "x", Kind: "video"},
		{FileID: "v1", PickCode: "pc1z", RelPath: "x", Kind: "video"},
	})
	model.DB.Model(&model.SyncedFile{}).Count(&n)
	if n != 3 {
		t.Fatalf("批内重复不应新增行，实得 %d", n)
	}
}

// TestInsertSyncEvents 事件批量插入：新事件计数 / 重复跳过 / 二次调用幂等
func TestInsertSyncEvents(t *testing.T) {
	if _, err := model.InitDB("file:incev_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer func() { model.DB = nil }()

	batch := []model.SyncEvent{
		{EventID: "e1", Type: "add", FileID: "f1"},
		{EventID: "e2", Type: "add", FileID: "f2"},
		{EventID: "e2", Type: "add", FileID: "f2"}, // 批内重复
	}
	if n := insertSyncEvents(model.DB, batch); n != 2 {
		t.Fatalf("首批应新增 2（批内 e2 去重），实得 %d", n)
	}
	// 幂等：再来一批含旧事件 + 新事件
	batch2 := []model.SyncEvent{
		{EventID: "e1", Type: "add", FileID: "f1"}, // 已存在
		{EventID: "e3", Type: "del", FileID: "f3"},
	}
	if n := insertSyncEvents(model.DB, batch2); n != 1 {
		t.Fatalf("第二批应只新增 1，实得 %d", n)
	}
	var n int64
	model.DB.Model(&model.SyncEvent{}).Count(&n)
	if n != 3 {
		t.Fatalf("总事件应为 3，实得 %d", n)
	}
}
