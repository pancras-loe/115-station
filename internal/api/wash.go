package api

// ==================== 洗版策略（版本比较替换）====================
//
// 命中已存在时不再一律移「已存在」：
//   1. 取库内该片的现有文件名（SyncedFile 台账，记录在 TargetPath 之下）
//   2. 与待整理文件按优先级规则逐条比较（制作组/分辨率/来源/效果）
//   3. replace 无优先级时新替旧，有优先级时新版更好才替换；旧版按配置
//      移到冗余/已存在/115 回收站，新版正常入库；有规则但平局则新版移已存在

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gopkg.in/yaml.v3"
)

// washRule 单条优先级规则（YAML priority_level 数组元素，字段与 CMS 一致）
type washRule struct {
	ResourceTeam   string `yaml:"resource_team" json:"resource_team"`
	ResourcePix    string `yaml:"resource_pix" json:"resource_pix"`
	ResourceType   string `yaml:"resource_type" json:"resource_type"`
	ResourceEffect string `yaml:"resource_effect" json:"resource_effect"`
	// 编码两项默认模板的注释里一直写着可用，但结构体里没有——yaml.v3 静默
	// 丢弃未知字段，用户按说明写的 video_encode/audio_encode 从来没参与过比较
	VideoEncode string `yaml:"video_encode" json:"video_encode"`
	AudioEncode string `yaml:"audio_encode" json:"audio_encode"`
}

// washStrategy 一条完整洗版策略（UI 的 YAML 编辑器格式，与 CMS 对齐）
type washStrategy struct {
	Mode             string     `yaml:"mode"`               // coexist/skip/replace/max_size/min_size
	Scope            string     `yaml:"scope"`              // all=全局一个版本 / group=按分辨率分组各留一个
	MediaType        string     `yaml:"media_type"`         // movie/tv（空=匹配所有）
	Category         string     `yaml:"category"`           // 匹配二级分类名，逗号分隔（空=所有）
	PriorityLevel    []washRule `yaml:"priority_level"`     // 优先级规则（上面的优先）
	OldVersionTarget string     `yaml:"old_version_target"` // 旧版去向 redundant/existing/delete（115 回收站）
}

// loadWashStrategies 从 UI 保存的 YAML（ScrapeRule.wash_config）解析全部策略。
// 此前引擎读的是 WashRule 表——没有任何代码往里写，用户在 UI 配的策略
// 从未生效过；现在直接解析 YAML，与编辑器真正连通
func loadWashStrategies() []washStrategy {
	var rule model.ScrapeRule
	model.DB.Where("type = ?", "wash_config").First(&rule)
	if strings.TrimSpace(rule.Config) == "" {
		return nil
	}
	var m map[string]washStrategy
	if err := yaml.Unmarshal([]byte(rule.Config), &m); err != nil {
		return nil
	}
	// YAML map 无序，按文本出现顺序排序保证"从上到下依次匹配"
	order := parseYAMLKeyOrder(rule.Config)
	sorted := make([]washStrategy, 0, len(m))
	for _, key := range order {
		if st, ok := m[key]; ok {
			sorted = append(sorted, st)
		}
	}
	for k, st := range m {
		if _, done := indexOfKey(order, k); !done {
			sorted = append(sorted, st)
		}
	}
	return sorted
}

// parseYAMLKeyOrder 提取 YAML 顶层键的出现顺序（yaml.v3 不保留 map 顺序）
func parseYAMLKeyOrder(src string) []string {
	var keys []string
	for _, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "	") && strings.HasSuffix(l, ":") {
			keys = append(keys, strings.TrimSuffix(l, ":"))
		}
	}
	return keys
}

func indexOfKey(keys []string, k string) (int, bool) {
	for i, key := range keys {
		if key == k {
			return i, true
		}
	}
	return -1, false
}

// matchWashStrategy 按 media_type/category 匹配第一条策略（空字段=匹配所有）
func matchWashStrategy(mediaType, category string) *washStrategy {
	for _, st := range washStrategyCache() {
		if st.MediaType != "" && st.MediaType != mediaType {
			continue
		}
		if st.Category != "" && !containsCategory(st.Category, category) {
			continue
		}
		return &st
	}
	return nil
}

// containsCategory 洗版策略的 category 列表是否覆盖该分类。
// 分类现在是库内相对目录（"电视剧/日番"），而策略里用户习惯只写末级名（"日番"），
// 所以全路径和末级名都算命中
func containsCategory(list, cat string) bool {
	leaf := path.Base(cat)
	for _, c := range strings.Split(list, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if c == cat || c == leaf {
			return true
		}
	}
	return false
}

