package api

// 癫影发布分享（shareout.go 调）。2026-10-17 读站点前端（ResourceDetail-*.js / tag-*.js，只读，Go 重写）得到：
//
//   - 发布 = POST /api/portal/share，走 dianweb.go 的签名调用器。人机验证开着（/auth/policy 的 turnstile_enabled）时
//     前端会带 turnstile_token，这里不做，直接跳过这次发布并写明原因。
//   - 载荷照详情页的「115 分享」：share_kind=115 + share_code + receive_code（**访问码必填**，前端没有访问码不让发），
//     影视关联：站内已有这一部（resource-detail 的 resource.id，剧集还要季对得上）带 resource_id + kind，否则 kind=tmdb + TMDB 字段。
//   - 前端的硬校验照抄一遍、先在本地拦：总大小 > 0、有分辨率、有文件格式、剧集要有具体集号、不收枪版。
//   - tag 是前端 Y() 拼的「DT2|4K|WEB-DL|HEVC|…」：每一项归一成站点的写法、去重、按类别排序。只放认得出的项。
//   - 重复链接回 code=share_code_duplicate；首次发布有 first_publish_reward。
//   - 离线链接（2026-11-02 读 ResourceDetail-CNb-r4n7.js / offline-DkwSPTp2.js）：表单「磁链/ED2K」页签，同一个接口，
//     share_kind=offline + offline_type（ed2k / magnet）+ share_code=链接原文（几条换行，最多 500 条、一批只能同一种）+ offline_title，
//     不要访问码；其余校验（大小、分辨率、格式、集号、枪版）与 115 分享相同。

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// dianTagCats 站点的类别顺序（tag-*.js 的 B）与类别内顺序（C）。只收本站能产出的那几项
var (
	dianTagCatOrder = map[string]int{"resolution": 10, "source": 20, "platform": 30, "video_codec": 40, "video_bit": 50,
		"effect": 60, "audio": 70, "frame_rate": 80, "subtitle": 90, "file": 120}
	dianTagCat = map[string]string{
		"8K": "resolution", "4K": "resolution", "1440P": "resolution", "1080P": "resolution", "720P": "resolution", "SD": "resolution",
		"UHD-BLURAY": "source", "BLURAY": "source", "BLURAY-REMUX": "source", "REMUX": "source", "WEB-DL": "source", "WEBRIP": "source",
		"HDTV": "source", "DVDRIP": "source",
		"NF": "platform", "AMZN": "platform", "DSNP": "platform", "ATVP": "platform", "HMAX": "platform", "HULU": "platform", "CR": "platform", "B-GLOBAL": "platform",
		"HEVC": "video_codec", "AVC": "video_codec", "AV1": "video_codec", "VP9": "video_codec", "MPEG2": "video_codec", "VC1": "video_codec",
		"8BIT": "video_bit", "10BIT": "video_bit", "12BIT": "video_bit",
		"DV": "effect", "HDR10+": "effect", "HDR10": "effect", "HDR-VIVID": "effect", "HLG": "effect",
		"TRUEHD": "audio", "TRUEHD7.1": "audio", "TRUEHD5.1": "audio", "DTS-HD.MA": "audio", "DTS-HD.MA7.1": "audio", "DTS-HD.MA5.1": "audio",
		"DTS-X": "audio", "DTS": "audio", "DDP": "audio", "DDP5.1": "audio", "EAC3": "audio", "AC3": "audio", "AAC": "audio", "AAC2.0": "audio",
		"FLAC": "audio", "LPCM": "audio", "ATMOS": "audio",
		"23.976FPS": "frame_rate", "24FPS": "frame_rate", "25FPS": "frame_rate", "30FPS": "frame_rate", "50FPS": "frame_rate", "60FPS": "frame_rate", "120FPS": "frame_rate",
		"EXT-SUB": "subtitle",
		"MKV":     "file", "MP4": "file", "ISO": "file", "M2TS": "file", "TS": "file", "MOV": "file", "AVI": "file", "WMV": "file", "WEBM": "file", "FLV": "file", "MPG": "file",
	}
	dianTagRank = map[string]float64{"8K": 1, "4K": 2, "1440P": 2.5, "1080P": 3, "720P": 5, "SD": 6,
		"UHD-BLURAY": 1, "BLURAY-REMUX": 2, "REMUX": 3, "BLURAY": 4, "WEB-DL": 5, "WEBRIP": 6, "HDTV": 8, "DVDRIP": 10,
		"HEVC": 1, "AVC": 2, "AV1": 3, "VP9": 4, "12BIT": 1, "10BIT": 2, "8BIT": 3,
		"DV": 1, "HDR10+": 2, "HDR10": 3, "HDR-VIVID": 4, "HLG": 5, "ATMOS": 99}
)

