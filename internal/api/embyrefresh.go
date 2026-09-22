package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ==================== EMBY 刷新通知（出站：本站 → Emby）====================
//
// 端点选择的依据（2026-09-20 对照 MoviePilot 2、p115strmhelper、qmediasync）：
// 三个项目刷新条目/媒体库用的都是 `POST /Items/{Id}/Refresh`，没有一个用
// `POST /Library/{Id}/Refresh` —— Emby 根本没注册后者，打过去是 404。
// 我们此前用的就是它，而且**没看状态码**：404 被当成「提交成功」，refreshed++
// 之后直接 return，连带下面 /Library/Media/Updated 的回退分支也永远走不到。
// 表现就是新增看着像正常（实际是 Emby 自己的实时监控/定时扫库兜的底），
// 删除完全不生效 —— 文件没了条目还在，点进去播放 404。
//
// 删除为什么不能只靠刷新（2026-09-21 实测改的结论）：刷新只是把「重新扫一遍」
// 排进 Emby 的队列，什么时候扫到是 Emby 说了算。实测网盘删片 → 本地 strm 立刻没了，
// Emby 里的条目又挂了 9 分钟才消失（library.deleted webhook 为证），这期间点进去
// 就是播放 404 —— 正是用户要我们解决的那个现象。
//
// 参考项目在这一步都是空白：MoviePilot 的 emby 模块全文没有删除条目的调用，
// p115strmhelper 的 refresh_mediaserver 只在新增 strm 的分支里调，remove() 删完
// 本地文件就结束了。它们都在等 Emby 自己的媒体库监控/定时扫描。
//
// 所以删除走「先精确删条目、删不掉再刷新」：按路径查到条目 id 再 DELETE /Items/{Id}，
// 立刻生效。怕删错是对的，护栏是 embyDeleteItems 里那道「本地路径确实已经不存在」
// 的检查 —— 只删我们自己刚删掉的那一个路径，路径还在就一律不碰。
//
// ⚠️ **新增不能刷新「刚建出来的那个片目目录」——要刷媒体库**（2026-09-21 定稿）。
//
// 实测现象：整理落盘几秒后 Emby 的文件监控就会在影片目录上建一个 Type=Folder
// 的目录条目，于是「按路径查得到条目」成立、我们把刷新打在了它身上，
// 结果片子晚了 4 分钟才真正进库。原因是刷新走 ValidateChildren、复核的是
// 【已知】子条目，把一个 Folder 条目刷一遍不会让 Emby 重新判定
// 「这目录其实是部电影」—— 那个判定属于库扫描。
//
// 五个参考项目在这一步的做法高度一致，没有一个刷新新建目录自己：
//
//   MoviePilot 2 refresh_library_by_items → POST /Items/{媒体库Id}/Refresh?Recursive=true
//                                           剧集已存在就刷那个 Series 条目；电影已存在干脆不刷
//   p115strmhelper refresh_mediaserver    → 走 MoviePilot 上面那条；识别不了才回退
//                                           trigger_refresh_by_path，而它是从 Path.parents
//                                           爬的 —— **刻意不含路径自己**。全量收尾直接
//                                           refresh_root_library()，还带可配置的刷新延迟
//   qmediasync RefreshLibrary             → POST /emby/Items/{libraryId}/Refresh
//   MediaSync115 refresh_library          → POST /emby/Library/Refresh（全库扫描）
//   openStrm refreshEmbyNow               → POST /Library/Refresh（全库扫描，增量侧 30s 静默防抖）
//
// 所以入库只保留一种精确刷新：**目标路径上已经是影视条目**（Movie/Series/…）。
// 那正是 MoviePilot「剧集已存在就刷 Series」的情形 —— 条目在，
// ValidateChildren 能发现它下面的新集。其余一律刷媒体库条目。
// 我们的新增本来就是按轮聚合的（整理一轮只传最浅目录、增量一轮只传一个 base），
// 不像 openStrm 那样一条生活事件一次，所以不需要再加防抖。
//
// 顺带记一笔：/Library/Media/Updated 看起来像是「报新内容」的正式通道，
// 但五个参考项目**一个都没用**，我们也不用 —— 它在这里只作为
// 「路径不落在任何媒体库里」的兜底（embyReportUpdated）。

// embyRefreshKind 刷新场景。删除与新增有三处不一样：
// 目标路径在本地已经不存在（得上移到最近的存活父目录才找得到 Emby 条目）、
// 回退通知的 UpdateType、以及要不要发「媒体入库」消息
type embyRefreshKind int

const (
	embyRefreshAdded embyRefreshKind = iota
	embyRefreshDeleted
)

func (k embyRefreshKind) updateType() string {
	if k == embyRefreshDeleted {
		return "Deleted"
	}
	return "Created"
}

func (k embyRefreshKind) label() string {
	if k == embyRefreshDeleted {
		return "删除"
	}
	return "入库"
}

// embyItemRefreshMax 同一个媒体库内最多精确刷几个条目，超过就整库刷一次。
// 精确刷要先按路径查条目 id（一次 GET / 条），批量清理时几百次查询
// 比整库扫一次还贵 —— 这个阈值就是「省一次全库扫」和「别把 Emby 打爆」的分界
const embyItemRefreshMax = 8