// ruleMatch 判断文件名是否满足规则条件（字段为"!"前缀表示排除）
func ruleMatch(name string, r washRule) bool {
	return matchField(name, r.ResourceTeam, extractTeam(name)) &&
		matchField(name, r.ResourcePix, extractPix(name)) &&
		matchField(name, r.ResourceType, extractType(name)) &&
		matchField(name, r.ResourceEffect, extractEffect(name)) &&
		matchField(name, r.VideoEncode, extractVideoEncode(name)) &&
		matchField(name, r.AudioEncode, extractAudioEncode(name))
}

// matchField 单字段匹配（CMS 语义）：逗号分隔多值——
//
//	"2160p,4k"    = 命中任一正值即通过（正值间 OR）
//	"!DV,!DV.HDR" = 任一排除词命中即不通过（负值间 AND NOT）
//	混合时：先看排除（命中即否），再看正值（命中任一即是），全未命中且存在正值则否
func matchField(name, cond, value string) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}
	lname := strings.ToLower(name)
	lvalue := strings.ToLower(value)
	parts := strings.Split(cond, ",")
	hasPositive := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		want := strings.ToLower(strings.TrimPrefix(part, "!"))
		if want == "" {
			continue
		}
		hit := strings.Contains(lname, want) || strings.Contains(lvalue, want)
		if strings.HasPrefix(part, "!") {
			if hit {
				return false // 命中排除词
			}
			continue
		}
		hasPositive = true
		if hit {
			return true // 命中任一正值
		}
	}
	// 只有排除词且都未命中 → 通过；有正值但一个没中 → 不通过
	return !hasPositive
}

// 画质提取已迁移到 resource.go 的 ParseResourceInfo（完整版）
func extractPix(name string) string    { return ParseResourceInfo(name).Pix }
func extractType(name string) string   { return ParseResourceInfo(name).Type }
func extractEffect(name string) string { return ParseResourceInfo(name).Effect }
func extractTeam(name string) string   { return ParseResourceInfo(name).Team }

func extractVideoEncode(name string) string { return ParseResourceInfo(name).VideoEncode }
func extractAudioEncode(name string) string { return ParseResourceInfo(name).AudioEncode }

// washDecision 洗版判定：返回是否替换（新版本优于库内版本）
func washDecision(newName string, libraryNames []string, rules []washRule) bool {
	if len(rules) == 0 || len(libraryNames) == 0 {
		return false
	}
	oldName := libraryNames[0]
	for _, r := range rules {
		newHit := ruleMatch(newName, r)
		oldHit := ruleMatch(oldName, r)
		if newHit != oldHit {
			return newHit // 首条能分出高下的规则决定胜负
		}
	}
	return false // 规则无法判定
}

// libraryFilesOf 从台账取某片目录下的现有文件（含大小，max/min_size 模式用）。
// 台账 rel_path 带库名层（如 "俱乐部/电影/…"），而库内相对路径不带——必须拼上
// ledgerPrefix 才查得到，此前缺失前缀导致洗版永远查空、判定恒为跳过；
// 无前缀回退兼容拿不到库名的场景。
// 不截断候选：长剧加字幕会超过分页上限，漏掉更优版本可能造成错误替换。
func libraryFilesOf(targetDir, ledgerPrefix string) []model.SyncedFile {
	var sfs []model.SyncedFile
	base := strings.Trim(targetDir, "/")
	if base == "" || path.Clean(base) != base || strings.HasPrefix(base, "../") {
		return nil
	}
	if ledgerPrefix != "" {
		base = strings.TrimSuffix(ledgerPrefix, "/") + "/" + base
		model.DB.Where("rel_path LIKE ? ESCAPE '\\'", likeEscape(base)+"/%").Find(&sfs)
	} else {
		model.DB.Where("rel_path LIKE ? ESCAPE '\\' OR rel_path LIKE ? ESCAPE '\\'", likeEscape(base)+"/%", "%/"+likeEscape(base)+"/%").Find(&sfs)
	}
	// 只接受直接文件；未知库名时必须唯一，防止跨库或递归搬走子目录中的其他影片。
	var out []model.SyncedFile
	parent := ""
	for _, sf := range sfs {
		dir := path.Dir(sf.RelPath)
		if dir != base && (ledgerPrefix != "" || !strings.HasSuffix(dir, "/"+base)) {
			continue
		}
		if parent != "" && parent != dir {
			return nil
		}
		parent = dir
		out = append(out, sf)
	}
	return out
}

// ledgerName 台账行的可比文件名。视频行存的是 "片名.1080p.BluRay.mkv.strm"，
// 剥掉 .strm 才是真正的资源名；不剥的话 classifyFile 把它当垃圾文件，
// 洗版一个视频都挑不出来，比较对象退化成目录里的第一行（很可能是 poster.jpg）
func ledgerName(sf model.SyncedFile) string {
	return strings.TrimSuffix(path.Base(sf.RelPath), ".strm")
}

