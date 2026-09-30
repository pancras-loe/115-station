package api

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// ---- 纯函数 ----

func TestPickChineseName(t *testing.T) {
	cases := []struct {
		name string
		d    tmdbPersonDetail
		want string
	}{
		{"zh-CN 翻译优先", func() tmdbPersonDetail {
			var d tmdbPersonDetail
			d.Name = "Tom Hanks"
			d.AlsoKnownAs = []string{"湯姆·漢克斯"}
			d.Translations.Translations = append(d.Translations.Translations,
				struct {
					Country  string `json:"iso_3166_1"`
					Language string `json:"iso_639_1"`
					Data     struct {
						Name      string `json:"name"`
						Biography string `json:"biography"`
					} `json:"data"`
				}{Country: "CN", Language: "zh", Data: struct {
					Name      string `json:"name"`
					Biography string `json:"biography"`
				}{Name: "汤姆·汉克斯"}})
			return d
		}(), "汤姆·汉克斯"},
		{"原名就是中文", tmdbPersonDetail{Name: "劉德華"}, "刘德华"},
		{"别名里挑已是简体的", tmdbPersonDetail{Name: "Robin Wright", AlsoKnownAs: []string{"Robin Penn", "羅賓·懷特", "罗宾·怀特"}}, "罗宾·怀特"},
		{"只有繁体别名就转简体", tmdbPersonDetail{Name: "Robin Wright", AlsoKnownAs: []string{"羅賓·懷特"}}, "罗宾·怀特"},
		{"日文假名不算中文名", tmdbPersonDetail{Name: "Takuya Kimura", AlsoKnownAs: []string{"キムタク"}}, ""},
		{"韩文不算", tmdbPersonDetail{Name: "Song Kang-ho", AlsoKnownAs: []string{"송강호"}}, ""},
		{"什么都没有", tmdbPersonDetail{Name: "Nobody"}, ""},
	}
	for _, c := range cases {
		if got := pickChineseName(c.d); got != c.want {
			t.Errorf("%s: 得到 %q，预期 %q", c.name, got, c.want)
		}
	}
}

func TestMatchCredit(t *testing.T) {
	c := &titleCredits{
		entries: []creditEntry{
			{ID: 31, Names: []string{"Tom Hanks", "Tom Hanks"}, Group: "cast"},
			{ID: 24, Names: []string{"Robert Zemeckis"}, Group: "Directing"},
			{ID: 24, Names: []string{"Robert Zemeckis"}, Group: "Production"},
			{ID: 50, Names: []string{"John Smith"}, Group: "cast"},
			{ID: 51, Names: []string{"John Smith"}, Group: "cast"},
			{ID: 60, Names: []string{"Jane Doe"}, Group: "Writing"},
			{ID: 61, Names: []string{"Jane Doe"}, Group: "Sound"},
		},
		zh: map[int]string{31: "汤姆·汉克斯"},
	}
	cases := []struct {
		name, typ string
		want      int
	}{
		{"Tom Hanks", "Actor", 31},
		{"tom  hanks", "Actor", 31},         // 大小写、空白不敏感
		{"湯姆 · 漢克斯", "Actor", 31},           // Emby 里已是中文名（繁体、带空格）：靠缓存的中文名对上
		{"Robert Zemeckis", "Director", 24}, // 同一个人出现在两个部门也只算一个
		{"Robert Zemeckis", "Actor", 24},    // 组里没有，整张表恰好一个
		{"John Smith", "Actor", 0},          // 同组两个同名：宁可不补
		{"Jane Doe", "Writer", 60},          // 组内唯一，另一个部门的同名不算歧义
		{"Jane Doe", "Actor", 0},            // 组外两个同名
		{"Nobody", "Actor", 0},
	}
	for _, cs := range cases {
		if got := matchCredit(c, cs.name, cs.typ); got != cs.want {
			t.Errorf("%s/%s: 得到 %d，预期 %d", cs.name, cs.typ, got, cs.want)
		}
	}
}

