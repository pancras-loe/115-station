package api

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 本地文件页：片目详情 ====================
//
// 卡片墙只放要紧的（海报、片名、刮削状态），细节进详情抽屉，分两块：
//   - 刮削文件（GET /local/titles/detail）：刮削会写的每一个产物在不在 —— 片目级的 NFO / 海报 / 背景图，
//     剧集按季列出季海报、season.nfo、每集的 NFO 与剧照，另带同基名的外挂字幕。只读本地，零 115 请求
//   - 媒体信息（GET /local/titles/emby）：Emby 手里每个视频的音视频 / 字幕轨道，以及提前探测的记账状态。
//     只打 Emby，不碰 115；用户切到这一页签时才请求
//
// 提前探测的记账（EmbyExtractMark）是入库确认、手动刮削「轨道探测」、详情里的「提前探测」三条入口共用的，
// 这是有意的（重复探测 = 重复取 115 直链，见 embyextract.go 文件头）。代价是用户在手动刮削里勾了轨道探测、
// 某些条目却没被探测时无从知道为什么 —— 详情逐条写清楚：探过了 / 近期请求过、几点后能再试 / 已放弃及原因。

// localFile 片目里一个刮削产物
type localFile struct {
	Name   string `json:"name"`
	Label  string `json:"label,omitempty"`
	Exists bool   `json:"exists"`
	// Optional 缺了不算没刮全：TMDB 上不一定有这张图（Logo、横版图、季海报），集剧照还可能被判成占位图不写
	Optional bool       `json:"optional,omitempty"`
	Size     int64      `json:"size,omitempty"`
	ModAt    *time.Time `json:"mod_at,omitempty"`
}

// localEntry 片目里的一个视频（台账的一行）
type localEntry struct {
	Name    string `json:"name"` // STRM 基名（不带 .strm）
	Rel     string `json:"rel"`  // 相对标题目录（含 .strm）
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"` // 剧集：0 = 解析不出集号，刮削跳过它的 NFO 与剧照
	Size    int64  `json:"size,omitempty"`    // 网盘上的视频大小（台账）
	// Orphan 最近一次全量扫描时网盘上已经没有这个文件（失效 STRM）
	Orphan bool `json:"orphan,omitempty"`
	// StrmMissing 台账有、本地 STRM 不在
	StrmMissing bool       `json:"strm_missing,omitempty"`
	NFO         localFile  `json:"nfo"`
	Thumb       *localFile `json:"thumb,omitempty"` // 只有剧集写集剧照
	Subtitles   []string   `json:"subtitles,omitempty"`
}

// localSeason 剧集的一季
type localSeason struct {
	Season int `json:"season"`
	// Dir 季目录（相对标题目录）。空 = 集文件平铺在标题目录下或一个目录混了几季，刮削不写 season.nfo
	Dir    string     `json:"dir,omitempty"`
	NFO    *localFile `json:"nfo,omitempty"`
	Poster localFile  `json:"poster"`
	Videos int        `json:"videos"`
}

// localDetailSummary 详情顶部的几个数
type localDetailSummary struct {
	NFOHave    int `json:"nfo_have"`
	NFOTotal   int `json:"nfo_total"`
	ImgHave    int `json:"img_have"`
	ImgTotal   int `json:"img_total"`
	ThumbHave  int `json:"thumb_have"`
	ThumbTotal int `json:"thumb_total"`
	// Subtitled 带外挂字幕的视频数
	Subtitled int `json:"subtitled"`
}

type localTitleDetail struct {
	localTitle
	Dir     string             `json:"dir"`
	Fanart  string             `json:"fanart,omitempty"`
	Files   []localFile        `json:"files"`
	Seasons []localSeason      `json:"seasons,omitempty"`
	Entries []localEntry       `json:"entries"`
	Summary localDetailSummary `json:"summary"`
	fanartV int64
	posterV int64
}

// localDirIndex 一个目录里的文件（小写名 → 信息），同一次详情里每个目录只读一遍
type localDirIndex map[string]map[string]os.FileInfo

