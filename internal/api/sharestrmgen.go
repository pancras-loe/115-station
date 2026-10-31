package api

// 分享 STRM 的生成（kind sharestrm，秒传与分享队列 xferLane，不拿 taskMu）。
// 一次生成 = 列整个分享 → 按文件算本地位置 → 写 STRM / 下附属文件 → 记 ShareFile → 通知 Emby。
// 「立即检查」就是再跑一次：内容一样的不重写，分享者改了名的挪到新位置（旧的是本站写的，删掉没问题）。
// 阶段 1 不删「分享里没了」的文件：要连续两轮确认（SHARE-STRM-PLAN.md §7.2，阶段 2），这里只报个数。

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
)

func init() { jobExecutors[jobKindShareStrm] = execShareStrmJob }

type shareStrmJobParams struct {
	SourceID uint `json:"source_id"`
}

// ---- 算本地位置（纯函数）----

// shareFilePlan 分享里的一个文件这次怎么处理
type shareFilePlan struct {
	Entry shareEntry
	Kind  string // video / asset
	Rel   string // 本地相对分享 STRM 根的路径（视频是 .strm）
}

type sharePlanOpts struct {
	LocalDir string
	MinSize  int64
	Assets   bool
	// Owned 自己库里已经有这一份（sha1）：不建 STRM
	Owned func(sha1 string) bool
}

type sharePlanSkip struct {
	Small int `json:"small"` // 比最小体积小
	Owned int `json:"owned"` // 库里已有
	Other int `json:"other"` // 不是视频、也不是要下的附属文件
	Bad   int `json:"bad"`   // 名字落不成本地路径
}

// planShareFiles 分享条目 → 本地位置。分享根下只有一个目录时去掉这一层：
// 本地目录本来就按分享标题起名，再套一层就成了「剧名/剧名/Season 01」
func planShareFiles(entries []shareEntry, o sharePlanOpts) ([]shareFilePlan, sharePlanSkip) {
	strip := ""
	var top []shareEntry
	for _, e := range entries {
		if e.Dir == "" {
			top = append(top, e)
		}
	}
	if len(top) == 1 && top[0].IsDir {
		strip = top[0].Name
	}
	var plans []shareFilePlan
	var skip sharePlanSkip
	used := map[string]bool{}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		dir := e.Dir
		if strip != "" {
			if dir == strip {
				dir = ""
			} else if strings.HasPrefix(dir, strip+"/") {
				dir = strings.TrimPrefix(dir, strip+"/")
			}
		}
		var segs []string
		for _, s := range strings.Split(dir, "/") {
			if s = strings.Trim(sanitizeName(s), ". "); s != "" {
				segs = append(segs, s)
			}
		}
		name := sanitizeName(e.Name)
		if name == "" || name == "." || name == ".." {
			skip.Bad++
			continue
		}
		kind := ""
		switch classifyFile(e.Name) {
		case FileTypeVideo:
			kind = "video"
		case FileTypeSubtitle, FileTypeNFO, FileTypeStdImage:
			if o.Assets {
				kind = "asset"
			}
		}
		if kind == "" {
			skip.Other++
			continue
		}
		if kind == "video" {
			if o.MinSize > 0 && e.Size > 0 && e.Size < o.MinSize {
				skip.Small++
				continue
			}
			if o.Owned != nil && e.Sha1 != "" && o.Owned(e.Sha1) {
				skip.Owned++
				continue
			}
		}
		file := name
		if kind == "video" {
			file = strmNameOf(name)
		}
		rel, ok := shareRelUnder(append(append([]string{o.LocalDir}, segs...), file)...)
		if ok && used[strings.ToLower(rel)] && kind == "video" {
			// 同目录同基名的两个视频（X.mkv / X.mp4）：后来的退回旧写法 X.mp4.strm（同 strmNameFor）
			rel, ok = shareRelUnder(append(append([]string{o.LocalDir}, segs...), legacyStrmNameOf(name))...)
		}
		if !ok || used[strings.ToLower(rel)] {
			skip.Bad++
			continue
		}
		used[strings.ToLower(rel)] = true
		plans = append(plans, shareFilePlan{Entry: e, Kind: kind, Rel: rel})
	}
	return plans, skip
}

