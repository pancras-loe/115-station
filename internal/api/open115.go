package api

// ==================== 115 开放平台 OpenAPI 客户端 ====================
//
// 参考 LitePan (drivers/115_Open) 与 QMediaSync (internal/v115open) 的实现。
// 与 webapi+Cookie 通道的本质区别：
//   - 认证：OAuth PKCE 设备码（access_token 约2小时 + refresh_token 自动续期）
//   - 接口：proapi.115.com/open/*，115 官方授权的第三方接口，无 UA 指纹风控
//   - 错误码明确：770004 频率过高 / 406 额度上限 / 401401xx 需刷新 token
//
// 需要在 115 开放平台（https://open.115.com）申请应用获得 AppID(client_id)。
//
// 登录流程（PKCE 设备码）：
//  1. POST passportapi.115.com/open/authDeviceCode   (client_id + code_challenge)
//  2. 轮询 qrcodeapi.115.com/get/status/              (status: 1=已扫码 2=已确认)
//  3. POST passportapi.115.com/open/deviceCodeToToken (uid + code_verifier)
//  4. 刷新 POST passportapi.115.com/open/refreshToken (refresh_token)

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strmhub/internal/config"
	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	openBase      = "https://proapi.115.com"
	openAuthCode  = "https://passportapi.115.com/open/authDeviceCode"
	openDevToken  = "https://passportapi.115.com/open/deviceCodeToToken"
	openRefresh   = "https://passportapi.115.com/open/refreshToken"
	openStatusAPI = "https://qrcodeapi.115.com/get/status/"
	// 开放平台是官方授权接口，无需伪装客户端 UA（LitePan 直接用普通 Chrome UA）
	openUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	// open API 路径（LitePan transport.go）
	openPathList    = "/open/ufile/files"
	openPathInfo    = "/open/folder/get_info"
	openPathDownurl = "/open/ufile/downurl"
	openPathMkdir   = "/open/folder/add"
	openPathMove    = "/open/ufile/move"

	openTokenLead  = 5 * time.Minute // 过期前提前刷新
	openRetryDelay = time.Second
)

// open API 错误码（QMediaSync constants.go）
const (
	openCodeTokenExpired1 = 40140126 // 刷新 access_token
	openCodeTokenExpired2 = 40140125 // 刷新 access_token
	openCodeAuthInvalid   = 40140124 // 刷新 access_token
	openCodeRefreshDead   = 40140116 // refresh_token 失效，需重新授权
	openCodeRateHigh      = 770004   // 访问频率过高
	openCodeQuotaLimit    = 406      // 额度上限
)

// openErr 带错误码的 OpenAPI 错误
type openErr struct {
	Code    int64
	Message string
}

func (e *openErr) Error() string {
	return fmt.Sprintf("115 OpenAPI 错误(%d): %s", e.Code, e.Message)
}

func isOpenAuthErr(err error) bool {
	oe, ok := err.(*openErr)
	if !ok {
		return false
	}
	c := oe.Code
	return c == openCodeTokenExpired1 || c == openCodeTokenExpired2 || c == openCodeAuthInvalid
}

// ==================== 客户端 ====================

// open115Client 开放平台客户端（无 Handler 依赖，302 代理也可用）
type open115Client struct {
	cfg   *config.Config
	appID string

	mu         sync.Mutex
	token      *config.OpenToken // 内存缓存
	tokenAt    time.Time         // 缓存加载时间
	refreshing bool
}

var (
	openClientMu   sync.Mutex
	openClientInst *open115Client // 单例：appID 相同则复用
)

// newOpen115 创建客户端
func newOpen115(cfg *config.Config, appID string) *open115Client {
	return &open115Client{cfg: cfg, appID: appID}
}

