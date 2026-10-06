package api

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ==================== 特别篇按集名对号（S00Exx） ====================
//
// 剧集条目里没有集号的视频（剧场版 / 特别篇）此前一律原名进特别篇目录，Emby 认不出是第 0 季的哪一集。
// 但这类文件名里往往写着集名：「越狱特别篇：最后一越.Prison.Break.The.Final.Break.2009…」，
// TMDB 第 0 季第 9 集正是「最后一越 / The Final Break」（2026-10-06 现场）。
// 拿 TMDB 第 0 季的集名去文件名里找，找得到就记成 S00Exx，照集模板改名。
//
// 思路参考 Sonarr 的 ParsingService.ParseSpecialEpisodeTitle（解析不出集号时拿第 0 季各集标题
// 去发布名里找）；MoviePilot 不做这一步，靠用户手写识别词。
//
// 只做「宁可不认」的那一侧，认错的代价是 NFO 钉进 Emby：
//   - 只看第 0 季，只看没有集号的视频；
//   - 占位集名（第 9 集 / Episode 9）、泛称（特别篇 / Special）、太短的、被剧名本身包含的集名都不用；
//   - 一个文件对上几集取最长的集名，一样长就不认；两个文件对上同一集都不认（两个版本会改成同一个名）；
//   - 文件名写了年份、那一集也有播出日期时，相差超过一年不认；
//   - 已有文件明写占着的集号不再分配。
// 中文集名先比，没对上的再拿英文集名比一次（多一次 TMDB 请求，只在确有没对上的时候发）。

// specialTitleGeneric 特别篇里常见的泛称集名：几乎任何特别篇的文件名都带，拿来对号必然认错
var specialTitleGeneric = map[string]bool{
	"特别篇": true, "特別篇": true, "特辑": true, "特別編": true, "番外": true, "番外篇": true, "花絮": true,
	"幕后花絮": true, "预告": true, "预告片": true, "总集篇": true, "剧场版": true, "电影版": true,
	"special": true, "specials": true, "trailer": true, "recap": true, "extras": true,
	"behindthescenes": true, "makingof": true, "pilot": true, "unairedpilot": true, "movie": true,
}

// specialTitleKey 集名 / 文件名比对用的归一化：繁转简后走片名的 titleKey（只留字母数字、小写、全角转半角）
func specialTitleKey(s string) string { return titleKey(toSimplified(s)) }

// specialTitleUsable 这个集名够不够格拿去文件名里找
func specialTitleUsable(key string, showKeys []string) bool {
	if key == "" || specialTitleGeneric[key] {
		return false
	}
	// 纯 ASCII 至少 4 个字符，含中日韩文字至少 2 个字：太短的集名会在画质串、片名里撞上
	if n := utf8.RuneCountInString(key); n < 2 || (n == len(key) && n < 4) {
		return false
	}
	for _, sk := range showKeys {
		if sk != "" && strings.Contains(sk, key) {
			return false // 集名就是剧名（的一部分）：文件名里带剧名不说明任何事
		}
	}
	return true
}

// isSpecialEpisode 第 0 季带集号：文件名明写的 S00Exx，或按集名认出来的特别篇
func isSpecialEpisode(p *ParsedName) bool { return p != nil && p.Season == 0 && p.Episode > 0 }

// specialHit 一个特别篇的对号结果
type specialHit struct {
	Fid, Name, Title string
	Episode          int
}

