package api

// ==================== 按需离线（边下边播）播放端点 ====================
//
// STRM 内容不再直接关联网盘文件，而是指向 StrmHub 自己的播放端点：
//
//	ed2k://|file|某剧集.S01E01.1080p.mkv|123456789|hash…
//	    ↓ 入库时（离线提交成功后自动登记）
//	/media/按需离线/某剧集.S01E01.1080p.mkv.strm → http://StrmHub:6086/ed2k/play/<id>
//	    ↓ Emby 播放时请求这个 URL
//	StrmHub: 查任务状态 → 没下过就提交 115 离线下载 → 短暂轮询
//	    ↓ 完成（本次等到，或之后的某次播放）
//	302 跳转 115 直链 → 播放器开播
//
// <id> 是链接指纹（ed2k hash / 磁力 btih / URL sha1），同一资源天然去重。
// 下载完成后把定位到的 pickcode 存进 OfflinePlay 表，后续播放与 /d/ 同速。
// 下载中的请求最多等 ~18 秒（秒传/小文件当场开播），未完成返回 503+进度，
// 播放器稍后重试即可；离线监视器与整理管线照常在后台推进。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// offlinePlayStageDir 占位 STRM 的库名（本地媒体根下的独立目录，
// Emby 可见可点；正式 STRM 由整理+同步生成后，这里的占位自动清掉）
const offlinePlayStageDir = "按需离线"

// offlinePlayWaitTotal / -Interval 播放请求的就地等待预算：
// 覆盖秒传与启动中的小任务；超出即 503 让播放器稍后重试
const (
	offlinePlayWaitTotal    = 18 * time.Second
	offlinePlayWaitInterval = 4 * time.Second
)

// offlinePlayMu 串行化「提交 + 状态迁移」临界区（Emby 起播常并发重试，
// 防止同一链接被并发重复提交 115）
var offlinePlayMu sync.Mutex

// reMagnetBtih 磁力链接 btih 提取（offlineMineAdd 同款，共享预编译）
var reMagnetBtih = regexp.MustCompile(`(?i)btih:([A-Za-z0-9]+)`)

// offlinePlayID 链接指纹（即端点 <id>）：ed2k 取文件 hash、磁力取 btih、
// 其余取 URL sha1 前 20 位。同一资源重复入库得到同一 id，天然去重
func offlinePlayID(link string) string {
	lower := strings.ToLower(link)
	if strings.HasPrefix(lower, "ed2k://") {
		if parts := strings.Split(link, "|"); len(parts) >= 5 && parts[4] != "" {
			return strings.ToUpper(parts[4])
		}
	}
	if strings.HasPrefix(lower, "magnet:?") {
		if m := reMagnetBtih.FindStringSubmatch(link); m != nil {
			return "M" + strings.ToUpper(m[1])
		}
	}
	sum := sha1.Sum([]byte(link))
	return "U" + strings.ToUpper(hex.EncodeToString(sum[:])[:20])
}

// offlinePlayNameSize 从链接提取文件名与字节数（ed2k 自带；http/ftp 取 URL base；
// 磁力两者皆无，返回空——占位 STRM 只对拿得到文件名的链接生成）
func offlinePlayNameSize(link string) (name string, size int64) {
	lower := strings.ToLower(link)
	if strings.HasPrefix(lower, "ed2k://") {
		parts := strings.Split(link, "|")
		if len(parts) >= 3 {
			if n, err := url.QueryUnescape(parts[2]); err == nil && n != "" {
				name = n
			} else {
				name = parts[2]
			}
		}
		if len(parts) >= 4 {
			size, _ = strconv.ParseInt(parts[3], 10, 64)
		}
		return
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ftp://") {
		if u, err := url.Parse(link); err == nil {
			if base := filepath.Base(u.Path); base != "" && base != "/" && base != "." {
				if n, err := url.QueryUnescape(base); err == nil {
					name = n
				} else {
					name = base
				}
			}
		}
	}
	return
}

