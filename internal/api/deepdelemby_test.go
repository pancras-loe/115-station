package api

import (
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

// 2026-09-20 从真实 Emby 采到的 deep.delete 载荷里的 Description 原文
// （神医助手 - 媒体深度删除）。用真载荷而不是手编的样例：段标题、空行位置、
// Mount Paths 的 URL 形态都是实测形状，手编容易编出一个「我以为」的格式
const deepDelSampleDesc = "Item Name:\n海洋奇缘：启航\n\nItem Path:\n" +
	"/media/影视/电影/海洋奇缘：启航.2026.{tmdbid=1108427}/海洋奇缘：启航.Moana.2026.WEB-DL.DV HDR.2160p.H265.DDP.5.1.ATMOS.mkv.strm\n\n" +
	"Mount Paths:\nhttp://192.168.31.35:6086/d/bifjp50n4pazy83of.mkv?/海洋奇缘：启航.Moana.2026.WEB-DL.DV HDR.2160p.H265.DDP.5.1.ATMOS.mkv"

func TestParseEmbyItemPathsRealPayload(t *testing.T) {
	got := parseEmbyItemPaths(deepDelSampleDesc)
	want := "/media/影视/电影/海洋奇缘：启航.2026.{tmdbid=1108427}/海洋奇缘：启航.Moana.2026.WEB-DL.DV HDR.2160p.H265.DDP.5.1.ATMOS.mkv.strm"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Item Path 解析不对: %#v", got)
	}
	// Item Name 段里的片名不能被当成路径收进来
	for _, p := range got {
		if p == "海洋奇缘：启航" {
			t.Fatal("把 Item Name 当成路径收进来了")
		}
	}
}

func TestParseEmbyMountPathsAndPickcode(t *testing.T) {
	urls := parseEmbyMountPaths(deepDelSampleDesc)
	if len(urls) != 1 {
		t.Fatalf("Mount Paths 解析不对: %#v", urls)
	}
	if pc := pickcodeFromStrmURL(urls[0]); pc != "bifjp50n4pazy83of" {
		t.Fatalf("pickcode 抠错了: %q", pc)
	}
}

// 多版本删除时 Item Path 段下有多行
func TestParseEmbyItemPathsMultiVersion(t *testing.T) {
	desc := "Item Name:\n某片\n\nItem Path:\n/media/影视/电影/X/a.mkv.strm\n/media/影视/电影/X/b.mkv.strm\n\nMount Paths:\nhttp://h/d/pc1.mkv\nhttp://h/d/pc2.mkv"
	paths := parseEmbyItemPaths(desc)
	if len(paths) != 2 || paths[1] != "/media/影视/电影/X/b.mkv.strm" {
		t.Fatalf("多版本 Item Path 解析不对: %#v", paths)
	}
	urls := parseEmbyMountPaths(desc)
	if len(urls) != 2 {
		t.Fatalf("多版本 Mount Paths 解析不对: %#v", urls)
	}
}

