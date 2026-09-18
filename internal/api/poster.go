package api

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// serveTMDBPoster 为仪表盘代理海报并缓存七天，避免客户端直连 TMDB。
func serveTMDBPoster(c *gin.Context, dataDir string) {
	p := strings.TrimPrefix(c.Param("path"), "/")
	if p == "" || strings.Contains(p, "..") {
		c.String(http.StatusBadRequest, "bad path")
		return
	}
	cacheDir := filepath.Join(dataDir, "posters")
	_ = os.MkdirAll(cacheDir, 0755)
	h := sha1.Sum([]byte(p))
	cacheFile := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+filepath.Ext(p))
	if st, err := os.Stat(cacheFile); err == nil && st.Size() > 0 && time.Since(st.ModTime()) < 7*24*time.Hour {
		c.Header("Cache-Control", "public, max-age=604800")
		c.File(cacheFile)
		return
	}
	// 服务端拉取，多级回退：配置图片域名(走代理) → 配置域名直连 → 官方域名直连。
	// 此前单次失败即静默回占位图，配置了不可达的图片域名时所有海报永远空白
	buildClient := func(withProxy bool) *http.Client {
		client := &http.Client{Timeout: 10 * time.Second}
		if withProxy {
			if pu := getProxyURL(); pu != "" {
				if pr, err := parseProxyURL(pu); err == nil {
					client.Transport = &http.Transport{Proxy: pr}
				}
			}
		}
		return client
	}
	candidates := []struct {
		base      string
		withProxy bool
	}{
		{tmdbImageBase(), true},
		{tmdbImageBase(), false},
		{"https://image.tmdb.org", false},
	}
	var data []byte
	var lastCT string
	var lastErr string
	for i, cand := range candidates {
		if i > 0 && cand.base == candidates[i-1].base && cand.withProxy == candidates[i-1].withProxy {
			continue
		}
		u := cand.base + "/t/p/w500/" + strings.TrimPrefix(p, "/")
		resp, err := buildClient(cand.withProxy).Get(u)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) < 100 {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
			continue
		}
		data, lastCT = body, resp.Header.Get("Content-Type")
		break
	}
	if len(data) == 0 {
		// 三条链路都失败：日志写明原因（占位图避免卡片裂图）
		log.Printf("[海报] ✗ 拉取失败 %s：配置域名与官方域名均不可达，最后错误: %s", p, lastErr)
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/gif", []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
		return
	}
	_ = os.WriteFile(cacheFile, data, 0644)
	if lastCT == "" {
		lastCT = "image/jpeg"
	}
	c.Header("Cache-Control", "public, max-age=604800")
	c.Data(http.StatusOK, lastCT, data)
}
