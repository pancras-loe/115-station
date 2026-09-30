package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"115-station/internal/model"
)

func TestDiscPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/media/某片/BD.iso.strm":                 true, // 旧命名
		"/media/某片/BD.strm":                     false,
		"/media/某片/BDMV/STREAM/00000.m2ts":      true,
		"/media/某片/VIDEO_TS/VTS_01_1.VOB":       true,
		"http://h:6086/d/abc.iso":               true,
		"http://h:6086/d/abc.ISO?/某片.iso":       true,
		"http://h:6086/d/abc?/某片.iso":           true, // pick_code_name 关了保留后缀
		"http://h:6086/d/abc":                   false,
		"http://h:6086/d/abc.mkv?/某片.mkv":       false,
		"http://h:6086/d/abc.mkv?/iso合集/某片.mkv": false,
	} {
		if got := discPath(p); got != want {
			t.Errorf("discPath(%q) = %v，期望 %v", p, got, want)
		}
	}
}

// 本地 STRM 里的直链：条目路径（disc.strm）与 Emby 记的旧直链都看不出是 ISO 时，靠它认
func TestProbeSourcesReadsLocalStrm(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.ToSlash(filepath.Join(dir, name))
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
		return p
	}
	iso := write("BD.strm", "http://h:6086/d/abc.iso")
	mkv := write("Remux.strm", "http://h:6086/d/def.mkv")
	old := write("Old.strm", "http://h:6086/d/ghi") // 还没补 .iso 的存量：本地判断不了，照常探

	single := embyExtractItem{ID: "1", Path: iso, MediaSources: []embyMediaSource{{ID: "s", Path: "http://h:6086/d/abc"}}}
	if single.needsProbe("") {
		t.Fatal("本地 STRM 直链带 .iso：不该探")
	}
	if !(embyExtractItem{ID: "2", Path: old}).needsProbe("") {
		t.Fatal("直链看不出是 ISO：照常探")
	}

	// 多版本：ISO 那个不探，mkv 那个照探
	multi := embyExtractItem{ID: "3", Path: iso, MediaSources: []embyMediaSource{{ID: "a", Path: iso}, {ID: "b", Path: mkv}}}
	got := multi.probeSources("")
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("多版本应只探 mkv 版本，得到 %+v", got)
	}
	rows := embyDetailsOf(multi, dir, "")
	if len(rows) != 2 || !rows[0].disc || rows[1].disc {
		t.Fatalf("详情按版本标光盘结构：ISO 行 disc、mkv 行不是，得到 %+v", rows)
	}
	// mkv 探过之后，只剩 ISO 缺媒体信息：整个条目不再探
	multi.MediaSources[1].MediaStreams = []embyStream{{Type: "Video"}, {Type: "Audio"}}
	if multi.needsProbe("") {
		t.Fatal("只剩 ISO 版本缺媒体信息：不该再探")
	}
}

func TestWriteStrmISOAlwaysKeepsExt(t *testing.T) {
	root := t.TempDir()
	iso := remoteFile{PickCode: "abc", Name: "某片.iso", Path: "电影/某片"}
	mkv := remoteFile{PickCode: "def", Name: "某片.mkv", Path: "电影/某片"}

	rel, _, err := writeStrmNamed(root, "http://h:6086", "pick_code", false, false, iso, "某片.strm")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := readRel(t, root, rel); b != "http://h:6086/d/abc.iso" {
		t.Fatalf("关了保留后缀的 ISO 也要带 .iso，得到 %q", b)
	}
	rel, _, _ = writeStrmNamed(root, "http://h:6086", "pick_code", false, false, mkv, "某片 mkv.strm")
	if b, _ := readRel(t, root, rel); b != "http://h:6086/d/def" {
		t.Fatalf("非 ISO 照开关来，得到 %q", b)
	}

	// 跳过已存在：还没带 .iso 的 ISO 照写，其他的照跳
	p := filepath.Join(root, "电影", "某片", "某片.strm")
	os.WriteFile(p, []byte("http://h:6086/d/abc"), 0o666)
	if _, wrote, _ := writeStrmNamed(root, "http://h:6086", "pick_code", false, true, iso, "某片.strm"); !wrote {
		t.Fatal("没带 .iso 的 ISO 要无视「跳过已存在」重写")
	}
	os.WriteFile(p, []byte("http://旧域名/d/abc.iso"), 0o666)
	if _, wrote, _ := writeStrmNamed(root, "http://h:6086", "pick_code", false, true, iso, "某片.strm"); wrote {
		t.Fatal("已带 .iso 的照「跳过已存在」跳过")
	}
}

