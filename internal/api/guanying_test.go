package api

import (
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
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

// TestGyExtractTorrentsSample 用真实搜索页样本验证内嵌 _obj.search 解析
func TestGyExtractTorrentsSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gy_search.html")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	items := gyExtractTorrents(string(raw))
	if len(items) == 0 {
		t.Fatalf("真实样本解析出 0 个条目")
	}
	var found bool
	for _, it := range items {
		if title, _ := it["title"].(string); strings.HasPrefix(title, "流浪地球2") && it["size"] == "10.49G" {
			found = true
		}
	}
	if !found {
		t.Errorf("未解析到流浪地球2种子（10.49G），得到 %v", items)
	}
}

// TestGyExtractMagnetSample 用真实种子详情页样本验证磁力提取
func TestGyExtractMagnetSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/gy_detail.html")
	if err != nil {
		t.Skipf("样本缺失: %v", err)
	}
	body := string(raw)
	m := reGyMagnet.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("真实样本未提取到磁力链接")
	}
	if !strings.HasPrefix(m[0], "magnet:?xt=urn:btih:94C6789B") {
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
	if !strings.Contains(title, "流浪地球2") {
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

