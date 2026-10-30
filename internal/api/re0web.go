package api

// ==================== RE0 网页通道：浏览器里登录、签到、查资源、解锁 ====================
//
// 浏览器是 re0browser.go 下载的 CloakBrowser，用 go-rod 走 CDP 控制。页面上的做法（选择器、
// 签到按钮怎么认、资源列表从 Next.js 内嵌数据里怎么读、解锁后怎么拿分享链接）参考
// p115strmhelper `helper/hdhive/browser.py` 与 `utils/hdhive.py`（只读，Go 重写）。
//
// 与插件不同的一点：插件每次操作都开新上下文、重新登录一遍；这里浏览器常驻、用户数据目录持久化、
// 指纹种子固定，只有被跳回 /login 才登录。订阅每轮都要查资源，次次登录既慢又容易触发风控。
// 如果站点的会话绑定连这样都不认，表现就是每次都会走一遍登录，结果仍然正确，只是慢。
//
// 站点会校验点击的 isTrusted（插件注释），所以点按钮一律用 rod 的 Click（CDP 输入事件），
// 不在页面里 el.click()。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

const (
	re0BrowserIdle  = 10 * time.Minute // 浏览器空闲多久关掉（下次用再起，登录态在用户数据目录里）
	re0LoginPath    = "/login"
	re0AutoRetryGap = 30 * time.Minute // 下载失败后，被动入口多久之后才自动重试
)

// re0WebConfigured 配了网页登录（账号 + 密码）
func re0WebConfigured(cfg *re0Cfg) bool {
	return strings.TrimSpace(cfg.Username) != "" && cfg.Password != ""
}

// errRe0BrowserNotReady 浏览器还没下载好
var errRe0BrowserNotReady = errors.New("RE0 网页登录用的浏览器还没准备好")

// re0BrowserBlocked 浏览器不能用的原因（空 = 能用）。没装就顺手触发下载
func re0BrowserBlocked(reason string) string {
	st := cloakSnapshot()
	switch st.State {
	case "ready":
		return ""
	case "unsupported":
		return st.Error
	case "downloading", "verifying", "extracting":
		if st.Total > 0 {
			return fmt.Sprintf("浏览器下载中（%d%%），下载完才能用", st.Done*100/st.Total)
		}
		return "浏览器下载中，下载完才能用"
	}
	if st.State == "error" && time.Since(time.Unix(st.UpdatedAt, 0)) < re0AutoRetryGap {
		// 刚失败过：被动入口（打开来源列表、定时签到）别一失败就重下，断网时会一直重试刷日志。
		// 界面上的「下载浏览器」按钮不受这个限制
		return "浏览器下载失败：" + st.Error + "（可在「发现与订阅 → 设置 → RE0」重新下载）"
	}
	ensureCloak(reason)
	if st.State == "error" && st.Error != "" {
		return "浏览器下载失败：" + st.Error + "（已重新开始下载）"
	}
	return "浏览器还没下载，已开始下载（约 210MB），可在「发现与订阅 → 设置 → RE0」看进度"
}

// ==================== 浏览器进程 ====================

var (
	re0OpMu sync.Mutex // 同一时间只做一件事：页面操作互相不干扰，也免得两个任务同时登录

	re0BrMu       sync.Mutex
	re0BrLauncher *launcher.Launcher
	re0Br         *rod.Browser
	re0BrLastUse  time.Time
	re0BrJanitor  sync.Once
	re0BrProxy    string // 浏览器这次启动走的线路（「直连」/「代理 …」），过不了 Cloudflare 时写进日志
)

// re0StartBrowser 起浏览器（已经起着就复用）。调用方持有 re0OpMu
func re0StartBrowser() (*rod.Browser, error) {
	re0BrMu.Lock()
	defer re0BrMu.Unlock()
	re0BrLastUse = time.Now()
	if re0Br != nil {
		// 进程可能被杀了：探一下
		if _, err := (proto.BrowserGetVersion{}).Call(re0Br); err == nil {
			return re0Br, nil
		}
		log.Printf("[RE0浏览器] ○ 常驻浏览器已失联，重新启动")
		re0killLocked()
	}
	bin, ok := cloakInstalled()
	if !ok {
		return nil, errRe0BrowserNotReady
	}
	cfg := loadRe0Cfg()
	l := launcher.New().
		Bin(bin).
		Leakless(false). // leakless 会往临时目录释放一个守护程序；进程由我们自己管
		Headless(true).
		UserDataDir(cloakProfileDir()).
		NoSandbox(true).
		Delete("enable-automation"). // 暴露 navigator.webdriver（官方包装层同样去掉）
		Set("fingerprint", cloakSeed()).
		Set("fingerprint-platform", "windows").
		Set("disable-dev-shm-usage") // 容器里 /dev/shm 默认只有 64MB
	// rod 自带的一串默认参数里，关站点隔离（disable-site-isolation-trials / disable-features=site-per-process）
	// 和 NetworkServiceInProcess 都不是真浏览器会有的，一并去掉；与 2026-10-14 实测能过 Cloudflare 的组合一致。
	// 别加 --disable-gpu：没有 WebGL 是很明显的机器人特征
	for _, f := range []string{"disable-site-isolation-trials", "disable-features", "enable-features",
		"disable-background-networking", "disable-popup-blocking", "metrics-recording-only", "use-mock-keychain",
		"force-color-profile", "disable-ipc-flooding-protection", "disable-renderer-backgrounding", "no-startup-window",
		"disable-background-timer-throttling", "disable-backgrounding-occluded-windows", "disable-breakpad",
		"disable-client-side-phishing-detection", "disable-component-extensions-with-background-pages",
		"disable-default-apps", "disable-hang-monitor", "disable-prompt-on-repost", "disable-sync"} {
		l.Delete(flags.Flag(f))
	}
	if tz := os.Getenv("TZ"); tz != "" {
		l.Set("fingerprint-timezone", tz)
	}
	proxyNote := "直连"
	if cfg.UseProxy {
		if pu := getProxyURL(); pu != "" {
			if u, err := url.Parse(pu); err == nil && u.Host != "" {
				if u.User != nil {
					log.Printf("[RE0浏览器] ⚠ 代理带账号密码，Chromium 的 --proxy-server 不支持，去掉账号部分照用（不通的话请换不带认证的代理）")
				}
				l.Proxy(u.Scheme + "://" + u.Host)
				proxyNote = "代理 " + u.Scheme + "://" + u.Host
			}
		} else {
			log.Printf("[RE0浏览器] ○ 勾了「通过代理访问」但系统配置里没填代理，按直连")
		}
	}
	start := time.Now()
	re0Step("启动浏览器")
	log.Printf("[RE0浏览器] ▶ 启动 CloakBrowser %s（%s，指纹种子 %s，用户目录 %s）", cloakVersion(), proxyNote, cloakSeed(), cloakProfileDir())
	u, err := l.Launch()
	if err != nil {
		l.Kill()
		return nil, fmt.Errorf("浏览器启动失败: %v（缺系统库时请用官方 Docker 镜像）", err)
	}
	// NoDefaultDevice：rod 默认给每个页面套一个「笔记本」设备，会把 UA 改成 Mac 上的 Chrome 114、
	// 再改触控与屏幕方向，和 CloakBrowser 自己的 Windows / Chrome 146 指纹对不上，Cloudflare 一眼认出是机器人。
	// 2026-10-14 实测：带着它点验证框永远重置成未勾选，去掉后点一次就过
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("连接浏览器失败: %v", err)
	}
	if v, err := (proto.BrowserGetVersion{}).Call(b); err == nil {
		log.Printf("[RE0浏览器] ✓ 浏览器已启动（%s，pid %d，用时 %s）", v.Product, l.PID(), time.Since(start).Round(time.Millisecond))
	}
	re0BrLauncher, re0Br, re0BrProxy = l, b, proxyNote
	re0BrJanitor.Do(func() { go re0BrowserJanitor() })
	return b, nil
}

func re0killLocked() {
	if re0Br != nil {
		re0Br.Close()
	}
	if re0BrLauncher != nil {
		re0BrLauncher.Kill() // 不调 Cleanup：那会删掉用户数据目录，登录态就没了
	}
	re0Br, re0BrLauncher = nil, nil
}

// re0CloseBrowser 关掉常驻浏览器（空闲 / 退出 / 换账号 / 重新下载）
func re0CloseBrowser(why string) {
	re0BrMu.Lock()
	defer re0BrMu.Unlock()
	if re0Br == nil {
		return
	}
	re0killLocked()
	log.Printf("[RE0浏览器] ■ 浏览器已关闭（%s）", why)
}

