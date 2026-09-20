package api

import (
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 离线这条主链路：提交登记 → 监视器回填 file_id/任务名 → 整理结果认领到同一行。
// 这条链断了，下载记录里就永远只有链接没有片名
func TestDownloadLinkOfflineFlow(t *testing.T) {
	if _, err := model.InitDB("file:dllink_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	magnet := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Some.Show.S01"
	id := dlLinkRecord(h, magnet, "", "", "12345", "web", "submitted", nil)
	if id == 0 {
		t.Fatal("登记未落库")
	}
	var row model.DownloadLink
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Kind != "magnet" || row.Hash != "ABCDEF0123456789ABCDEF0123456789ABCDEF01" {
		t.Errorf("类型/指纹不符: kind=%q hash=%q", row.Kind, row.Hash)
	}

	// 监视器回填：info_hash 大小写与提交时不一定一致，得能对上
	dlLinkSyncTask(h, offlineTaskInfo{
		key: "abcdef0123456789abcdef0123456789abcdef01", name: "Some.Show.S01", fileID: "9001", status: 2,
	})
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Status != "done" || row.Name != "Some.Show.S01" {
		t.Errorf("回填后状态/任务名不符: %+v", row)
	}
	if fids := unmarshalStrs(row.ResultFids); len(fids) != 1 || fids[0] != "9001" {
		t.Errorf("产物 fid 未回填: %q", row.ResultFids)
	}

	// 认领：整理记录的 SourceFid 命中产物 fid → 识别结果回写到链接那一行
	rec := &model.OrganizeRecord{
		ID: 77, Source: "Some.Show.S01/", SourceFid: "9001", SourceKind: "dir",
		Status: "success", TmdbID: 1396, Title: "绝命毒师", Year: "2008",
		MediaType: "tv", Category: "欧美剧", TargetDir: "欧美剧/绝命毒师 (2008)",
	}
	dlLinkClaim(h.DB, rec)
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.OrganizeStatus != "success" || row.TmdbID != 1396 || row.Title != "绝命毒师" ||
		row.Year != "2008" || row.MediaType != "tv" || row.RecordID != 77 ||
		row.Category != "欧美剧" || row.TargetDir != "欧美剧/绝命毒师 (2008)" {
		t.Errorf("识别结果未回写: %+v", row)
	}
	if row.OrganizedAt == nil {
		t.Error("整理时间未写入")
	}

	// 已认成功的行不再被别的整理记录抢走
	dlLinkClaim(h.DB, &model.OrganizeRecord{
		ID: 78, Source: "Some.Show.S01/", SourceFid: "9001", Status: "failed", Title: "别的片",
	})
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Title != "绝命毒师" || row.OrganizeStatus != "success" {
		t.Errorf("成功结果被覆盖: %+v", row)
	}
}

// 目录内文件的 fid 同样算命中：115 给的是目录 fid，而整理散文件时
// 记录的 SourceFid 是文件本身，两头都要能对上
func TestDownloadLinkClaimByFileFid(t *testing.T) {
	if _, err := model.InitDB("file:dllink_file_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	ed2k := "ed2k://|file|x.mkv|123|FEDCBA0987654321FEDCBA0987654321|/"
	id := dlLinkRecord(h, ed2k, "", "", "1", "web", "submitted", nil)
	dlLinkSyncTask(h, offlineTaskInfo{key: "FEDCBA0987654321FEDCBA0987654321", name: "x.mkv", fileID: "9001", status: 2})

	rec := &model.OrganizeRecord{
		ID: 3, Source: "x.mkv", SourceFid: "7777", SourceKind: "file", Status: "success", Title: "某片",
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "9001", Name: "x.mkv", Kind: "video"}}),
	}
	dlLinkClaim(h.DB, rec)
	var row model.DownloadLink
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Title != "某片" {
		t.Errorf("文件 fid 未命中: %+v", row)
	}
}

// 分享转存：指纹取 share_code，产物名来自 /share/snap，认领只能靠名字
func TestDownloadLinkShareClaimByName(t *testing.T) {
	if _, err := model.InitDB("file:dllink_share_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	link := "https://115cdn.com/s/swzabc123?password=a1b2"
	id := dlLinkRecord(h, link, "share", "某合集", "999", "机器人", "done",
		[]string{"某片名.2020.1080p", "说明.txt"})

	var row model.DownloadLink
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Hash != "swzabc123" || row.Status != "done" {
		t.Errorf("分享记录字段不符: %+v", row)
	}

	// 转存后 115 保留原名，整理记录的 Source 就是那个名字（目录带尾斜杠）
	dlLinkClaim(h.DB, &model.OrganizeRecord{
		ID: 9, Source: "某片名.2020.1080p/", SourceFid: "555", Status: "success",
		Title: "某片名", Year: "2020", TmdbID: 42,
	})
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if row.Title != "某片名" || row.TmdbID != 42 || row.OrganizeStatus != "success" {
		t.Errorf("按名字未认领到: %+v", row)
	}

	// 无关内容不该被认领
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	id2 := dlLinkRecord(h, link, "share", "某合集", "999", "web", "done", []string{"甲"})
	dlLinkClaim(h.DB, &model.OrganizeRecord{ID: 10, Source: "乙/", SourceFid: "666", Status: "success", Title: "乙片"})
	// 用新变量查：复用已带主键的 row 会被 GORM 当成附加的 id 条件
	var fresh model.DownloadLink
	if err := h.DB.First(&fresh, id2).Error; err != nil {
		t.Fatalf("查不到记录行: %v", err)
	}
	if fresh.OrganizeStatus != "" {
		t.Errorf("不该认领无关记录: %+v", fresh)
	}
}
