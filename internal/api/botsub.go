package api

// ==================== 机器人订阅：/sub 片名、/subs，以及找资源列表里的「订阅这部」 ====================
//
// 走找资源同一套会话（botflow.go 的 botFlow / botView），TG 按钮与企微文字指令都不用另写：
//   - /sub 片名：TMDB 选片（只有一部直接订），选定即按默认值订阅 —— 默认值与网页订阅弹窗相同：
//     补缺集；剧集还在播、而且不止一季时只订最新一季（全剧往往要补几百集，多半不是想要的）；
//   - 找资源的资源列表里多一个「🔔 订阅这部」（s），搜到一半想改成订阅不用重来；
//   - /subs：列出订阅，选一个看进度，能立即搜索、暂停 / 恢复、取消订阅（要再点一次确认）。
// 新建一律走 createSubscription，与网页同一条路。

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"
)

// 会话模式（botFlow.mode）
const (
	botModeFind = ""     // 找资源
	botModeSub  = "sub"  // /sub：选片即订阅
	botModeSubs = "subs" // /subs：管理订阅
)

// botSubFind /sub 片名
func (h *Handler) botSubFind(key, keyword string, io botIO) botView {
	f := &botFlow{token: botNewToken(), keyword: keyword, mode: botModeSub, at: time.Now()}
	botFlowPut(key, f)
	f.mu.Lock()
	defer f.mu.Unlock()

	io.report("正在查询 TMDB「" + keyword + "」…")
	movies, err := h.wecomTmdbMulti(keyword)
	switch {
	case err != nil:
		botFlowDrop(key, f)
		return botNote("TMDB 查询失败：" + err.Error() + "。订阅要先在 TMDB 上定下是哪一部。")
	case len(movies) == 0:
		botFlowDrop(key, f)
		return botNote("TMDB 没有找到「" + keyword + "」。可以换个片名，或直接发 TMDB ID。")
	case len(movies) == 1:
		botFlowDrop(key, f)
		return botNote(h.botSubscribe(movies[0])...)
	}
	f.movies, f.owned = movies, botOwned(h.DB, movies)
	f.stage, f.page = "media", 0
	return f.render()
}

// botSubDefaultForm 机器人订阅的默认值（与网页订阅弹窗一致）
func botSubDefaultForm(tc *TmdbClient, hit wecomTmdbHit) subForm {
	f := subForm{TmdbID: hit.ID, MediaType: hit.Type, Scope: subScopeAll, Follow: subFollowMissing}
	if hit.Type != "tv" || tc == nil {
		return f
	}
	d, err := tc.detailOf("tv", hit.ID)
	if err != nil || d == nil {
		return f
	}
	latest := 0
	for s := range d.SeasonEps {
		latest = max(latest, s)
	}
	if !tmdbEnded(d.Status) && latest > 1 {
		f.Scope, f.Season = subScopeSeason, latest
	}
	return f
}

// botSubscribe 订阅一部，返回给人看的几行
func (h *Handler) botSubscribe(hit wecomTmdbHit) []string {
	if hit.ID <= 0 || (hit.Type != "movie" && hit.Type != "tv") {
		return []string{"订阅要先在 TMDB 上定下是哪一部（按关键词搜的结果不能订阅）。"}
	}
	tc, _ := loadTmdbClient()
	sub, _, err := h.createSubscription(botSubDefaultForm(tc, hit), "bot")
	name := "《" + hit.Title + "》"
	switch {
	case errors.Is(err, errSubExists):
		return []string{"🔔 " + name + "已经订阅过了：" + botSubProgress(&sub), "发送 /subs 查看或修改。"}
	case err != nil:
		return []string{"✗ 订阅" + name + "失败：" + err.Error()}
	}
	lines := []string{fmt.Sprintf("🔔 已订阅%s（%s）", name, botSubScopeText(&sub)), "正在检查缺什么，找到资源会自动转存入库，提交时会通知。"}
	if sub.MediaType == "tv" && sub.Scope == subScopeSeason {
		lines = append(lines, "剧集还在播，默认只订最新一季；要订全剧或改范围，到网页「影视转存 → 订阅」里修改。")
	}
	return lines
}

// botSubScopeText 范围一句话（与前端 scopeText 同口径）
func botSubScopeText(s *model.Subscription) string {
	if s.MediaType == "movie" {
		return "电影"
	}
	t := "全剧"
	switch s.Scope {
	case subScopeSeason:
		t = fmt.Sprintf("第 %d 季", s.Season)
	case subScopeRange:
		t = fmt.Sprintf("第 %d 季 E%d", s.Season, s.EpStart)
		if s.EpEnd > 0 {
			t += fmt.Sprintf("–E%d", s.EpEnd)
		} else {
			t += " 起"
		}
	}
	if s.Follow == subFollowNew {
		return t + " · 只追新集"
	}
	return t + " · 补缺集"
}

