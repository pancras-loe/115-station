package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"

	"github.com/mozillazg/go-pinyin"
)

// ==================== 整理引擎 ====================

// OrganizeResult 整理单个文件的结果
type OrganizeResult struct {
	FileName  string `json:"file_name"`
	Status    string `json:"status"` // success, skipped, failed, exists
	TmdbID    int    `json:"tmdb_id"`
	Title     string `json:"title"`
	Year      string `json:"year"`
	MediaType string `json:"media_type"` // movie, tv
	Category  string `json:"category"`
	TargetDir string `json:"target_dir"`
	Message   string `json:"message"`
}

// OrgConfig 整理配置（从数据库加载）
type OrgConfig struct {
	Pending   string `json:"pending"`   // 待整理目录 cid
	Library   string `json:"library"`   // 我的影视库 cid（整理后最终归宿）
	Existing  string `json:"existing"`  // 已存在目录 cid（洗版重复）
	Redundant string `json:"redundant"` // 冗余目录 cid（识别失败等）
	// ManualConfirm 人工确认：识别完只登记「待确认」记录、文件原地不动，
	// 用户在整理记录里确认（或改指定 TMDB 条目）之后才继续后面的流水线
	ManualConfirm bool   `json:"manual_confirm"`
	MinSize       int64  `json:"-"` // MB，loadOrgConfig 从「识别规则」配置注入
	ShareCid      string `json:"-"` // 转存目录 cid（loadOrgConfig 注入；同为工作区根，绝不被当条目处理）
}

// orgCtx 一次整理运行的上下文：引擎各函数共用的只读配置 + 落盘出口。
// 此前这些是 6 个位置参数逐层传递，加上 sink 之后签名已经不可读
type orgCtx struct {
	ops    *pan115Ops
	cfg    *OrgConfig
	tc     *TmdbClient
	rules  []ReplaceRule
	libAbs string     // 库根绝对路径（去重的网盘验证用；OpenAPI 通道取不到则为空）
	sink   *orgSink   // 落盘出口：STRM / 附属文件 / 刮削目标 / 整理记录
	pruner *dirPruner // 搬空之后的空文件夹清理（收尾统一 flush）
	onLog  func(string)

	// forced 人工确认/改指定时直接用这个条目，跳过 TMDB 识别（也就不会再停下来等确认）
	forced *TmdbMedia
	// held fid → 它所在的待确认记录。顶层条目在整理开始时一次列完，
	// 本轮里新停下来的条目也要登记进来，否则同前缀的兄弟散文件会被当成新条目再识别一遍
	held map[string]*awaitingRef
	// handled 本轮已经被别的条目顺带处理掉的 fid（同前缀散文件批量入库）。
	// 顶层清单是开头列好的快照，不跳过的话兄弟文件会被当成新条目再整理一次，
	// 这时网盘里已经有同一份文件，于是被判「已存在」搬走
	handled map[string]bool
}

// holdable 这一条要不要停下来等人工确认
func (c *orgCtx) holdable() bool {
	return c.cfg.ManualConfirm && c.forced == nil
}

// withPending 换一个扫描根（转存目录兜底扫描用），其余配置不变
func (c *orgCtx) withPending(cfg *OrgConfig) *orgCtx {
	n := *c
	n.cfg = cfg
	return &n
}

// renameTpl 全局重命名模板（ensureRenameTpl 加载，两条整理引擎入口都会调用）
var renameTpl *RenameConfig

// ensureRenameTpl 加载重命名模板：用户配置（org-rename）优先，缺失或解析失败
// 回退默认模板。此前只在 runOrganizeEngine 里初始化，转存触发的
// runOrganizeEngineWithConfig 不经过它 → renameBeforeMove 解引用 nil panic
func ensureRenameTpl() {
	renameTpl = defaultRenameConfig()
	if v := modelSettingValue("org-rename"); v != "" {
		var saved RenameConfig
		if json.Unmarshal([]byte(v), &saved) == nil {
			mergeRenameConfig(renameTpl, &saved)
		}
	}
}

// RenameConfig 重命名配置
type RenameConfig struct {
	MovieFolder string `json:"movie_folder"` // 电影文件夹命名规则
	MovieFile   string `json:"movie_file"`   // 电影文件命名规则
	TVFolder    string `json:"tv_folder"`    // 电视剧文件夹命名规则
	TVFile      string `json:"tv_file"`      // 电视剧文件命名规则
}

// defaultRenameConfig 是唯一内置方案。界面只允许在此基础上自定义，不再维护多套预设，
// 避免前后端默认值漂移后同一份资源在不同入口得到不同路径。
func defaultRenameConfig() *RenameConfig {
	return &RenameConfig{
		MovieFolder: "{title}.{year}<.[[tmdbid={tmdb_id}]]>",
		MovieFile:   "{title}<.{en_title.replace('.', ' ')}>.{year}<.{resource_type}><.{resource_effect.replace('.', ' ')}><.{resource_pix}><.{video_encode}><.{audio_encode}>{ext}",
		TVFolder:    "{title}.{year}<.[[tmdbid={tmdb_id}]]>/Season {season_num}",
		TVFile:      "{title}<.{en_title.replace('.', ' ')}>.{season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}>{ext}",
	}
}

func mergeRenameConfig(dst, src *RenameConfig) {
	if src.MovieFolder != "" {
		dst.MovieFolder = src.MovieFolder
	}
	if src.MovieFile != "" {
		dst.MovieFile = src.MovieFile
	}
	if src.TVFolder != "" {
		dst.TVFolder = src.TVFolder
	}
	if src.TVFile != "" {
		dst.TVFile = src.TVFile
	}
}

// loadOrgConfig 从数据库加载整理配置
// loadOrgConfig 加载整理配置（yaml 优先 DB 回退）；
// 影视库不再单独配置，直接使用全量同步配置的媒体库 cid
func (h *Handler) loadOrgConfig() (*OrgConfig, error) {
	var cfg OrgConfig
	if v := h.getSettingValue("org-basic"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	// 最小体积在「识别规则」页（识别前的过滤），不在 org-basic 里
	cfg.MinSize = loadRecognizeConfig().MinSize
	if cfg.Pending == "" {
		return nil, fmt.Errorf("未配置待整理文件夹")
	}
	if cfg.Existing == "" {
		return nil, fmt.Errorf("未配置已存在文件夹")
	}
	if cfg.Redundant == "" {
		return nil, fmt.Errorf("未配置冗余文件夹")
	}
	// 影视库 = 全量同步配置的媒体库 cid；全量未配置时兼容旧的 org-basic.library
	var fullCfg struct {
		Cid string `json:"cid"`
	}
	if v := h.getSettingValue("full"); v != "" {
		json.Unmarshal([]byte(v), &fullCfg)
	}
	if fullCfg.Cid != "" {
		cfg.Library = fullCfg.Cid
	}
	if cfg.Library == "" {
		return nil, fmt.Errorf("未配置媒体库目录（请到「账号与媒体库」完成配置）")
	}
	// 转存目录同样是工作区根：整理引擎跳过它自身（内容由转存触发/守望者以它为扫描根处理）
	var shareCfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareCfg)
	cfg.ShareCid = shareCfg.Folder
	return &cfg, nil
}

// nilSprint fmt.Sprint(map 缺键) 会得到 "<nil>"（OpenAPI 通道列表无 sha/pc 键），
// 统一归一为空串，避免 "<nil>" 字面量进入台账造成误比对
func nilSprint(v interface{}) string {
	s := fmt.Sprint(v)
	if s == "<nil>" {
		return ""
	}
	return s
}

// orgAttachmentExts 整理时随视频同行的附件后缀（字幕/元数据/图片）。
// .idx/.sup/.xml 与 classifyFile 的字幕/NFO 分类对齐——此前缺它们：
// VobSub 的 .idx 随视频移动却不跟随改名，基名分裂后字幕不可用
var orgAttachmentExts = map[string]bool{
	".ass": true, ".srt": true, ".ssa": true, ".sub": true, ".vtt": true, ".smi": true,
	".idx": true, ".sup": true, ".xml": true,
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// orgAttachmentImageExts 附件中的图片后缀
var orgAttachmentImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// orgMetaFixedNames 标准元数据基名：随视频移动到目标目录但保持原名——
// Kodi/Emby 按 poster.jpg/fanart.jpg/tvshow.nfo 等固定名识别，跟随视频
// 改名会让它们失效
var orgMetaFixedNames = map[string]bool{
	"poster": true, "fanart": true, "backdrop": true, "banner": true,
	"folder": true, "cover": true, "logo": true,
	"海报": true, "封面": true,
	"tvshow": true, // tvshow.nfo（剧集级元数据，固定名）
}

// orgMetaFixedName 是否为"随行不改名"的标准元数据文件
func orgMetaFixedName(base, ext string) bool {
	if ext == ".nfo" {
		return orgMetaFixedNames[base]
	}
	if orgAttachmentImageExts[ext] {
		return orgMetaFixedNames[base]
	}
	return false
}

// moveSiblingAttachments 把与视频同目录的附件（字幕/nfo/图片）随视频一起移动，
// 并把与视频同名的字幕重命名为视频新名（播放器按视频名匹配外挂字幕）。
// 返回真正搬走的附件及其最终文件名——一条龙落盘要据此把字幕/NFO 下到本地
func moveSiblingAttachments(ops *pan115Ops, pendingCid, videoOldBase, videoNewBase, targetCid string, rename bool, onLog func(string)) []remoteFile {
	if pendingCid == "" {
		return nil
	}
	entries, _, err := ops.listEntries(pendingCid, 0)
	if err != nil {
		onLog(fmt.Sprintf("✗ 收集随行附件失败: %v", err))
		return nil
	}
	moved := 0
	var movedFiles []remoteFile
	for _, e := range entries {
		if fmt.Sprint(e["f"]) != "1" { // 只看文件
			continue
		}
		name := fmt.Sprint(e["n"])
		ext := strings.ToLower(pathExt(name))
		if !orgAttachmentExts[ext] {
			continue
		}
		fid := fmt.Sprint(e["fid"])
		base := baseName(name)
		// 随行两类：
		//   1) 与视频同名的附件（字幕/同名 nfo/同名图片）→ 随行并跟随视频新名
		//   2) 标准元数据命名的文件（poster/fanart/tvshow.nfo 等）
		//      → 随行但保持标准名——播放器/刮削器认固定名，跟随改名反而失效
		sameBase := base == videoOldBase || strings.HasPrefix(base, videoOldBase+".")
		metaFixed := !sameBase && orgMetaFixedName(base, ext)
		if !sameBase && !metaFixed {
			continue
		}
		if err := ops.moveFiles(targetCid, []string{fid}); err != nil {
			onLog(fmt.Sprintf("✗ 附件随行移动失败 %s: %v", name, err))
			continue
		}
		moved++
		finalName := name
		// 重命名对齐视频新名（仅成功入库场景；冗余/已存在保持原名；
		// 标准元数据命名保持固定名）
		if rename && !metaFixed && videoNewBase != "" && base != videoNewBase {
			newName := videoNewBase + strings.TrimPrefix(base, videoOldBase) + ext
			if err := ops.rename(fid, newName); err != nil {
				onLog(fmt.Sprintf("○ 附件随行 %s（重命名失败保持原名: %v）", name, err))
			} else {
				onLog(fmt.Sprintf("✓ 附件随行 %s → %s", name, newName))
				finalName = newName
			}
		} else {
			onLog(fmt.Sprintf("✓ 附件随行 %s", name))
		}
		movedFiles = append(movedFiles, remoteFile{
			Fid: fid, Name: finalName, PickCode: nilSprint(e["pc"]), Sha1: nilSprint(e["sha"]),
		})
	}
	if moved > 0 {
		time.Sleep(300 * time.Millisecond)
	}
	return movedFiles
}

// renameBeforeMove 在源目录中先重命名文件（带画质信息），再移动到目标。
// 全目录的重命名（视频+字幕）合并为一次 batch_rename 调用：
// 逐个调用时每个文件过一遍 API 限流，24 集仅等待就要 70+ 秒。
//
// 返回 fid → 最终文件名。一条龙落盘要靠它知道每个文件搬过去之后叫什么——
// 此前这张表算出来就丢了，STRM 只能等增量同步从生活事件里把新名捞回来
func renameBeforeMove(ops *pan115Ops, media *TmdbMedia, videoFiles, files []remoteFile, enrichRenames map[string]string, onLog func(string)) map[string]string {
	// 计算单个视频的新名（保持原命名规则）
	// 统一用模板引擎计算视频新名（与 buildNewNameWithTemplate 同源；
	// 此前硬编码 "标题 (年份) [tmdb]" 格式导致与用户配置的命名规则不一致）
	mediaCopy := *media
	mediaCopy.Title = sanitizeName(mediaCopy.Title)
	videoNewName := func(vf remoteFile) (string, bool) {
		p := parseFileName(vf.Name)
		var file string
		if mediaCopy.MediaType == "movie" {
			ctx := buildRenameContext(&mediaCopy, p, vf.Name)
			file = ctx.ApplyTemplate(renameTpl.MovieFile)
		} else if p.Season > 0 && p.Episode > 0 {
			ctx := buildRenameContext(&mediaCopy, p, vf.Name)
			file = ctx.ApplyTemplate(renameTpl.TVFile)
		} else {
			return "", false
		}
		// 115 不允许文件名含 "\ / : * ? " < > | — 模板输出统一清洗
		file = sanitizeName(file)
		// sanitizeName 把 < > 转成 ( )，可能重新引入括号 → 再清一次
		for strings.Contains(file, "(.)") {
			file = strings.ReplaceAll(file, "(.)", "")
		}
		file = parenValueRe.ReplaceAllString(file, "$1")
		for strings.Contains(file, "..") {
			file = strings.ReplaceAll(file, "..", ".")
		}
		file = strings.Trim(file, ".-")
		return file, file != vf.Name
	}

	names := map[string]string{}         // fid -> 新名
	videoNewBases := map[string]string{} // 原视频基名 → 新视频基名（字幕跟随用）
	example := ""
	for _, vf := range videoFiles {
		if n, changed := videoNewName(vf); changed {
			names[vf.Fid] = n
			videoNewBases[baseName(vf.Name)] = baseName(n)
			if example == "" {
				example = fmt.Sprintf("%s → %s", vf.Name, n)
			}
		}
	}

	// 字幕/附件跟随视频新名（用视频的模板新名，不再硬编码）
	for _, f := range files {
		ext := strings.ToLower(pathExt(f.Name))
		if !orgAttachmentExts[ext] {
			continue
		}
		fb := baseName(f.Name)
		// 视频先被补全改名过：字幕基名映射到补全后的基名再匹配，
		// 否则前缀失配、字幕掉队（视频带画质新名而字幕留旧名）
		for oldB, newB := range enrichRenames {
			if fb == oldB || strings.HasPrefix(fb, oldB+".") {
				fb = newB + strings.TrimPrefix(fb, oldB)
				break
			}
		}
		for vfOldBase, vfNewBase := range videoNewBases {
			if fb != vfOldBase && !strings.HasPrefix(fb, vfOldBase+".") {
				continue
			}
			suffix := strings.TrimPrefix(fb, vfOldBase)
			newSubName := vfNewBase + suffix + ext
			if newSubName != f.Name {
				names[f.Fid] = newSubName
			}
			break
		}
	}

	// 补全不再入异步队列：同步探测已在改名前完成（organize 主流程）；
	// 此前这里入队后 worker 用入队时的旧文件名重建名，会覆盖随后的模板
	// 重命名并使字幕失配。异步队列只保留给手动「补全」存量扫描用。
	if len(names) == 0 {
		return nil
	}
	renamed, err := ops.renameBatch(names)
	if err != nil {
		onLog(fmt.Sprintf("✗ 批量重命名未全部完成（成功 %d、保持原名 %d）: %v", len(renamed), len(names)-len(renamed), err))
		return renamed // 已成功的批次必须按新名落盘，不能生成指向旧名的死 STRM
	}
	onLog(fmt.Sprintf("✓ 批量重命名 %d 个文件（例: %s）", len(renamed), example))
	return renamed
}

// moveQuietly 移动并记录失败（失败不再被吞掉）
func moveQuietly(ops *pan115Ops, targetCid string, fids []string, label string, onLog func(string)) {
	if err := ops.moveFiles(targetCid, fids); err != nil {
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", label, err))
	}
}

// holdingFileOps 是「已存在/冗余」归档所需的最小写接口，单测不必接真实 115。
type holdingFileOps interface {
	ensurePath(string, string) (string, error)
	moveFiles(string, []string) error
}

// moveToHoldingDir 先在工作区根下建隔离目录，再整批移动文件。
// 已识别的剧集可能一次有上百集，调用方应把同一片目的 fid 合并后再进来，
// 不能为了目录整洁退回逐文件移动。
func moveToHoldingDir(ops holdingFileOps, rootCid, dirName string, fids []string) (string, error) {
	dirName = sanitizePath(dirName)
	if rootCid == "" || dirName == "" || len(fids) == 0 {
		return "", fmt.Errorf("归档目录或文件为空")
	}
	targetCid, err := ops.ensurePath(rootCid, dirName)
	if err != nil {
		return "", fmt.Errorf("创建归档目录 %s 失败: %w", dirName, err)
	}
	if err := ops.moveFiles(targetCid, fids); err != nil {
		return "", fmt.Errorf("移动到归档目录 %s 失败: %w", dirName, err)
	}
	return targetCid, nil
}

// recognizedHoldingDir 复用正式入库的标题目录模板，但只取片目根目录，
// 不带分类和 Season：既能靠年份/TMDB id 辨认，又能让整季仍只发一次 move。
func recognizedHoldingDir(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	if media == nil || parsed == nil {
		return ""
	}
	return titleDirOf(media, parsed, originalName)
}

// sourceHoldingDir 给无法命中 TMDB 的顶层散文件找一个稳定的原名分组。
// 有明确的剧集前缀时优先保留（Show.S01E01 → Show）；否则用已经解析出的标题，
// 最后才退回完整文件基名。目录条目本身会整目录搬移，不走这里。
func sourceHoldingDir(originalName, parsedTitle string) string {
	cleaned := sanitizeReleaseFilename(originalName)
	base := baseName(cleaned)
	if prefix := extractSeriesPrefix(cleaned); prefix != "" && prefix != base {
		return sanitizeName(prefix)
	}
	if title := sanitizeName(parsedTitle); title != "" && !isEpisodeOnly(title) {
		return title
	}
	return sanitizeName(base)
}

// recognizeConfig 「识别规则」页的配置：识别链最前面那一段——文件名预处理与过滤。
// 三项都在识别之前生效（替换 → 体积过滤 → parseFileName → TMDB 搜索）。
type recognizeConfig struct {
	ReplaceRules  []ReplaceRule `json:"replace_rules"`
	ReleaseGroups []string      `json:"release_groups"`
	MinSize       int64         `json:"min_size"` // MB，0 = 不限制
}

// loadRecognizeConfig 读识别配置（YAML 优先，DB 回退）
func loadRecognizeConfig() recognizeConfig {
	var cfg recognizeConfig
	json.Unmarshal([]byte(settingValueCompat("org-recognize")), &cfg)
	return cfg
}

// ReplaceRule 识别前的文件名替换规则。
// Regex 为真时 From 按正则（Go RE2，不支持断言/反向引用）解释，To 里可用 $1 引用捕获组；
// 否则 From/To 都按纯文本处理。To 为空表示「删掉这一段」。
type ReplaceRule struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Regex bool   `json:"regex"`

	re *regexp.Regexp // 正则规则加载时编译一次，编译不过的规则不进入返回值
}

