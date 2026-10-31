package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

func TestSharePlayID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"s~sws9e623h88~3537728615925941546", "s~sws9e623h88~3537728615925941546"},
		{"s~sws9e623h88~3537728615925941546.mkv", "s~sws9e623h88~3537728615925941546"}, // 保留文件后缀
		{"s~sws9e623h88~3537728615925941546/瞒天过海.mkv", "s~sws9e623h88~3537728615925941546"},
		{"s~AbC_d-12~1", "s~AbC_d-12~1"}, // 分享码不小写化
		{"s~sws9e623h88~abc", ""},        // fid 不是数字
		{"s~a~1", ""},                    // 分享码太短
		{"s~sws9e623h88", ""},
		{"s~../x~1", ""},
	}
	for _, c := range cases {
		if got := normalizePlaybackID(c.in); got != c.want {
			t.Errorf("normalizePlaybackID(%q) = %q，应为 %q", c.in, got, c.want)
		}
	}
	// 老标识照旧
	if got := normalizePlaybackID("ABCDEF123"); got != "abcdef123" {
		t.Errorf("pickcode 被改坏：%q", got)
	}
	if got := normalizePlaybackID("2~abcdef"); got != "2~abcdef" {
		t.Errorf("附加账号标识被改坏：%q", got)
	}
	// 分享直链不按 UA 分开缓存，自己网盘的照旧按 UA
	if playbackCacheKey("s~sws9e623h88~1", "Emby") != playbackCacheKey("s~sws9e623h88~1", "VLC") {
		t.Error("分享直链缓存不该按 UA 分开")
	}
	if playbackCacheKey("abc", "Emby") == playbackCacheKey("abc", "VLC") {
		t.Error("自己网盘的直链缓存要按 UA 分开")
	}
}

func TestShareStrmURL(t *testing.T) {
	if got := shareStrmURL("http://nas:6086/", "pick_code", false, "sws1", "12", "a.mkv"); got != "http://nas:6086/d/s~sws1~12" {
		t.Errorf("pick_code 格式：%s", got)
	}
	if got := shareStrmURL("http://nas:6086", "", true, "sws1", "12", "a#b.mkv"); got != "http://nas:6086/d/s~sws1~12.mkv?/a%23b.mkv" {
		t.Errorf("带文件名 + 保留后缀：%s", got)
	}
	if got := shareStrmURL("http://nas:6086", "pick_code", false, "sws1", "12", "x.iso"); got != "http://nas:6086/d/s~sws1~12.iso" {
		t.Errorf("ISO 一律带 .iso：%s", got)
	}
}

// 实测（2026-10-31）的几种失败形态
func TestParseShareDownurlErrors(t *testing.T) {
	cases := []struct {
		body string
		is   error
		gone string
	}{
		{`{"state":false,"errno":4100008,"error":"访问码错误"}`, errShareBadCode, ""},
		{`{"state":false,"errno":4100010,"error":"分享已取消"}`, errShareGone, "4"},
		{`{"state":false,"errno":4100018,"error":"链接已过期"}`, errShareGone, "7"},
		{`{"state":true,"errno":0,"error":"","data":[]}`, errShareFileGone, ""},
	}
	for _, c := range cases {
		_, err := parseShareDownurl([]byte(c.body))
		if !errors.Is(err, c.is) {
			t.Errorf("%s → %v，应是 %v", c.body, err, c.is)
		}
		if got := shareStateOfErrRaw(err); got != c.gone {
			t.Errorf("%s → share_state %q，应为 %q", c.body, got, c.gone)
		}
	}
	if _, err := parseShareDownurl([]byte(`{"state":false,"errno":50029,"error":"当前版本过低"}`)); err == nil || errors.Is(err, errShareGone) {
		t.Errorf("别的错误不能当成分享失效：%v", err)
	}
}

func TestShareRootProblem(t *testing.T) {
	libs := []model.Library{{Name: "电影", Local: "电影"}, {Name: "剧集", Local: "剧集/主号"}}
	for _, c := range []struct {
		root string
		bad  bool
	}{
		{"分享", false}, {"电影", true}, {"电影/分享", true}, {"剧集", true}, {"剧集/主号/x", true},
		{"剧集/副号", false}, {"../x", true}, {"/abs", true},
	} {
		if got := shareRootProblem(c.root, libs) != ""; got != c.bad {
			t.Errorf("shareRootProblem(%q) 有问题=%v，应为 %v", c.root, got, c.bad)
		}
	}
}

