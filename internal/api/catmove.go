package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 分类方案改过之后，把网盘上的片目按新方案搬过去 ====================
//
// 起因（2026-10-30 用户反馈）：电影「动画电影」改成「电影/动画电影」、「纪录片」改成「电影/纪录片」之后，
// 网盘上已经整理好的片目还在旧分类目录里，本地 / Emby 按新方案走，两边分家：本地多出只有 STRM 的重复片目，
// 纪录电影落在同名的剧集分类里被按剧集刮削。保存方案时 catDirChanges 只提醒、确认后什么都不做。
//
// 搬到哪：按片目的 TMDB 详情重新走一遍分类（classifyTargetIn，整理用的同一个），**不做旧名 → 新名的一一映射**。
// 理由：保存时只知道「旧名没了」，不知道它改成了哪个（简单模式按位置猜也不可靠：中间插一条就全错位），
// 一个旧分类也可能拆进好几个新分类；而按 TMDB 重分，搬过去的位置就是之后新来的同一类片会去的位置，不会再分家。
// 代价是每部片一次 TMDB 详情请求（不碰 115），只在预演 / 执行时发。
//
// 搬哪些：整理记录里落点还在台账上、分类目录却已经不是当前方案（按这部片的媒体类型）里的分类的片目。
// 片目、媒体类型、TMDB 编号都取整理记录：方案保存后旧分类目录已经认不出片目（ledger.go 只认当前配置），
// 台账本身说不清哪一层是旧分类；用户自己放进库里的（没有整理记录）不动。
//
// 怎么搬：整个片目目录一次 move（同一个新分类的一批一起，§6.13 的整批），走 ops.moveFiles（抑制登记、路径缓存失效），
// 本地目录跟着挪、台账整棵换前缀（同增量跟随网盘移动的 relocateLocalDir），整理记录 / MediaLibrary 改落点，
// 空了的旧分类目录走空目录清理（§6.9 的守卫一条不松），最后按旧位置删、新位置刷通知 Emby。

// catMoveCand 一部还在旧分类目录里的片目（只读整理记录与台账算出来，零 115 / TMDB 请求）
type catMoveCand struct {
	Key       string `json:"key"` // 库 id:库内旧位置，执行时按它挑
	LibID     uint   `json:"lib_id"`
	LibName   string `json:"lib_name"`
	TmdbID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
	Title     string `json:"title"`
	Year      string `json:"year"`
	OldCat    string `json:"old_cat"` // 库内旧分类目录
	TitleDir  string `json:"title_dir"`
	Videos    int    `json:"videos"` // 台账里这个片目下的文件数
}

func (c catMoveCand) oldRel() string { return libSubPath(c.OldCat, c.TitleDir) }

// catMoveItem 预演的一行：搬到哪，或为什么不搬
type catMoveItem struct {
	catMoveCand
	NewCat string `json:"new_cat,omitempty"`
	Move   bool   `json:"move"`
	Reason string `json:"reason,omitempty"`
}

func (it catMoveItem) newRel() string { return libSubPath(it.NewCat, it.TitleDir) }

func catMoveKey(libID uint, oldRel string) string { return fmt.Sprintf("%d:%s", libID, oldRel) }

// liveCatsOf 方案里某个媒体类型现在会用到的分类目录（catLookup 的口径，含系列子目录标记）
func liveCatsOf(rules []model.CategoryRule, mediaType string) map[string]string {
	var rows []model.CategoryRule
	for _, r := range rules {
		if r.MediaType == mediaType {
			rows = append(rows, r)
		}
	}
	return buildLibCategoryLayout(rows).cats
}