func re0BrowserJanitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			re0CloseBrowser("程序退出")
			return
		case <-t.C:
		}
		if !re0OpMu.TryLock() {
			continue // 正在用
		}
		re0BrMu.Lock()
		idle := re0Br != nil && time.Since(re0BrLastUse) > re0BrowserIdle
		re0BrMu.Unlock()
		if idle {
			re0CloseBrowser(fmt.Sprintf("空闲超过 %d 分钟", int(re0BrowserIdle.Minutes())))
		}
		re0OpMu.Unlock()
	}
}

// re0ResetProfile 换了账号：清掉旧的登录态（用户数据目录），指纹种子保留
func re0ResetProfile() {
	re0OpMu.Lock()
	defer re0OpMu.Unlock()
	re0CloseBrowser("RE0 账号变了")
	if cloakRoot == "" {
		return
	}
	if err := os.RemoveAll(cloakProfileDir()); err == nil {
		log.Printf("[RE0浏览器] ○ 已清掉旧账号的登录态")
	}
}

// ==================== 一次页面操作 ====================

// re0OpState 正在做的网页操作走到哪一步（界面「检查状态」「立即签到」等着的时候每秒读一次，GET /re0/browser 的 op）
type re0OpState struct {
	What  string `json:"what"`
	Step  string `json:"step"`
	Since int64  `json:"since"` // 这次操作开始的时间（unix 秒）
}

var (
	re0OpStMu sync.Mutex
	re0OpSt   *re0OpState
)

// re0Step 记下当前步骤（没有进行中的操作时忽略）
func re0Step(format string, args ...any) {
	re0OpStMu.Lock()
	if re0OpSt != nil {
		re0OpSt.Step = fmt.Sprintf(format, args...)
	}
	re0OpStMu.Unlock()
}

func re0OpSnapshot() *re0OpState {
	re0OpStMu.Lock()
	defer re0OpStMu.Unlock()
	if re0OpSt == nil {
		return nil
	}
	c := *re0OpSt
	return &c
}

// re0Web 一次网页操作：打开 target（相对站点根的路径），被跳到登录页就登录再回来，然后交给 fn。
// what 只用于日志
func re0Web(what, target string, timeout time.Duration, fn func(p *rod.Page) error) error {
	cfg := loadRe0Cfg()
	if !re0WebConfigured(cfg) {
		return fmt.Errorf("未配置 RE0 账号密码")
	}
	if why := re0BrowserBlocked("RE0 " + what + "时发现浏览器没装"); why != "" {
		return errors.New(why)
	}
	if !re0OpMu.TryLock() {
		if cur := re0OpSnapshot(); cur != nil {
			log.Printf("[RE0网页] ○ %s 排队：等「%s」做完", what, cur.What)
		}
		re0OpMu.Lock()
	}
	defer re0OpMu.Unlock()
	start := time.Now()
	re0OpStMu.Lock()
	re0OpSt = &re0OpState{What: what, Step: "准备", Since: start.Unix()}
	re0OpStMu.Unlock()
	defer func() {
		re0OpStMu.Lock()
		re0OpSt = nil
		re0OpStMu.Unlock()
	}()
	log.Printf("[RE0网页] ▶ %s：%s", what, target)
	err := re0WebRun(cfg, what, target, timeout, fn)
	re0BrMu.Lock()
	re0BrLastUse = time.Now()
	re0BrMu.Unlock()
	if err != nil {
		log.Printf("[RE0网页] ✗ %s 失败（用时 %s）: %v", what, time.Since(start).Round(time.Millisecond), err)
		return err
	}
	log.Printf("[RE0网页] ✓ %s 完成（用时 %s）", what, time.Since(start).Round(time.Millisecond))
	return nil
}

func re0WebRun(cfg *re0Cfg, what, target string, timeout time.Duration, fn func(p *rod.Page) error) (retErr error) {
	b, err := re0StartBrowser()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		// 浏览器进程坏了：下次重起
		re0CloseBrowser("开新页面失败")
		return fmt.Errorf("开页面失败: %v", err)
	}
	defer page.Close()
	defer func() {
		if retErr != nil {
			re0SaveDebug(page, what)
		}
	}()
	p := page.Context(ctx)
	// 无头时给一个固定的视口（官方包装层对 148 以下的版本也是这样，否则窗口尺寸前后对不上）
	_ = (proto.EmulationSetDeviceMetricsOverride{Width: 1920, Height: 947, DeviceScaleFactor: 1}).Call(p)

	full := cfg.BaseURL + target
	if err := re0Goto(p, full); err != nil {
		return err
	}
	// 没登录时站点是在页面加载完之后才在前端跳去 /login 的：等一下，看最后落在登录页还是出现了用户菜单
	re0Step("确认登录状态")
	if re0AwaitAuth(p) == "login" {
		log.Printf("[RE0网页] ○ 被跳到登录页，登录账号 %s", re0MaskUser(cfg.Username))
		if err := re0Login(p, cfg); err != nil {
			return err
		}
		if err := re0Goto(p, full); err != nil {
			return err
		}
		if re0AwaitAuth(p) == "login" {
			return fmt.Errorf("登录后仍被跳回登录页，请检查账号密码")
		}
	} else {
		log.Printf("[RE0网页] ○ 登录态有效，不用重新登录")
	}
	err = fn(p)
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("操作超时（%s）: %v", timeout, err)
	}
	return err
}

// re0SaveDebug 失败时把当时的页面存一份截图与 HTML（DATA_DIR/cloakbrowser/debug，只留最近 10 份），
// 站点改版后看图就知道卡在哪一步
func re0SaveDebug(page *rod.Page, what string) {
	if cloakRoot == "" || page == nil {
		return
	}
	dir := filepath.Join(cloakRoot, "debug")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	p := page.Timeout(10 * time.Second)
	base := filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+re0SafeName(what))
	if img, err := p.Screenshot(false, nil); err == nil {
		_ = os.WriteFile(base+".png", img, 0o644)
	}
	if html, err := p.HTML(); err == nil {
		_ = os.WriteFile(base+".html", []byte(html), 0o644)
	}
	log.Printf("[RE0网页]   失败现场已存：%s.png / .html（%s）", base, re0URL(p))
	// 截图要去容器里翻，日志里先给一段可见文字与弹窗情况：分得清是没登录、被弹窗挡着还是页面改版
	if res, err := p.Eval(`() => {
		const dlg = [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].filter(d => d.getClientRects().length)
			.map(d => (d.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 80));
		return {text: (document.body?.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 300), dialogs: dlg};
	}`); err == nil {
		var v struct {
			Text    string   `json:"text"`
			Dialogs []string `json:"dialogs"`
		}
		if res.Value.Unmarshal(&v) == nil {
			if len(v.Dialogs) > 0 {
				log.Printf("[RE0网页]   当时开着的弹窗：%s", strings.Join(v.Dialogs, " | "))
			}
			log.Printf("[RE0网页]   当时页面文字：%s", v.Text)
		}
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 20 {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names[:len(names)-20] {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

func re0SafeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' || r == '*' || r == '?' {
			return '_'
		}
		return r
	}, s)
}