func TestPlanShareFiles(t *testing.T) {
	mb := int64(1 << 20)
	entries := []shareEntry{
		{ID: "d1", Name: "S-狩猎的妻子们 (2025)", IsDir: true},
		{ID: "d2", Name: "Season 01", Dir: "S-狩猎的妻子们 (2025)", IsDir: true},
		{ID: "1", Name: "E01.mkv", Dir: "S-狩猎的妻子们 (2025)/Season 01", Size: 900 * mb, Sha1: "AA"},
		{ID: "2", Name: "E01.mp4", Dir: "S-狩猎的妻子们 (2025)/Season 01", Size: 900 * mb, Sha1: "BB"},
		{ID: "3", Name: "E01.chs.ass", Dir: "S-狩猎的妻子们 (2025)/Season 01", Size: 30000},
		{ID: "4", Name: "sample.mkv", Dir: "S-狩猎的妻子们 (2025)/Season 01", Size: 10 * mb},
		{ID: "5", Name: "E02.mkv", Dir: "S-狩猎的妻子们 (2025)/Season 01", Size: 900 * mb, Sha1: "OWNED"},
		{ID: "6", Name: "readme.txt", Dir: "S-狩猎的妻子们 (2025)", Size: 10},
		{ID: "7", Name: "tvshow.nfo", Dir: "S-狩猎的妻子们 (2025)", Size: 2000},
	}
	plans, skip := planShareFiles(entries, sharePlanOpts{LocalDir: "狩猎的妻子们", MinSize: 50 * mb, Assets: true,
		Owned: func(s string) bool { return s == "OWNED" }})
	got := map[string]string{}
	for _, p := range plans {
		got[p.Entry.ID] = p.Kind + " " + p.Rel
	}
	want := map[string]string{
		"1": "video 狩猎的妻子们/Season 01/E01.strm",     // 根下唯一的目录被去掉
		"2": "video 狩猎的妻子们/Season 01/E01.mp4.strm", // 同基名退回旧写法
		"3": "asset 狩猎的妻子们/Season 01/E01.chs.ass",
		"7": "asset 狩猎的妻子们/tvshow.nfo",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("计划：\n%v\n应为\n%v", got, want)
	}
	if skip.Small != 1 || skip.Owned != 1 || skip.Other != 1 {
		t.Fatalf("跳过统计：%+v", skip)
	}
	// 关掉附属文件：只剩视频
	plans, _ = planShareFiles(entries, sharePlanOpts{LocalDir: "x", Assets: false})
	for _, p := range plans {
		if p.Kind != "video" {
			t.Fatalf("关了附属文件还有 %+v", p)
		}
	}
	// 单文件分享：根下就是文件，不去层
	plans, _ = planShareFiles([]shareEntry{{ID: "9", Name: "电影.2020.mkv", Size: 2000 * mb}}, sharePlanOpts{LocalDir: "电影.2020"})
	if len(plans) != 1 || plans[0].Rel != "电影.2020/电影.2020.strm" {
		t.Fatalf("单文件分享：%+v", plans)
	}
}

