package api

// ==================== 机器人找资源：TG 与企微共用的会话流程 ====================
//
// 原来 TG（tgbot_commands.go）和企微（wecombot_gy.go / wecombot_pansou.go）各写了一遍
// 「TMDB 选片 → 搜一个来源 → 回序号提交」，企微还分成观影、网盘两套会话。用起来的问题：
//   - 一次只能搜一个来源，TG 频道、不太灵在机器人上根本搜不到；
//   - 没有分页，只截前 10 / 20 条，后面的看不到；
//   - TG 每一步都新发一条消息，选几次屏幕就被刷满；
//   - 选完一条会话就结束，同一部剧想要两个季得从头再搜一遍。
//
// 参考 MoviePilot（app/chain/message.py：每页 8 条、p / n 翻页、0 = 自动择优、
// 按钮操作编辑原消息而不是新发）与 p115strmhelper（interactive/views.py：数字按钮成排、
// 上一页 / 下一页 / 返回 / 刷新 / 关闭，一个 /sh 同时搜全部来源），只读思路、没有照搬代码。
//
// 这里只管会话状态，渲染成与通道无关的 botView；发按钮还是写提示、编辑原消息还是新发，
// 由各通道自己决定（tgbot_commands.go / wecombot.go）。
//
// 约束：
//   - 提交一律走 botSubmitResource → submitResource，不另起提交通道；
//   - 同一条资源在一个会话里只提交一次（双击、按钮与回复数字重复），失败了才放开重试；
//   - 机器人不接 RE0（解锁花积分，要人在网页上确认）。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

const (
	botPageSize = 8 // MoviePilot 同款：一屏看得完，TG 按钮两排
	botFlowTTL  = 10 * time.Minute
	// botMaxItems 聚合搜索动辄一两百条，会话里只留排在前面的这些（翻页到底也够挑了）
	botMaxItems = 96
	// botAllWait 全部来源一起搜时最多等多久：超时的来源不等了，结果里写明
	botAllWait = 40 * time.Second
)

// botBtn 一个按钮；Act 与纯文本通道回复的内容同一套（序号 / n / p / 0 / b / r / k / q）
type botBtn struct {
	Text string
	Act  string
}

// botView 一屏内容。
//   - Refresh：只是把原来那屏更新一下（例如标上「已提交」），能原地编辑的通道就编辑，
//     不能的直接忽略，别为这个再发一整屏；
//   - Toast：一句提示（「已经是最后一页了」），另发一条，不能拿它把列表那屏覆盖掉。
type botView struct {
	Lines   []string
	Buttons [][]botBtn
	Token   string        // 按钮所属会话（TG 回调数据里带着）
	Hint    string        // 纯文本通道的操作提示（有按钮的通道不显示）
	Cards   []NewsArticle // 企微选片用的海报图文，其他通道忽略
	Refresh bool
	Toast   bool
}

func (v botView) text() string { return strings.Join(v.Lines, "\n") }

// botSay 一句提示，另发
func botSay(lines ...string) botView { return botView{Lines: lines, Toast: true} }

// botNote 替换当前这屏的文字（关闭、搜索失败）
func botNote(lines ...string) botView { return botView{Lines: lines} }

// botIO 通道给的两个出口：say 另发一条消息（提交进度与结果）；
// progress 更新「正在搜索」那一条（为 nil 就不报进度）
type botIO struct {
	say      func(...string)
	progress func(string)
}

func (io botIO) report(s string) {
	if io.progress != nil {
		io.progress(s)
	}
}

// botFlow 一个人的一次找资源会话。mu 在整个操作期间持有：
// 企微每条消息各起一个 goroutine，搜索要好几秒，期间再来的输入用 TryLock 挡回去
type botFlow struct {
	mu      sync.Mutex
	token   string // TG 按钮里带着它，旧按钮点了认得出来
	source  string // 来源 key；空 = 全部可用来源
	keyword string
	stage   string // media 选片 / resource 选资源
	movies  []wecomTmdbHit
	owned   map[string]bool
	media   wecomTmdbHit
	items   []ResourceItem
	note    string
	page    int
	sent    map[int]bool // 本会话里提交过的（items 下标）
	at      time.Time
}

