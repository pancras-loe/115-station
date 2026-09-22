package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 2026-09-22 游戏王 S01E153 事故的三处回归。
// 一集离线下载进来，115 落地的文件名把空格吃掉了
// （"(DVD 960x720 AVC AAC)" → "(DVD960x720AVCAAC)"），由此连环翻车：
// 编码解析不出来、重整理丢原名、Emby 集数不涨。

// 粘连写法：AVC 后面紧跟 A、AAC 前面紧跟 C，\b 边界全部失配，
// 改出来的名字比同一部剧其他集少了 ".H264.AAC"
func TestResourceParseGluedTokens(t *testing.T) {
	glued := ParseResourceInfo("Yu-Gi-Oh!DuelMonsters-S01E153(DVD960x720AVCAAC).mkv")
	spaced := ParseResourceInfo("Yu-Gi-Oh! Duel Monsters - S01E152 (DVD 960x720 AVC AAC).mkv")
	if glued.VideoEncode != spaced.VideoEncode || glued.AudioEncode != spaced.AudioEncode {
		t.Fatalf("粘连写法与带空格写法解析结果不一致: %+v vs %+v", glued, spaced)
	}
	if glued.VideoEncode != "H264" || glued.AudioEncode != "AAC" {
		t.Fatalf("粘连写法没解析出编码: video=%q audio=%q", glued.VideoEncode, glued.AudioEncode)
	}
}

// 补分隔符只能发生在技术块里。片名里的英文单词不含数字，
// 拆错了就会给《Isaac》凭空加一个 AAC、给《Peacock》加一个 CR
func TestUnglueTechTokensLeavesTitlesAlone(t *testing.T) {
	for _, name := range []string{
		"Isaac.mkv",
		"The.Peacock.Murders.mkv",
		"ISAAC ASIMOV.mkv",
	} {
		if got := unglueTechTokens(name); got != name {
			t.Errorf("片名被拆了: %q → %q", name, got)
		}
		if ri := ParseResourceInfo(name); ri.AudioEncode != "" || ri.VideoEncode != "" {
			t.Errorf("%q 解析出了不存在的编码: %+v", name, ri)
		}
	}
}

