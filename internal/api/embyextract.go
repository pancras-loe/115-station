package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ==================== Emby 提前探测（入库后让 Emby 把媒体信息提取好）====================
//
// STRM 第一次播放慢，一半慢在 Emby 手里还没有这个文件的轨道信息，要在 PlaybackInfo 里
// 现场 ffprobe 一遍远端文件（经本站 302 到 115 CDN，常常好几秒）。提取过一次 Emby 就存进
// 自己的库，之后再播不再探测。所以入库后替用户先调一次 PlaybackInfo，第一次播放就和第二次一样快
// （另一半慢在取直链，详情页预取已经做了，见 embyproxy.go）。
//
// 此前本站自己 ffprobe、把轨道写进 NFO 的 streamdetails —— Emby / Jellyfin 导入 NFO 不读那一段，
// 播放时照样自己探测、还把 NFO 整份重写（2026-09-29 维护者现场对照两份 NFO 确认），已删除。
//
// 做法对照：
//   - LitePan embyproxy/media_info.go「补全媒体信息」：列出缺媒体信息（视频 + 音轨不足两条）的
//     Movie / Episode / Video，逐个 POST /Items/{id}/PlaybackInfo；ISO / BDMV 跳过
//   - qmediasync controllers/emby.go（enable_extract_media_info）：收到 library.new 后对电影 / 单集调一次
//     PlaybackInfo。它的更新说明提醒过并发开多了会把 115 请求占满、别的任务全卡住 —— 所以这里一次只探一个
//
// 开关复用影视刮削的「轨道探测」（scrape.probe_streams / 刮削任务的 Probe）。两个入口：
//   - 入库确认之后（embyVerifyIngest 查到条目的那一刻）：全局开关，整理 / 增量 / 全量进来的都算
//   - 刮削任务结束时（execScrapeJob）：这一次任务的开关，对刮到的片目补探 —— 全库刮削即存量补全
// 已经有媒体信息的条目一律不碰：每次探测都是一次 115 直链请求

// embyExtractGap 两次探测之间的间隔。每次探测都会经 302 取一次 115 直链，和整理、同步共用风控额度
const embyExtractGap = 3 * time.Second

// embyExtractTimeout 一次 PlaybackInfo 最多等多久。远端 STRM 的 ffprobe 慢的时候要几十秒，
// 超时不算失败：Emby 那边多半还在提取，下次入库 / 刮削时再看
const embyExtractTimeout = 2 * time.Minute

// embyExtractQueueMax 排队上限。全库刮削一次能排进几千个片目，积压太多说明 Emby 那边出了问题，
// 超出的丢掉（下次刮削还会再排），免得内存里挂着一条永远跑不完的队列
const embyExtractQueueMax = 5000

var embyExtractQ = struct {
	mu     sync.Mutex
	queue  []string        // Emby 路径（片目目录或单个 .strm），先进先出
	queued map[string]bool // 排着的路径，去重用
	wake   chan struct{}
	once   sync.Once
}{queued: map[string]bool{}, wake: make(chan struct{}, 1)}

// embyExtractEnabled 入库确认后要不要提前探测（全局开关：影视刮削「轨道探测」）
func embyExtractEnabled() bool { return loadScrapeCfg().ProbeStreams }

