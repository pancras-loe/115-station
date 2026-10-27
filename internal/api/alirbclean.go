package api

// ==================== 定时清理阿里云盘回收站 ====================
//
// 2026-10-25 维护者要求，照 115 那份（rbclean.go）做：执行时间全局一个，开关 / 保留天数按账号
// （阿里云盘账号 = PanSource 里 kind=ali 的行，与「阿里云盘秒传」共用登录）。
//
// 和 115 不同的两处：
//   - 保留天数可以是 0 = 整个清空。115 那边至少 1 天，是因为本站的空目录清理、深度删除都靠 115 回收站兜底；
//     阿里云盘上本站只删自己建的秒传临时副本（直接彻底删除，不进回收站），没有东西靠它的回收站还原；
//   - 资源盘 / 备份盘各有各的回收站，两个都清（没分资源盘的老账号只有一个）。
//
// 就算整个清空，也按条目 id 彻底删除（batch /file/delete），不调 /v2/recyclebin/clear：
// 那是异步任务，预览里列出来的和它真正删的对不上。
//
// 接口（OpenList drivers/aliyundrive、aligo 的写法，只读，Go 重写）：
//   POST /v2/recyclebin/list  {drive_id, limit, marker}   回收站列表，每条带 trashed_at
//   POST /adrive/v2/batch     子请求 /file/delete {drive_id, file_id}   彻底删除
// ⚠️ 还没用真账号跑过：trashed_at 读不出的条目（字段名不对）在「保留 N 天」时一律不删，
// 预览会写「N 条读不出删除时间」，第一次跑先看预览。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

const (
	aliRbCleanKey         = "alirbclean"
	aliRbCleanDefaultCron = "40 4 * * *"
	aliRbCleanMaxDays     = 3650
)

// aliRbAcctCfg 一个阿里云盘账号的设置。KeepDays 0 = 整个清空
type aliRbAcctCfg struct {
	On       bool `json:"on"`
	KeepDays int  `json:"keep_days"`
}

// aliRbCleanCfg 配置（setting key "alirbclean"），Accts 的 key 是 PanSource.ID。新加的账号默认不清
type aliRbCleanCfg struct {
	Enabled      bool                    `json:"enabled"`
	Cron         string                  `json:"cron"`
	Accts        map[string]aliRbAcctCfg `json:"accounts"`
	LastRun      string                  `json:"last_run"`
	LastResult   string                  `json:"last_result"`
	LastResultAt string                  `json:"last_result_at"`
	LastOK       bool                    `json:"last_ok"`
}

var (
	aliRbCleanMu   sync.Mutex
	aliRbCleanBusy atomic.Bool
)

func loadAliRbCleanCfg() aliRbCleanCfg {
	cfg := aliRbCleanCfg{Cron: aliRbCleanDefaultCron}
	if v := settingValueCompat(aliRbCleanKey); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = aliRbCleanDefaultCron
	}
	if cfg.Accts == nil {
		cfg.Accts = map[string]aliRbAcctCfg{}
	}
	return cfg
}

func updateAliRbCleanCfg(fn func(*aliRbCleanCfg)) error {
	aliRbCleanMu.Lock()
	defer aliRbCleanMu.Unlock()
	cfg := loadAliRbCleanCfg()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	return notifyConfigSource.SaveSetting(aliRbCleanKey, string(b))
}

// ---- 两个清理功能共用：一个阿里云盘账号的会话 ----

// aliCleanSide 清理要用的阿里云盘接口（测试换成假的）
type aliCleanSide interface {
	drives() (resource, backup string)
	listDirIn(drive, parent string) ([]aliFile, error)
	listRecycle(drive string) ([]aliTrashItem, error)
	batchByID(sub, drive string, ids []string) (int, error)
}

func (c *aliClient) drives() (string, string) { return c.driveID, firstNonEmpty(c.backupDrive, c.driveID) }

