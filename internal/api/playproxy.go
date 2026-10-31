package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 本机中转播放 ====================
//
// 设计见 docs/115-station-notes/PROXY-PLAY-PLAN.md §4。做法参考 LitePan internal/playback（只读）：
// 上游按 10 MB 分段拉、失效换链（同一组状态码、连续最多 2 次）、同账号共享连接名额、被限流后冷却；
// 直链参数 c（同时打开次数）出自 p115client download_url 的文档。
// 只在用户自己选了中转的播放上走这里，302 失败不许退化到这里。

const (
	proxyRefreshMax   = 2 // 连续失败最多换几次链（LitePan refreshLeft）
	proxyCoolDefault  = 30 * time.Second
	proxyLinksPerFile = 4 // 同一个文件最多同时挂几条直链（c 满了另取一条）
	proxyCopyBuf      = 256 << 10
)

// proxySegSize 上游每段多大：LitePan 115 驱动 downloadPartSize 同款 10 MB（变量只为测试能调小）
var proxySegSize int64 = 10 << 20

// proxyAcquireWait 等名额 / 等直链空位最多多久（LitePan acquireTimeout 同款；变量只为测试能调小）
var proxyAcquireWait = 10 * time.Second

// proxyUpstream 拉 CDN 用的客户端：不走全局代理（视频流量绕代理只会更慢），只限等响应头的时间，
// 整段传输不设总超时（一段 10 MB 在慢网下也可能要十几秒）
var proxyUpstream = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
		ForceAttemptHTTP2:     false, // CDN 按连接限速时 HTTP/2 多路复用反而挤在一条连接上
	},
	// CDN 一般不再跳；真跳了也只跟着走，同源才保留请求头
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("跳转太多")
		}
		return nil
	},
}

// ---- 直链池：按 pickcode 挂几条固定 UA 的直链 ----

type proxyLink struct {
	url     string
	headers map[string]string
	exp     time.Time
	limit   int // 直链参数 c：允许同时打开次数，0 = 不限
	active  int
	dead    bool
}

type proxyLinkFlight struct {
	done chan struct{}
	err  error
}

type proxyLinkPool struct {
	mu      sync.Mutex
	links   map[string][]*proxyLink
	flights map[string]*proxyLinkFlight
	fetch   func(*gorm.DB, *config.Config, string, string) (string, map[string]string, error)
	now     func() time.Time
}

func newProxyLinkPool(fetch func(*gorm.DB, *config.Config, string, string) (string, map[string]string, error)) *proxyLinkPool {
	return &proxyLinkPool{links: map[string][]*proxyLink{}, flights: map[string]*proxyLinkFlight{}, fetch: fetch, now: time.Now}
}

var proxyLinks = newProxyLinkPool(proxyDownloadURLFull)

