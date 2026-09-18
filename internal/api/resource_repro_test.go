package api

import (
	"strings"
	"testing"
)

// AV1/AVC 是普通视频编码，移除成人影片功能后仍应参与影视重命名。
func TestMovieRenamePreservesVideoCodecs(t *testing.T) {
	for _, tc := range []struct{ codec, want string }{{"AV1", "AV1"}, {"AVC", "H264"}} {
		t.Run(tc.codec, func(t *testing.T) {
			name := "Example.2024.1080p." + tc.codec + ".mkv"
			media := &TmdbMedia{Title: "示例电影", Year: "2024", MediaType: "movie", TmdbID: 123}
			ctx := buildRenameContext(media, parseFileName(name), name)
			got := ctx.ApplyTemplate("{title}.{year}<.{resource_pix}><.{video_encode}>{ext}")
			want := "示例电影.2024.1080p." + tc.want + ".mkv"
			if got != want {
				t.Fatalf("影视重命名结果 = %q，期望 %q", got, want)
			}
		})
	}
}

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
