package api

// ==================== AI 增强识别：识别链的最后一环 ====================
//
// 只有前面所有规则环节（id 标签、识别记忆、TMDB 搜索与候选校验、清洗重试、中英拆分）
// 都落空、而且「AI 增强识别」开着时才走到这里。每个条目最多调两次模型：
//
//  1. 改写：给模型原始文件名 + 所在各级目录，让它说出片名（中文名、原名）、年份、类型、季集，
//     再拿这些去搜 TMDB —— 搜出来的结果照样要过 choose 的片名校验，模型编的片名进不了库；
//  2. 挑选：改写也搜不中时，把 TMDB 搜到、但片名校验没通过的前几名候选交给模型，
//     让它选一个或者回答「都不是」。只能从真实存在的条目里选。
//
// 两条路出来的结果都带一个 0-100 的分数（aiScore）：模型自评的把握度，
// 再按年份、类型、片名这些能核实的信号封顶。分数决定要不要停下来等人工确认（aiHoldReason）。

import (
	"fmt"
	"log"
	"strings"
)

// 识别出处
const (
	viaAITitle = "ai_title" // 模型改写片名后搜中
	viaAIPick  = "ai_pick"  // 模型从候选里选中
)

// aiCandidateLimit 交给模型挑的候选上限：太多了模型容易看花，也白费 token
const aiCandidateLimit = 6

// recognizeByAI AI 这一环。没识别出来返回 (nil, nil)；TMDB 请求出错返回错误（瞬时失败，下轮重试）
func (tc *TmdbClient) recognizeByAI(cfg *aiRecognizeCfg, parsed *ParsedName) (*TmdbMedia, error) {
	g := aiGuessTitle(cfg, parsed)
	var queries []string
	if g != nil {
		log.Printf("[AI识别] 判断: %s → %q / %q (%s) 类型=%s S%dE%d 把握 %d",
			shortLogName(orDash(parsed.Source)), g.Title, g.OriginalTitle, orDash(g.Year),
			orDash(g.Type), g.Season, g.Episode, g.Confidence)
		media, err := tc.searchAIGuess(parsed, g)
		if err != nil || media != nil {
			return media, err
		}
		queries = append(queries, g.Title, g.OriginalTitle)
	}

	// 改写没搜中：把能搜到的候选交给模型挑。查询词先用模型给的片名，再用解析器的
	queries = append(queries, titleCandidates(parsed.Title)...)
	isTV := parsed.IsTV || (g != nil && g.Type == "tv")
	cands, err := tc.gatherAICandidates(queries, isTV)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, nil
	}
	idx, conf, reason := aiPickCandidate(cfg, parsed, cands)
	if idx < 0 {
		log.Printf("[AI识别] ○ %d 个候选都不是：%s", len(cands), orDash(reason))
		return nil, nil
	}
	pick := cands[idx]
	media, err := tc.getByTmdbID(pick.C.ID, pick.Kind == "tv")
	if err != nil || media == nil {
		return nil, err
	}
	out := *media
	out.Via = viaAIPick
	out.AIScore, out.AINote = aiScore(parsed, &out, conf)
	if reason != "" {
		out.AINote = "模型理由：" + reason + "；" + out.AINote
	}
	log.Printf("[AI识别] ✓ 从 %d 个候选里选中 %s (%s) [%s/%d]，%d 分", len(cands),
		out.Title, out.Year, out.MediaType, out.TmdbID, out.AIScore)
	return &out, nil
}

// searchAIGuess 拿模型给的片名去搜。中文名、原名依次试，类型按模型说的搜；
// 模型补出了文件名里没有的季集号时一并写回 parsed（后面的命名要用）
func (tc *TmdbClient) searchAIGuess(parsed *ParsedName, g *aiTitleGuess) (*TmdbMedia, error) {
	year := g.Year
	if year == "" {
		year = parsed.Year
	}
	season := parsed.Season
	if g.Season > 0 && (season == 0 || parsed.SeasonGuessed) {
		season = g.Season
	}
	for _, q := range dedupeTitles(g.Title, g.OriginalTitle) {
		var media *TmdbMedia
		var err error
		switch {
		case g.Type == "tv" || (g.Type == "" && parsed.IsTV):
			media, err = tc.searchTVSeason(q, year, season)
		case g.Type == "movie":
			media, err = tc.SearchMovie(q, year)
		default:
			if media, err = tc.SearchMovie(q, year); err == nil && media == nil {
				media, err = tc.SearchTV(q, year)
			}
		}
		if err != nil {
			return nil, err
		}
		if media == nil {
			continue
		}
		if media.MediaType == "tv" {
			if season != parsed.Season {
				parsed.Season, parsed.SeasonGuessed, parsed.IsTV = season, false, true
			}
			if parsed.Episode == 0 && g.Episode > 0 {
				parsed.Episode = g.Episode
			}
		}
		// 搜索结果是缓存里的同一个指针，标注要打在副本上，否则会串到下一次非 AI 的识别里
		out := *media
		out.Via = viaAITitle
		out.AIScore, out.AINote = aiScore(parsed, &out, g.Confidence)
		log.Printf("[AI识别] ✓ 按模型给的片名 %q 搜中 %s (%s) [%s/%d]，%d 分",
			q, out.Title, out.Year, out.MediaType, out.TmdbID, out.AIScore)
		return &out, nil
	}
	return nil, nil
}

