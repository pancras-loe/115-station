package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTGPrivateAuthorization(t *testing.T) {
	cfg := TGConfig{Enabled: true, ChatID: "42"}
	for _, tc := range []struct {
		name, kind, id     string
		chat, user         int64
		bot, enabled, want bool
	}{
		{"本人私聊", "private", "42", 42, 42, false, true, true},
		{"其他私聊", "private", "42", 43, 43, false, true, false},
		{"群聊即使匹配也拒绝", "supergroup", "-42", -42, 42, false, true, false},
		{"伪造按钮操作者", "private", "42", 42, 43, false, true, false},
		{"机器人", "private", "42", 42, 42, true, true, false},
		{"空配置", "private", "", 42, 42, false, true, false},
		{"已禁用", "private", "42", 42, 42, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.ChatID, cfg.Enabled = tc.id, tc.enabled
			if got := tgAllowed(cfg, &tgMessage{Chat: tgChat{ID: tc.chat, Type: tc.kind}}, tgUser{ID: tc.user, Bot: tc.bot}); got != tc.want {
				t.Fatalf("授权结果 %v，期望 %v", got, tc.want)
			}
		})
	}
	if tgAllowed(cfg, nil, tgUser{}) {
		t.Fatal("空消息不能授权")
	}
}

func TestTGCommandsExcludeRemovedOperations(t *testing.T) {
	for _, text := range []string{"/enrich", "enrich", "补全", "/libraries", "建库", "创建媒体库", "alist", "清空115"} {
		if cmd, _ := tgCommand(text); cmd != "unknown" {
			t.Fatalf("不应开放 %q: %s", text, cmd)
		}
	}
	for _, tc := range []struct{ text, cmd, arg string }{
		{"/wp 星际穿越", "wp", "星际穿越"}, {"网盘星际穿越", "wp", "星际穿越"},
		{"/sync", "sync", ""}, {"整理", "organize", ""}, {"magnet:?xt=urn:btih:abc", "download", "magnet:?xt=urn:btih:abc"},
		{"/download https://115.com/s/abc 提取码:1234", "download", "https://115.com/s/abc 提取码:1234"},
	} {
		cmd, arg := tgCommand(tc.text)
		if cmd != tc.cmd || arg != tc.arg {
			t.Fatalf("解析 %q 得到 %q %q", tc.text, cmd, arg)
		}
	}
}

func TestTGSelectionIsBoundAndConsumedOnce(t *testing.T) {
	now := time.Now()
	calls := 0
	s := &tgSelection{token: "new", chat: 42, user: 42, until: now.Add(time.Minute), choices: []tgChoice{{run: func() { calls++ }}}}
	for _, tc := range []struct {
		token      string
		chat, user int64
		n          int
	}{{"old", 42, 42, 1}, {"new", 43, 42, 1}, {"new", 42, 43, 1}, {"new", 42, 42, 2}} {
		if fn, _ := takeSelection(&s, tc.token, tc.n, tc.chat, tc.user, now); fn != nil {
			t.Fatal("旧按钮、越权或越界选择不应执行")
		}
		if s == nil {
			t.Fatal("错误按钮不应销毁当前有效会话")
		}
	}
	fn, _ := takeSelection(&s, "new", 1, 42, 42, now)
	if fn == nil {
		t.Fatal("有效选择未执行")
	}
	fn()
	if fn, _ := takeSelection(&s, "new", 1, 42, 42, now); fn != nil {
		fn()
	}
	if calls != 1 {
		t.Fatalf("重复提交 %d 次", calls)
	}
	s = &tgSelection{until: now.Add(-time.Second)}
	if fn, _ := takeSelection(&s, "", 1, 42, 42, now); fn != nil || s != nil {
		t.Fatal("过期会话未清理")
	}
}

