package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== EMBY 入库刷新通知 ====================

// notifyEmbyRefresh STRM 生成后通知 Emby 刷新入库
// 统一使用 EMBY管理 配置（emby：服务器地址/API密钥/本地路径映射/路径风格/入库时刷新），
// 兼容旧版 emby-refresh（path_rule/enabled）与从 webhook 地址提取 host 的配置方式
func (h *Handler) notifyEmbyRefresh(localPath string) {
	// emby 配置走 YAML 优先（settingValueCompat），与保存路径一致；
	// 此前直查 DB：新装环境 DB 无 emby 行 → 刷新永不触发；老环境读到过期配置
	v := settingValueCompat("emby")
	if v == "" {
		return
	}
	var cfg struct {
		Style          string `json:"style"`
		RefreshEnabled any    `json:"refresh_enabled"`
	}
	if json.Unmarshal([]byte(v), &cfg) != nil {
		return
	}
	// 检查是否启用；未写 refresh_enabled 时回退旧版 emby-refresh.enabled
	enabled := true
	switch v := cfg.RefreshEnabled.(type) {
	case bool:
		enabled = v
	case string:
		enabled = v == "true"
	default:
		var old struct {
			Enabled any `json:"enabled"`
		}
		if v2 := h.getSettingValue("emby-refresh"); v2 != "" && json.Unmarshal([]byte(v2), &old) == nil {
			if b, ok := old.Enabled.(bool); ok {
				enabled = b
			} else if str, ok := old.Enabled.(string); ok {
				enabled = str == "true"
			}
		}
	}
	if !enabled {
		return
	}

	// 路径替换：本地路径映射（emby.path_mapping，与建库插件/6086 反代共用）
	embyPath := h.mapToEmbyPath(localPath)
	style := cfg.Style
	if style == "" {
		var old struct {
			Style string `json:"style"`
		}
		if v2 := h.getSettingValue("emby-refresh"); v2 != "" && json.Unmarshal([]byte(v2), &old) == nil {
			style = old.Style
		}
	}
	if style == "windows" {
		embyPath = strings.ReplaceAll(embyPath, "/", "\\")
	}

	// Emby 地址与密钥：优先 EMBY管理 配置；未配置地址时回退从 webhook URL 提取（旧版）
	embyServer, apiKey, ok := h.embyServerInfo()
	if !ok {
		var embySetting model.Setting
		if h.DB.Where("key = ?", "emby-notify").First(&embySetting).Error != nil {
			return
		}
		var embyCfg struct {
			Webhook string `json:"webhook"`
		}
		json.Unmarshal([]byte(embySetting.Value), &embyCfg)
		// webhook 格式: http://ip:port/api/emby/webhook?token=xxx
		if embyCfg.Webhook == "" {
			return
		}
		u, err := url.Parse(embyCfg.Webhook)
		if err != nil {
			return
		}
		embyServer = u.Scheme + "://" + u.Host
		apiKey = ""
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// 优先按库刷新（CMS 同款）：取媒体库列表，将变更路径映射到所属库后逐库 Refresh
	apiKey = strings.TrimSpace(apiKey)
	q := ""
	if apiKey != "" {
		q = "?api_key=" + url.QueryEscape(apiKey)
	}
	libResp, err := client.Get(embyServer + "/Library/MediaFolders" + q)
	if err == nil {
		defer libResp.Body.Close()
		var libs struct {
			Items []struct {
				ID        string   `json:"Id"`
				Name      string   `json:"Name"`
				Locations []string `json:"Locations"`
			} `json:"Items"`
		}
		if json.NewDecoder(libResp.Body).Decode(&libs) == nil {
			refreshed := 0
			libNames := ""
			for _, lib := range libs.Items {
				hit := false
				for _, loc := range lib.Locations {
					if strings.HasPrefix(embyPath, strings.TrimRight(loc, "/")+"/") || embyPath == loc {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
				r, err := client.Post(embyServer+"/Library/"+lib.ID+"/Refresh"+q, "", nil)
				if err != nil {
					log.Printf("Emby 按库刷新失败 %s: %v", lib.Name, err)
					continue
				}
				r.Body.Close()
				refreshed++
				log.Printf("Emby 媒体库刷新任务提交成功：%s %s", lib.ID, lib.Name)
				if libNames != "" {
					libNames += "、"
				}
				libNames += lib.Name
			}
			if refreshed > 0 {
				// 配置了 Emby webhook 入库通知时，入库卡片（带海报）会随后到达，
				// 这里只记日志避免双重通知；未配置 webhook 时才发这条提示
				if h.embyWebhookConfigured() {
					log.Printf("Emby 已提交刷新（%s）；webhook 入库通知已配置，跳过本条消息", libNames)
				} else {
					go NotifyMessage("🎬 媒体入库", "已刷新媒体库："+libNames)
				}
				return
			}
			log.Printf("Emby 按库刷新：变更路径未命中任何媒体库（%s），回退路径通知", embyPath)
		}
	}

	// 回退：按路径通知
	// POST /Library/Media/Updated { "Updates": [{"Path":"...","UpdateType":"Created"}] }
	body, _ := json.Marshal(map[string]interface{}{
		"Updates": []map[string]string{
			{"Path": embyPath, "UpdateType": "Created"},
		},
	})
	req, _ := http.NewRequest("POST", embyServer+"/Library/Media/Updated"+q, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Emby 刷新通知失败: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Emby 刷新通知已发送: %s", embyPath)
}

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
	if c.Query("token") != wantToken {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token 无效"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求失败"})
		return
	}
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
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

	// 事件归类（stop 归入暂停/停止，需先于 play 判断，避免 "Playback start" 误命中；resume 归入播放）
	category, title := "", ""
	switch {
	case strings.Contains(event, "test"):
		// Emby 侧点「测试通知」发来的连通测试事件：转发一条测试消息，方便确认全链路
		log.Printf("[Emby Webhook] 收到 Emby 测试事件，转发连通测试通知")
		go NotifyMessage("✅ Emby Webhook 连通测试", "Emby → StrmHub → 企微/TG 链路正常")
		c.JSON(http.StatusOK, gin.H{"message": "ok（测试事件，已转发）"})
		return
	case strings.Contains(event, "add"):
		category, title = "added", "🎬 Emby 入库"
	case strings.Contains(event, "delete"), strings.Contains(event, "remove"):
		category, title = "deleted", "🗑️ Emby 删除"
	case strings.Contains(event, "pause"), strings.Contains(event, "stop"):
		category, title = "pause", "⏸️ Emby 暂停/停止"
	case strings.Contains(event, "play"), strings.Contains(event, "start"), strings.Contains(event, "resume"):
		category, title = "play", "▶️ Emby 播放"
	}
	if category == "" {
		log.Printf("[Emby Webhook] 未识别的事件类型 %q，已忽略", event)
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
		return
	}

	if category == "added" {
		// 入库事件走聚合富通知（封面/评分取自 Emby；15 秒防抖合并）
		go h.queueEmbyAddedNotif(payload)
		log.Printf("[Emby Webhook] Emby 入库事件: %s", itemName)
		c.JSON(http.StatusOK, gin.H{"message": "ok（已进入入库通知队列）"})
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
	line := "Emby 入库 · " + typeLabel
	if r, ok := item["CommunityRating"].(float64); ok && r > 0 {
		line += fmt.Sprintf(" · ⭐ %.1f", r)
	}

	entry := mediaNotifEntry{Title: title, Year: year, Line: line}
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

// embyWebhookConfigured 是否配置了 Emby webhook 入库通知（emby-notify 卡）
func (h *Handler) embyWebhookConfigured() bool {
	var cfg struct {
		Webhook string `json:"webhook"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("emby-notify")), &cfg) != nil {
		return false
	}
	return strings.TrimSpace(cfg.Webhook) != ""
}
