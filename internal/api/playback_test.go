package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type playbackFixture struct {
	front     *httptest.Server
	cdn       *httptest.Server
	cfg       *config.Config
	root      string
	embyMedia atomic.Int64
	cdnHits   atomic.Int64
	fetches   atomic.Int64
	mu        sync.Mutex
	uas       []string
	pcs       []string
}

func setupPlaybackFixture(t *testing.T) *playbackFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	newTestDB(t, "playback.db")
	f := &playbackFixture{root: t.TempDir(), cfg: &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}}
	save := func(key string, value any) {
		b, _ := json.Marshal(value)
		if err := f.cfg.SaveSetting(key, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(f.root, name+".strm"), []byte("http://old.example/d/"+name+".mkv?/movie.mkv"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	save("full", map[string]string{"local_path": f.root})
	save("strm", map[string]string{"domain": "https://station.example"})
	f.cdn = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.cdnHits.Add(1)
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 10-19/100")
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = io.WriteString(w, "CDN-VIDEO")
	}))
	t.Cleanup(f.cdn.Close)
	emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 配置里存了管理员 key，但它绝不能出现在播放权限查询中。
		if r.Header.Get("X-Emby-Token") == "server-admin" || r.URL.Query().Get("api_key") == "server-admin" {
			t.Error("使用了管理员凭据")
		}
		p := strings.TrimPrefix(r.URL.Path, "/backend")
		token := embyClientToken(r)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(strings.ToLower(p), "/playbackinfo") {
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{
				{"Id": "mediasource_a", "Path": filepath.Join(f.root, "first.strm"), "SupportsTranscoding": true, "DirectStreamUrl": "/old", "TranscodingUrl": "/old.m3u8"},
				{"Id": "mediasource_b", "Path": filepath.Join(f.root, "second.strm"), "SupportsTranscoding": true},
				{"Id": "external", "Path": "https://other.example/video.mkv", "SupportsDirectPlay": true},
			}})
			return
		}
		if strings.HasPrefix(p, "/Users/") && !strings.Contains(strings.TrimPrefix(p, "/Users/"), "/") {
			if p != "/Users/Me" && p != "/Users/viewer-id" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if token != "viewer" && token != "blocked" && token != "no-download" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "viewer-id", "Policy": map[string]bool{"EnableMediaPlayback": token != "blocked", "EnableContentDownloading": token != "no-download"}})
			return
		}
		if strings.HasPrefix(p, "/Users/") {
			if !strings.HasPrefix(p, "/Users/viewer-id/Items/") || r.URL.Query().Get("UserId") != "" {
				t.Error("用户身份未绑定到客户端 token")
			}
			if r.URL.Query().Get("Fields") != "Path,MediaSources" {
				t.Error("没有请求媒体源字段")
			}
			id := strings.TrimPrefix(p, "/Users/viewer-id/Items/")
			item := embyPlaybackItem{ID: id, Path: filepath.Join(f.root, "first.strm"), PlayAccess: "Full", MediaSources: []embyPlaybackMediaSource{
				{ID: "mediasource_a", Path: filepath.Join(f.root, "first.strm")},
				{ID: "mediasource_b", Path: filepath.Join(f.root, "second.strm")},
			}}
			switch id {
			case "denied":
				item.PlayAccess = "None"
			case "missing":
				item.Path = filepath.Join(f.root, "missing.strm")
				item.MediaSources = nil
			case "local":
				item.Path = filepath.Join(f.root, "local.mkv")
				item.MediaSources = nil
			case "external":
				item.Path = "https://other.example/video.mkv"
				item.MediaSources = nil
			case "failed":
				item.Path = "https://station.example/d/failure"
				item.MediaSources = nil
			case "cookie":
				item.Path = "https://station.example/d/cookie"
				item.MediaSources = nil
			case "notfound":
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(item)
			return
		}
		if strings.Contains(strings.ToLower(p), "/subtitles/") {
			_, _ = io.WriteString(w, "subtitle")
			return
		}
		f.embyMedia.Add(1)
		_, _ = io.WriteString(w, "EMBY-MEDIA")
	}))
	t.Cleanup(emby.Close)
	save("emby", map[string]string{"server_url": emby.URL + "/backend", "api_key": "server-admin"})
	old := playbackLinks
	playbackLinks = newPlaybackLinkResolver(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		f.fetches.Add(1)
		f.mu.Lock()
		f.uas = append(f.uas, ua)
		f.pcs = append(f.pcs, pc)
		f.mu.Unlock()
		if pc == "failure" {
			return "", nil, errors.New("模拟取链失败")
		}
		if pc == "cookie" {
			return f.cdn.URL + "/cookie?f=3", map[string]string{"Cookie": "secret"}, nil
		}
		return f.cdn.URL + "/" + pc, map[string]string{"User-Agent": ua}, nil
	})
	t.Cleanup(func() { playbackLinks = old })
	UpdateEmbyConfig("")
	t.Cleanup(func() { UpdateEmbyConfig("") })
	r := gin.New()
	r.SetTrustedProxies(nil)
	registerDirectPlaybackRoutes(r, model.DB, f.cfg)
	registerEmbyProxy(r, model.DB, f.cfg)
	f.front = httptest.NewServer(r)
	t.Cleanup(f.front.Close)
	return f
}

