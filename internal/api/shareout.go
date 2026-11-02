package api

// ==================== 转存后分享到 RE0 / 癫影 ====================
//
// 用户自己贴的 115 分享链接（链接转存页、机器人里贴链接）转存成功后，按 RE0 / 癫影卡片上的开关，
// 整理认出是哪一部之后，把这条分享链接发布到站上。设计见 docs/115-station-notes/SHARE-OUT-PLAN.md。
// 2026-11-02 起自己贴的离线链接（ed2k / 磁力，offlineSubmitManual）也发：两站都收「磁链/ED2K」分享、一条里可以放几条链接，
// 所以同一部、前后脚提交的几条合成一次发布（shareOutOfflineUnits），不按一集一条往站上铺。
//
// 几条定下来的口径：
//   - 时机在**整理认出片目之后**，不在转存成功那一刻：两站都要关联 TMDB 条目，贴链接的时候只有一个 URL，
//     转存成功也只说明 115 收下了，内容对不对还不知道。认错就挂到了别人的条目上。
//   - 只分享贴链接的。从资源站选的不分享：发回原站没意义，把一个站付费解锁的链接搬到另一个站多半违反站规。
//     订阅不是手动提交，也不分享。
//   - 一条链接整理出几部片（合集）不发：两站一次只能关联一部。
//   - **不为这个功能发任何 115 请求**：要用的东西全在整理记录里（TMDB、季集、文件名、体积），画质串按原文件名解析。
//
// 流程：提交时 shareOutMark 在来源链接上记下要发的站和访问码 → 整理按现有的来源认领把记录挂到这条链接上
// （OrganizeRecord.LinkID）→ 调度器每分钟 shareOutTick 找出已经稳定的链接，判定（shareOutJudge，纯函数）、
// 算载荷（shareOutPayloadOf，纯函数），再按站点发（shareout_re0.go / shareout_dian.go），结果一站一行记进 ShareOut。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	shareOutSiteRe0  = "re0"
	shareOutSiteDian = "dian"

	shareOutStatusDone    = "done"
	shareOutStatusSkipped = "skipped"
	shareOutStatusFailed  = "failed"

	shareOutKeepWaiting = 7 * 24 * time.Hour // 提交后最多等整理这么久，过了还没结果就不发了
	shareOutSettle      = 10 * time.Minute   // 最近一条整理记录写下后再等这么久：一条链接的几条记录可能前后脚落下
	shareOutMaxAttempts = 3
	shareOutRetryGap    = 30 * time.Minute
	shareOutPerTick     = 5                // 一分钟最多处理几次发布（每次可能要开浏览器）
	shareOutBatchGap    = 30 * time.Minute // 自己贴的离线链接前后隔不到这么久算同一批（shareOutOfflineUnits）
	shareOutBatchMax    = 500              // 一条分享最多放几条离线链接（癫影前端的上限）
)

var shareOutSites = []string{shareOutSiteRe0, shareOutSiteDian}

// shareOutSiteCfg 一个站的分享设置
type shareOutSiteCfg struct {
	Enabled bool `json:"enabled"`
	// UnlockPoints 分享出去的解锁积分；0 = 不设（RE0 免费，癫影用站点默认价）
	UnlockPoints int `json:"unlock_points"`
	// Anonymous 匿名分享（只有 RE0 支持）
	Anonymous bool `json:"anonymous,omitempty"`
}

// shareOutCfg setting key `shareout`。单独一个 key，不塞进 re0 / dian：那两个的保存接口整份覆盖，混在一起会互相冲掉
type shareOutCfg struct {
	Re0  shareOutSiteCfg `json:"re0"`
	Dian shareOutSiteCfg `json:"dian"`
}

func (c shareOutCfg) site(s string) shareOutSiteCfg {
	if s == shareOutSiteDian {
		return c.Dian
	}
	return c.Re0
}

