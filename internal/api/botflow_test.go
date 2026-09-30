package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// botFlowFixture 一个已经在资源列表里的会话，提交换成计数
func botFlowFixture(t *testing.T, key string, items []ResourceItem) (*botFlow, *[]string) {
	t.Helper()
	f := &botFlow{token: "tok", stage: "resource", media: wecomTmdbHit{ID: 1, Type: "movie", Title: "片名"},
		movies: []wecomTmdbHit{{ID: 1, Title: "片名"}, {ID: 2, Title: "别的"}}, items: items, sent: map[int]bool{}, at: time.Now()}
	botFlowPut(key, f)
	t.Cleanup(func() { botFlowDrop(key, nil) })
	var submitted []string
	old := botSubmit
	botSubmit = func(_ *Handler, it ResourceItem, _ func(...string)) bool {
		submitted = append(submitted, it.Title)
		return !strings.HasPrefix(it.Title, "fail")
	}
	t.Cleanup(func() { botSubmit = old })
	return f, &submitted
}

func botItems(n int) []ResourceItem {
	var out []ResourceItem
	for i := 1; i <= n; i++ {
		out = append(out, ResourceItem{Source: "pansou", Title: fmt.Sprintf("R%d", i), Action: "transfer", Relevant: true})
	}
	return out
}

var botNoIO = botIO{say: func(...string) {}}

// 旧按钮、失效会话、双击都不能多提交一次；提交完列表还在，可以接着挑别的
func TestBotFlowSubmitOnceAndKeepList(t *testing.T) {
	h := &Handler{}
	_, submitted := botFlowFixture(t, "t1", botItems(3))

	if v, _ := h.botAct("t1", "1", "old", botNoIO); !v.Toast || len(*submitted) != 0 {
		t.Fatal("旧按钮不应提交")
	}
	if v, ok := h.botAct("nobody", "1", "tok", botNoIO); !ok || !v.Toast {
		t.Fatal("会话不存在时按钮应提示失效")
	}
	if _, ok := h.botAct("nobody", "1", "", botNoIO); ok {
		t.Fatal("没有会话的纯文本不应被拦截")
	}
	v, _ := h.botAct("t1", "1", "tok", botNoIO)
	if !v.Refresh || !strings.Contains(v.text(), "已提交") {
		t.Fatalf("提交后应原地标出已提交: %+v", v)
	}
	h.botAct("t1", "1", "", botNoIO) // 按钮之后又回复了一次数字
	h.botAct("t1", "2", "tok", botNoIO)
	if strings.Join(*submitted, ",") != "R1,R2" {
		t.Fatalf("提交记录 %v", *submitted)
	}
}

// 提交失败放开，可以重试
func TestBotFlowFailedSubmitCanRetry(t *testing.T) {
	h := &Handler{}
	_, submitted := botFlowFixture(t, "t2", []ResourceItem{{Title: "fail", Action: "transfer", Relevant: true}})
	h.botAct("t2", "1", "", botNoIO)
	h.botAct("t2", "1", "", botNoIO)
	if len(*submitted) != 2 {
		t.Fatalf("失败后应能重试: %v", *submitted)
	}
}

// 翻页与序号：序号全局连续，翻到第二页回复 9 拿到的是第 9 条
func TestBotFlowPaging(t *testing.T) {
	h := &Handler{}
	_, submitted := botFlowFixture(t, "t3", botItems(10))
	if v, _ := h.botAct("t3", "p", "", botNoIO); !v.Toast {
		t.Fatal("第一页不能再往前翻")
	}
	v, _ := h.botAct("t3", "n", "", botNoIO)
	if !strings.Contains(v.text(), "第 2/2 页") || !strings.Contains(v.text(), "9. ") || strings.Contains(v.text(), "\n1. ") {
		t.Fatalf("第二页内容不对:\n%s", v.text())
	}
	var nums []string
	for _, row := range v.Buttons {
		for _, b := range row {
			nums = append(nums, b.Act)
		}
	}
	if !strings.HasPrefix(strings.Join(nums, ","), "9,10,0,p,") {
		t.Fatalf("第二页按钮 %v", nums)
	}
	if v, _ := h.botAct("t3", "n", "", botNoIO); !v.Toast {
		t.Fatal("最后一页不能再往后翻")
	}
	h.botAct("t3", "9", "", botNoIO)
	if strings.Join(*submitted, ",") != "R9" {
		t.Fatalf("提交记录 %v", *submitted)
	}
}

