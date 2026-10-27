package api

// ==================== 定时清空 115 指定目录 ====================
//
// 2026-10-25 维护者要求：按计划把几个 115 目录里的内容清掉（目录本身留着），比如「云下载」、
// 「冗余」「已存在」这类只进不出的目录。
//
// 删进 115 回收站（/rb/delete，可还原），不是彻底删除；要彻底删除再配「清理 115 回收站」（rbclean.go）。
//
// ⚠️ 这是按用户选的 cid 整目录删，守卫一条都不能松（同 emptydir.go 的教训，§6.9）：
//   - 列目录走 fetch115FilesPage，它核对返回的 cid（assert115SameDir）：115 对失效 cid 静默返回
//     **网盘根的内容**，不核就是把整个网盘删进回收站；
//   - 目标先读祖先链（fetch115Ancestors，同样认得出「已消失」），读不出就不删；网盘根不许选；
//   - 媒体库目录、转存/待整理文件夹（含绑定库的转存子目录）：目标是它们本身、在它们里面、是它们的上级，都拒绝；
//     「上级」靠逐个读这些目录的祖先链判断（看目标 cid 在不在链上），读不出来就整轮不删这个账号；
//   - 已存在 / 冗余本身可以清（这正是常见用法），但不许清它们的上级；
//   - 列出来的直接子项里有任何受保护的目录，整个目录不删（祖先链之外再兜一道）。
// 只走 Cookie 通道：列目录与祖先链都要 Cookie，各账号用各自的。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	dirCleanKey         = "dirclean"
	dirCleanDefaultCron = "0 5 * * *"
	dirCleanMaxDirs     = 20
	// dirCleanMaxPages 一个目录最多读几页（一页 1150 条），防止接口一直回同一页
	dirCleanMaxPages = 100
	// dirCleanBatch 一次删多少个（/rb/delete 的 fid[i] 表单，没实测上限，取保守值）
	dirCleanBatch = 500
)

// dirCleanDir 一个要清空的目录。Account 同 Library.Account：0 = 主号，其他是 Storage.ID
type dirCleanDir struct {
	Account uint   `json:"account"`
	Cid     string `json:"cid"`
	Path    string `json:"path"` // 选目录时记下的，给人看
}

// dirCleanCfg 配置（setting key "dirclean"）
type dirCleanCfg struct {
	Enabled      bool          `json:"enabled"`
	Cron         string        `json:"cron"`
	Dirs         []dirCleanDir `json:"dirs"`
	LastRun      string        `json:"last_run"` // 最近一次触发的分钟（去重）
	LastResult   string        `json:"last_result"`
	LastResultAt string        `json:"last_result_at"`
	LastOK       bool          `json:"last_ok"`
}

var (
	dirCleanMu   sync.Mutex
	dirCleanBusy atomic.Bool // 同一时间只跑一次（定时与手动共用）
)

func loadDirCleanCfg() dirCleanCfg {
	cfg := dirCleanCfg{Cron: dirCleanDefaultCron}
	if v := settingValueCompat(dirCleanKey); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = dirCleanDefaultCron
	}
	return cfg
}

// updateDirCleanCfg 读改写一次配置（调度器写 last_*、界面写设置，别互相覆盖）
func updateDirCleanCfg(fn func(*dirCleanCfg)) error {
	dirCleanMu.Lock()
	defer dirCleanMu.Unlock()
	cfg := loadDirCleanCfg()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	return notifyConfigSource.SaveSetting(dirCleanKey, string(b))
}

// ---- 受保护的目录 ----

// dirGuard 一个账号里受保护的目录
type dirGuard struct {
	// strict 媒体库、转存/待整理：本身、里面、上级都不许清
	strict map[string]string // cid → 给人看的名字
	// rootOnly 已存在 / 冗余：本身可以清，上级不许
	rootOnly map[string]string
}

