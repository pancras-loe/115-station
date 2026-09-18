package api

// ==================== 仪表盘 + 代理测试 ====================

import (
	"encoding/json"
	"fmt"
	"io"
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

// TestProxyLatency 测试代理延迟：通过代理访问 Google，返回毫秒
// POST /proxy/test
func (h *Handler) TestProxyLatency(c *gin.Context) {
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写代理地址"})
		return
	}

	proxyURL, err := url.Parse(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理地址格式错误"})
		return
	}

	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	start := time.Now()
	resp, err := client.Get("https://www.google.com/generate_204")
	elapsed := time.Since(start)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "latency_ms": -1})
		return
	}
	resp.Body.Close()
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"latency_ms": elapsed.Milliseconds(),
		"target":     "google_204",
	})
}

// NetworkCheck 网络连接测试：并发探测关键外部依赖的可达性与延迟
// GET /network/check
func (h *Handler) NetworkCheck(c *gin.Context) {
	type result struct {
		Name      string `json:"name"`
		OK        bool   `json:"ok"`
		LatencyMs int64  `json:"latency_ms"`
		Error     string `json:"error,omitempty"`
	}
	probe := func(name, url string, viaProxy bool) result {
		client := &http.Client{Timeout: 6 * time.Second}
		if viaProxy {
			if pu := getProxyURL(); pu != "" {
				if p, err := parseProxyURL(pu); err == nil {
					client.Transport = &http.Transport{Proxy: p}
				}
			} else {
				return result{Name: name + "（经代理）", OK: false, Error: "未配置代理"}
			}
		}
		start := time.Now()
		resp, err := client.Get(url)
		lat := time.Since(start).Milliseconds()
		if err != nil {
			return result{Name: name, OK: false, LatencyMs: lat, Error: err.Error()}
		}
		resp.Body.Close()
		return result{Name: name, OK: true, LatencyMs: lat}
	}

	type job struct {
		name string
		url  string
		px   bool
	}
	jobs := []job{
		// 直连组（国内可达）
		{"百度 www.baidu.com", "https://www.baidu.com", false},
		{"115 网盘 webapi.115.com", "https://webapi.115.com/", false},
		{"企业微信 api.weixin.qq.com", "https://qyapi.weixin.qq.com/cgi-bin/gettoken", false},
		{"TMDB api.tmdb.org（直连）", "https://api.tmdb.org/3/configuration", false},
		// 代理组（国内通常被墙，经代理测试）
		{"Google www.google.com", "https://www.google.com/generate_204", true},
		{"TMDB API（经代理）", "https://api.themoviedb.org/3/configuration", true},
		{"Telegram api.telegram.org", "https://api.telegram.org/", true},
		{"GitHub API（经代理）", "https://api.github.com/", true},
		{"Docker Hub hub.docker.com", "https://hub.docker.com/", true},
	}
	// 未配置代理时，代理组退回直连探测（结果能反映真实网络状况）
	hasProxy := getProxyURL() != ""

	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			// 未配置代理时，代理组任务改走直连（名称去掉"经代理"标注）
			if j.px && !hasProxy {
				j.name = strings.ReplaceAll(j.name, "（经代理）", "（直连）")
				j.px = false
			}
			results[i] = probe(j.name, j.url, j.px)
		}(i, j)
	}
	wg.Wait()

	allOK := true
	for _, r := range results {
		if !r.OK {
			allOK = false
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": allOK, "results": results, "via_proxy": hasProxy})
}

// cpuSample 上一次 /proc/stat 采样（CPU% 用两次调用差值计算）
var (
	cpuSampleMu   sync.Mutex
	cpuSampleLast [10]int64
	cpuSampleAt   time.Time
)

// cpuPercent 读取系统 CPU 使用率（/proc/stat 差值；无历史采样时短睡 150ms
// 采一次；非 Linux 返回 -1 由前端隐藏）
func cpuPercent() float64 {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return -1
	}
	cur := parseProcStat(stat)
	cpuSampleMu.Lock()
	last, lastAt := cpuSampleLast, cpuSampleAt
	cpuSampleLast, cpuSampleAt = cur, time.Now()
	cpuSampleMu.Unlock()
	if lastAt.IsZero() || time.Since(lastAt) < 300*time.Millisecond {
		time.Sleep(150 * time.Millisecond)
		stat2, err := os.ReadFile("/proc/stat")
		if err != nil {
			return -1
		}
		cur = parseProcStat(stat2)
		last = parseProcStat(stat)
	}
	idle := (cur[3] + cur[4]) - (last[3] + last[4])
	total := int64(0)
	for j := 0; j < 10; j++ {
		total += cur[j] - last[j]
	}
	if total <= 0 {
		return -1
	}
	pct := float64(total-idle) * 100 / float64(total)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

func parseProcStat(stat []byte) [10]int64 {
	var out [10]int64
	for i, line := range strings.Split(string(stat), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			if i == 0 {
				continue
			}
			break
		}
		fields := strings.Fields(strings.TrimSpace(line))[1:]
		for j := 0; j < 10 && j < len(fields); j++ {
			out[j], _ = strconv.ParseInt(fields[j], 10, 64)
		}
		break
	}
	return out
}

