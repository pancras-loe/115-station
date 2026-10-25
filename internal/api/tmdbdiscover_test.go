package api

import (
	"slices"
	"testing"
)

func TestParseDiscoverPage(t *testing.T) {
	// trending/all：混着人物、纪录片、没海报的
	body := []byte(`{"total_pages": 3, "results": [
		{"id": 1, "media_type": "movie", "title": "沙丘", "release_date": "2021-09-15", "poster_path": "/a.jpg"},
		{"id": 2, "media_type": "person", "name": "某演员"},
		{"id": 3, "media_type": "tv", "name": "三体", "first_air_date": "2023-01-15", "poster_path": "/b.jpg"},
		{"id": 4, "media_type": "movie", "title": "纪录片", "poster_path": "/c.jpg", "genre_ids": [99]},
		{"id": 5, "media_type": "tv", "name": "没海报"}
	]}`)
	items, total, err := parseDiscoverPage(body, discoverList{key: "trending_day"})
	if err != nil || total != 3 {
		t.Fatalf("err=%v total=%d", err, total)
	}
	if len(items) != 2 || items[0].ID != 1 || items[0].MediaType != "movie" || items[0].Year != "2021" ||
		items[1].ID != 3 || items[1].MediaType != "tv" || items[1].Title != "三体" {
		t.Fatalf("意外结果: %+v", items)
	}

	// 单类型榜单：结果不带 media_type，按榜单类型认
	body = []byte(`{"total_pages": 1, "results": [{"id": 9, "name": "正在播出", "poster_path": "/d.jpg"}]}`)
	items, _, _ = parseDiscoverPage(body, discoverList{key: "tv_air", kind: "tv"})
	if len(items) != 1 || items[0].MediaType != "tv" || items[0].Title != "正在播出" {
		t.Fatalf("意外结果: %+v", items)
	}
}

func TestDiscoverFilterList(t *testing.T) {
	l, err := discoverFilterList("tv", "KR", "18")
	if err != nil {
		t.Fatal(err)
	}
	if l.path != "/discover/tv" || l.kind != "tv" || l.params["with_origin_country"] != "KR" || l.params["with_genres"] != "18" {
		t.Fatalf("参数不对: %+v", l)
	}
	if l.key != "filter:tv:KR:18" {
		t.Fatalf("缓存键不对: %s", l.key)
	}
	// 只选类型：不带地区与题材
	l, _ = discoverFilterList("movie", "", "")
	if _, ok := l.params["with_origin_country"]; ok {
		t.Fatal("没选地区不该带 with_origin_country")
	}
	// 剧集的题材表里没有电影的「爱情」，清单外的值一律拒收
	for _, c := range [][3]string{{"tv", "", "10749"}, {"movie", "XX", ""}, {"person", "", ""}, {"movie", "CN&x=1", ""}} {
		if _, err := discoverFilterList(c[0], c[1], c[2]); err == nil {
			t.Fatalf("%v 应该被拒收", c)
		}
	}
}

func TestSortDiscoverPool(t *testing.T) {
	pool := func() []manualCand {
		return []manualCand{
			{ID: 1, Vote: 7.0, voteCount: 500, date: "2024-05-01"},
			{ID: 2, Vote: 9.5, voteCount: 3, date: "2025-01-01"}, // 人数太少：按评分排时垫底
			{ID: 3, Vote: 8.2, voteCount: 900, date: ""},         // 没日期：按时间排时垫底
			{ID: 4, Vote: 8.2, voteCount: 100, date: "2023-12-31"},
		}
	}
	ids := func(cs []manualCand) []int {
		out := make([]int, len(cs))
		for i, c := range cs {
			out[i] = c.ID
		}
		return out
	}
	p := pool()
	sortDiscoverPool(p, "vote")
	if got := ids(p); !slices.Equal(got, []int{3, 4, 1, 2}) { // 3、4 同分保持原先后
		t.Fatalf("按评分: %v", got)
	}
	p = pool()
	sortDiscoverPool(p, "date")
	if got := ids(p); !slices.Equal(got, []int{2, 1, 4, 3}) {
		t.Fatalf("按时间: %v", got)
	}
	p = pool()
	sortDiscoverPool(p, "")
	if got := ids(p); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("原顺序: %v", got)
	}
	if discoverSortOK("popularity.desc") {
		t.Fatal("不认得的排序应拒收")
	}
}