// catMoveCandidates 纯函数。recs 按 id 倒序（同一个片目只看最近那条）；
// ledgerCount 给出台账里「库本地目录/库内相对路径」这个目录下有几个文件
func catMoveCandidates(libs []model.Library, rulesByScheme map[uint][]model.CategoryRule,
	recs []model.OrganizeRecord, ledgerCount func(rel string) int) []catMoveCand {
	type liveKey struct {
		scheme uint
		mt     string
	}
	live := map[liveKey]map[string]string{}
	seen := map[string]bool{}
	out := []catMoveCand{}
	for i := range recs {
		rec := &recs[i]
		if rec.Status != "success" || rec.TmdbID <= 0 || strings.TrimSpace(rec.TargetDir) == "" {
			continue
		}
		mt := rec.MediaType
		if mt != "movie" && mt != "tv" {
			continue
		}
		lib, ok := orgLibOfRecord(rec, libs)
		if !ok || lib.SchemeID == 0 || len(rulesByScheme[lib.SchemeID]) == 0 {
			continue // 不分类的库没有分类目录可言；方案被删了的库不猜
		}
		cat := libSubPath(rec.Category)
		if cat == "" {
			cat = mediaTypeCategory(mt)
		}
		target := libSubPath(rec.TargetDir)
		if !strings.HasPrefix(target, cat+"/") {
			continue // 落点和分类对不上（老记录、被挪过），不猜哪一层是片目
		}
		titleDir := strings.SplitN(strings.TrimPrefix(target, cat+"/"), "/", 2)[0]
		if titleDir == "" {
			continue
		}
		key := catMoveKey(lib.ID, libSubPath(cat, titleDir))
		if seen[key] {
			continue
		}
		seen[key] = true
		lk := liveKey{lib.SchemeID, mt}
		if live[lk] == nil {
			live[lk] = liveCatsOf(rulesByScheme[lib.SchemeID], mt)
		}
		if _, ok := catLookup(live[lk], cat); ok {
			continue // 这个分类现在还在：不是改方案留下的
		}
		n := ledgerCount(libSubPath(normLibLocal(lib.Local), cat, titleDir))
		if n == 0 {
			continue // 台账里已经没有了（删了 / 挪走了）
		}
		out = append(out, catMoveCand{
			Key: key, LibID: lib.ID, LibName: lib.Name, TmdbID: rec.TmdbID, MediaType: mt,
			Title: rec.Title, Year: rec.Year, OldCat: cat, TitleDir: titleDir, Videos: n,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LibID != out[j].LibID {
			return out[i].LibID < out[j].LibID
		}
		if out[i].OldCat != out[j].OldCat {
			return out[i].OldCat < out[j].OldCat
		}
		return out[i].TitleDir < out[j].TitleDir
	})
	return out
}

// planCatMoves 给每个候选定去向（纯函数）。classify 返回新分类目录（库内相对路径）；
// ledgerCount 同 catMoveCandidates，用来认「新位置台账里已经有这部片」
func planCatMoves(cands []catMoveCand, libs []model.Library, classify func(c catMoveCand) (string, error),
	ledgerCount func(rel string) int) []catMoveItem {
	libLocal := map[uint]string{}
	for _, l := range libs {
		libLocal[l.ID] = normLibLocal(l.Local)
	}
	taken := map[string]string{} // 库:新位置 → 先占着的那部
	out := make([]catMoveItem, 0, len(cands))
	for _, c := range cands {
		it := catMoveItem{catMoveCand: c}
		newCat, err := classify(c)
		switch {
		case err != nil:
			it.Reason = "读不出 TMDB 详情：" + err.Error()
		case newCat == "":
			it.Reason = "新方案算不出分类目录"
		case newCat == c.OldCat:
			it.NewCat = newCat
			it.Reason = "按新方案还是这个目录"
		default:
			it.NewCat = newCat
			k := fmt.Sprintf("%d:%s", c.LibID, it.newRel())
			if ledgerCount(libSubPath(libLocal[c.LibID], it.newRel())) > 0 {
				it.Reason = "新位置已经有同名片目（多半是同一部的另一份），先自己处理"
			} else if prev, dup := taken[k]; dup {
				it.Reason = "和「" + prev + "」要搬到同一个位置"
			} else {
				taken[k] = c.oldRel()
				it.Move = true
			}
		}
		out = append(out, it)
	}
	return out
}

// ledgerDirCounter 一次读出这些库的台账路径，之后按目录数文件（目录 = 台账相对路径，含库目录）
func ledgerDirCounter(libs []model.Library) func(rel string) int {
	counts := map[string]int{}
	if model.DB == nil {
		return func(string) int { return 0 }
	}
	for _, l := range libs {
		local := normLibLocal(l.Local)
		if local == "" {
			continue
		}
		var rels []string
		model.DB.Model(&model.SyncedFile{}).
			Where("substr(rel_path, 1, length(?)) = ?", local+"/", local+"/").
			Pluck("rel_path", &rels)
		for _, r := range rels {
			for d := path.Dir(r); d != "." && d != "/" && d != local; d = path.Dir(d) {
				counts[d]++
			}
		}
	}
	return func(rel string) int { return counts[rel] }
}

// catMoveScope 要看的库（schemeID 0 = 所有选了方案的库）与方案规则
func catMoveScope(schemeID uint) ([]model.Library, map[uint][]model.CategoryRule) {
	libs := []model.Library{}
	for _, l := range loadLibraries() {
		if l.SchemeID != 0 && (schemeID == 0 || l.SchemeID == schemeID) {
			libs = append(libs, l)
		}
	}
	rules := map[uint][]model.CategoryRule{}
	if model.DB != nil && len(libs) > 0 {
		var rows []model.CategoryRule
		model.DB.Where("media_type IN ?", []string{"movie", "tv"}).Order("media_type, priority").Find(&rows)
		for _, r := range rows {
			rules[r.SchemeID] = append(rules[r.SchemeID], r)
		}
	}
	return libs, rules
}

// catMoveCandidatesNow 当前的候选。零 115 / TMDB 请求
func catMoveCandidatesNow(schemeID uint) ([]catMoveCand, []model.Library, func(string) int) {
	libs, rules := catMoveScope(schemeID)
	count := ledgerDirCounter(libs)
	if len(libs) == 0 || model.DB == nil {
		return []catMoveCand{}, libs, count
	}
	var recs []model.OrganizeRecord
	model.DB.Select("id, status, tmdb_id, title, year, media_type, category, target_dir, lib_cid, lib_id, source_cid").
		Where("status = ? AND tmdb_id > 0 AND target_dir <> ''", "success").Order("id desc").Find(&recs)
	return catMoveCandidates(libs, rules, recs, count), libs, count
}

// catMovePlanNow 候选 + 去向（每部一次 TMDB 详情，并发 6，catTryMedia 有 10 分钟缓存）
func catMovePlanNow(schemeID uint) ([]catMoveItem, []model.Library, error) {
	cands, libs, count := catMoveCandidatesNow(schemeID)
	if len(cands) == 0 {
		return []catMoveItem{}, libs, nil
	}
	if _, err := loadTmdbClient(); err != nil {
		return nil, libs, fmt.Errorf("要按 TMDB 详情重新分类，先配好 TMDB：%v", err)
	}
	scheme := map[uint]uint{}
	for _, l := range libs {
		scheme[l.ID] = l.SchemeID
	}
	cats := make([]string, len(cands))
	errs := make([]error, len(cands))
	idx := map[string]int{}
	for i, c := range cands {
		idx[c.Key] = i
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c catMoveCand) {
			defer func() { <-sem; wg.Done() }()
			media, err := catTryMedia(c.TmdbID, c.MediaType == "tv")
			if err == nil && media == nil {
				err = errors.New("TMDB 上没有这个条目")
			}
			if err != nil {
				errs[i] = err
				return
			}
			media.MediaType = c.MediaType
			cat, _ := classifyTargetIn(scheme[c.LibID], media)
			cats[i] = categoryDir(c.MediaType, cat)
		}(i, c)
	}
	wg.Wait()
	items := planCatMoves(cands, libs, func(c catMoveCand) (string, error) {
		i := idx[c.Key]
		return cats[i], errs[i]
	}, count)
	return items, libs, nil
}