// loadReplaceRules 加载替换规则，正则在这里一次性编译好——
// 每个文件名都重新编译一遍的话，一次整理几千次编译全是白费
func loadReplaceRules() []ReplaceRule {
	rules := loadRecognizeConfig().ReplaceRules
	out := make([]ReplaceRule, 0, len(rules))
	for _, r := range rules {
		if r.From == "" {
			continue
		}
		if r.Regex {
			re, err := regexp.Compile(r.From)
			if err != nil {
				// 丢掉这一条而不是整组失效：其余规则照常生效，日志里点名是哪条坏了
				log.Printf("[整理] 替换规则正则无效，已跳过: %s（%v）", r.From, err)
				continue
			}
			r.re = re
		}
		out = append(out, r)
	}
	return out
}

// applyReplaceRules 应用替换规则到文件名（按配置顺序依次套用，后一条作用在前一条的结果上）
func applyReplaceRules(name string, rules []ReplaceRule) string {
	for _, r := range rules {
		switch {
		case r.re != nil:
			name = r.re.ReplaceAllString(name, r.To)
		case r.Regex:
			// 正则没编译成功（手改 YAML 绕过了 loadReplaceRules），当纯文本处理只会更糟
		default:
			name = strings.ReplaceAll(name, r.From, r.To)
		}
	}
	return name
}

// mediaTypeCategory 媒体类型对应的一级分类目录名
func mediaTypeCategory(mediaType string) string {
	switch mediaType {
	case "movie":
		return "电影"
	case "tv":
		return "剧集"
	default:
		return "未分类"
	}
}

// 分类规则缓存：整理循环对每个媒体条目查一次规则表，规则极少变化（10s TTL）
var (
	classifyRuleMu    sync.Mutex
	classifyRuleCache = map[string][]model.CategoryRule{}
	classifyRuleAt    = map[string]time.Time{}
)

func classifyRules(mediaType string) []model.CategoryRule {
	classifyRuleMu.Lock()
	defer classifyRuleMu.Unlock()
	if rs, ok := classifyRuleCache[mediaType]; ok && time.Since(classifyRuleAt[mediaType]) < 10*time.Second {
		return rs
	}
	var categories []model.CategoryRule
	model.DB.Where("media_type = ?", mediaType).Order("priority ASC").Find(&categories)
	classifyRuleCache[mediaType] = categories
	classifyRuleAt[mediaType] = time.Now()
	return categories
}

// classifyMedia 根据分类规则判断分类目录（**库内相对路径**，可多级）。
//
// 分类名就是 115 目录名，写什么就是什么：tv 下写「动漫番剧」落到 库/动漫番剧，
// 写「电视剧/日番」才落到 库/电视剧/日番。此前这里会剥掉开头的媒体类型段、
// 再由调用方补一层 mediaTypeCategory，于是用户写的平铺结构
// （tv: 动漫番剧/综艺/剧集）被整理成了 剧集/动漫番剧。
//
// 返回空串表示「不要分类子目录」，由 categoryDir 退回媒体类型默认目录。
func classifyMedia(media *TmdbMedia) string {
	// 电影和电视剧分开查询
	mediaType := "movie"
	if media.MediaType == "tv" {
		mediaType = "tv"
	}
	categories := classifyRules(mediaType)

	for _, cat := range categories {
		if matchCategory(&cat, media) {
			return libSubPath(cat.Name)
		}
	}

	// 查找默认分类
	for _, cat := range categories {
		if cat.IsDefault {
			return libSubPath(cat.Name)
		}
	}

	// 这一档只在该媒体类型一条规则都没有时才会走到（有兜底规则就轮不到）。
	// 挂在媒体类型目录下而不是库根，免得库根多出一个孤零零的「未分类」
	return libSubPath(mediaTypeCategory(mediaType), "未分类")
}

// categoryDir 分类目录的库内相对路径。分类名为空 = 用户不要分类子目录，
// 此时退回媒体类型默认目录（电影/剧集），而不是把片子丢在库根
func categoryDir(mediaType, category string) string {
	if c := libSubPath(category); c != "" {
		return c
	}
	return mediaTypeCategory(mediaType)
}

