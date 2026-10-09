package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// 每个 fid 都要显式 keep_both：不传时文件默认 replace（覆盖、不可恢复），见 move115Form 注释
func TestMove115FormKeepBoth(t *testing.T) {
	form := move115Form("999", []string{"11", "22"})
	if form.Get("pid") != "999" || form.Get("fid[0]") != "11" || form.Get("fid[1]") != "22" {
		t.Fatalf("pid / fid[i] 不对: %v", form)
	}
	var policy map[string]map[string]string
	if err := json.Unmarshal([]byte(form.Get("conflict_policy")), &policy); err != nil {
		t.Fatalf("conflict_policy 不是 JSON: %v", err)
	}
	if len(policy) != 2 || policy["11"]["action"] != "keep_both" || policy["22"]["action"] != "keep_both" {
		t.Fatalf("conflict_policy 应逐个 fid 写 keep_both: %v", policy)
	}
}

// 成功响应的 data 是 115 用私钥加密的，测试造不出来；这里只管失败的几种形态都报得清楚、不 panic
func TestParseWebDownloadPostErrors(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`<html>`, "非 JSON"},
		{`{"state":false,"errno":50015,"error":"文件不存在"}`, "文件不存在（errno=50015）"},
		{`{"state":false,"msg":"目录"}`, "目录"},
		{`{"state":true}`, "无加密数据"},
		{`{"state":true,"data":{"file_url":"x"}}`, "无加密数据"},
		{`{"state":true,"data":"!!不是base64"}`, "解密失败"},
	}
	for _, c := range cases {
		_, err := parseWebDownloadPost([]byte(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 想要含 %q 的错误，得到 %v", c.body, c.want, err)
		}
	}
}
