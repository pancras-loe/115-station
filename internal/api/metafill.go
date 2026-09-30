package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 媒体信息补全（扩展功能，kind=metafill）====================
//
// 定时把本地媒体库里「没刮全」「Emby 里还缺媒体信息」的片目找出来，交给现成的两条队列去补：
//   - 缺 NFO / 海报 / 背景图（本地文件页卡片的「未刮全」，同一套 grade 口径）→ 建一个刮削任务，
//     只补缺失、不上传网盘（传不传照常由监控上传决定）、不顺带探测；
//   - Emby 里缺媒体信息（视频 + 音轨不足两条，同 needsProbe）→ 建一个 Emby 提前探测任务，
//     **按自动规则放行**：同一条目最多 2 次、间隔 24 小时，用完不再探（§6.16 禁止重复探测）。
//     定时任务每天都跑，按手动规则（只防抖）的话，探不成的条目每天都会再取一次 115 直链。
//
// 本任务自己只扫描：读本地目录、分页读 Emby 条目，零 115 请求，不拿 taskMu，
// 挂在刮削队列上（排在它建出来的刮削任务前面，本身几秒到几十秒）。
//
// 两道闸挡住「一次全库」：
//   - 单次上限：补刮最多 MaxTitles 部、探测最多 MaxProbe 个视频（每个视频一次 115 直链）。
//     探测按 Emby 条目点名排（item:<id>），上限是准的，不会因为一部几百集的剧整部排进去而超出；
//   - 补刮记账（MetaFillMark）：补刮过、缺的还是那几样的片目 metaFillRetryAfter 内不再刮 ——
//     TMDB 上就是没有背景图、目录名认不出条目的片目，每次都刮只是白跑 TMDB。
//     探测不另记账，EmbyExtractMark 本来就管着。
// 挑选顺序：最近入库的片目在前。补过的会落账（刮削）或记了尝试（探测），下一次自然轮到后面的。

const jobKindMetaFill = "metafill"

func init() { jobExecutors[jobKindMetaFill] = execMetaFillJob }

type metaFillCfg struct {
	Enabled bool   `json:"enabled"` // 定时开关；「立即运行」不看它
	Cron    string `json:"cron"`
	Scrape  bool   `json:"scrape"` // 补 NFO / 图片
	Probe   bool   `json:"probe"`  // 缺媒体信息的让 Emby 提前探测
	// MaxTitles 单次最多补刮多少部片目
	MaxTitles int `json:"max_titles"`
	// MaxProbe 单次最多请求探测多少个视频：每个都是一次 115 直链
	MaxProbe int `json:"max_probe"`
}

const metaFillSetting = "metafill"

// metaFillRetryAfter 补刮过、缺的没变的片目多久之后再试（TMDB 可能后来补上了图）
const metaFillRetryAfter = 30 * 24 * time.Hour

const (
	metaFillMaxTitlesCap = 1000
	metaFillMaxProbeCap  = 1000
)

func defaultMetaFillCfg() metaFillCfg {
	return metaFillCfg{Enabled: false, Cron: "0 4 * * *", Scrape: true, Probe: true, MaxTitles: 50, MaxProbe: 100}
}

func normalizeMetaFillCfg(c metaFillCfg) metaFillCfg {
	if strings.TrimSpace(c.Cron) == "" {
		c.Cron = "0 4 * * *"
	}
	if c.MaxTitles <= 0 {
		c.MaxTitles = 50
	}
	c.MaxTitles = min(c.MaxTitles, metaFillMaxTitlesCap)
	if c.MaxProbe <= 0 {
		c.MaxProbe = 100
	}
	c.MaxProbe = min(c.MaxProbe, metaFillMaxProbeCap)
	return c
}

func (h *Handler) loadMetaFillCfg() metaFillCfg {
	c := defaultMetaFillCfg()
	if v := h.Config.GetSetting(metaFillSetting); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return normalizeMetaFillCfg(c)
}

// ---- 挑片目（纯函数） ----

// metaFillLackSig 片目缺什么：状态 + 缺的必需产物。记账比对用
func metaFillLackSig(t localTitle) string {
	return truncateStr(t.Status+":"+strings.Join(t.Lack, ","), 500)
}

// planMetaFillScrape 要补刮的片目：没刮全、本地目录还在，且不在「补过、缺的没变」的冷却期里。
// 最近入库的在前，最多 max 部。lacking = 没刮全的片目总数，held = 其中在冷却期的
func planMetaFillScrape(titles []localTitle, marks map[string]model.MetaFillMark, now time.Time, max int) (pick []localTitle, lacking, held int) {
	var cands []localTitle
	for _, t := range titles {
		if t.Status == "ok" || t.Missing || t.Key == "" {
			continue
		}
		lacking++
		if m, ok := marks[t.Key]; ok && m.Lack == metaFillLackSig(t) && now.Sub(m.TriedAt) < metaFillRetryAfter {
			held++
			continue
		}
		cands = append(cands, t)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if !cands[i].LastAt.Equal(cands[j].LastAt) {
			return cands[i].LastAt.After(cands[j].LastAt)
		}
		return cands[i].Key < cands[j].Key
	})
	if len(cands) > max {
		cands = cands[:max]
	}
	return cands, lacking, held
}

