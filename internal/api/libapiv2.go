package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 媒体库页面的接口（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.1 / §10.3，V2 阶段 4） ====================
//
// 一个库一条（model.Library），页面上一个库一张卡片：名称 → 115 网盘目录（连同账号）→ 本地目录（媒体根下）
// → 转存/待整理文件夹 → 分类方案 → 刮削方式。增删改一次只动一个库，校验整组（validateLibraries）。
//
// 媒体库表是唯一事实来源。还有四十来处老代码读 full.cid / extra_libs / lib_opts / acct_libs（工作区角色、
// 网盘文件页、Emby 对账……），所以每次存完把表**写回**这几份老配置（writeLegacyLibConfigs），
// 让它们看到的和表一致；老配置不再反过来同步进表（syncLibrariesFromLegacy 只留给老的 PUT /libraries）。
//
// GET 零 115 请求：位置一律用库里存的显示路径；Emby 配了才读一次它的媒体库列表（覆盖检查）。

// mediaLibView 一张库卡片
type mediaLibView struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Account     uint   `json:"account"`
	AccountName string `json:"account_name"`
	Cid         string `json:"cid"`
	Path        string `json:"path"`
	Local       string `json:"local"`     // 媒体根下的相对路径
	LocalAbs    string `json:"local_abs"` // 媒体根 + 本地目录
	StagingCid  string `json:"staging_cid"`
	StagingPath string `json:"staging_path"`
	SchemeID    uint   `json:"scheme_id"`
	SchemeName  string `json:"scheme_name"`
	Depth       int    `json:"depth"`
	Scrape      string `json:"scrape"`
	Upload      bool   `json:"upload"` // 本地新产生的 NFO / 图片回传 115（libupload.go）
	// LocalLocked 台账里已经有这个库的内容：本地目录不能再改（改了旧 STRM 没人管、Emby 条目全换）
	LocalLocked bool         `json:"local_locked"`
	LastSync    *libSyncStat `json:"last_sync,omitempty"`
	// Problem 这个库这一轮整理会遇到的问题（没设转存文件夹 = 只同步，不算问题，见 Organize）
	Problem  string        `json:"problem,omitempty"`
	Organize bool          `json:"organize"` // 设了转存文件夹：本站往里整理
	Emby     *libEmbyState `json:"emby,omitempty"`
}

// schemeView 分类方案清单的一项
type schemeView struct {
	ID     uint     `json:"id"`
	Name   string   `json:"name"`
	UsedBy []string `json:"used_by"` // 用着它的库名
	Movie  int      `json:"movie"`   // 电影分类几条
	TV     int      `json:"tv"`      // 剧集分类几条
}

// acctOption 选目录时可选的账号
type acctOption struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Main bool   `json:"main"`
}

// mediaLibsView GET /media-libs 的全部内容
type mediaLibsView struct {
	MediaRoot string         `json:"media_root"`
	Libs      []mediaLibView `json:"libs"`
	Schemes   []schemeView   `json:"schemes"`
	Accounts  []acctOption   `json:"accounts"`
	// Notice 升级时的提示（转存文件夹要重设、原来的分库方式），点「知道了」清掉
	Notice []string `json:"notice"`
	EmbyConfigured bool   `json:"emby_configured"`
	EmbyError      string `json:"emby_error,omitempty"`
	RootProblem    string `json:"root_problem,omitempty"` // 媒体根没挂上（mediaroot.go）
}

// accountOptions 主号 + 附加账号
func accountOptions() []acctOption {
	out := []acctOption{}
	if model.DB == nil {
		return out
	}
	var rows []model.Storage
	model.DB.Where("type = ?", "115").Order("id").Find(&rows)
	for _, s := range rows {
		if s.Main {
			out = append([]acctOption{{ID: acctMain, Name: firstNonEmpty(s.UserName, "主账号"), Main: true}}, out...)
			continue
		}
		out = append(out, acctOption{ID: s.ID, Name: acctLabel(s)})
	}
	if len(out) == 0 || !out[0].Main {
		out = append([]acctOption{{ID: acctMain, Name: "主账号", Main: true}}, out...)
	}
	return out
}