func TestPlanPerson(t *testing.T) {
	cfg := defaultPersonFillCfg()
	full := model.PersonMeta{TmdbID: 31, ZhName: "汤姆·汉克斯", ProfilePath: "/a.jpg", ZhBio: "美国演员"}

	p := planPerson(cfg, "Tom Hanks", "", false, false, full, false)
	if !p.uploadImage || p.newName != "汤姆·汉克斯" || p.newBio != "美国演员" || !p.setTmdbID || p.state != "" {
		t.Fatalf("缺头像缺中文名，全都该补: %+v", p)
	}
	// 已有头像、已是中文名、已有中文简介：什么都不动
	p = planPerson(cfg, "汤姆·汉克斯", "他是演员", true, true, full, false)
	if p.uploadImage || p.newName != "" || p.newBio != "" || p.setTmdbID || p.state != "" {
		t.Fatalf("已补全的不该再动: %+v", p)
	}
	// 中文名被占：照样补头像，记账为 name_taken
	p = planPerson(cfg, "Tom Hanks", "", false, true, full, true)
	if !p.uploadImage || p.newName != "" || p.state != personStateNameTaken {
		t.Fatalf("中文名被占时的计划不对: %+v", p)
	}
	// TMDB 没头像：记 no_image，名字照改
	p = planPerson(cfg, "Tom Hanks", "", false, true, model.PersonMeta{TmdbID: 31, ZhName: "汤姆·汉克斯"}, false)
	if p.uploadImage || p.newName == "" || p.state != personStateNoImage {
		t.Fatalf("没头像的计划不对: %+v", p)
	}
	// 关掉中文名：英文名不算缺
	off := cfg
	off.ZhName, off.ZhBio = false, false
	p = planPerson(off, "Tom Hanks", "", true, true, model.PersonMeta{TmdbID: 31}, false)
	if p.newName != "" || p.state != "" {
		t.Fatalf("关掉中文名后不该改名也不该记账: %+v", p)
	}
}

func TestPersonCandidatesMaxCastAndTypes(t *testing.T) {
	cfg := defaultPersonFillCfg()
	cfg.MaxCast = 2
	cfg.Types = []string{"actor", "director"}
	r := &personRunner{cfg: cfg, skip: map[string]bool{"a2": true}, done: map[string]bool{}}
	title := embyTitleItem{Type: "Movie", People: []embyPersonRef{
		{ID: "a1", Name: "A One", Type: "Actor"},
		{ID: "a2", Name: "A Two", Type: "Actor"},                    // 记账暂缓
		{ID: "a3", Name: "A Three", Type: "Actor"},                  // 超出前 2 位
		{ID: "d1", Name: "Dir", Type: "Director"},                   // 导演不计入演员名额
		{ID: "w1", Name: "Writer", Type: "Writer"},                  // 没勾编剧
		{ID: "a4", Name: "张三", Type: "Actor", PrimaryImageTag: "x"}, // 已经补全
		{ID: "p1", Name: "Prod", Type: "Producer"},                  // 不在范围
	}}
	var got []string
	for _, p := range r.candidates(title) {
		got = append(got, p.ID)
	}
	if strings.Join(got, ",") != "a1,d1" {
		t.Fatalf("候选人物不对: %v", got)
	}
}

func TestPersonLane(t *testing.T) {
	if laneOfKind(jobKindPerson) != personLane {
		t.Fatal("演职人员补全必须走人物队列，不能挤进主队列拿 taskMu")
	}
	newTestDB(t, "person-lane.db")
	model.DB.Create(&model.TaskJob{Kind: jobKindPerson, Status: jobQueued, Title: "p"})
	model.DB.Create(&model.TaskJob{Kind: "organize", Status: jobQueued, Title: "o"})
	if j, ok := nextQueuedJob(model.DB, mainLane); !ok || j.Kind != "organize" {
		t.Fatalf("主队列取到了别的队列的任务: %+v", j)
	}
	if j, ok := nextQueuedJob(model.DB, personLane); !ok || j.Kind != jobKindPerson {
		t.Fatalf("人物队列没取到自己的任务: %+v", j)
	}
}

