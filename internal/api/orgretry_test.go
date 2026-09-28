package api

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 临时失败（TMDB 不可达）重试只留最新一条，成功后旧的失败行清掉；其他失败各留各的
func TestNoteDropsRetryLeftovers(t *testing.T) {
	if _, err := model.InitDB("file:orgretry_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM organize_records")
		model.DB = nil
	})
	s := &orgSink{}
	count := func(fid string) int64 {
		var n int64
		model.DB.Model(&model.OrganizeRecord{}).Where("source_fid = ?", fid).Count(&n)
		return n
	}

	s.noteFail("a.mkv", "f1", "file", "failed", "recognize", "TMDB 暂时不可达", nil)
	s.noteFail("a.mkv", "f1", "file", "failed", "recognize", "TMDB 暂时不可达", nil)
	if n := count("f1"); n != 1 {
		t.Fatalf("重试两轮应只留 1 条，实际 %d", n)
	}
	s.note(&model.OrganizeRecord{Source: "a.mkv", SourceFid: "f1", SourceKind: "file", Status: "success", Title: "欢乐好声音2"})
	var rec model.OrganizeRecord
	model.DB.Where("source_fid = ?", "f1").Take(&rec)
	if count("f1") != 1 || rec.Status != "success" {
		t.Fatalf("成功后应只剩成功那条: n=%d rec=%+v", count("f1"), rec)
	}

	// 搬移失败不是原地重试，不合并
	s.noteFail("b.mkv", "f2", "file", "failed", "move", "移到冗余失败", nil)
	s.noteFail("b.mkv", "f2", "file", "failed", "move", "移到冗余失败", nil)
	if n := count("f2"); n != 2 {
		t.Fatalf("搬移失败不应合并，实际 %d", n)
	}
}

// 存量：旧版本已经刷出来的两条失败 + 重新整理把其中一条改成成功（现场：欢乐好声音2）
func TestSweepRetryLeftovers(t *testing.T) {
	if _, err := model.InitDB("file:orgsweep_test?mode=memory&cache=shared"); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM organize_records")
		model.DB = nil
	})
	rows := []model.OrganizeRecord{
		{SourceFid: "f1", Status: "success"}, // 第一条失败被重新整理改成了成功
		{SourceFid: "f1", Status: "failed", Stage: "recognize"},
		{SourceFid: "f2", Status: "failed", Stage: "recognize"}, // 仍是最新，要留着
		{SourceFid: "f3", Status: "failed", Stage: "move"},
		{SourceFid: "f3", Status: "success"},
	}
	for i := range rows {
		model.DB.Create(&rows[i])
	}
	// 重新整理在 id 更小的那条上落成功，sweep 也要认：比较的是「有没有别的记录」而不只是更新的
	sweepRetryLeftovers(model.DB)
	var left []model.OrganizeRecord
	model.DB.Order("id").Find(&left)
	if len(left) != 4 {
		t.Fatalf("应只删 f1 的失败行，剩 %d: %+v", len(left), left)
	}
	for _, r := range left {
		if r.SourceFid == "f1" && r.Status != "success" {
			t.Fatalf("f1 的失败行没删: %+v", r)
		}
	}
}

func TestRedactTmdbErr(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://api.themoviedb.org/3/search/movie?api_key=secret123&query=x", Err: io.EOF}
	got := redactTmdbErr(err).Error()
	if strings.Contains(got, "secret123") || !strings.Contains(got, "EOF") {
		t.Fatalf("api_key 未抹掉或丢了原因: %s", got)
	}
	plain := errors.New("x")
	if redactTmdbErr(plain) != plain {
		t.Fatal("非 url.Error 应原样返回")
	}
}
