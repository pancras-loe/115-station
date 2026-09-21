package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 库类型 → 计数用的条目类型。混合库（CollectionType 为空）按电影+剧集算
func TestEmbyCountTypes(t *testing.T) {
	cases := map[string]string{
		"movies":  "Movie",
		"tvshows": "Series",
		"boxsets": "BoxSet",
		"":        "Movie,Series",
		"mixed":   "Movie,Series",
		"MOVIES":  "Movie", // Emby 各版本大小写不一致
	}
	for in, want := range cases {
		if got := embyCountTypes(in); got != want {
			t.Errorf("embyCountTypes(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// embyStub 一个最小 Emby：电影库里有 2 部电影，但每部各自占一个目录条目。
// 不带 IncludeItemTypes 查会连目录一起数（4 条）——正是「显示 1243 实际 619」的成因
type embyStub struct {
	mu       sync.Mutex
	lastTyps []string
}

func (s *embyStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/Items/Counts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"MovieCount": 619, "SeriesCount": 42, "EpisodeCount": 1800})
	})
	mux.HandleFunc("/Library/MediaFolders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"Items": []map[string]any{
			{"Id": "1", "Name": "电影", "CollectionType": "movies"},
			{"Id": "2", "Name": "电视剧", "CollectionType": "tvshows"},
		}})
	})
	mux.HandleFunc("/Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("ParentId") == "" { // 最新入库
			writeJSON(w, map[string]any{"Items": []map[string]any{
				{"Id": "900", "Name": "沙丘 2", "ProductionYear": float64(2024)},
			}})
			return
		}
		s.mu.Lock()
		s.lastTyps = append(s.lastTyps, q.Get("IncludeItemTypes"))
		s.mu.Unlock()
		total := 4 // 不过滤类型时：2 部影片 + 2 个影片目录
		if q.Get("IncludeItemTypes") != "" {
			total = 2
		}
		writeJSON(w, map[string]any{
			"TotalRecordCount": float64(total),
			"Items":            []map[string]any{{"Id": "11"}, {"Id": "12"}},
		})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func embyDashHandler(t *testing.T, serverURL string) *Handler {
	t.Helper()
	if _, err := model.InitDB("file:dash_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM settings")
		model.DB.Exec("DELETE FROM media_libraries")
		model.DB = nil
		embyDashMu.Lock()
		embyDashData = nil
		embyDashMu.Unlock()
	})
	cfg, _ := json.Marshal(map[string]string{"server_url": serverURL, "api_key": "k"})
	model.DB.Where("key = ?", "emby").Delete(&model.Setting{})
	if err := model.DB.Create(&model.Setting{Key: "emby", Value: string(cfg)}).Error; err != nil {
		t.Fatalf("写 emby 配置失败: %v", err)
	}
	// 缓存是包级的，上一个用例留下的数据会让本用例读不到 stub
	embyDashMu.Lock()
	embyDashData = nil
	embyDashMu.Unlock()
	// Config 不能是 nil：仪表盘顺手要读 115 容量（get115Cookie → Config.LoadCookie）
	return &Handler{DB: model.DB, Config: &config.Config{ConfigDir: t.TempDir(), DataDir: t.TempDir()}}
}

// 媒体库计数必须按库类型过滤条目：不过滤时 stub 会回 4，过滤后才是真实的 2
func TestFetchEmbyDashboardCountsByItemType(t *testing.T) {
	stub := &embyStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	h := embyDashHandler(t, srv.URL)
	out := fetchEmbyDashboard(h, true)
	if out == nil {
		t.Fatal("期望拿到 Emby 数据，得到 nil")
	}

	libs, _ := out["libraries"].([]gin.H)
	if len(libs) != 2 {
		t.Fatalf("期望 2 个媒体库，得到 %d", len(libs))
	}
	for _, l := range libs {
		if n, _ := l["count"].(int); n != 2 {
			t.Errorf("媒体库 %v 计数 %d，期望 2（不过滤类型会得到 4）", l["name"], n)
		}
	}
	if libs[0]["type_label"] != "电影" || libs[1]["type_label"] != "剧集" {
		t.Errorf("库类型标签不对: %v / %v", libs[0]["type_label"], libs[1]["type_label"])
	}

	stub.mu.Lock()
	typs := append([]string(nil), stub.lastTyps...)
	stub.mu.Unlock()
	want := map[string]bool{"Movie": true, "Series": true}
	if len(typs) != 2 {
		t.Fatalf("期望两次带类型的计数请求，得到 %v", typs)
	}
	for _, ty := range typs {
		if !want[ty] {
			t.Errorf("计数请求的 IncludeItemTypes=%q 不在预期内", ty)
		}
	}

	counts, _ := out["counts"].(gin.H)
	if counts["movies"] != 619 {
		t.Errorf("/Items/Counts 的电影数应原样带出，得到 %v", counts["movies"])
	}
}

