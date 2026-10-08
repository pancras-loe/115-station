package api

// 订阅第一步：缺什么。
//
//	缺 = TMDB 范围内已播的集 − 台账里已有的 − 已经提交、还没整理完的（在路上）
//
// 全程零 115 请求：已有看台账（SyncedFile），整理一结束就是最新的，Emby 没配置也能用；
// 在路上看 SubAttempt，不扣掉的话下一轮又会去找同一集。
// MediaSync115 用的是「TMDB 已播 − Emby 已有」（tv_missing_service.py，只读思路）。

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

// epKey 一集：季号 + 集号
type epKey struct{ S, E int }

func (k epKey) String() string { return fmt.Sprintf("S%02dE%02d", k.S, k.E) }

var reEpKey = regexp.MustCompile(`^S(\d+)E(\d+)$`)

func parseEpKey(s string) (epKey, bool) {
	m := reEpKey.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return epKey{}, false
	}
	se, _ := strconv.Atoi(m[1])
	ep, _ := strconv.Atoi(m[2])
	return epKey{se, ep}, true
}

func sortEpKeys(ks []epKey) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].S != ks[j].S {
			return ks[i].S < ks[j].S
		}
		return ks[i].E < ks[j].E
	})
}

func marshalEpKeys(ks []epKey) string {
	ss := make([]string, len(ks))
	for i, k := range ks {
		ss[i] = k.String()
	}
	b, _ := json.Marshal(ss)
	return string(b)
}

func unmarshalEpKeys(s string) []epKey {
	var ss []string
	_ = json.Unmarshal([]byte(s), &ss)
	out := make([]epKey, 0, len(ss))
	for _, x := range ss {
		if k, ok := parseEpKey(x); ok {
			out = append(out, k)
		}
	}
	return out
}

// ==================== 已有：台账 ====================

// subHave 库里已有的
type subHave struct {
	TitleKeys []string // 命中的片目（台账 key，含库名前缀）；同一部放在两处时都算
	Found     bool     // 片目里至少有一个有效视频
	Eps       map[epKey]bool
}

// subHaveOf 台账里某个 TMDB 条目已有什么。
// 片目先按标题目录名里的编号认（parseTitleDir），老目录名不带编号的用 MediaLibrary 的落点反推。
// TMDB 的电影与剧集编号是两套，类型对不上的同号片目不算
func subHaveOf(db *gorm.DB, titles map[string]*ledgerTitleEntry, tmdbID int, mediaType string) subHave {
	h := subHave{Eps: map[epKey]bool{}}
	if db == nil || tmdbID <= 0 {
		return h
	}
	keys := map[string]bool{}
	for k, e := range titles {
		if e.TmdbID == tmdbID && e.MediaType == mediaType {
			keys[k] = true
		}
	}
	var libs []model.MediaLibrary
	db.Where("tmdb_id = ? AND media_type = ?", tmdbID, mediaType).Find(&libs)
	if len(libs) > 0 {
		layout := loadLibCategoryLayout()
		for _, l := range libs {
			if k := subLedgerKeyOf(titles, layout, l.TargetPath); k != "" {
				keys[k] = true
			}
		}
	}
	for k := range keys {
		h.TitleKeys = append(h.TitleKeys, k)
	}
	sort.Strings(h.TitleKeys)

	for _, key := range h.TitleKeys {
		var sfs []model.SyncedFile
		// 前缀查询不用 LIKE（片名里的 % _ 要转义）：'/' 的下一个字符是 '0'，
		// [key/, key0) 正好是 key 目录下的全部路径
		db.Where("kind = ? AND orphan_at IS NULL AND rel_path >= ? AND rel_path < ?",
			"video", key+"/", key+"0").Find(&sfs)
		for _, sf := range sfs {
			h.Found = true
			if mediaType != "tv" {
				continue
			}
			for _, k := range ledgerVideoEpisodes(key, sf.RelPath) {
				h.Eps[k] = true
			}
		}
	}
	return h
}

// subLedgerKeyOf MediaLibrary 的落点（库内相对路径，可能不带库名）→ 台账片目 key
func subLedgerKeyOf(titles map[string]*ledgerTitleEntry, layout libCategoryLayout, target string) string {
	key, _, _, _, ok := layout.titleOf(target)
	if !ok {
		return ""
	}
	if _, hit := titles[key]; hit {
		return key
	}
	// 落点不带库名、台账带：按「库名/key」认，只认唯一的一个
	found := ""
	for k := range titles {
		if strings.HasSuffix(k, "/"+key) {
			if found != "" {
				return ""
			}
			found = k
		}
	}
	return found
}

