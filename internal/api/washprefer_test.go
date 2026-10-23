package api

import (
	"strings"
	"testing"

	"115-station/internal/model"
)

func preferRules(p washPrefer) []washRule { return p.normalized().tiers() }

func TestWashPreferCompare(t *testing.T) {
	base := washPrefer{Avoid: []string{"hdtv", "cam"}}
	cases := []struct {
		name string
		p    washPrefer
		a, b string // a 应当更好
	}{
		// 老默认配置判反的几种（见 model.LegacyDefaultWashYAML）
		{"4K WEB-DL 胜 1080p 蓝光", base, "X.2024.2160p.WEB-DL.HDR10.H265-ADWeb.mkv", "X.2024.1080p.BluRay.x264-CMCT.mkv"},
		{"4K DV 胜 1080p", base, "X.2024.2160p.WEB-DL.DV.H265-HHWEB.mkv", "X.2024.1080p.WEB-DL.H264-ADWeb.mkv"},
		{"同分辨率片源先比", base, "X.2024.2160p.BluRay.HDR10.x265-CMCT.mkv", "X.2024.2160p.WEB-DL.DV.H265-HHWEB.mkv"},
		{"DV 无所谓时 DV.HDR10 不压过 HDR10+", base, "X.2024.2160p.WEB-DL.HDR10+.H265-A.mkv", "X.2024.2160p.WEB-DL.DV.HDR10.H265-B.mkv"},
		{"DV 优先要时排在片源前面", washPrefer{DV: "prefer"}, "X.2024.2160p.WEB-DL.DV.H265-HHWEB.mkv", "X.2024.2160p.BluRay.HDR10.x265-CMCT.mkv"},
		{"DV 不要", washPrefer{DV: "avoid"}, "X.2024.1080p.WEB-DL.H264-A.mkv", "X.2024.2160p.WEB-DL.DV.H265-B.mkv"},
		{"最高 1080p", washPrefer{MaxPix: "1080p"}, "X.2024.1080p.WEB-DL.H264-A.mkv", "X.2024.2160p.BluRay.REMUX.HEVC-B.mkv"},
		{"HDTV 不要", base, "X.2024.720p.WEB-DL.H264-A.mkv", "X.2024.1080p.HDTV.H264-B.mkv"},
		{"枪版不要", base, "X.2024.720p.WEB-DL.H264-A.mkv", "X.2024.1080p.HDTC.H264-B.mkv"},
		{"偏好发布组", washPrefer{Teams: []string{"wiki"}}, "X.2024.2160p.BluRay.HDR10.x265-WiKi.mkv", "X.2024.2160p.BluRay.HDR10.x265-CMCT.mkv"},
		{"偏好发布组（没扩展名的台账名）", washPrefer{Teams: []string{"ADWeb"}}, "X.2024.2160p.WEB-DL.H265-ADWeb", "X.2024.2160p.WEB-DL.H265-HHWEB"},
		{"关键词不要", washPrefer{AvoidWords: []string{"60fps"}}, "X.2024.1080p.WEB-DL.H264-A.mkv", "X.2024.2160p.60FPS.WEB-DL.H265-B.mkv"},
	}
	for _, c := range cases {
		rules := preferRules(c.p)
		if got, why := washCompare(c.a, c.b, rules); got <= 0 {
			t.Errorf("%s: 想要 a 更好，得到 %d（%s）", c.name, got, why)
		}
		if got, _ := washCompare(c.b, c.a, rules); got >= 0 {
			t.Errorf("%s: 反过来比应当 b 更差，得到 %d", c.name, got)
		}
	}
}

func TestWashPreferNotCamByExtension(t *testing.T) {
	p := washPrefer{Avoid: []string{"cam"}}.normalized()
	if why := p.avoidedBy("X.2024.1080p.WEB-DL.H264-A.ts"); len(why) > 0 {
		t.Fatalf(".ts 扩展名不是枪版: %v", why)
	}
	if why := p.avoidedBy("X.2024.1080p.TS.H264-A.mkv"); len(why) == 0 {
		t.Fatalf("名字里的 TS 应当算枪版")
	}
}

