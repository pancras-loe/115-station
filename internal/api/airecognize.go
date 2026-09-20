package api

// AI 增强识别：TMDB 识别不出来时，让大模型从文件名里把标题/年份抠出来再搜一次。
//
// 对接的是 OpenAI 协议标准（POST {base}/v1/chat/completions，Bearer 鉴权，
// 取 choices[0].message.content），DeepSeek / 硅基流动 / Ollama / vLLM
// 这类兼容实现都能直接用。
//
// 这里的两个出口——界面上的「测试连接」与整理链路里的实际调用——必须走同一个
// aiChat：此前它们各自拼 URL（一个补 /v1/chat/completions、一个只补
// /chat/completions），结果是测试连接显示成功、真正识别时 404。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// aiSettingKey 配置存放的 Setting 键。名字是历史的（这张卡早年叫「GPT 识别」），
// 改键名只会让已填好的 API 密钥凭空消失，所以留着。
const aiSettingKey = "org-gpt"

// aiRecognizeCfg AI 增强识别配置
type aiRecognizeCfg struct {
	URL   string // 用户填的地址：可能是 base、带 /v1、或者整条 /v1/chat/completions
	Key   string // 可空——本地 Ollama / vLLM 通常不校验
	Model string
}

var aiCfgCache struct {
	sync.Mutex
	val *aiRecognizeCfg
	at  time.Time
}

// loadAIRecognizeCfg 读配置（5 分钟缓存；没配全返回 nil = 关掉这层兜底）
func loadAIRecognizeCfg() *aiRecognizeCfg {
	aiCfgCache.Lock()
	defer aiCfgCache.Unlock()
	if aiCfgCache.val != nil && time.Since(aiCfgCache.at) < 5*time.Minute {
		return aiCfgCache.val
	}
	var cfg struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
		Key     string `json:"key"`
		Model   string `json:"model"`
	}
	aiCfgCache.val = nil
	aiCfgCache.at = time.Now()
	v := settingValueCompat(aiSettingKey)
	if v == "" || json.Unmarshal([]byte(v), &cfg) != nil {
		return nil
	}
	// 开关关着就是关着——填好的地址/密钥留在配置里不动，随时能开回来
	if !cfg.Enabled {
		return nil
	}
	cfg.URL, cfg.Model = strings.TrimSpace(cfg.URL), strings.TrimSpace(cfg.Model)
	// 模型名不猜默认值：填了地址没填模型，猜一个只会换来一串 404，
	// 不如当成没配置，日志和界面都干净。
	if cfg.URL == "" || cfg.Model == "" {
		return nil
	}
	aiCfgCache.val = &aiRecognizeCfg{URL: cfg.URL, Key: strings.TrimSpace(cfg.Key), Model: cfg.Model}
	return aiCfgCache.val
}

// invalidateAICfgCache 配置保存后调用，免得 5 分钟 TTL 内改了还不生效
func invalidateAICfgCache() {
	aiCfgCache.Lock()
	aiCfgCache.val = nil
	aiCfgCache.Unlock()
}

// aiVersionSegRe 路径末段是不是版本号（v1 / v1beta / v4 …）
var aiVersionSegRe = regexp.MustCompile(`(?i)^v\d+[a-z0-9.\-]*$`)

// aiChatEndpoint 把用户填的地址补成 chat/completions 端点。
//
// 规则和 OpenAI SDK 的 base_url 一致：**只追加 /chat/completions，不替用户猜 /v1**。
// /v1 是各家自己文档里给的 base_url 的一部分，不是协议要求的前缀——DeepSeek 官方给的
// base_url 就是 https://api.deepseek.com（接口在 /chat/completions），OpenAI 给的才是
// https://api.openai.com/v1。替用户插一段只会把前者请求到别的路径上去。
//
//	https://api.deepseek.com                    → https://api.deepseek.com/chat/completions
//	https://api.openai.com/v1                   → https://api.openai.com/v1/chat/completions
//	http://127.0.0.1:11434/v1/chat/completions  → 原样
//	api.deepseek.com                            → https://api.deepseek.com/chat/completions
//
// 地址非法返回空串。
func aiChatEndpoint(raw string) string {
	u := aiParseBase(raw)
	if u == nil {
		return ""
	}
	// 已经是完整端点（含 Azure 那种带 ?api-version= 的）就不动
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path += "/chat/completions"
	}
	return u.String()
}

// aiChatEndpointV1 带 /v1 的备选端点。用户把 OpenAI 这类服务商的 base_url 少写了 /v1 时，
// aiChatEndpoint 那条会 404，拿这条再试一次。地址里已经有版本段、或已经是完整端点时
// 返回空串（没有第二条可试）。
func aiChatEndpointV1(raw string) string {
	u := aiParseBase(raw)
	if u == nil || strings.HasSuffix(u.Path, "/chat/completions") {
		return ""
	}
	if u.Path != "" && aiVersionSegRe.MatchString(path.Base(u.Path)) {
		return ""
	}
	u.Path += "/v1/chat/completions"
	return u.String()
}

// aiParseBase 规范化用户填的地址：没写协议头按 https 补、去尾斜杠；非法返回 nil
func aiParseBase(raw string) *url.URL {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u
}

// aiChat 发一次 chat/completions，返回首条回复文本
func aiChat(cfg aiRecognizeCfg, messages []map[string]string, maxTokens int, timeout time.Duration) (string, error) {
	reply, _, err := aiChatResolve(cfg, messages, maxTokens, timeout)
	return reply, err
}

