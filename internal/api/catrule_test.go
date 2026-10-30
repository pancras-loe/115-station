package api

import (
	"strings"
	"testing"

	"115-station/internal/model"
)

func catRuleOf(t *testing.T, src string) []model.CategoryRule {
	t.Helper()
	rows, err := parseCategoryYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// 值的写法：分隔符、+ 必须、- / ! 排除、区间不被 - 前缀误伤
func TestCatEvalTerms(t *testing.T) {
	cases := []struct {
		expr string
		have []string
		want bool
	}{
		{"28，12；14", []string{"14"}, true}, // 中文逗号、分号都是分隔符
		{"28\n12", []string{"12"}, true},
		{"+28,-27", []string{"28", "18"}, true},
		{"+28,-27", []string{"28", "27"}, false},
		{"+28,!27", []string{"28", "27"}, false},
		{"+28,12,-27", []string{"28"}, false}, // 写了候选就得命中一个（nanl 文档里的那个坑）
		{"+28,12,-27", []string{"28", "12"}, true},
		{"-27", []string{"27"}, false}, // 原来被当成区间、反而命中 27
		{"-27", []string{"16"}, true},
		{"1990-1999", []string{"1995"}, true},
		{"1999-1990", []string{"1995"}, true}, // 倒序自动修正
		{"-1990-1999", []string{"1995"}, false},
		{"-27", nil, false}, // 没数据按不命中
	}
	for _, c := range cases {
		if ok, _ := catEval(c.expr, c.have, false); ok != c.want {
			t.Errorf("%q vs %v = %v，要 %v", c.expr, c.have, ok, c.want)
		}
	}
	// 文本字段：子串、不分大小写，第一个命中的候选按书写顺序
	if ok, hit := catEval("漫威,Marvel", []string{"MARVEL Studios", "漫威影业"}, true); !ok || hit != "漫威" {
		t.Errorf("文本匹配：%v %q", ok, hit)
	}
	if ok, _ := catEval("+marvel,-漫画", []string{"Marvel Comics 漫画改编"}, true); ok {
		t.Error("文本排除没生效")
	}
}

func TestMatchCategoryOptional(t *testing.T) {
	rows := catRuleOf(t, `movie:
  电影/演唱会:
    ?keywords: '演唱会,concert'
    ?include_keywords: 'live concert'
  电影/华语动画:
    genre_ids: '16'
    ?origin_country: 'CN'
    ?original_language: 'zh'
  电影/其他:
`)
	concert := &TmdbMedia{MediaType: "movie", Title: "某某 2019 巡回演唱会", GenreIDs: []int{10402}}
	tagged := &TmdbMedia{MediaType: "movie", Title: "Stop Making Sense", Keywords: []string{"live concert", "music"}}
	cnAnim := &TmdbMedia{MediaType: "movie", Title: "哪吒", GenreIDs: []int{16}, OrigLanguage: "zh", OrigCountry: []string{"CN"}}
	twAnim := &TmdbMedia{MediaType: "movie", Title: "某片", GenreIDs: []int{16}, OrigLanguage: "zh"}
	jpAnim := &TmdbMedia{MediaType: "movie", Title: "某片", GenreIDs: []int{16}, OrigLanguage: "ja", OrigCountry: []string{"JP"}}
	for _, c := range []struct {
		m    *TmdbMedia
		want string
	}{{concert, "电影/演唱会"}, {tagged, "电影/演唱会"}, {cnAnim, "电影/华语动画"}, {twAnim, "电影/华语动画"}, {jpAnim, "电影/其他"}} {
		if got := catTry(rows, c.m).Dest; got != c.want {
			t.Errorf("%s：进了 %s，要 %s", c.m.Title, got, c.want)
		}
	}
	if why := categoryMissReason(&rows[1], jpAnim); !strings.Contains(why, "可选条件") {
		t.Errorf("没命中原因：%s", why)
	}
}

func TestMatchCategorySeries(t *testing.T) {
	rows := catRuleOf(t, `movie:
  电影/周星驰:
    series_actors: '周星驰'
  电影/动作:
    genre_ids: '28'
    series_keywords: '碟中谍,速度与激情'
    series_directors: '成龙'
  电影/其他:
    series_keywords: '+变形金刚'
`)
	if catRuleEmpty(&rows[2]) || rows[2].IsDefault {
		t.Fatal("只写了 + 系列值不是兜底")
	}
	stephen := &TmdbMedia{MediaType: "movie", Title: "功夫", Cast: []string{"周星驰 / Stephen Chow", "元秋"}}
	late := &TmdbMedia{MediaType: "movie", Title: "某片", Cast: []string{"a", "b", "c", "d", "e", "f", "周星驰"}}
	mi := &TmdbMedia{MediaType: "movie", Title: "碟中谍7", GenreIDs: []int{28}}
	action := &TmdbMedia{MediaType: "movie", Title: "某动作片", GenreIDs: []int{28}}
	tf := &TmdbMedia{MediaType: "movie", Title: "变形金刚4"}
	for _, c := range []struct {
		m    *TmdbMedia
		want string
	}{
		{stephen, "电影/周星驰/周星驰系列"},
		{late, "电影/未分类"}, // 第 7 位的演员不算；后面两条都没命中，也没有兜底
		{mi, "电影/动作/碟中谍系列"},
		{action, "电影/动作"}, // 混合规则：系列没命中照样进主分类
		{tf, "电影/未分类"},   // + 的系列值不生成目录，纯系列规则也就不成立
	} {
		if got := catTry(rows, c.m).Dest; got != c.want {
			t.Errorf("%s：进了 %s，要 %s", c.m.Title, got, c.want)
		}
	}
	// 已经以「系列」结尾的不重复加；分类名为空只有系列目录
	if got := catRuleDir(&model.CategoryRule{Name: ""}, "漫威系列"); got != "漫威系列" {
		t.Errorf("系列目录：%s", got)
	}
	if got := catSeriesDir("a/b"); got != "a b系列" {
		t.Errorf("系列名清洗：%s", got)
	}
	// 片目层：系列目录也是分类，下一层才是片目
	l := buildLibCategoryLayout(rows)
	key, title, cat, _, ok := l.titleOf("库/电影/动作/碟中谍系列/碟中谍7 (2023)/碟中谍7.strm")
	if !ok || title != "碟中谍7 (2023)" || cat != "电影/动作/碟中谍系列" || key != "库/电影/动作/碟中谍系列/碟中谍7 (2023)" {
		t.Errorf("系列目录下的片目：%v %q %q %q", ok, key, title, cat)
	}
	if _, title, _, _, _ := l.titleOf("库/电影/动作/某动作片 (2020)/x.strm"); title != "某动作片 (2020)" {
		t.Errorf("分类下直接放的片目：%q", title)
	}
	// 没写系列字段的分类下面，「X系列」照旧是一部片的目录
	l2 := buildLibCategoryLayout(catRuleOf(t, "movie:\n  电影/合集:\n"))
	if _, title, _, _, _ := l2.titleOf("库/电影/合集/某某系列/x.strm"); title != "某某系列" {
		t.Errorf("普通分类下的「X系列」：%q", title)
	}
	// 洗版策略写分类本身，覆盖下面的系列目录
	if !containsCategory("动作", "电影/动作/碟中谍系列") || containsCategory("电影", "电影/动作") {
		t.Error("containsCategory 系列目录")
	}
}

// 解析 → 写回：可选标记与新字段原样带上
func TestCategoryYAMLRoundTripNewFields(t *testing.T) {
	src := "movie:\n  电影/A:\n    ?genre_ids: '16'\n    ？keywords: 'x，y'\n    series_actors: '某人'\n  电影/B:\n"
	rows := catRuleOf(t, src)
	if rows[0].Optional != "genre_ids,keywords" || rows[0].Keywords != "x，y" || rows[0].SeriesActors != "某人" || !rows[1].IsDefault {
		t.Fatalf("解析：%+v", rows)
	}
	out := renderCategoryYAML(rows)
	if !strings.Contains(out, "?genre_ids") || !strings.Contains(out, "?keywords") || !strings.Contains(out, "series_actors") {
		t.Fatalf("写回：\n%s", out)
	}
	if !catSameRules(rows, catRuleOf(t, out)) {
		t.Fatalf("写回后不一致：\n%s", out)
	}
}

func TestCatValidateYAML(t *testing.T) {
	bad := []struct{ src, want string }{
		{"movie:\n  a:\n    vote_average: '8'\n", "不是分类条件"},
		{"movie:\n  a:\n    year: '2020'\n", "release_year"},
		{"movie:\n  a:\n    genre_ids:\n", "没有值"},
		{"movie:\n  a:\n    release_year: '20'\n", "四位年份"},
		{"movie:\n  a:\n    genre_ids: '+'\n", "后面要跟值"},
		{"movie:\n  a:\n    genre_ids: '16'\n    ?genre_ids: '12'\n", "写了两次"},
		{"movie:\n  a:\n    custom_regex: '('\n", "正则"},
	}
	for _, c := range bad {
		errs := catValidateYAML(c.src)
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%q：%v，要包含 %q", c.src, errs, c.want)
		}
	}
	ok := "movie:\n  a:\n    genre_ids: [16, 99]\n    origin_country: !CN\n    library: x\n    release_year: '1990-1999,!1995'\n  b:\ntv:\n  c:\n    ?production_countries: 'US'\n"
	if errs := catValidateYAML(ok); len(errs) > 0 {
		t.Errorf("合法写法被拦：%v", errs)
	}
}

