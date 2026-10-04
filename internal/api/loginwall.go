package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
)

// ==================== 登录页背景剧照 ====================
//
// 登录页背景轮播影视剧照，形态参考 MoviePilot（它取 TMDB 热门的 backdrop）。
// 这个接口没登录就能访问，所以只给 TMDB 本周热门 —— 公开数据；
// **别改成取本地媒体库 / Emby 的条目**：那等于把用户库里有什么片子告诉任何一个打开登录页的人。
//
// 和 MoviePilot 不同的两点（维护者明确要求）：
//   - **网络不通也要有图**：MoviePilot 每次打开登录页现拉 TMDB，连不上就是一片黑。这里由后台
//     预取：拉到热门列表后把每张图转好存进本地缓存，接口**只列已经在本地的**，永远不现场等 TMDB；
//     列表本身落盘（loginWallFile），重启后 TMDB 不通也照样有上一次的图。
//   - **服务器上行带宽小**：不直接转发 TMDB 的 w1280（约 110KB），而是重新编码成 960 / 1280 两档
//     （960 档约 40KB），另带一张 32px 的小图（不到 1KB，base64 直接内嵌在列表里），
//     前端先把小图模糊着铺满，大图到了再淡入 —— 带宽再小打开登录页也不会是空白。

type loginWallpaper struct {
	Path  string `json:"path"` // TMDB backdrop_path，形如 /abc.jpg
	Title string `json:"title"`
	Year  string `json:"year,omitempty"`
	Thumb string `json:"thumb"` // 32px 小图的 data URI，大图加载前模糊铺底
}

const (
	loginWallTTL      = 6 * time.Hour
	loginWallRetry    = 30 * time.Minute // 上一轮失败（没配 TMDB / 连不上）隔多久再试
	loginWallMax      = 12
	loginWallSrcSize  = "w1280" // 转码的源图；original 动辄几 MB，没必要
	loginWallQuality  = 65      // 剧照上压着一层暗角，65 看不出区别，比 82 小一半
	loginWallThumbW   = 32
	loginWallCacheNS  = "loginwall"
	loginWallFileName = "loginwall.json"
)

// loginWallWidths 大图的宽度档。前端按铺满后的实际宽度 × 设备像素比要，往上取一档
var loginWallWidths = []int{960, 1280}

var loginWall struct {
	sync.Mutex
	dataDir string
	items   []loginWallpaper
	at      time.Time // 上一次成功刷新
	tried   time.Time // 上一次尝试（含失败）
	lastErr string    // 同一个错误只记一次日志：没配 TMDB 的用户别每半小时刷一行
	running bool
}

type loginWallState struct {
	Items []loginWallpaper `json:"items"`
	At    time.Time        `json:"at"`
}

// startLoginWallRefresher 读回上次的列表，之后每 10 分钟看一眼要不要刷新
func startLoginWallRefresher(dataDir string) {
	loadLoginWallState(dataDir)
	go func() {
		for {
			refreshLoginWallIfStale()
			time.Sleep(10 * time.Minute)
		}
	}()
}

// loadLoginWallState 读回落盘的列表：重启后 TMDB 不通也有上一次的图
func loadLoginWallState(dataDir string) {
	loginWall.Lock()
	defer loginWall.Unlock()
	loginWall.dataDir = dataDir
	if b, err := os.ReadFile(filepath.Join(dataDir, loginWallFileName)); err == nil {
		var st loginWallState
		if json.Unmarshal(b, &st) == nil {
			loginWall.items, loginWall.at = st.Items, st.At
		}
	}
}

