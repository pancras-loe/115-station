package api

// 订阅：从搜到的资源里挑出值得试的，排好顺序（纯函数，不发请求）。
//
// 过滤顺序与理由见 SUBSCRIBE-PLAN.md §5.2；RE0 要花积分的另有几道闸（§5.4）：
// 积分花了就拿不回来，所以比免费资源严。解锁前能看的只有 RE0 的文件预览（Re0Preview，
// 只给文件名不给链接），预览不可用时只能看标题。

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"
)

// ==================== 标题估计覆盖哪些集 ====================

// resCoverage 从资源标题估出来的季集范围
type resCoverage struct {
	SeasonLo, SeasonHi int  // 0 = 标题没写季
	EpLo, EpHi         int  // 0 = 标题没写集
	All                bool // 全集 / 合集 / complete
	Ongoing            bool // 更新至 / 连载中：分享会原地更新，没用的过一天可以再看
}

// known 标题里写了能用的范围
func (c resCoverage) known() bool { return c.SeasonLo > 0 || c.EpLo > 0 || c.All }

// single 标题写的是单独一集（S01E05 / 第5集）：离线「只下合集包」时不下这种
func (c resCoverage) single() bool { return !c.All && c.EpLo > 0 && c.EpLo == c.EpHi }

// covers 估计这一集在不在里面；估计不出（!known）一律 false
func (c resCoverage) covers(k epKey) bool {
	if !c.known() {
		return false
	}
	if c.SeasonLo > 0 && (k.S < c.SeasonLo || k.S > c.SeasonHi) {
		return false
	}
	if c.EpLo > 0 && (k.E < c.EpLo || k.E > c.EpHi) {
		return false
	}
	return true
}

