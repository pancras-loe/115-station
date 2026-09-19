package api

import (
	"fmt"
	"strings"
	"testing"
)

// fakeDirIO 假的 115 目录树，驱动**真的** pruneEmptyDirTree —— 守卫判断错一次
// 就是误删用户文件，测试必须跑真逻辑，不能在用例里复刻一份
type fakeDirIO struct {
	dirs    map[string][]map[string]interface{} // cid → 子条目
	deleted []string
	moved   []string        // 被移走的 cid（残留内容的回退去向）
	failOn  map[string]bool // 列目录失败的 cid（模拟风控 / 目录已不存在）
	delFail map[string]bool // 删除失败的 cid
}

func (f *fakeDirIO) moveFiles(_ string, fids []string) error {
	f.moved = append(f.moved, fids...)
	return nil
}

func dirEnt(cid string) map[string]interface{} { return map[string]interface{}{"f": "0", "cid": cid} }
func fileEnt(name string) map[string]interface{} {
	return map[string]interface{}{"f": "1", "n": name, "fid": name}
}

func (f *fakeDirIO) listEntries(cid string, _ int) ([]map[string]interface{}, int, error) {
	if f.failOn[cid] {
		return nil, 0, fmt.Errorf("列目录失败")
	}
	ents := f.dirs[cid]
	return ents, len(ents), nil
}

func (f *fakeDirIO) deleteFiles(fids []string) error {
	for _, cid := range fids {
		if f.delFail[cid] {
			return fmt.Errorf("115 拒绝删除")
		}
		f.deleted = append(f.deleted, cid)
		delete(f.dirs, cid)
		for parent, ents := range f.dirs {
			kept := ents[:0]
			for _, e := range ents {
				if fmt.Sprint(e["cid"]) != cid {
					kept = append(kept, e)
				}
			}
			f.dirs[parent] = kept
		}
	}
	return nil
}

func newFakeDirs(d map[string][]map[string]interface{}) *fakeDirIO {
	return &fakeDirIO{dirs: d, failOn: map[string]bool{}, delFail: map[string]bool{}}
}

func quiet(string) {}

// 剧集场景：标题目录下只剩空的季目录 → 自下而上整棵删掉
func TestPruneEmptyTitleTree(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"title": {dirEnt("s01"), dirEnt("s02")},
		"s01":   {},
		"s02":   {},
	})
	n, gone := pruneEmptyDirTree(f, "title", nil, 0, "title", quiet)
	if n != 3 || !gone {
		t.Fatalf("应删掉 3 个目录（两个季 + 标题）且根被删，实际 %d/%v：%v", n, gone, f.deleted)
	}
}

// 有文件就整棵不动 —— 宁可留空壳也绝不误删内容
func TestPruneStopsOnFile(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"title": {dirEnt("s01")},
		"s01":   {fileEnt("S01E05.mkv")},
	})
	n, gone := pruneEmptyDirTree(f, "title", nil, 0, "title", quiet)
	if n != 0 || gone {
		t.Fatalf("含文件的子树一个都不该删，实际删了 %d：%v", n, f.deleted)
	}
}

// 目录里直接躺着文件：连自己都不该删
func TestPruneStopsOnDirectFile(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"title": {fileEnt("电影.mkv")},
	})
	if n, gone := pruneEmptyDirTree(f, "title", nil, 0, "title", quiet); n != 0 || gone {
		t.Fatalf("不该有任何删除，实际 %v", f.deleted)
	}
}

// 同一部剧分批入库：旧目录里还留着别的集数，父目录保住，只收掉真空的那个季
func TestPruneKeepsPartiallyUsedTitle(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"title": {dirEnt("s01"), dirEnt("s02")},
		"s01":   {},                      // 本次搬空了
		"s02":   {fileEnt("S02E01.mkv")}, // 上一批入库的，还在
	})
	n, gone := pruneEmptyDirTree(f, "title", nil, 0, "title", quiet)
	if n != 1 || gone || len(f.deleted) != 1 || f.deleted[0] != "s01" {
		t.Fatalf("只该删空的 s01、标题目录保住，实际删了 %v（gone=%v）", f.deleted, gone)
	}
}

// 工作区根目录（媒体库/待整理/已存在/冗余/转存）永不删
func TestPruneProtectsWorkspaceRoots(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{"pending": {}})
	if n, gone := pruneEmptyDirTree(f, "pending", map[string]bool{"pending": true}, 0, "pending", quiet); n != 0 || gone {
		t.Fatalf("工作区根不该被删，实际 %v", f.deleted)
	}
	// 保护的目录出现在子树里同样不该被删
	f2 := newFakeDirs(map[string][]map[string]interface{}{
		"parent":   {dirEnt("existing")},
		"existing": {},
	})
	pruneEmptyDirTree(f2, "parent", map[string]bool{"existing": true}, 0, "parent", quiet)
	for _, d := range f2.deleted {
		if d == "existing" {
			t.Fatal("受保护的子目录被删了")
		}
	}
}