func (x localDirIndex) files(dir string) map[string]os.FileInfo {
	if m, ok := x[dir]; ok {
		return m
	}
	m := map[string]os.FileInfo{}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, d := range ents {
			if d.IsDir() {
				continue
			}
			if info, err := d.Info(); err == nil {
				m[strings.ToLower(d.Name())] = info
			}
		}
	}
	x[dir] = m
	return m
}

// find 按候选名（依次）找一个非空文件；都没有时返回第一个候选名、Exists=false
func (x localDirIndex) find(dir, label string, optional bool, names ...string) localFile {
	m := x.files(dir)
	for _, n := range names {
		if info, ok := m[strings.ToLower(n)]; ok && info.Size() > 0 {
			mt := info.ModTime()
			return localFile{Name: info.Name(), Label: label, Exists: true, Optional: optional, Size: info.Size(), ModAt: &mt}
		}
	}
	return localFile{Name: names[0], Label: label, Optional: optional}
}

// findImage 同 find，标准名都没有时再认 Emby 按视频名存的图（perVideoImage，多版本电影）
func (x localDirIndex) findImage(dir, label, kind string, names ...string) localFile {
	m := x.files(dir)
	all := make([]string, 0, len(m))
	for n := range m {
		all = append(all, n)
	}
	if n := perVideoImage(all, kind); n != "" {
		names = append(append([]string{}, names...), n)
	}
	return x.find(dir, label, false, names...)
}

