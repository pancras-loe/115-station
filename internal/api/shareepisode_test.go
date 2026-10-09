package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"
)

func TestShareEntryOf(t *testing.T) {
	dec := func(s string) map[string]any {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := []struct {
		raw  string
		want shareEntry
	}{
		// 目录：fc=0，id 在 cid
		{`{"cid":"300","pid":"0","n":"三体","fc":"0"}`, shareEntry{ID: "300", Name: "三体", IsDir: true}},
		// 文件：id 在 fid，cid 是父目录；数字字段可能是数字
		{`{"fid":"401","cid":"300","n":"01.mkv","s":1024,"sha":"ABC","fc":1}`, shareEntry{ID: "401", Name: "01.mkv", Size: 1024, Sha1: "ABC"}},
		// 没有 fc：有 sha 就是文件
		{`{"fid":"402","cid":"300","file_name":"02.mkv","s":"2048","sha":"DEF"}`, shareEntry{ID: "402", Name: "02.mkv", Size: 2048, Sha1: "DEF"}},
		// 没有 fc 也没有 sha：只有 cid 的是目录
		{`{"cid":"301","n":"Season 2"}`, shareEntry{ID: "301", Name: "Season 2", IsDir: true}},
	}
	for i, c := range cases {
		if got := shareEntryOf(dec(c.raw)); got != c.want {
			t.Errorf("#%d = %+v, want %+v", i, got, c.want)
		}
	}
}

// fakeShareTree 假分享：cid → 子项
type fakeShareTree map[string][]shareEntry

func (f fakeShareTree) list(cid string) ([]shareEntry, string, error) {
	items, ok := f[cid]
	if !ok {
		return nil, "", fmt.Errorf("没有目录 %s", cid)
	}
	return append([]shareEntry(nil), items...), "某分享", nil
}

func dirE(id, name string) shareEntry { return shareEntry{ID: id, Name: name, IsDir: true} }
func fileE(id, name string, size int64) shareEntry {
	return shareEntry{ID: id, Name: name, Size: size}
}

func TestShareWalk(t *testing.T) {
	tree := fakeShareTree{
		"0":  {dirE("d1", "三体"), fileE("f0", "说明.txt", 1)},
		"d1": {dirE("d2", "Season 1"), dirE("d3", "Season 2")},
		"d2": {fileE("f1", "01.mkv", 10)},
		"d3": {fileE("f2", "01.mkv", 10), dirE("d4", "字幕")},
		"d4": {dirE("d5", "简体")},
		"d5": {fileE("f3", "01.ass", 1)},
	}
	entries, title, truncated, err := shareWalkWith(tree.list, 30)
	if err != nil || title != "某分享" {
		t.Fatal(err, title)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.ID] = e.Dir
	}
	if got["f1"] != "三体/Season 1" || got["f2"] != "三体/Season 2" {
		t.Fatalf("目录路径不对: %v", got)
	}
	// 三体/Season 2/字幕/简体 是第 4 层目录，进不去
	if _, ok := got["f3"]; ok || !truncated {
		t.Fatalf("超深的目录不该列: truncated=%v %v", truncated, got)
	}

	_, _, truncated, _ = shareWalkWith(tree.list, 2)
	if !truncated {
		t.Fatal("目录数到上限应标记没看全")
	}
	if _, _, _, err := shareWalkWith(fakeShareTree{}.list, 5); err == nil {
		t.Fatal("根都列不出来应报错")
	}
}

