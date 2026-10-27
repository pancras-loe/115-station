package api

// ==================== PT 站签到：调度与执行 ====================
//
// 2026-10-26，设计见 docs/115-station-notes/PT-SIGNIN-PLAN.md。
//
// - 每天在时间窗里随机挑一个时刻（挑好存进 setting，重启不变），或固定 cron。
//   随机是 MP「站点自动签到」的做法：所有人整点一起打站点，站点那边一眼就看得出是脚本。
// - 一个站接一个跑，站与站之间随机隔几秒；今天已经做完（签成或确认登录）的站跳过。
//   每个站每天一般 1–2 个请求。
// - 只有网络错误、站点 5xx 在 30–60 分钟后再试一次（内存里记着，重启就不补了，第二天照常）；
//   Cookie / Token 失效、被 Cloudflare 拦下再试也一样，不试，给站点打 NeedFix。
// - 不发 115 请求，不进任务队列、不拿 taskMu。
// - 每轮一条汇总通知：失败 → 只确认登录 → 成功，同样原因合并成一行。

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"115-station/internal/model"
)

const ptSettingKey = "ptsignin"

// ptSigninCfg setting "ptsignin"：只有 PT 签到这一页改它，整份存取
type ptSigninCfg struct {
	Enabled    bool   `json:"enabled"`
	Mode       string `json:"mode"` // window 每天随机 / cron 固定
	WindowFrom string `json:"window_from"`
	WindowTo   string `json:"window_to"`
	Cron       string `json:"cron"`
	GapMin     int    `json:"gap_min"` // 站与站之间随机间隔（秒）
	GapMax     int    `json:"gap_max"`
	Retry      bool   `json:"retry"`
	Notify     string `json:"notify"` // always / fail_only / off

	// 以下由调度器写
	Today   string `json:"today"`    // 挑时刻的那一天
	TodayAt string `json:"today_at"` // 那天挑好的时刻 HH:MM；空 = 当天不跑（开启时窗口已经过了）
	LastDay string `json:"last_day"` // 定时最近跑过的那一天（随机模式用）
	LastRun string `json:"last_run"` // 定时最近触发的分钟（cron 模式防同一分钟跑两次）
	// 最近一轮（记录页的「最近一轮」按时间段从日志里取）
	LastStart   string `json:"last_start"`
	LastEnd     string `json:"last_end"`
	LastTrigger string `json:"last_trigger"`
}

const (
	ptModeWindow = "window"
	ptModeCron   = "cron"
)

func defaultPtSigninCfg() ptSigninCfg {
	return ptSigninCfg{Mode: ptModeWindow, WindowFrom: "08:00", WindowTo: "10:00", Cron: "30 8 * * *",
		GapMin: 3, GapMax: 8, Retry: true, Notify: "always"}
}

var ptCfgMu sync.Mutex

func loadPtSigninCfg() ptSigninCfg {
	ptCfgMu.Lock()
	defer ptCfgMu.Unlock()
	return readPtSigninCfgLocked()
}

func readPtSigninCfgLocked() ptSigninCfg {
	cfg := defaultPtSigninCfg()
	if v := settingValueCompat(ptSettingKey); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg.normalized()
}

func (c ptSigninCfg) normalized() ptSigninCfg {
	d := defaultPtSigninCfg()
	if c.Mode != ptModeCron {
		c.Mode = ptModeWindow
	}
	if _, ok := ptParseHM(c.WindowFrom); !ok {
		c.WindowFrom = d.WindowFrom
	}
	if _, ok := ptParseHM(c.WindowTo); !ok {
		c.WindowTo = d.WindowTo
	}
	if strings.TrimSpace(c.Cron) == "" {
		c.Cron = d.Cron
	}
	if c.GapMin < 1 {
		c.GapMin = 1
	}
	if c.GapMax < c.GapMin {
		c.GapMax = c.GapMin
	}
	if c.GapMax > 120 {
		c.GapMax = 120
	}
	switch c.Notify {
	case "always", "fail_only", "off":
	default:
		c.Notify = d.Notify
	}
	return c
}

// updatePtSigninCfg 读-改-写：调度器写时刻与界面保存撞上时不丢字段
func updatePtSigninCfg(fn func(*ptSigninCfg)) error {
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	ptCfgMu.Lock()
	defer ptCfgMu.Unlock()
	cfg := readPtSigninCfgLocked()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	return notifyConfigSource.SaveSetting(ptSettingKey, string(b))
}