// 剧集的原名字段是 original_name。漏了这条回退，「重新整理」按 id 拉详情时
// {en_title} 恒为空 —— 自动整理（走搜索接口）改出 "游戏王GX.遊戯王…GX.S01E153"，
// 重整理（走详情接口）改出 "游戏王：怪兽之决斗.S01E153"，同一部剧两套命名
func TestGetByTmdbIDFillsTVOriginalName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/902":
			_, _ = w.Write([]byte(`{"id":902,"name":"游戏王：怪兽之决斗","original_name":"遊戯王デュエルモンスターズ","first_air_date":"2000-04-18"}`))
		case "/movie/1771":
			_, _ = w.Write([]byte(`{"id":1771,"title":"美国队长","original_title":"Captain America","release_date":"2011-07-22"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	tc := &TmdbClient{APIKey: "k", APIURL: srv.URL, httpClient: srv.Client()}

	tv, err := tc.getByTmdbID(902, true)
	if err != nil || tv == nil {
		t.Fatalf("拉剧集详情失败: %v", err)
	}
	if tv.OriginalTitle != "遊戯王デュエルモンスターズ" {
		t.Fatalf("剧集原名丢了: %q", tv.OriginalTitle)
	}
	movie, err := tc.getByTmdbID(1771, false)
	if err != nil || movie == nil {
		t.Fatalf("拉电影详情失败: %v", err)
	}
	if movie.OriginalTitle != "Captain America" {
		t.Fatalf("电影原名被回退逻辑带偏了: %q", movie.OriginalTitle)
	}
}

// 重整理拿的是记录里登记的**原名**来算模板变量。
// 拿改过的名字再 parse 一遍，上一次没写进文件名的画质/编码就永远回不来
func TestPlanRedoLayoutUsesOriginalName(t *testing.T) {
	ensureTestRenameTpl(t)
	media := &TmdbMedia{TmdbID: 902, Title: "游戏王", Year: "2000", MediaType: "tv"}

	withOrig := []orgRecordFile{{Fid: "v1", Kind: "video",
		Name: "游戏王.S01E153.mkv", Orig: "Yu-Gi-Oh!DuelMonsters-S01E153(DVD960x720AVCAAC).mkv"}}
	plan, err := planRedoLayout(media, "动漫番剧", withOrig, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.renames["v1"]; got != "游戏王.S01E153.H264.AAC.mkv" {
		t.Fatalf("没有按原名补回编码: %q", got)
	}

	// 老记录没有 Orig：单视频记录还能从 rec.Source 捞回原名
	legacy := []orgRecordFile{{Fid: "v1", Kind: "video", Name: "游戏王.S01E153.mkv"}}
	plan, err = planRedoLayout(media, "动漫番剧", legacy,
		"Yu-Gi-Oh!DuelMonsters-S01E153(DVD960x720AVCAAC).mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.renames["v1"]; got != "游戏王.S01E153.H264.AAC.mkv" {
		t.Fatalf("老记录没从 Source 捞回原名: %q", got)
	}

	// 目录整理的 Source 是目录名，不能当文件名用
	plan, err = planRedoLayout(media, "动漫番剧", legacy, "Yu-Gi-Oh! Duel Monsters (DVD AVC AAC)/")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.renames["v1"]; got != "" {
		t.Fatalf("目录名被当成原文件名用了: %q", got)
	}
}

// ensureTestRenameTpl 用默认模板（含编码变量）跑重整理路径
func ensureTestRenameTpl(t *testing.T) {
	t.Helper()
	prev := renameTpl
	t.Cleanup(func() { renameTpl = prev })
	renameTpl = &RenameConfig{
		TVFolder: "{title}.{year}/Season {season_num}",
		TVFile:   "{title}.{season_episode}<.{video_encode}><.{audio_encode}>{ext}",
	}
}

// 新增场景：路径上已经是 Series 条目也不能只刷它。
// 刷新跑的是 ValidateChildren（复核已知子条目还在不在），发现不了刚落进
// 季目录里的新一集 —— 游戏王那一集就是这么丢的：提交成功、集数不动
func TestNotifyEmbyRefreshFallsBackToLibraryForSeries(t *testing.T) {
	for _, typ := range []string{"Series", "Season"} {
		t.Run(typ, func(t *testing.T) {
			root := t.TempDir()
			f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
			f.itemType = typ
			setupEmbyRefreshCfg(t, f.srv.URL, root)

			dir := filepath.Join(root, "动漫番剧", "游戏王：怪兽之决斗.2000.{tmdbid=902}")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			(&Handler{}).notifyEmbyRefresh(dir)

			if f.sawHit("POST /Items/item9/Refresh") {
				t.Fatalf("不该只刷 %s 条目（刷了也发现不了新的一集）: %v", typ, f.hits)
			}
			if !f.sawHit("POST /Items/lib1/Refresh") {
				t.Fatalf("没有退到媒体库刷新，实际请求: %v", f.hits)
			}
		})
	}
}

// 入库回查要查真正落盘的 .strm，不能查标题目录：
// 标题目录上早就挂着 Series 条目，拿它回查等于自问自答，
// 那一集没进库也照样打 "✓ 入库确认"
func TestEmbyVerifyChecksLandedFileNotTitleDir(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	f.itemType = "Series"
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	dir := filepath.Join(root, "动漫番剧", "游戏王：怪兽之决斗.2000.{tmdbid=902}")
	strm := filepath.Join(dir, "Season 1", "游戏王：怪兽之决斗.S01E153.mkv.strm")
	if err := os.MkdirAll(filepath.Dir(strm), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := embyVerifyDelays
	embyVerifyDelays = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { embyVerifyDelays = prev })

	notifyEmbyPaths([]string{dir}, embyRefreshAdded, strm)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		hit := containsStr(f.itemPathQ, filepath.ToSlash(strm))
		f.mu.Unlock()
		if hit {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("回查没查落盘文件，查的是: %v", f.itemPathQ)
}
