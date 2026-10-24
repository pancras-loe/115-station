package api

// 订阅执行器：逐个订阅「结算 → 盘点 → 搜资源 → 挑 → 提交 → 排下次」。
//
// 走自己的队列（subLane），不拿 taskMu：搜索最慢要等几十秒，没理由占着主队列；
// 转存本来就不在 taskMu 下（找资源页提交也不拿）。提交进转存目录后照旧由守望者接管整理。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

const jobKindSubscribe = "subscribe"

func init() { jobExecutors[jobKindSubscribe] = execSubscribeJob }

// subJobParams 订阅任务的参数
type subJobParams struct {
	IDs    []uint `json:"ids"`
	Manual bool   `json:"manual,omitempty"` // 手动「立即搜索」：不看下次检查时间，暂停的也查
}

// subSearchWait 所有来源并发搜，最多等多久（盘搜慢）
const subSearchWait = 60 * time.Second

// enqueueSubscribeJob 入队。定时的合成一个任务（排着的取并集），手动的一个订阅一个
func enqueueSubscribeJob(h *Handler, ids []uint, manual bool, title, source string) (model.TaskJob, error) {
	spec := jobSpec{
		Kind: jobKindSubscribe, Title: "订阅检查", DedupeKey: "sub:auto",
		Source: source, Priority: jobPriorityBackground,
		Params: jobParams{Subs: &subJobParams{IDs: ids, Manual: manual}},
		Merge: func(prev, next jobParams) (jobParams, string) {
			seen := map[uint]bool{}
			var all []uint
			for _, p := range []jobParams{prev, next} {
				if p.Subs == nil {
					continue
				}
				for _, id := range p.Subs.IDs {
					if !seen[id] {
						seen[id] = true
						all = append(all, id)
					}
				}
			}
			return jobParams{Subs: &subJobParams{IDs: all, Manual: next.Subs != nil && next.Subs.Manual}},
				fmt.Sprintf("订阅检查（%d 个）", len(all))
		},
	}
	if manual {
		spec.Title, spec.Priority = "订阅搜索："+title, jobPriorityManual
		if len(ids) == 1 {
			spec.DedupeKey = fmt.Sprintf("sub:%d", ids[0])
		}
		spec.Merge = nil
	} else if len(ids) > 0 {
		spec.Title = fmt.Sprintf("订阅检查（%d 个）", len(ids))
	}
	return enqueueJob(h.DB, spec)
}

// ==================== 调度 ====================

var subSchedMu sync.Mutex

// StartSubscribeScheduler 每分钟看一眼哪些订阅到了检查时间，合成一个任务入队
func StartSubscribeScheduler(h *Handler) {
	if h.DB != nil {
		subClampNextChecks(h.DB, loadSubscribeCfg(), time.Now())
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			subSchedulerTick(h, time.Now())
		}
	}()
}

// subClampNextChecks 有缺的订阅下次检查最晚不超过补缺间隔：老版本退避 / 长期找不到排到了一天到三天后，
// 设置里把间隔调短也要马上生效。不缺、在等下一集播出的不动（那是按排期算的）
func subClampNextChecks(db *gorm.DB, cfg subscribeCfg, now time.Time) {
	limit := now.Add(subCadenceOf(cfg).Gap)
	res := db.Model(&model.Subscription{}).
		Where("state IN ? AND missing > 0 AND next_check_at > ?", []string{subStateActive, subStateStalled}, limit).
		Update("next_check_at", limit)
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[订阅] ○ %d 个订阅的下次检查提前到 %s（补缺间隔 %d 小时）", res.RowsAffected, limit.Format("01-02 15:04"), cfg.GapIntervalHours)
	}
}

func subSchedulerTick(h *Handler, now time.Time) {
	subSchedMu.Lock()
	defer subSchedMu.Unlock()
	cfg := loadSubscribeCfg()
	if !cfg.Enabled || h.DB == nil {
		return
	}
	if noOrganizeLibNow() {
		return // 没有本站整理的库：转存进来也没人整理、结算永远等不到记录，订阅一起停着（下次检查时间不动，恢复后接着查）
	}
	// 正在跑的时候不再入队：跑完会把这些订阅的下次检查时间往后排
	var running int64
	h.DB.Model(&model.TaskJob{}).Where("kind = ? AND status = ?", jobKindSubscribe, jobRunning).Count(&running)
	if running > 0 {
		return
	}
	subWakeSettled(h.DB, now)
	var ids []uint
	h.DB.Model(&model.Subscription{}).
		Where("state IN ? AND (next_check_at IS NULL OR next_check_at <= ?)", []string{subStateActive, subStateStalled}, now).
		Order("next_check_at ASC").Limit(cfg.MaxSubsPerRound).Pluck("id", &ids)
	if len(ids) == 0 {
		return
	}
	if _, err := enqueueSubscribeJob(h, ids, false, "", "cron"); err != nil {
		log.Printf("[订阅] ✗ 定时检查入队失败: %v", err)
	}
}

// subWakeSettled 在路上的资源已经整理完（有整理记录、没有一条还在等确认）的订阅，下次检查提前到现在。
// 结算只在订阅自己那一轮做，而还缺集、又找不到资源的订阅会退避到一天、长期找不到的三天才查一次：
// 现场转存的三条资源早已入库，列表上一直挂着「在路上 3」，已有集数也停在上一轮。
// 只查库、零 115 请求；还在等确认的不提前（结算会原样保持在路上，提前了也是白跑一轮）
func subWakeSettled(db *gorm.DB, now time.Time) {
	var subIDs []uint
	db.Model(&model.SubAttempt{}).
		Where("status = ? AND link_id > 0", subAttemptInflight).
		Where("EXISTS (SELECT 1 FROM organize_records r WHERE r.link_id = sub_attempts.link_id)").
		Where("NOT EXISTS (SELECT 1 FROM organize_records r WHERE r.link_id = sub_attempts.link_id AND r.status = ?)", "awaiting").
		Distinct().Pluck("sub_id", &subIDs)
	if len(subIDs) == 0 {
		return
	}
	db.Model(&model.Subscription{}).
		Where("id IN ? AND state IN ? AND next_check_at > ?", subIDs, []string{subStateActive, subStateStalled}, now).
		Update("next_check_at", now)
}

// ==================== 执行 ====================

// subRunItem 一个订阅这一轮做了什么（任务详情里看）
type subRunItem struct {
	ID        uint     `json:"id"`
	Title     string   `json:"title"`
	Have      int      `json:"have"`
	Total     int      `json:"total"`
	Missing   int      `json:"missing"`
	Submitted []string `json:"submitted,omitempty"` // 「盘搜 · 资源名：S01E05–E06」
	Tried     []string `json:"tried,omitempty"`     // 试了但没成的：「资源名：原因」
	// Skipped 因资源条件 / 离线策略跳过的：「跳过 2 条资源（单集磁力 2）」，没提交东西时写进上一轮结果
	Skipped string `json:"skipped,omitempty"`
	Note    string `json:"note,omitempty"`
	Err     string `json:"err,omitempty"`
}

type subJobResult struct {
	Subs      int          `json:"subs"`      // 检查了几个订阅（任务中心摘要用）
	Submitted int          `json:"submitted"` // 提交了几条资源
	Items     []subRunItem `json:"items"`
}

// subRunner 一个任务里所有订阅共用的东西
type subRunner struct {
	h      *Handler
	cfg    subscribeCfg
	tc     *TmdbClient
	target string // 转存目录 cid
	// subs 绑定了媒体库的转存子目录（stagingsub.go），按订阅选的子目录建包装目录
	subs   map[string]stagingLib
	cookie string
	libs   []model.Library      // 媒体库表（V2）；空 = 老口径（转存目录 + 子目录绑定）
	prio   map[uint]subPriority // 一轮里每个订阅的资源优先级（priorityOf 缓存）
	// washSub 正在给哪个订阅洗版（subwash.go）：这期间建的尝试记成洗版的，分享按集挑时只要比库里那份好的（washBeats）
	washSub   uint
	washBeats func(k epKey, f washFile) bool
	searched  map[uint]subSearchHit // 一轮里每个订阅只搜一次：补缺集与洗版共用
	re0Left   int                   // 今天还能自动解锁多少 RE0 积分，<0 = 不限
	dianLeft  int                   // 癫影同上（两个站的积分各算各的）
	// stopPaid 解锁报了积分不足 / 没登录的来源：这一轮不再在这个来源付费解锁
	stopPaid map[string]bool
	// paidSubs 这一轮已经付费解锁过的订阅：补缺与洗版合起来一个订阅一轮最多付费 1 条
	paidSubs map[uint]bool
	// 离线：offlineStop 非空 = 这一轮不再提交离线（原因）；offlineLeft 本月还能提交几个、quotaLeft 115 剩余配额，<0 = 不限 / 没查
	offlineChecked         bool
	offlineStop            string
	offlineLeft, quotaLeft int
	now                    func() time.Time
	// holdWrap 这一轮建的包装目录先压着不让整理（subhold.go），held 记着建了哪些，任务结束一起放行
	holdWrap bool
	held     []string
	// 下面三个是对外的动作，做成字段是为了测试能换成假的
	search  func(sub *model.Subscription) ([]ResourceItem, string)
	mkdir   func(parent, name string) (string, error)
	rmdir   func(cid string) error
	offline func(link, target string) (status int, msg string, linkID uint, hash string)
	// landed 包装目录里现在有哪些条目（名字）：看离线秒传到了没有；出错返回 nil。nil = 不等，当作到了（测试）
	landed func(cid string) []string
	// dropTask 删掉等不到秒传的离线任务；nil = 不删（测试）
	dropTask func(hash string) error
	quota    func() (left, total int, err error) // 115 离线剩余配额
	// re0Files RE0 文件预览（付费解锁前核对缺集）；nil = 不预览，按标题判
	re0Files func(slug string) (*re0Preview, error)
	// re0Cached 这条资源的预览缓存里有没有：有的不占一轮的预览额度；nil = 都算没有
	re0Cached func(slug string) bool
	// identity 订阅这一部的形态与同名条目（subtwin.go）；nil = 不查（测试）
	identity func(sub *model.Subscription) (subIdentity, error)
	// sleep 两条资源之间的冷却（subSleep）；nil = 不歇（测试）
	sleep func(d time.Duration) bool
}

func execSubscribeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	if p.Subs == nil || len(p.Subs.IDs) == 0 {
		return jobOutcome{Idle: true}, nil
	}
	if noOrganizeLibNow() {
		return jobOutcome{}, errNoOrganizeLib
	}
	r, err := newSubRunner(h)
	if err != nil {
		return jobOutcome{}, err
	}
	defer subLane.set("", progressKeep, progressKeep, "")
	// 这一轮转存进来的先不整理，提交完了（含停止 / 出错返回）一次放行、整理一次
	defer r.releaseHeld()

	res := subJobResult{}
	submitted, failed := 0, 0
	for i, id := range p.Subs.IDs {
		if subLane.stopRequested() {
			return jobOutcome{Canceled: true, Result: res, Message: fmt.Sprintf("已停止：检查了 %d 个订阅", i)}, nil
		}
		var sub model.Subscription
		if h.DB.First(&sub, id).Error != nil {
			continue // 订阅在排队期间被删了
		}
		subLane.set("检查订阅", i, len(p.Subs.IDs), sub.Title)
		item, ran := r.run(&sub, p.Subs.Manual)
		if !ran {
			continue
		}
		submitted += len(item.Submitted)
		if item.Err != "" {
			failed++
		}
		res.Items = append(res.Items, item)
	}
	res.Subs, res.Submitted = len(res.Items), submitted
	msg := fmt.Sprintf("检查 %d 个订阅，提交 %d 条资源", len(res.Items), submitted)
	if failed > 0 {
		msg += fmt.Sprintf("，%d 个出错", failed)
	}
	if failed > 0 && failed == len(res.Items) {
		// 全部出错时把第一个原因带上：多半是同一个原因（TMDB 连不上），队列日志一眼能看出来
		return jobOutcome{Result: res}, errors.New(msg + "：" + truncateStr(res.Items[0].Err, 120))
	}
	idle := job.Priority == jobPriorityBackground && submitted == 0 && failed == 0
	return jobOutcome{Message: msg, Result: res, Idle: idle, Partial: failed > 0}, nil
}

func newSubRunner(h *Handler) (*subRunner, error) {
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	cfg := loadSubscribeCfg()
	r := &subRunner{h: h, cfg: cfg, tc: tc, target: h.shareFolderCid(), now: time.Now, re0Left: -1, dianLeft: -1, sleep: subSleep, holdWrap: true}
	// 媒体库表（V2）：转存目录是库的转存文件夹，默认进哪个库见 subTargetLib（xfertarget.go）
	if libs := loadLibraries(); len(libs) > 0 {
		l, err := h.subTargetLib("", cfg.Subdir, libs)
		if err != nil {
			return nil, err
		}
		r.target, r.libs = l.StagingCid, libs
	}
	if r.target == "" {
		return nil, errors.New("未配置转存目录（账号与媒体库 → 媒体库，给库设转存文件夹）")
	}
	r.subs = stagingLibMap(loadStagingLibs())
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, err
	}
	if r.cookie, err = h.get115Cookie(); err != nil {
		return nil, err
	}
	if cfg.Re0DailyBudget > 0 {
		r.re0Left = max(cfg.Re0DailyBudget-subPointsSpentToday(h, time.Now(), "re0"), 0)
	}
	if cfg.DianDailyBudget > 0 {
		r.dianLeft = max(cfg.DianDailyBudget-subPointsSpentToday(h, time.Now(), "dian"), 0)
	}
	r.search = r.searchAll
	r.mkdir = ops.mkdir
	r.rmdir = func(cid string) error { return ops.deleteFiles([]string{cid}) }
	r.offline = func(link, target string) (int, string, uint, string) {
		// 不做秒传试探：tryOffline 自己等 30 秒，没秒传就弃用；到了的跟这一轮一起整理
		return h.offlineSubmitTask(link, target, "订阅", false)
	}
	r.landed = func(cid string) []string {
		entries, _, err := ops.listEntries(cid, 0)
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, fmt.Sprint(e["n"]))
		}
		return names
	}
	r.dropTask = func(hash string) error { return offlineDeleteTask(r.cookie, hash) }
	r.quota = func() (int, int, error) { return offlineQuota115(r.cookie) }
	r.identity = func(sub *model.Subscription) (subIdentity, error) { return subIdentityOf(r.tc, sub) }
	r.re0Files = func(slug string) (*re0Preview, error) { return re0PreviewFiles(h, slug) }
	r.re0Cached = re0PreviewCached
	return r, nil
}

// subPointsSpentToday 今天（本地 0 点起）自动解锁在这个来源（re0 / dian）已经花了多少积分
func subPointsSpentToday(h *Handler, now time.Time, source string) int {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var sum int64
	h.DB.Model(&model.SubAttempt{}).Where("created_at >= ? AND points > 0 AND source = ?", start, source).
		Select("COALESCE(SUM(points), 0)").Scan(&sum)
	return int(sum)
}

// canPay 这条要花积分的资源这一轮还能不能解锁：这个订阅这一轮没付过费、来源没报积分不足、今天的预算够
func (r *subRunner) canPay(sub *model.Subscription, c subCand) bool {
	left := r.paidLeft(c.Item.Source)
	return !r.paidSubs[sub.ID] && !r.stopPaid[c.Item.Source] && (left < 0 || c.Paid <= left)
}

// paidLeft 这个来源今天还能自动解锁多少积分，<0 = 不限
func (r *subRunner) paidLeft(src string) int {
	if src == "dian" {
		return r.dianLeft
	}
	return r.re0Left
}

// paidSpend 记一笔自动解锁花掉的积分
func (r *subRunner) paidSpend(src string, n int) {
	if src == "dian" {
		if r.dianLeft >= 0 {
			r.dianLeft = max(r.dianLeft-n, 0)
		}
		return
	}
	if r.re0Left >= 0 {
		r.re0Left = max(r.re0Left-n, 0)
	}
}

// paidCfg 这个来源的单条上限与每日预算
func (r *subRunner) paidCfg(src string) (unlockMax, budget int) {
	if src == "dian" {
		return r.cfg.DianUnlockMax, r.cfg.DianDailyBudget
	}
	return r.cfg.Re0UnlockMax, r.cfg.Re0DailyBudget
}

