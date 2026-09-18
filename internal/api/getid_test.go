package api

import "testing"

// files/getid 响应形态解析：主形态为顶层 id（openStrm 生产验证），
// 兼容 data 数组/对象等历史形态；目录不存在时 found=false 且 err=nil。
func TestGetidCidFromResponse(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		cid   string
		found bool
		isErr bool
	}{
		// 主形态：顶层 id（数字）
		{"top-level id", `{"state":true,"id":3499491791101494604}`, "3499491791101494604", true, false},
		// 顶层 id 为字符串（部分接口形态）
		{"top-level id string", `{"state":true,"id":"123456"}`, "123456", true, false},
		// 目录不存在：state=false（正常业务结果，非错误）
		{"not exist", `{"state":false,"error":"errFilesNotExist","errno":0}`, "", false, false},
		// id=0 根目录约定为无效
		{"id zero", `{"state":true,"id":0}`, "", false, false},
		// 兼容：data 数组 cid
		{"data array cid", `{"state":true,"data":[{"cid":"789","name":"x"}]}`, "789", true, false},
		// 兼容：data 对象 id
		{"data object id", `{"state":true,"data":{"id":456}}`, "456", true, false},
		// 兼容：data 对象 cid
		{"data object cid", `{"state":true,"data":{"cid":"999"}}`, "999", true, false},
		// state=true 但无任何 id → found=false（并打诊断日志）
		{"state true no id", `{"state":true,"data":[]}`, "", false, false},
		// 非法 JSON → 错误
		{"invalid json", `not-json`, "", false, true},
	}
	for _, c := range cases {
		cid, found, err := getidCidFromResponse([]byte(c.body), "影视测试/俱乐部")
		if c.isErr {
			if err == nil {
				t.Errorf("%s: want error, got nil", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if cid != c.cid || found != c.found {
			t.Errorf("%s: got (cid=%q, found=%v), want (%q, %v)", c.name, cid, found, c.cid, c.found)
		}
	}
}
