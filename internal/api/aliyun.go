package api

// 阿里云盘客户端：只做「分享秒传进 115」要用的几个接口（ali2115.go），不挂载、不列目录给界面看。
// 设计见 docs/115-station-notes/ALI2115-PLAN.md。
//
// 接口形态对照（只读，Go 重写）：
//   - 刷 token / 设备会话 / 自己网盘的列目录、建目录、取下载链接：OpenList drivers/aliyundrive
//   - 分享的 share_token 与列分享：OpenList drivers/aliyundrive_share
//   - 扫码登录、存分享进自己网盘、删临时文件：p115strmhelper core/aliyunpan.py 与 helper/ali2115（aligo 的写法）
//
// 阿里云盘对 access token 失效回 AccessTokenInvalid、设备会话失效回 DeviceSessionSignatureInvalid，
// 各自重建一次再重试；refresh token 每刷一次都换新的，刷完马上写回 PanSource（ali2115.go 的 save 回调）。

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 做成变量是为了测试能换成假服务
var (
	aliAPIBase      = "https://api.alipan.com"
	aliAuthBase     = "https://auth.alipan.com"
	aliPassportBase = "https://passport.aliyundrive.com"
	aliHTTP         = &http.Client{Timeout: 30 * time.Second}
)

const (
	aliCanaryDrive = "client=Android,app=adrive,version=v4.1.0"
	aliCanaryShare = "client=web,app=share,version=v2.3.1"
	aliReferer     = "https://www.alipan.com/"
)

// aliAPIError 阿里云盘接口的业务错误（HTTP 4xx 时响应体里的 code / message）
type aliAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *aliAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("阿里云盘：%s（%s）", e.Message, e.Code)
	}
	return fmt.Sprintf("阿里云盘：%s（HTTP %d）", e.Code, e.Status)
}

