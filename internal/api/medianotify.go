package api

// ==================== 入库通知：聚合防抖 + Emby 封面优先 ====================
//
// 两级入库语义（借鉴 EmbyPulse）：
//   第一级：整理完成（StrmHub 移库成功）——TMDB 封面（AV 无封面走纯文本）
//   第二级：Emby 扫描入库完成（Webhook item.added）——封面/评分直接取自 Emby，
//           AV 条目因 Emby 刮削插件同样有封面
// 同级通知 15 秒防抖聚合（上限 120 秒强制发送）：多部影视合并为一条，
// 企微 news 多卡片（每部一张封面），TG 一图 + 汇总列表。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mediaNotifEntry 一条待聚合的入库通知
type mediaNotifEntry struct {
	Title      string
	Year       string
	Line       string // 一行描述（类型/分类/重命名/评分）
	PosterURL  string // 公网封面 URL（TMDB）
	PosterAlt  string // Emby 直链封面（内网，企微 picurl 可尝试）
	PosterData []byte // 封面字节（Emby 下载，TG 上传用）
	Link       string
}

var mediaNotif struct {
	mu      sync.Mutex
	items   []mediaNotifEntry
	timer   *time.Timer
	firstAt time.Time
}

// QueueMediaNotif 入队入库通知（15 秒防抖；累计超 105 秒立即冲刷）
func QueueMediaNotif(e mediaNotifEntry) {
	if e.Title == "" {
		return
	}
	mediaNotif.mu.Lock()
	defer mediaNotif.mu.Unlock()
	mediaNotif.items = append(mediaNotif.items, e)
	if mediaNotif.timer != nil {
		mediaNotif.timer.Stop()
	}
	if mediaNotif.firstAt.IsZero() {
		mediaNotif.firstAt = time.Now()
	}
	wait := 15 * time.Second
	if time.Since(mediaNotif.firstAt) > 105*time.Second {
		wait = 0
	}
	mediaNotif.timer = time.AfterFunc(wait, FlushMediaNotif)
}

// FlushMediaNotif 冲刷并发送（单条富格式 / 多条合并）
func FlushMediaNotif() {
	mediaNotif.mu.Lock()
	items := mediaNotif.items
	mediaNotif.items = nil
	mediaNotif.firstAt = time.Time{}
	mediaNotif.timer = nil
	mediaNotif.mu.Unlock()
	if len(items) == 0 {
		return
	}
	cfg, err := loadMessageConfig()
	if err != nil {
		// 配置读取失败不能静默吞掉整批：回灌队列并 60 秒后重试（此前直接
		// return，整理 100 部片恰逢读取失败 = 100 条通知无声消失）
		log.Printf("[通知] ✗ 消息配置读取失败，%d 条通知延迟 60 秒重试: %v", len(items), err)
		mediaNotif.mu.Lock()
		mediaNotif.items = append(items, mediaNotif.items...)
		mediaNotif.firstAt = time.Now()
		mediaNotif.timer = time.AfterFunc(60*time.Second, FlushMediaNotif)
		mediaNotif.mu.Unlock()
		return
	}
	if len(items) == 1 {
		e := items[0]
		sendMediaNotifSingle(cfg, e)
		return
	}
	sendMediaNotifBatch(cfg, items)
}

func sendMediaNotifSingle(cfg *MessageConfig, e mediaNotifEntry) {
	title := e.Title
	if e.Year != "" {
		title += "（" + e.Year + "）"
	}
	// 企微 news 卡片（封面转存企微图床，失败退回原 URL）
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go func() {
			pic := wecomPickPic(cfg.Wecom, e)
			_ = sendWecomNews(cfg.Wecom, title, e.Line, pic, e.Link)
		}()
	}
	// TG：有字节直接上传（内网 Emby 图 TG 服务器拉不到），否则 URL，再退文本
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		caption := title
		if e.Line != "" {
			caption += "\n" + e.Line
		}
		switch {
		case len(e.PosterData) > 0:
			go sendTelegramPhotoData(cfg.TG, caption, e.PosterData)
		case e.PosterURL != "":
			go sendTelegramPhoto(cfg.TG, title, e.Line, e.PosterURL)
		default:
			go sendTelegram(cfg.TG, caption)
		}
	}
	// 飞书 / QQ OneBot / QQ 官方：文本推送（标题+详情）
	sendExtraChannels(cfg, title, e.Line)
	log.Printf("[通知] 入库通知已发送: %s", title)
}

func sendMediaNotifBatch(cfg *MessageConfig, items []mediaNotifEntry) {
	title := fmt.Sprintf("🎬 本轮入库 %d 部", len(items))
	lines := make([]string, 0, len(items))
	for i, e := range items {
		l := fmt.Sprintf("%d. %s", i+1, e.Title)
		if e.Year != "" {
			l += "（" + e.Year + "）"
		}
		if e.Line != "" {
			l += " — " + e.Line
		}
		lines = append(lines, truncateStr(l, 120))
	}
	// 企微：news 多卡片（每部一张封面，最多 8 张）
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecomNewsMulti(cfg.Wecom, title, items)
	}
	// TG：首图 + 汇总列表（caption 截到 3800：sendMessage/caption 上限 4096，
	// 超限 TG 直接 400 整条丢失，此前无截断）
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		list := strings.Join(lines, "\n")
		if len(list) > 3800 {
			// 按 rune 截断：字节截断会把多字节中文劈成非法 UTF-8，TG 整条 400
			list = string([]rune(list)[:3800]) + "\n…（超长截断）"
		}
		caption := title + "\n" + list
		first := items[0]
		switch {
		case len(first.PosterData) > 0:
			go sendTelegramPhotoData(cfg.TG, caption, first.PosterData)
		case first.PosterURL != "":
			go sendTelegramPhoto(cfg.TG, title, list, first.PosterURL)
		default:
			go sendTelegram(cfg.TG, caption)
		}
	}
	// 飞书 / QQ OneBot / QQ 官方：文本汇总
	sendExtraChannels(cfg, title, strings.Join(lines, "\n"))
	log.Printf("[通知] 聚合入库通知已发送: %d 部", len(items))
}

