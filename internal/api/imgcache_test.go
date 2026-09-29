package api

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一张图并发请求只拉一次，之后走磁盘
func TestCachedImageDedupeAndDisk(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	gate := make(chan struct{})
	fetch := func() ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		<-gate
		return []byte("img-bytes"), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if data, err := cachedImage(dir, "t", "k1", 0, fetch); err != nil || string(data) != "img-bytes" {
				t.Errorf("got %q %v", data, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls != 1 {
		t.Fatalf("并发请求应只拉一次，实际 %d", calls)
	}
	if _, err := cachedImage(dir, "t", "k1", 0, func() ([]byte, error) {
		t.Fatal("磁盘命中不该再拉")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 过期的缓存要重新拉；失败记忆期间不再打上游
func TestCachedImageTTLAndFailMemory(t *testing.T) {
	dir := t.TempDir()
	if _, err := cachedImage(dir, "e", "k", time.Hour, func() ([]byte, error) { return []byte("v1"), nil }); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(imgCacheFile(dir, "e", "k"), old, old)
	data, _ := cachedImage(dir, "e", "k", time.Hour, func() ([]byte, error) { return []byte("v2"), nil })
	if string(data) != "v2" {
		t.Fatalf("过期缓存应重新拉取，得到 %q", data)
	}

	var calls int
	bad := func() ([]byte, error) { calls++; return nil, errors.New("down") }
	_, err1 := cachedImage(dir, "e", "gone", 0, bad)
	_, err2 := cachedImage(dir, "e", "gone", 0, bad)
	if err1 == nil || err2 == nil || calls != 1 {
		t.Fatalf("失败应被记住：calls=%d err1=%v err2=%v", calls, err1, err2)
	}
}

func TestSweepImgCache(t *testing.T) {
	dir := t.TempDir()
	_, _ = cachedImage(dir, "t", "old", 0, func() ([]byte, error) { return []byte("a"), nil })
	_, _ = cachedImage(dir, "t", "new", 0, func() ([]byte, error) { return []byte("b"), nil })
	old := time.Now().Add(-90 * 24 * time.Hour)
	_ = os.Chtimes(imgCacheFile(dir, "t", "old"), old, old)
	if n := sweepImgCache(dir+"/imgcache", imgCacheIdle); n != 1 {
		t.Fatalf("应清掉 1 个，实际 %d", n)
	}
	if _, err := os.Stat(imgCacheFile(dir, "t", "new")); err != nil {
		t.Fatal("最近用过的不该被清")
	}
}

func TestValidTMDBPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/abc123.jpg":        true,
		"/abc.png":           true,
		"abc.jpg":            false,
		"/a/b.jpg":           false,
		"/../etc/passwd.jpg": false,
		"/x.html":            false,
		"":                   false,
	} {
		if got := validTMDBPath(p); got != want {
			t.Errorf("%q: got %v want %v", p, got, want)
		}
	}
}