// run 处理一个订阅；ran=false 表示没到时间、跳过（不进结果）
func (r *subRunner) run(sub *model.Subscription, manual bool) (subRunItem, bool) {
	db := r.h.DB
	now := r.now()
	item := subRunItem{ID: sub.ID, Title: sub.Title}
	// 头一次检查：新订阅通知放在这里而不是建订阅时，能顺带说清已有几集、缺几集
	// （网页与机器人新建后都会马上入队查一轮）
	first := sub.LastCheckAt == nil
	if !manual {
		if sub.State == subStatePaused || sub.State == subStateDone {
			return item, false
		}
		if sub.NextCheckAt != nil && sub.NextCheckAt.After(now) {
			return item, false // 排队期间已经被别的任务查过了
		}
	}

	rejected, landed := settleSubscription(db, sub, now)
	for _, a := range rejected {
		r.notify("⚠ 订阅资源内容不对", fmt.Sprintf("订阅《%s》：资源「%s」%s，这条资源不再使用", sub.Title, truncateStr(a.Title, 60), a.Reason), sub, true)
	}

	subLane.setSub("盘点缺集", 0, 0, "")
	ev, err := evaluateSubscription(db, r.tc, sub, r.cfg, now)
	if first {
		r.notifyKind("created", "🔔 新订阅", subCreatedText(sub, ev, err), sub)
	}
	if err != nil {
		item.Err = "盘点失败：" + err.Error()
		// 队列那行只有「N 个出错」，原因只在任务结果与订阅的 last_result 里，日志里看不出为什么
		log.Printf("[订阅] ✗ 《%s》%s，1 小时后再查", sub.Title, item.Err)
		next := now.Add(time.Hour)
		db.Model(sub).Updates(map[string]any{"last_check_at": now, "next_check_at": next, "last_result": truncateStr(item.Err, 250)})
		return item, true
	}

	// 补上了：按盘点结果说还差几集。这轮补齐、订阅要完成的交给完成通知，不连推两条。
	// 洗版换进来的不算「补上」（整理那边会推洗版替换）
	fill := landed[:0:0]
	for _, a := range landed {
		if !a.Wash {
			fill = append(fill, a)
		}
	}
	if eps := subLandedEpisodes(fill, ev); len(eps) > 0 && sub.MediaType == "tv" && !ev.Done {
		r.notifyKind("ingested", "🎉 订阅补上了", subLandedText(sub, eps, ev), sub)
	}

	submitted := false
	if len(ev.Missing) > 0 {
		submitted = r.searchAndSubmit(sub, ev, &item)
		if submitted {
			// 刚提交的集算进在路上，列表上的数字要跟着变（TMDB 有缓存，不多请求）
			if ev2, err := evaluateSubscription(db, r.tc, sub, r.cfg, now); err == nil {
				ev = ev2
			}
		}
	}
	item.Have, item.Total, item.Missing = ev.Have, ev.Total, len(ev.Missing)

	// 洗版（subwash.go）：补齐那一轮定下截止时间；没齐（新排了集）就清掉，下次补齐重新算
	washUpd := map[string]any{}
	if sub.Wash && ev.Done && sub.WashUntil == nil {
		t := washUntilFor(now, r.cfg.WashDays)
		sub.WashUntil = &t
		washUpd["wash_until"] = t
	} else if !ev.Done && sub.WashUntil != nil {
		sub.WashUntil = nil
		washUpd["wash_until"] = nil
	}
	wash := r.washRound(sub, ev, &item)

	sch := planSubNext(ev, submitted || wash.Submitted, sub.State, sub.EmptyRounds, now, subCadenceOf(r.cfg))
	state := sch.State
	if state == subStateDone && wash.Active {
		// 齐了但还在洗：不结束，按补老集的间隔回来看（提交了洗版的半小时后回来结算）
		state = subStateActive
		sch.Next = now.Add(time.Duration(r.cfg.GapIntervalHours) * time.Hour)
		if wash.Submitted {
			sch.Next = now.Add(subAfterSubmit)
		}
	}
	if sub.State == subStatePaused {
		state = subStatePaused // 手动查一次暂停的订阅，不替用户恢复
	}
	result := subResultLine(sub, ev, item)
	if wash.Note != "" {
		if ev.Done && wash.Active {
			result = "已齐，" + wash.Note
			if sub.WashUntil != nil {
				result += "（洗到 " + sub.WashUntil.Format("01-02") + "）"
			}
		} else {
			result += "；" + wash.Note
		}
	}
	upd := map[string]any{
		"have": ev.Have, "total": ev.Total, "missing": len(ev.Missing),
		"state": state, "last_check_at": now, "last_result": truncateStr(result, 250),
		"empty_rounds": sch.EmptyRounds,
	}
	if sch.Next.IsZero() {
		upd["next_check_at"] = nil
	} else {
		upd["next_check_at"] = sch.Next
	}
	for k, v := range washUpd {
		upd[k] = v
	}
	if state == subStateDone && sub.State != subStateDone {
		upd["done_at"] = now
		r.notifyKind("done", "✅ 订阅已完成", subDoneText(sub, ev), sub)
	}
	if sch.BecameStall && state == subStateStalled {
		r.notifyKind("stalled", "⏳ 订阅长期找不到资源",
			fmt.Sprintf("订阅《%s》缺 %d 集，约两周没找到能用的资源，仍每 %d 小时检查一次；新集播出后会加密检查", sub.Title, len(ev.Missing), r.cfg.GapIntervalHours), sub)
	}
	db.Model(sub).Updates(upd)
	item.Note = result
	return item, true
}

// subResultLine 列表上那一句「上一轮结果」
func subResultLine(sub *model.Subscription, ev subEval, item subRunItem) string {
	switch {
	case ev.Done:
		return "已完成"
	case len(item.Submitted) > 0:
		return fmt.Sprintf("缺 %d 集，提交了 %d 条资源", len(ev.Missing)+len(ev.Inflight), len(item.Submitted))
	case len(ev.Missing) > 0:
		s := fmt.Sprintf("缺 %d 集，没有找到能用的资源", len(ev.Missing))
		if len(item.Tried) > 0 {
			s = fmt.Sprintf("缺 %d 集，试了 %d 条资源都没用上", len(ev.Missing), len(item.Tried))
		}
		if item.Skipped != "" {
			s += "；" + item.Skipped
		}
		return s
	case len(ev.Inflight) > 0:
		return fmt.Sprintf("%d 集在路上，等整理", len(ev.Inflight))
	case sub.MediaType == "movie" && !ev.NextAt.IsZero():
		return "还没到发行日期，" + ev.NextAt.Format("01-02") + " 开始搜"
	case !ev.NextAt.IsZero():
		return "不缺，等下一集（" + ev.NextAt.Format("01-02 15:04") + "）"
	}
	return "不缺"
}

// subCreatedText 新订阅通知：范围 + 盘点结果
func subCreatedText(sub *model.Subscription, ev subEval, err error) string {
	s := fmt.Sprintf("订阅《%s》", sub.Title)
	if sub.Year != "" {
		s = fmt.Sprintf("订阅《%s》（%s）", sub.Title, sub.Year)
	}
	s += "：" + botSubScopeText(sub)
	switch {
	case err != nil:
		return s + "\n盘点缺集失败，稍后重试：" + truncateStr(err.Error(), 100)
	case ev.Done && sub.MediaType == "movie":
		return s + "\n库里已经有了"
	case ev.Done:
		return s + fmt.Sprintf("\n范围内 %d 集都有了", ev.Have)
	case sub.MediaType == "movie" && len(ev.Missing) == 0 && !ev.NextAt.IsZero():
		return s + "\n还没到发行日期，" + ev.NextAt.Format("01-02") + " 开始找"
	case sub.MediaType == "movie":
		return s + "\n开始找资源"
	case len(ev.Missing) > 0:
		return s + fmt.Sprintf("\n已有 %d / %d 集，缺 %d 集，开始找资源", ev.Have, ev.Total, len(ev.Missing))
	case !ev.NextAt.IsZero():
		return s + fmt.Sprintf("\n已播的 %d 集都有了，下一集 %s 播出后开始找", ev.Have, ev.NextAt.Format("01-02"))
	}
	return s + "\n还没有已播的集，等排期"
}

func subDoneText(sub *model.Subscription, ev subEval) string {
	if sub.MediaType == "movie" {
		return fmt.Sprintf("订阅《%s》已入库", sub.Title)
	}
	return fmt.Sprintf("订阅《%s》已完成：范围内 %d 集已齐", sub.Title, ev.Have)
}

// searchAll 并发搜订阅启用的全部来源（同机器人 /search 的 botResourcesAll）
func (r *subRunner) searchAll(sub *model.Subscription) ([]ResourceItem, string) {
	hub := loadTransferHubCfg()
	var want map[string]bool
	if strings.TrimSpace(sub.Sources) != "" {
		var keys []string
		if json.Unmarshal([]byte(sub.Sources), &keys) == nil && len(keys) > 0 {
			want = map[string]bool{}
			for _, k := range keys {
				want[k] = true
			}
		}
	}
	var keys []string
	var skipped []string
	for _, s := range resSources {
		if !hub.enabled(s.Key) || (want != nil && !want[s.Key]) {
			continue
		}
		if why := s.ready(); why != "" {
			skipped = append(skipped, s.Label+"："+why)
			continue
		}
		keys = append(keys, s.Key)
	}
	if len(keys) == 0 {
		return nil, joinNote("没有可用的来源", strings.Join(skipped, "；"))
	}
	q := resQuery{TmdbID: sub.TmdbID, Type: sub.MediaType, Title: sub.Title, OrigTitle: sub.OrigTitle, Year: sub.Year}
	type result struct {
		key   string
		items []ResourceItem
		err   error
	}
	ch := make(chan result, len(keys))
	for _, k := range keys {
		go func(key string) {
			res, err := searchResources(r.h, key, q, false)
			ch <- result{key, res.Items, err}
		}(k)
	}
	var all []ResourceItem
	var failed []string
	timeout := time.NewTimer(subSearchWait)
	defer timeout.Stop()
	for done := 0; done < len(keys); done++ {
		select {
		case res := <-ch:
			if res.err != nil {
				failed = append(failed, botSourceLabel(res.key)+"失败")
				continue
			}
			all = append(all, res.items...)
		case <-timeout.C:
			failed = append(failed, "有来源超时")
			done = len(keys)
		}
	}
	return resDedupe(all), strings.Join(append(failed, skipped...), "；")
}