// pickPicURL 选企微 news 卡片的封面图。PosterAlt 是 Emby 内网封面直链，
// 通常带 ?api_key=… 鉴权参数——picurl 会被企微/Telegram 的服务器抓取，
// 带密钥的 URL 等于把 Emby api_key 交给第三方（密钥泄露），必须剥掉
// 查询串再给（无密钥的内网地址对方拉不到图会自然回退纯文本，无害）
func pickPicURL(e mediaNotifEntry) string {
	if e.PosterURL != "" {
		return e.PosterURL
	}
	if e.PosterAlt != "" {
		if i := strings.Index(e.PosterAlt, "?"); i > 0 {
			return e.PosterAlt[:i]
		}
		return e.PosterAlt
	}
	return ""
}

// wecomUploadImg 图片转存企微图床（uploadimg 接口）：源站封面常有防盗链，
// 企微服务器直接拉 picurl 会失败；先传到企微图床拿公网 URL 再上卡片必然显示
func wecomUploadImg(cfg WecomConfig, data []byte) (string, error) {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("media", "cover.jpg")
	if err == nil {
		_, err = fw.Write(data)
	}
	if err2 := w.Close(); err == nil {
		err = err2
	}
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(fmt.Sprintf("%s/cgi-bin/media/uploadimg?access_token=%s",
		cfg.apiBase(), token), w.FormDataContentType(), &buf)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(body, &r)
	if r.ErrCode != 0 || r.URL == "" {
		return "", fmt.Errorf("uploadimg 失败: %d %s", r.ErrCode, r.ErrMsg)
	}
	return r.URL, nil
}

// wecomPickPic 封面优先转存企微图床（有字节时），失败退回原 URL
func wecomPickPic(cfg WecomConfig, e mediaNotifEntry) string {
	if len(e.PosterData) > 0 {
		if u, err := wecomUploadImg(cfg, e.PosterData); err == nil {
			return u
		}
	}
	return pickPicURL(e)
}

// sendWecomNewsMulti 企微多卡片图文（每部一张封面，news 最多 8 条 article）
func sendWecomNewsMulti(cfg WecomConfig, title string, items []mediaNotifEntry) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	articles := []map[string]string{}
	for _, e := range items[:min(len(items), 8)] {
		t := e.Title
		if e.Year != "" {
			t += "（" + e.Year + "）"
		}
		link := e.Link
		if link == "" {
			link = "https://github.com/DaisyYijin/STRMhub"
		}
		articles = append(articles, map[string]string{
			"title":       truncateStr(t, 90),
			"description": truncateStr(e.Line, 200),
			"picurl":      wecomPickPic(cfg, e),
			"url":         link,
		})
	}
	payload := map[string]interface{}{
		"touser":  "@all",
		"msgtype": "news",
		"agentid": cfg.AgentID,
		"news":    map[string]interface{}{"articles": articles},
	}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", cfg.apiBase(), token)
	req, _ := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return sendWecom(cfg, title+"\n"+strings.Join(lineListOf(items), "\n"))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(body, &r)
	if r.ErrCode != 0 {
		log.Printf("企业微信多卡片发送失败: %d %s（退回纯文本）", r.ErrCode, r.ErrMsg)
		return sendWecom(cfg, title+"\n"+strings.Join(lineListOf(items), "\n"))
	}
	log.Printf("企业微信多卡片发送成功: %d 部", len(items))
	return nil
}

func lineListOf(items []mediaNotifEntry) []string {
	lines := make([]string, 0, len(items))
	for _, e := range items {
		l := e.Title
		if e.Year != "" {
			l += "（" + e.Year + "）"
		}
		lines = append(lines, l)
	}
	return lines
}

// sendTelegramPhotoData 以文件上传方式发 TG 图片（内网图片字节直传，
// 解决 TG 服务器拉不到内网 URL 的问题）；失败退回纯文本
func sendTelegramPhotoData(cfg TGConfig, caption string, photo []byte) error {
	client := &http.Client{Timeout: 20 * time.Second}
	if proxyURL := getProxyURL(); proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", cfg.ChatID)
	_ = w.WriteField("caption", truncateStr(caption, 1000))
	fw, err := w.CreateFormFile("photo", "poster.jpg")
	if err == nil {
		_, err = fw.Write(photo)
	}
	w.Close()
	if err != nil {
		return sendTelegram(cfg, caption)
	}
	resp, err := client.Post(fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", cfg.Token), w.FormDataContentType(), &buf)
	if err != nil {
		log.Printf("Telegram 图片上传失败（退回文本）: %v", err)
		return sendTelegram(cfg, caption)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram 图片上传失败: HTTP %d %s（退回文本）", resp.StatusCode, truncateStr(string(body), 120))
		return sendTelegram(cfg, caption)
	}
	log.Printf("Telegram 图片上传成功")
	return nil
}

// fetchHTTPBytes 下载图片字节（用于把 Emby 内网封面直传 TG）
func fetchHTTPBytes(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 封面最多 8MB
	if err != nil {
		return nil, err
	}
	return data, nil
}
