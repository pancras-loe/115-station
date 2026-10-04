package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

func TestPickLoginWallpapers(t *testing.T) {
	got := pickLoginWallpapers([]tmdbTrendingItem{
		{MediaType: "movie", Title: "沙丘2", BackdropPath: "/a.jpg", ReleaseDate: "2024-02-27"},
		{MediaType: "tv", Name: "三体", BackdropPath: "/b.jpg", FirstAirDate: "2023-01-15"},
		{MediaType: "person", Name: "某演员", BackdropPath: "/c.jpg"},
		{MediaType: "movie", Title: "没剧照"},
		{MediaType: "movie", Title: "坏路径", BackdropPath: "/../x.jpg"},
	})
	if len(got) != 2 {
		t.Fatalf("应只留两条，实际 %+v", got)
	}
	if got[0].Title != "沙丘2" || got[0].Year != "2024" || got[1].Title != "三体" || got[1].Year != "2023" {
		t.Fatalf("片名 / 年份不对: %+v", got)
	}
}

func TestLoginWallWidth(t *testing.T) {
	for q, want := range map[string]int{"": 960, "x": 960, "0": 960, "500": 960, "960": 960, "961": 1280, "3000": 1280} {
		if got := loginWallWidth(q); got != want {
			t.Errorf("loginWallWidth(%q) = %d, want %d", q, got, want)
		}
	}
}

func resetLoginWall(dataDir string) {
	loginWall.Lock()
	loginWall.dataDir, loginWall.items, loginWall.at, loginWall.tried = dataDir, nil, time.Time{}, time.Time{}
	loginWall.lastErr, loginWall.running = "", false
	loginWall.Unlock()
}

// 预取转码 → 只列本地有的 → TMDB 断了沿用旧列表、重启读回落盘的列表 → 图片接口只认列表里的路径
func TestLoginWallPrefetchAndOffline(t *testing.T) {
	newTestDB(t, "loginwall.db")
	src := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for x := 0; x < 1280; x++ {
		for y := 0; y < 720; y++ {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var jpg bytes.Buffer
	_ = jpeg.Encode(&jpg, src, &jpeg.Options{Quality: 90})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/trending/all/week"):
			_, _ = w.Write([]byte(`{"results":[
				{"media_type":"movie","title":"甲","backdrop_path":"/a.jpg","release_date":"2026-01-01"},
				{"media_type":"tv","name":"乙","backdrop_path":"/b.jpg","first_air_date":"2025-05-01"}]}`))
		case strings.HasPrefix(r.URL.Path, "/t/p/w1280/"):
			_, _ = w.Write(jpg.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	model.DB.Create(&model.TmdbConfig{ApiKey: "k", ApiUrl: srv.URL, ImageApiUrl: srv.URL})

	dataDir := t.TempDir()
	resetLoginWall(dataDir)
	refreshLoginWallIfStale()
	items := currentLoginWallpapers()
	if len(items) != 2 || items[0].Title != "甲" || !strings.HasPrefix(items[0].Thumb, "data:image/jpeg;base64,") {
		t.Fatalf("应预取两张并带内嵌小图: %+v", items)
	}
	if len(items[0].Thumb) > 4000 {
		t.Fatalf("内嵌小图太大: %d 字节", len(items[0].Thumb))
	}
	big, _ := os.ReadFile(imgCacheFile(dataDir, loginWallCacheNS, "/a.jpg@960"))
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(big)); err != nil || cfg.Width != 960 {
		t.Fatalf("960 档应是 960 宽: %+v %v", cfg, err)
	}

	// TMDB 断了：刷新失败，旧列表照用
	srv.Close()
	loginWall.Lock()
	loginWall.at, loginWall.tried = time.Time{}, time.Time{}
	loginWall.Unlock()
	refreshLoginWallIfStale()
	if n := len(currentLoginWallpapers()); n != 2 {
		t.Fatalf("TMDB 不通应沿用旧的 2 张，实有 %d", n)
	}

	// 重启：从落盘的列表读回来，不需要 TMDB
	resetLoginWall("")
	loadLoginWallState(dataDir)
	if n := len(currentLoginWallpapers()); n != 2 {
		t.Fatalf("重启后应读回 2 张，实有 %d", n)
	}

	// 缓存被清掉的那张不再列出
	_ = os.Remove(imgCacheFile(dataDir, loginWallCacheNS, "/b.jpg@1280"))
	if got := currentLoginWallpapers(); len(got) != 1 || got[0].Path != "/a.jpg" {
		t.Fatalf("缺图的不该列出: %+v", got)
	}

	gin.SetMode(gin.TestMode)
	h := &Handler{}
	get := func(q string) (int, *bytes.Buffer) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/auth/wallpaper?"+q, nil)
		h.LoginWallpaperImage(c)
		return c.Writer.Status(), rec.Body
	}
	if code, _ := get("path=/zzz.jpg&w=960"); code != http.StatusNotFound {
		t.Fatalf("列表外的路径应 404，实为 %d", code)
	}
	code, body := get("path=/a.jpg&w=1920")
	if cfg, _, err := image.DecodeConfig(body); code != http.StatusOK || err != nil || cfg.Width != 1280 {
		t.Fatalf("w=1920 应给 1280 档: %d %+v %v", code, cfg, err)
	}
}
