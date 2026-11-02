package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 飞牛影视（2026-11-02，设计见 docs/115-station-notes/MEDIA-SERVER-PLAN.md §2 / §7）。
//
// 飞牛影视有两套接口：
//   - 兼容 Emby 的 /emby/…：VidHub、网易爆米花、Infuse 这类 Emby 客户端连的就是它，本站的 302 反代走它
//     （embyproxy.go 按类型把上游换成 地址/emby，LitePan internal/fnosproxy 同款）；
//   - 私有的 /v/api/v1/…：管理员账号密码登录拿 token，每个请求带 authx 签名。列媒体库（带实际目录）、
//     扫库只有它能做，本站入库刷新走它。
//
// 飞牛没有 API 密钥、没有 Webhook，Emby 那一套管理接口（按条目刷新、入库回查、演职人员、建库、
// 提前探测……）一律不碰：loadEmbyRefreshCfg / embyServerInfo 在飞牛下返回「没配」，
// 能力表（mediaServerUnsupported）写明原因。飞牛自己的 App / 网页走私有协议播放，拿不到本站的 302。
//
// 协议做法读自 LitePan internal/fnosproxy/management.go 与 MoviePilot app/modules/trimemedia/api.py（只读）。
// **都没连真飞牛跑过**，第一次看日志 [飞牛]。

// authx 签名里的两段常量是飞牛网页客户端内置的（MoviePilot trimemedia.py / api.py、LitePan 同值）
const (
	fnosSignSecret = "NDzZTVxnRKP8Z0jXg1VAMonaG8akvh"
	fnosAPIKey     = "16CCEB3D-AB42-077D-36A1-F355324E4237"
	fnosAppName    = "trimemedia-web"
)

// fnosCfg 飞牛的配置也存在 setting `emby` 里（类型 kind=fnos），只多两个字段
type fnosCfg struct {
	Kind           string `json:"kind"`
	ServerURL      string `json:"server_url"`
	Username       string `json:"fnos_username"`
	Password       string `json:"fnos_password"`
	PathMapping    string `json:"path_mapping"`
	Style          string `json:"style"`
	RefreshEnabled any    `json:"refresh_enabled"`
}

func (c fnosCfg) refreshCfg() embyRefreshCfg {
	return embyRefreshCfg{ServerURL: c.ServerURL, PathMapping: c.PathMapping, Style: c.Style, RefreshEnabled: c.RefreshEnabled}
}

// loadFnosCfg 当前配的是飞牛时返回它的配置
func loadFnosCfg() (fnosCfg, bool) {
	var cfg fnosCfg
	if json.Unmarshal([]byte(settingValueCompat("emby")), &cfg) != nil || !strings.EqualFold(strings.TrimSpace(cfg.Kind), msFnos) {
		return cfg, false
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	cfg.Username = strings.TrimSpace(cfg.Username)
	return cfg, cfg.ServerURL != ""
}

// fnosBase 私有接口的根：用户可能填成 http://nas:5666/v（飞牛网页的地址），去掉末尾的 /v
func fnosBase(serverURL string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(serverURL), "/"), "/v")
}

// fnosEmbyAPIBase 兼容 Emby 的那套接口的根：地址/emby（用户填的若是 …/v 先去掉）
func fnosEmbyAPIBase(serverURL string) string {
	b := fnosBase(serverURL)
	if strings.HasSuffix(strings.ToLower(b), "/emby") {
		return b
	}
	return b + "/emby"
}

// fnosAuthX 一次请求的签名：md5(secret _ 路径 _ 随机数 _ 毫秒时间戳 _ md5(请求体) _ apikey)。
// 路径是带 /v 的完整路径（/v/api/v1/mdb/list）；签的必须是真正发出去的那串字节
func fnosAuthX(apiPath string, body []byte, now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	nonce := strconv.Itoa(100000 + int(uint32(b[0])<<16|uint32(b[1])<<8|uint32(b[2]))%900000)
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	bodySum := md5.Sum(body)
	sign := md5.Sum([]byte(strings.Join([]string{fnosSignSecret, apiPath, nonce, ts, hex.EncodeToString(bodySum[:]), fnosAPIKey}, "_")))
	return "nonce=" + nonce + "&timestamp=" + ts + "&sign=" + hex.EncodeToString(sign[:])
}

// errFnosAuth 登录态失效 / 账号密码不对：调用方据此重新登录一次
var errFnosAuth = errors.New("飞牛影视认证失败")

// errFnosConn 连不上：登录时与「账号密码不对」分开报
var errFnosConn = errors.New("连接飞牛影视失败")

// fnosClient 一台飞牛的私有接口客户端。token 进程内缓存，失效了重登一次
type fnosClient struct {
	base     string
	user     string
	password string
	http     *http.Client
}

