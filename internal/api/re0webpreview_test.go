package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// 网页接口的返回：外层站点通用包装，data 里同 OpenAPI file-list；格式对不上报错，不当成「没有文件 = 失效」
func TestParseRe0WebPreview(t *testing.T) {
	ok := `{"success":true,"data":{"share_title":"剧 S01","files":[{"name":"S01E04.mkv","path":"剧/S01","size":1073741824}]}}`
	p, err := parseRe0WebPreview(200, ok)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Name != "S01E04.mkv" || p.Entries[0].Dir != "剧/S01" || p.Title != "剧 S01" {
		t.Fatalf("正常返回: %+v %v", p, err)
	}
	// 没有外层包装也认
	if p, err := parseRe0WebPreview(200, `{"files":[{"name":"a.mkv"}]}`); err != nil || len(p.Entries) != 1 {
		t.Fatalf("无包装: %+v %v", p, err)
	}
	// 站方检测失效
	if p, err := parseRe0WebPreview(200, `{"success":true,"data":{"result_type":"validation","files":[]}}`); err != nil || p.Invalid == "" {
		t.Fatalf("失效: %+v %v", p, err)
	}
	for name, c := range map[string]struct {
		status int
		text   string
		want   string
	}{
		"401":      {401, `{"code":401,"message":"请先登录"}`, "请先登录"},
		"success假": {200, `{"success":false,"code":"RESOURCE_NOT_FOUND"}`, "RESOURCE_NOT_FOUND"},
		"非JSON":    {200, `<html>`, "不是 JSON"},
		"格式变了":     {200, `{"success":true,"data":{"items":[]}}`, "格式认不出"},
	} {
		if _, err := parseRe0WebPreview(c.status, c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// 网页通道没读到预览（包着 errRe0NoPreview）：要花积分的直接跳过，不退回按标题判；读到了就和 OpenAPI 一样按文件判
func TestPlanSubCandidatesRe0WebPreview(t *testing.T) {
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	missing := epKeys("S01E04")
	items := []ResourceItem{
		{Source: "re0", Kind: "share115", Action: "unlock", Title: "剧 [更新至04集] S01", Ref: "fail", Relevant: true, Points: pts(10), Rank: 0},
		{Source: "re0", Kind: "share115", Action: "unlock", Title: "剧 S01", Ref: "ok", Relevant: true, Points: pts(10), Rank: 0},
	}
	preview := func(it ResourceItem) ([]epKey, string, error) {
		if it.Ref == "fail" {
			return nil, "", fmt.Errorf("%w（%v）", errRe0NoPreview, errors.New("登录态失效"))
		}
		return epKeys("S01E04"), "", nil
	}
	skipped := map[string]int{}
	cands, over := planSubCandidates(items, subPickCtx{Prio: prioAny, Sub: sub, Missing: missing, Now: time.Now(), Re0Max: 20, Re0Left: -1,
		Re0Preview: preview, Skipped: skipped})
	if len(cands) != 1 || cands[0].Item.Ref != "ok" || len(cands[0].Exact) != 1 || len(over) != 0 {
		t.Fatalf("候选 = %+v over=%d", cands, len(over))
	}
	if skipped["要花积分、没读到 RE0 文件预览"] != 1 {
		t.Fatalf("跳过原因 = %v", skipped)
	}
}

// 这一轮预览额度用完（errSubPreviewLater）：要花积分的同样跳过，不退回按标题判（标题写「全集」也不行）
func TestPlanSubCandidatesPreviewLater(t *testing.T) {
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	items := []ResourceItem{
		{Source: "re0", Kind: "share115", Action: "unlock", Title: "剧 全集 4K", Ref: "x", Relevant: true, Points: pts(8), Rank: 0},
	}
	preview := func(ResourceItem) ([]epKey, string, error) { return nil, "", errSubPreviewLater }
	skipped := map[string]int{}
	cands, over := planSubCandidates(items, subPickCtx{Prio: prioAny, Sub: sub, Missing: epKeys("S01E04"), Now: time.Now(), Re0Max: 20, Re0Left: -1,
		Re0Preview: preview, Skipped: skipped})
	if len(cands) != 0 || len(over) != 0 {
		t.Fatalf("额度用完不该付费：候选 %d over %d", len(cands), len(over))
	}
	if skipped["要花积分、这一轮文件预览次数用完，下一轮再看"] != 1 {
		t.Fatalf("跳过原因 = %v", skipped)
	}
}

// 追新集（缺的全是刚播出的）：要花积分的标题得写到这一集，老资源不预览、不解锁；补老集照常预览
func TestPlanSubCandidatesFreshNeedsTitle(t *testing.T) {
	now := time.Date(2026, 10, 24, 5, 0, 0, 0, time.Local)
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	paid := func(title, ref string) ResourceItem {
		return ResourceItem{Source: "re0", Kind: "share115", Action: "unlock", Title: title, Ref: ref, Relevant: true, Points: pts(8)}
	}
	items := []ResourceItem{
		paid("剧 1集-184集合集 2K", "old"),
		paid("剧 全集 4K", "all"),
		paid("剧 4K WEB-DL", "bare"),
		paid("剧 更新至197集 4K", "new"),
	}
	previewed := map[string]bool{}
	preview := func(it ResourceItem) ([]epKey, string, error) {
		previewed[it.Ref] = true
		return epKeys("S01E197"), "", nil
	}
	ctx := func(air time.Time) subPickCtx {
		return subPickCtx{Prio: prioAny, Sub: sub, Missing: epKeys("S01E197"), Now: now, Re0Max: 20, Re0Left: -1,
			Re0Preview: preview, MissingAir: map[epKey]time.Time{{S: 1, E: 197}: air}, Skipped: map[string]int{}}
	}
	cands, _ := planSubCandidates(items, ctx(time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)))
	if len(cands) != 1 || cands[0].Item.Ref != "new" || len(previewed) != 1 {
		t.Fatalf("追新集：候选 %+v 预览了 %v", cands, previewed)
	}
	// 同一集播出很久了（补老集）：标题没写范围的照常预览
	previewed = map[string]bool{}
	cands, _ = planSubCandidates(items, ctx(time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)))
	if !previewed["bare"] || !previewed["all"] || len(cands) != 3 {
		t.Fatalf("补老集：候选 %d 预览了 %v", len(cands), previewed)
	}
}
