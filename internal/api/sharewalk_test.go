package api

import (
	"errors"
	"fmt"
	"testing"
)

// 分享 STRM 列分享：不限目录数与层数，整库分享（分类/子分类/剧名/Season/字幕）要列全
func TestShareWalkUnlimited(t *testing.T) {
	tree := fakeShareTree{"0": {dirE("c", "剧集")}}
	// 一条 7 层深的链
	parent := "c"
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("d%d", i)
		tree[parent] = []shareEntry{dirE(id, fmt.Sprintf("层%d", i))}
		parent = id
	}
	tree[parent] = []shareEntry{fileE("f", "S01E01.mkv", 10)}
	// 再挂 400 个平级目录
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("w%d", i)
		tree["0"] = append(tree["0"], dirE(id, id))
		tree[id] = []shareEntry{fileE("f"+id, id+".mkv", 10)}
	}
	var lastDirs, lastFiles int
	entries, _, truncated, err := shareWalkCtl(tree.list, shareWalkOpts{Progress: func(d, _, f int) { lastDirs, lastFiles = d, f }})
	if err != nil || truncated {
		t.Fatalf("不限时应列全：truncated=%v err=%v", truncated, err)
	}
	files := 0
	for _, e := range entries {
		if !e.IsDir {
			files++
		}
	}
	if files != 401 || lastFiles != 401 || lastDirs != 408 {
		t.Fatalf("文件 %d、进度 %d 个目录 %d 个文件", files, lastDirs, lastFiles)
	}
	// 订阅那边照旧：4 层、目录数上限
	if _, _, truncated, _ := shareWalkWith(tree.list, 1000); !truncated {
		t.Fatal("订阅列分享应在第 4 层截住")
	}
}

// 风控：整个停下返回错误，不接着列剩下的目录、也不当成「没看全」
func TestShareWalkBusyStops(t *testing.T) {
	tree := fakeShareTree{"0": {dirE("a", "a"), dirE("b", "b"), dirE("c", "c")}, "b": {}, "c": {}}
	calls := 0
	list := func(cid string) ([]shareEntry, string, error) {
		calls++
		if cid == "a" {
			return nil, "", fmt.Errorf("115 提示「操作频繁」: %w", errShareBusy)
		}
		return tree.list(cid)
	}
	if _, _, _, err := shareWalkCtl(list, shareWalkOpts{}); !errors.Is(err, errShareBusy) {
		t.Fatalf("风控应整个返回错误：%v", err)
	}
	if calls != 2 {
		t.Fatalf("风控后还在列：调了 %d 次", calls)
	}
	// 订阅预看同样停下
	calls = 0
	if _, _, _, err := shareWalkWith(list, 30); !errors.Is(err, errShareBusy) || calls != 2 {
		t.Fatalf("订阅列分享碰到风控：%v，调了 %d 次", err, calls)
	}
}

// 停止：根列完后每个目录前问一次
func TestShareWalkStop(t *testing.T) {
	tree := fakeShareTree{"0": {dirE("a", "a"), dirE("b", "b")}, "a": {}, "b": {}}
	n := 0
	_, _, _, err := shareWalkCtl(tree.list, shareWalkOpts{Stop: func() bool { n++; return n >= 2 }})
	if !errors.Is(err, errShareWalkStopped) {
		t.Fatalf("应停下：%v", err)
	}
}

// parseShareSnap 的风控文案要能被 errors.Is 认成 errShareBusy
func TestParseShareSnapBusy(t *testing.T) {
	_, _, err := parseShareSnap([]byte(`{"state":false,"error":"操作太频繁，请稍后再试","errno":990001}`))
	if !errors.Is(err, errShareBusy) {
		t.Fatalf("风控应认成 errShareBusy：%v", err)
	}
	_, _, err = parseShareSnap([]byte(`{"state":false,"error":"访问码错误","errno":4100008}`))
	if errors.Is(err, errShareBusy) {
		t.Fatalf("访问码错误不是风控：%v", err)
	}
}