// searchAndSubmit 搜资源、挑、逐条尝试，直到缺的补齐或试满。返回这轮有没有提交东西
func (r *subRunner) searchAndSubmit(sub *model.Subscription, ev subEval, item *subRunItem) bool {
	db := r.h.DB
	now := r.now()
	subLane.setSub("搜索资源", 0, 0, "")
	items, note := r.searchOnce(sub)
	if note != "" {
		item.Note = note
	}

	// 同名的另一部查不到就这一轮不提交：分不清是哪一部时宁可晚一轮（现场把真人版当成动画入了库）
	var identity *subIdentity
	if r.identity != nil {
		id, err := r.identity(sub)
		if err != nil {
			item.Skipped = "查同名条目失败，本轮不提交：" + truncateStr(err.Error(), 120)
			log.Printf("[订阅] ✗ 《%s》%s", sub.Title, item.Skipped)
			return false
		}
		identity = &id
	}

	tried := map[string]subTried{}
	var rows []model.SubAttempt
	db.Where("sub_id = ?", sub.ID).Order("id ASC").Find(&rows)
	for _, a := range rows {
		tried[a.Hash] = subTried{Status: a.Status, RetryAt: a.RetryAt}
	}
	missing := map[epKey]bool{}
	for _, k := range ev.Missing {
		missing[k] = true
	}
	pctx := subPickCtx{
		Sub: sub, Missing: ev.Missing, Tried: tried, Now: now,
		Exclude: append(splitKeywords(r.cfg.ExcludeDefault), splitKeywords(sub.Exclude)...),
		Include: splitKeywords(sub.Include),
		Cond:    subCondOf(r.cfg, sub.Cond), Identity: identity,
		Re0Max: r.cfg.Re0UnlockMax, Re0Left: r.re0Left, DianMax: r.cfg.DianUnlockMax, DianLeft: r.dianLeft,
		OfflineMode: r.cfg.offlineModeOf(sub), OfflineWait: time.Duration(r.cfg.OfflineWaitHours) * time.Hour,
		MissingAir: ev.MissingAir, Skipped: map[string]int{}, Prio: r.priorityOf(sub),
	}
	pctx.Re0Preview = r.re0PreviewFn(sub, missing)
	cands, over := planSubCandidates(items, pctx)
	r.recordOverLimit(sub, over, tried)
	// 先列排在前面的几条分享，按文件实际能补几集重排（subpreview.go）
	cands, ok := r.previewShares(sub, cands, missing)
	if !ok {
		return false
	}

	// got 这一轮补上了几集（电影算 1）。候选从头试到尾，没用上的不提前停（见 subscribeCfg.MaxResPerSub）
	submitted, tries, got, offlineUsed := false, 0, 0, false
	for i, c := range cands {
		if len(missing) == 0 || subLane.stopRequested() {
			break
		}
		if got >= r.cfg.MaxEpsPerSub {
			item.Note = joinNote(item.Note, fmt.Sprintf("这一轮补了 %d 集，到了上限，剩下的下一轮接着找", got))
			break
		}
		if tries >= r.cfg.MaxResPerSub {
			item.Note = joinNote(item.Note, fmt.Sprintf("这一轮试了 %d 条资源，到了上限，剩下的下一轮接着试", tries))
			break
		}
		if c.Paid > 0 && !r.canPay(sub, c) {
			continue
		}
		// 前面的资源补上了一部分：预览核实的 / 标题写明的范围里已经没有还缺的，就不用再试它
		if !c.coversAnyMissing(missing, sub.MediaType == "movie") {
			continue
		}
		// 离线：一个订阅一轮最多一个任务（配额按任务数扣），剩下的缺集里要有播出够久的，再过每月上限与 115 配额
		if c.Item.offline() {
			if offlineUsed {
				pctx.skip("一轮只下一个")
				continue
			}
			if sub.MediaType != "movie" && !c.coversRipe(missing, pctx) {
				continue
			}
			if why := r.offlineGate(); why != "" {
				pctx.skip(why)
				continue
			}
		}
		if tries > 0 && !r.cooldown() {
			break
		}
		tries++
		subLane.setSub("尝试资源", i+1, len(cands), truncateStr(c.Item.Title, 40))
		att, covered := r.tryOne(sub, c, missing, r.cfg.MaxEpsPerSub-got)
		if att.Kind == "offline" && att.LinkID > 0 {
			offlineUsed = true
			r.offlineSpent(max(att.Tasks, 1))
		}
		if att.Status == subAttemptInflight {
			submitted = true
			got += max(len(covered), 1)
			for _, k := range covered {
				delete(missing, k)
			}
			item.Submitted = append(item.Submitted, fmt.Sprintf("%s · %s：%s", botSourceLabel(c.Item.Source), truncateStr(c.Item.Title, 50), subEpisodesText(sub, covered)))
		} else {
			item.Tried = append(item.Tried, fmt.Sprintf("%s：%s", truncateStr(c.Item.Title, 50), att.Reason))
		}
	}
	item.Skipped = subSkippedText(pctx.Skipped)
	if len(item.Submitted) > 0 {
		r.notifyKind("submit", "📥 订阅已提交资源", fmt.Sprintf("订阅《%s》：\n%s\n完成后自动整理入库", sub.Title, strings.Join(item.Submitted, "\n")), sub)
	}
	return submitted
}

// cooldown 两条资源之间歇一下：TryCooldownSec 秒、±30% 抖动（P115StrgmSub 的 RateLimiter 同款），
// 叠在全局节流之上 —— 节流管单个请求，这里管「一条接一条地列分享、转存」的整体节奏。
// 返回 false = 歇的时候被叫停了
func (r *subRunner) cooldown() bool {
	if r.sleep == nil || r.cfg.TryCooldownSec <= 0 {
		return !subLane.stopRequested()
	}
	d := time.Duration(r.cfg.TryCooldownSec) * time.Second
	d += time.Duration((rand.Float64()*0.6 - 0.3) * float64(d))
	subLane.setSub("资源之间歇一下", 0, 0, fmt.Sprintf("%.1f 秒", d.Seconds()))
	return r.sleep(d)
}

// subSleep 可被「停止」打断的等待
func subSleep(d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if subLane.stopRequested() {
			return false
		}
		time.Sleep(min(500*time.Millisecond, time.Until(end)))
	}
	return !subLane.stopRequested()
}

// coversRipe 估计的范围里有没有还缺、且播出够久可以下离线的
func (c subCand) coversRipe(missing map[epKey]bool, ctx subPickCtx) bool {
	for k := range missing {
		if c.Cov.covers(k) && ctx.offlineRipe(k) {
			return true
		}
	}
	return false
}

// subSkippedText 「跳过 3 条资源（分辨率不符 2、单集磁力 1）」
func subSkippedText(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	n := 0
	for _, v := range m {
		n += v
	}
	return fmt.Sprintf("跳过 %d 条资源（%s）", n, countsText(m))
}

// countsText 「分辨率不符 2、单集磁力 1」：按原因排序，输出稳定
func countsText(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, "、")
}

// ==================== 离线的闸 ====================

// subOfflineStopNotified 停下离线的通知去重：每月上限一个月推一次，配额不足一天推一次（重启后会再推一次，可以接受）
var subOfflineStopNotified = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// offlineGate 这一轮还能不能提交离线，不能就返回原因。第一次要提交离线时才查：
// 本月用量读订阅自己的记账（零 115 请求），115 剩余配额一次请求、整个任务只查一次
func (r *subRunner) offlineGate() string {
	if r.offlineStop != "" || r.offlineChecked {
		return r.offlineStop
	}
	r.offlineChecked = true
	r.offlineLeft, r.quotaLeft = -1, -1
	now := r.now()
	if n := r.cfg.OfflineMonthly; n > 0 {
		used := subOfflineUsedThisMonth(r.h.DB, now)
		if used >= n {
			r.stopOffline("month:"+now.Format("2006-01"), 30*24*time.Hour,
				fmt.Sprintf("本月已提交 %d 个离线任务，到了上限", used),
				fmt.Sprintf("订阅这个月已经提交了 %d 个离线任务，到了「订阅设置」里的每月上限 %d，下个月 1 号恢复。这期间只转存 115 分享", used, n))
			return r.offlineStop
		}
		r.offlineLeft = n - used
	}
	if r.cfg.OfflineReserve > 0 && r.quota != nil {
		left, total, err := r.quota()
		if err != nil {
			// 查不到不拦：每月上限还管着，别因为一次网络错误整轮不下
			log.Printf("[订阅] ○ 读 115 离线配额失败，本轮不按配额拦: %v", err)
		} else {
			r.quotaLeft = left
			if left < r.cfg.OfflineReserve {
				r.stopOffline("quota", 24*time.Hour, fmt.Sprintf("115 离线配额只剩 %d", left),
					fmt.Sprintf("115 离线配额剩 %d / %d，低于「订阅设置」里保留的 %d 次，订阅暂停离线下载（留给手动离线），只转存 115 分享", left, total, r.cfg.OfflineReserve))
			}
		}
	}
	return r.offlineStop
}

