package api

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ==================== 接管已有 STRM：认出别的项目写的直链指向哪个网盘文件 ====================
//
// 设计见 docs/115-station-notes/ADOPT-STRM-PLAN.md §1、§2.2。各项目的写法 2026-10-17 对照源码核实过：
//
//	p115strmhelper  {MP}/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=X[&file_name=…]
//	                URL 模板可只给 file_id；分享 STRM 是 ?share_code=&receive_code=&id=（utils/strm.py get_strm_url）
//	qmediasync      {base}/115/url/video.mkv?pickcode=X&userid=Y[&path=网盘完整路径]（driver_115.go MakeStrmContent）
//	MediaSync115    {base}/api/115/url/video.mkv?pickcode=X（strm_service.py build_play_url）
//	本站 / STRMhub  {domain}/d/{pickcode}[.ext][?/文件名]（full115.go writeStrmNamed）
//	openStrm        {前缀}/{网盘路径}，没有 pickcode（naming.ts strmContent）；前缀常见 OpenList 的 /d
//	挂载式          /mnt/xxx/网盘路径（CloudDrive / rclone 挂载出来的本地路径）
//
// 认的顺序是 pickcode → 文件 id → 网盘路径：pickcode 与 id 和账号绑定、换了名字也认得出，
// 路径只在前两样都没有（或对不上，比如换了账号）时兜底。

