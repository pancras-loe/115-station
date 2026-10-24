package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

// ==================== 媒体库表（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.1） ====================
//
// 一个库一条（model.Library）：名称 → 115 网盘目录（连同账号）→ 本地目录 → 转存/待整理文件夹 → 分类方案。
// 取代原来散在 setting full.cid / full.extra_libs / acct_libs / lib_opts / staging_libs 里的那几份配置。
//
// 老部署第一次启动时由 MigrateLibrariesV2 换算过来：网盘目录、本地路径一个都不改（同步、播放不断），
// 转存文件夹不自动设（原来是一个转存目录靠分类规则的 library 分库，换不成「一个库一个文件夹」，
// 让用户自己设，没设的库整理前校验不过、提醒用户，MULTI-ACCOUNT-V2-PLAN.md §10.6）。
//
// 读库一律走 loadLibraries（有缓存），改完 forgetLibraries。

var (
	libStoreMu sync.Mutex
	libStore   []model.Library
	libStoreAt time.Time
	libStoreDB *gorm.DB // 缓存是哪个库读出来的：换了数据库（测试、恢复备份后重启）不能沿用
)

func forgetLibraries() {
	libStoreMu.Lock()
	libStore, libStoreAt = nil, time.Time{}
	libStoreMu.Unlock()
}

// loadLibraries 全部媒体库（按 Sort、ID）。30 秒缓存兜底，正常靠保存时 forgetLibraries
func loadLibraries() []model.Library {
	libStoreMu.Lock()
	defer libStoreMu.Unlock()
	if libStore != nil && libStoreDB == model.DB && time.Since(libStoreAt) < 30*time.Second {
		return libStore
	}
	var out []model.Library
	if model.DB != nil {
		model.DB.Order("sort, id").Find(&out)
	}
	if out == nil {
		out = []model.Library{}
	}
	libStore, libStoreAt, libStoreDB = out, time.Now(), model.DB
	return out
}

// libraryByID 按 id 找库
func libraryByID(id uint) (model.Library, bool) {
	for _, l := range loadLibraries() {
		if l.ID == id {
			return l, true
		}
	}
	return model.Library{}, false
}

// normLibLocal 本地目录归一：媒体根下的相对路径，斜杠统一、去掉首尾斜杠与空段
func normLibLocal(raw string) string {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/")
	var segs []string
	for _, s := range strings.Split(raw, "/") {
		if s = strings.TrimSpace(s); s != "" {
			segs = append(segs, s)
		}
	}
	return strings.Join(segs, "/")
}

// libLocalContains a 是否等于 b 或在 b 之内（大小写不敏感：Windows / SMB 挂载下大小写不同也是同一个目录）
func libLocalContains(b, a string) bool {
	a, b = libLocalKey(a), libLocalKey(b)
	return a == b || strings.HasPrefix(a, b+"/")
}

// panPathContains 网盘路径 a 是否等于 b 或在 b 之内
func panPathContains(b, a string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/")
}