// embyRefreshCfg EMBY管理 卡片里与刷新相关的配置。
// 一次读齐：此前 enabled/style 走 settingValueCompat、服务器地址与路径映射走
// h.getSettingValue，同一份配置被两条路径分别解析，新装环境下能读出不同结果
type embyRefreshCfg struct {
	ServerURL      string `json:"server_url"`
	APIKey         string `json:"api_key"`
	PathMapping    string `json:"path_mapping"`
	Style          string `json:"style"`
	RefreshEnabled any    `json:"refresh_enabled"`
}

// enabled 入库时刷新是否打开。字段缺失（老配置里没这项）视为开
func (c embyRefreshCfg) enabled() bool {
	switch v := c.RefreshEnabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return true
}

// loadEmbyRefreshCfg 读 emby 配置；服务器地址没配返回 false（没配就不刷新）
func loadEmbyRefreshCfg() (embyRefreshCfg, bool) {
	var cfg embyRefreshCfg
	v := settingValueCompat("emby")
	if v == "" || json.Unmarshal([]byte(v), &cfg) != nil {
		return cfg, false
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	return cfg, cfg.ServerURL != ""
}

// notifyEmbyRefresh STRM 生成后通知 Emby 刷新入库。
// verifyLocal 是本轮真正落盘的文件（.strm 本体），只给回查用：
// 刷新按目录聚合，但「这一集到底进没进去」只有查到它自己才算数
func (h *Handler) notifyEmbyRefresh(localPath string, verifyLocal ...string) {
	notifyEmbyPaths([]string{localPath}, embyRefreshAdded, verifyLocal...)
}

// notifyEmbyDeleted 本地 STRM / 目录已经删掉了 → 通知 Emby 把对应条目清掉。
//
// 不通知的后果是用户最容易撞上的那个坑：网盘删了片子、增量同步删了 strm，
// 但 Emby 里条目还在，点进去播放 404。
// 做成包级函数（不挂 *Handler）是因为删 strm 的地方一半是自由函数
// （dropLocalByFidsExcept、洗版清旧版），跟 localMediaRoot 同样的理由
func notifyEmbyDeleted(localPaths ...string) {
	notifyEmbyPaths(localPaths, embyRefreshDeleted)
}

// notifyEmbyPaths 通知 Emby 一批本地路径发生了变化。
//
// 删除场景先试一次「按路径精确删条目」，成功的就不必再刷新了。
// 剩下的（以及全部新增场景）走三级策略，逐级降级：
//  1. 按路径定位到具体条目，只刷那一个（万级库不必整库扫）；
//     新增场景还要过 embyAddCanRefreshItem 这一关（剧集/季条目刷了没用）
//  2. 定位不到（或不该只刷条目）就刷该路径所属的媒体库条目
//  3. 路径不属于任何媒体库（路径映射配错了？）→ /Library/Media/Updated 报路径
func notifyEmbyPaths(localPaths []string, kind embyRefreshKind, verifyLocal ...string) {
	if len(localPaths) == 0 {
		return
	}
	cfg, ok := loadEmbyRefreshCfg()
	if !ok || !cfg.enabled() {
		return
	}

	// 媒体库列表懒加载：删条目与下面的分桶共用一次查询，
	// 而挂载掉线那种「一个请求都不该发」的情况下一次也不查
	var libs []embyMediaFolder
	libsLoaded := false
	loadLibs := func() []embyMediaFolder {
		if !libsLoaded {
			libs, libsLoaded = embyMediaFolders(cfg), true
		}
		return libs
	}

	if kind == embyRefreshDeleted {
		// 这批删除是本站自己做的（洗版让位、增量同步清 strm、深度删除）。
		// Emby 处理完会把 library.deleted 推回来，那条事件不该再当成
		// 「有人在 Emby 里删了东西」推一条通知给用户
		for _, local := range localPaths {
			markEmbySelfDeleted(embyPathOf(cfg, local))
		}
		// 精确删掉的路径不再进入刷新流程；全删干净就整轮结束
		if localPaths = embyDeleteItems(cfg, loadLibs, localPaths); len(localPaths) == 0 {
			return
		}
	}

	targets := embyTargetPaths(localPaths, cfg, kind)
	if len(targets) == 0 {
		return
	}

	// 变更路径分桶到所属媒体库
	libs = loadLibs()
	byID := map[string]embyMediaFolder{}
	buckets := map[string][]embyTarget{}
	wholeLib := map[string]bool{} // 只能整库刷的桶（目标路径在库之上，没有更小的条目可刷）
	var order []string
	var unmatched []string
	add := func(lib embyMediaFolder, t embyTarget) {
		if _, seen := buckets[lib.ID]; !seen {
			order = append(order, lib.ID)
			byID[lib.ID] = lib
		}
		buckets[lib.ID] = append(buckets[lib.ID], t)
	}
	for _, t := range targets {
		var hit embyMediaFolder
		for _, lib := range libs {
			if embyPathUnder(t.path, lib.Locations) {
				hit = lib
				break
			}
		}
		if hit.ID != "" {
			add(hit, t)
			continue
		}
		// 反向包含：目标路径在媒体库【之上】。全量同步传的就是媒体库根，
		// 而一键建库把库建在根下面第二层 —— 只做正向判断的话一个都不命中，
		// 路径通知又落不到任何库上，整轮等于白发
		covered := false
		for _, lib := range libs {
			if embyLocationsUnder(lib.Locations, t.path) {
				add(lib, t)
				wholeLib[lib.ID] = true
				covered = true
			}
		}
		if !covered {
			unmatched = append(unmatched, t.reportPaths()...)
		}
	}

	var refreshed []string
	var verify []string // 入库场景提交完回查用（只读，不改变 Emby 行为）
	// 调用方点名了落盘文件就只回查它们：刷新目标是标题目录，而标题目录上
	// 早就挂着 Series 条目，拿它回查等于自问自答，永远是 ✓
	for _, p := range verifyLocal {
		if ep := embyPathOf(cfg, p); ep != "" {
			verify = append(verify, ep)
		}
	}
	namedVerify := len(verify) > 0
	for _, libID := range order {
		lib, paths := byID[libID], buckets[libID]
		if kind == embyRefreshAdded && !namedVerify {
			for _, t := range paths {
				verify = append(verify, t.path)
			}
		}
		// 少量变更精确到条目刷（目标在库之上时没这个选项，只能整库刷）
		if !wholeLib[libID] && len(paths) <= embyItemRefreshMax {
			var rest []string
			for _, t := range paths {
				hit, exact := embyResolveItem(cfg, t, lib.Locations)
				// ⚠️ 入库能精确刷的只有「路径上正好是一个叶子影视条目」，
				// 其余一律交给整库刷新 —— 理由见 embyAddCanRefreshItem
				if kind == embyRefreshAdded && !embyAddCanRefreshItem(exact, hit.Type) {
					rest = append(rest, t.path)
					continue
				}
				if hit.ID == "" || !embyRefreshItem(cfg, hit.ID) {
					rest = append(rest, t.path)
					continue
				}
				if exact {
					log.Printf("[Emby] ○ 已提交条目刷新（%s）：%s（%s）—— %s", kind.label(), hit.Name, hit.Type, t.path)
				} else {
					log.Printf("[Emby] ○ 已提交条目刷新（%s）：上溯命中「%s」—— %s", kind.label(), hit.Name, t.path)
				}
			}
			if len(rest) == 0 {
				refreshed = append(refreshed, lib.Name)
				continue
			}
			if kind == embyRefreshAdded {
				log.Printf("[Emby] ○ %d 个新增路径没法只刷条目（还没有影视条目，或是剧集/季条目——刷它发现不了新的一集），改为刷新媒体库 %s", len(rest), lib.Name)
			} else {
				log.Printf("[Emby] ○ %d 个路径未定位到条目，改为刷新整个媒体库 %s", len(rest), lib.Name)
			}
		}
		if embyRefreshItem(cfg, lib.ID) {
			refreshed = append(refreshed, lib.Name)
			log.Printf("[Emby] ○ 已提交媒体库刷新（%s）：%s", kind.label(), lib.Name)
			continue
		}
		log.Printf("[Emby] ✗ 媒体库刷新失败：%s（%s），回退路径通知", lib.Name, lib.ID)
		for _, t := range paths {
			unmatched = append(unmatched, t.reportPaths()...)
		}
	}

	// 入库提交完回查一次，结论写进日志（纯只读，不改变 Emby 行为）
	if kind == embyRefreshAdded && len(verify) > 0 {
		go embyVerifyIngest(cfg, dedupeStrings(verify))
	}

	if len(unmatched) > 0 {
		if len(libs) > 0 {
			log.Printf("[Emby] ○ %d 个变更路径未命中任何媒体库（%v），回退路径通知 —— 路径映射配对了吗",
				len(unmatched), unmatched)
		}
		embyReportUpdated(cfg, unmatched, kind.updateType())
	}

	// 「已刷新媒体库：电影」这条消息不发了：它说的是「我让 Emby 去扫了一下」，
	// 对用户没有任何信息量，却每轮入库都要占一条推送。入库本身有卡片，
	// 刷新提交与回查结论都在日志里（MoviePilot / p115strmhelper 也都不推这个）
	if kind == embyRefreshAdded && len(refreshed) > 0 {
		log.Printf("[Emby] ○ 已提交刷新（入库）：%s", strings.Join(dedupeStrings(refreshed), "、"))
	}
}

// embyVerifyDelays 入库回查的时间点（相对提交刷新的时刻）。
// Emby 的文件监控在处理前有一段「等路径不再变动」的静默期，刮削紧跟着往
// 同一个目录写 NFO/海报还会把它一次次推后，所以第一次回查放在 30 秒之后。
// 测试里置空即关闭
var embyVerifyDelays = []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second}

