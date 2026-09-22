package api

import (
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// newCfgHandler 造一个能读写 setting 的 Handler
func newCfgHandler(t *testing.T, dbName string) *Handler {
	t.Helper()
	newTestDB(t, dbName)
	return &Handler{
		DB:     model.DB,
		Config: &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()},
	}
}

// 独立轮询默认开启（30 秒）。用指针区分「没配过」与「显式填 0」，
// 否则老用户升级上来会被当成显式关闭
func TestIncrIntervalDefaults(t *testing.T) {
	h := newCfgHandler(t, "poll_default.db")

	if got := h.loadIncrInterval(); got != incrIntervalDefault {
		t.Fatalf("没配过应取默认 %v，实得 %v", incrIntervalDefault, got)
	}

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{`{"cron":"*/10 * * * *"}`, incrIntervalDefault}, // 只配了 cron，没提 interval
		{`{"interval_sec":60}`, 60 * time.Second},
		{`{"interval_sec":5}`, incrIntervalMin}, // 低于下限收敛
		{`{"interval_sec":0}`, 0},               // 显式关闭（逃生门）
		{`{"interval_sec":-1}`, 0},
	}
	for _, c := range cases {
		if err := h.Config.SaveSetting("incr", c.raw); err != nil {
			t.Fatal(err)
		}
		if got := h.loadIncrInterval(); got != c.want {
			t.Fatalf("%s → 期望 %v，实得 %v", c.raw, c.want, got)
		}
	}
}

// cron 与 interval 共存于同一个 setting，互不干扰
func TestIncrCronAndIntervalCoexist(t *testing.T) {
	h := newCfgHandler(t, "poll_coexist.db")
	if err := h.Config.SaveSetting("incr", `{"cron":"*/10 8-23 * * *","interval_sec":45}`); err != nil {
		t.Fatal(err)
	}
	if got := h.loadIncrCron(); got != "*/10 8-23 * * *" {
		t.Fatalf("cron 读错: %q", got)
	}
	if got := h.loadIncrInterval(); got != 45*time.Second {
		t.Fatalf("interval 读错: %v", got)
	}
}

// 整理的 cron 命中时锁被占用 → 置位待补，而不是直接放弃这一轮。
//
// 改造前是 TryLock 失败直接 return。增量提频到 30 秒之后，整理的 cron
// 撞上一轮正在遍历大目录的增量是常态，一错过就要等下一个 cron 周期
func TestOrganizeCatchUpOnMissedSlot(t *testing.T) {
	h := newCfgHandler(t, "poll_missed.db")
	organizeMissed.Store(false)
	t.Cleanup(func() { organizeMissed.Store(false) })

	// 测试里不真等 90 秒
	defer func(d time.Duration) { organizeAcquireWait = d }(organizeAcquireWait)
	organizeAcquireWait = 10 * time.Millisecond

	if !taskMu.TryLock("测试占用") {
		t.Fatal("锁应当是空闲的")
	}
	h.runScheduledTick() // 锁被别人占着，等不到就该置位待补
	taskMu.Unlock()

	if !organizeMissed.Load() {
		t.Fatal("锁被占用时应置位待补")
	}
}

// 媒体库没配时轮询直接跳过，不去打 115
func TestIncrPollSkipsWithoutLibrary(t *testing.T) {
	h := newCfgHandler(t, "poll_nolib.db")
	if err := h.Config.SaveSetting("full", `{"cid":""}`); err != nil {
		t.Fatal(err)
	}
	// 没有 cid 时应直接返回；真去执行会因为拿不到 cookie 而报错刷屏
	h.runIncrPollTick()
}
