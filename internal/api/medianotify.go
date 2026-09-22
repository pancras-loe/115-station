package api

// ==================== 入库通知：一部影视一张卡片 ====================
//
// 一次入库有两个来源：
//   第一级：整理完成（115-Station 移库成功）——TMDB 封面、画质、文件数、集数
//   第二级：Emby 扫描入库完成（Webhook library.new）——Emby 封面、评分、详情链接
// **两级合成同一张卡片**：按媒体键合并（mergeKey），谁先到谁建卡，后到的补字段。
// 各发各的时候一次入库要推两条几乎一样的消息，这是最招人烦的地方。
//
// 15 秒防抖（上限 120 秒强制发送）：同一轮的多部影视合并成一条，
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

// mediaNotifEntry 一部影视的入库卡片。
// 字段是结构化的而不是拼好的文本：两级来源各填各的那几项，
// 合并时按字段取舍，渲染统一在 lines() 里做
type mediaNotifEntry struct {
	Title      string
	Year       string
	Kind       string  // 电影 / 剧集
	Source     string  // organize / emby；已发送后的延迟 webhook 去重用
	Category   string  // 二级分类（整理侧）
	Rating     float64 // 评分（Emby 侧）
	Quality    string  // 画质：2160P HDR BLURAY（整理侧）
	Files      string  // 1 个文件 · 27.4 GB（整理侧）
	Episodes   string  // E01-E12（全）（整理侧）
	Notes      []string
	PosterURL  string // 公网封面 URL（TMDB）
	PosterAlt  string // Emby 直链封面（内网，企微 picurl 可尝试）
	PosterData []byte // 封面字节（Emby 下载，TG 上传用）
	Link       string
}

// mergeKey 同一部影视的合并键。
// 剧集不带年份：整理侧拿的是首播年，Emby 单集事件给的是这一集的年份，
// 带上年份反而合不到一起
func (e mediaNotifEntry) mergeKey() string {
	t := strings.ToLower(strings.TrimSpace(e.Title))
	if t == "" {
		return ""
	}
	if e.Kind == "剧集" {
		return "tv:" + t
	}
	return "mv:" + t + "|" + e.Year
}

// headline 卡片标题
func (e mediaNotifEntry) headline() string {
	if e.Year != "" {
		return e.Title + "（" + e.Year + "）"
	}
	return e.Title
}

// lines 卡片正文。空字段不占行，别让卡片里留一堆「未知」
func (e mediaNotifEntry) lines() []string {
	var out []string
	head := []string{}
	if e.Kind != "" {
		icon := "🎬"
		if e.Kind == "剧集" {
			icon = "📺"
		}
		head = append(head, icon+" "+e.Kind)
	}
	if e.Category != "" && e.Category != e.Kind {
		head = append(head, e.Category)
	}
	if e.Rating > 0 {
		head = append(head, fmt.Sprintf("⭐ %.1f", e.Rating))
	}
	if len(head) > 0 {
		out = append(out, strings.Join(head, " · "))
	}
	if e.Quality != "" {
		out = append(out, "📀 "+e.Quality)
	}
	if e.Episodes != "" {
		out = append(out, "🎞 "+e.Episodes)
	}
	if e.Files != "" {
		out = append(out, "📦 "+e.Files)
	}
	return append(out, e.Notes...)
}

// body 正文文本
func (e mediaNotifEntry) body() string { return strings.Join(e.lines(), "\n") }

// summary 聚合列表里的一行简述
func (e mediaNotifEntry) summary() string {
	parts := []string{}
	if e.Kind != "" {
		parts = append(parts, e.Kind)
	}
	if e.Quality != "" {
		parts = append(parts, e.Quality)
	}
	if e.Episodes != "" {
		parts = append(parts, e.Episodes)
	}
	if e.Rating > 0 {
		parts = append(parts, fmt.Sprintf("⭐ %.1f", e.Rating))
	}
	return strings.Join(parts, " · ")
}

// mergeFrom 把另一条来源补进这张卡片：已有的字段不覆盖，
// 封面与详情链接偏向 Emby（评分、海报都是刮削完的成品）
func (e *mediaNotifEntry) mergeFrom(o mediaNotifEntry) {
	if e.Year == "" {
		e.Year = o.Year
	}
	if e.Kind == "" {
		e.Kind = o.Kind
	}
	if e.Category == "" {
		e.Category = o.Category
	}
	if e.Rating == 0 {
		e.Rating = o.Rating
	}
	if e.Quality == "" {
		e.Quality = o.Quality
	}
	if e.Files == "" {
		e.Files = o.Files
	}
	if e.Episodes == "" {
		e.Episodes = o.Episodes
	}
	if len(o.PosterData) > 0 {
		e.PosterData = o.PosterData
	}
	if o.PosterAlt != "" {
		e.PosterAlt = o.PosterAlt
	}
	if e.PosterURL == "" {
		e.PosterURL = o.PosterURL
	}
	if o.Link != "" {
		e.Link = o.Link
	}
	for _, n := range o.Notes {
		if !containsStr(e.Notes, n) {
			e.Notes = append(e.Notes, n)
		}
	}
}

