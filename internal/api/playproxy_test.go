package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"115-station/internal/config"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestPlayProxyDecision(t *testing.T) {
	cfg := playbackCfg{Mode: playModeRedirect, Users: map[string]string{"friend": playModeProxy, "me": playModeRedirect}}
	cfg.normalize()
	for _, tc := range []struct {
		name string
		cfg  playbackCfg
		req  playReq
		want bool
	}{
		{"默认 302", cfg, playReq{}, false},
		{"单独设了中转的用户", cfg, playReq{EmbyUser: "FRIEND"}, true},
		{"没设的用户跟随默认", cfg, playReq{EmbyUser: "other"}, false},
		{"Emby 服务器自己来拉一律 302", cfg, playReq{EmbyUser: "friend", FromEmbyServer: true}, false},
		{"默认中转", playbackCfg{Mode: playModeProxy, Users: map[string]string{"me": playModeRedirect}}, playReq{}, true},
		{"默认中转但这个用户设了 302", playbackCfg{Mode: playModeProxy, Users: map[string]string{"me": playModeRedirect}}, playReq{EmbyUser: "me"}, false},
	} {
		if got := playProxy(tc.cfg, tc.req); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlaybackCfgNormalizeAndCheck(t *testing.T) {
	c := playbackCfg{Mode: "weird", MaxConns: 99, Users: map[string]string{" ABC ": playModeProxy, "x": "bad", "": playModeProxy}}
	c.normalize()
	if c.Mode != playModeRedirect || c.MaxConns != playbackMaxConns || len(c.Users) != 1 || c.Users["abc"] != playModeProxy {
		t.Fatalf("normalize: %+v", c)
	}
	if c.anyProxy() != true {
		t.Fatal("有用户中转时 anyProxy 应为真")
	}
	for _, bad := range []playbackCfg{{Mode: "x"}, {MaxConns: 17}, {MaxConns: -1}, {Users: map[string]string{"u": "x"}}} {
		if checkPlaybackCfg(bad) == nil {
			t.Errorf("没拦住 %+v", bad)
		}
	}
	if checkPlaybackCfg(playbackCfg{}) != nil {
		t.Fatal("空配置应当按默认放行")
	}
}

func TestParseRanges(t *testing.T) {
	for _, tc := range []struct {
		in    string
		ok    bool
		set   bool
		start int64
		end   int64
	}{
		{"", true, false, 0, -1},
		{"bytes=0-", true, true, 0, -1},
		{"bytes=10-19", true, true, 10, 19},
		{"bytes=-500", false, false, 0, 0}, // 后缀：原样透传
		{"bytes=0-1,5-6", false, false, 0, 0},
		{"bytes=9-3", false, false, 0, 0},
		{"items=0-1", false, false, 0, 0},
	} {
		r, ok := parseClientRange(tc.in)
		if ok != tc.ok || (ok && (r.set != tc.set || r.start != tc.start || r.end != tc.end)) {
			t.Errorf("%q: %+v %v", tc.in, r, ok)
		}
	}
	if a, b, total, ok := parseContentRange("bytes 5-9/100"); !ok || a != 5 || b != 9 || total != 100 {
		t.Fatal("Content-Range 解析错误")
	}
	if _, _, total, ok := parseContentRange("bytes 5-9/*"); !ok || total != -1 {
		t.Fatal("总大小未知应为 -1")
	}
}

func TestParseProxyLink(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l, err := parseProxyLink(fmt.Sprintf("https://cdn.example/f?t=%d&c=2&f=3", now.Add(time.Hour).Unix()), map[string]string{"Cookie": "a=b"}, now)
	if err != nil || l.limit != 2 || l.headers["Cookie"] != "a=b" || l.headers["User-Agent"] != ua115Download {
		t.Fatalf("要 Cookie 的直链中转时应当可用：%+v %v", l, err)
	}
	if !l.exp.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("有效期最多 30 分钟：%v", l.exp)
	}
	if _, err := parseProxyLink(fmt.Sprintf("https://cdn.example/f?t=%d", now.Add(3*time.Minute).Unix()), nil, now); err == nil {
		t.Fatal("快过期的直链应当拒绝")
	}
	if _, err := parseProxyLink("ftp://x/y", nil, now); err == nil {
		t.Fatal("非 http 地址应当拒绝")
	}
}

// fakeCDN 支持 Range 的假 CDN：按需让某几次请求失败或中途断开
type fakeCDN struct {
	srv     *httptest.Server
	data    []byte
	mu      sync.Mutex
	ranges  []string
	fail    map[int]int // 第 n 次请求（从 1 数）回这个状态码
	cutAt   map[int]int // 第 n 次请求只写这么多字节就断
	total   map[int]int // 第 n 次请求谎报总大小
	ignore  bool        // 不认 Range，整个回 200
	hits    atomic.Int64
	uas     []string
	cookies []string
}

func newFakeCDN(t *testing.T, size int) *fakeCDN {
	f := &fakeCDN{data: make([]byte, size), fail: map[int]int{}, cutAt: map[int]int{}, total: map[int]int{}}
	for i := range f.data {
		f.data[i] = byte(i % 251)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.hits.Add(1))
		f.mu.Lock()
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		f.uas = append(f.uas, r.UserAgent())
		f.cookies = append(f.cookies, r.Header.Get("Cookie"))
		code, cut, lie, ignore := f.fail[n], f.cutAt[n], f.total[n], f.ignore
		f.mu.Unlock()
		if code != 0 {
			if code == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "1")
			}
			w.WriteHeader(code)
			return
		}
		rng := r.Header.Get("Range")
		if rng == "" || ignore {
			w.Header().Set("Content-Length", strconv.Itoa(len(f.data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(f.data)
			return
		}
		cr, ok := parseClientRange(rng)
		if n, err := strconv.Atoi(strings.TrimPrefix(rng, "bytes=-")); err == nil && strings.HasPrefix(rng, "bytes=-") {
			cr, ok = clientRange{set: true, start: int64(len(f.data) - n), end: -1}, true
		}
		if !ok || !cr.set || cr.start >= int64(len(f.data)) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(f.data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end := int64(len(f.data)) - 1
		if cr.end >= 0 && cr.end < end {
			end = cr.end
		}
		total := len(f.data)
		if lie != 0 {
			total = lie
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cr.start, end, total))
		w.Header().Set("Content-Length", strconv.FormatInt(end-cr.start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		body := f.data[cr.start : end+1]
		if cut > 0 && cut < len(body) {
			_, _ = w.Write(body[:cut])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
				}
			}
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// proxyFixture 把中转的全局状态换成测试用的：小分段、假取链、干净的名额
type proxyFixture struct {
	cdn     *fakeCDN
	front   *httptest.Server
	fetches atomic.Int64
	query   atomic.Value // 直链额外的查询参数，比如 c=1
}

func setupProxyFixture(t *testing.T, size int) *proxyFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	p := &proxyFixture{cdn: newFakeCDN(t, size)}
	p.query.Store("")
	oldSeg, oldLinks, oldLim := proxySegSize, proxyLinks, proxyLimiter
	proxySegSize = 1000
	proxyLinks = newProxyLinkPool(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		n := p.fetches.Add(1)
		return fmt.Sprintf("%s/%s?n=%d%s", p.cdn.srv.URL, pc, n, p.query.Load().(string)),
			map[string]string{"User-Agent": ua, "Cookie": "acw=1"}, nil
	})
	proxyLimiter = &proxyAcctLimiter{active: map[uint]int{}, cool: map[uint]time.Time{}, now: time.Now}
	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeProxy, MaxConns: 3})
	t.Cleanup(func() { proxySegSize, proxyLinks, proxyLimiter = oldSeg, oldLinks, oldLim })
	r := gin.New()
	r.SetTrustedProxies(nil)
	registerDirectPlaybackRoutes(r, nil, &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()})
	p.front = httptest.NewServer(r)
	t.Cleanup(p.front.Close)
	return p
}

