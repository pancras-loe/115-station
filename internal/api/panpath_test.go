package api

import (
	"errors"
	"testing"
	"time"

	"strmhub/internal/model"
)

// seedChain 往缓存里塞一条祖先链（等价于一次成功的 fetch115Ancestors）
func seedChain(t *testing.T, levels ...[3]string) {
	t.Helper()
	rows := make([]model.PathCache, 0, len(levels))
	acc := ""
	for _, l := range levels { // l = {cid, pid, name}
		acc += "/" + l[2]
		rows = append(rows, model.PathCache{FileID: l[0], ParentID: l[1], Name: l[2], Path: acc})
	}
	rememberDirPaths(rows)
}

func resetPathMem() {
	pathCacheMu.Lock()
	pathCacheMem = map[string]pathMemEntry{}
	pathCacheMu.Unlock()
}

// 缓存命中就不该打接口。cookie 传空串——真发请求会立刻失败，
// 所以只要返回了正确路径，就证明走的是缓存
func TestResolveDirAbsUsesCache(t *testing.T) {
	newTestDB(t, "panpath_cache.db")
	seedChain(t, [3]string{"c1", "0", "影视"}, [3]string{"c2", "c1", "剧集"})

	got, err := resolveDirAbs("", "c2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/影视/剧集" {
		t.Fatalf("路径不对: %q", got)
	}
	// 链上每一级都该进缓存，不只是末级
	if p, ok := lookupCachedAbs("c1"); !ok || p != "/影视" {
		t.Fatalf("祖先链中间级没进缓存: %q ok=%v", p, ok)
	}
}

// 内存层清空后应从 DB 层回填，而不是去打接口
func TestResolveDirAbsFallsBackToDB(t *testing.T) {
	newTestDB(t, "panpath_db.db")
	seedChain(t, [3]string{"c1", "0", "影视"})
	resetPathMem()

	got, err := resolveDirAbs("", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/影视" {
		t.Fatalf("DB 层没回填: %q", got)
	}
}

// DB 行超过保险期就不再采信：失效钩子万一漏了一处，陈旧最多持续这么久
func TestCachedRowExpires(t *testing.T) {
	newTestDB(t, "panpath_ttl.db")
	model.DB.Create(&model.PathCache{
		FileID: "old", ParentID: "0", Name: "旧", Path: "/旧",
		UpdatedAt: time.Now().Add(-pathRowTTL - time.Hour),
	})
	resetPathMem()

	if p, ok := lookupCachedAbs("old"); ok {
		t.Fatalf("过期行不该被采信，实得 %q", p)
	}
}

// lookupCachedAbs 只查缓存不打接口 —— move/rename 找旧路径全靠它，
// 查不到就该老老实实返回 false，而不是去发请求猜一个
func TestLookupCachedAbsNeverFetches(t *testing.T) {
	newTestDB(t, "panpath_nofetch.db")
	if p, ok := lookupCachedAbs("从未见过"); ok {
		t.Fatalf("没缓存过的 cid 不该有结果，实得 %q", p)
	}
}

// 子树失效的前缀边界：/a/b 失效不能连累 /a/bc
func TestForgetPathsUnderPrefixBoundary(t *testing.T) {
	newTestDB(t, "panpath_prefix.db")
	seedChain(t, [3]string{"a", "0", "a"}, [3]string{"b", "a", "b"})
	seedChain(t, [3]string{"a", "0", "a"}, [3]string{"bc", "a", "bc"})
	seedChain(t, [3]string{"a", "0", "a"}, [3]string{"b", "a", "b"}, [3]string{"deep", "b", "deep"})

	forgetPathsUnder("/a/b")

	if _, ok := lookupCachedAbs("b"); ok {
		t.Fatal("/a/b 自身应被失效")
	}
	if _, ok := lookupCachedAbs("deep"); ok {
		t.Fatal("/a/b/deep 是子孙，应被一起失效")
	}
	if p, ok := lookupCachedAbs("bc"); !ok || p != "/a/bc" {
		t.Fatalf("/a/bc 只是名字前缀相同，不该被误伤: %q ok=%v", p, ok)
	}
	if p, ok := lookupCachedAbs("a"); !ok || p != "/a" {
		t.Fatalf("父目录 /a 不该被失效: %q ok=%v", p, ok)
	}
}

// 按 cid 失效子树：整理把目录搬进冗余后，ops 层就是这么调的
func TestForgetDirSubtree(t *testing.T) {
	newTestDB(t, "panpath_subtree.db")
	seedChain(t, [3]string{"lib", "0", "影视"}, [3]string{"show", "lib", "某剧"})

	forgetDirSubtree("show")
	if _, ok := lookupCachedAbs("show"); ok {
		t.Fatal("被搬走的目录应从缓存里去掉")
	}
	if _, ok := lookupCachedAbs("lib"); !ok {
		t.Fatal("不该连累父目录")
	}

	// 没缓存过的 cid：什么都不用做，更不能清空别人
	forgetDirSubtree("从未见过")
	if _, ok := lookupCachedAbs("lib"); !ok {
		t.Fatal("对未知 cid 失效不该波及其他条目")
	}
}

// ==================== 探测 B 的硬门槛 ====================
//
// 115 对不存在/已删除的 cid 不报错，而是静默按根目录处理：
// HTTP 200 + state:true + path 只剩根那一级。
// 不认出这一点，已删除的父目录会被解析成「网盘根」，
// removeSyncedItem 的路径推导随即指向媒体库根下的同名文件。
//
// 解析逻辑在 fetch115Ancestors 里（要发请求），这里直接验校验规则本身

func TestAncestorChainValidation(t *testing.T) {
	cases := []struct {
		name  string
		cid   string
		chain []ancestor
		gone  bool
	}{
		{
			name:  "正常链：末元素就是请求的 cid",
			cid:   "c2",
			chain: []ancestor{{cid: "0", name: "根目录", aid: "1"}, {cid: "c1", pid: "0", name: "影视", aid: "1"}, {cid: "c2", pid: "c1", name: "剧集", aid: "1"}},
		},
		{
			name:  "115 静默降级成根目录：末元素是根，不是请求的 cid",
			cid:   "已删除的cid",
			chain: []ancestor{{cid: "0", name: "根目录", aid: "1"}},
			gone:  true,
		},
		{
			name:  "空链",
			cid:   "c1",
			chain: nil,
			gone:  true,
		},
		{
			name:  "非正常区（回收站等）：aid 不是 1",
			cid:   "c1",
			chain: []ancestor{{cid: "0", name: "根目录"}, {cid: "c1", pid: "0", name: "影视", aid: "7"}},
			gone:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ancestorChainGone(c.chain, c.cid); got != c.gone {
				t.Fatalf("判定不符：期望 gone=%v，实得 %v", c.gone, got)
			}
		})
	}
}

