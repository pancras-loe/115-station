package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ==================== 115 同步引擎（webapi） ====================
//
// 数据通道（走 webapi + Cookie，不依赖已暂停服务的开放平台）：
//   - 全量：webapi.115.com/files 递归遍历目录
//   - 增量：life_behavior_detail_app 拉取生活事件
//
// 文件条目字段（webapi files 接口返回）：
//   f = "0" 文件夹 / "1" 文件；n 文件名；fid 文件id；cid 目录id；s 文件大小

const (
	webapi115   = "https://webapi.115.com"
	fileListAPI = "https://webapi.115.com/files/"
	// 生活事件（App 接口，Cookie 认证）：type 省略则逆序拉全部类型；limit 最大 1000
	behaviorAPI = "https://proapi.115.com/android/behavior/detail"
	pageSize    = 1150
)

// webapiFileOrigins 文件列表接口的镜像域名轮换池（p115client get_webapi_origin 同款）
// 单域名被风控（开小差）时切换下一个镜像即可继续使用
var webapiFileOrigins = []string{
	"https://webapi.115.com",
	"http://web.api.115.com",
	"https://115cdn.com/webapi",
	"https://115vod.com/webapi",
}

// remoteFile 115 网盘上的一个文件
type remoteFile struct {
	Fid      string `json:"fid"`
	Name     string `json:"name"`
	Path     string `json:"path"`     // 相对媒体库根目录的路径
	Size     int64  `json:"size"`
	PickCode string `json:"pickcode"` // 下载直链用
	Sha1     string `json:"sha1"`     // 文件 sha1（整理去重用）
	IsAsset  bool   `json:"is_asset"` // 附属文件（图片/字幕/nfo 等，需实体落盘）
}

// syncFilter 全量同步的文件分类过滤器
type syncFilter struct {
	videoExts map[string]bool // 视频：生成 .strm
	assetExts map[string]bool // 附属（图片/字幕/元数据）：下载为真实文件
}

// httpGet115 带 Cookie 的 GET 请求（使用默认 UA）
func httpGet115(api string, query url.Values, cookie string, timeout time.Duration) ([]byte, error) {
	return httpGet115UA(api, query, cookie, "", timeout)
}

// httpGet115UA 带 Cookie 和自定义 UA 的 GET 请求
// UA 必须和扫码登录时的设备类型匹配，否则 115 返回"服务器开小差了"
func httpGet115UA(api string, query url.Values, cookie, ua string, timeout time.Duration) ([]byte, error) {
	return httpGet115Full(api, query, cookie, ua, timeout, nil)
}

