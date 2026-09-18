package api

// ==================== 影视刮削（原生 NFO + 海报到本地媒体库） ====================
//
// 直接生成 Emby/Kodi 标准元数据，替代"Emby 刮削到本地"这半段：
//   按 MediaLibrary(TmdbID) 拉 TMDB 详情 → 写 movie.nfo / tvshow.nfo
//   + poster.jpg / fanart.jpg / seasonNN-poster.jpg 到本地媒体库对应片目目录，
//   落盘后由「监控上传」自动回传 115 对应目录。
// Emby 侧建议把元数据读取器设为仅 NFO（以本站数据为准），避免二次刮削覆盖。
//
// 接口：GET/POST /scrape/config、POST /scrape/run、GET /scrape/status

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"strmhub/internal/model"
)

type scrapeCfg struct {
	LocalRoot   string `json:"local_root"`
	WriteNFO    bool   `json:"write_nfo"`
	WriteImages bool   `json:"write_images"`
	Force       bool   `json:"force"` // 覆盖已存在的元数据文件

	AutoAfterOrganize bool `json:"auto_after_organize"` // 增量同步动过媒体库后自动开始刮削
}

func loadScrapeCfg() scrapeCfg {
	c := scrapeCfg{WriteNFO: true, WriteImages: true}
	if v := settingValueCompat("scrape"); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	c.LocalRoot = strings.TrimRight(strings.TrimSpace(c.LocalRoot), "/")
	return c
}

func saveScrapeCfg(c scrapeCfg) error {
	b, _ := json.Marshal(c)
	return notifyConfigSource.SaveSetting("scrape", string(b))
}

// ---- 运行状态 ----

type scrapeStatus struct {
	Running bool     `json:"running"`
	Total   int      `json:"total"`
	Done    int      `json:"done"`
	Failed  int      `json:"failed"`
	Current string   `json:"current"`
	Errors  []string `json:"errors"`
}

var (
	scrapeMu       sync.Mutex
	scrapeSt       scrapeStatus
	scrapeStopFlag bool
)

// scrapeAutoTrigger 增量同步动过媒体库后的自动刮削入口（开关+运行中去重）。
// 刮削进行中：元数据文件边生成边由每分钟的监控上传分批回传（天然并行）；
// 刮削结束：再补一轮监控上传 + 元数据回传，收尾兜底
func (h *Handler) scrapeAutoTrigger() {
	cfg := loadScrapeCfg()
	if !cfg.AutoAfterOrganize {
		return
	}
	scrapeMu.Lock()
	running := scrapeSt.Running
	scrapeMu.Unlock()
	if running {
		return
	}
	scrapeSt = scrapeStatus{Running: true, Errors: []string{}}
	log.Printf("[影视刮削] ▶ 整理完成，自动开始刮削")
	go func() {
		h.scrapeAll(cfg)
		// 收尾兜底：等最后一写落盘后，立即各跑一轮回传（不等分钟级 ticker）
		go func() {
			time.Sleep(2 * time.Second)
			monitorOnce(h)
			h.uploadMetadataOnce()
		}()
	}()
}

func scrapeStatusSnapshot() scrapeStatus {
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	st := scrapeSt
	st.Errors = append([]string(nil), scrapeSt.Errors...)
	return st
}

func scrapeAddErr(format string, args ...any) {
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	scrapeSt.Failed++
	if len(scrapeSt.Errors) < 20 {
		scrapeSt.Errors = append(scrapeSt.Errors, fmt.Sprintf(format, args...))
	}
}

