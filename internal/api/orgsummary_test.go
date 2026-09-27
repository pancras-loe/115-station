package api

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"115-station/internal/model"
)

// 两个整理入口共用同一段收尾：完成行格式一致，入库片单一条都不能少。
// 2026-09-21 洗版验证的日志里，同一轮整理的两条链路打出了两种格式
func TestFinishOrganizeSummary(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	results := []OrganizeResult{
		{Status: "success", Title: "美国队长", Year: "2011", TmdbID: 1771,
			TargetDir: "电影/美国队长.2011.{tmdbid=1771}"},
		{Status: "exists", Title: "美国队长", Year: "2011", TmdbID: 1771,
			TargetDir: "电影/美国队长.2011.{tmdbid=1771}"},
	}
	finishOrganize(&orgSink{}, results, time.Now())

	out := buf.String()
	if !strings.Contains(out, "本次入库 1 部") {
		t.Fatalf("没打入库片单: %s", out)
	}
	if !strings.Contains(out, "整理完成（耗时") ||
		!strings.Contains(out, "成功 1（生成 STRM 0）· 已存在 1 · 失败 0") {
		t.Fatalf("完成行格式不对: %s", out)
	}
}

// 空转静默：一条结果都没有时不打完成汇总（定时任务每 10 分钟一轮）
func TestFinishOrganizeSilentWhenIdle(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	finishOrganize(&orgSink{}, nil, time.Now())

	if out := buf.String(); strings.Contains(out, "整理完成") {
		t.Fatalf("空转不该打完成汇总: %s", out)
	}
}

// 没入库的结果要推一条汇总：此前只有入库卡片，全判为已存在时用户收不到任何下文。
// 同一条失败在静默期内不重复推（定时整理每一轮都会再失败一次）
func TestOrgOutcomeNotifyText(t *testing.T) {
	now := time.Now()
	recs := []*model.OrganizeRecord{
		{Status: "success", SourceFid: "s1", Title: "入库的"},
		{Status: "exists", SourceFid: "d1", Title: "武林外传", Year: "2006",
			Message: "库内已有同一份文件（sha1 相同），80 个视频已移到 已存在/武林外传"},
		{Status: "failed", SourceFid: "d2", Source: "某目录/", Message: "移到已存在失败"},
	}
	text := orgOutcomeNotifyText(recs, now)
	if strings.Contains(text, "入库的") {
		t.Fatalf("入库成功另有卡片，不该再列: %s", text)
	}
	if !strings.Contains(text, "○ 已存在 · 武林外传 (2006)") || !strings.Contains(text, "80 个视频") ||
		!strings.Contains(text, "✗ 失败 · 某目录") {
		t.Fatalf("汇总内容不对: %s", text)
	}
	if again := orgOutcomeNotifyText(recs[2:], now.Add(time.Hour)); again != "" {
		t.Fatalf("静默期内同一条失败不该重复推: %s", again)
	}
	if later := orgOutcomeNotifyText(recs[2:], now.Add(orgOutcomeQuiet+time.Minute)); later == "" {
		t.Fatal("过了静默期应当再推")
	}
	if none := orgOutcomeNotifyText(recs[:1], now); none != "" {
		t.Fatalf("全部成功时不推: %s", none)
	}
}
