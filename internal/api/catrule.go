package api

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"115-station/internal/model"

	"gopkg.in/yaml.v3"
)

// ==================== 二级分类的条件字段（2026-10-30） ====================
//
// 写法对齐 nanl wiki 的分类语法（https://wiki.nanl.top/syntax/category），在 MoviePilot category.yaml 之上多了：
//   - 值的前缀：无前缀 = 候选（命中其一），`+` = 必须包含，`-` / `!` = 排除。只要写了候选，就得命中至少一个，
//     所以「动作且非恐怖」要写 `+28,-27`，写成 `+28,12,-27` 还要求命中 12；
//   - 分隔符：`,` `，` `;` `；` 和换行都行（原来只认半角逗号，写了中文逗号整串当一个值、永远不命中）；
//   - 字段名前加 `?`（或 `？`）是可选条件：可选的之间「或」、至少命中一个，必填的仍然全部要满足；
//   - keywords 按片名（标题 / 原名 / 别名 / 译名）、include_keywords 按 TMDB 标签，都是不分大小写的子串；
//   - series_keywords / series_actors / series_directors 命中时在分类目录下再加一层「X系列」，X 是命中的那个值。
//     只写了系列字段的规则必须命中系列才算命中；和别的条件写在一起时系列只决定加不加这一层。
//
// 字段的增减只改 catFields：解析、写回 YAML、匹配、没命中的原因、简单模式的比对都从这张表走。

// catField 一个条件字段
type catField struct {
	key, label string
	ptr        func(*model.CategoryRule) *string
	have       func(*TmdbMedia) []string
	text       bool // 子串匹配（不支持区间）；否则整值比较
	series     bool // 系列字段：命中时加子目录，不参与必填 / 可选
}

var catFields = []catField{
	{key: "genre_ids", label: "类型", ptr: func(r *model.CategoryRule) *string { return &r.GenreIds }, have: func(m *TmdbMedia) []string { return intStrings(m.GenreIDs) }},
	{key: "original_language", label: "原语言", ptr: func(r *model.CategoryRule) *string { return &r.OriginalLanguage }, have: func(m *TmdbMedia) []string { return nonEmpty(m.OrigLanguage) }},
	{key: "origin_country", label: "地区", ptr: func(r *model.CategoryRule) *string { return &r.OriginCountry }, have: func(m *TmdbMedia) []string { return m.OrigCountry }},
	{key: "production_countries", label: "制片国家", ptr: func(r *model.CategoryRule) *string { return &r.ProductionCountries }, have: func(m *TmdbMedia) []string { return m.ProdCountry }},
	{key: "release_year", label: "年份", ptr: func(r *model.CategoryRule) *string { return &r.ReleaseYear }, have: func(m *TmdbMedia) []string { return nonEmpty(m.Year) }},
	{key: "keywords", label: "片名关键词", ptr: func(r *model.CategoryRule) *string { return &r.Keywords }, have: catTitleHay, text: true},
	{key: "include_keywords", label: "TMDB 标签", ptr: func(r *model.CategoryRule) *string { return &r.IncludeKeywords }, have: func(m *TmdbMedia) []string { return m.Keywords }, text: true},
	{key: "series_keywords", label: "系列（片名）", ptr: func(r *model.CategoryRule) *string { return &r.SeriesKeywords }, have: catTitleHay, text: true, series: true},
	{key: "series_actors", label: "系列（演员）", ptr: func(r *model.CategoryRule) *string { return &r.SeriesActors }, have: func(m *TmdbMedia) []string { return firstN(m.Cast, catSeriesPeople) }, text: true, series: true},
	{key: "series_directors", label: "系列（导演）", ptr: func(r *model.CategoryRule) *string { return &r.SeriesDirectors }, have: func(m *TmdbMedia) []string { return firstN(m.Directors, catSeriesPeople) }, text: true, series: true},
}