// Mukaku 风格的图片拉取：走 TMDB 配置的图床/代理（国内直连常不通）
func tmdbFetchImageBytes(imgPath string) ([]byte, error) {
	var cfg model.TmdbConfig
	if err := model.DB.First(&cfg).Error; err != nil || cfg.ImageApiUrl == "" {
		return nil, fmt.Errorf("TMDB 图床未配置")
	}
	base := strings.TrimRight(cfg.ImageApiUrl, "/")
	if !strings.HasSuffix(base, "/t/p") {
		base += "/t/p"
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/original"+imgPath, nil)
	client := &http.Client{Timeout: 20 * time.Second}
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
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
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

type nfoStreamVideo struct {
	Codec             string `xml:"codec,omitempty"`
	Width             int    `xml:"width,omitempty"`
	Height            int    `xml:"height,omitempty"`
	DurationInSeconds int    `xml:"durationinseconds,omitempty"`
}

type nfoStreamAudio struct {
	Codec    string `xml:"codec,omitempty"`
	Language string `xml:"language,omitempty"`
	Channels int    `xml:"channels,omitempty"`
}

type nfoStreamSubtitle struct {
	Language string `xml:"language,omitempty"`
	Name     string `xml:"name,omitempty"`
}

type nfoStreamDetails struct {
	Video    *nfoStreamVideo     `xml:"video,omitempty"`
	Audio    []nfoStreamAudio    `xml:"audio,omitempty"`
	Subtitle []nfoStreamSubtitle `xml:"subtitle,omitempty"`
}

type nfoFileInfo struct {
	StreamDetails *nfoStreamDetails `xml:"streamdetails"`
}

// nfoFileInfoFrom 探测结果 → streamdetails（无有效轨道时返回 nil）
func nfoFileInfoFrom(probe *probeResult) *nfoFileInfo {
	if probe == nil {
		return nil
	}
	sd := &nfoStreamDetails{}
	for _, s := range probe.Streams {
		switch s.Kind {
		case "video":
			if sd.Video == nil {
				sd.Video = &nfoStreamVideo{Codec: s.Codec, Width: s.Width, Height: s.Height, DurationInSeconds: probe.Duration}
			}
		case "audio":
			sd.Audio = append(sd.Audio, nfoStreamAudio{Codec: s.Codec, Language: s.Language, Channels: s.Channels})
		case "subtitle":
			sd.Subtitle = append(sd.Subtitle, nfoStreamSubtitle{Language: s.Language, Name: s.Title})
		}
	}
	if sd.Video == nil && len(sd.Audio) == 0 && len(sd.Subtitle) == 0 {
		return nil
	}
	return &nfoFileInfo{StreamDetails: sd}
}

// ---- 集级 NFO（episodedetails，与视频同名落盘 xxx.mkv → xxx.mkv.nfo）----

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

// ScrapeGetConfig GET /scrape/config → 配置 + 状态
func (h *Handler) ScrapeGetConfig(c *gin.Context) {
	cfg := loadScrapeCfg()
	c.JSON(http.StatusOK, gin.H{"cfg": cfg, "status": scrapeStatusSnapshot()})
}

// ScrapeSaveConfig POST /scrape/config
func (h *Handler) ScrapeSaveConfig(c *gin.Context) {
	var req scrapeCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req.LocalRoot = strings.TrimRight(strings.TrimSpace(req.LocalRoot), "/")
	if err := saveScrapeCfg(req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[影视刮削] ✓ 配置已保存（根目录 %s）", req.LocalRoot)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ScrapeRun POST /scrape/run → 后台刮削（进行中时拒绝重复触发）
func (h *Handler) ScrapeRun(c *gin.Context) {
	scrapeMu.Lock()
	if scrapeSt.Running {
		scrapeMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "刮削正在进行中"})
		return
	}
	scrapeSt = scrapeStatus{Running: true, Errors: []string{}}
	scrapeMu.Unlock()
	go h.scrapeAll(loadScrapeCfg())
	c.JSON(http.StatusOK, gin.H{"message": "刮削已开始，可刷新状态查看进度"})
}

// ScrapeStatus GET /scrape/status
func (h *Handler) ScrapeStatus(c *gin.Context) {
	c.JSON(http.StatusOK, scrapeStatusSnapshot())
}

// ScrapeStop POST /scrape/stop → 停止本轮（当前条目处理完即退出）
func (h *Handler) ScrapeStop(c *gin.Context) {
	scrapeMu.Lock()
	if scrapeSt.Running {
		scrapeStopFlag = true
	}
	scrapeMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"message": "已请求停止，当前条目处理完即退出"})
}

func scrapeStopRequested() bool {
	select {
	case <-stopCh:
		return true
	default:
	}
	scrapeMu.Lock()
	defer scrapeMu.Unlock()
	return scrapeStopFlag
}

