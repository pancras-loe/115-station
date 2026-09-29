package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
)

// ==================== 本地文件页：本地媒体库的片目卡片 ====================
//
// 一张卡片 = 台账里的一个片目（库名/<分类>/<标题目录>，ledger.go 的 scanLedgerTitles），
// 状态看本地标题目录里有没有 NFO 与海报。形态参考 LitePan 的「STRM 刮削 → 海报墙」，
// 但 LitePan 没有台账，要从 STRM 树往上猜作品根、猜电影还是剧、在目录里放隐藏标记记状态；
// 我们的片目边界与类型台账早就给了，这里只读目录，不写任何标记文件。
//
// 只读本地，零 115 请求。

// localTitle 一张卡片
type localTitle struct {
	Key       string `json:"key"` // 台账片目 key（含库名前缀）：刮削提交的就是它
	Title     string `json:"title"`
	Year      string `json:"year,omitempty"`
	TmdbID    int    `json:"tmdb_id,omitempty"`
	MediaType string `json:"media_type"`
	Category  string `json:"category"`
	Videos    int    `json:"videos"`
	HasNFO    bool   `json:"has_nfo"`
	HasPoster bool   `json:"has_poster"`
	// Status ok = NFO 与海报都有；partial = 缺一样；miss = 都没有
	Status string `json:"status"`
	// Missing 本地没有这个标题目录（台账有、本地被删了或挂载没就绪）
	Missing bool      `json:"missing,omitempty"`
	LastAt  time.Time `json:"last_at"`
	// Poster 海报缩略图的查询串（key / v / sig），前端拼成 /api/local/poster?…
	Poster string `json:"poster,omitempty"`
}

// 卡片列表缓存：一次列表要把每个片目的标题目录读一遍，上千部时每翻一页都重读太浪费。
// 刮削任务结束时 forgetLocalTitles 清掉，别的写入（整理落盘、Emby 刮削）最多晚 30 秒可见
var (
	localTitlesMu    sync.Mutex
	localTitlesCache []localTitle
	localTitlesAt    time.Time
)

const localTitlesTTL = 30 * time.Second

func forgetLocalTitles() {
	localTitlesMu.Lock()
	localTitlesCache = nil
	localTitlesMu.Unlock()
}

func (h *Handler) localTitlesSnapshot(refresh bool) []localTitle {
	localTitlesMu.Lock()
	defer localTitlesMu.Unlock()
	if !refresh && localTitlesCache != nil && time.Since(localTitlesAt) < localTitlesTTL {
		return localTitlesCache
	}
	root := localMediaRoot()
	ledger := scanLedgerTitlesCached()
	if refresh {
		ledger = scanLedgerTitles()
	}
	out := make([]localTitle, 0, len(ledger))
	for _, e := range ledger {
		if e.Key == "" {
			continue
		}
		t := inspectLocalTitle(root, e)
		if t.HasPoster {
			t.Poster = h.localPosterQuery(e.Key, t.posterV)
		}
		out = append(out, t.localTitle)
	}
	localTitlesCache, localTitlesAt = out, time.Now()
	return out
}

type inspectedTitle struct {
	localTitle
	posterV int64 // 海报 mtime：进缩略图 URL，换了图浏览器缓存自然失效
}

// localPosterNames 标题目录里认作海报的文件名（小写比较）。poster.jpg 是我们刮削写的，
// 其余是 Emby / 别的刮削器常见写法
var localPosterNames = []string{"poster.jpg", "poster.png", "folder.jpg", "folder.png", "cover.jpg"}

// inspectLocalTitle 读一次标题目录，看 NFO 与海报在不在
func inspectLocalTitle(root string, e *ledgerTitleEntry) inspectedTitle {
	t := inspectedTitle{localTitle: localTitle{
		Key: e.Key, Title: e.Title, Year: e.Year, TmdbID: e.TmdbID, MediaType: e.MediaType,
		Category: e.Category, Videos: e.Videos, LastAt: e.LastAt,
	}}
	if t.Title == "" {
		t.Title = filepath.Base(e.Key)
	}
	var names map[string]os.DirEntry
	if root != "" {
		if ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(e.Key))); err == nil {
			names = make(map[string]os.DirEntry, len(ents))
			for _, d := range ents {
				if !d.IsDir() {
					names[strings.ToLower(d.Name())] = d
				}
			}
		}
	}
	if names == nil {
		t.Missing = true
	}
	if e.MediaType == "tv" {
		_, t.HasNFO = names["tvshow.nfo"]
	} else {
		// 电影的 NFO 与视频同基名（xxx.nfo），兜底是 movie.nfo：标题目录里有任何一个就算
		for n := range names {
			if strings.HasSuffix(n, ".nfo") {
				t.HasNFO = true
				break
			}
		}
	}
	for _, n := range localPosterNames {
		if d, ok := names[n]; ok {
			if info, err := d.Info(); err == nil && info.Size() > 0 {
				t.HasPoster = true
				t.posterV = info.ModTime().Unix()
				break
			}
		}
	}
	switch {
	case t.HasNFO && t.HasPoster:
		t.Status = "ok"
	case t.HasNFO || t.HasPoster:
		t.Status = "partial"
	default:
		t.Status = "miss"
	}
	return t
}

