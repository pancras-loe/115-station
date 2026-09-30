package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretAt(t *testing.T) {
	var root any
	_ = json.Unmarshal([]byte(`{"api_key":"k1","wecom":{"secret":"s1","agent_id":1}}`), &root)
	cases := map[string]string{
		"api_key":        "k1",
		"wecom.secret":   "s1",
		"wecom.agent_id": "", // 不是字符串
		"wecom.nope":     "",
		"api_key.x":      "", // 路径穿过了字符串
	}
	for path, want := range cases {
		if got := secretAt(root, strings.Split(path, ".")); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if secretAt(nil, []string{"a"}) != "" {
		t.Error("空配置应返回空")
	}
}

