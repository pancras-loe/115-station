package api

// TG 关键词订阅（基于 TG 频道搜索通道）：
// 订阅若干「关键词（可选指定频道）」，调度器按间隔轮询频道公开预览，
// 用消息 ID 做水位去重，命中新资源时推送通知；
// 开启「自动转存」时，命中的 115 分享/磁力直接入库（与 TG 搜索的转存同链路）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type TgSubItem struct {
	ID       int64  `json:"id"`
	Keyword  string `json:"keyword"`
	Channels string `json:"channels"` // 空 = 用 TG 搜索的全局频道
	Auto     bool   `json:"auto"`     // 命中后自动转存/离线
	LastID   int64  `json:"last_id"`  // 水位：已见过的最大消息 ID（0 = 新订阅，首轮只建水位）
	LastHit  string `json:"last_hit"` // 最近一次命中时间
	Enabled  *bool  `json:"enabled"`  // nil/true=启用（旧数据兼容）；false=暂停检查
}

// TgSubSource 订阅源（频道）
type TgSubSource struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"` // tg
	Name     string `json:"name"`
	URL      string `json:"url"`      // https://t.me/xxx 或 @xxx
	Priority int    `json:"priority"` // 越大越优先，默认 10
	Note     string `json:"note"`
	Enabled  *bool  `json:"enabled"` // nil/true=启用；false=暂停检查
}

type tgSubCfg struct {
	Sources     []TgSubSource `json:"sources"`
	Items       []TgSubItem   `json:"items"`
	IntervalMin int           `json:"interval_min"` // 检查间隔（分钟），最小 5，默认 30
}

// tgSubParseChannel 从链接/@名/裸名解析频道名（https://t.me/xxx → xxx）
func tgSubParseChannel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "t.me/"); i >= 0 {
		s = s[i+5:]
		s = strings.TrimPrefix(s, "s/")
	}
	s = strings.TrimPrefix(s, "@")
	if j := strings.IndexAny(s, "?#/"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

// tgSubEnabled 条目是否启用（nil 视为启用，兼容旧数据）
func tgSubEnabled(b *bool) bool {
	return b == nil || *b
}

// tgSubSourceChannels 订阅源频道列表（按优先级从高到低）
func tgSubSourceChannels(cfg tgSubCfg) []string {
	srcs := append([]TgSubSource(nil), cfg.Sources...)
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Priority > srcs[j].Priority })
	var out []string
	for _, s := range srcs {
		if !tgSubEnabled(s.Enabled) {
			continue // 已停用的订阅源不参与检查
		}
		if s.Type != "" && s.Type != "tg" {
			continue
		}
		if ch := tgSubParseChannel(s.URL); ch != "" {
			out = append(out, ch)
		}
	}
	return out
}

func (h *Handler) loadTgSubCfg() tgSubCfg {
	c := tgSubCfg{IntervalMin: 30}
	if v := h.Config.GetSetting("tgsub"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (h *Handler) saveTgSubCfg(c tgSubCfg) {
	b, _ := json.Marshal(c)
	h.Config.SaveSetting("tgsub", string(b))
}

// ==================== 检查 ====================

var tgSubRunning sync.Mutex

// tgSubCheck 单轮检查全部订阅。LastID=0 的新订阅首轮只建水位不通知（防历史消息轰炸）
func (h *Handler) tgSubCheck(silent bool) {
	if !tgSubRunning.TryLock() {
		return
	}
	defer tgSubRunning.Unlock()
	cfg := h.loadTgSubCfg()
	if len(cfg.Items) == 0 {
		return
	}
	changed := false
	seenMsg := map[string]bool{} // 单轮全局去重：同一消息命中多个关键词只处理一次
	for i := range cfg.Items {
		item := &cfg.Items[i]
		if !tgSubEnabled(item.Enabled) {
			continue // 已暂停的订阅不检查
		}
		channels := item.Channels
		if strings.TrimSpace(channels) == "" {
			// 默认检查全部订阅源（按优先级），没有订阅源时回退 TG 搜索全局频道
			channels = strings.Join(tgSubSourceChannels(cfg), "\n")
			if strings.TrimSpace(channels) == "" {
				channels = h.loadTgSearchCfg().Channels
			}
		}
		firstRun := item.LastID == 0
		var maxID int64
		var hits []tgItem
		for _, ch := range strings.Split(channels, "\n") {
			ch = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ch), "@"))
			if ch == "" {
				continue
			}
			items, err := tgSearchChannel(ch, item.Keyword)
			if err != nil {
				continue
			}
			for _, it := range items {
				if it.MsgID > maxID {
					maxID = it.MsgID
				}
				if it.MsgID > item.LastID {
					key := fmt.Sprintf("%s/%d", it.Channel, it.MsgID)
					if !seenMsg[key] {
						seenMsg[key] = true
						hits = append(hits, it)
					}
				}
			}
			time.Sleep(800 * time.Millisecond) // 频道间节流
		}
		if maxID > item.LastID {
			item.LastID = maxID
			changed = true
		}
		if silent || firstRun || len(hits) == 0 {
			continue
		}
		item.LastHit = time.Now().Format("01-02 15:04")
		changed = true
		lines := []string{fmt.Sprintf("🔔 订阅命中「%s」：%d 条新资源", item.Keyword, len(hits))}
		for k, it := range hits {
			if k >= 5 {
				lines = append(lines, fmt.Sprintf("…等共 %d 条", len(hits)))
				break
			}
			link := ""
			if len(it.Links) > 0 {
				link = it.Links[0].URL
			}
			lines = append(lines, fmt.Sprintf("• %s\n  %s", truncateStr(it.Title, 60), truncateStr(link, 90)))
			if item.Auto && link != "" {
				h.tgSubAutoSave(link, it.Pass)
			}
		}
		NotifyMessage("", strings.Join(lines, "\n"))
	}
	if changed {
		h.saveTgSubCfg(cfg)
	}
}

