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
// 所以这里只在证据确凿时换算，下面几条全部成立才动，任何一条核不准都原样保留
// （季目录是对的，至少不会比不换更糟）：
//
//  1. 季号 ≥ 2（第 1 季的绝对编号和季内编号是一回事）；
//  2. 前面每一季在 TMDB 上都有集数，缺一季就算不出偏移；
//  3. 同一季里**每一集**都落在「前几季累计集数」之后、本季之内，且至少一集超过本季集数
//     —— 超过本季集数这一条在季内编号下不可能出现，是区分两种编号的唯一硬证据；
//  4. TMDB 这一季自己的集号里没有那些超出的数字（排除海贼王式的季内绝对编号）。
//
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

// absRemap 一次换算的结果（日志与测试用）
type absRemap struct {
	Season, Offset, Count int
}

// remapAbsEpisodes 就地换算一个条目里各集的解析结果（fid → 解析）。
// seasonNums 取 TMDB 某季实际的集号，失败按「核不准」处理、这一季不换
func remapAbsEpisodes(eps map[string]*ParsedName, counts map[int]int,
	seasonNums func(season int) (map[int]bool, error)) []absRemap {
	bySeason := map[int][]*ParsedName{}
	for _, p := range eps {
		if p != nil && p.Season > 0 && p.Episode > 0 {
			bySeason[p.Season] = append(bySeason[p.Season], p)
		}
	}
	seasons := make([]int, 0, len(bySeason))
	for s := range bySeason {
		seasons = append(seasons, s)
	}
	sort.Ints(seasons)

	var out []absRemap
	for _, s := range seasons {
		ps := bySeason[s]
		nums := make([]int, len(ps))
		for i, p := range ps {
			nums[i] = p.Episode
		}
		offset, ok := absSeasonOffset(s, nums, counts)
		if !ok {
			continue
		}
		have, err := seasonNums(s)
		if err != nil {
			continue
		}
		native := false
		for _, e := range nums {
			if e > counts[s] && have[e] {
				native = true // TMDB 自己这一季就是这么编号的
				break
			}
		}
		if native {
			continue
		}
		for _, p := range ps {
			p.Episode -= offset
		}
		out = append(out, absRemap{Season: s, Offset: offset, Count: len(ps)})
	}
	return out
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
		onLog(fmt.Sprintf("▣ 第 %d 季的 %d 集是全剧连续编号，按 TMDB 各季集数换算为季内集号（集号减 %d：E%d → E01）",
			r.Season, r.Count, r.Offset, r.Offset+1))
	}
}