// matchSpecialTitles 纯计算：给 eps 里没有集号的视频按第 0 季集名对号，命中的就地改成 S00Exx。
// names 是 fid → 原文件名；lookup 按语言取第 0 季的集信息（先 zh-CN，没对完再 en-US）
func matchSpecialTitles(eps map[string]*ParsedName, names map[string]string, showNames []string,
	lookup func(lang string) (map[int]tmdbEpisodeInfo, error)) []specialHit {
	var todo []string
	taken := map[int]bool{}
	for fid, p := range eps {
		if p == nil {
			continue
		}
		if p.Episode == 0 && names[fid] != "" {
			todo = append(todo, fid)
		} else if isSpecialEpisode(p) {
			for _, e := range p.episodeList() {
				taken[e] = true
			}
		}
	}
	if len(todo) == 0 {
		return nil
	}
	sort.Strings(todo) // 结果与日志顺序稳定
	showKeys := make([]string, 0, len(showNames))
	for _, n := range showNames {
		showKeys = append(showKeys, specialTitleKey(n))
	}

	var hits []specialHit
	for _, lang := range []string{"zh-CN", "en-US"} {
		if len(todo) == 0 {
			break
		}
		info, err := lookup(lang)
		if err != nil || len(info) == 0 {
			continue
		}
		claim := map[int][]string{} // 集号 → 对上它的文件
		pick := map[string]int{}
		for _, fid := range todo {
			fk := specialTitleKey(baseName(names[fid]))
			best, bestLen, tie := 0, 0, false
			for no, e := range info {
				if no <= 0 || taken[no] || reEpisodePlaceholderName.MatchString(e.Name) {
					continue
				}
				k := specialTitleKey(e.Name)
				if !specialTitleUsable(k, showKeys) || !strings.Contains(fk, k) {
					continue
				}
				if y, ay := eps[fid].Year, dateYear(e.AirDate); y != "" && ay != "" && absYearGap(y, ay) > 1 {
					continue
				}
				switch l := len(k); {
				case l > bestLen:
					best, bestLen, tie = no, l, false
				case l == bestLen && no != best:
					tie = true
				}
			}
			if best > 0 && !tie {
				pick[fid] = best
				claim[best] = append(claim[best], fid)
			}
		}
		var rest []string
		for _, fid := range todo {
			no, ok := pick[fid]
			if !ok || len(claim[no]) > 1 {
				rest = append(rest, fid)
				continue
			}
			p := eps[fid]
			p.Season, p.Episode, p.EpisodeEnd, p.SeasonGuessed, p.IsTV = 0, no, 0, false, true
			taken[no] = true
			hits = append(hits, specialHit{Fid: fid, Name: names[fid], Title: info[no].Name, Episode: no})
		}
		todo = rest
	}
	return hits
}

// absYearGap 两个年份字符串相差几年，解析不了按 0
func absYearGap(a, b string) int {
	var x, y int
	if _, err := fmt.Sscanf(a, "%d", &x); err != nil {
		return 0
	}
	if _, err := fmt.Sscanf(b, "%d", &y); err != nil {
		return 0
	}
	if x > y {
		return x - y
	}
	return y - x
}

// matchSpecialsTmdb 接真实 TMDB：第 0 季集信息走刮削同一份季集缓存（6 小时），没有待对号的视频就不请求
func matchSpecialsTmdb(tc *TmdbClient, media *TmdbMedia, eps map[string]*ParsedName, names map[string]string, onLog func(string)) {
	if tc == nil || media == nil || media.MediaType != "tv" || media.TmdbID <= 0 || len(eps) == 0 {
		return
	}
	hits := matchSpecialTitles(eps, names, []string{media.Title, media.OriginalTitle},
		func(lang string) (map[int]tmdbEpisodeInfo, error) {
			m, _, err := tc.tmdbSeasonEpisodesLang(media.TmdbID, 0, lang)
			if err != nil {
				onLog(fmt.Sprintf("○ 取 TMDB 第 0 季集名（%s）失败，特别篇不对号：%v", lang, err))
			}
			return m, err
		})
	for _, h := range hits {
		onLog(fmt.Sprintf("▣ 特别篇按集名对上 S00E%02d「%s」：%s", h.Episode, h.Title, h.Name))
	}
}

// videoNames fid → 文件名
func videoNames(videos []remoteFile) map[string]string {
	out := make(map[string]string, len(videos))
	for _, v := range videos {
		out[v.Fid] = v.Name
	}
	return out
}
