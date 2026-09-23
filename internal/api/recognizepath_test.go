package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// 路径上下文合并：文件名缺什么由近及远从目录上补，文件名已有的不覆盖
func TestMergePathContext(t *testing.T) {
	cases := []struct {
		name, file, path, parent string
		title, year, from        string
		season, episode          int
		tv                       bool
		tmdb                     int
	}{
		{name: "片名年份在目录上", file: "ep01.mkv", path: "西游记.1987",
			title: "西游记", year: "1987", from: "西游记.1987", season: 1, episode: 1, tv: true},
		{name: "季号在子目录上", file: "E05.mkv", path: "狂飙 (2023)/Season 2",
			title: "狂飙", year: "2023", from: "狂飙 (2023)", season: 2, episode: 5, tv: true},
		{name: "中文季目录", file: "01.mp4", path: "庆余年/第二季",
			title: "庆余年", from: "庆余年", season: 2, episode: 1, tv: true},
		{name: "文件名有片名：只补季号", file: "Show.E03.mkv", path: "Show/Season 3",
			title: "Show", season: 3, episode: 3, tv: true},
		{name: "文件名明写的季号不被目录覆盖", file: "Show.S01E03.mkv", path: "Show/Season 3",
			title: "Show", season: 1, episode: 3, tv: true},
		{name: "分类目录不当片名", file: "01.mkv", path: "Movies/合集", parent: "奥本海默 2023",
			title: "奥本海默", year: "2023", from: "奥本海默 2023", season: 1, episode: 1, tv: true},
		{name: "片名对不上的目录年份不借", file: "Dune.mkv", path: "欧美电影 2023",
			title: "Dune"},
		{name: "片名对得上的目录年份借过来", file: "Dune.mkv", path: "Dune (2021)",
			title: "Dune", year: "2021"},
		{name: "目录上的 id 标签", file: "Movie.mkv", path: "Movie (1999) [tmdbid=603]",
			title: "Movie", year: "1999", tmdb: 603},
		{name: "全N集目录判为剧集", file: "01.mp4", path: "狂飙.全39集.国语中字",
			title: "狂飙", from: "狂飙.全39集.国语中字", season: 1, episode: 1, tv: true},
	}
	for _, c := range cases {
		dirs := pathDirs(c.path, c.parent)
		got, from := mergePathContext(parseFileName(c.file), parseDirs(dirs, nil), dirs)
		if got.Title != c.title || got.Year != c.year || from != c.from || got.Season != c.season ||
			got.Episode != c.episode || got.IsTV != c.tv || got.TmdbID != c.tmdb {
			t.Errorf("%s：%s/%s\n  得到 片名=%q 年份=%q 出处=%q S%dE%d 剧集=%v 标签=%d\n  期望 片名=%q 年份=%q 出处=%q S%dE%d 剧集=%v 标签=%d",
				c.name, c.path, c.file, got.Title, got.Year, from, got.Season, got.Episode, got.IsTV, got.TmdbID,
				c.title, c.year, c.from, c.season, c.episode, c.tv, c.tmdb)
		}
	}
}

// 同一部剧的 Season 1/、Season 2/ 里文件都叫 E01.mkv：季号要从各自的子目录上取
func TestParseVideoInDirSeasonFolders(t *testing.T) {
	main := &ParsedName{Title: "Show", Season: 1, SeasonGuessed: true, IsTV: true}
	for path, want := range map[string]int{"Show/Season 1": 1, "Show/Season 2": 2, "Show/第三季": 3, "Show": 1} {
		p := parseVideoInDir(remoteFile{Name: "E01.mkv", Path: path}, nil, main)
		if p.Season != want || p.Episode != 1 {
			t.Errorf("%s/E01.mkv → S%dE%d，期望 S%dE01", path, p.Season, p.Episode, want)
		}
	}
	// 替换规则也作用在每一集上：规则注入的 s= / eo= 标签决定季号与集号
	rules := []ReplaceRule{{From: "某番", To: "某番 {[s=2;eo=-12]}"}}
	p := parseVideoInDir(remoteFile{Name: "某番 - 14 [1080p].mkv", Path: "某番"}, rules, nil)
	if p.Season != 2 || p.Episode != 2 {
		t.Errorf("集偏移规则没生效：S%dE%d，期望 S02E02", p.Season, p.Episode)
	}
}

