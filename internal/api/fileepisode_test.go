package api

import (
	"path"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 2026-10-04 现场：蜡笔小新第二季-720.mp4 的 720 被当成分辨率，进了 Season 0。
// 预填要给出 S02E720，并标成「推测」
func TestGuessEpisodeResolutionLikeDigits(t *testing.T) {
	cases := []struct {
		name, dir string
		want      episodePick
		guessed   bool
	}{
		{"蜡笔小新第二季-720.mp4", "蜡笔小新.1992.{tmdbid=323180}/Season 0", episodePick{2, 720}, true},
		{"蜡笔小新第二季-712.mp4", "蜡笔小新.1992.{tmdbid=323180}/Season 0", episodePick{2, 712}, false},
		{"Show.S03E05.mkv", "Show/Season 1", episodePick{3, 5}, false},
		{"E07.mkv", "Show/Season 4", episodePick{4, 7}, false},
		{"剧场版.mkv", "Show/Specials", episodePick{1, 0}, true},
	}
	for _, c := range cases {
		got, guessed := guessEpisode(c.name, c.dir, nil)
		if got != c.want || guessed != c.guessed {
			t.Errorf("%s: 得到 %+v guessed=%v，期望 %+v guessed=%v", c.name, got, guessed, c.want, c.guessed)
		}
	}
}

func episodeTestLayout() (libCategoryLayout, map[string]string, []browseCrumb) {
	layout := libCategoryLayout{"动漫番剧": "tv", "电影": "movie"}
	roles := map[string]string{"lib": "library"}
	chain := []browseCrumb{{"lib", "影视"}, {"cat", "动漫番剧"}, {"title", "蜡笔小新.1992.{tmdbid=323180}"}, {"s0", "Season 0"}}
	return layout, roles, chain
}

func TestLibTitleAroundAndRowActions(t *testing.T) {
	layout, roles, chain := episodeTestLayout()
	ti, ok := libTitleAround(chain, roles, layout)
	if !ok || ti.titleCid != "title" || ti.titleRel != "动漫番剧/蜡笔小新.1992.{tmdbid=323180}" || ti.dirRel != "Season 0" || ti.libName != "影视" {
		t.Fatalf("片目定位不对: %+v ok=%v", ti, ok)
	}
	// 就在片目这一层
	if ti, ok := libTitleAround(chain[:3], roles, layout); !ok || ti.dirRel != "" {
		t.Fatalf("片目本层定位不对: %+v ok=%v", ti, ok)
	}
	// 分类层不在任何片目里
	if _, ok := libTitleAround(chain[:2], roles, layout); ok {
		t.Fatal("分类目录不该算在片目里")
	}

	a := rowActionsOf(chain, roles, layout, fileEntry{ID: "f1", Name: "蜡笔小新第二季-720.mp4"})
	if !a.Episode || a.Organize || a.Move || a.TitleRel != "动漫番剧/蜡笔小新.1992.{tmdbid=323180}" {
		t.Fatalf("剧集片目里的视频应能指定季集: %+v", a)
	}
	if a := rowActionsOf(chain, roles, layout, fileEntry{ID: "f2", Name: "蜡笔小新第二季-720.ass"}); a.Episode || a.Block == "" {
		t.Fatalf("字幕不能指定季集: %+v", a)
	}
	if a := rowActionsOf(chain, roles, layout, fileEntry{ID: "d", Name: "extras", IsDir: true}); a.Episode || a.Block == "" {
		t.Fatalf("片目里的子目录不能操作: %+v", a)
	}
	movie := []browseCrumb{{"lib", "影视"}, {"cat", "电影"}, {"m", "流浪地球.2019.{tmdbid=535167}"}}
	if a := rowActionsOf(movie, roles, layout, fileEntry{ID: "f3", Name: "流浪地球.mkv"}); a.Episode {
		t.Fatalf("电影片目里的视频不能指定季集: %+v", a)
	}
}

func TestEpisodeFilesOfCompanions(t *testing.T) {
	dir := "T/Season 0"
	all := []remoteFile{
		{Fid: "v1", Name: "X - 1.mp4", Path: dir},
		{Fid: "v12", Name: "X - 12.mp4", Path: dir},
		{Fid: "s1", Name: "X - 1.chs.ass", Path: dir},
		{Fid: "s12", Name: "X - 12.chs.ass", Path: dir},
		{Fid: "t12", Name: "X - 12-thumb.jpg", Path: dir},
		{Fid: "n12", Name: "X - 12.nfo", Path: dir},
		{Fid: "deep", Name: "X - 12.srt", Path: dir + "/sub"},
	}
	files, missing := episodeFilesOf(all, dir, []fileJobItem{{ID: "v12", Name: "X - 12.mp4"}, {ID: "gone", Name: "没了.mp4"}})
	if len(missing) != 1 || missing[0] != "没了.mp4" {
		t.Fatalf("缺失清单不对: %v", missing)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Fid] = f.owner
	}
	for _, fid := range []string{"v12", "s12", "t12", "n12"} {
		if got[fid] != "v12" {
			t.Errorf("%s 应跟着 v12，实际 %q", fid, got[fid])
		}
	}
	for _, fid := range []string{"v1", "s1", "deep"} {
		if _, ok := got[fid]; ok {
			t.Errorf("%s 不该被带上", fid)
		}
	}
}

