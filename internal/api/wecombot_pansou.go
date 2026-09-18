package api

// 企微机器人「网盘」搜索链路（PanSou 聚合，带会话状态的多轮交互）：
//   网盘 <片名> → TMDB 多结果选单 → 回复序号选片
//   → PanSou 聚合搜网盘分享（类型/提取码/时间）→ 回复序号
//   → 115 分享自动转存；磁力/ed2k 提交离线下载；其他网盘原样回链手动转存
// 会话按企微用户隔离，5 分钟过期；与「观影」会话互不影响。

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type wecomPansouSession struct {
	Stage   string // movie=待选片名 resource=待选资源
	Keyword string
	Movies  []wecomTmdbHit
	Items   []PansouItem
	At      time.Time
}

const wecomPansouSessionTTL = 5 * time.Minute

var (
	wecomPansouMu       sync.Mutex
	wecomPansouSessions = map[string]*wecomPansouSession{}
)

func wecomPansouSessionGet(user string) *wecomPansouSession {
	wecomPansouMu.Lock()
	defer wecomPansouMu.Unlock()
	s, ok := wecomPansouSessions[user]
	if !ok || time.Since(s.At) > wecomPansouSessionTTL {
		delete(wecomPansouSessions, user)
		return nil
	}
	return s
}

func wecomPansouSessionSet(user string, s *wecomPansouSession) {
	wecomPansouMu.Lock()
	defer wecomPansouMu.Unlock()
	for u, v := range wecomPansouSessions {
		if time.Since(v.At) > wecomPansouSessionTTL {
			delete(wecomPansouSessions, u)
		}
	}
	wecomPansouSessions[user] = s
}

// wecomHandlePansouSearch 「网盘 <片名>」：TMDB 搜索并下发选片单
func (h *Handler) wecomHandlePansouSearch(user, keyword string, reply func(...string)) {
	movies := h.wecomTmdbSendPickList(keyword, reply)
	if movies == nil {
		return
	}
	wecomPansouSessionSet(user, &wecomPansouSession{Stage: "movie", Keyword: keyword, Movies: movies, At: time.Now()})
}

// wecomHandlePansouPick 会话进行中收到序号：按阶段分流（选片 → 选资源）
func (h *Handler) wecomHandlePansouPick(user string, n int, reply func(...string)) {
	s := wecomPansouSessionGet(user)
	if s == nil {
		reply("当前没有进行中的网盘搜索，发送「网盘 <片名>」开始。")
		return
	}
	s.At = time.Now()
	switch s.Stage {
	case "movie":
		if n < 1 || n > len(s.Movies) {
			reply(fmt.Sprintf("序号超出范围（1-%d）", len(s.Movies)))
			return
		}
		m := s.Movies[n-1]
		reply("⏳ 正在 PanSou 聚合搜索「" + m.Title + "」…")
		items, err := pansouSearchItems(m.Title)
		if err != nil {
			reply("✗ PanSou 搜索失败: " + err.Error())
			return
		}
		if len(items) == 0 {
			reply("PanSou 没有找到「" + m.Title + "」的网盘分享，可重新「网盘 <其他片名>」")
			return
		}
		if len(items) > 10 {
			items = items[:10] // 消息长度限制，只发前 10 条（已按类型+时间排好）
		}
		s.Items = items
		s.Stage = "resource"
		s.Keyword = m.Title
		lines := []string{fmt.Sprintf("「%s」网盘资源 %d 条（115 分享自动转存，磁力/ed2k 提交离线，其他网盘回原链）：", m.Title, len(items))}
		for i, it := range items {
			line := fmt.Sprintf("%d. [%s] %s", i+1, pansouTypeLabel(it.CloudType), truncateStr(firstNonEmptyStr(it.Note, it.URL), 52))
			var meta []string
			if it.Password != "" {
				meta = append(meta, "提取码 "+it.Password)
			}
			if t := strings.Replace(it.Datetime, "T", " ", 1); len(t) >= 16 {
				meta = append(meta, t[:16])
			}
			if len(meta) > 0 {
				line += "\n     " + strings.Join(meta, " | ")
			}
			lines = append(lines, line)
		}
		lines = append(lines, "（回复 1-"+strconv.Itoa(len(items))+" 处理，5 分钟内有效）")
		reply(lines...)
	case "resource":
		if n < 1 || n > len(s.Items) {
			reply(fmt.Sprintf("序号超出范围（1-%d）", len(s.Items)))
			return
		}
		it := s.Items[n-1]
		label := pansouTypeLabel(it.CloudType)
		switch it.Action {
		case "transfer":
			reply("⏳ 正在转存 115…")
			msg, success, _, err := h.shareReceiveCore(it.URL, it.Password, h.shareFolderCid(), true)
			if err != nil {
				reply("✗ 转存失败: " + err.Error())
				return
			}
			reply("✓ "+msg, fmt.Sprintf("成功 %d 项，完成后自动整理入库。", success))
		case "offline":
			reply("⏳ 提交 115 离线下载…")
			if err := h.submitOfflineLink(it.URL); err != nil {
				reply("✗ 离线提交失败: " + err.Error())
				return
			}
			reply("✓ 已提交 115 离线下载。下载完成后自动整理入库并通知。")
		default:
			out := fmt.Sprintf("[%s] 链接（StrmHub 仅支持 115 自动转存，请手动打开）：\n%s", label, it.URL)
			if it.Password != "" {
				out += "\n提取码: " + it.Password
			}
			reply(out)
		}
	}
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
