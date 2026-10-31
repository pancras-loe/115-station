package api

import (
	"testing"
	"time"

	"115-station/internal/model"
)

func TestSubDelivery(t *testing.T) {
	cfg := defaultSubscribeCfg()
	sub := &model.Subscription{}
	if cfg.deliveryOf(sub) != subDeliverTransfer {
		t.Fatal("默认转存")
	}
	cfg.Delivery = subDeliverStrm
	if cfg.deliveryOf(sub) != subDeliverStrm || cfg.offlineModeOf(sub) != subOfflineShareOnly {
		t.Fatal("设置成分享 STRM：跟随的订阅也是，离线策略只挑分享")
	}
	sub.Delivery = subDeliverTransfer
	if cfg.deliveryOf(sub) != subDeliverTransfer || cfg.offlineModeOf(sub) != cfg.OfflineMode {
		t.Fatal("订阅自己选了转存按自己的")
	}
}

// 订阅建的分享 STRM：只建挑中的文件、同一个分享再挑并进来、算已有、结算看写出了哪几集、认定失效后不算
func TestSubShareStrmFlow(t *testing.T) {
	newTestDB(t, "subsharestrm.db")
	db := model.DB
	h := &Handler{DB: db}
	root := t.TempDir()
	withShareRoot(t, root)
	sub := model.Subscription{TmdbID: 9, MediaType: "tv", Title: "某剧"}
	db.Create(&sub)

	cfg := loadShareStrmCfg()
	src, err := subShareSource(h, cfg, &sub, "swssub001", "abcd", "某剧 2025", []string{"1"})
	if err != nil || src.SubID != sub.ID || src.TmdbID != 9 || !src.Manual || src.Layout != shareLayoutOrganize || src.FollowHours != subShareFollowDefault {
		t.Fatalf("新建：%+v %v", src, err)
	}
	src, _ = subShareSource(h, cfg, &sub, "swssub001", "abcd", "某剧 2025", []string{"2"})
	db.First(&src, src.ID)
	if only := shareOnlyFidSet(src.OnlyFids); !only["1"] || !only["2"] || len(only) != 2 {
		t.Fatalf("并进来：%s", src.OnlyFids)
	}

	// 生成只建挑中的（原样放，验证过滤；本地整理的落点另有测试）
	db.Model(&src).Updates(map[string]any{"layout": shareLayoutKeep, "only_fids": `["1"]`})
	db.First(&src, src.ID)
	mb := int64(1 << 20)
	r := &shareStrmRun{
		db: db, src: &src, cfg: shareStrmCfg{}, root: root, domain: "http://nas", cookie: "ck",
		walk: func(string, string, string, int) ([]shareEntry, string, bool, error) {
			return []shareEntry{
				{ID: "1", Name: "某剧.S01E01.mkv", Size: 900 * mb},
				{ID: "2", Name: "某剧.S01E02.mkv", Size: 900 * mb},
			}, "某剧", false, nil
		},
		stop: func() bool { return false }, set: func(string, int, int, string) {}, now: time.Now,
	}
	res, _, err := r.run()
	if err != nil || res.Videos != 1 {
		t.Fatalf("只建挑中的：%+v %v", res, err)
	}

	att := model.SubAttempt{SubID: sub.ID, Kind: "sharestrm", Status: subAttemptInflight, ShareSourceID: src.ID,
		Episodes: marshalEpKeys([]epKey{{1, 1}, {1, 2}}), CreatedAt: time.Now()}
	db.Create(&att)
	eps, found := subShareHave(db, sub.ID)
	if !found || !eps[epKey{1, 1}] || eps[epKey{1, 2}] {
		t.Fatalf("已有：%v", eps)
	}
	if r := settleShareStrmAttempt(db, &sub, &att, time.Now()); r.Status != subAttemptPartial {
		t.Fatalf("只建出一集：%+v", r)
	}
	db.Model(&src).Update("only_fids", `["1","2"]`)
	db.First(&src, src.ID)
	r.src = &src
	r.run()
	if r := settleShareStrmAttempt(db, &sub, &att, time.Now()); r.Status != subAttemptIngested {
		t.Fatalf("都建出来：%+v", r)
	}

	// 盘点把它们算进已有
	have := subHave{Eps: map[epKey]bool{}}
	mergeShareHave(db, &sub, &have)
	if !have.Found || len(have.Eps) != 2 {
		t.Fatalf("盘点：%+v", have)
	}
	// 认定失效：不算已有，尝试判失败
	db.Model(&src).Update("gone_streak", shareGoneStreakMax)
	if eps, _ := subShareHave(db, sub.ID); len(eps) != 0 {
		t.Fatal("失效的分享不算已有")
	}
	if r := settleShareStrmAttempt(db, &sub, &att, time.Now()); r.Status != subAttemptFailed {
		t.Fatalf("失效后结算：%+v", r)
	}
}
