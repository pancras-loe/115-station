package api

// ==================== 洗版策略：简单设置、试一试、排序预览、体检 ====================
//
// 2026-10-23，设计见 docs/115-station-notes/WASH-SIMPLIFY-PLAN.md。
// 事实来源只有一份：ScrapeRule.wash_config 的 YAML。「简单设置」就是一种写法受限的 YAML
// （一条不限类型的策略，或电影 / 剧集各一条，都用 prefer、不分分类、不写 priority_level），
// 表单存的时候生成 YAML、读的时候从 YAML 认回来（simpleFormOf），认不回来就只能在专家模式里改。
// 这样订阅、找资源、整理读的都是同一份，不会两边各存一份互相覆盖（incr.cron 同款坑）。
//
// 预览接口全是纯计算：不读台账、不发 115 请求，拿的是请求里的草稿，不是已保存的配置。

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// washSimpleForm 界面「简单设置」：① 怎么办 / 旧版去向（电影剧集共用），②–⑤ 是偏好（可以分开设）
type washSimpleForm struct {
	Mode   string     `json:"mode"`   // replace / coexist / skip
	Group  bool       `json:"group"`  // coexist 时：每个分辨率各留一份（scope: group）
	Target string     `json:"target"` // redundant / delete
	Split  bool       `json:"split"`  // 剧集单独设置
	Movie  washPrefer `json:"movie"`  // 不分开时就是共用的那一套
	TV     washPrefer `json:"tv"`
}

func (f washSimpleForm) normalized() washSimpleForm {
	switch f.Mode {
	case "coexist", "skip":
	default:
		f.Mode = "replace"
	}
	if f.Mode != "coexist" {
		f.Group = false
	}
	if f.Target != "delete" {
		f.Target = "redundant"
	}
	f.Movie = f.Movie.normalized()
	if f.Split {
		f.TV = f.TV.normalized()
	} else {
		f.TV = f.Movie
	}
	return f
}

// washSimpleOut 生成 YAML 用：只写有意义的键
type washSimpleOut struct {
	Mode             string     `yaml:"mode"`
	Scope            string     `yaml:"scope,omitempty"`
	MediaType        string     `yaml:"media_type,omitempty"`
	OldVersionTarget string     `yaml:"old_version_target"`
	Prefer           washPrefer `yaml:"prefer"`
}

// yaml 表单 → YAML（带说明头）。yaml.v3 默认缩进 4，这里统一成 2，与默认模板一致
func (f washSimpleForm) yaml() string {
	f = f.normalized()
	scope := ""
	if f.Group {
		scope = "group"
	}
	type block struct {
		key string
		out washSimpleOut
	}
	blocks := []block{{"洗版策略", washSimpleOut{Mode: f.Mode, Scope: scope, OldVersionTarget: f.Target, Prefer: f.Movie}}}
	if f.Split {
		blocks = []block{
			{"电影洗版策略", washSimpleOut{Mode: f.Mode, Scope: scope, MediaType: "movie", OldVersionTarget: f.Target, Prefer: f.Movie}},
			{"剧集洗版策略", washSimpleOut{Mode: f.Mode, Scope: scope, MediaType: "tv", OldVersionTarget: f.Target, Prefer: f.TV}},
		}
	}
	var sb strings.Builder
	sb.WriteString(model.WashYAMLHeader)
	for i, b := range blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		_ = enc.Encode(b.out)
		_ = enc.Close()
		sb.WriteString(b.key + ":\n")
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			sb.WriteString("  " + line + "\n")
		}
	}
	return sb.String()
}

