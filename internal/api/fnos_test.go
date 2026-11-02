package api

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// saveBothSetting 同时写 yaml 配置（反代读）与数据库（settingValueCompat 读）
func saveBothSetting(t *testing.T, cfg *config.Config, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if cfg != nil {
		if err := cfg.SaveSetting(key, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	model.DB.Where("key = ?", key).Delete(&model.Setting{})
	if err := model.DB.Create(&model.Setting{Key: key, Value: string(b)}).Error; err != nil {
		t.Fatal(err)
	}
}

func saveEmbySetting(t *testing.T, cfg *config.Config, v any) {
	t.Helper()
	saveBothSetting(t, cfg, "emby", v)
	UpdateEmbyConfig("")
	t.Cleanup(func() { UpdateEmbyConfig("") })
}

// checkFnosAuthX 照签名规则重算一遍，对不上就是签名写错了
func checkFnosAuthX(t *testing.T, r *http.Request, body []byte) {
	t.Helper()
	q, err := url.ParseQuery(r.Header.Get("authx"))
	if err != nil || len(q.Get("nonce")) != 6 || q.Get("timestamp") == "" {
		t.Errorf("authx 格式不对: %q", r.Header.Get("authx"))
		return
	}
	bodySum := md5.Sum(body)
	want := md5.Sum([]byte(strings.Join([]string{fnosSignSecret, r.URL.Path, q.Get("nonce"), q.Get("timestamp"), hex.EncodeToString(bodySum[:]), fnosAPIKey}, "_")))
	if q.Get("sign") != hex.EncodeToString(want[:]) {
		t.Errorf("%s 的 authx 签名对不上", r.URL.Path)
	}
}

func TestFnosAuthXFormat(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v/api/v1/mdb/scan/abc", nil)
	body := []byte(`{"guid":"abc"}`)
	r.Header.Set("authx", fnosAuthX("/v/api/v1/mdb/scan/abc", body, time.UnixMilli(1700000000123)))
	checkFnosAuthX(t, r, body)
	if !strings.Contains(r.Header.Get("authx"), "timestamp=1700000000123") {
		t.Fatalf("时间戳应是毫秒: %s", r.Header.Get("authx"))
	}
}

func TestFnosLibsFor(t *testing.T) {
	libs := []fnosLibrary{
		{GUID: "m", Name: "电影", DirList: []string{"/vol1/1000/strm/电影"}},
		{GUID: "t", Name: "剧集", DirList: []string{"/vol1/1000/strm/剧集", "/vol2/剧集"}},
		{GUID: "x", Name: "别的", DirList: []string{"/vol1/1000/其他"}},
	}
	hit, un := fnosLibsFor([]string{"/vol1/1000/strm/电影/阿凡达 (2009)", "/vol1/1000/strm/剧集/某剧/Season 01/E01.strm", "/tmp/外面"}, libs)
	if len(hit) != 2 || hit[0].GUID != "m" || hit[1].GUID != "t" || len(un) != 1 || un[0] != "/tmp/外面" {
		t.Fatalf("hit=%v unmatched=%v", hit, un)
	}
	// 全量传的是媒体根：根下面的库都扫，根外的不扫
	hit, un = fnosLibsFor([]string{"/vol1/1000/strm"}, libs)
	if len(hit) != 2 || len(un) != 0 {
		t.Fatalf("库之上的路径：hit=%v unmatched=%v", hit, un)
	}
	// 名字前缀相同不算在里面
	if hit, _ = fnosLibsFor([]string{"/vol1/1000/strm/电影2/x"}, libs); len(hit) != 0 {
		t.Fatalf("前缀相同的兄弟目录不该命中: %v", hit)
	}
}

type fakeFnos struct {
	srv     *httptest.Server
	mu      sync.Mutex
	logins  int
	scans   []string
	token   string // 当前有效的 token
	v2Login bool   // 新版登录接口存在
}

func newFakeFnos(t *testing.T) *fakeFnos {
	f := &fakeFnos{token: "tok-1", v2Login: true}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		checkFnosAuthX(t, r, body)
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		reply := func(code int, data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "", "data": data})
		}
		switch r.URL.Path {
		case "/v/api/v2/user/loginByPassword":
			if !f.v2Login {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var in map[string]string
			_ = json.Unmarshal(body, &in)
			// 新版传的是密码的 sha256
			if in["username"] != "admin" || in["password"] != "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918" || in["app_name"] != fnosAppName {
				reply(1, nil)
				return
			}
			f.logins++
			reply(0, map[string]string{"token": f.token})
		case "/v/api/v1/login":
			var in map[string]string
			_ = json.Unmarshal(body, &in)
			if in["username"] != "admin" || in["password"] != "admin" {
				reply(1, nil)
				return
			}
			f.logins++
			reply(0, map[string]string{"token": f.token})
		case "/v/api/v1/mdb/list":
			if r.Header.Get("Authorization") != f.token {
				reply(-1, nil) // 登录态失效时飞牛回的是业务错误码
				return
			}
			reply(0, []map[string]any{
				{"guid": "g-movie", "name": "电影", "dir_list": []string{"/vol1/strm/电影"}},
				{"guid": "g-tv", "name": "剧集", "dir_list": []string{"/vol1/strm/剧集"}},
			})
		case "/v/api/v1/mediadb/sum":
			reply(0, map[string]int{"movie": 12, "tv": 3, "total": 15})
		default:
			if strings.HasPrefix(r.URL.Path, "/v/api/v1/mdb/scan/") && r.Method == http.MethodPost && r.Header.Get("Authorization") == f.token {
				f.scans = append(f.scans, strings.TrimPrefix(r.URL.Path, "/v/api/v1/mdb/scan/"))
				reply(0, true)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func resetFnosState(t *testing.T) {
	fnosTokens.Lock()
	fnosTokens.m = map[string]string{}
	fnosTokens.Unlock()
	old := fnosScanAfter
	fnosScanAfter = func(_ time.Duration, f func()) *time.Timer { f(); return nil }
	t.Cleanup(func() { fnosScanAfter = old })
}

func TestFnosNotifyScansMatchingLibrary(t *testing.T) {
	newTestDB(t, "fnos_notify.db")
	resetFnosState(t)
	f := newFakeFnos(t)
	saveEmbySetting(t, nil, map[string]any{"kind": "fnos", "server_url": f.srv.URL + "/v", "fnos_username": "admin", "fnos_password": "admin",
		"path_mapping": "/media#/vol1/strm"})

	notifyEmbyPaths([]string{"/media/剧集/某剧/Season 01/E01.strm"}, embyRefreshAdded)
	if len(f.scans) != 1 || f.scans[0] != "g-tv" {
		t.Fatalf("应只扫剧集库: %v", f.scans)
	}
	// token 失效：飞牛回业务错误码 → 重登一次再来
	f.mu.Lock()
	f.token = "tok-2"
	f.mu.Unlock()
	notifyEmbyDeleted("/media/电影/阿凡达 (2009)/阿凡达.strm")
	if len(f.scans) != 2 || f.scans[1] != "g-movie" || f.logins != 2 {
		t.Fatalf("token 失效后应重登再扫: scans=%v logins=%d", f.scans, f.logins)
	}
	// 关了入库刷新就不扫
	saveEmbySetting(t, nil, map[string]any{"kind": "fnos", "server_url": f.srv.URL, "fnos_username": "admin", "fnos_password": "admin",
		"path_mapping": "/media#/vol1/strm", "refresh_enabled": false})
	notifyEmbyPaths([]string{"/media/剧集/x.strm"}, embyRefreshAdded)
	if len(f.scans) != 2 {
		t.Fatalf("关了刷新还在扫: %v", f.scans)
	}
}

func TestFnosLoginFallsBackToV1(t *testing.T) {
	newTestDB(t, "fnos_v1.db")
	resetFnosState(t)
	f := newFakeFnos(t)
	f.v2Login = false
	out := testFnosConnection(fnosCfg{Kind: msFnos, ServerURL: f.srv.URL, Username: "admin", Password: "admin"})
	if out["ok"] != true || out["library_count"] != 2 || out["movies"] != 12 {
		t.Fatalf("测试连接: %v", out)
	}
	resetFnosState(t)
	out = testFnosConnection(fnosCfg{Kind: msFnos, ServerURL: f.srv.URL, Username: "admin", Password: "wrong"})
	if out["ok"] != false || !strings.Contains(out["error"].(string), "账号和密码") {
		t.Fatalf("密码错应报登录失败: %v", out)
	}
}

// 飞牛下 Emby 那套管理接口一律按没配处理，做不了的功能写明原因
func TestFnosGatesEmbyAPI(t *testing.T) {
	newTestDB(t, "fnos_gate.db")
	saveEmbySetting(t, nil, map[string]any{"kind": "fnos", "server_url": "http://fn:5666", "api_key": "x"})
	if _, ok := loadEmbyRefreshCfg(); ok {
		t.Fatal("飞牛下不该当成配了 Emby")
	}
	if _, _, ok := (&Handler{DB: model.DB}).embyServerInfo(); ok {
		t.Fatal("飞牛下 embyServerInfo 不该可用")
	}
	if mediaServerBlock(msFeatDeepDelete) == "" || mediaServerBlock(msFeatPreProbe) == "" {
		t.Fatal("飞牛下深删 / 提前探测应被拦")
	}
	if mediaServerURL() != "http://fn:5666" {
		t.Fatalf("反代与豁免要拿到地址: %q", mediaServerURL())
	}
	if got := fnosEmbyAPIBase("http://fn:5666/v/"); got != "http://fn:5666/emby" {
		t.Fatalf("Emby 兼容接口根: %q", got)
	}
}

// 第三方 Emby 客户端连 6086 → 转进飞牛的 /emby；飞牛的用户对象没有 Policy 也能播，最后 302 到 CDN
func TestFnosProxyRedirectsThirdPartyClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTestDB(t, "fnos_proxy.db")
	root := t.TempDir()
	cfg := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	saveBothSetting(t, cfg, "full", map[string]string{"local_path": root})
	saveBothSetting(t, cfg, "strm", map[string]string{"domain": "https://station.example"})
	if err := os.WriteFile(filepath.Join(root, "a.strm"), []byte("https://station.example/d/fnpick.mkv?/a.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	var upstreamPaths []string
	var mu sync.Mutex
	fn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamPaths = append(upstreamPaths, r.URL.Path)
		mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/emby/") {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<!doctype html><html>飞牛网页</html>")
			return
		}
		if embyClientToken(r) != "fn-user" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/emby")
		switch {
		case strings.HasSuffix(p, "/PlaybackInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []map[string]any{{"Id": "ms1", "Path": "/vol1/strm/a.strm", "SupportsDirectPlay": true}}})
		case p == "/Users/u1":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "u1", "Name": "家人"}) // 没有 Policy
		case p == "/Users/u1/Items/i1":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "i1", "Path": "/vol1/strm/a.strm", "MediaSources": []map[string]any{{"Id": "ms1", "Path": "/vol1/strm/a.strm"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fn.Close()
	saveEmbySetting(t, cfg, map[string]any{"kind": "fnos", "server_url": fn.URL, "fnos_username": "admin", "fnos_password": "admin", "path_mapping": "#/vol1/strm"})

	oldGrants := embyPlayGrants
	embyPlayGrants = &playGrantCache{m: map[string]playGrant{}}
	t.Cleanup(func() { embyPlayGrants = oldGrants })
	old := playbackLinks
	playbackLinks = newPlaybackLinkResolver(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		return "https://cdn.example/" + pc, map[string]string{"User-Agent": ua}, nil
	})
	t.Cleanup(func() { playbackLinks = old })

	r := gin.New()
	registerDirectPlaybackRoutes(r, model.DB, cfg)
	registerEmbyProxy(r, model.DB, cfg)
	front := httptest.NewServer(r)
	defer front.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// PlaybackInfo 不带 /emby 发来也要转进 /emby，并改写成本站视频入口
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/Items/i1/PlaybackInfo?UserId=u1", strings.NewReader("{}"))
	req.Header.Set("X-Emby-Token", "fn-user")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		MediaSources []map[string]any
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if len(info.MediaSources) != 1 || !strings.HasPrefix(fmt.Sprint(info.MediaSources[0]["DirectStreamUrl"]), "/Videos/i1/stream?") {
		t.Fatalf("PlaybackInfo 没改写: %+v", info)
	}

	req, _ = http.NewRequest(http.MethodGet, front.URL+"/emby/Videos/i1/stream?Static=true&MediaSourceId=ms1&UserId=u1&api_key=fn-user", nil)
	req.Header.Set("User-Agent", "VidHub")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://cdn.example/fnpick" {
		t.Fatalf("应 302 到 CDN: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range upstreamPaths {
		if !strings.HasPrefix(p, "/emby/") {
			t.Fatalf("有请求没转进 /emby: %v", upstreamPaths)
		}
	}
}

// Jellyfin：Authorization: MediaBrowser Token="…" 认得出，/Items/{id}/File 也走 302
func TestJellyfinItemFileRedirects(t *testing.T) {
	f := setupPlaybackFixture(t)
	r, _ := http.NewRequest(http.MethodGet, f.front.URL+"/Items/item1/File?MediaSourceId=mediasource_a", nil)
	r.Header.Set("Authorization", `MediaBrowser Client="Jellyfin Web", Device="Chrome", DeviceId="d", Version="10.11.0", Token="viewer"`)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), f.cdn.URL) {
		t.Fatalf("Items/File 应 302: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if f.embyMedia.Load() != 0 {
		t.Fatal("流量不该回源到媒体服务器")
	}
}

// Jellyfin 的 WebSocket 在 /socket（Emby 是 /embywebsocket），走根路径透传，升级握手要原样过去
func TestJellyfinSocketPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTestDB(t, "jf_socket.db")
	cfg := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.URL.Query().Get("api_key") != "tok" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		line, _ := buf.ReadString('\n')
		_, _ = buf.WriteString("echo:" + line)
		_ = buf.Flush()
	}))
	defer jf.Close()
	saveEmbySetting(t, cfg, map[string]any{"kind": "jellyfin", "server_url": jf.URL, "api_key": "admin"})
	r := gin.New()
	registerEmbyProxy(r, model.DB, cfg)
	front := httptest.NewServer(r)
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /socket?api_key=tok&deviceId=d HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("握手没透传: %d", resp.StatusCode)
	}
	_, _ = io.WriteString(conn, "ping\n")
	line, _ := br.ReadString('\n')
	if line != "echo:ping\n" {
		t.Fatalf("升级后的双向数据没通: %q", line)
	}
}
