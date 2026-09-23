package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

func newConfirmCtx(manual bool) *orgCtx {
	return &orgCtx{
		cfg:     &OrgConfig{Pending: "pend-1", ManualConfirm: manual},
		sink:    &orgSink{batchID: "b1", jobs: map[string]scrapeJob{}},
		held:    map[string]*awaitingRef{},
		handled: map[string]bool{},
		onLog:   func(string) {},
	}
}

// 没识别出来的条目：记录里带原因、没有 TMDB 信息，文件 fid 全部登记为 held
func TestHoldForConfirmUnrecognized(t *testing.T) {
	newTestDB(t, "hold_unrec.db")
	ctx := newConfirmCtx(true)
	files := []orgRecordFile{{Fid: "v1", Name: "a.mkv", Kind: "video", Size: 100}, {Fid: "s1", Name: "a.srt", Kind: "subtitle"}}
	res := ctx.holdForConfirm("某目录/", "d1", "dir", nil, parseFileName("a.mkv"), "a.mkv", files, "TMDB 未找到匹配条目，请手动指定")

	if res.Status != orgStatusAwaiting {
		t.Fatalf("结果状态 = %q，想要 awaiting", res.Status)
	}
	var rec model.OrganizeRecord
	if err := model.DB.First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if rec.Status != orgStatusAwaiting || rec.TmdbID != 0 || rec.SourceCid != "pend-1" {
		t.Fatalf("记录不对: %+v", rec)
	}
	if rec.Message != "TMDB 未找到匹配条目，请手动指定" {
		t.Fatalf("原因没写进记录: %q", rec.Message)
	}
	if rec.VideoCount != 1 || rec.TotalSize != 100 {
		t.Fatalf("视频数/体积 = %d/%d", rec.VideoCount, rec.TotalSize)
	}
	for _, fid := range []string{"d1", "v1", "s1"} {
		if ctx.held[fid] == nil || ctx.held[fid].id != rec.ID {
			t.Fatalf("%s 没登记成 held", fid)
		}
	}
}

// 识别出来的条目：记录里带 TMDB 信息与预览落点
func TestHoldForConfirmRecognizedPreviewsTarget(t *testing.T) {
	newTestDB(t, "hold_rec.db")
	ensureRenameTpl()
	ctx := newConfirmCtx(true)
	media := &TmdbMedia{TmdbID: 42, Title: "测试电影", Year: "2020", MediaType: "movie"}
	name := "Test.Movie.2020.1080p.mkv"
	ctx.holdForConfirm(name, "f1", "file", media, parseFileName(name), name,
		[]orgRecordFile{{Fid: "f1", Name: name, Kind: "video"}}, "")

	var rec model.OrganizeRecord
	model.DB.First(&rec)
	if rec.TmdbID != 42 || rec.Title != "测试电影" || rec.MediaType != "movie" {
		t.Fatalf("TMDB 信息没写进记录: %+v", rec)
	}
	if rec.TargetDir == "" {
		t.Fatal("待确认记录应当预览入库目录")
	}
}

// 开关开着时顶层条目里去掉待确认的；关掉之后不过滤（交给 processEntry 接手）
func TestDropHeldRespectsSwitch(t *testing.T) {
	entries := []dirEntry{{Fid: "a"}, {Fid: "b"}, {Fid: "c"}}
	ref := &awaitingRef{id: 1}

	on := newConfirmCtx(true)
	on.held["b"] = ref
	if got := on.dropHeld(append([]dirEntry(nil), entries...)); len(got) != 2 || got[1].Fid != "c" {
		t.Fatalf("开关开着: %+v", got)
	}
	off := newConfirmCtx(false)
	off.held["b"] = ref
	if got := off.dropHeld(append([]dirEntry(nil), entries...)); len(got) != 3 {
		t.Fatalf("开关关着不该过滤: %+v", got)
	}
}

// loadAwaiting 按记录自身 fid 与登记的每个文件建索引，已处理的记录不算
func TestLoadAwaitingIndexesFiles(t *testing.T) {
	newTestDB(t, "load_awaiting.db")
	model.DB.Create(&model.OrganizeRecord{Status: orgStatusAwaiting, SourceFid: "main",
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "main"}, {Fid: "ep2"}})})
	model.DB.Create(&model.OrganizeRecord{Status: "success", SourceFid: "done"})

	got := loadAwaiting()
	if got["main"] == nil || got["ep2"] == nil || got["main"] != got["ep2"] {
		t.Fatalf("待确认记录没按文件建索引: %+v", got)
	}
	if got["done"] != nil {
		t.Fatal("已处理的记录不该算待确认")
	}
}

// 确认后的第一条记录写回待确认那一行（保留创建时间），之后的另起一行
func TestSinkReuseOverwritesAwaitingRow(t *testing.T) {
	newTestDB(t, "sink_reuse.db")
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	awaiting := model.OrganizeRecord{Status: orgStatusAwaiting, Source: "x/", CreatedAt: created}
	model.DB.Create(&awaiting)

	sink := &orgSink{batchID: "b2", jobs: map[string]scrapeJob{}}
	sink.reuse = &awaitingRef{id: awaiting.ID, created: created, manual: true}
	sink.note(&model.OrganizeRecord{Source: "x/", Status: "success", TmdbID: 7})
	if sink.reuse != nil {
		t.Fatal("reuse 用过一次就该清掉")
	}
	sink.note(&model.OrganizeRecord{Source: "x/", Status: "exists"})

	var rows []model.OrganizeRecord
	model.DB.Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("应当是 2 行（写回 1 + 新增 1），实际 %d", len(rows))
	}
	r := rows[0]
	if r.ID != awaiting.ID || r.Status != "success" || r.TmdbID != 7 || !r.ManualTmdb {
		t.Fatalf("待确认行没被正确写回: %+v", r)
	}
	if !r.CreatedAt.Equal(created) {
		t.Fatalf("创建时间被改了: %v → %v", created, r.CreatedAt)
	}
}

// 开关关掉后接手一条待确认，但流水线没落任何记录：那条待确认要删掉
func TestReleaseAwaitingDropsUnconsumed(t *testing.T) {
	newTestDB(t, "release_awaiting.db")
	rec := model.OrganizeRecord{Status: orgStatusAwaiting, SourceFid: "d"}
	model.DB.Create(&rec)
	ctx := newConfirmCtx(false)
	ref := &awaitingRef{id: rec.ID, fids: []string{"d"}}
	ctx.held["d"] = ref

	ctx.adoptAwaiting(ref)
	if ctx.held["d"] != nil {
		t.Fatal("接手后应当从 held 里摘掉")
	}
	ctx.releaseAwaiting(ref)

	var n int64
	model.DB.Model(&model.OrganizeRecord{}).Count(&n)
	if n != 0 {
		t.Fatalf("没被消费的待确认记录应当删除，还剩 %d", n)
	}
}

// 守望者：只剩等人工确认的条目也算「目录里没活」
func TestCountUnheldSkipsAwaiting(t *testing.T) {
	newTestDB(t, "count_unheld.db")
	model.DB.Create(&model.OrganizeRecord{Status: orgStatusAwaiting, SourceFid: "dir1",
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "f1"}})})
	entries := []map[string]interface{}{
		{"f": "0", "cid": "dir1", "n": "目录"},
		{"f": "1", "fid": "f1", "n": "a.mkv"},
		{"f": "1", "fid": "f2", "n": "b.mkv"},
	}
	if n := countUnheld(entries); n != 1 {
		t.Fatalf("countUnheld = %d，想要 1", n)
	}
}
