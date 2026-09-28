package api

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"115-station/internal/model"
)

// 临时失败（TMDB 不可达）重试时写回同一条记录，成功后也接管那一条；其他失败各留各的
func TestNoteReusesRetryLeftover(t *testing.T) {
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
	if count("f1") != 1 || rec.Status != "success" || rec.CreatedAt.IsZero() {
		t.Fatalf("成功应写回临时失败那条: n=%d rec=%+v", count("f1"), rec)
	}

	// 搬移失败不是原地重试，不合并
	s.noteFail("b.mkv", "f2", "file", "failed", "move", "移到冗余失败", nil)
	s.noteFail("b.mkv", "f2", "file", "failed", "move", "移到冗余失败", nil)
	if n := count("f2"); n != 2 {
		t.Fatalf("搬移失败不应合并，实际 %d", n)
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
