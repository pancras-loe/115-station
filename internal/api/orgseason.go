package api

import (
	"sort"
	"strings"
)

// ==================== 目录条目的落点：剧集按季分组 ====================
//
// 2026-09 之前 processDir 只拿主视频（体积最大的那个）算出一个季目录，整个条目的
// 视频与字幕一次 move 全搬进去、落盘也只认这一个目录 —— 多季合集不管每集的季号
// 识别得多对，全部挤进同一季（现场：《成长的烦恼》S01-S07 共 179 个视频全进 Season 1）。
// 同一个函数里的洗版判定、以及「重新整理」的 planRedoLayout 却一直是逐集算落点的，
// 三处口径不一致。现在三处都按每集自己的解析结果算，搬移按目录分组、每组一次写请求。
// 思路同 MoviePilot 的整理：每个文件单独经 MetaInfoPath 合并路径信息、单独算目标路径。

// episodeParses 条目里每个视频的季集解析（fid → 解析），整理全程只算这一次：
// 洗版判定、改名、落点都读它，连续编号换算（absepisode.go）也只需改这一份
func episodeParses(videos []remoteFile, rules []ReplaceRule, main *ParsedName) map[string]*ParsedName {
	out := make(map[string]*ParsedName, len(videos))
	for _, vf := range videos {
		out[vf.Fid] = parseVideoInDir(vf, rules, main)
	}
	return out
}

// pickMainVideo 识别用的样本视频。剧集目录里取带集号的里面最大的一个：
// 只看体积的话，合集里的剧场版 / 特别篇往往最大，拿它去搜就绕了远路
// （现场：拿《希瓦家归来》搜了 9 次都落空，最后靠目录名才认出来）。
// 带集号的不到两个（电影 + 花絮之类）时仍取最大的
func pickMainVideo(videos []remoteFile, rules []ReplaceRule) remoteFile {
	largest := func(vs []remoteFile) remoteFile {
		m := vs[0]
		for _, v := range vs[1:] {
			if v.Size > m.Size {
				m = v
			}
		}
		return m
	}
	var withEp []remoteFile
	for _, v := range videos {
		name := v.Name
		if len(rules) > 0 {
			name = applyReplaceRules(name, rules)
		}
		if parseFileName(name).Episode > 0 {
			withEp = append(withEp, v)
		}
	}
	if len(withEp) >= 2 {
		return largest(withEp)
	}
	return largest(videos)
}

// specialsRel 剧集里没有集号的视频（剧场版 / 特别篇）落到哪：模板有季变量就渲染第 0 季，
// 没有的话第 0 季渲染不出目录、路径甚至会退到分类根上 —— 只接受落在标题目录**下面**的结果，
// 其余一律 标题目录/Specials（Emby 认这个名字）。正常整理与重新整理共用
func specialsRel(media *TmdbMedia, base, rootRel string, p *ParsedName, name string) string {
	sp := *p
	sp.Season, sp.Episode = 0, 1
	rel := libSubPath(base, pathDir(buildNewNameWithTemplate(media, &sp, name)))
	if !strings.HasPrefix(rel, rootRel+"/") {
		rel = rootRel + "/Specials"
	}
	return rel
}

// hasEpisodes 这批解析里有没有带集号的（只有带集号的剧集条目，没集号的才算特别篇）
func hasEpisodes(eps map[string]*ParsedName) bool {
	for _, p := range eps {
		if p != nil && p.Season > 0 && p.Episode > 0 {
			return true
		}
	}
	return false
}

// orgPlacement 条目内视频 / 字幕的落点
type orgPlacement struct {
	relOf map[string]string // fid → 库内相对目录（含分类前缀）
	dirs  []string          // 用到的目录，排好序（建目录、搬移、落盘都按这个顺序）
}

// placeEntryFiles 纯计算：每个视频、字幕落到哪个库内目录。
//
//   - 电影：全部进 movieRel（与原来一致）；
//   - 剧集里带集号的：按自己的季号走模板，得到各自的季目录；
//   - 剧集里没有集号的（剧场版 / 特别篇 / 花絮），只要同条目里有别的带集号的，就放进
//     特别篇目录（模板有季变量就渲染第 0 季，没有就是标题目录下的 Specials）——
//     此前它们原名混进正片季目录，Emby 认不出，还占着正片的位置；
//     整个条目都没有集号时仍落在 fallbackRel（与原来一致）；
//   - 字幕跟同名视频走（先按补全改名映射一次），对不上的跟视频最多的那一组。
func placeEntryFiles(media *TmdbMedia, category, rootRel, fallbackRel string, videos, subs []remoteFile,
	eps map[string]*ParsedName, enrichRenames map[string]string) orgPlacement {
	pl := orgPlacement{relOf: map[string]string{}}
	base := categoryDir(media.MediaType, category)
	anyEp := false
	if media.MediaType == "tv" {
		for _, v := range videos {
			if p := eps[v.Fid]; p != nil && p.Season > 0 && p.Episode > 0 {
				anyEp = true
				break
			}
		}
	}
	for _, v := range videos {
		rel := fallbackRel
		if p := eps[v.Fid]; anyEp && p != nil {
			if p.Season > 0 && p.Episode > 0 {
				rel = libSubPath(base, pathDir(buildNewNameWithTemplate(media, p, v.Name)))
			} else {
				rel = specialsRel(media, base, rootRel, p, v.Name)
			}
		}
		pl.relOf[v.Fid] = rel
	}

	// 视频最多的那一组：认不出主人的字幕跟它走
	count := map[string]int{}
	for _, rel := range pl.relOf {
		count[rel]++
	}
	major := fallbackRel
	for rel, n := range count {
		if n > count[major] || (n == count[major] && rel < major) {
			major = rel
		}
	}
	for _, s := range subs {
		fb := baseName(s.Name)
		for oldB, newB := range enrichRenames {
			if fb == oldB || strings.HasPrefix(fb, oldB+".") {
				fb = newB + strings.TrimPrefix(fb, oldB)
				break
			}
		}
		rel, best := major, -1
		for _, v := range videos {
			vb := baseName(v.Name)
			// 前缀最长的那个是主人：「E1.ass」不能被「E1」和「E10」同时认领
			if (fb == vb || strings.HasPrefix(fb, vb+".")) && len(vb) > best {
				rel, best = pl.relOf[v.Fid], len(vb)
			}
		}
		pl.relOf[s.Fid] = rel
	}

	seen := map[string]bool{}
	for _, rel := range pl.relOf {
		if !seen[rel] {
			seen[rel] = true
			pl.dirs = append(pl.dirs, rel)
		}
	}
	sort.Strings(pl.dirs)
	return pl
}
