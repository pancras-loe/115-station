package api

import (
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 台账主链路：提交登记 → 离线任务回填 file_id → 整理记录按 fid 反查到链接。
// 这条链路一断，整理记录里的「来源链接」就永远是空的
func TestDownloadLinkLedgerFlow(t *testing.T) {
	if _, err := model.InitDB("file:dllink_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	magnet := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Some.Show.S01"
	id := dlLinkRecord(h, magnet, "", "", "12345", "web")
	if id == 0 {
		t.Fatal("登记未落库")
	}
	var row model.DownloadLink
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到台账行: %v", err)
	}
	if row.Kind != "magnet" || row.Hash != "ABCDEF0123456789ABCDEF0123456789ABCDEF01" {
		t.Errorf("类型/指纹不符: kind=%q hash=%q", row.Kind, row.Hash)
	}

	// 离线监视器回填：info_hash 大小写与提交时不一定一致，得能对上
	dlLinkSyncTask(h, offlineTaskInfo{
		key: "abcdef0123456789abcdef0123456789abcdef01", name: "Some.Show.S01", fileID: "9001", status: 2,
	})
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到台账行: %v", err)
	}
	if row.Status != "done" || row.Name != "Some.Show.S01" {
		t.Errorf("回填后状态/任务名不符: %+v", row)
	}
	if fids := unmarshalFids(row.ResultFids); len(fids) != 1 || fids[0] != "9001" {
		t.Errorf("产物 fid 未回填: %q", row.ResultFids)
	}

	// 整理记录：SourceFid 命中产物 fid → 补上链接
	rec := &model.OrganizeRecord{Source: "Some.Show.S01/", SourceFid: "9001", SourceKind: "dir"}
	fillRecordLink(h.DB, rec)
	if rec.SourceLink != magnet || rec.SourceLinkKind != "magnet" {
		t.Errorf("按 fid 未反查到链接: link=%q kind=%q", rec.SourceLink, rec.SourceLinkKind)
	}

	// 目录内文件的 fid 同样算命中（115 给的是目录 fid 时，整理散文件也要能对上）
	rec2 := &model.OrganizeRecord{
		Source: "x.mkv", SourceFid: "7777", SourceKind: "file",
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "9001", Name: "x.mkv", Kind: "video"}}),
	}
	fillRecordLink(h.DB, rec2)
	if rec2.SourceLink != magnet {
		t.Errorf("文件 fid 未命中: %q", rec2.SourceLink)
	}

	// 已有链接的记录不被覆盖（重新整理沿用原链接）
	rec3 := &model.OrganizeRecord{SourceFid: "9001", SourceLink: "ed2k://|file|old|1|H|/", SourceLinkKind: "ed2k"}
	fillRecordLink(h.DB, rec3)
	if rec3.SourceLink != "ed2k://|file|old|1|H|/" {
		t.Errorf("已有链接被覆盖: %q", rec3.SourceLink)
	}

	// 对不上的 fid：名字兜底（磁力产物目录名 = 任务名）
	rec4 := &model.OrganizeRecord{Source: "Some.Show.S01/", SourceFid: "8888"}
	fillRecordLink(h.DB, rec4)
	if rec4.SourceLink != magnet {
		t.Errorf("名字兜底未命中: %q", rec4.SourceLink)
	}

	// 既对不上 fid 也对不上名字：留空，不要乱认
	rec5 := &model.OrganizeRecord{Source: "别的东西/", SourceFid: "6666"}
	fillRecordLink(h.DB, rec5)
	if rec5.SourceLink != "" {
		t.Errorf("不该认领无关记录: %q", rec5.SourceLink)
	}
}

// 分享链接的指纹取 share_code，且 receive 后能把快照差集当产物 fid 存进去
func TestDownloadLinkShare(t *testing.T) {
	if _, err := model.InitDB("file:dllink_share_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	link := "https://115cdn.com/s/swzabc123?password=a1b2"
	id := dlLinkRecord(h, link, "share", "某合集", "999", "机器人")
	dlLinkSetFids(h, id, []string{"111", "222"})

	var row model.DownloadLink
	if err := h.DB.First(&row, id).Error; err != nil {
		t.Fatalf("查不到台账行: %v", err)
	}
	if row.Hash != "swzabc123" || row.Status != "done" {
		t.Errorf("分享台账字段不符: %+v", row)
	}
	rec := &model.OrganizeRecord{Source: "某片名 (2020)/", SourceFid: "222"}
	fillRecordLink(h.DB, rec)
	if rec.SourceLink != link || rec.SourceLinkKind != "share" {
		t.Errorf("分享链接未反查到: link=%q kind=%q", rec.SourceLink, rec.SourceLinkKind)
	}
}