// 指定季集后的落点、改名与洗版判定
func TestPlanEpisodePicks(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 323180, Title: "蜡笔小新", Year: "1992", MediaType: "tv"}
	category := "动漫番剧"
	files := []orgRecordFile{
		{Fid: "v720", Name: "蜡笔小新第二季-720.mp4", Kind: "video", Sha1: "AAA"},
		{Fid: "s720", Name: "蜡笔小新第二季-720.chs.ass", Kind: "subtitle"},
		{Fid: "v480", Name: "蜡笔小新第二季-480.mp4", Kind: "video", Sha1: "BBB"},
	}
	picks := map[string]episodePick{"v720": {2, 720}, "v480": {2, 480}}
	probe, err := planRedoLayoutWith(media, category, files, "", nil, func(eps map[string]*ParsedName) { applyEpisodePicks(eps, picks) })
	if err != nil {
		t.Fatal(err)
	}
	titleRel := probe.rootRel
	seasonRel := ""
	for rel, gfs := range probe.groups {
		for _, g := range gfs {
			if g.Fid == "v720" {
				seasonRel = rel
			}
		}
	}
	t.Logf("落点 %s", seasonRel)
	if path.Dir(seasonRel) != titleRel || !strings.Contains(path.Base(seasonRel), "2") {
		t.Fatalf("应落进第二季目录，实际 %q（片目 %q）", seasonRel, titleRel)
	}
	ti := libFileTitle{libName: "影视", titleRel: titleRel, titleCid: "title", dirRel: "Season 0"}
	for i := range files {
		files[i].Dir = path.Join(ti.titleName(), ti.dirRel)
	}

	// 库内第二季：E480 有个更好的版本（洗版输）、E720 没有；勾选文件自己的台账行（Season 0、sha1 AAA）不算
	lib := []model.SyncedFile{
		{FileID: "old480", RelPath: "影视/" + seasonRel + "/蜡笔小新 - S02E480 - 2160p.strm", Kind: "video"},
	}
	ownRow := model.SyncedFile{FileID: "v720", RelPath: "影视/" + titleRel + "/Season 0/蜡笔小新第二季-720.strm", Kind: "video", Sha1: "AAA"}
	lg := episodeLedger{
		inDir: func(rel string) []model.SyncedFile {
			if rel == seasonRel {
				return lib
			}
			return nil
		},
		bySha1: func(sha1 string) []model.SyncedFile {
			if sha1 == "AAA" {
				return []model.SyncedFile{ownRow}
			}
			return nil
		},
	}
	st := &washStrategy{Mode: "replace", PriorityLevel: []washRule{{ResourcePix: "2160p"}, {ResourcePix: "1080p"}}}
	ep, err := planEpisodePicks(media, category, ti, files, picks, nil, st, lg, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]episodePlanItem{}
	for _, it := range ep.items {
		byID[it.ID] = it
	}
	if it := byID["v720"]; it.Wash != "new" || it.TargetRel != seasonRel || !strings.Contains(it.NewName, "S02E720") {
		t.Errorf("E720 应作为新增集进第二季: %+v", it)
	}
	if it := byID["v480"]; it.Wash != "exists" {
		t.Errorf("E480 库内有更优版本，应判输: %+v", it)
	}
	// 字幕跟着改名
	if n := ep.layout.renames["s720"]; !strings.Contains(n, "S02E720") || !strings.HasSuffix(n, ".chs.ass") {
		t.Errorf("字幕应跟着视频改名，实际 %q", n)
	}

	// 片目目录与模板算出来的不一致：拒绝（不许借指定季集换标题目录）
	bad := ti
	bad.titleRel = "动漫番剧/别的名字"
	if _, err := planEpisodePicks(media, category, bad, files, picks, nil, st, lg, nil); err == nil {
		t.Error("片目与模板不一致时应拒绝")
	}
	// 没填集号
	if _, err := planEpisodePicks(media, category, ti, files, map[string]episodePick{"v720": {2, 720}}, nil, st, lg, nil); err == nil {
		t.Error("有视频没给季集时应拒绝")
	}
}

// 没配洗版策略时目标位置已有同名文件：不能落进去
func TestPlanEpisodePicksNameConflict(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 1, Title: "某剧", Year: "2020", MediaType: "tv"}
	files := []orgRecordFile{{Fid: "v", Name: "某剧-5.mp4", Kind: "video"}}
	picks := map[string]episodePick{"v": {1, 5}}
	probe, err := planRedoLayoutWith(media, "动漫番剧", files, "", nil, func(eps map[string]*ParsedName) { applyEpisodePicks(eps, picks) })
	if err != nil {
		t.Fatal(err)
	}
	var rel, name string
	for r, gfs := range probe.groups {
		rel, name = r, gfs[0].Name
	}
	ti := libFileTitle{libName: "影视", titleRel: probe.rootRel, titleCid: "t", dirRel: "Season 0"}
	lg := episodeLedger{
		inDir: func(r string) []model.SyncedFile {
			if r == rel {
				return []model.SyncedFile{{FileID: "other", RelPath: "影视/" + rel + "/" + strmNameOf(name), Kind: "video"}}
			}
			return nil
		},
		bySha1: func(string) []model.SyncedFile { return nil },
	}
	ep, err := planEpisodePicks(media, "动漫番剧", ti, files, picks, nil, nil, lg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ep.items[0].Wash != "conflict" {
		t.Fatalf("同名文件应判冲突: %+v", ep.items[0])
	}
}

func TestApplyEpisodePicksSkipsAbsRemap(t *testing.T) {
	eps := map[string]*ParsedName{"a": {Season: 0, Episode: 0}, "b": {Season: 1, Episode: 3}}
	picks := map[string]episodePick{"a": {2, 720}}
	rest := withoutPicked(eps, picks)
	if _, ok := rest["a"]; ok || rest["b"] == nil {
		t.Fatalf("被指定的不该参与换算: %v", rest)
	}
	applyEpisodePicks(eps, picks)
	if eps["a"].Season != 2 || eps["a"].Episode != 720 {
		t.Fatalf("指定值没盖上: %+v", eps["a"])
	}
}
