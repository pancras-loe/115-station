package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// notifyConfigSource 配置读取源（SetupRoutes 注入）。
// 配置保存在 YAML（Config.SaveSetting），数据库 Setting 表仅旧版本使用；
// 包级函数无法拿 Handler，这里用注入的 Config 走"YAML 优先、DB 回退"读取。
var notifyConfigSource *config.Config

// settingValueCompat 读配置：YAML 优先，数据库回退（兼容旧数据）。
// 配置由前端 saveConfig → Config.SaveSetting 写入 YAML；只读 DB 的旧写法永远读不到。
func settingValueCompat(key string) string {
	if notifyConfigSource != nil {
		if v := notifyConfigSource.GetSetting(key); v != "" {
			return v
		}
	}
	if model.DB != nil {
		var s model.Setting
		if err := model.DB.Where("key = ?", key).First(&s).Error; err == nil {
			return s.Value
		}
	}
	return ""
}

// ==================== 消息通知 ====================

// MessageConfig 消息通知配置
type MessageConfig struct {
	Wecom      WecomConfig      `json:"wecom"`
	TG         TGConfig         `json:"tg"`
	Feishu     FeishuConfig     `json:"feishu"`
	QQOneBot   QQOneBotConfig   `json:"qq_onebot"`
	QQOfficial QQOfficialConfig `json:"qq_official"`
}

// FeishuConfig 飞书群自定义机器人（webhook 推送，可选签名）
type FeishuConfig struct {
	Enabled any    `json:"enabled"`
	Webhook string `json:"webhook"`
	Secret  string `json:"secret"`
}

