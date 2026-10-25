package api

// ==================== 定时清理 115 回收站（只删 N 天前删除的） ====================
//
// 2026-10-25 维护者要求：保留最近 N 天进回收站的内容，更早的彻底删除。
//
// 参考项目的做法：
//   - p115strmhelper（helper/clean）只有「整个清空」：webapi /rb/secret_del 不带 tid + 安全密钥，
//     定时一到全清，没有保留天数；
//   - p115client 的 batch_recyclebin_clean：按回收站条目 id 分批（每批 1000 个）调同一个接口；
//   - LitePan 115_Open 的永久删除：先读回收站列表按 dtime 挑出条目，再带 tid 删。
// 这里取后两者：读列表 → 按 dtime 挑出早于 N 天的 → 带 tid 分批删。
//
// ⚠️ 本站很多删除都靠「进回收站可以还原」兜底（空目录清理、深度删除、洗版让位，§6.9 / §6.10），
// 增量同步认被删目录的位置也读回收站（recyclebin115.go），所以：
//   - **永远不发不带 tid 的请求**：/rb/secret_del 不带 tid 就是整个清空；
//   - 保留天数至少 1 天；删除时间读不出来的条目不删；
//   - 只走 Cookie 通道：每个账号的回收站各清各的（主号 + 附加账号），OpenAPI 只授权了主号。
//
// 接口（p115client recyclebin_clean / recyclebin_list）：
//   GET  webapi.115.com/rb?aid=7&cid=0&offset&limit   回收站列表，每条带 id / dtime / file_size
//   POST webapi.115.com/rb/secret_del  tid=逗号分隔 & password=6 位安全密钥
//        账号没开「文件(隐藏模式/清空删除回收站)」的安全密钥时密钥随便填，p115client 默认 000000

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

const (
	rbCleanKey         = "rbclean"
	rbCleanDefaultCron = "30 4 * * *"
	rbCleanDefaultDays = 30
	rbCleanMaxDays     = 3650
	// rbCleanPage 一页读多少条。没实测过 115 一页最多给几条，按返回的条数翻页，不假定它给满
	rbCleanPage = 1000
	// rbCleanMaxPages 一个账号最多读几页（≈ 20 万条），防止接口一直回同一页把调度器卡死
	rbCleanMaxPages = 200
	// rbCleanBatch 一次删多少条（p115client batch_recyclebin_clean 默认 1000）
	rbCleanBatch = 1000
)

// rbAcctCfg 一个账号的清理设置
type rbAcctCfg struct {
	On       bool   `json:"on"`
	KeepDays int    `json:"keep_days"` // 保留最近几天删除的
	Password string `json:"password"`  // 这个账号的 115 安全密钥（6 位数字），没开安全密钥可不填
}

// rbCleanCfg 配置（setting key "rbclean"）。
// 执行时间全局一个（几个账号一起跑、一条通知汇总），开关 / 保留天数 / 安全密钥每个账号一份：
// 115 的安全密钥本来就是按账号设的。按 Storage 行 id 存（主号也是），新加的账号默认不清
type rbCleanCfg struct {
	Enabled      bool                 `json:"enabled"` // 定时开关（全局）
	Cron         string               `json:"cron"`
	Accts        map[string]rbAcctCfg `json:"accounts"` // key = Storage 行 id
	LastRun      string               `json:"last_run"` // 最近一次触发的分钟（去重）
	LastResult   string               `json:"last_result"`
	LastResultAt string               `json:"last_result_at"`
	LastOK       bool                 `json:"last_ok"`
}

var (
	rbCleanMu   sync.Mutex
	rbCleanBusy atomic.Bool // 同一时间只跑一次清理（定时与手动共用）
)

func loadRbCleanCfg() rbCleanCfg {
	cfg := rbCleanCfg{Cron: rbCleanDefaultCron}
	if v := settingValueCompat(rbCleanKey); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = rbCleanDefaultCron
	}
	if cfg.Accts == nil {
		cfg.Accts = map[string]rbAcctCfg{}
	}
	return cfg
}

// acctOf 某个账号的设置；没设过的默认不清、保留 30 天
func (c rbCleanCfg) acctOf(id uint) rbAcctCfg {
	a := c.Accts[fmt.Sprint(id)]
	if a.KeepDays < 1 {
		a.KeepDays = rbCleanDefaultDays
	}
	return a
}

