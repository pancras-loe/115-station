package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

// deepDelTestDB 内存库 + 造数（同 orphanTestDB，库名不同避免两套用例互相看见对方的行）
func deepDelTestDB(t *testing.T, rows []model.SyncedFile) {
	t.Helper()
	if _, err := model.InitDB("file:deepdel_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM synced_files")
		model.DB = nil
	})
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}
}

// deepDelTestTree 按 rel 列表在临时目录里铺出本地文件，返回根路径
func deepDelTestTree(t *testing.T, rels ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range rels {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o666); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	return root
}

func vanishedIDs(t *testing.T) []string {
	t.Helper()
	var rows []model.SyncedFile
	model.DB.Where("vanish_at IS NOT NULL").Order("file_id").Find(&rows)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.FileID)
	}
	return out
}

func readyIDs(scan vanishScan) []string {
	out := make([]string, 0, len(scan.Ready))
	for _, r := range scan.Ready {
		out = append(out, r.FileID)
	}
	return out
}

// 本地缺失的被打标，本地还在的不动；首轮一个都不进可删集合（两轮确认）
func TestScanVanishedFirstRoundOnlyMarks(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "keep", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "gone", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video"},
	})
	// 库目录（影视）要在，否则库根探针会整轮放弃
	root := deepDelTestTree(t, "影视/电影/A/a.mkv.strm")

	scan, err := scanVanished(model.DB, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if scan.Marked != 1 || scan.Cleared != 0 || scan.Total != 1 {
		t.Fatalf("marked=%d cleared=%d total=%d，期望 1/0/1", scan.Marked, scan.Cleared, scan.Total)
	}
	if !eq(vanishedIDs(t), []string{"gone"}) {
		t.Fatalf("打标结果不对: %v", vanishedIDs(t))
	}
	// 关键：首轮标记的不能立刻删
	if len(scan.Ready) != 0 {
		t.Fatalf("首轮不该有可删条目，得到 %v", readyIDs(scan))
	}
}

// 第二轮仍然缺失才进可删集合
func TestScanVanishedSecondRoundReady(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "keep", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "gone", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video"},
	})
	root := deepDelTestTree(t, "影视/电影/A/a.mkv.strm")

	if _, err := scanVanished(model.DB, root); err != nil {
		t.Fatalf("首轮失败: %v", err)
	}
	scan, err := scanVanished(model.DB, root)
	if err != nil {
		t.Fatalf("次轮失败: %v", err)
	}
	if scan.Marked != 0 {
		t.Fatalf("次轮不该再新增标记，得到 %d", scan.Marked)
	}
	if !eq(readyIDs(scan), []string{"gone"}) {
		t.Fatalf("可删集合不对: %v", readyIDs(scan))
	}
}

// 本地文件又回来了（Emby 重新刮削 / 用户恢复）→ 撤标记，且不再可删
func TestScanVanishedClearsWhenFileReturns(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "back", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video", VanishAt: &past},
	})
	root := deepDelTestTree(t, "影视/电影/A/a.mkv.strm")

	scan, err := scanVanished(model.DB, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if scan.Cleared != 1 || scan.Total != 0 || len(scan.Ready) != 0 {
		t.Fatalf("cleared=%d total=%d ready=%d，期望 1/0/0", scan.Cleared, scan.Total, len(scan.Ready))
	}
	if len(vanishedIDs(t)) != 0 {
		t.Fatalf("标记没被撤掉: %v", vanishedIDs(t))
	}
}

// 已判为失效 STRM 的行（网盘那边也没了）不进候选：拿着不存在的 fid 去删只会换个报错
func TestScanVanishedSkipsOrphans(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "orphan", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video", OrphanAt: &past},
	})
	root := deepDelTestTree(t, "影视/电影/A/a.mkv.strm")

	scan, err := scanVanished(model.DB, root)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if scan.Marked != 0 || scan.Total != 0 {
		t.Fatalf("失效 STRM 不该进候选：marked=%d total=%d", scan.Marked, scan.Total)
	}
}

// 媒体库根整个读不出来（挂载掉线）→ 整轮放弃，一个标记都不能打
func TestScanVanishedAbortsOnMissingRoot(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
	})
	root := filepath.Join(t.TempDir(), "不存在的挂载点")

	if _, err := scanVanished(model.DB, root); err == nil {
		t.Fatal("根目录不存在时必须报错放弃")
	}
	if len(vanishedIDs(t)) != 0 {
		t.Fatalf("放弃的轮次不该留下标记: %v", vanishedIDs(t))
	}
}

// 某个库的目录整个消失（只挂掉了一个库）→ 同样整轮放弃
func TestScanVanishedAbortsOnMissingLibDir(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "b", RelPath: "动漫/剧集/B/b.mkv.strm", Kind: "video"},
	})
	// 只铺了「影视」，台账里还有「动漫」库
	root := deepDelTestTree(t, "影视/电影/A/a.mkv.strm")

	if _, err := scanVanished(model.DB, root); err == nil {
		t.Fatal("有库目录缺失时必须报错放弃")
	}
	if len(vanishedIDs(t)) != 0 {
		t.Fatalf("放弃的轮次不该留下标记: %v", vanishedIDs(t))
	}
}