// memInfo 读取 /proc/meminfo，返回 totalMB/usedMB/percent；非 Linux 返回 ok=false
func memInfo() (totalMB, usedMB uint64, pct float64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, false
	}
	get := func(prefix string) uint64 {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, prefix) {
				f := strings.Fields(line)
				if len(f) >= 2 {
					v, _ := strconv.ParseUint(f[1], 10, 64)
					return v / 1024
				}
			}
		}
		return 0
	}
	total := get("MemTotal:")
	avail := get("MemAvailable:")
	if total == 0 {
		return 0, 0, 0, false
	}
	used := total - avail
	return total, used, float64(used) * 100 / float64(total), true
}

// embyDashCache Emby 仪表盘数据短缓存（60 秒；每次刷新要打 Emby 十来个接口）
var (
	embyDashMu   sync.Mutex
	embyDashData gin.H
	embyDashAt   time.Time
)

// fetchEmbyDashboard 从 Emby 拉媒体统计/媒体库/最新入库（含封面路径）。
// 未配置 Emby 或请求失败返回 nil（前端回退本地台账数据）
func fetchEmbyDashboard(h *Handler) gin.H {
	embyDashMu.Lock()
	cached, cacheAt := embyDashData, embyDashAt
	embyDashMu.Unlock()
	if cached != nil && time.Since(cacheAt) < 60*time.Second {
		return cached
	}
	base, apiKey, ok := h.embyServerInfo()
	if !ok {
		return nil
	}
	client := &http.Client{Timeout: 8 * time.Second}
	getJSON := func(path string, query url.Values) (map[string]interface{}, error) {
		if query == nil {
			query = url.Values{}
		}
		query.Set("api_key", apiKey)
		resp, err := client.Get(strings.TrimRight(base, "/") + path + "?" + query.Encode())
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var out map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&out)
		return out, err
	}

	// 媒体数量
	var movies, series, episodes float64
	if counts, err := getJSON("/Items/Counts", nil); err == nil {
		movies, _ = counts["MovieCount"].(float64)
		series, _ = counts["SeriesCount"].(float64)
		episodes, _ = counts["EpisodeCount"].(float64)
	}

	// 最新入库（电影+剧集，按入库时间倒序 12 条）
	recent := []gin.H{}
	if res, err := getJSON("/Items", url.Values{
		"Recursive":        {"true"},
		"IncludeItemTypes": {"Movie,Series"},
		"SortBy":           {"DateCreated"},
		"SortOrder":        {"Descending"},
		"Limit":            {"12"},
		"Fields":           {"ProductionYear"},
	}); err == nil {
		if items, _ := res["Items"].([]interface{}); items != nil {
			for _, it := range items {
				m, _ := it.(map[string]interface{})
				id, _ := m["Id"].(string)
				name, _ := m["Name"].(string)
				year, _ := m["ProductionYear"].(float64)
				if id == "" || name == "" {
					continue
				}
				yearStr := ""
				if year > 0 {
					yearStr = fmt.Sprintf("%d", int(year))
				}
				recent = append(recent, gin.H{"id": id, "name": name, "year": yearStr})
			}
		}
	}

	// 媒体库（分类卡）：每个库的条目数 + 4 张封面拼贴。
	// 各库并发拉取（串行时 8 个库 × 每库一请求，冷缓存下仪表盘明显变慢）
	libraries := []gin.H{}
	if res, err := getJSON("/Library/MediaFolders", nil); err == nil {
		if items, _ := res["Items"].([]interface{}); items != nil {
			type libOut struct {
				idx     int
				name    string
				count   int
				collage []string
			}
			var wg sync.WaitGroup
			outCh := make(chan libOut, len(items))
			idx := 0
			for _, it := range items {
				m, _ := it.(map[string]interface{})
				id, _ := m["Id"].(string)
				name, _ := m["Name"].(string)
				if id == "" || name == "" || idx >= 8 {
					continue
				}
				wg.Add(1)
				go func(idx int, id, name string) {
					defer wg.Done()
					out := libOut{idx: idx, name: name}
					if lr, err := getJSON("/Items", url.Values{
						"ParentId": {id}, "Recursive": {"true"}, "Limit": {"4"},
						"SortBy": {"DateCreated"}, "SortOrder": {"Descending"},
					}); err == nil {
						if tc, ok := lr["TotalRecordCount"].(float64); ok {
							out.count = int(tc)
						}
						if citems, _ := lr["Items"].([]interface{}); citems != nil {
							for _, ci := range citems {
								if cm, _ := ci.(map[string]interface{}); cm != nil {
									if cid, _ := cm["Id"].(string); cid != "" {
										out.collage = append(out.collage, "Items/"+cid+"/Images/Primary")
									}
								}
							}
						}
					}
					outCh <- out
				}(idx, id, name)
				idx++
			}
			wg.Wait()
			close(outCh)
			ordered := make([]libOut, idx)
			for o := range outCh {
				ordered[o.idx] = o
			}
			for _, o := range ordered {
				libraries = append(libraries, gin.H{"name": o.name, "count": o.count, "collage": o.collage})
			}
		}
	}

	out := gin.H{
		"counts":    gin.H{"movies": int(movies), "series": int(series), "episodes": int(episodes)},
		"recent":    recent,
		"libraries": libraries,
	}
	embyDashMu.Lock()
	embyDashData, embyDashAt = out, time.Now()
	embyDashMu.Unlock()
	return out
}