func loadShareOutCfg() shareOutCfg {
	var cfg shareOutCfg
	if v := settingValueCompat("shareout"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg
}

func saveShareOutCfg(cfg shareOutCfg) error {
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	b, _ := json.Marshal(cfg)
	return notifyConfigSource.SaveSetting("shareout", string(b))
}

// shareOutEnabledSites 现在开着分享的站（逗号分隔，写进 DownloadLink.ShareOut）
func shareOutEnabledSites(cfg shareOutCfg) string {
	var on []string
	for _, s := range shareOutSites {
		if cfg.site(s).Enabled {
			on = append(on, s)
		}
	}
	return strings.Join(on, ",")
}

// shareOutMark 贴链接转存成功后，按当时的开关在来源链接上记下要发去哪些站。都关着就什么也不记
func (h *Handler) shareOutMark(linkID uint, code string) {
	if linkID == 0 || h == nil || h.DB == nil {
		return
	}
	sites := shareOutEnabledSites(loadShareOutCfg())
	if sites == "" {
		return
	}
	if err := h.DB.Model(&model.DownloadLink{}).Where("id = ?", linkID).
		Updates(map[string]any{"share_out": sites, "code": truncateStr(strings.TrimSpace(code), 16)}).Error; err != nil {
		log.Printf("[分享出去] ○ 记标记失败（不影响转存）: %v", err)
		return
	}
	log.Printf("[分享出去] ○ 来源链接 #%d 整理完后分享到 %s", linkID, sites)
}

// ---------- 判定（纯函数） ----------

// shareOutVerdict 一条链接现在该怎么办
type shareOutVerdict struct {
	Wait bool   // 还没到能发的时候
	Skip string // 不发了：原因
	// 能发时：这条链接认出的那一部，和挂在它下面的成功记录
	TmdbID    int
	MediaType string
	Records   []model.OrganizeRecord
}

// shareOutJudge 看这条链接的整理记录，决定等、不发、还是可以发
func shareOutJudge(link model.DownloadLink, recs []model.OrganizeRecord, now time.Time) shareOutVerdict {
	expired := now.Sub(link.CreatedAt) > shareOutKeepWaiting
	if len(recs) == 0 {
		if expired {
			return shareOutVerdict{Skip: "7 天内没整理出来"}
		}
		return shareOutVerdict{Wait: true}
	}
	var latest time.Time
	var ok []model.OrganizeRecord
	awaiting := false
	for _, r := range recs {
		if r.UpdatedAt.After(latest) {
			latest = r.UpdatedAt
		}
		switch r.Status {
		case orgStatusAwaiting:
			awaiting = true
		case "success", "exists":
			if r.TmdbID > 0 {
				ok = append(ok, r)
			}
		}
	}
	if awaiting {
		if expired {
			return shareOutVerdict{Skip: "一直在等人工确认"}
		}
		return shareOutVerdict{Wait: true}
	}
	if now.Sub(latest) < shareOutSettle && !expired {
		return shareOutVerdict{Wait: true}
	}
	if len(ok) == 0 {
		return shareOutVerdict{Skip: "整理没认出是哪一部"}
	}
	keys := map[string]bool{}
	for _, r := range ok {
		keys[r.MediaType+":"+strconv.Itoa(r.TmdbID)] = true
	}
	if len(keys) > 1 {
		return shareOutVerdict{Skip: fmt.Sprintf("一条链接里有 %d 部片（合集），站上一次只能关联一部", len(keys))}
	}
	return shareOutVerdict{TmdbID: ok[0].TmdbID, MediaType: ok[0].MediaType, Records: ok}
}

// ---------- 载荷（纯函数） ----------

// shareOutPayload 两站共用的发布内容，各站再按自己的字段转换
type shareOutPayload struct {
	URL       string // 分享链接（不带访问码）；离线链接是几条链接换行连起来
	Offline   string // 离线链接的类型 ed2k / magnet；空 = 115 分享
	ShareCode string
	Code      string // 访问码
	TmdbID    int
	MediaType string // movie / tv
	Title     string // TMDB 片名
	Year      string // 年份（整理记录上的）
	Poster    string // TMDB 海报路径（/xxx.jpg）
	ShareName string // 分享标题：分享者起的名字，没有就用片名
	SizeBytes int64
	Files     []string // 视频文件名（改名后的）
	Ext       string   // 主视频扩展名，大写（MKV）
	Info      ResourceInfo
	HasSubs   bool          // 带了字幕文件（外挂）
	SubLangs  []string      // 外挂字幕的语言（按字幕文件名认，RE0 的枚举写法：简中 / 繁中 / 简英双语…），认不出为空
	Seasons   []int         // 剧集：涉及哪几季
	Episodes  map[int][]int // 剧集：每季的集号
}

// shareOutPayloadOfLinks 一次发布的内容：115 分享就是那一条；离线链接把几条合成一条分享，
// 记录只取这几条链接的（同一次合并里查重跳过的那几条不算进集号与体积）
func shareOutPayloadOfLinks(links []model.DownloadLink, v shareOutVerdict) shareOutPayload {
	ids := map[uint]bool{}
	for _, l := range links {
		ids[l.ID] = true
	}
	recs := make([]model.OrganizeRecord, 0, len(v.Records))
	for _, r := range v.Records {
		if ids[r.LinkID] {
			recs = append(recs, r)
		}
	}
	v.Records = recs
	pl := shareOutPayloadOf(links[0], v)
	if shareOutOfflineKind(links[0].Kind) {
		pl.Offline, pl.Code = links[0].Kind, ""
		urls := make([]string, 0, len(links))
		for _, l := range links {
			urls = append(urls, strings.TrimSpace(l.URL))
		}
		pl.URL = strings.Join(urls, "\n")
		if len(links) > 1 {
			// 一条 ed2k 的名字只是其中一集的文件名
			pl.ShareName = pl.Title
		}
	}
	return pl
}

// shareOutPayloadOf 从整理记录里算出发布内容：零网络请求
func shareOutPayloadOf(link model.DownloadLink, v shareOutVerdict) shareOutPayload {
	pl := shareOutPayload{
		URL: link.URL, ShareCode: link.Hash, Code: link.Code,
		TmdbID: v.TmdbID, MediaType: v.MediaType,
		ShareName: strings.TrimSpace(link.Name),
		Episodes:  map[int][]int{},
	}
	if pl.ShareCode == "" {
		pl.ShareCode = extractShareCode(link.URL)
	}
	var origs []string
	seen := map[string]bool{}
	for _, r := range v.Records {
		if pl.Title == "" {
			pl.Title, pl.Poster, pl.Year = r.Title, r.PosterPath, r.Year
		}
		files := unmarshalRecordFiles(r.Files)
		var sum int64
		for _, f := range files {
			sum += f.Size
			switch f.Kind {
			case "subtitle":
				pl.HasSubs = true
				if l := subLangOf(firstNonEmpty(f.Orig, f.Name)); l != "" && !containsStr(pl.SubLangs, l) {
					pl.SubLangs = append(pl.SubLangs, l)
				}
			case "video":
				if seen[f.Fid] {
					continue
				}
				seen[f.Fid] = true
				pl.Files = append(pl.Files, f.Name)
				origs = append(origs, firstNonEmpty(f.Orig, f.Name))
				if v.MediaType == "tv" {
					p := parseFileName(f.Name)
					if p.Episode > 0 {
						for _, ep := range p.episodeList() {
							pl.Episodes[p.Season] = append(pl.Episodes[p.Season], ep)
						}
					}
				}
			}
		}
		// 老记录的文件可能没存体积：退回记录上的总大小
		if sum == 0 {
			sum = r.TotalSize
		}
		pl.SizeBytes += sum
	}
	if pl.ShareName == "" {
		pl.ShareName = pl.Title
	}
	if len(origs) > 0 {
		pl.Info = ParseResourceInfo(origs[0])
		pl.Ext = strings.ToUpper(strings.TrimPrefix(path.Ext(origs[0]), "."))
	}
	for s, eps := range pl.Episodes {
		pl.Episodes[s] = uniqSortedInts(eps)
		pl.Seasons = append(pl.Seasons, s)
	}
	sort.Ints(pl.Seasons)
	return pl
}

func uniqSortedInts(in []int) []int {
	sort.Ints(in)
	out := in[:0]
	for i, n := range in {
		if i == 0 || n != in[i-1] {
			out = append(out, n)
		}
	}
	return out
}

// episodesCSV 集号压成 1-12,15 这种写法（癫影要这个格式）
func episodesCSV(eps []int) string {
	eps = uniqSortedInts(append([]int(nil), eps...))
	var parts []string
	for i := 0; i < len(eps); {
		j := i
		for j+1 < len(eps) && eps[j+1] == eps[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, strconv.Itoa(eps[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", eps[i], eps[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// humanSize 58.3 GB 这种写法（RE0 的 share_size）
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	const gb, mb = 1 << 30, 1 << 20
	if n >= gb {
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	}
	return fmt.Sprintf("%.0f MB", float64(n)/mb)
}

// subLangOf 按字幕文件名认语言（xxx.chs.ass / xxx.简体.srt / xxx.chs&eng.ass），写成 RE0 的枚举。
// 只认文件名里明写的，认不出就不填：填错了比不填更误导人
func subLangOf(name string) string {
	low := strings.ToLower(name)
	has := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(low, k) {
				return true
			}
		}
		return false
	}
	sc := has("chs", ".sc.", "zh-cn", "zh-hans", "zh_cn", "简", "gb.")
	tc := has("cht", ".tc.", "zh-tw", "zh-hk", "zh-hant", "繁", "big5")
	en := has("eng", "英")
	jp := has("jpn", ".ja.", "日")
	switch {
	case sc && en:
		return "简英双语"
	case tc && en:
		return "繁英双语"
	case sc && jp:
		return "简日双语"
	case tc && jp:
		return "繁日双语"
	case sc:
		return "简中"
	case tc:
		return "繁中"
	}
	return ""
}

// shareOutHeadline 发布用的标题：「片名 (年份) S01 4K WEB-DL」。分享者自己起的名字常是第一个文件名或一串广告，
// 别人在站上一眼要看到的是哪一部、哪几季、什么画质；原名放进备注
func shareOutHeadline(pl shareOutPayload) string {
	parts := []string{pl.Title}
	if pl.Year != "" {
		parts[0] += " (" + pl.Year + ")"
	}
	if pl.MediaType == "tv" && len(pl.Seasons) > 0 {
		if len(pl.Seasons) == 1 {
			parts = append(parts, fmt.Sprintf("S%02d", pl.Seasons[0]))
		} else {
			parts = append(parts, fmt.Sprintf("S%02d-S%02d", pl.Seasons[0], pl.Seasons[len(pl.Seasons)-1]))
		}
	}
	if r := re0ResolutionOf(pl.Info.Pix); r != "" {
		parts = append(parts, r)
	}
	if pl.Info.Type != "" {
		parts = append(parts, pl.Info.Type)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// shareOutRemark 备注：片名年份、季集、视频、音频、字幕、文件，一项一行；没认出的那行不写
func shareOutRemark(pl shareOutPayload) string {
	var lines []string
	add := func(label string, vals ...string) {
		var vs []string
		for _, v := range vals {
			if v = strings.TrimSpace(v); v != "" {
				vs = append(vs, v)
			}
		}
		if len(vs) > 0 {
			lines = append(lines, label+"："+strings.Join(vs, " · "))
		}
	}
	name := pl.Title
	if pl.Year != "" {
		name += " (" + pl.Year + ")"
	}
	add("片名", name)
	if pl.MediaType == "tv" {
		var eps []string
		total := 0
		for _, s := range pl.Seasons {
			eps = append(eps, fmt.Sprintf("S%02d E%s", s, episodesCSV(pl.Episodes[s])))
			total += len(pl.Episodes[s])
		}
		if total > 0 {
			eps = append(eps, fmt.Sprintf("共 %d 集", total))
		}
		add("季集", eps...)
	}
	ri := pl.Info
	add("视频", re0ResolutionOf(ri.Pix), ri.Type, ri.Source, ri.VideoEncode, ri.Effect, ri.FPS)
	add("音频", ri.AudioEncode)
	subs := ""
	if pl.HasSubs {
		subs = "外挂"
		if len(pl.SubLangs) > 0 {
			subs += " " + strings.Join(pl.SubLangs, " / ")
		}
	}
	add("字幕", subs)
	files := ""
	if n := len(pl.Files); n > 0 {
		files = fmt.Sprintf("%d 个视频", n)
	}
	add("文件", pl.Ext, files, humanSize(pl.SizeBytes), ri.Team)
	if pl.ShareName != "" && pl.ShareName != pl.Title {
		add("原分享名", pl.ShareName)
	}
	return strings.Join(lines, "\n")
}

// shareOutBadSource 枪版：两站都不收
func shareOutBadSource(ri ResourceInfo) bool {
	switch strings.ToUpper(ri.Type) {
	case "CAM", "TS", "TC", "DVDSCR":
		return true
	}
	return false
}

// ---------- 调度 ----------

// shareOutRunning 上一轮还没跑完（发布可能要开浏览器，一分钟跑不完）就不起新的
var shareOutRunning atomic.Bool

// shareOutTick 调度器每分钟调一次：自己不阻塞，活交给后台
func (h *Handler) shareOutTick() {
	if h == nil || h.DB == nil || !shareOutRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer shareOutRunning.Store(false)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[分享出去] ✗ 处理时出错: %v", r)
			}
		}()
		h.shareOutRun(time.Now())
	}()
}

// shareOutPublisher 发一个站：返回站点那边的编号、奖励积分。errShareOutSkip 包着的是「不发了」
type shareOutPublisher func(pl shareOutPayload, cfg shareOutSiteCfg) (ref string, points int, err error)

// errShareOutSkip 不是失败，是这个站不该 / 不能发（没配置、站上已有、缺必要信息）：不重试
type errShareOutSkip struct{ why string }

func (e *errShareOutSkip) Error() string { return e.why }

func shareOutSkipf(format string, args ...any) error {
	return &errShareOutSkip{why: fmt.Sprintf(format, args...)}
}

// shareOutPublishers 各站的发布函数（变量：测试里换成假的）
var shareOutPublishers = map[string]shareOutPublisher{
	shareOutSiteRe0:  re0PublishShare,
	shareOutSiteDian: dianPublishShare,
}

func (h *Handler) shareOutRun(now time.Time) {
	var links []model.DownloadLink
	if err := h.DB.Where("share_out <> '' AND share_out IS NOT NULL AND created_at > ?", now.Add(-shareOutKeepWaiting-24*time.Hour)).
		Order("id").Limit(200).Find(&links).Error; err != nil || len(links) == 0 {
		return
	}
	ids := make([]uint, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.ID)
	}
	have := h.shareOutRows(ids)
	handled := 0
	var offline []shareOutCand
	for _, link := range links {
		if len(shareOutTodo(link, have[link.ID], now)) == 0 {
			continue
		}
		var recs []model.OrganizeRecord
		h.DB.Where("link_id = ?", link.ID).Find(&recs)
		v := shareOutJudge(link, recs, now)
		if shareOutOfflineKind(link.Kind) {
			// 离线链接常是一集一条，先攒起来按「同一部、前后脚提交」合成一次发布
			offline = append(offline, shareOutCand{Link: link, V: v})
			continue
		}
		if v.Wait {
			continue
		}
		if handled >= shareOutPerTick {
			return
		}
		handled++
		h.shareOutSend(shareOutUnit{Links: []model.DownloadLink{link}, V: v}, nil, have, now)
	}
	for _, u := range shareOutOfflineUnits(offline, now, false) {
		if handled >= shareOutPerTick {
			return
		}
		handled++
		h.shareOutSend(u, nil, have, now)
	}
}

// shareOutRows 这些链接已有的发布记账：链接 → 站 → 那一行
func (h *Handler) shareOutRows(ids []uint) map[uint]map[string]model.ShareOut {
	have := map[uint]map[string]model.ShareOut{}
	if len(ids) == 0 {
		return have
	}
	var outs []model.ShareOut
	h.DB.Where("link_id IN ?", ids).Find(&outs)
	for _, o := range outs {
		if have[o.LinkID] == nil {
			have[o.LinkID] = map[string]model.ShareOut{}
		}
		have[o.LinkID][o.Site] = o
	}
	return have
}

// shareOutTodo 这条链接还有哪些站要处理：没处理过的，和失败了、到了重试时间、次数没用完的
func shareOutTodo(link model.DownloadLink, have map[string]model.ShareOut, now time.Time) []string {
	var todo []string
	for _, s := range strings.Split(link.ShareOut, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		o, ok := have[s]
		// 用户删掉（隐藏）的失败记录不再自动重试：界面上已经没有它了，后台还在发会让人摸不着头脑
		if !ok || (o.Status == shareOutStatusFailed && o.HiddenAt == nil && o.Attempts < shareOutMaxAttempts && !now.Before(o.RetryAt)) {
			todo = append(todo, s)
		}
	}
	return todo
}

// shareOutOfflineKind 离线链接里两站收的那两种
func shareOutOfflineKind(kind string) bool {
	return kind == "ed2k" || kind == "magnet"
}

// shareOutUnit 一次发布：115 分享一条链接一次；自己贴的离线链接（一集一条 ed2k 很常见）同一部、前后脚提交的合成一次，
// 两站都允许一条分享里放多条离线链接（换行分隔）。一部剧拆成几十条分享发上去，站上就是几十条只有一集的资源
type shareOutUnit struct {
	Links []model.DownloadLink
	V     shareOutVerdict // Records 是所有链接的记录合起来
}

type shareOutCand struct {
	Link model.DownloadLink
	V    shareOutVerdict
}

// shareOutOfflineUnits 离线链接怎么合（纯函数）：按提交时间连成串，相邻两条隔不到 shareOutBatchGap 算同一批；
// 一批里有还在等整理的、或最后一条提交还不到 shareOutBatchGap（可能还在接着贴），整批先不发——
// 先认出来的那几集单独发了，后面的集就只能另起一条。批内按「链接类型 + 认出的那一部」分组，
// 判成不发的各自成一个（记跳过）。settled = 手动重试：不等，还在等的那几条不带
func shareOutOfflineUnits(cands []shareOutCand, now time.Time, settled bool) []shareOutUnit {
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Link.CreatedAt.Before(cands[j].Link.CreatedAt) })
	var units []shareOutUnit
	for i := 0; i < len(cands); {
		j := i
		for j+1 < len(cands) && cands[j+1].Link.CreatedAt.Sub(cands[j].Link.CreatedAt) <= shareOutBatchGap {
			j++
		}
		batch := cands[i : j+1]
		i = j + 1
		if !settled {
			wait := now.Sub(batch[len(batch)-1].Link.CreatedAt) < shareOutBatchGap
			for _, c := range batch {
				wait = wait || c.V.Wait
			}
			if wait {
				continue
			}
		}
		groups := map[string]int{}
		for _, c := range batch {
			if c.V.Wait {
				continue
			}
			if c.V.Skip != "" {
				units = append(units, shareOutUnit{Links: []model.DownloadLink{c.Link}, V: c.V})
				continue
			}
			key := c.Link.Kind + "|" + c.V.MediaType + ":" + strconv.Itoa(c.V.TmdbID)
			if k, ok := groups[key]; ok && len(units[k].Links) < shareOutBatchMax {
				units[k].Links = append(units[k].Links, c.Link)
				units[k].V.Records = append(units[k].V.Records, c.V.Records...)
				continue
			}
			groups[key] = len(units)
			v := c.V
			v.Records = append([]model.OrganizeRecord(nil), c.V.Records...)
			units = append(units, shareOutUnit{Links: []model.DownloadLink{c.Link}, V: v})
		}
	}
	return units
}

