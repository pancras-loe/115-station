package api

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Emby 探测失败时，去 Emby 自己的日志里把 ffprobe 的报错找出来，告诉用户具体是哪条轨道、什么编码读不了。
// 否则用户只看到「失败」，得自己翻 Emby 日志，不少人会以为是本站的问题（2026-10-02《鱿鱼游戏》S02：
// 一条 xHE-AAC 音轨，Emby 内置 ffprobe 5.1 不支持，整个文件探测失败）。
//
// 接口取自 Emby 官方 REST 文档（SystemService），都要求管理员权限，Emby 后台建的 API Key 满足：
//   - GET /System/Logs/{Name}：取日志原文（可选 Sanitize 脱敏；不传 —— 脱敏可能把直链地址改掉，对不上条目）
//   - 当前日志文件名 embyserver.txt，每天零点轮换成 embyserver_xxxxx.txt（Emby 支持文章 Log-Files）
//
// 文档里还有 /System/Logs/{Name}/Lines，但没写分页参数，不用它。
// 只在「直链正常、Emby 却没给出轨道」时读一次（embyExtractOne 的 fileFault），零 115 请求；
// 读不到 / 认不出就返回空，界面退回通用说明。

const embyServerLogName = "embyserver.txt"

// embyProbeLogMax 日志最多读多少字节。Emby 当天的日志一般几 MB 到几十 MB；再大就不读了，免得卡住探测队列
const embyProbeLogMax = 256 << 20

var embyProbeLogClient = &http.Client{Timeout: time.Minute}

// embyProbeLogCause 在 Emby 日志里找这个版本最近一次 ffprobe 失败的原因；找不到返回空
func embyProbeLogCause(cfg embyRefreshCfg, src embyMediaSource) string {
	key := embyProbeLogKey(src.Path)
	if key == "" {
		return ""
	}
	u := cfg.ServerURL + "/System/Logs/" + url.PathEscape(embyServerLogName) + "?" + url.Values{"api_key": {cfg.APIKey}}.Encode()
	resp, err := embyProbeLogClient.Get(u)
	if err != nil {
		vlog("[Emby探测] 读 Emby 日志失败: %v", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		// 401 / 403：API Key 不是管理员的。只进详细日志，界面照常显示通用说明
		log.Printf("[Emby探测] ○ 读 Emby 日志 HTTP %d，没法给出具体是哪条轨道（需要管理员权限的 API Key）", resp.StatusCode)
		return ""
	}
	return parseFfprobeFailure(io.LimitReader(resp.Body, embyProbeLogMax), key)
}

var embyProbeKeyRe = regexp.MustCompile(`/d/([^/?.#]+)`)

// embyProbeLogKey 用来在日志里认出这个文件的片段：本站直链里的 pickcode（/d/<pickcode>.mkv?/文件名）。
// Emby 的 ffprobe 命令行里 -i 的就是 STRM 里写的直链；不是本站直链的不认
func embyProbeLogKey(p string) string {
	if !strings.Contains(p, "://") {
		return ""
	}
	if m := embyProbeKeyRe.FindStringSubmatch(p); m != nil {
		return "/d/" + m[1]
	}
	return ""
}

var (
	// Emby 日志每条记录以时间戳开头；没有时间戳的行（错误报告、ffprobe 输出）都属于上一条
	embyLogEntryRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)
	// Stream #0:4(chi): Audio: aac, 48000 Hz, 2 channels, fltp
	// Stream #0:15(tel): Audio: aac (HE-AAC), 48000 Hz, stereo, fltp (dub)
	ffStreamRe = regexp.MustCompile(`Stream #\d+:(\d+)(?:\[[^\]]*\])?(?:\(([A-Za-z]+)\))?: (Video|Audio|Subtitle|Data|Attachment): ([A-Za-z0-9_]+)(?: \(([^)]*)\))?`)
	ffTitleRe  = regexp.MustCompile(`^\s*title\s*:\s*(.+?)\s*$`)
	// [aac @ 0x2b5149c0] Audio object type 42 is not implemented. Update your FFmpeg version ...
	ffNotImplRe = regexp.MustCompile(`\[([A-Za-z0-9_]+) @ 0x[0-9a-f]+\] (.+? is not implemented)`)
	// Could not open codec for input stream 4
	ffOpenFailRe = regexp.MustCompile(`Could not open codec for input stream (\d+)`)
)

