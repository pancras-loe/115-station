package api

// ==================== 企业微信机器人（双向互动） ====================
//
// 回调地址：http://<公网IP>:6086/wecom/callback
//   GET  = 企业微信后台的 URL 验证（解密 echostr 回显）
//   POST = 用户发给应用的消息（AES 加密 XML）→ 指令路由 → 异步回复
// 指令：下载 <链接> / 状态 / 搜索 <片名> / 整理 / 同步 / 帮助

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// wecomCallbackCfg 从消息配置读取的回调三要素
type wecomCallbackCfg struct {
	Token   string
	AESKey  string
	CorpID  string
	Enabled bool
}

func loadWecomCallback() (*wecomCallbackCfg, error) {
	cfg, err := loadMessageConfig()
	if err != nil {
		return nil, err
	}
	cb := &wecomCallbackCfg{
		Token:   cfg.Wecom.Token,
		AESKey:  cfg.Wecom.EncodingAESKey,
		CorpID:  cfg.Wecom.CorpID,
		Enabled: cfg.Wecom.isEnabled(),
	}
	if cb.Token == "" || cb.AESKey == "" {
		return nil, fmt.Errorf("未配置 Token / EncodingAESKey（消息配置卡）")
	}
	return cb, nil
}

// WecomCallback GET=URL验证 POST=消息接收
func (h *Handler) WecomCallback(c *gin.Context) {
	cb, err := loadWecomCallback()
	if err != nil {
		c.String(http.StatusServiceUnavailable, "callback not configured")
		return
	}
	key, err := newWecomAESKey(cb.AESKey)
	if err != nil {
		c.String(http.StatusInternalServerError, "aes key invalid")
		return
	}

	if c.Request.Method == http.MethodGet {
		// URL 验证：验签后解密 echostr，回显明文
		msgSig := c.Query("msg_signature")
		timestamp, nonce, echostr := c.Query("timestamp"), c.Query("nonce"), c.Query("echostr")
		if wecomSign(cb.Token, timestamp, nonce, echostr) != msgSig {
			c.String(http.StatusForbidden, "sign mismatch")
			return
		}
		plain, derr := key.wecomDecrypt(echostr)
		if derr != nil {
			c.String(http.StatusBadRequest, "decrypt fail: %v", derr)
			return
		}
		c.String(http.StatusOK, string(plain))
		return
	}

	// POST：收消息
	body, _ := io.ReadAll(c.Request.Body)
	var env struct {
		XMLName    xml.Name `xml:"xml"`
		ToUserName string   `xml:"ToUserName"`
		Encrypt    string   `xml:"Encrypt"`
	}
	if xml.Unmarshal(body, &env) != nil || env.Encrypt == "" {
		c.String(http.StatusOK, "success") // 格式异常也回 success，防企业微信重试轰炸
		return
	}
	msgSig := c.Query("msg_signature")
	timestamp, nonce := c.Query("timestamp"), c.Query("nonce")
	if wecomSign(cb.Token, timestamp, nonce, env.Encrypt) != msgSig {
		c.String(http.StatusForbidden, "sign mismatch")
		return
	}
	plainXML, derr := key.wecomDecrypt(env.Encrypt)
	if derr != nil {
		c.String(http.StatusOK, "success")
		return
	}
	var msg struct {
		XMLName    xml.Name `xml:"xml"`
		ToUserName string   `xml:"ToUserName"` // 企业 corpid
		FromUser   string   `xml:"FromUserName"`
		MsgType    string   `xml:"MsgType"`
		Content    string   `xml:"Content"`
		Event      string   `xml:"Event"`
		EventKey   string   `xml:"EventKey"`
		CreateTime int64    `xml:"CreateTime"`
	}
	if xml.Unmarshal(plainXML, &msg) != nil {
		c.String(http.StatusOK, "success")
		return
	}

	// 5 秒内必须应答：指令异步执行 + 异步回复
	switch {
	case msg.MsgType == "text":
		go h.wecomHandleCommand(msg.FromUser, strings.TrimSpace(msg.Content))
	case msg.MsgType == "event" && msg.Event == "click":
		// 聊天底栏菜单按钮（wecomMenuCreate 创建）→ key 即指令文本
		go h.wecomHandleCommand(msg.FromUser, strings.TrimSpace(msg.EventKey))
	}
	c.String(http.StatusOK, "success")
}

