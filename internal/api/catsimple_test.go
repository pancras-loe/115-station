package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 前端 defaultRules.ts 里的默认模板（带注释）
const catDefaultYAMLForTest = `# 配置电影的分类策略
movie:
  # 动画
  电影/动画电影:
    genre_ids: '16'
  电影/华语电影:
    original_language: 'zh,cn,bo,za'
  # 未匹配以上条件时，返回最后一个
  电影/外语电影:

tv:
  电视剧/国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  电视剧/日番:
    genre_ids: '16'
    origin_country: 'JP'
  电视剧/国产剧:
    origin_country: 'CN,TW,HK'
  电视剧/未分类:
`

func TestCatSimpleOfDefault(t *testing.T) {
	f, why := catSimpleOf(catDefaultYAMLForTest)
	if why != nil {
		t.Fatalf("默认模板应该认得回来: %v", why)
	}
	if len(f.Movie) != 2 || f.Fallback.Movie != "电影/外语电影" {
		t.Fatalf("电影: %+v / %q", f.Movie, f.Fallback.Movie)
	}
	if f.Movie[1].OriginalLanguage != "zh,cn,bo,za" {
		t.Fatalf("原语言没认回来: %+v", f.Movie[1])
	}
	if len(f.TV) != 3 || f.Fallback.TV != "电视剧/未分类" || f.TV[0].OriginCountry != "CN,TW,HK" || f.TV[0].GenreIds != "16" {
		t.Fatalf("剧集: %+v / %q", f.TV, f.Fallback.TV)
	}
	if !yamlHasComments(catDefaultYAMLForTest) {
		t.Fatal("默认模板有注释")
	}
	// 表单 → YAML → 表单，一模一样；生成的 YAML 没有注释
	src := f.yaml()
	if yamlHasComments(src) {
		t.Fatalf("生成的 YAML 不该有注释:\n%s", src)
	}
	g, why := catSimpleOf(src)
	if why != nil || !reflect.DeepEqual(f, g) {
		t.Fatalf("来回转对不上: %v\n%+v\n%+v\n%s", why, f, g, src)
	}
	// 分类结果也一样
	a, _ := parseCategoryYAML(catDefaultYAMLForTest)
	b, _ := parseCategoryYAML(src)
	if !catSameRules(a, b) {
		t.Fatalf("生成的规则和原来分得不一样:\n%s", src)
	}
}

// 没写兜底：整理放「X/未分类」，认回来时写明它，分类结果不变
func TestCatSimpleOfNoFallback(t *testing.T) {
	src := "movie:\n  电影/华语:\n    origin_country: CN\n"
	f, why := catSimpleOf(src)
	if why != nil {
		t.Fatal(why)
	}
	if f.Fallback.Movie != "电影/未分类" || f.Fallback.TV != "剧集/未分类" || len(f.TV) != 0 {
		t.Fatalf("%+v", f)
	}
	rows, _ := parseCategoryYAML(f.yaml())
	media := &TmdbMedia{MediaType: "movie", OrigCountry: []string{"US"}}
	if got := catTry(rows, media).Dest; got != "电影/未分类" {
		t.Fatalf("没兜底时应该进 电影/未分类，得到 %q", got)
	}
}

// 中间没写条件的规则原样留在清单里，不当兜底挪走
func TestCatSimpleOfMiddleCatchAll(t *testing.T) {
	src := "movie:\n  电影/全部:\n  电影/华语:\n    origin_country: CN\n  电影/其他:\n"
	f, why := catSimpleOf(src)
	if why != nil {
		t.Fatal(why)
	}
	if len(f.Movie) != 2 || f.Movie[0].Name != "电影/全部" || !f.Movie[0].noCond() || f.Fallback.Movie != "电影/其他" {
		t.Fatalf("%+v", f)
	}
}

