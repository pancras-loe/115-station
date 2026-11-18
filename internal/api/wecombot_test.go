package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSplitShareLink(t *testing.T) {
	cases := []struct {
		in   string
		url  string
		code string
	}{
		{"https://115.com/s/abc123?password=xyz9", "https://115.com/s/abc123?password=xyz9", "xyz9"},
		{"https://115.com/s/abc123 提取码：ab12", "https://115.com/s/abc123", "ab12"},
		{"https://115.com/s/abc123 提取码:ab12", "https://115.com/s/abc123", "ab12"},
		{"https://115.com/s/abc123 ab12cd", "https://115.com/s/abc123", "ab12cd"},
		{"我用115网盘分享了文件\n链接：https://115.com/s/abc123?password=xy89", "https://115.com/s/abc123?password=xy89", "xy89"},
		{"[打开分享](https://115.com/s/abc123?password=xy89)", "https://115.com/s/abc123?password=xy89", "xy89"},
		{"链接：https://115cdn.com/s/abc_123\n访问码： xy89", "https://115cdn.com/s/abc_123", "xy89"},
		{"https://anxia.com/s/abc-123#xy89", "https://anxia.com/s/abc-123#xy89", "xy89"},
		{"https://115.com/s/abc123", "https://115.com/s/abc123", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		u, code := splitShareLink(c.in)
		if u != c.url || code != c.code {
			t.Errorf("splitShareLink(%q) = (%q, %q), want (%q, %q)", c.in, u, code, c.url, c.code)
		}
	}
}

func TestClassifyLinkBare(t *testing.T) {
	if classifyLink("magnet:?xt=urn:btih:abc") != "magnet" {
		t.Error("magnet not classified")
	}
	if classifyLink("https://115.com/s/abc123") != "share" {
		t.Error("share not classified")
	}
	if classifyLink("你好") != "" {
		t.Error("plain text should not classify")
	}
}

func TestBotDownloadLinksBatch(t *testing.T) {
	const (
		ed2k1 = "ed2k://|file|第 1 集.mkv|42|0123456789abcdef0123456789abcdef|/"
		ed2k2 = "ed2k://|file|第 2 集.mkv|43|89abcdef0123456789abcdef01234567|/"
	)
	text := ed2k1 + ed2k2 + "\n" +
		"链接：https://115.com/s/share1\n访问码：ab12\n" +
		"https://115cdn.com/s/share2#cd34\n" +
		"https://anxia.com/s/share3\nFilm 4\n" +
		"https://115.com/s/share4 ef56\n" +
		"https://115cdn.com/s/share1?password=ZZZZ\n" +
		ed2k1 + "\n来自频道 https://example.com/details"
	want := []botDownloadLink{
		{kind: "ed2k", url: ed2k1},
		{kind: "ed2k", url: ed2k2},
		{kind: "share", url: "https://115.com/s/share1", code: "ab12"},
		{kind: "share", url: "https://115cdn.com/s/share2#cd34", code: "cd34"},
		{kind: "share", url: "https://anxia.com/s/share3"},
		{kind: "share", url: "https://115.com/s/share4", code: "ef56"},
	}
	got := botDownloadLinks(text)
	if !slices.Equal(got, want) {
		t.Fatalf("批量链接解析成 %#v，期望 %#v", got, want)
	}

	// 「每次问」保留整批分享与各自的提取码，且一次回复只消费一批。
	const user = "test-batch-shares"
	shareBotRemember(user, got[2:])
	if pending := shareBotTake(user); !slices.Equal(pending, want[2:]) {
		t.Fatalf("待处理分享 = %#v", pending)
	}
	if pending := shareBotTake(user); len(pending) != 0 {
		t.Fatalf("已取出的待处理分享又出现了：%#v", pending)
	}
}

func TestWecomBotOfflineBatchContinuesAfterFailure(t *testing.T) {
	h, _, cfg := mediaLibsSetup(t)
	if err := cfg.SaveSetting("share", `{"folder":"123"}`); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveCookie("UID=123_A1; CID=test; SEID=test"); err != nil {
		t.Fatal(err)
	}
	oldPolls := offlineFastPolls.Load()
	t.Cleanup(func() { offlineFastPolls.Store(oldPolls) })

	var links, submitted []string
	for i := 1; i <= 15; i++ {
		links = append(links, fmt.Sprintf("ed2k://|file|第 %02d 集.mkv|42|%032x|/", i, i))
	}
	transport := &http.Transport{}
	transport.RegisterProtocol("https", fullSyncTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/web/lixian/" || r.URL.Query().Get("ac") != "add_task_url" {
			t.Errorf("意外请求 %s %s", r.Method, r.URL)
		}
		if got := r.FormValue("wp_path_id"); got != "123" {
			t.Errorf("转存目录 = %q", got)
		}
		link := r.FormValue("url")
		submitted = append(submitted, link)
		w := httptest.NewRecorder()
		if link == links[1] {
			fmt.Fprint(w, `{"state":false,"error":"链接已失效"}`)
		} else {
			fmt.Fprint(w, `{"state":true}`)
		}
		return w.Result(), nil
	}))
	previous := http115Transport
	http115Transport = transport
	t.Cleanup(func() { http115Transport = previous })

	replies := make(chan string, 2)
	h.handleBotCommand("test-wecom", strings.Join(links, "\n"), func(lines ...string) {
		replies <- strings.Join(lines, "\n")
	})
	var start, result string
	select {
	case start = <-replies:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到批量提交提示")
	}
	select {
	case result = <-replies:
	case <-time.After(5 * time.Second):
		t.Fatal("没收到批量结果")
	}
	if !strings.Contains(start, "15 条链接") || !slices.Equal(submitted, links) {
		t.Fatalf("识别结果 %q，实际提交 %v", start, submitted)
	}
	if !strings.Contains(result, "2. 第 02 集.mkv\n✗ 提交失败: 链接已失效") ||
		!strings.Contains(result, "15. 第 15 集.mkv\n✓ 已提交离线下载") {
		t.Fatalf("批量结果 = %q", result)
	}
}