// re0AwaitAuth 最多等 10 秒：落在登录页回 "login"，出现用户菜单回 "in"，都没等到回 ""（交给后面的操作自己判断）
func re0AwaitAuth(p *rod.Page) string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if re0OnLogin(p) {
			return "login"
		}
		if re0FindVisible(p, re0UserMenuSel) != nil {
			return "in"
		}
		if time.Now().After(deadline) || p.GetContext().Err() != nil {
			return ""
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func re0MaskUser(u string) string {
	r := []rune(strings.TrimSpace(u))
	if len(r) <= 2 {
		return "**"
	}
	return string(r[:2]) + "***"
}

func re0URL(p *rod.Page) string {
	info, err := p.Info()
	if err != nil {
		return ""
	}
	return info.URL
}

func re0OnLogin(p *rod.Page) bool {
	u, err := url.Parse(re0URL(p))
	return err == nil && strings.TrimRight(u.Path, "/") == re0LoginPath
}

// re0Goto 打开页面并等过 Cloudflare 验证
func re0Goto(p *rod.Page, u string) error {
	start := time.Now()
	if pu, err := url.Parse(u); err == nil {
		re0Step("打开页面 %s", pu.Path)
	}
	cf := re0WatchCFNet(p)
	defer cf.stop()
	if err := p.Navigate(u); err != nil {
		return fmt.Errorf("打开 %s 失败: %v", u, err)
	}
	// 等 load 事件最多 30 秒；Next.js 的流式页面可能迟迟不触发，不算失败
	wctx, cancel := context.WithTimeout(p.GetContext(), 30*time.Second)
	_ = p.Context(wctx).WaitLoad()
	cancel()
	if err := re0WaitCloudflare(p); err != nil {
		re0BrMu.Lock()
		route := re0BrProxy
		re0BrMu.Unlock()
		log.Printf("[RE0网页]   浏览器线路：%s；challenges.cloudflare.com 的请求：%s", route, cf.summary())
		return err
	}
	log.Printf("[RE0网页]   打开 %s → %s（%s）", u, re0URL(p), time.Since(start).Round(time.Millisecond))
	return nil
}

// re0CFNet 记下浏览器对 challenges.cloudflare.com 的请求结果。
// 2026-10-30 现场：验证页从 re0.me 加载正常、Turnstile 的 api.js 也插进了页面，验证框却始终没出来，
// 看不出是这个域名不通、被代理拒了还是 Cloudflare 回了错误；有了这份记录，失败日志里一眼分清
type re0CFNet struct {
	mu    sync.Mutex
	urls  map[proto.NetworkRequestID]string
	notes []string
	stop  func()
}

func re0WatchCFNet(p *rod.Page) *re0CFNet {
	n := &re0CFNet{urls: map[proto.NetworkRequestID]string{}, stop: func() {}}
	if (proto.NetworkEnable{}).Call(p) != nil {
		return n
	}
	ctx, cancel := context.WithCancel(p.GetContext())
	n.stop = cancel
	add := func(id proto.NetworkRequestID, note string) {
		n.mu.Lock()
		defer n.mu.Unlock()
		u, ok := n.urls[id]
		if !ok {
			return
		}
		delete(n.urls, id)
		if len(n.notes) < 12 {
			n.notes = append(n.notes, u+" "+note)
		}
	}
	go p.Context(ctx).EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		if e.Request != nil && re0IsCFURL(e.Request.URL) {
			path := e.Request.URL
			if pu, err := url.Parse(path); err == nil {
				path = pu.Path
				if len(path) > 60 {
					path = path[:60] + "…"
				}
			}
			n.mu.Lock()
			n.urls[e.RequestID] = path
			n.mu.Unlock()
		}
	}, func(e *proto.NetworkResponseReceived) {
		if e.Response != nil {
			add(e.RequestID, strconv.Itoa(e.Response.Status))
		}
	}, func(e *proto.NetworkLoadingFailed) {
		add(e.RequestID, "失败 "+e.ErrorText)
	})()
	return n
}

// summary 一行写清：发出的请求各自什么结果，还挂着没回的有几个
func (n *re0CFNet) summary() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.notes) == 0 && len(n.urls) == 0 {
		return "一个都没发出去（页面里的 Cloudflare 脚本没跑起来）"
	}
	out := strings.Join(n.notes, "；")
	if len(n.urls) > 0 {
		if out != "" {
			out += "；"
		}
		out += fmt.Sprintf("另有 %d 个一直没回应（连不上或被代理挂住）", len(n.urls))
	}
	return out
}

const re0ChallengeJS = `() => Boolean(
	window._cf_chl_opt ||
	document.querySelector('#challenge-running, #challenge-form') ||
	/^(Just a moment|Checking your browser|请稍候|请稍等)/i.test(document.title)
)`

// re0WaitCloudflare 有 Cloudflare 验证页就等它过（最长 90 秒），每 5 秒试着点一次复选框，最多 3 次
func re0WaitCloudflare(p *rod.Page) error {
	start := time.Now()
	deadline := start.Add(90 * time.Second)
	detected, clicks, sawFrame, reloaded := false, 0, false, false
	var nextClick time.Time
	for time.Now().Before(deadline) {
		res, err := p.Eval(re0ChallengeJS)
		if err != nil {
			if p.GetContext().Err() != nil {
				return p.GetContext().Err()
			}
			time.Sleep(500 * time.Millisecond) // 页面正在跳转
			continue
		}
		if !res.Value.Bool() {
			if detected {
				log.Printf("[RE0网页] ✓ Cloudflare 验证已通过")
			}
			return nil
		}
		if !detected {
			detected = true
			log.Printf("[RE0网页] ○ 遇到 Cloudflare 验证（%s），等它自动通过或点验证框，最长 90 秒", re0URL(p))
			re0Step("过 Cloudflare 验证（最长 90 秒）")
		}
		if clicks < 3 && time.Now().After(nextClick) {
			if re0ClickCFCheckbox(p) {
				clicks++
				log.Printf("[RE0网页]   已点 Cloudflare 验证框（第 %d 次）", clicks)
				re0Step("过 Cloudflare 验证：已点验证框（第 %d 次），等结果", clicks)
			}
			nextClick = time.Now().Add(5 * time.Second)
		}
		if !sawFrame {
			sawFrame = re0CFFramePresent(p)
		}
		// 验证框一直没加载出来（2026-10-30 现场：页面只有「Performing security verification」一行字，90 秒没出框）：
		// 同样的浏览器在别的网络下几秒就出框、点一次就过，卡住的多半是那一次 challenges.cloudflare.com 没取到，刷新一次再给它机会
		if !sawFrame && !reloaded && time.Since(start) > 30*time.Second {
			reloaded = true
			log.Printf("[RE0网页]   30 秒没等到 Cloudflare 验证框，刷新页面重试一次")
			if err := p.Reload(); err == nil {
				wctx, cancel := context.WithTimeout(p.GetContext(), 20*time.Second)
				_ = p.Context(wctx).WaitLoad()
				cancel()
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if detected && !sawFrame {
		return fmt.Errorf("Cloudflare 验证框 90 秒内没加载出来（%s）：容器到 challenges.cloudflare.com 不通，或 Cloudflare 不给这个出口 IP 出验证框，试试 RE0 设置里的「通过代理访问」或换个出口", re0URL(p))
	}
	if detected {
		return fmt.Errorf("Cloudflare 验证 90 秒内没通过（%s，点了 %d 次验证框），可能是出口 IP 被 Cloudflare 判成高风险，试试换个代理 / 出口", re0URL(p), clicks)
	}
	return fmt.Errorf("页面一直在跳转，读不出来（%s）", re0URL(p))
}

// re0ClickCFCheckbox 验证框在 challenges.cloudflare.com 的 iframe 里、还套着封闭的 Shadow DOM，
// 选择器选不到：按无障碍树找 checkbox 再用坐标点（插件 _click_cf_checkbox 同思路）。
// 开着站点隔离时这个 iframe 是独立进程（OOPIF），要附到它自己的 target 上找、在它自己的视口坐标里点；
// 同进程时（关了隔离）直接在主页面按 FrameID 找
func re0ClickCFCheckbox(p *rod.Page) bool {
	// 独立进程的 iframe
	if tg, err := (proto.TargetGetTargets{}).Call(p.Browser()); err == nil {
		for _, ti := range tg.TargetInfos {
			if ti.Type != "iframe" || !re0IsCFURL(ti.URL) {
				continue
			}
			// PageFromTarget 会发只对顶层页面有效的命令，iframe 只能裸附加再按会话包一层
			att, err := proto.TargetAttachToTarget{TargetID: ti.TargetID, Flatten: true}.Call(p.Browser())
			if err != nil {
				continue
			}
			fp := p.Browser().PageFromSession(att.SessionID)
			x, y, ok := re0CFCheckboxAt(fp, "")
			clicked := ok && re0HumanClick(fp, x, y) == nil
			_ = proto.TargetDetachFromTarget{SessionID: att.SessionID}.Call(p.Browser())
			if clicked {
				return true
			}
		}
	}
	// 同进程的 iframe
	tree, err := proto.PageGetFrameTree{}.Call(p)
	if err != nil {
		return false
	}
	var frames []*proto.PageFrame
	var walk func(t *proto.PageFrameTree)
	walk = func(t *proto.PageFrameTree) {
		if t == nil {
			return
		}
		if re0IsCFURL(t.Frame.URL) {
			frames = append(frames, t.Frame)
		}
		for _, c := range t.ChildFrames {
			walk(c)
		}
	}
	walk(tree.FrameTree)
	for _, f := range frames {
		if x, y, ok := re0CFCheckboxAt(p, f.ID); ok && re0HumanClick(p, x, y) == nil {
			return true
		}
	}
	return false
}

// re0CFFramePresent 页面上有没有 Cloudflare 验证框的 iframe（独立进程的看 target，同进程的看 frame 树）
func re0CFFramePresent(p *rod.Page) bool {
	if tg, err := (proto.TargetGetTargets{}).Call(p.Browser()); err == nil {
		for _, ti := range tg.TargetInfos {
			if ti.Type == "iframe" && re0IsCFURL(ti.URL) {
				return true
			}
		}
	}
	tree, err := proto.PageGetFrameTree{}.Call(p)
	if err != nil {
		return false
	}
	var walk func(t *proto.PageFrameTree) bool
	walk = func(t *proto.PageFrameTree) bool {
		if t == nil {
			return false
		}
		if re0IsCFURL(t.Frame.URL) {
			return true
		}
		for _, c := range t.ChildFrames {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(tree.FrameTree)
}

func re0IsCFURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() == "challenges.cloudflare.com"
}

// re0CFCheckboxAt 在 p（或 p 里的 frameID）的无障碍树里找没勾上的 checkbox，返回点击坐标（框内随机偏一点）
func re0CFCheckboxAt(p *rod.Page, frameID proto.PageFrameID) (float64, float64, bool) {
	ax, err := proto.AccessibilityGetFullAXTree{FrameID: frameID}.Call(p)
	if err != nil {
		return 0, 0, false
	}
	for _, n := range ax.Nodes {
		if n.Ignored || n.Role == nil || n.Role.Value.Str() != "checkbox" || n.BackendDOMNodeID == 0 {
			continue
		}
		skip := false
		for _, prop := range n.Properties {
			if prop.Value == nil {
				continue
			}
			v := prop.Value.Value
			if (prop.Name == "disabled" && v.Bool()) || (prop.Name == "checked" && (v.Bool() || v.Str() == "true")) {
				skip = true
			}
		}
		if skip {
			continue
		}
		box, err := proto.DOMGetBoxModel{BackendNodeID: n.BackendDOMNodeID}.Call(p)
		if err != nil || box.Model == nil || len(box.Model.Content) < 8 {
			continue
		}
		q := box.Model.Content
		x, y := (q[0]+q[2]+q[4]+q[6])/4, (q[1]+q[3]+q[5]+q[7])/4
		if x <= 0 || y <= 0 {
			continue
		}
		// 落点在框内随机偏一点：Turnstile 看鼠标轨迹，每次都点正中心不像人
		return x + float64(rand.Intn(9)-4), y + float64(rand.Intn(7)-3), true
	}
	return 0, 0, false
}

// re0HumanClick 像人一样点一下：从旁边分几段移过去，停顿后按下、松开。
// 直接发 CDP 的 Input.dispatchMouseEvent（isTrusted=true），这样附加到 iframe 会话的页面也能用（它没有 rod 的 Mouse）
func re0HumanClick(p *rod.Page, x, y float64) error {
	sx, sy := x-180-float64(rand.Intn(120)), y+90+float64(rand.Intn(80))
	steps := 18 + rand.Intn(10)
	for i := 0; i <= steps; i++ {
		f := float64(i) / float64(steps)
		f = f * f * (3 - 2*f) // 先快后慢
		if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved,
			X: sx + (x-sx)*f, Y: sy + (y-sy)*f}).Call(p); err != nil {
			return err
		}
		time.Sleep(time.Duration(8+rand.Intn(14)) * time.Millisecond)
	}
	time.Sleep(time.Duration(120+rand.Intn(200)) * time.Millisecond)
	if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: x, Y: y,
		Button: proto.InputMouseButtonLeft, Buttons: gson.Int(1), ClickCount: 1}).Call(p); err != nil {
		return err
	}
	time.Sleep(time.Duration(60+rand.Intn(90)) * time.Millisecond)
	return proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: x, Y: y,
		Button: proto.InputMouseButtonLeft, ClickCount: 1}.Call(p)
}