func TestCatSimpleOfRefuses(t *testing.T) {
	cases := map[string]string{
		"认不出的条件": "movie:\n  电影/高分:\n    vote_average: '8'\n  电影/其他:\n",
		"条件写成映射": "movie:\n  电影/高分:\n    genre_ids:\n      a: 1\n",
		"顶层多了键":  "movie:\n  电影:\nanime:\n  动漫:\n",
		"分类后面写值": "movie:\n  电影/华语: CN\n",
		"格式错误":   "movie:\n  - a\n b: [",
	}
	for name, src := range cases {
		if f, why := catSimpleOf(src); f != nil || len(why) == 0 {
			t.Errorf("%s：应该认不回来，得到 %+v", name, f)
		}
	}
	// 列表写法、排除写法认得回来（原样保留成逗号串）
	f, why := catSimpleOf("tv:\n  纪录片:\n    genre_ids: [99]\n  国产:\n    origin_country: 'CN,!HK'\n  其他:\n")
	if why != nil || f.TV[0].GenreIds != "99" || f.TV[1].OriginCountry != "CN,!HK" {
		t.Fatalf("%v %+v", why, f)
	}
}

func TestCatSimpleValidate(t *testing.T) {
	ok := catSimpleForm{Movie: []catSimpleRule{{Name: "电影/华语", OriginCountry: "CN"}}, TV: []catSimpleRule{}}
	ok.Fallback.Movie, ok.Fallback.TV = "电影/其他", "剧集"
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	dup := ok
	dup.Movie = []catSimpleRule{{Name: "电影/华语", OriginCountry: "CN"}, {Name: "电影/华语/", GenreIds: "16"}}
	if err := dup.validate(); err == nil || !strings.Contains(err.Error(), "同一个目录") {
		t.Fatalf("重名应该拦下: %v", err)
	}
	empty := ok
	empty.Movie = []catSimpleRule{{Name: "  ", GenreIds: "16"}}
	if err := empty.validate(); err == nil {
		t.Fatal("空目录名应该拦下")
	}
	noFb := ok
	noFb.Fallback.TV = ""
	if err := noFb.validate(); err == nil {
		t.Fatal("兜底没名字应该拦下")
	}
	fbDup := ok
	fbDup.Fallback.Movie = "电影/华语"
	if err := fbDup.validate(); err == nil {
		t.Fatal("兜底和清单重名应该拦下")
	}
}

func TestCatTry(t *testing.T) {
	rows, _ := parseCategoryYAML(catDefaultYAMLForTest)
	frieren := &TmdbMedia{MediaType: "tv", GenreIDs: []int{16, 10759}, OrigCountry: []string{"JP"}, OrigLanguage: "ja", Year: "2023"}
	res := catTry(rows, frieren)
	if res.Dest != "电视剧/日番" || res.Index != 1 || len(res.Skips) != 1 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Skips[0].Reason, "地区不符") {
		t.Fatalf("跳过国漫的原因应该是地区: %q", res.Skips[0].Reason)
	}
	breaking := &TmdbMedia{MediaType: "tv", GenreIDs: []int{18}, OrigCountry: []string{"US"}}
	res = catTry(rows, breaking)
	if res.Dest != "电视剧/未分类" || res.Index != -1 || len(res.Skips) != 3 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Skips[0].Reason, "类型不符") {
		t.Fatalf("%q", res.Skips[0].Reason)
	}
	// TMDB 上没写原语言
	noLang := &TmdbMedia{MediaType: "movie"}
	res = catTry(rows, noLang)
	if res.Dest != "电影/外语电影" || !strings.Contains(res.Skips[1].Reason, "没写") {
		t.Fatalf("%+v", res)
	}
	// 试一试和整理同一个口径
	for _, m := range []*TmdbMedia{frieren, breaking, noLang} {
		var mt []model.CategoryRule
		for _, r := range rows {
			if r.MediaType == m.MediaType {
				mt = append(mt, r)
			}
		}
		want := ""
		for i := range mt {
			if matchCategory(&mt[i], m) {
				want = libSubPath(mt[i].Name)
				break
			}
		}
		if got := catTry(rows, m).Dest; got != want {
			t.Fatalf("试一试 %q 和整理 %q 不一致", got, want)
		}
	}
}