var botSubStateText = map[string]string{
	subStateActive: "追更中", subStatePaused: "已暂停", subStateDone: "已完成", subStateStalled: "长期找不到",
}

// botSubProgress 「已有 3/8 集，缺 2」「已入库」
func botSubProgress(s *model.Subscription) string {
	if s.MediaType == "movie" {
		if s.Have > 0 {
			return "已入库"
		}
		return "还没入库"
	}
	if s.Total == 0 {
		if s.LastCheckAt == nil {
			return "还没检查过"
		}
		return "还没有已播的集"
	}
	t := fmt.Sprintf("已有 %d/%d 集", s.Have, s.Total)
	if s.Missing > 0 {
		t += fmt.Sprintf("，缺 %d", s.Missing)
	}
	return t
}

// ==================== /subs ====================

// botSubsList /subs：列出订阅（追更中在前）
func (h *Handler) botSubsList(key string) botView {
	f := &botFlow{token: botNewToken(), mode: botModeSubs, stage: "subs", at: time.Now()}
	if !h.botSubsLoad(f) {
		return botNote("还没有订阅。发送 /sub 片名 订阅一部，或在找资源的结果里点「订阅这部」。")
	}
	botFlowPut(key, f)
	return f.render()
}

// botSubsLoad 重新读订阅列表；一条都没有返回 false
func (h *Handler) botSubsLoad(f *botFlow) bool {
	var rows []model.Subscription
	h.DB.Order("CASE state WHEN 'active' THEN 0 WHEN 'stalled' THEN 1 WHEN 'paused' THEN 2 ELSE 3 END, updated_at DESC").Find(&rows)
	f.subs = rows
	return len(rows) > 0
}

// botSubsAct /subs 会话里的操作。列表：序号选订阅、n/p 翻页；单个订阅：a 立即搜索、z 暂停 / 恢复、x 取消订阅（xx 确认）、b 返回
func (h *Handler) botSubsAct(key string, f *botFlow, act string, io botIO) botView {
	switch act {
	case "q":
		botFlowDrop(key, f)
		return botNote("已关闭。")
	case "n", "p":
		if f.stage != "subs" {
			return botSay("先返回列表（b）再翻页。")
		}
		if act == "n" && f.page+1 >= f.pages() {
			return botSay("已经是最后一页了。")
		}
		if act == "p" && f.page == 0 {
			return botSay("已经是第一页了。")
		}
		if act == "n" {
			f.page++
		} else {
			f.page--
		}
		return f.render()
	case "b":
		if f.stage == "subs" {
			return botSay("已经在列表里了。")
		}
		h.botSubsLoad(f)
		f.stage, f.subSel = "subs", 0
		return f.render()
	}

	if f.stage == "subs" {
		n, err := strconv.Atoi(act)
		if err != nil || n < 1 || n > len(f.subs) {
			return botSay(fmt.Sprintf("请选择 1-%d。", len(f.subs)))
		}
		f.stage, f.subSel = "sub", n-1
		return f.render()
	}

	// 单个订阅：先从库里重读（可能刚被网页改过 / 删了）
	cur := f.subs[f.subSel]
	var sub model.Subscription
	if h.DB.First(&sub, cur.ID).Error != nil {
		h.botSubsLoad(f)
		f.stage = "subs"
		v := f.render()
		v.Lines = append([]string{"这个订阅已经被删掉了。", ""}, v.Lines...)
		return v
	}
	switch act {
	case "a":
		if _, err := enqueueSubscribeJob(h, []uint{sub.ID}, true, sub.Title, "bot"); err != nil {
			return botSay("✗ 入队失败：" + err.Error())
		}
		io.say("🔍 已开始搜索《" + sub.Title + "》，提交了资源会通知。")
		return botView{}
	case "z":
		upd := map[string]any{"state": subStatePaused}
		msg := "⏸ 已暂停《" + sub.Title + "》"
		if sub.State == subStatePaused {
			now := time.Now()
			upd = map[string]any{"state": subStateActive, "empty_rounds": 0, "next_check_at": now}
			msg = "▶ 已恢复追更《" + sub.Title + "》"
		}
		h.DB.Model(&sub).Updates(upd)
		h.DB.First(&f.subs[f.subSel], sub.ID)
		io.say(msg)
		v := f.render()
		v.Refresh = true
		return v
	case "x":
		f.confirmDel = true
		return f.render()
	case "xx":
		if !f.confirmDel {
			return botSay("先点「取消订阅」。")
		}
		h.DB.Where("sub_id = ?", sub.ID).Delete(&model.SubAttempt{})
		h.DB.Delete(&sub)
		io.say("🗑 已取消订阅《" + sub.Title + "》（已转存、入库的不受影响）")
		f.confirmDel = false
		if !h.botSubsLoad(f) {
			botFlowDrop(key, f)
			return botNote("已经没有订阅了。")
		}
		f.stage, f.subSel, f.page = "subs", 0, 0
		return f.render()
	}
	return botSay("看不懂这个操作。")
}