// mediaLibsNow 当前状态（GET 与各写操作的响应共用）
func (h *Handler) mediaLibsNow() mediaLibsView {
	libs := loadLibraries()
	root := strings.TrimRight(strings.ReplaceAll(mediaRoot(), `\`, "/"), "/")
	v := mediaLibsView{MediaRoot: root, Libs: []mediaLibView{}, Accounts: accountOptions(),
		Notice: []string{}}
	acctName := map[uint]string{}
	for _, a := range v.Accounts {
		acctName[a.ID] = a.Name
	}

	cfg, embyOK := loadEmbyRefreshCfg()
	var locs []string
	if embyOK {
		if folders, err := embyVirtualFolders(cfg); err != nil {
			v.EmbyError = err.Error()
		} else {
			locs = []string{}
			for _, f := range folders {
				locs = append(locs, f.Locations...)
			}
		}
	}
	v.EmbyConfigured = embyOK

	used := map[uint][]string{}
	stats := parseLibSyncStats(h.getSettingValue(libSyncStatsKey))
	for _, l := range libs {
		mv := mediaLibView{ID: l.ID, Name: l.Name, Account: l.Account, AccountName: acctName[l.Account], Cid: l.Cid, Path: l.Path,
			Local: l.Local, LocalAbs: root + "/" + normLibLocal(l.Local), StagingCid: l.StagingCid, StagingPath: l.StagingPath,
			SchemeID: l.SchemeID, SchemeName: schemeName(l.SchemeID), Depth: l.Depth, Scrape: l.Scrape, Upload: l.Upload,
			LocalLocked: ledgerHasLib(l.Local), Organize: l.StagingCid != ""}
		if mv.AccountName == "" {
			mv.AccountName = fmt.Sprintf("已删除的账号 %d", l.Account)
		}
		if l.SchemeID == 0 {
			mv.SchemeName = ""
		}
		if st, ok := stats[libKeyOf(l)]; ok {
			mv.LastSync = &st
		}
		if l.SchemeID != 0 {
			if _, ok := schemeByID(l.SchemeID); !ok {
				mv.Problem = "选的分类方案已经删除了：重新选一套（或选不分类）"
			}
			used[l.SchemeID] = append(used[l.SchemeID], l.Name)
		}
		if locs != nil {
			var cats []string
			if l.SchemeID != 0 {
				for _, r := range classifyRulesOf(l.SchemeID, "movie") {
					cats = append(cats, r.Name)
				}
				for _, r := range classifyRulesOf(l.SchemeID, "tv") {
					cats = append(cats, r.Name)
				}
			} else if l.Depth >= 2 {
				// 不分类、片目在第二层以下：用户自己的目录结构（爱优腾芒/电影、爱优腾芒/剧集），
				// 第一层就是分类，Emby 常按它们各建一个库。没有分类规则可读，拿本地实际的子目录当分类，
				// 否则每个子目录都进了 Emby 也报「Emby 里还没有这个目录」（2026-10-22 现场误报）
				cats = localSubdirs(mv.LocalAbs)
			}
			st := libEmbyCoverage(cfg.PathMapping, locs, mv.LocalAbs, cats)
			mv.Emby = &st
		}
		v.Libs = append(v.Libs, mv)
	}
	for _, s := range loadSchemes() {
		sv := schemeView{ID: s.ID, Name: s.Name, UsedBy: used[s.ID]}
		if sv.UsedBy == nil {
			sv.UsedBy = []string{}
		}
		sv.Movie, sv.TV = len(classifyRulesOf(s.ID, "movie")), len(classifyRulesOf(s.ID, "tv"))
		v.Schemes = append(v.Schemes, sv)
	}
	if v.Schemes == nil {
		v.Schemes = []schemeView{}
	}
	var n libsV2Notice
	if json.Unmarshal([]byte(h.getSettingValue(libsV2NoticeKey)), &n) == nil && n.Lines != nil {
		v.Notice = n.Lines
	}
	v.RootProblem = mediaRootProblem()
	return v
}

// mediaLibReq 新建 / 修改一个库
type mediaLibReq struct {
	Name        string `json:"name"`
	Account     uint   `json:"account"`
	Cid         string `json:"cid"`
	Path        string `json:"path"`
	Local       string `json:"local"`
	StagingCid  string `json:"staging_cid"`
	StagingPath string `json:"staging_path"`
	SchemeID    uint   `json:"scheme_id"`
	Depth       int    `json:"depth"`
	Scrape      string `json:"scrape"`
	// Upload 不带 = 不改（批量添加与老页面不发这个字段，别把开着的关掉）
	Upload *bool `json:"upload"`
}

// applyTo 把请求写进一个库（新建时 l 是零值）
func (r mediaLibReq) applyTo(l *model.Library) {
	l.Name, l.Account, l.Cid, l.Path = strings.TrimSpace(r.Name), r.Account, strings.TrimSpace(r.Cid), strings.TrimSpace(r.Path)
	l.Local = normLibLocal(stripMediaRoot(r.Local))
	l.StagingCid, l.StagingPath = strings.TrimSpace(r.StagingCid), strings.TrimSpace(r.StagingPath)
	if l.StagingCid == "" {
		l.StagingPath = ""
	}
	l.SchemeID, l.Depth, l.Scrape = r.SchemeID, r.Depth, strings.TrimSpace(r.Scrape)
	if r.Upload != nil {
		l.Upload = *r.Upload
	}
	if l.SchemeID != 0 {
		l.Depth = 0 // 有分类方案时片目层级按方案认
	}
}

// stripMediaRoot 用户把媒体根也写进来了（/media/电影）：去掉，只留相对路径
func stripMediaRoot(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	root := strings.TrimRight(strings.ReplaceAll(mediaRoot(), `\`, "/"), "/")
	if root != "" && (p == root || strings.HasPrefix(p, root+"/")) {
		return strings.TrimPrefix(p, root)
	}
	return p
}

// checkMediaLibChange 一个库改成这样行不行（除了 validateLibraries 的整组校验之外的）
func checkMediaLibChange(old *model.Library, next model.Library, accounts []acctOption) error {
	known := false
	for _, a := range accounts {
		if a.ID == next.Account {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("账号 %d 不存在（可能已经删除）", next.Account)
	}
	if next.SchemeID != 0 {
		if _, ok := schemeByID(next.SchemeID); !ok {
			return errors.New("选的分类方案不存在（可能刚被删除），刷新后重选")
		}
	}
	if old != nil && libLocalKey(old.Local) != libLocalKey(next.Local) && ledgerHasLib(old.Local) {
		return fmt.Errorf("媒体库「%s」本地已经有同步的内容了，本地目录不能再改（改了旧 STRM 没人管，Emby 里的条目也会全部换掉）。要换位置，先删掉这个库、清理本地残留，再新建一个", old.Name)
	}
	if old != nil && (old.Account != next.Account) && ledgerHasLib(old.Local) {
		return fmt.Errorf("媒体库「%s」已经有同步的内容了，不能换账号：删掉这个库再新建一个", old.Name)
	}
	return nil
}

// saveMediaLibs 校验并整份存下（带写回老配置），返回出错原因
func (h *Handler) saveMediaLibs(libs []model.Library) error {
	sortLibraries(libs)
	if err := validateLibraries(libs); err != nil {
		return err
	}
	tx := h.DB.Begin()
	if _, err := saveLibrariesTx(tx, libs); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	forgetLibraries()
	forgetMediaRootCheck()
	if err := h.writeLegacyLibConfigs(loadLibraries()); err != nil {
		log.Printf("[媒体库] ○ 同步老配置失败（媒体库表已存好，老页面可能显示旧值）: %v", err)
	}
	return nil
}

// writeLegacyLibConfigs 媒体库表 → full / lib_opts / acct_libs（给还读老配置的代码）
func (h *Handler) writeLegacyLibConfigs(libs []model.Library) error {
	if h.Config == nil {
		return nil
	}
	m := map[string]interface{}{}
	if raw := h.settingValueRaw("full"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
		if m == nil {
			m = map[string]interface{}{}
		}
	}
	var extras []libRootEntry
	first := true
	m["cid"], m["cid_path"] = "", ""
	for _, l := range libs {
		if l.Account != acctMain {
			continue
		}
		if first {
			m["cid"], m["cid_path"], first = l.Cid, l.Path, false
			continue
		}
		extras = append(extras, libRootEntry{Cid: l.Cid, Path: l.Path})
	}
	if extras == nil {
		extras = []libRootEntry{}
	}
	m["extra_libs"] = extras
	m["local_path"] = mediaRoot()
	b, _ := json.Marshal(m)
	if err := h.Config.SaveSetting("full", string(b)); err != nil {
		return err
	}

	opts := loadLibOpts()
	opts.Libs = map[string]libOpt{}
	opts.Fallback = ""
	for _, l := range libs {
		o := libOpt{Local: l.Local, Scrape: l.Scrape}
		if l.StagingCid == "" && l.SchemeID == 0 {
			// 只同步、不分类的库 = 老口径的「我自己整理」：本站不往里放东西，片目层级用户声明
			o.Mode, o.Depth = libModeSelf, max(l.Depth, 1)
		} else if opts.Fallback == "" && l.Account == acctMain && l.StagingCid != "" {
			opts.Fallback = l.Cid
		}
		opts.Libs[libKeyOf(l)] = o
	}
	b, _ = json.Marshal(opts)
	if err := h.Config.SaveSetting(libOptsKey, string(b)); err != nil {
		return err
	}

	acct := []acctLibEntry{}
	for _, l := range libs {
		if l.Account != acctMain {
			acct = append(acct, acctLibEntry{Account: l.Account, Cid: l.Cid, Path: l.Path, Local: l.Local})
		}
	}
	b, _ = json.Marshal(acct)
	return h.Config.SaveSetting(acctLibsKey, string(b))
}

// ---- 接口 ----

// ListMediaLibs GET /media-libs
func (h *Handler) ListMediaLibs(c *gin.Context) {
	c.JSON(http.StatusOK, h.mediaLibsNow())
}

// CreateMediaLib POST /media-libs
func (h *Handler) CreateMediaLib(c *gin.Context) {
	var req mediaLibReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var l model.Library
	req.applyTo(&l)
	if l.Scrape == "" {
		l.Scrape = libScrapeManual
	}
	if err := checkMediaLibChange(nil, l, accountOptions()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	libs := append(append([]model.Library{}, loadLibraries()...), l)
	libs[len(libs)-1].Sort = len(libs)
	if err := h.saveMediaLibs(libs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[媒体库] ✓ 新建媒体库「%s」：%s → %s", l.Name, firstNonEmpty(l.Path, l.Cid), l.Local)
	c.JSON(http.StatusOK, h.mediaLibsNow())
}

// libBatchMax 一次批量最多建几个库：一个上级目录下勾子目录，几十个已经很多了；
// 每个要建转存文件夹的库最多要发几次 115 请求（逐段列目录、mkdir，都过节流）
const libBatchMax = 30

// batchEnsureStaging 按路径找到或建出转存文件夹，返回 cid。测试里换掉它，不碰 115
var batchEnsureStaging = func(h *Handler, acct uint, path string) (string, error) {
	ops, err := h.opsFor(acct)
	if err != nil {
		return "", err
	}
	return ops.ensurePath("0", strings.Trim(path, "/"))
}

// CreateMediaLibsBatch POST /media-libs/batch {libs:[…]}：一次建好几个库（选一个上级目录、勾它下面的子目录）。
// 转存文件夹只给路径（staging_path、staging_cid 空）的，按路径找，没有就建（界面默认填 /最近接收/库名）。
// 先不带转存文件夹把整组校验一遍再建目录：名字重了、目录互相包含这类错不该先在网盘上建出一堆空文件夹；
// 建完带上转存文件夹再校验一次、一次存下，全成或全不成
func (h *Handler) CreateMediaLibsBatch(c *gin.Context) {
	var req struct {
		Libs []mediaLibReq `json:"libs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Libs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要添加的媒体库"})
		return
	}
	if len(req.Libs) > libBatchMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多添加 %d 个媒体库", libBatchMax)})
		return
	}
	accounts := accountOptions()
	cur := loadLibraries()
	adds := make([]model.Library, len(req.Libs))
	for i, r := range req.Libs {
		r.applyTo(&adds[i])
		if adds[i].Scrape == "" {
			adds[i].Scrape = libScrapeManual
		}
		if err := checkMediaLibChange(nil, adds[i], accounts); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("「%s」：%v", adds[i].Name, err)})
			return
		}
	}
	combine := func(withStaging bool) []model.Library {
		libs := append([]model.Library{}, cur...)
		for i, l := range adds {
			if !withStaging && strings.TrimSpace(req.Libs[i].StagingCid) == "" {
				l.StagingCid, l.StagingPath = "", ""
			}
			l.Sort = len(libs) + 1
			libs = append(libs, l)
		}
		return libs
	}
	if err := validateLibraries(combine(false)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for i, r := range req.Libs {
		p := strings.TrimSpace(r.StagingPath)
		if strings.TrimSpace(r.StagingCid) != "" || strings.Trim(p, "/") == "" {
			continue
		}
		cid, err := batchEnsureStaging(h, adds[i].Account, p)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("「%s」的转存文件夹 %s 建不出来：%v", adds[i].Name, p, err)})
			return
		}
		adds[i].StagingCid, adds[i].StagingPath = cid, "/"+strings.Trim(p, "/")
	}
	if err := h.saveMediaLibs(combine(true)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, l := range adds {
		log.Printf("[媒体库] ✓ 新建媒体库「%s」（批量）：%s → %s", l.Name, firstNonEmpty(l.Path, l.Cid), l.Local)
	}
	c.JSON(http.StatusOK, h.mediaLibsNow())
}

