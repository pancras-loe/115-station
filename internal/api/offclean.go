package api

// ==================== 定时清理 115 离线任务 ====================
//
// 2026-10-25 维护者要求：115 离线大多秒传，但有些磁力会「下载失败」或卡在百分之几一直不动，
// 一直挂在 115 的云下载列表里。订阅提交的离线只等 30 秒秒传、等不到当场删任务（subrun.go），
// 但删任务也会失败（只打一行日志），网页 / 机器人提交的离线从来不删，所以另做一个定时清理。
//
// 规则：
//   - 下载失败的（status -1）删；
//   - 排队 / 下载中（status 0 / 1）且添加超过 N 小时的删（N = 0 不删卡住的；添加时间读不出来的不删）；
//   - 下载完成的不动（完成的记录不碍事，删了监视器与来源链接回填就对不上了）；
//   - 默认只动本站提交的任务：任务的 info_hash / 原始链接对得上来源链接（DownloadLink）或离线归属标记，
//     用户在 115 App 里自己加的不碰。可以改成全部。
// 删任务一律 flag=0：只删任务、不删已经下进网盘的文件（下了一半的 115 不会放进网盘目录）。
// 删掉的任务若是某个订阅还挂着的离线尝试，顺手判失败（与离线监视器看到失败同口径），不重试同一条链接。
//
// 接口（p115client offline_list / offline_remove）：
//   POST 115.com/web/lixian/?ac=task_lists  page=N，返回 page_count 与 tasks（info_hash / status / percentDone / add_time / url）
//   POST 115.com/web/lixian/?ac=task_del    hash[0]=…&hash[1]=…&flag=0

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

const (
	offCleanKey         = "offclean"
	offCleanDefaultCron = "0 * * * *"
	offCleanDefaultHrs  = 24
	offCleanMaxHrs      = 24 * 90
	// offCleanMaxPages 一个账号最多读几页（115 一页约 30 条，≈ 6000 条），防止接口一直回同一页
	offCleanMaxPages = 200
	// offCleanBatch 一次删几个任务
	offCleanBatch = 20
	// offCleanShow 预览 / 结果里每个账号最多列几条
	offCleanShow = 30
)

// offCleanCfg 配置（setting key "offclean"）。规则全局一套，开关按账号（Storage 行 id，主号也是），新加的账号默认不清
type offCleanCfg struct {
	Enabled      bool            `json:"enabled"`
	Cron         string          `json:"cron"`
	Failed       bool            `json:"failed"`      // 删下载失败的
	StuckHours   int             `json:"stuck_hours"` // 添加超过几小时还没下完的删；0 = 不删
	All          bool            `json:"all"`         // true = 115 App 里自己加的也删；默认只动本站提交的
	Accts        map[string]bool `json:"accounts"`
	LastRun      string          `json:"last_run"`
	LastResult   string          `json:"last_result"`
	LastResultAt string          `json:"last_result_at"`
	LastOK       bool            `json:"last_ok"`
}

var (
	offCleanMu   sync.Mutex
	offCleanBusy atomic.Bool
)

func loadOffCleanCfg() offCleanCfg {
	cfg := offCleanCfg{Cron: offCleanDefaultCron, Failed: true, StuckHours: offCleanDefaultHrs}
	if v := settingValueCompat(offCleanKey); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = offCleanDefaultCron
	}
	if cfg.Accts == nil {
		cfg.Accts = map[string]bool{}
	}
	return cfg
}

func updateOffCleanCfg(fn func(*offCleanCfg)) error {
	offCleanMu.Lock()
	defer offCleanMu.Unlock()
	cfg := loadOffCleanCfg()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	return notifyConfigSource.SaveSetting(offCleanKey, string(b))
}

// offCleanRule 一次清理用的规则
type offCleanRule struct {
	Failed     bool
	StuckHours int
	All        bool
}

func (c offCleanCfg) rule() offCleanRule {
	return offCleanRule{Failed: c.Failed, StuckHours: c.StuckHours, All: c.All}
}

