package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 失效 STRM 清理 ====================
//
// 失效 STRM（代码里沿用 orphan 命名，界面与日志一律叫「失效 STRM」）
// = 本地还有 strm/附属文件，但网盘上的源文件已经没了。
// 生活事件有窗口，增量同步停机、网页版批量删除、事件识别不出来，
// 都会漏掉删除动作，漏掉的就永远烂在库里——Emby 还显示着条目，点开播放 404。
//
// 全量同步本来就会拿到网盘当前的完整文件清单，和台账做一次差集就知道谁已失效。
// 但只标记不删：
//   - 清单不完整（翻页短缺/目录表缺项）时算出的差集是假的，一删就是真丢数据
//   - 用户改了扩展名配置（比如去掉 jpg）也会让老文件变成失效，这是预期行为但要让人看见
//
// 所以流程是：全量同步打标 → Strm 管理页显示「发现 N 个失效 STRM」+ 预览 → 用户点了才删。
// 检测打开后还能配一条全量 cron 定时刷新标记（见 cron.go 的 loadFullCron）。

// orphanSampleLimit 预览返回多少条（前端只做抽样展示，不下发全量清单）
const orphanSampleLimit = 50

// markOrphans 用本次扫描到的 fid 全集刷新台账的失效标记。
// libPrefix 为媒体库根目录名：台账 rel_path 的第一层就是它（见 libraryFilesOf），
// 据此把差集限定在本次同步的这个库内——否则同步 A 库会把 B 库的记录全判成失效。
// 返回 (新标记数, 恢复数, 当前失效总数)
func markOrphans(db *gorm.DB, libPrefix string, seen map[string]bool) (marked, cleared, total int) {
	if db == nil || libPrefix == "" {
		return 0, 0, 0
	}
	var rows []model.SyncedFile
	if err := db.Select("id", "file_id", "orphan_at").
		Where(`rel_path LIKE ? ESCAPE '\'`, likeEscape(libPrefix)+"/%").Find(&rows).Error; err != nil {
		log.Printf("[同步] 失效 STRM 标记：读取台账失败: %v", err)
		return 0, 0, 0
	}
	now := time.Now()
	var toMark, toClear []uint
	for _, r := range rows {
		switch {
		case seen[r.FileID]:
			if r.OrphanAt != nil {
				toClear = append(toClear, r.ID)
			}
		default:
			total++
			if r.OrphanAt == nil {
				toMark = append(toMark, r.ID)
			}
		}
	}
	// 分批更新：sqlite 对单条语句的参数个数有上限，万级库一次 IN 会炸
	for _, batch := range chunkIDs(toClear, 400) {
		db.Model(&model.SyncedFile{}).Where("id IN ?", batch).Update("orphan_at", nil)
	}
	for _, batch := range chunkIDs(toMark, 400) {
		db.Model(&model.SyncedFile{}).Where("id IN ?", batch).Update("orphan_at", now)
	}
	return len(toMark), len(toClear), total
}

// likeEscape 转义 LIKE 模式里的通配符。库名带 % 或 _ 时不转义会跨库匹配——
// 「电影_4K」的 _ 能匹配任意单字符，把「电影X4K」库的台账也拖进差集，
// 那些记录会被判成失效，用户一点清理就是真丢文件
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// chunkIDs 把 id 列表切成固定大小的批次
func chunkIDs(ids []uint, size int) [][]uint {
	var out [][]uint
	for len(ids) > size {
		out = append(out, ids[:size])
		ids = ids[size:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// orphanLocalRoot 本地媒体库根目录（失效条目的 rel_path 相对于它）
func (h *Handler) orphanLocalRoot() string {
	var cfg struct {
		LocalPath string `json:"local_path"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &cfg) == nil && cfg.LocalPath != "" {
		return cfg.LocalPath
	}
	return defaultLocalPath
}

// orphanDetectEnabled 用户是否在 Strm 管理页打开了失效 STRM 检测（默认关）
func (h *Handler) orphanDetectEnabled() bool {
	var cfg struct {
		DetectOrphans bool `json:"detect_orphans"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("full")), &cfg)
	return cfg.DetectOrphans
}

// ListOrphans 失效 STRM 预览。GET /sync/orphans
func (h *Handler) ListOrphans(c *gin.Context) {
	var total, ledger int64
	h.DB.Model(&model.SyncedFile{}).Where("orphan_at IS NOT NULL").Count(&total)
	h.DB.Model(&model.SyncedFile{}).Count(&ledger)

	var rows []model.SyncedFile
	h.DB.Where("orphan_at IS NOT NULL").Order("rel_path").Limit(orphanSampleLimit).Find(&rows)
	sample := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		sample = append(sample, gin.H{
			"rel_path": r.RelPath, "kind": r.Kind, "size": r.Size,
			"marked_at": r.OrphanAt.Format("2006-01-02 15:04"),
		})
	}

	ratio := 0.0
	if ledger > 0 {
		ratio = float64(total) / float64(ledger)
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":      h.orphanDetectEnabled(),
		"total":        total,
		"ledger_total": ledger,
		"ratio":        ratio,
		"sample":       sample,
		"sample_limit": orphanSampleLimit,
	})
}

// CleanOrphans 删除已标记的失效 STRM（本地文件 + 台账记录）。POST /sync/orphans/clean
// 只动 orphan_at 非空的记录——这些是上一次「完整」扫描确认过网盘已无源文件的
func (h *Handler) CleanOrphans(c *gin.Context) {
	root := h.orphanLocalRoot()
	var rows []model.SyncedFile
	if err := h.DB.Where("orphan_at IS NOT NULL").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取失效 STRM 列表失败: " + err.Error()})
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "没有待清理的失效 STRM", "removed": 0})
		return
	}

	removed, missing, failed := 0, 0, 0
	var doneIDs []uint
	for _, r := range rows {
		full := filepath.Join(root, filepath.FromSlash(r.RelPath))
		err := os.Remove(full)
		switch {
		case err == nil:
			removed++
			removeEmptyDirsUp(filepath.Dir(full), root)
		case os.IsNotExist(err):
			missing++ // 本地早就没了，台账清掉即可
		default:
			failed++
			log.Printf("[同步] 失效 STRM 清理失败 %s: %v", r.RelPath, err)
			continue // 删不掉就留着台账，下次再试
		}
		doneIDs = append(doneIDs, r.ID)
	}
	for _, batch := range chunkIDs(doneIDs, 400) {
		h.DB.Where("id IN ?", batch).Delete(&model.SyncedFile{})
	}
	log.Printf("[同步] ○ 失效 STRM 清理完成：删除 %d 个，本地已不存在 %d 个，失败 %d 个", removed, missing, failed)
	c.JSON(http.StatusOK, gin.H{
		"message": "失效 STRM 清理完成", "removed": removed, "missing": missing, "failed": failed,
	})
}

// removeEmptyDirsUp 逐级向上删空目录，到 root 为止（不删 root 自己）
func removeEmptyDirsUp(dir, root string) {
	root = filepath.Clean(root)
	for i := 0; i < fastMaxDepth; i++ {
		dir = filepath.Clean(dir)
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
