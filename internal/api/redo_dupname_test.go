package api

import (
	"errors"
	"strings"
	"testing"
)

// 2026-10-06 现场：越狱 S04E22 网盘上并排两份同名文件，重新整理整部剧被重名检查拦下
func TestRedoAllowsExistingSameNameCopies(t *testing.T) {
	prev := renameTpl
	renameTpl = nil
	t.Cleanup(func() { renameTpl = prev })
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	name := "越狱 - S04E22.mkv" // 硬编码兜底模板算出来的规范名
	files := []orgRecordFile{
		{Fid: "a", Name: "越狱 - S04E21.mkv", Kind: "video"},
		{Fid: "b", Name: name, Kind: "video"},
		{Fid: "c", Name: name, Kind: "video"},
		{Fid: "c", Name: name, Kind: "video"}, // 快照里重复的同一条
	}
	plan, err := planRedoLayout(media, "剧集", files, "越狱/", nil)
	if err != nil {
		t.Fatalf("已是规范名的两份不该拦：%v", err)
	}
	if len(plan.renames) != 0 {
		t.Fatalf("不该改名：%v", plan.renames)
	}
	n := 0
	for _, g := range plan.groups {
		n += len(g)
	}
	if n != 3 {
		t.Fatalf("重复的快照条目应去掉，得到 %d 个", n)
	}
}

func TestRedoSameEpisodeCopiesNeedRenameMessage(t *testing.T) {
	prev := renameTpl
	renameTpl = nil
	t.Cleanup(func() { renameTpl = prev })
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	files := []orgRecordFile{
		{Fid: "a", Name: "Prison.Break.S04E22.1080p.mkv", Kind: "video", Size: 1 << 30},
		{Fid: "b", Name: "Prison.Break.S04E22.720p.mkv", Kind: "video"},
	}
	_, err := planRedoLayout(media, "剧集", files, "越狱/", nil)
	var de *redoDupError
	if !errors.As(err, &de) || len(de.groups) != 1 || de.groups[0].Episode != "S04E22" || strings.Contains(err.Error(), "替换规则") {
		t.Fatalf("同一集的两份要改名才撞上：交给用户选，不提替换规则：%v", err)
	}
	// 选过之后：留 1080p，720p 移冗余
	plan, err := planRedoLayoutOpt(media, "剧集", files, "越狱/", nil, nil, redoPlanOpts{choice: map[string]string{"a": dupChoiceKeep, "b": dupChoiceDrop}})
	if err != nil || len(plan.drops) != 1 || plan.drops[0].Fid != "b" {
		t.Fatalf("按选择处理：%v %+v", err, plan)
	}
	// 都留：按 #A #B 改名，字母记进文件
	plan, err = planRedoLayoutOpt(media, "剧集", files, "越狱/", nil, nil, redoPlanOpts{choice: map[string]string{"a": "A", "b": "B"}})
	if err != nil || plan.renames["a"] != "越狱 - S04E22#A.mkv" || plan.renames["b"] != "越狱 - S04E22#B.mkv" {
		t.Fatalf("都留：%v %v", err, plan.renames)
	}
	for _, g := range plan.groups {
		for _, f := range g {
			if f.Fid == "b" && f.Variant != "B" {
				t.Fatalf("字母要记进文件：%+v", f)
			}
		}
	}
}

// 库内同集多份体检发起的（strict）：早就同名并排的也要问
func TestRedoStrictAsksForSameNameCopies(t *testing.T) {
	prev := renameTpl
	renameTpl = nil
	t.Cleanup(func() { renameTpl = prev })
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	files := []orgRecordFile{
		{Fid: "b", Name: "越狱 - S04E22.mkv", Kind: "video", Sha1: "B"},
		{Fid: "c", Name: "越狱 - S04E22.mkv", Kind: "video", Sha1: "C"},
	}
	if _, err := planRedoLayoutOpt(media, "剧集", files, "越狱/", nil, nil, redoPlanOpts{}); err != nil {
		t.Fatalf("默认放行：%v", err)
	}
	var de *redoDupError
	if _, err := planRedoLayoutOpt(media, "剧集", files, "越狱/", nil, nil, redoPlanOpts{strict: true}); !errors.As(err, &de) {
		t.Fatalf("strict 要问：%v", err)
	}
}