// httpGet115Full 完整版请求：可自定义 UA 和额外请求头
func httpGet115Full(api string, query url.Values, cookie, ua string, timeout time.Duration, extraHeaders map[string]string) ([]byte, error) {
	throttle115(api) // 全局节流，防止触发 115 风控
	if ua == "" {
		ua = ua115Unified()
	}
	full := api
	if len(query) > 0 {
		full = api + "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}
	// UA + Cookie + Referer（浏览器同源请求特征；openStrm 同款）
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://115.com/")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	// 115 专属客户端：部分节点证书缺 SAN，容忍主机名不匹配（证书链仍校验）
	resp, err := client115(timeout).Do(req)
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

// fetch115FilesPage 从 webapi 拉取一页文件列表（文件+文件夹）
// 主域名被风控返回"开小差"时自动切换镜像域名（p115client get_webapi_origin 轮换同款）
// 返回条目列表、总数、命中的域名
func fetch115FilesPage(cookie, ua, cid string, offset int) ([]map[string]interface{}, int, string, error) {
	query := build115FileQuery(cid, offset)
	var lastErr error
	for _, origin := range webapiFileOrigins {
		body, err := httpGet115UA(origin+"/files", query, cookie, ua, 20*time.Second)
		if err != nil {
			lastErr = err
			log.Printf("[诊断] %s 请求失败: %v", origin, err)
			continue
		}
		var result struct {
			State bool                      `json:"state"`
			Error string                    `json:"error"`
			Data  []map[string]interface{} `json:"data"`
			Count int                       `json:"count"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("解析 115 目录失败: %v", err)
			log.Printf("[诊断] %s 响应无法解析: %s", origin, truncateStr(string(body), 200))
			continue
		}
		if !result.State {
			lastErr = fmt.Errorf("115 返回错误: %s", result.Error)
			log.Printf("[诊断] %s 拒绝: %s", origin, result.Error)
			// Cookie 失效类错误换镜像也没用，直接返回
			if strings.Contains(result.Error, "登录") || strings.Contains(result.Error, "acc") {
				return nil, 0, "", lastErr
			}
			continue // 风控类错误，尝试下一个镜像
		}
		// 成功日志仅在走了镜像回退时记录（主域名成功是常态，不刷屏）
		if origin != webapiFileOrigins[0] {
			log.Printf("[诊断] %s 镜像接管: count=%d, 条目数=%d", origin, result.Count, len(result.Data))
		}
		// state=true 但 count>0 而 data 为空：响应异常，换镜像
		if result.Count > 0 && len(result.Data) == 0 {
			lastErr = fmt.Errorf("响应异常：count=%d 但未返回数据", result.Count)
			continue
		}
		for _, e := range result.Data {
			normalize115Entry(e)
		}
		return result.Data, result.Count, origin, nil
	}
	return nil, 0, "", lastErr
}

// normalize115Entry 归一化 webapi / open 两种条目方言，统一为 webapi 形态：
//   - webapi: f=="0" 目录（cid=目录自身 id）、n=名称、fid=文件 id
//   - open:   fc/file_category==0 目录（fid=自身 id）、fn/file_name=名称
func normalize115Entry(m map[string]interface{}) {
	if _, ok := m["f"]; !ok {
		dir := false
		if v, ok := m["fc"]; ok && fmt.Sprint(v) == "0" {
			dir = true
		}
		if v, ok := m["file_category"]; ok && fmt.Sprint(v) == "0" {
			dir = true
		}
		if dir {
			m["f"] = "0"
		} else {
			m["f"] = "1"
		}
	}
	if fmt.Sprint(m["f"]) == "0" && (fmt.Sprint(m["cid"]) == "" || fmt.Sprint(m["cid"]) == "<nil>") {
		m["cid"] = m["fid"]
	}
	if fmt.Sprint(m["n"]) == "" || fmt.Sprint(m["n"]) == "<nil>" {
		m["n"] = m["fn"]
	}
}

// list115Entries 拉取指定目录的一页条目（文件 + 文件夹），返回条目列表和总数
func list115Entries(cookie, cid string, offset int) ([]map[string]interface{}, int, error) {
	entries, count, _, err := fetch115FilesPage(cookie, ua115Unified(), cid, offset)
	return entries, count, err
}

// walk115Dir 递归遍历目录，按过滤器分别收集视频（生成 strm）和附属文件（实体落盘）
// assets 为 nil 时只收集视频（整理管线等场景）；skipCids 非空时跳过这些 cid 的子树
// （整理工作区：待整理/已存在/冗余/转存目录，同步库根时不应生成 STRM）
func walk115Dir(ops *pan115Ops, cid, basePath string, videos, assets *[]remoteFile, f *syncFilter, skipCids map[string]bool) error {
	dirLabel := basePath
	if dirLabel == "" {
		dirLabel = "(根目录)"
	}
	offset := 0
	for {
		entries, count, err := ops.listEntries(cid, offset)
		if err != nil {
			return err
		}
		// 等待时长并入目录行；简洁模式静默逐目录遍历
		if w := throttle115LastWait(); w > 0 {
			vlog("[同步] 同步%s（等 %.1fs）", dirLabel, w.Seconds())
		} else {
			vlog("[同步] 同步%s", dirLabel)
		}
		for _, d := range entries {
			isDir := fmt.Sprint(d["f"]) == "0"
			name := fmt.Sprint(d["n"])
			if isDir {
				subCid := fmt.Sprint(d["cid"])
				if skipCids != nil && skipCids[subCid] {
					log.Printf("[同步] ○ 跳过整理工作区目录: %s", path.Join(basePath, name))
					continue
				}
				subPath := path.Join(basePath, name)
				if err := walk115Dir(ops, subCid, subPath, videos, assets, f, skipCids); err != nil {
					return err
				}
			} else {
				ext := strings.ToLower(path.Ext(name))
				size := int64(0)
				if s, ok := d["s"].(float64); ok {
					size = int64(s)
				}
				sha1 := fmt.Sprint(d["sha"])
				if sha1 == "<nil>" {
					sha1 = ""
				}
				rf := remoteFile{
					Fid:      fmt.Sprint(d["fid"]),
					Name:     name,
					Path:     basePath,
					Size:     size,
					PickCode: fmt.Sprint(d["pc"]),
					Sha1:     sha1,
				}
				switch {
				case len(f.videoExts) > 0 && f.videoExts[ext]:
					if videos != nil {
						*videos = append(*videos, rf)
					}
				case len(f.assetExts) > 0 && f.assetExts[ext]:
					rf.IsAsset = true
					if assets != nil {
						*assets = append(*assets, rf)
					}
				}
			}
		}
		if len(entries) == 0 || offset+len(entries) >= count {
			break
		}
		offset += len(entries)
	}
	return nil
}

// downloadAssetBytes 下载附属文件内容，按 UA/Cookie 组合重试
// UA 必须与直链签发时一致（headers["User-Agent"]），直链对该 UA 绑定
func downloadAssetBytes(rawURL string, cdnHeaders map[string]string, loginCookie string) ([]byte, error) {
	dlUA := cdnHeaders["User-Agent"]
	if dlUA == "" {
		dlUA = ua115Unified()
	}
	setCookie := cdnHeaders["Cookie"]
	combined := setCookie
	if combined != "" && loginCookie != "" {
		combined += "; "
	}
	combined += loginCookie
	type attempt struct{ ua, cookie string }
	var attempts []attempt
	if setCookie != "" {
		attempts = append(attempts, attempt{dlUA, setCookie}) // 同 UA + 直链下发 Cookie（f=3）
	}
	attempts = append(attempts,
		attempt{dlUA, ""},          // openStrm 实战组合：同 UA，不带 Cookie
		attempt{dlUA, combined},    // 直链 Cookie + 登录 Cookie
		attempt{dlUA, loginCookie},
	)
	var lastErr error
	for _, a := range attempts {
		data, err := httpDownloadUA(rawURL, a.ua, a.cookie)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "403") {
			return nil, err // 非 403（如 404/超时）重试无意义
		}
	}
	return nil, fmt.Errorf("CDN 下载被拒(403)：已尝试 UA/Cookie 全部组合，%v", lastErr)
}

// httpDownloadUA 下载文件内容到内存（附属文件均为小文件），UA 可为空串（发空 UA 头）
func httpDownloadUA(rawURL, ua, cookieHeader string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

