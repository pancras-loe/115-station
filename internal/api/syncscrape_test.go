package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"115-station/internal/model"
)

// ---- 增量同步后自动刮削 ----

// 新 STRM 按当前分类规则归到片目：同一部剧的几集只算一个，分类目录外的不刮
func TestSyncScrapeKeys(t *testing.T) {
	layout := buildLibCategoryLayout([]model.CategoryRule{
		{Name: "电影/华语", MediaType: "movie"},
		{Name: "剧集/国产", MediaType: "tv"},
	})
	keys, outside := syncScrapeKeys(layout, []string{
		"库/剧集/国产/乙/Season 01/乙 S01E02.strm",
		"库/电影/华语/甲 (2020)/甲 (2020).strm",
		"库/剧集/国产/乙/Season 01/乙 S01E01.strm",
		"库/手机上传/随手拍.strm",
	})
	if want := []string{"库/剧集/国产/乙", "库/电影/华语/甲 (2020)"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("片目 = %v，期望 %v", keys, want)
	}
	if outside != 1 {
		t.Fatalf("分类目录外的应计 1 个，实得 %d", outside)
	}
}

// 严格识别只认 choose 第一关：包含关系 / 别名相等搜中的都不要，换下一个搜索词
func TestRecognizeStrict(t *testing.T) {
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	search := map[string]string{
		// 只有片名相近的候选：recognize 会在第三关收下它，严格模式不认
		"movie|繁花": `[{"id":1,"title":"繁花似锦","release_date":"2020-01-01"}]`,
		"tv|繁花":    `[{"id":2,"name":"繁花","first_air_date":"2023-12-27"}]`,
		// 中英双名：中文名搜不到相等的，英文名相等
		"movie|流浪":       `[{"id":3,"title":"流浪者之歌","release_date":"2001-01-01"}]`,
		"movie|Wanderer": `[{"id":4,"title":"漂泊者","original_title":"Wanderer","release_date":"2019-01-01"}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if kind, ok := strings.CutPrefix(r.URL.Path, "/search/"); ok {
			res := search[kind+"|"+r.URL.Query().Get("query")]
			if res == "" {
				res = "[]"
			}
			fmt.Fprintf(w, `{"results":%s}`, res)
			return
		}
		http.Error(w, `{"status_code":34}`, http.StatusNotFound)
	}))
	defer srv.Close()
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}

	// 分类说是电影：只搜电影，片名相近的不收
	if m, _ := tc.recognizeStrict(&ParsedName{Title: "繁花"}, "movie"); m != nil {
		t.Fatalf("片名只是相近，不该采用：%+v", m)
	}
	// 分类说是剧集：剧集那边片名相等
	if m, err := tc.recognizeStrict(&ParsedName{Title: "繁花"}, "tv"); err != nil || m == nil || m.TmdbID != 2 {
		t.Fatalf("剧集片名相等应采用 tv/2：%+v %v", m, err)
	}
	// 中文名落空，换英文名，原名相等
	if m, err := tc.recognizeStrict(&ParsedName{Title: "流浪 Wanderer"}, "movie"); err != nil || m == nil || m.TmdbID != 4 {
		t.Fatalf("原名相等应采用 movie/4：%+v %v", m, err)
	}
}

// 开关关着不入队；开着时入队、带 Origin、标题按目录名摘片名，几轮合并成一个任务
func TestEnqueueSyncScrape(t *testing.T) {
	newTestDB(t, "sync_scrape.db")
	root := t.TempDir()
	full, _ := json.Marshal(map[string]string{"local_path": root})
	model.DB.Save(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k"})
	model.DB.Create(&model.CategoryRule{Name: "电影/华语", MediaType: "movie"})
	rels := []string{"库/电影/华语/乙.2020.{tmdbid=1}/乙.strm"}

	model.DB.Save(&model.Setting{Key: "scrape", Value: `{"write_nfo":true,"auto_after_sync":false}`})
	if enqueueSyncScrape(model.DB, rels, filepath.Join(root, "库")) {
		t.Fatal("开关关着不该接刷新")
	}
	var n int64
	model.DB.Model(&model.TaskJob{}).Count(&n)
	if n != 0 {
		t.Fatalf("开关关着不该入队，实有 %d 个任务", n)
	}

	model.DB.Model(&model.Setting{}).Where("key = ?", "scrape").Update("value", `{"write_nfo":true,"auto_after_sync":true}`)
	if !enqueueSyncScrape(model.DB, rels, filepath.Join(root, "库")) {
		t.Fatal("刮削队列空闲时应接下 Emby 刷新")
	}
	enqueueSyncScrape(model.DB, []string{"库/电影/华语/甲/甲.strm"}, "")

	var jobs []model.TaskJob
	model.DB.Where("kind = ?", jobKindScrape).Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("两轮同步后刮削应并成一个任务，实际 %d 个", len(jobs))
	}
	j := jobs[0]
	lp := decodeJobParams(&j).Local
	if j.DedupeKey != scrapeSyncDedupe || j.Source != "incr" || j.Priority != jobPriorityBackground || !lp.strict() {
		t.Fatalf("去重键 / 来源 / 优先级 / 严格识别不对：%q %q %d %+v", j.DedupeKey, j.Source, j.Priority, lp)
	}
	if j.Title != "同步后刮削《乙》等 2 部" {
		t.Fatalf("标题 = %q", j.Title)
	}
	if !reflect.DeepEqual(lp.EmbyRefresh, []string{filepath.Join(root, "库")}) {
		t.Fatalf("上一轮交过来的刷新不能丢：%v", lp.EmbyRefresh)
	}
	if !isAutoScrapeKey(j.DedupeKey) {
		t.Fatal("同步后刮削也算自动刮削（不另建探测任务）")
	}

	// 整理后刮削不会被排着的同步后刮削挡住交接，也不和它并成一个
	if !enqueueAutoScrape(model.DB, []scrapeJob{{Key: "库/电影/华语/丙", Kind: "movie", TmdbID: 3}},
		scrapeCfg{WriteNFO: true}, []string{"/media/丙"}, nil) {
		t.Fatal("排着的同步后刮削不算占着队列")
	}
	model.DB.Model(&model.TaskJob{}).Where("kind = ?", jobKindScrape).Count(&n)
	if n != 2 {
		t.Fatalf("整理后与同步后应是两个任务，实有 %d 个", n)
	}
}

// 单轮片目超过上限只刮前 syncScrapeMaxTitles 个
func TestEnqueueSyncScrapeCap(t *testing.T) {
	newTestDB(t, "sync_scrape_cap.db")
	full, _ := json.Marshal(map[string]string{"local_path": t.TempDir()})
	model.DB.Save(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Save(&model.Setting{Key: "scrape", Value: `{"write_nfo":true,"auto_after_sync":true}`})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k"})
	model.DB.Create(&model.CategoryRule{Name: "电影/华语", MediaType: "movie"})
	var rels []string
	for i := 0; i < syncScrapeMaxTitles+7; i++ {
		rels = append(rels, fmt.Sprintf("库/电影/华语/片%03d/片.strm", i))
	}
	enqueueSyncScrape(model.DB, rels, "")
	var j model.TaskJob
	model.DB.Where("kind = ?", jobKindScrape).First(&j)
	if lp := decodeJobParams(&j).Local; lp == nil || len(lp.Keys) != syncScrapeMaxTitles {
		t.Fatalf("应只入队 %d 个片目：%+v", syncScrapeMaxTitles, lp)
	}
}

// 增量零遍历写出的新 STRM 交给同步后刮削；刮削接下刷新时增量自己不再刷
func TestIncrHandsNewStrmToScrape(t *testing.T) {
	for _, takes := range []bool{true, false} {
		t.Run(fmt.Sprint("接下刷新=", takes), func(t *testing.T) {
			h, d, p := newIncrTestEnv(t, fmt.Sprintf("incrflow_scrape_%v.db", takes))
			d.scrapeTakes = takes
			d.pages = [][]lifeEvent{{
				{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "d1", FileName: "新片.mkv", PickCode: "pc-1", Time: "100"},
			}}
			if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
				t.Fatal(err)
			}
			if want := []string{"媒体库/剧集/X/新片.strm"}; !reflect.DeepEqual(d.scraped, want) {
				t.Fatalf("交给刮削的 = %v，期望 %v", d.scraped, want)
			}
			if takes && len(d.refreshed) != 0 {
				t.Fatalf("刷新交给了刮削，增量不该再刷：%v", d.refreshed)
			}
			if !takes && len(d.refreshed) != 1 {
				t.Fatalf("刮削没接刷新，增量要自己刷一次：%v", d.refreshed)
			}

			// 同一个文件再来一遍（本地已有、内容一致）：不算新写，不再交给刮削
			d.scraped = nil
			d.pages = [][]lifeEvent{{
				{ID: "e-2", Type: evUpload, FileID: "f-1", Cid: "d1", FileName: "新片.mkv", PickCode: "pc-1", Time: "200"},
			}}
			if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
				t.Fatal(err)
			}
			if len(d.scraped) != 0 {
				t.Fatalf("没新写 STRM 不该交给刮削：%v", d.scraped)
			}
		})
	}
}
