package api

// ==================== PT 站签到：接口 ====================
//
// Cookie / Token 只进不出：列表只回有没有与末 4 位；PUT 不带这两个字段 = 不改。

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ptSiteView 界面上的一个站
type ptSiteView struct {
	ID        uint     `json:"id"`
	Preset    string   `json:"preset"`
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	Handler   string   `json:"handler"`
	Cred      string   `json:"cred"` // cookie / token
	HasCookie bool     `json:"has_cookie"`
	HasToken  bool     `json:"has_token"`
	CookieEnd string   `json:"cookie_end,omitempty"` // 末 4 位，认得出换没换过
	UA        string   `json:"ua"`
	Proxy     bool     `json:"proxy"`
	Mode      string   `json:"mode"`
	Enabled   bool     `json:"enabled"`
	NeedFix   bool     `json:"need_fix"`
	Today     *ptDay   `json:"today,omitempty"` // 今天最后一次结果
	Week      []string `json:"week"`            // 近 7 天每天的结果（旧 → 新，最后是今天），没跑是 ""
}

// ptDay 某天某站最后一次结果
type ptDay struct {
	Result  string `json:"result"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
	At      string `json:"at"` // HH:MM
}

func ptTail(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 4 {
		return ""
	}
	return string(r[len(r)-4:])
}

func ptViewOf(s *model.PtSite) ptSiteView {
	cred := "cookie"
	if p := ptPresetOf(s.Preset); p != nil {
		cred = p.Cred
	}
	return ptSiteView{ID: s.ID, Preset: s.Preset, Name: s.Name, BaseURL: s.BaseURL, Handler: ptHandlerOf(s), Cred: cred,
		HasCookie: strings.TrimSpace(s.Cookie) != "", HasToken: strings.TrimSpace(s.Token) != "", CookieEnd: ptTail(s.Cookie),
		UA: s.UA, Proxy: s.Proxy, Mode: s.Mode, Enabled: s.Enabled, NeedFix: s.NeedFix, Week: []string{}}
}

// ptDays 从 now 往前 n 天（旧 → 新）
func ptDays(now time.Time, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = now.AddDate(0, 0, i-n+1).Format("2006-01-02")
	}
	return out
}

// ptLastByDay 每个站每天最后一次结果
func (h *Handler) ptLastByDay(from string) map[uint]map[string]model.PtSigninLog {
	var logs []model.PtSigninLog
	h.DB.Where("day >= ?", from).Order("at").Find(&logs)
	out := map[uint]map[string]model.PtSigninLog{}
	for _, l := range logs {
		if out[l.SiteID] == nil {
			out[l.SiteID] = map[string]model.PtSigninLog{}
		}
		out[l.SiteID][l.Day] = l
	}
	return out
}

func ptDayOf(l model.PtSigninLog) *ptDay {
	return &ptDay{Result: l.Result, Reason: l.Reason, Message: l.Message, At: l.At.Format("15:04")}
}

// PtSigninPresets GET /ptsignin/presets
func (h *Handler) PtSigninPresets(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": ptPresets})
}

// PtSigninSites GET /ptsignin/sites → 站点 + 今天 + 近 7 天
func (h *Handler) PtSigninSites(c *gin.Context) {
	var sites []model.PtSite
	h.DB.Order("id").Find(&sites)
	now := ptNow()
	days := ptDays(now, 7)
	last := h.ptLastByDay(days[0])
	today := now.Format("2006-01-02")
	out := make([]ptSiteView, 0, len(sites))
	for i := range sites {
		v := ptViewOf(&sites[i])
		byDay := last[sites[i].ID]
		for _, d := range days {
			v.Week = append(v.Week, byDay[d].Result)
		}
		if l, ok := byDay[today]; ok {
			v.Today = ptDayOf(l)
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "running": ptRunning.Load()})
}

// ptSiteReq 新增 / 修改 / 测试共用
type ptSiteReq struct {
	Preset  string  `json:"preset"`
	Name    string  `json:"name"`
	BaseURL string  `json:"base_url"`
	Cookie  *string `json:"cookie"` // nil = 不改
	Token   *string `json:"token"`
	UA      string  `json:"ua"`
	Proxy   bool    `json:"proxy"`
	Mode    string  `json:"mode"`
	Enabled *bool   `json:"enabled"`
}

// apply 把请求填进站点；返回界面能看懂的错误
func (r ptSiteReq) apply(s *model.PtSite) string {
	if r.Preset != "" {
		p := ptPresetOf(r.Preset)
		if p == nil {
			return "认不出这个预置站"
		}
		if p.Unavailable {
			return p.Name + "：" + p.Note
		}
		s.Preset = p.Key
		if strings.TrimSpace(r.Name) == "" {
			r.Name = p.Name
		}
		if strings.TrimSpace(r.BaseURL) == "" {
			r.BaseURL = p.Domains[0]
		}
	}
	base, err := normPtBaseURL(r.BaseURL)
	if err != nil {
		return err.Error()
	}
	if s.Preset == "" {
		if p := ptPresetByHost(base); p != nil && p.Unavailable {
			return p.Name + "：" + p.Note
		}
	}
	s.BaseURL = base
	s.Name = strings.TrimSpace(r.Name)
	if s.Name == "" {
		s.Name = ptHostOf(base)
	}
	if len([]rune(s.Name)) > 32 {
		return "名称太长"
	}
	if r.Cookie != nil {
		ck := normPtCookie(*r.Cookie)
		if ck != "" && !ptCookieOK(ck) {
			return "Cookie 格式不对：应该是 a=1; b=2 这样一串，从请求头的 Cookie 那一行复制"
		}
		if ck != s.Cookie {
			s.NeedFix = false
		}
		s.Cookie = ck
	}
	if r.Token != nil {
		tk := strings.TrimSpace(*r.Token)
		if tk != s.Token {
			s.NeedFix = false
		}
		s.Token = tk
	}
	s.UA = strings.TrimSpace(r.UA)
	s.Proxy = r.Proxy
	s.Mode = ptModeSignin
	if r.Mode == ptModeKeep {
		s.Mode = ptModeKeep
	}
	if r.Enabled != nil {
		s.Enabled = *r.Enabled
	}
	if ptHandlerOf(s) == ptHandRousi {
		if s.Token == "" {
			return "要填 API Token"
		}
	} else if s.Cookie == "" {
		return "要填 Cookie"
	}
	return ""
}

// ptDupSite 同一个站（主机名一样）只能加一次
func (h *Handler) ptDupSite(base string, self uint) bool {
	host := ptHostOf(base)
	var sites []model.PtSite
	h.DB.Find(&sites)
	for _, s := range sites {
		if s.ID != self && ptHostMatch(ptHostOf(s.BaseURL), host) {
			return true
		}
	}
	return false
}

// PtSigninCreate POST /ptsignin/sites
func (h *Handler) PtSigninCreate(c *gin.Context) {
	var req ptSiteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	s := model.PtSite{Enabled: true}
	if req.Enabled == nil {
		t := true
		req.Enabled = &t
	}
	if why := req.apply(&s); why != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": why})
		return
	}
	if h.ptDupSite(s.BaseURL, 0) {
		c.JSON(http.StatusConflict, gin.H{"error": "这个站已经添加过了"})
		return
	}
	if err := h.DB.Create(&s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	logPtSite("添加", &s)
	c.JSON(http.StatusOK, gin.H{"data": ptViewOf(&s)})
}

func logPtSite(what string, s *model.PtSite) {
	// 不打印 Cookie / Token
	mode := "签到"
	if s.Mode == ptModeKeep {
		mode = "只保号"
	}
	plogPtSignin.Printf("○ %s站点 %s（%s，%s）", what, s.Name, ptHostOf(s.BaseURL), mode)
}

func (h *Handler) ptSiteByParam(c *gin.Context) *model.PtSite {
	id, _ := strconv.Atoi(c.Param("id"))
	var s model.PtSite
	if id <= 0 || h.DB.First(&s, id).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "站点不存在"})
		return nil
	}
	return &s
}

// PtSigninUpdate PUT /ptsignin/sites/:id
func (h *Handler) PtSigninUpdate(c *gin.Context) {
	s := h.ptSiteByParam(c)
	if s == nil {
		return
	}
	var req ptSiteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req.Preset = "" // 预置站不许改成别的站
	if why := req.apply(s); why != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": why})
		return
	}
	if h.ptDupSite(s.BaseURL, s.ID) {
		c.JSON(http.StatusConflict, gin.H{"error": "另一个站点已经是这个地址"})
		return
	}
	if err := h.DB.Save(s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	logPtSite("修改", s)
	c.JSON(http.StatusOK, gin.H{"data": ptViewOf(s)})
}

// PtSigninDelete DELETE /ptsignin/sites/:id（连同记录）
func (h *Handler) PtSigninDelete(c *gin.Context) {
	s := h.ptSiteByParam(c)
	if s == nil {
		return
	}
	h.DB.Where("site_id = ?", s.ID).Delete(&model.PtSigninLog{})
	h.DB.Delete(s)
	logPtSite("移除", s)
	c.JSON(http.StatusOK, gin.H{"message": "已移除"})
}

// PtSigninTest POST /ptsignin/test {id?, ...表单}：只访问首页确认登录，不签到、不记录。
// 带 id 时没填的凭据用库里的（修改表单里没动 Cookie 也能测）
func (h *Handler) PtSigninTest(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
		ptSiteReq
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var s model.PtSite
	if req.ID > 0 {
		if h.DB.First(&s, req.ID).Error != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "站点不存在"})
			return
		}
		req.Preset = ""
		if strings.TrimSpace(req.BaseURL) == "" {
			req.BaseURL = s.BaseURL
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = s.Name
		}
		if req.Cookie != nil && strings.TrimSpace(*req.Cookie) == "" {
			req.Cookie = nil
		}
		if req.Token != nil && strings.TrimSpace(*req.Token) == "" {
			req.Token = nil
		}
	}
	if why := req.apply(&s); why != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": why})
		return
	}
	start := time.Now()
	r := ptCheckLogin(newPtClient(&s))
	c.JSON(http.StatusOK, gin.H{"data": r, "ms": time.Since(start).Milliseconds()})
}

// PtSigninRun POST /ptsignin/run {site_id?}：不带 = 全部（后台跑，马上返回）；带 = 只跑这个站，等结果
func (h *Handler) PtSigninRun(c *gin.Context) {
	var req struct {
		SiteID uint `json:"site_id"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.SiteID == 0 {
		if ptRunning.Load() {
			c.JSON(http.StatusConflict, gin.H{"error": "已经在签到了，等这一轮跑完"})
			return
		}
		var n int64
		h.DB.Model(&model.PtSite{}).Where("enabled = ?", true).Count(&n)
		if n == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "还没有启用的站点"})
			return
		}
		h.ptRoundAsync("manual", nil, nil)
		c.JSON(http.StatusAccepted, gin.H{"message": "开始签到，今天已经做完的站跳过；结果见这里与通知"})
		return
	}
	if ptRunning.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "正在跑一轮签到，等它跑完再试"})
		return
	}
	items, err := h.ptRunRound("manual", []uint{req.SiteID})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "站点不存在或暂不支持签到"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": items[0].ptResult})
}

