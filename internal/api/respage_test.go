package api

import "testing"

func TestResPageURL(t *testing.T) {
	const base = "https://re0.me"
	cases := []struct {
		name string
		it   ResourceItem
		want string
	}{
		{"分享补提取码", ResourceItem{Kind: "share115", URL: "https://115cdn.com/s/swabc123", Code: "re71"}, "https://115cdn.com/s/swabc123?password=re71"},
		{"链接已带提取码", ResourceItem{Kind: "share115", URL: "https://115.com/s/swabc123?password=re71#", Code: "re71"}, "https://115.com/s/swabc123?password=re71#"},
		{"没有提取码原样", ResourceItem{Kind: "share115", URL: "https://115.com/s/swabc123"}, "https://115.com/s/swabc123"},
		{"RE0 未解锁", ResourceItem{Source: "re0", Kind: "share115", Ref: "abcdef0123456789abcd"}, base + "/resource/115/abcdef0123456789abcd"},
		{"RE0 ed2k 没页面", ResourceItem{Source: "re0", Kind: "ed2k", Ref: "abcdef0123456789abcd"}, ""},
		{"磁力没页面", ResourceItem{Kind: "magnet", URL: "magnet:?xt=urn:btih:abc"}, ""},
	}
	for _, c := range cases {
		if got := resPageURL(c.it, base); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