// ---- 执行 ----

// catMoveIO 搬移用到的 115 操作（*pan115Ops 满足；测试用假的）
type catMoveIO interface {
	dirIO
	ensurePath(parent, dirPath string) (string, error)
}

// catMoveLibResult 一个库搬完的结果
type catMoveLibResult struct {
	Moved  []catMoveItem
	Failed []catMoveItem // Reason 写失败原因
	// OldAbs / NewAbs 本地绝对路径：旧位置通知 Emby 删、新位置刷新
	OldAbs []string
	NewAbs []string
}

// catMoveChunk 一次 move 最多几个目录
const catMoveChunk = 100

// applyCatMoves 在一个库里搬一批（都必须是这个库的）。localRoot 是本地媒体根。
// 先网盘、成功了才动本地与台账：网盘没搬成的本地不动，下一次预演还会列出来
func applyCatMoves(io catMoveIO, lib model.Library, items []catMoveItem, localRoot string,
	protected []string, onLog func(string)) catMoveLibResult {
	res := catMoveLibResult{}
	fail := func(it catMoveItem, why string) {
		it.Move, it.Reason = false, why
		res.Failed = append(res.Failed, it)
		onLog(fmt.Sprintf("✗ %s：%s", it.oldRel(), why))
	}
	rels := make([]string, 0, len(items))
	for _, it := range items {
		rels = append(rels, it.oldRel())
	}
	// 逐级按名字列出来（深度删除同款）：拿到的是网盘上现在的结构，不信路径缓存
	cids := resolveDeepDelDirCids(io, lib.Cid, rels)

	byCat := map[string][]catMoveItem{}
	var cats []string
	for _, it := range items {
		if cids[it.oldRel()] == "" {
			fail(it, "网盘上没找到这个目录（可能已经挪走或改了名）")
			continue
		}
		if byCat[it.NewCat] == nil {
			cats = append(cats, it.NewCat)
		}
		byCat[it.NewCat] = append(byCat[it.NewCat], it)
	}
	sort.Strings(cats)
	libLocal := normLibLocal(lib.Local)
	var moved []catMoveItem
	for _, cat := range cats {
		group := byCat[cat]
		dest, err := io.ensurePath(lib.Cid, cat)
		if err != nil || dest == "" {
			for _, it := range group {
				fail(it, fmt.Sprintf("建不出新分类目录 %s：%v", cat, err))
			}
			continue
		}
		// 新分类目录里已经有同名目录：115 会把搬进来的悄悄改名成「X(1)」，台账就对不上了
		names, err := childDirNames(io, dest)
		if err != nil {
			for _, it := range group {
				fail(it, fmt.Sprintf("读不出新分类目录 %s：%v", cat, err))
			}
			continue
		}
		var ok []catMoveItem
		for _, it := range group {
			if names[it.TitleDir] {
				fail(it, "网盘上新位置已有同名目录")
				continue
			}
			ok = append(ok, it)
		}
		for start := 0; start < len(ok); start += catMoveChunk {
			end := min(start+catMoveChunk, len(ok))
			chunk := ok[start:end]
			fids := make([]string, 0, len(chunk))
			for _, it := range chunk {
				fids = append(fids, cids[it.oldRel()])
			}
			if err := io.moveFiles(dest, fids); err != nil {
				for _, it := range chunk {
					fail(it, "115 移动失败："+err.Error())
				}
				continue
			}
			onLog(fmt.Sprintf("✓ 网盘：%d 部 → %s", len(chunk), cat))
			moved = append(moved, chunk...)
		}
	}

	// 本地、台账、记录跟着走
	oldCats := map[string]bool{}
	for _, it := range moved {
		oldRel := libSubPath(libLocal, it.oldRel())
		newRel := libSubPath(libLocal, it.newRel())
		oldAbs := filepath.Join(localRoot, filepath.FromSlash(oldRel))
		newAbs := filepath.Join(localRoot, filepath.FromSlash(newRel))
		if err := mergeLocalDir(oldAbs, newAbs); err != nil {
			// 网盘已经搬了：台账照样换（下一次全量按台账补写），本地留着的旧目录由失效 STRM 收
			onLog(fmt.Sprintf("○ 本地目录没挪全（%v）：%s → %s", err, oldRel, newRel))
		}
		rewriteLedgerPrefix(oldRel, newRel)
		rewriteRecordTargets(lib, it, loadLibraries())
		res.OldAbs = append(res.OldAbs, oldAbs)
		res.NewAbs = append(res.NewAbs, newAbs)
		res.Moved = append(res.Moved, it)
		oldCats[it.OldCat] = true
		onLog(fmt.Sprintf("✓ %s → %s", it.oldRel(), it.newRel()))
	}

	// 空了的旧分类目录：网盘走空目录清理（整棵没有文件才删、进回收站），本地只删空目录
	if len(oldCats) > 0 {
		pr := newDirPruner(io, protected, onLog)
		for cat := range oldCats {
			pr.mark(cids[cat], cat)
			for d := filepath.Join(localRoot, filepath.FromSlash(libSubPath(libLocal, cat))); ; d = filepath.Dir(d) {
				rel, err := filepath.Rel(filepath.Join(localRoot, filepath.FromSlash(libLocal)), d)
				if err != nil || rel == "." || strings.HasPrefix(rel, "..") || os.Remove(d) != nil {
					break
				}
			}
		}
		pr.flush()
	}
	return res
}

