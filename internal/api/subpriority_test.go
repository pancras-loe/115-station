package api

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"115-station/internal/model"
)

// 规则里的词按词匹配：DV 不命中 DVDRip、!TS 不排掉 Ghosts；归一值里有的照样命中（WEB → WEBRip、HDR → HDR10）
func TestRuleMatchToken(t *testing.T) {
	cases := []struct {
		name string
		rule washRule
		want bool
	}{
		{"Movie.2010.1080p.DVDRip.x264.mkv", washRule{ResourceEffect: "DV"}, false},
		{"Movie.2010.2160p.WEB-DL.DV.H265.mkv", washRule{ResourceEffect: "DV"}, true},
		{"Ghosts.S01E01.1080p.WEB-DL.mkv", washRule{ResourceType: "!TS"}, true},
		{"Movie.2010.TS.mkv", washRule{ResourceType: "!TS"}, false},
		{"Show.S01E01.1080p.WEBRip.mkv", washRule{ResourceType: "WEB"}, true},
		{"Movie.2010.2160p.WEB-DL.HDR10.mkv", washRule{ResourceEffect: "HDR"}, true},
		{"Movie.2010.2160p.WEB-DL.DV.HDR.mkv", washRule{ResourceEffect: "!DV.HDR,!DV"}, false},
		{"三体 2023 4K 杜比视界", washRule{ResourcePix: "2160p"}, true},
	}
	for _, c := range cases {
		if got := ruleMatch(c.name, c.rule); got != c.want {
			t.Errorf("%s / %+v: got %v", c.name, c.rule, got)
		}
	}
}

func TestSubPriority(t *testing.T) {
	p := subPriority{Rules: []washRule{{ResourcePix: "2160p", ResourceEffect: "DV"}, {ResourcePix: "2160p,1080p"}}}
	if p.rank("剧.S01E01.2160p.WEB-DL.DV.H265.mkv") != 0 || p.rank("剧.S01E01.2160p.WEB-DL.H265.mkv") != 1 ||
		p.rank("剧.S01E01.720p.WEB-DL.mkv") != 2 {
		t.Fatal("rank = 命中第几条，没命中排最后")
	}
	// 同命中第 2 条：再按默认画质比，2160p 赢
	if p.compare("剧.S01E01.2160p.WEB-DL.mkv", "剧.S01E01.1080p.WEB-DL.mkv") <= 0 {
		t.Fatal("同一条规则里 2160p 应更好")
	}
	// 没有规则：只按默认画质，也没有达标一说
	basic := subPriority{}
	if basic.rank("剧.2160p.REMUX.DV.H265.mkv") != basic.rank("剧.480p.mkv") {
		t.Fatal("没有规则不分档")
	}
	if basic.compare("剧.1080p.BluRay.mkv", "剧.1080p.WEB-DL.mkv") <= 0 {
		t.Fatal("基础：BluRay 应好过 WEB-DL")
	}
}

// 先比命中第几条规则，同一条再补集数、再看画质
func TestSortSubCandidatesTop(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	share := func(title, code string) ResourceItem {
		return ResourceItem{Source: "pansou", Kind: "share115", Action: "transfer", Title: title, URL: "https://115.com/s/" + code, Relevant: true}
	}
	items := []ResourceItem{
		share("剧 S02E05 1080p WEB-DL", "a"),
		share("剧 S02E05-E06 1080p WEBRip", "b"),
		share("剧 S02E05 2160p WEB-DL", "c"),
		share("剧 S02E06 1080p BluRay", "d"),
	}
	prio := subPriority{Rules: []washRule{{ResourcePix: "2160p"}, {ResourcePix: "1080p"}}}
	cands, _ := planSubCandidates(items, subPickCtx{Sub: sub, Missing: epKeys("S02E05", "S02E06"), Now: now, Re0Left: -1, Prio: prio})
	var got []string
	for _, c := range cands {
		got = append(got, c.Item.URL[len("https://115.com/s/"):])
	}
	// c 命中第 1 条排第一；b 补两集；a / d 都补一集、同命中第 2 条，按片源 BluRay > WEB-DL
	if want := []string{"c", "b", "d", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("顺序 %v, want %v", got, want)
	}
}

