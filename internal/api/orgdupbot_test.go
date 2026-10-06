package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// 整季双语：每组都是粤语 / 英语
func bilingualGroups(n int) []dupGroup {
	var gs []dupGroup
	for i := 0; i < n; i++ {
		ep := []string{"S04E21", "S04E22", "S04E23", "S04E24", "S04E25", "S04E26"}[i]
		gs = append(gs, dupGroup{Target: "剧集/越狱/Season 4/越狱." + ep + ".mkv", Episode: ep, Files: []dupFile{
			{Fid: ep + "-yue", Name: "越狱." + ep + ".粤语.mkv", Size: 700, Label: "粤语", Rank: -1},
			{Fid: ep + "-eng", Name: "越狱." + ep + ".英语.mkv", Size: 9000, Label: "英语", Rank: -1},
		}})
	}
	return gs
}

func TestParseDupReply(t *testing.T) {
	gs := bilingualGroups(2)
	cases := []struct {
		in   string
		want string
	}{
		{"2", "k1,k1"},
		{"英语", "k1,k1"},
		{"全留", "a,a"},
		{"不要", "x,x"},
		{"1 全留", "k0,a"},
		{"粤 英", "k0,k1"}, // 区别词包含也认
	}
	for _, c := range cases {
		got, err := parseDupReply(gs, strings.Fields(c.in))
		if err != nil || strings.Join(got, ",") != c.want {
			t.Errorf("%q → %v %v，想要 %s", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"3", "1 2 3", "国语", ""} {
		if _, err := parseDupReply(gs, strings.Fields(bad)); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

// TG 回调数据上限 64 字节；组多了只给批量按钮
func TestDupTGButtons(t *testing.T) {
	gs := bilingualGroups(2)
	rows := dupTGButtons(4294967295, gs, []string{"k1", ""})
	if len(rows) != 3 {
		t.Fatalf("两组逐组两行 + 批量一行，得到 %d 行", len(rows))
	}
	if !strings.HasPrefix(rows[0][1].Text, "✓ ") || strings.HasPrefix(rows[1][1].Text, "✓ ") {
		t.Fatalf("已选的要打勾：%+v", rows[:2])
	}
	if rows[2][0].Text != "全部留粤语" || rows[2][1].Text != "全部留英语" {
		t.Fatalf("批量按区别词：%+v", rows[2])
	}
	for _, r := range rows {
		for _, b := range r {
			if len(b.Data) > 64 {
				t.Fatalf("回调数据超长：%s", b.Data)
			}
		}
	}
	if rows := dupTGButtons(1, bilingualGroups(6), nil); len(rows) != 1 {
		t.Fatalf("超过 %d 组只给批量一行，得到 %d 行", dupTGMaxGroups, len(rows))
	}
	// 单组不要批量行
	if rows := dupTGButtons(1, bilingualGroups(1), nil); len(rows) != 1 || len(rows[0]) != 4 {
		t.Fatalf("单组：%+v", rows)
	}
}

func seedDupRecord(t *testing.T, groups []dupGroup) *model.OrganizeRecord {
	t.Helper()
	b, _ := json.Marshal(groups)
	rec := &model.OrganizeRecord{Source: "越狱/", SourceKind: "dir", Status: orgStatusAwaiting, Stage: "confirm",
		TmdbID: 2288, Title: "越狱", Year: "2005", MediaType: "tv", HoldDup: true, DupGroups: string(b)}
	if err := model.DB.Create(rec).Error; err != nil {
		t.Fatal(err)
	}
	return rec
}

func dupChoiceOf(t *testing.T, id uint) map[string]string {
	t.Helper()
	var rec model.OrganizeRecord
	model.DB.First(&rec, id)
	return parseDupChoice(rec.DupChoice)
}

// TG 上逐组点：没点全之前只更新那条消息，点全了才落库入队；之后再点认作已选过
func TestDupTGCallbackFlow(t *testing.T) {
	newTestDB(t, "dup_tg.db")
	h := &Handler{DB: model.DB}
	rec := seedDupRecord(t, bilingualGroups(2))
	data := func(g, act string) string { return "d:" + itoa(rec.ID) + ":" + g + ":" + act }

	text, buttons := h.dupTGCallback(data("0", "k1"))
	if buttons == nil || !strings.Contains(text, "✓ 只留「英语」") || len(dupChoiceOf(t, rec.ID)) != 0 {
		t.Fatalf("点了一组：%s", text)
	}
	text, buttons = h.dupTGCallback(data("1", "a"))
	if buttons != nil || !strings.Contains(text, "已加入任务队列") {
		t.Fatalf("点全了应提交：%s", text)
	}
	c := dupChoiceOf(t, rec.ID)
	if c["S04E21-eng"] != dupChoiceKeep || c["S04E21-yue"] != dupChoiceDrop || c["S04E22-eng"] != "A" || c["S04E22-yue"] != "B" {
		t.Fatalf("选择：%v", c)
	}
	if text, _ := h.dupTGCallback(data("0", "k0")); !strings.Contains(text, "已经选过了") {
		t.Fatalf("先到先得：%s", text)
	}
	if text, _ := h.dupTGCallback("d:999:0:a"); !strings.Contains(text, "处理过了") {
		t.Fatalf("不存在的记录：%s", text)
	}
}

// 批量按钮与文字指令；网页可以改选，机器人不能盖掉已有的选择
func TestDupBotCommandAndOverride(t *testing.T) {
	newTestDB(t, "dup_cmd.db")
	h := &Handler{DB: model.DB}
	rec := seedDupRecord(t, bilingualGroups(2))

	if lines := h.dupBotCommand("wecom-user", ""); !strings.Contains(strings.Join(lines, "\n"), "#"+itoa(rec.ID)) {
		t.Fatalf("列出待选：%v", lines)
	}
	if lines := h.dupBotCommand("wecom-user", itoa(rec.ID)); !strings.Contains(strings.Join(lines, "\n"), "多份 "+itoa(rec.ID)+" 粤语") {
		t.Fatalf("详情带用法：%v", lines)
	}
	if lines := h.dupBotCommand("wecom-user", itoa(rec.ID)+" 国语"); !strings.HasPrefix(lines[0], "✗") {
		t.Fatalf("认不出的选择：%v", lines)
	}
	lines := h.dupBotCommand("tg:1:1", itoa(rec.ID)+" 英语")
	if !strings.HasPrefix(lines[0], "✓") {
		t.Fatalf("提交：%v", lines)
	}
	var job model.TaskJob
	model.DB.Order("id desc").First(&job)
	if job.Kind != "confirm" || job.Source != "tg" {
		t.Fatalf("任务：%+v", job)
	}
	if lines := h.dupBotCommand("wecom-user", itoa(rec.ID)+" 全留"); !strings.Contains(lines[0], "已经选过了") {
		t.Fatalf("机器人不能盖掉：%v", lines)
	}
	var fresh model.OrganizeRecord
	model.DB.First(&fresh, rec.ID)
	if _, err := h.submitDupChoice(&fresh, []dupAction{{Action: "keep_all"}, {Action: "keep_all"}}, "web", true); err != nil {
		t.Fatalf("网页可以改选：%v", err)
	}
	if dupChoiceOf(t, rec.ID)["S04E21-eng"] != "A" {
		t.Fatal("改选没生效")
	}
}

// 超时：默认不处理；到点按设置处理，有推荐留推荐、没推荐都留
func TestDupAutoTick(t *testing.T) {
	newTestDB(t, "dup_auto.db")
	h := &Handler{DB: model.DB}
	gs := bilingualGroups(1)
	gs[0].Recommend = gs[0].Files[1].Fid
	old := seedDupRecord(t, gs)
	fresh := seedDupRecord(t, bilingualGroups(1))
	model.DB.Model(&model.OrganizeRecord{}).Where("id = ?", old.ID).Update("created_at", time.Now().Add(-5*time.Hour))

	h.dupAutoTick()
	if len(dupChoiceOf(t, old.ID)) != 0 {
		t.Fatal("没配超时不该处理")
	}
	model.DB.Save(&model.Setting{Key: "org-basic", Value: `{"dup_auto_hours":4,"dup_auto_action":"recommend"}`})
	h.dupAutoTick()
	if c := dupChoiceOf(t, old.ID); c["S04E21-eng"] != dupChoiceKeep || c["S04E21-yue"] != dupChoiceDrop {
		t.Fatalf("到点留推荐：%v", c)
	}
	if len(dupChoiceOf(t, fresh.ID)) != 0 {
		t.Fatal("没到点的不该处理")
	}
	if p := dupAutoPicks(bilingualGroups(1), "recommend"); p[0] != "a" {
		t.Fatalf("没推荐就都留：%v", p)
	}
}

func itoa(n uint) string { return fmt.Sprint(n) }
