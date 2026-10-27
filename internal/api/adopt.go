package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"115-station/internal/model"
)

// ==================== 接管已有 STRM 目录 ====================
//
// 用户从别的 STRM 项目换过来时，Emby 的条目挂在本地 STRM 的路径上：路径和文件名一个都不变、
// 只把内容换成本站直链并登记台账，观看记录与元数据才都保得住（ADOPT-STRM-PLAN.md）。
//
// 预演与执行是同一个任务（kind adopt）、同一条流水：扫本地 STRM → 取一次网盘清单 → 逐个对号分桶。
// 预演到此为止，**不写本地、不写台账**（取清单时顺手记的路径缓存除外，那是全量本来就做的）；
// 执行（Apply）在同一份新算的结果上接着做，不拿上一次预演的结果：两次之间本地和网盘都可能变了。
// 执行只碰「原样接管」「名字不同」两桶：改写内容为本站直链（还原修改时间）、登记台账，
// 本地已有的附属文件一并登记，再写上两个 STRM 迁移的完成标记（见 applyAdopt）。
// 入口 2026-10-25 起在扩展功能「STRM 迁移」（原来在 Strm 管理 → 全量）。
//
// Emby 查询媒体源与播放时都现读 STRM 内容（2026-10-17 实测，ADOPT-STRM-PLAN.md §5.1），
// 所以接管只改内容就行，不用通知 Emby。

func init() {
	jobExecutors["adopt"] = execAdoptJob
}

// adoptJobParams 预演 / 执行参数
type adoptJobParams struct {
	// ScanDir 去哪里找旧 STRM（容器内绝对路径），空 = 本地媒体库目录。
	// 旧目录还没挂到本地根下面时，用它看清楚旧文件的层级、算出该怎么挂
	ScanDir string `json:"scan_dir,omitempty"`
	// Apply 执行接管（否则只预演）
	Apply bool `json:"apply,omitempty"`
	// ThenFull 接管完排一次全量，补写「网盘上有、本地没有」的
	ThenFull bool `json:"then_full,omitempty"`
}

// localStrm 本地一个 .strm
type localStrm struct {
	Rel string     // 相对本地媒体库目录（斜杠）；不在它下面时是绝对路径
	Abs string     // 绝对路径（斜杠）
	Ref foreignRef // 内容指向哪个网盘文件
}

// 报告里的桶，顺序即界面上的顺序
const (
	adoptBucketAdopt    = "adopt"    // 原样接管：位置与文件名都是本站会写的
	adoptBucketRename   = "rename"   // 名字不同：目录对，文件名是本站两种写法以外的
	adoptBucketAlready  = "already"  // 台账里已经是这个文件（本站自己写的，或已接管过）
	adoptBucketMoved    = "moved"    // 位置不同：目录对不上，要先改挂载 / 本地根
	adoptBucketDup      = "dup"      // 重复：和另一个本地 STRM 指向同一个网盘文件
	adoptBucketMissing  = "missing"  // 网盘上没有（或不在配置的媒体库里）
	adoptBucketShare    = "share"    // 分享 STRM：不是本账号的文件
	adoptBucketUnknown  = "unknown"  // 认不出指向哪里
	adoptBucketNotLocal = "notlocal" // 网盘上有、本地没有 STRM（之后由普通全量补写）
)

var adoptBucketOrder = []string{
	adoptBucketAdopt, adoptBucketRename, adoptBucketAlready, adoptBucketMoved, adoptBucketDup,
	adoptBucketMissing, adoptBucketShare, adoptBucketUnknown, adoptBucketNotLocal,
}

// adoptSampleLimit 每个桶最多带几条样例（报告整份存进 TaskJob.Result）
const adoptSampleLimit = 50

type adoptSample struct {
	Local  string `json:"local,omitempty"`  // 本地 STRM（相对本地根）
	Expect string `json:"expect,omitempty"` // 本站会写成的位置（相对本地根）
	Remote string `json:"remote,omitempty"` // 网盘文件（库内相对路径）
	Note   string `json:"note,omitempty"`
}