// wecomMenuCreate 创建应用聊天底栏菜单（企业微信自建应用的「自定义菜单」；
// 不创建则聊天框底部只有输入框，没有任何快捷按钮）。幂等：重复调用覆盖旧菜单。
// 菜单项：自动整理 / 增量同步 —— click 事件的 key 走既有指令路由。
func wecomMenuCreate(cfg WecomConfig) error {
	if !cfg.isEnabled() || cfg.CorpID == "" || cfg.Secret == "" || cfg.AgentID == "" {
		return fmt.Errorf("企业微信配置不完整（CorpID/Secret/AgentID）")
	}
	// 企业微信菜单为两级结构：一级最多 3 个，每个一级最多 5 个二级；
	// 二级不能再套子菜单（平台硬限制）。二级 click 按钮与一级一样
	// 通过 key 走指令路由，无需额外处理。
	type btn struct {
		Type      string `json:"type,omitempty"`
		Name      string `json:"name"`
		Key       string `json:"key,omitempty"`
		SubButton []btn  `json:"sub_button,omitempty"`
	}
	menu := map[string]interface{}{
		"button": []btn{
			{Name: "常用操作", SubButton: []btn{
				{Type: "click", Name: "立即整理", Key: "整理"},
				{Type: "click", Name: "增量同步", Key: "同步"},
				{Type: "click", Name: "画质补全", Key: "补全"},
				{Type: "click", Name: "任务状态", Key: "状态"},
			}},
			{Name: "插件功能", SubButton: []btn{
				{Type: "click", Name: "创建 Emby 媒体库", Key: "建库"},
				{Type: "click", Name: "使用帮助", Key: "帮助"},
			}},
		},
	}
	payload, _ := json.Marshal(menu)

	token, err := wecomAccessToken(cfg)
	if err != nil {
		return err
	}
	createURL := fmt.Sprintf("%s/cgi-bin/menu/create?access_token=%s&agentid=%s",
		cfg.apiBase(), token, cfg.AgentID)
	req, err := http.NewRequest("POST", createURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(body, &r) != nil {
		return fmt.Errorf("响应非 JSON: %s", truncateStr(string(body), 120))
	}
	if r.ErrCode != 0 {
		return fmt.Errorf("%d %s", r.ErrCode, r.ErrMsg)
	}
	return nil
}

// WecomMenuAutoEnsure 启动时自动生成聊天底栏菜单（默认就生成，无需手动操作；
// 未启用企微或配置不全时静默跳过，稍后保存消息配置时也会重新生成）
func WecomMenuAutoEnsure() {
	time.Sleep(15 * time.Second) // 等网络/数据库就绪
	cfg, err := loadMessageConfig()
	if err != nil || !cfg.Wecom.isEnabled() || cfg.Wecom.CorpID == "" || cfg.Wecom.AgentID == "" {
		return
	}
	if err := wecomMenuCreate(cfg.Wecom); err != nil {
		log.Printf("[企微菜单] ○ 启动自动生成失败（保存消息配置时会重试）: %v", err)
	} else {
		log.Printf("[企微菜单] ✓ 底栏菜单已生成（自动整理 / 增量同步）")
	}
}

// wecomHandleCommand 指令路由（异步执行，结果用应用消息推回）
func (h *Handler) wecomHandleCommand(user, text string) {
	h.handleBotCommand(user, text, func(lines ...string) {
		NotifyMessage("", strings.Join(lines, "\n"))
	})
}

// handleBotCommand 注入回复出口，避免私聊指令结果广播到其他通知渠道。
func (h *Handler) handleBotCommand(user, text string, reply func(...string)) {
	if text == "" {
		return
	}
	lower := strings.ToLower(text)
	// 直接发链接（无需"下载"前缀）：磁力/ed2k/http/115分享 一键触发
	if classifyLink(strings.TrimSpace(text)) != "" {
		h.wecomHandleLink(strings.TrimSpace(text), reply)
		return
	}
	// 观影/网盘会话中的序号回复（1-2 位纯数字）：选片 / 选资源
	if len(text) <= 2 && regexpPureDigits.MatchString(text) {
		if wecomGySessionGet(user) != nil {
			n, _ := strconv.Atoi(text)
			h.wecomHandleGyPick(user, n, reply)
			return
		}
		if wecomPansouSessionGet(user) != nil {
			n, _ := strconv.Atoi(text)
			h.wecomHandlePansouPick(user, n, reply)
			return
		}
	}
	switch {
	case lower == "帮助" || lower == "help" || lower == "?":
		reply(
			"可用指令：",
			"直接发链接 — 磁力/ed2k/HTTP 提交离线下载；115 分享链接（连同提取码一起发）自动转存并整理入库",
			"状态 — 任务状态 + 转存目录 + 离线任务",
			"搜索 <片名> — TMDB 搜片",
			"观影 <片名> — TMDB 选片 → 观影搜资源（大小/做种/中字）→ 回序号离线下载",
			"网盘 <片名> — TMDB 选片 → PanSou 聚合搜网盘分享（115/百度等）→ 回序号转存/离线",
			"整理 / 同步 / 补全 — 手动触发整理、增量同步、画质补全",
		)

	case strings.HasPrefix(text, "下载"), strings.HasPrefix(lower, "dl "):
		link := strings.TrimSpace(strings.TrimPrefix(text, "下载"))
		if strings.HasPrefix(lower, "dl ") {
			link = strings.TrimSpace(text[3:])
		}
		if classifyLink(link) == "" {
			reply("✗ 无法识别链接类型，支持：磁力 / ed2k / HTTP / 115分享")
			return
		}
		h.wecomHandleLink(link, reply)

	case lower == "状态" || lower == "status":
		running, name, since, progress := TaskStatus()
		lines := []string{}
		if running {
			lines = append(lines, fmt.Sprintf("▶ 任务运行中：%s（已 %s）", name, time.Since(since).Truncate(time.Second)))
			if progress != "" {
				lines = append(lines, "  进度："+progress)
			}
		} else {
			lines = append(lines, "○ 当前无任务运行")
		}
		if cid := h.shareFolderCid(); cid != "" {
			// 115 目录查询加超时保护：网盘响应慢时不能拖住整个状态回复
			type listRes struct {
				count int
				ok    bool
			}
			ch := make(chan listRes, 1)
			go func() {
				if ops, err := h.newPan115Ops(); err == nil {
					if entries, _, lerr := ops.listEntries(cid, 0); lerr == nil {
						ch <- listRes{len(entries), true}
						return
					}
				}
				ch <- listRes{}
			}()
			select {
			case r := <-ch:
				if r.ok {
					lines = append(lines, fmt.Sprintf("转存目录待处理：%d 个条目", r.count))
				}
			case <-time.After(5 * time.Second):
				lines = append(lines, "转存目录待处理：查询超时（115 响应慢）")
			}
		}
		if runs := GetRecentRuns(); len(runs) > 0 {
			lines = append(lines, "最近任务：")
			for _, r := range runs {
				mark := "✓"
				if !r.OK {
					mark = "✗"
				}
				lines = append(lines, fmt.Sprintf("  %s %s %s（%s）", mark, r.Name, r.Start, r.Elapsed))
			}
		}
		reply(lines...)

	case strings.HasPrefix(text, "搜索"), strings.HasPrefix(lower, "so "):
		q := strings.TrimSpace(strings.TrimPrefix(text, "搜索"))
		if strings.HasPrefix(lower, "so ") {
			q = strings.TrimSpace(text[3:])
		}
		if q == "" {
			reply("用法：搜索 <片名>")
			return
		}
		results := h.wecomSearchTMDB(q)
		if len(results) == 0 {
			reply("未找到: " + q)
			return
		}
		lines := []string{fmt.Sprintf("TMDB 搜索 %q：", q)}
		for _, r := range results {
			lines = append(lines, r)
		}
		reply(lines...)

	case strings.HasPrefix(text, "网盘"), strings.HasPrefix(lower, "wp "):
		kw := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "网盘"), "wp "))
		if kw == "" {
			reply("用法：网盘 <片名>，例如：网盘 蜘蛛侠")
			return
		}
		h.wecomHandlePansouSearch(user, kw, reply)
		return
	case strings.HasPrefix(text, "观影"), strings.HasPrefix(lower, "gy "):
		kw := strings.TrimSpace(strings.TrimPrefix(text, "观影"))
		if strings.HasPrefix(lower, "gy ") {
			kw = strings.TrimSpace(text[3:])
		}
		if kw == "" {
			reply("用法：观影 <片名>，例如：观影 蜘蛛侠")
			return
		}
		h.wecomHandleGySearch(user, kw, reply)

	case lower == "建库" || lower == "创建媒体库":
		reply("已开始创建 Emby 媒体库，完成后通知。")
		go func() {
			cands, err := h.scanLibCandidates()
			if err != nil {
				reply("✗ 建库扫描失败: " + err.Error())
				return
			}
			var items []embyLibCandidate
			for _, c := range cands {
				if !c.Exists {
					items = append(items, c)
				}
			}
			if len(items) == 0 {
				reply("没有需要创建的媒体库（Emby 里都已存在）")
				return
			}
			created, skipped := h.embyLibrariesCreateItems(items)
			msg := fmt.Sprintf("✓ Emby 建库完成：新建 %d 个", len(created))
			if len(skipped) > 0 {
				msg += fmt.Sprintf("，跳过 %d 个", len(skipped))
			}
			reply(msg)
		}()

	case lower == "alist" || lower == "清空115":
		reply("该插件功能开发中，敬请期待。")

	case lower == "补全" || lower == "enrich":
		if !loadEnrichPolicy().Enabled {
			reply("补全功能未开启（自动整理 → 媒体补全）")
			return
		}
		go func() {
			if _, _, err := h.executeEnrichScan(); err != nil {
				reply("✗ 补全扫描失败: " + err.Error())
			} else {
				reply("✓ 补全扫描完成，任务已入队（详见日志）")
			}
		}()
		reply("已开始扫描媒体库，缺画质信息的文件将入队探测。")

	case lower == "整理":
		reply("已开始整理，完成后通知。")
		go func() {
			// 必须取任务互斥锁：此前直接调执行函数，可与全量同步/定时任务
			// 并发搬动同一棵 115 目录树
			if !fullSyncMu.TryLock() {
				reply("○ 整理未开始：已有任务运行中")
				return
			}
			defer fullSyncMu.Unlock()
			beginTask("机器人指令-整理")
			defer endTask()
			if _, _, err := h.executeOrganize(); err != nil {
				reply("✗ 整理失败: " + err.Error())
			} else {
				reply("✓ 整理完成（详见日志）")
			}
		}()

	case lower == "同步":
		reply("已开始增量同步，完成后通知。")
		go func() {
			// 同上：增量同步与全量共用事件流与本地树，必须互斥
			if !fullSyncMu.TryLock() {
				reply("○ 增量同步未开始：已有任务运行中")
				return
			}
			defer fullSyncMu.Unlock()
			beginTask("机器人指令-增量同步")
			defer endTask()
			p := h.incrParamsFromConfig()
			if _, err := h.executeIncrementalSync(p); err != nil {
				reply("✗ 增量同步失败: " + err.Error())
			} else {
				reply("✓ 增量同步完成（详见日志）")
			}
		}()

	default:
		reply("未识别的指令。发送「帮助」查看可用指令。")
	}
}