// validateLibraries 一组库能不能这样存（纯函数）。网盘路径用库里记下的 Path / StagingPath 比，认不出（空）的不比
func validateLibraries(libs []model.Library) error {
	names := map[string]string{}
	keys := map[string]string{}
	for i := range libs {
		l := &libs[i]
		l.Name = strings.TrimSpace(l.Name)
		l.Local = normLibLocal(l.Local)
		l.Cid, l.StagingCid = strings.TrimSpace(l.Cid), strings.TrimSpace(l.StagingCid)
		if l.Name == "" {
			return fmt.Errorf("第 %d 个媒体库没有名称", i+1)
		}
		if len([]rune(l.Name)) > 50 {
			return fmt.Errorf("媒体库名称「%s」太长了（最多 50 个字）", l.Name)
		}
		if prev, ok := names[strings.ToLower(l.Name)]; ok {
			return fmt.Errorf("两个媒体库都叫「%s」（%s）", l.Name, prev)
		}
		names[strings.ToLower(l.Name)] = l.Name
		if l.Cid == "" || l.Cid == "0" {
			return fmt.Errorf("媒体库「%s」要选一个具体的 115 目录，不能是网盘根", l.Name)
		}
		k := fmt.Sprintf("%d:%s", l.Account, l.Cid)
		if prev, ok := keys[k]; ok {
			return fmt.Errorf("媒体库「%s」和「%s」选的是同一个 115 目录", l.Name, prev)
		}
		keys[k] = l.Name
		if l.Local == "" {
			return fmt.Errorf("媒体库「%s」没有本地目录", l.Name)
		}
		for _, seg := range strings.Split(l.Local, "/") {
			if err := checkLibLocalName(seg); err != nil {
				return fmt.Errorf("媒体库「%s」的本地目录：%v", l.Name, err)
			}
		}
		if l.Depth < 0 || l.Depth > libOptDepthMax {
			return fmt.Errorf("媒体库「%s」的片目层级只能是 1–%d 层", l.Name, libOptDepthMax)
		}
		if l.Scrape != "" && !validLibScrape(l.Scrape) {
			return fmt.Errorf("媒体库「%s」的刮削方式「%s」不认识", l.Name, l.Scrape)
		}
		if l.StagingCid != "" && (l.StagingCid == "0" || l.StagingCid == l.Cid) {
			return fmt.Errorf("媒体库「%s」的转存文件夹不能是网盘根，也不能是媒体库目录本身", l.Name)
		}
	}
	for i := range libs {
		for j := range libs {
			if i == j {
				continue
			}
			a, b := libs[i], libs[j]
			if i < j && (libLocalContains(a.Local, b.Local) || libLocalContains(b.Local, a.Local)) {
				return fmt.Errorf("媒体库「%s」（%s）和「%s」（%s）的本地目录互相包含：每个库要有自己单独的本地目录", a.Name, a.Local, b.Name, b.Local)
			}
			if a.Account != b.Account {
				continue
			}
			if i < j && (panPathContains(a.Path, b.Path) || panPathContains(b.Path, a.Path)) {
				return fmt.Errorf("媒体库「%s」和「%s」的 115 目录互相包含", a.Name, b.Name)
			}
			if a.StagingCid != "" {
				if a.StagingCid == b.Cid || panPathContains(b.Path, a.StagingPath) {
					return fmt.Errorf("媒体库「%s」的转存文件夹在「%s」的 115 目录里面", a.Name, b.Name)
				}
				if i < j && b.StagingCid != "" && (a.StagingCid == b.StagingCid ||
					panPathContains(a.StagingPath, b.StagingPath) || panPathContains(b.StagingPath, a.StagingPath)) {
					return fmt.Errorf("媒体库「%s」和「%s」的转存文件夹是同一个或互相包含：每个库要有自己的转存文件夹", a.Name, b.Name)
				}
			}
		}
		if libs[i].StagingCid != "" && panPathContains(libs[i].Path, libs[i].StagingPath) {
			return fmt.Errorf("媒体库「%s」的转存文件夹在它自己的 115 目录里面：整理会把自己整理进自己", libs[i].Name)
		}
	}
	return nil
}

// ==================== 老配置迁移 ====================

const libsV2MigrateKey = "migrate.libraries.v2"

// libsV2NoticeKey 迁移时给用户看的提示（转存文件夹要重设、原来的分库方式），库页面上显示到用户点「知道了」
const libsV2NoticeKey = "libraries_v2_notice"

// libsV2Notice 迁移提示
type libsV2Notice struct {
	At    int64    `json:"at"`
	Lines []string `json:"lines"`
}

// libsV2In 迁移的输入（纯函数用）
type libsV2In struct {
	FullRaw    string
	OptsRaw    string
	AcctRaw    string
	StagingRaw string
	ShareRaw   string
	YAML       string // 老的分类 YAML（看原来哪些规则写了 library）
	Legacy     string // 没设过刮削方式的库按老开关换算出的值
	SchemeID   uint   // 方案「默认」：原来本站整理的库选它，片目层级照旧按分类认
	// name 一个主号库的本地第一层（libNameSources 的结果，认不出再问一次 115）；空 = 认不出
	name func(cid string) string
	// shown 一个目录的显示路径（缓存或配置里存的）
	shown func(cid string) string
}