// ptRecordRow 记录页一行
type ptRecordRow struct {
	SiteID uint     `json:"site_id"`
	Name   string   `json:"name"`
	Days   []*ptDay `json:"days"` // 与 days 对齐，没跑是 null
	OK     int      `json:"ok"`
}

// ptLastItem 最近一轮的一条
type ptLastItem struct {
	Name string `json:"name"`
	model.PtSigninLog
}

// PtSigninRecords GET /ptsignin/records → 14 天矩阵 + 最近一轮 + 汇总通知预览
func (h *Handler) PtSigninRecords(c *gin.Context) {
	now := ptNow()
	days := ptDays(now, 14)
	var sites []model.PtSite
	h.DB.Order("id").Find(&sites)
	names := map[uint]string{}
	last := h.ptLastByDay(days[0])
	rows := make([]ptRecordRow, 0, len(sites))
	for _, s := range sites {
		names[s.ID] = s.Name
		row := ptRecordRow{SiteID: s.ID, Name: s.Name, Days: make([]*ptDay, len(days))}
		for i, d := range days {
			if l, ok := last[s.ID][d]; ok {
				row.Days[i] = ptDayOf(l)
				if l.Result == ptResOK {
					row.OK++
				}
			}
		}
		rows = append(rows, row)
	}
	cfg := loadPtSigninCfg()
	lastRound := gin.H{}
	items := []ptLastItem{}
	preview := ""
	if st, err1 := time.Parse(time.RFC3339, cfg.LastStart); err1 == nil {
		en, err2 := time.Parse(time.RFC3339, cfg.LastEnd)
		if err2 != nil {
			en = now
		}
		var logs []model.PtSigninLog
		h.DB.Where("at >= ? AND at <= ?", st, en.Add(time.Second)).Order("at").Find(&logs)
		var round []ptRoundItem
		for _, l := range logs {
			items = append(items, ptLastItem{Name: names[l.SiteID], PtSigninLog: l})
			round = append(round, ptRoundItem{SiteID: l.SiteID, Name: names[l.SiteID],
				ptResult: ptResult{Result: l.Result, Reason: l.Reason, Message: l.Message}, At: l.At})
		}
		order := map[string]int{ptResFail: 0, ptResLogin: 1, ptResOK: 2}
		sort.SliceStable(items, func(i, j int) bool { return order[items[i].Result] < order[items[j].Result] })
		if len(round) > 0 {
			t, b := ptSummary(round, st)
			preview = t + "\n" + b
		}
		lastRound = gin.H{"start": st.Format("2006-01-02 15:04"), "seconds": int(en.Sub(st).Seconds()), "trigger": cfg.LastTrigger}
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "rows": rows, "last": lastRound, "items": items, "notify_preview": preview})
}

