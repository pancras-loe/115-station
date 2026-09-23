package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

var tgMenu = []map[string]string{
	{"command": "help", "description": "帮助与指令列表"},
	{"command": "id", "description": "查看当前聊天 ID"},
	{"command": "status", "description": "运行状态"},
	{"command": "search", "description": "搜索 TMDB：/search 片名"},
	{"command": "gy", "description": "观影资源：/gy 片名"},
	{"command": "wp", "description": "网盘资源：/wp 片名"},
	{"command": "download", "description": "下载或转存：/download 链接"},
	{"command": "organize", "description": "执行整理"},
	{"command": "sync", "description": "执行增量同步"},
	{"command": "cancel", "description": "取消当前选择会话"},
}

const tgHelp = "115-Station 私聊机器人\n\n/status 状态\n/search 片名：TMDB 搜索\n/gy 片名：观影搜资源\n/wp 片名：网盘搜资源\n/download 链接：离线下载或 115 转存（也可直接发链接和提取码）\n/organize 整理\n/sync 增量同步\n/cancel 取消当前选择\n/id 查看聊天 ID\n\n支持对应中文指令。选片和资源可点按钮或回复数字，5 分钟有效。取消选择不会停止已提交的任务。"

type tgChoice struct {
	label string
	run   func()
}
type tgSelection struct {
	token   string
	chat    int64
	user    int64
	until   time.Time
	choices []tgChoice
}

// tgConversation 只由接收器的单个 worker 访问，阶段切换和消费按钮不需要跨请求共享裸指针。
type tgConversation struct {
	h         *Handler
	api       *tgAPI
	cfg       TGConfig
	ctx       context.Context
	chat      int64
	user      int64
	selection *tgSelection
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
	}
	if cmd, ok := aliases[first]; ok {
		return cmd, args
	}
	for _, p := range []struct{ prefix, command string }{{"搜索", "search"}, {"观影", "gy"}, {"网盘", "wp"}, {"下载", "download"}} {
		if strings.HasPrefix(text, p.prefix) {
			return p.command, strings.TrimSpace(strings.TrimPrefix(text, p.prefix))
		}
	}
	if classifyLink(text) != "" {
		return "download", text
	}
	if n, err := strconv.Atoi(text); err == nil && n > 0 {
		return "pick", text
	}
	return "unknown", ""
}

func (b *tgConversation) reply(lines ...string) { b.send(strings.Join(lines, "\n"), nil) }
func (b *tgConversation) send(text string, buttons [][]tgButton) {
	if err := b.api.send(b.ctx, b.chat, text, buttons); err != nil && b.ctx.Err() == nil {
		log.Printf("[TG机器人] ✗ 回复失败: %v", err)
	}
}

