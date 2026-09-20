package api

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"115-station/internal/model"
)

// ==================== 深度删除 · 触发器 B：Emby webhook 加速通道 ====================
//
// 本地消失扫描（deepdel.go 的 scanVanished）默认 5 分钟一轮 + 两轮确认，
// 删一部片子要等到典型 10 分钟后才落地。webhook 把这个延迟压下来。
//
// ⚠️ **它只是加速通道，不是第二条删除逻辑。** 收到事件只做两件事：
// 把确认已从本地消失的台账行打上 vanish_at、必要时触发一次扫描。
// 删不删仍然由 runDeepDelete + deepdel.go §守卫 决定，挂载探针与量级阈值一个不绕。
//
// 为什么不敢做成「事件来了就删」：`library.deleted` 的含义是**这个条目没了**，
// 不是**用户要删它**。Emby 定时扫库发现文件不在了同样会清掉条目并发这个事件 ——
// 挂载抖一下就能让它连发一整批。qmediasync 对此没有任何防御（唯一开关是全局的
// EnableDeleteNetdisk），我们不重复这个错误。
//
// 2026-09-20 实测到的事件形态（真实载荷见 DEEP-DELETE-PLAN.md §8）：
//   - 原生 library.deleted **带 Item.Path**，电影精确到 .strm 文件；
//     删整部剧只发一条 Type=Series、IsFolder=true 的事件，Path 是剧目录 ——
//     所以按 rel_path「精确 + 前缀」两路匹配就够，不必像 qmediasync 那样
//     分 Movie/Episode/Season/Series 四路；
//   - 装了神医助手时，deep.delete 与 library.deleted **两条都发**（不是替换），
//     Date 只差几毫秒且到达顺序不保证；
//   - deep.delete 的 Description 里除了 Item Path 还有 Mount Paths ——
//     那就是 strm 的内容 http://host:6086/d/{pickcode}.mkv?/名字，
//     等于直接把 pickcode 送到手上，比路径更精确（不经过路径映射）。

// deepDelOnEmbyDelete Emby 删除事件 → 打标（+ deep.delete 时立即触发一轮）。
//
// deep 为真表示这是神医助手的 deep.delete：它是用户在界面上点「深度删除」
// 按钮触发的，**载荷本身携带明确意图**，扫库清理不会发它。所以可以拿它换延迟
// （立刻跑一轮，秒级落地），但不拿它换守卫 —— 挂载探针与阈值照旧。
func (h *Handler) deepDelOnEmbyDelete(payload map[string]interface{}, deep bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[深度删除] ✗ webhook 处理 panic 已恢复: %v", r)
		}
	}()
	cfg := h.loadDeepDelCfg()
	if !cfg.Enabled {
		return
	}

	// 开关开着就把每一步的结果打进常驻日志。这条链有四种「什么都没发生」的结局
	// （定位不到 / 台账没有 / 本地文件还在 / 只标记不删），全写进 vlog 的话
	// 用户在界面上看到的就是「删了但网盘没动」，没有任何线索可查
	rels, pickcodes := h.deepDelLocators(payload, deep)
	if len(rels) == 0 && len(pickcodes) == 0 {
		log.Printf("[深度删除] ○ Emby 删除事件里没有可用的定位信息（载荷没带 Item.Path？），交给定时扫描")
		return
	}

	marked, matched := h.markVanishedByLocators(rels, pickcodes)
	switch {
	case matched == 0:
		log.Printf("[深度删除] ○ Emby 删除的内容不在台账里（路径 %v），本站没同步过它或路径映射对不上，跳过",
			rels)
		return
	case marked == 0:
		// 本地文件还在 = Emby 只是把条目移出库、没删文件，或者媒体目录是只读挂载。
		// 两种情况都不该删网盘
		log.Printf("[深度删除] ○ 台账命中 %d 个条目，但本地文件都还在，未标记（Emby 只是移出库、或媒体目录只读？）", matched)
		return
	}
	log.Printf("[深度删除] ○ Emby 删除事件：台账命中 %d 个，其中 %d 个本地文件已消失，标记待删", matched, marked)

	if !deep {
		// 原生事件的意图不明确，只完成「首轮确认」，第二轮留给定时扫描。
		// 延迟因此从典型 10 分钟降到 5 分钟，而两轮之间的时间间隔（抗瞬时抖动的
		// 关键）仍然保留 —— 这是加速与安全之间的那条线
		log.Printf("[深度删除] ○ 已标记，等下一轮扫描复核后执行（原生删除事件不跳过两轮确认）")
		return
	}
	if !cfg.auto() {
		log.Printf("[深度删除] ○ 已标记，但当前是「只标记」模式，不会自动删除 —— " +
			"到「Strm 管理 → 全量同步 → 深度删除」确认后执行，或把删除方式改成「自动删除」")
		return
	}
	// deep.delete：用户显式点的按钮，意图明确，立刻跑一轮把它删掉
	h.runDeepDelScanNow()
}