func TestWashPreferSize(t *testing.T) {
	rules := preferRules(washPrefer{Size: "small"})
	a := washFile{Name: "X.2024.2160p.WEB-DL.HDR10.H265-A.mkv", Size: 15 << 30}
	b := washFile{Name: "X.2024.2160p.WEB-DL.HDR10.H265-B.mkv", Size: 26 << 30}
	if c, why := washCompareFiles(a, b, rules); c <= 0 {
		t.Fatalf("留小的：15G 应当胜 26G，得到 %d（%s）", c, why)
	}
	b.Size = 15<<30 + 200<<20 // 相差不到 5%
	if c, _ := washCompareFiles(a, b, rules); c != 0 {
		t.Fatalf("相差不到 5%% 应当打平，得到 %d", c)
	}
	// 画质不一样时不看体积
	b = washFile{Name: "X.2024.1080p.WEB-DL.H264-B.mkv", Size: 2 << 30}
	if c, _ := washCompareFiles(a, b, rules); c <= 0 {
		t.Fatalf("分辨率先于体积")
	}
}

func TestWashPreferTiers(t *testing.T) {
	rules := preferRules(washPrefer{Avoid: []string{"hdtv"}})
	for name, want := range map[string]int{
		"X.2024.2160p.WEB-DL.H265-A.mkv": 0,
		"X.2024.1080p.BluRay.x264-A.mkv": 1,
		"X.2024.1080p.HDTV.H264-A.mkv":   -1,
	} {
		if got := washTier(name, rules); got != want {
			t.Errorf("%s: 档 %d，想要 %d", name, got, want)
		}
	}
	// 订阅「达标」= 第 0 档；写了体积偏好时永远不算洗完
	sp := subPriority{Rules: rules}
	if !sp.settled(washFile{Name: "X.2024.2160p.WEB-DL.H265-A.mkv"}) {
		t.Errorf("4K 应当算达标")
	}
	sp = subPriority{Rules: preferRules(washPrefer{Size: "small"})}
	if sp.settled(washFile{Name: "X.2024.2160p.WEB-DL.H265-A.mkv"}) {
		t.Errorf("留体积小的：总可能有更小的，不算洗完")
	}
	// 订阅按标题三态判前 1 档：1080p 不符，4K 符合
	sp = subPriority{Rules: rules, Limit: 1}
	if v, _ := sp.titleVerdict("X 2024 1080p BluRay", resTags{}); v != condFail {
		t.Errorf("前 1 档：1080p 应当不符，得到 %v", v)
	}
	if v, _ := sp.titleVerdict("X 2024 2160p WEB-DL", resTags{}); v != condOK {
		t.Errorf("前 1 档：4K 应当符合，得到 %v", v)
	}
}

func TestWashSimpleFormRoundTrip(t *testing.T) {
	forms := []washSimpleForm{
		{Mode: "replace", Target: "redundant", Movie: washPrefer{DV: "any", Avoid: []string{"hdtv", "cam"}}},
		{Mode: "coexist", Group: true, Target: "delete", Movie: washPrefer{MaxPix: "1080p", DV: "avoid", Size: "small", Teams: []string{"WiKi"}}},
		{Mode: "skip", Target: "redundant", Split: true,
			Movie: washPrefer{DV: "prefer", AvoidWords: []string{"60fps"}}, TV: washPrefer{Size: "large", Avoid: []string{"le720"}}},
	}
	for i, f := range forms {
		src := f.yaml()
		got, ok := simpleFormOf(src)
		if !ok {
			t.Fatalf("#%d 生成的 YAML 认不回来:\n%s", i, src)
		}
		if a, b := f.normalized().yaml(), got.yaml(); a != b {
			t.Fatalf("#%d 来回一次变了:\n%s\n----\n%s", i, a, b)
		}
	}
	if _, ok := simpleFormOf(model.DefaultWashYAML); !ok {
		t.Fatalf("默认策略应当能用简单设置显示")
	}
	if _, ok := simpleFormOf(model.LegacyDefaultWashYAML); ok {
		t.Fatalf("分档规则不该被认成简单设置")
	}
}