// wecomHandleLink 企微消息里的下载链接分流：
// 115 分享链接 → shareReceiveCore 转存（自动整理）；磁力/ed2k/http → 115 离线下载
func (h *Handler) wecomHandleLink(link string, reply func(lines ...string)) {
	if classifyLink(link) == "share" {
		shareURL, code := splitShareLink(link)
		if code == "" {
			reply("✗ 115 分享链接缺少提取码：",
				"把链接和提取码发在一起即可，例如：",
				"https://115.com/s/abc123?password=xxxx",
				"或：https://115.com/s/abc123 提取码：xxxx")
			return
		}
		organize := true // 机器人触发的转存默认走「整理+增量」闭环
		reply("⏳ 开始转存 115 分享…", truncateStr(shareURL, 70))
		go func() {
			msg, ok, fail, err := h.shareReceiveCore(shareURL, code, "", "机器人", organize)
			if err != nil {
				reply("✗ 转存失败: " + err.Error())
				return
			}
			if ok == 0 && fail == 0 {
				reply("分享为空，未转存任何内容")
				return
			}
			reply("✓ " + msg + "（整理入库已自动触发）")
		}()
		return
	}
	if err := h.submitOfflineLink(link); err != nil {
		reply("✗ 提交失败: " + err.Error())
		return
	}
	reply("✓ 已提交离线下载", "下载完成后自动整理入库并通知。")
}

