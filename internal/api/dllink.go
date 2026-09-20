package api

// ==================== 下载链接台账 ====================
//
// 磁力/ed2k/HTTP 离线下载与 115 分享转存，提交时把原始链接落进 DownloadLink，
// 之后回答两个问题：
//
//  1. 离线任务页「这条任务是哪个链接提交的」——115 的任务列表本身带 url 字段，
//     台账主要补 115 侧任务被清理后的历史，以及分享转存（不产生离线任务）
//  2. 整理记录页「这批内容是从哪个链接来的」——靠 fid 对账：
//     离线任务完成后 115 返回 file_id（产物落在转存目录里的 fid），
//     分享转存则取 receive 前后目录快照的差集；整理写记录时 SourceFid
//     命中台账就把链接冗余进 OrganizeRecord
//
// 全部操作失败只记日志：台账是旁路账本，不能把下载/转存/整理主流程拖下水。

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"strmhub/internal/model"

	"gorm.io/gorm"
)

// downloadLinkKeepDays 台账保留天数（随每日 prune 清理，与整理记录同节奏）
const downloadLinkKeepDays = 90

// linkHashOf 取链接指纹：磁力 btih / ed2k 文件 hash / 分享 share_code。
// 这是与 115 任务列表 info_hash 对账的键；HTTP/FTP 链接没有指纹，返回空
func linkHashOf(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "magnet:?"):
		if m := reMagnetBtih.FindStringSubmatch(raw); m != nil {
			return strings.ToUpper(m[1])
		}
	case strings.HasPrefix(lower, "ed2k://"):
		if parts := strings.Split(raw, "|"); len(parts) >= 5 {
			return strings.ToUpper(parts[4])
		}
	}
	if code := extractShareCode(raw); code != "" {
		return code
	}
	return ""
}

// dlLinkRecord 提交成功后登记台账。kind 为空则按链接形态自动判定；
// name 提交时取得到多少算多少（ed2k/http 有文件名，磁力没有，靠回填补）。
// 返回台账行 id（0 = 落库失败，调用方无需处理）
func dlLinkRecord(h *Handler, rawURL, kind, name, targetCid, source string) uint {
	if h == nil || h.DB == nil || rawURL == "" {
		return 0
	}
	if kind == "" {
		kind = classifyLink(rawURL)
	}
	if name == "" {
		name, _ = offlinePlayNameSize(rawURL)
	}
	row := model.DownloadLink{
		Kind: kind, URL: truncateStr(rawURL, 1000), Hash: linkHashOf(rawURL),
		Name: truncateStr(name, 500), TargetCid: targetCid,
		Source: source, Status: "submitted",
	}
	if err := h.DB.Create(&row).Error; err != nil {
		log.Printf("[台账] ○ 下载链接登记失败（不影响下载）: %v", err)
		return 0
	}
	return row.ID
}

// dlLinkSetFids 回填产物 fid（分享转存用：receive 前后快照的差集）
func dlLinkSetFids(h *Handler, id uint, fids []string) {
	if h == nil || h.DB == nil || id == 0 || len(fids) == 0 {
		return
	}
	b, _ := json.Marshal(fids)
	if err := h.DB.Model(&model.DownloadLink{}).Where("id = ?", id).
		Updates(map[string]interface{}{"result_fids": string(b), "status": "done"}).Error; err != nil {
		log.Printf("[台账] ○ 产物 fid 回填失败: %v", err)
	}
}

// dlLinkSyncTask 离线监视器回填：按 info_hash（磁力 btih / ed2k hash）或任务名
// 定位台账行，补上 115 才知道的任务名、产物 fid 与终态。
// 找不到对应行说明这个任务不是 115-Station 提交的（用户在 115 App 里加的），跳过
func dlLinkSyncTask(h *Handler, t offlineTaskInfo) {
	if h == nil || h.DB == nil {
		return
	}
	row, err := dlLinkFindByTask(h.DB, t)
	if err != nil {
		return
	}
	upd := map[string]interface{}{}
	if t.name != "" && t.name != row.Name {
		upd["name"] = truncateStr(t.name, 500)
	}
	if t.fileID != "" {
		if fids := unmarshalFids(row.ResultFids); !containsStr(fids, t.fileID) {
			b, _ := json.Marshal(append(fids, t.fileID))
			upd["result_fids"] = string(b)
		}
	}
	status := row.Status
	switch t.status {
	case 2:
		status = "done"
	case -1:
		status = "failed"
	case 1:
		status = "downloading"
	}
	if status != row.Status {
		upd["status"] = status
		if status == "failed" {
			upd["note"] = "115 离线任务报错（资源失效或任务被拒）"
		}
	}
	if len(upd) == 0 {
		return
	}
	if err := h.DB.Model(&model.DownloadLink{}).Where("id = ?", row.ID).Updates(upd).Error; err != nil {
		log.Printf("[台账] ○ 离线任务回填失败: %v", err)
	}
}

