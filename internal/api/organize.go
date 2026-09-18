package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"

	"github.com/mozillazg/go-pinyin"
	"gopkg.in/yaml.v3"
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
	Pending      string `json:"pending"`   // 待整理目录 cid
	Library      string `json:"library"`   // 我的影视库 cid（整理后最终归宿）
	Existing     string `json:"existing"`  // 已存在目录 cid（洗版重复）
	Redundant    string `json:"redundant"` // 冗余目录 cid（识别失败等）
	ReplaceRules string `json:"replace_rules"`
	MinSize      int64  `json:"min_size"`
	ShareCid     string `json:"-"` // 转存目录 cid（loadOrgConfig 注入；同为工作区根，绝不被当条目处理）
}

// renameTpl 全局重命名模板（ensureRenameTpl 加载，两条整理引擎入口都会调用）
var renameTpl *RenameConfig

// ensureRenameTpl 加载重命名模板：用户配置（org-rename）优先，缺失或解析失败
// 回退默认模板。此前只在 runOrganizeEngine 里初始化，转存触发的
// runOrganizeEngineWithConfig 不经过它 → renameBeforeMove 解引用 nil panic
func ensureRenameTpl() {
	if v := modelSettingValue("org-rename"); v != "" {
		var saved RenameConfig
		if json.Unmarshal([]byte(v), &saved) == nil {
			renameTpl = &saved
			return
		}
	}
	renameTpl = &RenameConfig{
		MovieFolder: "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		MovieFile:   "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
		TVFolder:    "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		TVFile:      "{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
		// AV 命名规范 = 番号 + AV 标题（如 "ABC-123 XXXXXX"），不带画质等附加信息；
		// 未配置 MetaTube 或识别不到时 <> 块整体省略，退回纯番号
		AVFolder: "{first_letter}-{num}",
		AVFile:   "{num}< {av_title}>{ext}",
	}
}

// RenameConfig 重命名配置
type RenameConfig struct {
	MovieFolder string `json:"movie_folder"` // 电影文件夹命名规则
	MovieFile   string `json:"movie_file"`   // 电影文件命名规则
	TVFolder    string `json:"tv_folder"`    // 电视剧文件夹命名规则
	TVFile      string `json:"tv_file"`      // 电视剧文件命名规则
	AVFolder    string `json:"av_folder"`    // AV 文件夹命名规则
	AVFile      string `json:"av_file"`      // AV 文件命名规则
}

// loadOrgConfig 从数据库加载整理配置
// loadOrgConfig 加载整理配置（yaml 优先 DB 回退）；
// 影视库不再单独配置，直接使用全量同步配置的媒体库 cid
func (h *Handler) loadOrgConfig() (*OrgConfig, error) {
	var cfg OrgConfig
	if v := h.getSettingValue("org-basic"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
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
		return nil, fmt.Errorf("未配置全量同步的媒体库目录（整理目标库取自全量同步配置）")
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
// 并把与视频同名的字幕重命名为视频新名（播放器按视频名匹配外挂字幕）
func moveSiblingAttachments(ops *pan115Ops, pendingCid, videoOldBase, videoNewBase, targetCid string, rename bool, onLog func(string)) {
	if pendingCid == "" {
		return
	}
	entries, _, err := ops.listEntries(pendingCid, 0)
	if err != nil {
		onLog(fmt.Sprintf("✗ 收集随行附件失败: %v", err))
		return
	}
	moved := 0
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
		// 重命名对齐视频新名（仅成功入库场景；冗余/已存在保持原名；
		// 标准元数据命名保持固定名）
		if rename && !metaFixed && videoNewBase != "" && base != videoNewBase {
			newName := videoNewBase + strings.TrimPrefix(base, videoOldBase) + ext
			if err := ops.rename(fid, newName); err != nil {
				onLog(fmt.Sprintf("○ 附件随行 %s（重命名失败保持原名: %v）", name, err))
			} else {
				onLog(fmt.Sprintf("✓ 附件随行 %s → %s", name, newName))
			}
		} else {
			onLog(fmt.Sprintf("✓ 附件随行 %s", name))
		}
	}
	if moved > 0 {
		time.Sleep(300 * time.Millisecond)
	}
}

// renameBeforeMove 在源目录中先重命名文件（带画质信息），再移动到目标。
// 全目录的重命名（视频+字幕）合并为一次 batch_rename 调用：
// 逐个调用时每个文件过一遍 API 限流，24 集仅等待就要 70+ 秒
func renameBeforeMove(ops *pan115Ops, media *TmdbMedia, videoFiles, files []remoteFile, enrichRenames map[string]string, onLog func(string)) {
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
		return
	}
	if err := ops.renameBatch(names); err != nil {
		onLog(fmt.Sprintf("✗ 批量重命名失败（%d 个文件保持原名）: %v", len(names), err))
		return
	}
	onLog(fmt.Sprintf("✓ 批量重命名 %d 个文件（例: %s）", len(names), example))
}

// moveQuietly 移动并记录失败（失败不再被吞掉）
func moveQuietly(ops *pan115Ops, targetCid string, fids []string, label string, onLog func(string)) {
	if err := ops.moveFiles(targetCid, fids); err != nil {
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", label, err))
	}
}

// loadReplaceRules 加载替换规则（YAML 优先，DB 回退）
func loadReplaceRules() []ReplaceRule {
	var cfg struct {
		ReplaceRules string `json:"replace_rules"`
	}
	json.Unmarshal([]byte(settingValueCompat("org-recognize")), &cfg)
	if cfg.ReplaceRules == "" {
		return nil
	}
	var rules []ReplaceRule
	json.Unmarshal([]byte(cfg.ReplaceRules), &rules)
	return rules
}

// ReplaceRule 替换规则
type ReplaceRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// applyReplaceRules 应用替换规则到文件名
func applyReplaceRules(name string, rules []ReplaceRule) string {
	for _, r := range rules {
		if r.From != "" {
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
		return "AV"
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

// classifyMedia 根据分类规则判断二级分类
func classifyMedia(media *TmdbMedia) string {
	// 电影和电视剧分开查询
	mediaType := "movie"
	if media.MediaType == "tv" {
		mediaType = "tv"
	}
	categories := classifyRules(mediaType)

	for _, cat := range categories {
		if matchCategory(&cat, media) {
			return cat.Name
		}
	}

	// 查找默认分类
	for _, cat := range categories {
		if cat.IsDefault {
			return cat.Name
		}
	}

	return "未分类"
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
	var path string
	switch media.MediaType {
	case "movie":
		path = ctx.ApplyTemplate(renameTpl.MovieFolder) + "/" + ctx.ApplyTemplate(renameTpl.MovieFile)
	case "tv":
		path = ctx.ApplyTemplate(renameTpl.TVFolder) + "/" + ctx.ApplyTemplate(renameTpl.TVFile)
	default:
		path = ctx.ApplyTemplate(renameTpl.AVFolder) + "/" + ctx.ApplyTemplate(renameTpl.AVFile)
		path = collapseDuplicateAVNum(path, media.Title)
	}
	// 剧集需要插入 Season 目录（如果模板没有包含）
	if media.MediaType == "tv" && parsed.Season > 0 && !strings.Contains(path, "Season") {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			path = parts[0] + "/" + fmt.Sprintf("Season %02d", parsed.Season) + "/" + parts[1]
		}
	}
	return sanitizePath(path)
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
		mediaTypeCategory(media.MediaType) + "/" + classifyMedia(media) + "/" + folderName

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
			folderTpl = renameTpl.AVFolder
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

// sha1ExistsInLibrary 文件 sha1 是否已存在于媒体库台账（同一文件不同命名也能识别）
func sha1ExistsInLibrary(sha1 string) bool {
	if sha1 == "" {
		return false
	}
	var count int64
	model.DB.Model(&model.SyncedFile{}).Where("sha1 = ? AND sha1 != ''", sha1).Count(&count)
	return count > 0
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

// recordMedia 记录已整理的媒体到数据库
// recordMedia 落整理记录（洗版查记录用）。source：空=115，cd2=CloudDrive2，
// 同 tmdb_id 各来源独立一条，互不覆盖
func recordMedia(media *TmdbMedia, category, targetPath, source string) {
	// 同一部影视同来源只留一条记录（重复整理时更新而非新增）
	var existing model.MediaLibrary
	if model.DB.Where("tmdb_id = ? AND media_type = ? AND source = ?", media.TmdbID, media.MediaType, source).First(&existing).Error == nil {
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
		Source:        source,
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
	Fid   string // 文件 id
	Name  string // 文件名
	IsDir bool   // 是否文件夹
	Size  int64  // 文件大小
	Cid   string // 子目录 cid（仅文件夹有）
	Sha1  string // 文件 sha1（散文件去重用）
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

func (g *orgGuards) absOf(cid string) string {
	if a, ok := g.absCache[cid]; ok {
		return a
	}
	a := absPathOf(g.cookie, cid, g.memo)
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
func runOrganizeEngine(ops *pan115Ops, cfg *OrgConfig, onLog func(string)) ([]OrganizeResult, int) {
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

	// 库根绝对路径（去重记录的网盘验证用；OpenAPI 通道取不到则跳过验证）
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library, map[string]dirInfo{})
	}

	// 获取待整理目录下的顶层条目
	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历待整理目录失败: " + err.Error())
		return results, 0
	}

	if len(topEntries) == 0 {
		onLog("○ 待整理目录为空")
		return results, 0
	}

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
	topEntries = filtered
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
		results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, entry, libAbs, onLog, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}
	SetTaskProgress("")

	return results, successCount
}

// processEntry 处理一个顶层条目：
//   - 目录不含直接视频文件但含子目录 → 容器目录（如用户把多部剧放进同一文件夹），
//     递归处理每个子目录（每部剧独立识别入库），容器自身最后移到冗余
//   - 其余目录 → 单部影视目录
//   - 文件 → 散视频
func processEntry(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, guards *orgGuards, entry dirEntry, libAbs string, onLog func(string), depth int, successCount *int) []OrganizeResult {
	results := []OrganizeResult{}
	// 库子树防护：目录传自身 cid，文件传父目录 cid；命中保护子树直接放行不处理
	if guards.skip(entry.Cid) {
		onLog(fmt.Sprintf("○ 跳过媒体库/工作区内条目: %s（整理不处理库内内容）", entry.Name))
		return results
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
		f := remoteFile{Fid: entry.Fid, Name: entry.Name, Size: entry.Size, Sha1: entry.Sha1}
		result := processSingleFileWithSiblings(ops, cfg, tc, replaceRules, f, libAbs, onLog)
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
				results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, child, libAbs, onLog, depth+1, successCount)...)
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
				if err := ops.moveFiles(cfg.Redundant, []string{entry.Fid}); err != nil {
					onLog(fmt.Sprintf("○ %s/ - 空容器目录移到冗余失败: %v", entry.Name, err))
				} else {
					onLog(fmt.Sprintf("○ %s/ - 空容器目录已移到冗余", entry.Name))
				}
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
		return results
	}
	if len(subFiles) == 0 {
		// 空目录（离线/转存产生的空壳）清进冗余：不处理会导致守望者
		// 反复"整理后转存目录仍有内容"且无任何日志可查
		if err := ops.moveFiles(cfg.Redundant, []string{entry.Fid}); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 空目录移到冗余失败: %v", entry.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - 空目录（无任何文件），已移到冗余", entry.Name))
		}
		return results
	}
	result := processDir(ops, cfg, tc, replaceRules, entry, subFiles, libAbs, onLog)
	results = append(results, result...)
	for _, r := range result {
		if r.Status == "success" {
			*successCount++
		}
	}
	return results
}

