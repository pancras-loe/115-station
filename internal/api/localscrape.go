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
	"gorm.io/gorm"
)

// ==================== 本地文件页：刮削所选片目 ====================
//
// 刮削对象是台账里的片目（库名/<分类>/<标题目录>），产物写本地媒体库，与「开始刮削」同一份。
// 原来挂在网盘文件页上（按 115 目录勾选、台账里没有的直接写网盘），2026-09 起挪到本地文件页：
// 刮削只认本地已经有的片目，不再有「只写网盘」这条路。
//
// 选项默认取「自动整理 → 刮削」里保存的配置，前端可以只为这一次改。
// 「上传到网盘」只管这一次：勾了就当场把这次写出的文件传进网盘对应目录（upload115FileConsented，
// 不看监控上传总开关）；没勾就只写本地，传不传交给监控上传 —— 它开着会照常把新文件传上去。

//
// 整理后自动刮削与「开始刮削」全库也走这里的执行器（execScrapeJob），只是参数不同：
// 前者带整理当时识别到的条目（Hints），后者 All=true 执行时现取台账。三者都在刮削队列上跑，不拿 taskMu。

func init() {
	jobExecutors[jobKindScrape] = execScrapeJob
}

// fileScrapeOpts 本次刮削的选项
type fileScrapeOpts struct {
	WriteNFO    bool `json:"write_nfo"`
	WriteImages bool `json:"write_images"`
	Force       bool `json:"force"`  // 覆盖已存在的元数据
	Upload      bool `json:"upload"` // 这一次把产物写进网盘
	// Probe 刮完让 Emby 对这些片目里还没有媒体信息的条目提前探测（scrapeCfg.ProbeStreams，界面叫「轨道探测」），
	// 见 embyextract.go。字段名沿用：它原来是本站自己 ffprobe 写 NFO streamdetails 的开关，那条已删
	Probe bool `json:"probe"`
	// SkipSharedStills 同一季多集共用的剧照判为占位图，不写（scrapeCfg.SkipSharedStills）
	SkipSharedStills bool `json:"skip_shared_stills"`
}

// localScrapeParams 刮削任务参数：台账片目 key（含库名前缀，与 ledgerTitleEntry.Key 同口径）
type localScrapeParams struct {
	Keys   []string       `json:"keys,omitempty"`
	Scrape fileScrapeOpts `json:"scrape"`
	// All 全库：执行时现取台账里带 TMDB 编号的片目（排队期间入库的也算上）
	All bool `json:"all,omitempty"`
	// Hints 整理后刮削：整理当时识别到的条目。重命名模板不带 {tmdbid} 时目录名里没有编号，
	// 光看台账认不出来，而整理手上明明有
	Hints map[string]scrapeHint `json:"hints,omitempty"`
	// EmbyRefresh / EmbyVerify 整理交过来的 Emby 刷新（本地绝对路径）与回查样本（落盘的 .strm）。
	// 整理后紧接着就要刮，整理自己刷一次、刮完再刷一次，等于十几秒里两次整库刷新，
	// 刮削期间写 NFO / 图片还一直推后 Emby 文件监控的静默期（2026-09-28 现场：
	// 两次刷新之后两分钟 Emby 连目录条目都没建）。所以交给刮削，写完元数据只刷一次
	EmbyRefresh []string `json:"emby_refresh,omitempty"`
	EmbyVerify  []string `json:"emby_verify,omitempty"`
}

// scrapeHint 整理时识别到的片目信息
type scrapeHint struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Year   string `json:"year,omitempty"`
	TmdbID int    `json:"tmdb_id"`
}

// localScrapeMax 一次最多刮多少部：再多就该用「开始刮削」跑全库
const localScrapeMax = 500

