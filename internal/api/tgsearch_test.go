package api

import "strings"
import "testing"

const tgSampleHTML = `<html><body>
<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="quanquan_115/12345">
<div class="tgme_widget_message_bubble">
<div class="tgme_widget_message_text js-message_text">沙丘3 4K <b>合集</b><br>描述：维伦纽瓦科幻续作<br>链接：<a href="https://115.com/s/swg9abc?password=a8b9#">115网盘</a> 备用 <a href="https://pan.quark.cn/s/abc12def">夸克</a><br>#沙丘 #科幻</div>
<time datetime="2026-08-30T12:30:41+00:00"></time>
<div class="tgme_widget_message_photo_wrap" style="background-image:url('https://cdn.tg.example/photo1.jpg')"></div>
</div></div></div>
<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="other_ch/99">
<div class="tgme_widget_message_text js-message_text">无链接的普通消息</div>
<time datetime="2026-08-29T10:00:00+00:00"></time>
</div></div>
</body></html>`

func TestTgParseChannel(t *testing.T) {
	items := tgParseChannel(tgSampleHTML, "quanquan_115", "沙丘")
	if len(items) != 1 {
		t.Fatalf("期望解析出 1 条带网盘链接的消息，实际 %d", len(items))
	}
	it := items[0]
	if it.Title == "" || !strings.Contains(it.Title, "沙丘3") {
		t.Errorf("标题解析异常: %q", it.Title)
	}
	if !strings.Contains(it.Content, "维伦纽瓦科幻续作") {
		t.Errorf("内容解析异常: %q", it.Content)
	}
	if it.Channel != "quanquan_115" || !strings.HasPrefix(it.Date, "2026-08-30") {
		t.Errorf("频道/日期异常: %q %q", it.Channel, it.Date)
	}
	if it.Image == "" || !strings.Contains(it.Image, "photo1") {
		t.Errorf("图片解析异常: %q", it.Image)
	}
	var has115, hasQuark bool
	for _, l := range it.Links {
		switch l.Type {
		case "115":
			has115 = strings.HasPrefix(l.URL, "https://115.com/s/swg9abc")
		case "quark":
			hasQuark = true
		}
	}
	if !has115 || hasQuark || len(it.Links) != 1 {
		t.Errorf("链接解析不完整: %+v", it.Links)
	}
	if it.Main.Type != "115" {
		t.Errorf("主链接应为 115 优先: %+v", it.Main)
	}
	if it.Pass != "a8b9" {
		t.Errorf("提取码解析异常: %q", it.Pass)
	}
	if len(it.Tags) == 0 || it.Tags[0] != "沙丘" {
		t.Errorf("标签解析异常: %v", it.Tags)
	}
}

func TestTgFilterDedup(t *testing.T) {
	items := []tgItem{
		{Title: "沙丘3 4K", Main: tgLink{URL: "https://115.com/s/aaa", Type: "115"}},
		{Title: "完全无关的内容", Main: tgLink{URL: "https://115.com/s/bbb", Type: "115"}},
		{Title: "沙丘合集", Main: tgLink{URL: "https://115.com/s/aaa", Type: "115"}}, // 与第一条同链接
	}
	got := tgFilterDedup(items, "沙丘")
	if len(got) != 1 {
		t.Errorf("关键词过滤+去重后应剩 1 条，实际 %d: %+v", len(got), got)
	}
}
