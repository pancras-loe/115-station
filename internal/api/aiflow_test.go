package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 分数：模型自评是起点，能核实的信号对不上就封顶
func TestAIScore(t *testing.T) {
	cases := []struct {
		name   string
		parsed *ParsedName
		media  *TmdbMedia
		conf   int
		want   int
	}{
		{"改写后片名相等、年份一致：用自评", &ParsedName{Title: "Liu Lang Di Qiu", Year: "2019"},
			&TmdbMedia{Title: "流浪地球", Year: "2019", MediaType: "movie", Via: viaAITitle, matchHow: "片名相等"}, 90, 90},
		{"年份差 3 年：封顶 50", &ParsedName{Title: "X", Year: "2016"},
			&TmdbMedia{Title: "X", Year: "2019", MediaType: "movie", Via: viaAITitle}, 95, 50},
		{"非首季剧集不比年份", &ParsedName{Title: "X", Year: "2024", IsTV: true, Season: 4, Episode: 1},
			&TmdbMedia{Title: "X", Year: "2019", MediaType: "tv", Via: viaAITitle}, 90, 90},
		{"有集号却判成电影：封顶 60", &ParsedName{Title: "X", IsTV: true, Season: 1, Episode: 3},
			&TmdbMedia{Title: "X", MediaType: "movie", Via: viaAITitle}, 90, 60},
		{"搜出来只是相近：封顶 70", &ParsedName{Title: "X"},
			&TmdbMedia{Title: "X Y", MediaType: "movie", Via: viaAITitle, matchHow: "片名相近、年份相符"}, 90, 70},
		{"挑候选、片名对不上：封顶 75", &ParsedName{Title: "Sousou no Frieren", IsTV: true},
			&TmdbMedia{Title: "葬送的芙莉莲", OriginalTitle: "葬送のフリーレン", MediaType: "tv", Via: viaAIPick}, 90, 75},
		{"挑候选、片名沾边：用自评", &ParsedName{Title: "葬送的芙莉莲 番外"},
			&TmdbMedia{Title: "葬送的芙莉莲", MediaType: "tv", Via: viaAIPick}, 88, 88},
		{"没给把握度：按 60", &ParsedName{Title: "X"}, &TmdbMedia{Title: "X", Via: viaAITitle}, 0, 60},
		{"两条硬伤取更低的封顶", &ParsedName{Title: "X", Year: "2010", IsTV: true, Episode: 2},
			&TmdbMedia{Title: "X", Year: "2020", MediaType: "movie", Via: viaAITitle}, 99, 50},
	}
	for _, c := range cases {
		got, note := aiScore(c.parsed, c.media, c.conf)
		if got != c.want {
			t.Errorf("%s：得分 %d，期望 %d（%s）", c.name, got, c.want, note)
		}
		if note == "" {
			t.Errorf("%s：没有打分依据", c.name)
		}
	}
}

// 三档确认策略；规则环节识别出来的结果不受影响
func TestAIHoldReason(t *testing.T) {
	ai := &TmdbMedia{Via: viaAIPick, AIScore: 70}
	high := &TmdbMedia{Via: viaAITitle, AIScore: 85}
	rule := &TmdbMedia{AIScore: 0}
	cfg := func(mode string, min int) *aiRecognizeCfg { return &aiRecognizeCfg{ConfirmMode: mode, MinScore: min} }

	if r := aiHoldReason(cfg(aiConfirmOff, 80), ai); r != "" {
		t.Errorf("off 不该停: %q", r)
	}
	if r := aiHoldReason(cfg(aiConfirmForce, 80), high); r == "" {
		t.Error("force 高分也要停")
	}
	if r := aiHoldReason(cfg(aiConfirmAuto, 80), ai); !strings.Contains(r, "70") || !strings.Contains(r, "80") {
		t.Errorf("auto 低于线要停并说明分数: %q", r)
	}
	if r := aiHoldReason(cfg(aiConfirmAuto, 80), high); r != "" {
		t.Errorf("auto 高于线不该停: %q", r)
	}
	if r := aiHoldReason(cfg(aiConfirmAuto, 80), &TmdbMedia{Via: viaAIPick, AIScore: 80}); r != "" {
		t.Errorf("正好压线算通过: %q", r)
	}
	for _, mode := range []string{aiConfirmOff, aiConfirmAuto, aiConfirmForce} {
		if r := aiHoldReason(cfg(mode, 80), rule); r != "" {
			t.Errorf("%s：规则环节识别的结果不该被 AI 策略拦下: %q", mode, r)
		}
	}
	if r := aiHoldReason(nil, ai); r != "" {
		t.Errorf("AI 没开时不该停: %q", r)
	}
}

// aiStub 假模型：候选判断与改写片名分别回不同的内容（按请求里有没有候选列表区分）
func aiStub(t *testing.T, guess, pick string, calls *[]string) *aiRecognizeCfg {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reply, kind := guess, "guess"
		if strings.Contains(string(b), "TMDB 候选") {
			reply, kind = pick, "pick"
		}
		*calls = append(*calls, kind)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return &aiRecognizeCfg{URL: srv.URL, Model: "m", ConfirmMode: aiConfirmAuto, MinScore: 80}
}

func loadMatchFixture(t *testing.T) *TmdbClient {
	t.Helper()
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
	return fakeTmdb(t, &c)
}

