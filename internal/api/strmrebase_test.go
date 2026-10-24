package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

func TestSplitStrmBase(t *testing.T) {
	for in, want := range map[string][2]string{
		"http://172.17.0.1:6086/d/abc.mkv?/某片.mkv": {"http://172.17.0.1:6086", "/d/abc.mkv?/某片.mkv"},
		"http://h:6086/d/abc":                      {"http://h:6086", "/d/abc"},
		"https://x.com/strm/d/2~abc.mkv":           {"https://x.com/strm", "/d/2~abc.mkv"}, // 域名带路径前缀、附加账号
		"http://h:6086/d/abc?/目录/d/某片.mkv":         {"http://h:6086", "/d/abc?/目录/d/某片.mkv"},
		"  http://h:6086/d/abc\r":                  {"http://h:6086", "/d/abc"},
	} {
		base, rest, ok := splitStrmBase(in)
		if !ok || base != want[0] || rest != want[1] {
			t.Errorf("splitStrmBase(%q) = %q %q %v，期望 %q %q", in, base, rest, ok, want[0], want[1])
		}
	}
	for _, in := range []string{
		"/mnt/115/电影/某片.mkv",                // 挂载路径
		"http://h:5244/p/电影/某片.mkv",         // OpenList
		"http://h:6086/d/",                  // 没有 pickcode
		"ftp://h/d/abc",                     // 不是 http
		"http:///d/abc",                     // 没有主机
		"http://h:9527/api/strm?pickcode=x", // 别的项目
	} {
		if _, _, ok := splitStrmBase(in); ok {
			t.Errorf("splitStrmBase(%q) 不该认成本站直链", in)
		}
	}
}

func TestRebaseStrms(t *testing.T) {
	const dir = "电影/某片"
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "1", RelPath: dir + "/回环.strm", Kind: "video"},
		{FileID: "2", RelPath: dir + "/回环2.strm", Kind: "video"},
		{FileID: "3", RelPath: dir + "/旧IP.strm", Kind: "video"},
		{FileID: "4", RelPath: dir + "/已是新的.strm", Kind: "video"},
		{FileID: "5", RelPath: dir + "/别家.strm", Kind: "video"},
		{FileID: "6", RelPath: dir + "/已丢.strm", Kind: "video"},
		{FileID: "7", RelPath: dir + "/某片.nfo", Kind: "asset"},
	})
	root := t.TempDir()
	local := filepath.Join(root, "电影", "某片")
	os.MkdirAll(local, 0o777)
	files := map[string]string{
		"回环.strm":   "http://172.17.0.1:6086/d/aaa.mkv?/回环.mkv",
		"回环2.strm":  "http://172.17.0.1:6086/d/bbb.mkv\n",
		"旧IP.strm":  "http://192.168.1.2:6086/d/ccc",
		"已是新的.strm": "http://nas.lan:6086/d/ddd",
		"别家.strm":   "/mnt/115/电影/某片.mkv",
		"某片.nfo":    "http://172.17.0.1:6086/d/aaa", // 附属文件不碰
	}
	old := time.Date(2025, 1, 2, 3, 4, 5, 0, time.Local)
	for name, body := range files {
		p := filepath.Join(local, name)
		os.WriteFile(p, []byte(body), 0o666)
		os.Chtimes(p, old, old)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(local, name))
		return string(b)
	}

	// 预演：只统计、不写
	rep, err := rebaseStrms(model.DB, root, "http://nas.lan:6086/", nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 6 || rep.Missing != 1 || rep.Foreign != 1 || rep.Current != 1 || rep.StaleTotal != 3 || rep.Rewritten != 0 {
		t.Fatalf("预演统计不对：%+v", rep)
	}
	if len(rep.Stale) != 2 || rep.Stale[0].Base != "http://172.17.0.1:6086" || rep.Stale[0].Count != 2 {
		t.Fatalf("旧地址分组不对：%+v", rep.Stale)
	}
	if read("回环.strm") != files["回环.strm"] {
		t.Fatal("预演不该写文件")
	}

	// 只改回环那一组
	rep, err = rebaseStrms(model.DB, root, "http://nas.lan:6086", []string{"http://172.17.0.1:6086/"}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewritten != 2 || rep.Failed != 0 {
		t.Fatalf("执行结果不对：%+v", rep)
	}
	if got := read("回环.strm"); got != "http://nas.lan:6086/d/aaa.mkv?/回环.mkv" {
		t.Fatalf("改写后 = %q", got)
	}
	if got := read("回环2.strm"); got != "http://nas.lan:6086/d/bbb.mkv\n" {
		t.Fatalf("改写后（带换行）= %q", got)
	}
	if read("旧IP.strm") != files["旧IP.strm"] || read("别家.strm") != files["别家.strm"] || read("某片.nfo") != files["某片.nfo"] {
		t.Fatal("没勾的旧地址、别家格式、附属文件都不该动")
	}
	if st, _ := os.Stat(filepath.Join(local, "回环.strm")); !st.ModTime().Equal(old) {
		t.Fatalf("修改时间应还原，得到 %v", st.ModTime())
	}

	// 本地整棵树读不到：报错，别说成「都是新地址」
	if _, err := rebaseStrms(model.DB, filepath.Join(root, "不存在"), "http://nas.lan:6086", nil, false, nil); err == nil {
		t.Fatal("挂载没就绪应报错")
	}
}
