package api

import (
	"fmt"
	"strings"

	"115-station/internal/model"

	"gopkg.in/yaml.v3"
)

// ==================== 二级分类的简单模式（2026-10-23） ====================
//
// 洗版有「简单设置」（washapi.go），二级分类照同一个思路：简单模式就是一份写法受限的 YAML，
// 不另存表单。保存时后端由表单生成 YAML（catSimpleForm.yaml），读的时候从 YAML 认回来（catSimpleOf），
// 认不回来的（写了认不出的条件、顶层有 movie / tv 以外的键）只能在专家模式里改。
// 别在前端另存一份表单：两处各存一份就会互相覆盖。
//
// 和 YAML 的对应：每个媒体类型一张有顺序的清单 + 一个固定在最后的兜底（「其余都放这里」）。
// 兜底 = 清单里最后一条没写条件的；YAML 里没有兜底时整理退回「电影/未分类」「剧集/未分类」
// （classifyTargetIn 的最后一档），认回来时就把它写明，行为不变。
// 中间没写条件的规则照原样留在清单里（会接走所有片，界面上标出来），不替用户挪。
//
// 条件值是原样的逗号串（genre_ids: '16,99'），类型 / 地区这些点选标签在前端拼，
// 排除写法（!CN）、范围这些标签表示不了的也原样留着，所以只有 YAML 结构本身能挡住简单模式。

// catSimpleRule 简单模式的一条分类（字段与 YAML 的条件键一一对应）
type catSimpleRule struct {
	Name                string `json:"name"`
	GenreIds            string `json:"genre_ids"`
	OriginCountry       string `json:"origin_country"`
	OriginalLanguage    string `json:"original_language"`
	ProductionCountries string `json:"production_countries"`
	ReleaseYear         string `json:"release_year"`
	CustomRegex         string `json:"custom_regex"`
	Ext                 string `json:"ext"`
	// 2026-10-30 起（catrule.go）。可选条件（?字段）简单模式表示不了，写了的只能在专家模式改
	Keywords        string `json:"keywords"`
	IncludeKeywords string `json:"include_keywords"`
	SeriesKeywords  string `json:"series_keywords"`
	SeriesActors    string `json:"series_actors"`
	SeriesDirectors string `json:"series_directors"`
}

// catSimpleForm 简单模式的整张表
type catSimpleForm struct {
	Movie    []catSimpleRule `json:"movie"`
	TV       []catSimpleRule `json:"tv"`
	Fallback struct {
		Movie string `json:"movie"`
		TV    string `json:"tv"`
	} `json:"fallback"`
}

func (r catSimpleRule) noCond() bool {
	row := r.row("")
	return catRuleEmpty(&row)
}

func (r catSimpleRule) row(mediaType string) model.CategoryRule {
	t := strings.TrimSpace
	return model.CategoryRule{
		MediaType: mediaType, Name: libSubPath(r.Name),
		GenreIds: t(r.GenreIds), OriginCountry: t(r.OriginCountry), OriginalLanguage: t(r.OriginalLanguage),
		ProductionCountries: t(r.ProductionCountries), ReleaseYear: t(r.ReleaseYear),
		CustomRegex: t(r.CustomRegex), Ext: t(r.Ext),
		Keywords: t(r.Keywords), IncludeKeywords: t(r.IncludeKeywords),
		SeriesKeywords: t(r.SeriesKeywords), SeriesActors: t(r.SeriesActors), SeriesDirectors: t(r.SeriesDirectors),
	}
}

// catSimpleDefaultFallback 某媒体类型没有兜底时整理实际放的目录（classifyTargetIn 的最后一档）
func catSimpleDefaultFallback(mediaType string) string {
	return libSubPath(mediaTypeCategory(mediaType), "未分类")
}

