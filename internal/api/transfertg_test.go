package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/model"
)

func TestTgResChannels(t *testing.T) {
	got := tgResChannels("@quanquan_115\nhttps://t.me/s/Share115?q=x\n  xyz ，@QuanQuan_115\n\n")
	if strings.Join(got, ",") != "quanquan_115,Share115,xyz" {
		t.Fatalf("频道解析: %v", got)
	}
}

// 一条消息挂几个链接就拆成几条资源；「名称：」前缀去掉；话题标签补画质；提取码只给 115 分享
func TestTgMessageResources(t *testing.T) {
	it := tgItem{
		Title:   "名称：名侦探柯南 第27季",
		Content: "大小：58.3GB 描述……",
		Channel: "quanquan_115",
		Date:    "2026-08-30 20:30",
		Tags:    []string{"4K", "杜比视界", "中字"},
		Pass:    "a8b9",
		Links: []tgLink{
			{URL: "https://115.com/s/swg9abc?password=a8b9#", Type: "115"},
			{URL: "magnet:?xt=urn:btih:ABCDEF0123456789", Type: "magnet"},
		},
	}
	seen := map[string]bool{"magnet:?xt=urn:btih:ABCDEF0123456789": true} // 别的频道已经出现过
	got := tgMessageResources(it, seen)
	if len(got) != 1 {
		t.Fatalf("已出现过的链接应当去重: %+v", got)
	}
	r := got[0]
	if r.Title != "名侦探柯南 第27季" {
		t.Errorf("标题前缀没去掉，或误删了片名首字: %q", r.Title)
	}
	if r.URL != "https://115.com/s/swg9abc?password=a8b9" || r.Kind != "share115" || r.Code != "a8b9" {
		t.Errorf("115 分享: %+v", r)
	}
	if r.Tags.Pix != "2160p" || !r.Tags.Zh || r.Tags.Season != "S27" {
		t.Errorf("标签: %+v", r.Tags)
	}
	if r.Size != "58.3GB" || r.Via != "@quanquan_115" || r.TimeUnix == 0 {
		t.Errorf("大小 / 频道 / 时间: %+v", r)
	}
}

// 走一遍完整来源流程：并发抓频道、部分失败写说明、已移除网盘过滤、片名比对
func TestSearchResourcesTg(t *testing.T) {
	newTestDB(t, "restg.db")
	model.DB.Create(&model.Setting{Key: "tgsearch", Value: `{"channels":"good_ch\nbad_ch"}`})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s/bad_ch" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("q") != "三体" {
			t.Errorf("应当用 TMDB 标题搜: %s", r.URL)
		}
		msg := func(id int, text string) string {
			return fmt.Sprintf(`<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="good_ch/%d">
<div class="tgme_widget_message_text js-message_text">%s</div><time datetime="2026-08-30T12:30:41+00:00"></time></div></div>`, id, text)
		}
		fmt.Fprint(w, "<html><body>"+
			msg(1, `三体 S01 2160p<br>链接：<a href="https://115cdn.com/s/tb1?password=x1y2">115</a> 备用 <a href="https://pan.quark.cn/s/q1">夸克</a>`)+
			msg(2, `流浪地球2 4K<br><a href="https://115.com/s/ll2">115</a>`)+
			"</body></html>")
	}))
	defer server.Close()
	old := tgWebBase
	tgWebBase = server.URL
	t.Cleanup(func() {
		tgWebBase = old
		resCacheMu.Lock()
		resCache = map[string]resCacheEntry{}
		resCacheMu.Unlock()
	})

	h := &Handler{DB: model.DB}
	if why := tgResReady(); why != "" {
		t.Fatalf("配了频道应当可用: %s", why)
	}
	r, err := searchResources(h, "tg", resQuery{TmdbID: 108545, Type: "tv", Title: "三体", Year: "2024"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 {
		t.Fatalf("夸克应当被过滤，剩两条: %+v", r.Items)
	}
	if !r.Items[0].Relevant || r.Items[0].Code != "x1y2" || r.Items[0].Via != "@good_ch" {
		t.Fatalf("三体那条: %+v", r.Items[0])
	}
	if r.Items[1].Relevant {
		t.Fatalf("流浪地球不是三体: %+v", r.Items[1])
	}
	if !strings.Contains(r.Note, "bad_ch") {
		t.Fatalf("抓失败的频道要写进说明: %q", r.Note)
	}

	// 全部频道都失败：报错并提示代理
	model.DB.Model(&model.Setting{}).Where("key = ?", "tgsearch").Update("value", `{"channels":"bad_ch"}`)
	if _, err := searchResources(h, "tg", resQuery{Title: "别的片"}, true); err == nil || !strings.Contains(err.Error(), "代理") {
		t.Fatalf("全部失败应当报错并提示代理: %v", err)
	}
}
