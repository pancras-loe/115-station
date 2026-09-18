package api

// ==================== 115 离线下载（磁力/ed2k/HTTP）====================
//
// POST https://clouddownload.115.com/lixianssp/?ac=add_task_url
// 认证：Cookie + data=RSA加密载荷（复用 115crypto 的 encrypt115）
// UA：  Mozilla/5.0 115disk/{ver} 115Browser/{ver} 115wangpan_android/{ver}

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// offlineSubmitCore 提交离线下载任务核心（/offline/add 与影巢磁力转存共用）。
// target 空则回落分享同步接收文件夹；organize 时挂秒传试探与延迟整理。
// 返回 (HTTP 状态码, 成功消息或错误信息)。
func (h *Handler) offlineSubmitCore(rawURL, target string, organize bool) (int, string) {
	// 验证链接类型
	linkType := classifyLink(rawURL)
	if linkType == "" {
		return http.StatusBadRequest, "不支持的链接类型（仅支持磁力/ed2k/HTTP/FTP）"
	}
	if linkType == "share" {
		return http.StatusBadRequest, "115 分享链接不支持离线下载，请使用「分享转存」功能（或把链接发给机器人自动转存）"
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}

	// 目标目录：参数优先，否则用分享同步配置的接收文件夹
	if target == "" {
		var cfg struct {
			Folder string `json:"folder"`
		}
		_ = json.Unmarshal([]byte(h.getSettingValue("share")), &cfg)
		target = cfg.Folder
	}

	// 用 web 版离线下载接口（Cookie 认证，表单提交）
	form := url.Values{"url": {rawURL}}
	if target != "" {
		form.Set("wp_path_id", target)
	}
	body, err := post115Form("https://115.com/web/lixian/?ac=add_task_url", form, cookie, ua115Unified(), 20*time.Second)
	if err != nil {
		return http.StatusBadGateway, "离线下载请求失败: " + err.Error()
	}
	vlog("[上传] 离线下载响应: %s", truncateStr(string(body), 150))

	// 解析响应（兼容多种错误字段名）
	var resp struct {
		State    bool            `json:"state"`
		Error    string          `json:"error"`
		ErrorMsg string          `json:"error_msg"`
		ErrNo    int             `json:"errno"`
		ErrCode  int             `json:"errcode"`
		ErrMsg   string          `json:"errMsg"`
		Message  string          `json:"message"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return http.StatusBadGateway, "解析响应失败: " + truncateStr(string(body), 300)
	}
	if !resp.State {
		// 按优先级取错误信息：error_msg > error > errMsg > message > 错误码
		errMsg := resp.ErrorMsg
		if errMsg == "" {
			errMsg = resp.Error
		}
		if errMsg == "" {
			errMsg = resp.ErrMsg
		}
		if errMsg == "" {
			errMsg = resp.Message
		}
		if errMsg == "" && resp.ErrNo != 0 {
			errMsg = fmt.Sprintf("错误码 %d", resp.ErrNo)
		}
		if errMsg == "" && resp.ErrCode != 0 {
			errMsg = fmt.Sprintf("错误码 %d", resp.ErrCode)
		}
		if errMsg == "" {
			errMsg = "未知原因（响应: " + truncateStr(string(body), 100) + "）"
		}
		return http.StatusBadGateway, errMsg
	}

	log.Printf("[上传] ✓ 离线下载任务已提交: %s（%s）", truncateStr(rawURL, 60), linkType)
	offlineMineAdd(h, rawURL)      // 归属标记：完成通知只发给 StrmHub 内提交的任务
	offlinePlayRegister(h, rawURL) // 按需离线登记：占位 STRM 指向 /ed2k/play/{id}，边下边播

	// 离线下载是异步的：提交后 10 秒先试探一轮（115 秒传命中时文件已就位，
	// CMS 同款极速响应——秒传场景 ~15 秒即开始整理）；未命中则 60 秒后再试，
	// 仍没好就交给守望者/离线任务监视器（下载完成时触发）
	if organize {
		go func() {
			time.Sleep(10 * time.Second)
			if cid := h.shareFolderCid(); cid != "" {
				if ops, err := h.newPan115Ops(); err == nil {
					if entries, _, lerr := ops.listEntries(cid, 0); lerr == nil && len(entries) > 0 {
						log.Printf("[上传] 秒传命中（转存目录已有 %d 个条目），立即整理", len(entries))
						h.triggerOrganizeAndSync()
						return
					}
				}
			}
			time.Sleep(50 * time.Second)
			h.triggerOrganizeAndSync()
		}()
	}
	return http.StatusOK, "离线下载任务已提交，下载完成后自动生成 STRM"
}

// offlineAddTask 添加离线下载任务（磁力/ed2k/HTTP/FTP）
// POST /offline/add  body: {"url":"magnet:?xt=...", "target_cid":"可选"}
func (h *Handler) offlineAddTask(c *gin.Context) {
	var req struct {
		URL      string `json:"url"`
		Target   string `json:"target_cid"`
		Organize bool   `json:"organize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写链接"})
		return
	}
	status, msg := h.offlineSubmitCore(req.URL, req.Target, req.Organize)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": msg,
		"type":    classifyLink(req.URL),
		"note":    "秒传命中约 1 分钟后 STRM 生成；新下载需等 115 下载完成后由定时任务接管",
	})
}