// libSubPath 拼库内相对路径，自动跳过空段。
// 二级分类允许为空（用户不想分二级时），拿 "+ / +" 硬拼会拼出 "剧集//片名"
func libSubPath(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// matchCategory 判断媒体是否匹配某个分类规则
func matchCategory(cat *model.CategoryRule, media *TmdbMedia) bool {
	// 检查 genre_ids
	if cat.GenreIds != "" {
		genreList := strings.Split(cat.GenreIds, ",")
		matched := false
		for _, g := range genreList {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			gid := 0
			fmt.Sscanf(g, "%d", &gid)
			for _, mg := range media.GenreIDs {
				if mg == gid {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 检查 original_language
	if cat.OriginalLanguage != "" {
		langList := strings.Split(cat.OriginalLanguage, ",")
		matched := false
		for _, l := range langList {
			l = strings.TrimSpace(l)
			if l != "" && media.OrigLanguage == l {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 检查自定义正则（命中即匹配，不需要其他条件）
	if cat.CustomRegex != "" {
		if re, err := regexp.Compile(cat.CustomRegex); err == nil {
			if re.MatchString(media.Title) || re.MatchString(media.OriginalTitle) {
				return true
			}
		}
		// 只有正则条件且未命中
		if cat.GenreIds == "" && cat.OriginalLanguage == "" && cat.OriginCountry == "" && cat.Ext == "" {
			return false
		}
	}

	// 检查 origin_country
	if cat.OriginCountry != "" {
		countryList := strings.Split(cat.OriginCountry, ",")
		matched := false
		for _, c := range countryList {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			for _, mc := range media.OrigCountry {
				if mc == c {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// sanitizeName 清洗名称中会破坏路径/被 115 拒绝的字符
func sanitizeName(name string) string {
	r := strings.NewReplacer("/", " ", "\\", " ", ":", "：", "*", " ", "?", "？", "\"", " ", "<", "(", ">", ")", "|", " ")
	return strings.TrimSpace(r.Replace(name))
}

// extractQualityInfo 从原始文件名提取画质信息（分辨率/来源/编码等）
// "Animal.Control.S04E01.1080p.NowPlayer.WEB-DL.AAC2.0.H.264-BlackTV.mkv"
//
//	→ "1080p.WEB-DL.AAC2.0.H.264"
func extractQualityInfo(filename string) string {
	base := baseName(filename)
	parts := strings.Split(base, ".")

	var qualityParts []string
	// 跳过：标题段（前面的大写单词）和 S/E 编号段
	skipTitle := true
	for _, part := range parts {
		upper := strings.ToUpper(part)

		// 跳过 SxxExx / EPxx / 纯数字编号
		if (strings.HasPrefix(upper, "S") && strings.Contains(upper, "E")) ||
			(strings.HasPrefix(upper, "EP") && len(upper) <= 5) {
			skipTitle = false
			continue
		}
		if skipTitle {
			continue // 标题部分
		}

		// 收集画质相关字段
		if isQualityToken(part) {
			qualityParts = append(qualityParts, part)
		}
	}
	return strings.Join(qualityParts, ".")
}

// isQualityToken 判断是否为画质相关的 token
func isQualityToken(token string) bool {
	upper := strings.ToUpper(token)
	// 常见画质关键词
	qualityKeywords := []string{
		"1080P", "720P", "2160P", "4K", "480P",
		"WEB-DL", "WEB-DL", "BLURAY", "BLU-RAY", "REMUX", "HDTV", "WEBRIP", "DVDRIP",
		"H.264", "H.265", "X264", "X265", "HEVC", "AVC",
		"AAC", "AC3", "DTS", "FLAC", "TRUEHD", "DDP", "DD",
		"HDR", "DV", "SDR", "DOVI",
		"10BIT", "8BIT",
	}
	for _, kw := range qualityKeywords {
		if strings.Contains(upper, kw) {
			return true
		}
	}
	// AAC2.0, DTS-HD等复合token
	if strings.HasPrefix(upper, "AAC") || strings.HasPrefix(upper, "DTS") ||
		strings.HasPrefix(upper, "DDP") || strings.HasPrefix(upper, "TRUEHD") {
		return true
	}
	return false
}

func buildNewName(media *TmdbMedia, parsed *ParsedName, ext string) string {
	// 兼容旧调用（无 Handler 时的硬编码格式，模板引擎在 rename.go 中）
	media.Title = sanitizeName(media.Title)
	firstLetter := titleFirstLetter(media.Title)
	year := media.Year
	if year == "" {
		year = "0000"
	}
	folder := fmt.Sprintf("%s-%s-%s-[tmdb=%d]", firstLetter, media.Title, year, media.TmdbID)
	if media.MediaType == "movie" {
		file := fmt.Sprintf("%s (%s) [%d]%s", media.Title, year, media.TmdbID, ext)
		return folder + "/" + file
	}
	if parsed.Season > 0 {
		subFolder := fmt.Sprintf("Season %02d", parsed.Season)
		if parsed.Episode > 0 {
			file := fmt.Sprintf("%s - S%02dE%02d", media.Title, parsed.Season, parsed.Episode)

			file += ext
			return folder + "/" + subFolder + "/" + file
		}
		return folder + "/" + subFolder
	}
	return folder
}

// buildNewNameWithTemplate 用模板引擎生成目标路径（Handler 方法，可读配置）
func buildNewNameWithTemplate(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	media.Title = sanitizeName(media.Title)
	if renameTpl == nil {
		return buildNewName(media, parsed, pathExt(originalName)) // 降级到硬编码
	}
	ctx := buildRenameContext(media, parsed, originalName)
	var folder, file string
	switch media.MediaType {
	case "movie":
		folder, file = ctx.ApplyTemplate(renameTpl.MovieFolder), ctx.ApplyTemplate(renameTpl.MovieFile)
	case "tv":
		folder, file = ctx.ApplyTemplate(renameTpl.TVFolder), ctx.ApplyTemplate(renameTpl.TVFile)
	default:
		return ""
	}
	// 剧集需要插入 Season 目录（模板没带就补一层）
	if media.MediaType == "tv" && parsed.Season > 0 && !folderHasSeason(renameTpl.TVFolder, folder) {
		folder += "/" + fmt.Sprintf("Season %02d", parsed.Season)
	}
	return sanitizePath(folder + "/" + file)
}

// seasonDirRe 匹配「这一段就是个季目录」的写法
var seasonDirRe = regexp.MustCompile(`(?i)^(season\s*\d+|specials|第.+季)$`)

// folderHasSeason 判断文件夹模板是否已经自己安排了季目录。
//
// 先看模板有没有引用季变量（最可靠），再退回看渲染结果里有没有整段是
// 季目录。不能拿 strings.Contains(folder, "Season") 了事——《Season of
// the Witch》这类片名会让整个剧集都漏掉 Season 层
func folderHasSeason(tpl, rendered string) bool {
	for _, v := range []string{"{season_num}", "{season_episode}", "{season_name}"} {
		if strings.Contains(tpl, v) {
			return true
		}
	}
	for _, seg := range strings.Split(rendered, "/") {
		if seasonDirRe.MatchString(strings.TrimSpace(seg)) {
			return true
		}
	}
	return false
}

// titleFirstLetter 取标题首字母：英文取首字母，中文取拼音首字母（巴→B），数字为 #
func titleFirstLetter(title string) string {
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z':
			return strings.ToUpper(string(r))
		case r >= 'A' && r <= 'Z':
			return string(r)
		case r >= '0' && r <= '9':
			return "#"
		case r >= 0x4e00 && r <= 0x9fff: // 汉字
			py := pinyin.Pinyin(string(r), pinyin.NewArgs())
			if len(py) > 0 && len(py[0]) > 0 && py[0][0] != "" {
				c := py[0][0][0]
				if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
					return strings.ToUpper(string(c))
				}
			}
		}
	}
	return "0"
}

// checkByCloudSHA1 直接查网盘媒体库目标目录的 SHA1 去重（不依赖本地缓存表）
// 流程：与实际入库同源计算目标标题目录路径（媒体类型/分类/标题目录）→
// 列出该目录及其 Season 子目录的文件 → 比对 SHA1
// 返回 true=已存在 / false=不存在或目录为空
//
// 目录计算必须与 buildNewNameWithTemplate 完全同源：真实的 parsed（季集等
// 模板变量）+ 原文件名（资源变量）+ 媒体类型/分类前缀 + sanitizePath 分段。
// 此前查 `库根/标题目录`（少了「电影|剧集/分类」层），cloudPathCid 必然
// not found → 去重恒失效；剧集文件在 Season 子目录里，还要多列一层
func checkByCloudSHA1(ops *pan115Ops, media *TmdbMedia, cfg *OrgConfig, libAbs, currentSHA1 string, parsed *ParsedName, originalName string) bool {
	if ops.cookie == "" || libAbs == "" || currentSHA1 == "" || parsed == nil || originalName == "" {
		return false // 条件不足，不判已存在（宁可重复入库也不误判）
	}
	folderName := titleDirOf(media, parsed, originalName)
	if folderName == "" {
		return false
	}
	absDir := strings.TrimSuffix(libAbs, "/") + "/" +
		libSubPath(categoryDir(media.MediaType, classifyMedia(media)), folderName)

	// 查目标标题目录 cid
	cid, ok := cloudPathCid(ops.cookie, absDir)
	if !ok {
		return false // 目录不存在 → 新片
	}

	// 列标题目录（电影文件直接在其下，剧集在 Season 子目录下）：
	// 一次调用同时拿文件 SHA1 与子目录 cid，再逐季比对
	if cloudDirHasSHA1(ops.cookie, cid, currentSHA1) {
		return true
	}
	for _, seasonCid := range cloudSubDirCids(ops.cookie, cid) {
		if cloudDirHasSHA1(ops.cookie, seasonCid, currentSHA1) {
			return true
		}
	}
	return false
}

// titleDirOf 与入库同源计算标题目录名（newPath 的第一段）
func titleDirOf(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	if renameTpl != nil {
		mediaCopy := *media
		mediaCopy.Title = sanitizeName(mediaCopy.Title)
		ctx := buildRenameContext(&mediaCopy, parsed, originalName)
		var folderTpl string
		switch media.MediaType {
		case "movie":
			folderTpl = renameTpl.MovieFolder
		case "tv":
			folderTpl = renameTpl.TVFolder
		default:
			return ""
		}
		// 拼占位文件段走与入库相同的 sanitizePath 分段清洗，取第一段
		if p := sanitizePath(ctx.ApplyTemplate(folderTpl) + "/x"); p != "" && p != "x" {
			return strings.SplitN(p, "/", 2)[0]
		}
	}
	firstLetter := titleFirstLetter(media.Title)
	year := media.Year
	if year == "" {
		year = "0000"
	}
	return fmt.Sprintf("%s-%s-%s-[tmdb=%d]", firstLetter, media.Title, year, media.TmdbID)
}

// cloudDirHasSHA1 列出目录（limit 1000）比对是否存在指定 SHA1 的文件
func cloudDirHasSHA1(cookie, cid, sha1Want string) bool {
	body, err := httpGet115UA("https://webapi.115.com/files",
		url.Values{
			"aid":      {"1"},
			"cid":      {cid},
			"show_dir": {"0"},
			"limit":    {"1000"},
			"format":   {"json"},
		}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return false // 查询失败，不判已存在
	}
	var r struct {
		State bool                     `json:"state"`
		Data  []map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return false
	}
	for _, d := range r.Data {
		if fmt.Sprint(d["f"]) != "1" {
			continue
		}
		sha1 := fmt.Sprint(d["sha"])
		if sha1 == "<nil>" {
			continue
		}
		if strings.EqualFold(sha1, sha1Want) {
			return true
		}
	}
	return false
}

// cloudSubDirCids 列出目录的一层子目录 cid（剧集的 Season 目录）
func cloudSubDirCids(cookie, cid string) []string {
	body, err := httpGet115UA("https://webapi.115.com/files",
		url.Values{
			"aid":      {"1"},
			"cid":      {cid},
			"show_dir": {"1"},
			"limit":    {"100"},
			"format":   {"json"},
		}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return nil
	}
	var r struct {
		State bool                     `json:"state"`
		Data  []map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || !r.State {
		return nil
	}
	var out []string
	for _, d := range r.Data {
		if fmt.Sprint(d["f"]) == "0" {
			if c := fmt.Sprint(d["cid"]); c != "" && c != "<nil>" {
				out = append(out, c)
			}
		}
	}
	return out
}

// cloudDirHasVideos 检查网盘目录下是否有视频文件（空目录或不存在都返回 false）
func cloudDirHasVideos(cookie, absPath string) bool {
	cid, ok := cloudPathCid(cookie, absPath)
	if !ok {
		return false
	}
	body, err := httpGet115UA("https://webapi.115.com/files",
		url.Values{
			"aid":      {"1"},
			"cid":      {cid},
			"show_dir": {"1"},
			"limit":    {"20"},
			"format":   {"json"},
		}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return true
	}
	var r struct {
		State bool                     `json:"state"`
		Data  []map[string]interface{} `json:"data"`
		Count int                      `json:"count"`
	}
	if json.Unmarshal(body, &r) != nil {
		return true
	}
	if !r.State || r.Count == 0 {
		return false
	}
	for _, d := range r.Data {
		if fmt.Sprint(d["f"]) == "1" {
			name := fmt.Sprint(d["n"])
			ext := strings.ToLower(pathExt(name))
			for _, ve := range []string{".mp4", ".mkv", ".ts", ".avi", ".mov", ".rmvb", ".webm", ".flv", ".m2ts", ".wmv", ".mpg", ".iso"} {
				if ext == ve {
					return true
				}
			}
		}
	}
	return false
}

// checkExistsVerified 去重判定：本地记录命中后到网盘验证目标路径仍存在，
// 记录失效（库被清空/内容被移走）则自动删除记录并视为不存在——
// 空库误判"已存在"的根治方案；网盘查询失败时保守按存在处理
func checkExistsVerified(ops *pan115Ops, media *TmdbMedia, cfg *OrgConfig, libAbs string) bool {
	var rec model.MediaLibrary
	if err := model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).First(&rec).Error; err != nil {
		return false
	}
	// 网盘验证：检查记录的目标目录下是否还有视频文件（空目录 = 已清空 = 记录过期）
	if ops.cookie != "" && libAbs != "" && rec.TargetPath != "" {
		dirPart := path.Dir(rec.TargetPath)
		absDir := path.Join(libAbs, dirPart)
		if !cloudDirHasVideos(ops.cookie, absDir) {
			log.Printf("[整理] ✦ 去重记录已过期（网盘目录为空或不存在），自动清除: %s (%s) tmdb=%d 旧目标=%s",
				rec.Title, rec.Year, rec.TmdbID, rec.TargetPath)
			model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).Delete(&model.MediaLibrary{})
			return false
		}
	}
	log.Printf("[整理] ○ 已存在: %s (%s) tmdb=%d 记录于 %s 目标=%s",
		rec.Title, rec.Year, rec.TmdbID, rec.CreatedAt.Format("2006-01-02 15:04"), rec.TargetPath)
	return true
}

// cloudPathExistsCk 校验网盘绝对路径是否存在（webapi files/getid，复用 cloudPathCidE）
func cloudPathExistsCk(cookie, absPath string) bool {
	_, ok, err := cloudPathCidE(cookie, absPath)
	if err != nil {
		return true // 查询失败保守按存在
	}
	return ok
}

// recordMedia 落整理记录（洗版查记录用）
func recordMedia(media *TmdbMedia, category, targetPath string) {
	// 同一部影视只留一条记录（重复整理时更新而非新增）
	var existing model.MediaLibrary
	if model.DB.Where("tmdb_id = ? AND media_type = ?", media.TmdbID, media.MediaType).First(&existing).Error == nil {
		existing.Category = category
		existing.TargetPath = targetPath
		existing.Title = media.Title
		existing.PosterPath = media.PosterPath
		existing.VoteAverage = media.VoteAverage
		existing.Overview = media.Overview
		model.DB.Save(&existing)
		return
	}
	record := &model.MediaLibrary{
		TmdbID:        media.TmdbID,
		Title:         media.Title,
		OriginalTitle: media.OriginalTitle,
		Year:          media.Year,
		MediaType:     media.MediaType,
		Category:      category,
		TargetPath:    targetPath,
		OrigLanguage:  media.OrigLanguage,
		OrigCountry:   strings.Join(media.OrigCountry, ","),
		PosterPath:    media.PosterPath,
		VoteAverage:   media.VoteAverage,
		Overview:      media.Overview,
	}
	model.DB.Save(record)
}

// ==================== 文件分类（附属文件保留规则） ====================

// videoExts 支持的视频后缀
var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".ts": true, ".avi": true, ".mov": true,
	".rmvb": true, ".webm": true, ".flv": true, ".m2ts": true,
	".wmv": true, ".mpg": true, ".iso": true, ".m4v": true,
}

// subtitleExts 支持的字幕后缀
var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true,
	".idx": true, ".sup": true, ".vtt": true,
}

// standardImageNames Emby/Kodi 标准命名的图片（不含后缀，小写匹配）
var standardImageNames = map[string]bool{
	"poster": true, "fanart": true, "backdrop": true, "banner": true,
	"thumb": true, "landscape": true, "logo": true, "logo-clear": true,
	"clearart": true, "clearlogo": true, "disc": true, "discart": true,
	// 常见中文命名（动漫资源包自带的海报/封面）
	"海报": true, "封面": true,
}

// FileType 文件分类类型
type FileType int

const (
	FileTypeVideo    FileType = iota // 视频文件
	FileTypeSubtitle                 // 字幕文件
	FileTypeNFO                      // NFO 媒体信息
	FileTypeStdImage                 // 标准命名图片
	FileTypeJunk                     // 无用文件（广告等）
)

// classifyFile 判断文件类型
func classifyFile(name string) FileType {
	ext := strings.ToLower(pathExt(name))
	base := strings.ToLower(baseName(name))

	// 视频
	if videoExts[ext] {
		return FileTypeVideo
	}
	// 字幕
	if subtitleExts[ext] {
		return FileTypeSubtitle
	}
	// NFO
	if ext == ".nfo" || ext == ".xml" {
		return FileTypeNFO
	}
	// 图片：只保留标准命名的
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
		if standardImageNames[base] {
			return FileTypeStdImage
		}
		return FileTypeJunk
	}
	// 其他文件（txt 等广告文件）
	return FileTypeJunk
}

// pathExt 取文件后缀（含点）
func pathExt(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx:]
	}
	return ""
}

// baseName 取文件名（不含后缀）
func baseName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[:idx]
	}
	return name
}

// ==================== 目录级遍历（获取待整理目录结构） ====================

// dirEntry 待整理目录中的一个条目（文件或子目录）
type dirEntry struct {
	Fid      string // 文件 id
	Name     string // 文件名
	IsDir    bool   // 是否文件夹
	Size     int64  // 文件大小
	Cid      string // 子目录 cid（仅文件夹有）
	Sha1     string // 文件 sha1（散文件去重用）
	PickCode string // 文件 pickcode（散文件一条龙落盘写 STRM 直链要用）
}

// listPendingTopLevel 列出待整理目录下的顶层条目（不递归）
func listPendingTopLevel(ops *pan115Ops, cid string) ([]dirEntry, error) {
	var entries []dirEntry
	offset := 0
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, d := range raw {
			isDir := fmt.Sprint(d["f"]) == "0"
			fid := fmt.Sprint(d["fid"])
			if isDir && (fid == "" || fid == "<nil>") {
				// webapi 列表中目录自身 id 在 cid 字段（无 fid），移动目录必须用它
				fid = fmt.Sprint(d["cid"])
			}
			e := dirEntry{
				Fid:   fid,
				Name:  fmt.Sprint(d["n"]),
				IsDir: isDir,
				Cid:   fmt.Sprint(d["cid"]),
			}
			sha1 := fmt.Sprint(d["sha"])
			if sha1 != "<nil>" {
				e.Sha1 = sha1
			}
			e.PickCode = nilSprint(d["pc"])
			if s, ok := d["s"].(float64); ok {
				e.Size = int64(s)
			}
			entries = append(entries, e)
		}
		if len(raw) == 0 || offset+len(raw) >= count {
			break
		}
		offset += len(raw)
	}
	return entries, nil
}

// collectDirFiles 递归收集某个 cid 目录下的所有文件（包括子目录），返回带 fid 的列表
func collectDirFiles(ops *pan115Ops, cid, basePath string) ([]remoteFile, error) {
	var files []remoteFile
	offset := 0
	for {
		raw, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, d := range raw {
			isDir := fmt.Sprint(d["f"]) == "0"
			name := fmt.Sprint(d["n"])
			if isDir {
				subFiles, err := collectDirFiles(ops, fmt.Sprint(d["cid"]), basePath+"/"+name)
				if err != nil {
					return nil, err
				}
				files = append(files, subFiles...)
			} else {
				size := int64(0)
				if s, ok := d["s"].(float64); ok {
					size = int64(s)
				}
				sha1 := fmt.Sprint(d["sha"])
				if sha1 == "<nil>" {
					sha1 = ""
				}
				pickCode := fmt.Sprint(d["pc"])
				if pickCode == "<nil>" || pickCode == "" {
					pickCode = fmt.Sprint(d["pickcode"])
				}
				if pickCode == "<nil>" {
					pickCode = ""
				}
				files = append(files, remoteFile{
					Fid:      fmt.Sprint(d["fid"]),
					Name:     name,
					Path:     basePath,
					Size:     size,
					Sha1:     sha1,
					PickCode: pickCode,
				})
			}
		}
		if len(raw) == 0 || offset+len(raw) >= count {
			break
		}
		offset += len(raw)
	}
	return files, nil
}

// orgGuards 整理防误伤守卫：
// 当扫描根（待整理/转存目录）位于媒体库、已存在、冗余三棵子树的同级或上层时，
// 这些子树内的条目一律跳过——绝不把库内内容当待整理素材重排或搬进冗余。
// 正常布局（待整理在库内/库外独立）不受影响；OpenAPI 无 Cookie 通道取不到
// 目录绝对路径时守卫自动失效（靠 trigger 层校验兜底）
type orgGuards struct {
	cookie    string
	memo      map[string]dirInfo
	absCache  map[string]string
	protected []string // 受保护子树绝对路径（尾 / 已去除）
	active    bool     // 扫描根是否覆盖到任一保护子树
}

// newOrgGuards 计算扫描根与三棵保护子树的空间关系
func newOrgGuards(cookie, scanCid string, cfg *OrgConfig) *orgGuards {
	g := &orgGuards{cookie: cookie, memo: map[string]dirInfo{}, absCache: map[string]string{}}
	if cookie == "" {
		return g
	}
	for _, cid := range []string{cfg.Library, cfg.Existing, cfg.Redundant} {
		if cid == "" {
			continue
		}
		if a := g.absOf(cid); a != "" {
			g.protected = append(g.protected, strings.TrimSuffix(a, "/"))
		}
	}
	// 转存目录同样是保护子树——但仅当它不是扫描根时：
	// 转存触发场景（Pending 被替换成转存目录）要正常处理其内容
	if cfg.ShareCid != "" && cfg.ShareCid != scanCid {
		if a := g.absOf(cfg.ShareCid); a != "" {
			g.protected = append(g.protected, strings.TrimSuffix(a, "/"))
		}
	}
	scanAbs := strings.TrimSuffix(g.absOf(scanCid), "/")
	for _, p := range g.protected {
		if scanAbs != "" && (p == scanAbs || strings.HasPrefix(p, scanAbs+"/")) {
			g.active = true
			break
		}
	}
	return g
}

// absOf 保护子树路径。走 absPathOfFresh 而不是缓存版：
// 这个值算错的后果是守卫失效、库内内容被当成待整理素材重排，
// 而 g.absCache 已经保证了单次整理里每个 cid 只查一次，代价就是几个请求
func (g *orgGuards) absOf(cid string) string {
	if a, ok := g.absCache[cid]; ok {
		return a
	}
	a := absPathOfFresh(g.cookie, cid)
	g.absCache[cid] = a
	return a
}

// skip 报告条目是否位于保护子树内（目录传自身 cid，文件传父目录 cid）。
// 仅在 active 模式下生效；路径取不到时宁可放行（由日志暴露异常布局）
func (g *orgGuards) skip(cid string) bool {
	if g == nil || !g.active || cid == "" {
		return false
	}
	a := strings.TrimSuffix(g.absOf(cid), "/")
	if a == "" {
		return false
	}
	for _, p := range g.protected {
		if a == p || strings.HasPrefix(a+"/", p+"/") {
			return true
		}
	}
	return false
}

// runOrganizeEngine 整理引擎核心逻辑
// 按目录级别整理：识别视频→分类→移动整个目录（视频+字幕+NFO+标准图片）到影视库
func runOrganizeEngine(ops *pan115Ops, cfg *OrgConfig, sink *orgSink, onLog func(string)) ([]OrganizeResult, int) {
	results := []OrganizeResult{}
	successCount := 0

	// 加载 TMDB 客户端
	tc, err := loadTmdbClient()
	if err != nil {
		onLog("✗ TMDB 配置错误: " + err.Error())
		return results, 0
	}

	// 加载替换规则
	replaceRules := loadReplaceRules()

	// 加载重命名模板配置
	ensureRenameTpl()

	// 先列待整理目录：绝大多数轮次它是空的（定时任务 10 分钟一跑），
	// 空转就该在这里结束。库根绝对路径要爬目录链、每层一次 115 调用，
	// 放在这之前等于每轮都为「没活干」先付几次请求
	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历待整理目录失败: " + err.Error())
		return results, 0
	}

	if len(topEntries) == 0 {
		onLog("○ 待整理目录为空")
		return results, 0
	}

	// 库根绝对路径（去重记录的网盘验证用；OpenAPI 通道取不到则跳过验证）
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library)
	}
	ctx := &orgCtx{ops: ops, cfg: cfg, tc: tc, rules: replaceRules, libAbs: libAbs, sink: sink,
		pruner: newDirPruner(ops, orgProtectedCids(cfg), onLog), onLog: onLog,
		held: loadAwaiting(), handled: map[string]bool{}}

	// 五个工作区根目录（媒体库/待整理/已存在/冗余/转存目录）永不被移动：
	// 引擎只往它们里面放内容，目录自身绝不能被当作影视条目处理
	// （否则会出现"已经存在文件夹移进自己"、转存目录被当容器壳搬进冗余等事故）
	excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
	if cfg.ShareCid != "" {
		excluded[cfg.ShareCid] = true
	}
	filtered := topEntries[:0]
	for _, e := range topEntries {
		if e.IsDir && excluded[e.Cid] {
			onLog(fmt.Sprintf("○ 跳过整理工作区目录: %s/", e.Name))
			continue
		}
		filtered = append(filtered, e)
	}
	topEntries = ctx.dropHeld(filtered)
	if len(topEntries) == 0 {
		return results, 0 // 空转静默：定时任务每 10 分钟一轮，不为空目录刷日志
	}

	onLog(fmt.Sprintf("发现 %d 个条目，开始整理...", len(topEntries)))

	guards := newOrgGuards(ops.cookie, cfg.Pending, cfg)
	if guards.active {
		onLog("⚠ 扫描根覆盖到媒体库/已存在/冗余目录，这些子树内的条目将被跳过（防误整理库内容）")
	}

	for i, entry := range topEntries {
		SetTaskProgress(fmt.Sprintf("整理 %d/%d：%s", i+1, len(topEntries), truncateStr(entry.Name, 40)))
		results = append(results, processEntry(ctx, guards, entry, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}
	ctx.pruner.flush()
	SetTaskProgress("")

	return results, successCount
}

// processEntry 处理一个顶层条目：
//   - 目录不含直接视频文件但含子目录 → 容器目录（如用户把多部剧放进同一文件夹），
//     递归处理每个子目录（每部剧独立识别入库），容器自身最后移到冗余
//   - 其余目录 → 单部影视目录
//   - 文件 → 散视频
func processEntry(ctx *orgCtx, guards *orgGuards, entry dirEntry, depth int, successCount *int) []OrganizeResult {
	ops, cfg, onLog := ctx.ops, ctx.cfg, ctx.onLog
	results := []OrganizeResult{}
	// 库子树防护：目录传自身 cid，文件传父目录 cid；命中保护子树直接放行不处理
	if guards.skip(entry.Cid) {
		onLog(fmt.Sprintf("○ 跳过媒体库/工作区内条目: %s（整理不处理库内内容）", entry.Name))
		return results
	}
	if ctx.handled[entry.Fid] {
		return results // 已随同前缀的散文件一起入库
	}
	if ref, ok := ctx.held[entry.Fid]; ok {
		if ctx.cfg.ManualConfirm {
			onLog(fmt.Sprintf("⏸ %s - 等待人工确认（整理记录 → 待确认），本轮跳过", entry.Name))
			return results
		}
		// 开关已经关了：按自动整理走，结果写回那条待确认记录，而不是再添一行
		ctx.adoptAwaiting(ref)
		defer ctx.releaseAwaiting(ref)
	}
	if !entry.IsDir {
		if classifyFile(entry.Name) != FileTypeVideo {
			// 顶层散落垃圾（txt/url 等）清进冗余，其余非视频跳过并留痕
			if classifyFile(entry.Name) == FileTypeJunk {
				if err := ops.moveFiles(cfg.Redundant, []string{entry.Fid}); err == nil {
					onLog(fmt.Sprintf("○ %s - 顶层垃圾文件，已移到冗余", entry.Name))
				}
				return results
			}
			onLog(fmt.Sprintf("○ %s - 顶层非视频文件，跳过", entry.Name))
			return results
		}
		if cfg.MinSize > 0 && entry.Size > 0 && entry.Size < cfg.MinSize*1024*1024 {
			onLog(fmt.Sprintf("○ %s - 仅 %.1fMB（小于最小体积 %dMB），跳过", entry.Name, float64(entry.Size)/1024/1024, cfg.MinSize))
			return results
		}
		f := remoteFile{Fid: entry.Fid, Name: entry.Name, Size: entry.Size, Sha1: entry.Sha1, PickCode: entry.PickCode}
		result := processSingleFileWithSiblings(ctx, f)
		results = append(results, result...)
		for _, r := range results {
			if r.Status == "success" {
				*successCount++
			}
		}
		return results
	}

	// 列直接子条目判断是否为容器目录
	direct, err := listPendingTopLevel(ops, entry.Cid)
	if err == nil && depth < 3 {
		excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
		hasDirectVideo := false
		var subDirs []dirEntry
		for _, c := range direct {
			if c.IsDir {
				if !excluded[c.Cid] {
					subDirs = append(subDirs, c)
				}
			} else if classifyFile(c.Name) == FileTypeVideo {
				hasDirectVideo = true
			}
		}
		if !hasDirectVideo && len(subDirs) > 0 {
			onLog(fmt.Sprintf("▣ %s/ 为容器目录（无直接视频，含 %d 个子目录），逐个处理", entry.Name, len(subDirs)))
			for _, child := range subDirs {
				results = append(results, processEntry(ctx, guards, child, depth+1, successCount)...)
			}
			// 容器壳处理：重新列目录确认真的空了才移冗余；
			// 有残留（移动失败/未识别跳过的条目）时保留原地，避免误吞内容目录
			remaining, relistErr := listPendingTopLevel(ops, entry.Cid)
			if relistErr != nil {
				onLog(fmt.Sprintf("○ %s/ - 复查目录失败，保留原地: %v", entry.Name, relistErr))
				return results
			}
			if len(remaining) > 0 {
				// 散落的纯垃圾文件（txt/url 广告等）可随壳一起清进冗余
				allJunk, junkFids := true, []string{}
				for _, r := range remaining {
					if r.IsDir || classifyFile(r.Name) != FileTypeJunk {
						allJunk = false
						break
					}
					junkFids = append(junkFids, r.Fid)
				}
				if allJunk && len(junkFids) > 0 {
					if err := ops.moveFiles(cfg.Redundant, junkFids); err == nil {
						onLog(fmt.Sprintf("○ %s/ - 容器内 %d 个垃圾文件已移到冗余", entry.Name, len(junkFids)))
						remaining, _ = listPendingTopLevel(ops, entry.Cid)
					}
				}
			}
			if len(remaining) == 0 {
				ctx.pruner.mark(entry.Fid, entry.Name)
				onLog(fmt.Sprintf("○ %s/ - 容器目录已清空，收尾统一清理", entry.Name))
			} else {
				onLog(fmt.Sprintf("○ %s/ - 容器目录仍有 %d 个残留条目，保留原地", entry.Name, len(remaining)))
			}
			return results
		}
	}

	// 单部影视目录：收集所有文件识别入库
	subFiles, err := collectDirFiles(ops, entry.Cid, entry.Name)
	if err != nil {
		onLog(fmt.Sprintf("✗ %s/ - 遍历失败: %v", entry.Name, err))
		// 遍历失败是用户要能看见的失败：目录还留在待整理，下轮会重试，
		// 但连续失败（cid 失效/风控）只在日志里一闪而过就没人知道了
		ctx.sink.noteFail(entry.Name+"/", entry.Fid, "dir", "failed", "recognize",
			"读取目录内容失败，留在待整理目录下轮重试: "+err.Error(), nil)
		return results
	}
	if len(subFiles) == 0 {
		// 空目录（离线/转存产生的空壳）直接删：不处理会导致守望者
		// 反复"整理后转存目录仍有内容"且无任何日志可查
		ctx.pruner.mark(entry.Fid, entry.Name)
		onLog(fmt.Sprintf("○ %s/ - 空目录（无任何文件），待清理", entry.Name))
		return results
	}
	result := processDir(ctx, entry, subFiles)
	results = append(results, result...)
	for _, r := range result {
		if r.Status == "success" {
			*successCount++
		}
	}
	return results
}

// fileKindSummary 按类型汇总条目里的文件数，如「视频 52、字幕 52、NFO/封面 2、其他 3」。
// 剧集目录动辄上百个文件，日志只报「目录 + 每类几个」；电影那种单文件条目才打文件名
func fileKindSummary(files []remoteFile) string {
	var video, sub, meta, junk int
	for _, f := range files {
		switch classifyFile(f.Name) {
		case FileTypeVideo:
			video++
		case FileTypeSubtitle:
			sub++
		case FileTypeNFO, FileTypeStdImage:
			meta++
		default:
			junk++
		}
	}
	var parts []string
	for _, kv := range []struct {
		label string
		n     int
	}{{"视频", video}, {"字幕", sub}, {"NFO/封面", meta}, {"其他", junk}} {
		if kv.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", kv.label, kv.n))
		}
	}
	if len(parts) == 0 {
		return "空"
	}
	return strings.Join(parts, "、")
}

