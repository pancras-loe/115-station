package api

// 阿里云盘分享秒传进 115（扩展功能「阿里云盘秒传」，2026-10-24），设计见 docs/115-station-notes/ALI2115-PLAN.md。
//
// 阿里云盘给整文件 SHA1（content_hash），115 秒传要的正是它：不经本站转发任何文件内容，
// 只在 115 要二次认证时按 Range 读源文件里的一小段。流程照 p115strmhelper helper/ali2115（只读）：
// 分享列表里没有 hash、也取不到下载链接，所以先把要的文件存进自己阿里云盘的临时目录，从副本取。
//
// 这不是恢复阿里云盘支持（AGENTS §6.2）：不挂载、不生成 STRM、不播放；进了 115 转存文件夹之后
// 和分享转存进来的内容一样交给整理。只收用户自己贴的链接，资源站搜到的阿里云盘链接照旧丢掉（resNormalize）。
//
// 任务走自己的队列（xferLane），不拿 taskMu：只往转存文件夹里放东西（同订阅），
// 一个文件至少一次 115 写请求（≥3 秒节流），放主队列一部剧就要挡住整理几分钟。
// 包装目录在任务结束前压着不整理（subhold.go，与订阅共用）：传了一半就被守望者整理走，后半截会变成另一条记录。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"gorm.io/gorm"

	"115-station/internal/model"
)

const (
	jobKindAli2115 = "ali2115"
	// aliTempRoot 自己阿里云盘里放临时副本的目录（根目录下），每个任务一个子目录 job<任务id>
	aliTempRoot = "115-Station秒传"
	// ali2115MaxFiles 一次最多秒传多少个文件（列分享时超了就报错，让用户拆开）
	ali2115MaxFiles = 3000
	ali2115MaxDepth = 40
)

// 做成变量只为让测试不必真等
var (
	ali2115Retry     = 3 * time.Second // 出错的文件重试前等多久（第二次翻倍）
	ali2115SaveWait  = 10 * time.Minute
	ali2115SavePoll  = 3 * time.Second
	ali2115BatchSize = 100 // 阿里云盘 batch 一次最多 100 个子请求
)

func init() { jobExecutors[jobKindAli2115] = execAli2115Job }

// ---- 配置 ----

// ali2115Cfg setting `ali2115`
type ali2115Cfg struct {
	Enabled bool `json:"enabled"`
	// Source 默认用哪个阿里云盘账号（PanSource.ID），0 = 第一个
	Source uint `json:"source"`
	// MediaOnly 只传视频与字幕（默认开）：图片、nfo 这种小文件 115 上多半没有，秒传不中还白占一次写请求
	MediaOnly bool `json:"media_only"`
}