// ledgerVideoEpisodes 台账一个视频算哪几集。
// 文件名只有集号（季号是缺省的 1）时看所在目录：「Season 02/E05」是 S02E05，和整理同一个口径（applySeasonHint）
func ledgerVideoEpisodes(titleKey, relPath string) []epKey {
	p := parseFileName(strings.TrimSuffix(path.Base(relPath), ".strm"))
	if p == nil || p.Episode <= 0 {
		return nil
	}
	if dir := path.Dir(relPath); dir != titleKey && strings.HasPrefix(dir, titleKey+"/") {
		applySeasonHint(p, parseFileName(path.Base(dir)))
	}
	eps := p.episodeList()
	out := make([]epKey, 0, len(eps))
	for _, e := range eps {
		out = append(out, epKey{p.Season, e})
	}
	return out
}

// ==================== 在路上 ====================

// subInflight 已经提交、还没结算的尝试挑中的集
func subInflight(db *gorm.DB, subID uint) map[epKey]bool {
	out := map[epKey]bool{}
	if db == nil {
		return out
	}
	var rows []model.SubAttempt
	db.Where("sub_id = ? AND status = ?", subID, subAttemptInflight).Find(&rows)
	for _, r := range rows {
		for _, k := range unmarshalEpKeys(r.Episodes) {
			out[k] = true
		}
	}
	return out
}

// SubAttempt.Status
const (
	subAttemptInflight = "inflight"
	subAttemptIngested = "ingested"
	subAttemptPartial  = "partial"
	subAttemptRejected = "rejected"
	subAttemptFailed   = "failed"
	subAttemptUseless  = "useless"
)

// ==================== 应有：TMDB ====================

// subEpAir 范围内的一集与它的播出时间（零值 = TMDB 还没排期）
type subEpAir struct {
	Key epKey
	Air time.Time
}

// subTVSchedule 剧集范围内的全部集（含未播）
type subTVSchedule struct {
	Ended        bool // TMDB 状态是完结 / 取消
	LatestSeason int  // TMDB 上最新的正片季号：订阅某一季时，有更新的季说明这一季已经播完
	Eps          []subEpAir
}

// tmdbEnded TMDB 的剧集状态是否表示不会再有新集
func tmdbEnded(status string) bool {
	switch status {
	case "Ended", "Canceled", "Cancelled":
		return true
	}
	return false
}

// subSeasonsInScope 订阅范围涉及哪几季
func subSeasonsInScope(sub *model.Subscription, seasons map[int]int) []int {
	var out []int
	switch sub.Scope {
	case subScopeSeason, subScopeRange:
		if sub.Season >= 0 {
			out = append(out, sub.Season)
		}
	default:
		for s := range seasons {
			if s == 0 && !sub.Specials {
				continue
			}
			out = append(out, s)
		}
	}
	sort.Ints(out)
	return out
}

// subEpInScope 某一集在不在订阅范围里（季已经由 subSeasonsInScope 选过，这里只管集段）
func subEpInScope(sub *model.Subscription, k epKey) bool {
	if sub.Scope != subScopeRange {
		return true
	}
	if k.S != sub.Season || k.E < sub.EpStart {
		return false
	}
	return sub.EpEnd <= 0 || k.E <= sub.EpEnd
}