// 改写：拼音片名规则环节搜不中，模型给出中文名后搜中，一次调用就够
func TestRecognizeByAIGuess(t *testing.T) {
	tc := loadMatchFixture(t)
	var calls []string
	cfg := aiStub(t, `{"title":"流浪地球","original_title":"The Wandering Earth","year":"2019","type":"movie","confidence":90}`, `{"pick":0}`, &calls)
	p := parseFileName("Liu.Lang.Di.Qiu.2019.1080p.mkv")
	p.Source = "Liu.Lang.Di.Qiu.2019.1080p.mkv"
	media, err := tc.recognizeByAI(cfg, p)
	if err != nil || media == nil || media.TmdbID != 535167 {
		t.Fatalf("改写后应识别为流浪地球 535167: %+v %v", media, err)
	}
	if media.Via != viaAITitle || media.AIScore != 90 || media.AINote == "" {
		t.Fatalf("标注不对: via=%s score=%d note=%q", media.Via, media.AIScore, media.AINote)
	}
	if strings.Join(calls, ",") != "guess" {
		t.Fatalf("搜中了就不该再调候选判断: %v", calls)
	}
	// 标注打在副本上：缓存里的那份不能带着 AI 标注被别的识别拿去用
	if cached, _ := tc.SearchMovie("流浪地球", "2019"); cached == nil || cached.Via != "" {
		t.Fatalf("缓存被 AI 标注污染: %+v", cached)
	}
}

// 挑选：改写也搜不中时，候选交给模型选；片名对不上所以封顶 75，低于 80 线要等确认
func TestRecognizeByAIPick(t *testing.T) {
	tc := loadMatchFixture(t)
	var calls []string
	cfg := aiStub(t, `{"title":"不存在的片","type":"tv","confidence":40}`,
		`{"pick":1,"confidence":92,"reason":"Sousou no Frieren 是葬送的芙莉莲的罗马音"}`, &calls)
	name := "[Nekomoe kissaten][Sousou no Frieren][01][1080p][JPSC].mp4"
	p := parseFileName(name)
	p.Source = name
	media, err := tc.recognizeByAI(cfg, p)
	if err != nil || media == nil || media.TmdbID != 209867 || media.MediaType != "tv" {
		t.Fatalf("应从候选里选中葬送的芙莉莲: %+v %v", media, err)
	}
	if media.Via != viaAIPick || media.AIScore != 75 || !strings.Contains(media.AINote, "罗马音") {
		t.Fatalf("标注不对: via=%s score=%d note=%q", media.Via, media.AIScore, media.AINote)
	}
	if strings.Join(calls, ",") != "guess,pick" {
		t.Fatalf("应先改写、再挑选，最多两次: %v", calls)
	}
	if r := aiHoldReason(cfg, media); r == "" {
		t.Fatal("75 分低于 80 分的线，应当等人工确认")
	}
}

// 模型说都不是：判未识别，不硬选
func TestRecognizeByAINoneFits(t *testing.T) {
	tc := loadMatchFixture(t)
	var calls []string
	cfg := aiStub(t, `{"title":"不存在的片","confidence":30}`, `{"pick":0,"reason":"都不像"}`, &calls)
	p := parseFileName("[Nekomoe kissaten][Sousou no Frieren][01][1080p][JPSC].mp4")
	if media, err := tc.recognizeByAI(cfg, p); err != nil || media != nil {
		t.Fatalf("模型说都不是时应返回未识别: %+v %v", media, err)
	}
}

// AI 判定停下的待确认：「人工确认」开关关着也不能被自动整理接手，
// 否则每一轮都会重新识别、再调一次模型、又停回来
func TestAIHeldSurvivesManualConfirmOff(t *testing.T) {
	newTestDB(t, "ai_hold.db")
	ensureRenameTpl()
	ctx := newConfirmCtx(false)
	media := &TmdbMedia{TmdbID: 209867, Title: "葬送的芙莉莲", Year: "2023", MediaType: "tv",
		Via: viaAIPick, AIScore: 75, AINote: "片名与文件名对不上"}
	ctx.sink.recog.markAI(media)
	ctx.sink.recog.holdAI = true
	name := "Frieren.01.mkv"
	ctx.holdForConfirm(name, "f1", "file", media, parseFileName(name), name,
		[]orgRecordFile{{Fid: "f1", Name: name, Kind: "video"}}, "AI 判定 75 分，低于自动入库线 80 分，等人工确认")

	var rec model.OrganizeRecord
	model.DB.First(&rec)
	if !rec.HoldAI || rec.RecogVia != viaAIPick || rec.AIScore != 75 || !strings.Contains(rec.Message, "75 分") {
		t.Fatalf("记录没标成 AI 停下: %+v", rec)
	}
	held := loadAwaiting()
	if held["f1"] == nil || !held["f1"].ai {
		t.Fatalf("重新加载后丢了 AI 标记: %+v", held["f1"])
	}
	ctx2 := newConfirmCtx(false)
	ctx2.held = held
	entries := []dirEntry{{Fid: "f1"}, {Fid: "other"}}
	if got := ctx2.dropHeld(entries); len(got) != 1 || got[0].Fid != "other" {
		t.Fatalf("开关关着也应跳过 AI 停下的条目: %+v", got)
	}
}
