package api

import (
	"net/url"
	"path"
	"strings"
)

// Jellyfin 与 Emby 分叉的查找（见 mediaserver.go 文件头）。
//
// Jellyfin 的 /Items 不认 Path、AnyProviderIdEquals 两个过滤（ItemsController.GetItems 的参数表里没有），
// 带上会被忽略、照常返回整库 —— Emby 那一套靠它们的入库确认、按新集提前探测、删除回声、
// 刮削后补全演职人员在 Jellyfin 上都会查不到（返回前有比对，不会认错，只是永远对不上）。

// jfWalkMax 往下走的层数上限：库目录 → 分类（可多级）→ 片目 / 剧 → 季 → 集，十层足够
const jfWalkMax = 10

func normEmbyPath(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
}

type jfChild struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
	Path string `json:"Path"`
	Type string `json:"Type"`
}

// jellyfinChildren 一个文件夹条目的直接子条目（带路径）
func jellyfinChildren(cfg embyRefreshCfg, parentID string) ([]jfChild, bool) {
	q := url.Values{
		"ParentId": {parentID}, "Fields": {"Path"},
		"EnableImages": {"false"}, "EnableUserData": {"false"}, "EnableTotalRecordCount": {"false"},
	}
	var out struct {
		Items []jfChild `json:"Items"`
	}
	if err := embyGetJSON(cfg.ServerURL, cfg.APIKey, "/Items", q, &out); err != nil {
		vlog("[Emby] Jellyfin 列子条目失败 %s: %v", parentID, err)
		return nil, false
	}
	return out.Items, true
}

// jellyfinItemsByPath 按路径找条目：从包含这条路径的媒体库根开始按 ParentId 一层层往下走，
// 每层只进路径是目标前缀的那个子目录，请求数 = 层数（四五次）。
//
// 和 Emby 的差别：Jellyfin 不给「一部电影一个目录」的那层目录建 Folder 条目（Movie 的路径就是 .strm），
// Emby 那边按目录查会命中 Type=Folder、再由 embyMediaChildOf 找下面的影片。
// 这里按目录查时直接把目录里的影视条目当命中返回，调用方拿到的就是 Movie，效果一样
func jellyfinItemsByPath(cfg embyRefreshCfg, embyPath string) []embyItemHit {
	want := normEmbyPath(embyPath)
	if want == "" {
		return nil
	}
	parent, best := "", -1
	for _, lib := range embyMediaFolders(cfg) {
		for _, loc := range lib.Locations {
			l := normEmbyPath(loc)
			if l == "" {
				continue
			}
			if l == want {
				return []embyItemHit{{ID: lib.ID, Name: lib.Name, Type: "CollectionFolder"}}
			}
			// 库目录嵌套时挑最深的那个
			if strings.HasPrefix(want, l+"/") && len(l) > best {
				parent, best = lib.ID, len(l)
			}
		}
	}
	if parent == "" {
		return nil
	}
	for depth := 0; depth < jfWalkMax; depth++ {
		kids, ok := jellyfinChildren(cfg, parent)
		if !ok {
			return nil
		}
		hits, next, virtual := jfMatchLevel(kids, want)
		if len(hits) > 0 {
			return hits
		}
		if next != "" {
			parent = next
			continue
		}
		// 没有季目录的剧（集直接放在剧目录里），Jellyfin 建的季条目没有路径，集挂在它下面
		for _, id := range virtual {
			if kids, ok := jellyfinChildren(cfg, id); ok {
				if hits, _, _ := jfMatchLevel(kids, want); len(hits) > 0 {
					return hits
				}
			}
		}
		return nil
	}
	return nil
}

// jfMatchLevel 一层子条目里找：路径正好是目标的；目标是目录时直接放在里面的影视条目；
// 路径是目标上级目录的那个（往下走）；没有路径的季（虚拟季）
func jfMatchLevel(kids []jfChild, want string) (hits []embyItemHit, next string, virtual []string) {
	var direct []embyItemHit
	for _, k := range kids {
		p := normEmbyPath(k.Path)
		hit := embyItemHit{ID: k.ID, Name: k.Name, Type: k.Type}
		switch {
		case p == "":
			if strings.EqualFold(k.Type, "Season") {
				virtual = append(virtual, k.ID)
			}
		case p == want:
			hits = append(hits, hit)
		case path.Dir(p) == want && embyTypeIsMedia(k.Type):
			direct = append(direct, hit)
		case strings.HasPrefix(want, p+"/"):
			next = k.ID
		}
	}
	if len(hits) == 0 {
		hits = direct
	}
	return hits, next, virtual
}

// jellyfinTitlesByTmdb 按 TMDB 编号找片目：Jellyfin 只有 HasTmdbId 布尔过滤，
// 所以按片名搜（条目名来自本站刮的 NFO，和 TMDB 片名一致）、再比编号。片名没有就查不了
func jellyfinTitlesByTmdb(base, key string, t personTarget, typ string) ([]embyTitleItem, error) {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		return nil, nil
	}
	q := url.Values{
		"Recursive": {"true"}, "IncludeItemTypes": {typ}, "SearchTerm": {title}, "HasTmdbId": {"true"},
		"Fields": {"ProviderIds,People,Path"}, "EnableUserData": {"false"}, "Limit": {"50"},
	}
	var page struct {
		Items []embyTitleItem `json:"Items"`
	}
	if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
		return nil, err
	}
	return page.Items, nil
}
