package api

import (
	"context"
	"fmt"
	"log"
	"strings"
)

var tgMenu = []map[string]string{
	{"command": "help", "description": "帮助与指令列表"},
	{"command": "id", "description": "查看当前聊天 ID"},
	{"command": "status", "description": "运行状态"},
	{"command": "search", "description": "找资源（全部来源）：/search 片名"},
	{"command": "gy", "description": "只搜观影：/gy 片名"},
	{"command": "wp", "description": "只搜网盘：/wp 片名"},
	{"command": "sub", "description": "订阅：/sub 片名"},
	{"command": "subs", "description": "我的订阅"},
	{"command": "download", "description": "下载或转存：/download 链接"},
	{"command": "organize", "description": "执行整理"},
	{"command": "sync", "description": "执行增量同步"},
	{"command": "dup", "description": "同集多份：选择保留哪份"},
	{"command": "cancel", "description": "关闭当前搜索"},
}

const tgHelp = "115-Station 私聊机器人\n\n" +
	"/search 片名：找资源，观影 / 盘搜 / TG 频道 / 不太灵一起搜\n" +
	"/gy 片名：只搜观影\n/wp 片名：只搜网盘\n" +
	"/sub 片名：订阅，定时找缺的集，转存后自动入库（找资源的结果里也能点「订阅这部」）\n" +
	"/subs 我的订阅：看进度、立即搜索、暂停、取消\n" +
	"/download 链接：离线下载或 115 转存（也可直接发链接和提取码）\n" +
	"/status 状态\n/organize 整理\n/sync 增量同步\n/cancel 关闭当前搜索\n/id 查看聊天 ID\n" +
	"/dup 同一集有几份文件、改名会重名时选择保留哪份（通知里的按钮也能选）\n\n" +
	"找资源：TMDB 只有一部时直接出资源；点按钮或回复序号选择，可以连续挑多条（一部剧好几季）。" +
	"也可以回复 0 自动择优、n / p 翻页、b 重新选片、r 重搜、q 关闭；片名在 TMDB 上查不到时直接按关键词搜。" +
	"10 分钟内有效，关闭不会停止已提交的任务。"

// tgConversation 只由接收器的单个 worker 访问，阶段切换和消费按钮不需要跨请求共享裸指针。
type tgConversation struct {
	h    *Handler
	api  *tgAPI
	cfg  TGConfig
	ctx  context.Context
	chat int64
	user int64
}

func tgCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	first := strings.ToLower(fields[0])
	args := strings.TrimSpace(text[len(fields[0]):])
	aliases := map[string]string{
		"/start": "help", "/help": "help", "帮助": "help", "help": "help", "?": "help",
		"/id": "id", "/status": "status", "状态": "status", "status": "status",
		"/search": "search", "so": "search", "/gy": "gy", "gy": "gy", "/wp": "wp", "wp": "wp",
		"/download": "download", "dl": "download", "/organize": "organize", "整理": "organize",
		"/sync": "sync", "同步": "sync", "/cancel": "cancel", "取消": "cancel",
		"/dup": "dup",
		"/sub": "sub", "/subscribe": "sub", "/subs": "subs", "我的订阅": "subs", "订阅列表": "subs",
	}
	if cmd, ok := aliases[first]; ok {
		return cmd, args
	}
	for _, p := range []struct{ prefix, command string }{{"搜索", "search"}, {"找资源", "search"}, {"观影", "gy"}, {"网盘", "wp"}, {"下载", "download"}, {"多份", "dup"}, {"订阅", "sub"}} {
		if strings.HasPrefix(text, p.prefix) {
			return p.command, strings.TrimSpace(strings.TrimPrefix(text, p.prefix))
		}
	}
	if classifyLink(text) != "" {
		return "download", text
	}
	if act, ok := botFlowAct(text); ok {
		return "flow", act
	}
	return "unknown", ""
}

func (b *tgConversation) reply(lines ...string) { b.send(strings.Join(lines, "\n"), nil) }
func (b *tgConversation) send(text string, buttons [][]tgButton) {
	if err := b.api.send(b.ctx, b.chat, text, buttons); err != nil && b.ctx.Err() == nil {
		log.Printf("[TG机器人] ✗ 回复失败: %v", err)
	}
}

func (b *tgConversation) flowKey() string { return fmt.Sprintf("tg:%d", b.chat) }

