package api

// 订阅挑资源的「谁更好」（2026-10-22，设计见 docs/115-station-notes/SUB-WASH-PLAN.md）。
//
// 和整理时洗版同一套（washCompare）：先比命中洗版策略 priority_level 的第几条，同一条再按默认画质顺序
// 分辨率 → 片源 → HDR → 视频编码。订阅和整理必须判得一样（维护者定的）：订阅觉得更好、整理觉得打平，
// 转进来的就被移进「已存在」。
//
// 规则从哪来，三选一（「资源优先级」，取代原来资源条件里的分辨率 / 质量 / 特效 / 编码 / 发布组）：
//   - basic  基础：没有规则，只按默认画质顺序比
//   - follow 跟随洗版策略：这部片入库时会用的那条洗版策略（按订阅转进的库的分类方案先算分类）
//   - custom 自定义：订阅设置或订阅自己写的一组规则（写法同洗版规则）
//
// 「达标」= 命中第 1 条规则（维护者定的：最好的画面设置要求）。没有规则就没有达标一说。
// 候选排序先比命中第几条（rank），再比能补几集（2026-11-16 起，原来只把达标的提到最前）。
// Limit「只要命中前 N 条」把排序变成过滤，原来资源条件的「要 2160p」迁过来就是一条规则 + 前 1 条。
// 过滤按三态判（condField 同口径）：标题没写的分享照样去列文件、到文件一级再判，否则「剧名 全集」这种整季分享全被误杀

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

const (
	subPrioBasic  = "basic"
	subPrioFollow = "follow"
	subPrioCustom = "custom"
)

// subPrioCfg 订阅设置里的资源优先级（setting subscribe 的 priority）
type subPrioCfg struct {
	Mode  string     `json:"mode"`
	Rules []washRule `json:"rules"`
	Limit int        `json:"limit"` // 只要命中前 N 条，0 = 不限（只排序）；基础模式不用
}

// subPrioSel 单个订阅自己选的资源优先级（Subscription.Priority 的 JSON）；空 = 跟随订阅设置。
// 「只要命中前 N 条」用订阅原有的 RankLimit
type subPrioSel struct {
	Mode  string     `json:"mode"`
	Rules []washRule `json:"rules,omitempty"`
}

func validSubPrioMode(m string) bool {
	return m == subPrioBasic || m == subPrioFollow || m == subPrioCustom
}

func normalizeWashRules(rs []washRule) []washRule {
	out := make([]washRule, 0, len(rs))
	for _, r := range rs {
		for _, p := range []*string{&r.ResourceTeam, &r.ResourcePix, &r.ResourceType, &r.ResourceEffect, &r.VideoEncode, &r.AudioEncode} {
			*p = truncateStr(strings.Trim(strings.TrimSpace(strings.ReplaceAll(*p, "，", ",")), ","), 120)
		}
		if r != (washRule{}) {
			out = append(out, r)
		}
		if len(out) >= 20 {
			break
		}
	}
	return out
}

func normalizeSubPrioCfg(p subPrioCfg) subPrioCfg {
	if !validSubPrioMode(p.Mode) {
		p.Mode = subPrioFollow
	}
	p.Rules = normalizeWashRules(p.Rules)
	if p.Limit < 0 {
		p.Limit = 0
	}
	return p
}

// parseSubPrioSel 订阅上存的；空 / 坏的 = 跟随订阅设置（nil）
func parseSubPrioSel(s string) *subPrioSel {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var p subPrioSel
	if json.Unmarshal([]byte(s), &p) != nil || !validSubPrioMode(p.Mode) {
		return nil
	}
	p.Rules = normalizeWashRules(p.Rules)
	return &p
}

// subPrioMode 这个订阅生效的模式、自定义规则与「前 N 条」
func subPrioMode(cfg subscribeCfg, sub *model.Subscription) (mode string, rules []washRule, limit int) {
	if sel := parseSubPrioSel(sub.Priority); sel != nil {
		return sel.Mode, sel.Rules, sub.RankLimit
	}
	limit = cfg.Priority.Limit
	if sub.RankLimit > 0 {
		limit = sub.RankLimit
	}
	return cfg.Priority.Mode, cfg.Priority.Rules, limit
}

// subPriority 一个订阅的资源优先级。Rules 为空 = 只按默认画质顺序
type subPriority struct {
	Rules []washRule
	Limit int // 只要命中前 N 条，0 = 不限
	// Strategy 跟随洗版策略时用的那条（订阅洗版要核对它是不是 replace）；其余模式为 nil
	Strategy *washStrategy
}

