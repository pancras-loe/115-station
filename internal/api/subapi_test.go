package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

func TestApplySubForm(t *testing.T) {
	now := time.Now()
	tv := &model.Subscription{MediaType: "tv"}
	if _, err := applySubForm(tv, subForm{Scope: "range", Season: 2, EpStart: 5, EpEnd: 3}, now); err == nil {
		t.Fatal("结束集号小于起始应报错")
	}
	if _, err := applySubForm(tv, subForm{Scope: "whatever"}, now); err == nil {
		t.Fatal("认不出的范围应报错")
	}
	changed, err := applySubForm(tv, subForm{Scope: "season", Season: 2, Specials: true, Follow: "new", Sources: []string{"pansou", "不存在"}, RankLimit: -3, Include: " 4K "}, now)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if tv.Scope != subScopeSeason || tv.Season != 2 || tv.Specials || tv.Follow != subFollowNew || tv.FollowFrom == nil ||
		tv.Sources != `["pansou"]` || tv.RankLimit != 0 || tv.Include != "4K" {
		t.Fatalf("表单落库不对: %+v", tv)
	}
	from := *tv.FollowFrom
	// 再存一次同样的：只追新集的起点不能被刷新，范围没变
	changed, _ = applySubForm(tv, subForm{Scope: "season", Season: 2, Follow: "new"}, now.Add(time.Hour))
	if changed || !tv.FollowFrom.Equal(from) {
		t.Fatalf("没改范围却判了变化 / 起点被刷新: %v %v", changed, tv.FollowFrom)
	}
	mv := &model.Subscription{MediaType: "movie"}
	if _, err := applySubForm(mv, subForm{Scope: "season", Season: 3, Follow: "new"}, now); err != nil || mv.Scope != "" || mv.Follow != "" {
		t.Fatalf("电影不该带剧集范围: %+v %v", mv, err)
	}
}

func subAPIReq(t *testing.T, h gin.HandlerFunc, method, target, body string, params gin.Params) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	h(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestSubscriptionAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTestDB(t, "subapi.db")
	subTestLayout(t)
	resetTmdbCaches()
	scrapeSeasonMu.Lock()
	scrapeSeasonCache = map[string]seasonCacheEntry{}
	scrapeSeasonMu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/tv/600":
			fmt.Fprint(w, `{"id":600,"name":"剧","original_name":"Show","first_air_date":"2020-01-01","poster_path":"/p.jpg","status":"Ended","seasons":[{"season_number":1,"episode_count":3,"name":"第 1 季"}]}`)
		case "/3/tv/600/season/1":
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"air_date":"2020-01-01"},{"episode_number":2,"air_date":"2020-01-08"},{"episode_number":3,"air_date":"2099-01-01"}]}`)
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	model.DB.Where("1 = 1").Delete(&model.TmdbConfig{})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k", ApiUrl: srv.URL})
	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}

	code, out := subAPIReq(t, h.CreateSubscription, http.MethodPost, "/subscriptions", `{"tmdb_id":600,"media_type":"tv"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("新建失败: %d %v", code, out)
	}
	var sub model.Subscription
	model.DB.First(&sub)
	if sub.Title != "剧" || sub.OrigTitle != "Show" || sub.Year != "2020" || sub.Scope != subScopeAll || sub.State != subStateActive || sub.NextCheckAt == nil {
		t.Fatalf("新建的订阅: %+v", sub)
	}
	var jobs int64
	model.DB.Model(&model.TaskJob{}).Where("kind = ?", jobKindSubscribe).Count(&jobs)
	if jobs != 1 {
		t.Fatalf("新建后应马上排一次检查: %d", jobs)
	}
	if code, _ = subAPIReq(t, h.CreateSubscription, http.MethodPost, "/subscriptions", `{"tmdb_id":600,"media_type":"tv"}`, nil); code != http.StatusConflict {
		t.Fatalf("重复订阅应 409: %d", code)
	}

	id := gin.Params{{Key: "id", Value: fmt.Sprint(sub.ID)}}
	// 暂停
	if code, out = subAPIReq(t, h.UpdateSubscription, http.MethodPut, "/", `{"scope":"all","state":"paused"}`, id); code != http.StatusOK {
		t.Fatalf("暂停失败: %d %v", code, out)
	}
	model.DB.First(&sub, sub.ID)
	if sub.State != subStatePaused {
		t.Fatalf("应已暂停: %+v", sub)
	}

	// 详情：集格子 E01 已有、E02 在路上、E03 没播；尝试列表带集号
	subTestVideo("媒体库/剧集/剧.2020.{tmdbid=600}/Season 01/剧.S01E01.strm")
	invalidateLedgerTitles()
	model.DB.Create(&model.SubAttempt{SubID: sub.ID, Status: subAttemptInflight, Reason: subReasonAwaiting, Episodes: marshalEpKeys(epKeys("S01E02"))})
	model.DB.Create(&model.SubAttempt{SubID: sub.ID, Status: subAttemptUseless, Hash: "x"})
	code, out = subAPIReq(t, h.GetSubscription, http.MethodGet, "/", "", id)
	if code != http.StatusOK {
		t.Fatalf("详情失败: %d %v", code, out)
	}
	grid, _ := json.Marshal(out["grid"])
	if !strings.Contains(string(grid), `"e":1,"state":"have"`) || !strings.Contains(string(grid), `"e":2,"state":"awaiting"`) || !strings.Contains(string(grid), `"e":3,"state":"unaired"`) {
		t.Fatalf("集格子: %s", grid)
	}
	if atts, _ := out["attempts"].([]any); len(atts) != 2 {
		t.Fatalf("尝试列表: %v", out["attempts"])
	}

	// 重试一条没用上的
	var useless model.SubAttempt
	model.DB.Where("status = ?", subAttemptUseless).First(&useless)
	retry := gin.Params{{Key: "id", Value: fmt.Sprint(sub.ID)}, {Key: "aid", Value: fmt.Sprint(useless.ID)}}
	if code, _ = subAPIReq(t, h.RetrySubAttempt, http.MethodPost, "/", "", retry); code != http.StatusOK {
		t.Fatalf("重试失败: %d", code)
	}
	model.DB.First(&useless, useless.ID)
	if useless.RetryAt == nil {
		t.Fatal("重试应设 RetryAt")
	}

	// 删除：订阅与尝试一起删
	if code, _ = subAPIReq(t, h.DeleteSubscription, http.MethodDelete, "/", "", id); code != http.StatusOK {
		t.Fatalf("删除失败: %d", code)
	}
	var n int64
	model.DB.Model(&model.SubAttempt{}).Count(&n)
	if n != 0 {
		t.Fatalf("尝试记账应一起删: %d", n)
	}
}