// updateRbCleanCfg 读改写一次配置（调度器写 last_*、界面写开关，别互相覆盖）
func updateRbCleanCfg(fn func(*rbCleanCfg)) error {
	rbCleanMu.Lock()
	defer rbCleanMu.Unlock()
	cfg := loadRbCleanCfg()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	return notifyConfigSource.SaveSetting(rbCleanKey, string(b))
}

// rbItem 回收站里的一条（只取清理要用的字段）
type rbItem struct {
	id    string
	dtime int64
	size  int64
}

// fetch115RecycleBinPage 读回收站一页，同时返回 115 报的总条数（没给时为 -1）
func fetch115RecycleBinPage(cookie string, offset, limit int) ([]rbItem, int, error) {
	query := url.Values{
		"aid":    {"7"},
		"cid":    {"0"},
		"offset": {fmt.Sprint(offset)},
		"limit":  {fmt.Sprint(limit)},
		"format": {"json"},
	}
	body, err := httpGet115UA("https://webapi.115.com/rb", query, cookie, ua115Unified(), 30*time.Second)
	if err != nil {
		return nil, -1, err
	}
	return parseRecycleBinPage(body)
}

func parseRecycleBinPage(body []byte) ([]rbItem, int, error) {
	var r struct {
		State bool            `json:"state"`
		Error string          `json:"error"`
		Count json.RawMessage `json:"count"`
		Data  []struct {
			ID       json.RawMessage `json:"id"`
			Dtime    json.RawMessage `json:"dtime"`
			FileSize json.RawMessage `json:"file_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, -1, fmt.Errorf("解析回收站列表失败: %s", truncateStr(string(body), 200))
	}
	if !r.State {
		return nil, -1, fmt.Errorf("115 拒绝回收站列表请求: %s", r.Error)
	}
	count := -1
	if s := rawStr(r.Count); s != "" {
		fmt.Sscan(s, &count)
	}
	out := make([]rbItem, 0, len(r.Data))
	for _, e := range r.Data {
		var it rbItem
		it.id = rawStr(e.ID)
		fmt.Sscan(rawStr(e.Dtime), &it.dtime)
		fmt.Sscan(rawStr(e.FileSize), &it.size)
		out = append(out, it)
	}
	return out, count, nil
}

// rbDigits 回收站条目 id 只认纯数字：tid 是逗号拼起来的，混进别的字符可能被 115 当成别的意思
var rbDigits = regexp.MustCompile(`^[0-9]+$`)

// pickExpiredRb 挑出删除时间早于 cutoff 的条目（纯函数）。
// 不依赖列表的排序：按删除时间倒序没实测保证过，逐条判。删除时间读不出来（0）的不删；id 去重
func pickExpiredRb(items []rbItem, cutoff int64) (ids []string, size int64) {
	seen := map[string]bool{}
	for _, it := range items {
		if it.dtime <= 0 || it.dtime >= cutoff || !rbDigits.MatchString(it.id) || seen[it.id] {
			continue
		}
		seen[it.id] = true
		ids = append(ids, it.id)
		size += it.size
	}
	return ids, size
}

// listAllRecycleBin 翻完一个账号的回收站
func listAllRecycleBin(cookie string) ([]rbItem, error) {
	var all []rbItem
	offset := 0
	for page := 0; page < rbCleanMaxPages; page++ {
		items, count, err := fetch115RecycleBinPage(cookie, offset, rbCleanPage)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		offset += len(items)
		if len(items) == 0 || (count >= 0 && offset >= count) {
			return all, nil
		}
	}
	return all, fmt.Errorf("回收站超过 %d 页，没读完", rbCleanMaxPages)
}

// delete115RecycleBin 彻底删除回收站里的这些条目。ids 为空直接返回：
// 不带 tid 的请求是「整个清空」，这里绝不能发出去
func delete115RecycleBin(cookie string, ids []string, password string) error {
	if len(ids) == 0 {
		return nil
	}
	pw := strings.TrimSpace(password)
	if pw == "" {
		pw = "000000"
	}
	body, err := httpPostForm115("https://webapi.115.com/rb/secret_del", url.Values{
		"tid":      {strings.Join(ids, ",")},
		"password": {pw},
	}, cookie, 60*time.Second)
	if err != nil {
		return err
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Errno any    `json:"errno"`
	}
	if json.Unmarshal(body, &r) != nil {
		return fmt.Errorf("响应解析失败: %s", truncateStr(string(body), 200))
	}
	if !r.State {
		msg := firstNonEmpty(r.Error, fmt.Sprint(r.Errno))
		if strings.Contains(msg, "密") {
			msg += "（115「账号安全 → 安全密钥」开着清空回收站验证时要填对 6 位安全密钥）"
		}
		return fmt.Errorf("115 拒绝删除: %s", msg)
	}
	return nil
}

// rbAcct 一个 115 账号（主号 + 附加账号；没登录的也列出来，界面要显示）
type rbAcct struct {
	id     uint // Storage 行 id
	main   bool
	label  string
	cookie string
	err    error // 拿不到 Cookie 的原因
}

func (h *Handler) rbCleanAccounts() []rbAcct {
	m, err := mainStorage115(h.DB)
	if err != nil {
		return nil
	}
	ck, err := h.get115Cookie()
	if err == nil && ck == "" {
		err = fmt.Errorf("主号没有 Cookie 登录（只授权了 OpenAPI）")
	}
	out := []rbAcct{{id: m.ID, main: true, label: firstNonEmpty(m.UserName, m.Name, "主号"), cookie: ck, err: err}}
	var rows []model.Storage
	h.DB.Where("type = ? AND main = ? AND id <> ?", "115", false, m.ID).Order("id").Find(&rows)
	for _, r := range rows {
		ck, err := h.cookieOf(r.ID)
		out = append(out, rbAcct{id: r.ID, label: acctLabel(r), cookie: ck, err: err})
	}
	return out
}

// rbPlan 这一次要清的一个账号
type rbPlan struct {
	acct     rbAcct
	keepDays int
	password string
}

// rbCleanPlans 按配置挑出开着的账号（纯函数）
func rbCleanPlans(cfg rbCleanCfg, accts []rbAcct) []rbPlan {
	var out []rbPlan
	for _, a := range accts {
		if ac := cfg.acctOf(a.id); ac.On {
			out = append(out, rbPlan{acct: a, keepDays: ac.KeepDays, password: ac.Password})
		}
	}
	return out
}

// rbAcctResult 一个账号的结果
type rbAcctResult struct {
	ID       uint   `json:"id"`
	Account  string `json:"account"`
	KeepDays int    `json:"keep_days"`
	Total    int    `json:"total"`   // 回收站一共几条
	Expired  int    `json:"expired"` // 早于保留期的几条
	Size     int64  `json:"size"`    // 早于保留期的总大小
	Deleted  int    `json:"deleted"` // 实际删掉几条（预览为 0）
	Error    string `json:"error,omitempty"`
}

// runRbClean 按计划逐个账号清（dry=true 只统计不删）
func runRbClean(plans []rbPlan, dry bool) []rbAcctResult {
	now := time.Now()
	var out []rbAcctResult
	for _, p := range plans {
		a := p.acct
		res := rbAcctResult{ID: a.id, Account: a.label, KeepDays: p.keepDays}
		switch {
		case p.keepDays < 1:
			res.Error = "保留天数至少 1 天"
		case a.err != nil:
			res.Error = a.err.Error()
		case a.cookie == "":
			res.Error = "没登录 115"
		}
		if res.Error != "" {
			out = append(out, res)
			continue
		}
		items, err := listAllRecycleBin(a.cookie)
		res.Total = len(items)
		if err != nil && len(items) == 0 {
			res.Error = "读回收站失败: " + err.Error()
			out = append(out, res)
			continue
		}
		// 没读完也照样清已经读到的：读到的每一条都按自己的删除时间判过
		listErr := err
		cutoff := now.Add(-time.Duration(p.keepDays) * 24 * time.Hour).Unix()
		ids, size := pickExpiredRb(items, cutoff)
		res.Expired, res.Size = len(ids), size
		if !dry {
			for i := 0; i < len(ids); i += rbCleanBatch {
				end := min(i+rbCleanBatch, len(ids))
				if err := delete115RecycleBin(a.cookie, ids[i:end], p.password); err != nil {
					res.Error = err.Error()
					break
				}
				res.Deleted = end
			}
		}
		if listErr != nil && res.Error == "" {
			res.Error = listErr.Error()
		}
		out = append(out, res)
	}
	return out
}

// rbCleanSummary 结果写成一段文字（通知与界面共用），每个账号一行
func rbCleanSummary(res []rbAcctResult, dry bool) (string, bool) {
	ok := true
	var lines []string
	for _, r := range res {
		head := ""
		if len(res) > 1 {
			head = "「" + r.Account + "」"
		}
		size := firstNonEmpty(humanSize(r.Size), "0 B")
		switch {
		case r.Error != "" && r.Total == 0:
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
			continue
		case r.Expired == 0:
			lines = append(lines, fmt.Sprintf("%s回收站 %d 条，没有 %d 天前删除的", head, r.Total, r.KeepDays))
		case dry:
			lines = append(lines, fmt.Sprintf("%s回收站 %d 条，其中 %d 条是 %d 天前删除的（%s）", head, r.Total, r.Expired, r.KeepDays, size))
		default:
			lines = append(lines, fmt.Sprintf("%s彻底删除 %d / %d 条 %d 天前删除的（%s），回收站原有 %d 条", head, r.Deleted, r.Expired, r.KeepDays, size, r.Total))
		}
		if r.Error != "" {
			ok = false
			lines = append(lines, head+"✗ "+r.Error)
		}
	}
	return strings.Join(lines, "\n"), ok
}

// rbCleanOnce 按已保存的配置清一次并记结果；notify=true 推通知（定时触发）
func (h *Handler) rbCleanOnce(notify bool) {
	if !rbCleanBusy.CompareAndSwap(false, true) {
		return
	}
	defer rbCleanBusy.Store(false)
	plans := rbCleanPlans(loadRbCleanCfg(), h.rbCleanAccounts())
	if len(plans) == 0 {
		return // 定时开着、账号一个都没开：什么都不做，也不记结果
	}
	res := runRbClean(plans, false)
	msg, ok := rbCleanSummary(res, false)
	flat := strings.ReplaceAll(msg, "\n", "；")
	if ok {
		log.Printf("[回收站清理] ✓ %s", flat)
	} else {
		log.Printf("[回收站清理] ✗ %s", flat)
	}
	_ = updateRbCleanCfg(func(c *rbCleanCfg) {
		c.LastResult, c.LastOK = msg, ok
		c.LastResultAt = time.Now().Format("2006-01-02 15:04:05")
	})
	if notify {
		deleted := 0
		for _, r := range res {
			deleted += r.Deleted
		}
		// 没删东西又没出错就不打扰
		if !ok || deleted > 0 {
			title := "🗑️ 115 回收站清理"
			if !ok {
				title += "失败"
			}
			NotifyMessage(title, msg)
		}
	}
}

// ==================== 调度器 ====================

// StartRbCleanScheduler 分钟级 tick：cron 命中时清一遍
func StartRbCleanScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.rbCleanTick(time.Now())
		}
	}()
}

func (h *Handler) rbCleanTick(now time.Time) {
	cfg := loadRbCleanCfg()
	if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
		return
	}
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return
	}
	_ = updateRbCleanCfg(func(c *rbCleanCfg) { c.LastRun = minute })
	h.rbCleanOnce(true)
}

// ==================== HTTP 接口 ====================

// rbAcctView 界面上的一个账号（密钥只回有没有）
type rbAcctView struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Main        bool   `json:"main"`
	LoggedIn    bool   `json:"logged_in"`
	On          bool   `json:"on"`
	KeepDays    int    `json:"keep_days"`
	HasPassword bool   `json:"has_password"`
}

// RbCleanGetConfig GET /rbclean/config
func (h *Handler) RbCleanGetConfig(c *gin.Context) {
	cfg := loadRbCleanCfg()
	accts := []rbAcctView{}
	for _, a := range h.rbCleanAccounts() {
		ac := cfg.acctOf(a.id)
		accts = append(accts, rbAcctView{ID: a.id, Name: a.label, Main: a.main, LoggedIn: a.err == nil && a.cookie != "",
			On: ac.On, KeepDays: ac.KeepDays, HasPassword: ac.Password != ""})
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
		"running":        rbCleanBusy.Load(),
	})
}

var rbPassword = regexp.MustCompile(`^[0-9]{6}$`)

// rbCleanPasswordOK 安全密钥是 6 位数字（空 = 不填）
func rbCleanPasswordOK(pw string) bool {
	return pw == "" || rbPassword.MatchString(pw)
}

// rbAcctReq 请求里的一个账号；password 不带 = 不改，带空串 = 清掉
type rbAcctReq struct {
	ID       uint    `json:"id"`
	On       bool    `json:"on"`
	KeepDays int     `json:"keep_days"`
	Password *string `json:"password"`
}

// checkRbAcctReqs 校验账号存不存在、天数、密钥（纯函数）
func checkRbAcctReqs(reqs []rbAcctReq, accts []rbAcct) error {
	known := map[uint]bool{}
	for _, a := range accts {
		known[a.id] = true
	}
	for _, r := range reqs {
		if !known[r.ID] {
			return fmt.Errorf("账号 id=%d 不存在", r.ID)
		}
		if r.KeepDays < 1 || r.KeepDays > rbCleanMaxDays {
			return fmt.Errorf("保留天数要在 1–%d 之间", rbCleanMaxDays)
		}
		if r.Password != nil && *r.Password != settingMask && !rbCleanPasswordOK(strings.TrimSpace(*r.Password)) {
			return fmt.Errorf("安全密钥是 6 位数字")
		}
	}
	return nil
}

// applyRbAcctReqs 把请求写进配置；请求里没列的账号不动（纯函数）
func applyRbAcctReqs(cfg *rbCleanCfg, reqs []rbAcctReq) {
	for _, r := range reqs {
		key := fmt.Sprint(r.ID)
		ac := cfg.Accts[key]
		ac.On, ac.KeepDays = r.On, r.KeepDays
		// 掩码原样回传 = 没改（界面回显的是 settingMask，与 unmaskSensitiveJSON 同口径）
		if r.Password != nil && *r.Password != settingMask {
			ac.Password = strings.TrimSpace(*r.Password)
		}
		cfg.Accts[key] = ac
	}
}

// RbCleanSaveConfig POST /rbclean/config {enabled, cron, accounts:[{id,on,keep_days,password?}]}
func (h *Handler) RbCleanSaveConfig(c *gin.Context) {
	var req struct {
		Enabled  bool        `json:"enabled"`
		Cron     string      `json:"cron"`
		Accounts []rbAcctReq `json:"accounts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = rbCleanDefaultCron
	}
	if nextCronTime(cronExpr, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 30 4 * * *）"})
		return
	}
	if err := checkRbAcctReqs(req.Accounts, h.rbCleanAccounts()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err := updateRbCleanCfg(func(cfg *rbCleanCfg) {
		cfg.Enabled, cfg.Cron = req.Enabled, cronExpr
		applyRbAcctReqs(cfg, req.Accounts)
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
	log.Printf("[配置] 回收站清理：%v，cron %s，%s", req.Enabled, cronExpr, firstNonEmpty(strings.Join(on, "、"), "没有开启的账号"))
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// RbCleanPreview POST /rbclean/preview {accounts:[{id,keep_days}]}：只读，按没保存的天数看看会删多少。
// 列出来的账号不看开关都算（打开之前先看一眼）
func (h *Handler) RbCleanPreview(c *gin.Context) {
	var req struct {
		Accounts []rbAcctReq `json:"accounts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Accounts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要预览的账号"})
		return
	}
	accts := h.rbCleanAccounts()
	for i := range req.Accounts {
		req.Accounts[i].Password = nil // 预览不删，用不着密钥
	}
	if err := checkRbAcctReqs(req.Accounts, accts); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	byID := map[uint]rbAcct{}
	for _, a := range accts {
		byID[a.id] = a
	}
	var plans []rbPlan
	for _, r := range req.Accounts {
		plans = append(plans, rbPlan{acct: byID[r.ID], keepDays: r.KeepDays})
	}
	res := runRbClean(plans, true)
	msg, _ := rbCleanSummary(res, true)
	c.JSON(http.StatusOK, gin.H{"data": res, "message": msg})
}

// RbCleanRun POST /rbclean/run：按已保存的配置后台清一遍（回收站大时要翻很多页），结果写进配置
func (h *Handler) RbCleanRun(c *gin.Context) {
	if rbCleanBusy.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在清理，稍后再看结果"})
		return
	}
	if len(rbCleanPlans(loadRbCleanCfg(), h.rbCleanAccounts())) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有开启清理的账号，先在配置里打开并保存"})
		return
	}
	go h.rbCleanOnce(false)
	c.JSON(http.StatusAccepted, gin.H{"message": "已开始清理"})
}