// childDirNames 目录下的子目录名（翻页列全）
func childDirNames(io dirIO, cid string) (map[string]bool, error) {
	out := map[string]bool{}
	offset := 0
	for {
		entries, total, err := io.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if fmt.Sprint(e["f"]) == "0" {
				out[fmt.Sprint(e["n"])] = true
			}
		}
		offset += len(entries)
		if len(entries) == 0 || offset >= total {
			return out, nil
		}
	}
}

// mergeLocalDir 本地片目目录挪到新位置。新位置没有就整个改名；
// 已经有了（方案改过之后按新路径写出来的只有 STRM 的重复片目）就逐个文件并进去，同名的以旧目录为准
// （旧目录才是台账认的那份，刮削产物也在它这里），并完把旧目录里剩下的空目录收掉
func mergeLocalDir(oldAbs, newAbs string) error {
	st, err := os.Stat(oldAbs)
	if err != nil || !st.IsDir() {
		return nil // 本地本来就没有（还没同步过），只换台账
	}
	if _, err := os.Stat(newAbs); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
			return err
		}
		return os.Rename(oldAbs, newAbs)
	}
	var firstErr error
	var dirs []string
	_ = filepath.Walk(oldAbs, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(oldAbs, p)
		dst := filepath.Join(newAbs, rel)
		if info.IsDir() {
			dirs = append(dirs, p)
			if err := os.MkdirAll(dst, 0o755); err != nil && firstErr == nil {
				firstErr = err
			}
			return nil
		}
		_ = os.Remove(dst) // Windows 上 Rename 不覆盖
		if err := os.Rename(p, dst); err != nil && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // 只删空的
	}
	return firstErr
}

