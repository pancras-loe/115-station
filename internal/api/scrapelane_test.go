package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"115-station/internal/model"
)

// ---- 刮削队列：不拿 taskMu ----

// 主队列的锁被占着（一轮长整理），刮削照样能跑完
func TestScrapeLaneRunsWhileTaskMuHeld(t *testing.T) {
	newTestDB(t, "scrape_lane.db")
	h := &Handler{DB: model.DB}
	ran := false
	withFakeExecutors(t, map[string]jobExecutor{
		jobKindScrape: func(*Handler, *model.TaskJob) (jobOutcome, error) {
			scrapeLane.set("刮削", 1, 3, "某剧")
			scrapeLane.setSub("集剧照", 5, 10, "E05-thumb.jpg")
			if p, ok := liveJobProgress(scrapeLane.id); !ok || p.Sub == nil || p.Sub.Done != 5 {
				t.Errorf("运行中应能读到两级进度：%+v", p)
			}
			ran = true
			return jobOutcome{Message: "好了"}, nil
		},
	})
	if !taskMu.TryLock("测试：长整理") {
		t.Fatal("锁被别人占着")
	}
	defer taskMu.Unlock()

	job, _ := enqueueJob(model.DB, jobSpec{Kind: jobKindScrape, Title: "刮削"})
	done := make(chan struct{})
	go func() { h.runJob(&job, scrapeLane); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("刮削队列不该等 taskMu")
	}
	if !ran || jobStatus(t, job.ID).Status != jobSuccess {
		t.Fatalf("刮削应执行成功：ran=%v status=%s", ran, jobStatus(t, job.ID).Status)
	}
	// 结束快照只留顶层进度
	if d := toJobDTO(jobStatus(t, job.ID), nil); d.Progress == nil || d.Progress.Sub != nil {
		t.Fatalf("结束快照不该带片目内进度：%+v", d.Progress)
	}
	// 机器人「状态」读的全局任务状态不归刮削队列管
	if running, name, _, _ := TaskStatus(); running && name == "刮削" {
		t.Fatal("刮削队列不该改写 taskMu 持有者的任务状态")
	}
}

// 两条队列各取各的，排队位置也各算各的
func TestQueueLanesSeparate(t *testing.T) {
	newTestDB(t, "scrape_lane_pos.db")
	a, _ := enqueueJob(model.DB, jobSpec{Kind: "redo", Title: "A"})
	s1, _ := enqueueJob(model.DB, jobSpec{Kind: jobKindScrape, Title: "S1"})
	b, _ := enqueueJob(model.DB, jobSpec{Kind: "organize", Title: "B"})
	s2, _ := enqueueJob(model.DB, jobSpec{Kind: jobKindScrape, Title: "S2"})

	if j, ok := nextQueuedJob(model.DB, mainLane); !ok || j.ID != a.ID {
		t.Fatalf("主队列下一个应是 A：%+v", j)
	}
	if j, ok := nextQueuedJob(model.DB, scrapeLane); !ok || j.ID != s1.ID {
		t.Fatalf("刮削队列下一个应是 S1：%+v", j)
	}
	q := queuedJobs(model.DB)
	for id, want := range map[uint]int{a.ID: 1, b.ID: 2, s1.ID: 1, s2.ID: 2} {
		if pos, _ := queuePosition(q, id); pos != want {
			t.Fatalf("任务 %d 应排第 %d，实际 %d", id, want, pos)
		}
	}
}

// 整理后刮削还没开始又来一轮：片目取并集，不覆盖上一轮的
func TestEnqueueAutoScrapeMerges(t *testing.T) {
	newTestDB(t, "scrape_auto.db")
	cfg := scrapeCfg{WriteNFO: true, WriteImages: true, SkipSharedStills: true}
	enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/剧集/甲", Kind: "tv", Title: "甲", TmdbID: 1}}, cfg, nil, nil)
	enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/电影/乙", Kind: "movie", Title: "乙", TmdbID: 2}}, cfg, nil, nil)

	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindScrape).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("两轮整理后刮削应并成一个任务，实际 %d 个", len(jobs))
	}
	lp := decodeJobParams(&jobs[0]).Local
	if lp == nil || !reflect.DeepEqual(lp.Keys, []string{"库/剧集/甲", "库/电影/乙"}) {
		t.Fatalf("片目应取并集：%+v", lp)
	}
	if lp.Hints["库/剧集/甲"].TmdbID != 1 || lp.Hints["库/电影/乙"].TmdbID != 2 {
		t.Fatalf("识别结果要一起并过来：%+v", lp.Hints)
	}
	if jobs[0].Priority != jobPriorityBackground || jobs[0].Title != "整理后刮削《甲》等 2 部" {
		t.Fatalf("优先级 / 标题不对：%d %q", jobs[0].Priority, jobs[0].Title)
	}
}