// re0Visible 按 CSS 选择器找第一个看得见的元素（找不到立刻返回 nil，不等）
const re0VisibleJS = `(sel) => [...document.querySelectorAll(sel)].find(e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden') || null`

func re0FindVisible(p *rod.Page, sel string) *rod.Element {
	el, err := p.Sleeper(rod.NotFoundSleeper).ElementByJS(rod.Eval(re0VisibleJS, sel))
	if err != nil {
		return nil
	}
	return el
}

// re0WaitVisible 等一个看得见的元素出现
func re0WaitVisible(p *rod.Page, sel string, d time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(d)
	for {
		if el := re0FindVisible(p, sel); el != nil {
			return el, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等 %s 出现超时", sel)
		}
		if err := p.GetContext().Err(); err != nil {
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// re0CoverJS 元素中心点上最上层的是谁（被挡住时说清被什么挡着；没被挡回空串）
const re0CoverJS = `function() {
	const r = this.getBoundingClientRect();
	const t = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
	if (!t || t === this || this.contains(t)) return '';
	const d = t.closest('[role="dialog"], [role="alertdialog"]');
	if (d) return '弹窗「' + (d.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 60) + '」';
	return t.tagName.toLowerCase() + (t.className && typeof t.className === 'string' ? '.' + t.className.trim().split(/\s+/).slice(0, 2).join('.') : '');
}`

// re0Click 真实点击（CDP 鼠标事件，isTrusted=true）。
// 元素多半出自 re0FindVisible，带着 NotFoundSleeper：rod 点击前「等元素不被挡住」的重试沿用元素的 sleeper，
// 被挡一次就直接报 cannot find element（2026-10-22 现场：首页公告晚一步弹出、盖住了用户菜单，签到时好时坏）。
// 换回默认 sleeper，被挡住时等到超时；失败了说清被什么挡着
func re0Click(el *rod.Element) error {
	err := el.Sleeper(rod.DefaultSleeper).Timeout(10*time.Second).Click(proto.InputMouseButtonLeft, 1)
	if err == nil {
		return nil
	}
	if res, e := el.Timeout(3 * time.Second).Eval(re0CoverJS); e == nil {
		if by := res.Value.Str(); by != "" {
			return fmt.Errorf("%v（被 %s 挡住）", err, by)
		}
	}
	return err
}

// ==================== 登录 ====================

const (
	re0UserSel = `input[name='username'], input[name='email'], input[type='email'], input[autocomplete='username'], input[placeholder*='邮箱'], input[placeholder*='email'], input[placeholder*='用户名']`
	re0PassSel = `input[name='password'], input[type='password']`
)

// re0NativeSetJS 受控输入框（React）的值没留住时：用原生 setter 写值再发 input / change，通知 React 更新
const re0NativeSetJS = `function(v) {
	const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
	setter.call(this, v);
	this.dispatchEvent(new Event('input', {bubbles: true}));
	this.dispatchEvent(new Event('change', {bubbles: true}));
}`

const re0LoginHintJS = `() => {
	for (const sel of ['[role="alert"]', '[data-slot="toast"]', '.error', '[class*="error"]', '[class*="Error"]']) {
		const el = document.querySelector(sel);
		const t = el && (el.innerText || '').trim();
		if (t) return t;
	}
	return '';
}`

func re0Login(p *rod.Page, cfg *re0Cfg) error {
	start := time.Now()
	re0Step("登录：填写账号密码")
	user, err := re0WaitVisible(p, re0UserSel, 15*time.Second)
	if err != nil {
		return fmt.Errorf("登录页上没找到账号输入框（%s）", re0URL(p))
	}
	pass, err := re0WaitVisible(p, re0PassSel, 15*time.Second)
	if err != nil {
		return fmt.Errorf("登录页上没找到密码输入框")
	}
	fields := []struct {
		label string
		el    *rod.Element
		val   string
	}{{"账号", user, strings.TrimSpace(cfg.Username)}, {"密码", pass, cfg.Password}}
	ok := false
	for attempt := 1; attempt <= 3 && !ok; attempt++ {
		for _, f := range fields {
			if re0InputValue(f.el) == f.val {
				continue
			}
			_ = f.el.SelectAllText()
			if err := f.el.Input(f.val); err != nil {
				log.Printf("[RE0网页]   %s常规填写出错（%v），检查实际值", f.label, err)
			}
			if re0InputValue(f.el) != f.val {
				if _, err := f.el.Eval(re0NativeSetJS, f.val); err == nil {
					log.Printf("[RE0网页]   %s值没留住，改用原生输入事件写入", f.label)
				}
			}
		}
		time.Sleep(500 * time.Millisecond) // 受控表单重渲染后两项一起复核，免得填密码时把账号清掉
		ok = true
		for _, f := range fields {
			if re0InputValue(f.el) != f.val {
				ok = false
				log.Printf("[RE0网页]   第 %d 次复核：%s没填上", attempt, f.label)
			}
		}
	}
	if !ok {
		return fmt.Errorf("登录表单填不上，已停止提交")
	}
	// 账号密码错误之类的提示是一闪而过的 toast：提交前装好观察器，等的时候一起看
	_, _ = p.Eval(re0ToastObserverJS)
	defer re0StopToasts(p)
	submitted := false
	if btn := re0FindVisible(p, `button[type='submit']`); btn != nil {
		submitted = re0Click(btn) == nil
	}
	if !submitted {
		if btn, err := p.Sleeper(rod.NotFoundSleeper).ElementR("button", `^\s*(登录|登 录|Login|Sign in)\s*$`); err == nil {
			submitted = re0Click(btn) == nil
		}
	}
	if !submitted {
		_ = pass.Type(input.Enter)
	}
	log.Printf("[RE0网页]   已提交登录表单，等离开登录页")
	re0Step("登录：已提交，等站点跳转")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !re0OnLogin(p) {
			log.Printf("[RE0网页] ✓ 登录成功（用时 %s）", time.Since(start).Round(time.Millisecond))
			return nil
		}
		for _, t := range re0ReadToasts(p) {
			if t.Status == "error" || strings.Contains(t.Text, "错误") || strings.Contains(t.Text, "失败") || strings.Contains(t.Text, "不正确") {
				return fmt.Errorf("登录失败：%s", truncateStr(strings.ReplaceAll(strings.TrimSpace(t.Text), "\n", " "), 120))
			}
		}
		if err := p.GetContext().Err(); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
	hint := ""
	if res, err := p.Eval(re0LoginHintJS); err == nil {
		hint = strings.TrimSpace(res.Value.Str())
	}
	if hint != "" {
		return fmt.Errorf("登录失败：%s", truncateStr(hint, 120))
	}
	return fmt.Errorf("登录超时（30 秒还在登录页），请检查账号密码")
}

func re0InputValue(el *rod.Element) string {
	v, err := el.Property("value")
	if err != nil {
		return ""
	}
	return v.Str()
}

// ==================== 签到 ====================

const (
	re0UserMenuSel = `button[aria-label="打开用户菜单"], button[aria-label="Open user menu"]`
	// 每日签到按钮按图标认（插件 CHECKIN_BUTTON_SELECTORS[False]）。赌狗签到不开放（会扣分，见 re0checkin.go）
	re0CheckinBtnSel = `button:has(path[d^="M11.5 21h-5.5"])`
)

// re0CheckinBtnJS 先按图标认，认不出再按文字认右上角抽屉里的「每日签到」（图标换了也能点；「赌狗签到」不匹配）
const re0CheckinBtnJS = `(sel) => {
	const vis = e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden';
	const byIcon = [...document.querySelectorAll(sel)].find(vis);
	if (byIcon) return byIcon;
	return [...document.querySelectorAll('button, [role="menuitem"], a')]
		.find(e => vis(e) && /^(每日签到|Daily check-?in)/i.test((e.innerText || '').trim().replace(/\s+/g, ' '))) || null;
}`

func re0FindCheckinBtn(p *rod.Page) *rod.Element {
	el, err := p.Sleeper(rod.NotFoundSleeper).ElementByJS(rod.Eval(re0CheckinBtnJS, re0CheckinBtnSel))
	if err != nil {
		return nil
	}
	return el
}

// re0ToastObserverJS 装一个观察者，收集之后新出现 / 变化的提示（toast / alert）
const re0ToastObserverJS = `() => {
	window.__s115Obs?.disconnect();
	window.__s115Toasts = [];
	const sel = '[data-slot="toast"], [role="alert"], [role="alertdialog"]';
	const prev = new WeakMap(), seen = new Map();
	const boxes = () => [...new Set([...document.querySelectorAll(sel)].map(el => el.closest('[data-slot="toast"]') || el))];
	const read = el => ({
		text: (el.innerText || '').trim(),
		status: el.classList.contains('toast--success') ? 'success' : el.classList.contains('toast--danger') ? 'error' : ''
	});
	for (const el of boxes()) prev.set(el, JSON.stringify(read(el)));
	const collect = () => {
		for (const el of boxes()) {
			const r = read(el), v = JSON.stringify(r);
			if (!r.text || !el.getClientRects().length || prev.get(el) === v) continue;
			prev.set(el, v);
			seen.set(el, r);
		}
		window.__s115Toasts = [...seen.values()];
	};
	window.__s115Obs = new MutationObserver(collect);
	window.__s115Obs.observe(document.body, {childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ['class']});
}`

type re0Toast struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

func re0ReadToasts(p *rod.Page) []re0Toast {
	res, err := p.Eval(`() => window.__s115Toasts || []`)
	if err != nil {
		return nil
	}
	var out []re0Toast
	_ = res.Value.Unmarshal(&out)
	return out
}

func re0StopToasts(p *rod.Page) { _, _ = p.Eval(`() => window.__s115Obs?.disconnect()`) }

// parseRe0CheckinToast 签到提示 → 结果。done=false 表示这不是签到结果（别的提示），继续等
func parseRe0CheckinToast(text, status string) (ok bool, msg string, done bool) {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	clean := strings.Join(lines, " ")
	if clean == "" {
		return false, "", false
	}
	low := strings.ToLower(clean)
	has := func(ws ...string) bool {
		for _, w := range ws {
			if strings.Contains(low, w) {
				return true
			}
		}
		return false
	}
	if has("已经签到", "已签到", "签到过", "明天再来", "already checked in", "already checked-in", "already signed in") {
		var rest []string
		for _, l := range lines {
			if l != "签到失败" && l != "Check-in failed" {
				rest = append(rest, l)
			}
		}
		return true, "今日已签到：" + firstNonEmpty(strings.Join(rest, " "), clean), true
	}
	if status == "error" || has("失败", "错误", "异常", "请先登录", "未登录", "error", "failed") {
		return false, clean, true
	}
	if status == "success" || has("签到成功", "check-in successful", "check in successful") {
		return true, clean, true
	}
	return false, "", false
}

// re0WebCheckin 网页签到（每日签到，不赌）
func re0WebCheckin() (ok bool, msg string, err error) {
	err = re0Web("每日签到", "/", 4*time.Minute, func(p *rod.Page) error {
		re0Step("关闭首页弹窗")
		if err := re0DismissDialogs(p); err != nil {
			return err
		}
		defer re0StopToasts(p)
		// 公告可能在上面关完之后才弹出来、盖住用户菜单或抽屉：点不了就再关一遍弹窗重来，最多 3 次
		var lastErr error
		clicked := false
		for try := 1; try <= 3 && !clicked; try++ {
			if try > 1 {
				log.Printf("[RE0网页]   第 %d 次尝试签到（上次：%v）", try, lastErr)
				re0Step("重新关闭首页弹窗")
				if err := re0DismissDialogs(p); err != nil {
					return err
				}
			}
			re0Step("打开用户菜单找签到按钮")
			btn := re0FindCheckinBtn(p) // 抽屉可能还开着
			if btn == nil {
				menu, err := re0WaitVisible(p, re0UserMenuSel, 15*time.Second)
				if err != nil {
					return fmt.Errorf("没找到用户菜单，页面可能改版了（%s）", re0URL(p))
				}
				if err := re0Click(menu); err != nil {
					lastErr = fmt.Errorf("点开用户菜单失败: %v", err)
					continue
				}
				deadline := time.Now().Add(10 * time.Second)
				for btn == nil && time.Now().Before(deadline) {
					if btn = re0FindCheckinBtn(p); btn == nil {
						time.Sleep(250 * time.Millisecond)
					}
				}
				if btn == nil {
					lastErr = fmt.Errorf("用户菜单里没找到「每日签到」，页面可能改版了")
					continue
				}
			}
			if _, err := p.Eval(re0ToastObserverJS); err != nil {
				return fmt.Errorf("装提示观察器失败: %v", err)
			}
			if err := re0Click(btn); err != nil {
				lastErr = fmt.Errorf("点签到按钮失败: %v", err)
				continue
			}
			clicked = true
		}
		if !clicked {
			return lastErr
		}
		log.Printf("[RE0网页]   已点每日签到，等结果提示")
		re0Step("已点每日签到，等结果")
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			ts := re0ReadToasts(p)
			for i := len(ts) - 1; i >= 0; i-- {
				if o, m, done := parseRe0CheckinToast(ts[i].Text, ts[i].Status); done {
					ok, msg = o, m
					log.Printf("[RE0网页]   签到提示：%s", truncateStr(strings.ReplaceAll(ts[i].Text, "\n", " "), 120))
					return nil
				}
			}
			if err := p.GetContext().Err(); err != nil {
				return err
			}
			time.Sleep(250 * time.Millisecond)
		}
		return fmt.Errorf("等签到结果超时（60 秒没有提示）")
	})
	return ok, msg, err
}

var re0DialogBtnRe = regexp.MustCompile(`^(我知道了(\s*[（(].*?[)）])?|保存并启用推荐|Got it|Save and enable recommendations)$`)

// re0DismissDialogs 首页的公告 / 首次登录的偏好设置弹窗会挡住用户菜单：逐个关掉（插件 _dismiss_home_dialogs 同流程）。
// 偏好设置要求选一个偏好网盘才能保存，没选过就选 115
func re0DismissDialogs(p *rod.Page) error {
	if _, err := re0WaitVisible(p, re0UserMenuSel, 30*time.Second); err != nil {
		return fmt.Errorf("首页没加载出用户菜单（%s）", re0URL(p))
	}
	deadline := time.Now().Add(45 * time.Second)
	// 公告是首页数据加载完才弹的，可能比用户菜单晚：没有弹窗也要连续 2 秒都没有才算关干净
	var clearSince time.Time
	for {
		res, err := p.Eval(`() => [...document.querySelectorAll('[role="dialog"]')].filter(d => d.getClientRects().length).length`)
		if err == nil && res.Value.Int() == 0 {
			if clearSince.IsZero() {
				clearSince = time.Now()
			}
			if time.Since(clearSince) >= 2*time.Second {
				return nil
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}
		clearSince = time.Time{}
		if time.Now().After(deadline) {
			return fmt.Errorf("首页弹窗关不掉，请先在网页上手动处理一次公告或首次登录设置")
		}
		btns, _ := p.Sleeper(rod.NotFoundSleeper).Elements(`[role="dialog"] button`)
		clicked := false
		for _, b := range btns {
			txt, _ := b.Text()
			txt = strings.TrimSpace(txt)
			if !re0DialogBtnRe.MatchString(txt) {
				continue
			}
			if vis, _ := b.Visible(); !vis {
				continue
			}
			save := txt == "保存并启用推荐" || txt == "Save and enable recommendations"
			if save {
				if err := re0EnsureProvider115(p, b); err != nil {
					log.Printf("[RE0网页]   偏好设置选 115 网盘失败: %v", err)
					continue
				}
				p.Eval(re0ToastObserverJS)
			}
			if err := re0Click(b); err != nil {
				log.Printf("[RE0网页]   弹窗按钮「%s」暂时点不了: %v", txt, err)
				continue
			}
			clicked = true
			if save {
				log.Printf("[RE0网页]   已保存偏好设置（偏好网盘 115），等站点确认")
				err := re0WaitPrefSaved(p)
				re0StopToasts(p)
				if err != nil {
					return err
				}
			} else {
				log.Printf("[RE0网页]   已关掉首页公告「%s」", txt)
			}
			break
		}
		if !clicked {
			time.Sleep(250 * time.Millisecond)
		}
	}
}

func re0EnsureProvider115(p *rod.Page, btn *rod.Element) error {
	res, err := btn.Eval(`function() {
		const d = this.closest('[role="dialog"]');
		const grid = d && d.querySelector('[class*="UserPreferenceDialog_providerGrid"]');
		if (!grid) return 'none';
		return grid.querySelector('input[type="checkbox"]:checked') ? 'checked' : 'unchecked';
	}`)
	if err != nil {
		return err
	}
	if res.Value.Str() != "unchecked" {
		return nil
	}
	cb, err := p.Sleeper(rod.NotFoundSleeper).ElementByJS(rod.Eval(`() => {
		const grid = document.querySelector('[role="dialog"] [class*="UserPreferenceDialog_providerGrid"]');
		if (!grid) return null;
		const box = [...grid.querySelectorAll('[data-slot="checkbox"]')].find(e => /^115\s*(网盘|Drive)$/i.test((e.innerText || '').trim()));
		return box ? box.querySelector('input[type="checkbox"]') : null;
	}`))
	if err != nil {
		return fmt.Errorf("没找到「115 网盘」选项")
	}
	// 嵌套 label 的鼠标点击切不准状态，用键盘空格（插件注释）
	if err := cb.Focus(); err != nil {
		return err
	}
	if err := cb.Type(input.Space); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	if v, err := cb.Property("checked"); err != nil || !v.Bool() {
		return fmt.Errorf("勾选后仍未选中")
	}
	log.Printf("[RE0网页]   偏好设置里没选网盘，已选 115 网盘")
	return nil
}

func re0WaitPrefSaved(p *rod.Page) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, t := range re0ReadToasts(p) {
			if t.Status == "error" {
				return fmt.Errorf("偏好设置保存失败：%s", t.Text)
			}
		}
		res, err := p.Eval(`() => [...document.querySelectorAll('[role="dialog"]')].some(d => d.getClientRects().length && /保存并启用推荐|Save and enable/.test(d.innerText))`)
		if err == nil && !res.Value.Bool() {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("偏好设置提交后弹窗没关，没收到保存成功的结果")
}

// re0WebCheck 测一下能不能登录（打开首页，需要就登录）
func re0WebCheck() error {
	return re0Web("检查登录", "/", 3*time.Minute, func(p *rod.Page) error {
		re0Step("确认已登录")
		if _, err := re0WaitVisible(p, re0UserMenuSel, 20*time.Second); err != nil {
			return fmt.Errorf("登录后首页没出现用户菜单（%s），可能没登录成功或页面改版了", re0URL(p))
		}
		return nil
	})
}

// ==================== 查资源 ====================

// re0ChunksJS 读页面内嵌的 Next.js 数据分片（self.__next_f.push([1, "..."])），不执行脚本
const re0ChunksJS = `() => [...document.scripts].flatMap(s => {
	const t = (s.textContent || '').trim(), pre = 'self.__next_f.push(';
	if (!t.startsWith(pre)) return [];
	try {
		const c = JSON.parse(t.slice(pre.length, t.lastIndexOf(')')));
		return c[0] === 1 && typeof c[1] === 'string' ? [c[1]] : [];
	} catch { return []; }
})`

// re0WebResources 网页查一部影片的 115 资源
func re0WebResources(mediaType string, tmdbID int) ([]re0Resource, error) {
	if (mediaType != "movie" && mediaType != "tv") || tmdbID <= 0 {
		return nil, fmt.Errorf("RE0 按 TMDB 条目查资源，需要有效的类型与编号")
	}
	var out []re0Resource
	target := fmt.Sprintf("/tmdb/%s/%d", mediaType, tmdbID)
	err := re0Web("查资源 "+mediaType+"/"+strconv.Itoa(tmdbID), target, 3*time.Minute, func(p *rod.Page) error {
		// 兜底：页面自己发的 JSON 请求里也可能带着资源列表
		var capMu sync.Mutex
		var reqIDs []proto.NetworkRequestID
		_ = proto.NetworkEnable{}.Call(p)
		wait := p.EachEvent(func(e *proto.NetworkResponseReceived) {
			if e.Response != nil && e.Response.Status == 200 && strings.Contains(e.Response.MIMEType, "json") {
				capMu.Lock()
				reqIDs = append(reqIDs, e.RequestID)
				capMu.Unlock()
			}
		})
		go wait()
		re0Step("读取资源列表")
		var lastChunks string
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if re0OnLogin(p) {
				return fmt.Errorf("登录态失效（被跳回登录页）")
			}
			if res, err := p.Eval(re0ChunksJS); err == nil {
				var chunks []string
				_ = res.Value.Unmarshal(&chunks)
				if j := strings.Join(chunks, ""); j != lastChunks {
					lastChunks = j
					if rows, ok := parseRe0PageResources(j); ok {
						out = rows
						if len(rows) == 0 {
							log.Printf("[RE0网页]   这部片在 RE0 上没有 115 资源")
						} else {
							log.Printf("[RE0网页]   页面数据里读到 %d 条 115 资源", len(rows))
						}
						return nil
					}
				}
			}
			capMu.Lock()
			ids := append([]proto.NetworkRequestID(nil), reqIDs...)
			reqIDs = nil
			capMu.Unlock()
			for _, id := range ids {
				body, err := proto.NetworkGetResponseBody{RequestID: id}.Call(p)
				if err != nil || body.Base64Encoded {
					continue
				}
				if rows := parseRe0JSONResources([]byte(body.Body)); len(rows) > 0 {
					out = rows
					log.Printf("[RE0网页]   接口响应里读到 %d 条 115 资源", len(rows))
					return nil
				}
			}
			if err := p.GetContext().Err(); err != nil {
				return err
			}
			time.Sleep(250 * time.Millisecond)
		}
		// 这句会原样显示到找资源页上：排查用的（停在哪个地址、可能改版）只进日志
		log.Printf("[RE0网页]   20 秒内页面数据里既没有资源分组也没有「暂无资源」（%s），页面可能改版了", re0URL(p))
		return fmt.Errorf("RE0 页面 20 秒内没加载出资源列表，请稍后再试")
	})
	return out, err
}

var (
	re0HexKeyRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	re0RefRe    = regexp.MustCompile(`^\$[0-9a-fA-F]+$`)
	re0SlugRe   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	re0HrefRe   = regexp.MustCompile(`/resource/115/([A-Za-z0-9_-]+)/?$`)
)

type re0Unresolved struct{}

// parseRe0PageResources 从 Next.js 的 RSC 数据里找资源分组（带 groupData 的对象，按网盘分组，取 "115"）。
// 数据是 `十六进制id:JSON` 一行一条，值里的 "$3a" 引用另一条（插件 extract_hdhive_page_resources 同思路）。
// ok=false 表示还没读全（有引用解析不出来 / 还没出现分组），调用方接着等页面流式输出
func parseRe0PageResources(raw string) ([]re0Resource, bool) {
	records := map[string]any{}
	for _, line := range strings.Split(raw, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found || !re0HexKeyRe.MatchString(key) {
			continue
		}
		var v any
		if json.Unmarshal([]byte(val), &v) == nil {
			records[key] = v
		}
	}
	var resolve func(v any, seen map[string]bool) any
	resolve = func(v any, seen map[string]bool) any {
		s, ok := v.(string)
		if !ok || !re0RefRe.MatchString(s) {
			return v
		}
		k := s[1:]
		rec, has := records[k]
		if !has || seen[k] {
			return re0Unresolved{}
		}
		next := map[string]bool{k: true}
		for kk := range seen {
			next[kk] = true
		}
		return resolve(rec, next)
	}
	var groups []map[string]any
	empty := false
	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 结果稳定
	for _, k := range keys {
		stack := []any{records[k]}
		for len(stack) > 0 {
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			switch t := v.(type) {
			case []any:
				stack = append(stack, t...)
			case map[string]any:
				if gd, ok := t["groupData"]; ok {
					g := resolve(gd, nil)
					if _, bad := g.(re0Unresolved); bad {
						return nil, false
					}
					if gm, ok := g.(map[string]any); ok {
						groups = append(groups, gm)
					}
				}
				if re0EmptyMarker(t, resolve) {
					empty = true
				}
				for _, vv := range t {
					stack = append(stack, vv)
				}
			}
		}
	}
	if len(groups) == 0 {
		// 没资源的片不给 groupData，而是给空状态组件（2026-10-14 现场，奥德赛 movie/1368337），
		// 认不出来的话要干等 20 秒才报超时
		return nil, empty
	}
	var rows []map[string]any
	for _, g := range groups {
		list, ok := resolve(g["115"], nil).([]any)
		if !ok {
			if g["115"] == nil {
				continue // 这一组没有 115 资源
			}
			return nil, false
		}
		for _, r := range list {
			rv := resolve(r, nil)
			if _, bad := rv.(re0Unresolved); bad {
				return nil, false
			}
			m, ok := rv.(map[string]any)
			if !ok {
				continue
			}
			full := map[string]any{}
			for k, v := range m {
				x := resolve(v, nil)
				if _, bad := x.(re0Unresolved); bad {
					return nil, false
				}
				full[k] = x
			}
			rows = append(rows, full)
		}
	}
	return re0NormalizeRows(rows), true
}

var re0EmptyMsgRe = regexp.MustCompile(`(?i)资源|resource`)

// re0EmptyMarker 网盘一栏的空状态：{"kind":"empty","error":{"message":"暂无资源",…}}。
// 只认 message 讲的是资源的，别的栏目（片单、照片…）也可能用同一个空状态组件
func re0EmptyMarker(m map[string]any, resolve func(any, map[string]bool) any) bool {
	if k, _ := m["kind"].(string); k != "empty" {
		return false
	}
	e, ok := resolve(m["error"], nil).(map[string]any)
	if !ok {
		return false
	}
	msg, _ := resolve(e["message"], nil).(string)
	return re0EmptyMsgRe.MatchString(msg)
}

// parseRe0JSONResources 接口响应 {data: [...]} 里的资源
func parseRe0JSONResources(body []byte) []re0Resource {
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	return re0NormalizeRows(env.Data)
}

// re0NormalizeRows 页面数据 → re0Resource。只留 115 分享与 ED2K（website 写了别的就丢），按 slug 去重。
// unlock_points 为空按免费算（插件同口径）。
// ED2K 资源站方也放在「115网盘」那一组里，行上 website 是 "ed2k"：解锁出来是 ed2k 链接，走 115 离线。
// 插件只会转存分享所以把它们剔掉，照抄的话只有 ED2K 资源的片会显示「没有 115 资源」（2026-10-15 现场，tv/332261）
func re0NormalizeRows(rows []map[string]any) []re0Resource {
	var out []re0Resource
	seen := map[string]bool{}
	for _, r := range rows {
		pan := "115"
		if w, ok := r["website"]; ok && w != nil {
			switch ws := strings.ToLower(fmt.Sprint(w)); ws {
			case "115", "ed2k":
				pan = ws
			default:
				continue
			}
		}
		slug, _ := r["slug"].(string)
		if !re0SlugRe.MatchString(slug) {
			href, _ := r["href"].(string)
			m := re0HrefRe.FindStringSubmatch(href)
			if m == nil {
				continue
			}
			slug = m[1]
		}
		if seen[slug] {
			continue
		}
		seen[slug] = true
		points := 0
		if n, ok := looseInt(r["unlock_points"]); ok {
			points = n
		}
		item := re0Resource{
			Slug:            slug,
			Title:           firstNonEmpty(re0Str(r["remark"]), re0Str(r["title"]), "未命名"),
			PanType:         pan,
			ShareSize:       firstNonEmpty(re0Str(r["share_size"]), re0Str(r["size"])),
			VideoResolution: re0StrList(r["video_resolution"]),
			Source:          re0StrList(r["source"]),
			SubtitleLang:    re0StrList(r["subtitle_language"]),
			UnlockPoints:    &points,
			CreatedAt:       firstNonEmpty(re0Str(r["submitted_at"]), re0Str(r["created_at"]), re0Str(r["posted_at"])),
		}
		for _, k := range []string{"is_unlocked", "unlocked", "is_owned", "already_owned"} {
			if b, ok := r[k].(bool); ok && b {
				item.IsUnlocked = true
			}
		}
		if len(item.VideoResolution) == 0 {
			if s := re0Str(r["resolution"]); s != "" {
				item.VideoResolution = []string{s}
			}
		}
		out = append(out, item)
	}
	return out
}

func re0Str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func re0StrList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s := re0Str(x); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	}
	return nil
}