// offlinePlayRegister 入库登记：离线提交成功后调用。
// 落库 OfflinePlay（幂等）+ 在媒体根「按需离线/」下生成占位 STRM 指向
// 播放端点，Emby 立即可见可点。任何失败只记日志，绝不影响提交流程
func offlinePlayRegister(h *Handler, link string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[按需离线] ○ 登记异常（不影响入库）: %v", r)
		}
	}()
	id := offlinePlayID(link)
	name, size := offlinePlayNameSize(link)

	rec := model.OfflinePlay{ID: id, Link: link, Name: name, Size: size, Status: "downloading"}
	offlinePlayMu.Lock()
	var existing model.OfflinePlay
	if err := h.DB.Where("id = ?", id).First(&existing).Error; err == nil {
		// 已登记过：链接同一资源。若此前已定位到 pickcode 或已失败，保留原状态
		rec = existing
		rec.Link = link // 补全（老记录可能缺 size/name）
		if name != "" {
			rec.Name = name
		}
		if size > 0 {
			rec.Size = size
		}
		if rec.Status != "ready" {
			rec.Status = "downloading"
			rec.ErrorMsg = ""
		}
	}
	if err := h.DB.Save(&rec).Error; err != nil {
		offlinePlayMu.Unlock()
		log.Printf("[按需离线] ○ 登记落库失败: %v", err)
		return
	}
	offlinePlayMu.Unlock()

	writeOfflinePlayStrm(h, id, name)
	log.Printf("[按需离线] ✓ 已登记播放端点 /ed2k/play/%s（%s）", truncateStr(id, 16), truncateStr(name, 50))
}

// offlinePlayLocalRoot 占位 STRM 的媒体根：与增量同步同一 local_path 来源
// （yaml 配置 → 数据库回退 → 默认 /media；直读 incrParamsFromConfig 会
// 跳过数据库配置，测试与 DB-only 部署会写错位置）
func (h *Handler) offlinePlayLocalRoot() string {
	if v := h.getSettingValue("full"); v != "" {
		var cfg struct {
			LocalPath string `json:"local_path"`
		}
		if json.Unmarshal([]byte(v), &cfg) == nil && cfg.LocalPath != "" {
			return cfg.LocalPath
		}
	}
	return h.incrParamsFromConfig().LocalPath
}

// writeOfflinePlayStrm 生成占位 STRM：{本地媒体根}/按需离线/{文件名}.strm
// 内容 = {STRM直链域名}/ed2k/play/{id}（域名与普通 STRM 同源，播放时反代
// 按客户端 Host 动态改写）。name 为空（磁力）时不生成——没有文件名没有意义
func writeOfflinePlayStrm(h *Handler, id, name string) {
	if name == "" {
		return
	}
	domain, _, _, _ := h.getStrmConfig()
	dir := filepath.Join(h.offlinePlayLocalRoot(), offlinePlayStageDir)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		log.Printf("[按需离线] ○ 占位 STRM 目录创建失败: %v", err)
		return
	}
	strmPath := filepath.Join(dir, name+".strm")
	content := fmt.Sprintf("%s/ed2k/play/%s", strings.TrimRight(domain, "/"), id)
	if data, err := os.ReadFile(strmPath); err == nil && strings.TrimSpace(string(data)) == content {
		return // 内容未变不重写（保留 mtime，避免 Emby 无谓重扫）
	}
	if err := os.WriteFile(strmPath, []byte(content), 0o666); err != nil {
		log.Printf("[按需离线] ○ 占位 STRM 写入失败: %v", err)
	}
}

// removeOfflinePlayStrm 占位 STRM 退役：正式 STRM 已由同步引擎生成
// （按名字或尺寸在台账定位成功即视为存在）后调用，避免库内重复条目
func removeOfflinePlayStrm(h *Handler, name string) {
	if name == "" {
		return
	}
	p := filepath.Join(h.offlinePlayLocalRoot(), offlinePlayStageDir, name+".strm")
	if err := os.Remove(p); err == nil {
		log.Printf("[按需离线] ○ 正式 STRM 已就位，移除占位: %s", name)
		// 目录空了顺手清掉，媒体根不残留空目录
		if entries, _ := os.ReadDir(filepath.Dir(p)); len(entries) == 0 {
			_ = os.Remove(filepath.Dir(p))
		}
	}
}

