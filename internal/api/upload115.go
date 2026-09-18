package api

// ==================== 115 文件上传 + 监控上传引擎 ====================
//
// 上传双通道（与同步一致：OpenAPI 优先，Cookie 回退）：
//  · OpenAPI：POST {open}/open/upload/init（fileid=全量SHA1, preid=前128KB
//    SHA1, Bearer 鉴权）status=2 秒传 / 6/7/8 二次认证 / 1 走 OSS；
//    凭证 /open/upload/get_token，PUT {bucket}.{endpoint}/{object} 带回调头
//  · Cookie：SheltonZhu/115driver 库（OpenList 同款）——ECDH 加密的
//    uplb.115.com/4.0/initupload.php，秒传 + OSS 直传 + 结果校验全内置
//
// 监控上传（CMS media_moni 同款）：定期扫描本地媒体目录中 Emby 新生成的
// 图片，按目录结构对应上传到 115 剧集目录（本地 strm 结构与网盘一致）。

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"strmhub/internal/model"
)

// flexStatus 兼容数字/字符串两种 status 形态
type flexStatus int64

func (f *flexStatus) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `" `)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexStatus(n)
	return nil
}

// openUploadInit /open/upload/init 响应（data 内为平铺结构）
type openUploadInit struct {
	Status      flexStatus      `json:"status"`
	FileID      string          `json:"file_id"`
	Bucket      string          `json:"bucket"`
	Object      string          `json:"object"`
	Callback    json.RawMessage `json:"callback"`
	CallbackVar string          `json:"callback_var"`
	SignKey     string          `json:"sign_key"`
	SignCheck   string          `json:"sign_check"`
	PickCode    string          `json:"pick_code"`
}

// openOSSCredential OSS 上传临时凭证（字段名兼容多种形态）
type openOSSCredential struct {
	AK, SK, Token, Endpoint string
}

func openOSSTokenFromMap(raw map[string]any) openOSSCredential {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return openOSSCredential{
		AK:       get("access_key_id", "AccessKeyId", "accessKeyId"),
		SK:       get("access_key_secret", "AccessKeySecret", "accessKeySecret"),
		Token:    get("security_token", "SecurityToken", "securityToken"),
		Endpoint: get("endpoint", "Endpoint", "endPoint"),
	}
}

// upload115File 上传文件内容到 115 指定目录（秒传或 OSS 直传）。
// 双通道：OpenAPI 已授权走官方开放接口；否则回退 Cookie 通道
// （115driver 库，OpenList/CMS 同款，扫码登录即可上传）。
func (h *Handler) upload115File(cookie string, pid int64, filename string, data []byte) error {
	if oc := h.getOpen115(); oc != nil && oc.authorized() {
		return h.upload115FileOpen(oc, pid, filename, data)
	}
	if cookie != "" {
		return h.upload115FileCookie(cookie, pid, filename, data)
	}
	return fmt.Errorf("未找到可用的 115 凭据（OpenAPI 未授权且 Cookie 为空）")
}

// upload115FileCookie Cookie 通道上传（ECDH 加密 initupload + 秒传/OSS 直传，
// OpenList/115driver 同款协议）。init 请求自行实现：库内写死的 appversion
// （27.x）已被 115 拒绝（报「请升级到最新版本」），这里注入动态获取的
// 最新客户端版本号（与同步链路 getAppVerCached 同源）。
func (h *Handler) upload115FileCookie(cookie string, pid int64, filename string, data []byte) error {
	client := driver115.New(driver115.UA(h.get115UA()))
	cr := &driver115.Credential{}
	if err := cr.FromCookie(cookie); err != nil {
		return fmt.Errorf("Cookie 格式异常: %w", err)
	}
	client.ImportCredential(cr)

	fileSize := int64(len(data))
	fileID := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(data)))
	preLen := len(data)
	if preLen > 128*1024 {
		preLen = 128 * 1024
	}
	preID := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(data[:preLen])))
	dirID := strconv.FormatInt(pid, 10)

	// 上传属写操作，纳入全局节流（复用 proapi 域名的间隔锚点）
	throttle115(driver115.ApiUploadInfo)
	init, err := rapidUploadInit115(client, getAppVerCached(), fileSize, filename, dirID, preID, fileID, bytes.NewReader(data))
	throttle115Done(driver115.ApiUploadInfo)
	if err != nil {
		return fmt.Errorf("upload init 失败: %w", err)
	}
	fast, err := init.Ok()
	if err != nil {
		return fmt.Errorf("upload init 被拒: %w", err)
	}
	if fast {
		return nil // 秒传完成
	}
	return client.UploadByOSS(&init.UploadOSSParams, bytes.NewReader(data), dirID)
}

