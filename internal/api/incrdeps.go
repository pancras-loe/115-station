package api

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ==================== 增量同步的外部依赖 ====================
//
// executeIncrementalSync 原先直接调 115 接口、读写配置、下载附属文件，
// 整个主流程没法单测 —— 这也是这块代码长期只有一个纯函数有测试的原因。
//
// 这里把「会打 115 / 会读写配置 / 会发通知」的部分收进一个接口。
// 刻意**没有**把数据库和本地文件系统也抽进来：仓库既有的测试
// （suppress_test、orgstrm_test）本来就是拿真的内存 SQLite 和临时目录跑的，
// 这两样用真实现比用桩更能暴露问题，桩反而会把 upsert、整树删除这类
// 最容易出错的地方糊过去。
//
// realIncrDeps 的每个方法都是对既有自由函数的一行转发，抽接口这一步因此是行为等价的。

type incrDeps interface {
	// ---- 115 只读 ----
	// lifeEvents 拉一轮生活事件：从最新往回翻到游标为止，
	// 返回倒序（新→旧）的事件与推进后的游标
	lifeEvents(cur lifeCursor, max int) ([]lifeEvent, lifeCursor, error)
	ensureLifeGate()
	dirName(cid string) string                         // 目录自身名字；取不到返回 ""
	absPath(cid string) string                         // 网盘绝对路径；取不到返回 ""
	relPath(cid, rootCid string) (string, bool, error) // 相对媒体库根的路径
	// walkDir 遍历一个目录。ctl 控制深度与中断（见 files115.go 的 walkCtl）：
	// 浅遍历只列本层，中断回调让正在排队的整理能把锁抢走
	walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter, ctl *walkCtl) error
	invalidateDirCache()
	// dirMoved 目录在网盘上改了名/换了位置：更新路径缓存，返回它【原来】的绝对路径。
	// 旧路径只可能来自缓存（事件里的字段全是新位置），查不到就返回 false，不猜
	dirMoved(fileID, newAbs string) (oldAbs string, ok bool)
	// dirGone 目录在网盘上没了：清掉那棵子树的缓存，返回清之前缓存里记的位置（没记过返回 ""）。
	// 删除事件的父目录 id 靠不住，这个位置是认出「删的是哪个目录」的主要依据
	dirGone(fileID string) (lastAbs string)
	// recycledParent 被删目录原来的父目录 id，从回收站列表里认（见 recyclebin115.go）。
	// 缓存里没记过这个目录、事件的父目录又用不上时才调；认不准返回 ""
	recycledParent(dirID, name string, at int64) string

	// ---- 配置 ----
	setting(key string) string
	strmConfig() (domain, format string, keepExt, skipExist bool)
	saveSetting(key, val string)

	// ---- 落盘（需要打 115 取直链的那部分）----
	downloadAsset(f remoteFile, localPath string) error
	applyResults(videos, assets []remoteFile, localPath, domain, format string,
		keepExt, skipExist bool, dirLabel string) applyStats

	// ---- 通知 ----
	notifyRefresh(base string)
	// notifyDeleted 本地 strm 被删/移走了：通知 Emby 清掉对应条目。
	// 跟 notifyRefresh 分开是因为两者的目标路径处理不一样（删除要上移到存活父目录）。
	// pan=true：网盘上真删了（删除事件），Emby 推回来的删除照常通知用户；false 是改名 / 移动的旧位置
	notifyDeleted(pan bool, paths ...string)
	// autoScrape 本轮新写的 STRM（台账相对路径）交给同步后自动刮削（开关在 scrape.auto_after_sync）。
	// refresh 是本来要刷的 Emby 目录；返回 true 表示刷新交给了刮削任务，增量不再自己刷
	autoScrape(strmRels []string, refresh string) (handedOff bool)
}

// errAssetExists 附属文件本地已存在，无需下载。
// 调用方据此记「跳过」而不是「下载」或「失败」
var errAssetExists = errors.New("附属文件已存在")

