package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// 多版本电影：片目级图按视频名写；本地已有的拷过去不重下；目录级的那张收掉

func multiRun(t *testing.T, dir string, force bool) (*titleRun, map[string]int) {
	t.Helper()
	fetches := map[string]int{}
	var mu sync.Mutex
	w := newFileScrapeWriter(nil, force, false)
	w.localRoot = filepath.Dir(dir)
	sess := newScrapeSession(&TmdbClient{}, fileScrapeOpts{WriteImages: true, Force: force}, w, &fakeScrapeReporter{})
	sess.fetch = func(p, size string) ([]byte, string, error) {
		mu.Lock()
		fetches[p]++
		mu.Unlock()
		return []byte("new" + p), "https://img.test" + p, nil
	}
	videos := []scrapeVideo{
		{Name: "片.2015.1080p", Dir: metaDest{Local: dir}},
		{Name: "片.2015.REMUX", Dir: metaDest{Local: dir}},
	}
	r := &titleRun{s: sess, t: scrapeTitle{Kind: "movie", Title: "片", Dir: metaDest{Local: dir}, Videos: videos},
		st: &titleScrapeStat{}, tmdbID: 1}
	r.images([]byte(`{"poster_path":"/p.jpg","backdrop_path":"/f.jpg"}`))
	return r, fetches
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s：%v", filepath.Base(p), err)
	}
	return string(b)
}

func TestMultiVersionArtFresh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "片.2015")
	os.MkdirAll(dir, 0o755)
	_, fetches := multiRun(t, dir, false)
	for _, n := range []string{"片.2015.1080p-poster.jpg", "片.2015.REMUX-poster.jpg"} {
		if got := readFile(t, filepath.Join(dir, n)); got != "new/p.jpg" {
			t.Fatalf("%s 内容 %q", n, got)
		}
	}
	if fetches["/p.jpg"] != 1 || fetches["/f.jpg"] != 1 {
		t.Fatalf("两个版本同一张图只该下一次：%v", fetches)
	}
	if fileExists(filepath.Join(dir, "poster.jpg")) {
		t.Fatal("多版本不该再写目录级 poster.jpg")
	}
}

// 单版本时刮过（目录级 poster.jpg / fanart.jpg），后来又进了一个版本：拷成按视频名的，不重下，收掉目录级的
func TestMultiVersionArtReuseLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "片.2015")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("old-poster"), 0o644)
	os.WriteFile(filepath.Join(dir, "fanart.jpg"), []byte("old-fanart"), 0o644)
	r, fetches := multiRun(t, dir, false)
	if len(fetches) != 0 {
		t.Fatalf("本地已有，不该下载：%v", fetches)
	}
	for _, v := range []string{"片.2015.1080p", "片.2015.REMUX"} {
		if got := readFile(t, filepath.Join(dir, v+"-poster.jpg")); got != "old-poster" {
			t.Fatalf("%s-poster.jpg 应沿用本地海报，得到 %q", v, got)
		}
		if got := readFile(t, filepath.Join(dir, v+"-fanart.jpg")); got != "old-fanart" {
			t.Fatalf("%s-fanart.jpg 应沿用本地背景图，得到 %q", v, got)
		}
	}
	if fileExists(filepath.Join(dir, "poster.jpg")) || fileExists(filepath.Join(dir, "fanart.jpg")) {
		t.Fatal("各版本都有了，目录级的应收掉")
	}
	if r.st.Reused != 4 {
		t.Fatalf("复用计数 %d，预期 4", r.st.Reused)
	}

	// 再刮一遍（只补缺失）：全都有了，零下载
	_, fetches = multiRun(t, dir, false)
	if len(fetches) != 0 {
		t.Fatalf("第二遍不该下载：%v", fetches)
	}
}

// 只有一个版本有按名图（Emby 自己存的），另一个版本从它拷
func TestMultiVersionArtFromSibling(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "片.2015")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "片.2015.1080p-poster.jpg"), []byte("emby"), 0o644)
	_, fetches := multiRun(t, dir, false)
	if fetches["/p.jpg"] != 0 {
		t.Fatalf("海报本地有一份，不该下载：%v", fetches)
	}
	if got := readFile(t, filepath.Join(dir, "片.2015.REMUX-poster.jpg")); got != "emby" {
		t.Fatalf("应从另一个版本拷，得到 %q", got)
	}
}

// 强制覆盖：重新拉（只拉一次），不用本地的
func TestMultiVersionArtForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "片.2015")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("old"), 0o644)
	_, fetches := multiRun(t, dir, true)
	if fetches["/p.jpg"] != 1 {
		t.Fatalf("强制覆盖应重新下一次：%v", fetches)
	}
	if got := readFile(t, filepath.Join(dir, "片.2015.REMUX-poster.jpg")); got != "new/p.jpg" {
		t.Fatalf("强制覆盖应写新图，得到 %q", got)
	}
}

// 海报墙：多版本时目录级海报不算，要每个版本都有
func TestPerVersionArtGrade(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "A-poster.jpg"), []byte("x"), 0o644)
	idx := localDirIndex{}
	d := &localTitleDetail{
		Files:   []localFile{idx.findImage(dir, "海报", "poster", localPosterNames...)},
		Entries: []localEntry{{Name: "A", Rel: "A.strm"}, {Name: "B", Rel: "B.strm"}},
	}
	d.perVersionArt(idx, dir)
	if d.Files[0].Exists || d.Files[0].Name != "B-poster.jpg" {
		t.Fatalf("B 版本没有海报应算缺：%+v", d.Files[0])
	}
	os.WriteFile(filepath.Join(dir, "B-poster.jpg"), []byte("x"), 0o644)
	idx = localDirIndex{}
	d.Files = []localFile{idx.findImage(dir, "海报", "poster", localPosterNames...)}
	d.perVersionArt(idx, dir)
	if !d.Files[0].Exists {
		t.Fatalf("两个版本都有应算有：%+v", d.Files[0])
	}
}

// 单版本照旧写目录级的
func TestSingleVersionArtUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "片.2015")
	os.MkdirAll(dir, 0o755)
	sess := newScrapeSession(&TmdbClient{}, fileScrapeOpts{WriteImages: true}, newFileScrapeWriter(nil, false, false), &fakeScrapeReporter{})
	sess.fetch = func(p, size string) ([]byte, string, error) { return []byte("x"), "u", nil }
	r := &titleRun{s: sess, t: scrapeTitle{Kind: "movie", Dir: metaDest{Local: dir},
		Videos: []scrapeVideo{{Name: "片.2015", Dir: metaDest{Local: dir}}}}, st: &titleScrapeStat{}, tmdbID: 1}
	r.images([]byte(`{"poster_path":"/p.jpg"}`))
	if !fileExists(filepath.Join(dir, "poster.jpg")) || fileExists(filepath.Join(dir, "片.2015-poster.jpg")) {
		t.Fatal("单版本应写目录级 poster.jpg")
	}
}