func ledgerIsVideo(sf model.SyncedFile) bool {
	return sf.Kind == "video" || strings.EqualFold(path.Ext(sf.RelPath), ".strm") || classifyFile(ledgerName(sf)) == FileTypeVideo
}

// 同一季的同一集才允许互相替换，未知集数不能扩展为整季。
var washEpisodeRange = regexp.MustCompile(`(?i)(?:s\d{1,2})?ep?\d{1,3}(?:-e?\d{1,3}|e\d{1,3})+`)

func sameWashEpisode(newName, oldName string) bool {
	// 合集文件不能被仅含第一集的新版顶掉；范围不同一律保留旧文件。
	if strings.ToLower(washEpisodeRange.FindString(newName)) != strings.ToLower(washEpisodeRange.FindString(oldName)) {
		return false
	}
	n, o := parseFileName(newName), parseFileName(oldName)
	if n.Episode <= 0 || n.Episode != o.Episode {
		return false
	}
	return n.Season == o.Season || n.Season == 0 || o.Season == 0
}

// filterLedger 按文件名条件筛台账行（不复用底层数组，避免改到调用方的切片）
func filterLedger(rows []model.SyncedFile, keep func(name string) bool) []model.SyncedFile {
	out := make([]model.SyncedFile, 0, len(rows))
	for _, sf := range rows {
		if keep(ledgerName(sf)) {
			out = append(out, sf)
		}
	}
	return out
}

// ledgerPrefixOf 取同步台账的库名前缀（库根目录名，如 "俱乐部"）
func ledgerPrefixOf(ops *pan115Ops, cfg *OrgConfig) string {
	if ops == nil || cfg == nil || ops.cookie == "" || cfg.Library == "" {
		return ""
	}
	if info, err := get115DirInfo(ops.cookie, cfg.Library); err == nil {
		return info.n
	}
	return ""
}

// washStrategyCache YAML 解析结果缓存（1 分钟），避免每个文件都重新解析。
// 未配置（解析结果为空）也要缓存，否则每个文件都白查一次库
var (
	washCacheMu    sync.Mutex
	washCacheVal   []washStrategy
	washCacheAt    time.Time
	washCacheValid bool
)

func washStrategyCache() []washStrategy {
	washCacheMu.Lock()
	defer washCacheMu.Unlock()
	if washCacheValid && time.Since(washCacheAt) < time.Minute {
		return washCacheVal
	}
	washCacheVal = loadWashStrategies()
	washCacheAt = time.Now()
	washCacheValid = true
	return washCacheVal
}

// resetWashCache 策略保存后立即失效。否则用户点完保存马上跑整理，
// 最多一分钟内还在按旧策略判（改完不生效的经典现场）
func resetWashCache() {
	washCacheMu.Lock()
	washCacheValid = false
	washCacheMu.Unlock()
}

// 洗版判定结果
const (
	washFailed    = "failed"    // 旧版未能让位，新版留在待整理，避免覆盖仍在库内的文件。
	washReplaced  = "replaced"  // 新版更优：旧版已让位，新版落入正常入库
	washNotBetter = "notbetter" // 库内已有更优版本：新版应移「已存在」
	washSkip      = "skip"      // 未配置策略/库内无可比文件/共存：不做替换
	washSameFile  = "samefile"  // 目标位置上已经是同一份文件（sha1 相同），没什么可洗的
)

// washScanner 一轮整理内的洗版台账缓存。
//
// 整季逐集判定时，153 集会把同一个季目录的台账查 153 遍，库名前缀还要跟着
// 走 153 次 PathCache 查询。判定阶段只读台账、不动网盘（执行统一推迟到
// applyWashPlans），所以一轮整理里查一次就够，缓存期间不会失真。
type washScanner struct {
	ops       *pan115Ops
	cfg       *OrgConfig
	prefix    string
	gotPrefix bool
	dirs      map[string][]model.SyncedFile
	sha1s     map[string][]model.SyncedFile
}

func newWashScanner(ops *pan115Ops, cfg *OrgConfig) *washScanner {
	return &washScanner{ops: ops, cfg: cfg,
		dirs: map[string][]model.SyncedFile{}, sha1s: map[string][]model.SyncedFile{}}
}

func (w *washScanner) ledgerPrefix() string {
	if !w.gotPrefix {
		w.prefix = ledgerPrefixOf(w.ops, w.cfg)
		w.gotPrefix = true
	}
	return w.prefix
}

// libFiles 某个库内目录下的台账行（洗版的比较对象）
func (w *washScanner) libFiles(targetDir string) []model.SyncedFile {
	if rows, ok := w.dirs[targetDir]; ok {
		return rows
	}
	rows := libraryFilesOf(targetDir, w.ledgerPrefix())
	w.dirs[targetDir] = rows
	return rows
}

