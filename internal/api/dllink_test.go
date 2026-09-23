package api

import (
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 离线这条主链路：提交登记 → 监视器回填 file_id/任务名 → 整理记录认领到这条链接。
// 这条链断了，整理记录上就永远看不到来源链接
func TestDownloadLinkOfflineFlow(t *testing.T) {
	if _, err := model.InitDB("file:dllink_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}

	magnet := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Some.Show.S01"
	dlLinkRecord(h, magnet, "", "", "web", nil)
	var row model.DownloadLink
	if err := h.DB.Last(&row).Error; err != nil {
		t.Fatalf("登记未落库: %v", err)
	}
	id := row.ID
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
	if row.Name != "Some.Show.S01" {
		t.Errorf("回填后任务名不符: %+v", row)
	}
	if fids := unmarshalStrs(row.ResultFids); len(fids) != 1 || fids[0] != "9001" {
		t.Errorf("产物 fid 未回填: %q", row.ResultFids)
	}

	// 认领：整理记录的 SourceFid 命中产物 fid
	rec := &model.OrganizeRecord{Source: "Some.Show.S01/", SourceFid: "9001", SourceKind: "dir", Status: "success"}
	if got := dlLinkMatch(h.DB, rec); got != id {
		t.Errorf("fid 未认领到链接: got=%d want=%d", got, id)
	}
	// 一条链接可以被多条记录认领（先失败后重做、合集拆成几条）
	if got := dlLinkMatch(h.DB, &model.OrganizeRecord{Source: "Some.Show.S01/", SourceFid: "9001", Status: "failed"}); got != id {
		t.Errorf("第二条记录未认领到同一链接: got=%d want=%d", got, id)
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
	dlLinkRecord(h, ed2k, "", "", "web", nil)
	var row model.DownloadLink
	if err := h.DB.Last(&row).Error; err != nil {
		t.Fatalf("登记未落库: %v", err)
	}
	dlLinkSyncTask(h, offlineTaskInfo{key: "FEDCBA0987654321FEDCBA0987654321", name: "x.mkv", fileID: "9001", status: 2})

	// Source 故意与任务名不同，只能靠文件 fid 对上
	rec := &model.OrganizeRecord{
		Source: "y.mkv", SourceFid: "7777", SourceKind: "file", Status: "success",
		Files: marshalRecordFiles([]orgRecordFile{{Fid: "9001", Name: "y.mkv", Kind: "video"}}),
	}
	if got := dlLinkMatch(h.DB, rec); got != row.ID {
		t.Errorf("文件 fid 未命中: got=%d want=%d", got, row.ID)
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
	dlLinkRecord(h, link, "share", "某合集", "机器人", []string{"某片名.2020.1080p", "说明.txt"})

	var row model.DownloadLink
	if err := h.DB.Last(&row).Error; err != nil {
		t.Fatalf("登记未落库: %v", err)
	}
	if row.Hash != "swzabc123" || row.Source != "机器人" {
		t.Errorf("分享记录字段不符: %+v", row)
	}

	// 转存后 115 保留原名，整理记录的 Source 就是那个名字（目录带尾斜杠）
	if got := dlLinkMatch(h.DB, &model.OrganizeRecord{Source: "某片名.2020.1080p/", SourceFid: "555"}); got != row.ID {
		t.Errorf("按名字未认领到: got=%d want=%d", got, row.ID)
	}
	// 无关内容不该被认领
	if got := dlLinkMatch(h.DB, &model.OrganizeRecord{Source: "乙/", SourceFid: "666"}); got != 0 {
		t.Errorf("不该认领无关链接: got=%d", got)
	}
}

// 列表带出来源链接：LinkID 为 0 或指向已清理的链接时不带
func TestRecordLinksLookup(t *testing.T) {
	if _, err := model.InitDB("file:dllink_lookup_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	model.DB.Where("1=1").Delete(&model.DownloadLink{})
	h := &Handler{DB: model.DB, Config: &config.Config{}}
	dlLinkRecord(h, "magnet:?xt=urn:btih:1111111111111111111111111111111111111111", "", "", "web", nil)
	var row model.DownloadLink
	if err := h.DB.Last(&row).Error; err != nil {
		t.Fatalf("登记未落库: %v", err)
	}
	recs := []model.OrganizeRecord{{ID: 1, LinkID: row.ID}, {ID: 2}, {ID: 3, LinkID: row.ID + 100}}
	links := recordLinks(h.DB, recs)
	if d := toRecordDTO(recs[0], links); d.Link == nil || d.Link.URL != row.URL {
		t.Errorf("来源链接未带出: %+v", d.Link)
	}
	if d := toRecordDTO(recs[1], links); d.Link != nil {
		t.Errorf("无来源的记录不该带链接: %+v", d.Link)
	}
	if d := toRecordDTO(recs[2], links); d.Link != nil {
		t.Errorf("已清理的链接不该带出: %+v", d.Link)
	}
}

// 存量补认领：旧版 record_id 精确回填 + 旧版落空的记录重新认领；
// 比链接更早的记录不能认到它，做完一次打标记不再重跑
func TestBackfillRecordLinks(t *testing.T) {
	if _, err := model.InitDB("file:dllink_backfill_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := model.DB
	db.Where("1=1").Delete(&model.DownloadLink{})
	db.Where("1=1").Delete(&model.OrganizeRecord{})
	db.Where("key = ?", recordLinkBackfillKey).Delete(&model.Setting{})
	// 旧版库里还留着这一列（GORM 不删列），新建的测试库要手动补上
	if !db.Migrator().HasColumn(&model.DownloadLink{}, "record_id") {
		if err := db.Exec("ALTER TABLE download_links ADD COLUMN record_id integer DEFAULT 0").Error; err != nil {
			t.Fatalf("补旧列: %v", err)
		}
	}
	now := time.Now()
	early := model.OrganizeRecord{Source: "合集第二部/", SourceFid: "3", Status: "success", CreatedAt: now.Add(-3 * time.Hour)}
	db.Create(&early)
	link := model.DownloadLink{Kind: "share", URL: "https://115cdn.com/s/abc", ResultNames: marshalStrs([]string{"合集第一部", "合集第二部"}),
		CreatedAt: now.Add(-2 * time.Hour)}
	db.Create(&link)
	first := model.OrganizeRecord{Source: "改过名的目录/", SourceFid: "1", Status: "success", CreatedAt: now.Add(-time.Hour)}
	second := model.OrganizeRecord{Source: "合集第二部/", SourceFid: "2", Status: "success", CreatedAt: now.Add(-time.Hour)}
	db.Create(&first)
	db.Create(&second)
	db.Exec("UPDATE download_links SET record_id = ? WHERE id = ?", first.ID, link.ID)

	BackfillRecordLinks(db)

	get := func(id uint) uint {
		var r model.OrganizeRecord
		db.First(&r, id)
		return r.LinkID
	}
	if got := get(first.ID); got != link.ID {
		t.Errorf("旧 record_id 未回填: got=%d want=%d", got, link.ID)
	}
	if got := get(second.ID); got != link.ID {
		t.Errorf("旧版落空的记录未重新认领: got=%d want=%d", got, link.ID)
	}
	if got := get(early.ID); got != 0 {
		t.Errorf("比链接早的记录不该认到它: got=%d", got)
	}

	// 标记已写：再跑一次不动任何东西
	db.Model(&model.OrganizeRecord{}).Where("id = ?", second.ID).Update("link_id", 0)
	BackfillRecordLinks(db)
	if got := get(second.ID); got != 0 {
		t.Errorf("补认领不该重复执行: got=%d", got)
	}
}
