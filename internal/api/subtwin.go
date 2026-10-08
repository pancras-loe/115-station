package api

// 订阅：同名的另一部。
//
// 2026-10-08 现场：订阅《凡人修仙传》(2020，动画)，挑中了「凡人修仙传 真人版.全集打包.2160p」的磁力。
// 剧集的相关性只看标题里有没有片名（resRelevant 不看剧集年份），整理时两部片名完全相等、同分，
// 包装目录名里的年份又把它推向了订阅的那一部 —— 于是真人版 30 集被认成动画、洗版顶掉了库里的 E01–E09，
// 结算也看不出来（整理记录的 TMDB 编号就是订阅的那一部）。整理那一环分不出来，只能在挑资源时拦住。
//
// 做法：按订阅的片名去 TMDB 搜剧集与电影，片名 / 原名归一后相等、编号不同的就是「同名的另一部」。
//   - 标题写着另一种形态（订阅动画、标题写真人版；订阅真人、同名的是动画、标题写动画）→ 不要；
//   - 有同名的，资源标题要拿得出证据才用：区分形态的字样、只属于订阅这一部的首播年份、
//     超出同名那部总集数的集号（「更新至194集」不可能是 30 集的真人版）。拿不出就跳过，原因写明。
// 不拿「标题里有同名那部的年份」当反证：连载中的动画标题常写当年年份（凡人修仙传 2025 更新至194集）。

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"sync"
	"time"

	"115-station/internal/model"
)

// tmdbGenreAnimation TMDB 的「动画」类型（电影与剧集同一个编号）
const tmdbGenreAnimation = 16

var (
	reResLiveAction = regexp.MustCompile(`真人|电视剧版|剧集版|(?i)\blive[\s.\-_]?action\b`)
	reResAnimated   = regexp.MustCompile(`动画|动漫|国漫|番剧|(?i)\banime\b`)
)

// subTwin 同名的另一部
type subTwin struct {
	ID       int
	Kind     string // movie / tv
	Title    string
	Year     string
	Anime    bool
	Episodes int // 正片总集数（电影 1；取不到为 0 = 不知道）
}

// subIdentity 订阅这一部是什么，以及有哪些同名的
type subIdentity struct {
	Anime bool
	Year  string
	Twins []subTwin
}

func (t subTwin) label() string {
	if t.Year != "" {
		return fmt.Sprintf("《%s》(%s)", t.Title, t.Year)
	}
	return "《" + t.Title + "》"
}

// subTwinVerdict 资源标题是不是订阅的这一部。ok=false 时 why 写给人看。cov 是标题估计的季集范围
func subTwinVerdict(title string, id subIdentity, cov resCoverage) (ok bool, why string) {
	live, animated := reResLiveAction.MatchString(title), reResAnimated.MatchString(title)
	twinAnime, twinLive := false, false
	for _, t := range id.Twins {
		if t.Anime {
			twinAnime = true
		} else {
			twinLive = true
		}
	}
	// 写明了另一种形态
	if id.Anime && live && !animated {
		return false, "标题写着真人版，订阅的是动画"
	}
	if !id.Anime && twinAnime && animated && !live {
		return false, "标题写着动画，订阅的不是动画"
	}
	if len(id.Twins) == 0 {
		return true, ""
	}
	// 证据一：区分形态的字样（同名的里有另一种形态才算数）
	if (id.Anime && animated && twinLive) || (!id.Anime && live && twinAnime) {
		return true, ""
	}
	// 证据二：标题年份是订阅这一部的首播年份，且不是任何同名那部的
	if id.Year != "" {
		for _, y := range reResYear.FindAllStringSubmatch(title, -1) {
			if y[1] != id.Year {
				continue
			}
			shared := false
			for _, t := range id.Twins {
				shared = shared || t.Year == id.Year
			}
			if !shared {
				return true, ""
			}
		}
	}
	// 证据三：标题写的集号超出了每一部同名的总集数
	if top := cov.EpHi; top > 0 {
		beyond := true
		for _, t := range id.Twins {
			if t.Episodes == 0 || top <= t.Episodes {
				beyond = false
				break
			}
		}
		if beyond {
			return true, ""
		}
	}
	return false, "同名的还有" + id.Twins[0].label() + "，标题看不出是哪一部"
}

// ==================== 取同名条目（TMDB，6 小时缓存）====================

var subIdentityCache = struct {
	sync.Mutex
	m map[string]subIdentityEntry
}{m: map[string]subIdentityEntry{}}

type subIdentityEntry struct {
	id subIdentity
	at time.Time
}

const subIdentityTTL = 6 * time.Hour

// subIdentityOf 订阅这一部的形态与同名条目：按片名搜一次剧集、一次电影，同名的各取一次详情数总集数
func subIdentityOf(tc *TmdbClient, sub *model.Subscription) (subIdentity, error) {
	key := fmt.Sprintf("%s/%d/%s", sub.MediaType, sub.TmdbID, sub.Title)
	subIdentityCache.Lock()
	if e, ok := subIdentityCache.m[key]; ok && time.Since(e.at) < subIdentityTTL {
		subIdentityCache.Unlock()
		return e.id, nil
	}
	subIdentityCache.Unlock()

	id := subIdentity{Year: sub.Year}
	names := map[string]bool{titleKey(sub.Title): true}
	if sub.OrigTitle != "" {
		names[titleKey(sub.OrigTitle)] = true
	}
	foundSelf := false
	for _, kind := range []string{"tv", "movie"} {
		cands, err := tc.searchCands(kind, map[string]string{"query": sub.Title})
		if err != nil {
			return id, fmt.Errorf("TMDB 搜同名条目失败：%w", err)
		}
		for _, c := range cands {
			anime := false
			for _, g := range c.GenreIDs {
				anime = anime || g == tmdbGenreAnimation
			}
			if kind == sub.MediaType && c.ID == sub.TmdbID {
				id.Anime, foundSelf = anime, true
				continue
			}
			if !names[titleKey(c.Title)] && !names[titleKey(c.Original)] {
				continue
			}
			t := subTwin{ID: c.ID, Kind: kind, Title: c.Title, Year: c.year(), Anime: anime, Episodes: 1}
			if kind == "tv" {
				t.Episodes = 0
				if d, err := tc.detailOf("tv", c.ID); err == nil && d != nil {
					for s, n := range d.SeasonEps {
						if s > 0 {
							t.Episodes += n
						}
					}
				}
			}
			id.Twins = append(id.Twins, t)
		}
	}
	if !foundSelf {
		// 搜索结果里没有自己（片名太常见、排得太后）：形态从详情取
		if m, err := tc.getByTmdbID(sub.TmdbID, sub.MediaType == "tv"); err == nil && m != nil {
			for _, g := range m.GenreIDs {
				id.Anime = id.Anime || g == tmdbGenreAnimation
			}
		}
	}
	if len(id.Twins) > 0 {
		var ls []string
		for _, t := range id.Twins {
			ls = append(ls, t.label()+" "+strconv.Itoa(t.Episodes)+" 集")
		}
		log.Printf("[订阅] ○ 《%s》有同名条目：%v，资源标题要能看出是哪一部才用", sub.Title, ls)
	}
	subIdentityCache.Lock()
	subIdentityCache.m[key] = subIdentityEntry{id: id, at: time.Now()}
	subIdentityCache.Unlock()
	return id, nil
}
