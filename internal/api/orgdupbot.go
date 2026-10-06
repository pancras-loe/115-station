package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

// ==================== 同集多份：通知、机器人确认、超时 ====================
//
// orgdup.go 停下来的「同集多份待选」要能在手机上选：
//   - TG：通知带按钮，点了原地编辑那条消息。按钮数据「d:<记录 id>:<组>:<操作>」认的是数据库里的记录，
//     不是找资源那种 10 分钟的内存会话 —— 几个小时之后点也得管用；
//   - 企微：文字说明 + 回复「多份 <id> <选择>」，TG 私聊里同样能这么回；
//   - 飞书 / QQ：只能单向推，写明到网页「任务中心 → 整理记录」处理。
// 谁先选算谁的：机器人只认还没选过的记录（网页可以改选）；选完一律走 submitDupChoice。
// 超时（org-basic.dup_auto_hours，默认 0 = 不超时）到点按 dup_auto_action 处理，同样推一条通知。

// dupBotSource 机器人来源（TaskJob.Source）：TG 的回复出口带「tg:」前缀
func dupBotSource(user string) string {
	if strings.HasPrefix(user, "tg:") {
		return "tg"
	}
	return "wecom"
}

// dupPick 一组的选择：k<n>（只留第 n 份，从 0 数）/ a（都留）/ x（都不要），没选为 ""
func dupPickAction(g dupGroup, pick string) (dupAction, bool) {
	switch {
	case pick == "a":
		return dupAction{Action: "keep_all"}, true
	case pick == "x":
		return dupAction{Action: "drop_all"}, true
	case strings.HasPrefix(pick, "k"):
		i, err := strconv.Atoi(pick[1:])
		if err != nil || i < 0 || i >= len(g.Files) {
			return dupAction{}, false
		}
		return dupAction{Action: "keep", Fid: g.Files[i].Fid}, true
	}
	return dupAction{}, false
}

// dupPickText 一组选择的大白话
func dupPickText(g dupGroup, pick string) string {
	switch {
	case pick == "a":
		return "都保留（#A #B）"
	case pick == "x":
		return "都不要"
	case strings.HasPrefix(pick, "k"):
		if i, err := strconv.Atoi(pick[1:]); err == nil && i >= 0 && i < len(g.Files) {
			return "只留「" + dupFileName(g, i) + "」"
		}
	}
	return "未选"
}

// dupFileName 一份文件的叫法：区别词，认不出时「第 N 份」
func dupFileName(g dupGroup, i int) string {
	if l := g.Files[i].Label; l != "" {
		return l
	}
	return fmt.Sprintf("第 %d 份", i+1)
}

// dupBulkLabels 每组都恰好有一份带这个区别词的（整季双语）：可以一键「每组都留英语」
func dupBulkLabels(groups []dupGroup) []string {
	if len(groups) < 2 {
		return nil
	}
	var out []string
	for _, f := range groups[0].Files {
		if f.Label == "" {
			continue
		}
		ok := true
		for _, g := range groups {
			n := 0
			for _, gf := range g.Files {
				if gf.Label == f.Label {
					n++
				}
			}
			if n != 1 {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, f.Label)
		}
	}
	return out
}

// dupLabelPick 按区别词在一组里找那一份（等于优先，其次包含；不唯一就不认）
func dupLabelPick(g dupGroup, word string) (string, bool) {
	hit := -1
	for _, exact := range []bool{true, false} {
		n := 0
		for i, f := range g.Files {
			if (exact && f.Label == word) || (!exact && f.Label != "" && strings.Contains(f.Label, word)) {
				hit, n = i, n+1
			}
		}
		if n == 1 {
			return fmt.Sprintf("k%d", hit), true
		}
		if n > 1 {
			return "", false
		}
	}
	return "", false
}

// dupPicksToActions 每组都选了才转换
func dupPicksToActions(groups []dupGroup, picks []string) ([]dupAction, bool) {
	if len(picks) != len(groups) {
		return nil, false
	}
	out := make([]dupAction, len(groups))
	for i, g := range groups {
		a, ok := dupPickAction(g, picks[i])
		if !ok {
			return nil, false
		}
		out[i] = a
	}
	return out, true
}

// ---- 消息内容 ----

