package api

// 订阅的资源条件：分辨率 / 质量 / 特效 / 编码 / 发布组 / 中字 / 体积，避免转存、离线下来又被洗版判输的东西。
//
// 字段与写法沿用洗版规则（washRule）：逗号分隔多个值命中任一即可，「!」开头是排除。
// 但匹配不用洗版的 matchField：它对名字做子串查找，DV 会命中 DVDRip、!TS 会排掉所有带 ts 的名字。
// 这里一个词要么包含在解析出的归一值里，要么在名字里前后都不挨着字母数字（condTokenHit）。
// MoviePilot 的订阅有质量 / 分辨率 / 特效三项正则；P115StrgmSub 把这三项放到分享里逐个文件判，
// 默认严格（不符合就不转）——这里同样在文件一级严格判，只读思路。
//
// 一项条件对一个名字有三种结果：符合、明确不符（名字里写的是别的值，或命中排除词）、没写（看不出来）。
//   - 资源标题：不符的丢掉；没写的分享照样去列，到文件一级再判；磁力只有标题可看、又要花离线配额，
//     没写的不下；RE0 要花积分的也不碰。
//   - 分享里的文件：先看文件名，文件名没写再看资源标题，两边都没写算不符（01.mkv 挂在「三体 全集」下面，
//     说不清是不是要的画质，就不转）。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// subCond 资源条件，零值 = 不限
type subCond struct {
	Pix    string `json:"pix"`    // 分辨率：2160p,1080p
	Type   string `json:"type"`   // 质量：WEB-DL,BluRay,REMUX
	Effect string `json:"effect"` // 特效：DV,HDR
	Video  string `json:"video"`  // 视频编码：H265,x265
	Audio  string `json:"audio"`  // 音频编码：TrueHD,DTS
	Team   string `json:"team"`   // 发布组
	Zh     bool   `json:"zh"`     // 要中文字幕
	// MinGB / MaxGB 单个视频文件的体积范围，0 = 不限（剧集是单集大小）
	MinGB float64 `json:"min_gb"`
	MaxGB float64 `json:"max_gb"`
}

func (c subCond) empty() bool { return c == subCond{} }

func normalizeSubCond(c subCond) subCond {
	for _, p := range []*string{&c.Pix, &c.Type, &c.Effect, &c.Video, &c.Audio, &c.Team} {
		*p = truncateStr(strings.TrimSpace(strings.ReplaceAll(*p, "，", ",")), 120)
	}
	if c.MinGB < 0 {
		c.MinGB = 0
	}
	if c.MaxGB < 0 || (c.MaxGB > 0 && c.MaxGB < c.MinGB) {
		c.MaxGB = 0
	}
	return c
}

// subCondDim 一项文本条件：界面上的名字、条件值、从标签里取的值
type subCondDim struct {
	label string
	cond  string
	value func(resTags) string
}

func (c subCond) dims() []subCondDim {
	return []subCondDim{
		{"分辨率", c.Pix, func(t resTags) string { return t.Pix }},
		{"质量", c.Type, func(t resTags) string { return t.Type }},
		{"特效", c.Effect, func(t resTags) string { return t.Effect }},
		{"视频编码", c.Video, func(t resTags) string { return t.Video }},
		{"音频编码", c.Audio, func(t resTags) string { return t.Audio }},
		{"发布组", c.Team, func(t resTags) string { return t.Team }},
	}
}

type condVerdict int

const (
	condOK condVerdict = iota
	condUnknown
	condFail
)

// condField 一项条件对一个名字：命中排除词是明确不符；命中任一正值是符合；
// 有正值都没命中时，名字里认出了这一项的别的值是明确不符，认不出是没写
func condField(name, cond, value string) condVerdict {
	lname, lvalue := strings.ToLower(name), strings.ToLower(value)
	hasPos, posHit := false, false
	for _, part := range strings.Split(cond, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		neg := strings.HasPrefix(part, "!")
		want := strings.TrimSpace(strings.TrimPrefix(part, "!"))
		if want == "" {
			continue
		}
		hit := condTokenHit(lname, lvalue, want)
		if neg {
			if hit {
				return condFail
			}
			continue
		}
		hasPos = true
		posHit = posHit || hit
	}
	switch {
	case !hasPos || posHit:
		return condOK
	case value != "":
		return condFail
	}
	return condUnknown
}