// aliDistinctDrives 资源盘、备份盘去重（没分资源盘的账号两个是同一个）
func aliDistinctDrives(s aliCleanSide) []string {
	r, b := s.drives()
	if b == "" || b == r {
		return []string{r}
	}
	return []string{r, b}
}

// aliCleanOpener 按账号建会话（每次建都会轮换 refresh token，aliClientOf 负责写回）
type aliCleanOpener func(src model.PanSource) (aliCleanSide, error)

func (h *Handler) aliCleanOpen(src model.PanSource) (aliCleanSide, error) {
	c, err := h.aliClientOf(src)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// aliSrcLabel 界面与通知里写哪个阿里云盘账号
func aliSrcLabel(s model.PanSource) string { return firstNonEmpty(s.Name, s.Nick, fmt.Sprintf("阿里云盘 %d", s.ID)) }

// ---- 挑条目 ----

// aliRbPick 按保留天数挑出要彻底删除的条目（纯函数），按盘分组。
// keepDays 0 = 全部；否则只删 trashed_at 早于 cutoff 的，读不出时间的计进 unknown、不删
func aliRbPick(items []aliTrashItem, keepDays int, now time.Time) (byDrive map[string][]string, n int, size int64, unknown int) {
	byDrive = map[string][]string{}
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	seen := map[string]bool{}
	for _, it := range items {
		if it.FileID == "" || it.DriveID == "" {
			continue
		}
		key := it.DriveID + "/" + it.FileID
		if seen[key] {
			continue
		}
		seen[key] = true
		if keepDays > 0 {
			t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(it.TrashedAt))
			if err != nil || t.IsZero() {
				unknown++
				continue
			}
			if !t.Before(cutoff) {
				continue
			}
		}
		byDrive[it.DriveID] = append(byDrive[it.DriveID], it.FileID)
		n++
		size += it.Size
	}
	return byDrive, n, size, unknown
}

// aliRbResult 一个账号的结果
type aliRbResult struct {
	ID       uint   `json:"id"`
	Account  string `json:"account"`
	KeepDays int    `json:"keep_days"`
	Total    int    `json:"total"`
	Expired  int    `json:"expired"`
	Unknown  int    `json:"unknown"` // 读不出删除时间、没删的
	Size     int64  `json:"size"`
	Deleted  int    `json:"deleted"`
	Error    string `json:"error,omitempty"`
}

// aliRbPlan 这一次要清的一个账号
type aliRbPlan struct {
	src      model.PanSource
	keepDays int
}