var mediaNotif struct {
	mu      sync.Mutex
	items   []mediaNotifEntry
	timer   *time.Timer
	firstAt time.Time
	sent    map[string]time.Time // 已经发出的媒体键；只拦截随后迟到的 Emby 回声
}

const mediaNotifSentWindow = 10 * time.Minute

// QueueMediaNotif 入队入库通知（15 秒防抖；累计超 105 秒立即冲刷）
func QueueMediaNotif(e mediaNotifEntry) {
	if e.Title == "" {
		return
	}
	mediaNotif.mu.Lock()
	defer mediaNotif.mu.Unlock()
	now := time.Now()
	for key, at := range mediaNotif.sent {
		if now.Sub(at) > mediaNotifSentWindow {
			delete(mediaNotif.sent, key)
		}
	}
	key := e.mergeKey()
	// 整理卡片通常先发，Emby 扫描完成可能几十秒乃至几分钟后才回 webhook。
	// 队列内合并管不到已经发出的卡片，因此只把迟到的 Emby 回声拦掉；新的整理动作
	// 仍允许再次通知，避免同一部剧稍后追加新集时被十分钟窗口误吞。
	if e.Source == "emby" && key != "" {
		if at, ok := mediaNotif.sent[key]; ok && now.Sub(at) <= mediaNotifSentWindow {
			vlog("[通知] ○ 跳过已发送片目的延迟 Emby 入库事件: %s", e.headline())
			return
		}
	}
	// 同一部影视的第二个来源（整理 / Emby 扫描）并进已有卡片，不新开一条
	merged := false
	if key != "" {
		for i := range mediaNotif.items {
			if mediaNotif.items[i].mergeKey() == key {
				mediaNotif.items[i].mergeFrom(e)
				merged = true
				break
			}
		}
	}
	if !merged {
		mediaNotif.items = append(mediaNotif.items, e)
	}
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
	// 配置读取成功才算进入发送阶段。先登记再发，避免发送期间又到一条相同 webhook
	// 穿过窗口；各通知通道沿用既有的异步发送与失败日志。
	mediaNotif.mu.Lock()
	if mediaNotif.sent == nil {
		mediaNotif.sent = map[string]time.Time{}
	}
	now := time.Now()
	for _, e := range items {
		if key := e.mergeKey(); key != "" {
			mediaNotif.sent[key] = now
		}
	}
	mediaNotif.mu.Unlock()
	if len(items) == 1 {
		e := items[0]
		sendMediaNotifSingle(cfg, e)
		return
	}
	sendMediaNotifBatch(cfg, items)
}

func sendMediaNotifSingle(cfg *MessageConfig, e mediaNotifEntry) {
	// 标题就是片名：正文第一行已经带了类型图标，再加一个只是重复
	title := e.headline()
	body := e.body()
	// 企微 news 卡片（封面转存企微图床，失败退回原 URL）
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go func() {
			pic := wecomPickPic(cfg.Wecom, e)
			_ = sendWecomNews(cfg.Wecom, e.headline(), body, pic, e.Link)
		}()
	}
	// TG：有字节直接上传（内网 Emby 图 TG 服务器拉不到），否则 URL，再退文本
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		caption := title
		if body != "" {
			caption += "\n" + body
		}
		switch {
		case len(e.PosterData) > 0:
			go sendTelegramPhotoData(cfg.TG, caption, e.PosterData)
		case e.PosterURL != "":
			go sendTelegramPhoto(cfg.TG, title, body, e.PosterURL)
		default:
			go sendTelegram(cfg.TG, caption)
		}
	}
	// 飞书 / QQ OneBot / QQ 官方：文本推送（标题+详情）
	sendExtraChannels(cfg, title, body)
	log.Printf("[通知] 入库通知已发送: %s", e.headline())
}

func sendMediaNotifBatch(cfg *MessageConfig, items []mediaNotifEntry) {
	title := fmt.Sprintf("🎬 本轮入库 %d 部", len(items))
	lines := make([]string, 0, len(items))
	for i, e := range items {
		l := fmt.Sprintf("%d. %s", i+1, e.headline())
		if s := e.summary(); s != "" {
			l += " — " + s
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
		link := e.Link
		if link == "" {
			link = wecomCardFallbackLink()
		}
		articles = append(articles, map[string]string{
			"title":       truncateStr(e.headline(), 90),
			"description": truncateStr(e.body(), 200),
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
		lines = append(lines, e.headline())
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
