package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 版本号与更新检测。
//
// 只做到「告诉用户有新版本」：不拉镜像、不重启容器、不挂 Docker socket。
// 应用内一键更新那条链路（selfupdate.go）已于上线前整条删除，理由见 AGENTS.md §6.7，
// 用户照提示自己跑 docker compose pull && docker compose up -d。
//
// 版本号的来源是 git tag：CI 用 `git describe --tags` 算出来注入（main.Version），
// 正式版按日期写 `v26.10.9`，同一天再发 `v26.10.9-2`（也是正式版），两个版本之间的 master 构建是
// `v26.10.9-3-gabc1234`（比 v26.10.9 新 3 个提交）。
// 新版本以 GitHub Releases 的 latest 为准（打 v* tag 时 CI 自动建 Release，预发布不算）。

// updateRepo 检测更新看的仓库
const updateRepo = "pancras-loe/115-station"

const (
	updateSettingKey  = "update"          // {check, notify}
	updateNotifiedKey = "update_notified" // 推送过通知的最新版本，同一个版本只推一次
	updateInterval    = 12 * time.Hour
	updateManualGap   = time.Minute // 手动「检查更新」的最小间隔：GitHub 匿名接口每小时只给 60 次
)

var (
	buildVersion = "dev"
	buildSHA     = ""
)

// SetVersion 注入构建版本号（tag 描述）与提交号
func SetVersion(version, sha string) {
	if version != "" {
		buildVersion = version
	}
	buildSHA = sha
}

// updateCfg 更新检测配置。默认都开：检测是每 12 小时一次 GitHub 请求，通知同一版本只推一次
type updateCfg struct {
	Check  bool `json:"check"`
	Notify bool `json:"notify"`
}

