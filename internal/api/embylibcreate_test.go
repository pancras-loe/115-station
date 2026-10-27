package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func findCand(t *testing.T, cs []embyLibCand, key string) embyLibCand {
	t.Helper()
	for _, c := range cs {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("没有候选 %s：%+v", key, cs)
	return embyLibCand{}
}

// 同一个分类目录在两个库里（两个账号）合成一个 Emby 库、带两个路径；类型来自规则
func TestPlanEmbyLibsMergeCategory(t *testing.T) {
	dirs := []embyLibDir{
		{Key: "cat:电影/华语电影", Name: "华语电影", Type: embyLibMovies, Fixed: true, LibName: "主号", Local: "/media/主号/电影/华语电影", Scrape: libScrapeAuto},
		{Key: "cat:电影/华语电影", Name: "华语电影", Type: embyLibMovies, Fixed: true, LibName: "附号", Local: "/media/附号/电影/华语电影", Scrape: libScrapeManual},
		{Key: "cat:剧集/国产剧", Name: "国产剧", Type: embyLibTV, Fixed: true, LibName: "主号", Local: "/media/主号/剧集/国产剧", Scrape: libScrapeOff},
	}
	cs := planEmbyLibs(dirs, nil, "#/mnt/media", nil)
	if len(cs) != 2 {
		t.Fatalf("应合成 2 个候选：%+v", cs)
	}
	m := findCand(t, cs, "cat:电影/华语电影")
	if m.Action != embyLibCreate || len(m.Paths) != 2 || m.Scrape != embyScrapeStation || m.Type != embyLibMovies {
		t.Fatalf("华语电影：%+v", m)
	}
	if m.Paths[0].Emby != "/mnt/media/主号/电影/华语电影" || m.Paths[1].Emby != "/mnt/media/附号/电影/华语电影" {
		t.Fatalf("路径映射：%+v", m.Paths)
	}
	if tv := findCand(t, cs, "cat:剧集/国产剧"); tv.Scrape != embyScrapeOff || tv.Type != embyLibTV {
		t.Fatalf("国产剧：%+v", tv)
	}
}

// 已覆盖的跳过；Emby 库在它下面的算冲突；同名库加路径；类型对不上的同名库跳过；映射换不出的标出来
func TestPlanEmbyLibsAgainstEmby(t *testing.T) {
	dirs := []embyLibDir{
		{Key: "cat:电影/华语电影", Name: "华语电影", Type: embyLibMovies, Fixed: true, LibName: "A", Local: "/media/A/电影/华语电影"},
		{Key: "cat:电影/欧美电影", Name: "欧美电影", Type: embyLibMovies, Fixed: true, LibName: "A", Local: "/media/A/电影/欧美电影"},
		{Key: "cat:剧集/国产剧", Name: "国产剧", Type: embyLibTV, Fixed: true, LibName: "A", Local: "/media/A/剧集/国产剧"},
		{Key: "cat:剧集/日韩剧", Name: "日韩剧", Type: embyLibTV, Fixed: true, LibName: "A", Local: "/media/A/剧集/日韩剧"},
		{Key: "lib:9", Name: "外面", Type: embyLibMixed, LibName: "外面", Local: "/other/外面"},
		{Key: "cat:动漫", Name: "动漫", Type: embyLibTV, Fixed: true, LibName: "A", Local: "/media/A/动漫"},
	}
	folders := []embyVirtualFolder{
		{Name: "电影", CollectionType: "movies", Locations: []string{"/mnt/media/A/电影/华语电影"}},
		{Name: "欧美电影", ItemID: "42", CollectionType: "movies", Locations: []string{"/mnt/media/B/欧美电影"}},
		{Name: "国产剧", CollectionType: "movies", Locations: []string{"/mnt/x"}},
		{Name: "动画", CollectionType: "tvshows", Locations: []string{"/mnt/media/A/动漫/日本"}},
	}
	cs := planEmbyLibs(dirs, folders, "/media#/mnt/media", nil)
	if c := findCand(t, cs, "cat:电影/华语电影"); c.Action != embyLibSkip || c.Paths[0].State != embyPathCovered {
		t.Fatalf("已覆盖：%+v", c)
	}
	if c := findCand(t, cs, "cat:电影/欧美电影"); c.Action != embyLibAdd || c.TargetID != "42" {
		t.Fatalf("同名加路径：%+v", c)
	}
	if c := findCand(t, cs, "cat:剧集/国产剧"); c.Action != embyLibSkip || !strings.Contains(c.Why, "类型") {
		t.Fatalf("类型冲突：%+v", c)
	}
	if c := findCand(t, cs, "cat:剧集/日韩剧"); c.Action != embyLibCreate {
		t.Fatalf("新建：%+v", c)
	}
	if c := findCand(t, cs, "lib:9"); c.Action != embyLibSkip || c.Paths[0].State != embyPathUnmapped {
		t.Fatalf("映射换不出：%+v", c)
	}
	if c := findCand(t, cs, "cat:动漫"); c.Action != embyLibSkip || c.Paths[0].State != embyPathConflict {
		t.Fatalf("下面已有 Emby 库：%+v", c)
	}
}

// 新建的互相重名加上级目录名；用户改名、改类型生效，规则定下的类型不让改
func TestPlanEmbyLibsNamesAndOverrides(t *testing.T) {
	dirs := []embyLibDir{
		{Key: "cat:电影/动画", Name: "动画", Type: embyLibMovies, Fixed: true, LibName: "A", Local: "/media/A/电影/动画"},
		{Key: "cat:剧集/动画", Name: "动画", Type: embyLibTV, Fixed: true, LibName: "A", Local: "/media/A/剧集/动画"},
		{Key: "lib:3", Name: "收藏", Type: embyLibMixed, LibName: "收藏", Local: "/media/收藏"},
		// 同一个分类目录两边都有规则：只能混合
		{Key: "cat:综合", Name: "综合", Type: embyLibMovies, Fixed: true, LibName: "A", Local: "/media/A/综合"},
		{Key: "cat:综合", Name: "综合", Type: embyLibTV, Fixed: true, LibName: "A", Local: "/media/A/综合"},
	}
	cs := planEmbyLibs(dirs, nil, "", []embyLibOverride{
		{Key: "lib:3", Name: "我的收藏", Type: embyLibMovies},
		{Key: "cat:电影/动画", Type: embyLibTV},
	})
	a1, a2 := findCand(t, cs, "cat:电影/动画"), findCand(t, cs, "cat:剧集/动画")
	if a1.Name != "动画" || a2.Name != "动画（剧集）" || a1.Type != embyLibMovies {
		t.Fatalf("重名 / 固定类型：%q %q %s", a1.Name, a2.Name, a1.Type)
	}
	if c := findCand(t, cs, "lib:3"); c.Name != "我的收藏" || c.Type != embyLibMovies {
		t.Fatalf("改名改类型：%+v", c)
	}
	if c := findCand(t, cs, "cat:综合"); c.Type != embyLibMixed || len(c.Paths) != 1 {
		t.Fatalf("两边都有规则：%+v", c)
	}
}

func TestGuessEmbyLibType(t *testing.T) {
	for in, want := range map[string]string{"华语电影": embyLibMovies, "国产剧": embyLibTV, "动漫": embyLibTV, "纪录": embyLibMixed, "Movies": embyLibMovies} {
		if got := guessEmbyLibType(in); got != want {
			t.Errorf("%s → %s，应为 %s", in, got, want)
		}
	}
}

func testAvail() *embyAvailOpts {
	var a embyAvailOpts
	_ = json.Unmarshal([]byte(`{
	  "MetadataSavers":[{"Name":"Nfo"}],
	  "TypeOptions":[
	    {"Type":"Movie","MetadataFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true},{"Name":"OMDb"}],"ImageFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}],"DefaultImageOptions":[{"Type":"Backdrop","Limit":1}]},
	    {"Type":"Person","MetadataFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}],"ImageFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}]}
	  ],
	  "DefaultLibraryOptions":{"EnableRealtimeMonitor":true,"EnableChapterImageExtraction":true,"DisabledLocalMetadataReaders":["Nfo","Other"],"MinResumePct":5}
	}`), &a)
	return &a
}

func typeOpt(opts map[string]any, typ string) map[string]any {
	for _, t := range opts["TypeOptions"].([]map[string]any) {
		if t["Type"] == typ {
			return t
		}
	}
	return nil
}

func TestBuildEmbyLibOptions(t *testing.T) {
	st := buildEmbyLibOptions(testAvail(), embyLibMovies, embyScrapeStation, []string{"/mnt/a"}, embyLibOptsReq{})
	if st["EnableRealtimeMonitor"] != false || st["EnableChapterImageExtraction"] != false || st["SaveLocalMetadata"] != false {
		t.Fatalf("本站刮：%+v", st)
	}
	if st["MinResumePct"] != float64(5) {
		t.Fatalf("Emby 默认值要保留：%v", st["MinResumePct"])
	}
	if d := st["DisabledLocalMetadataReaders"].([]string); len(d) != 1 || d[0] != "Other" {
		t.Fatalf("Nfo 读取器要开：%v", d)
	}
	if m := typeOpt(st, "Movie"); len(m["MetadataFetchers"].([]string)) != 0 || len(m["ImageFetchers"].([]string)) != 0 {
		t.Fatalf("本站刮的库不开下载器：%v", m)
	}
	off := buildEmbyLibOptions(testAvail(), embyLibMovies, embyScrapeOff, []string{"/mnt/a"}, embyLibOptsReq{Realtime: true, SaveNfo: true})
	if m := typeOpt(off, "Movie"); strings.Join(m["MetadataFetchers"].([]string), ",") != "TheMovieDb" || m["ImageOptions"] == nil {
		t.Fatalf("交给 Emby 刮：%v", m)
	}
	if p := typeOpt(off, "Person"); len(p["MetadataFetchers"].([]string)) != 0 {
		t.Fatalf("人物不开 TMDB：%v", p)
	}
	if off["SaveLocalMetadata"] != true || strings.Join(off["MetadataSavers"].([]string), ",") != "Nfo" || off["EnableRealtimeMonitor"] != true {
		t.Fatalf("保存 NFO / 实时监控：%+v", off)
	}
	fb := buildEmbyLibOptions(nil, embyLibTV, embyScrapeStation, []string{"/mnt/a"}, embyLibOptsReq{})
	if typeOpt(fb, "Series") == nil || typeOpt(fb, "Episode") == nil {
		t.Fatalf("读不到可选项时按内置类型填：%v", fb["TypeOptions"])
	}
}

// fakeEmbyLibs 假 Emby：记下建了什么库、加了什么路径、提交的选项
type fakeEmbyLibs struct {
	mu      sync.Mutex
	folders []map[string]any
	reqs    []string
	refresh int
	// ignorePathInfos 模拟只认 query paths 的老版本：建库时不看 PathInfos
	ignorePathInfos bool
}

func (f *fakeEmbyLibs) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	find := func(name string) map[string]any {
		for _, fd := range f.folders {
			if strings.EqualFold(fd["Name"].(string), name) {
				return fd
			}
		}
		return nil
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/Library/VirtualFolders":
		_ = json.NewEncoder(w).Encode(f.folders)
	case r.URL.Path == "/Libraries/AvailableOptions":
		w.Write([]byte(`{"TypeOptions":[{"Type":"Movie","MetadataFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}],"ImageFetchers":[{"Name":"TheMovieDb","DefaultEnabled":true}]}],"DefaultLibraryOptions":{}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/Library/VirtualFolders":
		var b struct {
			LibraryOptions map[string]any `json:"LibraryOptions"`
		}
		_ = json.Unmarshal(body, &b)
		locs := []string{}
		if !f.ignorePathInfos {
			for _, p := range b.LibraryOptions["PathInfos"].([]any) {
				locs = append(locs, p.(map[string]any)["Path"].(string))
			}
		}
		f.folders = append(f.folders, map[string]any{"Name": r.URL.Query().Get("name"), "ItemId": "new" + r.URL.Query().Get("name"),
			"CollectionType": r.URL.Query().Get("collectionType"), "Locations": locs, "LibraryOptions": map[string]any{}})
	case r.URL.Path == "/Library/VirtualFolders/Paths":
		var b struct {
			Name string
			Path string
		}
		_ = json.Unmarshal(body, &b)
		fd := find(b.Name)
		if fd == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fd["Locations"] = append(fd["Locations"].([]string), b.Path)
	case r.URL.Path == "/Library/VirtualFolders/LibraryOptions":
		var b struct {
			Id             string
			LibraryOptions map[string]any
		}
		_ = json.Unmarshal(body, &b)
		for _, fd := range f.folders {
			if fd["ItemId"] == b.Id {
				fd["LibraryOptions"] = b.LibraryOptions
			}
		}
	case r.URL.Path == "/Library/Refresh":
		f.refresh++
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// 端到端：新建（选项固化、自检通过）、给同名库加路径、只认 query 的老版本补加路径、最后只扫一次
func TestRunEmbyLibCreateE2E(t *testing.T) {
	for _, old := range []bool{false, true} {
		fake := &fakeEmbyLibs{ignorePathInfos: old, folders: []map[string]any{
			{"Name": "欧美电影", "ItemId": "42", "CollectionType": "movies", "Locations": []string{"/mnt/B/欧美电影"}, "LibraryOptions": map[string]any{}},
		}}
		srv := httptest.NewServer(http.HandlerFunc(fake.handler))
		root := filepath.ToSlash(t.TempDir())
		cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k", PathMapping: root + "#/mnt"}
		dirs := []embyLibDir{
			{Key: "cat:电影/华语电影", Name: "华语电影", Type: embyLibMovies, Fixed: true, LibName: "A", Local: root + "/A/电影/华语电影", Scrape: libScrapeAuto},
			{Key: "cat:电影/欧美电影", Name: "欧美电影", Type: embyLibMovies, Fixed: true, LibName: "A", Local: root + "/A/电影/欧美电影", Scrape: libScrapeAuto},
		}
		folders, err := embyVirtualFolders(cfg)
		if err != nil {
			t.Fatal(err)
		}
		plan := planEmbyLibs(dirs, folders, cfg.PathMapping, nil)
		res := runEmbyLibCreate(cfg, plan, []string{"cat:电影/华语电影", "cat:电影/欧美电影"}, embyLibOptsReq{}, true)
		srv.Close()
		if len(res) != 2 || !res[0].OK || !res[1].OK {
			t.Fatalf("old=%v 结果：%+v", old, res)
		}
		if len(res[0].Warns) != 0 {
			t.Fatalf("old=%v 新建的库自检应通过：%v", old, res[0].Warns)
		}
		var cn, ow map[string]any
		for _, fd := range fake.folders {
			switch fd["Name"] {
			case "华语电影":
				cn = fd
			case "欧美电影":
				ow = fd
			}
		}
		if cn == nil || len(cn["Locations"].([]string)) != 1 || cn["Locations"].([]string)[0] != "/mnt/A/电影/华语电影" || cn["CollectionType"] != "movies" {
			t.Fatalf("old=%v 新建的库：%+v", old, cn)
		}
		if lo := cn["LibraryOptions"].(map[string]any); lo["EnableRealtimeMonitor"] != false {
			t.Fatalf("old=%v 选项没固化：%+v", old, lo)
		}
		if len(ow["Locations"].([]string)) != 2 || len(ow["LibraryOptions"].(map[string]any)) != 0 {
			t.Fatalf("old=%v 同名库只加路径、不动它的选项：%+v", old, ow)
		}
		if fake.refresh != 1 {
			t.Fatalf("old=%v 应只扫一次：%d", old, fake.refresh)
		}
	}
}