// processDir 处理一个子目录（包含多个文件的影视目录）
func processDir(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, dir dirEntry, files []remoteFile, libAbs string, onLog func(string)) []OrganizeResult {
	var results []OrganizeResult

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
	onLog(fmt.Sprintf("▶ 开始识别: %s/（样本: %s）", shortLogName(dir.Name), shortLogName(mainVideo.Name)))

	// AV 番号检测：文件名或目录名含番号格式（如 START-622、MIDV-001）时
	// 跳过 TMDB 识别，直接用番号作为标题归入 AV 分类
	if avNum := detectAVNumber(dir.Name, mainVideo.Name); avNum != "" {
		onLog(fmt.Sprintf("✦ 检测到 AV 番号: %s（跳过 TMDB）", avNum))
		media := &TmdbMedia{
			Title:     avNum,
			MediaType: "av",
		}
		return processAVDirectory(ops, cfg, media, dir, files, onLog, results)
	}

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
			useDirName = true
		}
	}

	if parsed.Title == "" {
		// 文件名和目录名都无法识别，移到冗余
		moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
		onLog(fmt.Sprintf("○ %s/ - 无法提取标题，已移到冗余", dir.Name))
		return results
	}

	// TMDB 识别
	media, err := tc.recognize(parsed)
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
				return results
			}
			// 无番号 AV 的标题兜底：TMDB 未命中时用目录名/标题搜 MetaTube，
			// 命中则按命中番号走 AV 流程（元数据已落缓存，直接复用）
			avMedia, avNumDisplay := metatubeSearchTitle(dir.Name)
			if avMedia == nil && parsed.Title != "" && parsed.Title != dir.Name {
				avMedia, avNumDisplay = metatubeSearchTitle(parsed.Title)
			}
			if avMedia != nil && avMedia.Status == "ok" && avNumDisplay != "" {
				onLog(fmt.Sprintf("✦ MetaTube 标题匹配: %q → 番号 %s，按 AV 入库", dir.Name, avNumDisplay))
				media2 := &TmdbMedia{Title: avNumDisplay, MediaType: "av"}
				return processAVDirectory(ops, cfg, media2, dir, files, onLog, results)
			}
			moveQuietly(ops, cfg.Redundant, []string{dir.Fid}, dir.Name+"/", onLog)
			onLog(fmt.Sprintf("○ %s/ - TMDB 未找到匹配，已移到冗余", dir.Name))
			return results
		}
	}

	onLog(fmt.Sprintf("✦ 识别成功: %s → %s (%s)", shortLogName(dir.Name), media.Title, media.Year))

	// 直接查网盘去重（不依赖本地缓存表，不会过期）
	// 检查网盘目标目录里是否有相同 SHA1 的文件
	if checkByCloudSHA1(ops, media, cfg, libAbs, mainVideo.Sha1, parsed, mainVideo.Name) {
		if err := ops.moveFiles(cfg.Existing, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到已存在失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ → 已存在: %s (%s)，已移到已存在目录", dir.Name, media.Title, media.Year))
		}
		for _, vf := range videoFiles {
			results = append(results, OrganizeResult{FileName: vf.Name, Status: "exists", Title: media.Title, Year: media.Year, MediaType: media.MediaType,
				Message: "网盘已有相同文件"})
		}
		return results
	}

	// 洗版判定：库内已有同一部影视时按优先级规则比较版本
	// （rec.TargetPath 是文件路径，台账查询需要目录前缀，必须 path.Dir）
	if rec, ok := lookupMediaRecord(media); ok {
		switch tryWashReplace(ops, cfg, media, mainVideo.Name, path.Dir(rec.TargetPath), onLog) {
		case washReplaced:
			// 旧版已让位，落入下方正常入库
		case washNotBetter:
			// 库内版本更优：新文件按「已存在」处理，不再重复入库
			if err := ops.moveFiles(cfg.Existing, []string{dir.Fid}); err != nil {
				onLog(fmt.Sprintf("✗ %s/ - 移动到已存在失败: %v", dir.Name, err))
			} else {
				onLog(fmt.Sprintf("○ %s/ - 库内已有更优版本，已移到已存在目录", dir.Name))
			}
			for _, vf := range videoFiles {
				results = append(results, OrganizeResult{FileName: vf.Name, Status: "exists", Title: media.Title, Year: media.Year, MediaType: media.MediaType,
					Message: "库内已有更优版本"})
			}
			return results
		}
	}

	// 不存在 → 分类 + 移动到我的影视库
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, mainVideo.Name)
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	_ = targetDir // 目标目录在下方按新结构创建（根目录 + 季目录）

	// 按文件分类移动（规范结构）：
	//   视频 + 字幕 → 季目录（电影为根目录）；NFO + 标准封面图 → 剧集根目录；垃圾 → 冗余
	parts := strings.Split(newPath, "/")
	rootRel := mediaTypeCategory(media.MediaType) + "/" + category + "/" + parts[0]
	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", rootRel))
	rootCid, err := ops.ensurePath(cfg.Library, rootRel)
	if err != nil {
		onLog(fmt.Sprintf("✗ %s/ - 创建目录失败: %v（目标=%q）", dir.Name, err, rootRel))
		results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建目录失败: " + err.Error()})
		return results
	}
	mediaCid := rootCid
	if media.MediaType == "tv" && len(parts) >= 2 { // Season XX 层
		onLog(fmt.Sprintf("▣ 季目录就绪: %s/%s", rootRel, parts[1]))
		mediaCid, err = ops.ensurePath(cfg.Library, rootRel+"/"+parts[1])
		if err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 创建季目录失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "创建季目录失败: " + err.Error()})
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
	if policy := loadEnrichPolicy(); policy.Enabled {
		for i, vf := range videoFiles {
			if !enrichNeedsProbe(vf.Name) || vf.PickCode == "" {
				continue
			}
			onLog(fmt.Sprintf("▣ 补全探测: %s（文件名缺画质信息）", vf.Name))
			probe, perr := probeFileNow(vf.PickCode)
			if probe == nil {
				onLog(fmt.Sprintf("○ 补全探测失败 %s（保留原名）: %s", vf.Name, perr))
				continue
			}
			{
				action, reason := enrichDecide(vf.Name, probe, policy)
				if action == "rename" {
					ext := pathExt(vf.Name)
					base := strings.TrimSuffix(vf.Name, ext)
					newName := buildEnrichedName(base, ext, probe)
					if newName != vf.Name {
						if err := ops.rename(vf.Fid, newName); err != nil {
							onLog(fmt.Sprintf("○ 补全改名失败 %s: %v", vf.Name, err))
						} else {
							onLog(fmt.Sprintf("✓ 补全 %s → %s", vf.Name, newName))
							enrichRenames[baseName(vf.Name)] = baseName(newName)
							videoFiles[i].Name = newName // 后续模板重命名基于补全后的名字
						}
					}
				} else {
					onLog(fmt.Sprintf("○ 补全跳过 %s: %s", vf.Name, reason))
				}
			}
		}
	}

	// 先在源目录重命名（批量 batch_rename），再移动到目标
	renameBeforeMove(ops, media, videoFiles, files, enrichRenames, onLog)

	// 视频/字幕 → 季目录（电影为根目录）
	if len(mediaFids) > 0 {
		if err := ops.moveFiles(mediaCid, mediaFids); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动文件失败: %v", dir.Name, err))
			results = append(results, OrganizeResult{FileName: dir.Name + "/", Status: "failed", Message: "移动文件失败: " + err.Error()})
			return results
		}
	}
	// 封面/NFO → 剧集根目录
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
		if err == nil {
			ops.moveFiles(junkCid, junkFids)
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
			}
		}
	}

	// 整理完毕：已移空的源目录移到冗余（避免待整理目录残留空壳）
	if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
		onLog(fmt.Sprintf("○ %s/ - 空源目录移到冗余失败: %v", dir.Name, err))
	} else {
		onLog(fmt.Sprintf("○ %s/ - 空源目录已移到冗余", dir.Name))
	}

	// 记录到数据库
	recordMedia(media, category, targetDir+"/"+pathBase(newPath), "")

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
	notifyMediaStoredFull(media, dir.Name, pathBase(newPath), category, videoFiles, mainVideo.Name, movedCount, movedBytes)

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
func processSingleFileWithSiblings(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, onLog func(string)) []OrganizeResult {
	// 先识别主文件
	result := processSingleFile(ops, cfg, tc, replaceRules, f, libAbs, onLog)
	if result.Status != "success" {
		return []OrganizeResult{result}
	}

	// 识别成功 → 列出待整理目录中的其他散文件，找同前缀的视频
	mainPrefix := extractSeriesPrefix(f.Name)
	if mainPrefix == "" {
		return []OrganizeResult{result} // 无法提取前缀，不批量处理
	}

	entries, _, err := ops.listEntries(cfg.Pending, 0)
	if err != nil {
		return []OrganizeResult{result} // 列表失败，只处理主文件
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
			if pc := nilSprint(e["pc"]); pc != "" {
				sib.PickCode = pc // 散文件也要能触发补全探测（此前恒缺，静默跳过）
			}
			siblings = append(siblings, sib)
		}
	}

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
		sibResult := organizeIdentifiedFile(ops, cfg, tc, replaceRules, sib, libAbs, result, onLog)
		allResults = append(allResults, sibResult)
		if sibResult.Status == "success" {
			successCount++
		}
	}

	onLog(fmt.Sprintf("✓ 散文件批量完成: 共 %d 个文件（成功 %d）", len(allResults), successCount))
	return allResults
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
func organizeIdentifiedFile(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, mainResult OrganizeResult, onLog func(string)) OrganizeResult {
	result := OrganizeResult{FileName: f.Name}

	// sha1 去重
	if sha1ExistsInLibrary(f.Sha1) {
		ops.moveFiles(cfg.Existing, []string{f.Fid})
		onLog(fmt.Sprintf("○ %s - sha1已存在，已移到已存在目录", f.Name))
		return OrganizeResult{FileName: f.Name, Status: "exists", Message: "sha1已存在"}
	}

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
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		return result
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动失败: " + err.Error()
		return result
	}

	// 重命名为标准名
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
		}
	}

	result.Category = category
	result.TargetDir = targetDir
	result.TmdbID = mainResult.TmdbID
	result.Title = mainResult.Title
	result.Year = mainResult.Year
	result.MediaType = mainResult.MediaType
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s] → %s", mainResult.Title, mainResult.Year, category, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s", f.Name, stdPath(newPath)))
	return result
}