// sameFile 台账里 sha1 相同的行 —— 115 秒传 / 同一个分享转存两次拿到的
// 就是同一份文件。orphan_at 非空的行是全量扫描已经确认网盘上没了的，
// 拿它挡新内容等于让一行陈旧台账把片子永久钉在「已存在」里
func (w *washScanner) sameFile(sha1 string) []model.SyncedFile {
	if sha1 == "" || model.DB == nil {
		return nil
	}
	if rows, ok := w.sha1s[sha1]; ok {
		return rows
	}
	var rows []model.SyncedFile
	model.DB.Where("sha1 = ? AND sha1 != '' AND orphan_at IS NULL", sha1).Find(&rows)
	w.sha1s[sha1] = rows
	return rows
}

// washPlan 一次洗版判定的结果。
//
// 判定与执行必须分开：让位每集各发一次 115 写请求要过 3 秒写间隔，
// 一部 153 集的动漫光让位就是 7 分半，整理被钉住的同时增量同步也一直
// 抢不到锁（AGENTS §6.12）。判定只读台账，执行由调用方攒成一批一次发完。
type washPlan struct {
	decision  string
	victims   []model.SyncedFile // 让位的旧版台账行（含跟随它们命名的字幕）
	oldName   string             // 被顶掉的库内最优版本名（日志与通知用）
	newName   string
	targetDir string
	sameAt    string // washSameFile 时库内那一份的台账路径
}

// tryWashReplace 洗版判定与替换执行（单文件入口：散文件整理与重新整理用）。
//   - 新版更好或 replace 无优先级 → 旧版按配置迁移（冗余/已存在/回收站），清理台账与
//     本地产物，返回 washReplaced 让调用方继续正常入库
//   - 旧版更好 → 返回 washNotBetter，调用方应把新文件移「已存在」
//   - 目标位置上已经是同一份文件 → washSameFile，同样移「已存在」但理由不同
//   - 无策略/库内没有可比的版本 → 返回 washSkip
//
// targetDir 是**这个新文件将要落进的库内目录**（电影=标题目录，剧集=季目录），
// 由调用方用与入库同一套模板算出来。此前是拿 MediaLibrary 记录的 TargetPath
// 反推的：那张表只有整理自己会写，库是全量/增量同步建起来的用户永远等不到洗版，
// 而「重新整理」往里写的又是目录、再 path.Dir 一次就退到了二级分类层
//
// 整目录（一次几十上百集）走 processDir 里的批量路径，不要用这个入口。
func tryWashReplace(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, newName, newSha1, targetDir string, onLog func(string)) string {
	sc := newWashScanner(ops, cfg)
	sameFiles := sc.sameFile(newSha1)
	st := matchWashStrategy(media.MediaType, classifyMedia(media))
	if st == nil {
		return washNoStrategy(newName, sameFiles, onLog).decision
	}
	plan := decideWash(media, newName, newSha1, targetDir, st, sc.libFiles(targetDir), sameFiles, onLog)
	if plan.decision != washReplaced {
		return plan.decision
	}
	if err := applyWashPlans(ops, cfg, media, st, []*washPlan{&plan}, onLog, notifyEmbyDeleted); err != nil {
		return washFailed
	}
	return washReplaced
}

// 只注入写操作，测试可以验证实际搬移集合及失败时的台账保留，不接触真实网盘。
type washFileOps interface {
	ensurePath(string, string) (string, error)
	moveFiles(string, []string) error
	deleteFiles([]string) error
}

func runWashReplace(ops washFileOps, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, st *washStrategy, libFiles []model.SyncedFile, onLog func(string)) string {
	return runWashReplaceWithNotify(ops, cfg, media, newName, targetDir, st, libFiles, onLog, notifyEmbyDeleted)
}

func runWashReplaceWithNotify(ops washFileOps, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, st *washStrategy, libFiles []model.SyncedFile, onLog func(string), notifyDeleted func(...string)) string {
	plan := decideWash(media, newName, "", targetDir, st, libFiles, nil, onLog)
	if plan.decision != washReplaced {
		return plan.decision
	}
	if err := applyWashPlans(ops, cfg, media, st, []*washPlan{&plan}, onLog, notifyDeleted); err != nil {
		return washFailed
	}
	return washReplaced
}

// washExistsMsg 「移到已存在」的理由文案。两种理由必须分开说：
// 「库内已有更优版本」是策略比出来的，「库内已有同一份文件」是 sha1 撞上的。
// 此前合成一句「库内已有相同或更优版本」，用户配了 replace 却没看到洗版时
// 根本判断不出是哪一种（现场：龙珠 153 集全被这句话打发掉）
func washExistsMsg(decision string) string {
	if decision == washSameFile {
		return "库内已有同一份文件（sha1 相同）"
	}
	return "库内已有更优版本"
}

