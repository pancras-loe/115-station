package api

// IYUU（爱语飞飞）与通用 Webhook 两个推送通道，带「推哪些消息类型」勾选。
//
// 做法参考 MoviePilot 官方插件 iyuumsg / webhook（jxxghp/MoviePilot-Plugins，只读、Go 重写）：
//   - IYUU：`https://iyuu.cn/{token}.send` 带 text（标题）/ desp（正文），回 errcode；
//     插件把消息排进队列、每 5 秒最多发一条（IYUU 连发会被拒），这里同样排队限速。
//   - Webhook：MP 那个推的是原始事件数据，本站没有事件总线，推的是排好版的一条消息，
//     另给一个可选的请求体 / 地址模板（{{title}} 之类），好直接对接 Bark、PushPlus、ntfy 这类服务。
//   - 消息类型：MP 的 NotificationType 由发消息的地方带上，插件按勾选过滤；
//     不勾 = 全推（MP 同款）。机器人回复（kind=bot）是回给某个会话的，永远不推这两个通道
//     （MP 跳过带 channel 的消息，同一个意思）。
//
// 类型过滤只管这两个新通道：企微 / TG / 飞书 / QQ 照旧全推，老部署行为不变。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// notifyKind 消息类型：发通知的地方带上，IYUU / Webhook 按用户勾选过滤
type notifyKind string

const (
	kindIngest    notifyKind = "ingest"    // 入库通知（片目进了媒体库）
	kindOrganize  notifyKind = "organize"  // 整理结果、洗版、待确认、整理配置出错
	kindTransfer  notifyKind = "transfer"  // 离线下载完成 / 失败
	kindSubscribe notifyKind = "subscribe" // 资源订阅
	kindDelete    notifyKind = "delete"    // 深度删除、Emby 删除
	kindEmby      notifyKind = "emby"      // Emby Webhook 转发的播放 / 入库等事件
	kindCheckin   notifyKind = "checkin"   // 115 / RE0 / 癫影 / PT 签到
	kindTask      notifyKind = "task"      // 定时清理、备份、封面生成等扩展任务
	kindSystem    notifyKind = "system"    // 版本更新、媒体目录没挂上
	kindBot       notifyKind = "bot"       // 机器人回复：只回原会话，不推 IYUU / Webhook
)

// notifyKindNames 类型的中文名（日志、Webhook 载荷的 type_name）；
// 前端勾选列表在 webui/src/types/message.ts 的 NOTIFY_KINDS，改一边要改另一边
var notifyKindNames = map[notifyKind]string{
	kindIngest:    "入库",
	kindOrganize:  "整理",
	kindTransfer:  "下载转存",
	kindSubscribe: "订阅",
	kindDelete:    "删除",
	kindEmby:      "Emby 事件",
	kindCheckin:   "签到",
	kindTask:      "定时任务",
	kindSystem:    "系统",
	kindBot:       "机器人回复",
}

// notifyMsg 一条要推出去的消息
type notifyMsg struct {
	Kind    notifyKind
	Title   string
	Content string
	Image   string // 海报（http 地址才带）
	Link    string
	At      time.Time
}

// text 标题 + 正文合成一段（标题空时只有正文）
func (m notifyMsg) text() string {
	return strings.TrimSpace(strings.TrimSpace(m.Title) + "\n" + strings.TrimSpace(m.Content))
}

// IyuuConfig IYUU（爱语飞飞）：微信公众号推送，只要一个 Token
type IyuuConfig struct {
	Enabled any      `json:"enabled"`
	Token   string   `json:"token"`
	Types   []string `json:"types"` // 推哪些消息类型，空 = 全推
}

func (c *IyuuConfig) isEnabled() bool {
	return anyTrue(c.Enabled) && strings.TrimSpace(c.Token) != ""
}

// WebhookConfig 通用 Webhook
type WebhookConfig struct {
	Enabled any      `json:"enabled"`
	URL     string   `json:"url"`
	Method  string   `json:"method"` // POST（默认）/ GET
	Body    string   `json:"body"`   // POST 请求体模板，空 = 默认 JSON
	Types   []string `json:"types"`
}

