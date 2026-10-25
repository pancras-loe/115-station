package api

import (
	"fmt"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 「强制洗版」：新来的画质更差、配置是 skip 都不管，同一集库里的版本全部让位（连同字幕），
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
		t.Fatalf("强制洗版的旧版应进 115 回收站: %q", st.OldVersionTarget)
	}
	plan := decideWash(&TmdbMedia{MediaType: "tv", Title: "某剧"},
		washFile{Name: "某剧.S01E01.720p.HDTV.mkv"}, "NEW", "剧集/国产剧/某剧 (2024)/Season 01",
		st, rows, nil, quiet)
	if plan.decision != washReplaced {
		t.Fatalf("强制洗版应替换，得到 %s", plan.decision)
	}
	got := map[string]bool{}
	for _, v := range plan.victims {
		got[v.FileID] = true
	}
	if !got["e1a"] || !got["e1b"] || !got["e1s"] || got["e2"] || len(got) != 3 {
		t.Fatalf("让位集合不对: %+v", plan.victims)
	}
}

// 强制洗版也只换同一集：库里没有的新集照常入库
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

// 判输时要带出库里比下去它的那份，通知里给用户看
func TestWashNotBetterCarriesOldName(t *testing.T) {
	dir := "库/电影/怒之杀 (2026)"
	rows := []model.SyncedFile{{FileID: "m1", Kind: "video", RelPath: dir + "/怒之杀.2026.WEB-DL.DV.HDR.2160p.H265.strm"}}
	st := &washStrategy{Mode: "replace", PriorityLevel: []washRule{{ResourcePix: "2160p"}}}
	plan := decideWash(&TmdbMedia{MediaType: "movie", Title: "怒之杀"},
		washFile{Name: "怒之杀.2026.1080p.WEB-DL.mkv"}, "NEW", "电影/怒之杀 (2026)", st, rows, nil, quiet)
	if plan.decision != washNotBetter || !strings.Contains(plan.oldName, "2160p") || plan.why == "" {
		t.Fatalf("判输应带出旧版名与理由: %+v", plan)
	}
}

// 只问订阅以外的：订阅的来源链接认得出就不问；认不出来源（磁力秒传还没回填、自己放进去的）算手动
func TestWashAskOnlyManual(t *testing.T) {
	newTestDB(t, "washask.db")
	h := &Handler{DB: model.DB}
	dlLinkRecord(h, "https://115.com/s/sub1", "share", "某剧", dlLinkSourceSub, []string{"某剧 (2024) S01 ·订阅3-1"})
	dlLinkRecord(h, "https://115.com/s/web1", "share", "某片", "web", []string{"某片.2020.1080p"})

	c := &orgCtx{cfg: &OrgConfig{WashAsk: true}}
	if c.washAsk("x", "Season 1", "某剧 (2024) S01 ·订阅3-1") {
		t.Fatal("订阅进来的不该问")
	}
	if !c.washAsk("x", "某片.2020.1080p") {
		t.Fatal("链接转存的应该问")
	}
	if !c.washAsk("x", "没提交过") {
		t.Fatal("认不出来源的按手动算")
	}
	c.top = "某剧 (2024) S01 ·订阅3-1"
	if c.washAsk("x", "Season 1") {
		t.Fatal("容器目录里的子目录要靠顶层条目名认出订阅")
	}
	if (&orgCtx{cfg: &OrgConfig{}}).washAsk("x", "某片.2020.1080p") {
		t.Fatal("开关关着不问")
	}
	if (&orgCtx{cfg: &OrgConfig{WashAsk: true}, washChoice: washChoiceSkip}).washAsk("x", "某片.2020.1080p") {
		t.Fatal("答过了不再问")
	}
}

// 洗版待确认是 sticky：「人工确认」关着也不许自动整理接手
func TestWashHoldIsSticky(t *testing.T) {
	ref := &awaitingRef{id: 1, wash: true}
	c := &orgCtx{cfg: &OrgConfig{}, held: map[string]*awaitingRef{"d": ref}}
	if out := c.dropHeld([]dirEntry{{Fid: "d"}, {Fid: "e"}}); len(out) != 1 || out[0].Fid != "e" {
		t.Fatalf("洗版待确认的条目应被跳过: %+v", out)
	}
}

