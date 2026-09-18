package api

// ==================== 观影门户：服务端转封装（ffmpeg remux → HLS） ====================
//
// 浏览器直出解不了 MKV/没有多音轨 API；这里用 ffmpeg 在服务端把 115 文件
// 无损转封装为 HLS（-c copy，不重编码，同 Emby「直接串流」模式），并提供：
//   /api/portal/probe?pick=  → ffprobe 识别内嵌音轨/字幕（codec/语言/标题）
//   /api/portal/hls?pick=&a=N → 启动转封装会话，返回 m3u8/分片
//   /api/portal/estr?pick=&s=N → 提取内嵌字幕轨为 WebVTT
// 会话空闲自动清理；ffmpeg 缺失时接口报错，前端回退直链播放。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"strmhub/internal/model"

	"github.com/gin-gonic/gin"
)

// hlsSession 一个转封装会话（一次播放一个）
type hlsSession struct {
	dir      string
	pick     string // 精确记录（会话复用按它比对； Contains(dir,pick) 前缀误配的修复）
	vc       string // copy / transcode（复用时必须一致，否则分片编码不对）
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	lastHit  time.Time
	audioRel int
}

// portalHLSMaxSessions 并发转封装/转码会话上限（公开端点的 CPU/磁盘保护）
const portalHLSMaxSessions = 4

var (
	hlsSessions   = map[string]*hlsSession{}
	hlsSessionsMu sync.Mutex
	probeCache    = map[string]portalProbeResult{}
	probeCacheMu  sync.Mutex
)

func ffmpegBin() string { return "/usr/bin/ffmpeg" }

func hasFFmpeg() bool {
	_, err := os.Stat(ffmpegBin())
	if err == nil {
		return true
	}
	// 开发机 / 自定义安装路径
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return true
	} else if p != "" {
		return true
	}
	b, _ := exec.Command("ffmpeg", "-version").CombinedOutput()
	return strings.Contains(string(b), "ffmpeg")
}

// portalProbeResult ffprobe 识别结果（缓存 10 分钟）
type portalProbeResult struct {
	Video    []probeStream `json:"video"`
	Audio    []probeStream `json:"audio"`
	Subs     []probeStream `json:"subs"`
	Duration float64       `json:"duration,omitempty"` // 总时长（秒）；边转边播时先展示
	At       time.Time     `json:"-"`
}

type probeStream struct {
	Index    int    `json:"index"` // ffmpeg 全局流索引
	Rel      int    `json:"rel"`   // 同类流内序号（音轨 0/1/2…）
	Codec    string `json:"codec"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Channels int    `json:"channels,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// portalProbe ffprobe 识别内嵌轨道
func portalProbe(c *gin.Context) {
	pick := c.Query("pick")
	if pick == "" || len(pick) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad pick"})
		return
	}
	probeCacheMu.Lock()
	if pr, ok := probeCache[pick]; ok && time.Since(pr.At) < 10*time.Minute {
		probeCacheMu.Unlock()
		c.JSON(http.StatusOK, pr)
		return
	}
	probeCacheMu.Unlock()

	if !hasFFmpeg() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务端未安装 ffmpeg，无法识别内嵌轨道"})
		return
	}
	urlStr, headers, err := portalPickURL(c, pick)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取直链失败: " + err.Error()})
		return
	}
	args := append([]string{"-hide_banner", "-loglevel", "error"},
		append(ffmpegHeaderArgs(headers),
			"-print_format", "json", "-show_streams", "-show_format",
			"-probesize", "5M", "-analyzeduration", "5M", urlStr)...)
	out, err := exec.Command("ffprobe", args...).CombinedOutput()
	if err != nil {
		// 兼容：ffprobe 可能也在 /usr/bin
		out2, err2 := exec.Command("/usr/bin/ffprobe", args...).CombinedOutput()
		if err2 != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "ffprobe 失败: " + truncateStr(string(out), 150)})
			return
		}
		out = out2
	}
	var pr portalProbeResult
	var wrap struct {
		Streams []struct {
			Index       int            `json:"index"`
			CodecName   string         `json:"codec_name"`
			CodecType   string         `json:"codec_type"`
			Channels    int            `json:"channels,omitempty"`
			Disposition map[string]int `json:"disposition"`
			Tags        struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
	}
	var fmtWrap struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	_ = json.Unmarshal(out, &fmtWrap)
	pr.Duration, _ = strconv.ParseFloat(fmtWrap.Format.Duration, 64)
	if err := json.Unmarshal(out, &wrap); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ffprobe 输出解析失败"})
		return
	}
	ai, si := 0, 0
	for _, st := range wrap.Streams {
		ps := probeStream{Index: st.Index, Codec: st.CodecName,
			Language: st.Tags.Language, Title: st.Tags.Title, Channels: st.Channels,
			Default: st.Disposition["default"] == 1}
		switch st.CodecType {
		case "video":
			pr.Video = append(pr.Video, ps)
		case "audio":
			ps.Rel = ai
			ai++
			pr.Audio = append(pr.Audio, ps)
		case "subtitle":
			ps.Rel = si
			si++
			pr.Subs = append(pr.Subs, ps)
		}
	}
	pr.At = time.Now()
	probeCacheMu.Lock()
	probeCache[pick] = pr
	probeCacheMu.Unlock()
	c.JSON(http.StatusOK, pr)
}