// open115FromDB 从数据库读取启用状态，未启用或未授权返回 nil
func open115FromDB(db *gorm.DB, cfg *config.Config) *open115Client {
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err != nil {
		return nil
	}
	if !storage.OpenapiEnabled || strings.TrimSpace(storage.AppID) == "" {
		return nil
	}
	appID := strings.TrimSpace(storage.AppID)
	openClientMu.Lock()
	inst := openClientInst
	openClientMu.Unlock()
	if inst != nil && inst.appID == appID {
		return inst
	}
	inst = newOpen115(cfg, appID)
	openClientMu.Lock()
	openClientInst = inst
	openClientMu.Unlock()
	return inst
}

// getOpen115 Handler 快捷方式：启用 OpenAPI 且已授权时返回客户端，否则 nil
func (h *Handler) getOpen115() *open115Client {
	return open115FromDB(h.DB, h.Config)
}

// loadToken 读取 token（文件 -> 内存缓存）
func (o *open115Client) loadToken() *config.OpenToken {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != nil && time.Since(o.tokenAt) < 30*time.Second {
		return o.token
	}
	t, err := o.cfg.LoadOpenToken()
	if err != nil || t == nil || t.AccessToken == "" {
		return nil
	}
	if t.AppID != "" && t.AppID != o.appID {
		return nil // token 属于其他 AppID
	}
	o.token = t
	o.tokenAt = time.Now()
	return t
}

// saveToken 保存 token（文件 + 缓存）
func (o *open115Client) saveToken(t *config.OpenToken) {
	t.AppID = o.appID
	o.mu.Lock()
	o.token = t
	o.tokenAt = time.Now()
	o.mu.Unlock()
	if err := o.cfg.SaveOpenToken(t); err != nil {
		log.Printf("[系统] 保存 token 失败: %v", err)
	}
}

// clearToken 清空 token（refresh_token 失效时）
func (o *open115Client) clearToken() {
	o.mu.Lock()
	o.token = nil
	o.mu.Unlock()
	o.cfg.ClearOpenToken()
}

// authorized 是否已完成授权
func (o *open115Client) authorized() bool {
	return o.loadToken() != nil
}

// ensureToken 确保 access_token 可用（过期前 openTokenLead 刷新）
func (o *open115Client) ensureToken() (string, error) {
	t := o.loadToken()
	if t == nil {
		return "", &openErr{Code: openCodeRefreshDead, Message: "尚未授权开放平台，请先扫码登录"}
	}
	if time.Now().Add(openTokenLead).Unix() < t.ExpiresAt && t.AccessToken != "" {
		return t.AccessToken, nil
	}
	nt, err := o.doRefresh(t.RefreshToken)
	if err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

// doRefresh 刷新 token（带并发去重）
func (o *open115Client) doRefresh(refreshToken string) (*config.OpenToken, error) {
	o.mu.Lock()
	if o.refreshing {
		o.mu.Unlock()
		// 等待其他协程刷新完成后读缓存
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if t := o.loadToken(); t != nil && t.AccessToken != "" {
				return t, nil
			}
		}
		return nil, fmt.Errorf("等待 token 刷新超时")
	}
	o.refreshing = true
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.refreshing = false
		o.mu.Unlock()
	}()

	form := url.Values{"refresh_token": {refreshToken}}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := openPost(openRefresh, form, &data); err != nil {
		if oe, ok := err.(*openErr); ok && oe.Code == openCodeRefreshDead {
			o.clearToken()
			return nil, fmt.Errorf("授权已失效，请重新扫码登录")
		}
		return nil, fmt.Errorf("刷新 token 失败: %w", err)
	}
	if data.AccessToken == "" {
		return nil, fmt.Errorf("刷新 token 失败：响应为空")
	}
	nt := &config.OpenToken{
		AppID:        o.appID,
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(data.ExpiresIn) * time.Second).Unix(),
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = refreshToken
	}
	o.saveToken(nt)
	log.Printf("[系统] 授权已自动续期，有效期至 %s", time.Unix(nt.ExpiresAt, 0).Format("15:04:05"))
	return nt, nil
}

// ==================== HTTP 基础 ====================

