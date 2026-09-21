package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// setupEmbyProxy 起一个假 Emby（只回 PlaybackInfo）+ 本站反代，返回反代地址与 strm 的绝对路径
func setupEmbyProxy(t *testing.T) (proxyURL, strmPath string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	strmPath = filepath.Join(dir, "某片.mkv.strm")
	if err := os.WriteFile(strmPath, []byte("http://旧域名:6060/d/abc123.mkv?/某片.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(strings.ToLower(r.URL.Path), "/playbackinfo") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"MediaSources": []map[string]any{
				{"Path": strmPath, "Protocol": "File", "SupportsDirectStream": true, "SupportsTranscoding": true},
			},
		})
	}))
	t.Cleanup(emby.Close)

	// 真 DB：改写命中后会起一个预取协程打 115 取直链，
	// 传 nil 会在那条协程里 panic（生产上 db 永远不为 nil）
	newTestDB(t, "embyproxy.db")

	cfg := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	if err := cfg.SaveSetting("emby", `{"server_url":"`+emby.URL+`","api_key":"k"}`); err != nil {
		t.Fatal(err)
	}
	UpdateEmbyConfig("") // 清掉上一个用例留在包级变量里的地址缓存
	t.Cleanup(func() { UpdateEmbyConfig("") })

	r := gin.New()
	registerEmbyProxy(r, model.DB, cfg)
	front := httptest.NewServer(r)
	t.Cleanup(front.Close)
	return front.URL, strmPath
}

// 拿到改写后的 MediaSources[0]
func fetchRewritten(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	var out struct {
		MediaSources []map[string]any `json:"MediaSources"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.MediaSources) == 0 {
		t.Fatalf("响应解析失败: %s", body)
	}
	return out.MediaSources[0]
}

func assertDirectPlay(t *testing.T, ms map[string]any) {
	t.Helper()
	p, _ := ms["Path"].(string)
	if !strings.Contains(p, "/d/abc123.mkv") {
		t.Fatalf("Path 没改写成直链: %v", ms["Path"])
	}
	if ms["SupportsDirectStream"] != false {
		t.Fatalf("DirectStream 没关掉（会退回服务器转码）: %v", ms)
	}
	if ms["SupportsTranscoding"] != false {
		t.Fatalf("Transcoding 没关掉: %v", ms)
	}
}

// /emby 前缀那条：原本就在工作，作为对照组
func TestEmbyProxyRewritesUnderEmbyPrefix(t *testing.T) {
	proxyURL, _ := setupEmbyProxy(t)
	assertDirectPlay(t, fetchRewritten(t, proxyURL+"/emby/Items/1/PlaybackInfo"))
}

// 根路径那条：首页与文档都教用户把服务器地址填成 http://ip:6086/，
// 但它此前是另写的一份反代、没挂 ModifyResponse —— 直连改写整条不生效，
// 播放照样走 Emby 转码
func TestEmbyProxyRewritesAtRootPath(t *testing.T) {
	proxyURL, _ := setupEmbyProxy(t)
	assertDirectPlay(t, fetchRewritten(t, proxyURL+"/Items/1/PlaybackInfo"))
}

// 直链主机按客户端访问反代用的地址改写：strm 里残留的旧域名不该影响播放
func TestEmbyProxyRewritesHostToClientAddress(t *testing.T) {
	proxyURL, _ := setupEmbyProxy(t)
	ms := fetchRewritten(t, proxyURL+"/Items/1/PlaybackInfo")
	p, _ := ms["Path"].(string)
	if strings.Contains(p, "旧域名") {
		t.Fatalf("strm 里的旧域名没被改掉: %s", p)
	}
	if !strings.HasPrefix(p, proxyURL) {
		t.Fatalf("直链主机不是客户端访问的地址：%s（期望前缀 %s）", p, proxyURL)
	}
}
