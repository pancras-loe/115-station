package api

// ==================== 洗版：画质偏好（「简单设置」）====================
//
// 2026-10-23，设计见 docs/115-station-notes/WASH-SIMPLIFY-PLAN.md。原来的 priority_level 是分档：
// 命中第几条得几分、一条都没中算最差。这个模型连默认配置都写不对——默认的「4K 不要 DV」
// 让带 DV 的 4K 输给 1080p、4K WEB-DL 输给 1080p 蓝光（第 2 条要求 BluRay，它一条都不中）。
//
// 偏好不分档，只有一条比较线，前面分出高下就停：
//
//	不要的（avoid / avoid_words / max_pix 封顶 / dv: avoid）排最后
//	→ 分辨率 → 杜比视界（只在 dv: prefer 时单独比）→ 片源 → HDR → 视频编码 → 偏好发布组 → 体积（size 写了才比）
//
// 「没写的不算更差」同 washCompareFiles：分辨率 / 片源 / 编码有一边认不出不比这一项，HDR 没写算 SDR。
// dv: any 时 DV 只当普通 HDR（DV.HDR10 按 HDR10 算），不再像默认比法那样压过 HDR10+。
//
// 和别的代码怎么接：normalizeWashStrategy 把 priority_level 换成 tiers() 编出来的两档规则，规则上挂着偏好。
// washCompareFiles / washTierOf 见到偏好就改走这里；只认规则字段的地方（订阅「只要命中前 N 条」按字段三态判）
// 用编出来的规则，两档的口径与 tier() 一致：0 = 允许的最高分辨率（dv: prefer 时还要有 DV）、1 = 其余不是「不要」的、-1 = 不要的。

import (
	"fmt"
	"path"
	"strings"
)

// washPrefer 画质偏好（YAML 里策略的 prefer 键；界面「简单设置」的 ②–⑤）
type washPrefer struct {
	MaxPix     string   `yaml:"max_pix,omitempty" json:"max_pix"`              // "" 越高越好 / "1080p" 封顶（更高的当作不要）
	DV         string   `yaml:"dv,omitempty" json:"dv"`                        // prefer / any / avoid，"" = any
	Size       string   `yaml:"size,omitempty" json:"size"`                    // small / large，"" = 不看体积
	Avoid      []string `yaml:"avoid,omitempty,flow" json:"avoid"`             // hdtv / cam / dvdrip / le720，认不出的当关键词
	AvoidWords []string `yaml:"avoid_words,omitempty,flow" json:"avoid_words"` // 文件名里有这些词的不要
	Teams      []string `yaml:"teams,omitempty,flow" json:"teams"`             // 画质一样时偏好的发布组
}

// washAvoidKinds 界面上点选的几类「不要」
var washAvoidKinds = map[string]string{"hdtv": "HDTV", "cam": "枪版", "dvdrip": "DVDRip", "le720": "720p 及以下"}

// washCamWords 枪版的写法（前后不挨字母数字才算，见 condTokenHit）
var washCamWords = []string{"tc", "hdtc", "cam", "hdcam", "camrip", "ts", "hdts", "telesync", "telecine", "枪版"}

// normalized 去空白、转小写、去重；写错的 dv / size 当没写，认不出的 avoid 并进关键词。切片一律非 nil（给界面）
func (p washPrefer) normalized() washPrefer {
	out := washPrefer{Avoid: []string{}, AvoidWords: []string{}, Teams: []string{}}
	if mp := strings.ToLower(strings.TrimSpace(p.MaxPix)); mp == "1080p" || mp == "1080" {
		out.MaxPix = "1080p"
	}
	switch dv := strings.ToLower(strings.TrimSpace(p.DV)); dv {
	case "prefer", "avoid":
		out.DV = dv
	default:
		out.DV = "any"
	}
	if sz := strings.ToLower(strings.TrimSpace(p.Size)); sz == "small" || sz == "large" {
		out.Size = sz
	}
	seen := map[string]bool{}
	add := func(dst *[]string, v string, fold bool) {
		v = strings.TrimSpace(v)
		k := v
		if fold {
			k = strings.ToLower(v)
		}
		if v == "" || seen[k] {
			return
		}
		seen[k] = true
		*dst = append(*dst, v)
	}
	for _, a := range p.Avoid {
		if k := strings.ToLower(strings.TrimSpace(a)); washAvoidKinds[k] != "" {
			add(&out.Avoid, k, true)
		} else {
			add(&out.AvoidWords, a, true)
		}
	}
	for _, w := range p.AvoidWords {
		add(&out.AvoidWords, w, true)
	}
	seen = map[string]bool{}
	for _, t := range p.Teams {
		add(&out.Teams, t, true)
	}
	return out
}