// metaFillProbeItem 一个缺媒体信息、自动规则下现在能探的 Emby 条目
type metaFillProbeItem struct {
	Key    string // 所属片目
	ID     string
	Label  string
	lastAt time.Time // 片目入库时间，排序用
}

// planMetaFillProbe 最近入库的片目在前、片目内按称呼排，最多 max 个
func planMetaFillProbe(items []metaFillProbeItem, max int) []metaFillProbeItem {
	out := append([]metaFillProbeItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.lastAt.Equal(b.lastAt) {
			return a.lastAt.After(b.lastAt)
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Label < b.Label
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// ---- 执行 ----

// metaFillResult 任务结果（任务详情里显示）
type metaFillResult struct {
	Titles       int      `json:"titles"`        // 看了多少部片目
	ScrapeLack   int      `json:"scrape_lack"`   // 没刮全的片目
	ScrapeHeld   int      `json:"scrape_held"`   // 其中补刮过、缺的没变，冷却期内不再刮
	ScrapeQueued int      `json:"scrape_queued"` // 这次交给刮削的
	ProbeLack    int      `json:"probe_lack"`    // Emby 里缺媒体信息的视频
	ProbeHeld    int      `json:"probe_held"`    // 其中自动次数用完 / 24 小时内请求过 / 已忽略
	ProbeQueued  int      `json:"probe_queued"`  // 这次交给探测的
	FollowJobs   []uint   `json:"follow_jobs,omitempty"`
	Problems     []string `json:"problems,omitempty"`
}

func execMetaFillJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	cfg := h.loadMetaFillCfg()
	if !cfg.Scrape && !cfg.Probe {
		return jobOutcome{}, errors.New("补刮与探测都没开，没有可做的")
	}
	root := localMediaRoot()
	if root == "" {
		return jobOutcome{}, errors.New("未配置本地媒体库根目录")
	}
	defer scrapeLane.set("", progressKeep, progressKeep, "")
	res := metaFillResult{}
	var msgs []string
	now := time.Now()

	// 列表快照强制重读：定时任务几天才跑一次，30 秒缓存无所谓，但「立即运行」前用户可能刚删过文件
	scrapeLane.set("检查本地刮削产物", 0, 0, "")
	titles := h.localTitlesSnapshot(true)
	res.Titles = len(titles)

	if cfg.Scrape {
		msgs = append(msgs, h.metaFillScrape(job, cfg, titles, now, &res))
	}
	if cfg.Probe {
		scrapeLane.set("读取 Emby 媒体信息", 0, 0, "")
		msgs = append(msgs, h.metaFillProbe(job, cfg, root, now, &res))
	}

	msg := fmt.Sprintf("看了 %d 部片目；", len(titles)) + strings.Join(msgs, "；")
	log.Printf("[媒体信息补全] ■ %s", msg)
	queued := len(res.FollowJobs) > 0
	if !queued && len(res.Problems) > 0 {
		return jobOutcome{Result: res}, errors.New(strings.Join(res.Problems, "；"))
	}
	// 定时跑了一轮什么都没建：不留行，免得任务中心每天添一条「没有要补的」
	idle := job.Priority == jobPriorityBackground && !queued && len(res.Problems) == 0
	return jobOutcome{Message: msg, Result: res, Idle: idle, Partial: len(res.Problems) > 0}, nil
}

// metaFillScrape 挑没刮全的片目建刮削任务，返回一句结果
func (h *Handler) metaFillScrape(job *model.TaskJob, cfg metaFillCfg, titles []localTitle, now time.Time, res *metaFillResult) string {
	marks := map[string]model.MetaFillMark{}
	var rows []model.MetaFillMark
	h.DB.Find(&rows)
	for _, m := range rows {
		marks[m.Key] = m
	}
	pick, lacking, held := planMetaFillScrape(titles, marks, now, cfg.MaxTitles)
	res.ScrapeLack, res.ScrapeHeld = lacking, held
	head := fmt.Sprintf("没刮全 %d 部", lacking)
	if held > 0 {
		head += fmt.Sprintf("（%d 部补刮过、缺的没变，%d 天内不再刮）", held, int(metaFillRetryAfter.Hours()/24))
	}
	if len(pick) == 0 {
		return head
	}
	sc := loadScrapeCfg()
	if !sc.WriteNFO && !sc.WriteImages {
		res.Problems = append(res.Problems, "「自动整理 → 刮削」里 NFO 与图片都关着，没法补刮")
		return head + "，刮削配置里 NFO 与图片都关着，跳过"
	}
	if _, err := loadTmdbClient(); err != nil {
		res.Problems = append(res.Problems, "补刮："+err.Error())
		return head + "，" + err.Error()
	}
	// 选项取刮削配置，但只补缺失（强制覆盖会把用户自己换过的图冲掉）、不上传、不顺带探测（探测归下面那一步，按自动规则）
	opts := sc.opts()
	opts.Force, opts.Upload, opts.Probe = false, false, false
	keys := make([]string, 0, len(pick))
	for _, t := range pick {
		keys = append(keys, t.Key)
	}
	keys = normalizeTitleKeys(keys)
	name := pick[0].Title
	if name == "" {
		name = path.Base(pick[0].Key)
	}
	title := "媒体信息补全：刮削《" + truncateStr(name, 40) + "》"
	if len(pick) > 1 {
		title += fmt.Sprintf("等 %d 部", len(pick))
	}
	sj, err := enqueueJob(h.DB, jobSpec{
		Kind: jobKindScrape, Title: title, DedupeKey: "metafill-scrape",
		Source: job.Source, Priority: job.Priority,
		Params: jobParams{Local: &localScrapeParams{Keys: keys, Scrape: opts}},
	})
	if err != nil {
		res.Problems = append(res.Problems, "刮削任务入队失败："+err.Error())
		return head + "，刮削任务入队失败"
	}
	// 入队即记账：刮完缺的还是这些，就说明这次补不上（TMDB 上没有 / 认不出条目），冷却期内不再刮
	for _, t := range pick {
		h.DB.Save(&model.MetaFillMark{Key: t.Key, Lack: metaFillLackSig(t), TriedAt: now})
	}
	res.ScrapeQueued = len(pick)
	res.FollowJobs = append(res.FollowJobs, sj.ID)
	log.Printf("[媒体信息补全] ○ %d 部没刮全的片目交给刮削任务 #%d（只补缺失）", len(pick), sj.ID)
	tail := fmt.Sprintf("，补刮 %d 部（刮削任务 #%d）", len(pick), sj.ID)
	if n := lacking - held - len(pick); n > 0 {
		tail += fmt.Sprintf("，另 %d 部超出单次上限，下次再补", n)
	}
	return head + tail
}

// metaFillProbe 挑 Emby 里缺媒体信息、自动规则下能探的条目建探测任务，返回一句结果
func (h *Handler) metaFillProbe(job *model.TaskJob, cfg metaFillCfg, root string, now time.Time, res *metaFillResult) string {
	ecfg, ok := loadEmbyRefreshCfg()
	if !ok {
		res.Problems = append(res.Problems, "没有配置 Emby，跳过探测")
		return "没有配置 Emby，跳过探测"
	}
	marks := map[string]model.EmbyExtractMark{}
	var rows []model.EmbyExtractMark
	h.DB.Find(&rows)
	for _, m := range rows {
		marks[m.ItemID] = m
	}
	ledger := scanLedgerTitlesCached()
	seen := map[string]bool{}
	var cands []metaFillProbeItem
	lack, held := 0, 0
	_, err := walkLocalEmby(ecfg, root, ledger, func(key string, it embyExtractItem) {
		if it.ID == "" || seen[it.ID] || !it.needsProbe(ecfg.PathMapping) {
			return
		}
		seen[it.ID] = true
		lack++
		m, found := marks[it.ID]
		if ok, _ := embyExtractAllowedBy(m, found, now, false); !ok {
			held++
			return
		}
		var at time.Time
		if e := ledger[key]; e != nil {
			at = e.LastAt
		}
		cands = append(cands, metaFillProbeItem{Key: key, ID: it.ID, Label: it.label(), lastAt: at})
	})
	if err != nil {
		res.Problems = append(res.Problems, "读取 Emby 失败："+err.Error())
		return "读取 Emby 失败：" + err.Error()
	}
	res.ProbeLack, res.ProbeHeld = lack, held
	head := fmt.Sprintf("Emby 里缺媒体信息 %d 个视频", lack)
	if held > 0 {
		head += fmt.Sprintf("（%d 个自动探测次数已用完、24 小时内请求过或已忽略，不再自动探）", held)
	}
	pick := planMetaFillProbe(cands, cfg.MaxProbe)
	if len(pick) == 0 {
		return head
	}
	ids := make([]string, 0, len(pick))
	for _, it := range pick {
		ids = append(ids, it.ID)
	}
	title := fmt.Sprintf("媒体信息补全：探测 %d 个视频", len(pick))
	if len(pick) == 1 {
		title = "媒体信息补全：探测 " + truncateStr(pick[0].Label, 60)
	}
	pj, err := enqueueProbeJob(h.DB, probeJobSpec{
		Title: title, Source: job.Source, DedupeKey: "metafill-probe", Items: ids,
		Auto: true, Priority: job.Priority,
	})
	if err != nil {
		res.Problems = append(res.Problems, "探测任务入队失败："+err.Error())
		return head + "，探测任务入队失败"
	}
	res.ProbeQueued = len(pick)
	res.FollowJobs = append(res.FollowJobs, pj.ID)
	log.Printf("[媒体信息补全] ○ %d 个缺媒体信息的视频交给探测任务 #%d（自动规则：每个最多 %d 次、间隔 %s）",
		len(pick), pj.ID, embyExtractMaxAttempts, embyExtractRetryAfter)
	tail := fmt.Sprintf("，探测 %d 个（探测任务 #%d）", len(pick), pj.ID)
	if n := len(cands) - len(pick); n > 0 {
		tail += fmt.Sprintf("，另 %d 个超出单次上限，下次再探", n)
	}
	return head + tail
}

// enqueueMetaFillJob 定时与手动共用一个去重键：排着一个就不再排第二个
func enqueueMetaFillJob(h *Handler, source string, priority int) (model.TaskJob, error) {
	return enqueueJob(h.DB, jobSpec{
		Kind: jobKindMetaFill, Title: "媒体信息补全", DedupeKey: "metafill",
		Source: source, Priority: priority,
	})
}

// ---- 定时 ----

var (
	metaFillLastRun string
	metaFillMu      sync.Mutex
)

func StartMetaFillScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			cfg := h.loadMetaFillCfg()
			now := time.Now()
			if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
				continue
			}
			k := now.Format("2006-01-02 15:04")
			metaFillMu.Lock()
			dup := metaFillLastRun == k
			metaFillLastRun = k
			metaFillMu.Unlock()
			if dup {
				continue
			}
			if _, err := enqueueMetaFillJob(h, "cron", jobPriorityBackground); err != nil {
				log.Printf("[媒体信息补全] ✗ 定时任务入队失败: %v", err)
			}
		}
	}()
}

