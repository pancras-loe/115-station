package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

// ==================== 一个库一套整理配置（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.1 / §10.2 / §10.5） ====================
//
// 放进哪个库的转存/待整理文件夹，就整理进哪个库、按这个库选的分类方案分目录（没选就直接放进库目录下）。
// 所以整理按库跑：每个设了转存文件夹的库一套 OrgConfig、一条它所在账号的通道、一个落盘出口。
//
// 已存在 / 冗余：设置上一套，网盘上每个账号各一份（115 不能跨账号移动文件）。主号用 org-basic 里配的那两个目录；
// 别的账号在同一路径下各建一份（没有就建），建好的 cid 记在 setting acct_ws。
//
// 整理前校验（§10.5）：库没设转存文件夹的只同步不整理（不是错）；方案被删、账号没登录、已存在 / 冗余建不出来的，
// 这个库这一轮不整理，转存文件夹里的条目各记一条失败、推一条消息，用户改好之后在整理记录页点「重新整理」。

// errLibNoStaging 库没设转存文件夹（只同步不整理）
var errLibNoStaging = errors.New("这个媒体库没设转存/待整理文件夹，只同步不整理")

// organizeLibs 要整理的库（设了转存文件夹的）
func organizeLibs(libs []model.Library) []model.Library {
	var out []model.Library
	for _, l := range libs {
		if strings.TrimSpace(l.StagingCid) != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---- 每个账号的已存在 / 冗余 ----

const acctWorkspaceKey = "acct_ws"

// acctWS 一个账号的已存在 / 冗余目录 cid
type acctWS struct {
	Existing  string `json:"existing"`
	Redundant string `json:"redundant"`
}

// 默认路径：主号没配已存在 / 冗余时，别的账号按这个建（同「一键创建工作目录」的落点）
const (
	defaultExistingPath  = "/StrmStation/已存在"
	defaultRedundantPath = "/StrmStation/冗余"
)

var acctWSMu sync.Mutex

// accountWorkspace 某个账号的已存在 / 冗余目录 cid。主号读 org-basic；别的账号按主号那两个目录的路径在自己网盘里建一份
func (h *Handler) accountWorkspace(acct uint) (existing, redundant string, err error) {
	basic := h.getSettingValue("org-basic")
	mainEx, mainRe := jsonStrField(basic, "existing"), jsonStrField(basic, "redundant")
	if acct == acctMain {
		if mainEx == "" || mainRe == "" {
			return "", "", errors.New("还没设已存在 / 冗余文件夹（自动整理 → 基础配置，或账号与媒体库页一键创建）")
		}
		return mainEx, mainRe, nil
	}
	acctWSMu.Lock()
	defer acctWSMu.Unlock()
	all := map[string]acctWS{}
	_ = json.Unmarshal([]byte(h.getSettingValue(acctWorkspaceKey)), &all)
	key := fmt.Sprint(acct)
	if ws := all[key]; ws.Existing != "" && ws.Redundant != "" {
		return ws.Existing, ws.Redundant, nil
	}
	// 路径与主号一致（「设置上一套」）；主号没配就用默认路径
	exPath, rePath := defaultExistingPath, defaultRedundantPath
	if ck, e := h.get115Cookie(); e == nil && ck != "" {
		if mainEx != "" {
			if p := absPathOf(ck, mainEx); p != "" {
				exPath = p
			}
		}
		if mainRe != "" {
			if p := absPathOf(ck, mainRe); p != "" {
				rePath = p
			}
		}
	}
	ops, err := h.opsFor(acct)
	if err != nil {
		return "", "", err
	}
	ex, err := ops.ensurePath("0", strings.Trim(exPath, "/"))
	if err != nil {
		return "", "", fmt.Errorf("在账号 %d 里建已存在文件夹 %s 失败: %w", acct, exPath, err)
	}
	re, err := ops.ensurePath("0", strings.Trim(rePath, "/"))
	if err != nil {
		return "", "", fmt.Errorf("在账号 %d 里建冗余文件夹 %s 失败: %w", acct, rePath, err)
	}
	all[key] = acctWS{Existing: ex, Redundant: re}
	b, _ := json.Marshal(all)
	if h.Config != nil {
		if err := h.Config.SaveSetting(acctWorkspaceKey, string(b)); err != nil {
			log.Printf("[整理] ○ 记下账号 %d 的已存在 / 冗余目录失败（下次再查一遍）: %v", acct, err)
		}
	}
	log.Printf("[整理] ✓ 账号 %d 的已存在 / 冗余目录：%s / %s", acct, exPath, rePath)
	return ex, re, nil
}

// ---- 一个库的整理配置 ----

// orgConfigFor 一个库的整理配置。只读校验（方案在不在、工作目录配没配），不列转存文件夹
func (h *Handler) orgConfigFor(lib model.Library, libs []model.Library) (*OrgConfig, error) {
	if strings.TrimSpace(lib.StagingCid) == "" {
		return nil, errLibNoStaging
	}
	var cfg OrgConfig
	if v := h.getSettingValue("org-basic"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	cfg.MinSize = loadRecognizeConfig().MinSize
	if lib.SchemeID != 0 {
		if _, ok := schemeByID(lib.SchemeID); !ok {
			return nil, fmt.Errorf("媒体库「%s」选的分类方案已经删除了：到「账号与媒体库 → 媒体库」重新选一套（或选不分类）", lib.Name)
		}
		cfg.SchemeID = lib.SchemeID
	} else {
		cfg.Flat = true
	}
	ex, re, err := h.accountWorkspace(lib.Account)
	if err != nil {
		return nil, fmt.Errorf("媒体库「%s」：%v", lib.Name, err)
	}
	cfg.Existing, cfg.Redundant = ex, re
	cfg.Pending, cfg.ShareCid = lib.StagingCid, lib.StagingCid
	cfg.Library, cfg.Fallback = lib.Cid, lib.Cid
	cfg.LibID, cfg.Account, cfg.PinLib, cfg.LibLocal = lib.ID, lib.Account, true, normLibLocal(lib.Local)
	// 同一个账号里别的库与它们的转存文件夹：保护子树（不当条目、不被空目录清理）
	for _, o := range libs {
		if o.ID == lib.ID || o.Account != lib.Account {
			continue
		}
		cfg.OtherLibs = append(cfg.OtherLibs, o.Cid)
		if o.StagingCid != "" {
			cfg.OtherLibs = append(cfg.OtherLibs, o.StagingCid)
		}
	}
	return &cfg, nil
}

// defaultOrgConfig 只要「一套配置」的老调用方用：主号第一个要整理的库
func (h *Handler) defaultOrgConfig(libs []model.Library) (*OrgConfig, error) {
	for _, l := range organizeLibs(libs) {
		if l.Account == acctMain {
			return h.orgConfigFor(l, libs)
		}
	}
	for _, l := range organizeLibs(libs) {
		return h.orgConfigFor(l, libs)
	}
	return nil, errors.New("没有要整理的媒体库：到「账号与媒体库 → 媒体库」给库设转存/待整理文件夹")
}

// ---- 整理记录 → 库 ----

// orgLibOfRecord 一条整理记录属于哪个库：新记录看 LibID；老记录按 LibCid（空 = 首个库）
func orgLibOfRecord(rec *model.OrganizeRecord, libs []model.Library) (model.Library, bool) {
	if rec.LibID != 0 {
		for _, l := range libs {
			if l.ID == rec.LibID {
				return l, true
			}
		}
		return model.Library{}, false
	}
	key := strings.TrimSpace(rec.LibCid)
	for _, l := range libs {
		if key == "" && l.Account == acctMain {
			return l, true // 老记录 LibCid 记空 = 首个库（主号排第一个的库）
		}
		if key != "" && libKeyOf(l) == key {
			return l, true
		}
	}
	// 还没入过库（未识别 / 失败 / 待确认）：看来源在哪个库的转存文件夹里
	if src := strings.TrimSpace(rec.SourceCid); src != "" {
		for _, l := range libs {
			if l.StagingCid == src {
				return l, true
			}
		}
	}
	return model.Library{}, false
}

// orgConfigForRecord 处理一条记录（确认入库、忽略、重新整理）用的配置与通道。
// 媒体库表还空（迁移前、测试）时退回老口径
func (h *Handler) orgConfigForRecord(rec *model.OrganizeRecord) (*OrgConfig, *pan115Ops, error) {
	libs := loadLibraries()
	if len(libs) == 0 {
		cfg, err := h.loadOrgConfigLegacy()
		if err != nil {
			return nil, nil, err
		}
		ops, err := h.newPan115Ops()
		return cfg, ops, err
	}
	lib, ok := orgLibOfRecord(rec, libs)
	if !ok {
		return nil, nil, errors.New("认不出这条记录属于哪个媒体库（库可能已删除，或来源不在任何库的转存文件夹里）：到「账号与媒体库 → 媒体库」设好转存文件夹后再试")
	}
	cfg, err := h.orgConfigFor(lib, libs)
	if err == errLibNoStaging {
		// 只同步的库也允许对已入库的片重新整理 / 确认（用户点名要的）：转存文件夹没有就拿库目录本身当扫描根以外的占位
		cfg, err = h.orgConfigForSyncOnly(lib, libs)
	}
	if err != nil {
		return nil, nil, err
	}
	ops, err := h.opsFor(lib.Account)
	return cfg, ops, err
}

// orgConfigForSyncOnly 没设转存文件夹的库在重新整理时用：工作目录照常，扫描根留空
func (h *Handler) orgConfigForSyncOnly(lib model.Library, libs []model.Library) (*OrgConfig, error) {
	tmp := lib
	tmp.StagingCid = "-"
	cfg, err := h.orgConfigFor(tmp, libs)
	if err != nil {
		return nil, err
	}
	cfg.Pending, cfg.ShareCid = "", ""
	return cfg, nil
}

// libOfStagingCid 哪个库的转存文件夹是这个目录
func libOfStagingCid(libs []model.Library, cid string) (model.Library, bool) {
	for _, l := range libs {
		if l.StagingCid != "" && l.StagingCid == cid {
			return l, true
		}
	}
	return model.Library{}, false
}

// libOfChain 网盘文件页的面包屑（主号，祖先 cid 从根往下）落在哪个库：
// 在库目录里 → 那个库；在某个库的转存文件夹里 → 那个库（ok 第二个返回值说是哪种）
func libOfChain(libs []model.Library, acct uint, chain []string) (lib model.Library, inStaging bool, ok bool) {
	for i := len(chain) - 1; i >= 0; i-- {
		for _, l := range libs {
			if l.Account != acct {
				continue
			}
			if l.Cid == chain[i] {
				return l, false, true
			}
			if l.StagingCid != "" && l.StagingCid == chain[i] {
				return l, true, true
			}
		}
	}
	return model.Library{}, false, false
}

// ---- 一轮整理按库跑 ----

// orgRun 一个库这一轮整理用的配置与通道
type orgRun struct {
	lib model.Library
	cfg *OrgConfig
	ops *pan115Ops
}

// organizeRuns 这一轮要整理的库。校验不过的库（方案被删、账号没登录、已存在 / 冗余建不出来）不整理：
// 它转存文件夹里的条目各记一条失败、推一条消息（§10.5），problems 里带原因给任务结果。
// 媒体库表还空（迁移前、测试）时退回老口径的一套配置（lib 为零值）
func (h *Handler) organizeRuns() (runs []orgRun, problems []string, err error) {
	libs := loadLibraries()
	if len(libs) == 0 {
		cfg, err := h.loadOrgConfigLegacy()
		if err != nil {
			return nil, nil, err
		}
		ops, err := h.newPan115Ops()
		if err != nil {
			return nil, nil, err
		}
		ops.suppress = true
		return []orgRun{{cfg: cfg, ops: ops}}, nil, nil
	}
	targets := organizeLibs(libs)
	if len(targets) == 0 {
		return nil, nil, errors.New("没有要整理的媒体库：到「账号与媒体库 → 媒体库」给库设转存/待整理文件夹")
	}
	for _, lib := range targets {
		cfg, cerr := h.orgConfigFor(lib, libs)
		var ops *pan115Ops
		if cerr == nil {
			ops, cerr = h.opsFor(lib.Account)
		}
		if cerr != nil {
			problems = append(problems, cerr.Error())
			h.failLibStaging(lib, cerr.Error())
			continue
		}
		ops.suppress = true // 整理自产的网盘变更不该再被增量同步处理一遍
		runs = append(runs, orgRun{lib: lib, cfg: cfg, ops: ops})
	}
	if msg := h.orphanStagingProblem(libs); msg != "" {
		problems = append(problems, msg)
	}
	return runs, problems, nil
}

// libFailSeen 校验不过的库：同一个条目同一个原因 6 小时内只记一次、只推一次（定时整理每轮都会再撞一次）
var libFailSeen = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// failLibStaging 库没法整理：转存文件夹里的条目各记一条失败（用户改好后在整理记录页点「重新整理」），推一条消息。
// 账号没登录时列不了它的转存文件夹，只推消息
func (h *Handler) failLibStaging(lib model.Library, reason string) {
	now := time.Now()
	libFailSeen.Lock()
	for k, t := range libFailSeen.at {
		if now.Sub(t) > orgOutcomeQuiet {
			delete(libFailSeen.at, k)
		}
	}
	key := fmt.Sprintf("lib:%d|%s", lib.ID, reason)
	_, seen := libFailSeen.at[key]
	libFailSeen.at[key] = now
	libFailSeen.Unlock()

	var entries []dirEntry
	if ops, err := h.opsFor(lib.Account); err == nil {
		entries, _ = listPendingTopLevel(ops, lib.StagingCid)
	}
	n := 0
	for _, e := range entries {
		if model.DB == nil {
			break
		}
		var cnt int64
		model.DB.Model(&model.OrganizeRecord{}).
			Where("source_fid = ? AND status = ? AND stage = ? AND message = ? AND created_at > ?",
				e.Fid, "failed", "config", reason, now.Add(-orgOutcomeQuiet)).Count(&cnt)
		if cnt > 0 {
			continue
		}
		kind := "file"
		if e.IsDir {
			kind = "dir"
		}
		rec := &model.OrganizeRecord{Source: e.Name, SourceFid: e.Fid, SourceKind: kind, SourceCid: lib.StagingCid,
			Status: "failed", Stage: "config", Message: reason, LibID: lib.ID, Account: lib.Account}
		if id, _ := currentJob(); id != 0 {
			rec.JobID = id
		}
		if model.DB.Create(rec).Error == nil {
			n++
		}
	}
	log.Printf("[整理] ✗ 媒体库「%s」这一轮不整理：%s（记了 %d 条失败）", lib.Name, reason, n)
	if !seen {
		go NotifyMessage(kindOrganize, "⚠️ 媒体库没法整理", fmt.Sprintf("%s\n改好之后到「任务中心 → 整理记录」对失败的条目点「重新整理」，或等下一轮自动整理", reason))
	}
}

// orphanStagingProblem 老配置的转存目录（share.folder）不是任何库的转存文件夹：那里的东西没人整理了。
// 只提示不记失败（条目没有库可归，重新整理也认不出），提醒到库上设转存文件夹
func (h *Handler) orphanStagingProblem(libs []model.Library) string {
	cid := h.shareFolderCid()
	if cid == "" {
		return ""
	}
	if _, ok := libOfStagingCid(libs, cid); ok {
		return ""
	}
	for _, l := range libs {
		if l.Account == acctMain && l.Cid == cid {
			return ""
		}
	}
	msg := "原来的转存/待整理目录没有设给任何媒体库，里面的内容不会再整理：到「账号与媒体库 → 媒体库」把它设成某个库的转存文件夹"
	libFailSeen.Lock()
	_, seen := libFailSeen.at["orphan|"+cid]
	libFailSeen.at["orphan|"+cid] = time.Now()
	libFailSeen.Unlock()
	if !seen {
		log.Printf("[整理] ○ %s", msg)
	}
	return msg
}

// orgDirAbs 整理里查一个目录的网盘绝对路径：主号走路径缓存，别的账号现查、不写缓存
// （PathCache 按路径查时不分账号，两个账号都有「/影视」会串）
func orgDirAbs(ops *pan115Ops, cid string) string {
	if ops == nil || ops.cookie == "" {
		return ""
	}
	if ops.acct == acctMain {
		return absPathOf(ops.cookie, cid)
	}
	p, err := acctDirAbs(ops.cookie, cid)
	if err != nil {
		return ""
	}
	return p
}

// orgConfigForChain 网盘文件页（主号）整理所选条目用的配置：在某个库的转存文件夹里 → 那个库；
// 在库目录里（指定季集、片目整理）→ 那个库（没设转存文件夹也行）；别的位置（用户随手建的目录）→ 主号第一个要整理的库
func (h *Handler) orgConfigForChain(chain []browseCrumb) (*OrgConfig, error) {
	libs := loadLibraries()
	if len(libs) == 0 {
		return h.loadOrgConfigLegacy()
	}
	cids := make([]string, 0, len(chain))
	for _, c := range chain {
		cids = append(cids, c.Cid)
	}
	if lib, _, ok := libOfChain(libs, acctMain, cids); ok {
		cfg, err := h.orgConfigFor(lib, libs)
		if err == errLibNoStaging {
			cfg, err = h.orgConfigForSyncOnly(lib, libs)
		}
		return cfg, err
	}
	return h.defaultOrgConfig(libs)
}

// organizeReady 手动整理入队前的检查：有没有要整理的库（不列网盘、不记失败）
func (h *Handler) organizeReady() error {
	libs := loadLibraries()
	if len(libs) == 0 {
		_, err := h.loadOrgConfigLegacy()
		return err
	}
	if len(organizeLibs(libs)) == 0 {
		return errors.New("没有要整理的媒体库：到「账号与媒体库 → 媒体库」给库设转存/待整理文件夹")
	}
	return nil
}

// allOrgProtectedCids 所有库的工作区目录（深度删除清空目录时的守卫用）：主号的库、转存文件夹、已存在 / 冗余
func (h *Handler) allOrgProtectedCids() []string {
	libs := loadLibraries()
	if len(libs) == 0 {
		if cfg, err := h.loadOrgConfigLegacy(); err == nil {
			return orgProtectedCids(cfg)
		}
		return nil
	}
	var out []string
	for _, l := range libs {
		out = append(out, l.Cid)
		if l.StagingCid != "" {
			out = append(out, l.StagingCid)
		}
	}
	basic := h.getSettingValue("org-basic")
	out = append(out, jsonStrField(basic, "existing"), jsonStrField(basic, "redundant"))
	all := map[string]acctWS{}
	_ = json.Unmarshal([]byte(h.getSettingValue(acctWorkspaceKey)), &all)
	for _, ws := range all {
		out = append(out, ws.Existing, ws.Redundant)
	}
	return out
}

// watchDir 守望者要看的一个转存文件夹
type watchDir struct {
	cid  string
	ops  *pan115Ops
	subs map[string]stagingLib // 绑定了媒体库的子目录（老口径才有）
}

// watchStagingDirs 守望者要看的转存文件夹：每个要整理、校验也过得去的库一个（校验不过的库整理会记失败，
// 守望者再数它的条目只会一路计数到熔断）。媒体库表还空时是老的那一个
func (h *Handler) watchStagingDirs() []watchDir {
	libs := loadLibraries()
	if len(libs) == 0 {
		cid := h.shareFolderCid()
		if cid == "" {
			return nil
		}
		ops, err := h.newPan115Ops()
		if err != nil {
			return nil
		}
		return []watchDir{{cid: cid, ops: ops, subs: stagingLibMap(loadStagingLibs())}}
	}
	var out []watchDir
	for _, l := range organizeLibs(libs) {
		if _, err := h.orgConfigFor(l, libs); err != nil {
			continue
		}
		ops, err := h.opsFor(l.Account)
		if err != nil {
			continue
		}
		out = append(out, watchDir{cid: l.StagingCid, ops: ops})
	}
	return out
}