type adoptBucket struct {
	Key     string        `json:"key"`
	Count   int           `json:"count"`
	Samples []adoptSample `json:"samples"`
}

// adoptShift 「位置不同」里一种前缀换算：本地 From 下面的东西，本站会写在 To 下面
type adoptShift struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

type adoptLibInfo struct {
	Cid    string `json:"cid"`
	Name   string `json:"name"`
	Videos int    `json:"videos"`
	Mode   string `json:"mode,omitempty"`
	Error  string `json:"error,omitempty"`
}

type adoptReport struct {
	LocalRoot    string         `json:"local_root"`
	ScanDir      string         `json:"scan_dir"`
	LocalStrm    int            `json:"local_strm"`
	RemoteVideos int            `json:"remote_videos"`
	Libs         []adoptLibInfo `json:"libs"`
	// Formats 各项目写法各几个（看着像谁写的）
	Formats map[string]int `json:"formats"`
	Buckets []*adoptBucket `json:"buckets"`
	// Shifts 「位置不同」按前缀换算归类，最多 5 种
	Shifts []adoptShift `json:"shifts"`
	// OtherAccount 「网盘上没有」里 pickcode 不是这个 115 账号的个数（换了账号时按路径对号）
	OtherAccount int `json:"other_account"`
	// Verdict ok = 可以接管；move = 多数位置对不上，先按 Shifts 改挂载；empty = 一个都没对上
	Verdict string `json:"verdict"`
	// Applied 执行的结果（只在执行时有）
	Applied *adoptApplied `json:"applied,omitempty"`
}

// adoptAction 执行时要接管的一个本地 STRM
type adoptAction struct {
	Local  localStrm
	File   remoteFile
	Bucket string // adopt / rename
}

// adoptApplied 执行结果
type adoptApplied struct {
	Adopted   int  `json:"adopted"`   // 登记进台账的视频
	Renamed   int  `json:"renamed"`   // 其中沿用非本站写法文件名的
	Rewritten int  `json:"rewritten"` // 内容改写成本站直链的
	Unchanged int  `json:"unchanged"` // 内容已经是本站直链的
	Failed    int  `json:"failed"`    // 读写失败、没接管的
	Assets    int  `json:"assets"`    // 登记的本地已有附属文件
	FullJobID uint `json:"full_job_id,omitempty"`
}

func (r *adoptReport) bucket(key string) *adoptBucket {
	for _, b := range r.Buckets {
		if b.Key == key {
			return b
		}
	}
	b := &adoptBucket{Key: key, Samples: []adoptSample{}}
	r.Buckets = append(r.Buckets, b)
	return b
}

func (r *adoptReport) add(key string, s adoptSample) {
	b := r.bucket(key)
	b.Count++
	if len(b.Samples) < adoptSampleLimit {
		b.Samples = append(b.Samples, s)
	}
}

func (r *adoptReport) count(key string) int {
	for _, b := range r.Buckets {
		if b.Key == key {
			return b.Count
		}
	}
	return 0
}

// adoptRank 同一个网盘文件对上好几个本地 STRM 时留哪个：越小越优先
var adoptRank = map[string]int{
	adoptBucketAlready: 0, adoptBucketAdopt: 1, adoptBucketRename: 2, adoptBucketMoved: 3,
}

