package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 洗版待确认 ====================
//
// 115 同一个分享转存过一次就不能再转（「文件已接收，无需重复接收」），离线也要再扣一次配额：
// 手动提交的内容洗版判输、直接移进「已存在」的话，用户事后想「还是要新的这份」就只能去「已存在」里
// 自己捞。所以开着 org-basic.wash_ask 时，**订阅以外**提交的内容（链接转存页、找资源、机器人贴链接、
// 离线下载、自己放进转存目录的）判输了先不动：原地登记一条 HoldWash 的待确认记录，推一条通知问
// 「强制洗版」还是「不洗版」，网页、TG 按钮、企微回复都能答。
//   - 强制洗版：走「确认入库」，洗版换成 forceWashStrategy（库里同一部电影 / 同一集的版本进 115 回收站）；
//   - 不洗版：走「确认入库」，不再问，照原来的判定把新的移进「已存在」文件夹。
// 同一份文件（sha1 相同）不问：内容一模一样，换了等于白删一份。订阅的不问：订阅自己有洗版（subwash.go），
// 判得和整理一样，转进来又判输多半是同名并排的旧版，问了也是白问。
// 取代了原来机器人的「强制覆盖 <链接>」：那个要在提交前就想好，这里等判输了再问。
// 同 HoldDup：sticky，「人工确认」开关关着也不被自动整理接手；谁先答算谁的（机器人只认没答过的，网页可以改）。

const (
	washChoiceForce = "force" // 强制洗版
	washChoiceSkip  = "skip"  // 不洗版
)

// washAskItem 一个判输的视频：新来的、库里比下去它的那份、为什么
type washAskItem struct {
	Fid  string `json:"fid"`
	Name string `json:"name"`
	Old  string `json:"old"`
	Why  string `json:"why"`
}

// washAsk 这个条目判输了要不要停下来问：开关开着、还没问过、不是订阅进来的。
// names 是条目自己的名字与上层条目名（认来源链接用，见 dlLinkFromSub），顶层条目名 c.top 自动带上
func (c *orgCtx) washAsk(fid string, names ...string) bool {
	if !c.cfg.WashAsk || c.washChoice != "" {
		return false
	}
	return !dlLinkFromSub(model.DB, fid, append(names, c.top)...)
}

// washHoldMessage 待确认记录上的一句话
func washHoldMessage(items []washAskItem) string {
	if len(items) == 0 {
		return ""
	}
	why := strings.TrimPrefix(items[0].Why, "新来的")
	if len(items) == 1 {
		return "手动提交的这份不如库里的：新来的" + why + "。要强制洗版吗？"
	}
	return fmt.Sprintf("手动提交的 %d 个视频不如库里的（第一个：新来的%s 等）。要强制洗版吗？", len(items), why)
}

// holdWash 判输的这几个视频（连同 files 里跟着它们的字幕 / 集 NFO）原地不动，登记一条洗版待确认并推通知。
// videos 是判输的视频，files 是要登记的全部文件（含视频）
func (c *orgCtx) holdWash(source, fid, kind string, media *TmdbMedia, category, rootRel string,
	videos, files []remoteFile, items []washAskItem) []OrganizeResult {
	recFiles := make([]orgRecordFile, 0, len(files))
	for _, f := range files {
		recFiles = append(recFiles, orgRecordFile{Fid: f.Fid, Name: f.Name, Dir: f.Path,
			Kind: recordFileKind(f.Name), PickCode: f.PickCode, Size: f.Size, Sha1: f.Sha1})
	}
	var size int64
	for _, v := range videos {
		size += v.Size
	}
	msg := washHoldMessage(items)
	b, _ := json.Marshal(items)
	rec := &model.OrganizeRecord{
		Source: source, SourceFid: fid, SourceKind: kind, SourceCid: c.cfg.Pending,
		Status: orgStatusAwaiting, Stage: "confirm", Message: msg,
		TmdbID: media.TmdbID, Title: media.Title, Year: media.Year,
		MediaType: media.MediaType, PosterPath: media.PosterPath,
		Category: category, TargetDir: rootRel,
		Files: marshalRecordFiles(recFiles), VideoCount: len(videos), TotalSize: size,
		HoldWash: true, WashGroups: string(b),
	}
	c.sink.note(rec)
	c.onLog(fmt.Sprintf("⏸ %s - %s（整理记录 → 待确认），文件留在原处", shortLogName(source), msg))
	if rec.ID != 0 {
		// 本轮后面的顶层条目里还有这些文件（同前缀的散文件兄弟），别再当新条目整理一遍
		ref := &awaitingRef{id: rec.ID, created: rec.CreatedAt, wash: true}
		for _, f := range files {
			ref.fids = append(ref.fids, f.Fid)
		}
		for _, id := range ref.fids {
			c.held[id] = ref
		}
		go notifyWashHold(rec)
	}
	out := make([]OrganizeResult, 0, len(videos))
	for _, v := range videos {
		out = append(out, OrganizeResult{FileName: v.Name, Status: orgStatusAwaiting, TmdbID: media.TmdbID, Title: media.Title,
			Year: media.Year, MediaType: media.MediaType, Category: category, Message: msg})
	}
	return out
}

