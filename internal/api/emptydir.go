package api

import (
	"fmt"
	"log"
)

// ==================== 网盘空目录清理 ====================
//
// 整理把文件搬走之后，源目录（待整理下的片子目录、容器目录）和重新整理前的
// 旧标题目录都会变成空壳。不收拾的话：待整理/冗余里堆满空文件夹，媒体库里
// 留着一堆空的「错名」剧集目录，Emby 扫出来就是一排没有剧集的空条目。
//
// 各参考项目一致都删（MoviePilot / p115strmhelper / openStrm / qmediasync），
// 守卫取 MoviePilot 那套最严的：
//   - 工作区根目录（媒体库/待整理/已存在/冗余/转存）永不删
//   - 只删**确认为空**的目录：删之前重新列一遍，有任何文件就放弃
//   - 自下而上：先递归清空子目录，父目录才可能真空
//   - 限深，避免异常数据把清理变成整树遍历
//
// 删除走 115 的 /rb/delete —— rb = recycle bin，**进回收站可还原**，
// 不是不可逆的抹除。这是敢把清理做成默认行为的前提。

// emptyDirMaxDepth 递归清理的最大层数（标题目录 → 季目录 → 再一层足够）
const emptyDirMaxDepth = 3

// dirIO 清理空目录用到的两个 115 动作。抽成接口不是为了扩展，是为了让守卫
// 逻辑能被单测覆盖 —— 这段判断错一次就是误删用户文件，而真链路要 115 账号。
// *pan115Ops 直接满足它
type dirIO interface {
	listEntries(cid string, offset int) ([]map[string]interface{}, int, error)
	deleteFiles(fids []string) error
	moveFiles(targetCid string, fids []string) error
}

// pruneCand 一个待检查的空目录候选：cid + 给人看的路径/名字
type pruneCand struct {
	cid   string
	label string
}

// dirPruner 一轮整理内的空目录清理器：累积候选，收尾统一处理。
// 累积而不是即时删，是因为同一个父目录会被多个条目命中，逐个删要重复列目录
type dirPruner struct {
	ops       dirIO
	protected map[string]bool // 工作区根 cid：永不删
	cands     []pruneCand     // 待检查的目录（按加入顺序，可重复）
	onLog     func(string)
}

// newDirPruner protectedCids 传所有工作区根（媒体库/待整理/已存在/冗余/转存）
func newDirPruner(ops dirIO, protectedCids []string, onLog func(string)) *dirPruner {
	p := &dirPruner{ops: ops, protected: map[string]bool{}, onLog: onLog}
	for _, cid := range protectedCids {
		if cid != "" {
			p.protected[cid] = true
		}
	}
	if p.onLog == nil {
		// 默认带模块前缀：漏了前缀的日志在实时日志页里认不出是谁打的
		p.onLog = func(s string) { log.Printf("[整理] %s", s) }
	}
	return p
}

// mark 登记一个「可能已经空了」的目录。label 是给人看的路径/名字——
// 日志里只有一串 cid 的话，出了问题根本查不出删的是哪个目录
func (p *dirPruner) mark(cid, label string) {
	if p == nil || cid == "" || cid == "0" || p.protected[cid] {
		return
	}
	if label == "" {
		label = "cid=" + cid
	}
	p.cands = append(p.cands, pruneCand{cid: cid, label: label})
}

// protectedSet 保护集（pruner 为 nil 时返回 nil，调用方不必判空）
func (p *dirPruner) protectedSet() map[string]bool {
	if p == nil {
		return nil
	}
	return p.protected
}

// flush 检查并删除已登记的空目录，返回删除数量
func (p *dirPruner) flush() int {
	if p == nil || len(p.cands) == 0 {
		return 0
	}
	seen := map[string]bool{}
	removed := 0
	for _, c := range p.cands {
		if seen[c.cid] {
			continue
		}
		seen[c.cid] = true
		n, _ := pruneEmptyDirTree(p.ops, c.cid, p.protected, 0, c.label, p.onLog)
		removed += n
	}
	p.cands = nil
	// 只删了一个的时候上面那行已经报过路径了，再来句汇总纯属重复
	if removed > 1 {
		p.onLog(fmt.Sprintf("○ 本轮共清理 %d 个空文件夹（都在 115 回收站里，可还原）", removed))
	}
	return removed
}

// vlogTo 详细日志走调用方的 onLog：模块前缀由调用方认领（整理 / 深度删除各写各的），
// 清理器自己不该替它们决定日志里写哪个模块名
func vlogTo(onLog func(string), format string, args ...interface{}) {
	if onLog != nil && verboseLogging() {
		onLog(fmt.Sprintf(format, args...))
	}
}

