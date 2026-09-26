package api

import (
	"fmt"
	"sort"
)

// ==================== 全剧连续编号的集号换算 ====================
//
// 有的资源按季分目录、季号也写对了，集号却是从第 1 季第 1 集一路数下来的：
// 「成长的烦恼.第7季/成长的烦恼.Growing.Pains.S07E166…」。Emby 照着 S07E166
// 去找第 7 季第 166 集，永远对不上。
//
// 参考项目都没有自动换算：MoviePilot 靠用户手配「集偏移」识别词（EP-144）或给条目
// 选 TMDB 剧集组；LitePan 整理时把绝对集号收进 Season 1（长篇动漫的做法），刮削时
// 还专门防着 TMDB 自己就按绝对集号编季内集号的剧（海贼王第 21 季从 892 编起）。
// 所以这里只在证据确凿时换算，任何一条核不准都原样保留（季目录是对的，至少不会比不换更糟）。
// 证据有两种，按可靠程度先后试：
//
//   - **相邻季**：同一个条目里上一季也在，且本季最小集号 = 上一季最大集号 + 1（且 > 1）。
//     这是资源自己的编号，不依赖 TMDB 的分季和资源一致 —— 现场《成长的烦恼》TMDB 各季集数
//     和这个资源的分法差了几集，只按 TMDB 累计的话第 3 季往后一季都对不上；
//   - **TMDB 各季集数**：上一季不在（单独转存了一季）时退回这条。本季每一集都要落在
//     「前几季累计集数」之后、本季之内，且至少一集超过本季集数（季内编号不可能出现）。
//
// 两种都要过同一道排除：TMDB 这一季自己的集号不是从 1 编起（海贼王式的季内绝对编号），
// 那文件上的大集号本来就是对的，不换。季详情取不到也不换。
// 判定按「一个条目里的同一季」整体做：一季里有一集说不通就整季不换，不逐集各换各的。

// absSeasonOffset 纯判定：season 这一季的集号 eps 是否是全剧连续编号，是则返回偏移
// （季内集号 = 文件集号 - 偏移）。counts 是 TMDB 各季集数
func absSeasonOffset(season int, eps []int, counts map[int]int) (int, bool) {
	if season < 2 || len(eps) == 0 {
		return 0, false
	}
	prior := 0
	for s := 1; s < season; s++ {
		if counts[s] <= 0 {
			return 0, false
		}
		prior += counts[s]
	}
	cur := counts[season]
	if cur <= 0 {
		return 0, false
	}
	over := false
	for _, e := range eps {
		if e <= prior || e > prior+cur {
			return 0, false
		}
		if e > cur {
			over = true
		}
	}
	return prior, over
}

// absRemap 一季的换算结果（日志与测试用）。Offset 为 0 时 Skip 写着为什么没换
type absRemap struct {
	Season, Offset, Count int
	By                    string // 相邻季 / TMDB 各季集数
	Skip                  string
}