// rewriteLedgerPrefix 台账整棵子树换前缀（不用 LIKE：片目名里的 _ % 会误配别的目录）。
// SQL 用 length(?)：Go 数字节、SQLite 数字符
func rewriteLedgerPrefix(oldRel, newRel string) {
	if model.DB == nil || oldRel == "" || newRel == "" || oldRel == newRel {
		return
	}
	model.DB.Model(&model.SyncedFile{}).
		Where("substr(rel_path, 1, length(?)) = ?", oldRel+"/", oldRel+"/").
		Update("rel_path", gorm.Expr("? || substr(rel_path, length(?) + 1)", newRel, oldRel))
}

// rewriteRecordTargets 落点在这个片目里的整理记录（这个库的）改到新位置；MediaLibrary 同一部的跟着改
func rewriteRecordTargets(lib model.Library, it catMoveItem, libs []model.Library) {
	if model.DB == nil {
		return
	}
	oldRel, newRel := it.oldRel(), it.newRel()
	var recs []model.OrganizeRecord
	model.DB.Select("id, target_dir, lib_cid, lib_id, source_cid, category").
		Where("target_dir = ? OR substr(target_dir, 1, length(?)) = ?", oldRel, oldRel+"/", oldRel+"/").Find(&recs)
	for i := range recs {
		if l, ok := orgLibOfRecord(&recs[i], libs); !ok || l.ID != lib.ID {
			continue
		}
		upd := map[string]any{"target_dir": newRel + strings.TrimPrefix(recs[i].TargetDir, oldRel)}
		if libSubPath(recs[i].Category) == it.OldCat || recs[i].Category == "" {
			upd["category"] = it.NewCat
		}
		model.DB.Model(&model.OrganizeRecord{}).Where("id = ?", recs[i].ID).Updates(upd)
	}
	model.DB.Model(&model.MediaLibrary{}).
		Where("tmdb_id = ? AND media_type = ? AND substr(target_path, 1, length(?)) = ?", it.TmdbID, it.MediaType, oldRel+"/", oldRel+"/").
		Updates(map[string]any{
			"category":    it.NewCat,
			"target_path": gorm.Expr("? || substr(target_path, length(?) + 1)", newRel, oldRel),
		})
}

