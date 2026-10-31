package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPansouNormalize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" || r.URL.Query().Get("kw") != "movie" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		fmt.Fprint(w, `{"code":0,"data":{"total":10,"merged_by_type":{
   "115":[{"url":"https://115.com/s/old","datetime":"2026-01-01"},{"url":"https://115.com/s/new","datetime":"2026-02-01"},{"url":"https://115.com/s/new"}],
   "quark":[{"url":"https://pan.quark.cn/s/a"}],
   "aliyun":[{"url":"https://www.alipan.com/s/a"}],
   "123pan":[{"url":"https://www.123pan.com/s/a"}],
   "baidu":[{"url":"https://pan.baidu.com/s/a","password":" abcd "}],
   "unknown":[{"url":"https://pan.quark.cn/s/hidden"},{"url":"https://www.aliyundrive.com/s/hidden"},{"url":"https://www.123865.com/s/hidden"}],
   "moby":[{"url":"magnet:?xt=urn:btih:abc"}]
  }}}`)
	}))
	defer server.Close()
	pansouCfgMu.Lock()
	oldCfg, oldAt := pansouCfgV, pansouCfgAt
	pansouCfgV, pansouCfgAt = &pansouCfg{BaseURL: server.URL}, time.Now()
	pansouCfgMu.Unlock()
	t.Cleanup(func() {
		pansouCfgMu.Lock()
		pansouCfgV, pansouCfgAt = oldCfg, oldAt
		pansouCfgMu.Unlock()
	})
	items, err := pansouSearchItems("movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 retained, deduplicated results: %+v", items)
	}
	if items[0].URL != "https://115.com/s/new" || items[1].URL != "https://115.com/s/old" || items[0].Action != "transfer" {
		t.Fatalf("115 ordering/action: %+v", items)
	}
	if items[2].CloudType != "baidu" || items[2].Password != "abcd" || items[2].Action != "open" {
		t.Fatalf("retained provider: %+v", items[2])
	}
	if items[3].Action != "offline" {
		t.Fatalf("magnet action: %+v", items[3])
	}
}

// 自建实例现场：code=0 / success 却报「响应异常」。备注里没转义的换行、尾部多余内容、
// 某一类不是数组，都不该让整次搜索失败。
func TestPansouDecodeTolerant(t *testing.T) {
	body := "{\"code\":0,\"message\":\"success\",\"data\":{\"total\":2,\"merged_by_type\":{" +
		"\"quark\":[{\"url\":\"https://pan.quark.cn/s/x\",\"note\":\"a\nb\"}]," +
		"\"weird\":{\"url\":\"x\"}," +
		"\"115\":[{\"url\":\"https://115.com/s/ok\",\"note\":\"第一行\n\t第二行\"}]}}}\n{\"tail\":1}"
	out, err := pansouDecode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Data.MergedByType) != 3 {
		t.Fatalf("types: %v", out.Data.MergedByType)
	}
	if _, err := pansouDecode([]byte(`{"code":1,"message":"关键词为空"}`)); err == nil || !strings.Contains(err.Error(), "关键词为空") {
		t.Fatalf("code!=0: %v", err)
	}
	if _, err := pansouDecode([]byte(`{"code":0,"data":`)); err == nil || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("truncated: %v", err)
	}
}

func TestRetiredCloudLinksAreNotRecognized(t *testing.T) {
	for _, link := range []string{
		"https://pan.quark.cn/s/abc", "https://www.alipan.com/s/abc",
		"https://www.aliyundrive.com/s/abc", "https://www.123pan.com/s/abc",
		"https://www.123684.com/s/abc", "https://www.123865.com/s/abc", "https://www.123912.com/s/abc",
	} {
		if !removedCloudResource("unknown", link) {
			t.Errorf("search allowed retired link: %s", link)
		}
		if got := gyDetectPan(link); got != "" {
			t.Errorf("recognized retired provider %s: %s", got, link)
		}
		page := `<div class="tgme_widget_message_wrap"><div class="tgme_widget_message_text">movie<br><a href="` + link + `">download</a></div></div>`
		if got := tgParseChannel(page, "sample", "movie"); len(got) != 0 {
			t.Errorf("TG returned retired provider: %+v", got)
		}
	}
	for _, link := range []string{"https://115.com/s/abc", "https://pan.baidu.com/s/abc", "https://quark.cn.example.com/s/abc", "magnet:?xt=urn:btih:abc"} {
		if removedCloudResource("", link) {
			t.Errorf("blocked unrelated link: %s", link)
		}
	}
}