// washNoStrategy 未配置洗版策略时的兜底：同一份文件已经在库里就按已存在处理。
// 这是洗版接管之前就有的 sha1 去重行为 —— 没配策略不等于愿意在库里多一份副本
func washNoStrategy(newName string, sameFiles []model.SyncedFile, onLog func(string)) washPlan {
	if len(sameFiles) > 0 {
		onLog(fmt.Sprintf("○ 去重: %s 与库内 %s 是同一份文件（sha1 相同），未配置洗版策略，按已存在处理",
			shortLogName(newName), sameFiles[0].RelPath))
		return washPlan{decision: washSameFile, newName: newName, sameAt: sameFiles[0].RelPath}
	}
	return washPlan{decision: washSkip, newName: newName}
}

// decideWash 纯判定：只读台账，不发任何 115 请求，也不动本地文件。
func decideWash(media *TmdbMedia, newName, newSha1, targetDir string, st *washStrategy, libFiles, sameFiles []model.SyncedFile, onLog func(string)) washPlan {
	plan := washPlan{decision: washSkip, newName: newName, targetDir: targetDir}
	mode := st.Mode
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "skip" && mode != "coexist" && len(st.PriorityLevel) == 0 {
		return plan
	}

	// 同一份文件（sha1 相同）已经在库里，两种情况不能一概而论：
	//   a. 就落在这次的目标目录下 → 内容一模一样，确实没什么可洗的；
	//   b. 在库内别的位置（全量同步带进来的旧目录结构、用户手工建的目录）→
	//      交给策略。replace 的语义就是「新的进库、旧的让位」，旧位置那份让位后
	//      新副本才落到规范路径上，用户配的 mode/old_version_target 才算生效。
	//      内容一模一样，这里不做画质比较——比文件名毫无意义。
	// 此前这里是 processDir 里一句全表 sha1 查询直接短路掉洗版，且一行日志都不打：
	// 用户配着 replace 却只看到「库内已有相同或更优版本」，无从解释（现场：龙珠 153 集）
	if len(sameFiles) > 0 && newSha1 != "" {
		inTarget := ""
		for _, sf := range libFiles {
			if sf.Sha1 == newSha1 {
				inTarget = sf.RelPath
				break
			}
		}
		if inTarget != "" {
			onLog(fmt.Sprintf("○ 洗版判定: %s 与库内 %s 是同一份文件（sha1 相同），按已存在处理",
				shortLogName(newName), inTarget))
			plan.decision, plan.sameAt = washSameFile, inTarget
			return plan
		}
		switch mode {
		case "skip":
			onLog(fmt.Sprintf("○ 洗版判定: skip 模式，同一份文件已在库内 %s，%s 按已存在处理",
				sameFiles[0].RelPath, shortLogName(newName)))
			plan.decision, plan.sameAt = washSameFile, sameFiles[0].RelPath
			return plan
		case "coexist":
			return plan // 共存：正常入库
		default:
			plan.decision = washReplaced
			plan.oldName = ledgerName(sameFiles[0])
			plan.victims = washVictimsElsewhere(sameFiles)
			onLog(fmt.Sprintf("✦ 洗版判定: 同一份文件在库内位置不规范（%s），%s 按 %s 策略让位后落到 %s",
				sameFiles[0].RelPath, shortLogName(newName), mode, targetDir))
			return plan
		}
	}

	// 未配置优先级时明确采用新替旧；有规则时仍保留平局不换的行为。
	newWins := func(oldName string) bool {
		return (mode == "replace" && len(st.PriorityLevel) == 0) || washDecision(newName, []string{oldName}, st.PriorityLevel)
	}
	if len(libFiles) == 0 {
		return plan // 库内无该片的文件
	}

	// 候选 = 库内可能被这个新文件顶掉的**视频**行（海报/NFO 不参与比较）
	var cands []model.SyncedFile
	for _, sf := range libFiles {
		if ledgerIsVideo(sf) {
			cands = append(cands, sf)
		}
	}
	// 剧集：只和同一集比（CMS 图解的「当前集是否已存在」分支）。新文件的集数库内
	// 没出现过就是新增集，直接正常入库——不能拿别的集的画质去判它
	if media.MediaType == "tv" {
		if newEp := parseFileName(newName).Episode; newEp > 0 {
			same := filterLedger(cands, func(n string) bool { return sameWashEpisode(newName, n) })
			if len(same) == 0 {
				onLog(fmt.Sprintf("○ 洗版判定: 第 %d 集库内没有，%s 按新增集正常入库", newEp, truncateStr(newName, 50)))
				return plan
			}
			cands = same
		} else {
			return plan // 无法确定集数时不能把整季当旧版。
		}
	}
	// scope=group：按分辨率分组，组内各留一个最优。新分辨率没有同组文件 → 共存入库
	if st.Scope == "group" {
		newPix := strings.ToLower(ParseResourceInfo(newName).Pix)
		same := filterLedger(cands, func(n string) bool {
			return strings.ToLower(ParseResourceInfo(n).Pix) == newPix
		})
		if len(same) == 0 {
			onLog(fmt.Sprintf("○ 洗版判定: group 模式，%s 为新分辨率分组（%s），共存入库", truncateStr(newName, 50), newPix))
			return plan
		}
		cands = same
	}
	if len(cands) == 0 {
		return plan // 目录里只有海报/NFO，没有可比的版本
	}

	// coexist：多版本共存，新版本直接正常入库，不比较不淘汰
	if mode == "coexist" {
		onLog(fmt.Sprintf("○ 洗版判定: coexist 模式，%s 与库内版本共存入库", truncateStr(newName, 60)))
		return plan
	}
	// skip：库里已有（同集/同组任意版本）就不再收新的
	if mode == "skip" {
		onLog(fmt.Sprintf("○ 洗版判定: skip 模式，库内已有，%s 按已存在处理", truncateStr(newName, 60)))
		plan.decision = washNotBetter
		return plan
	}

	// replace：先在候选里选出库内最强的那一个，新版要赢的是它
	// （此前拿台账查出来的第一行比，同一部片留过多版本时比谁纯看查询顺序）
	best := cands[0]
	for _, c := range cands[1:] {
		if washDecision(ledgerName(c), []string{ledgerName(best)}, st.PriorityLevel) {
			best = c
		}
	}
	oldName := ledgerName(best)
	// max_size/min_size：规则分不出高下（平局）时保守不替换——
	// 新文件在网盘移动前拿不到可靠大小，误删更优版本代价比保守大
	if !newWins(oldName) {
		onLog(fmt.Sprintf("○ 《%s》洗版判定：新版 %s 不优于库内 %s（mode=%s），按已存在处理",
			media.Title, shortLogName(newName), shortLogName(oldName), mode))
		plan.decision = washNotBetter
		return plan
	}

	// 让位集合 = 新版真的赢过的那些候选 + 跟着它们命名的字幕。
	// 此前是「targetDir 下台账查到的全部」：剧集那就是整整一季，替换第 5 集
	// 会把同季其他集一起搬进冗余；海报/NFO 是版本无关的元数据，留着让新版覆盖
	victims := make([]model.SyncedFile, 0, len(cands))
	stems := make([]string, 0, len(cands))
	for _, c := range cands {
		if !newWins(ledgerName(c)) {
			continue // 和新版平手或更优的版本不动
		}
		victims = append(victims, c)
		stem := ledgerName(c)
		if classifyFile(stem) == FileTypeVideo {
			stem = baseName(stem)
		}
		stems = append(stems, stem)
	}
	victims = append(victims, followingSubtitles(libFiles, stems)...)
	if len(victims) == 0 {
		return plan // best 已经输了，理论上到不了这里
	}
	plan.decision, plan.victims, plan.oldName = washReplaced, victims, oldName
	return plan
}