var (
	reCovEpRange   = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:EP?)(\d{1,4})\s*[-~～至到]\s*(?:EP?)?(\d{1,4})(?:[^\d]|$)`)
	reCovEpRangeCn = regexp.MustCompile(`第?\s*(\d{1,4})\s*[-~～至到]\s*(\d{1,4})\s*集`)
	reCovEpTo      = regexp.MustCompile(`(?:更新至|更至|更新到|连载至|更新)\s*第?\s*(\d{1,4})\s*集`)
	reCovEpAll     = regexp.MustCompile(`全\s*(\d{1,4})\s*集`)
	reCovEpOne     = regexp.MustCompile(`(?i)S\d{1,2}\s*EP?(\d{1,4})(?:[^\d\-~]|$)|第\s*(\d{1,4})\s*集`)
	reCovOngoing   = regexp.MustCompile(`连载|更新中|更新至|更至|持续更新|更新到|未完结`)
)

// resCoverageOf 标题 → 估计范围。季用 resSeasonOf 同一套（S01 / S01-S03 / 第一季 / 全集）
func resCoverageOf(title string) resCoverage {
	var c resCoverage
	switch s := resSeasonOf(title); {
	case s == "全集":
		c.All = true
	case strings.HasPrefix(s, "S"):
		parts := strings.SplitN(strings.TrimPrefix(s, "S"), "-S", 2)
		c.SeasonLo, _ = strconv.Atoi(parts[0])
		c.SeasonHi = c.SeasonLo
		if len(parts) == 2 {
			c.SeasonHi, _ = strconv.Atoi(parts[1])
		}
	}
	c.Ongoing = reCovOngoing.MatchString(title)
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	switch {
	case reCovEpTo.MatchString(title):
		c.EpLo, c.EpHi = 1, atoi(reCovEpTo.FindStringSubmatch(title)[1])
	case reCovEpAll.MatchString(title):
		c.EpLo, c.EpHi, c.All = 1, atoi(reCovEpAll.FindStringSubmatch(title)[1]), true
	case reCovEpRange.MatchString(title):
		m := reCovEpRange.FindStringSubmatch(title)
		c.EpLo, c.EpHi = atoi(m[1]), atoi(m[2])
	case reCovEpRangeCn.MatchString(title):
		m := reCovEpRangeCn.FindStringSubmatch(title)
		c.EpLo, c.EpHi = atoi(m[1]), atoi(m[2])
	case reCovEpOne.MatchString(title):
		m := reCovEpOne.FindStringSubmatch(title)
		c.EpLo = atoi(firstNonEmpty(m[1], m[2]))
		c.EpHi = c.EpLo
	}
	if c.EpHi < c.EpLo {
		c.EpLo, c.EpHi = 0, 0
	}
	return c
}

// ==================== 过滤与排序 ====================

// subCand 一条值得试的资源
type subCand struct {
	Item   ResourceItem
	Hash   string // 去重键（SubAttempt.Hash）
	Cov    resCoverage
	Covers int // 估计能补几集（电影 1）
	// Reach 能补到的最靠后的一缺集（越接近已播出的最新一集越好）；Span 资源里一共几集（文件更多的在前）。
	// 预看过分享的按文件算，其余按标题估计；都只在能补的集数一样时才比
	Reach epKey
	Span  int
	// Stale 发布时间早于缺集里最早那集的播出日：不可能有缺的集（「持续更新」的分享内容会变，所以只排后、不丢）
	Stale bool
	Paid  int // 要花的 RE0 积分（0 = 不花）
	// Exact RE0 文件预览核实过、这条资源能补上的集（电影是一个零值）；nil = 没预览过，覆盖按标题估计
	Exact []epKey
}

// coversAnyMissing 还有没有能补的：预览核实过的按文件，其余按标题估计（估计不出的当作可能有）
func (c subCand) coversAnyMissing(missing map[epKey]bool, movie bool) bool {
	if c.Exact != nil {
		for _, k := range c.Exact {
			if movie || missing[k] {
				return true
			}
		}
		return false
	}
	if movie || !(c.Cov.known() || c.Item.Action == "offline") {
		return true
	}
	return c.Cov.coversAny(missing)
}

// share 提交后能不能按集挑（115 分享，含 RE0 解锁出来的）
func (c subCand) share() bool {
	return c.Item.Action == "transfer" || c.Item.Action == "unlock"
}

// subTried 这条资源以前对这个订阅的结果
type subTried struct {
	Status  string
	RetryAt *time.Time
}

// subPickCtx 挑选条件
type subPickCtx struct {
	Sub     *model.Subscription
	Missing []epKey
	Tried   map[string]subTried
	Now     time.Time
	Exclude []string // 内置排除词 + 订阅自己的
	Include []string
	Cond    subCond // 资源条件（subcond.go）
	// Identity 订阅这一部的形态与同名条目（subtwin.go）；nil = 不查（测试）
	Identity *subIdentity
	Re0Max   int // RE0 单条自动解锁上限，0 = 不自动解锁
	Re0Left  int // 今天还能花多少，<0 = 不限
	// Re0Preview 付费解锁前看一眼 RE0 的文件预览：返回按文件能补上的缺集（电影挑得出能用的视频就是一个零值），
	// 一集都补不上时 why 说明原因；err 非空 = 预览不可用，退回按标题判。nil = 不预览（测试 / 没接）
	Re0Preview func(it ResourceItem) (covered []epKey, why string, err error)

	// 离线：OfflineMode 空 = 不限；剧集只算播出满 OfflineWait 的缺集（MissingAir 里没有日期的不等）
	OfflineMode string
	OfflineWait time.Duration
	MissingAir  map[epKey]time.Time
	// Skipped 因资源条件 / 离线策略跳过的条数，按原因计（传了才记），任务结果里说清为什么没用
	Skipped map[string]int
}

// offlineRipe 这一集播出够久了，分享还没补上才轮到离线
func (c subPickCtx) offlineRipe(k epKey) bool {
	air := c.MissingAir[k]
	return air.IsZero() || !air.Add(c.OfflineWait).After(c.Now)
}

func (c subPickCtx) skip(reason string) {
	if c.Skipped != nil {
		c.Skipped[reason]++
	}
}

// subResHash 资源的去重键：有链接按链接（同 DownloadLink.Hash），观影 / RE0 还没换出链接的按来源 + 引用
func subResHash(it ResourceItem) string {
	if it.URL != "" {
		if h := linkHashOf(it.URL); h != "" {
			return h
		}
		return it.URL
	}
	if it.Ref != "" {
		return it.Source + ":" + it.Ref
	}
	return ""
}

// subRecentSubmit 别人（网页 / 机器人）刚提交过的不再提交：多半还在整理，结果会自己进库
const subRecentSubmit = 6 * time.Hour

// splitKeywords 逗号 / 中文逗号 / 空白分隔
func splitKeywords(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' || r == ' ' || r == '\n' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, strings.ToLower(f))
		}
	}
	return out
}

// planSubCandidates 过滤 + 排序。overLimit 是要花积分、但超过上限或今天预算的 RE0 资源（只通知、不提交）
func planSubCandidates(items []ResourceItem, c subPickCtx) (cands []subCand, overLimit []subCand) {
	movie := c.Sub.MediaType == "movie"
	var earliestAir time.Time // 缺集里最早的播出日；有没有日期不知道的缺集时不比（零值）
	for _, k := range c.Missing {
		air := c.MissingAir[k]
		if air.IsZero() {
			earliestAir = time.Time{}
			break
		}
		if earliestAir.IsZero() || air.Before(earliestAir) {
			earliestAir = air
		}
	}
	for _, it := range items {
		if !it.Relevant || it.Action == "open" || it.Kind == "pan" {
			continue
		}
		hash := subResHash(it)
		if hash == "" {
			continue
		}
		if t, ok := c.Tried[hash]; ok && t.Status != subAttemptPaid {
			if t.RetryAt == nil || t.RetryAt.After(c.Now) {
				continue
			}
		}
		if it.SubmittedAt > 0 && c.Now.Sub(time.Unix(it.SubmittedAt, 0)) < subRecentSubmit {
			continue
		}
		lower := strings.ToLower(it.Title)
		if containsAnyKeyword(lower, c.Exclude) {
			continue
		}
		if len(c.Include) > 0 && !containsAnyKeyword(lower, c.Include) {
			continue
		}
		if c.Sub.RankLimit > 0 && (it.Rank < 0 || it.Rank >= c.Sub.RankLimit) {
			continue
		}
		// 同名的另一部：标题看不出是订阅这一部的不要（凡人修仙传 2020 动画 vs 2025 真人版）。
		// RE0 不查：它是按 TMDB 编号列的资源，本来就是这一部，再按标题找证据只会误杀（相关性同样对它放行）
		if c.Identity != nil && it.Source != "re0" {
			if ok, why := subTwinVerdict(it.Title, *c.Identity, resCoverageOf(it.Title)); !ok {
				c.skip(why)
				continue
			}
		}
		// 资源条件：标题明确不符的丢；没写的分享到文件一级再判，磁力只有标题可看、又扣离线配额，不下
		verdict, why := c.Cond.titleVerdict(it.Title, it.Tags, movie, it.SizeBytes)
		if verdict == condFail {
			c.skip(why)
			continue
		}
		if verdict == condUnknown && it.Action == "offline" {
			c.skip("磁力" + why)
			continue
		}
		cand := subCand{Item: it, Hash: hash}
		cand.Stale = !movie && !earliestAir.IsZero() && it.TimeUnix > 0 && time.Unix(it.TimeUnix, 0).Before(earliestAir)
		if movie {
			cand.Covers = 1
		} else {
			cand.Cov = resCoverageOf(it.Title)
			for _, k := range c.Missing {
				if cand.Cov.covers(k) {
					cand.Covers++
				}
			}
			if cand.Cov.known() && cand.Covers == 0 {
				continue // 写明了范围，缺的不在里面
			}
			for _, k := range c.Missing {
				if cand.Cov.covers(k) && epKeyLess(cand.Reach, k) {
					cand.Reach = k
				}
			}
			if cand.Cov.EpLo > 0 && cand.Cov.EpHi >= cand.Cov.EpLo {
				cand.Span = cand.Cov.EpHi - cand.Cov.EpLo + 1
			}
		}
		// 离线只能整包下，标题估计不出覆盖缺集的不下（多出来的集交给整理去重，但不能全是多余的）。
		// 115 离线配额按任务数扣，所以另有策略：只转存分享的不下、只下合集包的不下单集磁力、
		// 剧集只算播出满等待时间的缺集（新集先等分享）
		if it.Action == "offline" {
			if c.OfflineMode == subOfflineShareOnly {
				c.skip("只转存分享")
				continue
			}
			if !movie {
				if c.OfflineMode == subOfflinePack && cand.Cov.single() {
					c.skip("单集磁力")
					continue
				}
				ripe := 0
				for _, k := range c.Missing {
					if cand.Cov.covers(k) && c.offlineRipe(k) {
						ripe++
					}
				}
				if ripe == 0 && cand.Covers > 0 {
					c.skip("新集先等分享")
					continue
				}
				cand.Covers = ripe
			}
			if cand.Covers == 0 {
				continue
			}
		}
		if it.Action == "unlock" {
			if it.Points == nil && !it.Owned {
				continue // 不知道要花多少，没法核上限
			}
			if !it.Owned && *it.Points > 0 {
				cand.Paid = *it.Points
				// 付费的额外要求：画质命中洗版规则、确认能覆盖缺集、看得出合不合资源条件（§5.4）
				if c.Sub.RankLimit == 0 && it.Rank < 0 {
					continue
				}
				over := c.Re0Max <= 0 || cand.Paid > c.Re0Max || (c.Re0Left >= 0 && cand.Paid > c.Re0Left)
				// 上限以内的先看 RE0 文件预览：按真实文件名核对缺集与资源条件，标题没写的也能用、标题写了文件里却没有的也拦得住。
				// 超限的不预览（只通知用户手动解锁，不值得多一次请求）
				if !over && c.Re0Preview != nil {
					covered, pwhy, err := c.Re0Preview(it)
					if err == nil {
						if len(covered) == 0 {
							c.skip("要花积分、文件预览" + pwhy)
							continue
						}
						cand.Exact, cand.Covers, cand.Reach = covered, len(covered), maxEpKey(covered)
						cands = append(cands, cand)
						continue
					}
					// 预览不可用（没开放 / 这个网盘不支持 / 出错）：退回按标题判
				}
				if cand.Covers == 0 {
					continue
				}
				if verdict == condUnknown {
					c.skip("要花积分、" + why) // 看不到文件、标题又看不出合不合条件，就不花积分
					continue
				}
				if over {
					overLimit = append(overLimit, cand)
					continue
				}
			}
		}
		cands = append(cands, cand)
	}
	sortSubCandidates(cands)
	return cands, overLimit
}

func containsAnyKeyword(lower string, kws []string) bool {
	for _, k := range kws {
		if k != "" && strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// sortSubCandidates 能补的集多的在前 → 缺集播出之后才发布的在前（追新集时旧资源不可能有新集）→
// 115 分享在离线前（能按集挑、秒转）→ 不花积分的在前 → 补到的集更接近最新已播出的在前 → 集数多的在前 →
// 洗版排名好的在前（没命中的最后）→ 新的在前。
// 别整体改成按时间倒序：一轮按集数限额、先提交的先占集，补老集时会让新发的低画质资源抢在高画质合集前面
func sortSubCandidates(cs []subCand) {
	rank := func(r int) int {
		if r < 0 {
			return 1 << 30
		}
		return r
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Covers != b.Covers {
			return a.Covers > b.Covers
		}
		if a.Stale != b.Stale {
			return !a.Stale
		}
		if a.share() != b.share() {
			return a.share()
		}
		if a.Paid != b.Paid {
			return a.Paid < b.Paid
		}
		if a.Reach != b.Reach {
			return epKeyLess(b.Reach, a.Reach)
		}
		if a.Span != b.Span {
			return a.Span > b.Span
		}
		if ra, rb := rank(a.Item.Rank), rank(b.Item.Rank); ra != rb {
			return ra < rb
		}
		return a.Item.TimeUnix > b.Item.TimeUnix
	})
}

// re0StopUnlocking 解锁报错是不是「再试也没用」的那种（积分不足、没登录）：本轮所有订阅都不再付费解锁。
// MediaSync115 的 _should_stop_unlocking_on_message 同款思路（只读）
func re0StopUnlocking(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"积分不足", "余额不足", "积分", "insufficient", "unauthorized", "forbidden", "token", "登录", "认证", "401", "403"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