// planAdopt 对号分桶（纯函数，不读写任何东西）。
// remote 只放视频；ledger 是台账里视频的 fid → 相对路径；localRoot 用来算「位置不同」的绝对前缀。
// acts 是执行时要接管的（「原样接管」「名字不同」两桶里留下的那份）
func planAdopt(locals []localStrm, remote []remoteFile, ledger map[string]string, localRoot string) (rep adoptReport, acts []adoptAction) {
	rep = adoptReport{LocalStrm: len(locals), RemoteVideos: len(remote), Formats: map[string]int{}, Shifts: []adoptShift{}}
	for _, k := range adoptBucketOrder {
		rep.bucket(k)
	}
	localRoot = strings.TrimRight(filepath.ToSlash(localRoot), "/")

	byPC := make(map[string]int, len(remote))
	byFid := make(map[string]int, len(remote))
	byPath := make(map[string]int, len(remote))
	for i, f := range remote {
		if f.PickCode != "" {
			byPC[strings.ToLower(f.PickCode)] = i
		}
		byFid[f.Fid] = i
		byPath[path.Join(f.Path, f.Name)] = i
	}
	// 账号不动点：pickcode 对不上时用来判断是不是另一个 115 账号的文件
	acct := ""
	for _, f := range remote {
		if sp, err := stablePoint115(f.PickCode); err == nil {
			acct = sp
			break
		}
	}

	sorted := append([]localStrm(nil), locals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rel < sorted[j].Rel })

	type cand struct {
		l      localStrm
		bucket string
	}
	groups := map[int][]cand{}
	var order []int
	for _, l := range sorted {
		rep.Formats[l.Ref.Format]++
		switch l.Ref.Kind {
		case "share":
			rep.add(adoptBucketShare, adoptSample{Local: l.Rel})
			continue
		case "unknown":
			rep.add(adoptBucketUnknown, adoptSample{Local: l.Rel})
			continue
		}
		idx, ok := adoptMatch(l.Ref, byPC, byFid, byPath)
		if !ok {
			s := adoptSample{Local: l.Rel}
			if l.Ref.PickCode != "" && acct != "" {
				if sp, err := stablePoint115(l.Ref.PickCode); err == nil && sp != acct {
					rep.OtherAccount++
					s.Note = "不是这个 115 账号的文件"
				}
			}
			rep.add(adoptBucketMissing, s)
			continue
		}
		f := remote[idx]
		b := adoptBucketMoved
		base := path.Base(l.Rel)
		switch {
		case ledger[f.Fid] == l.Rel:
			b = adoptBucketAlready
		case !path.IsAbs(l.Rel) && path.Dir(l.Rel) == f.Path:
			if base == strmNameOf(f.Name) || base == legacyStrmNameOf(f.Name) {
				b = adoptBucketAdopt
			} else {
				b = adoptBucketRename
			}
		}
		if _, seen := groups[idx]; !seen {
			order = append(order, idx)
		}
		groups[idx] = append(groups[idx], cand{l, b})
	}

	shifts := map[[2]string]int{}
	for _, idx := range order {
		f := remote[idx]
		cs := groups[idx]
		// 同一档里本站现在的写法（stem.strm）优先于旧写法（stem.mkv.strm）：留下的那份以后不用再改名
		score := func(c cand) int {
			s := adoptRank[c.bucket] * 2
			if path.Base(c.l.Rel) != strmNameOf(f.Name) {
				s++
			}
			return s
		}
		sort.SliceStable(cs, func(i, j int) bool { return score(cs[i]) < score(cs[j]) })
		win := cs[0]
		expect := path.Join(f.Path, strmNameOf(f.Name))
		remoteRel := path.Join(f.Path, f.Name)
		s := adoptSample{Local: win.l.Rel, Expect: expect, Remote: remoteRel}
		if win.bucket == adoptBucketMoved {
			from, to := adoptPathShift(path.Dir(win.l.Abs), path.Join(localRoot, f.Path))
			shifts[[2]string{from, to}]++
		}
		rep.add(win.bucket, s)
		if win.bucket == adoptBucketAdopt || win.bucket == adoptBucketRename {
			acts = append(acts, adoptAction{Local: win.l, File: f, Bucket: win.bucket})
		}
		for _, c := range cs[1:] {
			rep.add(adoptBucketDup, adoptSample{Local: c.l.Rel, Remote: remoteRel, Note: "与 " + win.l.Rel + " 指向同一个网盘文件"})
		}
	}
	for i, f := range remote {
		if _, ok := groups[i]; !ok {
			rep.add(adoptBucketNotLocal, adoptSample{Expect: path.Join(f.Path, strmNameOf(f.Name)), Remote: path.Join(f.Path, f.Name)})
		}
	}

	for k, n := range shifts {
		rep.Shifts = append(rep.Shifts, adoptShift{From: k[0], To: k[1], Count: n})
	}
	sort.Slice(rep.Shifts, func(i, j int) bool {
		if rep.Shifts[i].Count != rep.Shifts[j].Count {
			return rep.Shifts[i].Count > rep.Shifts[j].Count
		}
		return rep.Shifts[i].From < rep.Shifts[j].From
	})
	if len(rep.Shifts) > 5 {
		rep.Shifts = rep.Shifts[:5]
	}

	kept := rep.count(adoptBucketAdopt) + rep.count(adoptBucketRename) + rep.count(adoptBucketAlready)
	moved := rep.count(adoptBucketMoved)
	switch {
	case kept == 0 && moved == 0:
		rep.Verdict = "empty"
	case moved > kept:
		rep.Verdict = "move"
	default:
		rep.Verdict = "ok"
	}
	return rep, acts
}

