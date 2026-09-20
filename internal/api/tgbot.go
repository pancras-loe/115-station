package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tgAPI 只封装本站使用的接口；通知与指令共用 Token，不引入另一套凭据。
type tgAPI struct {
	base   string
	token  string
	client *http.Client
}

type tgAPIError struct {
	Code  int
	Retry int
}

func (e *tgAPIError) Error() string {
	switch e.Code {
	case 401:
		return "Bot Token 无效，请重新配置"
	case 409:
		return "同一机器人存在其他轮询器或 Webhook，请停止冲突服务"
	case 429:
		return "Telegram 请求过于频繁，正在等待重试"
	default:
		return fmt.Sprintf("Telegram 接口失败（%d）", e.Code)
	}
}

func (a *tgAPI) call(ctx context.Context, method string, data any, out any) error {
	if method != "getUpdates" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/bot"+a.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Telegram 请求配置无效")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// net/http 的错误包含完整 URL，不能把带 Token 的地址写入日志或状态页。
		return fmt.Errorf("Telegram 连接失败，请检查网络或代理")
	}
	defer resp.Body.Close()
	var envelope struct {
		OK         bool            `json:"ok"`
		Code       int             `json:"error_code"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			Retry int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("Telegram 返回了无效响应")
	}
	if !envelope.OK || resp.StatusCode != http.StatusOK {
		if envelope.Code == 0 {
			envelope.Code = resp.StatusCode
		}
		return &tgAPIError{envelope.Code, envelope.Parameters.Retry}
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

type tgUser struct {
	ID  int64 `json:"id"`
	Bot bool  `json:"is_bot"`
}
type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type tgMessage struct {
	ID   int64  `json:"message_id"`
	From tgUser `json:"from"`
	Chat tgChat `json:"chat"`
	Text string `json:"text"`
}
type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}
type tgUpdate struct {
	ID       int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
}
type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

var telegramRuntime struct {
	sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	state  string
	detail string
}

func tgSetState(state, detail string) {
	telegramRuntime.Lock()
	defer telegramRuntime.Unlock()
	telegramRuntime.state, telegramRuntime.detail = state, detail
}

// TelegramBotStatus 不返回凭据，供后台区分通知配置与实际接收连接状态。
func TelegramBotStatus() map[string]string {
	telegramRuntime.Lock()
	defer telegramRuntime.Unlock()
	return map[string]string{"state": telegramRuntime.state, "detail": telegramRuntime.detail}
}

// StartTelegramBot 由单个监督循环串行切换配置，避免保存配置启动多个 getUpdates。
func (h *Handler) StartTelegramBot() {
	telegramRuntime.Lock()
	defer telegramRuntime.Unlock()
	if telegramRuntime.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	telegramRuntime.cancel = cancel
	telegramRuntime.done = make(chan struct{})
	go func() {
		defer close(telegramRuntime.done)
		h.tgSupervise(ctx)
	}()
}

// StopTelegramBot 先停止收取新指令，已受理业务沿用既有 worker 收尾机制。
func StopTelegramBot() {
	telegramRuntime.Lock()
	cancel := telegramRuntime.cancel
	telegramRuntime.Unlock()
	if cancel != nil {
		cancel()
	}
}

func tgWait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (h *Handler) tgSupervise(ctx context.Context) {
	var activeCancel context.CancelFunc
	var done chan struct{}
	var signature string
	defer func() {
		if activeCancel != nil {
			activeCancel()
		}
		tgSetState("stopped", "接收器已停止")
	}()
	for ctx.Err() == nil {
		cfg, err := loadMessageConfig()
		if err != nil {
			tgSetState("error", "无法读取消息配置")
		} else {
			proxy := getProxyURL()
			tg := cfg.TG
			tg.Token, tg.ChatID = strings.TrimSpace(tg.Token), strings.TrimSpace(tg.ChatID)
			next := fmt.Sprintf("%t\x00%s\x00%s\x00%s", tg.isEnabled(), tg.Token, tg.ChatID, proxy)
			if next != signature {
				if activeCancel != nil {
					activeCancel()
					select {
					case <-done:
					case <-ctx.Done():
						return
					}
				}
				signature = next
				activeCancel = nil
				if !tg.isEnabled() {
					tgSetState("disabled", "TG 未启用")
				} else if tg.Token == "" {
					tgSetState("error", "请填写 Bot Token")
				} else {
					transport := http.DefaultTransport.(*http.Transport).Clone()
					if proxy != "" {
						p, e := parseProxyURL(proxy)
						if e != nil {
							tgSetState("error", "代理地址无效")
							if !tgWait(ctx, 2*time.Second) {
								return
							}
							continue
						}
						transport.Proxy = p
					}
					a := &tgAPI{base: "https://api.telegram.org", token: tg.Token, client: &http.Client{Transport: transport, Timeout: 40 * time.Second}}
					runCtx, runCancel := context.WithCancel(ctx)
					activeCancel = runCancel
					done = make(chan struct{})
					go func(finished chan struct{}) {
						defer close(finished)
						defer runCancel()
						defer transport.CloseIdleConnections()
						h.tgRun(runCtx, a, tg)
					}(done)
				}
			}
		}
		if !tgWait(ctx, 2*time.Second) {
			return
		}
	}
}

// tgRun 参考 openStrm 的 services/telegram/polling.ts 单轮询与退避思路；
// 启动时丢弃旧指令，避免重启后补执行写操作。
func (h *Handler) tgRun(ctx context.Context, a *tgAPI, cfg TGConfig) {
	tgSetState("connecting", "正在连接 Telegram")
	for ctx.Err() == nil {
		var webhook struct {
			URL string `json:"url"`
		}
		err := a.call(ctx, "getWebhookInfo", map[string]any{}, &webhook)
		if err == nil && webhook.URL != "" {
			tgSetState("error", "此 Bot 已配置 Webhook，请先在原服务解除；本站不会自动接管")
			if !tgWait(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if err == nil {
			// 已确认没有 Webhook，仅清掉离线期间积压的消息。
			err = a.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": true}, nil)
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		tgSetState("error", err.Error())
		if !tgWait(ctx, tgRetryDelay(err, 5*time.Second)) {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := a.call(ctx, "setMyCommands", map[string]any{"commands": tgMenu}, nil); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[TG机器人] ○ 命令菜单注册失败: %v", err)
	}
	queue := make(chan tgUpdate, 8)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		bot := &tgConversation{h: h, api: a, cfg: cfg, ctx: ctx}
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-queue:
				if ctx.Err() == nil {
					bot.handle(u)
				}
			}
		}
	}()
	// 配置切换等待旧会话结束，旧按钮和排队指令不会进入新配置。
	runCtx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); <-workerDone }()
	var offset int64
	delay := time.Second
	for runCtx.Err() == nil {
		var updates []tgUpdate
		err := a.call(runCtx, "getUpdates", map[string]any{"offset": offset, "timeout": 30, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			if runCtx.Err() != nil {
				return
			}
			tgSetState("error", err.Error())
			if !tgWait(runCtx, tgRetryDelay(err, delay)) {
				return
			}
			if delay < 30*time.Second {
				delay *= 2
			}
			continue
		}
		delay = time.Second
		detail := "私聊指令接收中；仅配置的 Chat ID 可操作"
		if id, err := strconv.ParseInt(cfg.ChatID, 10, 64); err != nil || id <= 0 {
			detail = "已连接；填写个人 Chat ID 后可操作，群和频道仅接收通知"
		}
		tgSetState("running", detail)
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			offset = u.ID + 1
			if u.Callback != nil {
				// 立即结束按钮转圈，业务处理仍通过有界队列串行执行。
				_ = a.call(runCtx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, nil)
			}
			select {
			case queue <- u:
			case <-runCtx.Done():
				return
			default:
				if m, from := tgUpdateMessage(u); tgAllowed(cfg, m, from) {
					_ = a.send(runCtx, m.Chat.ID, "操作较多，请等待当前查询结束后重试。", nil)
				}
			}
		}
	}
}

func tgRetryDelay(err error, fallback time.Duration) time.Duration {
	if e, ok := err.(*tgAPIError); ok {
		if e.Retry > 0 {
			return time.Duration(e.Retry) * time.Second
		}
		if e.Code == 401 || e.Code == 409 {
			return 30 * time.Second
		}
	}
	if fallback > 30*time.Second {
		return 30 * time.Second
	}
	return fallback
}

func tgUpdateMessage(u tgUpdate) (*tgMessage, tgUser) {
	if u.Callback != nil {
		return u.Callback.Message, u.Callback.From
	}
	if u.Message != nil {
		return u.Message, u.Message.From
	}
	return nil, tgUser{}
}

func tgAllowed(cfg TGConfig, m *tgMessage, from tgUser) bool {
	return cfg.isEnabled() && m != nil && m.Chat.Type == "private" && !from.Bot && from.ID > 0 &&
		m.Chat.ID == from.ID && strconv.FormatInt(m.Chat.ID, 10) == strings.TrimSpace(cfg.ChatID)
}

func (a *tgAPI) send(ctx context.Context, chat int64, text string, buttons [][]tgButton) error {
	// 纯文本避免资源标题里的 HTML 字符破坏消息；按字符分段并只在末段挂按钮。
	runes := []rune(text)
	for len(runes) > 0 {
		n := len(runes)
		if n > 1800 {
			n = 1800
		}
		payload := map[string]any{"chat_id": chat, "text": string(runes[:n])}
		if n == len(runes) && len(buttons) > 0 {
			payload["reply_markup"] = map[string]any{"inline_keyboard": buttons}
		}
		if err := a.call(ctx, "sendMessage", payload, nil); err != nil {
			return err
		}
		runes = runes[n:]
	}
	return nil
}
