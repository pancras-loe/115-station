package api

import "testing"

// 模拟一棵库：
//
//	root(100) = 影视
//	  ├─ 200 电影
//	  │    └─ 210 阿甘正传(1994)      ← 视频在这里
//	  ├─ 300 剧集
//	  │    └─ 310 白莲花度假村
//	  │         └─ 311 Season 01     ← 视频在这里
//	  └─ 900 待整理                   ← 整理工作区，应被排除
//	       └─ 910 某片
func testDirs() map[string]fastDirNode {
	return map[string]fastDirNode{
		"200": {name: "电影", parentID: "100"},
		"210": {name: "阿甘正传(1994)", parentID: "200"},
		"300": {name: "剧集", parentID: "100"},
		"310": {name: "白莲花度假村", parentID: "300"},
		"311": {name: "Season 01", parentID: "310"},
		"900": {name: "待整理", parentID: "100"},
		"910": {name: "某片", parentID: "900"},
	}
}

func newTestResolver(base string, skip map[string]bool) *fastResolver {
	return &fastResolver{root: "100", base: base, dirs: testDirs(), skip: skip, cache: map[string]string{}}
}

func TestResolverBuildsPath(t *testing.T) {
	r := newTestResolver("影视", nil)
	cases := map[string]string{
		"100": "影视",                          // 文件直接躺在库根
		"210": "影视/电影/阿甘正传(1994)",
		"311": "影视/剧集/白莲花度假村/Season 01", // 三层嵌套
	}
	for cid, want := range cases {
		got, ok := r.pathOf(cid)
		if !ok {
			t.Fatalf("cid=%s 解析失败", cid)
		}
		if got != want {
			t.Fatalf("cid=%s 路径 = %q, 期望 %q", cid, got, want)
		}
	}
}

// 排除整理工作区：命中的是祖先而不是直接父目录，整条链都要丢
func TestResolverSkipsWorkspaceSubtree(t *testing.T) {
	r := newTestResolver("影视", map[string]bool{"900": true})
	if _, ok := r.pathOf("910"); ok {
		t.Fatal("待整理子目录下的文件应被排除")
	}
	if r.skipped != 1 {
		t.Fatalf("skipped = %d, 期望 1", r.skipped)
	}
	// 排除不能误伤其它分支
	if p, ok := r.pathOf("210"); !ok || p != "影视/电影/阿甘正传(1994)" {
		t.Fatalf("正常目录被误伤: %q ok=%v", p, ok)
	}
	if r.skipped != 1 {
		t.Fatalf("误伤后 skipped = %d, 期望仍为 1", r.skipped)
	}
}

// 目录表缺项（downfolders 翻页不全等）不能静默生成错误路径
func TestResolverRejectsBrokenChain(t *testing.T) {
	r := newTestResolver("影视", nil)
	delete(r.dirs, "310")
	if _, ok := r.pathOf("311"); ok {
		t.Fatal("祖先链断裂时不应返回路径")
	}
	if r.orphan != 1 {
		t.Fatalf("orphan = %d, 期望 1", r.orphan)
	}
	if _, ok := r.pathOf(""); ok {
		t.Fatal("空 cid 不应返回路径")
	}
	if r.orphan != 2 {
		t.Fatalf("空 cid 后 orphan = %d, 期望 2", r.orphan)
	}
}

// 目录环不能把回溯卡死
func TestResolverStopsOnCycle(t *testing.T) {
	r := newTestResolver("影视", nil)
	r.dirs["500"] = fastDirNode{name: "A", parentID: "501"}
	r.dirs["501"] = fastDirNode{name: "B", parentID: "500"}
	if _, ok := r.pathOf("500"); ok {
		t.Fatal("成环时不应返回路径")
	}
	if r.orphan != 1 {
		t.Fatalf("orphan = %d, 期望 1", r.orphan)
	}
}

// base 为空（同步根即库根、拿不到库名）时不能拼出前导斜杠
func TestResolverEmptyBase(t *testing.T) {
	r := newTestResolver("", nil)
	if got, _ := r.pathOf("210"); got != "电影/阿甘正传(1994)" {
		t.Fatalf("空 base 路径 = %q", got)
	}
	if got, _ := r.pathOf("100"); got != "" {
		t.Fatalf("空 base 且文件在根: 路径 = %q, 期望空串", got)
	}
}

// 第二次解析同一目录必须走缓存且结果一致
func TestResolverCaches(t *testing.T) {
	r := newTestResolver("影视", nil)
	first, _ := r.pathOf("311")
	delete(r.dirs, "310") // 缓存生效的话，删掉中间层也不影响
	second, ok := r.pathOf("311")
	if !ok || second != first {
		t.Fatalf("缓存未生效: %q → %q", first, second)
	}
}

func TestFastEntryToFile(t *testing.T) {
	rf, ok := fastEntryToFile(map[string]interface{}{
		"fid": "3518330214302090865",
		"n":   "一战再战.mkv",
		"s":   float64(86966235502),
		"pc":  "bii7yrgpt7y9683of",
		"sha": "14E868421D1FC0BB223E8C11A8F95A46A9C1E8D6",
	})
	if !ok {
		t.Fatal("正常条目解析失败")
	}
	if rf.Fid != "3518330214302090865" || rf.Name != "一战再战.mkv" ||
		rf.Size != 86966235502 || rf.PickCode != "bii7yrgpt7y9683of" {
		t.Fatalf("字段错位: %+v", rf)
	}
	// 目录条目没有 fid，必须被拒（递归模式理论上不返回目录，但不能靠这个假设）
	if _, ok := fastEntryToFile(map[string]interface{}{"cid": "200", "n": "电影"}); ok {
		t.Fatal("无 fid 的条目应被拒绝")
	}
}

func TestRootDirPickcode(t *testing.T) {
	entries := []map[string]interface{}{
		{"n": "无 pc 的条目"},
		{"pc": "bii7yrgpt7y9683of"},
	}
	got, err := rootDirPickcode("3165030031459331046", entries)
	if err != nil {
		t.Fatalf("rootDirPickcode: %v", err)
	}
	if got != realDirPickcode {
		t.Fatalf("目录 pickcode = %q, 期望 %q", got, realDirPickcode)
	}
	if _, err := rootDirPickcode("3165030031459331046", []map[string]interface{}{{"n": "x"}}); err == nil {
		t.Fatal("没有可用 pickcode 时应报错")
	}
	if _, err := rootDirPickcode("不是数字", entries); err == nil {
		t.Fatal("cid 非法时应报错")
	}
}

// 整理库扫描只收视频，assets 传 nil——回滚逻辑不能对 nil 指针解引用
func TestSnapshotRestoreHandlesNil(t *testing.T) {
	if got := snapshotFiles(nil); got != nil {
		t.Fatalf("snapshotFiles(nil) = %v, 期望 nil", got)
	}
	restoreFiles(nil, []remoteFile{{Fid: "1"}}) // 不能 panic

	videos := []remoteFile{{Fid: "a"}}
	before := snapshotFiles(&videos)
	videos = append(videos, remoteFile{Fid: "b"}, remoteFile{Fid: "c"})
	restoreFiles(&videos, before)
	if len(videos) != 1 || videos[0].Fid != "a" {
		t.Fatalf("回滚后 = %+v, 期望只剩 a", videos)
	}
}
