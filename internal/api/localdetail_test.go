package api

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"115-station/internal/model"
)

// 剧集详情：季目录里的 season.nfo、季海报、每集 NFO / 剧照 / 外挂字幕，与刮削同一套落点
func TestInspectLocalTitleDetailTV(t *testing.T) {
	root := t.TempDir()
	key := "影视/剧集/狂飙 (2023)"
	dir := filepath.Join(root, filepath.FromSlash(key))
	touch(t, filepath.Join(dir, "tvshow.nfo"), "x")
	touch(t, filepath.Join(dir, "poster.jpg"), "x")
	touch(t, filepath.Join(dir, "season01-poster.jpg"), "x")
	touch(t, filepath.Join(dir, "Season 01/season.nfo"), "x")
	touch(t, filepath.Join(dir, "Season 01/狂飙.S01E01.strm"), "u")
	touch(t, filepath.Join(dir, "Season 01/狂飙.S01E01.nfo"), "x")
	touch(t, filepath.Join(dir, "Season 01/狂飙.S01E01-thumb.jpg"), "x")
	touch(t, filepath.Join(dir, "Season 01/狂飙.S01E01.chs.ass"), "x")
	touch(t, filepath.Join(dir, "Season 01/狂飙.S01E02.strm"), "u")
	// 没有集号的视频：刮削不写它的 NFO，不计入分母
	touch(t, filepath.Join(dir, "Season 01/花絮.strm"), "u")

	rows := []model.SyncedFile{
		{Kind: "video", RelPath: key + "/Season 01/狂飙.S01E02.strm", Size: 2 << 30},
		{Kind: "video", RelPath: key + "/Season 01/狂飙.S01E01.strm"},
		{Kind: "video", RelPath: key + "/Season 01/花絮.strm"},
		{Kind: "video", RelPath: key + "/Season 01/狂飙.S01E03.strm"}, // 本地 STRM 不在
	}
	d := inspectLocalTitleDetail(root, &ledgerTitleEntry{Key: key, MediaType: "tv"}, rows)

	if len(d.Entries) != 4 || d.Entries[0].Episode != 0 || d.Entries[1].Episode != 1 || d.Entries[3].Episode != 3 {
		t.Fatalf("按季集排序（无集号在前）: %+v", d.Entries)
	}
	e1 := d.Entries[1]
	if !e1.NFO.Exists || e1.Thumb == nil || !e1.Thumb.Exists || len(e1.Subtitles) != 1 || e1.Subtitles[0] != "狂飙.S01E01.chs.ass" {
		t.Fatalf("E01: %+v", e1)
	}
	if e2 := d.Entries[2]; e2.NFO.Exists || e2.Thumb == nil || e2.Thumb.Exists || e2.Size != 2<<30 || e2.StrmMissing {
		t.Fatalf("E02: %+v", e2)
	}
	if !d.Entries[3].StrmMissing {
		t.Fatalf("E03 本地 STRM 不在应标出来")
	}
	if d.Entries[0].Thumb != nil {
		t.Fatalf("没有集号的视频不该有剧照项")
	}
	if len(d.Seasons) != 1 {
		t.Fatalf("seasons: %+v", d.Seasons)
	}
	sn := d.Seasons[0]
	if sn.Season != 1 || sn.Dir != "Season 01" || sn.NFO == nil || !sn.NFO.Exists || !sn.Poster.Exists || sn.Videos != 3 {
		t.Fatalf("season: %+v", sn)
	}
	// NFO：tvshow + season + E01/E02（花絮没集号、E03 本地 STRM 不在，都不算）→ 3/4
	s := d.Summary
	if s.NFOHave != 3 || s.NFOTotal != 4 || s.ThumbHave != 1 || s.ThumbTotal != 2 || s.Subtitled != 1 {
		t.Fatalf("summary: %+v", s)
	}
	// 卡片状态与详情同口径：根目录有 tvshow.nfo 与海报也不算刮全（此前就是这样显示成「已刮削」的）
	if d.Status != "partial" || !reflect.DeepEqual(d.Lack, []string{"背景图", "1 集 NFO"}) ||
		!reflect.DeepEqual(d.Soft, []string{"1 集剧照"}) {
		t.Fatalf("grade: %s lack=%v soft=%v", d.Status, d.Lack, d.Soft)
	}
	// 图片：海报、背景图、Logo、横版图 + 季海报 → 2/5
	if s.ImgHave != 2 || s.ImgTotal != 5 {
		t.Fatalf("images: %+v", s)
	}
}

