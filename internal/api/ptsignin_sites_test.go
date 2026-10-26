package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"
)

const ptTestNav = `<a href="userdetails.php?id=1">me</a> <a href="logout.php">退出</a>`

// ptFakeSite 起一个假站；每个请求都核对 Cookie 带上了（海胆 302 那一下 MP 丢过 Cookie）
func ptFakeSite(t *testing.T, routes map[string]http.HandlerFunc) (*httptest.Server, *[]string) {
	t.Helper()
	var hits []string
	mux := http.NewServeMux()
	for path, fn := range routes {
		fn := fn
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			hits = append(hits, r.Method+" "+r.URL.RequestURI())
			if r.Header.Get("api-token") == "" && r.Header.Get("Authorization") == "" && !strings.Contains(r.Header.Get("Cookie"), "uid=1") {
				t.Errorf("%s %s 没带 Cookie", r.Method, r.URL.Path)
			}
			fn(w, r)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func ptHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<html><body>" + body + "</body></html>"))
}

func ptTestSite(srv *httptest.Server, preset string) *model.PtSite {
	return &model.PtSite{ID: 1, Name: "测试站", Preset: preset, BaseURL: srv.URL + "/", Cookie: "uid=1; pass=x", Mode: ptModeSignin, Enabled: true}
}

func TestPtNexusFlows(t *testing.T) {
	cases := []struct {
		name   string
		att    http.HandlerFunc
		home   http.HandlerFunc
		result string
		reason string
	}{
		{name: "签成", att: func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, ptTestNav+`<p>这是您的第 3 次签到，本次签到获得 50 个魔力值。</p>`)
		}, result: ptResOK},
		{name: "签到页认不出", att: func(w http.ResponseWriter, r *http.Request) { ptHTML(w, ptTestNav+`<p>欢迎</p>`) }, result: ptResLogin},
		{name: "Cookie 失效", att: func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, `<form><input type="password" name="password"></form>`)
		}, result: ptResFail, reason: ptWhyCookie},
		{name: "被拦下", att: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head></html>`))
		}, result: ptResFail, reason: ptWhyCF},
		// 签到页 404：退回访问首页，确认登录就算保号
		{name: "签到页 404 退回首页", att: http.NotFound, home: func(w http.ResponseWriter, r *http.Request) { ptHTML(w, ptTestNav) }, result: ptResLogin},
		{name: "站点 502", att: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			home: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }, result: ptResFail, reason: ptWhyHTTP},
	}
	for _, c := range cases {
		routes := map[string]http.HandlerFunc{"/attendance.php": c.att}
		if c.home != nil {
			routes["/"] = c.home
		}
		srv, _ := ptFakeSite(t, routes)
		r := ptSigninOne(ptTestSite(srv, ""))
		if r.Result != c.result || r.Reason != c.reason {
			t.Errorf("%s: %+v，应为 %s/%s", c.name, r, c.result, c.reason)
		}
	}
}

func TestPtRetryable(t *testing.T) {
	if !ptFail(ptWhyHTTP, "签到页返回 HTTP 502").retryable() || !ptFail(ptWhyNet, "超时").retryable() {
		t.Error("网络错误、5xx 应该重试")
	}
	for _, r := range []ptResult{ptFail(ptWhyHTTP, "签到页返回 HTTP 404"), ptFail(ptWhyCookie, "x"), ptFail(ptWhyCF, "x"), ptOK("x")} {
		if r.retryable() {
			t.Errorf("%+v 不该重试", r)
		}
	}
}

// 只保号的站只访问首页
func TestPtKeepMode(t *testing.T) {
	srv, hits := ptFakeSite(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) { ptHTML(w, ptTestNav) },
	})
	s := ptTestSite(srv, "")
	s.Mode = ptModeKeep
	if r := ptSigninOne(s); r.Result != ptResLogin {
		t.Fatalf("%+v", r)
	}
	if len(*hits) != 1 || (*hits)[0] != "GET /" {
		t.Fatalf("只保号应该只请求首页：%v", *hits)
	}
}

func TestPtTTG(t *testing.T) {
	srv, hits := ptFakeSite(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, ptTestNav+`<script>var x = { signed_timestamp: "1761400000", signed_token: "abc123" };</script><a id="signed">签到</a>`)
		},
		"/signed.php": func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Form.Get("signed_timestamp") != "1761400000" || r.Form.Get("signed_token") != "abc123" {
				t.Errorf("签到参数不对：%v", r.Form)
			}
			_, _ = w.Write([]byte("您已连续签到 12 天，奖励积分 10"))
		},
	})
	if r := ptSigninOne(ptTestSite(srv, "ttg")); r.Result != ptResOK {
		t.Fatalf("%+v", r)
	}
	if len(*hits) != 2 {
		t.Fatalf("请求：%v", *hits)
	}
	// 首页已经是绿色的「已签到」：不再 POST
	srv2, hits2 := ptFakeSite(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) { ptHTML(w, ptTestNav+`<b style="color:green;">已签到</b>`) },
	})
	if r := ptSigninOne(ptTestSite(srv2, "ttg")); r.Result != ptResOK || len(*hits2) != 1 {
		t.Fatalf("%+v %v", r, *hits2)
	}
}

func TestPtAgsv(t *testing.T) {
	signed := false
	srv, hits := ptFakeSite(t, map[string]http.HandlerFunc{
		"/attendance.php": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				signed = true
				ptHTML(w, ptTestNav+`<p>签到成功，本次签到获得 50</p>`)
				return
			}
			if signed {
				ptHTML(w, ptTestNav+`<p>[签到已得50]</p>`)
				return
			}
			ptHTML(w, ptTestNav+`<form method="post"><input type="submit" value="立即签到"></form>`)
		},
	})
	if r := ptSigninOne(ptTestSite(srv, "agsvpt")); r.Result != ptResOK || r.Message != "签到成功" {
		t.Fatalf("%+v", r)
	}
	if strings.Join(*hits, ",") != "GET /attendance.php,POST /attendance.php" {
		t.Fatalf("请求：%v", *hits)
	}
	// 签过了：只读一次签到页
	*hits = nil
	if r := ptSigninOne(ptTestSite(srv, "agsvpt")); r.Result != ptResOK || r.Message != "今日已签到" || len(*hits) != 1 {
		t.Fatalf("%+v %v", r, *hits)
	}
}

func TestPtHaidan(t *testing.T) {
	srv, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/signin.php": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/index.php", http.StatusFound) },
		"/index.php": func(w http.ResponseWriter, r *http.Request) {
			ptHTML(w, ptTestNav+`<input type="button" value="已经打卡">`)
		},
	})
	if r := ptSigninOne(ptTestSite(srv, "haidan")); r.Result != ptResOK {
		t.Fatalf("%+v", r)
	}
}

func TestPtHdarea(t *testing.T) {
	srv, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/sign_in.php": func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Method != http.MethodPost || r.Form.Get("action") != "sign_in" {
				t.Errorf("应 POST action=sign_in：%s %v", r.Method, r.Form)
			}
			_, _ = w.Write([]byte("已连续签到278天，此次签到您获得了100魔力值奖励!"))
		},
	})
	r := ptSigninOne(ptTestSite(srv, "hdarea"))
	if r.Result != ptResOK || !strings.Contains(r.Message, "此次签到您获得了100") {
		t.Fatalf("%+v", r)
	}
}

func TestPtBtschool(t *testing.T) {
	srv, hits := ptFakeSite(t, map[string]http.HandlerFunc{
		"/": func(w http.ResponseWriter, r *http.Request) { ptHTML(w, ptTestNav+`<a href="index.php?action=addbonus">每日签到</a>`) },
		"/index.php": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("action") != "addbonus" {
				t.Errorf("应访问 addbonus：%s", r.URL)
			}
			ptHTML(w, ptTestNav+`<p>今天签到您获得60点魔力值</p>`)
		},
	})
	if r := ptSigninOne(ptTestSite(srv, "btschool")); r.Result != ptResOK || len(*hits) != 2 {
		t.Fatalf("%+v %v", r, *hits)
	}
}

func TestPtPttime(t *testing.T) {
	// 签到返回只有几个字，没有导航条
	srv, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/attendance.php": func(w http.ResponseWriter, r *http.Request) { ptHTML(w, "签到成功") },
	})
	if r := ptSigninOne(ptTestSite(srv, "pttime")); r.Result != ptResOK {
		t.Fatalf("%+v", r)
	}
}

func TestPtRousi(t *testing.T) {
	status, body := http.StatusOK, `{"code":0,"message":"ok"}`
	srv, _ := ptFakeSite(t, map[string]http.HandlerFunc{
		"/api/points/attendance": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("api-token") != "tk" {
				t.Errorf("没带 api-token")
			}
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["mode"] != "fixed" {
				t.Errorf("body=%v", in)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		},
	})
	s := ptTestSite(srv, "rousi")
	s.Cookie, s.Token = "", "tk"
	if r := ptSigninOne(s); r.Result != ptResOK || r.Message != "签到成功" {
		t.Fatalf("%+v", r)
	}
	status, body = http.StatusBadRequest, `{"code":1,"message":"今日已签到"}`
	if r := ptSigninOne(s); r.Result != ptResOK || r.Message != "今日已签到" {
		t.Fatalf("%+v", r)
	}
	// 别的 400 不能当成已签
	status, body = http.StatusBadRequest, `{"code":1,"message":"参数错误"}`
	if r := ptSigninOne(s); r.Result != ptResFail {
		t.Fatalf("%+v", r)
	}
	status, body = http.StatusUnauthorized, `{"code":401}`
	if r := ptSigninOne(s); r.Reason != ptWhyToken {
		t.Fatalf("%+v", r)
	}
}

// 没填 Cookie 直接说，不发请求
func TestPtNoCookie(t *testing.T) {
	srv, hits := ptFakeSite(t, map[string]http.HandlerFunc{})
	s := ptTestSite(srv, "")
	s.Cookie = ""
	if r := ptSigninOne(s); r.Reason != ptWhyConfig || len(*hits) != 0 {
		t.Fatalf("%+v %v", r, *hits)
	}
}
