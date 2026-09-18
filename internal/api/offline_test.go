package api

import (
	"encoding/json"
	"testing"
)

// 115 离线任务接口响应形态多变，解析器需全部兼容
func TestExtractTaskItems(t *testing.T) {
	cases := []string{
		`[{"name":"a.mkv","percent":45.5}]`,                                            // 顶层数组
		`{"state":true,"data":[{"name":"a.mkv"}]}`,                                      // data 数组
		`{"state":true,"info":[{"name":"a.mkv"}]}`,                                      // info 数组
		`{"state":true,"data":{"tasks":[{"name":"a.mkv"}],"count":1}}`,                  // data.tasks
		`{"state":true,"info":{"list":[{"name":"a.mkv"}]}}`,                             // info.list
		`{"state":true,"tasks":[{"name":"a.mkv"}]}`,                                     // 顶层 tasks
		`{"state":true,"data":{"info":[{"name":"a.mkv"}]}}`,                             // data.info
	}
	for i, c := range cases {
		items, ok := extractTaskItems([]byte(c))
		if !ok || len(items) != 1 || items[0]["name"] != "a.mkv" {
			t.Errorf("case %d failed: ok=%v items=%v", i, ok, items)
		}
	}
	if _, ok := extractTaskItems([]byte(`{"state":true}`)); ok {
		t.Error("empty response should not parse")
	}
	_ = json.Marshal
}

func TestClassifyLinkShareDomains(t *testing.T) {
	cases := []struct {
		link string
		want string
	}{
		// 115cdn.com/s/ 曾被误判为 http 交给离线下载（115 假成功建空壳目录）
		{"https://115cdn.com/s/swsvlam3nqo?password=9527#", "share"},
		{"https://115.com/s/abc123?password=xxxx", "share"},
		{"https://anxia.com/s/abcd1234", "share"},
		{"magnet:?xt=urn:btih:ABC", "magnet"},
		{"https://example.com/file.mp4", "http"},
	}
	for _, c := range cases {
		if got := classifyLink(c.link); got != c.want {
			t.Errorf("classifyLink(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

func TestIs115BusyResp(t *testing.T) {
	busy := `{"state":false,"error":"服务器开小差了，稍后再试吧","errNo":0}`
	ok := `{"state":false,"error":"分享不存在","errNo":0}`
	if !is115BusyResp([]byte(busy)) {
		t.Errorf("开小差响应应识别为风控")
	}
	if is115BusyResp([]byte(ok)) {
		t.Errorf("业务错误不应误判为风控")
	}
}
