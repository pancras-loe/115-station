package api

import "testing"

// 判定规则一条一条过：认错一次就是把失败报成成功（或反过来天天报 Cookie 失效）
func TestPtPageJudge(t *testing.T) {
	const nav = `<a href="userdetails.php?id=1">me</a> <a href="logout.php">退出</a>`
	cases := []struct {
		name      string
		src       string
		loggedIn  bool
		password  bool
		challenge bool
		signed    bool
	}{
		{name: "已签到整句", src: `<html><body>` + nav + `<p>您今天已经签到过了，请勿重复刷新。</p></body></html>`, loggedIn: true, signed: true},
		{name: "本次奖励", src: `<body>` + nav + `<td>这是您的第 30 次签到，已连续签到 5 天，本次签到获得 100 个魔力值。</td></body>`, loggedIn: true, signed: true},
		{name: "按钮写着已签到", src: `<body>` + nav + `<a class="btn">今日已签到</a></body>`, loggedIn: true, signed: true},
		{name: "英文站已签", src: `<body>` + nav + `<p>You have already attended 12 days, Continuous 3 days, this time you will get 20 bonus.</p></body>`, loggedIn: true, signed: true},
		{name: "藏着的成功模板不算", src: `<body>` + nav + `<div style="display:none">签到成功</div><template>今日已签到</template><script>var s="签到成功"</script></body>`, loggedIn: true},
		{name: "还有签到按钮", src: `<body>` + nav + `<form><input type="submit" value="签到"></form><p>本次签到获得 10 魔力</p></body>`, loggedIn: true},
		{name: "明说失败", src: `<body>` + nav + `<p>签到失败，请稍后重试。已签到</p></body>`, loggedIn: true},
		{name: "尚未签到", src: `<body>` + nav + `<span>今天尚未签到</span></body>`, loggedIn: true},
		{name: "只是奖励说明", src: `<body>` + nav + `<p>每日签到可获得 10 魔力值，连续签到有加成。</p></body>`, loggedIn: true},
		{name: "登录页", src: `<body><form action="takelogin.php"><input name="username"><input type="password" name="password"></form></body>`, password: true},
		{name: "隐藏的密码框不算失效", src: `<body>` + nav + `<div hidden><input type="password"></div></body>`, loggedIn: true},
		{name: "用户面板链接", src: `<body><a href="usercp.php">控制面板</a></body>`, loggedIn: true},
		{name: "Cloudflare", src: `<html><head><title>Just a moment...</title></head><body><div id="challenge-spinner"></div></body></html>`, challenge: true},
		{name: "没有导航", src: `<body><p>欢迎</p></body>`},
	}
	for _, c := range cases {
		p := parsePtPage(c.src)
		if got := p.loggedIn(); got != c.loggedIn {
			t.Errorf("%s: loggedIn=%v，应为 %v", c.name, got, c.loggedIn)
		}
		if got := p.hasPasswordInput(); got != c.password {
			t.Errorf("%s: password=%v，应为 %v", c.name, got, c.password)
		}
		if got := p.challenge(); got != c.challenge {
			t.Errorf("%s: challenge=%v，应为 %v", c.name, got, c.challenge)
		}
		if got, _ := p.signinEvidence(); got != c.signed {
			t.Errorf("%s: signed=%v，应为 %v", c.name, got, c.signed)
		}
	}
}

func TestPtSnippet(t *testing.T) {
	p := parsePtPage(`<body><p>已连续签到 5 天，本次签到获得 100 个魔力值。明天继续</p></body>`)
	ok, snip := p.signinEvidence()
	if !ok || snip != "本次签到获得100个魔力值" {
		t.Fatalf("snip=%q ok=%v", snip, ok)
	}
}

func TestNormPtBaseURL(t *testing.T) {
	cases := map[string]string{
		"zmpt.cc":                            "https://zmpt.cc/",
		"https://zmpt.cc/index.php?x=1":      "https://zmpt.cc/",
		"http://pt.example.com:8080/foo":     "http://pt.example.com:8080/",
		"https://x.org/attendance.php?a=1#b": "https://x.org/attendance.php",
	}
	for in, want := range cases {
		got, err := normPtBaseURL(in)
		if err != nil || got != want {
			t.Errorf("%s → %q (%v)，应为 %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x.org/", "https://"} {
		if _, err := normPtBaseURL(bad); err == nil {
			t.Errorf("%q 应该报错", bad)
		}
	}
}

func TestPtPresetByHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.haidan.cc/":   "haidan", // MP 写的老域名
		"https://haidan.video/":    "haidan", // 截图里的新域名，没有 www 也认
		"https://pt.agsvpt.cn/":    "agsvpt",
		"https://rousi.pro/":       "rousi",
		"https://kp.m-team.cc/":    "mteam",
		"https://unknown.example/": "",
	} {
		got := ""
		if p := ptPresetByHost(raw); p != nil {
			got = p.Key
		}
		if got != want {
			t.Errorf("%s → %q，应为 %q", raw, got, want)
		}
	}
}

func TestPtCookie(t *testing.T) {
	if got := normPtCookie("Cookie: c_secure_uid=1;\n c_secure_pass=abc"); got != "c_secure_uid=1; c_secure_pass=abc" {
		t.Errorf("normPtCookie=%q", got)
	}
	for _, bad := range []string{"", "abc", `{"a":1}`} {
		if ptCookieOK(bad) {
			t.Errorf("%q 不该当成 Cookie", bad)
		}
	}
	if !ptCookieOK("a=1; b=2") {
		t.Error("a=1; b=2 应该算 Cookie")
	}
}
