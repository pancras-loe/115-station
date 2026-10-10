package api

import (
	"115-station/internal/config"

	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// re0Call 基础链路：envelope 解析 + OPENAPI_REFRESH_REQUIRED 自动刷新重放
func TestRe0CallAutoRefresh(t *testing.T) {
	refreshCalled := 0
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/public/openapi/oauth/refresh":
			refreshCalled++
			if r.Header.Get("X-API-Key") != "sec" {
				t.Errorf("刷新请求必须携带应用 Secret")
			}
			w.Write([]byte(`{"success":true,"code":"200","data":{"access_token":"newAT","refresh_token":"newRT","expires_in":3600}}`))
		case r.URL.Path == "/api/open/resources/movie/550":
			calls++
			if calls == 1 {
				w.Write([]byte(`{"success":false,"code":"OPENAPI_REFRESH_REQUIRED","message":"token expired"}`))
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer newAT" {
				t.Errorf("重放请求应携带新 Token: got %q", got)
			}
			w.Write([]byte(`{"success":true,"code":"200","data":[{"slug":"abc123","pan_type":"115","unlock_points":30,"is_unlocked":false}]}`))
		default:
			w.Write([]byte(`{"success":false,"code":"NOT_FOUND","message":"no route"}`))
		}
	}))
	defer srv.Close()

	// Token 未到期（排除了「预刷新」分支，专测 401 触发的自动刷新重放）
	cfg := &re0Cfg{BaseURL: srv.URL, ClientSecret: "sec", AccessToken: "oldAT",
		RefreshToken: "rt", TokenExp: time.Now().Add(time.Hour).Unix()}
	var out []re0Resource
	if err := re0Call(nil, cfg, http.MethodGet, "/api/open/resources/movie/550", nil, nil, &out); err != nil {
		t.Fatalf("re0Call: %v", err)
	}
	if refreshCalled != 1 {
		t.Errorf("应刷新一次 Token: %d", refreshCalled)
	}
	if len(out) != 1 || out[0].Slug != "abc123" || out[0].PanType != "115" || out[0].UnlockPoints == nil || *out[0].UnlockPoints != 30 {
		t.Errorf("资源列表解析不符: %+v", out)
	}
	if cfg.AccessToken != "newAT" || cfg.RefreshToken != "newRT" || cfg.TokenExp == 1 {
		t.Errorf("新 Token 应写回配置: %+v", cfg)
	}
}

// 解锁响应解析：full_url / access_code / already_owned
func TestRe0UnlockParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/resources/unlock" {
			w.Write([]byte(`{"success":false,"code":"NOT_FOUND"}`))
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("解锁必须是 POST")
		}
		w.Write([]byte(`{"success":true,"code":"200","message":"解锁成功","data":{"url":"https://115.com/s/abc","access_code":"x1y2","full_url":"https://115.com/s/abc?pwd=x1y2","already_owned":true}}`))
	}))
	defer srv.Close()

	cfg := &re0Cfg{BaseURL: srv.URL, ClientSecret: "sec", AccessToken: "at", TokenExp: 9999999999}
	var data struct {
		URL          string `json:"url"`
		AccessCode   string `json:"access_code"`
		FullURL      string `json:"full_url"`
		AlreadyOwned bool   `json:"already_owned"`
	}
	if err := re0Call(nil, cfg, http.MethodPost, "/api/open/resources/unlock", nil,
		map[string]string{"slug": "abc123"}, &data); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if data.FullURL != "https://115.com/s/abc?pwd=x1y2" || data.AccessCode != "x1y2" || !data.AlreadyOwned {
		t.Errorf("解锁响应解析不符: %+v", data)
	}
}

// 业务错误透出：code + description（如应用未获批）
func TestRe0CallErrorPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"code":"forbidden","message":"Forbidden","description":"开发者应用未审批"}`))
	}))
	defer srv.Close()

	// 新版文档：ping 也要用户 Token（没 Token 时 re0Call 直接报未授权，不发请求）
	cfg := &re0Cfg{BaseURL: srv.URL, ClientSecret: "sec", AccessToken: "at"}
	err := re0Call(nil, cfg, http.MethodGet, "/api/open/ping", nil, nil, nil)
	if err == nil {
		t.Fatal("未获批应用应报错")
	}
	re, ok := err.(*re0Err)
	if !ok || re.Code != "forbidden" || !strings.Contains(re.Error(), "开发者应用未审批") {
		t.Errorf("错误应透出站方 code 与说明: %v", err)
	}
}

// OAuth state：正常消费一次 + 无效/过期拒绝
func TestRe0StateStore(t *testing.T) {
	re0StatePut("st1", "https://strm.example/api/re0/oauth/callback")
	if uri, ok := re0StateTake("st1"); !ok || uri != "https://strm.example/api/re0/oauth/callback" {
		t.Errorf("state 应换回 redirect_uri: ok=%v uri=%q", ok, uri)
	}
	// state 只能用一次
	if _, ok := re0StateTake("st1"); ok {
		t.Errorf("state 应一次性消费")
	}
	if _, ok := re0StateTake("missing"); ok {
		t.Errorf("未知 state 应拒绝")
	}
}

// 出口 IP：只认 trace 里合法的 ip= 行；拿到挑战页 HTML 之类必须报错，不能把垃圾当 IP 给用户填
func TestParseTraceIP(t *testing.T) {
	cases := []struct {
		body, want string
		ok         bool
	}{
		{"fl=1\nh=re0.me\nip=211.20.18.214\nts=1\n", "211.20.18.214", true},
		{"h=re0.me\nip=2001:db8::1\n", "2001:db8::1", true},
		{"ip=\n", "", false},
		{"<!DOCTYPE html><title>Just a moment...</title>", "", false},
	}
	for _, c := range cases {
		got, err := parseTraceIP(c.body)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parseTraceIP(%q) = %q, %v", c.body, got, err)
		}
	}
}

// 保存应用配置：掩码 = 没改动保持旧值，清空 = 真清除，其他 = 新值
// （此前空串也当「没改」，界面上清空 Secret 保存后删不掉）
func TestRe0SaveConfigSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	reset := func() {
		re0CfgMu.Lock()
		re0CfgV, re0CfgAt = nil, time.Time{}
		re0CfgMu.Unlock()
	}
	t.Cleanup(func() { notifyConfigSource = nil; reset() })
	h := &Handler{}
	save := func(secret string) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"base_url":"https://re0.me","client_id":"app_x","client_secret":"` + secret + `"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/re0/config", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Re0SaveConfig(c)
		if w.Code != http.StatusOK {
			t.Fatalf("保存失败: %d %s", w.Code, w.Body)
		}
		reset()
		return loadRe0Cfg().ClientSecret
	}

	if got := save("sec1"); got != "sec1" {
		t.Fatalf("新值应写入: %q", got)
	}
	if got := save(settingMask); got != "sec1" {
		t.Fatalf("掩码回传应保持旧值: %q", got)
	}
	if got := save(""); got != "" {
		t.Fatalf("清空应真清除: %q", got)
	}
}
