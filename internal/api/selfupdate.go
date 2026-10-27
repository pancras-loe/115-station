package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 一键更新（2026-10-27，维护者要求；做法照 MoviePilot：不换镜像、换容器里的程序，靠重启策略拉起来）。
//
// 不挂 Docker socket（AGENTS.md §6.7）：
//  1. 从 ghcr 读这个版本的镜像清单（匿名拉取，和 docker pull 同一份），按本机架构挑出镜像，
//     从最后几层里解出 /app/115-station 与 /app/webui/dist/index.html，放到 DATA_DIR/app/<版本>/。
//     每个 blob 都按清单里的 sha256 摘要校验；新程序再跑一次 --print-version，版本号对得上才算下好。
//  2. 走网页「重启」同一套优雅退出（退出码 1），容器重启策略把镜像自带的程序拉起来；
//     它在读配置之前看到 DATA_DIR/app 里有更新的版本，就 chdir 进去 exec 新程序（BootStagedVersion）。
//
// 只换程序：镜像的运行环境（基础镜像、apt 装的库）不跟着换。Dockerfile 里的 STATION_RUNTIME 是运行环境版本号，
// 新镜像的和当前容器的不一样就拒绝一键更新、让用户 docker compose pull（改运行阶段时要把它加一）。
// 用户后来自己拉了更新的镜像，自带的不旧于下载的，下载的那份直接删掉。
// 新程序连续 3 次没撑过启动后 1 分钟，退回镜像自带的版本并记下原因。
//
// 仓库与镜像名写死：后台就算被攻破，能做的也只是「更新到官方最新版」。

const (
	updateImage       = updateRepo // ghcr.io/pancras-loe/115-station
	stagedDirName     = "app"
	stagedStateFile   = "state.json"
	stagedBinName     = "115-station"
	stagedIndexRel    = "webui/dist/index.html"
	stagedMaxAttempts = 3
	stagedOKAfter     = time.Minute
	// 运行环境版本号的环境变量（Dockerfile 的 ENV）。当前容器没有它 = 不是在本站镜像里跑
	runtimeEnvKey = "STATION_RUNTIME"
	// exec 新程序时带上镜像自带的版本号，界面上写「镜像里是 X」
	imageVersionEnvKey = "STATION_IMAGE_VERSION"
	layerScanMax       = 6         // 只看最后几层：程序与前端都是 Dockerfile 最后的 COPY
	layerSizeMax       = 400 << 20 // 再大的层是系统库，不可能是我们要的
)

// 测试替换
var (
	registryBase   = "https://ghcr.io"
	registryClient = func() *http.Client { return githubHTTPClient(0) }
	// probeStagedBinary 跑一次新程序的 --print-version，确认能在这台机器上跑、版本号对得上
	probeStagedBinary = func(bin string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "--print-version").Output()
		return strings.TrimSpace(string(out)), err
	}
	selfUpdateGOOS = runtime.GOOS
	selfUpdateArch = runtime.GOARCH
)

// stagedDataDir 由 BootStagedVersion 记下（main 启动时最先调用）
var stagedDataDir string

func stagedRoot(dataDir string) string { return filepath.Join(dataDir, stagedDirName) }

// stagedState 下载好的版本与启动记账（DATA_DIR/app/state.json）
type stagedState struct {
	Version  string    `json:"version,omitempty"`
	From     string    `json:"from,omitempty"`     // 从哪个版本更新过来的（通知用）
	Notify   bool      `json:"notify,omitempty"`   // 换上之后推一条「已更新」
	Attempts int       `json:"attempts,omitempty"` // 连续启动了几次还没确认成功
	OK       bool      `json:"ok,omitempty"`
	Failed   string    `json:"failed,omitempty"` // 退回镜像自带版本的原因，界面显示
	FailedAt time.Time `json:"failed_at,omitempty"`
}