// processDir 处理一个子目录（包含多个文件的影视目录）
func processDir(ctx *orgCtx, dir dirEntry, files []remoteFile) []OrganizeResult {
	ops, cfg, tc, replaceRules, libAbs, onLog := ctx.ops, ctx.cfg, ctx.tc, ctx.rules, ctx.libAbs, ctx.onLog
	var results []OrganizeResult

	// 记录快照：整理记录要能在事后定位这些文件（fid 在 115 上移动/改名后不变），
	// 所以失败分支也照样登记，用户才能在记录页点「重新整理」把它们捞回来
	snapshot := func(names map[string]string) []orgRecordFile {
		out := make([]orgRecordFile, 0, len(files))
		for _, f := range files {
			n := f.Name
			if names != nil {
				if v, ok := names[f.Fid]; ok {
					n = v
				}
			}
			out = append(out, orgRecordFile{
				Fid: f.Fid, Name: n, Orig: recordOrig(f.Name, n), Kind: recordFileKind(n),
				PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1,
			})
		}
		return out
	}
	fail := func(status, stage, msg string) {
		ctx.sink.noteFail(dir.Name+"/", dir.Fid, "dir", status, stage, msg, snapshot(nil))
	}
	failMedia := func(status, stage, msg string, media *TmdbMedia, category, targetDir string) {
		rec := &model.OrganizeRecord{
			Source: dir.Name + "/", SourceFid: dir.Fid, SourceKind: "dir",
			Status: status, Stage: stage, Message: msg,
			Category: category, TargetDir: targetDir,
			Files: marshalRecordFiles(snapshot(nil)),
		}
		if media != nil {
			rec.TmdbID, rec.Title, rec.Year, rec.MediaType, rec.PosterPath =
				media.TmdbID, media.Title, media.Year, media.MediaType, media.PosterPath
		}
		ctx.sink.note(rec)
	}

	// 分流视频：正片 vs 广告/引流（清洗后无有效内容名、仍含域名、或小于
	// 最小体积的"视频"是广告载体，不能重命名成正片名混入库）
	var videoFiles []remoteFile
	var adFids []string      // 广告/超小视频：循环后合并一次移动（逐个移动每个要过写限流）
	var adFiles []remoteFile // 同批文件的指纹登记用
	minBytes := int64(cfg.MinSize) * 1024 * 1024
	for _, f := range files {
		if classifyFile(f.Name) != FileTypeVideo {
			continue
		}
		switch {
		case isAdOnlyVideo(f.Name):
			onLog(fmt.Sprintf("○ %s - 广告/引流视频，移到冗余", f.Name))
			adFids = append(adFids, f.Fid)
			adFiles = append(adFiles, f)
		case minBytes > 0 && f.Size > 0 && f.Size < minBytes:
			onLog(fmt.Sprintf("○ %s - 仅 %.1fMB（小于最小体积 %dMB），移到冗余", f.Name, float64(f.Size)/1024/1024, cfg.MinSize))
			adFids = append(adFids, f.Fid)
			adFiles = append(adFiles, f)
		default:
			videoFiles = append(videoFiles, f)
		}
	}

	if len(adFids) > 0 {
		if junkCid, err := ops.ensurePath(cfg.Redundant, dir.Name); err == nil {
			ops.moveFiles(junkCid, adFids)
		}
	}

	if len(videoFiles) == 0 {
		// 没有视频文件，整个目录移到冗余
		allFids := make([]string, 0, len(files))
		for _, f := range files {
			allFids = append(allFids, f.Fid)
		}
		if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到冗余失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - 无视频文件，已移到冗余", dir.Name))
		}
		fail("unrecognized", "recognize", "目录内没有视频文件，已移到冗余")
		return results
	}

	// 识别第一个视频（取最大的文件作为主视频）
	mainVideo := videoFiles[0]
	for _, v := range videoFiles {
		if v.Size > mainVideo.Size {
			mainVideo = v
		}
	}

	// 去重方式：不再查本地台账，TMDB 识别后直接查网盘目标目录的 SHA1（checkByCloudSHA1）。
	// 好处：永远准确（查的是网盘实时状态），无本地缓存过期问题。

	// 应用替换规则
	name := mainVideo.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	onLog(fmt.Sprintf("▶ 开始识别: %s/（%s；样本: %s）",
		shortLogName(dir.Name), fileKindSummary(files), shortLogName(mainVideo.Name)))

	parsed := parseFileName(name)
	// 文件名无法提取标题 → 用目录名识别（目录名通常比文件名规范）
	// 场景：/西游记.1987/ep01.mkv — 文件名只有集数，目录名有标题和年份
	useDirName := false
	if parsed.Title == "" || isEpisodeOnly(parsed.Title) {
		dirParsed := parseFileName(dir.Name)
		if dirParsed.Title != "" && !isEpisodeOnly(dirParsed.Title) {
			onLog(fmt.Sprintf("▣ 文件名 %q 无法识别，改用目录名 %q", name, dir.Name))
			// 用目录名做识别，但保留文件名解析出的季集号
			if parsed.Season == 0 {
				parsed.Season = dirParsed.Season
			}
			if parsed.Episode == 0 {
				parsed.Episode = dirParsed.Episode
			}
			if parsed.Year == "" {
				parsed.Year = dirParsed.Year
			}
			parsed.IsTV = dirParsed.IsTV || parsed.IsTV
			parsed = dirParsed // 用目录名的标题/年份
			// 恢复文件名中的季集号（如果目录名没有的话）
			if parsed.Season == 0 && parseFileName(name).Season > 0 {
				parsed.Season = parseFileName(name).Season
			}
			if parsed.Episode == 0 && parseFileName(name).Episode > 0 {
				parsed.Episode = parseFileName(name).Episode
			}
			if fp := parseFileName(name); fp.TmdbID > 0 {
				parsed.TmdbID, parsed.TmdbKind = fp.TmdbID, fp.TmdbKind
			}
			useDirName = true
		}
	}
	// 目录名上的 id 标签同样算数（整理好的目录常见 "片名 (2019) [tmdbid=123]"，
	// 里面的文件名却不带标签）；文件名自己带了的优先
	if parsed.TmdbID == 0 {
		parsed.TmdbID, parsed.TmdbKind = extractTmdbID(dir.Name)
	}

	var media *TmdbMedia
	if ctx.forced != nil {
		media = ctx.forced
		onLog(fmt.Sprintf("✦ 人工确认: %s → %s (%s)", shortLogName(dir.Name), media.Title, media.Year))
	} else {
		if parsed.Title == "" && parsed.TmdbID == 0 {
			if ctx.holdable() {
				return append(results, ctx.holdForConfirm(dir.Name+"/", dir.Fid, "dir", nil, parsed, mainVideo.Name,
					snapshot(nil), "文件名与目录名都提取不出片名，请手动指定 TMDB 条目"))
			}
			// 文件名和目录名都无法识别，移到冗余
			moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
			onLog(fmt.Sprintf("○ %s/ - 无法提取标题，已移到冗余", dir.Name))
			fail("unrecognized", "recognize", "文件名与目录名都提取不出片名，已移到冗余")
			return results
		}

		// TMDB 识别
		var err error
		media, err = tc.recognize(parsed)
		if err != nil || media == nil {
			// 文件名识别失败 → 如果还没试过目录名，用目录名再识别一次
			if !useDirName {
				dirParsed := parseFileName(dir.Name)
				if dirParsed.Title != "" {
					onLog(fmt.Sprintf("▣ 文件名识别失败，改用目录名 %q 重试", dir.Name))
					// 保留文件名的季集号
					if dirParsed.Season == 0 {
						dirParsed.Season = parsed.Season
					}
					if dirParsed.Episode == 0 {
						dirParsed.Episode = parsed.Episode
					}
					media, err = tc.recognize(dirParsed)
				}
			}
			if err != nil || media == nil {
				if err != nil {
					// 瞬时错误（网络抖动/限流）≠ 找不到：绝不移冗余——
					// 好内容被误分流后只能靠人工捞回。留在待整理目录，下轮重试
					results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "TMDB 暂时不可达: " + err.Error()})
					onLog(fmt.Sprintf("○ %s/ - TMDB 暂时不可达（%v），留在待整理目录下轮重试", dir.Name, err))
					fail("failed", "recognize", "TMDB 暂时不可达，留在待整理目录下轮重试: "+err.Error())
					return results
				}
				if ctx.holdable() {
					return append(results, ctx.holdForConfirm(dir.Name+"/", dir.Fid, "dir", nil, parsed, mainVideo.Name,
						snapshot(nil), "TMDB 未找到匹配条目，请手动指定"))
				}
				moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
				onLog(fmt.Sprintf("○ %s/ - TMDB 未找到匹配，已移到冗余", dir.Name))
				fail("unrecognized", "recognize", "TMDB 未找到匹配条目，已移到冗余")
				return results
			}
		}

		onLog(fmt.Sprintf("✦ 识别成功: %s → %s (%s)", shortLogName(dir.Name), media.Title, media.Year))
		if ctx.holdable() {
			return append(results, ctx.holdForConfirm(dir.Name+"/", dir.Fid, "dir", media, parsed, mainVideo.Name,
				snapshot(nil), ""))
		}
	}

	// 直接查网盘去重（不依赖本地缓存表，不会过期）
	// 检查网盘目标目录里是否有相同 SHA1 的文件
	if len(videoFiles) == 1 && checkByCloudSHA1(ops, media, cfg, libAbs, mainVideo.Sha1, parsed, mainVideo.Name) {
		if err := ops.moveFiles(cfg.Existing, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到已存在失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ → 已存在: %s (%s)，已移到已存在目录", dir.Name, media.Title, media.Year))
		}
		for _, vf := range videoFiles {
			results = append(results, OrganizeResult{FileName: vf.Name, Status: "exists", Title: media.Title, Year: media.Year, MediaType: media.MediaType,
				Message: "网盘已有相同文件"})
		}
		failMedia("exists", "recognize", "网盘已有相同文件，已移到已存在目录", media, "", "")
		return results
	}

	// 分类 + 目标目录先算出来：洗版要按「这个新文件将要落到哪个库内目录」去查
	// 库内现有版本，不能再依赖 MediaLibrary 记录（同步建起来的库没有那张表的行）
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, mainVideo.Name)
	targetDir := libSubPath(categoryDir(media.MediaType, category), pathDir(newPath))
	holdingDir := recognizedHoldingDir(media, parsed, mainVideo.Name)
	if holdingDir == "" {
		holdingDir = sourceHoldingDir(dir.Name, parsed.Title)
	}

	// 洗版**判定**必须逐文件做（主视频重复或画质不佳，不代表同目录的新增集也该拒收），
	// 但**执行**一律攒到判完再发。逐集各发一次 115 写请求要过 3 秒写间隔：
	// 153 集的动漫光让位 + 移「已存在」就是十几分钟，这段时间整理一直占着 taskMu，
	// 增量同步每 30 秒来一次全被挡回去（现场日志：转存后 2 分半还没开始整理）
	sc := newWashScanner(ops, cfg)
	st := matchWashStrategy(media.MediaType, category)
	var accepted []remoteFile
	var plans []*washPlan
	var rejectFids []string         // 判为已存在的视频 + 跟着它命名的字幕
	var rejectFiles []orgRecordFile // 同一批的整理记录快照
	rejectVideos, sameFileVideos := 0, 0
	excluded := map[string]bool{}
	take := func(f remoteFile) {
		excluded[f.Fid] = true
		rejectFids = append(rejectFids, f.Fid)
		rejectFiles = append(rejectFiles, orgRecordFile{Fid: f.Fid, Name: f.Name,
			Kind: recordFileKind(f.Name), PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1})
	}
	for _, vf := range videoFiles {
		vp := parseFileName(vf.Name)
		if vp.Season == 0 {
			vp.Season = parsed.Season
		}
		vpath := buildNewNameWithTemplate(media, vp, vf.Name)
		vdir := libSubPath(categoryDir(media.MediaType, category), pathDir(vpath))
		plan := washNoStrategy(vf.Name, sc.sameFile(vf.Sha1), onLog)
		if st != nil {
			plan = decideWash(media, vf.Name, vf.Sha1, vdir, st, sc.libFiles(vdir), sc.sameFile(vf.Sha1), onLog)
		}
		if plan.decision == washReplaced {
			p := plan
			plans = append(plans, &p)
		}
		if plan.decision != washNotBetter && plan.decision != washSameFile {
			accepted = append(accepted, vf)
			continue
		}
		rejectVideos++
		if plan.decision == washSameFile {
			sameFileVideos++
		}
		take(vf)
		for _, af := range files {
			if classifyFile(af.Name) == FileTypeSubtitle && strings.HasPrefix(af.Name, baseName(vf.Name)+".") {
				take(af)
			}
		}
		results = append(results, OrganizeResult{FileName: vf.Name, Status: "exists", Title: media.Title,
			Year: media.Year, MediaType: media.MediaType, Message: washExistsMsg(plan.decision)})
	}
	// 旧版让位：整批一次网盘请求
	if len(plans) > 0 {
		if err := applyWashPlans(ops, cfg, media, st, plans, onLog, notifyEmbyDeleted); err != nil {
			failMedia("failed", "move", "洗版旧版让位失败，保留待整理文件", media, category, targetDir)
			return append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "洗版旧版让位失败"})
		}
	}
	// 判为已存在的整批搬一次，整理记录也只留一条：一集一条的话 153 集能把
	// 记录页刷满好几页，而它们本来就是同一部片同一次动作
	if len(rejectFids) > 0 {
		if _, err := moveToHoldingDir(ops, cfg.Existing, holdingDir, rejectFids); err != nil {
			failMedia("failed", "move", "移到已存在失败: "+err.Error(), media, category, targetDir)
			return append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: err.Error()})
		}
		reason := "库内已有更优版本"
		switch {
		case sameFileVideos == rejectVideos:
			reason = washExistsMsg(washSameFile)
		case sameFileVideos > 0:
			reason = "库内已有同一份文件或更优版本"
		}
		msg := fmt.Sprintf("%s，%d 个视频已移到 已存在/%s", reason, rejectVideos, holdingDir)
		onLog(fmt.Sprintf("○ %s/ - %s", dir.Name, msg))
		ctx.sink.note(&model.OrganizeRecord{
			Source: dir.Name + "/", SourceFid: dir.Fid, SourceKind: "dir",
			Status: "exists", Message: msg,
			TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
			MediaType: media.MediaType, PosterPath: media.PosterPath,
			Category: category, TargetDir: targetDir,
			Files: marshalRecordFiles(rejectFiles), VideoCount: rejectVideos,
		})
	}
	if len(accepted) == 0 {
		return results
	}
	videoFiles = accepted
	remaining := make([]remoteFile, 0, len(files))
	for _, f := range files {
		if !excluded[f.Fid] {
			remaining = append(remaining, f)
		}
	}
	files = remaining

	_ = targetDir // 目标目录在下方按新结构创建（根目录 + 季目录）

	// 按文件分类移动（规范结构）：
	//   视频 + 字幕 → 季目录（电影为根目录）；NFO + 标准封面图 → 剧集根目录；垃圾 → 冗余
	parts := strings.Split(newPath, "/")
	rootRel := libSubPath(categoryDir(media.MediaType, category), parts[0])
	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", rootRel))
	rootCid, err := ops.ensurePath(cfg.Library, rootRel)
	if err != nil {
		onLog(fmt.Sprintf("✗ %s/ - 创建目录失败: %v（目标=%q）", dir.Name, err, rootRel))
		results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建目录失败: " + err.Error()})
		failMedia("failed", "move", "创建目标目录失败: "+err.Error(), media, category, rootRel)
		return results
	}
	mediaCid := rootCid
	mediaRel := rootRel                             // 视频与字幕的实际落点（电影同标题目录，剧集到季目录）
	if media.MediaType == "tv" && len(parts) >= 2 { // Season XX 层
		onLog(fmt.Sprintf("▣ 季目录就绪: %s/%s", rootRel, parts[1]))
		mediaRel = rootRel + "/" + parts[1]
		mediaCid, err = ops.ensurePath(cfg.Library, rootRel+"/"+parts[1])
		if err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 创建季目录失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建季目录失败: " + err.Error()})
			failMedia("failed", "move", "创建季目录失败: "+err.Error(), media, category, rootRel)
			return results
		}
	}

	var mediaFids, metaFids, junkFids []string
	for _, f := range files {
		switch classifyFile(f.Name) {
		case FileTypeVideo:
			// 广告/引流视频与超小视频不随正片入库（此前按扩展名误入 mediaFids，
			// 被前移环节的过滤漏掉、未改名就搬进了库）
			if isAdOnlyVideo(f.Name) || (minBytes > 0 && f.Size > 0 && f.Size < minBytes) {
				junkFids = append(junkFids, f.Fid)
			} else {
				mediaFids = append(mediaFids, f.Fid)
			}
		case FileTypeSubtitle:
			mediaFids = append(mediaFids, f.Fid)
		case FileTypeNFO, FileTypeStdImage:
			metaFids = append(metaFids, f.Fid) // 封面/NFO 放剧集根目录
		default:
			junkFids = append(junkFids, f.Fid)
		}
	}

	// 同步补全：文件名缺画质信息时立即探测（ffprobe 拉头部几 MB），
	// 用探测结果补充画质后再重命名 → 移动 → 入库，一步到位
	enrichRenames := map[string]string{} // 补全前视频基名 → 补全后基名（字幕跟随用）
	enriched, enrichFailed := 0, 0
	if policy := loadEnrichPolicy(); policy.Enabled {
		for i, vf := range videoFiles {
			if !enrichNeedsProbe(vf.Name) || vf.PickCode == "" {
				continue
			}
			// 单文件条目（电影/散文件）才逐个点名；剧集几十集逐条打就是几十行，
			// 只累计数量、收尾给一行汇总（vlog 默认是开的，降级到 vlog 没用）
			enrichLog := onLog
			if len(videoFiles) > 1 {
				enrichLog = func(string) {}
			}
			enrichLog(fmt.Sprintf("▣ 补全探测: %s（文件名缺画质信息）", vf.Name))
			probe, perr := probeFileNow(vf.PickCode)
			if probe == nil {
				enrichLog(fmt.Sprintf("○ 补全探测失败 %s（保留原名）: %s", vf.Name, perr))
				enrichFailed++
				continue
			}
			enriched++
			{
				action, reason := enrichDecide(vf.Name, probe, policy)
				if action == "rename" {
					ext := pathExt(vf.Name)
					base := strings.TrimSuffix(vf.Name, ext)
					newName := buildEnrichedName(base, ext, probe)
					if newName != vf.Name {
						if err := ops.rename(vf.Fid, newName); err != nil {
							enrichLog(fmt.Sprintf("○ 补全改名失败 %s: %v", vf.Name, err))
						} else {
							enrichLog(fmt.Sprintf("✓ 补全 %s → %s", vf.Name, newName))
							enrichRenames[baseName(vf.Name)] = baseName(newName)
							videoFiles[i].Name = newName // 后续模板重命名基于补全后的名字
						}
					}
				} else {
					enrichLog(fmt.Sprintf("○ 补全跳过 %s: %s", vf.Name, reason))
				}
			}
		}
	}

	if len(videoFiles) > 1 && enriched+enrichFailed > 0 {
		failNote := ""
		if enrichFailed > 0 {
			failNote = fmt.Sprintf("，%d 个探测失败保留原名", enrichFailed)
		}
		onLog(fmt.Sprintf("▣ 画质补全：探测 %d 个视频%s", enriched, failNote))
	}

	// 先在源目录重命名（批量 batch_rename），再移动到目标。
	// finalNames 是落盘的依据：文件搬过去之后叫什么，只有这里知道
	finalNames := map[string]string{}
	for _, vf := range videoFiles {
		finalNames[vf.Fid] = vf.Name // 补全探测可能已经改过名
	}
	for fid, n := range renameBeforeMove(ops, media, videoFiles, files, enrichRenames, onLog) {
		finalNames[fid] = n
	}

	// 视频/字幕 → 季目录（电影为根目录）
	if len(mediaFids) > 0 {
		onLog(fmt.Sprintf("▣ 移动 %d 个视频/字幕 → %s（cid=%s）", len(mediaFids), mediaRel, mediaCid))
		if err := ops.moveFiles(mediaCid, mediaFids); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动文件失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "移动文件失败: " + err.Error()})
			failMedia("failed", "move", "移动文件到媒体库失败: "+err.Error(), media, category, rootRel)
			return results
		}
	}
	// 封面/NFO → 剧集根目录
	if len(metaFids) > 0 {
		onLog(fmt.Sprintf("▣ 移动 %d 个 NFO/封面 → %s（cid=%s）", len(metaFids), rootRel, rootCid))
	}
	if len(metaFids) > 0 && mediaCid != rootCid {
		if err := ops.moveFiles(rootCid, metaFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 封面/NFO 移动到根目录失败（留在季目录）: %v", dir.Name, err))
		}
	} else if len(metaFids) > 0 {
		// 电影：全部进根目录
		if err := ops.moveFiles(rootCid, metaFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 封面/NFO 移动失败: %v", dir.Name, err))
		}
	}

	// 移动无用文件到冗余
	if len(junkFids) > 0 {
		junkCid, err := ops.ensurePath(cfg.Redundant, dir.Name)
		if err != nil {
			onLog(fmt.Sprintf("○ %s/ - 冗余目录创建失败，%d 个无用文件留在原地: %v", dir.Name, len(junkFids), err))
		} else if err := ops.moveFiles(junkCid, junkFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - %d 个无用文件移到冗余失败: %v", dir.Name, len(junkFids), err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - %d 个无用文件已移到 冗余/%s", dir.Name, len(junkFids), dir.Name))
		}
	}

	// 海报/封面 → poster.ext（Emby 只认 poster/folder 等标准名，
	// 动漫资源包自带的 海报.png/封面.jpg 不改名不会被刮削器使用）
	for _, f := range files {
		ext := strings.ToLower(pathExt(f.Name))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
			continue
		}
		base := strings.ToLower(baseName(f.Name))
		if base != "海报" && base != "封面" {
			continue
		}
		newName := "poster" + pathExt(f.Name)
		if newName != f.Name {
			if err := ops.rename(f.Fid, newName); err != nil {
				onLog(fmt.Sprintf("○ 海报重命名失败 %s: %v", f.Name, err))
			} else {
				onLog(fmt.Sprintf("✓ 海报 %s → %s", f.Name, newName))
				finalNames[f.Fid] = newName
			}
		}
	}

	// 整理完毕收拾源目录：整棵没有文件就删掉（进 115 回收站，可还原）。
	// 此前一律搬进冗余——冗余目录被空壳越堆越多，点进去什么都没有。
	// 注意不能只看直接子项：待整理常见 片名/Season 01/*.mkv，文件搬走后
	// 父目录里还挂着空的 Season 01，pruneOrMove 会递归判断整棵子树
	pruneOrMove(ops, dir.Fid, ctx.pruner.protectedSet(), cfg.Redundant, dir.Name+"/", onLog)

	// ---- 一条龙落盘：STRM + 附属文件直接由整理写出 ----
	// 到这一步 targetDir / fid / pickcode / 最终文件名全都在手里，
	// 没有任何理由再让增量同步从生活事件里把它们反推一遍
	nameOf := func(f remoteFile) string {
		if n, ok := finalNames[f.Fid]; ok && n != "" {
			return n
		}
		return f.Name
	}
	movedFids := map[string]bool{}
	for _, fid := range mediaFids {
		movedFids[fid] = true
	}
	for _, fid := range metaFids {
		movedFids[fid] = true
	}
	var strmVideos, strmAssets []remoteFile
	recFiles := make([]orgRecordFile, 0, len(files))
	for _, f := range files {
		orig := f.Name
		f.Name = nameOf(f)
		kind := recordFileKind(f.Name)
		recFiles = append(recFiles, orgRecordFile{
			Fid: f.Fid, Name: f.Name, Orig: recordOrig(orig, f.Name), Kind: kind,
			PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1,
		})
		if !movedFids[f.Fid] {
			continue // 广告/垃圾已进冗余，不落盘
		}
		if kind == "video" {
			strmVideos = append(strmVideos, f)
		} else if kind == "subtitle" || kind == "meta" {
			strmAssets = append(strmAssets, f)
		}
	}
	strmCreated, assetsDL := ctx.sink.commit(ops, media, rootRel, mediaRel, strmVideos, strmAssets)
	onLog(fmt.Sprintf("✓ %s/ - 落盘完成：STRM %d 个、附属 %d 个 → %s",
		dir.Name, strmCreated, assetsDL,
		filepath.Join(ctx.sink.localRoot, filepath.FromSlash(ctx.sink.libRel(mediaRel)))))

	// 记录到数据库
	recordMedia(media, category, targetDir+"/"+pathBase(newPath))
	ctx.sink.note(&model.OrganizeRecord{
		Source: dir.Name + "/", SourceFid: dir.Fid, SourceKind: "dir",
		Status: "success", Stage: "", Message: fmt.Sprintf("→ %s", targetDir),
		TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
		MediaType: media.MediaType, PosterPath: media.PosterPath,
		Category: category, TargetDir: rootRel, TargetCid: rootCid,
		Files: marshalRecordFiles(recFiles), VideoCount: len(strmVideos),
		TotalSize: sumSizes(strmVideos), StrmCreated: strmCreated,
	})

	// 入库成功通知：TMDB 封面 + 重命名信息 + 详情链接（企微图文卡 / TG 图片）
	// 入库文件统计（视频+字幕+NFO/封面；垃圾文件已移冗余不计）
	movedSet := map[string]bool{}
	for _, f := range mediaFids {
		movedSet[f] = true
	}
	for _, f := range metaFids {
		movedSet[f] = true
	}
	movedCount, movedBytes := 0, int64(0)
	for _, f := range files {
		if movedSet[f.Fid] {
			movedCount++
			movedBytes += f.Size
		}
	}
	notifyMediaStoredFull(media, category, videoFiles, mainVideo.Name, movedCount, movedBytes)

	// 生成结果
	for _, vf := range videoFiles {
		results = append(results, OrganizeResult{
			FileName:  vf.Name,
			Status:    "success",
			TmdbID:    media.TmdbID,
			Title:     media.Title,
			Year:      media.Year,
			MediaType: media.MediaType,
			Category:  category,
			TargetDir: targetDir,
			Message:   fmt.Sprintf("→ %s (%s) [%s]", media.Title, media.Year, media.MediaType),
		})
	}
	onLog(fmt.Sprintf("✓ %s/ → %s (%s) [%s/%s] → %s", dir.Name, media.Title, media.Year, category, media.MediaType, targetDir))

	return results
}

