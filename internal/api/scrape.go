package api

// ==================== 影视刮削（原生 NFO + 海报到本地媒体库） ====================
//
// 直接生成 Emby/Kodi 标准元数据，替代"Emby 刮削到本地"这半段：
//   按台账片目的 TMDB 编号拉详情 → 写 <视频同名>.nfo / tvshow.nfo / season.nfo
//   + poster.jpg / fanart.jpg / clearlogo.png / landscape.jpg / seasonNN-poster.jpg
//   + <集同名>-thumb.jpg 到本地媒体库对应片目目录，
//   用户显式允许上传后，落盘产物才由「监控上传」回传 115 对应目录。
// Emby 侧建议把元数据读取器设为仅 NFO（以本站数据为准），避免二次刮削覆盖。
//
// 两个入口（整理后自动刮削、本地文件页勾选）都是刮削队列里的 scrape 任务。
// 「开始刮削」全库已于 2026-09-29 删除：手动刮削只在本地文件页按片目点名（开着 Emby 提前探测时
// 一次全库就是成千上万次 115 直链请求）。执行器只有一个（localscrape.go 的 execScrapeJob），核心在 scrapecore.go。
// 刮削队列不拿 taskMu（taskqueue.go），几百集的综艺刮半小时也不挡整理与同步。
//
// 接口：GET/POST /scrape/config、GET /scrape/status

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

type scrapeCfg struct {
	LocalRoot   string `json:"local_root"`
	WriteNFO    bool   `json:"write_nfo"`
	WriteImages bool   `json:"write_images"`
	Force       bool   `json:"force"` // 覆盖已存在的元数据文件

	// AutoAfterOrganize 整理完成后自动刮削本轮入库的片目（orgSink.flushScrape 入刮削队列）。
	// 此前挂在增量同步末尾且扫全台账——新增一部片也要全库过一遍
	AutoAfterOrganize bool `json:"auto_after_organize"`

	// ProbeStreams 界面上的「轨道探测」：入库后让 Emby 提前探测媒体信息，第一次播放不用现场探测（embyextract.go）。
	// 默认关：每个条目探测一次就是一次 115 直链请求。
	// 2026-09-29 之前它控制的是本站自己 ffprobe 写 NFO streamdetails —— Emby 导入 NFO 不读那一段，已删，key 沿用
	ProbeStreams bool `json:"probe_streams"`

	// SkipSharedStills 同一季里多集共用同一张剧照（综艺常见）时判为占位图，这些集不写 -thumb.jpg。
	// Emby 没有集缩略图时用剧的背景图，观感和一墙同样的图差不多，但省下几百次下载。默认开
	SkipSharedStills bool `json:"skip_shared_stills"`
}

