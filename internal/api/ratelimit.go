package api

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"gorm.io/gorm"
)

// ==================== 115 全局请求节流器 ====================
//
// 115 对高频请求有风控（返回"服务器开小差了"），且被风控后会持续一段时间。
// 所有对 webapi.115.com / proapi.115.com 的请求必须经过 throttle115()，
// 保证任意两次请求之间的最小间隔。
//
// 可通过环境变量 STRMHUB_115_INTERVAL（毫秒）调整，默认 1000ms。
// 登录相关域名（qrcodeapi / passportapi）不节流，不影响扫码体验。

var (
	throttleMu     sync.Mutex
	throttleLast   time.Time
	throttleMinGap = loadThrottleInterval()

	lastWaitMu sync.Mutex
	lastWait   time.Duration // 最近一次节流等待时长（供同步日志显示）
)

// loadThrottleInterval 读取节流间隔配置
func loadThrottleInterval() time.Duration {
	ms := 1000
	if v := os.Getenv("STRMHUB_115_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100 && n <= 60000 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// isThrottledHost 判断是否为需要节流的 115 域名（文件操作类接口，含镜像域名）
func isThrottledHost(api string) bool {
	return strings.Contains(api, "webapi.115.com") ||
		strings.Contains(api, "web.api.115.com") ||
		strings.Contains(api, "pro.api.115.com") ||
		strings.Contains(api, "115cdn.com") ||
		strings.Contains(api, "115vod.com") ||
		strings.Contains(api, "proapi.115.com")
}

// isWriteAPI 写操作接口（改名/移动/建目录/删除/转存）——风控敏感，
// 保持完整间隔；其余（列目录/查信息/取直链/事件流）为读操作，
// 用更短的读间隔：同步与整理的目录遍历占调用大头，读提速最直接
func isWriteAPI(api string) bool {
	return strings.Contains(api, "/files/batch_rename") ||
		strings.Contains(api, "/files/move") ||
		strings.Contains(api, "/files/add") ||
		strings.Contains(api, "/files/delete") ||
		strings.Contains(api, "/files/receive") ||
		strings.Contains(api, "/files/upload")
}

// readGap 读间隔：不超过 1 秒（用户间隔更大时按 1 秒走读通道）
func readGap() time.Duration {
	throttleMu.Lock()
	gap := throttleMinGap
	throttleMu.Unlock()
	if gap > time.Second {
		return time.Second
	}
	return gap
}

// writeGap 写间隔：不低于 3 秒（防风控底线，用户设置更大时从其设置）
func writeGap() time.Duration {
	throttleMu.Lock()
	gap := throttleMinGap
	throttleMu.Unlock()
	if gap < 3*time.Second {
		return 3 * time.Second
	}
	return gap
}

// throttle115 在发起 115 文件类 API 请求前调用，确保与上一请求【完成时刻】
// 的间隔不小于设置值（读写分级：写=设置值且≥3s，读=≤1s）
func throttle115(api string) {
	if !isThrottledHost(api) {
		return
	}
	// 取直链端点豁免：独立风控的读接口，且在播放起播关键路径上——
	// 与整理/同步共用 1s 队列会让播放被后台任务饿死。独立短节流防打爆
	if isDownloadURLAPI(api) {
		throttleFast(api, 200*time.Millisecond)
		return
	}
	gap := readGap()
	if isWriteAPI(api) {
		gap = writeGap()
	}
	throttleMu.Lock()
	var waited time.Duration
	if elapsed := time.Since(throttleLast); elapsed < gap {
		waited = gap - elapsed
		time.Sleep(waited)
	}
	throttleMu.Unlock()

	lastWaitMu.Lock()
	lastWait = waited
	lastWaitMu.Unlock()
}

// fastThrottleLast 取直链端点独立节流锚点（与全局队列分离）
var (
	fastThrottleMu   sync.Mutex
	fastThrottleLast time.Time
)

// throttleFast 独立短节流：独立锁+锚点，与全局 1s 队列完全解耦
// （若复用 throttleMu，全局 sleeper 持锁睡眠仍会堵住播放取链）
func throttleFast(api string, gap time.Duration) {
	fastThrottleMu.Lock()
	defer fastThrottleMu.Unlock()
	if elapsed := time.Since(fastThrottleLast); elapsed < gap {
		time.Sleep(gap - elapsed)
	}
	fastThrottleLast = time.Now()
	_ = api
}

// isDownloadURLAPI 取直链端点（播放起播关键路径）
func isDownloadURLAPI(api string) bool {
	return strings.Contains(api, "/ufile/download") ||
		strings.Contains(api, "files/download")
}

// throttle115Done 请求完成后调用，推进节流锚点（锚点=完成时刻）
func throttle115Done(api string) {
	if !isThrottledHost(api) {
		return
	}
	throttleMu.Lock()
	throttleLast = time.Now()
	throttleMu.Unlock()
	if isDownloadURLAPI(api) {
		fastThrottleMu.Lock()
		fastThrottleLast = time.Now()
		fastThrottleMu.Unlock()
	}
}

// throttle115LastWait 返回最近一次节流的等待时长（同步日志展示用）
func throttle115LastWait() time.Duration {
	lastWaitMu.Lock()
	defer lastWaitMu.Unlock()
	return lastWait
}

// Set115Interval 运行时调整节流间隔（供设置接口调用）
func Set115Interval(d time.Duration) {
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	throttleMu.Lock()
	throttleMinGap = d
	throttleMu.Unlock()
}

// Apply115Interval 从数据库读取用户设置的 API 请求间隔并应用
// 优先级：数据库设置 > STRMHUB_115_INTERVAL 环境变量 > 默认 1 秒
func Apply115Interval(db *gorm.DB) {
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Interval <= 0 {
		return
	}
	Set115Interval(time.Duration(storage.Interval * float64(time.Second)))
	log.Printf("[系统] API 请求间隔已设置: %v", time.Duration(storage.Interval*float64(time.Second)))
}
