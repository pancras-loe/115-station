package api

import (
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// fakeAliClean 假阿里云盘：tree[盘][父目录] = 子项；trash[盘] = 回收站
type fakeAliClean struct {
	res, back string
	tree      map[string]map[string][]aliFile
	trash     map[string][]aliTrashItem
	calls     []string // "sub drive id1,id2"
}

func (f *fakeAliClean) drives() (string, string) { return f.res, f.back }
func (f *fakeAliClean) listDirIn(drive, parent string) ([]aliFile, error) {
	return f.tree[drive][parent], nil
}
func (f *fakeAliClean) listRecycle(drive string) ([]aliTrashItem, error) { return f.trash[drive], nil }
func (f *fakeAliClean) batchByID(sub, drive string, ids []string) (int, error) {
	f.calls = append(f.calls, sub+" "+drive+" "+strings.Join(ids, ","))
	return len(ids), nil
}

func dirF(id, name, parent string) aliFile {
	return aliFile{FileID: id, Name: name, Type: "folder", ParentFileID: parent}
}
func fileF(id, name, parent string, size int64) aliFile {
	return aliFile{FileID: id, Name: name, Type: "file", ParentFileID: parent, Size: size}
}

func newFakeAliClean() *fakeAliClean {
	return &fakeAliClean{
		res: "R", back: "B",
		tree: map[string]map[string][]aliFile{
			"R": {
				"root":  {dirF("share", "来自分享", "root"), dirF("tmp", aliTempRoot, "root"), dirF("d1", "重名", "root"), dirF("d2", "重名", "root")},
				"share": {dirF("s1", "电影", "share"), fileF("s2", "a.mkv", "share", 100)},
				"tmp":   {dirF("job1", "job1", "tmp")},
			},
			"B": {
				"root": {dirF("bk", "备份", "root")},
				"bk":   {fileF("b1", "x.zip", "bk", 5)},
			},
		},
	}
}

func TestAliPathParts(t *testing.T) {
	p, err := aliPathParts(` \来自分享//电影/ `)
	if err != nil || aliJoinPath(p) != "/来自分享/电影" {
		t.Fatalf("got %v %v", p, err)
	}
	if _, err := aliPathParts("/a/../b"); err == nil {
		t.Fatal(".. 应拒绝")
	}
	srcs := []model.PanSource{{ID: 1}}
	if _, err := normalizeAliDirs([]aliDirCleanDir{{Account: 1, Path: "/"}}, srcs); err == nil {
		t.Fatal("根目录应拒绝")
	}
	if _, err := normalizeAliDirs([]aliDirCleanDir{{Account: 9, Path: "/a"}}, srcs); err == nil {
		t.Fatal("不存在的账号应拒绝")
	}
	out, err := normalizeAliDirs([]aliDirCleanDir{{Account: 1, Path: "a/"}, {Account: 1, Path: "/a", Drive: "x"}, {Account: 1, Path: "/a", Drive: "backup"}, {Account: 1, Path: " "}}, srcs)
	if err != nil || len(out) != 2 || out[0].Drive != "resource" || out[1].Drive != "backup" {
		t.Fatalf("去重 / 盘归一不对: %+v %v", out, err)
	}
}

func TestRunAliDirClean(t *testing.T) {
	f := newFakeAliClean()
	open := func(model.PanSource) (aliCleanSide, error) { return f, nil }
	srcs := []model.PanSource{{ID: 1, Name: "阿里"}}
	dirs := []aliDirCleanDir{
		{Account: 1, Path: "/来自分享"},
		{Account: 1, Path: "/" + aliTempRoot},
		{Account: 1, Path: "/" + aliTempRoot + "/job1"},
		{Account: 1, Path: "/重名"},
		{Account: 1, Path: "/没有"},
		{Account: 1, Path: "/备份", Drive: "backup"},
		{Account: 1, Path: "/来自分享/电影", FileID: "old"},
	}
	res := runAliDirClean(dirs, srcs, open, false, true)
	if len(res) != len(dirs) {
		t.Fatalf("结果数 %d", len(res))
	}
	if r := res[0]; r.Error != "" || r.Dirs != 1 || r.Files != 1 || r.Deleted != 2 || r.FileID != "share" {
		t.Fatalf("来自分享: %+v", r)
	}
	for i := 1; i <= 2; i++ {
		if !strings.Contains(res[i].Error, "秒传") {
			t.Fatalf("临时目录应拒绝: %+v", res[i])
		}
	}
	if !strings.Contains(res[3].Error, "同名") {
		t.Fatalf("同名目录应拒绝: %+v", res[3])
	}
	if res[4].Error == "" {
		t.Fatal("不存在的路径应报错")
	}
	if r := res[5]; r.Error != "" || r.Deleted != 1 {
		t.Fatalf("备份盘: %+v", r)
	}
	if !strings.Contains(res[6].Error, "另一个目录") {
		t.Fatalf("file_id 对不上应拒绝: %+v", res[6])
	}
	want := []string{"/recyclebin/trash R s1,s2", "/recyclebin/trash B b1"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("删除调用 %v", f.calls)
	}

	// 备份盘的同名临时目录不是秒传用的，可以清
	f.tree["B"]["root"] = append(f.tree["B"]["root"], dirF("btmp", aliTempRoot, "root"))
	if r := runAliDirClean([]aliDirCleanDir{{Account: 1, Path: "/" + aliTempRoot, Drive: "backup"}}, srcs, open, true, true); r[0].Error != "" {
		t.Fatalf("备份盘同名目录: %+v", r[0])
	}
	// 没分资源盘：备份盘就是资源盘，临时目录照样拦
	f.back = "R"
	if r := runAliDirClean([]aliDirCleanDir{{Account: 1, Path: "/" + aliTempRoot, Drive: "backup"}}, srcs, open, true, true); r[0].Error == "" {
		t.Fatal("同一个盘时临时目录应拒绝")
	}
	f.back = "B"

	// 返回的子项不是这个目录的：整个不删
	f.calls = nil
	f.tree["R"]["share"] = append(f.tree["R"]["share"], fileF("zz", "z", "root", 1))
	r := runAliDirClean(dirs[:1], srcs, open, false, true)
	if r[0].Error == "" || len(f.calls) != 0 {
		t.Fatalf("parent 对不上应不删: %+v %v", r[0], f.calls)
	}
	// 预览不删
	f.tree["R"]["share"] = f.tree["R"]["share"][:2]
	if r := runAliDirClean(dirs[:1], srcs, open, true, true); r[0].Deleted != 0 || len(f.calls) != 0 {
		t.Fatal("预览不该删")
	}
	if msg, ok := aliDirCleanSummary(res, false); ok || !strings.Contains(msg, "资源盘 /来自分享：删进回收站 2 / 2 项") {
		t.Fatalf("summary: %v %s", ok, msg)
	}
}

func TestAliRbPick(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	items := []aliTrashItem{
		{FileID: "a", DriveID: "R", Size: 10, TrashedAt: now.Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)},
		{FileID: "b", DriveID: "R", Size: 20, TrashedAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
		{FileID: "c", DriveID: "B", Size: 30},
		{FileID: "a", DriveID: "R", Size: 10, TrashedAt: "2020-01-01T00:00:00Z"},
		{FileID: "", DriveID: "R"},
	}
	by, n, size, unknown := aliRbPick(items, 7, now)
	if n != 1 || size != 10 || unknown != 1 || strings.Join(by["R"], ",") != "a" || len(by["B"]) != 0 {
		t.Fatalf("保留 7 天: %v %d %d %d", by, n, size, unknown)
	}
	by, n, size, unknown = aliRbPick(items, 0, now)
	if n != 3 || size != 60 || unknown != 0 || len(by["R"]) != 2 || len(by["B"]) != 1 {
		t.Fatalf("整个清空: %v %d %d %d", by, n, size, unknown)
	}
}

func TestRunAliRbClean(t *testing.T) {
	old := time.Now().Add(-40 * 24 * time.Hour).Format(time.RFC3339)
	f := newFakeAliClean()
	f.trash = map[string][]aliTrashItem{
		"R": {{FileID: "r1", DriveID: "R", TrashedAt: old}, {FileID: "r2", DriveID: "R", TrashedAt: time.Now().Format(time.RFC3339)}},
		"B": {{FileID: "b1", DriveID: "B", TrashedAt: old}},
	}
	open := func(model.PanSource) (aliCleanSide, error) { return f, nil }
	plans := []aliRbPlan{{src: model.PanSource{ID: 1, Name: "阿里"}, keepDays: 30}}
	if res := runAliRbClean(plans, open, true); res[0].Expired != 2 || res[0].Total != 3 || len(f.calls) != 0 {
		t.Fatalf("预览: %+v %v", res[0], f.calls)
	}
	res := runAliRbClean(plans, open, false)
	if res[0].Deleted != 2 || res[0].Error != "" {
		t.Fatalf("清理: %+v", res[0])
	}
	got := strings.Join(f.calls, "|")
	if !strings.Contains(got, "/file/delete R r1") || !strings.Contains(got, "/file/delete B b1") || strings.Contains(got, "r2") {
		t.Fatalf("删除调用 %v", f.calls)
	}
	cfg := aliRbCleanCfg{Accts: map[string]aliRbAcctCfg{"1": {On: true}, "2": {On: false, KeepDays: 3}}}
	if p := aliRbPlans(cfg, []model.PanSource{{ID: 1}, {ID: 2}}); len(p) != 1 || p[0].keepDays != 0 {
		t.Fatalf("plans %+v", p)
	}
	if err := checkAliRbReqs([]aliRbAcctReq{{ID: 1, KeepDays: -1}}, []model.PanSource{{ID: 1}}); err == nil {
		t.Fatal("负数天数应拒绝")
	}
}