func TestCatMoveFallbacks(t *testing.T) {
	src := "movie:\n  # 兜底\n  电影/其他:\n  电影/动画:\n    genre_ids: '16'\n  电影/外语:\n    library: x\n  电影/华语:\n    original_language: zh\ntv:\n  剧集/A:\n    genre_ids: '16'\n  剧集/其他:\n"
	out, moved := catMoveFallbacks(src)
	if !moved {
		t.Fatal("应该挪")
	}
	rows := catRuleOf(t, out)
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "电影/动画,电影/华语,电影/其他,电影/外语,剧集/A,剧集/其他" {
		t.Fatalf("顺序：%s\n%s", got, out)
	}
	if !strings.Contains(out, "# 兜底") {
		t.Errorf("注释丢了：\n%s", out)
	}
	if _, moved := catMoveFallbacks("movie:\n  a:\n    genre_ids: '1'\n  b:\n"); moved {
		t.Error("兜底已经在最后不该动")
	}
}

func TestParseTmdbClassExtra(t *testing.T) {
	body := `{"keywords":{"results":[{"name":"anime"}]},"created_by":[{"name":"主创"}],
	"credits":{"cast":[{"name":"周星驰","original_name":"Stephen Chow"}],"crew":[{"name":"导演甲","job":"Director"},{"name":"编剧","job":"Writer"}]},
	"alternative_titles":{"titles":[{"title":"别名"}]}}`
	x := parseTmdbClassExtra([]byte(body))
	if strings.Join(x.Keywords, ",") != "anime" || x.Cast[0] != "周星驰 / Stephen Chow" ||
		strings.Join(x.Directors, ",") != "主创,导演甲" || strings.Join(x.Names, ",") != "别名" {
		t.Fatalf("%+v", x)
	}
}