// ==================== 解锁 ====================

// re0ShareJS 从页面上找 115 分享链接（链接 / 输入框 / 正文），ED2K 资源找 ed2k 链接
const re0ShareJS = `() => {
	const ok = v => {
		try {
			const u = new URL(v);
			return ['http:', 'https:'].includes(u.protocol) && ['115.com', '115cdn.com', 'anxia.com'].includes(u.hostname) &&
				/^\/s\/[^/\s]+\/?$/.test(u.pathname) && !u.username && !u.password;
		} catch { return false; }
	};
	for (const el of document.querySelectorAll('a[href], input, textarea')) {
		const v = el.tagName === 'A' ? el.href : el.value;
		if (v && ok(v.trim())) return v.trim();
	}
	for (const v of (document.body?.innerText || '').match(/https?:\/\/[^\s<>"']+/g) || []) if (ok(v)) return v;
	// ED2K 资源解锁出来的是 ed2k 链接（文件名可能带空格，按 |file|名|大小|hash| 的结构抓）。
	// 一条资源常是一批 ed2k（一集一条），要全部抓下来按 hash 去重、换行拼起来；
	// 只取第一条的话 8 集只下了 1 集（2026-10-22 现场）。结尾的可选段不许吞进下一条 ed2k://
	const ed = /ed2k:\/\/\|file\|[^|\r\n]+\|\d+\|[0-9a-fA-F]{32}\|(?:(?!ed2k:)[^|\s]*\|)*\/?/g;
	const dec = v => { try { return decodeURIComponent(v); } catch { return v; } };
	const seen = new Set(), out = [];
	const add = s => {
		for (const m of s.match(ed) || []) {
			const k = (m.split('|')[4] || '').toLowerCase();
			if (k && !seen.has(k)) { seen.add(k); out.push(m); }
		}
	};
	for (const el of document.querySelectorAll('a[href], input, textarea')) {
		const v = (el.tagName === 'A' ? el.getAttribute('href') : el.value) || '';
		add(v.startsWith('ed2k') ? dec(v) : v);
	}
	add(document.body?.innerText || '');
	return out.join('\n');
}`