// processSingleFileWithSiblings 散文件批量处理：识别第一个文件后，
// 同前缀的其他散文件共享识别结果（一部剧 24 集只需 1 次 TMDB 调用）
// 前缀判定：文件名去掉 EP/SxxExx/集数 部分后剩余部分相同
func processSingleFileWithSiblings(ctx *orgCtx, f remoteFile) []OrganizeResult {
	onLog := ctx.onLog
	// 先识别主文件
	result, rec := processSingleFile(ctx, f)
	if result.Status != "success" {
		return []OrganizeResult{result}
	}

	siblings := seriesSiblings(ctx, f)
	if len(siblings) == 0 {
		return []OrganizeResult{result} // 没有同前缀的其他文件
	}

	onLog(fmt.Sprintf("▣ 发现 %d 个同前缀散文件，共享识别结果批量处理", len(siblings)))

	// 构建与主文件相同的目标（分类/目录/媒体信息从 result 提取不行，
	// 需要重新构造——用主文件的 parsed 和 media）
	// 简化：直接用 processDir 逻辑处理剩余文件
	var allResults = []OrganizeResult{result}
	successCount := 1

	for _, sib := range siblings {
		ctx.handled[sib.Fid] = true
		sibResult, sibFiles, sibStrm := organizeIdentifiedFile(ctx, sib, result)
		allResults = append(allResults, sibResult)
		if sibResult.Status == "success" {
			successCount++
		}
		// 并进主文件那条记录：一部剧的 24 集是一个整理动作，不该刷出 24 行
		ctx.sink.appendToRecord(rec, sibFiles, sibStrm, sib.Size)
	}

	onLog(fmt.Sprintf("✓ 散文件批量完成: 共 %d 个文件（成功 %d）", len(allResults), successCount))
	return allResults
}

