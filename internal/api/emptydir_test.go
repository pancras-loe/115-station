package api

import (
	"encoding/json"
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

// locatedDirIO 带祖先链的假通道：只有它能触发 prunableRoot 的范围守卫
// （*pan115Ops 走 Cookie 时同款）。chains 里没有的 cid 视为「已不在网盘上」，
// 正是本次事故里那个失效源目录的处境
type locatedDirIO struct {
	*fakeDirIO
	chains map[string][]string
	errs   map[string]error
}

func (f *locatedDirIO) dirAncestors(cid string) ([]string, error) {
	if err := f.errs[cid]; err != nil {
		return nil, err
	}
	chain, ok := f.chains[cid]
	if !ok {
		return nil, errDirGone
	}
	return chain, nil
}

func located(f *fakeDirIO, chains map[string][]string) *locatedDirIO {
	return &locatedDirIO{fakeDirIO: f, chains: chains, errs: map[string]error{}}
}

// 记录里的源目录 cid 早已失效时，绝不能把清理放进去跑 ——
// 115 会拿网盘根冒充它，于是「清理空目录」变成扫用户整个网盘（线上事故复现）
func TestPrunableRootRejectsGoneDir(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{
		"stale": {dirEnt("婚礼"), dirEnt("软件")}, // 115 返回的其实是网盘根的内容
	}), map[string][]string{})
	var logs []string
	sink := func(s string) { logs = append(logs, s) }
	if prunableRoot(f, "stale", map[string]bool{"pending": true}, "游戏王DVD国语", sink) {
		t.Fatal("失效 cid 必须被拦下")
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "已不在网盘上") {
		t.Errorf("要说清为什么跳过，实际 %v", logs)
	}
}

// 落在工作区外的目录一律拒绝：这是不依赖 115 行为的那道硬锁
func TestPrunableRootRejectsOutsideWorkspace(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{"wedding": {}}),
		map[string][]string{"wedding": {"0", "婚礼", "wedding"}})
	if prunableRoot(f, "wedding", map[string]bool{"pending": true, "lib": true}, "婚礼/典礼", quiet) {
		t.Fatal("工作区外的目录不该允许清理")
	}
	// 工作区内的真子孙照常放行
	f.chains["src"] = []string{"0", "分享", "pending", "src"}
	f.dirs["src"] = nil
	if !prunableRoot(f, "src", map[string]bool{"pending": true}, "待整理/片名", quiet) {
		t.Fatal("待整理下的源目录应允许清理")
	}
}

// 网盘根与一级目录永不删（MoviePilot 同款兜底），哪怕它恰好被登记成了候选
func TestPrunableRootRejectsShallowDirs(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{"top": {}}),
		map[string][]string{"top": {"0", "top"}})
	if prunableRoot(f, "top", nil, "分享", quiet) {
		t.Fatal("一级目录不该允许清理")
	}
}

// 祖先链读失败（风控等）时按「核不准」处理，不删
func TestPrunableRootFailsClosed(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{"x": {}}), map[string][]string{})
	f.errs["x"] = fmt.Errorf("访问频率过高")
	if prunableRoot(f, "x", nil, "x", quiet) {
		t.Fatal("核不准身份时必须放弃清理")
	}
	// 通道压根没有祖先链能力时退回旧守卫，清理照常（否则 OpenAPI 独立模式全停摆）
	if !prunableRoot(f.fakeDirIO, "x", nil, "x", quiet) {
		t.Fatal("没有祖先链能力时不该拦下")
	}
	f.errs["x"] = errNoDirLocator
	if !prunableRoot(f, "x", nil, "x", quiet) {
		t.Fatal("通道自报无能力时同样不该拦下")
	}
}

// 核不准身份时连「搬进冗余」也要停手：拿着指向别处的 cid 搬，比删更难收拾
func TestPruneOrMoveRefusesUnverifiedDir(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{
		"stale": {fileEnt("典礼2k.mp4")},
	}), map[string][]string{})
	if pruneOrMove(f, "stale", map[string]bool{"pending": true}, "redundant", "片名/", quiet) {
		t.Fatal("不该报告已清理")
	}
	if len(f.moved) != 0 || len(f.deleted) != 0 {
		t.Fatalf("既不该删也不该搬，实际 deleted=%v moved=%v", f.deleted, f.moved)
	}
}

// dirPruner 整轮跑：工作区内的删掉，工作区外/已失效的原样留着
func TestDirPrunerScopesToWorkspace(t *testing.T) {
	f := located(newFakeDirs(map[string][]map[string]interface{}{
		"src":     {},
		"wedding": {},
		"stale":   {dirEnt("婚礼")},
	}), map[string][]string{
		"src":     {"0", "分享", "pending", "src"},
		"wedding": {"0", "婚礼", "wedding"},
	})
	p := newDirPruner(f, []string{"pending", "lib"}, quiet)
	p.mark("src", "待整理/片名")
	p.mark("wedding", "婚礼/典礼")
	p.mark("stale", "游戏王DVD国语")
	if n := p.flush(); n != 1 {
		t.Fatalf("只该清掉工作区内那一个，实际 %d：%v", n, f.deleted)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "src" {
		t.Fatalf("删错了目录：%v", f.deleted)
	}
}

// 115 用网盘根冒充失效目录：列目录这一层就要识破（p115client fs_files 同款守卫）
func TestAssert115SameDir(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	cases := []struct {
		name          string
		want          string
		resp, pathCid json.RawMessage
		ok            bool
	}{
		{"cid 相符", "123", raw(`"123"`), raw(`"123"`), true},
		{"数字形态也认", "123", raw(`123`), nil, true},
		{"被降级成网盘根", "123", raw(`0`), raw(`"0"`), false},
		{"path 末级对不上", "123", nil, raw(`"456"`), false},
		{"请求的就是根目录", "0", raw(`0`), nil, true},
		{"两个字段都没回", "123", nil, nil, true},
	}
	for _, c := range cases {
		if got := assert115SameDir(c.want, c.resp, c.pathCid); got != c.ok {
			t.Errorf("%s: 期望 %v，实际 %v", c.name, c.ok, got)
		}
	}
}

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