// ptParseHM "08:30" → 当天第几分钟
func ptParseHM(s string) (int, bool) {
	var h, m int
	if n, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func ptHM(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

// ptPickTime 给 now 这一天挑执行时刻（纯函数）：窗口 [from, to) 里随机一分钟，
// 已经过了的部分不挑；窗口整个过了返回 ""（今天不跑）。to <= from 按跨不过零点处理成 from 后一小时
func ptPickTime(c ptSigninCfg, now time.Time, rnd *rand.Rand) string {
	from, _ := ptParseHM(c.WindowFrom)
	to, _ := ptParseHM(c.WindowTo)
	if to <= from {
		to = from + 60
		if to > 24*60 {
			to = 24 * 60
		}
	}
	cur := now.Hour()*60 + now.Minute() + 1
	if cur > from {
		from = cur
	}
	if from >= to {
		return ""
	}
	return ptHM(from + rnd.Intn(to-from))
}

// ptDue 随机模式下这一分钟该不该跑（纯函数）：当天挑好了、到点了、今天还没跑过
func ptDue(c ptSigninCfg, now time.Time) bool {
	day := now.Format("2006-01-02")
	if !c.Enabled || c.Mode != ptModeWindow || c.Today != day || c.TodayAt == "" || c.LastDay == day {
		return false
	}
	at, ok := ptParseHM(c.TodayAt)
	return ok && now.Hour()*60+now.Minute() >= at
}

// ==================== 执行 ====================

var (
	ptRunMu   sync.Mutex  // 整轮串行；手动单站也排在它上面
	ptRunning atomic.Bool // 界面看「正在跑」
	ptRetry   struct {
		sync.Mutex
		at  time.Time
		day string
		ids []uint
	}
	// ptSleep 站与站之间的等待；返回 false = 要停服了。测试里换掉
	ptSleep = func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-stopCh:
			return false
		}
	}
	ptNow = time.Now
)

// ptRoundItem 一轮里一个站的结果
type ptRoundItem struct {
	SiteID uint   `json:"site_id"`
	Name   string `json:"name"`
	ptResult
	At      time.Time `json:"at"`
	Skipped bool      `json:"-"` // 今天已经做完，没请求
}

// ptSigninOne 签一个站：签到；签到页认不出（或接口 404 之类）时再访问首页确认登录（MP signin_site）。
// 只保号的站只确认登录
func ptSigninOne(s *model.PtSite) ptResult {
	pc := newPtClient(s)
	if s.Mode == ptModeKeep {
		return ptCheckLogin(pc)
	}
	r := ptSignin(pc)
	if r.done() || (r.Reason != ptWhyUnknown && r.Reason != ptWhyHTTP) || ptHandlerOf(s) == ptHandRousi {
		return r
	}
	plogPtSignin.Printf("○ %s：签到没认出（%s），改查首页确认登录", s.Name, r.Message)
	l := ptCheckLogin(pc)
	if l.done() {
		return ptLogin("签到没确认（" + r.Message + "），首页确认已登录")
	}
	return ptFail(r.Reason, ptMergeReasons(r.Message, l.Message))
}

// ptMergeReasons 两个原因一样就只说一次（MP merge_failure_messages）
func ptMergeReasons(a, b string) string {
	if a == b || b == "" {
		return a
	}
	if a == "" {
		return b
	}
	return a + "；" + b
}

// ptRecord 记一次结果，顺手设 / 清 NeedFix
func ptRecord(db *gorm.DB, s *model.PtSite, r ptResult, trigger string, at time.Time) {
	msg := r.Message
	if len([]rune(msg)) > 250 {
		msg = string([]rune(msg)[:250])
	}
	if err := db.Create(&model.PtSigninLog{SiteID: s.ID, Day: at.Format("2006-01-02"), At: at, Trigger: trigger,
		Result: r.Result, Reason: r.Reason, Message: msg}).Error; err != nil {
		plogPtSignin.Printf("○ 结果落库失败（%s）: %v", s.Name, err)
	}
	need := r.Reason == ptWhyCookie || r.Reason == ptWhyToken
	if need != s.NeedFix {
		s.NeedFix = need
		db.Model(&model.PtSite{}).Where("id = ?", s.ID).Update("need_fix", need)
	}
}