func (g dirGuard) all() map[string]string {
	out := map[string]string{}
	for k, v := range g.rootOnly {
		out[k] = v
	}
	for k, v := range g.strict {
		out[k] = v
	}
	return out
}

// dirGuardsOf 各账号受保护的目录（只读配置，不发 115 请求）
func (h *Handler) dirGuardsOf() map[uint]dirGuard {
	out := map[uint]dirGuard{}
	at := func(acct uint) dirGuard {
		g, ok := out[acct]
		if !ok {
			g = dirGuard{strict: map[string]string{}, rootOnly: map[string]string{}}
			out[acct] = g
		}
		return g
	}
	put := func(m map[string]string, cid, name string) {
		if cid = strings.TrimSpace(cid); cid != "" && cid != "0" {
			if _, ok := m[cid]; !ok {
				m[cid] = name
			}
		}
	}
	libs := loadLibraries()
	for _, l := range libs {
		g := at(l.Account)
		put(g.strict, l.Cid, "媒体库「"+l.Name+"」")
		put(g.strict, l.StagingCid, "媒体库「"+l.Name+"」的转存/待整理文件夹")
	}
	main := at(acctMain)
	// 媒体库表还空的老部署：库在 full 里
	if len(libs) == 0 {
		for _, cid := range h.loadFullSyncCfg().libCids() {
			put(main.strict, cid, "媒体库")
		}
	}
	put(main.strict, h.shareFolderCid(), "转存/待整理文件夹")
	for cid := range stagingLibMap(loadStagingLibs()) {
		put(main.strict, cid, "绑定了媒体库的转存子目录")
	}
	basic := h.getSettingValue("org-basic")
	put(main.rootOnly, jsonStrField(basic, "existing"), "已存在文件夹")
	put(main.rootOnly, jsonStrField(basic, "redundant"), "冗余文件夹")
	ws := map[string]acctWS{}
	_ = json.Unmarshal([]byte(h.getSettingValue(acctWorkspaceKey)), &ws)
	for key, w := range ws {
		var acct uint
		if _, err := fmt.Sscan(key, &acct); err != nil || acct == acctMain {
			continue
		}
		g := at(acct)
		put(g.rootOnly, w.Existing, "已存在文件夹")
		put(g.rootOnly, w.Redundant, "冗余文件夹")
	}
	return out
}

// guardChains 受保护目录各自的祖先链（cid 列表，末元素是它自己）
type guardChains map[string][]string

// loadGuardChains 逐个读受保护目录的祖先链。已经不在了的跳过；别的错误整体返回（核不准就不删）
func loadGuardChains(cookie string, g dirGuard) (guardChains, error) {
	out := guardChains{}
	for cid, name := range g.all() {
		chain, err := fetch115Ancestors(cookie, cid)
		if errors.Is(err, errDirGone) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("读%s的位置失败: %w", name, err)
		}
		ids := make([]string, 0, len(chain))
		for _, a := range chain {
			ids = append(ids, a.cid)
		}
		out[cid] = ids
	}
	return out, nil
}

// checkDirCleanTarget 这个目录能不能清空（纯函数）。
// chain = 目标的祖先链（根在最前、末元素是目标自己）；chains = 受保护目录各自的祖先链
func checkDirCleanTarget(cid string, chain []string, g dirGuard, chains guardChains) error {
	if cid == "" || cid == "0" {
		return errors.New("不能清空网盘根目录")
	}
	if len(chain) == 0 || chain[len(chain)-1] != cid {
		return errors.New("核对不了目录位置")
	}
	// 目标本身或它的上级是媒体库 / 转存文件夹：在它们里面（或就是它们）
	for _, c := range chain {
		if name, ok := g.strict[c]; ok {
			if c == cid {
				return fmt.Errorf("这是%s，不能清空", name)
			}
			return fmt.Errorf("在%s里面，不能清空", name)
		}
	}
	// 目标是某个受保护目录的上级
	all := g.all()
	for pc, pchain := range chains {
		if pc == cid {
			continue // 自己（只可能是已存在 / 冗余，strict 上面已经拦了）
		}
		for _, c := range pchain[:max(len(pchain)-1, 0)] {
			if c == cid {
				return fmt.Errorf("里面有%s，不能清空", all[pc])
			}
		}
	}
	return nil
}

