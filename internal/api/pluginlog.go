package api

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 扩展功能各自的日志（做法同 MoviePilot 的插件日志 logs/plugins/<id>.log）：
// 照常写进 app.log，同时追加到 /logs/plugins/<插件 key>.log。
// 总日志 500 行很快被同步 / 整理刷掉，一天跑一次的签到第二天就看不到了，所以单独留一份。
//
// key 与前端扩展功能页的插件 key 一致（PluginsPage.vue 的 plugins[].key），
// 接口只认 pluginLogKeys 里的，防 ../。

// pluginLogDir 测试里换成临时目录（testmain_test.go）
var pluginLogDir = "/logs/plugins"

const (
	pluginLogMaxBytes = 1 << 20 // 超过 1MB 切一份，留一份旧的（.1）
	pluginLogMaxLines = 5000
)

// pluginLogKeys 有单独日志的插件
var pluginLogKeys = map[string]bool{
	"checkin": true, "ptsignin": true, "covergen": true, "rbclean": true, "offclean": true,
	"dirclean": true, "ali2115": true, "strmmigrate": true, "alidirclean": true, "alirbclean": true,
	"sharestrm": true,
}

// pluginLogger 一个插件的一个日志前缀。同一插件可以有几个前缀（STRM 迁移的接管 / 替换地址），写进同一个文件
type pluginLogger struct{ key, tag string }

func pluginLog(key, tag string) pluginLogger { return pluginLogger{key: key, tag: tag} }

var (
	plogCheckin     = pluginLog("checkin", "115签到")
	plogPtSignin    = pluginLog("ptsignin", "PT签到")
	plogCoverGen    = pluginLog("covergen", "媒体库海报")
	plogRbClean     = pluginLog("rbclean", "115回收站")
	plogOffClean    = pluginLog("offclean", "离线清理")
	plogDirClean    = pluginLog("dirclean", "115清空目录")
	plogAli2115     = pluginLog("ali2115", "阿里秒传")
	plogAdopt       = pluginLog("strmmigrate", "STRM接管")
	plogStrmRebase  = pluginLog("strmmigrate", "STRM替换地址")
	plogAliDirClean = pluginLog("alidirclean", "阿里清空目录")
	plogAliRbClean  = pluginLog("alirbclean", "阿里回收站")
)

// Printf 写一行：「[前缀] 消息」，app.log 与插件日志各一份
func (p pluginLogger) Printf(format string, args ...any) {
	line := "[" + p.tag + "] " + fmt.Sprintf(format, args...)
	_ = log.Output(2, line)
	pluginLogAppend(p.key, line)
}

var (
	pluginLogMu   sync.Mutex
	pluginLogWarn sync.Once
)

func pluginLogPath(key string) string { return filepath.Join(pluginLogDir, key+".log") }

// pluginLogAppend 追加一行（带与 app.log 同格式的时间）。写不了只在 app.log 里提一次，不影响插件本身
func pluginLogAppend(key, line string) {
	pluginLogMu.Lock()
	defer pluginLogMu.Unlock()
	if err := os.MkdirAll(pluginLogDir, 0o755); err != nil {
		pluginLogWarn.Do(func() { log.Printf("[插件日志] ○ 建不了 %s，插件日志只写进总日志: %v", pluginLogDir, err) })
		return
	}
	path := pluginLogPath(key)
	if st, err := os.Stat(path); err == nil && st.Size() >= pluginLogMaxBytes {
		_ = os.Rename(path, path+".1") // 覆盖上一份旧的
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		pluginLogWarn.Do(func() { log.Printf("[插件日志] ○ 写不了 %s，插件日志只写进总日志: %v", path, err) })
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().Format("2006/01/02 15:04:05") + " " + strings.TrimRight(line, "\n") + "\n")
}

// readPluginLog 最后 n 行（旧的那份在前）
func readPluginLog(key string, n int) []string {
	pluginLogMu.Lock()
	defer pluginLogMu.Unlock()
	var lines []string
	for _, p := range []string{pluginLogPath(key) + ".1", pluginLogPath(key)} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// GetPluginLogs GET /plugins/logs/:key?lines=1000
func (h *Handler) GetPluginLogs(c *gin.Context) {
	key := c.Param("key")
	if !pluginLogKeys[key] {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有这个插件的日志"})
		return
	}
	n, _ := strconv.Atoi(c.Query("lines"))
	if n <= 0 {
		n = 1000
	}
	if n > pluginLogMaxLines {
		n = pluginLogMaxLines
	}
	lines := readPluginLog(key, n)
	if lines == nil {
		lines = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"lines": lines})
}

// ClearPluginLogs POST /plugins/logs/:key/clear：只删这个插件的那份，app.log 不动
func (h *Handler) ClearPluginLogs(c *gin.Context) {
	key := c.Param("key")
	if !pluginLogKeys[key] {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有这个插件的日志"})
		return
	}
	pluginLogMu.Lock()
	for _, p := range []string{pluginLogPath(key), pluginLogPath(key) + ".1"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			pluginLogMu.Unlock()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "清空失败: " + err.Error()})
			return
		}
	}
	pluginLogMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"message": "已清空"})
}
