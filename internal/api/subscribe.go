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
	Notify         string `json:"notify"`          // 推哪几类通知：submit,done,stalled
}

func defaultSubscribeCfg() subscribeCfg {
	return subscribeCfg{
		Enabled:         true,
		AirDelayHours:   3,
		MovieWait:       subMovieWaitDigital,
		MaxSubsPerRound: 10,
		MaxTriesPerSub:  3,
		MaxSnapDirs:     30,
		ExcludeDefault:  "CAM,TS,TC,HDTC,枪版,抢先版",
		Notify:          "submit,done,stalled",
	}
}

// loadSubscribeCfg 读配置，缺的项与越界的值回落默认
func loadSubscribeCfg() subscribeCfg {
	c := defaultSubscribeCfg()
	if v := settingValueCompat("subscribe"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
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
	return c
}

func (c subscribeCfg) airDelay() time.Duration {
	return time.Duration(c.AirDelayHours) * time.Hour
}