// runAliRbClean 逐个账号清（dry=true 只统计不删）
func runAliRbClean(plans []aliRbPlan, open aliCleanOpener, dry bool) []aliRbResult {
	now := time.Now()
	var out []aliRbResult
	for _, p := range plans {
		res := aliRbResult{ID: p.src.ID, Account: aliSrcLabel(p.src), KeepDays: p.keepDays}
		if p.keepDays < 0 {
			res.Error = "保留天数不能是负数"
			out = append(out, res)
			continue
		}
		side, err := open(p.src)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		var items []aliTrashItem
		var listErr error
		for _, d := range aliDistinctDrives(side) {
			got, err := side.listRecycle(d)
			items = append(items, got...)
			if err != nil && listErr == nil {
				listErr = fmt.Errorf("读回收站失败: %w", err)
			}
		}
		res.Total = len(items)
		if listErr != nil && len(items) == 0 {
			res.Error = listErr.Error()
			out = append(out, res)
			continue
		}
		// 没读完也照样清读到的：每一条都按自己的删除时间判过
		byDrive, n, size, unknown := aliRbPick(items, p.keepDays, now)
		res.Expired, res.Size, res.Unknown = n, size, unknown
		if !dry {
			for drive, ids := range byDrive {
				done, err := side.batchByID("/file/delete", drive, ids)
				res.Deleted += done
				if err != nil {
					res.Error = "彻底删除失败: " + err.Error()
					break
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

// aliRbSummary 结果写成一段文字，每个账号一行
func aliRbSummary(res []aliRbResult, dry bool) (string, bool) {
	ok := true
	var lines []string
	for _, r := range res {
		head := ""
		if len(res) > 1 {
			head = "「" + r.Account + "」"
		}
		size := firstNonEmpty(humanSize(r.Size), "0 B")
		scope := fmt.Sprintf("%d 天前删除的", r.KeepDays)
		if r.KeepDays == 0 {
			scope = "全部"
		}
		switch {
		case r.Error != "" && r.Total == 0:
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
			continue
		case r.Total == 0:
			lines = append(lines, head+"回收站是空的")
		case r.Expired == 0:
			lines = append(lines, fmt.Sprintf("%s回收站 %d 条，没有%s", head, r.Total, scope))
		case dry:
			lines = append(lines, fmt.Sprintf("%s回收站 %d 条，会彻底删除%s %d 条（%s）", head, r.Total, scope, r.Expired, size))
		default:
			lines = append(lines, fmt.Sprintf("%s彻底删除%s %d / %d 条（%s），回收站原有 %d 条", head, scope, r.Deleted, r.Expired, size, r.Total))
		}
		if r.Unknown > 0 {
			lines = append(lines, fmt.Sprintf("%s○ %d 条读不出删除时间，没删", head, r.Unknown))
		}
		if r.Error != "" {
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
		}
	}
	return strings.Join(lines, "\n"), ok
}

// aliRbPlans 按配置挑出开着的账号（纯函数）
func aliRbPlans(cfg aliRbCleanCfg, srcs []model.PanSource) []aliRbPlan {
	var out []aliRbPlan
	for _, s := range srcs {
		if ac := cfg.Accts[fmt.Sprint(s.ID)]; ac.On {
			out = append(out, aliRbPlan{src: s, keepDays: ac.KeepDays})
		}
	}
	return out
}

func (h *Handler) aliRbCleanOnce(notify bool) {
	if !aliRbCleanBusy.CompareAndSwap(false, true) {
		return
	}
	defer aliRbCleanBusy.Store(false)
	plans := aliRbPlans(loadAliRbCleanCfg(), aliSources(h.DB))
	if len(plans) == 0 {
		return
	}
	res := runAliRbClean(plans, h.aliCleanOpen, false)
	msg, ok := aliRbSummary(res, false)
	flat := strings.ReplaceAll(msg, "\n", "；")
	if ok {
		plogAliRbClean.Printf("✓ %s", flat)
	} else {
		plogAliRbClean.Printf("✗ %s", flat)
	}
	_ = updateAliRbCleanCfg(func(c *aliRbCleanCfg) {
		c.LastResult, c.LastOK = msg, ok
		c.LastResultAt = time.Now().Format("2006-01-02 15:04:05")
	})
	if notify {
		deleted := 0
		for _, r := range res {
			deleted += r.Deleted
		}
		if !ok || deleted > 0 {
			title := "🗑️ 阿里云盘回收站清理"
			if !ok {
				title += "失败"
			}
			NotifyMessage(title, msg)
		}
	}
}

// ==================== 调度器 ====================

// StartAliRbCleanScheduler 分钟级 tick：cron 命中时清一遍
func StartAliRbCleanScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.aliRbCleanTick(time.Now())
		}
	}()
}

func (h *Handler) aliRbCleanTick(now time.Time) {
	cfg := loadAliRbCleanCfg()
	if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
		return
	}
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return
	}
	_ = updateAliRbCleanCfg(func(c *aliRbCleanCfg) { c.LastRun = minute })
	h.aliRbCleanOnce(true)
}

// ==================== HTTP 接口 ====================

type aliRbAcctView struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	LastErr  string `json:"last_err,omitempty"`
	On       bool   `json:"on"`
	KeepDays int    `json:"keep_days"`
}

