package api

import (
	"fmt"
	"net/http"
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
	h.DB.Model(&model.Setting{}).Where("key = ?", "deepdel").Update("value", `{"enabled":true,"notify":false}`)
	h.DB.Exec("DELETE FROM deep_delete_records")
	setDeepDelTestLibraries(t, h, []string{root}, http.StatusOK)
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

// 整剧删除时神医 deep.delete 先删完并清了台账，原生 library.deleted 随后到达：
// 前缀下已无台账，应静默跳过，不能报「台账缺少剧/季目录证据」的拦截通知（2026-09-23 现场）
func TestDeepDelSeriesDuplicateEventNotRejected(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	series := "影视/剧集/仁心俱乐部.2025.{tmdbid=276548}"
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "e1", RelPath: series + "/Season 1/e1.strm", Kind: "video"},
		{FileID: "e2", RelPath: series + "/Season 1/e2.strm", Kind: "video"},
	})
	calls := 0
	execute := func(rows []model.SyncedFile, _ string) (deepDelResult, error) {
		calls++
		for _, r := range rows {
			h.DB.Delete(&model.SyncedFile{}, r.ID)
		}
		return deepDelResult{}, nil
	}
	payload := eventPayload(root, series)
	payload["Item"].(map[string]interface{})["Type"] = "Series"
	h.processDeepDelEvent(payload, true, execute, func() bool { return true })
	h.processDeepDelEvent(payload, false, execute, func() bool { return true })
	var rejected int64
	h.DB.Model(&model.DeepDeleteRecord{}).Where("status = ?", "rejected").Count(&rejected)
	if calls != 1 || rejected != 0 {
		t.Fatalf("执行=%d 拦截=%d", calls, rejected)
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
	rows, matched, err := h.deepDelEventRows([]string{"影视/剧集/B"}, []string{"abc"})
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
	if _, _, err := h.deepDelEventRows([]string{"影视/电影/A/a.strm"}, nil); err == nil {
		t.Fatal("库目录缺失未拦下")
	}
	h.DB.Where("file_id = ?", "b").Delete(&model.SyncedFile{})
	for _, rel := range []string{"影视", "../outside", "影视/../outside"} {
		if _, _, err := h.deepDelEventRows([]string{rel}, nil); err == nil {
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

// 媒体库目录读不出来（挂载掉线）时必须整轮放弃并留痕，不能把缺失当成用户删除
func TestDeepDelEventRejectsBrokenMount(t *testing.T) {
	root := deepDelTestTree(t, "影视/keep")
	h := eventTestHandler(t, root, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.strm", Kind: "video"},
		{FileID: "b", RelPath: "已卸载的库/B/b.strm", Kind: "video"},
	})
	h.processDeepDelEvent(eventPayload(root, "影视/电影/A/a.strm"), true, func([]model.SyncedFile, string) (deepDelResult, error) {
		t.Fatal("库目录不可访问时不应执行")
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

// namedDir 带名字的目录条目：cid 是按名字逐级查出来的，用例里必须有 n
func namedDir(cid, name string) map[string]interface{} {
	return map[string]interface{}{"f": "0", "cid": cid, "n": name}
}

// 电影：影片目录删空了要跟着删，分类目录里还有别的影片就留着，且不许进兄弟目录
func TestDeepDelPruneMovieDirNeverVisitsSibling(t *testing.T) {
	h := eventTestHandler(t, deepDelTestTree(t, "影视/keep"), nil)
	ops := &tracedDeepDirIO{fakeDirIO: newFakeDirs(map[string][]map[string]interface{}{
		"lib":    {namedDir("movies", "电影")},
		"movies": {namedDir("A", "流浪地球-2019"), namedDir("B", "让子弹飞-2010")},
		"A":      {},
		"B":      {},
	})}
	n := h.pruneDeepDelDirs(ops, "lib", []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/流浪地球-2019/a.mkv.strm", Kind: "video"},
		{FileID: "b", RelPath: "影视/电影/流浪地球-2019/a.nfo", Kind: "asset"},
	})
	if n != 1 || !eq(ops.deleted, []string{"A"}) {
		t.Fatalf("影片目录没清掉或误删: n=%d deleted=%v", n, ops.deleted)
	}
	for _, cid := range ops.listed {
		if cid == "B" {
			t.Fatalf("越界遍历兄弟影片: %v", ops.listed)
		}
	}
}

// 剧集：季目录 → 标题目录 → 空掉的分类目录，一路往上删，但同步根永远留着
func TestDeepDelPruneSeasonChainStopsAtRoot(t *testing.T) {
	h := eventTestHandler(t, deepDelTestTree(t, "影视/keep"), nil)
	ops := &tracedDeepDirIO{fakeDirIO: newFakeDirs(map[string][]map[string]interface{}{
		"lib":   {namedDir("shows", "剧集")},
		"shows": {namedDir("S", "重器-2026")},
		"S":     {namedDir("s1", "Season 01")},
		"s1":    {},
	})}
	n := h.pruneDeepDelDirs(ops, "lib", []model.SyncedFile{
		{FileID: "e1", RelPath: "影视/剧集/重器-2026/Season 01/E01.mkv.strm", Kind: "video"},
		{FileID: "nfo", RelPath: "影视/剧集/重器-2026/tvshow.nfo", Kind: "asset"},
	})
	if n != 3 || !eq(ops.deleted, []string{"s1", "S", "shows"}) {
		t.Fatalf("空目录链没清干净: n=%d deleted=%v", n, ops.deleted)
	}
}

// 目录已经不在网盘上（改名/搬走）时按名字查不到，整条链安静跳过，不能误删别的目录
func TestDeepDelPruneMissingDirIsSkipped(t *testing.T) {
	h := eventTestHandler(t, deepDelTestTree(t, "影视/keep"), nil)
	ops := &tracedDeepDirIO{fakeDirIO: newFakeDirs(map[string][]map[string]interface{}{
		"lib": {namedDir("movies", "电影")}, "movies": {namedDir("B", "别的影片")}, "B": {},
	})}
	n := h.pruneDeepDelDirs(ops, "lib", []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/已经改名了/a.mkv.strm", Kind: "video"},
	})
	if n != 0 || len(ops.deleted) != 0 {
		t.Fatalf("查不到 cid 不该删任何目录: n=%d deleted=%v", n, ops.deleted)
	}
}

// 没配同步根 cid 时不做任何清理（拿不到起点就不猜）
func TestDeepDelPruneWithoutRootCid(t *testing.T) {
	h := eventTestHandler(t, deepDelTestTree(t, "影视/keep"), nil)
	ops := newFakeDirs(map[string][]map[string]interface{}{"lib": {}})
	rows := []model.SyncedFile{{FileID: "a", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"}}
	if n := h.pruneDeepDelDirs(ops, "", rows); n != 0 {
		t.Fatalf("没有同步根不该清理: %d", n)
	}
	if n := h.pruneDeepDelDirs(ops, "0", rows); n != 0 {
		t.Fatalf("网盘根不该清理: %d", n)
	}
}

// 目录链推导：叶子是文件所在那一级，往上逐级都是祖先，库根那一层不收
func TestDeepDelDirTargets(t *testing.T) {
	got := deepDelDirTargets([]model.SyncedFile{
		{RelPath: "影视/剧集/重器-2026/Season 01/E01.mkv.strm"},
		{RelPath: "影视/电影/流浪地球-2019/a.mkv.strm"},
		{RelPath: "影视/直接躺在库根下.strm"},
	})
	want := map[string]bool{
		"剧集/重器-2026/Season 01": true,
		"剧集/重器-2026":           false,
		"剧集":                   false,
		"电影/流浪地球-2019":         true,
		"电影":                   false,
	}
	if len(got) != len(want) {
		t.Fatalf("目录链不对: %v", got)
	}
	for k, v := range want {
		if leaf, ok := got[k]; !ok || leaf != v {
			t.Fatalf("目录链不对 %s: %v/%v", k, leaf, got)
		}
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