// offCleanHit 一个要删的任务
type offCleanHit struct {
	Name    string `json:"name"`
	Hash    string `json:"-"`
	Status  int    `json:"status"` // -1 失败 / 0 排队 / 1 下载中
	Percent int    `json:"percent"`
	Hours   int    `json:"hours"` // 添加了几小时
	Size    int64  `json:"size"`
	Mine    bool   `json:"mine"` // 本站提交的
	LinkID  uint   `json:"-"`
}

// pickOffCleanTasks 挑出要删的任务（纯函数）。mine 返回任务是不是本站提交的、对应的来源链接 id（没有为 0）
func pickOffCleanTasks(tasks []offlineTaskInfo, rule offCleanRule, now time.Time, mine func(offlineTaskInfo) (bool, uint)) []offCleanHit {
	var out []offCleanHit
	seen := map[string]bool{}
	for _, t := range tasks {
		if t.hash == "" || seen[t.hash] {
			continue // 没有 info_hash 删不了
		}
		hours := -1
		if t.addTime > 0 {
			hours = int(now.Sub(time.Unix(t.addTime, 0)).Hours())
		}
		switch {
		case t.status == -1:
			if !rule.Failed {
				continue
			}
		case t.status == 0 || t.status == 1:
			if rule.StuckHours <= 0 || hours < 0 || hours < rule.StuckHours {
				continue
			}
		default:
			continue // 完成的、认不出的状态不动
		}
		isMine, linkID := mine(t)
		if !isMine && !rule.All {
			continue
		}
		seen[t.hash] = true
		out = append(out, offCleanHit{Name: t.name, Hash: t.hash, Status: t.status, Percent: t.percent,
			Hours: max(hours, 0), Size: t.size, Mine: isMine, LinkID: linkID})
	}
	return out
}

// listAllOfflineTasks 翻完一个账号的离线任务列表
func listAllOfflineTasks(cookie string) ([]offlineTaskInfo, error) {
	var all []offlineTaskInfo
	for page := 1; page <= offCleanMaxPages; page++ {
		raws, pages, err := fetchLixianTasksPage(cookie, page)
		if err != nil {
			if len(all) > 0 {
				return all, err
			}
			return nil, err
		}
		all = append(all, parseOfflineTasks(raws)...)
		if len(raws) == 0 || pages <= page {
			return all, nil
		}
	}
	return all, fmt.Errorf("离线任务超过 %d 页，没读完", offCleanMaxPages)
}

// offCleanMine 认本站提交的任务：任务的 info_hash / 原始链接按来源链接表与离线归属标记对。
// 一次查完这一批，不逐个查库
func (h *Handler) offCleanMine(tasks []offlineTaskInfo) func(offlineTaskInfo) (bool, uint) {
	var hashes, urls []string
	for _, t := range tasks {
		if t.hash != "" {
			hashes = append(hashes, strings.ToUpper(t.hash))
		}
		if t.url != "" {
			urls = append(urls, t.url)
			if lh := linkHashOf(t.url); lh != "" {
				hashes = append(hashes, lh)
			}
		}
	}
	byHash, byURL := map[string]uint{}, map[string]uint{}
	if h.DB != nil && (len(hashes) > 0 || len(urls) > 0) {
		var rows []model.DownloadLink
		// 分批查，SQLite 一条语句的参数有上限；补一个空串免得 IN () 语法错（空串不会匹配到有效行）
		for i := 0; i < len(hashes) || i < len(urls); i += 400 {
			hs := append([]string{""}, hashes[min(i, len(hashes)):min(i+400, len(hashes))]...)
			us := append([]string{""}, urls[min(i, len(urls)):min(i+400, len(urls))]...)
			var part []model.DownloadLink
			h.DB.Select("id, hash, url").Where("kind <> ? AND (hash IN ? OR url IN ?)", "share", hs, us).Find(&part)
			rows = append(rows, part...)
		}
		for _, r := range rows {
			if r.Hash != "" {
				byHash[strings.ToUpper(r.Hash)] = r.ID
			}
			if r.URL != "" {
				byURL[r.URL] = r.ID
			}
		}
	}
	marks := offlineMineLoad(h)
	return func(t offlineTaskInfo) (bool, uint) {
		if id, ok := byHash[strings.ToUpper(t.hash)]; ok && t.hash != "" {
			return true, id
		}
		if id, ok := byURL[t.url]; ok && t.url != "" {
			return true, id
		}
		if lh := linkHashOf(t.url); lh != "" {
			if id, ok := byHash[lh]; ok {
				return true, id
			}
		}
		return offlineMineMatch(h, marks, t.hash, t.name), 0
	}
}