func (p *washPrefer) avoids(kind string) bool {
	for _, a := range p.Avoid {
		if a == kind {
			return true
		}
	}
	return false
}

// washStripVideoExt 名字结尾的视频扩展名去掉：「xxx.ts」的 ts 不是枪版
func washStripVideoExt(name string) string {
	if classifyFile(name) == FileTypeVideo {
		return strings.TrimSuffix(name, path.Ext(name))
	}
	return name
}

// washIsDV 带杜比视界（qualityOf 的 HDR 一维 4 就是 DV / DOVI）
func washIsDV(name string) bool { return qualityOf(name)[wqEffect] == 4 }

// washHDRNoDV DV 之外的 HDR 档：HDR10+ 3 / HDR10 2 / HDR、HLG 1 / SDR 0；只写了 DV 的算 HDR
func washHDRNoDV(name string) int {
	best := 0
	for _, e := range strings.Split(strings.ToUpper(ParseResourceInfo(name).Effect), ".") {
		r := 0
		switch {
		case strings.HasPrefix(e, "DV"):
		case e == "HDR10+":
			r = 3
		case e == "HDR10":
			r = 2
		case strings.HasPrefix(e, "HDR"), e == "HLG":
			r = 1
		}
		if r > best {
			best = r
		}
	}
	if best == 0 && washIsDV(name) {
		best = 1
	}
	return best
}

var washHDRNames = [4]string{"SDR", "HDR", "HDR10", "HDR10+"}

// avoidedBy 这个名字为什么是「不要」的（空 = 不是）
func (p *washPrefer) avoidedBy(name string) []string {
	q := qualityOf(name)
	lname := strings.ToLower(washStripVideoExt(name))
	var why []string
	if p.MaxPix == "1080p" && q[wqPix] > 4 {
		why = append(why, "超过 1080p")
	}
	if p.DV == "avoid" && q[wqEffect] == 4 {
		why = append(why, "杜比视界")
	}
	if p.avoids("hdtv") && q[wqType] == 2 {
		why = append(why, "HDTV")
	}
	if p.avoids("dvdrip") && q[wqType] == 1 {
		why = append(why, "DVDRip")
	}
	if p.avoids("le720") && q[wqPix] > 0 && q[wqPix] <= 3 {
		why = append(why, "720p 及以下")
	}
	if p.avoids("cam") {
		for _, w := range washCamWords {
			if condTokenHit(lname, "", w) {
				why = append(why, "枪版")
				break
			}
		}
	}
	for _, w := range p.AvoidWords {
		if condTokenHit(lname, "", strings.ToLower(w)) {
			why = append(why, w)
		}
	}
	return why
}

// tier 0 = 允许的最高分辨率（dv: prefer 时还要有 DV）；1 = 其余不是「不要」的；-1 = 不要的
func (p *washPrefer) tier(name string) int {
	if len(p.avoidedBy(name)) > 0 {
		return -1
	}
	q := qualityOf(name)
	top := q[wqPix] >= 6
	if p.MaxPix == "1080p" {
		top = q[wqPix] == 4
	}
	if top && (p.DV != "prefer" || q[wqEffect] == 4) {
		return 0
	}
	return 1
}

// teamHit 名字里有没有偏好的发布组。按词在名字里单独出现认（同规则的 resource_team，condTokenHit），
// 不只看 ParseResourceInfo 的 Team：它把最后一个「.」后面当扩展名去掉，没扩展名的名字（STRM 台账名、资源标题）
// 末尾的「-ADWeb」就被切掉了
func (p *washPrefer) teamHit(name string) (string, bool) {
	lname := strings.ToLower(washStripVideoExt(name))
	lteam := strings.ToLower(ParseResourceInfo(name).Team)
	for _, t := range p.Teams {
		if t != "" && condTokenHit(lname, lteam, strings.ToLower(t)) {
			return t, true
		}
	}
	return "", false
}