func TestNFOActorsUseCachedChineseName(t *testing.T) {
	newTestDB(t, "person-nfo.db")
	model.DB.Create(&model.PersonMeta{TmdbID: 31, ZhName: "汤姆·汉克斯", FetchedAt: time.Now()})
	actors := nfoActorsOf([]tmdbCastMember{
		{ID: 31, Name: "Tom Hanks", Character: "Forrest", ProfilePath: "/t.jpg"},
		{ID: 32, Name: "Robin Wright", Character: "Jenny"},
		{ID: 33, Name: ""},
	})
	if len(actors) != 2 {
		t.Fatalf("空名字的演员应跳过: %+v", actors)
	}
	if actors[0].Name != "汤姆·汉克斯" || actors[0].TmdbID != "31" || actors[0].Type != "Actor" ||
		actors[0].Thumb != "https://image.tmdb.org/t/p/h632/t.jpg" || *actors[0].Order != 0 {
		t.Fatalf("第一位演员不对: %+v", actors[0])
	}
	if actors[1].Name != "Robin Wright" || actors[1].Thumb != "" || *actors[1].Order != 1 {
		t.Fatalf("没缓存的用原名、没头像不写 thumb: %+v", actors[1])
	}
	b, _ := xml.Marshal(nfoMovie{Actors: actors, Writers: []string{"Eric Roth"}})
	s := string(b)
	for _, want := range []string{"<tmdbid>31</tmdbid>", "<type>Actor</type>", "<credits>Eric Roth</credits>", "<order>0</order>"} {
		if !strings.Contains(s, want) {
			t.Errorf("NFO 缺 %s: %s", want, s)
		}
	}
	names := nfoCrewNames([]tmdbCrewMember{
		{ID: 24, Name: "Robert Zemeckis", Job: "Director"},
		{ID: 24, Name: "Robert Zemeckis", Job: "Director"},
		{ID: 25, Name: "Eric Roth", Job: "Screenplay"},
		{ID: 26, Name: "Winston Groom", Job: "Novel"},
	}, crewIsWriter)
	if strings.Join(names, ",") != "Eric Roth" {
		t.Fatalf("编剧不该含原著作者: %v", names)
	}
}

// ---- 端到端：假 Emby + 假 TMDB ----

type fakePerson struct {
	Name     string
	Overview string
	Tmdb     string
	Image    string
	Locked   []string
}

type fakePeopleServer struct {
	mu      sync.Mutex
	persons map[string]*fakePerson
	uploads map[string]int
	updates map[string]int
	tmdbHit map[string]int
	// userGets 按用户读条目的次数，必须为 0
	userGets int
	// titleStarts 每次列片目请求的 StartIndex（看续扫从哪开始）
	titleStarts []int
}

// titles 假库里的片目（按加入时间）。有 p7 时多一部片：两位已有头像、已是中文名、只缺中文简介的演员
func (f *fakePeopleServer) titles() []map[string]any {
	out := []map[string]any{
		{"Id": "m1", "Name": "阿甘正传", "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "13"}, "People": []any{
			f.ref("p1", "Actor"), f.ref("p2", "Actor"), f.ref("p5", "Actor"), f.ref("p4", "Actor"), f.ref("p3", "Director"),
		}},
		{"Id": "s1", "Name": "某剧", "Type": "Series", "ProviderIds": map[string]string{"tmdb": "100"}, "People": []any{}},
	}
	if f.persons["p7"] != nil {
		out = append(out, map[string]any{"Id": "m2", "Name": "色戒", "Type": "Movie", "ProviderIds": map[string]string{"Tmdb": "14"},
			"People": []any{f.ref("p7", "Actor"), f.ref("p8", "Actor"), f.ref("p5", "Actor")}})
	}
	return out
}

