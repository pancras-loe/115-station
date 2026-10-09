package api

// 订阅：从分享里只挑缺的那几集。
//
// 思路来自 MediaSync115 的 _auto_save_resources（递归列分享 → 按文件名解析集号 → 只转缺的集，
// 同一集多份按画质挑一份，跳过 ISO），只读思路；解析换成和整理同一套的 parseVideoInDir
// （替换规则 → 所在子目录季号 → 订阅范围的季号），这样「Season 2/01.mkv」能认成 S02E01。
//
// 纯函数，不发请求：分享树由 shareWalk 列好传进来，库里有没有这份文件由调用方给的 haveSha1 判断。

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// sharePickOpts 挑选条件
type sharePickOpts struct {
	MediaType string         // movie / tv
	Missing   map[epKey]bool // 剧集：要补的集
	// SeasonHint 订阅某一季 / 集段时的季号：文件名和目录都没写季号时用它，
	// 不然「第二季」分享里的 01.mkv 会被当成 S01E01。全剧订阅为 nil
	SeasonHint *ParsedName
	Rules      []ReplaceRule
	// Remap 全剧连续编号换算（S01E166 → S07E22），接 absepisode.go；key 是条目 ID。nil = 不换
	Remap    func(eps map[string]*ParsedName)
	HaveSha1 func(sha1 string) bool // 库里已经有这一份（台账 sha1）
	// Rank 文件名的洗版排名（resWashRank），越小越好、-1 = 没命中；nil 时只比体积
	Rank func(name string) int
	// Accept 资源条件（subCond.fileOK）：hasSub = 同目录有跟着它的字幕。nil = 不限
	Accept func(e shareEntry, hasSub bool) (bool, string)
	// MaxEps 剧集最多挑几集（订阅一轮的集数上限还剩多少），超了留集号靠前的；0 = 不限。
	// 双集文件只要沾上留下的集就整个要，所以可能多出一集
	MaxEps int
}

// sharePick 挑选结果
type sharePick struct {
	Picks   []shareEntry // 要转存的：视频 + 跟着它的字幕 / NFO / 剧照
	Covered []epKey      // 这次能补上的集（剧集）
	Videos  int          // 分享里的视频数（不含光盘结构）
	Disc    int          // 光盘结构的视频（ISO / BDMV / VIDEO_TS），不挑
	Unknown int          // 认不出集号的视频
	// Episodes 分享里认得出的集一共几集（不只是缺的）：订阅预看分享时「集数多的在前」用
	Episodes int
	InLib    int // 库里已经有同一份（sha1 相同）的视频
	// Rejected 不符合资源条件的视频，按原因计
	Rejected map[string]int
}

// summary 给人看的一句话（尝试记录的原因 / 日志）
func (p sharePick) summary() string {
	parts := []string{fmt.Sprintf("分享里 %d 个视频", p.Videos)}
	if len(p.Covered) > 0 {
		parts = append(parts, fmt.Sprintf("挑中 %d 集", len(p.Covered)))
	}
	if p.Unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d 个认不出集号", p.Unknown))
	}
	if p.InLib > 0 {
		parts = append(parts, fmt.Sprintf("%d 个库里已有", p.InLib))
	}
	if p.Disc > 0 {
		parts = append(parts, fmt.Sprintf("%d 个光盘结构不要", p.Disc))
	}
	if len(p.Rejected) > 0 {
		parts = append(parts, "不符合资源条件的 "+countsText(p.Rejected))
	}
	return strings.Join(parts, "，")
}

type shareVideo struct {
	e    shareEntry
	eps  []epKey
	rank int
}

// better a 比 b 更值得要：洗版排名小的优先（没命中的最后），一样就要大的
func (a shareVideo) better(b shareVideo) bool {
	ra, rb := a.rank, b.rank
	if ra < 0 {
		ra = 1 << 30
	}
	if rb < 0 {
		rb = 1 << 30
	}
	if ra != rb {
		return ra < rb
	}
	return a.e.Size > b.e.Size
}

