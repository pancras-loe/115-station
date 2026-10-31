package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ==================== 批量在 Emby 里建媒体库 ====================
//
// 2026-10-27 起（维护者要求恢复；2026-09-23 删掉的那版扫本地媒体根第二层、按目录名猜类型，
// 和媒体库表 / 分类方案对不上）。候选一律从本站的媒体库表算，不扫目录猜：
//   - 选了分类方案的库：一个分类目录一个 Emby 库（「电影/华语电影」→「华语电影」），类型取规则的 media_type；
//     几个库里同一个分类目录（多账号放在同一套分类下）合成一个 Emby 库、带几个路径；
//   - 不分类、片目在第 1 层：整个库目录一个 Emby 库；第 2 层以下：本地的每个子目录一个（同覆盖检查口径）。
//     这两种没有规则可读，类型按名字猜个默认，用户可改。
//
// 跟 Emby 现有的库对一遍（路径映射与入库刷新、深删同一套）：已被覆盖的路径跳过；Emby 库的路径在它下面的不建
// （建了同一部片入两次库）；同名库已经有了就给它加路径，不另建同名库。
//
// 库选项按本站库的刮削方式定（规则是 checkEmbyLib 反过来用，建完拿它自检）：本站刮的关掉 Emby 的元数据 / 图像下载器、
// 不让 Emby 写 NFO（它会整份重写本站写的）；交给 Emby 刮的开 TheMovieDb。两种都开 Nfo 读取器、关掉人物的 TMDB 下载器
// （交给演职人员补全）。**章节图 / 片头检测一律关**：对 STRM 每一项都是 Emby 去取一次 115 直链（§6.16 同一个顾虑）。
// 实时监控默认关：本站入库后自己通知 Emby 刷新，开着等于再扫一遍。
//
// 下载器名字不写死：先读 /Libraries/AvailableOptions（Emby 网页新建库时也读它），按它给的类型与下载器填。
// 请求形态照 Emby 网页的 apiclient：建库是 query 带 name / collectionType / refreshLibrary、body 只放 {LibraryOptions}（路径在 PathInfos 里）；
// 建完按 Emby 网页的保存流程再提交一次 /Library/VirtualFolders/LibraryOptions 固化选项（老版本有的字段建库时不认）。
// **还没用真的 Emby 4.10 跑过**：第一次看日志 [Emby建库]，建完那一步自检会把没落上的路径 / 选项写进结果。
//
// 只调 Emby、零 115 请求，不进任务队列、不拿 taskMu（几个库几秒钟的事）。

// 候选的类型（Emby 的 CollectionType；mixed 建库时不带 collectionType = 混合内容）
const (
	embyLibMovies = "movies"
	embyLibTV     = "tvshows"
	embyLibMixed  = "mixed"
)

// 一个候选怎么处理
const (
	embyLibCreate = "create" // 新建
	embyLibAdd    = "add"    // 给 Emby 里同名的库加路径
	embyLibSkip   = "skip"   // 不动（Why 说明）
)

// 一个路径的状态
const (
	embyPathNew      = "new"
	embyPathCovered  = "covered"  // Emby 里已经有库包含它
	embyPathConflict = "conflict" // Emby 有库的路径在它下面：建了会重复入库
	embyPathUnmapped = "unmapped" // 路径映射换算不出
)

// embyLibDir 一个本站库里要进 Emby 的一个目录（gather 的产物，plan 的输入）
type embyLibDir struct {
	Key     string // 合并用：分类目录 = "cat:" + 分类相对路径（几个库同一个分类合成一个）；其余每个库自己的
	Name    string // 默认的 Emby 库名
	Type    string // movies / tvshows / mixed
	Fixed   bool   // 类型来自分类规则（界面不让改）
	LibName string // 本站库名
	Local   string // 本地绝对路径（/ 分隔）
	Scrape  string // 本站库的刮削方式
}

