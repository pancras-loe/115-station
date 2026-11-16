package api

// ==================== 机器人改订阅范围 ====================
//
// 起因（2026-11-16 用户反馈）：「订阅 一拳超人」按默认只订了最新一季，回复里让去网页改，
// 用户在网页上没找到入口（要点海报 → 详情抽屉 →「修改」）。想改范围的时候就是刚订完那一刻，
// 所以把入口放在那里：
//   - 订阅成功（或已经订过）的剧集，会话不关，直接给一屏「改范围」：全剧 / 各季 / 补缺集·只追新集 / 特别篇；
//   - /subs 里点开单个剧集订阅多一个「✏ 改范围」（e），进同一屏；
//   - 订阅时直接写在片名后面：「订阅 一拳超人 全剧」「订阅 一拳超人 第2季」（botSplitScopeWant）。
//
// 只管范围与追剧模式（applySubScope，与网页同一套校验），别的字段一概不碰：网页可能设过资源条件、
// 优先级，机器人整份覆盖就把它们冲掉了。集段（E5–E10）要输两个数，聊天里来回问太别扭，留在网页。
// 每点一下立刻保存（markSubRecheck 与网页同口径：马上重新检查缺什么）。

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"
)

// botScopeWant 订阅时片名后面带的范围；零值 = 按默认
type botScopeWant struct {
	All    bool
	Season int
}

func (w botScopeWant) set() bool { return w.All || w.Season > 0 }

// 片名后面跟着的「全剧」「第2季」「第二季」「S2」：前面必须有空白，片名本身带这几个字（「XX全剧」）的不拆
var botScopeSuffix = regexp.MustCompile(`^(.+?)\s+(全剧|全部|第\s*([0-9]{1,3}|[零〇一二两三四五六七八九十百]{1,4})\s*季|[sS]\s*([0-9]{1,3}))$`)

// botSplitScopeWant 「一拳超人 全剧」→「一拳超人」+ 全剧
func botSplitScopeWant(keyword string) (string, botScopeWant) {
	m := botScopeSuffix.FindStringSubmatch(strings.TrimSpace(keyword))
	if m == nil {
		return keyword, botScopeWant{}
	}
	switch {
	case m[2] == "全剧" || m[2] == "全部":
		return strings.TrimSpace(m[1]), botScopeWant{All: true}
	case m[3] != "":
		if n := parseCnNumber(m[3]); n > 0 {
			return strings.TrimSpace(m[1]), botScopeWant{Season: n}
		}
	case m[4] != "":
		if n, _ := strconv.Atoi(m[4]); n > 0 {
			return strings.TrimSpace(m[1]), botScopeWant{Season: n}
		}
	}
	return keyword, botScopeWant{}
}

// botScopeAct 纯文本回复里的改范围操作：全剧 → sa；第2季 / 季2 / s2 → s2；追新 → fn；补缺 → fm；特别篇 → sp；改范围 → e
func botScopeAct(t string) (string, bool) {
	switch t {
	case "e", "改", "修改", "改范围", "范围":
		return "e", true
	case "sa", "全剧":
		return "sa", true
	case "fn", "追新", "只追新集", "追新集":
		return "fn", true
	case "fm", "补缺", "补缺集":
		return "fm", true
	case "sp", "特别篇":
		return "sp", true
	}
	if m := botSeasonText.FindStringSubmatch(t); m != nil {
		num := m[1] + m[2] + m[3]
		if n := parseCnNumber(num); n > 0 && n < 1000 {
			return "s" + strconv.Itoa(n), true
		}
	}
	return "", false
}

var botSeasonText = regexp.MustCompile(`^(?:第\s*([0-9]{1,3}|[零〇一二两三四五六七八九十百]{1,4})\s*季|季\s*([0-9]{1,3})|s\s*([0-9]{1,3}))$`)

// botSeason TMDB 上的一季
type botSeason struct {
	N   int
	Eps int
}

