package api

import (
	"testing"

	"115-station/internal/model"
)

// 首次部署必须能洗版：默认策略要播种进引擎真正读的 ScrapeRule(wash_config)。
// 此前播的是 WashRule 表（没有任何代码再读），全新部署的洗版恒等于关闭
func TestDefaultWashConfigSeeded(t *testing.T) {
	db, err := model.InitDB("file:washseed_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer func() { model.DB = nil }()
	if err := model.InitDefaultWashConfig(db); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	resetWashCache()
	sts := washStrategyCache()
	if len(sts) != 2 {
		t.Fatalf("默认策略应解析出 2 条（电影/剧集），得到 %d 条", len(sts))
	}
	if st := matchWashStrategy("movie", "外语电影"); st == nil || len(st.PriorityLevel) == 0 {
		t.Fatalf("电影策略没命中或优先级为空: %+v", st)
	}
	if st := matchWashStrategy("tv", "欧美剧"); st == nil || len(st.PriorityLevel) == 0 {
		t.Fatalf("剧集策略没命中或优先级为空: %+v", st)
	}

	// 用户清空配置即表示不洗版：不再重新播种，也没有代码内兜底
	db.Model(&model.ScrapeRule{}).Where("type = ?", "wash_config").Update("config", "")
	if err := model.InitDefaultWashConfig(db); err != nil {
		t.Fatalf("二次调用不该出错: %v", err)
	}
	resetWashCache()
	if sts := washStrategyCache(); len(sts) != 0 {
		t.Errorf("清空后应当没有策略（不洗版），却拿到 %d 条", len(sts))
	}
}

// 台账里视频行是 xxx.mkv.strm，剥掉 .strm 才认得出是视频
func TestLedgerNameStripsStrm(t *testing.T) {
	sf := model.SyncedFile{RelPath: "俱乐部/电影/外语电影/P-片名-2024/片名 (2024).1080p.BluRay.mkv.strm"}
	if got := ledgerName(sf); got != "片名 (2024).1080p.BluRay.mkv" {
		t.Fatalf("ledgerName = %q", got)
	}
	if !ledgerIsVideo(sf) {
		t.Errorf("台账视频行应认作视频，否则洗版挑不出比较对象")
	}
	poster := model.SyncedFile{RelPath: "俱乐部/电影/外语电影/P-片名-2024/poster.jpg"}
	if ledgerIsVideo(poster) {
		t.Errorf("poster.jpg 不该当成视频参与画质比较")
	}
}

// 洗版让位范围：只搬输给新版的那一版 + 它的字幕。
// 此前搬的是「目标目录下台账查到的全部」，剧集那就是整整一季
func TestWashVictimScope(t *testing.T) {
	db, err := model.InitDB("file:washvictim_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	defer func() { model.DB = nil }()

	season := "俱乐部/电视剧/欧美剧/S-剧名-2024-[tmdb=9]/Season 01/"
	rows := []model.SyncedFile{
		{FileID: "v5", RelPath: season + "剧名 - S01E05 - 1080p.WEB-DL.mkv.strm", Kind: "video"},
		{FileID: "s5", RelPath: season + "剧名 - S01E05 - 1080p.WEB-DL.chs.srt", Kind: "asset"},
		{FileID: "v6", RelPath: season + "剧名 - S01E06 - 1080p.WEB-DL.mkv.strm", Kind: "video"},
		{FileID: "v7", RelPath: season + "剧名 - S01E07 - 2160p.BluRay.mkv.strm", Kind: "video"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("造数失败: %v", err)
		}
	}

	libFiles := libraryFilesOf("电视剧/欧美剧/S-剧名-2024-[tmdb=9]/Season 01", "俱乐部")
	if len(libFiles) != 4 {
		t.Fatalf("台账应查到 4 行，得到 %d 行", len(libFiles))
	}

	// 新的第 5 集（2160p 蓝光）只应顶掉第 5 集的旧版与它的字幕
	newName := "剧名 - S01E05 - 2160p.BluRay.mkv"
	cands := filterLedger(libFiles, func(n string) bool { return classifyFile(n) == FileTypeVideo })
	cands = filterLedger(cands, func(n string) bool { return parseFileName(n).Episode == 5 })
	if len(cands) != 1 || cands[0].FileID != "v5" {
		t.Fatalf("同集候选应只有 v5，得到 %+v", cands)
	}
	rules := []washRule{{ResourcePix: "2160p"}, {ResourcePix: "1080p"}}
	if !washDecision(newName, []string{ledgerName(cands[0])}, rules) {
		t.Fatalf("2160p 新版应判定优于 1080p 旧版")
	}
	// 第 6/7 集不在候选里 —— 替换第 5 集不能把同季其他集一起搬走
	for _, sf := range cands {
		if sf.FileID == "v6" || sf.FileID == "v7" {
			t.Errorf("同季其他集 %s 被卷进让位集合", sf.FileID)
		}
	}
}

// 新增集不做画质比较：库内没有这一集就直接入库
func TestWashNewEpisodeNotCompared(t *testing.T) {
	season := "俱乐部/电视剧/欧美剧/S-剧名-2024/Season 01/"
	libFiles := []model.SyncedFile{
		{FileID: "v1", RelPath: season + "剧名 - S01E01 - 2160p.BluRay.mkv.strm"},
	}
	cands := filterLedger(libFiles, func(n string) bool { return classifyFile(n) == FileTypeVideo })
	newEp := parseFileName("剧名 - S01E02 - 1080p.WEB-DL.mkv").Episode
	if newEp != 2 {
		t.Fatalf("集数解析失败: %d", newEp)
	}
	same := filterLedger(cands, func(n string) bool { return parseFileName(n).Episode == newEp })
	if len(same) != 0 {
		t.Errorf("第 2 集库内没有，不该拿第 1 集的画质去判它: %+v", same)
	}
}