// portalPickURL pick_code → 115 直链 + 必需请求头（直链与签发 UA 绑定，
// ffmpeg 服务端拉流必须按签发时的 UA/Referer 请求，否则 115 拒绝 → 无声失败）
func portalPickURL(c *gin.Context, pick string) (urlStr string, headers map[string]string, err error) {
	ua := c.Request.UserAgent()
	urlStr, headers, err = proxyDownloadURLFull(model.DB, portalCfg, pick, ua)
	if headers == nil {
		headers = map[string]string{"User-Agent": ua}
	}
	if _, ok := headers["User-Agent"]; !ok {
		headers["User-Agent"] = ua
	}
	return
}

// ffmpegHeaderArgs 把必需头转成 ffmpeg/ffprobe 参数（-user_agent + -headers）
func ffmpegHeaderArgs(headers map[string]string) []string {
	args := []string{"-user_agent", headers["User-Agent"]}
	others := []string{}
	for k, v := range headers {
		if k == "User-Agent" {
			continue
		}
		others = append(others, k+": "+v)
	}
	if len(others) > 0 {
		args = append(args, "-headers", strings.Join(others, "\r\n")+"\r\n")
	}
	return args
}

// cappedWriter 截尾字节缓冲（收 ffmpeg stderr 首段，防无限占用内存）
type cappedWriter struct {
	buf   []byte
	limit int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if len(w.buf) < w.limit {
		w.buf = append(w.buf, p...)
		if len(w.buf) > w.limit {
			w.buf = w.buf[:w.limit]
		}
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return string(w.buf) }

// portalHLS 启动/复用转封装会话；?pick=&a=<音轨序号>；返回 sid
func portalHLS(c *gin.Context) {
	// 兼容 JSON body 与 query 两种传参
	var body struct {
		Pick string `json:"pick"`
		A    int    `json:"a"`
		VC   string `json:"vc"`
	}
	_ = c.ShouldBindJSON(&body)
	pick := body.Pick
	if pick == "" {
		pick = c.Query("pick")
	}
	if pick == "" || len(pick) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad pick"})
		return
	}
	if !hasFFmpeg() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务端未安装 ffmpeg，无法转封装播放"})
		return
	}
	audioRel := body.A
	if audioRel == 0 {
		fmt.Sscanf(c.Query("a"), "%d", &audioRel)
	}
	// 转码模式（会话复用须比对；默认 copy 无损转封装）
	vc := body.VC
	if vc == "" {
		vc = c.Query("vc")
	}
	if vc == "" {
		vc = "copy"
	}

	urlStr, headers, err := portalPickURL(c, pick)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取直链失败: " + err.Error()})
		return
	}

	hlsSessionsMu.Lock()
	// 复用：同 pick + 同音轨 + 同转码模式且活跃。
	// 修复点：① 原用 strings.Contains(ss.dir, pick) 匹配——pick 是另一个
	// pick 的前缀时会错拿他人会话，改为精确记录 pick；② 不比对 vc 时
	// transcode 请求会复用 copy 会话拿到 H.265 分片（播放失败）
	for sid, ss := range hlsSessions {
		if ss.pick == pick && time.Since(ss.lastHit) < 2*time.Minute &&
			ss.audioRel == audioRel && ss.vc == vc {
			ss.lastHit = time.Now()
			hlsSessionsMu.Unlock()
			c.JSON(http.StatusOK, gin.H{"sid": sid, "m3u8": "/api/portal/hls/" + sid + "/index.m3u8"})
			return
		}
	}
	// 并发上限：只统计 ffmpeg 仍存活的会话——进程已退出的死会话不占位
	//（此前启动即占位，ffmpeg 秒退后连续重试 4 次就全被"会话已满"拒绝）
	live := 0
	for _, ss := range hlsSessions {
		if ss.cmd == nil || ss.cmd.ProcessState == nil {
			live++
		}
	}
	if live >= portalHLSMaxSessions {
		hlsSessionsMu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": fmt.Sprintf("并发播放会话已满（%d），请稍后再试或关闭其他播放页", live)})
		return
	}
	hlsSessionsMu.Unlock()

	dir, err := os.MkdirTemp("", "hlss-"+pick+"-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话目录失败"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	// ffmpeg：-c copy 无损转封装 HLS（MKV→TS）。字幕轨不进 HLS（单独提取）。
	// vc=copy：无损转封装（H.264 源）；vc=transcode：H.265→H.264 转码（浏览器能播）
	codecArgs := []string{"-c", "copy"}
	if vc == "transcode" {
		codecArgs = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-c:a", "aac", "-b:a", "128k", "-ac", "2"}
	}
	segArgs := []string{"-hide_banner", "-loglevel", "error"}
	segArgs = append(segArgs, ffmpegHeaderArgs(headers)...)
	segArgs = append(segArgs, "-probesize", "5M", "-analyzeduration", "5M",
		"-i", urlStr,
		"-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d", audioRel))
	segArgs = append(segArgs, codecArgs...)
	segArgs = append(segArgs,
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_segment_filename", filepath.Join(dir, "seg%05d.ts"),
		filepath.Join(dir, "index.m3u8"))
	cmd := exec.CommandContext(ctx, ffmpegBinOrPath(), segArgs...)
	// stderr 截尾收集：ffmpeg 读直链失败（403/超时/区域限制）时退出原因可查
	stderrCap := &cappedWriter{limit: 2048}
	cmd.Stderr = stderrCap
	if err := cmd.Start(); err != nil {
		cancel()
		os.RemoveAll(dir)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "启动 ffmpeg 失败: " + err.Error()})
		return
	}
	sid := fmt.Sprintf("%d", time.Now().UnixNano())
	ss := &hlsSession{dir: dir, pick: pick, vc: vc, cmd: cmd, cancel: cancel, lastHit: time.Now(), audioRel: audioRel}
	hlsSessionsMu.Lock()
	hlsSessions[sid] = ss
	hlsSessionsMu.Unlock()
	go func() {
		if err := cmd.Wait(); err != nil {
			// 未产出 m3u8 即退出 = 播放必然失败，记录 ffmpeg 报错便于定位
			//（115 直链对数据中心 IP 的 403、超时等）
			if _, statErr := os.Stat(filepath.Join(dir, "index.m3u8")); statErr != nil {
				log.Printf("[门户] ✗ 转封装会话 %s 退出且无产出（vc=%s）: %v ─ ffmpeg: %s",
					sid, vc, err, truncateStr(stderrCap.String(), 200))
			}
		}
	}()
	// 路径式地址：m3u8 内分片是相对路径，基于此 URL 解析自然带上 sid
	c.JSON(http.StatusOK, gin.H{"sid": sid, "m3u8": "/api/portal/hls/" + sid + "/index.m3u8"})
}

