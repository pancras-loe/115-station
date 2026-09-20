package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

func tmdbSetupRequest(t *testing.T, handler gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}

// 前端不传 ID，连续保存后识别必须使用最新密钥，不能继续读取第一条旧记录。
func TestTmdbSetupSaveUpdatesActiveConfig(t *testing.T) {
	newTestDB(t, "tmdb-save.db")
	h := &Handler{DB: model.DB}
	for _, key := range []string{"first", "second"} {
		w := tmdbSetupRequest(t, h.SaveTmdbConfig, `{"api_key":" `+key+` ","api_url":"https://api.tmdb.org"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("保存失败: %s", w.Body.String())
		}
	}
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	if cfg.ApiKey != "second" {
		t.Fatalf("仍在使用旧配置: %q", cfg.ApiKey)
	}
	var count int64
	model.DB.Model(&model.TmdbConfig{}).Count(&count)
	if count != 1 {
		t.Fatalf("保存产生了重复配置: %d", count)
	}
	w := tmdbSetupRequest(t, h.SaveTmdbConfig, `{"api_key":"  "}`)
	if w.Code != http.StatusBadRequest {
		t.Fatal("空密钥不应保存成功")
	}
}

// 缺少密钥应在任何网盘操作之前返回错误，手动和后台入口都要留下失败原因。
func TestTmdbSetupBlocksOrganizeAndReportsFailure(t *testing.T) {
	newTestDB(t, "tmdb-block.db")
	if _, err := loadTmdbClient(); err == nil {
		t.Fatal("未初始化配置必须提示用户")
	}
	if err := model.DB.Create(&model.TmdbConfig{ApiKey: "  "}).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: model.DB}
	w := tmdbSetupRequest(t, h.RunOrganizePipeline, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "TMDB") {
		t.Fatalf("未拒绝缺少配置的整理: %s", w.Body.String())
	}
	runs := GetRecentRuns()
	if len(runs) == 0 || runs[0].OK || !strings.Contains(runs[0].Message, "TMDB") {
		t.Fatalf("错误未进入历史: %+v", runs)
	}
	beginTask("后台整理")
	_, _, err := h.executeOrganizeWithConfig(nil)
	endTask()
	if err == nil || GetRecentRuns()[0].OK {
		t.Fatal("后台入口误报成功")
	}
	beginTask("下一次任务")
	endTask()
	if !GetRecentRuns()[0].OK || GetRecentRuns()[0].Message != "" {
		t.Fatal("失败状态污染下一次任务")
	}
}

func TestTmdbSetupConnectionFeedback(t *testing.T) {
	newTestDB(t, "tmdb-test.db")
	h := &Handler{DB: model.DB}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
		hint   string
	}{
		{"成功", 200, `{"images":{"base_url":"https://image.tmdb.org/"}}`, true, ""},
		{"无效密钥", 401, `{}`, false, "密钥验证失败"},
		{"反代返回网页", 200, `<html>login</html>`, false, "反代"},
		{"服务故障", 503, `{}`, false, "503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/3/configuration" || r.URL.Query().Get("api_key") != "a&b" {
					t.Errorf("测试未使用当前填写的配置: %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			body, _ := json.Marshal(map[string]string{"api_url": srv.URL, "api_key": "a&b"})
			w := tmdbSetupRequest(t, h.TestTMDBConnection, string(body))
			var result struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK != tc.ok || !strings.Contains(result.Error, tc.hint) {
				t.Fatalf("错误反馈不符: %s", w.Body.String())
			}
		})
	}
	if w := tmdbSetupRequest(t, h.TestTMDBConnection, `{"api_key":" "}`); w.Code != http.StatusBadRequest {
		t.Fatal("空密钥必须提前拒绝")
	}
	// 使用已关闭的本地服务验证网络故障，避免访问外网或泄漏请求中的密钥。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	body, _ := json.Marshal(map[string]string{"api_url": srv.URL, "api_key": "private-test-key"})
	w := tmdbSetupRequest(t, h.TestTMDBConnection, string(body))
	if !strings.Contains(w.Body.String(), "无法连接") || strings.Contains(w.Body.String(), "private-test-key") {
		t.Fatalf("网络错误提示不安全或不明确: %s", w.Body.String())
	}
}
