package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"115-station/internal/config"
)

// fakeEmby 记录收到的每一条请求，用来断言我们打的是哪个端点。
// 这一层是这组测试的重点：坏掉的 /Library/{Id}/Refresh 在真实 Emby 上是 404，
// 而旧代码不看状态码，本地怎么测都"成功"
type fakeEmby struct {
	mu        sync.Mutex
	hits      []string // "METHOD /path"
	itemPathQ []string // /Items 查询用的 Path 参数
	updates   []map[string]string
	deleted   []string         // 收到的删条目请求（条目 id）
	libs      []map[string]any // /Library/VirtualFolders/Query 返回的虚拟库（含实际目录）
	lookupHit bool             // /Items 是否返回匹配条目
	deleteOK  bool             // 删条目是否放行；false = 模拟没开「允许删除媒体」
	dupItems  bool             // 同一路径回两个条目（Folder 与刮削出的 Movie 并存）
	hitPath   string           // 非空时只有这个路径能查到条目（模拟 Emby 还没给新目录建条目）
	srv       *httptest.Server
}

func newFakeEmby(t *testing.T, locations []string, lookupHit bool) *fakeEmby {
	return newFakeEmbyLibs(t, []map[string]any{
		{"ItemId": "lib1", "Name": "电影", "Locations": locations},
	}, lookupHit)
}

// newFakeEmbyLibs 多库版。二级分类目录各建一个 Emby 库是常见摆法，
// 单库的假服务器测不出「刷错库」——命中哪个库都只有 lib1 一个答案
func newFakeEmbyLibs(t *testing.T, libs []map[string]any, lookupHit bool) *fakeEmby {
	t.Helper()
	f := &fakeEmby{libs: libs, lookupHit: lookupHit}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.Path)
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Library/MediaFolders":
			// 此接口只有条目，不能伪造 Locations 来掩盖生产代码的接口错误。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{"Id": "lib1", "Name": "电影", "Path": "/config/root/default/电影"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/VirtualFolders/Query":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": f.libs})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			p := r.URL.Query().Get("Path")
			f.mu.Lock()
			f.itemPathQ = append(f.itemPathQ, p)
			f.mu.Unlock()
			items := []map[string]any{}
			if f.lookupHit && (f.hitPath == "" || f.hitPath == p) {
				// 回显请求路径：调用方会再比对一次完整路径才认
				items = append(items, map[string]any{"Id": "item9", "Name": "某片", "Path": p})
				if f.dupItems {
					items = append(items, map[string]any{"Id": "item10", "Name": "某片", "Path": p})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Refresh"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/Items/"):
			if !f.deleteOK {
				// 401 = API 密钥对应的用户没勾「允许删除媒体」，真实 Emby 的回法
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.mu.Lock()
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/Items/"))
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Library/Media/Updated":
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Updates []map[string]string `json:"Updates"`
			}
			_ = json.Unmarshal(body, &payload)
			f.mu.Lock()
			f.updates = append(f.updates, payload.Updates...)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEmby) sawHit(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if h == want {
			return true
		}
	}
	return false
}

// setupEmbyRefreshCfg 把 emby / full / emby-notify 三张卡配好。
// root 由调用方先建好再传进来：假 Emby 的 Locations 也要用它，
// 先起服务器再改字段会踩数据竞争
func setupEmbyRefreshCfg(t *testing.T, serverURL, root string) {
	t.Helper()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	t.Cleanup(func() { notifyConfigSource = nil })

	save := func(key string, v any) {
		b, _ := json.Marshal(v)
		if err := notifyConfigSource.SaveSetting(key, string(b)); err != nil {
			t.Fatalf("保存配置 %s 失败: %v", key, err)
		}
	}
	// path_mapping 留空 = Emby 路径与本地路径一致，省掉映射这层干扰
	save("emby", map[string]any{"server_url": serverURL, "api_key": "k", "refresh_enabled": true})
	save("full", map[string]any{"local_path": root})
	// 配上 webhook，入库场景就不会再去走消息通道（那是另一条链路的事）
	save("emby-notify", map[string]any{"webhook": "http://x/api/emby/webhook?token=t"})
}

