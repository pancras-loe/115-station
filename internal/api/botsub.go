package api

// ==================== 机器人订阅：/sub 片名、/subs，以及找资源列表里的「订阅这部」 ====================
//
// 走找资源同一套会话（botflow.go 的 botFlow / botView），TG 按钮与企微文字指令都不用另写：
//   - /sub 片名：TMDB 选片（只有一部也要确认，按片名和按 TMDB 编号查询都一样），选定即按默认值订阅 —— 默认值与网页订阅弹窗相同：
//     补缺集；剧集还在播、而且不止一季时只订最新一季（全剧往往要补几百集，多半不是想要的）；
//   - 找资源的资源列表里多一个「🔔 订阅这部」（s），搜到一半想改成订阅不用重来；
//   - /subs：列出订阅，选一个看进度，能立即搜索、暂停 / 恢复、改范围、取消订阅（要再点一次确认）；
//   - 改范围（全剧 / 换一季 / 只追新集）见 botsubscope.go：剧集订完会话不关，直接给改范围那屏。
// 新建一律走 createSubscription，与网页同一条路。

import (
	"errors"
	"fmt"
	"regexp"
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
	keyword, want := botSplitScopeWant(keyword)
	f := &botFlow{token: botNewToken(), keyword: keyword, mode: botModeSub, subWant: want, at: time.Now()}
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
	}
	f.movies, f.owned = movies, botOwned(h.DB, movies)
	f.stage, f.page = "media", 0
	return f.render()
}