// adoptMatch pickcode → 文件 id → 网盘路径，依次对号。
// 路径按后缀对：内容里的路径是网盘完整路径（带前缀、可能从更上层开始），
// 清单里是「库目录名/库内路径」，取最长的一段能对上的后缀
func adoptMatch(ref foreignRef, byPC, byFid, byPath map[string]int) (int, bool) {
	if ref.PickCode != "" {
		if i, ok := byPC[ref.PickCode]; ok {
			return i, true
		}
	}
	if ref.FileID != "" {
		if i, ok := byFid[ref.FileID]; ok {
			return i, true
		}
	}
	if ref.PanPath != "" {
		segs := strings.Split(strings.Trim(ref.PanPath, "/"), "/")
		for i := 0; i < len(segs); i++ {
			if idx, ok := byPath[strings.Join(segs[i:], "/")]; ok {
				return idx, true
			}
		}
	}
	return 0, false
}

// adoptPathShift 两个目录去掉末尾相同的几段，剩下的就是「本地这里 → 本站那里」的前缀换算
func adoptPathShift(localDir, expectDir string) (from, to string) {
	a := strings.Split(strings.Trim(localDir, "/"), "/")
	b := strings.Split(strings.Trim(expectDir, "/"), "/")
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	return "/" + strings.Join(a, "/"), "/" + strings.Join(b, "/")
}

// adoptStrmReadLimit 读 STRM 内容的上限：正常的直链不过几百字节，读多了只会拖慢万级目录的扫描
const adoptStrmReadLimit = 8 << 10

// walkStrmFiles 遍历 dir 下所有 .strm（跳过隐藏目录与群晖的 @eaDir），读不了的子目录往 lg 记一行跳过。
// 接管与替换地址（strmrebase.go）共用
func walkStrmFiles(lg pluginLogger, dir string, fn func(p string, st os.FileInfo)) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return err
			}
			lg.Printf("○ 读不了 %s，跳过: %v", p, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(name, ".") || name == "@eaDir") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".strm") {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			lg.Printf("○ 读不了 %s，跳过: %v", p, err)
			return nil
		}
		fn(p, st)
		return nil
	})
}

// scanLocalStrms 遍历 scanDir 下所有 .strm，Rel 相对 localRoot（不在它下面时是绝对路径）
func scanLocalStrms(scanDir, localRoot string, progress func(int)) ([]localStrm, error) {
	root := filepath.Clean(localRoot)
	var out []localStrm
	err := walkStrmFiles(plogAdopt, scanDir, func(p string, _ os.FileInfo) {
		content, err := readHead(p, adoptStrmReadLimit)
		if err != nil {
			plogAdopt.Printf("○ 读不了 %s，跳过: %v", p, err)
			return
		}
		abs := filepath.ToSlash(p)
		rel := abs
		if r := localRelOrEmpty(root, p); r != "" && r != "." {
			rel = r
		}
		out = append(out, localStrm{Rel: rel, Abs: abs, Ref: parseForeignStrm(content)})
		if progress != nil && len(out)%500 == 0 {
			progress(len(out))
		}
	})
	return out, err
}