// gatherAICandidates 收集候选：每个查询词搜一页（剧集优先搜 TV，否则电影、剧集都搜），
// 按 TMDB 的相关度顺序去重取前几名。这里要的恰恰是「片名校验没通过」的那些，
// 所以直接看搜索原始结果，不经过 choose
func (tc *TmdbClient) gatherAICandidates(queries []string, isTV bool) ([]aiCandidate, error) {
	kinds := []string{"movie", "tv"}
	if isTV {
		kinds = []string{"tv", "movie"}
	}
	seen := map[string]bool{}
	var out []aiCandidate
	for _, q := range dedupeTitles(queries...) {
		for _, kind := range kinds {
			cands, err := tc.searchCands(kind, map[string]string{"query": q})
			if err != nil {
				return nil, err
			}
			// 每页只取前 3：相关度靠后的基本是噪声，而且要给别的查询词留位置
			for i, c := range cands {
				if i >= 3 || len(out) >= aiCandidateLimit {
					break
				}
				k := fmt.Sprintf("%s/%d", kind, c.ID)
				if !seen[k] {
					seen[k] = true
					out = append(out, aiCandidate{Kind: kind, C: c})
				}
			}
			if len(out) >= aiCandidateLimit {
				return out, nil
			}
		}
	}
	return out, nil
}

// dedupeTitles 按归一化片名去重（"Dune" 与 "dune" 算同一个查询词）
func dedupeTitles(ss ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" || seen[titleKey(s)] {
			continue
		}
		seen[titleKey(s)] = true
		out = append(out, s)
	}
	return out
}

// aiScore AI 判定结果的分数（0-100）与打分依据。
//
// 模型自评的把握度只是起点 —— 它对自己编的东西也可能很有信心。
// 能从文件名里核实的信号对不上时，分数封顶，而不是加减：
// 一条硬伤就足以说明这次判断不可靠，把握度再高也不该绕过人工确认
func aiScore(parsed *ParsedName, media *TmdbMedia, confidence int) (int, string) {
	var notes []string
	score := confidence
	if score <= 0 {
		score = 60
		notes = append(notes, "模型没给把握度，按 60 计")
	} else {
		notes = append(notes, fmt.Sprintf("模型自评 %d", confidence))
	}
	capAt := func(limit int, why string) {
		if score > limit {
			score = limit
		}
		notes = append(notes, fmt.Sprintf("%s，最高 %d", why, limit))
	}

	// 年份：非首季剧集的年份是那一季的，不比首播年份
	seasonYear := parsed.IsTV && parsed.Season > 1
	if parsed.Year != "" && media.Year != "" && !seasonYear {
		if d := absYearDiff(parsed.Year, media.Year); d > 1 {
			capAt(50, fmt.Sprintf("年份与文件名相差 %d 年", d))
		}
	}
	// 类型：文件名里有季集号，却判成了电影
	if parsed.IsTV && parsed.Episode > 0 && media.MediaType == "movie" {
		capAt(60, "文件名有集号却判成电影")
	}
	// 片名：模型从候选里挑的，或者搜出来只是「相近」的，看看和文件名到底沾不沾边
	switch {
	case media.Via == viaAITitle && strings.Contains(media.matchHow, "相近"):
		capAt(70, "TMDB 片名与模型给的片名只是相近")
	case media.Via == viaAIPick && parsed.Title != "" &&
		titleLevel(titleKey(parsed.Title), media.Title, media.OriginalTitle) == titleNone:
		capAt(75, "片名与文件名对不上，全凭模型判断")
	}
	return min(max(score, 0), 100), strings.Join(notes, "；")
}

// aiHoldReason AI 判定出来的结果要不要停下来等人工确认；要的话返回给用户看的原因，不要返回空。
// 规则环节识别出来的（Via 为空）一律不管 —— 那条路原来怎么走还怎么走
func aiHoldReason(cfg *aiRecognizeCfg, media *TmdbMedia) string {
	if cfg == nil || media == nil || (media.Via != viaAITitle && media.Via != viaAIPick) {
		return ""
	}
	switch cfg.ConfirmMode {
	case aiConfirmOff:
		return ""
	case aiConfirmForce:
		return fmt.Sprintf("AI 判定（%d 分），按设置一律等人工确认", media.AIScore)
	default:
		if media.AIScore >= cfg.MinScore {
			return ""
		}
		return fmt.Sprintf("AI 判定 %d 分，低于自动入库线 %d 分，等人工确认", media.AIScore, cfg.MinScore)
	}
}
