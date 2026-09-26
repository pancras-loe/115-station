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
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 网盘文件页：刮削所选条目 ====================
//
// 选项默认取「自动整理 → 刮削」里保存的配置，前端可以只为这一次改；
// 「上传到网盘」同样只管这一次，不碰监控上传的总开关（upload115FileConsented）。
//
// 所选条目分两种落法：
//   - 在媒体库里、台账认得出片目 → 按台账写本地媒体库（与「开始刮削」同一份产物），
//     勾了上传再把这次写出的文件传进网盘对应目录；没勾上传则把它们登记成「已处理」，
//     免得开着的监控上传随后又自己传上去 —— 用户这一次明确说了不传；
//   - 不在媒体库里（待整理 / 冗余 / 任意目录），或台账里没有 → 本地没有对应片目，
//     产物只能直接写进网盘里那个目录（MoviePilot 对存储条目刮削也是写回原目录），
//     所以必须勾上传；网盘里的文件夹按一部影片处理。

func init() {
	jobExecutors["scrape"] = execFileScrapeJob
}

// fileScrapeOpts 本次刮削的选项
type fileScrapeOpts struct {
	WriteNFO    bool `json:"write_nfo"`
	WriteImages bool `json:"write_images"`
	Force       bool `json:"force"`  // 覆盖已存在的元数据
	Upload      bool `json:"upload"` // 这一次把产物写进网盘
}

// ScrapeFiles POST /files/scrape → 入队，202
// body: {cid, chain, items:[{id,name,is_dir,pickcode}], scrape:{write_nfo,write_images,force,upload},
// tmdb_id?, media_type?, label?}
func (h *Handler) ScrapeFiles(c *gin.Context) {
	var req fileJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	roles := h.workspaceRoles()
	if err := req.validate(roles); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	o := req.Scrape
	if o == nil || (!o.WriteNFO && !o.WriteImages) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "NFO 与图片至少要生成一项"})
		return
	}
	if req.TmdbID > 0 && len(req.Items) > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定 TMDB 条目时一次只能刮削一项"})
		return
	}
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 不上传就只能刮媒体库里的：面包屑说得清位置时当场拦下，别排一次队再失败
	if !o.Upload && (req.Cid == "0" || len(req.Chain) > 0) {
		inLib := false
		for _, it := range req.Items {
			if itemLibIndex(req.Chain, roles, it) >= -1 {
				inLib = true
				break
			}
		}
		if !inLib {
			c.JSON(http.StatusBadRequest, gin.H{"error": "所选条目不在媒体库里，本地没有对应片目：要刮削请勾选「上传到网盘」，产物会直接写进网盘目录"})
			return
		}
	}
	title := "刮削" + fileJobTitle(req.Items)
	if req.TmdbID > 0 {
		title += " → " + pickLabel(pickReq{TmdbID: req.TmdbID, MediaType: req.MediaType, Label: req.Label})
	}
	fp := req.fileJobParams
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: "scrape", Title: title, DedupeKey: fileJobDedupe(fp),
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{TmdbID: req.TmdbID, MediaType: req.MediaType, Files: &fp},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "刮削")
}

// itemLibIndex 条目与媒体库的关系：
// >=0 媒体库根在面包屑上的下标（条目在库里）；-1 条目就是媒体库根目录本身；-2 不在库里
func itemLibIndex(chain []browseCrumb, roles map[string]string, it fileJobItem) int {
	if i := chainRoleIndex(chain, roles, "library"); i >= 0 {
		return i
	}
	if it.IsDir && roles[it.ID] == "library" {
		return -1
	}
	return -2
}

// itemLibRel 条目在媒体库里的位置：库名（本地媒体树第一层）与库内相对路径（库根本身为空）
func itemLibRel(chain []browseCrumb, idx int, it fileJobItem) (libName, rel string) {
	if idx == -1 {
		return it.Name, ""
	}
	parts := make([]string, 0, len(chain)-idx)
	for _, c := range chain[idx+1:] {
		parts = append(parts, c.Name)
	}
	parts = append(parts, it.Name)
	return chain[idx].Name, strings.Join(parts, "/")
}