// offlineSpent 提交了 n 个离线任务：本地扣掉，用完就停（不再请求 115）
func (r *subRunner) offlineSpent(n int) {
	if r.offlineLeft > 0 {
		r.offlineLeft = max(r.offlineLeft-n, 0)
		if r.offlineLeft == 0 {
			r.offlineStop = "本月离线任务到了上限"
		}
	}
	if r.quotaLeft > 0 {
		r.quotaLeft = max(r.quotaLeft-n, 0)
		if r.quotaLeft < r.cfg.OfflineReserve {
			r.offlineStop = fmt.Sprintf("115 离线配额只剩 %d", r.quotaLeft)
		}
	}
}

// offlineRoom 这一轮还能不能一次提交 n 个离线任务（一批链接）：本月剩余次数要够 n，
// 115 剩余配额扣掉 n 之后不低于保留数。offlineGate 只核过「至少还能下一个」
func (r *subRunner) offlineRoom(n int) string {
	if why := r.offlineGate(); why != "" {
		return why
	}
	if r.offlineLeft >= 0 && r.offlineLeft < n {
		return fmt.Sprintf("要提交 %d 个离线任务，本月只剩 %d 个", n, r.offlineLeft)
	}
	if r.quotaLeft >= 0 && r.quotaLeft-n < r.cfg.OfflineReserve {
		return fmt.Sprintf("要提交 %d 个离线任务，115 离线配额剩 %d，提交后低于保留的 %d 次", n, r.quotaLeft, r.cfg.OfflineReserve)
	}
	return ""
}

// stopOffline 这一轮不再提交离线；同一个原因在 quiet 之内只通知一次
func (r *subRunner) stopOffline(key string, quiet time.Duration, why, text string) {
	r.offlineStop = why
	log.Printf("[订阅] ○ 暂停离线下载：%s", why)
	subOfflineStopNotified.Lock()
	last, seen := subOfflineStopNotified.at[key]
	send := !seen || r.now().Sub(last) > quiet
	if send {
		subOfflineStopNotified.at[key] = r.now()
	}
	subOfflineStopNotified.Unlock()
	if send {
		r.notify("⚠ 订阅暂停离线下载", text, nil, false)
	}
}

// subOfflineUsedThisMonth 本月（本地 1 号 0 点起）订阅提交成功的离线任务数。
// 提交被 115 拒掉的（没有来源链接、状态 failed）没扣配额，不算
func subOfflineUsedThisMonth(db *gorm.DB, now time.Time) int {
	if db == nil {
		return 0
	}
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	// 一批链接一起提交的一笔记着几个任务（Tasks），老记录是 0，按 1 算
	var n int64
	db.Model(&model.SubAttempt{}).Where("kind = ? AND created_at >= ? AND (link_id > 0 OR status <> ?)", "offline", start, subAttemptFailed).
		Select("COALESCE(SUM(CASE WHEN tasks > 1 THEN tasks ELSE 1 END), 0)").Scan(&n)
	return int(n)
}

// coversAny 估计的范围里有没有还缺的
func (c resCoverage) coversAny(missing map[epKey]bool) bool {
	for k := range missing {
		if c.covers(k) {
			return true
		}
	}
	return false
}

// recordOverLimit 要花积分、超过上限 / 预算的 RE0 / 癫影资源：同一条只通知一次（记一笔 paid），按来源各推一条
func (r *subRunner) recordOverLimit(sub *model.Subscription, over []subCand, tried map[string]subTried) {
	lines := map[string][]string{}
	var order []string
	for _, c := range over {
		if _, seen := tried[c.Hash]; seen {
			continue
		}
		unlockMax, _ := r.paidCfg(c.Item.Source)
		reason := fmt.Sprintf("需要 %d 积分，超过自动解锁上限 %d", c.Paid, unlockMax)
		if unlockMax <= 0 {
			reason = fmt.Sprintf("需要 %d 积分，没开自动解锁", c.Paid)
		} else if c.Paid <= unlockMax {
			reason = fmt.Sprintf("需要 %d 积分，超出今天的预算", c.Paid)
		}
		r.h.DB.Create(&model.SubAttempt{SubID: sub.ID, Hash: c.Hash, Source: c.Item.Source, Kind: c.Item.Kind,
			Title: truncateStr(c.Item.Title, 480), Status: subAttemptPaid, Reason: reason})
		tried[c.Hash] = subTried{Status: subAttemptPaid}
		if _, ok := lines[c.Item.Source]; !ok {
			order = append(order, c.Item.Source)
		}
		lines[c.Item.Source] = append(lines[c.Item.Source], fmt.Sprintf("• %s（%s）", truncateStr(c.Item.Title, 60), reason))
	}
	for _, src := range order {
		label := botSourceLabel(src)
		// 记账照记（同一条不再提示），推不推看通知类型 paid
		r.notifyKind("paid", "💰 订阅："+label+"上有资源需要积分", fmt.Sprintf("订阅《%s》在%s有 %d 条资源需要手动解锁（去「发现与订阅 → 找资源」）：\n%s",
			sub.Title, label, len(lines[src]), strings.Join(lines[src], "\n")), sub)
	}
}

// tryOne 试一条资源：换链接 →（分享）列目录按集挑 / （离线）整包 → 建包装目录 → 提交。
// 返回落库的尝试与这次能补上的集
// maxEps 这一条最多补几集（这一轮的集数上限还剩多少）：分享按它截，离线挑不了集不截
func (r *subRunner) tryOne(sub *model.Subscription, c subCand, missing map[epKey]bool, maxEps int) (model.SubAttempt, []epKey) {
	db := r.h.DB
	now := r.now()
	att := model.SubAttempt{SubID: sub.ID, Hash: c.Hash, Source: c.Item.Source, Kind: c.Item.Kind,
		Title: truncateStr(c.Item.Title, 480), URL: truncateStr(c.Item.URL, 1000), Status: subAttemptFailed, Wash: r.washSub == sub.ID}
	fail := func(reason string, retry bool) (model.SubAttempt, []epKey) {
		att.Status, att.Reason = subAttemptFailed, truncateStr(reason, 480)
		if retry {
			t := now.Add(subRetryFailed)
			att.RetryAt = &t
		}
		att.ResolvedAt = &now
		db.Create(&att)
		log.Printf("[订阅] ○ 《%s》资源「%s」没用上：%s", sub.Title, truncateStr(c.Item.Title, 50), reason)
		return att, nil
	}

	// RE0 / 癫影解锁（免费 / 解锁过的也要调一次换出链接）：上限与预算在挑选时已核过
	rl, err := r.h.resolveResourceLink(resSubmitReq{Source: c.Item.Source, Action: c.Item.Action, URL: c.Item.URL,
		Code: c.Item.Code, Ref: c.Item.Ref, Title: c.Item.Title, Confirm: c.Item.Action == "unlock", Points: c.Item.Points})
	if err != nil {
		if c.Item.Action == "unlock" && c.Paid > 0 && re0StopUnlocking(err) {
			if r.stopPaid == nil {
				r.stopPaid = map[string]bool{}
			}
			r.stopPaid[c.Item.Source] = true
			label := botSourceLabel(c.Item.Source)
			r.notify("⚠ "+label+" 自动解锁失败", label+" 解锁报错："+err.Error()+"\n本轮停止在"+label+"自动解锁", sub, false)
		}
		return fail("换链接失败："+err.Error(), true)
	}
	if rl.Unlocked && c.Paid > 0 {
		att.Points = c.Paid
		r.paidSpend(c.Item.Source, c.Paid)
		if r.paidSubs == nil {
			r.paidSubs = map[uint]bool{}
		}
		r.paidSubs[sub.ID] = true
		r.notify("🔓 订阅自动解锁"+botSourceLabel(c.Item.Source)+"资源", r.re0SpentText(sub, c), sub, false)
	}
	att.URL = truncateStr(rl.URL, 1000)
	if len(rl.More) > 0 && rl.Action == "offline" {
		return r.tryOfflineBatch(sub, c, rl, missing, maxEps, &att, fail)
	}

	switch rl.Action {
	case "transfer":
		return r.tryShare(sub, c, rl, missing, maxEps, &att, fail)
	case "offline":
		return r.tryOffline(sub, c, rl, missing, &att, fail)
	}
	return fail("解锁出来的不是 115 分享，请手动处理："+rl.URL, false)
}

