package api

// 订阅：补上了没有（结算）与下次什么时候再看（排期）。
//
// 结算只看整理记录（OrganizeRecord.LinkID = 尝试的 DownloadLink.ID），零 115 请求；
// 它只改尝试的状态，「有没有」仍然每轮从台账重新算（submissing.go）—— 台账是唯一的事实。

import (
	"fmt"
	"strings"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

// subReasonAwaiting 在路上、但整理停下来等用户确认（人工确认 / AI 判定 / 同名多份）：详情页的集格子按它标「等确认」
const subReasonAwaiting = "整理停下来等你确认"

// subAttemptPaid RE0 资源要花的积分超过上限 / 预算：没提交，只记一笔免得反复通知
const subAttemptPaid = "paid"

// 在路上多久还没有整理记录就算失败：分享转存守望者每分钟接管，6 小时还没动静多半是熔断了；
// 离线要等下载，给 3 天
const (
	subInflightShareTTL   = 6 * time.Hour
	subInflightOfflineTTL = 72 * time.Hour
	// subRetryFailed 提交 / 整理失败的资源隔多久可以再试（链接失效的再试一次也就一两个请求）
	subRetryFailed = 24 * time.Hour
	// subRetryOngoing 连载分享里暂时没有缺的集，隔多久再看（分享会原地更新）
	subRetryOngoing = 24 * time.Hour
)

// subSettleResult 一条尝试的结算
type subSettleResult struct {
	Status string // 不变时为 inflight
	Reason string
	Retry  bool // 失败的可以过一阵再试
}

// settleSubAttempt 按整理记录判断一条在路上的尝试（纯函数）
func settleSubAttempt(sub *model.Subscription, a *model.SubAttempt, recs []model.OrganizeRecord, now time.Time) subSettleResult {
	keep := subSettleResult{Status: subAttemptInflight}
	if len(recs) == 0 {
		ttl := subInflightShareTTL
		if a.Kind == "offline" {
			ttl = subInflightOfflineTTL
		}
		if now.Sub(a.CreatedAt) < ttl {
			return keep
		}
		if a.Kind == "offline" {
			return subSettleResult{Status: subAttemptFailed, Reason: "离线下载 3 天还没有整理记录"}
		}
		return subSettleResult{Status: subAttemptFailed, Reason: "转存后 6 小时还没有整理记录（转存守望者可能熔断了，看任务中心）", Retry: true}
	}
	var ok, bad int
	var wrong, fails []string
	for _, r := range recs {
		switch r.Status {
		case "awaiting":
			keep.Reason = subReasonAwaiting
			return keep
		case "success", "exists":
			if r.TmdbID != 0 && (r.TmdbID != sub.TmdbID || (r.MediaType != "" && r.MediaType != sub.MediaType)) {
				wrong = append(wrong, fmt.Sprintf("《%s》", firstNonEmpty(r.Title, fmt.Sprint(r.TmdbID))))
				continue
			}
			ok++
		default: // failed / unrecognized
			bad++
			if r.Message != "" {
				fails = append(fails, r.Message)
			}
		}
	}
	switch {
	case len(wrong) > 0 && ok == 0:
		return subSettleResult{Status: subAttemptRejected, Reason: "整理认成了" + strings.Join(wrong, "、") + "，资源内容不对"}
	case ok > 0 && (bad > 0 || len(wrong) > 0):
		return subSettleResult{Status: subAttemptPartial, Reason: firstNonEmpty(strings.Join(fails, "；"), "有一部分没整理成")}
	case ok > 0:
		return subSettleResult{Status: subAttemptIngested}
	}
	// 离线的不自动重试：再提交一次同一个磁力又扣一次离线配额，下回来的还是同一份内容；要试在订阅详情里手动重试
	return subSettleResult{Status: subAttemptFailed, Reason: firstNonEmpty(strings.Join(fails, "；"), "整理失败"), Retry: a.Kind != "offline"}
}

// settleSubscription 结算一个订阅所有在路上的尝试。返回这轮被判「内容不对」的与入了库的（ingested / partial），
// 两种都要推通知
func settleSubscription(db *gorm.DB, sub *model.Subscription, now time.Time) (rejected, landed []model.SubAttempt) {
	var rows []model.SubAttempt
	db.Where("sub_id = ? AND status = ?", sub.ID, subAttemptInflight).Find(&rows)
	for i := range rows {
		a := &rows[i]
		var recs []model.OrganizeRecord
		if a.LinkID > 0 {
			db.Where("link_id = ?", a.LinkID).Find(&recs)
		}
		r := settleSubAttempt(sub, a, recs, now)
		if r.Status == subAttemptInflight {
			if r.Reason != a.Reason {
				db.Model(a).Update("reason", r.Reason)
			}
			continue
		}
		upd := map[string]any{"status": r.Status, "reason": truncateStr(r.Reason, 480), "resolved_at": now}
		if r.Retry {
			upd["retry_at"] = now.Add(subRetryFailed)
		}
		db.Model(a).Updates(upd)
		a.Status, a.Reason = r.Status, r.Reason
		switch r.Status {
		case subAttemptRejected:
			rejected = append(rejected, *a)
		case subAttemptIngested, subAttemptPartial:
			landed = append(landed, *a)
		}
	}
	return rejected, landed
}

// subLandedEpisodes 这轮入了库的尝试实际补上的集：挑中的集里已经不缺、也不在路上的
// （partial 里没整理成的那几集回到了「缺」，不算补上）
func subLandedEpisodes(landed []model.SubAttempt, ev subEval) []epKey {
	pending := map[epKey]bool{}
	for _, k := range ev.Missing {
		pending[k] = true
	}
	for _, k := range ev.Inflight {
		pending[k] = true
	}
	seen := map[epKey]bool{}
	var out []epKey
	for _, a := range landed {
		for _, k := range unmarshalEpKeys(a.Episodes) {
			if !pending[k] && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sortEpKeys(out)
	return out
}

// subLandedText 「补上了」通知的正文（剧集；电影入库就是完成，走完成通知）
func subLandedText(sub *model.Subscription, eps []epKey, ev subEval) string {
	s := fmt.Sprintf("订阅《%s》补上 %s", sub.Title, subEpisodesText(sub, eps))
	switch left := len(ev.Missing) + len(ev.Inflight); {
	case left > 0 && len(ev.Inflight) > 0:
		s += fmt.Sprintf("\n还差 %d 集（%d 集在路上）", left, len(ev.Inflight))
	case left > 0:
		s += fmt.Sprintf("\n还差 %d 集", left)
	case !ev.NextAt.IsZero():
		s += "\n已追平，下一集 " + ev.NextAt.Format("01-02") + " 播出"
	default:
		s += "\n已追平"
	}
	return s
}

// subMarkOfflineFailed 离线监视器看到任务失败：对应的订阅尝试直接判失败，不用等 3 天超时。
// 不设重试时间：115 报失败多半是资源本身的问题（死种、版权屏蔽），再提交只是再扣一次配额
func subMarkOfflineFailed(db *gorm.DB, linkID uint, name string) {
	if db == nil || linkID == 0 {
		return
	}
	now := time.Now()
	db.Model(&model.SubAttempt{}).Where("link_id = ? AND status = ?", linkID, subAttemptInflight).
		Updates(map[string]any{"status": subAttemptFailed, "reason": "115 离线任务失败：" + truncateStr(name, 200),
			"resolved_at": now})
}

// ==================== 排期 ====================

// subBackoff 有缺、但这轮没找到能用的资源时，第 n 轮之后隔多久再看
var subBackoff = []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

// subStalledRounds 连续这么多轮没找到算「长期找不到」：退避加起来约 14 天
const subStalledRounds = 17

// subStalledEvery 长期找不到的多久看一次
const subStalledEvery = 72 * time.Hour

// subWaitAirMax 不缺、等下一集时最多隔多久回来看一眼排期
const subWaitAirMax = 7 * 24 * time.Hour

// subAfterSubmit 提交过东西、或有在路上的，多久回来结算
const subAfterSubmit = 30 * time.Minute

// subSchedule 这轮之后的排期
type subSchedule struct {
	Next        time.Time
	State       string
	EmptyRounds int
	BecameStall bool // 这一轮刚变成长期找不到（推一次通知）
}

// planSubNext 算下次检查时间（纯函数）。submitted = 这轮提交了东西
func planSubNext(ev subEval, submitted bool, prevState string, emptyRounds int, now time.Time) subSchedule {
	s := subSchedule{State: subStateActive}
	soonest := func(t time.Time, d time.Duration) time.Time {
		at := now.Add(d)
		if !t.IsZero() && t.Before(at) {
			if t.Before(now) {
				return now.Add(subAfterSubmit)
			}
			return t
		}
		return at
	}
	switch {
	case ev.Done:
		s.State = subStateDone
	case submitted:
		s.Next = now.Add(subAfterSubmit)
	case len(ev.Missing) == 0 && len(ev.Inflight) > 0:
		s.Next = now.Add(subAfterSubmit)
	case len(ev.Missing) == 0:
		// 不缺：等下一集播出（最多等一周，TMDB 会改排期）；没有排期（季间）一天看一次，排期出来就接上
		switch {
		case ev.NextAt.IsZero():
			s.Next = now.Add(24 * time.Hour)
		default:
			s.Next = soonest(ev.NextAt, subWaitAirMax)
		}
	default:
		s.EmptyRounds = emptyRounds + 1
		if s.EmptyRounds >= subStalledRounds {
			s.State = subStateStalled
			s.BecameStall = prevState != subStateStalled
			s.Next = soonest(ev.NextAt, subStalledEvery)
			break
		}
		d := subBackoff[min(s.EmptyRounds, len(subBackoff))-1]
		// 下一集马上要播的话提前回来：新集出来往往连着旧集的资源一起出现
		s.Next = soonest(ev.NextAt, d)
	}
	if submitted {
		s.EmptyRounds = 0
	}
	return s
}
