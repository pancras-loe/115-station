package api

// ==================== 订阅：转存前先看分享里有什么 ====================
//
// 候选按「标题估计能补几集」排，但网盘上很多整季包标题只写「剧名 4K」「剧名 中字」，估不出范围就按 0 算，
// 排在单集分享后面：从头试到尾时单集分享一条条先转（每条一次转存写请求、还占一轮的集数额度），
// 真正的整季包最后才去补剩下的，来源零碎、请求多。
//
// 所以对排在前面的几条 115 分享先列一遍文件（shareWalk，读请求、走节流），用与真正转存同一套
// pickShareEpisodes 算出按文件实际能补的集（记进 subCand.Exact，RE0 付费预览同一个口子），再重排。
// 列的结果缓存起来：转存时直接用（不列第二次），下一轮一小时内也不重列。
// 多花的只是「排第一的那条就补齐了、后面本来不会碰」时预先看的那几条；省下的是转存写请求。
//
// 只看直接给了 115 分享链接的（盘搜 / TG 等）：RE0 免费的要先调一次解锁才换得出链接，磁力下载前看不到文件。

import (
	"log"
	"sync"
	"time"

	"115-station/internal/model"
)

// subShareWalkTTL 列分享结果的缓存时长：「持续更新」的分享内容会变，别放太久
const subShareWalkTTL = time.Hour

type subShareWalk struct {
	entries   []shareEntry
	title     string
	truncated bool
	err       error
	at        time.Time
}

var subShareWalks = struct {
	sync.Mutex
	m map[string]subShareWalk
}{m: map[string]subShareWalk{}}

// resetSubShareWalks 测试用：各个测试的假分享会用同一个分享码
func resetSubShareWalks() {
	subShareWalks.Lock()
	subShareWalks.m = map[string]subShareWalk{}
	subShareWalks.Unlock()
}

// walkShare 列整棵分享树，带缓存。出错也缓存：预览时列失败的，转存时不用再撞一次。
// cached = 这次没发请求
func (r *subRunner) walkShare(shareCode, receiveCode string) (w subShareWalk, cached bool) {
	key := shareCode + "#" + receiveCode
	now := time.Now()
	subShareWalks.Lock()
	if e, ok := subShareWalks.m[key]; ok && now.Sub(e.at) < subShareWalkTTL {
		subShareWalks.Unlock()
		return e, true
	}
	for k, e := range subShareWalks.m {
		if now.Sub(e.at) >= subShareWalkTTL {
			delete(subShareWalks.m, k)
		}
	}
	subShareWalks.Unlock()

	w.entries, w.title, w.truncated, w.err = shareWalk(shareCode, receiveCode, r.cookie, r.cfg.MaxSnapDirs)
	w.at = now
	subShareWalks.Lock()
	subShareWalks.m[key] = w
	subShareWalks.Unlock()
	return w, false
}

// previewShares 先列排在前面的 SharePreviewMax 条 115 分享，按文件实际能补的集重排。
// 一集都补不上的当场记 useless 并去掉（理由与转存时判的一样），不再占一次尝试与冷却。
// 返回 false = 中途被叫停
func (r *subRunner) previewShares(sub *model.Subscription, cands []subCand, missing map[epKey]bool) ([]subCand, bool) {
	// 电影一条就是一部，只缺一集时谁都最多补一集：排序看不出差别，不为它多列
	if r.cfg.SharePreviewMax <= 0 || sub.MediaType != "tv" || len(missing) < 2 {
		return cands, true
	}
	out := make([]subCand, 0, len(cands))
	looked, walked := 0, 0
	for i, c := range cands {
		if looked >= r.cfg.SharePreviewMax || c.Item.Action != "transfer" || c.Exact != nil {
			out = append(out, c)
			continue
		}
		rl, err := r.h.resolveResourceLink(resSubmitReq{Source: c.Item.Source, Action: c.Item.Action, URL: c.Item.URL, Code: c.Item.Code})
		code := extractShareCode(rl.URL)
		if err != nil || code == "" {
			out = append(out, c)
			continue
		}
		looked++
		if walked > 0 && !r.cooldown() {
			return append(out, cands[i:]...), false
		}
		subLane.setSub("预看分享文件", looked, r.cfg.SharePreviewMax, truncateStr(c.Item.Title, 40))
		w, cached := r.walkShare(code, rl.Code)
		if !cached {
			walked++
		}
		if w.err != nil {
			out = append(out, c) // 列不出来的按标题排，轮到它时照常记失败（用缓存的错误，不再请求）
			continue
		}
		pick := pickShareEpisodes(w.entries, r.sharePickOpts(sub, c.Item, missing))
		if len(pick.Picks) == 0 {
			r.recordShareUseless(sub, c, nil, rl.URL, pick, w.truncated)
			continue
		}
		c.Exact, c.Covers = pick.Covered, len(pick.Covered)
		c.Reach, c.Span = maxEpKey(pick.Covered), pick.Episodes
		out = append(out, c)
	}
	sortSubCandidates(out)
	return out, true
}

// recordShareUseless 分享里没有缺的集：记一笔 useless。连载 / 只看了一部分目录的过一阵可以再看。
// att 是转存时已经填好的那条（RE0 付费解锁的积分记在上面，不能丢）；预看时传 nil 现场建一条
func (r *subRunner) recordShareUseless(sub *model.Subscription, c subCand, att *model.SubAttempt, link string, pick sharePick, truncated bool) model.SubAttempt {
	if att == nil {
		att = &model.SubAttempt{SubID: sub.ID, Hash: c.Hash, Source: c.Item.Source,
			Title: truncateStr(c.Item.Title, 480), URL: truncateStr(firstNonEmpty(link, c.Item.URL), 1000)}
	}
	reason := "里面没有缺的集（" + pick.summary() + "）"
	if sub.MediaType == "movie" {
		reason = "里面没有能用的视频（" + pick.summary() + "）"
	}
	if truncated {
		reason += "；分享太大，只看了一部分目录"
	}
	now := r.now()
	att.Kind, att.Status, att.Reason, att.ResolvedAt = "share", subAttemptUseless, truncateStr(reason, 480), &now
	if c.Cov.Ongoing || truncated {
		t := now.Add(subRetryOngoing)
		att.RetryAt = &t
	}
	r.h.DB.Create(att)
	log.Printf("[订阅] ○ 《%s》资源「%s」没用上：%s", sub.Title, truncateStr(c.Item.Title, 50), reason)
	return *att
}

// maxEpKey 最靠后的一集（季在前）；空 = 零值
func maxEpKey(ks []epKey) epKey {
	var m epKey
	for _, k := range ks {
		if epKeyLess(m, k) {
			m = k
		}
	}
	return m
}

func epKeyLess(a, b epKey) bool {
	if a.S != b.S {
		return a.S < b.S
	}
	return a.E < b.E
}