// errAssetLocalGone 这份附属文件是本站从本地传上去的（有上传指纹），本地那份后来没了。
// 上传在 115 上产生的 upload_file 事件绕回来时本地常常已经被删（2026-10-22 现场：
// 刮削完回传 NFO，用户随即在 Emby 里删了这部片，增量把 NFO 又下回来，
// 留下一个只有 NFO 的空片目目录）。本地是源头，删了就不该从网盘再拉回来
var errAssetLocalGone = errors.New("本地已删除的回传文件")

// incrRetryDelay 拉取/遍历失败后的重试间隔。
// 做成变量只为让测试不必真等 30 秒，生产行为不变
var incrRetryDelay = 30 * time.Second

// realIncrDeps 生产实现：每个方法都是对既有自由函数的一行转发
type realIncrDeps struct {
	h      *Handler
	cookie string
	ops    *pan115Ops
	// acct 哪个账号（0 = 主账号）。别的账号（incracct.go）：路径不走 PathCache、游标与通道状态按账号分开存
	acct    uint
	absMemo map[string]string // 别的账号一轮里查过的目录路径

	// 回收站列表，一轮只读一次（recycledParent）
	rbLoaded  bool
	rbEntries []rbEntry
}

// newIncrDeps 组装生产依赖。cookie 与 ops 取不到时直接失败，
// 与改造前 executeIncrementalSync 开头两次取值的行为一致
func (h *Handler) newIncrDeps() (*realIncrDeps, error) {
	cookie, err := h.get115Cookie()
	if err != nil {
		return nil, err
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		return nil, err
	}
	return &realIncrDeps{h: h, cookie: cookie, ops: ops}, nil
}

func (d *realIncrDeps) newLifeFetcher() *lifeFetcher {
	return &lifeFetcher{
		cookie: d.cookie,
		load:   d.setting,
		save:   d.saveSetting,
	}
}

func (d *realIncrDeps) lifeEvents(cur lifeCursor, max int) ([]lifeEvent, lifeCursor, error) {
	return d.newLifeFetcher().fetch(cur, max)
}

func (d *realIncrDeps) ensureLifeGate() {
	if d.acct != acctMain {
		acctLifeGate(d.acct, d.cookie) // 别的账号各开一次「115 生活」，不动主号的门禁状态
		return
	}
	d.newLifeFetcher().ensureLifeGate()
}

func (d *realIncrDeps) dirName(cid string) string {
	info, err := get115DirInfo(d.cookie, cid)
	if err != nil {
		return ""
	}
	return info.n
}

func (d *realIncrDeps) absPath(cid string) string {
	if d.acct != acctMain {
		return d.acctAbs(cid)
	}
	return absPathOf(d.cookie, cid)
}

func (d *realIncrDeps) relPath(cid, rootCid string) (string, bool, error) {
	return get115RelPath(d.cookie, cid, rootCid)
}

func (d *realIncrDeps) walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter, ctl *walkCtl) error {
	if d.acct != acctMain {
		// 别的账号：遍历不写路径缓存（walkCtl.panAbs 会记目录），列出来的文件记上账号
		nv, na := len(*videos), len(*assets)
		if ctl != nil {
			saved := ctl.panAbs
			ctl.panAbs = "" // 深度上限与让路照旧，只是不记路径缓存
			defer func() { ctl.panAbs = saved }()
		}
		err := walk115DirCtl(d.ops, cid, basePath, videos, assets, f, nil, ctl)
		for i := nv; i < len(*videos); i++ {
			(*videos)[i].Acct = d.acct
		}
		for i := na; i < len(*assets); i++ {
			(*assets)[i].Acct = d.acct
		}
		return err
	}
	return walk115DirCtl(d.ops, cid, basePath, videos, assets, f, nil, ctl)
}

func (d *realIncrDeps) invalidateDirCache() {
	if d.acct != acctMain {
		d.absMemo = map[string]string{}
		return
	}
	forgetAllDirPaths()
}

func (d *realIncrDeps) dirMoved(fileID, newAbs string) (string, bool) {
	if d.acct != acctMain {
		return "", false // 别的账号没有路径缓存：拿不到旧位置，调用方回退重遍历（不猜）
	}
	old, ok := lookupCachedAbs(fileID)
	if !ok || old == newAbs {
		return "", false
	}
	repathSubtree(old, newAbs)
	return old, true
}