func (c *WebhookConfig) isEnabled() bool {
	return anyTrue(c.Enabled) && strings.TrimSpace(c.URL) != ""
}

func anyTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

// kindWanted 这个通道要不要这条消息：机器人回复一律不要；没勾 = 全要
func kindWanted(types []string, k notifyKind) bool {
	if k == kindBot {
		return false
	}
	return len(types) == 0 || slices.Contains(types, string(k))
}

// ==================== 发送队列 ====================

// pushQueue 一个通道一条队列、一个发送者：IYUU 要限速；Webhook 不限速，但串行发
// 免得一轮整理几十条消息同时打到用户的服务上、顺序还乱掉
type pushQueue struct {
	name string
	gap  time.Duration
	send func(cfg *MessageConfig, m notifyMsg) error
	once sync.Once
	ch   chan notifyMsg
}

// pushQueueCap 排队上限：IYUU 5 秒一条，200 条要排十几分钟，再多说明有东西在刷屏，丢掉新来的
const pushQueueCap = 200

func (q *pushQueue) put(m notifyMsg) {
	q.once.Do(func() {
		q.ch = make(chan notifyMsg, pushQueueCap)
		go q.loop()
	})
	select {
	case q.ch <- m:
	default:
		log.Printf("[%s] ✗ 排队的消息超过 %d 条，丢弃: %s", q.name, pushQueueCap, truncateStr(m.text(), 40))
	}
}

func (q *pushQueue) loop() {
	var last time.Time
	for m := range q.ch {
		if w := q.gap - time.Since(last); w > 0 {
			time.Sleep(w)
		}
		// 发的时候再读一次配置：排队期间用户可能关掉了通道或改了地址
		cfg, err := loadMessageConfig()
		if err != nil {
			continue
		}
		err = q.send(cfg, m)
		last = time.Now()
		if err == errPushOff {
			continue
		}
		if err != nil {
			log.Printf("[%s] ✗ 发送失败（%s）: %v", q.name, truncateStr(m.Title, 40), err)
		} else {
			log.Printf("[%s] ✓ 消息发送成功: %s", q.name, truncateStr(firstNonEmpty(m.Title, m.Content), 40))
		}
	}
}

var errPushOff = fmt.Errorf("通道已关闭")

var (
	iyuuQueue = &pushQueue{name: "IYUU", gap: 5 * time.Second, send: func(cfg *MessageConfig, m notifyMsg) error {
		if !cfg.Iyuu.isEnabled() || !kindWanted(cfg.Iyuu.Types, m.Kind) {
			return errPushOff
		}
		return sendIyuu(cfg.Iyuu, m)
	}}
	webhookQueue = &pushQueue{name: "Webhook", send: func(cfg *MessageConfig, m notifyMsg) error {
		if !cfg.Webhook.isEnabled() || !kindWanted(cfg.Webhook.Types, m.Kind) {
			return errPushOff
		}
		return sendWebhook(cfg.Webhook, m)
	}}
)

// pushChannels 把一条消息交给 IYUU / Webhook（按类型过滤后排队，不阻塞调用方）
func pushChannels(cfg *MessageConfig, m notifyMsg) {
	if m.text() == "" {
		return
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if !strings.HasPrefix(m.Image, "http://") && !strings.HasPrefix(m.Image, "https://") {
		m.Image = ""
	}
	if cfg.Iyuu.isEnabled() && kindWanted(cfg.Iyuu.Types, m.Kind) {
		iyuuQueue.put(m)
	}
	if cfg.Webhook.isEnabled() && kindWanted(cfg.Webhook.Types, m.Kind) {
		webhookQueue.put(m)
	}
}

// ==================== IYUU ====================

// iyuuBaseURL 测试里换成假服务
var iyuuBaseURL = "https://iyuu.cn"

var pushHTTP = &http.Client{Timeout: 15 * time.Second}

// sendIyuu 发一条 IYUU：text 是微信里显示的标题，desp 是点开后的正文。
// MP 用 GET 把两段拼进地址，正文长了会超出地址长度，这里改用 POST 表单（IYUU 两种都收）
func sendIyuu(cfg IyuuConfig, m notifyMsg) error {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return fmt.Errorf("IYUU Token 为空")
	}
	title, desp := strings.TrimSpace(m.Title), strings.TrimSpace(m.Content)
	if title == "" {
		// 标题空的消息（封面生成之类）拿正文第一行当标题，否则微信里只看到一条空通知
		title, desp = firstLine(desp), strings.TrimSpace(strings.TrimPrefix(desp, firstLine(desp)))
	}
	form := url.Values{"text": {title}, "desp": {desp}}
	resp, err := pushHTTP.PostForm(iyuuBaseURL+"/"+url.PathEscape(token)+".send", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &r) != nil {
		return fmt.Errorf("HTTP %d，返回看不懂: %s", resp.StatusCode, truncateStr(string(body), 120))
	}
	if r.Errcode != 0 {
		return fmt.Errorf("IYUU 错误 %d: %s", r.Errcode, r.Errmsg)
	}
	return nil
}

