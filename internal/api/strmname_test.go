package api

import (
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

func TestStrmNameOf(t *testing.T) {
	for in, want := range map[string]string{
		"海绵宝宝.SpongeBob SquarePants.S01E09.mp4": "海绵宝宝.SpongeBob SquarePants.S01E09.strm",
		"The.Matrix.1999.2160p.MKV":             "The.Matrix.1999.2160p.strm",
		"disc.iso":                              "disc.strm",
		"说明.txt":                                "说明.txt.strm", // 不是视频：不剥
		".mkv":                                  ".mkv.strm",   // 只有扩展名：剥了就没名字了
	} {
		if got := strmNameOf(in); got != want {
			t.Errorf("strmNameOf(%q) = %q，预期 %q", in, got, want)
		}
	}
	if got := strmRelCandidates("库/剧/E01.mkv"); len(got) != 2 || got[0] != "库/剧/E01.strm" || got[1] != "库/剧/E01.mkv.strm" {
		t.Errorf("候选应为新旧两种写法，得到 %v", got)
	}
}

// 同目录同基名的两个视频：先来的用新写法，后来的退回旧写法；已落盘的沿用自己的名字
func TestStrmNameForCollision(t *testing.T) {
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "mkv", RelPath: "库/片/X.strm", Kind: "video"},
	})
	mp4 := remoteFile{Fid: "mp4", Name: "X.mp4", Path: "库/片", PickCode: "p2"}
	if got := strmNameFor(mp4); got != "X.mp4.strm" {
		t.Fatalf("新写法已被别的视频占着，应退回旧写法，得到 %s", got)
	}
	if got := strmNameFor(remoteFile{Fid: "mkv", Name: "X.mkv", Path: "库/片"}); got != "X.strm" {
		t.Fatalf("已落盘的沿用自己的名字，得到 %s", got)
	}
	// 占着名字的那行已失效（网盘上没了）：新来的接手
	model.DB.Model(&model.SyncedFile{}).Where("file_id = ?", "mkv").Update("orphan_at", model.DB.NowFunc())
	if got := strmNameFor(mp4); got != "X.strm" {
		t.Fatalf("失效行不该占名字，得到 %s", got)
	}
}

// 同一批里的两个同名视频（都还没进台账）：applySyncResults 让后来的退回旧写法
func TestApplySyncResultsSameStemInBatch(t *testing.T) {
	deepDelTestDB(t, nil)
	root := t.TempDir()
	vs := []remoteFile{
		{Fid: "a", Name: "X.mkv", Path: "库/片", PickCode: "pa"},
		{Fid: "b", Name: "X.mp4", Path: "库/片", PickCode: "pb"},
	}
	applySyncResults(model.DB, nil, vs, nil, root, "http://x", "pick_code", false, false, "")
	for fid, want := range map[string]string{"a": "库/片/X.strm", "b": "库/片/X.mp4.strm"} {
		var sf model.SyncedFile
		if err := model.DB.Where("file_id = ?", fid).First(&sf).Error; err != nil || sf.RelPath != want {
			t.Errorf("%s 应落在 %s，得到 %q err=%v", fid, want, sf.RelPath, err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s 的 STRM 应已写出: %v", want, err)
		}
	}
}
