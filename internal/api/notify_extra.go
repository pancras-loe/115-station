package api

// ==================== 扩展通知渠道：飞书 / QQ OneBot / QQ 官方机器人 ====================
//
// 飞书：群自定义机器人 webhook（POST JSON，可选签名校验），通知场景够用
// QQ OneBot：NapCat/Lagrange/LLOneBot 等实现暴露的 HTTP API（send_msg），
//            事件通过 POST /onebot/event 回调（仅私聊管理 QQ 视为指令）
// QQ 官方机器人：q.qq.com 的 Bot API（appid+secret → access_token → 群消息），
//            群消息需 @机器人 或带 msg_id 被动回复，限制多，仅做通知推送

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 通用发送 ====================

// postJSON 小工具：POST JSON 并解析响应体
func postJSON(client *http.Client, url string, payload interface{}) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// ==================== 飞书（群自定义机器人） ====================

// feishuEnabled 飞书是否启用（webhook 已填）
func feishuEnabled(cfg FeishuConfig) bool {
	return cfg.isEnabled() && strings.TrimSpace(cfg.Webhook) != ""
}

// feishuSign 飞书自定义机器人签名：HMAC-SHA256(key=timestamp+"\n"+secret, data="") 再 base64
func feishuSign(timestamp, secret string) string {
	h := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// sendFeishu 发送飞书文本消息（title 与 content 间空一行）
func sendFeishu(cfg FeishuConfig, title, content string) error {
	if !feishuEnabled(cfg) {
		return fmt.Errorf("飞书未启用或 webhook 为空")
	}
	payload := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]string{
			"text": strings.TrimSpace(title + "\n" + content),
		},
	}
	// 填了签名密钥时附加校验字段
	if secret := strings.TrimSpace(cfg.Secret); secret != "" {
		ts := fmt.Sprintf("%d", time.Now().Unix())
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	body, err := postJSON(client, strings.TrimSpace(cfg.Webhook), payload)
	if err != nil {
		return err
	}
	// 飞书返回 {"code":0,...} 或旧版 {"StatusCode":0}
	var r struct {
		Code       int    `json:"code"`
		StatusCode int    `json:"StatusCode"`
		Msg        string `json:"msg"`
	}
	if json.Unmarshal(body, &r) == nil {
		if r.Code != 0 {
			return fmt.Errorf("飞书错误 %d: %s", r.Code, r.Msg)
		}
		if r.StatusCode != 0 && r.StatusCode != 200 {
			return fmt.Errorf("飞书错误 %d", r.StatusCode)
		}
	}
	return nil
}

// ==================== QQ OneBot（NapCat / Lagrange / LLOneBot） ====================