// 电影：NFO 与视频同基名；集文件平铺在标题目录下时没有季目录可写 season.nfo
func TestInspectLocalTitleDetailMovieAndFlatSeason(t *testing.T) {
	root := t.TempDir()
	mk := "影视/电影/流浪地球 (2019)"
	touch(t, filepath.Join(root, mk, "流浪地球.strm"), "u")
	touch(t, filepath.Join(root, mk, "流浪地球.nfo"), "x")
	touch(t, filepath.Join(root, mk, "Folder.JPG"), "x")
	touch(t, filepath.Join(root, mk, "fanart.jpg"), "x")
	d := inspectLocalTitleDetail(root, &ledgerTitleEntry{Key: mk, MediaType: "movie"},
		[]model.SyncedFile{{Kind: "video", RelPath: mk + "/流浪地球.strm"}})
	if len(d.Files) != 4 || !d.Files[0].Exists || d.Files[0].Name != "Folder.JPG" || !d.Files[1].Exists {
		t.Fatalf("movie files: %+v", d.Files)
	}
	if d.fanartV == 0 || len(d.Entries) != 1 || !d.Entries[0].NFO.Exists || d.Entries[0].Thumb != nil || d.Seasons != nil {
		t.Fatalf("movie: %+v", d)
	}
	// Logo / 横版图缺了不影响「已刮削」
	if d.Status != "ok" || d.Lack != nil || d.Soft != nil {
		t.Fatalf("movie grade: %s %v %v", d.Status, d.Lack, d.Soft)
	}

	// 别的刮削器写的 movie.nfo 同样算；缺背景图是 partial
	mk2 := "影视/电影/老片 (2001)"
	touch(t, filepath.Join(root, mk2, "老片.strm"), "u")
	touch(t, filepath.Join(root, mk2, "movie.nfo"), "x")
	touch(t, filepath.Join(root, mk2, "poster.jpg"), "x")
	d = inspectLocalTitleDetail(root, &ledgerTitleEntry{Key: mk2, MediaType: "movie"},
		[]model.SyncedFile{{Kind: "video", RelPath: mk2 + "/老片.strm"}})
	if !d.Entries[0].NFO.Exists || d.Status != "partial" || !reflect.DeepEqual(d.Lack, []string{"背景图"}) {
		t.Fatalf("movie.nfo: %s %v", d.Status, d.Lack)
	}

	tk := "影视/综艺/某综艺"
	touch(t, filepath.Join(root, tk, "某综艺.S01E01.strm"), "u")
	d = inspectLocalTitleDetail(root, &ledgerTitleEntry{Key: tk, MediaType: "tv"},
		[]model.SyncedFile{{Kind: "video", RelPath: tk + "/某综艺.S01E01.strm"}})
	if len(d.Seasons) != 1 || d.Seasons[0].NFO != nil || d.Seasons[0].Dir != "" {
		t.Fatalf("平铺的集没有季目录，不该要求 season.nfo: %+v", d.Seasons)
	}
	if d.Status != "miss" || !reflect.DeepEqual(d.Lack, []string{"剧集 NFO", "海报", "背景图", "1 集 NFO"}) {
		t.Fatalf("什么都没刮: %s %v", d.Status, d.Lack)
	}
}

