package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

const oneVideo = `[{"fid":"v1","name":"a.mkv","kind":"video"}]`

// 提交怎么拆：暂存的一条一个任务；未暂存已识别的待确认合成批量确认；其余跳过并说明
func TestPlanSubmit(t *testing.T) {
	recs := []model.OrganizeRecord{
		{ID: 1, Status: "success", Files: oneVideo, PendingTmdbID: 10, PendingMediaType: "movie"}, // 暂存 → redo
		{ID: 2, Status: orgStatusAwaiting, PendingTmdbID: 20, PendingMediaType: "tv"},             // 暂存 → confirm（改指定）
		{ID: 3, Status: orgStatusAwaiting, TmdbID: 30},                                            // 批量确认
		{ID: 4, Status: orgStatusAwaiting, TmdbID: 40},                                            // 批量确认
		{ID: 5, Status: orgStatusAwaiting},                                                        // 未识别、没暂存：跳过
		{ID: 6, Status: "unrecognized", Files: oneVideo},                                          // 没暂存：跳过
		{ID: 7, Status: "failed", PendingTmdbID: 70, PendingMediaType: "movie"},                   // 暂存了但没文件：跳过
	}
	p := planSubmit(recs)
	if len(p.staged) != 2 || p.staged[0].ID != 1 || p.staged[1].ID != 2 {
		t.Fatalf("暂存的应是 1、2：%+v", p.staged)
	}
	if len(p.confirmIDs) != 2 || p.confirmIDs[0] != 3 || p.confirmIDs[1] != 4 {
		t.Fatalf("批量确认应是 3、4：%v", p.confirmIDs)
	}
	if len(p.reasons) != 3 {
		t.Fatalf("三种跳过原因各一条（去重），实际 %v", p.reasons)
	}
}

func stageRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/records/:id/pending", h.SetRecordPending)
	r.POST("/records/submit", h.SubmitOrganizeRecords)
	return r
}

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// 暂存 → 提交：暂存落库、提交入队后清掉暂存；同一条记录重复提交只排一次
func TestStageAndSubmit(t *testing.T) {
	newTestDB(t, "stage.db")
	h := &Handler{DB: model.DB}
	r := stageRouter(h)
	a := model.OrganizeRecord{Source: "甲/", Status: "unrecognized", Files: oneVideo}
	b := model.OrganizeRecord{Source: "乙.mkv", Status: orgStatusAwaiting, TmdbID: 5}
	model.DB.Create(&a)
	model.DB.Create(&b)

	if w := doJSON(r, http.MethodPut, "/records/1/pending", `{"tmdb_id":99,"media_type":"tv","label":"某剧 (2020)"}`); w.Code != http.StatusOK {
		t.Fatalf("暂存失败 %d %s", w.Code, w.Body)
	}
	var got model.OrganizeRecord
	model.DB.First(&got, a.ID)
	if got.PendingTmdbID != 99 || got.PendingMediaType != "tv" || got.PendingLabel != "某剧 (2020)" {
		t.Fatalf("暂存没落库：%+v", got)
	}
	if w := doJSON(r, http.MethodPut, "/records/1/pending", `{"tmdb_id":99,"media_type":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 media_type 应 400，实际 %d", w.Code)
	}

	w := doJSON(r, http.MethodPost, "/records/submit", `{"ids":[1,2]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("提交应 202，实际 %d %s", w.Code, w.Body)
	}
	q := queuedJobs(model.DB)
	if len(q) != 2 {
		t.Fatalf("应入队 2 个任务（1 条重新整理 + 1 个批量确认），实际 %d", len(q))
	}
	kinds := map[string]jobParams{}
	for i := range q {
		kinds[q[i].Kind] = decodeJobParams(&q[i])
	}
	if p := kinds["redo"]; p.TmdbID != 99 || p.MediaType != "tv" || len(p.RecordIDs) != 1 || p.RecordIDs[0] != a.ID {
		t.Fatalf("重新整理任务参数不对：%+v", p)
	}
	if p := kinds["confirm"]; p.TmdbID != 0 || len(p.RecordIDs) != 1 || p.RecordIDs[0] != b.ID {
		t.Fatalf("批量确认任务参数不对：%+v", p)
	}
	model.DB.First(&got, a.ID)
	if got.PendingTmdbID != 0 || got.PendingLabel != "" {
		t.Fatalf("提交后应清掉暂存：%+v", got)
	}

	// 再暂存一次、再提交：同一条记录排队中的任务被覆盖，不重复入队
	doJSON(r, http.MethodPut, "/records/1/pending", `{"tmdb_id":100,"media_type":"movie","label":"某片"}`)
	doJSON(r, http.MethodPost, "/records/submit", `{"ids":[1]}`)
	var redo []model.TaskJob
	model.DB.Where("kind = ? AND status = ?", "redo", jobQueued).Find(&redo)
	if len(redo) != 1 || decodeJobParams(&redo[0]).TmdbID != 100 {
		t.Fatalf("同一条记录应只排一次、以最后一次指定为准：%d 条", len(redo))
	}

	// 撤销暂存
	doJSON(r, http.MethodPut, "/records/1/pending", `{"tmdb_id":7,"media_type":"movie"}`)
	doJSON(r, http.MethodPut, "/records/1/pending", `{}`)
	model.DB.First(&got, a.ID)
	if got.PendingTmdbID != 0 {
		t.Fatal("空 body 应撤销暂存")
	}

	// 什么都提交不了：400 并说明原因
	if w := doJSON(r, http.MethodPost, "/records/submit", `{"ids":[1]}`); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "指定") {
		t.Fatalf("没有可提交内容应 400 并说明，实际 %d %s", w.Code, w.Body)
	}
}
