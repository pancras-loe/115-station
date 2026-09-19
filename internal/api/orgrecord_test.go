package api

import (
	"sort"
	"strings"
	"testing"

	"strmhub/internal/model"
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

	plan, err := planRedoLayout(media, "国产剧", files)
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

	plan, err := planRedoLayout(media, "动作", files)
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

	if _, err := planRedoLayout(media, "动作", files); err == nil {
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