// localTitleStats 筛选栏上的计数
type localTitleStats struct {
	All     int `json:"all"`
	OK      int `json:"ok"`
	Partial int `json:"partial"`
	Miss    int `json:"miss"`
	Movie   int `json:"movie"`
	TV      int `json:"tv"`
}

// localTitleQuery 列表筛选
type localTitleQuery struct {
	Keyword   string
	MediaType string // movie / tv / 空
	Status    string // ok / partial / miss / 空
	Sort      string // added_desc（默认）/ title / year_desc / year_asc
}

// filterLocalTitles 纯函数：筛选 + 排序 + 计数。
// 状态计数只套用关键词与类型，类型计数只套用关键词与状态 —— 切换一边的筛选时另一边的数字仍然有意义
func filterLocalTitles(all []localTitle, q localTitleQuery) ([]localTitle, localTitleStats) {
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	var st localTitleStats
	out := make([]localTitle, 0, len(all))
	for _, t := range all {
		if kw != "" && !strings.Contains(strings.ToLower(t.Title), kw) && !strings.Contains(strings.ToLower(t.Key), kw) &&
			(t.TmdbID == 0 || strconv.Itoa(t.TmdbID) != kw) {
			continue
		}
		typeOK := q.MediaType == "" || t.MediaType == q.MediaType
		statusOK := q.Status == "" || t.Status == q.Status
		if statusOK {
			if t.MediaType == "tv" {
				st.TV++
			} else {
				st.Movie++
			}
		}
		if typeOK {
			st.All++
			switch t.Status {
			case "ok":
				st.OK++
			case "partial":
				st.Partial++
			default:
				st.Miss++
			}
		}
		if typeOK && statusOK {
			out = append(out, t)
		}
	}
	less := func(a, b localTitle) bool {
		switch q.Sort {
		case "title":
			return a.Title < b.Title
		case "year_desc":
			return a.Year > b.Year
		case "year_asc":
			return a.Year < b.Year
		default:
			return a.LastAt.After(b.LastAt)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if less(a, b) {
			return true
		}
		if less(b, a) {
			return false
		}
		return a.Key < b.Key
	})
	return out, st
}

// ListLocalTitles GET /local/titles?q=&type=&status=&sort=&offset=&limit=&refresh=
func (h *Handler) ListLocalTitles(c *gin.Context) {
	root := localMediaRoot()
	if root == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false, "items": []localTitle{}, "total": 0, "stats": localTitleStats{}})
		return
	}
	all := h.localTitlesSnapshot(c.Query("refresh") == "1")
	list, st := filterLocalTitles(all, localTitleQuery{
		Keyword: c.Query("q"), MediaType: c.Query("type"), Status: c.Query("status"), Sort: c.Query("sort"),
	})
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	offset = max(0, min(offset, len(list)))
	end := min(offset+limit, len(list))
	missing := 0
	for _, t := range all {
		if t.Missing {
			missing++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"configured": true, "root": root,
		"items": list[offset:end], "total": len(list), "offset": offset, "limit": limit,
		"stats": st, "missing": missing,
	})
}

// ---- 海报缩略图 ----
//
// <img> 带不了 Authorization 头，这条路由只能公开；列表接口按 key 签一个 HMAC（JWT 密钥），
// 没拿到过列表的人猜不出 URL。key 还要落在本地媒体库根下，防 ../ 读任意文件。

