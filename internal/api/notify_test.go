package api

import "testing"

func TestWecomAPIBase(t *testing.T) {
	cases := []struct {
		in   WecomConfig
		want string
	}{
		{WecomConfig{}, "https://qyapi.weixin.qq.com"},
		{WecomConfig{APIURL: "  "}, "https://qyapi.weixin.qq.com"},
		{WecomConfig{APIURL: "https://qyapi.example.com/"}, "https://qyapi.example.com"},
		{WecomConfig{APIURL: " https://qyapi.example.com// "}, "https://qyapi.example.com"},
	}
	for _, c := range cases {
		if got := c.in.apiBase(); got != c.want {
			t.Errorf("apiBase(%q) = %q, want %q", c.in.APIURL, got, c.want)
		}
	}
}