func (b *tgConversation) handle(u tgUpdate) {
	defer func() {
		if recover() != nil {
			b.selection = nil
			log.Printf("[TG机器人] ✗ 指令处理异常，已清理选择会话")
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
	if u.Callback != nil {
		parts := strings.Split(u.Callback.Data, ":")
		if len(parts) != 3 || parts[0] != "pick" {
			b.reply("按钮已失效，请重新搜索。")
			return
		}
		n, _ := strconv.Atoi(parts[2])
		b.pick(parts[1], n)
		return
	}
	switch cmd {
	case "pick":
		n, _ := strconv.Atoi(args)
		b.pick("", n)
	case "cancel":
		b.selection = nil
		b.reply("已取消当前选择；已提交的任务继续执行。")
	case "wp", "gy":
		b.selection = nil
		if args == "" {
			b.reply("请在命令后填写片名，例如 /" + cmd + " 星际穿越")
			return
		}
		b.search(cmd, args)
	case "status", "search", "download", "organize", "sync":
		if (cmd == "search" || cmd == "download") && args == "" {
			b.reply("请在命令后填写片名或链接。")
			return
		}
		if cmd == "search" {
			b.selection = nil
			b.reply("正在查询 TMDB…")
		}
		if cmd == "download" {
			b.selection = nil
		}
		text := map[string]string{"status": "状态", "search": "搜索 " + args, "download": "下载 " + args, "organize": "整理", "sync": "同步"}[cmd]
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

func (b *tgConversation) choose(title string, choices []tgChoice) {
	if b.ctx.Err() != nil {
		return
	}
	if len(choices) == 0 {
		b.reply("没有找到资源，请更换关键词。")
		return
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		b.reply("暂时无法创建选择会话，请重试。")
		return
	}
	s := &tgSelection{token: hex.EncodeToString(nonce[:]), chat: b.chat, user: b.user, until: time.Now().Add(5 * time.Minute), choices: choices}
	b.selection = s
	lines := []string{title}
	var rows [][]tgButton
	for i, c := range choices {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, c.label))
		rows = append(rows, []tgButton{{Text: fmt.Sprintf("%d. %s", i+1, truncateStr(c.label, 35)), Data: fmt.Sprintf("pick:%s:%d", s.token, i+1)}})
	}
	lines = append(lines, "点击按钮或回复数字选择，5 分钟有效；/cancel 取消。")
	b.send(strings.Join(lines, "\n"), rows)
}

// takeSelection 在执行外部请求前消费整组选项，双击与旧阶段按钮不会再次提交。
func takeSelection(s **tgSelection, token string, n int, chat, user int64, now time.Time) (func(), string) {
	current := *s
	if current == nil {
		return nil, "选择已完成或失效，请重新搜索。"
	}
	if !now.Before(current.until) {
		*s = nil
		return nil, "选择已过期，请重新搜索。"
	}
	if current.chat != chat || current.user != user || (token != "" && token != current.token) {
		return nil, "这是旧的选择按钮，请使用最新结果。"
	}
	if n < 1 || n > len(current.choices) {
		return nil, fmt.Sprintf("请选择 1-%d。", len(current.choices))
	}
	*s = nil
	return current.choices[n-1].run, ""
}

func (b *tgConversation) pick(token string, n int) {
	run, msg := takeSelection(&b.selection, token, n, b.chat, b.user, time.Now())
	if run == nil {
		b.reply(msg)
		return
	}
	if b.ctx.Err() == nil {
		run()
	}
}

func (b *tgConversation) search(kind, keyword string) {
	b.reply("正在查询 TMDB…")
	movies, err := b.h.wecomTmdbMulti(keyword)
	if err != nil {
		b.reply("TMDB 搜索失败：" + err.Error())
		return
	}
	if len(movies) > 10 {
		movies = movies[:10]
	}
	var choices []tgChoice
	for _, m := range movies {
		label := fmt.Sprintf("%s（%s）[%s] %.1f", m.Title, m.Year, m.Type, m.Vote)
		choices = append(choices, tgChoice{label: label, run: func() { b.resources(kind, m.Title) }})
	}
	b.choose("请选择影视：", choices)
}

func (b *tgConversation) resources(kind, title string) {
	b.reply("正在搜索「" + title + "」的资源…")
	var choices []tgChoice
	listTitle := "「" + title + "」资源："
	if kind == "gy" {
		items, _, err := gySearchTorrents(title, "")
		if err != nil {
			b.reply("观影搜索失败：" + err.Error())
			return
		}
		if len(items) > 20 {
			listTitle = fmt.Sprintf("「%s」资源（展示前 20 条，共 %d 条）：", title, len(items))
			items = items[:20]
		}
		for _, it := range items {
			name, _ := it["title"].(string)
			path, _ := it["path"].(string)
			label := truncateStr(name, 140)
			for _, field := range []string{"size", "seeds", "time"} {
				if value, _ := it[field].(string); value != "" {
					if field == "seeds" {
						value = "做种 " + value
					}
					label += " | " + value
				}
			}
			choices = append(choices, tgChoice{label: label, run: func() {
				b.reply("正在提取磁力链接…")
				magnet, _, err := gyFetchMagnet(path)
				if err != nil {
					b.reply("提取失败：" + err.Error())
					return
				}
				if b.ctx.Err() != nil {
					return
				}
				b.h.wecomHandleLink(magnet, b.replyForCurrentChat())
			}})
		}
	} else {
		items, err := pansouSearchItems(title)
		if err != nil {
			b.reply("网盘搜索失败：" + err.Error())
			return
		}
		if len(items) > 10 {
			listTitle = fmt.Sprintf("「%s」资源（展示前 10 条，共 %d 条）：", title, len(items))
			items = items[:10]
		}
		for _, it := range items {
			label := fmt.Sprintf("[%s] %s", pansouTypeLabel(it.CloudType), truncateStr(firstNonEmptyStr(it.Note, it.URL), 140))
			if it.Password != "" {
				label += " | 提取码 " + it.Password
			}
			if it.Datetime != "" {
				label += " | " + it.Datetime
			}
			choices = append(choices, tgChoice{label: label, run: func() {
				switch it.Action {
				case "transfer":
					b.reply("正在转存 115…")
					msg, ok, _, err := b.h.shareReceiveCore(it.URL, it.Password, b.h.shareFolderCid(), "机器人", true)
					if err != nil {
						b.reply("转存失败（部分内容可能已转存，请先看一眼网盘转存目录再决定是否重试）：" + err.Error())
						return
					}
					b.reply(fmt.Sprintf("%s\n成功 %d 项，已接入整理流程。", msg, ok))
				case "offline":
					b.h.wecomHandleLink(it.URL, b.replyForCurrentChat())
				default:
					b.reply("此资源请手动打开：", it.URL, "提取码："+it.Password)
				}
			}})
		}
	}
	b.choose(listTitle, choices)
}
