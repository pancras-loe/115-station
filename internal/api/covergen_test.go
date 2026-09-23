package api

import (
	"115-station/internal/config"
	"115-station/internal/model"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 全量同步没有 MediaLibrary 台账也必须能生成；上传目标必须是实际库 ID。
func TestCoverFromEmbyWithoutLedger(t *testing.T) {
	var poster bytes.Buffer
	if err := png.Encode(&poster, fakePoster(color.RGBA{60, 90, 200, 255})); err != nil {
		t.Fatal(err)
	}
	for _, uploadStatus := range []int{204, 403} {
		t.Run(http.StatusText(uploadStatus), func(t *testing.T) {
			pushed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("api_key") != "test-key" {
					t.Error("未传递 API Key")
				}
				switch r.URL.Path {
				case "/Library/VirtualFolders":
					io.WriteString(w, `[{"Name":"我的电影","ItemId":"library-1","CollectionType":"movies"},{"Name":"排除库","ItemId":"library-2"}]`)
				case "/Items":
					q := r.URL.Query()
					if q.Get("ParentId") != "library-1" || q.Get("IncludeItemTypes") != "Movie" || q.Get("IsVirtualItem") != "false" || q.Get("SortBy") != "CommunityRating" {
						t.Errorf("取图范围错误：%v", q)
					}
					io.WriteString(w, `{"Items":[{"Id":"poster-1"}]}`)
				case "/Items/poster-1/Images/Primary":
					w.Write(poster.Bytes())
				case "/Items/library-1/Images/Primary":
					pushed = true
					if r.Method != http.MethodPost {
						t.Error("上传方法错误")
					}
					body, _ := io.ReadAll(r.Body)
					decoded, err := base64.StdEncoding.DecodeString(string(body))
					if err != nil {
						t.Fatal(err)
					}
					im, err := png.Decode(bytes.NewReader(decoded))
					if err != nil || im.Bounds().Dx() != 1280 {
						t.Error("未上传有效封面")
					}
					w.WriteHeader(uploadStatus)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			h := &Handler{Config: &config.Config{ConfigDir: dir, DataDir: dir}}
			setting, _ := json.Marshal(map[string]string{"server_url": server.URL, "api_key": "test-key"})
			if err := h.Config.SaveSetting("emby", string(setting)); err != nil {
				t.Fatal(err)
			}
			if err := h.saveCoverGenCfg(coverGenCfg{Style: "editorial_c", Strategy: "rating", Blacklist: "排除库"}); err != nil {
				t.Fatal(err)
			}
			n, _, warnings, err := h.runCoverGen()
			if err != nil || n != 1 || !pushed {
				t.Fatalf("生成失败：%d %v %v", n, pushed, err)
			}
			if (len(warnings) > 0) != (uploadStatus == 403) {
				t.Fatalf("推送结果未如实报告：%v", warnings)
			}
			if _, err := os.Stat(filepath.Join(dir, "library-covers", "我的电影.png")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fakePoster(c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCoverRenderStyles(t *testing.T) {
	h := &Handler{}
	posters := []image.Image{
		fakePoster(color.RGBA{200, 60, 60, 255}),
		fakePoster(color.RGBA{60, 160, 90, 255}),
		fakePoster(color.RGBA{60, 90, 200, 255}),
		fakePoster(color.RGBA{200, 180, 60, 255}),
		fakePoster(color.RGBA{160, 60, 200, 255}),
	}
	for _, style := range coverSampleStyles {
		out, err := h.coverRenderWith(style, "动漫电影", posters)
		if err != nil {
			t.Fatalf("样式 %s 渲染失败: %v", style, err)
		}
		im, err := png.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("样式 %s 输出非 PNG: %v", style, err)
		}
		if im.Bounds().Dx() != 1280 || im.Bounds().Dy() != 720 {
			t.Errorf("样式 %s 尺寸异常: %v", style, im.Bounds())
		}
	}
	if coverFontObj == nil {
		t.Fatalf("中文字体未加载（opentype 解析失败）")
	}
	if coverSerifFontObj == nil {
		t.Fatalf("宋体字体未加载（opentype 解析失败）")
	}
	if w := coverTextWidthFor("动漫电影", 92, true); w <= 0 {
		t.Errorf("宋体中文测宽失败: %d", w)
	}
}

func TestCoverEditorialAEqualStaircase(t *testing.T) {
	for _, size := range [][2]int{{854, 480}, {1280, 720}, {1920, 1080}} {
		rects := coverDesignARects(size[0], size[1])
		for i := 1; i < len(rects); i++ {
			if rects[i].Dx() != rects[0].Dx() || rects[i].Dy() != rects[0].Dy() {
				t.Fatalf("%dx%d: 海报尺寸不一致：%v", size[0], size[1], rects)
			}
			if rects[i].Min.X <= rects[i-1].Min.X || rects[i].Min.Y <= rects[i-1].Min.Y {
				t.Fatalf("%dx%d: 海报未呈阶梯排列：%v", size[0], size[1], rects)
			}
		}
		if rects[2].Max.X > size[0] || rects[2].Max.Y > size[1] {
			t.Fatalf("%dx%d: 最后一张海报越界：%v", size[0], size[1], rects[2])
		}
	}
}

func TestCoverLegacyStyleFallsBackToC(t *testing.T) {
	for _, style := range []string{"static_1", "static_2", "static_3", "static_4", "random", "1"} {
		if got := normalizeCoverGenCfg(coverGenCfg{Style: style}).Style; got != "editorial_c" {
			t.Fatalf("旧样式 %s 没有迁移到 C：%s", style, got)
		}
	}
}

func TestCoverNewStylesWithOnePoster(t *testing.T) {
	poster := fakePoster(color.RGBA{R: 210, G: 55, B: 40, A: 255})
	for _, style := range []string{"editorial_d", "editorial_e"} {
		cfg := defaultCoverGenCfg()
		cfg.Style = style
		im := coverCompose(cfg, "动漫电影", []image.Image{poster})
		points := []image.Point{{X: 300, Y: 350}}
		if style == "editorial_e" {
			points = []image.Point{{X: 120, Y: 350}, {X: 380, Y: 350}, {X: 640, Y: 350}, {X: 900, Y: 350}, {X: 1150, Y: 350}}
		}
		for _, pt := range points {
			r, _, _, _ := im.At(pt.X, pt.Y).RGBA()
			if r>>8 < 180 {
				t.Fatalf("样式 %s 单张海报未覆盖位置 %v", style, pt)
			}
		}
	}
}

func TestCoverSortItems(t *testing.T) {
	mk := func(title string, vote float64, year string) model.MediaLibrary {
		return model.MediaLibrary{Title: title, VoteAverage: vote, Year: year}
	}
	items := []model.MediaLibrary{mk("A", 6.0, "2020"), mk("B", 9.1, "2023"), mk("C", 7.5, "2025")}
	h := &Handler{}
	h.coverSortItems(items, "rating")
	if items[0].Title != "B" {
		t.Errorf("rating 策略应最高分在前: %s", items[0].Title)
	}
	h.coverSortItems(items, "release")
	if items[0].Title != "C" {
		t.Errorf("release 策略应最新年份在前: %s", items[0].Title)
	}
	h.coverSortItems(items, "title")
	if items[0].Title != "A" {
		t.Errorf("title 策略应字母序在前: %s", items[0].Title)
	}
}

// 样式缩略图不能依赖 Emby/TMDB：没配任何服务也要五张都出得来。
func TestCoverSampleDemo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	h := &Handler{Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	body := `{"config":{"background":"custom","custom_color":"#ff0000","color_ratio":1},"live":false}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/covergen/sample", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CoverGenSample(c)
	if w.Code != http.StatusOK {
		t.Fatalf("预览失败：%d %s", w.Code, w.Body.String())
	}
	var resp struct{ Samples map[string]string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Samples) != 5 {
		t.Fatalf("应只展示五款新样式：%v", resp.Samples)
	}
	for _, style := range coverSampleStyles {
		u := resp.Samples[style]
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, "data:image/jpeg;base64,"))
		if err != nil {
			t.Fatalf("样式 %s 不是 JPEG dataURL", style)
		}
		im, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil || im.Bounds().Dx() != 854 {
			t.Fatalf("样式 %s 预览尺寸异常：%v", style, err)
		}
	}
}

// C 的中央遮罩应保留海报颜色，边缘比中间更亮。
func TestCoverOverlayAlpha(t *testing.T) {
	cfg := normalizeCoverGenCfg(coverGenCfg{Style: "editorial_c", Blur: 0, Resolution: "480p"})
	im := coverCompose(cfg, "电影", []image.Image{fakePoster(color.RGBA{255, 0, 0, 255})})
	edgeR, _, edgeB, _ := im.At(10, 10).RGBA()
	centerR, _, _, _ := im.At(427, 10).RGBA()
	if edgeR <= centerR || edgeR>>8 < 150 || edgeB>>8 > 40 {
		t.Fatalf("遮罩叠色错误：边缘 R=%d B=%d；中央 R=%d", edgeR>>8, edgeB>>8, centerR>>8)
	}
}

// 真实海报预览：用弹窗里的配置出图，但绝不能推 Emby、也不能写本地缓存。
func TestCoverSampleLiveNoSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var poster bytes.Buffer
	_ = png.Encode(&poster, fakePoster(color.RGBA{60, 90, 200, 255}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method != http.MethodGet:
			t.Errorf("预览不应发写请求：%s %s", r.Method, r.URL.Path)
		case r.URL.Path == "/Library/VirtualFolders":
			io.WriteString(w, `[{"Name":"电影","ItemId":"lib-1","CollectionType":"movies"},{"Name":"剧集","ItemId":"lib-2","CollectionType":"tvshows"}]`)
		case r.URL.Path == "/Items":
			io.WriteString(w, `{"Items":[{"Id":"p-`+r.URL.Query().Get("ParentId")+`"}]}`)
		default:
			w.Write(poster.Bytes())
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	h := &Handler{Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	setting, _ := json.Marshal(map[string]string{"server_url": server.URL, "api_key": "k"})
	_ = h.Config.SaveSetting("emby", string(setting))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/covergen/sample", strings.NewReader(`{"config":{"style":"editorial_b"},"live":true,"library":"剧集"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CoverGenSample(c)
	if w.Code != http.StatusOK {
		t.Fatalf("预览失败：%d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Image, Library string
		Libraries      []string
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Library != "剧集" || len(resp.Libraries) != 2 || !strings.HasPrefix(resp.Image, "data:image/jpeg;base64,") {
		t.Fatalf("预览结果不对：%s %v", resp.Library, resp.Libraries)
	}
	if _, err := os.Stat(coverOutDir(dir)); !os.IsNotExist(err) {
		t.Fatal("预览不应写本地海报缓存")
	}
}