var botFlows = struct {
	sync.Mutex
	m map[string]*botFlow
}{m: map[string]*botFlow{}}

func botFlowGet(key string) *botFlow {
	botFlows.Lock()
	defer botFlows.Unlock()
	f := botFlows.m[key]
	if f != nil && time.Since(f.at) > botFlowTTL {
		delete(botFlows.m, key)
		return nil
	}
	return f
}

func botFlowPut(key string, f *botFlow) {
	botFlows.Lock()
	defer botFlows.Unlock()
	for k, v := range botFlows.m {
		if time.Since(v.at) > botFlowTTL {
			delete(botFlows.m, k)
		}
	}
	botFlows.m[key] = f
}

// botFlowDrop 只删还是它自己的那一份：会话被新搜索替换后，旧会话收尾时别把新的删了
func botFlowDrop(key string, f *botFlow) bool {
	botFlows.Lock()
	defer botFlows.Unlock()
	cur := botFlows.m[key]
	if cur == nil || (f != nil && cur != f) {
		return false
	}
	delete(botFlows.m, key)
	return true
}

func botNewToken() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// botFlowAct 纯文本回复里哪些算会话操作（没有会话时不拦截，照常当指令处理）
func botFlowAct(text string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	switch t {
	case "n", "next", "下一页":
		return "n", true
	case "p", "prev", "上一页":
		return "p", true
	case "b", "back", "返回":
		return "b", true
	case "r", "刷新", "重搜":
		return "r", true
	case "k", "关键词":
		return "k", true
	case "q", "quit", "退出", "关闭":
		return "q", true
	case "0", "择优", "自动":
		return "0", true
	}
	if len(t) <= 3 && regexpPureDigits.MatchString(t) {
		return t, true
	}
	return "", false
}

func botSourceLabel(key string) string {
	if key == "" {
		return "全部来源"
	}
	if s := resSourceOf(key); s != nil {
		return s.Label
	}
	return key
}

// ==================== 入口 ====================

// botFind 开始一次找资源：TMDB 选片（只有一条就跳过）→ 搜资源。
// source 为空搜全部可用来源。新搜索顶掉同一个人之前的会话
func (h *Handler) botFind(key, source, keyword string, io botIO) botView {
	f := &botFlow{token: botNewToken(), source: source, keyword: keyword, at: time.Now()}
	botFlowPut(key, f)
	f.mu.Lock()
	defer f.mu.Unlock()

	io.report("正在查询 TMDB「" + keyword + "」…")
	movies, err := h.wecomTmdbMulti(keyword)
	switch {
	case err != nil || len(movies) == 0:
		// TMDB 不可用或查无此片：不让人空手而归，直接拿关键词搜（相关性判定跳过）。
		// p115strmhelper 的 /sh 本来就是按关键词直接搜的
		why := "TMDB 没有找到"
		if err != nil {
			why = "TMDB 查询失败（" + err.Error() + "）"
		}
		return h.botLoad(f, wecomTmdbHit{Title: keyword}, false, io, why+"，已按关键词直接搜")
	case len(movies) == 1:
		f.movies = movies
		return h.botLoad(f, movies[0], false, io, "")
	}
	f.movies, f.owned = movies, botOwned(h.DB, movies)
	f.stage, f.page = "media", 0
	return f.render()
}

