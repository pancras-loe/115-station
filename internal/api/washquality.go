package api

// ==================== 洗版：两份文件谁更好 ====================
//
// 2026-10-20 起分两层（维护者定的，现场：库里 1080p WEB-DL，新来 2160p DV WEB-DL，
// 规则写的是 `resource_pix: "2160p,1080p"` + `resource_type: "WEB-DL"`，两份都只命中这一条，
// 原来平局不换，新版被移进「已存在」）：
//
//  1. 规则分：命中 priority_level 的第几条（越靠上越高，一条都没命中最低）。
//     分数高的赢，同 MoviePilot 的 pri_order、P115StrgmSub 的过滤分「严格更高才换」。
//  2. 分数相同（命中同一条，或都没命中），按默认画质先后比：
//     分辨率 → 片源 → HDR → 视频编码，前面分出高下就不看后面
//     （MediaSync115 `Pan115Service._score_video_file` 同序；它最后还比体积，
//     这里不比：新文件在网盘移动前拿不到可靠大小，见 decideWash）。
//     没配 priority_level 时只有这一层。
//
// 命中的那条规则里写了某个字段（比如 `resource_effect: "HDR10+,HDR"`），这一项最多按它命中的值算：
// DV.HDR 的文件只命中「HDR」，按 HDR 算，输给 HDR10+ —— 用户没写 DV，就不该让 DV 占便宜。
// 规则里没写的字段照默认排序；只写了排除词（!DV）的也照默认排序。
// 逗号里的先后不参与比较：「1080p,2160p」和「2160p,1080p」是一个意思。
//
// 全部打平时不换：两份差不多的版本会来回互相顶，每顶一次就删一份网盘文件。
//
// 体积（2026-10-23，维护者要的「4K 画质优先留小体积」）：
//   - resource_size 是命中条件，和其他字段一样「且」：`<30G`、`>=5G`、`5G-30G`，逗号分隔命中任一；
//     单位 T / G / M（不写按 G）。只在知道体积时判，不知道（只有资源标题：订阅按标题筛候选、找资源排名）算符合。
//     订阅列开分享按集挑文件、订阅洗版和库里那份比，都带着体积（subPriority.compareFiles）。
//   - size_prefer: small / large 写在某一条上：两份都命中这一条时**先比体积**、再按默认画质比 ——
//     用户在这条里明说了要小的，默认画质（DV > HDR）不该盖过它。
//     相差不到 5% 算一样大，接着比画质：同一个片源重新封装差几十 MB，不值得换一次删一份。
//     有一边不知道体积就不比这一项。

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// washFile 洗版比较的一方：名字 + 体积（字节，0 = 不知道）
type washFile struct {
	Name string
	Size int64
}

// washSizeTolerance 体积相差不到这个比例算一样大
const washSizeTolerance = 0.05