func loadAli2115Cfg() ali2115Cfg {
	cfg := ali2115Cfg{MediaOnly: true}
	if v := settingValueCompat("ali2115"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg
}

// aliSources 登记过的阿里云盘账号
func aliSources(db *gorm.DB) []model.PanSource {
	var list []model.PanSource
	db.Where("kind = ?", "ali").Order("id ASC").Find(&list)
	return list
}

// aliSourceFor 这次用哪个账号：指定的 > 设置里的默认 > 第一个
func aliSourceFor(db *gorm.DB, pick uint) (model.PanSource, error) {
	list := aliSources(db)
	if len(list) == 0 {
		return model.PanSource{}, fmt.Errorf("还没登录阿里云盘：到「扩展功能 → 阿里云盘秒传」扫码登录")
	}
	if pick == 0 {
		pick = loadAli2115Cfg().Source
	}
	for _, s := range list {
		if s.ID == pick {
			return s, nil
		}
	}
	return list[0], nil
}

// aliClientOf 用登记的账号建会话；refresh token 轮换后写回、出错记在账号上
func (h *Handler) aliClientOf(src model.PanSource) (*aliClient, error) {
	id := src.ID
	c, err := newAliClient(src.Token, func(c *aliClient) {
		h.DB.Model(&model.PanSource{}).Where("id = ?", id).Updates(map[string]any{"token": c.refresh})
	})
	if err != nil {
		h.DB.Model(&model.PanSource{}).Where("id = ?", id).Update("last_err", truncateStr(err.Error(), 250))
		return nil, err
	}
	now := time.Now()
	h.DB.Model(&model.PanSource{}).Where("id = ?", id).Updates(map[string]any{
		"user_id": c.userID, "drive_id": c.driveID, "nick": c.nick, "last_err": "", "last_ok": &now})
	return c, nil
}

// ---- 提交 ----

// aliJobParams 一次秒传的参数
type aliJobParams struct {
	URL     string `json:"url"`
	ShareID string `json:"share_id"`
	Pwd     string `json:"pwd,omitempty"`
	// Target 落点（xfertarget.go 的写法：cid 或 a{账号}:{cid}）
	Target string `json:"target"`
	// Label 提交来源（链接转存页 = web、机器人），写进来源链接台账
	Label  string `json:"label,omitempty"`
	Source uint   `json:"source,omitempty"` // 阿里云盘账号，0 = 默认
}

// submitAli2115 阿里云盘分享入秒传队列。target 为空按默认转存库（同分享转存）
func (h *Handler) submitAli2115(text, code, target, label string) (model.TaskJob, error) {
	if !loadAli2115Cfg().Enabled {
		return model.TaskJob{}, fmt.Errorf("阿里云盘分享要先在「扩展功能 → 阿里云盘秒传」里开启")
	}
	link, shareID, pwd := splitAliShare(text, code)
	if shareID == "" {
		return model.TaskJob{}, fmt.Errorf("认不出阿里云盘分享链接")
	}
	if _, err := aliSourceFor(h.DB, 0); err != nil {
		return model.TaskJob{}, err
	}
	if target == "" {
		t, err := h.defaultXferTarget()
		if err != nil {
			return model.TaskJob{}, err
		}
		target = t
	}
	if target == "" {
		return model.TaskJob{}, fmt.Errorf("未配置转存文件夹")
	}
	return enqueueJob(h.DB, jobSpec{Kind: jobKindAli2115, Title: "阿里云盘秒传：" + shareID, DedupeKey: "ali:" + shareID,
		Source: firstNonEmpty(label, "web"), Priority: jobPriorityManual,
		Params: jobParams{Ali: &aliJobParams{URL: link, ShareID: shareID, Pwd: pwd, Target: target, Label: label}}})
}

// ---- 执行 ----

// aliSide 阿里云盘这一侧要用的操作（测试换成假的）
type aliSide interface {
	listShare(shareToken, shareID, parent string) ([]aliFile, error)
	saveShareFiles(shareToken, shareID string, fileIDs []string, toParent string) (map[string]string, error)
	ensureDir(parent, name string) (string, error)
	listDir(parent string) ([]aliFile, error)
	downloadURL(fileID string) (aliDownload, error)
	deleteFile(fileID string) error
}

// side115 115 这一侧
type side115 struct {
	mkdir func(parent, name string) (string, error)
	rmdir func(cid string) error
	// rapid 按 SHA1 秒传；hit=false 是 115 上没有这份
	rapid func(dirID, name string, size int64, sha1 string, digest func(spec string) (string, error)) (bool, error)
	// have 目标账号的库里是不是已经有这份文件（台账 sha1）
	have func(sha1 string) bool
}

// aliShareItem 分享里要传的一个文件
type aliShareItem struct {
	aliFile
	Dir string // 在分享里的上级目录（相对分享根，/ 分隔，已去掉作为包装目录的那一层）
}

// ali2115Result 一次秒传的结果（落 TaskJob.Result）
type ali2115Result struct {
	Title   string   `json:"title"`
	Wrapper string   `json:"wrapper,omitempty"`
	Total   int      `json:"total"`
	OK      int      `json:"ok"`
	Have    int      `json:"have"`    // 库里已经有同一份（sha1），跳过
	Miss    int      `json:"miss"`    // 115 上没有这份文件，秒传不了
	Failed  int      `json:"failed"`  // 出错
	Skipped int      `json:"skipped"` // 不是视频 / 字幕，按设置没传
	Missed  []string `json:"missed,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

func (r *ali2115Result) summary() string {
	parts := []string{fmt.Sprintf("「%s」%d 个文件", r.Title, r.Total)}
	if r.OK > 0 {
		parts = append(parts, fmt.Sprintf("秒传成功 %d", r.OK))
	}
	if r.Have > 0 {
		parts = append(parts, fmt.Sprintf("库里已有 %d", r.Have))
	}
	if r.Miss > 0 {
		parts = append(parts, fmt.Sprintf("115 上没有 %d（秒传不了）", r.Miss))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("出错 %d", r.Failed))
	}
	if r.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("非视频字幕没传 %d", r.Skipped))
	}
	return strings.Join(parts, "，")
}

// ali2115Run 一次秒传
type ali2115Run struct {
	jobID     uint
	p         aliJobParams
	ali       aliSide
	x         side115
	staging   string // 转存文件夹 cid
	mediaOnly bool
	// record 登记来源链接（返回 id，0 = 没登记上）
	record func(title, wrapper string) uint
	stop   func() bool
	set    func(phase string, done, total int, label string)
	// shareToken 取分享凭证（测试换成假的）
	shareToken func(shareID, pwd string) (string, error)
	shareName  func(shareID string) string
	// onTitle 认出分享名后改任务标题（任务中心里看得出是哪一部；nil = 不改）
	onTitle func(title string)
}

// collect 递归列分享，返回要传的文件、跳过的个数、包装目录名
func (r *ali2115Run) collect(tok string) (items []aliShareItem, skipped int, title string, err error) {
	top, err := r.ali.listShare(tok, r.p.ShareID, "root")
	if err != nil {
		return nil, 0, "", err
	}
	if len(top) == 0 {
		return nil, 0, "", fmt.Errorf("阿里云盘分享是空的")
	}
	// 分享根只有一个目录：拿它当包装目录（照分享原样重建），里面的结构从它下面算
	strip := ""
	if len(top) == 1 && top[0].isDir() {
		strip = top[0].Name
		title = top[0].Name
	} else {
		title = r.shareName(r.p.ShareID)
		if title == "" {
			title = strings.TrimSuffix(top[0].Name, path.Ext(top[0].Name))
		}
	}
	var walk func(parent, dir string, list []aliFile, depth int) error
	walk = func(parent, dir string, list []aliFile, depth int) error {
		if depth > ali2115MaxDepth {
			return fmt.Errorf("阿里云盘分享的目录层级太深（超过 %d 层）", ali2115MaxDepth)
		}
		if list == nil {
			var err error
			if list, err = r.ali.listShare(tok, r.p.ShareID, parent); err != nil {
				return err
			}
		}
		for _, f := range list {
			if f.isDir() {
				sub := f.Name
				if dir != "" {
					sub = dir + "/" + f.Name
				}
				if err := walk(f.FileID, sub, nil, depth+1); err != nil {
					return err
				}
				continue
			}
			ext := strings.ToLower(pathExt(f.Name))
			if r.mediaOnly && !videoExts[ext] && !subtitleExts[ext] {
				skipped++
				continue
			}
			items = append(items, aliShareItem{aliFile: f, Dir: dir})
			if len(items) > ali2115MaxFiles {
				return fmt.Errorf("阿里云盘分享里的文件超过 %d 个，请分开转存", ali2115MaxFiles)
			}
		}
		return nil
	}
	if strip != "" {
		err = walk(top[0].FileID, "", nil, 1)
	} else {
		err = walk("root", "", top, 0)
	}
	return items, skipped, sanitizeName(title), err
}

// stage 把要传的文件存进阿里云盘临时目录，返回 分享文件 id → 副本（带 content_hash）
func (r *ali2115Run) stage(tok, jobDir string, items []aliShareItem) (map[string]aliFile, error) {
	copies := map[string]aliFile{}
	pending := map[string]aliShareItem{} // 没直接回副本 id 的（异步复制），按名字 + 体积去临时目录里认
	for i := 0; i < len(items); i += ali2115BatchSize {
		if r.stop() {
			return copies, nil
		}
		batch := items[i:min(i+ali2115BatchSize, len(items))]
		ids := make([]string, 0, len(batch))
		for _, it := range batch {
			ids = append(ids, it.FileID)
		}
		r.set("存进阿里云盘临时目录", i, len(items), "")
		got, err := r.ali.saveShareFiles(tok, r.p.ShareID, ids, jobDir)
		if err != nil {
			return nil, err
		}
		for _, it := range batch {
			if nid := got[it.FileID]; nid != "" {
				copies[it.FileID] = aliFile{FileID: nid, Name: it.Name, Size: it.Size}
			} else {
				pending[it.FileID] = it
			}
		}
	}
	// 副本的 content_hash 列目录才有：一次列出临时目录，补上 hash，顺便认异步复制出来的
	deadline := time.Now().Add(ali2115SaveWait)
	for {
		list, err := r.ali.listDir(jobDir)
		if err != nil {
			return nil, err
		}
		byID := map[string]aliFile{}
		for _, f := range list {
			byID[f.FileID] = f
		}
		for sid, c := range copies {
			if f, ok := byID[c.FileID]; ok {
				copies[sid] = f
			}
		}
		taken := map[string]bool{}
		for _, c := range copies {
			taken[c.FileID] = true
		}
		for sid, it := range pending {
			for _, f := range list {
				// auto_rename 撞名时会改名（「x(1).mkv」），体积对上、名字以原名的主干开头就认
				stem := strings.TrimSuffix(it.Name, path.Ext(it.Name))
				if !taken[f.FileID] && f.Size == it.Size && strings.HasPrefix(f.Name, stem) {
					copies[sid] = f
					taken[f.FileID] = true
					delete(pending, sid)
					break
				}
			}
		}
		allHashed := true
		for _, c := range copies {
			if c.ContentHash == "" {
				allHashed = false
				break
			}
		}
		if len(pending) == 0 && allHashed {
			return copies, nil
		}
		if !time.Now().Before(deadline) || r.stop() {
			// 等不到的按出错记，不卡死整个任务
			return copies, nil
		}
		r.set("等阿里云盘复制完成", len(copies)-len(pending), len(items), "")
		time.Sleep(ali2115SavePoll)
	}
}

// cleanStale 删掉上次没收拾的临时目录（同一时刻只有一个秒传任务在跑，别的 job* 都是残留）
func (r *ali2115Run) cleanStale(root, keep string) {
	list, err := r.ali.listDir(root)
	if err != nil {
		return
	}
	for _, f := range list {
		if f.isDir() && strings.HasPrefix(f.Name, "job") && f.Name != keep {
			if err := r.ali.deleteFile(f.FileID); err != nil {
				plogAli2115.Printf("○ 上次的临时目录 %s 没删掉: %v", f.Name, err)
			}
		}
	}
}

// run 跑一次。返回结果与是否被停止
func (r *ali2115Run) run() (res ali2115Result, canceled bool, err error) {
	r.set("读阿里云盘分享", 0, 0, r.p.ShareID)
	tok, err := r.shareToken(r.p.ShareID, r.p.Pwd)
	if err != nil {
		return res, false, err
	}
	items, skipped, title, err := r.collect(tok)
	res.Title, res.Skipped = title, skipped
	if err != nil {
		return res, false, err
	}
	res.Total = len(items) + skipped
	if r.onTitle != nil && title != "" {
		r.onTitle(title)
	}
	if len(items) == 0 {
		return res, false, fmt.Errorf("「%s」里没有视频或字幕（%d 个别的文件按设置没传）", title, skipped)
	}

	// 临时目录：根目录下 115-Station秒传/job<id>，结束一定删
	root, err := r.ali.ensureDir("root", aliTempRoot)
	if err != nil {
		return res, false, fmt.Errorf("阿里云盘建临时目录失败: %w", err)
	}
	jobName := fmt.Sprintf("job%d", r.jobID)
	r.cleanStale(root, jobName)
	jobDir, err := r.ali.ensureDir(root, jobName)
	if err != nil {
		return res, false, fmt.Errorf("阿里云盘建临时目录失败: %w", err)
	}
	defer func() {
		if derr := r.ali.deleteFile(jobDir); derr != nil {
			plogAli2115.Printf("✗ 阿里云盘临时目录 %s/%s 没删掉，请手动删除: %v", aliTempRoot, jobName, derr)
		}
	}()
	copies, err := r.stage(tok, jobDir, items)
	if err != nil {
		return res, false, err
	}
	if r.stop() {
		return res, true, nil
	}

	// 115：转存文件夹下建包装目录，任务结束前压着不整理
	res.Wrapper = fmt.Sprintf("%s ·秒传%d", title, r.jobID)
	wrap, err := r.x.mkdir(r.staging, res.Wrapper)
	if err != nil {
		return res, false, fmt.Errorf("115 建目录失败: %w", err)
	}
	holdSubWrapper(wrap)
	if r.record != nil {
		r.record(title, res.Wrapper)
	}
	dirs := map[string]string{"": wrap}
	var dirOf func(rel string) (string, error)
	dirOf = func(rel string) (string, error) {
		if cid, ok := dirs[rel]; ok {
			return cid, nil
		}
		parent, name := "", rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			parent, name = rel[:i], rel[i+1:]
		}
		pcid, err := dirOf(parent)
		if err != nil {
			return "", err
		}
		cid, err := r.x.mkdir(pcid, sanitizeName(name))
		if err != nil {
			return "", err
		}
		dirs[rel] = cid
		return cid, nil
	}

	for i, it := range items {
		if r.stop() {
			canceled = true
			break
		}
		r.set("秒传进 115", i, len(items), truncateStr(it.Name, 40))
		c, ok := copies[it.FileID]
		switch {
		case !ok:
			res.Failed++
			res.Errors = append(res.Errors, it.Name+"：阿里云盘没复制出来")
			continue
		case c.ContentHash == "":
			res.Failed++
			res.Errors = append(res.Errors, it.Name+"：阿里云盘没给 SHA1")
			continue
		case r.x.have != nil && r.x.have(c.ContentHash):
			res.Have++
			continue
		}
		dir, err := dirOf(it.Dir)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, it.Name+"：115 建目录失败 "+err.Error())
			continue
		}
		hit, err := r.upload(c, it.Name, dir)
		switch {
		case err != nil:
			res.Failed++
			res.Errors = append(res.Errors, it.Name+"："+err.Error())
			plogAli2115.Printf("✗ %s: %v", it.Name, err)
		case hit:
			res.OK++
		default:
			res.Miss++
			res.Missed = append(res.Missed, path.Join(it.Dir, it.Name))
		}
	}

	releaseSubWrappers(wrap)
	if res.OK == 0 {
		// 一个都没进来：收掉空包装目录，留着守望者会反复整理它直到熔断（同订阅的 dropWrapper）
		if r.x.rmdir != nil {
			if err := r.x.rmdir(wrap); err != nil {
				plogAli2115.Printf("✗ 空目录「%s」没删掉，请到转存文件夹手动删除: %v", res.Wrapper, err)
			}
		}
		res.Wrapper = ""
	}
	return res, canceled, nil
}

// upload 秒传一个文件：出错重试两次（秒传不中不重试，115 上没有就是没有）。
// 下载链接只在 115 要二次认证时才取：多数文件用不上，省一次阿里云盘请求
func (r *ali2115Run) upload(c aliFile, name, dir string) (bool, error) {
	link := ""
	digest := func(spec string) (string, error) {
		if link == "" {
			d, err := r.ali.downloadURL(c.FileID)
			if err != nil {
				return "", err
			}
			link = d.URL
		}
		return aliRangeSHA1(link, spec)
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(ali2115Retry * time.Duration(attempt))
		}
		var hit bool
		if hit, err = r.x.rapid(dir, sanitizeName(name), c.Size, c.ContentHash, digest); err == nil {
			return hit, nil
		}
	}
	return false, err
}

// aliLive 真阿里云盘：saveShareFiles 包一层 saveShare，取回每个副本的 id
type aliLive struct{ *aliClient }

func (a aliLive) saveShareFiles(tok, shareID string, ids []string, toParent string) (map[string]string, error) {
	return a.saveShareIDs(tok, shareID, ids, toParent)
}

func execAli2115Job(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	p := decodeJobParams(job).Ali
	if p == nil || p.ShareID == "" {
		return jobOutcome{}, fmt.Errorf("任务参数缺失")
	}
	defer xferLane.set("", progressKeep, progressKeep, "")
	src, err := aliSourceFor(h.DB, p.Source)
	if err != nil {
		return jobOutcome{}, err
	}
	ac, err := h.aliClientOf(src)
	if err != nil {
		return jobOutcome{}, err
	}
	acct, staging := splitXferTarget(p.Target)
	cookie, err := h.cookieOf(acct)
	if err != nil {
		return jobOutcome{}, err
	}
	ops, err := h.opsFor(acct)
	if err != nil {
		return jobOutcome{}, err
	}
	r := &ali2115Run{
		jobID: job.ID, p: *p, ali: aliLive{ac}, staging: staging, mediaOnly: loadAli2115Cfg().MediaOnly,
		x: side115{
			mkdir: ops.mkdir,
			rmdir: func(cid string) error { return ops.deleteFiles([]string{cid}) },
			rapid: func(dirID, name string, size int64, sha1 string, digest func(string) (string, error)) (bool, error) {
				return h.rapidUpload115ByHash(cookie, dirID, name, size, sha1, digest)
			},
			// 查重只看目标账号的台账（ledgerAcctSQL）：A 号库里有，不代表 B 号的库里有
			have: func(sha1 string) bool {
				var n int64
				h.DB.Model(&model.SyncedFile{}).Where("UPPER(sha1) = ? AND orphan_at IS NULL AND "+ledgerAcctSQL, strings.ToUpper(sha1), acct).Count(&n)
				return n > 0
			},
		},
		record: func(title, wrapper string) uint {
			return dlLinkRecord(h, p.URL, "ali", title, firstNonEmpty(p.Label, "web"), []string{wrapper})
		},
		stop:       xferLane.stopRequested,
		set:        func(ph string, d, t int, l string) { xferLane.set(ph, d, t, l) },
		shareToken: aliShareToken,
		shareName:  aliShareName,
		onTitle: func(title string) {
			h.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("title", truncateStr("阿里云盘秒传："+title, 250))
		},
	}
	plogAli2115.Printf("▶ %s（%s）", p.ShareID, acctLabelOf(h, acct))
	res, canceled, err := r.run()
	if err != nil {
		plogAli2115.Printf("✗ %s: %v", p.ShareID, err)
		return jobOutcome{Result: res}, err
	}
	msg := res.summary()
	plogAli2115.Printf("✓ %s", msg)
	if res.OK > 0 {
		go h.triggerOrganizeAndSync()
		msg += "，整理入库已排上"
	}
	out := jobOutcome{Message: msg, Result: res, Canceled: canceled}
	if !canceled && res.OK+res.Have == 0 {
		return out, fmt.Errorf("%s", msg)
	}
	out.Partial = !canceled && (res.Miss > 0 || res.Failed > 0)
	return out, nil
}

// acctLabelOf 日志里写哪个 115 账号
func acctLabelOf(h *Handler, acct uint) string {
	if acct == acctMain {
		return "主号"
	}
	if s, err := h.extraAccount(acct); err == nil {
		return acctLabel(s)
	}
	return "账号 " + strconv.FormatUint(uint64(acct), 10)
}

// ---- 接口 ----

// aliSourceView 界面上的一个阿里云盘账号
type aliSourceView struct {
	ID      uint       `json:"id"`
	Name    string     `json:"name"`
	Nick    string     `json:"nick"`
	LastErr string     `json:"last_err,omitempty"`
	LastOK  *time.Time `json:"last_ok,omitempty"`
}

// Ali2115Config GET /ali2115/config
func (h *Handler) Ali2115Config(c *gin.Context) {
	list := aliSources(h.DB)
	views := make([]aliSourceView, 0, len(list))
	for _, s := range list {
		views = append(views, aliSourceView{ID: s.ID, Name: s.Name, Nick: s.Nick, LastErr: s.LastErr, LastOK: s.LastOK})
	}
	c.JSON(http.StatusOK, gin.H{"config": loadAli2115Cfg(), "accounts": views})
}

// Ali2115SaveConfig POST /ali2115/config
func (h *Handler) Ali2115SaveConfig(c *gin.Context) {
	var req ali2115Cfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting("ali2115", string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// addAliSource 用 refresh token 登记（或更新同一个阿里云盘用户的）账号：先真刷一次，刷得通才存
func (h *Handler) addAliSource(refresh string) (model.PanSource, error) {
	ac, err := newAliClient(refresh, nil)
	if err != nil {
		return model.PanSource{}, err
	}
	now := time.Now()
	var src model.PanSource
	h.DB.Where("kind = ? AND user_id = ?", "ali", ac.userID).First(&src)
	src.Kind, src.UserID, src.DriveID, src.Nick, src.Token = "ali", ac.userID, ac.driveID, ac.nick, ac.refresh
	src.LastErr, src.LastOK = "", &now
	if src.Name == "" {
		src.Name = firstNonEmpty(ac.nick, "阿里云盘")
	}
	if err := h.DB.Save(&src).Error; err != nil {
		return model.PanSource{}, err
	}
	return src, nil
}

// Ali2115AddToken POST /ali2115/accounts {token} —— 贴 refresh token 登录
func (h *Handler) Ali2115AddToken(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 refresh token"})
		return
	}
	src, err := h.addAliSource(req.Token)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已登录：" + src.Name, "id": src.ID})
}

// Ali2115QRCode POST /ali2115/qrcode —— 生成登录二维码
func (h *Handler) Ali2115QRCode(c *gin.Context) {
	q, err := aliQRGenerate()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "取二维码失败: " + err.Error()})
		return
	}
	png, err := qrcode.Encode(q.Content, qrcode.Medium, 256)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"t": q.T, "ck": q.CK, "image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

// Ali2115QRCheck POST /ali2115/qrcode/check {t, ck}
func (h *Handler) Ali2115QRCheck(c *gin.Context) {
	var req struct {
		T  string `json:"t"`
		CK string `json:"ck"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CK == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	status, refresh, err := aliQRQuery(req.T, req.CK)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if status != "CONFIRMED" {
		c.JSON(http.StatusOK, gin.H{"status": status})
		return
	}
	src, err := h.addAliSource(refresh)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "message": "已登录：" + src.Name, "id": src.ID})
}