// matchLedgerTitles 所选路径（含库名前缀）对应的台账片目：
//   - 所选就是标题目录，或在标题目录里面（季目录 / 单集）→ 那一部；within=true 表示只刮所选这部分的视频
//   - 所选是标题目录的上级（分类目录、库根）→ 下面的每一部
//
// 老台账有不带库名的两段式路径（电影/标题），调用方再用不带库名的相对路径查一遍
func matchLedgerTitles(entries map[string]*ledgerTitleEntry, sel string) (out []*ledgerTitleEntry, within bool) {
	sel = strings.Trim(sel, "/")
	if sel == "" {
		return nil, false
	}
	for key, e := range entries {
		if key == "" {
			continue
		}
		if key == sel || strings.HasPrefix(sel, key+"/") {
			return []*ledgerTitleEntry{e}, key != sel
		}
		if strings.HasPrefix(key, sel+"/") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, false
}

// selWithin 台账视频行是否落在所选范围内（sel 是一集视频或季目录）
func selWithin(relPath, sel string) bool {
	return strings.TrimSuffix(relPath, ".strm") == sel || strings.HasPrefix(relPath, sel+"/")
}

// fileScrapePlanner 把勾选的条目换算成刮削对象
type fileScrapePlanner struct {
	h         *Handler
	ops       *pan115Ops
	tc        *TmdbClient
	roles     map[string]string
	chain     []browseCrumb
	parentCid string
	localRoot string
	upload    bool
	pick      *TmdbMedia
	ledger    map[string]*ledgerTitleEntry
}

// plan 逐项换算；某一项换算失败记进 problems，不影响别的项
func (pl *fileScrapePlanner) plan(items []fileJobItem) (titles []scrapeTitle, problems []string) {
	seen := map[string]bool{}
	for i, it := range items {
		if jobStopRequested() {
			break
		}
		setJobProgress("定位片目", i, len(items), truncateStr(it.Name, 60))
		ts, err := pl.planItem(it)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：%v", it.Name, err))
			continue
		}
		for _, t := range ts {
			// 同一部片被勾了两次（整部 + 其中一季）：按标题目录去重，只刮一遍
			k := t.Dir.Local + "|" + t.Dir.CloudBase + "|" + t.Dir.CloudRel
			if seen[k] {
				continue
			}
			seen[k] = true
			titles = append(titles, t)
		}
	}
	return titles, problems
}

func (pl *fileScrapePlanner) planItem(it fileJobItem) ([]scrapeTitle, error) {
	idx := itemLibIndex(pl.chain, pl.roles, it)
	if idx >= -1 && pl.localRoot != "" {
		libName, rel := itemLibRel(pl.chain, idx, it)
		if pl.ledger == nil {
			pl.ledger = scanLedgerTitles()
		}
		sel := strings.Trim(libName+"/"+rel, "/")
		entries, within := matchLedgerTitles(pl.ledger, sel)
		if len(entries) == 0 && rel != "" {
			entries, within = matchLedgerTitles(pl.ledger, rel)
			if len(entries) > 0 {
				sel = rel
			}
		}
		if len(entries) > 0 {
			if pl.pick != nil && len(entries) > 1 {
				return nil, fmt.Errorf("下面有 %d 部影片，指定 TMDB 条目时只能选其中一部", len(entries))
			}
			libCid := pl.libraryCid()
			out := make([]scrapeTitle, 0, len(entries))
			for _, e := range entries {
				t, err := pl.ledgerTitle(e, libName, libCid, sel, within)
				if err != nil {
					if len(entries) == 1 {
						return nil, err
					}
					log.Printf("[影视刮削] ○ 跳过《%s》: %v", e.Title, err)
					continue
				}
				out = append(out, t)
			}
			if len(out) == 0 {
				return nil, errors.New("下面的片目都识别不出 TMDB 条目")
			}
			return out, nil
		}
		log.Printf("[影视刮削] ○ %s 在媒体库里但台账没有对应片目（还没同步到本地？），按网盘内容刮削", it.Name)
	}
	if !pl.upload {
		return nil, errors.New("不在媒体库里（或本地还没有对应片目），只能直接写进网盘目录：请勾选「上传到网盘」")
	}
	t, err := pl.cloudTitle(it)
	if err != nil {
		return nil, err
	}
	return []scrapeTitle{t}, nil
}

// libraryCid 媒体库根 cid（角色表反查）
func (pl *fileScrapePlanner) libraryCid() string {
	for cid, r := range pl.roles {
		if r == "library" {
			return cid
		}
	}
	return ""
}

