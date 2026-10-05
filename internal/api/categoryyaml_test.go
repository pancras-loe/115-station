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

// 认不出的条件不能让规则变成兜底：MoviePilot 的 release_year / production_countries 要认，
// 拼错的键、写成映射的值记进 Unsupported；列表写法按逗号拼；不加引号的 !CN 被 YAML 读成标签，要拼回来
func TestParseCategoryYAMLUnsupported(t *testing.T) {
	src := `movie:
  电影/老片:
    release_year: '1950-1989'
  电影/英国:
    production_countries: 'GB'
  电影/拼错:
    genre_id: '16'
  电影/映射:
    genre_ids:
      a: 1
  电影/列表:
    genre_ids: [16, 99]
  电影/非华语:
    origin_country: !CN
  电影/其他:
`
	rows, err := parseCategoryYAML(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d", len(rows))
	}
	if rows[0].ReleaseYear != "1950-1989" || rows[0].IsDefault {
		t.Errorf("release_year: %+v", rows[0])
	}
	if rows[1].ProductionCountries != "GB" || rows[1].IsDefault {
		t.Errorf("production_countries: %+v", rows[1])
	}
	if rows[2].Unsupported != "genre_id" || rows[2].IsDefault {
		t.Errorf("拼错的键: %+v", rows[2])
	}
	if rows[3].Unsupported != "genre_ids" || rows[3].IsDefault {
		t.Errorf("映射值: %+v", rows[3])
	}
	if rows[4].GenreIds != "16,99" || rows[4].Unsupported != "" {
		t.Errorf("列表值: %+v", rows[4])
	}
	if rows[5].OriginCountry != "!CN" || rows[5].IsDefault {
		t.Errorf("不加引号的排除: %+v", rows[5])
	}
	if !rows[6].IsDefault {
		t.Errorf("兜底: %+v", rows[6])
	}
}

// 匹配语义对齐 MoviePilot：且 / 或、! 排除、范围、不区分大小写、字段为空不匹配
func TestMatchCategory(t *testing.T) {
	yakka := &TmdbMedia{Title: "Yakka Dee!", MediaType: "tv", GenreIDs: []int{10762, 16},
		OrigLanguage: "en", OrigCountry: []string{"GB"}, Year: "2017"}
	cases := []struct {
		name string
		rule model.CategoryRule
		want bool
	}{
		{"儿童", model.CategoryRule{GenreIds: "10762"}, true},
		{"家庭不含儿童", model.CategoryRule{GenreIds: "10751"}, false},
		{"类型且国家", model.CategoryRule{GenreIds: "16", OriginCountry: "CN,TW,HK"}, false},
		{"小写国家", model.CategoryRule{OriginCountry: "us,gb"}, true},
		{"大写语言", model.CategoryRule{OriginalLanguage: "EN"}, true},
		{"排除命中", model.CategoryRule{GenreIds: "!10762"}, false},
		{"排除未命中", model.CategoryRule{OriginCountry: "!CN"}, true},
		{"正选加排除", model.CategoryRule{GenreIds: "16,!10762"}, false},
		{"数字范围", model.CategoryRule{GenreIds: "10760-10765"}, true},
		{"年份范围", model.CategoryRule{ReleaseYear: "2010-2019"}, true},
		{"年份范围外", model.CategoryRule{ReleaseYear: "1990-1999"}, false},
		{"排除年份范围", model.CategoryRule{ReleaseYear: "!2015-2020"}, false},
		{"制片国家为空", model.CategoryRule{ProductionCountries: "GB"}, false},
		{"认不出的条件", model.CategoryRule{GenreIds: "10762", Unsupported: "genre_id"}, false},
		{"只有 ext", model.CategoryRule{Ext: "iso"}, false},
		{"正则未命中只看其他条件", model.CategoryRule{CustomRegex: "^Peppa", GenreIds: "16"}, true},
		{"只有正则未命中", model.CategoryRule{CustomRegex: "^Peppa"}, false},
		{"正则命中", model.CategoryRule{CustomRegex: "(?i)yakka", GenreIds: "99"}, true},
		{"兜底", model.CategoryRule{}, true},
	}
	for _, c := range cases {
		if got := matchCategory(&c.rule, yakka); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	movie := &TmdbMedia{MediaType: "movie", ProdCountry: []string{"GB", "US"}}
	if !matchCategory(&model.CategoryRule{ProductionCountries: "gb"}, movie) {
		t.Error("制片国家应命中")
	}
	// 没有国家时，只写了排除也不匹配（MoviePilot 同款）
	if matchCategory(&model.CategoryRule{OriginCountry: "!CN"}, movie) {
		t.Error("产地为空时排除条件不应命中")
	}
}
