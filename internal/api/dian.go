package api

// ==================== 癫影：配置、资源列表、解锁、每日签到 ====================
//
// 浏览器那一层见 dianweb.go。这里是业务：
//   - 资源列表：GET /resource-detail?key=<dianResourceKey>，一次给出这部片（剧集是全部季）的所有分享，
//     每条带解锁积分、季集（seasons_csv / episodes）、文件清单（file_list）、画质标签（tag_decoded）。
//     不登录也能读；登录了才知道哪些已经解锁过（解锁过的直接带着链接，再解锁不扣分）。
//   - 解锁：POST /unlock {share_id, resource_id} → payload 里是 115 分享（url_115 或 share_code + receive_code）
//     或离线链接（urls，可能一次好几条 ed2k / 磁力）。
//   - 签到：POST /signin {mode: "normal"}。「运气」模式可能倒扣积分，不开放（同 RE0 不开赌狗签到）。
//
// 文件清单是不花积分就能看的：订阅付费解锁前按它核对缺集（同 RE0 OpenAPI 的文件预览），见 subrun.go。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// dianCfg setting key "dian"
type dianCfg struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func dianConfigured(cfg *dianCfg) bool {
	return strings.TrimSpace(cfg.Email) != "" && cfg.Password != ""
}

func loadDianCfg() *dianCfg {
	cfg := &dianCfg{}
	if v := settingValueCompat("dian"); v != "" {
		json.Unmarshal([]byte(v), cfg)
	}
	return cfg
}

func saveDianCfg(cfg *dianCfg) error {
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	b, _ := json.Marshal(cfg)
	return notifyConfigSource.SaveSetting("dian", string(b))
}

// dianReadyReason 能不能用。列资源本身不用登录，但没填账号也解锁不了；更要紧的是不填账号就不碰浏览器：
// 来源清单每次打开设置页都会问一遍，没配置的用户不该因此被下载 210MB 的浏览器
func dianReadyReason() string {
	if !dianConfigured(loadDianCfg()) {
		return "未配置癫影（在设置里填账号密码）"
	}
	return re0BrowserBlocked("找资源时发现癫影要用的浏览器没装")
}

// ==================== 资源列表 ====================

// dianResourceKey 站点前端的 St()：把 "1|tmdb|类型|编号|季" 与固定 4 字节异或后 base64url。
// 季传 0 = 整部（剧集的所有季都在里面，传具体季号返回的也是全部）
func dianResourceKey(mediaType string, tmdbID int) string {
	s := []byte(fmt.Sprintf("1|tmdb|%s|%d|0", mediaType, tmdbID))
	mask := [4]byte{55, 161, 92, 233}
	for i := range s {
		s[i] ^= mask[i%4]
	}
	return base64.RawURLEncoding.EncodeToString(s)
}

// dianShare 一条分享（resource-detail 的 shares[]）。解锁过的才带链接字段
type dianShare struct {
	ID             int            `json:"id"`
	ResourceID     int            `json:"resource_id"`
	ShareKind      string         `json:"share_kind"`   // 115 / offline
	OfflineType    string         `json:"offline_type"` // magnet / ed2k …（share_kind=offline 时）
	Status         string         `json:"status"`
	MediaType      string         `json:"media_type"`
	Season         int            `json:"season"`
	SeasonsCSV     string         `json:"seasons_csv"`
	EpisodeCount   int            `json:"episode_count"`
	Episodes       string         `json:"episodes"` // "1,2,3"
	TitleOverride  string         `json:"title_override"`
	OfflineTitle   string         `json:"offline_title"`
	FileName       string         `json:"file_name"`
	FileExtension  string         `json:"file_extension"`
	FileList       []string       `json:"file_list"`
	HasChineseSub  bool           `json:"has_chinese_subtitle"`
	TagDecoded     map[string]any `json:"tag_decoded"`
	TotalSizeBytes int64          `json:"total_size_bytes"`
	TotalSizeHuman string         `json:"total_size_human"`
	UnlockCost     *int           `json:"unlock_cost"`
	IsUnlocked     bool           `json:"is_unlocked"`
	CreatedAt      string         `json:"created_at"`
	ResourceTitle  string         `json:"resource_title"`
	// 解锁过的才有
	URL         string   `json:"url"`
	URLs        []string `json:"urls"`
	URL115      string   `json:"url_115"`
	ShareCode   string   `json:"share_code"`
	ReceiveCode string   `json:"receive_code"`
}

