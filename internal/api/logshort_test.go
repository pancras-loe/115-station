package api

import "testing"

func TestShortLogName(t *testing.T) {
	in := "【高清影视之家发布 www.BBQDDQ.com】玩具总动员5[国英多音轨+简繁英字幕].Toy.Story.5.2026.1080p.iTunes.WEB-DL.DDP.7.1.Atmos.H.264-DreamHD/"
	got := shortLogName(in)
	if got != "玩具总动员5[国英多音轨+简繁英字幕].Toy.Story.5.2026.1080p.iTunes.WEB-DL.DDP.7.1.Atmos.H.264-DreamHD/" &&
	   len([]rune(got)) > 49 {
		t.Errorf("shortLogName = %q (len %d)", got, len([]rune(got)))
	}
	want := "玩具总动员5[国英多音轨+简繁英字幕].Toy.Story.5.2026.1080p.iTu…"
	if got != want {
		t.Errorf("shortLogName = %q, want %q", got, want)
	}
}