// 拼路径要跳过根元素，否则每条路径都会平白多一层「/根目录」
func TestBuildAbsFromChainSkipsRoot(t *testing.T) {
	chain := []ancestor{
		{cid: "0", pid: "0", name: "根目录", aid: "1"},
		{cid: "c1", pid: "0", name: "影视", aid: "1"},
		{cid: "c2", pid: "c1", name: "剧集", aid: "1"},
	}
	rows := chainToRows(chain)
	if len(rows) != 2 {
		t.Fatalf("根元素应被跳过，实得 %d 行", len(rows))
	}
	if rows[0].Path != "/影视" || rows[1].Path != "/影视/剧集" {
		t.Fatalf("路径拼错: %q / %q", rows[0].Path, rows[1].Path)
	}
	if rows[1].ParentID != "c1" {
		t.Fatalf("父 id 没带上: %q", rows[1].ParentID)
	}
}

// errDirGone 要能被 errors.Is 认出来 —— 增量靠它把「目录已被删除」
// 的事件按已解决跳过，认不出就会让水位永远不推进
func TestErrDirGoneIsComparable(t *testing.T) {
	wrapped := errors.Join(errors.New("外层"), errDirGone)
	if !errors.Is(wrapped, errDirGone) {
		t.Fatal("包装后应仍能被 errors.Is 识别")
	}
}
