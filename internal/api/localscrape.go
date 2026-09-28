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

// ==================== 本地文件页：刮削所选片目 ====================
//
// 刮削对象是台账里的片目（库名/<分类>/<标题目录>），产物写本地媒体库，与「开始刮削」同一份。
// 原来挂在网盘文件页上（按 115 目录勾选、台账里没有的直接写网盘），2026-09 起挪到本地文件页：
// 刮削只认本地已经有的片目，不再有「只写网盘」这条路。
//
// 选项默认取「自动整理 → 刮削」里保存的配置，前端可以只为这一次改。
// 「上传到网盘」只管这一次：勾了就当场把这次写出的文件传进网盘对应目录（upload115FileConsented，
// 不看监控上传总开关）；没勾就只写本地，传不传交给监控上传 —— 它开着会照常把新文件传上去。

func init() {
	jobExecutors["scrape"] = execLocalScrapeJob
}

// fileScrapeOpts 本次刮削的选项
type fileScrapeOpts struct {
	WriteNFO    bool `json:"write_nfo"`
	WriteImages bool `json:"write_images"`
	Force       bool `json:"force"`  // 覆盖已存在的元数据
	Upload      bool `json:"upload"` // 这一次把产物写进网盘
}

// localScrapeParams 刮削任务参数：台账片目 key（含库名前缀，与 ledgerTitleEntry.Key 同口径）
type localScrapeParams struct {
	Keys   []string       `json:"keys"`
	Scrape fileScrapeOpts `json:"scrape"`
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
		Kind: "scrape", Title: title, DedupeKey: truncateStr("local:"+strings.Join(keys, "|"), 240),
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
			Name:     strings.TrimSuffix(path.Base(sf.RelPath), ".strm"),
			PickCode: sf.PickCode,
			Dir:      metaDest{Local: filepath.Join(localRoot, filepath.FromSlash(dir)), CloudBase: libCid, CloudRel: cloudRel(dir)},
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

func execLocalScrapeJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job)
	lp := p.Local
	if lp == nil || len(lp.Keys) == 0 {
		// 老版本网盘文件页入队的刮削任务（参数在 Files 里）：那条入口已经移除
		return jobOutcome{}, errors.New("任务参数错误（网盘文件页的刮削已移到本地文件页，请在那里重新提交）")
	}
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

	ledger := scanLedgerTitles()
	var titles []scrapeTitle
	var problems []string
	for i, k := range lp.Keys {
		if jobStopRequested() {
			break
		}
		e := ledger[k]
		if e == nil {
			problems = append(problems, k+"：台账里已经没有这个片目（被移走或删除了）")
			continue
		}
		setJobProgress("识别", i, len(lp.Keys), truncateStr(e.Title, 60))
		t, err := ledgerScrapeTitle(tc, e, localRoot, libCid, pick)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：%v", e.Title, err))
			continue
		}
		titles = append(titles, t)
	}
	for _, pr := range problems {
		log.Printf("[影视刮削] ✗ %s", pr)
	}
	if len(titles) == 0 {
		if jobStopRequested() {
			return jobOutcome{Message: "已按要求停止，没有刮削任何片目", Canceled: true}, nil
		}
		return jobOutcome{}, errors.New(strings.Join(problems, "；"))
	}

	w := newFileScrapeWriter(cloud, o.Force, o.Upload)
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

	// 本地写了新的元数据：通知 Emby 按路径刷新
	if len(w.localDirs) > 0 {
		dirs := make([]string, 0, len(w.localDirs))
		for d := range w.localDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		notifyEmbyPaths(dirs, embyRefreshAdded)
		if !o.Upload {
			// 这一次没勾上传：交给监控上传（与「开始刮削」同口径，它们各自看自己的开关）
			go func() {
				time.Sleep(2 * time.Second) // 等最后写入落盘
				monitorOnce(h)
				h.uploadMetadataOnce()
			}()
		}
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