// remapAbsEpisodes 就地换算一个条目里各集的解析结果（fid → 解析）。
// counts 是 TMDB 各季集数；seasonNums 取 TMDB 某季实际的集号，失败按「核不准」处理、这一季不换。
// 返回换了的季，以及「看着像连续编号却没换」的季（带原因）
func remapAbsEpisodes(eps map[string]*ParsedName, counts map[int]int,
	seasonNums func(season int) (map[int]bool, error)) []absRemap {
	bySeason := map[int][]*ParsedName{}
	for _, p := range eps {
		if p != nil && p.Season > 0 && p.Episode > 0 {
			bySeason[p.Season] = append(bySeason[p.Season], p)
		}
	}
	seasons := make([]int, 0, len(bySeason))
	orig := map[int][]int{} // 换算前的集号：相邻季要拿上一季的原始编号比
	for s, ps := range bySeason {
		seasons = append(seasons, s)
		for _, p := range ps {
			orig[s] = append(orig[s], p.Episode)
		}
		sort.Ints(orig[s])
	}
	sort.Ints(seasons)

	var out []absRemap
	for _, s := range seasons {
		nums := orig[s]
		if s < 2 || nums[0] <= 1 {
			continue // 第 1 季、或从 1 编起的季：季内编号，没什么可换的
		}
		r := absRemap{Season: s, Count: len(nums)}
		// 相邻季还要本季最大集号超过 TMDB 本季集数：「S01E01-E04 + S02E05-E10」这种残缺的季内编号
		// 首尾也正好接得上，只有「超出本季」才是季内编号不可能出现的硬证据
		exceeds := counts[s] <= 0 || nums[len(nums)-1] > counts[s]
		if prev := orig[s-1]; len(prev) > 0 && nums[0] == prev[len(prev)-1]+1 && exceeds {
			r.Offset, r.By = prev[len(prev)-1], "相邻季"
		} else if off, ok := absSeasonOffset(s, nums, counts); ok {
			r.Offset, r.By = off, "TMDB 各季集数"
		} else {
			if nums[len(nums)-1] > counts[s] {
				r.Skip = fmt.Sprintf("集号 E%d-E%d 超出本季集数，但上一季不在同一条目里、按 TMDB 各季集数（本季 %d 集）也对不上",
					nums[0], nums[len(nums)-1], counts[s])
				out = append(out, r)
			}
			continue
		}
		have, err := seasonNums(s)
		if err != nil {
			r.Offset, r.Skip = 0, "取不到 TMDB 这一季的集列表，无法排除季内绝对编号"
			out = append(out, r)
			continue
		}
		if tmdbNativeAbsolute(have) {
			r.Offset, r.Skip = 0, "TMDB 这一季自己就按全剧连续编号（如海贼王），文件集号本来就对"
			out = append(out, r)
			continue
		}
		for _, p := range bySeason[s] {
			p.Episode -= r.Offset
		}
		out = append(out, r)
	}
	return out
}

// tmdbNativeAbsolute TMDB 这一季的集号不是从 1 编起：季内集号延续上季（海贼王第 21 季从 892 起）
func tmdbNativeAbsolute(have map[int]bool) bool {
	if len(have) == 0 {
		return false
	}
	min := 0
	for e := range have {
		if min == 0 || e < min {
			min = e
		}
	}
	return min > 1
}

// remapAbsEpisodesTmdb 接真实 TMDB：各季集数取条目详情（识别校验时多半已缓存），
// 只有判定成立的季才再查一次这一季的集号
func remapAbsEpisodesTmdb(tc *TmdbClient, media *TmdbMedia, eps map[string]*ParsedName, onLog func(string)) {
	if tc == nil || media == nil || media.MediaType != "tv" || media.TmdbID <= 0 || len(eps) == 0 {
		return
	}
	maxEp := 0
	for _, p := range eps {
		if p != nil && p.Season >= 2 && p.Episode > maxEp {
			maxEp = p.Episode
		}
	}
	if maxEp == 0 {
		return // 没有第 2 季以后的集，不可能需要换算，省掉详情请求
	}
	d, err := tc.detailOf("tv", media.TmdbID)
	if err != nil || d == nil {
		return
	}
	for _, r := range remapAbsEpisodes(eps, d.SeasonEps, func(s int) (map[int]bool, error) {
		return tc.SeasonEpisodeNumbers(media.TmdbID, s)
	}) {
		if r.Skip != "" {
			onLog(fmt.Sprintf("○ 第 %d 季的 %d 集没有换算集号：%s", r.Season, r.Count, r.Skip))
			continue
		}
		onLog(fmt.Sprintf("▣ 第 %d 季的 %d 集是全剧连续编号，按%s换算为季内集号（集号减 %d：E%d → E01）",
			r.Season, r.Count, r.By, r.Offset, r.Offset+1))
	}
}
