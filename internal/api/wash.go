package api

// ==================== 洗版策略（版本比较替换）====================
//
// 命中已存在时不再一律移「已存在」：
//   1. 取库内该片的现有文件名（SyncedFile 台账，记录在 TargetPath 之下）
//   2. 与待整理文件按优先级规则逐条比较（制作组/分辨率/来源/效果）
//   3. 新版更好 → 旧版移冗余（洗版-旧版本/片名），新版正常入库（replace）
//      旧版更好 → 新版移已存在（现状）；规则无判定 → 按 coexist 也移已存在

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
	OldVersionTarget string     `yaml:"old_version_target"` // 旧版去向 redundant/existing（默认 redundant）
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

func containsCategory(list, cat string) bool {
	for _, c := range strings.Split(list, ",") {
		if strings.TrimSpace(c) == cat {
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
	washSkip      = "skip"      // 未配置规则/库内无该片的文件：不做洗版判定
)

// tryWashReplace 洗版判定与替换执行：
//   - 新版更好 → 被它顶掉的旧版按策略配置的去向迁移（冗余/已存在），清理台账与
//     本地产物，返回 washReplaced 让调用方继续正常入库
//   - 旧版更好 → 返回 washNotBetter，调用方应把新文件移「已存在」
//   - 无策略/库内没有可比的版本 → 返回 washSkip
//
// targetDir 是**这个新文件将要落进的库内目录**（电影=标题目录，剧集=季目录），
// 由调用方用与入库同一套模板算出来。此前是拿 MediaLibrary 记录的 TargetPath
// 反推的：那张表只有整理自己会写，库是全量/增量同步建起来的用户永远等不到洗版，
// 而「重新整理」往里写的又是目录、再 path.Dir 一次就退到了二级分类层
func tryWashReplace(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, onLog func(string)) string {
	st := matchWashStrategy(media.MediaType, classifyMedia(media))
	if st == nil {
		return washSkip // 未配置策略
	}
	libFiles := libraryFilesOf(targetDir, ledgerPrefixOf(ops, cfg))
	return runWashReplace(ops, cfg, media, newName, targetDir, st, libFiles, onLog)
}

// 只注入写操作，测试可以验证实际搬移集合及失败时的台账保留，不接触真实网盘。
type washFileOps interface {
	ensurePath(string, string) (string, error)
	moveFiles(string, []string) error
}

func runWashReplace(ops washFileOps, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, st *washStrategy, libFiles []model.SyncedFile, onLog func(string)) string {
	mode := st.Mode
	if mode == "" {
		mode = "replace"
	}
	if mode != "skip" && mode != "coexist" && len(st.PriorityLevel) == 0 {
		return washSkip
	}
	oldTarget := st.OldVersionTarget
	if oldTarget == "" {
		oldTarget = "redundant"
	}
	if len(libFiles) == 0 {
		return washSkip // 库内无该片的文件
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
				return washSkip
			}
			cands = same
		} else {
			return washSkip // 无法确定集数时不能把整季当旧版。
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
			return washSkip
		}
		cands = same
	}
	if len(cands) == 0 {
		return washSkip // 目录里只有海报/NFO，没有可比的版本
	}

	// coexist：多版本共存，新版本直接正常入库，不比较不淘汰
	if mode == "coexist" {
		onLog(fmt.Sprintf("○ 洗版判定: coexist 模式，%s 与库内版本共存入库", truncateStr(newName, 60)))
		return washSkip
	}
	// skip：库里已有（同集/同组任意版本）就不再收新的
	if mode == "skip" {
		onLog(fmt.Sprintf("○ 洗版判定: skip 模式，库内已有，%s 按已存在处理", truncateStr(newName, 60)))
		return washNotBetter
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
	if !washDecision(newName, []string{oldName}, st.PriorityLevel) {
		onLog(fmt.Sprintf("○ 《%s》洗版判定：新版 %s 不优于库内 %s（mode=%s），按已存在处理",
			media.Title, shortLogName(newName), shortLogName(oldName), mode))
		return washNotBetter
	}

	// 让位集合 = 新版真的赢过的那些候选 + 跟着它们命名的字幕。
	// 此前是「targetDir 下台账查到的全部」：剧集那就是整整一季，替换第 5 集
	// 会把同季其他集一起搬进冗余；海报/NFO 是版本无关的元数据，留着让新版覆盖
	victims := make([]model.SyncedFile, 0, len(cands))
	stems := make([]string, 0, len(cands))
	for _, c := range cands {
		if !washDecision(newName, []string{ledgerName(c)}, st.PriorityLevel) {
			continue // 和新版平手或更优的版本不动
		}
		victims = append(victims, c)
		stem := ledgerName(c)
		if classifyFile(stem) == FileTypeVideo {
			stem = baseName(stem)
		}
		stems = append(stems, stem)
	}
	for _, sf := range libFiles {
		n := ledgerName(sf)
		if classifyFile(n) != FileTypeSubtitle {
			continue
		}
		for _, stem := range stems {
			if stem != "" && strings.HasPrefix(n, stem+".") {
				victims = append(victims, sf)
				break
			}
		}
	}
	if len(victims) == 0 {
		return washSkip // best 已经输了，理论上到不了这里
	}

	fids := make([]string, 0, len(victims))
	for _, sf := range victims {
		fids = append(fids, sf.FileID)
	}
	destCid := cfg.Redundant
	if oldTarget == "existing" {
		destCid = cfg.Existing
	} else if oldTarget == "delete" {
		// 「删除」按约定不做网盘真删除，直接移入冗余目录
		onLog("○ 旧版去向「删除」按移入冗余目录处理（不做网盘删除）")
	}
	// 旧版去向目录：电影用标题目录；剧集用 标题/Season（保留季结构便于辨认）
	destRel := path.Base(targetDir)
	if strings.HasPrefix(strings.ToLower(destRel), "season") {
		destRel = path.Base(path.Dir(targetDir)) + "/" + destRel
	}
	junkCid, err := ops.ensurePath(destCid, "洗版-旧版本/"+destRel)
	if err != nil {
		// 建目录失败绝不能清台账：文件还在库里，台账一删同步/去重全部失明
		onLog(fmt.Sprintf("✗ 洗版：创建旧版目录失败: %v（本轮跳过，台账保留）", err))
		return washFailed
	}
	if err := ops.moveFiles(junkCid, fids); err != nil {
		onLog(fmt.Sprintf("✗ 洗版移动旧版失败: %v（台账保留）", err))
		return washFailed
	}
	onLog(fmt.Sprintf("○ 洗版：%d 个旧版文件已移到 %s/洗版-旧版本/%s（cid=%s）", len(fids), destLabelOf(oldTarget), destRel, junkCid))
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
		ids = append(ids, sf.ID)
		if sf.RelPath == "" {
			continue
		}
		full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			onLog(fmt.Sprintf("✗ 洗版：旧版本地文件清理失败 %s: %v", sf.RelPath, err))
			continue
		}
		cleaned++
		cleanedPaths = append(cleanedPaths, full)
		onLog(fmt.Sprintf("○ 洗版：已删除旧版本地文件 %s", sf.RelPath))
		removeEmptyParents(filepath.Dir(full), localRoot)
	}
	if cleaned > 0 {
		onLog(fmt.Sprintf("○ 洗版：共清理 %d 个旧版本地文件（本地根 %s）", cleaned, localRoot))
		// 旧版删了不通知 Emby 的话，同一集在库里会挂着两个源，
		// 点到旧的那个就是播放 404 —— 洗版最典型的翻车现场
		go notifyEmbyDeleted(cleanedPaths...)
	}
	model.DB.Where("id IN ?", ids).Delete(&model.SyncedFile{})

	destLabel := destLabelOf(oldTarget)
	onLog(fmt.Sprintf("✦ 洗版替换: 新版 %s 优于库内旧版 %s，旧版已移到%s/洗版-旧版本",
		shortLogName(newName), shortLogName(oldName), destLabel))
	go NotifyMessage("🔄 洗版替换", fmt.Sprintf("新版: %s\n旧版: %s\n旧版已移到%s/洗版-旧版本",
		truncateStr(newName, 80), truncateStr(oldName, 80), destLabel))
	return washReplaced
}

// destLabelOf 旧版去向的中文名（日志用）
func destLabelOf(oldTarget string) string {
	if oldTarget == "existing" {
		return "已存在"
	}
	return "冗余"
}