// ---- 执行 ----

// shareStrmResult 一次生成的结果（落 TaskJob.Result）
type shareStrmResult struct {
	Title     string        `json:"title"`
	Videos    int           `json:"videos"`    // 这次要写的视频
	Written   int           `json:"written"`   // 新写或内容变了重写的 STRM
	Unchanged int           `json:"unchanged"` // 内容一样没动
	Moved     int           `json:"moved"`     // 分享者改了名、挪到新位置的
	Assets    int           `json:"assets"`    // 下载的附属文件
	AssetSkip int           `json:"asset_skip"`
	Missing   int           `json:"missing"` // 以前写过、这次分享里没有的（阶段 1 只报数，不删）
	Skipped   sharePlanSkip `json:"skipped"`
	Errors    []string      `json:"errors,omitempty"`
}

func (r *shareStrmResult) summary() string {
	parts := []string{fmt.Sprintf("「%s」%d 个视频", r.Title, r.Videos)}
	if r.Written > 0 {
		parts = append(parts, fmt.Sprintf("写入 STRM %d", r.Written))
	}
	if r.Unchanged > 0 {
		parts = append(parts, fmt.Sprintf("没变 %d", r.Unchanged))
	}
	if r.Moved > 0 {
		parts = append(parts, fmt.Sprintf("改名挪动 %d", r.Moved))
	}
	if r.Assets > 0 {
		parts = append(parts, fmt.Sprintf("附属文件 %d", r.Assets))
	}
	if r.Skipped.Owned > 0 {
		parts = append(parts, fmt.Sprintf("库里已有 %d", r.Skipped.Owned))
	}
	if r.Skipped.Small > 0 {
		parts = append(parts, fmt.Sprintf("太小跳过 %d", r.Skipped.Small))
	}
	if r.Missing > 0 {
		parts = append(parts, fmt.Sprintf("分享里已没有 %d（本地暂留）", r.Missing))
	}
	if n := len(r.Errors); n > 0 {
		parts = append(parts, fmt.Sprintf("出错 %d", n))
	}
	return strings.Join(parts, "，")
}

// shareStrmRun 一次生成要用的东西（测试换成假的）
type shareStrmRun struct {
	db      *gorm.DB
	src     *model.ShareSource
	cfg     shareStrmCfg
	root    string // 分享 STRM 根（绝对路径）
	domain  string
	format  string
	keepExt bool
	cookie  string

	walk     func(code, receive, cookie string, maxDirs int) ([]shareEntry, string, bool, error)
	download func(cookie, code, receive, fid string) (shareDownInfo, error)
	fetch    func(u string, max int64) ([]byte, error)
	owned    func(sha1 string) bool
	stop     func() bool
	set      func(phase string, done, total int, label string)
	now      func() time.Time
}

func (r *shareStrmRun) abs(rel string) string { return filepath.Join(r.root, filepath.FromSlash(rel)) }

