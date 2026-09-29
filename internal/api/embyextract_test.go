package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 假 Emby：片目目录上是 Folder 条目，下面三集 —— 一集已有媒体信息、一集缺、一集是 ISO
func fakeEmbyForExtract(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var probed []string
	stream := func(types ...string) []map[string]string {
		var out []map[string]string
		for _, ty := range types {
			out = append(out, map[string]string{"Type": ty})
		}
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/Items" && q.Get("Path") != "":
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{
				{"Id": "dir1", "Name": "某剧", "Path": q.Get("Path"), "Type": "Folder"},
			}})
		case r.URL.Path == "/Items" && q.Get("ParentId") == "dir1":
			if q.Get("Recursive") != "true" || q.Get("Fields") == "" {
				t.Errorf("列子条目的参数不对: %v", q)
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "ep1", "Type": "Episode", "SeriesName": "某剧", "ParentIndexNumber": 1, "IndexNumber": 1,
					"Path": "/media/某剧/S01E01.strm", "MediaStreams": stream("Video", "Audio", "Subtitle")},
				{"Id": "ep2", "Type": "Episode", "SeriesName": "某剧", "ParentIndexNumber": 1, "IndexNumber": 2,
					"Path": "/media/某剧/S01E02.strm", "MediaStreams": stream("Subtitle")},
				{"Id": "ep3", "Type": "Episode", "Path": "/media/某剧/BD.iso.strm"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/ep2/PlaybackInfo":
			if r.URL.Query().Get("api_key") != "k" {
				t.Errorf("PlaybackInfo 没带 api_key")
			}
			mu.Lock()
			probed = append(probed, "ep2")
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{
				{"MediaStreams": stream("Video", "Audio", "Audio", "Subtitle")},
			}})
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &probed
}

func TestEmbyExtractTargetsOnlyMissing(t *testing.T) {
	srv, probed := fakeEmbyForExtract(t)
	cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}
	items, err := embyExtractTargets(cfg, "/media/某剧")
	if err != nil {
		t.Fatal(err)
	}
	// 已有音视频的不碰、ISO 不碰，只剩缺媒体信息的那一集
	if len(items) != 1 || items[0].ID != "ep2" || items[0].label() != "某剧 S01E02" {
		t.Fatalf("应只挑出 ep2，得到 %+v", items)
	}
	embyExtractOne(cfg, items[0])
	if len(*probed) != 1 {
		t.Fatalf("应对 ep2 调一次 PlaybackInfo，实际 %v", *probed)
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
		p, ok := embyExtractPop()
		if !ok {
			break
		}
		got = append(got, p)
	}
	if len(got) != 3 || got[0] != "/a" || got[1] != "/b" || got[2] != "/c" {
		t.Fatalf("排队应去重保序，得到 %v", got)
	}
	// 出队后可以再排
	queueEmbyExtract("/a")
	if p, ok := embyExtractPop(); !ok || p != "/a" {
		t.Fatal("出过队的路径应能再次排队")
	}
}