// ledgerTitle 台账片目 → 刮削对象（本地落点 + 网盘对应目录）
func (pl *fileScrapePlanner) ledgerTitle(e *ledgerTitleEntry, libName, libCid, sel string, within bool) (scrapeTitle, error) {
	t := scrapeTitle{Kind: e.MediaType, Title: e.Title, Year: e.Year, TmdbID: e.TmdbID}
	switch {
	case pl.pick != nil:
		t.Kind, t.TmdbID, t.Title, t.Year = pl.pick.MediaType, pl.pick.TmdbID, pl.pick.Title, pl.pick.Year
	case t.TmdbID <= 0:
		// 目录名里没有 [tmdb=…]：「开始刮削」会跳过这种片目，这里是用户点名要刮的，按目录名识别一次
		parsed := parseFileName(path.Base(e.Key))
		parsed.IsTV = parsed.IsTV || e.MediaType == "tv"
		media, err := pl.tc.recognize(parsed)
		if err != nil || media == nil {
			return t, errors.New("目录名里没有 TMDB 编号，也按片名识别不出来：请指定 TMDB 条目")
		}
		t.TmdbID, t.Title, t.Year = media.TmdbID, media.Title, media.Year
		if media.MediaType != "" {
			t.Kind = media.MediaType
		}
	}
	if t.Kind != "tv" {
		t.Kind = "movie"
	}
	cloudRel := func(rel string) string {
		rel = strings.Trim(rel, "/")
		if rel == libName {
			return ""
		}
		return strings.TrimPrefix(rel, libName+"/")
	}
	t.Dir = metaDest{Local: filepath.Join(pl.localRoot, filepath.FromSlash(e.Key)), CloudBase: libCid, CloudRel: cloudRel(e.Key)}
	for _, sf := range scrapeDirVideoRows(e.Key) {
		if within && !selWithin(sf.RelPath, sel) {
			continue
		}
		dir := path.Dir(sf.RelPath)
		t.Videos = append(t.Videos, scrapeVideo{
			Name:     strings.TrimSuffix(path.Base(sf.RelPath), ".strm"),
			PickCode: sf.PickCode,
			Dir:      metaDest{Local: filepath.Join(pl.localRoot, filepath.FromSlash(dir)), CloudBase: libCid, CloudRel: cloudRel(dir)},
		})
	}
	if within && len(t.Videos) == 0 {
		return t, errors.New("台账里没找到所选的视频（可能还没同步到本地）")
	}
	return t, nil
}

// cloudVideo 网盘里找到的一个视频
type cloudVideo struct {
	name, pickcode, parentCid string
}

// fileScrapeWalkDepth / fileScrapeWalkMax 刮削时往文件夹里找视频的深度与数量上限：
// 一部剧「片名/Season 01/*.mkv」两层就够，再深多半是勾错了目录（勾了一整个分类）
const (
	fileScrapeWalkDepth = 3
	fileScrapeWalkMax   = 2000
)

// cloudTitle 不在媒体库里的条目：按网盘内容刮削，产物写回网盘
func (pl *fileScrapePlanner) cloudTitle(it fileJobItem) (scrapeTitle, error) {
	var videos []cloudVideo
	titleCid, titleName := it.ID, it.Name
	if it.IsDir {
		var err error
		videos, err = walkCloudVideos(pl.ops, it.ID, fileScrapeWalkDepth, fileScrapeWalkMax)
		if err != nil {
			return scrapeTitle{}, fmt.Errorf("读取文件夹失败: %v", err)
		}
		if len(videos) == 0 {
			return scrapeTitle{}, errors.New("文件夹里没有视频文件")
		}
	} else {
		if !videoExts[strings.ToLower(pathExt(it.Name))] {
			return scrapeTitle{}, errors.New("不是视频文件")
		}
		if pl.parentCid == "" || pl.parentCid == "0" {
			return scrapeTitle{}, errors.New("网盘根目录下的散文件没有可以放海报与 NFO 的片目录，请先放进一个文件夹")
		}
		videos = []cloudVideo{{name: it.Name, pickcode: it.PickCode, parentCid: pl.parentCid}}
		titleCid = pl.parentCid
		if n := len(pl.chain); n > 0 {
			titleName = pl.chain[n-1].Name
		}
	}

	media := pl.pick
	if media == nil {
		parsed := parseFileName(it.Name)
		if !it.IsDir && parsed.Title == "" {
			parsed = parseFileName(titleName)
		}
		for _, v := range videos {
			if parseFileName(v.name).Episode > 0 {
				parsed.IsTV = true
				break
			}
		}
		parsed.Source = it.Name
		var err error
		media, err = pl.tc.recognize(parsed)
		if err != nil || media == nil {
			return scrapeTitle{}, errors.New("识别不出 TMDB 条目：请指定 TMDB 条目后再刮削")
		}
	}
	t := scrapeTitle{Kind: media.MediaType, Title: media.Title, Year: media.Year, TmdbID: media.TmdbID,
		Dir: metaDest{CloudBase: titleCid}}
	if t.Kind != "tv" {
		t.Kind = "movie"
	}
	for _, v := range videos {
		t.Videos = append(t.Videos, scrapeVideo{Name: v.name, PickCode: v.pickcode, Dir: metaDest{CloudBase: v.parentCid}})
	}
	return t, nil
}

