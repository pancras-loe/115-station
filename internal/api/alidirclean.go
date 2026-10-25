package api

// ==================== 定时清空阿里云盘指定目录 ====================
//
// 2026-10-25 维护者要求，照 115 那份（dirclean.go）做：到点把几个目录里的内容删进阿里云盘回收站
// （batch /recyclebin/trash，可还原），目录本身留着。常见用法是「来自分享」这类只进不出的目录。
// 要彻底删除再配「清理阿里云盘回收站」（alirbclean.go）。
//
// 目录按路径存（阿里云盘没有现成的目录选择器给界面用），保存时逐级按名字列目录查出 file_id 一起存；
// 每次执行再按路径查一遍，和存着的 file_id 对不上（改名、移动、删了重建）就不删，重新保存一次才认。
//
// 守卫（同 115 的思路，按路径就能判，不用读祖先链）：
//   - 网盘根不许；
//   - 阿里云盘秒传的临时目录（资源盘根下 aliTempRoot）本身与里面不许：秒传正在跑时清掉，那一单就废了；
//   - 同一层有两个同名目录时认不准，不删；
//   - 列出来的子项 parent_file_id 不是这个目录的，整个目录不删。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

const (
	aliDirCleanKey         = "alidirclean"
	aliDirCleanDefaultCron = "10 5 * * *"
	aliDirCleanMaxDirs     = 20
)

// aliDirCleanDir 一个要清空的目录。Drive：resource 资源盘（默认）/ backup 备份盘
type aliDirCleanDir struct {
	Account uint   `json:"account"` // PanSource.ID
	Drive   string `json:"drive"`
	Path    string `json:"path"`
	FileID  string `json:"file_id"` // 保存时按路径查出来的
}

type aliDirCleanCfg struct {
	Enabled      bool             `json:"enabled"`
	Cron         string           `json:"cron"`
	Dirs         []aliDirCleanDir `json:"dirs"`
	LastRun      string           `json:"last_run"`
	LastResult   string           `json:"last_result"`
	LastResultAt string           `json:"last_result_at"`
	LastOK       bool             `json:"last_ok"`
}

var (
	aliDirCleanMu   sync.Mutex
	aliDirCleanBusy atomic.Bool
)

func loadAliDirCleanCfg() aliDirCleanCfg {
	cfg := aliDirCleanCfg{Cron: aliDirCleanDefaultCron}
	if v := settingValueCompat(aliDirCleanKey); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = aliDirCleanDefaultCron
	}
	return cfg
}

func updateAliDirCleanCfg(fn func(*aliDirCleanCfg)) error {
	aliDirCleanMu.Lock()
	defer aliDirCleanMu.Unlock()
	cfg := loadAliDirCleanCfg()
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	return notifyConfigSource.SaveSetting(aliDirCleanKey, string(b))
}

// aliPathParts 把路径拆成各级名字（纯函数）。认 / 与 \，去掉空段；. 和 .. 不认
func aliPathParts(p string) ([]string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	var out []string
	for _, s := range strings.Split(p, "/") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return nil, fmt.Errorf("路径里不能有 %s", s)
		}
		out = append(out, s)
	}
	return out, nil
}

func aliJoinPath(parts []string) string { return "/" + strings.Join(parts, "/") }

func aliDriveLabel(d string) string {
	if d == "backup" {
		return "备份盘"
	}
	return "资源盘"
}

// checkAliDirTarget 这个目录能不能清空（纯函数）。tempDrive = 这个目录所在的盘是不是秒传临时目录那个盘
func checkAliDirTarget(parts []string, tempDrive bool) error {
	if len(parts) == 0 {
		return errors.New("不能清空网盘根目录")
	}
	if tempDrive && parts[0] == aliTempRoot {
		return errors.New("这是阿里云盘秒传的临时目录，不能清空（秒传自己会收拾）")
	}
	return nil
}

var errAliPathGone = errors.New("目录不在了（改名、移动或删除了），改好路径重新保存")

