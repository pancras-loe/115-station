package api

// ==================== 下载记录 ====================
//
// 磁力/ed2k/HTTP 离线下载与 115 分享转存，提交时把原始链接落进 DownloadLink；
// 内容被整理入库后，把识别结果（片名 / 年份 / TMDB id / 分类 / 落库目录）
// 回写到同一行。一条链接一行，「提交了什么」与「最后成了哪部片」并排放着。
//
// ⚠️ **认领产物不允许新增任何 115 请求**，只能用已经在手的数据：
//   - 离线任务：监视器本来就在 30 秒轮询任务列表（改动前就在转），
//     顺手摘 file_id（产物落在转存目录里的 fid）与任务名
//   - 分享转存：/share/snap 的返回里本来就有顶层条目名，转存后 115 保留原名
//
// 于是认领分两级：fid 精确对账（离线），名字兜底（分享，以及 115 偶尔不回
// file_id 的离线任务）。都对不上就让这一行停在「未认领」，不硬凑。
//
// 全部操作失败只记日志：记录是旁路账本，不能把下载/转存/整理主流程拖下水。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// downloadLinkKeepDays 记录保留天数（随每日 prune 清理，与整理记录同节奏）
const downloadLinkKeepDays = 90

// dlClaimWindow 认领时间窗：只有这段时间内提交的链接会被整理结果认领。
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
// names 是已知的产物名清单（分享转存从 /share/snap 拿，离线为空）。
// 返回记录行 id（0 = 落库失败，调用方无需处理）
func dlLinkRecord(h *Handler, rawURL, kind, name, targetCid, source, status string, names []string) uint {
	if h == nil || h.DB == nil || rawURL == "" {
		return 0
	}
	if kind == "" {
		kind = classifyLink(rawURL)
	}
	if name == "" {
		name = downloadLinkName(rawURL)
	}
	row := model.DownloadLink{
		Kind: kind, URL: truncateStr(rawURL, 1000), Hash: linkHashOf(rawURL),
		Name: truncateStr(name, 500), TargetCid: targetCid,
		Source: source, Status: status, ResultNames: marshalStrs(names),
	}
	if err := h.DB.Create(&row).Error; err != nil {
		log.Printf("[下载记录] ○ 登记失败（不影响下载）: %v", err)
		return 0
	}
	return row.ID
}

// dlLinkSyncTask 离线监视器回填：按 info_hash（磁力 btih / ed2k hash）或任务名
// 定位记录行，补上 115 才知道的任务名、产物 fid 与下载状态。
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
		log.Printf("[下载记录] ○ 离线任务回填失败: %v", err)
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

// dlLinkClaim 整理结果认领：一条整理记录写完后调用，把识别结果回写到
// 对应的下载记录行。先按 fid 精确对账，再按产物名兜底；都对不上就不动。
// 纯本地 DB 操作，不碰 115
func dlLinkClaim(db *gorm.DB, rec *model.OrganizeRecord) {
	if db == nil || rec == nil {
		return
	}
	row, ok := dlLinkMatch(db, rec)
	if !ok {
		return
	}
	now := time.Now()
	upd := map[string]interface{}{
		"organize_status": rec.Status,
		"organized_at":    &now,
		"record_id":       rec.ID,
		"tmdb_id":         rec.TmdbID,
		"title":           rec.Title,
		"year":            rec.Year,
		"media_type":      rec.MediaType,
		"poster_path":     rec.PosterPath,
		"category":        rec.Category,
		"target_dir":      rec.TargetDir,
	}
	if err := db.Model(&model.DownloadLink{}).Where("id = ?", row.ID).Updates(upd).Error; err != nil {
		log.Printf("[下载记录] ○ 整理结果回写失败: %v", err)
	}
}

// dlLinkMatch 给整理记录找它的来源链接行。
//
// 认领窗口内、且「还没认领到成功结果」的行才参与：同一条链接可能先失败
// 后重做，后一次成功应当覆盖前一次的失败；但已经认成功的行不再被别的
// 整理记录抢走
func dlLinkMatch(db *gorm.DB, rec *model.OrganizeRecord) (model.DownloadLink, bool) {
	var rows []model.DownloadLink
	if err := db.Where("created_at > ? AND organize_status <> ?", time.Now().Add(-dlClaimWindow), "success").
		Order("id DESC").Limit(200).Find(&rows).Error; err != nil {
		return model.DownloadLink{}, false
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
				return row, true
			}
		}
	}
	if src == "" {
		return model.DownloadLink{}, false
	}
	for _, row := range rows {
		for _, n := range unmarshalStrs(row.ResultNames) {
			if strings.TrimSuffix(n, "/") == src {
				return row, true
			}
		}
		if row.Name != "" && row.Name == src {
			return row, true
		}
	}
	return model.DownloadLink{}, false
}

// pruneDownloadLinks 清理过期记录（每日 prune 调用）
func pruneDownloadLinks() {
	if model.DB == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -downloadLinkKeepDays)
	res := model.DB.Where("created_at < ?", cutoff).Delete(&model.DownloadLink{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[下载记录] 清理 %d 条 %d 天前的记录", res.RowsAffected, downloadLinkKeepDays)
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

// ==================== 下载记录接口 ====================

// ListDownloadLinks GET /download/links?status=&q=&page=&size=
// status: all / pending（还没整理出结果）/ organized（已识别入库）/ failed（下载失败）
func (h *Handler) ListDownloadLinks(c *gin.Context) {
	q := h.DB.Model(&model.DownloadLink{})
	switch strings.TrimSpace(c.Query("status")) {
	case "", "all":
	case "pending":
		q = q.Where("organize_status = '' AND status <> ?", "failed")
	case "organized":
		q = q.Where("organize_status = ?", "success")
	case "failed":
		// 下载失败与整理没成（失败/未识别）都算「需要处理」
		q = q.Where("status = ? OR organize_status IN ?", "failed", []string{"failed", "unrecognized"})
	}
	if kw := strings.TrimSpace(c.Query("q")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("url LIKE ? OR name LIKE ? OR title LIKE ?", like, like, like)
	}
	var total int64
	q.Count(&total)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	var rows []model.DownloadLink
	q.Order("created_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "size": size})
}

// DeleteDownloadLink DELETE /download/links/:id（只删记录，网盘内容不受影响）
func (h *Handler) DeleteDownloadLink(c *gin.Context) {
	if err := h.DB.Delete(&model.DownloadLink{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ClearDownloadLinks POST /download/links/clear  body: {"status":"all|organized|failed"}
func (h *Handler) ClearDownloadLinks(c *gin.Context) {
	var req struct {
		Status string `json:"status"`
	}
	_ = c.ShouldBindJSON(&req)
	q := h.DB.Session(&gorm.Session{AllowGlobalUpdate: true})
	switch req.Status {
	case "", "all":
	case "organized":
		q = q.Where("organize_status = ?", "success")
	case "failed":
		q = q.Where("status = ? OR organize_status IN ?", "failed", []string{"failed", "unrecognized"})
	}
	res := q.Delete(&model.DownloadLink{})
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清空 %d 条记录", res.RowsAffected), "removed": res.RowsAffected})
}