func setPlaybackCfgForTest(t *testing.T, c playbackCfg) {
	t.Helper()
	c.normalize()
	playbackCfgCache.Lock()
	playbackCfgCache.cfg, playbackCfgCache.at = &c, time.Now().Add(time.Hour)
	playbackCfgCache.Unlock()
	t.Cleanup(forgetPlaybackCfg)
}

func (p *proxyFixture) get(t *testing.T, method, rng string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, p.front.URL+"/d/abcdef/movie.mkv", nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	req.Header.Set("User-Agent", "SomePlayer/1")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestProxyStreamsWholeFileInSegments(t *testing.T) {
	p := setupProxyFixture(t, 3500)
	resp, body := p.get(t, http.MethodGet, "")
	if resp.StatusCode != 200 || !bytes.Equal(body, p.cdn.data) || resp.Header.Get("Content-Length") != "3500" {
		t.Fatalf("整文件中转错误：%d len=%d", resp.StatusCode, len(body))
	}
	if resp.Header.Get("Location") != "" {
		t.Fatal("中转模式不该回 302")
	}
	want := []string{"bytes=0-999", "bytes=1000-1999", "bytes=2000-2999", "bytes=3000-3499"}
	if strings.Join(p.cdn.ranges, ",") != strings.Join(want, ",") {
		t.Fatalf("分段不对：%v", p.cdn.ranges)
	}
	for i, ua := range p.cdn.uas {
		if ua != ua115Download || p.cdn.cookies[i] != "acw=1" {
			t.Fatalf("取流没带固定 UA / Cookie：%q %q", ua, p.cdn.cookies[i])
		}
	}
	if p.fetches.Load() != 1 {
		t.Fatalf("同一条直链应当复用：取了 %d 次", p.fetches.Load())
	}
}

func TestProxyClientRangeAndHead(t *testing.T) {
	p := setupProxyFixture(t, 3500)
	resp, body := p.get(t, http.MethodGet, "bytes=1500-2600")
	if resp.StatusCode != 206 || !bytes.Equal(body, p.cdn.data[1500:2601]) ||
		resp.Header.Get("Content-Range") != "bytes 1500-2600/3500" || resp.Header.Get("Content-Length") != "1101" {
		t.Fatalf("Range 中转错误：%d %q len=%d", resp.StatusCode, resp.Header.Get("Content-Range"), len(body))
	}
	resp, body = p.get(t, http.MethodHead, "")
	if resp.StatusCode != 200 || len(body) != 0 || resp.Header.Get("Content-Length") != "3500" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD 错误：%d %v", resp.StatusCode, resp.Header)
	}
	if last := p.cdn.ranges[len(p.cdn.ranges)-1]; last != "bytes=0-0" {
		t.Fatalf("HEAD 应当只读 1 个字节认总大小：%s", last)
	}
	resp, _ = p.get(t, http.MethodGet, "bytes=9000-")
	if resp.StatusCode != 416 {
		t.Fatalf("超出范围应当 416：%d", resp.StatusCode)
	}
	resp, body = p.get(t, http.MethodGet, "bytes=-100")
	if resp.StatusCode != 206 || !bytes.Equal(body, p.cdn.data[3400:]) {
		t.Fatalf("后缀 Range 原样透传错误：%d len=%d", resp.StatusCode, len(body))
	}
}