func readHead(p string, limit int64) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	return string(b), err
}

// adoptLib 要取清单的一个库
type adoptLib struct {
	acct uint
	cid  string
	name string // 本地目录（库在本地根下的相对路径），也是清单里 Path 的第一段
}

// adoptLibs 库清单：媒体库表（libstore.go）；表还空（迁移前）时退回老配置的主号各库
func (h *Handler) adoptLibs(cookie string) []adoptLib {
	var out []adoptLib
	if libs := loadLibraries(); len(libs) > 0 {
		for _, l := range libs {
			out = append(out, adoptLib{acct: l.Account, cid: l.Cid, name: normLibLocal(l.Local)})
		}
		return out
	}
	for _, cid := range h.loadFullSyncCfg().libCids() {
		out = append(out, adoptLib{cid: cid, name: libDirName(cookie, cid)})
	}
	return out
}

// adoptRun 预演与执行共用的前半段：扫本地、取清单、对号
type adoptRun struct {
	rep    adoptReport
	acts   []adoptAction
	assets []remoteFile // 只在执行时取
	failed int          // 没取到清单的库
}

func (h *Handler) adoptCollect(params adoptJobParams) (*adoptRun, error) {
	cfg := h.loadFullSyncCfg()
	cookie, _ := h.get115Cookie()
	libs := h.adoptLibs(cookie)
	if len(libs) == 0 {
		return nil, errors.New("还没配置 115 媒体库目录，先到「账号与媒体库 → 媒体库」配好")
	}
	localRoot := mediaRoot() // mediaroot.go
	scanDir := strings.TrimSpace(params.ScanDir)
	if scanDir == "" {
		scanDir = localRoot
	}
	if st, err := os.Stat(scanDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("读不了目录 %s（容器里没挂上？）", scanDir)
	}

	setJobProgress("扫描本地 STRM", progressKeep, progressKeep, "")
	locals, err := scanLocalStrms(scanDir, localRoot, func(n int) {
		setJobProgress("扫描本地 STRM", n, progressKeep, "")
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 %s 失败: %w", scanDir, err)
	}
	// 一个都没有时不下任何结论：多半是挂载没就绪，或者旧 STRM 不在这个目录下（同 strmmigrate.go 的守卫）
	if len(locals) == 0 {
		return nil, fmt.Errorf("%s 下一个 .strm 都没找到：容器里没挂上，或者旧 STRM 不在这个目录下", scanDir)
	}

	vexts := buildExtSet(cfg.VideoExt)
	if len(vexts) == 0 {
		vexts = videoExts
	}
	filter := &syncFilter{videoExts: vexts, assetExts: map[string]bool{}}
	if params.Apply {
		// 执行时顺带取附属文件：本地已有的一并登记台账（全量同步的「已存在 → 登记」同口径），不重下
		filter.assetExts = buildExtSet(append(append([]string{}, cfg.ImageExt...), cfg.DataExt...))
		filter.assetExts[".nfo"] = true
	}

	run := &adoptRun{}
	var remote []remoteFile
	var infos []adoptLibInfo
	for i, l := range libs {
		info := adoptLibInfo{Cid: l.cid, Name: l.name}
		if info.Name == "" {
			info.Error = "读不出媒体库目录名（可能已删除，或暂时连不上 115）"
			infos = append(infos, info)
			run.failed++
			continue
		}
		ops, err := h.opsFor(l.acct)
		if err != nil {
			info.Error = err.Error()
			infos = append(infos, info)
			run.failed++
			continue
		}
		libCookie := cookie
		if l.acct != acctMain {
			libCookie = ops.cookie
		}
		setJobProgress(fmt.Sprintf("取网盘清单 %d/%d：%s", i+1, len(libs), info.Name), progressKeep, progressKeep, "")
		// 工作区都在主号上，附加账号的库不用排除（同 fullSyncLib）
		var skipCids map[string]bool
		if l.acct == acctMain {
			skipCids, _ = h.orgSkipCids(l.cid)
		}
		var videos, assets []remoteFile
		assetsPtr := &assets
		if !params.Apply {
			assetsPtr = nil
		}
		// 固定先试快速模式（请求少两个数量级），不可用时 collectSyncFiles 自己降级
		mode, _, err := h.collectSyncFiles(ops, libCookie, "fast", l.cid, info.Name, &videos, assetsPtr, filter, skipCids, nil)
		info.Mode = mode
		if err != nil {
			info.Error = err.Error()
			run.failed++
		} else {
			if l.acct != acctMain {
				// 附加账号的文件：直链与台账都带账号（acctlibs.go）
				for i := range videos {
					videos[i].Acct = l.acct
				}
				for i := range assets {
					assets[i].Acct = l.acct
				}
			}
			info.Videos = len(videos)
			remote = append(remote, videos...)
			run.assets = append(run.assets, assets...)
		}
		infos = append(infos, info)
	}
	if run.failed == len(libs) {
		msgs := make([]string, 0, len(infos))
		for _, l := range infos {
			msgs = append(msgs, firstNonEmpty(l.Name, "cid="+l.Cid)+"："+l.Error)
		}
		return nil, fmt.Errorf("取不到网盘清单：%s", strings.Join(msgs, "；"))
	}

	var rows []model.SyncedFile
	if err := h.DB.Select("file_id", "rel_path").Where("kind = ?", "video").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读台账失败: %w", err)
	}
	ledger := make(map[string]string, len(rows))
	for _, r := range rows {
		ledger[r.FileID] = r.RelPath
	}

	run.rep, run.acts = planAdopt(locals, remote, ledger, localRoot)
	run.rep.LocalRoot = filepath.ToSlash(localRoot)
	run.rep.ScanDir = filepath.ToSlash(scanDir)
	run.rep.Libs = infos
	return run, nil
}