// cleanEntry 目录里的一个直接子项
type cleanEntry struct {
	id   string
	name string
	dir  bool
	size int64
}

// listDirChildren 列出目录的全部直接子项。fetch115FilesPage 自带「是不是请求的那个目录」的核对
func listDirChildren(cookie, cid string) ([]cleanEntry, error) {
	var out []cleanEntry
	seen := map[string]bool{}
	offset := 0
	for page := 0; page < dirCleanMaxPages; page++ {
		items, count, _, err := fetch115FilesPage(cookie, ua115Unified(), cid, offset)
		if errors.Is(err, errEmptyUnverified) && offset == 0 {
			return nil, nil // 核不准的空结果：当空目录，什么都不删
		}
		if err != nil {
			return nil, err
		}
		for _, m := range items {
			e := cleanEntry{name: nilSprint(m["n"]), dir: nilSprint(m["f"]) == "0"}
			if e.dir {
				e.id = nilSprint(m["cid"])
			} else {
				e.id = nilSprint(m["fid"])
				fmt.Sscan(nilSprint(m["s"]), &e.size)
			}
			if e.id == "" || e.id == "0" || e.id == cid || seen[e.id] {
				continue
			}
			seen[e.id] = true
			out = append(out, e)
		}
		offset += len(items)
		if len(items) == 0 || offset >= count {
			return out, nil
		}
	}
	return nil, fmt.Errorf("目录超过 %d 页，没读完，这次不删", dirCleanMaxPages)
}