// scrapeAll 主流程：遍历媒体库（有 tmdb id 的）逐条刮削
func (h *Handler) scrapeAll(cfg scrapeCfg) {
	defer func() {
		scrapeMu.Lock()
		scrapeSt.Running = false
		scrapeSt.Current = ""
		scrapeMu.Unlock()
		log.Printf("[影视刮削] ■ 本轮结束：完成 %d，失败 %d", scrapeStatusSnapshot().Done, scrapeStatusSnapshot().Failed)
	}()
	// 边刮边传：刮削期间每分钟的监控上传已在分批回传；结束后立即补一轮
	go func() {
		time.Sleep(2 * time.Second) // 等最后写入落盘
		monitorOnce(h)
		h.uploadMetadataOnce()
	}()
	if cfg.LocalRoot == "" {
		scrapeAddErr("未配置本地媒体库根目录")
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		scrapeAddErr("TMDB 未配置: %v", err)
		return
	}
	// 以台账标题目录为刮削单位（每片一条，目录真实存在于 115/本地）。
	// 不用 MediaLibrary.TargetPath：剧集行记录的是"每集文件路径"，
	// 拿它当目录会在本地造出 <集名>.mkv/ 的假目录
	entries := scanLedgerTitlesCached()
	type scrapeTarget struct {
		key, kind, title, year string
		tmdbID                 int
	}
	var targets []scrapeTarget
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		if e.TmdbID > 0 {
			targets = append(targets, scrapeTarget{key: e.Key, kind: "movie", title: e.Title, year: e.Year, tmdbID: int(e.TmdbID)})
			if e.MediaType == "tv" {
				targets[len(targets)-1].kind = "tv"
			}
			continue
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].key < targets[j].key })
	scrapeMu.Lock()
	scrapeSt.Total = len(targets)
	scrapeMu.Unlock()
	if len(targets) == 0 {
		return
	}
	log.Printf("[影视刮削] ▶ 开始：共 %d 个片目（根目录 %s）", len(targets), cfg.LocalRoot)
	for _, t := range targets {
		if scrapeStopRequested() {
			return
		}
		scrapeMu.Lock()
		scrapeSt.Current = t.title
		scrapeMu.Unlock()
		dir := filepath.Join(cfg.LocalRoot, filepath.FromSlash(strings.Trim(t.key, "/")))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			scrapeAddErr("%s: 创建目录失败 %v", t.title, err)
			scrapeMu.Lock()
			scrapeSt.Done++
			scrapeMu.Unlock()
			continue
		}
		h.scrapeOne(tc, cfg, dir, t.key, t.kind, t.title, t.year, t.tmdbID)
		scrapeMu.Lock()
		scrapeSt.Done++
		scrapeMu.Unlock()
		time.Sleep(150 * time.Millisecond) // TMDB 限速保护
	}
}