// applyAdopt 执行接管：改写内容、登记台账（视频 + 本地已有的附属文件）。
// 中途出错或被打断，已接管的那部分保留；重跑一遍，接管过的落进「已在台账」，结果一样
func applyAdopt(db *gorm.DB, root string, acts []adoptAction, assets []remoteFile, domain, format string, keepExt bool, progress func(done, total int)) adoptApplied {
	var ap adoptApplied
	rows := make([]model.SyncedFile, 0, len(acts))
	for i, a := range acts {
		if progress != nil && i%200 == 0 {
			progress(i, len(acts))
		}
		p := filepath.Join(root, filepath.FromSlash(a.Local.Rel))
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			plogAdopt.Printf("✗ 读不了 %s: %v", a.Local.Rel, err)
			ap.Failed++
			continue
		}
		old, err := os.ReadFile(p)
		if err != nil {
			plogAdopt.Printf("✗ 读不了 %s: %v", a.Local.Rel, err)
			ap.Failed++
			continue
		}
		want := strmURLFor(domain, format, keepExt, a.File)
		if string(old) == want {
			ap.Unchanged++
		} else {
			// 整个内容换成本站直链（本站写的 STRM 就这一行，全量「内容一样就不写」才认得出它）。
			// 不调 markFreshStrm：一次改写上万个，下一次入库确认会把整库排进探测（§6.16）
			if err := os.WriteFile(p, []byte(want), 0o666); err != nil {
				plogAdopt.Printf("✗ 改写 %s 失败: %v", a.Local.Rel, err)
				ap.Failed++
				continue
			}
			// 还原修改时间：Emby 按「加入时间」排序看的就是它
			_ = os.Chtimes(p, st.ModTime(), st.ModTime())
			ap.Rewritten++
		}
		row := model.SyncedFile{
			FileID: a.File.ledgerFid(), PickCode: a.File.playID(), Account: a.File.Acct,
			RelPath: a.Local.Rel, Kind: "video", Size: a.File.Size, Sha1: a.File.Sha1,
		}
		if a.Bucket == adoptBucketRename {
			row.AdoptName = a.File.Name
			ap.Renamed++
		}
		rows = append(rows, row)
	}
	upsertSyncedFiles(db, rows)
	// upsert 冲突时只更新那几列，AdoptName 另补（新插入的行已经带着）
	_ = db.Transaction(func(tx *gorm.DB) error {
		for _, r := range rows {
			if r.AdoptName != "" {
				tx.Model(&model.SyncedFile{}).Where("file_id = ? AND rel_path = ?", r.FileID, r.RelPath).Update("adopt_name", r.AdoptName)
			}
		}
		return nil
	})
	ap.Adopted = len(rows)

	var assetRows []model.SyncedFile
	for _, f := range assets {
		rel := path.Join(f.Path, f.Name)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			continue // 本地没有的留给全量下载
		}
		assetRows = append(assetRows, model.SyncedFile{FileID: f.ledgerFid(), PickCode: f.playID(), Account: f.Acct,
			RelPath: rel, Kind: "asset", Size: f.Size, Sha1: f.Sha1})
	}
	upsertSyncedFiles(db, assetRows)
	ap.Assets = len(assetRows)

	// 两个 STRM 迁移的完成标记：新装时本地一个 STRM 都没有，这两个迁移会一直等着、标记没写。
	// 接管完再启动时它们一跑，就会把接管来的 stem.mkv.strm 改名成 stem.strm（Emby 当成新文件）
	for _, k := range []string{strmMigrateKey, strmISOMigrateKey} {
		var n int64
		db.Model(&model.Setting{}).Where("key = ?", k).Count(&n)
		if n == 0 {
			if err := db.Create(&model.Setting{Key: k, Value: "1"}).Error; err != nil {
				plogAdopt.Printf("○ 写迁移标记 %s 失败: %v", k, err)
			}
		}
	}
	return ap
}

func execAdoptJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	var params adoptJobParams
	if p := decodeJobParams(job).Adopt; p != nil {
		params = *p
	}
	var domain string
	if params.Apply {
		// 没填直连域名时按兜底的 Docker 回环写，Emby 多半连不上：上万个 STRM 一下子全播不了
		if domain = h.strmExplicitDomain(); domain == "" {
			return jobOutcome{}, errors.New("还没填「STRM 直连域名」：先到「Strm 管理 → 配置」填好并保存，再执行接管")
		}
	}
	run, err := h.adoptCollect(params)
	if err != nil {
		return jobOutcome{}, err
	}
	rep := run.rep

	msg := fmt.Sprintf("本地 STRM %d 个：可原样接管 %d、名字不同 %d、已在台账 %d、位置不同 %d、重复 %d、网盘上没有 %d、分享 %d、认不出 %d；网盘上有、本地没有 %d",
		rep.LocalStrm, rep.count(adoptBucketAdopt), rep.count(adoptBucketRename), rep.count(adoptBucketAlready),
		rep.count(adoptBucketMoved), rep.count(adoptBucketDup), rep.count(adoptBucketMissing),
		rep.count(adoptBucketShare), rep.count(adoptBucketUnknown), rep.count(adoptBucketNotLocal))
	if run.failed > 0 {
		msg += fmt.Sprintf("；%d 个库没取到清单", run.failed)
	}
	if !params.Apply {
		plogAdopt.Printf("✓ 预演完成（%s）：%s", rep.ScanDir, msg)
		return jobOutcome{Message: msg, Result: rep, Partial: run.failed > 0}, nil
	}

	// 执行：结论不是「可以接管」时不动（多数位置对不上时只接管一小部分，剩下的全量会在旁边另写一份）
	if rep.Verdict != "ok" {
		return jobOutcome{}, fmt.Errorf("这次对号的结论不是「可以接管」，没有执行。先按预演报告调整再试（%s）", msg)
	}
	if len(run.acts) == 0 {
		return jobOutcome{Message: "没有要接管的：" + msg, Result: rep}, nil
	}
	_, format, keepExt, _ := h.getStrmConfig()
	ap := applyAdopt(h.DB, mediaRoot(), run.acts, run.assets, domain, format, keepExt, func(done, total int) {
		setJobProgress("接管 STRM", done, total, "")
	})
	if params.ThenFull {
		p := h.fullParamsFromConfig()
		if p.Cid == "" && len(loadLibraries()) == 0 {
			plogAdopt.Printf("○ 没配全量同步，接管后的全量没排")
		} else if fj, err := enqueueJob(h.DB, jobSpec{Kind: "full", Title: "全量同步（接管后补写）", DedupeKey: "full",
			Source: "web", Priority: jobPriorityManual, Params: jobParams{Sync: &syncJobParams{
				Cid: p.Cid, LocalPath: p.LocalPath, VideoExt: p.VideoExt, ImageExt: p.ImageExt, DataExt: p.DataExt, Mode: p.Mode,
			}}}); err != nil {
			plogAdopt.Printf("✗ 接管后的全量同步入队失败: %v", err)
		} else {
			ap.FullJobID = fj.ID
		}
	}
	rep.Applied = &ap
	out := fmt.Sprintf("已接管 %d 个 STRM（改写 %d、本来就是本站直链 %d、沿用原文件名 %d），登记附属文件 %d 个",
		ap.Adopted, ap.Rewritten, ap.Unchanged, ap.Renamed, ap.Assets)
	if ap.Failed > 0 {
		out += fmt.Sprintf("，%d 个没接管成（看日志 [接管]）", ap.Failed)
	}
	if ap.FullJobID > 0 {
		out += "；已排一次全量同步补写本地没有的"
	}
	plogAdopt.Printf("✓ %s（%s）", out, msg)
	return jobOutcome{Message: out, Result: rep, Partial: ap.Failed > 0 || run.failed > 0}, nil
}