// aliResolvePath 逐级按名字找目录，返回 file_id。同一层有同名目录时报错（认不准）
func aliResolvePath(s aliCleanSide, drive string, parts []string) (string, error) {
	cur := "root"
	for _, name := range parts {
		items, err := s.listDirIn(drive, cur)
		if err != nil {
			return "", fmt.Errorf("列目录失败: %w", err)
		}
		next := ""
		for _, it := range items {
			if it.isDir() && it.Name == name {
				if next != "" {
					return "", fmt.Errorf("「%s」这一层有两个同名目录，认不准", name)
				}
				next = it.FileID
			}
		}
		if next == "" {
			return "", errAliPathGone
		}
		cur = next
	}
	return cur, nil
}

// aliDirResult 一个目录的结果
type aliDirResult struct {
	Account string `json:"account"`
	Drive   string `json:"drive"`
	Path    string `json:"path"`
	FileID  string `json:"file_id"`
	Dirs    int    `json:"dirs"`
	Files   int    `json:"files"`
	Size    int64  `json:"size"`
	Deleted int    `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// runAliDirClean 逐个目录清。dry=true 只统计不删；check=false 时不比存着的 file_id（保存 / 预览时还没有）
func runAliDirClean(dirs []aliDirCleanDir, srcs []model.PanSource, open aliCleanOpener, dry, check bool) []aliDirResult {
	byID := map[uint]model.PanSource{}
	for _, s := range srcs {
		byID[s.ID] = s
	}
	type sess struct {
		side  aliCleanSide
		label string
		err   error
	}
	sessions := map[uint]*sess{}
	sessOf := func(id uint) *sess {
		if s, ok := sessions[id]; ok {
			return s
		}
		s := &sess{label: fmt.Sprintf("阿里云盘 %d", id)}
		sessions[id] = s
		src, ok := byID[id]
		if !ok {
			s.err = errors.New("这个阿里云盘账号已经删掉了")
			return s
		}
		s.label = aliSrcLabel(src)
		s.side, s.err = open(src)
		return s
	}
	var out []aliDirResult
	for _, d := range dirs {
		ss := sessOf(d.Account)
		res := aliDirResult{Account: ss.label, Drive: d.Drive, Path: d.Path}
		fail := func(err error) {
			res.Error = err.Error()
			out = append(out, res)
		}
		if ss.err != nil {
			fail(ss.err)
			continue
		}
		parts, err := aliPathParts(d.Path)
		if err != nil {
			fail(err)
			continue
		}
		res.Path = aliJoinPath(parts)
		resDrive, backDrive := ss.side.drives()
		drive := resDrive
		if d.Drive == "backup" {
			drive = backDrive
		}
		if drive == "" {
			fail(errors.New("阿里云盘没返回网盘编号"))
			continue
		}
		// 秒传的临时目录建在 aliClient.driveID（= 资源盘，没有资源盘时是默认盘）的根下
		if err := checkAliDirTarget(parts, drive == resDrive); err != nil {
			fail(err)
			continue
		}
		id, err := aliResolvePath(ss.side, drive, parts)
		if err != nil {
			fail(err)
			continue
		}
		res.FileID = id
		if check && d.FileID != "" && d.FileID != id {
			fail(errors.New("这个路径现在是另一个目录了（删了重建或换过位置），确认后重新保存一次"))
			continue
		}
		items, err := ss.side.listDirIn(drive, id)
		if err != nil {
			fail(fmt.Errorf("列目录失败: %w", err))
			continue
		}
		var del []string
		for _, it := range items {
			if it.ParentFileID != "" && it.ParentFileID != id {
				res.Error = "阿里云盘返回的内容不是这个目录里的，这次不删"
				del = nil
				break
			}
			if it.FileID == "" || it.FileID == id {
				continue
			}
			if it.isDir() {
				res.Dirs++
			} else {
				res.Files++
				res.Size += it.Size
			}
			del = append(del, it.FileID)
		}
		if res.Error != "" {
			out = append(out, res)
			continue
		}
		if !dry && len(del) > 0 {
			done, err := ss.side.batchByID("/recyclebin/trash", drive, del)
			res.Deleted = done
			if err != nil {
				res.Error = "删除失败: " + err.Error()
			}
		}
		out = append(out, res)
	}
	return out
}

// aliDirCleanSummary 结果写成一段文字，每个目录一行
func aliDirCleanSummary(res []aliDirResult, dry bool) (string, bool) {
	ok := true
	multi := false
	for _, r := range res {
		if r.Account != res[0].Account {
			multi = true
		}
	}
	var lines []string
	for _, r := range res {
		head := aliDriveLabel(r.Drive) + " " + r.Path
		if multi {
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

func (h *Handler) aliDirCleanOnce(notify bool) {
	if !aliDirCleanBusy.CompareAndSwap(false, true) {
		return
	}
	defer aliDirCleanBusy.Store(false)
	cfg := loadAliDirCleanCfg()
	if len(cfg.Dirs) == 0 {
		return
	}
	res := runAliDirClean(cfg.Dirs, aliSources(h.DB), h.aliCleanOpen, false, true)
	msg, ok := aliDirCleanSummary(res, false)
	flat := strings.ReplaceAll(msg, "\n", "；")
	if ok {
		log.Printf("[阿里清空目录] ✓ %s", flat)
	} else {
		log.Printf("[阿里清空目录] ✗ %s", flat)
	}
	_ = updateAliDirCleanCfg(func(c *aliDirCleanCfg) {
		c.LastResult, c.LastOK = msg, ok
		c.LastResultAt = time.Now().Format("2006-01-02 15:04:05")
	})
	if notify {
		deleted := 0
		for _, r := range res {
			deleted += r.Deleted
		}
		if !ok || deleted > 0 {
			title := "🧹 定时清空阿里云盘目录"
			if !ok {
				title += "出错"
			}
			NotifyMessage(title, msg)
		}
	}
}

// ==================== 调度器 ====================

// StartAliDirCleanScheduler 分钟级 tick：cron 命中时清一遍
func StartAliDirCleanScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.aliDirCleanTick(time.Now())
		}
	}()
}

func (h *Handler) aliDirCleanTick(now time.Time) {
	cfg := loadAliDirCleanCfg()
	if !cfg.Enabled || len(cfg.Dirs) == 0 || !CronMatch(cfg.Cron, now) {
		return
	}
	minute := now.Format("2006-01-02 15:04")
	if cfg.LastRun == minute {
		return
	}
	_ = updateAliDirCleanCfg(func(c *aliDirCleanCfg) { c.LastRun = minute })
	h.aliDirCleanOnce(true)
}

// ==================== HTTP 接口 ====================

type aliDirAcctView struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	LastErr string `json:"last_err,omitempty"`
}

// AliDirCleanGetConfig GET /alidirclean/config
func (h *Handler) AliDirCleanGetConfig(c *gin.Context) {
	cfg := loadAliDirCleanCfg()
	next := ""
	if cfg.Enabled && len(cfg.Dirs) > 0 {
		if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
			next = t.Format("01-02 15:04")
		}
	}
	dirs := cfg.Dirs
	if dirs == nil {
		dirs = []aliDirCleanDir{}
	}
	accts := []aliDirAcctView{}
	for _, s := range aliSources(h.DB) {
		accts = append(accts, aliDirAcctView{ID: s.ID, Name: aliSrcLabel(s), LastErr: s.LastErr})
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
		"running":        aliDirCleanBusy.Load(),
	})
}

// normalizeAliDirs 校验并去重（纯函数）：账号要存在、路径规整且不是根、同账号同盘同路径只留一个
func normalizeAliDirs(in []aliDirCleanDir, srcs []model.PanSource) ([]aliDirCleanDir, error) {
	known := map[uint]bool{}
	for _, s := range srcs {
		known[s.ID] = true
	}
	seen := map[string]bool{}
	out := []aliDirCleanDir{}
	for _, d := range in {
		if strings.TrimSpace(d.Path) == "" {
			continue
		}
		parts, err := aliPathParts(d.Path)
		if err != nil {
			return nil, fmt.Errorf("「%s」：%v", d.Path, err)
		}
		if len(parts) == 0 {
			return nil, errors.New("不能清空网盘根目录")
		}
		if !known[d.Account] {
			return nil, fmt.Errorf("阿里云盘账号 id=%d 不存在", d.Account)
		}
		if d.Drive != "backup" {
			d.Drive = "resource"
		}
		d.Path = aliJoinPath(parts)
		key := fmt.Sprintf("%d:%s:%s", d.Account, d.Drive, d.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	if len(out) > aliDirCleanMaxDirs {
		return nil, fmt.Errorf("最多 %d 个目录", aliDirCleanMaxDirs)
	}
	return out, nil
}

// AliDirCleanSaveConfig POST /alidirclean/config {enabled, cron, dirs:[{account,drive,path}]}。
// 保存时按路径逐个查出目录（顺带过一遍守卫），查不到的整份不存
func (h *Handler) AliDirCleanSaveConfig(c *gin.Context) {
	var req struct {
		Enabled bool             `json:"enabled"`
		Cron    string           `json:"cron"`
		Dirs    []aliDirCleanDir `json:"dirs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if cronExpr == "" {
		cronExpr = aliDirCleanDefaultCron
	}
	if nextCronTime(cronExpr, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 10 5 * * *）"})
		return
	}
	srcs := aliSources(h.DB)
	dirs, err := normalizeAliDirs(req.Dirs, srcs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(dirs) > 0 {
		// 只查位置不删（dry）；列一次目录内容是多余的，但保存不常做，换来与执行同一条路径
		res := runAliDirClean(dirs, srcs, h.aliCleanOpen, true, false)
		for i, r := range res {
			if r.Error != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": aliDriveLabel(r.Drive) + " " + r.Path + "：" + r.Error})
				return
			}
			dirs[i].FileID = r.FileID
		}
	}
	err = updateAliDirCleanCfg(func(cfg *aliDirCleanCfg) {
		cfg.Enabled, cfg.Cron, cfg.Dirs = req.Enabled, cronExpr, dirs
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	var paths []string
	for _, d := range dirs {
		paths = append(paths, aliDriveLabel(d.Drive)+d.Path)
	}
	log.Printf("[配置] 定时清空阿里云盘目录：%v，cron %s，%s", req.Enabled, cronExpr, firstNonEmpty(strings.Join(paths, "、"), "没有目录"))
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// AliDirCleanPreview POST /alidirclean/preview {dirs:[...]}：只读，按没保存的目录看看会删什么
func (h *Handler) AliDirCleanPreview(c *gin.Context) {
	var req struct {
		Dirs []aliDirCleanDir `json:"dirs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	srcs := aliSources(h.DB)
	dirs, err := normalizeAliDirs(req.Dirs, srcs)
	if err == nil && len(dirs) == 0 {
		err = errors.New("还没添加目录")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res := runAliDirClean(dirs, srcs, h.aliCleanOpen, true, false)
	msg, _ := aliDirCleanSummary(res, true)
	c.JSON(http.StatusOK, gin.H{"data": res, "message": msg})
}

// AliDirCleanRun POST /alidirclean/run：按已保存的配置后台清一遍
func (h *Handler) AliDirCleanRun(c *gin.Context) {
	if aliDirCleanBusy.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在清空，稍后再看结果"})
		return
	}
	if len(loadAliDirCleanCfg().Dirs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没添加目录，先在配置里添加并保存"})
		return
	}
	go h.aliDirCleanOnce(false)
	c.JSON(http.StatusAccepted, gin.H{"message": "已开始清空"})
}