// subDefaultForm 一键订阅的默认值（机器人与找资源海报上的「订阅」共用，与网页订阅弹窗一致）
func subDefaultForm(tc *TmdbClient, id int, kind string) subForm {
	f := subForm{TmdbID: id, MediaType: kind, Scope: subScopeAll, Follow: subFollowMissing, Wash: loadSubscribeCfg().WashDefault}
	if kind != "tv" || tc == nil {
		return f
	}
	d, err := tc.detailOf("tv", id)
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

// botSubOutcome 订阅一部的结果
type botSubOutcome struct {
	lines     []string
	sub       model.Subscription
	ok        bool // sub 有效：刚订上，或者早就订过（exists）
	exists    bool
	defaulted bool // 剧集还在播，按默认只订了最新一季
}

// botSubscribeOne 订阅一部；want 是片名后面写的范围（零值按默认）
func (h *Handler) botSubscribeOne(hit wecomTmdbHit, want botScopeWant) botSubOutcome {
	if hit.ID <= 0 || (hit.Type != "movie" && hit.Type != "tv") {
		return botSubOutcome{lines: []string{"订阅要先在 TMDB 上定下是哪一部（按关键词搜的结果不能订阅）。"}}
	}
	name := "《" + hit.Title + "》"
	tc, _ := loadTmdbClient()
	form := subDefaultForm(tc, hit.ID, hit.Type)
	defaulted := form.Scope == subScopeSeason
	var extra []string
	if want.set() {
		switch {
		case hit.Type != "tv":
			extra = append(extra, "电影没有季，片名后面的范围不用写。")
		case want.All:
			form.Scope, form.Season, defaulted = subScopeAll, 0, false
		default:
			var f botFlow
			botLoadSubSeasons(&f, hit.ID)
			if f.seasons != nil && !botHasSeason(f.seasons, want.Season) {
				return botSubOutcome{lines: []string{fmt.Sprintf("✗ %s TMDB 上没有第 %d 季（%s），没有订阅。", name, want.Season, botSeasonSpan(f.seasons))}}
			}
			form.Scope, form.Season, defaulted = subScopeSeason, want.Season, false
		}
	}
	sub, _, err := h.createSubscription(form, "bot")
	switch {
	case errors.Is(err, errSubExists):
		return botSubOutcome{lines: []string{"🔔 " + name + "已经订阅过了：" + botSubScopeText(&sub) + "，" + botSubProgress(&sub)}, sub: sub, ok: true, exists: true}
	case err != nil:
		return botSubOutcome{lines: []string{"✗ 订阅" + name + "失败：" + err.Error()}}
	}
	lines := []string{fmt.Sprintf("🔔 已订阅%s（%s）", name, botSubScopeText(&sub)), "正在检查缺什么，找到资源会自动转存入库，提交时会通知。"}
	return botSubOutcome{lines: append(lines, extra...), sub: sub, ok: true, defaulted: defaulted}
}

// botSubscribe 订阅一部，返回给人看的几行（批量订阅、找资源列表里的「订阅这部」用：那里不接着改范围，只说怎么改）
func (h *Handler) botSubscribe(hit wecomTmdbHit, want botScopeWant) []string {
	out := h.botSubscribeOne(hit, want)
	switch {
	case out.exists:
		return append(out.lines, "发送 /subs 查看或修改。")
	case out.defaulted:
		return append(out.lines, "剧集还在播，默认只订最新一季；要订全剧或换一季，发 /subs 选这部点「改范围」。")
	}
	return out.lines
}

// botSubscribeOpen 订阅一部并接着给改范围的入口（/sub 只选了一部时）。
// 剧集的会话不关，转成 /subs 会话停在这个订阅上：按默认缩小了范围、或者早就订过的，直接给改范围那屏；
// 其余只给一个「改范围」按钮。电影与失败的照旧一条消息结束
func (h *Handler) botSubscribeOpen(key string, f *botFlow, hit wecomTmdbHit) botView {
	out := h.botSubscribeOne(hit, f.subWant)
	if !out.ok || out.sub.MediaType != "tv" {
		botFlowDrop(key, f)
		if out.exists {
			out.lines = append(out.lines, "发送 /subs 查看或修改。")
		}
		return botNote(out.lines...)
	}
	f.mode, f.subs, f.subSel, f.page, f.movies, f.confirmDel = botModeSubs, []model.Subscription{out.sub}, 0, 0, nil, false
	f.lead = out.lines
	switch {
	case out.defaulted:
		f.lead = append(f.lead, "剧集还在播，默认只订最新一季；要订全剧或换一季，点下面。")
	case out.exists:
		f.lead = append(f.lead, "要改范围点下面。")
	default:
		f.stage = "created"
		return f.render()
	}
	f.stage = "scope"
	botLoadSubSeasons(f, out.sub.TmdbID)
	return f.render()
}

// botPicksMax 一条消息里最多认几个序号（选片列表本来就不超过 20 部）
const botPicksMax = 20

// botPickSplit 序号之间的分界符：中英文逗号、顿号、分号、斜杠、加号与空白
var botPickSplit = regexp.MustCompile(`[,，、;；/+\s]+`)

// botParsePicks 解析「1,3,5」「1 3」「2-4」这类几个序号的写法，按出现顺序去重。
// 有一段不是序号（或区间）就整句不认，免得把普通聊天当成选片
func botParsePicks(text string) []int {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 60 {
		return nil
	}
	var out []int
	seen := map[int]bool{}
	add := func(n int) bool {
		if !seen[n] {
			if len(out) >= botPicksMax {
				return false
			}
			seen[n] = true
			out = append(out, n)
		}
		return true
	}
	for _, seg := range botPickSplit.Split(t, -1) {
		if seg == "" {
			continue
		}
		if a, b, ok := strings.Cut(strings.ReplaceAll(seg, "~", "-"), "-"); ok {
			lo, e1 := strconv.Atoi(a)
			hi, e2 := strconv.Atoi(b)
			if e1 != nil || e2 != nil || len(a) > 3 || len(b) > 3 || lo > hi {
				return nil
			}
			for n := lo; n <= hi; n++ {
				if !add(n) {
					return nil
				}
			}
			continue
		}
		if len(seg) > 3 || !regexpPureDigits.MatchString(seg) {
			return nil
		}
		n, _ := strconv.Atoi(seg)
		if !add(n) {
			return nil
		}
	}
	return out
}

// botSubscribeMany 一次订几部（序号从 1 起）：每部一行结果，共同的说明只写一遍
func (h *Handler) botSubscribeMany(movies []wecomTmdbHit, picks []int, want botScopeWant) []string {
	lines := []string{fmt.Sprintf("批量订阅 %d 部：", len(picks))}
	var notes []string
	seen := map[string]bool{}
	for _, n := range picks {
		res := h.botSubscribe(movies[n-1], want)
		if len(res) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", n, res[0]))
		for _, s := range res[1:] {
			if !seen[s] {
				seen[s] = true
				notes = append(notes, s)
			}
		}
	}
	return append(lines, notes...)
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
		if (f.stage == "scope" || f.stage == "created") && f.subSel < len(f.subs) {
			// TG 会把这一屏原地换成关闭语：订阅结果别跟着没了，留一句现在订的是什么
			var cur model.Subscription
			if h.DB.First(&cur, f.subs[f.subSel].ID).Error == nil {
				return botNote("🔔 《"+cur.Title+"》："+botSubScopeText(&cur)+"。", "以后要改，发 /subs 选这部点「改范围」。")
			}
		}
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
		if f.stage == "scope" || f.stage == "created" {
			// 改范围那屏退回这个订阅的详情（订阅详情里再 b 才回列表）
			f.stage, f.lead = "sub", nil
			var cur model.Subscription
			if h.DB.First(&cur, f.subs[f.subSel].ID).Error == nil {
				f.subs[f.subSel] = cur
				return f.render()
			}
		}
		h.botSubsLoad(f)
		f.stage, f.subSel = "subs", 0
		return f.render()
	}

	if f.stage == "subs" {
		if strings.HasPrefix(act, "s") || act == "e" || act == "fn" || act == "fm" {
			return botSay("先选一个订阅（回复序号），再改范围。")
		}
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
		f.stage, f.lead = "subs", nil
		v := f.render()
		v.Lines = append([]string{"这个订阅已经被删掉了。", ""}, v.Lines...)
		return v
	}
	// 改范围那屏里直接回季号也算选那一季
	if f.stage == "scope" && regexpPureDigits.MatchString(act) {
		act = "s" + act
	}
	switch act {
	case "e":
		if sub.MediaType != "tv" {
			return botSay("电影没有范围可改。")
		}
		f.subs[f.subSel] = sub
		f.stage, f.lead, f.confirmDel = "scope", nil, false
		botLoadSubSeasons(f, sub.TmdbID)
		return f.render()
	case "sa", "fn", "fm", "sp":
		return h.botSubSetScope(f, &sub, act)
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
	if strings.HasPrefix(act, "s") {
		return h.botSubSetScope(f, &sub, act)
	}
	return botSay("看不懂这个操作。")
}

// renderSubs /subs 会话的两屏：列表、单个订阅
func (f *botFlow) renderSubs() botView {
	switch f.stage {
	case "scope":
		return f.renderScope()
	case "created":
		return f.renderCreated()
	}
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
		if s.MediaType == "tv" {
			hint = "回复 a 立即搜索；e 改范围；z 暂停 / 恢复；x 取消订阅；b 返回列表；q 退出"
		}
		if f.confirmDel {
			v.Lines = append(v.Lines, "", "确定取消订阅？只删订阅和它的记账，已经转存、入库的不受影响。")
			del = botBtn{Text: "⚠ 确认取消订阅", Act: "xx"}
			hint = "回复 xx 确认取消订阅；b 返回列表"
		}
		row2 := []botBtn{del}
		if s.MediaType == "tv" {
			row2 = []botBtn{{Text: "✏ 改范围", Act: "e"}, del}
		}
		v.Buttons = [][]botBtn{
			{{Text: "🔍 立即搜索", Act: "a"}, pause},
			row2,
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
