package api

import (
	"path/filepath"
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
	// NFO：tvshow + season + E01/E02/E03（花絮不算）→ 3/5
	s := d.Summary
	if s.NFOHave != 3 || s.NFOTotal != 5 || s.ThumbHave != 1 || s.ThumbTotal != 3 || s.Subtitled != 1 {
		t.Fatalf("summary: %+v", s)
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

	tk := "影视/综艺/某综艺"
	touch(t, filepath.Join(root, tk, "某综艺.S01E01.strm"), "u")
	d = inspectLocalTitleDetail(root, &ledgerTitleEntry{Key: tk, MediaType: "tv"},
		[]model.SyncedFile{{Kind: "video", RelPath: tk + "/某综艺.S01E01.strm"}})
	if len(d.Seasons) != 1 || d.Seasons[0].NFO != nil || d.Seasons[0].Dir != "" {
		t.Fatalf("平铺的集没有季目录，不该要求 season.nfo: %+v", d.Seasons)
	}
}

func TestEmbyProbeStateOf(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
	mark := func(n int, ago time.Duration) *model.EmbyExtractMark {
		return &model.EmbyExtractMark{ItemID: "x", Attempts: n, LastAt: now.Add(-ago), LastErr: "超时"}
	}
	cases := []struct {
		name            string
		info, extract   bool
		m               *model.EmbyExtractMark
		queued, running bool
		want            string
	}{
		{"有媒体信息就是探过了，哪怕还挂着记账", true, true, mark(1, time.Hour), false, false, "done"},
		{"光盘结构", false, false, nil, true, false, "disc"},
		{"没探过", false, true, nil, false, false, "none"},
		{"没探过、片目在队列里", false, true, nil, true, false, "queued"},
		{"正在探", false, true, mark(1, 0), true, true, "running"},
		{"24 小时内请求过：排着队也不会探", false, true, mark(1, time.Hour), true, false, "wait"},
		{"过了间隔可以再试", false, true, mark(1, 25*time.Hour), false, false, "retry"},
		{"过了间隔且在队列里", false, true, mark(1, 25*time.Hour), true, false, "queued"},
		{"次数用完", false, true, mark(2, 48*time.Hour), true, false, "exhausted"},
	}
	for _, c := range cases {
		got := embyProbeStateOf(c.info, c.extract, c.m, c.queued, c.running, now)
		if got.State != c.want {
			t.Errorf("%s: state=%s，预期 %s", c.name, got.State, c.want)
		}
	}
	if st := embyProbeStateOf(false, true, mark(1, time.Hour), false, false, now); st.RetryAt == nil ||
		!st.RetryAt.Equal(now.Add(23*time.Hour)) || st.Attempts != 1 || st.LastErr != "超时" {
		t.Fatalf("wait 要带上次数、原因与可重试时间: %+v", st)
	}
	if st := embyProbeStateOf(false, true, mark(2, time.Hour), false, false, now); st.RetryAt == nil ||
		!st.RetryAt.Equal(now.Add(embyMarkPruneAfter-time.Hour)) {
		t.Fatalf("exhausted 的 RetryAt 是记账被清掉的时间: %+v", st)
	}
}

func TestEmbyDetailOf(t *testing.T) {
	var it embyExtractItem
	it.ID, it.Type, it.Name, it.ParentIndexNumber, it.IndexNumber = "1", "Episode", "第一集", 1, 2
	it.Path = "/media/影视/剧集/狂飙 (2023)/Season 01/狂飙.S01E02.strm"
	it.RunTimeTicks = 2700 * 10_000_000
	it.MediaSources = append(it.MediaSources, struct {
		Path         string       `json:"Path"`
		Container    string       `json:"Container"`
		Size         int64        `json:"Size"`
		Bitrate      int64        `json:"Bitrate"`
		MediaStreams []embyStream `json:"MediaStreams"`
	}{Container: "mkv", MediaStreams: []embyStream{
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
