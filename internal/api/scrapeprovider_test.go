package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ---- 刮削方式：本站刮削 / Emby 刮削 ----

func setScrapeSetting(t *testing.T, v string) {
	t.Helper()
	model.DB.Where("key = ?", "scrape").Delete(&model.Setting{})
	if err := model.DB.Create(&model.Setting{Key: "scrape", Value: v}).Error; err != nil {
		t.Fatal(err)
	}
}

// 老配置没有 provider 字段，按本站刮削算；只认 emby
func TestScrapeProviderDefault(t *testing.T) {
	newTestDB(t, "provider_default.db")
	setScrapeSetting(t, `{"write_nfo":true}`)
	if c := loadScrapeCfg(); !c.stationScrapes() || c.provider() != scrapeProviderStation {
		t.Fatalf("老配置应是本站刮削：%+v", c)
	}
	setScrapeSetting(t, `{"write_nfo":true,"provider":"bogus"}`)
	if !loadScrapeCfg().stationScrapes() {
		t.Fatal("不认识的值按本站刮削算")
	}
	setScrapeSetting(t, `{"write_nfo":true,"provider":"emby"}`)
	if loadScrapeCfg().stationScrapes() {
		t.Fatal("provider=emby 应是 Emby 刮削")
	}
}

// Emby 刮削时每个入口都挡住：整理后、同步后、手动入队、已经排着的任务
func TestEmbyProviderBlocksScrapeEntries(t *testing.T) {
	newTestDB(t, "provider_block.db")
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	full, _ := json.Marshal(map[string]string{"local_path": root})
	model.DB.Save(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Create(&model.TmdbConfig{ApiKey: "k"})
	model.DB.Create(&model.CategoryRule{Name: "电影/华语", MediaType: "movie"})
	setScrapeSetting(t, `{"write_nfo":true,"write_images":true,"auto_after_organize":true,"auto_after_sync":true,"provider":"emby"}`)
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	if h.newOrgSink("").scrapeOn {
		t.Fatal("Emby 刮削时整理后不该刮")
	}
	if enqueueSyncScrape(model.DB, []string{"库/电影/华语/乙.{tmdbid=1}/乙.strm"}, filepath.Join(root, "库")) {
		t.Fatal("Emby 刮削时同步后不该接刷新")
	}
	var n int64
	model.DB.Model(&model.TaskJob{}).Count(&n)
	if n != 0 {
		t.Fatalf("Emby 刮削时不该入队，实有 %d 个任务", n)
	}

	body, _ := json.Marshal(map[string]any{"keys": []string{"库/电影/华语/乙"}, "scrape": map[string]bool{"write_nfo": true}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/local/scrape", bytes.NewReader(body))
	h.ScrapeLocalTitles(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("手动刮削应 409，实得 %d %s", w.Code, w.Body.String())
	}

	// 切换之前排下的自动刮削：跳过、按空转删行
	job := &model.TaskJob{Kind: jobKindScrape, DedupeKey: scrapeAutoDedupe}
	job.Params = jobParamsJSON(t, jobParams{Local: &localScrapeParams{Keys: []string{"库/电影/华语/乙"}, Scrape: fileScrapeOpts{WriteNFO: true}}})
	out, err := execScrapeJob(h, job)
	if err != nil || !out.Idle {
		t.Fatalf("排着的自动刮削应跳过并按空转处理：%+v %v", out, err)
	}

	// 切回本站刮削：整理后照常刮
	setScrapeSetting(t, `{"write_nfo":true,"auto_after_organize":true}`)
	if !h.newOrgSink("").scrapeOn {
		t.Fatal("本站刮削时整理后应刮")
	}
}

// 刮削配置整存整取，带着旧 provider 保存不能把刚切的方式改回去；切换只认 /scrape/provider
func TestScrapeProviderSurvivesConfigSave(t *testing.T) {
	newTestDB(t, "provider_save.db")
	gin.SetMode(gin.TestMode)
	defer func() { notifyConfigSource = nil }()
	cfg := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	notifyConfigSource = cfg
	h := &Handler{DB: model.DB, Config: cfg}

	post := func(fn gin.HandlerFunc, v any) int {
		b, _ := json.Marshal(v)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
		fn(c)
		return w.Code
	}
	if code := post(h.SetScrapeProvider, map[string]string{"provider": "nope"}); code != http.StatusBadRequest {
		t.Fatalf("非法值应 400，实得 %d", code)
	}
	if code := post(h.SetScrapeProvider, map[string]string{"provider": "emby"}); code != http.StatusOK {
		t.Fatalf("切换失败：%d", code)
	}
	// 「媒体信息」页签拿着旧配置（provider=station）保存轨道探测开关
	if code := post(h.ScrapeSaveConfig, scrapeCfg{WriteNFO: true, ProbeStreams: true, Provider: "station"}); code != http.StatusOK {
		t.Fatalf("保存失败：%d", code)
	}
	c := loadScrapeCfg()
	if c.stationScrapes() || !c.ProbeStreams {
		t.Fatalf("保存刮削配置后方式应仍是 Emby、探测开关照存：%+v", c)
	}
}

// 媒体库选项 → 两种方式各自的问题
func TestCheckEmbyLib(t *testing.T) {
	var lib embyVirtualFolder
	lib.Name = "电影"
	if err := json.Unmarshal([]byte(`{"TypeOptions":[
		{"Type":"Movie","MetadataFetchers":["TheMovieDb"],"ImageFetchers":["TheMovieDb","FanArt"]},
		{"Type":"Person","MetadataFetchers":["TheMovieDb"]}
	]}`), &lib.LibraryOptions); err != nil {
		t.Fatal(err)
	}
	c := checkEmbyLib(lib)
	if !c.Known || !c.NfoReader || len(c.EmbyIssues) != 0 || len(c.StationIssues) != 2 {
		t.Fatalf("开着下载器：Emby 刮削没问题、本站刮削提示两条：%+v", c)
	}
	if !reflect.DeepEqual(c.ImageFetchers, []string{"FanArt", "TheMovieDb"}) {
		t.Fatalf("图像获取器 = %v", c.ImageFetchers)
	}

	// 下载器全关、Nfo 读取器也关了
	var off embyVirtualFolder
	_ = json.Unmarshal([]byte(`{"TypeOptions":[{"Type":"Series"},{"Type":"Episode","MetadataFetchers":["TheTVDB"]}],
		"DisabledLocalMetadataReaders":["Nfo"]}`), &off.LibraryOptions)
	c = checkEmbyLib(off)
	if len(c.EmbyIssues) != 2 || len(c.StationIssues) != 1 || c.NfoReader {
		t.Fatalf("下载器全关：Emby 刮削两条问题；Nfo 读取器关了：本站刮削一条：%+v", c)
	}

	// 读不到 Movie / Series 的设置：不对下载器下结论
	if c := checkEmbyLib(embyVirtualFolder{}); c.Known || len(c.EmbyIssues)+len(c.StationIssues) != 0 {
		t.Fatalf("没有类型设置时不该报问题：%+v", c)
	}
}

// Emby 刮削时的卡片分级
func TestGradeByEmby(t *testing.T) {
	cases := []struct {
		name   string
		tmdbID int
		st     *localEmbyStat
		status string
		lack   []string
		mis    string
	}{
		{"Emby 里没有", 1, nil, "miss", nil, ""},
		{"只有文件夹", 1, &localEmbyStat{Items: 2}, "miss", nil, ""},
		{"齐全", 1, &localEmbyStat{ItemID: "9", Tmdb: "1", Poster: true, Backdrop: true}, "ok", nil, ""},
		{"只有 IMDb 也算认出", 0, &localEmbyStat{ItemID: "9", Imdb: "tt1", Poster: true, Backdrop: true}, "ok", nil, ""},
		{"没认出缺图", 1, &localEmbyStat{ItemID: "9"}, "partial", []string{"TMDB 编号", "海报", "背景图"}, ""},
		{"认错条目", 1, &localEmbyStat{ItemID: "9", Tmdb: "2", Poster: true, Backdrop: true}, "partial", nil, "2"},
	}
	for _, tc := range cases {
		lt := localTitle{TmdbID: tc.tmdbID, Status: "ok", Lack: []string{"本地的"}, Soft: []string{"剧照"}}
		gradeByEmby(&lt, tc.st)
		if lt.Status != tc.status || !reflect.DeepEqual(lt.Lack, tc.lack) || lt.Mismatch != tc.mis || lt.Soft != nil {
			t.Errorf("%s：%+v", tc.name, lt)
		}
	}
}

// 列表：Emby 刮削时按 Emby 分级、快照没到记 pending；海报本站刮削时只补本地没有的
func TestFilterLocalTitlesEmbyGrade(t *testing.T) {
	all := []localTitle{
		{Key: "a", Title: "甲", MediaType: "movie", Status: "ok", HasPoster: true},
		{Key: "b", Title: "乙", MediaType: "movie", Status: "miss"},
	}
	snap := map[string]localEmbyStat{
		"a": {ItemID: "1", Tmdb: "5", Poster: true, Backdrop: true},
		"b": {ItemID: "2", Tmdb: "6", Poster: true, Backdrop: true},
	}

	list, st := filterLocalTitles(all, localTitleQuery{EmbyGrade: true})
	if st.All != 2 || st.OK+st.Partial+st.Miss != 0 || list[0].Status != "pending" {
		t.Fatalf("快照没到时不进三档计数：%+v %+v", st, list)
	}

	list, st = filterLocalTitles(all, localTitleQuery{EmbyGrade: true, Emby: snap})
	if st.OK != 2 || list[0].EmbyPoster != "1" || list[1].EmbyPoster != "2" {
		t.Fatalf("Emby 刮削：都按 Emby 算已刮削、海报都取 Emby 的：%+v %+v", st, list)
	}

	list, st = filterLocalTitles(all, localTitleQuery{Emby: snap})
	if st.OK != 1 || st.Miss != 1 || list[0].EmbyPoster != "" || list[1].EmbyPoster != "2" {
		t.Fatalf("本站刮削：状态看本地，Emby 海报只补本地没有的：%+v %+v", st, list)
	}
	if all[1].EmbyPoster != "" || all[1].Status != "miss" {
		t.Fatal("缓存里的切片不能被改")
	}
}

// Series 的路径就是标题目录：dir 模式连它自己也算；视频只往上找
func TestLocalEmbyTitleKeyOfDir(t *testing.T) {
	ledger := map[string]*ledgerTitleEntry{"库/剧集/乙": {Key: "库/剧集/乙"}}
	if k := localEmbyTitleKeyOf("/m/库/剧集/乙", "/m", ledger, true); k != "库/剧集/乙" {
		t.Fatalf("Series 路径应归到自己：%q", k)
	}
	if k := localEmbyTitleKeyOf("/m/库/剧集/乙/", "/m", ledger, true); k != "库/剧集/乙" {
		t.Fatalf("结尾带 / 也认：%q", k)
	}
	if k := localEmbyTitleKeyOf("/m/库/剧集/乙", "/m", ledger, false); k != "" {
		t.Fatalf("文件模式下最后一段是文件名，不该归到自己：%q", k)
	}
}

// 多版本电影几个 Movie 条目：都记进刷新清单，门面挑有海报的
func TestAddTitleItemMultiVersion(t *testing.T) {
	var st localEmbyStat
	st.addTitleItem(embyExtractItem{ID: "1", ProviderIds: map[string]string{"tmdb": "7"}})
	st.addTitleItem(embyExtractItem{ID: "2", ImageTags: map[string]string{"Primary": "x"}, BackdropImageTags: []string{"y"}})
	if st.ItemID != "2" || !st.Poster || !st.Backdrop || st.Tmdb != "7" || !reflect.DeepEqual(st.refresh, []string{"1", "2"}) {
		t.Fatalf("%+v", st)
	}
}

func jobParamsJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
