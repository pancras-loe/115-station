package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// 并发翻页：按页序交给 fn、读到不满的一页就停，不漏不重
func TestWalkLocalEmbyParallelPages(t *testing.T) {
	const total = 1234 // 500 + 500 + 234：第一批三页里最后一页不满
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Library/VirtualFolders/Query":
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		case "/Items":
			reqs.Add(1)
			start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
			items := []map[string]any{}
			for i := start; i < min(start+limit, total); i++ {
				items = append(items, map[string]any{"Id": strconv.Itoa(i), "Type": "Movie",
					"Path": fmt.Sprintf("/lib/电影/片%d (2020)/片%d.strm", i%7, i)})
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": items})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ledger := map[string]*ledgerTitleEntry{}
	for i := 0; i < 7; i++ {
		k := fmt.Sprintf("电影/片%d (2020)", i)
		ledger[k] = &ledgerTitleEntry{Key: k}
	}
	var got []int
	scanned, err := walkLocalEmby(embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}, "/lib", ledger, func(key string, it embyExtractItem) {
		n, _ := strconv.Atoi(it.ID)
		got = append(got, n)
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned != total || len(got) != total {
		t.Fatalf("读到 %d 条、交给 fn %d 条，应为 %d", scanned, len(got), total)
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("第 %d 条是 %d：没按页序交给 fn", i, n)
		}
	}
	if n := reqs.Load(); n != 3 {
		t.Fatalf("应只拉一批三页，实际 %d 次", n)
	}
}

// 页面请求不等 Emby：后台在拉时立即返回 refreshing，拉完快照就位
func TestKickLocalEmbyNonBlocking(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Library/VirtualFolders/Query":
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		case "/Items":
			<-release
			json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		}
	}))
	defer srv.Close()
	ledgerTestDB(t, nil, nil)
	t.Cleanup(invalidateLedgerTitles) // 后台拉取会把空台账缓存 30 秒，别留给后面的用例

	s := &localEmbySnap
	s.mu.Lock()
	s.stats, s.err, s.at = nil, "", time.Time{}
	s.mu.Unlock()
	cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}

	began := time.Now()
	if !kickLocalEmby(cfg, "/lib", false) {
		t.Fatal("没有快照时应开始后台拉取")
	}
	if !kickLocalEmby(cfg, "/lib", false) {
		t.Fatal("拉取途中再问应报 refreshing，且不另起一份")
	}
	if time.Since(began) > time.Second {
		t.Fatal("请求被 Emby 拖住了")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for kickLocalEmby(cfg, "/lib", false) {
		if time.Now().After(deadline) {
			t.Fatal("后台拉取一直没结束")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if localEmbyStats() == nil {
		t.Fatal("拉完应有快照")
	}
	s.mu.Lock()
	s.stats, s.at = nil, time.Time{}
	s.mu.Unlock()
}