func stdPath(p string) string {
	return p
}

// processSingleFile 处理待整理目录下的顶层单独视频文件
func processSingleFile(ops *pan115Ops, cfg *OrgConfig, tc *TmdbClient, replaceRules []ReplaceRule, f remoteFile, libAbs string, onLog func(string)) OrganizeResult {
	result := OrganizeResult{FileName: f.Name}

	// AV 番号检测（与目录流程对齐）：散文件直接放待整理根目录同样要能走
	// AV 通道——此前此路径缺检测，SSNI-056.mp4 这类番号文件被打去 TMDB
	// 搜索后进冗余
	if avNum := detectAVNumber("", f.Name); avNum != "" {
		onLog(fmt.Sprintf("✦ 检测到 AV 番号: %s（跳过 TMDB）", avNum))
		media := &TmdbMedia{
			Title:     avNum,
			MediaType: "av",
		}
		return processAVDirectory(ops, cfg, media, dirEntry{Name: f.Name, IsDir: false}, []remoteFile{f}, onLog, nil)[0]
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
	if parsed.Title == "" {
		// 无法识别，移到冗余（附件随行，避免字幕变孤儿）
		moveQuietly(ops, cfg.Redundant, []string{f.Fid}, f.Name, onLog)
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Redundant, false, onLog)
		result.Status = "failed"
		result.Message = "无法提取标题，已移到冗余"
		onLog(fmt.Sprintf("✗ %s - 无法提取标题，已移到冗余", f.Name))
		return result
	}

	// TMDB 识别
	media, err := tc.recognize(parsed)
	if err != nil {
		// 瞬时错误（网络/限流）：留在待整理目录，下轮重试（移冗余会误分流好内容）
		result.Status = "failed"
		result.Message = "TMDB 暂时不可达，留在待整理: " + err.Error()
		onLog(fmt.Sprintf("○ %s - TMDB 暂时不可达（%v），留在待整理目录下轮重试", f.Name, err))
		return result
	}
	if media == nil {
		moveQuietly(ops, cfg.Redundant, []string{f.Fid}, f.Name, onLog)
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Redundant, false, onLog)
		result.Status = "failed"
		result.Message = "TMDB 未找到匹配，已移到冗余"
		onLog(fmt.Sprintf("✗ %s - TMDB 未找到匹配，已移到冗余", f.Name))
		return result
	}

	result.TmdbID = media.TmdbID
	result.Title = media.Title
	result.Year = media.Year
	result.MediaType = media.MediaType

	onLog(fmt.Sprintf("✦ 识别成功: %s → %s (%s)", shortLogName(f.Name), media.Title, media.Year))

	// 直接查网盘去重（不依赖本地缓存表）
	if checkByCloudSHA1(ops, media, cfg, libAbs, f.Sha1, parsed, f.Name) {
		ops.moveFiles(cfg.Existing, []string{f.Fid})
		moveSiblingAttachments(ops, cfg.Pending, oldBase, "", cfg.Existing, false, onLog)
		result.Status = "exists"
		result.Message = fmt.Sprintf("已存在: %s (%s)，已移到已存在目录", media.Title, media.Year)
		onLog(fmt.Sprintf("○ %s → 已存在: %s (%s)", f.Name, media.Title, media.Year))
		return result
	}

	// 分类 + 移动到影视库
	category := classifyMedia(media)
	newPath := buildNewNameWithTemplate(media, parsed, f.Name)
	targetDir := mediaTypeCategory(media.MediaType) + "/" + category + "/" + pathDir(newPath)

	targetCid, err := ops.ensurePath(cfg.Library, targetDir)
	if err != nil {
		result.Status = "failed"
		result.Message = "创建目录失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 创建目录失败: %v", f.Name, err))
		return result
	}

	if err := ops.moveFiles(targetCid, []string{f.Fid}); err != nil {
		result.Status = "failed"
		result.Message = "移动文件失败: " + err.Error()
		onLog(fmt.Sprintf("✗ %s - 移动失败: %v", f.Name, err))
		return result
	}
	// 视频本体重命名为标准名
	if stdName := pathBase(newPath); stdName != "" && stdName != f.Name {
		if err := ops.rename(f.Fid, stdName); err != nil {
			onLog(fmt.Sprintf("○ 重命名失败保持原名 %s: %v", f.Name, err))
		} else {
			onLog(fmt.Sprintf("✓ 重命名 %s → %s", f.Name, stdName))
		}
	}

	onLog(fmt.Sprintf("▣ 目标目录就绪: %s", category+"/"+strings.Split(newPath, "/")[0]))
	// 成功入库：附件随行并按视频新名重命名字幕（播放器按视频名匹配外挂字幕）
	newBase := baseName(pathBase(newPath))
	moveSiblingAttachments(ops, cfg.Pending, oldBase, newBase, targetCid, true, onLog)

	recordMedia(media, category, targetDir+"/"+pathBase(newPath), "")
	result.Category = category
	result.TargetDir = targetDir
	result.Status = "success"
	result.Message = fmt.Sprintf("→ %s (%s) [%s/%s] → %s", media.Title, media.Year, category, media.MediaType, targetDir)
	onLog(fmt.Sprintf("✓ %s → %s (%s) [%s/%s] → %s", f.Name, media.Title, media.Year, category, media.MediaType, targetDir))

	return result
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