// AdoptPreview POST /sync/adopt/preview  body: {"scan_dir": ""}
// 入队一个只读预演，结果在任务详情的 result 里（adoptReport）
func (h *Handler) AdoptPreview(c *gin.Context) {
	h.enqueueAdopt(c, false)
}

// AdoptApply POST /sync/adopt/apply  body: {"scan_dir": "", "then_full": true}
// 入队执行：重新扫一遍、对一遍号，在新结果上接管（不拿上一次预演的结果）
func (h *Handler) AdoptApply(c *gin.Context) {
	h.enqueueAdopt(c, true)
}

func (h *Handler) enqueueAdopt(c *gin.Context, apply bool) {
	var req adoptJobParams
	_ = c.ShouldBindJSON(&req)
	req.Apply = apply
	if !apply {
		req.ThenFull = false
	}
	req.ScanDir = strings.TrimSpace(req.ScanDir)
	if req.ScanDir != "" && !filepath.IsAbs(req.ScanDir) && !strings.HasPrefix(req.ScanDir, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "扫描目录要写容器里的绝对路径"})
		return
	}
	if apply && h.strmExplicitDomain() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没填「STRM 直连域名」：先到「Strm 管理 → 配置」填好并保存，再执行接管"})
		return
	}
	title, dedupe, verb := "接管已有 STRM：预演", "adopt", "接管预演"
	if apply {
		title, dedupe, verb = "接管已有 STRM", "adopt:apply", "接管"
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "adopt", Title: title, DedupeKey: dedupe,
		Source: "web", Priority: jobPriorityManual, Params: jobParams{Adopt: &req}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, verb)
}
