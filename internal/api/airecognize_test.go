package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"115-station/internal/config"
	"115-station/internal/model"
)

// 地址补全是这条链路踩过的坑：测试连接与实际识别曾经各拼各的 URL
// （一个 /v1/chat/completions、一个 /chat/completions），
// 于是「测试成功 → 真跑 404」。两边现在共用 aiChatEndpoint。
//
// 补全规则按 OpenAI SDK 的 base_url 语义：只追加 /chat/completions，不插 /v1
// ——DeepSeek 官方 base_url 就是 https://api.deepseek.com（接口在 /chat/completions）。
func TestAIChatEndpoint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/chat/completions"},
		{"  https://api.deepseek.com  ", "https://api.deepseek.com/chat/completions"},
		{"https://api.siliconflow.cn/v1", "https://api.siliconflow.cn/v1/chat/completions"},
		{"https://api.siliconflow.cn/v1/", "https://api.siliconflow.cn/v1/chat/completions"},
		{"https://x.com/v1/chat/completions", "https://x.com/v1/chat/completions"},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:11434/v1/chat/completions"},
		// 没写协议头按 https 补
		{"api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		// 网关挂在子路径下
		{"https://gw.example.com/openai/v1", "https://gw.example.com/openai/v1/chat/completions"},
		// 非法地址
		{"", ""},
		{"   ", ""},
		{"://nope", ""},
	}
	for _, c := range cases {
		if got := aiChatEndpoint(c.in); got != c.want {
			t.Errorf("aiChatEndpoint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 备选端点只在「地址里还没有版本段」时才有，否则没有第二条可试
func TestAIChatEndpointV1(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.openai.com", "https://api.openai.com/v1/chat/completions"},
		{"https://gw.example.com/openai", "https://gw.example.com/openai/v1/chat/completions"},
		{"https://api.openai.com/v1", ""},
		{"https://x.com/chat/completions", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := aiChatEndpointV1(c.in); got != c.want {
			t.Errorf("aiChatEndpointV1(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAIJSONObject(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"title":"星际穿越","year":"2014"}`, `{"title":"星际穿越","year":"2014"}`},
		{"```json\n{\"title\":\"A\"}\n```", `{"title":"A"}`},
		{"好的，结果是：{\"title\":\"A\"}。", `{"title":"A"}`},
		// 推理模型的思考段里也有花括号，必须先剥掉
		{"<think>可能是 {x} 或 {y}</think>{\"title\":\"A\"}", `{"title":"A"}`},
		{"没有 JSON", ""},
	}
	for _, c := range cases {
		if got := aiJSONObject(c.in); got != c.want {
			t.Errorf("aiJSONObject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// chatStub 起一个最小的 OpenAI 协议服务端，返回固定的 content
func chatStub(t *testing.T, path *string, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path != nil {
			*path = r.URL.Path
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
	}))
}

func TestAIChatHitsCompletionsPath(t *testing.T) {
	var got string
	srv := chatStub(t, &got, "pong")
	defer srv.Close()

	reply, err := aiChat(aiRecognizeCfg{URL: srv.URL, Model: "m"}, []map[string]string{{"role": "user", "content": "hi"}}, 16, 5*time.Second)
	if err != nil {
		t.Fatalf("aiChat: %v", err)
	}
	if got != "/chat/completions" {
		t.Errorf("请求路径 = %q, want /chat/completions", got)
	}
	if reply != "pong" {
		t.Errorf("reply = %q, want pong", reply)
	}
}

// 200 但不是 OpenAI 协议的形状（地址填成了网关首页）要当失败报出来，
// 不能静默当成「识别不到」
func TestAIChatRejectsNonOpenAIResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>welcome</html>"))
	}))
	defer srv.Close()

	if _, err := aiChat(aiRecognizeCfg{URL: srv.URL, Model: "m"}, nil, 0, 5*time.Second); err == nil {
		t.Fatal("非 OpenAI 协议响应应当报错")
	}
}

// o 系列推理模型不收 max_tokens/temperature：第一次 400 后去掉这两项重试一次
func TestAIChatRetriesWithoutRejectedParams(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		if _, ok := m["max_tokens"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	reply, err := aiChat(aiRecognizeCfg{URL: srv.URL, Model: "o3"}, nil, 16, 5*time.Second)
	if err != nil {
		t.Fatalf("aiChat: %v", err)
	}
	if reply != "ok" || len(bodies) != 2 {
		t.Fatalf("reply=%q 请求次数=%d, want ok/2", reply, len(bodies))
	}
	if _, ok := bodies[1]["temperature"]; ok {
		t.Error("重试请求不该再带 temperature")
	}
}

// 用户把 OpenAI 这类服务商的 base_url 少写了 /v1：第一条 404，自动换带 /v1 的再试，
// 并把真正打通的那条地址报回去
func TestAIChatFallsBackToV1On404(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	reply, endpoint, err := aiChatResolve(aiRecognizeCfg{URL: srv.URL, Model: "m"}, nil, 16, 5*time.Second)
	if err != nil {
		t.Fatalf("aiChatResolve: %v", err)
	}
	if reply != "ok" || endpoint != srv.URL+"/v1/chat/completions" {
		t.Fatalf("reply=%q endpoint=%q", reply, endpoint)
	}
	if len(paths) != 2 || paths[0] != "/chat/completions" {
		t.Errorf("请求路径 = %v, want [/chat/completions /v1/chat/completions]", paths)
	}
}

// 地址已经带 /v1 时没有第二条可试，404 就是 404，别多打一次
func TestAIChatNoFallbackWhenVersioned(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := aiChat(aiRecognizeCfg{URL: srv.URL + "/v1", Model: "m"}, nil, 16, 5*time.Second); err == nil {
		t.Fatal("404 应当报错")
	}
	if calls != 1 {
		t.Errorf("请求次数 = %d, want 1", calls)
	}
}

// 400 不是参数问题时不重试，直接把错误交回去
func TestAIChatNoRetryOnAuthError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	if _, err := aiChat(aiRecognizeCfg{URL: srv.URL, Model: "m"}, nil, 16, 5*time.Second); err == nil {
		t.Fatal("401 应当报错")
	}
	if calls != 1 {
		t.Errorf("请求次数 = %d, want 1", calls)
	}
}

func TestAIGuessTitle(t *testing.T) {
	var gotBody string
	cfgURL := func(reply string) *aiRecognizeCfg {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
			})
		}))
		t.Cleanup(srv.Close)
		return &aiRecognizeCfg{URL: srv.URL, Model: "m"}
	}
	p := &ParsedName{Title: "Interstellar", Source: "Interstellar.2014.2160p.mkv", Context: []string{"星际", "电影"}}

	g := aiGuessTitle(cfgURL(`{"title":"星际穿越","original_title":"Interstellar","year":"2014","type":"movie","season":0,"episode":0,"confidence":92}`), p)
	if g == nil || g.Title != "星际穿越" || g.OriginalTitle != "Interstellar" || g.Year != "2014" || g.Type != "movie" || g.Confidence != 92 {
		t.Errorf("正常提取 = %+v", g)
	}
	// 模型要看到原始文件名和目录，不能只给解析后的片名
	for _, want := range []string{"Interstellar.2014.2160p.mkv", "星际 / 电影"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("请求里缺少上下文 %q", want)
		}
	}
	// 年份直接喂给 TMDB 搜索，模型给的非四位年份必须丢掉而不是原样传下去
	if g := aiGuessTitle(cfgURL(`{"title":"A","year":"未知","type":"电视剧","confidence":300}`), p); g == nil || g.Year != "" || g.Type != "" || g.Confidence != 100 {
		t.Errorf("非法年份/类型/把握度应清理, got %+v", g)
	}
	// 季集号写成字符串：退回只取片名年份，不整条作废
	if g := aiGuessTitle(cfgURL(`{"title":"A","year":"2014","season":"2"}`), p); g == nil || g.Title != "A" || g.Season != 0 {
		t.Errorf("季集号类型不对时应退回片名年份, got %+v", g)
	}
	// 只给了原名：原名顶上
	if g := aiGuessTitle(cfgURL(`{"title":"","original_title":"Dune"}`), p); g == nil || g.Title != "Dune" {
		t.Errorf("中文名为空时应用原名, got %+v", g)
	}
	if g := aiGuessTitle(cfgURL(`{"title":"","year":"2014"}`), p); g != nil {
		t.Errorf("空标题应返回 nil, got %+v", g)
	}
	if g := aiGuessTitle(cfgURL("抱歉，我无法识别"), p); g != nil {
		t.Errorf("无 JSON 应返回 nil, got %+v", g)
	}
}