// walkCloudVideos 递归找视频（限深、限量，可被任务停止打断）
func walkCloudVideos(ops fileEntryLister, cid string, depth, max int) ([]cloudVideo, error) {
	var out []cloudVideo
	var walk func(cid string, depth int) error
	walk = func(cid string, depth int) error {
		offset := 0
		for {
			if jobStopRequested() {
				return errors.New("已按要求停止")
			}
			raw, count, err := ops.listEntries(cid, offset)
			if err != nil {
				return err
			}
			for _, d := range raw {
				e := toFileEntry(d)
				if e.IsDir {
					if depth > 1 {
						if err := walk(e.ID, depth-1); err != nil {
							return err
						}
					}
					continue
				}
				if videoExts[strings.ToLower(pathExt(e.Name))] {
					out = append(out, cloudVideo{name: e.Name, pickcode: e.PickCode, parentCid: cid})
					if len(out) >= max {
						return nil
					}
				}
			}
			offset += len(raw)
			if len(raw) == 0 || offset >= count || len(out) >= max {
				return nil
			}
		}
	}
	if err := walk(cid, depth); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 产物出口 ----

// cloudMetaOps 写网盘要用到的三件事（测试里换成桩）
type cloudMetaOps interface {
	fileEntryLister
	deleteFiles(fids []string) error
	upload(cid, name string, data []byte) error
}

// panCloudMetaOps 真实实现：列目录 / 删除走 pan115Ops（节流、事件抑制、缓存失效都在里面），
// 上传走不看总开关的那条（用户这一次勾了上传）
type panCloudMetaOps struct {
	*pan115Ops
	h *Handler
}

func (o panCloudMetaOps) upload(cid, name string, data []byte) error {
	return o.h.upload115FileConsented(o.cookie, parseI64(cid), name, data)
}

// fileScrapeStat 本次刮削的产物计数
type fileScrapeStat struct {
	Local    int `json:"local"`    // 写入本地
	Uploaded int `json:"uploaded"` // 写入网盘
	Skipped  int `json:"skipped"`  // 已存在、按「只补缺失」跳过
}

// fileScrapeWriter 手动刮削的产物出口：本地、网盘，或两者都写
type fileScrapeWriter struct {
	ops    cloudMetaOps
	force  bool
	upload bool
	// markHandled 本地文件已由这一次处理完（传过了，或用户说了这次不传）：登记上传指纹，
	// 监控上传与元数据回传引擎据此跳过它。nil = 不登记（测试）
	markHandled func(localPath string)

	dirs  map[string]string            // cid → 子目录名 → cid 的扁平缓存（键 cid+"/"+name）
	names map[string]map[string]string // 网盘目录 cid → 文件名 → fid
	stat  fileScrapeStat
	// localDirs 这次在本地写过东西的标题目录（刷 Emby 用）
	localDirs map[string]bool
}

func newFileScrapeWriter(ops cloudMetaOps, force, upload bool) *fileScrapeWriter {
	return &fileScrapeWriter{ops: ops, force: force, upload: upload,
		dirs: map[string]string{}, names: map[string]map[string]string{}, localDirs: map[string]bool{}}
}

func (w *fileScrapeWriter) put(d metaDest, name string, data []byte) (bool, error) {
	localPath := ""
	if d.Local != "" {
		if err := os.MkdirAll(d.Local, 0o755); err != nil {
			return false, err
		}
		wrote, err := writeMetaFile(d.Local, name, data, w.force)
		if err != nil {
			return false, err
		}
		if !wrote {
			// 本地已有且不覆盖：网盘上那份也不动（它要么早传过，要么归监控上传管）
			w.stat.Skipped++
			return false, nil
		}
		w.stat.Local++
		w.localDirs[d.Local] = true
		localPath = filepath.Join(d.Local, name)
	}
	if !w.upload || d.CloudBase == "" {
		if localPath != "" && w.markHandled != nil {
			w.markHandled(localPath)
		}
		return localPath != "", nil
	}
	cid, err := w.cloudDir(d)
	if err != nil {
		return localPath != "", err
	}
	existing, err := w.cloudNames(cid)
	if err != nil {
		return localPath != "", fmt.Errorf("读取网盘目录失败: %w", err)
	}
	if fid, ok := existing[name]; ok {
		if !w.force {
			if localPath == "" {
				w.stat.Skipped++
			} else if w.markHandled != nil {
				w.markHandled(localPath)
			}
			return localPath != "", nil
		}
		// 115 允许同目录同名文件并存，直接上传会留下两份：先把旧的送进回收站
		if fid != "" {
			if err := w.ops.deleteFiles([]string{fid}); err != nil {
				return localPath != "", fmt.Errorf("替换网盘上的旧文件失败: %w", err)
			}
		}
	}
	if err := w.ops.upload(cid, name, data); err != nil {
		return localPath != "", fmt.Errorf("上传网盘失败: %w", err)
	}
	existing[name] = "" // 刚传上去的拿不到 fid；同一次里不会再写同名文件
	w.stat.Uploaded++
	if localPath != "" && w.markHandled != nil {
		w.markHandled(localPath)
	}
	return true, nil
}

// cloudDir 解析网盘落点：从已知 cid 逐级按名字往下找，不建目录 ——
// 媒体库里的片目录一定已经在网盘上，找不到说明本地与网盘对不上，建一个只会更乱
func (w *fileScrapeWriter) cloudDir(d metaDest) (string, error) {
	cur := d.CloudBase
	rel := strings.Trim(d.CloudRel, "/")
	if rel == "" || rel == "." {
		return cur, nil
	}
	for _, seg := range strings.Split(rel, "/") {
		key := cur + "/" + seg
		if cid, ok := w.dirs[key]; ok {
			if cid == "" {
				return "", fmt.Errorf("网盘上没有对应目录 %s", rel)
			}
			cur = cid
			continue
		}
		items, _, err := listFileEntries(w.ops, cur, fileListLimit)
		if err != nil {
			return "", fmt.Errorf("读取网盘目录失败: %w", err)
		}
		names := map[string]string{}
		for _, it := range items {
			if it.IsDir {
				w.dirs[cur+"/"+it.Name] = it.ID
			} else {
				names[it.Name] = it.ID
			}
		}
		if _, ok := w.names[cur]; !ok {
			w.names[cur] = names // 顺手记下这一层的文件，后面查重不用再列一次
		}
		cid, ok := w.dirs[key]
		if !ok {
			w.dirs[key] = ""
			return "", fmt.Errorf("网盘上没有对应目录 %s", rel)
		}
		cur = cid
	}
	return cur, nil
}

// cloudNames 网盘目录里已有的文件（名字 → fid），每个目录只列一次
func (w *fileScrapeWriter) cloudNames(cid string) (map[string]string, error) {
	if m, ok := w.names[cid]; ok {
		return m, nil
	}
	items, _, err := listFileEntries(w.ops, cid, fileListLimit)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, it := range items {
		if it.IsDir {
			w.dirs[cid+"/"+it.Name] = it.ID
		} else {
			m[it.Name] = it.ID
		}
	}
	w.names[cid] = m
	return m, nil
}

// jobScrapeReporter 手动刮削的错误收集：进日志，也进任务结果
type jobScrapeReporter struct {
	n    int
	errs []string
}

func (r *jobScrapeReporter) errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[影视刮削] ✗ %s", msg)
	r.n++
	if len(r.errs) < 20 {
		r.errs = append(r.errs, msg)
	}
}