func (b *tgConversation) handle(u tgUpdate) {
	defer func() {
		if recover() != nil {
			botFlowDrop(b.flowKey(), nil)
			log.Printf("[TG机器人] ✗ 指令处理异常，已关闭搜索会话")
		}
	}()
	m, from := tgUpdateMessage(u)
	if m == nil || m.Chat.Type != "private" || from.Bot || from.ID <= 0 || from.ID != m.Chat.ID {
		return
	}
	cmd, args := tgCommand(m.Text)
	// 身份查询只回当前请求者的 ID，允许尚未填写 Chat ID 时完成配置。
	if u.Callback == nil && (cmd == "id" || cmd == "help") {
		if cmd == "id" {
			_ = b.api.send(b.ctx, m.Chat.ID, fmt.Sprintf("当前私聊 Chat ID：%d\n填入消息配置后，此会话可接收通知并操作机器人。", m.Chat.ID), nil)
		} else {
			_ = b.api.send(b.ctx, m.Chat.ID, tgHelp, nil)
		}
		return
	}
	// 排队期间可能保存了新配置，执行业务前再校验，不能等下一轮监督检查才撤销权限。
	cfg, err := loadMessageConfig()
	if err != nil || strings.TrimSpace(cfg.TG.Token) != b.cfg.Token || !tgAllowed(cfg.TG, m, from) {
		return
	}
	b.chat, b.user = m.Chat.ID, from.ID
	if u.Callback != nil && strings.HasPrefix(u.Callback.Data, "d:") {
		// 同集多份的按钮：d:<记录 id>:<组>:<操作>，认数据库里的记录，不靠会话（orgdupbot.go）
		text, buttons := b.h.dupTGCallback(u.Callback.Data)
		if err := b.api.edit(b.ctx, b.chat, m.ID, text, buttons); err != nil && b.ctx.Err() == nil {
			b.reply(text)
		}
		return
	}
	if u.Callback != nil {
		// 按钮：f:<会话 token>:<操作>，操作结果编辑按钮所在的那条消息
		parts := strings.SplitN(u.Callback.Data, ":", 3)
		if len(parts) != 3 || parts[0] != "f" || parts[1] == "" {
			b.reply("按钮已失效，请重新搜索。")
			return
		}
		msg := m.ID
		v, _ := b.h.botAct(b.flowKey(), parts[2], parts[1], b.flowIO(&msg))
		b.show(v, &msg)
		return
	}
	switch cmd {
	case "flow":
		var msg int64
		v, ok := b.h.botAct(b.flowKey(), args, "", b.flowIO(&msg))
		if !ok {
			b.reply("当前没有进行中的搜索，发送 /search 片名 开始。")
			return
		}
		b.show(v, &msg)
	case "cancel":
		botFlowDrop(b.flowKey(), nil)
		b.reply("已关闭当前搜索；已提交的任务继续执行。")
	case "search", "wp", "gy":
		if args == "" {
			b.reply("请在命令后填写片名，例如 /" + cmd + " 星际穿越")
			return
		}
		source := map[string]string{"search": "", "gy": "gy", "wp": "pansou"}[cmd]
		var msg int64
		v := b.h.botFind(b.flowKey(), source, args, b.flowIO(&msg))
		b.show(v, &msg)
	case "sub":
		if args == "" {
			b.reply("请在命令后填写片名，例如 /sub 三体；发送 /subs 查看已有的订阅。")
			return
		}
		var msg int64
		v := b.h.botSubFind(b.flowKey(), args, b.flowIO(&msg))
		b.show(v, &msg)
	case "subs":
		var msg int64
		b.show(b.h.botSubsList(b.flowKey()), &msg)
	case "dup":
		b.reply(b.h.dupBotCommand(fmt.Sprintf("tg:%d:%d", b.chat, b.user), args)...)
	case "status", "download", "organize", "sync":
		if cmd == "download" && args == "" {
			b.reply("请在命令后填写链接。")
			return
		}
		text := map[string]string{"status": "状态", "download": "下载 " + args, "organize": "整理", "sync": "同步"}[cmd]
		// 只允许明确列出的业务进入公共路由，中文别名同样不能绕进补全或建库。
		reply := b.replyForCurrentChat()
		b.h.handleBotCommand(fmt.Sprintf("tg:%d:%d", b.chat, b.user), text, reply)
	default:
		b.reply("未识别的指令，发送 /help 查看帮助。")
	}
}

func (b *tgConversation) replyForCurrentChat() func(...string) {
	chat, ctx, a := b.chat, b.ctx, b.api
	return func(lines ...string) {
		if err := a.send(ctx, chat, strings.Join(lines, "\n"), nil); err != nil && ctx.Err() == nil {
			log.Printf("[TG机器人] ✗ 回复失败: %v", err)
		}
	}
}

// flowIO 搜索进度写在 *msg 那条消息上（没有就先发一条），提交进度与结果另发
func (b *tgConversation) flowIO(msg *int64) botIO {
	return botIO{
		say:      b.replyForCurrentChat(),
		progress: func(s string) { b.upsert(msg, "⏳ "+s, nil) },
	}
}

// upsert 有消息 id 就原地编辑，编辑不了（太旧、被删）就新发一条并记下 id
func (b *tgConversation) upsert(msg *int64, text string, buttons [][]tgButton) {
	if b.ctx.Err() != nil {
		return
	}
	if *msg != 0 && b.api.edit(b.ctx, b.chat, *msg, text, buttons) == nil {
		return
	}
	id, err := b.api.sendOne(b.ctx, b.chat, text, buttons)
	if err != nil {
		if b.ctx.Err() == nil {
			log.Printf("[TG机器人] ✗ 回复失败: %v", err)
		}
		return
	}
	*msg = id
}

// show 把一屏内容落到 TG：提示另发；列表编辑 *msg 那条（按钮所在的消息 / 搜索进度那条）
func (b *tgConversation) show(v botView, msg *int64) {
	if len(v.Lines) == 0 || (v.Refresh && *msg == 0) {
		return
	}
	if v.Toast {
		b.reply(v.Lines...)
		return
	}
	var rows [][]tgButton
	for _, row := range v.Buttons {
		var r []tgButton
		for _, btn := range row {
			r = append(r, tgButton{Text: btn.Text, Data: "f:" + v.Token + ":" + btn.Act})
		}
		rows = append(rows, r)
	}
	b.upsert(msg, v.text(), rows)
}
