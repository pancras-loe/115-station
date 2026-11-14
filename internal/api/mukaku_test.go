package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 站点开了 GoEdge WAF 时，接口 307 跳到滑块验证页：要报成人机验证，别把 HTML 原文甩给用户
func TestMukakuWAFPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/WAF/VERIFY/CAPTCHA" {
			w.Write([]byte(`<!DOCTYPE html><html><head><title>Verify Yourself</title></head><body><form method="POST" id="ui-form"><input type="hidden" name="GOEDGE_WAF_CAPTCHA_ID" value="x"/></form></body></html>`))
			return
		}
		http.Redirect(w, r, "/WAF/VERIFY/CAPTCHA?info=abc&from=%2F", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	cfg := &mukakuCfg{BaseURL: srv.URL, AppID: mukakuAppID, Identity: mukakuIdentity}
	full := srv.URL + "/prod/api/v1/getCaptcha"
	if _, _, err := mukakuHTTPDo(cfg, http.MethodGet, full, nil); !errors.Is(err, errMukakuWAF) {
		t.Fatalf("应认成 WAF 验证页，得到 %v", err)
	}
}

// 2026-11-14 现场：GoEdge 先回一页 JS 写 Cookie 再刷新的页面（200 + text/html），
// 照写 Cookie 重发就放行；原来当 JSON 解析，报「响应解析失败: <!DOCTYPE html>…」
func TestMukakuJSCookieValidator(t *testing.T) {
	const page = `<!DOCTYPE html>
<html>
<head>
<title></title>
<meta charset="UTF-8"/>
<script type="text/javascript">
document.cookie = "ge_js_validator_18=1794643786@18@7bd4b9a1c51154c5dfb98ea1ece41304; path=/; max-age=99999;";
window.location.reload();
</script>
</head>
<body>
</body>
</html>`
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if c, err := r.Cookie("ge_js_validator_18"); err == nil && c.Value == "1794643786@18@7bd4b9a1c51154c5dfb98ea1ece41304" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"code":200,"success":true,"message":"ok","data":{"key":"k","img":"data:image/jpeg;base64,AA"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()
	cfg := &mukakuCfg{BaseURL: srv.URL, AppID: mukakuAppID, Identity: mukakuIdentity}
	data, err := mukakuAPI(cfg, http.MethodGet, "getCaptcha", nil, nil)
	if err != nil {
		t.Fatalf("应写 Cookie 后放行，得到 %v", err)
	}
	if string(data) != `{"key":"k","img":"data:image/jpeg;base64,AA"}` || hits != 2 {
		t.Fatalf("data=%s hits=%d", data, hits)
	}
	if mukakuJSCookies(`{"msg":"ge_js_validator"}`) != nil {
		t.Fatal("JSON 响应不该认成验证页")
	}
}

func TestMukakuCleanToken(t *testing.T) {
	for in, want := range map[string]string{
		" abc ":        "abc",
		`"abc"`:        "abc",
		"Bearer abc":   "abc",
		`"Bearer abc"`: "abc",
		"":             "",
	} {
		if got := mukakuCleanToken(in); got != want {
			t.Errorf("%q → %q，应为 %q", in, got, want)
		}
	}
}
