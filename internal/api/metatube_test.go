package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"strmhub/internal/model"
)

// metatubeTestSetup 独立内存库 + 假 metatube-server；返回请求计数指针
func metatubeTestSetup(t *testing.T, name, searchBody, detailBody string) (*httptest.Server, *int64) {
	t.Helper()
	if _, err := model.InitDB("file:" + name + "?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/movies/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchBody))
	})
	mux.HandleFunc("/v1/movies/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(detailBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		model.DB.Where("key = ?", "metatube").Delete(&model.Setting{})
		model.DB.Where("1 = 1").Delete(&model.AVMeta{})
	})
	saveSettingRow(t, "metatube", `{"url":"`+srv.URL+`","token":"","enabled":true}`)
	return srv, &hits
}

func saveSettingRow(t *testing.T, key, value string) {
	t.Helper()
	model.DB.Where("key = ?", key).Delete(&model.Setting{})
	if err := model.DB.Create(&model.Setting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
}

func TestMetatubeUnwrapData(t *testing.T) {
	// responseMessage 包裹 → 剥出 data
	if got := metatubeUnwrapData([]byte(`{"data":{"k":"v"},"error":null}`)); string(got) != `{"k":"v"}` {
		t.Errorf("data 剥包错误: %s", got)
	}
	// 裸 JSON 原样返回
	if got := metatubeUnwrapData([]byte(`[1,2]`)); string(got) != `[1,2]` {
		t.Errorf("裸 JSON 应原样: %s", got)
	}
}

func TestMetatubeParseMoviesFormats(t *testing.T) {
	// 当前协议：responseMessage.data 包裹（字段名按 SDK model 对齐）
	wrapped := `{"data":[{"id":"abc123","number":"ABC-123","title":"作品名","provider":"javbus","actors":["演员A"],"release_date":"2023-01-05"}]}`
	hits, err := metatubeParseMovies([]byte(wrapped))
	if err != nil || len(hits) != 1 || hits[0].Number != "ABC-123" || hits[0].Title != "作品名" {
		t.Errorf("data 包裹解析错误: %v %+v", err, hits)
	}
	// 裸数组兼容
	hits, err = metatubeParseMovies([]byte(`[{"id":"x","number":"X-1","title":"t","provider":"p"}]`))
	if err != nil || len(hits) != 1 {
		t.Errorf("裸数组解析错误: %v", err)
	}
	// 无结果
	hits, _ = metatubeParseMovies([]byte(`{"data":[]}`))
	if len(hits) != 0 {
		t.Errorf("空结果应为 0 条: %+v", hits)
	}
}

func TestMetatubeFetchCachedFlow(t *testing.T) {
	// 裸数组搜索（兼容路径）+ data 包裹详情（当前协议）
	search := `[{"id":"abc123","number":"ABC-123","title":"テスト作品","provider":"javbus","cover_url":"http://example.com/cover.jpg","release_date":"2023-01-05","actors":["演员A"]}]`
	detail := `{"data":{"id":"abc123","number":"ABC-123","title":"テスト作品 オフィスレディ編","summary":"剧情简介","provider":"javbus","release_date":"2023-01-05","runtime":120,"director":"导演","maker":"厂牌","actors":["演员A","演员B"],"genres":["剧情","办公室"]}}`
	_, hits := metatubeTestSetup(t, "mt_flow", search, detail)

	meta := metatubeFetchCached("ABC-123")
	if meta == nil || meta.Status != "ok" {
		t.Fatalf("首次刮削应命中: %+v", meta)
	}
	if meta.Title != "テスト作品 オフィスレディ編" {
		t.Errorf("标题取详情: %q", meta.Title)
	}
	if meta.Year != "2023" || meta.Runtime != 120 {
		t.Errorf("年份/时长解析错误: %q %d", meta.Year, meta.Runtime)
	}
	if meta.Publisher != "厂牌" || meta.Plot != "剧情简介" {
		t.Errorf("厂牌/简介解析错误: %q %q", meta.Publisher, meta.Plot)
	}
	if actors := avMetaActors(meta); len(actors) != 2 {
		t.Errorf("演员应为 2 人: %v", actors)
	}
	if meta.CoverURL != "http://example.com/cover.jpg" {
		t.Errorf("封面应为源站公网 URL（通知 picurl 用）: %q", meta.CoverURL)
	}
	// 第二次：命中缓存，不再请求服务器（返回同一条记录）
	firstID := meta.ID
	again := metatubeFetchCached("abc-123") // 大小写不同的同番号
	if again == nil || again.ID != firstID {
		t.Fatalf("缓存命中失败: %+v", again)
	}
	if n := atomic.LoadInt64(hits); n != 2 {
		t.Errorf("缓存生效时请求次数应为 2（search+detail），实际 %d", n)
	}
}

func TestMetatubeDataWrapperFormat(t *testing.T) {
	// 当前协议：搜索/详情都包在 {"data": …} 里
	search := `{"data":[{"id":"xyz","number":"GGG-111","title":"包装格式作品","provider":"javdb","cover_url":"http://c/x.jpg","release_date":"2024-02-03"}],"took":1.2}`
	detail := `{"data":{"id":"xyz","number":"GGG-111","title":"包装格式作品 详情","summary":"简介","provider":"javdb","release_date":"2024-02-03","runtime":100,"actors":["演员C"],"genres":["剧情"]}}`
	metatubeTestSetup(t, "mt_wrapper", search, detail)

	meta := metatubeFetchCached("GGG-111")
	if meta == nil || meta.Status != "ok" {
		t.Fatalf("包裹格式搜索应命中: %+v", meta)
	}
	if meta.Title != "包装格式作品 详情" {
		t.Errorf("详情标题错误: %q", meta.Title)
	}
	if meta.Year != "2024" {
		t.Errorf("年份错误: %q", meta.Year)
	}
	if actors := avMetaActors(meta); len(actors) != 1 || actors[0] != "演员C" {
		t.Errorf("演员解析错误: %v", actors)
	}
}

func TestMetatubeEmptyOkCacheSelfHeal(t *testing.T) {
	// 历史 bug 写入的"全空 ok 行"应自动重刮而非直接返回
	search := `{"data":[{"id":"e1","number":"HHH-222","title":"坏缓存自愈","provider":"javbus","release_date":"2023-06-01"}]}`
	detail := `{"data":{"id":"e1","number":"HHH-222","title":"坏缓存自愈","provider":"javbus","release_date":"2023-06-01"}}`
	metatubeTestSetup(t, "mt_selfheal", search, detail)
	model.DB.Create(&model.AVMeta{Num: "HHH222", Status: "ok", Title: "", CoverURL: ""})

	meta := metatubeFetchCached("HHH-222")
	if meta == nil || meta.Title != "坏缓存自愈" {
		t.Fatalf("空 ok 缓存应触发重刮自愈: %+v", meta)
	}
}

func TestMetatubeNotFoundTTL(t *testing.T) {
	_, hits := metatubeTestSetup(t, "mt_notfound", `{"data":[]}`, `{"data":{}}`)
	if meta := metatubeFetchCached("XYZ-999"); meta != nil {
		t.Fatalf("无结果应返回 nil: %+v", meta)
	}
	var row model.AVMeta
	if err := model.DB.Where("num = ?", "XYZ999").First(&row).Error; err != nil || row.Status != "not_found" {
		t.Fatalf("not_found 应落库: %v %+v", err, row)
	}
	// TTL 内不再请求
	metatubeFetchCached("XYZ-999")
	if n := atomic.LoadInt64(hits); n != 1 {
		t.Errorf("not_found TTL 内不应重刮，请求次数 %d", n)
	}
	// 改旧时间 → 过期 → 重刮
	model.DB.Model(&row).Update("updated_at", time.Now().Add(-8*24*time.Hour))
	metatubeFetchCached("XYZ-999")
	if n := atomic.LoadInt64(hits); n != 2 {
		t.Errorf("not_found 过期后应重刮，请求次数 %d", n)
	}
}

func TestMetatubeDisabled(t *testing.T) {
	_, hits := metatubeTestSetup(t, "mt_disabled", `{"data":[]}`, `{"data":{}}`)
	saveSettingRow(t, "metatube", `{"url":"http://x","enabled":false}`)
	if meta := metatubeFetchCached("ABC-001"); meta != nil {
		t.Fatalf("未启用应返回 nil: %+v", meta)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("未启用不应发请求，实际 %d", n)
	}
}

func TestRecordAVMediaDedupe(t *testing.T) {
	metatubeTestSetup(t, "mt_bare", `{"data":[]}`, `{"data":{}}`) // 仅借 DB 初始化
	meta := &model.AVMeta{Num: "DDD001", Status: "ok", Title: "标题一", Year: "2024", CoverURL: "http://c/1.jpg", Plot: "简介"}
	recordAVMedia(meta, "DDD-001", "有码", "有码/DDD-001")
	recordAVMedia(meta, "DDD-001", "有码", "有码/DDD-001") // 重复入库 → 更新而非新增
	var count int64
	model.DB.Model(&model.MediaLibrary{}).Where("media_type = ?", "av").Count(&count)
	if count != 1 {
		t.Fatalf("AV 记录应去重为 1 条，实际 %d", count)
	}
	var rec model.MediaLibrary
	model.DB.Where("media_type = ? AND original_title = ?", "av", "DDD-001").First(&rec)
	if rec.PosterPath != "/av:http://c/1.jpg" {
		t.Errorf("PosterPath 应带 /av: 前缀: %q", rec.PosterPath)
	}
	// 无元数据 → 标题回退番号
	recordAVMedia(nil, "DDD-002", "有码", "有码/DDD-002")
	rec = model.MediaLibrary{} // GORM First 不会清零 dest，残留主键会附加 id 条件
	model.DB.Where("media_type = ? AND original_title = ?", "av", "DDD-002").First(&rec)
	if rec.Title != "DDD-002" || rec.PosterPath != "" {
		t.Errorf("无元数据应回退番号: %+v", rec)
	}
}

func TestRenameAVMetaVars(t *testing.T) {
	metatubeTestSetup(t, "mt_rename", `{"data":[]}`, `{"data":{}}`)
	model.DB.Create(&model.AVMeta{
		Num: "EEE002", Status: "ok", Title: "タイトル/危険", Year: "2021",
		ActorsJSON: `["演员A","演员B"]`,
	})
	media := &TmdbMedia{Title: "EEE-002", MediaType: "av"}
	ctx := buildRenameContext(media, &ParsedName{Title: "EEE-002"}, "EEE-002.mp4")
	if got := ctx.ApplyTemplate("{num} {av_title} ({av_year})"); got != "EEE-002 タイトル 危険 (2021)" {
		t.Errorf("模板输出错误: %q", got)
	}
	if got := ctx.ApplyTemplate("{actor}"); got != "演员A" {
		t.Errorf("{actor} 应为第一主演: %q", got)
	}
	if got := ctx.ApplyTemplate("{actors}"); got != "演员A、演员B" {
		t.Errorf("{actors} 错误: %q", got)
	}
	// 无缓存 → 变量为空，块语法整块丢弃
	media2 := &TmdbMedia{Title: "FFF-003", MediaType: "av"}
	ctx2 := buildRenameContext(media2, &ParsedName{}, "x.mp4")
	if got := ctx2.ApplyTemplate("{num}< {av_title}>{ext}"); got != "FFF-003.mp4" {
		t.Errorf("无刮削结果应退回番号: %q", got)
	}
}

func TestMetatubePaddedNumRetry(t *testing.T) {
	// 文件名补零识别出的番号（JUVR-00303）搜索失败时，用去零变体（JUVR-303）重试
	if _, err := model.InitDB("file:mt_padded?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	var hits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/movies/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "JUVR-303" {
			_, _ = w.Write([]byte(`{"data":[{"id":"j1","number":"JUVR-303","title":"去零重试命中","provider":"javbus","release_date":"2024-05-06"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/v1/movies/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"j1","number":"JUVR-303","title":"去零重试命中","provider":"javbus","release_date":"2024-05-06"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		model.DB.Where("key = ?", "metatube").Delete(&model.Setting{})
		model.DB.Where("1 = 1").Delete(&model.AVMeta{})
	})
	saveSettingRow(t, "metatube", `{"url":"`+srv.URL+`","token":"","enabled":true}`)

	meta := metatubeFetchCached("JUVR-00303")
	if meta == nil || meta.Status != "ok" || meta.Title != "去零重试命中" {
		t.Fatalf("补零番号应通过去零变体重试命中: %+v", meta)
	}
}

func TestMetatubeSearchTitle(t *testing.T) {
	// 无番号 AV：标题关键词搜索命中 → 按命中番号落缓存并返回展示形态番号
	if _, err := model.InitDB("file:mt_titlesearch?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/movies/search", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("search hit q=%q", r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"t9","number":"HMNX-0098","title":"题名検索ヒット作","provider":"javdb","cover_url":"http://c/t.jpg","release_date":"2025-01-02","actors":["演员T"]}]}`))
	})
	mux.HandleFunc("/v1/movies/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"t9","number":"HMNX-0098","title":"题名検索ヒット作（详情）","provider":"javdb","release_date":"2025-01-02"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		model.DB.Where("key = ?", "metatube").Delete(&model.Setting{})
		model.DB.Where("1 = 1").Delete(&model.AVMeta{})
	})
	saveSettingRow(t, "metatube", `{"url":"`+srv.URL+`","token":"","enabled":true}`)

	t.Logf("enabled=%v", metatubeEnabled())
	meta, display := metatubeSearchTitle("题名検索ヒット作")
	if meta == nil || meta.Status != "ok" {
		t.Fatalf("标题搜索应命中: %+v", meta)
	}
	if display != "HMNX-0098" {
		t.Errorf("展示番号应为源站原样: %q", display)
	}
	// 缓存键 = 归一化番号，后续按番号查询直接命中
	if again := metatubeFetchCached("HMNX-0098"); again == nil || again.Title != "题名検索ヒット作（详情）" {
		t.Errorf("按番号查询应命中同一缓存: %+v", again)
	}
}
