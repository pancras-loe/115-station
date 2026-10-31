package api

import (
	"fmt"
	"strings"

	"115-station/internal/model"
)

// 机器人的「检查更新」「更新」（TG /update 带按钮，企微发文字）。
// 更新本身走 startSelfUpdate，和网页同一条路（selfupdate.go）；
// 「更新」/ 按钮本身就是确认（检查更新的回复里已写明版本与更新内容），直接开始；
// 只有任务队列里有任务在跑时才再问一次，写明会中断哪些任务

const botUpdateNotesMax = 600

// botUpdateCheck 查一次更新，返回要回的话；能一键更新时 target 是要更新到的版本
func botUpdateCheck() (lines []string, target string) {
	st := checkUpdateFresh()
	cur := st.Current
	if st.ImageVersion != "" {
		cur += "（镜像自带 " + st.ImageVersion + "）"
	}
	lines = append(lines, "当前版本："+cur)
	if st.LastFailed != "" {
		lines = append(lines, "⚠ "+st.LastFailed)
	}
	switch {
	case st.Latest == "" && st.Error != "":
		return append(lines, "✗ 检查失败："+st.Error), ""
	case st.Latest == "":
		return append(lines, "还没有发布过正式版。"), ""
	case !st.Comparable:
		return append(lines, "最新正式版："+st.Latest, "当前是本地 / 开发构建，不做新旧比较。"), ""
	case !st.HasUpdate:
		return append(lines, "最新正式版："+st.Latest, "✓ 已是最新版本。"), ""
	}
	lines = append(lines, "🆕 最新版本："+st.Latest)
	if notes := strings.TrimSpace(st.Notes); notes != "" {
		lines = append(lines, "", truncRunes(notes, botUpdateNotesMax))
	}
	if st.URL != "" {
		lines = append(lines, "", "更新说明："+st.URL)
	}
	if a := st.Apply; a != nil && a.Phase != "failed" {
		return append(lines, "", fmt.Sprintf("正在更新到 %s，好了会推通知。", a.Target)), ""
	}
	if !st.CanApply {
		return append(lines, "", st.ApplyBlock, "docker compose pull && docker compose up -d"), ""
	}
	return lines, st.Latest
}

// updateBusyJobs 任务队列里正在跑的任务（重启会中断它们）。增量轮询只短暂持锁、中断了下一轮重来，不算
func (h *Handler) updateBusyJobs() []string {
	if h == nil || h.DB == nil {
		return nil
	}
	var jobs []model.TaskJob
	h.DB.Where("status = ?", jobRunning).Order("id").Limit(5).Find(&jobs)
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, shortTitle(j.Title))
	}
	return names
}

// botUpdateConfirmLines 有任务在跑时的确认
func botUpdateConfirmLines(target string, busy []string) []string {
	return []string{
		fmt.Sprintf("确认更新到 %s？", target),
		"正在跑的任务会被中断（重启后标为中断，可在任务中心重试）：" + strings.Join(busy, "、"),
	}
}

// botUpdateRequest 用户要更新：没任务在跑就直接开始；有就返回确认的话（ask = true）
func (h *Handler) botUpdateRequest(target, by string, reply func(...string)) (lines []string, ask bool) {
	if busy := h.updateBusyJobs(); len(busy) > 0 {
		return botUpdateConfirmLines(target, busy), true
	}
	return botUpdateGo(target, by, reply), false
}

// botUpdateGo 开始更新，返回立即回复的话；下载结果由 reply 推回
func botUpdateGo(target, by string, reply func(...string)) []string {
	if err := startSelfUpdate(target, by, reply); err != nil {
		return []string{"✗ " + err.Error()}
	}
	if target == "" {
		target = "最新版" // 企微「确认更新」不带版本号
	}
	return []string{fmt.Sprintf("⏳ 开始下载 %s（约几十 MB），校验后自动重启，大约一分钟后推「已更新」。", target)}
}

// updateTGCallback TG 按钮：u:a:<版本> 更新（没任务在跑直接开始，否则先确认）；
// u:g:<版本> 确认更新；u:c 取消
func (h *Handler) updateTGCallback(data string, reply func(...string)) (string, [][]tgButton) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) < 2 {
		return "按钮已失效，请重新发 /update。", nil
	}
	switch parts[1] {
	case "a":
		if len(parts) != 3 {
			break
		}
		lines, ask := h.botUpdateRequest(parts[2], "TG 机器人", reply)
		if !ask {
			return strings.Join(lines, "\n"), nil
		}
		return strings.Join(lines, "\n"), [][]tgButton{{
			{Text: "确认更新", Data: "u:g:" + parts[2]},
			{Text: "取消", Data: "u:c"},
		}}
	case "g":
		if len(parts) != 3 {
			break
		}
		return strings.Join(botUpdateGo(parts[2], "TG 机器人", reply), "\n"), nil
	case "c":
		return "已取消更新。", nil
	}
	return "按钮已失效，请重新发 /update。", nil
}
