package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

// ==================== 本地媒体根固定为 /media（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.3 / §10.8） ====================
//
// 原来「本地媒体库根目录」是一项设置（full.local_path）。现在固定：由环境变量 MEDIA_ROOT 决定，默认 /media，
// 界面上只显示不让改。各媒体库的本地目录都是它下面的相对路径（model.Library.Local）。
//
// 老用户在 compose 里挂到别处（比如 /data/strm）的，要把挂载改成 /media 再重启：
//   - 宿主机上的文件不动；Emby 有自己的挂载、路径映射本地一侧跟着根走，所以 Emby 不重扫、观看记录不丢；
//   - 台账、整理记录存的是相对路径，不受影响；
//   - 存了旧根**绝对路径**的地方（元数据回传的上传记录 UploadMark、监控上传目录）由 MigrateMediaRoot 改写，
//     不改的话元数据回传会把整库当成没传过、再往 115 传一遍；
//   - 没改挂载就升级：新根是个空目录，同步写进去重启就没了、Emby 也看不见，失效 STRM 还会把整库判成「本地没了」。
//     所以 mediaRootProblem 发现「台账里有东西、新根下一个库目录都看不到」时，同步与整理一律停下并提醒。
//
// 不在 Docker 里跑（本机调试）时用 MEDIA_ROOT 指过去。

// mediaRootLegacyForTests 只给单元测试用（testmain_test.go 的 TestMain 打开）：几十个老测试按 full.local_path
// 把本地根指到临时目录，打开后照旧认它；正式运行永远是 false，本地根只看 MEDIA_ROOT
var mediaRootLegacyForTests bool

// mediaRoot 本地媒体根
func mediaRoot() string {
	if v := strings.TrimSpace(os.Getenv("MEDIA_ROOT")); v != "" {
		return filepath.Clean(v)
	}
	if mediaRootLegacyForTests {
		if v := jsonStrField(settingValueCompat("full"), "local_path"); v != "" {
			return v
		}
	}
	return defaultLocalPath
}

// mediaRootAppliedKey 上次按哪个根改写过绝对路径（换根时据此知道旧根是谁）
const mediaRootAppliedKey = "media_root_applied"

// MigrateMediaRoot 启动时：根变了（老配置的 local_path ≠ 现在的根）就把存着旧根绝对路径的记录改成新根（幂等）
func (h *Handler) MigrateMediaRoot() {
	if h.DB == nil || h.Config == nil {
		return
	}
	newRoot := mediaRoot()
	// 老代码读 full.local_path 当本地根（显示、拼路径）：跟着改成现在的根（在算旧根之后，见下面的 defer）
	defer func() {
		if !mediaRootLegacyForTests && h.getSettingValue("full") != "" {
			_ = h.mergeSettingFields("full", map[string]string{"local_path": newRoot})
		}
	}()
	old := strings.TrimSpace(h.getSettingValue(mediaRootAppliedKey))
	if old == "" {
		old = jsonStrField(h.getSettingValue("full"), "local_path")
	}
	if old == "" {
		old = defaultLocalPath
	}
	old = filepath.Clean(old)
	if old == newRoot {
		if h.getSettingValue(mediaRootAppliedKey) == "" {
			_ = h.Config.SaveSetting(mediaRootAppliedKey, newRoot)
		}
		return
	}
	n := rewriteRootPrefix(h, old, newRoot)
	if err := h.Config.SaveSetting(mediaRootAppliedKey, newRoot); err != nil {
		log.Printf("[媒体根] ✗ 记录新根失败（下次启动再改写一遍，结果不变）: %v", err)
		return
	}
	log.Printf("[媒体根] ✓ 本地媒体根从「%s」换成「%s」：%d 条记录里的绝对路径已改写。请确认 compose 已把媒体目录挂到 %s", old, newRoot, n, newRoot)
}

// rewriteRootPrefix 把记录里以 old 开头的绝对路径换成 newRoot 开头。返回改了几条
func rewriteRootPrefix(h *Handler, old, newRoot string) int {
	total := 0
	// UploadMark：元数据回传的上传指纹按绝对路径记（upload115.go）
	var marks []model.UploadMark
	h.DB.Find(&marks)
	for _, m := range marks {
		if p, ok := swapRoot(m.Path, old, newRoot); ok {
			if err := h.DB.Model(&model.UploadMark{}).Where("id = ?", m.ID).Update("path", p).Error; err == nil {
				total++
			}
		}
	}
	// 监控上传目录（setting monitor 的 dir）
	if raw := h.getSettingValue("monitor"); raw != "" {
		if dir := jsonStrField(raw, "dir"); dir != "" {
			if p, ok := swapRoot(dir, old, newRoot); ok {
				if err := h.mergeSettingFields("monitor", map[string]string{"dir": p}); err == nil {
					total++
				}
			}
		}
	}
	return total
}

// swapRoot path 在 old 下面就换成 newRoot 下面（按整段比，斜杠两种都认）
func swapRoot(p, old, newRoot string) (string, bool) {
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/") }
	pp, oo := norm(p), norm(old)
	if oo == "" || (pp != oo && !strings.HasPrefix(pp, oo+"/")) {
		return p, false
	}
	return filepath.Join(newRoot, filepath.FromSlash(strings.TrimPrefix(pp, oo))), true
}

// ---- 根目录没挂上时停下 ----

var (
	rootProbMu   sync.Mutex
	rootProbAt   time.Time
	rootProbText string
	rootProbSent time.Time
)

// mediaRootProblem 媒体根能不能用：台账里有东西、根下却一个库目录都看不到 → 多半是 compose 没挂到这里。
// 返回给用户看的原因（空 = 没问题）。一分钟缓存；第一次发现时推一条消息（6 小时内只推一次）
func mediaRootProblem() string {
	if mediaRootLegacyForTests {
		return "" // 老测试的临时目录不按这个查（checkMediaRoot 另有测试）
	}
	rootProbMu.Lock()
	defer rootProbMu.Unlock()
	if time.Since(rootProbAt) < time.Minute {
		return rootProbText
	}
	rootProbAt = time.Now()
	rootProbText = checkMediaRoot(mediaRoot(), effectiveLibraries(nil))
	if rootProbText != "" && time.Since(rootProbSent) > 6*time.Hour {
		rootProbSent = time.Now()
		log.Printf("[媒体根] ✗ %s", rootProbText)
		go NotifyMessage(kindSystem, "⚠️ 本地媒体目录没挂上", rootProbText)
	}
	return rootProbText
}

// checkMediaRoot 纯判断（测试用）：没有台账 / 没有库 / 至少一个库目录在 → 没问题
func checkMediaRoot(root string, libs []model.Library) string {
	if model.DB == nil || len(libs) == 0 {
		return ""
	}
	var n int64
	model.DB.Model(&model.SyncedFile{}).Limit(1).Count(&n)
	if n == 0 {
		return ""
	}
	for _, l := range libs {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(normLibLocal(l.Local)))); err == nil && st.IsDir() {
			return ""
		}
	}
	return fmt.Sprintf("本地媒体根 %s 下一个媒体库目录都没有，台账里却有已同步的文件：多半是 compose 没把媒体目录挂到 %s。同步与整理先停着，改好挂载重启后自动恢复", root, root)
}

// forgetMediaRootCheck 测试用：清掉缓存
func forgetMediaRootCheck() {
	rootProbMu.Lock()
	rootProbAt, rootProbText = time.Time{}, ""
	rootProbMu.Unlock()
}
