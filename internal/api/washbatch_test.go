package api

import (
	"reflect"
	"testing"
	"time"

	"115-station/internal/model"
)

// 记录实际发出的网盘写请求次数与集合：批量化的全部意义就在这几个数字上
type washRecOps struct {
	deleteCalls [][]string
	moveCalls   [][]string
	ensured     []string
}

func (o *washRecOps) ensurePath(_ string, rel string) (string, error) {
	o.ensured = append(o.ensured, rel)
	return "junk", nil
}

func (o *washRecOps) moveFiles(_ string, ids []string) error {
	o.moveCalls = append(o.moveCalls, append([]string(nil), ids...))
	return nil
}

func (o *washRecOps) deleteFiles(ids []string) error {
	o.deleteCalls = append(o.deleteCalls, append([]string(nil), ids...))
	return nil
}

// 同一份文件（sha1 相同）已经落在这次的目标目录下：内容一模一样，没什么可洗的。
// 这条必须与「库内已有更优版本」分开报，否则用户配了 replace 却看到「已存在」时
// 分不清是策略判输了还是撞了 sha1
func TestWashSameFileInTargetDir(t *testing.T) {
	season := "库/电视剧/日番/龙珠 (1986)/Season 1"
	rows := []model.SyncedFile{
		{FileID: "old98", Kind: "video", Sha1: "AAA", RelPath: season + "/龙珠 - S01E098 - 1080p.mkv.strm"},
	}
	st := &washStrategy{Mode: "replace", Scope: "all", MediaType: "tv", OldVersionTarget: "delete"}
	plan := decideWash(&TmdbMedia{MediaType: "tv", Title: "龙珠"},
		"七龙珠.Dragon.Ball.S01E098.1080p.mkv", "AAA", "电视剧/日番/龙珠 (1986)/Season 1",
		st, rows, rows, quiet)
	if plan.decision != washSameFile || plan.sameAt != rows[0].RelPath {
		t.Fatalf("同一份文件应报 washSameFile: %+v", plan)
	}
	if washExistsMsg(plan.decision) == washExistsMsg(washNotBetter) {
		t.Fatal("两种「已存在」的理由必须给出不同的文案")
	}
}

// 同一份文件在库内别的位置（全量同步带进来的旧目录结构）：
// replace 的语义就是「新的进库、旧的让位」，不能让一句全表 sha1 查询把洗版短路掉。
// 现场：龙珠 153 集全被判成「库内已有相同或更优版本」，用户配的 delete 从未生效
func TestWashSameFileElsewhereStillReplaces(t *testing.T) {
	washTestDB(t)
	rows := []model.SyncedFile{
		{FileID: "old98", Kind: "video", Sha1: "AAA", RelPath: "库/动漫/七龙珠/龙珠 98.mkv.strm"},
	}
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	st := &washStrategy{Mode: "replace", Scope: "all", MediaType: "tv", OldVersionTarget: "delete"}
	plan := decideWash(&TmdbMedia{MediaType: "tv", Title: "龙珠"},
		"七龙珠.Dragon.Ball.S01E098.1080p.mkv", "AAA", "电视剧/日番/龙珠 (1986)/Season 1",
		st, nil, rows, quiet)
	if plan.decision != washReplaced {
		t.Fatalf("库内位置不规范的同一份文件应让位，得到 %s", plan.decision)
	}
	if len(plan.victims) != 1 || plan.victims[0].FileID != "old98" {
		t.Fatalf("让位集合不对: %+v", plan.victims)
	}
}

// 没配洗版策略不等于愿意在库里多一份副本：退回纯 sha1 去重
func TestWashNoStrategyFallsBackToDedupe(t *testing.T) {
	rows := []model.SyncedFile{{FileID: "x", Sha1: "AAA", RelPath: "库/片/片.mkv.strm"}}
	if got := washNoStrategy("片.mkv", rows, quiet); got.decision != washSameFile {
		t.Fatalf("同一份文件在库内应按已存在处理，得到 %s", got.decision)
	}
	if got := washNoStrategy("片.mkv", nil, quiet); got.decision != washSkip {
		t.Fatalf("库内没有就正常入库，得到 %s", got.decision)
	}
}