func TestWashLegacyDefaultDetect(t *testing.T) {
	// 老部署播种时注释可能不一样，只认策略本身
	stripped := strings.Join(func() []string {
		var out []string
		for _, l := range strings.Split(model.LegacyDefaultWashYAML, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				out = append(out, l)
			}
		}
		return out
	}(), "\n")
	if !washSameStrategies(stripped, model.LegacyDefaultWashYAML) {
		t.Fatalf("去掉注释的老默认应当认得出")
	}
	if washSameStrategies(model.DefaultWashYAML, model.LegacyDefaultWashYAML) {
		t.Fatalf("新老默认不该相同")
	}
}

func TestWashMaxSizeNormalize(t *testing.T) {
	st := normalizeWashStrategy(washStrategy{Mode: "max_size"})
	if st.Mode != "replace" || len(st.PriorityLevel) != 1 {
		t.Fatalf("max_size 没规则时应当补一条只比体积的: %+v", st)
	}
	a := washFile{Name: "X.2024.1080p.WEB-DL.H264-A.mkv", Size: 9 << 30}
	b := washFile{Name: "X.2024.1080p.WEB-DL.H264-B.mkv", Size: 4 << 30}
	if c, _ := washCompareFiles(a, b, st.PriorityLevel); c <= 0 {
		t.Fatalf("max_size 应当留大的")
	}
}

func TestWashTryVerdict(t *testing.T) {
	list, _ := parseWashYAML(model.DefaultWashYAML)
	st := normalizeWashStrategy(list[0].st)
	v := washTryVerdict(&st, "movie", washFile{Name: "X.2023.1080p.BluRay.x264-CMCT"}, washFile{Name: "X.2023.2160p.WEB-DL.HDR10.H265-ADWeb"})
	if v.Kind != "good" || !strings.Contains(v.Text, "分辨率") {
		t.Fatalf("4K 应当换下 1080p: %+v", v)
	}
	v = washTryVerdict(&st, "tv", washFile{Name: "S.S01E02.1080p.WEB-DL.H264-A"}, washFile{Name: "S.S01E02.720p.WEB-DL.H264-A"})
	if v.Kind != "keep" {
		t.Fatalf("720p 不该换下 1080p: %+v", v)
	}
	v = washTryVerdict(&st, "tv", washFile{Name: "S.1080p.WEB-DL"}, washFile{Name: "S.2160p.WEB-DL"})
	if v.Kind != "info" {
		t.Fatalf("剧集没集号应当比不了: %+v", v)
	}
}

func TestWashSemanticLintLegacy(t *testing.T) {
	raw, _ := parseWashYAML(model.LegacyDefaultWashYAML)
	var list []namedWashStrategy
	for _, n := range raw {
		list = append(list, namedWashStrategy{n.key, normalizeWashStrategy(n.st)})
	}
	items := washSemanticLint(raw, list)
	found := false
	for _, it := range items {
		if strings.Contains(it.Text, "更高的分辨率输给了更低的") {
			found = true
		}
	}
	if !found {
		t.Fatalf("老默认应当查出 4K 输给 1080p: %+v", items)
	}
	// 简单设置不出这类提醒
	raw, _ = parseWashYAML(model.DefaultWashYAML)
	list = list[:0]
	for _, n := range raw {
		list = append(list, namedWashStrategy{n.key, normalizeWashStrategy(n.st)})
	}
	if items := washSemanticLint(raw, list); len(items) != 0 {
		t.Fatalf("新默认不该有提醒: %+v", items)
	}
}

func TestWashLadderOrder(t *testing.T) {
	list, _ := parseWashYAML(model.DefaultWashYAML)
	st := normalizeWashStrategy(list[0].st)
	items := washLadder(&st, "movie")
	if !strings.Contains(items[0].Name, "2160p.BluRay.REMUX") {
		t.Fatalf("第一名应当是 4K 原盘: %s", items[0].Name)
	}
	last := items[len(items)-1]
	if last.Avoid == "" {
		t.Fatalf("最后一名应当是「不要」的: %+v", last)
	}
}
