package api

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStrm 必须如实报告「有没有真的写」。
//
// 改造前它只返回 error，跳过已存在也返回 nil，调用方照样 strmCreated++：
// 于是任何一次重复遍历都会把整棵树的老文件全部报成「新增视频 N 个」，
// 还连带触发一次 Emby 刷新。用户看到的「全盘 strm 一直在重读重建」，
// 有一大半是这个计数造成的错觉
func TestWriteStrmReportsWhetherItWrote(t *testing.T) {
	root := t.TempDir()
	f := remoteFile{Fid: "1", Name: "E01.mkv", Path: "库/剧集/某剧", PickCode: "pc-1"}
	dst := filepath.Join(root, "库", "剧集", "某剧", "E01.mkv.strm")

	wrote, err := writeStrm(root, "http://x", "pick_code", false, true, f)
	if err != nil || !wrote {
		t.Fatalf("首次落盘应报 wrote=true，实得 %v err=%v", wrote, err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("文件应已生成: %v", err)
	}

	// 开着「跳过已存在」：原样跳过
	if wrote, err := writeStrm(root, "http://x", "pick_code", false, true, f); err != nil || wrote {
		t.Fatalf("已存在时应报 wrote=false，实得 %v err=%v", wrote, err)
	}

	// 关掉「跳过已存在」，但内容一模一样：写了也是原样，同样不算新增
	if wrote, err := writeStrm(root, "http://x", "pick_code", false, false, f); err != nil || wrote {
		t.Fatalf("内容一致的重写不该算新增，实得 %v err=%v", wrote, err)
	}

	// 内容真变了（换了 pickcode）才算写
	f.PickCode = "pc-2"
	if wrote, err := writeStrm(root, "http://x", "pick_code", false, false, f); err != nil || !wrote {
		t.Fatalf("内容变了应报 wrote=true，实得 %v err=%v", wrote, err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "http://x/d/pc-2" {
		t.Fatalf("内容应已更新，实得 %q err=%v", string(data), err)
	}

	// 但「跳过已存在」开着时，即便内容变了也不动（这是用户选的语义，别改）
	f.PickCode = "pc-3"
	if wrote, _ := writeStrm(root, "http://x", "pick_code", false, true, f); wrote {
		t.Fatal("开了跳过已存在就不该改写")
	}
}