func (d *realIncrDeps) dirGone(fileID string) string {
	if d.acct != acctMain {
		return "" // 同上：只能靠回收站与名字认
	}
	abs, _ := lookupDirAbsAnyAge(fileID)
	forgetDirSubtree(fileID)
	if abs == "" {
		abs = selfGoneAbs(fileID, d.absPath)
	}
	return abs
}

// selfGoneAbs 本站自己删的目录：缓存在删的时候已经清掉了，从 selfdirs.go 的备忘里认。
// 只有父目录 + 名字时按父目录解析一次（多半是转存文件夹，解析结果会进缓存）
func selfGoneAbs(fileID string, absPath func(cid string) string) string {
	known, parent, name, ok := selfDirWhere(fileID)
	if !ok {
		return ""
	}
	if known != "" {
		return known
	}
	if parent == "" || name == "" || absPath == nil {
		return ""
	}
	p := absPath(parent)
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(p, "/") + "/" + name
}

func (d *realIncrDeps) setting(key string) string {
	return d.h.getSettingValue(acctRuntimeKey(d.acct, key))
}

func (d *realIncrDeps) strmConfig() (string, string, bool, bool) { return d.h.getStrmConfig() }

// saveSetting 写运行态水位。Config 为空只可能出现在测试里，静默跳过
func (d *realIncrDeps) saveSetting(key, val string) {
	if d.h.Config == nil {
		return
	}
	d.h.Config.SaveSetting(acctRuntimeKey(d.acct, key), val)
}

// downloadAsset 取直链 → 下载 → 落盘；本地已存在时返回 errAssetExists
func (d *realIncrDeps) downloadAsset(f remoteFile, localPath string) error {
	dst := filepath.Join(localPath, filepath.FromSlash(path.Join(f.Path, f.Name)))
	if _, err := os.Stat(dst); err == nil {
		return errAssetExists
	}
	if uploadedFromHere(d.h.DB, dst) {
		return errAssetLocalGone
	}
	u, hdrs, err := d.ops.downloadURLFull(f.PickCode, "")
	if err != nil {
		return err
	}
	data, err := downloadAssetBytes(u, hdrs, d.ops.cookieForDL())
	if err != nil {
		return err
	}
	_, err = writeAssetBytes(f, localPath, data)
	return err
}

func (d *realIncrDeps) applyResults(videos, assets []remoteFile, localPath, domain, format string,
	keepExt, skipExist bool, dirLabel string) applyStats {
	if d.acct != acctMain {
		// 事件直推的文件（零遍历）也要记上账号：台账 Account 与直链 /d/{账号}~{pickcode} 都靠它
		for i := range videos {
			videos[i].Acct = d.acct
		}
		for i := range assets {
			assets[i].Acct = d.acct
		}
	}
	return applySyncResults(d.h.DB, d.ops, videos, assets, localPath, domain, format, keepExt, skipExist, dirLabel)
}

func (d *realIncrDeps) notifyRefresh(base string) { d.h.notifyEmbyRefresh(base) }

func (d *realIncrDeps) notifyDeleted(pan bool, paths ...string) {
	if pan {
		notifyEmbyPanDeleted(paths...)
		return
	}
	notifyEmbyDeleted(paths...)
}

func (d *realIncrDeps) autoScrape(strmRels []string, refresh string) bool {
	return enqueueSyncScrape(d.h.DB, strmRels, refresh)
}

// ==================== 增量同步的媒体库集合（多个 115 媒体库，libroots.go） ====================

// incrLib 一个媒体库：cid、库名（STRM 路径第一层）、网盘绝对路径（不带结尾斜杠）
type incrLib struct {
	cid, name, abs string
}

// incrLibSet 本轮要管的全部媒体库，主库在前。库之间互不包含（保存时 checkWorkspaceSlots 拦）
type incrLibSet []incrLib