func (r *subRunner) re0SpentText(sub *model.Subscription, c subCand) string {
	s := fmt.Sprintf("订阅《%s》在%s自动解锁「%s」，花费 %d 积分", sub.Title, botSourceLabel(c.Item.Source), truncateStr(c.Item.Title, 60), c.Paid)
	if _, budget := r.paidCfg(c.Item.Source); budget > 0 {
		s += fmt.Sprintf("（今日已用 %d / 预算 %d）", budget-r.paidLeft(c.Item.Source), budget)
	}
	return s
}

type subFailFn func(reason string, retry bool) (model.SubAttempt, []epKey)

// tryShare 分享：列整棵树 → 只挑缺的集 → 转进包装目录
func (r *subRunner) tryShare(sub *model.Subscription, c subCand, rl resLink, missing map[epKey]bool, maxEps int, att *model.SubAttempt, fail subFailFn) (model.SubAttempt, []epKey) {
	db := r.h.DB
	att.Kind = "share"
	subLane.setSub("列分享目录", 0, 0, truncateStr(c.Item.Title, 40))
	shareCode := extractShareCode(rl.URL)
	w, _ := r.walkShare(shareCode, rl.Code) // 预看过的直接用，不列第二次
	entries, title, truncated, err := w.entries, w.title, w.truncated, w.err
	if err != nil {
		return fail(err.Error(), true)
	}
	opts := r.sharePickOpts(sub, c.Item, missing)
	opts.MaxEps = maxEps
	pick := pickShareEpisodes(entries, opts)
	covered := pick.Covered
	if sub.MediaType == "movie" && len(pick.Picks) > 0 {
		covered = []epKey{{}}
	}
	if len(pick.Picks) == 0 {
		return r.recordShareUseless(sub, c, att, rl.URL, pick, truncated), nil
	}
	wrapper, cid, err := r.makeWrapper(sub, covered)
	if err != nil {
		return fail("建包装目录失败："+err.Error(), true)
	}
	ids := make([]string, 0, len(pick.Picks))
	for _, e := range pick.Picks {
		ids = append(ids, e.ID)
	}
	linkID, err := r.h.shareReceivePicked(rl.URL, rl.Code, ids, cid, firstNonEmpty(title, c.Item.Title), "订阅", []string{wrapper})
	if err != nil {
		r.dropWrapper(cid, wrapper)
		return fail(err.Error(), true)
	}
	att.Status, att.LinkID, att.Wrapper = subAttemptInflight, linkID, wrapper
	att.Reason = truncateStr(pick.summary(), 480)
	if sub.MediaType == "tv" {
		att.Episodes = marshalEpKeys(covered)
	}
	db.Create(att)
	log.Printf("[订阅] ✓ 《%s》从「%s」转存 %d 个文件（%s）→ %s", sub.Title, truncateStr(c.Item.Title, 50), len(ids), subEpisodesText(sub, covered), wrapper)
	return *att, covered
}

// sharePickOpts 从分享里按集挑文件的条件：真正转存（tryShare）与 RE0 解锁前的文件预览共用，两边判得一样
func (r *subRunner) sharePickOpts(sub *model.Subscription, it ResourceItem, missing map[epKey]bool) sharePickOpts {
	opts := sharePickOpts{
		MediaType: sub.MediaType, Missing: missing, Rules: loadReplaceRules(),
		HaveSha1: subLedgerHasSha1,
		Compare:  r.priorityOf(sub).compareFiles,
	}
	if r.washSub == sub.ID {
		opts.Beats = r.washBeats // 洗版：只要比库里那一集好的
	}
	cond, prio := subCondOf(r.cfg, sub.Cond), r.priorityOf(sub)
	if !cond.empty() || prio.limited() {
		title, tags := it.Title, it.Tags
		opts.Accept = func(e shareEntry, hasSub bool) (bool, string) {
			if ok, why := prio.fileOK(e.Name, title, tags); !ok {
				return false, why
			}
			return cond.fileOK(e.Name, e.Size, title, tags, hasSub)
		}
	}
	if sub.MediaType == "tv" {
		if sub.Scope == subScopeSeason || sub.Scope == subScopeRange {
			opts.SeasonHint = &ParsedName{Season: sub.Season}
		}
		opts.Remap = func(eps map[string]*ParsedName) {
			remapAbsEpisodesTmdb(r.tc, &TmdbMedia{TmdbID: sub.TmdbID, MediaType: "tv"}, eps, func(string) {})
		}
	}
	return opts
}

// subRe0PreviewMax 一个订阅一轮最多新预览几条要花积分的 RE0 资源（缓存里有的不算；预览本身有 6 小时缓存，这里管的是冷启动那一轮）
const subRe0PreviewMax = 5

// errSubPreviewOff 不预览（没有 slug / 癫影清单是空的）：按标题判
var errSubPreviewOff = errors.New("不预览")

// errSubPreviewLater 这一轮的预览额度用完了：要花积分的跳过、下一轮再看，**不按标题判**——
// 看得到文件的资源没看就花积分，标题估计（尤其「全集」）靠不住
var errSubPreviewLater = errors.New("这一轮预览次数用完")

// re0PreviewFn 给挑选用的预览：列预览 → 和真正转存同一套按集挑（sharePickOpts）→ 能补上的集
func (r *subRunner) re0PreviewFn(sub *model.Subscription, missing map[epKey]bool) func(ResourceItem) ([]epKey, string, error) {
	if r.re0Files == nil {
		return nil
	}
	used := 0
	return func(it ResourceItem) ([]epKey, string, error) {
		if it.Source == "dian" {
			return r.dianPreview(sub, it, missing)
		}
		if it.Ref == "" {
			return nil, "", errSubPreviewOff
		}
		if r.re0Cached == nil || !r.re0Cached(it.Ref) {
			if used >= subRe0PreviewMax {
				return nil, "", errSubPreviewLater
			}
			used++
		}
		subLane.setSub("预览 RE0 文件", 0, 0, truncateStr(it.Title, 40))
		p, err := r.re0Files(it.Ref)
		if err != nil {
			then := "按标题判"
			if errors.Is(err, errRe0NoPreview) {
				then = "不花积分" // subpick 里跳过，不按标题判
			}
			log.Printf("[订阅] ○ 《%s》RE0 文件预览不可用，%s「%s」: %v", sub.Title, then, truncateStr(it.Title, 50), err)
			return nil, "", err
		}
		if p.Invalid != "" {
			return nil, "显示资源已失效", nil
		}
		pick := pickShareEpisodes(p.Entries, r.sharePickOpts(sub, it, missing))
		if len(pick.Picks) == 0 {
			log.Printf("[订阅] ○ 《%s》RE0「%s」文件预览里没有能用的：%s", sub.Title, truncateStr(it.Title, 50), pick.summary())
			if len(pick.Rejected) > 0 {
				return nil, "不符合资源条件", nil
			}
			return nil, "里没有缺的集", nil
		}
		if sub.MediaType == "movie" {
			return []epKey{{}}, "", nil
		}
		return pick.Covered, "", nil
	}
}

// dianPreview 癫影列表里自带的文件清单（不花积分、不发请求）当预览，判法同 RE0 预览。
// 清单是空的（老分享没填）就退回按标题判
func (r *subRunner) dianPreview(sub *model.Subscription, it ResourceItem, missing map[epKey]bool) ([]epKey, string, error) {
	if len(it.files) == 0 {
		return nil, "", errSubPreviewOff
	}
	entries := make([]shareEntry, 0, len(it.files))
	for i, f := range it.files {
		f = strings.Trim(strings.ReplaceAll(f, "\\", "/"), "/")
		dir, name := "", f
		if j := strings.LastIndex(f, "/"); j >= 0 {
			dir, name = f[:j], f[j+1:]
		}
		if name != "" {
			entries = append(entries, shareEntry{ID: "dianp" + strconv.Itoa(i), Name: name, Dir: dir})
		}
	}
	pick := pickShareEpisodes(entries, r.sharePickOpts(sub, it, missing))
	if len(pick.Picks) == 0 {
		log.Printf("[订阅] ○ 《%s》癫影「%s」文件清单里没有能用的：%s", sub.Title, truncateStr(it.Title, 50), pick.summary())
		if len(pick.Rejected) > 0 {
			return nil, "不符合资源条件", nil
		}
		return nil, "里没有缺的集", nil
	}
	if sub.MediaType == "movie" {
		return []epKey{{}}, "", nil
	}
	return pick.Covered, "", nil
}