// 列目录失败（风控 / 目录已不存在）时按「不删」处理
func TestPruneSkipsOnListError(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{"x": {}})
	f.failOn["x"] = true
	if n, gone := pruneEmptyDirTree(f, "x", nil, 0, "x", quiet); n != 0 || gone {
		t.Fatalf("列不出来时不该删，实际 %v", f.deleted)
	}
}

// 删除失败不该让整轮中断，也不该谎报删除数
func TestPruneDeleteFailure(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"title": {dirEnt("s01")},
		"s01":   {},
	})
	f.delFail["title"] = true
	if n, gone := pruneEmptyDirTree(f, "title", nil, 0, "title", quiet); n != 1 || gone {
		t.Fatalf("子目录删成功、父目录失败时应记 1 且 gone=false，实际 %d/%v（%v）", n, gone, f.deleted)
	}
}

// 限深：异常数据不该把清理变成整树遍历
func TestPruneDepthLimit(t *testing.T) {
	dirs := map[string][]map[string]interface{}{}
	for i := 0; i < 8; i++ {
		dirs[fmt.Sprintf("d%d", i)] = []map[string]interface{}{dirEnt(fmt.Sprintf("d%d", i+1))}
	}
	dirs["d8"] = []map[string]interface{}{}
	f := newFakeDirs(dirs)
	pruneEmptyDirTree(f, "d0", nil, 0, "d0", quiet)
	if len(f.deleted) > emptyDirMaxDepth+1 {
		t.Fatalf("超出限深仍在递归：删了 %v", f.deleted)
	}
}

// pruneOrMove：整棵空了就删，还有残留就连内容一起进冗余。
// 关键用例是嵌套空目录 —— 片名/Season 01/ 里文件搬走后只看直接子项会
// 判成「非空」，把本该删掉的空壳整个搬进冗余
func TestPruneOrMoveNestedEmpty(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"src":     {dirEnt("season1")},
		"season1": {},
	})
	if !pruneOrMove(f, "src", nil, "redundant", "片名/", quiet) {
		t.Fatalf("嵌套空目录应被清理掉，实际 deleted=%v moved=%v", f.deleted, f.moved)
	}
	if len(f.moved) != 0 {
		t.Errorf("清理成功就不该再搬冗余：%v", f.moved)
	}
}

func TestPruneOrMoveFallback(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{
		"src": {fileEnt("没搬走的.mkv")},
	})
	if pruneOrMove(f, "src", nil, "redundant", "片名/", quiet) {
		t.Fatal("有残留内容时不该报告已清理")
	}
	if len(f.moved) != 1 || f.moved[0] != "src" {
		t.Fatalf("残留目录应连内容移到冗余，实际 %v", f.moved)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("不该删除任何东西：%v", f.deleted)
	}
}

// 整轮累积 + 去重：同一个父目录被多个条目命中时只检查一次
func TestDirPrunerDedup(t *testing.T) {
	f := newFakeDirs(map[string][]map[string]interface{}{"a": {}, "b": {}})
	p := newDirPruner(f, []string{"lib"}, quiet)
	for _, cid := range []string{"a", "a", "b", "lib", "0", ""} {
		p.mark(cid, cid)
	}
	if n := p.flush(); n != 2 {
		t.Fatalf("应删 2 个，实际 %d：%v", n, f.deleted)
	}
	if n := p.flush(); n != 0 {
		t.Fatal("flush 后候选应清空")
	}
}

// 工作区根集合要齐（漏一个就可能把用户的待整理目录删了）
func TestOrgProtectedCids(t *testing.T) {
	cfg := &OrgConfig{Library: "1", Pending: "2", Existing: "3", Redundant: "4", ShareCid: "5"}
	got := orgProtectedCids(cfg)
	if len(got) != 5 {
		t.Fatalf("应覆盖 5 个工作区根，实际 %v", got)
	}
	p := newDirPruner(nil, got, quiet)
	for _, cid := range got {
		if !p.protected[cid] {
			t.Errorf("%s 未进保护集", cid)
		}
	}
	for _, cid := range got {
		p.mark(cid, cid)
	}
	p.mark("0", "零")
	p.mark("", "空")
	if len(p.cands) != 0 {
		t.Errorf("受保护/无效 cid 不该进候选：%v", p.cands)
	}
}

// 逐条列举必须有上限，且与日志级别无关 ——
// 一部 40 集的剧重整理会列出几十行，详细模式也不该放行
func TestLogListCaps(t *testing.T) {
	items := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		items = append(items, fmt.Sprintf("第%d集.strm", i))
	}
	var lines []string
	prev := logSink
	t.Cleanup(func() { logSink = prev })
	logSink = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	logList("  - %s", items)
	// 上限条 + 1 行「另有 N 条」提示
	if len(lines) != logListMax+1 {
		t.Fatalf("应截断为 %d 行，实际 %d 行", logListMax+1, len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "另有 30 条") {
		t.Errorf("末行应提示剩余条数，实际 %q", lines[len(lines)-1])
	}

	// 未超上限时全列，不加提示行
	lines = nil
	logList("  - %s", items[:3])
	if len(lines) != 3 {
		t.Fatalf("未超上限应全列，实际 %d 行", len(lines))
	}
}