func TestEmbyProbeStateOf(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	mark := func(n int, ago time.Duration) *model.EmbyExtractMark {
		return &model.EmbyExtractMark{ItemID: "x", Attempts: n, LastAt: now.Add(-ago), LastErr: "超时"}
	}
	const (
		no  = probeQueuedNone
		au  = probeQueuedAuto
		man = probeQueuedManual
	)
	cases := []struct {
		name          string
		info, extract bool
		m             *model.EmbyExtractMark
		queue         string
		running       bool
		want          string
	}{
		{"有媒体信息就是探过了，哪怕还挂着记账", true, true, mark(1, time.Hour), no, false, "done"},
		{"光盘结构", false, false, nil, au, false, "disc"},
		{"没探过", false, true, nil, no, false, "none"},
		{"没探过、片目在队列里", false, true, nil, au, false, "queued"},
		{"正在探", false, true, mark(1, 0), au, true, "running"},
		{"24 小时内请求过：自动排着也不会探", false, true, mark(1, time.Hour), au, false, "wait"},
		{"过了间隔可以再试", false, true, mark(1, 25*time.Hour), no, false, "retry"},
		{"过了间隔且在队列里", false, true, mark(1, 25*time.Hour), au, false, "queued"},
		{"次数用完：自动排着也不探", false, true, mark(2, 48*time.Hour), au, false, "exhausted"},
		{"次数用完、手动排着：会探", false, true, mark(2, 48*time.Hour), man, false, "queued"},
		{"手动排着但防抖没过：不会探", false, true, mark(1, time.Minute), man, false, "wait"},
	}
	for _, c := range cases {
		got := embyProbeStateOf(c.info, c.extract, c.m, c.queue, c.running, now)
		if got.State != c.want {
			t.Errorf("%s: state=%s，预期 %s", c.name, got.State, c.want)
		}
	}
	if st := embyProbeStateOf(false, true, mark(1, time.Hour), no, false, now); st.RetryAt == nil ||
		!st.RetryAt.Equal(now.Add(23*time.Hour)) || st.Attempts != 1 || st.LastErr != "超时" || st.ManualAt != nil {
		t.Fatalf("wait 要带上次数、原因与自动可重试时间，防抖已过不带 ManualAt: %+v", st)
	}
	if st := embyProbeStateOf(false, true, mark(2, time.Hour), no, false, now); st.RetryAt != nil || !st.manualOK() {
		t.Fatalf("exhausted 不再有自动重试时间，但可以手动: %+v", st)
	}
	if st := embyProbeStateOf(false, true, mark(2, time.Minute), no, false, now); st.ManualAt == nil ||
		!st.ManualAt.Equal(now.Add(embyExtractDebounce-time.Minute)) || st.manualOK() {
		t.Fatalf("防抖中要给出能手动请求的时间: %+v", st)
	}
}

func TestEmbyDetailOf(t *testing.T) {
	var it embyExtractItem
	it.ID, it.Type, it.Name, it.ParentIndexNumber, it.IndexNumber = "1", "Episode", "第一集", 1, 2
	it.Path = "/media/影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E02.strm"
	it.RunTimeTicks = 2700 * 10_000_000
	it.MediaSources = append(it.MediaSources, embyMediaSource{Container: "mkv", MediaStreams: []embyStream{
		{Type: "Video", Codec: "hevc", Height: 2160, VideoRange: "HDR", ExtendedVideoType: "DolbyVision"},
		{Type: "Audio", Codec: "eac3", Channels: 6, Language: "chi"},
		{Type: "Subtitle", Codec: "ass", IsExternal: true},
	}})
	// 映射留空：Emby 路径原样当本地路径（映射本身由 embyPathToLocal 的测试管）
	d := embyDetailOf(it, filepath.FromSlash("/media/影视/剧集/狂飙 (2023)"), "")
	if d.Rel != "Season 01/狂飙.S01E02.strm" || d.Season != 1 || d.Episode != 2 || d.Runtime != 2700 || d.Container != "mkv" {
		t.Fatalf("detail: %+v", d)
	}
	if !d.HasInfo || len(d.Video) != 1 || d.Video[0].Range != "DolbyVision" || len(d.Audio) != 1 || len(d.Subtitles) != 1 || !d.Subtitles[0].External {
		t.Fatalf("tracks: %+v", d)
	}
}
