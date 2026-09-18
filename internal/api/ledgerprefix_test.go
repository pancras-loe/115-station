package api

import (
	"testing"

	"strmhub/internal/model"
)

// TestLibraryFilesOfPrefix 验证洗版台账查询的库名前缀拼接：
// 台账 rel_path 带库名层（俱乐部/…），MediaLibrary.TargetPath 不带——
// 此前缺失前缀导致洗版永远查空、判定恒为跳过
func TestLibraryFilesOfPrefix(t *testing.T) {
	// 内存库：免文件句柄（Windows 下 TempDir 清理会因 sqlite 占用报错）
	if _, err := model.InitDB("file:wash_prefix_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer func() { model.DB = nil }()

	// 台账：带库名层的真实形态
	rows := []model.SyncedFile{
		{FileID: "f1", PickCode: "pc1", RelPath: "俱乐部/电影/外语电影/P-测试片-2024-[tmdb=1]/测试片 (2024).1080p.BluRay.mkv.strm", Kind: "video"},
		{FileID: "f2", PickCode: "pc2", RelPath: "俱乐部/电影/外语电影/P-测试片-2024-[tmdb=1]/poster.jpg", Kind: "asset"},
	}
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}

	// MediaLibrary 记录的目标路径（不带库名层）
	targetDir := "电影/外语电影/P-测试片-2024-[tmdb=1]"

	// 无前缀：第三重兜底（%/base/%）应仍能命中——OpenAPI 等拿不到库名的场景洗版不再静默跳过
	if got := libraryFilesOf(targetDir, ""); len(got) != 2 {
		t.Errorf("无前缀应通过兜底查到 2 条，得到 %d 条", len(got))
	}
	// 带库名前缀：查到该片的台账文件
	got := libraryFilesOf(targetDir, "俱乐部")
	if len(got) != 2 {
		t.Fatalf("带前缀应查到 2 条，得到 %d 条", len(got))
	}
	if got[0].FileID != "f1" && got[0].FileID != "f2" {
		t.Errorf("查到的文件不符: %+v", got[0])
	}
}