// 空配置也要有可用的默认值：默认预演、只标记、阈值取默认
func TestDeepDelCfgDefaults(t *testing.T) {
	var c deepDelCfg
	if !c.dryRun() {
		t.Fatal("没配过 dry_run 时必须默认预演")
	}
	if c.auto() {
		t.Fatal("没配过 mode 时不能是自动删除")
	}
	if c.maxBatch() != deepDelMaxBatchDefault || c.maxRatio() != deepDelMaxRatioDefault {
		t.Fatalf("阈值默认值不对: %d / %v", c.maxBatch(), c.maxRatio())
	}
	if c.interval() != deepDelScanDefault {
		t.Fatalf("扫描间隔默认值不对: %v", c.interval())
	}
	// 显式填 0 = 关掉后台扫描（逃生门），不能被下限顶回去
	zero := 0
	c.ScanInterval = &zero
	if c.interval() != 0 {
		t.Fatalf("显式填 0 应当关掉扫描，得到 %v", c.interval())
	}
	// 显式填 false 才是真的关掉预演
	no := false
	c.DryRun = &no
	if c.dryRun() {
		t.Fatal("显式 dry_run=false 没生效")
	}
}

// 阈值守卫：条数、比例各拦各的，正常量级放行
func TestDeepDelOverLimit(t *testing.T) {
	cfg := deepDelCfg{}
	// 一整季 24 集 + 附属，占台账很小一部分 → 放行
	if over, why := deepDelOverLimit(cfg, 24, 60, 10000); over {
		t.Fatalf("正常量级不该被拦: %s", why)
	}
	// 挂载掉线：视频数远超上限 → 拦
	if over, _ := deepDelOverLimit(cfg, 900, 2000, 10000); !over {
		t.Fatal("超过条数上限必须拦下")
	}
	// 小库：条数没超，但占比过半 → 拦
	if over, _ := deepDelOverLimit(cfg, 40, 45, 60); !over {
		t.Fatal("超过比例上限必须拦下")
	}
	// 台账为空时不能因为除零误判
	if over, why := deepDelOverLimit(cfg, 0, 0, 0); over {
		t.Fatalf("空台账不该被拦: %s", why)
	}
}

// 片名推导：标题目录里的年份与 tmdb 标记要被剥掉，多片名要汇总
func TestDeepDelTitle(t *testing.T) {
	one := deepDelTitle([]string{
		"影视/剧集/国产剧/Z-重器-2026-[tmdb=291856]/Season 01/E01.mkv.strm",
		"影视/剧集/国产剧/Z-重器-2026-[tmdb=291856]/Season 01/E02.mkv.strm",
	})
	if one != "重器 (2026)" {
		t.Fatalf("单片名推导不对: %q", one)
	}
	many := deepDelTitle([]string{
		"影视/电影/华语电影/流浪地球-2019/a.mkv.strm",
		"影视/电影/华语电影/让子弹飞-2010/b.mkv.strm",
	})
	if many != "流浪地球 (2019) 等 2 项" {
		t.Fatalf("多片名推导不对: %q", many)
	}
	// 推不出来也要有个能看的兜底，不能是空字符串
	if got := deepDelTitle([]string{"a.strm"}); got == "" {
		t.Fatal("兜底片名不能为空")
	}
}

func TestChunkStrings(t *testing.T) {
	got := chunkStrings([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("分批结果不对: %v", got)
	}
	if len(chunkStrings(nil, 10)) != 0 {
		t.Fatal("空输入应当得到空批次")
	}
}

// 按整理记录深度删除：记录里的 fid 只用来查台账，查不到的一概不删
func TestDeepDelRowsForRecord(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "f1", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "f2", RelPath: "影视/电影/A/a.nfo", Kind: "asset"},
	})

	rec := model.OrganizeRecord{Files: marshalRecordFiles([]orgRecordFile{
		{Fid: "f1", Name: "a.mkv", Kind: "video"},
		{Fid: "f2", Name: "a.nfo", Kind: "asset"},
		// 整理时落过盘、后来被移走的：台账里查不到，不能跟着删
		{Fid: "f3-不在台账", Name: "b.mkv", Kind: "video"},
	})}
	rows, total, err := deepDelRowsForRecord(model.DB, rec)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if total != 3 || len(rows) != 2 {
		t.Fatalf("total=%d rows=%d，期望 3/2", total, len(rows))
	}

	// 没留文件信息的记录必须报错拒绝，不能静默当成「没什么可删」
	if _, _, err := deepDelRowsForRecord(model.DB, model.OrganizeRecord{}); err == nil {
		t.Fatal("空文件清单必须报错")
	}
}