// tryOffline 磁力 / ed2k：整包离线进包装目录。挑不了集，在路上的集按标题估计算
func (r *subRunner) tryOffline(sub *model.Subscription, c subCand, rl resLink, missing map[epKey]bool, att *model.SubAttempt, fail subFailFn) (model.SubAttempt, []epKey) {
	db := r.h.DB
	att.Kind = "offline"
	var covered []epKey
	if sub.MediaType == "movie" {
		covered = []epKey{{}}
	} else {
		for k := range missing {
			if c.Cov.covers(k) {
				covered = append(covered, k)
			}
		}
		sortEpKeys(covered)
	}
	wrapper, cid, err := r.makeWrapper(sub, covered)
	if err != nil {
		return fail("建包装目录失败："+err.Error(), true)
	}
	status, msg, linkID, hash := r.offline(rl.URL, cid)
	if status != 200 {
		r.dropWrapper(cid, wrapper)
		if strings.Contains(msg, "配额") || strings.Contains(msg, "次数") {
			r.offlineStop = "115 离线配额用完了"
		}
		return fail(msg, true)
	}
	att.LinkID = linkID // 配额已经扣了：弃用的也算这一轮的那一个离线、也计入每月上限
	if _, ok := r.waitOfflineLanded(cid, nil); !ok {
		why := fmt.Sprintf("%d 秒内没秒传，弃用", int(subOfflineWait/time.Second))
		if r.dropTask != nil && hash != "" {
			if err := r.dropTask(hash); err != nil {
				log.Printf("[订阅] ✗ 《%s》删离线任务失败，请到 115 离线列表手动删除: %v", sub.Title, err)
			} else {
				why += "（已删掉离线任务）"
			}
		}
		r.dropWrapper(cid, wrapper)
		return fail(why, false)
	}
	att.Status, att.Wrapper = subAttemptInflight, wrapper
	att.Reason = "整包离线下载"
	r.claimByWrapper(wrapper, linkID)
	if sub.MediaType == "tv" {
		att.Episodes = marshalEpKeys(covered)
	}
	db.Create(att)
	log.Printf("[订阅] ✓ 《%s》提交离线「%s」（%s）→ %s", sub.Title, truncateStr(c.Item.Title, 50), subEpisodesText(sub, covered), wrapper)
	return *att, covered
}

// tryOfflineBatch RE0 / 癫影一条离线资源解锁出一批链接（多是按集拆开的 ed2k，一集一条）。
// 积分已经花了，停下来交给用户等于白花，所以照分享那套按文件挑：ed2k 链接里带文件名与体积，
// 当成一棵只有一层的分享交给 pickShareEpisodes（缺集、资源条件、优先级、同集挑一份，与 tryShare 同口径），
// 只提交挑中的；这几个任务一起放进同一个包装目录、算这一轮的那一个离线，配额与每月上限按任务数核（offlineRoom）。
// 提交后照样只等秒传：到了的留下，没到的删掉任务，一个都没到就整笔弃用。
// 有链接认不出文件名（磁力等）时挑不了集，退回推通知让用户手动处理
func (r *subRunner) tryOfflineBatch(sub *model.Subscription, c subCand, rl resLink, missing map[epKey]bool, maxEps int, att *model.SubAttempt, fail subFailFn) (model.SubAttempt, []epKey) {
	att.Kind = "offline"
	links := append([]string{rl.URL}, rl.More...)
	manual := func(why string) (model.SubAttempt, []epKey) {
		r.notify("⚠ 订阅：解锁出多条离线链接", fmt.Sprintf("订阅《%s》解锁「%s」得到 %d 条离线链接，%s，订阅没有提交，请手动处理：\n%s",
			sub.Title, truncateStr(c.Item.Title, 60), len(links), why, strings.Join(links, "\n")), sub, true)
		return fail(fmt.Sprintf("解锁出 %d 条离线链接，%s（已推通知）", len(links), why), false)
	}
	entries := make([]shareEntry, 0, len(links))
	for i, l := range links {
		name := downloadLinkName(l)
		if name == "" || !strings.HasPrefix(strings.ToLower(l), "ed2k://") {
			return manual("有链接认不出文件名、挑不了集")
		}
		var size int64
		if parts := strings.Split(l, "|"); len(parts) >= 4 {
			size, _ = strconv.ParseInt(parts[3], 10, 64)
		}
		entries = append(entries, shareEntry{ID: strconv.Itoa(i), Name: name, Size: size})
	}
	opts := r.sharePickOpts(sub, c.Item, missing)
	opts.HaveSha1 = nil // ed2k 没有 sha1
	opts.MaxEps = maxEps
	pick := pickShareEpisodes(entries, opts)
	if len(pick.Picks) == 0 {
		reason := "里面没有缺的集（" + pick.summary() + "）"
		if att.Wash {
			reason = "里面没有比库里更好的（" + pick.summary() + "）"
		} else if sub.MediaType == "movie" {
			reason = "里面没有能用的视频（" + pick.summary() + "）"
		}
		now := r.now()
		att.Status, att.Reason, att.ResolvedAt = subAttemptUseless, truncateStr(reason, 480), &now
		r.h.DB.Create(att)
		log.Printf("[订阅] ○ 《%s》资源「%s」没用上：%s", sub.Title, truncateStr(c.Item.Title, 50), reason)
		return *att, nil
	}
	if why := r.offlineRoom(len(pick.Picks)); why != "" {
		return manual(why)
	}
	coveredOf := func(p sharePick) []epKey {
		if sub.MediaType == "movie" {
			return []epKey{{}}
		}
		return p.Covered
	}
	wrapper, cid, err := r.makeWrapper(sub, coveredOf(pick))
	if err != nil {
		return fail("建包装目录失败："+err.Error(), true)
	}

	// 逐条提交：115 上已有同一个任务（409）的不算这一笔，下回来的东西不会进这个包装目录
	type sent struct {
		e      shareEntry
		linkID uint
		hash   string
	}
	var ok []sent
	var errs []string
	for _, e := range pick.Picks {
		i, _ := strconv.Atoi(e.ID)
		status, msg, linkID, hash := r.offline(links[i], cid)
		if status != 200 {
			errs = append(errs, truncateStr(e.Name, 40)+"："+msg)
			if strings.Contains(msg, "配额") || strings.Contains(msg, "次数") {
				r.offlineStop = "115 离线配额用完了"
				break
			}
			continue
		}
		ok = append(ok, sent{e, linkID, hash})
	}
	if len(ok) == 0 {
		r.dropWrapper(cid, wrapper)
		return fail(strings.Join(errs, "；"), true)
	}
	// 配额已经扣了：弃用的也算这一轮的那一个离线、按任务数计入每月上限
	att.LinkID, att.Tasks = ok[0].linkID, len(ok)
	var more []string
	for _, s := range ok[1:] {
		more = append(more, strconv.FormatUint(uint64(s.linkID), 10))
	}
	if len(more) > 0 {
		att.LinkIDs = "," + strings.Join(more, ",") + ","
	}

	want := make([]string, 0, len(ok))
	for _, s := range ok {
		want = append(want, s.e.Name)
	}
	got, landed := r.waitOfflineLanded(cid, want)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	var dropped, kept []shareEntry
	for _, s := range ok {
		if gotSet[s.e.Name] {
			kept = append(kept, s.e)
			continue
		}
		dropped = append(dropped, s.e)
		if r.dropTask != nil && s.hash != "" {
			if err := r.dropTask(s.hash); err != nil {
				log.Printf("[订阅] ✗ 《%s》删离线任务失败，请到 115 离线列表手动删除（%s）: %v", sub.Title, s.e.Name, err)
			}
		}
	}
	if !landed {
		r.dropWrapper(cid, wrapper)
		return fail(fmt.Sprintf("%d 个离线任务 %d 秒内都没秒传，弃用（已删掉离线任务）", len(ok), int(subOfflineWait/time.Second)), false)
	}
	// 实际补上的集按到了的文件重算（没到的那几集下一轮接着找）
	covered := coveredOf(pickShareEpisodes(kept, opts))
	att.Status, att.Wrapper = subAttemptInflight, wrapper
	att.Reason = fmt.Sprintf("解锁出 %d 条离线链接，提交 %d 个", len(links), len(ok))
	if len(dropped) > 0 {
		att.Reason += fmt.Sprintf("，%d 个没秒传已删掉", len(dropped))
	}
	if len(errs) > 0 {
		att.Reason += "，" + fmt.Sprintf("%d 个提交失败", len(errs))
	}
	att.Reason = truncateStr(att.Reason, 480)
	if sub.MediaType == "tv" {
		att.Episodes = marshalEpKeys(covered)
	}
	r.h.DB.Create(att)
	// 只给头一条记包装目录名：按名字认领时落到 att.LinkID 上，订阅详情跳整理记录（?link_id=）对得上
	r.claimByWrapper(wrapper, att.LinkID)
	log.Printf("[订阅] ✓ 《%s》「%s」解锁出 %d 条离线链接，提交 %d 个、秒传到了 %d 个（%s）→ %s", sub.Title, truncateStr(c.Item.Title, 50),
		len(links), len(ok), len(kept), subEpisodesText(sub, covered), wrapper)
	return *att, covered
}

// subAttemptLinkIDs 一笔尝试对应的全部来源链接（LinkID + 一批离线的 LinkIDs）
func subAttemptLinkIDs(a *model.SubAttempt) []uint {
	var ids []uint
	if a.LinkID > 0 {
		ids = append(ids, a.LinkID)
	}
	for _, f := range strings.Split(a.LinkIDs, ",") {
		if n, err := strconv.ParseUint(f, 10, 64); err == nil && n > 0 {
			ids = append(ids, uint(n))
		}
	}
	return ids
}