// isRe0ShareURL 是不是 115 分享链接
func isRe0ShareURL(v string) bool {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	switch u.Hostname() {
	case "115.com", "115cdn.com", "anxia.com":
	default:
		return false
	}
	return regexp.MustCompile(`^/s/[^/\s]+/?$`).MatchString(u.Path)
}

// isRe0Ed2k 是不是 ed2k 文件链接（ED2K 资源解锁出来的）
func isRe0Ed2k(v string) bool {
	return classifyLink(strings.TrimSpace(v)) == "ed2k"
}

var re0ConfirmRe = regexp.MustCompile(`^\s*(确认解锁|确定解锁|Confirm unlock)\s*$`)

// re0CopyHookJS 拦下页面写剪贴板的动作，把要复制的文本记在 window.__re0Copied。
// 无头浏览器里剪贴板读不回来（也不该动用户的剪贴板），所以不真写、只截下来
const re0CopyHookJS = `() => {
	window.__re0Copied = '';
	const cap = t => { window.__re0Copied = String(t ?? ''); };
	const c = navigator.clipboard;
	if (c) {
		try { c.writeText = t => { cap(t); return Promise.resolve(); }; } catch {}
		try {
			c.write = async items => {
				for (const it of items || []) {
					if ((it.types || []).includes('text/plain')) cap(await (await it.getType('text/plain')).text());
				}
			};
		} catch {}
	}
	const orig = document.execCommand.bind(document);
	document.execCommand = (cmd, ...a) => {
		if (String(cmd).toLowerCase() === 'copy') {
			const el = document.activeElement;
			const s = (el && 'value' in el && el.value) || String(window.getSelection() || '');
			if (s) { cap(s); return true; }
		}
		return orig(cmd, ...a);
	};
	return true;
}`