// parseTmdbDate TMDB 的 air_date / release_date（2023-01-15，或带时刻的 ISO 串）按本地 0 点算
func parseTmdbDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		if t, err := time.ParseInLocation("2006-01-02", s[:10], time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// fetchSubTVSchedule 拉剧集范围内每一集的播出日期。某一季拉不到就整体报错：
// 少一季算出来的「缺」是错的，宁可这一轮不算
func fetchSubTVSchedule(tc *TmdbClient, sub *model.Subscription) (subTVSchedule, error) {
	var sch subTVSchedule
	d, err := tc.detailOf("tv", sub.TmdbID)
	if err != nil {
		return sch, err
	}
	if d == nil {
		return sch, fmt.Errorf("TMDB 上没有这部剧（tv/%d）", sub.TmdbID)
	}
	sch.Ended = tmdbEnded(d.Status)
	for s := range d.SeasonEps {
		if s > sch.LatestSeason {
			sch.LatestSeason = s
		}
	}
	for _, s := range subSeasonsInScope(sub, d.SeasonEps) {
		if _, ok := d.SeasonEps[s]; !ok {
			continue // 订阅的季 TMDB 上还没有：当作还没排期
		}
		eps, _, err := tc.tmdbSeasonEpisodes(sub.TmdbID, s)
		if err != nil {
			return sch, fmt.Errorf("TMDB 第 %d 季集信息拉取失败: %w", s, err)
		}
		for n, e := range eps {
			k := epKey{s, n}
			if n <= 0 || !subEpInScope(sub, k) {
				continue
			}
			sch.Eps = append(sch.Eps, subEpAir{Key: k, Air: parseTmdbDate(e.AirDate)})
		}
	}
	sort.Slice(sch.Eps, func(i, j int) bool {
		a, b := sch.Eps[i].Key, sch.Eps[j].Key
		if a.S != b.S {
			return a.S < b.S
		}
		return a.E < b.E
	})
	return sch, nil
}

// subMovieRelease 电影的几种发行日期（零值 = 不知道）
type subMovieRelease struct {
	Theatrical time.Time // 院线（有限上映 / 正式上映里最早的）
	Digital    time.Time // 数字 / 实体 / 电视首播里最早的
}

// fetchSubMovieRelease 拉 /movie/{id}/release_dates，各地区取最早；
// 没有发行日期表时拿详情的 release_date 当院线日期
func fetchSubMovieRelease(tc *TmdbClient, tmdbID int) (subMovieRelease, error) {
	var rel subMovieRelease
	body, err := tc.get(fmt.Sprintf("/movie/%d/release_dates", tmdbID), nil)
	if err != nil && !isTmdbNotFound(err) {
		return rel, err
	}
	var r struct {
		Results []struct {
			ReleaseDates []struct {
				Type        int    `json:"type"`
				ReleaseDate string `json:"release_date"`
			} `json:"release_dates"`
		} `json:"results"`
	}
	if err == nil {
		_ = json.Unmarshal(body, &r)
	}
	earliest := func(cur, t time.Time) time.Time {
		if t.IsZero() || (!cur.IsZero() && !t.Before(cur)) {
			return cur
		}
		return t
	}
	for _, c := range r.Results {
		for _, d := range c.ReleaseDates {
			t := parseTmdbDate(d.ReleaseDate)
			switch d.Type {
			case 2, 3: // 有限上映 / 正式上映（1 是首映礼，不算）
				rel.Theatrical = earliest(rel.Theatrical, t)
			case 4, 5, 6: // 数字 / 实体 / 电视
				rel.Digital = earliest(rel.Digital, t)
			}
		}
	}
	if rel.Theatrical.IsZero() && rel.Digital.IsZero() {
		if d, err := tc.detailOf("movie", tmdbID); err == nil && d != nil {
			rel.Theatrical = parseTmdbDate(d.ReleaseDate)
		}
	}
	return rel, nil
}

// subMovieSearchFrom 电影从什么时候开始搜（零值 = 现在就能搜）。
// 数字发行模式下不知道数字发行日期的，按院线 + 90 天估；什么日期都没有的不拦
func subMovieSearchFrom(rel subMovieRelease, mode string) time.Time {
	switch mode {
	case subMovieWaitNow:
		return time.Time{}
	case subMovieWaitTheatrical:
		from := time.Time{}
		if !rel.Theatrical.IsZero() {
			from = rel.Theatrical.AddDate(0, 0, 45)
		}
		if !rel.Digital.IsZero() && (from.IsZero() || rel.Digital.Before(from)) {
			from = rel.Digital
		}
		return from
	default:
		if !rel.Digital.IsZero() {
			return rel.Digital
		}
		if !rel.Theatrical.IsZero() {
			return rel.Theatrical.AddDate(0, 0, 90)
		}
		return time.Time{}
	}
}

// ==================== 算缺 ====================

// subEval 一个订阅这一轮的盘点结果
type subEval struct {
	Have, Total int     // 范围内已播的集：已有 / 应有（电影是 0/1 或 1/1）
	Missing     []epKey // 已播、没有、不在路上的，排好序；电影缺时是一个零值占位
	// MissingAir 剧集缺集的播出日期（TMDB 只有日期，按当天 0 点）：离线要等新集播出一阵、分享没出来再下
	MissingAir map[epKey]time.Time
	Inflight   []epKey // 在路上的（只算范围内已播的）
	// NextAt 剧集：范围内下一集可以开始搜的时间；电影：可以开始搜的时间。零值 = 没有排期 / 现在就能搜
	NextAt time.Time
	Done   bool // 订阅可以结束了
	// Unaired 范围内还没播的集数（含没排期的）
	Unaired int
}

// evalSubTV 剧集盘点（纯函数）。
// 「已播」= 播出日期 + 延迟 ≤ now：TMDB 只有日期，资源一般几小时后才出现，延迟之前搜不到还白白退避
func evalSubTV(sub *model.Subscription, have subHave, sch subTVSchedule, inflight map[epKey]bool, now time.Time, delay time.Duration) subEval {
	var ev subEval
	var from time.Time
	if sub.Follow == subFollowNew && sub.FollowFrom != nil {
		f := sub.FollowFrom.In(time.Local)
		from = time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.Local)
	}
	for _, e := range sch.Eps {
		if !from.IsZero() && (e.Air.IsZero() || e.Air.Before(from)) {
			// 只追新集：订阅之前播的不要；没排期的还不知道算不算新，等排期出来再说
			if e.Air.IsZero() {
				ev.Unaired++
			}
			continue
		}
		if e.Air.IsZero() || e.Air.Add(delay).After(now) {
			ev.Unaired++
			if !e.Air.IsZero() {
				at := e.Air.Add(delay)
				if ev.NextAt.IsZero() || at.Before(ev.NextAt) {
					ev.NextAt = at
				}
			}
			continue
		}
		ev.Total++
		switch {
		case have.Eps[e.Key]:
			ev.Have++
		case inflight[e.Key]:
			ev.Inflight = append(ev.Inflight, e.Key)
		default:
			ev.Missing = append(ev.Missing, e.Key)
			if ev.MissingAir == nil {
				ev.MissingAir = map[epKey]time.Time{}
			}
			ev.MissingAir[e.Key] = e.Air
		}
	}
	sortEpKeys(ev.Missing)
	sortEpKeys(ev.Inflight)
	ev.Done = ev.Total > 0 && len(ev.Missing) == 0 && len(ev.Inflight) == 0 &&
		ev.Unaired == 0 && subScheduleComplete(sub, sch)
	return ev
}

