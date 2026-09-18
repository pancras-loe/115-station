package api

// ==================== PanSou 网盘聚合搜索 ====================
//
// 开源项目 PanSou（github.com/fish2018/pansou）聚合了 TG 频道与爬虫插件的
// 网盘分享链接，公开实例免认证：
//
//	GET {base}/api/search?kw=<关键词>&res=merged_by_type
//	→ {"code":0,"message":"success","data":{"total":N,"merged_by_type":
//	    {"115":[{url,password,note,datetime}],...}}}
//
// 常见类型：baidu/xunlei/uc（115 偶见）。StrmHub 只能自动
// 处理 115 分享（转存）与磁力/ed2k（离线下载），其余类型点击打开原链接
// 手动转存到对应网盘。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const pansouDefaultBase = "https://pansou.app"

// pansouCfg 配置（setting key "pansou"），支持自建实例换 base_url
type pansouCfg struct {
	BaseURL string `json:"base_url"`
}

var (
	pansouCfgMu sync.Mutex
	pansouCfgV  *pansouCfg
	pansouCfgAt time.Time
)

func loadPansouCfg() *pansouCfg {
	pansouCfgMu.Lock()
	defer pansouCfgMu.Unlock()
	if pansouCfgV != nil && time.Since(pansouCfgAt) < 10*time.Second {
		return pansouCfgV
	}
	cfg := &pansouCfg{BaseURL: pansouDefaultBase}
	if v := settingValueCompat("pansou"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = pansouDefaultBase
	}
	pansouCfgV = cfg
	pansouCfgAt = time.Now()
	return cfg
}

func savePansouCfg(cfg *pansouCfg) error {
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	if err := notifyConfigSource.SaveSetting("pansou", string(b)); err != nil {
		return err
	}
	pansouCfgMu.Lock()
	pansouCfgV = nil
	pansouCfgMu.Unlock()
	return nil
}

// PansouGetConfig GET /pansou/config
func (h *Handler) PansouGetConfig(c *gin.Context) {
	cfg := loadPansouCfg()
	c.JSON(http.StatusOK, gin.H{"base_url": cfg.BaseURL})
}

// PansouSaveConfig POST /pansou/config {base_url}
func (h *Handler) PansouSaveConfig(c *gin.Context) {
	var req struct {
		BaseURL string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadPansouCfg()
	base := strings.TrimSpace(req.BaseURL)
	if base == "" {
		base = pansouDefaultBase
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	cfg.BaseURL = strings.TrimRight(base, "/")
	if err := savePansouCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[盘搜] ✓ 聚合搜索站点已设为 %s", cfg.BaseURL)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功", "base_url": cfg.BaseURL})
}

// PansouItem 归一化后的搜索结果
type PansouItem struct {
	CloudType string `json:"cloud_type"` // baidu/xunlei/uc/115/…
	URL       string `json:"url"`
	Password  string `json:"password"`
	Note      string `json:"note"`
	Datetime  string `json:"datetime"`
	Action    string `json:"action"` // transfer=115 转存 / offline=离线下载 / open=打开链接
}

// pansouHTTP 包级复用客户端（连接池共享；单次超时用请求级 context 控制）
var pansouHTTP = &http.Client{}

// pansouTypeRank 类型展示顺序（1 起；未知类型 map 缺省 0 → 归一为 9 沉底）
var pansouTypeRank = map[string]int{
	"115": 1, "baidu": 2, "uc": 3, "xunlei": 4,
}

// PansouSearch GET /pansou/search?kw=
func (h *Handler) PansouSearch(c *gin.Context) {
	kw := strings.TrimSpace(c.Query("kw"))
	if kw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入搜索关键词"})
		return
	}
	items, err := pansouSearchItems(kw)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	cfg := loadPansouCfg()
	log.Printf("[盘搜] ✓ 「%s」：%d 条结果", kw, len(items))
	c.JSON(http.StatusOK, gin.H{"data": items, "base": cfg.BaseURL})
}

// pansouTypeLabel 类型中文名（机器人/前端共用）
func pansouTypeLabel(t string) string {
	switch t {
	case "115":
		return "115 网盘"
	case "baidu":
		return "百度网盘"
	case "uc":
		return "UC 网盘"
	case "xunlei":
		return "迅雷云盘"
	}
	return t
}