// seriesSiblings 扫描根里与 f 同前缀的其他散视频（同一部剧的其他集）。
// 前缀判定：文件名去掉 EP/SxxExx/集数 部分后剩余部分相同；
// 已经在等人工确认、或本轮已被处理掉的不算
func seriesSiblings(ctx *orgCtx, f remoteFile) []remoteFile {
	mainPrefix := extractSeriesPrefix(f.Name)
	if mainPrefix == "" {
		return nil // 无法提取前缀，不批量处理
	}

	entries, _, err := ctx.ops.listEntries(ctx.cfg.Pending, 0)
	if err != nil {
		return nil // 列表失败，只处理主文件
	}

	var siblings []remoteFile
	for _, e := range entries {
		if fmt.Sprint(e["f"]) != "1" { // 只看文件
			continue
		}
		name := fmt.Sprint(e["n"])
		if name == f.Name {
			continue // 跳过主文件（已处理）
		}
		if classifyFile(name) != FileTypeVideo {
			continue
		}
		if extractSeriesPrefix(name) == mainPrefix {
			sib := remoteFile{
				Fid:  fmt.Sprint(e["fid"]),
				Name: name,
				Size: 0,
				Sha1: nilSprint(e["sha"]),
			}
			if ctx.handled[sib.Fid] || ctx.held[sib.Fid] != nil {
				continue
			}
			if pc := nilSprint(e["pc"]); pc != "" {
				sib.PickCode = pc // 散文件也要能触发补全探测（此前恒缺，静默跳过）
			}
			if sz, ok := e["s"].(float64); ok {
				sib.Size = int64(sz)
			}
			siblings = append(siblings, sib)
		}
	}
	return siblings
}

// extractSeriesPrefix 提取剧集文件名的系列前缀（去掉 EP/SxxExx/集数部分）
// "BLJXD.2026.EP01.HD1080P..." → "BLJXD.2026"
// "Show.Name.S01E05.720p..." → "Show.Name"
func extractSeriesPrefix(name string) string {
	base := baseName(name)
	// 按 . 分割，找 EP/SxxExx 位置截断
	parts := strings.Split(base, ".")
	for i, part := range parts {
		upper := strings.ToUpper(part)
		// 匹配 EP01, E01, S01E05, S01E05E06 等
		if len(upper) >= 3 {
			if strings.HasPrefix(upper, "EP") && len(upper) <= 5 && isAllDigits(upper[2:]) {
				return strings.Join(parts[:i], ".")
			}
			if strings.HasPrefix(upper, "S") && strings.Contains(upper, "E") && len(upper) <= 10 {
				return strings.Join(parts[:i], ".")
			}
		}
		// 匹配纯数字段（可能是集数）
		if isAllDigits(part) && len(part) <= 3 && i > 0 {
			// 前一段不是年份（4位）则认为这是集数
			if !isAllDigits(parts[i-1]) || len(parts[i-1]) != 4 {
				return strings.Join(parts[:i], ".")
			}
		}
	}
	return base // 没找到 EP 标记，返回整个基名
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// organizeIdentifiedFile 用已识别的媒体信息处理单个散文件（跳过 TMDB 识别）
// organizeIdentifiedFile 用主文件的识别结果处理一个同前缀兄弟文件。
// 返回结果 + 本文件的记录条目 + 生成的 STRM 数（由调用方并进主记录）
func organizeIdentifiedFile(ctx *orgCtx, f remoteFile, mainResult OrganizeResult) (OrganizeResult, []orgRecordFile, int) {
	ops, cfg, replaceRules, onLog := ctx.ops, ctx.cfg, ctx.rules, ctx.onLog
	result := OrganizeResult{FileName: f.Name}

	// sha1 去重不再在这里抢答：它是一句全表查询，命中就直接跳过洗版且不打日志，
	// 用户配着 replace 只能看到「已存在」而无从解释。现在同一份文件的判定
	// 并进 decideWash（wash.go），由策略决定是「真的已存在」还是「位置不规范该让位」

	// 解析文件名获取季集号
	name := f.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	parsed := parseFileName(name)

	// 从主结果提取媒体信息
	// 重新调 recognize 太浪费——用 media 重建
	// 简化：直接构造目标路径
	media := &TmdbMedia{
		Title:     mainResult.Title,
		Year:      mainResult.Year,
		MediaType: mainResult.MediaType,
		TmdbID:    mainResult.TmdbID,
	}

	category := mainResult.Category
	newPath := buildNewNameWithTemplate(media, parsed, f.Name)
	targetDir := libSubPath(categoryDir(media.MediaType, category), pathDir(newPath))
	holdingDir := recognizedHoldingDir(media, parsed, f.Name)
	if holdingDir == "" {
		holdingDir = sourceHoldingDir(f.Name, parsed.Title)
	}

	// 洗版判定：每集各判一次（主文件赢了不代表这一集也该顶掉库内的）
	switch decision := tryWashReplace(ops, cfg, media, f.Name, f.Sha1, targetDir, onLog); decision {
	case washFailed:
		return OrganizeResult{FileName: f.Name, Status: "failed", Message: "洗版旧版让位失败"}, nil, 0
	case washReplaced:
		// 旧版已让位，落入下方正常入库
	case washNotBetter, washSameFile:
		holdingCid, err := moveToHoldingDir(ops, cfg.Existing, holdingDir, []string{f.Fid})
		if err != nil {
			return OrganizeResult{FileName: f.Name, Status: "failed", Message: "移到已存在失败: " + err.Error()}, nil, 0
		}
		moveSiblingAttachments(ops, cfg.Pending, baseName(f.Name), "", holdingCid, false, onLog)
		msg := washExistsMsg(decision) + "，已移到 已存在/" + holdingDir
		onLog(fmt.Sprintf("○ %s - %s", f.Name, msg))
		return OrganizeResult{FileName: f.Name, Status: "exists", Message: msg}, nil, 0
	}

	rootRel := libSubPath(categoryDir(media.MediaType, category), strings.SplitN(newPath, "/", 2)[0])

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		return result, nil, 0
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动失败: " + err.Error()
		return result, nil, 0
	}

	// 重命名为标准名
	finalName := f.Name
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
			finalName = stdName
		}
	}

	// 一条龙落盘（与主文件同一个片目，sink 内部按 key 去重刮削目标）
	video := f
	video.Name = finalName
	strmCreated, _ := ctx.sink.commit(ops, media, rootRel, targetDir, []remoteFile{video}, nil)
	recFiles := []orgRecordFile{{Fid: f.Fid, Name: finalName, Orig: recordOrig(f.Name, finalName),
		Kind: "video", PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}}

	result.Category = category
	result.TargetDir = targetDir
	result.TmdbID = mainResult.TmdbID
	result.Title = mainResult.Title
	result.Year = mainResult.Year
	result.MediaType = mainResult.MediaType
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s] → %s", mainResult.Title, mainResult.Year, category, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s", f.Name, stdPath(newPath)))
	return result, recFiles, strmCreated
}