func loadScrapeCfg() scrapeCfg {
	c := scrapeCfg{WriteNFO: true, WriteImages: true, SkipSharedStills: true}
	if v := settingValueCompat("scrape"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	// 本地媒体库根目录只有一个来源。刮削配置中的旧字段继续保留用于兼容，
	// 但运行时必须跟随 full.local_path，避免同步、整理和刮削落到不同目录树。
	c.LocalRoot = strings.TrimRight(strings.TrimSpace(localMediaRoot()), "/")
	return c
}

func saveScrapeCfg(c scrapeCfg) error {
	b, _ := json.Marshal(c)
	return notifyConfigSource.SaveSetting("scrape", string(b))
}

// opts 全局配置 → 一次刮削任务的选项（整理后刮削用）
func (c scrapeCfg) opts() fileScrapeOpts {
	return fileScrapeOpts{WriteNFO: c.WriteNFO, WriteImages: c.WriteImages, Force: c.Force,
		Probe: c.ProbeStreams, SkipSharedStills: c.SkipSharedStills}
}

// tmdbFetchImageSized 按 TMDB 尺寸档（original / w780 …）拉图，走 TMDB 配置的图床 / 代理（国内直连常不通）。
// 集剧照一部剧就是几十上百张，原图单张常见 0.5–1MB，列表缩略图用不着那么大。
// 返回实际请求的地址：刮削日志要写清「从哪下的」，图床配错时一眼能看出来
func tmdbFetchImageSized(imgPath, size string) ([]byte, string, error) {
	var cfg model.TmdbConfig
	_ = model.DB.First(&cfg).Error // 没有配置行时 cfg 为零值：图床走默认官方，代理走全局
	imgURL := tmdbImageURL(cfg.ImageApiUrl, size, imgPath)
	req, err := http.NewRequest(http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, imgURL, err
	}
	proxyURL := getProxyURL()
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	resp, err := scrapeImgClient(proxyURL).Do(req)
	if err != nil {
		return nil, imgURL, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imgURL, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := readImageBody(resp.Body, scrapeImageMax)
	return data, imgURL, err
}

var (
	scrapeImgClientMu sync.Mutex
	scrapeImgClients  = map[string]*http.Client{}
)

// scrapeImgClient 刮削拉图的 HTTP 客户端，按代理地址复用。
// 此前每张图新建一个 Client + Transport，连接用完即弃，一部剧几百张剧照张张重新握手
// （走代理还要再建一次隧道）；片目内改成并发拉图之后更要复用连接。
// 没配代理时沿用默认 Transport 的环境变量代理（与改动前一致）
func scrapeImgClient(proxyURL string) *http.Client {
	scrapeImgClientMu.Lock()
	defer scrapeImgClientMu.Unlock()
	if c := scrapeImgClients[proxyURL]; c != nil {
		return c
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = scrapeImgWorkers
	if proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			tr.Proxy = pu
		}
	}
	// 原图背景常见几 MB，走代理时 20 秒不够读完
	c := &http.Client{Timeout: 60 * time.Second, Transport: tr}
	scrapeImgClients[proxyURL] = c
	return c
}

// scrapeImageMax 单张图的大小上限。TMDB 原图背景常见几 MB，碰到上限多半是图床返回了别的东西
const scrapeImageMax = 20 << 20

// readImageBody 读图片响应体并核对是不是一张完整的图。
// 多读一个字节才分得清「正好到上限」和「超了被截」：LimitReader 读满就停、不报错，
// 截断的图会被当成完整的写下去
func readImageBody(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("图片超过 %s 上限", humanBytes(limit))
	}
	if err := checkImageData(data); err != nil {
		return nil, err
	}
	return data, nil
}

// checkImageData 拉回来的是不是一张完整的图。
// 状态码 200 不等于拿到了图：图床 / 代理出错时会回 200 的 HTML 错误页；按连接关闭分界、
// 不带 Content-Length 的响应断在半路，ReadAll 也不报错。这两种写下去之后大小 > 0，
// 「只补缺失」会把它当成已有，永远不再重拉
func checkImageData(data []byte) error {
	if len(data) == 0 {
		return errors.New("图片内容为空")
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return fmt.Errorf("返回的不是图片（%s）", ct)
	}
	// 截断只查 TMDB 实际给的两种格式。结束标记之后允许带一点尾巴（个别编码器会补零），
	// 所以看末尾一段里有没有，而不是要求正好落在最后
	tail := data[max(0, len(data)-1024):]
	switch ct {
	case "image/jpeg":
		if !bytes.Contains(tail, []byte{0xFF, 0xD9}) {
			return errors.New("JPEG 不完整（缺结束标记，下载可能被截断）")
		}
	case "image/png":
		if !bytes.Contains(tail, []byte("IEND")) {
			return errors.New("PNG 不完整（缺 IEND，下载可能被截断）")
		}
	}
	return nil
}

// tmdbImageURL 拼图片地址。图床配置里常见三种写法都要认：只填域名、填到 /t/p、
// 连尺寸一起填（照抄别的工具的 https://image.tmdb.org/t/p/w500）——
// 最后一种此前被拼成 …/t/p/w500/t/p/original/xx.jpg，每张图都是 404
func tmdbImageURL(base, size, imgPath string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://image.tmdb.org"
	}
	if i := strings.Index(base, "/t/p"); i >= 0 {
		base = base[:i]
	}
	return base + "/t/p/" + size + imgPath
}

// ---- NFO 结构（Kodi/Emby 标准） ----

type nfoRating struct {
	XMLName xml.Name `xml:"rating"`
	Name    string   `xml:"name,attr"`
	Max     int      `xml:"max,attr"`
	Default bool     `xml:"default,attr"`
	Value   float64  `xml:"value"`
}