// Ali2115DeleteAccount DELETE /ali2115/accounts/:id
func (h *Handler) Ali2115DeleteAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	if h.DB.Where("id = ? AND kind = ?", id, "ali").Delete(&model.PanSource{}).RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有这个账号"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Ali2115TestAccount POST /ali2115/accounts/:id/test —— 刷一次 token 看还能不能用
func (h *Handler) Ali2115TestAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	var src model.PanSource
	if h.DB.Where("id = ? AND kind = ?", id, "ali").First(&src).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有这个账号"})
		return
	}
	ac, err := h.aliClientOf(src)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "可以用：" + firstNonEmpty(ac.nick, src.Name)})
}

// botAli2115 机器人里贴的阿里云盘分享（企微与 TG 都走 wecomHandleLink）：入秒传队列，跑完回一句结果
func (h *Handler) botAli2115(text string, reply func(lines ...string)) {
	job, err := h.submitAli2115(text, "", "", "机器人")
	if err != nil {
		reply("✗ " + err.Error())
		return
	}
	reply(fmt.Sprintf("⏳ 阿里云盘分享已排进秒传队列（任务 #%d）", job.ID), "秒传完会回复结果，进 115 的自动整理入库。")
	onJobDone(job.ID, func(j model.TaskJob) {
		switch j.Status {
		case jobSuccess:
			reply("✓ " + j.Message)
		case jobPartial:
			reply("◐ "+j.Message, "没秒传成的文件在任务中心看。")
		case jobCanceled:
			reply("○ 秒传已停止：" + j.Message)
		default:
			reply("✗ 阿里云盘秒传失败：" + j.Message)
		}
	})
}
