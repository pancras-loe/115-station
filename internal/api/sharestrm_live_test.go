//go:build sharelive

package api

// 分享 STRM 阶段 0：拿真分享测 115 分享直链接口（设计见 docs/115-station-notes/SHARE-STRM-PLAN.md §2）。
// 不进 CI（要联网、要 Cookie、要一个真分享）：
//
//	SHARE_LIVE_COOKIE_FILE=D:\cookie.txt SHARE_LIVE_URL="https://115cdn.com/s/xxxx?password=abcd" \
//	  go test -tags sharelive -run ShareLive ./internal/api/ -v -timeout 30m
//
// 可选：
//
//	SHARE_LIVE_BAD_URLS  逗号分隔的「已取消 / 已过期 / 被屏蔽」分享，记下 snap 与 downurl 各回什么
//	SHARE_LIVE_BURST     连续取多少个文件的直链（默认 0 不测），看会不会被限
//	SHARE_LIVE_SAVE_LINK 把取到的完整直链存进这个文件（权限 600），取消分享后用 curl 再读一次
//
// 只读：列分享、取直链、按 Range 读 1 KB，不转存、不写网盘。
// 日志里不出现 Cookie；CDN 直链只打主机、路径前 40 个字符与过期参数 t。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 115driver UA115Browser 同款；ua115Unified 是本站 Cookie 通道的 UA
const shareLiveUA115 = "Mozilla/5.0 115Browser/27.0.5.7"

type shareLiveResp struct {
	HTTP    int
	State   string
	Errno   int
	Error   string
	Plain   string // 解密后的 data（截断）
	URL     string
	Expires time.Time
	Raw     string // 解不开时的原文（截断）
}

func (r shareLiveResp) String() string {
	s := fmt.Sprintf("http=%d state=%s errno=%d error=%q", r.HTTP, r.State, r.Errno, r.Error)
	if r.URL != "" {
		s += " url=" + shareLiveMaskURL(r.URL)
		if !r.Expires.IsZero() {
			s += fmt.Sprintf(" 过期=%s（%s 后）", r.Expires.Format("15:04:05"), time.Until(r.Expires).Round(time.Second))
		}
	}
	if r.Plain != "" {
		s += " data=" + r.Plain
	}
	if r.Raw != "" {
		s += " raw=" + r.Raw
	}
	return s
}

func shareLiveMaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return truncateStr(raw, 60)
	}
	return u.Host + truncateStr(u.Path, 40) + " t=" + u.Query().Get("t")
}