// 端到端：假分享 → 写 STRM 与附属文件 → 再跑一次（分享者改了名、少了一集）→ 删除分享
func TestShareStrmRunAndDelete(t *testing.T) {
	newTestDB(t, "sharestrm.db")
	db := model.DB
	root := t.TempDir()
	src := model.ShareSource{ShareCode: "sws9e623h88", ReceiveCode: "LGNB", LocalDir: "狩猎的妻子们", State: shareStatePending}
	db.Create(&src)
	mb := int64(1 << 20)
	entries := []shareEntry{
		{ID: "d1", Name: "狩猎的妻子们 (2025)", IsDir: true},
		{ID: "1", Name: "E01.mkv", Dir: "狩猎的妻子们 (2025)", Size: 900 * mb, Sha1: "aa"},
		{ID: "2", Name: "E02.mkv", Dir: "狩猎的妻子们 (2025)", Size: 900 * mb, Sha1: "bb"},
		{ID: "3", Name: "E01.chs.ass", Dir: "狩猎的妻子们 (2025)", Size: 6}, // 「字幕」6 字节
	}
	downloads := 0
	r := &shareStrmRun{
		db: db, src: &src, cfg: shareStrmCfg{Assets: true, MinSizeMB: 50}, root: root,
		domain: "http://nas:6086", format: "", cookie: "ck",
		walk: func(code, rc, ck string, max int) ([]shareEntry, string, bool, error) {
			if code != "sws9e623h88" || rc != "LGNB" {
				t.Fatalf("列分享用错了分享码 / 提取码：%s %s", code, rc)
			}
			return entries, "狩猎的妻子们 (2025)", false, nil
		},
		download: func(ck, code, rc, fid string) (shareDownInfo, error) {
			downloads++
			return shareDownInfo{URL: "https://cdn/x?t=1"}, nil
		},
		fetch: func(u string, max int64) ([]byte, error) { return []byte("字幕"), nil },
		stop:  func() bool { return false },
		set:   func(string, int, int, string) {},
		now:   time.Now,
	}
	res, canceled, err := r.run()
	if err != nil || canceled {
		t.Fatal(err, canceled)
	}
	if res.Videos != 2 || res.Written != 2 || res.Assets != 1 {
		t.Fatalf("第一次：%+v", res)
	}
	strm := filepath.Join(root, "狩猎的妻子们", "E01.strm")
	b, _ := os.ReadFile(strm)
	if string(b) != "http://nas:6086/d/s~sws9e623h88~1?/E01.mkv" {
		t.Fatalf("STRM 内容：%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "狩猎的妻子们", "E01.chs.ass")); string(b) != "字幕" {
		t.Fatalf("字幕没下：%q", b)
	}
	var got model.ShareSource
	db.First(&got, src.ID)
	if got.State != shareStateOK || got.Files != 2 || got.Assets != 1 || got.Title != "狩猎的妻子们 (2025)" {
		t.Fatalf("分享状态：%+v", got)
	}

	// 第二次：内容没变的不重写；附属文件已在不再下；分享者把 E01 改了名、E02 没了
	entries[1].Name = "狩猎的妻子们.S01E01.mkv"
	entries = entries[:2:2]
	entries = append(entries, shareEntry{ID: "3", Name: "E01.chs.ass", Dir: "狩猎的妻子们 (2025)", Size: 7})
	before := downloads
	res, _, err = r.run()
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || res.Missing != 1 || res.Written != 1 {
		t.Fatalf("第二次：%+v", res)
	}
	if _, err := os.Stat(strm); !os.IsNotExist(err) {
		t.Fatal("改名后旧 STRM 应删掉")
	}
	if _, err := os.Stat(filepath.Join(root, "狩猎的妻子们", "E02.strm")); err != nil {
		t.Fatal("分享里没了的 E02 阶段 1 不删（要连续两轮确认）")
	}
	if downloads != before+1 {
		t.Fatalf("字幕大小变了才重下：多下了 %d 次", downloads-before)
	}

	// 分享失效：状态记 gone，不动本地
	r.walk = func(string, string, string, int) ([]shareEntry, string, bool, error) {
		return nil, "", false, &shareGoneErr{state: "7", reason: "链接已过期"}
	}
	if _, _, err := r.run(); !errors.Is(err, errShareGone) {
		t.Fatalf("失效分享：%v", err)
	}
	db.First(&got, src.ID)
	if got.State != shareStateGone || got.ShareState != "7" {
		t.Fatalf("失效后状态：%+v", got)
	}

	// 删除：本地全删、目录收掉、两张表清空；分享根留着
	n, err := deleteShareSource(db, root, got)
	if err != nil || n != 2 {
		t.Fatalf("删除：%d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "狩猎的妻子们")); !os.IsNotExist(err) {
		t.Fatal("分享目录应收掉")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("分享根不该删")
	}
	var cnt int64
	db.Model(&model.ShareFile{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("ShareFile 还剩 %d 行", cnt)
	}
}

// 删除只碰分享根下的文件：记录里的路径被改成 ../ 也不越界
func TestRemoveShareLocalStaysInRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "分享")
	os.MkdirAll(root, 0o755)
	outside := filepath.Join(base, "电影.strm")
	os.WriteFile(outside, []byte("x"), 0o644)
	if got := removeShareLocal(root, "../电影.strm"); got != "" {
		t.Fatalf("越界路径被处理了：%s", got)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("分享根外的文件被删了")
	}
	if !strings.HasPrefix(filepath.Clean(root), filepath.Clean(base)) {
		t.Fatal("测试前提不对")
	}
}

func TestShareStrmCfgAction(t *testing.T) {
	c := defaultShareStrmCfg()
	if c.linkAction() != "transfer" {
		t.Fatal("开关关着一律转存")
	}
	c.Enabled = true
	if c.linkAction() != "ask" {
		t.Fatal("默认每次问")
	}
	c.DefaultAction = "乱写"
	if c.normalized().DefaultAction != "ask" {
		t.Fatal("认不出的处理方式退回每次问")
	}
}
