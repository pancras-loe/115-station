package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// 相关性：片名（含别名 / 原名）要被标题包含；电影再看年份；短片名无从判断时一律放行
func TestResRelevant(t *testing.T) {
	dune := resQuery{TmdbID: 693134, Type: "movie", Title: "沙丘2", OrigTitle: "Dune: Part Two", Year: "2024",
		names: []string{"沙丘：第二部", "Dune Part 2"}}
	threeBody := resQuery{TmdbID: 108545, Type: "tv", Title: "三体", OrigTitle: "3 Body Problem", Year: "2024",
		names: []string{"三體"}}
	up := resQuery{TmdbID: 14160, Type: "movie", Title: "Up", OrigTitle: "Up", Year: "2009"}
	cases := []struct {
		q     resQuery
		title string
		want  bool
	}{
		{dune, "Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR.H265-FLUX", true},
		{dune, "沙丘2 4K 杜比视界 中字", true},
		{dune, "Dune.1984.1080p.BluRay.x264", false},   // 同名旧版：片名对不上 Part Two
		{dune, "Dune.Part.Two.2021.Fake.1080p", false}, // 写了年份但差得远
		{dune, "Dune.Part.Two.2023.1080p", true},       // TMDB 年份与发布名常差一年
		{dune, "沙丘 合集 1-2", false},
		{threeBody, "三體 S01 2160p NF WEB-DL", true},   // 繁体译名
		{threeBody, "3.Body.Problem.S01.1080p", true}, // 原名
		{threeBody, "三体.2023.S01E01-E30.4K", true},    // 剧集不看年份
		{threeBody, "流浪地球2 4K", false},
		{up, "Upgrade.2018.1080p", true},           // 片名太短无从判断，交给人挑
		{resQuery{Title: "随便什么"}, "完全无关的标题", true}, // 跳过 TMDB：不比对
	}
	for _, c := range cases {
		if got := resRelevant(c.q, c.title); got != c.want {
			t.Errorf("resRelevant(%s, %q) = %v, want %v", c.q.Title, c.title, got, c.want)
		}
	}
}

