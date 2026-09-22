package api

import (
	"testing"

	"115-station/internal/model"
)

// 二级分类 YAML → 规则表：顺序、前缀剥离、无条件兜底
func TestParseCategoryYAML(t *testing.T) {
	src := `movie:
  电影/大陆动画:
    genre_ids: '16'
    origin_country: 'CN'
  电影/其他电影:
tv:
  电视剧/儿童节目:
    genre_ids: '10762'
  电视剧/大陆剧集:
    origin_country: 'CN'
  电视剧/其他剧集:
av:
  无码:
    num_prefix: 'ABC'
`
	rows, err := parseCategoryYAML(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("want 5 rows (av 不解析), got %d", len(rows))
	}
	// 顺序与优先级
	if rows[0].Name != "电影/大陆动画" || rows[0].MediaType != "movie" || rows[0].Priority != 1 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].GenreIds != "16" || rows[0].OriginCountry != "CN" {
		t.Errorf("row0 fields = %+v", rows[0])
	}
	// 分类名原样保留（不剥「电影/」前缀，它就是库内目录）
	if rows[1].Name != "电影/其他电影" || !rows[1].IsDefault {
		t.Errorf("row1 = %+v", rows[1])
	}
	// tv 顺序
	if rows[2].Name != "电视剧/儿童节目" || rows[2].GenreIds != "10762" {
		t.Errorf("row2 = %+v", rows[2])
	}
	if rows[3].Name != "电视剧/大陆剧集" || rows[3].OriginCountry != "CN" || rows[3].IsDefault {
		t.Errorf("row3 = %+v", rows[3])
	}
	if rows[4].Name != "电视剧/其他剧集" || !rows[4].IsDefault {
		t.Errorf("row4 = %+v", rows[4])
	}
	_ = model.DB
}

// 平铺写法：tv 下并列 动漫番剧/综艺/剧集，分类名就是库根下的目录名。
// 解析不能剥前缀也不能丢条目，否则整理会落到 剧集/动漫番剧 或「未分类」
func TestParseCategoryYAMLFlatLayout(t *testing.T) {
	src := `movie:
  电影:
tv:
  动漫番剧:
    genre_ids: '16'
  综艺:
    genre_ids: '10764,10767'
  剧集:
`
	rows, err := parseCategoryYAML(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d: %+v", len(rows), rows)
	}
	want := []struct {
		mediaType, name string
		isDefault       bool
	}{
		{"movie", "电影", true},
		{"tv", "动漫番剧", false},
		{"tv", "综艺", false},
		{"tv", "剧集", true},
	}
	for i, w := range want {
		if rows[i].MediaType != w.mediaType || rows[i].Name != w.name || rows[i].IsDefault != w.isDefault {
			t.Errorf("row%d = %+v，预期 %v", i, rows[i], w)
		}
	}
	_ = model.DB
}