// ScrapeLocalTitles POST /local/scrape → 入队，202
// body: {keys:[...], scrape:{write_nfo,write_images,force,upload}, tmdb_id?, media_type?, label?}
func (h *Handler) ScrapeLocalTitles(c *gin.Context) {
	var req struct {
		Keys      []string        `json:"keys"`
		Scrape    *fileScrapeOpts `json:"scrape"`
		TmdbID    int             `json:"tmdb_id"`
		MediaType string          `json:"media_type"`
		Label     string          `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	keys := normalizeTitleKeys(req.Keys)
	switch {
	case len(keys) == 0:
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有选择片目"})
		return
	case len(keys) > localScrapeMax:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("一次最多刮削 %d 部，全库请用「自动整理 → 刮削」里的开始刮削", localScrapeMax)})
		return
	case req.Scrape == nil || (!req.Scrape.WriteNFO && !req.Scrape.WriteImages):
		c.JSON(http.StatusBadRequest, gin.H{"error": "NFO 与图片至少要生成一项"})
		return
	case req.TmdbID > 0 && len(keys) > 1:
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定 TMDB 条目时一次只能刮削一部"})
		return
	case req.TmdbID > 0 && req.MediaType != "movie" && req.MediaType != "tv":
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定 TMDB 条目时要带上类型"})
		return
	}
	if localMediaRoot() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置本地媒体库根目录"})
		return
	}
	if _, err := loadTmdbClient(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ledger := scanLedgerTitlesCached()
	for _, k := range keys {
		if ledger[k] == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "台账里没有「" + k + "」，请刷新列表后重选"})
			return
		}
	}

	title := "刮削" + localScrapeJobTitle(ledger, keys)
	if req.TmdbID > 0 {
		title += " → " + pickLabel(pickReq{TmdbID: req.TmdbID, MediaType: req.MediaType, Label: req.Label})
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind: jobKindScrape, Title: title, DedupeKey: truncateStr("local:"+strings.Join(keys, "|"), 240),
		Source: "web", Priority: jobPriorityManual,
		Params: jobParams{TmdbID: req.TmdbID, MediaType: req.MediaType,
			Local: &localScrapeParams{Keys: keys, Scrape: *req.Scrape}},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "刮削")
}

// normalizeTitleKeys 去空、去首尾斜杠、去重并排序（去重键稳定）
func normalizeTitleKeys(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.Trim(strings.TrimSpace(k), "/")
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// localScrapeJobTitle 任务标题：「《片名》」或「《片名》等 N 部」
func localScrapeJobTitle(ledger map[string]*ledgerTitleEntry, keys []string) string {
	name := path.Base(keys[0])
	if e := ledger[keys[0]]; e != nil && e.Title != "" {
		name = e.Title
	}
	s := "《" + truncateStr(name, 40) + "》"
	if len(keys) > 1 {
		s += fmt.Sprintf("等 %d 部", len(keys))
	}
	return s
}

// ledgerScrapeTitle 台账片目 → 刮削对象（本地落点 + 网盘对应目录）。
// pick 非空 = 用户指定了 TMDB 条目；目录名里没有编号时按目录名识别一次
func ledgerScrapeTitle(tc *TmdbClient, e *ledgerTitleEntry, localRoot, libCid string, pick *TmdbMedia) (scrapeTitle, error) {
	t := scrapeTitle{Kind: e.MediaType, Title: e.Title, Year: e.Year, TmdbID: e.TmdbID}
	switch {
	case pick != nil:
		t.Kind, t.TmdbID, t.Title, t.Year = pick.MediaType, pick.TmdbID, pick.Title, pick.Year
	case t.TmdbID <= 0:
		// 目录名里没有 {tmdbid=…}：「开始刮削」会跳过这种片目，这里是用户点名要刮的，按目录名识别一次
		parsed := parseFileName(path.Base(e.Key))
		parsed.IsTV = parsed.IsTV || e.MediaType == "tv"
		media, err := tc.recognize(parsed)
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
	// 网盘落点：媒体库根 cid + 去掉库名的相对路径（本地第一层是库名，网盘上库名就是媒体库根本身）
	cloudRel := func(rel string) string {
		rel = strings.Trim(rel, "/")
		if e.LibName == "" {
			return rel
		}
		if rel == e.LibName {
			return ""
		}
		return strings.TrimPrefix(rel, e.LibName+"/")
	}
	t.Dir = metaDest{Local: filepath.Join(localRoot, filepath.FromSlash(e.Key)), CloudBase: libCid, CloudRel: cloudRel(e.Key)}
	for _, sf := range scrapeDirVideoRows(e.Key) {
		dir := path.Dir(sf.RelPath)
		t.Videos = append(t.Videos, scrapeVideo{
			Name: strings.TrimSuffix(path.Base(sf.RelPath), ".strm"),
			Dir:  metaDest{Local: filepath.Join(localRoot, filepath.FromSlash(dir)), CloudBase: libCid, CloudRel: cloudRel(dir)},
		})
	}
	return t, nil
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

// fileScrapeWriter 手动刮削的产物出口：写本地，勾了上传再写网盘
type fileScrapeWriter struct {
	ops    cloudMetaOps
	force  bool
	upload bool
	// markHandled 本地文件已由这一次传进网盘（或网盘上早有同名的）：登记上传指纹，
	// 监控上传与元数据回传引擎据此跳过它。没勾上传时不登记 —— 那是交给监控上传的。nil = 不登记（测试）
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

// skip 与 put 里「本地已有且不覆盖」那一支同口径（网盘那份同样不动）。
// 只有网盘落点时要列目录才知道，这里不为省一次拉图去多发 115 请求，交给 put 判断
func (w *fileScrapeWriter) skip(d metaDest, name string) bool {
	if w.force || d.Local == "" || !localMetaExists(d.Local, name) {
		return false
	}
	w.stat.Skipped++
	return true
}

// errMetaDirGone 本地落点目录已不在。刮削不建目录：媒体库里的片目目录一定已经有 STRM，
// 目录没了说明刮削期间片目被整理 / 洗版 / 深删 / 增量挪走了，建一个只会在旧位置留下
// 只有 NFO 和海报的空壳，Emby 把它认成一部没有视频的剧
var errMetaDirGone = errors.New("本地目录已不在")

func (w *fileScrapeWriter) put(d metaDest, name string, data []byte) (bool, error) {
	localPath := ""
	if d.Local != "" {
		if st, err := os.Stat(d.Local); err != nil || !st.IsDir() {
			return false, errMetaDirGone
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

// jobScrapeReporter 刮削任务的错误收集：进日志，也进任务结果；停止与进度走刮削队列
type jobScrapeReporter struct {
	n    int
	errs []string
}

// scrapeMaxErrs 任务结果里最多留几条错误（完整的在日志里）
const scrapeMaxErrs = 50

func (r *jobScrapeReporter) errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[影视刮削]   ✗ %s", msg)
	r.n++
	if len(r.errs) < scrapeMaxErrs {
		r.errs = append(r.errs, msg)
	}
}

func (r *jobScrapeReporter) stopped() bool { return scrapeLane.stopRequested() }

func (r *jobScrapeReporter) sub(phase string, done, total int, label string) {
	scrapeLane.setSub(phase, done, total, label)
}

// fileScrapeResult 任务结果（前端任务详情里显示）
type fileScrapeResult struct {
	Titles      int            `json:"titles"`
	Stat        fileScrapeStat `json:"stat"`
	Reused      int            `json:"reused,omitempty"`      // 复用本次已下载的图
	Placeholder int            `json:"placeholder,omitempty"` // 判为占位剧照没写的集
	Reclaimed   int            `json:"reclaimed,omitempty"`   // 片目被挪走后收回的文件
	Problems    []string       `json:"problems,omitempty"`
	Errors      []string       `json:"errors,omitempty"`
}

// scrapeTarget 一个待刮片目：台账条目 + 整理时的识别结果（可无）
type scrapeTarget struct {
	key  string
	e    *ledgerTitleEntry
	hint *scrapeHint
}

// scrapeTargets 按任务参数列出要刮的片目。All = 台账里带 TMDB 编号的全部片目
func scrapeTargets(lp *localScrapeParams, ledger map[string]*ledgerTitleEntry) (targets []scrapeTarget, problems []string, noID int) {
	if lp.All {
		for k, e := range ledger {
			if e.TmdbID <= 0 {
				noID++ // 全库刮削只刮带编号的（与改造前一致），按片名猜太容易刮错
				continue
			}
			targets = append(targets, scrapeTarget{key: k, e: e})
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].key < targets[j].key })
		return targets, nil, noID
	}
	for _, k := range lp.Keys {
		var hint *scrapeHint
		if h, ok := lp.Hints[k]; ok && h.TmdbID > 0 {
			hint = &h
		}
		e := ledger[k]
		if e == nil && hint != nil {
			// 台账口径的 key 与整理算的一致（库名/分类/标题）；老台账不带库名前缀时对不上，按识别结果补一条
			e = &ledgerTitleEntry{Key: k, Title: hint.Title, Year: hint.Year, TmdbID: hint.TmdbID, MediaType: hint.Kind}
			if i := strings.Index(k, "/"); i > 0 {
				e.LibName = k[:i]
			}
		}
		if e == nil {
			problems = append(problems, k+"：台账里已经没有这个片目（被移走或删除了）")
			continue
		}
		targets = append(targets, scrapeTarget{key: k, e: e, hint: hint})
	}
	return targets, problems, 0
}

func execScrapeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	lp := p.Local
	if lp == nil || (len(lp.Keys) == 0 && !lp.All) {
		// 老版本网盘文件页入队的刮削任务（参数在 Files 里）：那条入口已经移除
		return jobOutcome{}, errors.New("任务参数错误（网盘文件页的刮削已移到本地文件页，请在那里重新提交）")
	}
	// 本地写了新的元数据要刷 Emby；整理交过来的刷新不论这次刮成什么样都要做（见 scrapeEmbyRefresh）
	var wrote map[string]bool
	// 刮到的片目交给 Emby 提前探测。defer 在刷新之前注册、因此在它之后执行。
	// 整理后自动刮削（scrapeAutoDedupe）不排：那些片目刚入库，入库确认那条入口会排，
	// 这里再排一次就是同一批条目进两次队列（防重复探测，见 embyextract.go 文件头）
	var extract []string
	defer func() {
		if len(extract) > 0 {
			queueEmbyExtract(extract...)
		}
	}()
	defer func() { scrapeEmbyRefresh(lp, wrote) }()
	o := lp.Scrape
	localRoot := localMediaRoot()
	if localRoot == "" {
		return jobOutcome{}, errors.New("未配置本地媒体库根目录")
	}
	defer forgetLocalTitles()

	tc, err := loadTmdbClient()
	if err != nil {
		return jobOutcome{}, err
	}
	var pick *TmdbMedia
	if p.TmdbID > 0 {
		media, err := tc.getByTmdbID(p.TmdbID, p.MediaType == "tv")
		if err != nil || media == nil {
			return jobOutcome{}, fmt.Errorf("拉取 TMDB 条目 %s/%d 失败: %v", p.MediaType, p.TmdbID, err)
		}
		media.MediaType = p.MediaType
		pick = media
	}

	// 只有这一次要上传才需要 115：不上传的刮削零 115 请求，没配账号也能刮
	var cloud cloudMetaOps
	libCid := ""
	if o.Upload {
		ops, err := h.newPan115Ops()
		if err != nil {
			return jobOutcome{}, err
		}
		// 覆盖模式会删网盘上的旧元数据：登记抑制，别让增量同步回头把本地刚写的那份也删掉
		ops.suppress = true
		cloud = panCloudMetaOps{pan115Ops: ops, h: h}
		for cid, r := range h.workspaceRoles() {
			if r == "library" {
				libCid = cid
			}
		}
		if libCid == "" {
			return jobOutcome{}, errors.New("没有配置网盘媒体库目录，无法上传")
		}
		defer resetFileListCache()
	}

	targets, problems, noID := scrapeTargets(lp, scanLedgerTitles())
	if noID > 0 {
		log.Printf("[影视刮削] ○ %d 个片目的目录名里没有 TMDB 编号，全库刮削跳过（可在本地文件页逐个指定条目刮削）", noID)
	}

	w := newFileScrapeWriter(cloud, o.Force, o.Upload)
	wrote = w.localDirs
	w.markHandled = func(p string) {
		if st, ok := stampOfPath(p); ok {
			markUploadedStamp(h.DB, p, st)
		}
	}
	rep := &jobScrapeReporter{}
	sess := newScrapeSession(tc, o, w, rep)
	where := "本地媒体库"
	if o.Upload {
		where += "，并上传网盘"
	}
	log.Printf("[影视刮削] ▶ %s：%d 个片目（%s，%s，NFO %s · 图片 %s · Emby 提前探测 %s · 占位剧照 %s）",
		job.Title, len(targets), where, map[bool]string{true: "强制覆盖", false: "只补缺失"}[o.Force],
		onOff(o.WriteNFO), onOff(o.WriteImages), onOff(o.Probe), map[bool]string{true: "不写", false: "照写"}[o.SkipSharedStills])

	res := fileScrapeResult{}
	done, canceled := 0, false
	for i, tg := range targets {
		if rep.stopped() {
			canceled = true
			break
		}
		name := tg.e.Title
		scrapeLane.set("刮削", i, len(targets), truncateStr(name, 60))
		// 片目逐个现解析（视频列表现查台账），不在开头一次性算好：
		// 全库几百部刮下来要几个小时，开头算的列表到后面早就过时了
		e := *tg.e
		if tg.hint != nil {
			e.MediaType, e.Title, e.Year, e.TmdbID = tg.hint.Kind, tg.hint.Title, tg.hint.Year, tg.hint.TmdbID
		}
		t, err := ledgerScrapeTitle(tc, &e, localRoot, libCid, pick)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：%v", name, err))
			log.Printf("[影视刮削] ✗ %s：%v", name, err)
			continue
		}
		if !localHasStrm(t.Dir.Local) {
			problems = append(problems, name+"：本地片目目录里已经没有 STRM（被移走或删除了）")
			log.Printf("[影视刮削] ○ %s：本地片目目录里已经没有 STRM，跳过 → %s", name, t.Dir.Local)
			continue
		}
		start := time.Now()
		log.Printf("[影视刮削] ▶ 《%s》(%s) [tmdb=%d] %s，视频 %d 个 → %s",
			t.Title, t.Year, t.TmdbID, map[string]string{"tv": "剧集", "movie": "电影"}[t.Kind], len(t.Videos), t.Dir.Local)
		st := sess.scrapeTitleMeta(t)
		if o.Probe && job.DedupeKey != scrapeAutoDedupe {
			if cfg, ok := loadEmbyRefreshCfg(); ok {
				extract = append(extract, embyPathOf(cfg, t.Dir.Local))
			}
		}
		reclaimed := scrapeCompensate(t, st.written, localRoot)
		res.Reused += st.Reused
		res.Placeholder += st.Placeholder
		res.Reclaimed += reclaimed
		mark := "✓"
		if st.Failed > 0 || st.Gone {
			mark = "○"
		}
		line := fmt.Sprintf("[影视刮削] %s 《%s》：写入 %d 个", mark, t.Title, len(st.Wrote))
		if sm := st.summary(); sm != "" {
			line += " [" + sm + "]"
		}
		line += fmt.Sprintf("，已有跳过 %d", st.Skipped)
		if st.Reused > 0 {
			line += fmt.Sprintf("，复用已下载 %d", st.Reused)
		}
		if st.Placeholder > 0 {
			line += fmt.Sprintf("，占位剧照 %d 集未写", st.Placeholder)
		}
		line += fmt.Sprintf("，失败 %d，用时 %s", st.Failed, time.Since(start).Round(time.Second))
		log.Print(line)
		if reclaimed > 0 {
			log.Printf("[影视刮削] ○ 《%s》刮削期间片目（或其中几集）被移走 / 删除，已收回这次写下的 %d 个文件", t.Title, reclaimed)
		}
		done = i + 1
		time.Sleep(150 * time.Millisecond) // TMDB 限速保护
	}
	scrapeLane.set("", done, progressKeep, "")
	for _, pr := range problems {
		if len(res.Problems) < scrapeMaxErrs {
			res.Problems = append(res.Problems, pr)
		}
	}

	// Emby 刷新在开头的 defer 里（scrapeEmbyRefresh）
	if len(w.localDirs) > 0 && !o.Upload {
		// 这一次没勾上传：交给监控上传（它看自己的开关）
		go func() {
			time.Sleep(2 * time.Second) // 等最后写入落盘
			monitorOnce(h)
			h.uploadMetadataOnce()
		}()
	}

	msg := fmt.Sprintf("刮削 %d 个片目：写入本地 %d 个、上传网盘 %d 个、已存在跳过 %d 个",
		done, w.stat.Local, w.stat.Uploaded, w.stat.Skipped)
	if res.Placeholder > 0 {
		msg += fmt.Sprintf("、占位剧照未写 %d 集", res.Placeholder)
	}
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
	res.Titles, res.Stat, res.Errors = done, w.stat, rep.errs
	if len(targets) == 0 && len(problems) > 0 {
		return jobOutcome{Result: res}, errors.New(strings.Join(problems, "；"))
	}
	if w.stat.Local+w.stat.Uploaded+w.stat.Skipped == 0 && rep.n > 0 && !canceled {
		return jobOutcome{Result: res}, errors.New(msg)
	}
	// 整理后刮削什么都没写（全都已有）：后台任务不留行，免得每轮整理都添一条「跳过 N 个」
	idle := job.Priority == jobPriorityBackground && w.stat.Local+w.stat.Uploaded == 0 && rep.n == 0 && len(problems) == 0
	return jobOutcome{Message: msg, Result: res, Canceled: canceled, Idle: idle}, nil
}

// localHasStrm 本地目录（含子目录）里还有没有 STRM：片目还在不在原位的判据
func localHasStrm(dir string) bool {
	if dir == "" {
		return false
	}
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".strm") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// scrapeCompensate 刮完一部后核对它还在不在原位，返回收回的文件数。
//
// 刮削不拿 taskMu（taskqueue.go），整理 / 洗版 / 深删 / 增量可能正好在这期间把片目或其中几集
// 挪走、删掉。本地 STRM 不在了，这次写下的元数据就成了孤儿 —— 留着的话空目录删不掉，
// Emby 会把只剩 NFO 和海报的壳认成一部没有视频的剧。所以：
//   - 整个片目没有 STRM 了 → 这次写的全收回；
//   - 片目还在但某集的 STRM 没了（洗版换了文件名）→ 收回那一集的 NFO / 剧照。
//
// 只删这一次写下的，此前就有的不碰（那归删片目的一方管，与改造前一致）；
// 删完沿父目录往上收空目录，止于本地媒体库根。
// 选这种「事后收拾」而不是片目锁：删改本地的入口有十几处（增量还是拿着 taskMu 删的），
// 让它们等刮削放锁，几百集的剧又能把主队列卡住
func scrapeCompensate(t scrapeTitle, written []string, localRoot string) int {
	if len(written) == 0 {
		return 0
	}
	titleAlive := localHasStrm(t.Dir.Local)
	owner := map[string]scrapeVideo{} // 集级产物的本地路径 → 所属视频
	for _, v := range t.Videos {
		if v.Dir.Local == "" {
			continue
		}
		owner[filepath.Join(v.Dir.Local, v.Name+".nfo")] = v
		owner[filepath.Join(v.Dir.Local, v.Name+"-thumb.jpg")] = v
	}
	removed := 0
	dirs := map[string]bool{}
	for _, p := range written {
		orphan := !titleAlive
		if !orphan {
			if v, ok := owner[p]; ok && !fileExists(filepath.Join(v.Dir.Local, v.Name+".strm")) {
				orphan = true
			}
		}
		if !orphan {
			continue
		}
		if err := os.Remove(p); err == nil || os.IsNotExist(err) {
			removed++
			dirs[filepath.Dir(p)] = true
		} else {
			log.Printf("[影视刮削] ✗ 收回孤儿元数据失败 %s: %v", p, err)
		}
	}
	for d := range dirs {
		removeEmptyParents(d, localRoot)
	}
	return removed
}

// ---- 整理后自动刮削 ----

// scrapeAutoDedupe 整理后刮削的去重键：还没开始刮的几轮整理并成一个任务
const scrapeAutoDedupe = "auto"

// enqueueAutoScrape 整理完成后把本轮动过的片目丢进刮削队列就返回。
// 此前在整理任务里当场刮，一部几百集的综艺刮完才放 taskMu，这段时间整理 / 同步全在排队。
//
// refresh / verify 非空表示整理想把 Emby 刷新交给这个刮削任务；返回 true 才算交接成功，
// 否则调用方自己刷。只在刮削队列空闲时接：前面排着全库刮削的话，
// 等它跑完新片要晚几个小时才进 Emby，不如整理当场刷
func enqueueAutoScrape(db *gorm.DB, jobs []scrapeJob, cfg scrapeCfg, refresh, verify []string) (handedOff bool) {
	p := &localScrapeParams{Scrape: cfg.opts(), Hints: map[string]scrapeHint{}}
	for _, j := range jobs {
		p.Keys = append(p.Keys, j.Key)
		p.Hints[j.Key] = scrapeHint{Kind: j.Kind, Title: j.Title, Year: j.Year, TmdbID: j.TmdbID}
	}
	p.Keys = normalizeTitleKeys(p.Keys)
	if len(refresh) > 0 && scrapeLaneIdle(db) {
		p.EmbyRefresh, p.EmbyVerify = refresh, verify
	}
	job, err := enqueueJob(db, jobSpec{
		Kind: jobKindScrape, Title: autoScrapeTitle(p), DedupeKey: scrapeAutoDedupe,
		Source: "organize", Priority: jobPriorityBackground,
		Params: jobParams{Local: p}, Merge: mergeAutoScrape,
	})
	if err != nil {
		log.Printf("[影视刮削] ✗ 整理后刮削入队失败: %v", err)
		return false
	}
	tail := ""
	if len(p.EmbyRefresh) > 0 {
		tail = "，刮完再刷 Emby"
	}
	log.Printf("[影视刮削] ○ 整理完成，%d 个片目加入刮削队列（任务 #%d，与整理分开执行，不占任务锁%s）", len(p.Keys), job.ID, tail)
	wakeJobWorker()
	return len(p.EmbyRefresh) > 0
}

// scrapeLaneIdle 刮削队列上没有正在跑的任务，也没有排在整理后刮削前面的
// （手动刮削优先级更高，会插到它前面）
func scrapeLaneIdle(db *gorm.DB) bool {
	if id, _ := scrapeLane.current(); id != 0 {
		return false
	}
	var n int64
	db.Model(&model.TaskJob{}).
		Where("kind = ? AND status = ? AND dedupe_key <> ?", jobKindScrape, jobQueued, scrapeAutoDedupe).
		Count(&n)
	return n == 0
}

// scrapeEmbyRefresh 刮削收尾刷 Emby：这次写过元数据的目录，加上整理交过来的。
// 整理交过来的那部分整理自己没刷，所以刮削出错、被停下、什么都没写都得刷
func scrapeEmbyRefresh(lp *localScrapeParams, wrote map[string]bool) {
	set := map[string]bool{}
	for d := range wrote {
		set[d] = true
	}
	if lp != nil {
		for _, d := range lp.EmbyRefresh {
			set[d] = true
		}
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return
	}
	sort.Strings(dirs)
	var verify []string
	if lp != nil {
		verify = lp.EmbyVerify
	}
	scrapeEmbyNotify(dirs, verify...)
}

// scrapeEmbyNotify 测试替身的缝
var scrapeEmbyNotify = func(dirs []string, verify ...string) {
	notifyEmbyPaths(dirs, embyRefreshAdded, verify...)
}

// mergeAutoScrape 排着的整理后刮削还没开始，又来一轮：片目取并集，选项以新的为准
func mergeAutoScrape(prev, next jobParams) (jobParams, string) {
	out := next
	if prev.Local == nil || next.Local == nil {
		return out, autoScrapeTitle(next.Local)
	}
	lp := *next.Local
	lp.Keys = normalizeTitleKeys(append(append([]string{}, prev.Local.Keys...), next.Local.Keys...))
	lp.Hints = map[string]scrapeHint{}
	for k, v := range prev.Local.Hints {
		lp.Hints[k] = v
	}
	for k, v := range next.Local.Hints {
		lp.Hints[k] = v
	}
	// 上一轮交过来的 Emby 刷新不能丢：那一轮整理没自己刷
	lp.EmbyRefresh = dedupeStrings(append(append([]string{}, prev.Local.EmbyRefresh...), next.Local.EmbyRefresh...))
	lp.EmbyVerify = dedupeStrings(append(append([]string{}, prev.Local.EmbyVerify...), next.Local.EmbyVerify...))
	out.Local = &lp
	return out, autoScrapeTitle(&lp)
}

// autoScrapeTitle 「整理后刮削《片名》等 N 部」
func autoScrapeTitle(p *localScrapeParams) string {
	if p == nil || len(p.Keys) == 0 {
		return "整理后刮削"
	}
	name := path.Base(p.Keys[0])
	if h, ok := p.Hints[p.Keys[0]]; ok && h.Title != "" {
		name = h.Title
	}
	s := "整理后刮削《" + truncateStr(name, 40) + "》"
	if len(p.Keys) > 1 {
		s += fmt.Sprintf("等 %d 部", len(p.Keys))
	}
	return s
}
