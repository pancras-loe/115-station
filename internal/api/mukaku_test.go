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