// subPriorityOf 按类型取洗版策略（不知道分类时，同 resWashRank）
func subPriorityOf(mediaType string) subPriority {
	if st := washStrategyForType(mediaType); st != nil {
		return subPriority{Rules: st.PriorityLevel, Strategy: st}
	}
	return subPriority{}
}

// rank 资源标题命中第几条规则（0 最好），一条都没命中排在所有规则之后；没有规则一律 0（不分档）。
// 订阅候选排序先比它（sortSubCandidates）
func (p subPriority) rank(name string) int {
	if len(p.Rules) == 0 {
		return 0
	}
	if t := washTier(name, p.Rules); t >= 0 {
		return t
	}
	return len(p.Rules)
}

// compare >0 a 更好，<0 b 更好，0 分不出。只有名字（资源标题），体积两项不看
func (p subPriority) compare(a, b string) int {
	c, _ := washCompare(a, b, p.Rules)
	return c
}

// compareFiles 带体积的比较（分享里的文件、库里的台账行），和整理洗版的 washCompareFiles 同一套：
// 规则里写了 size_prefer / resource_size 的，订阅和整理判得一样，订阅换进来的才不会被整理移进「已存在」
func (p subPriority) compareFiles(a, b washFile) int {
	c, _ := washCompareFiles(a, b, p.Rules)
	return c
}

// settled 库里这份洗完了没有：命中第 1 条规则，且这条没写体积偏好。
// 写了 size_prefer 的话命中第 1 条也可能还有更小（大）的，只有看到资源才知道，所以照样要洗（洗版期限内）
func (p subPriority) settled(f washFile) bool {
	if len(p.Rules) == 0 || washTierOf(f, p.Rules) != 0 {
		return false
	}
	return !p.sizePreferAt(0)
}

// sizePreferAt 第 i 条规则写没写体积偏好（small / large）
func (p subPriority) sizePreferAt(i int) bool {
	if i < 0 || i >= len(p.Rules) {
		return false
	}
	sp := strings.ToLower(strings.TrimSpace(p.Rules[i].SizePrefer))
	return sp == "small" || sp == "large"
}

// limited 有没有「只要命中前 N 条」这道过滤
func (p subPriority) limited() bool { return p.Limit > 0 && len(p.Rules) > 0 }

// ruleFields 一条规则的各项（界面名、条件、从标签里取的归一值）
func ruleFields(r washRule) []subCondDim {
	return subCond{Pix: r.ResourcePix, Type: r.ResourceType, Effect: r.ResourceEffect,
		Video: r.VideoEncode, Audio: r.AudioEncode, Team: r.ResourceTeam}.dims()
}

// titleVerdict 资源标题过「前 N 条」：有一条符合就符合；都不符合但有一条是「没写」的算没写（交给文件一级）。
// 没有这道过滤时一律符合
func (p subPriority) titleVerdict(title string, tags resTags) (condVerdict, string) {
	if !p.limited() {
		return condOK, ""
	}
	if tags == (resTags{}) {
		tags = resTagsOf(title) // 没经过 resNormalize 的（测试 / 预看）现场解析
	}
	worst, why := condFail, ""
	for i := 0; i < p.Limit && i < len(p.Rules); i++ {
		v, label := ruleVerdict(p.Rules[i], func(d subCondDim) condVerdict { return condField(title, d.cond, d.value(tags)) })
		switch {
		case v == condOK:
			return condOK, ""
		case v == condUnknown:
			worst, why = condUnknown, "标题没写"+label
		case worst == condFail && why == "":
			why = label + "不符"
		}
	}
	if worst == condFail {
		return condFail, fmt.Sprintf("画质不在前 %d 条（%s）", p.Limit, why)
	}
	return worst, why
}

// ruleVerdict 一条规则的三态：有一项不符就不符，否则有一项没写就没写。label 是决定结果的那一项
func ruleVerdict(r washRule, field func(subCondDim) condVerdict) (condVerdict, string) {
	v, label := condOK, ""
	for _, d := range ruleFields(r) {
		switch field(d) {
		case condFail:
			return condFail, d.label
		case condUnknown:
			if v == condOK {
				v, label = condUnknown, d.label
			}
		}
	}
	return v, label
}

