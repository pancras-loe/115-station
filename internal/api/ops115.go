package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"
)

// ==================== 115 文件操作（创建目录 / 移动文件） ====================

// httpPostForm115 带 Cookie 的 POST 表单请求
func httpPostForm115(api string, form url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	throttle115(api) // 全局节流，防止触发 115 风控
	req, err := http.NewRequest(http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua115Unified())
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	throttle115Done(api) // 节流锚点推进到本请求完成时刻
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("115 接口返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// mkdir115 在指定父目录下创建文件夹，返回新文件夹 cid
func mkdir115(cookie, parentCid, folderName string) (string, error) {
	// 字段名以 p115client fs_mkdir 为准：pid + cname（旧字段 n 已失效，
	// 发 n 会被 115 判定为"目录名称不能为空"）
	form := url.Values{
		"aid":   {"1"},
		"pid":   {parentCid},
		"cname": {folderName},
	}
	body, err := httpPostForm115("https://webapi.115.com/files/add", form, cookie, 15*time.Second)
	if err != nil {
		return "", err
	}
	// 实测成功响应为平铺结构：{"state":true,"cid":"...","file_id":"...","file_name":"..."}
	// 且 errno 可能是空字符串（不能声明为 int），失败时才有 data/errMsg
	var result struct {
		State   bool            `json:"state"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
		ErrNo   json.RawMessage `json:"errNo"`
		ErrMsg  string          `json:"errMsg"`
		Cid     string          `json:"cid"`
		FileID  string          `json:"file_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析创建目录响应失败: %s", truncateStr(string(body), 150))
	}
	if !result.State {
		if result.ErrMsg != "" {
			return "", fmt.Errorf("创建目录失败: %s", result.ErrMsg)
		}
		return "", fmt.Errorf("创建目录失败: %s", result.Error)
	}
	// 平铺字段优先
	if result.Cid != "" || result.FileID != "" {
		if result.Cid != "" {
			return result.Cid, nil
		}
		return result.FileID, nil
	}
	// 兼容嵌套 data 结构
	var d struct {
		Cid    string `json:"cid"`
		FID    string `json:"fid"`
		FileID string `json:"file_id"`
	}
	if json.Unmarshal(result.Data, &d) == nil {
		if d.Cid != "" {
			return d.Cid, nil
		}
		if d.FID != "" {
			return d.FID, nil
		}
		if d.FileID != "" {
			return d.FileID, nil
		}
	}
	// data 无 cid（如字符串提示）：目录实际已创建，列出父目录按名找回 cid
	if cid, err := findSubDir115(cookie, parentCid, folderName); err == nil && cid != "" {
		return cid, nil
	}
	return "", fmt.Errorf("目录已创建但未获取到 cid（data=%s）", truncateStr(string(result.Data), 100))
}

// findSubDir115 在指定目录下查找名为 name 的子目录，返回 cid（不存在返回空）。
// 翻页查找：单层条目超一页（1150）时只查第一页会漏掉目标目录，
// ensurePath 随之误走 mkdir（重名被 115 拒绝则整理失败）
func findSubDir115(cookie, parentCid, name string) (string, error) {
	offset := 0
	for {
		entries, count, err := list115Entries(cookie, parentCid, offset)
		if err != nil {
			return "", err
		}
		for _, d := range entries {
			if fmt.Sprint(d["f"]) == "0" && fmt.Sprint(d["n"]) == name {
				return fmt.Sprint(d["cid"]), nil
			}
		}
		offset += len(entries)
		if len(entries) == 0 || offset >= count {
			return "", nil
		}
	}
}

// ensurePathCache parent+路径→最终 cid 缓存：整理每部影视都要 ensure
// 目标目录与季目录（逐级 find+mkdir，每层至少一次 API），重复入库同分类
// 时全是重复劳动；TTL 10 分钟，mkdir 失败时清除该键重试
var (
	ensurePathMu    sync.Mutex
	ensurePathCache = map[string]ensurePathEntry{}
)

type ensurePathEntry struct {
	cid string
	at  time.Time
}

// ensure115Path 在 parentCid 下逐级创建目录路径（如 "电影/华语电影/A"），返回最终目录 cid
func ensure115Path(cookie, parentCid, dirPath string) (string, error) {
	cacheKey := parentCid + "|" + dirPath
	ensurePathMu.Lock()
	if e, ok := ensurePathCache[cacheKey]; ok && time.Since(e.at) < 10*time.Minute {
		cid := e.cid
		ensurePathMu.Unlock()
		return cid, nil
	}
	ensurePathMu.Unlock()

	cid, err := ensure115PathUncached(cookie, parentCid, dirPath)
	if err == nil && cid != "" {
		ensurePathMu.Lock()
		ensurePathCache[cacheKey] = ensurePathEntry{cid: cid, at: time.Now()}
		ensurePathMu.Unlock()
	}
	return cid, err
}

func ensure115PathUncached(cookie, parentCid, dirPath string) (string, error) {
	parts := strings.Split(dirPath, "/")
	currentCid := parentCid
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 先查找是否已存在
		existingCid, err := findSubDir115(cookie, currentCid, part)
		if err != nil {
			return "", err
		}
		if existingCid != "" {
			currentCid = existingCid
			continue
		}
		// 不存在则创建
		newCid, err := mkdir115(cookie, currentCid, part)
		if err != nil {
			return "", err
		}
		currentCid = newCid
	}
	return currentCid, nil
}

// move115Files 将文件/文件夹移动到目标目录
func move115Files(cookie, targetCid string, fileIds []string) error {
	if len(fileIds) == 0 {
		return nil
	}
	// 字段格式以 p115client fs_move 为准：fid[0]、fid[1]...（重复的裸 fid 已失效）
	form := url.Values{
		"pid": {targetCid},
	}
	for i, fid := range fileIds {
		form.Set(fmt.Sprintf("fid[%d]", i), fid)
	}
	body, err := httpPostForm115("https://webapi.115.com/files/move", form, cookie, 20*time.Second)
	if err != nil {
		return err
	}
	var result struct {
		State bool   `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析移动文件响应失败")
	}
	if !result.State {
		msg := result.Error
		if msg == "" {
			msg = "未知错误"
		}
		return fmt.Errorf("移动文件失败: %s", msg)
	}
	return nil
}
func (h *Handler) get115Cookie() (string, error) {
	// 优先从文件读取
	cookie, err := h.Config.LoadCookie()
	if err == nil && cookie != "" {
		return cookie, nil
	}
	// 回退到数据库（兼容旧数据）
	var storage model.Storage
	if err := h.DB.Where("type = ?", "115").First(&storage).Error; err != nil || storage.Cookie == "" {
		return "", fmt.Errorf("尚未绑定 115 账号，请先在「115账号」页扫码登录")
	}
	return storage.Cookie, nil
}

// get115UA 返回与登录设备匹配的 User-Agent
func (h *Handler) get115UA() string {
	device := h.Config.Load115Device()
	if device == "" {
		// 回退到数据库
		var storage model.Storage
		if err := h.DB.Where("type = ?", "115").First(&storage).Error; err == nil && storage.Device != "" {
			device = storage.Device
		}
	}
	return deviceToUA(device)
}

// buildExtSet 把后缀列表（如 ["mp4","mkv"]）转成 map[".mp4"]=true
func buildExtSet(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, e := range exts {
		e = strings.TrimSpace(strings.ToLower(e))
		e = strings.TrimPrefix(e, ".")
		if e != "" {
			set["."+e] = true
		}
	}
	return set
}

