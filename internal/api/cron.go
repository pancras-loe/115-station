package api

// ==================== 同步 Cron 调度器 ====================
//
// 支持标准 5 字段 cron（分 时 日 月 周），字段支持 * 、*/n 、a-b 、逗号列表。
// 三条独立的调度线：
//
//   - 自动整理（incr.cron）：识别→搬移→STRM→刮削→刷 Emby 一条龙，
//     每分钟检查一次 cron 是否命中。重操作，低频合适
//   - 增量同步（incr.interval_sec）：**独立轮询**，默认 30 秒一轮。
//     只负责 115 端的外部变更（手机上传、离线下载、网页端删改）。
//     空转轮次只要 1 个请求（拉一页生活事件），挂在整理的 cron 上纯属浪费实时性。
//     ⚠️ 但**有事件的轮次不便宜**：事件带不出 pick_code 时要回退目录遍历，
//     每列一次目录就是一次 115 请求 + 1 秒节流。所以遍历范围被严格收着
//     （文件级事件只列一层，见 incr115.go 的 fallbackTarget），
//     并且遍历中途会给排队的任务让路。填 0 可退回「跟着整理串行跑」的老行为
//   - 全量（full.cron）：整库扫描，服务于失效 STRM 检测——生活事件有窗口，
//     网页版批量删除、停机期间的删除都会漏掉，只有整库差集能查出来。低频即可
//
// 全量整库扫描请求量大（115 风控敏感），所以它只在用户开了失效 STRM 检测时
// 才有意义：检测关着的时候定时全量纯属白跑一趟，前后端都直接当没开。
//
// 三条线共用 taskMu（见 synclock.go）。整理抢不到锁时会登记让路请求：
// 正在跑的增量遍历看到有人排队就提前收工，没消费完的事件下一轮原样重来。
// 还抢不到就置位 organizeMissed，每分钟继续补 —— 增量提频到 30 秒之后，
// 整理的 cron 撞上一轮正在遍历大目录的增量是常态。

import (
	"115-station/internal/model"

	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
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

// incrCfg setting "incr" 的结构。IntervalSec 用指针区分「没配过」与「显式填 0」
type incrCfg struct {
	Cron        string `json:"cron"`
	IntervalSec *int   `json:"interval_sec"`
}

// loadIncrCfg 读 incr 配置。走 getSettingValue 而不是 Config.GetSetting——
// 与 loadFullSyncCfg 保持同一条读取路径，两处读法不一致会出现
// 「配置改了但调度器没看见」这类只在某种部署形态下复现的偏差
func (h *Handler) loadIncrCfg() incrCfg {
	var cfg incrCfg
	_ = json.Unmarshal([]byte(h.getSettingValue("incr")), &cfg)
	return cfg
}

// loadIncrCron 自动整理的 cron（这条 cron 同时是整理的调度开关）。
// 它和 interval_sec 同住 setting "incr"（历史上整理与增量绑在一条 cron 上），
// 但界面在「自动整理 → 基础配置」，不在增量页——前端两侧各改各的字段
func (h *Handler) loadIncrCron() string {
	return strings.TrimSpace(h.loadIncrCfg().Cron)
}

const (
	// incrIntervalDefault 增量独立轮询的默认间隔。
	// 空转轮次只有 1 个请求（拉一页生活事件），30 秒一轮 ≈ 2 次/分钟，
	// 比改造前（10 分钟 34 次 ≈ 3.4 次/分钟）更低。
	// 有事件的轮次贵在目录遍历，那部分由遍历范围与让路机制控制，不靠调大间隔
	incrIntervalDefault = 30 * time.Second
	// incrIntervalMin 下限，再快也没有意义（115 的事件本身就有延迟）
	incrIntervalMin = 15 * time.Second
)

// loadIncrInterval 增量独立轮询间隔。没配过取默认 30 秒；
// 显式填 0 表示关闭独立轮询、退回「跟着整理的 cron 串行跑」的老行为（逃生门）
func (h *Handler) loadIncrInterval() time.Duration {
	sec := h.loadIncrCfg().IntervalSec
	if sec == nil {
		return incrIntervalDefault
	}
	if *sec <= 0 {
		return 0
	}
	if d := time.Duration(*sec) * time.Second; d > incrIntervalMin {
		return d
	}
	return incrIntervalMin
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
				organizeMissed.Store(false) // 调度被清空，别留着一个永远待补的标记
				continue
			}
			// 错过即补：上一次命中时锁被占用的话，这里每分钟继续尝试直到补上
			if CronMatch(cron, now) || organizeMissed.Load() {
				h.runScheduledTick()
			}
		}
	}()
	h.startIncrPoller()
	log.Println("[调度] 调度器已启动（cron 触发 自动整理 / 全量同步）")
}