// dirCleanResult 一个目录的结果
type dirCleanResult struct {
	Account string `json:"account"`
	Cid     string `json:"cid"`
	Path    string `json:"path"`
	Dirs    int    `json:"dirs"`  // 里面几个文件夹
	Files   int    `json:"files"` // 几个文件（直接放在里面的）
	Size    int64  `json:"size"`  // 直接放在里面的文件总大小（文件夹的大小列表里不给）
	Deleted int    `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// runDirClean 逐个目录清（dry=true 只统计不删）
func (h *Handler) runDirClean(dirs []dirCleanDir, dry bool) []dirCleanResult {
	guards := h.dirGuardsOf()
	type acctCtx struct {
		cookie string
		label  string
		chains guardChains
		err    error
	}
	ctxs := map[uint]*acctCtx{}
	ctxOf := func(acct uint) *acctCtx {
		if c, ok := ctxs[acct]; ok {
			return c
		}
		c := &acctCtx{label: acctLabelOf(h, acct)}
		ctxs[acct] = c
		ck, err := h.cookieOf(acct)
		if err == nil && ck == "" {
			err = errors.New("没有 Cookie 登录（只授权了 OpenAPI 时清不了，要扫码登录）")
		}
		if err != nil {
			c.err = err
			return c
		}
		c.cookie = ck
		c.chains, c.err = loadGuardChains(ck, guards[acct])
		return c
	}
	var out []dirCleanResult
	for _, d := range dirs {
		ac := ctxOf(d.Account)
		res := dirCleanResult{Account: ac.label, Cid: d.Cid, Path: firstNonEmpty(d.Path, d.Cid)}
		fail := func(err error) {
			res.Error = err.Error()
			out = append(out, res)
		}
		if ac.err != nil {
			fail(ac.err)
			continue
		}
		g := guards[d.Account]
		if g.strict == nil {
			g = dirGuard{strict: map[string]string{}, rootOnly: map[string]string{}}
		}
		chain, err := fetch115Ancestors(ac.cookie, d.Cid)
		if errors.Is(err, errDirGone) {
			fail(errors.New("目录已经不在了（被删除或移进回收站），重新选一次"))
			continue
		}
		if err != nil {
			fail(fmt.Errorf("读目录位置失败: %w", err))
			continue
		}
		ids := make([]string, 0, len(chain))
		for _, a := range chain {
			ids = append(ids, a.cid)
		}
		if rows := chainToRows(chain); len(rows) > 0 {
			res.Path = rows[len(rows)-1].Path // 以现在的位置为准（可能改过名）
		}
		if err := checkDirCleanTarget(d.Cid, ids, g, ac.chains); err != nil {
			fail(err)
			continue
		}
		items, err := listDirChildren(ac.cookie, d.Cid)
		if err != nil {
			fail(fmt.Errorf("列目录失败: %w", err))
			continue
		}
		all := g.all()
		var del []string
		for _, e := range items {
			if name, ok := all[e.id]; ok {
				res.Error = fmt.Sprintf("里面有%s，这次不删", name)
				del = nil
				break
			}
			if e.dir {
				res.Dirs++
			} else {
				res.Files++
				res.Size += e.size
			}
			del = append(del, e.id)
		}
		if res.Error != "" {
			out = append(out, res)
			continue
		}
		if !dry && len(del) > 0 {
			ops := &pan115Ops{cookie: ac.cookie, acct: d.Account}
			for i := 0; i < len(del); i += dirCleanBatch {
				end := min(i+dirCleanBatch, len(del))
				if err := ops.deleteFiles(del[i:end]); err != nil {
					res.Error = "删除失败: " + err.Error()
					break
				}
				res.Deleted = end
			}
		}
		out = append(out, res)
	}
	return out
}

// dirCleanSummary 结果写成一段文字（通知与界面共用），每个目录一行
func dirCleanSummary(res []dirCleanResult, dry bool) (string, bool) {
	ok := true
	var lines []string
	multiAcct := false
	for _, r := range res {
		if r.Account != res[0].Account {
			multiAcct = true
		}
	}
	for _, r := range res {
		head := r.Path
		if multiAcct {
			head = "「" + r.Account + "」" + head
		}
		n := r.Dirs + r.Files
		what := fmt.Sprintf("%d 个文件夹、%d 个文件", r.Dirs, r.Files)
		if r.Size > 0 {
			what += "（文件 " + humanSize(r.Size) + "）"
		}
		switch {
		case r.Error != "" && r.Deleted == 0:
			ok = false
			lines = append(lines, head+"：✗ "+r.Error)
			continue
		case n == 0:
			lines = append(lines, head+"：本来就是空的")
		case dry:
			lines = append(lines, head+"：会删掉 "+what)
		default:
			lines = append(lines, fmt.Sprintf("%s：删进回收站 %d / %d 项，%s", head, r.Deleted, n, what))
		}
		if r.Error != "" {
			ok = false
			lines = append(lines, head+"：✗ "+r.Error)
		}
	}
	return strings.Join(lines, "\n"), ok
}

// dirCleanOnce 按已保存的配置清一次并记结果；notify=true 推通知（定时触发）
func (h *Handler) dirCleanOnce(notify bool) {
	if !dirCleanBusy.CompareAndSwap(false, true) {
		return
	}
	defer dirCleanBusy.Store(false)
	cfg := loadDirCleanCfg()
	if len(cfg.Dirs) == 0 {
		return
	}
	res := h.runDirClean(cfg.Dirs, false)
	msg, ok := dirCleanSummary(res, false)
	flat := strings.ReplaceAll(msg, "\n", "；")
	if ok {
		plogDirClean.Printf("✓ %s", flat)
	} else {
		plogDirClean.Printf("✗ %s", flat)
	}
	_ = updateDirCleanCfg(func(c *dirCleanCfg) {
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
			title := "🧹 定时清空 115 目录"
			if !ok {
				title += "出错"
			}
			NotifyMessage(kindTask, title, msg)
		}
	}
}

// ==================== 调度器 ====================

// StartDirCleanScheduler 分钟级 tick：cron 命中时清一遍
func StartDirCleanScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.dirCleanTick(time.Now())
		}
	}()
}

func (h *Handler) dirCleanTick(now time.Time) {
	cfg := loadDirCleanCfg()
	if !cfg.Enabled || len(cfg.Dirs) == 0 || !CronMatch(cfg.Cron, now) {
		return
	}
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return
	}
	_ = updateDirCleanCfg(func(c *dirCleanCfg) { c.LastRun = minute })
	h.dirCleanOnce(true)
}

// ==================== HTTP 接口 ====================

// dirCleanAcctView 界面上的账号下拉（id 同 Library.Account：主号 0）
type dirCleanAcctView struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Main     bool   `json:"main"`
	LoggedIn bool   `json:"logged_in"`
}

func (h *Handler) dirCleanAccounts() []dirCleanAcctView {
	var out []dirCleanAcctView
	for _, a := range h.rbCleanAccounts() {
		v := dirCleanAcctView{ID: a.id, Name: a.label, Main: a.main, LoggedIn: a.err == nil && a.cookie != ""}
		if a.main {
			v.ID = acctMain
		}
		out = append(out, v)
	}
	return out
}

// DirCleanGetConfig GET /dirclean/config
func (h *Handler) DirCleanGetConfig(c *gin.Context) {
	cfg := loadDirCleanCfg()
	next := ""
	if cfg.Enabled && len(cfg.Dirs) > 0 {
		if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
			next = t.Format("01-02 15:04")
		}
	}
	dirs := cfg.Dirs
	if dirs == nil {
		dirs = []dirCleanDir{}
	}
	accts := h.dirCleanAccounts()
	if accts == nil {
		accts = []dirCleanAcctView{}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"cron":           cfg.Cron,
		"dirs":           dirs,
		"accounts":       accts,
		"next_run":       next,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"last_ok":        cfg.LastOK,
		"running":        dirCleanBusy.Load(),
	})
}

// normalizeDirCleanDirs 校验并去重（纯函数）：账号要存在、cid 是数字且不是根、同账号同 cid 只留一个
func normalizeDirCleanDirs(in []dirCleanDir, accts []uint) ([]dirCleanDir, error) {
	known := map[uint]bool{}
	for _, a := range accts {
		known[a] = true
	}
	seen := map[string]bool{}
	out := []dirCleanDir{}
	for _, d := range in {
		d.Cid, d.Path = strings.TrimSpace(d.Cid), strings.TrimSpace(d.Path)
		if d.Cid == "" {
			return nil, fmt.Errorf("目录「%s」还没选好（路径没解析出目录）", firstNonEmpty(d.Path, "空"))
		}
		if !rbDigits.MatchString(d.Cid) {
			return nil, fmt.Errorf("目录 id 不对：%s", d.Cid)
		}
		if d.Cid == "0" {
			return nil, errors.New("不能清空网盘根目录")
		}
		if !known[d.Account] {
			return nil, fmt.Errorf("账号 id=%d 不存在", d.Account)
		}
		key := fmt.Sprintf("%d:%s", d.Account, d.Cid)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	if len(out) > dirCleanMaxDirs {
		return nil, fmt.Errorf("最多 %d 个目录", dirCleanMaxDirs)
	}
	return out, nil
}

func (h *Handler) dirCleanAcctIDs() []uint {
	var ids []uint
	for _, a := range h.dirCleanAccounts() {
		ids = append(ids, a.ID)
	}
	return ids
}

// dirCleanPrecheck 保存 / 预览前对每个目录过一遍守卫（读祖先链，不列内容）。返回第一个不能清的
func (h *Handler) dirCleanPrecheck(dirs []dirCleanDir) error {
	guards := h.dirGuardsOf()
	chainsOf := map[uint]guardChains{}
	for _, d := range dirs {
		ck, err := h.cookieOf(d.Account)
		if err == nil && ck == "" {
			err = errors.New("没有 Cookie 登录")
		}
		if err != nil {
			return fmt.Errorf("%s：%v", firstNonEmpty(d.Path, d.Cid), err)
		}
		g := guards[d.Account]
		if g.strict == nil {
			g = dirGuard{strict: map[string]string{}, rootOnly: map[string]string{}}
		}
		chains, ok := chainsOf[d.Account]
		if !ok {
			if chains, err = loadGuardChains(ck, g); err != nil {
				return err
			}
			chainsOf[d.Account] = chains
		}
		chain, err := fetch115Ancestors(ck, d.Cid)
		if errors.Is(err, errDirGone) {
			return fmt.Errorf("%s：目录已经不在了", firstNonEmpty(d.Path, d.Cid))
		}
		if err != nil {
			return fmt.Errorf("%s：读目录位置失败: %v", firstNonEmpty(d.Path, d.Cid), err)
		}
		ids := make([]string, 0, len(chain))
		for _, a := range chain {
			ids = append(ids, a.cid)
		}
		if err := checkDirCleanTarget(d.Cid, ids, g, chains); err != nil {
			return fmt.Errorf("%s：%v", firstNonEmpty(d.Path, d.Cid), err)
		}
	}
	return nil
}

// DirCleanSaveConfig POST /dirclean/config {enabled, cron, dirs:[{account,cid,path}]}
func (h *Handler) DirCleanSaveConfig(c *gin.Context) {
	var req struct {
		Enabled bool          `json:"enabled"`
		Cron    string        `json:"cron"`
		Dirs    []dirCleanDir `json:"dirs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = dirCleanDefaultCron
	}
	if nextCronTime(cronExpr, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 0 5 * * *）"})
		return
	}
	dirs, err := normalizeDirCleanDirs(req.Dirs, h.dirCleanAcctIDs())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 保存时就拦下媒体库 / 转存文件夹，别等定时跑的时候才在通知里报
	if err := h.dirCleanPrecheck(dirs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err = updateDirCleanCfg(func(cfg *dirCleanCfg) {
		cfg.Enabled, cfg.Cron, cfg.Dirs = req.Enabled, cronExpr, dirs
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	var paths []string
	for _, d := range dirs {
		paths = append(paths, firstNonEmpty(d.Path, d.Cid))
	}
	plogDirClean.Printf("○ 保存配置：开启 %v，cron %s，%s", req.Enabled, cronExpr, firstNonEmpty(strings.Join(paths, "、"), "没有目录"))
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// DirCleanPreview POST /dirclean/preview {dirs:[...]}：只读，按没保存的目录看看会删什么
func (h *Handler) DirCleanPreview(c *gin.Context) {
	var req struct {
		Dirs []dirCleanDir `json:"dirs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	dirs, err := normalizeDirCleanDirs(req.Dirs, h.dirCleanAcctIDs())
	if err == nil && len(dirs) == 0 {
		err = errors.New("还没添加目录")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res := h.runDirClean(dirs, true)
	msg, _ := dirCleanSummary(res, true)
	c.JSON(http.StatusOK, gin.H{"data": res, "message": msg})
}

// DirCleanRun POST /dirclean/run：按已保存的配置后台清一遍，结果写进配置
func (h *Handler) DirCleanRun(c *gin.Context) {
	if dirCleanBusy.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在清空，稍后再看结果"})
		return
	}
	if len(loadDirCleanCfg().Dirs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没添加目录，先在配置里添加并保存"})
		return
	}
	go h.dirCleanOnce(false)
	c.JSON(http.StatusAccepted, gin.H{"message": "已开始清空"})
}