// dianTagTokens 本站的画质信息 → 站点写法的各项（认不出的不放）
func dianTagTokens(ri ResourceInfo, ext string, hasSubs bool) []string {
	var out []string
	add := func(t string) {
		if _, ok := dianTagCat[t]; ok {
			out = append(out, t)
		}
	}
	switch strings.ToLower(ri.Pix) {
	case "4320p":
		add("8K")
	case "2160p":
		add("4K")
	case "1440p":
		add("1440P")
	case "1080p":
		add("1080P")
	case "720p":
		add("720P")
	case "480p", "576p":
		add("SD")
	}
	isRemux := strings.EqualFold(ri.Type, "REMUX") || strings.Contains(strings.ToUpper(ri.VideoEncode), "REMUX")
	switch t := strings.ToUpper(ri.Type); {
	case isRemux:
		add("REMUX")
	case t == "BLURAY" && strings.EqualFold(ri.Source, "UHD"):
		add("UHD-BLURAY")
	case t == "BLURAY":
		add("BLURAY")
	case t == "WEB-DL", t == "WEBRIP", t == "HDTV", t == "DVDRIP":
		add(t)
	}
	switch strings.ToUpper(ri.Source) {
	case "NF", "AMZN", "DSNP", "ATVP", "HULU", "CR":
		add(strings.ToUpper(ri.Source))
	case "MAX":
		add("HMAX")
	case "BGLOBAL":
		add("B-GLOBAL")
	}
	for _, part := range strings.Split(strings.ToUpper(ri.VideoEncode), ".") {
		switch part {
		case "H265", "HEVC", "X265":
			add("HEVC")
		case "H264", "AVC", "X264":
			add("AVC")
		case "AV1", "VP9":
			add(part)
		case "MPEG2", "MPEG-2":
			add("MPEG2")
		case "VC1", "VC-1":
			add("VC1")
		case "8BIT", "10BIT", "12BIT":
			add(part)
		}
	}
	for _, e := range strings.Split(strings.ToUpper(ri.Effect), ".") {
		switch e {
		case "DV", "DOVI":
			add("DV")
		case "HDR10+":
			add("HDR10+")
		case "HDR", "HDR10":
			add("HDR10")
		case "HLG":
			add("HLG")
		}
	}
	if a := dianAudioToken(ri.AudioEncode); a != "" {
		add(a)
	}
	if strings.Contains(strings.ToUpper(ri.AudioEncode), "ATMOS") {
		add("ATMOS")
	}
	if fps := strings.ToUpper(ri.FPS); fps != "" {
		add(fps)
	}
	if hasSubs {
		add("EXT-SUB")
	}
	add(strings.ToUpper(ext))
	return out
}

// dianAudioToken 音频编码 → 站点写法：带声道的只有站点列出来的那几种（TRUEHD7.1、DDP5.1…），其余只写编码
func dianAudioToken(audio string) string {
	a := strings.ToUpper(audio)
	if a == "" {
		return ""
	}
	first := strings.SplitN(a, ".ATMOS", 2)[0]
	compact := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(first)
	ch := ""
	for _, c := range []string{"7.1", "5.1", "2.0"} {
		if strings.Contains(first, c) {
			ch = c
			break
		}
	}
	var codec string
	switch {
	case strings.HasPrefix(compact, "TRUEHD"):
		codec = "TRUEHD"
	case strings.HasPrefix(compact, "DTSHD") && strings.Contains(compact, "MA"), strings.HasPrefix(compact, "DTSHDMA"):
		codec = "DTS-HD.MA"
	case strings.HasPrefix(compact, "DTSX"), strings.HasPrefix(compact, "DTS:X"):
		return "DTS-X"
	case strings.HasPrefix(compact, "DTS"):
		return "DTS"
	case strings.HasPrefix(compact, "DDP"), strings.HasPrefix(compact, "DD+"):
		codec = "DDP"
	case strings.HasPrefix(compact, "EAC3"):
		return "EAC3"
	case strings.HasPrefix(compact, "AC3"):
		return "AC3"
	case strings.HasPrefix(compact, "AAC"):
		codec = "AAC"
	case strings.HasPrefix(compact, "FLAC"):
		return "FLAC"
	case strings.HasPrefix(compact, "LPCM"), strings.HasPrefix(compact, "PCM"):
		return "LPCM"
	default:
		return ""
	}
	if ch != "" {
		if _, ok := dianTagCat[codec+ch]; ok {
			return codec + ch
		}
	}
	return codec
}

