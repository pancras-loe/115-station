package api

import (
	"testing"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"
)

func TestCronMatch(t *testing.T) {
	// 2026-08-15 10:20:30 周六
	ts := time.Date(2026, 8, 15, 10, 20, 30, 0, time.Local)
	cases := []struct {
		expr string
		want bool
	}{
		{"*/10 8-23 * * *", true},   // 每10分钟，8-23点（10:20 命中）
		{"*/15 * * * *", false},     // 20 不被 15 整除
		{"30 * * * *", false},       // 分不对
		{"20 10 * * *", true},       // 分时精确命中
		{"* * 15 8 6", true},        // 日月周全命中（周六=6）
		{"* * * * 0", false},        // 周日不命中
		{"0 0 1 1 *", false},        // 元旦
		{"1,5,10-30 * * * *", true}, // 列表+范围：20 在 10-30
		{"bad expr", false},         // 非法表达式
		{"* * * *", false},          // 字段数不足
	}
	for _, c := range cases {
		if got := CronMatch(c.expr, ts); got != c.want {
			t.Errorf("CronMatch(%q, %v) = %v, want %v", c.expr, ts, got, c.want)
		}
	}
}

// 定时全量只服务于失效 STRM 检测：检测关掉后，即便配置里还留着
// cron_enabled 与表达式，调度器也必须当没开——否则界面上看不到开关，
// 后台却在每天偷偷跑一次整库扫描（115 风控高危）
func TestLoadFullCronGatedByOrphanDetect(t *testing.T) {
	if _, err := model.InitDB("file:fullcron_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"检测+开关都开", `{"detect_orphans":true,"cron_enabled":true,"cron":"0 4 * * *"}`, "0 4 * * *"},
		{"检测关", `{"detect_orphans":false,"cron_enabled":true,"cron":"0 4 * * *"}`, ""},
		{"开关关", `{"detect_orphans":true,"cron_enabled":false,"cron":"0 4 * * *"}`, ""},
		{"表达式为空", `{"detect_orphans":true,"cron_enabled":true,"cron":"  "}`, ""},
		{"配置损坏", `not-json`, ""},
	}
	for _, c := range cases {
		model.DB.Where("1=1").Delete(&model.Setting{})
		model.DB.Create(&model.Setting{Key: "full", Value: c.value})
		if got := h.loadFullCron(); got != c.want {
			t.Errorf("%s: loadFullCron() = %q, want %q", c.name, got, c.want)
		}
	}
}

// pending 事件卡死的逃生阀：某个网盘目录持续读不出来时增量会整轮放弃，
// 这批事件本来会每 10 分钟重放一次、永不落地
func TestPruneSyncEventsReleasesStuckPending(t *testing.T) {
	newTestDB(t, "prune_events.db")
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	now := time.Now()
	rows := []model.SyncEvent{
		{EventID: "applied-old", Status: "applied", CreatedAt: now.AddDate(0, 0, -40)},
		{EventID: "applied-new", Status: "applied", CreatedAt: now.AddDate(0, 0, -1)},
		{EventID: "pending-stuck", Status: "pending", CreatedAt: now.AddDate(0, 0, -8)},
		{EventID: "pending-fresh", Status: "pending", CreatedAt: now.AddDate(0, 0, -1)},
	}
	for i := range rows {
		// GORM 的 autoCreateTime 会覆盖 CreatedAt，直接指定列写入
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		model.DB.Model(&model.SyncEvent{}).Where("event_id = ?", rows[i].EventID).
			UpdateColumn("created_at", rows[i].CreatedAt)
	}

	lastPruneDay = "" // 每日只跑一次的闸门
	h.pruneSyncEvents()

	// 每次查询都用新变量：GORM 的 First 见到 dest 里已有主键会再加一条
	// id = ? 条件，复用同一个结构体会查出空结果，白白误判
	find := func(eventID string) (model.SyncEvent, bool) {
		var row model.SyncEvent
		err := model.DB.Where("event_id = ?", eventID).First(&row).Error
		return row, err == nil
	}

	if _, ok := find("applied-old"); ok {
		t.Error("30 天前的已应用事件应被删除")
	}
	if _, ok := find("applied-new"); !ok {
		t.Error("近期的已应用事件不该被删")
	}
	stuck, ok := find("pending-stuck")
	if !ok {
		t.Fatal("卡死的 pending 事件应保留但改状态，而不是删除")
	}
	if stuck.Status != "applied" {
		t.Errorf("积压超 7 天的 pending 应停止重试，实际 status=%s", stuck.Status)
	}
	fresh, ok := find("pending-fresh")
	if !ok || fresh.Status != "pending" {
		t.Error("近期的 pending 事件必须原样留着继续重试")
	}
}