func stdPath(p string) string {
	return p
}

// processSingleFile 处理待整理目录下的顶层单独视频文件
// processSingleFile 处理一个散视频。第二个返回值是本次留下的整理记录：
// 同前缀的兄弟文件会并进同一条记录（一部剧 24 集不该刷出 24 行）
func processSingleFile(ctx *orgCtx, f remoteFile) (OrganizeResult, *model.OrganizeRecord) {
	ops, cfg, tc, replaceRules, libAbs, onLog := ctx.ops, ctx.cfg, ctx.tc, ctx.rules, ctx.libAbs, ctx.onLog
	result := OrganizeResult{FileName: f.Name}
	self := []orgRecordFile{{Fid: f.Fid, Name: f.Name, Kind: recordFileKind(f.Name), PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}}
	appendSelf := func(files []remoteFile) {
		for _, a := range files {
			self = append(self, orgRecordFile{Fid: a.Fid, Name: a.Name, Kind: recordFileKind(a.Name), PickCode: a.PickCode, Size: a.Size, Sha1: a.Sha1})
		}
	}
	fail := func(status, stage, msg string) (OrganizeResult, *model.OrganizeRecord) {
		ctx.sink.noteFail(f.Name, f.Fid, "file", status, stage, msg, self)
		return result, nil
	}

	// 应用替换规则
	name := f.Name
	if len(replaceRules) > 0 {
		name = applyReplaceRules(name, replaceRules)
	}
	// SHA1 去重移到 TMDB 识别后（需要 media 信息来计算目标目录）
	onLog(fmt.Sprintf("▶ 开始识别: %s", shortLogName(f.Name)))
	parsed := parseFileName(name)
	oldBase := baseName(f.Name)
	// 人工确认：同前缀的其他集一起挂在这条待确认记录上，确认时一并入库
	holdFiles := func() []orgRecordFile {
		out := append([]orgRecordFile(nil), self...)
		for _, sib := range seriesSiblings(ctx, f) {
			out = append(out, orgRecordFile{Fid: sib.Fid, Name: sib.Name, Kind: recordFileKind(sib.Name),
				PickCode: sib.PickCode, Size: sib.Size, Sha1: sib.Sha1})
		}
		return out
	}
	var media *TmdbMedia
	if ctx.forced != nil {
		media = ctx.forced
		onLog(fmt.Sprintf("✦ 人工确认: %s → %s (%s)", shortLogName(f.Name), media.Title, media.Year))
	} else {
		if parsed.Title == "" && parsed.TmdbID == 0 {
			if ctx.holdable() {
				return ctx.holdForConfirm(f.Name, f.Fid, "file", nil, parsed, f.Name, holdFiles(),
					"文件名提取不出片名，请手动指定 TMDB 条目"), nil
			}
			// 无法识别，按原文件的剧集前缀归档（附件随行，避免字幕变孤儿）
			holdingDir := sourceHoldingDir(f.Name, "")
			holdingCid, err := moveToHoldingDir(ops, cfg.Redundant, holdingDir, []string{f.Fid})
			if err != nil {
				result.Status = "failed"
				result.Message = "移到冗余失败: " + err.Error()
				onLog(fmt.Sprintf("✗ %s - %s", f.Name, result.Message))
				return fail("failed", "move", result.Message)
			}
			appendSelf(moveSiblingAttachments(ops, cfg.Pending, oldBase, "", holdingCid, false, onLog))
			result.Status = "failed"
			result.Message = "无法提取标题，已移到 冗余/" + holdingDir
			onLog(fmt.Sprintf("✗ %s - 无法提取标题，已移到 冗余/%s", f.Name, holdingDir))
			return fail("unrecognized", "recognize", result.Message)
		}

		// TMDB 识别
		var err error
		media, err = tc.recognize(parsed)
		if err != nil {
			// 瞬时错误（网络/限流）：留在待整理目录，下轮重试（移冗余会误分流好内容）
			result.Status = "failed"
			result.Message = "TMDB 暂时不可达，留在待整理: " + err.Error()
			onLog(fmt.Sprintf("○ %s - TMDB 暂时不可达（%v），留在待整理目录下轮重试", f.Name, err))
			return fail("failed", "recognize", "TMDB 暂时不可达，留在待整理目录下轮重试: "+err.Error())
		}
		if media == nil {
			if ctx.holdable() {
				return ctx.holdForConfirm(f.Name, f.Fid, "file", nil, parsed, f.Name, holdFiles(),
					"TMDB 未找到匹配条目，请手动指定"), nil
			}
			holdingDir := sourceHoldingDir(f.Name, parsed.Title)
			holdingCid, moveErr := moveToHoldingDir(ops, cfg.Redundant, holdingDir, []string{f.Fid})
			if moveErr != nil {
				result.Status = "failed"
				result.Message = "移到冗余失败: " + moveErr.Error()
				onLog(fmt.Sprintf("✗ %s - %s", f.Name, result.Message))
				return fail("failed", "move", result.Message)
			}
			appendSelf(moveSiblingAttachments(ops, cfg.Pending, oldBase, "", holdingCid, false, onLog))
			result.Status = "failed"
			result.Message = "TMDB 未找到匹配，已移到 冗余/" + holdingDir
			onLog(fmt.Sprintf("✗ %s - TMDB 未找到匹配，已移到 冗余/%s", f.Name, holdingDir))
			return fail("unrecognized", "recognize", result.Message)
		}

		onLog(fmt.Sprintf("✦ 识别成功: %s → %s (%s)", shortLogName(f.Name), media.Title, media.Year))
		if ctx.holdable() {
			return ctx.holdForConfirm(f.Name, f.Fid, "file", media, parsed, f.Name, holdFiles(), ""), nil
		}
	}

	result.TmdbID = media.TmdbID
	result.Title = media.Title
	result.Year = media.Year
	result.MediaType = media.MediaType
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, f.Name)
	targetDir := libSubPath(categoryDir(media.MediaType, category), pathDir(newPath))
	holdingDir := recognizedHoldingDir(media, parsed, f.Name)
	if holdingDir == "" {
		holdingDir = sourceHoldingDir(f.Name, parsed.Title)
	}

	// 直接查网盘去重（不依赖本地缓存表）
	if checkByCloudSHA1(ops, media, cfg, libAbs, f.Sha1, parsed, f.Name) {
		holdingCid, err := moveToHoldingDir(ops, cfg.Existing, holdingDir, []string{f.Fid})
		if err != nil {
			result.Status = "failed"
			result.Message = "移到已存在失败: " + err.Error()
			return fail("failed", "move", result.Message)
		}
		appendSelf(moveSiblingAttachments(ops, cfg.Pending, oldBase, "", holdingCid, false, onLog))
		result.Status = "exists"
		result.Message = fmt.Sprintf("已存在: %s (%s)，已移到 已存在/%s", media.Title, media.Year, holdingDir)
		onLog(fmt.Sprintf("○ %s → 已存在: %s (%s) → 已存在/%s", f.Name, media.Title, media.Year, holdingDir))
		ctx.sink.note(&model.OrganizeRecord{
			Source: f.Name, SourceFid: f.Fid, SourceKind: "file",
			Status: "exists", Message: result.Message,
			TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
			MediaType: media.MediaType, PosterPath: media.PosterPath,
			Category: category, TargetDir: targetDir,
			Files: marshalRecordFiles(self),
		})
		return result, nil
	}

	// 洗版判定（此前只有目录条目走，待整理目录里是散文件时整段被跳过）
	switch decision := tryWashReplace(ops, cfg, media, f.Name, f.Sha1, targetDir, onLog); decision {
	case washFailed:
		result.Status, result.Message = "failed", "洗版旧版让位失败"
		return fail("failed", "move", result.Message)
	case washReplaced:
		// 旧版已让位，落入下方正常入库
	case washNotBetter, washSameFile:
		holdingCid, err := moveToHoldingDir(ops, cfg.Existing, holdingDir, []string{f.Fid})
		if err != nil {
			result.Status, result.Message = "failed", "移到已存在失败: "+err.Error()
			return fail("failed", "move", result.Message)
		}
		appendSelf(moveSiblingAttachments(ops, cfg.Pending, oldBase, "", holdingCid, false, onLog))
		msg := washExistsMsg(decision) + "，已移到 已存在/" + holdingDir
		result.Status, result.Message = "exists", msg
		onLog(fmt.Sprintf("○ %s - %s", f.Name, msg))
		ctx.sink.note(&model.OrganizeRecord{
			Source: f.Name, SourceFid: f.Fid, SourceKind: "file",
			Status: "exists", Message: msg,
			TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
			MediaType: media.MediaType, PosterPath: media.PosterPath,
			Category: category, TargetDir: targetDir,
			Files: marshalRecordFiles(self),
		})
		return result, nil
	}

	rootRel := libSubPath(categoryDir(media.MediaType, category), strings.SplitN(newPath, "/", 2)[0])

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 创建目录失败: %v", f.Name, err))
		return fail("failed", "move", "创建目标目录失败: "+err.Error())
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动文件失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", f.Name, err))
		return fail("failed", "move", "移动文件到媒体库失败: "+err.Error())
	}
	// 视频本体重命名为标准名
	finalName := f.Name
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
			finalName = stdName
		}
	}

	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", category+"/"+strings.Split(newPath, "/")[0]))
	// 成功入库：附件随行并按视频新名重命名字幕（播放器按视频名匹配外挂字幕）
	newBase := baseName(pathBase(newPath))
	attachments := moveSiblingAttachments(ops, cfg.Pending, oldBase, newBase, targetCid, true, onLog)

	// 一条龙落盘
	video := f
	video.Name = finalName
	strmCreated, _ := ctx.sink.commit(ops, media, rootRel, targetDir, []remoteFile{video}, attachments)
	onLog(fmt.Sprintf("✓ %s - 落盘完成：STRM %d 个、附属 %d 个 → %s", f.Name, strmCreated, len(attachments),
		filepath.Join(ctx.sink.localRoot, filepath.FromSlash(ctx.sink.libRel(targetDir)))))

	recFiles := []orgRecordFile{{Fid: f.Fid, Name: finalName, Orig: recordOrig(f.Name, finalName),
		Kind: "video", PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1}}
	for _, a := range attachments {
		recFiles = append(recFiles, orgRecordFile{Fid: a.Fid, Name: a.Name, Kind: recordFileKind(a.Name), PickCode: a.PickCode, Sha1: a.Sha1})
	}
	rec := &model.OrganizeRecord{
		Source: f.Name, SourceFid: f.Fid, SourceKind: "file",
		Status: "success", Message: "→ " + targetDir,
		TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
		MediaType: media.MediaType, PosterPath: media.PosterPath,
		Category: category, TargetDir: rootRel, TargetCid: targetCid,
		Files: marshalRecordFiles(recFiles), VideoCount: 1,
		TotalSize: f.Size, StrmCreated: strmCreated,
	}
	ctx.sink.note(rec)

	recordMedia(media, category, targetDir+"/"+pathBase(newPath))
	// 入库卡片：散文件这条线此前一张都不发，同样一次入库，用户收不收得到
	// 通知全看内容是目录还是单文件
	notifyMediaStoredFull(media, category, []remoteFile{video}, finalName, 1+len(attachments), f.Size)
	result.Category = category
	result.TargetDir = targetDir
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s/%s] → %s", media.Title, media.Year, category, media.MediaType, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s (%s) [%s/%s] → %s", f.Name, media.Title, media.Year, category, media.MediaType, targetDir))

	return result, rec
}

// pathDir 取路径中的目录部分（最后一个 / 之前）
func pathDir(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[:idx]
	}
	return ""
}

// pathBase 取路径中的文件名部分（最后一个 / 之后）
func pathBase(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// isEpisodeOnly 判断标题是否只是集数/编号（如 "ep01"、"E05"、"01"）
func isEpisodeOnly(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return true
	}
	// ep01, e01, 01, 1
	cleaned := strings.TrimPrefix(strings.TrimPrefix(t, "ep"), "e")
	return isAllDigits(cleaned) && len(cleaned) <= 4
}

// modelSettingValue 读配置值：YAML 优先，DB 回退（配置由前端 SaveSetting 写入 YAML，
// 旧的仅读 DB 写法会永远读到空——替换规则/重命名模板等在整理执行时全部失效）
func modelSettingValue(key string) string {
	return settingValueCompat(key)
}

// executeOrganizeWithConfig 用指定的 OrgConfig 执行整理（转存目录等场景）。
// 与 executeOrganize 一样自带落盘：引擎跑完统一刮削 + 刷 Emby
func (h *Handler) executeOrganizeWithConfig(cfg *OrgConfig) (stepsOut []gin.H, detailsOut []OrganizeResult, runErr error) {
	defer func() { failTask(runErr) }()
	if _, err := loadTmdbClient(); err != nil {
		return nil, nil, fmt.Errorf("未执行：%w", err)
	}
	orgStart := time.Now()
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, nil, err
	}
	ops.suppress = true

	sink := h.newOrgSink(cfg.Library)
	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngineWithConfig(ops, cfg, sink, logFn)
	sink.flushScrape()
	sink.flushRefresh()
	// 收尾与 executeOrganize 共用：同一件事不该因为触发来源不同而汇总不同
	finishOrganize(sink, orgResults, orgStart)

	totalFiles := len(orgResults)
	existsCount, failedCount := 0, 0
	for _, r := range orgResults {
		if r.Status == "exists" {
			existsCount++
		}
		if r.Status == "failed" {
			failedCount++
		}
	}
	steps := []gin.H{{"step": "整理（转存目录）", "status": "完成",
		"message": fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)}}

	return steps, orgResults, nil
}