// dupNoticeLines 通知正文：哪部片、哪几集、每份是什么
func dupNoticeLines(rec *model.OrganizeRecord, groups []dupGroup, picks []string) []string {
	title := rec.Title
	if rec.Year != "" {
		title += " (" + rec.Year + ")"
	}
	lines := []string{fmt.Sprintf("⏸ 同集多份待选 · %s", title),
		fmt.Sprintf("记录 #%d：下面几集各有几份不同的文件，按命名规则会改成同一个名字，请选择保留哪份。", rec.ID)}
	for gi, g := range groups {
		what := g.Episode
		if what == "" {
			what = pathBase(g.Target)
		}
		head := fmt.Sprintf("【%d】%s", gi+1, what)
		if gi < len(picks) && picks[gi] != "" {
			head += " ✓ " + dupPickText(g, picks[gi])
		}
		lines = append(lines, "", head)
		for i, f := range g.Files {
			l := fmt.Sprintf("  %d. %s", i+1, dupFileName(g, i))
			if f.Size > 0 {
				l += " · " + formatBytes(f.Size)
			}
			if g.Recommend == f.Fid {
				l += "（推荐）"
			}
			lines = append(lines, l)
		}
	}
	return lines
}

// dupTGMaxGroups 超过这么多组就不逐组放按钮了（TG 一屏放不下），只给批量按钮，逐组请到网页
const dupTGMaxGroups = 4

// dupTGButtons TG 按钮。逐组一行：留第几份 / 都留 / 都不要；两组以上再加一行批量
func dupTGButtons(id uint, groups []dupGroup, picks []string) [][]tgButton {
	var rows [][]tgButton
	mark := func(gi int, act, text string) string {
		if gi < len(picks) && picks[gi] == act {
			return "✓ " + text
		}
		return text
	}
	if len(groups) <= dupTGMaxGroups {
		for gi, g := range groups {
			prefix := ""
			if len(groups) > 1 {
				prefix = fmt.Sprintf("%d·", gi+1)
			}
			var row []tgButton
			for i := range g.Files {
				act := fmt.Sprintf("k%d", i)
				row = append(row, tgButton{Text: mark(gi, act, prefix+"留"+dupFileName(g, i)),
					Data: fmt.Sprintf("d:%d:%d:%s", id, gi, act)})
			}
			row = append(row,
				tgButton{Text: mark(gi, "a", prefix+"都留"), Data: fmt.Sprintf("d:%d:%d:a", id, gi)},
				tgButton{Text: mark(gi, "x", prefix+"都不要"), Data: fmt.Sprintf("d:%d:%d:x", id, gi)})
			rows = append(rows, row)
		}
	}
	if len(groups) > 1 {
		var row []tgButton
		for i, l := range dupBulkLabels(groups) {
			row = append(row, tgButton{Text: "全部留" + l, Data: fmt.Sprintf("d:%d:*:l%d", id, i)})
		}
		row = append(row,
			tgButton{Text: "全部都留", Data: fmt.Sprintf("d:%d:*:a", id)},
			tgButton{Text: "全部不要", Data: fmt.Sprintf("d:%d:*:x", id)})
		rows = append(rows, row)
	}
	return rows
}

// dupCommandHelp 文字回复的用法（企微、TG 私聊都认）
func dupCommandHelp(rec *model.OrganizeRecord, groups []dupGroup) []string {
	lines := []string{"", "回复选择："}
	ex := fmt.Sprintf("多份 %d", rec.ID)
	lines = append(lines,
		fmt.Sprintf("「%s 2」只留第 2 份；「%s 全留」都保留（#A #B）；「%s 不要」都不要", ex, ex, ex))
	if len(groups) > 1 {
		lines = append(lines, fmt.Sprintf("几组分别选就按顺序写：「%s 1 2 全留」；只写一项就是每组都这么选", ex))
	}
	if ls := dupBulkLabels(groups); len(ls) > 0 {
		lines = append(lines, fmt.Sprintf("也可以写区别词：「%s %s」= 每组都留「%s」", ex, ls[0], ls[0]))
	} else if len(groups) == 1 && groups[0].Files[0].Label != "" {
		lines = append(lines, fmt.Sprintf("也可以写区别词：「%s %s」", ex, groups[0].Files[0].Label))
	}
	return lines
}

// ---- 通知 ----