// simpleFormOf YAML → 表单；写法超出简单设置（按分类分开、分档规则、旧版去「已存在」…）时 ok=false
func simpleFormOf(src string) (washSimpleForm, bool) {
	list, err := parseWashYAML(src)
	if err != nil || len(list) == 0 || len(list) > 2 {
		return washSimpleForm{}, false
	}
	for _, n := range list {
		st := n.st
		if st.Prefer == nil || len(st.PriorityLevel) > 0 || strings.TrimSpace(st.Category) != "" {
			return washSimpleForm{}, false
		}
		switch st.Mode {
		case "", "replace", "coexist", "skip":
		default:
			return washSimpleForm{}, false
		}
		switch st.Scope {
		case "", "all":
		case "group":
			if st.Mode != "coexist" {
				return washSimpleForm{}, false
			}
		default:
			return washSimpleForm{}, false
		}
		switch st.OldVersionTarget {
		case "", "redundant", "delete":
		default:
			return washSimpleForm{}, false
		}
	}
	first := list[0].st
	f := washSimpleForm{Mode: first.Mode, Group: first.Scope == "group", Target: first.OldVersionTarget}
	if len(list) == 1 {
		if first.MediaType != "" {
			return washSimpleForm{}, false
		}
		f.Movie = *first.Prefer
		return f.normalized(), true
	}
	second := list[1].st
	if second.Mode != first.Mode || second.Scope != first.Scope || second.OldVersionTarget != first.OldVersionTarget {
		return washSimpleForm{}, false
	}
	byType := map[string]*washPrefer{first.MediaType: first.Prefer, second.MediaType: second.Prefer}
	if byType["movie"] == nil || byType["tv"] == nil {
		return washSimpleForm{}, false
	}
	f.Split, f.Movie, f.TV = true, *byType["movie"], *byType["tv"]
	return f.normalized(), true
}

// ==================== 默认策略迁移 ====================

const washSimpleMigrateKey = "migrate.wash_simple.v1"

// washSameStrategies 两份 YAML 的策略是否一样（不看注释与排版）
func washSameStrategies(a, b string) bool {
	la, errA := parseWashYAML(a)
	lb, errB := parseWashYAML(b)
	if errA != nil || errB != nil || len(la) != len(lb) {
		return false
	}
	for i := range la {
		ya, _ := yaml.Marshal(la[i].st)
		yb, _ := yaml.Marshal(lb[i].st)
		if la[i].key != lb[i].key || string(ya) != string(yb) {
			return false
		}
	}
	return true
}

// MigrateWashSimple 没改过老默认策略的部署换成新默认（一次性）。老默认会把 4K 判输给 1080p（见
// model.LegacyDefaultWashYAML），用户多半以为它在按画质洗。改过的、清空的（= 不洗版）一律不动，停在专家模式
func (h *Handler) MigrateWashSimple() {
	if h.Config == nil || h.getSettingValue(washSimpleMigrateKey) != "" {
		return
	}
	var rule model.ScrapeRule
	if err := h.DB.Where("type = ?", "wash_config").First(&rule).Error; err == nil &&
		strings.TrimSpace(rule.Config) != "" && washSameStrategies(rule.Config, model.LegacyDefaultWashYAML) {
		rule.Config = model.DefaultWashYAML
		if err := h.DB.Save(&rule).Error; err != nil {
			log.Printf("[迁移] ✗ 洗版策略：换成新默认失败，下次启动再试: %v", err)
			return
		}
		resetWashCache()
		log.Printf("[迁移] ✓ 洗版策略：老默认策略换成「简单设置」（越高越好、杜比视界无所谓、不要 HDTV 与枪版）")
	}
	if err := h.Config.SaveSetting(washSimpleMigrateKey, "done"); err != nil {
		log.Printf("[迁移] ✗ 洗版策略：记录迁移标记失败: %v", err)
	}
}

// ==================== 接口 ====================

func (h *Handler) ListWashRules(c *gin.Context) {
	// 从 ScrapeRule 表读取已保存的 YAML
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	resp := gin.H{"config": rule.Config, "default_config": model.DefaultWashYAML, "simple": nil}
	if f, ok := simpleFormOf(rule.Config); ok {
		resp["simple"] = f
	}
	def, _ := simpleFormOf(model.DefaultWashYAML)
	resp["default_simple"] = def
	c.JSON(http.StatusOK, resp)
}

// saveWashYAML 校验并保存，保存后立即失效缓存（不失效的话点完保存最多一分钟内还在按旧策略判）
func (h *Handler) saveWashYAML(src string) error {
	if _, err := parseWashYAML(src); err != nil {
		return fmt.Errorf("洗版策略格式错误: %w", err)
	}
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "wash_config").First(&rule)
	rule.Type = "wash_config"
	rule.Enabled = true
	rule.Config = src
	if err := h.DB.Save(&rule).Error; err != nil {
		return fmt.Errorf("保存洗版策略失败")
	}
	resetWashCache()
	return nil
}