// queueEmbyExtract 把一批 Emby 路径排进提前探测队列（去重），worker 第一次用到时才起
func queueEmbyExtract(embyPaths ...string) {
	q := &embyExtractQ
	q.mu.Lock()
	added, dropped := 0, 0
	for _, p := range embyPaths {
		if p == "" || q.queued[p] {
			continue
		}
		if len(q.queue) >= embyExtractQueueMax {
			dropped++
			continue
		}
		q.queued[p] = true
		q.queue = append(q.queue, p)
		added++
	}
	q.mu.Unlock()
	if dropped > 0 {
		log.Printf("[Emby探测] ⚠ 队列已满（%d），丢弃 %d 个路径 —— 下次入库 / 刮削时会再排", embyExtractQueueMax, dropped)
	}
	if added == 0 {
		return
	}
	q.once.Do(func() { go embyExtractWorker() })
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func embyExtractPop() (string, bool) {
	q := &embyExtractQ
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 {
		return "", false
	}
	p := q.queue[0]
	q.queue = q.queue[1:]
	delete(q.queued, p)
	return p, true
}

// embyExtractWorker 串行消费队列：一次只探一个条目，条目之间隔 embyExtractGap
func embyExtractWorker() {
	for {
		p, ok := embyExtractPop()
		if !ok {
			select {
			case <-stopCh:
				return
			case <-embyExtractQ.wake:
			}
			continue
		}
		if !embyExtractPath(p) {
			return
		}
	}
}

// embyExtractPath 探测一条路径下缺媒体信息的条目；返回 false 表示服务要退出了。
// 单条出错（含 panic）只丢这一条，worker 不能死：它只在第一次排队时拉起
func embyExtractPath(p string) (alive bool) {
	alive = true
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Emby探测] ✗ 处理 %s 异常: %v", p, r)
		}
	}()
	cfg, ok := loadEmbyRefreshCfg()
	if !ok {
		return
	}
	items, err := embyExtractTargets(cfg, p)
	if err != nil {
		vlog("[Emby探测] 查 %s 的条目失败: %v", p, err)
		return
	}
	for _, it := range items {
		embyExtractOne(cfg, it)
		select {
		case <-stopCh:
			return false
		case <-time.After(embyExtractGap):
		}
	}
	return
}

// embyStream PlaybackInfo / Items 返回里的一条轨道
type embyStream struct {
	Type string `json:"Type"`
}

// embyExtractItem 一个要提前探测的影视条目
type embyExtractItem struct {
	ID                string       `json:"Id"`
	Name              string       `json:"Name"`
	Type              string       `json:"Type"`
	Path              string       `json:"Path"`
	SeriesName        string       `json:"SeriesName"`
	IndexNumber       int          `json:"IndexNumber"`
	ParentIndexNumber int          `json:"ParentIndexNumber"`
	MediaStreams      []embyStream `json:"MediaStreams"`
	MediaSources      []struct {
		Path         string       `json:"Path"`
		MediaStreams []embyStream `json:"MediaStreams"`
	} `json:"MediaSources"`
}

// label 日志里怎么称呼它：剧集带剧名与季集号
func (it embyExtractItem) label() string {
	if strings.EqualFold(it.Type, "Episode") && it.SeriesName != "" {
		return fmt.Sprintf("%s S%02dE%02d", it.SeriesName, it.ParentIndexNumber, it.IndexNumber)
	}
	return it.Name
}

// embyStreamsComplete 视频 + 音轨至少两条才算提取过（LitePan 同一判据）：
// 只有字幕、或者只有一条视频流，都说明 Emby 没真正读过这个文件
func embyStreamsComplete(streams []embyStream) bool {
	n := 0
	for _, s := range streams {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "Subtitle") {
			n++
		}
	}
	return n >= 2
}

func (it embyExtractItem) hasMediaInfo() bool {
	if embyStreamsComplete(it.MediaStreams) {
		return true
	}
	for _, s := range it.MediaSources {
		if embyStreamsComplete(s.MediaStreams) {
			return true
		}
	}
	return false
}

// extractable 光盘结构（ISO / BDMV / VIDEO_TS）Emby 探测不了，探了也是白占一次 115 直链
func (it embyExtractItem) extractable() bool {
	paths := []string{it.Path}
	for _, s := range it.MediaSources {
		paths = append(paths, s.Path)
	}
	for _, p := range paths {
		p = strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
		p = strings.TrimSuffix(p, ".strm")
		if strings.HasSuffix(p, ".iso") || strings.Contains(p, "/bdmv/") || strings.Contains(p, "/video_ts/") {
			return false
		}
	}
	return true
}

