package api

// 分享 STRM 的接口与机器人入口（sharestrm.go / sharestrmgen.go）。
// 页面「分享 STRM」（/share-strm）与扩展功能卡片读写这里；链接转存页的「生成 STRM」与机器人都走 addShareStrm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ---- 添加 ----

// addShareStrm 登记一个分享并排一次生成。已经登记过的同一个分享：有新提取码就换上，再排一次立即检查。
// 先在请求里列一次分享根：提取码错、分享失效当场告诉用户，不排一个注定失败的任务
func (h *Handler) addShareStrm(text, code, label string) (model.ShareSource, model.TaskJob, error) {
	cfg := loadShareStrmCfg()
	if !cfg.Enabled {
		return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("分享 STRM 没开：到「扩展功能 → 分享 STRM」打开")
	}
	if msg := shareRootProblem(cfg.Root, loadLibraries()); msg != "" {
		return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("%s", msg)
	}
	if h.strmExplicitDomain() == "" {
		return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("还没填直连域名（Strm 管理 → 配置）：没有它写出来的 STRM 播不了")
	}
	shareCode := extractShareCode(text)
	if !is115ShareLink(text) && !reShareCodeID.MatchString(shareCode) {
		return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("认不出 115 分享链接")
	}
	if code == "" {
		_, code = splitShareLink(text)
	}
	if code == "" {
		if m := reSharePass.FindStringSubmatch(text); m != nil {
			code = m[1]
		}
	}
	code = strings.TrimSpace(code)

	var src model.ShareSource
	if h.DB.Where("share_code = ?", shareCode).First(&src).Error == nil {
		if code != "" && code != src.ReceiveCode {
			h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Update("receive_code", code)
			src.ReceiveCode = code
		}
		job, err := enqueueShareStrm(h.DB, src, label)
		return src, job, err
	}

	cookie, err := h.cookieOf(acctMain)
	if err != nil {
		return model.ShareSource{}, model.TaskJob{}, err
	}
	items, title, err := shareList(shareCode, code, "0", cookie)
	if err != nil {
		if shareStateOfErr(err) == shareStateBadCode {
			return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("提取码不对（或没带提取码）：%s", err.Error())
		}
		return model.ShareSource{}, model.TaskJob{}, err
	}
	if len(items) == 0 {
		return model.ShareSource{}, model.TaskJob{}, fmt.Errorf("分享是空的")
	}
	src = model.ShareSource{ShareCode: shareCode, ReceiveCode: code, Title: truncateStr(title, 480),
		LocalDir: h.uniqueShareDir(cfg, shareLocalDirName(title, shareCode), shareCode),
		Layout:   "keep", State: shareStatePending, Source: firstNonEmpty(label, "web"), FollowHours: cfg.FollowHours}
	if err := h.DB.Create(&src).Error; err != nil {
		return model.ShareSource{}, model.TaskJob{}, err
	}
	plogShareStrm.Printf("+ 添加分享「%s」（%s）→ %s", src.Title, shareCode, src.LocalDir)
	job, err := enqueueShareStrm(h.DB, src, label)
	return src, job, err
}

// uniqueShareDir 本地目录名不能和别的分享撞，也不能撞上本地已有的同名目录（可能是用户自己放的）
func (h *Handler) uniqueShareDir(cfg shareStrmCfg, name, shareCode string) string {
	taken := func(n string) bool {
		var c int64
		h.DB.Model(&model.ShareSource{}).Where("local_dir = ?", n).Count(&c)
		if c > 0 {
			return true
		}
		_, err := os.Stat(filepath.Join(shareRootAbs(cfg), filepath.FromSlash(n)))
		return err == nil
	}
	if !taken(name) {
		return name
	}
	alt := name + " - " + shareCode
	for i := 2; taken(alt); i++ {
		alt = fmt.Sprintf("%s - %s-%d", name, shareCode, i)
	}
	return alt
}

// ---- 接口 ----

// shareSourceView 列表里的一个分享
type shareSourceView struct {
	model.ShareSource
	ReceiveCode string `json:"receive_code"`
	Busy        bool   `json:"busy"` // 有排着 / 正在跑的生成任务
}