// deepDelLocators 从事件载荷里抽出定位信息：台账相对路径 + pickcode。
// 两者都返回，调用方按「命中任一即可」处理 —— 路径受路径映射影响，
// pickcode 不受，互为备份。
func (h *Handler) deepDelLocators(payload map[string]interface{}, deep bool) (rels, pickcodes []string) {
	root := h.orphanLocalRoot()
	addRel := func(embyPath string) {
		if embyPath == "" {
			return
		}
		rel := relPathFromLocal(root, h.mapFromEmbyPath(embyPath))
		if rel != "" {
			rels = append(rels, rel)
		}
	}

	item, _ := payload["Item"].(map[string]interface{})
	if p, ok := item["Path"].(string); ok {
		addRel(p)
	}

	if !deep {
		return rels, nil
	}
	// deep.delete 的 Description 是给人看的纯文本，但它是多版本删除时
	// 唯一能拿到全部条目的地方（Item.Path 只有一条）
	desc, _ := payload["Description"].(string)
	for _, p := range parseEmbyItemPaths(desc) {
		addRel(p)
	}
	for _, u := range parseEmbyMountPaths(desc) {
		if pc := pickcodeFromStrmURL(u); pc != "" {
			pickcodes = append(pickcodes, pc)
		}
	}
	return rels, pickcodes
}

// markVanishedByLocators 给命中的台账行打 vanish_at，返回打标数。
//
// **打标之前一定要 os.Stat 确认本地文件真的没了。** 事件说「删了」不等于文件没了：
// Emby 可能只是把条目移出库、媒体目录可能是只读挂载。不确认就打标，等于让
// webhook 绕过深度删除的全部前提。
// 返回 (打标数, 台账命中数)：两个数分开报，才能在日志里区分
// 「压根没同步过 / 路径映射不对」与「同步过但本地文件还在」
func (h *Handler) markVanishedByLocators(rels, pickcodes []string) (marked, matched int) {
	root := h.orphanLocalRoot()
	seen := map[uint]bool{}

	mark := func(rows []model.SyncedFile) {
		for _, r := range rows {
			if seen[r.ID] || r.RelPath == "" {
				continue
			}
			seen[r.ID] = true
			matched++
			if r.VanishAt != nil || r.OrphanAt != nil {
				continue // 已经标过 / 网盘那份也没了
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(r.RelPath))); err == nil {
				continue // 本地文件还在，不打标
			} else if !os.IsNotExist(err) {
				continue // 读不出来不等于没了
			}
			if h.DB.Model(&model.SyncedFile{}).Where("id = ?", r.ID).
				Update("vanish_at", time.Now()).Error == nil {
				marked++
			}
		}
	}

	for _, rel := range rels {
		var rows []model.SyncedFile
		// 精确命中一个 strm，或前缀命中一整棵子树（删整部剧时 Path 是剧目录）。
		// LIKE 必须过 likeEscape：库名或目录名带 _ / % 时不转义会跨目录匹配，
		// 理由见 orphan115.go 里那段注释
		h.DB.Where(`rel_path = ? OR rel_path LIKE ? ESCAPE '\'`, rel, likeEscape(rel)+"/%").Find(&rows)
		mark(rows)
	}
	for _, pc := range pickcodes {
		var rows []model.SyncedFile
		h.DB.Where("pick_code = ? AND pick_code != ''", pc).Find(&rows)
		mark(rows)
	}
	return marked, matched
}

