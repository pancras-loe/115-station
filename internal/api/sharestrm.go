package api

// 分享 STRM（2026-10-31，设计见 docs/115-station-notes/SHARE-STRM-PLAN.md）：
// 不转存、不占自己的网盘空间，本地 STRM 直接指向别人的 115 分享，播放时按分享换 CDN 直链 302。
//
// 和自己网盘的文件是两套东西，别混：
//   - 分享里的文件不进 SyncedFile（ShareSource / ShareFile 两张表）：深删、洗版、sha1 去重都按自己网盘设计，
//     混进来深删就可能对别人分享里的 fid 发删除；
//   - 本地放在单独的分享 STRM 根（setting sharestrm.root，媒体根下），不和任何媒体库目录重叠：
//     全量、增量、整理都只看库的目录，碰不到这里；
//   - 直链形态 /d/s~{分享码}~{fid}，提取码不写进 STRM（按分享码查 ShareSource），
//     进的是同一个 playbackLinkResolver，Emby 反代、缓存、在途合并照旧（proxyDownloadURLFull 按前缀分派）。
//
// 取直链只能用 POST proapi.115.com/app/share/downurl（m115 加密）。2026-10-31 在用户 NAS 上实测（§2.1–2.3）：
// 主号 Cookie 能直接取别人分享的直链；任何 UA 都能取，返回的 CDN 直链不绑 UA；
// 网页版 webapi/share/downurl 回 50029「当前版本过低」；4100008 = 访问码错误，4100010 = 分享已取消，
// 4100018 = 链接已过期；fid 不在分享里时 state=true 但 data=[]，拿目录 id 取时 url=false。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"

	"gorm.io/gorm"
)

const (
	jobKindShareStrm = "sharestrm"
	// sharePlayPrefix 直链标识前缀：/d/s~{分享码}~{fid}
	sharePlayPrefix = "s~"
)

var plogShareStrm = pluginLog("sharestrm", "分享STRM")

// ---- 配置（setting sharestrm）----

type shareStrmCfg struct {
	// Enabled 总开关，默认关。关着只是不再新增、不再检查；已经生成的 STRM 照常能播（关开关不等于删除）
	Enabled bool `json:"enabled"`
	// DefaultAction 贴 115 分享链接时默认怎么处理：ask 每次问 / transfer 转存 / strm 生成 STRM。开关关着一律按转存
	DefaultAction string `json:"default_action"`
	// Root 分享 STRM 根：媒体根下的相对路径，不能和任何媒体库的本地目录重叠
	Root string `json:"root"`
	// MinSizeMB 比这小的视频不建 STRM（挡样片 / 广告）
	MinSizeMB int `json:"min_size_mb"`
	// SkipOwned 自己库里已经有同一份（sha1）的不建
	SkipOwned bool `json:"skip_owned"`
	// Assets 字幕 / NFO / 标准命名图片从分享下载到本地（Emby 要在 STRM 旁边找它们）
	Assets bool `json:"assets"`
}

const (
	shareStrmDefaultRoot = "分享"
	// shareAssetMaxBytes 附属文件单个上限：字幕、NFO、海报都远小于它，超了多半是认错了类型
	shareAssetMaxBytes = 10 << 20
	// shareAssetMaxCount 一个分享最多下载多少个附属文件
	shareAssetMaxCount = 200
	// shareWalkMaxDirs 一个分享最多列多少个目录：超了报「分享太大」，不按列到的那一半生成
	shareWalkMaxDirs = 300
)

func defaultShareStrmCfg() shareStrmCfg {
	return shareStrmCfg{DefaultAction: "ask", Root: shareStrmDefaultRoot, MinSizeMB: 50, SkipOwned: true, Assets: true}
}

func loadShareStrmCfg() shareStrmCfg {
	cfg := defaultShareStrmCfg()
	if v := settingValueCompat("sharestrm"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	return cfg.normalized()
}

func (c shareStrmCfg) normalized() shareStrmCfg {
	switch c.DefaultAction {
	case "ask", "transfer", "strm":
	default:
		c.DefaultAction = "ask"
	}
	c.Root = normLibLocal(c.Root)
	if c.Root == "" {
		c.Root = shareStrmDefaultRoot
	}
	if c.MinSizeMB < 0 {
		c.MinSizeMB = 0
	}
	return c
}

// linkAction 贴 115 分享链接时这次怎么处理（总开关关着一律转存）
func (c shareStrmCfg) linkAction() string {
	if !c.Enabled {
		return "transfer"
	}
	return c.DefaultAction
}

// shareRootProblem 分享 STRM 根能不能用：要是媒体根下的相对路径、不能和媒体库的本地目录互相包含。
// 重叠的话全量 / 整理会把这里的 STRM 当成库里的东西，深删也会按台账去碰它
func shareRootProblem(root string, libs []model.Library) string {
	if strings.Contains("/"+root+"/", "/../") || strings.HasPrefix(root, "/") || root == "" || root == "." {
		return "分享 STRM 目录要写成媒体根下的相对路径"
	}
	for _, l := range libs {
		local := normLibLocal(l.Local)
		if local == "" {
			continue
		}
		if local == root || strings.HasPrefix(root, local+"/") || strings.HasPrefix(local, root+"/") {
			return fmt.Sprintf("分享 STRM 目录「%s」和媒体库「%s」的本地目录「%s」重叠，换一个目录", root, l.Name, local)
		}
	}
	return ""
}

// shareRootAbs 分享 STRM 根的绝对路径
func shareRootAbs(cfg shareStrmCfg) string {
	return filepath.Join(mediaRoot(), filepath.FromSlash(cfg.Root))
}

// ---- 直链标识 ----

// reShareCodeID 分享码：115 的分享码是字母数字（链接里可能带 - _）
var reShareCodeID = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)