// ---- HTTP ----

// MetaFillGetConfig GET /metafill/config → 配置 + 记账统计 + 上次结果
func (h *Handler) MetaFillGetConfig(c *gin.Context) {
	cfg := h.loadMetaFillCfg()
	next := ""
	if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
		next = t.Format("01-02 15:04")
	}
	var held int64
	h.DB.Model(&model.MetaFillMark{}).Where("tried_at > ?", time.Now().Add(-metaFillRetryAfter)).Count(&held)
	var last model.TaskJob
	lastJob := gin.H(nil)
	if h.DB.Where("kind = ? AND status NOT IN ?", jobKindMetaFill, []string{jobQueued, jobRunning}).
		Order("id DESC").First(&last).Error == nil {
		lastJob = gin.H{"id": last.ID, "status": last.Status, "message": last.Message, "finished_at": last.FinishedAt}
	}
	_, embyOK := loadEmbyRefreshCfg()
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"config": cfg, "next_run": next, "marks": held, "retry_days": int(metaFillRetryAfter.Hours() / 24),
		"last_job": lastJob, "emby": embyOK, "local_root": localMediaRoot() != "",
		"limits": gin.H{"max_attempts": embyExtractMaxAttempts, "retry_hours": int(embyExtractRetryAfter.Hours())},
	}})
}

