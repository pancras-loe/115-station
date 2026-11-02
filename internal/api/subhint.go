package api

// ==================== 订阅包装目录的核对 ====================
//
// 订阅提交的内容都放在包装目录「片名 (年份) S01 ·订阅<id>-<序号>」里（subWrapperName）。
// 包装目录名刻意不写 {tmdbid}：资源站上的内容可能是同名的另一部（subtwin.go），
// 写了就会被强制归进订阅那部。但订阅确实知道「要的是哪一部」，内容自己的名字和年份
// 又都对得上它时，不该再被搜索结果带偏 —— 2026-11-02 现场：订阅飞驰人生（tmdb 575219），
// 文件只叫 Pegasus.2019…，按 Pegasus 搜到的是《Pegasus: Pony With a Broken Wing》，
// 连着四个离线都进了那部片，订阅一直缺、一直再下。
//
// 所以这里只做一件事：内容自己（文件名、包装目录以外的各级目录）写的片名，和订阅那部的
// 标题 / 原名 / 别名 / 译名之一相等，年份也对得上，才按订阅那部认。证据一律不取包装目录，
// 否则包装目录的名字和年份是订阅那部的，任何内容都「对得上」。

import (
	"fmt"
	"regexp"
	"strconv"

	"115-station/internal/model"
)

var reSubWrapper = regexp.MustCompile(`·订阅(\d+)-\d+\s*$`)

// isSubWrapperName 这个目录名是不是订阅的包装目录
func isSubWrapperName(name string) bool {
	return reSubWrapper.MatchString(name)
}

// subWrapperIDOf 各级目录里订阅包装目录对应的订阅 id；没有返回 0
func subWrapperIDOf(dirNames []string) uint {
	for _, n := range dirNames {
		if m := reSubWrapper.FindStringSubmatch(n); m != nil {
			if id, err := strconv.ParseUint(m[1], 10, 64); err == nil {
				return uint(id)
			}
		}
	}
	return 0
}

// subEvidence 内容自己写的名字（文件名与包装目录以外的各级目录）
type subEvidence struct {
	title  string
	year   string
	season int // 明写的季号（猜的 1 不算）
}

func subEvidenceOf(file *ParsedName, dirs []*ParsedName, dirNames []string) []subEvidence {
	var out []subEvidence
	add := func(p *ParsedName) {
		if p == nil || !usableTitle(p.Title) {
			return
		}
		e := subEvidence{title: p.Title, year: p.Year}
		if !p.SeasonGuessed {
			e.season = p.Season
		}
		out = append(out, e)
	}
	add(file)
	for i, d := range dirs {
		if i < len(dirNames) && isSubWrapperName(dirNames[i]) {
			continue
		}
		add(d)
	}
	return out
}

// subYearFits 内容写的年份对得上订阅那部：电影、剧集首季差一年以内；
// 剧集后面的季年份是那一季的，只要求不早于首播
func subYearFits(e subEvidence, sub *model.Subscription) bool {
	if e.year == "" || sub.Year == "" {
		return false
	}
	if sub.MediaType == "tv" && e.season > 1 {
		ey, _ := strconv.Atoi(e.year)
		sy, _ := strconv.Atoi(sub.Year)
		return ey > 0 && sy > 0 && ey >= sy-1
	}
	return absYearDiff(e.year, sub.Year) <= 1
}

// subHintMatch 内容的证据里有没有一条名字、年份都对得上订阅那部；对得上返回那条证据的片名。纯函数
func subHintMatch(evidence []subEvidence, sub *model.Subscription, names []string) (string, bool) {
	for _, e := range evidence {
		if !subYearFits(e, sub) {
			continue
		}
		for _, q := range titleCandidates(e.title) {
			if titleLevelOf(q, names...) == titleExact {
				return q, true
			}
		}
	}
	return "", false
}

// subHint 订阅包装目录里的内容：识别结果（可能为 nil）不是订阅那部、而内容自己的名字和年份
// 都对得上订阅那部时，换成订阅那部。不在包装目录里、对不上、查不到订阅都原样返回。
// 只读一次订阅表与订阅那部的详情（详情有缓存）
func (tc *TmdbClient) subHint(media *TmdbMedia, file *ParsedName, dirs []*ParsedName, dirNames []string, onLog func(string)) *TmdbMedia {
	subID := subWrapperIDOf(dirNames)
	if subID == 0 || model.DB == nil {
		return media
	}
	var sub model.Subscription
	if model.DB.First(&sub, subID).Error != nil || sub.TmdbID <= 0 {
		return media
	}
	isTV := sub.MediaType == "tv"
	if media != nil && media.TmdbID == sub.TmdbID && (media.MediaType == "tv") == isTV {
		return media
	}
	kind := "movie"
	if isTV {
		kind = "tv"
	}
	d, err := tc.detailOf(kind, sub.TmdbID)
	if err != nil || d == nil {
		return media
	}
	names := append([]string{sub.Title, sub.OrigTitle}, d.Names...)
	q, ok := subHintMatch(subEvidenceOf(file, dirs, dirNames), &sub, names)
	if !ok {
		return media
	}
	sm, err := tc.getByTmdbID(sub.TmdbID, isTV)
	if err != nil || sm == nil {
		return media
	}
	sm.matchHow = "订阅核对"
	was := "没认出来"
	if media != nil {
		was = fmt.Sprintf("认成了「%s」(%s)", media.Title, media.Year)
	}
	onLog(fmt.Sprintf("✦ 订阅包装目录里的内容%s，但内容写的片名「%s」和年份对得上订阅的「%s」(%s)，按订阅那部认",
		was, q, sm.Title, sm.Year))
	return sm
}