// 整理把 Emby 刷新交给刮削：刮削队列空闲才接，排着的几轮合并时刷新目标一个不丢
func TestEnqueueAutoScrapeEmbyHandoff(t *testing.T) {
	newTestDB(t, "scrape_handoff.db")
	cfg := scrapeCfg{WriteNFO: true}

	if !enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/电影/甲", Kind: "movie", TmdbID: 1}}, cfg,
		[]string{"/media/库/电影/甲"}, []string{"/media/库/电影/甲/甲.strm"}) {
		t.Fatal("刮削队列空闲时应接下 Emby 刷新")
	}
	if !enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/电影/乙", Kind: "movie", TmdbID: 2}}, cfg,
		[]string{"/media/库/电影/乙"}, []string{"/media/库/电影/乙/乙.strm"}) {
		t.Fatal("排着的整理后刮削不算占着队列")
	}
	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindScrape).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("应并成一个任务，实际 %d 个", len(jobs))
	}
	lp := decodeJobParams(&jobs[0]).Local
	if !reflect.DeepEqual(lp.EmbyRefresh, []string{"/media/库/电影/甲", "/media/库/电影/乙"}) ||
		!reflect.DeepEqual(lp.EmbyVerify, []string{"/media/库/电影/甲/甲.strm", "/media/库/电影/乙/乙.strm"}) {
		t.Fatalf("合并时刷新目标要取并集：%+v / %+v", lp.EmbyRefresh, lp.EmbyVerify)
	}

	// 前面排着手动刮削：整理自己刷，不交接
	enqueueJob(model.DB, jobSpec{Kind: jobKindScrape, Title: "手动刮削", DedupeKey: "local:库/电影/丁", Priority: jobPriorityManual})
	if enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/电影/丙", Kind: "movie", TmdbID: 3}}, cfg,
		[]string{"/media/库/电影/丙"}, nil) {
		t.Fatal("刮削队列前面还有别的任务时不该接")
	}
}

// 整理交过来的刷新：刮削什么都没写也要刷；本地已经不在的目录不刷
func TestScrapeEmbyRefreshTargets(t *testing.T) {
	root := t.TempDir()
	alive := filepath.Join(root, "甲")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	var gotIngest bool
	prev := scrapeEmbyNotify
	scrapeEmbyNotify = func(dirs []string, ingest bool, verify ...string) { got, gotIngest = dirs, ingest }
	t.Cleanup(func() { scrapeEmbyNotify = prev })

	scrapeEmbyRefresh(&localScrapeParams{EmbyRefresh: []string{alive, filepath.Join(root, "没了")}}, nil)
	if !reflect.DeepEqual(got, []string{alive}) {
		t.Fatalf("应只刷还在的目录：%v", got)
	}
	if !gotIngest {
		t.Fatal("整理交过来的刷新是入库，要回查")
	}
	got = nil
	scrapeEmbyRefresh(&localScrapeParams{}, map[string]bool{})
	if got != nil {
		t.Fatalf("没有交接、也没写东西就不该刷：%v", got)
	}

	// 手动刮削只写了元数据：照刷，但不是入库，不回查（回查会按全局「轨道探测」自动探，顶掉弹窗里关掉的选择）
	scrapeEmbyRefresh(&localScrapeParams{Keys: []string{"剧集/甲"}}, map[string]bool{alive: true})
	if !reflect.DeepEqual(got, []string{alive}) || gotIngest {
		t.Fatalf("手动刮削应只刷不回查：dirs=%v ingest=%v", got, gotIngest)
	}
}

