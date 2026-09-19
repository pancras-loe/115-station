package api

// ==================== 重命名模板引擎（CMS 20+ 变量完整支持） ====================
//
// 模板变量（与 CMS 完全对齐）：
//   {original_name}     原文件名
//   {ext}               扩展名（含.）
//   {title}             TMDB 中文标题
//   {en_title}          TMDB 英文标题
//   {first_letter}      标题拼音首字母（大写）
//   {year}              年份
//   {tmdb_id}           TMDB ID
//   {resource_pix}      分辨率
//   {resource_version}  资源版本（IMAX/HQ/3D/CC/DC等）
//   {resource_source}   来源平台（NF/DSNP/AMZN等）
//   {resource_type}     资源质量（BluRay/WEB-DL/HDTV）
//   {resource_effect}   特效（DV.HDR/DV/HDR/SDR）
//   {video_encode}      视频编码（H265.10bit/x264等）
//   {audio_encode}      音频编码（TrueHD.7.1/AAC2.0等）
//   {resource_team}     发布组
//   {fps}               帧率
//   {season_episode}    季集 SxxExx
//   {season_num}        季号
//   {episode_num}       集号
//   {disc_num}          盘号
//   {season_name}       季名（TMDB 季信息，可能为空）
//   {season_year}       季年份（可能为空）
//   {episode_name}      集名（TMDB 集信息，可能为空）
//   {custom_regex_match} 自定义正则匹配结果
//
// 支持块语法：{变量非空则输出}，如 {first_letter}/{title} ({year}) [{tmdb_id}]

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// RenameContext 重命名上下文（包含模板引擎需要的全部数据）
type RenameContext struct {
	OriginalName string
	Ext          string
	Media        *TmdbMedia
	Parsed       *ParsedName
	Resource     ResourceInfo
	SeasonName   string
	SeasonYear   string
	EpisodeName  string
	CustomRegex  string
}

// buildRenameContext 构建重命名上下文
func buildRenameContext(media *TmdbMedia, parsed *ParsedName, originalName string) *RenameContext {
	ctx := &RenameContext{
		OriginalName: originalName,
		Ext:          pathExt(originalName),
		Media:        media,
		Parsed:       parsed,
		Resource:     ParseResourceInfo(originalName),
	}
	return ctx
}

// ApplyTemplate 应用重命名模板，替换所有变量
// 支持块语法：{xxx非空则输出xxx包裹的内容}
func (ctx *RenameContext) ApplyTemplate(template string) string {
	result := template

	replacements := ctx.allReplacements()
	addExpressionReplacements(template, replacements)

	// 第一步：处理 <> 块语法（变量非空才输出块内容）
	// 格式：<.{resource_pix}> → resource_pix 非空时输出 ".1080p"，空时输出 ""
	// 格式：<-{resource_team}> → resource_team 非空时输出 "-TnT"，空时输出 ""
	result = processBlocks(result, replacements)

	// 第二步：替换普通 {variable}
	for k, v := range replacements {
		result = strings.ReplaceAll(result, k, v)
	}

	// 输出统一清理：
	// 1. (.) 空括号 → 删掉（变量为空时残留）
	// 2. (.xxx) 有值括号 → 改为 .xxx（统一点分隔，不管模板用的什么括号语法）
	// 3. 连续点/横线 → 压缩为单个
	// 4. 首尾多余分隔符 → 去掉
	for strings.Contains(result, "(.)") {
		result = strings.ReplaceAll(result, "(.)", "")
	}
	result = parenValueRe.ReplaceAllString(result, "$1")
	for strings.Contains(result, "..") {
		result = strings.ReplaceAll(result, "..", ".")
	}
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, ".-")
	result = strings.TrimSpace(result)

	// [[ ]] 是花括号的字面量转义：变量语法占用了 {}，要在文件名里输出真正的
	// 大括号只能这么写（前端 renderRenameExample 的示例渲染同款规则）。
	// 必须放在变量替换之后——先转义的话，转出来的 { 会被当成变量起始符
	result = strings.ReplaceAll(result, "[[", "{")
	result = strings.ReplaceAll(result, "]]", "}")

	return result
}

// parenValueRe 匹配 (.xxx) 形式（括号内有点+值）→ 提取 .xxx（去掉括号）
var parenValueRe = regexp.MustCompile(`\((\.[\w.-]+)\)`)

// templateExprRe 只开放无副作用的字符串变换，既满足命名清洗需要，也避免把模板变成
// 可执行脚本。replace 使用单引号参数，与前端规则构建器展示的写法保持一致。
var templateExprRe = regexp.MustCompile(`\{([a-z_]+)(?:\.replace\('([^']*)',\s*'([^']*)'\)|\.(lower|upper)\(\))\}`)

func addExpressionReplacements(template string, replacements map[string]string) {
	for _, match := range templateExprRe.FindAllStringSubmatch(template, -1) {
		base, ok := replacements["{"+match[1]+"}"]
		if !ok {
			continue
		}
		value := base
		switch match[4] {
		case "lower":
			value = strings.ToLower(value)
		case "upper":
			value = strings.ToUpper(value)
		default:
			value = strings.ReplaceAll(value, match[2], match[3])
		}
		replacements[match[0]] = value
	}
}