// ptDoneToday 今天已经做完的站（签成或确认登录过）
func ptDoneToday(db *gorm.DB, day string) map[uint]bool {
	var ids []uint
	db.Model(&model.PtSigninLog{}).Where("day = ? AND result IN ?", day, []string{ptResOK, ptResLogin}).Distinct().Pluck("site_id", &ids)
	out := map[uint]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// ptPruneLogs 只留含当天 14 天
func ptPruneLogs(db *gorm.DB, now time.Time) {
	cut := now.AddDate(0, 0, -13).Format("2006-01-02")
	db.Where("day < ?", cut).Delete(&model.PtSigninLog{})
}

// ptRunRound 跑一轮。ids 空 = 全部启用的站，今天做完的跳过；
// 点名的站不管今天做没做完都跑（手动单站、重试）
func (h *Handler) ptRunRound(trigger string, ids []uint) ([]ptRoundItem, error) {
	ptRunMu.Lock()
	defer ptRunMu.Unlock()
	ptRunning.Store(true)
	defer ptRunning.Store(false)

	cfg := loadPtSigninCfg()
	now := ptNow()
	ptPruneLogs(h.DB, now)
	var sites []model.PtSite
	q := h.DB.Order("id")
	if len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	} else {
		q = q.Where("enabled = ?", true)
	}
	if err := q.Find(&sites).Error; err != nil {
		return nil, err
	}
	sites = ptRunnable(sites)
	if len(sites) == 0 {
		return nil, nil
	}
	done := map[uint]bool{}
	if len(ids) == 0 {
		done = ptDoneToday(h.DB, now.Format("2006-01-02"))
	}
	plogPtSignin.Printf("▶ 开始签到（%s）：%d 个站，今天已做完、跳过 %d 个", ptTriggerLabel(trigger), len(sites), ptCountDone(sites, done))
	start := ptNow()
	var items []ptRoundItem
	requested := 0
	for i := range sites {
		s := &sites[i]
		if done[s.ID] {
			items = append(items, ptRoundItem{SiteID: s.ID, Name: s.Name, Skipped: true})
			continue
		}
		if requested > 0 {
			gap := cfg.GapMin + rand.Intn(cfg.GapMax-cfg.GapMin+1)
			if !ptSleep(time.Duration(gap) * time.Second) {
				break
			}
		}
		requested++
		r := ptSigninOne(s)
		at := ptNow()
		ptRecord(h.DB, s, r, trigger, at)
		if r.done() {
			plogPtSignin.Printf("✓ %s：%s", s.Name, r.Message)
		} else {
			plogPtSignin.Printf("✗ %s：%s", s.Name, r.Message)
		}
		items = append(items, ptRoundItem{SiteID: s.ID, Name: s.Name, ptResult: r, At: at})
	}
	if requested > 0 {
		_ = updatePtSigninCfg(func(c *ptSigninCfg) {
			c.LastStart, c.LastEnd, c.LastTrigger = start.Format(time.RFC3339), ptNow().Format(time.RFC3339), trigger
		})
	}
	return items, nil
}

// ptTriggerLabel 日志里写触发来源
func ptTriggerLabel(trigger string) string {
	switch trigger {
	case "cron":
		return "定时"
	case "retry":
		return "失败重试"
	case "manual":
		return "手动"
	case "bot":
		return "机器人"
	}
	return trigger
}

func ptCountDone(sites []model.PtSite, done map[uint]bool) int {
	n := 0
	for _, s := range sites {
		if done[s.ID] {
			n++
		}
	}
	return n
}

