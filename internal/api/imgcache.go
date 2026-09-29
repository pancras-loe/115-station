package api

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 界面图片的服务端缓存 ====================
//
// 界面上的图（整理记录 / 选片弹窗的 TMDB 海报、仪表盘的海报与 Emby 封面）原来每个代理各写各的：
// /tmdb/img 完全不落盘，浏览器缓存一过期、换个浏览器或手机打开，每张图都要重新走一次代理去 TMDB；
// 每次请求还 new 一个 http.Client，连接不复用，跨境链路上光握手就占掉大半时间。
// 这里收成一处：磁盘缓存 + 同一张图的并发请求合并 + 共享连接池 + 失败短时记忆。
//
// TMDB 的图片路径是内容寻址的（换图就换文件名），缓存可以永不过期；
// Emby 的封面会被用户替换，只缓存一天。

const (
	imgFetchTimeout = 15 * time.Second
	imgMaxBytes     = 8 << 20
	// 同时向外拉图的上限：海报墙一屏几十张，一窝蜂打出去在代理上容易被限流，反而更慢
	imgFetchConcurrency = 6
	// 失败记忆：配着不可达的图片域名时，一屏海报每张都要把三条链路超时走一遍；
	// 记住失败几分钟，别让每次翻页都再等一轮超时
	imgFailTTL = 5 * time.Minute
)

var errImgUpstream = errors.New("图片源不可用")

var (
	imgFetchSem = make(chan struct{}, imgFetchConcurrency)

	imgFlightMu sync.Mutex
	imgFlight   = map[string]*imgCall{}

	imgFailMu sync.Mutex
	imgFail   = map[string]time.Time{}

	imgClientMu sync.Mutex
	imgClients  = map[string]*http.Client{}
)

type imgCall struct {
	wg   sync.WaitGroup
	data []byte
	err  error
}

// imgHTTPClient 按代理地址复用 client（连接池跟着 Transport 走，每次 new 等于每次重新握手）
func imgHTTPClient(proxy string) *http.Client {
	imgClientMu.Lock()
	defer imgClientMu.Unlock()
	if c := imgClients[proxy]; c != nil {
		return c
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = imgFetchConcurrency
	tr.Proxy = nil
	if proxy != "" {
		if pr, err := parseProxyURL(proxy); err == nil {
			tr.Proxy = pr
		}
	}
	c := &http.Client{Timeout: imgFetchTimeout, Transport: tr}
	imgClients[proxy] = c
	return c
}

// imgGet 拉一张图；非 200 或内容过短（有的反代出错时回一个空 200）都算失败
func imgGet(client *http.Client, u string) ([]byte, error) {
	imgFetchSem <- struct{}{}
	defer func() { <-imgFetchSem }()
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, imgMaxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || len(body) < 100 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// imgCacheFile 缓存文件路径：分两级子目录，海报多了以后单目录几万个文件在 NAS 上列目录很慢
func imgCacheFile(dataDir, ns, key string) string {
	h := sha1.Sum([]byte(key))
	s := hex.EncodeToString(h[:])
	return filepath.Join(dataDir, "imgcache", ns, s[:2], s[2:20])
}

// cachedImage 先查磁盘，没有就用 fetch 拉一次并落盘。ttl=0 表示永不过期。
// 同一个 key 并发进来只拉一次，其余的等结果（海报墙首屏同一张图常被列表和详情同时要）
func cachedImage(dataDir, ns, key string, ttl time.Duration, fetch func() ([]byte, error)) ([]byte, error) {
	file := imgCacheFile(dataDir, ns, key)
	if st, err := os.Stat(file); err == nil && st.Size() > 0 && (ttl == 0 || time.Since(st.ModTime()) < ttl) {
		if data, err := os.ReadFile(file); err == nil {
			// 永不过期的缓存靠 mtime 记「最近还有人用」，清理时据此淘汰（见 imgCacheJanitor）；
			// 一天内碰过的不再改，免得每张图每次都多一次写
			if ttl == 0 && time.Since(st.ModTime()) > 24*time.Hour {
				now := time.Now()
				_ = os.Chtimes(file, now, now)
			}
			return data, nil
		}
	}
	fk := ns + "\x00" + key
	imgFailMu.Lock()
	if at, ok := imgFail[fk]; ok {
		if time.Since(at) < imgFailTTL {
			imgFailMu.Unlock()
			return nil, errImgUpstream
		}
		delete(imgFail, fk)
	}
	imgFailMu.Unlock()

	imgFlightMu.Lock()
	if call := imgFlight[fk]; call != nil {
		imgFlightMu.Unlock()
		call.wg.Wait()
		return call.data, call.err
	}
	call := &imgCall{}
	call.wg.Add(1)
	imgFlight[fk] = call
	imgFlightMu.Unlock()

	call.data, call.err = fetch()
	if call.err == nil {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
			// 先写临时文件再改名：并发读到半截文件会被当成坏图缓存进浏览器
			tmp := file + ".tmp"
			if os.WriteFile(tmp, call.data, 0o644) == nil {
				_ = os.Rename(tmp, file)
			}
		}
	} else {
		imgFailMu.Lock()
		if len(imgFail) > 2000 {
			imgFail = map[string]time.Time{}
		}
		imgFail[fk] = time.Now()
		imgFailMu.Unlock()
	}
	imgFlightMu.Lock()
	delete(imgFlight, fk)
	imgFlightMu.Unlock()
	call.wg.Done()
	return call.data, call.err
}

// imgCacheIdle 缓存文件多久没人用就清掉。刮削换了海报、Emby 换了封面、本地缩略图随 mtime 换了键，
// 旧文件都不会再被读到，不清的话缓存目录只涨不跌
const imgCacheIdle = 60 * 24 * time.Hour

// startImgCacheJanitor 启动后清一次、之后每天一次；顺带删掉旧版的 posters 缓存目录（已由 imgcache/tmdb 取代）
func startImgCacheJanitor(dataDir string) {
	go func() {
		time.Sleep(time.Minute)
		_ = os.RemoveAll(filepath.Join(dataDir, "posters"))
		for {
			sweepImgCache(filepath.Join(dataDir, "imgcache"), imgCacheIdle)
			time.Sleep(24 * time.Hour)
		}
	}()
}

func sweepImgCache(root string, idle time.Duration) (removed int) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > idle {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	if removed > 0 {
		log.Printf("[图片缓存] ✓ 清理 %d 个 %d 天未使用的缓存文件", removed, int(idle.Hours()/24))
	}
	return removed
}

// serveImage 回图片，带内容 ETag：浏览器缓存过期后再来问一次，图没变就只回 304、不重传
func serveImage(c *gin.Context, data []byte, cacheControl string) {
	etag := `"` + fmt.Sprintf("%x", sha1.Sum(data))[:16] + `"`
	c.Header("Cache-Control", cacheControl)
	c.Header("ETag", etag)
	if inm := c.GetHeader("If-None-Match"); inm != "" && strings.Contains(inm, etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, http.DetectContentType(data), data)
}

// imgMissing 拉不到图：回 404 让前端显示占位。此前回 1x1 透明 GIF，
// 前端的 onerror 不触发，卡片上是一块什么都没有的空白，比占位图更难看
func imgMissing(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNotFound)
}