// 2026-11-16 用户现场：前 5 条是 1080p（第 1 条 WEBRip）、后面是 2160p，订阅却挑了 36 集的 4K 整季包。
// 1080p WEB-DL 没命中第 1 条，原来和 4K 算同一档、按集数输给整季包；现在命中靠前的规则先试
func TestSortSubCandidatesTierBeforeCovers(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	share := func(title, code string) ResourceItem {
		return ResourceItem{Source: "pansou", Kind: "share115", Action: "transfer", Title: title, URL: "https://115.com/s/" + code, Relevant: true}
	}
	items := []ResourceItem{
		share("棋魂 S01E01-E36 2160p HDR WEB-DL", "uhd"),
		share("棋魂 S01E01-E10 1080p WEB-DL", "fhd"),
		share("棋魂 S01E01-E36 720p WEB-DL", "hd"),
	}
	prio := subPriority{Rules: []washRule{
		{ResourcePix: "1080p", ResourceType: "WEBRip", ResourceEffect: "!DV"},
		{ResourcePix: "1080p", ResourceType: "WEB-DL", ResourceEffect: "!DV"},
		{ResourcePix: "2160p", ResourceType: "WEB-DL", ResourceEffect: "!DV"},
	}}
	var missing []string
	for e := 1; e <= 36; e++ {
		missing = append(missing, fmt.Sprintf("S01E%02d", e))
	}
	cands, _ := planSubCandidates(items, subPickCtx{Sub: sub, Missing: epKeys(missing...), Now: now, Re0Left: -1, Prio: prio})
	var got []string
	for _, c := range cands {
		got = append(got, c.Item.URL[len("https://115.com/s/"):])
	}
	if want := []string{"fhd", "uhd", "hd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("顺序 %v, want %v", got, want)
	}
}

// 分享里同一集几份：同命中一条规则时按默认画质挑，不再只看命中第几条
func TestPickShareEpisodesCompare(t *testing.T) {
	entries := []shareEntry{
		fileE("lo", "剧.S01E01.1080p.WEB-DL.mkv", 900),
		fileE("hi", "剧.S01E01.2160p.WEB-DL.mkv", 500),
	}
	prio := subPriority{Rules: []washRule{{ResourceType: "WEB-DL"}}}
	p := pickShareEpisodes(entries, sharePickOpts{MediaType: "tv", Missing: map[epKey]bool{{1, 1}: true}, Compare: prio.compareFiles})
	if got := pickIDs(p); !reflect.DeepEqual(got, []string{"hi"}) {
		t.Fatalf("挑中 %v", got)
	}
}

// testSubCfg 测试用的订阅设置：中字默认开（2026-10-22）之后，测试里的资源名大多没写字幕，关掉免得个个被拦
func testSubCfg() subscribeCfg {
	c := defaultSubscribeCfg()
	c.Cond = subCond{}
	return c
}

// prioAny 命中一切的规则（只写了排除词）：付费资源要「画质至少命中一条规则」，测试标题不写画质时用它
var prioAny = subPriority{Rules: []washRule{{ResourcePix: "!480p"}}}

// 老配置迁移：资源条件写了画质几项 → 自定义 + 前 1 条（行为不变）；没写 → 跟随洗版策略；中字没存过的不替用户打开
func TestMigrateSubPrioGlobal(t *testing.T) {
	c := defaultSubscribeCfg()
	c.Cond = subCond{Pix: "2160p", Type: "WEB-DL", Zh: true, MaxGB: 20}
	m := migrateSubPrioGlobal(c, true)
	if m.Priority.Mode != subPrioCustom || m.Priority.Limit != 1 || len(m.Priority.Rules) != 1 ||
		m.Priority.Rules[0].ResourcePix != "2160p" || m.Priority.Rules[0].ResourceType != "WEB-DL" {
		t.Fatalf("写了画质的应迁成自定义: %+v", m.Priority)
	}
	if m.Cond != (subCond{Zh: true, MaxGB: 20}) || m.PrioVer != subPrioVer {
		t.Fatalf("条件只留中字与体积: %+v", m.Cond)
	}
	if m := migrateSubPrioGlobal(defaultSubscribeCfg(), false); m.Priority.Mode != subPrioFollow || m.Cond.Zh {
		t.Fatalf("没写画质 / 没存过条件: %+v %+v", m.Priority, m.Cond)
	}
}