// planLibrariesV2 老配置 → 媒体库表（纯函数）。返回库、给用户的提示、认不出名字的库（这一次不迁，下次启动再试）
func planLibrariesV2(in libsV2In) ([]model.Library, []string, []string) {
	cfg := parseFullCfg(in.FullRaw)
	opts := parseLibOpts(in.OptsRaw)
	shownOf := map[string]string{strings.TrimSpace(cfg.Cid): jsonStrField(in.FullRaw, "cid_path")}
	for _, e := range cfg.ExtraLibs {
		shownOf[strings.TrimSpace(e.Cid)] = e.Path
	}
	var libs []model.Library
	var unresolved []string
	nameByKey := map[string]string{}
	for i, cid := range cfg.libCids() {
		o := opts.of(cid)
		local := strings.TrimSpace(o.Local)
		if local == "" && in.name != nil {
			local = in.name(cid)
		}
		if local == "" {
			unresolved = append(unresolved, cid)
			continue
		}
		p := shownOf[cid]
		if p == "" && in.shown != nil {
			p = in.shown(cid)
		}
		l := model.Library{Name: local, Account: acctMain, Cid: cid, Path: p, Local: local, Scrape: firstNonEmpty(o.Scrape, in.Legacy), Sort: i}
		if o.self() {
			l.Depth = o.depth()
		} else {
			l.SchemeID = in.SchemeID // 片目按原来那份分类认（分类目录没变）
		}
		libs = append(libs, l)
		nameByKey[cid] = local
	}
	for _, e := range parseAcctLibs(in.AcctRaw) {
		o := opts.of(e.key())
		libs = append(libs, model.Library{Name: e.Local, Account: e.Account, Cid: e.Cid, Path: e.Path, Local: e.Local,
			Scrape: firstNonEmpty(o.Scrape, in.Legacy), Depth: o.depth(), Sort: len(libs)})
		nameByKey[e.key()] = e.Local
	}

	// 给用户的提示：转存文件夹改成每个库一个了，原来的分库方式说清楚，照着设
	var lines []string
	if len(libs) > 0 {
		lines = append(lines, "媒体库改成了「一个库一个转存/待整理文件夹」：放进哪个库的转存文件夹，就整理进哪个库。每个库都要单独设转存文件夹，没设的库不会整理（整理会失败并提醒）。")
		if folder := jsonStrField(in.ShareRaw, "folder"); folder != "" {
			p := folder
			if in.shown != nil {
				p = firstNonEmpty(in.shown(folder), folder)
			}
			lines = append(lines, fmt.Sprintf("原来所有库共用的转存目录是「%s」。", p))
		}
		var bindings []stagingLib
		_ = json.Unmarshal([]byte(in.StagingRaw), &bindings)
		for _, b := range bindings {
			if n := nameByKey[strings.TrimSpace(b.Lib)]; n != "" {
				lines = append(lines, fmt.Sprintf("原来转存目录下的子目录「%s」整理进「%s」：可以把它设成「%s」的转存文件夹。", b.Name, n, n))
			}
		}
		if rows, err := parseCategoryYAML(in.YAML); err == nil {
			for _, r := range rows {
				if v := strings.TrimSpace(r.Library); v != "" {
					lib := v
					for k, n := range nameByKey {
						if k == v {
							lib = n
						}
					}
					lines = append(lines, fmt.Sprintf("原来分类规则「%s」写着 library: %s（整理进「%s」）：现在要把这类内容放进「%s」的转存文件夹。", r.Name, v, lib, lib))
				}
			}
		}
		lines = append(lines, "原来的二级分类存成了分类方案「"+defaultSchemeName+"」，原来由本站整理的库都选上了它，可以在「自动整理 → 二级分类」里改。")
	}
	return libs, lines, unresolved
}

