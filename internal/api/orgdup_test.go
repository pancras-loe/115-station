package api

import (
	"strings"
	"testing"
)

// 2026-10-06 现场：越狱第 4 季第 22 集粤语 / 英语两份，命名模板不带语言，改出同一个名字
var (
	pbCantonese = remoteFile{Fid: "yue", Name: "越狱.Prison.Break.S04E22.粤语.2008.BluRay.REMUX.1080p.SDR.x264.AVC.DTS-HD.MA.5.1-LGNB.mkv", Size: 669980000, Sha1: "AAA"}
	pbEnglish   = remoteFile{Fid: "eng", Name: "越狱.Prison.Break.S04E22.英语.2008.BluRay.REMUX.1080p.SDR.x264.AVC.DTS-HD.MA.5.1-LGNB.mkv", Size: 9050000000, Sha1: "BBB"}
	pbE21       = remoteFile{Fid: "e21", Name: "越狱.Prison.Break.S04E21.2008.BluRay.REMUX.1080p.mkv", Size: 9000000000, Sha1: "CCC"}
)

// 落点只看集号：模拟命名模板丢掉了语言
func targetByEpisode(v remoteFile) (string, string) {
	p := parseFileName(v.Name)
	ep := "S04E" + map[int]string{21: "21", 22: "22"}[p.Episode]
	return "剧集/越狱/Season 4/越狱." + ep + ".mkv", ep
}

func TestSettleCollisionsHoldsDifferentFiles(t *testing.T) {
	out := settleCollisions([]remoteFile{pbE21, pbCantonese, pbEnglish}, targetByEpisode, nil, nil)
	if len(out.keep) != 1 || out.keep[0].Fid != "e21" {
		t.Fatalf("没撞名的照常入库，keep = %+v", out.keep)
	}
	if len(out.held) != 1 || !out.heldFids["yue"] || !out.heldFids["eng"] || len(out.drop) != 0 {
		t.Fatalf("撞名的两份应停下，held = %+v drop = %+v", out.held, out.drop)
	}
	g := out.held[0]
	if g.Episode != "S04E22" || g.Recommend != "" {
		t.Fatalf("没配策略不推荐：%+v", g)
	}
	labels := map[string]string{}
	for _, f := range g.Files {
		labels[f.Fid] = f.Label
	}
	if labels["yue"] != "粤语" || labels["eng"] != "英语" {
		t.Fatalf("区别词：%v", labels)
	}
	if msg := dupHoldMessage(out.held); !strings.Contains(msg, "S04E22") || !strings.Contains(msg, "粤语 / 英语") {
		t.Fatalf("提示：%s", msg)
	}
}

func TestSettleCollisionsSameSha1NotAsked(t *testing.T) {
	copy := pbEnglish
	copy.Fid = "eng2"
	out := settleCollisions([]remoteFile{pbEnglish, copy}, targetByEpisode, nil, nil)
	if len(out.held) != 0 || len(out.keep) != 1 || len(out.drop) != 1 || out.copies != 1 || out.drop[0].Fid != "eng2" {
		t.Fatalf("sha1 相同不问、留第一份：%+v", out)
	}
}

func TestSettleCollisionsApplyChoice(t *testing.T) {
	g := settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, nil, nil).held

	// 只留英语
	choice, err := buildDupChoice(g, []dupAction{{Action: "keep", Fid: "eng"}})
	if err != nil {
		t.Fatal(err)
	}
	out := settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, nil, choice)
	if len(out.held) != 0 || len(out.keep) != 1 || out.keep[0].Fid != "eng" || len(out.drop) != 1 || len(out.variants) != 0 {
		t.Fatalf("留英语：%+v", out)
	}

	// 都留：没有推荐时按体积，英语（9GB）是 A
	choice, _ = buildDupChoice(g, []dupAction{{Action: "keep_all"}})
	out = settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, nil, choice)
	if len(out.keep) != 2 || out.variants["eng"] != "A" || out.variants["yue"] != "B" {
		t.Fatalf("都留：%+v", out.variants)
	}

	// 都不要
	choice, _ = buildDupChoice(g, []dupAction{{Action: "drop_all"}})
	out = settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, nil, choice)
	if len(out.keep) != 0 || len(out.drop) != 2 {
		t.Fatalf("都不要：%+v", out)
	}

	// 选完之后又多出一份：照样再问
	third := remoteFile{Fid: "man", Name: "越狱.Prison.Break.S04E22.国语.2008.mkv", Sha1: "DDD"}
	out = settleCollisions([]remoteFile{pbCantonese, pbEnglish, third}, targetByEpisode, nil, choice)
	if len(out.held) != 1 || len(out.held[0].Files) != 3 {
		t.Fatalf("多出一份应重新问：%+v", out)
	}
}