// fileOK 分享里的一个视频过「前 N 条」：每一项先看文件名，没写看资源标题，都没写算不符（同 subCond.fileOK）
func (p subPriority) fileOK(name, title string, titleTags resTags) (bool, string) {
	if !p.limited() {
		return true, ""
	}
	tags := resTagsOf(name)
	why := ""
	for i := 0; i < p.Limit && i < len(p.Rules); i++ {
		v, label := ruleVerdict(p.Rules[i], func(d subCondDim) condVerdict {
			v := condField(name, d.cond, d.value(tags))
			if v == condUnknown {
				v = condField(title, d.cond, d.value(titleTags))
			}
			return v
		})
		if v == condOK {
			return true, ""
		}
		if why == "" {
			why = label + "不符"
			if v == condUnknown {
				why = "没写" + label
			}
		}
	}
	return false, fmt.Sprintf("画质不在前 %d 条（%s）", p.Limit, why)
}

// paidOK 要花积分的资源：画质至少说得清。有规则时要命中其中一条（原来的口径），基础模式要认得出分辨率
func (p subPriority) paidOK(title string) bool {
	if len(p.Rules) > 0 {
		return washTier(title, p.Rules) >= 0
	}
	return ParseResourceInfo(title).Pix != ""
}

// ==================== 跟随洗版策略：先算这部片会进哪个分类 ====================

// subMediaCache 订阅条目的 TMDB 详情（分类规则要类型、国家、语言），按条目缓存一天
var subMediaCache = struct {
	sync.Mutex
	m map[string]subMediaHit
}{m: map[string]subMediaHit{}}

type subMediaHit struct {
	media *TmdbMedia
	at    time.Time
}

func subMediaOf(tc *TmdbClient, sub *model.Subscription) *TmdbMedia {
	key := fmt.Sprintf("%s/%d", sub.MediaType, sub.TmdbID)
	subMediaCache.Lock()
	if h, ok := subMediaCache.m[key]; ok && time.Since(h.at) < 24*time.Hour {
		subMediaCache.Unlock()
		return h.media
	}
	subMediaCache.Unlock()
	if tc == nil {
		return nil
	}
	m, err := tc.getByTmdbID(sub.TmdbID, sub.MediaType == "tv")
	if err != nil || m == nil {
		return nil // 这一轮按「不知道分类」挑策略，下一轮再取
	}
	subMediaCache.Lock()
	subMediaCache.m[key] = subMediaHit{media: m, at: time.Now()}
	subMediaCache.Unlock()
	return m
}

// subWashStrategy 跟随洗版策略：这部片入库时会用哪条。分类按订阅转进的那个库的分类方案算（V2），
// 没有媒体库表时按第一套方案；取不到 TMDB 详情或库没分类时退回不限分类的那条
func (r *subRunner) subWashStrategy(sub *model.Subscription) *washStrategy {
	category := ""
	if media := subMediaOf(r.tc, sub); media != nil {
		scheme, flat := primarySchemeID(), false
		if len(r.libs) > 0 {
			if l, err := r.h.subTargetLib(sub.Subdir, r.cfg.Subdir, r.libs); err == nil {
				scheme, flat = l.SchemeID, l.SchemeID == 0
			}
		}
		if !flat {
			category, _ = classifyTargetIn(scheme, media)
		}
	}
	if category != "" {
		if st := matchWashStrategy(sub.MediaType, category); st != nil {
			return st
		}
	}
	return washStrategyForType(sub.MediaType)
}

// priorityOf 这个订阅的资源优先级，一轮里按订阅缓存
func (r *subRunner) priorityOf(sub *model.Subscription) subPriority {
	if p, ok := r.prio[sub.ID]; ok {
		return p
	}
	mode, rules, limit := subPrioMode(r.cfg, sub)
	var p subPriority
	switch mode {
	case subPrioBasic:
	case subPrioCustom:
		p = subPriority{Rules: rules, Limit: limit}
	default:
		if st := r.subWashStrategy(sub); st != nil {
			p = subPriority{Rules: st.PriorityLevel, Limit: limit, Strategy: st}
		}
	}
	if r.prio == nil {
		r.prio = map[uint]subPriority{}
	}
	r.prio[sub.ID] = p
	return p
}

// ==================== 老配置迁移：资源条件的画质几项 → 资源优先级 ====================

// condRule 资源条件的画质几项换成一条洗版规则
func condRule(c subCond) washRule {
	return washRule{ResourcePix: c.Pix, ResourceType: c.Type, ResourceEffect: c.Effect,
		VideoEncode: c.Video, AudioEncode: c.Audio, ResourceTeam: c.Team}
}

// condDimsSet 资源条件里有没有写画质几项
func condDimsSet(c subCond) bool { return condRule(c) != (washRule{}) }