// embyExtractTargets 一条 Emby 路径下还缺媒体信息的影视条目。
// 路径可能是单个 .strm（整理落盘点名回查的就是它，条目是 Episode / Movie），
// 也可能是片目目录（条目是 Folder / Series，要往下找）
func embyExtractTargets(cfg embyRefreshCfg, embyPath string) ([]embyExtractItem, error) {
	hits := embyItemsByPath(cfg, embyPath)
	if len(hits) == 0 {
		// 还没入库：刮削结束时排进来的新片目常见，入库确认那条入口会再排一次
		vlog("[Emby探测] %s 在 Emby 里还没有条目，跳过", embyPath)
		return nil, nil
	}
	hit := pickMediaHit(hits)
	q := url.Values{
		"Fields":                 {"MediaStreams,MediaSources,Path"},
		"IncludeItemTypes":       {"Movie,Episode,Video"},
		"EnableTotalRecordCount": {"false"},
	}
	switch strings.ToLower(hit.Type) {
	case "movie", "episode", "video":
		q.Set("Ids", hit.ID)
	default:
		q.Set("ParentId", hit.ID)
		q.Set("Recursive", "true")
		q.Set("Limit", "2000")
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Items []embyExtractItem `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var todo []embyExtractItem
	for _, it := range out.Items {
		if it.ID != "" && !it.hasMediaInfo() && it.extractable() {
			todo = append(todo, it)
		}
	}
	if len(todo) > 0 {
		log.Printf("[Emby探测] ▶ %s：%d 个条目还没有媒体信息，逐个提前探测", embyPathBase(embyPath), len(todo))
	}
	return todo, nil
}

func embyPathBase(p string) string {
	return filepath.Base(strings.ReplaceAll(p, "\\", "/"))
}

// embyExtractClient PlaybackInfo 专用：embyRequest 的 20 秒不够远端 STRM 探测一次
var embyExtractClient = &http.Client{Timeout: embyExtractTimeout}

// embyExtractOne POST /Items/{id}/PlaybackInfo，让 Emby 探测并存下这个条目的媒体信息。
// 直接打 Emby 本身而不是本站反代：反代会拦 PlaybackInfo 做直连改写和直链预取，这里都用不上
func embyExtractOne(cfg embyRefreshCfg, it embyExtractItem) {
	start := time.Now()
	q := url.Values{"api_key": {cfg.APIKey}}
	req, err := http.NewRequest(http.MethodPost, cfg.ServerURL+"/Items/"+url.PathEscape(it.ID)+"/PlaybackInfo?"+q.Encode(), nil)
	if err != nil {
		return
	}
	resp, err := embyExtractClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			log.Printf("[Emby探测] ○ %s：等了 %s 还没返回，Emby 可能仍在提取", it.label(), embyExtractTimeout)
			return
		}
		log.Printf("[Emby探测] ✗ %s：请求失败 %v", it.label(), err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		log.Printf("[Emby探测] ✗ %s：HTTP %d", it.label(), resp.StatusCode)
		return
	}
	var info struct {
		MediaSources []struct {
			MediaStreams []embyStream `json:"MediaStreams"`
		} `json:"MediaSources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	took := time.Since(start).Round(100 * time.Millisecond)
	var streams []embyStream
	if len(info.MediaSources) > 0 {
		streams = info.MediaSources[0].MediaStreams
	}
	if !embyStreamsComplete(streams) {
		log.Printf("[Emby探测] ○ %s：Emby 返回了，但没提取到音视频轨道（%s）—— 看 Emby 日志里这条 STRM 的 ffprobe 报错",
			it.label(), took)
		return
	}
	log.Printf("[Emby探测] ✓ %s：%s（%s）", it.label(), embyStreamsBrief(streams), took)
}

// embyStreamsBrief 视频 1 · 音轨 4 · 字幕 2
func embyStreamsBrief(streams []embyStream) string {
	n := map[string]int{}
	for _, s := range streams {
		n[strings.ToLower(s.Type)]++
	}
	out := fmt.Sprintf("视频 %d · 音轨 %d", n["video"], n["audio"])
	if n["subtitle"] > 0 {
		out += fmt.Sprintf(" · 字幕 %d", n["subtitle"])
	}
	return out
}