// runOrganizeEngineWithConfig 用指定的 OrgConfig 运行整理引擎
func runOrganizeEngineWithConfig(ops *pan115Ops, cfg *OrgConfig, sink *orgSink, onLog func(string)) ([]OrganizeResult, int) {
	results := []OrganizeResult{}
	successCount := 0

	tc, err := loadTmdbClient()
	if err != nil {
		onLog("✗ TMDB 配置错误: " + err.Error())
		return results, 0
	}

	replaceRules := loadReplaceRules()

	// 重命名模板必须先就绪（转存触发路径此前漏了这一步 → renameBeforeMove nil panic）
	ensureRenameTpl()

	// 计算库根绝对路径（去重记录网盘验证用，不能传空否则验证被跳过）
	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历转存目录失败: " + err.Error())
		return results, 0
	}
	if len(topEntries) == 0 {
		return results, 0 // 空转静默：库根路径等开销留到确认有活干之后
	}

	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library)
	}
	ctx := &orgCtx{ops: ops, cfg: cfg, tc: tc, rules: replaceRules, libAbs: libAbs, sink: sink,
		pruner: newDirPruner(ops, orgProtectedCids(cfg), onLog), onLog: onLog,
		held: loadAwaiting(), handled: map[string]bool{}}

	// 五个工作区根目录自身永不被当作条目处理（与 runOrganizeEngine 一致）
	excluded := map[string]bool{cfg.Library: true, cfg.Existing: true, cfg.Redundant: true, cfg.Pending: true}
	if cfg.ShareCid != "" && cfg.ShareCid != cfg.Pending {
		excluded[cfg.ShareCid] = true
	}
	filtered := topEntries[:0]
	for _, e := range topEntries {
		if e.IsDir && excluded[e.Cid] {
			continue
		}
		filtered = append(filtered, e)
	}
	topEntries = ctx.dropHeld(filtered)
	if len(topEntries) == 0 {
		return results, 0 // 只剩等待人工确认的条目：静默，记录页里看得到
	}

	guards := newOrgGuards(ops.cookie, cfg.Pending, cfg)
	if guards.active {
		onLog("⚠ 扫描根覆盖到媒体库/已存在/冗余目录，这些子树内的条目将被跳过（防误整理库内容）")
	}

	onLog(fmt.Sprintf("▶ 转存目录发现 %d 个条目，开始整理...", len(topEntries)))
	for i, entry := range topEntries {
		SetTaskProgress(fmt.Sprintf("整理 %d/%d：%s", i+1, len(topEntries), truncateStr(entry.Name, 40)))
		results = append(results, processEntry(ctx, guards, entry, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}
	ctx.pruner.flush()
	SetTaskProgress("")
	return results, successCount
}

// shortLogName 日志用短名：剥掉发布站广告前缀（【…】块/域名@），超长截断。
// 纯粹为了日志可读——原始名在网盘里保持不变
func shortLogName(s string) string {
	s = reAdBracket.ReplaceAllString(s, "")
	s = reAdDomainTail.ReplaceAllString(s, "")
	s = strings.Trim(s, " .@")
	if r := []rune(s); len(r) > 46 {
		s = string(r[:46]) + "…"
	}
	return s
}

// sanitizeReleaseFilename 清洗文件名中的发布站广告前缀
// "4k688.com@START-622.mp4" → "START-622.mp4"
// "www.xxx.com@MIDV-001.mp4" → "MIDV-001.mp4"
func sanitizeReleaseFilename(name string) string {
	// 保留扩展名，只清洗基名
	ext := pathExt(name)
	base := baseName(name)

	// ===== 1. 网站域名类前缀 =====
	// "4k688.com@XXX-001" / "www.xxx.com@XXX-001" / "xxx.com-XXX-001"
	// @ 分隔符：取 @ 之后
	if idx := strings.LastIndex(base, "@"); idx >= 0 && idx < len(base)/2 {
		base = base[idx+1:]
	}
	// www.xxx.com 前缀
	base = reAdWwwPrefix.ReplaceAllString(base, "")
	// 纯域名前缀（4k688.com、avxxx.net 等）
	base = reAdDomPrefix.ReplaceAllString(base, "")

	// ===== 2. 括号类广告 =====
	// 【高清xxx网】【广告】等全角括号
	base = reAdBracket.ReplaceAllString(base, "")
	// (www.xxx.com) [4k688.com] 等半角括号（只清开头/结尾的，不清中间的标签）
	base = reAdParenHead.ReplaceAllString(base, "")
	base = reAdSquareHead.ReplaceAllString(base, "")

	// ===== 3. 文字类广告前缀/后缀 =====
	// "高清剧集网"、"破解版"、"完整版"、"中文字幕" 等常见前缀
	adPrefixes := []string{
		"高清剧集网", "高清网站", "最新地址", "永久地址", "官方网址",
		"破解版", "完整版", "无修正版", "高清版", "无码版", "有码版",
		"中文字幕", "无码破解", "字幕版", "4K修复版",
	}
	for _, ad := range adPrefixes {
		if strings.HasPrefix(base, ad) {
			base = strings.TrimPrefix(base, ad)
			// 清除前缀后面的分隔符
			base = strings.TrimLeft(base, "-_. ")
		}
	}

	// ===== 4. URL 参数类后缀 =====
	// "XXX-001?from=4k688" / "XXX-001 - www.4k688.com"
	base = reAdUrlTail.ReplaceAllString(base, "")
	base = reAdQueryTail.ReplaceAllString(base, "")

	// ===== 5. 多余分隔符 =====
	base = strings.Trim(base, "-_. ")
	for strings.Contains(base, "  ") {
		base = strings.ReplaceAll(base, "  ", " ")
	}
	base = strings.Trim(base, "-_. ")

	return base + ext
}

// 广告清洗与日志短名共享预编译正则，避免逐文件重复编译。
var (
	reAdBracket    = regexp.MustCompile(`【[^】]*】`)
	reAdDomainTail = regexp.MustCompile(`(?i)[a-z0-9.-]+\.[a-z]{2,}@`)
	reAdWwwPrefix  = regexp.MustCompile(`(?i)^www\.[a-z0-9.-]+\.(com|net|org|cc|xyz|me|tv|info)[-_.@]?`)
	reAdDomPrefix  = regexp.MustCompile(`(?i)^[a-z0-9]{2,15}\.(com|net|org|cc|xyz|me|tv|info)[-_.@]?`)
	reAdParenHead  = regexp.MustCompile(`(?i)^\(\s*(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz)\s*\)[-_. ]*`)
	reAdSquareHead = regexp.MustCompile(`(?i)^\[\s*(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz)\s*\][-_. ]*`)
	reAdUrlTail    = regexp.MustCompile(`[-_ ]+(www\.)?[a-z0-9.-]+\.(com|net|cc|xyz|me|tv)$`)
	reAdQueryTail  = regexp.MustCompile(`\?[a-z=&0-9]+$`)
)

// adDomainRegex 广告域名（清洗后仍任意位置出现即视为广告载体）
var adDomainRegex = regexp.MustCompile(`(?i)(?:https?://|www\.)?[a-z0-9][a-z0-9-]{1,15}\.(?:com|net|org|cc|xyz|me|tv|info|vip|top|app|club|site|online|icu|fun|win)\b`)

// adKeywords 广告文件常见关键词
var adKeywords = []string{
	"18+", "游戏大全", "最新地址", "永久地址", "永久导航", "网址导航", "导航网",
	"发布页", "发布器", "天天更新", "每周更新", "免费观看", "在线观看", "手机看片",
	"福利网", "福利社", "破解版", "高清资源网", "资源网", "看片网", "影片网",
	"电影网", "安卓版", "app版", "app下载", "磁力搜索", "同城约",
}

// containsAdKeyword 广告词检测（大小写不敏感）
func containsAdKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range adKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// isAdOnlyVideo 判断视频文件是否为纯广告/引流载体：
// 清洗发布站广告（【…】块/域名）后连片名都没剩下（文件名整体是广告），
// 或清洗后仍残留域名/广告词。"【更多无水印蓝光原盘请访问 www.BBQDDQ.com】.MP4"
// 清洗后为空 → 广告；"骗不了人的男人.Softie...mkv" 清洗后保留完整片名 → 正片
//
// 站点水印前缀 ≠ 广告本体："4k688.com@START-635.mp4" 开头的 4k688.com 只是
// 发布站水印（sanitizeReleaseFilename 能剥掉）。剥掉水印后剩干净片名的算
// 正片；剥不掉（域名在中间）或剥完剩广告词的（"18+游戏大全(996gg.cc)-…"、
// "4k688.com@免费观看…"）才是真广告
func isAdOnlyVideo(name string) bool {
	raw := baseName(name) // 无扩展名的基名
	cleaned := strings.TrimSpace(stripReleaseAds(raw))
	if cleaned == "" {
		return true
	}
	// 水印前缀判定必须喂完整文件名：sanitizeReleaseFilename 依赖 pathExt 找到真
	// 扩展名，传无扩展名基名时 "xxx.com@片名" 的域名会被误当扩展名保留
	trimmed := strings.TrimSpace(baseName(sanitizeReleaseFilename(name)))
	if t := strings.Trim(trimmed, ".-_ "); t != "" && trimmed != raw &&
		!adDomainRegex.MatchString(t) && !containsAdKeyword(t) {
		return false // 水印前缀剥掉后是干净片名 → 正片
	}
	return adDomainRegex.MatchString(cleaned) || containsAdKeyword(cleaned)
}

// tmdbImageBase TMDB 图片地址（读取 TMDB 配置卡保存的数据库配置，默认官方）
func tmdbImageBase() string {
	var cfg model.TmdbConfig
	if model.DB != nil && model.DB.First(&cfg).Error == nil && cfg.ImageApiUrl != "" {
		return strings.TrimRight(cfg.ImageApiUrl, "/")
	}
	return "https://image.tmdb.org"
}

// humanSizeBytes 字节数转可读大小
func humanSizeBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20: // MB 段
		return fmt.Sprintf("%d MB", n/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n/1024)
	}
}

// episodeRangeStr 集数区间格式化：S01E01-E12 / S01E01-E03,E05,E07-E09
func episodeRangeStr(videoFiles []remoteFile) string {
	type epRec struct{ season, ep int }
	var eps []epRec
	for _, vf := range videoFiles {
		p := parseFileName(vf.Name)
		if p.Episode > 0 {
			s := p.Season
			if s == 0 {
				s = 1
			}
			eps = append(eps, epRec{s, p.Episode})
		}
	}
	if len(eps) == 0 {
		return ""
	}
	// 按季分组
	bySeason := map[int][]int{}
	for _, e := range eps {
		bySeason[e.season] = append(bySeason[e.season], e.ep)
	}
	parts := []string{}
	for se := 1; se <= 99; se++ {
		list, ok := bySeason[se]
		if !ok {
			continue
		}
		sort.Ints(list)
		// 连续段合并
		var segs [][2]int
		start, prev := list[0], list[0]
		for _, e := range list[1:] {
			if e == prev+1 {
				prev = e
				continue
			}
			segs = append(segs, [2]int{start, prev})
			start, prev = e, e
		}
		segs = append(segs, [2]int{start, prev})
		segStrs := make([]string, 0, len(segs))
		for _, sg := range segs {
			if sg[0] == sg[1] {
				segStrs = append(segStrs, fmt.Sprintf("E%02d", sg[0]))
			} else {
				segStrs = append(segStrs, fmt.Sprintf("E%02d-E%02d", sg[0], sg[1]))
			}
		}
		parts = append(parts, fmt.Sprintf("S%02d%s", se, strings.Join(segStrs, ",")))
	}
	return strings.Join(parts, " ")
}

// episodeRangeWithMissing 集数区间 + 缺集描述。
// 缺集 = TMDB 该季总集数范围内未入库的集；无法取到总集数时只返回区间。
func episodeRangeWithMissing(videoFiles []remoteFile, media *TmdbMedia) (string, string) {
	rng := episodeRangeStr(videoFiles)
	if rng == "" || media == nil || media.MediaType != "tv" || media.TmdbID == 0 {
		return rng, ""
	}
	// 解析出各集（复用分组解析）
	type epRec struct{ season, ep int }
	var eps []epRec
	for _, vf := range videoFiles {
		p := parseFileName(vf.Name)
		if p.Episode > 0 {
			s := p.Season
			if s == 0 {
				s = 1
			}
			if media.SeasonNum > 0 {
				s = media.SeasonNum
			}
			eps = append(eps, epRec{s, p.Episode})
		}
	}
	if len(eps) == 0 {
		return rng, ""
	}
	season := eps[0].season
	total := 0
	if tc, err := loadTmdbClient(); err == nil {
		total = tc.SeasonEpisodeCount(media.TmdbID, season)
	}
	if total <= 0 {
		return rng, ""
	}
	have := map[int]bool{}
	maxEp := 0
	for _, e := range eps {
		have[e.ep] = true
		if e.ep > maxEp {
			maxEp = e.ep
		}
	}
	// 缺集 = 1..total 中没有的
	var missing []int
	for i := 1; i <= total; i++ {
		if !have[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return rng, ""
	}
	// 缺集描述：合并连续段；段太多时只报数量
	var segs [][2]int
	start, prev := missing[0], missing[0]
	for _, m := range missing[1:] {
		if m == prev+1 {
			prev = m
			continue
		}
		segs = append(segs, [2]int{start, prev})
		start, prev = m, m
	}
	segs = append(segs, [2]int{start, prev})
	if len(segs) > 4 {
		return rng, fmt.Sprintf("%d 集", len(missing))
	}
	parts := make([]string, 0, len(segs))
	for _, sg := range segs {
		if sg[0] == sg[1] {
			parts = append(parts, fmt.Sprintf("E%02d", sg[0]))
		} else {
			parts = append(parts, fmt.Sprintf("E%02d-E%02d", sg[0], sg[1]))
		}
	}
	return rng, strings.Join(parts, ",")
}

// notifyMediaStoredFull 整理入库 → 入库卡片（TMDB 封面 + 画质/文件数/集数）。
// Emby 扫描完成后 webhook 那条会并进同一张卡片，不会各推一条
func notifyMediaStoredFull(media *TmdbMedia, category string, videoFiles []remoteFile, mainVideoName string, movedCount int, movedBytes int64) {
	if media == nil {
		return
	}
	typeLabel := "电影"
	if media.MediaType == "tv" {
		typeLabel = "剧集"
	}
	entry := mediaNotifEntry{
		Title: media.Title, Year: media.Year, Kind: typeLabel, Source: "organize",
		Category: category, Quality: qualityLabel(mainVideoName), Rating: media.VoteAverage,
	}
	if movedCount > 0 {
		entry.Files = fmt.Sprintf("%d 个文件 · %s", movedCount, humanSizeBytes(movedBytes))
	}
	if ep, miss := episodeRangeWithMissing(videoFiles, media); ep != "" {
		entry.Episodes = ep + "（全）"
		if miss != "" {
			entry.Episodes = ep + "（缺 " + miss + "）"
		}
	}
	// 洗版替换的说明挂到这部片的卡片上，不再单独推一条
	entry.Notes = washNotesFor(entry.mergeKey())
	if media.TmdbID != 0 && media.PosterPath != "" {
		entry.PosterURL = tmdbImageBase() + "/t/p/w500" + media.PosterPath
		if media.MediaType == "tv" {
			entry.Link = fmt.Sprintf("https://www.themoviedb.org/tv/%d", media.TmdbID)
		} else {
			entry.Link = fmt.Sprintf("https://www.themoviedb.org/movie/%d", media.TmdbID)
		}
	}
	QueueMediaNotif(entry)
}

// qualityLabel 从文件名里提出给人看的画质标签：分辨率 + 特效 + 来源。
// 一个都认不出来就返回空，卡片上宁可不显示这一行
func qualityLabel(name string) string {
	if name == "" {
		return ""
	}
	ri := ParseResourceInfo(name)
	parts := make([]string, 0, 3)
	for _, v := range []string{ri.Pix, ri.Effect, ri.Type} {
		if v != "" {
			parts = append(parts, strings.ToUpper(v))
		}
	}
	return strings.Join(parts, " ")
}
