package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 目录浏览 ====================

// dirCacheEntry 目录列表缓存（避免选择器反复点开同一路径时重复请求 115）
type dirCacheEntry struct {
	dirs    []gin.H
	count   int
	origin  string
	expires time.Time
}

var (
	dirCacheMu sync.Mutex
	dirCache   = map[string]dirCacheEntry{}
)

const dirCacheTTL = 5 * time.Minute

// List115Dirs 浏览 115 网盘目录（只返回文件夹）
// GET /storage/115/dirs?cid=0
func (h *Handler) List115Dirs(c *gin.Context) {
	cid := c.Query("cid")
	if cid == "" {
		cid = "0"
	}
	// account_id：浏览播放小号的目录（镜像目录选择用），走小号 Cookie，
	// 缓存按账号分键（与主号目录树隔离）
	altID := c.Query("account_id")

	// 命中缓存直接返回（5 分钟）
	cacheKey := cid
	if altID != "" {
		cacheKey = "alt:" + altID + ":" + cid
	}
	dirCacheMu.Lock()
	if e, ok := dirCache[cacheKey]; ok && time.Now().Before(e.expires) {
		dirs, count, origin := e.dirs, e.count, e.origin
		dirCacheMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"data": dirs, "cid": cid, "count": count, "origin": origin, "channel": "cache"})
		return
	}
	dirCacheMu.Unlock()

	// 统一操作通道：OpenAPI 优先，Cookie 回退；小号浏览走小号 Cookie
	var ops *pan115Ops
	if altID != "" {
		pcfg := loadPlaybackCfg()
		for i := range pcfg.Alts {
			if fmt.Sprint(pcfg.Alts[i].ID) == altID {
				ops = &pan115Ops{cookie: pcfg.Alts[i].Cookie}
				break
			}
		}
		if ops == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "小号不存在或已删除"})
			return
		}
	} else {
		var err error
		ops, err = h.newPan115Ops()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	dirs, count, origin, err := ops.listDirs(cid)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// 写入缓存（超过 500 项时清空防止膨胀）
	dirCacheMu.Lock()
	if len(dirCache) > 500 {
		dirCache = map[string]dirCacheEntry{}
	}
	dirCache[cacheKey] = dirCacheEntry{dirs: dirs, count: count, origin: origin, expires: time.Now().Add(dirCacheTTL)}
	dirCacheMu.Unlock()

	c.JSON(http.StatusOK, gin.H{"data": dirs, "cid": cid, "channel": ops.channelName(), "count": count, "origin": origin})
}

// fetch115Dirs 调用 115 webapi 获取目录下的文件夹列表（多镜像自动回退）
// 返回文件夹列表、目录总条目数、命中的域名
func fetch115Dirs(cookie, ua, cid string) ([]gin.H, int, string, error) {
	if cookie == "" {
		return nil, 0, "", fmt.Errorf("Cookie 为空，请先扫码登录")
	}

	// 翻页取全：目录选择器此前只取第一页（1150 条），大目录后面的文件夹看不到
	dirs := make([]gin.H, 0, 64)
	count, origin := 0, ""
	offset := 0
	for {
		entries, c, o, err := fetch115FilesPage(cookie, ua, cid, offset)
		if err != nil {
			return nil, 0, "", err
		}
		count, origin = c, o
		for _, d := range entries {
			if fmt.Sprint(d["f"]) == "0" {
				dirs = append(dirs, gin.H{"cid": fmt.Sprint(d["cid"]), "name": fmt.Sprint(d["n"])})
			}
		}
		offset += len(entries)
		if len(entries) == 0 || offset >= count {
			break
		}
	}
	return dirs, count, origin, nil
}

