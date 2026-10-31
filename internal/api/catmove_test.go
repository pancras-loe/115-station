package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 分类方案改过之后按新方案搬网盘上的片目（catmove.go）。现场：电影「动画电影」→「电影/动画电影」、
// 「纪录片」→「电影/纪录片」（剧集还有个「纪录片」），网盘没搬，本地 / Emby 和网盘分了家

func catMoveRules() map[uint][]model.CategoryRule {
	return map[uint][]model.CategoryRule{1: {
		{SchemeID: 1, MediaType: "movie", Name: "电影/动画电影"},
		{SchemeID: 1, MediaType: "movie", Name: "电影/纪录片"},
		{SchemeID: 1, MediaType: "movie", Name: "电影/其他", IsDefault: true},
		{SchemeID: 1, MediaType: "tv", Name: "纪录片"},
		{SchemeID: 1, MediaType: "tv", Name: "剧集"},
	}}
}

func TestCatMoveCandidates(t *testing.T) {
	libs := []model.Library{{ID: 1, Name: "影视", Local: "影视", Cid: "L", SchemeID: 1}, {ID: 2, Name: "不分类", Local: "其他", Cid: "M"}}
	ledger := map[string]int{
		"影视/动画电影/A (2020)":    3,
		"影视/纪录片/B (2021)":     1,
		"影视/纪录片/C (2019)":     8,
		"影视/电影/动画电影/D (2022)": 1,
		"其他/动画电影/F (2020)":    1,
	}
	recs := []model.OrganizeRecord{
		// 倒序：最近的在前
		{ID: 9, Status: "success", TmdbID: 1, MediaType: "movie", Title: "A", Category: "动画电影", TargetDir: "动画电影/A (2020)", LibID: 1},
		{ID: 8, Status: "success", TmdbID: 1, MediaType: "movie", Title: "A", Category: "动画电影", TargetDir: "动画电影/A (2020)", LibID: 1},
		{ID: 7, Status: "success", TmdbID: 2, MediaType: "movie", Title: "B", Category: "纪录片", TargetDir: "纪录片/B (2021)", LibID: 1},
		{ID: 6, Status: "success", TmdbID: 3, MediaType: "tv", Title: "C", Category: "纪录片", TargetDir: "纪录片/C (2019)", LibID: 1},
		{ID: 5, Status: "success", TmdbID: 4, MediaType: "movie", Title: "D", Category: "电影/动画电影", TargetDir: "电影/动画电影/D (2022)", LibID: 1},
		// 台账里已经没有了
		{ID: 4, Status: "success", TmdbID: 5, MediaType: "movie", Title: "E", Category: "动画电影", TargetDir: "动画电影/E (2018)", LibID: 1},
		// 失败的、不分类的库都不算
		{ID: 3, Status: "failed", TmdbID: 6, MediaType: "movie", Category: "动画电影", TargetDir: "动画电影/X", LibID: 1},
		{ID: 2, Status: "success", TmdbID: 7, MediaType: "movie", Category: "动画电影", TargetDir: "动画电影/F (2020)", LibID: 2},
		// 落点和分类对不上：不猜
		{ID: 1, Status: "success", TmdbID: 8, MediaType: "movie", Category: "动画电影", TargetDir: "别处/G", LibID: 1},
	}
	got := catMoveCandidates(libs, catMoveRules(), recs, func(rel string) int { return ledger[rel] })
	var keys []string
	for _, c := range got {
		keys = append(keys, c.Key)
	}
	want := []string{"1:动画电影/A (2020)", "1:纪录片/B (2021)"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("候选 %v，期望 %v", keys, want)
	}
	if got[0].Videos != 3 || got[0].TmdbID != 1 || got[1].MediaType != "movie" {
		t.Fatalf("候选内容不对：%+v", got)
	}
}