// followingSubtitles 跟着这些视频基名命名的字幕行（版本无关的海报/NFO 不算）
func followingSubtitles(rows []model.SyncedFile, stems []string) []model.SyncedFile {
	var out []model.SyncedFile
	for _, sf := range rows {
		n := ledgerName(sf)
		if classifyFile(n) != FileTypeSubtitle {
			continue
		}
		for _, stem := range stems {
			if stem != "" && strings.HasPrefix(n, stem+".") {
				out = append(out, sf)
				break
			}
		}
	}
	return out
}

// washVictimsElsewhere 同一份文件在库内别处时的让位集合：那几行 + 它们所在目录里
// 跟着它们命名的字幕。字幕要另查一次目录（sha1 查询只会命中视频本身），
// 漏掉的话旧位置会留下一堆指不到视频的孤儿字幕
func washVictimsElsewhere(sameFiles []model.SyncedFile) []model.SyncedFile {
	victims := append([]model.SyncedFile(nil), sameFiles...)
	byDir := map[string][]string{}
	for _, sf := range sameFiles {
		stem := ledgerName(sf)
		if classifyFile(stem) == FileTypeVideo {
			stem = baseName(stem)
		}
		dir := path.Dir(sf.RelPath)
		byDir[dir] = append(byDir[dir], stem)
	}
	seen := map[string]bool{}
	for _, sf := range victims {
		seen[sf.FileID] = true
	}
	for dir, stems := range byDir {
		for _, sf := range followingSubtitles(ledgerRowsInDir(dir), stems) {
			if !seen[sf.FileID] {
				seen[sf.FileID] = true
				victims = append(victims, sf)
			}
		}
	}
	return victims
}