func TestPickcodeFromStrmURL(t *testing.T) {
	cases := map[string]string{
		"http://h:6086/d/abc123.mkv?/名字.mkv": "abc123",
		"http://h:6086/d/abc123":             "abc123",
		"http://h:6086/d/abc123/名字.mkv":      "abc123",
		// 旧版 STRM 用纯数字 fid 生成，那不是 pickcode，拿去查 pick_code 只会错配
		"http://h:6086/d/123456/名字.mkv": "",
		"/media/影视/电影/x.mkv.strm":       "",
		"":                              "",
	}
	for in, want := range cases {
		if got := pickcodeFromStrmURL(in); got != want {
			t.Fatalf("pickcodeFromStrmURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestRelPathFromLocal(t *testing.T) {
	cases := []struct{ root, local, want string }{
		{"/media", "/media/影视/电影/A/a.mkv.strm", "影视/电影/A/a.mkv.strm"},
		{"/media/", "/media/影视/剧集/B", "影视/剧集/B"},
		{"/media", "/media", ""},       // 根自己不是相对路径
		{"/media", "/other/影视/a", ""},  // 库外路径绝不能拿去前缀匹配台账
		{"/media", "/mediaX/影视/a", ""}, // 前缀相同但不是同一个目录
		{"", "/media/影视/a", ""},
	}
	for _, c := range cases {
		if got := relPathFromLocal(c.root, c.local); got != c.want {
			t.Fatalf("relPathFromLocal(%q, %q) = %q，期望 %q", c.root, c.local, got, c.want)
		}
	}
}

func TestEmbyPathToLocal(t *testing.T) {
	// path_mapping 格式是「本地#Emby」，本地根实际以 full.local_path 为准
	deepDelEmbyTestDB(t, "/media", nil)
	got := embyPathToLocal("/media#/mnt/emby", "/mnt/emby/影视/电影/A/a.mkv.strm")
	if got != "/media/影视/电影/A/a.mkv.strm" {
		t.Fatalf("映射结果不对: %q", got)
	}
	// 没配映射 / 前缀对不上 → 原样返回，调用方按「就是本地路径」处理
	if got := embyPathToLocal("", "/media/影视/a.strm"); got != "/media/影视/a.strm" {
		t.Fatalf("空映射应原样返回，得到 %q", got)
	}
	if got := embyPathToLocal("/media#/mnt/emby", "/somewhere/else"); got != "/somewhere/else" {
		t.Fatalf("前缀不匹配应原样返回，得到 %q", got)
	}
}

// deepDelEmbyTestDB 内存库 + 一条 full 配置（localRoot）+ 台账行
func deepDelEmbyTestDB(t *testing.T, localRoot string, rows []model.SyncedFile) *Handler {
	t.Helper()
	if _, err := model.InitDB("file:deepdelemby_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM synced_files")
		model.DB.Exec("DELETE FROM settings")
		model.DB = nil
	})
	if err := model.DB.Create(&model.Setting{
		Key: "full", Value: `{"local_path":"` + filepath.ToSlash(localRoot) + `","deep_delete":{"enabled":true}}`,
	}).Error; err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
	return &Handler{DB: model.DB}
}

func markedCount(t *testing.T) int {
	t.Helper()
	var n int64
	model.DB.Model(&model.SyncedFile{}).Where("vanish_at IS NOT NULL").Count(&n)
	return int(n)
}

// 精确路径命中一条；本地文件还在的那条绝不能打标
func TestMarkVanishedByLocatorsExact(t *testing.T) {
	root := deepDelTestTree(t, "影视/电影/A/still-here.mkv.strm")
	h := deepDelEmbyTestDB(t, root, []model.SyncedFile{
		{FileID: "gone", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video", PickCode: "pc1"},
		{FileID: "here", RelPath: "影视/电影/A/still-here.mkv.strm", Kind: "video"},
	})

	if n := h.markVanishedByLocators([]string{"影视/电影/A/a.mkv.strm"}, nil); n != 1 {
		t.Fatalf("应当打标 1 条，得到 %d", n)
	}
	// 本地还在的那条即使被显式点名也不能打标
	if n := h.markVanishedByLocators([]string{"影视/电影/A/still-here.mkv.strm"}, nil); n != 0 {
		t.Fatalf("本地文件还在却打了标: %d", n)
	}
	if markedCount(t) != 1 {
		t.Fatalf("最终打标数不对: %d", markedCount(t))
	}
}

// 删整部剧：事件只给剧目录，要按前缀把整棵子树捞出来
func TestMarkVanishedByLocatorsPrefix(t *testing.T) {
	root := deepDelTestTree(t, "影视/剧集/B/S01/e2.mkv.strm")
	h := deepDelEmbyTestDB(t, root, []model.SyncedFile{
		{FileID: "e1", RelPath: "影视/剧集/B/S01/e1.mkv.strm", Kind: "video"},
		{FileID: "e2", RelPath: "影视/剧集/B/S01/e2.mkv.strm", Kind: "video"},
		{FileID: "nfo", RelPath: "影视/剧集/B/tvshow.nfo", Kind: "asset"},
		{FileID: "other", RelPath: "影视/剧集/BB/S01/x.mkv.strm", Kind: "video"},
	})

	n := h.markVanishedByLocators([]string{"影视/剧集/B"}, nil)
	// e1 与 tvshow.nfo 本地已没；e2 还在；BB 是另一部剧，前缀不能误伤
	if n != 2 {
		t.Fatalf("前缀命中数不对: %d", n)
	}
	var row model.SyncedFile
	model.DB.Where("file_id = ?", "other").First(&row)
	if row.VanishAt != nil {
		t.Fatal("「影视/剧集/BB」被「影视/剧集/B」的前缀误伤了")
	}
}

// pickcode 是路径之外的第二条线：路径映射配错时它仍然能命中
func TestMarkVanishedByLocatorsPickcode(t *testing.T) {
	root := deepDelTestTree(t, "影视/电影/A/keep.strm")
	h := deepDelEmbyTestDB(t, root, []model.SyncedFile{
		{FileID: "gone", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video", PickCode: "bifjp50n4pazy83of"},
	})

	if n := h.markVanishedByLocators(nil, []string{"bifjp50n4pazy83of"}); n != 1 {
		t.Fatalf("pickcode 应当命中 1 条，得到 %d", n)
	}
}

// 已判为失效 STRM 的行不重复打标（网盘那份本来就没了）
func TestMarkVanishedByLocatorsSkipsOrphan(t *testing.T) {
	root := deepDelTestTree(t, "影视/电影/A/keep.strm")
	now := timeRef()
	h := deepDelEmbyTestDB(t, root, []model.SyncedFile{
		{FileID: "orphan", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video", OrphanAt: &now},
	})

	if n := h.markVanishedByLocators([]string{"影视/电影/A/a.mkv.strm"}, nil); n != 0 {
		t.Fatalf("失效 STRM 不该被打标，得到 %d", n)
	}
}

// 删除事件去重：同一条目窗口内只放行一次，不同条目互不影响
func TestEmbyDeleteDuplicate(t *testing.T) {
	embyDeleteSeenMu.Lock()
	embyDeleteSeen = map[string]time.Time{}
	embyDeleteSeenMu.Unlock()

	if embyDeleteDuplicate("119421") {
		t.Fatal("首次不该判为重复")
	}
	if !embyDeleteDuplicate("119421") {
		t.Fatal("同一条目的第二条事件应当判为重复")
	}
	if embyDeleteDuplicate("108939") {
		t.Fatal("不同条目不该互相影响")
	}
	// 拿不到条目 id 时宁可重复通知，也不要把两次真实删除吃掉一次
	if embyDeleteDuplicate("") || embyDeleteDuplicate("") {
		t.Fatal("空 id 不该被去重")
	}
}

// timeRef 取一个可寻址的当前时刻（造数用）
func timeRef() time.Time { return time.Now() }
