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
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
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

func subSchedulerTick(h *Handler, now time.Time) {
	subSchedMu.Lock()
	defer subSchedMu.Unlock()
	cfg := loadSubscribeCfg()
	if !cfg.Enabled || h.DB == nil {
		return
	}
	// 正在跑的时候不再入队：跑完会把这些订阅的下次检查时间往后排
	var running int64
	h.DB.Model(&model.TaskJob{}).Where("kind = ? AND status = ?", jobKindSubscribe, jobRunning).Count(&running)
	if running > 0 {
		return
	}
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
	Note      string   `json:"note,omitempty"`
	Err       string   `json:"err,omitempty"`
}

type subJobResult struct {
	Subs      int          `json:"subs"`      // 检查了几个订阅（任务中心摘要用）
	Submitted int          `json:"submitted"` // 提交了几条资源
	Items     []subRunItem `json:"items"`
}

// subRunner 一个任务里所有订阅共用的东西
type subRunner struct {
	h        *Handler
	cfg      subscribeCfg
	tc       *TmdbClient
	target   string // 转存目录 cid
	cookie   string
	re0Left  int  // 今天还能自动解锁多少积分，<0 = 不限
	stopPaid bool // 解锁报了积分不足 / 没登录：这一轮不再付费解锁
	now      func() time.Time
	// 下面三个是对外的动作，做成字段是为了测试能换成假的
	search  func(sub *model.Subscription) ([]ResourceItem, string)
	mkdir   func(parent, name string) (string, error)
	rmdir   func(cid string) error
	offline func(link, target string) (status int, msg string, linkID uint)
}

func execSubscribeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	if p.Subs == nil || len(p.Subs.IDs) == 0 {
		return jobOutcome{Idle: true}, nil
	}
	r, err := newSubRunner(h)
	if err != nil {
		return jobOutcome{}, err
	}
	defer subLane.set("", progressKeep, progressKeep, "")

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
	if submitted > 0 {
		// 守望者每分钟也会看转存目录，这里只是让它别等
		go h.triggerOrganizeAndSync()
	}
	msg := fmt.Sprintf("检查 %d 个订阅，提交 %d 条资源", len(res.Items), submitted)
	if failed > 0 {
		msg += fmt.Sprintf("，%d 个出错", failed)
	}
	if failed > 0 && failed == len(res.Items) {
		return jobOutcome{Result: res}, errors.New(msg)
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
	r := &subRunner{h: h, cfg: cfg, tc: tc, target: h.shareFolderCid(), now: time.Now, re0Left: -1}
	if r.target == "" {
		return nil, errors.New("未配置转存目录（影视转存 → 链接转存）")
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, err
	}
	if r.cookie, err = h.get115Cookie(); err != nil {
		return nil, err
	}
	if cfg.Re0DailyBudget > 0 {
		r.re0Left = cfg.Re0DailyBudget - subPointsSpentToday(h, time.Now())
		if r.re0Left < 0 {
			r.re0Left = 0
		}
	}
	r.search = r.searchAll
	r.mkdir = ops.mkdir
	r.rmdir = func(cid string) error { return ops.deleteFiles([]string{cid}) }
	r.offline = func(link, target string) (int, string, uint) {
		return h.offlineSubmitLinked(link, target, "订阅", true)
	}
	return r, nil
}

// subPointsSpentToday 今天（本地 0 点起）自动解锁已经花了多少 RE0 积分
func subPointsSpentToday(h *Handler, now time.Time) int {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var sum int64
	h.DB.Model(&model.SubAttempt{}).Where("created_at >= ? AND points > 0", start).Select("COALESCE(SUM(points), 0)").Scan(&sum)
	return int(sum)
}

