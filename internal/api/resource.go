package api

// ==================== 资源信息结构化解析（CMS 同款变量体系） ====================
//
// 从原始文件名中提取结构化资源信息，支持以下变量（与 CMS 对齐）：
//   {resource_pix}      分辨率    2160p / 1080p / 720p
//   {resource_version}  资源版本  IMAX / HQ / 3D / CC / DC / Extended
//   {resource_source}   来源平台  NF / DSNP / AMZN / MAX / BGLOBAL / UHD
//   {resource_type}     资源质量  BluRay / WEB-DL / HDTV / REMUX
//   {resource_effect}   特效      DV.HDR / DV / HDR10+ / HDR / SDR
//   {video_encode}      视频编码  H265.10bit / x264 / HEVC / REMUX
//   {audio_encode}      音频编码  TrueHD.7.1 / AAC2.0 / DTS-HD.MA.5.1
//   {resource_team}     发布组    TnT / FRDS / LNT / CHDWEB
//   {fps}               帧率      60FPS / 23.976FPS
//   {disc_num}          盘号      Disc1 / D2

import (
	"regexp"
	"strings"
)

// ResourceInfo 从文件名中提取的结构化资源信息
type ResourceInfo struct {
	Pix         string // 分辨率
	Version     string // 资源版本（IMAX/HQ/3D/CC/DC/Extended/Unrated/Remastered）
	Source      string // 来源平台（NF/DSNP/AMZN/MAX/HULU/BGLOBAL/UHD等）
	Type        string // 资源质量（BluRay/WEB-DL/HDTV/REMUX）
	Effect      string // 特效（DV.HDR/DV/HDR10+/HDR/SDR）
	VideoEncode string // 视频编码（H265.10bit/x264/HEVC）
	AudioEncode string // 音频编码（TrueHD.7.1/AAC2.0/DTS-HD.MA.5.1）
	Team        string // 发布组（最后一个-后面的部分）
	FPS         string // 帧率（60FPS/23.976FPS）
	DiscNum     string // 盘号（Disc1/D1）
}

var (
	rePix     = regexp.MustCompile(`(?i)\b(4320p|2160p|1440p|1080[pi]|720p|576p|480p|4k|8k)\b`)
	reVersion = regexp.MustCompile(`(?i)\b(IMAX|HQ|3D|CC|DC|EXTENDED|EXT|UNRATED|REMASTERED|THEATRICAL|DIRECTOR.?S.?CUT|COMPLETE|HYBRID|ITUNES)\b`)
	reSource  = regexp.MustCompile(`(?i)\b(NF|NETFLIX|DSNP|DISNEY|AMZN|AMAZON|ATVP|APPLE.?TV|MAX|HBO|HULU|PMTP|PARAMOUNT|PCOK|PEACOCK|BGLOBAL|B.?GLOBAL|CR|CRUNCHYROLL|IQ|IQIYI|YOUKU|TENCENT|MGTV|BILI|UHD|WEB)\b`)
	reType    = regexp.MustCompile(`(?i)\b(BLURAY|BLU.?RAY|BLU.?R|WEB.?DL|WEB.?RIP|HDTV|DVDRIP|DVDSCR|CAM|TS|TC|R5|REMUX|PROPER|REPACK)\b`)
	reEffect  = regexp.MustCompile(`(?i)\b(DV|DOLBY.?VISION|DOVI|HDR10\+?|HDR\+?|HDR|SDR|SL.?HDR|DOLBY)(?:[.\s_+-]|$)`)
	reVideo   = regexp.MustCompile(`(?i)\b(H\.?26[45]|X26[45]|HEVC|AVC|XVID|DIVX|AV1|VP9)\b`)
	reBit     = regexp.MustCompile(`(?i)\b(10bit|8bit|12bit)\b`)
	reAudio   = regexp.MustCompile(`(?i)\b(AAC[\d.]*|AC3|EAC3|DD[P+]?[\d.]*|DD[\d.]*|DTS.?HD.?MA[\d.]*|DTS.?HD[\d.]*|DTS.?X[\d.]*|DTS[\d.]*|TRUEHD[\d.]*|ATMOS|FLAC|PCM|LPCM|LPCM[\d.]*|[\d]\.[\d])\b`)
	reFPS     = regexp.MustCompile(`(?i)\b([\d.]+)\s*FPS\b`)
	reDisc    = regexp.MustCompile(`(?i)\b(DISC[\d]+|D[\d])\b`)
	reTeam    = regexp.MustCompile(`-([A-Za-z0-9@]+)$`)
)