// aiChatResolve 同 aiChat，额外返回实际请求的端点（测试连接把它回显给用户）。
// maxTokens <= 0 表示不限制。走全局代理——模型接口和 TMDB 共用一份代理配置。
func aiChatResolve(cfg aiRecognizeCfg, messages []map[string]string, maxTokens int, timeout time.Duration) (string, string, error) {
	endpoint := aiChatEndpoint(cfg.URL)
	if endpoint == "" {
		return "", "", fmt.Errorf("API 地址无效：%s", cfg.URL)
	}
	client := &http.Client{Timeout: timeout}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	reply, status, err := aiChatPost(client, endpoint, cfg, messages, maxTokens)
	// 404：多半是服务商把接口挂在 /v1 下、用户却只填了域名（OpenAI 官方就是这样）。
	// 换带 /v1 的那条再试一次，成功就把这条报回去，用户照着把地址补全即可。
	if status == http.StatusNotFound {
		if alt := aiChatEndpointV1(cfg.URL); alt != "" {
			if r, _, e := aiChatPost(client, alt, cfg, messages, maxTokens); e == nil {
				return r, alt, nil
			}
		}
	}
	return reply, endpoint, err
}

// aiChatPost 向指定端点发一次请求；第二个返回值是 HTTP 状态码（网络层失败为 0）
func aiChatPost(client *http.Client, endpoint string, cfg aiRecognizeCfg, messages []map[string]string, maxTokens int) (string, int, error) {
	// 第一次带上 temperature/max_tokens；被模型拒收时去掉这两项重试一次——
	// OpenAI o 系列这类推理模型只认 max_completion_tokens，temperature 也只能是默认值。
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		payload := map[string]any{"model": cfg.Model, "messages": messages}
		if attempt == 0 {
			payload["temperature"] = 0
			if maxTokens > 0 {
				payload["max_tokens"] = maxTokens
			}
		}
		b, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(b))
		if err != nil {
			return "", 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.Key != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", 0, fmt.Errorf("连接失败：%v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateStr(strings.TrimSpace(string(body)), 200))
			if attempt == 0 && resp.StatusCode == http.StatusBadRequest && aiParamRejected(body) {
				continue
			}
			return "", lastStatus, lastErr
		}
		var r struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &r) != nil || len(r.Choices) == 0 {
			// 200 但不是 OpenAI 协议的形状：多半是地址填成了网关首页/文档页
			return "", lastStatus, fmt.Errorf("响应不是 OpenAI 协议格式：%s", truncateStr(strings.TrimSpace(string(body)), 200))
		}
		return r.Choices[0].Message.Content, lastStatus, nil
	}
	return "", lastStatus, lastErr
}

// aiParamRejected 400 的原因是不是 temperature/max_tokens 不被支持
func aiParamRejected(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "max_tokens") || strings.Contains(s, "temperature")
}

// ==================== 文件名 → 标题/年份 ====================

// aiTitleGuess 大模型给出的标题/年份
type aiTitleGuess struct {
	Title string `json:"title"`
	Year  string `json:"year"`
}

var aiYearRe = regexp.MustCompile(`^(19|20)\d{2}$`)

// aiExtractTitle 让模型从文件名里提取标准标题与年份；拿不到可信结果返回 nil
func aiExtractTitle(cfg *aiRecognizeCfg, filename string) *aiTitleGuess {
	if cfg == nil || filename == "" {
		return nil
	}
	content, err := aiChat(*cfg, []map[string]string{
		{"role": "system", "content": `从影视文件名中提取标准标题和上映/开播年份。只输出 JSON：{"title":"...","year":"..."}，找不到年份输出空字符串。`},
		{"role": "user", "content": filename},
	}, 200, 30*time.Second)
	if err != nil {
		log.Printf("[AI识别] ✗ %v", err)
		return nil
	}
	obj := aiJSONObject(content)
	if obj == "" {
		log.Printf("[AI识别] ○ 回复里没有 JSON：%s", truncateStr(strings.TrimSpace(content), 100))
		return nil
	}
	var out aiTitleGuess
	if json.Unmarshal([]byte(obj), &out) != nil {
		return nil
	}
	out.Title = strings.TrimSpace(out.Title)
	out.Year = strings.TrimSpace(out.Year)
	if out.Title == "" {
		return nil
	}
	// 年份直接喂给 TMDB 搜索，"未知"/"1990s" 这种只会把结果搜没
	if !aiYearRe.MatchString(out.Year) {
		out.Year = ""
	}
	return &out
}

// aiJSONObject 从回复里抠出 JSON 对象：推理模型的 <think> 段、Markdown 代码围栏、
// 以及 JSON 前后的客套话都要能剥掉。
func aiJSONObject(s string) string {
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		}
	}
	a, b := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if a < 0 || b <= a {
		return ""
	}
	return s[a : b+1]
}

// ==================== 连通性测试 ====================

// TestAIConnection 测试 AI 增强识别的模型接口
// POST /config/test-ai  body: {"url":"...","key":"...","model":"..."}
func (h *Handler) TestAIConnection(c *gin.Context) {
	var req struct {
		URL   string `json:"url"`
		Key   string `json:"key"`
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.Model) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写 API 地址和模型名称"})
		return
	}
	if aiChatEndpoint(req.URL) == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "API 地址无效：" + req.URL})
		return
	}
	cfg := aiRecognizeCfg{URL: req.URL, Key: strings.TrimSpace(req.Key), Model: strings.TrimSpace(req.Model)}
	start := time.Now()
	reply, endpoint, err := aiChatResolve(cfg, []map[string]string{{"role": "user", "content": "hi"}}, 16, 15*time.Second)
	latency := time.Since(start).Milliseconds()
	// 两条分支都带上实际请求的地址：用户填的是 base，这一行才是真正打出去的 URL
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "endpoint": endpoint, "latency_ms": latency})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"latency_ms": latency,
		"endpoint":   endpoint,
		"reply":      truncateStr(strings.TrimSpace(reply), 60),
	})
}
