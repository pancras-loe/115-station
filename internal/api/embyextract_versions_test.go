package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeVersionsEmby 假 Emby：一部电影目录里两个 .strm，Emby 合成一个 Movie 条目、两个版本。
// PlaybackInfo 只探被点名（MediaSourceId）的版本；不点名时只探第一个 —— 和现场一样。
// alsoAlt=true 时另一个版本还被单独列成一个条目（有的 Emby 版本这么列）
type fakeVersionsEmby struct {
	srv     *httptest.Server
	mu      sync.Mutex
	done    map[string]bool // 版本 id → 已有媒体信息
	calls   []string        // 每次 PlaybackInfo 点名的版本（空 = 没点名）
	alsoAlt bool
}

func newFakeVersionsEmby(t *testing.T, alsoAlt bool) *fakeVersionsEmby {
	t.Helper()
	f := &fakeVersionsEmby{done: map[string]bool{}, alsoAlt: alsoAlt}
	full := []map[string]string{{"Type": "Video"}, {"Type": "Audio"}}
	src := func(id, p string) map[string]any {
		m := map[string]any{"Id": id, "Path": p}
		if f.done[id] {
			m["MediaStreams"] = full
		}
		return m
	}
	item := func(id, p string, order ...[2]string) map[string]any {
		var ss []map[string]any
		for _, o := range order {
			ss = append(ss, src(o[0], o[1]))
		}
		it := map[string]any{"Id": id, "Type": "Movie", "Name": "回到未来", "Path": p, "MediaSources": ss}
		if f.done[id] {
			it["MediaStreams"] = full
		}
		return it
	}
	a := [2]string{"va", "/media/回到未来/A.strm"}
	b := [2]string{"vb", "/media/回到未来/B.strm"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/Items" && q.Get("Path") != "":
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{
				{"Id": "dir", "Path": q.Get("Path"), "Type": "Folder"},
			}})
		case r.URL.Path == "/Items" && q.Get("ParentId") == "dir":
			items := []map[string]any{item("va", a[1], a, b)}
			if f.alsoAlt {
				items = append(items, item("vb", b[1], b, a))
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": items})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			sid := q.Get("MediaSourceId")
			f.calls = append(f.calls, sid)
			if sid == "" {
				sid = "va"
			}
			f.done[sid] = true
			p := a[1]
			if sid == "vb" {
				p = b[1]
			}
			json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{src(sid, p)}})
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeVersionsEmby) cfg() embyRefreshCfg { return embyRefreshCfg{ServerURL: f.srv.URL, APIKey: "k"} }

// 双版本电影：两个版本各点名探一次，之后不再探（2026-09-29 现场：只探了一个，另一个一直「未探测」）
func TestEmbyExtractMultiVersion(t *testing.T) {
	resetExtractState(t)
	f := newFakeVersionsEmby(t, false)
	f.done["va"] = true // 主版本早有了，只剩 B：只按条目 id 探的话 Emby 探的还是 A
	for i := 0; i < 3; i++ {
		embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/回到未来", manual: true})
	}
	if strings.Join(f.calls, ",") != "vb" {
		t.Fatalf("应只点名探 B 一次，实际 %q", f.calls)
	}
	if _, ok := embyExtractLoad("va"); ok {
		t.Fatal("成功后应删账")
	}
}

// Emby 把另一个版本也单独列成条目：同一轮里同一个版本不探两次
func TestEmbyExtractMultiVersionListedTwice(t *testing.T) {
	resetExtractState(t)
	f := newFakeVersionsEmby(t, true)
	embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/回到未来", manual: true})
	if strings.Join(f.calls, ",") != "va,vb" {
		t.Fatalf("两个版本应各探一次，实际 %q", f.calls)
	}
}