// offlineTaskList 查询离线下载任务列表
// GET /offline/tasks
func (h *Handler) offlineTaskList(c *gin.Context) {
	cookie, err := h.get115Cookie()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	raws, err := fetchLixianTasksRaw(cookie)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询失败: " + err.Error()})
		return
	}
	// 规范化：name / size / percent / status(-1失败 1下载中 2完成)
	items := make([]gin.H, 0, len(raws))
	for _, m := range raws {
		name := firstStr(m, "name", "task_name")
		if name == "" {
			continue
		}
		status := 0
		switch v := m["status"].(type) {
		case float64:
			status = int(v)
		case string:
			switch {
			case strings.Contains(strings.ToLower(v), "fail") || strings.Contains(v, "失败"):
				status = -1
			case strings.Contains(v, "完成") || strings.Contains(strings.ToLower(v), "done"):
				status = 2
			case strings.Contains(v, "下载"):
				status = 1
			}
		}
		delTime := int64(0)
		switch v := m["del_time"].(type) {
		case float64:
			delTime = int64(v)
		case string:
			delTime, _ = strconv.ParseInt(v, 10, 64)
		}
		items = append(items, gin.H{"name": name, "size": m["size"], "percent": m["percent"], "status": status, "del_time": delTime})
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// classifyLink 判断链接类型
func classifyLink(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "magnet:?"):
		return "magnet"
	case strings.HasPrefix(lower, "ed2k://"):
		return "ed2k"
	case strings.Contains(lower, "115.com/s/"),
		strings.Contains(lower, "115cdn.com/s/"),
		strings.Contains(lower, "anxia.com/s/"):
		// 分享链接需先于 http 判断（分享链接也是 http(s) 开头）。
		// 此前只认 115.com——115cdn.com/s/ 被当普通 http 交给离线下载，
		// 115 假成功实际只建空壳目录，永远等不到下载完成
		return "share"
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return "http"
	case strings.HasPrefix(lower, "ftp://"):
		return "ftp"
	default:
		return ""
	}
}

