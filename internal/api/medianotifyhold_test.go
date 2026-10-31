package api

import (
	"testing"
	"time"
)

// 整理一轮期间的入库通知只攒着、同一部剧并成一张卡片，收尾后才排冲刷
// （2026-10-04 现场：蜡笔小新 4 季各发了一条）
func TestHoldMediaNotifDefersFlush(t *testing.T) {
	reset := func() {
		mediaNotif.mu.Lock()
		if mediaNotif.timer != nil {
			mediaNotif.timer.Stop()
		}
		mediaNotif.items, mediaNotif.timer, mediaNotif.holds = nil, nil, 0
		mediaNotif.firstAt = time.Time{}
		mediaNotif.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	release := holdMediaNotif()
	QueueMediaNotif(mediaNotifEntry{Title: "蜡笔小新", Kind: "剧集", Source: "organize", Episodes: "S01"})
	QueueMediaNotif(mediaNotifEntry{Title: "蜡笔小新", Kind: "剧集", Source: "organize", Episodes: "S02"})
	mediaNotif.mu.Lock()
	n, timer := len(mediaNotif.items), mediaNotif.timer
	mediaNotif.mu.Unlock()
	if n != 1 || timer != nil {
		t.Fatalf("整理期间应合并且不排冲刷，实际 items=%d timer=%v", n, timer != nil)
	}
	release()
	release() // 重复调用无害
	mediaNotif.mu.Lock()
	timer, holds := mediaNotif.timer, mediaNotif.holds
	mediaNotif.mu.Unlock()
	if timer == nil || holds != 0 {
		t.Fatalf("收尾后应排上冲刷，实际 timer=%v holds=%d", timer != nil, holds)
	}
}

// 同一部剧分几个条目整理（分享里 8 个散文件一集一个），合并后的卡片要算全部的集数与文件数，
// 不能只留第一个条目的（2026-10-31 现场：8 集的分享通知成「S01E01（缺 E02-E08）· 1 个文件」）
func TestMediaNotifMergeAccumulatesOrganize(t *testing.T) {
	media := &TmdbMedia{Title: "末路兼职", Year: "2026", MediaType: "tv"} // 没有 TMDB 编号：不查缺集
	mk := func(name string) mediaNotifEntry {
		return mediaNotifEntry{Title: media.Title, Kind: "剧集", Source: "organize",
			orgMedia: media, orgVideos: []remoteFile{{Name: name}}, orgCount: 1, orgBytes: 1 << 30}
	}
	e := mk("末路兼职 - S01E02 - 第02集.mkv")
	for _, n := range []string{"末路兼职 - S01E01 - 第01集.mkv", "末路兼职 - S01E04 - 第04集.mkv", "末路兼职 - S01E03 - 第03集.mkv"} {
		e.mergeFrom(mk(n))
	}
	e.finalizeOrg()
	if e.Episodes != "S01E01-E04（全）" {
		t.Fatalf("集数应合并成 S01E01-E04，实际 %q", e.Episodes)
	}
	if e.Files != "4 个文件 · 4.0 GB" {
		t.Fatalf("文件数应累加，实际 %q", e.Files)
	}
}
