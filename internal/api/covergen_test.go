package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"strmhub/internal/model"
)

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