// relPathFromLocal 本地绝对路径 → 台账 rel_path（去掉媒体库根前缀）。
// 不在根下面就返回空：拿一个库外的路径去前缀匹配台账是危险的
func relPathFromLocal(root, local string) string {
	root = strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/")
	local = strings.TrimRight(strings.ReplaceAll(local, "\\", "/"), "/")
	if root == "" || local == "" || !strings.HasPrefix(local, root+"/") {
		return ""
	}
	return strings.Trim(local[len(root):], "/")
}

// parseEmbyItemPaths 从 deep.delete 的 Description 里抽出全部 Item Path。
//
// 格式（实测）：
//
//	Item Name:
//	海洋奇缘：启航
//
//	Item Path:
//	/media/影视/电影/…/xxx.mkv.strm
//
//	Mount Paths:
//	http://host:6086/d/{pickcode}.mkv?/xxx.mkv
//
// 多版本删除时 Item Path 下会有多行。思路取自 p115strmhelper 的
// parse_item_paths_from_description（Python），这里是重写的 Go 版本
func parseEmbyItemPaths(desc string) []string {
	return parseEmbySection(desc, "Item Path:", func(line string) bool {
		return strings.HasPrefix(line, "/") || strings.HasPrefix(line, `\`) ||
			(len(line) > 2 && line[1] == ':' && (line[2] == '/' || line[2] == '\\'))
	})
}

// parseEmbyMountPaths 同上，取 Mount Paths 段里的 URL（strm 的内容）
func parseEmbyMountPaths(desc string) []string {
	return parseEmbySection(desc, "Mount Paths:", func(line string) bool {
		return strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://")
	})
}

// embyDescSections Description 里的段标题。扫到任意一个就说明当前段结束了
var embyDescSections = []string{"Item Name:", "Item Path:", "Mount Paths:", "Description:", "Other Info:"}

func parseEmbySection(desc, header string, accept func(string) bool) []string {
	if desc == "" {
		return nil
	}
	var out []string
	in := false
	for _, raw := range strings.Split(desc, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasPrefix(line, header) {
			in = true
			// 「Item Path: /x/y」这种同行写法也要收
			if v := strings.TrimSpace(strings.TrimPrefix(line, header)); v != "" && accept(v) {
				out = append(out, v)
			}
			continue
		}
		if !in {
			continue
		}
		if line == "" {
			continue
		}
		for _, sec := range embyDescSections {
			if strings.HasPrefix(line, sec) {
				in = false
				break
			}
		}
		if !in {
			continue
		}
		if accept(line) {
			out = append(out, line)
		}
	}
	return out
}

// pickcodeFromStrmURL 从 strm 内容里抠 pickcode：http://host:6086/d/{pickcode}[.ext][?/名字]。
// 剥后缀的规则与 302 端点 handleProxyRedirect 一致（115 pickcode 是纯字母数字）
func pickcodeFromStrmURL(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	i := strings.Index(u, "/d/")
	if i < 0 {
		return ""
	}
	seg := u[i+3:]
	if j := strings.Index(seg, "/"); j >= 0 {
		seg = seg[:j]
	}
	if j := strings.LastIndex(seg, "."); j > 0 {
		seg = seg[:j]
	}
	// 旧版 STRM 用数字 fid 生成 /d/{fid}/…，那不是 pickcode，交给路径那条线去匹配
	if seg == "" || isAllDigits(seg) {
		return ""
	}
	return seg
}