// openPost 发起 open 平台表单 POST（passport 域，无 Bearer）
func openPost(api string, form url.Values, out any) error {
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", openUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return openDo(req, out)
}

// openDo 执行请求并解析 {state, code, message, data} 信封
func openDo(req *http.Request, out any) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &openErr{Code: openCodeAuthInvalid, Message: "HTTP 401 未授权"}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("115 OpenAPI HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	var env struct {
		State   json.RawMessage `json:"state"`
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("115 OpenAPI 返回非 JSON: %s", truncateStr(string(body), 200))
	}
	if !openStateOK(env.State) {
		msg := env.Message
		if msg == "" {
			msg = "未知错误"
		}
		return &openErr{Code: env.Code, Message: msg}
	}
	if out != nil && len(env.Data) > 0 && string(strings.TrimSpace(string(env.Data))) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			// data 结构不符时降级：把原始 data 给 json.RawMessage 类型的 out
			if raw, ok := out.(*json.RawMessage); ok {
				*raw = env.Data
				return nil
			}
			return fmt.Errorf("解析 OpenAPI data 失败: %v", err)
		}
	}
	return nil
}

// openStateOK state 兼容 true / 1 / "true"（LitePan isSuccessState）
func openStateOK(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return true
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n == 1
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.EqualFold(strings.TrimSpace(str), "true")
	}
	return false
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// apiCall 统一 API 调用：自动带 Bearer、过期刷新重试一次
func (o *open115Client) apiCall(method, path string, query url.Values, form url.Values, out any) error {
	throttle115(openBase) // 复用全局节流
	token, err := o.ensureToken()
	if err != nil {
		return err
	}
	err = o.rawCall(token, method, path, query, form, out)
	if err != nil && isOpenAuthErr(err) {
		// token 失效：强制刷新后重试一次
		t := o.loadToken()
		if t == nil {
			return err
		}
		nt, rerr := o.doRefresh(t.RefreshToken)
		if rerr != nil {
			return rerr
		}
		return o.rawCall(nt.AccessToken, method, path, query, form, out)
	}
	return err
}

// rawCall 发起一次带 Bearer 的请求（完成后推进全局节流锚点——apiCall 只在
// 请求前 throttle，锚点不推进的话连续 OpenAPI 调用之间实际没有强制间隔）
func (o *open115Client) rawCall(token, method, path string, query url.Values, form url.Values, out any) error {
	full := openBase + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var body io.Reader
	if len(form) > 0 {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, full, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", openUA)
	req.Header.Set("Authorization", "Bearer "+token)
	if len(form) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	err = openDo(req, out)
	throttle115Done(openBase)
	return err
}

// ==================== PKCE 扫码登录 ====================

// openGenVerifier 生成 64 位随机 code_verifier
func openGenVerifier() string {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("strmhub%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// openGenChallenge code_challenge = base64url(SHA256(verifier))
func openGenChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// openQrSession 扫码会话（uid -> verifier）
type openQrSession struct {
	AppID        string
	CodeVerifier string
}

var openQrSessions sync.Map

// CreateOpenQrCode 获取开放平台授权二维码
// POST /storage/open/qrcode  body: {"app_id": "..."}（为空则用已保存的）
func (h *Handler) CreateOpenQrCode(c *gin.Context) {
	var req struct {
		AppID string `json:"app_id"`
	}
	_ = c.ShouldBindJSON(&req)
	appID := strings.TrimSpace(req.AppID)
	if appID == "" {
		var storage model.Storage
		if err := h.DB.Where("type = ?", "115").First(&storage).Error; err == nil {
			appID = strings.TrimSpace(storage.AppID)
		}
	}
	if appID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写开放平台 AppID"})
		return
	}

	verifier := openGenVerifier()
	form := url.Values{
		"client_id":             {appID},
		"code_challenge":        {openGenChallenge(verifier)},
		"code_challenge_method": {"sha256"},
	}
	var data struct {
		Uid  string `json:"uid"`
		Time int64  `json:"time"`
		Sign string `json:"sign"`
	}
	if err := openPost(openAuthCode, form, &data); err != nil {
		log.Printf("[系统] 获取授权二维码失败: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取授权二维码失败: " + err.Error()})
		return
	}
	if data.Uid == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取授权二维码失败：AppID 可能无效"})
		return
	}

	// 二维码图片
	imgBody, err := httpGetJSON(qrcodeImgAPI, url.Values{"uid": {data.Uid}}, 15*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取二维码图片失败: " + err.Error()})
		return
	}
	openQrSessions.Store(data.Uid, openQrSession{AppID: appID, CodeVerifier: verifier})

	c.JSON(http.StatusOK, gin.H{
		"qrcode": "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBody),
		"uid":    data.Uid,
		"time":   data.Time,
		"sign":   data.Sign,
	})
}

