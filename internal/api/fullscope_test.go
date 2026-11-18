package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

type fullSyncTestTransport func(*http.Request) (*http.Response, error)

func (f fullSyncTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFullSelectionQueueAndExecution(t *testing.T) {
	h, router, cfg := mediaLibsSetup(t)
	router.POST("/sync/full", h.RunFullSync)
	router.POST("/tasks/:id/retry", h.RetryTaskJob)
	account := model.Storage{Name: "副号", Type: "115", Cookie: "UID=123_A1; CID=test; SEID=test"}
	if err := h.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	libs := []model.Library{
		{ID: 1, Name: "电影", Cid: "10", Local: "电影"},
		{ID: 2, Name: "剧集", Cid: "20", Local: "剧集", Account: account.ID},
		{ID: 3, Name: "动画", Cid: "30", Local: "动画", Account: account.ID},
	}
	if err := cfg.SaveSetting("full", `{"detect_orphans":true}`); err != nil {
		t.Fatal(err)
	}
	if err := h.saveMediaLibs(libs); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveSetting(libSyncStatsKey, `{"10":{"at":100,"ok":true,"videos":5}}`); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{`{"library_ids":[]}`, `{"library_ids":null}`, `{"library_ids":[2,99]}`} {
		if code, _ := libHTTPDo(t, router, http.MethodPost, "/sync/full", body); code != http.StatusBadRequest {
			t.Fatalf("无效选库 %s 返回 %d", body, code)
		}
	}
	root := t.TempDir()
	submit := func(ids string) uint {
		t.Helper()
		body := fmt.Sprintf(`{"library_ids":%s,"mode":"normal","local_path":%q,"video_ext":["mkv"]}`, ids, root)
		code, reply := libHTTPDo(t, router, http.MethodPost, "/sync/full", body)
		if code != http.StatusAccepted {
			t.Fatalf("选库 %s 入队失败：%d %v", ids, code, reply)
		}
		return uint(reply["job_id"].(float64))
	}
	h.runScheduledFullSync()
	id := submit("[2,3]")
	if submit("[3,2]") != id || submit("[1]") == id || len(queuedJobs(h.DB)) != 3 {
		t.Fatal("同组任务应合并，定时任务和其他组应分别排队")
	}
	job := jobStatus(t, id)
	params := decodeJobParams(&job)
	if got := strings.Join(jobParamsSummary(params), " "); !strings.Contains(got, "剧集、动画") {
		t.Fatalf("任务详情缺少所选库：%q", got)
	}
	if err := h.DB.Model(&model.Library{}).Where("id = ?", 3).Update("cid", "31").Error; err != nil {
		t.Fatal(err)
	}
	forgetLibraries()

	stale := []model.SyncedFile{
		{FileID: "101", Kind: "video", RelPath: "电影/旧.strm"},
		{FileID: "201", Kind: "video", RelPath: "剧集/旧.strm", Account: account.ID},
		{FileID: "301", Kind: "video", RelPath: "动画/旧.strm", Account: account.ID},
	}
	if err := h.DB.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	event := model.SyncEvent{EventID: "100", EventTime: time.Now().Add(-time.Hour).Unix(), Status: "pending"}
	if err := h.DB.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	requests := 0
	transport := &http.Transport{}
	transport.RegisterProtocol("https", fullSyncTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		w := httptest.NewRecorder()
		if r.URL.Path != "/files" || r.URL.Query().Get("cid") != "20" || r.Header.Get("Cookie") != account.Cookie {
			t.Errorf("选库发出了预期范围外的请求：%s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return w.Result(), nil
		}
		_, _ = io.WriteString(w, `{"state":true,"cid":"20","count":1,"data":[{"f":"1","fid":"202","n":"第一集.mkv","pc":"episode"}]}`)
		return w.Result(), nil
	}))
	previous := http115Transport
	http115Transport = transport
	defer func() { http115Transport = previous }()

	h.runJob(&job, mainLane)
	job = jobStatus(t, id)
	if job.Status != jobPartial || !strings.Contains(job.Message, "动画：媒体库已删除或目录已变更") || requests != 1 {
		t.Fatalf("目录变更后只应扫描剧集：状态 %s，请求 %d，结果 %s", job.Status, requests, job.Message)
	}
	if _, err := os.Stat(filepath.Join(root, "剧集", "第一集.strm")); err != nil {
		t.Fatal(err)
	}
	for i := range stale {
		if err := h.DB.First(&stale[i], stale[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if (stale[i].OrphanAt != nil) != (i == 1) {
			t.Fatalf("只应标记剧集库的失效 STRM：%+v", stale[i])
		}
	}
	stats := parseLibSyncStats(h.getSettingValue(libSyncStatsKey))
	if stats["10"].At != 100 || stats[acctLibKey(account.ID, "20")].LastScan == nil || len(stats) != 2 {
		t.Fatalf("只应更新剧集库的扫描记录：%+v", stats)
	}
	if err := h.DB.First(&event, event.ID).Error; err != nil || event.Status != "pending" || !h.acctLibsSyncedAt().IsZero() {
		t.Fatalf("部分同步不应消耗主号事件或延迟附加账号补同步：%+v %v", event, err)
	}
	updated := submit("[2,3]")
	code, reply := libHTTPDo(t, router, http.MethodPost, fmt.Sprintf("/tasks/%d/retry", id), "")
	if code != http.StatusAccepted {
		t.Fatalf("重试失败：%d %v", code, reply)
	}
	retry := jobStatus(t, uint(reply["job_id"].(float64)))
	if retry.ID == updated {
		t.Fatal("旧任务重试不应覆盖换过目录后重新提交的任务")
	}
	if got := decodeJobParams(&retry).Sync.Libraries; len(got) != 2 || got[1].Cid != "30" {
		t.Fatalf("重试应保留原先选中的范围：%+v", got)
	}
}

func TestFullScopeCoverageAndEventBoundary(t *testing.T) {
	libs := []model.Library{
		{ID: 1, Cid: "10"}, {ID: 2, Cid: "20"},
		{ID: 3, Account: 2, Cid: "10"}, {ID: 4, Account: 3, Cid: "10"},
	}
	refs, _, err := selectFullLibraries(libs, []uint{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	covered := map[uint]fullLibraryRef{1: refs[0], 3: refs[2]}
	if main, attached := fullScopeCoverage(libs, covered); main || attached {
		t.Fatal("账号内漏库或漏掉一个附加账号时，不能写全局状态")
	}
	covered[2] = refs[1]
	if main, attached := fullScopeCoverage(libs, covered); !main || attached {
		t.Fatal("完整覆盖主号时，附加账号仍应独立判断")
	}
	covered[4] = refs[3]
	if main, attached := fullScopeCoverage(libs, covered); !main || !attached {
		t.Fatal("所有库完整覆盖时应允许各自的收尾")
	}

	h, _, _ := mediaLibsSetup(t)
	start := time.Now()
	rows := []model.SyncEvent{
		{EventID: "100", EventTime: start.Unix() - 1, Status: "pending"},
		{EventID: "101", EventTime: start.Unix(), Status: "pending"},
		{EventID: "a2:100", EventTime: start.Unix() - 1, Status: "pending", Account: 2},
	}
	if err := h.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	cursor := lifeCursor{FromID: "100", FromTime: start.Unix() - 1}
	if n, err := h.markEventsCoveredByFullSync(cursor, start); n != 1 || err != nil {
		t.Fatalf("只应标记扫描前的主号事件，实际 %d 条：%v", n, err)
	}
	if got := parseLifeCursor(h.getSettingValue("incr-cursor")); got != cursor {
		t.Fatalf("预期游标 %+v，实际 %+v", cursor, got)
	}
}