// inspectLocalTitleDetail 读片目目录，列出每个刮削产物在不在。rows 是台账里这个片目的视频行
func inspectLocalTitleDetail(root string, e *ledgerTitleEntry, rows []model.SyncedFile) localTitleDetail {
	it := inspectLocalTitle(root, e)
	titleAbs := filepath.Join(root, filepath.FromSlash(e.Key))
	d := localTitleDetail{localTitle: it.localTitle, Dir: titleAbs, Files: []localFile{}, Entries: []localEntry{}, posterV: it.posterV}
	idx := localDirIndex{}
	tv := e.MediaType == "tv"

	// ---- 片目级 ----
	if tv {
		d.Files = append(d.Files, idx.find(titleAbs, "剧集 NFO", false, "tvshow.nfo"))
	}
	d.Files = append(d.Files,
		idx.findImage(titleAbs, "海报", "poster", localPosterNames...),
		idx.findImage(titleAbs, "背景图", "fanart", "fanart.jpg", "fanart.png", "backdrop.jpg", "backdrop.png"),
		idx.find(titleAbs, "透明 Logo", true, "clearlogo.png", "logo.png"),
		idx.find(titleAbs, "横版图", true, "landscape.jpg"),
	)
	for _, f := range d.Files {
		if f.Label == "背景图" && f.Exists {
			d.fanartV = f.ModAt.Unix()
		}
	}

	// ---- 视频 ----
	var videos []scrapeVideo
	for _, sf := range rows {
		rel := sf.RelPath
		if i := strings.Index(rel, e.Key+"/"); i >= 0 {
			rel = rel[i+len(e.Key)+1:]
		}
		name := strings.TrimSuffix(path.Base(rel), ".strm")
		dirAbs := filepath.Join(titleAbs, filepath.FromSlash(path.Dir(rel)))
		en := localEntry{Name: name, Rel: rel, Size: sf.Size, Orphan: sf.OrphanAt != nil}
		en.StrmMissing = !d.Missing && idx.files(dirAbs)[strings.ToLower(path.Base(rel))] == nil
		if tv {
			en.NFO = idx.find(dirAbs, "NFO", false, name+".nfo")
		} else {
			// 电影的 NFO 我们写成与视频同基名，别的刮削器常写 movie.nfo：Emby 两种都认
			en.NFO = idx.find(dirAbs, "NFO", false, name+".nfo", "movie.nfo")
		}
		if tv {
			en.Season, en.Episode = scrapeEpisodeNo(name)
			if en.Episode > 0 {
				th := idx.find(dirAbs, "剧照", true, name+"-thumb.jpg")
				en.Thumb = &th
			}
		}
		prefix := strings.ToLower(name) + "."
		for n, info := range idx.files(dirAbs) {
			if strings.HasPrefix(n, prefix) && subtitleExts[strings.ToLower(path.Ext(n))] {
				en.Subtitles = append(en.Subtitles, info.Name())
			}
		}
		sort.Strings(en.Subtitles)
		d.Entries = append(d.Entries, en)
		videos = append(videos, scrapeVideo{Name: name, Dir: metaDest{Local: dirAbs}})
	}
	sort.SliceStable(d.Entries, func(i, j int) bool {
		a, b := d.Entries[i], d.Entries[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.Rel < b.Rel
	})

	// ---- 季（与刮削同一套判定：scrapeSeasonDirs 决定 season.nfo 写不写）----
	if tv {
		seasonDirs := scrapeSeasonDirs(videos, metaDest{Local: titleAbs})
		count := map[int]int{}
		for _, en := range d.Entries {
			if en.Episode > 0 {
				count[en.Season]++
			}
		}
		for s, n := range count {
			sn := localSeason{Season: s, Videos: n,
				Poster: idx.find(titleAbs, "季海报", true, fmt.Sprintf("season%02d-poster.jpg", s))}
			if dest, ok := seasonDirs[s]; ok {
				if rel, err := filepath.Rel(titleAbs, dest.Local); err == nil {
					sn.Dir = filepath.ToSlash(rel)
				}
				f := idx.find(dest.Local, "季 NFO", false, "season.nfo")
				sn.NFO = &f
			}
			d.Seasons = append(d.Seasons, sn)
		}
		sort.Slice(d.Seasons, func(i, j int) bool { return d.Seasons[i].Season < d.Seasons[j].Season })
	}

	// ---- 汇总 ----
	s := &d.Summary
	count := func(f localFile, have, total *int) {
		*total++
		if f.Exists {
			*have++
		}
	}
	for _, f := range d.Files {
		if strings.HasSuffix(strings.ToLower(f.Name), ".nfo") {
			count(f, &s.NFOHave, &s.NFOTotal)
		} else {
			count(f, &s.ImgHave, &s.ImgTotal)
		}
	}
	for _, sn := range d.Seasons {
		count(sn.Poster, &s.ImgHave, &s.ImgTotal)
		if sn.NFO != nil {
			count(*sn.NFO, &s.NFOHave, &s.NFOTotal)
		}
	}
	for _, en := range d.Entries {
		// 本地 STRM 不在的视频刮削也写不进去（scrapeCompensate 会收回），同样不计入
		if en.StrmMissing {
			continue
		}
		// 解析不出集号的集刮削不写 NFO，不计入分母，免得永远差几个
		if !tv || en.Episode > 0 {
			count(en.NFO, &s.NFOHave, &s.NFOTotal)
		}
		if en.Thumb != nil {
			count(*en.Thumb, &s.ThumbHave, &s.ThumbTotal)
		}
		if len(en.Subtitles) > 0 {
			s.Subtitled++
		}
	}
	d.grade()
	return d
}

// grade 按详情里的产物定卡片状态：必需产物（片目级 NFO、海报、背景图、季 NFO、每集 NFO）缺一样就是 partial，
// 一样都没有是 miss。可选的图（剧照、季海报）只记进 Soft 提示 —— TMDB 上不一定有，占位剧照还会被故意跳过，
// 算进状态的话有些剧重刮多少遍都是「不完整」。Logo / 横版图更是常缺，只在详情里列
func (d *localTitleDetail) grade() {
	tv := d.MediaType == "tv"
	have := 0
	var lack, soft []string
	for _, f := range d.Files {
		switch {
		case f.Optional:
		case f.Exists:
			have++
		default:
			lack = append(lack, f.Label)
		}
	}
	seasonNFO, seasonPoster := 0, 0
	for _, sn := range d.Seasons {
		if sn.NFO != nil {
			if sn.NFO.Exists {
				have++
			} else {
				seasonNFO++
			}
		}
		if !sn.Poster.Exists {
			seasonPoster++
		}
	}
	nfo, nfoTotal, thumb := 0, 0, 0
	for _, en := range d.Entries {
		if en.StrmMissing || (tv && en.Episode == 0) {
			continue
		}
		nfoTotal++
		if en.NFO.Exists {
			have++
		} else {
			nfo++
		}
		if en.Thumb != nil && !en.Thumb.Exists {
			thumb++
		}
	}
	switch {
	case !tv && nfoTotal == 0:
		// 台账行不全（老数据没有 pickcode）时没有逐个视频可比，退回「标题目录里有任何 NFO」
		if d.HasNFO {
			have++
		} else {
			lack = append(lack, "NFO")
		}
	case nfo == 0:
	case tv:
		lack = append(lack, fmt.Sprintf("%d 集 NFO", nfo))
	case nfoTotal == 1:
		lack = append(lack, "NFO")
	default:
		lack = append(lack, fmt.Sprintf("%d 个 NFO", nfo))
	}
	switch {
	case seasonNFO == 1 && len(d.Seasons) == 1:
		lack = append(lack, "季 NFO")
	case seasonNFO > 0:
		lack = append(lack, fmt.Sprintf("%d 季 NFO", seasonNFO))
	}
	if thumb > 0 {
		soft = append(soft, fmt.Sprintf("%d 集剧照", thumb))
	}
	if seasonPoster > 0 {
		soft = append(soft, fmt.Sprintf("%d 张季海报", seasonPoster))
	}
	d.Lack, d.Soft = lack, soft
	switch {
	case d.Missing || have == 0:
		d.Status = "miss"
	case len(lack) == 0:
		d.Status = "ok"
	default:
		d.Status = "partial"
	}
}

// localDetailEntry 按 key 取台账片目；找不到时已经写好了响应
func localDetailEntry(c *gin.Context, key string) (*ledgerTitleEntry, string, bool) {
	root := localMediaRoot()
	if root == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置本地媒体库根目录"})
		return nil, "", false
	}
	key = strings.Trim(strings.TrimSpace(key), "/")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少片目"})
		return nil, "", false
	}
	e := scanLedgerTitlesCached()[key]
	if e == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "台账里没有这个片目（可能刚被移走或删除），请刷新列表"})
		return nil, "", false
	}
	return e, root, true
}