// placeholderGIF 1x1 透明像素（图片代理失败时的占位，避免 <img> 裂图）
var placeholderGIF = []byte("GIF89aÿÿÿ!ù,D;")

// EmbyImageProxy Emby 图片代理：服务端注入 api_key（封面 URL 带密钥会泄露，
// 此前已从通知里移除）。仅放行 Items/…/Images 路径，浏览器缓存 1 天
// GET /embyimg?path=Items/{Id}/Images/Primary&maxWidth=300
func (h *Handler) EmbyImageProxy(c *gin.Context) {
	base, apiKey, ok := h.embyServerInfo()
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	path := strings.Trim(c.Query("path"), "/")
	// 仅放行图片端点（此前 Items/ 前缀下任意子路径可打：可借本代理枚举
	// Emby 全库元数据）。仪表盘只用 Items/{id}/Images/{type} 形态
	if !regexp.MustCompile(`^Items/\d+/Images/[A-Za-z]{2,20}(?:/\d+)?$`).MatchString(path) {
		c.Status(http.StatusBadRequest)
		return
	}
	q := url.Values{}
	if mw := c.Query("maxWidth"); mw != "" {
		q.Set("maxWidth", mw)
	}
	q.Set("api_key", apiKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/" + path + "?" + q.Encode())
	if err == nil {
		defer resp.Body.Close()
	}
	if err != nil || resp.StatusCode != 200 {
		// 1x1 透明占位，避免 <img> 裂图
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/gif", placeholderGIF)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	c.Data(http.StatusOK, ct, data)
}

// DashboardEnhanced 仪表盘（真实数据版）
// GET /dashboard
func (h *Handler) DashboardEnhanced(c *gin.Context) {
	// ---- 媒体统计（电影/剧集 + 本月新增）----
	var movieCount, tvCount, movieMonth, tvMonth int64
	monthStart := time.Now().Format("2006-01") + "-01"
	h.DB.Model(&model.MediaLibrary{}).Where("media_type = ?", "movie").Count(&movieCount)
	h.DB.Model(&model.MediaLibrary{}).Where("media_type = ?", "tv").Count(&tvCount)
	h.DB.Model(&model.MediaLibrary{}).Where("media_type = ? AND created_at >= ?", "movie", monthStart).Count(&movieMonth)
	h.DB.Model(&model.MediaLibrary{}).Where("media_type = ? AND created_at >= ?", "tv", monthStart).Count(&tvMonth)

	// ---- STRM 统计（失效口径：台账 video 行中本地文件已不存在的，抽样上限防大库卡顿）----
	var strmTotal, strmInvalid, syncedFiles int64
	var dashRecentVideos []model.SyncedFile
	dashLocalRoot := defaultLocalPath
	var dashFullCfg struct {
		LocalPath string `json:"local_path"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &dashFullCfg) == nil && dashFullCfg.LocalPath != "" {
		dashLocalRoot = dashFullCfg.LocalPath
	}
	h.DB.Model(&model.SyncedFile{}).Where("kind = ?", "video").Count(&strmTotal)
	h.DB.Model(&model.SyncedFile{}).Where("kind = ?", "video").Order("updated_at DESC").Limit(500).Find(&dashRecentVideos)
	for _, sf := range dashRecentVideos {
		if _, err := os.Stat(filepath.Join(dashLocalRoot, filepath.FromSlash(sf.RelPath))); err != nil {
			strmInvalid++
		}
	}
	h.DB.Model(&model.SyncedFile{}).Count(&syncedFiles)

	// ---- 最近整理（12 条，带海报）----
	var recentMedia []model.MediaLibrary
	h.DB.Order("created_at DESC").Limit(12).Find(&recentMedia)
	recent := make([]gin.H, 0, len(recentMedia))
	for _, m := range recentMedia {
		recent = append(recent, gin.H{
			"title": m.Title, "year": m.Year, "category": m.Category,
			"type": m.MediaType, "poster": m.PosterPath, "at": m.CreatedAt.Format("01-02 15:04"),
		})
	}

	// ---- 近 7 天入库曲线 ----
	type dayCount struct {
		Day   string `json:"day"`
		Count int64  `json:"count"`
	}
	weekly := make([]dayCount, 7)
	type row struct {
		Day   string
		Count int64
	}
	var rows []row
	weekAgo := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	h.DB.Model(&model.MediaLibrary{}).
		Select("DATE(created_at) as day, COUNT(*) as count").
		Where("created_at >= ?", weekAgo).
		Group("DATE(created_at)").Scan(&rows)
	countByDay := map[string]int64{}
	for _, r := range rows {
		countByDay[r.Day] = r.Count
	}
	weekTotal := int64(0)
	for i := 6; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i)
		key := d.Format("2006-01-02")
		n := countByDay[key]
		weekly[6-i] = dayCount{Day: d.Format("01-02"), Count: n}
		weekTotal += n
	}

	// ---- 我的媒体库（分类聚卡：计数 + 4 张海报拼贴）----
	type catRow struct {
		Category string
		Count    int64
	}
	var catRows []catRow
	h.DB.Model(&model.MediaLibrary{}).
		Select("category, COUNT(*) as count").
		Where("category != ''").
		Group("category").Order("count DESC").Limit(8).Scan(&catRows)
	categories := make([]gin.H, 0, len(catRows))
	for _, cr := range catRows {
		var posters []model.MediaLibrary
		h.DB.Select("poster_path").
			Where("category = ? AND poster_path != ''", cr.Category).
			Order("created_at DESC").Limit(4).Find(&posters)
		plist := make([]string, 0, 4)
		for _, pm := range posters {
			plist = append(plist, pm.PosterPath)
		}
		categories = append(categories, gin.H{"name": cr.Category, "count": cr.Count, "posters": plist})
	}

	// ---- 后台任务/当前任务卡片已下线，不再组装（任务状态走「任务日志」页）----
	_, _, _, _ = TaskStatus()

	// ---- 系统状态（内存/CPU）----
	sys := gin.H{}
	if totalMB, usedMB, pct, ok := memInfo(); ok {
		sys["mem_total_mb"] = totalMB
		sys["mem_used_mb"] = usedMB
		sys["mem_percent"] = pct
	}
	if cp := cpuPercent(); cp >= 0 {
		sys["cpu_percent"] = cp
	}

	// ---- 待处理事件 ----
	var pendingEvents int64
	h.DB.Model(&model.SyncEvent{}).Where("status = ?", "pending").Count(&pendingEvents)

	var organizedTotal int64
	h.DB.Model(&model.MediaLibrary{}).Count(&organizedTotal)

	embyData := fetchEmbyDashboard(h)

	c.JSON(http.StatusOK, gin.H{
		"emby":    embyData,
		"storage": h.pan115CapacityCached(),
		"media": gin.H{
			"movies": movieCount, "tvs": tvCount,
			"movies_month": movieMonth, "tvs_month": tvMonth,
			"total": movieCount + tvCount,
		},
		"strm":           gin.H{"total": strmTotal, "invalid": strmInvalid, "active": strmTotal - strmInvalid},
		"synced_files":   syncedFiles,
		"organized":      organizedTotal,
		"recent_media":   recent,
		"weekly":         weekly,
		"week_total":     weekTotal,
		"categories":     categories,
		"sys":            sys,
		"pending_events": pendingEvents,
	})
}

// saveProxyConfigToDB 把代理配置写入 DB（TMDB/GPT 请求共用）
func saveProxyConfigToDB(h *Handler, proxyURL string) {
	var s model.Setting
	if err := h.DB.Where("`key` = ?", "proxy").First(&s).Error; err == nil {
		h.DB.Model(&s).Update("value", fmt.Sprintf(`{"url":%q}`, proxyURL))
	} else {
		h.DB.Create(&model.Setting{Key: "proxy", Value: fmt.Sprintf(`{"url":%q}`, proxyURL)})
	}
}
