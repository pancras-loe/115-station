package api

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

func ed2kOf(name string, n int) string {
	return "ed2k://|file|" + name + "|1073741824|" + strings.Repeat(string(rune('A'+n)), 32) + "|/"
}

// 离线链接怎么合：同一批（前后脚）、同一部合成一次；有还在等的、最后一条刚提交的整批先等；跳过的各自成一个
func TestShareOutOfflineUnits(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	base := now.Add(-3 * time.Hour)
	cand := func(id uint, at time.Time, kind string, v shareOutVerdict) shareOutCand {
		return shareOutCand{Link: model.DownloadLink{ID: id, Kind: kind, CreatedAt: at}, V: v}
	}
	tv := func(tmdb int, recID uint) shareOutVerdict {
		return shareOutVerdict{TmdbID: tmdb, MediaType: "tv", Records: []model.OrganizeRecord{{ID: recID, LinkID: recID}}}
	}
	cands := []shareOutCand{
		cand(3, base.Add(10*time.Minute), "ed2k", tv(1396, 3)),
		cand(1, base, "ed2k", tv(1396, 1)),
		cand(2, base.Add(5*time.Minute), "ed2k", tv(1396, 2)),
		cand(4, base.Add(12*time.Minute), "magnet", tv(1396, 4)), // 同一部但类型不同：另起一个
		cand(5, base.Add(13*time.Minute), "ed2k", tv(550, 5)),    // 另一部
		cand(6, base.Add(14*time.Minute), "ed2k", shareOutVerdict{Skip: "整理没认出是哪一部"}),
		// 隔了一个多小时的另一批，其中一条还在等整理：整批先不发
		cand(7, base.Add(90*time.Minute), "ed2k", tv(1396, 7)),
		cand(8, base.Add(95*time.Minute), "ed2k", shareOutVerdict{Wait: true}),
	}
	units := shareOutOfflineUnits(cands, now, false)
	if len(units) != 4 {
		t.Fatalf("应合成 4 个，得到 %d: %+v", len(units), units)
	}
	if ids := unitIDs(units[0]); ids != "1,2,3" || len(units[0].V.Records) != 3 {
		t.Errorf("第一部的三条应合在一起（按提交先后），得到 %s，记录 %d 条", ids, len(units[0].V.Records))
	}
	if unitIDs(units[1]) != "4" || unitIDs(units[2]) != "5" || unitIDs(units[3]) != "6" || units[3].V.Skip == "" {
		t.Errorf("类型不同 / 另一部 / 跳过的应各自一个: %+v", units[1:])
	}
	// 合并不能改到原来的记录切片
	if len(cands[0].V.Records) != 1 {
		t.Errorf("合并改到了原来的记录: %+v", cands[0].V.Records)
	}

	// 最后一条刚提交：可能还在接着贴，整批等
	fresh := []shareOutCand{cand(1, now.Add(-40*time.Minute), "ed2k", tv(1396, 1)), cand(2, now.Add(-10*time.Minute), "ed2k", tv(1396, 2))}
	if u := shareOutOfflineUnits(fresh, now, false); len(u) != 0 {
		t.Errorf("最后一条刚提交应等，得到 %+v", u)
	}
	// 手动重试：不等，还在等整理的那条不带
	retry := []shareOutCand{cand(1, now.Add(-40*time.Minute), "ed2k", tv(1396, 1)), cand(2, now.Add(-10*time.Minute), "ed2k", shareOutVerdict{Wait: true})}
	if u := shareOutOfflineUnits(retry, now, true); len(u) != 1 || unitIDs(u[0]) != "1" {
		t.Errorf("手动重试应只发认出来的那条，得到 %+v", u)
	}
}

func unitIDs(u shareOutUnit) string {
	var ids []string
	for _, l := range u.Links {
		ids = append(ids, strconv.Itoa(int(l.ID)))
	}
	return strings.Join(ids, ",")
}