// rapidUploadInit115 Cookie 通道 upload init（115driver RapidUpload 同款
// 流程：ECDH 加密表单 + k_ec 令牌 + status 7 二次认证重试，可注入 appVer）
func rapidUploadInit115(client *driver115.Pan115Client, appVer string, fileSize int64, fileName, dirID, preID, fileID string, r io.ReadSeeker) (*driver115.UploadInitResp, error) {
	ecdh, err := ec115.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	if ok, err := client.UploadAvailable(); !ok || err != nil {
		return nil, err
	}
	userID := strconv.FormatInt(client.UserID, 10)
	target := "U_1_" + dirID
	fileSizeStr := strconv.FormatInt(fileSize, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileID)
	form.Set("target", target)
	form.Set("sig", client.GenerateSignature(fileID, target))
	form.Set("topupload", "true")

	signKey, signVal := "", ""
	result := &driver115.UploadInitResp{}
	for retry := true; retry; {
		t := driver115.NowMilli()
		encodedToken, err := ecdh.EncodeToken(t.ToInt64())
		if err != nil {
			return nil, err
		}
		form.Set("t", t.String())
		form.Set("token", uploadToken115(client.UserID, fileID, preID, t.String(), fileSizeStr, signKey, signVal, appVer))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		encrypted, err := ecdh.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		resp, err := client.NewRequest().
			SetQueryParams(map[string]string{"k_ec": encodedToken}).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true).
			Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.RawBody())
		resp.RawBody().Close()
		if err != nil {
			return nil, err
		}
		decrypted, err := ecdh.Decrypt(body)
		if err != nil {
			return nil, err
		}
		result = &driver115.UploadInitResp{}
		if err := driver115.CheckErr(json.Unmarshal(decrypted, result), result, resp); err != nil {
			return nil, err
		}
		if result.Status == 7 {
			signKey = result.SignKey
			signVal, _ = client.UploadDigestRange(r, result.SignCheck)
		} else {
			retry = false
		}
	}
	// 库的 RapidUpload 同款：SHA1 回填（UploadByOSS 上传后按它校验结果）
	result.SHA1 = fileID
	return result, nil
}

// uploadToken115 115driver GenerateToken 同款算法，appVer 可注入最新版本
func uploadToken115(userID int64, fileID, preID, timeStamp, fileSize, signKey, signVal, appVer string) string {
	uid := strconv.FormatInt(userID, 10)
	uidMd5 := md5.Sum([]byte(uid))
	tokenMd5 := md5.Sum([]byte("Qclm8MGWUv59TnrR0XPg" + fileID + fileSize + signKey + signVal +
		uid + timeStamp + hex.EncodeToString(uidMd5[:]) + appVer))
	return hex.EncodeToString(tokenMd5[:])
}

