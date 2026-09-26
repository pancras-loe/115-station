package api

import (
	"strings"
	"testing"
)

// 现场：合集记录里的两部电影版被用户挪到待整理、单独入了电影库，
// 旧记录重新整理时还把它们算进来，按剧集模板算出同一个名字被重名拦下
func TestRedoSkipsFilesClaimedByNewerRecord(t *testing.T) {
	files := []orgRecordFile{
		{Fid: "e1", Name: "成长的烦恼.S01E01.mkv", Kind: "video"},
		{Fid: "m1", Name: "【电影版】成长的烦恼.The.Growing.Pains.Movie.2000.mkv", Kind: "video"},
	}
	newer := []string{marshalRecordFiles([]orgRecordFile{{Fid: "m1", Name: "成长的烦恼：电影版.2000.mkv", Kind: "video"}}), ""}
	kept, claimed := dropClaimedFiles(files, newer)
	if len(kept) != 1 || kept[0].Fid != "e1" {
		t.Fatalf("被新记录接手的文件应剔除，留下 %+v", kept)
	}
	if len(claimed) != 1 || !strings.Contains(claimed[0], "电影版") {
		t.Fatalf("应报出被接手的文件，得到 %v", claimed)
	}
	if k, c := dropClaimedFiles(files, nil); len(k) != 2 || c != nil {
		t.Fatal("没有更新的记录时原样返回")
	}
}

func TestRedoLayoutPutsEpisodelessIntoSpecials(t *testing.T) {
	media := &TmdbMedia{TmdbID: 54, Title: "成长的烦恼", Year: "1985", MediaType: "tv"}
	files := []orgRecordFile{
		{Fid: "e1", Name: "成长的烦恼.Growing.Pains.S01E001.2160p.mkv", Kind: "video"},
		{Fid: "e2", Name: "成长的烦恼.Growing.Pains.S02E023.2160p.mkv", Kind: "video"},
		{Fid: "m1", Name: "【电影版】成长的烦恼.The.Growing.Pains.Movie.2000.2160p.mkv", Kind: "video"},
		{Fid: "m2", Name: "【电影版】成长的烦恼：希瓦家归来.Growing.Pains.Return.of.the.Seavers.2004.2160p.mkv", Kind: "video"},
	}
	plan, err := planRedoLayout(media, "剧集", files, gpPack+"/", nil)
	if err != nil {
		t.Fatalf("两部没集号的电影版不该再被重名拦下: %v", err)
	}
	where := map[string]string{}
	for rel, gfs := range plan.groups {
		for _, f := range gfs {
			where[f.Fid] = rel
		}
	}
	if where["m1"] == "" || where["m1"] != where["m2"] || !strings.HasPrefix(where["m1"], plan.rootRel+"/") {
		t.Fatalf("电影版应一起进标题目录下的特别篇目录，得到 %v", where)
	}
	if where["m1"] == where["e1"] || where["e1"] == where["e2"] {
		t.Fatalf("正片按季分开、特别篇单独一处，得到 %v", where)
	}
	if _, renamed := plan.renames["m1"]; renamed {
		t.Fatal("没有集号的视频保持原名，不套剧集模板")
	}
}
