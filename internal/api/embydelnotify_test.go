package api

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type sentMsg struct{ title, content string }

// 换掉发送与等待，返回收到的消息与「等全部发完」
func captureEmbyDel(t *testing.T) (func() []sentMsg, func()) {
	t.Helper()
	var mu sync.Mutex
	var got []sentMsg
	oldSend, oldGrace := embyDelSend, embyDelGrace
	embyDelSend = func(title, content string) {
		mu.Lock()
		got = append(got, sentMsg{title, content})
		mu.Unlock()
	}
	embyDelGrace = 30 * time.Millisecond
	embyDelMu.Lock()
	embyDelNotices = map[string]*embyDelNotice{}
	embyDelMu.Unlock()
	t.Cleanup(func() { embyDelSend, embyDelGrace = oldSend, oldGrace })
	return func() []sentMsg {
			mu.Lock()
			defer mu.Unlock()
			return append([]sentMsg(nil), got...)
		}, func() {
			time.Sleep(200 * time.Millisecond)
		}
}

// 一次删除：删除本身与深删结果合成一条，标题写明类型
func TestEmbyDeleteOneMessageWithDeepResult(t *testing.T) {
	got, wait := captureEmbyDel(t)
	trackEmbyDelete("81", embyDeleteTitle("电影"), "欢乐好声音 (2016)", func() deepDelOutcome {
		return deepDelOutcome{Res: deepDelResult{Fids: 1, Videos: 1, PanDirs: 1}}
	})
	wait()
	msgs := got()
	if len(msgs) != 1 {
		t.Fatalf("应只发一条，实际 %d 条: %+v", len(msgs), msgs)
	}
	if msgs[0].title != "🗑️ Emby 删除 · 电影" {
		t.Fatalf("标题 = %q", msgs[0].title)
	}
	for _, want := range []string{"欢乐好声音 (2016)", "已联动删除网盘源文件 1 个", "空目录 1 个", "回收站"} {
		if !strings.Contains(msgs[0].content, want) {
			t.Fatalf("内容缺 %q: %q", want, msgs[0].content)
		}
	}
}

// 神医 deep.delete + 原生 library.deleted 同一条目：只发一条，等两条的深删都跑完，
// 真正删了文件的那条结果不管先后都要进消息
func TestEmbyDeletePairedEventsMerge(t *testing.T) {
	got, wait := captureEmbyDel(t)
	release := make(chan struct{})
	trackEmbyDelete("571", embyDeleteTitle("电影"), "挽救计划", func() deepDelOutcome {
		return deepDelOutcome{} // 先跑完的那条没命中台账
	})
	trackEmbyDelete("571", embyDeleteTitle("电影"), "挽救计划", func() deepDelOutcome {
		<-release // 排锁中，比宽限期更久
		return deepDelOutcome{Res: deepDelResult{Fids: 2, Videos: 1, Assets: 1}}
	})
	time.Sleep(100 * time.Millisecond)
	if n := len(got()); n != 0 {
		t.Fatalf("深删没跑完不该先发，已发 %d 条", n)
	}
	close(release)
	wait()
	msgs := got()
	if len(msgs) != 1 || !strings.Contains(msgs[0].content, "已联动删除网盘源文件 2 个") {
		t.Fatalf("应合成一条且带深删结果: %+v", msgs)
	}
}

// 深删没动任何东西：只报删除本身
func TestEmbyDeleteWithoutDeepResult(t *testing.T) {
	got, wait := captureEmbyDel(t)
	trackEmbyDelete("9", embyDeleteTitle("剧集"), "某剧", func() deepDelOutcome { return deepDelOutcome{} })
	wait()
	msgs := got()
	if len(msgs) != 1 || msgs[0].content != "某剧" || msgs[0].title != "🗑️ Emby 删除 · 剧集" {
		t.Fatalf("%+v", msgs)
	}
}