// subScheduleComplete 范围内还会不会有新的集：
// 全剧要等 TMDB 标完结；某一季在有更新的一季时就算播完了；集段写明了结束集号的，集都有了就算完
func subScheduleComplete(sub *model.Subscription, sch subTVSchedule) bool {
	if sch.Ended {
		return true
	}
	switch sub.Scope {
	case subScopeSeason:
		return sch.LatestSeason > sub.Season
	case subScopeRange:
		if sub.EpEnd > 0 {
			for _, e := range sch.Eps {
				if e.Key.E == sub.EpEnd {
					return true
				}
			}
			return false
		}
		return sch.LatestSeason > sub.Season
	}
	return false
}

// evalSubMovie 电影盘点（纯函数）：有就完成；没有时看能不能开始搜
func evalSubMovie(have subHave, inflight bool, searchFrom, now time.Time) subEval {
	ev := subEval{Total: 1}
	switch {
	case have.Found:
		ev.Have, ev.Done = 1, true
	case inflight:
		ev.Inflight = []epKey{{}}
	case searchFrom.After(now):
		ev.Total, ev.Unaired, ev.NextAt = 0, 1, searchFrom
	default:
		ev.Missing = []epKey{{}}
	}
	return ev
}

// evaluateSubscription 盘点一个订阅：台账 + TMDB + 在路上。只读，不改订阅本身
func evaluateSubscription(db *gorm.DB, tc *TmdbClient, sub *model.Subscription, cfg subscribeCfg, now time.Time) (subEval, error) {
	have := subHaveOf(db, scanLedgerTitlesCached(), sub.TmdbID, sub.MediaType)
	inflight := subInflight(db, sub.ID)
	if sub.MediaType == "movie" {
		if have.Found {
			return evalSubMovie(have, false, time.Time{}, now), nil
		}
		var hasInflight bool
		if db != nil {
			var n int64
			db.Model(&model.SubAttempt{}).Where("sub_id = ? AND status = ?", sub.ID, subAttemptInflight).Count(&n)
			hasInflight = n > 0
		}
		rel, err := fetchSubMovieRelease(tc, sub.TmdbID)
		if err != nil {
			return subEval{}, err
		}
		return evalSubMovie(have, hasInflight, subMovieSearchFrom(rel, cfg.MovieWait), now), nil
	}
	sch, err := fetchSubTVSchedule(tc, sub)
	if err != nil {
		return subEval{}, err
	}
	return evalSubTV(sub, have, sch, inflight, now, cfg.airDelay()), nil
}