func TestMigrateSubPrioSub(t *testing.T) {
	// 自己写了画质：自定义 + 前 1 条
	s := model.Subscription{Cond: `{"pix":"1080p","zh":true}`}
	if !migrateSubPrioSub(&s, false) || s.RankLimit != 1 || s.Cond != `{"pix":"","type":"","effect":"","video":"","audio":"","team":"","zh":true,"min_gb":0,"max_gb":0}` {
		t.Fatalf("自己写了画质: %+v", s)
	}
	if sel := parseSubPrioSel(s.Priority); sel == nil || sel.Mode != subPrioCustom || sel.Rules[0].ResourcePix != "1080p" {
		t.Fatalf("优先级: %q", s.Priority)
	}
	// 自定义成不限、订阅设置原来写了画质：跟随洗版策略，不被迁过去的订阅设置套上
	s = model.Subscription{Cond: `{}`}
	if !migrateSubPrioSub(&s, true) || parseSubPrioSel(s.Priority).Mode != subPrioFollow {
		t.Fatalf("不限: %+v", s)
	}
	// 跟随订阅设置的不动
	s = model.Subscription{}
	if migrateSubPrioSub(&s, true) || s.Priority != "" {
		t.Fatalf("跟随的不该动: %+v", s)
	}
}

// 订阅自己选了模式用自己的与 RankLimit；跟随订阅设置时 RankLimit 盖过订阅设置的前 N 条
func TestSubPrioMode(t *testing.T) {
	cfg := defaultSubscribeCfg()
	cfg.Priority = subPrioCfg{Mode: subPrioCustom, Rules: []washRule{{ResourcePix: "2160p"}}, Limit: 2}
	if m, r, l := subPrioMode(cfg, &model.Subscription{}); m != subPrioCustom || len(r) != 1 || l != 2 {
		t.Fatalf("跟随订阅设置: %s %v %d", m, r, l)
	}
	if _, _, l := subPrioMode(cfg, &model.Subscription{RankLimit: 1}); l != 1 {
		t.Fatalf("订阅的门槛优先: %d", l)
	}
	if m, _, l := subPrioMode(cfg, &model.Subscription{Priority: `{"mode":"basic"}`}); m != subPrioBasic || l != 0 {
		t.Fatalf("自己选基础: %s %d", m, l)
	}
}

// 「前 N 条」三态：标题没写的分享到文件一级再判，文件名没写看标题，都没写算不符
func TestSubPriorityLimitVerdict(t *testing.T) {
	p := subPriority{Rules: []washRule{{ResourcePix: "2160p", ResourceEffect: "DV"}, {ResourcePix: "1080p"}}, Limit: 1}
	for title, want := range map[string]condVerdict{
		"剧 S01 2160p DV":   condOK,
		"剧 S01 1080p":      condFail,
		"剧 S01 全集":         condUnknown,
		"剧 S01 2160p HDR10": condFail, // 写了 HDR10、不是 DV
	} {
		if v, why := p.titleVerdict(title, resTags{}); v != want {
			t.Errorf("%s: %v（%s）", title, v, why)
		}
	}
	if ok, _ := p.fileOK("01.2160p.DV.mkv", "剧 全集", resTagsOf("剧 全集")); !ok {
		t.Fatal("文件名写了")
	}
	if ok, _ := p.fileOK("01.mkv", "剧 全集 2160p DV", resTagsOf("剧 全集 2160p DV")); !ok {
		t.Fatal("文件名没写看标题")
	}
	if ok, why := p.fileOK("01.mkv", "剧 全集", resTagsOf("剧 全集")); ok || why == "" {
		t.Fatal("都没写算不符")
	}
	if ok, _ := (subPriority{Rules: p.Rules}).fileOK("01.mkv", "", resTags{}); !ok {
		t.Fatal("没有前 N 条就不过滤")
	}
}
