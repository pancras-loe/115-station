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

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
)

// ==================== 本地文件页：本地媒体库的片目卡片 ====================
//
// 一张卡片 = 台账里的一个片目（库名/<分类>/<标题目录>，ledger.go 的 scanLedgerTitles），
// 状态与详情抽屉同一套口径（inspectLocalTitleDetail + grade）：片目级 / 季 / 每集的 NFO、海报、背景图
// 都在才算刮全。此前只看标题目录里有没有 NFO 与海报，剧集缺集 NFO、缺背景图的一律显示「已刮削」。
// 形态参考 LitePan 的「STRM 刮削 → 海报墙」，
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
	// Status ok = 必需产物（各级 NFO、海报、背景图）都在；partial = 缺一部分；miss = 一样都没有
	Status string `json:"status"`
	// Lack 缺的必需产物（「背景图」「3 集 NFO」…），卡片角标直接写它
	Lack []string `json:"lack,omitempty"`
	// Soft 缺的可选图片（剧照、季海报）：TMDB 上不一定有、占位剧照会被故意跳过，只提示，不影响 Status
	Soft []string `json:"soft,omitempty"`
	// Emby 媒体信息（打开页面时拉的 Emby 快照，localemby.go）；快照没有或 Emby 里没这个片目时为空
	Emby *localEmbyStat `json:"emby,omitempty"`
	// Missing 本地没有这个标题目录（台账有、本地被删了或挂载没就绪）
	Missing bool      `json:"missing,omitempty"`
	LastAt  time.Time `json:"last_at"`
	// Poster 海报缩略图的查询串（key / v / sig），前端拼成 /api/local/poster?…
	Poster string `json:"poster,omitempty"`
}

// 卡片列表缓存：一次列表要把每个片目的目录（标题 / 季）全读一遍，上千部时每翻一页都重读太浪费。
// 刮削任务结束时 forgetLocalTitles 清掉（下一次请求当场重建，刚刮完的状态要立刻看得到）；
// 别的写入（整理落盘、Emby 刮削）靠过期：过期后先把旧快照给出去、后台重建，最多晚一次请求可见。
// 此前过期就在请求里同步重建，大库在 NAS 上一次要一两秒，每隔 30 秒打开页面都要干等
var (
	localTitlesMu    sync.Mutex
	localTitlesCache []localTitle
	localTitlesAt    time.Time
	// localTitlesGen 每次 forget 加一：后台重建开始前记下，写回时对不上说明中途被清过，结果作废
	localTitlesGen      int
	localTitlesBuilding bool
)

const localTitlesTTL = 30 * time.Second

func forgetLocalTitles() {
	localTitlesMu.Lock()
	localTitlesCache = nil
	localTitlesGen++
	localTitlesMu.Unlock()
}

func (h *Handler) localTitlesSnapshot(refresh bool) []localTitle {
	localTitlesMu.Lock()
	defer localTitlesMu.Unlock()
	if !refresh && localTitlesCache != nil {
		if time.Since(localTitlesAt) >= localTitlesTTL && !localTitlesBuilding {
			localTitlesBuilding = true
			gen := localTitlesGen
			go func() {
				out := h.buildLocalTitles(false)
				localTitlesMu.Lock()
				defer localTitlesMu.Unlock()
				localTitlesBuilding = false
				if gen == localTitlesGen {
					localTitlesCache, localTitlesAt = out, time.Now()
				}
			}()
		}
		return localTitlesCache
	}
	out := h.buildLocalTitles(refresh)
	localTitlesGen++ // 正在跑的后台重建比这份旧，别让它写回来盖掉
	localTitlesCache, localTitlesAt = out, time.Now()
	return out
}

// buildLocalTitles 重建卡片快照：读台账、读每个片目的目录。refresh = 台账也不用缓存
func (h *Handler) buildLocalTitles(refresh bool) []localTitle {
	root := localMediaRoot()
	ledger := scanLedgerTitlesCached()
	if refresh {
		ledger = scanLedgerTitles()
	}
	rows := ledgerVideoRowsByTitle()
	out := make([]localTitle, 0, len(ledger))
	for _, e := range ledger {
		if e.Key == "" {
			continue
		}
		d := inspectLocalTitleDetail(root, e, rows[e.Key])
		t := d.localTitle
		if t.HasPoster {
			t.Poster = h.localPosterQuery(e.Key, d.posterV)
		}
		out = append(out, t)
	}
	return out
}

type inspectedTitle struct {
	localTitle
	posterV int64 // 海报 mtime：进缩略图 URL，换了图浏览器缓存自然失效
}

