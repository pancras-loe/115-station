package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestRebaseLine(t *testing.T) {
	pairs := []strmRebasePair{
		{From: "http://172.17.0.1:6086", To: "http://nas:6086"},
		{From: "http://192.168.1.2:9527", To: "http://192.168.1.9:9527"},
		{From: "http://192.168.1.2:9527/api/v1/plugin", To: "http://x/p"}, // 更长的开头优先
		{From: "/mnt/115", To: "/mnt/cd2/115"},
	}
	for in, want := range map[string]string{
		"http://172.17.0.1:6086/d/abc.mkv?/某片.mkv":                      "http://nas:6086/d/abc.mkv?/某片.mkv",
		"HTTP://172.17.0.1:6086/d/abc":                                  "http://nas:6086/d/abc",
		"http://192.168.1.2:9527?pickcode=x":                            "http://192.168.1.9:9527?pickcode=x",
		"http://192.168.1.2:9527/api/v1/plugin/P115StrmHelper/redirect": "http://x/p/P115StrmHelper/redirect",
		"/mnt/115/电影/某片.mkv":                                            "/mnt/cd2/115/电影/某片.mkv",
		"/mnt/115":                                                      "/mnt/cd2/115",
	} {
		got, _, ok := rebaseLine(in, pairs)
		if !ok || got != want {
			t.Errorf("rebaseLine(%q) = %q %v，期望 %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"http://172.17.0.1:60861/d/abc", // 端口只是开头相同
		"/mnt/1150/电影/某片.mkv",           // 目录名只是开头相同
		"http://nas:6086/d/abc",
		"xhttp://172.17.0.1:6086/d/abc",
	} {
		if got, _, ok := rebaseLine(in, pairs); ok {
			t.Errorf("rebaseLine(%q) 不该命中，得到 %q", in, got)
		}
	}
}

func TestNormRebasePairs(t *testing.T) {
	got, err := normRebasePairs([]strmRebasePair{{From: " http://a:1/ ", To: "http://b:2/"}, {}})
	if err != nil || len(got) != 1 || got[0] != (strmRebasePair{From: "http://a:1", To: "http://b:2"}) {
		t.Fatalf("归一 = %+v %v", got, err)
	}
	for _, bad := range [][]strmRebasePair{
		{{From: "http", To: "http://b"}},                                          // 太短
		{{From: "/", To: "/mnt"}},                                                 // 根目录
		{{From: "http://a", To: "nas:6086"}},                                      // 新地址不是地址
		{{From: "http://a/", To: "HTTP://A"}},                                     // 一样
		{{From: "http://a", To: "http://b"}, {From: "http://A/", To: "http://c"}}, // 旧地址重复
	} {
		if _, err := normRebasePairs(bad); err == nil {
			t.Errorf("%+v 应拒绝", bad)
		}
	}
}

func TestStrmAddrPrefix(t *testing.T) {
	for in, want := range map[string]struct {
		p       string
		station bool
	}{
		"http://172.17.0.1:6086/d/abc.mkv?/x.mkv":                  {"http://172.17.0.1:6086", true},
		"https://x.com/strm/d/abc":                                 {"https://x.com/strm", true},
		"http://mp:3000/api/v1/plugin/P115StrmHelper/redirect_url": {"http://mp:3000", false},
		"/mnt/115/电影/某片/某片.mkv":                                    {"/mnt/115", false},
		"/mnt/某片.mkv":                                              {"/mnt", false},
		"hello":                                                    {"", false},
	} {
		p, st := strmAddrPrefix(in)
		if p != want.p || st != want.station {
			t.Errorf("strmAddrPrefix(%q) = %q %v", in, p, st)
		}
	}
}

func TestRebaseStrms(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "电影", "某片")
	os.MkdirAll(local, 0o777)
	files := map[string]string{
		"回环.strm":   "http://172.17.0.1:6086/d/aaa.mkv?/回环.mkv",
		"回环2.strm":  "#EXTM3U\nhttp://172.17.0.1:6086/d/bbb.mkv\n",
		"旧IP.strm":  "http://192.168.1.2:6086/d/ccc",
		"已是新的.strm": "http://nas.lan:6086/d/ddd",
		"别家.strm":   "/mnt/115/电影/某片.mkv",
		"助手.strm":   "http://192.168.1.2:9527/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=x",
		"某片.nfo":    "http://172.17.0.1:6086/d/aaa", // 不是 .strm 不碰
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

	// 不给替换：只分组
	rep, err := rebaseStrms(root, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 6 || rep.Matched != 0 {
		t.Fatalf("分组统计不对：%+v", rep)
	}
	if rep.Groups[0] != (strmBaseCount{Base: "http://172.17.0.1:6086", Count: 2, Station: true}) {
		t.Fatalf("分组不对：%+v", rep.Groups)
	}
	if len(rep.Groups) != 5 {
		t.Fatalf("应有 5 组：%+v", rep.Groups)
	}

	pairs := []strmRebasePair{
		{From: "http://172.17.0.1:6086", To: "http://nas.lan:6086"},
		{From: "http://192.168.1.2:9527", To: "http://192.168.1.9:9527"},
	}
	// 预演：只统计、不写
	rep, err = rebaseStrms(root, pairs, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Matched != 3 || rep.Pairs[0].Count != 2 || rep.Pairs[1].Count != 1 || rep.Rewritten != 0 {
		t.Fatalf("预演统计不对：%+v", rep)
	}
	if read("回环.strm") != files["回环.strm"] {
		t.Fatal("预演不该写文件")
	}

	rep, err = rebaseStrms(root, pairs, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewritten != 3 || rep.Failed != 0 {
		t.Fatalf("执行结果不对：%+v", rep)
	}
	if got := read("回环.strm"); got != "http://nas.lan:6086/d/aaa.mkv?/回环.mkv" {
		t.Fatalf("改写后 = %q", got)
	}
	if got := read("回环2.strm"); got != "#EXTM3U\nhttp://nas.lan:6086/d/bbb.mkv\n" {
		t.Fatalf("改写后（带注释与换行）= %q", got)
	}
	if got := read("助手.strm"); got != "http://192.168.1.9:9527/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=x" {
		t.Fatalf("别的项目的地址也能换：%q", got)
	}
	if read("旧IP.strm") != files["旧IP.strm"] || read("别家.strm") != files["别家.strm"] || read("某片.nfo") != files["某片.nfo"] {
		t.Fatal("没填的旧地址、非 .strm 都不该动")
	}
	if st, _ := os.Stat(filepath.Join(local, "回环.strm")); !st.ModTime().Equal(old) {
		t.Fatalf("修改时间应还原，得到 %v", st.ModTime())
	}

	// 本地一个 STRM 都没有：报错，别说成「没有要改的」
	empty := t.TempDir()
	if _, err := rebaseStrms(empty, pairs, false, nil); err == nil {
		t.Fatal("挂载没就绪应报错")
	}
}
