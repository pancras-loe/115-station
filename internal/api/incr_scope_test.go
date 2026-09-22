package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

// ==================== 回退遍历的范围与「永久跳过」 ====================
//
// 这一组盯的是同一个事故：一轮增量把远超必要范围的目录树重扫一遍，
// 或者一条永远处理不完的事件把整批事件钉死在每 30 秒一次的重放里。
// 用户侧的现象是「一直在轮询」「全盘 strm 一直在重读」「几个小时不整理」。

// 事件指向媒体库外的目录 = **永久**跳过，绝不能阻止本轮消费。
//
// 改造前它被静默计进 DirsSkipped，与「网络抖动读不到」同等对待：
// 整轮不标 applied、游标不推进，于是这一条事件让**整批**事件每 30 秒
// 重放一遍（连带把库内那些目录一遍遍重扫），直到 7 天后被强杀
func TestIncrOutsideLibraryDirDoesNotBlockConsumption(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrscope_outside.db")

	// cid=outside 既不在 abs 里（作用域判不出 → 放行到回退遍历），
	// 也不在 rel 里（定位得出结论：不在媒体库内）
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "outside", FileName: "片.mkv", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.DirsOutside != 1 || sum.DirsSkipped != 0 {
		t.Fatalf("库外目录应记 DirsOutside 而不是 DirsSkipped，实得 outside=%d skipped=%d",
			sum.DirsOutside, sum.DirsSkipped)
	}
	if !sum.Consumed {
		t.Fatalf("永久跳过不该阻止消费，实得 NotConsumed=%q", sum.NotConsumed)
	}
	assertEventApplied(t, "e-1")
	if _, ok := d.saved["incr-cursor"]; !ok {
		t.Fatal("本轮已消费，游标必须推进——不推进就是下一轮原样重放")
	}
}

// 事件没带父目录（115 对某些上传/转存事件不返回 parent_id）同样是永久跳过。
// 改造前它以空 cid 混进遍历队列，最终也落到 DirsSkipped 上，后果同上
func TestIncrEventWithoutParentDoesNotBlockConsumption(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrscope_noparent.db")

	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "", FileName: "片.mkv", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.DirsNoParent != 1 || sum.DirsSkipped != 0 {
		t.Fatalf("没带父目录应记 DirsNoParent，实得 noparent=%d skipped=%d",
			sum.DirsNoParent, sum.DirsSkipped)
	}
	if !sum.Consumed {
		t.Fatalf("永久跳过不该阻止消费，实得 NotConsumed=%q", sum.NotConsumed)
	}
	assertEventApplied(t, "e-1")
}

// 文件级事件只扫父目录**这一层**。
//
// 事件的 cid 就是那个文件的父目录，文件一定在这一层。改造前一律递归到底，
// 于是「往 影视/剧集 里丢了一个文件」会把整个 剧集 分类重扫一遍
func TestIncrFileEventWalksOneLevelOnly(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrscope_shallow.db")

	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "d1", FileName: "片.mkv", Time: "100"},
	}}

	if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
		t.Fatal(err)
	}
	if len(d.walked) != 1 {
		t.Fatalf("应回退遍历一次，实得 %d", len(d.walked))
	}
	if got := d.walked[0]; got.deep || got.cid != "d1" {
		t.Fatalf("文件级事件应浅遍历它的父目录，实得 %+v", got)
	}
}

// 目录级事件（新建目录/整目录转存）内容全在子树里，必须深遍历
func TestIncrNewFolderWalksDeep(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrscope_deep.db")

	d.rel["dir-9"] = "剧集/新剧"
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evNewFolder, FileID: "dir-9", Cid: "d1", FileName: "新剧", FileCat: "0", Time: "100"},
	}}

	if _, err := h.executeIncrementalSyncWith(d, p); err != nil {
		t.Fatal(err)
	}
	if len(d.walked) != 1 {
		t.Fatalf("应遍历一次，实得 %d", len(d.walked))
	}
	if got := d.walked[0]; !got.deep || got.cid != "dir-9" {
		t.Fatalf("目录新增应深遍历新目录自身，实得 %+v", got)
	}
}

// 有任务在排队等锁时，遍历就地收工，且本轮**不消费**（下轮原样重来）。
//
// 没有这条让路，一轮长遍历期间整理只有 30 秒空窗可抢，
// 转存守望者 5 分钟回来一次，期望等待时间是小时级的
func TestIncrYieldsToWaiterAndKeepsEventsPending(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrscope_yield.db")

	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "d1", FileName: "片.mkv", Time: "100"},
	}}

	// 造一个「锁被占着 + 有人在排队」的现场
	if !taskMu.TryLock("测试占用") {
		t.Fatal("锁应当是空闲的")
	}
	queued := make(chan bool, 1)
	go func() { queued <- taskMu.Acquire("定时整理", 3*time.Second) }()
	for i := 0; i < 200; i++ {
		if w, _ := taskMu.Waiters(); len(w) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w, _ := taskMu.Waiters(); len(w) == 0 {
		taskMu.Unlock()
		t.Fatal("等待方没登记上")
	}

	sum, err := h.executeIncrementalSyncWith(d, p)

	taskMu.Unlock()
	if got := <-queued; got {
		taskMu.Unlock() // 等待方拿到了锁，收尾
	}
	if err != nil {
		t.Fatal(err)
	}

	if !sum.Interrupted {
		t.Fatal("有人排队时应中断本轮遍历")
	}
	if sum.Consumed {
		t.Fatal("没跑完的一轮绝不能标记消费——事件会永久丢失")
	}
	if d.walkCalls != 0 {
		t.Fatalf("让路应发生在遍历之前，实际遍历了 %d 次", d.walkCalls)
	}
	var ev model.SyncEvent
	if err := model.DB.Where("event_id = ?", "e-1").First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Status != "pending" {
		t.Fatalf("让路中断的这轮不该标记事件已应用，实得 %q", ev.Status)
	}
	if _, ok := d.saved["incr-cursor"]; ok {
		t.Fatal("让路中断时不该推进游标")
	}
}

func assertEventApplied(t *testing.T, id string) {
	t.Helper()
	var ev model.SyncEvent
	if err := model.DB.Where("event_id = ?", id).First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Status != "applied" {
		t.Fatalf("事件 %s 应已消费，实得 %q —— 没消费就意味着下一轮原样重放", id, ev.Status)
	}
}
