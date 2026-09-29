package api

import (
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

// ledgerTestDB 临时库 + 指定的分类规则（替换 InitDB 播种的默认规则）+ 台账行
func ledgerTestDB(t *testing.T, rules []model.CategoryRule, files []model.SyncedFile) {
	t.Helper()
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	db, err := model.InitDB(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	seedCategoryRules(t, rules...)
	if len(files) > 0 {
		if err := db.Create(&files).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// seedCategoryRules 把分类规则表换成给定的这几条
func seedCategoryRules(t *testing.T, rules ...model.CategoryRule) {
	t.Helper()
	if err := model.DB.Where("1 = 1").Delete(&model.CategoryRule{}).Error; err != nil {
		t.Fatal(err)
	}
	if len(rules) > 0 {
		if err := model.DB.Create(&rules).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type wantTitle struct {
	key, title, year, kind string
	tmdb                   int
}

func checkLedgerTitles(t *testing.T, entries map[string]*ledgerTitleEntry, want []wantTitle) {
	t.Helper()
	if len(entries) != len(want) {
		t.Fatalf("应聚合为 %d 个片目，实际 %d: %+v", len(want), len(entries), entries)
	}
	for _, w := range want {
		got := entries[w.key]
		if got == nil {
			t.Errorf("缺少片目 %s", w.key)
			continue
		}
		if got.Title != w.title || got.Year != w.year || got.MediaType != w.kind || got.TmdbID != w.tmdb {
			t.Errorf("片目信息不符: %+v，预期 %+v", got, w)
		}
	}
}

// 多级分类（电影/动作）：标题目录在分类的下一层，剧集各季聚合为一部
func TestScanLedgerTitles(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "movie", Name: "电影/动作"},
		{MediaType: "tv", Name: "剧集/国产剧"},
	}, []model.SyncedFile{
		{FileID: "movie", Kind: "video", RelPath: "媒体库/电影/动作/Z-测试电影-2024-[tmdb=123]/电影.mkv.strm"},
		{FileID: "episode1", Kind: "video", RelPath: "媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]/Season 01/S01E01.mkv.strm"},
		{FileID: "episode2", Kind: "video", RelPath: "媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]/Season 02/S02E01.mkv.strm"},
		{FileID: "invalid", Kind: "video", RelPath: "电影.mkv"},
		{FileID: "other", Kind: "video", RelPath: "媒体库/其他/分类/未知/电影.mkv"},
		{FileID: "asset", Kind: "asset", RelPath: "媒体库/电影/动作/海报-2024-[tmdb=999]/poster.jpg"},
		// 视频直接放在分类目录里、没有标题目录：不是片目
		{FileID: "loose", Kind: "video", RelPath: "媒体库/电影/动作/散落.mkv.strm"},
	})
	checkLedgerTitles(t, scanLedgerTitles(), []wantTitle{
		{"媒体库/电影/动作/Z-测试电影-2024-[tmdb=123]", "测试电影", "2024", "movie", 123},
		{"媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]", "测试剧集", "2025", "tv", 456},
	})
}

// 平铺分类（二级分类 YAML 写 movie: 电影 / tv: 动漫番剧、综艺、剧集）：
// 此前写死成 库名/电影|剧集/分类/标题，电影的标题目录被当成分类、文件被当成标题，
// 动漫番剧 / 综艺整类被跳过，剧集的季目录被当成标题
func TestScanLedgerTitlesFlatCategories(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "movie", Name: "电影", IsDefault: true},
		{MediaType: "tv", Name: "动漫番剧", GenreIds: "16"},
		{MediaType: "tv", Name: "综艺", GenreIds: "10764,10767"},
		{MediaType: "tv", Name: "剧集", IsDefault: true},
	}, []model.SyncedFile{
		{FileID: "m", Kind: "video", RelPath: "影视/电影/流浪地球-2019-[tmdb=535167]/流浪地球.2019.2160p.mkv.strm"},
		{FileID: "a1", Kind: "video", RelPath: "影视/动漫番剧/葬送的芙莉莲-2023-[tmdb=209867]/Season 01/S01E01.mkv.strm"},
		{FileID: "a2", Kind: "video", RelPath: "影视/动漫番剧/葬送的芙莉莲-2023-[tmdb=209867]/Season 01/S01E02.mkv.strm"},
		{FileID: "v", Kind: "video", RelPath: "影视/综艺/奔跑吧-2014-[tmdb=61565]/Season 12/S12E01.mkv.strm"},
		{FileID: "s", Kind: "video", RelPath: "影视/剧集/狂飙-2023-[tmdb=207468]/Season 01/S01E01.mkv.strm"},
	})
	checkLedgerTitles(t, scanLedgerTitles(), []wantTitle{
		{"影视/电影/流浪地球-2019-[tmdb=535167]", "流浪地球", "2019", "movie", 535167},
		{"影视/动漫番剧/葬送的芙莉莲-2023-[tmdb=209867]", "葬送的芙莉莲", "2023", "tv", 209867},
		{"影视/综艺/奔跑吧-2014-[tmdb=61565]", "奔跑吧", "2014", "tv", 61565},
		{"影视/剧集/狂飙-2023-[tmdb=207468]", "狂飙", "2023", "tv", 207468},
	})
}

