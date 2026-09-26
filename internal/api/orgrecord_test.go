package api

import (
	"sort"
	"strings"
	"testing"

	"115-station/internal/model"
)

// useDefaultRenameTpl 让用例走硬编码降级模板，并在结束后还原全局
// （renameTpl 是包级变量，改了不还会污染同包后面的用例）
func useDefaultRenameTpl(t *testing.T) {
	t.Helper()
	prev := renameTpl
	t.Cleanup(func() { renameTpl = prev })
	renameTpl = nil
}

// 重整理最容易出错的是路径推导：剧集要分季、字幕要跟到视频所在的季目录、
// NFO/封面要留在标题目录。这些算错的后果是文件搬到错位置且本地 STRM 对不上
func TestPlanRedoLayoutTV(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 456, Title: "测试剧集", Year: "2025", MediaType: "tv"}
	files := []orgRecordFile{
		{Fid: "v1", Name: "Test.Show.S01E01.1080p.mkv", Kind: "video"},
		{Fid: "v2", Name: "Test.Show.S02E03.1080p.mkv", Kind: "video"},
		{Fid: "s1", Name: "Test.Show.S01E01.1080p.chs.srt", Kind: "subtitle"},
		{Fid: "n1", Name: "tvshow.nfo", Kind: "meta"},
		{Fid: "j1", Name: "说明.txt", Kind: "junk"},
	}

	plan, err := planRedoLayout(media, "剧集/国产剧", files, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(plan.rootRel, "剧集/国产剧/") {
		t.Fatalf("标题目录应在 剧集/国产剧 之下，实际 %q", plan.rootRel)
	}
	if strings.Contains(strings.TrimPrefix(plan.rootRel, "剧集/国产剧/"), "/") {
		t.Fatalf("标题目录不该含季层，实际 %q", plan.rootRel)
	}

	// 两季分成两个落点，且都挂在标题目录下
	rels := make([]string, 0, len(plan.groups))
	for rel := range plan.groups {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	if len(rels) != 2 {
		t.Fatalf("两季应有两个落点，实际 %d: %v", len(rels), rels)
	}
	for _, rel := range rels {
		if !strings.HasPrefix(rel, plan.rootRel+"/Season ") {
			t.Errorf("落点 %q 不在 %q 的季目录下", rel, plan.rootRel)
		}
	}

	// 字幕必须跟到 S01 那一组，不能掉到 S02 或标题目录
	subRel := ""
	for rel, gfs := range plan.groups {
		for _, f := range gfs {
			if f.Fid == "s1" {
				subRel = rel
			}
		}
	}
	if !strings.HasSuffix(subRel, "Season 01") {
		t.Errorf("字幕应落在 Season 01，实际 %q", subRel)
	}

	// NFO 单独走标题目录；垃圾文件不参与
	if len(plan.metaFiles) != 1 || plan.metaFiles[0].Fid != "n1" {
		t.Errorf("NFO 应进 metaFiles，实际 %+v", plan.metaFiles)
	}
	for _, gfs := range plan.groups {
		for _, f := range gfs {
			if f.Fid == "j1" || f.Fid == "n1" {
				t.Errorf("%s 不该出现在视频/字幕分组里", f.Fid)
			}
		}
	}

	// 改名表要覆盖两个视频（原名与模板名不同）
	if plan.renames["v1"] == "" || plan.renames["v2"] == "" {
		t.Errorf("视频应被改名，实际 %+v", plan.renames)
	}
}

func TestPlanRedoLayoutMovie(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 123, Title: "测试电影", Year: "2024", MediaType: "movie"}
	files := []orgRecordFile{{Fid: "v1", Name: "Test.Movie.2024.2160p.mkv", Kind: "video"}}

	plan, err := planRedoLayout(media, "电影/动作", files, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.groups) != 1 {
		t.Fatalf("电影只该有一个落点，实际 %d", len(plan.groups))
	}
	// 电影没有季层：落点就是标题目录本身
	for rel := range plan.groups {
		if rel != plan.rootRel {
			t.Errorf("电影落点应等于标题目录，%q != %q", rel, plan.rootRel)
		}
	}
	if !strings.HasPrefix(plan.rootRel, "电影/动作/") {
		t.Errorf("标题目录应在 电影/动作 之下，实际 %q", plan.rootRel)
	}
}

// 没有视频文件的记录不能重整理——否则会凭空建出一个空的标题目录
func TestPlanRedoLayoutNoVideo(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 1, Title: "X", Year: "2024", MediaType: "movie"}
	files := []orgRecordFile{{Fid: "n1", Name: "movie.nfo", Kind: "meta"}}

	if _, err := planRedoLayout(media, "电影/动作", files, "", nil); err == nil {
		t.Fatal("没有视频文件时应报错")
	}
}