// 新增场景：必须打 /Items/{Id}/Refresh，绝不能打 /Library/{Id}/Refresh。
// 后者在 Emby 上没有注册路由，是这次修复的起点
func TestNotifyEmbyRefreshUsesItemsEndpoint(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	dir := filepath.Join(root, "电影", "某片 (2020)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	(&Handler{}).notifyEmbyRefresh(dir)

	if !f.sawHit("POST /Items/item9/Refresh") {
		t.Fatalf("没有打条目刷新端点，实际请求: %v", f.hits)
	}
	for _, h := range f.hits {
		if strings.HasPrefix(h, "POST /Library/") && strings.HasSuffix(h, "/Refresh") {
			t.Fatalf("又打了不存在的 /Library/{Id}/Refresh: %v", f.hits)
		}
	}
	if len(f.itemPathQ) != 1 || f.itemPathQ[0] != filepath.ToSlash(dir) {
		t.Fatalf("按路径查条目用的路径不对: %v", f.itemPathQ)
	}
}

// 条目定位不到时退回整库刷新（删整棵目录时父目录也可能刚被清掉）
func TestNotifyEmbyRefreshFallsBackToLibraryItem(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, false) // /Items 查不到
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	dir := filepath.Join(root, "电影")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	(&Handler{}).notifyEmbyRefresh(dir)

	if !f.sawHit("POST /Items/lib1/Refresh") {
		t.Fatalf("没有退回整库刷新，实际请求: %v", f.hits)
	}
}

// 删除场景：目标已经不在本地了，要上移到最近还存在的父目录再去找条目
func TestNotifyEmbyDeletedClimbsToExistingParent(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	// 电影/ 还在，电影/某片 (2020)/ 整个被删掉了
	alive := filepath.Join(root, "电影")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(alive, "某片 (2020)", "某片.mkv.strm")

	notifyEmbyDeleted(gone)

	// 先按被删路径试删条目（本例模拟 Emby 不放行），再上移到存活父目录刷新
	want := []string{filepath.ToSlash(gone), filepath.ToSlash(alive)}
	if len(f.itemPathQ) != 2 || f.itemPathQ[0] != want[0] || f.itemPathQ[1] != want[1] {
		t.Fatalf("删除应先试被删路径、再上移到存活父目录：期望 %v，实际 %v", want, f.itemPathQ)
	}
	if !f.sawHit("POST /Items/item9/Refresh") {
		t.Fatalf("没有提交刷新，实际请求: %v", f.hits)
	}
}

// 同一批删除会折叠成一个刷新目标：父目录一起没了的话它们的存活祖先是同一个
func TestNotifyEmbyDeletedDedupesTargets(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	alive := filepath.Join(root, "电影")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	notifyEmbyDeleted(
		filepath.Join(alive, "某片 (2020)", "a.mkv.strm"),
		filepath.Join(alive, "某片 (2020)", "b.mkv.strm"),
		filepath.Join(alive, "另一片 (2021)", "c.mkv.strm"),
	)
	// 三次删条目各查一次（本例模拟不放行），回退刷新时才折叠成一个目标
	if len(f.itemPathQ) != 4 || f.itemPathQ[3] != filepath.ToSlash(alive) {
		t.Fatalf("三个删除应折叠成一个刷新目标，实际 %v", f.itemPathQ)
	}
	refreshes := 0
	for _, h := range f.hits {
		if strings.HasSuffix(h, "/Refresh") {
			refreshes++
		}
	}
	if refreshes != 1 {
		t.Fatalf("只该提交一次刷新，实际 %v", f.hits)
	}
}

// 路径不属于任何媒体库时回退 /Library/Media/Updated，
// 且 UpdateType 要如实反映删除（此前写死 Created）
func TestNotifyEmbyDeletedFallbackUsesDeletedUpdateType(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{"/somewhere/else"}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	alive := filepath.Join(root, "电影")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(alive, "某片 (2020)", "a.mkv.strm")
	notifyEmbyDeleted(gone)

	if len(f.updates) != 1 {
		t.Fatalf("应回退路径通知，实际 updates=%v hits=%v", f.updates, f.hits)
	}
	if f.updates[0]["UpdateType"] != "Deleted" {
		t.Fatalf("UpdateType 应为 Deleted，实际 %v", f.updates[0])
	}
	// 报的必须是被删的那个路径。报上移后的 电影/ 等于告诉 Emby
	// 「整个电影目录没了」，它照着清库就是一场事故
	if f.updates[0]["Path"] != filepath.ToSlash(gone) {
		t.Fatalf("回退通知应报被删路径而不是存活父目录: %v", f.updates[0])
	}
}