func (r *jobScrapeReporter) stopped() bool { return jobStopRequested() }

// fileScrapeResult 任务结果（前端任务详情里显示）
type fileScrapeResult struct {
	Titles   int            `json:"titles"`
	Stat     fileScrapeStat `json:"stat"`
	Problems []string       `json:"problems,omitempty"`
	Errors   []string       `json:"errors,omitempty"`
}

func execFileScrapeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	fp := p.Files
	if fp == nil || fp.Scrape == nil || len(fp.Items) == 0 {
		return jobOutcome{}, errors.New("任务参数错误")
	}
	o := *fp.Scrape
	defer resetFileListCache()

	tc, err := loadTmdbClient()
	if err != nil {
		return jobOutcome{}, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return jobOutcome{}, err
	}
	// 覆盖模式会删网盘上的旧元数据：登记抑制，别让增量同步回头把本地刚写的那份也删掉
	ops.suppress = true
	chain, err := h.resolveBrowseChain(fp.Cid, fp.Chain)
	if err != nil {
		if errors.Is(err, errDirGone) {
			return jobOutcome{}, errors.New("所选条目所在的目录已不存在（被删除或移动了），请刷新后重选")
		}
		return jobOutcome{}, err
	}

	pl := &fileScrapePlanner{h: h, ops: ops, tc: tc, roles: h.workspaceRoles(), chain: chain,
		parentCid: fp.Cid, localRoot: loadScrapeCfg().LocalRoot, upload: o.Upload}
	if p.TmdbID > 0 {
		media, err := tc.getByTmdbID(p.TmdbID, p.MediaType == "tv")
		if err != nil || media == nil {
			return jobOutcome{}, fmt.Errorf("拉取 TMDB 条目 %s/%d 失败: %v", p.MediaType, p.TmdbID, err)
		}
		media.MediaType = p.MediaType
		pl.pick = media
	}
	titles, problems := pl.plan(fp.Items)
	for _, pr := range problems {
		log.Printf("[影视刮削] ✗ %s", pr)
	}
	if len(titles) == 0 {
		if jobStopRequested() {
			return jobOutcome{Message: "已按要求停止，没有刮削任何片目", Canceled: true}, nil
		}
		return jobOutcome{}, errors.New(strings.Join(problems, "；"))
	}

	w := newFileScrapeWriter(panCloudMetaOps{pan115Ops: ops, h: h}, o.Force, o.Upload)
	w.markHandled = func(p string) {
		if st, ok := stampOfPath(p); ok {
			markUploadedStamp(h.DB, p, st)
		}
	}
	rep := &jobScrapeReporter{}
	cfg := scrapeCfg{WriteNFO: o.WriteNFO, WriteImages: o.WriteImages, Force: o.Force}
	where := "本地媒体库"
	if o.Upload {
		where += "，并上传网盘"
	}
	log.Printf("[影视刮削] ▶ 手动刮削 %d 个片目（%s，%s）", len(titles), where, map[bool]string{true: "强制覆盖", false: "只补缺失"}[o.Force])
	done, canceled := 0, false
	for i, t := range titles {
		if jobStopRequested() {
			canceled = true
			break
		}
		setJobProgress("刮削", i, len(titles), truncateStr(t.Title, 60))
		log.Printf("[影视刮削] ▶ %s (%s) [tmdb=%d]，视频 %d 个", t.Title, t.Year, t.TmdbID, len(t.Videos))
		scrapeTitleMeta(tc, cfg, t, w, rep)
		done = i + 1
		time.Sleep(150 * time.Millisecond) // TMDB 限速保护
	}
	setJobProgress("", done, progressKeep, "")

	// 本地写了新的元数据：通知 Emby 按路径刷新（只有媒体库里的片目才有本地产物）
	if len(w.localDirs) > 0 {
		dirs := make([]string, 0, len(w.localDirs))
		for d := range w.localDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		notifyEmbyPaths(dirs, embyRefreshAdded)
	}

	msg := fmt.Sprintf("刮削 %d 个片目：写入本地 %d 个、上传网盘 %d 个、已存在跳过 %d 个",
		done, w.stat.Local, w.stat.Uploaded, w.stat.Skipped)
	if rep.n > 0 {
		msg += fmt.Sprintf("；%d 处出错（见任务详情 / 实时日志）", rep.n)
	}
	if len(problems) > 0 {
		msg += fmt.Sprintf("；%d 项未能刮削：%s", len(problems), truncateStr(strings.Join(problems, "；"), 200))
	}
	if canceled {
		msg = "已按要求停止；" + msg
	}
	log.Printf("[影视刮削] ■ %s", msg)
	res := fileScrapeResult{Titles: done, Stat: w.stat, Problems: problems, Errors: rep.errs}
	if w.stat.Local+w.stat.Uploaded+w.stat.Skipped == 0 && rep.n > 0 && !canceled {
		return jobOutcome{Result: res}, errors.New(msg)
	}
	return jobOutcome{Message: msg, Result: res, Canceled: canceled}, nil
}
