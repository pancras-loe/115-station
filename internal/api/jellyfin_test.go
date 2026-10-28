package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"115-station/internal/config"
	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// 假 Jellyfin 的一棵树：库目录 → 分类 → 电影（Movie 的路径就是 .strm，影片目录没有条目）
// 剧 → 有季目录的季 / 没季目录的虚拟季（没有路径）→ 集
type jfNode struct {
	ID, Name, Path, Type, Parent string
	Tmdb                         string
}

var jfTree = []jfNode{
	{ID: "cat-om", Name: "欧美电影", Path: "/mnt/电影/欧美电影", Type: "Folder", Parent: "lib-movie"},
	{ID: "m1", Name: "钢铁侠", Path: "/mnt/电影/欧美电影/钢铁侠 (2008)/钢铁侠 (2008).strm", Type: "Movie", Parent: "cat-om", Tmdb: "1726"},
	{ID: "m2", Name: "钢铁侠2", Path: "/mnt/电影/欧美电影/钢铁侠2 (2010)/钢铁侠2 (2010).strm", Type: "Movie", Parent: "cat-om", Tmdb: "10138"},
	{ID: "s1", Name: "庆余年", Path: "/mnt/剧集/庆余年 (2019)", Type: "Series", Parent: "lib-tv", Tmdb: "90761"},
	{ID: "s1s1", Name: "第 1 季", Path: "/mnt/剧集/庆余年 (2019)/Season 01", Type: "Season", Parent: "s1"},
	{ID: "s1e1", Name: "第1集", Path: "/mnt/剧集/庆余年 (2019)/Season 01/庆余年 S01E01.strm", Type: "Episode", Parent: "s1s1"},
	{ID: "s2", Name: "无季目录", Path: "/mnt/剧集/无季目录 (2020)", Type: "Series", Parent: "lib-tv"},
	{ID: "s2s1", Name: "第 1 季", Path: "", Type: "Season", Parent: "s2"},
	{ID: "s2e1", Name: "第1集", Path: "/mnt/剧集/无季目录 (2020)/无季目录 S01E01.strm", Type: "Episode", Parent: "s2s1"},
}

// newFakeJellyfin 只认新版鉴权；Emby 专有的接口一律 404，带 Path / AnyProviderIdEquals 过滤的请求照 Jellyfin 忽略
func newFakeJellyfin(t *testing.T, hits *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if hits != nil {
			*hits = append(*hits, r.URL.Path+"?"+r.URL.RawQuery)
		}
		if r.URL.Path == "/System/Info/Public" {
			_, _ = w.Write([]byte(`{"ProductName":"Jellyfin Server","Version":"10.11.0"}`))
			return
		}
		if q.Get("ApiKey") != "k" && r.Header.Get("Authorization") != `MediaBrowser Token="k"` {
			http.Error(w, "legacy auth disabled", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/Library/VirtualFolders":
			_, _ = w.Write([]byte(`[{"Name":"电影","ItemId":"lib-movie","CollectionType":"movies","Locations":["/mnt/电影"]},
				{"Name":"剧集","ItemId":"lib-tv","CollectionType":"tvshows","Locations":["/mnt/剧集"]}]`))
		case "/Users":
			_, _ = w.Write([]byte(`[{"Id":"u1","Name":"admin","Policy":{"IsAdministrator":true,"IsDisabled":false}}]`))
		case "/Items":
			var items []map[string]any
			for _, n := range jfTree {
				if p := q.Get("ParentId"); p != "" && n.Parent != p {
					continue
				}
				if s := q.Get("SearchTerm"); s != "" && !strings.Contains(n.Name, s) {
					continue
				}
				if ty := q.Get("IncludeItemTypes"); ty != "" && !strings.Contains(","+ty+",", ","+n.Type+",") {
					continue
				}
				it := map[string]any{"Id": n.ID, "Name": n.Name, "Path": n.Path, "Type": n.Type}
				if n.Tmdb != "" {
					it["ProviderIds"] = map[string]string{"Tmdb": n.Tmdb}
				}
				items = append(items, it)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items})
		default:
			http.NotFound(w, r)
		}
	}))
}

func useJellyfinSetting(t *testing.T, base, kind string) {
	t.Helper()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	v, _ := json.Marshal(map[string]string{"server_url": base, "api_key": "k", "kind": kind})
	if err := notifyConfigSource.SaveSetting("emby", string(v)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { notifyConfigSource = nil; model.DB = nil })
}

