package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"115-station/internal/model"
)

// fakeExtractEmby 假 Emby：片目目录上是 Folder 条目，下面若干集。
// probeOK=false 时 PlaybackInfo 返回 500；成功探测过的集之后列出来就带媒体信息（和真 Emby 一样）
type fakeExtractEmby struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	probeOK bool
	// noStreams：PlaybackInfo 返回 200 却没有轨道（Emby 的 ffprobe 读不了文件）；
	// linkServed 再模拟探测期间本站给出过直链
	noStreams, linkServed bool
	eps     []string        // 缺媒体信息的集 id
	done    map[string]bool // 已探测成功
	calls   map[string]int  // 每个 id 的 PlaybackInfo 次数
}

func newFakeExtractEmby(t *testing.T, probeOK bool, eps ...string) *fakeExtractEmby {
	t.Helper()
	f := &fakeExtractEmby{t: t, probeOK: probeOK, eps: eps, done: map[string]bool{}, calls: map[string]int{}}
	streams := func(types ...string) []map[string]string {
		var out []map[string]string
		for _, ty := range types {
			out = append(out, map[string]string{"Type": ty})
		}
		return out
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/Items" && q.Get("Path") != "":
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{
				{"Id": "dir1", "Name": "某剧", "Path": q.Get("Path"), "Type": "Folder"},
			}})
		case r.URL.Path == "/Items" && q.Get("ParentId") == "dir1":
			items := []map[string]any{
				// 早就有媒体信息的：不碰
				{"Id": "has", "Type": "Episode", "Path": "/media/某剧/S01E09.strm", "MediaStreams": streams("Video", "Audio")},
				// 光盘结构：不碰。STRM 名不带扩展名之后，认它靠 Emby 记下的直链（ISO 一律带 .iso）
				{"Id": "iso", "Type": "Episode", "Path": "/media/某剧/BD.strm",
					"MediaSources": []map[string]any{{"Id": "iso", "Path": "http://127.0.0.1:6086/d/abc.iso?/BD.iso"}}},
			}
			for i, id := range f.eps {
				it := map[string]any{"Id": id, "Type": "Episode", "SeriesName": "某剧", "ParentIndexNumber": 1,
					"IndexNumber": i + 1, "Path": fmt.Sprintf("/media/某剧/S01E%02d.strm", i+1)}
				if f.done[id] {
					it["MediaStreams"] = streams("Video", "Audio")
				}
				items = append(items, it)
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": items})
		case r.Method == http.MethodPost && len(r.URL.Path) > len("/Items/") && r.URL.Query().Get("api_key") == "k":
			var id string
			fmt.Sscanf(r.URL.Path, "/Items/%s", &id)
			id = id[:len(id)-len("/PlaybackInfo")]
			f.calls[id]++
			if f.linkServed {
				playbackLinksServed.Add(1)
			}
			if f.noStreams {
				json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{{"MediaStreams": nil}}})
				return
			}
			if !f.probeOK {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			f.done[id] = true
			json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{
				{"MediaStreams": streams("Video", "Audio", "Audio", "Subtitle")},
			}})
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeExtractEmby) cfg() embyRefreshCfg {
	return embyRefreshCfg{ServerURL: f.srv.URL, APIKey: "k"}
}

func (f *fakeExtractEmby) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// resetExtractState 每个测试一个新库（记账走线上同一条落库路径），间隔调到几乎为零
func resetExtractState(t *testing.T) {
	t.Helper()
	newTestDB(t, "extract.db")
	embyExtractFails = 0
	gap, pause := embyExtractGap, embyExtractBreakPause
	embyExtractGap, embyExtractBreakPause = time.Millisecond, time.Millisecond
	t.Cleanup(func() { embyExtractGap, embyExtractBreakPause = gap, pause })
}

func TestEmbyExtractTargetsOnlyMissing(t *testing.T) {
	f := newFakeExtractEmby(t, true, "ep1")
	items, found, err := embyExtractTargets(f.cfg(), "/media/某剧")
	if err != nil || !found {
		t.Fatal(err)
	}
	// 已有音视频的不碰、ISO 不碰，只剩缺媒体信息的那一集
	if len(items) != 1 || items[0].ID != "ep1" || items[0].label() != "某剧 S01E01" {
		t.Fatalf("应只挑出 ep1，得到 %+v", items)
	}
}

// 同一片目反复进队列（整理后刮削 + 两次入库确认 + 目录 / .strm 两种路径）：成功过的不再探
func TestEmbyExtractSuccessNotRepeated(t *testing.T) {
	resetExtractState(t)
	f := newFakeExtractEmby(t, true, "ep1", "ep2")
	for i := 0; i < 3; i++ {
		embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/某剧"})
	}
	if f.calls["ep1"] != 1 || f.calls["ep2"] != 1 || f.totalCalls() != 2 {
		t.Fatalf("每集只能探一次，实际 %v", f.calls)
	}
	if _, ok := embyExtractLoad("ep1"); ok {
		t.Fatal("成功后应删账")
	}
}

