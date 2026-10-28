package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 假的 Jellyfin 10.11+：关掉旧式鉴权后，只认 ApiKey 参数或 Authorization: MediaBrowser Token="…"，
// 不认 api_key 与 X-Emby-Token（LitePan TestJellyfinV12AuthAndVirtualFolderFallback 同一场景）
func newModernJellyfin(t *testing.T, key string, hits *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaQuery := r.URL.Query().Get("ApiKey") == key
		viaHeader := r.Header.Get("Authorization") == `MediaBrowser Token="`+key+`"`
		*hits = append(*hits, r.URL.Path)
		if !viaQuery && !viaHeader {
			http.Error(w, "legacy auth disabled", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/Library/VirtualFolders":
			_, _ = w.Write([]byte(`[{"Name":"电影","ItemId":"jf-lib","CollectionType":"movies","Locations":["/media/电影"]}]`))
		case "/System/Info":
			_, _ = w.Write([]byte(`{"ServerName":"jf","Version":"10.11.0"}`))
		case "/Library/MediaFolders":
			_, _ = w.Write([]byte(`{"Items":[{"Id":"jf-lib"}]}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}

func TestEmbyRequestModernJellyfinAuth(t *testing.T) {
	var hits []string
	srv := newModernJellyfin(t, "jk", &hits)
	defer srv.Close()

	ids := embyVirtualFolderIds(srv.URL, "jk")
	if ids["电影"] != "jf-lib" {
		t.Fatalf("新版 Jellyfin 读媒体库失败: %v", ids)
	}
	resp, err := embyRequest(http.MethodPost, srv.URL, "jk", "/Items/jf-lib/Refresh", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("刷新媒体库 HTTP %d", resp.StatusCode)
	}
}

// 只能带查询参数的地址（图片交给通用下载器）：两种写法都在，各家认自己那个
func TestEmbyAuthQueryBothStyles(t *testing.T) {
	q := embyAuthQuery(url.Values{"quality": {"80"}}, "k")
	if q.Get("api_key") != "k" || q.Get("ApiKey") != "k" || q.Get("quality") != "80" {
		t.Fatalf("查询参数不对: %v", q)
	}
	if got := embyAuthQuery(nil, ""); len(got) != 0 {
		t.Fatalf("没有密钥时不该带参数: %v", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	embyAuthHeader(req, `a"b`)
	if req.Header.Get("Authorization") != `MediaBrowser Token="a\"b"` || req.Header.Get("X-Emby-Token") != `a"b` {
		t.Fatalf("请求头不对: %v", req.Header)
	}
}

// 播放器按 Jellyfin 新写法 ?ApiKey= 带令牌，反代要认得出
func TestEmbyClientTokenApiKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/Videos/1/stream?ApiKey=tok", nil)
	if got := embyClientToken(req); got != "tok" {
		t.Fatalf("没认出 ApiKey: %q", got)
	}
	if u := embyStreamURL(req, "1", "s"); !strings.Contains(u, "ApiKey=tok") || !strings.Contains(u, "api_key=tok") {
		t.Fatalf("改写后的地址没带两种令牌: %s", u)
	}
}