func ffmpegBinOrPath() string {
	if _, err := os.Stat(ffmpegBin()); err == nil {
		return ffmpegBin()
	}
	return "ffmpeg"
}

// portalHLSServe 提供会话的 m3u8 与分片：/api/portal/hls/{sid}/{file}
// m3u8 里的分片是相对路径，基于路径式 URL 解析自动带上 sid。
func portalHLSServe(c *gin.Context) {
	sid := c.Param("sid")
	if sid == "" {
		sid = c.Query("sid")
	}
	f := strings.TrimPrefix(c.Param("file"), "/")
	if f == "" {
		f = "index.m3u8"
	}
	f = filepath.Base(f) // 防目录穿越
	hlsSessionsMu.Lock()
	ss, ok := hlsSessions[sid]
	if ok {
		ss.lastHit = time.Now()
	}
	hlsSessionsMu.Unlock()
	if !ok {
		c.String(http.StatusNotFound, "会话不存在或已过期")
		return
	}
	full := filepath.Join(ss.dir, f)
	// 边转边播：文件未生成时等待（m3u8 最多 30s、分片最多 30s）
	if _, err := os.Stat(full); err != nil {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(full); err == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if _, err := os.Stat(full); err != nil {
			c.String(http.StatusNotFound, "分片尚未生成（转封装可能失败）")
			return
		}
	}
	if f == "index.m3u8" {
		c.Header("Cache-Control", "no-store")
	} else {
		// 分片内容不变，可缓存
		c.Header("Cache-Control", "public, max-age=3600")
	}
	c.File(full)
}