// pickShareEpisodes 从分享树里挑要转存的文件。
// 剧集：只挑覆盖了缺集的视频，同一集多份挑一份；电影：挑一份最好的。
// 一个都没挑中时 Picks 为空，由调用方记成「里面没有缺的集」
func pickShareEpisodes(entries []shareEntry, o sharePickOpts) sharePick {
	var res sharePick
	var vids []shareVideo
	// 有字幕跟着的视频（同目录、字幕名以视频基名开头）：资源条件要中字时算有
	subBases := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir && classifyFile(e.Name) == FileTypeSubtitle {
			subBases[e.Dir+"/"+e.Name] = true
		}
	}
	hasSub := func(v shareEntry) bool {
		base := v.Dir + "/" + baseName(v.Name)
		for k := range subBases {
			if strings.HasPrefix(k, base+".") || strings.HasPrefix(k, base+"-") {
				return true
			}
		}
		return false
	}
	for _, e := range entries {
		if e.IsDir || classifyFile(e.Name) != FileTypeVideo {
			continue
		}
		if discPath("/" + e.path()) {
			res.Disc++
			continue
		}
		res.Videos++
		if e.Sha1 != "" && o.HaveSha1 != nil && o.HaveSha1(strings.ToUpper(e.Sha1)) {
			res.InLib++
			continue
		}
		if o.Accept != nil {
			if ok, why := o.Accept(e, hasSub(e)); !ok {
				if res.Rejected == nil {
					res.Rejected = map[string]int{}
				}
				res.Rejected[why]++
				continue
			}
		}
		rank := -1
		if o.Rank != nil {
			rank = o.Rank(e.Name)
		}
		vids = append(vids, shareVideo{e: e, rank: rank})
	}

	var chosen []shareVideo
	if o.MediaType == "movie" {
		for _, v := range vids {
			if len(chosen) == 0 || v.better(chosen[0]) {
				chosen = []shareVideo{v}
			}
		}
	} else {
		chosen = pickEpisodeVideos(vids, o, &res)
	}
	if len(chosen) == 0 {
		return res
	}

	picked := map[string]bool{}
	for _, v := range chosen {
		res.Picks = append(res.Picks, v.e)
		picked[v.e.ID] = true
	}
	// 跟着视频走的附属文件：同目录、名字以视频基名开头的字幕 / NFO / 剧照（01.chs.ass、01.nfo、01-thumb.jpg）
	for _, e := range entries {
		if e.IsDir || picked[e.ID] {
			continue
		}
		switch classifyFile(e.Name) {
		case FileTypeSubtitle, FileTypeNFO, FileTypeStdImage:
		default:
			continue
		}
		for _, v := range chosen {
			if e.Dir != v.e.Dir {
				continue
			}
			base := baseName(v.e.Name)
			if strings.HasPrefix(e.Name, base+".") || strings.HasPrefix(e.Name, base+"-") {
				res.Picks = append(res.Picks, e)
				picked[e.ID] = true
				break
			}
		}
	}
	return res
}

// pickEpisodeVideos 剧集按集挑：解析每个视频的季集 → 只要覆盖了缺集的 → 每集留最好的一份。
// 双集文件只要其中一集缺就要；被它占了的那集不再另挑
func pickEpisodeVideos(vids []shareVideo, o sharePickOpts, res *sharePick) []shareVideo {
	parsed := map[string]*ParsedName{}
	for _, v := range vids {
		p := parseVideoInDir(remoteFile{Fid: v.e.ID, Name: v.e.Name, Path: v.e.path()}, o.Rules, o.SeasonHint)
		if p != nil && p.Episode > 0 {
			parsed[v.e.ID] = p
		}
	}
	if o.Remap != nil && len(parsed) > 0 {
		o.Remap(parsed)
	}
	var cands []shareVideo
	all := map[epKey]bool{}
	defer func() { res.Episodes = len(all) }()
	for _, v := range vids {
		p := parsed[v.e.ID]
		if p == nil || p.Episode <= 0 {
			res.Unknown++
			continue
		}
		for _, e := range p.episodeList() {
			v.eps = append(v.eps, epKey{p.Season, e})
			all[epKey{p.Season, e}] = true
		}
		for _, k := range v.eps {
			if o.Missing[k] {
				cands = append(cands, v)
				break
			}
		}
	}
	// 好的在前，然后逐个认领还没被认领的缺集
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].better(cands[j]) })
	taken := map[epKey]bool{}
	var out []shareVideo
	for _, v := range cands {
		gain := false
		for _, k := range v.eps {
			if o.Missing[k] && !taken[k] {
				gain = true
			}
		}
		if !gain {
			continue
		}
		for _, k := range v.eps {
			if o.Missing[k] && !taken[k] {
				taken[k] = true
				res.Covered = append(res.Covered, k)
			}
		}
		out = append(out, v)
	}
	sortEpKeys(res.Covered)
	if o.MaxEps > 0 && len(res.Covered) > o.MaxEps {
		out, res.Covered = capEpisodeVideos(out, res.Covered[:o.MaxEps], o.Missing)
	}
	// 按路径排，转存与日志顺序稳定
	sort.Slice(out, func(i, j int) bool {
		return path.Join(out[i].e.Dir, out[i].e.Name) < path.Join(out[j].e.Dir, out[j].e.Name)
	})
	return out
}

// capEpisodeVideos 只留覆盖了 keep 里某一集的视频，Covered 按留下的重算
func capEpisodeVideos(vids []shareVideo, keep []epKey, missing map[epKey]bool) ([]shareVideo, []epKey) {
	want := map[epKey]bool{}
	for _, k := range keep {
		want[k] = true
	}
	var out []shareVideo
	var covered []epKey
	seen := map[epKey]bool{}
	for _, v := range vids {
		hit := false
		for _, k := range v.eps {
			if want[k] {
				hit = true
			}
		}
		if !hit {
			continue
		}
		out = append(out, v)
		for _, k := range v.eps {
			if missing[k] && !seen[k] {
				seen[k] = true
				covered = append(covered, k)
			}
		}
	}
	sortEpKeys(covered)
	return out, covered
}