// MigrateLibrariesV2 第一次启动时把老配置换算成媒体库表（幂等）。要在 FreezeLibLocalNames 之后跑（本地名已经定下来）。
// 有库认不出名字时只迁认得出的、不打完成标记，下次启动再试（同步还读老配置，不会断）
func (h *Handler) MigrateLibrariesV2() {
	if h.DB == nil || h.Config == nil {
		return
	}
	var done int64
	h.DB.Model(&model.Setting{}).Where("key = ?", libsV2MigrateKey).Count(&done)
	if done > 0 {
		return
	}
	schemeID, err := ensureDefaultScheme(h.DB)
	if err != nil {
		log.Printf("[媒体库] ✗ 迁移：准备分类方案失败，下次启动再试: %v", err)
		return
	}
	var have []model.Library
	h.DB.Find(&have)
	haveKey := map[string]bool{}
	for _, l := range have {
		haveKey[fmt.Sprintf("%d:%s", l.Account, l.Cid)] = true
	}
	var rule model.ScrapeRule
	h.DB.Where("type = ?", "category_config").First(&rule)
	fullRaw := h.getSettingValue("full")
	sources := map[string]libNameSource{}
	for _, s := range legacyLibNameSources(fullRaw) {
		sources[s.Cid] = s
	}
	cookie, _ := h.get115Cookie()
	libs, lines, unresolved := planLibrariesV2(libsV2In{
		FullRaw: fullRaw, OptsRaw: h.getSettingValue(libOptsKey), AcctRaw: h.getSettingValue(acctLibsKey),
		StagingRaw: h.getSettingValue(stagingLibsKey), ShareRaw: h.getSettingValue("share"), YAML: rule.Config,
		Legacy: loadLegacyLibScrape(), SchemeID: schemeID,
		name: func(cid string) string {
			if s, ok := sources[cid]; ok {
				if n := s.libRootOf().Name; n != "" {
					return n
				}
			}
			return libDirName(cookie, cid) // 一次性迁移，认不出时问一次 115
		},
		shown: func(cid string) string {
			if row, ok := lookupCachedRow(cid); ok {
				return row.Path
			}
			return ""
		},
	})
	added := 0
	// 原来的全局「允许上传到 115」只管主号的库（附加账号的库一律不传），换算成每库开关时同口径
	legacyUpload := parseMonitorUploadCfg(h.getSettingValue("monitor")).Enabled
	for _, l := range libs {
		if haveKey[fmt.Sprintf("%d:%s", l.Account, l.Cid)] {
			continue // 上次迁了一半：已经迁过的不重复
		}
		l.Upload = legacyUpload && l.Account == acctMain
		if err := h.DB.Create(&l).Error; err != nil {
			log.Printf("[媒体库] ✗ 迁移媒体库「%s」失败，下次启动再试: %v", l.Name, err)
			return
		}
		added++
	}
	forgetLibraries()
	if len(have) == 0 && len(lines) > 0 {
		b, _ := json.Marshal(libsV2Notice{At: time.Now().Unix(), Lines: lines})
		if err := h.Config.SaveSetting(libsV2NoticeKey, string(b)); err != nil {
			log.Printf("[媒体库] ○ 迁移提示没存上（只影响页面提示）: %v", err)
		}
		for _, line := range lines {
			log.Printf("[媒体库] ○ %s", line)
		}
	}
	if len(unresolved) > 0 {
		log.Printf("[媒体库] ⚠ 迁移：%d 个媒体库（cid=%s）认不出名字，这次没迁，下次启动再试", len(unresolved), strings.Join(unresolved, ","))
		return
	}
	if err := h.DB.Create(&model.Setting{Key: libsV2MigrateKey, Value: "1"}).Error; err != nil {
		log.Printf("[媒体库] ○ 迁移完成标记写入失败（下次启动再跑一遍，结果不变）: %v", err)
	}
	if added > 0 {
		log.Printf("[媒体库] ✓ 已把原来的 %d 个媒体库换算成新的媒体库表，本地路径都没变", added)
	}
}

// ---- 小工具 ----

// sortLibraries 按 Sort、ID 排（保存时重排 Sort 用）
func sortLibraries(libs []model.Library) {
	sort.SliceStable(libs, func(i, j int) bool {
		if libs[i].Sort != libs[j].Sort {
			return libs[i].Sort < libs[j].Sort
		}
		return libs[i].ID < libs[j].ID
	})
}

// libLocalBase 本地目录的最后一段（日志、默认名称用）
func libLocalBase(local string) string { return path.Base(normLibLocal(local)) }

// errNoLibraries 一个媒体库都没有
var errNoLibraries = errors.New("还没有媒体库：到「账号与媒体库 → 媒体库」添加一个")

// saveLibrariesTx 整份替换媒体库表（校验过的），返回存好的（带 id）
func saveLibrariesTx(tx *gorm.DB, libs []model.Library) ([]model.Library, error) {
	keep := map[uint]bool{}
	for i := range libs {
		libs[i].Sort = i
		if libs[i].ID != 0 {
			keep[libs[i].ID] = true
		}
	}
	var old []model.Library
	if err := tx.Find(&old).Error; err != nil {
		return nil, err
	}
	for _, o := range old {
		if !keep[o.ID] {
			if err := tx.Delete(&model.Library{}, o.ID).Error; err != nil {
				return nil, err
			}
		}
	}
	for i := range libs {
		if err := tx.Save(&libs[i]).Error; err != nil {
			return nil, err
		}
	}
	return libs, nil
}

// ==================== 当前生效的媒体库 ====================

// effectiveLibraries 当前生效的媒体库：表里有就用表；表还空（迁移前、测试）按老配置在内存里换算一份
// （不写库、不发 115 请求，名字认不出的库这时看不到）。get 读 setting（测试注入，正常传 settingValueCompat）
func effectiveLibraries(get func(string) string) []model.Library {
	if libs := loadLibraries(); len(libs) > 0 {
		return libs
	}
	if get == nil {
		get = settingValueCompat
	}
	fullRaw := get("full")
	sources := map[string]libNameSource{}
	for _, s := range legacyLibNameSources(fullRaw) {
		sources[s.Cid] = s
	}
	libs, _, _ := planLibrariesV2(libsV2In{
		FullRaw: fullRaw, OptsRaw: get(libOptsKey), AcctRaw: get(acctLibsKey), Legacy: loadLegacyLibScrape(),
		SchemeID: primarySchemeID(),
		name: func(cid string) string {
			if s, ok := sources[cid]; ok {
				return s.libRootOf().Name
			}
			return ""
		},
		shown: func(cid string) string {
			if row, ok := lookupCachedRow(cid); ok {
				return row.Path
			}
			return ""
		},
	})
	return libs
}

