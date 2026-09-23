package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

// 识别回归语料：解析一份（testdata/recognize_corpus.txt）、TMDB 候选选择一份
// （testdata/recognize_match.json）。改识别逻辑前后各跑一遍，看通过率有没有退步。

type corpusRow struct {
	line            int
	status, name    string
	title, year     string
	season, episode int
	tv              bool
	tmdbID          int
	tmdbKind, note  string
}

func loadParseCorpus(t *testing.T) []corpusRow {
	t.Helper()
	f, err := os.Open("testdata/recognize_corpus.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dash := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	var rows []corpusRow
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, " | ")
		if len(cols) < 8 {
			t.Fatalf("语料第 %d 行字段不足: %q", n, line)
		}
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		r := corpusRow{line: n, status: cols[0], name: cols[1], title: dash(cols[2]), year: dash(cols[3]), tv: cols[6] == "1"}
		r.season, _ = strconv.Atoi(cols[4])
		r.episode, _ = strconv.Atoi(cols[5])
		if tag := dash(cols[7]); tag != "" {
			id, kind, _ := strings.Cut(tag, "/")
			r.tmdbID, _ = strconv.Atoi(id)
			r.tmdbKind = kind
		}
		if len(cols) > 8 {
			r.note = cols[8]
		}
		if r.status != "ok" && r.status != "todo" {
			t.Fatalf("语料第 %d 行状态只能是 ok / todo: %q", n, r.status)
		}
		rows = append(rows, r)
	}
	return rows
}

func TestRecognizeParseCorpus(t *testing.T) {
	rows := loadParseCorpus(t)
	pass, todo, todoPass := 0, 0, 0
	for _, r := range rows {
		p := parseFileName(r.name)
		got := fmt.Sprintf("片名=%q 年份=%q S%dE%d 剧集=%v 标签=%d/%s", p.Title, p.Year, p.Season, p.Episode, p.IsTV, p.TmdbID, p.TmdbKind)
		want := fmt.Sprintf("片名=%q 年份=%q S%dE%d 剧集=%v 标签=%d/%s", r.title, r.year, r.season, r.episode, r.tv, r.tmdbID, r.tmdbKind)
		ok := got == want
		if ok {
			pass++
		}
		switch {
		case r.status == "todo":
			todo++
			if ok {
				todoPass++
				t.Logf("第 %d 行已经能正确解析，可以把状态改成 ok: %s", r.line, r.name)
			}
		case !ok:
			t.Errorf("第 %d 行 %s（%s）\n  得到 %s\n  期望 %s", r.line, r.name, r.note, got, want)
		}
	}
	t.Logf("解析语料：%d/%d 条正确（其中待改进 %d 条，已通过 %d 条）", pass, len(rows), todo, todoPass)
}

// matchCorpus testdata/recognize_match.json 的结构
type matchCorpus struct {
	Cases []struct {
		Name string `json:"name"`
		Want string `json:"want"`
		Note string `json:"note"`
	} `json:"cases"`
	Search map[string]json.RawMessage `json:"search"`
	Detail map[string]json.RawMessage `json:"detail"`
}

// fakeTmdb 按语料里的 search / detail 表应答的假 TMDB
func fakeTmdb(t *testing.T, c *matchCorpus) *TmdbClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if kind, ok := strings.CutPrefix(r.URL.Path, "/search/"); ok {
			year := q.Get("year") + q.Get("first_air_date_year")
			results := c.Search[kind+"|"+q.Get("query")+"|"+year]
			if results == nil {
				results = json.RawMessage("[]")
			}
			fmt.Fprintf(w, `{"results":%s}`, results)
			return
		}
		if d, ok := c.Detail[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Write(d)
			return
		}
		http.Error(w, `{"status_code":34}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}
}

func resetTmdbCaches() {
	tmdbSearchMu.Lock()
	tmdbSearchCache = map[string]tmdbCacheEntry{}
	tmdbSearchMu.Unlock()
	tmdbDetailMu.Lock()
	tmdbDetailCache = map[string]tmdbDetailEntry{}
	tmdbDetailMu.Unlock()
}

func TestRecognizeMatchCorpus(t *testing.T) {
	newTestDB(t, "recognize-corpus.db") // AI 兜底读配置要用到库；空库 = 不走 AI
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
	pass := 0
	for _, cs := range c.Cases {
		media, err := tc.recognize(parseFileName(cs.Name))
		if err != nil {
			t.Errorf("%s: 识别报错 %v", cs.Name, err)
			continue
		}
		got := ""
		if media != nil {
			got = fmt.Sprintf("%s/%d", media.MediaType, media.TmdbID)
		}
		if got != cs.Want {
			t.Errorf("%s（%s）\n  得到 %q，期望 %q", cs.Name, cs.Note, got, cs.Want)
			continue
		}
		pass++
	}
	t.Logf("匹配语料：%d/%d 条正确", pass, len(c.Cases))
}

// 选中之后的产地要从同一份详情里取（分类规则按 origin_country 分），不能丢
func TestRecognizeKeepsOriginCountry(t *testing.T) {
	newTestDB(t, "recognize-country.db")
	raw, _ := os.ReadFile("testdata/recognize_match.json")
	var c matchCorpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	tc := fakeTmdb(t, &c)
	media, err := tc.recognize(parseFileName("Parasite.2020.1080p.mkv"))
	if err != nil || media == nil {
		t.Fatalf("识别失败: %v", err)
	}
	if strings.Join(media.OrigCountry, ",") != "KR" {
		t.Fatalf("产地丢了: %v", media.OrigCountry)
	}
}

// 详情请求失败（网络/限流）时不能把「没选出来」当成确定的没有：
// 调用方会缓存这个结论并把文件移进冗余
func TestRecognizeDetailErrorIsTransient(t *testing.T) {
	resetTmdbCaches()
	t.Cleanup(resetTmdbCaches)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search/") {
			fmt.Fprint(w, `{"results":[{"id":1,"title":"甲","original_title":"Kou","release_date":"2001-01-01"}]}`)
			return
		}
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, Language: "zh-CN", httpClient: srv.Client()}
	media, err := tc.SearchMovie("Otsu", "")
	if media != nil || err == nil {
		t.Fatalf("详情取不到时应返回错误让调用方下轮重试，得到 media=%v err=%v", media, err)
	}
}

func TestTitleLevel(t *testing.T) {
	cases := []struct {
		q, name string
		want    int
	}{
		{"Dune Part Two", "Dune: Part Two", titleExact},
		{"ＤＵＮＥ", "dune", titleExact},
		{"The Office US", "The Office", titleLoose},
		{"狂飙 全39集", "狂飙", titleLoose},
		{"up", "Upgrade", titleNone}, // 太短的英文不算包含
		{"爱", "爱情公寓", titleNone},     // 单个汉字不算包含
		{"流浪地球", "星际穿越", titleNone},
	}
	for _, c := range cases {
		if got := titleLevel(titleKey(c.q), c.name); got != c.want {
			t.Errorf("titleLevel(%q, %q) = %d, want %d", c.q, c.name, got, c.want)
		}
	}
}