func TestJellyfinItemsByPath(t *testing.T) {
	var reqs []string
	srv := newFakeJellyfin(t, &reqs)
	defer srv.Close()
	useJellyfinSetting(t, srv.URL, msJellyfin)
	cfg, _ := loadEmbyRefreshCfg()

	if libs := embyMediaFolders(cfg); len(libs) != 2 || libs[0].ID != "lib-movie" || libs[1].Locations[0] != "/mnt/剧集" {
		t.Fatalf("Jellyfin 媒体库读错: %+v", libs)
	}
	cases := []struct {
		path, want string
	}{
		{"/mnt/电影/欧美电影/钢铁侠 (2008)/钢铁侠 (2008).strm", "m1"}, // STRM 本身
		{"/mnt/电影/欧美电影/钢铁侠 (2008)", "m1"},                 // 影片目录：Jellyfin 没有目录条目，取里面的 Movie
		{"/mnt/电影/欧美电影", "cat-om"},
		{"/mnt/电影", "lib-movie"},
		{"/mnt/剧集/庆余年 (2019)/Season 01/庆余年 S01E01.strm", "s1e1"},
		{"/mnt/剧集/庆余年 (2019)/Season 01", "s1s1"},
		{"/mnt/剧集/无季目录 (2020)/无季目录 S01E01.strm", "s2e1"}, // 虚拟季下面的集
		{"/mnt/剧集/庆余年 (2019)/Season 02/x.strm", ""},
		{"/外面/x.strm", ""},
	}
	for _, c := range cases {
		hits := embyItemsByPath(cfg, c.path)
		got := ""
		if len(hits) > 0 {
			got = pickMediaHit(hits).ID
		}
		if got != c.want {
			t.Errorf("%s → %q，想要 %q（%+v）", c.path, got, c.want, hits)
		}
	}
	for _, r := range reqs {
		if strings.Contains(r, "Path=") || strings.Contains(r, "VirtualFolders/Query") {
			t.Fatalf("对 Jellyfin 发了它不认的请求: %s", r)
		}
	}
}

