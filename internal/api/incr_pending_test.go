package api

import (
	"testing"
	"time"

	"strmhub/internal/model"
)

// insertSyncEvents 只能返回【真正新插入】的行。
//
// 它此前只返回条数，调用方无从区分新旧，只好把整页事件都当新的塞进 pending：
// 一页里只要有 1 条新事件，同页那些早已 applied 的历史事件就跟着被重放一遍。
// 重放一条 delete 事件的后果不是白跑——台账行那时已经没了，removeSyncedItem
// 会落到路径推导那一级，把用户后来重新上传的同名文件删掉
func TestInsertSyncEventsReturnsOnlyFresh(t *testing.T) {
	newTestDB(t, "incrpending.db")

	applied := time.Now()
	model.DB.Create(&model.SyncEvent{
		EventID: "e-old", Type: evDelete, FileName: "旧片.mkv",
		Status: "applied", AppliedAt: &applied,
	})

	fresh := insertSyncEvents(model.DB, []model.SyncEvent{
		{EventID: "e-old", Type: evDelete, FileName: "旧片.mkv"},
		{EventID: "e-new", Type: evUpload, FileName: "新片.mkv"},
	})

	if len(fresh) != 1 {
		t.Fatalf("应只返回 1 条新事件，实得 %d 条", len(fresh))
	}
	if fresh[0].EventID != "e-new" {
		t.Fatalf("返回的应是新事件 e-new，实得 %s", fresh[0].EventID)
	}

	// 已应用的行不能被这次插入改回 pending——改回去它就会被 stale 查询重新捞出来消费
	var old model.SyncEvent
	if err := model.DB.Where("event_id = ?", "e-old").First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.Status != "applied" {
		t.Fatalf("已应用事件的状态被改写成 %q", old.Status)
	}
}

// 批内去重与跨批幂等由 full115_test.go 的 TestInsertSyncEvents 覆盖，这里不重复。

// 空输入与 db 为 nil 都不该崩，也不该插出行来
func TestInsertSyncEventsEmpty(t *testing.T) {
	newTestDB(t, "incrpending_empty.db")

	if got := insertSyncEvents(model.DB, nil); got != nil {
		t.Fatalf("空批次应返回 nil，实得 %v", got)
	}
	if got := insertSyncEvents(nil, []model.SyncEvent{{EventID: "x"}}); got != nil {
		t.Fatalf("db 为 nil 时应返回 nil，实得 %v", got)
	}
	var n int64
	model.DB.Model(&model.SyncEvent{}).Count(&n)
	if n != 0 {
		t.Fatalf("不该插出行来，实得 %d 行", n)
	}
}

// 「上轮遗留的 pending」与「本轮新插入」必然重叠：本轮刚插入的行状态就是 pending，
// stale 那条查询会把它们一起捞出来。不去重就是同一个事件在一轮里被应用两次
func TestMergePendingEventsDedupes(t *testing.T) {
	fresh := []model.SyncEvent{
		{EventID: "e-2", EventTime: 200},
		{EventID: "e-3", EventTime: 300},
	}
	// stale 里既有真正的遗留事件（e-1），也有本轮刚插入、被一起捞出来的（e-2/e-3）
	stale := []model.SyncEvent{
		{EventID: "e-1", EventTime: 100},
		{EventID: "e-2", EventTime: 200},
		{EventID: "e-3", EventTime: 300},
	}

	merged := mergePendingEvents(stale, fresh)
	if len(merged) != 3 {
		t.Fatalf("去重后应为 3 条，实得 %d 条", len(merged))
	}
	seen := map[string]int{}
	for _, ev := range merged {
		seen[ev.EventID]++
	}
	for _, id := range []string{"e-1", "e-2", "e-3"} {
		if seen[id] != 1 {
			t.Fatalf("%s 出现 %d 次，应为 1 次", id, seen[id])
		}
	}
}

// stale 自己有重复行时也要收敛（同一 event_id 理论上有唯一索引兜底，
// 但这个函数不该依赖调用方的输入干净）
func TestMergePendingEventsDedupesWithinStale(t *testing.T) {
	merged := mergePendingEvents(
		[]model.SyncEvent{{EventID: "e-1"}, {EventID: "e-1"}},
		nil,
	)
	if len(merged) != 1 {
		t.Fatalf("stale 内部去重后应为 1 条，实得 %d 条", len(merged))
	}
}

// 没有遗留事件时原样返回本轮的行，不做多余拷贝
func TestMergePendingEventsNoStale(t *testing.T) {
	fresh := []model.SyncEvent{{EventID: "e-1"}}
	merged := mergePendingEvents(nil, fresh)
	if len(merged) != 1 || merged[0].EventID != "e-1" {
		t.Fatalf("无遗留事件时应原样返回，实得 %v", merged)
	}
}