// Emby 可用时顶部的电影/剧集数以 Emby 为准，台账数字仍作为 local_* 一起返回
func TestDashboardPrefersEmbyCounts(t *testing.T) {
	stub := &embyStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	h := embyDashHandler(t, srv.URL)

	// 台账里有 3 条电影（历史水位），Emby 说 619
	for i := 0; i < 3; i++ {
		model.DB.Create(&model.MediaLibrary{TmdbID: i + 1, Title: "T", MediaType: "movie"})
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/dashboard?refresh=1", nil)
	h.DashboardEnhanced(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d", w.Code)
	}
	var body struct {
		Media struct {
			Movies      int    `json:"movies"`
			LocalMovies int    `json:"local_movies"`
			Source      string `json:"source"`
		} `json:"media"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.Media.Movies != 619 || body.Media.Source != "emby" {
		t.Errorf("期望以 Emby 为准（619/emby），得到 %d/%s", body.Media.Movies, body.Media.Source)
	}
	if body.Media.LocalMovies != 3 {
		t.Errorf("台账数字应照常带出，期望 3 得到 %d", body.Media.LocalMovies)
	}
}

// ==================== 台账校准 ====================

// 剧集的 TargetPath 落在季目录里，要再往上一层才是标题目录
func TestMediaLibTitleDir(t *testing.T) {
	cases := map[string]string{
		"电影/华语电影/让子弹飞 (2010)/让子弹飞.mkv":           "电影/华语电影/让子弹飞 (2010)",
		"电视剧/国产剧/三体 (2023)/Season 01/S01E01.mkv": "电视剧/国产剧/三体 (2023)",
		"电视剧/国产剧/三体 (2023)/第一季/S01E01.mkv":       "电视剧/国产剧/三体 (2023)",
		"电影/孤零零.mkv": "电影",
		"孤零零.mkv":    "",
		"":           "",
	}
	for in, want := range cases {
		if got := mediaLibTitleDir(in); got != want {
			t.Errorf("mediaLibTitleDir(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 造一棵 根/影视/电影/片名/*.strm 的本地树
func buildLocalTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, file string) {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if file != "" {
			if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("影视/电影/让子弹飞 (2010)", "让子弹飞.strm")
	mk("影视/电影/沙丘 2 (2024)", "沙丘 2.strm")
	mk("影视/电视剧/三体 (2023)/Season 01", "S01E01.strm")
	return root
}

// 本地还在的留下，本地已经没有的判定为失效；台账没记落点的一律跳过
func TestAuditMediaLibrary(t *testing.T) {
	root := buildLocalTree(t)
	libs := mediaLibRoots(root)
	rows := []model.MediaLibrary{
		{Title: "让子弹飞", TargetPath: "电影/让子弹飞 (2010)/让子弹飞.mkv"},
		{Title: "三体", TargetPath: "电视剧/三体 (2023)/Season 01/S01E01.mkv"},
		{Title: "已被手工删掉", TargetPath: "电影/不存在了 (1999)/x.mkv"},
		{Title: "没有落点", TargetPath: ""},
	}
	got := auditMediaLibrary(rows, root, libs)
	if len(got) != 4 {
		t.Fatalf("期望 4 条结论，得到 %d", len(got))
	}
	if !got[0].Alive || !got[1].Alive {
		t.Errorf("本地还在的被判成失效: %+v %+v", got[0], got[1])
	}
	if got[2].Alive || got[2].Skip {
		t.Errorf("本地已删的应判为失效: %+v", got[2])
	}
	if !got[3].Skip {
		t.Errorf("没有落点的应跳过而不是删掉: %+v", got[3])
	}
}

// 本地实际部数：标题目录算一部，季目录不重复计
func TestMediaLibLocalCounts(t *testing.T) {
	root := buildLocalTree(t)
	got := mediaLibLocalCounts(root)
	want := map[string]int{"电影": 2, "电视剧": 1}
	if len(got) != 2 {
		t.Fatalf("期望 2 个库，得到 %v", got)
	}
	for _, g := range got {
		name, _ := g["name"].(string)
		count, _ := g["count"].(int)
		if want[name] != count {
			t.Errorf("库 %s 数出 %d，期望 %d", name, count, want[name])
		}
	}
}

// 全部台账都对不上本地目录时拒绝执行：那是路径配错，不是库被清空
func TestCalibrateRefusesWhenNothingMatches(t *testing.T) {
	root := buildLocalTree(t)
	if _, err := model.InitDB("file:dash_cal_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM settings")
		model.DB.Exec("DELETE FROM media_libraries")
		model.DB = nil
	})
	full, _ := json.Marshal(map[string]string{"local_path": root})
	model.DB.Create(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Create(&model.MediaLibrary{TmdbID: 1, Title: "A", TargetPath: "别的库/A (2020)/a.mkv"})
	model.DB.Create(&model.MediaLibrary{TmdbID: 2, Title: "B", TargetPath: "别的库/B (2020)/b.mkv"})

	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: t.TempDir(), DataDir: t.TempDir()}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/media-library/calibrate",
		jsonBody(`{"apply":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CalibrateMediaLibrary(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 拒绝，得到 %d: %s", w.Code, w.Body.String())
	}
	var n int64
	model.DB.Model(&model.MediaLibrary{}).Count(&n)
	if n != 2 {
		t.Errorf("被拒绝时不该删任何台账行，剩 %d 条", n)
	}
}

// 正常校准：失效行被清掉，本地还在的保留
func TestCalibrateRemovesStaleRows(t *testing.T) {
	root := buildLocalTree(t)
	if _, err := model.InitDB("file:dash_cal2_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM settings")
		model.DB.Exec("DELETE FROM media_libraries")
		model.DB = nil
	})
	full, _ := json.Marshal(map[string]string{"local_path": root})
	model.DB.Create(&model.Setting{Key: "full", Value: string(full)})
	model.DB.Create(&model.MediaLibrary{TmdbID: 1, Title: "让子弹飞",
		TargetPath: "电影/让子弹飞 (2010)/让子弹飞.mkv"})
	model.DB.Create(&model.MediaLibrary{TmdbID: 2, Title: "已删",
		TargetPath: "电影/不存在了 (1999)/x.mkv"})

	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: t.TempDir(), DataDir: t.TempDir()}}
	gin.SetMode(gin.TestMode)

	// 预演不动数据
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/media-library/calibrate", jsonBody(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CalibrateMediaLibrary(c)
	if w.Code != http.StatusOK {
		t.Fatalf("预演 HTTP %d: %s", w.Code, w.Body.String())
	}
	var preview struct {
		Stale, Removed, Kept int
	}
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Stale != 1 || preview.Removed != 0 || preview.Kept != 1 {
		t.Fatalf("预演结果不对: %+v", preview)
	}
	var n int64
	model.DB.Model(&model.MediaLibrary{}).Count(&n)
	if n != 2 {
		t.Fatalf("预演不该删数据，剩 %d", n)
	}

	// 执行
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/media-library/calibrate", jsonBody(`{"apply":true}`))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.CalibrateMediaLibrary(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("执行 HTTP %d: %s", w2.Code, w2.Body.String())
	}
	model.DB.Model(&model.MediaLibrary{}).Count(&n)
	if n != 1 {
		t.Fatalf("执行后应只剩 1 条，剩 %d", n)
	}
	var left model.MediaLibrary
	model.DB.First(&left)
	if left.Title != "让子弹飞" {
		t.Errorf("留下的不是本地还在的那条: %s", left.Title)
	}
}

func jsonBody(s string) io.Reader { return strings.NewReader(s) }
