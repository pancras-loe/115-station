package api

// ==================== 应用内自更新（Docker 部署） ====================
//
// 依赖 compose 挂载 Docker socket：
//   volumes:
//     - /var/run/docker.sock:/var/run/docker.sock
// 流程：前端比对 GitHub 最新提交 → 展示两个版本间的提交（更新内容）
//   → 拉取当前容器镜像引用的最新版 → 用完全相同的配置重建并启动容器。
// 未挂载 socket 时接口返回配置指引，不做任何危险动作。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const dockerSockPath = "/var/run/docker.sock"

func dockerHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", dockerSockPath, 3*time.Second)
			},
		},
		Timeout: timeout,
	}
}

// fetchLatestSHA 查询 GitHub main 最新提交（15 秒内去重防狂刷，force 时跳过；失败返回上次已知值）
func fetchLatestSHA(force bool) (sha, errText string) {
	latestVersionCache.Lock()
	cached, cacheAt := latestVersionCache.sha, latestVersionCache.at
	latestVersionCache.Unlock()
	if !force && cached != "" && time.Since(cacheAt) < 15*time.Second {
		return cached, ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/DaisyYijin/STRMhub/commits/main", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return cached, "GitHub 不可达: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		// 403: 未认证限额 60 次/小时（走代理还共享出口 IP），提示等待
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if ts, e := strconv.ParseInt(reset, 10, 64); e == nil {
				wait := time.Until(time.Unix(ts, 0)).Truncate(time.Minute)
				if wait < 0 {
					wait = 0
				}
				return cached, fmt.Sprintf("GitHub API 限流，约 %s 后恢复（期间显示的是 %s 前的缓存结果）", wait, time.Since(cacheAt).Truncate(time.Second))
			}
		}
		return cached, "GitHub API 限流（60 次/小时，走代理共享出口 IP 更易触发），期间显示缓存结果"
	}
	var out struct {
		Sha string `json:"sha"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) == nil && out.Sha != "" {
		latestVersionCache.Lock()
		latestVersionCache.sha, latestVersionCache.at = out.Sha, time.Now()
		latestVersionCache.Unlock()
		return out.Sha, ""
	}
	return cached, "GitHub 响应异常"
}

// commitItem 更新日志条目
type commitItem struct {
	Sha     string `json:"sha"`
	Message string `json:"message"`
	Date    string `json:"date"`
}

// fetchCommitsBetween 拉取 current..latest 之间的提交清单（更新日志；最新在最上）
func fetchCommitsBetween(current, latest string) []commitItem {
	client := &http.Client{Timeout: 10 * time.Second}
	if pu := getProxyURL(); pu != "" {
		if p, err := parseProxyURL(pu); err == nil {
			client.Transport = &http.Transport{Proxy: p}
		}
	}
	u := "https://api.github.com/repos/DaisyYijin/STRMhub/compare/" + current + "..." + latest
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var cmp struct {
		Commits []struct {
			Sha string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
	}
	if json.NewDecoder(resp.Body).Decode(&cmp) != nil {
		return nil
	}
	commits := []commitItem{}
	for i := len(cmp.Commits) - 1; i >= 0; i-- {
		cm := cmp.Commits[i]
		msg := cm.Commit.Message
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx] // 提交首行（标题）
		}
		commits = append(commits, commitItem{Sha: cm.Sha, Message: msg, Date: cm.Commit.Author.Date})
	}
	return commits
}

// ==================== 更新通知（构建完成 / 更新完成 → 企微/TG） ====================
//
// 后台每 5 分钟检查一次 CI 构建状态：
//   - 新版本镜像「构建完成」→ 按用户配置的通知渠道推送 更新日志（提交清单），
//     每个提交 SHA 只通知一次（状态持久化，重启不重复）
//   - 新版本「构建失败」→ 同样只提醒一次，方便去 Actions 查看
//   - 容器重启后检测到运行版本变化 → 推送「已更新到 vX」确认
// dev 构建与通知渠道未配置时静默。

type updateNotifyState struct {
	ReadySHA string `json:"ready_sha"` // 已通知过"可更新"的提交
	FailSHA  string `json:"fail_sha"`  // 已通知过"构建失败"的提交
	Applied  string `json:"applied"`   // 上次运行的版本（用于更新完成确认）
}

func updateNotifyLoad() updateNotifyState {
	var st updateNotifyState
	if v := settingValueCompat("update-notify"); v != "" {
		_ = json.Unmarshal([]byte(v), &st)
	}
	return st
}

func updateNotifySave(st updateNotifyState) {
	b, _ := json.Marshal(st)
	if cfgGlobal != nil {
		_ = cfgGlobal.SaveSetting("update-notify", string(b))
	}
}

func shortSha(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

// StartUpdateNotifier 启动更新通知监视（SetupRoutes 调用）
func StartUpdateNotifier() {
	go func() {
		time.Sleep(15 * time.Second) // 等网络就绪（通知配置在启动时已同步加载）
		notifyUpdateOnce()           // 启动即查：更新完成确认立刻发，不等轮询周期
		for {
			select {
			case <-stopCh:
				return
			case <-time.After(5 * time.Minute):
			}
			notifyUpdateOnce()
		}
	}()
}

func notifyUpdateOnce() {
	if buildVersion == "dev" {
		return
	}
	st := updateNotifyLoad()

	// 1) 更新完成确认：持久化的"上次运行版本"与当前不同 = 刚完成了一次更新
	if st.Applied != "" && st.Applied != buildVersion {
		var b strings.Builder
		fmt.Fprintf(&b, "v%s → v%s", shortSha(st.Applied), shortSha(buildVersion))
		if commits := fetchCommitsBetween(st.Applied, buildVersion); len(commits) > 0 {
			fmt.Fprintf(&b, "，更新内容（%d 个提交）：", len(commits))
			for i, cm := range commits {
				if i >= 10 {
					fmt.Fprintf(&b, "\n…等共 %d 个提交", len(commits))
					break
				}
				fmt.Fprintf(&b, "\n• %s %s", shortSha(cm.Sha), truncateStr(cm.Message, 60))
			}
		}
		b.WriteString("\n\n新版本已生效，如遇异常可在 GitHub 提交反馈")
		NotifyMessage("✓ StrmHub 更新完成", b.String())
	}
	st.Applied = buildVersion

	// 2) 新版本检测
	latest, _ := fetchLatestSHA(false)
	if latest == "" || strings.HasPrefix(latest, buildVersion[:7]) {
		updateNotifySave(st)
		return
	}
	switch imageBuildState(latest) {
	case "ready":
		if st.ReadySHA != latest {
			st.ReadySHA = latest
			commits := fetchCommitsBetween(buildVersion, latest)
			var b strings.Builder
			fmt.Fprintf(&b, "v%s → v%s，更新内容（%d 个提交）：", shortSha(buildVersion), shortSha(latest), len(commits))
			for i, cm := range commits {
				if i >= 10 {
					fmt.Fprintf(&b, "\n…等共 %d 个提交", len(commits))
					break
				}
				fmt.Fprintf(&b, "\n• %s %s", shortSha(cm.Sha), truncateStr(cm.Message, 60))
			}
			b.WriteString("\n\n到管理后台左下角点击「有新版本」即可更新")
			NotifyMessage("↑ StrmHub 有新版本", b.String())
		}
	case "failed":
		if st.FailSHA != latest {
			st.FailSHA = latest
			NotifyMessage("✗ StrmHub 新版本构建失败",
				"提交 "+shortSha(latest)+" 的镜像构建失败，暂无法更新。\n可到 GitHub Actions 查看失败原因，修复提交后会再次通知")
		}
	}
	updateNotifySave(st)
}

// VersionChanges GET /version/changes —— 当前版本与最新版本之间的提交列表（更新内容）
func (h *Handler) VersionChanges(c *gin.Context) {
	current := buildVersion
	latest, ferr := fetchLatestSHA(true)
	if current == "dev" || latest == "" || strings.HasPrefix(latest, current[:7]) {
		c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": nil, "error": ferr,
			"uptodate": current != "dev" && latest != "" && strings.HasPrefix(latest, current[:7])})
		return
	}
	buildState := imageBuildState(latest)
	c.JSON(http.StatusOK, gin.H{"current": current, "latest": latest, "commits": fetchCommitsBetween(current, latest), "build": buildState})
}

// applyUpdateFlow 应用内更新核心流程，返回 HTTP 状态码与响应载荷。
// HTTP 端点与企微/机器人「执行更新」指令共用
func (h *Handler) applyUpdateFlow() (int, gin.H) {
	if buildVersion == "dev" {
		return http.StatusBadRequest, gin.H{"error": "本地开发构建（dev）不支持自更新，请用 Docker 镜像部署"}
	}
	if _, err := os.Stat(dockerSockPath); err != nil {
		return http.StatusBadRequest, gin.H{"error": "未挂载 Docker socket，无法自更新。\n配置方法：在 docker-compose.yml 的 strmhub 服务 volumes 中加一行 /var/run/docker.sock:/var/run/docker.sock，然后 docker compose up -d 重建一次，之后即可在界面内一键更新"}
	}
	latest, ferr := fetchLatestSHA(true)
	if ferr != "" {
		log.Printf("[自更新] ○ 获取最新版本失败: %s", ferr)
	}
	if latest == "" {
		return http.StatusBadGateway, gin.H{"error": "无法获取最新版本（GitHub 不可达，可在系统配置代理卡配置代理），稍后再试"}
	}
	if strings.HasPrefix(latest, buildVersion[:7]) {
		return http.StatusOK, gin.H{"message": "已是最新版本", "latest": latest}
	}
	// 镜像就绪检查：只看提交会抢在 Actions 构建完成前更新，拉到的仍是旧镜像
	// 并白重启一次容器；构建中/失败时直接拒绝，CI 查询失败（unknown）则放行
	switch imageBuildState(latest) {
	case "building":
		return http.StatusServiceUnavailable, gin.H{"error": "新版本镜像还在 GitHub Actions 构建中（通常 3~8 分钟），构建完成后会提示更新，请稍后再试", "latest": latest, "build": "building"}
	case "failed":
		return http.StatusServiceUnavailable, gin.H{"error": "最新提交的 CI 构建失败，镜像未发布；请到 GitHub Actions 查看失败原因，或等修复提交后重试", "latest": latest, "build": "failed"}
	}

	client := dockerHTTPClient(5 * time.Minute)

	// 1. 定位当前容器（优先 cgroup 里的真实 ID；HOSTNAME 可能被自定义 hostname 覆盖）
	selfID := selfContainerID()
	if selfID == "" {
		return http.StatusBadGateway, gin.H{"error": "无法确定当前容器 ID（cgroup 与 HOSTNAME 均不可用）"}
	}
	insp, err := dockerRequestJSON(client, "GET", "/containers/"+selfID+"/json", nil)
	if err != nil {
		// ID 未命中（cgroup namespace 隐藏真实 ID / HOSTNAME 异常）：按镜像名兜底，
		// 找"唯一运行中的 strmhub 镜像容器"即视为自身
		if fallbackID, ferr := dockerFindSelfByImage(client); ferr == nil && fallbackID != "" && fallbackID != selfID {
			if finsp, ierr := dockerRequestJSON(client, "GET", "/containers/"+fallbackID+"/json", nil); ierr == nil {
				log.Printf("[自更新] HOSTNAME(%s) 未命中，已按镜像名兜底定位容器 %s", selfID, fallbackID[:12])
				selfID, insp, err = fallbackID, finsp, nil
			}
		}
	}
	if err != nil {
		// 附带诊断：列出该 socket 对应守护进程里可见的容器，区分"自定义 hostname"与"挂错 socket"
		hint := ""
		if names, lerr := dockerListContainerNames(client); lerr == nil && len(names) > 0 {
			hint = "该 Docker 守护进程可见容器：" + strings.Join(names, ", ")
		} else if lerr == nil {
			hint = "该 Docker 守护进程下没有任何容器（疑似挂载了别的 docker.sock）"
		}
		errMsg := "查询当前容器失败（" + selfID + "）: " + err.Error()
		if hint != "" {
			errMsg += "\n" + hint
		}
		return http.StatusBadGateway, gin.H{"error": errMsg}
	}
	cfgMap, _ := insp["Config"].(map[string]interface{})
	hostCfg, _ := insp["HostConfig"].(map[string]interface{})
	imageRef, _ := cfgMap["Image"].(string)
	containerName := "strmhub"
	if n, ok := insp["Name"].(string); ok && n != "" {
		containerName = strings.TrimPrefix(n, "/")
	}
	ref := normalizeImageRef(imageRef)
	cfgCmd, cfgEnv, cfgLabels := cfgMap["Cmd"], cfgMap["Env"], cfgMap["Labels"]
	cfgEntry, cfgWD, cfgUser := cfgMap["Entrypoint"], cfgMap["WorkingDir"], cfgMap["User"]
	// compose 自定义网络需要显式 EndpointsConfig（静态 IP/别名缺失会导致启动失败）
	endpoints := map[string]interface{}{}
	if nsOuter, ok := insp["NetworkSettings"].(map[string]interface{}); ok {
		if ns, ok := nsOuter["Networks"].(map[string]interface{}); ok {
			for netName, nv := range ns {
				if m, ok := nv.(map[string]interface{}); ok {
					ep := map[string]interface{}{}
					for _, k := range []string{"IPAMConfig", "Aliases", "MacAddress"} {
						if v, ok := m[k]; ok && v != nil {
							ep[k] = v
						}
					}
					endpoints[netName] = ep
				}
			}
		}
	}
	// 先清理上次失败残留的 -updating/-old 容器，避免本次改名冲突
	for _, suffix := range []string{"-updating", "-old", "-updater"} {
		if resp, err := dockerDo(client, "DELETE", "/containers/"+containerName+suffix+"?force=1", nil); err == nil {
			resp.Body.Close()
		}
	}

	// 2. 拉取最新镜像（同步等待，可能需要几十秒）
	if err := dockerPull(client, ref); err != nil {
		return http.StatusBadGateway, gin.H{"error": "拉取镜像失败: " + err.Error()}
	}
	log.Printf("[自更新] ✓ 镜像已拉取: %s（当前 v%s → v%s）", ref, buildVersion[:7], latest[:7])

	// 3. 创建新容器（临时名；此刻旧容器仍在运行，无任何影响）
	tmpName := containerName + "-updating"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+tmpName+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	createCfg := map[string]interface{}{
		"Image":      ref,
		"Cmd":        cfgCmd, "Env": cfgEnv, "Labels": cfgLabels,
		"Entrypoint": cfgEntry, "WorkingDir": cfgWD, "User": cfgUser,
		"HostConfig": hostCfg,
	}
	if len(endpoints) > 0 {
		createCfg["NetworkingConfig"] = map[string]interface{}{"EndpointsConfig": endpoints}
	}
	createBody, _ := json.Marshal(createCfg)
	resp, err := dockerDo(client, "POST", "/containers/create?name="+url.QueryEscape(tmpName), createBody)
	if err != nil {
		return http.StatusBadGateway, gin.H{"error": "创建新容器失败: " + err.Error()}
	}
	var created struct {
		ID string `json:"Id"`
	}
	createCode := resp.StatusCode
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if createCode >= 400 || created.ID == "" {
		return http.StatusBadGateway, gin.H{"error": fmt.Sprintf("创建新容器失败: HTTP %d", createCode)}
	}
	log.Printf("[自更新] ✓ 新容器已创建（%s）", tmpName)

	// 4. 双改名（运行中容器可安全改名）：旧→bak 让出原名，新→原名。
	// 改名失败必须如实处理——此前错误被丢弃还打「改名完成」，旧容器删失败
	// 残留时新容器会以 -updating 名义一直跑错名字
	oldBak := containerName + "-old"
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldBak+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+selfID+"/rename?name="+url.QueryEscape(oldBak), nil); err != nil {
		return http.StatusBadGateway, gin.H{"error": "旧容器改名失败（" + oldBak + "）: " + err.Error() + "（未做任何变更，服务未受影响）"}
	} else {
		resp.Body.Close()
	}
	if resp, err := dockerDo(client, "POST", "/containers/"+created.ID+"/rename?name="+url.QueryEscape(containerName), nil); err != nil {
		log.Printf("[自更新] ✗ 新容器占用原名失败: %v（回滚旧容器名）", err)
		if r2, e2 := dockerDo(client, "POST", "/containers/"+selfID+"/rename?name="+url.QueryEscape(containerName), nil); e2 == nil {
			r2.Body.Close()
		}
		return http.StatusBadGateway, gin.H{"error": "新容器改名失败: " + err.Error() + "（已回滚，服务未受影响）"}
	} else {
		resp.Body.Close()
	}
	log.Printf("[自更新] ✓ 改名完成（%s → %s，新容器已占用原名）", oldBak, containerName)

	// 5. 启动更新辅助容器（独立进程）完成停旧/启新/清理——主容器不能停自己
	if err := dockerLaunchUpdater(client, containerName, ref, selfID, created.ID, hostCfg); err != nil {
		// 辅助容器失败：回滚改名，一切如旧
		log.Printf("[自更新] ✗ %v，回滚改名", err)
		if resp, e := dockerDo(client, "POST", "/containers/"+created.ID+"/rename?name="+url.QueryEscape(tmpName), nil); e == nil {
			resp.Body.Close()
		}
		if resp, e := dockerDo(client, "POST", "/containers/"+selfID+"/rename?name="+url.QueryEscape(containerName), nil); e == nil {
			resp.Body.Close()
		}
		if resp, e := dockerDo(client, "DELETE", "/containers/"+created.ID+"?force=1", nil); e == nil {
			resp.Body.Close()
		}
		return http.StatusBadGateway, gin.H{"error": err.Error() + "（已回滚，服务未受影响）"}
	}
	return http.StatusAccepted, gin.H{"message": "镜像已拉取，容器切换中（约 10~30 秒），页面将自动刷新", "latest": latest}
}

// ApplyUpdate POST /update/apply —— HTTP 包装
func (h *Handler) ApplyUpdate(c *gin.Context) {
	code, payload := h.applyUpdateFlow()
	c.JSON(code, payload)
}

// normalizeImageRef 规范镜像引用为 repo:tag（digest 固定或无 tag 时取 latest）
func normalizeImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i] + ":latest"
	}
	if !strings.Contains(strings.SplitN(ref, "/", 2)[len(strings.SplitN(ref, "/", 2))-1], ":") {
		ref += ":latest"
	}
	return ref
}

// dockerPull POST /images/create 流式拉取，直到完成或出错
func dockerPull(client *http.Client, ref string) error {
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	resp, err := dockerDo(client, "POST", "/images/create?fromImage="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 150))
	}
		dec := json.NewDecoder(resp.Body)
		for {
			var line struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := dec.Decode(&line); err != nil {
				if err == io.EOF {
					return nil
				}
				// 流异常（截断/非 JSON）视为拉取失败：镜像层复用时空流返回的是
				// EOF，走不到这里；损坏流被当成功会误导后续报错指向创建容器
				return fmt.Errorf("拉取流解析中断: %v", err)
			}
			if line.Error != "" {
				return fmt.Errorf("%s", line.Error)
			}
		}
}

// dockerLaunchUpdater 启动「更新辅助容器」执行更新收尾。
// 主容器不能停自己（docker stop 会杀掉本进程，后续步骤无法执行），
// 因此用一个独立容器（同一镜像、以 update-finish 子命令运行、挂同一 docker.sock）
// 完成：停旧 → 启动新 → 删除旧。此前主容器已完成：创建新容器 + 双改名。
// updater 名字跟主容器名走（此前固定 strmhub-updater：容器改名后残留的
// 固定名容器会让 create 409，之后每次更新都失败，只能手工清理）
func dockerLaunchUpdater(client *http.Client, containerName, imageRef, oldID, newID string, hostCfg map[string]interface{}) error {
	// 从自身挂载里找 docker.sock 的 bind（辅助容器需要同样的 socket）
	sockBind := ""
	if binds, ok := hostCfg["Binds"].([]interface{}); ok {
		for _, b := range binds {
			if bs, ok := b.(string); ok && strings.HasSuffix(bs, ":"+dockerSockPath) {
				sockBind = bs
				break
			}
		}
	}
	if sockBind == "" {
		sockBind = dockerSockPath + ":" + dockerSockPath
	}
	updaterName := containerName + "-updater"
	// 清理上次可能残留的同名 updater（AutoRemove 只在正常退出后生效）
	if resp, err := dockerDo(client, "DELETE", "/containers/"+updaterName+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	body, _ := json.Marshal(map[string]interface{}{
		"Image": imageRef,
		"Cmd":   []string{"update-finish", oldID, newID},
		"HostConfig": map[string]interface{}{
			"Binds":       []string{sockBind},
			"NetworkMode": "none",
			"AutoRemove":  true, // 退出后自动删除（收尾日志已写入主流程可观测的结果）
		},
		"Labels": map[string]string{"strmhub-role": "updater"},
	})
	resp, err := dockerDo(client, "POST", "/containers/create?name="+url.QueryEscape(updaterName), body)
	if err != nil {
		return fmt.Errorf("创建辅助容器: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	code := resp.StatusCode
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if code >= 400 || created.ID == "" {
		return fmt.Errorf("创建辅助容器 HTTP %d", code)
	}
	resp2, err := dockerDo(client, "POST", "/containers/"+created.ID+"/start", nil)
	if err != nil {
		return fmt.Errorf("启动辅助容器: %w", err)
	}
	resp2.Body.Close()
	log.Printf("[自更新] ✓ 更新辅助容器已启动（strmhub-updater），将由它完成停旧/启新")
	return nil
}

// RunUpdateFinish 更新收尾（在辅助容器进程内运行）：停旧 → 启动新 → 清理旧。
// 新容器启动失败时回滚：把旧容器重新启动（旧容器只是被停，配置未动）。
func RunUpdateFinish(oldID, newID string) {
	log.Printf("[更新辅助] ▶ 收尾开始：旧=%s 新=%s", truncateStr(oldID, 12), truncateStr(newID, 12))
	client := dockerHTTPClient(3 * time.Minute)

	// 等 2 秒让主容器把 HTTP 应答发出去
	time.Sleep(2 * time.Second)

	// 1. 停旧容器
	if resp, err := dockerDo(client, "POST", "/containers/"+oldID+"/stop?t=15", nil); err == nil {
		resp.Body.Close()
	} else {
		log.Printf("[更新辅助] ○ 停止旧容器返回: %v（可能已退出，继续）", err)
	}
	// 2. 等旧容器真正停止（端口释放），最多 30 秒
	stopped := false
	for i := 0; i < 15; i++ {
		insp, err := dockerRequestJSON(client, "GET", "/containers/"+oldID+"/json", nil)
		if err != nil {
			stopped = true // 已查不到（异常但可继续）
			break
		}
		if st, ok := insp["State"].(map[string]interface{}); ok {
			if run, _ := st["Running"].(bool); !run {
				stopped = true
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if !stopped {
		log.Printf("[更新辅助] ✗ 旧容器 30 秒未停止，中止（新容器未启动，服务未受影响）")
		return
	}
	log.Printf("[更新辅助] ✓ 旧容器已停止")

	// 3. 启动新容器（端口已释放）
	resp, err := dockerDo(client, "POST", "/containers/"+newID+"/start", nil)
	if err != nil {
		log.Printf("[更新辅助] ✗ 启动新容器失败: %v —— 回滚：重新启动旧容器", err)
		if r2, e2 := dockerDo(client, "POST", "/containers/"+oldID+"/start", nil); e2 == nil {
			r2.Body.Close()
			log.Printf("[更新辅助] ✓ 旧容器已回滚启动，服务恢复")
		} else {
			log.Printf("[更新辅助] ✗ 回滚失败：%v（请手动 docker start 旧容器）", e2)
		}
		return
	}
	resp.Body.Close()
	log.Printf("[更新辅助] ✓ 新容器已启动")

	// 4. 等新容器确认运行（最多 20 秒）。只有确认 Running 才删旧容器——
	// 此前无条件删除：新镜像启动即崩时旧容器已被删，服务永久下线无人回滚
	newRunning := false
	for i := 0; i < 10; i++ {
		insp, err := dockerRequestJSON(client, "GET", "/containers/"+newID+"/json", nil)
		if err == nil {
			if st, ok := insp["State"].(map[string]interface{}); ok {
				if run, _ := st["Running"].(bool); run {
					newRunning = true
					break
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	if !newRunning {
		log.Printf("[更新辅助] ✗ 新容器 20 秒内未进入运行状态（疑似新镜像启动失败）——保留新旧两容器供排查，不删除旧容器")
		// 尝试把旧容器拉起来兜底（新容器可能占用端口，起不来时至少日志里有明确指引）
		if r2, e2 := dockerDo(client, "POST", "/containers/"+oldID+"/start", nil); e2 == nil {
			r2.Body.Close()
			log.Printf("[更新辅助] ✓ 旧容器已重新启动（若端口被新容器占用导致失败，请手动: docker rm -f <新容器> && docker start %s）", truncateStr(oldID, 12))
		} else {
			log.Printf("[更新辅助] ✗ 旧容器启动失败: %v（请手动排查: docker ps -a，新容器=%s）", e2, truncateStr(newID, 12))
		}
		return
	}
	if resp, err := dockerDo(client, "DELETE", "/containers/"+oldID+"?force=1", nil); err == nil {
		resp.Body.Close()
	}
	log.Printf("[更新辅助] ✅ 更新完成（旧容器已清理）")
}

func dockerDo(client *http.Client, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, "http://docker"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

// selfContainerID 定位自身容器 ID：
// /proc/self/cgroup 形如 .../docker-<64位ID>.scope（cgroup v2）或 /docker/<64位ID>（v1），最可靠；
// HOSTNAME 默认等于容器短 ID，但用户自定义 hostname 时会失效，仅作兜底。
func selfContainerID() string {
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		re := regexp.MustCompile(`docker[/-]([0-9a-f]{12,64})`)
		for _, line := range strings.Split(string(b), "\n") {
			if m := re.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return os.Getenv("HOSTNAME")
}

// dockerFindSelfByImage 按镜像名兜底定位自身：唯一运行中的 strmhub 镜像容器视为自己
func dockerFindSelfByImage(client *http.Client) (string, error) {
	resp, err := dockerDo(client, "GET", "/containers/json?all=1", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", err
	}
	hit := ""
	for _, it := range list {
		img := strings.ToLower(it.Image)
		name := ""
		if len(it.Names) > 0 {
			name = strings.TrimPrefix(it.Names[0], "/")
		}
		if it.State == "running" && (strings.Contains(img, "/strmhub") || strings.HasSuffix(img, "strmhub:latest") || strings.Contains(name, "strmhub")) {
			if hit != "" {
				return "", fmt.Errorf("发现多个 strmhub 容器，无法确定自身")
			}
			hit = it.ID
		}
	}
	return hit, nil
}

// dockerListContainerNames 列出守护进程内所有容器名（自更新失败时诊断用）
func dockerListContainerNames(client *http.Client) ([]string, error) {
	resp, err := dockerDo(client, "GET", "/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list []struct {
		Names []string `json:"Names"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	names := []string{}
	for _, it := range list {
		if len(it.Names) > 0 {
			names = append(names, strings.TrimPrefix(it.Names[0], "/"))
		}
	}
	return names, nil
}

func dockerRequestJSON(client *http.Client, method, path string, body []byte) (map[string]interface{}, error) {
	resp, err := dockerDo(client, method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 150))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
