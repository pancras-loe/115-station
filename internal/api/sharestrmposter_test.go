package api

import (
	"testing"

	"115-station/internal/model"
)

// 本地整理的分享进海报墙（列表、详情、刮削取视频），但不进台账聚合（订阅、总览、分类改名都按台账）
func TestShareTitlesOnPosterWall(t *testing.T) {
	newTestDB(t, "shareposter.db")
	db := model.DB
	db.Create(&model.Setting{Key: "sharestrm", Value: `{"enabled":true,"root":"分享"}`})
	org := model.ShareSource{ShareCode: "swsposter1", LocalDir: "某剧", Layout: shareLayoutOrganize}
	keep := model.ShareSource{ShareCode: "swsposter2", LocalDir: "原样片", Layout: shareLayoutKeep}
	db.Create(&org)
	db.Create(&keep)
	db.Create(&model.ShareFile{SourceID: org.ID, FileID: "1", Kind: "video", RelPath: "剧集/某剧 (2025)/Season 01/某剧.S01E01.strm"})
	db.Create(&model.ShareFile{SourceID: org.ID, FileID: "2", Kind: "video", RelPath: "剧集/某剧 (2025)/Season 01/某剧.S01E02.strm"})
	db.Create(&model.ShareFile{SourceID: org.ID, FileID: "3", Kind: "video", Blocked: true, RelPath: "剧集/某剧 (2025)/Season 01/某剧.S01E03.strm"})
	db.Create(&model.ShareFile{SourceID: keep.ID, FileID: "9", Kind: "video", RelPath: "原样片/a.strm"})

	key := "分享/剧集/某剧 (2025)"
	share := scanShareTitles()
	e := share[key]
	if len(share) != 1 || e == nil || e.MediaType != "tv" || e.Videos != 2 || e.Title != "某剧" {
		t.Fatalf("分享片目：%v %+v", share, e)
	}
	if posterLedgerTitles(true)[key] == nil {
		t.Fatal("海报墙里要有分享片目")
	}
	if scanLedgerTitles()[key] != nil {
		t.Fatal("分享片目不能进台账聚合（订阅会当成库里已有）")
	}
	rows := scrapeDirVideoRows(key)
	if len(rows) != 2 || rows[0].PickCode != "s~swsposter1~1" && rows[1].PickCode != "s~swsposter1~1" {
		t.Fatalf("刮削取视频：%+v", rows)
	}
	if got := shareVideoRowsByTitle()[key]; len(got) != 2 {
		t.Fatalf("卡片视频行：%d", len(got))
	}
	// 刮削策略：本站刮削关着按「不刮」，开着按「只手动刮」
	if m := loadLibScrapePolicy().of(key); m != libScrapeOff {
		t.Fatalf("刮削关着：%s", m)
	}
	db.Model(&model.Setting{}).Where("`key` = ?", "sharestrm").Update("value", `{"enabled":true,"root":"分享","scrape":true}`)
	if m := loadLibScrapePolicy().of(key); m != libScrapeManual {
		t.Fatalf("刮削开着：%s", m)
	}
}