// offline 离线类（磁力 / ed2k），解锁出来的不是 115 分享
func (s dianShare) offline() bool {
	return s.ShareKind == "offline" || s.ShareKind == "magnet"
}

// owned 已经解锁过（列表里直接带着链接，或者站点标了已解锁）
func (s dianShare) owned() bool {
	return s.IsUnlocked || s.URL115 != "" || s.ShareCode != "" || s.URL != "" || len(s.URLs) > 0
}

// title 展示 / 解析画质用的标题：分享者写的标题，后面补上站点解析出来的画质与季集（画质标签按标题解析）
func (s dianShare) title() string {
	name := firstNonEmpty(strings.TrimSpace(s.TitleOverride), strings.TrimSpace(s.OfflineTitle), strings.TrimSpace(s.FileName), s.ResourceTitle)
	var extra []string
	low := strings.ToLower(name)
	for _, k := range []string{"resolution", "source", "video_codec", "hdr", "audio_codec"} {
		v, _ := s.TagDecoded[k].(string)
		if v = strings.TrimSpace(v); v != "" && !strings.Contains(low, strings.ToLower(v)) {
			extra = append(extra, v)
		}
	}
	// 剧集把站点标的季集放在最前面：覆盖估计（resCoverageOf）取最左边的匹配，分享者常拿第一个文件名当标题，
	// 「绝命毒师 - S01E01 - 4K.WEB-DL…等7个文件」放在后面会被读成 E01-E04（2026-10-15 现场样本）
	if s.MediaType == "tv" {
		if m := s.seasonMark(); m != "" && !strings.Contains(strings.ToUpper(name), m) {
			name = "[" + m + "] " + name
		}
	}
	if len(extra) == 0 {
		return name
	}
	return name + " " + strings.Join(extra, " ")
}

// seasonMark 站点标的季集写成标题里认得出的样子（S05E01-E16 / S01-S03 / S05），给 resCoverageOf 估覆盖用
func (s dianShare) seasonMark() string {
	var seasons []int
	for _, f := range strings.Split(s.SeasonsCSV, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
			seasons = append(seasons, n)
		}
	}
	if len(seasons) == 0 && s.Season > 0 {
		seasons = []int{s.Season}
	}
	switch {
	case len(seasons) == 0:
		return ""
	case len(seasons) > 1:
		return fmt.Sprintf("S%02d-S%02d", seasons[0], seasons[len(seasons)-1])
	}
	var eps []int
	for _, f := range strings.Split(s.Episodes, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
			eps = append(eps, n)
		}
	}
	if len(eps) == 0 {
		return fmt.Sprintf("S%02d", seasons[0])
	}
	lo, hi := eps[0], eps[0]
	for _, e := range eps {
		lo, hi = min(lo, e), max(hi, e)
	}
	// 中间缺集的不写范围：写成 E01-E16 会让订阅以为整段都有
	if hi-lo+1 != len(eps) {
		return fmt.Sprintf("S%02d", seasons[0])
	}
	if lo == hi {
		return fmt.Sprintf("S%02dE%02d", seasons[0], lo)
	}
	return fmt.Sprintf("S%02dE%02d-E%02d", seasons[0], lo, hi)
}

