package api

// 分享 STRM 进海报墙（SHARE-STRM-PLAN.md §8）：本地整理认出的分享，按「分享根/分类/标题目录」认成片目，
// 海报墙列表、详情抽屉、勾选刮削都看得到。
//
// 只在海报墙这几处并进来（posterLedgerTitles / posterVideoRowsByTitle / scrapeDirVideoRows），
// **不进 scanLedgerTitles**：订阅按它判「库里已有」（分享随时会失效，不能算有）、总览按它数片目、
// 分类改名按它查影响、媒体信息补全按它挑要补的（补刮、提前探测，每次探测都要取一次分享直链）。
// 台账行是现拼的（FileID 带 s: 前缀、PickCode 是 s~ 直链标识），不写库。
// 原样放的分享认不出是哪部，不进海报墙。

import (
	"path"
	"strings"

	"115-station/internal/model"
)

// shareLayoutLib 分享根在片目认定上的样子：只认本地整理放的分类目录
func shareLayoutLib(cfg shareStrmCfg) (layoutLib, bool) {
	if model.DB == nil || shareRootProblem(cfg.Root, loadLibraries()) != "" {
		return layoutLib{}, false
	}
	ll := layoutLib{local: cfg.Root, depth: 1}
	if cfg.SchemeID != 0 {
		var rules []model.CategoryRule
		model.DB.Where("scheme_id = ? AND media_type IN ?", cfg.SchemeID, []string{"movie", "tv"}).Find(&rules)
		if len(rules) > 0 {
			ll.cats = buildLibCategoryLayout(rules).cats
		}
	}
	if ll.cats == nil {
		// 没选方案（或方案没规则）：整理只分电影 / 剧集两个目录（shareCategoryOf 返回空 → categoryDir 的类型目录）
		ll.cats = map[string]string{mediaTypeCategory("movie"): "movie", mediaTypeCategory("tv"): "tv"}
	}
	return ll, true
}

// shareLedgerRows 本地整理的分享里的视频，拼成台账行的样子（RelPath 相对媒体根）
func shareLedgerRows(cfg shareStrmCfg) []model.SyncedFile {
	if model.DB == nil {
		return nil
	}
	var rows []struct {
		model.ShareFile
		ShareCode string
	}
	model.DB.Table("share_files").
		Select("share_files.*, share_sources.share_code").
		Joins("JOIN share_sources ON share_sources.id = share_files.source_id").
		Where("share_sources.layout = ? AND share_files.kind = ? AND share_files.blocked = ?", shareLayoutOrganize, "video", false).
		Scan(&rows)
	out := make([]model.SyncedFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.SyncedFile{FileID: "s:" + r.FileID, PickCode: sharePlayID(r.ShareCode, r.FileID),
			Sha1: r.Sha1, Size: r.Size, Kind: "video", RelPath: path.Join(cfg.Root, r.RelPath), CreatedAt: r.CreatedAt})
	}
	return out
}

// scanShareTitles 分享 STRM 的片目（key 与台账片目同口径：媒体根下的相对路径）
func scanShareTitles() map[string]*ledgerTitleEntry {
	cfg := loadShareStrmCfg()
	lib, ok := shareLayoutLib(cfg)
	if !ok {
		return nil
	}
	return aggregateLedgerTitles(libCategoryLayout{libs: []layoutLib{lib}}, shareLedgerRows(cfg))
}

// posterLedgerTitles 海报墙用的片目：台账的 + 分享 STRM 的（新 map，不改台账缓存）
func posterLedgerTitles(fresh bool) map[string]*ledgerTitleEntry {
	base := scanLedgerTitlesCached()
	if fresh {
		base = scanLedgerTitles()
	}
	share := scanShareTitles()
	if len(share) == 0 {
		return base
	}
	out := make(map[string]*ledgerTitleEntry, len(base)+len(share))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range share {
		if _, dup := out[k]; !dup { // 和库里的片目撞 key 不会发生（分享根不许和库重叠），保险起见库优先
			out[k] = v
		}
	}
	return out
}

// isShareTitleKey 片目 key 在分享 STRM 根下
func isShareTitleKey(key string) bool {
	root := loadShareStrmCfg().Root
	key = strings.Trim(key, "/")
	return root != "" && (key == root || strings.HasPrefix(key, root+"/"))
}

// shareVideoRowsOf 分享片目里的视频行（scrapeDirVideoRows 的分享版）
func shareVideoRowsOf(key string) []model.SyncedFile {
	base := strings.Trim(key, "/")
	var out []model.SyncedFile
	for _, sf := range shareLedgerRows(loadShareStrmCfg()) {
		if strings.HasPrefix(sf.RelPath, base+"/") {
			out = append(out, sf)
		}
	}
	return out
}

// shareVideoRowsByTitle 海报墙卡片：分享片目 → 视频行
func shareVideoRowsByTitle() map[string][]model.SyncedFile {
	cfg := loadShareStrmCfg()
	lib, ok := shareLayoutLib(cfg)
	if !ok {
		return nil
	}
	layout := libCategoryLayout{libs: []layoutLib{lib}}
	out := map[string][]model.SyncedFile{}
	for _, sf := range shareLedgerRows(cfg) {
		if key, _, _, _, ok := layout.titleOf(sf.RelPath); ok {
			out[key] = append(out[key], sf)
		}
	}
	return out
}