// 几条离线链接的载荷：链接换行连起来、不带访问码、集号与体积只算参与发布的几条
func TestShareOutPayloadOfLinks(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	l1 := model.DownloadLink{ID: 1, Kind: "ed2k", URL: ed2kOf("Breaking.Bad.S01E01.1080p.WEB-DL.mkv", 1), Name: "Breaking.Bad.S01E01.1080p.WEB-DL.mkv"}
	l2 := model.DownloadLink{ID: 2, Kind: "ed2k", URL: ed2kOf("Breaking.Bad.S01E02.1080p.WEB-DL.mkv", 2), Name: "Breaking.Bad.S01E02.1080p.WEB-DL.mkv"}
	rec := func(id uint, ep string) model.OrganizeRecord {
		r := shareOutRec(id, "success", 1396, "tv", old, orgRecordFile{Fid: "f" + ep, Name: "绝命毒师 - S01E" + ep + ".mkv",
			Orig: "Breaking.Bad.S01E" + ep + ".1080p.WEB-DL.mkv", Kind: "video", Size: 1 << 30})
		r.LinkID = id
		return r
	}
	v := shareOutVerdict{TmdbID: 1396, MediaType: "tv", Records: []model.OrganizeRecord{rec(1, "01"), rec(2, "02"), rec(3, "03")}}
	pl := shareOutPayloadOfLinks([]model.DownloadLink{l1, l2}, v)
	if pl.Offline != "ed2k" || pl.Code != "" || pl.URL != l1.URL+"\n"+l2.URL {
		t.Fatalf("离线字段不对: %+v", pl)
	}
	if episodesCSV(pl.Episodes[1]) != "1-2" || pl.SizeBytes != 2<<30 || pl.ShareName != "绝命毒师" {
		t.Errorf("只应算这两条链接的记录、几条合并时名字用片名: eps=%v size=%d name=%q", pl.Episodes, pl.SizeBytes, pl.ShareName)
	}
	// 单条：名字留文件名（进备注「原分享名」）
	one := shareOutPayloadOfLinks([]model.DownloadLink{l1}, v)
	if one.URL != l1.URL || one.ShareName != l1.Name {
		t.Errorf("单条离线链接: %+v", one)
	}

	// 两站的请求体
	body, err := dianShareBody(pl, shareOutSiteCfg{}, dianResourceRef{})
	if err != nil {
		t.Fatal(err)
	}
	if body["share_kind"] != "offline" || body["offline_type"] != "ed2k" || body["share_code"] != pl.URL ||
		body["receive_code"] != nil || body["title_override"] != nil || !strings.Contains(body["offline_title"].(string), "绝命毒师") ||
		body["episodes_csv"] != "1-2" {
		t.Errorf("癫影离线请求体不对: %v", body)
	}
	rb := re0ShareBody(pl, shareOutSiteCfg{})
	if rb["url"] != pl.URL || rb["access_code"] != nil {
		t.Errorf("RE0 离线请求体不对: %v", rb)
	}
}

// 端到端：自己贴的几条 ed2k 打上标记 → 整理认出同一部 → 合成一次发布，三行记账结果相同；
// 从站上拿到的那条（来源记着站名）不带进去
func TestShareOutOfflineFlow(t *testing.T) {
	h := newShareOutEnv(t)
	if err := saveShareOutCfg(shareOutCfg{Dian: shareOutSiteCfg{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	var sent []shareOutPayload
	oldPubs := shareOutPublishers
	t.Cleanup(func() { shareOutPublishers = oldPubs })
	shareOutPublishers = map[string]shareOutPublisher{
		shareOutSiteDian: func(pl shareOutPayload, cfg shareOutSiteCfg) (string, int, error) {
			sent = append(sent, pl)
			return "88", 5, nil
		},
	}
	now := time.Now()
	var links []model.DownloadLink
	for i := 1; i <= 3; i++ {
		u := ed2kOf("Breaking.Bad.S01E0"+strconv.Itoa(i)+".1080p.WEB-DL.mkv", i)
		id := dlLinkRecord(h, u, "", "", "web", nil)
		h.shareOutMark(id, "")
		var l model.DownloadLink
		h.DB.First(&l, id)
		if l.ShareOut != "dian" || l.Kind != "ed2k" {
			t.Fatalf("标记没记上: %+v", l)
		}
		links = append(links, l)
		r := shareOutRec(0, "success", 1396, "tv", now, orgRecordFile{Fid: "f" + strconv.Itoa(i), Name: "绝命毒师 - S01E0" + strconv.Itoa(i) + ".mkv",
			Orig: "Breaking.Bad.S01E0" + strconv.Itoa(i) + ".1080p.WEB-DL.mkv", Kind: "video", Size: 1 << 30})
		r.LinkID = id
		h.DB.Create(&r)
	}
	// 第三条以前从癫影解锁过（同一个 ed2k hash，来源记着站名）
	h.DB.Create(&model.DownloadLink{Kind: "ed2k", URL: links[2].URL, Hash: links[2].Hash, Source: "癫影"})

	h.shareOutRun(now.Add(shareOutSettle + time.Minute))
	if len(sent) != 0 {
		t.Fatalf("最后一条刚提交，应等着看还有没有接着贴的: %d", len(sent))
	}
	h.shareOutRun(now.Add(shareOutBatchGap + time.Minute))
	if len(sent) != 1 {
		t.Fatalf("应合成一次发布，实际 %d 次", len(sent))
	}
	pl := sent[0]
	if pl.URL != links[0].URL+"\n"+links[1].URL || episodesCSV(pl.Episodes[1]) != "1-2" {
		t.Errorf("合并的载荷不对: url=%q eps=%v", pl.URL, pl.Episodes)
	}
	var outs []model.ShareOut
	h.DB.Order("link_id").Find(&outs)
	if len(outs) != 3 || outs[0].Status != shareOutStatusDone || outs[1].Status != shareOutStatusDone || outs[0].Ref != "88" ||
		outs[0].Points != 5 || outs[1].Points != 0 || outs[2].Status != shareOutStatusSkipped {
		t.Fatalf("记账不对: %+v", outs)
	}
	h.shareOutRun(now.Add(2 * shareOutBatchGap))
	if len(sent) != 1 {
		t.Fatalf("发过的不该再发: %d", len(sent))
	}

	// HTTP 离线链接不打标记：两站都不收
	if shareOutOfflineKind(classifyLink("https://example.com/a.mkv")) {
		t.Errorf("HTTP 链接不该分享")
	}
}
