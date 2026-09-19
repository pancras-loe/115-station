package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"strmhub/internal/model"
)

// ==================== 增量同步主流程测试 ====================
//
// 这些用例踩在 incrDeps 上（见 incrdeps.go）：115 调用、配置读写、通知走桩，
// 数据库用真的内存 SQLite、本地文件用真的临时目录 —— upsert 与整树删除
// 这些最容易出错的地方，用桩反而会糊过去。

type stubIncrDeps struct {
	pages    [][]lifeEvent     // 按次序返回的事件页
	names    map[string]string // cid → 目录名
	abs      map[string]string // cid → 网盘绝对路径
	rel      map[string]string // cid → 相对媒体库根的路径
	settings map[string]string
	walkErr  error // 非 nil 时所有目录遍历都失败

	// 观测点
	fetchCalls int
	gateCalls  int
	walkCalls  int
	refreshed  []string
	saved      map[string]string
}

func newStubDeps() *stubIncrDeps {
	return &stubIncrDeps{
		names:    map[string]string{},
		abs:      map[string]string{},
		rel:      map[string]string{},
		settings: map[string]string{"org-basic": "{}", "share": "{}"},
		saved:    map[string]string{},
	}
}

// lifeEvents 桩：一轮返回一批（真实现是翻页到游标为止，翻页逻辑归 life115_test 管）
func (s *stubIncrDeps) lifeEvents(cur lifeCursor, max int) ([]lifeEvent, lifeCursor, error) {
	s.fetchCalls++
	if len(s.pages) == 0 {
		return nil, cur, nil
	}
	evs := s.pages[0]
	next := cur
	if len(evs) > 0 && evs[0].ID != "" {
		next.FromID = evs[0].ID
	}
	return evs, next, nil
}

func (s *stubIncrDeps) ensureLifeGate() { s.gateCalls++ }

func (s *stubIncrDeps) dirName(cid string) string { return s.names[cid] }
func (s *stubIncrDeps) absPath(cid string) string { return s.abs[cid] }

func (s *stubIncrDeps) relPath(cid, rootCid string) (string, bool, error) {
	if cid == rootCid {
		return "", true, nil
	}
	r, ok := s.rel[cid]
	return r, ok, nil
}

func (s *stubIncrDeps) walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter) error {
	s.walkCalls++
	return s.walkErr
}

func (s *stubIncrDeps) invalidateDirCache()                    {}
func (s *stubIncrDeps) setting(key string) string              { return s.settings[key] }
func (s *stubIncrDeps) saveSetting(key, val string)            { s.saved[key] = val }
func (s *stubIncrDeps) notifyRefresh(base string)              { s.refreshed = append(s.refreshed, base) }
func (s *stubIncrDeps) downloadAsset(remoteFile, string) error { return nil }

func (s *stubIncrDeps) strmConfig() (string, string, bool, bool) {
	return "http://strm.test", "pick_code", false, false
}

func (s *stubIncrDeps) applyResults(videos, assets []remoteFile, localPath, domain, format string,
	keepExt, skipExist bool, dirLabel string) (int, int, int, int) {
	return 0, 0, 0, 0
}

// newIncrTestEnv 搭一套「媒体库 lib = /影视，库名 媒体库，子目录 d1 = 剧集/X」的环境
func newIncrTestEnv(t *testing.T, dbName string) (*Handler, *stubIncrDeps, incrParams) {
	t.Helper()
	newTestDB(t, dbName)

	d := newStubDeps()
	d.names["lib"] = "媒体库"
	d.abs["lib"] = "/影视"
	d.abs["d1"] = "/影视/剧集/X"
	d.rel["d1"] = "剧集/X"

	p := incrParams{
		Cid:       "lib",
		LocalPath: t.TempDir(),
		VideoExt:  []string{".mkv"},
		Limit:     1000,
	}
	return &Handler{DB: model.DB}, d, p
}

// 媒体库 cid 解析不出绝对路径时必须熔断，且**一条事件都不能拉**。
// 拉了就会全部被判成 other 静默吞掉并标已消费，对应 STRM 永久缺失
func TestIncrAbortsWhenLibraryPathUnresolvable(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrflow_nolib.db")
	delete(d.abs, "lib") // 媒体库 cid 失效

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err == nil {
		t.Fatal("媒体库路径解析不出时必须报错中止")
	}
	if d.fetchCalls != 0 {
		t.Fatalf("熔断时不该拉取事件，实际拉了 %d 次", d.fetchCalls)
	}
	if sum.EventsFresh != 0 {
		t.Fatalf("熔断时不该消费事件，实得 %d", sum.EventsFresh)
	}
}

// 工作区目录（待整理/已存在/冗余/转存）覆盖了整个媒体库时同样熔断：
// 此时排除区判定会吞掉所有库内事件，继续跑就是把媒体库当垃圾区处理
func TestIncrAbortsWhenWorkspaceCoversLibrary(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrflow_cover.db")
	d.settings["org-basic"] = `{"pending":"w1"}`
	d.abs["w1"] = "/影视" // 待整理目录选在了媒体库根上

	if _, err := h.executeIncrementalSyncWith(d, p); err == nil {
		t.Fatal("工作区覆盖整个媒体库时必须报错中止")
	}
	if d.fetchCalls != 0 {
		t.Fatalf("熔断时不该拉取事件，实际拉了 %d 次", d.fetchCalls)
	}
}