// offCleanAcctResult 一个账号的结果
type offCleanAcctResult struct {
	ID      uint          `json:"id"`
	Account string        `json:"account"`
	Total   int           `json:"total"`   // 离线列表一共几条
	Matched int           `json:"matched"` // 符合规则的几条
	Deleted int           `json:"deleted"` // 实际删掉几条（预览为 0）
	Tasks   []offCleanHit `json:"tasks"`   // 符合规则的（最多 offCleanShow 条）
	Error   string        `json:"error,omitempty"`
}

// runOffClean 逐个账号清（dry=true 只列出不删）
func (h *Handler) runOffClean(accts []rbAcct, rule offCleanRule, dry bool) []offCleanAcctResult {
	now := time.Now()
	var out []offCleanAcctResult
	for _, a := range accts {
		res := offCleanAcctResult{ID: a.id, Account: a.label, Tasks: []offCleanHit{}}
		switch {
		case a.err != nil:
			res.Error = a.err.Error()
		case a.cookie == "":
			res.Error = "没登录 115"
		}
		if res.Error != "" {
			out = append(out, res)
			continue
		}
		tasks, listErr := listAllOfflineTasks(a.cookie)
		res.Total = len(tasks)
		if listErr != nil && len(tasks) == 0 {
			res.Error = "读离线任务失败: " + listErr.Error()
			out = append(out, res)
			continue
		}
		hits := pickOffCleanTasks(tasks, rule, now, h.offCleanMine(tasks))
		// 失败的排前面，再按添加时间从早到晚
		sort.SliceStable(hits, func(i, j int) bool {
			if (hits[i].Status == -1) != (hits[j].Status == -1) {
				return hits[i].Status == -1
			}
			return hits[i].Hours > hits[j].Hours
		})
		res.Matched = len(hits)
		// 一条都没挑中时 hits 是 nil，切出来还是 nil，会盖掉上面的 []、序列化成 null（前端 .length 报错）
		res.Tasks = append([]offCleanHit{}, hits[:min(len(hits), offCleanShow)]...)
		if !dry {
			for i := 0; i < len(hits); i += offCleanBatch {
				part := hits[i:min(i+offCleanBatch, len(hits))]
				hs := make([]string, len(part))
				for k, t := range part {
					hs[k] = t.Hash
				}
				if err := offlineDeleteTasks(a.cookie, hs); err != nil {
					res.Error = err.Error()
					break
				}
				res.Deleted += len(part)
				for _, t := range part {
					if t.LinkID == 0 {
						continue
					}
					why := "115 离线任务失败，已删掉任务：" + truncateStr(t.Name, 200)
					if t.Status != -1 {
						why = fmt.Sprintf("115 离线任务 %d 小时没下完（%d%%），已删掉任务：%s", t.Hours, t.Percent, truncateStr(t.Name, 200))
					}
					subMarkOfflineReason(h.DB, t.LinkID, why)
				}
			}
		}
		if listErr != nil && res.Error == "" {
			res.Error = listErr.Error()
		}
		out = append(out, res)
	}
	return out
}