// keepCondExtras 资源条件只留中字与体积
func keepCondExtras(c subCond) subCond { return subCond{Zh: c.Zh, MinGB: c.MinGB, MaxGB: c.MaxGB} }

// migrateSubPrioGlobal 订阅设置：写了画质几项的换成「自定义 + 只要前 1 条」（原来就是硬过滤，行为不变），
// 没写的跟随洗版策略。hadCond = 原配置里存过 cond：没存过的老配置中字按原来的默认（关），不替用户打开
func migrateSubPrioGlobal(c subscribeCfg, hadCond bool) subscribeCfg {
	if condDimsSet(c.Cond) {
		c.Priority = subPrioCfg{Mode: subPrioCustom, Rules: normalizeWashRules([]washRule{condRule(c.Cond)}), Limit: 1}
	} else {
		c.Priority = subPrioCfg{Mode: subPrioFollow}
	}
	if !hadCond {
		c.Cond.Zh = false
	}
	c.Cond = keepCondExtras(c.Cond)
	c.PrioVer = subPrioVer
	return c
}

// migrateSubPrioSub 单个订阅：自己写了画质几项的换成自定义 + 前 1 条；自己设过条件（含「{}」不限）、
// 而订阅设置原来写了画质几项的，改成「跟随洗版策略」——它原来就是要盖过订阅设置的那几项，迁移后不能反被套上
func migrateSubPrioSub(sub *model.Subscription, globalDims bool) bool {
	if strings.TrimSpace(sub.Cond) == "" || sub.Priority != "" {
		return false
	}
	var c subCond
	if json.Unmarshal([]byte(sub.Cond), &c) != nil {
		return false
	}
	var sel *subPrioSel
	switch {
	case condDimsSet(c):
		sel = &subPrioSel{Mode: subPrioCustom, Rules: normalizeWashRules([]washRule{condRule(c)})}
		if sub.RankLimit > 1 {
			log.Printf("[订阅] ○ 《%s》资源条件迁成自定义优先级，画质门槛从前 %d 条改成前 1 条（原条件是硬性要求）", sub.Title, sub.RankLimit)
		}
		sub.RankLimit = 1
	case globalDims:
		sel = &subPrioSel{Mode: subPrioFollow}
	}
	if sel != nil {
		b, _ := json.Marshal(sel)
		sub.Priority = string(b)
	}
	b, _ := json.Marshal(keepCondExtras(c))
	sub.Cond = string(b)
	return true
}

const subPrioMigrateKey = "migrate.sub_priority.v1"

// MigrateSubPriority 启动时把订阅的资源条件（画质几项）迁成资源优先级（一次性）。
// 订阅设置本身在 loadSubscribeCfg 里按 prio_ver 换算，这里落库，好让单个订阅判「订阅设置原来有没有写画质」
func (h *Handler) MigrateSubPriority() {
	if h.DB == nil || h.Config == nil || h.getSettingValue(subPrioMigrateKey) != "" {
		return
	}
	raw := h.getSettingValue("subscribe")
	globalDims := false
	if raw != "" {
		var legacy struct {
			Cond    *subCond `json:"cond"`
			PrioVer int      `json:"prio_ver"`
		}
		if json.Unmarshal([]byte(raw), &legacy) == nil && legacy.PrioVer < subPrioVer && legacy.Cond != nil {
			globalDims = condDimsSet(*legacy.Cond)
		}
	}
	var subs []model.Subscription
	h.DB.Find(&subs)
	n := 0
	for i := range subs {
		if migrateSubPrioSub(&subs[i], globalDims) {
			if err := h.DB.Model(&subs[i]).Updates(map[string]any{"cond": subs[i].Cond, "priority": subs[i].Priority, "rank_limit": subs[i].RankLimit}).Error; err != nil {
				log.Printf("[迁移] ✗ 订阅资源优先级：《%s》写入失败，下次启动再试: %v", subs[i].Title, err)
				return
			}
			n++
		}
	}
	if raw != "" {
		cfg := loadSubscribeCfg() // 已按 prio_ver 换算
		b, _ := json.Marshal(cfg)
		if err := h.Config.SaveSetting("subscribe", string(b)); err != nil {
			log.Printf("[迁移] ✗ 订阅资源优先级：写入订阅设置失败，下次启动再试: %v", err)
			return
		}
	}
	_ = h.Config.SaveSetting(subPrioMigrateKey, time.Now().Format(time.RFC3339))
	if n > 0 || globalDims {
		log.Printf("[迁移] ✓ 订阅资源条件的画质几项迁成「资源优先级」（订阅设置 %v，单个订阅 %d 个）", globalDims, n)
	}
}