func (c *FeishuConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// QQOneBotConfig QQ OneBot HTTP 适配（NapCat / Lagrange / LLOneBot 等）
type QQOneBotConfig struct {
	Enabled    any    `json:"enabled"`
	URL        string `json:"url"`         // OneBot HTTP 服务地址，如 http://127.0.0.1:3000
	Token      string `json:"token"`       // access_token（与服务端 access_token 配置一致）
	TargetType string `json:"target_type"` // group / private
	Target     string `json:"target"`      // 群号或 QQ 号
	Admin      string `json:"admin"`       // 管理 QQ（私聊指令触发任务用）
	EventToken string `json:"event_token"` // 事件回调地址的鉴权 token
}

func (c *QQOneBotConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// QQOfficialConfig QQ 官方机器人（q.qq.com，appid+secret）
type QQOfficialConfig struct {
	Enabled   any    `json:"enabled"`
	AppID     string `json:"app_id"`
	Secret    string `json:"secret"`
	GroupID   string `json:"group_id"`    // 群 ID（group_openid）
	LastMsgID string `json:"last_msg_id"` // 最近收到的消息 id（被动回复用，可选）
}

func (c *QQOfficialConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// WecomConfig 企业微信配置（Token/EncodingAESKey 为机器人回调验签解密用）
type WecomConfig struct {
	CorpID         string `json:"corp_id"`
	Secret         string `json:"secret"`
	AgentID        string `json:"agent_id"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encoding_aes_key"`
	APIURL         string `json:"api_url"`
	Enabled        any    `json:"enabled"`
}

// apiBase 企微 API 地址（海外部署可配反代；默认官方地址）
func (c *WecomConfig) apiBase() string {
	base := strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	if base == "" {
		base = "https://qyapi.weixin.qq.com"
	}
	return base
}

// TGConfig Telegram 配置
type TGConfig struct {
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
	Enabled any    `json:"enabled"`
}

// msgCfgCache 消息配置短缓存：批量入库时每条通知都全量读盘解析 YAML
// 太浪费；5 秒 TTL，保存配置时主动失效（SaveSetting 钩子调用）
var msgCfgCache struct {
	sync.Mutex
	cfg *MessageConfig
	err error
	at  time.Time
}

// invalidateMsgCfgCache 消息配置保存后调用
func invalidateMsgCfgCache() {
	msgCfgCache.Lock()
	msgCfgCache.cfg, msgCfgCache.err, msgCfgCache.at = nil, nil, time.Time{}
	msgCfgCache.Unlock()
}

// loadMessageConfig 加载消息通知配置（YAML 优先/DB 回退，5 秒缓存）
func loadMessageConfig() (*MessageConfig, error) {
	msgCfgCache.Lock()
	cached, cacheAt := msgCfgCache.cfg, msgCfgCache.at
	msgCfgCache.Unlock()
	if cached != nil && time.Since(cacheAt) < 5*time.Second {
		return cached, nil
	}
	raw := settingValueCompat("message")
	var cfg *MessageConfig
	var err error
	if raw == "" {
		err = fmt.Errorf("尚未保存过消息配置：请到「消息配置」页填写并点击保存配置")
	} else {
		c := &MessageConfig{}
		if jerr := json.Unmarshal([]byte(raw), c); jerr != nil {
			err = fmt.Errorf("解析消息通知配置失败")
		} else {
			cfg = c
		}
	}
	if cfg != nil {
		msgCfgCache.Lock()
		msgCfgCache.cfg, msgCfgCache.at = cfg, time.Now()
		msgCfgCache.Unlock()
	}
	return cfg, err
}

// wecomTokenCache 企微 access_token 缓存（有效期约 7200 秒，提前 10 分钟刷新；
// 此前每条消息独立 gettoken，批量通知易撞企微 gettoken 频控）
var wecomTokenCache struct {
	sync.Mutex
	corpID string
	secret string
	token  string
	expire time.Time
}

func wecomTokenCached(cfg WecomConfig) (string, bool) {
	wecomTokenCache.Lock()
	defer wecomTokenCache.Unlock()
	if wecomTokenCache.token != "" && cfg.CorpID == wecomTokenCache.corpID &&
		cfg.Secret == wecomTokenCache.secret && time.Now().Before(wecomTokenCache.expire) {
		return wecomTokenCache.token, true
	}
	return "", false
}

func wecomTokenPut(cfg WecomConfig, token string, expiresIn int) {
	wecomTokenCache.Lock()
	defer wecomTokenCache.Unlock()
	wecomTokenCache.corpID, wecomTokenCache.secret = cfg.CorpID, cfg.Secret
	wecomTokenCache.token = token
	wecomTokenCache.expire = time.Now().Add(time.Duration(expiresIn)*time.Second - 10*time.Minute)
}

func (c *WecomConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func (c *TGConfig) isEnabled() bool {
	switch v := c.Enabled.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// NotifyMessage 推送消息到所有启用的通知渠道
func NotifyMessage(title, content string) {
	cfg, err := loadMessageConfig()
	if err != nil {
		log.Printf("[通知] ✗ 配置不可用，消息未发送（%s）: %v", title, err)
		return
	}

	fullMsg := title
	if content != "" && title != "" {
		fullMsg = fmt.Sprintf("%s\n%s", title, content)
	} else if content != "" {
		fullMsg = content
	}

	// 企业微信
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecom(cfg.Wecom, fullMsg)
	}

	// Telegram
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go sendTelegram(cfg.TG, fullMsg)
	}

	// 飞书 / QQ OneBot / QQ 官方机器人
	sendExtraChannels(cfg, title, content)
}

// NotifyMessageRich 富媒体通知：带封面图与链接。
// Telegram 直接发图片（sendPhoto）；企业微信应用消息发 news 图文卡片（picurl 外链封面）。
// posterURL 为空时自动退回纯文本通知。
func NotifyMessageRich(title, content, posterURL, linkURL string) {
	cfg, err := loadMessageConfig()
	if err != nil {
		log.Printf("[通知] ✗ 配置不可用，富通知未发送（%s）: %v", title, err)
		return
	}
	if posterURL == "" {
		NotifyMessage(title, content)
		return
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecomNews(cfg.Wecom, title, content, posterURL, linkURL)
	}
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go sendTelegramPhoto(cfg.TG, title, content, posterURL)
	}
}

// NewsArticle 图文卡片单篇
type NewsArticle struct {
	Title  string
	Desc   string
	PicURL string
	Link   string
}

// NotifyMessageNews 多篇图文消息（企微 news 最多 8 篇）。
// 返回是否已按图文发送（企微未配置时返回 false，调用方可回退纯文本）
func NotifyMessageNews(articles []NewsArticle) bool {
	if len(articles) == 0 {
		return false
	}
	cfg, err := loadMessageConfig()
	if err != nil {
		return false
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		if len(articles) > 8 {
			articles = articles[:8]
		}
		go sendWecomNewsArticles(cfg.Wecom, articles)
		return true
	}
	return false
}

// sendWecomNewsMulti 一条 news 消息带多篇图文
func sendWecomNewsArticles(cfg WecomConfig, articles []NewsArticle) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	list := make([]map[string]string, 0, len(articles))
	for _, a := range articles {
		link := a.Link
		if link == "" {
			link = "https://github.com/DaisyYijin/STRMhub"
		}
		list = append(list, map[string]string{
			"title":       truncateStr(a.Title, 90),
			"description": truncateStr(a.Desc, 200),
			"picurl":      a.PicURL,
			"url":         link,
		})
	}
	payload := map[string]interface{}{
		"touser":  "@all",
		"msgtype": "news",
		"agentid": cfg.AgentID,
		"news":    map[string]interface{}{"articles": list},
	}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 20 * time.Second}
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", cfg.apiBase(), token)
	req, err := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// sanitizeWecomErr 企微错误脱敏：URL 里的 access_token / corpsecret 打码后再进日志
func sanitizeWecomErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = regexp.MustCompile(`access_token=[^&\s]+`).ReplaceAllString(msg, "access_token=***")
	msg = regexp.MustCompile(`corpsecret=[^&\s]+`).ReplaceAllString(msg, "corpsecret=***")
	return msg
}

