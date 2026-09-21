package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"115-station/internal/model"
	"github.com/gin-gonic/gin"
)

func washTestDB(t *testing.T) {
	t.Helper()
	previous := model.DB
	db, err := model.InitDB(filepath.Join(t.TempDir(), "wash.db"))
	if err != nil {
		t.Fatal(err)
	}
	resetWashCache()
	t.Cleanup(func() {
		model.DB = previous
		resetWashCache()
		sqlDB, _ := db.DB()
		sqlDB.Close()
	})
}

func TestWashSavePreservesUserChoice(t *testing.T) {
	washTestDB(t)
	if err := model.InitDefaultWashConfig(model.DB); err != nil {
		t.Fatal(err)
	}
	if len(washStrategyCache()) != 2 {
		t.Fatal("首次部署未启用默认策略")
	}
	h := &Handler{DB: model.DB}
	r := gin.New()
	r.POST("/wash", h.SaveWashRules)
	for _, tc := range []struct {
		body   string
		status int
		mode   string
	}{
		{`{"yaml":"自定义:\n  mode: skip\n  media_type: movie\n"}`, 200, "skip"},
		{`{"yaml":"[错误"}`, 400, "skip"},
		{`{"yaml":""}`, 200, ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/wash", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("保存返回 %d: %s", w.Code, w.Body.String())
		}
		// 模拟重启播种，用户修改和空配置均不得被默认值覆盖。
		if err := model.InitDefaultWashConfig(model.DB); err != nil {
			t.Fatal(err)
		}
		st := matchWashStrategy("movie", "")
		if tc.mode == "" {
			if st != nil {
				t.Fatal("清空后仍有策略")
			}
		} else if st == nil || st.Mode != tc.mode {
			t.Fatalf("保存未立即生效: %+v", st)
		}
		resetWashCache()
		reloaded := matchWashStrategy("movie", "")
		if (reloaded == nil) != (st == nil) || (reloaded != nil && reloaded.Mode != st.Mode) {
			t.Fatal("重启后用户策略被覆盖")
		}
	}
}

func TestWashLedgerLiteralAndDirectScope(t *testing.T) {
	washTestDB(t)
	for i, rel := range []string{
		"库/电影/A_100%/old.1080p.mkv.strm",
		"库/电影/AX100abc/unrelated.mkv.strm",
		"库/电影/A_100%/另一部片/unrelated.mkv.strm",
		"别的库/电影/A_100%/other.mkv.strm",
	} {
		row := model.SyncedFile{FileID: string(rune('a' + i)), RelPath: rel}
		if err := model.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := libraryFilesOf("电影/A_100%", "库")
	if len(rows) != 1 || rows[0].FileID != "a" {
		t.Fatalf("查询扩大范围: %+v", rows)
	}
	if rows := libraryFilesOf("电影", "库"); len(rows) != 0 {
		t.Fatal("分类目录匹配到了子影片")
	}
	if rows := libraryFilesOf("电影/A_100%", ""); len(rows) != 0 {
		t.Fatal("库名不明确时跨库匹配")
	}
	if rows := libraryFilesOf("电影/A_100%", "不存在的库"); len(rows) != 0 {
		t.Fatal("已知库名仍回退其他库")
	}
}

type washFailOps struct{ moved []string }

func (f *washFailOps) ensurePath(string, string) (string, error) { return "old", nil }
func (f *washFailOps) moveFiles(_ string, ids []string) error {
	f.moved = append(f.moved, ids...)
	return errors.New("模拟移动失败")
}

func TestWashActualVictimsAndFailure(t *testing.T) {
	washTestDB(t)
	rows := []model.SyncedFile{
		{FileID: "poster", RelPath: "库/剧/Season 01/poster.jpg"},
		{FileID: "v5", RelPath: "库/剧/Season 01/剧.S01E05.1080p.strm"},
		{FileID: "s5", RelPath: "库/剧/Season 01/剧.S01E05.1080p.chs.srt"},
		{FileID: "v6", RelPath: "库/剧/Season 01/剧.S01E06.1080p.mkv.strm"},
		{FileID: "s6", RelPath: "库/剧/Season 01/剧.S01E05.1080pExtra.srt"},
	}
	for i := range rows {
		if err := model.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	st := &washStrategy{Mode: "replace", PriorityLevel: []washRule{{ResourcePix: "2160p"}, {ResourcePix: "1080p"}}}
	ops := &washFailOps{}
	got := runWashReplace(ops, &OrgConfig{Redundant: "redundant"}, &TmdbMedia{MediaType: "tv"}, "剧.S01E05.2160p.mkv", "剧/Season 01", st, rows, quiet)
	if got != washFailed || !reflect.DeepEqual(ops.moved, []string{"v5", "s5"}) {
		t.Fatalf("结果=%s，实际搬移=%v", got, ops.moved)
	}
	var count int64
	model.DB.Model(&model.SyncedFile{}).Count(&count)
	if count != int64(len(rows)) {
		t.Fatal("搬移失败仍清理了台账")
	}
}

func TestWashActualDecisionModes(t *testing.T) {
	rows := []model.SyncedFile{{FileID: "old", Kind: "video", RelPath: "剧.S01E01.2160p.strm"}}
	for _, tc := range []struct{ name, mode, scope, want string }{
		{"剧.S01E01.1080p.mkv", "skip", "", washNotBetter},
		{"剧.S01E01.1080p.mkv", "coexist", "", washSkip},
		{"剧.S01E02.1080p.mkv", "replace", "", washSkip},
		{"剧.S02E01.1080p.mkv", "replace", "", washSkip},
		{"剧.1080p.mkv", "replace", "", washSkip},
		{"剧.S01E01.1080p.mkv", "replace", "group", washSkip},
		{"剧.S01E01.1080p.mkv", "replace", "", washNotBetter},
	} {
		st := &washStrategy{Mode: tc.mode, Scope: tc.scope}
		if tc.mode == "replace" {
			st.PriorityLevel = []washRule{{ResourcePix: "2160p"}}
		}
		ops := &washFailOps{}
		got := runWashReplace(ops, &OrgConfig{}, &TmdbMedia{MediaType: "tv"}, tc.name, "剧", st, rows, quiet)
		if got != tc.want || len(ops.moved) != 0 {
			t.Fatalf("%+v: %s, %v", tc, got, ops.moved)
		}
	}
}

func TestRedoWashRecordUsesVideoPath(t *testing.T) {
	plan := redoLayout{rootRel: "电影/分类/片名", groups: map[string][]orgRecordFile{
		"电影/分类/片名": {{Kind: "meta", Name: "poster.jpg"}, {Kind: "video", Name: "片名.mkv"}},
	}}
	if got := plan.sampleVideoPath(); got != "电影/分类/片名/片名.mkv" {
		t.Fatal(got)
	}
}

func TestWashEpisodeRangesAreNotPartialVictims(t *testing.T) {
	if sameWashEpisode("剧.S01E01.2160p.mkv", "剧.S01E01-E03.1080p.mkv") {
		t.Fatal("单集不能替换多集合集")
	}
	if !sameWashEpisode("剧.S01E01-E03.2160p.mkv", "剧.S01E01-E03.1080p.mkv") {
		t.Fatal("相同范围应该可以比较")
	}
}