// executeOrganizeWithConfig 用指定的 OrgConfig 执行整理（转存目录等场景）
func (h *Handler) executeOrganizeWithConfig(cfg *OrgConfig, syncAfter bool) ([]gin.H, []OrganizeResult, error) {
	orgStart := time.Now()
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, nil, err
	}

	logFn := func(msg string) { log.Println(msg) }
	orgResults, successCount := runOrganizeEngineWithConfig(ops, cfg, logFn)
	existsN, failN := 0, 0
	for _, r := range orgResults {
		if r.Status == "exists" {
			existsN++
		}
		if r.Status == "failed" {
			failN++
		}
	}
	log.Printf("[整理] ✅ 整理完成（成功 %d · 已存在 %d · 失败 %d，耗时 %s）",
		successCount, existsN, failN, time.Since(orgStart).Truncate(time.Second))

	steps := []gin.H{}
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
	steps = append(steps, gin.H{"step": "整理（转存目录）", "status": "完成",
		"message": fmt.Sprintf("共 %d 个文件，成功 %d，已存在 %d，失败 %d", totalFiles, successCount, existsCount, failedCount)})

	return steps, orgResults, nil
}

// runOrganizeEngineWithConfig 用指定的 OrgConfig 运行整理引擎
func runOrganizeEngineWithConfig(ops *pan115Ops, cfg *OrgConfig, onLog func(string)) ([]OrganizeResult, int) {
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
	libAbs := ""
	if ops.cookie != "" {
		libAbs = absPathOf(ops.cookie, cfg.Library, map[string]dirInfo{})
	}

	topEntries, err := listPendingTopLevel(ops, cfg.Pending)
	if err != nil {
		onLog("✗ 遍历转存目录失败: " + err.Error())
		return results, 0
	}
	if len(topEntries) == 0 {
		return results, 0 // 空转静默
	}

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
	topEntries = filtered

	guards := newOrgGuards(ops.cookie, cfg.Pending, cfg)
	if guards.active {
		onLog("⚠ 扫描根覆盖到媒体库/已存在/冗余目录，这些子树内的条目将被跳过（防误整理库内容）")
	}

	onLog(fmt.Sprintf("▶ 转存目录发现 %d 个条目，开始整理...", len(topEntries)))
	for i, entry := range topEntries {
		SetTaskProgress(fmt.Sprintf("整理 %d/%d：%s", i+1, len(topEntries), truncateStr(entry.Name, 40)))
		results = append(results, processEntry(ops, cfg, tc, replaceRules, guards, entry, libAbs, onLog, 0, &successCount)...)
		time.Sleep(300 * time.Millisecond)
	}
	SetTaskProgress("")
	return results, successCount
}