// localPosterNames 标题目录里认作海报的文件名（小写比较）。poster.jpg 是我们刮削写的，
// 其余是 Emby / 别的刮削器常见写法
var localPosterNames = []string{"poster.jpg", "poster.png", "folder.jpg", "folder.png", "cover.jpg"}

// perVideoImage 标准名都没有时，认 Emby 按视频名存的图（<视频名>-poster.jpg / -fanart.jpg），
// names 是目录里的小写文件名，kind 是 poster / fanart。按名字排序取第一个，结果稳定。
//
// 一个目录里有多个视频（多版本电影）时，Emby 存图用的是「视频名-poster.jpg」，
// 还会把我们刮削写的 poster.jpg 删掉（2026-09-30《夏洛特烦恼》两个版本现场）。
// 只认 poster.jpg 的话，这个片目刮完 Emby 一刷新就又变回「未刮全」，重刮也只是再被删一次。
// seasonNN-poster.jpg / season-specials-poster.jpg 是季海报，不算
func perVideoImage(names []string, kind string) string {
	var hit []string
	for _, n := range names {
		for _, ext := range []string{".jpg", ".png"} {
			if strings.HasSuffix(n, "-"+kind+ext) && !strings.HasPrefix(n, "season") {
				hit = append(hit, n)
			}
		}
	}
	if len(hit) == 0 {
		return ""
	}
	sort.Strings(hit)
	return hit[0]
}

// inspectLocalTitle 读一次标题目录，看片目级 NFO 与海报在不在。Status 由 inspectLocalTitleDetail 的 grade 定
func inspectLocalTitle(root string, e *ledgerTitleEntry) inspectedTitle {
	return inspectLocalTitleIn(root, e, localDirIndex{})
}