// 媒体库根整个消失（挂载掉线）时一个请求都不该发：
// 这时候刷新只会让 Emby 把整个库清空
func TestNotifyEmbyDeletedSkipsWhenRootGone(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	notifyEmbyDeleted(filepath.Join(root, "电影", "某片 (2020)", "a.mkv.strm"))

	if len(f.hits) != 0 {
		t.Fatalf("挂载掉线时不应发任何请求，实际: %v", f.hits)
	}
}

// 新片目录 Emby 还没建条目时，沿父目录上溯找到库里已有的那一层就停，
// 不该直接跳到整库扫描（刷父目录同样能让 Emby 发现新文件）
func TestNotifyEmbyRefreshClimbsToKnownAncestor(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	known := filepath.Join(root, "电影")
	fresh := filepath.Join(known, "新片 (2026)")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	f.hitPath = filepath.ToSlash(known) // Emby 只认识 电影/，还没扫到 新片 (2026)/

	(&Handler{}).notifyEmbyRefresh(fresh)

	want := []string{filepath.ToSlash(fresh), filepath.ToSlash(known)}
	if len(f.itemPathQ) != 2 || f.itemPathQ[0] != want[0] || f.itemPathQ[1] != want[1] {
		t.Fatalf("上溯顺序不对：期望 %v，实际 %v", want, f.itemPathQ)
	}
	if !f.sawHit("POST /Items/item9/Refresh") {
		t.Fatalf("没有刷新上溯命中的条目，实际请求: %v", f.hits)
	}
	if f.sawHit("POST /Items/lib1/Refresh") {
		t.Fatalf("上溯已命中，不该再整库扫描: %v", f.hits)
	}
}

// 反向包含：全量同步传的是媒体库根，而库建在根下面第二层。
// 正向判断一个都不命中，得把根「盖住」的库都整库刷一遍
func TestNotifyEmbyRefreshCoversLibrariesBelowRoot(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "电影", "国产剧")
	f := newFakeEmby(t, []string{filepath.ToSlash(lib)}, true)
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}

	(&Handler{}).notifyEmbyRefresh(root)

	if !f.sawHit("POST /Items/lib1/Refresh") {
		t.Fatalf("媒体根之下的库没被刷新，实际请求: %v", f.hits)
	}
	if len(f.itemPathQ) != 0 {
		t.Fatalf("目标在库之上，没有更小的条目可刷，不该按路径查条目: %v", f.itemPathQ)
	}
	if len(f.updates) != 0 {
		t.Fatalf("已经命中媒体库，不该再走路径通知回退: %v", f.updates)
	}
}

