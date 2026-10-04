package api

import (
	"fmt"
	"testing"
)

// 2026-10-04 现场：「蜡笔小新第二季-NNN.mp4」873 集里 -480 / -576 / -720 被当成分辨率，
// 没了集号进了 Season 0。兄弟视频都是同一模板时，这段数字就是集号
func TestEpisodeParsesFillsResolutionLikeNumbers(t *testing.T) {
	var vids []remoteFile
	for _, n := range []int{1, 2, 479, 480, 481, 576, 719, 720, 721} {
		vids = append(vids, remoteFile{Fid: fmt.Sprint(n), Name: fmt.Sprintf("蜡笔小新第二季-%d.mp4", n)})
	}
	eps := episodeParses(vids, nil, nil)
	for _, n := range []int{480, 576, 720} {
		p := eps[fmt.Sprint(n)]
		if p.Episode != n || p.Season != 2 {
			t.Errorf("-%d 应解析为 S02E%d，实际 S%02dE%d", n, n, p.Season, p.Episode)
		}
	}
}

// 单独一个「某剧 - 1080.mkv」没有兄弟模板撑腰，仍按分辨率处理
func TestEpisodeParsesKeepsLoneResolution(t *testing.T) {
	vids := []remoteFile{
		{Fid: "a", Name: "某剧 - 1080.mkv"},
		{Fid: "b", Name: "某剧 S01E01.mkv"},
		{Fid: "c", Name: "某剧 S01E02.mkv"},
		{Fid: "d", Name: "某剧 S01E03.mkv"},
	}
	if p := episodeParses(vids, nil, nil)["a"]; p.Episode != 0 {
		t.Fatalf("不同模板不该被补集号，实际 E%d", p.Episode)
	}
}

// 那一集已经有文件（重复的两份）：不补，免得两份算出同一个名字、批量改名撞名
func TestEpisodeParsesFillSkipsTakenEpisode(t *testing.T) {
	vids := []remoteFile{
		{Fid: "a", Name: "蜡笔小新第二季-1.mp4"},
		{Fid: "b", Name: "蜡笔小新第二季-2.mp4"},
		{Fid: "c", Name: "蜡笔小新第二季-3.mp4"},
		{Fid: "d", Name: "蜡笔小新第二季 E720.mp4"},
		{Fid: "e", Name: "蜡笔小新第二季-720.mp4"},
	}
	eps := episodeParses(vids, nil, nil)
	if eps["d"].Episode != 720 {
		t.Fatalf("前提：E720 应解析出集号，实际 %+v", *eps["d"])
	}
	if eps["e"].Episode != 0 {
		t.Fatalf("E720 已被占用，-720 不该再补成同一集，实际 E%d", eps["e"].Episode)
	}
}