// SaveWashRules 保存洗版策略（专家模式，YAML 字符串，对齐 CMS）
func (h *Handler) SaveWashRules(c *gin.Context) {
	var req struct {
		Yaml string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := h.saveWashYAML(req.Yaml); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// SaveWashSimple POST /scrape/wash/simple 保存简单设置（生成 YAML 覆盖原来那份）
func (h *Handler) SaveWashSimple(c *gin.Context) {
	var req struct {
		Form washSimpleForm `json:"form"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	src := req.Form.yaml()
	if err := h.saveWashYAML(src); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功", "config": src})
}

// washPreviewFile 试一试的一方；体积按 G 填
type washPreviewFile struct {
	Name   string  `json:"name"`
	SizeGB float64 `json:"size_gb"`
}

func (f washPreviewFile) file() washFile {
	return washFile{Name: strings.TrimSpace(f.Name), Size: int64(f.SizeGB * (1 << 30))}
}

// washLadderItem 排序预览的一行
type washLadderItem struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
	Rank   int      `json:"rank"`   // 第几名，和上一行一样好时同名次
	Tie    bool     `json:"tie"`    // 和上一行分不出
	Tier   int      `json:"tier"`   // 档：偏好 0 最高分辨率 / 1 其余 / -1 不要；分档规则是命中第几条，-1 没命中
	Avoid  string   `json:"avoid"`  // 偏好里「不要」的原因
	Prefer bool     `json:"prefer"` // 这条策略用的是偏好（界面按「不要」显示 -1）
}

// washVerdict 试一试的结论
type washVerdict struct {
	Kind string `json:"kind"` // good 会换 / keep 不换 / both 两份都留 / info 比不了
	Head string `json:"head"`
	Text string `json:"text"`
}

// washLintItem 专家模式的语义体检（只提醒，不拦保存）
type washLintItem struct {
	Strategy string `json:"strategy"`
	Level    string `json:"level"` // warn / info
	Text     string `json:"text"`
}

// PreviewWash POST /scrape/wash/preview 排序预览 + 试一试 + 体检。草稿二选一：form（简单设置）或 yaml（专家模式）
func (h *Handler) PreviewWash(c *gin.Context) {
	var req struct {
		Form      *washSimpleForm  `json:"form"`
		Yaml      *string          `json:"yaml"`
		MediaType string           `json:"media_type"`
		Old       *washPreviewFile `json:"old"`
		New       *washPreviewFile `json:"new"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.MediaType != "tv" {
		req.MediaType = "movie"
	}
	src := ""
	switch {
	case req.Form != nil:
		src = req.Form.yaml()
	case req.Yaml != nil:
		src = *req.Yaml
	}
	raw, err := parseWashYAML(src)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "YAML 格式错误：" + err.Error()})
		return
	}
	list := make([]namedWashStrategy, 0, len(raw))
	for _, n := range raw {
		list = append(list, namedWashStrategy{n.key, normalizeWashStrategy(n.st)})
	}
	resp := gin.H{"strategy": "", "ladder": []washLadderItem{}, "lint": []washLintItem{}, "verdict": nil}
	if req.Form == nil {
		resp["lint"] = washSemanticLint(raw, list)
	}
	pick := washPreviewPick(list, req.MediaType)
	if pick == nil {
		resp["verdict"] = &washVerdict{Kind: "info", Head: "没有洗版策略", Text: "整理时不比画质：同一份文件（sha1 相同）已在库里就移「已存在」，其余照常入库。"}
		c.JSON(http.StatusOK, resp)
		return
	}
	resp["strategy"] = pick.key
	resp["ladder"] = washLadder(&pick.st, req.MediaType)
	if req.Old != nil && req.New != nil && strings.TrimSpace(req.Old.Name) != "" && strings.TrimSpace(req.New.Name) != "" {
		resp["verdict"] = washTryVerdict(&pick.st, req.MediaType, req.Old.file(), req.New.file())
	}
	c.JSON(http.StatusOK, resp)
}

// washPreviewPick 预览用哪条：不知道分类，同 washStrategyForType（不限分类的优先）
func washPreviewPick(list []namedWashStrategy, mediaType string) *namedWashStrategy {
	for i := range list {
		st := list[i].st
		if (st.MediaType == "" || st.MediaType == mediaType) && strings.TrimSpace(st.Category) == "" {
			return &list[i]
		}
	}
	for i := range list {
		if list[i].st.MediaType == "" || list[i].st.MediaType == mediaType {
			return &list[i]
		}
	}
	return nil
}

