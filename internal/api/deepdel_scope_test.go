package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"115-station/internal/model"
)

func setDeepDelTestLibraries(t *testing.T, h *Handler, roots []string, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Library/VirtualFolders" {
			t.Errorf("意外请求: %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"Locations": roots}})
	}))
	t.Cleanup(srv.Close)
	cfg, _ := json.Marshal(map[string]string{"server_url": srv.URL, "api_key": "test"})
	h.DB.Where("key = ?", "emby").Delete(&model.Setting{})
	if err := h.DB.Create(&model.Setting{Key: "emby", Value: string(cfg)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDeepDelRejectsContainer633Files(t *testing.T) {
	for _, kind := range []string{"Folder", "CollectionFolder", "AggregateFolder", "", "Unknown"} {
		for _, deep := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deep=%v", kind, deep), func(t *testing.T) {
				root := deepDelTestTree(t, "影视/keep")
				var ledger []model.SyncedFile
				for i := 0; i < 633; i++ {
					row := model.SyncedFile{FileID: fmt.Sprint(i), RelPath: fmt.Sprintf("影视/电影/A%d/a.strm", i), Kind: "video"}
					if i >= 622 {
						row.RelPath = fmt.Sprintf("影视/电影/A%d/a.nfo", i)
						row.Kind = "asset"
					}
					ledger = append(ledger, row)
				}
				h := eventTestHandler(t, root, ledger)
				payload := eventPayload(root, "影视/电影")
				payload["Event"] = "library.deleted"
				item := payload["Item"].(map[string]interface{})
				item["Type"], item["IsFolder"], item["ParentId"] = kind, true, "1"
				h.processDeepDelEvent(payload, deep, func([]model.SyncedFile, string) (deepDelResult, error) {
					t.Fatal("目录事件触发了源文件删除")
					return deepDelResult{}, nil
				}, func() bool { t.Fatal("应在入口拒绝目录事件"); return false })
				var left, rejected int64
				h.DB.Model(&model.SyncedFile{}).Count(&left)
				h.DB.Model(&model.DeepDeleteRecord{}).Where("status = ?", "rejected").Count(&rejected)
				if left != 633 || rejected != 1 {
					t.Fatalf("台账=%d 拦截记录=%d", left, rejected)
				}
			})
		}
	}
}

func TestDeepDelRechecksAfterLibraryRequest(t *testing.T) {
	for _, mode := range []string{"restored", "disabled", "bad_json", "mapped"} {
		t.Run(mode, func(t *testing.T) {
			root := deepDelTestTree(t, "影视/电影/keep")
			rel := "影视/电影/A/a.strm"
			h := eventTestHandler(t, root, []model.SyncedFile{{FileID: "a", RelPath: rel, Kind: "video"}})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "restored":
					full := filepath.Join(root, filepath.FromSlash(rel))
					if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
						t.Error(err)
					}
					if err := os.WriteFile(full, []byte("restored"), 0644); err != nil {
						t.Error(err)
					}
				case "disabled":
					h.DB.Model(&model.Setting{}).Where("key = ?", "deepdel").Update("value", `{"enabled":false,"notify":false}`)
				case "bad_json":
					_, _ = w.Write([]byte(`{"Items":[]}`))
					return
				}
				location := filepath.ToSlash(filepath.Join(root, "影视", "电影"))
				if mode == "mapped" {
					location = "/emby-media/影视/电影"
				}
				_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"Locations": []string{location}}})
			}))
			t.Cleanup(srv.Close)
			cfg := map[string]string{"server_url": srv.URL, "api_key": "test"}
			payload := eventPayload(root, rel)
			if mode == "mapped" {
				cfg["path_mapping"] = filepath.ToSlash(root) + "#/emby-media"
				payload["Item"].(map[string]interface{})["Path"] = "/emby-media/" + rel
			}
			encoded, _ := json.Marshal(cfg)
			h.DB.Model(&model.Setting{}).Where("key = ?", "emby").Update("value", string(encoded))
			calls := 0
			h.processDeepDelEvent(payload, false, func([]model.SyncedFile, string) (deepDelResult, error) { calls++; return deepDelResult{}, nil }, func() bool { return true })
			want := 0
			if mode == "mapped" {
				want = 1
			}
			if calls != want {
				t.Fatalf("删除调用=%d，预期=%d", calls, want)
			}
		})
	}
}