// ledgerRowsInDir 台账里某个目录下的直接文件（rel_path 含库名前缀，按原样匹配）
func ledgerRowsInDir(dir string) []model.SyncedFile {
	if dir == "" || dir == "." || model.DB == nil {
		return nil
	}
	var rows []model.SyncedFile
	model.DB.Where("rel_path LIKE ? ESCAPE '\\'", likeEscape(dir)+"/%").Find(&rows)
	out := make([]model.SyncedFile, 0, len(rows))
	for _, sf := range rows {
		if path.Dir(sf.RelPath) == dir {
			out = append(out, sf)
		}
	}
	return out
}

// washOldDestRel 旧版去向目录：电影用标题目录；剧集用 标题/Season（保留季结构便于辨认）
func washOldDestRel(targetDir string) string {
	destRel := path.Base(targetDir)
	if strings.HasPrefix(strings.ToLower(destRel), "season") {
		destRel = path.Base(path.Dir(targetDir)) + "/" + destRel
	}
	return destRel
}

// applyWashPlans 把一批判定结果一次性落地：旧版让位（**整批一次 115 写请求**）、
// 清台账、删本地产物、通知 Emby。任何一步没做干净都返回错误，调用方据此
// 让新版留在待整理，绝不能在旧版还挂在库里的时候把同名新版盖上去。
//
// 逐集各发一次写请求的老写法，在 153 集的动漫上就是 7 分多钟的纯等待。
func applyWashPlans(ops washFileOps, cfg *OrgConfig, media *TmdbMedia, st *washStrategy, plans []*washPlan, onLog func(string), notifyDeleted func(...string)) error {
	oldTarget := st.OldVersionTarget
	if oldTarget == "" {
		oldTarget = "redundant"
	}
	// 去重：同一行旧版可能被同批里的两个新文件同时判赢。
	// 按 file_id 认（那是台账的唯一索引），不能按主键 ID —— 还没落库的行主键全是 0
	seen := map[string]bool{}
	byDest := map[string][]model.SyncedFile{} // 旧版去向目录 → 让位行
	var victims []model.SyncedFile
	var destOrder []string
	for _, p := range plans {
		if p == nil || p.decision != washReplaced {
			continue
		}
		dest := washOldDestRel(p.targetDir)
		for _, sf := range p.victims {
			key := sf.FileID
			if key == "" {
				key = sf.RelPath
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, ok := byDest[dest]; !ok {
				destOrder = append(destOrder, dest)
			}
			byDest[dest] = append(byDest[dest], sf)
			victims = append(victims, sf)
		}
	}
	if len(victims) == 0 {
		return nil
	}

	destCid := cfg.Redundant
	if oldTarget == "existing" {
		destCid = cfg.Existing
	}
	if oldTarget == "delete" {
		// 必须走整理的 ops：成功后登记删除抑制并失效缓存，增量只消费事件，
		// 不再按旧路径删 STRM 或刷新 Emby（同名新版可能已经落盘）。
		if err := ops.deleteFiles(fidsOfLedger(victims)); err != nil {
			onLog(fmt.Sprintf("✗ 洗版：旧版移入回收站失败: %v（台账保留）", err))
			return err
		}
	} else {
		for _, dest := range destOrder {
			junkCid, err := ops.ensurePath(destCid, "洗版-旧版本/"+dest)
			if err != nil {
				// 建目录失败绝不能清台账：文件还在库里，台账一删同步/去重全部失明
				onLog(fmt.Sprintf("✗ 洗版：创建旧版目录失败: %v（本轮跳过，台账保留）", err))
				return err
			}
			if err := ops.moveFiles(junkCid, fidsOfLedger(byDest[dest])); err != nil {
				onLog(fmt.Sprintf("✗ 洗版移动旧版失败: %v（台账保留）", err))
				return err
			}
		}
	}
	onLog(fmt.Sprintf("○ 洗版：%d 个旧版文件已让位（去向 %s）", len(victims), destLabelOf(oldTarget)))

	// 搬移成功后才清台账（按查到的行精确清理，避免前缀字符串推导）。
	// 本地 strm/附属实体也一并删：旧版已经不在库目录下了，留着就是
	// 指向「冗余/洗版-旧版本」的多余版本，Emby 会当成同一集的两个源。
	// 此前指望增量同步的 move 事件来清，但台账行这里已经删掉、事件也
	// 因为是整理自产而被跳过，谁都不会来收拾
	localRoot := localMediaRoot()
	ids := make([]uint, 0, len(victims))
	cleaned := 0
	var cleanedPaths []string
	for _, sf := range victims {
		if sf.RelPath == "" {
			continue
		}
		full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			onLog(fmt.Sprintf("✗ 洗版：旧版本地文件清理失败 %s: %v", sf.RelPath, err))
			continue
		}
		cleaned++
		ids = append(ids, sf.ID)
		cleanedPaths = append(cleanedPaths, full)
		removeEmptyParents(filepath.Dir(full), localRoot)
	}
	if len(ids) > 0 {
		if err := model.DB.Where("id IN ?", ids).Delete(&model.SyncedFile{}).Error; err != nil {
			onLog(fmt.Sprintf("✗ 洗版：清理旧版台账失败: %v，本轮停止入库", err))
			return err
		}
	}
	if cleaned > 0 {
		onLog(fmt.Sprintf("○ 洗版：共清理 %d 个旧版本地文件（本地根 %s）", cleaned, localRoot))
		// 旧版删了不通知 Emby 的话，同一集在库里会挂着两个源，
		// 点到旧的那个就是播放 404 —— 洗版最典型的翻车现场
		// 先清台账再通知，避免 Emby 删除回调再次命中旧版；等待请求提交后才
		// 允许同名新版落盘，防止异步删除旧条目的请求误碰新版文件。
		notifyDeleted(cleanedPaths...)
	}
	if cleaned != len(victims) {
		onLog("✗ 洗版：旧版本地清理未完成，失败项台账保留，本轮停止入库")
		return fmt.Errorf("洗版旧版本地清理未完成（%d/%d）", cleaned, len(victims))
	}

	for _, p := range plans {
		if p == nil || p.decision != washReplaced {
			continue
		}
		dest := "115 回收站"
		if oldTarget != "delete" {
			dest = destLabelOf(oldTarget) + "/洗版-旧版本/" + washOldDestRel(p.targetDir)
		}
		onLog(fmt.Sprintf("✦ 洗版替换: 新版 %s 替换库内旧版 %s，旧版已移到%s",
			shortLogName(p.newName), shortLogName(p.oldName), dest))
		noteWashReplace(media, p.oldName, p.newName, dest)
	}
	return nil
}