// ptRunnable 去掉暂不支持的站（馒头 / 朱雀 / 影巢：库里本不该有，老数据或手填的地址也拦一下）
func ptRunnable(sites []model.PtSite) []model.PtSite {
	out := sites[:0]
	for _, s := range sites {
		if h := ptHandlerOf(&s); h == ptHandKeep || h == ptHandRe0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ptScheduleRetry 这一轮里网络错误 / 5xx 的站，30–60 分钟后再试一次；同一天只试一次
func ptScheduleRetry(items []ptRoundItem, trigger string, now time.Time) {
	if trigger == "retry" {
		return
	}
	var ids []uint
	for _, it := range items {
		if !it.Skipped && it.retryable() {
			ids = append(ids, it.SiteID)
		}
	}
	ptRetry.Lock()
	defer ptRetry.Unlock()
	day := now.Format("2006-01-02")
	if len(ids) == 0 || ptRetry.day == day {
		return
	}
	ptRetry.day, ptRetry.ids = day, ids
	ptRetry.at = now.Add(time.Duration(30+rand.Intn(31)) * time.Minute)
}

// ptTakeRetry 到点的重试（取走）
func ptTakeRetry(now time.Time) []uint {
	ptRetry.Lock()
	defer ptRetry.Unlock()
	if len(ptRetry.ids) == 0 || now.Before(ptRetry.at) {
		return nil
	}
	ids := ptRetry.ids
	ptRetry.ids = nil
	return ids
}

// ==================== 汇总 ====================

// ptSummary 一轮的汇总（纯函数）：失败 → 只确认登录 → 成功，失败里同样原因合并成一行、Cookie / Token 失效排最前
func ptSummary(items []ptRoundItem, day time.Time) (title, body string) {
	var ok, login []string
	type grp struct {
		why, msg string
		names    []string
	}
	var fails []*grp
	byMsg := map[string]*grp{}
	skipped := 0
	for _, it := range items {
		if it.Skipped {
			skipped++
			continue
		}
		switch it.Result {
		case ptResOK:
			ok = append(ok, it.Name)
		case ptResLogin:
			login = append(login, it.Name)
		default:
			g := byMsg[it.Message]
			if g == nil {
				g = &grp{why: it.Reason, msg: it.Message}
				byMsg[it.Message] = g
				fails = append(fails, g)
			}
			g.names = append(g.names, it.Name)
		}
	}
	rank := func(w string) int {
		if w == ptWhyCookie || w == ptWhyToken {
			return 0
		}
		return 1
	}
	sort.SliceStable(fails, func(i, j int) bool { return rank(fails[i].why) < rank(fails[j].why) })
	nFail := 0
	for _, g := range fails {
		nFail += len(g.names)
	}
	title = "PT 站签到"
	if nFail > 0 {
		title = "PT 站签到 · " + fmt.Sprint(nFail) + " 个失败"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n成功 %d · 只确认登录 %d · 失败 %d", day.Format("1月2日"), len(ok), len(login), nFail)
	if skipped > 0 {
		fmt.Fprintf(&b, "（%d 个今天已做完，没再请求）", skipped)
	}
	if len(fails) > 0 {
		b.WriteString("\n\n✗ 失败")
		for _, g := range fails {
			fmt.Fprintf(&b, "\n%s\n  %s", strings.Join(g.names, "、"), g.msg)
		}
	}
	if len(login) > 0 {
		b.WriteString("\n\n○ 只确认登录（保号有效，签到没确认）\n" + strings.Join(login, "、"))
	}
	if len(ok) > 0 {
		b.WriteString("\n\n✓ 签到成功\n" + strings.Join(ok, "、"))
	}
	return title, b.String()
}

// ptNotifyRound 按设置推这一轮的汇总；一个站都没请求（全跳过）不推
func ptNotifyRound(items []ptRoundItem, now time.Time) {
	cfg := loadPtSigninCfg()
	requested, failed := 0, false
	for _, it := range items {
		if !it.Skipped {
			requested++
			failed = failed || !it.done()
		}
	}
	if requested == 0 || cfg.Notify == "off" || (cfg.Notify == "fail_only" && !failed) {
		return
	}
	title, body := ptSummary(items, now)
	NotifyMessage(kindCheckin, title, body)
}

// ptRoundAsync 后台跑一轮（定时、界面「全部」、机器人），跑完推通知、排重试。
// reply 非空时把汇总也回给它（机器人）
func (h *Handler) ptRoundAsync(trigger string, ids []uint, reply func(...string)) {
	go func() {
		items, err := h.ptRunRound(trigger, ids)
		if err != nil {
			plogPtSignin.Printf("✗ 读站点失败: %v", err)
			if reply != nil {
				reply("✗ PT 签到失败：" + err.Error())
			}
			return
		}
		now := ptNow()
		if loadPtSigninCfg().Retry {
			ptScheduleRetry(items, trigger, now)
		}
		if reply != nil {
			title, body := ptSummary(items, now)
			reply(title, body)
			return
		}
		ptNotifyRound(items, now)
	}()
}

// ==================== 调度器 ====================

// StartPtSigninScheduler 每分钟看一次：随机模式先给当天挑时刻、到点跑；cron 模式命中就跑；到点的重试也在这里
func StartPtSigninScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.ptSigninTick(ptNow())
		}
	}()
}

func (h *Handler) ptSigninTick(now time.Time) {
	if ptRunning.Load() {
		return
	}
	cfg := loadPtSigninCfg()
	if !cfg.Enabled {
		return
	}
	if ids := ptTakeRetry(now); len(ids) > 0 {
		h.ptRoundAsync("retry", ids, nil)
		return
	}
	day := now.Format("2006-01-02")
	switch cfg.Mode {
	case ptModeCron:
		minute := now.Format("2006-01-02 15:04")
		if !CronMatch(cfg.Cron, now) || cfg.LastRun == minute {
			return
		}
		_ = updatePtSigninCfg(func(c *ptSigninCfg) { c.LastRun = minute })
		h.ptRoundAsync("cron", nil, nil)
	default:
		if cfg.Today != day {
			at := ptPickTime(cfg, now, rand.New(rand.NewSource(now.UnixNano())))
			_ = updatePtSigninCfg(func(c *ptSigninCfg) { c.Today, c.TodayAt = day, at })
			cfg.Today, cfg.TodayAt = day, at
			if at != "" {
				plogPtSignin.Printf("○ 今天 %s 签到", at)
			}
		}
		if !ptDue(cfg, now) {
			return
		}
		_ = updatePtSigninCfg(func(c *ptSigninCfg) { c.LastDay = day })
		h.ptRoundAsync("cron", nil, nil)
	}
}
