package api

import "testing"

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
