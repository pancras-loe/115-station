package api

import (
	"os"
	"path/filepath"
	"testing"

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

func TestDeepDelCfgDefaults(t *testing.T) {
	c := deepDelCfg{}
	if c.Enabled || !c.prunePanDirs() || !c.notify() {
		t.Fatal("默认关闭，清理空目录与通知默认开")
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