// notifyDupHold 停下来时推一条通知。TG 带按钮（Chat ID 是个人私聊才行：按钮回调只认配置的私聊），
// 企微写回复指令，飞书 / QQ 指向网页
func notifyDupHold(rec *model.OrganizeRecord) {
	groups := parseDupGroups(rec.DupGroups)
	if rec.ID == 0 || len(groups) == 0 {
		return
	}
	cfg, err := loadMessageConfig()
	if err != nil {
		return
	}
	body := dupNoticeLines(rec, groups, nil)
	web := "到网页「任务中心 → 整理记录 → 待确认」选择。"
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go func(tg TGConfig) {
			if id, err := strconv.ParseInt(strings.TrimSpace(tg.ChatID), 10, 64); err == nil && id > 0 {
				lines := body
				if len(groups) > dupTGMaxGroups {
					lines = append(append([]string(nil), body...), "", "组数较多，逐组选择请"+web)
				}
				if err := tgDupSend(tg, id, strings.Join(lines, "\n"), dupTGButtons(rec.ID, groups, nil)); err != nil {
					log.Printf("[通知] ✗ TG 同集多份通知失败: %v", err)
				}
				return
			}
			// 群 / 频道：按钮回调只认私聊，只发文字
			_ = sendTelegramPlain(tg, strings.Join(append(append([]string(nil), body...), "", web), "\n"))
		}(cfg.TG)
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecom(cfg.Wecom, strings.Join(append(append([]string(nil), body...), dupCommandHelp(rec, groups)...), "\n"))
	}
	sendExtraChannels(cfg, body[0], strings.Join(append(append([]string(nil), body[1:]...), "", web), "\n"))
}

// tgClientFor 通知用的 TG 客户端（与接收器同一个 Token，走全局代理）
func tgClientFor(tg TGConfig) *tgAPI {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy := getProxyURL(); proxy != "" {
		if p, err := parseProxyURL(proxy); err == nil {
			transport.Proxy = p
		}
	}
	return &tgAPI{base: "https://api.telegram.org", token: strings.TrimSpace(tg.Token),
		client: &http.Client{Transport: transport, Timeout: 20 * time.Second}}
}

// tgDupSend 发一条带按钮的纯文本消息（文件名里的 < > & 不能走 HTML 模式）
func tgDupSend(tg TGConfig, chat int64, text string, buttons [][]tgButton) error {
	_, err := tgClientFor(tg).sendOne(context.Background(), chat, text, buttons)
	return err
}

// sendTelegramPlain 纯文本发到配置的 Chat ID（群 / 频道）
func sendTelegramPlain(tg TGConfig, text string) error {
	payload := map[string]any{"chat_id": strings.TrimSpace(tg.ChatID), "text": tgClip(text)}
	return tgClientFor(tg).call(context.Background(), "sendMessage", payload, nil)
}

// ---- 机器人的选择 ----

// dupBotPicks 多组记录在 TG 上逐组点的进度（记录 id → 每组的选择）。只在内存里：
// 重启丢了就是再点一遍，而真正的选择一旦凑齐就落库入队
var dupBotPicks = struct {
	sync.Mutex
	m map[uint][]string
}{m: map[uint][]string{}}

// errDupHandled 记录已经不在待选状态（网页上选过、已入库、被忽略）
var errDupHandled = errors.New("这一条已经处理过了")

// loadDupForBot 机器人只认还没选过的（谁先选算谁的；要改选请到网页）
func loadDupForBot(h *Handler, id uint) (*model.OrganizeRecord, []dupGroup, error) {
	var rec model.OrganizeRecord
	if h.DB.First(&rec, id).Error != nil || rec.Status != orgStatusAwaiting || !rec.HoldDup {
		return nil, nil, errDupHandled
	}
	if rec.DupChoice != "" {
		return nil, nil, errors.New("这一条已经选过了，正在排队；要改选请到网页")
	}
	groups := parseDupGroups(rec.DupGroups)
	if len(groups) == 0 {
		return nil, nil, errDupHandled
	}
	return &rec, groups, nil
}

