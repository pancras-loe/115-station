package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Emby 的入库事件叫 library.new —— 此前判的是 Contains(event,"add")，
// 一个字都对不上，整条入库通知从来没响过
func TestEmbyEventCategory(t *testing.T) {
	cases := []struct{ event, want string }{
		// Emby 官方 Webhooks 插件（事件名对照 qmediasync 的 emby webhook 控制器）
		{"library.new", "added"},
		{"library.deleted", "deleted"},
		{"deep.delete", "deleted"}, // 神医助手
		{"playback.start", "play"},
		{"playback.stop", "pause"},
		{"playback.pause", "pause"},
		{"playback.unpause", "play"}, // 继续播放，不是暂停
		{"system.notificationtest", "test"},
		// 不该当成媒体事件报出去的
		{"device.new", ""},
		{"item.markplayed", ""},
		{"item.markunplayed", ""},
		{"user.authenticated", ""},
		{"user.authenticationfailed", ""},
		{"playback.progress", ""},
		{"", ""},
		// Jellyfin 的 NotificationType 没有点号，同一套判定也要吃得下
		{"itemadded", "added"},
		{"playbackstart", "play"},
		{"playbackstop", "pause"},
	}
	for _, c := range cases {
		if got, _ := embyEventCategory(c.event); got != c.want {
			t.Errorf("embyEventCategory(%q) = %q, want %q", c.event, got, c.want)
		}
	}
	// 能报出去的事件都得有标题，否则通知是空的
	for _, ev := range []string{"library.new", "library.deleted", "playback.start", "playback.pause"} {
		if _, title := embyEventCategory(ev); title == "" {
			t.Errorf("%s 没有通知标题", ev)
		}
	}
}

// /Library/VirtualFolders 回的是裸数组，不是 {"Items":[…]}。
// 按 Items 解析解出来永远是空表，建库后的「固化库选项」于是静默跳过
func TestEmbyVirtualFolderIdsParsesBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Library/VirtualFolders" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Emby 4.7 的真实回法：顶层就是数组
		_, _ = w.Write([]byte(`[
			{"Name":"电影","ItemId":"11","Locations":["/media/影视/电影"]},
			{"Name":"剧集","ItemId":"22","Locations":["/media/影视/剧集"]}
		]`))
	}))
	defer srv.Close()

	ids := embyVirtualFolderIds(srv.URL, "k")
	if ids["电影"] != "11" || ids["剧集"] != "22" {
		t.Fatalf("解析裸数组失败: %v", ids)
	}
}

// 非 200 不能当成「没有媒体库」静默吞掉（401 是最常见的：API 密钥填错）
func TestEmbyVirtualFolderIdsRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if ids := embyVirtualFolderIds(srv.URL, "bad"); len(ids) != 0 {
		t.Fatalf("401 不该解出媒体库: %v", ids)
	}
}