// parseDianDetail resource-detail 的返回 → 有效的分享
func parseDianDetail(raw []byte) ([]dianShare, error) {
	var data struct {
		Resource struct {
			Title     string `json:"title"`
			MediaType string `json:"media_type"`
		} `json:"resource"`
		Shares []dianShare `json:"shares"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("解析癫影资源列表失败: %v", err)
	}
	out := data.Shares[:0]
	for _, s := range data.Shares {
		if s.ID <= 0 || (s.Status != "" && s.Status != "active") {
			continue
		}
		if s.ResourceTitle == "" {
			s.ResourceTitle = data.Resource.Title
		}
		if s.MediaType == "" {
			s.MediaType = data.Resource.MediaType
		}
		out = append(out, s)
	}
	return out, nil
}

// dianListShares 按 TMDB 条目列分享。配了账号就登录后再读（才知道哪些已解锁），没配也能读。
// 缓存在上一层（searchResources 的 resCache），这里每次都真的去问
func dianListShares(mediaType string, tmdbID int) ([]dianShare, error) {
	var shares []dianShare
	login := dianConfigured(loadDianCfg())
	err := dianWeb(fmt.Sprintf("查资源 %s/%d", mediaType, tmdbID), login, 90*time.Second, func(c *dianCaller) error {
		raw, err := c.do("GET", "/resource-detail?key="+dianResourceKey(mediaType, tmdbID), nil)
		if err != nil {
			return err
		}
		shares, err = parseDianDetail(raw)
		return err
	})
	return shares, err
}

// dianForgetLists 解锁 / 换账号后，缓存里的「已解锁」状态过时了：清掉癫影的搜索缓存
func dianForgetLists() {
	resCacheMu.Lock()
	for k := range resCache {
		if strings.HasPrefix(k, "dian|") {
			delete(resCache, k)
		}
	}
	resCacheMu.Unlock()
}

// dianRef 资源引用：分享 id + 所属资源 id（解锁要两个都带，资源 id 用来校验归属）
func dianRef(s dianShare) string { return strconv.Itoa(s.ID) + ":" + strconv.Itoa(s.ResourceID) }

func parseDianRef(ref string) (shareID, resourceID int, ok bool) {
	a, b, _ := strings.Cut(ref, ":")
	shareID, err := strconv.Atoi(a)
	if err != nil || shareID <= 0 {
		return 0, 0, false
	}
	resourceID, _ = strconv.Atoi(b)
	return shareID, resourceID, true
}

// ==================== 解锁 ====================

// dianUnlocked 解锁出来的东西：115 分享（Share + Code）或离线链接（Links，可能好几条）
type dianUnlocked struct {
	Share   string
	Code    string
	Links   []string
	Balance *int // 解锁后的积分余额（站点回 new_balance；不回这次扣了多少）
	Again   bool // 之前解锁过，这次没扣分
}

// parseDianUnlock 解锁返回 → 链接。字段对照站点的 UnlockResultDialog：
// 115 分享取 url_115，没有就用 share_code + receive_code 拼；离线取 urls，没有就把 url 按行拆。
// 2026-10-15 实测返回：{"already":false,"code":"ok","new_balance":353,"payload":{"share_kind":"115","url_115":"https://115.com/s/…?password=…",…}}
func parseDianUnlock(raw []byte) (*dianUnlocked, error) {
	var data struct {
		Already bool      `json:"already"`
		Balance *int      `json:"new_balance"`
		Payload dianShare `json:"payload"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("解析解锁结果失败: %v", err)
	}
	p := data.Payload
	out := &dianUnlocked{Again: data.Already, Balance: data.Balance}
	if !p.offline() {
		out.Share, out.Code = strings.TrimSpace(p.URL115), strings.TrimSpace(p.ReceiveCode)
		if out.Share == "" && is115ShareLink(p.ShareCode) {
			out.Share = strings.TrimSpace(p.ShareCode)
		}
		if out.Share == "" && p.ShareCode != "" {
			out.Share = "https://115cdn.com/s/" + strings.TrimSpace(p.ShareCode)
			if out.Code != "" {
				out.Share += "?password=" + out.Code
			}
		}
		if out.Share == "" && is115ShareLink(p.URL) {
			out.Share = strings.TrimSpace(p.URL)
		}
		if out.Share != "" {
			return out, nil
		}
	}
	links := p.URLs
	if len(links) == 0 {
		links = strings.Split(p.URL, "\n")
	}
	for _, l := range links {
		if l = strings.TrimSpace(l); l != "" {
			out.Links = append(out.Links, l)
		}
	}
	if len(out.Links) == 0 {
		return nil, fmt.Errorf("解锁成功了，但返回里没有链接")
	}
	return out, nil
}

// dianUnlock 解锁一条分享（扣积分；解锁过的再调不扣）。调用方负责积分确认
func dianUnlock(ref string) (*dianUnlocked, error) {
	shareID, resourceID, ok := parseDianRef(ref)
	if !ok {
		return nil, fmt.Errorf("资源引用不对：%s", ref)
	}
	var out *dianUnlocked
	err := dianWeb(fmt.Sprintf("解锁分享 %d", shareID), true, 90*time.Second, func(c *dianCaller) error {
		body := map[string]int{"share_id": shareID}
		if resourceID > 0 {
			body["resource_id"] = resourceID
		}
		raw, err := c.do("POST", "/unlock", body)
		if err != nil {
			return err
		}
		out, err = parseDianUnlock(raw)
		return err
	})
	if err != nil {
		return nil, err
	}
	dianForgetLists()
	what := out.Share
	if what == "" {
		what = fmt.Sprintf("%d 条离线链接", len(out.Links))
	}
	balance := ""
	if out.Balance != nil {
		balance = fmt.Sprintf("，余额 %d 积分", *out.Balance)
	}
	log.Printf("[癫影] ✦ 解锁分享 %d → %s（之前解锁过 %v%s）", shareID, truncateStr(what, 60), out.Again, balance)
	return out, nil
}