// upload115FileOpen OpenAPI 通道上传
func (h *Handler) upload115FileOpen(oc *open115Client, pid int64, filename string, data []byte) error {
	sha := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(data)))
	n := len(data)
	if n > 128*1024 {
		n = 128 * 1024
	}
	preid := strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(data[:n])))

	// 1. init（含二次认证重试：status 6/7/8 时按 sign_check 区间复验）
	signKey, signVal, pickCode := "", "", ""
	var init openUploadInit
	for round := 0; round < 3; round++ {
		form := url.Values{
			"file_name": {filename},
			"file_size": {fmt.Sprint(len(data))},
			"target":    {fmt.Sprintf("U_1_%d", pid)},
			"fileid":    {sha},
			"preid":     {preid},
			"topupload": {"0"},
		}
		if pickCode != "" {
			form.Set("pick_code", pickCode)
		}
		if signKey != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		if err := oc.apiCall(http.MethodPost, "/open/upload/init", nil, form, &init); err != nil {
			return fmt.Errorf("upload init 失败: %w", err)
		}
		switch init.Status {
		case 2:
			return nil // 秒传成功
		case 1:
			// 走 OSS 直传
		case 6, 7, 8:
			// 二次认证：sign_check = "offset-length"，取该区间 SHA1 复验
			off, length, ok := parseSignCheck(init.SignCheck, len(data))
			if !ok || init.SignKey == "" {
				return fmt.Errorf("上传需要二次认证但参数异常（sign_check=%q sign_key=%q）", init.SignCheck, init.SignKey)
			}
			signKey = init.SignKey
			signVal = strings.ToUpper(fmt.Sprintf("%x", sha1.Sum(data[off:off+length])))
			pickCode = init.PickCode
			continue
		default:
			return fmt.Errorf("upload init 被拒: status=%d", int64(init.Status))
		}
		break
	}
	if init.Bucket == "" || init.Object == "" {
		return fmt.Errorf("upload init 未返回 OSS 参数（bucket/object 为空）")
	}

	// 2. OSS 临时凭证（GET 失败再试 POST，LitePan 同款兜底）
	var tkRaw map[string]any
	if err := oc.apiCall(http.MethodGet, "/open/upload/get_token", nil, nil, &tkRaw); err != nil {
		tkRaw = nil
		_ = oc.apiCall(http.MethodPost, "/open/upload/get_token", nil, nil, &tkRaw)
	}
	tk := openOSSTokenFromMap(tkRaw)
	if tk.AK == "" || tk.SK == "" || tk.Token == "" {
		return fmt.Errorf("获取 OSS 上传凭证失败")
	}

	// 3. OSS PUT（带回调，由 115 服务端确认入库）
	return ossPutOpen(tk, init, sha, data)
}

// parseSignCheck 解析二次认证区间 "offset-length"，越界时收敛到文件内
func parseSignCheck(s string, size int) (int, int, bool) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	off, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	length, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || off < 0 || length <= 0 || off >= size {
		return 0, 0, false
	}
	if off+length > size {
		length = size - off
	}
	return off, length, true
}

// extractOSSCallback 解析 init 返回的回调配置（字符串/嵌套两种形态），
// 并把 ${sha1} 占位符替换为实际值
func extractOSSCallback(cbRaw json.RawMessage, cbVar, sha string) (callback, callbackVar string) {
	callback = ""
	if len(cbRaw) > 0 {
		var asString string
		if json.Unmarshal(cbRaw, &asString) == nil {
			callback = asString
		}
		var nested struct {
			Callback    string `json:"callback"`
			CallbackVar string `json:"callback_var"`
			Value       struct {
				Callback    string `json:"callback"`
				CallbackVar string `json:"callback_var"`
			} `json:"value"`
		}
		if json.Unmarshal(cbRaw, &nested) == nil {
			if nested.Callback != "" || nested.CallbackVar != "" {
				callback, cbVar = nested.Callback, nested.CallbackVar
			} else if nested.Value.Callback != "" {
				callback, cbVar = nested.Value.Callback, nested.Value.CallbackVar
			}
		}
		if callback == "" {
			callback = strings.TrimSpace(string(cbRaw))
		}
	}
	callback = strings.ReplaceAll(callback, "${sha1}", sha)
	return callback, cbVar
}

