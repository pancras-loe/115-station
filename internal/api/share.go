package api

// ==================== 115 分享链接转存 ====================
//
// 流程（协议以 p115client 为准，字段与方法都踩过坑，改前先对一遍）：
//  1. 解析分享链接取 share_code
//  2. GET  webapi.115.com/share/snap    拿文件列表与分享标题（翻页收全）
//     —— 旧的 POST /share/info 恒返「开小差」、POST /share/snap 回 405，都已作废
//  3. POST webapi.115.com/share/receive 一次性转存（file_id 逗号分隔）
//     —— 这个是 POST，和上一步的 GET 不一样；用 GET 打会回
//        {"state":false,"error":"405 METHOD NOT ALLOWED","errNo":980005}
//
// 转存落到「转存目录」后由整理流水线接管（整理自带 STRM 落盘与刮削）。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ShareReceive 转存分享链接到接收文件夹
// POST /share/receive  body: {"url":"https://115.com/s/xxx", "code":"提取码", "target_cid":"可选，默认接收文件夹"}

// ==================== 分享接口镜像轮换 ====================
//
// 分享三接口（info/snap/sharepost）此前直连 webapi.115.com，被风控
// （"服务器开小差了"）时整个分享转存失败。与文件列表接口同款思路：
// 主域名被拒时轮换镜像域名重试（web.api / 115cdn / 115vod）。

var shareAPIOrigins = []string{
	"https://webapi.115.com",
	"http://web.api.115.com",
	"https://115cdn.com/webapi",
	"https://115vod.com/webapi",
}

