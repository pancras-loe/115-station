package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
	"gorm.io/gorm"
)

// 参考 p115strmhelper/helper/r302：预取与播放共用 UA 缓存和在途请求，
// 空 UA 也按原样取链；直连失败不能悄悄退化成服务器转发整段视频。
type playbackLinkFlight struct {
	done chan struct{}
	url  string
	err  error
}

type playbackLinkResolver struct {
	mu      sync.Mutex
	cache   map[string]downloadCacheEntry
	flights map[string]*playbackLinkFlight
	fetch   func(*gorm.DB, *config.Config, string, string) (string, map[string]string, error)
}

func newPlaybackLinkResolver(fetch func(*gorm.DB, *config.Config, string, string) (string, map[string]string, error)) *playbackLinkResolver {
	return &playbackLinkResolver{cache: make(map[string]downloadCacheEntry), flights: make(map[string]*playbackLinkFlight), fetch: fetch}
}

var playbackLinks = newPlaybackLinkResolver(proxyDownloadURLFull)

func normalizePlaybackID(id string) string {
	id = strings.SplitN(id, "/", 2)[0]
	if i := strings.IndexByte(id, '.'); i >= 0 {
		id = id[:i]
	}
	if id == "" {
		return ""
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return ""
		}
	}
	return strings.ToLower(id)
}

func playbackPickcode(db *gorm.DB, id string) (string, error) {
	id = normalizePlaybackID(id)
	if id == "" {
		return "", fmt.Errorf("播放标识无效")
	}
	if isAllDigits(id) {
		var sf model.SyncedFile
		if db == nil || db.Where("file_id = ?", id).First(&sf).Error != nil || sf.PickCode == "" {
			return "", fmt.Errorf("旧文件 ID 未命中同步台账")
		}
		id = normalizePlaybackID(sf.PickCode)
	}
	if id == "" {
		return "", fmt.Errorf("台账播放标识无效")
	}
	return id, nil
}

func (r *playbackLinkResolver) resolve(ctx context.Context, db *gorm.DB, cfg *config.Config, id, ua string) (string, error) {
	pc, err := playbackPickcode(db, id)
	if err != nil {
		return "", err
	}
	key := pc + "|" + ua
	r.mu.Lock()
	if cached, ok := r.cache[key]; ok && time.Now().Before(cached.Expiry) {
		r.mu.Unlock()
		return cached.URL, nil
	}
	f, exists := r.flights[key]
	if !exists {
		f = &playbackLinkFlight{done: make(chan struct{})}
		r.flights[key] = f
		go func() {
			u, headers, fetchErr := r.fetch(db, cfg, pc, ua)
			expires := time.Now().Add(30 * time.Minute)
			if fetchErr == nil {
				expires, fetchErr = validatePlaybackLink(u, headers, ua, time.Now())
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			f.url, f.err = u, fetchErr
			if fetchErr == nil {
				// 有界缓存避免公开入口长期积累不同 UA。
				if len(r.cache) >= 4096 {
					clear(r.cache)
				}
				r.cache[key] = downloadCacheEntry{URL: u, Expiry: expires}
			}
			delete(r.flights, key)
			close(f.done)
		}()
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.done:
		return f.url, f.err
	}
}

func validatePlaybackLink(raw string, headers map[string]string, ua string, now time.Time) (time.Time, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return time.Time{}, fmt.Errorf("下载地址无效")
	}
	for k, v := range headers {
		if strings.EqualFold(k, "User-Agent") && v != ua {
			return time.Time{}, fmt.Errorf("直链 UA 与播放器不一致")
		}
		// f=1/2 是已验证可免 Cookie 的地址。f=3 及未知类型不能靠
		// 给 302 响应加 Cookie 头解决：浏览器不会把本站 Cookie 送给 CDN。
		if strings.EqualFold(k, "Cookie") && v != "" && u.Query().Get("f") != "1" && u.Query().Get("f") != "2" {
			return time.Time{}, fmt.Errorf("直链要求额外 Cookie，无法直接播放")
		}
	}
	if u.Query().Get("f") == "3" {
		return time.Time{}, fmt.Errorf("直链要求额外 Cookie，无法直接播放")
	}
	expires := now.Add(30 * time.Minute)
	if stamp, err := strconv.ParseInt(u.Query().Get("t"), 10, 64); err == nil {
		deadline := time.Unix(stamp, 0).Add(-5 * time.Minute)
		if deadline.Before(expires) {
			expires = deadline
		}
	}
	if !expires.After(now) {
		return time.Time{}, fmt.Errorf("下载地址已过期或即将过期")
	}
	return expires, nil
}

// 禁止下游缓存签名地址，避免账号切换、UA 变化后复用旧的 302。
func playbackRedirect(w http.ResponseWriter, req *http.Request, target string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Add("Vary", "User-Agent")
	// 直接设置 Location，HEAD 无响应体，也避免 HTML 中重复暴露签名地址。
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}