// inspectLocalTitleIn 同 inspectLocalTitle，标题目录从 idx 读（详情接着要用同一个目录，不再读第二遍）
func inspectLocalTitleIn(root string, e *ledgerTitleEntry, idx localDirIndex) inspectedTitle {
	t := inspectedTitle{localTitle: localTitle{
		Key: e.Key, Title: e.Title, Year: e.Year, TmdbID: e.TmdbID, MediaType: e.MediaType,
		Category: e.Category, Videos: e.Videos, LastAt: e.LastAt,
	}}
	if t.Title == "" {
		t.Title = filepath.Base(e.Key)
	}
	var names map[string]os.FileInfo
	if root != "" {
		names = idx.files(filepath.Join(root, filepath.FromSlash(e.Key)))
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
	candidates := localPosterNames
	if len(names) > 0 {
		lower := make([]string, 0, len(names))
		for n := range names {
			lower = append(lower, n)
		}
		if n := perVideoImage(lower, "poster"); n != "" {
			candidates = append(append([]string{}, localPosterNames...), n)
		}
	}
	for _, n := range candidates {
		if info, ok := names[n]; ok && info.Size() > 0 {
			t.HasPoster = true
			t.posterV = info.ModTime().Unix()
			break
		}
	}
	return t
}

// ledgerVideoRowsByTitle 台账视频行按片目分组：一次查表，代替每个片目各跑一次 scrapeDirVideoRows。
// 分组用 scanLedgerTitles 同一个 titleOf，key 两边对得上；行的取舍与 scrapeDirVideoRows 一致
func ledgerVideoRowsByTitle() map[string][]model.SyncedFile {
	out := map[string][]model.SyncedFile{}
	if model.DB == nil {
		return out
	}
	layout := loadLibCategoryLayout()
	var sfs []model.SyncedFile
	model.DB.Where("rel_path LIKE ? AND pick_code <> ''", "%.strm").Order("rel_path").Find(&sfs)
	for _, sf := range sfs {
		if !scrapeVideoRow(sf) {
			continue
		}
		if key, _, _, _, ok := layout.titleOf(sf.RelPath); ok {
			out[key] = append(out[key], sf)
		}
	}
	return out
}

// localTitleStats 筛选栏上的计数
type localTitleStats struct {
	All     int `json:"all"`
	OK      int `json:"ok"`
	Partial int `json:"partial"`
	Miss    int `json:"miss"`
	Movie   int `json:"movie"`
	TV      int `json:"tv"`
	// ProbeLack Emby 里还缺媒体信息的片目数（套用其余全部筛选）；没有 Emby 快照时为 0
	ProbeLack int `json:"probe_lack"`
}

// localTitleQuery 列表筛选
type localTitleQuery struct {
	Keyword   string
	MediaType string // movie / tv / 空
	Status    string // ok / partial / miss / 空
	Sort      string // added_desc（默认）/ title / year_desc / year_asc
	// Probe lack = 只看 Emby 里还缺媒体信息的片目（要有 Emby 快照才生效）
	Probe string
	// Emby 片目 key → 媒体信息计数（localemby.go 的快照），挂到返回的卡片上
	Emby map[string]localEmbyStat
}

// filterLocalTitles 纯函数：筛选 + 排序 + 计数。
// 状态计数只套用关键词与类型，类型计数只套用关键词与状态 —— 切换一边的筛选时另一边的数字仍然有意义。
// 「缺媒体信息」和关键词一样是收窄条件，它自己的计数套用其余全部筛选。
// all 是缓存里的切片，只读；Emby 计数挂在复制出来的元素上
func filterLocalTitles(all []localTitle, q localTitleQuery) ([]localTitle, localTitleStats) {
	kw := strings.ToLower(strings.TrimSpace(q.Keyword))
	var st localTitleStats
	out := make([]localTitle, 0, len(all))
	for _, t := range all {
		if kw != "" && !strings.Contains(strings.ToLower(t.Title), kw) && !strings.Contains(strings.ToLower(t.Key), kw) &&
			(t.TmdbID == 0 || strconv.Itoa(t.TmdbID) != kw) {
			continue
		}
		if es, ok := q.Emby[t.Key]; ok {
			t.Emby = &es
		}
		typeOK := q.MediaType == "" || t.MediaType == q.MediaType
		statusOK := q.Status == "" || t.Status == q.Status
		lacking := t.Emby != nil && t.Emby.Lack > 0
		if typeOK && statusOK && lacking {
			st.ProbeLack++
		}
		if q.Probe == "lack" && len(q.Emby) > 0 && !lacking {
			continue
		}
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

// ListLocalTitles GET /local/titles?q=&type=&status=&probe=&sort=&offset=&limit=&refresh=&all=
//
// all=1 一次返回全部筛选结果：给「全选筛选结果」用。列表本来就在内存快照里（零 115 请求），
// 一次给全比让前端按 200 一页循环拉省事，也不会翻页途中快照过期导致前后对不上
func (h *Handler) ListLocalTitles(c *gin.Context) {
	root := localMediaRoot()
	if root == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false, "items": []localTitle{}, "total": 0, "stats": localTitleStats{}})
		return
	}
	all := h.localTitlesSnapshot(c.Query("refresh") == "1")
	list, st := filterLocalTitles(all, localTitleQuery{
		Keyword: c.Query("q"), MediaType: c.Query("type"), Status: c.Query("status"), Sort: c.Query("sort"),
		Probe: c.Query("probe"), Emby: localEmbyStats(),
	})
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if c.Query("all") == "1" {
		offset, limit = 0, len(list)
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

// localPosterWidths 海报缩略图可选的宽度档：前端按卡片实际显示宽度 × 设备像素比要（w=），
// 往上取到最近一档。此前一律 400px：小卡片、列表视图（40px 宽）也拿 400px 的图，
// 服务器上行带宽小的时候一屏几十张图要等一两秒。只开放几档，免得任意宽度把缓存撑爆
var localPosterWidths = []int{120, 180, 240, 320, localPosterThumbW}

// localPosterWidth 请求的宽度 → 档位；没带或不认识的给最大档（老前端 / 收藏的链接）
func localPosterWidth(q string) int {
	w, err := strconv.Atoi(q)
	if err != nil || w <= 0 {
		return localPosterThumbW
	}
	for _, b := range localPosterWidths {
		if w <= b {
			return b
		}
	}
	return localPosterThumbW
}

// localFanartNames 标题目录里认作背景图的文件名（小写比较）
var localFanartNames = []string{"fanart.jpg", "fanart.png", "backdrop.jpg", "backdrop.png", "landscape.jpg"}

// 缩略图内存缓存（路径 + mtime → JPEG）。一张 400px 宽的 JPEG 约 30–50KB，
// 上限几百张不到 30MB；满了整张清掉重来，比维护 LRU 简单，浏览器那头还有一周的缓存
var (
	posterThumbMu    sync.Mutex
	posterThumbCache = map[string][]byte{}
)

const posterThumbMax = 600

// LocalPoster GET /local/poster?key=&v=&sig=[&w=][&img=fanart]（公开路由）
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
	names, kind, width := localPosterNames, "poster", localPosterWidth(c.Query("w"))
	if c.Query("img") == "fanart" {
		names, kind, width = localFanartNames, "fanart", localFanartThumbW
	}
	file, info := findLocalImage(dir, names, kind)
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

func findLocalImage(dir string, names []string, kind string) (string, os.FileInfo) {
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
	all := make([]string, 0, len(byLower))
	for n := range byLower {
		all = append(all, n)
	}
	if n := perVideoImage(all, kind); n != "" {
		names = append(append([]string{}, names...), n)
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