// scrapeOne 单个片目：详情 → NFO + 图片
func (h *Handler) scrapeOne(tc *TmdbClient, cfg scrapeCfg, dir, key, kind, title, year string, tmdbID int) {
	kindPath := kind
	params := map[string]string{"language": "zh-CN", "append_to_response": "credits"}
	body, err := tc.get("/"+kindPath+"/"+strconv.Itoa(tmdbID), params)
	if err != nil {
		// 详情 404 = 目录名标记的 tmdb id 查无条目（整理时匹配错/条目已删）：
		// 回退按标题+年份搜一次，自愈错误 ID
		var alt *TmdbMedia
		if kind == "tv" {
			alt, _ = tc.SearchTV(title, year)
		} else {
			alt, _ = tc.SearchMovie(title, year)
		}
		if alt == nil || alt.TmdbID == 0 {
			scrapeAddErr("%s: TMDB 详情失败 %v（id=%d，按标题搜索也未命中）", title, err, tmdbID)
			return
		}
		log.Printf("[影视刮削] ○ %s: 标记 id=%d 查无详情，按标题匹配到 id=%d，已自愈", title, tmdbID, alt.TmdbID)
		body, err = tc.get("/"+kindPath+"/"+strconv.Itoa(alt.TmdbID), params)
		if err != nil {
			scrapeAddErr("%s: TMDB 详情失败 %v", title, err)
			return
		}
	}

	// ---- NFO ----
	if cfg.WriteNFO {
		// 片目录下的视频文件（含 pickcode）：探测轨道写 streamdetails；
		// 剧集还逐集生成同名集级 NFO
		videoRows := scrapeDirVideoRows(key)
		var mainProbe *probeResult
		if len(videoRows) > 0 {
			if p, perr := probeFileNow(videoRows[0].PickCode); perr == "" && p != nil {
				mainProbe = p
			} else if perr != "" {
				scrapeAddErr("%s: 轨道探测失败（%s），NFO 不含 streamdetails", title, truncateStr(perr, 80))
			}
		}
		if kind == "movie" {
			var d struct {
				Title         string  `json:"title"`
				OriginalTitle string  `json:"original_title"`
				Overview      string  `json:"overview"`
				VoteAverage   float64 `json:"vote_average"`
				ReleaseDate   string  `json:"release_date"`
				Runtime       int     `json:"runtime"`
				IMDbID        string  `json:"imdb_id"`
				Genres        []struct {
					Name string `json:"name"`
				} `json:"genres"`
				Credits struct {
					Cast []struct {
						Name      string `json:"name"`
						Character string `json:"character"`
					} `json:"cast"`
					Crew []struct {
						Job  string `json:"job"`
						Name string `json:"name"`
					} `json:"crew"`
				} `json:"credits"`
				ProductionCompanies []struct {
					Name string `json:"name"`
				} `json:"production_companies"`
			}
			if json.Unmarshal(body, &d) != nil {
				scrapeAddErr("%s: 详情解析失败", title)
				return
			}
			nfo := nfoMovie{
				Title: d.Title, OriginalTitle: d.OriginalTitle, Plot: d.Overview,
				Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
				Year:    dateYear(d.ReleaseDate), Premiered: d.ReleaseDate,
				Runtime: d.Runtime,
				UniqueIDs: []nfoUniqueID{
					{Type: "tmdb", Default: true, Value: strconv.Itoa(tmdbID)},
					{Type: "imdb", Value: d.IMDbID},
				},
				TmdbID: strconv.Itoa(tmdbID), IMDbID: d.IMDbID,
			}
			for _, g := range d.Genres {
				nfo.Genres = append(nfo.Genres, g.Name)
			}
			for _, c := range d.Credits.Crew {
				if c.Job == "Director" {
					nfo.Directors = append(nfo.Directors, c.Name)
				}
			}
			for _, cc := range d.Credits.Cast {
				if cc.Name == "" {
					continue
				}
				nfo.Actors = append(nfo.Actors, nfoActor{Name: cc.Name, Role: cc.Character})
			}
			for _, pc := range d.ProductionCompanies {
				nfo.Studios = append(nfo.Studios, pc.Name)
			}
			nfo.Fileinfo = nfoFileInfoFrom(mainProbe)
			if b, err := marshalNFO(nfo); err == nil {
				if _, err := writeMetaFile(dir, "movie.nfo", b, cfg.Force); err != nil {
					scrapeAddErr("%s: 写 movie.nfo 失败 %v", title, err)
				}
			}
		} else {
			var d struct {
				Name         string  `json:"name"`
				OriginalName string  `json:"original_name"`
				Overview     string  `json:"overview"`
				VoteAverage  float64 `json:"vote_average"`
				FirstAirDate string  `json:"first_air_date"`
				Genres       []struct {
					Name string `json:"name"`
				} `json:"genres"`
				Networks []struct {
					Name string `json:"name"`
				} `json:"networks"`
				CreatedBy []struct {
					Name string `json:"name"`
				} `json:"created_by"`
			}
			if json.Unmarshal(body, &d) != nil {
				scrapeAddErr("%s: 详情解析失败", title)
				return
			}
			nfo := nfoTVShow{
				Title: d.Name, OriginalTitle: d.OriginalName, Plot: d.Overview,
				Ratings: []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: d.VoteAverage}},
				Year:    dateYear(d.FirstAirDate), Premiered: d.FirstAirDate,
				UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: strconv.Itoa(tmdbID)}},
				TmdbID:    strconv.Itoa(tmdbID),
			}
			for _, g := range d.Genres {
				nfo.Genres = append(nfo.Genres, g.Name)
			}
			for _, nw := range d.Networks {
				nfo.Studios = append(nfo.Studios, nw.Name)
			}
			for _, cb := range d.CreatedBy {
				nfo.Actors = append(nfo.Actors, nfoActor{Name: cb.Name})
			}
			if b, err := marshalNFO(nfo); err == nil {
				if _, err := writeMetaFile(dir, "tvshow.nfo", b, cfg.Force); err != nil {
					scrapeAddErr("%s: 写 tvshow.nfo 失败 %v", title, err)
				}
			}
			// 集级 NFO：每集与视频同名（xxx.mkv → xxx.mkv.nfo）落在集文件旁，
			// TMDB 集信息（标题/首播/简介/剧照）+ 该集轨道 streamdetails。
			// 解析不出集号的集文件跳过（tvshow.nfo 与海报仍正常生成）
			for _, sf := range videoRows {
				if scrapeStopRequested() {
					return
				}
				base := strings.TrimSuffix(path.Base(sf.RelPath), ".strm")
				fp := parseFileName(base)
				if fp.Episode == 0 {
					continue
				}
				season := fp.Season
				if season == 0 {
					season = 1
				}
				ep := tc.tmdbSeasonEpisodes(tmdbID, season)[fp.Episode]
				epNFO := nfoEpisode{
					Season:  season,
					Episode: fp.Episode,
					Title:   ep.Name,
					Aired:   ep.AirDate,
					Plot:    ep.Overview,
					UniqueIDs: []nfoUniqueID{
						{Type: "tmdb", Default: true, Value: strconv.Itoa(tmdbID)},
					},
				}
				if ep.VoteAverage > 0 {
					epNFO.Ratings = []nfoRating{{Name: "tmdb", Max: 10, Default: true, Value: ep.VoteAverage}}
				}
				if ep.StillPath != "" {
					epNFO.Thumb = tmdbImageBase() + "/t/p/w500" + ep.StillPath
				}
				if probe, perr := probeFileNow(sf.PickCode); perr == "" {
					epNFO.Fileinfo = nfoFileInfoFrom(probe)
				}
				if b, err := marshalNFO(epNFO); err == nil {
					epDir := filepath.Join(cfg.LocalRoot, filepath.FromSlash(path.Dir(sf.RelPath)))
					_ = os.MkdirAll(epDir, 0o755)
					if _, err := writeMetaFile(epDir, base+".nfo", b, cfg.Force); err != nil {
						scrapeAddErr("%s: 写集 NFO %s 失败 %v", title, base, err)
					}
				}
			}
		}
	}

	// ---- 图片 ----
	if !cfg.WriteImages {
		return
	}
	var d struct {
		PosterPath   string `json:"poster_path"`
		BackdropPath string `json:"backdrop_path"`
		Seasons      []struct {
			SeasonNumber int    `json:"season_number"`
			PosterPath   string `json:"poster_path"`
		} `json:"seasons"`
	}
	if json.Unmarshal(body, &d) != nil {
		return
	}
	images := [][2]string{
		{d.PosterPath, "poster.jpg"},
		{d.BackdropPath, "fanart.jpg"},
	}
	if kind == "tv" {
		for _, sn := range d.Seasons {
			if sn.PosterPath == "" {
				continue
			}
			name := fmt.Sprintf("season%02d-poster.jpg", sn.SeasonNumber)
			images = append(images, [2]string{sn.PosterPath, name})
		}
	}
	for _, img := range images {
		if scrapeStopRequested() {
			return
		}
		if img[0] == "" {
			continue
		}
		data, err := tmdbFetchImageBytes(img[0])
		if err != nil {
			scrapeAddErr("%s: 拉图失败 %s %v", title, img[1], err)
			continue
		}
		if _, err := writeMetaFile(dir, img[1], data, cfg.Force); err != nil {
			scrapeAddErr("%s: 写 %s 失败 %v", title, img[1], err)
		}
	}
}

