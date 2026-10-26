package api

import (
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

func ptAt(hm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-26 "+hm, time.Local)
	return t
}

func TestPtPickTime(t *testing.T) {
	c := defaultPtSigninCfg()
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		at := ptPickTime(c, ptAt("06:00"), rnd)
		m, ok := ptParseHM(at)
		if !ok || m < 8*60 || m >= 10*60 {
			t.Fatalf("挑出 %q，应在 08:00–10:00", at)
		}
	}
	// 窗口里开的：只挑之后的分钟
	for i := 0; i < 200; i++ {
		m, _ := ptParseHM(ptPickTime(c, ptAt("09:30"), rnd))
		if m <= 9*60+30 || m >= 10*60 {
			t.Fatalf("挑出 %s，应在 09:31 之后", ptHM(m))
		}
	}
	// 窗口过了：今天不跑
	if at := ptPickTime(c, ptAt("10:00"), rnd); at != "" {
		t.Fatalf("窗口过了应返回空，得到 %q", at)
	}
}

func TestPtDue(t *testing.T) {
	c := defaultPtSigninCfg()
	c.Enabled, c.Today, c.TodayAt = true, "2026-10-26", "08:37"
	if ptDue(c, ptAt("08:36")) {
		t.Error("没到点")
	}
	if !ptDue(c, ptAt("08:37")) || !ptDue(c, ptAt("11:00")) {
		t.Error("到点（或重启后错过）都该跑")
	}
	c.LastDay = "2026-10-26"
	if ptDue(c, ptAt("09:00")) {
		t.Error("今天跑过了")
	}
	c.LastDay, c.TodayAt = "", ""
	if ptDue(c, ptAt("09:00")) {
		t.Error("今天没挑时刻（开启时窗口已过）不该跑")
	}
	c.TodayAt, c.Mode = "08:37", ptModeCron
	if ptDue(c, ptAt("09:00")) {
		t.Error("cron 模式不走这里")
	}
}

func TestPtSummary(t *testing.T) {
	items := []ptRoundItem{
		{Name: "织梦", ptResult: ptOK("签到成功")},
		{Name: "憨憨", ptResult: ptFail(ptWhyCF, "被拦下")},
		{Name: "高清视界", ptResult: ptLogin("x")},
		{Name: "我堡", ptResult: ptFail(ptWhyCookie, "Cookie 已失效")},
		{Name: "观众", ptResult: ptFail(ptWhyCF, "被拦下")},
		{Name: "大青虫", Skipped: true},
	}
	title, body := ptSummary(items, ptAt("08:40"))
	if title != "PT 站签到 · 3 个失败" {
		t.Errorf("title=%q", title)
	}
	for _, want := range []string{"成功 1 · 只确认登录 1 · 失败 3", "1 个今天已做完", "憨憨、观众\n  被拦下", "✓ 签到成功\n织梦"} {
		if !strings.Contains(body, want) {
			t.Errorf("汇总里没有 %q：\n%s", want, body)
		}
	}
	// Cookie 失效排在失败最前
	if strings.Index(body, "我堡") > strings.Index(body, "憨憨") {
		t.Errorf("Cookie 失效应排最前：\n%s", body)
	}
}

func TestPtRetrySchedule(t *testing.T) {
	ptRetry.Lock()
	ptRetry.ids, ptRetry.day = nil, ""
	ptRetry.Unlock()
	now := ptAt("08:40")
	items := []ptRoundItem{
		{SiteID: 1, ptResult: ptFail(ptWhyNet, "超时")},
		{SiteID: 2, ptResult: ptFail(ptWhyCookie, "失效")},
		{SiteID: 3, ptResult: ptFail(ptWhyHTTP, "签到页返回 HTTP 503")},
	}
	ptScheduleRetry(items, "cron", now)
	if ids := ptTakeRetry(now.Add(29 * time.Minute)); ids != nil {
		t.Fatalf("不到 30 分钟不该重试：%v", ids)
	}
	ids := ptTakeRetry(now.Add(61 * time.Minute))
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("应重试 1、3：%v", ids)
	}
	// 同一天只试一次
	ptScheduleRetry(items, "cron", now.Add(2*time.Hour))
	if ids := ptTakeRetry(now.Add(5 * time.Hour)); ids != nil {
		t.Fatalf("同一天不该再排：%v", ids)
	}
}