// shareLiveDownurl POST proapi /app/share/downurl（m115 加密，115driver DownloadByShareCodeWithUA 同款）。
// 自己发请求：post115FormResp 遇到非 200 只回错误、不给响应体，这里要看原文
func shareLiveDownurl(cookie, ua, shareCode, receiveCode, fid string) shareLiveResp {
	payload, _ := json.Marshal(map[string]string{"share_code": shareCode, "receive_code": receiveCode, "file_id": fid})
	form := url.Values{"data": {encrypt115(payload)}}
	api := "https://proapi.115.com/app/share/downurl"
	throttle115(api)
	defer throttle115Done(api)
	req, _ := http.NewRequest(http.MethodPost, api+"?t="+strconv.FormatInt(time.Now().Unix(), 10), strings.NewReader(form.Encode()))
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	resp, err := client115(20 * time.Second).Do(req)
	if err != nil {
		return shareLiveResp{Error: "请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := shareLiveResp{HTTP: resp.StatusCode}
	var env struct {
		State json.RawMessage `json:"state"`
		Errno json.Number     `json:"errno"`
		Code  json.Number     `json:"code"`
		Error string          `json:"error"`
		Msg   string          `json:"msg"`
		Data  json.RawMessage `json:"data"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.UseNumber()
	if d.Decode(&env) != nil {
		out.Raw = truncateStr(string(body), 300)
		return out
	}
	out.State = strings.TrimSpace(string(env.State))
	out.Errno, _ = strconv.Atoi(env.Errno.String())
	if out.Errno == 0 {
		out.Errno, _ = strconv.Atoi(env.Code.String())
	}
	out.Error = env.Error
	if out.Error == "" {
		out.Error = env.Msg
	}
	var enc string
	if json.Unmarshal(env.Data, &enc) != nil || enc == "" {
		if len(env.Data) > 0 && string(env.Data) != "null" {
			out.Raw = truncateStr(string(env.Data), 300)
		}
		return out
	}
	plain, err := decrypt115(enc)
	if err != nil {
		out.Raw = "解密失败: " + err.Error()
		return out
	}
	out.Plain = truncateStr(shareLiveScrub(string(plain)), 400)
	// 结构没核对过：先按 115driver SharedDownloadInfo 认 {url:{url}}，不行再正则兜底
	var info struct {
		URL json.RawMessage `json:"url"`
	}
	if json.Unmarshal(plain, &info) == nil {
		out.URL = openParseDownloadURL(info.URL)
	}
	if out.URL == "" {
		if m := reJSONURL.FindSubmatch(plain); m != nil {
			out.URL = strings.ReplaceAll(string(m[1]), `\/`, `/`)
		}
	}
	if u, err := url.Parse(out.URL); err == nil {
		if t, err := strconv.ParseInt(u.Query().Get("t"), 10, 64); err == nil {
			out.Expires = time.Unix(t, 0)
		}
	}
	return out
}

// shareLiveScrub 把明文里的直链换成打码版，日志里只留结构
func shareLiveScrub(s string) string {
	return reJSONURL.ReplaceAllStringFunc(s, func(m string) string {
		sub := reJSONURL.FindStringSubmatch(m)
		return `"url":"` + shareLiveMaskURL(strings.ReplaceAll(sub[1], `\/`, `/`)) + `"`
	})
}

// shareLiveSnap 原样调 /share/snap 一层，返回条目与原文摘要（错误时看 state / errno / error）
func shareLiveSnap(cookie, shareCode, receiveCode, cid string) ([]shareEntry, string, error) {
	body, err := getShareAPI("/share/snap", url.Values{
		"share_code": {shareCode}, "receive_code": {receiveCode}, "cid": {cid},
		"offset": {"0"}, "limit": {"1150"}, "asc": {"1"}, "fc_mix": {"0"},
	}, cookie, 15*time.Second)
	if err != nil {
		return nil, "", err
	}
	var snap struct {
		State bool   `json:"state"`
		Errno any    `json:"errno"`
		Error string `json:"error"`
		Data  struct {
			Count     int              `json:"count"`
			List      []map[string]any `json:"list"`
			ShareInfo map[string]any   `json:"shareinfo"`
		} `json:"data"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.UseNumber()
	if err := d.Decode(&snap); err != nil {
		return nil, truncateStr(string(body), 300), err
	}
	sum := fmt.Sprintf("state=%v errno=%v error=%q count=%d", snap.State, snap.Errno, snap.Error, snap.Data.Count)
	if si := snap.Data.ShareInfo; si != nil {
		sum += fmt.Sprintf(" share_state=%v share_title=%q expire=%v forbid=%v", si["share_state"], si["share_title"], si["expire_time"], si["forbid_reason"])
	}
	var out []shareEntry
	for _, m := range snap.Data.List {
		out = append(out, shareEntryOf(m))
	}
	return out, sum, nil
}

// shareLiveRange 用某个 UA 按 Range 读 CDN 直链开头 1 KB
func shareLiveRange(u, ua string) string {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Range", "bytes=0-1023")
	c := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return "请求失败: " + err.Error()
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return fmt.Sprintf("HTTP %d，读到 %d 字节，Content-Range=%q", resp.StatusCode, n, resp.Header.Get("Content-Range"))
}

func shareLiveCookie(t *testing.T) string {
	if f := os.Getenv("SHARE_LIVE_COOKIE_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 Cookie 文件: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	return strings.TrimSpace(os.Getenv("SHARE_LIVE_COOKIE"))
}

func shareLiveParse(raw string) (code, pass string) {
	code = extractShareCode(raw)
	if m := reSharePass.FindStringSubmatch(raw); m != nil {
		pass = m[1]
	}
	return
}

func TestShareLive(t *testing.T) {
	cookie := shareLiveCookie(t)
	link := os.Getenv("SHARE_LIVE_URL")
	if cookie == "" || link == "" {
		t.Skip("没设 SHARE_LIVE_COOKIE(_FILE) / SHARE_LIVE_URL")
	}
	code, pass := shareLiveParse(link)
	t.Logf("分享码=%s 提取码=%s", code, pass)

	// ---- 1. 列分享，找视频 ----
	entries, title, truncated, err := shareWalk(code, pass, cookie, 30)
	if err != nil {
		t.Fatalf("列分享失败: %v", err)
	}
	var videos []shareEntry
	for _, e := range entries {
		if !e.IsDir && isVideoName(e.Name) {
			videos = append(videos, e)
		}
	}
	t.Logf("【1 列分享】标题=%q 条目=%d 视频=%d 截断=%v", title, len(entries), len(videos), truncated)
	if len(videos) == 0 {
		t.Fatal("分享里没找到视频")
	}
	v := videos[0]
	t.Logf("  测试文件：fid=%s %s（%.1f MB，sha1=%s）", v.ID, v.path(), float64(v.Size)/1e6, v.Sha1)

	// ---- 2. 取直链的 UA ----
	uas := []struct{ name, ua string }{
		{"115Browser（115driver）", shareLiveUA115},
		{"本站 Cookie 通道 UA", ua115Unified()},
		{"下载 UA（ua115Download）", ua115Download},
		{"Chrome", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"},
		{"空 UA", ""},
	}
	var good shareLiveResp
	for _, u := range uas {
		r := shareLiveDownurl(cookie, u.ua, code, pass, v.ID)
		t.Logf("【2 取直链】%-22s %s", u.name, r)
		if good.URL == "" && r.URL != "" {
			good = r
		}
	}
	// 网页版接口：p115client 说限 200 MB，115driver 说 2026-09 起回「版本过低」，确认一次
	body, err := getShareAPI("/share/downurl", url.Values{"share_code": {code}, "receive_code": {pass}, "file_id": {v.ID}}, cookie, 15*time.Second)
	t.Logf("【2 网页版 share/downurl】err=%v body=%s", err, truncateStr(shareLiveScrub(string(body)), 300))

	// ---- 3. CDN 直链绑不绑 UA ----
	if good.URL == "" {
		t.Log("【3】没有任何 UA 取到直链，跳过 CDN 测试")
	} else {
		// 测「分享取消后旧直链还能不能读」：存下完整直链，取消分享后用 curl 再读一次
		if f := os.Getenv("SHARE_LIVE_SAVE_LINK"); f != "" {
			if err := os.WriteFile(f, []byte(good.URL+"\n"), 0o600); err != nil {
				t.Logf("【3】存直链失败: %v", err)
			} else {
				t.Logf("【3】完整直链已存到 %s", f)
			}
		}
		for _, u := range []struct{ name, ua string }{
			{"取链同款 115 UA", shareLiveUA115},
			{"Emby", "Emby/4.10.0.1"},
			{"VLC", "VLC/3.0.20 LibVLC/3.0.20"},
			{"Chrome", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"},
			{"空 UA", ""},
		} {
			t.Logf("【3 CDN Range】%-14s %s", u.name, shareLiveRange(good.URL, u.ua))
		}
	}

	// ---- 4. 错误形态：提取码错、目录 id 当文件、不存在的 fid ----
	bad := "zz" + strings.Repeat("9", 2)
	if pass == bad {
		bad = "yy88"
	}
	_, sum, err := shareLiveSnap(cookie, code, bad, "0")
	t.Logf("【4 提取码错】snap: %s err=%v", sum, err)
	t.Logf("【4 提取码错】downurl: %s", shareLiveDownurl(cookie, shareLiveUA115, code, bad, v.ID))
	t.Logf("【4 不存在的 fid】downurl: %s", shareLiveDownurl(cookie, shareLiveUA115, code, pass, "1"))
	for _, e := range entries {
		if e.IsDir {
			t.Logf("【4 拿目录 id 取链】downurl: %s", shareLiveDownurl(cookie, shareLiveUA115, code, pass, e.ID))
			break
		}
	}
	_, sum, err = shareLiveSnap(cookie, code, pass, "0")
	t.Logf("【4 正常分享 snap 摘要】%s err=%v", sum, err)

	// ---- 5. 失效分享 ----
	for _, raw := range strings.Split(os.Getenv("SHARE_LIVE_BAD_URLS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		bc, bp := shareLiveParse(raw)
		// 往下最多列 5 层找一个文件取链：根下常常只有一个目录
		queue, file, listed := []string{"0"}, "", 0
		for len(queue) > 0 && file == "" && listed < 5 {
			cid := queue[0]
			queue = queue[1:]
			items, sum, err := shareLiveSnap(cookie, bc, bp, cid)
			listed++
			t.Logf("【5 失效分享 %s】snap cid=%s: %s 条目=%d err=%v", bc, cid, sum, len(items), err)
			for _, e := range items {
				if !e.IsDir {
					file = e.ID
					break
				}
				queue = append(queue, e.ID)
			}
		}
		if file == "" {
			t.Logf("【5 失效分享 %s】没找到文件，跳过 downurl", bc)
			continue
		}
		t.Logf("【5 失效分享 %s】downurl fid=%s: %s", bc, file, shareLiveDownurl(cookie, shareLiveUA115, bc, bp, file))
	}

	// ---- 6. 连续取直链 ----
	n, _ := strconv.Atoi(os.Getenv("SHARE_LIVE_BURST"))
	if n > len(videos) {
		n = len(videos)
	}
	if n > 0 {
		ok, start := 0, time.Now()
		for i := 0; i < n; i++ {
			r := shareLiveDownurl(cookie, shareLiveUA115, code, pass, videos[i].ID)
			if r.URL != "" {
				ok++
			} else {
				t.Logf("【6 连续取链】第 %d 个失败：%s", i+1, r)
			}
		}
		t.Logf("【6 连续取链】%d 个成功 %d 个，用时 %s（本站节流间隔照常生效）", n, ok, time.Since(start).Round(time.Second))
	}
}
