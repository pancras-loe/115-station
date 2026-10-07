package api

// 订阅的 HTTP 接口。所有写操作只改订阅表本身，搜索与提交一律入订阅队列执行。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// subForm 新建 / 修改订阅的表单（修改时只认带了的字段之外的整份，前端整存整取）
type subForm struct {
	TmdbID    int      `json:"tmdb_id"`
	MediaType string   `json:"media_type"`
	Scope     string   `json:"scope"`
	Season    int      `json:"season"`
	EpStart   int      `json:"ep_start"`
	EpEnd     int      `json:"ep_end"`
	Specials  bool     `json:"specials"`
	Follow    string   `json:"follow"`
	Sources   []string `json:"sources"`
	RankLimit int      `json:"rank_limit"`
	Include   string   `json:"include"`
	Exclude   string   `json:"exclude"`
}

// applySubForm 校验表单并写进订阅；范围变了返回 true（要立刻重新检查）
func applySubForm(sub *model.Subscription, f subForm, now time.Time) (bool, error) {
	before := fmt.Sprint(sub.Scope, sub.Season, sub.EpStart, sub.EpEnd, sub.Specials, sub.Follow)
	if sub.MediaType == "movie" {
		sub.Scope, sub.Season, sub.EpStart, sub.EpEnd, sub.Specials, sub.Follow = "", 0, 0, 0, false, ""
	} else {
		switch f.Scope {
		case "", subScopeAll:
			sub.Scope, sub.Season, sub.EpStart, sub.EpEnd = subScopeAll, 0, 0, 0
		case subScopeSeason:
			if f.Season < 0 {
				return false, errors.New("季号不对")
			}
			sub.Scope, sub.Season, sub.EpStart, sub.EpEnd = subScopeSeason, f.Season, 0, 0
		case subScopeRange:
			if f.Season < 0 || f.EpStart <= 0 || (f.EpEnd > 0 && f.EpEnd < f.EpStart) {
				return false, errors.New("集段不对：起始集号要 ≥ 1，结束集号为空或不小于起始")
			}
			sub.Scope, sub.Season, sub.EpStart, sub.EpEnd = subScopeRange, f.Season, f.EpStart, f.EpEnd
		default:
			return false, errors.New("范围只能是 全剧 / 某一季 / 集段")
		}
		sub.Specials = f.Specials && sub.Scope == subScopeAll
		switch f.Follow {
		case "", subFollowMissing:
			sub.Follow, sub.FollowFrom = subFollowMissing, nil
		case subFollowNew:
			if sub.Follow != subFollowNew || sub.FollowFrom == nil {
				t := now
				sub.FollowFrom = &t
			}
			sub.Follow = subFollowNew
		default:
			return false, errors.New("追剧模式只能是 补缺集 / 只追新集")
		}
	}
	srcs := make([]string, 0, len(f.Sources))
	for _, k := range f.Sources {
		if resSourceOf(k) != nil {
			srcs = append(srcs, k)
		}
	}
	sub.Sources = ""
	if len(srcs) > 0 {
		b, _ := json.Marshal(srcs)
		sub.Sources = string(b)
	}
	if f.RankLimit < 0 {
		f.RankLimit = 0
	}
	sub.RankLimit = f.RankLimit
	sub.Include = truncateStr(strings.TrimSpace(f.Include), 250)
	sub.Exclude = truncateStr(strings.TrimSpace(f.Exclude), 250)
	return before != fmt.Sprint(sub.Scope, sub.Season, sub.EpStart, sub.EpEnd, sub.Specials, sub.Follow), nil
}

// subDTO 列表与详情里的订阅
type subDTO struct {
	model.Subscription
	Sources  []string `json:"sources"`
	Inflight int      `json:"inflight"` // 在路上的尝试数
	Running  bool     `json:"running"`  // 正在排队 / 检查
}

func toSubDTO(s model.Subscription, inflight map[uint]int, running map[uint]bool) subDTO {
	d := subDTO{Subscription: s, Inflight: inflight[s.ID], Running: running[s.ID]}
	_ = json.Unmarshal([]byte(s.Sources), &d.Sources)
	if d.Sources == nil {
		d.Sources = []string{}
	}
	return d
}