// validate 保存前查一遍：目录名不能空、同一媒体类型里不能重名（YAML 的键重名直接解析失败）
func (f catSimpleForm) validate() error {
	for _, mt := range []struct {
		key, label string
		list       []catSimpleRule
		fb         string
	}{{"movie", "电影", f.Movie, f.Fallback.Movie}, {"tv", "剧集", f.TV, f.Fallback.TV}} {
		seen := map[string]int{}
		for i, r := range mt.list {
			name := libSubPath(r.Name)
			if name == "" {
				return fmt.Errorf("%s第 %d 条分类没有填目录名", mt.label, i+1)
			}
			if j, ok := seen[strings.ToLower(name)]; ok {
				return fmt.Errorf("%s第 %d 条和第 %d 条是同一个目录「%s」，合成一条（条件写在一起是「或」）", mt.label, j+1, i+1, name)
			}
			// 没写条件的保存时会被挪到最后当兜底（catMoveFallbacks），「其余都放这里」就永远用不上了：让用户自己定
			if r.noCond() {
				return fmt.Errorf("%s第 %d 条「%s」没写条件，会接走所有片：补上条件，或者删掉它、把目录填进「其余都放这里」", mt.label, i+1, name)
			}
			seen[strings.ToLower(name)] = i
		}
		fb := libSubPath(mt.fb)
		if fb == "" {
			return fmt.Errorf("%s的「其余都放这里」没有填目录名", mt.label)
		}
		if j, ok := seen[strings.ToLower(fb)]; ok {
			return fmt.Errorf("%s第 %d 条和「其余都放这里」是同一个目录「%s」", mt.label, j+1, fb)
		}
	}
	return nil
}

// rows 表单 → 规则行（兜底排在最后）
func (f catSimpleForm) rows() []model.CategoryRule {
	var out []model.CategoryRule
	for _, mt := range []struct {
		key  string
		list []catSimpleRule
		fb   string
	}{{"movie", f.Movie, f.Fallback.Movie}, {"tv", f.TV, f.Fallback.TV}} {
		for i, r := range mt.list {
			row := r.row(mt.key)
			row.Priority = i + 1
			out = append(out, row)
		}
		out = append(out, model.CategoryRule{MediaType: mt.key, Name: libSubPath(mt.fb), Priority: len(mt.list) + 1})
	}
	return out
}

// yaml 表单 → YAML。两个媒体类型都写（哪怕只有兜底），读回来才认得出
func (f catSimpleForm) yaml() string {
	return renderCategoryYAML(f.rows())
}

