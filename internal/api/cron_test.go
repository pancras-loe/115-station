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
		{"*/10 8-23 * * *", true},  // 每10分钟，8-23点（10:20 命中）
		{"*/15 * * * *", false},    // 20 不被 15 整除
		{"30 * * * *", false},      // 分不对
		{"20 10 * * *", true},      // 分时精确命中
		{"* * 15 8 6", true},       // 日月周全命中（周六=6）
		{"* * * * 0", false},       // 周日不命中
		{"0 0 1 1 *", false},       // 元旦
		{"1,5,10-30 * * * *", true}, // 列表+范围：20 在 10-30
		{"bad expr", false},        // 非法表达式
		{"* * * *", false},         // 字段数不足
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