// splitShareLink 从消息中拆出分享链接与提取码。
// 支持：URL 自带 ?password=xxx；"链接 提取码：xxx"；"链接 xxx"（末尾独立字段）
func splitShareLink(msg string) (shareURL, code string) {
	fields := strings.Fields(msg)
	if len(fields) == 0 {
		return "", ""
	}
	shareURL = fields[0]
	if u, err := url.Parse(shareURL); err == nil {
		if p := u.Query().Get("password"); p != "" {
			return shareURL, p
		}
	}
	// 其余字段里找提取码（"提取码：xxx" / "密码:xxx" / 纯 4-8 位字母数字）
	for i := 1; i < len(fields); i++ {
		f := strings.TrimPrefix(strings.TrimPrefix(fields[i], "提取码"), "密码")
		f = strings.Trim(f, "：:，, ")
		if len(f) >= 3 && len(f) <= 12 && isShareCode(f) {
			return shareURL, f
		}
	}
	return shareURL, ""
}

func isShareCode(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return len(s) > 0
}

// wecomSearchTMDB 搜片（独立轻实现：不加载完整整理客户端）
func (h *Handler) wecomSearchTMDB(q string) []string {
	tc, err := loadTmdbClient()
	if err != nil {
		return []string{"TMDB 未配置: " + err.Error()}
	}
	parsed := &ParsedName{Title: q}
	media, err := tc.recognize(parsed)
	if err != nil || media == nil {
		return nil
	}
	typ := "电影"
	if media.MediaType == "tv" {
		typ = "剧集"
	}
	return []string{fmt.Sprintf("%s《%s》(%s) tmdb=%d — 可发送：下载 <磁力链接> 提交", typ, media.Title, media.Year, media.TmdbID)}
}

// submitOfflineLink 提交离线下载（磁力/ed2k/HTTP 走 web lixian 接口）
func (h *Handler) submitOfflineLink(rawURL string) error {
	cookie, err := h.get115Cookie()
	if err != nil {
		return err
	}
	form := url.Values{"url": {rawURL}}
	if target := h.shareFolderCid(); target != "" {
		form.Set("wp_path_id", target)
	}
	body, err := post115Form("https://115.com/web/lixian/?ac=add_task_url", form, cookie, ua115Unified(), 20*time.Second)
	if err != nil {
		return err
	}
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.State {
		return fmt.Errorf("115 拒绝: %s", truncateStr(string(body), 100))
	}
	log.Printf("[机器人] ✓ 离线下载已提交: %s", truncateStr(rawURL, 60))
	offlineMineAdd(h, rawURL)      // 归属标记（企微提交的同样只在本项目内通知）
	offlinePlayRegister(h, rawURL) // 按需离线登记：占位 STRM 指向 /ed2k/play/{id}，边下边播
	// 下载记录：与 offlineSubmitCore 同样登记，机器人提交的也要进记录表
	dlLinkRecord(h, rawURL, "", "", h.shareFolderCid(), "机器人", "submitted", nil)
	return nil
}