// pruneEmptyDirTree 自下而上清理以 cid 为根的空目录子树。
// 返回删除的目录总数，以及 cid 自己是否被删掉。
//
// 「空」指的是整棵子树里没有任何文件 —— 待整理目录常见 片名/Season 01/*.mkv
// 这种嵌套，文件搬走之后父目录里还挂着一个空的 Season 01，只看直接子项会误判成
// 「非空」。只要撞到一个文件就整棵放弃：宁可留着空壳，也绝不误删内容
func pruneEmptyDirTree(ops dirIO, cid string, protected map[string]bool, depth int, label string, onLog func(string)) (removed int, gone bool) {
	if cid == "" || cid == "0" {
		return 0, false
	}
	if label == "" {
		label = "cid=" + cid
	}
	if protected[cid] {
		vlogTo(onLog, "○ 跳过工作区目录（永不清理）: %s", label)
		return 0, false
	}
	if depth > emptyDirMaxDepth {
		onLog(fmt.Sprintf("○ 空文件夹清理达到深度上限（%d 层），停在: %s", emptyDirMaxDepth, label))
		return 0, false
	}
	entries, _, err := ops.listEntries(cid, 0)
	if err != nil {
		// 列不出来就不删：可能是目录已经不存在（上一轮删过），也可能是风控，
		// 两种情况下删都没有好处
		vlogTo(onLog, "○ 读不出目录内容，不做清理: %s（%v）", label, err)
		return 0, false
	}

	if len(entries) > 0 {
		// 先把子目录递归清掉，父目录才可能变空；有任何文件直接放弃
		type subDir struct{ cid, name string }
		var subDirs []subDir
		for _, e := range entries {
			if fmt.Sprint(e["f"]) != "0" {
				vlogTo(onLog, "○ %s 里还有文件（如 %s），整棵不清理", label, fmt.Sprint(e["n"]))
				return 0, false // 有文件，整棵不动
			}
			if sub := fmt.Sprint(e["cid"]); sub != "" && sub != "<nil>" {
				subDirs = append(subDirs, subDir{cid: sub, name: fmt.Sprint(e["n"])})
			}
		}
		for _, sub := range subDirs {
			n, _ := pruneEmptyDirTree(ops, sub.cid, protected, depth+1, label+"/"+sub.name, onLog)
			removed += n
		}
		// 复查：子目录没全删掉（内部有文件/受保护）时父目录仍然非空
		entries, _, err = ops.listEntries(cid, 0)
		if err != nil || len(entries) > 0 {
			vlogTo(onLog, "○ %s 仍有 %d 个子项未清空，保留", label, len(entries))
			return removed, false
		}
	}

	if err := ops.deleteFiles([]string{cid}); err != nil {
		onLog(fmt.Sprintf("✗ 空文件夹清理失败: %s（cid=%s）: %v", label, cid, err))
		return removed, false
	}
	// 删除是破坏性动作，逐个常驻打印 —— 出了问题要能一眼看出删的是哪个目录
	onLog(fmt.Sprintf("○ 已删除空文件夹: %s（cid=%s，在 115 回收站，可还原）", label, cid))
	return removed + 1, true
}

// pruneOrMove 整理收尾处理一个源目录：整棵没有文件就删掉（进 115 回收站），
// 还有残留内容就连同内容移到 fallbackCid（冗余），等人工过目。
// 返回是否已删除
func pruneOrMove(ops dirIO, cid string, protected map[string]bool, fallbackCid, label string, onLog func(string)) bool {
	_, gone := pruneEmptyDirTree(ops, cid, protected, 0, label, onLog)
	if gone {
		return true // pruneEmptyDirTree 已经逐个打过日志
	}
	if fallbackCid != "" {
		if err := ops.moveFiles(fallbackCid, []string{cid}); err != nil {
			onLog(fmt.Sprintf("○ %s - 残留源目录移到冗余失败: %v", label, err))
		} else {
			onLog(fmt.Sprintf("○ %s - 源目录仍有残留内容，已连同内容移到冗余", label))
		}
	}
	return false
}

// orgProtectedCids 整理工作区的全部根目录：清理空目录时永不触碰
func orgProtectedCids(cfg *OrgConfig) []string {
	return []string{cfg.Library, cfg.Pending, cfg.Existing, cfg.Redundant, cfg.ShareCid}
}