// ParseResourceInfo 从文件名解析完整的资源信息
func ParseResourceInfo(filename string) ResourceInfo {
	var ri ResourceInfo
	upper := strings.ToUpper(filename)

	// 分辨率
	if m := rePix.FindStringSubmatch(filename); m != nil {
		ri.Pix = normalizePix(m[1])
	}

	// 资源版本（可能有多个，取全部）
	var versions []string
	for _, m := range reVersion.FindAllStringSubmatch(filename, -1) {
		v := strings.ToUpper(m[1])
		v = strings.ReplaceAll(v, " ", "")
		if !containsStr(versions, v) {
			versions = append(versions, v)
		}
	}
	ri.Version = strings.Join(versions, ".")

	// 来源平台
	if m := reSource.FindStringSubmatch(filename); m != nil {
		ri.Source = normalizeSource(m[1])
	}

	// 资源质量
	if m := reType.FindStringSubmatch(filename); m != nil {
		ri.Type = normalizeType(m[1])
	}
	// Source/Type 去重：WEB-DL 会被 source 的 WEB 前缀抢先命中，造成
	// ".WEB.WEB-DL" 重复——Type 已包含 Source 时丢弃 Source
	if ri.Source != "" && ri.Type != "" && strings.Contains(strings.ToUpper(ri.Type), strings.ToUpper(ri.Source)) {
		ri.Source = ""
	}

	// 特效（可能组合：DV.HDR）
	var effects []string
	for _, m := range reEffect.FindAllStringSubmatch(filename, -1) {
		e := strings.ToUpper(m[1])
		e = strings.ReplaceAll(strings.ReplaceAll(e, "DOLBY VISION", "DV"), "DOLBY", "DV")
		e = strings.ReplaceAll(e, " ", "")
		if e == "HDR" || e == "DV" || e == "SDR" || strings.HasPrefix(e, "HDR") || strings.HasPrefix(e, "DV") {
			if !containsStr(effects, e) {
				effects = append(effects, e)
			}
		}
	}
	ri.Effect = strings.Join(effects, ".")

	// 视频编码（含位深）
	videoParts := []string{}
	if m := reVideo.FindStringSubmatch(filename); m != nil {
		videoParts = append(videoParts, normalizeVideoEncode(m[1]))
	}
	if m := reBit.FindStringSubmatch(filename); m != nil {
		videoParts = append(videoParts, m[1])
	}
	if strings.Contains(upper, "REMUX") {
		videoParts = append(videoParts, "REMUX")
	}
	ri.VideoEncode = strings.Join(videoParts, ".")

	// 音频编码（可能组合：TrueHD.7.1 Atmos）
	audioParts := []string{}
	for _, m := range reAudio.FindAllStringSubmatch(filename, -1) {
		// [\d.]* 贪婪会把后续分隔符一并吃进（如 "DDP.7.1."），先去掉尾部
		// 分隔符再归一化，否则 "7.1." 的声道判定失败被丢弃
		a := normalizeAudioEncode(strings.TrimRight(m[1], "."))
		if a != "" && !containsStr(audioParts, a) {
			audioParts = append(audioParts, a)
		}
	}
	if strings.Contains(upper, "ATMOS") {
		has := false
		for _, a := range audioParts {
			if strings.EqualFold(a, "ATMOS") {
				has = true
				break
			}
		}
		if !has {
			audioParts = append(audioParts, "ATMOS")
		}
	}
	ri.AudioEncode = strings.Join(audioParts, ".")

	// 发布组（最后一个 - 后面的部分）
	base := filename
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx] // 去扩展名
	}
	if m := reTeam.FindStringSubmatch(base); m != nil {
		team := m[1]
		// 排除误匹配（纯数字或太短）
		if len(team) >= 2 && !isAllDigits(team) {
			ri.Team = team
		}
	}

	// 帧率
	if m := reFPS.FindStringSubmatch(filename); m != nil {
		ri.FPS = m[1] + "FPS"
	}

	// 盘号
	if m := reDisc.FindStringSubmatch(filename); m != nil {
		ri.DiscNum = m[1]
	}

	return ri
}

// QualityString 生成完整的画质串（用于重命名）
// 格式：2160p.IMAX.BluRay.DV.HDR.H265.10bit.TrueHD.7.1.Atmos-TnT
func (ri ResourceInfo) QualityString() string {
	var parts []string
	if ri.Pix != "" {
		parts = append(parts, ri.Pix)
	}
	if ri.Version != "" {
		parts = append(parts, ri.Version)
	}
	if ri.Source != "" && ri.Source != "WEB" {
		parts = append(parts, ri.Source)
	}
	if ri.Type != "" {
		parts = append(parts, ri.Type)
	}
	if ri.Effect != "" {
		parts = append(parts, ri.Effect)
	}
	if ri.VideoEncode != "" {
		parts = append(parts, ri.VideoEncode)
	}
	if ri.AudioEncode != "" {
		parts = append(parts, ri.AudioEncode)
	}
	return strings.Join(parts, ".")
}

