package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

func subTestLayout(t *testing.T) {
	t.Helper()
	model.DB.Where("1 = 1").Delete(&model.CategoryRule{})
	model.DB.Create(&model.CategoryRule{MediaType: "tv", Name: "剧集"})
	model.DB.Create(&model.CategoryRule{MediaType: "movie", Name: "电影"})
}

func subTestVideo(rel string) {
	model.DB.Create(&model.SyncedFile{FileID: rel, RelPath: rel, Kind: "video"})
}

func epKeys(ss ...string) []epKey {
	out := make([]epKey, 0, len(ss))
	for _, s := range ss {
		k, _ := parseEpKey(s)
		out = append(out, k)
	}
	return out
}

func TestSubHaveFromLedger(t *testing.T) {
	newTestDB(t, "subhave.db")
	subTestLayout(t)
	base := "媒体库/剧集/三体.2023.{tmdbid=108545}"
	subTestVideo(base + "/Season 01/三体.S01E01.2023.2160p.strm")
	subTestVideo(base + "/Season 01/三体.S01E02-E03.2023.strm") // 双集文件两集都算
	subTestVideo(base + "/Season 02/E05.strm")                // 文件名只有集号：季号看目录
	subTestVideo(base + "/Season 01/三体.S01E04.2023.mkv.strm") // 旧写法 STRM 名
	orphan := base + "/Season 01/三体.S01E09.2023.strm"
	now := time.Now()
	model.DB.Create(&model.SyncedFile{FileID: orphan, RelPath: orphan, Kind: "video", OrphanAt: &now})
	model.DB.Create(&model.SyncedFile{FileID: "nfo", RelPath: base + "/tvshow.nfo", Kind: "asset"})
	// 同号不同类型、不同编号的片目都不能算进来
	subTestVideo("媒体库/电影/三体.2023.{tmdbid=108545}/三体.2023.strm")
	subTestVideo("媒体库/剧集/三体2.2025.{tmdbid=999}/S01E07.strm")

	h := subHaveOf(model.DB, scanLedgerTitles(), 108545, "tv")
	if !h.Found || len(h.TitleKeys) != 1 || h.TitleKeys[0] != base {
		t.Fatalf("片目认错: %+v", h)
	}
	want := map[epKey]bool{{1, 1}: true, {1, 2}: true, {1, 3}: true, {1, 4}: true, {2, 5}: true}
	if !reflect.DeepEqual(h.Eps, want) {
		t.Fatalf("已有集 = %v, want %v", h.Eps, want)
	}

	mv := subHaveOf(model.DB, scanLedgerTitles(), 108545, "movie")
	if !mv.Found || len(mv.Eps) != 0 {
		t.Fatalf("电影已有判断错: %+v", mv)
	}
	if none := subHaveOf(model.DB, scanLedgerTitles(), 1, "tv"); none.Found {
		t.Fatalf("不存在的条目不该算已有: %+v", none)
	}
}

// 老目录名不带编号：用 MediaLibrary 的落点反推片目（落点不带库名、台账带）
func TestSubHaveFromMediaLibrary(t *testing.T) {
	newTestDB(t, "subhave-lib.db")
	subTestLayout(t)
	subTestVideo("媒体库/剧集/老剧 (2001)/S01E01.strm")
	subTestVideo("媒体库/剧集/老剧 (2001)/S01E02.strm")
	model.DB.Create(&model.MediaLibrary{TmdbID: 42, MediaType: "tv", Title: "老剧", TargetPath: "剧集/老剧 (2001)/S01E01.mkv"})

	h := subHaveOf(model.DB, scanLedgerTitles(), 42, "tv")
	if !h.Found || len(h.Eps) != 2 || !h.Eps[epKey{1, 2}] {
		t.Fatalf("MediaLibrary 反推失败: %+v", h)
	}
}

func TestSubInflight(t *testing.T) {
	newTestDB(t, "subinflight.db")
	model.DB.Create(&model.SubAttempt{SubID: 1, Status: subAttemptInflight, Episodes: marshalEpKeys(epKeys("S01E05", "S01E06"))})
	model.DB.Create(&model.SubAttempt{SubID: 1, Status: subAttemptFailed, Episodes: marshalEpKeys(epKeys("S01E07"))})
	model.DB.Create(&model.SubAttempt{SubID: 2, Status: subAttemptInflight, Episodes: marshalEpKeys(epKeys("S01E08"))})
	got := subInflight(model.DB, 1)
	if len(got) != 2 || !got[epKey{1, 5}] || !got[epKey{1, 6}] {
		t.Fatalf("在路上 = %v", got)
	}
}

func day(s string) time.Time { return parseTmdbDate(s) }

func subTestSchedule(eps map[string]string) subTVSchedule {
	var sch subTVSchedule
	for k, d := range eps {
		key, _ := parseEpKey(k)
		sch.Eps = append(sch.Eps, subEpAir{Key: key, Air: day(d)})
		if key.S > sch.LatestSeason {
			sch.LatestSeason = key.S
		}
	}
	return sch
}