func TestProxyRefreshesLinkOnForbidden(t *testing.T) {
	p := setupProxyFixture(t, 2500)
	p.cdn.fail[1] = http.StatusForbidden
	p.cdn.fail[3] = http.StatusGone // 410 不在换链名单里：换了也一样，直接失败
	resp, body := p.get(t, http.MethodGet, "bytes=0-999")
	if resp.StatusCode != 206 || !bytes.Equal(body, p.cdn.data[:1000]) || p.fetches.Load() != 2 {
		t.Fatalf("403 后应当换链重试：%d fetches=%d", resp.StatusCode, p.fetches.Load())
	}
	resp, _ = p.get(t, http.MethodGet, "bytes=0-999")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("410 应当直接报错：%d", resp.StatusCode)
	}
}

func TestProxyGivesUpAfterTwoRefreshes(t *testing.T) {
	p := setupProxyFixture(t, 2500)
	for i := 1; i <= 5; i++ {
		p.cdn.fail[i] = http.StatusInternalServerError
	}
	resp, _ := p.get(t, http.MethodGet, "")
	if resp.StatusCode != http.StatusBadGateway || p.cdn.hits.Load() != 3 {
		t.Fatalf("连续失败最多换 2 次链：%d hits=%d", resp.StatusCode, p.cdn.hits.Load())
	}
}

func TestProxyResumesAfterMidSegmentCut(t *testing.T) {
	p := setupProxyFixture(t, 2500)
	p.cdn.cutAt[2] = 300 // 第二段写了 300 字节就断
	resp, body := p.get(t, http.MethodGet, "")
	if resp.StatusCode != 200 || !bytes.Equal(body, p.cdn.data) {
		t.Fatalf("中途断开后续传的数据不对：len=%d", len(body))
	}
	if p.cdn.ranges[2] != "bytes=1300-1999" {
		t.Fatalf("续传应当从断开的位置接着拉：%v", p.cdn.ranges)
	}
}