func TestDeepDelMediaScopes(t *testing.T) {
	for _, tc := range []struct {
		kind, target string
		want         int
	}{
		{"Movie", "影视/电影/A/a.strm", 1},
		{"Episode", "影视/剧集/B/Season 01/e1.strm", 1},
		{"Series", "影视/剧集/B", 3},
		{"Season", "影视/剧集/B/Season 01", 1},
		{"Movie", "影视/电影", 0},
		{"Episode", "影视/剧集/B", 0},
		{"Series", "影视/剧集", 0},
		{"Season", "影视/剧集/B", 0},
	} {
		t.Run(tc.kind+tc.target, func(t *testing.T) {
			root := deepDelTestTree(t, "影视/keep")
			h := eventTestHandler(t, root, []model.SyncedFile{
				{FileID: "m", RelPath: "影视/电影/A/a.strm", Kind: "video"},
				{FileID: "m2", RelPath: "影视/电影/A/b.strm", Kind: "video"},
				{FileID: "e1", RelPath: "影视/剧集/B/Season 01/e1.strm", Kind: "video"},
				{FileID: "e2", RelPath: "影视/剧集/B/Season 02/e2.strm", Kind: "video"},
				{FileID: "nfo", RelPath: "影视/剧集/B/tvshow.nfo", Kind: "asset"},
				{FileID: "other", RelPath: "影视/剧集/BB/Season 01/e1.strm", Kind: "video"},
			})
			payload := eventPayload(root, tc.target)
			payload["Item"].(map[string]interface{})["Type"] = tc.kind
			got := 0
			h.processDeepDelEvent(payload, false, func(rows []model.SyncedFile, _ string) (deepDelResult, error) {
				got += len(rows)
				return deepDelResult{}, nil
			}, func() bool { return true })
			if got != tc.want {
				t.Fatalf("删除数量=%d，预期=%d", got, tc.want)
			}
		})
	}
}

func TestDeepDelCurrentLibraryGuards(t *testing.T) {
	for _, mode := range []string{"valid", "removed", "unavailable", "http_error", "nested_root", "removed_during_wait", "pickcode_removed"} {
		t.Run(mode, func(t *testing.T) {
			root := deepDelTestTree(t, "影视/电影/keep")
			rel := "影视/电影/A/a.strm"
			h := eventTestHandler(t, root, []model.SyncedFile{{FileID: "a", PickCode: "abc", RelPath: rel, Kind: "video"}})
			lib := filepath.Join(root, "影视", "电影")
			roots, status := []string{lib}, http.StatusOK
			switch mode {
			case "removed", "pickcode_removed":
				roots = nil
			case "unavailable":
				if err := os.Remove(filepath.Join(lib, "keep")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(lib); err != nil {
					t.Fatal(err)
				}
			case "http_error":
				status = http.StatusUnauthorized
			case "nested_root":
				roots = []string{root, filepath.Join(root, filepath.FromSlash(rel))}
			}
			setDeepDelTestLibraries(t, h, roots, status)
			payload := eventPayload(root, rel)
			deep := mode == "pickcode_removed"
			if deep {
				delete(payload["Item"].(map[string]interface{}), "Path")
				payload["Description"] = "Mount Paths:\nhttp://h/d/abc.mkv"
			}
			calls := 0
			h.processDeepDelEvent(payload, deep, func([]model.SyncedFile, string) (deepDelResult, error) { calls++; return deepDelResult{}, nil }, func() bool {
				if mode == "removed_during_wait" {
					setDeepDelTestLibraries(t, h, nil, http.StatusOK)
				}
				return true
			})
			want := 0
			if mode == "valid" {
				want = 1
			}
			if calls != want {
				t.Fatalf("删除调用=%d，预期=%d", calls, want)
			}
		})
	}
}

// 冒号是合法文件名字符：“美国队长.Captain America: The First Avenger…”
// 这种带冒号的片名曾被当成路径越界拦下（洗版验证实测）。
// 盘符开头的绝对路径仍然要拦
func TestDeepDelScopeAllowsColonInFilename(t *testing.T) {
	h := &Handler{}
	ok := "影视/电影/美国队长.2011.{tmdbid=1771}/美国队长.Captain America: The First Avenger.2011.1080p.mkv.strm"
	if err := h.checkDeepDelScope("Movie", []string{ok}); err != nil {
		t.Fatalf("带冒号的片名被误拦: %v", err)
	}
	for _, bad := range []string{"C:/影视/a.strm", "d:影视/a.strm"} {
		if err := h.checkDeepDelScope("Movie", []string{bad}); err == nil {
			t.Fatalf("盘符路径没拦住: %s", bad)
		}
	}
}