func refreshLoginWallIfStale() {
	loginWall.Lock()
	due := !loginWall.running &&
		time.Since(loginWall.at) > loginWallTTL &&
		time.Since(loginWall.tried) > loginWallRetry
	if due {
		loginWall.running, loginWall.tried = true, time.Now()
	}
	dataDir := loginWall.dataDir
	loginWall.Unlock()
	if !due {
		return
	}

	items, err := buildLoginWallpapers(dataDir)

	loginWall.Lock()
	defer loginWall.Unlock()
	loginWall.running = false
	if err != nil {
		// 失败保留旧列表：它们的图都在本地，TMDB 不通照样能用
		if msg := err.Error(); msg != loginWall.lastErr {
			loginWall.lastErr = msg
			log.Printf("[登录页] ○ 刷新背景剧照失败（沿用已缓存的 %d 张）: %v", len(loginWall.items), err)
		}
		return
	}
	loginWall.items, loginWall.at, loginWall.lastErr = items, time.Now(), ""
	if b, err := json.Marshal(loginWallState{Items: items, At: loginWall.at}); err == nil {
		file := filepath.Join(dataDir, loginWallFileName)
		if os.WriteFile(file+".tmp", b, 0o644) == nil {
			_ = os.Rename(file+".tmp", file)
		}
	}
	log.Printf("[登录页] ✓ 背景剧照已更新：%d 张", len(items))
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

// buildLoginWallpapers 拉热门列表，逐张预取并转码。只收各档都转好的；一张都没成算失败（保留旧列表）
func buildLoginWallpapers(dataDir string) ([]loginWallpaper, error) {
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	body, err := tc.get("/trending/all/week", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []tmdbTrendingItem `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := []loginWallpaper{}
	for _, w := range pickLoginWallpapers(resp.Results) {
		if err := prefetchLoginWallpaper(dataDir, &w); err != nil {
			continue
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return nil, errors.New("剧照一张都没拉到")
	}
	return out, nil
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

// prefetchLoginWallpaper 把各档大图转好放进缓存，并填上内嵌小图
func prefetchLoginWallpaper(dataDir string, w *loginWallpaper) error {
	for _, width := range loginWallWidths {
		if _, err := loginWallImage(dataDir, w.Path, width); err != nil {
			return err
		}
	}
	src, err := cachedTMDBImage(dataDir, loginWallSrcSize, w.Path)
	if err != nil {
		return err
	}
	thumb, err := resizeJPEG(src, loginWallThumbW, 60)
	if err != nil {
		return err
	}
	w.Thumb = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(thumb)
	return nil
}

// loginWallImage 某一档的大图：先查缓存，没有才从 TMDB 源图转（源图本身也有缓存）
func loginWallImage(dataDir, p string, width int) ([]byte, error) {
	return cachedImage(dataDir, loginWallCacheNS, p+"@"+strconv.Itoa(width), 0, func() ([]byte, error) {
		src, err := cachedTMDBImage(dataDir, loginWallSrcSize, p)
		if err != nil {
			return nil, err
		}
		return resizeJPEG(src, width, loginWallQuality)
	})
}

// resizeJPEG 缩到 w 宽并重新编码；原图不够宽时只重新编码（不放大）
func resizeJPEG(data []byte, w, quality int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := src
	if b.Dx() > w {
		dst := image.NewRGBA(image.Rect(0, 0, w, b.Dy()*w/b.Dx()))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// loginWallWidth 请求的宽度 → 档位；没带或不认识的给最小档（省带宽优先）
func loginWallWidth(q string) int {
	w, err := strconv.Atoi(q)
	if err != nil || w <= 0 {
		return loginWallWidths[0]
	}
	for _, b := range loginWallWidths {
		if w <= b {
			return b
		}
	}
	return loginWallWidths[len(loginWallWidths)-1]
}

// currentLoginWallpapers 当前可用的剧照：只列本地缓存里还在的（缓存清理会删 60 天没用过的图）
func currentLoginWallpapers() []loginWallpaper {
	loginWall.Lock()
	items, dataDir := loginWall.items, loginWall.dataDir
	loginWall.Unlock()
	out := []loginWallpaper{}
	for _, w := range items {
		ok := true
		for _, width := range loginWallWidths {
			if _, err := os.Stat(imgCacheFile(dataDir, loginWallCacheNS, w.Path+"@"+strconv.Itoa(width))); err != nil {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, w)
		}
	}
	return out
}

// LoginWallpapers GET /auth/wallpapers：不访问 TMDB，只读本地。一张都没有时前端退回光晕背景
func (h *Handler) LoginWallpapers(c *gin.Context) {
	go refreshLoginWallIfStale() // 过期了顺手在后台刷新，这次先给旧的
	c.Header("Cache-Control", "no-cache")
	c.JSON(http.StatusOK, gin.H{"items": currentLoginWallpapers()})
}

// LoginWallpaperImage GET /auth/wallpaper?path=&w=：转好的大图。
// 只认当前列表里的路径 —— 公开接口不能变成任意 TMDB 图的缩放代理（每张都要解码重编码，白白吃 CPU）
func (h *Handler) LoginWallpaperImage(c *gin.Context) {
	p := c.Query("path")
	listed := false
	loginWall.Lock()
	for _, w := range loginWall.items {
		if w.Path == p {
			listed = true
			break
		}
	}
	dataDir := loginWall.dataDir
	loginWall.Unlock()
	if !listed {
		imgMissing(c)
		return
	}
	data, err := loginWallImage(dataDir, p, loginWallWidth(c.Query("w")))
	if err != nil {
		imgMissing(c)
		return
	}
	serveImage(c, data, tmdbImgCacheControl)
}