func newFnosClient(cfg fnosCfg) *fnosClient {
	return &fnosClient{base: fnosBase(cfg.ServerURL), user: cfg.Username, password: cfg.Password, http: &http.Client{Timeout: 20 * time.Second}}
}

var fnosTokens = struct {
	sync.Mutex
	m map[string]string // base|user → token
}{m: map[string]string{}}

func (c *fnosClient) tokenKey() string { return c.base + "|" + c.user }

// call 发一次私有接口请求（path 是 /api/v1 之后那段），登录态失效时重登再试一次
func (c *fnosClient) call(ctx context.Context, method, path string, body, out any) error {
	fnosTokens.Lock()
	token := fnosTokens.m[c.tokenKey()]
	fnosTokens.Unlock()
	if token == "" {
		var err error
		if token, err = c.login(ctx); err != nil {
			return err
		}
	}
	err := c.do(ctx, token, method, "/v/api/v1"+path, body, out)
	if !errors.Is(err, errFnosAuth) {
		return err
	}
	if token, err = c.login(ctx); err != nil {
		return err
	}
	return c.do(ctx, token, method, "/v/api/v1"+path, body, out)
}

// login 先试新版（密码 sha256）再试老版（明文），LitePan 同序
func (c *fnosClient) login(ctx context.Context) (string, error) {
	if c.user == "" || c.password == "" {
		return "", errors.New("请先填写飞牛影视管理员账号和密码")
	}
	var data struct {
		Token string `json:"token"`
	}
	sum := sha256.Sum256([]byte(c.password))
	err := c.do(ctx, "", http.MethodPost, "/v/api/v2/user/loginByPassword",
		map[string]any{"username": c.user, "password": hex.EncodeToString(sum[:]), "app_name": fnosAppName}, &data)
	if err != nil || strings.TrimSpace(data.Token) == "" {
		data.Token = ""
		err = c.do(ctx, "", http.MethodPost, "/v/api/v1/login",
			map[string]any{"username": c.user, "password": c.password, "app_name": fnosAppName}, &data)
	}
	if err != nil || strings.TrimSpace(data.Token) == "" {
		if err == nil || !errors.Is(err, errFnosConn) {
			// 两个登录接口都回业务错误：多半是账号密码不对，原话附在后面
			why := ""
			if err != nil {
				why = "（" + err.Error() + "）"
			}
			err = errors.New("飞牛影视管理员登录失败，请检查账号和密码" + why)
		}
		return "", err
	}
	fnosTokens.Lock()
	fnosTokens.m[c.tokenKey()] = data.Token
	fnosTokens.Unlock()
	return data.Token, nil
}

func (c *fnosClient) do(ctx context.Context, token, method, apiPath string, body, out any) error {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPath, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", c.base)
	req.Header.Set("authx", fnosAuthX(apiPath, raw, time.Now()))
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errFnosConn, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errFnosAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("飞牛影视接口返回 HTTP %d", resp.StatusCode)
	}
	var env struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return errors.New("飞牛影视接口响应无法解析（地址填的是飞牛影视吗）")
	}
	if code, _ := env.Code.Int64(); code != 0 {
		// 业务错误码哪几个代表登录失效没有资料；token 缓存着时一律当失效重登一次，
		// 重登后还错就是真错（call 只重试一次）
		if token != "" {
			return fmt.Errorf("%w: %s", errFnosAuth, fnosMsg(code, env.Msg))
		}
		return errors.New("飞牛影视接口失败: " + fnosMsg(code, env.Msg))
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return errors.New("飞牛影视接口数据无法解析")
		}
	}
	return nil
}

func fnosMsg(code int64, msg string) string {
	if msg = strings.TrimSpace(msg); msg != "" {
		return msg
	}
	return "错误码 " + strconv.FormatInt(code, 10)
}

// fnosLibrary 管理员看到的媒体库，dir_list 是飞牛那边的实际目录
type fnosLibrary struct {
	GUID    string   `json:"guid"`
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	DirList []string `json:"dir_list"`
}

func (l fnosLibrary) label() string {
	if l.Name != "" {
		return l.Name
	}
	return l.Title
}

func (c *fnosClient) libraries(ctx context.Context) ([]fnosLibrary, error) {
	var out []fnosLibrary
	err := c.call(ctx, http.MethodGet, "/mdb/list", nil, &out)
	return out, err
}

// scan 扫一个媒体库。飞牛没有按路径 / 按条目刷新（MoviePilot refresh_library_by_items 也是找到库整库扫）
func (c *fnosClient) scan(ctx context.Context, guid string) error {
	return c.call(ctx, http.MethodPost, "/mdb/scan/"+url.PathEscape(guid), map[string]any{"guid": guid}, nil)
}

