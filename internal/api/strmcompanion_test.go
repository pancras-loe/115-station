package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 删 STRM 时连集 NFO / 剧照一起删的其余几条路：失效 STRM 清理、洗版让位。
// 增量的三个分支见 incr_companion_test.go

func TestCleanOrphansDropsCompanions(t *testing.T) {
	newTestDB(t, "orphan_companion.db")
	root := t.TempDir()
	model.DB.Create(&model.Setting{Key: "full", Value: `{"local_path":"` + filepath.ToSlash(root) + `"}`})
	season := "剧集/越狱/Season 4"
	gone, alive := "越狱.S04E21", "越狱.S04E22"
	for _, n := range []string{gone + ".strm", gone + ".nfo", gone + "-thumb.jpg",
		alive + ".strm", alive + ".nfo", alive + "-thumb.jpg"} {
		writeTestFile(t, filepath.Join(root, season, n))
	}
	now := time.Now()
	model.DB.Create(&model.SyncedFile{FileID: "a", Kind: "video", RelPath: season + "/" + gone + ".strm", OrphanAt: &now})
	model.DB.Create(&model.SyncedFile{FileID: "b", Kind: "video", RelPath: season + "/" + alive + ".strm"})

	h := &Handler{DB: model.DB}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/sync/orphans/clean", nil)
	h.CleanOrphans(c)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	for _, n := range []string{gone + ".strm", gone + ".nfo", gone + "-thumb.jpg"} {
		if exists(filepath.Join(root, season, n)) {
			t.Fatalf("%s 应已删除", n)
		}
	}
	for _, n := range []string{alive + ".strm", alive + ".nfo", alive + "-thumb.jpg"} {
		if !exists(filepath.Join(root, season, n)) {
			t.Fatalf("%s 不是失效的，不该删", n)
		}
	}
}

// 洗版让位：新版落到同一个名字上，旧版的集 NFO / 剧照留给新版；不同名就删掉
func TestWashVictimCompanionsKeptOnlyWhenSameName(t *testing.T) {
	cases := []struct {
		name    string
		old     string // 旧版 STRM 基名
		landing string
		kept    bool
	}{
		{"同名", "X.S01E01", "剧集/X/Season 01/X.S01E01.mkv", true},
		{"旧版是旧写法", "X.S01E01.mkv", "剧集/X/Season 01/X.S01E01.mkv", true},
		{"不同名", "X.S01E01", "剧集/X/Season 01/X.S01E01.2160p.mkv", false},
		{"另一部同名剧", "X.S01E01", "剧集/Y/Season 01/X.S01E01.mkv", false},
		{"不知道新版叫什么", "X.S01E01", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			washTestDB(t)
			root := t.TempDir()
			model.DB.Create(&model.Setting{Key: "full", Value: `{"local_path":"` + filepath.ToSlash(root) + `"}`})
			dir := "媒体库/剧集/X/Season 01"
			old := tc.old
			for _, n := range []string{old + ".strm", old + ".nfo", old + "-thumb.jpg"} {
				writeTestFile(t, filepath.Join(root, dir, n))
			}
			row := model.SyncedFile{FileID: "old", Kind: "video", RelPath: dir + "/" + old + ".strm"}
			model.DB.Create(&row)
			plan := &washPlan{decision: washReplaced, victims: []model.SyncedFile{row}, targetDir: "剧集/X/Season 01"}
			if tc.landing != "" {
				plan.landing = []string{tc.landing}
			}
			err := applyWashPlans(&washSuccessOps{}, &OrgConfig{}, &TmdbMedia{MediaType: "tv"},
				&washStrategy{Mode: "replace", OldVersionTarget: "delete"}, []*washPlan{plan}, quiet, func(...string) {})
			if err != nil {
				t.Fatal(err)
			}
			if exists(filepath.Join(root, dir, old+".strm")) {
				t.Fatal("旧版 STRM 应已删除")
			}
			for _, n := range []string{old + ".nfo", old + "-thumb.jpg"} {
				if got := exists(filepath.Join(root, dir, n)); got != tc.kept {
					t.Fatalf("%s 存在=%v，期望 %v", n, got, tc.kept)
				}
			}
		})
	}
}
