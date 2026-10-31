package api

// 订阅交付成分享 STRM（SHARE-STRM-PLAN.md §13.2，2026-10-31，按维护者定的四条做）：
//  1. 订阅自己建的分享 STRM 里的集算「已有」；分享认定失效（连续 3 次）后那几集重新算缺，订阅接着找；
//  2. 只建缺的那几集（ShareSource.OnlyFids，按集挑仍走 pickShareEpisodes，和转存同一套）；
//  3. 只有磁力、没有分享的集不管：离线策略按「只转存分享」算，等分享出现；
//  4. 每个订阅单独选（Subscription.Delivery），订阅设置里给默认（subscribeCfg.Delivery）。
//
// 分享 STRM 用本地整理、手动指定成订阅那一部的 TMDB 编号（不按名字认：同名的认错了就进了别的片目），
// 开追更（检查失效靠它）。结算不看整理记录，看分享 STRM 写出了哪些集（settleShareStrmAttempt）。
// 订阅洗版不做：文件不在自己网盘里，没法让位。

import (
	"encoding/json"
	"fmt"
	"log"
	"path"
	"time"

	"gorm.io/gorm"

	"115-station/internal/model"
)

const (
	subDeliverTransfer = "transfer"
	subDeliverStrm     = "strm"
	// subShareFollowDefault 订阅建的分享 STRM 至少隔多久查一次：分享失效要靠检查发现，发现了订阅才会接着找
	subShareFollowDefault = 24
)

func validSubDelivery(s string) bool { return s == subDeliverTransfer || s == subDeliverStrm }

// deliveryOf 这个订阅的交付方式：自己选了按自己的，没选跟随订阅设置
func (c subscribeCfg) deliveryOf(sub *model.Subscription) string {
	if sub != nil && validSubDelivery(sub.Delivery) {
		return sub.Delivery
	}
	if validSubDelivery(c.Delivery) {
		return c.Delivery
	}
	return subDeliverTransfer
}

// tryShareStrm 分享里挑好了缺的集：不转存，建成（或补进）分享 STRM
func (r *subRunner) tryShareStrm(sub *model.Subscription, c subCand, rl resLink, title string, pick sharePick, covered []epKey,
	att *model.SubAttempt, fail subFailFn) (model.SubAttempt, []epKey) {
	cfg := loadShareStrmCfg()
	if !cfg.Enabled {
		return fail("订阅设成「分享 STRM」交付，但分享 STRM 没开（扩展功能 → 分享 STRM）", true)
	}
	if r.h.strmExplicitDomain() == "" {
		return fail("订阅设成「分享 STRM」交付，但还没填直连域名（Strm 管理 → 配置）", true)
	}
	ids := make([]string, 0, len(pick.Picks))
	for _, e := range pick.Picks {
		ids = append(ids, e.ID)
	}
	src, err := subShareSource(r.h, cfg, sub, extractShareCode(rl.URL), rl.Code, firstNonEmpty(title, c.Item.Title), ids)
	if err != nil {
		return fail("建分享 STRM 失败："+err.Error(), true)
	}
	if _, err := enqueueShareStrmAs(r.h.DB, src, "订阅", jobPriorityBackground); err != nil {
		return fail("排分享 STRM 任务失败："+err.Error(), true)
	}
	att.Kind, att.Status, att.ShareSourceID = "sharestrm", subAttemptInflight, src.ID
	att.Reason = truncateStr(pick.summary(), 480)
	if sub.MediaType == "tv" {
		att.Episodes = marshalEpKeys(covered)
	}
	r.h.DB.Create(att)
	log.Printf("[订阅] ✓ 《%s》从「%s」建分享 STRM %d 个文件（%s，不转存）", sub.Title, truncateStr(c.Item.Title, 50), len(ids), subEpisodesText(sub, covered))
	return *att, covered
}

// subShareSource 找到或建这个分享的 ShareSource，把要的文件并进 OnlyFids。
// 同一个分享已经是用户自己整个建的（没限定文件）：不再限定，只认领给这个订阅
func subShareSource(h *Handler, cfg shareStrmCfg, sub *model.Subscription, code, receive, title string, fids []string) (model.ShareSource, error) {
	db := h.DB
	var src model.ShareSource
	if db.Where("share_code = ?", code).First(&src).Error == nil {
		upd := map[string]any{}
		if src.SubID == 0 {
			upd["sub_id"] = sub.ID
		}
		if receive != "" && receive != src.ReceiveCode {
			upd["receive_code"] = receive
			src.ReceiveCode = receive
		}
		if src.OnlyFids != "" {
			upd["only_fids"] = mergeShareFids(src.OnlyFids, fids)
		}
		// 认定失效过、现在又能用了（订阅这一轮刚列出来）：清掉计数，重新开始
		if src.GoneStreak > 0 {
			upd["gone_streak"], upd["notified"], upd["ignored"] = 0, false, false
		}
		if len(upd) > 0 {
			db.Model(&model.ShareSource{}).Where("id = ?", src.ID).Updates(upd)
		}
		return src, nil
	}
	follow := cfg.FollowHours
	if follow <= 0 {
		follow = subShareFollowDefault
	}
	src = model.ShareSource{ShareCode: code, ReceiveCode: receive, Title: truncateStr(title, 480),
		LocalDir: h.uniqueShareDir(cfg, shareLocalDirName(title, code), code),
		Layout:   shareLayoutOrganize, TmdbID: sub.TmdbID, MediaType: sub.MediaType, Manual: true,
		FollowHours: follow, SubID: sub.ID, OnlyFids: mergeShareFids("", fids),
		State: shareStatePending, Source: "订阅"}
	if err := db.Create(&src).Error; err != nil {
		return model.ShareSource{}, err
	}
	plogShareStrm.Printf("+ 订阅《%s》建分享 STRM「%s」（%s），%d 个文件", sub.Title, src.Title, code, len(fids))
	return src, nil
}