func fidsOfLedger(rows []model.SyncedFile) []string {
	out := make([]string, 0, len(rows))
	for _, sf := range rows {
		out = append(out, sf.FileID)
	}
	return out
}

// ---- 洗版替换说明：挂到入库卡片上，不单独推一条 ----
//
// 洗版本来就是「这部片入库了，顺带把旧版换掉」，拆成两条消息读起来像
// 两件事；文件名又长，一条消息里贴两个原始文件名根本没法看。
// 现在只留一行画质对比，跟着这部片的入库卡片一起发。
//
// 卡片没能在 washNoteTTL 内认领（Emby 没扫到 / 整理侧没发卡片）时，
// 到点自己发一条兜底消息 —— 旧版去了哪儿这种事不能无声无息
const washNoteTTL = 5 * time.Minute

// washNote 一条洗版说明。整季逐集替换时 12 集的对比文字一模一样，
// 合成一行带次数，不能在卡片上摞 12 行
type washNote struct {
	text string
	n    int
}

var (
	washNoteMu sync.Mutex
	washNotes  = map[string][]washNote{}
)

func noteWashReplace(media *TmdbMedia, oldName, newName, destination string) {
	text := fmt.Sprintf("♻️ 洗版替换 %s → %s · 旧版已移到 %s",
		washQualityOf(oldName), washQualityOf(newName), destination)
	key := washNoteKeyOf(media)
	if key == "" {
		go NotifyMessage("♻️ 洗版替换", text)
		return
	}
	washNoteMu.Lock()
	notes, found := washNotes[key], false
	for i := range notes {
		if notes[i].text == text {
			notes[i].n++
			found = true
			break
		}
	}
	if !found {
		notes = append(notes, washNote{text: text, n: 1})
	}
	washNotes[key] = notes
	washNoteMu.Unlock()
	time.AfterFunc(washNoteTTL, func() {
		for _, n := range washNotesFor(key) {
			NotifyMessage("♻️ 洗版替换", n) // 没人认领：兜底单独发
		}
	})
}

// washNotesFor 取走并清空这部片待认领的洗版说明
func washNotesFor(key string) []string {
	if key == "" {
		return nil
	}
	washNoteMu.Lock()
	defer washNoteMu.Unlock()
	notes := washNotes[key]
	delete(washNotes, key)
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		if n.n > 1 {
			out = append(out, fmt.Sprintf("%s ×%d", n.text, n.n))
			continue
		}
		out = append(out, n.text)
	}
	return out
}

// washNoteKeyOf 与入库卡片同一套合并键
func washNoteKeyOf(media *TmdbMedia) string {
	if media == nil {
		return ""
	}
	e := mediaNotifEntry{Title: media.Title, Year: media.Year, Kind: "电影"}
	if media.MediaType == "tv" {
		e.Kind = "剧集"
	}
	return e.mergeKey()
}

// washQualityOf 画质标签，认不出来就退回截短的文件名
func washQualityOf(name string) string {
	if q := qualityLabel(name); q != "" {
		return q
	}
	return truncateStr(name, 40)
}

// destLabelOf 旧版去向的中文名（日志用）
func destLabelOf(oldTarget string) string {
	if oldTarget == "existing" {
		return "已存在"
	}
	return "冗余"
}
