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
	if rows[0].Name != "大陆动画" || rows[0].MediaType != "movie" || rows[0].Priority != 1 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[0].GenreIds != "16" || rows[0].OriginCountry != "CN" {
		t.Errorf("row0 fields = %+v", rows[0])
	}
	// 电影/ 前缀剥离
	if rows[1].Name != "其他电影" || !rows[1].IsDefault {
		t.Errorf("row1 = %+v", rows[1])
	}
	// tv 顺序
	if rows[2].Name != "儿童节目" || rows[2].GenreIds != "10762" {
		t.Errorf("row2 = %+v", rows[2])
	}
	if rows[3].Name != "大陆剧集" || rows[3].OriginCountry != "CN" || rows[3].IsDefault {
		t.Errorf("row3 = %+v", rows[3])
	}
	if rows[4].Name != "其他剧集" || !rows[4].IsDefault {
		t.Errorf("row4 = %+v", rows[4])
	}
	_ = model.DB
}

// 分类名写成一级目录名本身 = 不加二级分类，直接放 电影/ 剧集/ 这一层。
// 这样的条目必须留在规则表里当兜底，否则整理会落到「未分类」子目录
func TestParseCategoryYAMLBareMediaDir(t *testing.T) {
	src := `movie:
  电影:
tv:
  动漫番剧:
    genre_ids: '16'
  剧集:
`
	rows, err := parseCategoryYAML(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].MediaType != "movie" || rows[0].Name != "" || !rows[0].IsDefault {
		t.Errorf("row0 = %+v，电影 应作为 movie 的空名兜底", rows[0])
	}
	if rows[2].MediaType != "tv" || rows[2].Name != "" || !rows[2].IsDefault {
		t.Errorf("row2 = %+v，剧集 应作为 tv 的空名兜底", rows[2])
	}
}