func TestNearestExistingDir(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, in, want string
	}{
		{"文件在，返回它的目录", filepath.Join(deep, "x.strm"), deep},
		{"目录本身还在", deep, deep},
		{"逐级上移", filepath.Join(root, "a", "gone", "deeper", "x.strm"), filepath.Join(root, "a")},
		{"根自己", root, root},
		{"库外路径不处理", filepath.Join(t.TempDir(), "x.strm"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nearestExistingDir(c.in, root); got != c.want {
				t.Fatalf("nearestExistingDir(%q) = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}
}

func TestMapLocalToEmbyPath(t *testing.T) {
	root := t.TempDir()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	t.Cleanup(func() { notifyConfigSource = nil })
	b, _ := json.Marshal(map[string]any{"local_path": root})
	if err := notifyConfigSource.SaveSetting("full", string(b)); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(root, "电影", "某片.strm")
	if got := mapLocalToEmbyPath("", local); got != filepath.ToSlash(local) {
		t.Fatalf("映射没配时应原样返回: %q", got)
	}
	// path_mapping 形如 本地根#Emby挂载根，本地根以 full.local_path 为准
	if got := mapLocalToEmbyPath(root+"#/media", local); got != "/media/电影/某片.strm" {
		t.Fatalf("映射结果不对: %q", got)
	}
}

func TestEmbyMediaFoldersVirtualLibraryResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   []embyMediaFolder
	}{
		{"真实目录和条目标识", 200, `{"Items":[{"Id":"other-id","ItemId":"42","Name":"电影","Locations":["/media/影视/电影","/archive/电影"]}]}`,
			[]embyMediaFolder{{ID: "42", ItemID: "42", Name: "电影", Locations: []string{"/media/影视/电影", "/archive/电影"}}}},
		{"只有新版标识", 200, `{"Items":[{"Id":"43","Name":"剧集","Locations":["/media/影视/剧集"]}]}`,
			[]embyMediaFolder{{ID: "43", Name: "剧集", Locations: []string{"/media/影视/剧集"}}}},
		{"缺失标识不刷新", 200, `{"Items":[{"Name":"电影","Locations":["/media/影视/电影"]}]}`, []embyMediaFolder{}},
		{"鉴权失败", 401, `{}`, nil},
		{"响应损坏", 200, `{`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/Library/VirtualFolders/Query" {
					t.Errorf("查询了错误接口: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			got := embyMediaFolders(embyRefreshCfg{ServerURL: srv.URL, APIKey: "test"})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("媒体库解析错误: got=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestNotifyEmbyDeletedMappedMovieLibrary(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{"/media/影视/电影"}, false)
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	cfg, _ := json.Marshal(embyRefreshCfg{ServerURL: f.srv.URL, APIKey: "k", PathMapping: root + "#/media"})
	if err := notifyConfigSource.SaveSetting("emby", string(cfg)); err != nil {
		t.Fatal(err)
	}
	alive := filepath.Join(root, "影视", "电影")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	notifyEmbyDeleted(filepath.Join(alive, "海洋奇缘：启航.2026.{tmdbid=1108427}"))
	if !f.sawHit("POST /Items/lib1/Refresh") || len(f.updates) != 0 {
		t.Fatalf("删除应命中电影库刷新，不应回退路径通知: hits=%v updates=%v", f.hits, f.updates)
	}
	if len(f.itemPathQ) != 2 || f.itemPathQ[1] != "/media/影视/电影" {
		t.Fatalf("删除目标不应退回 /media: %v", f.itemPathQ)
	}
}

// 用户最常见的摆法：媒体库建在「映射目录 / 一级分类 / 二级分类」这一层，
// 每个二级目录各是一个 Emby 库。入库要精确落到命中的那个库，
// 既不能整站乱刷，也不能因为路径映射没对上而退回路径通知
func TestNotifyEmbyRefreshMappedSecondLevelLibraries(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmbyLibs(t, []map[string]any{
		{"ItemId": "movie", "Name": "电影", "Locations": []string{"/映射目录/影视/电影"}},
		{"ItemId": "tv", "Name": "剧集", "Locations": []string{"/映射目录/影视/剧集"}},
	}, false) // Emby 还没给新片目录建条目 → 退到整库刷新
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	cfg, _ := json.Marshal(embyRefreshCfg{ServerURL: f.srv.URL, APIKey: "k", PathMapping: root + "#/映射目录"})
	if err := notifyConfigSource.SaveSetting("emby", string(cfg)); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(root, "影视", "电影", "新片 (2026)")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	(&Handler{}).notifyEmbyRefresh(fresh)

	if !f.sawHit("POST /Items/movie/Refresh") {
		t.Fatalf("没有刷新命中的电影库，实际请求: %v", f.hits)
	}
	if f.sawHit("POST /Items/tv/Refresh") {
		t.Fatalf("不该连带刷新剧集库: %v", f.hits)
	}
	if len(f.updates) != 0 {
		t.Fatalf("已经命中媒体库，不该回退路径通知: %v", f.updates)
	}
	// 查条目用的必须是映射后的 Emby 路径，且到库根为止不再上溯
	want := []string{"/映射目录/影视/电影/新片 (2026)", "/映射目录/影视/电影"}
	if len(f.itemPathQ) != 2 || f.itemPathQ[0] != want[0] || f.itemPathQ[1] != want[1] {
		t.Fatalf("上溯路径不对：期望 %v，实际 %v", want, f.itemPathQ)
	}
}

// 删除的主路径：按被删路径查到条目就直接删掉，不再等 Emby 自己扫。
// 实测只提交刷新的话，条目能在库里多挂几分钟，这期间点进去就是播放 404
func TestNotifyEmbyDeletedRemovesItemDirectly(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	f.deleteOK = true
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	alive := filepath.Join(root, "电影")
	if err := os.MkdirAll(alive, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(alive, "美国队长2.2014.{tmdbid=100402}")

	notifyEmbyDeleted(gone)

	if len(f.deleted) != 1 || f.deleted[0] != "item9" {
		t.Fatalf("没有精确删掉条目，实际 deleted=%v hits=%v", f.deleted, f.hits)
	}
	if len(f.itemPathQ) != 1 || f.itemPathQ[0] != filepath.ToSlash(gone) {
		t.Fatalf("查条目用的应当是被删的那个路径: %v", f.itemPathQ)
	}
	for _, h := range f.hits {
		if strings.HasSuffix(h, "/Refresh") {
			t.Fatalf("条目已删掉，不该再刷新: %v", f.hits)
		}
	}
	if len(f.updates) != 0 {
		t.Fatalf("条目已删掉，不该再发路径通知: %v", f.updates)
	}
}

// 本地路径还在 = 不是真删除，绝不能让 Emby 去删（它会连磁盘文件一起删）
func TestNotifyEmbyDeletedNeverDeletesLivingPath(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	f.deleteOK = true
	setupEmbyRefreshCfg(t, f.srv.URL, root)

	live := filepath.Join(root, "电影", "还在的片 (2020)")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}

	notifyEmbyDeleted(live)

	if len(f.deleted) != 0 {
		t.Fatalf("本地还在的路径不该删条目: %v", f.deleted)
	}
}

// 挂载掉线：整个库的路径都会「不存在」，这时候一个删除请求都不许发
func TestNotifyEmbyDeletedNeverDeletesWhenRootGone(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	f.deleteOK = true
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	notifyEmbyDeleted(filepath.Join(root, "电影", "某片 (2020)", "a.mkv.strm"))

	if len(f.hits) != 0 {
		t.Fatalf("挂载掉线时一个请求都不该发，实际: %v", f.hits)
	}
}

// 网盘上删掉一整个分类目录时，媒体库目录自己绝不能被当成条目删掉 ——
// 删一个库条目就是整个媒体库从 Emby 消失
func TestNotifyEmbyDeletedNeverDeletesLibraryRoot(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "影视", "电影")
	f := newFakeEmbyLibs(t, []map[string]any{
		{"ItemId": "movie", "Name": "电影", "Locations": []string{filepath.ToSlash(lib)}},
	}, true)
	f.deleteOK = true
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	if err := os.MkdirAll(filepath.Join(root, "影视"), 0o755); err != nil {
		t.Fatal(err)
	}

	notifyEmbyDeleted(lib) // 电影/ 整个没了

	if len(f.deleted) != 0 {
		t.Fatalf("媒体库目录不该被当成条目删掉: %v", f.deleted)
	}
}

// 同一路径上挂着两个条目时一次删干净：留一个下来，库里照样看得见
func TestNotifyEmbyDeletedRemovesAllItemsOnPath(t *testing.T) {
	root := t.TempDir()
	f := newFakeEmby(t, []string{filepath.ToSlash(root)}, true)
	f.deleteOK, f.dupItems = true, true
	setupEmbyRefreshCfg(t, f.srv.URL, root)
	if err := os.MkdirAll(filepath.Join(root, "电影"), 0o755); err != nil {
		t.Fatal(err)
	}

	notifyEmbyDeleted(filepath.Join(root, "电影", "美国队长2.2014.{tmdbid=100402}"))

	if len(f.deleted) != 2 {
		t.Fatalf("同一路径上的条目应当全部删掉，实际 %v", f.deleted)
	}
}