// washSampleNames 排序预览用的一组常见资源（剧集换成同一集的写法，洗版只和同一集比）
var washSampleNames = []string{
	"2160p.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.7.1-FRDS",
	"2160p.BluRay.HDR10.x265-WiKi",
	"2160p.BluRay.HDR10.x265-CMCT",
	"2160p.WEB-DL.DV.H265-HHWEB",
	"2160p.WEB-DL.HDR10+.H265-ADWeb",
	"2160p.WEB-DL.H265-ADWeb",
	"1080p.BluRay.REMUX.AVC-FRDS",
	"1080p.BluRay.x265-WiKi",
	"1080p.BluRay.x264-CMCT",
	"1080p.WEB-DL.H264-ADWeb",
	"1080p.HDTV.H264-CNHK",
	"720p.WEB-DL.H264-ADWeb",
	"480p.DVDRip.x264-XXX",
	"1080p.HDTC.H264-XXX",
}

func washSamples(mediaType string) []string {
	prefix := "Dune.Part.Two.2024."
	if mediaType == "tv" {
		prefix = "Shogun.S01E03."
	}
	out := make([]string, len(washSampleNames))
	for i, n := range washSampleNames {
		out[i] = prefix + n + ".mkv" // 带扩展名：ParseResourceInfo 认发布组前会去掉最后一段
	}
	return out
}

// washLabels 一个名字认出来的几项（界面上的小标签）
func washLabels(name string) []string {
	ri := ParseResourceInfo(name)
	q := qualityOf(name)
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	add(ri.Pix)
	switch t := q[wqType]; {
	case t > 0:
		add([]string{"", "DVDRip", "HDTV", "WEBRip", "WEB-DL", "BluRay", "REMUX"}[t])
	case condTokenHit(strings.ToLower(name), "", "hdtc"):
		add("枪版")
	}
	if q[wqEffect] == 4 {
		add("DV")
	}
	if h := washHDRNoDV(name); h > 0 && !(q[wqEffect] == 4 && h == 1 && !strings.Contains(strings.ToUpper(ri.Effect), "HDR")) {
		add(washHDRNames[h])
	}
	add(strings.SplitN(ri.VideoEncode, ".", 2)[0])
	add(ri.Team)
	return out
}

// washLadder 一组常见资源按这条策略从好到差排（稳定排序，分不出的同名次）
func washLadder(st *washStrategy, mediaType string) []washLadderItem {
	names := washSamples(mediaType)
	rules := st.PriorityLevel
	sort.SliceStable(names, func(i, j int) bool {
		c, _ := washCompare(names[i], names[j], rules)
		return c > 0
	})
	p := preferOf(rules)
	items := make([]washLadderItem, 0, len(names))
	for i, n := range names {
		it := washLadderItem{Name: n, Labels: washLabels(n), Rank: i + 1, Tier: washTier(n, rules), Prefer: p != nil}
		if i > 0 {
			if c, _ := washCompare(names[i-1], n, rules); c == 0 {
				it.Tie, it.Rank = true, items[i-1].Rank
			}
		}
		if p != nil {
			it.Avoid = strings.Join(p.avoidedBy(n), "、")
		}
		items = append(items, it)
	}
	return items
}

// washTryVerdict 试一试：用整理时同一个 decideWash 判（库里那份当作一行台账），话说成人话
func washTryVerdict(st *washStrategy, mediaType string, oldF, newF washFile) *washVerdict {
	if mediaType == "tv" {
		n, o := parseFileName(newF.Name), parseFileName(oldF.Name)
		if n.Episode <= 0 || o.Episode <= 0 {
			return &washVerdict{Kind: "info", Head: "比不了", Text: "剧集只和库里同一集比，两个文件名里都要有集号（比如 S01E03）。"}
		}
		if !sameWashEpisode(newF.Name, oldF.Name) {
			return &washVerdict{Kind: "both", Head: "不是同一集", Text: "两个文件不是同一集，新来的按新增集照常入库，不做洗版。"}
		}
	}
	dir := "预览/Season 01"
	if mediaType == "movie" {
		dir = "预览"
	}
	lib := []model.SyncedFile{{RelPath: dir + "/" + oldF.Name + ".strm", Kind: "video", Size: oldF.Size}}
	var lines []string
	plan := decideWash(&TmdbMedia{Title: "预览", MediaType: mediaType}, newF, "", dir, st, lib, nil, func(s string) { lines = append(lines, s) })
	_, why := washCompareFiles(newF, oldF, st.PriorityLevel)
	switch plan.decision {
	case washReplaced:
		dest := "冗余目录"
		switch st.OldVersionTarget {
		case "delete":
			dest = "115 回收站"
		case "existing":
			dest = "「已存在」"
		}
		return &washVerdict{Kind: "good", Head: "会换成新来的", Text: "新来的更好（" + why + "）。库里那份移到" + dest + "。"}
	case washNotBetter:
		if st.Mode == "skip" {
			return &washVerdict{Kind: "keep", Head: "不收新的", Text: "这条策略是「不要新的」：库里已经有了，新来的移到「已存在」。"}
		}
		if c, _ := washCompareFiles(newF, oldF, st.PriorityLevel); c == 0 {
			return &washVerdict{Kind: "keep", Head: "不换", Text: "两份分不出高下（" + why + "），换了也没好处。新来的移到「已存在」。"}
		}
		return &washVerdict{Kind: "keep", Head: "不换，留着库里的", Text: "新来的不如库里的（" + why + "）。新来的移到「已存在」。"}
	}
	text := "两份都留着，新来的照常入库。"
	if st.Scope == "group" && ParseResourceInfo(newF.Name).Pix != ParseResourceInfo(oldF.Name).Pix {
		text = "分辨率不同，按分辨率各留一份，新来的照常入库。"
	}
	return &washVerdict{Kind: "both", Head: "两份都留着", Text: text}
}