// 整批让位只发一次写请求。逐集各发一次要过 3 秒写间隔，153 集就是 7 分半，
// 整理这段时间一直占着 taskMu，增量同步每 30 秒来一次全被挡回去
func TestApplyWashPlansBatchesOneRequest(t *testing.T) {
	washTestDB(t)
	var plans []*washPlan
	var want []string
	for _, ep := range []string{"01", "02", "03", "04", "05"} {
		row := model.SyncedFile{FileID: "old" + ep, Kind: "video",
			RelPath: "库/电视剧/剧/Season 01/剧 - S01E" + ep + " - 720p.mkv.strm"}
		if err := model.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		plans = append(plans, &washPlan{decision: washReplaced, victims: []model.SyncedFile{row},
			oldName: ledgerName(row), newName: "剧.S01E" + ep + ".1080p.mkv", targetDir: "电视剧/剧/Season 01"})
		want = append(want, row.FileID)
	}
	// 同一行被两个新文件同时判赢时不能删两次
	plans = append(plans, &washPlan{decision: washReplaced, victims: plans[0].victims,
		oldName: plans[0].oldName, newName: "剧.S01E01.2160p.mkv", targetDir: "电视剧/剧/Season 01"})

	ops := &washRecOps{}
	st := &washStrategy{Mode: "replace", OldVersionTarget: "delete"}
	err := applyWashPlans(ops, &OrgConfig{}, &TmdbMedia{MediaType: "tv"}, st, plans, quiet, func(...string) {})
	if err != nil {
		t.Fatalf("让位失败: %v", err)
	}
	if len(ops.deleteCalls) != 1 || !reflect.DeepEqual(ops.deleteCalls[0], want) {
		t.Fatalf("应当整批一次删除，实际 %d 次: %v", len(ops.deleteCalls), ops.deleteCalls)
	}
	var left int64
	model.DB.Model(&model.SyncedFile{}).Count(&left)
	if left != 0 {
		t.Fatalf("台账未清干净，剩 %d 行", left)
	}
}

// old_version_target=redundant 时按去向目录分组，每组一次移动（不是每集一次）
func TestApplyWashPlansGroupsByDestination(t *testing.T) {
	washTestDB(t)
	var plans []*washPlan
	for _, tc := range []struct{ fid, season string }{
		{"a1", "Season 01"}, {"a2", "Season 01"}, {"b1", "Season 02"},
	} {
		row := model.SyncedFile{FileID: tc.fid, Kind: "video",
			RelPath: "库/电视剧/剧/" + tc.season + "/" + tc.fid + ".mkv.strm"}
		if err := model.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		plans = append(plans, &washPlan{decision: washReplaced, victims: []model.SyncedFile{row},
			oldName: tc.fid, newName: tc.fid + ".new.mkv", targetDir: "电视剧/剧/" + tc.season})
	}
	ops := &washRecOps{}
	st := &washStrategy{Mode: "replace", OldVersionTarget: "redundant"}
	if err := applyWashPlans(ops, &OrgConfig{Redundant: "r"}, &TmdbMedia{MediaType: "tv"}, st, plans, quiet, func(...string) {}); err != nil {
		t.Fatalf("让位失败: %v", err)
	}
	if !reflect.DeepEqual(ops.ensured, []string{"洗版-旧版本/剧/Season 01", "洗版-旧版本/剧/Season 02"}) {
		t.Fatalf("旧版目录不对: %v", ops.ensured)
	}
	if len(ops.moveCalls) != 2 ||
		!reflect.DeepEqual(ops.moveCalls[0], []string{"a1", "a2"}) ||
		!reflect.DeepEqual(ops.moveCalls[1], []string{"b1"}) {
		t.Fatalf("应按去向目录各发一次: %v", ops.moveCalls)
	}
}

// 台账缓存：同一个季目录只查一次，同一个 sha1 只查一次 ——
// 153 集各查一遍的话，光 PathCache 与台账查询就跑满整轮
func TestWashScannerCachesLookups(t *testing.T) {
	washTestDB(t)
	row := model.SyncedFile{FileID: "v1", Kind: "video", Sha1: "AAA", RelPath: "剧/Season 01/剧.S01E01.mkv.strm"}
	if err := model.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	sc := newWashScanner(nil, nil)
	first := sc.libFiles("剧/Season 01")
	if len(first) != 1 {
		t.Fatalf("台账查询没命中: %+v", first)
	}
	model.DB.Where("file_id = ?", "v1").Delete(&model.SyncedFile{})
	if again := sc.libFiles("剧/Season 01"); len(again) != 1 {
		t.Fatal("同一目录第二次应走缓存，不再查库")
	}
	if len(sc.sameFile("AAA")) != 0 {
		t.Fatal("sha1 查询不该命中已删除的行")
	}
	model.DB.Create(&model.SyncedFile{FileID: "v2", Sha1: "AAA", RelPath: "x.strm"})
	if len(sc.sameFile("AAA")) != 0 {
		t.Fatal("同一 sha1 第二次应走缓存，不再查库")
	}
}

// 全量扫描已确认网盘上没有的行（orphan_at 非空）不能拿来挡新内容 ——
// 一行陈旧台账会把这部片永久钉在「已存在」里
func TestWashSameFileIgnoresOrphanRows(t *testing.T) {
	washTestDB(t)
	now := time.Now()
	if err := model.DB.Create(&model.SyncedFile{FileID: "gone", Sha1: "AAA",
		RelPath: "库/片/片.mkv.strm", OrphanAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	if rows := newWashScanner(nil, nil).sameFile("AAA"); len(rows) != 0 {
		t.Fatalf("失效台账行不该参与去重: %+v", rows)
	}
}
