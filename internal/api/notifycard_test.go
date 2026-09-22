package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetMediaNotifQueue 清空入库卡片队列（定时器留着会在测试结束后乱发）
func resetMediaNotifQueue(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		mediaNotif.mu.Lock()
		defer mediaNotif.mu.Unlock()
		if mediaNotif.timer != nil {
			mediaNotif.timer.Stop()
		}
		mediaNotif.items, mediaNotif.timer, mediaNotif.firstAt = nil, nil, time.Time{}
		mediaNotif.sent = nil
	})
	mediaNotif.mu.Lock()
	if mediaNotif.timer != nil {
		mediaNotif.timer.Stop()
	}
	mediaNotif.items, mediaNotif.timer, mediaNotif.firstAt = nil, nil, time.Time{}
	mediaNotif.sent = nil
	mediaNotif.mu.Unlock()
}

func mediaNotifSnapshot() []mediaNotifEntry {
	mediaNotif.mu.Lock()
	defer mediaNotif.mu.Unlock()
	return append([]mediaNotifEntry(nil), mediaNotif.items...)
}

// 一次入库两个来源（整理落盘 + Emby 扫描完成）合成同一张卡片。
// 各发各的时候用户一次入库要收两条几乎一样的消息
func TestQueueMediaNotifMergesBothSources(t *testing.T) {
	resetMediaNotifQueue(t)

	QueueMediaNotif(mediaNotifEntry{
		Title: "美国队长", Year: "2011", Kind: "电影", Category: "电影",
		Quality: "2160P HDR", Files: "1 个文件 · 27.4 GB",
		Notes:     []string{"♻️ 洗版替换 1080P → 2160P HDR（旧版已移到115 回收站）"},
		PosterURL: "https://image.tmdb.org/x.jpg",
	})
	QueueMediaNotif(mediaNotifEntry{
		Title: "美国队长", Year: "2011", Kind: "电影", Rating: 7.0,
		PosterData: []byte("emby-poster"), Link: "http://emby/web/index.html#!/item?id=1",
	})

	items := mediaNotifSnapshot()
	if len(items) != 1 {
		t.Fatalf("同一部片应当合成一张卡片，实际 %d 张: %+v", len(items), items)
	}
	e := items[0]
	if e.Rating != 7.0 || e.Quality != "2160P HDR" || e.Files == "" {
		t.Fatalf("两边的字段没合全: %+v", e)
	}
	if string(e.PosterData) != "emby-poster" || e.Link == "" {
		t.Fatalf("封面/详情链接应当用 Emby 那份: %+v", e)
	}
	body := e.body()
	for _, want := range []string{"🎬 电影", "⭐ 7.0", "📀 2160P HDR", "📦 1 个文件", "♻️ 洗版替换"} {
		if !strings.Contains(body, want) {
			t.Fatalf("卡片正文缺 %q:\n%s", want, body)
		}
	}
	if e.headline() != "美国队长（2011）" {
		t.Fatalf("卡片标题不对: %s", e.headline())
	}
}

// 不同影视不能被合并；剧集按片名合并（Emby 单集事件给的是这一集的年份）
func TestMediaNotifMergeKey(t *testing.T) {
	movie := mediaNotifEntry{Title: "美国队长", Year: "2011", Kind: "电影"}
	remake := mediaNotifEntry{Title: "美国队长", Year: "2021", Kind: "电影"}
	if movie.mergeKey() == remake.mergeKey() {
		t.Fatal("同名不同年的电影被合并了")
	}
	tvOrganize := mediaNotifEntry{Title: "权力的游戏", Year: "2011", Kind: "剧集"}
	tvEmby := mediaNotifEntry{Title: "权力的游戏", Year: "2019", Kind: "剧集"}
	if tvOrganize.mergeKey() != tvEmby.mergeKey() {
		t.Fatal("剧集应当按片名合并，年份不参与")
	}
	if (mediaNotifEntry{}).mergeKey() != "" {
		t.Fatal("没有片名就不该有合并键")
	}
}

// 整理通知已经发出后，Emby 晚到的同片 webhook 不能再发第二次；但一次新的
// 整理动作仍然要能通知（例如同一部剧稍后追加新集）。
func TestQueueMediaNotifSuppressesLateEmbyOnly(t *testing.T) {
	resetMediaNotifQueue(t)
	key := (mediaNotifEntry{Title: "游戏王 朝日版", Kind: "剧集"}).mergeKey()
	mediaNotif.mu.Lock()
	mediaNotif.sent = map[string]time.Time{key: time.Now()}
	mediaNotif.mu.Unlock()

	QueueMediaNotif(mediaNotifEntry{Title: "游戏王 朝日版", Kind: "剧集", Source: "emby"})
	if got := len(mediaNotifSnapshot()); got != 0 {
		t.Fatalf("迟到的 Emby 回声又入队了: %d", got)
	}
	QueueMediaNotif(mediaNotifEntry{Title: "游戏王 朝日版", Kind: "剧集", Source: "organize"})
	if got := len(mediaNotifSnapshot()); got != 1 {
		t.Fatalf("新的整理动作被短期去重误吞: %d", got)
	}
}