// triggerOrganizeAndSync 转存后自动「整理+增量同步」（整理优先，增量收尾）。
// 返回是否真正执行（false = 因互斥锁被其他任务占用而跳过）
func (h *Handler) triggerOrganizeAndSync() bool {
	time.Sleep(3 * time.Second)

	if !fullSyncMu.TryLock() {
		// 被其他任务占用：静默跳过（占用方的日志已覆盖，等下轮守望者接管）
		return false
	}
	defer fullSyncMu.Unlock()
	beginTask("自动整理+增量（转存触发）")
	defer endTask()

	start := time.Now()

	// 整理：直接扫描转存目录（转存触发时不需要扫待整理目录）
	shareFolder := ""
	var shareCfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &shareCfg)
	shareFolder = shareCfg.Folder

	if shareFolder != "" {
		orgCfg, err := h.loadOrgConfig()
		if err != nil {
			log.Printf("[上传] ○ 整理跳过（配置缺失）: %v", err)
		} else if h.dirOverlapWithLibrary(shareFolder, orgCfg.Library) {
			// 转存目录与媒体库存在包含关系（转存进了库里，或转存目录覆盖整个库）：
			// 扫描会波及库内容，直接放弃本次自动整理（引擎层守卫之外的第二道防线）
			log.Printf("[上传] ⚠ 转存目录与媒体库存在包含关系，跳过自动整理以防误搬库内容，请到「分享同步」修正转存目录")
		} else {
			// 转存目录作为待整理目录
			if orgCfg.Pending != shareFolder {
				orgCfg.Pending = shareFolder
			}
			_, _, orgErr := h.executeOrganizeWithConfig(orgCfg, false)
			if orgErr != nil {
				log.Printf("[上传] ○ 转存目录整理失败: %v", orgErr)
			}
		}
	} else {
		// 没配转存目录，退回到扫待整理目录
		_, _, orgErr := h.executeOrganize(false)
		if orgErr != nil {
			log.Printf("[上传] ○ 整理跳过: %v", orgErr)
		}
	}

	// 增量同步
	p := h.incrParamsFromConfig()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[上传] ✗ 自动增量同步失败: %v", err)
		return true
	}
	// 空转（STRM/附属都是 0）静默，只有真的生成了内容才记录
	if sum.StrmCreated+sum.AssetsDownloaded > 0 {
		log.Printf("[上传] ✅ 自动整理+增量完成，耗时 %s · STRM %d，附属 %d",
			time.Since(start).Truncate(time.Second), sum.StrmCreated, sum.AssetsDownloaded)
	}
	return true
}

// ==================== 离线任务「归属」标记 ====================
//
// 115 的离线任务列表是账号级的：用户直接在 115 App 里提交的任务也会出现。
// 通知与自动整理只应响应 StrmHub 内提交的任务——提交时把任务指纹
// （磁力 btih / ed2k hash / 原始 URL）写入持久化标记，监视器比对后才动作。

const offlineMineKey = "offline-mine"

// offlineMineLoad 读取归属标记（map 指纹 → 提交时间戳；顺带清理 7 天前的旧标记）
func offlineMineLoad(h *Handler) map[string]int64 {
	marks := map[string]int64{}
	if v := h.getSettingValue(offlineMineKey); v != "" {
		_ = json.Unmarshal([]byte(v), &marks)
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	changed := false
	for k, ts := range marks {
		if ts < cutoff {
			delete(marks, k)
			changed = true
		}
	}
	if len(marks) > 300 { // 防膨胀：只留最近的
		type kv struct {
			k  string
			ts int64
		}
		list := make([]kv, 0, len(marks))
		for k, ts := range marks {
			list = append(list, kv{k, ts})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ts > list[j].ts })
		for _, e := range list[300:] {
			delete(marks, e.k)
		}
		changed = true
	}
	if changed {
		offlineMineSave(h, marks)
	}
	return marks
}

func offlineMineSave(h *Handler, marks map[string]int64) {
	b, _ := json.Marshal(marks)
	h.Config.SaveSetting(offlineMineKey, string(b))
}

// offlineMineAdd 提交成功后登记任务指纹（magnet→btih、ed2k→hash、以及原始 URL）
// offlineFastPolls 快速轮询预算：新任务提交后 10 秒一轮、最多 5 次
// （覆盖提交后第一分钟，秒传/小任务即刻被发现），用尽回 30 秒常规节奏
// 守 115 风控；任务从排队转为下载中时重新给满 5 次
var offlineFastPolls atomic.Int64

func offlineArmFastPoll() { offlineFastPolls.Store(5) }

// offlineNextPollDelay 本轮监视器应等待的间隔（消费一次预算）
func offlineNextPollDelay() time.Duration {
	for {
		n := offlineFastPolls.Load()
		if n > 0 {
			if offlineFastPolls.CompareAndSwap(n, n-1) {
				return 10 * time.Second
			}
			continue
		}
		return 30 * time.Second
	}
}

