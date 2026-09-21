package api

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
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