// OpenQrCodeStatus 轮询开放平台扫码状态；确认后换取 token 并保存
// POST /storage/open/qrcode/status  body: {"uid":"","time":0,"sign":""}
func (h *Handler) OpenQrCodeStatus(c *gin.Context) {
	var req struct {
		Uid  string `json:"uid"`
		Time int64  `json:"time"`
		Sign string `json:"sign"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Uid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	query := url.Values{"uid": {req.Uid}, "time": {fmt.Sprint(req.Time)}, "sign": {req.Sign}}
	body, err := httpGetJSON(openStatusAPI, query, 60*time.Second)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
		return
	}
	var st struct {
		Data struct {
			Status int `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "解析状态失败"})
		return
	}

	switch st.Data.Status {
	case 0:
		c.JSON(http.StatusOK, gin.H{"status": "waiting"})
	case 1:
		c.JSON(http.StatusOK, gin.H{"status": "scanned"})
	case 2:
		// 已确认：换取 token
		sessVal, ok := openQrSessions.Load(req.Uid)
		if !ok {
			c.JSON(http.StatusBadGateway, gin.H{"error": "会话已失效，请重新获取二维码"})
			return
		}
		sess := sessVal.(openQrSession)
		form := url.Values{"uid": {req.Uid}, "code_verifier": {sess.CodeVerifier}}
		var data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := openPost(openDevToken, form, &data); err != nil {
			log.Printf("[系统] 换取 token 失败: %v", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "换取访问凭证失败: " + err.Error()})
			return
		}
		if data.AccessToken == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "换取访问凭证失败：响应为空"})
			return
		}
		oc := newOpen115(h.Config, sess.AppID)
		oc.saveToken(&config.OpenToken{
			AppID:        sess.AppID,
			AccessToken:  data.AccessToken,
			RefreshToken: data.RefreshToken,
			ExpiresAt:    time.Now().Add(time.Duration(data.ExpiresIn) * time.Second).Unix(),
		})
		openQrSessions.Delete(req.Uid)
		log.Printf("[系统] 开放平台授权成功 %d 秒", data.ExpiresIn)

		// 同步更新 Storage 表（标记 OpenAPI 已授权）
		h.upsertOpenStorage(sess.AppID)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	case -1:
		openQrSessions.Delete(req.Uid)
		log.Printf("[系统] ○ 开放平台授权二维码已过期（uid=%s），前端会自动换新码", truncateStr(req.Uid, 12))
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
	case -2:
		openQrSessions.Delete(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
	default:
		log.Printf("[系统] ○ 开放平台扫码状态异常: %d（按过期处理，uid=%s）", st.Data.Status, truncateStr(req.Uid, 12))
		openQrSessions.Delete(req.Uid)
		c.JSON(http.StatusOK, gin.H{"status": "expired"})
	}
}

// upsertOpenStorage 更新 Storage 表的 OpenAPI 配置
func (h *Handler) upsertOpenStorage(appID string) {
	var storage model.Storage
	if err := h.DB.Where("type = ?", "115").First(&storage).Error; err != nil {
		h.DB.Create(&model.Storage{
			Name: "OpenAPI", Type: "115", Status: "online",
			OpenapiEnabled: true, AppID: appID,
		})
		return
	}
	storage.OpenapiEnabled = true
	storage.AppID = appID
	storage.Status = "online"
	h.DB.Save(&storage)
}