func offlineMineAdd(h *Handler, rawURL string) {
	lower := strings.ToLower(rawURL)
	var keys []string
	if strings.HasPrefix(lower, "magnet:?") {
		if m := reMagnetBtih.FindStringSubmatch(rawURL); m != nil {
			keys = append(keys, "btih:"+strings.ToUpper(m[1]))
		}
	} else if strings.HasPrefix(lower, "ed2k://") {
		// ed2k 链接倒数第二段是文件 hash
		parts := strings.Split(rawURL, "|")
		if len(parts) >= 5 {
			keys = append(keys, "ed2k:"+strings.ToUpper(parts[4]))
		}
	}
	// 名称标记：115 对 HTTP/FTP 任务用链接里的文件名作为任务名，监视器
	// 按「name:任务名」兜底匹配（磁力无名称，靠 btih 匹配）
	if strings.HasPrefix(lower, "ed2k://") {
		if parts := strings.Split(rawURL, "|"); len(parts) >= 3 && parts[2] != "" {
			keys = append(keys, "name:"+parts[2])
		}
	} else if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ftp://") {
		if u, err := url.Parse(rawURL); err == nil {
			if base := path.Base(u.Path); base != "" && base != "/" && base != "." {
				keys = append(keys, "name:"+base)
			}
		}
	}
	keys = append(keys, "url:"+rawURL)
	marks := offlineMineLoad(h)
	now := time.Now().Unix()
	for _, k := range keys {
		marks[k] = now
	}
	offlineMineSave(h, marks)
	offlineArmFastPoll() // 新任务提交：监视器切快速轮询（10s×5）
}

// offlineMineMatch 任务是否属于 StrmHub 提交（info_hash / 任务名指纹 / URL 任一命中；
// 115 对磁力任务返回的 info_hash 即 btih；名称兜底用于 HTTP/ed2k 无 hash 场景）
func offlineMineMatch(h *Handler, marks map[string]int64, key, name string) bool {
	if _, ok := marks["btih:"+strings.ToUpper(key)]; ok {
		return true
	}
	if _, ok := marks["ed2k:"+strings.ToUpper(key)]; ok {
		return true
	}
	if name != "" {
		if _, ok := marks["name:"+name]; ok {
			return true
		}
	}
	return false
}

// StartTransferWatcher 转存目录守望者：每分钟检查转存目录，发现内容且
// 无任务运行时触发「自动整理+增量」。磁力/离线下载完成时间不可控，
// 提交 60 秒后的触发器常在下载完成前跑掉，定时任务又要等下一轮 cron——
// 守望者保证下载完成后约 1 分钟内被接管。5 分钟冷却防止整理失败时死循环
func StartTransferWatcher(h *Handler) {
	go func() {
		lastTrigger := time.Time{}
		failCount := 0 // 整理后目录仍未清空的连续次数
		pauseUntil := time.Time{}
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(60 * time.Second):
			}
			if time.Now().Before(pauseUntil) {
				continue // 熔断暂停中
			}
			if time.Since(lastTrigger) < 5*time.Minute {
				continue
			}
			cid := h.shareFolderCid()
			if cid == "" {
				continue
			}
			ops, err := h.newPan115Ops()
			if err != nil {
				continue
			}
			entries, _, err := ops.listEntries(cid, 0)
			if err != nil || len(entries) == 0 {
				continue
			}
			// 有内容：触发整理（内部自带互斥、与媒体库重叠校验、3 秒沉淀）。
			// 发现本身不记日志——整理引擎会输出目录扫描结果，避免重复两行
			ran := h.triggerOrganizeAndSync()
			// 成功清空后冷却只要 60 秒（连续多个下载先后完成时快速接续）：
			// 闸门是 since(lastTrigger)≥5min，把锚点拨回 4 分钟前即再等 60 秒
			//（此前写成 +4min，实际冷却 9 分钟，快速接续从未生效）
			lastTrigger = time.Now().Add(-4 * time.Minute)
			if !ran {
				lastTrigger = time.Now()
				continue // 其他任务占用，本轮不计成败
			}
			// 整理后复查：目录清空 = 成功；仍有条目 = 一轮失败。
			// 连续 3 次失败（如整理反复报错/内容无法处理）后暂停 30 分钟，
			// 避免每 5 分钟无限重试刷日志
			remaining, _, rerr := ops.listEntries(cid, 0)
			switch {
			case rerr != nil:
				// 查询失败≠未清空：不计失败次数（此前三次瞬时抖动就误触 30 分钟熔断）
				log.Printf("[守望] ○ 复查转存目录失败（不计失败次数）: %v", rerr)
			case len(remaining) == 0:
				failCount = 0
			default:
				lastTrigger = time.Now() // 失败：恢复 5 分钟冷却
				failCount++
				log.Printf("[守望] ○ 整理后转存目录仍有内容（第 %d 次未清空）", failCount)
				if failCount >= 3 {
					pauseUntil = time.Now().Add(30 * time.Minute)
					log.Printf("[守望] ⚠ 连续 %d 次未能清空转存目录，暂停守望 30 分钟（请查看整理日志定位失败原因）", failCount)
					failCount = 0
				}
			}
		}
	}()
}

