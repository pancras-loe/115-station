package api

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- 产物出口 ----

func TestFileScrapeWriterLocalNoUploadLeavesToMonitor(t *testing.T) {
	dir := t.TempDir()
	ops := &fakeCloudOps{}
	w := newFileScrapeWriter(ops, false, false)
	var handled []string
	w.markHandled = func(p string) { handled = append(handled, p) }
	d := metaDest{Local: dir, CloudBase: "100", CloudRel: "电影/某片"}

	if wrote, err := w.put(d, "poster.jpg", []byte("x")); !wrote || err != nil {
		t.Fatalf("首次写入 wrote=%v err=%v", wrote, err)
	}
	// 本次不上传：一个 115 请求都不该发，也不登记成已处理 —— 传不传交给监控上传
	if len(ops.listed) != 0 || len(ops.uploaded) != 0 {
		t.Fatalf("不上传时不该碰网盘: listed=%v uploaded=%v", ops.listed, ops.uploaded)
	}
	if len(handled) != 0 {
		t.Fatalf("没勾上传不该登记指纹: %v", handled)
	}
	// 只补缺失：本地已有就跳过
	if wrote, _ := w.put(d, "poster.jpg", []byte("y")); wrote {
		t.Fatal("已存在且不覆盖时不该写")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "poster.jpg")); string(b) != "x" {
		t.Fatalf("内容被改了: %q", b)
	}
	if w.stat != (fileScrapeStat{Local: 1, Skipped: 1}) {
		t.Fatalf("stat = %+v", w.stat)
	}
}

func TestFileScrapeWriterUploadResolvesCloudDir(t *testing.T) {
	dir := t.TempDir()
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"100": {fdir("101", "电影")},
		"101": {fdir("102", "某片 (2020)")},
		"102": {ffile("old", "poster.jpg"), ffile("v1", "某片.mkv")},
	}}
	w := newFileScrapeWriter(ops, false, true)
	d := metaDest{Local: dir, CloudBase: "100", CloudRel: "电影/某片 (2020)"}

	// 网盘已有 poster.jpg、不覆盖：本地照写，网盘不动
	if _, err := w.put(d, "poster.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(ops.uploaded) != 0 || len(ops.deleted) != 0 {
		t.Fatalf("网盘已有同名文件且不覆盖，不该上传: %v %v", ops.uploaded, ops.deleted)
	}
	// 网盘没有的 fanart.jpg：上传到解析出来的目录
	if _, err := w.put(d, "fanart.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ops.uploaded, []string{"102/fanart.jpg"}) {
		t.Fatalf("uploaded = %v", ops.uploaded)
	}
	// 每一层只列一次
	for cid, n := range ops.listed {
		if n != 1 {
			t.Fatalf("目录 %s 被列了 %d 次", cid, n)
		}
	}
}

func TestFileScrapeWriterForceReplacesCloudFile(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"7": {ffile("old", "tvshow.nfo")},
	}}
	w := newFileScrapeWriter(ops, true, true)
	if wrote, err := w.put(metaDest{CloudBase: "7"}, "tvshow.nfo", []byte("new")); !wrote || err != nil {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	// 115 允许同名并存：覆盖必须先删旧的再传，否则目录里会有两份
	if !reflect.DeepEqual(ops.deleted, []string{"old"}) || !reflect.DeepEqual(ops.uploaded, []string{"7/tvshow.nfo"}) {
		t.Fatalf("deleted=%v uploaded=%v", ops.deleted, ops.uploaded)
	}
}

func TestFileScrapeWriterCloudOnlySkipsExisting(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{
		"7": {ffile("old", "poster.jpg")},
	}}
	w := newFileScrapeWriter(ops, false, true)
	if wrote, _ := w.put(metaDest{CloudBase: "7"}, "poster.jpg", []byte("x")); wrote {
		t.Fatal("只补缺失：网盘已有就跳过")
	}
	if w.stat.Skipped != 1 || len(ops.uploaded) != 0 {
		t.Fatalf("stat=%+v uploaded=%v", w.stat, ops.uploaded)
	}
}

func TestFileScrapeWriterMissingCloudDir(t *testing.T) {
	ops := &fakeCloudOps{dirs: map[string][]map[string]interface{}{"100": {fdir("101", "电影")}}}
	w := newFileScrapeWriter(ops, false, true)
	_, err := w.put(metaDest{CloudBase: "100", CloudRel: "电影/不存在"}, "poster.jpg", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "网盘上没有对应目录") {
		t.Fatalf("找不到目录应报错且不建目录: %v", err)
	}
	if len(ops.uploaded) != 0 {
		t.Fatal("不该上传")
	}
}