type nfoActor struct {
	Name string `xml:"name"`
	Role string `xml:"role"`
}

// nfoUniqueID Kodi 多唯一 ID 元素（同名不同 type 属性）；encoding/xml
// 不允许两个同名字段重复，需用切片
type nfoUniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

type nfoMovie struct {
	XMLName       xml.Name      `xml:"movie"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle"`
	Ratings       []nfoRating   `xml:"ratings>rating"`
	Year          string        `xml:"year"`
	Premiered     string        `xml:"premiered"`
	Runtime       int           `xml:"runtime"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
	Genres        []string      `xml:"genre"`
	Directors     []string      `xml:"director"`
	Studios       []string      `xml:"studio"`
	Actors        []nfoActor    `xml:"actor"`
	Plot          string        `xml:"plot"`
	TmdbID        string        `xml:"tmdbid"`
	IMDbID        string        `xml:"id"`
}

type nfoTVShow struct {
	XMLName       xml.Name      `xml:"tvshow"`
	Title         string        `xml:"title"`
	OriginalTitle string        `xml:"originaltitle"`
	Ratings       []nfoRating   `xml:"ratings>rating"`
	Year          string        `xml:"year"`
	Premiered     string        `xml:"premiered"`
	UniqueIDs     []nfoUniqueID `xml:"uniqueid"`
	Genres        []string      `xml:"genre"`
	Studios       []string      `xml:"studio"`
	Actors        []nfoActor    `xml:"actor"`
	Plot          string        `xml:"plot"`
	TmdbID        string        `xml:"tmdbid"`
}

// ---- 集级 NFO（episodedetails，与 STRM 同基名落盘 xxx.strm → xxx.nfo）----

type nfoEpisode struct {
	XMLName   xml.Name      `xml:"episodedetails"`
	Title     string        `xml:"title"`
	Season    int           `xml:"season"`
	Episode   int           `xml:"episode"`
	Aired     string        `xml:"aired"`
	Plot      string        `xml:"plot"`
	Ratings   []nfoRating   `xml:"ratings>rating"`
	UniqueIDs []nfoUniqueID `xml:"uniqueid"`
	Thumb     string        `xml:"thumb"`
}

// ---- 季级 NFO（season.nfo，放在季目录里）----
// Emby 没有它也能按目录名认季，有了才带上 TMDB 的季名与简介（「第 1 季」之外的「烈火篇」之类）

type nfoSeason struct {
	XMLName      xml.Name `xml:"season"`
	Title        string   `xml:"title"`
	Plot         string   `xml:"plot"`
	Premiered    string   `xml:"premiered,omitempty"`
	Year         string   `xml:"year,omitempty"`
	SeasonNumber int      `xml:"seasonnumber"`
}

func marshalNFO(v any) ([]byte, error) {
	b, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}

func writeMetaFile(dir, name string, content []byte, force bool) (bool, error) {
	dst := filepath.Join(dir, name)
	if !force {
		if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
			return false, nil // 已存在且不强制 → 跳过
		}
	}
	return true, writeFileAtomic(dst, content)
}

// writeFileAtomic 先写同目录的临时文件再改名。直接 WriteFile 写到一半进程被杀 / 磁盘满 /
// 挂载掉线，会留下一个大小 > 0 的残缺文件，之后「只补缺失」一直把它当成已有跳过。
// 临时名以 .tmp 结尾：监控上传只认标准图片名与 .nfo，不会把它传上网盘
func writeFileAtomic(dst string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0644) // CreateTemp 是 0600，与原来 WriteFile 的权限对齐（Emby 可能是另一个用户）
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// ---- 处理器 ----

// scrapeLaneStatus 刮削队列此刻在跑什么（刮削页的状态读它）
func scrapeLaneStatus() (running bool, progress string) {
	id, p := scrapeLane.current()
	if id == 0 {
		return false, ""
	}
	progress = p.Phase
	if p.Total > 0 {
		progress += fmt.Sprintf(" %d/%d", p.Done, p.Total)
	}
	if p.Label != "" {
		progress += "：" + p.Label
	}
	if s := p.Sub; s != nil {
		progress += fmt.Sprintf("（%s %d/%d）", s.Phase, s.Done, s.Total)
	}
	return true, progress
}

// ScrapeGetConfig GET /scrape/config → 配置 + 状态
func (h *Handler) ScrapeGetConfig(c *gin.Context) {
	cfg := loadScrapeCfg()
	running, progress := scrapeLaneStatus()
	c.JSON(http.StatusOK, gin.H{"cfg": cfg, "status": gin.H{"running": running, "progress": progress}})
}

// ScrapeSaveConfig POST /scrape/config
func (h *Handler) ScrapeSaveConfig(c *gin.Context) {
	var req scrapeCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 忽略旧客户端提交的独立根目录，统一使用媒体库位置配置。
	req.LocalRoot = strings.TrimRight(strings.TrimSpace(localMediaRoot()), "/")
	if err := saveScrapeCfg(req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[影视刮削] ✓ 配置已保存（根目录 %s，Emby 提前探测 %s，占位剧照 %s）",
		req.LocalRoot, onOff(req.ProbeStreams), map[bool]string{true: "不写", false: "照写"}[req.SkipSharedStills])
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ScrapeStatus GET /scrape/status
func (h *Handler) ScrapeStatus(c *gin.Context) {
	running, progress := scrapeLaneStatus()
	c.JSON(http.StatusOK, gin.H{"running": running, "progress": progress})
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// ---- 刮削对象与产物落点 ----
//
// 刮削核心（scrapecore.go）只管「拉 TMDB → 生成 NFO / 图片字节」，写到哪里交给 metaWriter：
// 唯一的实现是 fileScrapeWriter（localscrape.go），写本地媒体库，这一次勾了上传再写网盘。

// metaDest 一个元数据产物目录：本地与网盘两个落点，可以只有其一。
// 网盘落点用「已知 cid + 相对路径」表示，只在真的要上传时才逐级解析，不上传的刮削零 115 请求
type metaDest struct {
	Local     string // 本地绝对目录；空 = 本地没有对应目录
	CloudBase string // 网盘上一个已知目录的 cid；空 = 没有网盘落点
	CloudRel  string // 从 CloudBase 往下的相对路径（/ 分隔，空 = 就是 CloudBase）
}

// scrapeVideo 片目里的一个视频：影片 NFO / 集 NFO 与它的 STRM 同基名
// （xxx.strm → xxx.nfo；同名冲突退回旧写法的 xxx.mkv.strm → xxx.mkv.nfo），
// Emby 就是把 STRM 的扩展名换成 .nfo 去找的。Name 是 STRM 去掉 .strm，不一定是网盘上的视频名
type scrapeVideo struct {
	Name string // 视频文件名（带扩展名）
	Dir  metaDest
}

// scrapeTitle 一个待刮削片目
type scrapeTitle struct {
	Kind   string // movie / tv
	Title  string
	Year   string
	TmdbID int
	Dir    metaDest // 标题目录：tvshow.nfo、海报、季海报
	Videos []scrapeVideo
}

// metaWriter 产物出口。返回 wrote=false 表示按「只补缺失」跳过了；
// 本地目录已不在时返回 errMetaDirGone（刮削不建目录，见 fileScrapeWriter.put）
type metaWriter interface {
	put(d metaDest, name string, data []byte) (wrote bool, err error)
}

// metaSkipper writer 可选实现：拉图之前先问一声这个产物会不会被「只补缺失」跳过。
// 图片要从 TMDB 图床下载，集剧照一部剧就是几十上百张，已有的不该每轮刮削都重拉一遍。
// 返回 true 就当 put 过一次（计数由 writer 自己记），调用方不再拉图
type metaSkipper interface {
	skip(d metaDest, name string) bool
}

func localMetaExists(dir, name string) bool {
	st, err := os.Stat(filepath.Join(dir, name))
	return err == nil && st.Size() > 0
}

// scrapeReporter 错误、停止请求与片目内进度的去处（刮削队列）
type scrapeReporter interface {
	errf(format string, args ...any)
	stopped() bool
	// sub 片目内的第二级进度（集 NFO 87/212 · 当前文件）
	sub(phase string, done, total int, label string)
}

// tmdbImage /images 接口里的一张图
type tmdbImage struct {
	FilePath    string  `json:"file_path"`
	Lang        *string `json:"iso_639_1"` // null = 无文字的图
	VoteAverage float64 `json:"vote_average"`
	Width       int     `json:"width"`
}

// pickTMDBImage 按语言优先级挑一张：同语言里评分高者优先、再比宽度。
// langs 里的 "" 表示无语言（iso_639_1 为 null）；ext 非空时只要该扩展名。挑不到返回空
func pickTMDBImage(imgs []tmdbImage, langs []string, ext string) string {
	for _, lang := range langs {
		var best *tmdbImage
		for i := range imgs {
			im := &imgs[i]
			l := ""
			if im.Lang != nil {
				l = *im.Lang
			}
			if l != lang || im.FilePath == "" {
				continue
			}
			if ext != "" && !strings.EqualFold(path.Ext(im.FilePath), ext) {
				continue
			}
			if best == nil || im.VoteAverage > best.VoteAverage ||
				(im.VoteAverage == best.VoteAverage && im.Width > best.Width) {
				best = im
			}
		}
		if best != nil {
			return best.FilePath
		}
	}
	return ""
}

// scrapeEpisodeNo 刮削用的季集号：文件名没写季号按第 1 季。集号为 0 = 解析不出
func scrapeEpisodeNo(name string) (season, episode int) {
	season, episode, _ = scrapeEpisodeSpan(name)
	return
}

// scrapeEpisodeSpan 同 scrapeEpisodeNo，另给出双集文件（S04E01-E02）的结束集号，单集为 0
func scrapeEpisodeSpan(name string) (season, episode, end int) {
	fp := parseFileName(name)
	season = fp.Season
	if season == 0 {
		season = 1
	}
	return season, fp.Episode, fp.EpisodeEnd
}

// marshalEpisodeNFO 集 NFO。双集文件按 Kodi / Emby 的多集约定，同一个文件里依次放几段
// <episodedetails>（只有一个 XML 声明），Emby 据此把一个文件挂到两集上
func marshalEpisodeNFO(eps []nfoEpisode) ([]byte, error) {
	out := []byte(xml.Header)
	for i, e := range eps {
		b, err := xml.MarshalIndent(e, "", "  ")
		if err != nil {
			return nil, err
		}
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, b...)
	}
	return out, nil
}

// scrapeSeasonDirs 季号 → 季目录（season.nfo 的落点）。
// 只收「整个目录都是同一季」的：集文件直接平铺在标题目录下（没有季目录），
// 或一个目录里混着几季的，season.nfo 放进去说不清是哪一季，干脆不写
func scrapeSeasonDirs(videos []scrapeVideo, titleDir metaDest) map[int]metaDest {
	dirSeason := map[metaDest]int{} // -1 = 混了几季
	for _, v := range videos {
		season, epNo := scrapeEpisodeNo(v.Name)
		if epNo == 0 || v.Dir == titleDir {
			continue
		}
		if s, ok := dirSeason[v.Dir]; ok && s != season {
			dirSeason[v.Dir] = -1
		} else if !ok {
			dirSeason[v.Dir] = season
		}
	}
	out := map[int]metaDest{}
	dup := map[int]bool{}
	for dir, s := range dirSeason {
		if s < 0 {
			continue
		}
		if _, ok := out[s]; ok {
			dup[s] = true // 同一季分在两个目录里：哪个都不认
		}
		out[s] = dir
	}
	for s := range dup {
		delete(out, s)
	}
	return out
}

func dateYear(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}

// movieNFONames 影片目录里每个视频对应的 NFO 文件名。
//
// 与 STRM 同基名（xxx.strm → xxx.nfo），口径与集级 NFO 以及 Emby 自己
// 刮削出来的产物完全一致。固定名 movie.nfo 虽然 Emby 也认，但一个片目里放了
// 两个版本时两份元数据会打架，而且与 Emby 写出来的文件名对不上，
// 用户一眼看不出哪份是谁写的。
// 台账里查不到视频行（还没落盘/被清过）时才退回固定名
func movieNFONames(rows []model.SyncedFile) []string {
	vs := make([]scrapeVideo, 0, len(rows))
	for _, sf := range rows {
		vs = append(vs, scrapeVideo{Name: strings.TrimSuffix(path.Base(sf.RelPath), ".strm")})
	}
	return videoNFONames(vs)
}

// videoNFONames 同上，按刮削对象里的视频算
func videoNFONames(vs []scrapeVideo) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Name+".nfo")
	}
	if len(out) == 0 {
		return []string{"movie.nfo"}
	}
	return out
}

// scrapeDirVideoRows 台账里某片目录（key 含库名前缀）下的视频文件行，
// 取 pickcode 供轨道探测；带前缀/任意前缀两级 LIKE 兜底（与洗版查询同套路）。
// 只查 STRM 行、不设上限：此前取前 300 行再筛视频，综艺连同字幕 / NFO / 剧照的台账行
// 轻易过 300，排在后面的集被静默漏刮
func scrapeDirVideoRows(key string) []model.SyncedFile {
	base := strings.Trim(key, "/")
	if base == "" {
		return nil
	}
	strmRows := func(pattern string) []model.SyncedFile {
		var sfs []model.SyncedFile
		model.DB.Where(`rel_path LIKE ? ESCAPE '\' AND rel_path LIKE ?`, pattern, "%.strm").
			Order("rel_path").Find(&sfs)
		return sfs
	}
	sfs := strmRows(likeEscape(base) + "/%")
	if len(sfs) == 0 {
		sfs = strmRows("%/" + likeEscape(base) + "/%")
	}
	var out []model.SyncedFile
	for _, sf := range sfs {
		if scrapeVideoRow(sf) {
			out = append(out, sf)
		}
	}
	return out
}