// mergeShareFids JSON 数组并集（保持先后）
func mergeShareFids(old string, add []string) string {
	var list []string
	_ = json.Unmarshal([]byte(old), &list)
	seen := map[string]bool{}
	for _, f := range list {
		seen[f] = true
	}
	for _, f := range add {
		if !seen[f] {
			seen[f] = true
			list = append(list, f)
		}
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// shareOnlyFidSet OnlyFids → 集合；空 = 不限定
func shareOnlyFidSet(s string) map[string]bool {
	var list []string
	if s == "" || json.Unmarshal([]byte(s), &list) != nil || len(list) == 0 {
		return nil
	}
	out := make(map[string]bool, len(list))
	for _, f := range list {
		out[f] = true
	}
	return out
}

// subShareHave 订阅自己建的分享 STRM 里已有的集（分享认定失效的、文件在分享里没了的、Emby 里删过的不算）。
// found = 有视频（电影就是有了）
func subShareHave(db *gorm.DB, subID uint) (map[epKey]bool, bool) {
	eps := map[epKey]bool{}
	if db == nil || subID == 0 {
		return eps, false
	}
	var rows []model.ShareFile
	db.Table("share_files").Select("share_files.*").
		Joins("JOIN share_sources ON share_sources.id = share_files.source_id").
		Where("share_sources.sub_id = ? AND share_sources.gone_streak < ? AND share_files.kind = ? AND share_files.blocked = ? AND share_files.gone_at IS NULL",
			subID, shareGoneStreakMax, "video", false).
		Scan(&rows)
	for _, f := range rows {
		// 季号写在上一级目录（Season 2/E01）的也认：拿祖父目录当片目 key，父目录就成了季目录
		for _, k := range ledgerVideoEpisodes(path.Dir(path.Dir(f.RelPath)), f.RelPath) {
			eps[k] = true
		}
	}
	return eps, len(rows) > 0
}

// mergeShareHave 盘点时把订阅自己建的分享 STRM 并进「已有」
func mergeShareHave(db *gorm.DB, sub *model.Subscription, have *subHave) {
	eps, found := subShareHave(db, sub.ID)
	if found {
		have.Found = true
	}
	for k := range eps {
		have.Eps[k] = true
	}
}

// settleShareStrmAttempt 交付成分享 STRM 的尝试：看分享 STRM 写出了挑中的哪几集（不看整理记录）
func settleShareStrmAttempt(db *gorm.DB, sub *model.Subscription, a *model.SubAttempt, now time.Time) subSettleResult {
	keep := subSettleResult{Status: subAttemptInflight}
	var src model.ShareSource
	if db.First(&src, a.ShareSourceID).Error != nil {
		return subSettleResult{Status: subAttemptFailed, Reason: "分享 STRM 已经删掉了", Retry: true}
	}
	if src.GoneStreak >= shareGoneStreakMax {
		return subSettleResult{Status: subAttemptFailed, Reason: "分享已失效（" + firstNonEmpty(src.LastError, "分享已取消或过期") + "）"}
	}
	have, found := subShareHave(db, sub.ID)
	want := unmarshalEpKeys(a.Episodes)
	got := 0
	if sub.MediaType != "tv" {
		if found {
			got, want = 1, []epKey{{}}
		}
	} else {
		for _, k := range want {
			if have[k] {
				got++
			}
		}
	}
	busy := shareSourceBusy(db, src.ID)
	switch {
	case got > 0 && got >= len(want):
		return subSettleResult{Status: subAttemptIngested}
	case got > 0 && !busy:
		return subSettleResult{Status: subAttemptPartial, Reason: fmt.Sprintf("挑中的 %d 集只建出 %d 集", len(want), got)}
	case busy || now.Sub(a.CreatedAt) < subInflightShareTTL:
		if src.State == shareStateBadCode || src.State == shareStateError {
			keep.Reason = "分享 STRM：" + firstNonEmpty(src.LastError, src.State)
		}
		return keep
	}
	return subSettleResult{Status: subAttemptFailed, Reason: "建分享 STRM 6 小时还没写出挑中的集：" + firstNonEmpty(src.LastError, "看「分享 STRM」页"), Retry: true}
}