func (h *Handler) localPosterSig(key string) string {
	m := hmac.New(sha256.New, []byte(h.Config.JWTSecret))
	m.Write([]byte("local-poster\x00" + key))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (h *Handler) localPosterQuery(key string, v int64) string {
	return "key=" + url.QueryEscape(key) + "&v=" + strconv.FormatInt(v, 10) + "&sig=" + h.localPosterSig(key)
}

// localFanartQuery 背景图（片目详情的头图）。签名还是按 key 签：同一个目录里挑哪张图不涉及越权
func (h *Handler) localFanartQuery(key string, v int64) string {
	return h.localPosterQuery(key, v) + "&img=fanart"
}

// localPosterThumbW 缩略图宽度：卡片最宽 ~200px，给高分屏留两倍；背景图铺在详情抽屉顶部，最宽 ~760px
const (
	localPosterThumbW = 400
	localFanartThumbW = 960
)

// localFanartNames 标题目录里认作背景图的文件名（小写比较）
var localFanartNames = []string{"fanart.jpg", "fanart.png", "backdrop.jpg", "backdrop.png", "landscape.jpg"}

// 缩略图内存缓存（路径 + mtime → JPEG）。一张 400px 宽的 JPEG 约 30–50KB，
// 上限几百张不到 30MB；满了整张清掉重来，比维护 LRU 简单，浏览器那头还有一周的缓存
var (
	posterThumbMu    sync.Mutex
	posterThumbCache = map[string][]byte{}
)

const posterThumbMax = 600

// LocalPoster GET /local/poster?key=&v=&sig=[&img=fanart]（公开路由）
func (h *Handler) LocalPoster(c *gin.Context) {
	key := strings.Trim(c.Query("key"), "/")
	sig := c.Query("sig")
	if key == "" || !hmac.Equal([]byte(sig), []byte(h.localPosterSig(key))) {
		c.Status(http.StatusForbidden)
		return
	}
	root := localMediaRoot()
	if root == "" {
		c.Status(http.StatusNotFound)
		return
	}
	dir, ok := underRoot(root, key)
	if !ok {
		c.Status(http.StatusForbidden)
		return
	}
	names, width := localPosterNames, localPosterThumbW
	if c.Query("img") == "fanart" {
		names, width = localFanartNames, localFanartThumbW
	}
	file, info := findLocalImage(dir, names)
	if file == "" {
		c.Status(http.StatusNotFound)
		return
	}
	ck := file + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "|" + strconv.Itoa(width)
	posterThumbMu.Lock()
	data := posterThumbCache[ck]
	posterThumbMu.Unlock()
	if data == nil {
		// 内存没有再查磁盘：内存缓存一重启就空，一屏几十张 2000px 原图重新解码，
		// 在 NAS 的小 CPU 上要好几秒，首屏海报一张张往外蹦
		var err error
		data, err = cachedImage(h.Config.DataDir, "localthumb", ck, 0, func() ([]byte, error) {
			imgFetchSem <- struct{}{}
			defer func() { <-imgFetchSem }()
			return posterThumb(file, width)
		})
		if err != nil {
			// 解不开（webp 之类）就原样给，浏览器自己会画
			c.Header("Cache-Control", "private, max-age=604800")
			c.File(file)
			return
		}
		posterThumbMu.Lock()
		if len(posterThumbCache) >= posterThumbMax {
			posterThumbCache = map[string][]byte{}
		}
		posterThumbCache[ck] = data
		posterThumbMu.Unlock()
	}
	// URL 里带着 mtime（v=），换图后 URL 就变了，缓存可以放心给长
	serveImage(c, data, "private, max-age=2592000, immutable")
}

// underRoot key（/ 分隔的相对路径）拼到 root 下，并确认没有逃出 root
func underRoot(root, key string) (string, bool) {
	if strings.Contains(key, "\\") || strings.Contains(key, "\x00") {
		return "", false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	p := filepath.Join(absRoot, filepath.FromSlash(key))
	rel, err := filepath.Rel(absRoot, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return p, true
}

func findLocalImage(dir string, names []string) (string, os.FileInfo) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", nil
	}
	byLower := map[string]os.DirEntry{}
	for _, d := range ents {
		if !d.IsDir() {
			byLower[strings.ToLower(d.Name())] = d
		}
	}
	for _, n := range names {
		if d, ok := byLower[n]; ok {
			if info, err := d.Info(); err == nil && info.Size() > 0 {
				return filepath.Join(dir, d.Name()), info
			}
		}
	}
	return "", nil
}

// posterThumb 缩到 w 宽的 JPEG；原图本来就不宽时只重新编码（原图常见 2000px、1MB 上下）
func posterThumb(file string, w int) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := src
	if b.Dx() > w {
		h := b.Dy() * w / b.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