// 整理时识别到的编号优先：目录名不带 {tmdbid} 时台账认不出来
func TestScrapeTargetsHints(t *testing.T) {
	ledger := map[string]*ledgerTitleEntry{
		"库/剧集/甲":   {Key: "库/剧集/甲", Title: "甲", MediaType: "tv"},
		"库/电影/有编号": {Key: "库/电影/有编号", Title: "有编号", TmdbID: 9, MediaType: "movie"},
	}
	lp := &localScrapeParams{
		Keys:  []string{"库/剧集/甲", "库/电影/新来的", "库/电影/不见了"},
		Hints: map[string]scrapeHint{"库/剧集/甲": {Kind: "tv", Title: "甲", TmdbID: 1}, "库/电影/新来的": {Kind: "movie", Title: "新来的", TmdbID: 2}},
	}
	targets, problems := scrapeTargets(lp, ledger)
	if len(targets) != 2 || targets[0].hint == nil || targets[0].hint.TmdbID != 1 {
		t.Fatalf("甲应带上整理时的识别结果：%+v", targets)
	}
	if targets[1].e.TmdbID != 2 || targets[1].e.LibName != "库" {
		t.Fatalf("台账缓存里还没有的片目按识别结果补一条：%+v", targets[1].e)
	}
	if len(problems) != 1 {
		t.Fatalf("既不在台账也没有识别结果的要报出来：%v", problems)
	}
}

// ---- 产物出口：不建目录 ----

func TestFileScrapeWriterDoesNotRecreateGoneDir(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "已被移走的片目")
	w := newFileScrapeWriter(nil, false, false)
	if _, err := w.put(metaDest{Local: gone}, "poster.jpg", []byte("x")); !errors.Is(err, errMetaDirGone) {
		t.Fatalf("目录不在时应返回 errMetaDirGone：%v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatal("刮削不许把目录建回来")
	}
}

// ---- 事后收拾 ----

func TestScrapeCompensate(t *testing.T) {
	root := t.TempDir()
	title := filepath.Join(root, "库", "剧集", "某剧")
	s1 := filepath.Join(title, "Season 01")
	must(t, os.MkdirAll(s1, 0o755))
	write := func(p string) string {
		must(t, os.WriteFile(p, []byte("x"), 0o644))
		return p
	}
	write(filepath.Join(s1, "E01.strm"))
	// E02 的 STRM 在刮削期间被洗版换掉了
	tt := scrapeTitle{Dir: metaDest{Local: title}, Videos: []scrapeVideo{
		{Name: "E01", Dir: metaDest{Local: s1}}, {Name: "E02", Dir: metaDest{Local: s1}},
	}}
	written := []string{
		write(filepath.Join(title, "tvshow.nfo")),
		write(filepath.Join(s1, "E01.nfo")),
		write(filepath.Join(s1, "E02.nfo")),
		write(filepath.Join(s1, "E02-thumb.jpg")),
	}
	if n := scrapeCompensate(tt, written, root); n != 2 {
		t.Fatalf("只收回 E02 的两个产物，实际 %d", n)
	}
	if fileExists(filepath.Join(s1, "E02.nfo")) || !fileExists(filepath.Join(s1, "E01.nfo")) {
		t.Fatal("E02 的收回、E01 的留着")
	}

	// 整部被移走：STRM 没了，这次写的全收回，空目录一路收到库根
	must(t, os.Remove(filepath.Join(s1, "E01.strm")))
	pre := write(filepath.Join(title, "poster.jpg")) // 此前就有的，不在 written 里
	if n := scrapeCompensate(tt, []string{filepath.Join(title, "tvshow.nfo"), filepath.Join(s1, "E01.nfo")}, root); n != 2 {
		t.Fatalf("整部收回应删 2 个，实际 %d", n)
	}
	if !fileExists(pre) {
		t.Fatal("不是这次写的不许碰")
	}
	if _, err := os.Stat(s1); !os.IsNotExist(err) {
		t.Fatal("空的季目录应收掉")
	}
}

// ---- 占位剧照：只在同一季内判 ----

// fakeScrapeReporter 测试用
type fakeScrapeReporter struct{ errs []string }

func (r *fakeScrapeReporter) errf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *fakeScrapeReporter) stopped() bool                { return false }
func (r *fakeScrapeReporter) sub(string, int, int, string) {}

