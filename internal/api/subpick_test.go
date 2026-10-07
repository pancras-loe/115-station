package api

import (
	"reflect"
	"testing"
	"time"

	"115-station/internal/model"
)

func TestResCoverageOf(t *testing.T) {
	cases := []struct {
		title string
		want  resCoverage
	}{
		{"三体 S01 4K", resCoverage{SeasonLo: 1, SeasonHi: 1}},
		{"三体 S01-S03 合集", resCoverage{SeasonLo: 1, SeasonHi: 3}},
		{"三体 第二季 第1-12集", resCoverage{SeasonLo: 2, SeasonHi: 2, EpLo: 1, EpHi: 12}},
		{"三体.S01E05-E08.2160p", resCoverage{SeasonLo: 1, SeasonHi: 1, EpLo: 5, EpHi: 8}},
		{"三体 更新至12集 4K", resCoverage{EpLo: 1, EpHi: 12, Ongoing: true}},
		{"三体 全30集", resCoverage{EpLo: 1, EpHi: 30, All: true}},
		{"三体 全集 1080p", resCoverage{All: true}},
		{"三体 S01E07 1080p", resCoverage{SeasonLo: 1, SeasonHi: 1, EpLo: 7, EpHi: 7}},
		{"三体 2023 4K 杜比视界", resCoverage{}},
	}
	for _, c := range cases {
		if got := resCoverageOf(c.title); got != c.want {
			t.Errorf("%q = %+v, want %+v", c.title, got, c.want)
		}
	}
	c := resCoverageOf("三体 第二季 第1-12集")
	if !c.covers(epKey{2, 5}) || c.covers(epKey{1, 5}) || c.covers(epKey{2, 13}) {
		t.Error("covers 判断不对")
	}
	if (resCoverage{}).covers(epKey{1, 1}) {
		t.Error("估计不出的不该算覆盖")
	}
}

func pts(n int) *int { return &n }

func TestPlanSubCandidates(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	missing := epKeys("S02E05", "S02E06")
	share := func(title, url string, rank int) ResourceItem {
		return ResourceItem{Source: "pansou", Kind: "share115", Action: "transfer", Title: title, URL: url, Relevant: true, Rank: rank}
	}
	items := []ResourceItem{
		share("剧 第二季 全集", "https://115.com/s/aaa", -1),                                                                          // 覆盖 2 集
		share("剧 S02E05", "https://115.com/s/bbb", 0),                                                                           // 覆盖 1 集，排名好
		share("剧 S01 1080p", "https://115.com/s/ccc", 0),                                                                        // 写明第 1 季：缺的不在里面
		share("剧 2023 4K", "https://115.com/s/ddd", 0),                                                                          // 估计不出：留着，排后面
		share("剧 第二季 枪版", "https://115.com/s/eee", 0),                                                                           // 排除词
		{Source: "pansou", Kind: "share115", Action: "transfer", Title: "剧 S02", URL: "https://115.com/s/fff", Relevant: false}, // 不相关
		{Source: "tg", Kind: "magnet", Action: "offline", Title: "剧 2023", URL: "magnet:?xt=urn:btih:2222222222222222222222222222222222222222", Relevant: true}, // 离线估计不出
		{Source: "tg", Kind: "magnet", Action: "offline", Title: "剧 S02E06", URL: "magnet:?xt=urn:btih:3333333333333333333333333333333333333333", Relevant: true},
		share("剧 第二季 试过的", "https://115.com/s/ggg", 0),
		share("剧 第二季 连载过一天了", "https://115.com/s/hhh", 0),
		{Source: "pan", Kind: "pan", Action: "open", Title: "剧 第二季", URL: "https://pan.baidu.com/s/x", Relevant: true},
	}
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	ctx := subPickCtx{
		Sub: sub, Missing: missing, Now: now,
		Exclude: splitKeywords("枪版,CAM"),
		Tried: map[string]subTried{
			"ggg": {Status: subAttemptUseless, RetryAt: &future},
			"hhh": {Status: subAttemptUseless, RetryAt: &past},
		},
		Re0Left: -1,
	}
	cands, over := planSubCandidates(items, ctx)
	var got []string
	for _, c := range cands {
		got = append(got, c.Item.Title)
	}
	// 覆盖一样多时洗版排名好的在前（全集那条没命中规则）
	want := []string{"剧 第二季 连载过一天了", "剧 第二季 全集", "剧 S02E05", "剧 S02E06", "剧 2023 4K"}
	if !reflect.DeepEqual(got, want) || len(over) != 0 {
		t.Fatalf("候选 = %v, want %v（over %d）", got, want, len(over))
	}

	// 画质门槛：只要前 1 条规则
	sub.RankLimit = 1
	cands, _ = planSubCandidates(items, ctx)
	for _, c := range cands {
		if c.Item.Rank != 0 {
			t.Fatalf("门槛外的不该留下: %+v", c.Item)
		}
	}
	sub.RankLimit = 0

	// 包含词
	ctx.Include = splitKeywords("全集")
	if cands, _ = planSubCandidates(items, ctx); len(cands) != 1 {
		t.Fatalf("包含词过滤后应只剩 1 条: %d", len(cands))
	}
}