// run 返回结果、是否被停止、失败原因。分享层面的错误（失效、提取码错）记在 ShareSource 上
func (r *shareStrmRun) run() (shareStrmResult, bool, error) {
	src := r.src
	res := shareStrmResult{Title: firstNonEmpty(src.Title, src.ShareCode)}
	r.set("列分享", 0, 0, res.Title)
	entries, title, truncated, err := r.walk(src.ShareCode, src.ReceiveCode, r.cookie, shareWalkMaxDirs)
	if err == nil && truncated {
		err = fmt.Errorf("分享太大：超过 %d 个目录，没有生成（只按列到的一半生成，Emby 里会缺一截）", shareWalkMaxDirs)
	}
	if err != nil {
		r.markSource(shareStateOfErr(err), err.Error(), shareStateOfErrRaw(err))
		return res, false, err
	}
	if title != "" {
		res.Title = title
	}
	minSize := int64(r.cfg.MinSizeMB) << 20
	plans, skip := planShareFiles(entries, sharePlanOpts{LocalDir: src.LocalDir, MinSize: minSize, Assets: r.cfg.Assets, Owned: r.owned})
	res.Skipped = skip

	var existing []model.ShareFile
	r.db.Where("source_id = ?", src.ID).Find(&existing)
	byFid := map[string]model.ShareFile{}
	for _, f := range existing {
		byFid[f.FileID] = f
	}
	seen := map[string]bool{}
	var written, removed []string
	canceled := false
	assetsDone := 0
	for i, p := range plans {
		if r.stop() {
			canceled = true
			break
		}
		seen[p.Entry.ID] = true
		r.set("写 STRM", i, len(plans), p.Entry.Name)
		old, had := byFid[p.Entry.ID]
		if had && old.RelPath != "" && old.RelPath != p.Rel {
			// 分享者改了名 / 挪了目录：旧位置是本站写的，挪过去
			if rm := r.removeLocal(old.RelPath); rm != "" {
				removed = append(removed, rm)
				res.Moved++
			}
		}
		switch p.Kind {
		case "video":
			res.Videos++
			content := shareStrmURL(r.domain, r.format, r.keepExt, src.ShareCode, p.Entry.ID, p.Entry.Name)
			changed, err := writeFileIfChanged(r.abs(p.Rel), []byte(content))
			if err != nil {
				res.Errors = append(res.Errors, p.Rel+"："+err.Error())
				continue
			}
			if changed {
				res.Written++
				written = append(written, r.abs(p.Rel))
			} else {
				res.Unchanged++
			}
		case "asset":
			if assetsDone >= shareAssetMaxCount {
				res.AssetSkip++
				continue
			}
			ok, err := r.fetchAsset(p)
			if err != nil {
				res.Errors = append(res.Errors, p.Rel+"："+err.Error())
				if errors.Is(err, errShareGone) || errors.Is(err, errShareBadCode) {
					// 列表还在、直链拿不到：分享这会儿失效了，后面的不用试
					r.markSource(shareStateOfErr(err), err.Error(), shareStateOfErrRaw(err))
					return res, false, err
				}
				continue
			}
			assetsDone++
			if ok {
				res.Assets++
			}
		}
		e := p.Entry
		r.db.Where(model.ShareFile{SourceID: src.ID, FileID: e.ID}).
			Assign(model.ShareFile{Sha1: strings.ToUpper(e.Sha1), Size: e.Size, Name: e.Name, SharePath: e.path(), RelPath: p.Rel, Kind: p.Kind}).
			FirstOrCreate(&model.ShareFile{})
	}
	if !canceled {
		for _, f := range existing {
			if !seen[f.FileID] {
				res.Missing++
			}
		}
	}

	if len(written) > 0 {
		notifyEmbyPathsWith(uniqueDirs(written), embyRefreshAdded, false)
	}
	if len(removed) > 0 {
		notifyEmbyDeleted(removed...)
	}
	r.finishSource(res.Title)
	return res, canceled, nil
}

// fetchAsset 下载一个附属文件。本地已有同样大小的不再下；返回 true = 这次下了
func (r *shareStrmRun) fetchAsset(p shareFilePlan) (bool, error) {
	dst := r.abs(p.Rel)
	if st, err := os.Stat(dst); err == nil && !st.IsDir() && (p.Entry.Size <= 0 || st.Size() == p.Entry.Size) {
		return false, nil
	}
	if p.Entry.Size > shareAssetMaxBytes {
		return false, fmt.Errorf("附属文件太大（%d MB），没下", p.Entry.Size>>20)
	}
	info, err := r.download(r.cookie, r.src.ShareCode, r.src.ReceiveCode, p.Entry.ID)
	if err != nil {
		return false, err
	}
	data, err := r.fetch(info.URL, shareAssetMaxBytes)
	if err != nil {
		return false, err
	}
	if _, err := writeFileIfChanged(dst, data); err != nil {
		return false, err
	}
	return true, nil
}

