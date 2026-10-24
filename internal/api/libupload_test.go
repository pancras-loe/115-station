package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"
)

// 监控上传按库（libupload.go）：每库开关、老全局开关的换算、按账号定位网盘目录

func TestMediaLibUploadField(t *testing.T) {
	_, r, _ := mediaLibsSetup(t)
	code, body := libHTTPDo(t, r, "POST", "/media-libs",
		`{"name":"电影","cid":"100","path":"/影视/电影","local":"电影","scheme_id":0,"scrape":"manual","upload":true}`)
	if code != http.StatusOK {
		t.Fatalf("新建：%d %v", code, body)
	}
	libs := loadLibraries()
	if len(libs) != 1 || !libs[0].Upload {
		t.Fatalf("新建时带的上传开关没存上：%+v", libs)
	}
	id := libs[0].ID
	// 不带 upload 字段（批量添加、老页面）= 不改，别把开着的关掉
	code, body = libHTTPDo(t, r, "PUT", "/media-libs/"+itoa(id),
		`{"name":"电影","cid":"100","path":"/影视/电影","local":"电影","scheme_id":0,"scrape":"auto"}`)
	if code != http.StatusOK {
		t.Fatalf("修改：%d %v", code, body)
	}
	if !loadLibraries()[0].Upload {
		t.Fatal("没带 upload 的修改把上传开关关掉了")
	}
	code, body = libHTTPDo(t, r, "PUT", "/media-libs/"+itoa(id),
		`{"name":"电影","cid":"100","path":"/影视/电影","local":"电影","scheme_id":0,"scrape":"auto","upload":false}`)
	if code != http.StatusOK {
		t.Fatalf("修改：%d %v", code, body)
	}
	if loadLibraries()[0].Upload {
		t.Fatal("upload:false 没关上")
	}
	// 新建不带 = 默认关
	code, _ = libHTTPDo(t, r, "POST", "/media-libs", `{"name":"剧集","cid":"200","path":"/影视/剧集","local":"剧集","scheme_id":0}`)
	if code != http.StatusOK {
		t.Fatal("新建第二个库失败")
	}
	for _, l := range loadLibraries() {
		if l.Name == "剧集" && l.Upload {
			t.Fatal("新建的库默认就开着上传")
		}
	}
}

func TestMigrateLibUpload(t *testing.T) {
	h, _, cfg := mediaLibsSetup(t)
	for _, l := range []model.Library{
		{Name: "电影", Cid: "100", Local: "电影", Account: acctMain},
		{Name: "副号", Cid: "300", Local: "副号", Account: 2},
	} {
		if err := model.DB.Create(&l).Error; err != nil {
			t.Fatal(err)
		}
	}
	forgetLibraries()
	if err := cfg.SaveSetting("monitor", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	h.MigrateLibUpload()
	for _, l := range loadLibraries() {
		if want := l.Account == acctMain; l.Upload != want {
			t.Fatalf("老开关只管主号的库：%s upload=%v", l.Name, l.Upload)
		}
	}
	// 只跑一次：用户之后关掉，重启不再被老开关打开
	model.DB.Model(&model.Library{}).Where("1 = 1").Update("upload", false)
	forgetLibraries()
	h.MigrateLibUpload()
	for _, l := range loadLibraries() {
		if l.Upload {
			t.Fatalf("迁移跑了第二次：%s 又被打开", l.Name)
		}
	}
}

func TestMigrateLibUploadOff(t *testing.T) {
	h, _, _ := mediaLibsSetup(t)
	model.DB.Create(&model.Library{Name: "电影", Cid: "100", Local: "电影"})
	forgetLibraries()
	h.MigrateLibUpload() // 没开过全局上传：一个都不开
	if loadLibraries()[0].Upload {
		t.Fatal("老配置没开上传，迁移却打开了")
	}
}

func TestJoinPanRel(t *testing.T) {
	cases := []struct{ root, rel, want string }{
		{"/影视/电影", "阿凡达 (2009)", "/影视/电影/阿凡达 (2009)"},
		{"/影视/电影/", `剧集\S01`, "/影视/电影/剧集/S01"},
		{"/影视/电影", ".", "/影视/电影"},
		{"/影视/电影", "", "/影视/电影"},
	}
	for _, c := range cases {
		if got := joinPanRel(c.root, c.rel); got != c.want {
			t.Errorf("joinPanRel(%q,%q)=%q，要 %q", c.root, c.rel, got, c.want)
		}
	}
}

func TestRawLibCid(t *testing.T) {
	if got := rawLibCid(libRoot{Cid: "100"}); got != "100" {
		t.Fatalf("主号的库：%s", got)
	}
	if got := rawLibCid(libRoot{Cid: acctLibKey(3, "555"), Account: 3}); got != "555" {
		t.Fatalf("别的账号的库要剥掉 a3: 前缀：%s", got)
	}
}

func TestScanUploadCandidates(t *testing.T) {
	newTestDB(t, "libupload.db")
	h := &Handler{DB: model.DB}
	dir := t.TempDir()
	write := func(rel string, age time.Duration) string {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		at := time.Now().Add(-age)
		os.Chtimes(p, at, at)
		return p
	}
	nfo := write("片/片.nfo", time.Hour)
	poster := write("片/poster.jpg", time.Hour)
	write("片/片.strm", time.Hour)         // 不是元数据
	write("片/随便.jpg", time.Hour)         // 不是标准图片名
	write("旧片/poster.jpg", 48*time.Hour) // 超过 24 小时
	done := write("片/fanart.jpg", time.Hour)
	if st, ok := stampOfPath(done); ok {
		markUploadedStamp(model.DB, done, st) // 按当前指纹传过
	}
	got := scanUploadCandidates(h, dir, time.Now().Add(-24*time.Hour))
	want := map[string]bool{nfo: true, poster: true}
	if len(got) != len(want) {
		t.Fatalf("候选不对：%v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("不该传的进了候选：%s", p)
		}
	}
}
