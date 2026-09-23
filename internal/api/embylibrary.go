package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// embyPathRoots 返回统一的本地媒体库根与 Emby 挂载根。
// path_mapping 的前半段是历史兼容字段；本地根始终以 full.local_path 为准，
// 这样在统一入口修改本地目录后，不需要再去 Emby 卡片重复修改一次。
func embyPathRoots(pathMapping string) (string, string) {
	localRoot := strings.TrimRight(strings.ReplaceAll(localMediaRoot(), "\\", "/"), "/")
	parts := strings.SplitN(pathMapping, "#", 2)
	if len(parts) != 2 {
		return localRoot, ""
	}
	return localRoot, strings.TrimRight(strings.ReplaceAll(parts[1], "\\", "/"), "/")
}

// embyPathToLocal Emby 路径 → 本地路径（mapToEmbyPath 的逆向）。
//
// 抽成纯函数是因为有两个调用方：302 反代读 strm 内容（readStrmDirectURL）
// 与 webhook 删除联动（deepdelemby.go）。两边各写一份的话，
// 修对一处另一处照旧出错 —— STRM 配置解析就是这么踩过一次的。
// 映射没配或前缀对不上时原样返回：调用方一律按「就是本地路径」处理。
func embyPathToLocal(pathMapping, embyPath string) string {
	embyPath = strings.ReplaceAll(embyPath, "\\", "/")
	if pathMapping == "" || embyPath == "" {
		return embyPath
	}
	localRoot, embyRoot := embyPathRoots(pathMapping)
	if localRoot == "" || embyRoot == "" {
		return embyPath
	}
	if embyPath == embyRoot {
		return localRoot
	}
	if strings.HasPrefix(embyPath, embyRoot+"/") {
		return localRoot + embyPath[len(embyRoot):]
	}
	return embyPath
}

// mapFromEmbyPath Emby 路径 → 本地路径（读当前 emby 配置）
func (h *Handler) mapFromEmbyPath(embyPath string) string {
	var embyCfg struct {
		PathMapping string `json:"path_mapping"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("emby")), &embyCfg)
	return embyPathToLocal(embyCfg.PathMapping, embyPath)
}

// embyServerInfo 取 Emby 地址与 API Key（复用 EMBY 管理卡配置）
func (h *Handler) embyServerInfo() (base, apiKey string, ok bool) {
	var cfg struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if json.Unmarshal([]byte(h.getSettingValue("emby")), &cfg) != nil || cfg.ServerURL == "" {
		return "", "", false
	}
	base = strings.TrimRight(cfg.ServerURL, "/")
	apiKey = cfg.APIKey
	return base, apiKey, true
}

// embyRequest 带 api_key 的请求构造
func embyRequest(method, base, apiKey, path string, query url.Values, body []byte) (*http.Response, error) {
	if apiKey != "" {
		if query == nil {
			query = url.Values{}
		}
		query.Set("api_key", apiKey)
	}
	u := base + path
	if qs := query.Encode(); qs != "" {
		u += "?" + qs
	}
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return (&http.Client{Timeout: 20 * time.Second}).Do(req)
}

// embyVirtualFolderIds 根据库名查找 ItemId，供媒体库封面推送使用。
func embyVirtualFolderIds(base, apiKey string) map[string]string {
	resp, err := embyRequest(http.MethodGet, base, apiKey, "/Library/VirtualFolders", nil, nil)
	if err != nil {
		log.Printf("[Emby] ✗ 取媒体库列表失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[Emby] ✗ 取媒体库列表 HTTP %d（API 密钥填对了吗）", resp.StatusCode)
		return nil
	}
	var libs []struct {
		Name   string `json:"Name"`
		ItemID string `json:"ItemId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		log.Printf("[Emby] ✗ 解析媒体库列表失败: %v", err)
		return nil
	}
	m := map[string]string{}
	for _, it := range libs {
		if it.Name != "" && it.ItemID != "" {
			m[it.Name] = it.ItemID
		}
	}
	return m
}