// 失败 / 超时的条目：再来多少次入库确认都不能马上重探
func TestEmbyExtractFailureNotRepeated(t *testing.T) {
	resetExtractState(t)
	f := newFakeExtractEmby(t, false, "ep1")
	for i := 0; i < 5; i++ {
		embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/某剧"})
	}
	if f.calls["ep1"] != 1 {
		t.Fatalf("失败的条目 24 小时内只能请求一次，实际 %d 次", f.calls["ep1"])
	}
	m, ok := embyExtractLoad("ep1")
	if !ok || m.Attempts != 1 || m.LastErr != "HTTP 500" {
		t.Fatalf("应留下失败记账：%+v", m)
	}
}

func TestEmbyExtractLedgerWindowAndCap(t *testing.T) {
	resetExtractState(t)
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	if ok, _ := embyExtractClaim("x", "", t0, false); !ok {
		t.Fatal("第一次应允许")
	}
	embyExtractSettle("x", false, "超时")
	if ok, _ := embyExtractClaim("x", "", t0.Add(time.Hour), false); ok {
		t.Fatal("24 小时内不许再试")
	}
	if ok, _ := embyExtractClaim("x", "", t0.Add(25*time.Hour), false); !ok {
		t.Fatal("过了间隔应允许第二次")
	}
	embyExtractSettle("x", false, "超时")
	// 次数用完后永远不再自动探（不再 30 天清账重来）
	if ok, why := embyExtractClaim("x", "", t0.Add(365*24*time.Hour), false); ok {
		t.Fatal("试满两次不再自动探测")
	} else if why == "" {
		t.Fatal("拒绝要说明原因")
	}

	// 手动：不看次数与 24 小时，只看防抖
	t1 := t0.Add(25 * time.Hour) // 第二次请求在 t1
	if ok, _ := embyExtractClaim("x", "", t1.Add(time.Minute), true); ok {
		t.Fatal("防抖期内手动也不许请求")
	}
	if ok, _ := embyExtractClaim("x", "", t1.Add(embyExtractDebounce+time.Second), true); !ok {
		t.Fatal("防抖过了手动应允许，哪怕自动次数已用完")
	}
	if m, _ := embyExtractLoad("x"); m.Attempts != 3 {
		t.Fatalf("手动的尝试也要记账：%+v", m)
	}
	// 手动失败过的条目，自动入口不会因为手动那次而多出机会
	if ok, _ := embyExtractClaim("x", "", t1.Add(48*time.Hour), false); ok {
		t.Fatal("自动次数已用完，手动之后也不许自动再探")
	}
	// 成功即删账
	embyExtractClaim("y", "", t0, false)
	embyExtractSettle("y", true, "")
	if _, ok := embyExtractLoad("y"); ok {
		t.Fatal("成功后应删账")
	}
}

// 任务中心「忽略」：只打标记不删账 —— 自动入口不再探、次数不清零；手动请求清掉标记，再失败回到清单
func TestEmbyExtractIgnored(t *testing.T) {
	resetExtractState(t)
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	embyExtractClaim("x", "", t0, false)
	embyExtractSettle("x", false, "超时")
	model.DB.Model(&model.EmbyExtractMark{}).Where("item_id = ?", "x").Update("ignored_at", t0.Add(time.Hour))
	if ok, why := embyExtractClaim("x", "", t0.Add(48*time.Hour), false); ok || why == "" {
		t.Fatal("忽略后自动入口不许再探，且要说明原因")
	}
	if m, _ := embyExtractLoad("x"); m.Attempts != 1 || m.IgnoredAt == nil {
		t.Fatalf("忽略不删账、不改次数: %+v", m)
	}
	if ok, _ := embyExtractClaim("x", "", t0.Add(48*time.Hour), true); !ok {
		t.Fatal("忽略的条目手动照常能探")
	}
	if m, _ := embyExtractLoad("x"); m.IgnoredAt != nil || m.Attempts != 2 {
		t.Fatalf("手动请求要清掉忽略标记、照常记次数: %+v", m)
	}
}

// 连续失败触发熔断暂停，暂停之后计数清零
func TestEmbyExtractBreaker(t *testing.T) {
	resetExtractState(t)
	embyExtractBreakPause = 150 * time.Millisecond
	f := newFakeExtractEmby(t, false, "a", "b", "c")
	start := time.Now()
	embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/某剧"})
	if f.totalCalls() != 3 {
		t.Fatalf("三个条目各请求一次，实际 %v", f.calls)
	}
	if time.Since(start) < embyExtractBreakPause {
		t.Fatal("连续 3 个失败应暂停")
	}
	if embyExtractFails != 0 {
		t.Fatal("暂停后连续失败计数应清零")
	}
}