// ossPutOpen 阿里云 OSS PUT 上传（Aliyun OSS V1 签名规范 + 回调头）
func ossPutOpen(tk openOSSCredential, init openUploadInit, sha string, data []byte) error {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(tk.Endpoint, "https://"), "http://")
	if endpoint == "" {
		endpoint = "oss-cn-shenzhen.aliyuncs.com"
	}
	host := init.Bucket + "." + endpoint
	resource := "/" + init.Bucket + "/" + init.Object
	date := time.Now().UTC().Format(http.TimeFormat)
	contentType := "application/octet-stream"
	callback, callbackVar := extractOSSCallback(init.Callback, init.CallbackVar, sha)

	// 回调头（base64 编码后放入 x-oss-callback / x-oss-callback-var）
	var cbHeader, cbVarHeader string
	if callback != "" {
		cbHeader = base64.StdEncoding.EncodeToString([]byte(callback))
	}
	if callbackVar != "" {
		cbVarHeader = base64.StdEncoding.EncodeToString([]byte(callbackVar))
	}

	// OSS 签名：VERB\nMD5\nContentType\nDate\nCanonicalizedOSSHeaders+Resource
	// CanonicalizedOSSHeaders = 所有 x-oss-* 头按字典序 "k:v\n" 拼接
	var canon strings.Builder
	ossHeaders := []string{"x-oss-callback:" + cbHeader, "x-oss-callback-var:" + cbVarHeader, "x-oss-security-token:" + tk.Token}
	sort.Strings(ossHeaders)
	for _, h := range ossHeaders {
		if !strings.HasSuffix(h, ":") {
			canon.WriteString(h)
			canon.WriteString("\n")
		}
	}
	stringToSign := "PUT\n\n" + contentType + "\n" + date + "\n" + canon.String() + resource
	mac := hmac.New(sha1.New, []byte(tk.SK))
	mac.Write([]byte(stringToSign))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPut, "https://"+host+"/"+init.Object, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-oss-security-token", tk.Token)
	if cbHeader != "" {
		req.Header.Set("x-oss-callback", cbHeader)
	}
	if cbVarHeader != "" {
		req.Header.Set("x-oss-callback-var", cbVarHeader)
	}
	req.Header.Set("Authorization", "OSS "+tk.AK+":"+sign)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("OSS PUT 失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 && resp.StatusCode != 203 {
		return fmt.Errorf("OSS PUT HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 150))
	}
	// 回调结果里带错误信息时上报（如空间不足、目标目录异常）
	var cbResult struct {
		State   bool   `json:"state"`
		Message string `json:"message"`
	}
	if json.Unmarshal(respBody, &cbResult) == nil && cbResult.Message != "" && !cbResult.State {
		return fmt.Errorf("OSS 回调失败: %s", truncateStr(cbResult.Message, 150))
	}
	return nil
}

// ==================== 监控上传引擎 ====================

// fileStamp 已上传文件的指纹（修改时间+大小）。Emby 重新刮削会覆盖同名
// poster/nfo，指纹随之变化，下一轮扫描即检测到并重新上传（内容未变则
// 115 秒传命中，不会产生重复文件）。
type fileStamp struct {
	modTime time.Time
	size    int64
}

func stampOf(d os.DirEntry) (fileStamp, bool) {
	info, err := d.Info()
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, true
}

func stampOfPath(p string) (fileStamp, bool) {
	info, err := os.Stat(p)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, true
}

func (a fileStamp) same(b fileStamp) bool {
	return a.size == b.size && a.modTime.Equal(b.modTime)
}

// uploadStampDone 该文件是否已按当前指纹上传过（DB 持久化，重启不重复上传）
func uploadStampDone(db *gorm.DB, path string, st fileStamp) bool {
	var m model.UploadMark
	if err := db.Where("path = ?", path).First(&m).Error; err != nil {
		return false
	}
	return m.ModTime == st.modTime.UnixNano() && m.Size == st.size
}

// markUploadedStamp 记录上传指纹（存在则更新；Emby 覆盖文件后指纹变化触发重传）
func markUploadedStamp(db *gorm.DB, path string, st fileStamp) {
	db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"mod_time", "size"}),
	}).Create(&model.UploadMark{Path: path, ModTime: st.modTime.UnixNano(), Size: st.size})
}

// StartMonitorUploader 启动监控上传：定期扫描监控目录中新生成的图片，
// 按本地目录结构对应上传到 115 媒体库（本地 strm 树与网盘一致）
func StartMonitorUploader(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				monitorOnce(h)
			case <-stopCh:
				return
			}
		}
	}()
	log.Println("[监控上传] 引擎已启动（每分钟扫描一次监控目录）")
}