func TestTGReplySplitsWithoutChangingTarget(t *testing.T) {
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Chat   int64           `json:"chat_id"`
			Text   string          `json:"text"`
			Markup json.RawMessage `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Chat != 42 {
			t.Errorf("回复发错目标 %d", p.Chat)
		}
		texts = append(texts, p.Text)
		if len(texts) < 3 && len(p.Markup) > 0 {
			t.Error("按钮只能放末段")
		}
		if len(texts) == 3 && len(p.Markup) == 0 {
			t.Error("末段缺少按钮")
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	a := &tgAPI{base: server.URL, token: "test", client: server.Client()}
	want := strings.Repeat("中文😀<>&", 700)
	if err := a.send(context.Background(), 42, want, [][]tgButton{{{Text: "选择", Data: "pick:x:1"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != want {
		t.Fatal("分段破坏正文")
	}
}

func TestTGAPIRedactsTokenAndHandlesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"secret-token","parameters":{"retry_after":12}}`)
	}))
	defer server.Close()
	a := &tgAPI{base: server.URL, token: "secret-token", client: server.Client()}
	err := a.call(context.Background(), "getUpdates", nil, nil)
	if err == nil || strings.Contains(err.Error(), a.token) {
		t.Fatalf("错误泄露凭据或缺失: %v", err)
	}
	if tgRetryDelay(err, time.Second) != 12*time.Second {
		t.Fatal("未尊重限流等待")
	}
	server.Close()
	err = a.call(context.Background(), "getMe", nil, nil)
	if err == nil || strings.Contains(err.Error(), a.token) {
		t.Fatalf("网络错误泄露凭据: %v", err)
	}
}

func TestTGPollingDropsBacklogAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var calls []string
	var offsets []int64
	polled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		calls = append(calls, method)
		mu.Unlock()
		switch method {
		case "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "deleteWebhook":
			var p map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&p)
			if !p["drop_pending_updates"] {
				t.Error("启动未丢弃积压")
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "getUpdates":
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			offsets = append(offsets, p.Offset)
			n := len(offsets)
			mu.Unlock()
			if n == 1 {
				fmt.Fprint(w, `{"ok":true,"result":[{"update_id":10},{"update_id":10}]}`)
			} else {
				close(polled)
				<-r.Context().Done()
			}
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()
	a := &tgAPI{base: server.URL, token: "test", client: server.Client()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Handler{}).tgRun(ctx, a, TGConfig{Enabled: true, Token: "test", ChatID: "42"})
	}()
	select {
	case <-polled:
	case <-time.After(3 * time.Second):
		t.Fatal("未开始轮询")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后轮询未退出")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 11 {
		t.Fatalf("游标不正确: %v", offsets)
	}
	if strings.Join(calls[:3], ",") != "getWebhookInfo,deleteWebhook,setMyCommands" {
		t.Fatalf("启动顺序错误: %v", calls)
	}
}

func TestTGWebhookConflictDoesNotTakeOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getWebhookInfo") {
			t.Errorf("不应接管已有 Webhook: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"url":"https://existing.invalid/hook"}}`)
		close(checked)
	}))
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Handler{}).tgRun(ctx, &tgAPI{base: server.URL, token: "test", client: server.Client()}, TGConfig{Enabled: true})
	}()
	select {
	case <-checked:
	case <-time.After(3 * time.Second):
		t.Fatal("未检查 Webhook")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("冲突等待不能取消")
	}
}

func TestTGQueuedCommandRechecksSavedConfig(t *testing.T) {
	msgCfgCache.Lock()
	old, at, oldErr := msgCfgCache.cfg, msgCfgCache.at, msgCfgCache.err
	msgCfgCache.cfg = &MessageConfig{TG: TGConfig{Enabled: true, Token: "test", ChatID: "99"}}
	msgCfgCache.at = time.Now()
	msgCfgCache.Unlock()
	defer func() {
		msgCfgCache.Lock()
		msgCfgCache.cfg, msgCfgCache.at, msgCfgCache.err = old, at, oldErr
		msgCfgCache.Unlock()
	}()
	b := &tgConversation{cfg: TGConfig{Enabled: true, Token: "test", ChatID: "42"}, ctx: context.Background(), selection: &tgSelection{token: "keep"}}
	b.handle(tgUpdate{Message: &tgMessage{From: tgUser{ID: 42}, Chat: tgChat{ID: 42, Type: "private"}, Text: "/cancel"}})
	if b.selection == nil {
		t.Fatal("配置变更后仍执行了旧用户的排队指令")
	}
}