// ==================== Webhook ====================

// webhookFields 模板里能用的变量，也是默认 JSON 载荷的字段
func webhookFields(m notifyMsg) map[string]string {
	return map[string]string{
		"type":      string(m.Kind),
		"type_name": notifyKindNames[m.Kind],
		"title":     strings.TrimSpace(m.Title),
		"content":   strings.TrimSpace(m.Content),
		"text":      m.text(),
		"image":     m.Image,
		"link":      m.Link,
		"time":      m.At.Format(time.RFC3339),
	}
}

// fillTemplate 把 {{名字}} 换成值；esc 决定怎么转义（JSON 字符串里 / 地址里）
func fillTemplate(tpl string, fields map[string]string, esc func(string) string) string {
	pairs := make([]string, 0, len(fields)*2)
	for k, v := range fields {
		pairs = append(pairs, "{{"+k+"}}", esc(v))
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}

// jsonInner 转成 JSON 字符串字面量去掉两头引号：模板里写 "title":"{{title}}"，换行、引号都不会把 JSON 弄坏
func jsonInner(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// buildWebhookRequest 拼请求（纯函数，测试直接比结果）
func buildWebhookRequest(cfg WebhookConfig, m notifyMsg) (*http.Request, error) {
	fields := webhookFields(m)
	raw := strings.TrimSpace(cfg.URL)
	if strings.EqualFold(strings.TrimSpace(cfg.Method), http.MethodGet) {
		var target string
		if strings.Contains(raw, "{{") {
			// Bark 这种把内容放在路径里的：https://api.day.app/KEY/{{title}}/{{content}}
			target = fillTemplate(raw, fields, url.PathEscape)
		} else {
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("Webhook 地址不对: %v", err)
			}
			q := u.Query()
			for _, k := range []string{"type", "type_name", "title", "content", "image", "link", "time"} {
				if fields[k] != "" {
					q.Set(k, fields[k])
				}
			}
			u.RawQuery = q.Encode()
			target = u.String()
		}
		return http.NewRequest(http.MethodGet, target, nil)
	}
	target := raw
	if strings.Contains(raw, "{{") {
		target = fillTemplate(raw, fields, url.QueryEscape)
	}
	var body []byte
	ctype := "application/json"
	if tpl := strings.TrimSpace(cfg.Body); tpl != "" {
		body = []byte(fillTemplate(tpl, fields, jsonInner))
		if !json.Valid(body) {
			// 用户写的不是 JSON（纯文本模板）：照原样发，别谎称是 JSON
			ctype = "text/plain; charset=utf-8"
		}
	} else {
		payload := map[string]string{"source": "115-Station"}
		for k, v := range fields {
			payload[k] = v
		}
		body, _ = json.Marshal(payload)
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ctype)
	return req, nil
}

func sendWebhook(cfg WebhookConfig, m notifyMsg) error {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	req, err := buildWebhookRequest(cfg, m)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "115-Station")
	resp, err := pushHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(strings.TrimSpace(string(body)), 120))
	}
	return nil
}