// botAct 会话里的一次操作。ok=false 表示没有会话（调用方照常处理这句话）。
// token 非空时必须是当前会话的（TG 按钮）；纯文本回复传空
func (h *Handler) botAct(key, act, token string, io botIO) (botView, bool) {
	f := botFlowGet(key)
	if f == nil {
		if token != "" {
			return botSay("这组按钮已失效（超过 10 分钟或已关闭），请重新搜索。"), true
		}
		return botView{}, false
	}
	if token != "" && token != f.token {
		return botSay("这是旧的按钮，请使用最新的结果。"), true
	}
	if !f.mu.TryLock() {
		return botSay("上一步还在进行，请稍候。"), true
	}
	defer f.mu.Unlock()
	f.at = time.Now()

	switch act {
	case "q":
		botFlowDrop(key, f)
		return botNote("已关闭。已提交的任务继续执行。"), true
	case "n", "p":
		pages := f.pages()
		if act == "n" && f.page+1 >= pages {
			return botSay("已经是最后一页了。"), true
		}
		if act == "p" && f.page == 0 {
			return botSay("已经是第一页了。"), true
		}
		if act == "n" {
			f.page++
		} else {
			f.page--
		}
		return f.render(), true
	case "b":
		if f.stage != "resource" || len(f.movies) < 2 {
			return botSay("没有可返回的选片列表。"), true
		}
		f.stage, f.page, f.items, f.note, f.sent = "media", 0, nil, "", nil
		return f.render(), true
	case "k":
		if f.stage != "media" {
			return botSay("已经在资源列表里了。"), true
		}
		return h.botLoad(f, wecomTmdbHit{Title: f.keyword}, false, io, "不选片，按关键词直接搜"), true
	case "r":
		if f.stage != "resource" {
			return botSay("选好影片后才能刷新资源。"), true
		}
		return h.botLoad(f, f.media, true, io, ""), true
	case "0":
		if f.stage != "resource" {
			return botSay("先选影片，资源列表里才能自动择优。"), true
		}
		i := f.bestIndex()
		if i < 0 {
			return botSay("没有能自动提交的资源：片名对得上、能直接转存或离线、之前没提交过的一条都没有，请手动挑。"), true
		}
		io.say(fmt.Sprintf("⭐ 自动择优：第 %d 条\n%s", i+1, resBotLabel(f.items[i], 80, f.source == "")))
		return h.botSubmitAt(f, i, io), true
	}

	n, err := strconv.Atoi(act)
	if err != nil {
		return botSay("看不懂这个操作。"), true
	}
	switch f.stage {
	case "media":
		if n < 1 || n > len(f.movies) {
			return botSay(fmt.Sprintf("请选择 1-%d。", len(f.movies))), true
		}
		return h.botLoad(f, f.movies[n-1], false, io, ""), true
	case "resource":
		if n < 1 || n > len(f.items) {
			return botSay(fmt.Sprintf("请选择 1-%d。", len(f.items))), true
		}
		return h.botSubmitAt(f, n-1, io), true
	}
	return botSay("看不懂这个操作。"), true
}

// botLoad 选定影片后搜资源，进入资源列表
func (h *Handler) botLoad(f *botFlow, m wecomTmdbHit, refresh bool, io botIO, lead string) botView {
	var (
		items []ResourceItem
		note  string
		err   error
	)
	if f.source == "" {
		items, note = h.botResourcesAll(m, refresh, io)
	} else {
		io.report("正在搜索「" + m.Title + "」的" + botSourceLabel(f.source) + "资源…")
		items, note, err = h.botResources(f.source, m, refresh)
		if err != nil {
			v := botNote(botSourceLabel(f.source) + "搜索失败：" + err.Error())
			if f.stage == "media" {
				// 选片列表还在，换一部再试
				mv := f.render()
				v.Lines = append(v.Lines, "", mv.text())
				v.Buttons, v.Hint = mv.Buttons, mv.Hint
			}
			return v
		}
	}
	if lead != "" {
		if note != "" {
			note = lead + "；" + note
		} else {
			note = lead
		}
	}
	f.stage, f.media, f.items, f.note, f.page, f.sent = "resource", m, items, note, 0, map[int]bool{}
	return f.render()
}

