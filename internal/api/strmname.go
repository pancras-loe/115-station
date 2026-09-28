package api

import (
	"path"
	"path/filepath"
	"strings"

	"115-station/internal/model"
)

// ==================== STRM 文件名 ====================
//
// STRM 以「视频去掉扩展名 + .strm」命名：海绵宝宝.S01E09.mp4 → 海绵宝宝.S01E09.strm。
//
// 2026-09-28 之前是「视频全名 + .strm」（海绵宝宝.S01E09.mp4.strm）。Emby 找配套文件时
// 把 STRM 的扩展名换掉再找，于是只认 xxx.mp4.nfo / xxx.mp4-thumb.jpg / xxx.mp4.chs.ass，
// 网盘上跟视频同基名的 xxx.nfo / xxx-thumb.jpg / xxx.chs.ass 同步下来全都对不上号。
// 去掉视频扩展名之后，网盘名、本地名与 Emby 期望的名字三边一致
// （同 p123strmhelper 的 file_path.stem + ".strm"）。存量由 strmmigrate.go 在启动时一次性迁移。
//
// 同一目录里有同基名的两个视频（X.mkv 与 X.mp4）时，后来的那个退回旧写法 X.mkv.strm，
// 见 strmNameFor。凡是要从视频名推 STRM 名的地方都走这里，不要再手拼 name + ".strm"。

// strmNameOf 视频文件名 → STRM 文件名（不考虑同名冲突）
func strmNameOf(videoName string) string {
	return strmStemOf(videoName) + ".strm"
}

// strmStemOf 视频文件名去掉视频扩展名（不是视频扩展名的原样返回）。
// 也是本地配套文件（NFO / 缩略图 / 字幕）的基名
func strmStemOf(videoName string) string {
	ext := strings.ToLower(pathExt(videoName))
	if videoExts[ext] && len(videoName) > len(ext) {
		return videoName[:len(videoName)-len(ext)]
	}
	return videoName
}

// legacyStrmNameOf 旧写法：视频全名 + .strm。同名冲突时的退路，也是迁移与按名兜底时要认的形态
func legacyStrmNameOf(videoName string) string {
	return videoName + ".strm"
}

// strmRelOf 视频的库内相对路径 → STRM 相对路径（不考虑同名冲突）
func strmRelOf(videoRel string) string {
	return path.Join(path.Dir(videoRel), strmNameOf(path.Base(videoRel)))
}

// strmRelCandidates 一个视频的 STRM 可能叫的两个名字（新写法在前）。
// 按视频名反查本地产物时两种都要认：同名冲突的那个是旧写法
func strmRelCandidates(videoRel string) []string {
	n, l := strmRelOf(videoRel), videoRel+".strm"
	if n == l {
		return []string{n}
	}
	return []string{n, l}
}

// strmNameFor 这个视频实际该写成的 STRM 文件名（按台账裁决，model.DB 为空时直接用新写法）：
//
//  1. 台账里这个 fid 已有同目录的视频行 → 沿用它的名字。已落盘的文件名字不会因为
//     换了写入路径（全量 / 增量 / 整理）而来回变；
//  2. 新写法的名字已被台账里**别的、未失效的**文件占着 → 同目录另有同基名的视频（X.mkv 与 X.mp4），
//     这个退回旧写法 X.mkv.strm；
//  3. 否则用新写法。
//
// 同一批里两个同名视频都还没进台账，那种情况由调用方另行去重（applySyncResults 的 claimed）
func strmNameFor(f remoteFile) string {
	name := strmNameOf(f.Name)
	legacy := legacyStrmNameOf(f.Name)
	if name == legacy || model.DB == nil {
		return name
	}
	if f.Fid != "" {
		var own model.SyncedFile
		if model.DB.Where("file_id = ?", f.Fid).First(&own).Error == nil && path.Dir(own.RelPath) == f.Path {
			if b := path.Base(own.RelPath); b == name || b == legacy {
				return b
			}
		}
	}
	var other model.SyncedFile
	if model.DB.Where("rel_path = ? AND file_id <> ? AND orphan_at IS NULL", path.Join(f.Path, name), f.Fid).
		First(&other).Error == nil {
		return legacy
	}
	return name
}

// isVideoName 按扩展名判断是不是视频（决定要不要按 STRM 形态去找本地产物）
func isVideoName(name string) bool {
	return videoExts[strings.ToLower(pathExt(name))]
}

// strmOwnedByOther 台账里 rel 这一行是不是别的文件的（fid 不同）。
// 按视频名推新写法的 STRM 路径时用：同目录同基名的另一个视频可能正占着这个名字，
// 删它就删错了。台账里没有这一行、或不知道 fid 时按「不是别人的」处理（与改造前一致）
func (h *Handler) strmOwnedByOther(rel, fid string) bool {
	if rel == "" || fid == "" || h.DB == nil {
		return false
	}
	var sf model.SyncedFile
	if h.DB.Where("rel_path = ?", rel).First(&sf).Error != nil {
		return false
	}
	return sf.FileID != fid
}

// localRelOrEmpty 本地绝对路径 → 相对库根的 / 分隔路径，越界返回空
func localRelOrEmpty(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}
