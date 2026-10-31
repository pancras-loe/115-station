package api

import (
	"encoding/json"
	"errors"
	"strings"
)

// ==================== 每个库让本站做什么（2026-10-15，LIBRARY-ROLES-PLAN.md） ====================
//
// 库卡片只管「做不做」，功能页只管「怎么做」。阶段 1 只有一项：**谁来整理**。
//
//   - 本站整理（organize，默认）：分类规则 library / 转存入口 / 默认库把内容整理进来，目录结构是 库/分类/片目，
//     片目按二级分类认（libCategoryLayout 的分类表）。现有的库都是这种，所以没写过 lib_opts 的老部署零变化。
//   - 我自己整理（self）：本站**不往里放任何东西**（规则、转存入口都不能指向它，网盘文件页里不能整理 / 移动），
//     只同步 STRM、海报墙、刮削、探测。目录是用户自己起的，片目在第几层由用户**声明**（Depth），不推断：
//     平铺与多级分类混在一起时推断认不准（海报墙那条约束）。
//
// 存在单独的 setting key 里，**不塞进 full**：「账号与媒体库」整存整取 full，塞进去两边会互相冲掉
// （onLibsSaved 已经踩过一次）。按 cid 记：库在 115 上改名不影响它。
//
// 首个媒体库（full.cid）阶段 1 只能是本站整理：它还兼着「默认放这里」，拆开要等阶段 3（设计文档 §2.3）。

const libOptsKey = "lib_opts"

// libModeSelf 我自己整理
const libModeSelf = "self"

// libOpt 一个库的选项
type libOpt struct {
	Mode  string `json:"mode,omitempty"`  // 空 / organize = 本站整理；self = 我自己整理
	Depth int    `json:"depth,omitempty"` // 自己整理的库：片目在库下第几层（1 = 库/片目，2 = 库/分类/片目，3 = 再多一层分类）
	// Scrape 刮不刮：auto / manual / off；空 = 没设过，按老的全局开关换算（legacyLibScrape）
	Scrape string `json:"scrape,omitempty"`
	// Local 本地目录名（STRM 第一层、台账路径第一段、规则 library 认的名字，liblocal.go）。
	// 空 = 老行为：现读 115 目录名。定下来之后 115 上改名不再影响本地
	Local string `json:"local,omitempty"`
}

// libOptDepthMax 片目层级最多三层：再深基本是选错了，界面也只给这三档
const libOptDepthMax = 3

// self 是不是自己整理的库
func (o libOpt) self() bool { return o.Mode == libModeSelf }

// depth 归一过的层级：没写或越界按 2（本站整理出来的就是 库/分类/片目，改成自己整理时最常见）
func (o libOpt) depth() int {
	if o.Depth < 1 || o.Depth > libOptDepthMax {
		return 2
	}
	return o.Depth
}

// libOpts setting lib_opts 的全部内容
type libOpts struct {
	Libs map[string]libOpt `json:"libs,omitempty"` // 库 cid → 选项；没有的库按默认（本站整理）
	// Fallback 「默认放这里」的库（阶段 3）：二级分类里没写 library 的规则整理进它。
	// 空 / 已移除 / 改成自己整理了 → 取第一个本站整理的库（首个库在前，老部署就是首个库），见 fallbackOf。
	// 和首个库（full.cid）是两回事：整理记录 LibCid 记空表示首个库，不跟着它变，所以换它不用迁移老记录
	Fallback string `json:"fallback,omitempty"`
}

// fallbackOf 「默认放这里」的库 cid。cids 是配置里的各库（首个在前）；一个本站整理的库都没有返回空
func (o libOpts) fallbackOf(cids []string) string {
	first := ""
	for _, c := range cids {
		if o.of(c).self() {
			continue
		}
		if c == strings.TrimSpace(o.Fallback) {
			return c
		}
		if first == "" {
			first = c
		}
	}
	return first
}

// errNoOrganizeLib 一个本站整理的库都没有：整理线停用（定时整理、转存后整理、守望者、订阅）
var errNoOrganizeLib = errors.New("没有本站整理的媒体库，整理停着：要用整理，到「账号与媒体库 → 媒体库」把一个库改成本站整理")

// noOrganizeLibNow 配了媒体库、却一个本站整理的都没有（没配媒体库不算：那是另一个报错）
func noOrganizeLibNow() bool {
	if libs := loadLibraries(); len(libs) > 0 {
		return len(organizeLibs(libs)) == 0 // 媒体库表：设了转存文件夹的库才整理（orgconfigv2.go）
	}
	cids := parseFullCfg(settingValueCompat("full")).libCids()
	return len(cids) > 0 && loadLibOpts().fallbackOf(cids) == ""
}