// botSubmitAt 提交第 i 条。先记账再提交（双击 / 按钮加回复数字不会提交两次），失败了放开
func (h *Handler) botSubmitAt(f *botFlow, i int, io botIO) botView {
	if f.sent[i] {
		return botSay(fmt.Sprintf("第 %d 条刚才已经提交过了，不重复提交。", i+1))
	}
	if f.sent == nil {
		f.sent = map[int]bool{}
	}
	f.sent[i] = true
	if !botSubmit(h, f.items[i], io.say) {
		delete(f.sent, i)
		return botView{}
	}
	// 列表留着：同一部剧常常要挑好几季。能原地更新的通道把这条标成「刚提交」
	v := f.render()
	v.Refresh = true
	return v
}

// botSubmit 测试里替换掉，免得真的去提交
var botSubmit = func(h *Handler, it ResourceItem, say func(...string)) bool {
	return h.botSubmitResource(it, say)
}

// bestIndex 自动择优挑哪一条：列表已按默认顺序排好（相关 → 能直接处理 → 洗版偏好 → 分辨率 → 做种 → 时间），
// 取第一条片名对得上、能直接转存或离线、之前没提交过的
func (f *botFlow) bestIndex() int {
	for i, it := range f.items {
		if !it.Relevant || (it.Action != "transfer" && it.Action != "offline") {
			continue
		}
		if it.SubmittedAt > 0 || f.sent[i] {
			continue
		}
		return i
	}
	return -1
}

// ==================== 渲染 ====================

func (f *botFlow) total() int {
	if f.stage == "media" {
		return len(f.movies)
	}
	return len(f.items)
}

func (f *botFlow) pages() int {
	n := (f.total() + botPageSize - 1) / botPageSize
	if n < 1 {
		n = 1
	}
	return n
}

func botTypeName(kind string) string {
	switch kind {
	case "tv":
		return "剧集"
	case "movie":
		return "电影"
	}
	return ""
}

func (f *botFlow) render() botView {
	if f.page >= f.pages() {
		f.page = f.pages() - 1
	}
	start := f.page * botPageSize
	end := start + botPageSize
	if end > f.total() {
		end = f.total()
	}
	v := botView{Token: f.token}
	var nums []botBtn
	var hint []string
	pageNote := ""
	if f.pages() > 1 {
		pageNote = fmt.Sprintf("，第 %d/%d 页", f.page+1, f.pages())
	}

	if f.stage == "media" {
		v.Lines = append(v.Lines, fmt.Sprintf("「%s」TMDB 找到 %d 部%s，选一部：", f.keyword, len(f.movies), pageNote))
		for i := start; i < end; i++ {
			m := f.movies[i]
			line := fmt.Sprintf("%d. %s", i+1, m.Title)
			if m.Year != "" {
				line += "（" + m.Year + "）"
			}
			if t := botTypeName(m.Type); t != "" {
				line += " " + t
			}
			if m.Vote > 0 {
				line += fmt.Sprintf(" %.1f分", m.Vote)
			}
			owned := f.owned[fmt.Sprintf("%s:%d", m.Type, m.ID)]
			if owned {
				line += " ✓已入库"
			}
			v.Lines = append(v.Lines, line)
			nums = append(nums, botBtn{Text: strconv.Itoa(i + 1), Act: strconv.Itoa(i + 1)})

			a := NewsArticle{Title: fmt.Sprintf("%d. %s (%s)", i+1, m.Title, m.Year), Desc: botTypeName(m.Type) + voteSuffix(m.Vote),
				Link: fmt.Sprintf("https://www.themoviedb.org/%s/%d", m.Type, m.ID)}
			if owned {
				a.Title += " ✓已入库"
			}
			if m.Poster != "" {
				a.PicURL = tmdbImageBase() + "/t/p/w300" + m.Poster
			}
			v.Cards = append(v.Cards, a)
		}
		hint = append(hint, fmt.Sprintf("回复 1-%d 选片", len(f.movies)))
	} else {
		head := "「" + f.media.Title + "」"
		if f.media.Year != "" {
			head = "「" + f.media.Title + "」（" + f.media.Year + "）"
		}
		if f.media.ID == 0 {
			head = "关键词「" + f.media.Title + "」"
		}
		if len(f.items) == 0 {
			v.Lines = append(v.Lines, head+"：没有找到资源（"+botSourceLabel(f.source)+"）")
		} else {
			v.Lines = append(v.Lines, fmt.Sprintf("%s%s资源 %d 条%s：", head, botSourceLabel(f.source), len(f.items), pageNote))
		}
		if f.note != "" {
			v.Lines = append(v.Lines, "（"+f.note+"）")
		}
		for i := start; i < end; i++ {
			line := fmt.Sprintf("%d. %s", i+1, resBotLabel(f.items[i], 60, f.source == ""))
			if f.sent[i] {
				line = "✓ " + line + " ← 已提交"
			}
			v.Lines = append(v.Lines, line)
			nums = append(nums, botBtn{Text: strconv.Itoa(i + 1), Act: strconv.Itoa(i + 1)})
		}
		if len(f.items) > 0 {
			hint = append(hint, fmt.Sprintf("回复 1-%d 提交（可以挑多条）", len(f.items)), "0 自动择优")
		}
	}

	// 数字按钮 4 个一排（p115strmhelper 同款），下面是翻页与导航
	for i := 0; i < len(nums); i += 4 {
		j := i + 4
		if j > len(nums) {
			j = len(nums)
		}
		v.Buttons = append(v.Buttons, nums[i:j])
	}
	if f.stage == "resource" && len(f.items) > 0 {
		v.Buttons = append(v.Buttons, []botBtn{{Text: "⭐ 自动择优", Act: "0"}})
	}
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
	var tail []botBtn
	if f.stage == "media" {
		tail = append(tail, botBtn{Text: "🔎 不选片，按关键词搜", Act: "k"})
		hint = append(hint, "k 不选片直接按关键词搜")
	} else {
		if len(f.movies) > 1 {
			tail = append(tail, botBtn{Text: "↩ 重新选片", Act: "b"})
			hint = append(hint, "b 重新选片")
		}
		tail = append(tail, botBtn{Text: "🔄 重搜", Act: "r"})
		hint = append(hint, "r 重搜")
	}
	tail = append(tail, botBtn{Text: "✕ 关闭", Act: "q"})
	hint = append(hint, "q 退出")
	v.Buttons = append(v.Buttons, tail)
	v.Hint = "（" + strings.Join(hint, "；") + "，10 分钟内有效）"
	return v
}