func TestPlanSubCandidatesRe0(t *testing.T) {
	now := time.Now()
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	missing := epKeys("S01E03")
	re0 := func(title, ref string, points *int, owned bool, rank int) ResourceItem {
		return ResourceItem{Source: "re0", Kind: "share115", Action: "unlock", Title: title, Ref: ref, Relevant: true, Points: points, Owned: owned, Rank: rank}
	}
	items := []ResourceItem{
		re0("剧 S01 免费", "free", pts(0), false, -1),
		re0("剧 S01 解锁过", "owned", pts(50), true, -1),
		re0("剧 S01 10分", "p10", pts(10), false, 0),
		re0("剧 S01 30分", "p30", pts(30), false, 0),
		re0("剧 2023 估不出", "p5u", pts(5), false, 0),   // 付费但估计不出覆盖：不花钱
		re0("剧 S01 没命中规则", "p5r", pts(5), false, -1), // 付费但画质没命中洗版规则
		re0("剧 S01 不知道多少分", "pn", nil, false, 0),
	}
	titles := func(cs []subCand) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Item.Title)
		}
		return out
	}

	// 没开自动解锁：免费和解锁过的照常；付费的进「超过上限」
	cands, over := planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 0, Re0Left: -1})
	if got := titles(cands); !reflect.DeepEqual(got, []string{"剧 S01 免费", "剧 S01 解锁过"}) {
		t.Fatalf("没开自动解锁 候选 = %v", got)
	}
	if got := titles(over); !reflect.DeepEqual(got, []string{"剧 S01 10分", "剧 S01 30分"}) {
		t.Fatalf("没开自动解锁 超限 = %v", got)
	}

	// 上限 20：10 分的可以，30 分的超限；免费的排在付费前面
	cands, over = planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 20, Re0Left: -1})
	if got := titles(cands); !reflect.DeepEqual(got, []string{"剧 S01 免费", "剧 S01 解锁过", "剧 S01 10分"}) || cands[2].Paid != 10 {
		t.Fatalf("上限 20 候选 = %v", got)
	}
	if got := titles(over); !reflect.DeepEqual(got, []string{"剧 S01 30分"}) {
		t.Fatalf("上限 20 超限 = %v", got)
	}

	// 预算只剩 5：10 分的也超
	_, over = planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 20, Re0Left: 5})
	if got := titles(over); !reflect.DeepEqual(got, []string{"剧 S01 10分", "剧 S01 30分"}) {
		t.Fatalf("预算 5 超限 = %v", got)
	}

	// 记过「需要积分」的不拦：调高上限后还能用
	tried := map[string]subTried{"re0:p30": {Status: subAttemptPaid}}
	cands, _ = planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Tried: tried, Re0Max: 50, Re0Left: -1})
	if got := titles(cands); len(got) != 4 {
		t.Fatalf("调高上限后 候选 = %v", got)
	}
}

func TestRe0StopUnlocking(t *testing.T) {
	for msg, want := range map[string]bool{
		"积分不足":          true,
		"RE0 请求未通过：401": true,
		"Unauthorized":  true,
		"资源已下架":         false,
		"网络超时":          false,
	} {
		if got := re0StopUnlocking(errString(msg)); got != want {
			t.Errorf("%q = %v", msg, got)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestSubWrapperName(t *testing.T) {
	tv := &model.Subscription{ID: 12, MediaType: "tv", Title: "三体", Year: "2023"}
	if got := subWrapperName(tv, epKeys("S01E02", "S01E03"), 3); got != "三体 (2023) S01 ·订阅12-3" {
		t.Errorf("同一季 = %q", got)
	}
	if got := subWrapperName(tv, epKeys("S01E02", "S02E01"), 1); got != "三体 (2023) ·订阅12-1" {
		t.Errorf("跨季 = %q", got)
	}
	mv := &model.Subscription{ID: 5, MediaType: "movie", Title: "沙丘：第二部", Year: "2024"}
	if got := subWrapperName(mv, nil, 1); got != "沙丘：第二部 (2024) ·订阅5-1" {
		t.Errorf("电影 = %q", got)
	}
	bad := &model.Subscription{ID: 1, MediaType: "movie", Title: `A/B: C?`}
	if got := subWrapperName(bad, nil, 1); got != "A B C ·订阅1-1" {
		t.Errorf("非法字符 = %q", got)
	}
}

func TestSubEpisodesText(t *testing.T) {
	tv := &model.Subscription{MediaType: "tv"}
	cases := map[string][]epKey{
		"S01E05":              epKeys("S01E05"),
		"S01E05–E07（3 集）":     epKeys("S01E07", "S01E05", "S01E06"),
		"S01E01、S01E03":       epKeys("S01E01", "S01E03"),
		"S01E01、S01E03 等 5 集": epKeys("S01E01", "S01E03", "S01E05", "S01E07", "S02E01"),
		"范围估计不出":              nil,
	}
	for want, ks := range cases {
		if got := subEpisodesText(tv, ks); got != want {
			t.Errorf("%v = %q, want %q", ks, got, want)
		}
	}
}