// ==================== AV 番号识别与处理 ====================

// avCategoryConfig AV 分类配置（与 UI 上的 YAML 对应）
type avCategoryConfig struct {
	Name     string   // 分类名 = 网盘目录名（可任意命名，如英文）
	Prefixes []string // 自定义番号前缀（可选）
	Builtin  string   // 内置库绑定："uncensored" / "domestic"（可选）；
	// 未填写时按分类名含"无码"/"国产"自动映射
}

// avBuiltinUncensored 内置无码番号前缀库（常见厂牌；命中即归入名字含
// "无码"的分类）——无码厂牌是有限集合，适合内置；有码厂牌上千个，
// 由"兜底分类"自然承接，用户无需枚举
var avBuiltinUncensored = []string{
	"FC2", "HEYZO", "N10", // Tokyo Hot（n1xxx）
	"10MU", "1PON", "CARIB", "PACO", // 10Musume/1Pondo/Caribbean/Pacopacomama
	"MURA", "KIN8", "C0930", "H0930", "SCUTE", "XXXAV", "AV9898", "GACHI", "MESU",
}

// avBuiltinDomestic 内置国产番号前缀库（命中归入名字含"国产"的分类）
var avBuiltinDomestic = []string{
	"MD", "MDX", "MDT", "PMC", "JD", "TZ", "MT", "91", "CHARU", "MKY", "MSN",
}

// normalizeAVNum 番号归一化：去分隔符、转大写（"fc2-ppv-123" → "FC2PPV123"）
func normalizeAVNum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loadAVCategories 读 AV 分类：优先解析用户在分类策略 YAML 里保存的
// av: 段（此前该段被解析器跳过、保存无效果）；无 av: 段时回退默认两分类
func loadAVCategories() []avCategoryConfig {
	if model.DB != nil {
		var rule model.ScrapeRule
		model.DB.Where("type = ?", "category_config").First(&rule)
		if cats := parseAVCategoriesFromYAML(rule.Config); len(cats) > 0 {
			return cats
		}
	}
	// 默认分类：无码走内置前缀库识别；有码排最后作兜底
	//（兜底=最后一个空前缀分类；未配置国产分类时国产内容也落有码）
	return []avCategoryConfig{
		{Name: "无码", Builtin: "uncensored"},
		{Name: "有码"},
	}
}

// parseAVCategoriesFromYAML 解析分类 YAML 中的 av: 段
//
//	av:
//	  无码:
//	    num_prefix: 'FC2,HEYZO'
//	  有码:
//	    num_prefix: ''   ← 空 = 兜底分类
func parseAVCategoriesFromYAML(src string) []avCategoryConfig {
	var root yaml.Node
	if yaml.Unmarshal([]byte(src), &root) != nil || len(root.Content) == 0 {
		return nil
	}
	mp := root.Content[0]
	for i := 0; i+1 < len(mp.Content); i += 2 {
		if mp.Content[i].Value != "av" {
			continue
		}
		val := mp.Content[i+1]
		if val.Kind != yaml.MappingNode {
			return nil
		}
		var cats []avCategoryConfig
		for j := 0; j+1 < len(val.Content); j += 2 {
			name := strings.TrimSpace(val.Content[j].Value)
			if name == "" {
				continue
			}
			c := avCategoryConfig{Name: name}
			if val.Content[j+1].Kind == yaml.MappingNode {
				sub := val.Content[j+1]
				for k := 0; k+1 < len(sub.Content); k += 2 {
					if sub.Content[k].Value == "builtin" {
						c.Builtin = strings.ToLower(strings.TrimSpace(sub.Content[k+1].Value))
						continue
					}
					if sub.Content[k].Value != "num_prefix" {
						continue
					}
					raw := strings.Trim(strings.TrimSpace(sub.Content[k+1].Value), "'\"")
					for _, p := range strings.Split(raw, ",") {
						if p = strings.TrimSpace(p); p != "" {
							c.Prefixes = append(c.Prefixes, p)
						}
					}
				}
			}
			cats = append(cats, c)
		}
		return cats
	}
	return nil
}

