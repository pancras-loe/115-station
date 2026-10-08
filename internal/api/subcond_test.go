package api

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"115-station/internal/model"
)

func TestCondField(t *testing.T) {
	cases := []struct {
		name, cond, value string
		want              condVerdict
	}{
		{"剧.S01E01.2160p.WEB-DL.mkv", "", "", condOK},
		{"剧.S01E01.2160p.WEB-DL.mkv", "2160p,4k", "2160p", condOK},
		{"剧.S01E01.1080p.WEB-DL.mkv", "2160p,4k", "1080p", condFail}, // 认出了别的值
		{"剧.S01E01.mkv", "2160p,4k", "", condUnknown},                // 没写
		{"剧.S01E01.2160p.DV.mkv", "!DV", "DV", condFail},             // 命中排除词
		{"剧.S01E01.2160p.mkv", "!DV", "", condOK},                    // 只有排除词、没命中
		{"剧 4K 杜比视界", "2160p", "2160p", condOK},                      // 中文标题的 4K 由标签认成 2160p
		{"剧 4K杜比视界", "杜比视界,dv", "", condOK},                          // 汉字挨着也算单独出现
		{"剧.1080p.DVDRip.mkv", "!dv", "", condOK},                    // DVDRip 里的 dv 不算杜比视界
		{"剧.1080p.DVDRip.mkv", "dv", "", condUnknown},
		{"剧.2160p.DV.HDR.mkv", "!dv", "DV", condFail},
		{"剧.Atmos.mkv", "!ts", "", condOK}, // 排除 TS 不误伤带 ts 字母的名字
		{"剧.HDR10+.mkv", "hdr10+", "HDR10+", condOK},
		{"剧.H.265.mkv", "h265,x265,hevc", "H265", condOK}, // 名字写 H.265，靠归一值命中
	}
	for _, c := range cases {
		if got := condField(c.name, c.cond, c.value); got != c.want {
			t.Errorf("condField(%q, %q, %q) = %d, want %d", c.name, c.cond, c.value, got, c.want)
		}
	}
}

func TestSubCondTitleAndFile(t *testing.T) {
	c := subCond{Pix: "2160p", Type: "WEB-DL,BluRay", Zh: true}
	verdict := func(title string) (condVerdict, string) {
		return c.titleVerdict(title, resTagsOf(title), false, 0)
	}
	if v, why := verdict("剧 S01 2160p WEB-DL 中字"); v != condOK {
		t.Fatalf("都符合: %d %s", v, why)
	}
	if v, why := verdict("剧 S01 1080p WEB-DL 中字"); v != condFail || why != "分辨率不符" {
		t.Fatalf("分辨率不符: %d %s", v, why)
	}
	if v, why := verdict("剧 S01 全集"); v != condUnknown || why != "标题没写分辨率" {
		t.Fatalf("标题什么都没写: %d %s", v, why)
	}
	if v, why := verdict("剧 S01 2160p WEB-DL"); v != condUnknown || why != "标题没写中字" {
		t.Fatalf("没写中字: %d %s", v, why)
	}

	// 电影按标题体积判；剧集的资源体积是整包的，不判
	m := subCond{MaxGB: 30}
	if v, why := m.titleVerdict("片 2160p", resTagsOf("片 2160p"), true, 60<<30); v != condFail || why != "体积太大" {
		t.Fatalf("电影体积: %d %s", v, why)
	}
	if v, _ := m.titleVerdict("剧 S01", resTagsOf("剧 S01"), false, 60<<30); v != condOK {
		t.Fatal("剧集不按整包体积判")
	}

	title := "剧 S01 4K WEB-DL"
	tt := resTagsOf(title)
	file := func(name string, size int64, hasSub bool) (bool, string) {
		return c.fileOK(name, size, title, tt, hasSub)
	}
	if ok, why := file("剧.S01E01.2160p.WEB-DL.mkv", 2<<30, true); !ok {
		t.Fatalf("文件名符合、有字幕: %s", why)
	}
	if ok, why := file("01.mkv", 2<<30, true); !ok {
		t.Fatalf("文件名没写的看资源标题: %s", why)
	}
	if ok, why := file("剧.S01E01.1080p.mkv", 2<<30, true); ok || why != "分辨率不符" {
		t.Fatalf("文件名写了别的分辨率，不看标题: %v %s", ok, why)
	}
	if ok, why := file("01.mkv", 2<<30, false); ok || why != "没有中字" {
		t.Fatalf("没有中字: %v %s", ok, why)
	}
	if ok, why := c.fileOK("01.mkv", 2<<30, "剧 S01 全集", resTagsOf("剧 S01 全集"), true); ok || why != "没写分辨率" {
		t.Fatalf("文件名与标题都没写算不符: %v %s", ok, why)
	}
	s := subCond{MinGB: 1}
	if ok, why := s.fileOK("01.mkv", 300<<20, "", resTags{}, false); ok || why != "体积太小" {
		t.Fatalf("体积太小: %v %s", ok, why)
	}
}