// MetaFillSaveConfig POST /metafill/config
func (h *Handler) MetaFillSaveConfig(c *gin.Context) {
	var req metaFillCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效"})
		return
	}
	req = normalizeMetaFillCfg(req)
	if nextCronTime(req.Cron, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 0 4 * * *）"})
		return
	}
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting(metaFillSetting, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	log.Printf("[配置] 媒体信息补全：定时 %v（%s），补刮 %v（单次 %d 部）/ 探测 %v（单次 %d 个）",
		req.Enabled, req.Cron, req.Scrape, req.MaxTitles, req.Probe, req.MaxProbe)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// MetaFillRun POST /metafill/run → 入队，202
func (h *Handler) MetaFillRun(c *gin.Context) {
	if localMediaRoot() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置本地媒体库根目录"})
		return
	}
	job, err := enqueueMetaFillJob(h, "web", jobPriorityManual)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "媒体信息补全")
}

// MetaFillResetMarks POST /metafill/reset：清空补刮记账，下次把没刮全的片目全部重新补一遍（探测记账不动）
func (h *Handler) MetaFillResetMarks(c *gin.Context) {
	r := h.DB.Where("1 = 1").Delete(&model.MetaFillMark{})
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清空 %d 条补刮记账", r.RowsAffected)})
}
