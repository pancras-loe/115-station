package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 样本取自 2026-10-02 现场的 Emby 日志（《鱿鱼游戏》S02，api_key 与 IP 已替换）
func TestParseFfprobeFailure(t *testing.T) {
	raw, err := os.ReadFile("testdata/emby_ffprobe_fail.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := parseFfprobeFailure(strings.NewReader(string(raw)), "/d/biislctuies7t83of")
	want := "音轨 #4（chi「台配国语」AAC）：Emby 内置的 ffprobe 不支持 xHE-AAC 音频（Audio object type 42 is not implemented）"
	if got != want {
		t.Fatalf("\n得到 %q\n期望 %q", got, want)
	}
	// 只认 -i 的是这个文件的那条：另一集的 pickcode 不能串
	if got := parseFfprobeFailure(strings.NewReader(string(raw)), "/d/nosuchpick"); got != "" {
		t.Fatalf("日志里没有的文件应返回空，得到 %q", got)
	}
	// 没有 ffprobe 失败记录（只有 Execute 那行带着 pickcode）不算
	onlyExec := "2026-10-02 12:46:05.835 Info MediaProbeManager: ProcessRun 'ffprobe' Execute: /bin/ffprobe -i \"http://127.0.0.1:6086/d/abc.mkv?/x.mkv\"\n"
	if got := parseFfprobeFailure(strings.NewReader(onlyExec), "/d/abc"); got != "" {
		t.Fatalf("没有失败记录应返回空，得到 %q", got)
	}
}

func TestEmbyProbeLogKey(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:6086/d/biislctuies7t83of.mkv?/鱿鱼游戏.mkv": "/d/biislctuies7t83of",
		"http://127.0.0.1:6086/d/abc?/x.iso":                        "/d/abc",
		"/media/某剧/S01E01.strm":                                     "",
		"http://other.example/video.mkv":                            "",
	}
	for in, want := range cases {
		if got := embyProbeLogKey(in); got != want {
			t.Errorf("%q → %q，期望 %q", in, got, want)
		}
	}
}

// 走一遍 HTTP：路径、带 api_key；非 200（API Key 不是管理员）返回空
func TestEmbyProbeLogCauseHTTP(t *testing.T) {
	raw, _ := os.ReadFile("testdata/emby_ffprobe_fail.txt")
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Logs/embyserver.txt" || r.URL.Query().Get("api_key") != "k" {
			t.Errorf("意外请求 %s", r.URL)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write(raw)
		}
	}))
	defer srv.Close()
	cfg := embyRefreshCfg{ServerURL: srv.URL, APIKey: "k"}
	src := embyMediaSource{Path: "http://127.0.0.1:6086/d/e55lf8g5cxocqzw9v.mkv?/S02E01.mkv"}
	if got := embyProbeLogCause(cfg, src); !strings.HasPrefix(got, "音轨 #4") {
		t.Fatalf("应认出音轨 #4，得到 %q", got)
	}
	status = http.StatusUnauthorized
	if got := embyProbeLogCause(cfg, src); got != "" {
		t.Fatalf("没权限应返回空，得到 %q", got)
	}
}