func TestSubCondOf(t *testing.T) {
	cfg := subscribeCfg{Cond: subCond{Pix: "2160p"}}
	if c := subCondOf(cfg, ""); c.Pix != "2160p" {
		t.Fatalf("空 = 跟随全局: %+v", c)
	}
	if c := subCondOf(cfg, "{}"); !c.empty() {
		t.Fatalf(`"{}" = 自定义成不限: %+v`, c)
	}
	if c := subCondOf(cfg, `{"pix":"1080p，720p","max_gb":-1}`); c.Pix != "1080p,720p" || c.MaxGB != 0 {
		t.Fatalf("订阅自己的条件要归一: %+v", c)
	}
}

// 挑资源：标题不符的丢；没写的分享留着到文件一级判，磁力不下，RE0 要花积分的不碰
func TestPlanSubCandidatesCond(t *testing.T) {
	now := time.Now()
	sub := &model.Subscription{ID: 1, MediaType: "tv", Title: "剧"}
	missing := []epKey{{S: 1, E: 1}, {S: 1, E: 2}}
	pts := 10
	it := func(action, title, hash string) ResourceItem {
		r := ResourceItem{Source: "tg", Kind: "magnet", Action: action, Title: title, Relevant: true, Rank: 0, Tags: resTagsOf(title)}
		switch action {
		case "offline":
			r.URL = "magnet:?xt=urn:btih:" + hash
		case "transfer":
			r.Kind, r.URL = "share115", "https://115.com/s/"+hash
		case "unlock":
			r.Source, r.Kind, r.Ref, r.Points = "re0", "share115", hash, &pts
		}
		return r
	}
	items := []ResourceItem{
		it("transfer", "剧 S01 2160p", "s1"),
		it("transfer", "剧 S01 1080p", "s2"), // 不符
		it("transfer", "剧 S01 全集", "s3"),    // 没写：分享留着
		it("offline", "剧 S01 全集", "m1"),     // 没写：磁力不下
		it("offline", "剧 S01 2160p", "m2"),
		it("unlock", "剧 S01 全集", "r1"), // 没写：不花积分
	}
	skipped := map[string]int{}
	cands, _ := planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 50, Re0Left: -1,
		Cond: subCond{Pix: "2160p"}, Skipped: skipped})
	var got []string
	for _, c := range cands {
		got = append(got, c.Item.Title+"|"+c.Item.Action)
	}
	sort.Strings(got)
	want := []string{"剧 S01 2160p|offline", "剧 S01 2160p|transfer", "剧 S01 全集|transfer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("候选 = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(skipped, map[string]int{"分辨率不符": 1, "磁力标题没写分辨率": 1, "要花积分、标题没写分辨率": 1}) {
		t.Fatalf("跳过原因 = %v", skipped)
	}
}

// 分享内挑文件：不符合条件的不挑，同一集有符合的就用符合的；原因进 summary
func TestPickShareEpisodesCond(t *testing.T) {
	cond := subCond{Pix: "2160p", Zh: true}
	title := "剧 S01"
	entries := []shareEntry{
		{ID: "1", Name: "剧.S01E01.1080p.mkv", Dir: "剧", Size: 2 << 30},
		{ID: "2", Name: "剧.S01E01.2160p.mkv", Dir: "剧", Size: 4 << 30},
		{ID: "3", Name: "剧.S01E01.2160p.chs.ass", Dir: "剧", Size: 1},
		{ID: "4", Name: "剧.S01E02.2160p.mkv", Dir: "剧", Size: 4 << 30}, // 没字幕
		{ID: "5", Name: "剧.S01E03.mkv", Dir: "剧", Size: 4 << 30},       // 文件名和标题都没写分辨率
	}
	o := sharePickOpts{
		MediaType: "tv", Missing: map[epKey]bool{{S: 1, E: 1}: true, {S: 1, E: 2}: true, {S: 1, E: 3}: true},
		Accept: func(e shareEntry, hasSub bool) (bool, string) {
			return cond.fileOK(e.Name, e.Size, title, resTagsOf(title), hasSub)
		},
	}
	p := pickShareEpisodes(entries, o)
	var ids []string
	for _, e := range p.Picks {
		ids = append(ids, e.ID)
	}
	if !reflect.DeepEqual(ids, []string{"2", "3"}) || len(p.Covered) != 1 {
		t.Fatalf("应只挑 E01 的 2160p（带字幕）: %v %v", ids, p.Covered)
	}
	if !reflect.DeepEqual(p.Rejected, map[string]int{"分辨率不符": 1, "没有中字": 1, "没写分辨率": 1}) {
		t.Fatalf("不符原因 = %v", p.Rejected)
	}
	if s := p.summary(); s != "分享里 4 个视频，挑中 1 集，不符合资源条件的 分辨率不符 1、没写分辨率 1、没有中字 1" {
		t.Fatalf("summary = %q", s)
	}
}
