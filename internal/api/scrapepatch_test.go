package api

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 只补缺失 + 轨道探测：已有 NFO 就地补 streamdetails ----

const oldEpisodeNFO = `<?xml version="1.0" encoding="UTF-8"?>
<episodedetails>
  <title>手改过的标题</title>
  <season>1</season>
  <episode>1</episode>
</episodedetails>
`

func testProbe() *probeResult {
	return &probeResult{Duration: 1420, Streams: []probeTrack{
		{Kind: "video", Codec: "hevc", Width: 1920, Height: 1080},
		{Kind: "audio", Codec: "aac", Language: "jpn", Channels: 2},
	}}
}

func TestInsertNFOFileinfo(t *testing.T) {
	out, err := insertNFOFileinfo([]byte(oldEpisodeNFO), "episodedetails", nfoFileInfoFrom(testProbe()))
	if err != nil {
		t.Fatal(err)
	}
	var got nfoEpisode
	if err := xml.Unmarshal(out, &got); err != nil {
		t.Fatalf("补完应仍是合法 XML: %v\n%s", err, out)
	}
	if got.Title != "手改过的标题" || got.Episode != 1 {
		t.Fatalf("原有内容不该被改: %+v", got)
	}
	sd := got.Fileinfo
	if sd == nil || sd.StreamDetails == nil || sd.StreamDetails.Video == nil ||
		sd.StreamDetails.Video.Codec != "hevc" || len(sd.StreamDetails.Audio) != 1 {
		t.Fatalf("streamdetails 没补进去:\n%s", out)
	}
	if !strings.Contains(string(out), "\n  <fileinfo>\n    <streamdetails>") {
		t.Fatalf("缩进应和原文一致:\n%s", out)
	}
	if _, err := insertNFOFileinfo([]byte("<movie></movie>"), "episodedetails", sd); err == nil {
		t.Fatal("根元素对不上应报错，不能乱插")
	}
}

func TestPatchStreamsOnExistingNFO(t *testing.T) {
	dir := t.TempDir()
	v := scrapeVideo{Name: "S01E01", PickCode: "pc1", Dir: metaDest{Local: dir}}
	nfoPath := filepath.Join(dir, "S01E01.nfo")

	run := func(force, probeOn bool) (patched bool, probes int, st *titleScrapeStat) {
		w := newFileScrapeWriter(nil, force, false)
		sess := newScrapeSession(&TmdbClient{}, fileScrapeOpts{Probe: probeOn}, w, &fakeScrapeReporter{})
		sess.probe = func(string) (*probeResult, bool, string) {
			probes++
			return testProbe(), false, ""
		}
		r := &titleRun{s: sess, t: scrapeTitle{Kind: "tv", Title: "花名"}, st: &titleScrapeStat{}}
		patched = r.patchStreams(v.Dir, "S01E01.nfo", "episodedetails", v)
		return patched, probes, r.st
	}

	must(t, os.WriteFile(nfoPath, []byte(oldEpisodeNFO), 0o644))
	if ok, n, _ := run(false, false); ok || n != 0 {
		t.Fatal("探测没开：不该动已有 NFO")
	}
	if ok, n, _ := run(true, true); ok || n != 0 {
		t.Fatal("强制覆盖：交给整份重写，这里不插手")
	}

	ok, n, st := run(false, true)
	if !ok || n != 1 || len(st.Wrote) != 1 || st.Skipped != 0 {
		t.Fatalf("已有 NFO 缺轨道：应探测一次并补上，得到 ok=%v probes=%d stat=%+v", ok, n, st)
	}
	b, _ := os.ReadFile(nfoPath)
	if !strings.Contains(string(b), "<streamdetails>") || !strings.Contains(string(b), "手改过的标题") {
		t.Fatalf("补完的 NFO 不对:\n%s", b)
	}

	// 再刮一次：已经有 fileinfo 了，不探测、不重复插
	if ok, n, _ := run(false, true); ok || n != 0 {
		t.Fatal("已有轨道信息的 NFO 不该再探测")
	}
	if b2, _ := os.ReadFile(nfoPath); strings.Count(string(b2), "<fileinfo>") != 1 {
		t.Fatalf("fileinfo 只能有一份:\n%s", b2)
	}
}
