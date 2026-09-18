package api

import (
	"testing"
	"time"
)

func TestGyBotTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Spider-Man.4K.2160p.中字.HDR.mkv", "[中字·4K·HDR]"},
		{"Movie.1080p.WEB-DL.H264.mkv", "[1080P]"},
		{"Blade.Runner.2049.UHD.BluRay.Remux.原盘.HEVC.DTS.mkv", "[原盘·HEVC]"},
		{"Dune.4K.DoVi.HDR10.mkv", "[4K·DV·HDR]"},
		{"plain.movie.mkv", ""},
	}
	for _, c := range cases {
		if got := gyBotTags(c.in); got != c.want {
			t.Errorf("gyBotTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWecomGySession(t *testing.T) {
	if s := wecomGySessionGet("userA"); s != nil {
		t.Fatalf("无会话时应返回 nil")
	}
	if s := wecomGySessionGet("userA"); s != nil {
		t.Fatalf("无会话时应返回 nil（第二次确认）")
	}
	wecomGySessionSet("userA", &wecomGySession{Stage: "movie", At: time.Now().Add(-10 * time.Minute)})
	if s := wecomGySessionGet("userA"); s != nil {
		t.Fatalf("过期会话应被清理")
	}
	s := &wecomGySession{Stage: "movie", At: time.Now()}
	wecomGySessionSet("userA", s)
	got := wecomGySessionGet("userA")
	if got == nil || got.Stage != "movie" {
		t.Fatalf("会话存取失败: %+v", got)
	}
}