// 一轮：今天做完的跳过、站与站之间等、Cookie 失效打 NeedFix、改了 Cookie 清掉
func TestPtRunRound(t *testing.T) {
	newTestDB(t, "ptsignin.db")
	prevSrc := notifyConfigSource
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	t.Cleanup(func() { notifyConfigSource = prevSrc })
	prevSleep := ptSleep
	var slept []time.Duration
	ptSleep = func(d time.Duration) bool { slept = append(slept, d); return true }
	t.Cleanup(func() { ptSleep = prevSleep })

	srv, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/attendance.php": func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, ptTestNav+`<p>本次签到获得 10 魔力</p>`)
		},
	})
	bad, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/attendance.php": func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, `<input type="password">`)
		},
	})
	h := &Handler{DB: model.DB}
	ok := model.PtSite{Name: "好站", BaseURL: srv.URL + "/", Cookie: "uid=1", Mode: ptModeSignin, Enabled: true}
	broken := model.PtSite{Name: "坏站", BaseURL: bad.URL + "/", Cookie: "uid=1", Mode: ptModeSignin, Enabled: true}
	off := model.PtSite{Name: "停用", BaseURL: "https://off.example/", Cookie: "uid=1", Mode: ptModeSignin}
	mteam := model.PtSite{Name: "馒头", BaseURL: "https://kp.m-team.cc/", Cookie: "uid=1", Mode: ptModeSignin, Enabled: true}
	for _, s := range []*model.PtSite{&ok, &broken, &off, &mteam} {
		if err := h.DB.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	h.DB.Model(&off).Update("enabled", false)

	items, err := h.ptRunRound("cron", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Result != ptResOK || items[1].Reason != ptWhyCookie {
		t.Fatalf("items=%+v", items)
	}
	if len(slept) != 1 || slept[0] < 3*time.Second || slept[0] > 8*time.Second {
		t.Fatalf("两个站之间应等一次 3–8 秒：%v", slept)
	}
	var got model.PtSite
	h.DB.First(&got, broken.ID)
	if !got.NeedFix {
		t.Fatal("Cookie 失效应打 NeedFix")
	}

	// 再跑一轮：好站今天做完了跳过，只请求坏站
	items, _ = h.ptRunRound("manual", nil)
	if len(items) != 2 || !items[0].Skipped || items[1].Skipped {
		t.Fatalf("第二轮 items=%+v", items)
	}

	// 改了 Cookie：NeedFix 清掉
	ck := "uid=1; new=1"
	if why := (ptSiteReq{BaseURL: got.BaseURL, Cookie: &ck}).apply(&got); why != "" || got.NeedFix {
		t.Fatalf("why=%q needfix=%v", why, got.NeedFix)
	}

	var n int64
	h.DB.Model(&model.PtSigninLog{}).Count(&n)
	if n != 3 {
		t.Fatalf("应记 3 条结果，记了 %d", n)
	}
	// 14 天前的清掉
	h.DB.Create(&model.PtSigninLog{SiteID: ok.ID, Day: ptNow().AddDate(0, 0, -20).Format("2006-01-02"), Result: ptResOK})
	ptPruneLogs(h.DB, ptNow())
	h.DB.Model(&model.PtSigninLog{}).Count(&n)
	if n != 3 {
		t.Fatalf("老记录没清：%d", n)
	}
}

func TestPtSiteReqApply(t *testing.T) {
	ck := "uid=1"
	var s model.PtSite
	if why := (ptSiteReq{Preset: "mteam", Cookie: &ck}).apply(&s); why == "" {
		t.Error("馒头第一期不能加")
	}
	s = model.PtSite{}
	if why := (ptSiteReq{BaseURL: "https://zhuque.in/", Cookie: &ck}).apply(&s); why == "" {
		t.Error("填朱雀的地址也要拦")
	}
	s = model.PtSite{}
	if why := (ptSiteReq{Preset: "zmpt", Cookie: &ck}).apply(&s); why != "" || s.BaseURL != "https://zmpt.cc/" || s.Name != "织梦" {
		t.Errorf("why=%q %+v", why, s)
	}
	s = model.PtSite{}
	if why := (ptSiteReq{Preset: "rousi"}).apply(&s); !strings.Contains(why, "Token") {
		t.Errorf("Rousi 没填 Token 应报错：%q", why)
	}
	s = model.PtSite{}
	bad := "abc"
	if why := (ptSiteReq{Preset: "zmpt", Cookie: &bad}).apply(&s); !strings.Contains(why, "格式") {
		t.Errorf("Cookie 格式不对应报错：%q", why)
	}
}
