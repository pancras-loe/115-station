package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKindWanted(t *testing.T) {
	if !kindWanted(nil, kindIngest) {
		t.Fatal("没勾 = 全推")
	}
	if kindWanted(nil, kindBot) || kindWanted([]string{"bot"}, kindBot) {
		t.Fatal("机器人回复永远不推")
	}
	if kindWanted([]string{"ingest"}, kindOrganize) || !kindWanted([]string{"ingest"}, kindIngest) {
		t.Fatal("勾了就只推勾的")
	}
}

func TestSendIyuu(t *testing.T) {
	var gotPath string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotForm = r.PostForm
		if strings.Contains(r.URL.Path, "BAD") {
			w.Write([]byte(`{"errcode":1,"errmsg":"token 无效"}`))
			return
		}
		w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()
	old := iyuuBaseURL
	iyuuBaseURL = srv.URL
	defer func() { iyuuBaseURL = old }()

	if err := sendIyuu(IyuuConfig{Token: "IYUU1T2"}, notifyMsg{Title: "✓ 入库", Content: "片名 (2024)\n第二行"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/IYUU1T2.send" || gotForm.Get("text") != "✓ 入库" || gotForm.Get("desp") != "片名 (2024)\n第二行" {
		t.Fatalf("请求不对: %s %v", gotPath, gotForm)
	}
	// 标题空：正文第一行顶上去当标题
	if err := sendIyuu(IyuuConfig{Token: "IYUU1T2"}, notifyMsg{Content: "封面生成完成\n3 个库"}); err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("text") != "封面生成完成" || gotForm.Get("desp") != "3 个库" {
		t.Fatalf("标题空时没拿第一行: %v", gotForm)
	}
	if err := sendIyuu(IyuuConfig{Token: "BAD"}, notifyMsg{Title: "x"}); err == nil || !strings.Contains(err.Error(), "token 无效") {
		t.Fatalf("errcode 非 0 要报错: %v", err)
	}
}

func TestWebhookDefaultJSON(t *testing.T) {
	m := notifyMsg{Kind: kindIngest, Title: "入库", Content: "片名\n\"引号\"", Image: "https://x/p.jpg", At: time.Unix(0, 0)}
	req, err := buildWebhookRequest(WebhookConfig{URL: "https://hook.example/a"}, m)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("默认 POST JSON: %s %s", req.Method, req.Header.Get("Content-Type"))
	}
	b, _ := io.ReadAll(req.Body)
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "ingest" || got["type_name"] != "入库" || got["content"] != "片名\n\"引号\"" ||
		got["text"] != "入库\n片名\n\"引号\"" || got["image"] != "https://x/p.jpg" || got["source"] != "115-Station" {
		t.Fatalf("载荷不对: %v", got)
	}
}

func TestWebhookBodyTemplate(t *testing.T) {
	m := notifyMsg{Kind: kindOrganize, Title: "标\"题", Content: "一\n二"}
	req, _ := buildWebhookRequest(WebhookConfig{URL: "https://h/x", Body: `{"msgtype":"text","text":{"content":"{{text}}"},"t":"{{type_name}}"}`}, m)
	b, _ := io.ReadAll(req.Body)
	var got struct {
		Text struct{ Content string } `json:"text"`
		T    string                   `json:"t"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("换值后 JSON 被弄坏: %v %s", err, b)
	}
	if got.Text.Content != "标\"题\n一\n二" || got.T != "整理" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("模板不对: %+v", got)
	}
	// 不是 JSON 的模板照原样发、不冒充 JSON
	req, _ = buildWebhookRequest(WebhookConfig{URL: "https://h/x", Body: "{{title}}: {{content}}"}, m)
	b, _ = io.ReadAll(req.Body)
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("纯文本模板的类型: %s", req.Header.Get("Content-Type"))
	}
	_ = b
}

func TestWebhookGet(t *testing.T) {
	m := notifyMsg{Kind: kindSystem, Title: "有新版本", Content: "v26.10.27 / 说明"}
	// 地址里写了变量（Bark）：按路径转义填进去，不另加查询参数
	req, _ := buildWebhookRequest(WebhookConfig{URL: "https://api.day.app/KEY/{{title}}/{{content}}", Method: "get"}, m)
	if req.Method != http.MethodGet || req.URL.RawQuery != "" ||
		req.URL.EscapedPath() != "/KEY/"+url.PathEscape("有新版本")+"/"+url.PathEscape("v26.10.27 / 说明") {
		t.Fatalf("Bark 地址不对: %s", req.URL.String())
	}
	// 没写变量：原有查询参数保留，内容追加在后面
	req, _ = buildWebhookRequest(WebhookConfig{URL: "https://h/x?key=abc", Method: "GET"}, m)
	q := req.URL.Query()
	if q.Get("key") != "abc" || q.Get("title") != "有新版本" || q.Get("type") != "system" {
		t.Fatalf("查询参数不对: %s", req.URL.String())
	}
}

func TestSendWebhookStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("nope"))
		}
	}))
	defer srv.Close()
	if err := sendWebhook(WebhookConfig{URL: srv.URL + "/ok"}, notifyMsg{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := sendWebhook(WebhookConfig{URL: srv.URL + "/bad"}, notifyMsg{Title: "x"}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("非 2xx 要报错: %v", err)
	}
}

// 队列按间隔发、按发送时的配置判断：这里只测限速，配置读不到时整条跳过
func TestPushQueueGap(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	q := &pushQueue{name: "测试", gap: 120 * time.Millisecond, send: func(*MessageConfig, notifyMsg) error {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		return nil
	}}
	n := func() int { mu.Lock(); defer mu.Unlock(); return len(at) }
	msgCfgCache.Lock()
	msgCfgCache.cfg, msgCfgCache.at = &MessageConfig{}, time.Now().Add(time.Hour)
	msgCfgCache.Unlock()
	defer invalidateMsgCfgCache()
	for i := 0; i < 3; i++ {
		q.put(notifyMsg{Title: "x"})
	}
	deadline := time.Now().Add(2 * time.Second)
	for n() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 3 {
		t.Fatalf("只发了 %d 条", len(at))
	}
	if at[1].Sub(at[0]) < 100*time.Millisecond || at[2].Sub(at[1]) < 100*time.Millisecond {
		t.Fatalf("没按间隔发: %v", at)
	}
}