// LocalTitleDetail GET /local/titles/detail?key=
func (h *Handler) LocalTitleDetail(c *gin.Context) {
	e, root, ok := localDetailEntry(c, c.Query("key"))
	if !ok {
		return
	}
	d := inspectLocalTitleDetail(root, e, scrapeDirVideoRows(e.Key))
	if d.HasPoster {
		for _, f := range d.Files {
			if f.Label == "海报" && f.Exists {
				d.Poster = h.localPosterQuery(e.Key, f.ModAt.Unix())
			}
		}
	}
	if d.fanartV > 0 {
		d.Fanart = h.localFanartQuery(e.Key, d.fanartV)
	}
	c.JSON(http.StatusOK, d)
}

// ---- 媒体信息（Emby）----

// embyProbeState 一个条目的提前探测状态
type embyProbeState struct {
	// State done 已有媒体信息 / none 没探测过 / queued 排队中 / running 探测中 /
	// retry 上次失败、已过间隔，自动入口还会再试一次 / wait 自动入口冷却中、要等到 RetryAt /
	// exhausted 自动入口次数用完、不再自动探测 / disc 光盘结构不探测
	State    string     `json:"state"`
	Attempts int        `json:"attempts,omitempty"`
	LastAt   *time.Time `json:"last_at,omitempty"`
	LastErr  string     `json:"last_err,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"` // 自动入口最早什么时候再试（只有 wait 有）
	// ManualAt 手动防抖还没过：这个时间之后才能手动请求。为空 = 现在就能手动请求（done / disc / 排队中的除外）
	ManualAt *time.Time `json:"manual_at,omitempty"`
	// Ignored 在任务中心的失败清单里被忽略了：State 按 exhausted 报（同样不再自动探），界面据此换一句说明
	Ignored bool `json:"ignored,omitempty"`
}