// 自动择优：跳过对不上的、只能打开原链接的、提交过的
func TestBotFlowAutoBest(t *testing.T) {
	h := &Handler{}
	items := []ResourceItem{
		{Title: "open", Action: "open", Relevant: true},
		{Title: "unrelated", Action: "transfer"},
		{Title: "before", Action: "offline", Relevant: true, SubmittedAt: 1},
		{Title: "best", Action: "offline", Relevant: true},
		{Title: "next", Action: "transfer", Relevant: true},
	}
	_, submitted := botFlowFixture(t, "t4", items)
	h.botAct("t4", "0", "", botNoIO)
	h.botAct("t4", "0", "", botNoIO)
	if strings.Join(*submitted, ",") != "best,next" {
		t.Fatalf("择优顺序 %v", *submitted)
	}
	if v, _ := h.botAct("t4", "0", "", botNoIO); !v.Toast || len(*submitted) != 2 {
		t.Fatal("没有可选的时候不能乱提交")
	}
}

// 返回选片、关闭
func TestBotFlowBackAndQuit(t *testing.T) {
	h := &Handler{}
	f, _ := botFlowFixture(t, "t5", botItems(2))
	v, _ := h.botAct("t5", "b", "", botNoIO)
	if f.stage != "media" || !strings.Contains(v.text(), "1. 片名") {
		t.Fatalf("应回到选片列表:\n%s", v.text())
	}
	v, _ = h.botAct("t5", "q", "", botNoIO)
	if v.Toast || botFlowGet("t5") != nil {
		t.Fatal("关闭应替换那屏并删掉会话")
	}
}

// 正在处理时再来的输入挡回去（企微每条消息一个 goroutine）
func TestBotFlowBusy(t *testing.T) {
	h := &Handler{}
	f, submitted := botFlowFixture(t, "t6", botItems(1))
	f.mu.Lock()
	v, _ := h.botAct("t6", "1", "", botNoIO)
	f.mu.Unlock()
	if !strings.Contains(v.text(), "稍候") || len(*submitted) != 0 {
		t.Fatal("忙的时候不应执行")
	}
}

// 过期会话清掉；新搜索顶掉旧会话后，旧会话收尾删不掉新的
func TestBotFlowStore(t *testing.T) {
	old := &botFlow{at: time.Now().Add(-botFlowTTL - time.Minute)}
	botFlowPut("t7", old)
	if botFlowGet("t7") != nil {
		t.Fatal("过期会话应清理")
	}
	a, b := &botFlow{at: time.Now()}, &botFlow{at: time.Now()}
	botFlowPut("t7", a)
	botFlowPut("t7", b)
	if botFlowDrop("t7", a) || botFlowGet("t7") != b {
		t.Fatal("旧会话不应删掉新会话")
	}
	botFlowDrop("t7", nil)
}

func TestResDedupe(t *testing.T) {
	items := []ResourceItem{
		{Source: "pansou", URL: "https://115cdn.com/s/abc?password=1234"},
		{Source: "tg", URL: "https://115.com/s/abc?password=1234"},
		{Source: "gy", Ref: "/a"},
		{Source: "gy", Ref: "/b"},
		{Source: "tg", URL: "magnet:?xt=urn:btih:ABCDEF"},
		{Source: "pansou", URL: "magnet:?xt=urn:btih:abcdef&dn=x"},
	}
	got := resDedupe(items)
	var srcs []string
	for _, it := range got {
		srcs = append(srcs, it.Source)
	}
	if strings.Join(srcs, ",") != "pansou,gy,gy,tg" {
		t.Fatalf("去重结果 %v", srcs)
	}
}

func TestBotFilter(t *testing.T) {
	items := []ResourceItem{{Title: "a", Relevant: true}, {Title: "b"}}
	got, note := botFilter(items, "X")
	if len(got) != 1 || !strings.Contains(note, "隐藏 1 条") {
		t.Fatalf("%v %s", got, note)
	}
	got, note = botFilter([]ResourceItem{{Title: "b"}}, "X")
	if len(got) != 1 || !strings.Contains(note, "仅供参考") {
		t.Fatalf("一条都对不上时应退回全部: %v %s", got, note)
	}
}

func TestWecomFindArgs(t *testing.T) {
	for _, c := range []struct{ in, src, kw string }{
		{"搜索 星际穿越", "", "星际穿越"}, {"so 星际", "", "星际"}, {"观影星际", "gy", "星际"},
		{"GY 星际", "gy", "星际"}, {"网盘 星际", "pansou", "星际"}, {"wp 星际", "pansou", "星际"},
	} {
		src, kw := wecomFindArgs(c.in)
		if src != c.src || kw != c.kw {
			t.Errorf("%q → %q %q", c.in, src, kw)
		}
	}
}