// UpdateMediaLib PUT /media-libs/:id
func (h *Handler) UpdateMediaLib(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req mediaLibReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	libs := append([]model.Library{}, loadLibraries()...)
	idx := -1
	for i := range libs {
		if libs[i].ID == uint(id) {
			idx = i
		}
	}
	if idx < 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "这个媒体库不存在（可能已经删除），刷新后再试"})
		return
	}
	old := libs[idx]
	next := old
	req.applyTo(&next)
	if err := checkMediaLibChange(&old, next, accountOptions()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	libs[idx] = next
	if err := h.saveMediaLibs(libs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[媒体库] ✓ 修改媒体库「%s」", next.Name)
	c.JSON(http.StatusOK, h.mediaLibsNow())
}

// DeleteMediaLib DELETE /media-libs/:id —— 只删配置：网盘不动，本地 STRM 与台账留着，
// 由「本地残留」提示让用户决定清不清（libleft.go）
func (h *Handler) DeleteMediaLib(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	libs := loadLibraries()
	keep := make([]model.Library, 0, len(libs))
	var gone *model.Library
	for i := range libs {
		if libs[i].ID == uint(id) {
			gone = &libs[i]
			continue
		}
		keep = append(keep, libs[i])
	}
	if gone == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "这个媒体库不存在（可能已经删除），刷新后再试"})
		return
	}
	if err := h.saveMediaLibs(keep); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[媒体库] ✓ 删除媒体库「%s」（网盘与本地文件都没动）", gone.Name)
	c.JSON(http.StatusOK, h.mediaLibsNow())
}

// DismissMediaLibNotice POST /media-libs/notice/dismiss
func (h *Handler) DismissMediaLibNotice(c *gin.Context) {
	if h.Config != nil {
		_ = h.Config.SaveSetting(libsV2NoticeKey, "")
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