// libKeyOf 库在 lib_opts、整理记录、转存入口这些老地方用的 key：主号的库是 cid，别的账号的库是 a{账号}:{cid}
func libKeyOf(l model.Library) string {
	if l.Account != acctMain {
		return acctLibKey(l.Account, l.Cid)
	}
	return l.Cid
}

// libOfLocalRel 本地相对路径（媒体根下）落在哪个库里：按库的本地目录**整段**最长前缀认（本地目录可以多级）。
// 返回库、库内相对路径。各库的本地目录互不包含（validateLibraries），所以最多一个库匹配
func libOfLocalRel(libs []model.Library, rel string) (model.Library, string, bool) {
	rel = normLibLocal(rel)
	best, bestLen := model.Library{}, -1
	for _, l := range libs {
		local := normLibLocal(l.Local)
		if local == "" {
			continue
		}
		if rel == local || strings.HasPrefix(rel, local+"/") {
			if len(local) > bestLen {
				best, bestLen = l, len(local)
			}
		}
	}
	if bestLen < 0 {
		return model.Library{}, "", false
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(rel, normLibLocal(best.Local)), "/"), true
}

// syncLibrariesFromLegacy 老的库卡片（PUT /libraries、媒体库位置）存完之后，把老配置的变化同步进媒体库表。
// 过渡用：新的媒体库页面上线（V2 阶段 4）之前，老页面还在写 full / lib_opts / acct_libs。
// 按「账号 + 网盘目录」对号：老配置里有的更新位置 / 本地名 / 刮削 / 层级，没有的删掉，新的加上；
// 转存文件夹、分类方案这些老配置里没有的字段原样留着
func (h *Handler) syncLibrariesFromLegacy() {
	if h.DB == nil {
		return
	}
	var done int64
	h.DB.Model(&model.Setting{}).Where("key = ?", libsV2MigrateKey).Count(&done)
	if done == 0 {
		h.MigrateLibrariesV2() // 还没迁移过：直接迁
		return
	}
	fullRaw := h.getSettingValue("full")
	sources := map[string]libNameSource{}
	for _, s := range legacyLibNameSources(fullRaw) {
		sources[s.Cid] = s
	}
	cookie, _ := h.get115Cookie()
	plan, _, unresolved := planLibrariesV2(libsV2In{
		FullRaw: fullRaw, OptsRaw: h.getSettingValue(libOptsKey), AcctRaw: h.getSettingValue(acctLibsKey),
		Legacy: loadLegacyLibScrape(), SchemeID: primarySchemeID(),
		name: func(cid string) string {
			if s, ok := sources[cid]; ok {
				if n := s.libRootOf().Name; n != "" {
					return n
				}
			}
			return libDirName(cookie, cid)
		},
		shown: func(cid string) string {
			if row, ok := lookupCachedRow(cid); ok {
				return row.Path
			}
			return ""
		},
	})
	if len(unresolved) > 0 {
		log.Printf("[媒体库] ○ 有 %d 个库认不出名字，这次没同步进媒体库表", len(unresolved))
		return
	}
	var have []model.Library
	h.DB.Find(&have)
	byKey := map[string]model.Library{}
	for _, l := range have {
		byKey[libKeyOf(l)] = l
	}
	seen := map[string]bool{}
	for _, p := range plan {
		k := libKeyOf(p)
		seen[k] = true
		if old, ok := byKey[k]; ok {
			old.Local, old.Path, old.Scrape, old.Depth, old.Sort = p.Local, firstNonEmpty(p.Path, old.Path), p.Scrape, p.Depth, p.Sort
			if old.Name == "" {
				old.Name = p.Name
			}
			if p.SchemeID == 0 {
				old.SchemeID = 0 // 改成了自己整理
			} else if old.SchemeID == 0 {
				old.SchemeID = p.SchemeID
			}
			h.DB.Save(&old)
			continue
		}
		h.DB.Create(&p)
	}
	for k, l := range byKey {
		if !seen[k] {
			h.DB.Delete(&model.Library{}, l.ID)
		}
	}
	forgetLibraries()
}