// embyProbeStateOf 纯函数：与 embyExtractAllowed / worker 同一套判定，只是把「为什么不探」说出来。
// queue 是它所在路径在队列里按什么规则排着（probeQueued*）：同样排着，自动规则下冷却中的不会真探
func embyProbeStateOf(hasInfo, extractable bool, mark *model.EmbyExtractMark, queue string, running bool, now time.Time) embyProbeState {
	switch {
	case hasInfo:
		return embyProbeState{State: "done"}
	case !extractable:
		return embyProbeState{State: "disc"}
	}
	st := embyProbeState{State: "none"}
	if mark != nil {
		last := mark.LastAt
		st.Attempts, st.LastAt, st.LastErr = mark.Attempts, &last, mark.LastErr
		if at := mark.LastAt.Add(embyExtractDebounce); now.Before(at) {
			st.ManualAt = &at
		}
	}
	switch {
	case running:
		st.State = "running"
	case mark == nil:
		if queue != probeQueuedNone {
			st.State = "queued"
		}
	case queue == probeQueuedManual && st.ManualAt == nil:
		st.State = "queued"
	case mark.IgnoredAt != nil:
		st.State, st.Ignored = "exhausted", true
	case mark.Attempts >= embyExtractMaxAttempts:
		st.State = "exhausted"
	case now.Sub(mark.LastAt) < embyExtractRetryAfter:
		st.State = "wait"
		at := mark.LastAt.Add(embyExtractRetryAfter)
		st.RetryAt = &at
	case queue == probeQueuedAuto:
		st.State = "queued"
	default:
		st.State = "retry"
	}
	return st
}

// manualOK 手动请求现在能不能探它（与 embyExtractAllowed 的手动分支同口径）
func (st embyProbeState) manualOK() bool {
	switch st.State {
	case "done", "disc", "queued", "running":
		return false
	}
	return st.ManualAt == nil
}