func TestResSeasonOf(t *testing.T) {
	cases := map[string]string{
		"三体.S01.2160p.WEB-DL":             "S01",
		"The.Office.S01-S09.Complete":     "S01-S09",
		"Friends.S03E05.1080p":            "S03",
		"狂飙 第二季 4K":                       "S02",
		"甄嬛传 第1-3季":                       "S01-S03",
		"老友记 全十季 合集":                      "全集",
		"Dune.Part.Two.2024.2160p.WEB-DL": "",
	}
	for in, want := range cases {
		if got := resSeasonOf(in); got != want {
			t.Errorf("resSeasonOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResParseSizeAndTime(t *testing.T) {
	g := float64(1 << 30)
	if got := resParseSize("10.49G"); got != int64(10.49*g) {
		t.Errorf("10.49G → %d", got)
	}
	if got := resParseSize("1.5 GB"); got != int64(1.5*g) {
		t.Errorf("1.5 GB → %d", got)
	}
	if got := resParseSize("H265 5.1"); got != 0 {
		t.Errorf("没有单位不算大小: %d", got)
	}
	if got := resParseTime("0001-01-01T00:00:00Z"); got != 0 {
		t.Errorf("盘搜的零时间应当认作没有: %d", got)
	}
	if got := resParseTime("2026-02-01"); got == 0 {
		t.Errorf("日期应当认得出")
	}
	ago := resParseTime("3 小时前")
	if d := time.Now().Unix() - ago; d < 3*3600-5 || d > 3*3600+5 {
		t.Errorf("3 小时前 → %d（差 %d 秒）", ago, d)
	}
}

func TestResKindOf(t *testing.T) {
	cases := []struct{ link, hint, kind, action, pan string }{
		{"https://115cdn.com/s/abc?password=x1y2", "115", "share115", "transfer", ""},
		{"magnet:?xt=urn:btih:ABCDEF0123456789", "", "magnet", "offline", ""},
		{"ed2k://|file|a.mkv|1|HASH|/", "", "ed2k", "offline", ""},
		{"https://pan.baidu.com/s/1abc", "", "pan", "open", "baidu"},
		{"https://pan.xunlei.com/s/xyz", "xunlei", "pan", "open", "xunlei"},
	}
	for _, c := range cases {
		kind, action, pan := resKindOf(c.link, c.hint)
		if kind != c.kind || action != c.action || pan != c.pan {
			t.Errorf("resKindOf(%q) = %s/%s/%s", c.link, kind, action, pan)
		}
	}
}

// 不太灵：站内搜出来的是影片列表，要挑对得上的那部；一部都对不上就不挑
func TestMukakuPickVideo(t *testing.T) {
	q := resQuery{TmdbID: 1, Type: "movie", Title: "沙丘2", OrigTitle: "Dune: Part Two", Year: "2024"}
	videos := []mukakuVideo{
		{ID: 1, Title: "沙丘", Otitle: "Dune", Years: "2021"},
		{ID: 2, Title: "沙丘2", Otitle: "Dune: Part Two", Years: "2024"},
		{ID: 3, Title: "沙丘2 幕后", Years: "2024"},
	}
	if v := mukakuPickVideo(q, videos); v == nil || v.ID != 2 {
		t.Fatalf("应当挑片名、年份都对得上的那部: %+v", v)
	}
	if v := mukakuPickVideo(q, []mukakuVideo{{ID: 9, Title: "流浪地球"}}); v != nil {
		t.Fatalf("对不上时不应挑: %+v", v)
	}
	if v := mukakuPickVideo(resQuery{Title: "x"}, videos); v == nil || v.ID != 1 {
		t.Fatalf("跳过 TMDB 时取第一部: %+v", v)
	}
}

// 归一：已移除网盘过滤、标签、大小兜底、洗版排名、默认排序
func TestResNormalizeAndSort(t *testing.T) {
	newTestDB(t, "reshub.db")
	model.DB.Create(&model.ScrapeRule{Type: "wash_config", Config: `
电影:
  mode: replace
  media_type: movie
  priority_level:
    - resource_pix: 2160p
      resource_effect: DV
    - resource_pix: 2160p
    - resource_pix: 1080p
`})
	resetWashCache()
	q := resQuery{TmdbID: 693134, Type: "movie", Title: "沙丘2", OrigTitle: "Dune: Part Two", Year: "2024"}
	items := resNormalize(q, []ResourceItem{
		{Source: "pansou", Kind: "pan", Pan: "quark", Action: "open", Title: "Dune.Part.Two.2024.2160p", URL: "https://pan.quark.cn/s/x"},
		{Source: "pansou", Kind: "pan", Pan: "baidu", Action: "open", Title: "Dune.Part.Two.2024.2160p.DV [58.3G]", URL: "https://pan.baidu.com/s/1"},
		{Source: "gy", Kind: "magnet", Action: "offline", Title: "Dune.Part.Two.2024.1080p.WEB-DL", Size: "8.2G"},
		{Source: "gy", Kind: "magnet", Action: "offline", Title: "Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR", Size: "20G"},
		{Source: "pansou", Kind: "share115", Action: "transfer", Title: "流浪地球2 4K", URL: "https://115.com/s/zz"},
		{Source: "gy", Kind: "magnet", Action: "offline", Title: "Dune.Part.Two.2024.2160p.WEB-DL.HDR", Size: "18G"},
	})
	if len(items) != 5 {
		t.Fatalf("夸克应当被过滤: %d 条", len(items))
	}
	resSortDefault(items)
	var order []string
	for _, it := range items {
		order = append(order, fmt.Sprintf("%s|%d|%v", it.Title, it.Rank, it.Relevant))
	}
	want := []string{
		"Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR|0|true",
		"Dune.Part.Two.2024.2160p.WEB-DL.HDR|1|true",
		"Dune.Part.Two.2024.1080p.WEB-DL|2|true",
		"Dune.Part.Two.2024.2160p.DV [58.3G]|0|true", // 其他网盘要手动处理，沉在能直接转存的后面
		"流浪地球2 4K|1|false",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("排序不符:\n%s\n预期:\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	if items[3].Size != "58.3G" || items[3].SizeBytes == 0 {
		t.Errorf("标题里的大小应当兜底取出: %+v", items[3])
	}
	if items[0].Tags.Pix != "2160p" || items[0].Tags.Effect == "" {
		t.Errorf("画质标签: %+v", items[0].Tags)
	}
}

// 提交过的链接按台账标记（分享按 share_code、磁力按 btih，与提交时登记的指纹同一套）
func TestResMarkSubmitted(t *testing.T) {
	newTestDB(t, "ressubmitted.db")
	model.DB.Create(&model.DownloadLink{Kind: "share", URL: "https://115.com/s/abc", Hash: linkHashOf("https://115.com/s/abc")})
	model.DB.Create(&model.DownloadLink{Kind: "magnet", Hash: linkHashOf("magnet:?xt=urn:btih:ABCDEF0123456789ABCD")})
	items := []ResourceItem{
		{URL: "https://115cdn.com/s/abc?password=1234"},
		{URL: "magnet:?xt=urn:btih:abcdef0123456789abcd&dn=x"},
		{URL: "https://115.com/s/other"},
		{Ref: "/bt/1"},
	}
	resMarkSubmitted(model.DB, items)
	if items[0].SubmittedAt == 0 || items[1].SubmittedAt == 0 {
		t.Fatalf("提交过的应当标上: %+v", items)
	}
	if items[2].SubmittedAt != 0 || items[3].SubmittedAt != 0 {
		t.Fatalf("没提交过的不应标: %+v", items)
	}
}

// 盘搜走一遍完整的来源流程：请求、归一、相关性、缓存
func TestSearchResourcesPansou(t *testing.T) {
	newTestDB(t, "respansou.db")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("kw") != "三体" {
			t.Errorf("应当用 TMDB 标题搜: %s", r.URL)
		}
		fmt.Fprint(w, `{"code":0,"data":{"total":3,"merged_by_type":{
   "115":[{"url":"https://115.com/s/a1","password":"x1y2","note":"三体 S01 2160p 中字","datetime":"2026-02-01T00:00:00Z"}],
   "baidu":[{"url":"https://pan.baidu.com/s/b1","note":"流浪地球 合集"}],
   "quark":[{"url":"https://pan.quark.cn/s/q1","note":"三体"}]
  }}}`)
	}))
	defer server.Close()
	pansouCfgMu.Lock()
	oldCfg, oldAt := pansouCfgV, pansouCfgAt
	pansouCfgV, pansouCfgAt = &pansouCfg{BaseURL: server.URL}, time.Now()
	pansouCfgMu.Unlock()
	t.Cleanup(func() {
		pansouCfgMu.Lock()
		pansouCfgV, pansouCfgAt = oldCfg, oldAt
		pansouCfgMu.Unlock()
		resCacheMu.Lock()
		resCache = map[string]resCacheEntry{}
		resCacheMu.Unlock()
	})
	h := &Handler{DB: model.DB}
	q := resQuery{TmdbID: 108545, Type: "tv", Title: "三体", Year: "2024"}
	r, err := searchResources(h, "pansou", q, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 {
		t.Fatalf("夸克应当被过滤: %+v", r.Items)
	}
	first := r.Items[0]
	if first.Kind != "share115" || first.Action != "transfer" || first.Code != "x1y2" || !first.Relevant ||
		first.Tags.Season != "S01" || !first.Tags.Zh || first.Time != "2026-02-01" {
		t.Fatalf("115 分享归一不符: %+v", first)
	}
	if r.Items[1].Relevant {
		t.Fatalf("流浪地球不是三体: %+v", r.Items[1])
	}
	// 再搜一次走缓存，但提交状态要现查
	model.DB.Create(&model.DownloadLink{Kind: "share", Hash: linkHashOf("https://115.com/s/a1")})
	r2, err := searchResources(h, "pansou", q, false)
	if err != nil || calls != 1 {
		t.Fatalf("第二次应当走缓存: calls=%d err=%v", calls, err)
	}
	if r2.Items[0].SubmittedAt == 0 {
		t.Fatalf("缓存命中时提交状态也要现查")
	}
	if r.Items[0].SubmittedAt != 0 {
		t.Fatalf("缓存里存的是副本，不应被改写")
	}
	if _, err := searchResources(h, "pansou", q, true); err != nil || calls != 2 {
		t.Fatalf("refresh 应当绕过缓存: calls=%d", calls)
	}
}

// 提交前的守卫：认不出的链接、已移除网盘、RE0 未确认都在请求 115 之前拦下
func TestSubmitResourceGuards(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		req  resSubmitReq
		want string
	}{
		{resSubmitReq{URL: "随便一段文字"}, "认不出"},
		{resSubmitReq{URL: "https://pan.quark.cn/s/abc"}, "不支持该网盘"},
		{resSubmitReq{Source: "re0", Action: "unlock", Ref: "slug1"}, "积分"},
		{resSubmitReq{Source: "pansou", Action: "delete", URL: "https://115.com/s/a"}, "不支持的操作"},
	}
	for _, c := range cases {
		_, err := h.submitResource(c.req, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("submitResource(%+v) err=%v，预期含「%s」", c.req, err, c.want)
		}
	}
	r, err := h.submitResource(resSubmitReq{Source: "pansou", Action: "open", URL: "https://pan.baidu.com/s/1", Code: "ab12"}, "")
	if err != nil || r.OpenURL != "https://pan.baidu.com/s/1" || r.Code != "ab12" {
		t.Errorf("其他网盘应当原样交回: %+v %v", r, err)
	}
}
