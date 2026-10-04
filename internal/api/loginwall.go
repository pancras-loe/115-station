package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 登录页背景海报 ====================
//
// 登录页背景轮播影视剧照，形态参考 MoviePilot（它取 TMDB 热门的 backdrop）。
// 这个接口没登录就能访问，所以只给 TMDB 本周热门 —— 公开数据；
// **别改成取本地媒体库 / Emby 的条目**：那等于把用户库里有什么片子告诉任何一个打开登录页的人。
// 图片本身走公开的 /tmdb/img 代理（落盘缓存），前端拼地址，这里只给路径与片名。

type loginWallpaper struct {
	Path  string `json:"path"` // TMDB backdrop_path，形如 /abc.jpg
	Title string `json:"title"`
	Year  string `json:"year,omitempty"`
}

const (
	loginWallTTL     = 6 * time.Hour
	loginWallFailTTL = 10 * time.Minute // TMDB 没配 / 连不上时别每次打开登录页都等一轮超时
	loginWallMax     = 12
)

var loginWall struct {
	sync.Mutex
	items []loginWallpaper
	at    time.Time
	ttl   time.Duration
}

// LoginWallpapers GET /auth/wallpapers：TMDB 本周热门的剧照。拿不到返回空列表，前端退回纯色背景
func (h *Handler) LoginWallpapers(c *gin.Context) {
	loginWall.Lock()
	defer loginWall.Unlock()
	if loginWall.at.IsZero() || time.Since(loginWall.at) > loginWall.ttl {
		items, err := fetchLoginWallpapers()
		loginWall.items, loginWall.at, loginWall.ttl = items, time.Now(), loginWallTTL
		if err != nil {
			loginWall.ttl = loginWallFailTTL
			log.Printf("[登录页] ○ 取背景海报失败（退回纯色背景）: %v", err)
		}
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.JSON(http.StatusOK, gin.H{"items": loginWall.items})
}

// tmdbTrendingItem trending/all 的一条（电影用 title / release_date，剧集用 name / first_air_date）
type tmdbTrendingItem struct {
	MediaType    string `json:"media_type"`
	Title        string `json:"title"`
	Name         string `json:"name"`
	BackdropPath string `json:"backdrop_path"`
	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`
}

func fetchLoginWallpapers() ([]loginWallpaper, error) {
	tc, err := loadTmdbClient()
	if err != nil {
		return []loginWallpaper{}, err
	}
	body, err := tc.get("/trending/all/week", nil)
	if err != nil {
		return []loginWallpaper{}, err
	}
	var resp struct {
		Results []tmdbTrendingItem `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return []loginWallpaper{}, err
	}
	return pickLoginWallpapers(resp.Results), nil
}

// pickLoginWallpapers 只要电影 / 剧集里带剧照的（trending/all 还会混进人物）
func pickLoginWallpapers(rs []tmdbTrendingItem) []loginWallpaper {
	out := []loginWallpaper{}
	for _, r := range rs {
		if (r.MediaType != "movie" && r.MediaType != "tv") || !validTMDBPath(r.BackdropPath) {
			continue
		}
		w := loginWallpaper{Path: r.BackdropPath, Title: r.Title}
		date := r.ReleaseDate
		if r.MediaType == "tv" {
			w.Title, date = r.Name, r.FirstAirDate
		}
		if strings.TrimSpace(w.Title) == "" {
			continue
		}
		if len(date) >= 4 {
			w.Year = date[:4]
		}
		out = append(out, w)
		if len(out) >= loginWallMax {
			break
		}
	}
	return out
}
