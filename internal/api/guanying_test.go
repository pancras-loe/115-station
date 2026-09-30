package api

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestGyPowLoopMath 验证 RSW 求解循环（t 次平方取模）等价于 x^(2^t) mod N
func TestGyPowLoopMath(t *testing.T) {
	cases := []struct {
		nHex, xHex string
		t          int
	}{
		{"61", "2", 5},
		{"9a7f3", "1c", 17},
		{"fffffffffffffffffffffffffffffffe", "abcdef0123456789", 33},
	}
	for _, c := range cases {
		n, _ := new(big.Int).SetString(c.nHex, 16)
		x, _ := new(big.Int).SetString(c.xHex, 16)
		exp := new(big.Int).Lsh(big.NewInt(1), uint(c.t))
		want := new(big.Int).Exp(x, exp, n)
		y := new(big.Int).Set(x)
		for i := 0; i < c.t; i++ {
			y.Mul(y, y)
			y.Mod(y, n)
		}
		if y.Cmp(want) != 0 {
			t.Errorf("RSW loop mismatch: N=%s x=%s t=%d got %s want %s", c.nHex, c.xHex, c.t, y.Text(16), want.Text(16))
		}
	}
}

// TestGyExtractTorrentsSample 用脱敏搜索页样本验证内嵌 _obj.search 解析。
// 样本保留站点真实的页面结构（_obj.* 内嵌 JSON、并行数组、数字型 seeds），
// 片名/ID/用户名全部换成占位值——真实抓包含站点账号与版权片源，不入库。
func TestGyExtractTorrentsSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gy_search.html")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	items := gyExtractTorrents(string(raw))
	if len(items) == 0 {
		t.Fatalf("样本解析出 0 个条目")
	}
	var found bool
	for _, it := range items {
		if title, _ := it["title"].(string); strings.HasPrefix(title, "示例影片2") && it["size"] == "10.49G" {
			found = true
		}
	}
	if !found {
		t.Errorf("未解析到示例影片2种子（10.49G），得到 %v", items)
	}
}

// TestGyExtractMagnetSample 用脱敏种子详情页样本验证磁力提取
func TestGyExtractMagnetSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gy_detail.html")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	body := string(raw)
	m := reGyMagnet.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("样本未提取到磁力链接")
	}
	if !strings.HasPrefix(m[0], "magnet:?xt=urn:btih:0123456789ABCDEF") {
		t.Errorf("磁力哈希异常: %s", m[0])
	}
	title := ""
	if objStr, ok := gyObjJSON(body, "d"); ok {
		var d struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(objStr), &d) == nil {
			title = d.Title
		}
	}
	if !strings.Contains(title, "示例影片2") {
		t.Errorf("标题解析异常: %q", title)
	}
}

// TestGyObjJSONBalanced 验证花括号配平扫描（含字符串内转义与花括号）
func TestGyObjJSONBalanced(t *testing.T) {
	body := `xx _obj.d={"title":"a\"b{brace}","n":2}; _obj.footer={t:1};`
	objStr, ok := gyObjJSON(body, "d")
	if !ok {
		t.Fatal("未提取到 _obj.d")
	}
	if !strings.HasPrefix(objStr, `{"title"`) || !strings.HasSuffix(objStr, `"n":2}`) {
		t.Errorf("提取范围异常: %q", objStr)
	}
}

// 登录按钮先保存配置、再发登录请求：请求体里没带账号密码时要用已保存的，
// 不能回「请填写账号和密码」（此前前端发空请求体，登录永远失败）
func TestGyLoginFallsBackToSavedCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 登录走不下去即可，只看有没有被参数校验拦下
	}))
	defer site.Close()
	setCfg := func(c *gyCfg) {
		gyCfgMu.Lock()
		gyCfgV, gyCfgAt = c, time.Now()
		gyCfgMu.Unlock()
	}
	t.Cleanup(func() { setCfg(nil) })
	h := &Handler{}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/guanying/login", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.GyLogin(c)
		return w
	}

	setCfg(&gyCfg{BaseURL: site.URL, Cookies: map[string]string{}})
	if w := post(`{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("没有已保存的账号时应当 400: %d %s", w.Code, w.Body)
	}
	setCfg(&gyCfg{BaseURL: site.URL, Username: "u", Password: "p", Cookies: map[string]string{}})
	if w := post(`{}`); w.Code == http.StatusBadRequest {
		t.Fatalf("空请求体应当用已保存的账号去登录: %s", w.Body)
	}
	setCfg(&gyCfg{BaseURL: site.URL, Username: "u", Password: "p", Cookies: map[string]string{}})
	if w := post(`{"username":"u","password":"` + settingMask + `"}`); w.Code == http.StatusBadRequest {
		t.Fatalf("密码是掩码时应当用已存密码: %s", w.Body)
	}
}