// wecomAccessToken 获取企业微信 access_token（带缓存）
func wecomAccessToken(cfg WecomConfig) (string, error) {
	if t, ok := wecomTokenCached(cfg); ok {
		return t, nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	tokenURL := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		cfg.apiBase(), cfg.CorpID, cfg.Secret)
	resp, err := client.Get(tokenURL)
	if err != nil {
		log.Printf("企业微信获取 token 失败: %s", sanitizeWecomErr(err))
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tokenResult struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResult); err != nil {
		return "", err
	}
	if tokenResult.ErrCode != 0 {
		log.Printf("企业微信获取 token 失败: %d %s", tokenResult.ErrCode, tokenResult.ErrMsg)
		return "", fmt.Errorf("%s", tokenResult.ErrMsg)
	}
	if tokenResult.AccessToken != "" {
		wecomTokenPut(cfg, tokenResult.AccessToken, tokenResult.ExpiresIn)
	}
	return tokenResult.AccessToken, nil
}

// sendWecomNews 发送企业微信图文卡片（封面 + 标题 + 描述 + 链接）
func sendWecomNews(cfg WecomConfig, title, desc, picurl, linkURL string) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	if linkURL == "" {
		linkURL = "https://github.com/DaisyYijin/STRMhub"
	}
	payload := map[string]interface{}{
		"touser":  "@all",
		"msgtype": "news",
		"agentid": cfg.AgentID,
		"news": map[string]interface{}{
			"articles": []map[string]string{{
				"title":       truncateStr(title, 90),
				"description": truncateStr(desc, 300),
				"picurl":      picurl,
				"url":         linkURL,
			}},
		},
	}
	payloadBytes, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 20 * time.Second}
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s", cfg.apiBase(), token)
	req, err := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("企业微信图文发送失败: %s", sanitizeWecomErr(err))
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(body, &r)
	if r.ErrCode != 0 {
		log.Printf("企业微信图文发送失败: %d %s（退回纯文本）", r.ErrCode, r.ErrMsg)
		// 图文被拒（如 picurl 不可达）时退回纯文本，保证消息不丢
		return sendWecom(cfg, title+"\n"+desc)
	}
	return nil
}

// sendTelegramPhoto 发送 Telegram 图片（caption 带正文；失败退回文本）
func sendTelegramPhoto(cfg TGConfig, title, content, photoURL string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	if proxyURL := getProxyURL(); proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}
	caption := title
	if content != "" {
		caption += "\n" + content
	}
	form := url.Values{
		"chat_id": {cfg.ChatID},
		"photo":   {photoURL},
		"caption": {truncateStr(caption, 1000)},
	}
	resp, err := client.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", cfg.Token), form)
	if err != nil {
		log.Printf("Telegram 发送图片失败（退回文本）: %v", err)
		return sendTelegram(cfg, caption)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram 发送图片失败: HTTP %d %s（退回文本）", resp.StatusCode, truncateStr(string(body), 120))
		return sendTelegram(cfg, caption)
	}
	return nil
}

// sendWecom 发送企业微信应用消息
func sendWecom(cfg WecomConfig, msg string) error {
	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}

	// Step 2: 发送消息（超长自动分段：text 消息上限 2048 字节）
	chunks := splitWecomText(msg)
	sendURL := fmt.Sprintf("%s/cgi-bin/message/send?access_token=%s",
		cfg.apiBase(), token)

	for i, chunk := range chunks {
		payload := map[string]interface{}{
			"touser":  "@all",
			"msgtype": "text",
			"agentid": cfg.AgentID,
			"text":    map[string]string{"content": chunk},
		}
		payloadBytes, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", sendURL, strings.NewReader(string(payloadBytes)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp2, err := client.Do(req)
		if err != nil {
			// 传输层错误（超时/连接重置）自动重试一次，避免网络抖动丢消息
			time.Sleep(2 * time.Second)
			resp2, err = client.Do(req)
			if err != nil {
				log.Printf("企业微信发送消息失败(重试后仍失败): %s", sanitizeWecomErr(err))
				return err
			}
		}
		body2, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()

		var sendResult struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		json.Unmarshal(body2, &sendResult)
		if sendResult.ErrCode != 0 {
			log.Printf("企业微信发送消息失败: %d %s", sendResult.ErrCode, sendResult.ErrMsg)
			return fmt.Errorf("%s", sendResult.ErrMsg)
		}
		if i < len(chunks)-1 {
			time.Sleep(400 * time.Millisecond) // 分段间隔，避免触发频控
		}
	}

	return nil
}