func loadStagedState(dataDir string) stagedState {
	var st stagedState
	if b, err := os.ReadFile(filepath.Join(stagedRoot(dataDir), stagedStateFile)); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveStagedState(dataDir string, st stagedState) error {
	if err := os.MkdirAll(stagedRoot(dataDir), 0755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	p := filepath.Join(stagedRoot(dataDir), stagedStateFile)
	if err := os.WriteFile(p+".tmp", b, 0644); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// stagedVersionDir 版本目录。版本号来自 GitHub Release 的 tag，进路径前再挡一次 ../
func stagedVersionDir(dataDir, version string) (string, error) {
	if version == "" || strings.ContainsAny(version, `/\`) || strings.Contains(version, "..") {
		return "", fmt.Errorf("版本号不合法：%q", version)
	}
	return filepath.Join(stagedRoot(dataDir), version), nil
}

// stagedReady 版本目录里两个文件都在
func stagedReady(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, stagedBinName)); err != nil || st.IsDir() {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(stagedIndexRel)))
	return err == nil
}

// ==================== 启动时切换 ====================

// bootAction 启动时拿下载好的版本怎么办
type bootAction int

const (
	bootNone     bootAction = iota // 没有下载的版本 / 本地构建，什么都不做
	bootExec                       // 切到下载的版本
	bootDiscard                    // 镜像自带的不旧于下载的：删掉
	bootGiveUp                     // 连续启动失败：退回镜像自带的版本
	bootIsStaged                   // 自己就是下载的那份
)

// planBoot 纯判定
func planBoot(st stagedState, own string, inStaged, ready bool) bootAction {
	if inStaged {
		return bootIsStaged
	}
	if st.Version == "" {
		return bootNone
	}
	if _, ok := parseVersion(own); !ok {
		return bootNone // dev 构建：本地调试别被切走，也别删
	}
	if !versionNewer(st.Version, own) {
		return bootDiscard
	}
	if !ready {
		return bootDiscard
	}
	if st.Attempts >= stagedMaxAttempts && !st.OK {
		return bootGiveUp
	}
	return bootExec
}

// BootStagedVersion main 在读配置、开库之前调：有更新的已下载版本就 exec 过去（不返回）
func BootStagedVersion(dataDir, own string) {
	stagedDataDir = dataDir
	root := stagedRoot(dataDir)
	st := loadStagedState(dataDir)
	exe, _ := os.Executable()
	inStaged := exe != "" && strings.HasPrefix(filepath.Clean(exe), filepath.Clean(root)+string(filepath.Separator))
	dir, derr := stagedVersionDir(dataDir, st.Version)
	ready := derr == nil && stagedReady(dir)

	switch planBoot(st, own, inStaged, ready) {
	case bootIsStaged, bootNone:
		return
	case bootDiscard:
		if derr == nil {
			_ = os.RemoveAll(dir)
		}
		log.Printf("[更新] ○ 已下载的 %s 不比镜像自带的 %s 新（或文件不全），删掉", st.Version, own)
		_ = saveStagedState(dataDir, stagedState{Failed: st.Failed, FailedAt: st.FailedAt})
		return
	case bootGiveUp:
		_ = os.RemoveAll(dir)
		reason := fmt.Sprintf("%s 连续 %d 次没能正常启动，已退回镜像自带的 %s；请用 docker compose pull 更新", st.Version, st.Attempts, own)
		log.Printf("[更新] ✗ %s", reason)
		_ = saveStagedState(dataDir, stagedState{Failed: reason, FailedAt: time.Now()})
		return
	}

	st.Attempts++
	st.OK = false
	if err := saveStagedState(dataDir, st); err != nil {
		log.Printf("[更新] ✗ 写启动记账失败，不切换：%v", err)
		return
	}
	log.Printf("[更新] ○ 切到已下载的 %s（镜像自带 %s，第 %d 次启动）", st.Version, own, st.Attempts)
	env := append(os.Environ(), imageVersionEnvKey+"="+own)
	wd, _ := os.Getwd()
	// 前端按相对路径 ./webui/dist/index.html 读，版本目录和 /app 同布局
	if err := os.Chdir(dir); err != nil {
		log.Printf("[更新] ✗ 进入 %s 失败，继续用镜像自带版本：%v", dir, err)
		return
	}
	if err := execBinary(filepath.Join(dir, stagedBinName), os.Args, env); err != nil {
		_ = os.Chdir(wd)
		log.Printf("[更新] ✗ 启动 %s 失败，继续用镜像自带版本：%v", st.Version, err)
	}
}

// MarkStagedBootOK main 在服务起来之后调：自己是下载的那份、撑过一分钟就清掉启动记账，并推一次「已更新」
func MarkStagedBootOK(own string) {
	if os.Getenv(imageVersionEnvKey) == "" || stagedDataDir == "" {
		return
	}
	go func() {
		time.Sleep(stagedOKAfter)
		st := loadStagedState(stagedDataDir)
		if st.Version != own {
			return
		}
		notify := st.Notify
		st.Attempts, st.OK, st.Notify = 0, true, false
		st.Failed, st.FailedAt = "", time.Time{}
		if err := saveStagedState(stagedDataDir, st); err != nil {
			log.Printf("[更新] ✗ 写启动记账失败：%v", err)
		}
		if notify {
			from := st.From
			if from == "" {
				from = "旧版本"
			}
			log.Printf("[更新] ✓ 已更新到 %s", own)
			NotifyMessage(kindSystem, "✓ 115-Station 已更新", fmt.Sprintf("%s → %s", from, own))
		}
	}()
}

// ==================== 从 ghcr 取程序 ====================

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform,omitempty"`
}

type ociManifest struct {
	MediaType string          `json:"mediaType"`
	Manifests []ociDescriptor `json:"manifests"` // 多架构索引
	Config    ociDescriptor   `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type registry struct {
	c     *http.Client
	token string
}

func newRegistry(ctx context.Context) (*registry, error) {
	r := &registry{c: registryClient()}
	u := registryBase + "/token?service=ghcr.io&scope=" + url.QueryEscape("repository:"+updateImage+":pull")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := r.c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 ghcr.io：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ghcr.io 拿拉取令牌返回 HTTP %d", resp.StatusCode)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil || t.Token == "" {
		return nil, fmt.Errorf("ghcr.io 拉取令牌解析失败")
	}
	r.token = t.Token
	return r, nil
}

func (r *registry) get(ctx context.Context, path, accept string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registryBase+"/v2/"+updateImage+path, nil)
	req.Header.Set("Authorization", "Bearer "+r.token) // blob 会 302 到别的域名，Go 跨域跳转时自己去掉
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := r.c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 ghcr.io：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("ghcr.io 上找不到 %s", path)
		}
		return nil, fmt.Errorf("ghcr.io 返回 HTTP %d（%s）", resp.StatusCode, path)
	}
	return resp, nil
}

// getVerified 读一个小对象（清单 / 配置），按摘要引用时核对 sha256
func (r *registry) getVerified(ctx context.Context, path, digest, accept string) ([]byte, error) {
	resp, err := r.get(ctx, path, accept)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if digest != "" && sha256Digest(b) != digest {
		return nil, fmt.Errorf("%s 摘要对不上", path)
	}
	return b, nil
}

func sha256Digest(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// imageTagOf GitHub Release 的 tag → 镜像 tag（CI 的 type=semver,pattern={{version}} 去掉了 v）
func imageTagOf(version string) string { return strings.TrimPrefix(version, "v") }

// platformManifest 读这个版本、本机架构的镜像清单
func (r *registry) platformManifest(ctx context.Context, tag string) (*ociManifest, error) {
	b, err := r.getVerified(ctx, "/manifests/"+url.PathEscape(tag), "", manifestAccept)
	if err != nil {
		return nil, err
	}
	var m ociManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("镜像清单解析失败：%v", err)
	}
	if len(m.Manifests) > 0 {
		var pick *ociDescriptor
		for i, d := range m.Manifests {
			// buildx 的索引里还挂着 attestation（platform unknown/unknown），按系统与架构认
			if d.Platform != nil && d.Platform.OS == selfUpdateGOOS && d.Platform.Architecture == selfUpdateArch {
				pick = &m.Manifests[i]
				break
			}
		}
		if pick == nil {
			return nil, fmt.Errorf("镜像 %s 没有 %s/%s 版本", tag, selfUpdateGOOS, selfUpdateArch)
		}
		b, err = r.getVerified(ctx, "/manifests/"+pick.Digest, pick.Digest, manifestAccept)
		if err != nil {
			return nil, err
		}
		m = ociManifest{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("镜像清单解析失败：%v", err)
		}
	}
	if len(m.Layers) == 0 || m.Config.Digest == "" {
		return nil, fmt.Errorf("镜像清单里没有层")
	}
	return &m, nil
}

// imageRuntime 镜像配置里的 STATION_RUNTIME
func (r *registry) imageRuntime(ctx context.Context, m *ociManifest) (string, error) {
	b, err := r.getVerified(ctx, "/blobs/"+m.Config.Digest, m.Config.Digest, "")
	if err != nil {
		return "", err
	}
	var cfg struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"config"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("镜像配置解析失败：%v", err)
	}
	for _, e := range cfg.Config.Env {
		if v, ok := strings.CutPrefix(e, runtimeEnvKey+"="); ok {
			return v, nil
		}
	}
	return "", nil
}

// stagedWanted 层里的路径 → 版本目录里的相对路径
var stagedWanted = map[string]string{
	"app/" + stagedBinName:  stagedBinName,
	"app/" + stagedIndexRel: stagedIndexRel,
}

// extractLayer 下载一层，把要的文件解进 dir；读完整层核对摘要，对不上返回错误（调用方删掉 dir）
func (r *registry) extractLayer(ctx context.Context, layer ociDescriptor, dir string, got map[string]bool, onBytes func(int64)) error {
	if !strings.Contains(layer.MediaType, "gzip") {
		return fmt.Errorf("不认识的层格式 %s", layer.MediaType)
	}
	resp, err := r.get(ctx, "/blobs/"+layer.Digest, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	h := sha256.New()
	src := io.TeeReader(&countReader{r: io.LimitReader(resp.Body, layerSizeMax+1), on: onBytes}, h)
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("解压失败：%v", err)
	}
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读层失败：%v", err)
		}
		name := strings.TrimPrefix(strings.TrimPrefix(hdr.Name, "./"), "/")
		rel, ok := stagedWanted[name]
		// 后面的层盖前面的：已经从更靠后的层拿到的不再覆盖
		if !ok || got[rel] || hdr.Typeflag != tar.TypeReg {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(f, tr)
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return fmt.Errorf("写 %s 失败：%v", rel, cerr)
		}
		found[rel] = true
	}
	// 摘要算的是压缩后的整层：剩下的也要读完
	if _, err := io.Copy(io.Discard, src); err != nil {
		return fmt.Errorf("下载中断：%v", err)
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != layer.Digest {
		return fmt.Errorf("层 %.19s 摘要对不上，下载的内容不完整或被改过", layer.Digest)
	}
	for k := range found {
		got[k] = true
	}
	return nil
}

type countReader struct {
	r  io.Reader
	on func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.on != nil {
		c.on(int64(n))
	}
	return n, err
}

// errRuntimeChanged 新镜像改了运行环境，一键更新不够
var errRuntimeChanged = errors.New("runtime changed")

// stageVersion 把 version 的程序与前端下到 DATA_DIR/app/<version>/，返回目录
func stageVersion(ctx context.Context, dataDir, version string, progress func(done, total int64)) (string, error) {
	final, err := stagedVersionDir(dataDir, version)
	if err != nil {
		return "", err
	}
	reg, err := newRegistry(ctx)
	if err != nil {
		return "", err
	}
	m, err := reg.platformManifest(ctx, imageTagOf(version))
	if err != nil {
		return "", err
	}
	rt, err := reg.imageRuntime(ctx, m)
	if err != nil {
		return "", err
	}
	if cur := os.Getenv(runtimeEnvKey); rt == "" || rt != cur {
		return "", fmt.Errorf("%w：%s 的运行环境是 %q，当前容器是 %q", errRuntimeChanged, version, rt, cur)
	}

	tmp := filepath.Join(stagedRoot(dataDir), ".tmp-"+version)
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()

	var layers []ociDescriptor
	for i := len(m.Layers) - 1; i >= 0 && len(layers) < layerScanMax; i-- {
		if m.Layers[i].Size <= layerSizeMax {
			layers = append(layers, m.Layers[i])
		}
	}
	got := map[string]bool{}
	var done, total int64
	for _, l := range layers {
		total = done + l.Size
		if err := reg.extractLayer(ctx, l, tmp, got, func(n int64) {
			done += n
			if progress != nil {
				progress(done, total)
			}
		}); err != nil {
			return "", err
		}
		if len(got) == len(stagedWanted) {
			break
		}
	}
	if len(got) != len(stagedWanted) {
		return "", fmt.Errorf("镜像最后 %d 层里没找到程序或前端", len(layers))
	}
	bin := filepath.Join(tmp, stagedBinName)
	_ = os.Chmod(bin, 0755)
	v, err := probeStagedBinary(bin)
	if err != nil {
		return "", fmt.Errorf("新程序在这台机器上跑不起来：%v", err)
	}
	if v != version {
		return "", fmt.Errorf("新程序报的版本是 %q，不是 %s", v, version)
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	ok = true
	// 别的版本目录（上一次下的、正在跑的这份之前的）都收掉，正在跑的那份不动
	if ents, err := os.ReadDir(stagedRoot(dataDir)); err == nil {
		running, _ := os.Executable()
		for _, e := range ents {
			p := filepath.Join(stagedRoot(dataDir), e.Name())
			if !e.IsDir() || p == final || (running != "" && strings.HasPrefix(running, p+string(filepath.Separator))) {
				continue
			}
			_ = os.RemoveAll(p)
		}
	}
	return final, nil
}

// ==================== 执行与状态 ====================

// applyStatus 一键更新的进度（内存，重启即清）
type applyStatus struct {
	Phase   string `json:"phase"` // downloading / restarting / failed
	Target  string `json:"target"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
	By      string `json:"by,omitempty"`
	Started string `json:"started"`
}

var (
	applyMu  sync.Mutex
	applyCur *applyStatus
)

func currentApply() *applyStatus {
	applyMu.Lock()
	defer applyMu.Unlock()
	if applyCur == nil {
		return nil
	}
	c := *applyCur
	return &c
}

// applyBlock 这个部署能不能一键更新；能就返回空
func applyBlock() string {
	switch {
	case selfUpdateGOOS != "linux" || os.Getenv(runtimeEnvKey) == "":
		return "不是在本站官方镜像里运行，请用 docker compose pull 更新"
	case restartHook == nil:
		return "当前运行方式不支持重启，请用 docker compose pull 更新"
	case stagedDataDir == "":
		return "数据目录未就绪"
	}
	if _, ok := parseVersion(buildVersion); !ok {
		return "当前是本地 / 开发构建，不做一键更新"
	}
	return ""
}

// startSelfUpdate 开始一键更新到 target（必须是检测到的最新版本）。下载在后台，
// reply 收到「下载好了正在重启」或失败原因；成功重启后新版本推「已更新」
func startSelfUpdate(target, by string, reply func(...string)) error {
	if reason := applyBlock(); reason != "" {
		return errors.New(reason)
	}
	st := currentUpdateStatus()
	if !st.HasUpdate || st.Latest == "" {
		return errors.New("没有检测到新版本，先检查一次更新")
	}
	if target != "" && target != st.Latest {
		return fmt.Errorf("最新版本已经是 %s，不是 %s，请重新检查更新", st.Latest, target)
	}
	target = st.Latest
	applyMu.Lock()
	if applyCur != nil && applyCur.Phase != "failed" {
		applyMu.Unlock()
		return fmt.Errorf("已经在更新到 %s 了", applyCur.Target)
	}
	applyCur = &applyStatus{Phase: "downloading", Target: target, By: by, Started: time.Now().Format(time.RFC3339)}
	applyMu.Unlock()
	log.Printf("[更新] ○ 开始一键更新 %s → %s（%s）", buildVersion, target, by)

	go func() {
		fail := func(err error) {
			msg := err.Error()
			if errors.Is(err, errRuntimeChanged) {
				msg = "这一版改了运行环境（系统库 / 基础镜像），一键更新换不了，请用 docker compose pull && docker compose up -d 更新"
				log.Printf("[更新] ✗ %v", err)
			} else {
				log.Printf("[更新] ✗ 一键更新失败：%s", msg)
			}
			applyMu.Lock()
			applyCur.Phase, applyCur.Error = "failed", msg
			applyMu.Unlock()
			if reply != nil {
				reply("✗ 更新失败：" + msg)
			}
		}
		dir, derr := stagedVersionDir(stagedDataDir, target)
		if derr != nil {
			fail(derr)
			return
		}
		if !stagedReady(dir) {
			var lastLog time.Time
			_, err := stageVersion(context.Background(), stagedDataDir, target, func(done, total int64) {
				applyMu.Lock()
				applyCur.Done, applyCur.Total = done, total
				applyMu.Unlock()
				if time.Since(lastLog) > 10*time.Second {
					lastLog = time.Now()
					log.Printf("[更新] ○ 下载中 %.1f / %.1f MB", float64(done)/(1<<20), float64(total)/(1<<20))
				}
			})
			if err != nil {
				fail(err)
				return
			}
		}
		if err := saveStagedState(stagedDataDir, stagedState{Version: target, From: buildVersion, Notify: true}); err != nil {
			fail(fmt.Errorf("写更新记账失败：%v", err))
			return
		}
		applyMu.Lock()
		applyCur.Phase = "restarting"
		applyMu.Unlock()
		log.Printf("[更新] ✓ %s 已下载并校验，重启换上", target)
		if reply != nil {
			reply("✓ "+target+" 已下载并校验，正在重启。", "启动后大约一分钟推「已更新」。")
		}
		time.Sleep(2 * time.Second) // 回复先发出去
		restartHook()
	}()
	return nil
}

// ApplyUpdate POST /system/update/apply {version}
func (h *Handler) ApplyUpdate(c *gin.Context) {
	var body struct {
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := startSelfUpdate(body.Version, "网页", nil); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, currentUpdateStatus())
}