// portalExtractSub 提取内嵌字幕轨为 WebVTT（ffmpeg -c:s webvtt 直出）
func portalExtractSub(c *gin.Context) {
	pick := c.Query("pick")
	rel := 0
	fmt.Sscanf(c.Query("s"), "%d", &rel)
	if pick == "" || len(pick) > 64 {
		c.String(http.StatusBadRequest, "bad pick")
		return
	}
	if !hasFFmpeg() {
		c.String(http.StatusServiceUnavailable, "服务端未安装 ffmpeg")
		return
	}
	urlStr, headers, err := portalPickURL(c, pick)
	if err != nil {
		c.String(http.StatusBadGateway, "获取直链失败")
		return
	}
	cmdArgs := append([]string{"-hide_banner", "-loglevel", "error"},
		append(append(ffmpegHeaderArgs(headers), "-i", urlStr),
			"-map", fmt.Sprintf("0:s:%d", rel),
			"-c:s", "webvtt",
			"-f", "webvtt", "pipe:1")...)
	out, err := exec.Command(ffmpegBinOrPath(), cmdArgs...).Output()
	if err != nil {
		c.String(http.StatusBadGateway, "字幕提取失败（该字幕轨编码可能是图片格式 PGS/VobSub，无法转文本）")
		return
	}
	c.Header("Access-Control-Allow-Origin", "*")
	c.Data(http.StatusOK, "text/vtt; charset=utf-8", out)
}

// hlsCleaner 空闲会话回收（2 分钟无访问即杀 ffmpeg 删分片）
func hlsCleaner() {
	for {
		select {
		case <-stopCh:
			return // 进程退出：回收循环跟着停
		case <-time.After(30 * time.Second):
		}
		type deadSession struct {
			dir    string
			cancel context.CancelFunc
			cmd    *exec.Cmd
		}
		var dead []deadSession
		hlsSessionsMu.Lock()
		for sid, ss := range hlsSessions {
			if time.Since(ss.lastHit) > 2*time.Minute {
				dead = append(dead, deadSession{ss.dir, ss.cancel, ss.cmd})
				delete(hlsSessions, sid)
				log.Printf("[门户] 转封装会话回收：%s", sid)
			}
		}
		hlsSessionsMu.Unlock()
		// 进程终止与目录删除放在锁外：此前持锁做文件 IO 会阻塞所有 HLS 请求
		for _, d := range dead {
			d.cancel()
			if d.cmd != nil && d.cmd.Process != nil {
				d.cmd.Process.Kill()
			}
			os.RemoveAll(d.dir)
		}
		// probeCache 过期清出（此前只写不删，TTL 仅在读取命中时判断）
		probeCacheMu.Lock()
		for k, pr := range probeCache {
			if time.Since(pr.At) > 10*time.Minute {
				delete(probeCache, k)
			}
		}
		probeCacheMu.Unlock()
	}
}