// shareOutSend 处理一次发布。sites 为空 = 每条链接按自己的待办（shareOutTodo），手动重试时指定那一个站。
// 记账仍是一条链接一站一行：查重、界面列表都按链接走，合在一起发的几行结果相同
func (h *Handler) shareOutSend(u shareOutUnit, sites []string, have map[uint]map[string]model.ShareOut, now time.Time) {
	cfg := loadShareOutCfg()
	todo := map[uint][]string{}
	for _, l := range u.Links {
		if sites != nil {
			todo[l.ID] = sites
		} else {
			todo[l.ID] = shareOutTodo(l, have[l.ID], now)
		}
	}
	for _, site := range shareOutSites {
		rows := map[uint]*model.ShareOut{}
		var send []model.DownloadLink
		for _, l := range u.Links {
			if !containsStr(todo[l.ID], site) {
				continue
			}
			row := have[l.ID][site]
			row.LinkID, row.Site = l.ID, site
			row.ShareCode = firstNonEmpty(l.Hash, extractShareCode(l.URL))
			row.TmdbID, row.MediaType = u.V.TmdbID, u.V.MediaType
			row.Attempts++
			switch {
			case u.V.Skip != "":
				row.Status, row.Message = shareOutStatusSkipped, u.V.Skip
			case h.shareOutFromSite(row.ShareCode):
				row.Status, row.Message = shareOutStatusSkipped, "这条链接是从 RE0 / 癫影拿到的，两站都不收重复链接"
			case h.shareOutSentBefore(site, row.ShareCode, l.ID):
				row.Status, row.Message = shareOutStatusSkipped, "这条链接之前已经发过"
			default:
				send = append(send, l)
			}
			rows[l.ID] = &row
		}
		if len(rows) == 0 {
			continue
		}
		label := shareOutSiteLabel(site)
		if len(send) > 0 {
			pl := shareOutPayloadOfLinks(send, u.V)
			status, msg, ref, points := "", "", "", 0
			var retryAt time.Time
			if pub := shareOutPublishers[site]; pub == nil {
				status, msg = shareOutStatusSkipped, "不认识的站点"
			} else {
				r, p, err := pub(pl, cfg.site(site))
				var skip *errShareOutSkip
				switch {
				case err == nil:
					status, ref, points = shareOutStatusDone, truncateStr(r, 64), p
				case errors.As(err, &skip):
					status, msg = shareOutStatusSkipped, skip.why
				default:
					status, msg, retryAt = shareOutStatusFailed, err.Error(), now.Add(shareOutRetryGap)
				}
			}
			for i, l := range send {
				row := rows[l.ID]
				row.Status, row.Message, row.Ref, row.RetryAt = status, msg, ref, retryAt
				// 奖励积分只记在第一行：几行加起来才是真得到的
				row.Points = 0
				if i == 0 {
					row.Points = points
				}
			}
			what := "「" + pl.ShareName + "」"
			if len(send) > 1 {
				what += fmt.Sprintf("（%d 条链接合成一条）", len(send))
			}
			switch status {
			case shareOutStatusDone:
				extra := ""
				if points > 0 {
					extra = fmt.Sprintf("，奖励 %d 积分", points)
				}
				log.Printf("[分享出去] ✓ %s已分享到 %s%s", what, label, extra)
			case shareOutStatusFailed:
				log.Printf("[分享出去] ✗ %s分享到 %s 失败（第 %d 次）：%s", what, label, rows[send[0].ID].Attempts, msg)
			}
		}
		for _, l := range u.Links {
			row := rows[l.ID]
			if row == nil {
				continue
			}
			row.Message = truncateStr(row.Message, 255)
			if err := h.DB.Save(row).Error; err != nil {
				log.Printf("[分享出去] ○ 记账失败: %v", err)
			}
			if row.Status == shareOutStatusSkipped {
				log.Printf("[分享出去] ○ 链接 #%d 不分享到 %s：%s", l.ID, label, row.Message)
			}
		}
	}
}

