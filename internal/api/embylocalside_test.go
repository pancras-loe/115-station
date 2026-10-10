package api

import (
	"encoding/json"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 映射本地一侧：空 = 本地媒体库目录、绝对路径照用、相对路径 = 根下子目录（-6 版兼容）
func TestEmbyPathRootsLocalSide(t *testing.T) {
	deepDelEmbyTestDB(t, "/Movies", nil)
	cases := []struct{ mapping, wantLocal string }{
		{"#/Movies", "/Movies"},
		{"/Movies/资源库#/Movies", "/Movies/资源库"},
		{"/Movies/资源库/#/Movies", "/Movies/资源库"},
		{`D:\strm#/Movies`, "D:/strm"},
		{"资源库#/Movies", "/Movies/资源库"},
		{"/#/Movies", "/Movies"}, // 只剩 / 的不认
	}
	for _, c := range cases {
		if l, e := embyPathRoots(c.mapping); l != c.wantLocal || e != "/Movies" {
			t.Errorf("%q: got %q / %q want %q", c.mapping, l, e, c.wantLocal)
		}
	}
	// 2026-10-10 现场：本站 /vol1/1000 → /Movies，Emby /vol1/1000/资源库 → /Movies
	if got := embyPathToLocal("/Movies/资源库#/Movies", "/Movies/电影/动画电影/A/a.strm"); got != "/Movies/资源库/电影/动画电影/A/a.strm" {
		t.Errorf("Emby → 本地: %q", got)
	}
	if got := mapLocalToEmbyPath("/Movies/资源库#/Movies", "/Movies/资源库/电影/A/a.strm"); got != "/Movies/电影/A/a.strm" {
		t.Errorf("本地 → Emby: %q", got)
	}
}

// 老版本保存时自动写进前半段的本地根要清掉（用户之后改过本地目录的话那个值是过时的），
// 用户填的相对子目录不动；只跑一次
func TestMigrateEmbyLocalSide(t *testing.T) {
	cases := []struct{ before, after string }{
		{"/media#/mnt/emby", "#/mnt/emby"},
		{`D:\strm#/mnt/emby`, "#/mnt/emby"},
		{"资源库#/Movies", "资源库#/Movies"},
		{"#/Movies", "#/Movies"},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.before, func(t *testing.T) { migrateEmbyLocalSideCase(t, c.before, c.after) })
	}
}

func migrateEmbyLocalSideCase(t *testing.T, before, after string) {
	h := deepDelEmbyTestDB(t, "/strm", nil)
	h.Config = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	b, _ := json.Marshal(map[string]any{"server_url": "http://e", "path_mapping": before, "refresh_enabled": true})
	if err := h.Config.SaveSetting("emby", string(b)); err != nil {
		t.Fatal(err)
	}
	h.MigrateEmbyLocalSide()

	var got map[string]any
	if err := json.Unmarshal([]byte(h.getSettingValue("emby")), &got); err != nil {
		t.Fatal(err)
	}
	if got["path_mapping"] != after || got["server_url"] != "http://e" || got["refresh_enabled"] != true {
		t.Errorf("%q: 迁移后 %v", before, got)
	}
	var done int64
	model.DB.Model(&model.Setting{}).Where("key = ?", embyLocalSideMigrateKey).Count(&done)
	if done != 1 {
		t.Errorf("%q: 没打完成标记", before)
	}

	// 迁移过之后用户自己填的绝对路径不能再被清掉
	b, _ = json.Marshal(map[string]any{"path_mapping": "/strm/资源库#/Movies"})
	_ = h.Config.SaveSetting("emby", string(b))
	h.MigrateEmbyLocalSide()
	if v := h.getSettingValue("emby"); v != string(b) {
		t.Errorf("%q: 第二次又迁移了: %s", before, v)
	}
}