func TestProxyAbortsWhenResumeDoesNotMatch(t *testing.T) {
	p := setupProxyFixture(t, 2500)
	p.cdn.total[2] = 9999 // 第二段谎报总大小：拼起来会错位，宁可断开
	_, body := p.get(t, http.MethodGet, "")
	if len(body) != 1000 || !bytes.Equal(body, p.cdn.data[:1000]) {
		t.Fatalf("对不上的续传应当断开：len=%d", len(body))
	}
}

func TestProxyUpstreamIgnoresRange(t *testing.T) {
	p := setupProxyFixture(t, 2500)
	p.cdn.ignore = true
	resp, body := p.get(t, http.MethodGet, "")
	if resp.StatusCode != 200 || !bytes.Equal(body, p.cdn.data) {
		t.Fatalf("上游不认 Range 时从头整个转发：%d len=%d", resp.StatusCode, len(body))
	}
	if resp, _ := p.get(t, http.MethodGet, "bytes=10-20"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("上游不认 Range 又要中间一段应当报错：%d", resp.StatusCode)
	}
}

func TestProxyCoolsDownOn429(t *testing.T) {
	p := setupProxyFixture(t, 1500)
	p.cdn.fail[1] = http.StatusTooManyRequests
	start := time.Now()
	resp, body := p.get(t, http.MethodGet, "bytes=0-99")
	if resp.StatusCode != 206 || !bytes.Equal(body, p.cdn.data[:100]) {
		t.Fatalf("429 冷却后应当重试成功：%d", resp.StatusCode)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("没按 Retry-After 冷却就重试了")
	}
}

func TestProxyAcctLimiter(t *testing.T) {
	l := &proxyAcctLimiter{active: map[uint]int{}, cool: map[uint]time.Time{}, now: time.Now}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := l.acquire(ctx, 0, 2, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.acquire(ctx, 0, 2, 100*time.Millisecond); err != errProxyFull {
		t.Fatalf("满了应当报已满：%v", err)
	}
	if err := l.acquire(ctx, 7, 2, 0); err != nil {
		t.Fatal("别的账号不受影响")
	}
	go func() { time.Sleep(50 * time.Millisecond); l.release(0) }()
	if err := l.acquire(ctx, 0, 2, time.Second); err != nil {
		t.Fatal("有人放了名额应当拿得到")
	}
	l.coolDown(7, time.Hour)
	if err := l.acquire(ctx, 7, 9, 50*time.Millisecond); err != errProxyFull {
		t.Fatal("冷却中不发新连接")
	}
	if s := l.snapshot(); len(s) != 2 || s[0].Active != 2 || s[1].CoolLeft == 0 {
		t.Fatalf("状态快照不对：%+v", s)
	}
}

func TestProxyFullReturns503(t *testing.T) {
	p := setupProxyFixture(t, 1500)
	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeProxy, MaxConns: 1})
	if err := proxyLimiter.acquire(context.Background(), 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	old := proxyAcquireWait
	proxyAcquireWait = 100 * time.Millisecond
	t.Cleanup(func() { proxyAcquireWait = old })
	resp, _ := p.get(t, http.MethodGet, "")
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("名额满了应当 503：%d", resp.StatusCode)
	}
}

// 直链 c=1：同一条链接同时只能开一个，第二个并发的段要另取一条
func TestProxyLinkOpenLimit(t *testing.T) {
	pool := newProxyLinkPool(nil)
	var n atomic.Int64
	pool.fetch = func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		return fmt.Sprintf("https://cdn.example/%s?c=1&n=%d", pc, n.Add(1)), nil, nil
	}
	ctx := context.Background()
	a, err := pool.acquire(ctx, nil, nil, "pc")
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.acquire(ctx, nil, nil, "pc")
	if err != nil || a == b || n.Load() != 2 {
		t.Fatalf("c=1 时第二个连接应当另取一条：same=%v fetches=%d", a == b, n.Load())
	}
	pool.release(a)
	c, _ := pool.acquire(ctx, nil, nil, "pc")
	if c != a || n.Load() != 2 {
		t.Fatal("放回的名额应当复用原来那条")
	}
	if !pool.invalidate(a) || pool.invalidate(a) {
		t.Fatal("同一条直链只作废一次")
	}
}

