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
		{"搜索 星际穿越", "search", "星际穿越"}, {"12", "flow", "12"}, {"0", "flow", "0"},
		{"下一页", "flow", "n"}, {"Q", "flow", "q"},
	} {
		cmd, arg := tgCommand(tc.text)
		if cmd != tc.cmd || arg != tc.arg {
			t.Fatalf("解析 %q 得到 %q %q", tc.text, cmd, arg)
		}
	}
}

// 从频道转发来的磁力消息（2026-11-06 现场，蜜柑计划）：带封面图，文字在 caption 里，
// 磁力夹在一长段说明中间、tr= 写在 xt= 前面。
func TestTGForwardedMagnet(t *testing.T) {
	const magnet = "magnet:?tr=http%3A%2F%2Ft.nyaatracker.com%2Fannounce&xt=urn:btih:90e9cad8559ced10caa856ee1e604b156e91cc03"
	caption := "[黒ネズミたち] 黑色五叶草 Season 2 / Black Clover 2nd Season - 175 (ABEMA 1920x1080 AVC AAC MKV)\n\n" +
		"📺 番组： #黑色五叶草_第二季\n👤 发布组：#Kirara_Fantasia\n📅 发布时间： 2026-11-05 11:01:00\n📦 大小：716.0 MB\n" +
		"🔑 Hash： 90E9CAD8559CED10CAA856EE1E604B156E91CC03\n\n🧲 磁力链接：\n" + magnet + "\n🔗 Mikan 详情页 · 📥 种子下载"
	var u tgUpdate
	raw, _ := json.Marshal(map[string]any{"update_id": 1, "message": map[string]any{
		"message_id": 7, "chat": map[string]any{"id": 42, "type": "private"}, "photo": []any{}, "caption": caption,
	}})
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	if cmd, arg := tgCommand(u.Message.body()); cmd != "download" || arg != magnet {
		t.Fatalf("转发消息解析成 %q %q", cmd, arg)
	}
	// 同一个 hash 写了两遍（大小写不同）只算一条；结尾的中文标点不算进链接
	if got := embeddedLinks("A " + magnet + "。\nB " + strings.ToUpper(magnet)); len(got) != 1 || got[0] != magnet {
		t.Fatalf("去重得到 %v", got)
	}
	ed2k := "ed2k://|file|Black Clover 175.mkv|750780416|0123456789ABCDEF0123456789ABCDEF|/"
	if cmd, arg := tgCommand("ed2k 链接：\n" + ed2k + "\n来自某频道"); cmd != "download" || arg != ed2k {
		t.Fatalf("ed2k 解析成 %q %q", cmd, arg)
	}
	if cmd, arg := tgCommand("第一集 " + magnet + "\n第二集 magnet:?xt=urn:btih:aaaabbbbccccdddd"); cmd != "manylinks" || arg != "2" {
		t.Fatalf("几条链接解析成 %q %q", cmd, arg)
	}
	// 只有详情页这类 http 链接的长消息不当成下载
	if cmd, _ := tgCommand("看看这部 片名不错 https://mikanani.me/Home/Episode/xxx"); cmd == "download" {
		t.Fatal("不该把夹在话里的 http 链接当下载")
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
	f := &botFlow{token: "keep", at: time.Now()}
	botFlowPut("tg:42", f)
	defer botFlowDrop("tg:42", f)
	b := &tgConversation{cfg: TGConfig{Enabled: true, Token: "test", ChatID: "42"}, ctx: context.Background()}
	b.handle(tgUpdate{Message: &tgMessage{From: tgUser{ID: 42}, Chat: tgChat{ID: 42, Type: "private"}, Text: "/cancel"}})
	if botFlowGet("tg:42") == nil {
		t.Fatal("配置变更后仍执行了旧用户的排队指令")
	}
}

// 按钮翻页编辑按钮所在的那条消息，「已经是最后一页」这种提示另发，不能把列表覆盖掉
func TestTGFlowButtonsEditInPlace(t *testing.T) {
	msgCfgCache.Lock()
	old, at, oldErr := msgCfgCache.cfg, msgCfgCache.at, msgCfgCache.err
	msgCfgCache.cfg = &MessageConfig{TG: TGConfig{Enabled: true, Token: "test", ChatID: "42"}}
	msgCfgCache.at = time.Now()
	msgCfgCache.Unlock()
	defer func() {
		msgCfgCache.Lock()
		msgCfgCache.cfg, msgCfgCache.at, msgCfgCache.err = old, at, oldErr
		msgCfgCache.Unlock()
	}()
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID int64 `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		calls = append(calls, fmt.Sprintf("%s#%d", r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], p.ID))
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
	}))
	defer server.Close()
	_, _ = botFlowFixture(t, "tg:42", botItems(10))
	b := &tgConversation{api: &tgAPI{base: server.URL, token: "test", client: server.Client()},
		cfg: TGConfig{Enabled: true, Token: "test", ChatID: "42"}, ctx: context.Background()}
	press := func(data string) {
		b.handle(tgUpdate{Callback: &tgCallback{From: tgUser{ID: 42}, Data: data,
			Message: &tgMessage{ID: 7, Chat: tgChat{ID: 42, Type: "private"}}}})
	}
	press("f:tok:n")
	press("f:tok:n")
	press("pick:old:1")
	if got := strings.Join(calls, ","); got != "editMessageText#7,sendMessage#0,sendMessage#0" {
		t.Fatalf("调用顺序 %s", got)
	}
}
