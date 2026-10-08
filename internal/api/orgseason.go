package api

import (
	"regexp"
	"sort"
	"strconv"
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
	names := make(map[string]string, len(videos))
	for _, vf := range videos {
		names[vf.Fid] = vf.Name
	}
	fillEpisodesFromSiblings(names, out)
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
//   - 第 0 季带集号的（明写 S00Exx、或 matchSpecialsTmdb 按集名认出来的）进特别篇目录；
//   - 剧集里没有集号的（剧场版 / 特别篇 / 花絮），只要同条目里有别的带集号的，就放进
//     特别篇目录（模板有季变量就渲染第 0 季，没有就是标题目录下的 Specials）——
//     此前它们原名混进正片季目录，Emby 认不出，还占着正片的位置；
//     整个条目都没有集号时仍落在 fallbackRel（与原来一致）；
//   - 字幕跟同名视频走，对不上的跟视频最多的那一组。
func placeEntryFiles(media *TmdbMedia, category, rootRel, fallbackRel string, videos, subs []remoteFile,
	eps map[string]*ParsedName) orgPlacement {
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
		if p := eps[v.Fid]; media.MediaType == "tv" && isSpecialEpisode(p) {
			rel = specialsRel(media, base, rootRel, p, v.Name) // 认出集号的特别篇（S00E09）：改名，落点同特别篇
		} else if anyEp && p != nil {
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

	pl.collectDirs()
	return pl
}

// collectDirs 按 relOf 重算用到的目录
func (pl *orgPlacement) collectDirs() {
	pl.dirs = pl.dirs[:0]
	seen := map[string]bool{}
	for _, rel := range pl.relOf {
		if !seen[rel] {
			seen[rel] = true
			pl.dirs = append(pl.dirs, rel)
		}
	}
	sort.Strings(pl.dirs)
}

// ==================== NFO / 图片的落点：剧 / 季 / 集三级 ====================
//
// 此前 NFO 与封面一律进标题目录：每集自带的「集名.nfo」被从季目录里拎出来，
// 几季的集 NFO 全堆在「海绵宝宝/」下面，Emby 读不到（它只在视频旁边找同名 NFO）。
// 现在按 Emby / Kodi 的约定分三级，与 MoviePilot app/chain/media.py 刮削时的落点一致：
//   - 剧：tvshow.nfo、poster / fanart / seasonXX-poster 等 → 标题目录
//   - 季：season.nfo、纯季目录（Season 1 / S01 / 第1季）里没有主人的通用图片 → 季目录
//   - 集：与某个视频同基名的 xxx.nfo、xxx-thumb.jpg → 跟那个视频走

// assetOwner 附属文件（字幕 / NFO / 图片）的主人视频在 bases 里的下标，没有返回 -1。
// 基名等于视频基名、或以「视频基名.」「视频基名-」开头都算（xxx.chs.ass、xxx.nfo、xxx-thumb.jpg）。
// 对得上多个时取最长的：「E1.nfo」不能被「E1」和「E10」同时认领
func assetOwner(assetBase string, bases []string) int {
	owner, best := -1, -1
	for i, vb := range bases {
		if vb == "" || len(vb) <= best {
			continue
		}
		if assetBase == vb || strings.HasPrefix(assetBase, vb+".") || strings.HasPrefix(assetBase, vb+"-") {
			owner, best = i, len(vb)
		}
	}
	return owner
}

// isSeasonOnlyDir 目录名是不是纯季目录（Season 1 / S01 / 第1季），带片名的「某剧 第1季」不算：
// 那种目录往往就是整部剧的顶层，里面的 poster.jpg 是剧的海报
func isSeasonOnlyDir(name string, rules []ReplaceRule) bool {
	if len(rules) > 0 {
		name = applyReplaceRules(name, rules)
	}
	p := parseFileName(name)
	return p.Season > 0 && !p.SeasonGuessed && p.Episode == 0 && !usableTitle(p.Title)
}

// isSeasonLevelMeta 没有主人视频的 NFO / 图片是不是季一级的。dir 是它所在的源目录名
func isSeasonLevelMeta(name, dir string, rules []ReplaceRule) bool {
	lower := strings.ToLower(name)
	if lower == "season.nfo" {
		return true
	}
	if classifyFile(name) != FileTypeStdImage || reSeasonImg.MatchString(baseName(lower)) {
		return false // seasonXX-poster.jpg 按约定放在标题目录
	}
	return isSeasonOnlyDir(dir, rules)
}

// metaVideo 算 NFO / 图片落点时需要的视频信息
type metaVideo struct {
	base string // 视频基名（与附属文件同一时刻的名字：都改名前、或都改名后）
	dir  string // 所在的源目录（remoteFile.Path / orgRecordFile.Dir）
	rel  string // 视频的库内落点
}

// metaRel 剧集条目里一个 NFO / 图片的库内落点，"" 表示标题目录
func metaRel(name, dir string, vids []metaVideo, rules []ReplaceRule) string {
	bases := make([]string, len(vids))
	for i, v := range vids {
		bases[i] = v.base
	}
	if i := assetOwner(baseName(name), bases); i >= 0 {
		return vids[i].rel
	}
	if !isSeasonLevelMeta(name, pathBase(dir), rules) {
		return ""
	}
	// 季一级的：跟同一个源目录里的视频走；那个目录里没有视频（季 NFO 单放一处）就跟视频最多的季
	count := map[string]int{}
	for _, v := range vids {
		if v.dir == dir {
			count[v.rel]++
		}
	}
	if len(count) == 0 {
		for _, v := range vids {
			count[v.rel]++
		}
	}
	major := ""
	for rel, n := range count {
		if major == "" || n > count[major] || (n == count[major] && rel < major) {
			major = rel
		}
	}
	return major
}

// placeMeta 把剧集条目里的 NFO / 图片分到集、季目录（记进 relOf），剧一级的不记 —— 调用方另行放进标题目录。
// 电影不分：视频本来就在标题目录里
func (pl *orgPlacement) placeMeta(media *TmdbMedia, rootRel string, videos, metas []remoteFile,
	rules []ReplaceRule) {
	if media.MediaType != "tv" || len(metas) == 0 {
		return
	}
	vids := make([]metaVideo, 0, len(videos))
	for _, v := range videos {
		vids = append(vids, metaVideo{base: baseName(v.Name), dir: v.Path, rel: pl.relOf[v.Fid]})
	}
	for _, m := range metas {
		if rel := metaRel(m.Name, m.Path, vids, rules); rel != "" && rel != rootRel {
			pl.relOf[m.Fid] = rel
		}
	}
	pl.collectDirs()
}

// reLastDigits 名字里最后一段数字（集号模板的「#」）
var reLastDigits = regexp.MustCompile(`\d+`)

// siblingTemplateMin 至少这么多个兄弟视频套着同一个模板、且那段数字就是集号，才拿它补没集号的
const siblingTemplateMin = 3

// lastDigitsTemplate 去掉扩展名后把最后一段数字换成 #：「蜡笔小新第二季-712.mp4」→「蜡笔小新第二季-#」
func lastDigitsTemplate(name string) (tpl, digits string) {
	name = baseName(name) // 扩展名里的数字（.mp4）不算
	locs := reLastDigits.FindAllStringIndex(name, -1)
	if len(locs) == 0 {
		return "", ""
	}
	l := locs[len(locs)-1]
	return name[:l[0]] + "#" + name[l[1]:], name[l[0]:l[1]]
}

// digitTemplate 名字里某一段数字换成 # 之后的模板
type digitTemplate struct{ tpl, digits string }

// digitTemplates 去掉扩展名后，每一段数字各出一个模板（按出现顺序）。
// 补集号不能只看最后一段：「08.2160p.60fps.HD国语中字无水印[最新电影www.5266ys.com].mp4」最后一段是
// 广告域名里的 5266，模板永远对不上，10–30 集因此没补上集号、全进了 Season 0（2026-10-08 现场）
func digitTemplates(name string) []digitTemplate {
	name = baseName(name)
	locs := reLastDigits.FindAllStringIndex(name, -1)
	out := make([]digitTemplate, 0, len(locs))
	for _, l := range locs {
		out = append(out, digitTemplate{name[:l[0]] + "#" + name[l[1]:], name[l[0]:l[1]]})
	}
	return out
}

// fillEpisodesFromSiblings 同一条目里按兄弟视频的命名模板补集号。
// plausibleEpisode 单看一个名字时把 480 / 576 / 720 / 1080 当分辨率排除，这本身没错
// （「某剧 - 1080.mkv」多半是画质）；但 873 集的「蜡笔小新第二季-NNN.mp4」里，
// -480 / -576 / -720 三集因此没了集号，被当特别篇丢进 Season 0、保持原名（2026-10-04 现场）。
// 一批兄弟都是「同一模板 + 那段数字 = 集号」时，这段数字在它身上同样是集号。
// names 用原始文件名（替换规则只用于解析，模板按用户眼里的名字比对就够了）
func fillEpisodesFromSiblings(names map[string]string, parses map[string]*ParsedName) {
	agree := map[string]int{} // 模板 → 数字就是集号的兄弟数
	taken := map[[2]int]bool{} // 已被兄弟占用的 季,集：补出来撞上的不补，否则批量改名会撞名
	for fid, p := range parses {
		if p != nil && p.Episode > 0 {
			for _, ep := range p.episodeList() {
				taken[[2]int{p.Season, ep}] = true
			}
		}
		if p == nil || p.Episode <= 0 || p.EpisodeEnd > 0 {
			continue
		}
		// 哪一段数字就是集号，那一段的模板记一票（一个名字里只认第一段对得上的）
		for _, t := range digitTemplates(names[fid]) {
			if n, err := strconv.Atoi(t.digits); err == nil && n == p.Episode {
				agree[t.tpl]++
				break
			}
		}
	}
	hasTpl := func(name, tpl string) bool {
		for _, t := range digitTemplates(name) {
			if t.tpl == tpl {
				return true
			}
		}
		return false
	}
	for fid, p := range parses {
		if p == nil || p.Episode > 0 {
			continue
		}
		tpl, n := "", 0
		for _, t := range digitTemplates(names[fid]) {
			if agree[t.tpl] < siblingTemplateMin {
				continue
			}
			if v, err := strconv.Atoi(t.digits); err == nil && v > 0 {
				tpl, n = t.tpl, v
				break
			}
		}
		if tpl == "" {
			continue
		}
		var ref *ParsedName // 季号与片名取同模板的兄弟：它自己的解析在这一处是残的
		for f2, p2 := range parses {
			if p2 != nil && p2.Episode > 0 && hasTpl(names[f2], tpl) {
				ref = p2
				break
			}
		}
		season := p.Season
		if season == 0 && ref != nil {
			season = ref.Season
		}
		if taken[[2]int{season, n}] {
			continue // 那一集已经有文件了（重复的两份），留给特别篇，别让两份算出同一个名字
		}
		taken[[2]int{season, n}] = true
		p.Episode, p.IsTV = n, true
		if p.Season == 0 && ref != nil {
			p.Season, p.SeasonGuessed = ref.Season, ref.SeasonGuessed
		}
	}
}