func TestEvalSubTV(t *testing.T) {
	now := day("2026-10-07").Add(10 * time.Hour) // 10:00
	delay := 3 * time.Hour
	sch := subTestSchedule(map[string]string{
		"S01E01": "2026-09-01", "S01E02": "2026-09-08", "S01E03": "2026-09-15",
		"S01E04": "2026-10-07", // 今天播：0 点 + 3 小时 < 10:00，算已播
		"S01E05": "2026-10-14", // 还没播
		"S01E06": "",           // 没排期
	})
	sub := &model.Subscription{Scope: subScopeAll, Follow: subFollowMissing}
	have := subHave{Found: true, Eps: map[epKey]bool{{1, 1}: true}}
	inflight := map[epKey]bool{{1, 2}: true}

	ev := evalSubTV(sub, have, sch, inflight, now, delay)
	if ev.Total != 4 || ev.Have != 1 {
		t.Fatalf("Total/Have = %d/%d", ev.Total, ev.Have)
	}
	if !reflect.DeepEqual(ev.Missing, epKeys("S01E03", "S01E04")) {
		t.Fatalf("缺 = %v", ev.Missing)
	}
	if !reflect.DeepEqual(ev.Inflight, epKeys("S01E02")) {
		t.Fatalf("在路上 = %v", ev.Inflight)
	}
	if ev.Unaired != 2 || !ev.NextAt.Equal(day("2026-10-14").Add(delay)) {
		t.Fatalf("未播 %d，下次 %v", ev.Unaired, ev.NextAt)
	}
	if ev.Done {
		t.Fatal("还有未播的集，不能完成")
	}

	// 延迟没过：今天这集还不算已播
	early := evalSubTV(sub, have, sch, inflight, day("2026-10-07").Add(time.Hour), delay)
	if early.Total != 3 || !early.NextAt.Equal(day("2026-10-07").Add(delay)) {
		t.Fatalf("延迟内 Total=%d NextAt=%v", early.Total, early.NextAt)
	}

	// 只追新集：订阅之前播的不要
	from := day("2026-09-10")
	subNew := &model.Subscription{Scope: subScopeAll, Follow: subFollowNew, FollowFrom: &from}
	evNew := evalSubTV(subNew, have, sch, inflight, now, delay)
	if !reflect.DeepEqual(evNew.Missing, epKeys("S01E03", "S01E04")) || evNew.Total != 2 {
		t.Fatalf("只追新集 缺 = %v Total=%d", evNew.Missing, evNew.Total)
	}
}

func TestEvalSubTVScopeAndDone(t *testing.T) {
	now := day("2026-10-07")
	sch := subTestSchedule(map[string]string{
		"S01E01": "2025-01-01", "S01E02": "2025-01-08",
		"S02E01": "2026-01-01", "S02E02": "2026-01-08", "S02E03": "2026-01-15",
	})
	all := map[epKey]bool{{1, 1}: true, {1, 2}: true, {2, 1}: true, {2, 2}: true, {2, 3}: true}
	have := subHave{Found: true, Eps: all}

	// 全剧：集都齐了，但 TMDB 没标完结 → 不算完成
	sub := &model.Subscription{Scope: subScopeAll}
	if ev := evalSubTV(sub, have, sch, nil, now, 0); ev.Done || len(ev.Missing) != 0 {
		t.Fatalf("连载中的全剧不该完成: %+v", ev)
	}
	sch.Ended = true
	if ev := evalSubTV(sub, have, sch, nil, now, 0); !ev.Done {
		t.Fatalf("完结且齐了应完成: %+v", ev)
	}
	sch.Ended = false

	// 某一季：有更新的一季 → 这季播完了
	s1 := &model.Subscription{Scope: subScopeSeason, Season: 1}
	sch1 := subTVSchedule{LatestSeason: 2, Eps: sch.Eps[:2]}
	if ev := evalSubTV(s1, have, sch1, nil, now, 0); !ev.Done {
		t.Fatalf("第 1 季应完成: %+v", ev)
	}
	// 最新一季：没完结就继续追
	s2 := &model.Subscription{Scope: subScopeSeason, Season: 2}
	sch2 := subTVSchedule{LatestSeason: 2, Eps: sch.Eps[2:]}
	if ev := evalSubTV(s2, have, sch2, nil, now, 0); ev.Done {
		t.Fatalf("最新一季不该完成: %+v", ev)
	}
	// 在路上的也不能完成
	partial := subHave{Found: true, Eps: map[epKey]bool{{1, 1}: true}}
	if ev := evalSubTV(s1, partial, sch1, map[epKey]bool{{1, 2}: true}, now, 0); ev.Done {
		t.Fatalf("还有在路上的不该完成: %+v", ev)
	}
}