func TestJellyfinBranches(t *testing.T) {
	srv := newFakeJellyfin(t, nil)
	defer srv.Close()
	useJellyfinSetting(t, srv.URL, msJellyfin)

	type u struct {
		ID     string `json:"Id"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
		} `json:"Policy"`
	}
	if us, err := embyUserList[u](srv.URL, "k", nil); err != nil || len(us) != 1 || !us[0].Policy.IsAdministrator {
		t.Fatalf("Jellyfin 用户列表: %+v %v", us, err)
	}
	if id, err := embyAdminUser(srv.URL, "k"); err != nil || id != "u1" {
		t.Fatalf("管理员: %q %v", id, err)
	}

	items, err := embyTitlesByTmdb(srv.URL, "k", personTarget{Kind: "movie", TmdbID: 1726, Title: "钢铁侠"})
	if err != nil || len(items) != 1 || items[0].ID != "m1" {
		t.Fatalf("按片名 + 编号找片目: %+v %v", items, err)
	}

	q := embyRealOnly(srv.URL, map[string][]string{})
	if q.Get("IsMissing") != "false" || q.Has("IsVirtualItem") {
		t.Fatalf("Jellyfin 该用 IsMissing: %v", q)
	}

	// 做不了的功能：提前探测不建任务、合并人物与深删不跑
	if embyExtractEnabled() {
		t.Fatal("Jellyfin 下不该自动提前探测")
	}
	if _, err := enqueueProbeJob(nil, probeJobSpec{Paths: []string{"/mnt/x.strm"}}); err == nil || !strings.Contains(err.Error(), "Jellyfin") {
		t.Fatalf("Jellyfin 下建探测任务应被拒: %v", err)
	}
	for _, f := range []string{msFeatPreProbe, msFeatProbeLog, msFeatDeepDelete, msFeatPeopleMerge} {
		if mediaServerBlock(f) == "" {
			t.Errorf("%s 在 Jellyfin 下应该不支持", f)
		}
	}
}

// 不写 kind 时读 /System/Info/Public 自动认；写了 emby 就按 Emby，不发识别请求
func TestMediaServerDetect(t *testing.T) {
	var reqs []string
	srv := newFakeJellyfin(t, &reqs)
	defer srv.Close()
	mediaServerAutoDetect = true
	defer func() { mediaServerAutoDetect = false }()
	msDetectCache.Delete(srv.URL)
	defer msDetectCache.Delete(srv.URL)

	useJellyfinSetting(t, srv.URL, "")
	if !embyIsJellyfin() || !embyIsJellyfin() {
		t.Fatal("没认出 Jellyfin")
	}
	n := 0
	for _, r := range reqs {
		if strings.HasPrefix(r, "/System/Info/Public") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("识别结果没缓存，问了 %d 次", n)
	}

	useJellyfinSetting(t, srv.URL, msEmby)
	if embyIsJellyfin() || mediaServerBlock(msFeatPreProbe) != "" {
		t.Fatal("手动指定 Emby 时应按 Emby 走")
	}
}

// 插件「Send All Properties」发来的扁平载荷（字段名照 jellyfin-plugin-webhook 的 DataObjectHelpers）
const jfAddedEpisode = `{"ServerId":"s","ServerName":"jf","NotificationType":"ItemAdded","ItemId":"e1","ItemType":"Episode",
"Name":"第 \\\"1\\\" 集","SeriesName":"庆余年","SeasonNumber":2,"EpisodeNumber":1,"Year":2019,"Provider_tmdb":"90761","Provider_imdb":"tt1"}`

func TestJellyfinWebhookPayload(t *testing.T) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(jfAddedEpisode), &raw); err != nil {
		t.Fatal(err)
	}
	p, ok := jellyfinWebhookPayload(raw)
	if !ok || p["Event"] != "library.new" {
		t.Fatalf("没认出 Jellyfin 入库事件: %v", p)
	}
	item := p["Item"].(map[string]interface{})
	ids := item["ProviderIds"].(map[string]interface{})
	if item["Name"] != `第 "1" 集` || item["SeasonName"] != "第 2 季" || item["ProductionYear"] != 2019.0 ||
		ids["Tmdb"] != "90761" || ids["Imdb"] != "tt1" || item["Type"] != "Episode" {
		t.Fatalf("字段换错: %v", item)
	}

	// Emby 的载荷原样放过
	emby := map[string]interface{}{"Event": "library.new", "Item": map[string]interface{}{"Name": "x"}}
	if _, ok := jellyfinWebhookPayload(emby); ok {
		t.Fatal("Emby 载荷不该被改写")
	}
	// 用不上的类型不能被动作词猜成播放（SessionStart 带 start）
	p, _ = jellyfinWebhookPayload(map[string]interface{}{"NotificationType": "SessionStart"})
	if c, _ := embyEventCategory(strings.ToLower(p["Event"].(string))); c != "" {
		t.Fatalf("SessionStart 被归成了 %q", c)
	}
	for nt, want := range map[string]string{"PlaybackStart": "play", "PlaybackStop": "pause", "ItemDeleted": "deleted"} {
		p, _ = jellyfinWebhookPayload(map[string]interface{}{"NotificationType": nt})
		if c, _ := embyEventCategory(strings.ToLower(p["Event"].(string))); c != want {
			t.Errorf("%s → %q，想要 %q", nt, c, want)
		}
	}
}

// 走一遍接口：删除事件不带路径，只记日志、不深删不推；入库事件进入库通知队列
func TestJellyfinWebhookHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTestDB(t, "jfhook.db")
	resetWebhookSeen()
	t.Cleanup(resetWebhookSeen)
	cfg := &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}
	_ = cfg.SaveSetting("emby-notify", `{"token":"tk"}`)
	h := &Handler{DB: model.DB, Config: cfg}
	post := func(body string) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/emby/webhook?token=tk", strings.NewReader(body))
		h.EmbyWebhook(c)
		return w.Body.String()
	}
	if b := post(`{"NotificationType":"ItemDeleted","ItemId":"m1","ItemType":"Movie","Name":"钢铁侠"}`); !strings.Contains(b, "只记日志") {
		t.Fatalf("Jellyfin 删除事件: %s", b)
	}
	if b := post(jfAddedEpisode); !strings.Contains(b, "入库通知队列") {
		t.Fatalf("Jellyfin 入库事件: %s", b)
	}
	if b := post(`{"NotificationType":"PendingRestart"}`); !strings.Contains(b, "ok") {
		t.Fatalf("用不上的事件: %s", b)
	}
}