func sharePlayID(code, fid string) string { return sharePlayPrefix + code + "~" + fid }

func isSharePlayID(id string) bool { return strings.HasPrefix(id, sharePlayPrefix) }

// parseSharePlayID 拆 s~{分享码}~{fid}。fid 后面可能跟着「保留文件后缀」带上的 .mkv
func parseSharePlayID(id string) (code, fid string, ok bool) {
	if !isSharePlayID(id) {
		return "", "", false
	}
	code, fid, found := strings.Cut(id[len(sharePlayPrefix):], "~")
	if i := strings.IndexByte(fid, '.'); i >= 0 {
		fid = fid[:i]
	}
	if !found || !reShareCodeID.MatchString(code) || fid == "" || !isAllDigits(fid) {
		return "", "", false
	}
	return code, fid, true
}

// shareStrmURL STRM 内容：拼法跟着 Strm 管理里的直链格式（pick_code / 带文件名）与「保留文件后缀」
func shareStrmURL(domain, format string, keepExt bool, code, fid, name string) string {
	base := strings.TrimRight(domain, "/")
	id := sharePlayID(code, fid)
	if keepExt || isISOName(name) {
		id += pathExt(name)
	}
	if format == "pick_code" {
		return base + "/d/" + id
	}
	return base + "/d/" + id + "?/" + strings.ReplaceAll(name, "#", "%23")
}

// ---- 取直链 ----

var (
	errShareBadCode   = errors.New("访问码错误，到「分享 STRM」页改提取码")
	errShareFileGone  = errors.New("这个文件已经不在分享里了")
	errShareNotListed = errors.New("这个分享没有登记（可能已在「分享 STRM」页删除）")
)

// shareDownInfo downurl 解密后的内容（实测：{"fid","fn","fs","sha1","url":{"url":...}}，fs 是字符串）
type shareDownInfo struct {
	URL  string
	Name string
	Size int64
	Sha1 string
}

// shareDownurlFn 取直链（测试换成假的）
var shareDownurlFn = shareDownurl

// shareDownurl POST proapi /app/share/downurl。照 115driver DownloadByShareCodeWithUA（只读）：
// 载荷 m115 加密，返回的 data 同样加密。UA 用 Cookie 会话那一个（实测任何 UA 都能取）
func shareDownurl(cookie, code, receive, fid string) (shareDownInfo, error) {
	payload, _ := json.Marshal(map[string]string{"share_code": code, "receive_code": receive, "file_id": fid})
	form := url.Values{"data": {encrypt115(payload)}}
	api := "https://proapi.115.com/app/share/downurl"
	throttle115(api)
	defer throttle115Done(api)
	req, _ := http.NewRequest(http.MethodPost, api+"?t="+strconv.FormatInt(time.Now().Unix(), 10), strings.NewReader(form.Encode()))
	req.Header.Set("User-Agent", ua115Unified())
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	resp, err := client115(dlFirstTryTimeout * 3).Do(req)
	if err != nil {
		return shareDownInfo{}, fmt.Errorf("请求 115 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return shareDownInfo{}, fmt.Errorf("115 返回 HTTP %d", resp.StatusCode)
	}
	return parseShareDownurl(body)
}

// parseShareDownurl 解析 downurl 的返回（纯函数，测试直接喂实测的形态）
func parseShareDownurl(body []byte) (shareDownInfo, error) {
	var env struct {
		State json.RawMessage `json:"state"`
		Errno json.Number     `json:"errno"`
		Error string          `json:"error"`
		Msg   string          `json:"msg"`
		Data  json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		return shareDownInfo{}, fmt.Errorf("115 返回认不出：%s", truncateStr(string(body), 120))
	}
	if !openStateOK(env.State) {
		msg := firstNonEmpty(env.Error, env.Msg, "未知错误")
		switch env.Errno.String() {
		case "4100008":
			return shareDownInfo{}, errShareBadCode
		case "4100010":
			return shareDownInfo{}, &shareGoneErr{state: "4", reason: msg}
		case "4100018":
			return shareDownInfo{}, &shareGoneErr{state: "7", reason: msg}
		}
		return shareDownInfo{}, fmt.Errorf("115 拒绝：%s（errno %s）", msg, env.Errno.String())
	}
	var enc string
	if json.Unmarshal(env.Data, &enc) != nil || enc == "" {
		// 不存在的 fid：state=true、data=[]
		return shareDownInfo{}, errShareFileGone
	}
	plain, err := decrypt115(enc)
	if err != nil {
		return shareDownInfo{}, fmt.Errorf("解密直链失败: %w", err)
	}
	var d struct {
		FN   string          `json:"fn"`
		FS   any             `json:"fs"`
		Sha1 string          `json:"sha1"`
		URL  json.RawMessage `json:"url"`
	}
	dec = json.NewDecoder(strings.NewReader(string(plain)))
	dec.UseNumber()
	if dec.Decode(&d) != nil {
		if strings.TrimSpace(string(plain)) == "[]" {
			return shareDownInfo{}, errShareFileGone
		}
		return shareDownInfo{}, fmt.Errorf("直链内容认不出：%s", truncateStr(string(plain), 120))
	}
	// 拿目录 id 取：url=false
	u := openParseDownloadURL(d.URL)
	if u == "" {
		return shareDownInfo{}, errShareFileGone
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(d.FS)), 10, 64)
	return shareDownInfo{URL: u, Name: d.FN, Size: size, Sha1: strings.ToUpper(d.Sha1)}, nil
}

