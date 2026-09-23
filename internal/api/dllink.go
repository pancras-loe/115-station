package api

// ==================== 来源链接 ====================
//
// 磁力/ed2k/HTTP 离线下载与 115 分享转存，提交时把原始链接落进 DownloadLink；
// 内容被整理时，整理记录按产物认领它（OrganizeRecord.LinkID），记录页据此显示
// 「这部片是哪条链接下来的」。链接本身不单独成页：下载/转存失败由提交处当场
// 回复或离线监视器推通知，不靠翻记录发现。
//
// ⚠️ **认领产物不允许新增任何 115 请求**，只能用已经在手的数据：
//   - 离线任务：监视器本来就在 30 秒轮询任务列表（改动前就在转），
//     顺手摘 file_id（产物落在转存目录里的 fid）与任务名
//   - 分享转存：/share/snap 的返回里本来就有顶层条目名，转存后 115 保留原名
//
// 于是认领分两级：fid 精确对账（离线），名字兜底（分享，以及 115 偶尔不回
// file_id 的离线任务）。都对不上就让那条整理记录没有来源，不硬凑。
//
// 全部操作失败只记日志：这是旁路账本，不能把下载/转存/整理主流程拖下水。

import (
	"encoding/json"
	"log"
	"net/url"
	"path"
	"strings"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

// downloadLinkKeepDays 保留天数（随每日 prune 清理，与整理记录同节奏）
const downloadLinkKeepDays = 90

// dlClaimWindow 认领时间窗：只有这段时间内提交的链接会被整理记录认领。
// 太长会让同名内容错认到陈年旧链接上
const dlClaimWindow = 14 * 24 * time.Hour

// downloadLinkName 只取链接自带的文件名；磁力名称由现有任务监视器回填。
func downloadLinkName(raw string) string {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "ed2k://") {
		parts := strings.Split(raw, "|")
		if len(parts) >= 3 {
			if name, err := url.QueryUnescape(parts[2]); err == nil && name != "" {
				return name
			}
			return parts[2]
		}
		return ""
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ftp://") {
		if u, err := url.Parse(raw); err == nil {
			if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
				if name, err := url.QueryUnescape(base); err == nil {
					return name
				}
				return base
			}
		}
	}
	return ""
}

// linkHashOf 取链接指纹：磁力 btih / ed2k 文件 hash / 分享 share_code。
// 这是与 115 离线任务列表 info_hash 对账的键；HTTP/FTP 链接没有指纹，返回空
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

// dlLinkRecord 提交成功后登记一行。kind 为空则按链接形态自动判定；
// name 提交时取得到多少算多少（ed2k/http 有文件名，磁力没有，靠监视器回填）；
// names 是已知的产物名清单（分享转存从 /share/snap 拿，离线为空）
func dlLinkRecord(h *Handler, rawURL, kind, name, source string, names []string) {
	if h == nil || h.DB == nil || rawURL == "" {
		return
	}
	if kind == "" {
		kind = classifyLink(rawURL)
	}
	if name == "" {
		name = downloadLinkName(rawURL)
	}
	row := model.DownloadLink{
		Kind: kind, URL: truncateStr(rawURL, 1000), Hash: linkHashOf(rawURL),
		Name: truncateStr(name, 500), Source: source, ResultNames: marshalStrs(names),
	}
	if err := h.DB.Create(&row).Error; err != nil {
		log.Printf("[来源链接] ○ 登记失败（不影响下载）: %v", err)
	}
}

// dlLinkSyncTask 离线监视器回填：按 info_hash（磁力 btih / ed2k hash）或任务名
// 定位记录行，补上 115 才知道的任务名与产物 fid。
// 找不到对应行说明这个任务不是本项目提交的（用户在 115 App 里加的），跳过。
// 数据全部来自监视器已有的那次轮询，不额外请求 115
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
		if fids := unmarshalStrs(row.ResultFids); !containsStr(fids, t.fileID) {
			upd["result_fids"] = marshalStrs(append(fids, t.fileID))
		}
	}
	// 任务名同时也是产物名（磁力下的目录名 = 种子名），存一份给名字兜底认领用
	if t.name != "" {
		if names := unmarshalStrs(row.ResultNames); !containsStr(names, t.name) {
			upd["result_names"] = marshalStrs(append(names, t.name))
		}
	}
	if len(upd) == 0 {
		return
	}
	if err := h.DB.Model(&model.DownloadLink{}).Where("id = ?", row.ID).Updates(upd).Error; err != nil {
		log.Printf("[来源链接] ○ 离线任务回填失败: %v", err)
	}
}

// dlLinkFindByTask 按任务指纹定位记录行：info_hash 优先，任务名兜底
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

// dlLinkMatch 给整理记录找它的来源链接，返回 DownloadLink.ID（0 = 认不出来）。
//
// 只看认领窗口内的链接，新的优先：同名内容重复提交时归到最近那一次。
// 一条链接能被多条记录认领（合集分享拆成几部片、散文件逐集各一条），
// 纯本地 DB 操作，不碰 115
func dlLinkMatch(db *gorm.DB, rec *model.OrganizeRecord) uint {
	if db == nil || rec == nil {
		return 0
	}
	var rows []model.DownloadLink
	if err := db.Where("created_at > ?", time.Now().Add(-dlClaimWindow)).
		Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		return 0
	}
	// 待匹配的 fid：记录自身 + 记录里每个文件（115 给的是目录 fid 时，
	// 整理散文件的记录 SourceFid 是文件本身，两头都要能对上）
	fids := map[string]bool{rec.SourceFid: true}
	for _, f := range unmarshalRecordFiles(rec.Files) {
		fids[f.Fid] = true
	}
	src := strings.TrimSuffix(strings.TrimSpace(rec.Source), "/")
	for _, row := range rows {
		for _, f := range unmarshalStrs(row.ResultFids) {
			if f != "" && fids[f] {
				return row.ID
			}
		}
	}
	if src == "" {
		return 0
	}
	for _, row := range rows {
		for _, n := range unmarshalStrs(row.ResultNames) {
			if strings.TrimSuffix(n, "/") == src {
				return row.ID
			}
		}
		if row.Name != "" && row.Name == src {
			return row.ID
		}
	}
	return 0
}

// recordLinks 给一页整理记录批量取来源链接（按 LinkID 一次 IN 查询）
func recordLinks(db *gorm.DB, recs []model.OrganizeRecord) map[uint]*model.DownloadLink {
	ids := make([]uint, 0, len(recs))
	for _, r := range recs {
		if r.LinkID != 0 {
			ids = append(ids, r.LinkID)
		}
	}
	out := map[uint]*model.DownloadLink{}
	if len(ids) == 0 || db == nil {
		return out
	}
	var rows []model.DownloadLink
	if err := db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out
}

// pruneDownloadLinks 清理过期记录（每日 prune 调用）
func pruneDownloadLinks() {
	if model.DB == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -downloadLinkKeepDays)
	res := model.DB.Where("created_at < ?", cutoff).Delete(&model.DownloadLink{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[来源链接] 清理 %d 条 %d 天前的记录", res.RowsAffected, downloadLinkKeepDays)
	}
}

func marshalStrs(list []string) string {
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func unmarshalStrs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