func TestDupRecommendOnlyWhenWashDecides(t *testing.T) {
	rank := func(name string) int {
		if strings.Contains(name, "2160p") {
			return 0
		}
		return 1
	}
	uhd := remoteFile{Fid: "uhd", Name: "越狱.S04E22.2160p.mkv", Size: 1, Sha1: "X"}
	fhd := remoteFile{Fid: "fhd", Name: "越狱.S04E22.1080p.mkv", Size: 2, Sha1: "Y"}
	g := settleCollisions([]remoteFile{fhd, uhd}, targetByEpisode, rank, nil).held[0]
	if g.Recommend != "uhd" || g.Reason == "" {
		t.Fatalf("洗版分得出高下时推荐：%+v", g)
	}
	// 推荐的那份排 A，哪怕它更小
	choice, _ := buildDupChoice([]dupGroup{g}, []dupAction{{Action: "keep_all"}})
	if choice["uhd"] != "A" || choice["fhd"] != "B" {
		t.Fatalf("字母：%v", choice)
	}
	// 并列第一：不推荐
	g = settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, func(string) int { return 0 }, nil).held[0]
	if g.Recommend != "" {
		t.Fatalf("并列不推荐：%+v", g)
	}
}

func TestBuildDupChoiceValidates(t *testing.T) {
	g := settleCollisions([]remoteFile{pbCantonese, pbEnglish}, targetByEpisode, nil, nil).held
	if _, err := buildDupChoice(g, nil); err == nil {
		t.Fatal("组数对不上应报错")
	}
	if _, err := buildDupChoice(g, []dupAction{{Action: "keep", Fid: "nope"}}); err == nil {
		t.Fatal("不在组里的文件应报错")
	}
	if _, err := buildDupChoice(g, []dupAction{{Action: "whatever"}}); err == nil {
		t.Fatal("不认识的选择应报错")
	}
}

func TestVariantNaming(t *testing.T) {
	n := withVariant("越狱.Prison Break.S04E22.1080p.BluRay.REMUX.DTS-HD.mkv", "A")
	if n != "越狱.Prison Break.S04E22.1080p.BluRay.REMUX.DTS-HD#A.mkv" {
		t.Fatalf("加字母：%s", n)
	}
	if withVariant("x.mkv", "") != "x.mkv" {
		t.Fatal("没字母原样返回")
	}
	p := parseFileName(n)
	q := parseFileName("越狱.Prison Break.S04E22.1080p.BluRay.REMUX.DTS-HD.mkv")
	if p.Season != 4 || p.Episode != 22 || p.Title != q.Title || p.Resolution != q.Resolution {
		t.Fatalf("#A 不能影响解析：%+v vs %+v", *p, *q)
	}
}

// 附属文件跟着视频走：字幕、集 NFO 认得出，别的视频的不算
func TestDupCompanions(t *testing.T) {
	files := []remoteFile{
		pbCantonese, pbEnglish,
		{Fid: "s1", Name: strings.TrimSuffix(pbCantonese.Name, ".mkv") + ".chs.ass"},
		{Fid: "s2", Name: strings.TrimSuffix(pbEnglish.Name, ".mkv") + ".chs.ass"},
		{Fid: "n1", Name: strings.TrimSuffix(pbCantonese.Name, ".mkv") + ".nfo"},
	}
	got := dupCompanions([]remoteFile{pbCantonese}, files)
	if len(got) != 2 || got[0].Fid != "s1" || got[1].Fid != "n1" {
		t.Fatalf("附属：%+v", got)
	}
}

// 撞名停下的待确认：「人工确认」开关关着也不许自动接手
func TestDupHoldIsSticky(t *testing.T) {
	ref := &awaitingRef{dup: true, fids: []string{"d"}}
	c := &orgCtx{cfg: &OrgConfig{ManualConfirm: false}, held: map[string]*awaitingRef{"d": ref}}
	if left := c.dropHeld([]dirEntry{{Fid: "d"}, {Fid: "x"}}); len(left) != 1 || left[0].Fid != "x" {
		t.Fatalf("撞名停下的条目应跳过：%+v", left)
	}
}

// 重新整理按记录里的字母沿用后缀：两份不会再撞名，A / B 也不会互换
func TestRedoKeepsVariants(t *testing.T) {
	prev := renameTpl
	renameTpl = nil
	t.Cleanup(func() { renameTpl = prev })
	media := &TmdbMedia{TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv"}
	files := []orgRecordFile{
		{Fid: "eng", Name: "越狱 - S04E22#A.mkv", Kind: "video", Variant: "A"},
		{Fid: "yue", Name: "越狱 - S04E22#B.mkv", Kind: "video", Variant: "B"},
		{Fid: "sub", Name: "越狱 - S04E22#B.chs.ass", Kind: "subtitle"},
	}
	plan, err := planRedoLayout(media, "剧集", files, "越狱/", nil)
	if err != nil {
		t.Fatalf("带字母的两份不该撞名：%v", err)
	}
	if len(plan.renames) != 0 {
		t.Fatalf("名字已是规范名，不该改：%v", plan.renames)
	}
	for _, g := range plan.groups {
		for _, f := range g {
			if f.Fid == "sub" && f.Name != "越狱 - S04E22#B.chs.ass" {
				t.Fatalf("字幕跟着 #B：%s", f.Name)
			}
		}
	}
}