// dianTagString 拼成站点的 tag 串：去重、按类别 → 类别内顺序 → 字母排序，前缀 DT2（tag-*.js 的 Y / m）
func dianTagString(tokens []string) string {
	seen := map[string]bool{}
	var uniq []string
	for _, t := range tokens {
		if t != "" && !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	if len(uniq) == 0 {
		return ""
	}
	sort.SliceStable(uniq, func(i, j int) bool {
		a, b := uniq[i], uniq[j]
		if ca, cb := dianTagCatOrder[dianTagCat[a]], dianTagCatOrder[dianTagCat[b]]; ca != cb {
			return ca < cb
		}
		if ra, rb := dianTagRank[a], dianTagRank[b]; ra != rb {
			return ra < rb
		}
		return a < b
	})
	return "DT2|" + strings.Join(uniq, "|")
}

// dianResourceRef resource-detail 里站内已有的那一条（剧集只取单季、季号对得上的）
type dianResourceRef struct {
	ID     int    `json:"id"`
	Kind   string `json:"kind"`
	Season int    `json:"season"`
}

// dianShareBody 发布的请求体（纯函数）；校验不过返回 errShareOutSkip
func dianShareBody(pl shareOutPayload, cfg shareOutSiteCfg, res dianResourceRef) (map[string]any, error) {
	if pl.Offline == "" {
		if pl.ShareCode == "" {
			return nil, shareOutSkipf("不是 115 分享链接")
		}
		if pl.Code == "" {
			return nil, shareOutSkipf("癫影要求分享带访问码，这条分享没有")
		}
	}
	if shareOutBadSource(pl.Info) {
		return nil, shareOutSkipf("枪版资源，癫影不收")
	}
	if pl.SizeBytes <= 0 {
		return nil, shareOutSkipf("不知道总大小（整理记录里没有文件体积）")
	}
	tokens := dianTagTokens(pl.Info, pl.Ext, pl.HasSubs)
	hasRes, hasExt := false, false
	for _, t := range tokens {
		switch dianTagCat[t] {
		case "resolution":
			hasRes = true
		case "file":
			hasExt = true
		}
	}
	if !hasRes {
		return nil, shareOutSkipf("文件名里认不出分辨率，癫影要求必填")
	}
	if !hasExt {
		return nil, shareOutSkipf("认不出文件格式，癫影要求必填")
	}
	body := map[string]any{
		"share_kind":       "115",
		"share_code":       pl.ShareCode,
		"receive_code":     pl.Code,
		"total_size_bytes": pl.SizeBytes,
		"file_name":        pl.ShareName,
		"title_override":   shareOutHeadline(pl), // 站上列表显示的标题；file_name 留原分享名（前端拿它认格式）
		"file_extension":   strings.ToUpper(pl.Ext),
		"file_list":        pl.Files,
		"tag":              dianTagString(tokens),
		"subtitle_kind":    "",
	}
	if pl.Offline != "" {
		// 「磁链/ED2K」：链接原文放 share_code（几条换行），不要访问码；列表标题在 offline_title，
		// 前端不给离线分享填 title_override（ResourceDetail-*.js 的 is()）
		delete(body, "receive_code")
		delete(body, "title_override")
		body["share_kind"] = "offline"
		body["offline_type"] = pl.Offline
		body["share_code"] = pl.URL
		body["offline_title"] = shareOutHeadline(pl)
	}
	// external_chs 是「外挂中字」：字幕文件名认得出中文才这么标，认不出语言的不替它说是中字
	for _, l := range pl.SubLangs {
		if strings.HasPrefix(l, "简") || strings.HasPrefix(l, "繁") {
			body["subtitle_kind"] = "external_chs"
			break
		}
	}
	if cfg.UnlockPoints > 0 {
		body["unlock_cost_override"] = cfg.UnlockPoints
	}
	single := 0
	if pl.MediaType == "tv" {
		if len(pl.Seasons) == 0 {
			return nil, shareOutSkipf("认不出集号，癫影要求剧集写明具体集号")
		}
		var all []int
		for _, s := range pl.Seasons {
			all = append(all, pl.Episodes[s]...)
		}
		// 多季的集号按季拼不出一串（前端也只给一串），只有一季时才写具体集号
		if len(pl.Seasons) == 1 {
			single = pl.Seasons[0]
			body["episodes_csv"] = episodesCSV(all)
		} else {
			body["episodes_csv"] = episodesCSV(pl.Episodes[pl.Seasons[0]])
		}
		body["episode_count"] = len(all)
		var ss []string
		for _, s := range pl.Seasons {
			ss = append(ss, fmt.Sprint(s))
		}
		body["seasons_csv"] = strings.Join(ss, ",")
	}
	if res.ID > 0 && (pl.MediaType != "tv" || (single > 0 && single == res.Season)) {
		body["resource_id"], body["kind"] = res.ID, firstNonEmpty(res.Kind, "tmdb")
	} else {
		body["kind"] = "tmdb"
		body["tmdb_id"] = pl.TmdbID
		body["media_type"] = pl.MediaType
		body["title"] = pl.Title
		if pl.Poster != "" {
			body["poster_url"] = "https://image.tmdb.org/t/p/w500" + pl.Poster
		}
		if pl.MediaType == "tv" {
			body["season"] = single
		}
	}
	return body, nil
}

// dianPublishShare 发到癫影（shareOutPublishers 里注册）
func dianPublishShare(pl shareOutPayload, cfg shareOutSiteCfg) (string, int, error) {
	if !dianConfigured(loadDianCfg()) {
		return "", 0, shareOutSkipf("还没配置癫影账号")
	}
	// 先在本地校验一遍，校验不过就不开浏览器
	if _, err := dianShareBody(pl, cfg, dianResourceRef{}); err != nil {
		return "", 0, err
	}
	var ref string
	var points int
	err := dianWeb(fmt.Sprintf("发布分享 %s/%d", pl.MediaType, pl.TmdbID), true, 2*time.Minute, func(c *dianCaller) error {
		if raw, err := c.do("GET", "/auth/policy", nil); err == nil {
			var pol struct {
				Turnstile bool `json:"turnstile_enabled"`
			}
			if json.Unmarshal(raw, &pol) == nil && pol.Turnstile {
				return shareOutSkipf("癫影现在要求人机验证，本站暂时不能自动发布")
			}
		}
		var res dianResourceRef
		if raw, err := c.do("GET", "/resource-detail?key="+dianResourceKey(pl.MediaType, pl.TmdbID), nil); err == nil {
			var d struct {
				Resource dianResourceRef `json:"resource"`
			}
			_ = json.Unmarshal(raw, &d)
			res = d.Resource
		}
		body, err := dianShareBody(pl, cfg, res)
		if err != nil {
			return err
		}
		raw, err := c.do("POST", "/share", body)
		if err != nil {
			var ae *dianAPIErr
			if errors.As(err, &ae) {
				switch {
				case ae.Code == "share_code_duplicate":
					return shareOutSkipf("癫影上已经有这条分享")
				case ae.Status >= 400 && ae.Status < 500 && ae.Status != 429 && !ae.needLogin():
					return shareOutRefusal("癫影", ae.Error())
				}
			}
			return err
		}
		var out struct {
			ResourceID int `json:"resource_id"`
			ShareID    int `json:"share_id"`
			Reward     struct {
				Awarded bool `json:"awarded"`
				Points  int  `json:"points"`
				Amount  int  `json:"amount"`
			} `json:"first_publish_reward"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.ResourceID > 0 {
			ref = fmt.Sprint(out.ResourceID)
		}
		if out.Reward.Awarded {
			points = max(out.Reward.Points, out.Reward.Amount)
		}
		return nil
	})
	if err == nil {
		dianForgetLists()
	}
	return ref, points, err
}