// dupSubmitLines 提交后的回复
func dupSubmitLines(groups []dupGroup, picks []string, job model.TaskJob) []string {
	var parts []string
	for i, g := range groups {
		what := g.Episode
		if what == "" {
			what = fmt.Sprintf("第 %d 组", i+1)
		}
		parts = append(parts, what+" "+dupPickText(g, picks[i]))
	}
	return []string{"✓ 已选择：" + strings.Join(parts, "；"),
		fmt.Sprintf("已加入任务队列（任务 #%d），入库后另有通知。", job.ID)}
}

// dupTGCallback 处理 TG 按钮「d:<id>:<组|*>:<操作>」，返回编辑后的消息与按钮（按钮为 nil = 收起）
func (h *Handler) dupTGCallback(data string) (string, [][]tgButton) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 {
		return "按钮已失效。", nil
	}
	id64, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return "按钮已失效。", nil
	}
	id := uint(id64)
	rec, groups, err := loadDupForBot(h, id)
	if err != nil {
		dupBotPicks.Lock()
		delete(dupBotPicks.m, id)
		dupBotPicks.Unlock()
		return fmt.Sprintf("记录 #%d：%v。", id, err), nil
	}
	dupBotPicks.Lock()
	picks := dupBotPicks.m[id]
	if len(picks) != len(groups) {
		picks = make([]string, len(groups))
	}
	act := parts[3]
	if parts[2] == "*" {
		for i, g := range groups {
			switch {
			case act == "a" || act == "x":
				picks[i] = act
			case strings.HasPrefix(act, "l"):
				n, _ := strconv.Atoi(act[1:])
				ls := dupBulkLabels(groups)
				if n < 0 || n >= len(ls) {
					dupBotPicks.Unlock()
					return "按钮已失效。", nil
				}
				p, _ := dupLabelPick(g, ls[n])
				picks[i] = p
			}
		}
	} else if gi, err := strconv.Atoi(parts[2]); err == nil && gi >= 0 && gi < len(groups) {
		if _, ok := dupPickAction(groups[gi], act); ok {
			picks[gi] = act
		}
	}
	actions, full := dupPicksToActions(groups, picks)
	if !full {
		dupBotPicks.m[id] = picks
		dupBotPicks.Unlock()
		return strings.Join(dupNoticeLines(rec, groups, picks), "\n"), dupTGButtons(id, groups, picks)
	}
	delete(dupBotPicks.m, id)
	dupBotPicks.Unlock()
	job, err := h.submitDupChoice(rec, actions, "tg", false)
	if err != nil {
		return strings.Join(append(dupNoticeLines(rec, groups, picks), "", "✗ "+err.Error()), "\n"), nil
	}
	return strings.Join(append(dupNoticeLines(rec, groups, picks), append([]string{""}, dupSubmitLines(groups, picks, job)...)...), "\n"), nil
}

// parseDupReply 把「2」「全留」「不要」「英语」这些回复翻成每组的选择。
// 只写一项 = 每组都这么选；写多项要和组数一样多，按顺序对应
func parseDupReply(groups []dupGroup, tokens []string) ([]string, error) {
	if len(tokens) == 0 {
		return nil, errors.New("请写出选择")
	}
	if len(tokens) != 1 && len(tokens) != len(groups) {
		return nil, fmt.Errorf("这一条有 %d 组，要么写 1 项（每组都这么选），要么按顺序写 %d 项", len(groups), len(groups))
	}
	one := func(g dupGroup, t string) (string, error) {
		switch strings.ToLower(t) {
		case "全留", "都留", "都保留", "全部保留", "all", "a":
			return "a", nil
		case "不要", "都不要", "全不要", "none", "x":
			return "x", nil
		}
		if n, err := strconv.Atoi(t); err == nil {
			if n < 1 || n > len(g.Files) {
				return "", fmt.Errorf("没有第 %d 份（这一组共 %d 份）", n, len(g.Files))
			}
			return fmt.Sprintf("k%d", n-1), nil
		}
		if p, ok := dupLabelPick(g, t); ok {
			return p, nil
		}
		return "", fmt.Errorf("认不出「%s」：写份数序号、全留、不要，或区别词", t)
	}
	picks := make([]string, len(groups))
	for i, g := range groups {
		t := tokens[0]
		if len(tokens) > 1 {
			t = tokens[i]
		}
		p, err := one(g, t)
		if err != nil {
			if len(groups) > 1 {
				return nil, fmt.Errorf("第 %d 组：%v", i+1, err)
			}
			return nil, err
		}
		picks[i] = p
	}
	return picks, nil
}