func TestSubEpInScopeAndSeasons(t *testing.T) {
	r := &model.Subscription{Scope: subScopeRange, Season: 2, EpStart: 3, EpEnd: 5}
	for k, want := range map[epKey]bool{{2, 2}: false, {2, 3}: true, {2, 5}: true, {2, 6}: false, {1, 4}: false} {
		if got := subEpInScope(r, k); got != want {
			t.Errorf("%v in range = %v", k, got)
		}
	}
	open := &model.Subscription{Scope: subScopeRange, Season: 2, EpStart: 3}
	if !subEpInScope(open, epKey{2, 99}) {
		t.Error("没写结束集号的集段应一直到最后")
	}
	seasons := map[int]int{0: 3, 1: 10, 2: 8}
	if got := subSeasonsInScope(&model.Subscription{Scope: subScopeAll}, seasons); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("全剧默认不含特别篇: %v", got)
	}
	if got := subSeasonsInScope(&model.Subscription{Scope: subScopeAll, Specials: true}, seasons); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("含特别篇: %v", got)
	}
}

func TestEvalSubMovie(t *testing.T) {
	now := day("2026-10-07")
	if ev := evalSubMovie(subHave{Found: true}, false, time.Time{}, now); !ev.Done || ev.Have != 1 {
		t.Fatalf("已有应完成: %+v", ev)
	}
	if ev := evalSubMovie(subHave{}, true, time.Time{}, now); ev.Done || len(ev.Inflight) != 1 || len(ev.Missing) != 0 {
		t.Fatalf("在路上: %+v", ev)
	}
	later := day("2026-12-01")
	if ev := evalSubMovie(subHave{}, false, later, now); len(ev.Missing) != 0 || !ev.NextAt.Equal(later) {
		t.Fatalf("没到时间不该缺: %+v", ev)
	}
	if ev := evalSubMovie(subHave{}, false, time.Time{}, now); len(ev.Missing) != 1 {
		t.Fatalf("该缺: %+v", ev)
	}
}

func TestSubMovieSearchFrom(t *testing.T) {
	th, dg := day("2026-07-01"), day("2026-09-20")
	cases := []struct {
		rel  subMovieRelease
		mode string
		want time.Time
	}{
		{subMovieRelease{th, dg}, subMovieWaitDigital, dg},
		{subMovieRelease{Theatrical: th}, subMovieWaitDigital, th.AddDate(0, 0, 90)},
		{subMovieRelease{}, subMovieWaitDigital, time.Time{}},
		{subMovieRelease{th, dg}, subMovieWaitTheatrical, th.AddDate(0, 0, 45)},
		{subMovieRelease{th, day("2026-07-20")}, subMovieWaitTheatrical, day("2026-07-20")},
		{subMovieRelease{th, dg}, subMovieWaitNow, time.Time{}},
	}
	for i, c := range cases {
		if got := subMovieSearchFrom(c.rel, c.mode); !got.Equal(c.want) {
			t.Errorf("#%d %s = %v, want %v", i, c.mode, got, c.want)
		}
	}
}

func TestEpKeyRoundTrip(t *testing.T) {
	ks := epKeys("S01E05", "s2e10", "S00E01")
	if got := unmarshalEpKeys(marshalEpKeys(ks)); !reflect.DeepEqual(got, ks) {
		t.Fatalf("round trip %v → %v", ks, got)
	}
	if _, ok := parseEpKey("E05"); ok {
		t.Fatal("不带季号的不该认")
	}
}

// 端到端：假 TMDB 上的剧集详情与分季集信息 → 盘点
func TestEvaluateSubscriptionTV(t *testing.T) {
	newTestDB(t, "subeval.db")
	subTestLayout(t)
	resetTmdbCaches()
	scrapeSeasonMu.Lock()
	scrapeSeasonCache = map[string]seasonCacheEntry{}
	scrapeSeasonMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/777":
			fmt.Fprint(w, `{"status":"Ended","seasons":[{"season_number":0,"episode_count":1},{"season_number":1,"episode_count":3}]}`)
		case "/tv/777/season/1":
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"air_date":"2020-01-01"},{"episode_number":2,"air_date":"2020-01-08"},{"episode_number":3,"air_date":"2020-01-15"}]}`)
		default:
			if strings.HasPrefix(r.URL.Path, "/tv/777/season/0") {
				t.Errorf("没订阅特别篇却去拉第 0 季")
			}
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}

	subTestVideo("媒体库/剧集/X.2020.{tmdbid=777}/Season 01/X.S01E01.strm")
	sub := &model.Subscription{ID: 9, TmdbID: 777, MediaType: "tv", Scope: subScopeAll, Follow: subFollowMissing}
	model.DB.Create(&model.SubAttempt{SubID: 9, Status: subAttemptInflight, Episodes: marshalEpKeys(epKeys("S01E02"))})
	invalidateLedgerTitles()

	ev, err := evaluateSubscription(model.DB, tc, sub, defaultSubscribeCfg(), day("2026-10-07"))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Total != 3 || ev.Have != 1 || !reflect.DeepEqual(ev.Missing, epKeys("S01E03")) || !reflect.DeepEqual(ev.Inflight, epKeys("S01E02")) || ev.Done {
		t.Fatalf("盘点 = %+v", ev)
	}
}