// scrapeVideoRow 台账里一行 .strm 是不是要刮削的视频（本地文件页按片目分组时同一口径）
func scrapeVideoRow(sf model.SyncedFile) bool {
	if sf.PickCode == "" {
		return false
	}
	// STRM 名不再带视频扩展名（strmname.go），认视频看台账的 kind；旧写法的行仍按扩展名认
	return sf.Kind == "video" || videoExts[strings.ToLower(pathExt(strings.TrimSuffix(path.Base(sf.RelPath), ".strm")))]
}

// ---- TMDB 集信息（按季拉取，进程内缓存）----

type tmdbEpisodeInfo struct {
	Name          string  `json:"name"`
	AirDate       string  `json:"air_date"`
	Overview      string  `json:"overview"`
	StillPath     string  `json:"still_path"`
	EpisodeNumber int     `json:"episode_number"`
	VoteAverage   float64 `json:"vote_average"`
}

// scrapeSeasonTTL 集信息缓存多久：此前进程内永久缓存，连载中的剧新出的集在重启之前永远没有标题和剧照
const scrapeSeasonTTL = 6 * time.Hour

type seasonCacheEntry struct {
	eps map[int]tmdbEpisodeInfo
	at  time.Time
}

var (
	scrapeSeasonMu    sync.Mutex
	scrapeSeasonCache = map[string]seasonCacheEntry{}
)

