package api

import (
	"os"
	"strings"
	"testing"
)

// TestGyLiveFlow 真实链路验证：PoW（统一 UA）→ 登录请求（code/siteid/dosubmit，
// 仅 XHR 头）→ 业务 JSON。假凭据应返回「账号不存在」而非「浏览器验证已过期」。
// 需外网且消耗站点风控额度：仅设 STRMHUB_GY_LIVE=1 时运行。
func TestGyLiveFlow(t *testing.T) {
	if os.Getenv("STRMHUB_GY_LIVE") == "" {
		t.Skip("设 STRMHUB_GY_LIVE=1 运行真实链路验证")
	}
	base := "https://www.xn--wcv59z.com"
	jar := &gyJar{m: map[string]string{}}
	client := gyClient(jar)
	err := gyLoginOnce(client, base, "probe_fake_user_42", "wrongpass")
	if err == nil {
		t.Fatalf("假凭据不应登录成功")
	}
	t.Logf("站点响应: %v", err)
	if strings.Contains(err.Error(), "浏览器验证") {
		t.Errorf("PoW 放行凭证仍被拒: %v", err)
	}
}
