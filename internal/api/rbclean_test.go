package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPickExpiredRb(t *testing.T) {
	items := []rbItem{
		{id: "1", dtime: 100, size: 10}, // 早于 cutoff：删
		{id: "2", dtime: 300, size: 20}, // 保留期内
		{id: "3", dtime: 0, size: 30},   // 删除时间读不出来：不删
		{id: "4", dtime: 199, size: 40}, // 删
		{id: "1", dtime: 100, size: 10}, // 翻页重复：只算一次
		{id: "5,6", dtime: 50},          // 不是纯数字：不删（tid 是逗号拼的）
		{id: "", dtime: 50},
		{id: "7", dtime: 200}, // 正好等于 cutoff：保留
	}
	ids, size := pickExpiredRb(items, 200)
	if !reflect.DeepEqual(ids, []string{"1", "4"}) || size != 50 {
		t.Fatalf("ids=%v size=%d", ids, size)
	}
}

func TestParseRecycleBinPage(t *testing.T) {
	body := []byte(`{"state":true,"count":"3","data":[
		{"id":"111","file_name":"a","dtime":"1700000000","file_size":"1024"},
		{"id":222,"file_name":"b","dtime":1700000001,"file_size":0}]}`)
	items, count, err := parseRecycleBinPage(body)
	if err != nil || count != 3 || len(items) != 2 {
		t.Fatalf("items=%v count=%d err=%v", items, count, err)
	}
	if items[0] != (rbItem{id: "111", dtime: 1700000000, size: 1024}) || items[1].id != "222" || items[1].dtime != 1700000001 {
		t.Fatalf("items=%+v", items)
	}
	if _, _, err := parseRecycleBinPage([]byte(`{"state":false,"error":"登录超时"}`)); err == nil || !strings.Contains(err.Error(), "登录超时") {
		t.Fatalf("err=%v", err)
	}
}

// 没有要删的条目时绝不能发请求：不带 tid 的 /rb/secret_del 是整个清空
func TestDelete115RecycleBinEmptyNoRequest(t *testing.T) {
	if err := delete115RecycleBin("bad-cookie", nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRbCleanSummary(t *testing.T) {
	msg, ok := rbCleanSummary([]rbAcctResult{{Account: "a", KeepDays: 30, Total: 5, Expired: 2, Deleted: 2, Size: 1 << 30}}, false)
	if !ok || !strings.Contains(msg, "彻底删除 2 / 2 条 30 天前删除的") {
		t.Fatalf("%v %s", ok, msg)
	}
	// 每个账号按自己的天数写
	msg, ok = rbCleanSummary([]rbAcctResult{
		{Account: "主", KeepDays: 30, Total: 3},
		{Account: "副", KeepDays: 7, Total: 4, Expired: 1},
		{Account: "三", KeepDays: 7, Error: "读回收站失败: x"},
	}, true)
	if ok || !strings.Contains(msg, "「主」回收站 3 条，没有 30 天前删除的") ||
		!strings.Contains(msg, "「副」回收站 4 条，其中 1 条是 7 天前删除的") || !strings.Contains(msg, "「三」✗ 读回收站失败") {
		t.Fatalf("%v %s", ok, msg)
	}
}

// 只清开着的账号，各用各的天数和密钥；没设过的账号（新加的）默认不清
func TestRbCleanPlans(t *testing.T) {
	cfg := rbCleanCfg{Accts: map[string]rbAcctCfg{
		"1": {On: true, KeepDays: 30, Password: "111111"},
		"2": {On: true},               // 天数没填按 30
		"3": {On: false, KeepDays: 7}, // 关着
	}}
	accts := []rbAcct{{id: 1, main: true}, {id: 2}, {id: 3}, {id: 4}}
	plans := rbCleanPlans(cfg, accts)
	if len(plans) != 2 || plans[0].acct.id != 1 || plans[0].keepDays != 30 || plans[0].password != "111111" ||
		plans[1].acct.id != 2 || plans[1].keepDays != rbCleanDefaultDays {
		t.Fatalf("%+v", plans)
	}
}

// 没登录的账号不读回收站，直接报原因
func TestRunRbCleanNotLoggedIn(t *testing.T) {
	res := runRbClean([]rbPlan{{acct: rbAcct{id: 2, label: "副"}, keepDays: 7}}, false)
	if len(res) != 1 || res[0].Error != "没登录 115" || res[0].KeepDays != 7 {
		t.Fatalf("%+v", res)
	}
}

func TestRbAcctReqs(t *testing.T) {
	accts := []rbAcct{{id: 1}, {id: 2}}
	pw := func(s string) *string { return &s }
	if err := checkRbAcctReqs([]rbAcctReq{{ID: 9, KeepDays: 7}}, accts); err == nil {
		t.Fatal("不存在的账号要拒")
	}
	if err := checkRbAcctReqs([]rbAcctReq{{ID: 1, KeepDays: 0}}, accts); err == nil {
		t.Fatal("0 天要拒")
	}
	if err := checkRbAcctReqs([]rbAcctReq{{ID: 1, KeepDays: 7, Password: pw("12")}}, accts); err == nil {
		t.Fatal("密钥不是 6 位要拒")
	}

	cfg := rbCleanCfg{Accts: map[string]rbAcctCfg{
		"1": {On: true, KeepDays: 30, Password: "111111"},
		"2": {On: true, KeepDays: 90, Password: "222222"},
	}}
	// 1 不带密钥 = 不改；请求里没列的 2 原样不动
	applyRbAcctReqs(&cfg, []rbAcctReq{{ID: 1, On: false, KeepDays: 7}})
	if a := cfg.Accts["1"]; a.On || a.KeepDays != 7 || a.Password != "111111" {
		t.Fatalf("%+v", a)
	}
	if a := cfg.Accts["2"]; !a.On || a.KeepDays != 90 || a.Password != "222222" {
		t.Fatalf("%+v", a)
	}
	// 掩码原样回传 = 不改（界面回显的是掩码）
	if err := checkRbAcctReqs([]rbAcctReq{{ID: 1, KeepDays: 7, Password: pw(settingMask)}}, accts); err != nil {
		t.Fatal(err)
	}
	applyRbAcctReqs(&cfg, []rbAcctReq{{ID: 1, On: true, KeepDays: 7, Password: pw(settingMask)}})
	if cfg.Accts["1"].Password != "111111" {
		t.Fatal("掩码不能覆盖已存的密钥")
	}
	// 通用接口读 rbclean 要掩掉密钥，眼睛查看按点路径取得到
	raw := `{"accounts":{"1":{"on":true,"keep_days":7,"password":"111111"}}}`
	if !isSensitiveSetting("rbclean") || strings.Contains(maskSensitiveJSON(raw), "111111") {
		t.Fatal("rbclean 的密钥要掩码")
	}
	var root any
	_ = json.Unmarshal([]byte(raw), &root)
	if secretAt(root, []string{"accounts", "1", "password"}) != "111111" {
		t.Fatal("点路径取不到密钥")
	}
	// 带空串 = 清掉
	applyRbAcctReqs(&cfg, []rbAcctReq{{ID: 2, On: true, KeepDays: 90, Password: pw("")}})
	if cfg.Accts["2"].Password != "" {
		t.Fatal("空串要清掉密钥")
	}
}

func TestRbCleanPasswordOK(t *testing.T) {
	for pw, want := range map[string]bool{"": true, "000000": true, "12345": false, "abcdef": false, "1234567": false} {
		if rbCleanPasswordOK(pw) != want {
			t.Errorf("%q", pw)
		}
	}
}
