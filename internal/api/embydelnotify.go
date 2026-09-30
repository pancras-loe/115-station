package api

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ---- Emby 删除通知：一次删除一条消息 ----
//
// 在 Emby 里删一部片，此前会收到三条消息（2026-09-30 现场）：
// 「🗑️ Emby 删除 欢乐好声音」、深删完成的「🗑️ 深度删除 …strm」，
// 以及 Emby 刷新时收掉空片目推回来的「🗑️ Emby 删除 欢乐好声音.2016.{tmdbid=…}」（Folder）。
// 装了神医助手还会多一对 deep.delete / library.deleted。现在：
//   - Folder 一类的目录条目不推（embyDeleteKind），那是 Emby 在收拾空目录，不是用户删了什么；
//   - 同一条目的几条事件按条目 id 聚成一份，等它们的深删都跑完再发**一条**，
//     深删结果（删了几个网盘文件 / 被拦下的原因 / 失败）写进同一条消息里。
//
// 代价是开着深删时通知要等深删跑完才发（深删要排 taskMu，整理占着锁时会晚一些），
// 换来的是消息里直接写清网盘那边动没动。

// embyDelGrace 最后一条事件的深删跑完后再等一会儿才发：
// deep.delete 与 library.deleted 到达只差几毫秒但顺序不保证，
// 深删开关关着时第一条可能在第二条到之前就处理完了
var embyDelGrace = 3 * time.Second

// 同一条目的事件在这段时间内都算同一次删除（原 embyDeleteDuplicate 的窗口）
const embyDelWindow = 2 * time.Minute

// embyDelSend 可替换，测试里收消息用
var embyDelSend = func(title, content string) { go NotifyMessage(title, content) }

type embyDelNotice struct {
	title   string // 空 = 不推这次删除本身（本站自产的回声），只在深删被拦下 / 失败时单报
	content string
	pending int
	sent    bool
	out     deepDelOutcome
	timer   *time.Timer
	at      time.Time
}

var (
	embyDelMu      sync.Mutex
	embyDelNotices = map[string]*embyDelNotice{}
)

// embyDeleteKind Emby 条目类型 → 通知里的叫法。ok=false 的是目录条目，不推通知。
// 类型为空（Jellyfin 等载荷不带 Item.Type）照常推，只是不标类型
func embyDeleteKind(itemType string) (label string, ok bool) {
	switch itemType {
	case "Folder", "CollectionFolder", "AggregateFolder", "UserRootFolder":
		return "", false
	case "Movie":
		return "电影", true
	case "Series":
		return "剧集", true
	case "Season":
		return "季", true
	case "Episode":
		return "单集", true
	case "Video":
		return "视频", true
	case "BoxSet":
		return "合集", true
	}
	return itemType, true
}

func embyDeleteTitle(label string) string {
	if label == "" {
		return "🗑️ Emby 删除"
	}
	return "🗑️ Emby 删除 · " + label
}

// trackEmbyDelete 登记一条删除事件并跑它的深删（run），同一 key 的事件合成一条通知。
// title 为空表示这次删除本身不推通知。key 为空（拿不到条目 id 与路径）时单独成一份，
// 宁可重复通知，也不要把两次真实删除吃掉一次
func trackEmbyDelete(key, title, content string, run func() deepDelOutcome) {
	embyDelMu.Lock()
	now := time.Now()
	for k, n := range embyDelNotices {
		if n.sent && now.Sub(n.at) > embyDelWindow {
			delete(embyDelNotices, k)
		}
	}
	n := embyDelNotices[key]
	if n == nil || (n.sent && now.Sub(n.at) > embyDelWindow) {
		n = &embyDelNotice{title: title, content: content, at: now}
		if key != "" {
			embyDelNotices[key] = n
		}
	} else if n.title == "" && title != "" {
		n.title, n.content = title, content
	}
	late := n.sent
	n.pending++
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	embyDelMu.Unlock()

	go func() {
		out := run()
		embyDelMu.Lock()
		defer embyDelMu.Unlock()
		n.pending--
		if late {
			// 消息已经发出去了：同一次删除的迟到事件，深删结果只留日志与深删记录
			if out.Res.Fids > 0 || out.Rejected != "" || out.Err != "" {
				log.Printf("[Emby Webhook] ○ 删除通知已发出，迟到事件的深删结果未并入通知（见深度删除记录）")
			}
			return
		}
		n.out = mergeDeepDelOutcome(n.out, out)
		if n.pending == 0 {
			n.timer = time.AfterFunc(embyDelGrace, func() { flushEmbyDelete(n) })
		}
	}()
}

func flushEmbyDelete(n *embyDelNotice) {
	embyDelMu.Lock()
	if n.pending > 0 || n.sent {
		embyDelMu.Unlock()
		return
	}
	n.sent, n.at = true, time.Now()
	title, content, out := n.title, n.content, n.out
	embyDelMu.Unlock()

	lines := deepDelOutcomeLines(out)
	if title == "" {
		// 自产删除的回声不报，但深删在这上面被拦下 / 失败还是要让用户知道
		if len(lines) == 0 || (out.Rejected == "" && out.Err == "") {
			return
		}
		title = "🗑️ 深度删除"
	}
	if len(lines) > 0 {
		if content != "" {
			content += "\n"
		}
		content += strings.Join(lines, "\n")
	}
	embyDelSend(title, content)
}

func mergeDeepDelOutcome(a, b deepDelOutcome) deepDelOutcome {
	a.Res.Videos += b.Res.Videos
	a.Res.Assets += b.Res.Assets
	a.Res.Fids += b.Res.Fids
	a.Res.PanDirs += b.Res.PanDirs
	if a.Rejected == "" {
		a.Rejected = b.Rejected
	}
	if a.Err == "" {
		a.Err = b.Err
	}
	return a
}

// deepDelOutcomeLines 深删结果在通知里的几行；深删没动任何东西时为空
func deepDelOutcomeLines(out deepDelOutcome) []string {
	var lines []string
	if out.Res.Fids > 0 {
		s := fmt.Sprintf("已联动删除网盘源文件 %d 个（视频 %d / 附属 %d）", out.Res.Fids, out.Res.Videos, out.Res.Assets)
		if out.Res.PanDirs > 0 {
			s += fmt.Sprintf("，空目录 %d 个", out.Res.PanDirs)
		}
		lines = append(lines, s, "文件在 115 回收站里，可还原")
	}
	if out.Err != "" {
		lines = append(lines, "✗ 联动删除网盘文件失败："+out.Err)
	}
	if out.Rejected != "" {
		lines = append(lines, "⚠️ 网盘文件没有联动删除，已被拦下："+out.Rejected)
	}
	return lines
}