// aliErrCode 取业务错误码（不是 aliAPIError 返回空）
func aliErrCode(err error) string {
	var e *aliAPIError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// aliClient 一个阿里云盘账号的会话
type aliClient struct {
	refresh  string
	access   string
	userID   string
	driveID  string
	nick     string
	deviceID string
	sign     string
	// save refresh token 轮换后写回（nil = 不写，测试用）
	save func(c *aliClient)
}

// aliTokenMu 刷 token 全局串行：refresh token 用一次就作废，两处同时拿同一个去刷，后到的必然失败
var aliTokenMu sync.Mutex

// newAliClient 用 refresh token 建会话：刷 token → 读用户信息（存分享用资源盘）→ 建设备会话
func newAliClient(refresh string, save func(c *aliClient)) (*aliClient, error) {
	c := &aliClient{refresh: strings.TrimSpace(refresh), save: save}
	if c.refresh == "" {
		return nil, fmt.Errorf("没有阿里云盘的 refresh token，请重新登录")
	}
	if err := c.refreshToken(); err != nil {
		return nil, err
	}
	var u struct {
		UserID          string `json:"user_id"`
		NickName        string `json:"nick_name"`
		DefaultDriveID  string `json:"default_drive_id"`
		ResourceDriveID string `json:"resource_drive_id"`
	}
	if err := c.call("/v2/user/get", map[string]any{}, &u, "", aliCanaryDrive); err != nil {
		return nil, err
	}
	if u.UserID != "" {
		c.userID = u.UserID
	}
	if u.NickName != "" {
		c.nick = u.NickName
	}
	// 资源盘 / 备份盘分开之后，分享只能存进资源盘；老账号没有资源盘就用默认盘
	c.driveID = firstNonEmpty(u.ResourceDriveID, u.DefaultDriveID, c.driveID)
	if c.driveID == "" {
		return nil, fmt.Errorf("阿里云盘没返回网盘编号")
	}
	if err := c.createSession(); err != nil {
		return nil, err
	}
	return c, nil
}

// refreshToken refresh token 换 access token；新的 refresh token 立刻写回
func (c *aliClient) refreshToken() error {
	aliTokenMu.Lock()
	defer aliTokenMu.Unlock()
	var resp struct {
		AccessToken    string `json:"access_token"`
		RefreshToken   string `json:"refresh_token"`
		UserID         string `json:"user_id"`
		NickName       string `json:"nick_name"`
		DefaultDriveID string `json:"default_drive_id"`
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": c.refresh, "grant_type": "refresh_token"})
	req, _ := http.NewRequest(http.MethodPost, aliAuthBase+"/v2/account/token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if err := aliDo(req, &resp); err != nil {
		return fmt.Errorf("阿里云盘登录已失效，请重新扫码或换一个 refresh token（%w）", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		return fmt.Errorf("阿里云盘刷新 token 没返回新 token，请重新登录")
	}
	c.access, c.refresh = resp.AccessToken, resp.RefreshToken
	c.userID = firstNonEmpty(resp.UserID, c.userID)
	c.nick = firstNonEmpty(resp.NickName, c.nick)
	c.driveID = firstNonEmpty(c.driveID, resp.DefaultDriveID)
	if c.save != nil {
		c.save(c)
	}
	return nil
}

// createSession 登记设备公钥，之后的请求带 X-Device-Id / X-Signature（alisign.go）
func (c *aliClient) createSession() error {
	if c.userID == "" {
		return fmt.Errorf("阿里云盘没返回用户编号")
	}
	dev, sig, err := aliSignature(c.userID)
	if err != nil {
		return err
	}
	c.deviceID, c.sign = dev, sig
	_, d := aliDeviceKey(c.userID)
	return c.callRaw("/users/v1/users/device/create_session", map[string]any{
		"deviceName":   "samsung",
		"modelName":    "SM-G9810",
		"nonce":        0,
		"pubKey":       aliPubKeyHex(d),
		"refreshToken": c.refresh,
	}, nil, "", aliCanaryDrive)
}

// call 带鉴权调接口；token 失效 / 设备会话失效各重建一次
func (c *aliClient) call(path string, body any, out any, shareToken, canary string) error {
	err := c.callRaw(path, body, out, shareToken, canary)
	switch aliErrCode(err) {
	case "AccessTokenInvalid", "AccessTokenExpired":
		if err := c.refreshToken(); err != nil {
			return err
		}
	case "DeviceSessionSignatureInvalid", "UserDeviceOffline":
		if err := c.createSession(); err != nil {
			return err
		}
	default:
		return err
	}
	return c.callRaw(path, body, out, shareToken, canary)
}

func (c *aliClient) callRaw(path string, body any, out any, shareToken, canary string) error {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, aliAPIBase+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.alipan.com")
	req.Header.Set("Referer", aliReferer)
	req.Header.Set("X-Canary", canary)
	if c.access != "" {
		req.Header.Set("Authorization", "Bearer "+c.access)
	}
	if c.deviceID != "" {
		req.Header.Set("X-Device-Id", c.deviceID)
		req.Header.Set("X-Signature", c.sign)
	}
	if shareToken != "" {
		req.Header.Set("X-Share-Token", shareToken)
	}
	return aliDo(req, out)
}

// aliDo 发请求：4xx / 5xx 解析成 aliAPIError；2xx 解析进 out（204 / 空体不解析）
func aliDo(req *http.Request, out any) error {
	resp, err := aliHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连不上阿里云盘: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		e := &aliAPIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, e)
		if e.Code == "" {
			e.Code = truncateStr(strings.TrimSpace(string(data)), 80)
		}
		return e
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("阿里云盘返回格式认不出: %w（%s）", err, truncateStr(string(data), 120))
	}
	return nil
}

// ---- 分享 ----

// aliFile 分享里 / 自己网盘里的一项
type aliFile struct {
	FileID       string `json:"file_id"`
	Name         string `json:"name"`
	Type         string `json:"type"` // file / folder
	Size         int64  `json:"size"`
	ParentFileID string `json:"parent_file_id"`
	ContentHash  string `json:"content_hash"` // 自己网盘里才有：整文件 SHA1
}

func (f aliFile) isDir() bool { return f.Type == "folder" }

// aliShareToken 分享的 share_token（有提取码带上 share_pwd）
func aliShareToken(shareID, pwd string) (string, error) {
	body := map[string]string{"share_id": shareID}
	if pwd != "" {
		body["share_pwd"] = pwd
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, aliAPIBase+"/v2/share_link/get_share_token", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Canary", aliCanaryShare)
	var resp struct {
		ShareToken string `json:"share_token"`
	}
	if err := aliDo(req, &resp); err != nil {
		switch aliErrCode(err) {
		case "InvalidParameter.SharePwd", "ShareLink.Forbidden", "SharePwdIncorrect":
			return "", fmt.Errorf("阿里云盘分享的提取码不对")
		case "ShareLink.Cancelled", "ShareLink.Expired", "NotFound.ShareLink":
			return "", fmt.Errorf("阿里云盘分享已失效")
		}
		return "", err
	}
	if resp.ShareToken == "" {
		return "", fmt.Errorf("阿里云盘没返回分享凭证")
	}
	return resp.ShareToken, nil
}

// aliShareName 分享名（列表页标题）；拿不到返回空，不算错
func aliShareName(shareID string) string {
	raw, _ := json.Marshal(map[string]string{"share_id": shareID})
	req, _ := http.NewRequest(http.MethodPost, aliAPIBase+"/adrive/v3/share_link/get_share_by_anonymous?share_id="+url.QueryEscape(shareID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Canary", aliCanaryShare)
	var resp struct {
		ShareName string `json:"share_name"`
	}
	if aliDo(req, &resp) != nil {
		return ""
	}
	return strings.TrimSpace(resp.ShareName)
}

// listShare 列分享的一层（翻页）
func (c *aliClient) listShare(shareToken, shareID, parent string) ([]aliFile, error) {
	var all []aliFile
	marker := ""
	for page := 0; page < 200; page++ {
		var resp struct {
			Items      []aliFile `json:"items"`
			NextMarker string    `json:"next_marker"`
		}
		body := map[string]any{"share_id": shareID, "parent_file_id": parent, "limit": 100,
			"order_by": "name", "order_direction": "ASC", "marker": marker}
		if err := c.call("/adrive/v3/file/list", body, &resp, shareToken, aliCanaryShare); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextMarker == "" {
			return all, nil
		}
		marker = resp.NextMarker
	}
	return all, nil
}

// saveShareIDs 把分享里的这几个文件存进自己网盘的某个目录，返回 分享文件 id → 副本 id。
// 文件的复制多半当场完成、响应里带副本的 file_id；没带的（异步复制，回 async_task_id）不在返回里，调用方去目录里认
func (c *aliClient) saveShareIDs(shareToken, shareID string, fileIDs []string, toParent string) (map[string]string, error) {
	reqs := make([]map[string]any, 0, len(fileIDs))
	for i, id := range fileIDs {
		reqs = append(reqs, map[string]any{
			"id":      fmt.Sprint(i),
			"method":  "POST",
			"url":     "/file/copy",
			"headers": map[string]string{"Content-Type": "application/json"},
			"body": map[string]any{"file_id": id, "share_id": shareID, "auto_rename": true,
				"to_parent_file_id": toParent, "to_drive_id": c.driveID},
		})
	}
	var resp struct {
		Responses []struct {
			ID     string          `json:"id"`
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"responses"`
	}
	if err := c.call("/adrive/v2/batch", map[string]any{"requests": reqs, "resource": "file"}, &resp, shareToken, aliCanaryShare); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range resp.Responses {
		if r.Status >= 400 {
			e := &aliAPIError{Status: r.Status}
			_ = json.Unmarshal(r.Body, e)
			if e.Code == "QuotaExhausted.Drive" || strings.Contains(e.Code, "Quota") {
				return nil, fmt.Errorf("阿里云盘空间不够，存不下这次要秒传的文件（临时副本传完就删）: %w", e)
			}
			return nil, fmt.Errorf("存分享进阿里云盘失败: %w", e)
		}
		var b struct {
			FileID string `json:"file_id"`
		}
		_ = json.Unmarshal(r.Body, &b)
		var i int
		if _, err := fmt.Sscanf(r.ID, "%d", &i); err == nil && i >= 0 && i < len(fileIDs) && b.FileID != "" {
			out[fileIDs[i]] = b.FileID
		}
	}
	return out, nil
}

// ---- 自己网盘 ----

// listDir 列自己网盘的一层（带 content_hash）
func (c *aliClient) listDir(parent string) ([]aliFile, error) {
	var all []aliFile
	marker := ""
	for page := 0; page < 200; page++ {
		var resp struct {
			Items      []aliFile `json:"items"`
			NextMarker string    `json:"next_marker"`
		}
		body := map[string]any{"drive_id": c.driveID, "parent_file_id": parent, "limit": 200,
			"fields": "*", "order_by": "name", "order_direction": "ASC", "marker": marker}
		if err := c.call("/adrive/v3/file/list", body, &resp, "", aliCanaryDrive); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.NextMarker == "" {
			return all, nil
		}
		marker = resp.NextMarker
	}
	return all, nil
}

// ensureDir 自己网盘里找或建目录（check_name_mode=refuse：同名已存在时返回已有的那个）
func (c *aliClient) ensureDir(parent, name string) (string, error) {
	var resp struct {
		FileID string `json:"file_id"`
	}
	err := c.call("/adrive/v2/file/createWithFolders", map[string]any{"drive_id": c.driveID, "parent_file_id": parent,
		"name": name, "type": "folder", "check_name_mode": "refuse"}, &resp, "", aliCanaryDrive)
	if err != nil {
		return "", err
	}
	if resp.FileID == "" {
		return "", fmt.Errorf("阿里云盘建目录「%s」没返回编号", name)
	}
	return resp.FileID, nil
}

// aliDownload 下载链接与整文件 SHA1
type aliDownload struct {
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	ContentHash string `json:"content_hash"`
}

func (c *aliClient) downloadURL(fileID string) (aliDownload, error) {
	var d aliDownload
	err := c.call("/v2/file/get_download_url", map[string]any{"drive_id": c.driveID, "file_id": fileID, "expire_sec": 14400}, &d, "", aliCanaryDrive)
	if err == nil && d.URL == "" {
		err = fmt.Errorf("阿里云盘没给下载链接")
	}
	return d, err
}

// deleteFile 彻底删除（不进回收站：回收站照样占空间）。只用来删本站自己建的临时目录
func (c *aliClient) deleteFile(fileID string) error {
	return c.call("/v3/file/delete", map[string]any{"drive_id": c.driveID, "file_id": fileID}, nil, "", aliCanaryDrive)
}

// aliRangeSHA1 读源文件 [start, end]（含两端）这一段算 SHA1（115 二次认证用，一般只有几十 KB）
func aliRangeSHA1(link, spec string) (string, error) {
	var start, end int64
	if _, err := fmt.Sscanf(spec, "%d-%d", &start, &end); err != nil || start < 0 || end < start {
		return "", fmt.Errorf("115 给的校验区间认不出：%q", spec)
	}
	if end-start > 16<<20 {
		return "", fmt.Errorf("115 要校验的区间太大：%s", spec)
	}
	req, _ := http.NewRequest(http.MethodGet, link, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("Referer", aliReferer)
	resp, err := aliHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("读阿里云盘文件片段失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("阿里云盘不支持按区间读（HTTP %d）", resp.StatusCode)
	}
	h := sha1.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, end-start+1))
	if err != nil {
		return "", fmt.Errorf("读阿里云盘文件片段失败: %w", err)
	}
	if n != end-start+1 {
		return "", fmt.Errorf("阿里云盘只给了 %d 字节，要 %d", n, end-start+1)
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// ---- 扫码登录（p115strmhelper core/aliyunpan.py 同款接口）----

// aliQRCode 二维码内容与轮询用的 t / ck
type aliQRCode struct {
	T       string `json:"t"`
	CK      string `json:"ck"`
	Content string `json:"content"`
}

func aliQRGenerate() (aliQRCode, error) {
	q := url.Values{"appName": {"aliyun_drive"}, "fromSite": {"52"}, "appEntrance": {"web"}, "isMobile": {"false"}, "lang": {"zh_CN"}, "returnUrl": {""}}
	req, _ := http.NewRequest(http.MethodGet, aliPassportBase+"/newlogin/qrcode/generate.do?"+q.Encode(), nil)
	var resp struct {
		Content struct {
			Data struct {
				T           json.Number `json:"t"`
				CK          string      `json:"ck"`
				CodeContent string      `json:"codeContent"`
			} `json:"data"`
		} `json:"content"`
	}
	if err := aliDo(req, &resp); err != nil {
		return aliQRCode{}, err
	}
	d := resp.Content.Data
	if d.CodeContent == "" || d.CK == "" {
		return aliQRCode{}, fmt.Errorf("阿里云盘没给二维码")
	}
	return aliQRCode{T: d.T.String(), CK: d.CK, Content: d.CodeContent}, nil
}

var reAliBizRefresh = regexp.MustCompile(`"refreshToken"\s*:\s*"([^"]+)"`)

// aliQRQuery 查扫码状态：NEW / SCANED / CONFIRMED / EXPIRED / CANCELED；确认后带 refresh token
func aliQRQuery(t, ck string) (status, refresh string, err error) {
	form := url.Values{"t": {t}, "ck": {ck}, "appName": {"aliyun_drive"}, "appEntrance": {"web"}, "isMobile": {"false"},
		"lang": {"zh_CN"}, "returnUrl": {""}, "fromSite": {"52"}, "navlanguage": {"zh-CN"}, "navPlatform": {"Win32"}}
	req, _ := http.NewRequest(http.MethodPost, aliPassportBase+"/newlogin/qrcode/query.do?appName=aliyun_drive&fromSite=52", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", aliPassportBase)
	req.Header.Set("Referer", aliPassportBase+"/mini_login.htm?&appName=aliyun_drive")
	var resp struct {
		Content struct {
			Data struct {
				QrCodeStatus string `json:"qrCodeStatus"`
				BizExt       string `json:"bizExt"`
			} `json:"data"`
		} `json:"content"`
	}
	if err := aliDo(req, &resp); err != nil {
		return "", "", err
	}
	d := resp.Content.Data
	if d.QrCodeStatus != "CONFIRMED" {
		return d.QrCodeStatus, "", nil
	}
	// bizExt 是 base64 的 JSON（GBK 编码，昵称可能是乱码），只按正则取 refreshToken
	raw, derr := base64.StdEncoding.DecodeString(d.BizExt)
	if derr != nil {
		return "", "", fmt.Errorf("扫码结果认不出: %w", derr)
	}
	m := reAliBizRefresh.FindSubmatch(raw)
	if m == nil {
		return "", "", fmt.Errorf("扫码结果里没有 refresh token")
	}
	return "CONFIRMED", string(m[1]), nil
}

// ---- 链接 ----

// reAliShare 阿里云盘分享链接：alipan.com/s/<share_id>、aliyundrive.com/s/<share_id>（后面可能跟 /folder/…）
var reAliShare = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?(?:alipan|aliyundrive)\.com/s/([a-z0-9]{8,})(?:/folder/[a-z0-9]+)?`)

// reAliSharePwd 链接里带的提取码 ?pwd=xxxx
var reAliSharePwd = regexp.MustCompile(`(?i)[?&]pwd=([a-z0-9]{4})`)

// isAliShareLink 是不是阿里云盘分享
func isAliShareLink(raw string) bool { return reAliShare.MatchString(raw) }

// splitAliShare 从一段文本里取分享链接、share_id 与提取码（链接里的 pwd，或文案里的「提取码：xxxx」）
func splitAliShare(text, code string) (link, shareID, pwd string) {
	loc := reAliShare.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", "", ""
	}
	link, shareID = text[loc[0]:loc[1]], text[loc[2]:loc[3]]
	pwd = strings.TrimSpace(code)
	if pwd == "" {
		if m := reAliSharePwd.FindStringSubmatch(text[loc[1]:]); m != nil && strings.HasPrefix(text[loc[1]:], "?") {
			pwd = m[1]
		} else if m := reBotShareCode.FindStringSubmatch(text); m != nil {
			pwd = m[1]
		}
	}
	if !strings.HasPrefix(strings.ToLower(link), "http") {
		link = "https://" + link
	}
	return link, shareID, pwd
}
