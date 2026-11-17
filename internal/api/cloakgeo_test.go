package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestParseCFTrace(t *testing.T) {
	ip, cc := parseCFTrace([]byte("fl=4f822\nh=re0.me\nip=211.20.18.214\nts=1794908060.000\nloc=TW\nwarp=off\n"))
	if ip != "211.20.18.214" || cc != "TW" {
		t.Fatalf("got %q %q", ip, cc)
	}
	ip, cc = parseCFTrace([]byte("ip=2001:db8::1\r\nloc=XX\r\n"))
	if ip != "2001:db8::1" || cc != "" {
		t.Fatalf("IPv6 / XX: got %q %q", ip, cc)
	}
	if ip, _ := parseCFTrace([]byte("<html>Just a moment...</html>")); ip != "" {
		t.Fatalf("验证页不该认出 IP：%q", ip)
	}
}

func TestParseGeoJSON(t *testing.T) {
	cases := []struct{ name, body, ip, cc, tz string }{
		{"ipinfo", `{"ip":"1.2.3.4","country":"JP","timezone":"Asia/Tokyo","readme":"x"}`, "1.2.3.4", "JP", "Asia/Tokyo"},
		{"ip.sb", `{"ip":"1.2.3.4","country":"Japan","country_code":"JP","timezone":"Asia\/Tokyo","offset":32400}`, "1.2.3.4", "JP", "Asia/Tokyo"},
		{"ipwho.is", `{"ip":"1.2.3.4","success":true,"country_code":"US","timezone":{"id":"America/Los_Angeles","utc":"-08:00"}}`, "1.2.3.4", "US", "America/Los_Angeles"},
		{"ip-api", `{"status":"success","countryCode":"SG","timezone":"Asia/Singapore","query":"1.2.3.4"}`, "1.2.3.4", "SG", "Asia/Singapore"},
		{"三段时区", `{"ip":"1.2.3.4","country":"US","timezone":"America/Indiana/Indianapolis"}`, "1.2.3.4", "US", "America/Indiana/Indianapolis"},
		{"失败", `{"status":"fail","message":"reserved range","query":"10.0.0.1"}`, "", "", ""},
		{"ipwho 失败", `{"success":false,"message":"Invalid IP address"}`, "", "", ""},
		{"时区乱写", `{"ip":"1.2.3.4","country":"JP","timezone":"--lang=xx"}`, "1.2.3.4", "JP", ""},
		{"不是 JSON", `rate limited`, "", "", ""},
	}
	for _, c := range cases {
		ip, cc, tz := parseGeoJSON([]byte(c.body))
		if ip != c.ip || cc != c.cc || tz != c.tz {
			t.Errorf("%s: got %q %q %q, want %q %q %q", c.name, ip, cc, tz, c.ip, c.cc, c.tz)
		}
	}
}

func TestCloakGeoArgs(t *testing.T) {
	// 走代理、全查到：时区语言按出口，WebRTC 报出口 IP
	got := cloakGeoArgs(cloakGeo{IP: "1.2.3.4", Country: "JP", Timezone: "Asia/Tokyo"}, true, "Asia/Shanghai")
	want := map[string]string{"fingerprint-timezone": "Asia/Tokyo", "lang": "ja-JP", "fingerprint-locale": "ja-JP", "fingerprint-webrtc-ip": "1.2.3.4"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("代理：got %v", got)
	}
	// 直连：WebRTC 不改
	got = cloakGeoArgs(cloakGeo{IP: "1.2.3.4", Country: "CN", Timezone: "Asia/Shanghai"}, false, "")
	if got["fingerprint-webrtc-ip"] != "" || got["lang"] != "zh-CN" {
		t.Fatalf("直连：got %v", got)
	}
	// 什么都没查到：时区退回容器 TZ，语言不设（和改之前一样）
	got = cloakGeoArgs(cloakGeo{}, true, "Asia/Shanghai")
	if fmt.Sprint(got) != fmt.Sprint(map[string]string{"fingerprint-timezone": "Asia/Shanghai"}) {
		t.Fatalf("没查到：got %v", got)
	}
	// 表里没有的国家用 en-US
	if got := cloakGeoArgs(cloakGeo{IP: "1.2.3.4", Country: "IS"}, true, ""); got["lang"] != "en-US" {
		t.Fatalf("冷门国家：got %v", got)
	}
}