func dateYear(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}

// scrapeDirVideoRows 台账里某片目录（key 含库名前缀）下的视频文件行，
// 取 pickcode 供轨道探测；带前缀/任意前缀两级 LIKE 兜底（与洗版查询同套路）
func scrapeDirVideoRows(key string) []model.SyncedFile {
	base := strings.Trim(key, "/")
	if base == "" {
		return nil
	}
	var sfs []model.SyncedFile
	model.DB.Where("rel_path LIKE ?", base+"/%").Limit(300).Find(&sfs)
	if len(sfs) == 0 {
		model.DB.Where("rel_path LIKE ?", "%/"+base+"/%").Limit(300).Find(&sfs)
	}
	var out []model.SyncedFile
	for _, sf := range sfs {
		if sf.PickCode == "" || !strings.HasSuffix(strings.ToLower(sf.RelPath), ".strm") {
			continue
		}
		if videoExts[strings.ToLower(pathExt(strings.TrimSuffix(path.Base(sf.RelPath), ".strm")))] {
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

var (
	scrapeSeasonMu    sync.Mutex
	scrapeSeasonCache = map[string]map[int]tmdbEpisodeInfo{}
)

// tmdbSeasonEpisodes 某季的集信息映射（集号 → 信息）；TMDB 失败返回空表
// （集标题/剧照缺失时集级 NFO 仍会生成季集号与轨道信息）
func (tc *TmdbClient) tmdbSeasonEpisodes(tvID, season int) map[int]tmdbEpisodeInfo {
	cacheKey := fmt.Sprintf("%d:%d", tvID, season)
	scrapeSeasonMu.Lock()
	if m, ok := scrapeSeasonCache[cacheKey]; ok {
		scrapeSeasonMu.Unlock()
		return m
	}
	scrapeSeasonMu.Unlock()
	m := map[int]tmdbEpisodeInfo{}
	if body, err := tc.get(fmt.Sprintf("/tv/%d/season/%d", tvID, season), map[string]string{"language": "zh-CN"}); err == nil {
		var r struct {
			Episodes []tmdbEpisodeInfo `json:"episodes"`
		}
		if json.Unmarshal(body, &r) == nil {
			for _, e := range r.Episodes {
				m[e.EpisodeNumber] = e
			}
		}
	}
	scrapeSeasonMu.Lock()
	scrapeSeasonCache[cacheKey] = m
	scrapeSeasonMu.Unlock()
	return m
}
