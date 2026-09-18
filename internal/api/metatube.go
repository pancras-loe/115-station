package api

// ==================== MetaTube AV 番号识别 ====================
//
// 对接自部署的 metatube-server（github.com/metatube-community/metatube）。
// 响应协议（以 metatube-sdk-go route/model 源码为准）：
//   统一包裹     {"data": X, "error": {code,message}}        （responseMessage）
//   搜索         GET /v1/movies/search?q=番号 → data: []MovieSearchResult
//   详情         GET /v1/movies/{provider}/{id} → data: MovieInfo
//   刮削源       GET /v1/providers（公开端点）→ data: {movie_providers, actor_providers}
//   海报代理     GET /v1/images/primary/{provider}/{id}（公开端点，免防盗链）
// 认证：movies/actors 组走 token（服务端 MT_TOKEN）；请求同时带
// ?token= 参数与 Authorization: Bearer 头，两种校验方式都兼容。
//
// 字段名与直觉不同的：番号 = number（不是 title_number）、简介 = summary、
// 厂牌 = maker、演员/类型 = 纯字符串数组。
//
// 定位 = AV 版的 TMDB 识别：整理流程（processAVDirectory）番号提取后自动识别，
// 结果缓存 AVMeta 表，失败/未配置自动退回纯番号行为。识别结果只用于
// 重命名模板变量 {av_title}/{av_year}/{actor}/{actors}（缓存直读，不触发网络）
// 与总览面板展示，不向媒体目录写任何元数据文件。

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	nethttp "net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

// MetatubeConfig MetaTube 服务器连接配置（settings 键 "metatube"）
type MetatubeConfig struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
}

func loadMetatubeCfg() MetatubeConfig {
	out := MetatubeConfig{}
	if v := modelSettingValue("metatube"); v != "" {
		_ = json.Unmarshal([]byte(v), &out)
	}
	out.URL = strings.TrimRight(strings.TrimSpace(out.URL), "/")
	return out
}

func metatubeEnabled() bool {
	cfg := loadMetatubeCfg()
	return cfg.Enabled && cfg.URL != ""
}

// metatubeMovie MovieInfo/MovieSearchResult 的实际 JSON 结构（字段名按
// metatube-sdk-go model 包逐字对齐）
type metatubeMovie struct {
	ID       string `json:"id"`
	Number   string `json:"number"` // 番号
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Provider string `json:"provider"`
	Homepage string `json:"homepage"`

	Director string   `json:"director"`
	Actors   []string `json:"actors"`

	ThumbURL      string   `json:"thumb_url"`
	BigThumbURL   string   `json:"big_thumb_url"`
	CoverURL      string   `json:"cover_url"`
	BigCoverURL   string   `json:"big_cover_url"`
	PreviewImages []string `json:"preview_images"`

	Maker  string   `json:"maker"` // 厂牌
	Label  string   `json:"label"`
	Series string   `json:"series"`
	Genres []string `json:"genres"`
	Score  float64  `json:"score"`

	Runtime     int    `json:"runtime"`
	ReleaseDate string `json:"release_date"`
}

var metatubeClient = &nethttp.Client{Timeout: 15 * time.Second}

// metatubeGetRaw 调用 metatube-server（自动带上 token）返回原始响应体。
// 认证双保险：URL ?token= 参数 + Authorization: Bearer 头都带上
func metatubeGetRaw(cfg MetatubeConfig, apiPath string) ([]byte, error) {
	u := cfg.URL + apiPath
	if cfg.Token != "" {
		sep := "?"
		if strings.Contains(apiPath, "?") {
			sep = "&"
		}
		u += sep + "token=" + url.QueryEscape(cfg.Token)
	}
	req, err := nethttp.NewRequest(nethttp.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := metatubeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != nethttp.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 120))
	}
	return body, nil
}

