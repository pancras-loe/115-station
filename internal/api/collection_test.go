package api

import (
	"testing"

	"115-station/internal/model"
)

// 合集目录：往下再认一层，自己整理的库与分类库都一样
func TestCollectionTitleOf(t *testing.T) {
	self := libCategoryLayout{libs: layoutLibsOf([]model.Library{
		{Local: "动漫", Depth: 1, Collections: `["高达系列","高达系列/外传"]`},
	}, nil)}
	cat := libCategoryLayout{libs: layoutLibsOf([]model.Library{
		{Local: "影视", SchemeID: 1, Collections: `["日漫/高达系列"]`},
	}, map[uint][]model.CategoryRule{1: {{Name: "日漫", MediaType: "tv"}}})}

	cases := []struct {
		name   string
		layout libCategoryLayout
		rel    string
		key    string
		dir    string
		cat    string
		mt     string
		ok     bool
	}{
		{"没标记的照旧", self, "动漫/海贼王/Season 1/E01.strm", "动漫/海贼王", "海贼王", "", "", true},
		{"合集里的剧", self, "动漫/高达系列/机动战士Z高达/Season 1/E01.strm", "动漫/高达系列/机动战士Z高达", "机动战士Z高达", "", "", true},
		{"合集里直接放的剧场版", self, "动漫/高达系列/逆袭的夏亚.1988.strm", "动漫/高达系列/逆袭的夏亚.1988.strm", "逆袭的夏亚.1988", "", "movie", true},
		{"合集里带集号的散文件不算", self, "动漫/高达系列/S01E01.strm", "", "", "", "", false},
		{"合集层的 NFO 不属于任何片目", self, "动漫/高达系列/tvshow.nfo", "", "", "", "", false},
		{"合集套合集", self, "动漫/高达系列/外传/08小队/E01.strm", "动漫/高达系列/外传/08小队", "08小队", "", "", true},
		{"分类库：分类沿用合集的", cat, "影视/日漫/高达系列/闪光的哈萨维/a.strm", "影视/日漫/高达系列/闪光的哈萨维", "闪光的哈萨维", "日漫", "", true},
		{"分类库：没标记的类型照分类", cat, "影视/日漫/海贼王/E01.strm", "影视/日漫/海贼王", "海贼王", "日漫", "tv", true},
	}
	for _, c := range cases {
		key, dir, category, mt, ok := c.layout.titleOf(c.rel)
		if key != c.key || dir != c.dir || category != c.cat || mt != c.mt || ok != c.ok {
			t.Errorf("%s: titleOf(%q) = %q %q %q %q %v，期望 %q %q %q %q %v",
				c.name, c.rel, key, dir, category, mt, ok, c.key, c.dir, c.cat, c.mt, c.ok)
		}
	}

	if !self.isCollectionKey("动漫/高达系列") || self.isCollectionKey("动漫/海贼王") {
		t.Error("isCollectionKey 认错")
	}
	if got := self.collectionOf("动漫/高达系列/逆袭的夏亚.1988.strm"); got != "动漫/高达系列" {
		t.Errorf("collectionOf 平铺 = %q", got)
	}
	if got := self.collectionOf("动漫/海贼王"); got != "" {
		t.Errorf("collectionOf 不在合集里 = %q", got)
	}
}

func TestCollectionNameHint(t *testing.T) {
	for name, want := range map[string]bool{
		"机动战士高达（系列）":      true,
		"机动战士高达（系列":       true,
		"指环王三部曲":          true,
		"宫崎骏合集 1984-2013": true,
		"漫威大全":            true,
		"海贼王":             false,
		"系列之一 某某":         false,
		"老友记 全集":          false, // 「全集」多半是一部剧的全部季，不提示
	} {
		if got := collectionNameHint(name); got != want {
			t.Errorf("collectionNameHint(%q) = %v，期望 %v", name, got, want)
		}
	}
}

func TestCollectionLeftover(t *testing.T) {
	for name, want := range map[string]bool{
		"tvshow.nfo": true, "Poster.jpg": true, "fanart.png": true, "clearlogo.png": true,
		"逆袭的夏亚.nfo": false, "逆袭的夏亚-poster.jpg": false, "a.strm": false,
	} {
		if got := isCollectionLeftover(name); got != want {
			t.Errorf("isCollectionLeftover(%q) = %v，期望 %v", name, got, want)
		}
	}
}