// 直链取到了、Emby 的 ffprobe 读不了文件（2026-10-02《鱿鱼游戏》S02 的 xHE-AAC 音轨）：
// 是文件的毛病，不计入熔断；没取过直链的「没轨道」仍然计入（可能是 115 取不到链）
func TestEmbyExtractFileFaultNoBreak(t *testing.T) {
	resetExtractState(t)
	embyExtractBreakPause = 300 * time.Millisecond
	f := newFakeExtractEmby(t, false, "a", "b", "c")
	f.noStreams, f.linkServed = true, true
	start := time.Now()
	embyExtractPath(f.cfg(), embyExtractEntry{path: "/media/某剧"})
	if f.totalCalls() != 3 {
		t.Fatalf("三个条目各请求一次，实际 %v", f.calls)
	}
	if time.Since(start) >= embyExtractBreakPause {
		t.Fatal("文件本身读不了不应触发熔断")
	}
	if m, ok := embyExtractLoad("a"); !ok || m.LastErr == "" {
		t.Fatalf("仍要记失败账：%+v", m)
	}

	resetExtractState(t)
	embyExtractBreakPause = 150 * time.Millisecond
	g := newFakeExtractEmby(t, false, "a", "b", "c")
	g.noStreams = true
	start = time.Now()
	embyExtractPath(g.cfg(), embyExtractEntry{path: "/media/某剧"})
	if time.Since(start) < embyExtractBreakPause {
		t.Fatal("没取到直链的失败应照常熔断")
	}
}

func TestEmbyStreamsComplete(t *testing.T) {
	s := func(types ...string) []embyStream {
		var out []embyStream
		for _, ty := range types {
			out = append(out, embyStream{Type: ty})
		}
		return out
	}
	if embyStreamsComplete(s("Video")) || embyStreamsComplete(s("Subtitle", "Subtitle")) || embyStreamsComplete(nil) {
		t.Fatal("只有一条视频 / 只有字幕不算提取过")
	}
	if !embyStreamsComplete(s("Video", "Audio")) {
		t.Fatal("视频 + 音轨算提取过")
	}
	if got := embyStreamsBrief(s("Video", "Audio", "Audio", "Subtitle")); got != "视频 1 · 音轨 2 · 字幕 1" {
		t.Fatalf("摘要: %q", got)
	}
}

func TestQueueEmbyExtractDedupe(t *testing.T) {
	embyExtractQ.once.Do(func() {}) // 不起 worker：只测排队
	queueEmbyExtract("/a", "/b", "/a", "")
	queueEmbyExtract("/b", "/c")
	var got []string
	for {
		e, ok := embyExtractPop()
		if !ok {
			break
		}
		got = append(got, e.path)
	}
	if len(got) != 3 || got[0] != "/a" || got[1] != "/b" || got[2] != "/c" {
		t.Fatalf("排队应去重保序，得到 %v", got)
	}
}

// 入库确认的路径归到片目目录：样本 .strm → 整部；片目之上的目录（同步根 / 媒体库 / 分类）一律不自动探
func TestExtractTitleTargets(t *testing.T) {
	layout := buildLibCategoryLayout([]model.CategoryRule{
		{MediaType: "movie", Name: "电影"},
		{MediaType: "tv", Name: "剧集/国产剧"},
	})
	id := func(s string) string { return s }
	got := extractTitleTargets([]string{
		"/strm/媒体库/剧集/国产剧/某剧 (2023)/Season 01/某剧 S01E01.strm",
		"/strm/媒体库/剧集/国产剧/某剧 (2023)/Season 01/某剧 S01E02.strm", // 同一部：只排一次
		"/strm/媒体库/剧集/国产剧/某剧 (2023)/Season 02",                // 季目录：归到片目
		"/strm/媒体库/电影/某片 (2020)/某片.strm",
		"/strm/媒体库/电影/散文件.strm", // 不属于任何片目：只探它自己
		"/strm",                 // 同步根
		"/strm/媒体库",             // 媒体库
		"/strm/媒体库/剧集/国产剧",      // 分类
		"/strm/媒体库/剧集",          // 分类的上级
	}, "/strm", layout, id, filepath.ToSlash) // 真实的 embyPathOf 收本地原生路径；测试在 Windows 上也要跑
	want := []string{
		"/strm/媒体库/剧集/国产剧/某剧 (2023)",
		"/strm/媒体库/电影/某片 (2020)",
		"/strm/媒体库/电影/散文件.strm",
	}
	if len(got) != len(want) {
		t.Fatalf("得到 %v，预期 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个：%s，预期 %s（全部 %v）", i, got[i], want[i], got)
		}
	}
}
