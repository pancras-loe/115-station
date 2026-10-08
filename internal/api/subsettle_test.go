package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

func TestSettleSubAttempt(t *testing.T) {
	now := time.Now()
	sub := &model.Subscription{TmdbID: 100, MediaType: "tv"}
	share := &model.SubAttempt{Kind: "share", CreatedAt: now.Add(-time.Hour)}
	rec := func(status string, tmdb int, title string) model.OrganizeRecord {
		return model.OrganizeRecord{Status: status, TmdbID: tmdb, MediaType: "tv", Title: title, Message: status + "了"}
	}
	cases := []struct {
		name   string
		a      *model.SubAttempt
		recs   []model.OrganizeRecord
		status string
		retry  bool
	}{
		{"还没整理", share, nil, subAttemptInflight, false},
		{"分享超时", &model.SubAttempt{Kind: "share", CreatedAt: now.Add(-7 * time.Hour)}, nil, subAttemptFailed, true},
		{"离线没超时", &model.SubAttempt{Kind: "offline", CreatedAt: now.Add(-7 * time.Hour)}, nil, subAttemptInflight, false},
		{"入库", share, []model.OrganizeRecord{rec("success", 100, "剧")}, subAttemptIngested, false},
		{"已存在也算补上", share, []model.OrganizeRecord{rec("exists", 100, "剧")}, subAttemptIngested, false},
		{"等确认", share, []model.OrganizeRecord{rec("success", 100, "剧"), rec("awaiting", 0, "")}, subAttemptInflight, false},
		{"认错", share, []model.OrganizeRecord{rec("success", 200, "别的剧")}, subAttemptRejected, false},
		{"部分", share, []model.OrganizeRecord{rec("success", 100, "剧"), rec("failed", 0, "")}, subAttemptPartial, false},
		{"失败", share, []model.OrganizeRecord{rec("unrecognized", 0, "")}, subAttemptFailed, true},
	}
	for _, c := range cases {
		r := settleSubAttempt(sub, c.a, c.recs, now)
		if r.Status != c.status || r.Retry != c.retry {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

func TestSettleSubscriptionAndOfflineFailed(t *testing.T) {
	newTestDB(t, "subsettle.db")
	now := time.Now()
	sub := &model.Subscription{ID: 1, TmdbID: 100, MediaType: "tv"}
	ok := model.SubAttempt{SubID: 1, LinkID: 11, Kind: "share", Status: subAttemptInflight, CreatedAt: now}
	wrong := model.SubAttempt{SubID: 1, LinkID: 12, Kind: "share", Status: subAttemptInflight, CreatedAt: now}
	off := model.SubAttempt{SubID: 1, LinkID: 13, Kind: "offline", Status: subAttemptInflight, CreatedAt: now}
	for _, a := range []*model.SubAttempt{&ok, &wrong, &off} {
		model.DB.Create(a)
	}
	model.DB.Create(&model.OrganizeRecord{LinkID: 11, Status: "success", TmdbID: 100, MediaType: "tv"})
	model.DB.Create(&model.OrganizeRecord{LinkID: 12, Status: "success", TmdbID: 999, MediaType: "tv", Title: "别的"})

	rejected, landed := settleSubscription(model.DB, sub, now)
	if len(rejected) != 1 || rejected[0].ID != wrong.ID {
		t.Fatalf("认错的应推通知: %+v", rejected)
	}
	if len(landed) != 1 || landed[0].ID != ok.ID || landed[0].Status != subAttemptIngested {
		t.Fatalf("入库的应推「补上了」: %+v", landed)
	}
	model.DB.First(&ok, ok.ID)
	if ok.Status != subAttemptIngested || ok.ResolvedAt == nil {
		t.Fatalf("应判入库: %+v", ok)
	}

	subMarkOfflineFailed(model.DB, 13, "某种子")
	model.DB.First(&off, off.ID)
	if off.Status != subAttemptFailed || off.RetryAt != nil {
		t.Fatalf("离线失败应判失败、不自动重试（再提交又扣一次配额）: %+v", off)
	}
}

func TestSubLandedEpisodes(t *testing.T) {
	e := func(s, ep int) epKey { return epKey{S: s, E: ep} }
	landed := []model.SubAttempt{
		{Status: subAttemptIngested, Episodes: marshalEpKeys([]epKey{e(1, 5), e(1, 6)})},
		// partial：E07 没整理成、回到了缺；E06 与上一条重复
		{Status: subAttemptPartial, Episodes: marshalEpKeys([]epKey{e(1, 6), e(1, 7)})},
	}
	ev := subEval{Missing: []epKey{e(1, 7)}, Inflight: []epKey{e(1, 8)}}
	got := subLandedEpisodes(landed, ev)
	if len(got) != 2 || got[0] != e(1, 5) || got[1] != e(1, 6) {
		t.Fatalf("补上的应是 E05 E06: %v", got)
	}
	sub := &model.Subscription{Title: "三体", MediaType: "tv"}
	if txt := subLandedText(sub, got, ev); txt != "订阅《三体》补上 S01E05–E06（2 集）\n还差 2 集（1 集在路上）" {
		t.Fatalf("正文: %q", txt)
	}
	next := time.Date(2026, 10, 12, 3, 0, 0, 0, time.Local)
	if txt := subLandedText(sub, got, subEval{NextAt: next}); txt != "订阅《三体》补上 S01E05–E06（2 集）\n已追平，下一集 10-12 播出" {
		t.Fatalf("追平的正文: %q", txt)
	}
}

func TestLoadSubscribeCfgNotifyMigrate(t *testing.T) {
	newTestDB(t, "subcfg.db")
	save := func(v string) {
		model.DB.Where("key = ?", "subscribe").Delete(&model.Setting{})
		model.DB.Create(&model.Setting{Key: "subscribe", Value: v})
	}
	save(`{"notify":"submit,done"}`)
	if c := loadSubscribeCfg(); c.Notify != "submit,done,ingested,created" || c.NotifyVer != subNotifyVer {
		t.Fatalf("老配置应补上 ingested 与 created: %+v", c)
	}
	save(`{"notify":""}`)
	if c := loadSubscribeCfg(); c.Notify != "" {
		t.Fatalf("原来都不推的不替用户打开: %q", c.Notify)
	}
	// 版本 1 时保存过：关掉的 ingested 不补回来，版本 2 新加的 created 补上
	save(`{"notify":"submit","notify_ver":1}`)
	if c := loadSubscribeCfg(); c.Notify != "submit,created" {
		t.Fatalf("只补保存之后新加的类型: %q", c.Notify)
	}
	save(`{"notify":"submit","notify_ver":2}`)
	if c := loadSubscribeCfg(); c.Notify != "submit" {
		t.Fatalf("当前版本保存时关掉的不补回来: %q", c.Notify)
	}
}

func TestPlanSubNext(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	next := now.Add(48 * time.Hour)
	cases := []struct {
		name   string
		ev     subEval
		sub    bool
		prev   string
		rounds int
		want   time.Time
		state  string
		stall  bool
		after  int
	}{
		{"完成", subEval{Done: true}, false, subStateActive, 3, time.Time{}, subStateDone, false, 0},
		{"提交了", subEval{Missing: epKeys("S01E01")}, true, subStateActive, 5, now.Add(subAfterSubmit), subStateActive, false, 0},
		{"只剩在路上", subEval{Inflight: epKeys("S01E01")}, false, subStateActive, 0, now.Add(subAfterSubmit), subStateActive, false, 0},
		{"不缺等下一集", subEval{NextAt: next}, false, subStateActive, 0, next, subStateActive, false, 0},
		{"不缺没排期", subEval{}, false, subStateActive, 0, now.Add(24 * time.Hour), subStateActive, false, 0},
		{"没找到第 1 轮", subEval{Missing: epKeys("S01E01")}, false, subStateActive, 0, now.Add(time.Hour), subStateActive, false, 1},
		{"没找到第 3 轮", subEval{Missing: epKeys("S01E01")}, false, subStateActive, 2, now.Add(6 * time.Hour), subStateActive, false, 3},
		{"没找到很多轮", subEval{Missing: epKeys("S01E01")}, false, subStateActive, 10, now.Add(24 * time.Hour), subStateActive, false, 11},
		{"新集更早播：提前回来", subEval{Missing: epKeys("S01E01"), NextAt: now.Add(2 * time.Hour)}, false, subStateActive, 10, now.Add(2 * time.Hour), subStateActive, false, 11},
		{"变成长期找不到", subEval{Missing: epKeys("S01E01")}, false, subStateActive, subStalledRounds - 1, now.Add(subStalledEvery), subStateStalled, true, subStalledRounds},
		{"已经长期找不到", subEval{Missing: epKeys("S01E01")}, false, subStateStalled, subStalledRounds, now.Add(subStalledEvery), subStateStalled, false, subStalledRounds + 1},
	}
	for _, c := range cases {
		s := planSubNext(c.ev, c.sub, c.prev, c.rounds, now)
		if !s.Next.Equal(c.want) || s.State != c.state || s.BecameStall != c.stall || s.EmptyRounds != c.after {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}
