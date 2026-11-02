package api

// ==================== 不太灵影视（木咖）网页通道：CloakBrowser 过 GoEdge WAF 滑块 ====================
//
// 2026-11-02 起站点整站套了 GoEdge WAF 的滑块验证：首页和 /prod/api/v1/* 一律先 307 到
// /WAF/VERIFY/CAPTCHA?info=…&from=…，页面是「Verify Yourself」，把蓝色滑块拖到最右边才提交表单放行。
// 普通 HTTP 请求过不去，所以和癫影一样借 RE0 的 CloakBrowser（进程、用户数据目录、re0OpMu 都共用）：
//   - 打开站点首页，遇到验证页就用真实鼠标事件把滑块拖到底（页面只认 mousedown / mousemove / mouseup）；
//   - 过了之后在页面里 fetch 这次要调的接口（同源，带上浏览器自己的 Cookie），这一次一定拿得到结果；
//   - 顺手把站点 Cookie 和浏览器 UA 抄进 mukakuHTTP：GoEdge 放行认的是 Cookie 的话，之后普通请求就够了，
//     认的是别的（IP / 指纹）时普通请求照样被拦，再走一次浏览器（用户数据目录里有 Cookie，不用再拖）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

var (
	mukakuUAMu sync.Mutex
	mukakuUA   string // 过验证时浏览器的 UA；普通请求跟着用，免得 Cookie 和 UA 对不上
)

func mukakuUserAgent() string {
	mukakuUAMu.Lock()
	defer mukakuUAMu.Unlock()
	if mukakuUA != "" {
		return mukakuUA
	}
	return chromeUA
}

// mukakuWAFJS 当前页面是不是 WAF 验证页
const mukakuWAFJS = `() => location.pathname.toUpperCase().startsWith('/WAF/VERIFY/') || !!document.querySelector('input[name=GOEDGE_WAF_CAPTCHA_ID]')`

// mukakuSliderJS 滑块（#handler）中心与滑槽（#input）右端的视口坐标
const mukakuSliderJS = `() => {
  const h = document.getElementById('handler'), i = document.getElementById('input')
  if (!h || !i) return null
  const a = h.getBoundingClientRect(), b = i.getBoundingClientRect()
  if (!a.width || !b.width) return null
  return { hx: a.left + a.width / 2, hy: a.top + a.height / 2, ex: b.right - a.width / 2 }
}`

// mukakuFetchJS 在页面里调接口；fetch 跟随跳转后落在验证页时 waf=true
const mukakuFetchJS = `async (method, url, body, token) => {
  const h = { 'Accept': 'application/json;charset=UTF-8' }
  if (body != null) h['Content-Type'] = 'application/json;charset=UTF-8'
  if (token) h['Authorization'] = 'Bearer ' + token
  const r = await fetch(url, { method, headers: h, body: body == null ? undefined : body, credentials: 'include' })
  const text = await r.text()
  const waf = /\/WAF\/VERIFY\//i.test(r.url) || text.includes('GOEDGE_WAF_CAPTCHA_ID')
  return { status: r.status, text, waf }
}`

// mukakuWebDo 在 CloakBrowser 里过 WAF、调一次接口，返回 HTTP 状态与原始响应
func mukakuWebDo(cfg *mukakuCfg, method, full string, reqBody []byte) (int, []byte, error) {
	if why := re0BrowserBlocked("不太灵影视遇到人机验证、浏览器没装"); why != "" {
		return 0, nil, fmt.Errorf("站点开启了人机验证，要用浏览器过验证：%s", why)
	}
	what := "调用接口"
	if u, err := url.Parse(full); err == nil {
		what = "调用 " + strings.TrimPrefix(u.Path, "/prod/api/v1/")
	}
	if !re0OpMu.TryLock() {
		if cur := re0OpSnapshot(); cur != nil {
			log.Printf("[不太灵影视] ○ %s 排队：等「%s」做完", what, cur.What)
		}
		re0OpMu.Lock()
	}
	defer re0OpMu.Unlock()
	start := time.Now()
	re0OpStMu.Lock()
	re0OpSt = &re0OpState{What: "不太灵影视" + what, Step: "准备", Since: start.Unix()}
	re0OpStMu.Unlock()
	defer func() {
		re0OpStMu.Lock()
		re0OpSt = nil
		re0OpStMu.Unlock()
		re0BrMu.Lock()
		re0BrLastUse = time.Now()
		re0BrMu.Unlock()
	}()
	status, raw, err := mukakuWebRun(cfg, what, method, full, reqBody)
	if err != nil {
		log.Printf("[不太灵影视] ✗ 浏览器%s 失败（用时 %s）: %v", what, time.Since(start).Round(time.Millisecond), err)
		return 0, nil, err
	}
	log.Printf("[不太灵影视] ✓ 浏览器%s（HTTP %d，用时 %s）", what, status, time.Since(start).Round(time.Millisecond))
	return status, raw, nil
}