// pruneLocked 去掉作废与快过期的（快过期的不再发给新的段，在拉的段照常拉完）
func (p *proxyLinkPool) pruneLocked(pc string) []*proxyLink {
	now := p.now()
	kept := p.links[pc][:0]
	for _, l := range p.links[pc] {
		if !l.dead && now.Before(l.exp) {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		delete(p.links, pc)
		return nil
	}
	p.links[pc] = kept
	return kept
}

// usable 有没有能立刻用的直链（不取链；用于判断这次要不要扣取链限流额度）
func (p *proxyLinkPool) usable(pc string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.pruneLocked(pc) {
		if l.limit == 0 || l.active < l.limit {
			return true
		}
	}
	return false
}

// acquire 拿一条还有空位的直链（active+1）；都满了且没到上限就另取一条，到了上限就等
func (p *proxyLinkPool) acquire(ctx context.Context, db *gorm.DB, cfg *config.Config, pc string) (*proxyLink, error) {
	deadline := p.now().Add(proxyAcquireWait)
	for {
		p.mu.Lock()
		links := p.pruneLocked(pc)
		for _, l := range links {
			if l.limit == 0 || l.active < l.limit {
				l.active++
				p.mu.Unlock()
				return l, nil
			}
		}
		if len(links) < proxyLinksPerFile {
			f, ok := p.flights[pc]
			if !ok {
				f = &proxyLinkFlight{done: make(chan struct{})}
				p.flights[pc] = f
				go p.run(db, cfg, pc, f)
			}
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.done:
			}
			if f.err != nil {
				return nil, f.err
			}
			continue
		}
		p.mu.Unlock()
		if p.now().After(deadline) {
			return nil, fmt.Errorf("这个文件的直链同时打开次数用满了")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *proxyLinkPool) run(db *gorm.DB, cfg *config.Config, pc string, f *proxyLinkFlight) {
	// 中转一律用固定 UA 取链（qmediasync 同款）：几个人看同一部共用一条，取流也是本站同一个 UA，
	// 不会出现 302 时「换链与取流 UA 不一致、CDN 反复 403」把文件打进 115 临时限流（openStrm redirect.ts 记过）
	u, headers, err := p.fetch(db, cfg, pc, ua115Download)
	var link *proxyLink
	if err == nil {
		link, err = parseProxyLink(u, headers, p.now())
	}
	p.mu.Lock()
	if err == nil {
		p.links[pc] = append(p.links[pc], link)
		vlog("[中转] ✓ 取到直链（%s，同时打开次数 c=%d，有效到 %s）", proxyLinkHost(u), link.limit, link.exp.Format("15:04:05"))
	}
	f.err = err
	delete(p.flights, pc)
	if len(p.links) > 4096 { // 有界：公开入口不能无限堆积
		clear(p.links)
	}
	p.mu.Unlock()
	close(f.done)
}

func (p *proxyLinkPool) release(l *proxyLink) {
	p.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	p.mu.Unlock()
}

// invalidate 作废这条直链；返回是不是这次调用作废的。几个段同时在同一条链上失败，只算一次
// （LitePan refreshAfterFailure：并发的 Range 别各自重复刷新），后来的直接去拿新的
func (p *proxyLinkPool) invalidate(l *proxyLink) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.dead {
		return false
	}
	l.dead = true
	return true
}

// parseProxyLink 中转用的直链校验。和 302 的 validatePlaybackLink 不同：要 Cookie 的链接（f=3）也能用，
// 本站取流时自己带上；UA 是本站固定的那个
func parseProxyLink(raw string, headers map[string]string, now time.Time) (*proxyLink, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return nil, fmt.Errorf("下载地址无效")
	}
	exp := now.Add(30 * time.Minute)
	if stamp, err := strconv.ParseInt(u.Query().Get("t"), 10, 64); err == nil {
		if d := time.Unix(stamp, 0).Add(-5 * time.Minute); d.Before(exp) {
			exp = d
		}
	}
	if !exp.After(now) {
		return nil, fmt.Errorf("下载地址已过期或即将过期")
	}
	limit, _ := strconv.Atoi(u.Query().Get("c"))
	if limit < 0 {
		limit = 0
	}
	h := map[string]string{}
	for k, v := range headers {
		if v != "" && !strings.ContainsAny(k+v, "\r\n") {
			h[k] = v
		}
	}
	if _, ok := headerFold(h, "User-Agent"); !ok {
		h["User-Agent"] = ua115Download
	}
	return &proxyLink{url: raw, headers: h, exp: exp, limit: limit}, nil
}

func headerFold(h map[string]string, key string) (string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// proxyLinkHost 日志里只记 CDN 主机名：直链带签名，不进日志（LitePan logAction 同口径）
func proxyLinkHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return "?"
}

// ---- 每个 115 账号的连接名额与冷却（LitePan account_range_limiter.go 的思路） ----

type proxyAcctLimiter struct {
	mu     sync.Mutex
	active map[uint]int
	cool   map[uint]time.Time
	now    func() time.Time
}

var proxyLimiter = &proxyAcctLimiter{active: map[uint]int{}, cool: map[uint]time.Time{}, now: time.Now}

var errProxyFull = errors.New("中转连接已满")

