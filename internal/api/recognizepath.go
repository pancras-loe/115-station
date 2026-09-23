package api

// ==================== 识别的路径上下文 ====================
//
// 文件名只是识别的一份证据。「狂飙 (2023)/Season 2/E05.mkv」里，片名和年份在顶层目录上、
// 季号在子目录上、集号才在文件名上 —— 只看文件名什么都识别不出来，只看顶层目录又会把
// 第二季当成第一季。这里把文件、各级子目录、顶层目录、再往上的容器目录由近及远合成一份。
// 思路参考 MoviePilot app/core/metainfo.py 的 MetaInfoPath（文件 / 父目录 / 祖父目录三层合并），
// 实现是自己写的。

import (
	"strings"
)

// genericDirNames 只是分类、不是片名的目录名：拿它们去搜 TMDB 只会搜出同名的片子
var genericDirNames = map[string]bool{
	"电影": true, "影片": true, "电视剧": true, "剧集": true, "连续剧": true, "动漫": true, "动画": true,
	"番剧": true, "综艺": true, "纪录片": true, "合集": true, "未整理": true, "下载": true, "视频": true,
	"movie": true, "movies": true, "film": true, "films": true, "tv": true, "tv shows": true, "shows": true,
	"series": true, "anime": true, "video": true, "videos": true, "download": true, "downloads": true,
	"specials": true, "extras": true, "featurettes": true, "sample": true, "samples": true,
	"花絮": true, "特典": true, "特别篇": true, "sp": true, "bdmv": true, "stream": true,
}

// usableTitle 这个片名值得拿去搜：不空、不是纯集号、不是分类目录名
func usableTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t != "" && !isEpisodeOnly(t) && !genericDirNames[strings.ToLower(t)]
}

// pathDirs 文件所在的各级目录名，由近及远。path 是 collectDirFiles 给的相对路径
// （顶层目录名/子目录/…），extra 是更外层的目录（容器目录名）
func pathDirs(path string, extra ...string) []string {
	var out []string
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			out = append(out, p)
		}
	}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// parseDirs 逐级解析目录名（替换规则一并套用，与文件名同一套预处理）
func parseDirs(dirs []string, rules []ReplaceRule) []*ParsedName {
	out := make([]*ParsedName, 0, len(dirs))
	for _, d := range dirs {
		if len(rules) > 0 {
			d = applyReplaceRules(d, rules)
		}
		out = append(out, parseFileName(d))
	}
	return out
}

// mergePathContext 用目录补全文件名缺的信息，返回合并后的结果与片名的出处（"" = 文件名本身）。
//
//   - 片名：文件名的能用就用文件名的；否则取最近一级能用的目录名（跳过 Season 2、合集、Movies 这类）。
//   - 年份：片名从哪来就跟着哪来；片名来自文件名时，只接受片名对得上的目录上的年份 ——
//     「欧美电影 2023 合集/Dune.mkv」的 2023 不是《沙丘》的年份。
//   - 季号：文件名只有集号（季号是猜的 1）时，取最近一级明写了季号的目录。
//   - 剧集判定：任何一级目录明写了季号，或片名来自一个剧集样的目录，就按剧集搜。
//   - id 标签：文件名没有就取最近一级带标签的目录。
//
// 不覆盖文件名已有的东西：文件名是离内容最近的证据
func mergePathContext(file *ParsedName, dirs []*ParsedName, dirNames []string) (*ParsedName, string) {
	out := *file
	from := ""
	if !usableTitle(out.Title) {
		for i, d := range dirs {
			if usableTitle(d.Title) {
				out.Title, from = d.Title, dirNames[i]
				if out.Year == "" {
					out.Year = d.Year
				}
				if d.IsTV && d.Season == 0 && d.Episode == 0 {
					out.IsTV = true // 「狂飙 全39集」这种目录
				}
				break
			}
		}
	}
	if out.Year == "" && from == "" {
		key := titleKey(out.Title)
		for _, d := range dirs {
			if d.Year != "" && d.Title != "" && titleLevel(key, d.Title) != titleNone {
				out.Year = d.Year
				break
			}
		}
	}
	for _, d := range dirs {
		if d.Season > 0 && !d.SeasonGuessed {
			applySeasonHint(&out, d)
			out.IsTV = true
			break
		}
	}
	if out.TmdbID == 0 {
		for _, d := range dirs {
			if d.TmdbID > 0 {
				out.TmdbID, out.TmdbKind = d.TmdbID, d.TmdbKind
				break
			}
		}
	}
	return &out, from
}

// seasonHintFromPath 文件所在各级目录里最近的一个明写的季号（没有返回 nil）。
// 同一部剧的 Season 1/、Season 2/ 子目录里文件都叫 E01.mkv，季号只能从各自的目录上取
func seasonHintFromPath(path string, rules []ReplaceRule) *ParsedName {
	for _, d := range parseDirs(pathDirs(path), rules) {
		if d.Season > 0 && !d.SeasonGuessed {
			return d
		}
	}
	return nil
}

// parseVideoInDir 目录里某一集的季集解析：文件名（套替换规则）→ 所在子目录的季号 → 整个条目识别时的季号
func parseVideoInDir(vf remoteFile, rules []ReplaceRule, main *ParsedName) *ParsedName {
	name := vf.Name
	if len(rules) > 0 {
		name = applyReplaceRules(name, rules)
	}
	p := parseFileName(name)
	applySeasonHint(p, seasonHintFromPath(vf.Path, rules))
	if p.Season == 0 && main != nil {
		p.Season = main.Season
	}
	applySeasonHint(p, main)
	return p
}