// 分类互相嵌套（电视剧 与 电视剧/日番 都是分类）取最长匹配：日番 不是一部片
func TestScanLedgerTitlesNestedCategories(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "movie", Name: "电影"},
		{MediaType: "tv", Name: "电视剧/日番", GenreIds: "16"},
		{MediaType: "tv", Name: "电视剧", IsDefault: true},
	}, []model.SyncedFile{
		{FileID: "j", Kind: "video", RelPath: "影视/电视剧/日番/孤独摇滚-2022-[tmdb=119100]/Season 01/S01E01.mkv.strm"},
		{FileID: "c", Kind: "video", RelPath: "影视/电视剧/繁花-2023-[tmdb=203055]/Season 01/S01E01.mkv.strm"},
	})
	checkLedgerTitles(t, scanLedgerTitles(), []wantTitle{
		{"影视/电视剧/日番/孤独摇滚-2022-[tmdb=119100]", "孤独摇滚", "2022", "tv", 119100},
		{"影视/电视剧/繁花-2023-[tmdb=203055]", "繁花", "2023", "tv", 203055},
	})
}

// 电影与剧集用了同名分类（纪录片）：类型按文件判断，有集号的是剧集
func TestScanLedgerTitlesSharedCategoryName(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "movie", Name: "纪录片", GenreIds: "99"},
		{MediaType: "tv", Name: "纪录片", GenreIds: "99"},
	}, []model.SyncedFile{
		{FileID: "m", Kind: "video", RelPath: "影视/纪录片/徒手攀岩-2018/徒手攀岩.2018.1080p.mkv.strm"},
		{FileID: "t1", Kind: "video", RelPath: "影视/纪录片/地球脉动-2006/Season 01/地球脉动.S01E01.mkv.strm"},
	})
	checkLedgerTitles(t, scanLedgerTitles(), []wantTitle{
		{"影视/纪录片/徒手攀岩-2018", "徒手攀岩", "2018", "movie", 0},
		{"影视/纪录片/地球脉动-2006", "地球脉动", "2006", "tv", 0},
	})
}

// 只认当前分类配置：改过策略后旧分类目录下的片不算片目；
// 某个媒体类型一条规则都没有时认整理的兜底目录（电影/未分类）；
// 不带库名前缀的老台账路径照样认
func TestScanLedgerTitlesLayoutEdges(t *testing.T) {
	ledgerTestDB(t, []model.CategoryRule{
		{MediaType: "tv", Name: "剧集", IsDefault: true},
	}, []model.SyncedFile{
		{FileID: "stale", Kind: "video", RelPath: "影视/电影/华语电影/老片-2001/老片.mkv.strm"},
		{FileID: "fallback", Kind: "video", RelPath: "影视/电影/未分类/某片-2020-[tmdb=1]/某片.mkv.strm"},
		{FileID: "short", Kind: "video", RelPath: "剧集/测试短路径-2023-[tmdb=789]/S01E01.mkv.strm"},
	})
	checkLedgerTitles(t, scanLedgerTitles(), []wantTitle{
		{"影视/电影/未分类/某片-2020-[tmdb=1]", "某片", "2020", "movie", 1},
		{"剧集/测试短路径-2023-[tmdb=789]", "测试短路径", "2023", "tv", 789},
	})
}

// 标题目录名 → 片名 / 年份 / 编号。默认重命名模板渲染出来的是 {tmdbid=…}，
// 此前只认 [tmdb=…]，用默认模板的库刮削时一个片目都找不到
func TestParseTitleDir(t *testing.T) {
	cases := []struct {
		dir, title, year string
		tmdb             int
	}{
		{"流浪地球.2019.{tmdbid=535167}", "流浪地球", "2019", 535167}, // 默认模板
		{"流浪地球.2019.[tmdbid=535167]", "流浪地球", "2019", 535167},
		{"流浪地球 (2019) [tmdbid=535167]", "流浪地球", "2019", 535167}, // Emby 风格
		{"流浪地球 (2019) {tmdb-535167}", "流浪地球", "2019", 535167},   // Plex 风格
		{"Z-重器-2026-[tmdb=291856]", "重器", "2026", 291856},       // 首字母分组的老模板
		{"三体.2023.{[tmdbid=204541;type=tv]}", "三体", "2023", 204541},
		{"星际穿越 (2014)", "星际穿越", "2014", 0},
		{"某片（2020）", "某片", "2020", 0},
		{"繁花", "繁花", "", 0},
		// 片名本身是年份：取最右侧那个当年份
		{"1917-2019", "1917", "2019", 0},
		{"2012.2009.{tmdbid=14161}", "2012", "2009", 14161},
		{"The Matrix.1999.{tmdbid=603}", "The Matrix", "1999", 603},
	}
	for _, c := range cases {
		title, year, tmdb := parseTitleDir(c.dir)
		if title != c.title || year != c.year || tmdb != c.tmdb {
			t.Errorf("parseTitleDir(%q) = (%q, %q, %d)，预期 (%q, %q, %d)", c.dir, title, year, tmdb, c.title, c.year, c.tmdb)
		}
	}
}
