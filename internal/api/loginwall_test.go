package api

import "testing"

func TestPickLoginWallpapers(t *testing.T) {
	got := pickLoginWallpapers([]tmdbTrendingItem{
		{MediaType: "movie", Title: "沙丘2", BackdropPath: "/a.jpg", ReleaseDate: "2024-02-27"},
		{MediaType: "tv", Name: "三体", BackdropPath: "/b.jpg", FirstAirDate: "2023-01-15"},
		{MediaType: "person", Name: "某演员", BackdropPath: "/c.jpg"},
		{MediaType: "movie", Title: "没剧照"},
		{MediaType: "movie", Title: "坏路径", BackdropPath: "/../x.jpg"},
	})
	if len(got) != 2 {
		t.Fatalf("应只留两条，实际 %+v", got)
	}
	if got[0].Title != "沙丘2" || got[0].Year != "2024" || got[1].Title != "三体" || got[1].Year != "2023" {
		t.Fatalf("片名 / 年份不对: %+v", got)
	}
}
