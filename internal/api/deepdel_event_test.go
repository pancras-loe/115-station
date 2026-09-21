package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

func eventPayload(root, rel string) map[string]interface{} {
	return map[string]interface{}{"Item": map[string]interface{}{"Path": filepath.ToSlash(filepath.Join(root, rel)), "Type": "Movie"}}
}

func eventTestHandler(t *testing.T, root string, rows []model.SyncedFile) *Handler {
	t.Helper()
	h := deepDelEmbyTestDB(t, root, rows)
	h.DB.Model(&model.Setting{}).Where("key = ?", "deepdel").Update("value", `{"enabled":true,"max_ratio":1,"notify":false}`)
	h.DB.Exec("DELETE FROM deep_delete_records")
	return h
}

// 本次精确路径不能带入同目录其他版本、兄弟目录以及旧扫描留下的标记。
func TestDeepDelEventOnlyDeletesMatchedRows(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	past := time.Now()
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "target", RelPath: "影视/电影/A/a.strm", Kind: "video"},
		{FileID: "version", RelPath: "影视/电影/A/b.strm", Kind: "video"},
		{FileID: "other", RelPath: "影视/电影/B/b.strm", Kind: "video", VanishAt: &past},
	})
	calls := 0
	execute := func(rows []model.SyncedFile, reason string) (deepDelResult, error) {
		calls++
		if len(rows) != 1 || rows[0].FileID != "target" || reason != "emby_webhook" {
			t.Fatalf("事件范围错误: %+v", rows)
		}
		h.DB.Delete(&model.SyncedFile{}, rows[0].ID)
		return deepDelResult{}, nil
	}
	payload := eventPayload(root, "影视/电影/A/a.strm")
	h.processDeepDelEvent(payload, true, execute, func() bool { return true })
	// 神医与原生重复到达：台账已清，不再次执行，也不影响其他缺失文件。
	h.processDeepDelEvent(payload, false, execute, func() bool { return true })
	if calls != 1 {
		t.Fatalf("执行次数=%d", calls)
	}
	var left int64
	h.DB.Model(&model.SyncedFile{}).Count(&left)
	if left != 2 {
		t.Fatalf("无关台账被删除: %d", left)
	}
}

func TestDeepDelEventPrefixPickcodeAndOrphan(t *testing.T) {
	root := deepDelTestTree(t, "影视/剧集/B/S01/keep.strm")
	past := time.Now()
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/剧集/B/S01/a.strm", Kind: "video", PickCode: "abc"},
		{FileID: "b", RelPath: "影视/剧集/B/tvshow.nfo", Kind: "asset"},
		{FileID: "keep", RelPath: "影视/剧集/B/S01/keep.strm", Kind: "video"},
		{FileID: "other", RelPath: "影视/剧集/BB/a.strm", Kind: "video"},
		{FileID: "orphan", RelPath: "影视/剧集/B/orphan.strm", Kind: "video", OrphanAt: &past},
	})
	rows, matched, _, err := h.deepDelEventRows([]string{"影视/剧集/B"}, []string{"abc"})
	if err != nil || len(rows) != 2 || matched != 3 {
		t.Fatalf("范围/去重/失效筛选错误: %v %d %+v", err, matched, rows)
	}
	for _, row := range rows {
		if row.FileID != "a" && row.FileID != "b" {
			t.Fatalf("选入无关文件: %+v", row)
		}
	}
}

func TestDeepDelEventRejectsUnavailableLibraryAndRootLocator(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.strm", Kind: "video"},
		{FileID: "b", RelPath: "离线库/B/b.strm", Kind: "video"},
	})
	if _, _, _, err := h.deepDelEventRows([]string{"影视/电影/A/a.strm"}, nil); err == nil {
		t.Fatal("库目录缺失未拦下")
	}
	h.DB.Where("file_id = ?", "b").Delete(&model.SyncedFile{})
	for _, rel := range []string{"影视", "../outside", "影视/../outside"} {
		if _, _, _, err := h.deepDelEventRows([]string{rel}, nil); err == nil {
			t.Fatalf("危险定位未拦下: %s", rel)
		}
	}
}