// getShareAPI 分享接口 GET：请求失败或命中 115 风控响应（开小差/频繁）
// 时切换下一镜像，全部镜像用尽后返回最后一次结果
func getShareAPI(path string, query url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	var lastBody []byte
	var lastErr error
	for _, origin := range shareAPIOrigins {
		body, err := httpGet115Full(origin+path, query, cookie, ua115Unified(), timeout, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if is115BusyResp(body) {
			lastBody = body
			log.Printf("[上传] ○ %s 命中风控（开小差），切换镜像重试", path)
			continue
		}
		return body, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return lastBody, nil
}

// postShareAPI 分享接口 POST 表单：镜像轮换与风控识别同 getShareAPI。
//
// /share/receive 只认 POST —— 用 GET 打会被回
// {"state":false,"error":"405 METHOD NOT ALLOWED","errNo":980005}，
// 且这个错误跟链接失效、提取码错完全长得不一样，排查时别往那边想。
// 协议以 p115client 的 share_receive 为准（POST + form）。
//
// 轮换是安全的：只有命中 is115BusyResp（开小差/稍后再试/频繁）才换下一个镜像，
// 那类响应代表请求被拒、没有真的执行，不会重复转存
func postShareAPI(path string, form url.Values, cookie, referer string, timeout time.Duration) ([]byte, error) {
	var lastBody []byte
	var lastErr error
	for _, origin := range shareAPIOrigins {
		body, err := httpPostForm115Ref(origin+path, form, cookie, referer, timeout)
		if err != nil {
			lastErr = err
			continue
		}
		if is115BusyResp(body) {
			lastBody = body
			log.Printf("[上传] ○ %s 命中风控（开小差），切换镜像重试", path)
			continue
		}
		return body, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return lastBody, nil
}

// shareReferer 分享页 Referer（115driver 的 BuildShareReferer 同款）
func shareReferer(shareCode, receiveCode string) string {
	return fmt.Sprintf("https://115cdn.com/s/%s?password=%s&", shareCode, receiveCode)
}

// is115BusyResp 识别 115 风控响应（state=false 且带"开小差/稍后再试/频繁"文案）
func is115BusyResp(body []byte) bool {
	if !strings.Contains(string(body), "\"state\":false") {
		return false
	}
	return strings.Contains(string(body), "开小差") ||
		strings.Contains(string(body), "稍后再试") ||
		strings.Contains(string(body), "频繁")
}

func (h *Handler) ShareReceive(c *gin.Context) {
	var req struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		Target   string `json:"target_cid"`
		Organize bool   `json:"organize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写分享链接"})
		return
	}
	// 提取码允许为空：无密码分享可直接转存（与机器人通道一致），
	// 码错误时 115 会返回明确报错
	msg, success, fail, err := h.shareReceiveCore(req.URL, req.Code, req.Target, "web", req.Organize)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if success == 0 && fail == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "分享为空", "count": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "count": success, "failed": fail,
		"note": "转存成功，增量同步已自动触发（约 30 秒后完成 STRM 生成）"})
}

// shareReceiveCore 转存核心（HTTP 接口与企微机器人共用）：
// 解析分享码 → info → snap（翻页收全）→ sharepost → 逐项 receive；organize=true 时转存后触发整理+增量。
// source 是提交来源（web / 机器人 / 影巢 / TG 订阅…），只用于链接台账留痕
func (h *Handler) shareReceiveCore(shareURL, code, target, source string, organize bool) (msg string, success, fail int, err error) {
	shareCode := extractShareCode(shareURL)
	if shareCode == "" {
		return "", 0, 0, fmt.Errorf("无法从链接解析分享码")
	}
	// 提取码允许为空：无提取码分享直接转存（影巢解锁的资源可能不带访问码）
	// 目标目录：参数优先，否则取分享同步配置的接收文件夹
	if target == "" {
		var cfg struct {
			Folder string `json:"folder"`
		}
		if err := json.Unmarshal([]byte(h.getSettingValue("share")), &cfg); err != nil {
			log.Printf("[上传] ○ 分享配置解析失败: %v", err)
		}
		target = cfg.Folder
	}
	if target == "" {
		return "", 0, 0, fmt.Errorf("未配置接收文件夹（分享同步卡）")
	}

	cookie, err := h.get115Cookie()
	if err != nil {
		return "", 0, 0, err
	}
	req := struct {
		Organize bool
	}{organize}

	log.Printf("[上传] ▶ 分享转存开始: %s（提取码 %q）", truncateStr(shareURL, 70), code)

	// 1. 文件列表 + 分享信息（GET /share/snap）。
	//    此前的 POST /share/info 与 POST /share/snap 均已失效（信息端点恒返
	//    "开小差"、列表端点 405），p115client 权威协议为 GET + query
	// 条目名（Name/FileName 三选一非空）：转存后 115 保留原名，整理记录靠它
	// 认领来源链接。同一个响应里本来就有，不额外请求 115；字段名防御式地都收，
	// 115 的列表接口在 n / file_name / name 之间换过
	type snapItem struct {
		Fid      string `json:"fid"`
		Name     string `json:"n"`
		FileName string `json:"file_name"`
		NameAlt  string `json:"name"`
	}
	var allItems []snapItem
	shareTitle := ""
	for offset := 0; ; offset += 1150 {
		snapBody, err := getShareAPI("/share/snap", url.Values{
			"share_code":   {shareCode},
			"receive_code": {code},
			"cid":          {"0"},
			"offset":       {fmt.Sprint(offset)},
			"limit":        {"1150"},
			"asc":          {"1"},
			"fc_mix":       {"0"},
		}, cookie, 15*time.Second)
		if err != nil {
			return "", 0, 0, fmt.Errorf("获取分享文件列表失败: %s", err.Error())
		}
		var snap struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Data  struct {
				List      []snapItem `json:"list"`
				ShareInfo struct {
					ShareTitle string `json:"share_title"`
				} `json:"shareinfo"`
			} `json:"data"`
		}
		if json.Unmarshal(snapBody, &snap) != nil || !snap.State {
			log.Printf("[上传] ✗ 文件列表获取失败（链接失效或提取码错误）: %s", truncateStr(string(snapBody), 120))
			return "", 0, 0, fmt.Errorf("文件列表获取失败（链接失效或提取码错误）: %s", truncateStr(string(snapBody), 120))
		}
		if shareTitle == "" {
			shareTitle = snap.Data.ShareInfo.ShareTitle
		}
		allItems = append(allItems, snap.Data.List...)
		if len(snap.Data.List) < 1150 {
			break // 最后一页
		}
	}
	if len(allItems) == 0 {
		return "分享为空", 0, 0, nil
	}
	log.Printf("[上传] ▣ 分享「%s」共 %d 项，开始转存...", shareTitle, len(allItems))

	// 2. 一次性转存到目标目录：POST /share/receive（file_id 逗号分隔）。
	//    列表端点 /share/snap 是 GET，转存端点是 POST——两者不一样，
	//    用 GET 打 receive 会拿到 "405 METHOD NOT ALLOWED"（errNo 980005）
	fids := make([]string, 0, len(allItems))
	for _, f := range allItems {
		fids = append(fids, f.Fid)
	}
	rBody, err := postShareAPI("/share/receive", url.Values{
		"share_code":   {shareCode},
		"receive_code": {code},
		"file_id":      {strings.Join(fids, ",")},
		"cid":          {target},
	}, cookie, shareReferer(shareCode, code), 30*time.Second)
	if err != nil {
		return "", 0, 0, fmt.Errorf("转存提交失败: %s", err.Error())
	}
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal(rBody, &r) != nil || !r.State {
		log.Printf("[上传] ✗ 转存被拒: %s", truncateStr(string(rBody), 120))
		return "", 0, 0, fmt.Errorf("转存被拒: %s", truncateStr(string(rBody), 120))
	}
	success, fail = len(allItems), 0
	msg = fmt.Sprintf("「%s」转存完成: 成功 %d（共 %d 项）", shareTitle, success, len(allItems))
	log.Printf("[上传] %s", msg)

	// 来源链接：分享转存不产生 115 离线任务，产物就是 snap 列表里的顶层条目，
	// 转存后 115 保留原名 —— 整理时按名字认领，不用再问 115 一次
	names := make([]string, 0, len(allItems))
	for _, it := range allItems {
		if n := firstNonEmpty(it.Name, it.FileName, it.NameAlt); n != "" {
			names = append(names, n)
		}
	}
	dlLinkRecord(h, shareURL, "share", shareTitle, source, names)

	// 转存成功且开启自动整理 → 触发「整理+增量」
	if success > 0 && req.Organize {
		go h.triggerOrganizeAndSync()
	}
	return msg, success, fail, nil
}

// re115Share 115 分享链接（含 115cdn / anxia 新域名）；分享码可能带 - _
var re115Share = regexp.MustCompile(`(?:115\.com|115cdn\.com|anxia\.com)/s/([a-zA-Z0-9_-]+)`)

// reSharePass 分享链接内嵌提取码：?password=xxxx 或 #xxxx
var reSharePass = regexp.MustCompile(`(?:[?&]password=|#)([A-Za-z0-9]+)`)

// extractShareCode 从分享链接提取 share_code
func extractShareCode(raw string) string {
	if m := re115Share.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return strings.TrimSpace(raw)
}

// is115ShareLink 判断链接是否为 115 分享（可自动转存的域）
func is115ShareLink(raw string) bool {
	return re115Share.MatchString(raw)
}
