package api

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

// ffprobe JSON 样例：1 视频 + 2 音轨（不同语言）+ 1 内嵌字幕 + 1 挂图封面
const probeSampleJSON = `{
 "streams": [
  {"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,
   "tags":{"language":"und"},
   "side_data_list":[{"side_data_type":"Dolby Vision configuration record"}]},
  {"codec_type":"audio","codec_name":"eac3","channels":6,"tags":{"language":"chi","title":"国语 5.1"}},
  {"codec_type":"audio","codec_name":"aac","channels":2,"tags":{"language":"eng","title":"英语"}},
  {"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"chi","title":"简体中文"}},
  {"codec_type":"video","codec_name":"mjpeg","width":500,"height":750,
   "disposition":{"attached_pic":1}}
 ],
 "format": {"duration":"5400.5"}
}`

func TestParseProbeOutputStreams(t *testing.T) {
	res, err := parseProbeOutput([]byte(probeSampleJSON))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 全量轨道：attached_pic 封面不算
	if len(res.Streams) != 4 {
		t.Fatalf("streams = %d want 4 (attached_pic excluded)", len(res.Streams))
	}
	if res.Streams[0].Kind != "video" || res.Streams[0].Codec != "hevc" || res.Streams[0].Height != 2160 {
		t.Errorf("video stream: %+v", res.Streams[0])
	}
	if res.Streams[0].Language != "" {
		t.Errorf("und 应归一为空: %q", res.Streams[0].Language)
	}
	if res.Streams[1].Kind != "audio" || res.Streams[1].Language != "chi" || res.Streams[1].Channels != 6 || res.Streams[1].Title != "国语 5.1" {
		t.Errorf("audio1: %+v", res.Streams[1])
	}
	if res.Streams[2].Language != "eng" {
		t.Errorf("audio2 lang: %+v", res.Streams[2])
	}
	sub := res.Streams[3]
	if sub.Kind != "subtitle" || sub.Language != "chi" || sub.Title != "简体中文" {
		t.Errorf("subtitle: %+v", sub)
	}
	// 摘要字段不受影响（Audio 是标签化后的习惯名）
	if res.Pix != "2160p" || res.Effect != "DV" || res.Audio != "DDP" {
		t.Errorf("summary fields: %+v", res)
	}
	if res.Duration != 5400 {
		t.Errorf("duration: %d", res.Duration)
	}
}

func TestNfoFileInfoFromAndMarshal(t *testing.T) {
	res := &probeResult{Duration: 5400, ProbedAt: time.Now()}
	res.Streams = []probeTrack{
		{Kind: "video", Codec: "hevc", Width: 3840, Height: 2160},
		{Kind: "audio", Codec: "eac3", Channels: 6, Language: "chi", Title: "国语 5.1"},
		{Kind: "audio", Codec: "aac", Channels: 2, Language: "eng"},
		{Kind: "subtitle", Codec: "subrip", Language: "chi", Title: "简体中文"},
	}
	fi := nfoFileInfoFrom(res)
	if fi == nil || fi.StreamDetails == nil {
		t.Fatal("fileinfo nil")
	}
	sd := fi.StreamDetails
	if sd.Video == nil || sd.Video.Height != 2160 || sd.Video.DurationInSeconds != 5400 {
		t.Errorf("video: %+v", sd.Video)
	}
	if len(sd.Audio) != 2 || sd.Audio[0].Language != "chi" || sd.Audio[0].Channels != 6 {
		t.Errorf("audio: %+v", sd.Audio)
	}
	if len(sd.Subtitle) != 1 || sd.Subtitle[0].Language != "chi" || sd.Subtitle[0].Name != "简体中文" {
		t.Errorf("subtitle: %+v", sd.Subtitle)
	}
	// 集级 NFO 序列化：streamdetails 在 episodedetails 内、音轨/字幕逐条成标签
	ep := nfoEpisode{
		Title: "第一集", Season: 1, Episode: 2, Aired: "2025-01-02",
		UniqueIDs: []nfoUniqueID{{Type: "tmdb", Default: true, Value: "123"}},
		Fileinfo:  fi,
	}
	b, err := marshalNFO(ep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"<episodedetails>", "<season>1</season>", "<episode>2</episode>",
		"<fileinfo>", "<streamdetails>", "<video>", "<codec>hevc</codec>",
		"<audio>", "<language>chi</language>", "<channels>6</channels>",
		"<subtitle>", "<name>简体中文</name>", "<durationinseconds>5400</durationinseconds>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("NFO 缺少 %q:\n%s", want, s)
		}
	}
	var back nfoEpisode
	if err := xml.Unmarshal(b, &back); err != nil {
		t.Fatalf("roundtrip unmarshal: %v", err)
	}
	if back.Fileinfo == nil || len(back.Fileinfo.StreamDetails.Audio) != 2 {
		t.Errorf("roundtrip fileinfo lost: %+v", back.Fileinfo)
	}
	// 空探测 → 不写 fileinfo
	if nfoFileInfoFrom(nil) != nil {
		t.Error("nil probe should yield nil fileinfo")
	}
	if nfoFileInfoFrom(&probeResult{}) != nil {
		t.Error("empty streams should yield nil fileinfo")
	}
}
