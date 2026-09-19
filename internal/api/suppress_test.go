package api

import (
	"testing"
	"time"

	"strmhub/internal/model"
)

// 抑制表的核心约定：查的时候**不消费**。
// 增量同步遇到目录读取失败会整轮放弃重来，查时就删的话下一轮没标记可命中，
// 整理自产的 move 会被当成外部变更，把整理刚生成的 STRM 删掉
func TestPeekDoesNotConsume(t *testing.T) {
	newTestDB(t, "suppress.db")

	markSuppressed("move", []string{"fid-a", "fid-b"})
	if !peekSuppressed("fid-a") {
		t.Fatal("首次命中应返回 true")
	}
	if !peekSuppressed("fid-a") {
		t.Fatal("查询不该消费标记：重来一轮仍要命中")
	}

	// 事件落定后由 unmarkSuppressed 统一清
	unmarkSuppressed("fid-a")
	if peekSuppressed("fid-a") {
		t.Fatal("清理后不该再命中——之后的真实手动移动必须能正常处理")
	}
	if !peekSuppressed("fid-b") {
		t.Fatal("清理 fid-a 不该连累 fid-b")
	}

	if peekSuppressed("fid-never") || peekSuppressed("") {
		t.Fatal("没登记过 / 空 fid 不该被抑制")
	}
}

// 过期标记不抑制事件，并顺手删掉 —— 留着会让很久以后的一次真实移动被误跳过
func TestSuppressExpired(t *testing.T) {
	newTestDB(t, "suppress_exp.db")

	model.DB.Create(&model.EventSuppress{
		FileID: "old", Op: "move", ExpireAt: time.Now().Add(-time.Hour),
	})
	if peekSuppressed("old") {
		t.Fatal("过期标记不该抑制事件")
	}
	var n int64
	model.DB.Model(&model.EventSuppress{}).Where("file_id = ?", "old").Count(&n)
	if n != 0 {
		t.Fatal("过期标记应在命中时一并删除")
	}
}

// 同一个 fid 先 rename 再 move：后写的覆盖前一条，不应插出两行
func TestSuppressUpsert(t *testing.T) {
	newTestDB(t, "suppress_upsert.db")

	markSuppressed("rename", []string{"fid-x"})
	markSuppressed("move", []string{"fid-x"})
	var rows []model.EventSuppress
	model.DB.Where("file_id = ?", "fid-x").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("同一 fid 应只有一行，实际 %d", len(rows))
	}
	if rows[0].Op != "move" {
		t.Fatalf("应被后写的操作覆盖，实际 %s", rows[0].Op)
	}
}

func TestPruneEventSuppress(t *testing.T) {
	newTestDB(t, "suppress_prune.db")

	model.DB.Create(&model.EventSuppress{FileID: "stale", ExpireAt: time.Now().Add(-time.Hour)})
	model.DB.Create(&model.EventSuppress{FileID: "fresh", ExpireAt: time.Now().Add(time.Hour)})
	pruneEventSuppress()

	var n int64
	model.DB.Model(&model.EventSuppress{}).Count(&n)
	if n != 1 {
		t.Fatalf("只应留下未过期的一行，实际 %d", n)
	}
}

// 整理没能自己落盘时必须撤销抑制，否则文件两头落空：
// 整理没写 STRM、增量又把它的事件跳过了
func TestUnmarkSuppressed(t *testing.T) {
	newTestDB(t, "suppress_unmark.db")

	markSuppressed("move", []string{"kept", "handover"})
	unmarkSuppressed("handover")

	if peekSuppressed("handover") {
		t.Fatal("撤销后不该再被抑制")
	}
	if !peekSuppressed("kept") {
		t.Fatal("没撤销的标记应仍然生效")
	}
	unmarkSuppressed() // 空参数不该炸
}
