package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseOfflineTasksFields(t *testing.T) {
	var raws []map[string]interface{}
	json.Unmarshal([]byte(`[
		{"name":"a","info_hash":"AB12","status":-1,"percentDone":0,"add_time":1700000000,"size":"8500000000","url":"magnet:?xt=urn:btih:ab12"},
		{"name":"b","info_hash":"cd34","status":"1","percentDone":"46.5","add_time":"1700003600"},
		{"name":"c","info_hash":"ef56","status":2,"percent":100,"del_time":1700007200},
		{"info_hash":"no-name","status":-1}
	]`), &raws)
	got := parseOfflineTasks(raws)
	if len(got) != 3 {
		t.Fatalf("没名字的任务应跳过，得到 %d 条", len(got))
	}
	a, b, c := got[0], got[1], got[2]
	if a.hash != "AB12" || a.status != -1 || a.addTime != 1700000000 || a.size != 8500000000 || a.url == "" {
		t.Fatalf("a 解析错: %+v", a)
	}
	if b.status != 1 || b.percent != 46 || b.addTime != 1700003600 {
		t.Fatalf("b 解析错（字符串状态 / 进度 / 时间）: %+v", b)
	}
	if c.status != 2 || c.percent != 100 || c.delTime != 1700007200 {
		t.Fatalf("c 解析错（老字段 percent / del_time）: %+v", c)
	}
}

func TestPickOffCleanTasks(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ago := func(h int) int64 { return now.Add(-time.Duration(h) * time.Hour).Unix() }
	tasks := []offlineTaskInfo{
		{hash: "f1", name: "失败的本站任务", status: -1, addTime: ago(1)},
		{hash: "f2", name: "失败的 App 任务", status: -1, addTime: ago(1)},
		{hash: "s1", name: "卡了 30 小时", status: 1, percent: 16, addTime: ago(30)},
		{hash: "s2", name: "下了 2 小时", status: 1, percent: 46, addTime: ago(2)},
		{hash: "q1", name: "排队 25 小时", status: 0, addTime: ago(25)},
		{hash: "n1", name: "添加时间读不出", status: 1},
		{hash: "d1", name: "完成的", status: 2, addTime: ago(100)},
		{hash: "", name: "没有 hash", status: -1},
		{hash: "f1", name: "重复", status: -1},
	}
	mine := func(x offlineTaskInfo) (bool, uint) {
		if x.hash == "f2" {
			return false, 0
		}
		return true, 7
	}
	names := func(hs []offCleanHit) map[string]bool {
		m := map[string]bool{}
		for _, h := range hs {
			m[h.Hash] = true
		}
		return m
	}

	got := names(pickOffCleanTasks(tasks, offCleanRule{Failed: true, StuckHours: 24}, now, mine))
	want := map[string]bool{"f1": true, "s1": true, "q1": true}
	if len(got) != len(want) {
		t.Fatalf("默认规则挑错: %v", got)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("漏了 %s: %v", k, got)
		}
	}

	// 全部：App 里加的失败任务也删
	if g := names(pickOffCleanTasks(tasks, offCleanRule{Failed: true, StuckHours: 24, All: true}, now, mine)); !g["f2"] {
		t.Fatalf("全部模式应包含 App 任务: %v", g)
	}
	// 只删卡住的
	if g := names(pickOffCleanTasks(tasks, offCleanRule{StuckHours: 24}, now, mine)); g["f1"] || !g["s1"] {
		t.Fatalf("关掉失败后不该删失败的: %v", g)
	}
	// 卡住 = 0 不删卡住的
	if g := names(pickOffCleanTasks(tasks, offCleanRule{Failed: true}, now, mine)); g["s1"] || g["q1"] || !g["f1"] {
		t.Fatalf("卡住 0 小时 = 不删卡住的: %v", g)
	}
}

func TestCheckOffCleanReq(t *testing.T) {
	accts := []rbAcct{{id: 1}, {id: 2}}
	if err := checkOffCleanReq(offCleanReq{StuckHours: 24, Accounts: []uint{1, 2}}, accts); err != nil {
		t.Fatal(err)
	}
	if checkOffCleanReq(offCleanReq{StuckHours: -1}, accts) == nil {
		t.Fatal("负数小时应拒绝")
	}
	if checkOffCleanReq(offCleanReq{Accounts: []uint{3}}, accts) == nil {
		t.Fatal("不存在的账号应拒绝")
	}
}

func TestOffCleanSummaryNoDelete(t *testing.T) {
	msg, ok := offCleanSummary([]offCleanAcctResult{{Account: "主号", Total: 5}}, false)
	if !ok || msg == "" {
		t.Fatalf("%q %v", msg, ok)
	}
	_, ok = offCleanSummary([]offCleanAcctResult{{Account: "主号", Error: "没登录 115"}}, false)
	if ok {
		t.Fatal("出错应报不 ok")
	}
}