// botOwned 选片列表上标出已经整理进库的（只查本地台账，和网页「影视转存」的已入库角标同口径）
func botOwned(db *gorm.DB, movies []wecomTmdbHit) map[string]bool {
	out := map[string]bool{}
	if db == nil {
		return out
	}
	ids := map[string][]int{}
	for _, m := range movies {
		if m.ID > 0 && (m.Type == "movie" || m.Type == "tv") {
			ids[m.Type] = append(ids[m.Type], m.ID)
		}
	}
	for kind, list := range ids {
		var rows []model.MediaLibrary
		db.Select("tmdb_id").Where("media_type = ? AND tmdb_id IN ?", kind, list).Find(&rows)
		for _, r := range rows {
			out[fmt.Sprintf("%s:%d", kind, r.TmdbID)] = true
		}
	}
	return out
}

// ==================== 搜索 ====================

// botResources 机器人按片搜一个来源，过滤后的结果见 botFilter
func (h *Handler) botResources(key string, hit wecomTmdbHit, refresh bool) ([]ResourceItem, string, error) {
	r, err := searchResources(h, key, resQueryOfHit(hit), refresh)
	if err != nil {
		return nil, "", err
	}
	items, note := botFilter(r.Items, hit.Title)
	if r.Note != "" {
		note = joinNote(r.Note, note)
	}
	return items, note, nil
}

func resQueryOfHit(hit wecomTmdbHit) resQuery {
	return resQuery{TmdbID: hit.ID, Type: hit.Type, Title: hit.Title, Year: hit.Year}
}