// catSimpleOf 从 YAML 认回简单模式的表单。认不出时返回原因（给人看的，一条一行）
func catSimpleOf(src string) (*catSimpleForm, []string) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return nil, []string{"YAML 格式错误：" + err.Error()}
	}
	var why []string
	if len(root.Content) > 0 {
		mp := root.Content[0]
		if mp.Kind != yaml.MappingNode {
			return nil, []string{"最外层不是 movie: / tv: 这样的键值结构"}
		}
		for i := 0; i+1 < len(mp.Content); i += 2 {
			k := mp.Content[i]
			switch {
			case k.Value != "movie" && k.Value != "tv":
				why = append(why, fmt.Sprintf("第 %d 行的「%s」不是 movie / tv，整理不看它，简单模式也表示不了", k.Line, k.Value))
			case mp.Content[i+1].Kind == yaml.MappingNode:
				for j := 0; j+1 < len(mp.Content[i+1].Content); j += 2 {
					if f := mp.Content[i+1].Content[j+1]; f.Kind != yaml.MappingNode && !yamlNullNode(f) {
						why = append(why, fmt.Sprintf("第 %d 行的分类「%s」后面直接写了值，条件要写成下一层的键值", f.Line, mp.Content[i+1].Content[j].Value))
					}
				}
			case !yamlNullNode(mp.Content[i+1]):
				why = append(why, fmt.Sprintf("第 %d 行的 %s 下面不是分类清单", k.Line, k.Value))
			}
		}
	}
	rows, err := parseCategoryYAML(src)
	if err != nil {
		return nil, []string{"YAML 解析失败：" + err.Error()}
	}
	for _, r := range rows {
		if r.Unsupported != "" {
			why = append(why, fmt.Sprintf("%s的「%s」写了认不出的条件 %s（这条规则永远不会命中）", mediaLabel(r.MediaType), r.Name, r.Unsupported))
		}
		if r.Optional != "" {
			why = append(why, fmt.Sprintf("%s的「%s」用了可选条件（?%s），简单模式表示不了", mediaLabel(r.MediaType), r.Name, strings.ReplaceAll(r.Optional, ",", "、?")))
		}
	}
	if len(why) > 0 {
		return nil, why
	}
	f := &catSimpleForm{Movie: []catSimpleRule{}, TV: []catSimpleRule{}}
	for _, mt := range []string{"movie", "tv"} {
		var list []catSimpleRule
		for _, r := range rows {
			if r.MediaType == mt {
				list = append(list, catSimpleRule{
					Name: r.Name, GenreIds: r.GenreIds, OriginCountry: r.OriginCountry, OriginalLanguage: r.OriginalLanguage,
					ProductionCountries: r.ProductionCountries, ReleaseYear: r.ReleaseYear, CustomRegex: r.CustomRegex, Ext: r.Ext,
					Keywords: r.Keywords, IncludeKeywords: r.IncludeKeywords,
					SeriesKeywords: r.SeriesKeywords, SeriesActors: r.SeriesActors, SeriesDirectors: r.SeriesDirectors,
				})
			}
		}
		fb := catSimpleDefaultFallback(mt)
		if n := len(list); n > 0 && list[n-1].noCond() {
			fb, list = list[n-1].Name, list[:n-1]
		}
		if list == nil {
			list = []catSimpleRule{}
		}
		if mt == "movie" {
			f.Movie, f.Fallback.Movie = list, fb
		} else {
			f.TV, f.Fallback.TV = list, fb
		}
	}
	// 生成回去的 YAML 必须和原来分得一模一样，对不上就别让用户在简单模式里存（存了就悄悄改了分类）
	if !catSameRules(rows, f.rows()) {
		return nil, []string{"简单模式生成的规则和原来对不上，为免改变分类结果只能在专家模式里改"}
	}
	return f, nil
}

// yamlNullNode 空值（`电影/外语电影:` 后面什么都没写，或写了 ~ / null）
func yamlNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "")
}

func mediaLabel(mt string) string {
	if mt == "tv" {
		return "剧集"
	}
	return "电影"
}

// catSameRules 两份规则分类结果是否一样：逐条比条件；b 比 a 多出来的只许是补写的默认兜底
func catSameRules(a, b []model.CategoryRule) bool {
	for _, mt := range []string{"movie", "tv"} {
		var x, y []model.CategoryRule
		for _, r := range a {
			if r.MediaType == mt {
				x = append(x, r)
			}
		}
		for _, r := range b {
			if r.MediaType == mt {
				y = append(y, r)
			}
		}
		if len(y) == len(x)+1 {
			last := y[len(y)-1]
			hasFb := false
			for _, r := range x {
				hasFb = hasFb || r.IsDefault
			}
			if hasFb || last.Name != catSimpleDefaultFallback(mt) {
				return false
			}
			y = y[:len(y)-1]
		}
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			p, q := x[i], y[i]
			if !catSameRule(&p, &q) {
				return false
			}
		}
	}
	return true
}

// catSameRule 两条规则名字与条件都一样（可选标记按集合比）
func catSameRule(p, q *model.CategoryRule) bool {
	t := strings.TrimSpace
	if p.Name != q.Name || t(p.CustomRegex) != t(q.CustomRegex) || t(p.Ext) != t(q.Ext) {
		return false
	}
	for _, f := range catFields {
		if t(*f.ptr(p)) != t(*f.ptr(q)) {
			return false
		}
	}
	po, qo := catOptional(p), catOptional(q)
	for _, f := range catFields {
		if !f.series && t(*f.ptr(p)) != "" && po[f.key] != qo[f.key] {
			return false
		}
	}
	return true
}