// embyVerifyPending 正在回查的路径。同一条路径同时只跑一轮回查
var (
	embyVerifyMu      sync.Mutex
	embyVerifyPending = map[string]bool{}
)

// embyVerifyClaim 认领这批路径，返回真正轮到自己回查的那些
func embyVerifyClaim(paths []string) []string {
	embyVerifyMu.Lock()
	defer embyVerifyMu.Unlock()
	var mine []string
	for _, p := range paths {
		if embyVerifyPending[p] {
			continue
		}
		embyVerifyPending[p] = true
		mine = append(mine, p)
	}
	return mine
}

func embyVerifyRelease(paths []string) {
	embyVerifyMu.Lock()
	defer embyVerifyMu.Unlock()
	for _, p := range paths {
		delete(embyVerifyPending, p)
	}
}

// embyVerifyIngest 提交刷新之后回查：Emby 到底收进去没有。
// 这是本项目自己加的一层，参考项目都没有 —— 但它是纯只读的，不改变 Emby 行为。
//
// 这条日志是给人看的 —— 入库链路上能出错的地方（路径映射、媒体库范围、库类型、
// 实时监控开关）在提交那一刻全都表现为「提交成功」，不回查就只能靠用户
// 一遍遍去 Emby 界面上翻。查到影视条目就报用时，只看到目录条目或什么都没有
// 就把该查哪儿写在日志里
func embyVerifyIngest(cfg embyRefreshCfg, paths []string) {
	if len(paths) == 0 || len(embyVerifyDelays) == 0 {
		return
	}
	// 同一次入库会被提交两次刷新（整理一次、增量同步再一次）。
	// 回查只让先到的那一轮跑，否则同一条结论要在日志里出现两遍
	paths = embyVerifyClaim(paths)
	if len(paths) == 0 {
		return
	}
	defer embyVerifyRelease(paths)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Emby] ✗ 入库回查异常: %v", r)
		}
	}()
	start := time.Now()
	pending := append([]string(nil), paths...)
	lastType := map[string]string{}
	for _, d := range embyVerifyDelays {
		select {
		case <-stopCh:
			return
		case <-time.After(d - time.Since(start)):
		}
		var rest []string
		for _, p := range pending {
			hits := embyItemsByPath(cfg, p)
			if len(hits) == 0 {
				rest = append(rest, p)
				continue
			}
			hit := pickMediaHit(hits)
			if !embyTypeIsMedia(hit.Type) {
				// 目录条目下面挂着影视条目也算入库（详见 embyMediaChildOf）
				child, ok := embyMediaChildOf(cfg, hit.ID)
				if !ok {
					lastType[p] = hit.Type
					rest = append(rest, p)
					continue
				}
				hit = child
			}
			log.Printf("[Emby] ✓ 入库确认：%s（%s，用时 %s）—— %s",
				hit.Name, hit.Type, time.Since(start).Truncate(time.Second), p)
		}
		if pending = rest; len(pending) == 0 {
			return
		}
	}
	for _, p := range pending {
		if t := lastType[p]; t != "" {
			log.Printf("[Emby] ✗ 入库未完成：%s 在 Emby 里只有目录条目（Type=%s），影片没被识别 —— "+
				"检查这个目录是不是在某个媒体库的范围内、库类型是不是「电影/剧集」", p, t)
			continue
		}
		log.Printf("[Emby] ✗ 入库未完成：刷新提交 %s 后 Emby 仍查不到 %s 的任何条目 —— "+
			"检查「EMBY 管理」的本地路径映射，以及 Emby 那边的媒体库目录是否包含它",
			time.Since(start).Truncate(time.Second), p)
	}
}