// StartOfflineTaskMonitor 离线任务监视器：30 秒轮询 115 离线任务列表。
// 磁力下载不是百分百成功（资源失效/任务报错都常见），且完成时间不可控：
//   - 任务完成（status=2）→ 立即触发整理（不等 60 秒目录轮询）
//   - 任务失败（status=-1）→ 日志告警（此前静默失败，用户永远等不到）
//
// 状态码语义与 LitePan/openapi 一致：-1 失败 / 0 排队 / 1 下载中 / 2 完成
func StartOfflineTaskMonitor(h *Handler) {
	go func() {
		bootAt := time.Now().Unix()    // 只处理本进程启动之后完成的任务
		lastStatus := map[string]int{} // info_hash/url → 上次状态
		notified := map[string]bool{}  // 已处理过的终态任务（避免重复告警/触发）
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(offlineNextPollDelay()):
			}
			cookie, err := h.get115Cookie()
			if err != nil {
				continue
			}
			tasks, err := fetchOfflineTaskList(cookie)
			if err != nil {
				continue // 网络抖动/接口拒绝：下轮再看
			}
			mine := offlineMineLoad(h) // StrmHub 提交的归属标记（App 里提交的不在标记内 → 不通知）
			// 本轮新完成的任务（聚合为一条通知+一次整理触发，防止批量完成时
			// 一秒内发几十条企微消息、并发几十个整理触发）
			var doneNames, failedNames []string
			for _, t := range tasks {
				key := t.key
				prev, seen := lastStatus[key]
				lastStatus[key] = t.status
				if notified[key] || (seen && prev == t.status) {
					continue
				}
				// 归属过滤：只响应 StrmHub 内提交的任务（用户直接在 115 App
				// 提交的磁力/链接不发通知、不触发整理）
				if !offlineMineMatch(h, mine, key, t.name) {
					continue
				}
				// 排队 → 下载中：115 真正开始下载，再给 5 次快速轮询
				//（完成后 10 秒内即可发现并整理）
				if seen && prev == 0 && t.status == 1 {
					offlineArmFastPoll()
				}
				// !seen 且已终态的任务：只处理「本进程启动之后完成的」——
				// 115 任务列表会长期保留全部历史任务，重启后首轮若不加时间
				// 门槛，会把陈年旧账全部当成新完成的通知+触发整理（刷屏事故）
				if !seen {
					if t.delTime == 0 || t.delTime <= bootAt {
						continue
					}
				}
				switch t.status {
				case 2: // 完成
					log.Printf("[离线] 下载完成: %s，开始整理入库", truncateStr(t.name, 60))
					notified[key] = true
					doneNames = append(doneNames, truncateStr(t.name, 80))
				case -1: // 失败
					log.Printf("[离线] 下载失败: %s（资源问题或 115 任务报错，请重新提交）", truncateStr(t.name, 60))
					failedNames = append(failedNames, truncateStr(t.name, 80))
					notified[key] = true
				}
			}
			// 聚合通知：本轮全部新完成/失败合并为一条（名称列表截断防超长）
			clip := func(names []string, keep int) string {
				if len(names) <= keep {
					return strings.Join(names, "\n")
				}
				return strings.Join(names[:keep], "\n") + fmt.Sprintf("\n…等共 %d 个", len(names))
			}
			if len(doneNames) > 0 {
				NotifyMessage("✓ 离线下载完成", fmt.Sprintf("%d 个任务完成，已开始整理入库（完成后另行通知）：\n%s",
					len(doneNames), clip(doneNames, 15)))
				go h.triggerOrganizeAndSync()
			}
			if len(failedNames) > 0 {
				NotifyMessage("✗ 磁力下载失败", fmt.Sprintf("%d 个任务失败（115 离线任务报错，请检查资源或重新提交）：\n%s",
					len(failedNames), clip(failedNames, 15)))
			}
			// 终态表防膨胀：只保留最近一轮见到的任务
			if len(notified) > 500 {
				notified = map[string]bool{}
			}
			if len(lastStatus) > 1000 {
				lastStatus = map[string]int{}
			}
		}
	}()
}