// washSemanticLint 拿常见资源跑一遍，找出多半不是本意的结果（专家模式）。raw 是原样解析的，list 是引擎实际跑的
func washSemanticLint(raw, list []namedWashStrategy) []washLintItem {
	out := []washLintItem{}
	for i, n := range list {
		st, r := n.st, raw[i].st
		if r.Prefer != nil && len(r.PriorityLevel) > 0 {
			out = append(out, washLintItem{n.key, "warn", "写了 prefer，priority_level 不再生效"})
		}
		switch r.Mode {
		case "max_size", "min_size":
			want := "大"
			if r.Mode == "min_size" {
				want = "小"
			}
			out = append(out, washLintItem{n.key, "info", fmt.Sprintf("%s 按 replace 处理，画质命中同一条时留体积%s的（第一条规则补上 size_prefer）", r.Mode, want)})
		}
		if st.Prefer != nil || st.Mode == "coexist" || st.Mode == "skip" || len(st.PriorityLevel) == 0 {
			continue
		}
		mt := st.MediaType
		if mt == "" {
			mt = "movie"
		}
		names := washSamples(mt)
		label := func(s string) string { return strings.Join(washLabels(s), " ") }
		var inv []string
		seen := map[string]bool{}
		for _, a := range names {
			qa := qualityOf(a)
			for _, b := range names {
				qb := qualityOf(b)
				// 只看正经资源（WEB-DL 及以上、1080p 及以上），HDTV / 枪版输给谁都不奇怪
				if qa[wqPix] <= qb[wqPix] || qb[wqPix] < 4 || qa[wqType] < 4 || qb[wqType] < 4 {
					continue
				}
				if c, _ := washCompare(a, b, st.PriorityLevel); c < 0 {
					k := label(a) + "|" + label(b)
					if !seen[k] {
						seen[k] = true
						inv = append(inv, fmt.Sprintf("%s 会输给 %s（前者%s，后者%s）", label(a), label(b), washTierText(washTier(a, st.PriorityLevel)), washTierText(washTier(b, st.PriorityLevel))))
					}
				}
			}
		}
		if len(inv) > 0 {
			text := "更高的分辨率输给了更低的：" + strings.Join(inv[:min(3, len(inv))], "；")
			if len(inv) > 3 {
				text += fmt.Sprintf("；还有 %d 组", len(inv)-3)
			}
			out = append(out, washLintItem{n.key, "warn", text + "。没命中任何一条规则算最差，多半不是本意；不想要 4K 的话改用简单设置里的「最高 1080p」。"})
		}
		var miss []string
		for _, s := range names {
			q := qualityOf(s)
			if q[wqPix] >= 4 && q[wqType] >= 4 && washTier(s, st.PriorityLevel) < 0 {
				miss = append(miss, label(s))
			}
		}
		if len(miss) > 0 {
			out = append(out, washLintItem{n.key, "warn", "这些常见资源一条规则都不中，会被当作最差：" + strings.Join(miss, "、")})
		}
	}
	return out
}

func washTierText(t int) string {
	if t < 0 {
		return "没命中任何一条"
	}
	return fmt.Sprintf("命中第 %d 条", t+1)
}