type ffStreamInfo struct {
	kind, lang, codec, profile, title string
}

// parseFfprobeFailure 从日志里挑出最后一条「ffprobe 失败、且 -i 的是 key 这个文件」的记录，
// 把它的 ffprobe 输出翻成一句话。纯函数，测试用真实日志
func parseFfprobeFailure(r io.Reader, key string) string {
	var cur, last []string
	flush := func() {
		if len(cur) > 0 && entryIsFfprobeFailure(cur, key) {
			last = cur
		}
		cur = nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if embyLogEntryRe.MatchString(line) {
			flush()
		}
		if cur != nil || embyLogEntryRe.MatchString(line) {
			cur = append(cur, line)
		}
	}
	flush()
	if last == nil {
		return ""
	}
	return describeFfprobeFailure(last)
}

func entryIsFfprobeFailure(lines []string, key string) bool {
	failed, hit := false, false
	for _, l := range lines {
		if strings.Contains(l, "ffprobe failed") {
			failed = true
		}
		if strings.Contains(l, key) {
			hit = true
		}
		if failed && hit {
			return true
		}
	}
	return false
}

func describeFfprobeFailure(lines []string) string {
	streams := map[int]*ffStreamInfo{}
	var lastStream *ffStreamInfo
	badStream := -1
	var notImpl, notImplCodec string
	for _, l := range lines {
		if m := ffStreamRe.FindStringSubmatch(l); m != nil {
			idx, _ := strconv.Atoi(m[1])
			st := &ffStreamInfo{kind: m[3], lang: m[2], codec: m[4], profile: m[5]}
			streams[idx] = st
			lastStream = st
			continue
		}
		if m := ffTitleRe.FindStringSubmatch(l); m != nil && lastStream != nil && lastStream.title == "" {
			lastStream.title = m[1]
			continue
		}
		if m := ffNotImplRe.FindStringSubmatch(l); m != nil && notImpl == "" {
			notImplCodec, notImpl = m[1], m[2]
			continue
		}
		if m := ffOpenFailRe.FindStringSubmatch(l); m != nil && badStream < 0 {
			badStream, _ = strconv.Atoi(m[1])
		}
	}
	reason := ""
	if notImpl != "" {
		reason = "Emby 内置的 ffprobe 不支持"
		if name := ffKnownUnsupported(notImplCodec, notImpl); name != "" {
			reason += " " + name
		}
		reason += "（" + notImpl + "）"
	}
	st := streams[badStream]
	if badStream < 0 || st == nil {
		return reason
	}
	track := fmt.Sprintf("%s #%d（%s）", ffKindText(st.kind), badStream, st.brief())
	if reason == "" {
		return track + " 打不开"
	}
	return track + "：" + reason
}

// ffKnownUnsupported 常见的「not implemented」翻成人话；认不出的只给原文
func ffKnownUnsupported(codec, msg string) string {
	if codec == "aac" && strings.Contains(msg, "object type 42") {
		return "xHE-AAC 音频"
	}
	return ""
}

func ffKindText(kind string) string {
	switch kind {
	case "Audio":
		return "音轨"
	case "Video":
		return "视频轨"
	case "Subtitle":
		return "字幕轨"
	}
	return "轨道"
}

func (s *ffStreamInfo) brief() string {
	out := s.lang
	if s.title != "" {
		out += "「" + s.title + "」"
	} else if out != "" {
		out += " "
	}
	codec := strings.ToUpper(s.codec)
	if s.profile != "" {
		codec += " " + s.profile
	}
	return out + codec
}