// fetchLixianTasksRaw 拉取离线任务原始列表（明文 web 接口，与 add_task_url 同通道；
// 兼容 data/info、数组、{tasks}/{list} 多种响应形态）
func fetchLixianTasksRaw(cookie string) ([]map[string]interface{}, error) {
	body, err := post115Form("https://115.com/web/lixian/?ac=task_lists", url.Values{}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		return nil, err
	}
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.State {
		msg := resp.Error
		if msg == "" {
			msg = truncateStr(string(body), 120)
		}
		return nil, fmt.Errorf("115 拒绝: %s", msg)
	}
	items, ok := extractTaskItems(body)
	if !ok {
		return nil, fmt.Errorf("任务列表结构无法解析: %s", truncateStr(string(body), 150))
	}
	return items, nil
}

// extractTaskItems 从 115 离线任务响应中形态宽容地提取任务数组：
// 依次尝试 顶层数组 / {data|info: [...] } / {data|info: {tasks|list|info: [...]}} / 顶层 {tasks|list|info: [...]}
func extractTaskItems(body []byte) ([]map[string]interface{}, bool) {
	// 顶层直接是数组
	var arr []map[string]interface{}
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return arr, true
	}
	var top map[string]interface{}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, false
	}
	for _, k := range []string{"data", "info"} {
		if v, ok := top[k]; ok {
			if items, ok := unwrapItems(v); ok {
				return items, true
			}
		}
	}
	if items, ok := unwrapItems(top); ok {
		return items, true
	}
	return nil, false
}

func unwrapItems(v interface{}) ([]map[string]interface{}, bool) {
	switch t := v.(type) {
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(t))
		for _, it := range t {
			if m, ok := it.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out, true
	case map[string]interface{}:
		for _, k := range []string{"tasks", "list", "info"} {
			if arr, ok := t[k].([]interface{}); ok {
				out := make([]map[string]interface{}, 0, len(arr))
				for _, it := range arr {
					if m, ok := it.(map[string]interface{}); ok {
						out = append(out, m)
					}
				}
				return out, true
			}
		}
	}
	return nil, false
}

// offlineTaskInfo 离线任务摘要
type offlineTaskInfo struct {
	key     string // info_hash 优先，空则 name
	name    string
	status  int
	percent int   // 下载进度 0-100（下载中提示用）
	delTime int64 // 完成时间戳（秒；115 任务列表会长期保留历史任务，用它区分新旧）
}