// subOfflineWait 订阅提交的离线等多久秒传。离线一般都是秒传，等不到的多半要真下载几个小时，
// 不值得让它拖着这一轮、也不让它下完单独再整理一轮（维护者定的：等 30 秒，不到就弃用）
const subOfflineWait = 30 * time.Second

// waitOfflineLanded 每 5 秒看一次包装目录。want 为空（整包离线）= 有东西就算到了；
// 否则（一批 ed2k）等这几个文件名都到，返回到了的那些（got），ok = 至少到了一个。
// 中途被叫停的按全到了算：不替用户删任务，下完照旧由离线监视器触发整理
func (r *subRunner) waitOfflineLanded(cid string, want []string) (got []string, ok bool) {
	if r.landed == nil {
		return want, true
	}
	subLane.setSub("等离线秒传", 0, 0, fmt.Sprintf("最多 %d 秒", int(subOfflineWait/time.Second)))
	for waited := time.Duration(0); ; {
		step := 5 * time.Second
		if r.sleep != nil && !r.sleep(step) {
			return want, true
		}
		waited += step
		have := r.landed(cid)
		if len(want) == 0 {
			if len(have) > 0 {
				return nil, true
			}
		} else if got = landedOf(want, have); len(got) == len(want) {
			return got, true
		}
		if waited >= subOfflineWait {
			return got, len(want) > 0 && len(got) > 0
		}
	}
}

// landedOf want 里已经出现在包装目录的那些。115 存 ed2k 用链接里的文件名；
// 名字对不上、条目数却一样多的（115 替换了名字里的字符）按全到了算：包装目录是这一轮新建的，不会有别的东西
func landedOf(want, have []string) []string {
	set := map[string]bool{}
	for _, n := range have {
		set[n] = true
	}
	var got []string
	for _, n := range want {
		if set[n] {
			got = append(got, n)
		}
	}
	if len(got) < len(want) && len(have) >= len(want) {
		return want
	}
	return got
}

// claimByWrapper 给这次提交的来源链接记上包装目录名：整理记录（Source = 包装目录名）按名字就认得出来源，
// 不用等离线监视器 30 秒一轮的轮询回填产物 fid（包装目录在这一轮结束就放行整理，可能抢在回填之前）
func (r *subRunner) claimByWrapper(wrapper string, ids ...uint) {
	if r.h == nil || r.h.DB == nil || wrapper == "" {
		return
	}
	for _, id := range ids {
		var row model.DownloadLink
		if id == 0 || r.h.DB.First(&row, id).Error != nil {
			continue
		}
		if names := unmarshalStrs(row.ResultNames); !containsStr(names, wrapper) {
			r.h.DB.Model(&model.DownloadLink{}).Where("id = ?", row.ID).Update("result_names", marshalStrs(append(names, wrapper)))
		}
	}
}

// makeWrapper 在转存目录下建包装目录：给 01.mkv 这种文件补上片名与季的上下文，
// 也是整理记录认领来源链接用的名字（SUBSCRIBE-PLAN.md §6.4）。
// 不写 {tmdbid=…}：标签会强制识别，资源内容不对时会被悄悄归进订阅的那部。
// 转存目录不在媒体库里，增量本来就不管，这里的 mkdir 不用登记事件抑制
func (r *subRunner) makeWrapper(sub *model.Subscription, covered []epKey) (name, cid string, err error) {
	var n int64
	r.h.DB.Model(&model.SubAttempt{}).Where("sub_id = ?", sub.ID).Count(&n)
	name = subWrapperName(sub, covered, int(n)+1)
	cid, err = r.mkdir(r.targetOf(sub), name)
	if err == nil && r.holdWrap {
		holdSubWrapper(cid)
		r.held = append(r.held, cid)
	}
	return name, cid, err
}

// releaseHeld 这一轮提交完了：包装目录一起放行，有的话入队一次整理
func (r *subRunner) releaseHeld() {
	if len(r.held) == 0 {
		return
	}
	releaseSubWrappers(r.held...)
	r.held = nil
	go r.h.triggerOrganizeAndSync()
}

// targetOf 这个订阅的包装目录建在哪：订阅自己选的子目录 > 订阅设置的默认 > 转存目录本身。
// 子目录已经解绑的退回转存目录本身（照常入库，只是不再按来源分库），记一行日志
func (r *subRunner) targetOf(sub *model.Subscription) string {
	if len(r.libs) > 0 {
		// V2：订阅选的库（lib:<id>）→ 订阅设置的 → 默认转存库；只能是主号的库
		if l, err := r.h.subTargetLib(sub.Subdir, r.cfg.Subdir, r.libs); err == nil {
			return l.StagingCid
		}
		return r.target
	}
	sd := strings.TrimSpace(sub.Subdir)
	if sd == "" {
		sd = strings.TrimSpace(r.cfg.Subdir)
	}
	if sd == "" || sd == "root" || sd == r.target {
		return r.target
	}
	if s, ok := r.subs[sd]; ok {
		return s.Cid
	}
	log.Printf("[订阅] ○ 《%s》选的转存子目录（cid=%s）已不在绑定表里，改转存到转存目录本身", sub.Title, sd)
	return r.target
}

// dropWrapper 提交失败时收掉刚建的空包装目录：留在转存目录里，守望者会把它当成待整理的活，
// 整理不掉还会连续计数直到熔断、卡住所有转存。删除进 115 回收站
func (r *subRunner) dropWrapper(cid, name string) {
	releaseSubWrappers(cid)
	if r.rmdir == nil || cid == "" {
		return
	}
	if err := r.rmdir(cid); err != nil {
		log.Printf("[订阅] ✗ 空包装目录「%s」没删掉，请到转存目录手动删除: %v", name, err)
	}
}

// subWrapperName 「三体 (2023) S01 ·订阅12-3」。挑中的集都在同一季时才写季号
func subWrapperName(sub *model.Subscription, covered []epKey, seq int) string {
	title := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(sub.Title))
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = fmt.Sprintf("tmdb%d", sub.TmdbID)
	}
	if sub.Year != "" {
		title += " (" + sub.Year + ")"
	}
	if sub.MediaType == "tv" {
		season := -1
		for _, k := range covered {
			if season == -1 {
				season = k.S
			} else if season != k.S {
				season = -2
			}
		}
		if season < 0 && (sub.Scope == subScopeSeason || sub.Scope == subScopeRange) {
			season = sub.Season
		}
		if season >= 0 {
			title += fmt.Sprintf(" S%02d", season)
		}
	}
	return fmt.Sprintf("%s ·订阅%d-%d", title, sub.ID, seq)
}

// subEpisodesText 「S01E05–E06」/「S01E05、S02E01 等 12 集」；电影「整部」
func subEpisodesText(sub *model.Subscription, ks []epKey) string {
	if sub.MediaType == "movie" {
		return "整部"
	}
	if len(ks) == 0 {
		return "范围估计不出"
	}
	sortEpKeys(ks)
	contiguous := true
	for i := 1; i < len(ks); i++ {
		if ks[i].S != ks[0].S || ks[i].E != ks[i-1].E+1 {
			contiguous = false
			break
		}
	}
	if len(ks) == 1 {
		return ks[0].String()
	}
	if contiguous {
		return fmt.Sprintf("%s–E%02d（%d 集）", ks[0], ks[len(ks)-1].E, len(ks))
	}
	if len(ks) <= 4 {
		parts := make([]string, len(ks))
		for i, k := range ks {
			parts[i] = k.String()
		}
		return strings.Join(parts, "、")
	}
	return fmt.Sprintf("%s、%s 等 %d 集", ks[0], ks[1], len(ks))
}

// subLedgerHasSha1 台账里有没有这一份文件（115 的 sha1 是大写，台账照存）
func subLedgerHasSha1(sha1 string) bool {
	if model.DB == nil || sha1 == "" {
		return false
	}
	var n int64
	// 附加账号里有不算：订阅是往主号里补（acctlibs.go）
	model.DB.Model(&model.SyncedFile{}).Where("UPPER(sha1) = ? AND orphan_at IS NULL AND "+ledgerAcctSQL, strings.ToUpper(sha1), acctMain).Count(&n)
	return n > 0
}

// ==================== 通知 ====================

func (r *subRunner) notifyKind(kind, title, content string, sub *model.Subscription) {
	if !strings.Contains(","+r.cfg.Notify+",", ","+kind+",") {
		return
	}
	r.notify(title, content, sub, true)
}

// notify 带海报推一条（海报取不到退回纯文本）
func (r *subRunner) notify(title, content string, sub *model.Subscription, poster bool) {
	img := ""
	if poster && sub != nil && sub.PosterPath != "" && r.tc != nil {
		img = tmdbImageURL(r.tc.ImageURL, "w500", sub.PosterPath)
	}
	NotifyMessageRich(title, content, img, "")
}