// compare >0 a 更好，<0 b 更好，0 分不出；why 从 a 的角度说（同 washCompareFiles）
func (p *washPrefer) compare(fa, fb washFile) (int, string) {
	a, b := fa.Name, fb.Name
	ea, eb := p.avoidedBy(a), p.avoidedBy(b)
	switch {
	case len(ea) > 0 && len(eb) == 0:
		return -1, "是不要的（" + strings.Join(ea, "、") + "）"
	case len(ea) == 0 && len(eb) > 0:
		return 1, "对方是不要的（" + strings.Join(eb, "、") + "）"
	}
	sign := func(x, y int) int {
		if x > y {
			return 1
		}
		return -1
	}
	qa, qb := qualityOf(a), qualityOf(b)
	if qa[wqPix] != qb[wqPix] && qa[wqPix] > 0 && qb[wqPix] > 0 {
		c := sign(qa[wqPix], qb[wqPix])
		return c, "分辨率：" + wqDimPair(a, b, wqPix, c)
	}
	if p.DV == "prefer" {
		da, db := qa[wqEffect] == 4, qb[wqEffect] == 4
		if da != db {
			if da {
				return 1, "杜比视界：有 > 没有（优先要 DV）"
			}
			return -1, "杜比视界：没有 < 有（优先要 DV）"
		}
	}
	if qa[wqType] != qb[wqType] && qa[wqType] > 0 && qb[wqType] > 0 {
		c := sign(qa[wqType], qb[wqType])
		return c, "片源：" + wqDimPair(a, b, wqType, c)
	}
	if ha, hb := washHDRNoDV(a), washHDRNoDV(b); ha != hb {
		c := sign(ha, hb)
		op := ">"
		if c < 0 {
			op = "<"
		}
		return c, fmt.Sprintf("HDR：%s %s %s", washHDRNames[ha], op, washHDRNames[hb])
	}
	if qa[wqCodec] != qb[wqCodec] && qa[wqCodec] > 0 && qb[wqCodec] > 0 {
		c := sign(qa[wqCodec], qb[wqCodec])
		return c, "视频编码：" + wqDimPair(a, b, wqCodec, c)
	}
	ta, hitA := p.teamHit(a)
	tb, hitB := p.teamHit(b)
	if hitA != hitB {
		if hitA {
			return 1, "画质一样，" + ta + " 是偏好的发布组"
		}
		return -1, "画质一样，对方的 " + tb + " 是偏好的发布组"
	}
	if c := washSizeCmp(fa.Size, fb.Size, p.Size); c != 0 {
		op, want := ">", "小"
		if fa.Size < fb.Size {
			op = "<"
		}
		if p.Size == "large" {
			want = "大"
		}
		return c, fmt.Sprintf("画质一样，体积：%s %s %s（留体积%s的）", washSizeText(fa.Size), op, washSizeText(fb.Size), want)
	}
	return 0, "画质也分不出"
}

// tiers 编成两档规则给只认规则字段的地方用（见文件头）。规则上挂着偏好，比较照样走 compare
func (p washPrefer) tiers() []washRule {
	var pix, typ, eff, team []string
	if p.MaxPix == "1080p" {
		pix = append(pix, "!2160p", "!4k", "!1440p", "!4320p")
	}
	if p.avoids("le720") {
		pix = append(pix, "!720p", "!576p", "!480p")
	}
	if p.avoids("hdtv") {
		typ = append(typ, "!HDTV")
	}
	if p.avoids("dvdrip") {
		typ = append(typ, "!DVDRip")
	}
	if p.avoids("cam") {
		for _, w := range washCamWords {
			typ = append(typ, "!"+w)
		}
	}
	if p.DV == "avoid" {
		eff = append(eff, "!DV", "!DOVI")
	}
	for _, w := range p.AvoidWords {
		team = append(team, "!"+w) // 发布组那一项也是按名字里单独出现判，借它放关键词
	}
	topPix := []string{"2160p", "4k", "4320p"}
	if p.MaxPix == "1080p" {
		topPix = []string{"1080p"}
	}
	topEff := eff
	if p.DV == "prefer" {
		topEff = append([]string{"DV", "DOVI"}, eff...)
	}
	pp := p
	top := washRule{ResourcePix: strings.Join(append(topPix, pix...), ","), ResourceType: strings.Join(typ, ","),
		ResourceEffect: strings.Join(topEff, ","), ResourceTeam: strings.Join(team, ","), SizePrefer: p.Size, prefer: &pp}
	rest := washRule{ResourcePix: strings.Join(pix, ","), ResourceType: strings.Join(typ, ","),
		ResourceEffect: strings.Join(eff, ","), ResourceTeam: strings.Join(team, ","), prefer: &pp}
	return []washRule{top, rest}
}

// preferOf 这组规则是不是偏好编出来的
func preferOf(rules []washRule) *washPrefer {
	if len(rules) > 0 && rules[0].prefer != nil {
		return rules[0].prefer
	}
	return nil
}