// shareOutFromSite 这个分享码以前是从 RE0 / 癫影拿到的：用户把站上解锁出来的链接复制下来又贴进「链接转存」，
// 提交入口认不出来，只能翻本地历史——找资源提交的来源链接记着站名（「RE0」，老数据「影巢」，「癫影」），
// 订阅提交的来源写的是「订阅」，站名在对应的 SubAttempt 上。两站都不收重复链接，发了也是被拒
func (h *Handler) shareOutFromSite(shareCode string) bool {
	if shareCode == "" {
		return false
	}
	var ids []uint
	h.DB.Model(&model.DownloadLink{}).Where("hash = ?", shareCode).Pluck("id", &ids)
	if len(ids) == 0 {
		return false
	}
	var n int64
	h.DB.Model(&model.DownloadLink{}).Where("id IN ? AND source IN ?", ids, []string{"RE0", "影巢", "癫影"}).Count(&n)
	if n > 0 {
		return true
	}
	h.DB.Model(&model.SubAttempt{}).Where("link_id IN ? AND source IN ?", ids, []string{"re0", "dian"}).Count(&n)
	return n > 0
}

// shareOutSentBefore 同一站、同一分享码已经发成过（另一条链接，同一个分享又贴了一次）
func (h *Handler) shareOutSentBefore(site, shareCode string, linkID uint) bool {
	if shareCode == "" {
		return false
	}
	var n int64
	h.DB.Model(&model.ShareOut{}).Where("site = ? AND share_code = ? AND status = ? AND link_id <> ?",
		site, shareCode, shareOutStatusDone, linkID).Count(&n)
	return n > 0
}

