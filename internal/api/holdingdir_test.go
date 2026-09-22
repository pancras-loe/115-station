package api

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type holdingOpsRecorder struct {
	ensureRoot string
	ensureRel  string
	movedCid   string
	movedFids  []string
	ensureErr  error
	moveErr    error
}

func (o *holdingOpsRecorder) ensurePath(root, rel string) (string, error) {
	o.ensureRoot, o.ensureRel = root, rel
	if o.ensureErr != nil {
		return "", o.ensureErr
	}
	return "holding-cid", nil
}

func (o *holdingOpsRecorder) moveFiles(cid string, fids []string) error {
	o.movedCid = cid
	o.movedFids = append([]string(nil), fids...)
	return o.moveErr
}

func TestMoveToHoldingDirCreatesThenMovesBatch(t *testing.T) {
	ops := &holdingOpsRecorder{}
	cid, err := moveToHoldingDir(ops, "existing-root", "某剧.2026.[tmdbid=42]", []string{"e1", "e2", "e3"})
	if err != nil {
		t.Fatal(err)
	}
	if cid != "holding-cid" || ops.ensureRoot != "existing-root" || ops.ensureRel != "某剧.2026.[tmdbid=42]" {
		t.Fatalf("归档目录不对: cid=%q root=%q rel=%q", cid, ops.ensureRoot, ops.ensureRel)
	}
	if ops.movedCid != "holding-cid" || !reflect.DeepEqual(ops.movedFids, []string{"e1", "e2", "e3"}) {
		t.Fatalf("应整批移动到新目录，实际 cid=%q fids=%v", ops.movedCid, ops.movedFids)
	}
}

func TestMoveToHoldingDirDoesNotFallBackToWorkspaceRoot(t *testing.T) {
	ops := &holdingOpsRecorder{ensureErr: errors.New("mkdir failed")}
	if _, err := moveToHoldingDir(ops, "redundant-root", "某剧", []string{"e1"}); err == nil {
		t.Fatal("建隔离目录失败时应报错并把文件留在原处")
	}
	if ops.movedCid != "" || len(ops.movedFids) != 0 {
		t.Fatalf("不应退回工作区根目录堆放，实际 cid=%q fids=%v", ops.movedCid, ops.movedFids)
	}
}

func TestRecognizedHoldingDirUsesTitleTemplateWithoutSeason(t *testing.T) {
	saved := renameTpl
	renameTpl = defaultRenameConfig()
	defer func() { renameTpl = saved }()

	media := &TmdbMedia{Title: "某剧", Year: "2026", MediaType: "tv", TmdbID: 42}
	parsed := &ParsedName{Season: 2, Episode: 3}
	got := recognizedHoldingDir(media, parsed, "Show.S02E03.mkv")
	if !strings.Contains(got, "某剧.2026") || !strings.Contains(got, "tmdbid=42") {
		t.Fatalf("应复用已识别片目的标题目录，实际 %q", got)
	}
	if strings.Contains(strings.ToLower(got), "season") {
		t.Fatalf("隔离目录只取片目根，不能拆成逐季写请求: %q", got)
	}
}

func TestSourceHoldingDirGroupsLooseEpisodes(t *testing.T) {
	if got := sourceHoldingDir("Show.Name.S01E02.1080p.mkv", ""); got != "Show.Name" {
		t.Fatalf("散落剧集应按原始系列前缀归组，实际 %q", got)
	}
	if got := sourceHoldingDir("Movie.2026.1080p.mkv", "电影名"); got != "电影名" {
		t.Fatalf("没有剧集前缀时应使用解析标题，实际 %q", got)
	}
}
