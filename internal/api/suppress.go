package api

import (
	"log"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm/clause"
)

// ==================== 整理自产事件抑制 ====================
//
// 整理在 115 上做的每一次 move / rename 都会产生生活事件，几分钟后被增量同步拉回来。
// 一条龙改造之后 STRM 已经由整理自己落盘，这些事件再被增量处理一遍纯属重复请求
// （推导路径、重遍历目录），还会和整理的结果打架。
//
// 做法与 p115strmhelper 的 pantransfercacher 一致：整理动手前后登记 fid，
// 增量消费事件时命中就**删除该行并跳过**（命中即消费）。删除而不是留着，
// 是因为同一个 fid 之后可能被用户真的手动移动，那次必须正常处理。
//
// TTL 2 小时：生活事件窗口远小于此；过期行随 pruneSyncEvents 每日清理。

const suppressTTL = 2 * time.Hour

// markSuppressed 批量登记整理自产的 fid。失败只记日志不阻断整理——
// 抑制表丢一条的后果只是增量多做一次幂等的重复处理，不值得让整理失败
func markSuppressed(op string, fids []string) {
	if model.DB == nil || len(fids) == 0 {
		return
	}
	expire := time.Now().Add(suppressTTL)
	rows := make([]model.EventSuppress, 0, len(fids))
	seen := map[string]bool{}
	for _, fid := range fids {
		if fid == "" || fid == "0" || seen[fid] {
			continue
		}
		seen[fid] = true
		rows = append(rows, model.EventSuppress{FileID: fid, Op: op, ExpireAt: expire})
	}
	if len(rows) == 0 {
		return
	}
	// 同一 fid 可能先 rename 再 move：后写的覆盖，续上 TTL
	err := model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "file_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"op", "expire_at"}),
	}).CreateInBatches(&rows, 200).Error
	if err != nil {
		log.Printf("[整理] ○ 自产事件标记失败（%d 个，增量会重复处理一次，无副作用）: %v", len(rows), err)
	}
}

// peekSuppressed 事件命中检查：**只查不删**。
//
// 为什么不在这里就消费掉：增量同步遇到目录读取失败时会整轮放弃
// （DirsSkipped>0 → 事件保持 pending，下轮重做）。如果查的时候就把标记删了，
// 下一轮同一批事件就没有标记可命中，整理自产的 move 会被当成外部变更处理——
// removeSyncedItem 按 file_id 精确删掉整理刚生成的 STRM，再靠重遍历建回来。
// 所以标记必须等到事件真的被标成 applied 之后才由 unmarkSuppressed 批量清掉。
//
// 过期的标记顺手删掉并返回 false（事件走正常流程）
func peekSuppressed(fid string) bool {
	if model.DB == nil || fid == "" {
		return false
	}
	var row model.EventSuppress
	if model.DB.Where("file_id = ?", fid).First(&row).Error != nil {
		return false
	}
	if !time.Now().Before(row.ExpireAt) {
		model.DB.Delete(&model.EventSuppress{}, row.ID)
		return false
	}
	return true
}

// unmarkSuppressed 撤销抑制：整理没能自己把这个文件落盘时必须调用，
// 否则事件被跳过、STRM 也没生成，这个文件就永久消失在两条链路之间
func unmarkSuppressed(fids ...string) {
	if model.DB == nil || len(fids) == 0 {
		return
	}
	model.DB.Where("file_id IN ?", fids).Delete(&model.EventSuppress{})
}

// pruneEventSuppress 清理过期的抑制标记（随 pruneSyncEvents 每日一次）
func pruneEventSuppress() {
	if model.DB == nil {
		return
	}
	model.DB.Where("expire_at < ?", time.Now()).Delete(&model.EventSuppress{})
}
