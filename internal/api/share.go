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
	"strconv"
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
// 解析分享码 → shareList 列根目录 → shareReceive 转存全部顶层条目；organize=true 时转存后触发整理+增量。
// source 是提交来源（web / 机器人 / 影巢…），只用于链接台账留痕
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

	// 1. 文件列表 + 分享信息（GET /share/snap，只看根这一层：转存顶层条目即整份分享）
	allItems, shareTitle, err := shareList(shareCode, code, "0", cookie)
	if err != nil {
		return "", 0, 0, err
	}
	if len(allItems) == 0 {
		return "分享为空", 0, 0, nil
	}
	log.Printf("[上传] ▣ 分享「%s」共 %d 项，开始转存...", shareTitle, len(allItems))

	// 2. 一次性转存到目标目录
	ids := make([]string, 0, len(allItems))
	for _, f := range allItems {
		ids = append(ids, f.ID)
	}
	if err := shareReceive(shareCode, code, ids, target, cookie); err != nil {
		return "", 0, 0, err
	}
	success, fail = len(allItems), 0
	msg = fmt.Sprintf("「%s」转存完成: 成功 %d（共 %d 项）", shareTitle, success, len(allItems))
	log.Printf("[上传] %s", msg)

	// 来源链接：分享转存不产生 115 离线任务，产物就是 snap 列表里的顶层条目，
	// 转存后 115 保留原名 —— 整理时按名字认领，不用再问 115 一次
	names := make([]string, 0, len(allItems))
	for _, it := range allItems {
		if it.Name != "" {
			names = append(names, it.Name)
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

// ==================== 分享列目录与转存（转存核心与订阅共用） ====================

// shareEntry 分享里的一个条目
type shareEntry struct {
	ID    string // 转存时 file_id 传它：文件是 fid，目录是 cid
	Name  string
	Dir   string // 在分享里所在的目录路径（根为空），不含自己
	IsDir bool
	Size  int64
	Sha1  string
}

// path 在分享里的完整路径
func (e shareEntry) path() string {
	if e.Dir == "" {
		return e.Name
	}
	return e.Dir + "/" + e.Name
}

// shareEntryOf 解析 snap 列表里的一条。字段口径以 p115client tool/attr.py 的 normalize_attr 为准：
// fc=0 是目录（id 在 cid、父目录在 pid），文件 id 在 fid、父目录在 cid；没有 fc 时看有没有 sha / fid。
// 名字字段 115 在 n / fn / file_name / name 之间换过，都收；数字字段可能是数字也可能是字符串
func shareEntryOf(m map[string]any) shareEntry {
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case json.Number:
			return v.String()
		case float64:
			return strconv.FormatInt(int64(v), 10)
		case bool:
			if v {
				return "1"
			}
			return "0"
		}
		return ""
	}
	e := shareEntry{
		Name: firstNonEmpty(str("n"), str("fn"), str("file_name"), str("name")),
		Sha1: firstNonEmpty(str("sha"), str("sha1")),
	}
	e.Size, _ = strconv.ParseInt(firstNonEmpty(str("s"), str("fs")), 10, 64)
	fid, cid := str("fid"), str("cid")
	switch {
	case str("fc") != "":
		e.IsDir = str("fc") == "0"
	case e.Sha1 != "":
		e.IsDir = false
	default:
		e.IsDir = fid == "" && cid != ""
	}
	if e.IsDir {
		e.ID = firstNonEmpty(cid, fid)
	} else {
		e.ID = firstNonEmpty(fid, cid)
	}
	return e
}

// shareList 列分享里某个目录的直接子项（翻页收全），顺带返回分享标题。
// 经 getShareAPI → httpGet115Full，走全局节流
func shareList(shareCode, receiveCode, cid, cookie string) ([]shareEntry, string, error) {
	var out []shareEntry
	title := ""
	for offset := 0; ; offset += 1150 {
		body, err := getShareAPI("/share/snap", url.Values{
			"share_code":   {shareCode},
			"receive_code": {receiveCode},
			"cid":          {cid},
			"offset":       {fmt.Sprint(offset)},
			"limit":        {"1150"},
			"asc":          {"1"},
			"fc_mix":       {"0"},
		}, cookie, 15*time.Second)
		if err != nil {
			return nil, "", fmt.Errorf("获取分享文件列表失败: %s", err.Error())
		}
		var snap struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Data  struct {
				List      []map[string]any `json:"list"`
				ShareInfo struct {
					ShareTitle string `json:"share_title"`
				} `json:"shareinfo"`
			} `json:"data"`
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		if dec.Decode(&snap) != nil || !snap.State {
			log.Printf("[上传] ✗ 文件列表获取失败（链接失效或提取码错误）: %s", truncateStr(string(body), 120))
			return nil, "", fmt.Errorf("文件列表获取失败（链接失效或提取码错误）: %s", truncateStr(string(body), 120))
		}
		if title == "" {
			title = snap.Data.ShareInfo.ShareTitle
		}
		for _, m := range snap.Data.List {
			if e := shareEntryOf(m); e.ID != "" {
				out = append(out, e)
			}
		}
		if len(snap.Data.List) < 1150 {
			break // 最后一页
		}
	}
	return out, title, nil
}