// acquire 占一个名额。满了或在冷却就等，最多 wait；等不到返回 errProxyFull。
// 不无限等：拖动进度条时播放器连发几个 Range，旧连接还没关完时无限等会把起播整个卡死
func (l *proxyAcctLimiter) acquire(ctx context.Context, acct uint, max int, wait time.Duration) error {
	deadline := l.now().Add(wait)
	for {
		l.mu.Lock()
		now := l.now()
		if now.After(l.cool[acct]) && l.active[acct] < max {
			l.active[acct]++
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		if now.After(deadline) {
			return errProxyFull
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (l *proxyAcctLimiter) release(acct uint) {
	l.mu.Lock()
	if l.active[acct] > 0 {
		l.active[acct]--
	}
	if l.active[acct] == 0 {
		delete(l.active, acct)
	}
	l.mu.Unlock()
}

// coolDown 被 115 限流（429 / 503）后这个账号暂停一会儿：新连接等着，在传的这条重试前也等
func (l *proxyAcctLimiter) coolDown(acct uint, d time.Duration) {
	l.mu.Lock()
	until := l.now().Add(d)
	if until.After(l.cool[acct]) {
		l.cool[acct] = until
	}
	l.mu.Unlock()
}

func (l *proxyAcctLimiter) coolLeft(acct uint) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cool[acct].Sub(l.now())
}

type proxyAcctState struct {
	Acct     uint `json:"acct"`
	Active   int  `json:"active"`
	CoolLeft int  `json:"cool_left"` // 秒，0 = 没在冷却
}

func (l *proxyAcctLimiter) snapshot() []proxyAcctState {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	seen := map[uint]bool{}
	var out []proxyAcctState
	for a, n := range l.active {
		seen[a] = true
		out = append(out, proxyAcctState{Acct: a, Active: n})
	}
	for a, until := range l.cool {
		if !until.After(now) {
			delete(l.cool, a)
			continue
		}
		if !seen[a] {
			out = append(out, proxyAcctState{Acct: a})
		}
		for i := range out {
			if out[i].Acct == a {
				out[i].CoolLeft = int(until.Sub(now).Seconds() + 0.999)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Acct < out[j].Acct })
	return out
}

// retryAfterOf 上游的 Retry-After（秒数）；没有或读不懂按默认 30 秒，最多 5 分钟
func retryAfterOf(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		if n > 300 {
			n = 300
		}
		return time.Duration(n) * time.Second
	}
	return proxyCoolDefault
}

// shouldRefreshStatus 只对直链过期或上游短暂异常换链，其余 4xx 换了也一样（LitePan shouldRefreshUpstreamStatus 同一组）
func shouldRefreshStatus(s int) bool {
	return s == http.StatusUnauthorized || s == http.StatusForbidden || s == http.StatusNotFound ||
		s == http.StatusRequestTimeout || s == http.StatusTooManyRequests || s >= http.StatusInternalServerError
}

// ---- 在传的会话（状态接口、日志） ----

type proxySession struct {
	id     uint64
	who    string // Emby 用户名；/d/ 入口认不出人，写来源 IP
	ip     string
	ua     string
	pc     string
	name   string
	acct   uint
	start  time.Time
	bytes  atomic.Int64
	sample struct {
		at    time.Time
		bytes int64
		rate  float64 // 字节 / 秒
	}
}

var proxySessions = struct {
	sync.Mutex
	m        map[uint64]*proxySession
	seq      uint64
	day      string
	dayBytes int64                // 今天已结束的连接传的字节
	recent   map[string]time.Time // 「谁 + 哪部」上一次连接结束的时间：播放器拖动会重连，同一次观看只记一行起播日志
}{m: map[uint64]*proxySession{}, recent: map[string]time.Time{}}

func proxySessionStart(s *proxySession) (first bool) {
	proxySessions.Lock()
	defer proxySessions.Unlock()
	proxySessions.seq++
	s.id = proxySessions.seq
	s.sample.at = s.start
	key := s.who + "|" + s.pc
	at, seen := proxySessions.recent[key]
	first = !seen || time.Since(at) > 5*time.Minute
	for _, o := range proxySessions.m {
		if o.who == s.who && o.pc == s.pc {
			first = false
		}
	}
	proxySessions.m[s.id] = s
	return first
}

func proxySessionEnd(s *proxySession) {
	proxySessions.Lock()
	defer proxySessions.Unlock()
	delete(proxySessions.m, s.id)
	today := time.Now().Format("2006-01-02")
	if proxySessions.day != today {
		proxySessions.day, proxySessions.dayBytes = today, 0
	}
	proxySessions.dayBytes += s.bytes.Load()
	if len(proxySessions.recent) > 1024 {
		clear(proxySessions.recent)
	}
	proxySessions.recent[s.who+"|"+s.pc] = time.Now()
}

// ---- 入口 ----

// servePickcodeProxy 本机中转一个文件。who 是认得出的 Emby 用户名（/d/ 入口传空）
func servePickcodeProxy(c *gin.Context, db *gorm.DB, cfg *config.Config, id, who string) {
	pc, err := playbackPickcode(db, id)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	// 分享 STRM 的中转还没接（SHARE-STRM-PLAN.md §6.3 阶段 3）：明确报错，不悄悄改成 302 或别的通道
	if isSharePlayID(pc) {
		plogShareStrm.Printf("✗ %s 选的是本机中转，分享 STRM 还不支持中转（%s）", firstNonEmpty(who, c.ClientIP()), pc)
		c.String(http.StatusNotImplemented, "分享 STRM 暂不支持本机中转：到「Strm 管理 → 配置 → 播放方式」把这个用户改成 302 直连")
		return
	}
	// 和 302 同一道取链限流：只在要真去 115 取链时扣（池里有能用的直链就不扣）
	if !proxyLinks.usable(pc) && !proxyRateAllow(c.ClientIP()) {
		c.String(http.StatusTooManyRequests, "too many requests")
		return
	}
	pcfg := loadPlaybackCfg()
	acct, _ := decodePlayID(pc)
	ctx := c.Request.Context()
	if err := proxyLimiter.acquire(ctx, acct, pcfg.MaxConns, proxyAcquireWait); err != nil {
		if errors.Is(err, errProxyFull) {
			log.Printf("[中转] ✗ %s 中转连接已满（%d/%d），这次播放没开始；要同时看的人多就调高「同时连接上限」",
				proxyAcctLabel(db, acct), pcfg.MaxConns, pcfg.MaxConns)
			c.Header("Retry-After", "5")
			c.String(http.StatusServiceUnavailable, "中转连接已满，请稍后再试")
		}
		return
	}
	defer proxyLimiter.release(acct)

	if who == "" {
		who = c.ClientIP()
	}
	s := &proxySession{who: who, ip: c.ClientIP(), ua: c.Request.UserAgent(), pc: pc, acct: acct,
		name: proxyFileName(db, c, pc), start: time.Now()}
	if proxySessionStart(s) {
		log.Printf("[中转] ▶ %s 开始看 %s（%s）", s.who, s.name, proxyAcctLabel(db, acct))
	}
	st := &proxyStream{ctx: ctx, db: db, cfg: cfg, pc: pc, acct: acct, w: c.Writer, sess: s, refreshLeft: proxyRefreshMax}
	reason := st.serve(c.Request)
	proxySessionEnd(s)
	n := s.bytes.Load()
	msg := fmt.Sprintf("[中转] ■ %s · %s 传了 %s，用时 %s，%s", s.who, s.name, humanBytes(int(n)),
		time.Since(s.start).Round(time.Second), reason)
	if n >= 64<<20 || strings.HasPrefix(reason, "上游") {
		log.Print(msg)
	} else {
		vlog("%s", msg)
	}
}

// proxyFileName 状态与日志里显示的文件名：/d/ 地址里带的原名，没有就查台账，再没有写 pickcode
func proxyFileName(db *gorm.DB, c *gin.Context, pc string) string {
	if fn := strings.Trim(c.Param("filename"), "/"); fn != "" {
		return path.Base(fn)
	}
	if q := c.Request.URL.RawQuery; strings.HasPrefix(q, "/") {
		if v, err := url.PathUnescape(q); err == nil {
			return path.Base(v)
		}
	}
	if db != nil {
		_, raw := decodePlayID(pc)
		var sf model.SyncedFile
		if db.Select("rel_path").Where("pick_code = ?", raw).First(&sf).Error == nil && sf.RelPath != "" {
			return strings.TrimSuffix(path.Base(sf.RelPath), ".strm")
		}
	}
	return pc
}

func proxyAcctLabel(db *gorm.DB, acct uint) string {
	if acct == acctMain {
		return "主号"
	}
	if db != nil {
		var s model.Storage
		if db.Select("name").Where("id = ?", acct).First(&s).Error == nil && s.Name != "" {
			return s.Name
		}
	}
	return fmt.Sprintf("账号 %d", acct)
}

// ---- 转发 ----

type proxyStream struct {
	ctx         context.Context
	db          *gorm.DB
	cfg         *config.Config
	pc          string
	acct        uint
	w           gin.ResponseWriter
	sess        *proxySession
	refreshLeft int
}

// clientRange 客户端的 Range。只认单段的 bytes=a- / bytes=a-b，其余（后缀、多段、写错的）原样透传一次请求
type clientRange struct {
	set   bool
	start int64
	end   int64 // -1 = 到结尾
}

func parseClientRange(v string) (clientRange, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return clientRange{end: -1}, true
	}
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return clientRange{}, false
	}
	a, b, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok || a == "" {
		return clientRange{}, false
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 {
		return clientRange{}, false
	}
	r := clientRange{set: true, start: start, end: -1}
	if b != "" {
		end, err := strconv.ParseInt(b, 10, 64)
		if err != nil || end < start {
			return clientRange{}, false
		}
		r.end = end
	}
	return r, true
}

// parseContentRange "bytes a-b/total"；total 为 * 时返回 -1
func parseContentRange(v string) (start, end, total int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	rng, tot, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err1, err2 error
	start, err1 = strconv.ParseInt(a, 10, 64)
	end, err2 = strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || end < start {
		return 0, 0, 0, false
	}
	total = -1
	if tot != "*" {
		t, err := strconv.ParseInt(tot, 10, 64)
		if err != nil {
			return 0, 0, 0, false
		}
		total = t
	}
	return start, end, total, true
}

// fetch 向 CDN 发一次请求（rangeHeader 为空表示不带 Range）。遇到该换链的状态就换链重试，
// 连续最多 refreshLeft 次；被限流时这个账号冷却，重试前也等冷却结束（最多 10 秒）
func (s *proxyStream) fetch(method, rangeHeader string) (*http.Response, *proxyLink, error) {
	for {
		if wait := proxyLimiter.coolLeft(s.acct); wait > 0 {
			if wait > proxyAcquireWait {
				return nil, nil, fmt.Errorf("上游限流，账号冷却中（还剩 %s）", wait.Round(time.Second))
			}
			select {
			case <-s.ctx.Done():
				return nil, nil, s.ctx.Err()
			case <-time.After(wait):
			}
		}
		link, err := proxyLinks.acquire(s.ctx, s.db, s.cfg, s.pc)
		if err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(s.ctx, method, link.url, nil)
		if err != nil {
			proxyLinks.release(link)
			return nil, nil, err
		}
		for k, v := range link.headers {
			req.Header.Set(k, v)
		}
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		resp, err := proxyUpstream.Do(req)
		status := 0
		if err == nil {
			if !shouldRefreshStatus(resp.StatusCode) {
				return resp, link, nil
			}
			status = resp.StatusCode
			if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
				proxyLimiter.coolDown(s.acct, retryAfterOf(resp.Header.Get("Retry-After")))
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
		}
		proxyLinks.release(link)
		if s.ctx.Err() != nil {
			return nil, nil, s.ctx.Err()
		}
		if s.refreshLeft <= 0 {
			if err != nil {
				return nil, nil, fmt.Errorf("上游连接失败: %v", err)
			}
			return nil, nil, fmt.Errorf("上游返回 HTTP %d", status)
		}
		s.refreshLeft--
		if proxyLinks.invalidate(link) {
			if err != nil {
				log.Printf("[中转] ○ 连 CDN 失败（%s），换一条直链重试: %v", proxyLinkHost(link.url), err)
			} else {
				log.Printf("[中转] ○ CDN 返回 HTTP %d（%s），换一条直链重试", status, proxyLinkHost(link.url))
			}
		}
	}
}

func segRange(from, to int64) string {
	return "bytes=" + strconv.FormatInt(from, 10) + "-" + strconv.FormatInt(to, 10)
}

// serve 处理一个客户端请求，返回结束原因（写进日志）
func (s *proxyStream) serve(r *http.Request) string {
	cr, ok := parseClientRange(r.Header.Get("Range"))
	if !ok {
		return s.passthrough(r)
	}
	head := r.Method == http.MethodHead
	// 第一段：HEAD 只要 1 个字节认出总大小（115 CDN 对 HEAD 的支持没核实过，LitePan probeSizeViaRange0 同款）
	first := cr.start + proxySegSize - 1
	if head {
		first = cr.start
	}
	if cr.end >= 0 && first > cr.end {
		first = cr.end
	}
	resp, link, err := s.fetch(http.MethodGet, segRange(cr.start, first))
	if err != nil {
		if s.ctx.Err() != nil {
			return "客户端断开"
		}
		s.w.Header().Set("Cache-Control", "no-store")
		http.Error(s.w, "中转取流失败："+err.Error(), http.StatusBadGateway)
		return err.Error()
	}
	// body 换段时会换成新的；一段读完就还直链名额（c 按段算），不攥到整个请求结束
	var body io.ReadCloser = &linkBody{ReadCloser: resp.Body, link: link}
	defer func() { body.Close() }()

	h := s.w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	for _, k := range []string{"Content-Type", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		if v := resp.Header.Get("Content-Range"); v != "" {
			h.Set("Content-Range", v)
		}
		s.w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return "请求的范围超出文件"
	case http.StatusOK:
		// 上游不认 Range：只在客户端要的就是从头整个文件时原样转发
		if cr.start != 0 || cr.end >= 0 {
			http.Error(s.w, "上游不支持分段读取", http.StatusBadGateway)
			return "上游不支持分段读取"
		}
		if v := resp.Header.Get("Content-Length"); v != "" {
			h.Set("Content-Length", v)
		}
		s.w.WriteHeader(http.StatusOK)
		if head {
			return "HEAD"
		}
		_, reason := s.copy(body, -1)
		return reason
	default:
		http.Error(s.w, fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
	}
	a, b, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || a != cr.start || total < 0 {
		http.Error(s.w, "上游分段响应不对", http.StatusBadGateway)
		return "上游分段响应不对"
	}
	end := total - 1
	if cr.end >= 0 && cr.end < end {
		end = cr.end
	}
	h.Set("Accept-Ranges", "bytes")
	if cr.set {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cr.start, end, total))
		h.Set("Content-Length", strconv.FormatInt(end-cr.start+1, 10))
		s.w.WriteHeader(http.StatusPartialContent)
	} else {
		h.Set("Content-Length", strconv.FormatInt(total, 10))
		s.w.WriteHeader(http.StatusOK)
	}
	if head {
		return "HEAD"
	}

	pos := cr.start
	segEnd := min(b, end)
	for {
		want := segEnd - pos + 1
		n, reason := s.copy(body, want)
		pos += n
		if reason == "客户端断开" {
			return reason
		}
		body.Close()
		if n < want {
			// 段中途断了：从断开的位置接着拉（LitePan remote_reader 同款），起点与总大小必须对得上。
			// 一个字节都没拉到的断开算一次失败，否则上游一直秒断会在这里转圈
			if n == 0 {
				if s.refreshLeft <= 0 {
					return "上游中断"
				}
				s.refreshLeft--
			}
		} else {
			s.refreshLeft = proxyRefreshMax // 一段顺利拉完，「连续失败」从头数
			if pos > end {
				return "播完"
			}
			segEnd = min(pos+proxySegSize-1, end)
		}
		nb, err := s.resume(pos, segEnd, total)
		if err != nil {
			body = io.NopCloser(strings.NewReader(""))
			return err.Error()
		}
		body = nb
	}
}

// resume 拉 [from, to] 这一段。拿到的必须是 206、起点对得上、总大小没变，否则放弃：
// 宁可让播放器自己重连，也不能拼出错位的数据
func (s *proxyStream) resume(from, to, total int64) (io.ReadCloser, error) {
	resp, link, err := s.fetch(http.MethodGet, segRange(from, to))
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, errors.New("客户端断开")
		}
		return nil, fmt.Errorf("上游续传失败: %v", err)
	}
	a, _, t, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode != http.StatusPartialContent || !ok || a != from || t != total {
		resp.Body.Close()
		proxyLinks.release(link)
		return nil, fmt.Errorf("上游续传响应对不上（HTTP %d %q）", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	return &linkBody{ReadCloser: resp.Body, link: link}, nil
}

// linkBody 读完一段时把直链名额还回去（c 按段算）
type linkBody struct {
	io.ReadCloser
	link *proxyLink
	once sync.Once
}

func (b *linkBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { proxyLinks.release(b.link) })
	return err
}

