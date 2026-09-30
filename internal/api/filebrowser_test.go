package api

import (
	"reflect"
	"strings"
	"testing"
)

// fakeCloudOps 网盘桩：cid → 条目（webapi 形态），记录删除与上传
type fakeCloudOps struct {
	dirs     map[string][]map[string]interface{}
	listed   map[string]int
	deleted  []string
	uploaded []string // cid/name
}

func (f *fakeCloudOps) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	if f.listed == nil {
		f.listed = map[string]int{}
	}
	f.listed[cid]++
	all := f.dirs[cid]
	if offset >= len(all) {
		return nil, len(all), nil
	}
	return all[offset:], len(all), nil
}

func (f *fakeCloudOps) deleteFiles(fids []string) error {
	f.deleted = append(f.deleted, fids...)
	return nil
}

func (f *fakeCloudOps) upload(cid, name string, data []byte) error {
	f.uploaded = append(f.uploaded, cid+"/"+name)
	return nil
}

func fdir(cid, name string) map[string]interface{} {
	return map[string]interface{}{"f": "0", "cid": cid, "n": name}
}

func ffile(fid, name string) map[string]interface{} {
	return map[string]interface{}{"f": "1", "fid": fid, "n": name, "pc": "pc" + fid, "s": float64(1)}
}

func TestListFileEntriesDirsFirst(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"1": {ffile("f2", "b.mkv"), fdir("d1", "Z 目录"), ffile("f1", "a.mkv"), fdir("d2", "A 目录")},
	}}
	items, truncated, err := listFileEntries(ops, "1", 100)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	want := []string{"A 目录", "Z 目录", "a.mkv", "b.mkv"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("顺序 = %v, want %v", names, want)
	}
	if items[0].ID != "d2" || items[2].ID != "f1" || items[2].PickCode != "pcf1" {
		t.Fatalf("目录用自身 cid、文件用 fid：%+v", items)
	}
}

func TestItemLibRel(t *testing.T) {
	chain := []browseCrumb{{"100", "影视"}, {"101", "剧集"}, {"102", "国产剧"}}
	it := fileJobItem{ID: "103", Name: "狂飙 (2023)", IsDir: true}
	lib, rel := itemLibRel(chain, 0, it)
	if lib != "影视" || rel != "剧集/国产剧/狂飙 (2023)" {
		t.Fatalf("lib=%q rel=%q", lib, rel)
	}
	// 在网盘根勾选了媒体库目录本身
	root := fileJobItem{ID: "100", Name: "影视", IsDir: true}
	if lib, rel := itemLibRel(nil, -1, root); lib != "影视" || rel != "" {
		t.Fatalf("库根本身 lib=%q rel=%q", lib, rel)
	}
}

func TestChainZone(t *testing.T) {
	roles := map[string]string{"1": "library", "5": "redundant"}
	chain := []browseCrumb{{"9", "StrmStation"}, {"5", "冗余"}, {"6", "某片"}}
	if r, i := chainZone(chain, roles); r != "redundant" || i != 1 {
		t.Fatalf("zone = %s,%d", r, i)
	}
	if r, i := chainZone([]browseCrumb{{"9", "x"}}, roles); r != "" || i != -1 {
		t.Fatalf("不在工作区: %s,%d", r, i)
	}
}

func TestOrgPickPrecheck(t *testing.T) {
	roles := map[string]string{"100": "library", "200": "pending", "300": "redundant"}
	dir := fileJobItem{ID: "301", Name: "某片", IsDir: true}
	if err := orgPickPrecheck([]browseCrumb{{"300", "冗余"}}, roles, []fileJobItem{dir}); err != nil {
		t.Fatalf("冗余里的目录应该能整理: %v", err)
	}
	if err := orgPickPrecheck([]browseCrumb{{"100", "影视"}, {"101", "电影"}}, roles, []fileJobItem{dir}); err == nil {
		t.Fatal("媒体库里的内容必须拒绝（走重新整理）")
	}
	if err := orgPickPrecheck(nil, roles, []fileJobItem{{ID: "200", Name: "待整理", IsDir: true}}); err == nil {
		t.Fatal("工作区根目录本身不能当条目整理")
	}
	if err := orgPickPrecheck(nil, roles, []fileJobItem{{ID: "f1", Name: "说明.txt"}}); err == nil {
		t.Fatal("非视频散文件应拒绝")
	}
	if err := orgPickPrecheck(nil, roles, []fileJobItem{{ID: "f2", Name: "某片.2020.1080p.mkv"}}); err != nil {
		t.Fatalf("视频散文件应放行: %v", err)
	}
}

func TestPickEntries(t *testing.T) {
	top := []dirEntry{
		{Fid: "d1", Cid: "d1", Name: "A", IsDir: true},
		{Fid: "f1", Cid: "p", Name: "b.mkv"},
		{Fid: "f2", Cid: "p", Name: "c.mkv"},
	}
	picked, missing := pickEntries(top, []fileJobItem{
		{ID: "f2", Name: "c.mkv"}, {ID: "d1", Name: "A", IsDir: true}, {ID: "gone", Name: "已搬走.mkv"},
	})
	var names []string
	for _, e := range picked {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"c.mkv", "A"}) || !reflect.DeepEqual(missing, []string{"已搬走.mkv"}) {
		t.Fatalf("picked=%v missing=%v", names, missing)
	}
}