func mukakuWebRun(cfg *mukakuCfg, what, method, full string, reqBody []byte) (status int, raw []byte, retErr error) {
	b, err := re0StartBrowser()
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		re0CloseBrowser("开新页面失败")
		return 0, nil, fmt.Errorf("开页面失败: %v", err)
	}
	defer page.Close()
	defer func() {
		if retErr != nil {
			re0SaveDebug(page, "不太灵"+what)
		}
	}()
	p := page.Context(ctx)
	_ = (proto.EmulationSetDeviceMetricsOverride{Width: 1920, Height: 947, DeviceScaleFactor: 1}).Call(p)
	re0Step("打开不太灵影视")
	if err := p.Navigate(cfg.BaseURL + "/"); err != nil {
		return 0, nil, fmt.Errorf("打开不太灵影视失败: %v", err)
	}
	mukakuWaitLoad(p, 30*time.Second)
	if err := mukakuPassWAF(p); err != nil {
		return 0, nil, err
	}
	mukakuKeepCookies(p, cfg.BaseURL)

	re0Step("%s", what)
	var body any
	if reqBody != nil {
		body = string(reqBody)
	}
	// 页面里调同源地址就行，免得镜像域名和 BaseURL 写法（大小写、末尾斜杠）对不上算跨域
	rel := full
	if u, err := url.Parse(full); err == nil {
		rel = u.RequestURI()
	}
	for try := 0; ; try++ {
		res, err := p.Eval(mukakuFetchJS, method, rel, body, cfg.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return 0, nil, fmt.Errorf("操作超时: %v", err)
			}
			return 0, nil, fmt.Errorf("页面里调用接口失败: %v", err)
		}
		if !res.Value.Get("waf").Bool() {
			return res.Value.Get("status").Int(), []byte(res.Value.Get("text").Str()), nil
		}
		// 首页放行了、接口又被拦（WAF 按路径单独计）：到接口地址上再过一次
		if try >= 1 {
			return 0, nil, errors.New("过了人机验证，接口仍被拦下")
		}
		if err := p.Navigate(full); err != nil {
			return 0, nil, fmt.Errorf("打开接口地址失败: %v", err)
		}
		mukakuWaitLoad(p, 30*time.Second)
		if err := mukakuPassWAF(p); err != nil {
			return 0, nil, err
		}
		mukakuKeepCookies(p, cfg.BaseURL)
	}
}

func mukakuWaitLoad(p *rod.Page, d time.Duration) {
	wctx, cancel := context.WithTimeout(p.GetContext(), d)
	_ = p.Context(wctx).WaitLoad()
	cancel()
}

// mukakuOnWAF 页面是不是停在验证页（页面正在跳转读不出来时按「不是」，由调用方再看）
func mukakuOnWAF(p *rod.Page) (bool, error) {
	res, err := p.Eval(mukakuWAFJS)
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

// mukakuPassWAF 停在验证页就拖滑块，最多拖 3 次，每次等它提交跳走
func mukakuPassWAF(p *rod.Page) error {
	on, err := mukakuOnWAF(p)
	if err != nil {
		time.Sleep(time.Second)
		if on, err = mukakuOnWAF(p); err != nil {
			return fmt.Errorf("读不出页面（%s）: %v", re0URL(p), err)
		}
	}
	if !on {
		return nil
	}
	log.Printf("[不太灵影视] ○ 遇到人机验证（滑块），用浏览器拖过去")
	for try := 1; try <= 3; try++ {
		re0Step("过人机验证：拖滑块（第 %d 次）", try)
		if err := mukakuDragSlider(p); err != nil {
			return err
		}
		// 拖到底后页面提交表单，跳回原来的地址
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(700 * time.Millisecond)
			if p.GetContext().Err() != nil {
				return p.GetContext().Err()
			}
			on, err := mukakuOnWAF(p)
			if err != nil {
				continue // 正在跳转
			}
			if !on {
				mukakuWaitLoad(p, 10*time.Second)
				if on, err := mukakuOnWAF(p); err == nil && !on {
					log.Printf("[不太灵影视] ✓ 人机验证已通过（第 %d 次）", try)
					return nil
				}
			}
		}
		log.Printf("[不太灵影视]   第 %d 次拖完还停在验证页，刷新再试", try)
		if err := p.Reload(); err == nil {
			mukakuWaitLoad(p, 20*time.Second)
		}
	}
	return fmt.Errorf("拖了 3 次滑块都没过人机验证（%s）", re0URL(p))
}