// fnosSummary 可见内容的数量（测试连接时显示）
type fnosSummary struct {
	Movie int `json:"movie"`
	TV    int `json:"tv"`
	Total int `json:"total"`
}

func (c *fnosClient) summary(ctx context.Context) (fnosSummary, error) {
	var s fnosSummary
	err := c.call(ctx, http.MethodGet, "/mediadb/sum", nil, &s)
	return s, err
}

// ==================== 入库刷新 ====================

// 飞牛只能整库扫：整理一轮可能落好几次盘，每次都扫一遍整库太重，按库攒 30 秒再扫
// （openStrm 增量侧同理：一集触发一次全库扫描会把服务器打瘫）
const fnosScanDelay = 30 * time.Second

var fnosScanQueue = struct {
	sync.Mutex
	pending map[string]string // guid → 库名
	armed   bool              // 已经排了一次扫描，后来的并进去
}{pending: map[string]string{}}

// fnosScanAfter 测试里换成立即执行
var fnosScanAfter = func(d time.Duration, f func()) *time.Timer { return time.AfterFunc(d, f) }

// fnosLibsFor 纯函数：飞牛那边的路径落在哪几个库里。路径在库之上（全量传的是媒体根）时，
// 根下面的库全算；一个都没对上的原样返回给调用方报出来
func fnosLibsFor(paths []string, libs []fnosLibrary) (hit []fnosLibrary, unmatched []string) {
	seen := map[string]bool{}
	for _, p := range paths {
		found := false
		for _, lib := range libs {
			if lib.GUID == "" {
				continue
			}
			if embyPathUnder(p, lib.DirList) || embyLocationsUnder(lib.DirList, p) {
				found = true
				if !seen[lib.GUID] {
					seen[lib.GUID] = true
					hit = append(hit, lib)
				}
			}
		}
		if !found {
			unmatched = append(unmatched, p)
		}
	}
	sort.Slice(hit, func(i, j int) bool { return hit[i].GUID < hit[j].GUID })
	return hit, unmatched
}

// fnosNotifyPaths 本地 STRM 有增删 → 攒一会儿扫对应的飞牛媒体库。删除也是扫：没有按条目删的接口
func fnosNotifyPaths(cfg fnosCfg, localPaths []string) {
	if len(localPaths) == 0 {
		return
	}
	rc := cfg.refreshCfg()
	if !rc.enabled() {
		return
	}
	paths := make([]string, 0, len(localPaths))
	for _, p := range localPaths {
		if ep := embyPathOf(rc, p); ep != "" {
			paths = append(paths, ep)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	libs, err := newFnosClient(cfg).libraries(ctx)
	if err != nil {
		log.Printf("[飞牛] ✗ 读媒体库失败，这次没刷新: %v", err)
		return
	}
	hit, unmatched := fnosLibsFor(paths, libs)
	if len(unmatched) > 0 {
		log.Printf("[飞牛] ○ %d 个变更路径不在任何飞牛媒体库里（%v）—— 路径映射配对了吗", len(unmatched), unmatched)
	}
	if len(hit) == 0 {
		return
	}
	fnosScanQueue.Lock()
	for _, lib := range hit {
		fnosScanQueue.pending[lib.GUID] = lib.label()
	}
	start := !fnosScanQueue.armed
	fnosScanQueue.armed = true
	fnosScanQueue.Unlock()
	// 计时器在锁外起：到点的 fnosFlushScans 自己要拿这把锁
	if start {
		fnosScanAfter(fnosScanDelay, fnosFlushScans)
	}
}

func fnosFlushScans() {
	fnosScanQueue.Lock()
	pending := fnosScanQueue.pending
	fnosScanQueue.pending, fnosScanQueue.armed = map[string]string{}, false
	fnosScanQueue.Unlock()
	cfg, ok := loadFnosCfg()
	if !ok || len(pending) == 0 {
		return
	}
	c := newFnosClient(cfg)
	guids := make([]string, 0, len(pending))
	for g := range pending {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	for _, g := range guids {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.scan(ctx, g)
		cancel()
		if err != nil {
			log.Printf("[飞牛] ✗ 扫描媒体库 %s 失败: %v", pending[g], err)
			continue
		}
		log.Printf("[飞牛] ○ 已提交媒体库扫描：%s", pending[g])
	}
}

// testFnosConnection 测试连接：登录、读库、读数量
func testFnosConnection(cfg fnosCfg) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := newFnosClient(cfg)
	libs, err := c.libraries(ctx)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	out := map[string]any{"ok": true, "kind": msFnos, "product": mediaServerName(msFnos), "server_name": "飞牛影视", "library_count": len(libs)}
	if s, err := c.summary(ctx); err == nil {
		out["movies"], out["series"] = s.Movie, s.TV
	}
	return out
}