func pickIDs(p sharePick) []string {
	var ids []string
	for _, e := range p.Picks {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestPickShareEpisodesTV(t *testing.T) {
	s1, s2 := "剧/Season 1", "剧/Season 2"
	at := func(e shareEntry, dir string) shareEntry { e.Dir = dir; return e }
	entries := []shareEntry{
		at(fileE("a1", "01.mkv", 100), s1),
		at(fileE("a1s", "01.chs.ass", 1), s1), // 01 不缺，字幕也不要
		at(fileE("a2", "02.mkv", 100), s1),
		at(fileE("b1", "01.mkv", 100), s2),
		at(fileE("b1t", "01-thumb.jpg", 1), s2),
		at(fileE("b2a", "第02集.1080p.mkv", 300), s2),
		at(fileE("b2b", "第02集.2160p.mkv", 200), s2), // 洗版排名更好，体积小也要它
		at(fileE("b2bs", "第02集.2160p.chs.ass", 1), s2),
		at(fileE("b34", "S02E03-E04.mkv", 100), s2), // 双集：缺 E04 就要
		{ID: "b5", Name: "05.mkv", Dir: s2, Size: 100, Sha1: "inlib"},
		at(fileE("x1", "花絮.mkv", 50), "剧"),
		at(fileE("iso", "00001.m2ts", 50), "剧/BDMV/STREAM"),
		at(fileE("txt", "广告.txt", 1), "剧"),
	}
	missing := map[epKey]bool{{1, 2}: true, {2, 1}: true, {2, 2}: true, {2, 4}: true, {2, 5}: true}
	p := pickShareEpisodes(entries, sharePickOpts{
		MediaType: "tv",
		Missing:   missing,
		HaveSha1:  func(s string) bool { return s == "INLIB" },
		Rank: func(name string) int {
			if strings.Contains(name, "2160p") {
				return 0
			}
			return -1
		},
	})
	want := []string{"a2", "b1", "b1t", "b2b", "b2bs", "b34"}
	if got := pickIDs(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("挑中 %v, want %v（%s）", got, want, p.summary())
	}
	if !reflect.DeepEqual(p.Covered, epKeys("S01E02", "S02E01", "S02E02", "S02E04")) {
		t.Fatalf("覆盖 %v", p.Covered)
	}
	if p.Videos != 8 || p.Disc != 1 || p.InLib != 1 || p.Unknown != 1 {
		t.Fatalf("计数不对: %+v", p)
	}
}

// 订阅某一季：文件名和目录都没写季号时按订阅的季算
func TestPickShareEpisodesSeasonHint(t *testing.T) {
	entries := []shareEntry{fileE("e1", "01.mp4", 1), fileE("e2", "02.mp4", 1)}
	missing := map[epKey]bool{{2, 2}: true}
	p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: missing, SeasonHint: &ParsedName{Season: 2}})
	if got := pickIDs(p); !reflect.DeepEqual(got, []string{"e2"}) {
		t.Fatalf("挑中 %v（%s）", got, p.summary())
	}
	// 没有季号提示：01.mp4 / 02.mp4 是第 1 季，一集都不中
	if p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: missing}); len(p.Picks) != 0 {
		t.Fatalf("不该挑中: %v", pickIDs(p))
	}
}

func TestPickShareEpisodesRemap(t *testing.T) {
	entries := []shareEntry{fileE("e166", "S01E166.mkv", 1)}
	missing := map[epKey]bool{{7, 22}: true}
	p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: missing, Remap: func(eps map[string]*ParsedName) {
		if q := eps["e166"]; q != nil && q.Episode == 166 {
			q.Season, q.Episode = 7, 22
		}
	}})
	if !reflect.DeepEqual(p.Covered, epKeys("S07E22")) {
		t.Fatalf("连续编号换算后应覆盖 S07E22: %+v", p)
	}
}

func TestPickShareEpisodesMovie(t *testing.T) {
	entries := []shareEntry{
		fileE("m1", "沙丘2.2024.1080p.mkv", 8<<30),
		fileE("m1s", "沙丘2.2024.1080p.chs.srt", 1),
		fileE("m2", "沙丘2.2024.2160p.mkv", 20<<30),
		fileE("m2s", "沙丘2.2024.2160p.chs.srt", 1),
		fileE("m2n", "沙丘2.2024.2160p.nfo", 1),
		fileE("smp", "sample.mkv", 1<<20),
		fileE("iso", "沙丘2.iso", 40<<30),
	}
	p := pickShareEpisodes(entries, sharePickOpts{MediaType: "movie"})
	if got := pickIDs(p); !reflect.DeepEqual(got, []string{"m2", "m2n", "m2s"}) {
		t.Fatalf("挑中 %v", got)
	}
	if p.Disc != 1 {
		t.Fatalf("ISO 应算光盘结构: %+v", p)
	}
}

