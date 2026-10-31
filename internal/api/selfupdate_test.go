package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"115-station/internal/model"
)

// fakeRegistry 假 ghcr：多架构索引 → 本机架构清单 → 配置 + 若干层
type fakeRegistry struct {
	blobs     map[string][]byte // digest → 内容
	manifests map[string][]byte // tag / digest → 内容
	served    []string
}

func gzTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// newFakeImage 建一个镜像：layers 从底到顶
func newFakeImage(t *testing.T, tag, runtimeRev string, layers ...[]byte) *fakeRegistry {
	t.Helper()
	fr := &fakeRegistry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	cfg, _ := json.Marshal(map[string]any{"config": map[string]any{"Env": []string{"PATH=/usr/bin", runtimeEnvKey + "=" + runtimeRev}}})
	cfgD := sha256Digest(cfg)
	fr.blobs[cfgD] = cfg
	var ls []ociDescriptor
	for _, l := range layers {
		d := sha256Digest(l)
		fr.blobs[d] = l
		ls = append(ls, ociDescriptor{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: d, Size: int64(len(l))})
	}
	man, _ := json.Marshal(ociManifest{MediaType: "application/vnd.oci.image.manifest.v1+json",
		Config: ociDescriptor{Digest: cfgD, Size: int64(len(cfg))}, Layers: ls})
	manD := sha256Digest(man)
	fr.manifests[manD] = man
	other, _ := json.Marshal(ociManifest{Layers: []ociDescriptor{{Digest: "sha256:x"}}, Config: ociDescriptor{Digest: "sha256:y"}})
	fr.manifests["sha256:other"] = other
	idx := map[string]any{
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"digest": "sha256:other", "platform": map[string]string{"os": "linux", "architecture": "s390x"}},
			{"digest": "sha256:att", "platform": map[string]string{"os": "unknown", "architecture": "unknown"}},
			{"digest": manD, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
		},
	}
	fr.manifests[tag], _ = json.Marshal(idx)
	return fr
}