// 阶段 1 修复的主流程验证：同一页里混着已 applied 的历史事件时，
// 只有真正的新事件会被消费。
//
// 旧代码把整页都塞进 pending，那条早已应用过的 delete 事件会被重放一遍，
// 按台账把文件删掉——而用户可能已经重新上传了同名文件
func TestIncrSkipsAlreadyAppliedEvents(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrflow_replay.db")

	// 一个此前同步过、台账有记录、本地有实体的文件
	oldRel := "媒体库/剧集/X/旧片.mkv.strm"
	oldAbs := filepath.Join(p.LocalPath, filepath.FromSlash(oldRel))
	if err := os.MkdirAll(filepath.Dir(oldAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldAbs, []byte("http://strm.test/d/pc-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	model.DB.Create(&model.SyncedFile{FileID: "f-old", RelPath: oldRel, Kind: "video"})

	// 删除它的那条事件早就应用过了
	applied := time.Now()
	model.DB.Create(&model.SyncEvent{
		EventID: "e-old", Type: evDelete, FileID: "f-old", Cid: "d1",
		FileName: "旧片.mkv", EventTime: 100, Status: "applied", AppliedAt: &applied,
	})

	// 115 这一页同时返回了那条老事件和一条新上传
	d.pages = [][]lifeEvent{{
		{ID: "e-new", Type: evUpload, FileID: "f-new", Cid: "d1",
			FileName: "新片.mkv", PickCode: "pc-new", Time: "200"},
		{ID: "e-old", Type: evDelete, FileID: "f-old", Cid: "d1",
			FileName: "旧片.mkv", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}

	if sum.EventsFresh != 1 {
		t.Fatalf("只有 1 条新事件，实得 %d", sum.EventsFresh)
	}
	if sum.Deleted != 0 {
		t.Fatalf("已应用过的删除事件不该被重放，实得 Deleted=%d", sum.Deleted)
	}
	if _, err := os.Stat(oldAbs); err != nil {
		t.Fatalf("旧文件被重放的删除事件误删了: %v", err)
	}
	// 新事件带 pick_code，走零遍历直推，不该触发目录遍历
	newAbs := filepath.Join(p.LocalPath, filepath.FromSlash("媒体库/剧集/X/新片.mkv.strm"))
	if _, err := os.Stat(newAbs); err != nil {
		t.Fatalf("新事件应生成 STRM: %v", err)
	}
	if d.walkCalls != 0 {
		t.Fatalf("带 pick_code 的事件不该回退目录遍历，实际遍历了 %d 次", d.walkCalls)
	}
}

// 目录遍历重试后仍失败时整轮不消费：被标 applied 的事件永远不会再处理，
// 对应的 STRM 就永久缺失了。STRM 写入是 upsert、删除幂等，重做无副作用
func TestIncrKeepsEventsPendingWhenWalkFails(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrflow_walkfail.db")

	// 测试里不真等 30 秒
	prev := incrRetryDelay
	incrRetryDelay = time.Millisecond
	t.Cleanup(func() { incrRetryDelay = prev })

	d.walkErr = os.ErrDeadlineExceeded
	// 没有 pick_code → 回退目录遍历
	d.pages = [][]lifeEvent{{
		{ID: "e-1", Type: evUpload, FileID: "f-1", Cid: "d1", FileName: "片.mkv", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.DirsSkipped == 0 {
		t.Fatal("遍历失败应计入 DirsSkipped")
	}
	if d.walkCalls != 2 {
		t.Fatalf("应遍历 2 次（首次 + 重试一次），实得 %d", d.walkCalls)
	}

	var ev model.SyncEvent
	if err := model.DB.Where("event_id = ?", "e-1").First(&ev).Error; err != nil {
		t.Fatal(err)
	}
	if ev.Status != "pending" {
		t.Fatalf("遍历失败的这轮不该标记事件已应用，实得 %q", ev.Status)
	}
	if _, ok := d.saved["incr-last"]; ok {
		t.Fatal("整轮放弃时不该推进水位")
	}
	// 游标同样不能推进——推了这批事件下轮就拉不回来了
	if _, ok := d.saved["incr-cursor"]; ok {
		t.Fatal("整轮放弃时不该推进游标")
	}
}

// 整理自产的变更绕回来时跳过，但抑制标记要留到事件真的 applied 之后才清。
// 查的时候就清掉的话，遇上「整轮放弃重来」，下一轮就没有标记可命中，
// 整理刚落好的 STRM 会被当成外部变更删掉
func TestIncrSuppressedEventsSkippedAndUnmarkedAfterApply(t *testing.T) {
	h, d, p := newIncrTestEnv(t, "incrflow_suppress.db")

	markSuppressed("move", []string{"f-org"})
	d.pages = [][]lifeEvent{{
		{ID: "e-org", Type: evMove, FileID: "f-org", Cid: "d1",
			FileName: "整理搬的.mkv", PickCode: "pc-org", Time: "100"},
	}}

	sum, err := h.executeIncrementalSyncWith(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Suppressed != 1 {
		t.Fatalf("整理自产事件应被抑制，实得 Suppressed=%d", sum.Suppressed)
	}
	if sum.Moved != 0 || sum.StrmCreated != 0 {
		t.Fatalf("被抑制的事件不该产生本地动作，实得 Moved=%d StrmCreated=%d", sum.Moved, sum.StrmCreated)
	}
	// 事件已落定 → 标记必须清掉，否则之后用户真的手动移动这个文件会被一起吞掉
	if peekSuppressed("f-org") {
		t.Fatal("事件已 applied，抑制标记应被清掉")
	}
}