func loadUpdateCfg() updateCfg {
	cfg := updateCfg{Check: true, Notify: true}
	if raw := settingValueCompat(updateSettingKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	return cfg
}

// UpdateStatus 更新检测结果（GET /system/update）
type UpdateStatus struct {
	Current     string `json:"current"`
	SHA         string `json:"sha,omitempty"`
	Latest      string `json:"latest,omitempty"`
	HasUpdate   bool   `json:"has_update"`
	Comparable  bool   `json:"comparable"` // 当前版本认得出版本号；dev / 裸提交号的构建比不了
	Notes       string `json:"notes,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	CheckedAt   string `json:"checked_at,omitempty"`
	Error       string `json:"error,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

var (
	updateMu      sync.Mutex
	updateLatest  *githubRelease
	updateChecked time.Time
	updateErr     string
	// 测试替换：默认去 GitHub 拉
	fetchLatestRelease = fetchGithubLatestRelease
)

// fetchGithubLatestRelease 读 GitHub 的 latest release（不含预发布与草稿）。
// 走全局代理：国内直连 api.github.com 经常超时，和 TMDB 同一个处境
func fetchGithubLatestRelease() (*githubRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "115-station/"+buildVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 GitHub：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// 仓库还没发过正式版
		return nil, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub 限流（HTTP %d），稍后再试", resp.StatusCode)
	default:
		return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败：%v", err)
	}
	return &rel, nil
}

// checkUpdate 拉一次最新版本并更新缓存
func checkUpdate() {
	rel, err := fetchLatestRelease()
	updateMu.Lock()
	updateChecked = time.Now()
	if err != nil {
		updateErr = err.Error()
	} else {
		updateErr = ""
		updateLatest = rel
	}
	updateMu.Unlock()
	if err != nil {
		log.Printf("[更新] ✗ 检查失败：%v", err)
	}
}

// currentUpdateStatus 用缓存拼出当前状态（不发请求）
func currentUpdateStatus() UpdateStatus {
	updateMu.Lock()
	defer updateMu.Unlock()
	st := UpdateStatus{
		Current: buildVersion,
		SHA:     buildSHA,
		Error:   updateErr,
		Enabled: loadUpdateCfg().Check,
	}
	_, st.Comparable = parseVersion(buildVersion)
	if !updateChecked.IsZero() {
		st.CheckedAt = updateChecked.Format(time.RFC3339)
	}
	if rel := updateLatest; rel != nil {
		st.Latest = rel.TagName
		st.Notes = rel.Body
		st.URL = rel.HTMLURL
		st.PublishedAt = rel.PublishedAt
		st.HasUpdate = versionNewer(rel.TagName, buildVersion)
	}
	return st
}

// notifyUpdate 有新版本时推一次通知；同一个版本只推一次（重启也不重推）
func notifyUpdate(st UpdateStatus) {
	if !st.HasUpdate || !loadUpdateCfg().Notify || notifyConfigSource == nil {
		return
	}
	if notifyConfigSource.GetSetting(updateNotifiedKey) == st.Latest {
		return
	}
	// 先记账再推：推送失败也不重试，免得通道坏着的时候每 12 小时刷一次错误日志
	if err := notifyConfigSource.SaveSetting(updateNotifiedKey, st.Latest); err != nil {
		log.Printf("[更新] ✗ 记录已通知版本失败：%v", err)
		return
	}
	content := fmt.Sprintf("当前 %s → 最新 %s\n更新：docker compose pull && docker compose up -d", st.Current, st.Latest)
	if st.URL != "" {
		content += "\n更新说明：" + st.URL
	}
	NotifyMessage("🆕 115-Station 有新版本", content)
}

// StartUpdateChecker 后台定时检查更新：启动后 1 分钟一次，之后每 12 小时一次。
// 关着「检查更新」时照样醒，只是不发请求 —— 用户在界面上打开后不用重启就生效
func StartUpdateChecker() {
	go func() {
		time.Sleep(time.Minute)
		for {
			if loadUpdateCfg().Check {
				checkUpdate()
				st := currentUpdateStatus()
				if st.HasUpdate {
					log.Printf("[更新] ○ 有新版本 %s（当前 %s）", st.Latest, st.Current)
				}
				notifyUpdate(st)
			}
			time.Sleep(updateInterval)
		}
	}()
}

// GetUpdateStatus GET /system/update：返回缓存的检测结果，不发请求
func (h *Handler) GetUpdateStatus(c *gin.Context) {
	c.JSON(http.StatusOK, currentUpdateStatus())
}

// CheckUpdateNow POST /system/update/check：立刻查一次（一分钟内重复点直接回缓存）。
// 手动检查不看「检查更新」开关：用户点了就是想知道
func (h *Handler) CheckUpdateNow(c *gin.Context) {
	updateMu.Lock()
	fresh := !updateChecked.IsZero() && time.Since(updateChecked) < updateManualGap && updateErr == ""
	updateMu.Unlock()
	if !fresh {
		checkUpdate()
	}
	c.JSON(http.StatusOK, currentUpdateStatus())
}

// ==================== 版本号比较 ====================

type semVer struct {
	major, minor, patch int
	pre                 string // 预发布标记（-rc1 之类），有它的比同号正式版旧
	seq                 int    // 同日序号：v26.10.9-4 的 4，当天第一版 v26.10.9 是 0
	ahead               int    // git describe 的「比 tag 新几个提交」
}

// describeRe 匹配 git describe 的尾巴：-3-gabc1234（可能还跟着 -dirty）
var describeRe = regexp.MustCompile(`-(\d+)-g[0-9a-f]+(-dirty)?$`)

// parseVersion 认 v1.2.3 / 1.2.3 / v1.2.3-rc1 / v1.2.3-4-gabc1234；dev 和裸提交号认不出。
//
// 版本号按发版日期写（v26.10.9），同一天再发是 v26.10.9-2、-3……（AGENTS.md §4）。
// 按 semver 的读法 -4 是预发布、比 v26.10.9 旧，装着 -4 的用户会被一直提示「更新」到当天第一版；
// 所以纯数字的 - 后缀认成同日序号（正式版），只有带字母的（-rc1）才是预发布。
// git describe 的尾巴先剥：v26.10.9-4-1-gabc1234 = -4 之后 1 个提交，v26.10.9-3-gabc1234 = 当天第一版之后 3 个提交
func parseVersion(s string) (semVer, bool) {
	var v semVer
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if m := describeRe.FindStringSubmatch(s); m != nil {
		v.ahead, _ = strconv.Atoi(m[1])
		s = s[:len(s)-len(m[0])]
	}
	core := s
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		core, v.pre = s[:i], s[i+1:]
		if n, err := strconv.Atoi(v.pre); err == nil && n > 0 && s[i] == '-' {
			v.seq, v.pre = n, ""
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := [3]*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		*nums[i] = n
	}
	return v, true
}

// versionNewer latest 是否比 current 新。任一方认不出版本号都算「不新」：
// dev 构建不该天天被提示更新，拉不到 Release 也不该误报
func versionNewer(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	return compareVersion(l, c) > 0
}

func compareVersion(a, b semVer) int {
	for _, d := range [3]int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	// 同号：预发布 < 正式版（同日序号版按序号排）< 它之后的开发构建
	if (a.pre != "") != (b.pre != "") {
		if a.pre != "" {
			return -1
		}
		return 1
	}
	if d := a.seq - b.seq; d != 0 {
		return sign(d)
	}
	return sign(a.ahead - b.ahead)
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