func TestPlanCatMoves(t *testing.T) {
	libs := []model.Library{{ID: 1, Local: "影视"}}
	cands := []catMoveCand{
		{Key: "a", LibID: 1, OldCat: "动画电影", TitleDir: "A"},
		{Key: "b", LibID: 1, OldCat: "纪录片", TitleDir: "B"},
		{Key: "c", LibID: 1, OldCat: "旧", TitleDir: "C"},
		{Key: "d", LibID: 1, OldCat: "旧2", TitleDir: "A"}, // 和 a 要去同一个位置
		{Key: "e", LibID: 1, OldCat: "旧3", TitleDir: "E"},
		{Key: "f", LibID: 1, OldCat: "旧4", TitleDir: "F"},
	}
	dest := map[string]string{"a": "电影/动画电影", "b": "电影/纪录片", "c": "旧", "d": "电影/动画电影", "f": "电影/其他"}
	ledger := map[string]int{"影视/电影/其他/F": 2}
	items := planCatMoves(cands, libs, func(c catMoveCand) (string, error) {
		if c.Key == "e" {
			return "", errors.New("超时")
		}
		return dest[c.Key], nil
	}, func(rel string) int { return ledger[rel] })
	wantMove := map[string]bool{"a": true, "b": true}
	for _, it := range items {
		if it.Move != wantMove[it.Key] {
			t.Errorf("%s: move=%v reason=%s", it.Key, it.Move, it.Reason)
		}
		if !it.Move && it.Reason == "" {
			t.Errorf("%s 不搬却没写原因", it.Key)
		}
	}
	if items[0].newRel() != "电影/动画电影/A" {
		t.Fatalf("新位置 %s", items[0].newRel())
	}
}

// fakeCatIO 假 115：目录树按 cid 存子项
type fakeCatIO struct {
	kids  map[string][]map[string]interface{}
	next  int
	moves int
	gone  map[string]bool
}

func newFakeCatIO() *fakeCatIO {
	return &fakeCatIO{kids: map[string][]map[string]interface{}{}, gone: map[string]bool{}}
}

func (f *fakeCatIO) add(parent, name string, dir bool) string {
	f.next++
	id := fmt.Sprintf("x%d", f.next)
	e := map[string]interface{}{"n": name}
	if dir {
		e["f"], e["cid"] = "0", id
	} else {
		e["f"], e["fid"] = "1", id
	}
	f.kids[parent] = append(f.kids[parent], e)
	return id
}

func (f *fakeCatIO) idOf(e map[string]interface{}) string {
	if fmt.Sprint(e["f"]) == "0" {
		return fmt.Sprint(e["cid"])
	}
	return fmt.Sprint(e["fid"])
}

func (f *fakeCatIO) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	all := f.kids[cid]
	if offset >= len(all) {
		return nil, len(all), nil
	}
	return all[offset:], len(all), nil
}

func (f *fakeCatIO) take(id string) map[string]interface{} {
	for p, list := range f.kids {
		for i, e := range list {
			if f.idOf(e) == id {
				f.kids[p] = append(list[:i:i], list[i+1:]...)
				return e
			}
		}
	}
	return nil
}

func (f *fakeCatIO) moveFiles(target string, fids []string) error {
	f.moves++
	for _, id := range fids {
		if e := f.take(id); e != nil {
			f.kids[target] = append(f.kids[target], e)
		}
	}
	return nil
}

func (f *fakeCatIO) deleteFiles(fids []string) error {
	for _, id := range fids {
		f.take(id)
		f.gone[id] = true
	}
	return nil
}

func (f *fakeCatIO) ensurePath(parent, dirPath string) (string, error) {
	cur := parent
	for _, seg := range strings.Split(dirPath, "/") {
		found := ""
		for _, e := range f.kids[cur] {
			if fmt.Sprint(e["f"]) == "0" && fmt.Sprint(e["n"]) == seg {
				found = fmt.Sprint(e["cid"])
			}
		}
		if found == "" {
			found = f.add(cur, seg, true)
		}
		cur = found
	}
	return cur, nil
}

func writeCatFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCatMoves(t *testing.T) {
	newTestDB(t, "catmove.db")
	forgetLibraries()
	t.Cleanup(forgetLibraries)
	lib := model.Library{Name: "影视", Local: "影视", Cid: "L", SchemeID: 1}
	if err := model.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	forgetLibraries()

	// 网盘：L/动画电影/A (2020)/A.mkv；L/纪录片/B (2021)/B.mkv，而新位置 电影/纪录片 下已经有个 B (2021)
	io := newFakeCatIO()
	anim := io.add("L", "动画电影", true)
	a := io.add(anim, "A (2020)", true)
	io.add(a, "A.mkv", false)
	doc := io.add("L", "纪录片", true)
	b := io.add(doc, "B (2021)", true)
	io.add(b, "B.mkv", false)
	mv := io.add("L", "电影", true)
	mdoc := io.add(mv, "纪录片", true)
	io.add(mdoc, "B (2021)", true)

	root := t.TempDir()
	writeCatFile(t, filepath.Join(root, "影视/动画电影/A (2020)/A.strm"), "old")
	writeCatFile(t, filepath.Join(root, "影视/动画电影/A (2020)/A.nfo"), "nfo")
	// 方案改过之后按新路径写出来的、只有 STRM 的重复片目
	writeCatFile(t, filepath.Join(root, "影视/电影/动画电影/A (2020)/A.strm"), "dup")
	writeCatFile(t, filepath.Join(root, "影视/纪录片/B (2021)/B.strm"), "b")

	model.DB.Create(&model.SyncedFile{FileID: "fa", RelPath: "影视/动画电影/A (2020)/A.strm", Kind: "video"})
	model.DB.Create(&model.SyncedFile{FileID: "fa2", RelPath: "影视/动画电影/A (2020)_x/Z.strm", Kind: "video"}) // 名字前缀相同的别的目录不动
	model.DB.Create(&model.SyncedFile{FileID: "fb", RelPath: "影视/纪录片/B (2021)/B.strm", Kind: "video"})
	rec := model.OrganizeRecord{Status: "success", TmdbID: 1, MediaType: "movie", Category: "动画电影", TargetDir: "动画电影/A (2020)", LibID: lib.ID}
	model.DB.Create(&rec)

	items := []catMoveItem{
		{catMoveCand: catMoveCand{LibID: lib.ID, TmdbID: 1, MediaType: "movie", OldCat: "动画电影", TitleDir: "A (2020)"}, NewCat: "电影/动画电影", Move: true},
		{catMoveCand: catMoveCand{LibID: lib.ID, TmdbID: 2, MediaType: "movie", OldCat: "纪录片", TitleDir: "B (2021)"}, NewCat: "电影/纪录片", Move: true},
		{catMoveCand: catMoveCand{LibID: lib.ID, TmdbID: 3, MediaType: "movie", OldCat: "纪录片", TitleDir: "没有这部"}, NewCat: "电影/纪录片", Move: true},
	}
	var logs []string
	res := applyCatMoves(io, lib, items, root, []string{"L"}, func(s string) { logs = append(logs, s) })
	if len(res.Moved) != 1 || len(res.Failed) != 2 {
		t.Fatalf("搬成 %d、失败 %d：%v", len(res.Moved), len(res.Failed), logs)
	}

	// 网盘：A 进了 电影/动画电影，旧的「动画电影」空了被清掉；「纪录片」里 B 还在，不删
	dest, _ := io.ensurePath("L", "电影/动画电影")
	names, _ := childDirNames(io, dest)
	if !names["A (2020)"] {
		t.Fatalf("A 没进新分类目录")
	}
	if !io.gone[anim] {
		t.Fatalf("空了的旧分类目录没清")
	}
	if io.gone[doc] {
		t.Fatalf("还有片的旧分类目录被删了")
	}

	// 本地：并进新位置，旧目录与空了的旧分类目录收掉
	newDir := filepath.Join(root, "影视/电影/动画电影/A (2020)")
	if b, _ := os.ReadFile(filepath.Join(newDir, "A.strm")); string(b) != "old" {
		t.Fatalf("同名文件应以旧目录为准，得到 %q", b)
	}
	if _, err := os.Stat(filepath.Join(newDir, "A.nfo")); err != nil {
		t.Fatalf("NFO 没跟过去")
	}
	if _, err := os.Stat(filepath.Join(root, "影视/动画电影")); !os.IsNotExist(err) {
		t.Fatalf("本地旧分类目录还在")
	}
	if _, err := os.Stat(filepath.Join(root, "影视/纪录片/B (2021)/B.strm")); err != nil {
		t.Fatalf("没搬成的本地不该动")
	}

	// 台账与记录
	var sf model.SyncedFile
	model.DB.Where("file_id = ?", "fa").First(&sf)
	if sf.RelPath != "影视/电影/动画电影/A (2020)/A.strm" {
		t.Fatalf("台账没换前缀：%s", sf.RelPath)
	}
	var sf2 model.SyncedFile
	model.DB.Where("file_id = ?", "fa2").First(&sf2)
	if sf2.RelPath != "影视/动画电影/A (2020)_x/Z.strm" {
		t.Fatalf("误改了别的目录：%s", sf2.RelPath)
	}
	var r model.OrganizeRecord
	model.DB.First(&r, rec.ID)
	if r.TargetDir != "电影/动画电影/A (2020)" || r.Category != "电影/动画电影" {
		t.Fatalf("整理记录没改：%s / %s", r.TargetDir, r.Category)
	}
}
