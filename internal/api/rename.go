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
	"strings"
	"regexp"

	"strmhub/internal/model"
)

// RenameContext 重命名上下文（包含模板引擎需要的全部数据）
type RenameContext struct {
	Num         string // AV 番号（{num} 变量；AV 流程由 detectAVNumber 填充）
	OriginalName string
	Ext          string
	Media        *TmdbMedia
	Parsed       *ParsedName
	Resource     ResourceInfo
	SeasonName   string
	SeasonYear   string
	EpisodeName  string
	CustomRegex  string
	// MetaTube AV 元数据（缓存直读，绝不触发网络；整理流程已提前刮好入库）
	AvTitle string // 真实标题 {av_title}
	AvYear  string // 发行年份 {av_year}
	Actor   string // 第一主演 {actor}
	Actors  string // 全部主演（、连接）{actors}
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
	// AV 流程：番号就是 media.Title（detectAVNumber 的产出），供 {num} 使用
	if media.MediaType == "av" {
		ctx.Num = media.Title
		// MetaTube 刮削结果只从缓存读：整理流程在进模板前已通过
		// metatubeFetchCached 提前刮好入库；预览等场景查不到就保持空值，
		// 不发起网络请求
		if model.DB != nil {
			var av model.AVMeta
			if model.DB.Where("num = ? AND status = ?", normalizeAVNum(media.Title), "ok").First(&av).Error == nil {
				// 真实标题会进文件名，必须过 sanitizeName（日文标题常含 / : 等非法字符）
				ctx.AvTitle = sanitizeName(av.Title)
				ctx.AvYear = av.Year
				if actors := avMetaActors(&av); len(actors) > 0 {
					ctx.Actor = sanitizeName(actors[0])
					for i, a := range actors {
						actors[i] = sanitizeName(a)
					}
					ctx.Actors = strings.Join(actors, "、")
				}
			}
		}
	}
	return ctx
}

// ApplyTemplate 应用重命名模板，替换所有变量
// 支持块语法：{xxx非空则输出xxx包裹的内容}
func (ctx *RenameContext) ApplyTemplate(template string) string {
	result := template

	replacements := ctx.allReplacements()

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

	return result
}

// parenValueRe 匹配 (.xxx) 形式（括号内有点+值）→ 提取 .xxx（去掉括号）
var parenValueRe = regexp.MustCompile(`\((\.[\w.-]+)\)`)

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
		"{original_name}":     ctx.OriginalName,
		"{ext}":              ctx.Ext,
		"{custom_regex_match}": ctx.CustomRegex,
		"{num}":              ctx.Num,

		// TMDB 信息
		"{title}":       ctx.Media.Title,
		"{en_title}":    ctx.Media.OriginalTitle,
		"{year}":        ctx.Media.Year,
		"{tmdb_id}":     fmt.Sprintf("%d", ctx.Media.TmdbID),
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

		// AV 元数据（MetaTube 刮削；未刮到时为空，配合 <> 块语法使用）
		"{av_title}": ctx.AvTitle,
		"{av_year}":  ctx.AvYear,
		"{actor}":    ctx.Actor,
		"{actors}":   ctx.Actors,
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
	cfg := &RenameConfig{
		MovieFolder:   "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		MovieFile:   "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
		TVFolder:   "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
		TVFile:   "{title} - {season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}",
		// AV 命名规范 = 番号 + AV 标题（"ABC-123 XXXXXX"），不带画质附加信息
		AVFolder:   "{first_letter}-{num}",
		AVFile:   "{num}< {av_title}>{ext}",
	}

	v := h.getSettingValue("org-rename")
	if v == "" {
		return cfg // 使用默认
	}

	var saved RenameConfig
	if err := json.Unmarshal([]byte(v), &saved); err == nil {
		if saved.MovieFolder != "" {
			cfg.MovieFolder = saved.MovieFolder
		}
		if saved.MovieFile != "" {
			cfg.MovieFile = saved.MovieFile
		}
		if saved.TVFolder != "" {
			cfg.TVFolder = saved.TVFolder
		}
		if saved.TVFile != "" {
			cfg.TVFile = saved.TVFile
		}
		if saved.AVFolder != "" {
			cfg.AVFolder = saved.AVFolder
		}
		if saved.AVFile != "" {
			cfg.AVFile = saved.AVFile
		}
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
	default: // AV 等
		folder = ctx.ApplyTemplate(tpl.AVFolder)
		file = ctx.ApplyTemplate(tpl.AVFile)
	}

	return collapseDuplicateAVNum(folder+"/"+file, ctx.Num)
}

// collapseDuplicateAVNum AV 兜底：番号在结果中背靠背重复（如历史模板
// {num}-{title} 渲染出 "ABC-123-ABC-123"）时折叠为单个。AV 流程里
// {num} 与 {title} 同值，模板同用两个变量必产生重复
func collapseDuplicateAVNum(s, num string) string {
	if num == "" || !strings.Contains(s, num) {
		return s
	}
	q := regexp.QuoteMeta(num)
	return regexp.MustCompile(`(` + q + `)[-_. ]?` + q).ReplaceAllString(s, num)
}

// sanitizePath 清理路径中的空段和连续分隔符
func sanitizePath(p string) string {
	parts := strings.Split(p, "/")
	var cleaned []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/")
}
