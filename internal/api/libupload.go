package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"115-station/internal/model"
)

// 监控上传（2026-10-24 起按库）：把本地媒体目录里新产生的 NFO / 图片（本站刮削或 Emby 写的）
// 回传到同一个库的 115 目录。开关在每个库上（model.Library.Upload，「账号与媒体库 → 媒体库」的库弹窗），
// 取代原来全局的 monitor.enabled 与单独的「监控上传」页：几个库要不要回传本来就各不相同。
// 别的账号的库用那个账号的 Cookie 传（V2 起各账号平等，MULTI-ACCOUNT-V2-PLAN.md §10）。
// 原来另有一条 5 分钟一轮的「元数据回传」兜底引擎：监控目录固定成媒体根之后它开头就返回、从来没跑过，一并删了

const libUploadMigrateKey = "migrate.lib_upload.v1"

// MigrateLibUpload 老配置开着全局上传的，换成主号各库的开关（附加账号的库原来一律不传，照旧关着）。
// 要排在 MigrateLibrariesV2 之后：表还空、之后才由它建出来的库，建行时已按同一口径换算（libstore.go），
// 所以这里不管表空不空都只跑一次 —— 否则用户后来手动建的库会被老开关顺手打开
func (h *Handler) MigrateLibUpload() {
	if h.DB == nil || h.Config == nil {
		return
	}
	var done int64
	h.DB.Model(&model.Setting{}).Where("key = ?", libUploadMigrateKey).Count(&done)
	if done > 0 {
		return
	}
	if parseMonitorUploadCfg(h.getSettingValue("monitor")).Enabled {
		res := h.DB.Model(&model.Library{}).Where("account = ?", acctMain).Update("upload", true)
		if res.Error != nil {
			log.Printf("[上传] ✗ 迁移：把原来的上传开关换成每个库的开关失败，下次启动再试: %v", res.Error)
			return
		}
		forgetLibraries()
		if res.RowsAffected > 0 {
			log.Printf("[上传] ✓ 原来开着「允许上传到 115」：已给主账号的 %d 个媒体库打开上传，之后在每个库上单独开关", res.RowsAffected)
		}
	}
	if err := h.DB.Create(&model.Setting{Key: libUploadMigrateKey, Value: "1"}).Error; err != nil {
		log.Printf("[上传] ○ 迁移完成标记写入失败（下次启动再跑一遍，结果不变）: %v", err)
	}
}

// uploadLibs 开着上传的库
func uploadLibs() []model.Library {
	var out []model.Library
	for _, l := range loadLibraries() {
		if l.Upload {
			out = append(out, l)
		}
	}
	return out
}

// uploadLibOn 这个库现在还开着上传。扫描到上传之间用户可能刚关掉（保存时 forgetLibraries，这里马上看得到），
// 上传是风控敏感操作，每传一个之前再看一次
func uploadLibOn(id uint) bool {
	for _, l := range loadLibraries() {
		if l.ID == id {
			return l.Upload
		}
	}
	return false
}

// uploadAs 按账号上传：主号照旧（OpenAPI 已授权走 OpenAPI，否则 Cookie）；别的账号只走 Cookie 通道 ——
// OpenAPI 授权的是主号，拿它往别的账号的目录 cid 上传，不是报错就是传进主号
func (h *Handler) uploadAs(acct uint, cookie string, pid int64, name string, data []byte) error {
	if acct == acctMain {
		return h.upload115FileConsented(cookie, pid, name, data)
	}
	if cookie == "" {
		return fmt.Errorf("账号 %d 没有可用的 Cookie", acct)
	}
	return h.upload115FileCookie(cookie, pid, name, data)
}

// libPanRootCache 别的账号的库根路径（主号走 PathCache；别的账号不写 PathCache，见 AGENTS.md「多个 115 账号」）。
// Emby 刮削时每分钟都可能有新文件，不缓存就是每轮一次祖先链请求
var (
	libPanRootMu    sync.Mutex
	libPanRootCache = map[string]libPanRootHit{}
)

type libPanRootHit struct {
	abs string
	at  time.Time
}

const libPanRootTTL = 10 * time.Minute

// libPanRoot 库的 115 目录现在的绝对路径（库目录在 115 上改了名也跟着变，所以不用 Library.Path：那是选目录时记下给人看的）
func libPanRoot(cookie string, l model.Library) (string, error) {
	if l.Account == acctMain {
		if p := absPathOf(cookie, l.Cid); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("查不到 115 目录 %s 的位置", l.Cid)
	}
	key := acctLibKey(l.Account, l.Cid)
	libPanRootMu.Lock()
	hit, ok := libPanRootCache[key]
	libPanRootMu.Unlock()
	if ok && time.Since(hit.at) < libPanRootTTL {
		return hit.abs, nil
	}
	p, err := acctDirAbs(cookie, l.Cid)
	if err != nil {
		return "", err
	}
	if p == "" || p == "/" {
		return "", fmt.Errorf("查不到 115 目录 %s 的位置", l.Cid)
	}
	libPanRootMu.Lock()
	libPanRootCache[key] = libPanRootHit{abs: p, at: time.Now()}
	libPanRootMu.Unlock()
	return p, nil
}