// foreignRef 一个 STRM 指向的网盘文件
type foreignRef struct {
	// Kind 主要凭据：pickcode / fileid / path / share（分享 STRM，不是本账号的文件）/ unknown
	Kind string `json:"kind"`
	// Format 看着像哪个项目写的，只用于报告里的统计
	Format   string `json:"format"`
	PickCode string `json:"pickcode,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	// PanPath 内容里带的网盘路径（已解码，以 / 开头）；qmediasync 的 &path= 与 pickcode 同时存在
	PanPath string `json:"pan_path,omitempty"`
	// 分享 STRM（Kind = share）：分享码、提取码、分享里的文件 id（本站的 /d/s~ 没有提取码）
	ShareCode   string `json:"share_code,omitempty"`
	ReceiveCode string `json:"receive_code,omitempty"`
	ShareFid    string `json:"share_fid,omitempty"`
}

const (
	strmFmtStation  = "station" // 本站或上游 STRMhub 的 /d/{pickcode}
	strmFmtHelper   = "p115strmhelper"
	strmFmtQms      = "qmediasync"
	strmFmtMs115    = "mediasync115"
	strmFmtOpenList = "openlist" // /d/网盘路径（OpenList / Alist，openStrm 也常配这个前缀）
	strmFmtMount    = "mount"    // 本地挂载路径
	strmFmtOther    = "other"
)

var reDigits = regexp.MustCompile(`^[0-9]+$`)

// validFilePickcode 像不像一个文件的 pickcode：小写字母数字、a~e 开头（目录是 fa~fe）、能解出 id。
// pcPrefixOf 本身很宽松（只看首字符），单靠它会把 OpenList 路径里的「abc」之类当成 pickcode
func validFilePickcode(s string) bool {
	if len(s) < 8 || len(s) > 40 || s[0] < 'a' || s[0] > 'e' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(pcAlphabet, s[i]) < 0 {
			return false
		}
	}
	_, err := pickcodeToID115(s)
	return err == nil
}

// parseForeignStrm 认出 STRM 内容指向哪个网盘文件（纯函数）
func parseForeignStrm(content string) foreignRef {
	line := firstStrmLine(content)
	if line == "" {
		return foreignRef{Kind: "unknown", Format: strmFmtOther}
	}
	lower := strings.ToLower(line)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		if strings.HasPrefix(line, "/") {
			return foreignRef{Kind: "path", Format: strmFmtMount, PanPath: cleanPanPath(line)}
		}
		return foreignRef{Kind: "unknown", Format: strmFmtOther}
	}
	u, err := url.Parse(line)
	if err != nil {
		return foreignRef{Kind: "unknown", Format: strmFmtOther}
	}
	ref := foreignRef{Format: urlFormatOf(u.Path)}
	q := u.Query()
	if q.Get("share_code") != "" {
		ref.Kind = "share"
		ref.ShareCode, ref.ReceiveCode, ref.ShareFid = q.Get("share_code"), q.Get("receive_code"), q.Get("id")
		return ref
	}
	// 本站分享 STRM 的 /d/s~{分享码}~{fid}（sharestrm.go）：也是分享，不是本账号的 pickcode
	if seg, ok := strings.CutPrefix(u.Path, "/d/"); ok && isSharePlayID(seg) {
		if code, fid, ok := parseSharePlayID(strings.SplitN(seg, "/", 2)[0]); ok {
			ref.Kind, ref.Format, ref.ShareCode, ref.ShareFid = "share", strmFmtStation, code, fid
			return ref
		}
	}

	// pickcode：查询串，或本站的 /d/{pickcode}[.ext]
	for _, k := range []string{"pickcode", "pick_code", "pc"} {
		if v := strings.ToLower(strings.TrimSpace(q.Get(k))); validFilePickcode(v) {
			ref.PickCode = v
			break
		}
	}
	if ref.PickCode == "" {
		if pc, ok := stationPickcode(u.Path); ok {
			ref.PickCode = pc
			ref.Format = strmFmtStation
		}
	}
	// 文件 id：p115strmhelper 的 URL 模板可以只给 file_id
	for _, k := range []string{"file_id", "fid", "id"} {
		if v := strings.TrimSpace(q.Get(k)); reDigits.MatchString(v) && v != "0" {
			ref.FileID = v
			break
		}
	}
	// 网盘路径：qmediasync 的 &path=；没有任何编号时整条 URL 路径就是网盘路径（OpenList / openStrm）
	if p := strings.TrimSpace(q.Get("path")); strings.HasPrefix(p, "/") {
		ref.PanPath = cleanPanPath(p)
	} else if ref.PickCode == "" && ref.FileID == "" && ref.Format != strmFmtHelper && ref.Format != strmFmtQms && ref.Format != strmFmtMs115 {
		p := u.Path
		if ref.Format == strmFmtOpenList {
			p = strings.TrimPrefix(p, "/d")
		}
		if path.Ext(p) != "" {
			ref.PanPath = cleanPanPath(p)
		}
	}

	switch {
	case ref.PickCode != "":
		ref.Kind = "pickcode"
	case ref.FileID != "":
		ref.Kind = "fileid"
	case ref.PanPath != "":
		ref.Kind = "path"
	default:
		ref.Kind = "unknown"
	}
	return ref
}

// firstStrmLine 第一行有内容、不是注释的行（个别播放列表式 STRM 会带 #EXTM3U）
func firstStrmLine(content string) string {
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "\uFEFF"))
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// urlFormatOf 按 URL 路径认是哪个项目写的
func urlFormatOf(p string) string {
	lp := strings.ToLower(p)
	switch {
	case strings.Contains(lp, "/p115strmhelper/"):
		return strmFmtHelper
	case strings.HasPrefix(lp, "/api/115/url/"): // 要在 qmediasync 之前判：路径里也含 /115/url/
		return strmFmtMs115
	case strings.HasPrefix(lp, "/115/url/"):
		return strmFmtQms
	case strings.HasPrefix(lp, "/d/"):
		return strmFmtOpenList // 是本站的 /d/{pickcode} 时由 stationPickcode 改判
	}
	return strmFmtOther
}

// stationPickcode 本站直链的路径恰好两段：/d/{pickcode}[.ext]。
// 多于两段的 /d/... 是 OpenList 的网盘路径，第二段是目录名，别当成 pickcode
func stationPickcode(p string) (string, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) != 2 || segs[0] != "d" {
		return "", false
	}
	pc := strings.ToLower(segs[1])
	if i := strings.IndexByte(pc, '.'); i > 0 {
		pc = pc[:i]
	}
	if validFilePickcode(pc) {
		return pc, true
	}
	return "", false
}

// cleanPanPath 统一成以 / 开头、没有多余斜杠的路径；URL 已由 url.Parse 解码，
// 挂载路径与 &path= 里个别项目会再编码一层，解得开就解
func cleanPanPath(p string) string {
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if p == "/" {
		return ""
	}
	return p
}