// splitWecomText 企微 text 消息上限 2048 字节（UTF-8），按行边界切成 ≤1800
// 字节的段；单行超限（超长标题等）在 rune 边界硬切
func splitWecomText(msg string) []string {
	const maxBytes = 1800
	if len(msg) <= maxBytes {
		return []string{msg}
	}
	var out []string
	var cur []string
	curLen := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = cur[:0]
			curLen = 0
		}
	}
	for _, ln := range strings.Split(msg, "\n") {
		for len(ln) > maxBytes {
			cut := maxBytes
			for cut > 0 && !utf8.RuneStart(ln[cut]) {
				cut--
			}
			flush()
			out = append(out, ln[:cut])
			ln = ln[cut:]
		}
		if curLen+len(ln)+1 > maxBytes && len(cur) > 0 {
			flush()
		}
		cur = append(cur, ln)
		curLen += len(ln) + 1
	}
	flush()
	if len(out) == 0 {
		out = []string{msg}
	}
	return out
}

// sendTelegram 发送 Telegram 消息
func sendTelegram(cfg TGConfig, msg string) error {
	client := &http.Client{Timeout: 20 * time.Second}

	sendURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Token)

	payload := fmt.Sprintf(`{"chat_id":"%s","text":%s,"parse_mode":"HTML"}`,
		cfg.ChatID, mustJSON(msg))

	req, err := http.NewRequest("POST", sendURL, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// 通过代理发送（如果配置了代理）
	proxyURL := getProxyURL()
	if proxyURL != "" {
		if pu, err := parseProxyURL(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: pu}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Telegram 发送消息失败: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Telegram 发送消息失败: HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return nil
}

// TestMessage 测试消息通知
// POST /message/test
func (h *Handler) TestMessage(c *gin.Context) {
	cfg, err := loadMessageConfig()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": err.Error()})
		return
	}

	testMsg := "StrmHub 消息通知测试"
	successCount := 0
	errorMsg := ""

	// 渠道状态预检：给出比"发送失败"更明确的指引
	if !cfg.Wecom.isEnabled() && !cfg.TG.isEnabled() && !extraChannelsEnabled(cfg) {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "所有通知渠道都是禁用状态：请启用对应渠道的「状态」开关后再保存并测试"})
		return
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "企业微信已启用但「企业 ID」为空：请填写后再保存并测试"})
		return
	}
	if cfg.TG.isEnabled() && cfg.TG.Token == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "TG 已启用但 Bot Token 为空：请填写后再保存并测试"})
		return
	}

	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" {
		if err := sendWecom(cfg.Wecom, testMsg); err != nil {
			errorMsg += "企业微信: " + err.Error() + "; "
		} else {
			successCount++
		}
	}

	if cfg.TG.isEnabled() && cfg.TG.Token != "" {
		if err := sendTelegram(cfg.TG, testMsg); err != nil {
			errorMsg += "Telegram: " + err.Error()
		} else {
			successCount++
		}
	}

	// 飞书 / QQ OneBot / QQ 官方（同步测试，错误逐渠道汇报）
	if em, n := testExtraChannels(cfg, testMsg); em != "" {
		errorMsg += em
	} else {
		successCount += n
	}

	if successCount > 0 {
		c.JSON(http.StatusOK, gin.H{"success": true})
	} else {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "未启用任何通知渠道或发送失败: " + errorMsg})
	}
}

// getProxyURL 从数据库读取代理配置
func getProxyURL() string {
	var cfg struct {
		URL string `json:"url"`
	}
	json.Unmarshal([]byte(settingValueCompat("proxy")), &cfg)
	return cfg.URL
}

func parseProxyURL(proxyURL string) (func(*http.Request) (*url.URL, error), error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	return http.ProxyURL(u), nil
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// firstLine 取文本首行（日志摘要用，避免多行长消息刷屏）
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