// copy 从上游读最多 want 个字节（-1 = 读到结束）写给客户端。返回实际写出的字节数与原因：
// 写不出去是客户端断开；读不够是上游断了（调用方续传）
func (s *proxyStream) copy(src io.Reader, want int64) (int64, string) {
	buf := make([]byte, proxyCopyBuf)
	var done int64
	for want < 0 || done < want {
		chunk := buf
		if want >= 0 && want-done < int64(len(chunk)) {
			chunk = chunk[:want-done]
		}
		n, rerr := src.Read(chunk)
		if n > 0 {
			if _, werr := s.w.Write(chunk[:n]); werr != nil {
				return done, "客户端断开"
			}
			done += int64(n)
			s.sess.bytes.Add(int64(n))
		}
		if rerr != nil {
			if s.ctx.Err() != nil {
				return done, "客户端断开"
			}
			if want < 0 && rerr == io.EOF {
				return done, "播完"
			}
			return done, "上游中断"
		}
	}
	return done, "播完"
}

// passthrough 后缀 / 多段 Range：原样转发一次请求（这类请求通常很小，比如读 mp4 尾部的 moov）
func (s *proxyStream) passthrough(r *http.Request) string {
	resp, link, err := s.fetch(r.Method, r.Header.Get("Range"))
	if err != nil {
		if s.ctx.Err() != nil {
			return "客户端断开"
		}
		http.Error(s.w, "中转取流失败："+err.Error(), http.StatusBadGateway)
		return err.Error()
	}
	defer func() { resp.Body.Close(); proxyLinks.release(link) }()
	h := s.w.Header()
	h.Set("Cache-Control", "no-store")
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	s.w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return "HEAD"
	}
	_, reason := s.copy(resp.Body, -1)
	return reason
}