// metatubeGet 调用 metatube-server 并解析 JSON 到 out
func metatubeGet(cfg MetatubeConfig, apiPath string, out any) error {
	body, err := metatubeGetRaw(cfg, apiPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// metatubeUnwrapData 服务器统一包裹 {"data": X, "error": …}（responseMessage），
// 剥出 data；裸 JSON（老版本/其他形态）原样返回
func metatubeUnwrapData(raw []byte) []byte {
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && len(wrapper.Data) > 0 {
		return wrapper.Data
	}
	return raw
}

// metatubeParseMovies 搜索结果解析：data 包裹对象为主，裸数组兼容
func metatubeParseMovies(raw []byte) ([]metatubeMovie, error) {
	raw = metatubeUnwrapData(raw)
	var arr []metatubeMovie
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var legacy struct {
		Result []metatubeMovie `json:"result"`
	}
	if err := json.Unmarshal(raw, &legacy); err == nil {
		return legacy.Result, nil
	}
	return nil, fmt.Errorf("响应格式无法解析: %s", truncateStr(string(raw), 80))
}

// metatubeParseMovie 详情解析：data 包裹对象为主，result 兼容
func metatubeParseMovie(raw []byte) (*metatubeMovie, error) {
	raw = metatubeUnwrapData(raw)
	var m metatubeMovie
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Title == "" && m.CoverURL == "" && m.ID == "" {
		var legacy struct {
			Result *metatubeMovie `json:"result"`
		}
		if json.Unmarshal(raw, &legacy) == nil && legacy.Result != nil {
			return legacy.Result, nil
		}
	}
	return &m, nil
}

// metatubeFetchCoverBytes 从 metatube-server 公开图片代理取封面字节
// （/v1/images/primary/{provider}/{id}：服务端统一处理源站防盗链并缓存，
// 比直连源站 CDN 可靠；该端点无需 token）
func metatubeFetchCoverBytes(m *model.AVMeta) ([]byte, error) {
	cfg := loadMetatubeCfg()
	if cfg.URL == "" || m.Provider == "" || m.ProviderID == "" {
		return nil, fmt.Errorf("metatube 图片代理不可用（缺地址或 provider/id）")
	}
	u := fmt.Sprintf("%s/v1/images/primary/%s/%s", cfg.URL,
		url.PathEscape(m.Provider), url.PathEscape(m.ProviderID))
	return fetchHTTPBytes(u, 15*time.Second)
}

// metatubeScrapeMu 批量整理时防止并发重复刮削同一批番号（双引擎竞态）
var metatubeScrapeMu sync.Mutex

// avNotFoundTTL not_found 缓存时长：7 天后过期重试（新片源收录有滞后）
const avNotFoundTTL = 7 * 24 * time.Hour

// metatubeFetchCached 番号 → 元数据（带缓存）。只应在整理流程调用（允许触发网络）。
// 未启用/失败返回 nil，调用方退回纯番号行为
func metatubeFetchCached(num string) *model.AVMeta {
	if !metatubeEnabled() || num == "" {
		return nil
	}
	key := normalizeAVNum(num)
	if key == "" {
		return nil
	}
	// 缓存命中（ok 永久有效；not_found 过 TTL 后重刮）
	var cached model.AVMeta
	if model.DB.Where("num = ?", key).First(&cached).Error == nil {
		if cached.Status == "ok" {
			// 字段全空的 ok 行是历史 bug（响应格式解析失败时期）写入的坏缓存，
			// 视为无效，继续走重刮流程自愈
			if strings.TrimSpace(cached.Title) != "" || cached.CoverURL != "" {
				return &cached
			}
		}
		if cached.Status == "not_found" && time.Since(cached.UpdatedAt) < avNotFoundTTL {
			return nil
		}
	}
	// 单飞：拿不到锁说明另一个整理引擎正在刮，直接退回（不阻塞整理主流程）
	if !metatubeScrapeMu.TryLock() {
		return nil
	}
	defer metatubeScrapeMu.Unlock()

	cfg := loadMetatubeCfg()
	rawBody, err := metatubeGetRaw(cfg, "/v1/movies/search?q="+url.QueryEscape(num))
	if err != nil {
		log.Printf("[MetaTube] ✗ 搜索 %s 失败: %v", num, err)
		return nil
	}
	hits, err := metatubeParseMovies(rawBody)
	if err != nil {
		log.Printf("[MetaTube] ✗ 搜索 %s 响应解析失败: %v", num, err)
		return nil
	}
	if len(hits) == 0 {
		// 补零番号（文件名 juvr00303 → 识别为 JUVR-00303）官方番号未必带零，
		// 去前导零后重试一次（MIDV-001 这类本就带零的先按原样搜，不受影响）
		if alt := avTrimNumZeros(num); alt != "" && alt != num {
			if rb, e2 := metatubeGetRaw(cfg, "/v1/movies/search?q="+url.QueryEscape(alt)); e2 == nil {
				if h2, pe := metatubeParseMovies(rb); pe == nil {
					hits = h2
				}
			}
		}
	}
	if len(hits) == 0 {
		saveAVMeta(&model.AVMeta{Num: key, Status: "not_found", UpdatedAt: time.Now()})
		return nil
	}
	// 取第一个命中（provider 优先级由服务端排序）
	return metatubeBuildMeta(cfg, key, hits[0])
}

// metatubeBuildMeta 从搜索摘要拉详情、构建 AVMeta 并落缓存。
// num 必须是归一化番号（缓存主键）
func metatubeBuildMeta(cfg MetatubeConfig, key string, sum metatubeMovie) *model.AVMeta {
	sum2 := sum
	detailPath := fmt.Sprintf("/v1/movies/%s/%s", url.PathEscape(sum.Provider), url.PathEscape(sum.ID))
	if detailBody, err := metatubeGetRaw(cfg, detailPath); err != nil {
		log.Printf("[MetaTube] ✗ 详情 %s/%s 失败: %v", sum.Provider, sum.ID, err)
		// 详情失败就用搜索摘要（标题/封面通常已含）
	} else if d, perr := metatubeParseMovie(detailBody); perr == nil {
		sum2 = *d
	}
	detail := sum2
	if detail.Provider == "" {
		detail.Provider = sum.Provider
	}
	if detail.ID == "" {
		detail.ID = sum.ID
	}
	if detail.CoverURL == "" {
		detail.CoverURL = sum.CoverURL // 详情缺封面时回退搜索摘要
	}
	year := ""
	if d := detail.ReleaseDate; len(d) >= 4 {
		year = d[:4]
	}
	meta := &model.AVMeta{
		Num: key, Status: "ok",
		Provider: detail.Provider, ProviderID: detail.ID,
		Title: strings.TrimSpace(detail.Title),
		Year:  year, ReleaseDate: detail.ReleaseDate,
		Runtime: detail.Runtime, Director: detail.Director, Publisher: detail.Maker,
		Plot: detail.Summary, Score: detail.Score,
	}
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(detail.Number)
	}
	if len(detail.Actors) > 0 {
		b, _ := json.Marshal(detail.Actors)
		meta.ActorsJSON = string(b)
	}
	if len(detail.Genres) > 0 {
		b, _ := json.Marshal(detail.Genres)
		meta.GenresJSON = string(b)
	}
	meta.CoverURL = detail.CoverURL // 源站公网 URL：入库通知 picurl 直接可用；
	// 面板显示走 /av: 代理（服务端抓取+缓存），不受内网/防盗链影响
	saveAVMeta(meta)
	return meta
}

// metatubeSearchTitle 标题关键词搜索（无番号 AV 的兜底识别）：
// 命中条目必须带合法番号，构建元数据落缓存后返回。
// 返回值第二个为展示形态番号（源站原样，如 HMN-898——缓存键是归一化形态，
// 重命名模板需要横杠形态，两者分开）。
// 匹配质量依赖源站搜索排序——日志会打印匹配来源，错误匹配可人工发现
func metatubeSearchTitle(title string) (*model.AVMeta, string) {
	if !metatubeEnabled() || title == "" {
		return nil, ""
	}
	if !metatubeScrapeMu.TryLock() {
		return nil, ""
	}
	defer metatubeScrapeMu.Unlock()
	cfg := loadMetatubeCfg()
	rawBody, err := metatubeGetRaw(cfg, "/v1/movies/search?q="+url.QueryEscape(title))
	if err != nil {
		log.Printf("[MetaTube] ✗ 标题搜索 %q 失败: %v", truncateStr(title, 40), err)
		return nil, ""
	}
	hits, err := metatubeParseMovies(rawBody)
	if err != nil {
		return nil, ""
	}
	for _, h := range hits {
		num := strings.TrimSpace(h.Number)
		if num == "" || strings.TrimSpace(h.Title) == "" || strings.TrimSpace(h.Provider) == "" {
			continue
		}
		meta := metatubeBuildMeta(cfg, normalizeAVNum(num), h)
		if meta == nil || meta.Status != "ok" {
			continue
		}
		log.Printf("[MetaTube] ✓ 标题匹配: %q → %s《%s》（来源 %s）",
			truncateStr(title, 40), num, truncateStr(meta.Title, 40), meta.Provider)
		return meta, num
	}
	return nil, ""
}

// avTrimNumZeros 番号数字段去前导零（JUVR-00303 → JUVR-303）
func avTrimNumZeros(num string) string {
	return avLeadingZerosRe.ReplaceAllString(num, "-$1")
}

var avLeadingZerosRe = regexp.MustCompile(`-0+(\d)`)

func saveAVMeta(meta *model.AVMeta) {
	if model.DB == nil {
		return
	}
	var existing model.AVMeta
	if model.DB.Where("num = ?", meta.Num).First(&existing).Error == nil {
		meta.ID = existing.ID
		model.DB.Save(meta)
		return
	}
	model.DB.Create(meta)
}

// appDataDir 海报缓存的根目录（与门户共用 DataDir）
func appDataDir() string {
	if portalCfg != nil && portalCfg.DataDir != "" {
		return portalCfg.DataDir
	}
	return "/data"
}

// posterCacheFile 海报磁盘缓存路径——serveTMDBPoster（总览/封面代理）
// 与 readPosterCache（媒体库海报插件）必须用同一 key 推导
func posterCacheFile(dataDir, key string) string {
	cacheDir := filepath.Join(dataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0o755)
	h := sha1.Sum([]byte(key))
	return filepath.Join(cacheDir, hex.EncodeToString(h[:8])+path.Ext(key))
}

// avMetaActors 缓存行 → 演员名列表
func avMetaActors(m *model.AVMeta) []string {
	if m == nil || m.ActorsJSON == "" {
		return nil
	}
	var list []string
	_ = json.Unmarshal([]byte(m.ActorsJSON), &list)
	return list
}

func avMetaGenres(m *model.AVMeta) []string {
	if m == nil || m.GenresJSON == "" {
		return nil
	}
	var list []string
	_ = json.Unmarshal([]byte(m.GenresJSON), &list)
	return list
}

// readPosterCache 读海报缓存（未命中返回 nil）
func readPosterCache(coverURL string) []byte {
	data, err := os.ReadFile(posterCacheFile(appDataDir(), "av:"+coverURL))
	if err != nil {
		return nil
	}
	return data
}

// recordAVMedia AV 条目落 MediaLibrary（总览面板/最近整理展示用）。
// AV 没有 tmdb_id，去重键 = media_type + OriginalTitle（恒存归一化番号，
// Title 则存真实标题/番号）；
// PosterPath 带 av: 前缀，serveTMDBPoster 据此走封面代理
func recordAVMedia(m *model.AVMeta, num, category, targetPath string) {
	if model.DB == nil {
		return
	}
	title := num
	year := ""
	poster := ""
	overview := ""
	if m != nil && m.Status == "ok" {
		if m.Title != "" {
			title = m.Title
		}
		// 前导 / 与 TMDB 路径一致：前端按 '/api/poster' + path 直接拼接
		year, poster, overview = m.Year, "/av:"+m.CoverURL, m.Plot
	}
	var existing model.MediaLibrary
	if model.DB.Where("media_type = ? AND original_title = ?", "av", num).First(&existing).Error == nil {
		existing.Category = category
		existing.TargetPath = targetPath
		existing.Title = title
		existing.Year = year
		existing.PosterPath = poster
		existing.Overview = overview
		model.DB.Save(&existing)
		return
	}
	model.DB.Create(&model.MediaLibrary{
		Title: title, OriginalTitle: num, Year: year, MediaType: "av",
		Category: category, TargetPath: targetPath,
		PosterPath: poster, Overview: overview,
	})
}

// ==================== HTTP 接口 ====================

// MetatubeCheck 测试连接：POST {url?, token?}（表单值优先，缺省回退已保存配置）。
// 两段式：/v1/providers（公开端点）验可达 → /v1/movies/search（token 保护端点）
// 验 token 真实有效。成功返回刮削源数量
func (h *Handler) MetatubeCheck(c *gin.Context) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	cfg := loadMetatubeCfg()
	if req.URL != "" {
		cfg.URL = strings.TrimRight(strings.TrimSpace(req.URL), "/")
		cfg.Token = req.Token
	}
	if cfg.URL == "" {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "请先填写服务器地址"})
		return
	}
	start := time.Now()
	// 第一段：providers 响应为 responseMessage 包裹的映射（名称→主页）
	var providers struct {
		Data struct {
			MovieProviders map[string]string `json:"movie_providers"`
			ActorProviders map[string]string `json:"actor_providers"`
		} `json:"data"`
	}
	if err := metatubeGet(cfg, "/v1/providers", &providers); err != nil {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "服务器不可达: " + truncateStr(err.Error(), 150)})
		return
	}
	// 第二段：搜索端点受 token 保护，providers 不校验 token，
	// 只测它会出现"随便填 token 也成功"的假象
	probeBody, err := metatubeGetRaw(cfg, "/v1/movies/search?q=test")
	if err != nil {
		if strings.Contains(err.Error(), "401") {
			c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "Token 错误：服务器已开启认证，请填写部署时设置的 MT_TOKEN"})
		} else {
			c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "搜索接口异常: " + truncateStr(err.Error(), 150)})
		}
		return
	}
	if _, err := metatubeParseMovies(probeBody); err != nil {
		c.JSON(nethttp.StatusOK, gin.H{"success": false, "error": "搜索接口响应格式异常: " + truncateStr(err.Error(), 150)})
		return
	}
	n := len(providers.Data.MovieProviders) + len(providers.Data.ActorProviders)
	msg := fmt.Sprintf("连接成功（%dms）", time.Since(start).Milliseconds())
	if n > 0 {
		msg += fmt.Sprintf("，已启用 %d 个刮削源", n)
	}
	c.JSON(nethttp.StatusOK, gin.H{"success": true, "message": msg})
}