// classifyAVNumber AV 分类判定（顺序）：
//  1. 用户 YAML 里配的 num_prefix 前缀匹配
//  2. 内置无码/国产前缀库 → 名字含"无码"/"国产"的分类
//  3. 目录名关键词提示（无码/破解/uncensored → 无码；国产/麻豆/探花 → 国产）
//  4. 第一个 num_prefix 为空的分类（兜底，如"有码"）
//  5. 未分类
func classifyAVNumber(avNum, hint string) string {
	cats := loadAVCategories()
	norm := normalizeAVNum(avNum)

	// 1) 用户自定义前缀
	for _, cat := range cats {
		for _, p := range cat.Prefixes {
			if p != "" && strings.HasPrefix(norm, normalizeAVNum(p)) {
				return cat.Name
			}
		}
	}

	// 2) 内置前缀库 → 解析到绑定了对应内置库的分类（Builtin 字段优先，
	// 旧式"分类名含无码/国产"兼容回退）
	matchBuiltinCat := func(code, nameHint string) string {
		for _, cat := range cats {
			if cat.Builtin == code || (cat.Builtin == "" && strings.Contains(cat.Name, nameHint)) {
				return cat.Name
			}
		}
		return ""
	}
	for _, p := range avBuiltinUncensored {
		if strings.HasPrefix(norm, p) {
			if n := matchBuiltinCat("uncensored", "无码"); n != "" {
				return n
			}
			break
		}
	}
	for _, p := range avBuiltinDomestic {
		if strings.HasPrefix(norm, p) {
			if n := matchBuiltinCat("domestic", "国产"); n != "" {
				return n
			}
			break
		}
	}

	// 3) 目录名关键词提示（文件名常自带"无码/破解/国产/麻豆"字样）
	hintLower := strings.ToLower(hint)
	switch {
	case strings.Contains(hintLower, "无码") || strings.Contains(hint, "無碼") || strings.Contains(hintLower, "uncensored") || strings.Contains(hintLower, "破解"):
		if n := matchBuiltinCat("uncensored", "无码"); n != "" {
			return n
		}
	case strings.Contains(hint, "国产") || strings.Contains(hint, "麻豆") || strings.Contains(hint, "探花"):
		if n := matchBuiltinCat("domestic", "国产"); n != "" {
			return n
		}
	}

	// 4) 兜底：最后一个 num_prefix 为空的分类（与 movie/tv「兜底放最后」
	// 约定一致——无码留空走内置库、有码留空作兜底时顺序无关歧义）
	fallback := ""
	for _, cat := range cats {
		if len(cat.Prefixes) == 0 {
			fallback = cat.Name
		}
	}
	if fallback != "" {
		return fallback
	}
	return "未分类"
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

// sanitizeAVFilename 清洗 AV 文件名中的广告前缀
// "4k688.com@START-622.mp4" → "START-622.mp4"
// "www.xxx.com@MIDV-001.mp4" → "MIDV-001.mp4"
func sanitizeAVFilename(name string) string {
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

// avNumRegex 匹配常见 AV 番号格式：ABC-123、ABCD-12 等
// sanitizeAVFilename / shortLogName 的预编译正则（热路径：AV 整理对每个
// 文件多阶段反复调用，此前每次执行编译 9 个正则）
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

var avNumRegex = regexp.MustCompile(`(?i)\b([A-Z]{2,6})-?(\d{2,5})\b`)

// avNumLooseRe 番号后带发布标记的变体（hmn-898ch = HMN-898 中字版、
// xxx898uc = 无码版）：数字后紧跟小写标记导致词边界断开，主正则失配。
// 标记限定为常见后缀白名单，避免 top10mv 之类随机词误判成番号
var avNumLooseRe = regexp.MustCompile(`(?i)\b([A-Z]{2,6})-?(\d{2,5})(?:ch|unc|uc|leak|4k|8k|cd\d?|c\d|u\d|v\d|c|u)?\b`)

// avNumDigitPrefixRe 数字前缀系列（259LUXU-666）：字母段 ≥3 位避免把
// "1080px265" 这类画质词误判成番号；字母段仍过排除前缀表
var avNumDigitPrefixRe = regexp.MustCompile(`(?i)\b(\d{2,4}[A-Z]{3,6})-?(\d{2,5})\b`)

var avLettersRe = regexp.MustCompile(`[A-Z]+`)

// fc2NumRegex 匹配 FC2 番号：FC2-PPV-1234567、FC2_1234567 等（数字 5-8 位）
var fc2NumRegex = regexp.MustCompile(`(?i)\bfc2[-_]?(?:ppv[-_]?)?(\d{5,8})\b`)

// avExcludedPrefixes 常见非 AV 前缀（画质/编码/剧集标记），命中则视为误匹配
var avExcludedPrefixes = map[string]bool{
	"THE": true, "AND": true, "FOR": true, "HD": true, "SD": true, "XX": true,
	"WEB": true, "DL": true, "BLU": true, "UHD": true, "HDR": true, "DTS": true,
	"AAC": true, "H264": true, "H265": true, "X264": true, "X265": true,
	"HEVC": true, "AVC": true, "1080P": true, "720P": true, "2160P": true,
	"4K": true, "2K": true, "CD": true, "DVD": true, "BDRIP": true, "HDRIP": true,
	"WP": true, "XXX": true,
	// 剧集/动漫常见标记（ep01、se02、sp01、ova、ncop 等）
	"EP": true, "SE": true, "SP": true, "EPT": true, "OVA": true, "OAD": true,
	"NCOP": true, "NCED": true, "PV": true, "CM": true, "TV": true, "VOL": true,
}

// detectAVNumber 从目录名和文件名中检测 AV 番号
// 优先用目录名（更干净），文件名作为补充；返回规范化的番号（前缀大写）
func detectAVNumber(dirName, fileName string) string {
	for _, s := range []string{dirName, fileName} {
		if s == "" {
			continue
		}
		// FC2 番号优先（通用规则匹配不到 7 位数字）
		if m := fc2NumRegex.FindStringSubmatch(s); m != nil {
			return "FC2-PPV-" + m[1]
		}
		if m := avNumRegex.FindStringSubmatch(s); m != nil {
			prefix := strings.ToUpper(m[1])
			if !avExcludedPrefixes[prefix] {
				return prefix + "-" + m[2]
			}
		}
		if m := avNumLooseRe.FindStringSubmatch(s); m != nil {
			prefix := strings.ToUpper(m[1])
			if !avExcludedPrefixes[prefix] {
				return prefix + "-" + m[2]
			}
		}
		if m := avNumDigitPrefixRe.FindStringSubmatch(s); m != nil {
			prefix := strings.ToUpper(m[1])
			letters := avLettersRe.FindString(prefix)
			if letters != "" && !avExcludedPrefixes[letters] {
				return prefix + "-" + m[2]
			}
		}
	}
	return ""
}

// avAdDomainRegex 广告域名（清洗后仍任意位置出现即视为广告载体）
var avAdDomainRegex = regexp.MustCompile(`(?i)(?:https?://|www\.)?[a-z0-9][a-z0-9-]{1,15}\.(?:com|net|org|cc|xyz|me|tv|info|vip|top|app|club|site|online|icu|fun|win)\b`)

// avAdKeywords 广告文件常见关键词
var avAdKeywords = []string{
	"18+", "游戏大全", "最新地址", "永久地址", "永久导航", "网址导航", "导航网",
	"发布页", "发布器", "天天更新", "每周更新", "免费观看", "在线观看", "手机看片",
	"福利网", "福利社", "破解版", "高清资源网", "资源网", "看片网", "影片网",
	"电影网", "安卓版", "app版", "app下载", "磁力搜索", "同城约",
}

// isAVAdFile 判断 AV 目录内的文件是否为广告/引流文件。
// 以「清洗后的文件名」为准：正规片的广告前缀会被 sanitizeAVFilename 清掉
// （"4k688.com@START-622.mp4" → "START-622.mp4"，不含域名，正常入库）；
// 清完仍残留域名或广告词的才是真广告（如 "18+游戏大全(996gg.cc)-…"）
func isAVAdFile(name string) bool {
	cleaned := baseName(sanitizeAVFilename(name))
	if avAdDomainRegex.MatchString(cleaned) {
		return true
	}
	lower := strings.ToLower(cleaned)
	for _, kw := range avAdKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// containsAVAdKeyword 广告词检测（大小写不敏感）
func containsAVAdKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range avAdKeywords {
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
// 发布站水印（sanitizeAVFilename 能剥掉）。剥掉水印后剩干净片名/番号的算
// 正片；剥不掉（域名在中间）或剥完剩广告词的（"18+游戏大全(996gg.cc)-…"、
// "4k688.com@免费观看…"）才是真广告
func isAdOnlyVideo(name string) bool {
	raw := baseName(name) // 无扩展名的基名
	cleaned := strings.TrimSpace(stripReleaseAds(raw))
	if cleaned == "" {
		return true
	}
	// 水印前缀判定必须喂完整文件名：sanitizeAVFilename 依赖 pathExt 找到真
	// 扩展名，传无扩展名基名时 "xxx.com@番号" 的域名会被误当扩展名保留
	trimmed := strings.TrimSpace(baseName(sanitizeAVFilename(name)))
	if t := strings.Trim(trimmed, ".-_ "); t != "" && trimmed != raw &&
		!avAdDomainRegex.MatchString(t) && !containsAVAdKeyword(t) {
		return false // 水印前缀剥掉后是干净片名/番号 → 正片
	}
	return avAdDomainRegex.MatchString(cleaned) || containsAVAdKeyword(cleaned)
}

// avSubLangRegex 字幕语言标记（.chs/.cht/.eng 等）
var avSubLangRegex = regexp.MustCompile(`\.(chs|cht|eng|chi|jap)\b`)

// avCarriesNumber 正片文件名（清洗后）应携带番号，不带番号的多为引流视频；
// FC2 文件名常省略 PPV 段（FC2-1234567 / FC2-PPV-1234567 两种写法都认），
// 连字符差异（START622 / START-622）也容忍
func avCarriesNumber(cleanedBase, avNum string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }
	lc := norm(cleanedBase)
	lcc := strings.ReplaceAll(lc, "-", "")
	for _, c := range []string{norm(avNum), strings.ReplaceAll(norm(avNum), "-ppv", "")} {
		if c != "" && (strings.Contains(lc, c) || strings.Contains(lcc, strings.ReplaceAll(c, "-", ""))) {
			return true
		}
	}
	// 分词模糊匹配：发布方常把番号数字段补零且不带连字符
	//（JUVR-303 ↔ 文件名 juvr00303_1_8k）。按分隔符切词，词内
	// 「字母前缀+数字」的数字段去掉前导零后与番号数字段精确比对
	alnum := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	m := avPrefixNumRe.FindStringSubmatch(alnum(avNum))
	if m == nil {
		return false
	}
	prefixes := []string{m[1]}
	if p := strings.TrimSuffix(m[1], "ppv"); p != m[1] {
		prefixes = append(prefixes, p) // FC2-PPV ↔ 文件 fc2_xxx
	}
	num := strings.TrimLeft(m[2], "0")
	if num == "" {
		return false
	}
	for _, tok := range strings.FieldsFunc(lc, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		if tm := avPrefixNumRe.FindStringSubmatch(tok); tm != nil {
			for _, p := range prefixes {
				if tm[1] == p && strings.TrimLeft(tm[2], "0") == num {
					return true
				}
			}
		}
	}
	return false
}

// avPrefixNumRe 「字母前缀+纯数字段」（juvr303 / fc2ppv1234567 → 前缀+番号数字）
var avPrefixNumRe = regexp.MustCompile(`^([a-z]+)(\d+)$`)

// processAVDirectory 处理 AV 目录（跳过 TMDB，直接用番号入库）
func processAVDirectory(ops *pan115Ops, cfg *OrgConfig, media *TmdbMedia, dir dirEntry, files []remoteFile, onLog func(string), results []OrganizeResult) []OrganizeResult {
	// AV 目录结构：/AV/分类/<AVFolder 模板>/（分类 = 无码/有码，按番号前缀匹配）。
	// 目录与文件名走重命名模板（AVFolder/AVFile，UI 可配）；模板缺省时
	// 降级为番号直命名（旧行为）
	// MetaTube 识别（配置后生效）：番号 → 真实标题/演员 → AVMeta 缓存，
	// 供重命名模板 {av_title} 等变量使用；不写任何元数据文件到媒体目录。
	// 未配置/识别不到 → meta 为 nil，完全退回纯番号行为
	avMeta := metatubeFetchCached(media.Title)
	if avMeta != nil && avMeta.Status == "ok" {
		onLog(fmt.Sprintf("✦ MetaTube 识别: %s《%s》%s（%s）",
			media.Title, avMeta.Title, strings.Join(avMetaActors(avMeta), "、"), avMeta.Year))
	} else if metatubeEnabled() {
		onLog(fmt.Sprintf("○ %s - MetaTube 未识别到，按番号入库", media.Title))
	}
	category := classifyAVNumber(media.Title, dir.Name)
	// 模板变量（画质/制作组等）取自体积最大的视频
	mainName, mainSize := "", int64(-1)
	for _, f := range files {
		if classifyFile(f.Name) == FileTypeVideo && f.Size > mainSize {
			mainSize, mainName = f.Size, f.Name
		}
	}
	avFolder, baseFile := "", ""
	if rel := buildNewNameWithTemplate(media, &ParsedName{Title: media.Title}, mainName); rel != "" {
		parts := strings.SplitN(rel, "/", 2)
		if parts[0] != "" {
			avFolder = parts[0]
		}
		if len(parts) == 2 && parts[1] != "" {
			baseFile = parts[1]
		}
	}
	if avFolder == "" || baseFile == "" {
		avFolder = media.Title
		baseFile = media.Title + pathExt(mainName)
	}
	avDir := category + "/" + avFolder
	baseNoExt := strings.TrimSuffix(baseFile, pathExt(baseFile))

	// 分流：正片视频（含多分卷）/ 字幕 / 元数据 / 广告垃圾
	var videos, subs, metas []remoteFile
	var junkFids []string
	var junkFiles []remoteFile // 指纹登记用
	minBytes := int64(cfg.MinSize) * 1024 * 1024
	for _, f := range files {
		switch classifyFile(f.Name) {
		case FileTypeVideo:
			cleanedBase := baseName(sanitizeAVFilename(f.Name))
			switch {
			case isAVAdFile(f.Name):
				junkFids = append(junkFids, f.Fid)
				junkFiles = append(junkFiles, f)
				onLog(fmt.Sprintf("○ %s - 广告/引流视频，移到冗余", f.Name))
			case !avCarriesNumber(cleanedBase, media.Title):
				junkFids = append(junkFids, f.Fid)
				junkFiles = append(junkFiles, f)
				onLog(fmt.Sprintf("○ %s - 清洗后不含番号 %s，疑似引流视频，移到冗余", f.Name, media.Title))
			case minBytes > 0 && f.Size > 0 && f.Size < minBytes:
				junkFids = append(junkFids, f.Fid)
				junkFiles = append(junkFiles, f)
				onLog(fmt.Sprintf("○ %s - 仅 %.1fMB（小于最小体积 %dMB），移到冗余",
					f.Name, float64(f.Size)/1024/1024, cfg.MinSize))
			default:
				videos = append(videos, f)
			}
		case FileTypeSubtitle:
			subs = append(subs, f)
		case FileTypeNFO, FileTypeStdImage:
			metas = append(metas, f)
		default:
			junkFids = append(junkFids, f.Fid)
			junkFiles = append(junkFiles, f)
			onLog(fmt.Sprintf("○ %s - 垃圾文件，移到冗余", f.Name))
		}
	}

	// 正片全被过滤（整包都是广告）→ 整目录进冗余
	if len(videos) == 0 {
		if err := ops.moveFiles(cfg.Redundant, []string{dir.Fid}); err != nil {
			onLog(fmt.Sprintf("✗ %s/ - 移动到冗余失败: %v", dir.Name, err))
		} else {
			onLog(fmt.Sprintf("○ %s/ - 无有效视频（全部为广告/垃圾），已移到冗余", dir.Name))
		}
		return results
	}

	// 广告/垃圾先行移到冗余（登记指纹：重复投放的引流内容秒判）
	if len(junkFiles) > 0 {
		if err := ops.moveFiles(cfg.Redundant, junkFids); err != nil {
			onLog(fmt.Sprintf("○ %s/ - 垃圾文件移到冗余失败: %v", dir.Name, err))
		}
	}

	onLog(fmt.Sprintf("▣ AV 目标目录: %s（分类: %s）", avDir, category))
	targetCid, err := ops.ensurePath(cfg.Library, avDir)
	if err != nil {
		onLog(fmt.Sprintf("✗ AV 创建目录失败: %v", err))
		return results
	}

	// 重命名：正片按体积降序，主片 = AVFile 模板名，其余分卷 = 基名-CDn.ext
	//（广告包里常见"引流视频+正片"，过去全部重命名为番号导致同名冲突）。
	// 全部重命名合并为一次 batch_rename（逐个调用要过 3 秒/次的 API 限流）
	sort.Slice(videos, func(i, j int) bool { return videos[i].Size > videos[j].Size })
	names := map[string]string{}
	var moveFids []string
	for i, v := range videos {
		newName := baseNoExt + pathExt(v.Name)
		if i > 0 {
			newName = fmt.Sprintf("%s-CD%d%s", baseNoExt, i+1, pathExt(v.Name))
		}
		if newName != v.Name {
			names[v.Fid] = newName
		}
		moveFids = append(moveFids, v.Fid)
	}
	for _, sub := range subs {
		subSuffix := ""
		if m := avSubLangRegex.FindStringSubmatch(baseName(sub.Name)); m != nil {
			subSuffix = m[0]
		}
		newName := baseNoExt + subSuffix + pathExt(sub.Name)
		if newName != sub.Name {
			names[sub.Fid] = newName
		}
		moveFids = append(moveFids, sub.Fid)
	}
	for _, m := range metas {
		newName := baseNoExt + pathExt(m.Name)
		if newName != m.Name {
			names[m.Fid] = newName
		}
		moveFids = append(moveFids, m.Fid)
	}
	if len(names) > 0 {
		if err := ops.renameBatch(names); err != nil {
			onLog(fmt.Sprintf("✗ AV 批量重命名失败（%d 个文件保持原名）: %v", len(names), err))
		} else {
			onLog(fmt.Sprintf("✓ AV 批量重命名 %d 个文件（例: %s → %s）", len(names), videos[0].Name, baseNoExt+pathExt(videos[0].Name)))
		}
	}

	if err := ops.moveFiles(targetCid, moveFids); err != nil {
		onLog(fmt.Sprintf("✗ AV 移动失败: %v", err))
		return results
	}

	// 移空的源目录到冗余
	ops.moveFiles(cfg.Redundant, []string{dir.Fid})

	onLog(fmt.Sprintf("✓ AV 入库: %s → %s", dir.Name, avDir))
	// 识别结果落库（总览面板最近整理显示真实标题）；媒体目录不写任何文件
	recordAVMedia(avMeta, media.Title, category, avDir)
	// 入库富通知（与电影/剧集同款：封面卡片 + 类型/演员/厂牌信息）
	notifyAVStored(avMeta, media.Title, category, videos)
	for _, v := range videos {
		results = append(results, OrganizeResult{
			FileName: v.Name, Status: "success",
			Title: media.Title, MediaType: "av",
			Category: category, TargetDir: avDir,
			Message: fmt.Sprintf("AV %s/%s", category, media.Title),
		})
	}
	return results
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

// avCleanMaker 厂牌展示清洗：JavBus 原始厂牌名常带全角连接符和尾部
// 分隔符（"KMPVR－彩－"），统一半角并去掉首尾装饰后展示
func avCleanMaker(s string) string {
	out := strings.TrimSpace(strings.NewReplacer("－", "-", "‐", "-", "–", "-", "—", "-").Replace(s))
	return strings.Trim(out, "-_~～ ")
}

// notifyAVStored AV 入库富通知：MetaTube 封面卡片（企微 news / TG 图片），
// 内容含 类别/演员/厂牌/文件数与大小——与电影剧集的 notifyMediaStoredFull 同级
func notifyAVStored(m *model.AVMeta, num, category string, videos []remoteFile) {
	title := "✓ 整理成功 " + num
	line := "类型：AV · 类别：" + category
	var totalBytes int64
	for _, v := range videos {
		totalBytes += v.Size
	}
	line += fmt.Sprintf("\n共计：%d 个文件 · %s", len(videos), humanSizeBytes(totalBytes))
	entry := mediaNotifEntry{Title: title, Line: line}
	if m != nil && m.Status == "ok" {
		if m.Title != "" {
			entry.Title = title + " " + m.Title
		}
		entry.Year = m.Year
		if actors := avMetaActors(m); len(actors) > 0 {
			entry.Line += "\n演员：" + strings.Join(actors, "、")
		}
		if maker := avCleanMaker(m.Publisher); maker != "" {
			entry.Line += "\n厂牌：" + maker
		}
		if m.CoverURL != "" {
			entry.PosterURL = m.CoverURL // 源站公网封面（字节抓取失败时的兜底）
			// 封面字节优先从 metatube-server 图片代理取（免源站防盗链）：
			// TG 直接上传，企微转存官方图床后 picurl 必然显示
			data, err := metatubeFetchCoverBytes(m)
			if err != nil || len(data) <= 100 {
				data, err = fetchHTTPBytes(m.CoverURL, 10*time.Second)
			}
			if err == nil && len(data) > 100 {
				entry.PosterData = data
			}
		}
	}
	QueueMediaNotif(entry)
}

// notifyMediaStoredFull 入库成功富通知：TMDB 封面（企微图文卡 / TG 图片），
// 内容含 类型/类别/质量/文件数与大小/集数区间/重命名信息
func notifyMediaStoredFull(media *TmdbMedia, oldName, newName, category string, videoFiles []remoteFile, mainVideoName string, movedCount int, movedBytes int64) {
	if media == nil {
		return
	}
	typeLabel := "电影"
	if media.MediaType == "tv" {
		typeLabel = "剧集"
	}
	// 质量：分辨率 + 特效 + 来源（从主视频文件名解析）
	quality := ""
	if mainVideoName != "" {
		ri := ParseResourceInfo(mainVideoName)
		q := strings.ToUpper(ri.Pix)
		if ri.Effect != "" {
			q += " " + strings.ToUpper(ri.Effect)
		}
		if ri.Type != "" {
			q += " " + strings.ToUpper(ri.Type)
		}
		quality = q
	}
	lines := []string{fmt.Sprintf("类型：%s · 类别：%s", typeLabel, category)}
	if quality != "" {
		lines = append(lines, "质量："+quality)
	}
	if movedCount > 0 {
		lines = append(lines, fmt.Sprintf("共计：%d 个文件 · %s", movedCount, humanSizeBytes(movedBytes)))
	}
	if ep, miss := episodeRangeWithMissing(videoFiles, media); ep != "" {
		if miss != "" {
			lines = append(lines, "集数："+ep+"（缺 "+miss+"）")
		} else {
			lines = append(lines, "集数："+ep+"（全）")
		}
	}
	content := strings.Join(lines, "\n")

	entry := mediaNotifEntry{Title: media.Title, Year: media.Year, Line: content}
	if media.MediaType != "av" && media.TmdbID != 0 && media.PosterPath != "" {
		entry.PosterURL = tmdbImageBase() + "/t/p/w500" + media.PosterPath
		if media.MediaType == "tv" {
			entry.Link = fmt.Sprintf("https://www.themoviedb.org/tv/%d", media.TmdbID)
		} else {
			entry.Link = fmt.Sprintf("https://www.themoviedb.org/movie/%d", media.TmdbID)
		}
	}
	QueueMediaNotif(entry)
}