// resolveIncrLibs 主库 + 配置里的附加库，逐个解析名字与路径。
// 任何一个解析不出都报错：它里面的事件会被当成库外吞掉、标成已消费
func resolveIncrLibs(d incrDeps, primary string) (incrLibSet, error) {
	if primary = strings.TrimSpace(primary); primary == "" || primary == "0" {
		// 主库是 "0" / 空：沿用老口径报主库无效
		return nil, fmt.Errorf("媒体库 cid 无效（%s）", primary)
	}
	// 主号的媒体库（libstore.go 的媒体库表）。别的账号的库没有事件增量，靠全量（acctlibs.go）。
	// 表还空（迁移前、测试）时按老配置：这次的主库 + full.extra_libs，本地名看 lib_opts、没有再问 115
	var cids []string
	opts := libOpts{Libs: map[string]libOpt{}}
	acct := incrAcctOf(d)
	if libs := loadLibraries(); len(libs) > 0 {
		for _, l := range libs {
			if l.Account == acct {
				cids = append(cids, l.Cid)
				opts.Libs[l.Cid] = libOpt{Local: l.Local}
			}
		}
		if len(cids) == 0 {
			if acct != acctMain {
				return nil, fmt.Errorf("账号 %d 没有媒体库", acct)
			}
			return nil, fmt.Errorf("没有主账号的媒体库")
		}
	} else {
		cids = libCidsOf(primary, parseFullCfg(d.setting("full")).ExtraLibs)
		opts = parseLibOpts(d.setting(libOptsKey))
	}
	out := make(incrLibSet, 0, len(cids))
	seen := map[string]string{}
	for _, cid := range cids {
		abs := strings.TrimSuffix(d.absPath(cid), "/")
		if abs == "" {
			return nil, fmt.Errorf("媒体库 cid=%s 解析不出绝对路径（未配置或已失效）", cid)
		}
		// 本地第一层：定过本地名用它（liblocal.go），不用再问 115
		name := strings.TrimSpace(opts.of(cid).Local)
		if name == "" {
			name = d.dirName(cid)
		}
		l := incrLib{cid: cid, name: name, abs: abs}
		if len(cids) > 1 {
			if l.name == "" {
				return nil, fmt.Errorf("媒体库 %s 读不出目录名", abs)
			}
			if prev, ok := seen[libLocalKey(l.name)]; ok {
				return nil, fmt.Errorf("媒体库 %s 与 %s 的本地目录名相同，会落进同一个本地目录（到「账号与媒体库 → 媒体库」给其中一个改本地目录名）", prev, abs)
			}
			seen[libLocalKey(l.name)] = abs
		}
		out = append(out, l)
	}
	return out, nil
}

// at 网盘绝对路径落在哪个库里，返回库与库内相对路径
func (ls incrLibSet) at(panAbs string) (incrLib, string, bool) {
	panAbs = strings.TrimSuffix(panAbs, "/")
	for _, l := range ls {
		if l.abs == "" {
			continue
		}
		if panAbs == l.abs {
			return l, "", true
		}
		if strings.HasPrefix(panAbs, l.abs+"/") {
			return l, strings.TrimPrefix(panAbs, l.abs+"/"), true
		}
	}
	return incrLib{}, "", false
}

// localRelOf 网盘绝对路径 → 本地相对路径（含库名前缀）；不在任何库内返回 false
func (ls incrLibSet) localRelOf(panAbs string) (string, bool) {
	l, rest, ok := ls.at(panAbs)
	if !ok {
		return "", false
	}
	return path.Join(l.name, rest), true
}

// isRoot 本地相对路径是不是某个库的根（库根本身不跟删，见删除事件那段）
func (ls incrLibSet) isRoot(rel string) bool {
	for _, l := range ls {
		if rel == l.name {
			return true
		}
	}
	return false
}

// dirRel 目录相对它所在媒体库的路径。逐个库问 d.relPath：
// 临时失败 / 目录已删（errDirGone）原样返回，调用方按原来的分类处理；都不在库内返回 ok=false
func (ls incrLibSet) dirRel(d incrDeps, cid string) (incrLib, string, bool, error) {
	for _, l := range ls {
		base, ok, err := d.relPath(cid, l.cid)
		if err != nil {
			return incrLib{}, "", false, err
		}
		if ok {
			return l, base, true, nil
		}
	}
	return incrLib{}, "", false, nil
}

// absList 日志用：全部库的网盘路径
func (ls incrLibSet) absList() string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, l.abs)
	}
	return strings.Join(parts, "、")
}