// ==================== 每日签到 ====================
//
// 与 RE0 签到同一套调度（re0CheckinDue）：每 10 分钟看一次，08:00 后今天没签成就签，失败一小时后再试。
// 当天已经签过时站点回错误（文案里带「已」），也算这一天完成

var (
	dianCheckinMu    sync.Mutex
	dianCheckinRunMu sync.Mutex
	dianCheckinTried atomic.Int64
)

func loadDianCheckinCfg() re0CheckinCfg {
	dianCheckinMu.Lock()
	defer dianCheckinMu.Unlock()
	var cfg re0CheckinCfg
	if v := settingValueCompat("diancheckin"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	return cfg
}

func updateDianCheckinCfg(fn func(*re0CheckinCfg)) error {
	if notifyConfigSource == nil {
		return fmt.Errorf("配置源未就绪")
	}
	dianCheckinMu.Lock()
	defer dianCheckinMu.Unlock()
	var cfg re0CheckinCfg
	if v := settingValueCompat("diancheckin"); v != "" {
		json.Unmarshal([]byte(v), &cfg)
	}
	fn(&cfg)
	b, _ := json.Marshal(cfg)
	return notifyConfigSource.SaveSetting("diancheckin", string(b))
}

func dianCheckinReady() string {
	if !dianConfigured(loadDianCfg()) {
		return "未配置癫影账号密码"
	}
	return re0BrowserBlocked("癫影签到时发现浏览器没装")
}

// dianCheckinText 签到返回 → 文案。award 正数是得分，0 空签，负数倒扣（只在「运气」模式出现，这里不用）
func dianCheckinText(raw []byte, points int) string {
	var res struct {
		Award int `json:"award"`
	}
	json.Unmarshal(raw, &res)
	msg := "签到完成，本次空签"
	if res.Award > 0 {
		msg = fmt.Sprintf("签到成功，获得 %d 积分", res.Award)
	} else if res.Award < 0 {
		msg = fmt.Sprintf("签到完成，扣了 %d 积分", -res.Award)
	}
	if points >= 0 {
		msg += fmt.Sprintf("，当前积分 %d", points)
	}
	return msg
}

// dianAlreadySigned 当天已签过的报错（站点没公开错误码，按 code 与文案都认）
func dianAlreadySigned(err error) bool {
	var ae *dianAPIErr
	if errors.As(err, &ae) && (strings.Contains(ae.Code, "already") || ae.Code == "conflict") {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "已签") || strings.Contains(s, "已经签") || strings.Contains(strings.ToLower(s), "already")
}

func (h *Handler) runDianCheckin(notify bool) (ok bool, msg string) {
	dianCheckinRunMu.Lock()
	defer dianCheckinRunMu.Unlock()
	now := time.Now()
	defer func() {
		if ok {
			log.Printf("[癫影签到] ✓ %s", msg)
		} else {
			log.Printf("[癫影签到] ✗ %s", msg)
		}
		if err := updateDianCheckinCfg(func(c *re0CheckinCfg) {
			c.LastResult, c.LastResultAt = msg, now.Format("2006-01-02 15:04")
			if ok {
				c.LastDone = now.Format("2006-01-02")
			}
		}); err != nil {
			log.Printf("[癫影签到] ○ 结果落盘失败: %v", err)
		}
		if notify {
			title := "癫影签到失败"
			if ok {
				title = "癫影签到成功"
			}
			NotifyMessage(kindCheckin, title, msg)
		}
	}()
	if why := dianCheckinReady(); why != "" {
		return false, why
	}
	err := dianWeb("签到", true, 90*time.Second, func(c *dianCaller) error {
		raw, err := c.do("POST", "/signin", map[string]string{"mode": "normal"})
		points := -1
		if me, merr := c.me(); merr == nil {
			points = me.Points
		}
		if err != nil {
			if dianAlreadySigned(err) {
				ok, msg = true, "今日已签到"
				if points >= 0 {
					msg += fmt.Sprintf("，当前积分 %d", points)
				}
				return nil
			}
			return err
		}
		ok, msg = true, dianCheckinText(raw, points)
		return nil
	})
	if err != nil {
		return false, "签到失败：" + err.Error()
	}
	return ok, msg
}

// StartDianCheckinScheduler 每 10 分钟看一次该不该签
func StartDianCheckinScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(re0CheckinTick)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			h.dianCheckinTick()
		}
	}()
}

