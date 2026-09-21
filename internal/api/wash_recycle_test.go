package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

type washTransport func(*http.Request) (*http.Response, error)

func (f washTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 用真实 ops 和增量主流程验证闭环，HTTP 只接受回收站请求，绝不访问真实网盘。
func TestWashRecycleAndIncrementalDoNotDeleteNewVersion(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "wash_recycle.db")
	if err := model.DB.Create(&model.Setting{Key: "full", Value: `{"local_path":"` + filepath.ToSlash(p.LocalPath) + `"}`}).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.SyncedFile{
		{FileID: "old", Kind: "video", RelPath: "媒体库/剧集/X/剧.S01E01.2160p.mkv.strm"},
		{FileID: "sub", RelPath: "媒体库/剧集/X/剧.S01E01.2160p.chs.srt"},
		{FileID: "other", Kind: "video", RelPath: "媒体库/剧集/X/剧.S01E02.2160p.mkv.strm"},
	}
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(p.LocalPath, filepath.FromSlash(rows[i].RelPath))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("旧版"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	appVerMu.Lock()
	previousVerTime := appVerTime
	appVerTime = time.Now()
	appVerMu.Unlock()
	t.Cleanup(func() {
		appVerMu.Lock()
		appVerTime = previousVerTime
		appVerMu.Unlock()
	})
	requests := 0
	http.DefaultTransport = washTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method != "POST" || r.URL.String() != "https://webapi.115.com/rb/delete" {
			t.Fatalf("意外请求: %s %s", r.Method, r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("fid[0]") != "old" || r.Form.Get("fid[1]") != "sub" || len(r.Form) != 2 {
			t.Fatalf("删除范围错误: %v", r.Form)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"state":true}`))}, nil
	})
	notifications := 0
	notify := func(paths ...string) {
		notifications++
		if len(paths) != 2 || !peekSuppressed("old") || !peekSuppressed("sub") {
			t.Fatal("通知前未登记抑制或路径不完整")
		}
		var count int64
		model.DB.Model(&model.SyncedFile{}).Where("file_id IN ?", []string{"old", "sub"}).Count(&count)
		if count != 0 {
			t.Fatal("通知 Emby 前未清台账")
		}
		for _, path := range paths {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("通知前旧文件仍存在: %s", path)
			}
		}
	}
	got := runWashReplaceWithNotify(&pan115Ops{suppress: true}, &OrgConfig{}, &TmdbMedia{MediaType: "tv"}, "剧.S01E01.1080p.mkv", "剧集/X", &washStrategy{Mode: "replace", OldVersionTarget: "delete"}, rows, quiet, notify)
	if got != washReplaced || requests != 1 || notifications != 1 {
		t.Fatalf("结果 %s，请求 %d，通知 %d", got, requests, notifications)
	}
	// 新版可以落到同一路径；旧版删除事件不能凭路径兜底把它删掉。
	newPath := filepath.Join(p.LocalPath, filepath.FromSlash(rows[0].RelPath))
	if err := os.WriteFile(newPath, []byte("新版"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := model.DB.Create(&model.SyncedFile{FileID: "new", Kind: "video", RelPath: rows[0].RelPath}).Error; err != nil {
		t.Fatal(err)
	}
	d.pages = [][]lifeEvent{{
		{ID: "delete-old", Type: evDelete, FileID: "old", Cid: "d1", FileName: "剧.S01E01.2160p.mkv", Time: "100"},
		{ID: "delete-sub", Type: evDelete, FileID: "sub", Cid: "d1", FileName: "剧.S01E01.2160p.chs.srt", Time: "101"},
	}}
	for i := 0; i < 2; i++ {
		sum, err := h.executeIncrementalSyncWith(d, p)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && sum.Suppressed != 2 {
			t.Fatalf("未抑制删除事件: %+v", sum)
		}
		if sum.Deleted != 0 || d.walkCalls != 0 || len(d.deleted)+len(d.refreshed) != 0 {
			t.Fatal("增量重复删除、遍历或通知")
		}
	}
	if peekSuppressed("old") {
		t.Fatal("事件应用后标记未清理")
	}
	if data, err := os.ReadFile(newPath); err != nil || string(data) != "新版" {
		t.Fatal("同路径新版被误删")
	}
	if _, err := os.Stat(filepath.Join(p.LocalPath, filepath.FromSlash(rows[2].RelPath))); err != nil {
		t.Fatal("其他集被误删")
	}
}

func TestWashNoPriorityStillRespectsScope(t *testing.T) {
	for _, tc := range []struct {
		media, name, scope, mode string
		want                     []string
	}{
		{"movie", "片.1080p.mkv", "all", "replace", []string{"a", "b"}},
		{"movie", "片.1080p.mkv", "group", "replace", []string{"b"}},
		{"movie", "片.1080p.mkv", "all", "", []string{"a", "b"}},
		{"tv", "剧.S01E01.1080p.mkv", "all", "replace", []string{"a"}},
		{"tv", "剧.S01E03.1080p.mkv", "all", "replace", nil},
	} {
		ops := &washFailOps{}
		rows := []model.SyncedFile{{FileID: "a", RelPath: "剧.S01E01.2160p.strm"}, {FileID: "b", RelPath: "剧.S01E02.1080p.strm"}}
		got := runWashReplace(ops, &OrgConfig{}, &TmdbMedia{MediaType: tc.media}, tc.name, "片", &washStrategy{Mode: tc.mode, Scope: tc.scope}, rows, quiet)
		wantResult := washFailed
		if tc.want == nil {
			wantResult = washSkip
		}
		if got != wantResult || !reflect.DeepEqual(ops.moved, tc.want) {
			t.Fatalf("%+v: %s %v", tc, got, ops.moved)
		}
	}
}

func TestWashRecycleFailurePreservesLedger(t *testing.T) {
	washTestDB(t)
	row := model.SyncedFile{FileID: "old", RelPath: "片.2160p.strm"}
	if err := model.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	notified := false
	got := runWashReplaceWithNotify(&washFailOps{}, &OrgConfig{}, &TmdbMedia{MediaType: "movie"}, "片.1080p.mkv", "片", &washStrategy{Mode: "replace", OldVersionTarget: "delete"}, []model.SyncedFile{row}, quiet, func(...string) { notified = true })
	if got != washFailed || notified {
		t.Fatal("回收站失败仍继续整理或通知")
	}
	if err := model.DB.First(&model.SyncedFile{}, row.ID).Error; err != nil {
		t.Fatal("回收站失败清除了台账")
	}
}

type washSuccessOps struct{ washFailOps }

func (*washSuccessOps) deleteFiles([]string) error { return nil }

func TestWashLocalCleanupFailureKeepsLedgerAndStops(t *testing.T) {
	washTestDB(t)
	root := t.TempDir()
	if err := model.DB.Create(&model.Setting{Key: "full", Value: `{"local_path":"` + filepath.ToSlash(root) + `"}`}).Error; err != nil {
		t.Fatal(err)
	}
	row := model.SyncedFile{FileID: "old", Kind: "video", RelPath: "片/旧版.strm"}
	if err := model.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	// 非空目录模拟本地删除失败，Windows/Linux 均稳定，不能依赖 root 下的权限位。
	blocked := filepath.Join(root, filepath.FromSlash(row.RelPath))
	if err := os.MkdirAll(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "占位"), []byte("保留"), 0644); err != nil {
		t.Fatal(err)
	}
	notified := false
	got := runWashReplaceWithNotify(&washSuccessOps{}, &OrgConfig{}, &TmdbMedia{MediaType: "movie"}, "新版.mkv", "片", &washStrategy{Mode: "replace", OldVersionTarget: "delete"}, []model.SyncedFile{row}, quiet, func(...string) { notified = true })
	if got != washFailed || notified {
		t.Fatal("本地删除失败后仍继续入库或通知")
	}
	if err := model.DB.First(&model.SyncedFile{}, row.ID).Error; err != nil {
		t.Fatal("本地删除失败却清除了台账")
	}
}
