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
// 三个入口（整理后自动刮削、「开始刮削」全库、本地文件页勾选）全部是刮削队列里的 scrape 任务，
// 执行器只有一个（localscrape.go 的 execScrapeJob），核心在 scrapecore.go。
// 刮削队列不拿 taskMu（taskqueue.go），几百集的综艺刮半小时也不挡整理与同步。
//
// 接口：GET/POST /scrape/config、POST /scrape/run、GET /scrape/status、POST /scrape/stop

import (
	"encoding/json"
	"encoding/xml"
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

	// ProbeStreams 刮削时逐个视频 ffprobe，把轨道写进 NFO 的 fileinfo/streamdetails。默认关：
	// 每个视频要取一次 115 直链再读十来 MB，几百集的剧多出几十分钟；Emby / Jellyfin 导入 NFO
	// 一般不读这一段，主要是 Kodi 用得上。此前是无条件探测，剧集的探测失败还被静默吞掉，
	// 维护者的 NFO 里一直没有 streamdetails 也没人发现（2026-09-28）
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

// opts 全局配置 → 一次刮削任务的选项（整理后刮削、全库刮削用）
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
	// 原图背景常见几 MB，走代理时 20 秒不够读完
	client := &http.Client{Timeout: 60 * time.Second}
	proxyURL := getProxyURL()
	if cfg.EnableProxy && cfg.ProxyUrl != "" {
		proxyURL = cfg.ProxyUrl
	}
	if proxyURL != "" {
		if pu, perr := parseProxyURL(proxyURL); perr == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, imgURL, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, imgURL, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return data, imgURL, err
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
	Fileinfo      *nfoFileInfo  `xml:"fileinfo,omitempty"`
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

// ---- 轨道信息（Kodi/Emby 标准的 fileinfo/streamdetails）----
// 播放器/媒体库据此显示内嵌音轨与字幕，无需对 strm 远端 URL 做探测——
// 对 strm 媒体库（探测常超时/不全）尤其重要

// 字段与顺序照 Emby 回写 NFO 时的写法（2026-09-29 维护者现场拿到的 Emby NFO 对照）：
// codec / micodec 同值，scantype 音轨也写，default / forced 写成 True / False，
// duration 是整分钟（向下取整）、durationinseconds 是秒。
// scantype / default / forced 只有探测带了扩展字段（probeTrack.Ext）才写，老缓存里没有就不猜

type nfoStreamVideo struct {
	Codec             string `xml:"codec,omitempty"`
	MiCodec           string `xml:"micodec,omitempty"`
	Bitrate           int    `xml:"bitrate,omitempty"`
	Width             int    `xml:"width,omitempty"`
	Height            int    `xml:"height,omitempty"`
	Aspect            string `xml:"aspect,omitempty"`
	AspectRatio       string `xml:"aspectratio,omitempty"`
	FrameRate         string `xml:"framerate,omitempty"`
	Language          string `xml:"language,omitempty"`
	ScanType          string `xml:"scantype,omitempty"`
	Default           string `xml:"default,omitempty"`
	Forced            string `xml:"forced,omitempty"`
	Duration          int    `xml:"duration,omitempty"`
	DurationInSeconds int    `xml:"durationinseconds,omitempty"`
}

type nfoStreamAudio struct {
	Codec        string `xml:"codec,omitempty"`
	MiCodec      string `xml:"micodec,omitempty"`
	Bitrate      int    `xml:"bitrate,omitempty"`
	Language     string `xml:"language,omitempty"`
	ScanType     string `xml:"scantype,omitempty"`
	Channels     int    `xml:"channels,omitempty"`
	SamplingRate int    `xml:"samplingrate,omitempty"`
	Default      string `xml:"default,omitempty"`
	Forced       string `xml:"forced,omitempty"`
}

type nfoStreamSubtitle struct {
	Codec    string `xml:"codec,omitempty"`
	MiCodec  string `xml:"micodec,omitempty"`
	Language string `xml:"language,omitempty"`
	Name     string `xml:"name,omitempty"`
	ScanType string `xml:"scantype,omitempty"`
	Default  string `xml:"default,omitempty"`
	Forced   string `xml:"forced,omitempty"`
}

type nfoStreamDetails struct {
	Video    *nfoStreamVideo     `xml:"video,omitempty"`
	Audio    []nfoStreamAudio    `xml:"audio,omitempty"`
	Subtitle []nfoStreamSubtitle `xml:"subtitle,omitempty"`
}

type nfoFileInfo struct {
	StreamDetails *nfoStreamDetails `xml:"streamdetails"`
}

// nfoFlags 一条轨道的 scantype / default / forced（没有扩展字段时三个都空，omitempty 不输出）
func nfoFlags(t probeTrack) (scan, def, forced string) {
	if !t.Ext {
		return "", "", ""
	}
	scan = "progressive"
	if t.Interlaced {
		scan = "interlaced"
	}
	tf := func(b bool) string {
		if b {
			return "True"
		}
		return "False"
	}
	return scan, tf(t.Default), tf(t.Forced)
}

// nfoFileInfoFrom 探测结果 → streamdetails（无有效轨道时返回 nil）
func nfoFileInfoFrom(probe *probeResult) *nfoFileInfo {
	if probe == nil {
		return nil
	}
	sd := &nfoStreamDetails{}
	for _, s := range probe.Streams {
		scan, def, forced := nfoFlags(s)
		switch s.Kind {
		case "video":
			if sd.Video == nil {
				sd.Video = &nfoStreamVideo{Codec: s.Codec, MiCodec: s.Codec, Bitrate: s.Bitrate,
					Width: s.Width, Height: s.Height, Aspect: s.Aspect, AspectRatio: s.Aspect,
					FrameRate: s.FrameRate, Language: s.Language, ScanType: scan, Default: def, Forced: forced,
					Duration: probe.Duration / 60, DurationInSeconds: probe.Duration}
			}
		case "audio":
			sd.Audio = append(sd.Audio, nfoStreamAudio{Codec: s.Codec, MiCodec: s.Codec, Bitrate: s.Bitrate,
				Language: s.Language, ScanType: scan, Channels: s.Channels, SamplingRate: s.SampleRate,
				Default: def, Forced: forced})
		case "subtitle":
			sd.Subtitle = append(sd.Subtitle, nfoStreamSubtitle{Codec: s.Codec, MiCodec: s.Codec,
				Language: s.Language, Name: s.Title, ScanType: scan, Default: def, Forced: forced})
		}
	}
	if sd.Video == nil && len(sd.Audio) == 0 && len(sd.Subtitle) == 0 {
		return nil
	}
	return &nfoFileInfo{StreamDetails: sd}
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
	Fileinfo  *nfoFileInfo  `xml:"fileinfo,omitempty"`
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
	return true, os.WriteFile(dst, content, 0644)
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
	log.Printf("[影视刮削] ✓ 配置已保存（根目录 %s，轨道探测 %s，占位剧照 %s）",
		req.LocalRoot, onOff(req.ProbeStreams), map[bool]string{true: "不写", false: "照写"}[req.SkipSharedStills])
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// scrapeAllDedupe 全库刮削的去重键：排着或跑着一个就不再收第二个
const scrapeAllDedupe = "all"

// ScrapeRun POST /scrape/run → 全库刮削入刮削队列（按已保存的配置）
func (h *Handler) ScrapeRun(c *gin.Context) {
	cfg := loadScrapeCfg()
	if cfg.LocalRoot == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置本地媒体库根目录"})
		return
	}
	if !cfg.WriteNFO && !cfg.WriteImages {
		c.JSON(http.StatusBadRequest, gin.H{"error": "NFO 与图片至少要生成一项"})
		return
	}
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var n int64
	h.DB.Model(&model.TaskJob{}).Where("kind = ? AND dedupe_key = ? AND status IN ?",
		jobKindScrape, scrapeAllDedupe, []string{jobQueued, jobRunning}).Count(&n)
	if n > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "全库刮削已在刮削队列里"})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: jobKindScrape, Title: "全库刮削", DedupeKey: scrapeAllDedupe, Source: "web", Priority: jobPriorityManual,
		Params: jobParams{Local: &localScrapeParams{All: true, Scrape: cfg.opts()}},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "全库刮削")
}