// oneBotSendMsg 通过 OneBot HTTP API 发消息。
// targetType: "group" / "private"；target: 群号或 QQ 号
func oneBotSendMsg(cfg QQOneBotConfig, targetType, target, text string) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		return fmt.Errorf("OneBot HTTP 地址为空")
	}
	num := strings.Join(strings.FieldsFunc(target, func(r rune) bool {
		return r < '0' || r > '9'
	}), "")
	if num == "" {
		return fmt.Errorf("目标群号/QQ 号无效: %q", target)
	}
	msgType := "group"
	key := "group_id"
	if targetType == "private" {
		msgType, key = "private", "user_id"
	}
	payload := map[string]interface{}{
		"message_type": msgType,
		key:            json.Number(num),
		"message":      text,
	}
	req, err := http.NewRequest(http.MethodPost, base+"/send_msg", bytes.NewReader(toBytes(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tk := strings.TrimSpace(cfg.Token); tk != "" {
		req.Header.Set("Authorization", "Bearer "+tk)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("OneBot HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 120))
	}
	var r struct {
		Status string `json:"status"`
		Retcode int   `json:"retcode"`
	}
	if json.Unmarshal(body, &r) == nil && r.Retcode != 0 && r.Retcode != 1 {
		return fmt.Errorf("OneBot retcode %d (%s)", r.Retcode, r.Status)
	}
	return nil
}

// OneBotEvent QQ OneBot 事件回调（NapCat 等把事件 POST 到这里）。
// 仅处理私聊消息，且发送者必须等于配置的管理 QQ，文本走企微同款指令路由。
// POST /onebot/event?token=xxx   （token 与 OneBot 配置里的回调 Token 一致）
func (h *Handler) OneBotEvent(c *gin.Context) {
	cfg, err := loadMessageConfig()
	if err != nil || strings.TrimSpace(cfg.QQOneBot.EventToken) == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "onebot callback not configured"})
		return
	}
	if strings.TrimSpace(c.Query("token")) != strings.TrimSpace(cfg.QQOneBot.EventToken) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token mismatch"})
		return
	}
	var ev struct {
		PostType    string `json:"post_type"`
		MessageType string `json:"message_type"`
		RawMessage  string `json:"raw_message"`
		UserID      any    `json:"user_id"`
	}
	_ = json.NewDecoder(c.Request.Body).Decode(&ev)
	if ev.PostType == "message" && ev.MessageType == "private" {
		admin := strings.Join(strings.FieldsFunc(cfg.QQOneBot.Admin, func(r rune) bool {
			return r < '0' || r > '9'
		}), "")
		sender := strings.Join(strings.FieldsFunc(fmt.Sprint(ev.UserID), func(r rune) bool {
			return r < '0' || r > '9'
		}), "")
		if admin == "" || sender == admin {
			text := strings.TrimSpace(ev.RawMessage)
			if text != "" {
				go h.wecomHandleCommand(sender, text) // 与企微机器人同一套路由（sender 作会话隔离键）
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ==================== QQ 官方机器人（q.qq.com Bot API） ====================

// qqOfficialTokenCache access_token 缓存（有效期约 2 小时，提前 5 分钟刷新）
var (
	qqTokenMu     sync.Mutex
	qqTokenVal    string
	qqTokenExpire time.Time
)

func qqOfficialToken(cfg QQOfficialConfig) (string, error) {
	qqTokenMu.Lock()
	if qqTokenVal != "" && time.Now().Before(qqTokenExpire) {
		t := qqTokenVal
		qqTokenMu.Unlock()
		return t, nil
	}
	qqTokenMu.Unlock()

	appID := strings.TrimSpace(cfg.AppID)
	secret := strings.TrimSpace(cfg.Secret)
	if appID == "" || secret == "" {
		return "", fmt.Errorf("AppID / Secret 未配置")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	body, err := postJSON(client, "https://bots.qq.com/app/getAppAccessToken",
		map[string]string{"appId": appID, "clientSecret": secret})
	if err != nil {
		return "", err
	}
	var r struct {
		AccessToken       string `json:"access_token"`
		ExpiresIn         string `json:"expires_in"`
		Code              any    `json:"code"`
		Message           string `json:"message"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.AccessToken == "" {
		return "", fmt.Errorf("获取 access_token 失败: %s", truncateStr(string(body), 120))
	}
	expSec, _ := parseDurSec(r.ExpiresIn)
	qqTokenMu.Lock()
	qqTokenVal = r.AccessToken
	qqTokenExpire = time.Now().Add(time.Duration(expSec-300) * time.Second)
	qqTokenMu.Unlock()
	return r.AccessToken, nil
}

func parseDurSec(s string) (int64, error) {
	s = strings.TrimSpace(s)
	var v int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("非数字: %q", s)
		}
		v = v*10 + int64(ch-'0')
	}
	return v, nil
}

// sendQQOfficial 推送文本到官方机器人群（group_openid）
func sendQQOfficial(cfg QQOfficialConfig, title, content string) error {
	if !cfg.isEnabled() || strings.TrimSpace(cfg.GroupID) == "" {
		return fmt.Errorf("QQ 官方机器人未启用或群 ID 为空")
	}
	token, err := qqOfficialToken(cfg)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{
		"content": strings.TrimSpace(title + "\n" + content),
		"msg_type": 0,
		"msg_id":   strings.TrimSpace(cfg.LastMsgID),
	}
	req, err := http.NewRequest(http.MethodPost,
		"https://api.sgroup.qq.com/groups/"+strings.TrimSpace(cfg.GroupID)+"/messages",
		bytes.NewReader(toBytes(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("QQ 官方 HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 150))
	}
	return nil
}

// toBytes 序列化失败返回空 JSON（防御）
func toBytes(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ==================== 汇总发送入口 ====================

// sendExtraChannels 把三个扩展渠道的通知发出（NotifyMessage / 富通知共用）。
// 返回 errors 里非空项的渠道名拼接（供测试按钮展示）
func sendExtraChannels(cfg *MessageConfig, title, content string) string {
	var errs []string
	// 飞书
	if feishuEnabled(cfg.Feishu) {
		go func() {
			if err := sendFeishu(cfg.Feishu, title, content); err != nil {
				log.Printf("[飞书] ✗ 发送失败: %v", err)
			} else {
				log.Printf("[飞书] ✓ 消息发送成功: %s", truncateStr(title, 40))
			}
		}()
	}
	// QQ OneBot
	if cfg.QQOneBot.isEnabled() && strings.TrimSpace(cfg.QQOneBot.URL) != "" {
		go func() {
			if err := oneBotSendMsg(cfg.QQOneBot, cfg.QQOneBot.TargetType, cfg.QQOneBot.Target,
				strings.TrimSpace(title+"\n"+content)); err != nil {
				log.Printf("[QQ-OneBot] ✗ 发送失败: %v", err)
			} else {
				log.Printf("[QQ-OneBot] ✓ 消息发送成功: %s", truncateStr(title, 40))
			}
		}()
	}
	// QQ 官方机器人（同步发：有 token 缓存与错误回传需求）
	if cfg.QQOfficial.isEnabled() && strings.TrimSpace(cfg.QQOfficial.GroupID) != "" {
		if err := sendQQOfficial(cfg.QQOfficial, title, content); err != nil {
			log.Printf("[QQ官方] ✗ 发送失败: %v", err)
			errs = append(errs, "QQ官方: "+err.Error())
		} else {
			log.Printf("[QQ官方] ✓ 消息发送成功: %s", truncateStr(title, 40))
		}
	}
	return strings.Join(errs, "; ")
}

// extraChannelsEnabled 是否有任一扩展渠道启用
func extraChannelsEnabled(cfg *MessageConfig) bool {
	return feishuEnabled(cfg.Feishu) ||
		(cfg.QQOneBot.isEnabled() && strings.TrimSpace(cfg.QQOneBot.URL) != "") ||
		(cfg.QQOfficial.isEnabled() && strings.TrimSpace(cfg.QQOfficial.GroupID) != "")
}

// ==================== 测试入口（TestMessage 扩展） ====================

// testExtraChannels 同步测试三个扩展渠道，返回 "渠道: 错误" 拼接串与成功数
func testExtraChannels(cfg *MessageConfig, testMsg string) (string, int) {
	errMsg := ""
	okCount := 0
	if feishuEnabled(cfg.Feishu) {
		if err := sendFeishu(cfg.Feishu, testMsg, "来自 StrmHub 的测试消息"); err != nil {
			errMsg += "飞书: " + err.Error() + "; "
		} else {
			okCount++
		}
	}
	if cfg.QQOneBot.isEnabled() && strings.TrimSpace(cfg.QQOneBot.URL) != "" {
		if err := oneBotSendMsg(cfg.QQOneBot, cfg.QQOneBot.TargetType, cfg.QQOneBot.Target, testMsg); err != nil {
			errMsg += "QQ(OneBot): " + err.Error() + "; "
		} else {
			okCount++
		}
	}
	if cfg.QQOfficial.isEnabled() && strings.TrimSpace(cfg.QQOfficial.GroupID) != "" {
		if err := sendQQOfficial(cfg.QQOfficial, testMsg, ""); err != nil {
			errMsg += "QQ官方: " + err.Error() + "; "
		} else {
			okCount++
		}
	}
	return errMsg, okCount
}
