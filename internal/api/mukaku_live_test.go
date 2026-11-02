//go:build mukakulive

package api

// 对真站点的冒烟测试：站点换了验证方式 / 接口后先跑它。不进 CI（要联网、要浏览器）：
//
//	MUKAKU_LIVE_BROWSER="C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe" go test -tags mukakulive -run MukakuLive ./internal/api/ -v
//
// 用本机的 Chrome / Edge 起无头浏览器并改掉 UA，生产环境用的是 CloakBrowser。
// 只测不登录也能做的事：过滑块、页面里调搜索、抄出来的 Cookie 普通请求能不能用。

import (
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

func TestMukakuLive(t *testing.T) {
	bin := os.Getenv("MUKAKU_LIVE_BROWSER")
	if bin == "" {
		t.Skip("没设 MUKAKU_LIVE_BROWSER")
	}
	u, err := launcher.New().Bin(bin).Leakless(false).Headless(true).
		Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36").
		Launch()
	if err != nil {
		t.Fatal(err)
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cfg := &mukakuCfg{BaseURL: mukakuDefaultBase, AppID: mukakuAppID, Identity: mukakuIdentity}
	p := b.MustPage(cfg.BaseURL + "/").Timeout(120 * time.Second)
	mukakuWaitLoad(p, 30*time.Second)
	on, _ := mukakuOnWAF(p)
	t.Logf("打开首页后停在验证页：%v（%s）", on, re0URL(p))
	if err := mukakuPassWAF(p); err != nil {
		t.Fatalf("过滑块: %v", err)
	}
	mukakuKeepCookies(p, cfg.BaseURL)
	t.Logf("过了之后：%s，UA %s", re0URL(p), mukakuUserAgent())

	q := url.Values{"sb": {"沙丘"}, "page": {"1"}, "limit": {"5"}, "app_id": {cfg.AppID}, "identity": {cfg.Identity}}
	rel := "/prod/api/v1/getVideoList?" + q.Encode()
	res, err := p.Eval(mukakuFetchJS, "GET", rel, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.Get("waf").Bool() {
		t.Fatalf("页面里调搜索仍被拦")
	}
	data, err := mukakuEnvelope(res.Value.Get("status").Int(), []byte(res.Value.Get("text").Str()))
	if err != nil {
		t.Fatalf("页面里搜索: %v", err)
	}
	t.Logf("页面里搜索返回 %d 字节", len(data))

	// 抄出来的 Cookie 普通请求能不能用（不能用也不算失败：生产会退回浏览器）
	status, raw, err := mukakuHTTPDo(cfg, http.MethodGet, cfg.BaseURL+rel, nil)
	if err != nil {
		t.Logf("普通请求带 Cookie 仍被拦：%v（每次都要走浏览器）", err)
		return
	}
	if _, err := mukakuEnvelope(status, raw); err != nil {
		t.Fatalf("普通请求: %v", err)
	}
	t.Logf("普通请求带 Cookie 可以直接用")
}