// 开关关着 / 配置不全（缺模型名）都视为没开启，
// 别拿默认模型名或半截配置去撞一串 404
func TestLoadAIRecognizeCfg(t *testing.T) {
	defer func() { notifyConfigSource = nil; model.DB = nil; invalidateAICfgCache() }()
	notifyConfigSource = &config.Config{DataDir: t.TempDir(), ConfigDir: t.TempDir()}

	save := func(v string) {
		if err := notifyConfigSource.SaveSetting(aiSettingKey, v); err != nil {
			t.Fatalf("SaveSetting: %v", err)
		}
		invalidateAICfgCache()
	}

	// 开关关着：配置填得再全也不启用
	save(`{"enabled":false,"url":"https://api.deepseek.com","key":"sk-x","model":"deepseek-flash"}`)
	if got := loadAIRecognizeCfg(); got != nil {
		t.Errorf("开关关着应视为未配置, got %+v", got)
	}
	// 没有 enabled 字段（老配置）同样按关闭算
	save(`{"url":"https://api.deepseek.com","key":"sk-x","model":"deepseek-flash"}`)
	if got := loadAIRecognizeCfg(); got != nil {
		t.Errorf("缺 enabled 字段应视为未配置, got %+v", got)
	}
	save(`{"enabled":true,"url":"https://api.deepseek.com","key":"sk-x","model":""}`)
	if got := loadAIRecognizeCfg(); got != nil {
		t.Errorf("缺模型名应视为未配置, got %+v", got)
	}
	save(`{"enabled":true,"url":"","key":"sk-x","model":"deepseek-flash"}`)
	if got := loadAIRecognizeCfg(); got != nil {
		t.Errorf("缺地址应视为未配置, got %+v", got)
	}
	save(`{"enabled":true,"url":"https://api.deepseek.com","key":"sk-x","model":"deepseek-flash"}`)
	got := loadAIRecognizeCfg()
	if got == nil || got.Model != "deepseek-flash" {
		t.Fatalf("开关打开且配置齐全应当可用, got %+v", got)
	}
}