// ==================== 文件条目（LitePan fileEntry 字段兼容）====================

// openFileEntry 开放平台文件条目
type openFileEntry struct {
	Fid          string      `json:"fid"`
	FileID       string      `json:"file_id"`
	Fn           string      `json:"fn"`
	FileName     string      `json:"file_name"`
	Fc           json.Number `json:"fc"`
	FileCategory json.Number `json:"file_category"`
	Pid          string      `json:"pid"`
	Cid          string      `json:"cid"`
	Pc           string      `json:"pc"`
	PickCode     string      `json:"pick_code"`
	Pickcode     string      `json:"pickcode"`
	Sha1         string      `json:"sha1"`
	Size         json.Number `json:"size"`
	S            json.Number `json:"s"`
	SizeByte     json.Number `json:"size_byte"`
}

func (e *openFileEntry) entryID() string {
	if v := strings.TrimSpace(e.Fid); v != "" {
		return v
	}
	return strings.TrimSpace(e.FileID)
}

func (e *openFileEntry) name() string {
	if v := strings.TrimSpace(e.Fn); v != "" {
		return v
	}
	return strings.TrimSpace(e.FileName)
}

// isDirectory file_category/fc == 0 为目录
func (e *openFileEntry) isDirectory() bool {
	if s := strings.TrimSpace(e.FileCategory.String()); s != "" {
		return s == "0"
	}
	return e.Fc.String() == "0"
}