// ---- 接口 ----

// CatMoveStatus GET /category/move/status?scheme_id= —— 网盘上还有几部片在旧分类目录里（海报墙 / 媒体库页的提示）。
// 零 115 / TMDB 请求：只读整理记录、台账与规则
func (h *Handler) CatMoveStatus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Query("scheme_id"), 10, 64)
	cands, _, _ := catMoveCandidatesNow(uint(id))
	type group struct {
		LibName   string `json:"lib_name"`
		MediaType string `json:"media_type"`
		OldCat    string `json:"old_cat"`
		Titles    int    `json:"titles"`
	}
	var groups []group
	at := map[string]int{}
	for _, c := range cands {
		k := fmt.Sprintf("%d|%s|%s", c.LibID, c.MediaType, c.OldCat)
		if i, ok := at[k]; ok {
			groups[i].Titles++
			continue
		}
		at[k] = len(groups)
		groups = append(groups, group{LibName: c.LibName, MediaType: c.MediaType, OldCat: c.OldCat, Titles: 1})
	}
	if groups == nil {
		groups = []group{}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(cands), "groups": groups})
}

// CatMovePreview POST /category/move/preview {scheme_id?} —— 每部片搬到哪。只读：每部一次 TMDB 详情，不碰 115
func (h *Handler) CatMovePreview(c *gin.Context) {
	var req struct {
		SchemeID uint `json:"scheme_id"`
	}
	_ = c.ShouldBindJSON(&req)
	items, _, err := catMovePlanNow(req.SchemeID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	moves := 0
	for _, it := range items {
		if it.Move {
			moves++
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "moves": moves})
}

// catMoveJobParams 搬移任务的参数
type catMoveJobParams struct {
	SchemeID uint `json:"scheme_id,omitempty"`
	// Keys 只搬这几部（catMoveCand.Key）；空 = 预演里能搬的全搬
	Keys []string `json:"keys,omitempty"`
}

// CatMoveApply POST /category/move {scheme_id?, keys?} → 入主队列，202。执行时重新算一遍、不拿预演的结果
func (h *Handler) CatMoveApply(c *gin.Context) {
	var req catMoveJobParams
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	title := "按新分类方案搬移网盘目录"
	if len(req.Keys) > 0 {
		title += fmt.Sprintf("（%d 部）", len(req.Keys))
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "catmove", Title: title, DedupeKey: "catmove",
		Source: "web", Priority: jobPriorityManual, Params: jobParams{CatMove: &req}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "搬移")
}

func init() {
	jobExecutors["catmove"] = execCatMoveJob
}

// catMoveReport 任务结果（任务详情里看）
type catMoveReport struct {
	Moved  []catMoveItem `json:"moved"`
	Failed []catMoveItem `json:"failed"`
}

// execCatMoveJob 主队列（拿着 taskMu）：动的是库里的网盘目录与本地 STRM，增量 / 整理 / 全量都不能同时动
func execCatMoveJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).CatMove
	if p == nil {
		p = &catMoveJobParams{}
	}
	setJobProgress("按新分类方案重新分类", 0, progressKeep, "")
	items, libs, err := catMovePlanNow(p.SchemeID)
	if err != nil {
		return jobOutcome{}, err
	}
	want := map[string]bool{}
	for _, k := range p.Keys {
		want[k] = true
	}
	byLib := map[uint][]catMoveItem{}
	total := 0
	for _, it := range items {
		if it.Move && (len(want) == 0 || want[it.Key]) {
			byLib[it.LibID] = append(byLib[it.LibID], it)
			total++
		}
	}
	if total == 0 {
		return jobOutcome{Message: "没有要搬的片目（网盘上的分类目录已经和方案一致）", Result: catMoveReport{Moved: []catMoveItem{}, Failed: []catMoveItem{}}}, nil
	}
	protected := []string{}
	for _, l := range loadLibraries() {
		protected = append(protected, l.Cid, l.StagingCid)
	}
	rep := catMoveReport{Moved: []catMoveItem{}, Failed: []catMoveItem{}}
	var oldAbs, newAbs []string
	done := 0
	for _, lib := range libs {
		group := byLib[lib.ID]
		if len(group) == 0 {
			continue
		}
		if jobStopRequested() {
			return jobOutcome{Canceled: true, Message: fmt.Sprintf("已停止：搬了 %d 部", len(rep.Moved)), Result: rep}, nil
		}
		setJobProgress("搬移「"+lib.Name+"」", done, total, "")
		ops, err := h.opsFor(lib.Account)
		if err != nil {
			for _, it := range group {
				it.Move, it.Reason = false, err.Error()
				rep.Failed = append(rep.Failed, it)
			}
			continue
		}
		ops.suppress = true // 自己搬的，绕回来的生活事件不该再被增量处理一遍（§6.8）
		r := applyCatMoves(ops, lib, group, mediaRoot(), protected, func(s string) { log.Printf("[分类搬移] %s", s) })
		rep.Moved = append(rep.Moved, r.Moved...)
		rep.Failed = append(rep.Failed, r.Failed...)
		oldAbs = append(oldAbs, r.OldAbs...)
		newAbs = append(newAbs, r.NewAbs...)
		done += len(group)
	}
	if len(rep.Moved) > 0 {
		forgetLedgerTitles()
		forgetLocalTitles()
		markEmbyRenamed(newAbs...) // 换了位置的老片，Emby 推回来的 library.new 不是新片入库
		notifyEmbyDeleted(oldAbs...)
		for _, p := range newAbs {
			h.notifyEmbyRefresh(p)
		}
	}
	log.Printf("[分类搬移] ✓ 搬了 %d 部，%d 部没搬成", len(rep.Moved), len(rep.Failed))
	out := jobOutcome{Result: rep, Message: fmt.Sprintf("已按新分类方案搬了 %d 部", len(rep.Moved))}
	if len(rep.Failed) > 0 {
		out.Partial = true
		out.Message = fmt.Sprintf("搬了 %d 部，%d 部没搬成（看任务详情）", len(rep.Moved), len(rep.Failed))
	}
	return out, nil
}