// tgSubAutoSave 自动入库：115 分享走转存，磁力/ed2k 提交离线下载
func (h *Handler) tgSubAutoSave(link, pass string) {
	if is115ShareLink(link) {
		// 转存目录与 TG 频道搜索插件保持一致：其配置目录优先，回退分享接收目录
		target := h.loadTgSearchCfg().Target
		if strings.TrimSpace(target) == "" {
			target = h.shareFolderCid()
		}
		msg, ok, fail, err := h.shareReceiveCore(link, pass, target, true)
		if err != nil {
			log.Printf("[TG订阅] ✗ 自动转存失败: %v", err)
			return
		}
		NotifyMessage("", fmt.Sprintf("🔔 订阅自动转存完成（%d 成功/%d 失败）\n%s", ok, fail, truncateStr(msg, 80)))
		return
	}
	if strings.HasPrefix(link, "magnet:") || strings.HasPrefix(link, "ed2k:") {
		if err := h.submitOfflineLink(link); err != nil {
			log.Printf("[TG订阅] ✗ 自动离线提交失败: %v", err)
			return
		}
		NotifyMessage("", "🔔 订阅自动离线下载已提交")
	}
}

// ==================== 调度与处理器 ====================

var (
	tgSubLastRunMu sync.Mutex
	tgSubLastRun   time.Time
)

func tgSubMarkRun() {
	tgSubLastRunMu.Lock()
	tgSubLastRun = time.Now()
	tgSubLastRunMu.Unlock()
}

func tgSubSinceLastRun() time.Duration {
	tgSubLastRunMu.Lock()
	defer tgSubLastRunMu.Unlock()
	return time.Since(tgSubLastRun)
}

func StartTgSubScheduler(h *Handler) {
	go func() {
		time.Sleep(40 * time.Second)
		h.tgSubCheck(true) // 首轮只建水位
		tgSubMarkRun()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			cfg := h.loadTgSubCfg()
			interval := cfg.IntervalMin
			if interval < 5 {
				interval = 5
			}
			if tgSubSinceLastRun() < time.Duration(interval)*time.Minute {
				continue
			}
			tgSubMarkRun()
			go h.tgSubCheck(false)
		}
	}()
	log.Println("[TG订阅] 调度器已启动")
}

// TgSubGetConfig GET /tgsub/config
func (h *Handler) TgSubGetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": h.loadTgSubCfg()})
}

// TgSubSaveConfig POST /tgsub/config（前端整表提交：新增/修改/删除）
func (h *Handler) TgSubSaveConfig(c *gin.Context) {
	var req tgSubCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.IntervalMin < 5 {
		req.IntervalMin = 5
	}
	// 新条目/新订阅源分配 ID（LastID 保持 0 → 下轮只建水位，不轰炸历史）
	var maxID, maxSrcID int64 = 1, 1
	for _, it := range req.Items {
		if it.ID > maxID {
			maxID = it.ID
		}
	}
	for i := range req.Items {
		if req.Items[i].ID == 0 {
			maxID++
			req.Items[i].ID = maxID
		}
	}
	for _, s := range req.Sources {
		if s.ID > maxSrcID {
			maxSrcID = s.ID
		}
	}
	for i := range req.Sources {
		if req.Sources[i].ID == 0 {
			maxSrcID++
			req.Sources[i].ID = maxSrcID
			if req.Sources[i].Priority == 0 {
				req.Sources[i].Priority = 10
			}
			if req.Sources[i].Type == "" {
				req.Sources[i].Type = "tg"
			}
		}
	}
	h.saveTgSubCfg(req)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// TgSubRun POST /tgsub/run：立即检查一轮（新消息会通知）
func (h *Handler) TgSubRun(c *gin.Context) {
	tgSubMarkRun()
	go h.tgSubCheck(false)
	c.JSON(http.StatusOK, gin.H{"message": "检查已开始，命中会推送通知"})
}