// ---- TMDB ----

// tmdbImageSizes TMDB 支持的海报尺寸（其余尺寸 TMDB 会 404，放行只会白白占一条失败记忆）
var tmdbImageSizes = map[string]bool{
	"w92": true, "w154": true, "w185": true, "w342": true, "w500": true, "w780": true,
	"w300": true, "w1280": true, "original": true,
}

// fetchTMDBImage 多级回退拉 TMDB 图：配置的图片域名（走代理）→ 配置域名直连 → 官方域名直连。
// 代理优先用 TMDB 配置里单独设的，其次全局代理（与刮削同策略）。
// 此前只试配置域名一次，配了不可达的镜像域名时所有海报永远是裂图
func fetchTMDBImage(size, p string) ([]byte, error) {
	proxy := getProxyURL()
	var cfg model.TmdbConfig
	if model.DB != nil && model.DB.First(&cfg).Error == nil && cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxy = cfg.ProxyUrl
	}
	base := tmdbImageBase()
	type cand struct{ base, proxy string }
	cands := []cand{{base, proxy}}
	if proxy != "" {
		cands = append(cands, cand{base, ""})
	}
	if strings.TrimSuffix(strings.TrimSuffix(base, "/t/p"), "/") != "https://image.tmdb.org" {
		cands = append(cands, cand{"https://image.tmdb.org", ""})
	}
	var lastErr error
	for _, cd := range cands {
		b := strings.TrimRight(cd.base, "/")
		if !strings.HasSuffix(b, "/t/p") {
			b += "/t/p" // 配置里一般只填到域名，海报尺寸挂在 /t/p 下
		}
		data, err := imgGet(imgHTTPClient(cd.proxy), b+"/"+size+"/"+strings.TrimPrefix(p, "/"))
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	log.Printf("[海报] ✗ 拉取失败 %s/%s：配置域名与官方域名均不可达，最后错误: %v", size, p, lastErr)
	return nil, lastErr
}

// cachedTMDBImage 带磁盘缓存的 TMDB 图（路径内容寻址，永不过期）
func cachedTMDBImage(dataDir, size, p string) ([]byte, error) {
	return cachedImage(dataDir, "tmdb", size+p, 0, func() ([]byte, error) { return fetchTMDBImage(size, p) })
}

// validTMDBPath TMDB 图片路径形如 /abcDEF123.jpg：只放行这一层，防借代理访问任意 URL
func validTMDBPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 128 || strings.Contains(p, "..") || strings.Count(p, "/") != 1 {
		return false
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".svg":
		return true
	}
	return false
}

// tmdbImgCacheControl TMDB 图 URL 不会换内容，浏览器缓存一年
const tmdbImgCacheControl = "public, max-age=31536000, immutable"
