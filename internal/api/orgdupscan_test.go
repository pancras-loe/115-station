package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

func TestFindLedgerDups(t *testing.T) {
	s4 := "影视/剧集/越狱 (2005)/Season 4"
	now := time.Now()
	rows := []model.SyncedFile{
		// 两份同名：一个新写法、一个旧写法（同基名冲突时后来的那份退回旧写法）
		{FileID: "a", Kind: "video", RelPath: s4 + "/越狱.S04E22.strm"},
		{FileID: "b", Kind: "video", RelPath: s4 + "/越狱.S04E22.mkv.strm"},
		// 115 自动改名的
		{FileID: "c", Kind: "video", RelPath: s4 + "/越狱.S04E21.strm"},
		{FileID: "d", Kind: "video", RelPath: s4 + "/越狱.S04E21(1).strm"},
		// 已经分好 #A #B 的不算
		{FileID: "e", Kind: "video", RelPath: s4 + "/越狱.S04E20#A.strm"},
		{FileID: "f", Kind: "video", RelPath: s4 + "/越狱.S04E20#B.strm"},
		// 字幕、失效的、别的目录的不算
		{FileID: "g", Kind: "asset", RelPath: s4 + "/越狱.S04E22.chs.ass"},
		{FileID: "h", Kind: "video", RelPath: s4 + "/越狱.S04E19.strm"},
		{FileID: "i", Kind: "video", RelPath: s4 + "/越狱.S04E19(1).strm", OrphanAt: &now},
		{FileID: "j", Kind: "video", RelPath: "影视/剧集/越狱 (2005)/Season 3/越狱.S04E22.strm"},
	}
	got := findLedgerDups(rows)
	if len(got) != 2 {
		t.Fatalf("应找出两组，得到 %+v", got)
	}
	if got[0].kind != "same_name" || len(got[0].rows) != 2 || got[0].dir != s4 {
		t.Fatalf("同名并排：%+v", got[0])
	}
	if got[1].kind != "auto_renamed" || got[1].rows[1].FileID != "d" {
		t.Fatalf("115 自动改名：%+v", got[1])
	}
}