// pansouSearchItems 聚合搜索核心（HTTP 与企微机器人共用）。
// 公开实例可能瞬时过载/限流（503）：失败自动重试一次（换 15s 短超时）
func pansouSearchItems(kw string) ([]PansouItem, error) {
	cfg := loadPansouCfg()
	api := cfg.BaseURL + "/api/search?kw=" + url.QueryEscape(kw) + "&res=merged_by_type"

	var body []byte
	var lastErr string
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt == 2 {
			time.Sleep(1500 * time.Millisecond)
		}
		req, err := http.NewRequest(http.MethodGet, api, nil)
		if err != nil {
			lastErr = err.Error()
			break
		}
		req.Header.Set("User-Agent", chromeUA)
		req.Header.Set("Accept", "application/json")
		// 30s → 重试 15s：请求级超时（客户端为包级复用，不能设整体 Timeout）
		ctx, cancel := context.WithTimeout(req.Context(), time.Duration(30-15*(attempt-1))*time.Second)
		resp, err := pansouHTTP.Do(req.WithContext(ctx))
		cancel()
		if err != nil {
			lastErr = "连接盘搜失败: " + sanitizeWecomErr(err)
			continue
		}
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			lastErr = ""
			break
		}
		lastErr = fmt.Sprintf("盘搜返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 100))
	}
	if lastErr != "" {
		if strings.Contains(lastErr, "503") || strings.Contains(lastErr, "502") {
			lastErr += "——公开实例可能过载/限流，请稍后重试；或到 StrmHub 网页端「影视转存 → 盘搜」配置自建实例（docker run ghcr.io/fish2018/pansou）"
		}
		return nil, fmt.Errorf("%s", lastErr)
	}
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Total        int                          `json:"total"`
			MergedByType map[string][]json.RawMessage `json:"merged_by_type"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || out.Code != 0 {
		msg := out.Message
		if msg == "" {
			msg = truncateStr(string(body), 120)
		}
		return nil, fmt.Errorf("盘搜响应异常: %s", msg)
	}

	items := make([]PansouItem, 0, out.Data.Total)
	seen := map[string]bool{}
	for ctype, arr := range out.Data.MergedByType {
		if removedCloudResource(ctype, "") {
			continue
		}
		for _, raw := range arr {
			var it struct {
				URL      string `json:"url"`
				Password string `json:"password"`
				Note     string `json:"note"`
				Datetime string `json:"datetime"`
			}
			if json.Unmarshal(raw, &it) != nil || it.URL == "" || seen[it.URL] || removedCloudResource("", it.URL) {
				continue
			}
			seen[it.URL] = true
			note := strings.TrimSpace(it.Note)
			items = append(items, PansouItem{
				CloudType: ctype,
				URL:       it.URL,
				Password:  strings.TrimSpace(it.Password),
				Note:      note,
				Datetime:  it.Datetime,
				Action:    pansouActionFor(it.URL),
			})
		}
	}
	// 类型顺序 → 时间新在前
	sort.Slice(items, func(i, j int) bool {
		ri, rj := pansouTypeRank[items[i].CloudType], pansouTypeRank[items[j].CloudType]
		if ri == 0 {
			ri = 9
		}
		if rj == 0 {
			rj = 9
		}
		if ri != rj {
			return ri < rj
		}
		return items[i].Datetime > items[j].Datetime
	})
	return items, nil
}

// pansouActionFor 按链接协议决定行内动作
func pansouActionFor(rawURL string) string {
	lower := strings.ToLower(rawURL)
	switch {
	case strings.HasPrefix(lower, "magnet:"), strings.HasPrefix(lower, "ed2k:"):
		return "offline"
	}
	switch classifyLink(rawURL) {
	case "share":
		return "transfer"
	case "magnet", "ed2k":
		return "offline"
	}
	return "open"
}

// removedCloudResource excludes retired providers from aggregated search results,
// including links returned under an unknown or incorrect provider label.
func removedCloudResource(cloudType, rawURL string) bool {
	switch strings.ToLower(strings.TrimSpace(cloudType)) {
	case "123", "123pan", "pan123", "123云盘", "123网盘", "quark", "夸克", "ali", "aliyun", "aliyundrive", "alipan", "阿里":
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"quark.cn", "alipan.com", "aliyundrive.com", "123pan.com", "123pan.cn", "123684.com", "123684.cn", "123865.com", "123865.cn", "123912.com", "123912.cn"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