// botResourcesAll 全部可用来源一起搜（网页上是每个来源各发一次请求，这里在服务端并发），
// 合并去重后按默认顺序排。慢的来源等到 botAllWait 就不等了，结果里写明谁没回来
func (h *Handler) botResourcesAll(hit wecomTmdbHit, refresh bool, io botIO) ([]ResourceItem, string) {
	cfg := loadTransferHubCfg()
	var srcs []*resSource
	var skipped []string
	for i := range resSources {
		s := &resSources[i]
		if s.Key == "re0" || !cfg.enabled(s.Key) {
			continue
		}
		if why := s.ready(); why != "" {
			skipped = append(skipped, s.Label+"："+why)
			continue
		}
		srcs = append(srcs, s)
	}
	if len(srcs) == 0 {
		return nil, joinNote("没有可用的来源", strings.Join(skipped, "；"))
	}

	type result struct {
		key   string
		items []ResourceItem
		err   error
	}
	ch := make(chan result, len(srcs)) // 带缓冲：超时后不再有人收，晚到的 goroutine 也不会卡住
	q := resQueryOfHit(hit)
	for _, s := range srcs {
		go func(key string) {
			r, err := searchResources(h, key, q, refresh)
			ch <- result{key: key, items: r.Items, err: err}
		}(s.Key)
	}

	state := map[string]string{}
	progress := func() {
		var parts []string
		for _, s := range srcs {
			st := state[s.Key]
			if st == "" {
				st = "…"
			}
			parts = append(parts, s.Label+" "+st)
		}
		io.report("正在搜索「" + hit.Title + "」：" + strings.Join(parts, " · "))
	}
	progress()

	var all []ResourceItem
	var failed []string
	timeout := time.NewTimer(botAllWait)
	defer timeout.Stop()
	for done := 0; done < len(srcs); done++ {
		select {
		case r := <-ch:
			if r.err != nil {
				state[r.key] = "✗"
				failed = append(failed, botSourceLabel(r.key)+"失败")
			} else {
				state[r.key] = fmt.Sprintf("✓%d", len(r.items))
				all = append(all, r.items...)
			}
			progress()
		case <-timeout.C:
			for _, s := range srcs {
				if state[s.Key] == "" {
					failed = append(failed, s.Label+"超时")
				}
			}
			done = len(srcs)
		}
	}

	all = resDedupe(all)
	resSortDefault(all)
	items, note := botFilter(all, hit.Title)
	return items, joinNote(note, strings.Join(append(failed, skipped...), "；"))
}

// resDedupe 同一条链接在好几个来源里都有（盘搜和 TG 频道常见），只留排在前面的那条
func resDedupe(items []ResourceItem) []ResourceItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		k := ""
		if it.URL != "" {
			if k = linkHashOf(it.URL); k == "" {
				k = it.URL
			}
		}
		if k != "" {
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		out = append(out, it)
	}
	return out
}

// botFilter 只留片名对得上的；一条都对不上时退回全部并说明，别让人空手而归
func botFilter(items []ResourceItem, title string) ([]ResourceItem, string) {
	var relevant []ResourceItem
	for _, it := range items {
		if it.Relevant {
			relevant = append(relevant, it)
		}
	}
	note := ""
	switch {
	case len(items) == 0:
	case len(relevant) == 0:
		note = fmt.Sprintf("%d 条结果的片名都对不上「%s」，以下仅供参考", len(items), title)
		relevant = items
	case len(relevant) < len(items):
		note = fmt.Sprintf("已隐藏 %d 条片名对不上的", len(items)-len(relevant))
	}
	if len(relevant) > botMaxItems {
		note = joinNote(note, fmt.Sprintf("共 %d 条，只列前 %d 条", len(relevant), botMaxItems))
		relevant = relevant[:botMaxItems]
	}
	return relevant, note
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "；" + b
}

// ==================== TMDB ====================

type wecomTmdbHit struct {
	ID     int
	Type   string // movie / tv；按关键词直接搜时为空
	Title  string
	Year   string
	Vote   float64
	Poster string // TMDB poster_path（/xx.jpg）
}

func voteSuffix(v float64) string {
	if v > 0 {
		return fmt.Sprintf(" · %.1f分", v)
	}
	return ""
}