// ScrapeStatus GET /scrape/status
func (h *Handler) ScrapeStatus(c *gin.Context) {
	running, progress := scrapeLaneStatus()
	c.JSON(http.StatusOK, gin.H{"running": running, "progress": progress})
}

// ScrapeStop POST /scrape/stop → 停掉正在跑的刮削（当前文件写完即退出），
// 并取消排着的全库刮削与整理后刮削。本地文件页手动提交的留着：那是用户点名要刮的，要取消去任务队列
func (h *Handler) ScrapeStop(c *gin.Context) {
	stopped := false
	if id, _ := scrapeLane.current(); id != 0 {
		stopped = scrapeLane.requestStop(id)
	}
	res := h.DB.Model(&model.TaskJob{}).
		Where("kind = ? AND status = ? AND (dedupe_key = ? OR priority = ?)", jobKindScrape, jobQueued, scrapeAllDedupe, jobPriorityBackground).
		Updates(map[string]interface{}{"status": jobCanceled, "message": "已取消（未执行）", "finished_at": time.Now()})
	msg := "没有正在进行的刮削"
	switch {
	case stopped && res.RowsAffected > 0:
		msg = fmt.Sprintf("已请求停止，当前文件写完即退出；另取消了 %d 个排队中的刮削", res.RowsAffected)
	case stopped:
		msg = "已请求停止，当前文件写完即退出"
	case res.RowsAffected > 0:
		msg = fmt.Sprintf("已取消 %d 个排队中的刮削", res.RowsAffected)
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
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
	Name     string // 视频文件名（带扩展名）
	PickCode string // 轨道探测用
	Dir      metaDest
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

// metaPatcher writer 可选实现：「只补缺失」要跳过的产物，读出已有内容改一处再写回。
// 用在开了轨道探测、而视频 NFO 早就有了的时候：整份重写会冲掉 NFO 里别处来的内容
// （网盘上带过来的、用户手改的），只往里补 streamdetails
type metaPatcher interface {
	existing(d metaDest, name string) (data []byte, ok bool)
	replace(d metaDest, name string, data []byte) (wrote bool, err error)
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
	fp := parseFileName(name)
	season = fp.Season
	if season == 0 {
		season = 1
	}
	return season, fp.Episode
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
		if sf.PickCode == "" {
			continue
		}
		// STRM 名不再带视频扩展名（strmname.go），认视频看台账的 kind；旧写法的行仍按扩展名认
		if sf.Kind == "video" || videoExts[strings.ToLower(pathExt(strings.TrimSuffix(path.Base(sf.RelPath), ".strm")))] {
			out = append(out, sf)
		}
	}
	return out
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
