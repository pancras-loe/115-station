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
// 删除为什么还是走「刷新」而不是「删条目」：Emby 对文件夹条目做 Recursive 刷新
// 时会跑一遍 ValidateChildren，文件已经不在的子条目就是在这一步被清掉的。
// 三个参考项目都没有调删除条目的 API，我们也不调 —— 自己判断该删哪个条目，
// 判错一次就是把用户还在的片子从库里抹掉。

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

// notifyEmbyRefresh STRM 生成后通知 Emby 刷新入库
func (h *Handler) notifyEmbyRefresh(localPath string) {
	notifyEmbyPaths([]string{localPath}, embyRefreshAdded)
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
// 三级策略，逐级降级：
//  1. 按路径定位到具体条目，只刷那一个（万级库不必整库扫）
//  2. 定位不到就刷该路径所属的媒体库条目
//  3. 路径不属于任何媒体库（路径映射配错了？）→ /Library/Media/Updated 报路径
func notifyEmbyPaths(localPaths []string, kind embyRefreshKind) {
	if len(localPaths) == 0 {
		return
	}
	cfg, ok := loadEmbyRefreshCfg()
	if !ok || !cfg.enabled() {
		return
	}

	targets := embyTargetPaths(localPaths, cfg, kind)
	if len(targets) == 0 {
		return
	}

	// 变更路径分桶到所属媒体库
	libs := embyMediaFolders(cfg)
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
			unmatched = append(unmatched, t.path)
		}
	}

	var refreshed []string
	for _, libID := range order {
		lib, paths := byID[libID], buckets[libID]
		// 少量变更精确到条目刷（目标在库之上时没这个选项，只能整库刷）
		if !wholeLib[libID] && len(paths) <= embyItemRefreshMax {
			var rest []string
			for _, t := range paths {
				id, name := embyResolveItem(cfg, t, lib.Locations)
				if id == "" || !embyRefreshItem(cfg, id) {
					rest = append(rest, t.path)
					continue
				}
				log.Printf("[Emby] ○ 已提交条目刷新（%s）：%s —— %s", kind.label(), name, t.path)
			}
			if len(rest) == 0 {
				refreshed = append(refreshed, lib.Name)
				continue
			}
			log.Printf("[Emby] ○ %d 个路径未定位到条目，改为刷新整个媒体库 %s", len(rest), lib.Name)
		}
		if embyRefreshItem(cfg, lib.ID) {
			refreshed = append(refreshed, lib.Name)
			log.Printf("[Emby] ○ 已提交媒体库刷新（%s）：%s", kind.label(), lib.Name)
			continue
		}
		log.Printf("[Emby] ✗ 媒体库刷新失败：%s（%s），回退路径通知", lib.Name, lib.ID)
		for _, t := range paths {
			unmatched = append(unmatched, t.path)
		}
	}

	if len(unmatched) > 0 {
		if len(libs) > 0 {
			log.Printf("[Emby] ○ %d 个变更路径未命中任何媒体库（%v），回退路径通知 —— 路径映射配对了吗",
				len(unmatched), unmatched)
		}
		embyReportUpdated(cfg, unmatched, kind.updateType())
	}

	// 配置了 Emby webhook 入库通知时，入库卡片（带海报）会随后到达，
	// 这里只记日志避免双重通知；未配置 webhook 时才发这条提示。
	// 删除场景不发：删除通知由 webhook 那条线负责，这里再发一条就是重复
	if kind != embyRefreshAdded || len(refreshed) == 0 {
		return
	}
	names := strings.Join(dedupeStrings(refreshed), "、")
	if embyWebhookConfigured() {
		log.Printf("[Emby] ○ 已提交刷新（%s）；webhook 入库通知已配置，跳过本条消息", names)
		return
	}
	go NotifyMessage("🎬 媒体入库", "已刷新媒体库："+names)
}

// embyTarget 一个刷新目标。
// ancestors 是「自己 + 逐级父目录」的 Emby 路径，定位条目时按序试：
// 刚落盘的新片目录 Emby 还没建条目，这时候命中的是它的父目录 —— 刷父目录
// 同样能让 Emby 发现新文件，比整库扫一遍便宜得多
type embyTarget struct {
	path      string   // 主目标（分桶与回退通知用它）
	ancestors []string // path 自己排第一，之后逐级向上，到本地媒体库根为止
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
	toEmby := func(local string) string {
		ep := mapLocalToEmbyPath(cfg.PathMapping, local)
		if cfg.Style == "windows" {
			ep = strings.ReplaceAll(ep, "/", "\\")
		}
		return ep
	}

	out := make([]embyTarget, 0, len(localPaths))
	seen := map[string]bool{}
	for _, p := range localPaths {
		if p == "" {
			continue
		}
		if kind == embyRefreshDeleted {
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
		if seen[t.path] {
			continue
		}
		seen[t.path] = true
		out = append(out, t)
	}
	return out
}

// embyResolveItem 沿祖先链找第一个 Emby 认识的条目，出了媒体库就停。
// 逐级上溯的做法取自 p115strmhelper 的 trigger_refresh_by_path
func embyResolveItem(cfg embyRefreshCfg, t embyTarget, locations []string) (id, name string) {
	for _, a := range t.ancestors {
		if !embyPathUnder(a, locations) {
			return "", "" // 再往上就出了这个媒体库，交给整库刷新
		}
		if id, name := embyItemIDByPath(cfg, a); id != "" {
			return id, name
		}
	}
	return "", ""
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
func embyItemIDByPath(cfg embyRefreshCfg, embyPath string) (id, name string) {
	q := url.Values{
		"Path":                   {embyPath},
		"Recursive":              {"true"},
		"Fields":                 {"Path"},
		"Limit":                  {"50"},
		"EnableTotalRecordCount": {"false"},
	}
	resp, err := embyRequest(http.MethodGet, cfg.ServerURL, cfg.APIKey, "/Items", q, nil)
	if err != nil {
		vlog("[Emby] 按路径查条目失败 %s: %v", embyPath, err)
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		vlog("[Emby] 按路径查条目 HTTP %d：%s", resp.StatusCode, embyPath)
		return "", ""
	}
	var out struct {
		Items []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
			Path string `json:"Path"`
		} `json:"Items"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return "", ""
	}
	want := strings.TrimRight(strings.ReplaceAll(embyPath, "\\", "/"), "/")
	for _, it := range out.Items {
		if strings.TrimRight(strings.ReplaceAll(it.Path, "\\", "/"), "/") == want {
			return it.ID, it.Name
		}
	}
	return "", ""
}

// embyRefreshItem POST /Items/{Id}/Refresh —— Emby 真正的条目/媒体库刷新端点。
//
// Recursive=true 会连带校验子项：文件已经不在的子条目在这一步被清掉，
// 「本地删了 Emby 里还挂着条目」就是靠它修的。
// MetadataRefreshMode 取 Default 而不是 FullRefresh：我们要的是「重新看一眼
// 文件还在不在」，不是把整库元数据重刮一遍（那会把 TMDB 打到限流）
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

// embyWebhookConfigured 是否配置了 Emby webhook 入库通知（emby-notify 卡）
func embyWebhookConfigured() bool {
	var cfg struct {
		Webhook string `json:"webhook"`
	}
	if json.Unmarshal([]byte(settingValueCompat("emby-notify")), &cfg) != nil {
		return false
	}
	return strings.TrimSpace(cfg.Webhook) != ""
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