func (h *Handler) shareSourceViewOf(s model.ShareSource) shareSourceView {
	return shareSourceView{ShareSource: s, ReceiveCode: s.ReceiveCode, Busy: shareSourceBusy(h.DB, s.ID)}
}

// ShareStrmConfig GET /sharestrm/config
func (h *Handler) ShareStrmConfig(c *gin.Context) {
	cfg := loadShareStrmCfg()
	var n int64
	h.DB.Model(&model.ShareSource{}).Count(&n)
	c.JSON(http.StatusOK, gin.H{
		"config": cfg, "media_root": mediaRoot(), "root_abs": shareRootAbs(cfg),
		"root_problem": shareRootProblem(cfg.Root, loadLibraries()),
		"domain_ok":    h.strmExplicitDomain() != "", "sources": n,
	})
}

// ShareStrmSaveConfig POST /sharestrm/config
func (h *Handler) ShareStrmSaveConfig(c *gin.Context) {
	var req shareStrmCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	req = req.normalized()
	old := loadShareStrmCfg()
	if req.Root != old.Root {
		if msg := shareRootProblem(req.Root, loadLibraries()); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": msg})
			return
		}
		// 已经生成过的 STRM 都在旧目录下、台账也按旧根记：改根要先删掉那些分享，别留下一堆没人管的 STRM
		var n int64
		h.DB.Model(&model.ShareSource{}).Count(&n)
		if n > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("已经有 %d 个分享生成在「%s」下，先删掉它们再改目录", n, old.Root)})
			return
		}
	}
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting("sharestrm", string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	plogShareStrm.Printf("保存配置：开关=%v 默认处理=%s 目录=%s 最小体积=%dMB 跳过库里已有=%v 附属文件=%v 默认追更=%d小时",
		req.Enabled, req.DefaultAction, req.Root, req.MinSizeMB, req.SkipOwned, req.Assets, req.FollowHours)
	c.JSON(http.StatusOK, gin.H{"message": "已保存", "config": req})
}

// ShareStrmSources GET /sharestrm/sources
func (h *Handler) ShareStrmSources(c *gin.Context) {
	var list []model.ShareSource
	h.DB.Order("id DESC").Find(&list)
	out := make([]shareSourceView, 0, len(list))
	for _, s := range list {
		out = append(out, h.shareSourceViewOf(s))
	}
	c.JSON(http.StatusOK, gin.H{"sources": out})
}

// ShareStrmFiles GET /sharestrm/sources/:id/files
func (h *Handler) ShareStrmFiles(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	var files []model.ShareFile
	h.DB.Where("source_id = ?", src.ID).Order("kind DESC, rel_path ASC").Limit(5000).Find(&files)
	c.JSON(http.StatusOK, gin.H{"source": h.shareSourceViewOf(src), "files": files, "root_abs": shareRootAbs(loadShareStrmCfg())})
}

// ShareStrmAdd POST /sharestrm/add {url, code}
func (h *Handler) ShareStrmAdd(c *gin.Context) {
	var req struct {
		URL  string `json:"url"`
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请贴 115 分享链接"})
		return
	}
	src, job, err := h.addShareStrm(strings.TrimSpace(req.URL), req.Code, "web")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": fmt.Sprintf("「%s」已排进队列（任务 #%d）", firstNonEmpty(src.Title, src.ShareCode), job.ID),
		"source": h.shareSourceViewOf(src), "job_id": job.ID})
}

// ShareStrmCheck POST /sharestrm/sources/:id/check —— 立即检查（再生成一次）
func (h *Handler) ShareStrmCheck(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	if h.strmExplicitDomain() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没填直连域名（Strm 管理 → 配置）"})
		return
	}
	job, err := enqueueShareStrm(h.DB, src, "web")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": fmt.Sprintf("已排进队列（任务 #%d）", job.ID), "job_id": job.ID})
}