// 不推的删除（目录条目 / 自产回声）：深删正常或没动时静默，被拦下时仍单报
func TestEmbyDeleteSilentUnlessRejected(t *testing.T) {
	got, wait := captureEmbyDel(t)
	trackEmbyDelete("a", "", "x", func() deepDelOutcome { return deepDelOutcome{} })
	trackEmbyDelete("b", "", "x", func() deepDelOutcome {
		return deepDelOutcome{Res: deepDelResult{Fids: 1, Videos: 1}}
	})
	wait()
	if n := len(got()); n != 0 {
		t.Fatalf("不推的删除发了 %d 条", n)
	}
	trackEmbyDelete("c", "", "x", func() deepDelOutcome { return deepDelOutcome{Rejected: "事件路径越界或指向根: a"} })
	wait()
	msgs := got()
	if len(msgs) != 1 || !strings.Contains(msgs[0].content, "已被拦下") {
		t.Fatalf("%+v", msgs)
	}
}

// 迟到的同条目事件不再发第二条；拿不到 key 的各发各的
func TestEmbyDeleteLateDuplicateAndEmptyKey(t *testing.T) {
	got, wait := captureEmbyDel(t)
	none := func() deepDelOutcome { return deepDelOutcome{} }
	trackEmbyDelete("1", embyDeleteTitle("电影"), "A", none)
	wait()
	trackEmbyDelete("1", embyDeleteTitle("电影"), "A", none)
	trackEmbyDelete("", embyDeleteTitle(""), "B", none)
	trackEmbyDelete("", embyDeleteTitle(""), "B", none)
	wait()
	if n := len(got()); n != 3 {
		t.Fatalf("应为 1（A）+ 2（B 各一条），实际 %d: %+v", n, got())
	}
}

func TestEmbyDeleteKind(t *testing.T) {
	for typ, want := range map[string]string{"Movie": "电影", "Series": "剧集", "Season": "季", "Episode": "单集"} {
		if l, ok := embyDeleteKind(typ); !ok || l != want {
			t.Fatalf("%s → %q %v", typ, l, ok)
		}
	}
	for _, typ := range []string{"Folder", "CollectionFolder", "AggregateFolder"} {
		if _, ok := embyDeleteKind(typ); ok {
			t.Fatalf("%s 不该推通知", typ)
		}
	}
	if embyDeleteTitle("") != "🗑️ Emby 删除" {
		t.Fatal("没有类型时不带后缀")
	}
}

// 网盘删除引起的删除只在精确路径上推「网盘删除」；Emby 顺手收掉的季 / 剧集按回声处理，
// 改名 / 移动清掉的旧位置（普通自产标记）照旧不推
func TestEmbyPanDeletedMarks(t *testing.T) {
	embySelfDelMu.Lock()
	embySelfDel = map[string]embySelfDelMark{}
	embySelfDelMu.Unlock()
	markEmbyDeleted("/media/动漫/凡人修仙传.2020", true)
	markEmbyDeleted("/media/剧集/某剧/Season 1/某剧.S01E02.strm", true)
	markEmbySelfDeleted("/media/电影/旧名.2020")

	if !embyPanDeleted(`\media\动漫\凡人修仙传.2020\`) || !embySelfDeleted("/media/动漫/凡人修仙传.2020") {
		t.Fatal("网盘删掉的剧集目录应认作网盘删除")
	}
	if embyPanDeleted("/media/剧集/某剧") || !embySelfDeletedRelated("/media/剧集/某剧") {
		t.Fatal("删一集后 Emby 收掉的剧集条目应按回声处理，不再推一条")
	}
	if embyPanDeleted("/media/电影/旧名.2020") || !embySelfDeleted("/media/电影/旧名.2020") {
		t.Fatal("改名 / 移动的旧位置仍是回声")
	}
	if panDeleteTitle("剧集") != "🗑️ 网盘删除 · 剧集" || panDeleteTitle("") != "🗑️ 网盘删除" {
		t.Fatal("标题")
	}
}
