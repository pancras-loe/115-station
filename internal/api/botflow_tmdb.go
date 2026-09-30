package api

// 机器人选片用的 TMDB 搜索（botflow.go）

import "encoding/json"

// wecomTmdbMulti TMDB 多结果搜索（纯数字按 ID 直查详情）
func (h *Handler) wecomTmdbMulti(q string) ([]wecomTmdbHit, error) {
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	type hit struct {
		ID     int     `json:"id"`
		Type   string  `json:"media_type"`
		Title  string  `json:"title"`
		Name   string  `json:"name"`
		Date   string  `json:"release_date"`
		First  string  `json:"first_air_date"`
		Vote   float64 `json:"vote_average"`
		Poster string  `json:"poster_path"`
	}
	toHit := func(id int, kind, title, name, date, first string, vote float64, poster string) wecomTmdbHit {
		t, d := title, date
		if t == "" {
			t = name
		}
		if d == "" {
			d = first
		}
		year := ""
		if len(d) >= 4 {
			year = d[:4]
		}
		return wecomTmdbHit{ID: id, Type: kind, Title: t, Year: year, Vote: vote, Poster: poster}
	}
	if regexpPureDigits.MatchString(q) {
		for _, kind := range []string{"movie", "tv"} {
			body, err := tc.get("/"+kind+"/"+q, nil)
			if err != nil {
				continue
			}
			var d hit
			if json.Unmarshal(body, &d) != nil || d.ID == 0 {
				continue
			}
			return []wecomTmdbHit{toHit(d.ID, kind, d.Title, d.Name, d.Date, d.First, d.Vote, d.Poster)}, nil
		}
		return nil, nil
	}
	// multi 搜索按热度排序，热门片名常被电影刷屏；
	// 再单独拉一次剧集搜索，按「2 部电影 + 1 部剧集」穿插，保证剧集有露出
	body, err := tc.get("/search/multi", map[string]string{"query": q, "include_adult": "false"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Results []hit `json:"results"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, nil
	}
	conv := func(it hit) wecomTmdbHit {
		return toHit(it.ID, it.Type, it.Title, it.Name, it.Date, it.First, it.Vote, it.Poster)
	}
	seen := map[int]bool{}
	var movies, tvs []wecomTmdbHit
	for _, it := range r.Results {
		if it.Type != "movie" && it.Type != "tv" {
			continue // multi-search 会混入 person
		}
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		if it.Type == "tv" {
			tvs = append(tvs, conv(it))
		} else {
			movies = append(movies, conv(it))
		}
	}
	// 剧集条目不足时单独补一次 /search/tv
	tvInList := 0
	for _, m := range tvs {
		if m.ID != 0 {
			tvInList++
		}
	}
	if tvInList < 2 {
		if tvb, err := tc.get("/search/tv", map[string]string{"query": q, "include_adult": "false"}); err == nil {
			var tr struct {
				Results []hit `json:"results"`
			}
			if json.Unmarshal(tvb, &tr) == nil {
				for _, it := range tr.Results {
					if seen[it.ID] {
						continue
					}
					seen[it.ID] = true
					tvs = append(tvs, conv(it))
					if len(tvs) >= 4 {
						break
					}
				}
			}
		}
	}
	// 穿插：每 2 部电影后插 1 部剧集
	var out []wecomTmdbHit
	mi, ti := 0, 0
	for mi < len(movies) || ti < len(tvs) {
		for k := 0; k < 2 && mi < len(movies); k++ {
			out = append(out, movies[mi])
			mi++
		}
		if ti < len(tvs) {
			out = append(out, tvs[ti])
			ti++
		}
		if len(out) >= 8 {
			break
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out, nil
}