func TestDeepDelNativeEventRechecksRecovery(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	rel := "影视/电影/A/a.strm"
	h := eventTestHandler(t, root, []model.SyncedFile{{FileID: "a", RelPath: rel, Kind: "video"}})
	h.processDeepDelEvent(eventPayload(root, rel), false, func([]model.SyncedFile, string) (deepDelResult, error) {
		t.Fatal("复核时已恢复的文件不应删除")
		return deepDelResult{}, nil
	}, func() bool {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("restored"), 0644); err != nil {
			t.Fatal(err)
		}
		return true
	})
}

func TestDeepDelNativeEventBeforeLocalRemoval(t *testing.T) {
	rel := "影视/电影/A/a.strm"
	root := deepDelTestTree(t, rel)
	h := eventTestHandler(t, root, []model.SyncedFile{{FileID: "a", RelPath: rel, Kind: "video"}})
	waits, calls := 0, 0
	h.processDeepDelEvent(eventPayload(root, rel), false, func(rows []model.SyncedFile, _ string) (deepDelResult, error) {
		calls++
		if len(rows) != 1 {
			t.Fatal("未选中事件文件")
		}
		return deepDelResult{}, nil
	}, func() bool {
		waits++
		if waits == 1 {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	if calls != 1 || waits != 2 {
		t.Fatalf("应在文件落地删除后两次确认: calls=%d waits=%d", calls, waits)
	}
}

func TestDeepDelEventThresholdBlocksExecution(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/A/a.strm", Kind: "video"},
		{FileID: "b", RelPath: "影视/A/b.strm", Kind: "video"},
	})
	h.DB.Model(&model.Setting{}).Where("key = ?", "deepdel").Update("value", `{"enabled":true,"max_batch":1,"max_ratio":1,"notify":false}`)
	h.processDeepDelEvent(eventPayload(root, "影视/A"), true, func([]model.SyncedFile, string) (deepDelResult, error) {
		t.Fatal("超限不应执行")
		return deepDelResult{}, nil
	}, func() bool { return true })
	var count int64
	h.DB.Model(&model.DeepDeleteRecord{}).Where("status = ?", "rejected").Count(&count)
	if count != 1 {
		t.Fatalf("缺少拒绝流水: %d", count)
	}
}

// 记录读目录范围，防止清理分类目录时再递归进入无关影片。
type tracedDeepDirIO struct {
	*fakeDirIO
	listed []string
}

func (f *tracedDeepDirIO) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	f.listed = append(f.listed, cid)
	return f.fakeDirIO.listEntries(cid, offset)
}
func TestDeepDelPruneNeverVisitsSibling(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	h := eventTestHandler(t, root, nil)
	for _, r := range []model.PathCache{
		{FileID: "movie", Path: "/影视/电影"},
		{FileID: "target", Path: "/影视/电影/A"},
	} {
		if err := h.DB.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { h.DB.Exec("DELETE FROM path_caches") })
	ops := &tracedDeepDirIO{fakeDirIO: newFakeDirs(map[string][]map[string]interface{}{
		"movie": {dirEnt("target"), dirEnt("sibling")}, "target": {}, "sibling": {},
	})}
	n := h.pruneDeepDelDirs(ops, []string{"/影视/电影", "/影视/电影/A"})
	if n != 1 || !eq(ops.deleted, []string{"target"}) {
		t.Fatalf("误删其他目录: %v", ops.deleted)
	}
	if !eq(ops.listed, []string{"target", "movie"}) {
		t.Fatalf("越界遍历: %v", ops.listed)
	}
}

func TestDeepDelPruneFailureAndRoot(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{"x": {}})
	f.failOn["x"] = true
	if pruneDeepDelEmptyDir(f, "x", "x") || pruneDeepDelEmptyDir(f, "0", "root") {
		t.Fatal("读取失败/根目录不能删")
	}
	if len(f.deleted) != 0 {
		t.Fatal(fmt.Sprint(f.deleted))
	}
}