// yamlHasComments YAML 里有没有注释（简单模式保存会去掉它们，界面上提醒一句）
func yamlHasComments(src string) bool {
	var root yaml.Node
	if yaml.Unmarshal([]byte(src), &root) != nil {
		return false
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
			return true
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(&root)
}

// ---- 试一试：这部片会进哪个目录 ----

// catTrySkip 排在前面、没命中的一条
type catTrySkip struct {
	Index  int    `json:"index"` // 清单里第几条（0 起）
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// catTryResult 「试一试」的结果
type catTryResult struct {
	Dest string `json:"dest"`
	// Index 命中第几条（0 起）；-1 = 都没命中，进了「其余都放这里」或默认的「未分类」
	Index int          `json:"index"`
	Skips []catTrySkip `json:"skips"`
}

// catTry 按一份规则（还没保存的也行）算一部片的分类，同 classifyTargetIn 的口径，另带每条没命中的原因
func catTry(rows []model.CategoryRule, media *TmdbMedia) catTryResult {
	mt := "movie"
	if media.MediaType == "tv" {
		mt = "tv"
	}
	res := catTryResult{Index: -1, Skips: []catTrySkip{}}
	var list []model.CategoryRule
	for _, r := range rows {
		if r.MediaType == mt {
			list = append(list, r)
		}
	}
	for i := range list {
		if ok, series := matchCategoryDir(&list[i], media); ok {
			if list[i].IsDefault && i == len(list)-1 {
				break // 兜底：和简单模式的「其余都放这里」一样报成 -1
			}
			res.Dest, res.Index = catRuleDir(&list[i], series), i
			return res
		}
		res.Skips = append(res.Skips, catTrySkip{Index: i, Name: list[i].Name, Reason: categoryMissReason(&list[i], media)})
	}
	for _, r := range list {
		if r.IsDefault {
			res.Dest = libSubPath(r.Name)
			return res
		}
	}
	res.Dest = catSimpleDefaultFallback(mt)
	return res
}

// categoryMissReason 一条规则为什么没命中（matchCategory 判过不命中之后才调；口径跟着它改）
func categoryMissReason(cat *model.CategoryRule, media *TmdbMedia) string {
	if cat.Unsupported != "" {
		return "写了认不出的条件 " + cat.Unsupported
	}
	opt := catOptional(cat)
	normal, hasSeries := false, false
	var optMiss []string
	for _, f := range catFields {
		expr := strings.TrimSpace(*f.ptr(cat))
		if expr == "" {
			continue
		}
		if f.series {
			hasSeries = true
			continue
		}
		normal = true
		have := f.have(media)
		if ok, _ := catEval(expr, have, f.text); ok {
			continue
		}
		why := f.label + "：TMDB 上这部片没写"
		if len(have) > 0 {
			why = fmt.Sprintf("%s不符（要 %s，这部是 %s）", f.label, expr, catShortList(have))
		}
		if !opt[f.key] {
			return why
		}
		optMiss = append(optMiss, why)
	}
	switch {
	case len(optMiss) > 0:
		return "可选条件一个都没命中：" + strings.Join(optMiss, "；")
	case !normal && hasSeries:
		return "只写了系列条件，一个系列都没命中"
	case !normal && cat.CustomRegex != "":
		return "片名不符合正则"
	case !normal && cat.Ext != "":
		return "只写了文件后缀，目前不参与匹配"
	}
	return "没命中"
}

// catShortList 给人看的值列表（片名、演员可能很长，只列前几个）
func catShortList(have []string) string {
	if len(have) > 6 {
		return strings.Join(have[:6], ",") + fmt.Sprintf(" 等 %d 个", len(have))
	}
	return strings.Join(have, ",")
}