// FullString 含发布组的完整串
func (ri ResourceInfo) FullString() string {
	q := ri.QualityString()
	if ri.Team != "" {
		if q != "" {
			q += "-" + ri.Team
		} else {
			q = ri.Team
		}
	}
	return q
}

// --- 辅助归一化 ---

func normalizePix(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "i", "p") // 1080i → 1080p
	if s == "4k" {
		return "2160p"
	}
	if s == "8k" {
		return "4320p"
	}
	return s
}

func normalizeSource(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), " ", ""))
	switch upper {
	case "NETFLIX":
		return "NF"
	case "DISNEY":
		return "DSNP"
	case "AMAZON":
		return "AMZN"
	case "APPLETV", "APPLE.TV":
		return "ATVP"
	case "HBO":
		return "MAX"
	case "PARAMOUNT":
		return "PMTP"
	case "PEACOCK":
		return "PCOK"
	case "BGLOBAL", "B.GLOBAL":
		return "BGLOBAL"
	case "CRUNCHYROLL":
		return "CR"
	case "IQIYI":
		return "IQ"
	case "TENCENT":
		return "TX"
	case "BILI", "BILIBILI":
		return "BILI"
	default:
		return upper
	}
}

func normalizeType(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), "_", ""))
	switch upper {
	case "BLURAY", "BLU", "BLU-RAY", "BLUR":
		return "BluRay"
	case "WEB-DL", "WEBDL":
		return "WEB-DL"
	case "WEBRIP", "WEB-RIP":
		return "WEBRip"
	case "DVDRIP", "DVD":
		return "DVDRip"
	default:
		return strings.Title(strings.ToLower(s))
	}
}

func normalizeVideoEncode(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(s, ".", ""))
	switch {
	case strings.Contains(upper, "H264"):
		return "H264"
	case strings.Contains(upper, "H265"):
		return "H265"
	case strings.Contains(upper, "X264"):
		return "x264"
	case strings.Contains(upper, "X265"):
		return "x265"
	case strings.Contains(upper, "HEVC"):
		return "H265"
	case strings.Contains(upper, "AVC"):
		return "H264"
	case strings.Contains(upper, "AV1"):
		return "AV1"
	case strings.Contains(upper, "VP9"):
		return "VP9"
	case strings.Contains(upper, "XVID"):
		return "XviD"
	default:
		return strings.Title(strings.ToLower(s))
	}
}

func normalizeAudioEncode(s string) string {
	upper := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(upper, "TRUEHD"):
		return "TrueHD" + extractNumSuffix(s, "TrueHD")
	case strings.HasPrefix(upper, "DTS-HD-MA"), strings.HasPrefix(upper, "DTSHDMA"):
		return "DTS-HD.MA" + extractNumSuffix(s, "MA")
	case strings.HasPrefix(upper, "DTS-HD"), strings.HasPrefix(upper, "DTSHD"):
		return "DTS-HD" + extractNumSuffix(s, "HD")
	case strings.HasPrefix(upper, "DTS-X"):
		return "DTS.X"
	case strings.HasPrefix(upper, "DTS"):
		return "DTS" + extractNumSuffix(s, "DTS")
	case strings.HasPrefix(upper, "DDP"):
		return "DDP" + extractNumSuffix(s, "DDP")
	case strings.HasPrefix(upper, "DD+"):
		return "DD+" + extractNumSuffix(s, "DD+")
	case strings.HasPrefix(upper, "DD"):
		return "DD" + extractNumSuffix(s, "DD")
	case strings.HasPrefix(upper, "EAC3"):
		return "DDP" + extractNumSuffix(s, "EAC3")
	case strings.HasPrefix(upper, "AAC"):
		return "AAC" + extractNumSuffix(s, "AAC")
	case strings.HasPrefix(upper, "AC3"):
		return "DD" + extractNumSuffix(s, "AC3")
	case upper == "ATMOS":
		return "ATMOS"
	case upper == "FLAC":
		return "FLAC"
	case upper == "PCM" || upper == "LPCM":
		return "LPCM"
	default:
		if isAudioChannel(s) {
			return s // 5.1 / 7.1 / 2.0 等通道数
		}
		return ""
	}
}

// extractNumSuffix 提取编码名后面的数字后缀（如 TrueHD.7.1 → .7.1）
func extractNumSuffix(s, prefix string) string {
	rest := strings.TrimPrefix(strings.ToUpper(s), strings.ToUpper(prefix))
	rest = strings.TrimPrefix(rest, ".")
	if isAudioChannel(rest) {
		return "." + rest
	}
	return ""
}

// isAudioChannel 判断是否为声道数（如 5.1、7.1、2.0）
func isAudioChannel(s string) bool {
	return regexp.MustCompile(`^\d\.\d$`).MatchString(s)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
