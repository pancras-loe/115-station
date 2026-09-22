package api

import (
	"errors"
	"testing"
)

// ==================== 遍历范围与让路中断 ====================
//
// 每列一次目录 = 一次 115 请求 + 全局 1 秒读节流，所以「扫多大范围」
// 直接等于「这一轮跑多久、任务互斥锁被攥多久」。这两条控制逻辑出错的代价
// 是整棵分类树被重扫、整理被饿死几小时，必须有测试盯着。

// fakeLister 造一棵目录树：cid → 该目录下的条目
type fakeLister struct {
	tree  map[string][]map[string]interface{}
	calls []string // 依次被列过的 cid
}

func (f *fakeLister) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	f.calls = append(f.calls, cid)
	es := f.tree[cid]
	return es, len(es), nil
}

func walkDirRow(cid, name string) map[string]interface{} {
	return map[string]interface{}{"f": "0", "cid": cid, "n": name}
}

func walkFileRow(fid, name string) map[string]interface{} {
	return map[string]interface{}{"f": "1", "fid": fid, "n": name, "pc": "pc-" + fid}
}

// 分类目录 → 剧集目录 → 季目录 的三层树
func seasonTree() *fakeLister {
	return &fakeLister{tree: map[string][]map[string]interface{}{
		"cat":    {walkDirRow("show", "某剧"), walkFileRow("f0", "散装.mkv")},
		"show":   {walkDirRow("s1", "Season 01")},
		"s1":     {walkFileRow("f1", "E01.mkv")},
		"unused": {},
	}}
}

// 浅遍历只列目标这一层：子目录一个都不下钻。
//
// 改造前没有这个模式，文件级事件也整棵子树递归 ——
// 「往 影视/剧集 里丢了一个文件」会把整个分类扫一遍
func TestWalkShallowStopsAtOneLevel(t *testing.T) {
	l := seasonTree()
	f := &syncFilter{videoExts: buildExtSet([]string{".mkv"}), assetExts: map[string]bool{}}
	var videos []remoteFile
	ctl := &walkCtl{maxDepth: 1}

	if err := walk115DirCtl(l, "cat", "库/剧集", &videos, nil, f, nil, ctl); err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 1 || l.calls[0] != "cat" {
		t.Fatalf("浅遍历只该列目标本层，实际列了 %v", l.calls)
	}
	if ctl.depthCut != 1 {
		t.Fatalf("应记下 1 个没下钻的子目录，实得 %d", ctl.depthCut)
	}
	if len(videos) != 1 || videos[0].Name != "散装.mkv" {
		t.Fatalf("应只收到本层的文件，实得 %+v", videos)
	}
}

// 深遍历照旧递归到底，并记下代价（目录数 / 列目录次数）供日志溯源
func TestWalkDeepRecursesAndCounts(t *testing.T) {
	l := seasonTree()
	f := &syncFilter{videoExts: buildExtSet([]string{".mkv"}), assetExts: map[string]bool{}}
	var videos []remoteFile
	ctl := &walkCtl{}

	if err := walk115DirCtl(l, "cat", "库/剧集", &videos, nil, f, nil, ctl); err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 {
		t.Fatalf("深遍历应收齐整棵子树，实得 %d 个", len(videos))
	}
	if ctl.dirs != 3 || ctl.pages != 3 {
		t.Fatalf("代价计数不对：目录 %d，列目录 %d 次（用于日志溯源）", ctl.dirs, ctl.pages)
	}
	if ctl.depthCut != 0 {
		t.Fatal("深遍历不该有被截断的子目录")
	}
}

// 中断回调一旦返回原因就立刻停下，并且**在发请求之前**停：
// 让路要的是别再去排队等那 1 秒节流
func TestWalkAbortsOnYield(t *testing.T) {
	l := seasonTree()
	f := &syncFilter{videoExts: buildExtSet([]string{".mkv"}), assetExts: map[string]bool{}}
	var videos []remoteFile
	n := 0
	ctl := &walkCtl{abort: func() string {
		n++
		if n > 1 { // 第一层放过，下钻时喊停
			return "定时整理（已等 3s）"
		}
		return ""
	}}

	err := walk115DirCtl(l, "cat", "库/剧集", &videos, nil, f, nil, ctl)
	var aborted errWalkAborted
	if !errors.As(err, &aborted) {
		t.Fatalf("应返回中断错误，实得 %v", err)
	}
	if aborted.reason != "定时整理（已等 3s）" {
		t.Fatalf("中断原因要能说清让给了谁，实得 %q", aborted.reason)
	}
	if len(l.calls) != 1 {
		t.Fatalf("喊停之后不该再列目录，实际列了 %v", l.calls)
	}
}