// AliRbCleanGetConfig GET /alirbclean/config
func (h *Handler) AliRbCleanGetConfig(c *gin.Context) {
	cfg := loadAliRbCleanCfg()
	accts := []aliRbAcctView{}
	for _, s := range aliSources(h.DB) {
		ac := cfg.Accts[fmt.Sprint(s.ID)]
		accts = append(accts, aliRbAcctView{ID: s.ID, Name: aliSrcLabel(s), LastErr: s.LastErr, On: ac.On, KeepDays: ac.KeepDays})
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
		"accounts":       accts,
		"next_run":       next,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"last_ok":        cfg.LastOK,
		"running":        aliRbCleanBusy.Load(),
	})
}

type aliRbAcctReq struct {
	ID       uint `json:"id"`
	On       bool `json:"on"`
	KeepDays int  `json:"keep_days"`
}

// checkAliRbReqs 校验账号与天数（纯函数）
func checkAliRbReqs(reqs []aliRbAcctReq, srcs []model.PanSource) error {
	known := map[uint]bool{}
	for _, s := range srcs {
		known[s.ID] = true
	}
	for _, r := range reqs {
		if !known[r.ID] {
			return fmt.Errorf("阿里云盘账号 id=%d 不存在", r.ID)
		}
		if r.KeepDays < 0 || r.KeepDays > aliRbCleanMaxDays {
			return fmt.Errorf("保留天数要在 0–%d 之间（0 = 整个清空）", aliRbCleanMaxDays)
		}
	}
	return nil
}

// AliRbCleanSaveConfig POST /alirbclean/config {enabled, cron, accounts:[{id,on,keep_days}]}
func (h *Handler) AliRbCleanSaveConfig(c *gin.Context) {
	var req struct {
		Enabled  bool           `json:"enabled"`
		Cron     string         `json:"cron"`
		Accounts []aliRbAcctReq `json:"accounts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = aliRbCleanDefaultCron
	}
	if nextCronTime(cronExpr, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 40 4 * * *）"})
		return
	}
	if err := checkAliRbReqs(req.Accounts, aliSources(h.DB)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := updateAliRbCleanCfg(func(cfg *aliRbCleanCfg) {
		cfg.Enabled, cfg.Cron = req.Enabled, cronExpr
		for _, r := range req.Accounts {
			cfg.Accts[fmt.Sprint(r.ID)] = aliRbAcctCfg{On: r.On, KeepDays: r.KeepDays}
		}
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	var on []string
	for _, r := range req.Accounts {
		if r.On {
			on = append(on, fmt.Sprintf("id=%d 保留 %d 天", r.ID, r.KeepDays))
		}
	}
	plogAliRbClean.Printf("○ 保存配置：开启 %v，cron %s，%s", req.Enabled, cronExpr, firstNonEmpty(strings.Join(on, "、"), "没有开启的账号"))
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// AliRbCleanPreview POST /alirbclean/preview {accounts:[{id,keep_days}]}：只读，列出的账号不看开关
func (h *Handler) AliRbCleanPreview(c *gin.Context) {
	var req struct {
		Accounts []aliRbAcctReq `json:"accounts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Accounts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要预览的账号"})
		return
	}
	srcs := aliSources(h.DB)
	if err := checkAliRbReqs(req.Accounts, srcs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	byID := map[uint]model.PanSource{}
	for _, s := range srcs {
		byID[s.ID] = s
	}
	var plans []aliRbPlan
	for _, r := range req.Accounts {
		plans = append(plans, aliRbPlan{src: byID[r.ID], keepDays: r.KeepDays})
	}
	res := runAliRbClean(plans, h.aliCleanOpen, true)
	msg, _ := aliRbSummary(res, true)
	c.JSON(http.StatusOK, gin.H{"data": res, "message": msg})
}

// AliRbCleanRun POST /alirbclean/run：按已保存的配置后台清一遍
func (h *Handler) AliRbCleanRun(c *gin.Context) {
	if aliRbCleanBusy.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在清理，稍后再看结果"})
		return
	}
	if len(aliRbPlans(loadAliRbCleanCfg(), aliSources(h.DB))) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有开启清理的阿里云盘账号，先在配置里打开并保存"})
		return
	}
	go h.aliRbCleanOnce(false)
	c.JSON(http.StatusAccepted, gin.H{"message": "已开始清理"})
}
