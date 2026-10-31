package api

// 分享 STRM 的「转存这份」（SHARE-STRM-PLAN.md §8）：用户想长期留着某个分享时，把整个分享转存进某个库的
// 转存文件夹，照常整理入库；整理把同一个文件（sha1）写进台账之后，这里对应的分享 STRM 就多余了，
// 由调度器每分钟收掉（shareTransferSweep）。没入库的（整理失败、判输进了「已存在」）分享 STRM 留着，照常能播。
// 转存失败什么都不动。

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// shareTransferURL 用登记的分享码与提取码拼回分享链接（转存走 shareReceiveOpt，它认链接）
func shareTransferURL(src model.ShareSource) string {
	u := "https://115cdn.com/s/" + src.ShareCode
	if src.ReceiveCode != "" {
		u += "?password=" + src.ReceiveCode
	}
	return u
}

// ShareStrmTransfer POST /sharestrm/sources/:id/transfer {subdir} —— 转存这份
func (h *Handler) ShareStrmTransfer(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	var req struct {
		Subdir string `json:"subdir"` // 转进哪个库（lib:<id>）；空 = 默认转存库
	}
	_ = c.ShouldBindJSON(&req)
	if shareSourceBusy(h.DB, src.ID) {
		c.JSON(http.StatusConflict, gin.H{"error": "这个分享正在生成，等它结束再转存"})
		return
	}
	target, err := h.stagingTarget(req.Subdir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 不打「整理完分享到 RE0 / 癫影」的标记：这是从分享 STRM 转过来的，不是用户这次贴的链接
	msg, okN, fail, err := h.shareReceiveOpt(shareTransferURL(src), src.ReceiveCode, target, "分享STRM", true, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "转存失败：" + err.Error()})
		return
	}
	if okN == 0 && fail == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "分享是空的，没有转存任何内容"})
		return
	}
	now := time.Now()
	// 转存后不再追更：之后的新集要么再转存一次，要么删掉重新添加成分享 STRM
	h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Updates(map[string]any{"transferred": &now, "paused": true, "next_check": nil})
	plogShareStrm.Printf("→ 转存这份：%s（%s）%s", firstNonEmpty(src.Title, src.ShareCode), src.ShareCode, msg)
	c.JSON(http.StatusOK, gin.H{"message": msg + "。整理入库后，这里对应的 STRM 会自动收掉"})
}

// shareTransferSweep 调度器每分钟：转存过的分享，文件已经整理进库（台账里有同一个 sha1）的，收掉这里的分享 STRM；
// 视频全收完了整个分享删掉（附属文件一起）。不碰 115
func (h *Handler) shareTransferSweep() {
	if h.DB == nil {
		return
	}
	var list []model.ShareSource
	h.DB.Where("transferred IS NOT NULL").Find(&list)
	if len(list) == 0 {
		return
	}
	root := shareRootAbs(loadShareStrmCfg())
	for _, src := range list {
		if shareSourceBusy(h.DB, src.ID) {
			continue
		}
		n := retireOwnedShareFiles(h, root, src)
		var left int64
		h.DB.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ? AND blocked = ?", src.ID, "video", false).Count(&left)
		if left == 0 {
			if _, err := deleteShareSource(h.DB, root, src); err == nil {
				plogShareStrm.Printf("✓ 转存的「%s」都入库了，分享 STRM 已全部收掉", firstNonEmpty(src.Title, src.ShareCode))
			}
			continue
		}
		if n > 0 {
			h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Update("files", int(left))
		}
	}
}

// retireOwnedShareFiles 收掉台账里已经有同一份（sha1）的分享视频，返回收掉几个
func retireOwnedShareFiles(h *Handler, root string, src model.ShareSource) int {
	var files []model.ShareFile
	h.DB.Where("source_id = ? AND kind = ? AND sha1 <> ''", src.ID, "video").Find(&files)
	var removed []string
	for _, f := range files {
		var n int64
		h.DB.Model(&model.SyncedFile{}).Where("UPPER(sha1) = ? AND orphan_at IS NULL", strings.ToUpper(f.Sha1)).Count(&n)
		if n == 0 {
			continue
		}
		if abs := removeShareLocal(root, f.RelPath); abs != "" {
			removed = append(removed, abs)
		}
		h.DB.Delete(&model.ShareFile{}, f.ID)
	}
	if len(removed) > 0 {
		notifyEmbyDeleted(removed...)
		plogShareStrm.Printf("○ 「%s」有 %d 个已经整理进库，收掉对应的分享 STRM", firstNonEmpty(src.Title, src.ShareCode), len(removed))
	}
	return len(removed)
}