// Resolve115Path 把网盘绝对路径逐段解析为 cid（供前端手填路径时换算）
// GET /storage/115/resolve?path=/影视测试/俱乐部
// 依赖目录列表缓存，逐段匹配目录名；单层目录数超过一页（1150）时可能漏配
func (h *Handler) Resolve115Path(c *gin.Context) {
	p := strings.Trim(c.Query("path"), "/ \t")
	if p == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径为空"})
		return
	}
	ops, err := h.newPan115Ops()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cid := "0"
	for i, seg := range strings.Split(p, "/") {
		dirs, _, _, err := ops.listDirs(cid)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "读取目录失败: " + err.Error()})
			return
		}
		found := ""
		for _, d := range dirs {
			if fmt.Sprint(d["name"]) == seg {
				found = fmt.Sprint(d["cid"])
				break
			}
		}
		if found == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("路径不存在：/%s（第 %d 段 %q 未找到）", p, i+1, seg)})
			return
		}
		cid = found
	}
	c.JSON(http.StatusOK, gin.H{"cid": cid, "path": "/" + p})
}

// build115FileQuery 构造 115 文件列表查询参数
// 与 p115client web 通道默认参数一致（asc/cid/cur/fc_mix/o/offset/limit/show_dir），
// 多余参数曾被怀疑参与触发风控，保持最简
func build115FileQuery(cid string, offset int) url.Values {
	return url.Values{
		"asc":      {"1"},
		"cid":      {cid},
		"cur":      {"1"},
		"fc_mix":   {"1"},
		"o":        {"user_ptime"},
		"offset":   {fmt.Sprint(offset)},
		"limit":    {"1150"},
		"show_dir": {"1"},
	}
}

// localDirLimit 本地目录单次返回上限（有界读取，大目录/网络挂载不全量扫描）
const localDirLimit = 1000

// ListLocalDirs 浏览本地文件系统目录
// GET /storage/local/dirs?path=C:\media
// 注意：浏览的是 StrmHub 进程所在机器（Docker 部署时是容器内文件系统，卷挂载点也在其中）
func (h *Handler) ListLocalDirs(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		// 空路径时返回顶层：Windows 枚举盘符，Linux/Docker 列根目录
		if runtime.GOOS == "windows" {
			c.JSON(http.StatusOK, gin.H{"data": listWindowsDrives(), "path": ""})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": listUnixRootDirs(), "path": ""})
		return
	}

	// 有界读取：NAS 挂载（CIFS/NFS）下大目录全量 readdir + 排序可能要求数秒，
	// 只读前 localDirLimit+1 条即可判断截断；ReadDir(n) 不会预读整个目录
	f, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	entries, readErr := f.ReadDir(localDirLimit + 1)
	if readErr != nil && len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": readErr.Error()})
		return
	}
	truncated := len(entries) > localDirLimit
	if truncated {
		entries = entries[:localDirLimit]
	}

	dirs := make([]gin.H, 0)
	// 父目录
	parent := filepath.Dir(path)
	if parent != path {
		dirs = append(dirs, gin.H{"path": parent, "name": ".."})
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, gin.H{"path": filepath.Join(path, e.Name()), "name": e.Name()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return fmt.Sprint(dirs[i]["name"]) < fmt.Sprint(dirs[j]["name"]) })

	c.JSON(http.StatusOK, gin.H{"data": dirs, "path": path, "truncated": truncated})
}

// listWindowsDrives 枚举 Windows 盘符
func listWindowsDrives() []gin.H {
	var drives []gin.H
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		p := string(letter) + ":\\"
		if _, err := os.Stat(p); err == nil {
			drives = append(drives, gin.H{"path": p, "name": p})
		}
	}
	return drives
}

// listUnixRootDirs 列出 Linux/Docker 根目录下的文件夹（/media、/data 等挂载点在此可见）
func listUnixRootDirs() []gin.H {
	entries, err := os.ReadDir("/")
	if err != nil {
		return []gin.H{}
	}
	dirs := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, gin.H{"path": "/" + e.Name(), "name": e.Name()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return fmt.Sprint(dirs[i]["name"]) < fmt.Sprint(dirs[j]["name"]) })
	return dirs
}