func (h *Handler) dianCheckinTick() {
	now := time.Now()
	var lastFail time.Time
	if t := dianCheckinTried.Load(); t > 0 {
		lastFail = time.Unix(t, 0)
	}
	if !re0CheckinDue(loadDianCheckinCfg(), now, lastFail) {
		return
	}
	if why := dianCheckinReady(); why != "" {
		dianCheckinTried.Store(now.Unix())
		return
	}
	if ok, _ := h.runDianCheckin(true); ok {
		dianCheckinTried.Store(0)
	} else {
		dianCheckinTried.Store(now.Unix())
	}
}

// ==================== HTTP 接口 ====================

// DianGetConfig GET /dian/config
func (h *Handler) DianGetConfig(c *gin.Context) {
	cfg := loadDianCfg()
	c.JSON(http.StatusOK, gin.H{"email": cfg.Email, "password": maskIfSet(cfg.Password), "configured": dianConfigured(cfg)})
}

// DianSaveConfig POST /dian/config {email, password}
func (h *Handler) DianSaveConfig(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg := loadDianCfg()
	oldEmail := cfg.Email
	cfg.Email = strings.TrimSpace(req.Email)
	if req.Password != settingMask {
		cfg.Password = req.Password // 不 trim：空格可能是密码的一部分
	}
	if err := saveDianCfg(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if oldEmail != "" && oldEmail != cfg.Email {
		// 换账号：浏览器和 RE0 共用一个用户数据目录，只能把站点的会话从页面里登出
		go dianLogout()
	}
	dianForgetLists()
	log.Printf("[癫影] ✓ 配置已保存（%s）", firstNonEmpty(re0MaskUser(cfg.Email), "未填账号"))
	msg := "保存成功"
	if dianConfigured(cfg) && !ensureCloak("保存癫影配置") {
		if st := cloakSnapshot(); st.State != "ready" && st.State != "unsupported" {
			msg = "保存成功，正在下载浏览器（约 210MB，只下一次，和 RE0 共用）"
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}

// dianLogout 退出站点的登录（换账号时）；失败无妨，下次 /me 认出的是旧账号会照样用，所以记一行日志
func dianLogout() {
	if err := dianWeb("退出旧账号", false, 60*time.Second, func(c *dianCaller) error {
		_, err := c.do("POST", "/auth/logout", nil)
		return err
	}); err != nil {
		log.Printf("[癫影] ⚠ 换账号时退出旧账号失败: %v", err)
	}
}

// DianCheck GET /dian/check —— 真的登录一次，读账号与积分
func (h *Handler) DianCheck(c *gin.Context) {
	cfg := loadDianCfg()
	if !dianConfigured(cfg) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有填癫影账号密码"})
		return
	}
	var me *dianMe
	err := dianWeb("检查登录", false, 90*time.Second, func(dc *dianCaller) error {
		var err error
		me, err = dc.ensureLogin(cfg)
		return err
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	msg := fmt.Sprintf("登录有效：%s，积分 %d", me.Nickname, me.Points)
	if me.VIP {
		msg += "（VIP）"
	}
	c.JSON(http.StatusOK, gin.H{"message": msg, "user": me.Nickname, "points": me.Points, "vip": me.VIP})
}

// DianCheckinGet GET /dian/checkin
func (h *Handler) DianCheckinGet(c *gin.Context) {
	cfg := loadDianCheckinCfg()
	c.JSON(http.StatusOK, gin.H{
		"enabled":        cfg.Enabled,
		"last_done":      cfg.LastDone,
		"last_result":    cfg.LastResult,
		"last_result_at": cfg.LastResultAt,
		"done_today":     cfg.LastDone == time.Now().Format("2006-01-02"),
		"hour":           re0CheckinHour,
		"blocked":        dianCheckinReady(),
	})
}

// DianCheckinSave POST /dian/checkin {enabled}
func (h *Handler) DianCheckinSave(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if err := updateDianCheckinCfg(func(cfg *re0CheckinCfg) { cfg.Enabled = req.Enabled }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	dianCheckinTried.Store(0)
	log.Printf("[配置] 癫影每日签到：%v", req.Enabled)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// DianCheckinRun POST /dian/checkin/run
func (h *Handler) DianCheckinRun(c *gin.Context) {
	ok, msg := h.runDianCheckin(false)
	if !ok {
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": msg})
}