func TestFileJobDedupeOrderIndependent(t *testing.T) {
	a := fileJobDedupe(fileJobParams{Cid: "1", Items: []fileJobItem{{ID: "b"}, {ID: "a"}}})
	b := fileJobDedupe(fileJobParams{Cid: "1", Items: []fileJobItem{{ID: "a"}, {ID: "b"}}})
	if a != b {
		t.Fatalf("勾选顺序不同也是同一批: %q vs %q", a, b)
	}
}

func TestRowActionsOf(t *testing.T) {
	roles := map[string]string{"100": "library", "200": "pending", "300": "redundant"}
	layout := libCategoryLayout{"电影/华语电影": "movie", "剧集/国产剧": "tv"}
	dir := func(id, name string) fileEntry { return fileEntry{ID: id, Name: name, IsDir: true} }
	file := func(id, name string) fileEntry {
		return fileEntry{ID: id, Name: name, Video: videoExts[strings.ToLower(pathExt(name))]}
	}

	if a := rowActionsOf(nil, roles, layout, dir("9", "x")); a.Organize || a.Move || a.Block == "" {
		t.Fatalf("位置未知必须什么都不给做: %+v", a)
	}
	if a := rowActionsOf([]browseCrumb{}, roles, layout, dir("200", "待整理")); a.Organize || a.Move || a.Block == "" {
		t.Fatalf("工作区根不能动: %+v", a)
	}
	// 网盘根下的普通目录：整理 + 移动
	if a := rowActionsOf([]browseCrumb{}, roles, layout, dir("9", "某片")); !a.Organize || !a.Move || a.Title {
		t.Fatalf("普通目录: %+v", a)
	}
	// 前端曾把 .strm / .vob 当视频，后端不认：按钮点下去 400
	for _, n := range []string{"a.strm", "a.vob", "说明.txt"} {
		if a := rowActionsOf([]browseCrumb{}, roles, layout, file("f", n)); a.Organize || !a.Move || a.OrganizeBlock == "" {
			t.Fatalf("%s 只能移动: %+v", n, a)
		}
	}
	if a := rowActionsOf([]browseCrumb{}, roles, layout, file("f", "某片.2020.mkv")); !a.Organize || !a.Move {
		t.Fatalf("视频: %+v", a)
	}
	// 冗余里：不能再移到冗余，但还能去已存在 / 待整理
	if a := rowActionsOf([]browseCrumb{{"300", "冗余"}}, roles, layout, dir("9", "某片")); !a.Move {
		t.Fatalf("冗余里的目录应能移走: %+v", a)
	}
	// 只配了冗余、又正在冗余里：无处可移
	only := map[string]string{"300": "redundant"}
	if a := rowActionsOf([]browseCrumb{{"300", "冗余"}}, only, layout, file("f", "说明.txt")); a.Move || a.Block == "" {
		t.Fatalf("无处可移的非视频文件: %+v", a)
	}

	// 媒体库：分类目录的下一层才是片目
	lib := []browseCrumb{{"100", "影视"}, {"101", "电影"}, {"102", "华语电影"}}
	a := rowActionsOf(lib, roles, layout, dir("103", "流浪地球 (2019)"))
	if !a.Title || !a.Organize || !a.Move || a.TitleKey != "影视/电影/华语电影/流浪地球 (2019)" || a.TitleRel != "电影/华语电影/流浪地球 (2019)" {
		t.Fatalf("片目: %+v", a)
	}
	if a := rowActionsOf(lib[:2], roles, layout, dir("102", "华语电影")); a.Organize || a.Move || a.Block == "" {
		t.Fatalf("分类目录不能动: %+v", a)
	}
	inTitle := append(append([]browseCrumb{}, lib...), browseCrumb{"103", "流浪地球 (2019)"})
	if a := rowActionsOf(inTitle, roles, layout, file("f", "流浪地球.mkv")); a.Organize || a.Move || a.Block == "" {
		t.Fatalf("片目里的文件不能动: %+v", a)
	}
}

func TestBrowseChainHint(t *testing.T) {
	if c := browseChainHint("", "0"); c == nil || len(c) != 0 {
		t.Fatalf("网盘根是已知位置: %v", c)
	}
	if c := browseChainHint(`[{"cid":"1","name":"a"},{"cid":"2","name":"b"}]`, "2"); len(c) != 2 {
		t.Fatalf("对得上的面包屑: %v", c)
	}
	if c := browseChainHint(`[{"cid":"1","name":"a"}]`, "2"); c != nil {
		t.Fatalf("末元素对不上要作废: %v", c)
	}
	if c := browseChainHint(`oops`, "2"); c != nil {
		t.Fatalf("坏 JSON 要作废: %v", c)
	}
}
