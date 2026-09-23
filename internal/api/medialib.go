package api

// ==================== 媒体库台账校准 ====================
//
// `MediaLibrary` 是「一部影视一条」的整理台账：整理成功时写入，
// 只有「重新整理」「删整理记录」「去重回查发现网盘目录已空」才会更新它。
// 手工删本地 STRM、在 Emby 里解除媒体库目录关联、直接上网盘删片，
// 这三种事都不会回头改它 —— 台账于是永远停在历史最高水位，
// 仪表盘拿它当媒体库规模就会比实际大一截。
//
// 校准 = **拿本地 STRM 树当事实**，把已经没有任何落盘产物的台账行删掉。
// 只读本地文件系统：不打 115、不打 Emby、不碰 SyncedFile 台账，更不删网盘内容。
// （台账行被删的唯一后果是这部片重新变成「可整理」——它本来就已经不在库里了。）

import (
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// mediaLibScanDepth 本地媒体树往下数几层。
// 正常形态是 根/库名/电影|电视剧/分类/标题目录 —— 从库名层算起 4 层足够，
// 再深就是剧集的季目录了，不该被当成「一部」
const mediaLibScanDepth = 4

// mediaLibTitleDir 从台账行的 TargetPath 推出「标题目录」的库内相对路径。
//
// TargetPath 由 recordMedia 写成「库内相对路径 + 最终文件名」（不含库名那一层），
// 所以去掉文件名就是落点目录。剧集的落点是季目录，还要再往上一层才是标题目录 ——
// 用户手工删的往往是某一季，标题目录还在，那就不该把整条台账判成失效。
func mediaLibTitleDir(targetPath string) string {
	p := strings.Trim(strings.ReplaceAll(strings.TrimSpace(targetPath), "\\", "/"), "/")
	if p == "" {
		return ""
	}
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	if seasonDirRe.MatchString(strings.TrimSpace(path.Base(d))) {
		if up := path.Dir(d); up != "." && up != "/" {
			d = up
		}
	}
	return d
}

// mediaLibRoots 本地媒体根下的第一层目录名（库名层）。
//
// 台账里的路径不含库名（库名是落盘时才从 115 目录名解析出来的），
// 校准时反过来要补上它：第一层目录本来就只有一两个，全试一遍比去猜可靠。
func mediaLibRoots(localRoot string) []string {
	ents, err := os.ReadDir(localRoot)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// mediaLibDirAlive 库内相对路径在本地是否还存在（任一库名下命中即算存在）
func mediaLibDirAlive(localRoot string, libs []string, rel string) bool {
	if rel == "" {
		return false
	}
	sub := filepath.FromSlash(rel)
	// 扁平结构（根下直接就是 电影/…）也要认，否则单库用户会被全判成失效
	if st, err := os.Stat(filepath.Join(localRoot, sub)); err == nil && st.IsDir() {
		return true
	}
	for _, lib := range libs {
		if st, err := os.Stat(filepath.Join(localRoot, lib, sub)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// localTitleCount 数一个目录下有多少「部」影视。
//
// 判据按「哪一层是标题目录」来：直接挂着 .strm 的目录算一部（电影目录、扁平剧集目录），
// 挂着季目录的也算一部（片名/Season 01/*.strm），两者都不是就当分类层继续往下数。
// 认定为一部之后**立刻停止下钻** —— 不然一部剧的每个季目录都会被再数一遍。
func localTitleCount(dir string, depth int) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var subs []string
	hasSeason := false
	for _, e := range ents {
		if !e.IsDir() {
			if strings.EqualFold(filepath.Ext(e.Name()), ".strm") {
				return 1
			}
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if seasonDirRe.MatchString(strings.TrimSpace(e.Name())) {
			hasSeason = true
		}
		subs = append(subs, filepath.Join(dir, e.Name()))
	}
	if hasSeason {
		return 1
	}
	if depth <= 0 {
		return 0
	}
	n := 0
	for _, s := range subs {
		n += localTitleCount(s, depth-1)
	}
	return n
}

// mediaLibLocalCounts 扫本地 STRM 树，按媒体库分类目录给出实际部数。
//
// 目录层级为根/库名/分类，按分类统计各 Emby 媒体库的内容，
// 这样这里数出来的名字能跟 Emby 媒体库卡片一一对上，差多少一眼就看出来。
func mediaLibLocalCounts(localRoot string) []gin.H {
	out := []gin.H{}
	for _, lib := range mediaLibRoots(localRoot) {
		libPath := filepath.Join(localRoot, lib)
		subs, err := os.ReadDir(libPath)
		if err != nil {
			continue
		}
		hasSub := false
		for _, s := range subs {
			if !s.IsDir() || strings.HasPrefix(s.Name(), ".") {
				continue
			}
			hasSub = true
			out = append(out, gin.H{
				"name":  s.Name(),
				"count": localTitleCount(filepath.Join(libPath, s.Name()), mediaLibScanDepth),
			})
		}
		if !hasSub {
			out = append(out, gin.H{"name": lib, "count": localTitleCount(libPath, mediaLibScanDepth)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ci, _ := out[i]["count"].(int)
		cj, _ := out[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		ni, _ := out[i]["name"].(string)
		nj, _ := out[j]["name"].(string)
		return ni < nj
	})
	return out
}

// mediaLibVerdict 一行台账的校准结论
type mediaLibVerdict struct {
	Row   model.MediaLibrary
	Alive bool
	// Skip 为真表示「无法判断」（台账里压根没记落点），一律保留
	Skip bool
}

// auditMediaLibrary 逐行比对台账与本地 STRM 树
func auditMediaLibrary(rows []model.MediaLibrary, localRoot string, libs []string) []mediaLibVerdict {
	out := make([]mediaLibVerdict, 0, len(rows))
	for _, r := range rows {
		rel := mediaLibTitleDir(r.TargetPath)
		if rel == "" {
			out = append(out, mediaLibVerdict{Row: r, Skip: true})
			continue
		}
		out = append(out, mediaLibVerdict{Row: r, Alive: mediaLibDirAlive(localRoot, libs, rel)})
	}
	return out
}

// CalibrateMediaLibrary 媒体库台账校准
// POST /media-library/calibrate   body: {"apply": bool}
//
// apply=false（默认）只预演，返回将被清除的条目；apply=true 才真的删台账行。
func (h *Handler) CalibrateMediaLibrary(c *gin.Context) {
	var req struct {
		Apply bool `json:"apply"`
	}
	_ = c.ShouldBindJSON(&req)

	localRoot := localMediaRoot()
	libs := mediaLibRoots(localRoot)
	if _, err := os.Stat(localRoot); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "本地媒体库目录不可访问：" + localRoot + "（先到「Strm 管理 → 配置」确认本地目录）",
		})
		return
	}

	var rows []model.MediaLibrary
	if err := h.DB.Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取台账失败：" + err.Error()})
		return
	}

	verdicts := auditMediaLibrary(rows, localRoot, libs)
	var stale []mediaLibVerdict
	kept, skipped := 0, 0
	for _, v := range verdicts {
		switch {
		case v.Skip:
			skipped++
		case v.Alive:
			kept++
		default:
			stale = append(stale, v)
		}
	}

	// 全军覆没多半是本地目录配错/没挂上，而不是库真被清空了。
	// 这种时候删台账等于把去重信息全丢掉，下次整理会把整库重做一遍
	if len(stale) > 0 && kept == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "本地媒体库目录下找不到任何一条台账对应的目录（" + localRoot +
				"），多半是本地目录配置变了或挂载没上来；先确认路径再校准",
		})
		return
	}

	sample := make([]gin.H, 0, 20)
	for _, v := range stale {
		if len(sample) >= 20 {
			break
		}
		sample = append(sample, gin.H{
			"title": v.Row.Title, "year": v.Row.Year,
			"type": v.Row.MediaType, "category": v.Row.Category,
			"target_path": v.Row.TargetPath,
		})
	}

	removed := 0
	if req.Apply && len(stale) > 0 {
		ids := make([]uint, 0, len(stale))
		for _, v := range stale {
			ids = append(ids, v.Row.ID)
		}
		// 分批：sqlite 单条语句的参数个数有上限，万级库一次 IN 会炸（同 chunkIDs 的理由）
		for _, batch := range chunkIDs(ids, 400) {
			res := h.DB.Where("id IN ?", batch).Delete(&model.MediaLibrary{})
			if res.Error != nil {
				log.Printf("[仪表盘] ✗ 台账校准删除失败: %v", res.Error)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "清除失败：" + res.Error.Error()})
				return
			}
			removed += int(res.RowsAffected)
		}
		log.Printf("[仪表盘] ✓ 媒体库台账校准：共 %d 条，清除 %d 条失效、保留 %d 条、跳过 %d 条（本地根 %s）",
			len(rows), removed, kept, skipped, localRoot)
	} else {
		log.Printf("[仪表盘] ○ 媒体库台账校准预演：共 %d 条，%d 条本地已不存在、保留 %d 条、跳过 %d 条",
			len(rows), len(stale), kept, skipped)
	}

	c.JSON(http.StatusOK, gin.H{
		"applied":    req.Apply,
		"local_root": localRoot,
		"total":      len(rows),
		"stale":      len(stale),
		"kept":       kept,
		"skipped":    skipped,
		"removed":    removed,
		"sample":     sample,
		"libraries":  mediaLibLocalCounts(localRoot),
	})
}