// fetchOfflineTaskList 拉取离线任务列表（web lixian 加密接口，防御式解析）
func fetchOfflineTaskList(cookie string) ([]offlineTaskInfo, error) {
	raws, err := fetchLixianTasksRaw(cookie)
	if err != nil {
		return nil, err
	}
	tasks := make([]offlineTaskInfo, 0, len(raws))
	for _, m := range raws {
		name := firstStr(m, "name", "task_name")
		if name == "" {
			continue
		}
		status := 0
		switch v := m["status"].(type) {
		case float64:
			status = int(v)
		case string:
			switch {
			case strings.Contains(strings.ToLower(v), "fail") || strings.Contains(v, "失败"):
				status = -1
			case strings.Contains(v, "完成") || strings.Contains(strings.ToLower(v), "done"):
				status = 2
			case strings.Contains(v, "下载"):
				status = 1
			}
		}
		key := firstStr(m, "info_hash", "infoHash")
		if key == "" {
			key = name
		}
		percent := 0
		switch v := m["percent"].(type) {
		case float64:
			percent = int(v)
		case string:
			if f, perr := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64); perr == nil {
				percent = int(f)
			}
		}
		delTime := int64(0)
		switch v := m["del_time"].(type) {
		case float64:
			delTime = int64(v)
		case string:
			delTime, _ = strconv.ParseInt(v, 10, 64)
		}
		tasks = append(tasks, offlineTaskInfo{key: key, name: name, status: status, percent: percent, delTime: delTime})
	}
	return tasks, nil
}

// shareFolderCid 读取分享同步配置的转存目录 cid（未配置返回空）
func (h *Handler) shareFolderCid() string {
	var cfg struct {
		Folder string `json:"folder"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("share")), &cfg)
	return cfg.Folder
}

// dirInside 判断 childCid 是否位于 parentCid 子树内（含相等），取不到路径返回 false
func (h *Handler) dirInside(childCid, parentCid string) bool {
	if childCid == "" || parentCid == "" || childCid == parentCid {
		return childCid == parentCid && childCid != ""
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return false
	}
	memo := map[string]dirInfo{}
	childAbs := strings.TrimSuffix(absPathOf(cookie, childCid, memo), "/")
	parentAbs := strings.TrimSuffix(absPathOf(cookie, parentCid, memo), "/")
	if childAbs == "" || parentAbs == "" {
		return false
	}
	return childAbs == parentAbs || strings.HasPrefix(childAbs+"/", parentAbs+"/")
}

// dirOverlapWithLibrary 判断转存目录与媒体库是否存在包含关系
// （相等、转存目录在库内、或转存目录覆盖整个库都算）。取不到路径时返回
// false 放行，由引擎层的 orgGuards 兜底
func (h *Handler) dirOverlapWithLibrary(shareCid, libCid string) bool {
	if shareCid == "" || libCid == "" {
		return false
	}
	if shareCid == libCid {
		return true
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return false
	}
	memo := map[string]dirInfo{}
	shareAbs := strings.TrimSuffix(absPathOf(cookie, shareCid, memo), "/")
	libAbs := strings.TrimSuffix(absPathOf(cookie, libCid, memo), "/")
	if shareAbs == "" || libAbs == "" {
		return false
	}
	return shareAbs == libAbs ||
		strings.HasPrefix(shareAbs+"/", libAbs+"/") || // 转存目录在媒体库内
		strings.HasPrefix(libAbs+"/", shareAbs+"/") // 转存目录覆盖媒体库
}

// triggerIncrementalAfterTransfer 转存/离线下载成功后立即触发增量同步
// （协程内执行，不阻塞 HTTP 响应；等 3 秒让 115 服务端写完文件索引）
func (h *Handler) triggerIncrementalAfterTransfer() {
	time.Sleep(3 * time.Second)

	if !fullSyncMu.TryLock() {
		log.Printf("[上传] ○ 增量同步已跳过（已有任务运行中）")
		return
	}
	defer fullSyncMu.Unlock()
	beginTask("增量同步（转存触发）")
	defer endTask()

	p := h.incrParamsFromConfig()
	log.Printf("[上传] ▶ 转存/下载后自动增量同步开始...")
	start := time.Now()
	sum, err := h.executeIncrementalSync(p)
	if err != nil {
		log.Printf("[上传] ✗ 自动增量同步失败: %v", err)
		return
	}
	log.Printf("[上传] ✅ 自动增量同步完成，耗时 %s · STRM %d，附属 %d",
		time.Since(start).Truncate(time.Second), sum.StrmCreated, sum.AssetsDownloaded)
}