// shareCookieOf 取这个分享直链用的 Cookie（302 不挂 Handler，直接读配置与库）
func shareCookieOf(db *gorm.DB, cfg *config.Config, acct uint) (string, error) {
	if acct == acctMain {
		if ck := cfgCookie(db, cfg); ck != "" {
			return ck, nil
		}
		return "", fmt.Errorf("115 账号未绑定")
	}
	return acctCookieByCfg(db, cfg, acct)
}

// shareDownloadURLFor 播放：s~{分享码}~{fid} → CDN 直链。总开关关着也照常取（关开关不是删除）
func shareDownloadURLFor(db *gorm.DB, cfg *config.Config, id string) (string, map[string]string, error) {
	code, fid, ok := parseSharePlayID(id)
	if !ok || db == nil {
		return "", nil, fmt.Errorf("播放标识无效")
	}
	var src model.ShareSource
	if db.Where("share_code = ?", code).First(&src).Error != nil {
		return "", nil, errShareNotListed
	}
	cookie, err := shareCookieOf(db, cfg, src.Account)
	if err != nil {
		return "", nil, err
	}
	info, err := shareDownurlFn(cookie, code, src.ReceiveCode, fid)
	if err != nil {
		plogShareStrm.Printf("✗ 取直链失败 %s fid=%s：%v", code, fid, err)
		// 访问码错是确定的：记在分享上，页面里提示改提取码。分享取消 / 过期只记原因，
		// 播放时报一次错不算确凿证据（设计 §7.2），状态等「立即检查」或追更来改
		upd := map[string]any{"last_error": truncateStr(err.Error(), 480)}
		if errors.Is(err, errShareBadCode) {
			upd["state"] = shareStateBadCode
		}
		db.Model(&model.ShareSource{}).Where("id = ?", src.ID).Updates(upd)
		return "", nil, err
	}
	// 分享者换过文件（同 fid 内容变了）只记日志：STRM 照播新内容，等「立即检查」更新记录
	var sf model.ShareFile
	if db.Where("source_id = ? AND file_id = ?", src.ID, fid).First(&sf).Error == nil &&
		sf.Sha1 != "" && info.Sha1 != "" && !strings.EqualFold(sf.Sha1, info.Sha1) {
		plogShareStrm.Printf("○ %s fid=%s 的内容和生成时不一样（sha1 变了），建议在「分享 STRM」页立即检查一次", code, fid)
	}
	return info.URL, nil, nil
}

// ---- 状态 ----

const (
	shareStateOK      = "ok"
	shareStatePending = "pending" // 刚添加，还没跑完第一次生成
	shareStateGone    = "gone"    // 分享已取消 / 过期 / 被屏蔽
	shareStateBadCode = "badcode" // 提取码错误
	shareStateError   = "error"   // 列不出来等别的原因
)

// shareStateOfErr 列分享 / 取链出错时分享该记成什么状态
func shareStateOfErr(err error) string {
	switch {
	case errors.Is(err, errShareGone):
		return shareStateGone
	case errors.Is(err, errShareBadCode):
		return shareStateBadCode
	case err != nil && strings.Contains(err.Error(), "访问码错误"):
		return shareStateBadCode
	}
	return shareStateError
}

// shareLocalDirName 分享在本地的目录名：取分享标题（单文件分享的标题是文件名，去掉视频扩展名）
func shareLocalDirName(title, code string) string {
	name := sanitizeName(strmStemOf(strings.TrimSpace(title)))
	name = strings.Trim(name, ". ")
	if name == "" {
		name = code
	}
	return truncateRunes(name, 120)
}

// truncateRunes 按字符截断（本地目录名不宜过长）
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// shareRelUnder 把本地相对路径限制在分享根下（防 ../），返回干净的 / 分隔路径
func shareRelUnder(parts ...string) (string, bool) {
	rel := path.Clean(path.Join(parts...))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	return rel, true
}
