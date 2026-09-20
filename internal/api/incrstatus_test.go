package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 状态接口在「什么都还没跑过」时也要能正常返回 —— 用户装好第一次点开就是这个状态
func TestIncrStatusColdStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCfgHandler(t, "status_cold.db")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/sync/incr-status", nil)
	h.IncrStatus(c)

	if w.Code != 200 {
		t.Fatalf("状态接口应返回 200，实得 %d：%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"life_gate", "endpoint", "cursor", "last_round", "pending_events", "path_cache", "interval_sec"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("响应缺字段 %s: %s", k, w.Body.String())
		}
	}
	if got := body["interval_sec"].(float64); got != 30 {
		t.Fatalf("默认间隔应为 30，实得 %v", got)
	}
}

// 积压事件数要如实反映 pending 行 —— 它持续不降是「有目录一直读不出来」的唯一外部信号
func TestIncrStatusReportsPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCfgHandler(t, "status_pending.db")
	model.DB.Create(&model.SyncEvent{EventID: "e1", Status: "pending"})
	model.DB.Create(&model.SyncEvent{EventID: "e2", Status: "applied"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/sync/incr-status", nil)
	h.IncrStatus(c)

	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if got := body["pending_events"].(float64); got != 1 {
		t.Fatalf("应只算 pending 的那条，实得 %v", got)
	}
}