// offCleanSummary 结果写成一段文字（通知与界面共用）
func offCleanSummary(res []offCleanAcctResult, dry bool) (string, bool) {
	ok := true
	var lines []string
	for _, r := range res {
		head := ""
		if len(res) > 1 {
			head = "「" + r.Account + "」"
		}
		failed, stuck := 0, 0
		for _, t := range r.Tasks {
			if t.Status == -1 {
				failed++
			} else {
				stuck++
			}
		}
		detail := ""
		if r.Matched > 0 && r.Matched == len(r.Tasks) {
			detail = fmt.Sprintf("（失败 %d、卡住 %d）", failed, stuck)
		}
		switch {
		case r.Error != "" && r.Total == 0:
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
			continue
		case r.Matched == 0:
			lines = append(lines, fmt.Sprintf("%s离线列表 %d 条，没有要清理的", head, r.Total))
		case dry:
			lines = append(lines, fmt.Sprintf("%s离线列表 %d 条，其中 %d 条要删%s", head, r.Total, r.Matched, detail))
		default:
			lines = append(lines, fmt.Sprintf("%s删掉 %d / %d 个离线任务%s，列表原有 %d 条", head, r.Deleted, r.Matched, detail, r.Total))
		}
		if r.Error != "" {
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
		}
	}
	return strings.Join(lines, "\n"), ok
}

// offCleanAccounts 配置里开着的账号
func (h *Handler) offCleanAccounts(cfg offCleanCfg) []rbAcct {
	var out []rbAcct
	for _, a := range h.rbCleanAccounts() {
		if cfg.Accts[fmt.Sprint(a.id)] {
			out = append(out, a)
		}
	}
	return out
}

// offCleanOnce 按已保存的配置清一次并记结果；notify=true 推通知（定时触发）
func (h *Handler) offCleanOnce(notify bool) {
	if !offCleanBusy.CompareAndSwap(false, true) {
		return
	}
	defer offCleanBusy.Store(false)
	cfg := loadOffCleanCfg()
	accts := h.offCleanAccounts(cfg)
	if len(accts) == 0 || (!cfg.Failed && cfg.StuckHours <= 0) {
		return
	}
	res := h.runOffClean(accts, cfg.rule(), false)
	msg, ok := offCleanSummary(res, false)
	flat := strings.ReplaceAll(msg, "\n", "；")
	if ok {
		log.Printf("[离线清理] ✓ %s", flat)
	} else {
		log.Printf("[离线清理] ✗ %s", flat)
	}
	_ = updateOffCleanCfg(func(c *offCleanCfg) {
		c.LastResult, c.LastOK = msg, ok
		c.LastResultAt = time.Now().Format("2006-01-02 15:04:05")
	})
	if notify {
		deleted := 0
		for _, r := range res {
			deleted += r.Deleted
		}
		if !ok || deleted > 0 {
			title := "🧹 115 离线任务清理"
			if !ok {
				title += "失败"
			}
			NotifyMessage(title, msg)
		}
	}
}

// StartOffCleanScheduler 分钟级 tick：cron 命中时清一遍
func StartOffCleanScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.offCleanTick(time.Now())
		}
	}()
}

func (h *Handler) offCleanTick(now time.Time) {
	cfg := loadOffCleanCfg()
	if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
		return
	}
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return
	}
	_ = updateOffCleanCfg(func(c *offCleanCfg) { c.LastRun = minute })
	h.offCleanOnce(true)
}

// ==================== HTTP 接口 ====================

type offCleanAcctView struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Main     bool   `json:"main"`
	LoggedIn bool   `json:"logged_in"`
	On       bool   `json:"on"`
}

