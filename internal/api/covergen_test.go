package api

import (
	"115-station/internal/config"
	"115-station/internal/model"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