// 「重新整理到同一个 TMDB 条目」= 原地刷新：网盘一个字节都不该动。
// 把文件移动到它已经在的目录对 115 是未定义行为，失败还会让整次重整理作废
func TestIsInPlaceRedo(t *testing.T) {
	const rel = "电影/动作/海王.2018.{tmdbid=297802}"
	base := func() *model.OrganizeRecord {
		return &model.OrganizeRecord{Status: "success", TargetDir: rel, TargetCid: "3167"}
	}

	if !isInPlaceRedo(base(), rel, nil) {
		t.Error("目标一致且无改名，应判为原地刷新")
	}
	if !isInPlaceRedo(base(), rel, map[string]string{}) {
		t.Error("空改名表同样算无改名")
	}

	// 目标目录变了（换了片、或分类规则改了）→ 必须搬
	if isInPlaceRedo(base(), "电影/科幻/海王.2018.{tmdbid=297802}", nil) {
		t.Error("目标目录不同不该走原地刷新")
	}
	// 有文件要改名 → 不算没变
	if isInPlaceRedo(base(), rel, map[string]string{"111": "新名.mkv"}) {
		t.Error("有改名时不该走原地刷新")
	}
	// 上次没入库成功：文件还在冗余里躺着，必须搬
	for _, st := range []string{"failed", "unrecognized", "exists"} {
		r := base()
		r.Status = st
		if isInPlaceRedo(r, rel, nil) {
			t.Errorf("status=%s 不该走原地刷新", st)
		}
	}
	// 记录里没存旧位置 → 确认不了现状，老实搬一遍
	r := base()
	r.TargetCid = ""
	if isInPlaceRedo(r, rel, nil) {
		t.Error("缺 TargetCid 时不该走原地刷新")
	}
	r = base()
	r.TargetDir = ""
	if isInPlaceRedo(r, "", nil) {
		t.Error("缺 TargetDir 时不该走原地刷新")
	}
}

// 未识别的剧集重新整理：文件名是原始的，要和正常整理同一套解析 ——
// 子目录上的季号、条目目录名上的季号、替换规则都要生效；认不出集号时两集撞名，动网盘之前就拦下
func TestPlanRedoLayoutRawNames(t *testing.T) {
	useDefaultRenameTpl(t)
	media := &TmdbMedia{TmdbID: 789, Title: "某剧", Year: "2024", MediaType: "tv"}
	seasonOf := func(plan *redoLayout, fid string) string {
		for rel, gfs := range plan.groups {
			for _, f := range gfs {
				if f.Fid == fid {
					return rel[strings.LastIndex(rel, "/")+1:]
				}
			}
		}
		return ""
	}

	// 各季都叫 E01.mkv，季号只在子目录上（此前两集都算成 S01E01，改名撞车）
	bySubdir := []orgRecordFile{
		{Fid: "a", Name: "E01.mkv", Kind: "video", Dir: "某剧/Season 1"},
		{Fid: "b", Name: "E01.mkv", Kind: "video", Dir: "某剧/Season 2"},
	}
	plan, err := planRedoLayout(media, "剧集", bySubdir, "某剧/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if seasonOf(plan, "a") != "Season 01" || seasonOf(plan, "b") != "Season 02" {
		t.Fatalf("季号应取自子目录：a=%s b=%s", seasonOf(plan, "a"), seasonOf(plan, "b"))
	}

	// 老记录没有 Dir：条目目录名上的季号兜底
	byEntry := []orgRecordFile{
		{Fid: "a", Name: "E01.mkv", Kind: "video"},
		{Fid: "b", Name: "E02.mkv", Kind: "video"},
	}
	plan, err = planRedoLayout(media, "剧集", byEntry, "某剧.S03/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if seasonOf(plan, "a") != "Season 03" || seasonOf(plan, "b") != "Season 03" {
		t.Fatalf("季号应取自条目目录名：a=%s b=%s", seasonOf(plan, "a"), seasonOf(plan, "b"))
	}

	// 认不出集号：两集会撞名，必须在动网盘之前报错并点名是哪两个文件
	raw := []orgRecordFile{
		{Fid: "a", Name: "某剧 上篇.mkv", Kind: "video"},
		{Fid: "b", Name: "某剧 下篇.mkv", Kind: "video"},
	}
	_, err = planRedoLayout(media, "剧集", raw, "某剧/", nil)
	if err == nil || !strings.Contains(err.Error(), "某剧 上篇.mkv") || !strings.Contains(err.Error(), "替换规则") {
		t.Fatalf("撞名应报错并给出处理办法：%v", err)
	}
	// 配上替换规则就能认出集号
	rules := []ReplaceRule{{From: "上篇", To: "E01"}, {From: "下篇", To: "E02"}}
	plan, err = planRedoLayout(media, "剧集", raw, "某剧/", rules)
	if err != nil {
		t.Fatalf("替换规则应当生效：%v", err)
	}
	if plan.renames["a"] == plan.renames["b"] || plan.renames["a"] == "" {
		t.Fatalf("替换后两集应各有各的名字：%v", plan.renames)
	}

	// 整理改过名的文件（有 Orig）照旧按规范名解析，替换规则不去碰它
	renamed := []orgRecordFile{{Fid: "a", Name: "某剧 - S02E05.mkv", Orig: "raw.mkv", Kind: "video"}}
	plan, err = planRedoLayout(media, "剧集", renamed, "某剧/", []ReplaceRule{{From: "S02", To: "S09"}})
	if err != nil {
		t.Fatal(err)
	}
	if seasonOf(plan, "a") != "Season 02" {
		t.Fatalf("改过名的文件不该再套替换规则：%s", seasonOf(plan, "a"))
	}
}