// PtSigninGetConfig GET /ptsignin/config
func (h *Handler) PtSigninGetConfig(c *gin.Context) {
	cfg := loadPtSigninCfg()
	now := ptNow()
	next, todayAt := "", ""
	if cfg.Enabled {
		next = ptNextText(cfg, now)
	}
	if cfg.Today == now.Format("2006-01-02") {
		todayAt = cfg.TodayAt
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled": cfg.Enabled, "mode": cfg.Mode, "window_from": cfg.WindowFrom, "window_to": cfg.WindowTo,
		"cron": cfg.Cron, "gap_min": cfg.GapMin, "gap_max": cfg.GapMax, "retry": cfg.Retry, "notify": cfg.Notify,
		"today_at": todayAt,
		"next_run": next, "running": ptRunning.Load(),
	})
}

// ptNextText 下次执行时间的说法
func ptNextText(cfg ptSigninCfg, now time.Time) string {
	if cfg.Mode == ptModeCron {
		if t := nextCronTime(cfg.Cron, now); !t.IsZero() {
			return t.Format("01-02 15:04")
		}
		return ""
	}
	day := now.Format("2006-01-02")
	if cfg.Today == day && cfg.TodayAt != "" && cfg.LastDay != day {
		return "今天 " + cfg.TodayAt
	}
	return "明天 " + cfg.WindowFrom + "–" + cfg.WindowTo + " 随机"
}