// subInflightCounts / subRunningSet 列表上的两个小标记
func subInflightCounts(db *gorm.DB) map[uint]int {
	out := map[uint]int{}
	var rows []struct {
		SubID uint
		N     int
	}
	db.Model(&model.SubAttempt{}).Select("sub_id, COUNT(*) AS n").Where("status = ?", subAttemptInflight).Group("sub_id").Scan(&rows)
	for _, r := range rows {
		out[r.SubID] = r.N
	}
	return out
}

func subRunningSet(db *gorm.DB) map[uint]bool {
	out := map[uint]bool{}
	var jobs []model.TaskJob
	db.Where("kind = ? AND status IN ?", jobKindSubscribe, []string{jobQueued, jobRunning}).Find(&jobs)
	for i := range jobs {
		if p := decodeJobParams(&jobs[i]); p.Subs != nil {
			for _, id := range p.Subs.IDs {
				out[id] = true
			}
		}
	}
	return out
}

// ListSubscriptions GET /subscriptions
func (h *Handler) ListSubscriptions(c *gin.Context) {
	var rows []model.Subscription
	h.DB.Order("CASE state WHEN 'active' THEN 0 WHEN 'stalled' THEN 1 WHEN 'paused' THEN 2 ELSE 3 END, updated_at DESC").Find(&rows)
	inflight, running := subInflightCounts(h.DB), subRunningSet(h.DB)
	out := make([]subDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSubDTO(r, inflight, running))
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// SubscriptionOf GET /subscriptions/of?tmdb_id=&type= —— 找资源页查「订阅过没有」
func (h *Handler) SubscriptionOf(c *gin.Context) {
	id, _ := strconv.Atoi(c.Query("tmdb_id"))
	var sub model.Subscription
	if id <= 0 || h.DB.Where("tmdb_id = ? AND media_type = ?", id, c.Query("type")).First(&sub).Error != nil {
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toSubDTO(sub, subInflightCounts(h.DB), subRunningSet(h.DB))})
}

