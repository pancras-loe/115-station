package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strmhub/internal/config"
	"strmhub/internal/model"
)

// 播放端点 <id> 的稳定性：同一链接必须得到同一指纹（库内去重的根基），
// 不同类型链接各取自带指纹（ed2k hash / btih / URL sha1）
func TestOfflinePlayID(t *testing.T) {
	ed2k := "ed2k://|file|Some.Show.S01E01.1080p.mkv|123456789|ABCDEF0123456789ABCDEF0123456789|/"
	if got := offlinePlayID(ed2k); got != "ABCDEF0123456789ABCDEF0123456789" {
		t.Errorf("ed2k 指纹应取文件 hash: got %q", got)
	}
	// 同 hash 大小写差异（站点生成的 ed2k 常见）必须归一
	ed2kLower := strings.Replace(ed2k, "|ABCDEF", "|abcdef", 1)
	if offlinePlayID(ed2kLower) != offlinePlayID(ed2k) {
		t.Errorf("hash 大小写应归一到同一指纹")
	}
	m1 := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=x"
	if got := offlinePlayID(m1); got != "M0123456789ABCDEF0123456789ABCDEF01234567" {
		t.Errorf("磁力指纹应取 btih: got %q", got)
	}
	if offlinePlayID("http://a/f.mkv") == offlinePlayID("http://b/f.mkv") {
		t.Errorf("不同 URL 指纹不应相同")
	}
	if !strings.HasPrefix(offlinePlayID("http://a/f.mkv"), "U") {
		t.Errorf("URL 指纹应带 U 前缀")
	}
}

// 链接元数据提取：ed2k 自带文件名与字节数（定位兜底用），http 取 URL base
func TestOfflinePlayNameSize(t *testing.T) {
	name, size := offlinePlayNameSize("ed2k://|file|%E6%9F%90%E5%89%A7%E9%9B%86.S01E01.1080p.mkv|123456789|ABCDEF|/")
	if name != "某剧集.S01E01.1080p.mkv" {
		t.Errorf("ed2k 文件名应 URL 解码: got %q", name)
	}
	if size != 123456789 {
		t.Errorf("ed2k 字节数: got %d", size)
	}
	name, _ = offlinePlayNameSize("https://example.com/path/to/movie.mp4")
	if name != "movie.mp4" {
		t.Errorf("http 应取 URL base: got %q", name)
	}
	if n, _ := offlinePlayNameSize("magnet:?xt=urn:btih:ABCD"); n != "" {
		t.Errorf("磁力无文件名: got %q", n)
	}
}

// 任务匹配：hash 优先且大小写不敏感；同名任务兜底；多任务取状态最优
func TestOfflinePlayPickTask(t *testing.T) {
	ed2k := "ed2k://|file|a.mkv|1|ABCD1234|/"
	tasks := []offlineTaskInfo{
		{key: "abcd1234", name: "a.mkv", status: -1}, // 旧失败任务
		{key: "abcd1234", name: "a.mkv", status: 1, percent: 40},
		{key: "ffffffff", name: "b.mkv", status: 2},
	}
	got, ok := offlinePlayPickTask(ed2k, "a.mkv", tasks)
	if !ok || got.status != 1 || got.percent != 40 {
		t.Errorf("应命中下载中任务（状态最优）: ok=%v got=%+v", ok, got)
	}
	// 同名兜底：hash 对不上但任务名一致（http 任务 115 以文件名作 key）
	if _, ok := offlinePlayPickTask("https://dl.example.com/x/ep1.mkv", "ep1.mkv",
		[]offlineTaskInfo{{key: "ep1.mkv", name: "ep1.mkv", status: 2}}); !ok {
		t.Errorf("同名任务应兜底命中")
	}
	// 无匹配
	if _, ok := offlinePlayPickTask("ed2k://|file|missing.mkv|1|00000000|/", "missing.mkv", tasks); ok {
		t.Errorf("无指纹无同名不应命中")
	}
}

// 入库登记 → 占位 STRM → 完成定位 的闭环（内存库 + 临时媒体根）。
// 这是「按需离线」的主链路：登记幂等、占位内容指向播放端点、
// 定位按 名字 → 尺寸 兜底（整理改写文件名后仍可命中）
func TestOfflinePlayRegisterAndResolve(t *testing.T) {
	if _, err := model.InitDB("file:offplay_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	root := t.TempDir()
	model.DB.Where("1=1").Delete(&model.Setting{})
	model.DB.Create(&model.Setting{Key: "full", Value: `{"local_path":"` + filepath.ToSlash(root) + `"}`})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	ed2k := "ed2k://|file|%E6%9F%90%E5%89%A7%E9%9B%86.S01E01.1080p.mkv|123456789|FEDCBA0987654321FEDCBA0987654321|/"

	// 登记：落库 + 占位 STRM 指向播放端点
	offlinePlayRegister(h, ed2k)
	var rec model.OfflinePlay
	if err := h.DB.Where("id = ?", offlinePlayID(ed2k)).First(&rec).Error; err != nil {
		t.Fatalf("登记未落库: %v", err)
	}
	if rec.Status != "downloading" || rec.Name != "某剧集.S01E01.1080p.mkv" || rec.Size != 123456789 {
		t.Errorf("登记字段不符: %+v", rec)
	}
	strmPath := filepath.Join(root, offlinePlayStageDir, "某剧集.S01E01.1080p.mkv.strm")
	data, err := os.ReadFile(strmPath)
	if err != nil {
		t.Fatalf("占位 STRM 未生成: %v", err)
	}
	wantSuffix := "/ed2k/play/" + offlinePlayID(ed2k)
	if !strings.HasSuffix(strings.TrimSpace(string(data)), wantSuffix) {
		t.Errorf("占位 STRM 内容应指向播放端点: %q", string(data))
	}

	// 重复登记幂等：不产生第二行，内容未变不重写
	fi1, _ := os.Stat(strmPath)
	offlinePlayRegister(h, ed2k)
	var cnt int64
	h.DB.Model(&model.OfflinePlay{}).Where("id = ?", offlinePlayID(ed2k)).Count(&cnt)
	if cnt != 1 {
		t.Errorf("重复登记应幂等: count=%d", cnt)
	}
	fi2, _ := os.Stat(strmPath)
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Errorf("内容未变不应重写（mtime 应保持）")
	}

	// 定位 ①：台账按文件名（整理未改名）
	h.DB.Create(&model.SyncedFile{FileID: "1", PickCode: "pickAAA", RelPath: "剧/某剧集.S01E01.1080p.mkv.strm", Kind: "video", Size: 999})
	if got := h.offlinePlayResolve(rec); got != "pickAAA" {
		t.Errorf("应按名字定位: got %q", got)
	}

	// 定位 ②：名字被整理改写后按尺寸兜底
	rec2 := model.OfflinePlay{ID: "SIZEONLY", Link: ed2k, Name: "原名.mkv", Size: 123456789, Status: "downloading"}
	h.DB.Create(&model.SyncedFile{FileID: "2", PickCode: "pickBBB", RelPath: "剧/规范名.S01E01.mkv.strm", Kind: "video", Size: 123456789})
	if got := h.offlinePlayResolve(rec2); got != "pickBBB" {
		t.Errorf("应按尺寸兜底定位: got %q", got)
	}

	// 定位不到：返回空串（由端点给出「整理中」提示）
	rec3 := model.OfflinePlay{ID: "NOPE", Link: ed2k, Name: "没有.mkv", Size: 42, Status: "downloading"}
	if got := h.offlinePlayResolve(rec3); got != "" {
		t.Errorf("无匹配应返回空: got %q", got)
	}
}