// RegisterOfflinePlayRoutes 在给定引擎/分组上挂播放端点
// （6086 代理端口与主端口 /api 各挂一份，二合一部署任一可达）
func RegisterOfflinePlayRoutes(r gin.IRouter, h *Handler) {
	r.GET("/ed2k/play/:id", h.handleOfflinePlay)
}

// handleOfflinePlay GET /ed2k/play/{id} —— 按需离线播放端点
func (h *Handler) handleOfflinePlay(c *gin.Context) {
	if !proxyRateAllow(c.ClientIP()) {
		c.String(http.StatusTooManyRequests, "too many requests")
		return
	}
	id := c.Param("id")
	var rec model.OfflinePlay
	if err := h.DB.Where("id = ?", id).First(&rec).Error; err != nil {
		c.String(http.StatusNotFound, "未知播放 ID（占位 STRM 与登记记录不匹配，请重新入库）")
		return
	}

	// 快路径：已定位到 pickcode —— 与 /d/{pickcode} 同一条 302/中转出流路径
	if rec.PickCode != "" {
		servePickcodeDirect(c, h.DB, h.Config, rec.PickCode)
		return
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		c.String(http.StatusBadGateway, "115 未登录: %v", err)
		return
	}

	// 从未提交过（登记早于提交的记录/历史遗留）：此处按需提交
	if rec.Status == "pending" {
		offlinePlayMu.Lock()
		if code, msg := h.offlineSubmitCore(rec.Link, "", true); code != http.StatusOK {
			// 「任务已存在」类拒绝 = 实际已在下载，不算失败
			if !strings.Contains(msg, "已存在") && !strings.Contains(strings.ToLower(msg), "exist") {
				h.DB.Model(&rec).Updates(map[string]interface{}{"status": "failed", "error_msg": truncateStr(msg, 500)})
				offlinePlayMu.Unlock()
				c.String(http.StatusBadGateway, "离线提交失败: %s", msg)
				return
			}
		}
		h.DB.Model(&rec).Updates(map[string]interface{}{"status": "downloading", "error_msg": ""})
		offlinePlayMu.Unlock()
	}

	// 就地等待预算内轮询任务状态（秒传/小文件当场开播）
	deadline := time.Now().Add(offlinePlayWaitTotal)
	percent := 0
	sawDone := false
	for {
		task, ok := offlinePlayFindTask(cookie, rec)
		if ok {
			if task.status == 2 {
				sawDone = true
				if pick := h.offlinePlayResolve(rec); pick != "" {
					h.DB.Model(&rec).Updates(map[string]interface{}{"pick_code": pick, "status": "ready", "error_msg": ""})
					removeOfflinePlayStrm(h, rec.Name)
					log.Printf("[按需离线] ✦ 就绪开播: %s → %s", truncateStr(rec.Name, 50), truncateStr(pick, 12))
					servePickcodeDirect(c, h.DB, h.Config, pick)
					return
				}
				// 下载完成但文件还没进转存目录可见/未整理：再等下一轮，
				// 等到预算耗尽给「整理中」提示
			} else if task.status == -1 {
				msg := "115 离线任务失败（资源失效或任务报错），请重新提交"
				h.DB.Model(&rec).Updates(map[string]interface{}{"status": "failed", "error_msg": msg})
				c.String(http.StatusBadGateway, msg)
				return
			} else {
				percent = task.percent
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(offlinePlayWaitInterval)
	}

	// 预算耗尽：任务完成但尚未整理 → 试一次本地定位（转存目录/台账）
	if pick := h.offlinePlayResolve(rec); pick != "" {
		h.DB.Model(&rec).Updates(map[string]interface{}{"pick_code": pick, "status": "ready", "error_msg": ""})
		removeOfflinePlayStrm(h, rec.Name)
		servePickcodeDirect(c, h.DB, h.Config, pick)
		return
	}

	hint := "离线下载中，完成后本地址即可播放（稍后重试）"
	switch {
	case sawDone:
		hint = "下载完成，整理入库中（约 1 分钟），稍后重试即可播放"
	case percent > 0:
		hint = fmt.Sprintf("离线下载中 %d%%，完成后本地址即可播放（稍后重试）", percent)
	}
	c.Header("Retry-After", "20")
	c.String(http.StatusServiceUnavailable, hint)
}

// offlinePlayFindTask 在 115 离线任务列表里找本链接对应的任务
func offlinePlayFindTask(cookie string, rec model.OfflinePlay) (offlineTaskInfo, bool) {
	tasks, err := fetchOfflineTaskList(cookie)
	if err != nil {
		return offlineTaskInfo{}, false
	}
	return offlinePlayPickTask(rec.Link, rec.Name, tasks)
}

// offlinePlayTaskMatch 单个任务是否对应本链接。
// 匹配指纹与归属标记同款：ed2k hash / btih / 任务名（同名任务=同内容，
// 用户在 115 App 里提前下过的同一资源直接命中，不重复下载）
func offlinePlayTaskMatch(link, name string, t offlineTaskInfo) bool {
	lower := strings.ToLower(link)
	var hash string
	if strings.HasPrefix(lower, "ed2k://") {
		if parts := strings.Split(link, "|"); len(parts) >= 5 {
			hash = strings.ToUpper(parts[4])
		}
	} else if strings.HasPrefix(lower, "magnet:?") {
		if m := reMagnetBtih.FindStringSubmatch(link); m != nil {
			hash = strings.ToUpper(m[1])
		}
	}
	if hash != "" && strings.EqualFold(strings.TrimPrefix(t.key, "ed2k:"), hash) {
		return true
	}
	return name != "" && t.name == name
}

// offlinePlayPickTask 从任务列表选出本链接的任务。同一资源可能有多个
// 历史任务（如失败后重新提交），取状态最优的一个：下载中/排队 > 完成 > 失败
func offlinePlayPickTask(link, name string, tasks []offlineTaskInfo) (offlineTaskInfo, bool) {
	prio := func(status int) int {
		switch status {
		case 0, 1:
			return 2 // 排队/下载中最优
		case 2:
			return 1
		default:
			return 0 // 失败垫底
		}
	}
	best, found := offlineTaskInfo{}, false
	for _, t := range tasks {
		if !offlinePlayTaskMatch(link, name, t) {
			continue
		}
		if !found || prio(t.status) > prio(best.status) {
			best, found = t, true
		}
	}
	return best, found
}

// offlinePlayResolve 下载完成后定位 115 pickcode，顺序：
//  1. 转存目录列目录按名字匹配（刚下完、还没整理的常态路径）
//  2. 同步台账按文件名（整理+同步已完成；名字可能被整理改写则 miss）
//  3. 同步台账按字节数（ed2k 链接自带精确 size，覆盖被改名的文件）
//
// 返回空串表示暂时定位不到（稍后重试）。前两条之外的路径不动占位 STRM：
// 正式 STRM 已存在（台账命中）时由调用方移除占位
func (h *Handler) offlinePlayResolve(rec model.OfflinePlay) string {
	// 1. 转存目录（115 里文件还躺在接收文件夹时）
	if cid := h.shareFolderCid(); cid != "" {
		if ops, err := h.newPan115Ops(); err == nil {
			for offset := 0; offset < 3000; offset += 1000 {
				entries, total, err := ops.listEntries(cid, offset)
				if err != nil {
					break
				}
				for _, e := range entries {
					if firstStr(e, "n") == rec.Name {
						if pc := firstStr(e, "pc"); pc != "" {
							return pc
						}
					}
				}
				if offset+1000 >= total {
					break
				}
			}
		}
	}

	// 2/3. 台账（整理入库后；库内同名/同尺寸的正式 STRM 行）。
	// 台账 video 行的 rel_path 以 .strm 结尾，名字匹配要带上后缀
	var sf model.SyncedFile
	if rec.Name != "" &&
		h.DB.Where("kind = ? AND rel_path LIKE ?", "video", "%/"+rec.Name+".strm").
			Order("updated_at DESC").First(&sf).Error == nil && sf.PickCode != "" {
		return sf.PickCode
	}
	if rec.Size > 0 {
		if err := h.DB.Where("kind = ? AND size = ?", "video", rec.Size).
			Order("updated_at DESC").First(&sf).Error; err == nil && sf.PickCode != "" {
			return sf.PickCode
		}
	}
	return ""
}