func parseWashItems(s string) []washAskItem {
	var out []washAskItem
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ---- 回答 ----

// washChoiceText 回答的说法
func washChoiceText(choice string) string {
	if choice == washChoiceForce {
		return "强制洗版"
	}
	return "不洗版"
}

// submitWashChoice 记下回答并入「确认入库」队列。网页、机器人共用这一个入口。
// override：网页可以在排队期间改主意（执行时才读记录上的回答）；机器人传 false，只认还没答过的
func (h *Handler) submitWashChoice(rec *model.OrganizeRecord, choice, source string, override bool) (model.TaskJob, error) {
	if !rec.HoldWash || rec.Status != orgStatusAwaiting {
		return model.TaskJob{}, errors.New("这条记录不在「洗版待确认」状态（可能已经处理过）")
	}
	if choice != washChoiceForce && choice != washChoiceSkip {
		return model.TaskJob{}, errors.New("回答只能是强制洗版或不洗版")
	}
	q := h.DB.Model(&model.OrganizeRecord{}).Where("id = ? AND status = ? AND hold_wash = ?", rec.ID, orgStatusAwaiting, true)
	if !override {
		q = q.Where("wash_choice = '' OR wash_choice IS NULL")
	}
	res := q.Update("wash_choice", choice)
	if res.Error != nil {
		return model.TaskJob{}, res.Error
	}
	if res.RowsAffected == 0 {
		if !override {
			return model.TaskJob{}, errors.New("这一条刚被别处答过了")
		}
		return model.TaskJob{}, errors.New("这条记录刚被处理过，请刷新")
	}
	rec.WashChoice = choice
	job, err := h.enqueueConfirm(rec, pickReq{})
	if err == nil && source != "" && source != "web" {
		h.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("source", source)
	}
	if err == nil {
		log.Printf("[整理] ▶ 洗版待确认 #%d《%s》：%s（%s）", rec.ID, rec.Title, washChoiceText(choice), firstNonEmpty(source, "web"))
	}
	return job, err
}

// SubmitWashChoice POST /organize/records/:id/wash  body: {"choice":"force" | "skip"}
func (h *Handler) SubmitWashChoice(c *gin.Context) {
	var req struct {
		Choice string `json:"choice"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	job, err := h.submitWashChoice(&rec, req.Choice, "web", true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, washChoiceText(req.Choice))
}

// ---- 通知与机器人 ----

// washNoticeMax 通知里最多列几个视频（一季几十集判输时只列前几个）
const washNoticeMax = 5

// washNoticeLines 通知正文（第一行是标题）
func washNoticeLines(rec *model.OrganizeRecord, items []washAskItem) []string {
	title := rec.Title
	if rec.Year != "" {
		title += " (" + rec.Year + ")"
	}
	lines := []string{"⚖ 洗版待确认 · " + title, fmt.Sprintf("记录 #%d：手动提交的内容不如库里已有的，先没动。", rec.ID)}
	for i, it := range items {
		if i == washNoticeMax {
			lines = append(lines, fmt.Sprintf("…另有 %d 个", len(items)-washNoticeMax))
			break
		}
		lines = append(lines, "", "新来的："+shortLogName(it.Name))
		if it.Old != "" {
			lines = append(lines, "库里的："+shortLogName(it.Old))
		}
		if it.Why != "" {
			lines = append(lines, "（"+it.Why+"）")
		}
	}
	return append(lines, "",
		"强制洗版 = 库里同一部电影 / 同一集原有的版本移到 115 回收站（可还原），新的入库",
		"不洗版 = 库里的不动，新的移到「已存在」文件夹")
}

// washTGButtons TG 按钮：w:<记录 id>:<f | s>
func washTGButtons(id uint) [][]tgButton {
	return [][]tgButton{{
		{Text: "⚡ 强制洗版", Data: fmt.Sprintf("w:%d:f", id)},
		{Text: "不洗版", Data: fmt.Sprintf("w:%d:s", id)},
	}}
}

// notifyWashHold 停下来时推一条通知。TG 私聊带按钮，企微写回复指令，飞书 / QQ 指向网页
func notifyWashHold(rec *model.OrganizeRecord) {
	items := parseWashItems(rec.WashGroups)
	if rec.ID == 0 || len(items) == 0 {
		return
	}
	cfg, err := loadMessageConfig()
	if err != nil {
		return
	}
	body := washNoticeLines(rec, items)
	web := "到网页「任务中心 → 整理记录 → 待确认」处理。"
	if cfg.TG.isEnabled() && cfg.TG.Token != "" && cfg.TG.ChatID != "" {
		go func(tg TGConfig) {
			if id, err := strconv.ParseInt(strings.TrimSpace(tg.ChatID), 10, 64); err == nil && id > 0 {
				if err := tgDupSend(tg, id, strings.Join(body, "\n"), washTGButtons(rec.ID)); err != nil {
					log.Printf("[通知] ✗ TG 洗版待确认通知失败: %v", err)
				}
				return
			}
			// 群 / 频道：按钮回调只认私聊，只发文字
			_ = sendTelegramPlain(tg, strings.Join(append(append([]string(nil), body...), "", web), "\n"))
		}(cfg.TG)
	}
	if cfg.Wecom.isEnabled() && cfg.Wecom.CorpID != "" && cfg.Wecom.Secret != "" {
		go sendWecom(cfg.Wecom, strings.Join(append(append([]string(nil), body...), "",
			fmt.Sprintf("回复「洗版 %d 是」强制洗版，「洗版 %d 否」不洗版。", rec.ID, rec.ID)), "\n"))
	}
	sendExtraChannels(cfg, body[0], strings.Join(append(append([]string(nil), body[1:]...), "", web), "\n"))
}

// loadWashForBot 机器人只认还没答过的（谁先答算谁的；要改主意请到网页）
func loadWashForBot(h *Handler, id uint) (*model.OrganizeRecord, error) {
	var rec model.OrganizeRecord
	if h.DB.First(&rec, id).Error != nil || rec.Status != orgStatusAwaiting || !rec.HoldWash {
		return nil, errors.New("这一条已经处理过了")
	}
	if rec.WashChoice != "" {
		return nil, fmt.Errorf("这一条已经答过了（%s），正在排队；要改请到网页", washChoiceText(rec.WashChoice))
	}
	return &rec, nil
}

// washSubmitLine 提交后的回复
func washSubmitLine(choice string, job model.TaskJob) string {
	return fmt.Sprintf("✓ 已选择%s，已加入任务队列（任务 #%d），整理完另有通知。", washChoiceText(choice), job.ID)
}

// washTGCallback 处理 TG 按钮「w:<id>:<f|s>」，返回编辑后的消息（按钮收起）
func (h *Handler) washTGCallback(data string) string {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return "按钮已失效。"
	}
	id64, err := strconv.ParseUint(parts[1], 10, 32)
	choice := map[string]string{"f": washChoiceForce, "s": washChoiceSkip}[parts[2]]
	if err != nil || choice == "" {
		return "按钮已失效。"
	}
	rec, err := loadWashForBot(h, uint(id64))
	if err != nil {
		return fmt.Sprintf("记录 #%d：%v。", id64, err)
	}
	lines := washNoticeLines(rec, parseWashItems(rec.WashGroups))
	job, err := h.submitWashChoice(rec, choice, "tg", false)
	if err != nil {
		return strings.Join(append(lines, "", "✗ "+err.Error()), "\n")
	}
	return strings.Join(append(lines, "", washSubmitLine(choice, job)), "\n")
}

// washReplyChoice 文字回答 → force / skip，认不出为空
func washReplyChoice(word string) string {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "是", "要", "强制", "强制洗版", "洗", "洗版", "y", "yes", "f", "force":
		return washChoiceForce
	case "否", "不", "不要", "不用", "不洗", "不洗版", "已存在", "跳过", "n", "no", "s", "skip":
		return washChoiceSkip
	}
	return ""
}

// washBotCommand 文字指令「洗版」：不带参数列出等着答的；「洗版 <id>」看详情；「洗版 <id> 是/否」提交
func (h *Handler) washBotCommand(user, args string) []string {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		var recs []model.OrganizeRecord
		h.DB.Where("status = ? AND hold_wash = ? AND (wash_choice = '' OR wash_choice IS NULL)", orgStatusAwaiting, true).
			Order("id ASC").Limit(10).Find(&recs)
		if len(recs) == 0 {
			return []string{"没有等着确认的洗版。"}
		}
		lines := []string{"等着确认的洗版："}
		for i := range recs {
			lines = append(lines, fmt.Sprintf("#%d %s：%s", recs[i].ID, recs[i].Title, recs[i].Message))
		}
		return append(lines, "", "回复「洗版 <编号>」看详情，「洗版 <编号> 是」强制洗版，「洗版 <编号> 否」不洗版。")
	}
	id64, err := strconv.ParseUint(strings.TrimPrefix(fields[0], "#"), 10, 32)
	if err != nil {
		return []string{"用法：洗版 <记录编号> 是 / 否，例如「洗版 12 是」。发「洗版」列出等着确认的。"}
	}
	rec, err := loadWashForBot(h, uint(id64))
	if err != nil {
		return []string{fmt.Sprintf("记录 #%d：%v。", id64, err)}
	}
	if len(fields) == 1 {
		return append(washNoticeLines(rec, parseWashItems(rec.WashGroups)), "",
			fmt.Sprintf("回复「洗版 %d 是」强制洗版，「洗版 %d 否」不洗版。", rec.ID, rec.ID))
	}
	choice := washReplyChoice(fields[1])
	if choice == "" {
		return []string{fmt.Sprintf("认不出「%s」：写「是」强制洗版，或「否」不洗版。", fields[1])}
	}
	job, err := h.submitWashChoice(rec, choice, dupBotSource(user), false)
	if err != nil {
		return []string{"✗ " + err.Error()}
	}
	return []string{washSubmitLine(choice, job)}
}