// shareWalkMaxDepth 列分享最多往下几层：剧名/Season 1/字幕/xx.ass 已经是三层
const shareWalkMaxDepth = 4

// shareWalk 广度优先列整个分享，最多进 maxDirs 个目录（每个目录至少一次节流后的请求）。
// truncated = 还有目录没列（超了目录数或层数）：挑集时据此说明「没看全」
func shareWalk(shareCode, receiveCode, cookie string, maxDirs int) (entries []shareEntry, title string, truncated bool, err error) {
	return shareWalkWith(func(cid string) ([]shareEntry, string, error) {
		return shareList(shareCode, receiveCode, cid, cookie)
	}, maxDirs)
}

// shareWalkWith 可注入列目录函数的 shareWalk（测试用假分享树）
func shareWalkWith(list func(cid string) ([]shareEntry, string, error), maxDirs int) (entries []shareEntry, title string, truncated bool, err error) {
	type node struct {
		cid, dir string
		depth    int
	}
	queue := []node{{cid: "0"}}
	listed := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if listed >= maxDirs {
			truncated = true
			break
		}
		items, t, err := list(n.cid)
		if err != nil {
			if listed == 0 {
				return nil, "", false, err
			}
			// 根列出来了、子目录失败：已有的照样能挑，算没看全
			log.Printf("[订阅] ○ 分享子目录「%s」列不出来: %v", n.dir, err)
			truncated = true
			continue
		}
		listed++
		if title == "" {
			title = t
		}
		for _, it := range items {
			it.Dir = n.dir
			entries = append(entries, it)
			if !it.IsDir {
				continue
			}
			if n.depth+1 >= shareWalkMaxDepth {
				truncated = true
				continue
			}
			queue = append(queue, node{cid: it.ID, dir: it.path(), depth: n.depth + 1})
		}
	}
	return entries, title, truncated, nil
}

// shareReceiveBatch 一次 /share/receive 最多带多少个 file_id（上限未实测，保守分批）
const shareReceiveBatch = 500

// shareReceive 把分享里的若干条目（文件或目录，任意层级）转存到 target。
// POST webapi.115.com/share/receive，file_id 逗号分隔；列表端点是 GET、转存端点是 POST，
// 用 GET 打 receive 会拿到 "405 METHOD NOT ALLOWED"（errNo 980005）
func shareReceive(shareCode, receiveCode string, ids []string, target, cookie string) error {
	for i := 0; i < len(ids); i += shareReceiveBatch {
		end := min(i+shareReceiveBatch, len(ids))
		body, err := postShareAPI("/share/receive", url.Values{
			"share_code":   {shareCode},
			"receive_code": {receiveCode},
			"file_id":      {strings.Join(ids[i:end], ",")},
			"cid":          {target},
		}, cookie, shareReferer(shareCode, receiveCode), 30*time.Second)
		if err != nil {
			return fmt.Errorf("转存提交失败: %s", err.Error())
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) != nil || !r.State {
			log.Printf("[上传] ✗ 转存被拒: %s", truncateStr(string(body), 120))
			return fmt.Errorf("转存被拒: %s", truncateStr(string(body), 120))
		}
	}
	return nil
}

// shareReceivePicked 只转存分享里挑中的条目（订阅按集挑选用），并登记来源链接。
// names 是整理认领用的名字：订阅转进包装目录，传包装目录名；返回 DownloadLink.ID 供结算
func (h *Handler) shareReceivePicked(shareURL, code string, ids []string, target, title, source string, names []string) (uint, error) {
	shareCode := extractShareCode(shareURL)
	if shareCode == "" {
		return 0, fmt.Errorf("无法从链接解析分享码")
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("没有要转存的文件")
	}
	if target == "" {
		return 0, fmt.Errorf("没有转存目标目录")
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return 0, err
	}
	if err := shareReceive(shareCode, code, ids, target, cookie); err != nil {
		return 0, err
	}
	log.Printf("[上传] ✓ 分享「%s」转存 %d 个挑中的文件", title, len(ids))
	return dlLinkRecord(h, shareURL, "share", title, source, names), nil
}
