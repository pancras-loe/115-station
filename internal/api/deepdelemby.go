package api

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"115-station/internal/model"
)

// 参考 qmediasync internal/controllers/emby.go 的事件定位执行、p115strmhelper
// helper/mediasyncdel 的 deep.delete 定位方式，独立实现仅限本次事件的台账筛选。
// 原生 library.deleted 也可能来自扫库，仍须检查挂载、缺失复核与事件数量阈值。
func (h *Handler) deepDelOnEmbyDelete(payload map[string]interface{}, deep bool) {
	h.processDeepDelEvent(payload, deep, h.runDeepDelete, func() bool {
		select {
		case <-stopCh:
			return false
		case <-time.After(2 * time.Second):
			return true
		}
	})
}

// 注入执行和短暂复核等待，让事件范围、重复事件及恢复场景可用假执行器验证。
func (h *Handler) processDeepDelEvent(payload map[string]interface{}, deep bool,
	execute func([]model.SyncedFile, string) (deepDelResult, error), pause func() bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[深度删除] ✗ 事件处理异常: %v", r)
		}
	}()
	if !h.loadDeepDelCfg().Enabled {
		return
	}
	rels, pcs := h.deepDelLocators(payload, deep)
	if len(rels) == 0 && len(pcs) == 0 {
		log.Printf("[深度删除] ○ 事件缺少有效路径或 pickcode，跳过")
		return
	}
	// 等待同步完成，不能像旧 TryLock 路径一样丢弃事件；等锁不启动任何全库扫描。
	for !fullSyncMu.TryLock() {
		select {
		case <-stopCh:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	defer fullSyncMu.Unlock()
	cfg := h.loadDeepDelCfg()
	if !cfg.Enabled {
		return
	}
	rows, matched, ledger, err := h.deepDelEventRows(rels, pcs)
	if err != nil {
		h.rejectDeepDelEvent(err.Error())
		return
	}
	if matched == 0 {
		log.Printf("[深度删除] ○ 事件未命中同步台账，跳过")
		return
	}
	// Emby 可能先发 webhook 再删本地文件；只对本次事件做一次短暂复核。
	// 原生事件保留两次本地确认，不再等待分钟级的后台全库扫描。
	if !deep || len(rows) < matched {
		if !pause() {
			return
		}
		second, _, n, e := h.deepDelEventRows(rels, pcs)
		if e != nil {
			h.rejectDeepDelEvent(e.Error())
			return
		}
		ledger = n
		if !deep {
			// 只接受两次均缺失的条目；首次尚在的条目再给一次短暂落盘时间。
			if len(rows) < len(second) {
				rows = second
				if !pause() {
					return
				}
				second, _, ledger, e = h.deepDelEventRows(rels, pcs)
				if e != nil {
					h.rejectDeepDelEvent(e.Error())
					return
				}
			}
			before := map[uint]bool{}
			for _, r := range rows {
				before[r.ID] = true
			}
			rows = nil
			for _, r := range second {
				if before[r.ID] {
					rows = append(rows, r)
				}
			}
		} else {
			rows = second
		}
	}
	if len(rows) == 0 {
		log.Printf("[深度删除] ○ 事件命中 %d 条，但无通过本地缺失复核的文件，跳过", matched)
		return
	}
	cfg = h.loadDeepDelCfg()
	if !cfg.Enabled {
		return
	}
	videos, assets := countKinds(rows)
	if over, why := deepDelOverLimit(cfg, videos, videos+assets, ledger); over {
		h.rejectDeepDelEvent(why)
		return
	}
	log.Printf("[深度删除] ○ 处理本次 Emby 事件：视频 %d / 附属 %d", videos, assets)
	if _, err := execute(rows, "emby_webhook"); err != nil {
		log.Printf("[深度删除] ✗ 事件删除失败: %v", err)
	}
}

func (h *Handler) rejectDeepDelEvent(why string) {
	log.Printf("[深度删除] ✗ 事件删除已拦下: %s", why)
	h.noteDeepDelete("emby_webhook", deepDelResult{}, nil, nil, "rejected", why)
	if h.loadDeepDelCfg().notify() {
		go NotifyMessage("深度删除被拦下", why)
	}
}

// 只读台账校验库根；只对本次事件的路径执行文件检查，不消费旧 vanish_at 标记。
func (h *Handler) deepDelEventRows(rels, pcs []string) ([]model.SyncedFile, int, int, error) {
	root := h.orphanLocalRoot()
	if strings.TrimSpace(root) == "" {
		return nil, 0, 0, fmt.Errorf("未配置本地媒体目录")
	}
	if _, err := os.ReadDir(root); err != nil {
		return nil, 0, 0, fmt.Errorf("媒体库根不可访问: %w", err)
	}
	var ledger []model.SyncedFile
	if err := h.DB.Select("rel_path").Find(&ledger).Error; err != nil {
		return nil, 0, 0, err
	}
	if err := checkLibRoots(root, ledger); err != nil {
		return nil, 0, len(ledger), err
	}
	var matches []model.SyncedFile
	for _, rel := range rels {
		// 不接受库根或路径穿越，防止目录前缀匹配扩展成整库删除。
		if path.Clean(rel) != rel || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || !strings.Contains(rel, "/") {
			return nil, 0, len(ledger), fmt.Errorf("事件路径越界或指向库根: %s", rel)
		}
		var part []model.SyncedFile
		if err := h.DB.Where(`rel_path = ? OR rel_path LIKE ? ESCAPE '\'`, rel, likeEscape(rel)+"/%").Find(&part).Error; err != nil {
			return nil, 0, len(ledger), err
		}
		matches = append(matches, part...)
	}
	for _, pc := range pcs {
		if pc == "" {
			continue
		}
		var part []model.SyncedFile
		if err := h.DB.Where("pick_code = ?", pc).Find(&part).Error; err != nil {
			return nil, 0, len(ledger), err
		}
		matches = append(matches, part...)
	}
	seen := map[uint]bool{}
	var rows []model.SyncedFile
	matched := 0
	for _, r := range matches {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if r.OrphanAt != nil || r.RelPath == "" || r.FileID == "" {
			continue
		}
		matched++
		full := filepath.Join(root, filepath.FromSlash(r.RelPath))
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, matched, len(ledger), fmt.Errorf("台账路径越界: %s", r.RelPath)
		}
		if _, err := os.Stat(full); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return nil, matched, len(ledger), fmt.Errorf("本地文件不可访问: %w", err)
		}
		rows = append(rows, r)
	}
	return rows, matched, len(ledger), nil
}

// 神医事件除 Item.Path 外，还可能在 Description 中携带多版本路径和 pickcode。
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