// 回答：网页可以改主意，机器人只认还没答过的；只收 force / skip，入「确认入库」队列
func TestSubmitWashChoice(t *testing.T) {
	newTestDB(t, "washchoice.db")
	h := &Handler{DB: model.DB}
	items := `[{"fid":"v1","name":"某片.2020.1080p.mkv","old":"某片.2020.2160p.strm","why":"新来的不如库里的（分辨率：1080p < 2160p）"}]`
	rec := model.OrganizeRecord{Source: "某片.2020.1080p.mkv", SourceFid: "v1", SourceKind: "file", Status: orgStatusAwaiting,
		TmdbID: 1, Title: "某片", Year: "2020", MediaType: "movie", HoldWash: true, WashGroups: items}
	if err := model.DB.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.submitWashChoice(&rec, "maybe", "web", true); err == nil {
		t.Fatal("认不出的回答应拒绝")
	}
	if out := h.washTGCallback("w:" + fmt.Sprint(rec.ID) + ":s"); !strings.Contains(out, "按已存在处理") || !strings.Contains(out, "任务 #") {
		t.Fatalf("TG 按钮应提交：%s", out)
	}
	if out := h.washBotCommand("wecom-user", fmt.Sprint(rec.ID)+" 是"); !strings.Contains(out[0], "答过了") {
		t.Fatalf("机器人不能盖掉已有的回答：%v", out)
	}
	var fresh model.OrganizeRecord
	model.DB.First(&fresh, rec.ID)
	if fresh.WashChoice != washChoiceSkip {
		t.Fatalf("回答应记在记录上：%q", fresh.WashChoice)
	}
	if _, err := h.submitWashChoice(&fresh, washChoiceForce, "web", true); err != nil {
		t.Fatalf("网页可以改主意：%v", err)
	}
	model.DB.First(&fresh, rec.ID)
	if fresh.WashChoice != washChoiceForce {
		t.Fatalf("改主意没记上：%q", fresh.WashChoice)
	}
	var jobs []model.TaskJob
	model.DB.Where("kind = ?", "confirm").Find(&jobs)
	if len(jobs) != 1 {
		t.Fatalf("同一条记录只排一个确认任务（去重），得到 %d 个", len(jobs))
	}
	if !strings.Contains(strings.Join(h.washBotCommand("u", ""), "\n"), "没有等着确认") {
		t.Fatal("答过的不该再列出来")
	}
}

func TestWashReplyChoice(t *testing.T) {
	for in, want := range map[string]string{"是": washChoiceForce, "强制": washChoiceForce, "Y": washChoiceForce,
		"否": washChoiceSkip, "不用": washChoiceSkip, "n": washChoiceSkip, "随便": ""} {
		if got := washReplyChoice(in); got != want {
			t.Errorf("%q → %q，期望 %q", in, got, want)
		}
	}
}

// 确认时同一个源目录里别的待确认记录的文件不归这一条
func TestAwaitingOthers(t *testing.T) {
	newTestDB(t, "washothers.db")
	a := model.OrganizeRecord{Source: "剧/", SourceFid: "d", SourceKind: "dir", Status: orgStatusAwaiting, HoldWash: true,
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "e1", Kind: "video"}})}
	b := model.OrganizeRecord{Source: "剧/", SourceFid: "d", SourceKind: "dir", Status: orgStatusAwaiting, HoldDup: true,
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "e2", Kind: "video"}, {Fid: "e3", Kind: "video"}})}
	model.DB.Create(&a)
	model.DB.Create(&b)
	others := awaitingOthers(a.ID)
	if others["e1"] != nil || others["e2"] == nil || others["e3"] == nil {
		t.Fatalf("应只剩另一条的文件: %v", others)
	}
}

// 「洗版 …」是指令，不能被当成别的；「强制覆盖」已经删了
func TestTGWashCommand(t *testing.T) {
	if cmd, args := tgCommand("洗版 12 是"); cmd != "wash" || args != "12 是" {
		t.Fatalf("洗版指令: %s %q", cmd, args)
	}
	if cmd, args := tgCommand("/wash 12 否"); cmd != "wash" || args != "12 否" {
		t.Fatalf("/wash 指令: %s %q", cmd, args)
	}
	if cmd, _ := tgCommand("https://115.com/s/abc"); cmd != "download" {
		t.Fatalf("直接发链接仍是普通转存: %s", cmd)
	}
}
