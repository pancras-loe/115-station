package api

import "testing"

// [[ ]] 是花括号的字面量转义。前端示例渲染（webui/src/utils/rename.ts）一直
// 实现着这条规则，后端却没有——用户照着前端示例配好模板，整理出来的却是
// 「片名.1992.[[tmdbid=20382]]」。两边必须一致
func TestApplyTemplateBraceEscape(t *testing.T) {
	media := &TmdbMedia{TmdbID: 20382, Title: "大时代", Year: "1992", MediaType: "tv"}
	parsed := &ParsedName{Title: "大时代", Year: "1992", Season: 1, Episode: 1, IsTV: true}
	ctx := buildRenameContext(media, parsed, "大时代.S01E01.1080p.mkv")

	for _, c := range []struct{ tpl, want string }{
		{"{title}.{year}<.[[tmdbid={tmdb_id}]]>", "大时代.1992.{tmdbid=20382}"},
		{"{title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}", "大时代.1992.{tmdbid=20382}/Season 1"},
		// 块内变量为空时整块丢弃，转义也跟着没了
		{"{title}<.[[team={resource_team}]]>", "大时代"},
		// 块外的转义同样生效
		{"[[{title}]]", "{大时代}"},
	} {
		if got := ctx.ApplyTemplate(c.tpl); got != c.want {
			t.Errorf("模板 %q：得到 %q，预期 %q", c.tpl, got, c.want)
		}
	}
}

// 方括号本身（不成对的单个 [ ]）是正常文件名字符，不该被动
func TestApplyTemplateSingleBracketKept(t *testing.T) {
	media := &TmdbMedia{TmdbID: 1726, Title: "钢铁侠", Year: "2008", MediaType: "movie"}
	parsed := &ParsedName{Title: "钢铁侠", Year: "2008"}
	ctx := buildRenameContext(media, parsed, "钢铁侠.2008.mkv")

	if got := ctx.ApplyTemplate("{title}-{year}-[tmdb={tmdb_id}]"); got != "钢铁侠-2008-[tmdb=1726]" {
		t.Errorf("单层方括号应原样保留，得到 %q", got)
	}
}

// 分类名归一化：前缀要剥到底，分类名本身就是一级分类名时视为「不要二级分类」
func TestNormalizeCategoryName(t *testing.T) {
	for in, want := range map[string]string{
		"国产剧":        "国产剧",
		"电视剧/国产剧":    "国产剧",
		"剧集/国产剧":     "国产剧",
		"电影/动画电影":    "动画电影",
		"movie/动画电影": "动画电影",
		"剧集/电视剧/国产剧": "国产剧", // 叠了两层前缀
		"剧集":         "",    // 用户的意思是不分二级，不是要个叫「剧集」的子目录
		"电影":         "",
		"tv":         "",
		"  剧集/国产剧  ": "国产剧",
		"/剧集/国产剧/":   "国产剧",
		"":           "",
	} {
		if got := normalizeCategoryName(in); got != want {
			t.Errorf("%q: 得到 %q，预期 %q", in, got, want)
		}
	}
}

// 二级分类为空时不能拼出 "剧集//片名"
func TestLibSubPath(t *testing.T) {
	for _, c := range []struct {
		parts []string
		want  string
	}{
		{[]string{"剧集", "国产剧", "大时代.1992"}, "剧集/国产剧/大时代.1992"},
		{[]string{"剧集", "", "大时代.1992"}, "剧集/大时代.1992"},
		{[]string{"剧集", "  ", "大时代.1992"}, "剧集/大时代.1992"},
		{[]string{"剧集/", "/国产剧/", "大时代.1992"}, "剧集/国产剧/大时代.1992"},
		{[]string{"", "", ""}, ""},
	} {
		if got := libSubPath(c.parts...); got != c.want {
			t.Errorf("%v: 得到 %q，预期 %q", c.parts, got, c.want)
		}
	}
}

// 用户实配模板的端到端校验：
//
//	文件夹规则 {title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}
//	期望入库到 剧集/<二级分类>/大时代.1992.{tmdbid=20382}/Season 1/<文件名>
//
// 此前两处都错：[[ ]] 没转义（出 [[tmdbid=20382]]），二级分类名叫「剧集」时
// 又被 mediaTypeCategory 叠了一层（出 剧集/剧集/…）
func TestUserTemplateEndToEnd(t *testing.T) {
	prev := renameTpl
	t.Cleanup(func() { renameTpl = prev })
	renameTpl = &RenameConfig{
		TVFolder: "{title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}",
		TVFile:   "{title} - {season_episode}{ext}",
	}

	media := &TmdbMedia{TmdbID: 20382, Title: "大时代", Year: "1992", MediaType: "tv"}
	parsed := parseFileName("大时代.S01E01.1080p.mkv")
	parsed.Season, parsed.Episode = 1, 1

	newPath := buildNewNameWithTemplate(media, parsed, "大时代.S01E01.1080p.mkv")
	wantPath := "大时代.1992.{tmdbid=20382}/Season 1/大时代 - S01E01.mkv"
	if newPath != wantPath {
		t.Fatalf("模板产出 %q，预期 %q", newPath, wantPath)
	}

	// 有二级分类
	if got := libSubPath(mediaTypeCategory("tv"), "国产剧", pathDir(newPath)); got != "剧集/国产剧/大时代.1992.{tmdbid=20382}/Season 1" {
		t.Errorf("带二级分类的目标目录不符: %q", got)
	}
	// 二级分类被归一化成空（用户在 YAML 里把分类名写成了「剧集」）
	if got := libSubPath(mediaTypeCategory("tv"), normalizeCategoryName("剧集"), pathDir(newPath)); got != "剧集/大时代.1992.{tmdbid=20382}/Season 1" {
		t.Errorf("无二级分类时不该叠出 剧集/剧集: %q", got)
	}
}

// 剧集日志按类型计数，不逐个列文件名
func TestFileKindSummary(t *testing.T) {
	files := []remoteFile{
		{Name: "大时代 - S01E01.mkv"}, {Name: "大时代 - S01E02.mkv"},
		{Name: "大时代 - S01E01.srt"},
		{Name: "tvshow.nfo"}, {Name: "poster.jpg"},
		{Name: "说明.txt"},
	}
	if got := fileKindSummary(files); got != "视频 2、字幕 1、NFO/封面 2、其他 1" {
		t.Errorf("汇总不符: %q", got)
	}
	if got := fileKindSummary(nil); got != "空" {
		t.Errorf("空清单应显示「空」，得到 %q", got)
	}
	// 记录侧口径必须与整理侧一致，否则同一次操作前后两条日志对不上
	rec := []orgRecordFile{{Name: "电影.mkv"}, {Name: "电影.srt"}}
	if recordKindSummary(rec) != fileKindSummary([]remoteFile{{Name: "电影.mkv"}, {Name: "电影.srt"}}) {
		t.Error("记录侧与整理侧的分类汇总口径不一致")
	}
}