func playbackRequest(t *testing.T, f *playbackFixture, method, route, ua, token string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, f.front.URL+route, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Range", "bytes=10-19")
	if token != "" {
		r.Header.Set("X-Emby-Token", token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPlaybackActualVideoRedirectsWithoutRelaying(t *testing.T) {
	f := setupPlaybackFixture(t)
	for _, prefix := range []string{"", "/emby"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, ua := range []string{"Player/1", ""} {
				for _, route := range []string{"/Videos/one/stream.mkv?MediaSourceId=mediasource_b", "/videos/one/original?mediasourceid=b", "/Items/one/Download?MediaSourceId=b"} {
					resp := playbackRequest(t, f, method, prefix+route, ua, "viewer")
					body, _ := io.ReadAll(resp.Body)
					if resp.StatusCode != 302 || resp.Header.Get("Location") != f.cdn.URL+"/second" || len(body) != 0 {
						t.Fatalf("%s %s UA=%q: %d %s %q", method, route, ua, resp.StatusCode, resp.Header.Get("Location"), body)
					}
				}
			}
		}
	}
	if f.embyMedia.Load() != 0 || f.cdnHits.Load() != 0 {
		t.Fatalf("代理读取了视频：Emby=%d CDN=%d", f.embyMedia.Load(), f.cdnHits.Load())
	}
	if f.fetches.Load() != 2 {
		t.Fatalf("相同 pickcode+UA 没有复用缓存：%d", f.fetches.Load())
	}
	// 客户端自行跟随 Location 并携带 Range，视频此时才从 CDN 读取。
	r, _ := http.NewRequest(http.MethodGet, f.cdn.URL+"/second", nil)
	r.Header.Set("Range", "bytes=10-19")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 || f.cdnHits.Load() != 1 {
		t.Fatal("客户端 CDN Range 播放失败")
	}
}

func TestPlaybackFailsClosedAndKeepsPermissions(t *testing.T) {
	f := setupPlaybackFixture(t)
	for _, tc := range []struct {
		route, token string
		status       int
	}{
		{"/Videos/one/stream?MediaSourceId=a", "", 401},
		{"/Videos/one/stream?MediaSourceId=a", "bad", 401},
		{"/Videos/one/stream?MediaSourceId=a", "blocked", 403},
		{"/Videos/denied/stream?MediaSourceId=a&UserId=admin", "viewer", 403},
		{"/Items/one/Download?MediaSourceId=a", "no-download", 403},
		{"/Videos/notfound/stream", "viewer", 404},
		{"/Videos/one/stream?MediaSourceId=unknown", "viewer", 400},
		{"/Videos/one/stream", "viewer", 400},
		{"/Videos/missing/stream", "viewer", 502},
		{"/Videos/failed/stream", "viewer", 502},
		{"/Videos/cookie/stream", "viewer", 502},
		{"/Videos/one/master.m3u8?MediaSourceId=a", "viewer", 415},
		{"/Videos/one/hls1/main/0.ts?MediaSourceId=a", "viewer", 415},
		{"/Videos/one/stream?MediaSourceId=a&Static=false", "viewer", 415},
	} {
		resp := playbackRequest(t, f, http.MethodGet, tc.route, "Player", tc.token)
		if resp.StatusCode != tc.status || resp.Header.Get("Location") != "" {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("%s: %d %s", tc.route, resp.StatusCode, b)
		}
	}
	if f.embyMedia.Load() != 0 || f.cdnHits.Load() != 0 {
		t.Fatal("失败或无权限请求退回了中转")
	}
}

func TestPlaybackLeavesOtherMediaAndSubtitlesAlone(t *testing.T) {
	f := setupPlaybackFixture(t)
	for _, route := range []string{"/Videos/local/stream", "/Videos/external/stream", "/Videos/one/Subtitles/0/Stream.srt"} {
		resp := playbackRequest(t, f, http.MethodGet, route, "Player", "viewer")
		if resp.StatusCode != 200 || resp.Header.Get("Location") != "" {
			t.Fatalf("普通媒体被接管：%s %d", route, resp.StatusCode)
		}
	}
	if f.fetches.Load() != 0 {
		t.Fatal("普通媒体触发了 115 取链")
	}
}

func TestPlaybackInfoLeadsToAuthenticatedRedirect(t *testing.T) {
	f := setupPlaybackFixture(t)
	resp := playbackRequest(t, f, http.MethodGet, "/emby/Items/one/PlaybackInfo", "MetadataUA", "viewer")
	var body struct{ MediaSources []map[string]any }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.MediaSources[2]["Path"] != "https://other.example/video.mkv" {
		t.Fatal("外部 CDN 被改写")
	}
	stream := body.MediaSources[1]["DirectStreamUrl"].(string)
	u, err := url.Parse(stream)
	if err != nil {
		t.Fatal(err)
	}
	if u.IsAbs() || u.Query().Get("api_key") != "viewer" || u.Query().Get("MediaSourceId") != "mediasource_b" {
		t.Fatalf("播放地址错误：%s", stream)
	}
	video := playbackRequest(t, f, http.MethodGet, stream, "ActualPlayerUA", "")
	if video.StatusCode != 302 || video.Header.Get("Location") != f.cdn.URL+"/second" {
		t.Fatalf("视频没有重定向：%d", video.StatusCode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for i, ua := range f.uas {
		if ua == "ActualPlayerUA" && f.pcs[i] == "second" {
			found = true
		}
	}
	if !found {
		t.Fatal("实际播放复用了元数据 UA 的直链")
	}
	if f.cdnHits.Load() != 0 || f.embyMedia.Load() != 0 {
		t.Fatal("PlaybackInfo 改写后仍然转流")
	}
}

func TestDirectPlaybackGETHEADAndLegacyID(t *testing.T) {
	f := setupPlaybackFixture(t)
	if err := model.DB.Create(&model.SyncedFile{FileID: "12345", PickCode: "first", RelPath: "first.strm"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/d/first.mkv?/movie.mkv", "/d/12345/movie.mkv"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			resp := playbackRequest(t, f, method, route, "", "")
			if resp.StatusCode != 302 || resp.Header.Get("Location") != f.cdn.URL+"/first" {
				t.Fatalf("%s %s: %d", method, route, resp.StatusCode)
			}
		}
	}
	if f.fetches.Load() != 1 || f.cdnHits.Load() != 0 {
		t.Fatal("旧 fid、扩展名或空 UA 未共用纯直链出口")
	}
}

func TestPlaybackPOSTPreservesUserAndBody(t *testing.T) {
	const body = `{"UserId":"viewer-id","MediaSourceId":"mediasource_b"}`
	req := httptest.NewRequest(http.MethodPost, "https://station.example/Items/movie/PlaybackInfo", strings.NewReader(body))
	req.Header.Set("X-Emby-Token", "post-viewer-token")
	prepared, err := prepareEmbyPlaybackRequest(req, req.URL.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(prepared.Body)
	if err != nil || string(got) != body {
		t.Fatalf("请求体发生变化: %q, %v", got, err)
	}
	stream, err := url.Parse(embyStreamURL(prepared, "movie", "mediasource_b"))
	if err != nil {
		t.Fatal(err)
	}
	if stream.IsAbs() || stream.Query().Get("UserId") != "viewer-id" || stream.Query().Get("api_key") != "post-viewer-token" {
		t.Fatalf("播放入口没有保留用户上下文: %s", stream)
	}
}

func TestPlaybackResolverCoalescesAndExpires(t *testing.T) {
	var calls atomic.Int64
	start := make(chan struct{})
	release := make(chan struct{})
	r := newPlaybackLinkResolver(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		if calls.Add(1) == 1 {
			close(start)
			<-release
		}
		return "https://cdn.example/" + pc, nil, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.resolve(context.Background(), nil, nil, "ABC.mkv", "UA"); err != nil {
				t.Error(err)
			}
		}()
	}
	<-start
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("并发换链 %d 次", calls.Load())
	}
	r.mu.Lock()
	r.cache["abc|UA"] = downloadCacheEntry{URL: "https://expired.example", Expiry: time.Now().Add(-time.Second)}
	r.mu.Unlock()
	if _, err := r.resolve(context.Background(), nil, nil, "abc", "UA"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve(context.Background(), nil, nil, "abc", ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("过期或 UA 变化未换链")
	}
}

func TestPlaybackLinkValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		u string
		h map[string]string
	}{
		{"javascript:alert(1)", nil},
		{"https://cdn.example/video?f=3", nil},
		{"https://cdn.example/video", map[string]string{"Cookie": "secret"}},
		{"https://cdn.example/video", map[string]string{"User-Agent": "different"}},
		{fmt.Sprintf("https://cdn.example/video?t=%d", now.Unix()), nil},
	} {
		if _, err := validatePlaybackLink(tc.u, tc.h, "UA", now); err == nil {
			t.Errorf("接受了不可直连的地址：%s", tc.u)
		}
	}
	if _, err := validatePlaybackLink("https://cdn.example/video?f=1", map[string]string{"Cookie": "optional", "User-Agent": "UA"}, "UA", now); err != nil {
		t.Fatal(err)
	}
}
