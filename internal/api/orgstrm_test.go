package api

import (
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

func newTestDB(t *testing.T, name string) {
	t.Helper()
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	db, err := model.InitDB(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
}

// 整理直接落盘的 STRM 路径必须和全量/增量同步算出来的完全一致，
// 否则同一部片会在本地出现两棵目录树（库名前缀漏掉是最容易犯的错）。
// 这里用 scanLedgerTitles 的 key 做交叉验证：刮削按它定位片目目录
func TestOrgSinkPathMatchesLedgerKey(t *testing.T) {
	newTestDB(t, "orgsink.db")
	s := &orgSink{libName: "媒体库", jobs: map[string]scrapeJob{}}

	// 剧集：视频落季目录，刮削目标是标题目录
	rootRel := "剧集/国产剧/C-测试剧集-2025-[tmdb=456]"
	mediaRel := rootRel + "/Season 01"
	wantVideo := "媒体库/剧集/国产剧/C-测试剧集-2025-[tmdb=456]/Season 01"
	if got := s.libRel(mediaRel); got != wantVideo {
		t.Fatalf("视频落点不符: %s，预期 %s", got, wantVideo)
	}

	// 台账里按这个 RelPath 落行时，scanLedgerTitles 应聚合出同一个 key
	relPath := wantVideo + "/测试剧集 - S01E01.mkv.strm"
	if err := model.DB.Create(&model.SyncedFile{FileID: "ep1", Kind: "video", RelPath: relPath}).Error; err != nil {
		t.Fatal(err)
	}
	entries := scanLedgerTitles()
	if _, ok := entries[s.libRel(rootRel)]; !ok {
		t.Fatalf("刮削 key %q 在台账聚合结果里找不到: %+v", s.libRel(rootRel), entries)
	}
}

// 库名取不到时（纯 OpenAPI 通道无 Cookie）路径不该多出一个前导斜杠或空段
func TestOrgSinkLibRelWithoutLibName(t *testing.T) {
	s := &orgSink{}
	if got := s.libRel("电影/动作/X-片名-2024"); got != "电影/动作/X-片名-2024" {
		t.Fatalf("空库名时路径不符: %q", got)
	}
}

// NFO 与标准封面进标题目录，字幕跟视频进季目录——分流口径要和 classifyFile 一致
func TestIsTitleLevelAsset(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"tvshow.nfo", true},
		{"poster.jpg", true},
		{"测试剧集 - S01E01.chs.ass", false},
		{"测试剧集 - S01E01.srt", false},
	} {
		if got := isTitleLevelAsset(c.name); got != c.want {
			t.Errorf("%s: 得到 %v，预期 %v", c.name, got, c.want)
		}
	}
}

// 记录里的文件清单要能原样往返：重新整理完全依赖它定位 fid
func TestRecordFilesRoundTrip(t *testing.T) {
	in := []orgRecordFile{
		{Fid: "111", Name: "测试剧集 - S01E01.mkv", Kind: "video", PickCode: "abc", Size: 123, Sha1: "d1"},
		{Fid: "222", Name: "测试剧集 - S01E01.srt", Kind: "subtitle"},
	}
	out := unmarshalRecordFiles(marshalRecordFiles(in))
	if len(out) != len(in) {
		t.Fatalf("条目数不符: %d，预期 %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("第 %d 条不符: %+v，预期 %+v", i, out[i], in[i])
		}
	}
	if got := unmarshalRecordFiles(""); got != nil {
		t.Errorf("空串应得到 nil，实际 %+v", got)
	}
	if got := unmarshalRecordFiles("不是 json"); got != nil {
		t.Errorf("坏数据应得到 nil，实际 %+v", got)
	}
}

func TestRecordFileKind(t *testing.T) {
	for name, want := range map[string]string{
		"片名.mkv":     "video",
		"片名.srt":     "subtitle",
		"movie.nfo":  "meta",
		"poster.jpg": "meta",
		"说明.txt":     "junk",
	} {
		if got := recordFileKind(name); got != want {
			t.Errorf("%s: 得到 %s，预期 %s", name, got, want)
		}
	}
}

// 库名解析必须是懒的：定时整理绝大多数轮次待整理目录是空的，
// 为「没活干」先打一次 115 取库名是纯浪费。只有真要落盘（libRel）时才解析，
// 且只解析一次
func TestOrgSinkLibNameLazy(t *testing.T) {
	s := &orgSink{libCid: "123"} // h 为 nil：resolveLibName 里有守卫，不会解引用
	if s.libResolved {
		t.Fatal("刚构造出来不该已解析库名")
	}

	// 第一次 libRel 触发解析；h 为 nil 取不到库名，路径退化为不带前缀
	if got := s.libRel("电影/动作/片名"); got != "电影/动作/片名" {
		t.Fatalf("取不到库名时不该多出前导斜杠或空段: %q", got)
	}
	if !s.libResolved {
		t.Fatal("libRel 之后应标记为已解析")
	}

	// 已解析就不再重来：手工塞个库名，再调一次应该直接用它
	s.libName = "影视"
	if got := s.libRel("电影/动作/片名"); got != "影视/电影/动作/片名" {
		t.Fatalf("已解析后应直接复用库名，得到 %q", got)
	}
}