// monitorOnce 单轮扫描上传
// monitorRunning 触发轮与分钟 ticker 轮防重叠（同时走两轮会重复上传）
var monitorRunning atomic.Bool

func monitorOnce(h *Handler) {
	if !monitorRunning.CompareAndSwap(false, true) {
		return // 上一轮还没跑完，跳过本轮
	}
	defer monitorRunning.Store(false)
	// 配置：仅需监控目录；上传目标固定为全量同步的媒体库（旧配置里的
	// target 字段已废弃忽略——监控的是本地媒体树，目标自然是云端媒体库）
	var cfg struct {
		Dir    string `json:"dir"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(h.getSettingValue("monitor")), &cfg); err != nil {
		return // 配置解析失败视为未启用
	}
	if cfg.Dir == "" {
		return // 未启用
	}
	_ = cfg.Target

	cookie, err := h.get115Cookie()
	if err != nil {
		return
	}

	// 目标库根 cid 与绝对路径
	rootCid := cfg.Target
	var fullCfg struct {
		Cid       string `json:"cid"`
		LocalPath string `json:"local_path"`
	}
	if err := json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg); err != nil {
		return
	}
	if rootCid == "" {
		rootCid = fullCfg.Cid
	}
	if rootCid == "" {
		return
	}
	libAbs := absPathOf(cookie, rootCid, map[string]dirInfo{})
	if libAbs == "" {
		return
	}
	// 库名（云端库根目录名，如「俱乐部」）：本地媒体树第一层是库名，
	// 拼云端绝对路径前要剥掉，否则出现 /俱乐部/俱乐部/... 查不到目录
	libName := ""
	if info, err := get115DirInfo(cookie, rootCid); err == nil {
		libName = info.n
	}

	// 扫描监控目录：Emby 刮削产物（标准图片命名 + NFO），只处理最近 24h 内的新文件
	var imgs []string
	cutoff := time.Now().Add(-24 * time.Hour)
	filepath.WalkDir(cfg.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		ext := strings.ToLower(filepath.Ext(p))
		isImg := (ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp") &&
			isStandardMediaImageName(name)
		isNfo := strings.HasSuffix(name, ".nfo")
		if !isImg && !isNfo {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
			st := fileStamp{modTime: info.ModTime(), size: info.Size()}
			if uploadStampDone(h.DB, p, st) {
				return nil // 已上传且未变化（DB 持久，重启不重复）
			}
			imgs = append(imgs, p)
		}
		return nil
	})
	if len(imgs) == 0 {
		return
	}
	// 无跳过时也要归零（skippedAnnounceSummary 需要非 nil map）
	skippedMeta := 0
	skipDirs := map[string]bool{}
	_ = skipDirs

	for _, img := range imgs {
		rel, err := filepath.Rel(cfg.Dir, img)
		if err != nil {
			continue
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		// 剥掉本地路径的库名第一层（监控目录=本地媒体根时存在），与云端库根对齐
		if libName != "" {
			relDir = strings.TrimPrefix(strings.TrimPrefix(relDir, libName), "/")
		}
		// 定位 115 目标目录：媒体库绝对路径 + 相对目录（files/getid 查询）
		targetAbs := strings.TrimSuffix(libAbs, "/")
		if relDir != "" && relDir != "." {
			targetAbs += "/" + relDir
		}
		cid, ok := cloudPathCid(cookie, targetAbs)
		if !ok {
			// 115 无对应目录（多为播放器自建的 <集名>.mkv/ 本地元数据目录）：
			// 每目录只记一条，同目录后续文件静默跳过——逐文件逐轮刷屏无意义
			metaSkipAnnounce(targetAbs)
			skippedMeta++
			skipDirs[targetAbs] = true
			continue
		}
		metaMissClear(img)
		data, err := os.ReadFile(img)
		if err != nil {
			continue
		}
		if err := h.upload115File(cookie, parseI64(cid), filepath.Base(img), data); err != nil {
			log.Printf("[上传] ✗ 上传失败 %s: %v", rel, err)
			continue
		}
		if st, ok := stampOfPath(img); ok {
			markUploadedStamp(h.DB, img, st)
		}
		log.Printf("[上传] ✓ 上传成功: %s → %s", rel, targetAbs)
	}

	skippedAnnounceSummary(skippedMeta, skipDirs)
	log.Printf("[上传] 发现 %d 个新刮削文件（图片+NFO）", len(imgs))
}

// metadataUploadNames 元数据回传监听的文件名（Emby 写入媒体目录的标准名）
var metadataUploadNames = map[string]bool{
	"poster.jpg": true, "poster.jpeg": true, "poster.png": true,
	"fanart.jpg": true, "fanart.jpeg": true, "banner.jpg": true,
	"tvshow.nfo": true, "movie.nfo": true, "season.nfo": true,
}

// StartMetadataUploader 元数据回传引擎：每 5 分钟扫描本地媒体树，
// 把 Emby 写入的 poster/nfo/fanart 上传到 115 对应目录（按相对路径
// 用 files/getid 定位）。与「监控上传」（Emby 图片目录→115）互补：
// 这里针对的是媒体卷内、随剧集目录存放的元数据文件
func StartMetadataUploader(h *Handler) {
	// 监控上传（monitor 配置）已覆盖同一职责且更可配（目录自选）；
	// 本引擎仅在监控目录未配置时作为兜底启用，避免双路重复上传
	var mc struct {
		Dir string `json:"dir"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("monitor")), &mc)
	if mc.Dir != "" {
		return // 监控上传已启用，兜底引擎休眠
	}
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(5 * time.Minute):
			}
			h.uploadMetadataOnce()
		}
	}()
	log.Println("[元数据回传] 兜底引擎已启动（未配置监控目录；每 5 分钟扫描媒体目录）")
}

