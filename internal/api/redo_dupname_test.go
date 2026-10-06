package api

import (
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
	if err == nil || !strings.Contains(err.Error(), "同一集的两份") || strings.Contains(err.Error(), "替换规则") {
		t.Fatalf("应说明是同一集的两份：%v", err)
	}
}