func parseLibOpts(raw string) libOpts {
	var o libOpts
	_ = json.Unmarshal([]byte(raw), &o)
	return o
}

func loadLibOpts() libOpts { return parseLibOpts(settingValueCompat(libOptsKey)) }

// of 某个库的选项（没写过的库返回零值 = 本站整理）。
// 附加账号的库（key 是 a{账号}:{cid}，acctlibs.go）一律是「我自己整理」：本站不改它的网盘
func (o libOpts) of(cid string) libOpt {
	cid = strings.TrimSpace(cid)
	v := o.Libs[cid]
	if isAcctLibKey(cid) {
		v.Mode = libModeSelf
	}
	return v
}

// selfCids 自己整理的库（cid 集合）
func (o libOpts) selfCids() map[string]bool {
	out := map[string]bool{}
	for cid, v := range o.Libs {
		if v.self() {
			out[cid] = true
		}
	}
	return out
}

// selfLibDepths 自己整理的库：库名 → 片目层级。名字只读路径缓存与配置里的显示路径（零 115 请求），
// 认不出名字的库这里没有它 —— 它的片目暂时按分类规则认，等名字进了缓存（打开一次网盘文件页 / 跑一次同步）就对了
func selfLibDepths(fullRaw string, opts libOpts) map[string]int {
	if len(opts.Libs) == 0 {
		return nil
	}
	var out map[string]int
	for _, s := range libNameSources(fullRaw) {
		o := opts.of(s.Cid)
		if !o.self() {
			continue
		}
		if name := s.libRootOf().Name; name != "" {
			if out == nil {
				out = map[string]int{}
			}
			out[name] = o.depth()
		}
	}
	return out
}

