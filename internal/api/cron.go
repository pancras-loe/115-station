package api

// ==================== 同步 Cron 调度器 ====================
//
// 支持标准 5 字段 cron（分 时 日 月 周），字段支持 * 、*/n 、a-b 、逗号列表。
// 每分钟检查一次，命中且无同步任务运行时触发。两条独立的调度线：
//
//   - 增量（incr.cron）：「自动整理 → 增量同步」两段，日常入库主力，高频。
//     整理是一条自带落盘的完整流水线（识别→搬移→STRM→刮削→刷 Emby），
//     跟在后面的增量只负责 115 端的外部变更（手机上传、离线下载、网页端删改）
//   - 全量（full.cron）：整库扫描，服务于失效 STRM 检测——生活事件有窗口，
//     网页版批量删除、停机期间的删除都会漏掉，只有整库差集能查出来。低频即可
//
// 全量整库扫描请求量大（115 风控敏感），所以它只在用户开了失效 STRM 检测时
// 才有意义：检测关着的时候定时全量纯属白跑一趟，前后端都直接当没开。

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

// fullSyncCfg 全量同步的持久化配置（setting "full"，与前端「全量同步」页签同构）
type fullSyncCfg struct {
	Cid           string   `json:"cid"`
	LocalPath     string   `json:"local_path"`
	VideoExt      []string `json:"video_ext"`
	ImageExt      []string `json:"image_ext"`
	DataExt       []string `json:"data_ext"`
	Mode          string   `json:"mode"`
	DetectOrphans bool     `json:"detect_orphans"`
	CronEnabled   bool     `json:"cron_enabled"`
	Cron          string   `json:"cron"`
}

// loadFullSyncCfg 读取 full 配置（解析失败返回零值，调用方按「没配」处理）。
// 走 getSettingValue 而不是 Config.GetSetting：失效 STRM 那侧的
// orphanDetectEnabled 也是这么读的，两处读法不一致会出现
// 「检测开着但调度器认为没开」这类只在某种部署形态下复现的偏差
func (h *Handler) loadFullSyncCfg() fullSyncCfg {
	var cfg fullSyncCfg
	_ = json.Unmarshal([]byte(h.getSettingValue("full")), &cfg)
	return cfg
}

// loadFullCron 全量同步的 cron 表达式，未启用时返回空。
// DetectOrphans 是硬前提：定时全量的用途就是刷新失效 STRM 标记，检测关掉后
// 前端连开关都不显示——后台若还在每天跑整库扫描，用户在界面上根本看不出来
func (h *Handler) loadFullCron() string {
	cfg := h.loadFullSyncCfg()
	if !cfg.CronEnabled || !cfg.DetectOrphans {
		return ""
	}
	return strings.TrimSpace(cfg.Cron)
}

// fullParamsFromConfig 从已保存的 full 配置组装全量同步参数
func (h *Handler) fullParamsFromConfig() fullParams {
	cfg := h.loadFullSyncCfg()
	p := fullParams{
		Cid: cfg.Cid, LocalPath: cfg.LocalPath, Mode: cfg.Mode,
		VideoExt: cfg.VideoExt, ImageExt: cfg.ImageExt, DataExt: cfg.DataExt,
	}
	if p.LocalPath == "" {
		p.LocalPath = defaultLocalPath
	}
	return p
}

// incrParamsFromConfig 从已保存的 full 配置组装增量参数
func (h *Handler) incrParamsFromConfig() incrParams {
	cfg := h.loadFullSyncCfg()
	p := incrParams{Cid: "0", LocalPath: defaultLocalPath, Limit: 1000}
	if cfg.Cid != "" {
		p.Cid = cfg.Cid
	}
	if cfg.LocalPath != "" {
		p.LocalPath = cfg.LocalPath
	}
	p.VideoExt, p.ImageExt, p.DataExt = cfg.VideoExt, cfg.ImageExt, cfg.DataExt
	return p
}