var reWashSizeTerm = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*([TGM]?)I?B?$`)

// parseWashSize 「30G」「1.5T」「800M」「20」（按 G）→ 字节，认不出 ok=false
func parseWashSize(s string) (int64, bool) {
	m := reWashSizeTerm.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	unit := map[string]float64{"T": 1 << 40, "G": 1 << 30, "": 1 << 30, "M": 1 << 20}[strings.ToUpper(m[2])]
	return int64(v * unit), true
}

// parseWashSizeRange 一段体积条件 → [lo, hi]（hi 为 0 = 不设上限），认不出 ok=false
func parseWashSizeRange(part string) (lo, hi int64, ok bool) {
	part = strings.TrimSpace(part)
	for _, op := range []string{"<=", ">=", "<", ">"} {
		if strings.HasPrefix(part, op) {
			v, ok := parseWashSize(part[len(op):])
			if !ok {
				return 0, 0, false
			}
			switch op {
			case "<=":
				return 0, v, true
			case "<":
				return 0, v - 1, true
			case ">=":
				return v, 0, true
			default:
				return v + 1, 0, true
			}
		}
	}
	a, b, found := strings.Cut(part, "-")
	if !found {
		return 0, 0, false
	}
	// 「5-30G」前一个数没写单位时跟后一个
	if m := reWashSizeTerm.FindStringSubmatch(strings.TrimSpace(b)); m != nil {
		if am := reWashSizeTerm.FindStringSubmatch(strings.TrimSpace(a)); am != nil && am[2] == "" {
			a = am[1] + m[2]
		}
	}
	lo, ok1 := parseWashSize(a)
	hi, ok2 := parseWashSize(b)
	if !ok1 || !ok2 || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// matchSize 体积条件：空 = 不限；size 为 0（不知道）算符合；写错的一段当作命中不了
// （写错不报错的话用户只会看到「这条规则怎么从来不中」，比静默放行好查）
func matchSize(size int64, cond string) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" || size <= 0 {
		return true
	}
	for _, part := range strings.Split(cond, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		lo, hi, ok := parseWashSizeRange(part)
		if ok && size >= lo && (hi == 0 || size <= hi) {
			return true
		}
	}
	return false
}

// washSizeCmp size_prefer 下 a 比 b：>0 a 更合意，0 分不出（没设、有一边不知道、相差不到 5%）
func washSizeCmp(a, b int64, prefer string) int {
	prefer = strings.ToLower(strings.TrimSpace(prefer))
	if (prefer != "small" && prefer != "large") || a <= 0 || b <= 0 {
		return 0
	}
	if math.Abs(float64(a-b)) <= washSizeTolerance*math.Max(float64(a), float64(b)) {
		return 0
	}
	c := 1
	if a > b {
		c = -1
	}
	if prefer == "large" {
		c = -c
	}
	return c
}

// washSizeText 日志里的体积：12.3G
func washSizeText(n int64) string {
	if n >= 1<<40 {
		return fmt.Sprintf("%.2fT", float64(n)/(1<<40))
	}
	if n >= 1<<30 {
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%dM", n>>20)
}

// 默认画质的四个维度，下标就是比较的先后
const (
	wqPix = iota
	wqType
	wqEffect
	wqCodec
	wqDims
)

var wqDimNames = [wqDims]string{"分辨率", "片源", "HDR", "视频编码"}

type washQuality [wqDims]int

var reWqDovi = regexp.MustCompile(`(?i)\bDOVI\b`)

// qualityOf 一个名字（文件名，或规则里的一个值）的默认画质分，认不出的维度为 0
func qualityOf(name string) washQuality {
	ri := ParseResourceInfo(name)
	upper := strings.ToUpper(name)
	var q washQuality
	q[wqPix] = map[string]int{"4320p": 7, "2160p": 6, "1440p": 5, "1080p": 4, "720p": 3, "576p": 2, "480p": 1}[ri.Pix]

	// REMUX 要看整个名字：「BluRay.REMUX」的 Type 是先出现的 BluRay
	switch t := strings.ToUpper(ri.Type); {
	case strings.Contains(upper, "REMUX"):
		q[wqType] = 6
	case t == "BLURAY":
		q[wqType] = 5
	case t == "WEB-DL":
		q[wqType] = 4
	case t == "WEBRIP":
		q[wqType] = 3
	case t == "HDTV":
		q[wqType] = 2
	case t == "DVDRIP":
		q[wqType] = 1
	}

	// 组合写法（DV.HDR10）取其中最高的那一项
	for _, e := range strings.Split(strings.ToUpper(ri.Effect), ".") {
		r := 0
		switch {
		case strings.HasPrefix(e, "DV"):
			r = 4
		case e == "HDR10+":
			r = 3
		case e == "HDR10":
			r = 2
		case strings.HasPrefix(e, "HDR"), e == "HLG":
			r = 1
		}
		if r > q[wqEffect] {
			q[wqEffect] = r
		}
	}
	if reWqDovi.MatchString(name) {
		q[wqEffect] = 4 // ParseResourceInfo 不把 DOVI 归进特效
	}

	switch strings.ToUpper(strings.SplitN(ri.VideoEncode, ".", 2)[0]) {
	case "H265", "X265", "AV1":
		q[wqCodec] = 2
	case "H264", "X264":
		q[wqCodec] = 1
	}
	return q
}

// ruleFieldOf 规则里对应某个维度的字段
func ruleFieldOf(r *washRule, dim int) string {
	switch dim {
	case wqPix:
		return r.ResourcePix
	case wqType:
		return r.ResourceType
	case wqEffect:
		return r.ResourceEffect
	default:
		return r.VideoEncode
	}
}

// qualityUnder 按命中的规则收窄后的画质分（r 为 nil = 没命中规则，照默认）
func qualityUnder(name string, r *washRule) washQuality {
	q := qualityOf(name)
	if r == nil {
		return q
	}
	lname := strings.ToLower(name)
	ri := ParseResourceInfo(name)
	vals := [wqDims]string{ri.Pix, ri.Type, ri.Effect, ri.VideoEncode} // 与 ruleMatch 各字段用的归一值同源
	for d := 0; d < wqDims; d++ {
		ceil := 0
		for _, part := range strings.Split(ruleFieldOf(r, d), ",") {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, "!") {
				continue
			}
			// 命中判断与 matchField 同口径（condTokenHit），值本身的分按同一套默认排序算；
			// 默认排序里认不出的值（UHD、发布组之类）不设上限，否则这一维被抹成 0
			if condTokenHit(lname, strings.ToLower(vals[d]), strings.ToLower(part)) {
				if v := qualityOf(part)[d]; v > ceil {
					ceil = v
				}
			}
		}
		if ceil > 0 && ceil < q[d] {
			q[d] = ceil
		}
	}
	return q
}

// washTier 命中 priority_level 的第几条（0 最优），一条都没命中 -1
func washTier(name string, rules []washRule) int { return washTierOf(washFile{Name: name}, rules) }

func washTierOf(f washFile, rules []washRule) int {
	if p := preferOf(rules); p != nil {
		return p.tier(f.Name)
	}
	for i, r := range rules {
		if ruleMatchFile(f, r) {
			return i
		}
	}
	return -1
}

// washCompare 比较 a、b 两个名字：>0 a 更好，<0 b 更好，0 分不出。why 是给日志看的理由（从 a 的角度说）。
// 只有名字，体积两项不看；整理洗版知道体积，走 washCompareFiles
func washCompare(a, b string, rules []washRule) (cmp int, why string) {
	return washCompareFiles(washFile{Name: a}, washFile{Name: b}, rules)
}

func washCompareFiles(fa, fb washFile, rules []washRule) (cmp int, why string) {
	if p := preferOf(rules); p != nil {
		return p.compare(fa, fb)
	}
	a, b := fa.Name, fb.Name
	ta, tb := washTierOf(fa, rules), washTierOf(fb, rules)
	tierText := func(t int) string {
		if t < 0 {
			return "没命中规则"
		}
		return fmt.Sprintf("命中第 %d 条规则", t+1)
	}
	if ta != tb {
		// -1 是没命中，比任何一条都差
		if tb < 0 || (ta >= 0 && ta < tb) {
			return 1, fmt.Sprintf("%s，对方%s", tierText(ta), tierText(tb))
		}
		return -1, fmt.Sprintf("%s，对方%s", tierText(ta), tierText(tb))
	}
	var r *washRule
	if ta >= 0 {
		r = &rules[ta]
	}
	prefix := ""
	if len(rules) > 0 {
		prefix = "都" + tierText(ta) + "，"
		if ta < 0 {
			prefix = "都没命中规则，"
		}
	}
	if r != nil {
		if c := washSizeCmp(fa.Size, fb.Size, r.SizePrefer); c != 0 {
			op := ">"
			if fa.Size < fb.Size {
				op = "<"
			}
			want := "小"
			if strings.EqualFold(strings.TrimSpace(r.SizePrefer), "large") {
				want = "大"
			}
			return c, fmt.Sprintf("%s体积：%s %s %s（这条规则优先%s体积）", prefix, washSizeText(fa.Size), op, washSizeText(fb.Size), want)
		}
	}
	qa, qb := qualityUnder(a, r), qualityUnder(b, r)
	for d := 0; d < wqDims; d++ {
		if qa[d] == qb[d] {
			continue
		}
		// 分辨率 / 片源 / 编码有一边没写就不比这一项：没写是不知道，不是更差，
		// 否则同是 REMUX、只因一边名字里多写了 H264 就互相顶（间谍过家家 E08）。
		// HDR 没写就是 SDR，照比
		if d != wqEffect && (qa[d] == 0 || qb[d] == 0) {
			continue
		}
		cmp = 1
		if qa[d] < qb[d] {
			cmp = -1
		}
		return cmp, fmt.Sprintf("%s%s：%s", prefix, wqDimNames[d], wqDimPair(a, b, d, cmp))
	}
	return 0, prefix + "画质也分不出"
}

// wqDimPair 「2160p > 1080p」这样的一小段，认不出的写「未标明」
func wqDimPair(a, b string, dim, cmp int) string {
	val := func(n string) string {
		ri := ParseResourceInfo(n)
		v := ""
		switch dim {
		case wqPix:
			v = ri.Pix
		case wqType:
			v = ri.Type
			if strings.Contains(strings.ToUpper(n), "REMUX") {
				v = "REMUX"
			}
		case wqEffect:
			v = ri.Effect
			if v == "" && reWqDovi.MatchString(n) {
				v = "DV"
			}
		default:
			v = strings.SplitN(ri.VideoEncode, ".", 2)[0]
		}
		if v == "" {
			return "未标明"
		}
		return v
	}
	op := ">"
	if cmp < 0 {
		op = "<"
	}
	return val(a) + " " + op + " " + val(b)
}
