package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// EmbyWebhook POST /api/emby/webhook —— 接收 Emby Webhooks 插件的事件推送，转发为企微/TG 通知
// 可选鉴权：配置的 Webhook 地址带 ?token=xxx 时，请求需携带相同 token 才被接受
func (h *Handler) EmbyWebhook(c *gin.Context) {
	var notifyCfg struct {
		Webhook string `json:"webhook"`
		Token   string `json:"token"`
	}
	if v := h.getSettingValue("emby-notify"); v != "" {
		_ = json.Unmarshal([]byte(v), &notifyCfg)
	}
	// 鉴权 token：优先专用 token 字段（界面自动生成），兼容旧版存在 webhook URL 里的 token
	wantToken := strings.TrimSpace(notifyCfg.Token)
	if wantToken == "" && notifyCfg.Webhook != "" {
		if u, err := url.Parse(notifyCfg.Webhook); err == nil {
			wantToken = u.Query().Get("token")
		}
	}
	// token 强制：未配置时拒绝处理（此前可空，任何人可伪造入库/播放事件，
	// 借站长通知通道外发垃圾内容）。界面会自动生成 token
	if wantToken == "" {
		log.Printf("[Emby] ✗ webhook 未配置鉴权 token，已拒绝处理（到 消息配置 生成 token 并更新 Emby 的 webhook URL）")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "webhook 未配置鉴权 token：请到 消息配置 里生成，并更新 Emby 的 webhook URL（加 &token=xxx）"})
		return
	}
	if got := c.Query("token"); got != wantToken {
		// 此前这里不留日志，token 对不上与「Emby 根本没推」在本站看起来一模一样。
		// 只说带没带、对不对，不打明文：日志页谁都可能截图外传
		why := "URL 未带 token"
		if got != "" {
			why = "token 与消息配置里的不一致（重新生成过 token 的话，Emby 里的 URL 要一起更新）"
		}
		webhookRejectLog("token", "[Emby Webhook] ✗ 拒绝来自 %s 的推送：%s", c.ClientIP(), why)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token 无效"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		webhookRejectLog("read", "[Emby Webhook] ✗ 读取来自 %s 的请求体失败: %v", c.ClientIP(), err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求失败"})
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		// Emby 通知里内容类型选成 form 时会走到这里，把开头打出来一眼就能看出来
		webhookRejectLog("json", "[Emby Webhook] ✗ 来自 %s 的请求体不是 JSON（Content-Type=%q）: %v，开头: %q",
			c.ClientIP(), c.ContentType(), err, truncateStr(string(body), 200))
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 JSON"})
		return
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := payload[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	event := strings.ToLower(getStr("Event", "event", "NotificationType", "notification_type"))
	// 片名：兼容 Emby 官方 Item.Name 与 Plex 兼容格式的 Metadata.title（社区 Webhooks 插件）
	getNested := func(holders []string, keys []string) string {
		for _, holder := range holders {
			if m, ok := payload[holder].(map[string]interface{}); ok {
				for _, k := range keys {
					if v, ok := m[k].(string); ok && v != "" {
						return v
					}
				}
			}
		}
		return ""
	}
	itemName := getNested([]string{"Item", "Metadata"}, []string{"Name", "title", "fullTitle"})
	userName := getNested([]string{"User", "Account"}, []string{"Name", "title"})
	// 剧集单集的 Name 通常只有"第 11 集"，拼上剧集名与季名（Emby: SeriesName/SeasonName；Plex 兼容: grandparentTitle/parentTitle）
	if series := getNested([]string{"Item", "Metadata"}, []string{"SeriesName", "grandparentTitle"}); series != "" && !strings.Contains(itemName, series) {
		ep := itemName
		if season := getNested([]string{"Item", "Metadata"}, []string{"SeasonName", "parentTitle"}); season != "" && !strings.Contains(itemName, season) {
			ep = season + " " + ep
		}
		itemName = series + " " + ep
	}

	category, title := embyEventCategory(event)
	if category == "test" {
		// Emby 侧点「测试通知」发来的连通测试事件：转发一条测试消息，方便确认全链路
		log.Printf("[Emby Webhook] 收到 Emby 测试事件，转发连通测试通知")
		go NotifyMessage("✅ Emby Webhook 连通测试", "Emby → 115-Station → 企微/TG 链路正常")
		c.JSON(http.StatusOK, gin.H{"message": "ok（测试事件，已转发）"})
		return
	}
	if category == "" {
		log.Printf("[Emby Webhook] 未识别的事件类型 %q，已忽略", event)
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
		return
	}

	if category == "added" {
		// 入库事件走聚合富通知（封面/评分取自 Emby；15 秒防抖合并）
		go h.queueEmbyAddedNotif(payload)
		// 回查放弃过的入库（Emby 扫得太慢）在这里补上提前探测
		if ip := getNested([]string{"Item"}, []string{"Path"}); ip != "" {
			go embyLateIngestHit(ip)
		}
		log.Printf("[Emby Webhook] Emby 入库事件: %s", itemName)
		c.JSON(http.StatusOK, gin.H{"message": "ok（已进入入库通知队列）"})
		return
	}
	if category == "deleted" {
		itemPath := getNested([]string{"Item"}, []string{"Path"})
		itemType := getNested([]string{"Item"}, []string{"Type"})
		log.Printf("[Emby Webhook] 删除事件: event=%s type=%s path=%s", event, itemType, itemPath)
		// 原始载荷只在详细日志里打：字段形态已经实测清楚（见 DEEP-DELETE-PLAN.md §8），
		// 常驻打整包会把实时日志页淹掉——Overview 一个字段就上千字
		vlog("[Emby Webhook] 删除事件原始载荷: %s", truncateStr(string(body), 2000))

		// STRM 改名迁移之后 Emby 扫库清掉旧路径条目的回声：不深删、不通知。
		// 只对原生 library.deleted 生效 —— 神医 deep.delete 是用户在 Emby 里主动删的，照常处理
		deepEvent := strings.Contains(event, "deep.delete")
		if !deepEvent && strmMigrateEcho(h.mapFromEmbyPath(itemPath)) {
			vlog("[Emby Webhook] STRM 改名迁移的回声（%s），跳过深删与通知", itemName)
			c.JSON(http.StatusOK, gin.H{"message": "ok（STRM 改名迁移回声，已跳过）"})
			return
		}

		// 通知与深删结果合成一条（embydelnotify.go）：
		//   - 目录条目（Folder）是 Emby 在收拾空目录，不推；
		//   - 本站自己删的（洗版让位、增量同步清 strm、深度删除）是我们动作的回声，不推；
		//   - 装了神医助手时 deep.delete 与 library.deleted 两条都发（实测，不是替换），
		//     按条目 id 聚成一份。两条到达只差几毫秒且顺序不保证，不能假设谁先谁后。
		// 每条事件的深删都照跑：神医事件可能带着原生事件没有的定位信息
		label, notifiable := embyDeleteKind(itemType)
		title, content := "", itemName
		switch {
		case !notifiable:
			log.Printf("[Emby Webhook] ○ 目录条目的删除（%s，%s），不推通知", itemType, itemName)
		case embySelfDeleted(itemPath) || (embyIsDirItemType(itemType) && embySelfDeletedRelated(itemPath)):
			log.Printf("[Emby Webhook] 本站自产的删除事件（%s），跳过通知", itemName)
		default:
			title = embyDeleteTitle(label)
			if content == "" {
				content = event
			}
			item, _ := payload["Item"].(map[string]interface{})
			if y, ok := item["ProductionYear"].(float64); ok && y > 0 {
				if ys := fmt.Sprintf("%d", int(y)); !strings.Contains(content, ys) {
					content += " (" + ys + ")"
				}
			}
			log.Printf("[Emby Webhook] %s %s", title, content)
		}
		key := getNested([]string{"Item"}, []string{"Id"})
		if key == "" {
			key = itemPath
		}
		trackEmbyDelete(key, title, content, func() deepDelOutcome {
			out := h.deepDelOnEmbyDelete(payload, deepEvent)
			if !h.loadDeepDelCfg().notify() {
				return deepDelOutcome{} // 用户关了深删通知：消息里不带深删结果
			}
			return out
		})
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
		return
	}
	content := itemName
	if content == "" {
		content = event
	}
	if userName != "" && category != "deleted" {
		content = userName + "：" + content
	}
	log.Printf("[Emby Webhook] %s %s", title, content)
	go NotifyMessage(title, content)
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ---- 拒收日志节流 ----
//
// webhook 端点不需要登录，拒收日志不节流的话，谁都能拿错 token 刷请求把实时日志页淹掉。
// 同一类原因每分钟最多打一行，压下去的次数并进下一行里。
var webhookReject = struct {
	sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
}{last: map[string]time.Time{}, suppressed: map[string]int{}}

const webhookRejectEvery = time.Minute

func webhookRejectLog(kind, format string, args ...any) {
	webhookReject.Lock()
	now := time.Now()
	if t, ok := webhookReject.last[kind]; ok && now.Sub(t) < webhookRejectEvery {
		webhookReject.suppressed[kind]++
		webhookReject.Unlock()
		return
	}
	n := webhookReject.suppressed[kind]
	webhookReject.last[kind] = now
	webhookReject.suppressed[kind] = 0
	webhookReject.Unlock()

	msg := fmt.Sprintf(format, args...)
	if n > 0 {
		msg += fmt.Sprintf("（此前一分钟内同类拒收另有 %d 次未记录）", n)
	}
	log.Print(msg)
}

// embyEventCategory webhook 事件 → 归类 + 通知标题（event 已转小写）。
//
// ⚠️ **Emby 的入库事件叫 `library.new`，不叫 `item.added`。**
// 此前这里判的是 `Contains(event, "add")`，`library.new` 一个字都对不上，
// 于是整条 Emby 入库通知从来没响过 —— 更坑的是 embyrefresh 那边一旦发现
// 配了 webhook 就会把自己那条「🎬 媒体入库」压掉（让位给带海报的富卡片），
// 结果是配了 webhook 反而彻底收不到入库消息。事件名对照 qmediasync
// `internal/controllers/emby.go`（`library.new` / `library.deleted`）。
//
// 判定取「最后一段动作词」而不是整串 Contains：Emby 事件形如 `playback.stop`、
// `playback.unpause`，整串里都带着 "play"，按 Contains 的顺序去猜必然出错 ——
// `playback.unpause`（继续播放）此前就被判成了「暂停」。
// Jellyfin 的 NotificationType 没有点号（`ItemAdded` / `PlaybackStart`），
// 整串就是动作词，同一套判定也能吃下。
func embyEventCategory(event string) (category, title string) {
	action := event
	if i := strings.LastIndex(event, "."); i >= 0 {
		action = event[i+1:]
	}
	has := func(sub string) bool { return strings.Contains(action, sub) }
	switch {
	case has("test"):
		return "test", ""
	case has("delete"), has("remove"):
		return "deleted", "🗑️ Emby 删除"
	// `library.new` 是 Emby 的入库事件；`device.new` 同样以 new 结尾，
	// 但那是「发现新设备」，不能当入库报
	case has("new") && !strings.HasPrefix(event, "device."), has("add"):
		return "added", "🎬 Emby 入库"
	// markplayed/markunplayed 是「标记已看」，带着 play 但不是播放事件
	case has("mark"), has("progress"):
		return "", ""
	case has("stop"):
		return "pause", "⏸️ Emby 暂停/停止"
	case has("unpause"), has("resume"):
		return "play", "▶️ Emby 播放"
	case has("pause"):
		return "pause", "⏸️ Emby 暂停/停止"
	case has("play"), has("start"):
		return "play", "▶️ Emby 播放"
	}
	return "", ""
}

// queueEmbyAddedNotif Emby 入库事件 → 聚合队列（Emby 封面优先 + 播放链接）
func (h *Handler) queueEmbyAddedNotif(payload map[string]interface{}) {
	item, _ := payload["Item"].(map[string]interface{})
	str := func(m map[string]interface{}, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	// 重新整理会先删掉错误标题目录，再让 Emby 扫入正确目录。Emby 可能在几分钟后
	// 才把旧扫描任务的 library.new 送回来；旧路径此时已经不存在，不能再算一部入库。
	if itemPath := str(item, "Path"); itemPath != "" {
		localPath := h.mapFromEmbyPath(itemPath)
		if managedMediaPathGone(h.orphanLocalRoot(), localPath) {
			log.Printf("[Emby Webhook] ○ 忽略已不存在路径的过期入库事件: %s", itemPath)
			return
		}
		// 网盘上改个名、挪个位置，增量同步跟着重建 STRM，Emby 扫完推回一条
		// library.new —— 片子还是原来那部，报「入库」是假消息。
		// 同窗口内这条路径下真有新增内容时不会命中（见 embyRenameEcho）
		if embyRenameEcho(localPath) || strmMigrateEcho(localPath) {
			log.Printf("[Emby Webhook] ○ 本站改名/移动的回声，跳过入库通知: %s", itemPath)
			return
		}
	}
	// 剧集条目用剧集名（单集标题没有辨识度）
	title := str(item, "SeriesName")
	year := ""
	if title == "" {
		title = str(item, "Name")
	}
	if y, ok := item["ProductionYear"].(float64); ok && y > 0 {
		year = fmt.Sprintf("%d", int(y))
	}
	if title == "" {
		return
	}
	typeLabel := "电影"
	if t := str(item, "Type"); t == "Episode" || t == "Series" {
		typeLabel = "剧集"
	}
	entry := mediaNotifEntry{Title: title, Year: year, Kind: typeLabel, Source: "emby"}
	if r, ok := item["CommunityRating"].(float64); ok && r > 0 {
		entry.Rating = r
	}
	// 洗版替换的说明挂到这部片的卡片上，不再单独推一条
	entry.Notes = washNotesFor(entry.mergeKey())
	// Emby 封面与播放链接
	if base, apiKey, ok := h.embyServerInfo(); ok {
		if id := str(item, "Id"); id != "" {
			qs := ""
			if apiKey != "" {
				qs = "?api_key=" + url.QueryEscape(apiKey)
			}
			imgURL := base + "/Items/" + id + "/Images/Primary" + qs
			entry.PosterAlt = imgURL
			entry.Link = base + "/web/index.html#!/item?id=" + id
			if data, err := fetchHTTPBytes(imgURL, 8*time.Second); err == nil && len(data) > 0 {
				entry.PosterData = data
			}
		}
	}
	QueueMediaNotif(entry)
}

// managedMediaPathGone 只判断本站媒体根内的路径。外部 Emby 媒体库不在本站文件系统上，
// Stat 不到不能据此吞事件；权限错误、挂载异常同样按“不确定”保留通知。
func managedMediaPathGone(root, localPath string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(localPath) == "" {
		return false
	}
	root = filepath.Clean(root)
	localPath = filepath.Clean(localPath)
	rel, err := filepath.Rel(root, localPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	_, err = os.Stat(localPath)
	return os.IsNotExist(err)
}

// TestEmbyConnection 测试 Emby 服务器连接
// POST /config/test-emby  body: {"server_url":"...", "api_key":"..."}
func (h *Handler) TestEmbyConnection(c *gin.Context) {
	var req struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ServerURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 Emby 服务器地址"})
		return
	}

	// 页面加载时 GET /config/setting 把 api_key 脱敏成掩码，用户没改就点测试，
	// 发来的是掩码本身 —— 换成已保存的密钥，与保存时 unmaskSensitiveJSON 同口径
	if strings.TrimSpace(req.APIKey) == settingMask {
		saved, _ := loadEmbyRefreshCfg()
		req.APIKey = saved.APIKey
	}

	base := strings.TrimRight(req.ServerURL, "/")
	q := ""
	if req.APIKey != "" {
		q = "?api_key=" + url.QueryEscape(req.APIKey)
	}

	// 获取服务器信息
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(base + "/System/Info" + q)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "无法连接: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "API 密钥无效或未填写"})
		return
	}
	if resp.StatusCode != 200 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return
	}
	var info struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	_ = json.Unmarshal(body, &info)

	// 获取媒体库数量
	libraryCount := 0
	if resp2, err := client.Get(base + "/Library/MediaFolders" + q); err == nil {
		defer resp2.Body.Close()
		var libs struct {
			Items []struct {
				ID string `json:"Id"`
			} `json:"Items"`
		}
		body2, _ := io.ReadAll(resp2.Body)
		if json.Unmarshal(body2, &libs) == nil {
			libraryCount = len(libs.Items)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"server_name":   info.ServerName,
		"version":       info.Version,
		"library_count": libraryCount,
	})
}

// embyIsDirItemType 剧集 / 季这类按目录存在的条目（删除回声按路径包含关系认，见 embySelfDeletedRelated）
func embyIsDirItemType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "series", "season":
		return true
	}
	return false
}
