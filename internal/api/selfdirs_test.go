package api

import (
	"testing"

	"115-station/internal/model"
)

// 2026-11-05 现场：订阅收掉空包装目录，删除事件回来时缓存已清，增量认不出位置
func TestSelfGoneAbs(t *testing.T) {
	selfDirMu.Lock()
	selfDirMem = map[string]selfDir{}
	selfDirMu.Unlock()

	// 父目录不在缓存里：mkdir 只记得父目录 + 名字，删除事件回来时按父目录解析一次
	noteSelfMkdir("stage", "我不是大师 (2026) S01 ·订阅71-14", "w1")
	noteSelfDelete([]string{"w1"})
	asked := ""
	abs := selfGoneAbs("w1", func(cid string) string { asked = cid; return "/最近接收/剧集/" })
	if asked != "stage" || abs != "/最近接收/剧集/我不是大师 (2026) S01 ·订阅71-14" {
		t.Fatalf("按父目录解析：asked=%q abs=%q", asked, abs)
	}

	// 删的时候缓存里有位置：抄下来，之后缓存清了照样认得出，不再解析父目录
	rememberDirPaths([]model.PathCache{{FileID: "w2", Name: "Season 0", Path: "/影视/剧集/某剧/Season 0"}})
	noteSelfDelete([]string{"w2"})
	forgetDirSubtree("w2")
	if _, ok := lookupDirAbsAnyAge("w2"); ok {
		t.Fatal("缓存应已清掉")
	}
	if got := selfGoneAbs("w2", func(string) string { t.Fatal("不该解析父目录"); return "" }); got != "/影视/剧集/某剧/Season 0" {
		t.Fatalf("抄下的位置：%q", got)
	}

	// 既没建过也没缓存的（文件、别人删的）：不记、认不出
	noteSelfDelete([]string{"f9"})
	if got := selfGoneAbs("f9", func(string) string { return "/x" }); got != "" {
		t.Fatalf("不该认出：%q", got)
	}
}
