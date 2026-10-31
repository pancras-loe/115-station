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
	Missing   int           `json:"missing"` // 分享里没了、等下一轮确认的
	Deleted   int           `json:"deleted"` // 连续两轮都没有，本地删掉的
	Blocked   int           `json:"blocked"` // 在 Emby 里删过、不再生成的
	Taken     int           `json:"taken"`   // 同一个位置别的分享已经占着（本地整理时两个分享是同一集）
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
		parts = append(parts, fmt.Sprintf("分享里已没有 %d（下次检查还没有才删）", r.Missing))
	}
	if r.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("分享里没了、删掉本地 %d", r.Deleted))
	}
	if r.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("在 Emby 里删过的 %d 不再生成", r.Blocked))
	}
	if r.Taken > 0 {
		parts = append(parts, fmt.Sprintf("别的分享已占着同一位置 %d", r.Taken))
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
	// auto 定时追更排的（不是用户点的）：有新写的 STRM 推一条通知
	auto bool
	// org 本地整理要用的（识别、分类）；nil = 只能按原样放（没配 TMDB 时）
	org *shareOrgDeps
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
	if normShareLayout(src.Layout) == shareLayoutOrganize && r.org != nil {
		org, err := r.organizePlans(entries, title, plans)
		if err != nil {
			// TMDB 暂时连不上：什么都不动（按原样放会把已经整理好的文件搬回去），按出错退避
			r.markSource(shareStateError, err.Error(), "")
			return res, false, err
		}
		plans = org
	}

	var existing []model.ShareFile
	r.db.Where("source_id = ?", src.ID).Find(&existing)
	byFid := map[string]model.ShareFile{}
	for _, f := range existing {
		byFid[f.FileID] = f
	}
	// 分享里还在的（不管这次建不建）：被过滤掉的（太小、库里已有）不算「分享里没了」
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.ID] = true
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
		if had && old.Blocked {
			// 用户在 Emby 里删过它：不再生成回来
			res.Blocked++
			continue
		}
		var taken int64
		r.db.Model(&model.ShareFile{}).Where("rel_path = ? AND source_id <> ?", p.Rel, src.ID).Count(&taken)
		if taken > 0 {
			// 本地整理时两个分享是同一部片的同一集：先来的占着，后来的不覆盖（覆盖了先来的那个分享就对不上账）
			res.Taken++
			continue
		}
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
		// gone_at 用 map 写：Assign 一个结构体时零值字段不更新，又出现的文件清不掉消失标记
		r.db.Where(model.ShareFile{SourceID: src.ID, FileID: e.ID}).
			Assign(map[string]any{"sha1": strings.ToUpper(e.Sha1), "size": e.Size, "name": e.Name,
				"share_path": e.path(), "rel_path": p.Rel, "kind": p.Kind, "gone_at": nil}).
			FirstOrCreate(&model.ShareFile{})
	}
	// 分享里没了的：这一轮列全了（列不全在上面就报错返回了）才算数，
	// 头一次只记下时间，隔 shareGoneConfirm 以上再一轮还没有才删本地（SHARE-STRM-PLAN.md §7.2）
	if !canceled {
		now := r.now()
		for _, f := range existing {
			if listed[f.FileID] {
				continue
			}
			switch {
			case f.Blocked:
				r.db.Delete(&model.ShareFile{}, f.ID) // 分享里也没了，屏蔽不用再记
			case f.GoneAt == nil:
				r.db.Model(&model.ShareFile{}).Where("id = ?", f.ID).Update("gone_at", &now)
				res.Missing++
			case now.Sub(*f.GoneAt) < shareGoneConfirm:
				res.Missing++
			default:
				if rm := r.removeLocal(f.RelPath); rm != "" && f.Kind == "video" {
					removed = append(removed, rm)
				}
				r.db.Delete(&model.ShareFile{}, f.ID)
				res.Deleted++
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
	if r.auto && res.Written > 0 {
		NotifyMessage(kindTask, "📺 分享 STRM：「"+res.Title+"」有更新",
			fmt.Sprintf("追更新写了 %d 个 STRM。%s", res.Written, res.summary()))
	}
	return res, canceled, nil
}

// shareGoneConfirm 分享里没了的文件要隔多久再确认一次才删：连点两次「立即检查」不该把文件删掉
const shareGoneConfirm = 30 * time.Minute

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
	st := shareAfterRun(*r.src, state, now)
	upd := map[string]any{"state": state, "last_error": truncateStr(msg, 480), "last_check": &now,
		"fail_streak": st.FailStreak, "gone_streak": st.GoneStreak, "next_check": st.NextCheck, "notified": st.Notified}
	if shareState != "" {
		upd["share_state"] = shareState
	}
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Updates(upd)
	name := firstNonEmpty(r.src.Title, r.src.ShareCode)
	plogShareStrm.Printf("✗ %s（%s）：%s", name, r.src.ShareCode, msg)
	switch st.Notify {
	case shareStateGone:
		NotifyMessage(kindTask, "⚠️ 分享 STRM：「"+name+"」分享已失效",
			fmt.Sprintf("连续 %d 次检查都是「%s」，本地 %d 个 STRM 已经播不了。到「分享 STRM → 待清理」删掉，或者先留着。", st.GoneStreak, msg, r.src.Files))
	case shareStateBadCode:
		NotifyMessage(kindTask, "⚠️ 分享 STRM：「"+name+"」提取码不对", "分享者可能换了提取码，到「分享 STRM」页改提取码后会重新检查。")
	}
}

func (r *shareStrmRun) finishSource(title string) {
	var videos, assets int64
	r.db.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ? AND blocked = ?", r.src.ID, "video", false).Count(&videos)
	r.db.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ?", r.src.ID, "asset").Count(&assets)
	now := r.now()
	st := shareAfterRun(*r.src, shareStateOK, now)
	r.db.Model(&model.ShareSource{}).Where("id = ?", r.src.ID).Updates(map[string]any{
		"title": truncateStr(title, 480), "state": shareStateOK, "share_state": "1", "last_error": "",
		"last_check": &now, "files": int(videos), "assets": int(assets),
		"fail_streak": 0, "gone_streak": 0, "notified": false, "ignored": false, "next_check": st.NextCheck})
}

// ---- 追更与失效的状态机（纯函数）----

const (
	// shareGoneStreakMax 连续几次查到分享失效才认定：115 偶尔对正常分享也回怪状态
	shareGoneStreakMax = 3
	// shareRetryMax 出错退避的上限
	shareRetryMax = 72 * time.Hour
)

type shareRunState struct {
	FailStreak int
	GoneStreak int
	NextCheck  *time.Time
	Notified   bool
	Notify     string // 这一轮要推的通知：gone / badcode，空 = 不推
}

// shareAfterRun 一轮跑完（或出错）之后：连续次数、下一次什么时候查、要不要通知。
//   - 正常：清零，开了追更就隔 FollowHours 再查；
//   - 分享失效：连续计数，满 3 次认定、通知一次、不再自动查（等用户在待清理里处理）；不满 3 次隔「追更间隔，没开就 24 小时」再确认；
//   - 提取码错：通知一次、不再自动查（要用户改提取码）；
//   - 别的错（网络、风控）：退避 间隔 × 2^次数，封顶 72 小时，没开追更的不自动重试
func shareAfterRun(src model.ShareSource, outcome string, now time.Time) shareRunState {
	base := time.Duration(src.FollowHours) * time.Hour
	at := func(d time.Duration) *time.Time {
		t := now.Add(d)
		return &t
	}
	st := shareRunState{}
	switch outcome {
	case shareStateOK:
		if base > 0 && !src.Paused {
			st.NextCheck = at(base)
		}
	case shareStateGone:
		st.GoneStreak = src.GoneStreak + 1
		st.Notified = src.Notified
		if st.GoneStreak >= shareGoneStreakMax {
			if !src.Notified {
				st.Notify, st.Notified = shareStateGone, true
			}
			return st // 不再自动查
		}
		if !src.Paused {
			st.NextCheck = at(max(base, 24*time.Hour))
		}
	case shareStateBadCode:
		st.Notified = true
		if !src.Notified {
			st.Notify = shareStateBadCode
		}
	default:
		st.FailStreak = src.FailStreak + 1
		st.GoneStreak = src.GoneStreak
		if base > 0 && !src.Paused {
			d := base
			for i := 1; i < st.FailStreak && d < shareRetryMax; i++ {
				d *= 2
			}
			st.NextCheck = at(min(d, shareRetryMax))
		}
	}
	return st
}

// shareStrmTick 调度器每分钟：开着总开关时把到期的分享排进队列（一轮最多 shareTickMax 个，后台优先级）
func (h *Handler) shareStrmTick(now time.Time) {
	if h.DB == nil || !loadShareStrmCfg().Enabled {
		return
	}
	var due []model.ShareSource
	h.DB.Where("next_check IS NOT NULL AND next_check <= ? AND paused = ?", now, false).
		Order("next_check ASC").Limit(shareTickMax).Find(&due)
	for _, s := range due {
		if shareSourceBusy(h.DB, s.ID) {
			continue
		}
		// 先把下一次往后挪：任务失败到不了 markSource（比如 Cookie 没了）时，不会每分钟排一次
		later := now.Add(time.Hour)
		h.DB.Model(&model.ShareSource{}).Where("id = ?", s.ID).Update("next_check", &later)
		if _, err := enqueueShareStrmAs(h.DB, s, "cron", jobPriorityBackground); err != nil {
			plogShareStrm.Printf("✗ 追更排队失败 %s：%v", s.ShareCode, err)
		}
	}
}

const shareTickMax = 20

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
		auto: job.Source == "cron",
	}
	if !cfg.SkipOwned {
		r.owned = nil
	}
	if normShareLayout(src.Layout) == shareLayoutOrganize {
		if tc, err := loadTmdbClient(); err == nil && tc != nil {
			r.org = &shareOrgDeps{recognize: tc.recognize, byID: tc.getByTmdbID, classify: shareCategoryOf(cfg.SchemeID), rules: loadReplaceRules()}
		} else {
			// 没配 TMDB：认不了片，按原样放并写清楚原因
			h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Update("recog_note", "没配 TMDB，认不了片，按原样放")
		}
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
	// 定时追更多半什么都没变：跑完删行，不刷满任务历史（同定时整理的空转）
	out.Idle = r.auto && !canceled && res.Written+res.Moved+res.Deleted+res.Assets+len(res.Errors) == 0
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

// enqueueShareStrm 排一次生成 / 立即检查（手动优先级）
func enqueueShareStrm(db *gorm.DB, src model.ShareSource, label string) (model.TaskJob, error) {
	return enqueueShareStrmAs(db, src, label, jobPriorityManual)
}

func enqueueShareStrmAs(db *gorm.DB, src model.ShareSource, label string, prio int) (model.TaskJob, error) {
	return enqueueJob(db, jobSpec{Kind: jobKindShareStrm, Title: "分享 STRM：" + firstNonEmpty(src.Title, src.ShareCode),
		DedupeKey: shareJobKey(src.ID), Source: firstNonEmpty(label, "web"), Priority: prio,
		Params: jobParams{ShareStrm: &shareStrmJobParams{SourceID: src.ID}}})
}