// joinPanRel 网盘库根 + 库内相对目录
func joinPanRel(root, rel string) string {
	rel = strings.Trim(strings.ReplaceAll(rel, `\`, "/"), "/")
	root = strings.TrimRight(root, "/")
	if rel == "" || rel == "." {
		if root == "" {
			return "/"
		}
		return root
	}
	return root + "/" + rel
}

// isMonitorUploadFile 监控上传认的文件：Emby / Jellyfin 标准命名的图片 + 任意 .nfo
func isMonitorUploadFile(name string) bool {
	n := strings.ToLower(name)
	if strings.HasSuffix(n, ".nfo") {
		return true
	}
	switch strings.ToLower(filepath.Ext(n)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return isStandardMediaImageName(n)
	}
	return false
}

// scanUploadCandidates 一个库的本地目录里最近 24 小时新产生、还没按当前指纹传过的元数据文件
func scanUploadCandidates(h *Handler, local string, cutoff time.Time) []string {
	var out []string
	filepath.WalkDir(local, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isMonitorUploadFile(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().After(cutoff) {
			return nil
		}
		if uploadStampDone(h.DB, p, fileStamp{modTime: info.ModTime(), size: info.Size()}) {
			return nil // 已上传且未变化（DB 持久，重启不重复）
		}
		out = append(out, p)
		return nil
	})
	return out
}

// StartMonitorUploader 启动监控上传：每分钟扫一遍开着上传的库
func StartMonitorUploader(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				monitorOnce(h)
			case <-stopCh:
				return
			}
		}
	}()
	log.Println("[监控上传] 引擎已启动（每个库单独开关，默认关；开着的库每分钟扫描一次）")
}

// monitorRunning 触发轮与分钟 ticker 轮防重叠（同时走两轮会重复上传）
var monitorRunning atomic.Bool

// monitorOnce 单轮扫描上传
func monitorOnce(h *Handler) {
	libs := uploadLibs()
	if len(libs) == 0 {
		return
	}
	if !monitorRunning.CompareAndSwap(false, true) {
		return // 上一轮还没跑完，跳过本轮
	}
	defer monitorRunning.Store(false)
	root := mediaRoot()
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, l := range libs {
		h.uploadLibOnce(root, l, cutoff)
	}
}

// uploadLibOnce 一个库的一轮：本地目录按相对路径对到这个库的 115 目录（本地 STRM 树与网盘同布局）
func (h *Handler) uploadLibOnce(root string, l model.Library, cutoff time.Time) {
	local := filepath.Join(root, filepath.FromSlash(normLibLocal(l.Local)))
	files := scanUploadCandidates(h, local, cutoff)
	if len(files) == 0 {
		return
	}
	cookie, err := h.cookieOf(l.Account)
	if err != nil {
		vlog("[上传] ○ 媒体库「%s」的账号用不了，本轮跳过: %v", l.Name, err)
		return
	}
	panRoot, err := libPanRoot(cookie, l)
	if err != nil {
		vlog("[上传] ○ 媒体库「%s」认不出 115 目录位置，本轮跳过: %v", l.Name, err)
		return
	}
	skipped, ok := 0, 0
	skipDirs := map[string]bool{}
	for _, f := range files {
		rel, err := filepath.Rel(local, f)
		if err != nil {
			continue
		}
		target := joinPanRel(panRoot, filepath.ToSlash(filepath.Dir(rel)))
		cid, found := cloudPathCid(cookie, target)
		if !found {
			// 115 无对应目录（多为播放器自建的 <集名>.mkv/ 本地元数据目录）：
			// 每目录只记一条，同目录后续文件静默跳过——逐文件逐轮刷屏无意义
			metaSkipAnnounce(target)
			skipped++
			skipDirs[target] = true
			continue
		}
		if !uploadLibOn(l.ID) {
			log.Printf("[上传] ○ 媒体库「%s」的上传刚被关掉，本轮停下", l.Name)
			return
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if err := h.uploadAs(l.Account, cookie, parseI64(cid), filepath.Base(f), data); err != nil {
			log.Printf("[上传] ✗ 上传失败 %s/%s: %v", l.Name, filepath.ToSlash(rel), err)
			continue
		}
		if st, ok := stampOfPath(f); ok {
			markUploadedStamp(h.DB, f, st)
		}
		ok++
		log.Printf("[上传] ✓ 上传成功: %s/%s → %s", l.Name, filepath.ToSlash(rel), target)
	}
	skippedAnnounceSummary(skipped, skipDirs)
	log.Printf("[上传] 媒体库「%s」发现 %d 个新刮削文件（图片+NFO），上传 %d 个", l.Name, len(files), ok)
}