// embyTarget 一个刷新目标。
// ancestors 是「自己 + 逐级父目录」的 Emby 路径，定位条目时按序试：
// 刚落盘的新片目录 Emby 还没建条目，这时候命中的是它的父目录 —— 刷父目录
// 同样能让 Emby 发现新文件，比整库扫一遍便宜得多
type embyTarget struct {
	path      string   // 主目标（分桶用它）
	ancestors []string // path 自己排第一，之后逐级向上，到本地媒体库根为止
	// origins 删除场景里上移之前那些真正被删掉的 Emby 路径。
	// 回退通知必须报它们而不是 path：path 是还活着的父目录，
	// 拿它去报 UpdateType=Deleted 等于告诉 Emby「整个 电影 目录没了」
	origins []string
}

// reportPaths 回退给 /Library/Media/Updated 的路径
func (t embyTarget) reportPaths() []string {
	if len(t.origins) > 0 {
		return t.origins
	}
	return []string{t.path}
}

// embyTargetPaths 本地路径 → 去重后的刷新目标。
// 删除场景先上移到最近还存在的父目录：被删的路径本身已经不在了，
// 拿它去查 Emby 条目必然落空（思路同 p115strmhelper 的 trigger_refresh_by_path）。
//
// 祖先链在这里就地生成、逐级做路径映射：不能拿映射后的字符串去切父目录 ——
// style=windows 时那是反斜杠路径，用 path.Dir 切会切出错的东西
func embyTargetPaths(localPaths []string, cfg embyRefreshCfg, kind embyRefreshKind) []embyTarget {
	root := filepath.Clean(localMediaRoot())
	underRoot := func(x string) bool {
		return x == root || strings.HasPrefix(x, root+string(filepath.Separator))
	}
	toEmby := func(local string) string { return embyPathOf(cfg, local) }

	out := make([]embyTarget, 0, len(localPaths))
	seen := map[string]int{} // Emby 路径 → out 下标
	for _, p := range localPaths {
		if p == "" {
			continue
		}
		origin := ""
		if kind == embyRefreshDeleted {
			origin = toEmby(filepath.Clean(p))
			if p = nearestExistingDir(p, root); p == "" {
				continue
			}
		}
		var t embyTarget
		cur := filepath.Clean(p)
		for i := 0; i < fastMaxDepth; i++ {
			if ep := toEmby(cur); ep != "" {
				t.ancestors = append(t.ancestors, ep)
			}
			// 库根以上不再上溯；本来就不在库里的路径只取它自己
			if cur == root || !underRoot(cur) {
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
		if len(t.ancestors) == 0 {
			continue
		}
		t.path = t.ancestors[0]
		if origin != "" {
			t.origins = []string{origin}
		}
		// 同一个存活父目录下的多次删除折叠成一个刷新目标，
		// 但各自被删的原路径都要留下 —— 回退通知一条都不能少
		if i, dup := seen[t.path]; dup {
			out[i].origins = append(out[i].origins, t.origins...)
			continue
		}
		seen[t.path] = len(out)
		out = append(out, t)
	}
	return out
}

// ---- 自产删除标记 ----
//
// 本站删掉 STRM 之后会让 Emby 清条目，Emby 处理完又把 library.deleted
// 推回来。那条事件不是「有人删了片子」，是我们自己动作的回声 ——
// 报给用户就是噪音（洗版一次就能收到一条「🗑️ Emby 删除 美国队长」）。
// 窗口给得比较宽：删不掉条目时走的是刷新，Emby 实测能拖到 9 分钟才扫到
const embySelfDeleteTTL = 15 * time.Minute

var (
	embySelfDelMu sync.Mutex
	embySelfDel   = map[string]time.Time{}
)

func markEmbySelfDeleted(embyPath string) {
	if embyPath == "" {
		return
	}
	embySelfDelMu.Lock()
	defer embySelfDelMu.Unlock()
	now := time.Now()
	for k, t := range embySelfDel {
		if now.Sub(t) > embySelfDeleteTTL {
			delete(embySelfDel, k)
		}
	}
	embySelfDel[embyDelKey(embyPath)] = now
}

// embySelfDeleted 这条删除事件是不是本站自己捅出来的
func embySelfDeleted(embyPath string) bool {
	if embyPath == "" {
		return false
	}
	embySelfDelMu.Lock()
	defer embySelfDelMu.Unlock()
	t, ok := embySelfDel[embyDelKey(embyPath)]
	return ok && time.Since(t) <= embySelfDeleteTTL
}

// embyDelKey 路径风格（windows 反斜杠）与末尾斜杠都不参与比对
func embyDelKey(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
}

// embyPathOf 本地路径 → Emby 看到的路径（映射规则 + 路径风格）
func embyPathOf(cfg embyRefreshCfg, local string) string {
	ep := mapLocalToEmbyPath(cfg.PathMapping, local)
	if cfg.Style == "windows" {
		ep = strings.ReplaceAll(ep, "/", "\\")
	}
	return ep
}

// embyDeleteItems 按路径精确删除 Emby 条目，返回没删成、要回退刷新的本地路径。
//
// 这是删除链路上唯一「立刻生效」的手段：刷新只是排队等 Emby 扫，
// 实测能拖到 9 分钟，这期间条目还在库里挂着，点进去播放 404。
//
// 安全护栏只有一条，但够用：**本地路径必须确实已经不存在**。
// Emby 的 DELETE /Items/{Id} 连带删磁盘文件，所以绝不能对还活着的路径动手；
// 而走到这里的路径都是本站自己刚删掉的，再 Stat 一次确认没了才发请求。
// 条目路径也要与映射后的路径【完全相等】才算命中（embyItemIDByPath 里比的）
func embyDeleteItems(cfg embyRefreshCfg, loadLibs func() []embyMediaFolder, localPaths []string) []string {
	root := filepath.Clean(localMediaRoot())
	rest := make([]string, 0, len(localPaths))
	for _, local := range dedupeStrings(localPaths) {
		if local == "" {
			continue
		}
		// 「本地没了」这个判断在挂载掉线时对整个库都成立 —— 那一刻按路径删条目
		// 就是把整个媒体库从 Emby 里抹掉。nearestExistingDir 要求路径在库内、
		// 且往上能找到一个还活着的目录，根都没了就返回空，这一轮谁也不碰
		if nearestExistingDir(local, root) == "" {
			rest = append(rest, local)
			continue
		}
		if _, err := os.Stat(local); err == nil {
			// 还在：不是真删除（可能只是整理搬走了同名文件），交给刷新
			rest = append(rest, local)
			continue
		}
		ep := embyPathOf(cfg, local)
		// 只删严格在媒体库目录【之下】的东西。库目录自己对应的是 Emby 的库条目，
		// 删它等于整个媒体库从 Emby 消失 —— 网盘上删掉一整个分类目录时就会撞上
		if !embyPathStrictlyUnder(ep, loadLibs()) {
			rest = append(rest, local)
			continue
		}
		// 同一路径上可能同时挂着两个条目（实测被删的电影目录在 Emby 里是
		// Type=Folder，而刮削出的 Movie 也可能落在同一路径），一次删干净
		hits := embyItemsByPath(cfg, ep)
		if len(hits) == 0 {
			rest = append(rest, local)
			continue
		}
		done := true
		for _, hit := range hits {
			if !embyDeleteItem(cfg, hit.ID) {
				done = false
				continue
			}
			log.Printf("[Emby] ○ 已删除条目：%s —— %s", hit.Name, ep)
		}
		if !done {
			rest = append(rest, local)
		}
	}
	return rest
}

// embyDeleteItem DELETE /Items/{Id}。
// Emby 同时注册了 POST /Items/{Id}/Delete，DELETE 被反代拦掉（405）时用它兜底。
// 403/401 一般是 API 密钥对应的用户没开「允许删除媒体」，日志里说清楚，
// 调用方会退回刷新，不至于整条链路哑掉
func embyDeleteItem(cfg embyRefreshCfg, itemID string) bool {
	try := func(method, path string) (int, bool) {
		resp, err := embyRequest(method, cfg.ServerURL, cfg.APIKey, path, nil, nil)
		if err != nil {
			log.Printf("[Emby] ✗ 删除条目请求失败 %s: %v", itemID, err)
			return 0, false
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	code, ok := try(http.MethodDelete, "/Items/"+itemID)
	if ok {
		return true
	}
	if code == http.StatusMethodNotAllowed || code == http.StatusNotFound {
		if _, ok := try(http.MethodPost, "/Items/"+itemID+"/Delete"); ok {
			return true
		}
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		log.Printf("[Emby] ✗ 删除条目被拒（HTTP %d，条目 %s）—— API 密钥对应的用户要勾上「允许删除媒体」", code, itemID)
		return false
	}
	log.Printf("[Emby] ✗ 删除条目失败（HTTP %d，条目 %s），回退刷新", code, itemID)
	return false
}

// embyResolveItem 沿祖先链找第一个 Emby 认识的条目，出了媒体库就停。
// 逐级上溯的做法取自 p115strmhelper 的 trigger_refresh_by_path。
//
// exact 表示命中的就是目标路径本身（ancestors[0]）；命中祖先说明 Emby
// 压根还不知道目标路径的存在。**但 exact 不等于「已入库」**，判据看 hit.Type
func embyResolveItem(cfg embyRefreshCfg, t embyTarget, locations []string) (hit embyItemHit, exact bool) {
	for i, a := range t.ancestors {
		if !embyPathUnder(a, locations) {
			return embyItemHit{}, false // 再往上就出了这个媒体库，交给整库刷新
		}
		if hits := embyItemsByPath(cfg, a); len(hits) > 0 {
			return pickMediaHit(hits), i == 0
		}
	}
	return embyItemHit{}, false
}

// pickMediaHit 同一路径上的多个条目里优先挑真正的影视条目。
// 被删的影片目录实测会同时挂着 Type=Folder 与刮削出的 Movie，刷新要挑后者
func pickMediaHit(hits []embyItemHit) embyItemHit {
	for _, h := range hits {
		if embyTypeIsMedia(h.Type) {
			return h
		}
	}
	return hits[0]
}

// embyTypeIsMedia 这个条目类型算不算「影片已经入库」。
// Folder 不算 —— Emby 的文件监控发现新目录就会先建一个目录条目，
// 影片要等库扫描的解析器把这个目录认成 Movie 才叫入库。空 Type 按保守算「不是」
func embyTypeIsMedia(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "movie", "series", "season", "episode", "video", "musicvideo":
		return true
	}
	return false
}

// embyAddCanRefreshItem 新增场景能不能只刷这一个条目。
//
// 两条判据：路径上得**正好**是个影视条目（只有 Folder 条目意味着还没入库，
// 刷它不会让 Emby 重新判定这目录是部电影）；而且它不能是 Series / Season ——
// 刷新跑的是 ValidateChildren，复核的是 Emby【已知】的子条目还在不在，
// 发现不了刚落进季目录里的新一集。
//
// 2026-09-22 游戏王 S01E153 就是栽在后一条上：剧集早在库里（223 集），
// 新一集落盘后精确刷了 Series 条目、提交成功、回查还报了 ✓，集数纹丝不动，
// 最后是 Emby 自己的文件监控隔了六分钟才把它捞进去
func embyAddCanRefreshItem(exact bool, itemType string) bool {
	if !exact || !embyTypeIsMedia(itemType) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "series", "season":
		return false
	}
	return true
}

// nearestExistingDir 从 p 往上找第一个还存在的目录，到 root 为止（含 root）。
// p 不在 root 下面时返回空 —— 媒体库外的路径不该触发刷新
func nearestExistingDir(p, root string) string {
	root = filepath.Clean(root)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return ""
	}
	p = filepath.Clean(p)
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return ""
	}
	for i := 0; i < fastMaxDepth; i++ {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		if p == root {
			return "" // 根都没了：挂载掉线，这时候刷新只会让 Emby 清空整个库
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
	return ""
}

// embyPathUnder 路径是否落在某个媒体库的目录下。
// 两边都归一成正斜杠再比：Emby 装在 Windows 上时 Locations 是反斜杠，
// 我们的路径受 style 配置影响，不归一就永远对不上
func embyPathUnder(embyPath string, locations []string) bool {
	p := strings.TrimRight(strings.ReplaceAll(embyPath, "\\", "/"), "/")
	for _, loc := range locations {
		l := strings.TrimRight(strings.ReplaceAll(loc, "\\", "/"), "/")
		if l == "" {
			continue
		}
		if p == l || strings.HasPrefix(p, l+"/") {
			return true
		}
	}
	return false
}

// embyLocationsUnder 媒体库的目录是否在 ancestor 【下面】（反向包含）。
// 只算严格在下面的：相等那种情况由 embyPathUnder 的正向判断先接走了
func embyLocationsUnder(locations []string, ancestor string) bool {
	a := strings.TrimRight(strings.ReplaceAll(ancestor, "\\", "/"), "/")
	if a == "" {
		return false
	}
	for _, loc := range locations {
		l := strings.TrimRight(strings.ReplaceAll(loc, "\\", "/"), "/")
		if l != "" && strings.HasPrefix(l, a+"/") {
			return true
		}
	}
	return false
}

// embyPathStrictlyUnder 路径是否严格落在某个媒体库目录【之下】。
// 与 embyPathUnder 的区别就是不含库目录本身，删条目前的护栏专用
func embyPathStrictlyUnder(embyPath string, libs []embyMediaFolder) bool {
	p := strings.TrimRight(strings.ReplaceAll(embyPath, "\\", "/"), "/")
	if p == "" {
		return false
	}
	for _, lib := range libs {
		for _, loc := range lib.Locations {
			l := strings.TrimRight(strings.ReplaceAll(loc, "\\", "/"), "/")
			if l != "" && strings.HasPrefix(p, l+"/") {
				return true
			}
		}
	}
	return false
}

// embyMediaFolder 使用虚拟库的实际目录，而不是 MediaFolders 的条目路径。
// 官方 VirtualFolders/Query 返回 Locations 与 ItemId；MediaFolders 的 BaseItemDto
// 没有 Locations，按它匹配会把所有媒体库都当成路径映射错误。
type embyMediaFolder struct {
	ID        string   `json:"Id"`
	ItemID    string   `json:"ItemId"`
	Name      string   `json:"Name"`
	Locations []string `json:"Locations"`
}

func embyMediaFolders(cfg embyRefreshCfg) []embyMediaFolder {
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Library/VirtualFolders/Query", nil, nil)
	if err != nil {
		log.Printf("[Emby] ✗ 取媒体库列表失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 此前这里不看状态码：401 会被当成「没有媒体库」静默走到回退分支
		log.Printf("[Emby] ✗ 取媒体库列表 HTTP %d（API 密钥填对了吗）", resp.StatusCode)
		return nil
	}
	var libs struct {
		Items []embyMediaFolder `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		log.Printf("[Emby] ✗ 解析媒体库目录失败: %v", err)
		return nil
	}
	valid := make([]embyMediaFolder, 0, len(libs.Items))
	for _, lib := range libs.Items {
		// ItemId 是供 Items 刷新使用的标识；兼容只返回 Id 的版本。
		if lib.ItemID != "" {
			lib.ID = lib.ItemID
		}
		if lib.ID != "" {
			valid = append(valid, lib)
		}
	}
	return valid
}

// embyItemIDByPath 按路径查 Emby 条目 id（做法取自 p115strmhelper 的
// get_item_id_by_path）。Emby 对 Path 的过滤不保证精确，返回前再比对一次完整路径。
//
// Limit 是防守：万一某个 Emby 版本压根不认 Path 参数，Recursive=true
// 会把整个库倒出来。比不中就返回空，调用方退到整库刷新，不会误刷别的条目
// embyItemHit 一条按路径命中的 Emby 条目。
// Type 必须留着：路径上有条目 ≠ 影片已经入库 —— Emby 的文件监控看到新目录会先
// 建一个 Type=Folder 的目录条目，影片要等解析器把它认成 Movie/Series 才算真进库
type embyItemHit struct{ ID, Name, Type string }

func embyItemsByPath(cfg embyRefreshCfg, embyPath string) (hits []embyItemHit) {
	q := url.Values{
		"Path":      {embyPath},
		"Recursive": {"true"},
		"Fields":    {"Path"},
		// 不带类型过滤时 Emby 不一定把 Folder 吐出来，而网盘里删掉的常常
		// 正是「片名.年份.{tmdbid=…}」这种目录（实测它在 Emby 里就是 Type=Folder）。
		// 类型清单抄 p115strmhelper 的 get_item_id_by_path，另加剧集季与裸视频
		"IncludeItemTypes":       {"Movie,Series,Season,Episode,Video,Folder"},
		"Limit":                  {"50"},
		"EnableTotalRecordCount": {"false"},
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		vlog("[Emby] 按路径查条目失败 %s: %v", embyPath, err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		vlog("[Emby] 按路径查条目 HTTP %d：%s", resp.StatusCode, embyPath)
		return nil
	}
	var out struct {
		Items []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
			Path string `json:"Path"`
			Type string `json:"Type"`
		} `json:"Items"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	want := strings.TrimRight(strings.ReplaceAll(embyPath, "\\", "/"), "/")
	for _, it := range out.Items {
		if strings.TrimRight(strings.ReplaceAll(it.Path, "\\", "/"), "/") == want {
			hits = append(hits, embyItemHit{ID: it.ID, Name: it.Name, Type: it.Type})
		}
	}
	return hits
}

// embyMediaChildOf 目录条目底下有没有真正的影视条目。
//
// 单文件影片在 Emby 里的条目路径是那个 .strm 文件，而不是它所在的目录
// （实测 webhook 载荷里的 Movie.Path 就是 .strm）。回查传的是目录，按路径
// 只查得到 Type=Folder —— 据此报「影片没被识别」是误判（2026-09-21 洗版
// 验证里就出了两条误报）。纯只读，只用于写日志
func embyMediaChildOf(cfg embyRefreshCfg, parentID string) (embyItemHit, bool) {
	if parentID == "" {
		return embyItemHit{}, false
	}
	q := url.Values{
		"ParentId":               {parentID},
		"Recursive":              {"true"},
		"IncludeItemTypes":       {"Movie,Series,Season,Episode,Video,MusicVideo"},
		"Limit":                  {"1"},
		"EnableTotalRecordCount": {"false"},
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		vlog("[Emby] 查目录子条目失败 %s: %v", parentID, err)
		return embyItemHit{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		vlog("[Emby] 查目录子条目 HTTP %d：%s", resp.StatusCode, parentID)
		return embyItemHit{}, false
	}
	var out struct {
		Items []struct{ Id, Name, Type string } `json:"Items"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return embyItemHit{}, false
	}
	for _, it := range out.Items {
		if embyTypeIsMedia(it.Type) {
			return embyItemHit{ID: it.Id, Name: it.Name, Type: it.Type}, true
		}
	}
	return embyItemHit{}, false
}

// embyItemIDByPath 同一路径上的第一个条目（刷新只需要一个入口）
func embyItemIDByPath(cfg embyRefreshCfg, embyPath string) (id, name string) {
	hits := embyItemsByPath(cfg, embyPath)
	if len(hits) == 0 {
		return "", ""
	}
	return hits[0].ID, hits[0].Name
}

// embyRefreshItem POST /Items/{Id}/Refresh —— Emby 真正的条目/媒体库刷新端点。
// {Id} 可以是媒体库条目，也可以是任意影片/剧集条目（MoviePilot、qmediasync、
// p115strmhelper 用的都是它，见 notifyEmbyPaths 上方的对照）。
//
// Recursive=true 会连带校验子项：文件已经不在的子条目在这一步被清掉，
// 「本地删了 Emby 里还挂着条目」就是靠它修的；打在媒体库条目上时，
// 这一遍校验就是新内容被扫进来的时机。
// MetadataRefreshMode 取 Default 而不是 FullRefresh：我们要的是「重新看一眼
// 文件还在不在」，不是把整库元数据重刮一遍（那会把 TMDB 打到限流）。
// MoviePilot 与 qmediasync 干脆一个模式参数都不传，用的就是 Emby 的默认值
func embyRefreshItem(cfg embyRefreshCfg, itemID string) bool {
	q := url.Values{
		"Recursive":           {"true"},
		"MetadataRefreshMode": {"Default"},
		"ImageRefreshMode":    {"Default"},
		"ReplaceAllMetadata":  {"false"},
		"ReplaceAllImages":    {"false"},
	}
	resp, err := embyRequest(http.MethodPost, cfg.ServerURL, cfg.APIKey, "/Items/"+itemID+"/Refresh", q, nil)
	if err != nil {
		log.Printf("[Emby] ✗ 条目刷新请求失败 %s: %v", itemID, err)
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[Emby] ✗ 条目刷新 HTTP %d（条目 %s）", resp.StatusCode, itemID)
		return false
	}
	return true
}

// embyReportUpdated 回退通道：路径没落在任何媒体库里，或者刷新请求失败时，
// 把变更直接报给 Emby 的媒体监控。UpdateType 按场景取 Created / Deleted ——
// 此前这里写死 Created，删除即使走到这条分支也表达不出来
func embyReportUpdated(cfg embyRefreshCfg, embyPaths []string, updateType string) {
	ups := make([]map[string]string, 0, len(embyPaths))
	for _, p := range embyPaths {
		ups = append(ups, map[string]string{"Path": p, "UpdateType": updateType})
	}
	body, _ := json.Marshal(map[string]any{"Updates": ups})
	resp, err := embyRequest(http.MethodPost, cfg.ServerURL, cfg.APIKey, "/Library/Media/Updated", nil, body)
	if err != nil {
		log.Printf("[Emby] ✗ 路径变更通知失败: %v", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[Emby] ✗ 路径变更通知 HTTP %d：%v", resp.StatusCode, embyPaths)
		return
	}
	log.Printf("[Emby] ○ 路径变更通知已发送（%s）：%v", updateType, embyPaths)
}

// mapLocalToEmbyPath 本地路径 → Emby 路径（mapToEmbyPath 的纯函数形态）。
// 抽出来是为了让包级的刷新通知复用同一套映射规则 —— 两份实现迟早分叉
func mapLocalToEmbyPath(pathMapping, local string) string {
	local = strings.ReplaceAll(local, "\\", "/")
	if pathMapping == "" {
		return local
	}
	src, dst := embyPathRoots(pathMapping)
	if src != "" && dst != "" && (local == src || strings.HasPrefix(local, src+"/")) {
		return dst + strings.TrimPrefix(local, src)
	}
	return local
}

// absUnder 台账相对路径 → 本地绝对路径（空串跳过）
func absUnder(root string, rels []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
	}
	return out
}

// dedupeStrings 去重保序
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