func (e *openFileEntry) pickCode() string {
	for _, s := range []string{e.Pc, e.PickCode, e.Pickcode} {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}

func (e *openFileEntry) sizeInt() int64 {
	for _, n := range []json.Number{e.S, e.SizeByte, e.Size} {
		if v, err := n.Int64(); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// toWebapiMap 转成 webapi 兼容 map（f/n/fid/cid/s/pc），供现有遍历逻辑复用
// 注意：webapi 中目录条目的 cid = 目录自身 id；open 中自身 id 是 fid
func (e *openFileEntry) toWebapiMap() map[string]interface{} {
	m := map[string]interface{}{
		"n":   e.name(),
		"fid": e.entryID(),
		"pc":  e.pickCode(),
		"s":   float64(e.sizeInt()),
	}
	if e.isDirectory() {
		m["f"] = "0"
		m["cid"] = e.entryID() // 目录自身 id
	} else {
		m["f"] = "1"
		m["cid"] = strings.TrimSpace(e.Cid)
	}
	return m
}

// ==================== 文件操作 ====================

// listEntries 拉取一页条目（webapi 兼容格式）
func (o *open115Client) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	query := url.Values{
		"cid":      {cid},
		"limit":    {"1000"},
		"offset":   {fmt.Sprint(offset)},
		"show_dir": {"1"},
	}
	// LitePan listPageResp：顶层平铺 count + data 数组（apiCallFull 解析整个 body）
	var page struct {
		State   json.RawMessage `json:"state"`
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Count   int             `json:"count"`
		Data    []openFileEntry `json:"data"`
	}
	token, err := o.ensureToken()
	if err != nil {
		return nil, 0, err
	}
	throttle115(openBase) // 全量同步走此通道列目录，不加节流会高频触发风控（此前完全绕过限流器）
	defer throttle115Done(openBase)
	req, err := http.NewRequest(http.MethodGet, openBase+openPathList+"?"+query.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", openUA)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, fmt.Errorf("解析 OpenAPI 列表失败: %s", truncateStr(string(body), 200))
	}
	if !openStateOK(page.State) {
		oerr := &openErr{Code: page.Code, Message: page.Message}
		if isOpenAuthErr(oerr) {
			// 刷新后重试一次
			t := o.loadToken()
			if t == nil {
				return nil, 0, oerr
			}
			nt, rerr := o.doRefresh(t.RefreshToken)
			if rerr != nil {
				return nil, 0, rerr
			}
			_ = nt
			// 简化：直接返回让上层重试
			return nil, 0, fmt.Errorf("token 已刷新，请重试")
		}
		return nil, 0, oerr
	}
	// 兼容 data 为对象内嵌 list 的返回格式
	if page.Data == nil {
		var alt struct {
			Data struct {
				Count int             `json:"count"`
				List  []openFileEntry `json:"list"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &alt); err == nil && alt.Data.List != nil {
			page.Data = alt.Data.List
			if page.Count == 0 {
				page.Count = alt.Data.Count
			}
		}
	}
	out := make([]map[string]interface{}, 0, len(page.Data))
	for i := range page.Data {
		out = append(out, page.Data[i].toWebapiMap())
	}
	return out, page.Count, nil
}

// listDirs 目录浏览（只返回文件夹；翻页取全，大目录不再截断在第一页）
func (o *open115Client) listDirs(cid string) ([]gin.H, error) {
	dirs := make([]gin.H, 0, 64)
	offset := 0
	for {
		entries, count, err := o.listEntries(cid, offset)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if fmt.Sprint(e["f"]) == "0" {
				dirs = append(dirs, gin.H{"cid": fmt.Sprint(e["cid"]), "name": fmt.Sprint(e["n"])})
			}
		}
		offset += len(entries)
		if len(entries) == 0 || offset >= count {
			return dirs, nil
		}
	}
}

// ping 验证 token 有效（列 1 条）
func (o *open115Client) ping() error {
	_, _, err := o.listEntries("0", 0)
	return err
}

// mkdir 创建目录，返回新目录 id
func (o *open115Client) mkdir(parent, name string) (string, error) {
	form := url.Values{"pid": {parent}, "file_name": {name}}
	var data struct {
		Cid    string `json:"cid"`
		FileID string `json:"file_id"`
	}
	if err := o.apiCall(http.MethodPost, openPathMkdir, nil, form, &data); err != nil {
		return "", err
	}
	if data.Cid != "" {
		return data.Cid, nil
	}
	return data.FileID, nil
}

// moveFiles 移动文件/目录到目标目录
func (o *open115Client) moveFiles(targetCid string, fids []string) error {
	if len(fids) == 0 {
		return nil
	}
	form := url.Values{
		"file_ids": {strings.Join(fids, ",")},
		"to_cid":   {targetCid},
	}
	return o.apiCall(http.MethodPost, openPathMove, nil, form, nil)
}

// downloadURL 通过 pickcode 获取下载直链（LitePan ResolveDownload）
func (o *open115Client) downloadURL(pickcode string) (string, error) {
	form := url.Values{"pick_code": {pickcode}}
	var raw json.RawMessage
	if err := o.apiCall(http.MethodPost, openPathDownurl, nil, form, &raw); err != nil {
		return "", err
	}
	u := openParseDownloadURL(raw)
	if u == "" {
		return "", fmt.Errorf("未获取到下载链接")
	}
	return u, nil
}

// openParseDownloadURL 多格式解析下载链接（LitePan parseDownloadURL 简化版）
func openParseDownloadURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 直接字符串
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(s, "http") {
			return s
		}
	}
	// {url: {url: "..."} } 或 {url: "..."}
	var obj struct {
		URL json.RawMessage `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && len(obj.URL) > 0 {
		var u1 string
		if json.Unmarshal(obj.URL, &u1) == nil && strings.HasPrefix(u1, "http") {
			return u1
		}
		var nested struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(obj.URL, &nested) == nil && strings.HasPrefix(nested.URL, "http") {
			return nested.URL
		}
	}
	// {file_id: {url: {...}}} 形式：取第一个值
	var byID map[string]json.RawMessage
	if json.Unmarshal(raw, &byID) == nil {
		for _, v := range byID {
			if u := openParseDownloadURL(v); u != "" {
				return u
			}
		}
	}
	return ""
}

// ==================== 双通道分发层 ====================

// pan115Ops 统一 115 操作通道：OpenAPI 优先，Cookie 回退
// 所有上层调用（目录浏览/同步/整理/302代理）都通过本结构，不再直接持有 cookie
type pan115Ops struct {
	open   *open115Client // OpenAPI 通道（nil 表示走 cookie）
	cookie string         // Cookie 通道
}

// newPan115Ops 构造操作通道：OpenAPI 启用且已授权则优先
func (h *Handler) newPan115Ops() (*pan115Ops, error) {
	if oc := h.getOpen115(); oc != nil && oc.authorized() {
		ops := &pan115Ops{open: oc}
		// 重命名等操作 OpenAPI 暂无接口，带上 Cookie 作回退（有则可用）
		if ck, err := h.get115Cookie(); err == nil && ck != "" {
			ops.cookie = ck
		}
		return ops, nil
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return nil, err
	}
	return &pan115Ops{cookie: cookie}, nil
}

// channelName 当前通道名（日志用）
func (o *pan115Ops) channelName() string {
	if o.open != nil {
		return "OpenAPI"
	}
	return "Cookie(webapi)"
}

// listEntries 拉取目录一页条目
func (o *pan115Ops) listEntries(cid string, offset int) ([]map[string]interface{}, int, error) {
	if o.open != nil {
		return o.open.listEntries(cid, offset)
	}
	return list115Entries(o.cookie, cid, offset)
}

// listDirs 目录浏览（只返回文件夹），返回列表、总条目数、命中域名
func (o *pan115Ops) listDirs(cid string) ([]gin.H, int, string, error) {
	if o.open != nil {
		dirs, err := o.open.listDirs(cid)
		return dirs, len(dirs), "open", err
	}
	return fetch115Dirs(o.cookie, ua115Unified(), cid)
}

// mkdir 创建目录
func (o *pan115Ops) mkdir(parent, name string) (string, error) {
	if o.open != nil {
		return o.open.mkdir(parent, name)
	}
	return mkdir115(o.cookie, parent, name)
}

// ensurePath 逐级创建目录路径
func (o *pan115Ops) ensurePath(parent, dirPath string) (string, error) {
	if o.open != nil {
		return o.openEnsurePath(parent, dirPath)
	}
	return ensure115Path(o.cookie, parent, dirPath)
}

// openEnsurePath OpenAPI 版逐级建目录
func (o *pan115Ops) openEnsurePath(parent, dirPath string) (string, error) {
	current := parent
	for _, part := range strings.Split(dirPath, "/") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 翻页查找同名子目录（只查第一页时大目录会漏 → 误走 mkdir 重名失败）
		existing := ""
		offset := 0
		for existing == "" {
			entries, count, err := o.open.listEntries(current, offset)
			if err != nil {
				return "", err
			}
			for _, e := range entries {
				if fmt.Sprint(e["f"]) == "0" && fmt.Sprint(e["n"]) == part {
					existing = fmt.Sprint(e["cid"])
					break
				}
			}
			offset += len(entries)
			if len(entries) == 0 || offset >= count {
				break
			}
		}
		if existing != "" {
			current = existing
			continue
		}
		newID, err := o.open.mkdir(current, part)
		if err != nil {
			return "", err
		}
		current = newID
	}
	return current, nil
}

// rename 重命名网盘文件（字幕随视频新名对齐用）。
// OpenAPI 暂无重命名接口：回退 Cookie 通道（未配置 Cookie 时明确报错）
func (o *pan115Ops) rename(fid, newName string) error {
	if o.open != nil && o.cookie == "" {
		return fmt.Errorf("OpenAPI 通道暂不支持重命名，且未配置 Cookie 无法回退（账号管理 → 二维码登录可补 Cookie）")
	}
	return rename115(o.cookie, fid, newName)
}

// renameBatch 批量重命名（一次调用）；OpenAPI 模式同样回退 Cookie 通道
func (o *pan115Ops) renameBatch(names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	if o.open != nil && o.cookie == "" {
		return fmt.Errorf("OpenAPI 通道暂不支持批量重命名，且未配置 Cookie 无法回退（账号管理 → 二维码登录可补 Cookie）")
	}
	return rename115Batch(o.cookie, names)
}

// moveFiles 移动文件
func (o *pan115Ops) moveFiles(targetCid string, fids []string) error {
	if o.open != nil {
		return o.open.moveFiles(targetCid, fids)
	}
	return move115Files(o.cookie, targetCid, fids)
}

// downloadURL 获取下载直链（默认签发 UA）
func (o *pan115Ops) downloadURL(pickcode string) (string, error) {
	u, _, err := o.downloadURLFull(pickcode, "")
	return u, err
}

// downloadURLFull 获取下载直链及 CDN 要求的请求头（直链响应 Set-Cookie 下发）
// ua 为签发 UA（空则用默认浏览器 UA）；直链与签发 UA 绑定，302 场景应传播放端 UA
func (o *pan115Ops) downloadURLFull(pickcode, ua string) (string, map[string]string, error) {
	if o.open != nil {
		u, err := o.open.downloadURL(pickcode)
		return u, nil, err
	}
	return get115DownloadURL(pickcode, o.cookie, ua)
}

// cookieForDL 附属文件下载重试用的登录 Cookie（OpenAPI 通道返回空）
func (o *pan115Ops) cookieForDL() string {
	return o.cookie
}

// proxyDownloadURL 302 代理专用：不依赖 Handler，直接从 DB+配置构造通道
// cfgCookie 115 Cookie 读取（配置文件优先，回退 DB Storage；两通道共用）
func cfgCookie(db *gorm.DB, cfg *config.Config) string {
	if ck, err := cfg.LoadCookie(); err == nil && ck != "" {
		return ck
	}
	var storage model.Storage
	if err := db.Where("type = ?", "115").First(&storage).Error; err == nil {
		return storage.Cookie
	}
	return ""
}

func proxyDownloadURL(db *gorm.DB, cfg *config.Config, pickcode, ua string) (string, error) {
	u, _, err := proxyDownloadURLFull(db, cfg, pickcode, ua)
	return u, err
}

// proxyDownloadURLFull 同 proxyDownloadURL，但一并返回直链要求的请求头
// （含必须绑定的 User-Agent，服务端中转拉流时使用）
func proxyDownloadURLFull(db *gorm.DB, cfg *config.Config, pickcode, ua string) (string, map[string]string, error) {
	// OpenAPI 通道（失败不直接报错——回退 Cookie 通道；OpenAPI 撞限流/额度
	// 时 302 与补全探测不能整体失败，而 Cookie 明明可用）
	if oc := open115FromDB(db, cfg); oc != nil && oc.authorized() {
		// 通道粘滞：Cookie 通道近期成功过 → 先走 Cookie（OpenAPI 撞限流/
		// 额度后短时间内大概率仍失败，跳过这次必败尝试）
		if pref, ok := dlFastGet(); ok && pref.kind == "web" {
			u, hdrs, err := get115DownloadURL(pickcode, cfgCookie(db, cfg), ua)
			if err == nil && u != "" {
				return u, hdrs, nil
			}
			log.Printf("[直链] ○ 粘滞 Cookie 通道失败，回退 OpenAPI: %v", err)
		}
		u, err := oc.downloadURL(pickcode)
		if err == nil && u != "" {
			dlFastSet("open", 10*time.Minute)
			return u, map[string]string{"User-Agent": ua}, nil
		}
		log.Printf("[直链] ○ OpenAPI 取链失败，回退 Cookie 通道: %v", err)
	}
	// Cookie 通道（文件优先，回退 DB）
	cookie := cfgCookie(db, cfg)
	if cookie == "" {
		return "", nil, fmt.Errorf("115 账号未绑定")
	}
	return get115DownloadURL(pickcode, cookie, ua)
}