// embyTrack 一条轨道（界面用）
type embyTrack struct {
	Codec     string  `json:"codec,omitempty"`
	Profile   string  `json:"profile,omitempty"`
	Language  string  `json:"language,omitempty"`
	LangName  string  `json:"lang_name,omitempty"`
	Display   string  `json:"display,omitempty"`
	Title     string  `json:"title,omitempty"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	BitRate   int64   `json:"bitrate,omitempty"`
	BitDepth  int     `json:"bit_depth,omitempty"`
	FrameRate float64 `json:"fps,omitempty"`
	Range     string  `json:"range,omitempty"` // SDR / HDR / HDR10 / DolbyVision …
	Channels  int     `json:"channels,omitempty"`
	Layout    string  `json:"layout,omitempty"`
	Default   bool    `json:"default,omitempty"`
	Forced    bool    `json:"forced,omitempty"`
	External  bool    `json:"external,omitempty"`
}

type embyDetailItem struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Season    int            `json:"season,omitempty"`
	Episode   int            `json:"episode,omitempty"`
	Rel       string         `json:"rel,omitempty"` // 相对标题目录；映射不回本地时为空
	Container string         `json:"container,omitempty"`
	Size      int64          `json:"size,omitempty"`
	Bitrate   int64          `json:"bitrate,omitempty"`
	Runtime   int64          `json:"runtime,omitempty"` // 秒
	Video     []embyTrack    `json:"video"`
	Audio     []embyTrack    `json:"audio"`
	Subtitles []embyTrack    `json:"subtitles"`
	HasInfo   bool           `json:"has_info"`
	Probe     embyProbeState `json:"probe"`
	disc      bool           // 缺媒体信息但是光盘结构（ISO / BDMV），探不了：按版本算
}

func toEmbyTrack(s embyStream) embyTrack {
	rng := s.ExtendedVideoType
	if rng == "" || strings.EqualFold(rng, "None") {
		rng = s.VideoRange
	}
	return embyTrack{
		Codec: s.Codec, Profile: s.Profile, Language: s.Language, LangName: s.DisplayLanguage,
		Display: s.DisplayTitle, Title: s.Title, Width: s.Width, Height: s.Height, BitRate: s.BitRate,
		BitDepth: s.BitDepth, FrameRate: s.AverageFrameRate, Range: rng, Channels: s.Channels,
		Layout: s.ChannelLayout, Default: s.IsDefault, Forced: s.IsForced, External: s.IsExternal,
	}
}

// embyDetailOf Emby 条目 → 界面条目。轨道优先取 MediaSources 里的（PlaybackInfo 探测后写的就是它）
func embyDetailOf(it embyExtractItem, titleLocal, pathMapping string) embyDetailItem {
	out := embyDetailItem{ID: it.ID, Name: it.Name, Type: it.Type, Runtime: it.RunTimeTicks / 10_000_000,
		Video: []embyTrack{}, Audio: []embyTrack{}, Subtitles: []embyTrack{}, HasInfo: it.hasMediaInfo()}
	if strings.EqualFold(it.Type, "Episode") {
		out.Season, out.Episode = it.ParentIndexNumber, it.IndexNumber
	}
	if it.Path != "" && titleLocal != "" {
		local := embyPathToLocal(pathMapping, it.Path)
		base := strings.TrimRight(filepath.ToSlash(titleLocal), "/") + "/"
		if strings.HasPrefix(local, base) {
			out.Rel = strings.TrimPrefix(local, base)
		}
	}
	streams := it.MediaStreams
	for _, s := range it.MediaSources {
		out.Container, out.Size, out.Bitrate = s.Container, s.Size, s.Bitrate
		if len(s.MediaStreams) > 0 {
			streams = s.MediaStreams
		}
		break
	}
	out.setTracks(streams)
	return out
}

// embyDetailsOf 多版本条目（同目录两个 .strm，Emby 合成一个条目）拆成每个版本一行：
// 只显示第一个版本的话，另一个版本缺不缺媒体信息界面上看不出来。
// 探测记账是按条目记的，各行共用；有没有媒体信息按版本各算各的
func embyDetailsOf(it embyExtractItem, titleLocal, pathMapping string) []embyDetailItem {
	if len(it.MediaSources) <= 1 {
		d := embyDetailOf(it, titleLocal, pathMapping)
		d.disc = !d.HasInfo && !it.needsProbe(pathMapping)
		return []embyDetailItem{d}
	}
	lacking := map[string]bool{}
	for _, s := range it.lackingSources() {
		lacking[embySourceKey(s)] = true
	}
	var out []embyDetailItem
	for i, s := range it.MediaSources {
		v := it
		v.Path = s.Path
		v.MediaSources = nil
		d := embyDetailOf(v, titleLocal, pathMapping)
		if i > 0 {
			d.ID = fmt.Sprintf("%s#%d", it.ID, i) // 界面拿 id 当列表 key
		}
		if s.Path != "" {
			d.Name = strings.TrimSuffix(embyPathBase(s.Path), ".strm")
		}
		d.Container, d.Size, d.Bitrate = s.Container, s.Size, s.Bitrate
		d.HasInfo = !lacking[embySourceKey(s)]
		d.disc = !d.HasInfo && it.discSource(s, pathMapping)
		streams := s.MediaStreams
		if len(streams) == 0 && s.Path == it.Path {
			streams = it.MediaStreams
		}
		d.setTracks(streams)
		out = append(out, d)
	}
	return out
}

func (out *embyDetailItem) setTracks(streams []embyStream) {
	out.Video, out.Audio, out.Subtitles = []embyTrack{}, []embyTrack{}, []embyTrack{}
	for _, s := range streams {
		switch strings.ToLower(s.Type) {
		case "video":
			out.Video = append(out.Video, toEmbyTrack(s))
		case "audio":
			out.Audio = append(out.Audio, toEmbyTrack(s))
		case "subtitle":
			out.Subtitles = append(out.Subtitles, toEmbyTrack(s))
		}
	}
}

// embyTitleLookup 在 Emby 里找片目的条目。先按标题目录找（剧集的 Series、电影所在目录的 Folder），
// 找不到再逐个按 .strm 找（有的 Emby 版本电影目录本身不是条目）。
// paths 是真正找到条目的路径：提前探测排队就排它们
func embyTitleLookup(cfg embyRefreshCfg, titleLocal string, entries []localEntry) (paths []string, items []embyExtractItem, err error) {
	dirPath := embyPathOf(cfg, titleLocal)
	found, items, err := embyMediaItemsAt(cfg, dirPath)
	if err != nil || found {
		return []string{dirPath}, items, err
	}
	seen := map[string]bool{}
	for i, en := range entries {
		if i >= 5 {
			break // 剧集的目录都查不到，逐集查也是白查
		}
		p := embyPathOf(cfg, filepath.Join(titleLocal, filepath.FromSlash(en.Rel)))
		ok, got, err := embyMediaItemsAt(cfg, p)
		if err != nil {
			return paths, items, err
		}
		if !ok {
			continue
		}
		paths = append(paths, p)
		for _, it := range got {
			if !seen[it.ID] {
				seen[it.ID] = true
				items = append(items, it)
			}
		}
	}
	return paths, items, nil
}

func embyMarksOf(items []embyExtractItem) map[string]*model.EmbyExtractMark {
	out := map[string]*model.EmbyExtractMark{}
	if model.DB == nil || len(items) == 0 {
		return out
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	var marks []model.EmbyExtractMark
	model.DB.Where("item_id IN ?", ids).Find(&marks)
	for i := range marks {
		out[marks[i].ItemID] = &marks[i]
	}
	return out
}

// embyTitleProbe 片目在 Emby 里的条目与探测状态（媒体信息页签与「提前探测」按钮共用）
type embyTitleProbe struct {
	paths  []string
	items  []embyDetailItem
	counts map[string]int
	// manual 现在点「提前探测」会真正请求的条目数；debounce 还缺媒体信息、但刚请求过（防抖中）的条目数，
	// manualAt 是其中最早能再手动请求的时间
	manual, debounce int
	manualAt         *time.Time
}

func embyTitleProbeOf(cfg embyRefreshCfg, root string, e *ledgerTitleEntry) (embyTitleProbe, error) {
	titleLocal := filepath.Join(root, filepath.FromSlash(e.Key))
	entries := inspectLocalTitleDetail(root, e, scrapeDirVideoRows(e.Key)).Entries
	paths, raw, err := embyTitleLookup(cfg, titleLocal, entries)
	out := embyTitleProbe{paths: paths, items: []embyDetailItem{}, counts: map[string]int{}}
	if err != nil {
		return out, err
	}
	marks := embyMarksOf(raw)
	queued := probeQueuedNone
	for _, p := range paths {
		if m := embyExtractQueuedFor(p); m == probeQueuedManual || (m == probeQueuedAuto && queued == probeQueuedNone) {
			queued = m
		}
	}
	running, now := embyExtractRunningID(), time.Now()
	for _, it := range raw {
		for _, d := range embyDetailsOf(it, titleLocal, cfg.PathMapping) {
			d.Probe = embyProbeStateOf(d.HasInfo, !d.disc, marks[it.ID], queued, running != "" && running == it.ID, now)
			out.counts[d.Probe.State]++
			switch {
			case d.Probe.manualOK():
				out.manual++
			case d.Probe.ManualAt != nil && d.Probe.State != "queued" && d.Probe.State != "running":
				out.debounce++
				if out.manualAt == nil || d.Probe.ManualAt.Before(*out.manualAt) {
					out.manualAt = d.Probe.ManualAt
				}
			}
			out.items = append(out.items, d)
		}
	}
	sort.SliceStable(out.items, func(i, j int) bool {
		a, b := out.items[i], out.items[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.Rel < b.Rel
	})
	return out, nil
}

// embyProbeLimits 记账规则（界面照着解释「为什么没探」）
func embyProbeLimits() gin.H {
	return gin.H{
		"max_attempts": embyExtractMaxAttempts, "retry_hours": int(embyExtractRetryAfter.Hours()),
		"break_after": embyExtractBreakAfter, "break_minutes": int(embyExtractBreakPause.Minutes()),
		"debounce_minutes": int(embyExtractDebounce.Minutes()), "gap_seconds": int(embyExtractGap.Seconds()),
	}
}

// LocalTitleEmby GET /local/titles/emby?key=
func (h *Handler) LocalTitleEmby(c *gin.Context) {
	e, root, ok := localDetailEntry(c, c.Query("key"))
	if !ok {
		return
	}
	cfg, configured := loadEmbyRefreshCfg()
	resp := gin.H{"configured": configured, "auto_probe": embyExtractEnabled(), "limits": embyProbeLimits(),
		"found": false, "items": []embyDetailItem{}, "counts": gin.H{}}
	if !configured {
		c.JSON(http.StatusOK, resp)
		return
	}
	p, err := embyTitleProbeOf(cfg, root, e)
	if err != nil {
		resp["error"] = "查询 Emby 失败：" + err.Error()
		c.JSON(http.StatusOK, resp)
		return
	}
	resp["found"] = len(p.paths) > 0
	resp["items"], resp["counts"] = p.items, p.counts
	resp["manual"], resp["debounce"] = p.manual, p.debounce
	if p.manualAt != nil {
		resp["manual_at"] = p.manualAt
	}
	if id := activeProbeJobFor(c.Query("key")); id != 0 {
		resp["job_id"] = id
	}
	c.JSON(http.StatusOK, resp)
}

// LocalTitleProbe POST /local/titles/probe {key, confirm}：给这个片目里还没有媒体信息的条目建一个
// 「Emby 提前探测」任务（手动入口：不受自动入口的次数与 24 小时限制，只有防抖，见 embyextract.go）。
// 任务中心看得到进度、能停、失败能重试
func (h *Handler) LocalTitleProbe(c *gin.Context) {
	var req struct {
		Key     string `json:"key"`
		Confirm bool   `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	e, root, ok := localDetailEntry(c, req.Key)
	if !ok {
		return
	}
	cfg, configured := loadEmbyRefreshCfg()
	if !configured {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有配置 Emby"})
		return
	}
	p, err := embyTitleProbeOf(cfg, root, e)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询 Emby 失败：" + err.Error()})
		return
	}
	if len(p.paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Emby 里还没有这个片目，等它扫描入库后再探测"})
		return
	}
	if p.manual == 0 {
		msg := "全部视频都已有媒体信息，不需要探测"
		switch {
		case p.counts["queued"]+p.counts["running"] > 0:
			msg = "这个片目正在探测，进度在任务中心"
		case p.debounce > 0 && p.manualAt != nil:
			msg = fmt.Sprintf("还缺媒体信息的 %d 个视频刚请求过探测，%s 之后才能再请求（防抖 %d 分钟，避免重复取 115 直链）",
				p.debounce, p.manualAt.Format("15:04:05"), int(embyExtractDebounce.Minutes()))
		case p.counts["disc"] > 0 && p.counts["done"]+p.counts["disc"] == len(p.items):
			msg = "剩下的是光盘结构（ISO / BDMV），Emby 探测不了"
		}
		c.JSON(http.StatusOK, gin.H{"queued": 0, "held": p.debounce, "message": msg})
		return
	}
	if p.manual > localProbeConfirmVideos && !req.Confirm {
		c.JSON(http.StatusConflict, gin.H{"need_confirm": true, "videos": p.manual,
			"error": fmt.Sprintf("要探测 %d 个条目，需要先确认", p.manual)})
		return
	}
	job, err := enqueueProbeJob(h.DB, probeJobSpec{
		Title: fmt.Sprintf("Emby 提前探测《%s》", truncateStr(e.Title, 60)), Source: "web",
		DedupeKey: "probe:" + req.Key, Key: req.Key, Paths: p.paths,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	msg := fmt.Sprintf("已创建探测任务：%d 个视频，后台一次一个（间隔 %s），进度在任务中心", p.manual, embyExtractGap)
	if p.debounce > 0 {
		msg += fmt.Sprintf("；另有 %d 个刚请求过，这次跳过", p.debounce)
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": p.manual, "held": p.debounce, "message": msg, "job_id": job.ID})
}
