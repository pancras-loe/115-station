package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 回归：刷新页面后表单里的 api_key 是掩码，点「测试连接」发来的就是掩码本身，
// 原样拿去请求 Emby 必然 401 —— 要换成已保存的密钥
func TestEmbyConnectionMaskedKeyUsesSaved(t *testing.T) {
	defer func() { notifyConfigSource = nil; model.DB = nil }()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	if err := notifyConfigSource.SaveSetting("emby", `{"server_url":"http://x","api_key":"real-key-123"}`); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	var gotKey string
	emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		if gotKey != "real-key-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"ServerName":"e","Version":"4"}`))
	}))
	defer emby.Close()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"server_url":"` + emby.URL + `","api_key":"` + settingMask + `"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/config/test-emby", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	(&Handler{}).TestEmbyConnection(c)

	if gotKey != "real-key-123" || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("api_key 发出去的是 %q，响应 %s", gotKey, w.Body.String())
	}
}