// ShareStrmUpdate PUT /sharestrm/sources/:id {receive_code} —— 改提取码，改完排一次检查
func (h *Handler) ShareStrmUpdate(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	var req struct {
		ReceiveCode *string `json:"receive_code"`
		FollowHours *int    `json:"follow_hours"`
		Paused      *bool   `json:"paused"`
		// Ignored 待清理里「先留着」：失效了也不再列进待清理
		Ignored *bool `json:"ignored"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name := firstNonEmpty(src.Title, src.ShareCode)
	if req.ReceiveCode != nil {
		// 改提取码：清掉失效 / 出错的计数，马上重新检查
		code := strings.TrimSpace(*req.ReceiveCode)
		h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Updates(map[string]any{"receive_code": code,
			"state": shareStatePending, "last_error": "", "gone_streak": 0, "fail_streak": 0, "notified": false})
		src.ReceiveCode = code
		plogShareStrm.Printf("改提取码：%s（%s）", name, src.ShareCode)
		job, err := enqueueShareStrm(h.DB, src, "web")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已保存，正在重新检查（任务 #%d）", job.ID)})
		return
	}
	upd := map[string]any{}
	if req.FollowHours != nil {
		src.FollowHours = normFollowHours(*req.FollowHours)
		upd["follow_hours"] = src.FollowHours
	}
	if req.Paused != nil {
		src.Paused = *req.Paused
		upd["paused"] = src.Paused
	}
	if req.Ignored != nil {
		upd["ignored"] = *req.Ignored
	}
	if len(upd) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有要改的"})
		return
	}
	if req.FollowHours != nil || req.Paused != nil {
		// 下一次检查从现在算：暂停、关掉追更就不排；已经认定失效的等用户处理，不再自动查
		var next *time.Time
		if !src.Paused && src.FollowHours > 0 && src.GoneStreak < shareGoneStreakMax && src.State != shareStateBadCode {
			t := time.Now().Add(time.Duration(src.FollowHours) * time.Hour)
			next = &t
		}
		upd["next_check"] = next
	}
	h.DB.Model(&model.ShareSource{}).Where("id = ?", src.ID).Updates(upd)
	plogShareStrm.Printf("改设置：%s（%s）%v", name, src.ShareCode, upd)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// ShareStrmDelete DELETE /sharestrm/sources/:id —— 删本地 STRM 与记录，不碰 115
func (h *Handler) ShareStrmDelete(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	if shareSourceBusy(h.DB, src.ID) {
		c.JSON(http.StatusConflict, gin.H{"error": "这个分享正在生成（或排着队），等它结束或在任务中心停掉后再删"})
		return
	}
	n, err := deleteShareSource(h.DB, shareRootAbs(loadShareStrmCfg()), src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	plogShareStrm.Printf("− 删除分享「%s」（%s），本地删掉 %d 个 STRM", firstNonEmpty(src.Title, src.ShareCode), src.ShareCode, n)
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已删除，本地删掉 %d 个 STRM", n)})
}

func (h *Handler) shareSourceParam(c *gin.Context) (model.ShareSource, bool) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var src model.ShareSource
	if id == 0 || h.DB.First(&src, id).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分享不存在"})
		return src, false
	}
	return src, true
}

// ---- 机器人 ----

// shareBotPending 「每次问」时记着用户刚发的分享链接，等他回「转存」或「STRM」（内存里，10 分钟）
var shareBotPending = struct {
	sync.Mutex
	m map[string]shareBotAsk
}{m: map[string]shareBotAsk{}}

type shareBotAsk struct {
	link string
	at   time.Time
}

const shareBotAskTTL = 10 * time.Minute

func shareBotRemember(user, link string) {
	shareBotPending.Lock()
	defer shareBotPending.Unlock()
	for k, v := range shareBotPending.m {
		if time.Since(v.at) > shareBotAskTTL {
			delete(shareBotPending.m, k)
		}
	}
	shareBotPending.m[user] = shareBotAsk{link: link, at: time.Now()}
}

func shareBotTake(user string) string {
	shareBotPending.Lock()
	defer shareBotPending.Unlock()
	v, ok := shareBotPending.m[user]
	delete(shareBotPending.m, user)
	if !ok || time.Since(v.at) > shareBotAskTTL {
		return ""
	}
	return v.link
}

// shareBotPick 用户回了「转存」或「STRM」：认不出返回 false，交给别的指令
func (h *Handler) shareBotPick(user, lower string, reply func(lines ...string)) bool {
	t := strings.TrimPrefix(strings.TrimSpace(lower), "/")
	isStrm := t == "strm" || t == "生成strm"
	if !isStrm && t != "转存" {
		return false
	}
	link := shareBotTake(user)
	if link == "" {
		reply("没有等着处理的 115 分享链接，先把链接发过来。")
		return true
	}
	if isStrm {
		h.botShareStrm(link, reply)
	} else {
		h.botShareTransfer(link, reply)
	}
	return true
}

// botShareLink 机器人收到 115 分享链接：按「贴链接时默认怎么处理」分流
func (h *Handler) botShareLink(user, link string, reply func(lines ...string)) {
	switch loadShareStrmCfg().linkAction() {
	case "strm":
		h.botShareStrm(link, reply)
	case "ask":
		shareBotRemember(user, link)
		reply("这条 115 分享要怎么处理？",
			"回复「转存」— 转存进网盘，整理入库",
			"回复「STRM」— 不转存，直接生成 STRM 播放（不占网盘空间，分享失效后就播不了）")
	default:
		h.botShareTransfer(link, reply)
	}
}

// botShareTransfer 转存（原来机器人贴分享链接的做法）
func (h *Handler) botShareTransfer(link string, reply func(lines ...string)) {
	shareURL, code := splitShareLink(link)
	reply("⏳ 开始转存 115 分享…", truncateStr(shareURL, 70))
	go func() {
		msg, ok, fail, err := h.shareReceiveManual(shareURL, code, "", "机器人", true)
		if err != nil {
			reply("✗ 转存失败: " + err.Error())
			return
		}
		if ok == 0 && fail == 0 {
			reply("分享为空，未转存任何内容")
			return
		}
		reply("✓ " + msg + "（整理入库已自动触发）")
	}()
}

// botShareStrm 生成分享 STRM，跑完回结果
func (h *Handler) botShareStrm(link string, reply func(lines ...string)) {
	src, job, err := h.addShareStrm(link, "", "机器人")
	if err != nil {
		reply("✗ " + err.Error())
		return
	}
	reply(fmt.Sprintf("⏳「%s」排进队列（任务 #%d），生成完回复结果", firstNonEmpty(src.Title, src.ShareCode), job.ID))
	onJobDone(job.ID, func(j model.TaskJob) {
		switch j.Status {
		case jobSuccess:
			reply("✓ 分享 STRM：" + j.Message)
		case jobPartial:
			reply("◐ 分享 STRM：" + j.Message, "出错的文件在「分享 STRM」页看。")
		case jobCanceled:
			reply("○ 分享 STRM 已停止：" + j.Message)
		default:
			reply("✗ 分享 STRM 失败：" + j.Message)
		}
	})
}


// ---- Emby 里删了分享 STRM ----

// shareStrmOnEmbyDelete 用户在 Emby 里删了分享 STRM（或它所在的季 / 剧）：记屏蔽，之后的检查不再生成回来。
// 只认本地确实已经没了的文件（Emby 删条目会连 STRM 一起删）；扫库清掉的旧条目如果本地文件还在，不算。
// 只动本地记账，任何情况下不对 115 发请求（分享不是自己的，SHARE-STRM-PLAN.md §7.3）
func (h *Handler) shareStrmOnEmbyDelete(localPath string) {
	if localPath == "" || h.DB == nil {
		return
	}
	root := shareRootAbs(loadShareStrmCfg())
	rel, err := filepath.Rel(root, localPath)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return
	}
	var files []model.ShareFile
	h.DB.Where(`blocked = ? AND (rel_path = ? OR rel_path LIKE ? ESCAPE '\')`, false, rel, likeEscape(rel)+"/%").Find(&files)
	n := 0
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.RelPath))); !os.IsNotExist(err) {
			continue
		}
		h.DB.Model(&model.ShareFile{}).Where("id = ?", f.ID).Update("blocked", true)
		n++
	}
	if n > 0 {
		plogShareStrm.Printf("○ Emby 里删了 %s：%d 个文件以后不再生成", rel, n)
	}
}