func TestTakeTags(t *testing.T) {
	cases := []struct {
		in, rest string
		want     nameTag
	}{
		{"庆余年 {[tmdbid=69851;type=tv;s=2]}", "庆余年", nameTag{TmdbID: 69851, Kind: "tv", Season: 2}},
		{"某番 {[s=2]} {[eo=-12]}", "某番", nameTag{Season: 2, EpOffset: -12}},
		{"某番 {[eo=+3]}", "某番", nameTag{EpOffset: 3}},
		{"Movie [tmdbid-603]", "Movie", nameTag{TmdbID: 603}},
		{"奇怪的 {[abc]} 名字", "奇怪的 {[abc]} 名字", nameTag{}}, // 不认识的花括号原样保留
	}
	for _, c := range cases {
		got, rest := takeTags(c.in)
		if got != c.want || rest != c.rest {
			t.Errorf("takeTags(%q) = %+v %q，期望 %+v %q", c.in, got, rest, c.want, c.rest)
		}
	}
}

// 识别记忆：人工指定过的结论压过搜索；按片名 + 年份区分同名的不同作品
func TestRecognizeMemory(t *testing.T) {
	newTestDB(t, "recognize-memory.db")
	raw, err := os.ReadFile("testdata/recognize_match.json")
	if err != nil {
		t.Fatal(err)
	}
	var c matchCorpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	tc := fakeTmdb(t, &c)

	// 搜索会把 Dune.2021 认成 438631；人工改指定成 841 之后，下次同名同年按记忆走
	rememberRecognition(recogKey(parseFileName("Dune.2021.1080p.mkv")), "Dune.2021.1080p.mkv",
		&TmdbMedia{TmdbID: 841, MediaType: "movie", Title: "沙丘"})
	media, err := tc.recognize(parseFileName("Dune.2021.2160p.REMUX.mkv"))
	if err != nil || media == nil || media.TmdbID != 841 {
		t.Fatalf("没有按记忆采用人工结论: %+v %v", media, err)
	}
	// 年份不同的同名片不受影响
	media, _ = tc.recognize(parseFileName("Solaris.2002.mkv"))
	if media == nil || media.TmdbID != 2103 {
		t.Fatalf("记忆串到别的片子上: %+v", media)
	}
	rememberRecognition("solaris|1972", "", &TmdbMedia{TmdbID: 593, MediaType: "movie", Title: "飞向太空"})
	if media, _ = tc.recognize(parseFileName("Solaris.2002.mkv")); media == nil || media.TmdbID != 2103 {
		t.Fatalf("1972 的记忆不该作用在 2002 的文件上: %+v", media)
	}
	// 文件名没年份：同名记忆有两条（2021 与 1972）时分不出来，不用记忆
	rememberRecognition("solaris|2002", "", &TmdbMedia{TmdbID: 2103, MediaType: "movie", Title: "索拉里斯星"})
	if mem := recallRecognition(parseFileName("Solaris.mkv")); mem != nil {
		t.Fatalf("同名多条记忆不该随便挑一条: %+v", mem)
	}
	// 再改一次指定会覆盖同一个键
	rememberRecognition(recogKey(parseFileName("Dune.2021.mkv")), "",
		&TmdbMedia{TmdbID: 438631, MediaType: "movie", Title: "沙丘"})
	var n int64
	model.DB.Model(&model.RecognizeMemory{}).Where("title_key = ?", "dune").Count(&n)
	if mem := recallRecognition(parseFileName("Dune.2021.mkv")); n != 1 || mem == nil || mem.TmdbID != 438631 {
		t.Fatalf("改指定应覆盖原记忆: 条数=%d %+v", n, mem)
	}
}

// 识别记忆的管理接口：列表带上原名样例，删一条、清空都生效
func TestRecognizeMemoryHandlers(t *testing.T) {
	newTestDB(t, "recognize-memory-api.db")
	h := &Handler{DB: model.DB}
	rememberRecognition("dune|2021", "Dune.2021.1080p.mkv", &TmdbMedia{TmdbID: 438631, MediaType: "movie", Title: "沙丘"})
	rememberRecognition("庆余年|", "庆余年第二季/", &TmdbMedia{TmdbID: 69851, MediaType: "tv", Title: "庆余年"})

	list := func() []model.RecognizeMemory {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		h.ListRecognizeMemory(c)
		var out struct {
			Data []model.RecognizeMemory `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	rows := list()
	if len(rows) != 2 {
		t.Fatalf("应有 2 条记忆，得到 %d", len(rows))
	}
	var dune model.RecognizeMemory
	for _, r := range rows {
		if r.TitleKey == "dune" {
			dune = r
		}
	}
	if dune.Sample != "Dune.2021.1080p.mkv" || dune.TmdbID != 438631 {
		t.Fatalf("列表里缺原名或条目: %+v", dune)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(dune.ID))}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/", nil)
	h.DeleteRecognizeMemory(c)
	if rows = list(); len(rows) != 1 || rows[0].TitleKey != "庆余年" {
		t.Fatalf("删除后应只剩庆余年: %+v", rows)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	h.ClearRecognizeMemory(c)
	if rows = list(); len(rows) != 0 {
		t.Fatalf("清空后应为空: %+v", rows)
	}
}