// removeLocal 删掉分享根下本站写的一个文件，顺手收空目录（到分享根为止）。返回删掉的绝对路径
func (r *shareStrmRun) removeLocal(rel string) string {
	return removeShareLocal(r.root, rel)
}

// removeShareLocal 删分享根下的一个文件并往上收空目录。路径出了分享根一律不碰
func removeShareLocal(root, rel string) string {
	clean, ok := shareRelUnder(rel)
	if !ok {
		return ""
	}
	abs := filepath.Join(root, filepath.FromSlash(clean))
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return ""
	}
	pruneShareDirs(root, filepath.Dir(abs))
	return abs
}

// pruneShareDirs 从 dir 往上删空目录，到分享根为止（根本身留着）。os.Remove 只删得掉空目录
func pruneShareDirs(root, dir string) {
	root = filepath.Clean(root)
	for d := filepath.Clean(dir); d != root && strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			return
		}
	}
}

func (r *shareStrmRun) markSource(state, msg, shareState string) {
	now := r.now()
	upd := map[string]any{"state": state, "last_error": truncateStr(msg, 480), "last_check": &now}
	if shareState != "" {
		upd["share_state"] = shareState
	}
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Updates(upd)
	plogShareStrm.Printf("✗ %s（%s）：%s", firstNonEmpty(r.src.Title, r.src.ShareCode), r.src.ShareCode, msg)
}

func (r *shareStrmRun) finishSource(title string) {
	var videos, assets int64
	r.db.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ?", r.src.ID, "video").Count(&videos)
	r.db.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ?", r.src.ID, "asset").Count(&assets)
	now := r.now()
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Updates(map[string]any{
		"title": truncateStr(title, 480), "state": shareStateOK, "share_state": "1", "last_error": "",
		"last_check": &now, "files": int(videos), "assets": int(assets)})
}

// shareStateOfErrRaw 失效时 115 给的 share_state 原值（记下来给界面看）
func shareStateOfErrRaw(err error) string {
	var g *shareGoneErr
	if errors.As(err, &g) {
		return g.state
	}
	return ""
}

// writeFileIfChanged 内容一样不重写（Emby 按修改时间排序，白写一遍会把整部片顶到「最近添加」）
func writeFileIfChanged(abs string, data []byte) (bool, error) {
	if old, err := os.ReadFile(abs); err == nil && string(old) == string(data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(abs, data, 0o666); err != nil {
		return false, err
	}
	return true, nil
}

// uniqueDirs 一批文件的上级目录（去重）：Emby 按目录刷就够了
func uniqueDirs(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		d := filepath.Dir(f)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// httpFetchLimited 下附属文件（CDN 直链不绑 UA、不要 Cookie）
func httpFetchLimited(u string, max int64) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("文件超过 %d MB", max>>20)
	}
	return b, nil
}

func execShareStrmJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).ShareStrm
	if p == nil || p.SourceID == 0 {
		return jobOutcome{}, fmt.Errorf("任务参数缺失")
	}
	defer xferLane.set("", progressKeep, progressKeep, "")
	var src model.ShareSource
	if h.DB.First(&src, p.SourceID).Error != nil {
		return jobOutcome{}, fmt.Errorf("这个分享已经删除")
	}
	cfg := loadShareStrmCfg()
	if msg := shareRootProblem(cfg.Root, loadLibraries()); msg != "" {
		return jobOutcome{}, fmt.Errorf("%s", msg)
	}
	domain := h.strmExplicitDomain()
	if domain == "" {
		return jobOutcome{}, fmt.Errorf("还没填直连域名（Strm 管理 → 配置）：没有它写出来的 STRM 播不了")
	}
	_, format, keepExt := readStrmLinkConfig(h.DB, h.Config)
	cookie, err := h.cookieOf(src.Account)
	if err != nil {
		return jobOutcome{}, err
	}
	r := &shareStrmRun{
		db: h.DB, src: &src, cfg: cfg, root: shareRootAbs(cfg), domain: domain, format: format, keepExt: keepExt, cookie: cookie,
		walk:     shareWalk,
		download: shareDownurlFn,
		fetch:    httpFetchLimited,
		owned: func(sha1 string) bool {
			var n int64
			h.DB.Model(&model.SyncedFile{}).Where("UPPER(sha1) = ? AND orphan_at IS NULL", strings.ToUpper(sha1)).Count(&n)
			return n > 0
		},
		stop: xferLane.stopRequested,
		set:  func(ph string, d, t int, l string) { xferLane.set(ph, d, t, l) },
		now:  time.Now,
	}
	if !cfg.SkipOwned {
		r.owned = nil
	}
	plogShareStrm.Printf("▶ %s（%s）", firstNonEmpty(src.Title, src.ShareCode), src.ShareCode)
	res, canceled, err := r.run()
	if err != nil {
		return jobOutcome{Result: res}, err
	}
	msg := res.summary()
	plogShareStrm.Printf("✓ %s", msg)
	h.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("title", truncateStr("分享 STRM："+res.Title, 250))
	out := jobOutcome{Message: msg, Result: res, Canceled: canceled}
	out.Partial = !canceled && len(res.Errors) > 0
	return out, nil
}

// ---- 删除 ----

// deleteShareSource 删一个分享：本地 STRM 与附属文件、两张表的行，通知 Emby。不对 115 发任何请求
func deleteShareSource(db *gorm.DB, root string, src model.ShareSource) (int, error) {
	var files []model.ShareFile
	db.Where("source_id = ?", src.ID).Find(&files)
	var removed []string
	for _, f := range files {
		if abs := removeShareLocal(root, f.RelPath); abs != "" && f.Kind == "video" {
			removed = append(removed, abs)
		}
	}
	// 本地目录里还剩的空目录（没有文件时也收掉分享目录本身）
	if rel, ok := shareRelUnder(src.LocalDir); ok {
		pruneEmptyTree(filepath.Join(root, filepath.FromSlash(rel)))
		pruneShareDirs(root, filepath.Join(root, filepath.FromSlash(rel)))
	}
	if err := db.Where("source_id = ?", src.ID).Delete(&model.ShareFile{}).Error; err != nil {
		return 0, err
	}
	if err := db.Delete(&model.ShareSource{}, src.ID).Error; err != nil {
		return 0, err
	}
	if len(removed) > 0 {
		notifyEmbyDeleted(removed...)
	}
	return len(removed), nil
}

// pruneEmptyTree 自底向上删掉只剩空目录的子树（有文件的目录留着：可能是用户自己放的）
func pruneEmptyTree(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			pruneEmptyTree(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir)
}

// shareSourceBusy 这个分享有没有排着 / 正在跑的生成任务
func shareSourceBusy(db *gorm.DB, id uint) bool {
	var n int64
	db.Model(&model.TaskJob{}).Where("kind = ? AND dedupe_key = ? AND status IN ?", jobKindShareStrm, shareJobKey(id), []string{jobQueued, jobRunning}).Count(&n)
	return n > 0
}

func shareJobKey(id uint) string { return fmt.Sprintf("sharestrm:%d", id) }

// enqueueShareStrm 排一次生成 / 立即检查
func enqueueShareStrm(db *gorm.DB, src model.ShareSource, label string) (model.TaskJob, error) {
	return enqueueJob(db, jobSpec{Kind: jobKindShareStrm, Title: "分享 STRM：" + firstNonEmpty(src.Title, src.ShareCode),
		DedupeKey: shareJobKey(src.ID), Source: firstNonEmpty(label, "web"), Priority: jobPriorityManual,
		Params: jobParams{ShareStrm: &shareStrmJobParams{SourceID: src.ID}}})
}