func TestLackingSources(t *testing.T) {
	full := []embyStream{{Type: "Video"}, {Type: "Audio"}}
	var it embyExtractItem
	it.Path = "/m/A.strm"
	if len(it.lackingSources()) != 1 {
		t.Fatal("没带 MediaSources 又没轨道：整个条目算缺")
	}
	it.MediaStreams = full
	if !it.hasMediaInfo() {
		t.Fatal("单版本沿用旧判据：顶层齐了就算有")
	}
	it.MediaSources = []embyMediaSource{{ID: "a", Path: "/m/A.strm"}, {ID: "b", Path: "/m/B.strm"}}
	got := it.lackingSources()
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("顶层轨道只算主版本的，B 还缺: %+v", got)
	}
	it.MediaSources[1].MediaStreams = full
	if !it.hasMediaInfo() {
		t.Fatal("两个版本都齐了")
	}
}

// 片目详情：双版本拆成两行，各自报有没有媒体信息
func TestEmbyDetailsOfVersions(t *testing.T) {
	var it embyExtractItem
	it.ID, it.Type, it.Name, it.Path = "9", "Movie", "回到未来", "/m/A.strm"
	it.MediaSources = []embyMediaSource{
		{ID: "a", Path: "/m/A.strm", MediaStreams: []embyStream{{Type: "Video"}, {Type: "Audio"}}},
		{ID: "b", Path: "/m/B.strm"},
	}
	ds := embyDetailsOf(it, "/m", "")
	if len(ds) != 2 || ds[0].ID == ds[1].ID || !ds[0].HasInfo || ds[1].HasInfo || ds[1].Name != "B" || ds[1].Rel != "B.strm" {
		t.Fatalf("应拆成两行: %+v", ds)
	}
}

// 真实 Emby 的多版本（2026-09-30《夏洛特烦恼》现场）：同目录两个 .strm 是两个独立的 Movie 条目，
// 各带一个 MediaSources；对任何一个发 PlaybackInfo 返回整组版本，主版本排第一。
// 主版本早有媒体信息时，另一个必须点名探，且不能拿主版本的轨道报成功
func TestEmbyExtractSeparateVersionItems(t *testing.T) {
	resetExtractState(t)
	full := []map[string]string{{"Type": "Video"}, {"Type": "Audio"}}
	var mu sync.Mutex
	var calls []string
	done := map[string]bool{"882": true}
	src := func(id string) map[string]any {
		m := map[string]any{"Id": "mediasource_" + id, "Path": "http://x/d/" + id + ".mkv"}
		if done[id] {
			m["MediaStreams"] = full
		}
		return m
	}
	item := func(id string) map[string]any {
		return map[string]any{"Id": id, "Type": "Movie", "Name": "夏洛特烦恼",
			"Path": "/media/夏洛特烦恼/" + id + ".strm", "MediaSources": []any{src(id)}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/Items" && q.Get("Path") != "":
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{
				{"Id": "dir", "Path": q.Get("Path"), "Type": "Folder"}}})
		case r.URL.Path == "/Items" && q.Get("ParentId") == "dir":
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{item("882"), item("883")}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			sid := q.Get("MediaSourceId")
			calls = append(calls, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Items/"), "/PlaybackInfo")+"|"+sid)
			if sid == "" {
				sid = "mediasource_882" // 不点名：Emby 挑主版本
			}
			done[strings.TrimPrefix(sid, "mediasource_")] = true
			json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{src("882"), src("883")}})
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}
	for i := 0; i < 3; i++ {
		embyExtractPath(cfg, embyExtractEntry{path: "/media/夏洛特烦恼", manual: true})
	}
	if strings.Join(calls, ",") != "883|mediasource_883" || !done["883"] {
		t.Fatalf("应点名探 883 一次，实际 %q", calls)
	}
}

func TestPlaybackSourceStreamsByID(t *testing.T) {
	full := []embyStream{{Type: "Video"}, {Type: "Audio"}}
	resp := []embyMediaSource{{ID: "mediasource_882", MediaStreams: full}, {ID: "mediasource_883"}}
	if embyStreamsComplete(playbackSourceStreams(resp, embyMediaSource{ID: "mediasource_883"})) {
		t.Fatal("返回里 883 没有轨道，不能拿排第一的 882 报成功")
	}
	if !embyStreamsComplete(playbackSourceStreams(resp, embyMediaSource{})) {
		t.Fatal("不知道版本时沿用第一个")
	}
}
