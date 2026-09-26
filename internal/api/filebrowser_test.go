package api

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

func TestMatchLedgerTitles(t *testing.T) {
	entries := map[string]*ledgerTitleEntry{
		"影视/剧集/国产剧/狂飙 (2023)":    {Key: "影视/剧集/国产剧/狂飙 (2023)"},
		"影视/剧集/国产剧/繁花 (2023)":    {Key: "影视/剧集/国产剧/繁花 (2023)"},
		"影视/电影/华语电影/流浪地球 (2019)": {Key: "影视/电影/华语电影/流浪地球 (2019)"},
		"电影/老台账 (2001)":          {Key: "电影/老台账 (2001)"},
	}
	keys := func(es []*ledgerTitleEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Key)
		}
		return out
	}

	got, within := matchLedgerTitles(entries, "影视/剧集/国产剧/狂飙 (2023)")
	if within || !reflect.DeepEqual(keys(got), []string{"影视/剧集/国产剧/狂飙 (2023)"}) {
		t.Fatalf("选中标题目录本身: %v within=%v", keys(got), within)
	}
	got, within = matchLedgerTitles(entries, "影视/剧集/国产剧/狂飙 (2023)/Season 01")
	if !within || len(got) != 1 {
		t.Fatalf("选中季目录应落在那一部里且只刮这一季: %v within=%v", keys(got), within)
	}
	got, _ = matchLedgerTitles(entries, "影视/剧集")
	if !reflect.DeepEqual(keys(got), []string{"影视/剧集/国产剧/狂飙 (2023)", "影视/剧集/国产剧/繁花 (2023)"}) {
		t.Fatalf("选中分类目录应展开到下面每一部: %v", keys(got))
	}
	// 名字是前缀但不是上级目录：「狂飙」不能匹配「狂飙 (2023)」
	if got, _ = matchLedgerTitles(entries, "影视/剧集/国产剧/狂飙"); len(got) != 0 {
		t.Fatalf("前缀相同的兄弟目录不该命中: %v", keys(got))
	}
	if got, _ = matchLedgerTitles(entries, "电影/老台账 (2001)/a.mkv"); len(got) != 1 {
		t.Fatalf("不带库名的老台账路径: %v", keys(got))
	}
}

func TestSelWithin(t *testing.T) {
	sel := "影视/剧集/国产剧/狂飙 (2023)/Season 01/狂飙.S01E01.mkv"
	if !selWithin(sel+".strm", sel) {
		t.Fatal("单集：strm 就是所选视频")
	}
	if selWithin("影视/剧集/国产剧/狂飙 (2023)/Season 01/狂飙.S01E02.mkv.strm", sel) {
		t.Fatal("别的集不在范围内")
	}
	dir := "影视/剧集/国产剧/狂飙 (2023)/Season 01"
	if !selWithin(dir+"/狂飙.S01E02.mkv.strm", dir) || selWithin("影视/剧集/国产剧/狂飙 (2023)/Season 010/x.mkv.strm", dir) {
		t.Fatal("季目录按目录边界判定")
	}
}