func (fr *fakeRegistry) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr.served = append(fr.served, r.URL.Path)
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"token":"tk"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		prefix := "/v2/" + updateImage + "/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		if ref, ok := strings.CutPrefix(rest, "manifests/"); ok {
			if b, ok := fr.manifests[ref]; ok {
				w.Write(b)
				return
			}
		}
		if d, ok := strings.CutPrefix(rest, "blobs/"); ok {
			if b, ok := fr.blobs[d]; ok {
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	oldBase, oldClient, oldProbe, oldOS, oldArch := registryBase, registryClient, probeStagedBinary, selfUpdateGOOS, selfUpdateArch
	registryBase = srv.URL
	registryClient = func() *http.Client { return srv.Client() }
	selfUpdateGOOS, selfUpdateArch = "linux", "amd64"
	probeStagedBinary = func(bin string) (string, error) {
		b, err := os.ReadFile(bin)
		return strings.TrimSpace(string(b)), err // 测试里「程序」的内容就是它的版本号
	}
	t.Cleanup(func() {
		registryBase, registryClient, probeStagedBinary, selfUpdateGOOS, selfUpdateArch = oldBase, oldClient, oldProbe, oldOS, oldArch
	})
}

func TestStageVersionFromRegistry(t *testing.T) {
	t.Setenv(runtimeEnvKey, "1")
	base := gzTar(t, map[string]string{"usr/lib/libc.so": "x", "app/115-station": "v1.0.0"}) // 旧层里的旧程序
	bin := gzTar(t, map[string]string{"app/115-station": "v26.10.27"})
	web := gzTar(t, map[string]string{"app/webui/dist/index.html": "<html>new</html>", "app/webui/dist/other.txt": "x"})
	fr := newFakeImage(t, "26.10.27", "1", base, bin, web)
	fr.serve(t)

	dataDir := t.TempDir()
	dir, err := stageVersion(t.Context(), dataDir, "v26.10.27", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, stagedBinName)); string(b) != "v26.10.27" {
		t.Fatalf("程序取错层了：%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "webui", "dist", "index.html")); string(b) != "<html>new</html>" {
		t.Fatalf("前端不对：%q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "webui", "dist", "other.txt")); err == nil {
		t.Fatal("不要的文件也解出来了")
	}
	// 两个文件都在最后两层，最底下那层不用下
	for _, p := range fr.served {
		if strings.HasSuffix(p, sha256Digest(base)) {
			t.Fatal("多下了不需要的层")
		}
	}
}

func TestStageVersionRejects(t *testing.T) {
	bin := gzTar(t, map[string]string{"app/115-station": "v26.10.27"})
	web := gzTar(t, map[string]string{"app/webui/dist/index.html": "x"})

	t.Run("运行环境变了", func(t *testing.T) {
		t.Setenv(runtimeEnvKey, "1")
		newFakeImage(t, "26.10.27", "2", bin, web).serve(t)
		_, err := stageVersion(t.Context(), t.TempDir(), "v26.10.27", nil)
		if err == nil || !strings.Contains(err.Error(), "runtime changed") {
			t.Fatalf("应当拒绝：%v", err)
		}
	})
	t.Run("层被改过", func(t *testing.T) {
		t.Setenv(runtimeEnvKey, "1")
		fr := newFakeImage(t, "26.10.27", "1", bin, web)
		fr.blobs[sha256Digest(web)] = gzTar(t, map[string]string{"app/webui/dist/index.html": "evil"})
		fr.serve(t)
		dataDir := t.TempDir()
		if _, err := stageVersion(t.Context(), dataDir, "v26.10.27", nil); err == nil || !strings.Contains(err.Error(), "摘要") {
			t.Fatalf("摘要不对应当失败：%v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, stagedDirName, "v26.10.27")); err == nil {
			t.Fatal("失败了不该留下版本目录")
		}
	})
	t.Run("版本号对不上", func(t *testing.T) {
		t.Setenv(runtimeEnvKey, "1")
		newFakeImage(t, "26.10.28", "1", bin, web).serve(t)
		if _, err := stageVersion(t.Context(), t.TempDir(), "v26.10.28", nil); err == nil || !strings.Contains(err.Error(), "版本") {
			t.Fatalf("应当失败：%v", err)
		}
	})
	t.Run("没有本机架构", func(t *testing.T) {
		t.Setenv(runtimeEnvKey, "1")
		newFakeImage(t, "26.10.27", "1", bin, web).serve(t)
		selfUpdateArch = "arm64"
		if _, err := stageVersion(t.Context(), t.TempDir(), "v26.10.27", nil); err == nil || !strings.Contains(err.Error(), "arm64") {
			t.Fatalf("应当失败：%v", err)
		}
	})
	t.Run("版本号带路径", func(t *testing.T) {
		if _, err := stagedVersionDir(t.TempDir(), "../x"); err == nil {
			t.Fatal("应当拒绝")
		}
	})
}

func TestPlanBoot(t *testing.T) {
	cases := []struct {
		name     string
		st       stagedState
		own      string
		inStaged bool
		ready    bool
		want     bootAction
	}{
		{"没有下载的", stagedState{}, "v26.10.1", false, false, bootNone},
		{"自己就是下载的", stagedState{Version: "v26.10.2"}, "v26.10.2", true, true, bootIsStaged},
		{"切过去", stagedState{Version: "v26.10.2"}, "v26.10.1", false, true, bootExec},
		{"同日序号版", stagedState{Version: "v26.10.1-2"}, "v26.10.1", false, true, bootExec},
		{"镜像追上了", stagedState{Version: "v26.10.2"}, "v26.10.2", false, true, bootDiscard},
		{"镜像更新", stagedState{Version: "v26.10.2"}, "v26.10.3", false, true, bootDiscard},
		{"文件不全", stagedState{Version: "v26.10.2"}, "v26.10.1", false, false, bootDiscard},
		{"本地构建不动", stagedState{Version: "v26.10.2"}, "dev", false, true, bootNone},
		{"连续失败", stagedState{Version: "v26.10.2", Attempts: 3}, "v26.10.1", false, true, bootGiveUp},
		{"失败两次再试", stagedState{Version: "v26.10.2", Attempts: 2}, "v26.10.1", false, true, bootExec},
	}
	for _, c := range cases {
		if got := planBoot(c.st, c.own, c.inStaged, c.ready); got != c.want {
			t.Errorf("%s：得到 %d，应为 %d", c.name, got, c.want)
		}
	}
}

func TestBootStagedVersionDiscardsAndGivesUp(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, stagedDirName, "v26.10.2")
	os.MkdirAll(filepath.Join(dir, "webui", "dist"), 0755)
	os.WriteFile(filepath.Join(dir, stagedBinName), []byte("x"), 0755)
	os.WriteFile(filepath.Join(dir, "webui", "dist", "index.html"), []byte("x"), 0644)

	saveStagedState(dataDir, stagedState{Version: "v26.10.2", Attempts: 3})
	BootStagedVersion(dataDir, "v26.10.1")
	st := loadStagedState(dataDir)
	if st.Version != "" || st.Failed == "" {
		t.Fatalf("连续失败应当退回并记原因：%+v", st)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("退回后版本目录应当删掉")
	}
}

func TestStartSelfUpdateBlocked(t *testing.T) {
	t.Setenv(runtimeEnvKey, "")
	if err := startSelfUpdate("", "测试", nil); err == nil || !strings.Contains(err.Error(), "官方镜像") {
		t.Fatalf("不在本站镜像里应当拒绝：%v", err)
	}
}

func TestUpdateTGCallback(t *testing.T) {
	t.Setenv(runtimeEnvKey, "")
	newTestDB(t, "botupdate.db")
	h := &Handler{DB: model.DB}

	// 没任务在跑：点「更新」直接开始，不再弹确认（测试环境不在本站镜像里，开始即报原因）
	text, btns := h.updateTGCallback("u:a:v26.10.27", nil)
	if len(btns) != 0 || !strings.Contains(text, "官方镜像") {
		t.Fatalf("没任务在跑应当直接开始：%q %+v", text, btns)
	}

	// 有任务在跑：先确认，写明会中断哪个
	h.DB.Create(&model.TaskJob{Kind: "full", Title: "全量同步", Status: jobRunning})
	text, btns = h.updateTGCallback("u:a:v26.10.27", nil)
	if !strings.Contains(text, "v26.10.27") || !strings.Contains(text, "全量同步") || len(btns) != 1 || btns[0][0].Data != "u:g:v26.10.27" {
		t.Fatalf("确认屏不对：%q %+v", text, btns)
	}
	if text, _ := h.updateTGCallback("u:c", nil); !strings.Contains(text, "取消") {
		t.Fatalf("取消不对：%q", text)
	}
	if text, _ := h.updateTGCallback("u:zz", nil); !strings.Contains(text, "失效") {
		t.Fatalf("坏按钮不对：%q", text)
	}
}
