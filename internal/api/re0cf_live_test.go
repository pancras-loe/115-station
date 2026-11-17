//go:build re0cflive

package api

// 对 re0.me 的 Cloudflare 验证页跑一遍真浏览器（不登录，只看过不过得了验证页）。不进 CI（要联网、要浏览器）：
//
//	RE0CF_BROWSER="C:\…\cloakbrowser\chrome.exe" go test -tags re0cflive -run Re0CFLive ./internal/api/ -v -timeout 6m
//
// 走的是生产的 re0StartBrowser + re0Goto：启动参数、出口对齐（cloakgeo.go）、点验证框、读验证框的字都是同一份代码。
// 本机测要用 CloakBrowser 的 Windows 版（同 cloakVersionX64），Edge / Chrome 过不了。
// RE0CF_PROXY=http://127.0.0.1:7890 走代理；RE0CF_ROOT 指定数据目录（指纹种子、用户目录、失败截图），
// 不指定用临时目录 = 每次都是没来过的新访客。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"115-station/internal/config"

	"github.com/go-rod/rod/lib/proto"
)

func TestRe0CFLive(t *testing.T) {
	bin := os.Getenv("RE0CF_BROWSER")
	if bin == "" {
		t.Skip("没设 RE0CF_BROWSER")
	}
	t.Setenv("CLOAKBROWSER_BINARY_PATH", bin)
	oldRoot, oldSrc := cloakRoot, notifyConfigSource
	cloakRoot = firstNonEmpty(os.Getenv("RE0CF_ROOT"), t.TempDir())
	src := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	notifyConfigSource = src
	defer func() {
		re0CloseBrowser("测试结束")
		cloakRoot, notifyConfigSource = oldRoot, oldSrc
	}()
	if px := os.Getenv("RE0CF_PROXY"); px != "" {
		if err := src.SaveSetting("proxy", `{"url":`+mustJSON(px)+`}`); err != nil {
			t.Fatal(err)
		}
		if err := src.SaveSetting("re0", `{"use_proxy":true}`); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("数据目录 %s", cloakRoot)

	b, err := re0StartBrowser()
	if err != nil {
		t.Fatal(err)
	}
	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatal(err)
	}
	p := page.Timeout(3 * time.Minute)
	_ = (proto.EmulationSetDeviceMetricsOverride{Width: 1920, Height: 947, DeviceScaleFactor: 1}).Call(p)
	if res, err := p.Eval(`() => ({ua: navigator.userAgent, lang: navigator.language, langs: navigator.languages.join(','),
		tz: Intl.DateTimeFormat().resolvedOptions().timeZone, off: new Date().getTimezoneOffset()})`); err == nil {
		t.Logf("浏览器：%s", res.Value.JSON("", ""))
	}

	start := time.Now()
	gerr := re0Goto(p, loadRe0Cfg().BaseURL+"/")
	shot := filepath.Join(cloakRoot, "re0cf-"+time.Now().Format("150405")+".png")
	if img, err := p.Screenshot(false, nil); err == nil {
		_ = os.WriteFile(shot, img, 0o644)
	}
	if gerr != nil {
		re0SaveDebug(page, "CF冒烟")
		t.Fatalf("没过（%s，截图 %s）: %v", time.Since(start).Round(time.Second), shot, gerr)
	}
	t.Logf("过了（%s）：%s，截图 %s", time.Since(start).Round(time.Second), re0URL(p), shot)
}