// embyLibPath 候选的一个路径
type embyLibPath struct {
	Local string `json:"local"`
	Emby  string `json:"emby"`
	Lib   string `json:"lib"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// embyLibCand 一个候选 Emby 库
type embyLibCand struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Fixed bool   `json:"type_fixed"`
	// Scrape 这个 Emby 库按哪种方式设选项：off = 交给 Emby 刮，station = 本站刮（auto / manual 一样）
	Scrape string        `json:"scrape"`
	Libs   []string      `json:"libs"`
	Paths  []embyLibPath `json:"paths"`
	Action string        `json:"action"`
	// Target add 时 Emby 里那个库的名字与 ItemId
	Target   string `json:"target,omitempty"`
	TargetID string `json:"-"`
	Why      string `json:"why,omitempty"`
	// Note 不挡执行的提醒（几个本站库刮削方式不同之类）
	Note string `json:"note,omitempty"`
}

// embyLibOverride 用户在预览里改的（按 key）
type embyLibOverride struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

const (
	embyScrapeStation = "station"
	embyScrapeOff     = "off"
)

// guessEmbyLibType 不分类的库没有规则可读：按名字猜个默认（用户可改），认不出算混合
func guessEmbyLibType(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "电影") || strings.Contains(n, "movie") || strings.Contains(n, "film"):
		return embyLibMovies
	case strings.Contains(n, "剧") || strings.Contains(n, "番") || strings.Contains(n, "动漫") || strings.Contains(n, "综艺") ||
		strings.Contains(n, "tv") || strings.Contains(n, "show") || strings.Contains(n, "anime"):
		return embyLibTV
	}
	return embyLibMixed
}

// embyTypeOfMedia 分类规则的 media_type → Emby 类型
func embyTypeOfMedia(mt string) string {
	if mt == "tv" {
		return embyLibTV
	}
	return embyLibMovies
}

// gatherEmbyLibDirs 从媒体库表算出要进 Emby 的目录。零 115 请求；不分类、第 2 层以下的库读一次本地子目录。
// warnings 是认不出的库（方案被删了）之类，界面列出来
func gatherEmbyLibDirs() (dirs []embyLibDir, warnings []string) {
	root := strings.TrimRight(strings.ReplaceAll(mediaRoot(), `\`, "/"), "/")
	legacy := ""
	for _, l := range loadLibraries() {
		local := normLibLocal(l.Local)
		if local == "" {
			continue
		}
		abs := root + "/" + local
		mode := l.Scrape
		if mode == "" {
			if legacy == "" {
				legacy = loadLegacyLibScrape()
			}
			mode = legacy
		}
		base := embyLibDir{LibName: l.Name, Scrape: mode}
		switch {
		case l.SchemeID != 0:
			if _, ok := schemeByID(l.SchemeID); !ok {
				warnings = append(warnings, fmt.Sprintf("「%s」选的分类方案已经删除了，先给它重新选一套", l.Name))
				continue
			}
			for _, mt := range []string{"movie", "tv"} {
				rules := classifyRulesOf(l.SchemeID, mt)
				rels := []string{}
				for _, r := range rules {
					rels = append(rels, categoryDir(mt, r.Name))
				}
				if len(rules) == 0 {
					// 这个媒体类型一条规则都没有：整理落进「电影/未分类」（classifyTargetIn 最后一档）
					rels = append(rels, libSubPath(mediaTypeCategory(mt), "未分类"))
				}
				for _, rel := range rels {
					d := base
					d.Key, d.Name, d.Type, d.Fixed = "cat:"+rel, path.Base(rel), embyTypeOfMedia(mt), true
					d.Local = abs + "/" + rel
					dirs = append(dirs, d)
				}
			}
		case l.Depth >= 2:
			subs := localSubdirs(filepath.FromSlash(abs))
			if len(subs) == 0 {
				warnings = append(warnings, fmt.Sprintf("「%s」不分类、片目在第 %d 层，但本地 %s 下还没有子目录：跑过全量同步再来", l.Name, l.Depth, abs))
			}
			for _, s := range subs {
				d := base
				d.Key, d.Name, d.Type = fmt.Sprintf("lib:%d/%s", l.ID, s), s, guessEmbyLibType(s)
				d.Local = abs + "/" + s
				dirs = append(dirs, d)
			}
		default:
			d := base
			d.Key, d.Name, d.Type, d.Local = fmt.Sprintf("lib:%d", l.ID), l.Name, guessEmbyLibType(l.Name), abs
			dirs = append(dirs, d)
		}
	}
	sd, sw := shareStrmEmbyDirs(root)
	return append(dirs, sd...), append(warnings, sw...)
}

// shareStrmEmbyDirs 分享 STRM（sharestrm.go）要进 Emby 的目录：本地整理的分享按分类目录放，
// 和库的分类目录同一个 key（"cat:"+分类）——同名分类合成一个 Emby 库，分享进来的片和库里的片在一起；
// 只列本地已经有的分类目录（还没整理进任何一部的不建空库）。原样放的分享各在自己的目录里，
// 合不成一个库，只提示用户自己加路径
func shareStrmEmbyDirs(root string) (dirs []embyLibDir, warnings []string) {
	if model.DB == nil {
		return nil, nil
	}
	var srcs []model.ShareSource
	model.DB.Select("layout").Find(&srcs)
	if len(srcs) == 0 {
		return nil, nil
	}
	cfg := loadShareStrmCfg()
	if shareRootProblem(cfg.Root, loadLibraries()) != "" {
		return nil, nil
	}
	shareAbs := root + "/" + cfg.Root
	scrape := embyScrapeOff
	if cfg.Scrape {
		scrape = "auto"
	}
	keep := 0
	for _, s := range srcs {
		if normShareLayout(s.Layout) != shareLayoutOrganize {
			keep++
		}
	}
	if keep < len(srcs) {
		for _, mt := range []string{"movie", "tv"} {
			var rels []string
			if cfg.SchemeID == 0 {
				rels = []string{mediaTypeCategory(mt)}
			} else {
				for _, r := range classifyRulesOf(cfg.SchemeID, mt) {
					rels = append(rels, categoryDir(mt, r.Name))
				}
				rels = append(rels, libSubPath(mediaTypeCategory(mt), "未分类"))
			}
			for _, rel := range rels {
				local := shareAbs + "/" + rel
				if st, err := os.Stat(filepath.FromSlash(local)); err != nil || !st.IsDir() {
					continue
				}
				dirs = append(dirs, embyLibDir{Key: "cat:" + rel, Name: path.Base(rel), Type: embyTypeOfMedia(mt), Fixed: true,
					LibName: "分享 STRM", Local: local, Scrape: scrape})
			}
		}
	}
	if keep > 0 {
		warnings = append(warnings, fmt.Sprintf("有 %d 个分享是「原样」放的，各在 %s 下自己的目录里，合不进一个 Emby 库：想进 Emby 就把它们改成「本地整理」，或者自己在 Emby 里加路径", keep, shareAbs))
	}
	return dirs, warnings
}

// embyTypesCompatible 加路径时类型对不对得上：一样，或者有一边是混合
func embyTypesCompatible(cand, existing string) bool {
	existing = strings.ToLower(existing)
	return cand == embyLibMixed || existing == "" || existing == embyLibMixed || existing == cand
}

// planEmbyLibs 纯函数：本站的目录 + Emby 现有的库 + 用户改的 → 候选。
// 先按 key 合并（同一个分类合成一个库），再套用户改的名字 / 类型，再逐个路径对 Emby，最后定动作、去重名
func planEmbyLibs(dirs []embyLibDir, folders []embyVirtualFolder, pathMapping string, overrides []embyLibOverride) []embyLibCand {
	ov := map[string]embyLibOverride{}
	for _, o := range overrides {
		ov[o.Key] = o
	}
	var cands []*embyLibCand
	byKey := map[string]*embyLibCand{}
	modes := map[string]map[string]bool{}
	for _, d := range dirs {
		c := byKey[d.Key]
		if c == nil {
			c = &embyLibCand{Key: d.Key, Name: d.Name, Type: d.Type, Fixed: d.Fixed}
			byKey[d.Key] = c
			cands = append(cands, c)
			modes[d.Key] = map[string]bool{}
		} else if c.Type != d.Type {
			// 同一个分类目录在电影、剧集两边都有规则（「动漫」两边都写了）：只能建混合库
			c.Type, c.Fixed = embyLibMixed, false
		}
		modes[d.Key][d.Scrape] = true
		if !slices.Contains(c.Libs, d.LibName) {
			c.Libs = append(c.Libs, d.LibName)
		}
		dup := false
		for _, p := range c.Paths {
			dup = dup || p.Local == d.Local
		}
		if !dup {
			c.Paths = append(c.Paths, embyLibPath{Local: d.Local, Lib: d.LibName})
		}
	}

	// Emby 现有库的路径换回本地，覆盖判定用
	type loc struct{ local, lib string }
	var locs []loc
	for _, f := range folders {
		for _, l := range f.Locations {
			if local := strings.TrimRight(filepath.ToSlash(embyPathToLocal(pathMapping, l)), "/"); local != "" {
				locs = append(locs, loc{local, f.Name})
			}
		}
	}
	folderByName := map[string]embyVirtualFolder{}
	for _, f := range folders {
		folderByName[strings.ToLower(strings.TrimSpace(f.Name))] = f
	}

	for _, c := range cands {
		if o, ok := ov[c.Key]; ok {
			if n := strings.TrimSpace(o.Name); n != "" {
				c.Name = n
			}
			if !c.Fixed && (o.Type == embyLibMovies || o.Type == embyLibTV || o.Type == embyLibMixed) {
				c.Type = o.Type
			}
		}
		m := modes[c.Key]
		c.Scrape = embyScrapeStation
		if len(m) == 1 && m[libScrapeOff] {
			c.Scrape = embyScrapeOff
		} else if m[libScrapeOff] {
			c.Note = "这几个本站库有的刮、有的不刮，按本站刮削设 Emby 选项（Emby 不自己刮）"
		}
		nNew := 0
		for i := range c.Paths {
			p := &c.Paths[i]
			e, ok := localToEmbyPath(pathMapping, p.Local)
			if !ok {
				p.State, p.Note = embyPathUnmapped, "路径映射换算不出它在 Emby 里的路径（设置 → Emby → 路径映射）"
				continue
			}
			p.Emby, p.State = e, embyPathNew
			for _, l := range locs {
				switch {
				case l.local == p.Local || strings.HasPrefix(p.Local, l.local+"/"):
					p.State, p.Note = embyPathCovered, fmt.Sprintf("已在 Emby 库「%s」里", l.lib)
				case strings.HasPrefix(l.local, p.Local+"/") && p.State == embyPathNew:
					p.State, p.Note = embyPathConflict, fmt.Sprintf("Emby 库「%s」已经包含它下面的 %s，再建会重复入库", l.lib, l.local[len(p.Local)+1:])
				}
				if p.State == embyPathCovered {
					break
				}
			}
			if p.State == embyPathNew {
				nNew++
			}
		}
		switch {
		case nNew == 0 && allPathsCovered(c.Paths):
			c.Action, c.Why = embyLibSkip, "已经都在 Emby 里"
		case nNew == 0:
			c.Action, c.Why = embyLibSkip, "没有能加进 Emby 的路径（见各路径的说明）"
		default:
			c.Action = embyLibCreate
			if f, ok := folderByName[strings.ToLower(c.Name)]; ok {
				if embyTypesCompatible(c.Type, f.CollectionType) {
					c.Action, c.Target, c.TargetID = embyLibAdd, f.Name, f.ItemID
				} else {
					c.Action = embyLibSkip
					c.Why = fmt.Sprintf("Emby 里已有同名库「%s」但类型是 %s：改个库名再建", f.Name, embyTypeLabel(f.CollectionType))
				}
			}
		}
	}

	// 要新建的库互相重名（「电影/动画」「剧集/动画」都叫动画）：后面的加上上级目录名，还重就加序号。
	// 和 Emby 现有库重名的已经在上面变成「加路径」或跳过了，这里只管新建的之间
	taken := map[string]bool{}
	for _, f := range folders {
		taken[strings.ToLower(strings.TrimSpace(f.Name))] = true
	}
	for _, c := range cands {
		if c.Action != embyLibCreate {
			continue
		}
		name := c.Name
		if taken[strings.ToLower(name)] {
			if parent := embyCandParent(c); parent != "" {
				name = fmt.Sprintf("%s（%s）", c.Name, parent)
			}
			for i := 2; taken[strings.ToLower(name)]; i++ {
				name = fmt.Sprintf("%s %d", c.Name, i)
			}
			c.Note = joinNote(c.Note, fmt.Sprintf("「%s」重名，改叫「%s」", c.Name, name))
			c.Name = name
		}
		taken[strings.ToLower(name)] = true
	}

	out := make([]embyLibCand, 0, len(cands))
	for _, c := range cands {
		out = append(out, *c)
	}
	return out
}

// embyCandParent 重名时拿来区分的上级目录名：分类候选取分类路径的上一段（电影 / 剧集），其余取本站库名
func embyCandParent(c *embyLibCand) string {
	if rel, ok := strings.CutPrefix(c.Key, "cat:"); ok {
		if d := path.Dir(rel); d != "." && d != "/" {
			return path.Base(d)
		}
	}
	if len(c.Libs) > 0 {
		return c.Libs[0]
	}
	return ""
}

func allPathsCovered(ps []embyLibPath) bool {
	for _, p := range ps {
		if p.State != embyPathCovered {
			return false
		}
	}
	return len(ps) > 0
}

func embyTypeLabel(t string) string {
	switch strings.ToLower(t) {
	case embyLibMovies:
		return "电影"
	case embyLibTV:
		return "剧集"
	case "", embyLibMixed:
		return "混合内容"
	}
	return t
}

// ---- 库选项 ----

// embyOptInfo AvailableOptions 里一个下载器 / 读取器 / 保存器
type embyOptInfo struct {
	Name           string `json:"Name"`
	DefaultEnabled bool   `json:"DefaultEnabled"`
}

// embyAvailOpts GET /Libraries/AvailableOptions（只取用得到的；DefaultLibraryOptions 原样保留，未知字段照 Emby 的默认）
type embyAvailOpts struct {
	MetadataSavers []embyOptInfo `json:"MetadataSavers"`
	TypeOptions    []struct {
		Type                string          `json:"Type"`
		MetadataFetchers    []embyOptInfo   `json:"MetadataFetchers"`
		ImageFetchers       []embyOptInfo   `json:"ImageFetchers"`
		DefaultImageOptions json.RawMessage `json:"DefaultImageOptions"`
	} `json:"TypeOptions"`
	DefaultLibraryOptions map[string]any `json:"DefaultLibraryOptions"`
}

// embyLibOptsReq 用户在弹窗里选的
type embyLibOptsReq struct {
	Realtime bool `json:"realtime"`
	SaveNfo  bool `json:"save_nfo"` // 只对交给 Emby 刮的库：Emby 把刮来的写进媒体文件夹
}

// embyFallbackTypes 读不到 AvailableOptions 时按这几个类型填（本站刮的要显式写空，不写 Emby 会按它的默认开下载器）
var embyFallbackTypes = map[string][]string{
	embyLibMovies: {"Movie", "Person"},
	embyLibTV:     {"Series", "Season", "Episode", "Person"},
	embyLibMixed:  {"Movie", "Series", "Season", "Episode", "Person"},
}

// buildEmbyLibOptions 纯函数：新库的 LibraryOptions
func buildEmbyLibOptions(avail *embyAvailOpts, typ, scrape string, paths []string, o embyLibOptsReq) map[string]any {
	opts := map[string]any{}
	if avail != nil {
		for k, v := range avail.DefaultLibraryOptions {
			opts[k] = v
		}
	}
	off := scrape == embyScrapeOff
	opts["EnableRealtimeMonitor"] = o.Realtime
	// 下面几项对 STRM 都要 Emby 去取 115 直链（读视频内容），一律关
	opts["EnableChapterImageExtraction"] = false
	opts["ExtractChapterImagesDuringLibraryScan"] = false
	opts["EnableMarkerDetection"] = false
	opts["EnableMarkerDetectionDuringLibraryScan"] = false
	// Jellyfin 的缩略进度图（Emby 没有这两项，多给的字段它不认）：同样要读整部片
	opts["EnableTrickplayImageExtraction"] = false
	opts["ExtractTrickplayImagesDuringLibraryScan"] = false
	opts["SaveLocalThumbnailSets"] = false
	opts["DownloadImagesInAdvance"] = false
	opts["EnableMultiVersionByFiles"] = true
	opts["PreferredMetadataLanguage"] = "zh-CN"
	opts["PreferredImageLanguage"] = "zh-CN"
	opts["MetadataCountryCode"] = "CN"
	saveNfo := off && o.SaveNfo
	opts["SaveLocalMetadata"] = saveNfo
	savers := []string{}
	if saveNfo {
		if avail != nil {
			for _, s := range avail.MetadataSavers {
				if strings.EqualFold(s.Name, "Nfo") {
					savers = append(savers, s.Name)
				}
			}
		} else {
			savers = append(savers, "Nfo")
		}
	}
	opts["MetadataSavers"] = savers
	// 一定要读本站写的 NFO（交给 Emby 刮的库也读：用户自己放的 NFO 优先）
	var disabled []string
	if raw, ok := opts["DisabledLocalMetadataReaders"].([]any); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok && !strings.EqualFold(s, "Nfo") {
				disabled = append(disabled, s)
			}
		}
	}
	if disabled == nil {
		disabled = []string{}
	}
	opts["DisabledLocalMetadataReaders"] = disabled
	pis := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		pis = append(pis, map[string]any{"Path": p})
	}
	opts["PathInfos"] = pis

	enabledOf := func(list []embyOptInfo, person bool) (on, order []string) {
		on, order = []string{}, []string{}
		for _, f := range list {
			order = append(order, f.Name)
		}
		if person || !off {
			return on, order
		}
		for _, f := range list {
			if f.DefaultEnabled {
				on = append(on, f.Name)
			}
		}
		if len(on) == 0 {
			for _, f := range list {
				if strings.EqualFold(f.Name, "TheMovieDb") {
					on = append(on, f.Name)
				}
			}
		}
		return on, order
	}
	var types []map[string]any
	if avail != nil && len(avail.TypeOptions) > 0 {
		for _, t := range avail.TypeOptions {
			person := t.Type == "Person"
			meta, metaOrder := enabledOf(t.MetadataFetchers, person)
			img, imgOrder := enabledOf(t.ImageFetchers, person)
			to := map[string]any{"Type": t.Type, "MetadataFetchers": meta, "MetadataFetcherOrder": metaOrder,
				"ImageFetchers": img, "ImageFetcherOrder": imgOrder}
			if len(t.DefaultImageOptions) > 0 && string(t.DefaultImageOptions) != "null" {
				to["ImageOptions"] = t.DefaultImageOptions
			}
			types = append(types, to)
		}
	} else {
		for _, t := range embyFallbackTypes[typ] {
			on := []string{}
			if off && t != "Person" {
				on = []string{"TheMovieDb"}
			}
			types = append(types, map[string]any{"Type": t, "MetadataFetchers": on, "ImageFetchers": on})
		}
	}
	opts["TypeOptions"] = types
	return opts
}

// ---- 调 Emby ----

// embyLibClient 建库要发的几种请求；测试换成假 Emby 的地址即可（同一个 embyRequest）
type embyLibClient struct{ cfg embyRefreshCfg }

func (e embyLibClient) do(method, p string, q url.Values, body any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	resp, err := embyRequest(method, e.cfg.ServerURL, e.cfg.APIKey, p, q, b)
	if err != nil {
		return fmt.Errorf("连不上 Emby：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("HTTP %d：建库要管理员的 API 密钥", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (e embyLibClient) availOptions(typ string) (*embyAvailOpts, error) {
	q := url.Values{"IsNewLibrary": {"true"}}
	if typ != embyLibMixed {
		q.Set("LibraryContentType", typ)
	}
	resp, err := embyRequest(http.MethodGet, e.cfg.ServerURL, e.cfg.APIKey, "/Libraries/AvailableOptions", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out embyAvailOpts
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// embyLibResult 一个候选执行的结果
type embyLibResult struct {
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Action string   `json:"action"`
	OK     bool     `json:"ok"`
	Error  string   `json:"error,omitempty"`
	Warns  []string `json:"warns,omitempty"`
	Paths  []string `json:"paths"`
}

// applyEmbyLib 执行一个候选：建库 / 加路径 → 固化选项 → 读回来自检
func applyEmbyLib(cl embyLibClient, c embyLibCand, avail *embyAvailOpts, o embyLibOptsReq) embyLibResult {
	r := embyLibResult{Key: c.Key, Name: c.Name, Action: c.Action, Paths: []string{}}
	var paths []string
	for _, p := range c.Paths {
		if p.State != embyPathNew {
			continue
		}
		// 分类目录还没有片时本地可能还不存在：Emby 加不存在的路径会报错，先建出空目录（只在媒体根下，与写 STRM 同一个地方）
		if err := os.MkdirAll(filepath.FromSlash(p.Local), 0o755); err != nil {
			r.Warns = append(r.Warns, fmt.Sprintf("本地建不出 %s：%v", p.Local, err))
			continue
		}
		paths = append(paths, p.Emby)
	}
	r.Paths = paths
	if len(paths) == 0 {
		r.Error = "没有能加进 Emby 的路径"
		return r
	}
	opts := buildEmbyLibOptions(avail, c.Type, c.Scrape, paths, o)
	target := c.Name

	switch c.Action {
	case embyLibCreate:
		q := url.Values{"name": {c.Name}, "refreshLibrary": {"false"}}
		if c.Type != embyLibMixed {
			q.Set("collectionType", c.Type)
		}
		if err := cl.do(http.MethodPost, "/Library/VirtualFolders", q, map[string]any{"LibraryOptions": opts}); err != nil {
			r.Error = "建库失败：" + err.Error()
			return r
		}
	case embyLibAdd:
		target = c.Target
		for _, p := range paths {
			body := map[string]any{"Name": c.Target, "Id": c.TargetID, "Path": p, "PathInfo": map[string]any{"Path": p}}
			if err := cl.do(http.MethodPost, "/Library/VirtualFolders/Paths", url.Values{"refreshLibrary": {"false"}}, body); err != nil {
				r.Error = fmt.Sprintf("给「%s」加路径 %s 失败：%v", c.Target, p, err)
				return r
			}
		}
		// 加路径不改现有库的选项：那是用户自己调过的库
		r.OK = true
		if f, ok := findEmbyFolder(cl.cfg, target); ok {
			r.Warns = append(r.Warns, missingLocations(f, paths)...)
		}
		return r
	}

	f, ok := findEmbyFolder(cl.cfg, target)
	if !ok {
		r.Error = "建库请求成功了，但读回来的媒体库列表里找不到「" + target + "」"
		return r
	}
	// 老版本建库时只认 query 里的 paths：PathInfos 没落上就逐个补
	for _, p := range paths {
		if !folderHasLocation(f, p) {
			body := map[string]any{"Name": target, "Id": f.ItemID, "Path": p, "PathInfo": map[string]any{"Path": p}}
			if err := cl.do(http.MethodPost, "/Library/VirtualFolders/Paths", url.Values{"refreshLibrary": {"false"}}, body); err != nil {
				r.Warns = append(r.Warns, fmt.Sprintf("补加路径 %s 失败：%v", p, err))
			}
		}
	}
	if f.ItemID != "" {
		if err := cl.do(http.MethodPost, "/Library/VirtualFolders/LibraryOptions", url.Values{"id": {f.ItemID}},
			map[string]any{"Id": f.ItemID, "LibraryOptions": opts}); err != nil {
			r.Warns = append(r.Warns, "库已建好，固化选项失败："+err.Error())
		}
	} else {
		r.Warns = append(r.Warns, "Emby 没给这个库的 ItemId，选项没法再固化一次")
	}
	if f, ok = findEmbyFolder(cl.cfg, target); ok {
		r.Warns = append(r.Warns, missingLocations(f, paths)...)
		r.Warns = append(r.Warns, embyLibSelfCheck(f, c.Scrape, o)...)
	}
	r.OK = true
	return r
}

// embyLibSelfCheck 建完读回来的选项和想要的对不对得上（checkEmbyLib 同一套判定）
func embyLibSelfCheck(f embyVirtualFolder, scrape string, o embyLibOptsReq) []string {
	var out []string
	chk := checkEmbyLib(f)
	if scrape == embyScrapeOff {
		out = append(out, chk.EmbyIssues...)
		if !chk.NfoReader {
			out = append(out, "Nfo 元数据读取器没开")
		}
	} else {
		out = append(out, chk.StationIssues...)
	}
	if f.LibraryOptions.EnableRealtimeMonitor != o.Realtime {
		out = append(out, fmt.Sprintf("实时监控没设上（现在是%s）", map[bool]string{true: "开", false: "关"}[f.LibraryOptions.EnableRealtimeMonitor]))
	}
	if f.LibraryOptions.EnableChapterImageExtraction {
		out = append(out, "章节图提取还开着：Emby 会去取 115 直链，到 Emby 里关掉")
	}
	return out
}

func folderHasLocation(f embyVirtualFolder, p string) bool {
	p = strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	for _, l := range f.Locations {
		if strings.TrimRight(strings.ReplaceAll(l, `\`, "/"), "/") == p {
			return true
		}
	}
	return false
}

func missingLocations(f embyVirtualFolder, paths []string) []string {
	var out []string
	for _, p := range paths {
		if !folderHasLocation(f, p) {
			out = append(out, "读回来的库里没有路径 "+p+"（到 Emby 里看一下）")
		}
	}
	return out
}

func findEmbyFolder(cfg embyRefreshCfg, name string) (embyVirtualFolder, bool) {
	folders, err := embyVirtualFolders(cfg)
	if err != nil {
		return embyVirtualFolder{}, false
	}
	for _, f := range folders {
		if strings.EqualFold(strings.TrimSpace(f.Name), strings.TrimSpace(name)) {
			return f, true
		}
	}
	return embyVirtualFolder{}, false
}

// runEmbyLibCreate 按选中的 key 执行。plan 是这次现算的（不拿预览时的结果）
func runEmbyLibCreate(cfg embyRefreshCfg, plan []embyLibCand, keys []string, o embyLibOptsReq, refresh bool) []embyLibResult {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	cl := embyLibClient{cfg}
	avail := map[string]*embyAvailOpts{}
	var out []embyLibResult
	changed := false
	for _, c := range plan {
		if !want[c.Key] {
			continue
		}
		if c.Action == embyLibSkip {
			out = append(out, embyLibResult{Key: c.Key, Name: c.Name, Action: c.Action, Error: c.Why, Paths: []string{}})
			continue
		}
		a, seen := avail[c.Type]
		if !seen {
			var err error
			if a, err = cl.availOptions(c.Type); err != nil {
				log.Printf("[Emby建库] ○ 读不到 Emby 的可选下载器（%v），按内置清单填", err)
				a = nil
			}
			avail[c.Type] = a
		}
		r := applyEmbyLib(cl, c, a, o)
		if r.OK {
			changed = true
			verb := map[string]string{embyLibCreate: "建好", embyLibAdd: "加进「" + c.Target + "」"}[c.Action]
			log.Printf("[Emby建库] ✓ %s：%s（%s · 路径 %s）", verb, c.Name, embyTypeLabel(c.Type), strings.Join(r.Paths, "、"))
			for _, w := range r.Warns {
				log.Printf("[Emby建库] ○ %s：%s", c.Name, w)
			}
		} else {
			log.Printf("[Emby建库] ✗ %s：%s", c.Name, r.Error)
		}
		out = append(out, r)
	}
	if changed && refresh {
		if err := cl.do(http.MethodPost, "/Library/Refresh", nil, nil); err != nil {
			log.Printf("[Emby建库] ✗ 触发扫描媒体库失败：%v", err)
		} else {
			log.Printf("[Emby建库] ✓ 已让 Emby 扫描媒体库")
		}
	}
	return out
}

// ---- 接口 ----

var embyLibCreateMu sync.Mutex

type embyLibPlanView struct {
	Configured bool          `json:"configured"`
	Error      string        `json:"error,omitempty"`
	Warnings   []string      `json:"warnings"`
	Cands      []embyLibCand `json:"cands"`
}

// embyLibPlanNow 现算一遍候选；Emby 没配 / 读不到时 Cands 为空、带原因
func embyLibPlanNow(overrides []embyLibOverride) (embyLibPlanView, embyRefreshCfg) {
	v := embyLibPlanView{Warnings: []string{}, Cands: []embyLibCand{}}
	cfg, ok := loadEmbyRefreshCfg()
	v.Configured = ok
	if !ok {
		v.Error = "还没配置 Emby（设置 → Emby）"
		return v, cfg
	}
	folders, err := embyVirtualFolders(cfg)
	if err != nil {
		v.Error = err.Error()
		return v, cfg
	}
	dirs, warns := gatherEmbyLibDirs()
	v.Warnings = append(v.Warnings, warns...)
	v.Cands = planEmbyLibs(dirs, folders, cfg.PathMapping, overrides)
	return v, cfg
}

// EmbyLibPlan POST /emby/libraries/plan：预览（body 可带用户改过的名字 / 类型）
func (h *Handler) EmbyLibPlan(c *gin.Context) {
	var req struct {
		Overrides []embyLibOverride `json:"overrides"`
	}
	_ = c.ShouldBindJSON(&req)
	v, _ := embyLibPlanNow(req.Overrides)
	c.JSON(http.StatusOK, v)
}

// EmbyLibCreate POST /emby/libraries/create：按选中的 key 执行，候选现算
func (h *Handler) EmbyLibCreate(c *gin.Context) {
	var req struct {
		Overrides []embyLibOverride `json:"overrides"`
		Keys      []string          `json:"keys"`
		Refresh   bool              `json:"refresh"`
		embyLibOptsReq
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Keys) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误：一个库都没选"})
		return
	}
	if !embyLibCreateMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "上一次建库还没做完"})
		return
	}
	defer embyLibCreateMu.Unlock()
	v, cfg := embyLibPlanNow(req.Overrides)
	if v.Error != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": v.Error})
		return
	}
	log.Printf("[Emby建库] ▶ 开始：选了 %d 个", len(req.Keys))
	res := runEmbyLibCreate(cfg, v.Cands, req.Keys, req.embyLibOptsReq, req.Refresh)
	ok, failed := 0, 0
	for _, r := range res {
		if r.OK {
			ok++
		} else {
			failed++
		}
	}
	log.Printf("[Emby建库] ■ 结束：成功 %d 个、没做成 %d 个", ok, failed)
	if res == nil {
		res = []embyLibResult{}
	}
	c.JSON(http.StatusOK, gin.H{"results": res, "ok": ok, "failed": failed, "message": strconv.Itoa(ok) + " 个已加进 Emby"})
}