func TestManagedMediaPathGone(t *testing.T) {
	root := t.TempDir()
	alive := filepath.Join(root, "影视", "动漫番剧", "正确条目")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	if managedMediaPathGone(root, alive) {
		t.Fatal("仍存在的入库路径被判成过期")
	}
	if !managedMediaPathGone(root, filepath.Join(root, "影视", "动漫番剧", "已删除旧条目")) {
		t.Fatal("媒体根内已经不存在的旧路径没有被拦截")
	}
	if managedMediaPathGone(root, filepath.Join(t.TempDir(), "外部媒体库")) {
		t.Fatal("媒体根外的路径不能按本站文件系统状态判断")
	}
}

// 空字段不占行：认不出画质/集数时卡片不留空行，也不写「未知」
func TestMediaNotifLinesSkipEmptyFields(t *testing.T) {
	e := mediaNotifEntry{Title: "某片", Kind: "电影"}
	if got := e.lines(); len(got) != 1 || got[0] != "🎬 电影" {
		t.Fatalf("空字段没被跳过: %q", got)
	}
}

// 洗版说明挂到这部片的卡片上；被认领之后不会再发第二次
func TestWashNoteAttachesToCardOnce(t *testing.T) {
	media := &TmdbMedia{Title: "美国队长", Year: "2011", MediaType: "movie"}
	noteWashReplace(media,
		"美国队长.Captain America.2011.1080p.H264.mkv",
		"美国队长.Captain America.2011.HDR.2160p.H265.mkv", "115 回收站")

	key := washNoteKeyOf(media)
	notes := washNotesFor(key)
	if len(notes) != 1 {
		t.Fatalf("洗版说明没登记上: %v", notes)
	}
	for _, want := range []string{"1080P", "2160P HDR", "115 回收站"} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("洗版说明缺 %q: %s", want, notes[0])
		}
	}
	if left := washNotesFor(key); len(left) != 0 {
		t.Fatalf("认领过的说明还留着，会被兜底再发一遍: %v", left)
	}
}

// 整季逐集替换时对比文字一模一样，合成一行带次数，别在卡片上摞 12 行
func TestWashNoteCollapsesRepeats(t *testing.T) {
	media := &TmdbMedia{Title: "某剧", Year: "2025", MediaType: "tv"}
	for i := 0; i < 12; i++ {
		noteWashReplace(media, "某剧.S01E01.1080p.WEB-DL.mkv", "某剧.S01E01.2160p.HDR.WEB-DL.mkv", "冗余/洗版-旧版本/某剧")
	}
	notes := washNotesFor(washNoteKeyOf(media))
	if len(notes) != 1 || !strings.HasSuffix(notes[0], "×12") {
		t.Fatalf("重复的洗版说明没合并: %v", notes)
	}
}

// 本站自己删的条目绕回来的 library.deleted 不算「有人删了片子」
func TestEmbySelfDeletedMark(t *testing.T) {
	p := "/media/影视/电影/美国队长.2011.{tmdbid=1771}/a.mkv.strm"
	if embySelfDeleted(p) {
		t.Fatal("没标记过就不该命中")
	}
	markEmbySelfDeleted(p)
	if !embySelfDeleted(p) {
		t.Fatal("标记过的路径没命中")
	}
	// Emby 侧配成 windows 风格时事件里回来的是反斜杠路径
	if !embySelfDeleted(strings.ReplaceAll(p, "/", "\\")) {
		t.Fatal("路径风格不同就认不出来了")
	}
	if embySelfDeleted("/media/影视/电影/别的片/b.mkv.strm") {
		t.Fatal("别的路径不该被抑制")
	}
}

// resetEmbyChangeMarks 清掉包级的改名/新增标记，避免用例之间互相污染
func resetEmbyChangeMarks() {
	embyChangeMu.Lock()
	defer embyChangeMu.Unlock()
	embyRenamedAt = map[string]time.Time{}
	embyFreshAt = map[string]time.Time{}
}

// 库内改名的回声不算入库；同窗口内这条路径下真有新增时照常通知
func TestEmbyRenameEchoMark(t *testing.T) {
	resetEmbyChangeMarks()
	strm := "/media/影视/剧集/某剧.2025/Season 1/某剧.S01E02.mkv.strm"
	season := "/media/影视/剧集/某剧.2025/Season 1"
	if embyRenameEcho(strm) {
		t.Fatal("没标记过就不该命中")
	}
	markEmbyRenamed(strm)
	if !embyRenameEcho(strm) || !embyRenameEcho(season) {
		t.Fatal("改名路径与它所在的季条目都该算回声")
	}
	// Emby 侧配成 windows 风格时事件里回来的是反斜杠路径
	if !embyRenameEcho(strings.ReplaceAll(strm, "/", "\\")) {
		t.Fatal("路径风格不同就认不出来了")
	}
	if embyRenameEcho("/media/影视/剧集/别的剧.2025") {
		t.Fatal("别的路径不该被抑制")
	}
	markEmbyFreshAdded(season + "/某剧.S01E03.mkv.strm")
	if embyRenameEcho(season) {
		t.Fatal("同一季真的来了新集，季/剧集条目的入库通知不能被吞")
	}
	if !embyRenameEcho(strm) {
		t.Fatal("改名的那条路径本身仍是回声")
	}
}
