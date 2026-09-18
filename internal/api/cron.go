package api

// ==================== 增量同步 Cron 调度器 ====================
//
// 支持标准 5 字段 cron（分 时 日 月 周），字段支持 * 、*/n 、a-b 、逗号列表。
// 每分钟检查一次，命中且无同步任务运行时触发一次增量同步（CMS lift_sync_task 模式）。

import (
	"strmhub/internal/model"

	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
)

// cronFieldMatch 检查单个 cron 字段是否匹配当前值
func cronFieldMatch(field string, v int) bool {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// */n 或 *：任意值
		if part == "*" {
			return true
		}
		if strings.HasPrefix(part, "*/") {
			if n, err := strconv.Atoi(part[2:]); err == nil && n > 0 && v%n == 0 {
				return true
			}
			continue
		}
		// a-b 范围
		if i := strings.IndexByte(part, '-'); i > 0 {
			lo, err1 := strconv.Atoi(part[:i])
			hi, err2 := strconv.Atoi(part[i+1:])
			if err1 == nil && err2 == nil && v >= lo && v <= hi {
				return true
			}
			continue
		}
		// 单值
		if n, err := strconv.Atoi(part); err == nil && n == v {
			return true
		}
	}
	return false
}

// CronMatch 判断 5 字段 cron 表达式是否命中给定时间（分 时 日 月 周）
func CronMatch(expr string, t time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return false
	}
	return cronFieldMatch(fields[0], t.Minute()) &&
		cronFieldMatch(fields[1], t.Hour()) &&
		cronFieldMatch(fields[2], t.Day()) &&
		cronFieldMatch(fields[3], int(t.Month())) &&
		cronFieldMatch(fields[4], int(t.Weekday()))
}

// loadIncrCron 从配置读取增量同步 cron（setting "incr" 的 cron 字段）
func (h *Handler) loadIncrCron() string {
	v := h.Config.GetSetting("incr")
	if v == "" {
		return ""
	}
	var cfg struct {
		Cron string `json:"cron"`
	}
	if json.Unmarshal([]byte(v), &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Cron)
}

// incrParamsFromConfig 从已保存的 full 配置组装增量参数
func (h *Handler) incrParamsFromConfig() incrParams {
	v := h.Config.GetSetting("full")
	p := incrParams{Cid: "0", LocalPath: defaultLocalPath, Limit: 1000}
	if v != "" {
		var cfg struct {
			Cid       string   `json:"cid"`
			Path      string   `json:"path"`
			LocalPath string   `json:"local_path"`
			VideoExt  []string `json:"video_ext"`
			ImageExt  []string `json:"image_ext"`
			DataExt   []string `json:"data_ext"`
		}
		if json.Unmarshal([]byte(v), &cfg) == nil {
			if cfg.Cid != "" {
				p.Cid = cfg.Cid
			}
			if cfg.LocalPath != "" {
				p.LocalPath = cfg.LocalPath
			}
			p.VideoExt, p.ImageExt, p.DataExt = cfg.VideoExt, cfg.ImageExt, cfg.DataExt
		}
	}
	return p
}

// StartIncrScheduler 启动分钟级调度器：incr 配置的 cron 命中时自动执行
// 「自动整理 → 增量同步」流水线（CMS 主任务模式；全量同步仅供手动触发，不参与调度）
func StartIncrScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.pruneSyncEvents()

			cron := h.loadIncrCron()
			if cron == "" {
				continue // 未配置调度
			}
			if !CronMatch(cron, time.Now()) {
				continue
			}
			h.runScheduledTick(cron)
		}
	}()
	log.Println("[调度] 调度器已启动（cron 触发 自动整理+增量同步；全量同步仅手动）")
}

// runScheduledTick 单轮定时任务（独立函数保证 defer 在本轮结束即执行——
// defer 写在 for-select 循环体会累积到 goroutine 退出，锁被永久持有）。
// defer 解锁 + recover：中途 panic（解析外部数据的路径是高发区）也不会
// 永久抱死互斥锁——此前非 defer 的 Unlock 在 panic 时被跳过，之后所有
// 同步入口都报"任务正在进行中"直到重启
func (h *Handler) runScheduledTick(cron string) {
	if !fullSyncMu.TryLock() {
		log.Printf("[定时] ○ 已有任务运行中，本轮跳过")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[定时] ✗ 任务 panic 已恢复: %v", r)
		}
		endTask()
		fullSyncMu.Unlock()
	}()
	beginTask("定时整理+增量")
	start := time.Now()
	// 1) 自动整理（不联动全量同步，交给下一步增量精确处理）
	orgSteps, _, orgErr := h.executeOrganize(false)
	if orgErr != nil {
		log.Printf("[定时] ○ 整理跳过: %v", orgErr)
	} else {
		for _, st := range orgSteps {
			if st["status"] == "失败" {
				msg, _ := st["message"].(string)
				log.Printf("[定时] ✗ 整理失败: %v", msg)
			}
		}
	}
	// 2) 增量同步（整理产生的 move 事件会被精确应用）
	p := h.incrParamsFromConfig()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[定时] 增量同步失败: %v", err)
	}
	// 空转判定：无整理产出且增量无新事件 → 整轮只留一行（此前每轮 ~10 行噪音）
	idle := orgErr == nil
	for _, st := range orgSteps {
		if st["status"] == "失败" {
			idle = false
		}
	}
	if sum != nil && sum.EventsFresh > 0 {
		idle = false
	}
	// 空转轮次完全静默（每 10 分钟一 tick，静默才不刷屏）；
	// 只有真的处理了内容才输出摘要
	if !idle {
		if sum != nil {
			log.Printf("[定时] 增量: 新事件 %d，删 %d，移/改 %d，STRM %d，附属下载 %d",
				sum.EventsFresh, sum.Deleted, sum.Moved, sum.StrmCreated, sum.AssetsDownloaded)
		}
		log.Printf("[定时] ✅ 定时任务完成，耗时 %.2f 秒", time.Since(start).Seconds())
	}
}

// pruneSyncEvents 清理 30 天前已应用的生活事件（每日一次）。
// 事件表只增不减，长期运行会无限膨胀拖慢去重查询
var lastPruneDay string

func (h *Handler) pruneSyncEvents() {
	today := time.Now().Format("2006-01-02")
	if lastPruneDay == today {
		return
	}
	lastPruneDay = today
	res := h.DB.Where("status = ? AND created_at < ?", "applied", time.Now().AddDate(0, 0, -30)).Delete(&model.SyncEvent{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[系统] ○ 清理 %d 条 30 天前的已应用事件", res.RowsAffected)
	}
}

// nextCronTime 计算给定时刻之后下一次 cron 触发时间
// 逐分钟扫描（最多扫描一年，约 52.6 万次，毫秒级完成）
func nextCronTime(expr string, after time.Time) time.Time {
	// 从下一分钟开始（对齐分钟）
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 0) // 最多扫描一年
	for t.Before(limit) {
		if CronMatch(expr, t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{} // 未找到
}
