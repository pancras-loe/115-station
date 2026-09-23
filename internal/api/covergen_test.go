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
			if err := h.saveCoverGenCfg(coverGenCfg{Style: "3", Strategy: "rating", Blacklist: "排除库"}); err != nil {
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
	for _, style := range []string{"1", "2", "3"} {
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
	if w := coverTextWidth("动漫电影", 92); w <= 0 {
		t.Errorf("中文测宽失败: %d", w)
	}
	// 写一张样例图供人工检查
	out, _ := h.coverRenderWith("1", "动漫电影", posters)
	_ = os.WriteFile(os.TempDir()+"/cover-sample.png", out, 0644)
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

// 样式缩略图不能依赖 Emby/TMDB：没配任何服务也要四张都出得来，且是用弹窗里未保存的配置画的。
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
		// static_1 左上角是纯背景色，自定义红色必须生效
		if style == "static_1" {
			r, g, b, _ := im.At(5, 5).RGBA()
			if r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
				t.Errorf("自定义背景色未生效：%d %d %d", r>>8, g>>8, b>>8)
			}
		}
	}
}

// 沉浸背景的遮罩必须按真实透明度叠：曾用预乘的 color.RGBA 写半透明色，蓝色遮罩叠在红海报上
// 蓝通道直接溢出到 255，海报被整个盖死。
func TestCoverOverlayAlpha(t *testing.T) {
	cfg := normalizeCoverGenCfg(coverGenCfg{Style: "static_4", Background: "custom", CustomColor: "#0000ff", ColorRatio: 1, Blur: 0, Resolution: "480p"})
	im := coverCompose(cfg, "电影", []image.Image{fakePoster(color.RGBA{255, 0, 0, 255})})
	r, _, b, _ := im.At(10, 10).RGBA()
	if r>>8 < 80 || b>>8 > 200 {
		t.Fatalf("遮罩叠色错误：R=%d B=%d", r>>8, b>>8)
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
	c.Request = httptest.NewRequest(http.MethodPost, "/covergen/sample", strings.NewReader(`{"config":{"style":"static_2"},"live":true,"library":"剧集"}`))
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
