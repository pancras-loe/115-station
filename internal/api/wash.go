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
	"path"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"gopkg.in/yaml.v3"
)

// washRule 单条优先级规则（YAML priority_level 数组元素，字段与 CMS 一致）
type washRule struct {
	ResourceTeam   string `yaml:"resource_team" json:"resource_team"`
	ResourcePix    string `yaml:"resource_pix" json:"resource_pix"`
	ResourceType   string `yaml:"resource_type" json:"resource_type"`
	ResourceEffect string `yaml:"resource_effect" json:"resource_effect"`
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
	for i, st := range washStrategyCache() {
		if st.MediaType != "" && st.MediaType != mediaType {
			continue
		}
		if st.Category != "" && !containsCategory(st.Category, category) {
			continue
		}
		return &washStrategyCache()[i]
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
		matchField(name, r.ResourceEffect, extractEffect(name))
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

// libraryFilesOf 从台账取某片目录下的现有文件（含大小，max/min_size 模式用）
// libraryFilesOf 从台账取某片目录下的现有文件（含大小，max/min_size 模式用）。
// 台账 rel_path 带库名层（如 "俱乐部/电影/…"），而 MediaLibrary.TargetPath
// 不带——必须拼上 ledgerPrefix 才查得到，此前缺失前缀导致洗版永远查空、
// 判定恒为跳过（从未真正应用）；无前缀回退兼容旧记录
func libraryFilesOf(targetDir, ledgerPrefix string) []model.SyncedFile {
	var sfs []model.SyncedFile
	base := strings.TrimSuffix(targetDir, "/")
	if ledgerPrefix != "" {
		model.DB.Where("rel_path LIKE ?", strings.TrimSuffix(ledgerPrefix, "/")+"/"+base+"/%").Limit(20).Find(&sfs)
	}
	if len(sfs) == 0 {
		model.DB.Where("rel_path LIKE ?", base+"/%").Limit(20).Find(&sfs)
	}
	if len(sfs) == 0 {
		// 第三重兜底：任意库名前缀（OpenAPI 等场景拿不到库名时仍能命中台账）
		model.DB.Where("rel_path LIKE ?", "%/"+base+"/%").Limit(20).Find(&sfs)
	}
	return sfs
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

// washStrategyCache YAML 解析结果缓存（1 分钟），避免每个文件都重新解析
var (
	washCacheMu  sync.Mutex
	washCacheVal []washStrategy
	washCacheAt  time.Time
)

func washStrategyCache() []washStrategy {
	washCacheMu.Lock()
	defer washCacheMu.Unlock()
	if washCacheVal != nil && time.Since(washCacheAt) < time.Minute {
		return washCacheVal
	}
	washCacheVal = loadWashStrategies()
	washCacheAt = time.Now()
	return washCacheVal
}

// lookupMediaRecord 查库内整理记录（命中返回记录）。
// source：空=115（历史行 source 为空），cd2=CloudDrive2——两来源记录隔离
func lookupMediaRecord(media *TmdbMedia) (*model.MediaLibrary, bool) {
	return lookupMediaRecordSrc(media, "")
}

func lookupMediaRecordSrc(media *TmdbMedia, source string) (*model.MediaLibrary, bool) {
	var rec model.MediaLibrary
	if err := model.DB.Where("tmdb_id = ? AND media_type = ? AND source = ?", media.TmdbID, media.MediaType, source).First(&rec).Error; err != nil {
		return nil, false
	}
	return &rec, true
}

// 洗版判定结果
const (
	washReplaced  = "replaced"  // 新版更优：旧版已让位，新版落入正常入库
	washNotBetter = "notbetter" // 库内已有更优版本：新版应移「已存在」
	washSkip      = "skip"      // 未配置规则/库内无该片的文件：不做洗版判定
)

// tryWashReplace 洗版判定与替换执行：
//   - 新版更好 → 按策略配置的旧版去向迁移（冗余/已存在；delete 暂不支持按冗余），
//     清理台账/记录，返回 washReplaced 让调用方继续正常入库
//   - 旧版更好 → 返回 washNotBetter，调用方应把新文件移「已存在」
//   - 无规则/库内无文件 → 返回 washSkip
//
// targetDir 必须是目录（不含文件名）：台账按 rel_path LIKE dir+"/%" 匹配，
// 此前调用方传入的 TargetPath 是文件路径，恒查空 → 洗版从未真正生效
func tryWashReplace(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, newName, targetDir string, onLog func(string)) string {
	st := matchWashStrategy(media.MediaType, classifyMedia(media))
	if st == nil || len(st.PriorityLevel) == 0 {
		return washSkip // 未配置策略
	}
	mode := st.Mode
	if mode == "" {
		mode = "replace"
	}
	oldTarget := st.OldVersionTarget
	if oldTarget == "" {
		oldTarget = "redundant"
	}
	libFiles := libraryFilesOf(targetDir, ledgerPrefixOf(ops, cfg))
	if len(libFiles) == 0 {
		return washSkip // 库内无该片的文件
	}
	libNames := make([]string, 0, len(libFiles))
	for _, sf := range libFiles {
		libNames = append(libNames, path.Base(sf.RelPath))
	}

	// 比较对象选取：剧集用同一集的文件（否则多集时拿任意一行比较，结果随机）；
	// 电影取第一个视频文件（跳过 poster.jpg/nfo 等附件行）
	oldName := ""
	if media.MediaType == "tv" {
		if newEp := parseFileName(newName).Episode; newEp > 0 {
			for _, ln := range libNames {
				if parseFileName(ln).Episode == newEp {
					oldName = ln
					break
				}
			}
		}
	}
	if oldName == "" {
		for _, ln := range libNames {
			if classifyFile(ln) == FileTypeVideo {
				oldName = ln
				break
			}
		}
	}
	if oldName == "" {
		oldName = libNames[0]
	}

	// 剧集集数守卫（CMS 图解"当前集是否已存在"分支）：
	// 新文件的集数在库内从未出现 → 是新增集，直接正常入库；
	// 只有同一集已存在时才做画质洗版比较，否则新剧集会被误判进已存在
	if media.MediaType == "tv" {
		if newEp := parseFileName(newName).Episode; newEp > 0 && oldName != "" {
			if parseFileName(oldName).Episode != newEp {
				return washSkip
			}
		}
	}

	// coexist：多版本共存，新版本直接正常入库，不比较不淘汰
	if mode == "coexist" {
		onLog(fmt.Sprintf("○ 洗版判定: coexist 模式，%s 与库内版本共存入库", truncateStr(newName, 60)))
		return washSkip
	}
	// skip：库里已有（同集/同片任意版本）就不再收新的
	if mode == "skip" {
		onLog(fmt.Sprintf("○ 洗版判定: skip 模式，库内已有，%s 按已存在处理", truncateStr(newName, 60)))
		return washNotBetter
	}

	// scope=group：按分辨率分组，组内各留一个最优版本。
	// 新文件的分辨率在库内没有同组文件 → 新分组版本，共存入库；
	// 有同组文件 → 只与同组文件比较
	if st.Scope == "group" {
		newPix := strings.ToLower(ParseResourceInfo(newName).Pix)
		oldPix := strings.ToLower(ParseResourceInfo(oldName).Pix)
		if newPix != oldPix {
			onLog(fmt.Sprintf("○ 洗版判定: group 模式，%s 为新分辨率分组（%s vs 库内 %s），共存入库", truncateStr(newName, 50), newPix, oldPix))
			return washSkip
		}
	}

	// replace：按优先级规则判定（与选定的旧版文件单对单比较）
	better := washDecision(newName, []string{oldName}, st.PriorityLevel)
	// max_size/min_size：规则分不出高下（平局）时保守不替换——
	// 新文件在网盘移动前拿不到可靠大小，误删更优版本代价比保守大
	if !better {
		onLog(fmt.Sprintf("○ 《%s》洗版判定：新版不优于库内版本（mode=%s），按已存在处理", media.Title, mode))
		return washNotBetter
	}
	// 新版更好：旧版按配置的去向迁移（统一放「洗版-旧版本/片名」子目录便于辨认）。
	// 台账查询同样要拼库名前缀（与 libraryFilesOf 一致）
	var sfs []model.SyncedFile
	base := strings.TrimSuffix(targetDir, "/")
	lp := strings.TrimSuffix(ledgerPrefixOf(ops, cfg), "/")
	if lp != "" {
		model.DB.Where("rel_path LIKE ?", lp+"/"+base+"/%").Find(&sfs)
	}
	if len(sfs) == 0 {
		model.DB.Where("rel_path LIKE ?", base+"/%").Find(&sfs)
	}
	// group 模式只搬同分辨率组的旧文件（此前不过滤，其他组/其他集一并被搬走）
	if st.Scope == "group" {
		newPix := strings.ToLower(ParseResourceInfo(newName).Pix)
		filtered := sfs[:0]
		for _, sf := range sfs {
			if strings.ToLower(ParseResourceInfo(path.Base(sf.RelPath)).Pix) == newPix {
				filtered = append(filtered, sf)
			}
		}
		sfs = filtered
	}
	fids := make([]string, 0, len(sfs))
	for _, sf := range sfs {
		fids = append(fids, sf.FileID)
	}
	if len(fids) > 0 {
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
			return washSkip
		}
		if err := ops.moveFiles(junkCid, fids); err != nil {
			onLog(fmt.Sprintf("✗ 洗版移动旧版失败: %v（台账保留）", err))
			return washSkip
		}
		// 搬移成功后才清台账（按查到的行精确清理，避免前缀字符串推导）
		ids := make([]uint, 0, len(sfs))
		for _, sf := range sfs {
			ids = append(ids, sf.ID)
		}
		model.DB.Where("id IN ?", ids).Delete(&model.SyncedFile{})
	}
	model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).Delete(&model.MediaLibrary{})
	destLabel := "冗余"
	if oldTarget == "existing" {
		destLabel = "已存在"
	}
	onLog(fmt.Sprintf("✦ 洗版替换: 新版 %s 优于库内旧版，旧版已移到%s/洗版-旧版本", shortLogName(newName), destLabel))
	go NotifyMessage("🔄 洗版替换", fmt.Sprintf("新版: %s\n旧版: %s\n旧版已移到%s/洗版-旧版本", truncateStr(newName, 80), truncateStr(oldName, 80), destLabel))
	return washReplaced
}