// renderSubs /subs 会话的两屏：列表、单个订阅
func (f *botFlow) renderSubs() botView {
	v := botView{Token: f.token}
	if f.stage == "sub" {
		s := &f.subs[f.subSel]
		head := "《" + s.Title + "》"
		if s.Year != "" {
			head += "（" + s.Year + "）"
		}
		v.Lines = append(v.Lines,
			"🔔 "+head,
			botSubStateText[s.State]+" · "+botSubScopeText(s),
			botSubProgress(s),
		)
		if s.LastResult != "" {
			v.Lines = append(v.Lines, "上一轮："+s.LastResult)
		}
		if s.NextCheckAt != nil && s.State != subStatePaused && s.State != subStateDone {
			v.Lines = append(v.Lines, "下次检查："+s.NextCheckAt.Format("01-02 15:04"))
		}
		pause := botBtn{Text: "⏸ 暂停", Act: "z"}
		if s.State == subStatePaused {
			pause = botBtn{Text: "▶ 恢复", Act: "z"}
		}
		del := botBtn{Text: "🗑 取消订阅", Act: "x"}
		hint := "回复 a 立即搜索；z 暂停 / 恢复；x 取消订阅；b 返回列表；q 退出"
		if f.confirmDel {
			v.Lines = append(v.Lines, "", "确定取消订阅？只删订阅和它的记账，已经转存、入库的不受影响。")
			del = botBtn{Text: "⚠ 确认取消订阅", Act: "xx"}
			hint = "回复 xx 确认取消订阅；b 返回列表"
		}
		v.Buttons = [][]botBtn{
			{{Text: "🔍 立即搜索", Act: "a"}, pause},
			{del},
			{{Text: "↩ 返回列表", Act: "b"}, {Text: "✕ 关闭", Act: "q"}},
		}
		v.Hint = "（" + hint + "，10 分钟内有效）"
		return v
	}

	if f.page >= f.pages() {
		f.page = f.pages() - 1
	}
	start := f.page * botPageSize
	end := min(start+botPageSize, len(f.subs))
	pageNote := ""
	if f.pages() > 1 {
		pageNote = fmt.Sprintf("，第 %d/%d 页", f.page+1, f.pages())
	}
	v.Lines = append(v.Lines, fmt.Sprintf("订阅 %d 个%s：", len(f.subs), pageNote))
	var nums []botBtn
	for i := start; i < end; i++ {
		s := &f.subs[i]
		line := fmt.Sprintf("%d. %s", i+1, s.Title)
		if s.Year != "" {
			line += "（" + s.Year + "）"
		}
		line += " " + botSubStateText[s.State] + " · " + botSubProgress(s)
		v.Lines = append(v.Lines, line)
		nums = append(nums, botBtn{Text: strconv.Itoa(i + 1), Act: strconv.Itoa(i + 1)})
	}
	for i := 0; i < len(nums); i += 4 {
		v.Buttons = append(v.Buttons, nums[i:min(i+4, len(nums))])
	}
	hint := []string{fmt.Sprintf("回复 1-%d 查看", len(f.subs))}
	var nav []botBtn
	if f.page > 0 {
		nav = append(nav, botBtn{Text: "◀ 上一页", Act: "p"})
	}
	if f.page+1 < f.pages() {
		nav = append(nav, botBtn{Text: "下一页 ▶", Act: "n"})
	}
	if len(nav) > 0 {
		v.Buttons = append(v.Buttons, nav)
		hint = append(hint, "n/p 翻页")
	}
	v.Buttons = append(v.Buttons, []botBtn{{Text: "✕ 关闭", Act: "q"}})
	v.Hint = "（" + strings.Join(append(hint, "q 退出"), "；") + "，10 分钟内有效）"
	return v
}