// condTokenHit 一个词命中：包含在归一值里（HEVC 归一成 H265），或者在名字里单独出现——
// 前后不是字母数字（「4K杜比视界」里的 4k 算，「DVDRip」里的 dv 不算）。参数都已转小写
func condTokenHit(lname, lvalue, want string) bool {
	if lvalue != "" && strings.Contains(lvalue, want) {
		return true
	}
	alnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
	for from := 0; ; {
		i := strings.Index(lname[from:], want)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(want)
		// 词本身首尾是符号（hdr10+）时那一侧不用看边界
		before := i == 0 || !alnum(want[0]) || !alnum(lname[i-1])
		after := end == len(lname) || !alnum(want[len(want)-1]) || !alnum(lname[end])
		if before && after {
			return true
		}
		from = i + 1
	}
}

// sizeOK 体积在范围内（size ≤ 0 = 不知道，不拦）
func (c subCond) sizeOK(size int64) (bool, string) {
	if size <= 0 {
		return true, ""
	}
	gb := float64(size) / (1 << 30)
	if c.MinGB > 0 && gb < c.MinGB {
		return false, "体积太小"
	}
	if c.MaxGB > 0 && gb > c.MaxGB {
		return false, "体积太大"
	}
	return true, ""
}

// titleVerdict 资源标题过一遍条件。返回最坏的结果与原因（「分辨率不符」/「标题没写分辨率」）。
// 体积只对电影判：剧集的资源体积是整包的，和单集体积比不了
func (c subCond) titleVerdict(title string, tags resTags, movie bool, size int64) (condVerdict, string) {
	worst, why := condOK, ""
	for _, d := range c.dims() {
		switch condField(title, d.cond, d.value(tags)) {
		case condFail:
			return condFail, d.label + "不符"
		case condUnknown:
			if worst == condOK {
				worst, why = condUnknown, "标题没写"+d.label
			}
		}
	}
	if movie {
		if ok, w := c.sizeOK(size); !ok {
			return condFail, w
		}
	}
	if c.Zh && !tags.Zh && worst == condOK {
		worst, why = condUnknown, "标题没写中字"
	}
	return worst, why
}

// fileOK 分享里的一个视频过一遍条件：文件名没写的看资源标题，两边都没写算不符。
// 中字：资源标题或文件名写了、或者同目录有跟着它的字幕文件
func (c subCond) fileOK(name string, size int64, title string, titleTags resTags, hasSub bool) (bool, string) {
	tags := resTagsOf(name)
	for _, d := range c.dims() {
		v := condField(name, d.cond, d.value(tags))
		if v == condUnknown {
			v = condField(title, d.cond, d.value(titleTags))
		}
		switch v {
		case condFail:
			return false, d.label + "不符"
		case condUnknown:
			return false, "没写" + d.label
		}
	}
	if c.Zh && !titleTags.Zh && !tags.Zh && !hasSub {
		return false, "没有中字"
	}
	return c.sizeOK(size)
}

// subCondOf 这个订阅用的条件：自己设了（Cond 非空，"{}" 也算：自定义成不限）用自己的，否则跟全局
func subCondOf(cfg subscribeCfg, condJSON string) subCond {
	if strings.TrimSpace(condJSON) == "" {
		return cfg.Cond
	}
	var c subCond
	if err := json.Unmarshal([]byte(condJSON), &c); err != nil {
		return cfg.Cond
	}
	return normalizeSubCond(c)
}

// subCondText 条件写成一句话（日志 / 详情）
func subCondText(c subCond) string {
	var parts []string
	for _, d := range c.dims() {
		if d.cond != "" {
			parts = append(parts, d.label+" "+d.cond)
		}
	}
	if c.Zh {
		parts = append(parts, "要中字")
	}
	switch {
	case c.MinGB > 0 && c.MaxGB > 0:
		parts = append(parts, fmt.Sprintf("单个视频 %g–%g GB", c.MinGB, c.MaxGB))
	case c.MinGB > 0:
		parts = append(parts, fmt.Sprintf("单个视频 ≥ %g GB", c.MinGB))
	case c.MaxGB > 0:
		parts = append(parts, fmt.Sprintf("单个视频 ≤ %g GB", c.MaxGB))
	}
	if len(parts) == 0 {
		return "不限"
	}
	return strings.Join(parts, "，")
}