// 端到端：假 115 分享接口（替换镜像列表）。转存核心拆开后行为不变，顶层目录用 cid 转存
func TestShareReceiveCoreAndPicked(t *testing.T) {
	newTestDB(t, "sharerecv.db")
	var received []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/share/snap":
			q := r.URL.Query()
			if q.Get("share_code") != "sw1abc" || q.Get("receive_code") != "x1y2" {
				t.Errorf("参数不对: %v", q)
			}
			switch q.Get("cid") {
			case "0":
				fmt.Fprint(w, `{"state":true,"data":{"shareinfo":{"share_title":"三体合集"},"list":[
					{"cid":"300","pid":"0","n":"三体","fc":"0"},
					{"fid":"9","cid":"0","n":"说明.txt","s":3,"sha":"S9"}]}}`)
			case "300":
				fmt.Fprint(w, `{"state":true,"data":{"list":[{"fid":"401","cid":"300","n":"01.mkv","s":10,"sha":"A1"}]}}`)
			}
		case "/share/receive":
			r.ParseForm()
			received = append(received, r.PostForm)
			fmt.Fprint(w, `{"state":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	orig := shareAPIOrigins
	shareAPIOrigins = []string{srv.URL}
	t.Cleanup(func() { shareAPIOrigins = orig })

	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	model.DB.Create(&model.Storage{Type: "115", Cookie: "UID=1"})
	link := "https://115.com/s/sw1abc?password=x1y2"

	msg, ok, _, err := h.shareReceiveCore(link, "x1y2", "999", "web", false)
	if err != nil || ok != 2 {
		t.Fatalf("转存核心: %v %d %s", err, ok, msg)
	}
	if len(received) != 1 || received[0].Get("file_id") != "300,9" || received[0].Get("cid") != "999" {
		t.Fatalf("转存请求不对: %v", received)
	}
	var row model.DownloadLink
	model.DB.Last(&row)
	if names := unmarshalStrs(row.ResultNames); !reflect.DeepEqual(names, []string{"三体", "说明.txt"}) {
		t.Fatalf("认领名字 = %v", names)
	}

	entries, title, truncated, err := shareWalk("sw1abc", "x1y2", "UID=1", 10)
	if err != nil || title != "三体合集" || truncated || len(entries) != 3 {
		t.Fatalf("递归列分享: %v %q %v %+v", err, title, truncated, entries)
	}

	id, err := h.shareReceivePicked(link, "x1y2", []string{"401"}, "888", "三体合集", "订阅", []string{"三体 (2023) S01 ·订阅1-1"})
	if err != nil || id == 0 {
		t.Fatalf("挑选转存: %v %d", err, id)
	}
	if last := received[len(received)-1]; last.Get("file_id") != "401" || last.Get("cid") != "888" {
		t.Fatalf("挑选转存请求不对: %v", last)
	}
	var picked model.DownloadLink
	model.DB.First(&picked, id)
	if picked.Source != "订阅" || unmarshalStrs(picked.ResultNames)[0] != "三体 (2023) S01 ·订阅1-1" {
		t.Fatalf("来源链接登记不对: %+v", picked)
	}
}

// 订阅一轮的集数上限：只留集号靠前的，字幕跟着留下的视频走
func TestPickShareEpisodesMaxEps(t *testing.T) {
	entries := []shareEntry{
		fileE("e3", "S01E03.mkv", 1), fileE("e1", "S01E01.mkv", 1), fileE("e1s", "S01E01.chs.ass", 1),
		fileE("e2", "S01E02.mkv", 1), fileE("e4", "S01E04.mkv", 1), fileE("e4s", "S01E04.chs.ass", 1),
	}
	missing := map[epKey]bool{{1, 1}: true, {1, 2}: true, {1, 3}: true, {1, 4}: true}
	p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: missing, MaxEps: 2})
	if got := pickIDs(p); !reflect.DeepEqual(got, []string{"e1", "e1s", "e2"}) {
		t.Fatalf("挑中 %v（%s）", got, p.summary())
	}
	if !reflect.DeepEqual(p.Covered, []epKey{{1, 1}, {1, 2}}) {
		t.Fatalf("Covered = %v", p.Covered)
	}
	if p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: missing}); len(p.Covered) != 4 {
		t.Fatalf("不限时应挑满 4 集: %v", p.Covered)
	}
}
