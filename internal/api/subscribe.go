package api

// ==================== 资源订阅 ====================
//
// 以 TMDB 条目为单位订阅（model.Subscription），定时回答三件事：
//   缺什么（submissing.go，零 115 请求）→ 哪条资源能补 → 补上了没有。
// 搜资源复用影视转存的来源，入库仍交给整理流水线；设计见 docs/115-station-notes/SUBSCRIBE-PLAN.md。
//
// 取代 2026-10-07 删除的 TG 关键词订阅：那个只按关键词匹配频道消息，
// 不认片目、不挑资源、不管结果，开着自动转存会把同名的解说、续集一并转进来。

import (
	"encoding/json"
	"strings"
	"time"

	"115-station/internal/model"
)

// 订阅范围与追剧模式
const (
	subScopeAll    = "all"
	subScopeSeason = "season"
	subScopeRange  = "range"

	subFollowMissing = "missing" // 补缺集：范围内所有已播的集都要有（默认）
	subFollowNew     = "new"     // 只追订阅之后播出的

	subStateActive  = "active"
	subStatePaused  = "paused"
	subStateDone    = "done"
	subStateStalled = "stalled"
)

// 电影什么时候开始搜（subscribeCfg.MovieWait）
const (
	subMovieWaitDigital    = "digital"         // 数字 / 实体发行之后（默认：院线期网盘上多是枪版）
	subMovieWaitTheatrical = "theatrical_plus" // 院线上映 45 天之后
	subMovieWaitNow        = "now"             // 立刻
)

// 订阅用不用离线下载（subscribeCfg.OfflineMode / Subscription.OfflineMode）。
// 磁力在下载前看不到里面有什么（115 列种子文件要 .torrent 的 sha1，来源给的都是磁力），挑不了集，
// 而 115 的离线配额按任务数扣：一集一个磁力的话追一部剧就是几十次配额
const (
	subOfflinePack      = "pack"       // 只下合集包：标题写着单集的磁力不下（剧集默认；电影一个磁力就是整部，照下）
	subOfflineShareOnly = "share_only" // 只转存 115 分享，不用离线
	subOfflineAny       = "any"        // 不限
)

func validSubOfflineMode(m string) bool {
	return m == subOfflinePack || m == subOfflineShareOnly || m == subOfflineAny
}

// subscribeCfg 全局配置（setting "subscribe"）
type subscribeCfg struct {
	Enabled bool `json:"enabled"`
	// AirDelayHours 一集播出后多久开始搜：TMDB 只有播出日期，网盘资源一般几小时内出现
	AirDelayHours   int    `json:"air_delay_hours"`
	MovieWait       string `json:"movie_wait"`
	MaxSubsPerRound int    `json:"max_subs_per_round"`
	MaxTriesPerSub  int    `json:"max_tries_per_sub"`
	MaxSnapDirs     int    `json:"max_snap_dirs"` // 列一个分享最多进几个目录（每个目录一次节流后的请求）
	// RE0 自动解锁：单条积分 ≤ Re0UnlockMax 才解锁，0 = 不自动解锁；Re0DailyBudget 每天上限，0 = 不限
	Re0UnlockMax   int    `json:"re0_unlock_max"`
	Re0DailyBudget int    `json:"re0_daily_budget"`
	ExcludeDefault string `json:"exclude_default"` // 内置排除词，订阅自己的 Exclude 叠加在上面
	Notify         string `json:"notify"`          // 推哪几类通知：created,submit,ingested,done,stalled
	// NotifyVer 通知类型清单的版本：新增类型时加一，老配置读出来时把新类型补进 Notify。
	// 不补的话已保存过设置的用户永远收不到新加的那类（Notify 是整串存的）
	NotifyVer int `json:"notify_ver"`

	// 离线下载的几道闸（见 subOffline* 常量的说明）
	OfflineMode string `json:"offline_mode"` // 默认的离线策略，订阅自己可以改
	// OfflineWaitHours 一集播出后这么久之内只等分享：新集的分享一般几小时内就有，先别花离线配额
	OfflineWaitHours int `json:"offline_wait_hours"`
	// OfflineMonthly 订阅每月最多提交多少个离线任务，0 = 不限
	OfflineMonthly int `json:"offline_monthly"`
	// OfflineReserve 115 剩余离线配额低于它就不再提交，留给手动离线；0 = 不查配额
	OfflineReserve int `json:"offline_reserve"`

	// Cond 默认的资源条件（subcond.go），订阅自己可以另设
	Cond subCond `json:"cond"`
}