const re0CopyAllRe = `^\s*(复制全部|全部复制|一键复制|复制所有|Copy all)`

// re0CopyAll 点资源页的「复制全部」，返回它要复制的全文（没有这个按钮 / 没截到返回空）。
// 一条 ED2K 资源常是一批链接，页面上未必全展开，以这个按钮给的为准
func re0CopyAll(p *rod.Page) string {
	btn, err := p.Sleeper(rod.NotFoundSleeper).ElementR("button", re0CopyAllRe)
	if err != nil {
		return ""
	}
	if vis, _ := btn.Visible(); !vis {
		return ""
	}
	if _, err := p.Eval(re0CopyHookJS); err != nil {
		log.Printf("[RE0网页]   ⚠ 拦剪贴板失败，只用页面上看得到的链接：%v", err)
		return ""
	}
	if err := re0Click(btn); err != nil {
		log.Printf("[RE0网页]   ⚠ 点「复制全部」失败，只用页面上看得到的链接：%v", err)
		return ""
	}
	for i := 0; i < 12; i++ {
		time.Sleep(250 * time.Millisecond)
		if res, err := p.Eval(`() => window.__re0Copied || ''`); err == nil {
			if s := strings.TrimSpace(res.Value.Str()); s != "" {
				log.Printf("[RE0网页]   「复制全部」拿到 %d 条链接", len(re0SplitLinks(s)))
				return s
			}
		}
	}
	log.Printf("[RE0网页]   ⚠ 点了「复制全部」但没截到内容，只用页面上看得到的链接")
	return ""
}