func TestItemLibIndexAndRel(t *testing.T) {
	roles := map[string]string{"100": "library", "200": "pending"}
	chain := []browseCrumb{{"100", "影视"}, {"101", "剧集"}, {"102", "国产剧"}}
	it := fileJobItem{ID: "103", Name: "狂飙 (2023)", IsDir: true}
	idx := itemLibIndex(chain, roles, it)
	if idx != 0 {
		t.Fatalf("idx = %d", idx)
	}
	lib, rel := itemLibRel(chain, idx, it)
	if lib != "影视" || rel != "剧集/国产剧/狂飙 (2023)" {
		t.Fatalf("lib=%q rel=%q", lib, rel)
	}
	// 在网盘根勾选了媒体库目录本身
	root := fileJobItem{ID: "100", Name: "影视", IsDir: true}
	if idx := itemLibIndex(nil, roles, root); idx != -1 {
		t.Fatalf("库根本身 idx = %d", idx)
	}
	if lib, rel := itemLibRel(nil, -1, root); lib != "影视" || rel != "" {
		t.Fatalf("库根本身 lib=%q rel=%q", lib, rel)
	}
	if idx := itemLibIndex([]browseCrumb{{"200", "待整理"}}, roles, it); idx != -2 {
		t.Fatalf("待整理里的不在库里: %d", idx)
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

// ---- 产物出口 ----

func TestFileScrapeWriterLocalNoUploadMarksHandled(t *testing.T) {
	dir := t.TempDir()
	ops := &fakeCloudOps{}
	w := newFileScrapeWriter(ops, false, false)
	var handled []string
	w.markHandled = func(p string) { handled = append(handled, p) }
	d := metaDest{Local: dir, CloudBase: "100", CloudRel: "电影/某片"}

	if wrote, err := w.put(d, "poster.jpg", []byte("x")); !wrote || err != nil {
		t.Fatalf("首次写入 wrote=%v err=%v", wrote, err)
	}
	// 本次不上传：一个 115 请求都不该发，但要登记成已处理，免得监控上传随后自己传上去
	if len(ops.listed) != 0 || len(ops.uploaded) != 0 {
		t.Fatalf("不上传时不该碰网盘: listed=%v uploaded=%v", ops.listed, ops.uploaded)
	}
	if !reflect.DeepEqual(handled, []string{filepath.Join(dir, "poster.jpg")}) {
		t.Fatalf("handled = %v", handled)
	}
	// 只补缺失：本地已有就跳过
	if wrote, _ := w.put(d, "poster.jpg", []byte("y")); wrote {
		t.Fatal("已存在且不覆盖时不该写")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "poster.jpg")); string(b) != "x" {
		t.Fatalf("内容被改了: %q", b)
	}
	if w.stat != (fileScrapeStat{Local: 1, Skipped: 1}) {
		t.Fatalf("stat = %+v", w.stat)
	}
}

func TestFileScrapeWriterUploadResolvesCloudDir(t *testing.T) {
	dir := t.TempDir()
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"100": {fdir("101", "电影")},
		"101": {fdir("102", "某片 (2020)")},
		"102": {ffile("old", "poster.jpg"), ffile("v1", "某片.mkv")},
	}}
	w := newFileScrapeWriter(ops, false, true)
	d := metaDest{Local: dir, CloudBase: "100", CloudRel: "电影/某片 (2020)"}

	// 网盘已有 poster.jpg、不覆盖：本地照写，网盘不动
	if _, err := w.put(d, "poster.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(ops.uploaded) != 0 || len(ops.deleted) != 0 {
		t.Fatalf("网盘已有同名文件且不覆盖，不该上传: %v %v", ops.uploaded, ops.deleted)
	}
	// 网盘没有的 fanart.jpg：上传到解析出来的目录
	if _, err := w.put(d, "fanart.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ops.uploaded, []string{"102/fanart.jpg"}) {
		t.Fatalf("uploaded = %v", ops.uploaded)
	}
	// 每一层只列一次
	for cid, n := range ops.listed {
		if n != 1 {
			t.Fatalf("目录 %s 被列了 %d 次", cid, n)
		}
	}
}

func TestFileScrapeWriterForceReplacesCloudFile(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"7": {ffile("old", "tvshow.nfo")},
	}}
	w := newFileScrapeWriter(ops, true, true)
	if wrote, err := w.put(metaDest{CloudBase: "7"}, "tvshow.nfo", []byte("new")); !wrote || err != nil {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	// 115 允许同名并存：覆盖必须先删旧的再传，否则目录里会有两份
	if !reflect.DeepEqual(ops.deleted, []string{"old"}) || !reflect.DeepEqual(ops.uploaded, []string{"7/tvshow.nfo"}) {
		t.Fatalf("deleted=%v uploaded=%v", ops.deleted, ops.uploaded)
	}
}

func TestFileScrapeWriterCloudOnlySkipsExisting(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"7": {ffile("old", "poster.jpg")},
	}}
	w := newFileScrapeWriter(ops, false, true)
	if wrote, _ := w.put(metaDest{CloudBase: "7"}, "poster.jpg", []byte("x")); wrote {
		t.Fatal("只补缺失：网盘已有就跳过")
	}
	if w.stat.Skipped != 1 || len(ops.uploaded) != 0 {
		t.Fatalf("stat=%+v uploaded=%v", w.stat, ops.uploaded)
	}
}

func TestFileScrapeWriterMissingCloudDir(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{"100": {fdir("101", "电影")}}}
	w := newFileScrapeWriter(ops, false, true)
	_, err := w.put(metaDest{CloudBase: "100", CloudRel: "电影/不存在"}, "poster.jpg", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "网盘上没有对应目录") {
		t.Fatalf("找不到目录应报错且不建目录: %v", err)
	}
	if len(ops.uploaded) != 0 {
		t.Fatal("不该上传")
	}
}

func TestWalkCloudVideosDepthAndFilter(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"1": {fdir("2", "Season 01"), ffile("a", "说明.txt"), ffile("b", "某剧.S00E01.mkv")},
		"2": {ffile("c", "某剧.S01E01.mkv"), fdir("3", "花絮")},
		"3": {ffile("d", "花絮.mp4")},
	}}
	vs, err := walkCloudVideos(ops, "1", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range vs {
		got = append(got, v.parentCid+"/"+v.name)
	}
	sort.Strings(got)
	want := []string{"1/某剧.S00E01.mkv", "2/某剧.S01E01.mkv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v（深度 2 不下钻第三层，非视频不收）", got, want)
	}
}
