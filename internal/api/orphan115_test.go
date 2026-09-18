package api

import (
	"testing"

	"strmhub/internal/model"
)

func orphanTestDB(t *testing.T, rows []model.SyncedFile) {
	t.Helper()
	// 内存库：免文件句柄（Windows 下 TempDir 清理会因 sqlite 占用报错）
	if _, err := model.InitDB("file:orphan_test?mode=memory&cache=shared"); err != nil {
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

func orphanIDs(t *testing.T) []string {
	t.Helper()
	var rows []model.SyncedFile
	model.DB.Where("orphan_at IS NOT NULL").Order("file_id").Find(&rows)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.FileID)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 扫描没见到的记录被标记，见到的不动
func TestMarkOrphansBasic(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "keep1", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "keep2", RelPath: "影视/电影/A/poster.jpg", Kind: "asset"},
		{FileID: "gone1", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video"},
	})
	marked, cleared, total := markOrphans(model.DB, "影视", map[string]bool{"keep1": true, "keep2": true})
	if marked != 1 || cleared != 0 || total != 1 {
		t.Fatalf("marked=%d cleared=%d total=%d，期望 1/0/1", marked, cleared, total)
	}
	if got := orphanIDs(t); !eq(got, []string{"gone1"}) {
		t.Fatalf("被标记的 = %v，期望 [gone1]", got)
	}
}

// 文件回来了（比如用户又传回网盘）必须取消标记，不能留着等用户误删
func TestMarkOrphansClearsWhenFileReturns(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "b", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video"},
	})
	if _, _, total := markOrphans(model.DB, "影视", map[string]bool{"a": true}); total != 1 {
		t.Fatalf("首轮 total = %d，期望 1", total)
	}
	// 第二轮 b 回来了
	marked, cleared, total := markOrphans(model.DB, "影视", map[string]bool{"a": true, "b": true})
	if marked != 0 || cleared != 1 || total != 0 {
		t.Fatalf("marked=%d cleared=%d total=%d，期望 0/1/0", marked, cleared, total)
	}
	if got := orphanIDs(t); len(got) != 0 {
		t.Fatalf("应无孤儿，实为 %v", got)
	}
}

// 关键安全性质：同步 A 库不能把 B 库的记录判成孤儿
func TestMarkOrphansScopedByLibrary(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "a1", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "b1", RelPath: "俱乐部/电影/B/b.mkv.strm", Kind: "video"},
		{FileID: "b2", RelPath: "俱乐部/电影/C/c.mkv.strm", Kind: "video"},
	})
	// 只扫「影视」库，且 a1 仍在
	marked, _, total := markOrphans(model.DB, "影视", map[string]bool{"a1": true})
	if marked != 0 || total != 0 {
		t.Fatalf("marked=%d total=%d，期望 0/0（俱乐部的记录不该被牵连）", marked, total)
	}
	if got := orphanIDs(t); len(got) != 0 {
		t.Fatalf("不该有任何标记，实为 %v", got)
	}
}

// 库名前缀必须是完整的一层，不能被前缀相同的另一个库名带走
func TestMarkOrphansPrefixIsWholeSegment(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "x", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
		{FileID: "y", RelPath: "影视合集/电影/B/b.mkv.strm", Kind: "video"},
	})
	marked, _, total := markOrphans(model.DB, "影视", map[string]bool{})
	if marked != 1 || total != 1 {
		t.Fatalf("marked=%d total=%d，期望 1/1", marked, total)
	}
	if got := orphanIDs(t); !eq(got, []string{"x"}) {
		t.Fatalf("被标记的 = %v，期望只有 [x]（影视合集 不该被 影视 前缀带走）", got)
	}
}

// 库名为空时必须直接不干活——拿不到库名就没法分区，全表差集会误伤
func TestMarkOrphansRefusesEmptyPrefix(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "a", RelPath: "影视/电影/A/a.mkv.strm", Kind: "video"},
	})
	marked, cleared, total := markOrphans(model.DB, "", map[string]bool{})
	if marked != 0 || cleared != 0 || total != 0 {
		t.Fatalf("空前缀应当直接返回 0/0/0，实为 %d/%d/%d", marked, cleared, total)
	}
	if got := orphanIDs(t); len(got) != 0 {
		t.Fatalf("空前缀不该标记任何记录，实为 %v", got)
	}
	// db 为 nil 也不能 panic（Handler 未初始化 DB 的极端路径）
	if m, c, n := markOrphans(nil, "影视", nil); m != 0 || c != 0 || n != 0 {
		t.Fatalf("db=nil 应返回 0/0/0，实为 %d/%d/%d", m, c, n)
	}
}

// 库名含 LIKE 通配符时不能跨库匹配
func TestMarkOrphansEscapesLikeWildcards(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "hit", RelPath: "电影_4K/A/a.mkv.strm", Kind: "video"},
		{FileID: "other", RelPath: "电影X4K/B/b.mkv.strm", Kind: "video"},
		{FileID: "pct", RelPath: "100%片库/C/c.mkv.strm", Kind: "video"},
	})
	marked, _, total := markOrphans(model.DB, "电影_4K", map[string]bool{})
	if marked != 1 || total != 1 {
		t.Fatalf("marked=%d total=%d，期望 1/1（电影X4K 不该被 _ 通配命中）", marked, total)
	}
	if got := orphanIDs(t); !eq(got, []string{"hit"}) {
		t.Fatalf("被标记的 = %v，期望只有 [hit]", got)
	}
}

func TestLikeEscape(t *testing.T) {
	cases := map[string]string{
		"影视":     "影视",
		"电影_4K":  `电影\_4K`,
		"100%片库": `100\%片库`,
		`a\b`:    `a\\b`, // 反斜杠自身要先转义，否则会把后面的字符变成转义序列
		`%_\`:    `\%\_\\`,
	}
	for in, want := range cases {
		if got := likeEscape(in); got != want {
			t.Errorf("likeEscape(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 重复标记不该刷新时间戳之外的东西，也不该重复计数
func TestMarkOrphansIdempotent(t *testing.T) {
	orphanTestDB(t, []model.SyncedFile{
		{FileID: "gone", RelPath: "影视/电影/B/b.mkv.strm", Kind: "video"},
	})
	if marked, _, _ := markOrphans(model.DB, "影视", map[string]bool{}); marked != 1 {
		t.Fatalf("首轮 marked = %d，期望 1", marked)
	}
	marked, cleared, total := markOrphans(model.DB, "影视", map[string]bool{})
	if marked != 0 || cleared != 0 || total != 1 {
		t.Fatalf("二轮 marked=%d cleared=%d total=%d，期望 0/0/1", marked, cleared, total)
	}
}

func TestChunkIDs(t *testing.T) {
	if got := chunkIDs(nil, 3); got != nil {
		t.Fatalf("空输入 = %v，期望 nil", got)
	}
	got := chunkIDs([]uint{1, 2, 3, 4, 5, 6, 7}, 3)
	if len(got) != 3 || len(got[0]) != 3 || len(got[1]) != 3 || len(got[2]) != 1 {
		t.Fatalf("分批结果 = %v", got)
	}
	if got[2][0] != 7 {
		t.Fatalf("末批 = %v，期望 [7]", got[2])
	}
}