// tmdbSeasonEpisodes 某季的集信息映射（集号 → 信息）。cached = 命中缓存；
// TMDB 失败返回空表与错误（集标题 / 剧照缺失时集级 NFO 仍会生成季集号），失败不缓存
func (tc *TmdbClient) tmdbSeasonEpisodes(tvID, season int) (eps map[int]tmdbEpisodeInfo, cached bool, err error) {
	cacheKey := fmt.Sprintf("%d:%d", tvID, season)
	scrapeSeasonMu.Lock()
	if e, ok := scrapeSeasonCache[cacheKey]; ok && time.Since(e.at) < scrapeSeasonTTL {
		scrapeSeasonMu.Unlock()
		return e.eps, true, nil
	}
	scrapeSeasonMu.Unlock()
	m := map[int]tmdbEpisodeInfo{}
	body, err := tc.get(fmt.Sprintf("/tv/%d/season/%d", tvID, season), map[string]string{"language": "zh-CN"})
	if err != nil {
		return m, false, err
	}
	var r struct {
		Episodes []tmdbEpisodeInfo `json:"episodes"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return m, false, fmt.Errorf("解析失败: %w", err)
	}
	for _, e := range r.Episodes {
		m[e.EpisodeNumber] = e
	}
	scrapeSeasonMu.Lock()
	scrapeSeasonCache[cacheKey] = seasonCacheEntry{eps: m, at: time.Now()}
	scrapeSeasonMu.Unlock()
	return m, false, nil
}