// CreateSubscription POST /subscriptions
func (h *Handler) CreateSubscription(c *gin.Context) {
	var f subForm
	if err := c.ShouldBindJSON(&f); err != nil || f.TmdbID <= 0 || (f.MediaType != "movie" && f.MediaType != "tv") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var exist model.Subscription
	if h.DB.Where("tmdb_id = ? AND media_type = ?", f.TmdbID, f.MediaType).First(&exist).Error == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "这部已经订阅过了", "data": toSubDTO(exist, subInflightCounts(h.DB), subRunningSet(h.DB))})
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	m, err := tc.getByTmdbID(f.TmdbID, f.MediaType == "tv")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 查询失败：" + err.Error()})
		return
	}
	if m == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "TMDB 上没有这个条目"})
		return
	}
	now := time.Now()
	sub := model.Subscription{
		TmdbID: f.TmdbID, MediaType: f.MediaType, Title: m.Title, OrigTitle: m.OriginalTitle,
		Year: m.Year, PosterPath: m.PosterPath, State: subStateActive, NextCheckAt: &now,
	}
	if _, err := applySubForm(&sub, f, now); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.DB.Create(&sub).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	// 建完马上查一轮：用户要立刻看到缺几集、能不能找到
	job, err := enqueueSubscribeJob(h, []uint{sub.ID}, true, sub.Title, "web")
	resp := gin.H{"data": toSubDTO(sub, nil, map[uint]bool{sub.ID: err == nil}), "message": "已订阅，正在检查"}
	if err == nil {
		resp["job_id"] = job.ID
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateSubscription PUT /subscriptions/:id —— 表单字段 + 状态（paused / active）
func (h *Handler) UpdateSubscription(c *gin.Context) {
	sub, ok := h.subByParam(c)
	if !ok {
		return
	}
	var req struct {
		subForm
		State string `json:"state"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	now := time.Now()
	changed, err := applySubForm(&sub, req.subForm, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	switch req.State {
	case "":
	case subStatePaused:
		sub.State = subStatePaused
	case subStateActive:
		if sub.State != subStateActive {
			sub.State, sub.EmptyRounds, sub.DoneAt = subStateActive, 0, nil
			changed = true
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "状态只能是 追更 / 暂停"})
		return
	}
	if changed && sub.State != subStatePaused {
		// 范围变了 / 恢复追更：完成状态作废，下一分钟调度器就会来查
		if sub.State == subStateDone {
			sub.State, sub.DoneAt = subStateActive, nil
		}
		sub.NextCheckAt = &now
	}
	if err := h.DB.Save(&sub).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toSubDTO(sub, subInflightCounts(h.DB), subRunningSet(h.DB)), "message": "已保存"})
}

// DeleteSubscription DELETE /subscriptions/:id —— 只删订阅与尝试记账，不碰网盘、不碰整理记录
func (h *Handler) DeleteSubscription(c *gin.Context) {
	sub, ok := h.subByParam(c)
	if !ok {
		return
	}
	h.DB.Where("sub_id = ?", sub.ID).Delete(&model.SubAttempt{})
	h.DB.Delete(&sub)
	c.JSON(http.StatusOK, gin.H{"message": "已取消订阅"})
}

// RunSubscription POST /subscriptions/:id/run —— 立即搜索（入队，暂停 / 已完成的也查）
func (h *Handler) RunSubscription(c *gin.Context) {
	sub, ok := h.subByParam(c)
	if !ok {
		return
	}
	job, err := enqueueSubscribeJob(h, []uint{sub.ID}, true, sub.Title, "web")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "订阅搜索")
}

// RetrySubAttempt POST /subscriptions/:id/attempts/:aid/retry —— 把试过的资源放回可尝试（下一轮就会再试）
func (h *Handler) RetrySubAttempt(c *gin.Context) {
	sub, ok := h.subByParam(c)
	if !ok {
		return
	}
	aid, _ := strconv.Atoi(c.Param("aid"))
	var a model.SubAttempt
	if aid <= 0 || h.DB.Where("id = ? AND sub_id = ?", aid, sub.ID).First(&a).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有这条记录"})
		return
	}
	if a.Status == subAttemptInflight {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这条还在路上，等整理完再说"})
		return
	}
	now := time.Now()
	h.DB.Model(&a).Update("retry_at", now)
	if sub.State != subStatePaused {
		h.DB.Model(&sub).Updates(map[string]any{"next_check_at": now, "empty_rounds": 0})
	}
	c.JSON(http.StatusOK, gin.H{"message": "下一轮会再试这条资源"})
}

func (h *Handler) subByParam(c *gin.Context) (model.Subscription, bool) {
	var sub model.Subscription
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 || h.DB.First(&sub, id).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "订阅不存在"})
		return sub, false
	}
	return sub, true
}

// ==================== 详情 ====================

// subEpCell 集格子里的一集
type subEpCell struct {
	E     int    `json:"e"`
	State string `json:"state"` // have / inflight / awaiting / missing / unaired / skipped（只追新集时订阅之前播的）
	Air   string `json:"air,omitempty"`
}

type subSeasonGrid struct {
	Season int         `json:"season"`
	Eps    []subEpCell `json:"eps"`
}

// subAttemptDTO 尝试记录
type subAttemptDTO struct {
	model.SubAttempt
	Episodes []string `json:"episodes"`
	Records  int      `json:"records"` // 认领到这条来源链接的整理记录数（跳整理记录页看）
}

// subEpisodeGrid 剧集按季的集格子（盘点同一套口径，TMDB 有缓存）
func subEpisodeGrid(db *gorm.DB, tc *TmdbClient, sub *model.Subscription, cfg subscribeCfg, now time.Time) ([]subSeasonGrid, error) {
	sch, err := fetchSubTVSchedule(tc, sub)
	if err != nil {
		return nil, err
	}
	have := subHaveOf(db, scanLedgerTitlesCached(), sub.TmdbID, sub.MediaType)
	inflight := subInflight(db, sub.ID)
	awaiting := map[epKey]bool{}
	var rows []model.SubAttempt
	db.Where("sub_id = ? AND status = ? AND reason = ?", sub.ID, subAttemptInflight, subReasonAwaiting).Find(&rows)
	for _, r := range rows {
		for _, k := range unmarshalEpKeys(r.Episodes) {
			awaiting[k] = true
		}
	}
	var from time.Time
	if sub.Follow == subFollowNew && sub.FollowFrom != nil {
		f := sub.FollowFrom.In(time.Local)
		from = time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.Local)
	}
	var out []subSeasonGrid
	idx := map[int]int{}
	for _, e := range sch.Eps {
		cell := subEpCell{E: e.Key.E}
		if !e.Air.IsZero() {
			cell.Air = e.Air.Format("2006-01-02")
		}
		switch {
		case have.Eps[e.Key]:
			cell.State = "have"
		case awaiting[e.Key]:
			cell.State = "awaiting"
		case inflight[e.Key]:
			cell.State = "inflight"
		case e.Air.IsZero() || e.Air.Add(cfg.airDelay()).After(now):
			cell.State = "unaired"
		case !from.IsZero() && e.Air.Before(from):
			cell.State = "skipped"
		default:
			cell.State = "missing"
		}
		i, ok := idx[e.Key.S]
		if !ok {
			i = len(out)
			idx[e.Key.S] = i
			out = append(out, subSeasonGrid{Season: e.Key.S})
		}
		out[i].Eps = append(out[i].Eps, cell)
	}
	return out, nil
}

// GetSubscription GET /subscriptions/:id —— 订阅 + 集格子 + 尝试记录
func (h *Handler) GetSubscription(c *gin.Context) {
	sub, ok := h.subByParam(c)
	if !ok {
		return
	}
	resp := gin.H{"data": toSubDTO(sub, subInflightCounts(h.DB), subRunningSet(h.DB))}

	var atts []model.SubAttempt
	h.DB.Where("sub_id = ?", sub.ID).Order("id DESC").Limit(100).Find(&atts)
	linkIDs := []uint{}
	for _, a := range atts {
		if a.LinkID > 0 {
			linkIDs = append(linkIDs, a.LinkID)
		}
	}
	recCount := map[uint]int{}
	if len(linkIDs) > 0 {
		var rows []struct {
			LinkID uint
			N      int
		}
		h.DB.Model(&model.OrganizeRecord{}).Select("link_id, COUNT(*) AS n").Where("link_id IN ?", linkIDs).Group("link_id").Scan(&rows)
		for _, r := range rows {
			recCount[r.LinkID] = r.N
		}
	}
	list := make([]subAttemptDTO, 0, len(atts))
	for _, a := range atts {
		d := subAttemptDTO{SubAttempt: a, Records: recCount[a.LinkID], Episodes: []string{}}
		for _, k := range unmarshalEpKeys(a.Episodes) {
			d.Episodes = append(d.Episodes, k.String())
		}
		list = append(list, d)
	}
	resp["attempts"] = list

	if sub.MediaType == "tv" {
		if tc, err := loadTmdbClient(); err != nil {
			resp["grid_error"] = err.Error()
		} else if grid, err := subEpisodeGrid(h.DB, tc, &sub, loadSubscribeCfg(), time.Now()); err != nil {
			resp["grid_error"] = err.Error()
		} else {
			resp["grid"] = grid
		}
	}
	c.JSON(http.StatusOK, resp)
}

// SubTmdbSeasons GET /subscriptions/seasons?tmdb_id= —— 订阅弹窗选季用
func (h *Handler) SubTmdbSeasons(c *gin.Context) {
	id, _ := strconv.Atoi(c.Query("tmdb_id"))
	if id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d, err := tc.detailOf("tv", id)
	if err != nil || d == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "TMDB 查询失败"})
		return
	}
	type season struct {
		Season   int    `json:"season"`
		Name     string `json:"name"`
		Episodes int    `json:"episodes"`
		AirDate  string `json:"air_date"`
	}
	out := []season{}
	for s, n := range d.SeasonEps {
		out = append(out, season{Season: s, Name: d.SeasonNames[s], Episodes: n, AirDate: d.Seasons[s]})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Season < out[j-1].Season; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "status": d.Status, "ended": tmdbEnded(d.Status)})
}

// ==================== 配置 ====================

// SubscribeGetConfig GET /subscribe/config
func (h *Handler) SubscribeGetConfig(c *gin.Context) {
	cfg := loadSubscribeCfg()
	spent := 0
	if h.DB != nil {
		spent = subPointsSpentToday(h, time.Now())
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg, "re0_spent_today": spent})
}

// SubscribeSaveConfig POST /subscribe/config
func (h *Handler) SubscribeSaveConfig(c *gin.Context) {
	var cfg subscribeCfg
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	cfg = normalizeSubscribeCfg(cfg)
	b, _ := json.Marshal(cfg)
	if err := h.Config.SaveSetting("subscribe", string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg, "message": "已保存"})
}