// run 处理一个订阅；ran=false 表示没到时间、跳过（不进结果）
func (r *subRunner) run(sub *model.Subscription, manual bool) (subRunItem, bool) {
	db := r.h.DB
	now := r.now()
	item := subRunItem{ID: sub.ID, Title: sub.Title}
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
	if err != nil {
		item.Err = "盘点失败：" + err.Error()
		next := now.Add(time.Hour)
		db.Model(sub).Updates(map[string]any{"last_check_at": now, "next_check_at": next, "last_result": truncateStr(item.Err, 250)})
		return item, true
	}

	// 补上了：按盘点结果说还差几集。这轮补齐、订阅要完成的交给完成通知，不连推两条
	if eps := subLandedEpisodes(landed, ev); len(eps) > 0 && sub.MediaType == "tv" && !ev.Done {
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

	sch := planSubNext(ev, submitted, sub.State, sub.EmptyRounds, now)
	state := sch.State
	if sub.State == subStatePaused {
		state = subStatePaused // 手动查一次暂停的订阅，不替用户恢复
	}
	result := subResultLine(sub, ev, item)
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
	if state == subStateDone && sub.State != subStateDone {
		upd["done_at"] = now
		r.notifyKind("done", "✅ 订阅已完成", subDoneText(sub, ev), sub)
	}
	if sch.BecameStall && state == subStateStalled {
		r.notifyKind("stalled", "⏳ 订阅长期找不到资源",
			fmt.Sprintf("订阅《%s》缺 %d 集，约两周没找到能用的资源，改为每 %d 天检查一次", sub.Title, len(ev.Missing), int(subStalledEvery.Hours()/24)), sub)
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
		if len(item.Tried) > 0 {
			return fmt.Sprintf("缺 %d 集，试了 %d 条资源都没用上", len(ev.Missing), len(item.Tried))
		}
		return fmt.Sprintf("缺 %d 集，没有找到能用的资源", len(ev.Missing))
	case len(ev.Inflight) > 0:
		return fmt.Sprintf("%d 集在路上，等整理", len(ev.Inflight))
	case sub.MediaType == "movie" && !ev.NextAt.IsZero():
		return "还没到发行日期，" + ev.NextAt.Format("01-02") + " 开始搜"
	case !ev.NextAt.IsZero():
		return "不缺，等下一集（" + ev.NextAt.Format("01-02 15:04") + "）"
	}
	return "不缺"
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
	items, note := r.search(sub)
	if note != "" {
		item.Note = note
	}

	tried := map[string]subTried{}
	var rows []model.SubAttempt
	db.Where("sub_id = ?", sub.ID).Order("id ASC").Find(&rows)
	for _, a := range rows {
		tried[a.Hash] = subTried{Status: a.Status, RetryAt: a.RetryAt}
	}
	cands, over := planSubCandidates(items, subPickCtx{
		Sub: sub, Missing: ev.Missing, Tried: tried, Now: now,
		Exclude: append(splitKeywords(r.cfg.ExcludeDefault), splitKeywords(sub.Exclude)...),
		Include: splitKeywords(sub.Include),
		Re0Max:  r.cfg.Re0UnlockMax, Re0Left: r.re0Left,
	})
	r.recordOverLimit(sub, over, tried)

	missing := map[epKey]bool{}
	for _, k := range ev.Missing {
		missing[k] = true
	}
	submitted, tries, paidUsed := false, 0, false
	for i, c := range cands {
		if len(missing) == 0 || tries >= r.cfg.MaxTriesPerSub || subLane.stopRequested() {
			break
		}
		if c.Paid > 0 && (paidUsed || r.stopPaid || (r.re0Left >= 0 && c.Paid > r.re0Left)) {
			continue
		}
		// 前面的资源补上了一部分：标题写明的范围里已经没有还缺的，就不用再试它
		if sub.MediaType != "movie" && (c.Cov.known() || c.Item.Action == "offline") && !c.Cov.coversAny(missing) {
			continue
		}
		tries++
		subLane.setSub("尝试资源", i+1, len(cands), truncateStr(c.Item.Title, 40))
		att, covered := r.tryOne(sub, c, missing)
		if att.Points > 0 {
			paidUsed = true
		}
		if att.Status == subAttemptInflight {
			submitted = true
			for _, k := range covered {
				delete(missing, k)
			}
			item.Submitted = append(item.Submitted, fmt.Sprintf("%s · %s：%s", botSourceLabel(c.Item.Source), truncateStr(c.Item.Title, 50), subEpisodesText(sub, covered)))
		} else {
			item.Tried = append(item.Tried, fmt.Sprintf("%s：%s", truncateStr(c.Item.Title, 50), att.Reason))
		}
	}
	if len(item.Submitted) > 0 {
		r.notifyKind("submit", "📥 订阅已提交资源", fmt.Sprintf("订阅《%s》：\n%s\n完成后自动整理入库", sub.Title, strings.Join(item.Submitted, "\n")), sub)
	}
	return submitted
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

// recordOverLimit 要花积分、超过上限 / 预算的 RE0 资源：同一条只通知一次（记一笔 paid）
func (r *subRunner) recordOverLimit(sub *model.Subscription, over []subCand, tried map[string]subTried) {
	var lines []string
	for _, c := range over {
		if _, seen := tried[c.Hash]; seen {
			continue
		}
		reason := fmt.Sprintf("需要 %d 积分，超过自动解锁上限 %d", c.Paid, r.cfg.Re0UnlockMax)
		if r.cfg.Re0UnlockMax <= 0 {
			reason = fmt.Sprintf("需要 %d 积分，没开自动解锁", c.Paid)
		} else if c.Paid <= r.cfg.Re0UnlockMax {
			reason = fmt.Sprintf("需要 %d 积分，超出今天的预算", c.Paid)
		}
		r.h.DB.Create(&model.SubAttempt{SubID: sub.ID, Hash: c.Hash, Source: c.Item.Source, Kind: c.Item.Kind,
			Title: truncateStr(c.Item.Title, 480), Status: subAttemptPaid, Reason: reason})
		tried[c.Hash] = subTried{Status: subAttemptPaid}
		lines = append(lines, fmt.Sprintf("• %s（%s）", truncateStr(c.Item.Title, 60), reason))
	}
	if len(lines) > 0 {
		r.notify("💰 订阅：RE0 上有资源需要积分", fmt.Sprintf("订阅《%s》在 RE0 有 %d 条资源需要手动解锁（去「影视转存 → 找资源」）：\n%s", sub.Title, len(lines), strings.Join(lines, "\n")), sub, true)
	}
}

// tryOne 试一条资源：换链接 →（分享）列目录按集挑 / （离线）整包 → 建包装目录 → 提交。
// 返回落库的尝试与这次能补上的集
func (r *subRunner) tryOne(sub *model.Subscription, c subCand, missing map[epKey]bool) (model.SubAttempt, []epKey) {
	db := r.h.DB
	now := r.now()
	att := model.SubAttempt{SubID: sub.ID, Hash: c.Hash, Source: c.Item.Source, Kind: c.Item.Kind,
		Title: truncateStr(c.Item.Title, 480), URL: truncateStr(c.Item.URL, 1000), Status: subAttemptFailed}
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

	// RE0 解锁（免费 / 解锁过的也要调一次换出链接）：上限与预算在挑选时已核过
	rl, err := r.h.resolveResourceLink(resSubmitReq{Source: c.Item.Source, Action: c.Item.Action, URL: c.Item.URL,
		Code: c.Item.Code, Ref: c.Item.Ref, Title: c.Item.Title, Confirm: c.Item.Action == "unlock"})
	if err != nil {
		if c.Item.Action == "unlock" && c.Paid > 0 && re0StopUnlocking(err) {
			r.stopPaid = true
			r.notify("⚠ RE0 自动解锁失败", "RE0 解锁报错："+err.Error()+"\n本轮停止自动解锁", sub, false)
		}
		return fail("换链接失败："+err.Error(), true)
	}
	if rl.Unlocked && c.Paid > 0 {
		att.Points = c.Paid
		if r.re0Left >= 0 {
			r.re0Left -= c.Paid
		}
		r.notify("🔓 订阅自动解锁 RE0 资源", r.re0SpentText(sub, c), sub, false)
	}
	att.URL = truncateStr(rl.URL, 1000)

	switch rl.Action {
	case "transfer":
		return r.tryShare(sub, c, rl, missing, &att, fail)
	case "offline":
		return r.tryOffline(sub, c, rl, missing, &att, fail)
	}
	return fail("解锁出来的不是 115 分享，请手动处理："+rl.URL, false)
}

func (r *subRunner) re0SpentText(sub *model.Subscription, c subCand) string {
	s := fmt.Sprintf("订阅《%s》自动解锁「%s」，花费 %d 积分", sub.Title, truncateStr(c.Item.Title, 60), c.Paid)
	if r.cfg.Re0DailyBudget > 0 {
		s += fmt.Sprintf("（今日已用 %d / 预算 %d）", r.cfg.Re0DailyBudget-r.re0Left, r.cfg.Re0DailyBudget)
	}
	return s
}

type subFailFn func(reason string, retry bool) (model.SubAttempt, []epKey)

// tryShare 分享：列整棵树 → 只挑缺的集 → 转进包装目录
func (r *subRunner) tryShare(sub *model.Subscription, c subCand, rl resLink, missing map[epKey]bool, att *model.SubAttempt, fail subFailFn) (model.SubAttempt, []epKey) {
	db := r.h.DB
	att.Kind = "share"
	subLane.setSub("列分享目录", 0, 0, truncateStr(c.Item.Title, 40))
	shareCode := extractShareCode(rl.URL)
	entries, title, truncated, err := shareWalk(shareCode, rl.Code, r.cookie, r.cfg.MaxSnapDirs)
	if err != nil {
		return fail(err.Error(), true)
	}
	opts := sharePickOpts{
		MediaType: sub.MediaType, Missing: missing, Rules: loadReplaceRules(),
		HaveSha1: subLedgerHasSha1,
		Rank:     func(n string) int { return resWashRank(sub.MediaType, n) },
	}
	if sub.MediaType == "tv" {
		if sub.Scope == subScopeSeason || sub.Scope == subScopeRange {
			opts.SeasonHint = &ParsedName{Season: sub.Season}
		}
		opts.Remap = func(eps map[string]*ParsedName) {
			remapAbsEpisodesTmdb(r.tc, &TmdbMedia{TmdbID: sub.TmdbID, MediaType: "tv"}, eps, func(string) {})
		}
	}
	pick := pickShareEpisodes(entries, opts)
	covered := pick.Covered
	if sub.MediaType == "movie" && len(pick.Picks) > 0 {
		covered = []epKey{{}}
	}
	if len(pick.Picks) == 0 {
		reason := "里面没有缺的集（" + pick.summary() + "）"
		if sub.MediaType == "movie" {
			reason = "里面没有能用的视频（" + pick.summary() + "）"
		}
		if truncated {
			reason += "；分享太大，只看了一部分目录"
		}
		att.Status, att.Reason = subAttemptUseless, truncateStr(reason, 480)
		now := r.now()
		att.ResolvedAt = &now
		if c.Cov.Ongoing || truncated {
			t := now.Add(subRetryOngoing)
			att.RetryAt = &t
		}
		db.Create(att)
		return *att, nil
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
	status, msg, linkID := r.offline(rl.URL, cid)
	if status != 200 {
		r.dropWrapper(cid, wrapper)
		return fail(msg, true)
	}
	att.Status, att.LinkID, att.Wrapper = subAttemptInflight, linkID, wrapper
	att.Reason = "整包离线下载"
	if sub.MediaType == "tv" {
		att.Episodes = marshalEpKeys(covered)
	}
	db.Create(att)
	log.Printf("[订阅] ✓ 《%s》提交离线「%s」（%s）→ %s", sub.Title, truncateStr(c.Item.Title, 50), subEpisodesText(sub, covered), wrapper)
	return *att, covered
}

// makeWrapper 在转存目录下建包装目录：给 01.mkv 这种文件补上片名与季的上下文，
// 也是整理记录认领来源链接用的名字（SUBSCRIBE-PLAN.md §6.4）。
// 不写 {tmdbid=…}：标签会强制识别，资源内容不对时会被悄悄归进订阅的那部。
// 转存目录不在媒体库里，增量本来就不管，这里的 mkdir 不用登记事件抑制
func (r *subRunner) makeWrapper(sub *model.Subscription, covered []epKey) (name, cid string, err error) {
	var n int64
	r.h.DB.Model(&model.SubAttempt{}).Where("sub_id = ?", sub.ID).Count(&n)
	name = subWrapperName(sub, covered, int(n)+1)
	cid, err = r.mkdir(r.target, name)
	return name, cid, err
}

// dropWrapper 提交失败时收掉刚建的空包装目录：留在转存目录里，守望者会把它当成待整理的活，
// 整理不掉还会连续计数直到熔断、卡住所有转存。删除进 115 回收站
func (r *subRunner) dropWrapper(cid, name string) {
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
	model.DB.Model(&model.SyncedFile{}).Where("UPPER(sha1) = ? AND orphan_at IS NULL", strings.ToUpper(sha1)).Count(&n)
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
