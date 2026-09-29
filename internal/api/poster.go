package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// dashPosterSize 仪表盘海报的默认尺寸。海报墙最宽一百多像素，w342 给高分屏留足两倍；
// 原来一律拉 w500，一张七八十 KB。更小的位置（最近整理 36px、拼图格子）由前端带 size 要更小的图
const dashPosterSize = "w342"

// serveTMDBPoster GET /poster/*path[?size=w154] 为仪表盘代理 TMDB 海报，避免客户端直连 TMDB。缓存与回退见 imgcache.go
func serveTMDBPoster(c *gin.Context, dataDir string) {
	p := c.Param("path")
	if !validTMDBPath(p) {
		c.String(http.StatusBadRequest, "bad path")
		return
	}
	size := c.DefaultQuery("size", dashPosterSize)
	if !tmdbImageSizes[size] {
		size = dashPosterSize
	}
	data, err := cachedTMDBImage(dataDir, size, p)
	if err != nil {
		imgMissing(c)
		return
	}
	serveImage(c, data, tmdbImgCacheControl)
}