// catSeriesPeople 演员 / 导演只看前几位（排在后面的客串不该把片拉进别人的系列）
const catSeriesPeople = 6

// catLegacyKeys 不在 catFields 里、但 YAML 里认的键：custom_regex（本站自己的）、ext（不参与匹配）、library（作废，只为老数据）
var catLegacyKeys = []string{"custom_regex", "ext", "library"}

func catFieldOf(key string) (catField, bool) {
	for _, f := range catFields {
		if f.key == key {
			return f, true
		}
	}
	return catField{}, false
}

// catKnownKeys 给人看的可用字段清单
func catKnownKeys() string {
	keys := make([]string, 0, len(catFields)+2)
	for _, f := range catFields {
		keys = append(keys, f.key)
	}
	return strings.Join(append(keys, "custom_regex", "ext"), " / ")
}

// catTitleHay 片名干草堆：标题、原名、别名与各语言译名
func catTitleHay(m *TmdbMedia) []string {
	out := make([]string, 0, 2+len(m.Names))
	for _, s := range append([]string{m.Title, m.OriginalTitle}, m.Names...) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstN(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// catSplitKey YAML 里的字段名：去掉可选前缀 ? / ？
func catSplitKey(raw string) (key string, optional bool) {
	raw = strings.TrimSpace(raw)
	for _, p := range []string{"?", "？"} {
		if strings.HasPrefix(raw, p) {
			return strings.TrimSpace(raw[len(p):]), true
		}
	}
	return raw, false
}

// catTerm 一个取值
type catTerm struct {
	op byte // 0 候选，'+' 必须包含，'-' 排除
	v  string
}

// catTerms 拆开一个条件的取值。前缀后面没值的（单写一个 +）返回 op 带空值，校验时拦下，匹配时跳过
func catTerms(expr string) []catTerm {
	f := strings.FieldsFunc(expr, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r'
	})
	out := make([]catTerm, 0, len(f))
	for _, t := range f {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		var op byte
		switch {
		case strings.HasPrefix(t, "+"):
			op, t = '+', t[1:]
		case strings.HasPrefix(t, "-"), strings.HasPrefix(t, "!"):
			op, t = '-', t[1:]
		case strings.HasPrefix(t, "！"):
			op, t = '-', strings.TrimPrefix(t, "！")
		}
		out = append(out, catTerm{op: op, v: strings.TrimSpace(t)})
	}
	return out
}

// catTermIn 一个取值是否命中条目上的某个值
func catTermIn(term string, have []string, text bool) bool {
	if text {
		t := strings.ToLower(term)
		return slices.ContainsFunc(have, func(v string) bool { return strings.Contains(strings.ToLower(v), t) })
	}
	t := strings.ToUpper(term)
	return slices.ContainsFunc(have, func(v string) bool { return categoryTermHas(t, strings.ToUpper(strings.TrimSpace(v))) })
}

// catEval 一个条件是否成立；hit 是按书写顺序第一个命中的候选值（系列名取它，`+` 的值不算）。
// 条目上这个字段没数据时不成立（只写了排除也一样，MoviePilot 同款）
func catEval(expr string, have []string, text bool) (ok bool, hit string) {
	if len(have) == 0 {
		return false, ""
	}
	want := false
	for _, t := range catTerms(expr) {
		if t.v == "" {
			continue
		}
		in := catTermIn(t.v, have, text)
		switch t.op {
		case '+':
			if !in {
				return false, ""
			}
		case '-':
			if in {
				return false, ""
			}
		default:
			want = true
			if in && hit == "" {
				hit = t.v
			}
		}
	}
	return !want || hit != "", hit
}

// catOptional 规则里写成 ?字段 的那些
func catOptional(cat *model.CategoryRule) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(cat.Optional, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

// catRuleEmpty 一个条件都没写（兜底）
func catRuleEmpty(r *model.CategoryRule) bool {
	for _, f := range catFields {
		if strings.TrimSpace(*f.ptr(r)) != "" {
			return false
		}
	}
	return r.CustomRegex == "" && r.Ext == "" && r.Unsupported == ""
}

// catRuleHasSeries 规则写了系列字段（分类目录下面可能还有一层「X系列」，片目层判定要知道，见 ledger.go）
func catRuleHasSeries(r *model.CategoryRule) bool {
	for _, f := range catFields {
		if f.series && strings.TrimSpace(*f.ptr(r)) != "" {
			return true
		}
	}
	return false
}

// matchCategoryDir 规则是否命中，另带命中的系列名（没有系列为空）
func matchCategoryDir(cat *model.CategoryRule, media *TmdbMedia) (bool, string) {
	if cat.Unsupported != "" {
		return false, "" // 写了认不出的条件，宁可不匹配也不当兜底
	}
	series, hasSeries := "", false
	for _, f := range catFields {
		expr := strings.TrimSpace(*f.ptr(cat))
		if !f.series || expr == "" {
			continue
		}
		hasSeries = true
		if ok, hit := catEval(expr, f.have(media), f.text); ok && hit != "" && series == "" {
			series = hit
		}
	}
	if cat.CustomRegex != "" {
		if re, err := regexp.Compile(cat.CustomRegex); err == nil && (re.MatchString(media.Title) || re.MatchString(media.OriginalTitle)) {
			return true, series // 本站自己的：正则命中即归此类，不要求其他条件同时成立
		}
	}
	opt := catOptional(cat)
	normal, optN, optHit := false, 0, 0
	for _, f := range catFields {
		expr := strings.TrimSpace(*f.ptr(cat))
		if f.series || expr == "" {
			continue
		}
		normal = true
		ok, _ := catEval(expr, f.have(media), f.text)
		if opt[f.key] {
			optN++
			if ok {
				optHit++
			}
		} else if !ok {
			return false, ""
		}
	}
	if !normal {
		if hasSeries {
			return series != "", series // 纯系列规则：命中系列才算
		}
		// 只写了正则（没命中）或只写了 ext：没有任何条件成立；什么都没写是兜底
		return cat.CustomRegex == "" && cat.Ext == "", ""
	}
	if optN > 0 && optHit == 0 {
		return false, ""
	}
	return true, series
}

// catSeriesDir 系列子目录名：「X系列」，X 已经以「系列」结尾不重复加
func catSeriesDir(name string) string {
	name = strings.Trim(sanitizeName(name), ". ")
	if name == "" {
		return ""
	}
	if !strings.HasSuffix(name, "系列") {
		name += "系列"
	}
	return name
}

// catRuleDir 命中这条规则后的分类目录（库内相对路径）：分类名 + 系列子目录
func catRuleDir(cat *model.CategoryRule, series string) string {
	return libSubPath(cat.Name, catSeriesDir(series))
}

// ---- 片目层：分类目录下的系列子目录也是分类 ----

// catSeriesMark 分类目录表（libCategoryLayout.cats）里标记「这个分类下面可能有 X系列 子目录」的键尾。
// 系列名是整理时现算的，表里列不全，所以只记一个标记，查的时候认（catLookup）。不可能是目录名
const catSeriesMark = "\x00系列"

func catSeriesMarkKey(dir string) string {
	if dir = libSubPath(dir); dir == "" {
		return catSeriesMark
	}
	return dir + "/" + catSeriesMark
}

// catLookup 在分类目录表里查一个库内相对路径：分类目录本身，或写了系列字段的分类下面的「X系列」
func catLookup(cats map[string]string, rel string) (string, bool) {
	if t, ok := cats[rel]; ok {
		return t, true
	}
	if !strings.HasSuffix(rel, "系列") {
		return "", false
	}
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	t, ok := cats[catSeriesMarkKey(parent)]
	return t, ok
}

// ---- 保存前：校验与兜底挪到最后 ----

var catYearTerm = regexp.MustCompile(`^\d{4}(\s*-\s*\d{4})?$`)

// catValidateYAML 保存前查一遍（只拦写错的，老配置里能用的写法照旧放行）：
// 认不出的字段、空值、前缀后面没值、年份格式、正则写错、同一条里一个字段写两次。返回给人看的错误，一条一个
func catValidateYAML(src string) []string {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return []string{"YAML 格式错误：" + err.Error()}
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	var errs []string
	mp := root.Content[0]
	for i := 0; i+1 < len(mp.Content); i += 2 {
		mt := mp.Content[i].Value
		if mt != "movie" && mt != "tv" || mp.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		rules := mp.Content[i+1]
		for j := 0; j+1 < len(rules.Content); j += 2 {
			name, fields := rules.Content[j].Value, rules.Content[j+1]
			if fields.Kind != yaml.MappingNode {
				continue
			}
			at := func(n *yaml.Node, format string, a ...any) {
				errs = append(errs, fmt.Sprintf("第 %d 行（%s「%s」）：", n.Line, mediaLabel(mt), name)+fmt.Sprintf(format, a...))
			}
			seen := map[string]bool{}
			for k := 0; k+1 < len(fields.Content); k += 2 {
				kn, vn := fields.Content[k], fields.Content[k+1]
				key, _ := catSplitKey(kn.Value)
				if key == "library" {
					continue
				}
				_, known := catFieldOf(key)
				switch {
				case key == "year":
					at(kn, "year 已不用了，改成 release_year（写法 2020 或 2000-2009）")
					continue
				case !known && !slices.Contains(catLegacyKeys, key):
					at(kn, "「%s」不是分类条件（可用：%s）", key, catKnownKeys())
					continue
				}
				if seen[key] {
					at(kn, "%s 写了两次（带不带 ? 都算同一个），合成一行", key)
					continue
				}
				seen[key] = true
				val, ok := categoryYAMLValue(vn)
				if !ok {
					at(kn, "%s 的值写成了认不出的形态，写成 '值1,值2' 或列表", key)
					continue
				}
				if strings.TrimSpace(val) == "" {
					at(kn, "%s 没有值，不要这个条件就把这一行删掉", key)
					continue
				}
				switch key {
				case "custom_regex":
					if _, err := regexp.Compile(val); err != nil {
						at(kn, "custom_regex 正则写错了：%v", err)
					}
					continue
				case "ext":
					continue
				}
				for _, t := range catTerms(val) {
					if t.v == "" {
						at(kn, "%s 里「+」「-」「!」后面要跟值", key)
						break
					}
					if key == "release_year" && !catYearTerm.MatchString(t.v) {
						at(kn, "release_year 要写四位年份或范围（2020、2000-2009），「%s」不是", t.v)
						break
					}
				}
			}
		}
	}
	return errs
}

// catMoveFallbacks 把没写条件的规则（兜底）挪到各自那一段的最后（nanl 同款）：
// 写在中间的兜底会接走后面所有的片，后面的规则永远轮不到。几条兜底之间保持原顺序（第一条接住剩下的）。
// 没挪动时原样返回，挪了重新编码（注释跟着各自的键走）
func catMoveFallbacks(src string) (string, bool) {
	var root yaml.Node
	if yaml.Unmarshal([]byte(src), &root) != nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return src, false
	}
	moved := false
	mp := root.Content[0]
	for i := 0; i+1 < len(mp.Content); i += 2 {
		if mt := mp.Content[i].Value; mt != "movie" && mt != "tv" || mp.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		rules := mp.Content[i+1]
		var keep, tail []*yaml.Node
		for j := 0; j+1 < len(rules.Content); j += 2 {
			pair := rules.Content[j : j+2]
			if catNodeEmpty(pair[1]) && strings.TrimSpace(pair[0].Value) != "" {
				tail = append(tail, pair...)
				continue
			}
			if len(tail) > 0 {
				moved = true
			}
			keep = append(keep, pair...)
		}
		rules.Content = append(keep, tail...)
	}
	if !moved {
		return src, false
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if enc.Encode(&root) != nil {
		return src, false
	}
	_ = enc.Close()
	return buf.String(), true
}

// catNodeEmpty 规则的值是不是「没写条件」：空值，或只写了 library（不是条件）
func catNodeEmpty(n *yaml.Node) bool {
	if yamlNullNode(n) {
		return true
	}
	if n.Kind != yaml.MappingNode {
		return false
	}
	for k := 0; k+1 < len(n.Content); k += 2 {
		if key, _ := catSplitKey(n.Content[k].Value); key != "library" {
			return false
		}
	}
	return true
}

// catFallbackNote 挪了兜底时给用户的一句话
const catFallbackNote = "兜底空规则保存时会自动移动到最后"

// prepCategoryYAML 保存前：校验，再把兜底挪到最后
func prepCategoryYAML(src string) (string, string, error) {
	if errs := catValidateYAML(src); len(errs) > 0 {
		return "", "", fmt.Errorf("分类规则有写错的地方：\n%s", strings.Join(errs, "\n"))
	}
	if out, moved := catMoveFallbacks(src); moved {
		return out, catFallbackNote, nil
	}
	return src, "", nil
}

// ---- TMDB 详情里分类要用的部分 ----

// tmdbClassExtra 详情 append_to_response=alternative_titles,keywords,credits 里分类用得上的
type tmdbClassExtra struct {
	Names     []string // 别名（只有 alternative_titles；detailOf 另有译名，用它自己的 Names）
	Keywords  []string
	Cast      []string
	Directors []string
}

// parseTmdbClassExtra 从详情原文里取标签、演员、导演与别名。剧集的标签在 keywords.results、电影在 keywords.keywords；
// 剧集的导演按集记在 crew 里多半是空的，主创 created_by 排在前面一起算
func parseTmdbClassExtra(body []byte) tmdbClassExtra {
	type named struct {
		Name         string `json:"name"`
		OriginalName string `json:"original_name"`
		Title        string `json:"title"`
		Job          string `json:"job"`
	}
	var raw struct {
		Keywords struct {
			Keywords []named `json:"keywords"`
			Results  []named `json:"results"`
		} `json:"keywords"`
		Credits struct {
			Cast []named `json:"cast"`
			Crew []named `json:"crew"`
		} `json:"credits"`
		CreatedBy         []named `json:"created_by"`
		AlternativeTitles struct {
			Titles  []named `json:"titles"`
			Results []named `json:"results"`
		} `json:"alternative_titles"`
	}
	var out tmdbClassExtra
	if json.Unmarshal(body, &raw) != nil {
		return out
	}
	person := func(n named) string {
		// 中文界面下 name 是译名、original_name 是原名，两个都放进去（子串匹配，哪个写法都能命中）
		if n.OriginalName != "" && n.OriginalName != n.Name {
			return n.Name + " / " + n.OriginalName
		}
		return n.Name
	}
	for _, k := range append(raw.Keywords.Keywords, raw.Keywords.Results...) {
		if k.Name != "" {
			out.Keywords = append(out.Keywords, k.Name)
		}
	}
	for _, c := range raw.Credits.Cast {
		if c.Name != "" {
			out.Cast = append(out.Cast, person(c))
		}
	}
	for _, c := range raw.CreatedBy {
		if c.Name != "" {
			out.Directors = append(out.Directors, person(c))
		}
	}
	for _, c := range raw.Credits.Crew {
		if c.Job == "Director" && c.Name != "" {
			out.Directors = append(out.Directors, person(c))
		}
	}
	for _, t := range append(raw.AlternativeTitles.Titles, raw.AlternativeTitles.Results...) {
		if t.Title != "" {
			out.Names = append(out.Names, t.Title)
		}
	}
	return out
}