// StartSyncScheduler 启动分钟级调度器：
//   - full 配置的 cron 命中 → 定时全量同步（刷新失效 STRM 标记）
//   - incr 配置的 cron 命中 → 自动整理（自带落盘）+ 增量同步（只管外部变更）
//
// 两者同一分钟同时命中时只跑全量：整库扫描本来就会覆盖增量那点事件，
// 且全量跑完会把事件窗口标记为已覆盖，紧接着再跑一次增量纯属重复请求 115
func StartSyncScheduler(h *Handler) {
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
			now := time.Now()

			if cron := h.loadFullCron(); cron != "" && CronMatch(cron, now) {
				h.runScheduledFullSync()
				continue
			}

			cron := h.loadIncrCron()
			if cron == "" {
				continue // 未配置调度
			}
			if !CronMatch(cron, now) {
				continue
			}
			h.runScheduledTick(cron)
		}
	}()
	log.Println("[调度] 调度器已启动（cron 触发 自动整理+增量同步 / 全量同步）")
}

// runScheduledFullSync 单轮定时全量同步。defer 解锁 + recover 的理由同 runScheduledTick。
// 只标记失效 STRM 不删除——定时任务没人盯着，误判一次就是真丢文件，
// 清理仍然只能由用户在 Strm 管理页确认后触发
func (h *Handler) runScheduledFullSync() {
	p := h.fullParamsFromConfig()
	if p.Cid == "" || p.Cid == "0" {
		log.Printf("[定时] ○ 全量同步已开启定时，但未配置媒体库 cid，本轮跳过")
		return
	}
	if !fullSyncMu.TryLock() {
		log.Printf("[定时] ○ 已有任务运行中，本轮全量同步跳过")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[定时] ✗ 全量同步 panic 已恢复: %v", r)
		}
		endTask()
		fullSyncMu.Unlock()
	}()
	beginTask("定时全量同步")

	sum, err := h.executeFullSync(p)
	if err != nil {
		log.Printf("[定时] ✗ 全量同步失败: %v", err)
		return
	}
	log.Printf("[定时] ✅ 全量同步完成（%s）：视频 %d，生成 STRM %d，附属下载 %d，失效 STRM %d 个待清理",
		sum.Elapsed, sum.Total, sum.Created, sum.AssetsDownloaded, sum.Orphans)
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
	// 1) 自动整理：识别 → 搬移 → 写 STRM → 刮削 → 刷 Emby 一条龙跑完
	orgSteps, _, orgErr := h.executeOrganize()
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
	// 2) 增量同步：只处理 115 端的外部变更。整理刚刚自产的 move/rename
	// 已登记进抑制表，绕回来时会被 pop 掉跳过
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
			log.Printf("[定时] 增量: 新事件 %d，删 %d，移/改 %d，STRM %d，附属下载 %d，跳过自产 %d",
				sum.EventsFresh, sum.Deleted, sum.Moved, sum.StrmCreated, sum.AssetsDownloaded, sum.Suppressed)
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
	// 卡死的 pending 事件：某个网盘目录持续读不出来时，增量会整轮放弃
	// （DirsSkipped>0 不标记），这批事件就每 10 分钟被重放一次、永不落地。
	// 超过 7 天直接判死：生活事件窗口早过了，真缺的内容只有全量整库差集能补回来
	stuck := h.DB.Model(&model.SyncEvent{}).
		Where("status = ? AND created_at < ?", "pending", time.Now().AddDate(0, 0, -7)).
		Updates(map[string]interface{}{"status": "applied", "applied_at": time.Now()})
	if stuck.Error == nil && stuck.RowsAffected > 0 {
		log.Printf("[系统] ⚠ 有 %d 条网盘变动积压超过 7 天始终没能处理完，已停止重试。"+
			"如果发现媒体库缺内容，到「Strm 管理 → 全量同步」跑一次整库扫描即可补齐", stuck.RowsAffected)
	}
	pruneEventSuppress()
	pruneOrganizeRecords()
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
