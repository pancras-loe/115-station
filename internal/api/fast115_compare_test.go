package api

import (
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"
)

// 快速模式与标准模式的对拍：同一个 cid 两条路各跑一遍，逐条比对产出。
// 这是唯一能证明两种模式等价的办法——日志里的数字对得上不代表路径也对得上。
//
// 需要真实账号，默认跳过。跑法：
//
//	STRMHUB_TEST_115_COOKIE='UID=...; CID=...; SEID=...' \
//	STRMHUB_TEST_115_CID=<你要对拍的 115 目录 ID> \
//	go test ./internal/api/ -run TestFastVsNormal -v -timeout 2h
//
// 标准模式要逐个目录遍历，大库会跑很久。第一次对拍建议挑个中等大小的
// 子目录（几百个文件），确认等价后再拿整库跑。
func TestFastVsNormalEquivalence(t *testing.T) {
	cookie := strings.TrimSpace(os.Getenv("STRMHUB_TEST_115_COOKIE"))
	cid := strings.TrimSpace(os.Getenv("STRMHUB_TEST_115_CID"))
	if cookie == "" || cid == "" {
		t.Skip("未设置 STRMHUB_TEST_115_COOKIE / STRMHUB_TEST_115_CID，跳过对拍")
	}

	filter := &syncFilter{
		videoExts: buildExtSet([]string{"mp4", "mkv", "ts", "avi", "mov", "rmvb", "webm", "flv", "m2ts", "wmv", "mpg", "iso"}),
		assetExts: buildExtSet([]string{"jpg", "png", "jpeg", "webp", "ass", "srt", "ssa", "sub"}),
	}
	filter.assetExts[".nfo"] = true

	const libName = "库根"
	ops := &pan115Ops{cookie: cookie}

	start := time.Now()
	var fastVideos, fastAssets []remoteFile
	complete, err := list115SubtreeFast(cookie, cid, libName, &fastVideos, &fastAssets, filter, nil)
	if err != nil {
		t.Fatalf("快速模式失败: %v", err)
	}
	if !complete {
		// 不完整的清单拿来对拍没有意义，而且孤儿清理也会拒绝使用它
		t.Errorf("快速模式报告清单不完整（翻页短缺或目录表缺项），下面的差异可能由此而来")
	}
	fastElapsed := time.Since(start)

	start = time.Now()
	var normVideos, normAssets []remoteFile
	if err := walk115Dir(ops, cid, libName, &normVideos, &normAssets, filter, nil); err != nil {
		t.Fatalf("标准模式失败: %v", err)
	}
	normElapsed := time.Since(start)

	t.Logf("快速模式 %s：视频 %d + 附属 %d", fastElapsed.Truncate(time.Second), len(fastVideos), len(fastAssets))
	t.Logf("标准模式 %s：视频 %d + 附属 %d", normElapsed.Truncate(time.Second), len(normVideos), len(normAssets))

	diffSets(t, "视频", fastVideos, normVideos)
	diffSets(t, "附属文件", fastAssets, normAssets)
}

// diffSets 以 fid 为键比对两组结果：缺失、多余、以及同一个 fid 上的字段分歧
func diffSets(t *testing.T, label string, fast, norm []remoteFile) {
	t.Helper()
	index := func(fs []remoteFile) map[string]remoteFile {
		m := make(map[string]remoteFile, len(fs))
		for _, f := range fs {
			m[f.Fid] = f
		}
		return m
	}
	fastByID, normByID := index(fast), index(norm)

	var missing, extra, mismatch []string
	for fid, n := range normByID {
		f, ok := fastByID[fid]
		if !ok {
			missing = append(missing, path.Join(n.Path, n.Name))
			continue
		}
		// 路径是重点：目录表拼错会静默把 STRM 写到错误的位置
		if f.Path != n.Path || f.Name != n.Name || f.PickCode != n.PickCode || f.Size != n.Size {
			mismatch = append(mismatch, "快速["+path.Join(f.Path, f.Name)+" pc="+f.PickCode+"] != 标准["+path.Join(n.Path, n.Name)+" pc="+n.PickCode+"]")
		}
	}
	for fid, f := range fastByID {
		if _, ok := normByID[fid]; !ok {
			extra = append(extra, path.Join(f.Path, f.Name))
		}
	}

	report := func(name string, items []string) {
		if len(items) == 0 {
			return
		}
		sort.Strings(items)
		t.Errorf("%s：%s %d 条", label, name, len(items))
		for i, it := range items {
			if i >= 20 {
				t.Errorf("  …还有 %d 条", len(items)-20)
				break
			}
			t.Errorf("  %s", it)
		}
	}
	report("快速模式缺失", missing)
	report("快速模式多出", extra)
	report("字段不一致", mismatch)

	if len(missing)+len(extra)+len(mismatch) == 0 {
		t.Logf("%s：%d 条完全一致 ✓", label, len(normByID))
	}
}
