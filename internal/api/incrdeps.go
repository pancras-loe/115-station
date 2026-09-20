package api

import (
	"errors"
	"os"
	"path"
	"path/filepath"
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
	walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter) error
	invalidateDirCache()
	// dirMoved 目录在网盘上改了名/换了位置：更新路径缓存，返回它【原来】的绝对路径。
	// 旧路径只可能来自缓存（事件里的字段全是新位置），查不到就返回 false，不猜
	dirMoved(fileID, newAbs string) (oldAbs string, ok bool)
	// dirGone 目录在网盘上没了：清掉那棵子树的缓存
	dirGone(fileID string)

	// ---- 配置 ----
	setting(key string) string
	strmConfig() (domain, format string, keepExt, skipExist bool)
	saveSetting(key, val string)

	// ---- 落盘（需要打 115 取直链的那部分）----
	downloadAsset(f remoteFile, localPath string) error
	applyResults(videos, assets []remoteFile, localPath, domain, format string,
		keepExt, skipExist bool, dirLabel string) (strmCreated, downloaded, skipped, failed int)

	// ---- 通知 ----
	notifyRefresh(base string)
	// notifyDeleted 本地 strm 被删/移走了：通知 Emby 清掉对应条目。
	// 跟 notifyRefresh 分开是因为两者的目标路径处理不一样（删除要上移到存活父目录）
	notifyDeleted(base string)
}

// errAssetExists 附属文件本地已存在，无需下载。
// 调用方据此记「跳过」而不是「下载」或「失败」
var errAssetExists = errors.New("附属文件已存在")

// incrRetryDelay 拉取/遍历失败后的重试间隔。
// 做成变量只为让测试不必真等 30 秒，生产行为不变
var incrRetryDelay = 30 * time.Second

// realIncrDeps 生产实现：每个方法都是对既有自由函数的一行转发
type realIncrDeps struct {
	h      *Handler
	cookie string
	ops    *pan115Ops
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
		load:   func(k string) string { return d.h.getSettingValue(k) },
		save:   d.saveSetting,
	}
}

func (d *realIncrDeps) lifeEvents(cur lifeCursor, max int) ([]lifeEvent, lifeCursor, error) {
	return d.newLifeFetcher().fetch(cur, max)
}

func (d *realIncrDeps) ensureLifeGate() { d.newLifeFetcher().ensureLifeGate() }

func (d *realIncrDeps) dirName(cid string) string {
	info, err := get115DirInfo(d.cookie, cid)
	if err != nil {
		return ""
	}
	return info.n
}

func (d *realIncrDeps) absPath(cid string) string {
	return absPathOf(d.cookie, cid)
}

func (d *realIncrDeps) relPath(cid, rootCid string) (string, bool, error) {
	return get115RelPath(d.cookie, cid, rootCid)
}

func (d *realIncrDeps) walkDir(cid, basePath string, videos, assets *[]remoteFile, f *syncFilter) error {
	return walk115Dir(d.ops, cid, basePath, videos, assets, f, nil)
}

func (d *realIncrDeps) invalidateDirCache() { forgetAllDirPaths() }

func (d *realIncrDeps) dirMoved(fileID, newAbs string) (string, bool) {
	old, ok := lookupCachedAbs(fileID)
	if !ok || old == newAbs {
		return "", false
	}
	repathSubtree(old, newAbs)
	return old, true
}

func (d *realIncrDeps) dirGone(fileID string) { forgetDirSubtree(fileID) }

func (d *realIncrDeps) setting(key string) string { return d.h.getSettingValue(key) }

func (d *realIncrDeps) strmConfig() (string, string, bool, bool) { return d.h.getStrmConfig() }

// saveSetting 写运行态水位。Config 为空只可能出现在测试里，静默跳过
func (d *realIncrDeps) saveSetting(key, val string) {
	if d.h.Config == nil {
		return
	}
	d.h.Config.SaveSetting(key, val)
}

// downloadAsset 取直链 → 下载 → 落盘；本地已存在时返回 errAssetExists
func (d *realIncrDeps) downloadAsset(f remoteFile, localPath string) error {
	dst := filepath.Join(localPath, filepath.FromSlash(path.Join(f.Path, f.Name)))
	if _, err := os.Stat(dst); err == nil {
		return errAssetExists
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
	keepExt, skipExist bool, dirLabel string) (int, int, int, int) {
	return applySyncResults(d.h.DB, d.ops, videos, assets, localPath, domain, format, keepExt, skipExist, dirLabel)
}

func (d *realIncrDeps) notifyRefresh(base string) { d.h.notifyEmbyRefresh(base) }

func (d *realIncrDeps) notifyDeleted(base string) { notifyEmbyDeleted(base) }