// subNotifyVer 当前通知类型清单的版本。1 = 加了 ingested（补上了缺集），2 = 加了 created（新订阅）
const subNotifyVer = 2

// subNotifyAdded 每个版本新增的通知类型，默认开启
var subNotifyAdded = map[int]string{1: "ingested", 2: "created"}

func defaultSubscribeCfg() subscribeCfg {
	return subscribeCfg{
		Enabled:         true,
		AirDelayHours:   3,
		MovieWait:       subMovieWaitDigital,
		MaxSubsPerRound: 10,
		MaxTriesPerSub:  3,
		MaxSnapDirs:     30,
		ExcludeDefault:  "CAM,TS,TC,HDTC,枪版,抢先版",
		Notify:          "created,submit,ingested,done,stalled",
		NotifyVer:       subNotifyVer,

		OfflineMode:      subOfflinePack,
		OfflineWaitHours: 24,
		OfflineMonthly:   30,
		OfflineReserve:   10,
	}
}

// loadSubscribeCfg 读配置，缺的项与越界的值回落默认
func loadSubscribeCfg() subscribeCfg {
	c := defaultSubscribeCfg()
	if v := settingValueCompat("subscribe"); v != "" {
		c.NotifyVer = 0 // 老配置里没有这个键
		_ = json.Unmarshal([]byte(v), &c)
		// 原来一类都不推的，是用户关掉了通知，新类型也不替用户打开
		for ver := c.NotifyVer + 1; ver <= subNotifyVer && strings.TrimSpace(c.Notify) != ""; ver++ {
			if k := subNotifyAdded[ver]; k != "" && !strings.Contains(","+c.Notify+",", ","+k+",") {
				c.Notify = strings.Trim(c.Notify+","+k, ",")
			}
		}
	}
	return normalizeSubscribeCfg(c)
}

func normalizeSubscribeCfg(c subscribeCfg) subscribeCfg {
	d := defaultSubscribeCfg()
	if c.AirDelayHours < 0 || c.AirDelayHours > 72 {
		c.AirDelayHours = d.AirDelayHours
	}
	switch c.MovieWait {
	case subMovieWaitDigital, subMovieWaitTheatrical, subMovieWaitNow:
	default:
		c.MovieWait = d.MovieWait
	}
	if c.MaxSubsPerRound <= 0 || c.MaxSubsPerRound > 50 {
		c.MaxSubsPerRound = d.MaxSubsPerRound
	}
	if c.MaxTriesPerSub <= 0 || c.MaxTriesPerSub > 10 {
		c.MaxTriesPerSub = d.MaxTriesPerSub
	}
	if c.MaxSnapDirs <= 0 || c.MaxSnapDirs > 200 {
		c.MaxSnapDirs = d.MaxSnapDirs
	}
	if c.Re0UnlockMax < 0 {
		c.Re0UnlockMax = 0
	}
	if c.Re0DailyBudget < 0 {
		c.Re0DailyBudget = 0
	}
	c.ExcludeDefault = strings.TrimSpace(c.ExcludeDefault)
	if !validSubOfflineMode(c.OfflineMode) {
		c.OfflineMode = d.OfflineMode
	}
	if c.OfflineWaitHours < 0 || c.OfflineWaitHours > 24*7 {
		c.OfflineWaitHours = d.OfflineWaitHours
	}
	if c.OfflineMonthly < 0 {
		c.OfflineMonthly = 0
	}
	if c.OfflineReserve < 0 {
		c.OfflineReserve = 0
	}
	c.Cond = normalizeSubCond(c.Cond)
	// 读的时候已经补过新类型；保存时记成当前版本，用户之后关掉的类型不会再被补回来
	c.NotifyVer = subNotifyVer
	return c
}

func (c subscribeCfg) airDelay() time.Duration {
	return time.Duration(c.AirDelayHours) * time.Hour
}

// offlineModeOf 这个订阅的离线策略：订阅自己设了用自己的，否则跟全局
func (c subscribeCfg) offlineModeOf(sub *model.Subscription) string {
	if validSubOfflineMode(sub.OfflineMode) {
		return sub.OfflineMode
	}
	return c.OfflineMode
}