// ---- 状态接口 ----

type proxyPlayView struct {
	Who   string  `json:"who"`
	Name  string  `json:"name"`
	Acct  string  `json:"acct"`
	UA    string  `json:"ua"`
	IP    string  `json:"ip"`
	Conns int     `json:"conns"`
	Bytes int64   `json:"bytes"`
	Rate  float64 `json:"rate"` // 字节 / 秒
	Start string  `json:"start"`
}

type proxyAcctView struct {
	Acct     uint   `json:"acct"`
	Name     string `json:"name"`
	Active   int    `json:"active"`
	CoolLeft int    `json:"cool_left"`
}

// PlaybackProxyStatus GET /playback/proxy-status：只看这台机器内存里的，零 115 请求
func (h *Handler) PlaybackProxyStatus(c *gin.Context) {
	now := time.Now()
	proxySessions.Lock()
	today := now.Format("2006-01-02")
	if proxySessions.day != today {
		proxySessions.day, proxySessions.dayBytes = today, 0
	}
	dayBytes := proxySessions.dayBytes
	groups := map[string]*proxyPlayView{}
	var order []string
	for _, s := range proxySessions.m {
		b := s.bytes.Load()
		dayBytes += b
		if dt := now.Sub(s.sample.at).Seconds(); dt >= 1 {
			s.sample.rate = float64(b-s.sample.bytes) / dt
			s.sample.at, s.sample.bytes = now, b
		}
		key := s.who + "|" + s.pc
		g, ok := groups[key]
		if !ok {
			g = &proxyPlayView{Who: s.who, Name: s.name, Acct: proxyAcctLabel(h.DB, s.acct), UA: s.ua, IP: s.ip,
				Start: s.start.Format(time.RFC3339)}
			groups[key] = g
			order = append(order, key)
		} else if s.start.Format(time.RFC3339) < g.Start {
			g.Start = s.start.Format(time.RFC3339)
		}
		g.Conns++
		g.Bytes += b
		g.Rate += s.sample.rate
	}
	proxySessions.Unlock()
	plays := make([]proxyPlayView, 0, len(order))
	var rate float64
	for _, k := range order {
		plays = append(plays, *groups[k])
		rate += groups[k].Rate
	}
	sort.Slice(plays, func(i, j int) bool { return plays[i].Start < plays[j].Start })
	accts := []proxyAcctView{}
	for _, a := range proxyLimiter.snapshot() {
		accts = append(accts, proxyAcctView{Acct: a.Acct, Name: proxyAcctLabel(h.DB, a.Acct), Active: a.Active, CoolLeft: a.CoolLeft})
	}
	cfg := loadPlaybackCfg()
	c.JSON(http.StatusOK, gin.H{
		"plays": plays, "accounts": accts, "rate": rate, "today_bytes": dayBytes,
		"max_conns": cfg.MaxConns, "any_proxy": cfg.anyProxy(),
	})
}