func (f *fakePeopleServer) ref(id, typ string) map[string]any {
	p := f.persons[id]
	return map[string]any{"Id": id, "Name": p.Name, "Type": typ, "PrimaryImageTag": p.Image}
}

func (f *fakePeopleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if strings.HasPrefix(path, "/3/") || strings.HasPrefix(path, "/t/p/") {
		f.tmdbHit[path]++
	}
	switch {
	// ---- TMDB ----
	case path == "/3/movie/13/credits":
		js(map[string]any{
			"cast": []map[string]any{{"id": 31, "name": "Tom Hanks"}, {"id": 32, "name": "Robin Wright"}},
			"crew": []map[string]any{{"id": 24, "name": "Robert Zemeckis", "department": "Directing"}},
		})
	case path == "/3/tv/100/aggregate_credits":
		js(map[string]any{"cast": []any{}, "crew": []map[string]any{{"id": 77, "name": "Jane Doe", "department": "Writing"}}})
	case path == "/3/person/31":
		js(map[string]any{"id": 31, "name": "Tom Hanks", "profile_path": "/tom.jpg", "biography": "汤姆·汉克斯是美国演员。",
			"translations": map[string]any{"translations": []map[string]any{
				{"iso_3166_1": "CN", "iso_639_1": "zh", "data": map[string]any{"name": "汤姆·汉克斯"}}}}})
	case path == "/3/person/32":
		js(map[string]any{"id": 32, "name": "Robin Wright", "also_known_as": []string{"羅賓·懷特"}, "profile_path": "/robin.jpg"})
	case path == "/3/person/24":
		js(map[string]any{"id": 24, "name": "Robert Zemeckis"})
	case path == "/3/person/70":
		js(map[string]any{"id": 70, "name": "汤唯", "profile_path": "/tw.jpg", "biography": "汤唯，中国内地女演员。"})
	case path == "/3/person/80":
		js(map[string]any{"id": 80, "name": "巩俐", "profile_path": "/gl.jpg", "biography": "Gong Li is an actress."})
	case path == "/3/person/77":
		js(map[string]any{"id": 77, "name": "Jane Doe", "profile_path": "/jane.jpg"})
	case strings.HasPrefix(path, "/t/p/h632/"):
		_, _ = w.Write(append([]byte("\xff\xd8\xff\xe0"), make([]byte, 200)...)) // imgGet 拒收 100 字节以下的图

	// ---- Emby ----
	case path == "/Items" && r.URL.Query().Get("IncludeItemTypes") == "Movie,Series":
		titles := f.titles()
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
		f.titleStarts = append(f.titleStarts, start)
		end := start + limit
		if end > len(titles) {
			end = len(titles)
		}
		if start > end {
			start = end
		}
		js(map[string]any{"TotalRecordCount": len(titles), "Items": titles[start:end]})
	case path == "/Items" && r.URL.Query().Get("ParentId") == "s1":
		js(map[string]any{"TotalRecordCount": 2, "Items": []map[string]any{
			{"Id": "e1", "People": []any{f.ref("w1", "Writer")}},
			{"Id": "e2", "People": []any{f.ref("w1", "Writer"), f.ref("p1", "GuestStar")}},
		}})
	case strings.HasPrefix(path, "/Users/"):
		// 按用户读单个条目会让 Emby 当场去 TMDB 刷新人物，连不上时卡到超时：一律不许用
		f.userGets++
		w.WriteHeader(http.StatusGatewayTimeout)
	case path == "/Items" && r.URL.Query().Get("Ids") != "":
		fields := r.URL.Query().Get("Fields")
		if strings.Contains(fields, "ProviderIds") && !strings.Contains(fields, "Settings") {
			// 读完整人物条目却没带 Settings：回写时会把锁定字段清掉
			js(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
			return
		}
		var items []any
		for _, id := range strings.Split(r.URL.Query().Get("Ids"), ",") {
			p := f.persons[id]
			if p == nil {
				continue
			}
			it := map[string]any{"Id": id, "Name": p.Name, "Overview": p.Overview, "Type": "Person",
				"ProviderIds": map[string]any{}, "ImageTags": map[string]any{}, "SomeOtherField": "keep"}
			if p.Tmdb != "" {
				it["ProviderIds"] = map[string]any{"Tmdb": p.Tmdb}
			}
			if p.Image != "" {
				it["ImageTags"] = map[string]any{"Primary": p.Image}
			}
			items = append(items, it)
		}
		js(map[string]any{"Items": items, "TotalRecordCount": len(items)})
	case path == "/Persons":
		term := r.URL.Query().Get("SearchTerm")
		var items []map[string]any
		for id, p := range f.persons {
			if strings.Contains(p.Name, term) {
				items = append(items, map[string]any{"Id": id, "Name": p.Name})
			}
		}
		js(map[string]any{"Items": items})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/Images/Primary"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/Items/"), "/Images/Primary")
		body, _ := io.ReadAll(r.Body)
		if _, err := base64.StdEncoding.DecodeString(string(body)); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.uploads[id]++
		f.persons[id].Image = "img"
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/Items/"):
		id := strings.TrimPrefix(path, "/Items/")
		var it map[string]any
		_ = json.NewDecoder(r.Body).Decode(&it)
		if it["SomeOtherField"] != "keep" {
			w.WriteHeader(http.StatusBadRequest) // 条目更新必须把读到的完整 DTO 送回去
			return
		}
		p := f.persons[id]
		p.Name, _ = it["Name"].(string)
		p.Overview, _ = it["Overview"].(string)
		if ids, ok := it["ProviderIds"].(map[string]any); ok {
			p.Tmdb, _ = ids["Tmdb"].(string)
		}
		p.Locked = nil
		if l, ok := it["LockedFields"].([]any); ok {
			for _, v := range l {
				p.Locked = append(p.Locked, v.(string))
			}
		}
		f.updates[id]++
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPersonFillEndToEnd(t *testing.T) {
	newTestDB(t, "person-e2e.db")
	f := &fakePeopleServer{
		persons: map[string]*fakePerson{
			"p1": {Name: "Tom Hanks"},                                                       // 无 TMDB id、无头像、英文名 → 全补
			"p2": {Name: "Robin Wright", Tmdb: "32", Image: "old"},                          // 有头像；中文名已被 p6 占用
			"p3": {Name: "Robert Zemeckis"},                                                 // 导演：TMDB 无头像无中文名
			"p4": {Name: "Nobody"},                                                          // 演职员表里没有
			"p5": {Name: "刘德华", Image: "x", Overview: "香港演员、歌手。"},                           // 已补全，不该碰
			"p6": {Name: "罗宾·怀特", Image: "x"},                                               // 占着中文名的另一个条目
			"w1": {Name: "Jane Doe"},                                                        // 剧集编剧（挂在集上）：有头像无中文名
			"p7": {Name: "汤唯", Tmdb: "70", Image: "x", Overview: "Tang Wei is an actress."}, // 只缺中文简介，TMDB 有
			"p8": {Name: "巩俐", Tmdb: "80", Image: "x", Overview: ""},                        // 只缺中文简介，TMDB 也没有
		},
		uploads: map[string]int{}, updates: map[string]int{}, tmdbHit: map[string]int{},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()

	model.DB.Create(&model.TmdbConfig{ApiKey: "k", ApiUrl: srv.URL, ImageApiUrl: srv.URL, Language: "zh-CN"})
	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	emby, _ := json.Marshal(map[string]string{"server_url": srv.URL, "api_key": "ek"})
	if err := h.Config.SaveSetting("emby", string(emby)); err != nil {
		t.Fatal(err)
	}

	out, err := execPersonJob(h, &model.TaskJob{Kind: jobKindPerson, Priority: jobPriorityManual})
	if err != nil {
		t.Fatal(err)
	}
	res := out.Result.(personJobResult)
	if f.userGets != 0 {
		t.Fatalf("不许按用户读人物条目（会触发 Emby 按需刷新卡住）: %d 次", f.userGets)
	}

	p1 := f.persons["p1"]
	if f.uploads["p1"] != 1 || p1.Name != "汤姆·汉克斯" || p1.Tmdb != "31" || !strings.Contains(p1.Overview, "美国演员") {
		t.Fatalf("p1 应补头像、中文名、简介并写回 TMDB id: %+v uploads=%d", p1, f.uploads["p1"])
	}
	if strings.Join(p1.Locked, ",") != "Name,Overview" {
		t.Fatalf("改过的字段要锁定，否则 Emby 刷新又改回去: %v", p1.Locked)
	}
	if f.uploads["p2"] != 0 || f.persons["p2"].Name != "Robin Wright" {
		t.Fatalf("p2 有头像、中文名被占：不该上传也不该改名: %+v", f.persons["p2"])
	}
	if f.uploads["p3"] != 0 || f.updates["p3"] != 1 || f.persons["p3"].Tmdb != "24" {
		t.Fatalf("p3 没头像可补，但对上号了要写回 TMDB id: %+v", f.persons["p3"])
	}
	if f.uploads["p5"]+f.updates["p5"]+f.updates["p4"]+f.uploads["p4"] != 0 {
		t.Fatal("已补全的 / 对不上号的人物不该被改")
	}
	if f.uploads["w1"] != 1 {
		t.Fatal("剧集分集上的编剧也要补头像")
	}
	// p1 在剧里以客串出现：同一轮只处理一次
	if !strings.Contains(f.persons["p7"].Overview, "中国内地") || strings.Join(f.persons["p7"].Locked, ",") != "Overview" {
		t.Fatalf("只缺中文简介的人物也要补上并锁定: %+v", f.persons["p7"])
	}
	if res.Handled != 7 || res.Images != 2 || res.Renamed != 1 || res.Bios != 2 {
		t.Fatalf("统计不对: %+v", res)
	}
	if !out.Partial && res.States[personStateFailed] > 0 {
		t.Fatal("有出错的应记部分失败")
	}

	marks := map[string]string{}
	var rows []model.EmbyPersonMark
	model.DB.Find(&rows)
	for _, m := range rows {
		marks[m.PersonID] = m.State
		if !m.NextAt.After(time.Now().Add(29 * 24 * time.Hour)) {
			t.Errorf("%s 查无结果应暂缓一个月: %v", m.PersonID, m.NextAt)
		}
	}
	want := map[string]string{"p8": personStateNoBio, "p2": personStateNameTaken, "p3": personStateNoImage, "p4": personStateNoMatch, "w1": personStateNoZh}
	for id, st := range want {
		if marks[id] != st {
			t.Errorf("%s 记账应为 %s，实际 %q", id, st, marks[id])
		}
	}
	if _, ok := marks["p1"]; ok {
		t.Error("补全的人物不该留记账")
	}

	// 第二轮：记账里的人物暂缓，已补全的不再有需求，也不该再问 TMDB 人物详情
	before := f.tmdbHit["/3/person/31"] + f.tmdbHit["/3/person/32"]
	out, err = execPersonJob(h, &model.TaskJob{Kind: jobKindPerson, Priority: jobPriorityBackground})
	if err != nil {
		t.Fatal(err)
	}
	if r2 := out.Result.(personJobResult); r2.Handled != 0 || !out.Idle {
		t.Fatalf("第二轮应无事可做且按空转处理: %+v idle=%v", r2, out.Idle)
	}
	if f.tmdbHit["/3/person/31"]+f.tmdbHit["/3/person/32"] != before {
		t.Fatal("第二轮不该再问 TMDB")
	}

	// 缓存的中文名随后用于 NFO
	if zh := cachedZhNames([]int{31, 32}); zh[31] != "汤姆·汉克斯" || zh[32] != "罗宾·怀特" {
		t.Fatalf("人物缓存没落库: %v", zh)
	}
}

func TestPersonFillMaxPerRun(t *testing.T) {
	newTestDB(t, "person-cap.db")
	f := &fakePeopleServer{
		persons: map[string]*fakePerson{
			"p1": {Name: "Tom Hanks"}, "p2": {Name: "Robin Wright", Tmdb: "32"}, "p3": {Name: "Robert Zemeckis"},
			"p4": {Name: "Nobody"}, "p5": {Name: "刘德华", Image: "x", Overview: "香港演员、歌手。"}, "w1": {Name: "Jane Doe"},
		},
		uploads: map[string]int{}, updates: map[string]int{}, tmdbHit: map[string]int{},
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	model.DB.Create(&model.TmdbConfig{ApiKey: "k", ApiUrl: srv.URL, ImageApiUrl: srv.URL, Language: "zh-CN"})
	dir := t.TempDir()
	h := &Handler{DB: model.DB, Config: &config.Config{ConfigDir: dir, DataDir: dir}}
	emby, _ := json.Marshal(map[string]string{"server_url": srv.URL, "api_key": "ek"})
	_ = h.Config.SaveSetting("emby", string(emby))
	cfg := defaultPersonFillCfg()
	cfg.MaxPerRun = 2
	b, _ := json.Marshal(cfg)
	_ = h.Config.SaveSetting(personFillSetting, string(b))

	run := func() (jobOutcome, []int) {
		f.titleStarts = nil
		out, err := execPersonJob(h, &model.TaskJob{Kind: jobKindPerson})
		if err != nil {
			t.Fatal(err)
		}
		return out, f.titleStarts
	}

	// 第 1 轮：阿甘正传（第 1 部）里就用完 2 个名额 → 断点仍是第 1 部
	out, _ := run()
	if res := out.Result.(personJobResult); res.Handled != 2 || !strings.Contains(out.Message, "单次上限") {
		t.Fatalf("单次上限没生效: %+v %s", res, out.Message)
	}
	if c := h.loadPersonFillCursor(); c != 0 {
		t.Fatalf("在第 1 部停下，断点应为 0: %d", c)
	}
	// 第 2 轮：第 1 部剩下的 2 人处理完，到第 2 部（某剧）的编剧时名额用完 → 断点 1
	out, _ = run()
	if res := out.Result.(personJobResult); res.Handled != 2 {
		t.Fatalf("第 2 轮应接着处理第 1 部剩下的人物: %+v", res)
	}
	if c := h.loadPersonFillCursor(); c != 1 {
		t.Fatalf("在第 2 部停下，断点应为 1: %d", c)
	}
	// 第 3 轮：从第 2 部开始，处理完编剧后绕回第 1 部，看完一圈断点归零
	out, starts := run()
	if res := out.Result.(personJobResult); res.Handled != 1 || !strings.Contains(out.Message, "已看完全部") {
		t.Fatalf("第 3 轮应只剩编剧并看完一圈: %+v %s", res, out.Message)
	}
	// starts[0] 是开头问总数的那次（从 0 起、只取 1 条），真正的扫描从断点开始，再绕回 0
	if len(starts) != 3 || starts[1] != 1 || starts[2] != 0 {
		t.Fatalf("续扫顺序不对: %v", starts)
	}
	if c := h.loadPersonFillCursor(); c != 0 {
		t.Fatalf("看完一圈断点应归零: %d", c)
	}
}