// dupBotCommand 文字指令「多份」：不带参数列出待选的；「多份 <id> <选择…>」提交
func (h *Handler) dupBotCommand(user, args string) []string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		var recs []model.OrganizeRecord
		h.DB.Where("status = ? AND hold_dup = ? AND (dup_choice = '' OR dup_choice IS NULL)", orgStatusAwaiting, true).
			Order("id ASC").Limit(10).Find(&recs)
		if len(recs) == 0 {
			return []string{"没有等着选的同集多份。"}
		}
		lines := []string{"等着选的同集多份："}
		for i := range recs {
			gs := parseDupGroups(recs[i].DupGroups)
			if len(gs) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("#%d %s：%s", recs[i].ID, recs[i].Title, dupHoldMessage(gs)))
		}
		return append(lines, "", "回复「多份 <编号>」看详情，「多份 <编号> <选择>」提交。")
	}
	id64, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "#"), 10, 32)
	if err != nil {
		return []string{"用法：多份 <记录编号> <选择>，例如「多份 12 2」。发「多份」列出等着选的。"}
	}
	rec, groups, err := loadDupForBot(h, uint(id64))
	if err != nil {
		return []string{fmt.Sprintf("记录 #%d：%v。", id64, err)}
	}
	if len(fields) == 1 {
		return append(dupNoticeLines(rec, groups, nil), dupCommandHelp(rec, groups)...)
	}
	picks, err := parseDupReply(groups, fields[1:])
	if err != nil {
		return []string{"✗ " + err.Error()}
	}
	actions, _ := dupPicksToActions(groups, picks)
	job, err := h.submitDupChoice(rec, actions, dupBotSource(user), false)
	if err != nil {
		return []string{"✗ " + err.Error()}
	}
	return dupSubmitLines(groups, picks, job)
}

// ---- 超时 ----

// dupAutoConfig org-basic 里的超时设置
type dupAutoConfig struct {
	Hours  int    `json:"dup_auto_hours"`  // 0 = 不超时（默认）
	Action string `json:"dup_auto_action"` // keep_all（都留）/ recommend（有推荐留推荐，没有就都留）
}

// dupAutoPicks 超时时每组怎么选
func dupAutoPicks(groups []dupGroup, action string) []string {
	picks := make([]string, len(groups))
	for i, g := range groups {
		picks[i] = "a"
		if action == "recommend" && g.Recommend != "" {
			for fi, f := range g.Files {
				if f.Fid == g.Recommend {
					picks[i] = fmt.Sprintf("k%d", fi)
				}
			}
		}
	}
	return picks
}

// dupAutoTick 每分钟查一次：等过了期限还没人选的，按设置处理并推一条通知
func (h *Handler) dupAutoTick() {
	var ac dupAutoConfig
	if v := h.getSettingValue("org-basic"); v != "" {
		_ = json.Unmarshal([]byte(v), &ac)
	}
	if ac.Hours <= 0 {
		return
	}
	var recs []model.OrganizeRecord
	h.DB.Where("status = ? AND hold_dup = ? AND (dup_choice = '' OR dup_choice IS NULL) AND created_at < ?",
		orgStatusAwaiting, true, time.Now().Add(-time.Duration(ac.Hours)*time.Hour)).Limit(20).Find(&recs)
	for i := range recs {
		rec := &recs[i]
		groups := parseDupGroups(rec.DupGroups)
		if len(groups) == 0 {
			continue
		}
		picks := dupAutoPicks(groups, ac.Action)
		actions, _ := dupPicksToActions(groups, picks)
		job, err := h.submitDupChoice(rec, actions, "auto", false)
		if err != nil {
			log.Printf("[整理] ✗ 同集多份 #%d 超时自动处理失败: %v", rec.ID, err)
			continue
		}
		lines := dupSubmitLines(groups, picks, job)
		log.Printf("[整理] ○ 同集多份 #%d 等了 %d 小时没人选，%s", rec.ID, ac.Hours, lines[0])
		NotifyMessage(fmt.Sprintf("⏱ 同集多份超时自动处理 · %s", rec.Title),
			fmt.Sprintf("记录 #%d 等了 %d 小时没人选。\n%s", rec.ID, ac.Hours, strings.Join(lines, "\n")))
	}
}