// 出口按站点自己的 trace 认；时区接口回的 IP 对不上、没时区的跳过，换下一个
func TestCloakResolveGeo(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/cdn-cgi/trace":
			fmt.Fprint(w, "h=re0.me\nip=5.6.7.8\nloc=HK\n")
		case strings.HasPrefix(r.URL.Path, "/a/"):
			fmt.Fprint(w, `{"ip":"9.9.9.9","country":"US","timezone":"America/New_York"}`) // 不是问的 IP
		case strings.HasPrefix(r.URL.Path, "/b/"):
			http.Error(w, "busy", http.StatusTooManyRequests)
		case strings.HasPrefix(r.URL.Path, "/c/"):
			fmt.Fprint(w, `{"ip":"`+strings.TrimPrefix(r.URL.Path, "/c/")+`","country_code":"HK","timezone":{"id":"Asia/Hong_Kong"}}`)
		}
	}))
	defer srv.Close()
	old := cloakGeoLookups
	defer func() { cloakGeoLookups = old }()
	cloakGeoLookups = []string{srv.URL + "/a/%s", srv.URL + "/b/%s", srv.URL + "/c/%s"}

	g, err := cloakResolveGeo(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if g.IP != "5.6.7.8" || g.Country != "HK" || g.Timezone != "Asia/Hong_Kong" || !strings.HasPrefix(srv.URL, "http://"+g.Via) {
		t.Fatalf("got %+v", g)
	}
	if strings.Join(hits, ",") != "/cdn-cgi/trace,/a/5.6.7.8,/b/5.6.7.8,/c/5.6.7.8" {
		t.Fatalf("请求顺序：%v", hits)
	}

	// 时区都查不到：IP 和国家照样带回来（语言、WebRTC 还能对齐），报错说清
	cloakGeoLookups = []string{srv.URL + "/a/%s"}
	g, err = cloakResolveGeo(srv.URL, "")
	if err == nil || g.IP != "5.6.7.8" || g.Country != "HK" || g.Timezone != "" {
		t.Fatalf("查不到时区：%+v %v", g, err)
	}
}

// 走代理时 trace 与时区查询都经代理发出去（和浏览器同一条线路）
func TestCloakResolveGeoViaProxy(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String()) // 代理收到的是绝对地址
		mu.Unlock()
		if r.URL.Path == "/cdn-cgi/trace" {
			fmt.Fprint(w, "ip=5.6.7.8\nloc=JP\n")
			return
		}
		fmt.Fprint(w, `{"status":"success","countryCode":"JP","timezone":"Asia/Tokyo","query":"5.6.7.8"}`)
	}))
	defer proxy.Close()
	old := cloakGeoLookups
	defer func() { cloakGeoLookups = old }()
	cloakGeoLookups = []string{"http://geo.example/json/%s"}

	g, err := cloakResolveGeo("http://re0.example", proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if g.String() != "5.6.7.8 JP Asia/Tokyo" {
		t.Fatalf("got %s", g)
	}
	if strings.Join(seen, ",") != "http://re0.example/cdn-cgi/trace,http://geo.example/json/5.6.7.8" {
		t.Fatalf("代理收到：%v", seen)
	}
	if _, err := cloakResolveGeo("http://re0.example", "::bad"); err == nil || strings.Contains(err.Error(), "::bad") {
		t.Fatalf("坏代理地址应报错且不回显地址：%v", err)
	}
}

func TestRe0CFStateText(t *testing.T) {
	got := re0CFStateText([]string{"Verify you are human", "Cloudflare", "Privacy", "•", "Help", "Verify you are human"})
	if got != "Verify you are human" {
		t.Fatalf("got %q", got)
	}
	got = re0CFStateText([]string{"Verification failed", "Troubleshoot", "Error: 600010", "Cloudflare"})
	if got != "Verification failed / Troubleshoot / Error: 600010" {
		t.Fatalf("got %q", got)
	}
}