// marshalLibOpts 只留还在配置里、且有设置（自己整理 / 设过刮削）的库。一个都没有时写 {} 而不是空串：
// 空串在 getSettingValue 里会退回去读数据库的老值
func marshalLibOpts(o libOpts, libCids []string) string {
	keep := map[string]bool{}
	for _, c := range libCids {
		keep[c] = true
	}
	out := libOpts{Libs: map[string]libOpt{}}
	if fb := strings.TrimSpace(o.Fallback); keep[fb] && !o.of(fb).self() {
		out.Fallback = fb
	}
	for cid, v := range o.Libs {
		v.Local = strings.TrimSpace(v.Local)
		if !keep[cid] || (!v.self() && v.Scrape == "" && v.Local == "") {
			continue
		}
		x := libOpt{Scrape: v.Scrape, Local: v.Local}
		if v.self() {
			x.Mode, x.Depth = libModeSelf, v.depth()
		}
		out.Libs[cid] = x
	}
	// 「默认放这里」就是第一个本站整理的库（自然默认）时不存：库的顺序或整理方式变了它跟着走，
	// 老部署也一直是这个口径
	if out.Fallback != "" && out.Fallback == (libOpts{Libs: out.Libs}).fallbackOf(libCids) {
		out.Fallback = ""
	}
	if len(out.Libs) == 0 && out.Fallback == "" {
		return "{}"
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ==================== 每个库刮不刮（阶段 2） ====================
//
// 卡片上三选一，取代原来全局的「刮削方式」（scrapeCfg.Provider）与「整理后 / 同步后自动刮削」两个开关：
//   - auto   自动刮新片：整理进来的、增量同步进来的新片目都刮，媒体信息补全也补它
//   - manual 只手动刮：只在海报墙勾选后刮，自动刮削与定时补全都不碰它
//   - off    不刮（交给 Emby）：本站一样元数据都不写（原来 provider=emby 那套挡法，按库生效），海报墙按 Emby 分级
//
// 没设过的库按老的全局开关换算（legacyLibScrape），所以升级后行为不变、也不用写迁移；
// 老字段保留不删，回退到旧版本照旧能读。唯一的口径变化：整理后与同步后合成了「自动刮新片」一项，
// 原来只开了整理后刮削的库，同步进来的新片也会刮（设计文档 §8-3，维护者已同意）。
//
// 刮削怎么做（写 NFO / 图片、强制覆盖、占位剧照…）仍是「影视刮削」页的全局配置：卡片管「做不做」，功能页管「怎么做」。

const (
	libScrapeAuto   = "auto"
	libScrapeManual = "manual"
	libScrapeOff    = "off"
)

func validLibScrape(s string) bool {
	return s == "" || s == libScrapeAuto || s == libScrapeManual || s == libScrapeOff
}

// legacyLibScrape 没设过刮削的库：按阶段 2 之前的全局开关换算。
// 定时「媒体信息补全」开着补刮也算自动：补全现在只补「自动刮新片」的库，不这么算的话
// 关了整理后 / 同步后刮削、靠定时补全兜底的老用户升级后补全就不补了
func legacyLibScrape(c scrapeCfg, metaFillScrapes bool) string {
	switch {
	case !c.stationScrapes():
		return libScrapeOff
	case c.AutoAfterOrganize || c.AutoAfterSync || metaFillScrapes:
		return libScrapeAuto
	}
	return libScrapeManual
}

// loadLegacyLibScrape 当前老配置换算出来的刮削方式
func loadLegacyLibScrape() string {
	m := defaultMetaFillCfg()
	if v := settingValueCompat(metaFillSetting); v != "" {
		_ = json.Unmarshal([]byte(v), &m)
	}
	return legacyLibScrape(loadScrapeCfg(), m.Enabled && m.Scrape)
}

// libScrapePolicy 一次读好的「哪个库刮不刮」：片目 key 第一段是库名。零 115 请求
type libScrapePolicy struct {
	byName  map[string]string
	primary string // 首个媒体库的方式：第一段认不出库名时用它（只有一个库的老台账不带库名）
	// shareRoot / shareMode 分享 STRM 根下的片目（sharestrmposter.go）：本站刮削开着按「只手动刮」，
	// 关着按「不刮」（交给 Emby、海报墙按 Emby 分级）。不按「自动刮」：媒体信息补全会去补刮、提前探测，
	// 每次探测都要取一次分享直链
	shareRoot, shareMode string
}

// loadLibScrapePolicy 当前配置。库名认不出（缓存里还没有）的库按首个库算 —— 和老台账同一个退路
func loadLibScrapePolicy() libScrapePolicy {
	p := buildLibScrapePolicy(libNameSources(settingValueCompat("full")), loadLibOpts(), loadLegacyLibScrape())
	cfg := loadShareStrmCfg()
	p.shareRoot, p.shareMode = cfg.Root, libScrapeOff
	if cfg.Scrape {
		p.shareMode = libScrapeManual
	}
	return p
}

// buildLibScrapePolicy legacy 是没设过刮削的库用的方式（loadLegacyLibScrape）
func buildLibScrapePolicy(sources []libNameSource, opts libOpts, legacy string) libScrapePolicy {
	p := libScrapePolicy{byName: map[string]string{}, primary: legacy}
	for i, s := range sources {
		mode := s.Lib.Scrape // 媒体库表（libstore.go）
		if mode == "" {
			mode = opts.of(s.Cid).Scrape
		}
		if mode == "" {
			mode = legacy
		}
		if i == 0 {
			p.primary = mode
		}
		if name := s.libRootOf().Name; name != "" {
			p.byName[name] = mode
		}
	}
	return p
}

// of 片目 key（或任何以库名开头的本地相对路径）所在库的刮削方式。零值策略（测试里没设）按自动算
func (p libScrapePolicy) of(key string) string {
	if p.shareRoot != "" {
		if k := strings.Trim(key, "/"); k == p.shareRoot || strings.HasPrefix(k, p.shareRoot+"/") {
			return p.shareMode
		}
	}
	lib, _ := splitLibRel(key)
	if m, ok := p.byName[lib]; ok {
		return m
	}
	if p.primary == "" {
		return libScrapeAuto
	}
	return p.primary
}

// stationScrapes 本站可以往这个片目写元数据（手动、自动都算）
func (p libScrapePolicy) stationScrapes(key string) bool { return p.of(key) != libScrapeOff }

// autoScrapes 这个片目进来时自动刮
func (p libScrapePolicy) autoScrapes(key string) bool { return p.of(key) == libScrapeAuto }

// any 有没有哪个库是这种方式
func (p libScrapePolicy) any(mode string) bool {
	if len(p.byName) == 0 {
		return p.primary == mode || (p.primary == "" && mode == libScrapeAuto)
	}
	for _, m := range p.byName {
		if m == mode {
			return true
		}
	}
	return false
}

// offLibsOf 这些片目里落在「不刮」库的库名（去重、按出现顺序），给报错用
func (p libScrapePolicy) offLibsOf(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if p.of(k) != libScrapeOff {
			continue
		}
		lib, _ := splitLibRel(k)
		if !seen[lib] {
			seen[lib] = true
			out = append(out, lib)
		}
	}
	return out
}

// embyGrades 海报墙按 Emby 分级的片目：所在库「不刮（交给 Emby）」
func (p libScrapePolicy) embyGrades(key string) bool { return p.of(key) == libScrapeOff }