// botLoadSubSeasons 读这部剧的季列表（TMDB 详情有 30 分钟缓存，订阅时已经读过一次）
func botLoadSubSeasons(f *botFlow, tmdbID int) {
	if f.seasonsFor == tmdbID && f.seasons != nil {
		return
	}
	f.seasons, f.specialEps, f.seasonsFor = nil, 0, tmdbID
	tc, err := loadTmdbClient()
	if err != nil {
		return
	}
	d, err := tc.detailOf("tv", tmdbID)
	if err != nil || d == nil {
		return
	}
	out := []botSeason{}
	for s, n := range d.SeasonEps {
		if s == 0 {
			f.specialEps = n
			continue
		}
		out = append(out, botSeason{N: s, Eps: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	f.seasons = out
}

// botSubSetScope 改一下范围 / 模式并保存，回到改范围那屏
func (h *Handler) botSubSetScope(f *botFlow, sub *model.Subscription, act string) botView {
	if sub.MediaType != "tv" {
		return botSay("电影没有范围可改。")
	}
	botLoadSubSeasons(f, sub.TmdbID)
	form := subForm{Scope: sub.Scope, Season: sub.Season, EpStart: sub.EpStart, EpEnd: sub.EpEnd, Specials: sub.Specials, Follow: sub.Follow}
	switch {
	case act == "sa":
		form.Scope, form.Season, form.EpStart, form.EpEnd = subScopeAll, 0, 0, 0
	case act == "fn":
		form.Follow = subFollowNew
	case act == "fm":
		form.Follow = subFollowMissing
	case act == "sp":
		if sub.Scope != subScopeAll {
			return botSay("特别篇只在订全剧时能选（先点「全剧」）。")
		}
		form.Specials = !sub.Specials
	case strings.HasPrefix(act, "s"):
		n, err := strconv.Atoi(act[1:])
		if err != nil || n < 1 {
			return botSay("季号不对。")
		}
		if f.seasons != nil && !botHasSeason(f.seasons, n) {
			return botSay(fmt.Sprintf("TMDB 上没有第 %d 季（%s）。", n, botSeasonSpan(f.seasons)))
		}
		form.Scope, form.Season, form.EpStart, form.EpEnd = subScopeSeason, n, 0, 0
	default:
		return botSay("看不懂这个操作。")
	}

	before := subScopeSig(sub)
	now := time.Now()
	if err := applySubScope(sub, form, now); err != nil {
		return botSay("✗ " + err.Error())
	}
	if subScopeSig(sub) == before {
		return botSay("已经是「" + botSubScopeText(sub) + "」了。")
	}
	markSubRecheck(sub, now)
	if err := h.DB.Model(sub).Select("scope", "season", "ep_start", "ep_end", "specials", "follow", "follow_from", "state", "done_at", "next_check_at").
		Updates(sub).Error; err != nil {
		return botSay("✗ 保存失败：" + err.Error())
	}
	f.subs[f.subSel] = *sub

	msg := "✓ 已改为「" + botSubScopeText(sub) + "」"
	if sub.Scope == subScopeAll && f.seasons != nil {
		eps := 0
		for _, s := range f.seasons {
			eps += s.Eps
		}
		msg += fmt.Sprintf("，全剧 %d 季共 %d 集", len(f.seasons), eps)
		if sub.Specials && f.specialEps > 0 {
			msg += fmt.Sprintf("，另加特别篇 %d 集", f.specialEps)
		}
	}
	if sub.State == subStatePaused {
		msg += "。订阅暂停着，恢复后才会检查。"
	} else {
		msg += "，马上重新检查缺什么。"
	}
	f.stage, f.lead, f.confirmDel = "scope", []string{msg}, false
	return f.render()
}

func botHasSeason(ss []botSeason, n int) bool {
	for _, s := range ss {
		if s.N == n {
			return true
		}
	}
	return false
}

func botSeasonSpan(ss []botSeason) string {
	if len(ss) == 0 {
		return "TMDB 上还没有季"
	}
	if len(ss) == 1 {
		return fmt.Sprintf("只有第 %d 季", ss[0].N)
	}
	return fmt.Sprintf("共 %d 季，第 %d–%d 季", len(ss), ss[0].N, ss[len(ss)-1].N)
}

// botScopeMaxBtns 季按钮最多几个：再多就只写在文字里，回「第N季」照样能选
const botScopeMaxBtns = 32

// renderScope 改范围那屏
func (f *botFlow) renderScope() botView {
	s := &f.subs[f.subSel]
	v := botView{Token: f.token}
	if len(f.lead) > 0 {
		v.Lines = append(v.Lines, f.lead...)
		v.Lines = append(v.Lines, "")
	}
	head := "《" + s.Title + "》"
	if s.Year != "" {
		head += "（" + s.Year + "）"
	}
	v.Lines = append(v.Lines, "✏ 改范围 "+head, "当前："+botBold(botSubScopeText(s)))
	if f.seasons == nil {
		v.Lines = append(v.Lines, "读不到 TMDB 的季列表，可以直接回「第N季」。")
	} else if len(f.seasons) > 0 {
		parts := make([]string, 0, len(f.seasons)+1)
		for _, x := range f.seasons {
			parts = append(parts, fmt.Sprintf("第 %d 季 %d 集", x.N, x.Eps))
		}
		if f.specialEps > 0 {
			parts = append(parts, fmt.Sprintf("特别篇 %d 集", f.specialEps))
		}
		v.Lines = append(v.Lines, "TMDB："+strings.Join(parts, " · "))
	}

	mark := func(on bool, t string) string {
		if on {
			return "✓ " + t
		}
		return t
	}
	v.Buttons = append(v.Buttons, []botBtn{{Text: mark(s.Scope == subScopeAll, "📚 全剧"), Act: "sa"}})
	var row []botBtn
	for i, x := range f.seasons {
		if i >= botScopeMaxBtns {
			break
		}
		row = append(row, botBtn{Text: mark(s.Scope == subScopeSeason && s.Season == x.N, fmt.Sprintf("第 %d 季", x.N)), Act: "s" + strconv.Itoa(x.N)})
		if len(row) == 4 {
			v.Buttons, row = append(v.Buttons, row), nil
		}
	}
	if len(row) > 0 {
		v.Buttons = append(v.Buttons, row)
	}
	v.Buttons = append(v.Buttons, []botBtn{
		{Text: mark(s.Follow != subFollowNew, "补缺集"), Act: "fm"},
		{Text: mark(s.Follow == subFollowNew, "只追新集"), Act: "fn"},
	})
	hint := []string{"回复 全剧 / 第2季（或直接回季号）", "补缺 / 追新"}
	if s.Scope == subScopeAll && f.specialEps > 0 {
		t := "☐ 含特别篇"
		if s.Specials {
			t = "☑ 含特别篇"
		}
		v.Buttons = append(v.Buttons, []botBtn{{Text: t, Act: "sp"}})
		hint = append(hint, "特别篇（开 / 关）")
	}
	v.Buttons = append(v.Buttons, []botBtn{{Text: "↩ 订阅详情", Act: "b"}, {Text: "✓ 完成", Act: "q"}})
	v.Lines = append(v.Lines, "", "补缺集：范围内已播的缺哪集找哪集；只追新集：只要从现在起新播的。集段（E5–E10）请到网页改。")
	v.Hint = "（" + strings.Join(append(hint, "b 订阅详情", "q 完成"), "；") + "，10 分钟内有效）"
	return v
}

// renderCreated 订阅成功、没有按默认缩小范围的剧集：只给一个改范围的入口
func (f *botFlow) renderCreated() botView {
	v := botView{Token: f.token, Lines: append([]string{}, f.lead...)}
	v.Buttons = [][]botBtn{{{Text: "✏ 改范围", Act: "e"}, {Text: "✕ 关闭", Act: "q"}}}
	v.Hint = "（回复 e 改范围（全剧 / 换一季 / 只追新集）；q 关闭，10 分钟内有效）"
	return v
}