func TestStrmISOFix(t *testing.T) {
	for in, want := range map[string]struct {
		fixed   string
		unknown bool
	}{
		"http://h:6086/d/abc?/某片.iso":     {fixed: "http://h:6086/d/abc.iso?/某片.iso"},
		"http://h:6086/d/abc?/某片.ISO":     {fixed: "http://h:6086/d/abc.ISO?/某片.ISO"},
		"http://h:6086/d/abc.iso?/某片.iso": {},
		"http://h:6086/d/abc?/某片.mkv":     {},
		"http://h:6086/d/abc.iso":         {},
		"http://h:6086/d/abc.mkv":         {},
		"http://h:6086/d/abc":             {unknown: true},
		"http://h:6086/d/123/某片.iso":      {}, // 旧版 fid 直链：文件名本来就在路径上
		"https://别的站/x.iso":               {},
	} {
		fixed, unknown := strmISOFix(in)
		if fixed != want.fixed || unknown != want.unknown {
			t.Errorf("strmISOFix(%q) = (%q, %v)，期望 (%q, %v)", in, fixed, unknown, want.fixed, want.unknown)
		}
	}
}

func TestMigrateStrmISOExt(t *testing.T) {
	const dir = "电影/某片"
	deepDelTestDB(t, []model.SyncedFile{
		{FileID: "1", RelPath: dir + "/碟.strm", Kind: "video"},
		{FileID: "2", RelPath: dir + "/正片.strm", Kind: "video"},
		{FileID: "3", RelPath: dir + "/短链.strm", Kind: "video"},
		{FileID: "4", RelPath: dir + "/已丢.strm", Kind: "video"}, // 本地没有：跳过
		{FileID: "5", RelPath: dir + "/碟.nfo", Kind: "asset"},
	})
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "电影", "某片"), 0o777)
	for name, body := range map[string]string{
		"碟.strm":  "http://h:6086/d/abc?/碟.iso\n",
		"正片.strm": "http://h:6086/d/def?/正片.mkv",
		"短链.strm": "http://h:6086/d/ghi",
		"碟.nfo":   "http://h:6086/d/abc?/碟.iso", // 附属文件不碰
	} {
		os.WriteFile(filepath.Join(root, "电影", "某片", name), []byte(body), 0o666)
	}

	st, err := migrateStrmISOExt(model.DB, root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Seen != 3 || st.Fixed != 1 || st.Unknown != 1 || st.Failed != 0 {
		t.Fatalf("统计不对: %+v", st)
	}
	for name, want := range map[string]string{
		"碟.strm":  "http://h:6086/d/abc.iso?/碟.iso",
		"正片.strm": "http://h:6086/d/def?/正片.mkv",
		"短链.strm": "http://h:6086/d/ghi",
		"碟.nfo":   "http://h:6086/d/abc?/碟.iso",
	} {
		if got, _ := readRel(t, root, dir+"/"+name); got != want {
			t.Errorf("%s 应为 %q，得到 %q", name, want, got)
		}
	}
	// 幂等：再跑一遍什么都不改
	if st, _ := migrateStrmISOExt(model.DB, root); st.Fixed != 0 {
		t.Fatalf("第二遍不该再改: %+v", st)
	}
	// 本地一个都读不到（挂载未就绪）：报错，不打完成标记
	if _, err := migrateStrmISOExt(model.DB, filepath.Join(root, "不存在")); err == nil || !strings.Contains(err.Error(), "读不到") {
		t.Fatalf("本地读不到任何 STRM 时应报错，得到 %v", err)
	}
}

// 起播耗时日志里的媒体源摘要：STRM 名不带扩展名时，容器从本地 STRM 的直链里读
func TestDescribePlaybackSources(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.ToSlash(filepath.Join(dir, "BD.strm"))
	os.WriteFile(iso, []byte("http://h:6086/d/abc.iso?/BD.iso"), 0o666)
	got := describePlaybackSources(nil, nil, []interface{}{
		map[string]interface{}{"Path": iso, "Container": "strm"},
		map[string]interface{}{"Path": "http://h:6086/d/def.mkv", "MediaStreams": []interface{}{
			map[string]interface{}{"Type": "Video"}, map[string]interface{}{"Type": "Audio"},
			map[string]interface{}{"Type": "Audio"}, map[string]interface{}{"Type": "Subtitle"},
		}},
	})
	if got != "iso 无轨道、mkv 视频1/音频2/字幕1" {
		t.Fatalf("摘要不对: %q", got)
	}
}