func seedSeason(t *testing.T, tvID, season int, eps map[int]string) {
	t.Helper()
	m := map[int]tmdbEpisodeInfo{}
	for n, still := range eps {
		m[n] = tmdbEpisodeInfo{EpisodeNumber: n, StillPath: still}
	}
	scrapeSeasonMu.Lock()
	scrapeSeasonCache[fmt.Sprintf("%d:%d", tvID, season)] = seasonCacheEntry{eps: m, at: time.Now()}
	scrapeSeasonMu.Unlock()
	t.Cleanup(func() {
		scrapeSeasonMu.Lock()
		delete(scrapeSeasonCache, fmt.Sprintf("%d:%d", tvID, season))
		scrapeSeasonMu.Unlock()
	})
}

func TestEpisodeStillsPlaceholderPerSeason(t *testing.T) {
	const tvID = 990001
	// 第 1 季：E01–E03 共用 /same.jpg（占位），E04 自己一张；E05–E07 路径不同但内容一样（占位）
	seedSeason(t, tvID, 1, map[int]string{1: "/same.jpg", 2: "/same.jpg", 3: "/same.jpg", 4: "/own.jpg",
		5: "/a.jpg", 6: "/b.jpg", 7: "/c.jpg"})
	// 第 2 季：只有 E01 用 /same.jpg —— 别的季用过不算，这一季里它不是占位
	seedSeason(t, tvID, 2, map[int]string{1: "/same.jpg", 2: "/s2e2.jpg"})

	dir := t.TempDir()
	var videos []scrapeVideo
	for _, n := range []string{"S01E01", "S01E02", "S01E03", "S01E04", "S01E05", "S01E06", "S01E07", "S02E01", "S02E02"} {
		must(t, os.WriteFile(filepath.Join(dir, n+".strm"), []byte("x"), 0o644))
		videos = append(videos, scrapeVideo{Name: n, Dir: metaDest{Local: dir}})
	}
	fetches := map[string]int{}
	var fetchMu sync.Mutex // fetch 在片目内是并发调用的
	content := map[string]string{"/a.jpg": "dup", "/b.jpg": "dup", "/c.jpg": "dup"}
	run := func(skipShared bool) (*titleScrapeStat, []string) {
		for _, v := range videos {
			os.Remove(filepath.Join(dir, v.Name+"-thumb.jpg"))
		}
		fetches = map[string]int{}
		w := newFileScrapeWriter(nil, false, false)
		sess := newScrapeSession(&TmdbClient{}, fileScrapeOpts{WriteImages: true, SkipSharedStills: skipShared}, w, &fakeScrapeReporter{})
		sess.fetch = func(p, size string) ([]byte, string, error) {
			fetchMu.Lock()
			fetches[p]++
			fetchMu.Unlock()
			data := content[p]
			if data == "" {
				data = "img" + p
			}
			return []byte(data), "https://img.test/t/p/" + size + p, nil
		}
		r := &titleRun{s: sess, t: scrapeTitle{Kind: "tv", Title: "综艺", Dir: metaDest{Local: dir}, Videos: videos},
			st: &titleScrapeStat{}, tmdbID: tvID, seasons: map[int]map[int]tmdbEpisodeInfo{}}
		for _, v := range videos {
			s, e := scrapeEpisodeNo(v.Name)
			r.eps = append(r.eps, scrapeEp{v: v, season: s, ep: e})
		}
		r.episodeStills()
		var got []string
		for _, v := range videos {
			if fileExists(filepath.Join(dir, v.Name+"-thumb.jpg")) {
				got = append(got, v.Name)
			}
		}
		sort.Strings(got)
		return r.st, got
	}

	st, got := run(true)
	if want := []string{"S01E04", "S02E01", "S02E02"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("占位判定只在同一季内：写了 %v，预期 %v", got, want)
	}
	if st.Placeholder != 6 {
		t.Fatalf("第 1 季 6 集应判为占位，实际 %d", st.Placeholder)
	}
	// 第 1 季的 /same.jpg 连下载都省了；第 2 季那一集要下，一次任务里只下一次
	if fetches["/same.jpg"] != 1 {
		t.Fatalf("/same.jpg 应只为第 2 季下一次，实际 %d 次", fetches["/same.jpg"])
	}

	// 关掉判定：全写，同一张图只下一次、其余复用
	st, got = run(false)
	if len(got) != len(videos) || st.Placeholder != 0 {
		t.Fatalf("关掉占位判定应全写：%v placeholder=%d", got, st.Placeholder)
	}
	if fetches["/same.jpg"] != 1 || st.Reused != 3 {
		t.Fatalf("同一张图应只下一次、其余复用：下载 %d 次、复用 %d", fetches["/same.jpg"], st.Reused)
	}
}

