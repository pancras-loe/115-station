package api

import (
	"strings"
	"testing"
)

// 回归：iTunes WEB-DL HDR10+ Atmos 命名（曾出现 WEB.WEB-DL 重复、ATMOS 重复、
// 7.1 丢失、HDR10+ 丢加号、iTunes 被丢弃等解析缺陷）
func TestResourceParseiTunesWEBDL(t *testing.T) {
	name := "Toy.Story.5.2026.2160p.iTunes.WEB-DL.DDP.7.1.Atmos.HDR10+.H.265-DreamHD.mkv"
	ri := ParseResourceInfo(name)
	checks := map[string]string{
		"Pix": ri.Pix, "Version": ri.Version, "Source": ri.Source, "Type": ri.Type,
		"Effect": ri.Effect, "VideoEncode": ri.VideoEncode, "AudioEncode": ri.AudioEncode,
		"Team": ri.Team,
	}
	want := map[string]string{
		"Pix": "2160p", "Version": "ITUNES", "Source": "", "Type": "WEB-DL",
		"Effect": "HDR10+", "VideoEncode": "H265", "AudioEncode": "DDP.7.1.ATMOS",
		"Team": "DreamHD",
	}
	for k, v := range want {
		if checks[k] != v {
			t.Errorf("%s = %q, want %q", k, checks[k], v)
		}
	}
}

func TestResourceRenderToyStory(t *testing.T) {
	name := "Toy.Story.5.2026.2160p.iTunes.WEB-DL.DDP.7.1.Atmos.HDR10+.H.265-DreamHD.mkv"
	media := &TmdbMedia{Title: "玩具总动员5", Year: "2026", MediaType: "movie", TmdbID: 1234}
	ctx := buildRenameContext(media, parseFileName(name), name)
	got := ctx.ApplyTemplate("{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>{ext}")
	if !strings.Contains(got, "HDR10+") || strings.Contains(got, "..") || strings.Count(got, "WEB") != 1 {
		t.Errorf("渲染异常: %s", got)
	}
}