// processBlocks 处理 <> 块语法
// 语法：<任意文字{variable}任意文字> — 块内所有 {variable} 非空时输出整块，有空变量时丢弃整块
// 多变量块：<.{a}{b}> — a 和 b 都非空才输出
// 可嵌套变量：<({resource_effect}).{video_encode}> — effect 和 encode 都非空才输出
func processBlocks(template string, replacements map[string]string) string {
	for {
		// 找最深层的 <...> 块（不包含其他 <）
		start := -1
		replaced := false
		for i := 0; i < len(template); i++ {
			if template[i] == '<' && (i+1 >= len(template) || template[i+1] != '<') {
				start = i
			}
			if template[i] == '>' && start >= 0 {
				block := template[start+1 : i]
				// 检查块内是否有 {variable}
				if !strings.Contains(block, "{") {
					start = -1
					continue
				}
				// 替换块内所有变量
				blockContent := block
				allNonEmpty := true
				for k, v := range replacements {
					if strings.Contains(blockContent, k) {
						if v == "" {
							allNonEmpty = false
							break
						}
						blockContent = strings.ReplaceAll(blockContent, k, v)
					}
				}
				// 还有没有未替换的 {xxx}（可能是嵌套变量）
				if strings.Contains(blockContent, "{") {
					// 保守：假设未替换的变量为空
					allNonEmpty = false
				}
				if allNonEmpty {
					template = template[:start] + blockContent + template[i+1:]
				} else {
					template = template[:start] + template[i+1:]
				}
				// 模板长度已变，当前索引失效——必须从头重扫（原实现 continue
				// 带旧索引继续，插入内容更长时会错配）
				replaced = true
				break
			}
		}
		if replaced {
			continue // 重新从头扫描
		}
		if start >= 0 {
			// 残留未闭合的 '<'（用户模板少写了 '>'）：按字面量剔除并结束。
			// 原实现此处不改 template，外层 for 原样重扫 → 100% CPU 死循环
			template = strings.ReplaceAll(template, "<", "")
		}
		break
	}
	return template
}

// allReplacements 返回全部变量映射
func (ctx *RenameContext) allReplacements() map[string]string {
	return map[string]string{
		// 原始文件信息
		"{original_name}":      ctx.OriginalName,
		"{ext}":                ctx.Ext,
		"{custom_regex_match}": ctx.CustomRegex,

		// TMDB 信息
		"{title}":        ctx.Media.Title,
		"{en_title}":     ctx.Media.OriginalTitle,
		"{year}":         ctx.Media.Year,
		"{tmdb_id}":      fmt.Sprintf("%d", ctx.Media.TmdbID),
		"{first_letter}": titleFirstLetter(ctx.Media.Title),

		// 资源信息
		"{resource_pix}":     ctx.Resource.Pix,
		"{resource_version}": ctx.Resource.Version,
		"{resource_source}":  ctx.Resource.Source,
		"{resource_type}":    ctx.Resource.Type,
		"{resource_effect}":  ctx.Resource.Effect,
		"{video_encode}":     ctx.Resource.VideoEncode,
		"{audio_encode}":     ctx.Resource.AudioEncode,
		"{resource_team}":    ctx.Resource.Team,
		"{fps}":              ctx.Resource.FPS,
		"{disc_num}":         ctx.Resource.DiscNum,

		// 季集信息
		"{season_episode}": ctx.seasonEpisode(),
		"{season_num}":     fmt.Sprintf("%d", ctx.Parsed.Season),
		"{episode_num}":    fmt.Sprintf("%d", ctx.Parsed.Episode),
		"{season_name}":    ctx.SeasonName,
		"{season_year}":    ctx.SeasonYear,
		"{episode_name}":   ctx.EpisodeName,
	}
}

// seasonEpisode 返回 SxxExx 格式
func (ctx *RenameContext) seasonEpisode() string {
	if ctx.Parsed.Season > 0 && ctx.Parsed.Episode > 0 {
		return fmt.Sprintf("S%02dE%02d", ctx.Parsed.Season, ctx.Parsed.Episode)
	}
	if ctx.Parsed.Season > 0 {
		return fmt.Sprintf("S%02d", ctx.Parsed.Season)
	}
	return ""
}

// LoadRenameTemplates 从配置加载重命名模板（yaml 优先，DB 回退）
func (h *Handler) LoadRenameTemplates() *RenameConfig {
	cfg := defaultRenameConfig()

	v := h.getSettingValue("org-rename")
	if v == "" {
		return cfg // 使用默认
	}

	var saved RenameConfig
	if err := json.Unmarshal([]byte(v), &saved); err == nil {
		mergeRenameConfig(cfg, &saved)
	}
	return cfg
}

// BuildPathWithTemplate 用模板引擎生成完整目标路径
// 返回: 文件夹路径/文件名（不含扩展名已含在文件名模板中）
func (h *Handler) BuildPathWithTemplate(media *TmdbMedia, parsed *ParsedName, originalName string) string {
	ctx := buildRenameContext(media, parsed, originalName)
	tpl := h.LoadRenameTemplates()

	var folder, file string
	switch media.MediaType {
	case "movie":
		folder = ctx.ApplyTemplate(tpl.MovieFolder)
		file = ctx.ApplyTemplate(tpl.MovieFile)
	case "tv":
		folder = ctx.ApplyTemplate(tpl.TVFolder)
		file = ctx.ApplyTemplate(tpl.TVFile)
	default:
		return ""
	}

	return folder + "/" + file
}

// sanitizePath 清理路径中的空段和连续分隔符
//
// 逐段剪首尾的 . - 空格：ApplyTemplate 的 Trim 只够到整串两端，模板里带
// 层级时（默认剧集文件夹是 "标题.年份/Season N"）中间那段缺变量就会留下
// 尾巴，建出 "钢铁侠." 这种目录
func sanitizePath(p string) string {
	parts := strings.Split(p, "/")
	var cleaned []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimSpace(strings.Trim(part, ".-"))
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/")
}
