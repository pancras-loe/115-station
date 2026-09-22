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

func TestApplyTemplateStringExpressions(t *testing.T) {
	media := &TmdbMedia{TmdbID: 1726, Title: "钢铁侠", OriginalTitle: "Iron.Man", Year: "2008", MediaType: "movie"}
	parsed := &ParsedName{Title: "钢铁侠", Year: "2008"}
	ctx := buildRenameContext(media, parsed, "钢铁侠.2008.DV.HDR.mkv")

	got := ctx.ApplyTemplate("{title}<.{en_title.replace('.', ' ')}><.{resource_effect.replace('.', ' ')}>.{en_title.lower()}.{en_title.upper()}{ext}")
	want := "钢铁侠.Iron Man.DV HDR.iron.man.IRON.MAN.mkv"
	if got != want {
		t.Fatalf("字符串变换结果 %q，预期 %q", got, want)
	}
}

func TestDefaultRenameConfig(t *testing.T) {
	cfg := defaultRenameConfig()
	if cfg.MovieFolder != "{title}.{year}<.[[tmdbid={tmdb_id}]]>" {
		t.Errorf("电影文件夹默认规则不正确: %q", cfg.MovieFolder)
	}
	if cfg.MovieFile != "{title}<.{en_title.replace('.', ' ')}>.{year}<.{resource_type}><.{resource_effect.replace('.', ' ')}><.{resource_pix}><.{video_encode}><.{audio_encode}>{ext}" {
		t.Errorf("电影文件默认规则不正确: %q", cfg.MovieFile)
	}
	if cfg.TVFolder != "{title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}" {
		t.Errorf("剧集文件夹默认规则不正确: %q", cfg.TVFolder)
	}
	if cfg.TVFile != "{title}<.{en_title.replace('.', ' ')}>.{season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}>{ext}" {
		t.Errorf("剧集文件默认规则不正确: %q", cfg.TVFile)
	}
}

// 分类目录：分类名就是库内目录名，写什么就是什么；为空才退回媒体类型默认目录
func TestCategoryDir(t *testing.T) {
	for _, c := range []struct {
		mediaType, category, want string
	}{
		{"tv", "动漫番剧", "动漫番剧"},       // 平铺写法：不再被叠成 剧集/动漫番剧
		{"tv", "电视剧/国产剧", "电视剧/国产剧"}, // 多级写法：原样保留
		{"tv", "剧集", "剧集"},
		{"tv", "", "剧集"}, // 不要分类子目录 → 媒体类型默认目录
		{"movie", "", "电影"},
		{"movie", " /电影/动画电影/ ", "电影/动画电影"},
		{"其它", "", "未分类"},
	} {
		if got := categoryDir(c.mediaType, c.category); got != c.want {
			t.Errorf("categoryDir(%q, %q) = %q，预期 %q", c.mediaType, c.category, got, c.want)
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
//	期望入库到 <分类目录>/大时代.1992.{tmdbid=20382}/Season 1/<文件名>
//
// 此前两处都错：[[ ]] 没转义（出 [[tmdbid=20382]]），分类名又被
// mediaTypeCategory 叠了一层（出 剧集/剧集/…、剧集/动漫番剧/…）
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

	// 分类写全路径
	if got := libSubPath(categoryDir("tv", "电视剧/国产剧"), pathDir(newPath)); got != "电视剧/国产剧/大时代.1992.{tmdbid=20382}/Season 1" {
		t.Errorf("带分类的目标目录不符: %q", got)
	}
	// 用户在 YAML 里把分类名写成了「剧集」——就是 剧集/，不叠 剧集/剧集
	if got := libSubPath(categoryDir("tv", "剧集"), pathDir(newPath)); got != "剧集/大时代.1992.{tmdbid=20382}/Season 1" {
		t.Errorf("分类名为 剧集 时不该叠出 剧集/剧集: %q", got)
	}
	// 平铺分类：tv 下并列写 动漫番剧，就落在 动漫番剧/ 而不是 剧集/动漫番剧/
	if got := libSubPath(categoryDir("tv", "动漫番剧"), pathDir(newPath)); got != "动漫番剧/大时代.1992.{tmdbid=20382}/Season 1" {
		t.Errorf("平铺分类不该被叠上一级目录: %q", got)
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

// 多级文件夹模板 + 缺年份：中间段不能留下 "钢铁侠." 这种尾巴
func TestSanitizePathTrimsSegments(t *testing.T) {
	for in, want := range map[string]string{
		"钢铁侠./Season 01/a.mkv": "钢铁侠/Season 01/a.mkv",
		"钢铁侠.2008//x.mkv":      "钢铁侠.2008/x.mkv",
		"  钢铁侠 -/Season 01/":   "钢铁侠/Season 01",
	} {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q，预期 %q", in, got, want)
		}
	}
}

// Season 目录只看文件夹段：片名里带 Season 的剧集也要补目录
func TestBuildNewNameSeasonDirFromFolderOnly(t *testing.T) {
	saved := renameTpl
	defer func() { renameTpl = saved }()
	renameTpl = &RenameConfig{
		TVFolder: "{title}.{year}",
		TVFile:   "{title}.{season_episode}{ext}",
	}

	media := &TmdbMedia{TmdbID: 1, Title: "Season of the Witch", Year: "2011", MediaType: "tv"}
	got := buildNewNameWithTemplate(media, &ParsedName{Season: 1, Episode: 2}, "x.mkv")
	want := "Season of the Witch.2011/Season 01/Season of the Witch.S01E02.mkv"
	if got != want {
		t.Fatalf("Season 目录补位结果 %q，预期 %q", got, want)
	}
}

// 模板自己安排了季目录（默认模板的季变量，或手写的字面量）就不再补一层
func TestBuildNewNameSeasonDirNotDuplicated(t *testing.T) {
	saved := renameTpl
	defer func() { renameTpl = saved }()

	for _, tc := range []struct {
		folder string
		want   string
	}{
		{defaultRenameConfig().TVFolder, "海贼王.1999.{tmdbid=2}/Season 1/海贼王.S01E02.mkv"},
		{"{title}/Season 01", "海贼王/Season 01/海贼王.S01E02.mkv"},
		{"{title}/第一季", "海贼王/第一季/海贼王.S01E02.mkv"},
	} {
		renameTpl = &RenameConfig{TVFolder: tc.folder, TVFile: "{title}.{season_episode}{ext}"}
		media := &TmdbMedia{TmdbID: 2, Title: "海贼王", Year: "1999", MediaType: "tv"}
		if got := buildNewNameWithTemplate(media, &ParsedName{Season: 1, Episode: 2}, "x.mkv"); got != tc.want {
			t.Errorf("模板 %q 结果 %q，预期 %q", tc.folder, got, tc.want)
		}
	}
}