func TestCatDirChanges(t *testing.T) {
	oldRows, _ := parseCategoryYAML(catDefaultYAMLForTest)
	newRows, _ := parseCategoryYAML(strings.Replace(catDefaultYAMLForTest, "电视剧/日番", "动漫/日番", 1))
	titles := func(mt, cat string) int {
		if mt == "tv" && cat == "电视剧/日番" {
			return 7
		}
		return 0
	}
	got := catDirChanges(oldRows, newRows, titles)
	if len(got) != 1 || got[0].Name != "电视剧/日番" || got[0].Titles != 7 {
		t.Fatalf("%+v", got)
	}
	// 原目录里没片的不打扰
	if got := catDirChanges(oldRows, newRows, func(string, string) int { return 0 }); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// 只改条件不改名：不报
	same, _ := parseCategoryYAML(strings.Replace(catDefaultYAMLForTest, "'JP'", "'JP,KR'", 1))
	if got := catDirChanges(oldRows, same, titles); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// 简单模式走 HTTP：存表单 → 方案的 YAML 换成生成的那份 → 读回来还是这张表；规则行同步生效
func TestCatSimpleHTTP(t *testing.T) {
	_, r, _ := mediaLibsSetup(t)
	id := primarySchemeID()
	form := `{"movie":[{"name":"电影/动画","genre_ids":"16"}],"tv":[{"name":"剧集/日番","genre_ids":"16","origin_country":"JP"}],"fallback":{"movie":"电影/其他","tv":"剧集/其他"}}`
	code, body := libHTTPDo(t, r, "PUT", "/category/schemes/"+itoa(id), `{"simple":`+form+`}`)
	if code != http.StatusOK {
		t.Fatalf("保存：%d %v", code, body)
	}
	forgetSchemes()
	s, _ := schemeByID(id)
	if !strings.Contains(s.Config, "剧集/日番") || strings.Contains(s.Config, "#") {
		t.Fatalf("YAML 没换成生成的那份：\n%s", s.Config)
	}
	if rs := classifyRulesOf(id, "tv"); len(rs) != 2 || rs[1].Name != "剧集/其他" || !rs[1].IsDefault {
		t.Fatalf("规则行没跟上：%+v", rs)
	}
	code, body = libHTTPDo(t, r, "GET", "/category/schemes/"+itoa(id), "")
	if code != http.StatusOK || body["simple"] == nil || body["empty"] != false {
		t.Fatalf("读回：%d %v", code, body)
	}
	// 重名：400，原来的不动
	bad := `{"movie":[{"name":"电影/其他","genre_ids":"16"}],"tv":[],"fallback":{"movie":"电影/其他","tv":"剧集"}}`
	if code, body = libHTTPDo(t, r, "PUT", "/category/schemes/"+itoa(id), `{"simple":`+bad+`}`); code != http.StatusBadRequest {
		t.Fatalf("重名要拒绝：%d %v", code, body)
	}
	// 专家模式写了认不出的条件：保存时就拦下（catValidateYAML）
	if code, body = libHTTPDo(t, r, "PUT", "/category/schemes/"+itoa(id), `{"yaml":"movie:\n  电影/高分:\n    vote_average: '8'\n  电影:\n"}`); code != http.StatusBadRequest {
		t.Fatalf("认不出的条件要拒绝：%d %v", code, body)
	}
	// 可选条件简单模式表示不了：能存，读回来 simple 为空、带原因；写在前面的兜底挪到最后
	if code, body = libHTTPDo(t, r, "PUT", "/category/schemes/"+itoa(id), `{"yaml":"movie:\n  电影:\n  电影/动画:\n    ?genre_ids: '16'\n    ?keywords: '动画'\n"}`); code != http.StatusOK || body["moved_fallback"] != true {
		t.Fatalf("专家模式保存：%d %v", code, body)
	}
	forgetSchemes()
	_, body = libHTTPDo(t, r, "GET", "/category/schemes/"+itoa(id), "")
	if body["simple"] != nil || len(body["simple_why"].([]any)) == 0 {
		t.Fatalf("可选条件应该挡住简单模式：%v", body)
	}
	if rs := classifyRulesOf(id, "movie"); len(rs) != 2 || rs[0].Name != "电影/动画" || rs[0].Optional != "genre_ids,keywords" || !rs[1].IsDefault {
		t.Fatalf("兜底没挪到最后 / 可选没记下：%+v", rs)
	}
}
