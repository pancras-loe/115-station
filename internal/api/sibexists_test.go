package api

import (
	"strings"
	"testing"

	"115-station/internal/model"
)

// 散文件的兄弟集判为已存在后已经离开转存目录，必须汇成一条 exists 记录，
// 此前这些集不进任何记录，记录页里查不到去向
func TestSiblingExistsRecord(t *testing.T) {
	main := OrganizeResult{TmdbID: 7, Title: "剧", Year: "2020", MediaType: "tv", Category: "国产剧", TargetDir: "剧集/剧/Season 01"}
	rec := &model.OrganizeRecord{PosterPath: "/p.jpg"}

	if siblingExistsRecord(main, rec, nil) != nil {
		t.Fatal("没有已存在的集不该出记录")
	}

	ep := func(fid, holding, decision string, withSub bool) sibOutcome {
		files := []orgRecordFile{{Fid: fid, Name: fid + ".mkv", Kind: "video"}}
		if withSub {
			files = append(files, orgRecordFile{Fid: fid + "s", Name: fid + ".srt", Kind: "subtitle"})
		}
		return sibOutcome{result: OrganizeResult{Status: "exists"}, files: files, decision: decision, holding: holding}
	}

	er := siblingExistsRecord(main, rec, []sibOutcome{
		ep("e2", "剧 (2020)", washSameFile, true),
		ep("e3", "剧 (2020)", washSameFile, false),
	})
	if er == nil || er.Status != "exists" || er.SourceFid != "e2" || er.SourceKind != "file" {
		t.Fatalf("记录头不对: %+v", er)
	}
	if er.VideoCount != 2 || len(unmarshalRecordFiles(er.Files)) != 3 {
		t.Fatalf("视频数应为 2、文件含字幕共 3 个: %d / %s", er.VideoCount, er.Files)
	}
	if er.TmdbID != 7 || er.PosterPath != "/p.jpg" || er.TargetDir != main.TargetDir {
		t.Fatalf("识别信息没带上: %+v", er)
	}
	if !strings.Contains(er.Message, washExistsMsg(washSameFile)) || !strings.Contains(er.Message, "已存在/剧 (2020)") {
		t.Fatalf("文案: %s", er.Message)
	}

	mixed := siblingExistsRecord(main, nil, []sibOutcome{
		ep("a", "X", washSameFile, false),
		ep("b", "Y", washNotBetter, false),
	})
	if !strings.Contains(mixed.Message, "库内已有同一份文件或更优版本，2 个视频已移到 已存在") ||
		strings.Contains(mixed.Message, "已存在/") {
		t.Fatalf("混合判定/不同子目录的文案: %s", mixed.Message)
	}
}
