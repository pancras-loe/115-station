package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 预览返回的 path 文档没写死是目录还是全路径，两种都要认；size 可能是字节数、数字字符串或「1.2 GB」
func TestParseRe0Preview(t *testing.T) {
	raw := `{"share_title":"剧 S01","result_type":"files","files":[
		{"name":"E01.mkv","path":"/剧/Season 1/E01.mkv","size":1073741824},
		{"name":"E02.mkv","path":"/剧/Season 1","size":"2048"},
		{"name":"E03","extension":"mp4","path":"","size":"1.5 GB"},
		{"name":"","path":"/x"}
	]}`
	p, err := parseRe0Preview([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Name, Dir string
		Size      int64
	}
	var got []row
	for _, e := range p.Entries {
		got = append(got, row{e.Name, e.Dir, e.Size})
	}
	want := []row{
		{"E01.mkv", "剧/Season 1", 1 << 30},
		{"E02.mkv", "剧/Season 1", 2048},
		{"E03.mp4", "", resParseSize("1.5 GB")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %+v", got)
	}
	if p.Invalid != "" || p.Title != "剧 S01" {
		t.Fatalf("invalid=%q title=%q", p.Invalid, p.Title)
	}
	// ID 唯一：pickShareEpisodes 按 ID 记解析结果
	if p.Entries[0].ID == p.Entries[1].ID {
		t.Fatal("预览条目 ID 重复")
	}

	p, _ = parseRe0Preview([]byte(`{"result_type":"validation","files":[],"resource_validate_message":"分享已取消"}`))
	if p.Invalid != "分享已取消" {
		t.Fatalf("失效说明 = %q", p.Invalid)
	}
}

func TestParseRe0Me(t *testing.T) {
	me, err := parseRe0Me([]byte(`{"id":7,"username":"u","nickname":"","level":"V","points":"123","is_banned":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if me.Name() != "u" || me.Points == nil || *me.Points != 123 || me.Banned {
		t.Fatalf("me = %+v", me)
	}
	me, _ = parseRe0Me([]byte(`{"nickname":"昵称","points":5,"banned":true}`))
	if me.Name() != "昵称" || *me.Points != 5 || !me.Banned {
		t.Fatalf("me = %+v", me)
	}
	if me, _ = parseRe0Me([]byte(`{"username":"u"}`)); me.Points != nil {
		t.Fatal("没给积分应为 nil")
	}
}

// 老授权没记 scope：按当时申请的 query unlock 算，缺 meta / write 要提示重新授权
func TestRe0MissingScopes(t *testing.T) {
	cfg := &re0Cfg{}
	if m := cfg.missingScopes(); m != nil {
		t.Fatalf("未授权不提示: %v", m)
	}
	cfg.AccessToken = "t"
	if m := cfg.missingScopes(); !reflect.DeepEqual(m, []string{"meta", "write"}) {
		t.Fatalf("老授权缺 = %v", m)
	}
	cfg.Scope = re0Scope
	if m := cfg.missingScopes(); m != nil {
		t.Fatalf("新授权缺 = %v", m)
	}
	if re0ScopeOf("", []string{"query", "unlock"}) != "query unlock" {
		t.Fatal("scopes 数组没拼上")
	}
}

func TestRe0ErrHint(t *testing.T) {
	e := &re0Err{Code: "FORBIDDEN", Message: "forbidden", Status: http.StatusForbidden}
	if !strings.Contains(e.Error(), "V") {
		t.Fatalf("403 没提示 V 用户: %s", e.Error())
	}
	e = &re0Err{Code: "USER_SCOPE_NOT_ALLOWED", Message: "x", Status: http.StatusForbidden}
	if !strings.Contains(e.Error(), "重新授权") || strings.Contains(e.Error(), "V 状态") {
		t.Fatalf("scope 错误提示不对: %s", e.Error())
	}
	// 积分不足要让订阅停下付费解锁
	if !re0StopUnlocking(&re0Err{Code: "INSUFFICIENT_POINTS", Message: "Insufficient points", Status: 402}) {
		t.Fatal("积分不足应停止付费解锁")
	}
}

// 假 RE0：业务接口按 Token 应答，refresh 换出新 Token
type fakeRe0 struct {
	srv       *httptest.Server
	valid     string // 当前有效的 Access Token
	refreshOK bool
	refreshes atomic.Int32
	calls     atomic.Int32
}

func newFakeRe0(t *testing.T) *fakeRe0 {
	f := &fakeRe0{valid: "new", refreshOK: true}
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "secret" {
			write(w, 401, map[string]any{"success": false, "code": "INVALID_API_KEY", "message": "bad key"})
			return
		}
		if r.URL.Path == "/api/public/openapi/oauth/refresh" {
			f.refreshes.Add(1)
			if !f.refreshOK {
				write(w, 401, map[string]any{"success": false, "code": "OPENAPI_REAUTH_REQUIRED", "message": "reauth"})
				return
			}
			write(w, 200, map[string]any{"success": true, "code": "200", "data": map[string]any{
				"access_token": f.valid, "refresh_token": "r2", "expires_in": 3600, "scope": re0Scope}})
			return
		}
		f.calls.Add(1)
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "":
			write(w, 401, map[string]any{"success": false, "code": "OPENAPI_USER_REQUIRED", "message": "need user"})
		case auth == "Bearer expired":
			write(w, 401, map[string]any{"success": false, "code": "OPENAPI_REFRESH_REQUIRED", "message": "expired"})
		case auth == "Bearer revoked":
			write(w, 401, map[string]any{"success": false, "code": "OPENAPI_REAUTH_REQUIRED", "message": "reauth"})
		case auth != "Bearer "+f.valid:
			write(w, 401, map[string]any{"success": false, "code": "INVALID_OPENAPI_USER_TOKEN", "message": "bad token"})
		case r.URL.Path == "/api/open/me":
			write(w, 200, map[string]any{"success": true, "code": "200", "data": map[string]any{"username": "u", "points": 42}})
		default:
			write(w, 403, map[string]any{"success": false, "code": "FORBIDDEN", "message": "not vip"})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func withRe0Settings(t *testing.T) {
	t.Helper()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	t.Cleanup(func() {
		notifyConfigSource = nil
		model.DB = nil
		re0CfgMu.Lock()
		re0CfgV = nil
		re0CfgMu.Unlock()
	})
}

func TestRe0CallTokenLifecycle(t *testing.T) {
	withRe0Settings(t)
	f := newFakeRe0(t)

	// 过期 → 刷新一次 → 重放成功，新 Token 与权限落盘
	cfg := &re0Cfg{BaseURL: f.srv.URL, ClientSecret: "secret", AccessToken: "expired", RefreshToken: "r1"}
	me, err := re0FetchMe(nil, cfg)
	if err != nil || me.Name() != "u" || *me.Points != 42 {
		t.Fatalf("刷新后重放: me=%+v err=%v", me, err)
	}
	if f.refreshes.Load() != 1 || cfg.AccessToken != "new" || cfg.RefreshToken != "r2" || cfg.Scope != re0Scope {
		t.Fatalf("刷新结果 cfg=%+v refreshes=%d", cfg, f.refreshes.Load())
	}
	if saved := loadRe0Cfg(); saved.AccessToken != "new" {
		t.Fatalf("新 Token 没落盘: %+v", saved)
	}

	// 要重新授权：不刷新，清掉 Token
	cfg = &re0Cfg{BaseURL: f.srv.URL, ClientSecret: "secret", AccessToken: "revoked", RefreshToken: "r1", Scope: re0Scope}
	f.refreshes.Store(0)
	if _, err := re0FetchMe(nil, cfg); !errors.Is(err, errRe0Reauth) {
		t.Fatalf("REAUTH 应返回 errRe0Reauth: %v", err)
	}
	if f.refreshes.Load() != 0 || cfg.AccessToken != "" || cfg.RefreshToken != "" || cfg.Scope != "" {
		t.Fatalf("REAUTH 后 cfg=%+v refreshes=%d", cfg, f.refreshes.Load())
	}

	// 刷新也被拒（Refresh Token 失效）：同样清掉
	f.refreshOK = false
	cfg = &re0Cfg{BaseURL: f.srv.URL, ClientSecret: "secret", AccessToken: "expired", RefreshToken: "r1"}
	if _, err := re0FetchMe(nil, cfg); !errors.Is(err, errRe0Reauth) || cfg.AccessToken != "" {
		t.Fatalf("刷新失败: err=%v cfg=%+v", err, cfg)
	}

	// 403 带上 V 用户提示
	cfg = &re0Cfg{BaseURL: f.srv.URL, ClientSecret: "secret", AccessToken: "new"}
	err = re0Call(nil, cfg, http.MethodGet, "/api/open/resources/movie/1", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "V 状态") {
		t.Fatalf("403 提示: %v", err)
	}
}

func TestRe0CheckinDue(t *testing.T) {
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	on := re0CheckinCfg{Enabled: true}
	cases := []struct {
		name string
		cfg  re0CheckinCfg
		now  time.Time
		fail time.Time
		want bool
	}{
		{"关着", re0CheckinCfg{}, day.Add(9 * time.Hour), time.Time{}, false},
		{"不到 8 点", on, day.Add(7 * time.Hour), time.Time{}, false},
		{"8 点后", on, day.Add(8 * time.Hour), time.Time{}, true},
		{"今天签过", re0CheckinCfg{Enabled: true, LastDone: "2026-10-08"}, day.Add(9 * time.Hour), time.Time{}, false},
		{"昨天签过", re0CheckinCfg{Enabled: true, LastDone: "2026-10-07"}, day.Add(9 * time.Hour), time.Time{}, true},
		{"刚失败", on, day.Add(9 * time.Hour), day.Add(9*time.Hour - 10*time.Minute), false},
		{"失败一小时后", on, day.Add(9 * time.Hour), day.Add(8 * time.Hour), true},
	}
	for _, c := range cases {
		if got := re0CheckinDue(c.cfg, c.now, c.fail); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

// 付费的 RE0 资源：上限以内先看文件预览，按文件判能补的集；预览不可用退回按标题；超限的不预览
func TestPlanSubCandidatesRe0Preview(t *testing.T) {
	now := time.Now()
	sub := &model.Subscription{ID: 1, MediaType: "tv"}
	missing := epKeys("S01E03")
	paid := func(title, ref string) ResourceItem {
		return ResourceItem{Source: "re0", Kind: "share115", Action: "unlock", Title: title, Ref: ref, Relevant: true, Points: pts(10), Rank: 0}
	}
	items := []ResourceItem{
		paid("剧 2023 估不出", "unknown"),      // 标题看不出覆盖：预览说有 E03 → 能用
		paid("剧 S01 写着有", "titlelies"),     // 标题写着 S01：预览说里面没有 E03 → 不用
		paid("剧 S01 预览不可用", "nopreview"), // 预览出错 → 退回按标题，S01 覆盖 E03
		paid("剧 2023 预览不可用也估不出", "nopreview2"),
	}
	var asked []string
	preview := func(it ResourceItem) ([]epKey, string, error) {
		asked = append(asked, it.Ref)
		switch it.Ref {
		case "unknown":
			return epKeys("S01E03"), "", nil
		case "titlelies":
			return nil, "里没有缺的集", nil
		}
		return nil, "", errors.New("503")
	}
	skipped := map[string]int{}
	cands, over := planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 20, Re0Left: -1,
		Re0Preview: preview, Skipped: skipped})
	var got []string
	for _, c := range cands {
		got = append(got, c.Item.Ref)
	}
	if !reflect.DeepEqual(got, []string{"unknown", "nopreview"}) || len(over) != 0 {
		t.Fatalf("候选 = %v over=%d", got, len(over))
	}
	if !reflect.DeepEqual(cands[0].Exact, epKeys("S01E03")) || cands[1].Exact != nil {
		t.Fatalf("Exact: %v / %v", cands[0].Exact, cands[1].Exact)
	}
	if skipped["要花积分、文件预览里没有缺的集"] != 1 {
		t.Fatalf("跳过原因 = %v", skipped)
	}

	// 超过上限：只通知，不预览
	asked = nil
	_, over = planSubCandidates(items, subPickCtx{Sub: sub, Missing: missing, Now: now, Re0Max: 5, Re0Left: -1, Re0Preview: preview})
	if len(asked) != 0 || len(over) != 2 {
		t.Fatalf("超限时 预览了 %v，over=%d", asked, len(over))
	}

	// 预览核实的范围里已经不缺了，就不再试（前一条资源补上了）
	if cands[0].coversAnyMissing(map[epKey]bool{}, false) {
		t.Fatal("缺集补齐后不该再试")
	}
	if !cands[0].coversAnyMissing(map[epKey]bool{{S: 1, E: 3}: true}, false) {
		t.Fatal("还缺 E03 时应该试")
	}
}

// 预览和真正转存同一套按集挑：Season 2 目录里的 01.mkv 认成 S02E01；一轮最多预览 subRe0PreviewMax 条
func TestSubRe0PreviewFn(t *testing.T) {
	if _, err := model.InitDB("file:re0preview_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer func() { model.DB = nil }()
	sub := &model.Subscription{ID: 1, Title: "剧", MediaType: "tv"}
	calls := 0
	r := &subRunner{h: &Handler{DB: model.DB}, cfg: defaultSubscribeCfg(), re0Files: func(slug string) (*re0Preview, error) {
		calls++
		switch slug {
		case "dead":
			return &re0Preview{Invalid: "分享已取消"}, nil
		case "err":
			return nil, errors.New("400")
		}
		return &re0Preview{Entries: []shareEntry{
			{ID: "a", Name: "01.mkv", Dir: "剧/Season 2", Size: 1 << 30},
			{ID: "b", Name: "02.mkv", Dir: "剧/Season 2", Size: 1 << 30},
		}}, nil
	}}
	fn := r.re0PreviewFn(sub, map[epKey]bool{{S: 2, E: 1}: true, {S: 3, E: 1}: true})
	covered, _, err := fn(ResourceItem{Ref: "ok", Title: "剧"})
	if err != nil || !reflect.DeepEqual(covered, []epKey{{S: 2, E: 1}}) {
		t.Fatalf("covered=%v err=%v", covered, err)
	}
	if covered, why, err := fn(ResourceItem{Ref: "dead"}); err != nil || covered != nil || !strings.Contains(why, "失效") {
		t.Fatalf("失效资源: %v %q %v", covered, why, err)
	}
	if _, _, err := fn(ResourceItem{Ref: "err"}); err == nil {
		t.Fatal("预览出错应返回错误（调用方退回按标题判）")
	}
	for i := 0; i < 5; i++ {
		fn(ResourceItem{Ref: "ok"})
	}
	if calls != subRe0PreviewMax {
		t.Fatalf("一轮预览了 %d 次，上限 %d", calls, subRe0PreviewMax)
	}
	// 电影：挑得出视频就算能补
	movie := &model.Subscription{ID: 2, Title: "片", MediaType: "movie"}
	if covered, _, _ := r.re0PreviewFn(movie, nil)(ResourceItem{Ref: "ok"}); len(covered) != 1 {
		t.Fatalf("电影 covered=%v", covered)
	}
	if (&subRunner{}).re0PreviewFn(sub, nil) != nil {
		t.Fatal("没接预览时应返回 nil")
	}
}
