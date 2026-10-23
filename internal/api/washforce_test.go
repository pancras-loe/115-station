package api

import (
	"testing"

	"115-station/internal/model"
)

// 「强制覆盖」：新来的画质更差、配置是 skip 都不管，同一集库里的版本全部让位（连同字幕），
// 别的集不动；去向固定 115 回收站
func TestForceWashReplacesSameEpisodeRegardless(t *testing.T) {
	season := "库/剧集/国产剧/某剧 (2024)/Season 01"
	rows := []model.SyncedFile{
		{FileID: "e1a", Kind: "video", RelPath: season + "/某剧 - S01E01 - 2160p.WEB-DL.strm"},
		{FileID: "e1b", Kind: "video", RelPath: season + "/某剧 - S01E01 - 1080p.WEB-DL.strm"},
		{FileID: "e1s", Kind: "subtitle", RelPath: season + "/某剧 - S01E01 - 2160p.WEB-DL.chs.ass"},
		{FileID: "e2", Kind: "video", RelPath: season + "/某剧 - S01E02 - 2160p.WEB-DL.strm"},
	}
	st := forceWashStrategy()
	if st.OldVersionTarget != "delete" {
		t.Fatalf("强制覆盖的旧版应进 115 回收站: %q", st.OldVersionTarget)
	}
	plan := decideWash(&TmdbMedia{MediaType: "tv", Title: "某剧"},
		washFile{Name: "某剧.S01E01.720p.HDTV.mkv"}, "NEW", "剧集/国产剧/某剧 (2024)/Season 01",
		st, rows, nil, quiet)
	if plan.decision != washReplaced {
		t.Fatalf("强制覆盖应替换，得到 %s", plan.decision)
	}
	got := map[string]bool{}
	for _, v := range plan.victims {
		got[v.FileID] = true
	}
	if !got["e1a"] || !got["e1b"] || !got["e1s"] || got["e2"] || len(got) != 3 {
		t.Fatalf("让位集合不对: %+v", plan.victims)
	}
}

// 强制覆盖也只换同一集：库里没有的新集照常入库
func TestForceWashNewEpisodeJustAdds(t *testing.T) {
	season := "库/剧集/国产剧/某剧 (2024)/Season 01"
	rows := []model.SyncedFile{{FileID: "e1", Kind: "video", RelPath: season + "/某剧 - S01E01.strm"}}
	plan := decideWash(&TmdbMedia{MediaType: "tv", Title: "某剧"},
		washFile{Name: "某剧.S01E05.mkv"}, "", "剧集/国产剧/某剧 (2024)/Season 01",
		forceWashStrategy(), rows, nil, quiet)
	if plan.decision != washSkip || len(plan.victims) != 0 {
		t.Fatalf("新集不该顶掉任何东西: %+v", plan)
	}
}

// 电影：标题目录里的版本全部让位；目标目录里已经是同一份文件的仍按已存在处理
func TestForceWashMovie(t *testing.T) {
	dir := "库/电影/某片 (2020)"
	rows := []model.SyncedFile{
		{FileID: "m1", Kind: "video", Sha1: "OLD1", RelPath: dir + "/某片 (2020) - 2160p.strm"},
		{FileID: "m2", Kind: "video", Sha1: "OLD2", RelPath: dir + "/某片 (2020) - 1080p.strm"},
	}
	plan := decideWash(&TmdbMedia{MediaType: "movie", Title: "某片"},
		washFile{Name: "某片.2020.480p.mkv"}, "NEW", "电影/某片 (2020)", forceWashStrategy(), rows, nil, quiet)
	if plan.decision != washReplaced || len(plan.victims) != 2 {
		t.Fatalf("电影的版本应全部让位: %+v", plan)
	}
	same := decideWash(&TmdbMedia{MediaType: "movie", Title: "某片"},
		washFile{Name: "某片.2020.2160p.mkv"}, "OLD1", "电影/某片 (2020)", forceWashStrategy(), rows, rows[:1], quiet)
	if same.decision != washSameFile {
		t.Fatalf("同一份文件不必再换: %s", same.decision)
	}
}

// 来源链接上的强制标记：按顶层条目名认（容器里的子目录名对不上时靠上层名）
func TestDlLinkForced(t *testing.T) {
	newTestDB(t, "force.db")
	h := &Handler{DB: model.DB}
	if dlLinkRecordOpt(h, "https://115.com/s/abc", "share", "某剧", "机器人", []string{"某剧 合集"}, true) == 0 {
		t.Fatal("登记失败")
	}
	dlLinkRecord(h, "https://115.com/s/def", "share", "别的", "机器人", []string{"别的片"})
	if !dlLinkForced(model.DB, "x", "Season 1", "某剧 合集") {
		t.Fatal("应认出强制覆盖的来源")
	}
	if dlLinkForced(model.DB, "x", "别的片") {
		t.Fatal("普通转存不该被当成强制覆盖")
	}
	if dlLinkForced(model.DB, "x", "没提交过") {
		t.Fatal("认不出来源时不该强制")
	}
}

// 「强制覆盖 链接」不能被当成普通的直接发链接
func TestTGForceCommand(t *testing.T) {
	for _, in := range []string{"强制覆盖 https://115.com/s/abc?password=1234", "/force https://115.com/s/abc"} {
		if cmd, args := tgCommand(in); cmd != "force" || args != "https://115.com/s/abc"+map[bool]string{true: "?password=1234"}[in[0] != '/'] {
			t.Fatalf("%q → %s %q", in, cmd, args)
		}
	}
	if cmd, _ := tgCommand("https://115.com/s/abc"); cmd != "download" {
		t.Fatalf("直接发链接仍是普通转存: %s", cmd)
	}
}