// OffCleanGetConfig GET /offclean/config
func (h *Handler) OffCleanGetConfig(c *gin.Context) {
	cfg := loadOffCleanCfg()
	accts := []offCleanAcctView{}
	for _, a := range h.rbCleanAccounts() {
		accts = append(accts, offCleanAcctView{ID: a.id, Name: a.label, Main: a.main,
			LoggedIn: a.err == nil && a.cookie != "", On: cfg.Accts[fmt.Sprint(a.id)]})
	}
	next := ""
	if cfg.Enabled {
		if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
			next = t.Format("01-02 15:04")
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"cron":           cfg.Cron,
		"failed":         cfg.Failed,
		"stuck_hours":    cfg.StuckHours,
		"all":            cfg.All,
		"accounts":       accts,
		"next_run":       next,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"last_ok":        cfg.LastOK,
		"running":        offCleanBusy.Load(),
	})
}

// offCleanReq 保存 / 预览共用的请求体
type offCleanReq struct {
	Enabled    bool   `json:"enabled"`
	Cron       string `json:"cron"`
	Failed     bool   `json:"failed"`
	StuckHours int    `json:"stuck_hours"`
	All        bool   `json:"all"`
	Accounts   []uint `json:"accounts"` // 开着的账号 id
}

// checkOffCleanReq 校验规则与账号（纯函数）
func checkOffCleanReq(req offCleanReq, accts []rbAcct) error {
	if req.StuckHours < 0 || req.StuckHours > offCleanMaxHrs {
		return fmt.Errorf("卡住多久算要删：0–%d 小时", offCleanMaxHrs)
	}
	known := map[uint]bool{}
	for _, a := range accts {
		known[a.id] = true
	}
	for _, id := range req.Accounts {
		if !known[id] {
			return fmt.Errorf("账号 id=%d 不存在", id)
		}
	}
	return nil
}

// OffCleanSaveConfig POST /offclean/config
func (h *Handler) OffCleanSaveConfig(c *gin.Context) {
	var req offCleanReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = offCleanDefaultCron
	}
	if nextCronTime(cronExpr, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 0 * * * *）"})
		return
	}
	if err := checkOffCleanReq(req, h.rbCleanAccounts()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := updateOffCleanCfg(func(cfg *offCleanCfg) {
		cfg.Enabled, cfg.Cron = req.Enabled, cronExpr
		cfg.Failed, cfg.StuckHours, cfg.All = req.Failed, req.StuckHours, req.All
		cfg.Accts = map[string]bool{}
		for _, id := range req.Accounts {
			cfg.Accts[fmt.Sprint(id)] = true
		}
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	log.Printf("[配置] 离线任务清理：%v，cron %s，失败 %v、卡住 %d 小时、全部 %v，账号 %v", req.Enabled, cronExpr, req.Failed, req.StuckHours, req.All, req.Accounts)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// OffCleanPreview POST /offclean/preview：只读，按没保存的规则列出会删哪些。
// 列出来的账号不看开关都算（打开之前先看一眼）
func (h *Handler) OffCleanPreview(c *gin.Context) {
	var req offCleanReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Accounts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要预览的账号"})
		return
	}
	all := h.rbCleanAccounts()
	if err := checkOffCleanReq(req, all); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	want := map[uint]bool{}
	for _, id := range req.Accounts {
		want[id] = true
	}
	var accts []rbAcct
	for _, a := range all {
		if want[a.id] {
			accts = append(accts, a)
		}
	}
	res := h.runOffClean(accts, offCleanRule{Failed: req.Failed, StuckHours: req.StuckHours, All: req.All}, true)
	msg, _ := offCleanSummary(res, true)
	c.JSON(http.StatusOK, gin.H{"data": res, "message": msg})
}

// OffCleanRun POST /offclean/run：按已保存的配置后台清一遍
func (h *Handler) OffCleanRun(c *gin.Context) {
	if offCleanBusy.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在清理，稍后再看结果"})
		return
	}
	cfg := loadOffCleanCfg()
	if len(h.offCleanAccounts(cfg)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有开启清理的账号，先在配置里打开并保存"})
		return
	}
	if !cfg.Failed && cfg.StuckHours <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "「下载失败的」和「卡住的」都关着，没有要清理的"})
		return
	}
	go h.offCleanOnce(false)
	c.JSON(http.StatusAccepted, gin.H{"message": "已开始清理"})
}