// ---- 片目内并发拉图 ----

// stopAfterReporter 拉到第 n 张图后报告「已停止」
type stopAfterReporter struct {
	fakeScrapeReporter
	stop atomic.Bool
}

func (r *stopAfterReporter) stopped() bool { return r.stop.Load() }

// 集剧照并发拉（不超过 scrapeImgWorkers），拉失败的那一集记错不写，其余照写
func TestScrapeStillsFetchInParallel(t *testing.T) {
	const tvID = 990002
	eps := map[int]string{}
	for n := 1; n <= 12; n++ {
		eps[n] = fmt.Sprintf("/e%02d.jpg", n)
	}
	seedSeason(t, tvID, 1, eps)

	dir := t.TempDir()
	var videos []scrapeVideo
	for n := 1; n <= 12; n++ {
		name := fmt.Sprintf("S01E%02d", n)
		must(t, os.WriteFile(filepath.Join(dir, name+".strm"), []byte("x"), 0o644))
		videos = append(videos, scrapeVideo{Name: name, Dir: metaDest{Local: dir}})
	}
	newRun := func(rep scrapeReporter, fetch func(p, size string) ([]byte, string, error)) *titleRun {
		w := newFileScrapeWriter(nil, false, false)
		sess := newScrapeSession(&TmdbClient{}, fileScrapeOpts{WriteImages: true, SkipSharedStills: true}, w, rep)
		sess.fetch = fetch
		r := &titleRun{s: sess, t: scrapeTitle{Kind: "tv", Title: "某剧", Dir: metaDest{Local: dir}, Videos: videos},
			st: &titleScrapeStat{}, tmdbID: tvID, seasons: map[int]map[int]tmdbEpisodeInfo{}}
		for _, v := range videos {
			s, e := scrapeEpisodeNo(v.Name)
			r.eps = append(r.eps, scrapeEp{v: v, season: s, ep: e})
		}
		return r
	}

	var inFlight, peak atomic.Int32
	rep := &fakeScrapeReporter{}
	r := newRun(rep, func(p, size string) ([]byte, string, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		if p == "/e05.jpg" {
			return nil, "https://img.test" + p, errors.New("HTTP 502")
		}
		return []byte("img" + p), "https://img.test" + p, nil
	})
	r.episodeStills()
	if pk := peak.Load(); pk < 2 || pk > scrapeImgWorkers {
		t.Fatalf("同时在拉的图应在 2..%d 之间，实际峰值 %d", scrapeImgWorkers, pk)
	}
	if r.st.Failed != 1 || len(rep.errs) != 1 {
		t.Fatalf("E05 拉失败应记一处错：failed=%d errs=%v", r.st.Failed, rep.errs)
	}
	for _, v := range videos {
		has := fileExists(filepath.Join(dir, v.Name+"-thumb.jpg"))
		if has == (v.Name == "S01E05") {
			t.Fatalf("%s 剧照写入状态不对：%v", v.Name, has)
		}
	}
	if len(r.st.Wrote) != 11 {
		t.Fatalf("应写 11 张，实际 %d", len(r.st.Wrote))
	}

	// 中途停止：不再发新请求，已拉到的也不写
	for _, v := range videos {
		os.Remove(filepath.Join(dir, v.Name+"-thumb.jpg"))
	}
	stopRep := &stopAfterReporter{}
	var calls atomic.Int32
	r = newRun(stopRep, func(p, size string) ([]byte, string, error) {
		if calls.Add(1) >= 2 {
			stopRep.stop.Store(true)
		}
		time.Sleep(10 * time.Millisecond)
		return []byte("img" + p), "https://img.test" + p, nil
	})
	r.episodeStills()
	if c := calls.Load(); c > scrapeImgWorkers+1 {
		t.Fatalf("停止后不该再发新请求，实际拉了 %d 张", c)
	}
	if len(r.st.Wrote) != 0 {
		t.Fatalf("停止后不该再写：%v", r.st.Wrote)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