// uploadMetadataOnce 单轮回传
var metadataRunning atomic.Bool

func (h *Handler) uploadMetadataOnce() {
	if !metadataRunning.CompareAndSwap(false, true) {
		return
	}
	defer metadataRunning.Store(false)
	local := defaultLocalPath
	var fullCfg struct {
		LocalPath string `json:"local_path"`
		Cid       string `json:"cid"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("full")), &fullCfg) == nil && fullCfg.LocalPath != "" {
		local = fullCfg.LocalPath
	}
	if fullCfg.Cid == "" {
		return // 未配置媒体库，无法定位云端目录
	}
	cookie, err := h.get115Cookie()
	if err != nil {
		return
	}

	// 库根绝对路径与库名（云端目录定位用）。
	// 本地路径第一层是库名（STRM 结构特性），拼接云端绝对路径前要剥掉，
	// 否则出现 /俱乐部/俱乐部/... 查不到目录（全部跳过）
	libAbs := absPathOf(cookie, fullCfg.Cid, map[string]dirInfo{})
	if libAbs == "" {
		return
	}
	libName := ""
	if info, err := get115DirInfo(cookie, fullCfg.Cid); err == nil {
		libName = info.n
	}

	// 扫描媒体树里的元数据文件（跳过已上传标记；用台账判定是否已在云端）
	var files []string
	filepath.WalkDir(local, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !metadataUploadNames[strings.ToLower(d.Name())] {
			// 每集同名 nfo（xxx.mkv.nfo）也要回传
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".mkv.nfo") &&
				!strings.HasSuffix(strings.ToLower(d.Name()), ".mp4.nfo") {
				return nil
			}
		}
		if st, ok := stampOf(d); ok {
			// 已上传且内容未变（指纹一致）才跳过；Emby 重新刮削覆盖后会重传
			if uploadStampDone(h.DB, p, st) {
				return nil
			}
		}
		files = append(files, p)
		return nil
	})
	if len(files) == 0 {
		return
	}
	log.Printf("[元数据回传] 发现 %d 个待上传文件", len(files))

	ok, fail := 0, 0
	for _, f := range files {
		rel, err := filepath.Rel(local, f)
		if err != nil {
			continue
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		// 剥掉本地路径的库名第一层，与云端库根对齐
		if libName != "" {
			relDir = strings.TrimPrefix(strings.TrimPrefix(relDir, libName), "/")
		}
		targetAbs := strings.TrimSuffix(libAbs, "/")
		if relDir != "" && relDir != "." {
			targetAbs += "/" + relDir
		}
		cid, found := cloudPathCid(cookie, targetAbs)
		if !found {
			// 同一文件连续多轮查不到云端目录就停止重试（防每 5 分钟刷屏）
			if n := metaMissIncr(f); n <= 2 {
				log.Printf("[元数据回传] ○ 云端目录不存在，跳过 %s（%s）", rel, targetAbs)
			} else if n == 3 {
				log.Printf("[元数据回传] ○ %s 连续 3 轮未找到云端目录，本会话内不再重试（如目录确实存在请反馈日志）", rel)
			}
			continue
		}
		metaMissClear(f)
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if err := h.upload115File(cookie, parseI64(cid), filepath.Base(f), data); err != nil {
			fail++
			log.Printf("[元数据回传] ✗ 失败 %s: %v", rel, err)
			continue
		}
		if st, ok := stampOfPath(f); ok {
			markUploadedStamp(h.DB, f, st)
		}
		ok++
		log.Printf("[元数据回传] ✓ %s → %s", rel, targetAbs)
	}
	if ok+fail > 0 {
		log.Printf("[元数据回传] 本轮完成: 成功 %d，失败 %d", ok, fail)
	}
}

// metaMissCount 云端目录连续未命中计数（会话级；达到上限后不再重试防刷屏）。
// 监控引擎（1 分钟轮）与元数据回传引擎（5 分钟轮）两个 goroutine 并发访问，
// 必须持锁——此前裸 map 并发写是进程级 panic（直接宕服务）
var (
	metaMissCount = map[string]int{}
	metaMissMu    sync.Mutex
)

// skippedAnnounceSummary 一轮结束的跳过汇总（防逐文件逐轮刷屏）
func skippedAnnounceSummary(skipped int, dirs map[string]bool) {
	if skipped <= 0 {
		return
	}
	example := ""
	for d := range dirs {
		example = d
		break
	}
	log.Printf("[上传] ○ %d 个元数据文件因 115 无对应目录跳过回传（涉及 %d 个本地目录；多为播放器自建的每集元数据目录，不影响 115 数据。示例：%s）",
		skipped, len(dirs), truncateStr(example, 120))
}

// metaSkipAnnounced 已宣告过"115 无对应目录"的本地目录（每目录只记一条）
var (
	metaSkipMu        sync.Mutex
	metaSkipAnnounced = map[string]bool{}
)

func metaSkipAnnounce(dir string) {
	metaSkipMu.Lock()
	defer metaSkipMu.Unlock()
	if metaSkipAnnounced[dir] {
		return
	}
	metaSkipAnnounced[dir] = true
	log.Printf("[上传] ○ 目录在 115 上不存在，跳过该目录的元数据回传：%s", truncateStr(dir, 120))
}

// metaMissIncr 计数 +1 并返回新值（持锁）
func metaMissIncr(key string) int {
	metaMissMu.Lock()
	defer metaMissMu.Unlock()
	metaMissCount[key]++
	return metaMissCount[key]
}

// metaMissClear 命中后清零（持锁）
func metaMissClear(key string) {
	metaMissMu.Lock()
	delete(metaMissCount, key)
	metaMissMu.Unlock()
}

// isStandardMediaImageName Emby/Jellyfin 标准媒体图片命名
// （poster/fanart/banner/logo/clearart/disc…及 seasonXX-poster 等变体）
// reSeasonImg 季海报命名（监控上传全目录树高频调用，预编译）
var reSeasonImg = regexp.MustCompile(`^season(\d{1,2}|specials)-.+`)

func isStandardMediaImageName(lowerName string) bool {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(
		strings.TrimSuffix(strings.TrimSuffix(lowerName, ".jpg"), ".jpeg"),
		".png"), ".webp"), "")
	for _, std := range []string{"poster", "fanart", "backdrop", "banner", "thumb",
		"landscape", "logo", "clearlogo", "clearart", "disc", "discart", "folder"} {
		if base == std || strings.HasPrefix(base, std+"-") || strings.HasSuffix(base, "-"+std) {
			return true
		}
	}
	// seasonXX-poster / season-specials-poster 变体
	if reSeasonImg.MatchString(base) {
		return true
	}
	// 剧集名-poster 等含分隔符的形态已由前后缀匹配覆盖
	return false
}

// cloudPathCid 按绝对路径查询 115 目录 cid（webapi files/getid）。
// path 参数不带前导斜杠（openStrm 生产验证的调用形态）；
// 响应顶层是 id 字段（非 data 数组），同时兼容 data 数组/对象等历史形态。
// 查询失败/解析异常时打印原始响应，便于排查。
func cloudPathCid(cookie, absPath string) (string, bool) {
	cid, ok, _ := cloudPathCidE(cookie, absPath)
	return cid, ok
}

// cloudPathCidE 同 cloudPathCid，额外返回请求/解析错误（区分「目录不存在」与「查询失败」）
func cloudPathCidE(cookie, absPath string) (cid string, found bool, reqErr error) {
	pathParam := strings.TrimLeft(absPath, "/")
	body, err := httpGet115UA("https://webapi.115.com/files/getid",
		url.Values{"path": {pathParam}}, cookie, ua115Unified(), 15*time.Second)
	if err != nil {
		log.Printf("[元数据回传] files/getid 请求失败（%s）: %v", pathParam, err)
		return "", false, err
	}
	return getidCidFromResponse(body, pathParam)
}

// getidCidFromResponse 解析 files/getid 响应（纯函数，便于测试）。
// found=false 表示目录不存在（正常业务结果）；err 非 nil 表示响应异常。
func getidCidFromResponse(body []byte, pathParam string) (cid string, found bool, err error) {
	var r struct {
		State bool            `json:"state"`
		ID    *json.Number    `json:"id"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		log.Printf("[元数据回传] files/getid 响应解析失败（%s）: %s", pathParam, truncateStr(string(body), 160))
		return "", false, err
	}
	// 主形态：顶层 id（openStrm 生产验证）。
	// id=0 = 路径在 115 上不存在（正常业务结果，静默由调用方记跳过）
	if r.ID != nil {
		if str := r.ID.String(); str != "" && str != "0" {
			return str, true, nil
		}
		return "", false, nil
	}
	// 兼容形态：data 数组 [{"cid":...}]
	if len(r.Data) > 0 && r.Data[0] == '[' {
		var arr []struct {
			Cid string `json:"cid"`
		}
		if json.Unmarshal(r.Data, &arr) == nil && len(arr) > 0 && arr[0].Cid != "" {
			return arr[0].Cid, true, nil
		}
	}
	// 兼容形态：data 对象 {"id":...} / {"cid":...}
	if len(r.Data) > 0 && r.Data[0] == '{' {
		var obj struct {
			ID  json.Number `json:"id"`
			Cid string      `json:"cid"`
		}
		if json.Unmarshal(r.Data, &obj) == nil {
			if s := obj.ID.String(); s != "" && s != "0" {
				return s, true, nil
			}
			if obj.Cid != "" && obj.Cid != "0" {
				return obj.Cid, true, nil
			}
		}
	}
	// state=true 却没取到 id：响应形态又变了，打印原始内容供排查
	if r.State {
		log.Printf("[元数据回传] files/getid 响应缺少 id 字段（%s）: %s", pathParam, truncateStr(string(body), 160))
	}
	return "", false, nil
}

// cookieUserID 从 Cookie 的 UID 字段提取用户 id（UID=xxx_格式）
func cookieUserID(cookie string) int64 {
	for _, part := range strings.Split(cookie, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == "UID" {
			idStr := strings.SplitN(kv[1], "_", 2)[0]
			var id int64
			fmt.Sscanf(idStr, "%d", &id)
			return id
		}
	}
	return 0
}

func parseI64(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}