func TestPlaybackRedirectByDefaultUnderProxyCode(t *testing.T) {
	p := setupProxyFixture(t, 1500)
	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeRedirect})
	old := playbackLinks
	playbackLinks = newPlaybackLinkResolver(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		return p.cdn.srv.URL + "/" + pc, map[string]string{"User-Agent": ua}, nil
	})
	t.Cleanup(func() { playbackLinks = old })
	resp, _ := p.get(t, http.MethodGet, "")
	if resp.StatusCode != 302 || p.cdn.hits.Load() != 0 {
		t.Fatalf("默认 302 不该经过本站转发：%d hits=%d", resp.StatusCode, p.cdn.hits.Load())
	}
}

// 经本站 Emby 反代：按核对过的 Emby 用户分 302 / 中转；中转用户开了转码时转码请求交给 Emby
func TestPlaybackPerEmbyUserProxy(t *testing.T) {
	f := setupPlaybackFixture(t)
	cdn := newFakeCDN(t, 1200)
	oldSeg, oldLinks, oldLim := proxySegSize, proxyLinks, proxyLimiter
	proxySegSize = 500
	proxyLinks = newProxyLinkPool(func(_ *gorm.DB, _ *config.Config, pc, ua string) (string, map[string]string, error) {
		return cdn.srv.URL + "/" + pc, map[string]string{"User-Agent": ua}, nil
	})
	proxyLimiter = &proxyAcctLimiter{active: map[uint]int{}, cool: map[uint]time.Time{}, now: time.Now}
	t.Cleanup(func() { proxySegSize, proxyLinks, proxyLimiter = oldSeg, oldLinks, oldLim })

	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeRedirect, Users: map[string]string{"other-user": playModeProxy}})
	resp := playbackRequest(t, f, http.MethodGet, "/Videos/one/stream?MediaSourceId=mediasource_b", "Player", "viewer")
	if resp.StatusCode != 302 {
		t.Fatalf("没单独设置的用户应当跟随默认 302：%d", resp.StatusCode)
	}

	forgetPlaybackCfg()
	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeRedirect, Users: map[string]string{"viewer-id": playModeProxy}})
	resp = playbackRequest(t, f, http.MethodGet, "/Videos/one/stream?MediaSourceId=mediasource_b", "Player", "viewer")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || !bytes.Equal(body, cdn.data[10:20]) || resp.Header.Get("Location") != "" {
		t.Fatalf("单独设了中转的用户应当本站转发：%d len=%d", resp.StatusCode, len(body))
	}
	if at := embyViaProxyAt("viewer-id"); at.IsZero() {
		t.Fatal("应当记下这个用户经本站反代播放过")
	}

	// 转码请求：没开转码照旧 415；开了交给 Emby
	if resp := playbackRequest(t, f, http.MethodGet, "/Videos/one/master.m3u8?MediaSourceId=mediasource_b", "Player", "viewer"); resp.StatusCode != 415 {
		t.Fatalf("没开转码应当 415：%d", resp.StatusCode)
	}
	forgetPlaybackCfg()
	setPlaybackCfgForTest(t, playbackCfg{Mode: playModeRedirect, Users: map[string]string{"viewer-id": playModeProxy}, Transcode: true})
	before := f.embyMedia.Load()
	resp = playbackRequest(t, f, http.MethodGet, "/Videos/one/master.m3u8?MediaSourceId=mediasource_b", "Player", "viewer")
	if resp.StatusCode != 200 || f.embyMedia.Load() != before+1 {
		t.Fatalf("开了转码应当交给 Emby：%d", resp.StatusCode)
	}
}

func TestEmbyWantsTranscode(t *testing.T) {
	if !embyWantsTranscode(map[string]interface{}{"SupportsDirectPlay": false, "SupportsDirectStream": false, "TranscodingUrl": "/x.m3u8"}) {
		t.Fatal("两种直接方式都不行且给了转码地址 = 要转码")
	}
	if embyWantsTranscode(map[string]interface{}{"SupportsDirectStream": true, "TranscodingUrl": "/x.m3u8"}) {
		t.Fatal("能直接串流的不算")
	}
	if embyWantsTranscode(map[string]interface{}{}) {
		t.Fatal("没给转码地址的不算")
	}
}
