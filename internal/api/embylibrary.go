package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"115-station/internal/model"
)

// embyPathRoots 返回映射两侧的根：本地这一侧、Emby 那一侧。
//
// path_mapping 写作「本地#Emby」，两侧都由用户填（p115strmhelper / qmediasync 同款）。本地这一侧：
//   - 空：跟随本地媒体库目录（full.local_path），两个容器挂同一个目录时就是它，改了本地目录不用回来再改；
//   - 绝对路径：照用。Emby 直接从 115 库目录那一层挂进去时要填它（STRM 第一层是库名，本站 /vol1/1000 → /Movies、
//     Emby /vol1/1000/资源库 → /Movies，Emby 的 /Movies 对应本地 /Movies/资源库，2026-10-10 现场）；
//   - 相对路径：本地媒体库目录下的子目录（v26.10.10-6 那一版的存法，兼容）。
//
// 老版本保存时会把当时的本地根写进前半段、后端却一直忽略它，用户改过本地目录的话那个值早就过时了：
// 启动时由 MigrateEmbyLocalSide 一次性清掉，别去掉那道迁移再让前半段生效。
func embyPathRoots(pathMapping string) (string, string) {
	localRoot := strings.TrimRight(strings.ReplaceAll(localMediaRoot(), "\\", "/"), "/")
	parts := strings.SplitN(pathMapping, "#", 2)
	if len(parts) != 2 {
		return localRoot, ""
	}
	if abs := embyLocalAbs(parts[0]); abs != "" {
		localRoot = abs
	} else if sub := embyLocalSub(parts[0]); sub != "" && localRoot != "" {
		localRoot += "/" + sub
	}
	return localRoot, strings.TrimRight(strings.ReplaceAll(parts[1], "\\", "/"), "/")
}

// isAbsLocalPath / 开头或带盘符（D:/、D:\）
func isAbsLocalPath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") || (len(s) >= 2 && s[1] == ':')
}

// embyLocalAbs path_mapping 前半段是绝对路径时返回它（/ 分隔、去掉结尾 /），否则空串。
// 只剩「/」的不认：映射成整个文件系统根没有意义，多半是填错
func embyLocalAbs(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	if !isAbsLocalPath(s) {
		return ""
	}
	return strings.TrimRight(s, "/")
}

// embyLocalSub path_mapping 前半段 → 本地媒体库根下的子目录（/ 分隔、首尾无 /）。
// 绝对路径、空、带 .. 的一律返回空串
func embyLocalSub(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	if s == "" || isAbsLocalPath(s) {
		return ""
	}
	s = path.Clean(s)
	if s == "." || s == ".." || strings.HasPrefix(s, "../") {
		return ""
	}
	return s
}

// embyLocalSideMigrateKey 完成标记（Setting 表）
const embyLocalSideMigrateKey = "migrate.emby_local_side.v1"

// MigrateEmbyLocalSide 一次性清掉老配置 path_mapping 前半段的绝对路径（见 embyPathRoots）。
// 老界面从不让用户填这一侧，清掉 = 跟随本地媒体库目录，和迁移前的实际行为完全一致。
// 相对路径（-6 版的子目录）是用户填的，不动
func (h *Handler) MigrateEmbyLocalSide() {
	if h.DB == nil {
		return
	}
	var done int64
	h.DB.Model(&model.Setting{}).Where("key = ?", embyLocalSideMigrateKey).Count(&done)
	if done > 0 {
		return
	}
	if raw := h.getSettingValue("emby"); raw != "" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			log.Printf("[迁移] ✗ Emby 路径映射：配置解析失败，下次启动再试: %v", err)
			return
		}
		pm, _ := cfg["path_mapping"].(string)
		if l, e, ok := strings.Cut(pm, "#"); ok && isAbsLocalPath(strings.TrimSpace(l)) {
			cfg["path_mapping"] = "#" + e
			b, _ := json.Marshal(cfg)
			// 和 SaveSetting 接口同一个去处（yaml，读取时它优先于数据库里的旧值）
			if h.Config == nil {
				return
			}
			if err := h.Config.SaveSetting("emby", string(b)); err != nil {
				log.Printf("[迁移] ✗ Emby 路径映射：保存失败，下次启动再试: %v", err)
				return
			}
			log.Printf("[迁移] ✓ Emby 路径映射本地一侧「%s」改为跟随本地媒体库目录（老版本自动写入、一直未生效）", l)
		}
	}
	if err := h.DB.Create(&model.Setting{Key: embyLocalSideMigrateKey, Value: "1"}).Error; err != nil {
		log.Printf("[迁移] ○ Emby 路径映射迁移完成标记写入失败（下次启动再跑一遍，结果不变）: %v", err)
	}
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