// PtSigninSaveConfig POST /ptsignin/config
func (h *Handler) PtSigninSaveConfig(c *gin.Context) {
	var req struct {
		Enabled    bool   `json:"enabled"`
		Mode       string `json:"mode"`
		WindowFrom string `json:"window_from"`
		WindowTo   string `json:"window_to"`
		Cron       string `json:"cron"`
		GapMin     int    `json:"gap_min"`
		GapMax     int    `json:"gap_max"`
		Retry      bool   `json:"retry"`
		Notify     string `json:"notify"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Mode == ptModeCron && !checkin115CronValid(strings.TrimSpace(req.Cron)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 30 8 * * *）"})
		return
	}
	if req.Mode != ptModeCron {
		from, ok1 := ptParseHM(req.WindowFrom)
		to, ok2 := ptParseHM(req.WindowTo)
		if !ok1 || !ok2 || to <= from {
			c.JSON(http.StatusBadRequest, gin.H{"error": "时间窗要写成 08:00 到 10:00 这样，结束要晚于开始"})
			return
		}
	}
	err := updatePtSigninCfg(func(cfg *ptSigninCfg) {
		changed := cfg.Mode != req.Mode || cfg.WindowFrom != req.WindowFrom || cfg.WindowTo != req.WindowTo
		cfg.Enabled, cfg.Mode, cfg.WindowFrom, cfg.WindowTo = req.Enabled, req.Mode, req.WindowFrom, req.WindowTo
		cfg.Cron, cfg.GapMin, cfg.GapMax, cfg.Retry, cfg.Notify = strings.TrimSpace(req.Cron), req.GapMin, req.GapMax, req.Retry, req.Notify
		if changed {
			cfg.Today, cfg.TodayAt = "", "" // 时间窗改了：今天的时刻按新窗口重挑（已经跑过的 LastDay 照旧挡着）
		}
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	cfg := loadPtSigninCfg()
	when := "每天 " + cfg.WindowFrom + "–" + cfg.WindowTo + " 随机"
	if cfg.Mode == ptModeCron {
		when = "cron " + cfg.Cron
	}
	plogPtSignin.Printf("○ 保存配置：开启 %v，%s", cfg.Enabled, when)
	c.JSON(http.StatusOK, gin.H{"message": "保存成功"})
}

// ==================== 机器人 ====================

// botPtSignin 机器人「PT签到」：后台跑一轮全部，跑完把汇总回给发指令的人
func (h *Handler) botPtSignin(reply func(...string)) {
	if ptRunning.Load() {
		reply("PT 签到已经在跑了，跑完会推汇总。")
		return
	}
	var n int64
	h.DB.Model(&model.PtSite{}).Where("enabled = ?", true).Count(&n)
	if n == 0 {
		reply("还没有启用的 PT 站点，先在网页「扩展功能 → PT 站签到」里添加。")
		return
	}
	reply("开始签到，今天已经做完的站跳过，跑完回复汇总。")
	h.ptRoundAsync("bot", nil, reply)
}