// re0WebUnlock 网页解锁一条资源。已解锁过的页面直接显示链接，不会再点按钮（不扣积分）。
// 只能从 resolveResourceLink 的 confirm 分支进来
func re0WebUnlock(slug string, points *int) (links []string, code string, err error) {
	if !re0SlugRe.MatchString(slug) {
		return nil, "", fmt.Errorf("RE0 资源编号无效")
	}
	submitted := false
	var link string
	err = re0Web("解锁 "+truncateStr(slug, 16), "/resource/115/"+slug, 3*time.Minute, func(p *rod.Page) error {
		re0Step("找分享链接 / 解锁按钮")
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if re0OnLogin(p) {
				return fmt.Errorf("登录态失效（被跳回登录页）")
			}
			cur := re0URL(p)
			if isRe0ShareURL(cur) {
				link = cur // 解锁后整页跳到了 115
				return nil
			}
			if res, err := p.Eval(re0ShareJS); err == nil {
				if s := res.Value.Str(); isRe0ShareURL(s) {
					link = s
					return nil
				} else if isRe0Ed2k(firstLine(s)) {
					// 页面上的列表可能折叠 / 只显示一部分，「复制全部」给的才是全的；页面上抓到的并在后面兜底
					if all := re0CopyAll(p); all != "" {
						s = all + "\n" + s
					}
					link = s
					return nil
				}
			}
			if el := re0FindVisible(p, `[data-slot="toast"].toast--danger`); el != nil {
				t, _ := el.Text()
				return fmt.Errorf("RE0 解锁失败：%s", strings.TrimSpace(t))
			}
			if !submitted {
				if btn, err := p.Sleeper(rod.NotFoundSleeper).ElementR("button", re0ConfirmRe.String()); err == nil {
					if vis, _ := btn.Visible(); vis {
						if dis, _ := btn.Attribute("disabled"); dis == nil {
							if err := re0Click(btn); err == nil {
								submitted = true
								deadline = time.Now().Add(30 * time.Second)
								log.Printf("[RE0网页]   已点「确认解锁」（%s），等分享链接", re0SpendNote(true, points))
								re0Step("已确认解锁，等分享链接")
							}
						}
					}
				}
			}
			if err := p.GetContext().Err(); err != nil {
				return err
			}
			time.Sleep(250 * time.Millisecond)
		}
		if submitted {
			return fmt.Errorf("点了解锁但 30 秒内没拿到 115 / ed2k 链接（%s）", re0URL(p))
		}
		return fmt.Errorf("页面上既没有分享链接也没有「确认解锁」按钮（%s）", re0URL(p))
	})
	if err != nil {
		return nil, "", err
	}
	links = re0SplitLinks(link)
	if isRe0Ed2k(links[0]) {
		// ed2k 没有提取码；文件名里的 ? 会被 url.Parse 当成查询串
	} else if u, perr := url.Parse(link); perr == nil {
		code = firstNonEmpty(u.Query().Get("password"), u.Query().Get("pwd"))
	}
	log.Printf("[RE0] ✦ 网页解锁: %s → %s%s（%s）", truncateStr(slug, 16), truncateStr(links[0], 60), re0MoreNote(links), re0SpendNote(submitted, points))
	return links, code, nil
}

// re0Ed2kHeadRe 一段以 ed2k:// 开头的文本里取出这条链接（文件名可能带空格，不跨行）
var re0Ed2kHeadRe = regexp.MustCompile(`^ed2k://\|file\|[^|\r\n]+\|\d+\|[0-9a-fA-F]{32}\|(?:[^|\s]*\|)*/?`)

// re0SplitLinks 解锁结果拆成一条条链接：一条资源可能给出一批 ed2k（一集一条），
// 网页上抓到的 / 「复制全部」给的是一段文本（链接之间可能夹着「第1集：」之类），OpenAPI 的 full_url 也可能是多行。
// 先按 ed2k:// 切段再在每段开头取链接（切段在先，结尾的可选段就吞不进下一条），按 hash 去重；
// 不是 ed2k 的原样一条
func re0SplitLinks(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "ed2k://") {
		return []string{s}
	}
	var out []string
	seen := map[string]bool{}
	for i, part := range strings.Split(s, "ed2k://") {
		if i == 0 {
			continue // 第一个 ed2k:// 之前的东西
		}
		l := re0Ed2kHeadRe.FindString("ed2k://" + part)
		if l == "" {
			continue
		}
		key := l
		if f := strings.Split(l, "|"); len(f) > 4 {
			key = strings.ToLower(f[4])
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

func re0MoreNote(links []string) string {
	if len(links) > 1 {
		return fmt.Sprintf(" 等 %d 条", len(links))
	}
	return ""
}

// re0SpendNote 解锁日志里的积分说明。网页通道里免费资源也要点「确认解锁」，
// 所以「点了按钮」不等于花了积分，得看列表上标的积分
func re0SpendNote(submitted bool, points *int) string {
	switch {
	case !submitted:
		return "之前已解锁，不扣积分"
	case points == nil:
		return "积分数不明，以站内记录为准"
	case *points <= 0:
		return "免费资源，没花积分"
	}
	return fmt.Sprintf("花了 %d 积分", *points)
}