// mukakuDragSlider 按下滑块、分步拖到滑槽最右端再松开。页面拿 mousemove 的 e.x 定位、
// 松开时滑块离右端超过 4px 就弹回去，所以终点故意拖过一点（页面会钳在右端）
func mukakuDragSlider(p *rod.Page) error {
	var pos struct {
		HX, HY, EX float64
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := p.Eval(mukakuSliderJS)
		if err == nil && res.Value.Get("hx").Num() > 0 {
			pos.HX, pos.HY, pos.EX = res.Value.Get("hx").Num(), res.Value.Get("hy").Num(), res.Value.Get("ex").Num()
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("验证页上找不到滑块（%s），站点可能换了验证方式", re0URL(p))
		}
		time.Sleep(500 * time.Millisecond)
	}
	x, y := pos.HX+float64(rand.Intn(7)-3), pos.HY+float64(rand.Intn(7)-3)
	// 先把鼠标从旁边挪过来
	sx, sy := x-120-float64(rand.Intn(80)), y+60+float64(rand.Intn(60))
	for i := 0; i <= 12; i++ {
		f := float64(i) / 12
		if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved,
			X: sx + (x-sx)*f, Y: sy + (y-sy)*f}).Call(p); err != nil {
			return err
		}
		time.Sleep(time.Duration(8+rand.Intn(12)) * time.Millisecond)
	}
	time.Sleep(time.Duration(100+rand.Intn(150)) * time.Millisecond)
	if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: x, Y: y,
		Button: proto.InputMouseButtonLeft, Buttons: gson.Int(1), ClickCount: 1}).Call(p); err != nil {
		return err
	}
	ex := pos.EX + 6 + float64(rand.Intn(10))
	steps := 25 + rand.Intn(10)
	cx, cy := x, y
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		f = f * f * (3 - 2*f) // 先快后慢
		cx, cy = x+(ex-x)*f, y+float64(rand.Intn(3)-1)
		if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved,
			X: cx, Y: cy, Button: proto.InputMouseButtonLeft, Buttons: gson.Int(1)}).Call(p); err != nil {
			return err
		}
		time.Sleep(time.Duration(10+rand.Intn(18)) * time.Millisecond)
	}
	time.Sleep(time.Duration(80+rand.Intn(120)) * time.Millisecond)
	return proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: cx, Y: cy,
		Button: proto.InputMouseButtonLeft, ClickCount: 1}.Call(p)
}

// mukakuKeepCookies 把浏览器里站点的 Cookie 与 UA 抄给普通请求用
func mukakuKeepCookies(p *rod.Page, base string) {
	u, err := url.Parse(base)
	if err != nil {
		return
	}
	res, err := proto.NetworkGetCookies{Urls: []string{base + "/"}}.Call(p)
	if err != nil {
		return
	}
	var cs []*http.Cookie
	for _, c := range res.Cookies {
		cs = append(cs, &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: strings.TrimPrefix(c.Domain, "."),
			Secure: c.Secure, HttpOnly: c.HTTPOnly})
	}
	if len(cs) > 0 {
		mukakuHTTP.Jar.SetCookies(u, cs)
	}
	if ua, err := p.Eval(`() => navigator.userAgent`); err == nil {
		if s := ua.Value.Str(); s != "" && !strings.Contains(s, "Headless") {
			mukakuUAMu.Lock()
			mukakuUA = s
			mukakuUAMu.Unlock()
		}
	}
}