// shareOutRefusal 站点拒收（不重试）。两站都不收重复链接，提示里带「已存在 / 重复」的写成一句人话，
// 其余照站点原文
func shareOutRefusal(site, msg string) error {
	low := strings.ToLower(msg)
	for _, k := range []string{"已存在", "已经存在", "重复", "已被分享", "exist", "duplicate"} {
		if strings.Contains(low, k) {
			return shareOutSkipf("%s 上已经有这条分享", site)
		}
	}
	return shareOutSkipf("%s 拒收：%s", site, msg)
}

func shareOutSiteLabel(site string) string {
	if site == shareOutSiteDian {
		return "癫影"
	}
	return "RE0"
}

// attachShareOuts 给来源链接带上发布结果（整理记录列表用）
func attachShareOuts(db *gorm.DB, links map[uint]*model.DownloadLink) {
	ids := make([]uint, 0, len(links))
	for id, l := range links {
		if l.ShareOut != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || db == nil {
		return
	}
	var rows []model.ShareOut
	if db.Where("link_id IN ?", ids).Order("site").Find(&rows).Error != nil {
		return
	}
	for _, r := range rows {
		if l := links[r.LinkID]; l != nil {
			l.ShareOuts = append(l.ShareOuts, r)
		}
	}
}

// ---------- 接口 ----------

// ShareOutGetConfig GET /shareout/config：两站的分享设置，外加 RE0 现在走哪条通道（界面提示用）
func (h *Handler) ShareOutGetConfig(c *gin.Context) {
	rc := loadRe0Cfg()
	c.JSON(http.StatusOK, gin.H{
		"re0":             loadShareOutCfg().Re0,
		"dian":            loadShareOutCfg().Dian,
		"re0_channel":     re0Channel(rc),
		"re0_ready":       re0ShareReady(rc),
		"dian_configured": dianConfigured(loadDianCfg()),
	})
}

// ShareOutSaveConfig POST /shareout/config {site, enabled, unlock_points, anonymous}：只改这一个站，另一个站原样保留
func (h *Handler) ShareOutSaveConfig(c *gin.Context) {
	var req struct {
		Site string `json:"site"`
		shareOutSiteCfg
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Site != shareOutSiteRe0 && req.Site != shareOutSiteDian) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.UnlockPoints < 0 || req.UnlockPoints > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "解锁积分不对"})
		return
	}
	cfg := loadShareOutCfg()
	sc := req.shareOutSiteCfg
	if req.Site == shareOutSiteDian {
		sc.Anonymous = false
		cfg.Dian = sc
	} else {
		cfg.Re0 = sc
	}
	if err := saveShareOutCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	state := "关闭"
	if sc.Enabled {
		state = "开启"
	}
	log.Printf("[分享出去] ✓ %s 转存后分享已%s（解锁积分 %d）", shareOutSiteLabel(req.Site), state, sc.UnlockPoints)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}