// organizeMissed 整理的 cron 命中时锁被占用 → 置位，之后每分钟继续尝试补跑。
//
// 改造前这里是 TryLock 失败直接 return。增量提频到 30 秒之后，
// 整理的 cron 撞上一轮正在遍历大目录的增量是常态，一错过就要等下一个
// cron 周期（默认配置下 10 分钟）
var organizeMissed atomic.Bool

// startIncrPoller 增量独立轮询。
//
// 从整理的 cron 里拆出来：改造后一轮增量只要 1~2 个请求，
// 挂在最细 1 分钟、默认 10 分钟的 cron 上纯属浪费实时性。
// 间隔每轮重读，改配置不必重启
func (h *Handler) startIncrPoller() {
	if h.loadIncrInterval() > 0 {
		log.Printf("[调度] 增量轮询已启动（每 %v 一轮）", h.loadIncrInterval())
	}
	go func() {
		for {
			wait := h.loadIncrInterval()
			if wait <= 0 {
				wait = time.Minute // 关闭状态下也每分钟回来看一眼配置改没改
			}
			select {
			case <-time.After(wait):
			case <-stopCh:
				return
			}
			if h.loadIncrInterval() > 0 {
				h.runIncrPollTick()
			}
		}
	}()
}

// runIncrPollTick 一轮独立增量。
//
// ⚠️ 这里不 beginTask：30 秒一次的 beginTask 会让前端「当前任务」闪个不停，
// 还会把运行历史刷满。后台轮询的可见性交给状态页，不占用「当前任务」这个位置。
// 但锁还是要拿——手动触发时才能正确地报「任务进行中」
func (h *Handler) runIncrPollTick() {
	p := h.incrParamsFromConfig()
	if p.Cid == "" || p.Cid == "0" {
		return // 未配置媒体库
	}
	// TryLockPolite：**有人在排队就主动不抢**。
	// 没有这道礼让，增量会在整理刚放开锁的瞬间又把锁抢回去
	// （整理 60 秒才回来一次、转存守望者 5 分钟一次，抢不过 30 秒一轮的增量），
	// 登记的让路就白做了
	if !taskMu.TryLockPolite("增量轮询") {
		vlog("[轮询] ○ 本轮增量跳过：%s", taskMu.Describe())
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[轮询] ✗ 增量 panic 已恢复: %v", r)
		}
		taskMu.Unlock()
	}()
	// 结果由增量自己按轮次号打账单（见 incrtrace.go）：
	// 这里再打一行摘要就是同一轮内容出现两遍，排查时反而更难分清轮次
	if _, err := h.executeIncrementalSync(p); err != nil {
		log.Printf("[轮询] 增量同步失败: %v", err)
	}
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
	if !taskMu.Acquire("定时全量同步", organizeAcquireWait) {
		logBusy("全量同步", "定时")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[定时] ✗ 全量同步 panic 已恢复: %v", r)
		}
		endTask()
		taskMu.Unlock()
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
func (h *Handler) runScheduledTick() {
	// 先登记让路再等：正在跑的增量遍历看到有人排队会就地收工。
	// 等不到才置位「错过即补」，每分钟继续尝试，不再等下一个 cron 周期
	if !taskMu.Acquire("定时整理", organizeAcquireWait) {
		if organizeMissed.CompareAndSwap(false, true) {
			logBusy("整理", "定时")
			log.Printf("[定时] ○ 整理稍后补跑（每分钟重试一次，不等下一个 cron 周期）")
		}
		return
	}
	organizeMissed.Store(false)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[定时] ✗ 任务 panic 已恢复: %v", r)
		}
		endTask()
		taskMu.Unlock()
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
	// 2) 增量同步：只在独立轮询关掉时才在这里串一次（逃生门下的老行为）。
	// 轮询开着的话它每 30 秒就跑一轮，这里再跑纯属重复请求 115
	var sum *incrSummary
	if h.loadIncrInterval() <= 0 {
		p := h.incrParamsFromConfig()
		var err error
		sum, err = h.executeIncrementalSync(p)
		if err != nil {
			log.Printf("[定时] 增量同步失败: %v", err)
		}
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
	// 增量那半边的明细由它自己按轮次号打账单（见 incrtrace.go），这里不复述
	if !idle {
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
	pruneDownloadLinks()
	pruneDeepDeleteRecords()
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