// dlLinkFindByTask 按任务指纹定位台账行：info_hash 优先，任务名兜底
// （HTTP/FTP 任务没有 hash，115 用链接里的文件名做任务名）
func dlLinkFindByTask(db *gorm.DB, t offlineTaskInfo) (model.DownloadLink, error) {
	var row model.DownloadLink
	if t.key != "" {
		if err := db.Where("hash = ?", strings.ToUpper(t.key)).
			Order("id DESC").First(&row).Error; err == nil {
			return row, nil
		}
	}
	if t.name != "" {
		if err := db.Where("name = ?", t.name).Order("id DESC").First(&row).Error; err == nil {
			return row, nil
		}
	}
	return row, gorm.ErrRecordNotFound
}

// linkLedger 台账的内存索引：离线任务列表一次性把近期台账读进来，
// 按 hash / 名字给 115 没回 url 的任务兜底补链接（逐条查库会放大 N 倍开销）
type linkLedger struct {
	byHash map[string]model.DownloadLink
	byName map[string]model.DownloadLink
}

func newLinkLedger(h *Handler) *linkLedger {
	l := &linkLedger{byHash: map[string]model.DownloadLink{}, byName: map[string]model.DownloadLink{}}
	if h == nil || h.DB == nil {
		return l
	}
	var rows []model.DownloadLink
	if err := h.DB.Order("id DESC").Limit(500).Find(&rows).Error; err != nil {
		return l
	}
	// 倒序读入、正序覆盖：同一 hash 命中多行时留下最新的那条
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Hash != "" {
			l.byHash[strings.ToUpper(rows[i].Hash)] = rows[i]
		}
		if rows[i].Name != "" {
			l.byName[rows[i].Name] = rows[i]
		}
	}
	return l
}

// lookup 按 info_hash 优先、任务名兜底取链接；查不到返回空串
func (l *linkLedger) lookup(hash, name string) (link, kind string) {
	if l == nil {
		return "", ""
	}
	if hash != "" {
		if row, ok := l.byHash[strings.ToUpper(hash)]; ok {
			return row.URL, row.Kind
		}
	}
	if name != "" {
		if row, ok := l.byName[name]; ok {
			return row.URL, row.Kind
		}
	}
	return "", ""
}

// dlLinkByFids 按产物 fid 反查来源链接（整理写记录时调用）。
// fids 传「被整理的目录/散文件 fid + 目录内文件 fid」，任一命中即算同一来源：
// 磁力下的是目录时 115 给的是目录 fid，分享转存快照差集给的也是顶层 fid，
// 但整理散文件时记录的 SourceFid 是文件本身，两头都要能对上
func dlLinkByFids(db *gorm.DB, fids []string) (link, kind string) {
	if db == nil || len(fids) == 0 {
		return "", ""
	}
	// 只在最近的台账里找：fid 是 115 全局唯一的，理论上不会撞，
	// 但限定范围能让这条查询在台账长大后依然是常数开销
	var rows []model.DownloadLink
	if err := db.Where("result_fids <> ''").Order("id DESC").Limit(500).Find(&rows).Error; err != nil {
		return "", ""
	}
	want := make(map[string]bool, len(fids))
	for _, f := range fids {
		if f != "" {
			want[f] = true
		}
	}
	for _, row := range rows {
		for _, f := range unmarshalFids(row.ResultFids) {
			if want[f] {
				return row.URL, row.Kind
			}
		}
	}
	return "", ""
}

// dlLinkByName 按名字兜底反查：磁力任务的产物目录名 = 种子名 = 115 任务名，
// 整理前还没改过名，所以 Source（原目录名/原文件名）通常与台账里的 Name 一致。
// 只在 fid 对不上时用（115 偶尔不回 file_id），且要求台账行近 7 天内提交
func dlLinkByName(db *gorm.DB, name string) (link, kind string) {
	name = strings.TrimSuffix(strings.TrimSpace(name), "/")
	if db == nil || name == "" {
		return "", ""
	}
	var row model.DownloadLink
	if err := db.Where("name = ? AND created_at > ?", name, time.Now().Add(-7*24*time.Hour)).
		Order("id DESC").First(&row).Error; err != nil {
		return "", ""
	}
	return row.URL, row.Kind
}

// fillRecordLink 给整理记录补来源链接：先按 fid 对账（准），再按名字兜底（糙）。
// 记录里已有链接（重新整理沿用）则不动
func fillRecordLink(db *gorm.DB, rec *model.OrganizeRecord) {
	if db == nil || rec == nil || rec.SourceLink != "" {
		return
	}
	fids := []string{rec.SourceFid}
	for _, f := range unmarshalRecordFiles(rec.Files) {
		fids = append(fids, f.Fid)
	}
	if link, kind := dlLinkByFids(db, fids); link != "" {
		rec.SourceLink, rec.SourceLinkKind = link, kind
		return
	}
	rec.SourceLink, rec.SourceLinkKind = dlLinkByName(db, rec.Source)
}

// pruneDownloadLinks 清理过期台账（每日 prune 调用）
func pruneDownloadLinks() {
	if model.DB == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -downloadLinkKeepDays)
	res := model.DB.Where("created_at < ?", cutoff).Delete(&model.DownloadLink{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[台账] 清理 %d 条 %d 天前的下载链接", res.RowsAffected, downloadLinkKeepDays)
	}
}

func unmarshalFids(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
