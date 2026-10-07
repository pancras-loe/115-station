package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 假 TMDB：tv/600 是还在播的两季剧，movie/1 是电影
func botSubFixture(t *testing.T) *Handler {
	t.Helper()
	newTestDB(t, "botsub.db")
	resetTmdbCaches()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/600":
			fmt.Fprint(w, `{"id":600,"name":"剧","first_air_date":"2020-01-01","status":"Returning Series",
				"seasons":[{"season_number":0,"episode_count":2},{"season_number":1,"episode_count":8},{"season_number":2,"episode_count":8}]}`)
		case "/3/movie/1":
			fmt.Fprint(w, `{"id":1,"title":"片名","release_date":"2024-03-01"}`)
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	model.DB.Where("1 = 1").Delete(&model.TmdbConfig{})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k", ApiUrl: srv.URL})
	return &Handler{DB: model.DB}
}

func TestBotSubFindSingleHit(t *testing.T) {
	h := botSubFixture(t)
	v := h.botSubFind("bs1", "600", botIO{})
	if !strings.Contains(v.text(), "已订阅《剧》") || !strings.Contains(v.text(), "第 2 季") {
		t.Fatalf("应直接订阅最新一季: %q", v.text())
	}
	var sub model.Subscription
	model.DB.First(&sub)
	if sub.TmdbID != 600 || sub.Scope != subScopeSeason || sub.Season != 2 || sub.Follow != subFollowMissing {
		t.Fatalf("订阅默认值不对: %+v", sub)
	}
	if botFlowGet("bs1") != nil {
		t.Fatal("订阅完会话应关掉")
	}
	if v = h.botSubFind("bs1", "600", botIO{}); !strings.Contains(v.text(), "已经订阅过了") {
		t.Fatalf("重复订阅: %q", v.text())
	}
}

// 找资源的列表里点「订阅这部」：列表不动，另发一条结果
func TestBotFlowSubscribeFromResources(t *testing.T) {
	h := botSubFixture(t)
	f, _ := botFlowFixture(t, "bs2", botItems(3))
	if v := f.render(); !strings.Contains(v.Hint, "s 订阅这部") {
		t.Fatalf("资源列表应有订阅入口: %q", v.Hint)
	}
	v, ok := h.botAct("bs2", "s", "", botIO{say: func(...string) {}})
	if !ok || !v.Toast || !strings.Contains(v.text(), "已订阅《片名》") {
		t.Fatalf("订阅这部: %+v", v)
	}
	if botFlowGet("bs2") == nil {
		t.Fatal("订阅后找资源会话应保留")
	}
	var sub model.Subscription
	if model.DB.Where("tmdb_id = 1 AND media_type = 'movie'").First(&sub).Error != nil || sub.Scope != "" {
		t.Fatalf("电影订阅: %+v", sub)
	}
}

func TestBotSubsManage(t *testing.T) {
	h := botSubFixture(t)
	if v := h.botSubsList("bs3"); !strings.Contains(v.text(), "还没有订阅") {
		t.Fatalf("空列表: %q", v.text())
	}
	model.DB.Create(&model.Subscription{TmdbID: 600, MediaType: "tv", Title: "剧", Scope: subScopeAll, Follow: subFollowMissing, State: subStateActive, Have: 3, Total: 8, Missing: 5})
	model.DB.Create(&model.Subscription{TmdbID: 1, MediaType: "movie", Title: "片名", State: subStateDone, Have: 1, Total: 1})

	v := h.botSubsList("bs3")
	if !strings.Contains(v.text(), "1. 剧 追更中 · 已有 3/8 集，缺 5") || !strings.Contains(v.text(), "2. 片名 已完成 · 已入库") {
		t.Fatalf("列表: %q", v.text())
	}
	var said []string
	io := botIO{say: func(l ...string) { said = append(said, l...) }}
	act := func(a string) botView {
		v, ok := h.botAct("bs3", a, "", io)
		if !ok {
			t.Fatalf("操作 %s 没被会话接住", a)
		}
		return v
	}

	if v = act("1"); !strings.Contains(v.text(), "《剧》") || !strings.Contains(v.text(), "全剧 · 补缺集") {
		t.Fatalf("单个订阅: %q", v.text())
	}
	act("z")
	var sub model.Subscription
	model.DB.Where("tmdb_id = 600").First(&sub)
	if sub.State != subStatePaused {
		t.Fatalf("应已暂停: %+v", sub)
	}
	act("z")
	model.DB.First(&sub, sub.ID)
	if sub.State != subStateActive || sub.NextCheckAt == nil {
		t.Fatalf("应已恢复并马上检查: %+v", sub)
	}

	act("a")
	var jobs int64
	model.DB.Model(&model.TaskJob{}).Where("kind = ?", jobKindSubscribe).Count(&jobs)
	if jobs != 1 {
		t.Fatalf("立即搜索应入队: %d", jobs)
	}

	if v = act("xx"); !strings.Contains(v.text(), "先点") {
		t.Fatalf("没点取消就确认不该删: %q", v.text())
	}
	if v = act("x"); !strings.Contains(v.text(), "确定取消订阅") {
		t.Fatalf("取消要确认: %q", v.text())
	}
	v = act("xx")
	var n int64
	model.DB.Model(&model.Subscription{}).Count(&n)
	if n != 1 || !strings.Contains(v.text(), "订阅 1 个") {
		t.Fatalf("取消后回到列表: %d %q", n, v.text())
	}
	if !strings.Contains(strings.Join(said, "\n"), "已取消订阅《剧》") {
		t.Fatalf("应提示已取消: %v", said)
	}
}

func TestTgCommandSubscribe(t *testing.T) {
	for in, want := range map[string][2]string{
		"/sub 三体":   {"sub", "三体"},
		"订阅 三体":     {"sub", "三体"},
		"/subs":     {"subs", ""},
		"我的订阅":      {"subs", ""},
		"订阅列表":      {"subs", ""},
		"xx":        {"flow", "xx"},
		"/search x": {"search", "x"},
	} {
		cmd, args := tgCommand(in)
		if cmd != want[0] || args != want[1] {
			t.Errorf("%q → %q %q, want %v", in, cmd, args, want)
		}
	}
}
