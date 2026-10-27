package api

// ==================== 备份、恢复与定时备份 ====================
//
// 备份包是一个 zip：
//   - config/*            配置目录第一层的全部文件（setting.yaml、auth.yaml、jwt.key、
//     115 Cookie / 设备 / OpenAPI Token、TLS 证书……），子目录不收
//   - data/115-station.db 数据库一致性快照（VACUUM INTO，不锁库）
//   - manifest.json       版本、时间、来源（手动 / 定时 / 恢复前）
//
// 不收的：DATA_DIR 下的图片缓存、CloakBrowser、日志——都能重新生成，收进来只会让包变大。
//
// **恢复不在运行中替换文件**：数据库正开着，setting.yaml 也有内存缓存，
// 运行中覆盖要么被下一次保存写回去，要么让 SQLite 读到一半新一半旧的页。
// 所以恢复分两步：接口只校验并解包到 DATA_DIR/restore-pending（暂存），
// 下次启动时、打开数据库之前由 ApplyPendingRestore 换进去。暂存之前先自动备份一份当前状态
// （kind=prerestore），恢复错了能退回来。
//
// 定时备份挂在 StartSyncScheduler 的分钟调度上（cron 命中），只读本地文件与数据库，
// 不碰 115，也就不进任务队列、不拿 taskMu。

import (
	"115-station/internal/config"

	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	backupSettingKey = "backup"
	backupDBName     = "115-station.db"
	backupDBEntry    = "data/" + backupDBName
	backupManifest   = "manifest.json"
	restorePending   = "restore-pending"
	restoreResult    = ".restore-result.json"
	// 上传的备份包上限：台账大的库数据库能到几百 MB，再大多半传错了文件
	backupUploadMax = 4 << 30
	// 恢复前自动留的那份只留最近几份，否则来回恢复几次就堆满
	backupKeepPrerestore = 3
)

// backupCfg 定时备份配置（setting `backup`）
type backupCfg struct {
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	// Keep 定时备份保留份数；手动备份与恢复前备份不算在里面，也不会被它清掉
	Keep int `json:"keep"`
	// Dir 备份存放目录，空 = DATA_DIR/backups。想放到别的挂载卷（NAS 另一块盘）就填绝对路径
	Dir string `json:"dir"`
}

func defaultBackupCfg() backupCfg {
	return backupCfg{Enabled: false, Cron: "0 4 * * *", Keep: 7}
}

func loadBackupCfg() backupCfg {
	cfg := defaultBackupCfg()
	if raw := settingValueCompat(backupSettingKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	if cfg.Keep <= 0 {
		cfg.Keep = 7
	}
	if strings.TrimSpace(cfg.Cron) == "" {
		cfg.Cron = defaultBackupCfg().Cron
	}
	return cfg
}

func (h *Handler) backupDir() string {
	if d := strings.TrimSpace(loadBackupCfg().Dir); d != "" && filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(h.Config.DataDir, "backups")
}

// backupManifestData 备份包里的 manifest.json
type backupManifestData struct {
	App       string    `json:"app"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Kind      string    `json:"kind"` // manual / auto / prerestore
	HasDB     bool      `json:"has_db"`
	Files     []string  `json:"files"`
}

var (
	// backupMu 同一时刻只做一份备份：定时与手动撞在一起时 VACUUM INTO 两遍纯属浪费
	backupMu sync.Mutex
	// 最近一次定时备份的结果（内存里就够了：备份文件本身就在列表里）
	backupLastAuto   time.Time
	backupLastAutoOK bool
	backupLastErr    string
)

// 备份文件名：115-station-backup-20261017-040000-auto.zip。只认这个形态，
// 下载 / 删除 / 恢复接口拿名字拼路径，别的名字一律拒绝（防 ../）
var backupNameRe = regexp.MustCompile(`^115-station-backup-\d{8}-\d{6}(-[a-z]+)?(-\d+)?\.zip$`)

// writeBackupZip 把配置目录与数据库快照写成 zip
func (h *Handler) writeBackupZip(w io.Writer, kind string) (backupManifestData, error) {
	man := backupManifestData{App: "115-station", Version: buildVersion, CreatedAt: time.Now(), Kind: kind}
	zw := zip.NewWriter(w)

	addFile := func(src, name string) error {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(st)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		zf, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, f)
		return err
	}

	ents, err := os.ReadDir(h.Config.ConfigDir)
	if err != nil {
		return man, fmt.Errorf("读取配置目录失败: %w", err)
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		if err := addFile(filepath.Join(h.Config.ConfigDir, e.Name()), "config/"+e.Name()); err != nil {
			return man, fmt.Errorf("打包 %s 失败: %w", e.Name(), err)
		}
		man.Files = append(man.Files, "config/"+e.Name())
	}

	// sqlite 一致性快照（VACUUM INTO 不锁库）。快照放在备份目录旁边而不是系统临时目录：
	// 容器里 /tmp 常是小 tmpfs，几百 MB 的库放不下
	snapDir := filepath.Join(h.Config.DataDir, ".backup-tmp")
	_ = os.MkdirAll(snapDir, 0o755)
	snap := filepath.Join(snapDir, fmt.Sprintf("db-%d.db", time.Now().UnixNano()))
	defer os.Remove(snap)
	if err := h.DB.Exec("VACUUM INTO ?", snap).Error; err != nil {
		return man, fmt.Errorf("数据库快照失败: %w", err)
	}
	if err := addFile(snap, backupDBEntry); err != nil {
		return man, fmt.Errorf("打包数据库失败: %w", err)
	}
	man.HasDB = true
	man.Files = append(man.Files, backupDBEntry)

	mw, err := zw.Create(backupManifest)
	if err != nil {
		return man, err
	}
	if err := json.NewEncoder(mw).Encode(man); err != nil {
		return man, err
	}
	return man, zw.Close()
}

// backupItem 备份列表的一项
type backupItem struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Kind      string    `json:"kind"`
}

func backupKindOf(name string) string {
	switch {
	case strings.Contains(name, "-auto"):
		return "auto"
	case strings.Contains(name, "-prerestore"):
		return "prerestore"
	default:
		return "manual"
	}
}

// createBackupFile 在备份目录里生成一份备份（先写 .part 再改名，列表里不会出现写了一半的包）
func (h *Handler) createBackupFile(kind string) (backupItem, error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	dir := h.backupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return backupItem{}, fmt.Errorf("创建备份目录失败: %w", err)
	}
	now := time.Now()
	base := "115-station-backup-" + now.Format("20060102-150405")
	if kind != "manual" {
		base += "-" + kind
	}
	name := base + ".zip"
	// 同一秒点两次：加序号，别覆盖上一份
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
	final := filepath.Join(dir, name)
	part := final + ".part"
	f, err := os.Create(part)
	if err != nil {
		return backupItem{}, fmt.Errorf("创建备份文件失败: %w", err)
	}
	_, werr := h.writeBackupZip(f, kind)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(part)
		return backupItem{}, werr
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return backupItem{}, err
	}
	st, _ := os.Stat(final)
	item := backupItem{Name: name, CreatedAt: now, Kind: kind}
	if st != nil {
		item.Size = st.Size()
	}
	switch kind {
	case "auto":
		h.pruneBackups("auto", loadBackupCfg().Keep)
	case "prerestore":
		h.pruneBackups("prerestore", backupKeepPrerestore)
	}
	return item, nil
}

func (h *Handler) listBackups() []backupItem {
	ents, err := os.ReadDir(h.backupDir())
	if err != nil {
		return nil
	}
	var out []backupItem
	for _, e := range ents {
		if !e.Type().IsRegular() || !backupNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, backupItem{Name: e.Name(), Size: info.Size(), CreatedAt: info.ModTime(), Kind: backupKindOf(e.Name())})
	}
	// 文件名里的时间戳到秒（前缀 19 字 + 15 字），同一秒的几份再按修改时间：
	// 直接比整个名字的话 `-auto-2.zip` 会排在 `-auto.zip` 后面
	stamp := func(n string) string { return n[:34] }
	sort.Slice(out, func(i, j int) bool {
		if a, b := stamp(out[i].Name), stamp(out[j].Name); a != b {
			return a > b
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// pruneBackups 只清指定来源的旧备份，保留最新 keep 份（listBackups 已按新到旧排好）
func (h *Handler) pruneBackups(kind string, keep int) {
	n := 0
	for _, b := range h.listBackups() {
		if b.Kind != kind {
			continue
		}
		n++
		if n > keep {
			if err := os.Remove(filepath.Join(h.backupDir(), b.Name)); err == nil {
				log.Printf("[备份] ○ 清理旧备份 %s（%s 保留 %d 份）", b.Name, kind, keep)
			}
		}
	}
}

// backupTick 分钟调度：cron 命中就后台做一份定时备份
func (h *Handler) backupTick(now time.Time) {
	cfg := loadBackupCfg()
	if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
		return
	}
	go func() {
		item, err := h.createBackupFile("auto")
		backupLastAuto = time.Now()
		backupLastAutoOK = err == nil
		if err != nil {
			backupLastErr = err.Error()
			log.Printf("[备份] ✗ 定时备份失败: %v", err)
			NotifyMessage(kindTask, "✗ 定时备份失败", err.Error())
			return
		}
		backupLastErr = ""
		log.Printf("[备份] ✓ 定时备份完成 %s（%s）", item.Name, humanBytes(int(item.Size)))
	}()
}

// ==================== 恢复 ====================

// restorePendingInfo 暂存待恢复的备份（restore-pending/pending.json）
type restorePendingInfo struct {
	Source    string             `json:"source"` // 来源：备份文件名 / 上传的文件名
	StagedAt  time.Time          `json:"staged_at"`
	Manifest  backupManifestData `json:"manifest"`
	SafetyBak string             `json:"safety_backup,omitempty"`
}

// restoreResultInfo 上次启动时恢复的结果（给界面显示，恢复发生在日志落盘之前）
type restoreResultInfo struct {
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Source string    `json:"source"`
	Error  string    `json:"error,omitempty"`
}

// validateBackupZip 只收本站自己的备份包：条目只能是 config/第一层文件、数据库、manifest；
// 数据库要过 SQLite 文件头。返回 manifest（老版本导出的包没有，按内容补）
func validateBackupZip(zr *zip.Reader) (backupManifestData, error) {
	var man backupManifestData
	hasDB, hasSetting := false, false
	for _, f := range zr.File {
		name := f.Name
		switch {
		case name == backupManifest:
			rc, err := f.Open()
			if err != nil {
				return man, err
			}
			err = json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&man)
			rc.Close()
			if err != nil {
				return man, fmt.Errorf("manifest.json 解析失败: %w", err)
			}
		case name == backupDBEntry:
			rc, err := f.Open()
			if err != nil {
				return man, err
			}
			head := make([]byte, 16)
			_, err = io.ReadFull(rc, head)
			rc.Close()
			if err != nil || string(head) != "SQLite format 3\x00" {
				return man, errors.New("数据库文件不是有效的 SQLite 文件")
			}
			hasDB = true
		case strings.HasPrefix(name, "config/"):
			rest := strings.TrimPrefix(name, "config/")
			if rest == "" || strings.ContainsAny(rest, `/\`) || rest == "." || rest == ".." {
				if f.FileInfo().IsDir() && rest == "" {
					continue
				}
				return man, fmt.Errorf("备份包里有不认识的条目：%s", name)
			}
			if rest == "setting.yaml" {
				hasSetting = true
			}
		case name == "data/" && f.FileInfo().IsDir():
		default:
			return man, fmt.Errorf("备份包里有不认识的条目：%s", name)
		}
	}
	if !hasDB && !hasSetting {
		return man, errors.New("不是 115-Station 的备份包（没有数据库也没有 setting.yaml）")
	}
	if man.App == "" {
		man.App = "115-station"
	}
	man.HasDB = hasDB
	if man.Files == nil {
		for _, f := range zr.File {
			if f.Name != backupManifest && !f.FileInfo().IsDir() {
				man.Files = append(man.Files, f.Name)
			}
		}
	}
	return man, nil
}

// checkSQLiteFile 打开解出来的数据库跑一次 quick_check：文件头对了不代表没截断
func checkSQLiteFile(path string) error {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var res string
	if err := db.Raw("PRAGMA quick_check").Scan(&res).Error; err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("数据库校验未通过：%s", res)
	}
	var n int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='settings'").Scan(&n).Error; err != nil || n == 0 {
		return errors.New("数据库里没有本站的表，不像是 115-Station 的数据库")
	}
	return nil
}

// stageRestore 校验备份包并解到 restore-pending，下次启动时换进去
func (h *Handler) stageRestore(zipPath, source string) (restorePendingInfo, error) {
	var info restorePendingInfo
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return info, fmt.Errorf("打不开备份包: %w", err)
	}
	defer zr.Close()
	man, err := validateBackupZip(&zr.Reader)
	if err != nil {
		return info, err
	}

	pending := filepath.Join(h.Config.DataDir, restorePending)
	tmp := pending + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "config"), 0o700); err != nil {
		return info, err
	}
	if err := os.MkdirAll(filepath.Join(tmp, "data"), 0o755); err != nil {
		return info, err
	}
	fail := func(e error) (restorePendingInfo, error) {
		_ = os.RemoveAll(tmp)
		return info, e
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Name == backupManifest {
			continue
		}
		// 条目名已由 validateBackupZip 限定在 config/<文件> 与 data/115-station.db
		dst := filepath.Join(tmp, filepath.FromSlash(f.Name))
		if err := extractZipFile(f, dst); err != nil {
			return fail(fmt.Errorf("解包 %s 失败: %w", f.Name, err))
		}
	}
	if man.HasDB {
		if err := checkSQLiteFile(filepath.Join(tmp, "data", backupDBName)); err != nil {
			return fail(err)
		}
	}

	// 暂存之前先留一份当前状态：恢复错了还能从这份恢复回来
	safety, err := h.createBackupFile("prerestore")
	if err != nil {
		return fail(fmt.Errorf("恢复前备份当前状态失败，已中止: %w", err))
	}

	info = restorePendingInfo{Source: source, StagedAt: time.Now(), Manifest: man, SafetyBak: safety.Name}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "pending.json"), b, 0o600); err != nil {
		return fail(err)
	}
	_ = os.RemoveAll(pending)
	if err := os.Rename(tmp, pending); err != nil {
		return fail(err)
	}
	log.Printf("[备份] ✓ 已暂存待恢复的备份 %s（恢复前的状态另存为 %s），重启后生效", source, safety.Name)
	return info, nil
}

func extractZipFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func readPendingRestore(dataDir string) *restorePendingInfo {
	b, err := os.ReadFile(filepath.Join(dataDir, restorePending, "pending.json"))
	if err != nil {
		return nil
	}
	var info restorePendingInfo
	if json.Unmarshal(b, &info) != nil {
		return nil
	}
	return &info
}

// ApplyPendingRestore 启动时、打开数据库之前把暂存的备份换进去（main.go 调用）。
// 返回 true 表示换过了：配置目录里的 jwt.key 可能变了，调用方要重新 config.Load。
//
// 只覆盖备份包里有的文件，配置目录里多出来的不删（老版本导出的包不带 115 设备 / OpenAPI Token，
// 删了等于恢复一次就把现在的登录态丢掉）。数据库连同 -wal / -shm 一起换掉，
// 否则旧库残留的 WAL 会被 SQLite 回放到新库上。
// 失败时把暂存目录改名成 restore-failed-<时间>，别每次启动都再撞一次。
func ApplyPendingRestore(cfg *config.Config) bool {
	pending := filepath.Join(cfg.DataDir, restorePending)
	info := readPendingRestore(cfg.DataDir)
	if info == nil {
		if _, err := os.Stat(pending); err == nil {
			_ = os.RemoveAll(pending) // 没有 pending.json 的是暂存到一半的残留
		}
		return false
	}
	res := restoreResultInfo{At: time.Now(), Source: info.Source}
	err := applyRestoreFiles(pending, cfg)
	if err != nil {
		res.Error = err.Error()
		log.Printf("[备份] ✗ 恢复备份 %s 失败: %v（暂存保留在 restore-failed-*，恢复前的状态见 %s）", info.Source, err, info.SafetyBak)
		_ = os.Rename(pending, filepath.Join(cfg.DataDir, "restore-failed-"+time.Now().Format("20060102-150405")))
	} else {
		res.OK = true
		log.Printf("[备份] ✓ 已从备份 %s 恢复配置与数据库", info.Source)
		_ = os.RemoveAll(pending)
	}
	b, _ := json.Marshal(res)
	_ = os.WriteFile(filepath.Join(cfg.DataDir, restoreResult), b, 0o600)
	return res.OK
}

func applyRestoreFiles(pending string, cfg *config.Config) error {
	cfgSrc := filepath.Join(pending, "config")
	ents, err := os.ReadDir(cfgSrc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		return err
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		if err := replaceFile(filepath.Join(cfgSrc, e.Name()), filepath.Join(cfg.ConfigDir, e.Name())); err != nil {
			return fmt.Errorf("恢复 %s 失败: %w", e.Name(), err)
		}
	}
	dbSrc := filepath.Join(pending, "data", backupDBName)
	if _, err := os.Stat(dbSrc); err == nil {
		dbDst := filepath.Join(cfg.DataDir, backupDBName)
		for _, suf := range []string{"-wal", "-shm", "-journal"} {
			_ = os.Remove(dbDst + suf)
		}
		if err := replaceFile(dbSrc, dbDst); err != nil {
			return fmt.Errorf("恢复数据库失败: %w", err)
		}
	}
	return nil
}

// replaceFile 先拷到目标旁边再改名：配置目录与暂存目录常在两个挂载卷上，直接 rename 会跨设备失败
func replaceFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0o600)
	if st, err := os.Stat(dst); err == nil {
		mode = st.Mode().Perm()
	} else if strings.HasSuffix(dst, ".db") {
		mode = 0o644
	}
	tmp := dst + ".restore-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ==================== 重启 ====================

// restartHook 由 main 注入：走和 SIGTERM 同一套优雅退出，退出码非 0，
// 让 compose 的 restart 策略（unless-stopped / always / on-failure 都算）把容器拉起来
var restartHook func()

func SetRestartHook(fn func()) { restartHook = fn }

// ==================== 接口 ====================

// SystemBackup 现场打一份备份直接下载，不落到备份目录（GET /system/backup）
func (h *Handler) SystemBackup(c *gin.Context) {
	c.Header("Content-Disposition", "attachment; filename=115-station-backup-"+time.Now().Format("20060102-150405")+".zip")
	c.Header("Content-Type", "application/zip")
	c.Status(http.StatusOK)
	backupMu.Lock()
	defer backupMu.Unlock()
	if _, err := h.writeBackupZip(c.Writer, "manual"); err != nil {
		// 头已经发出去了，只能记日志：浏览器拿到的是一个坏 zip
		log.Printf("[备份] ✗ 导出备份失败: %v", err)
	}
}

// ListBackups GET /system/backups
func (h *Handler) ListBackups(c *gin.Context) {
	resp := gin.H{
		"items":       h.listBackups(),
		"dir":         h.backupDir(),
		"pending":     readPendingRestore(h.Config.DataDir),
		"can_restart": restartHook != nil,
	}
	if b, err := os.ReadFile(filepath.Join(h.Config.DataDir, restoreResult)); err == nil {
		var r restoreResultInfo
		// 只提示一周：恢复成功之后这条一直挂着没意义
		if json.Unmarshal(b, &r) == nil && time.Since(r.At) < 7*24*time.Hour {
			resp["last_restore"] = r
		}
	}
	if !backupLastAuto.IsZero() {
		resp["last_auto"] = gin.H{"at": backupLastAuto, "ok": backupLastAutoOK, "error": backupLastErr}
	}
	if cfg := loadBackupCfg(); cfg.Enabled {
		if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
			resp["next_auto"] = t
		}
	}
	c.JSON(http.StatusOK, resp)
}

// CreateBackup POST /system/backups：立即备份一份到备份目录
func (h *Handler) CreateBackup(c *gin.Context) {
	item, err := h.createBackupFile("manual")
	if err != nil {
		log.Printf("[备份] ✗ 手动备份失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[备份] ✓ 手动备份完成 %s（%s）", item.Name, humanBytes(int(item.Size)))
	c.JSON(http.StatusOK, item)
}

func (h *Handler) backupPathParam(c *gin.Context) (string, bool) {
	name := c.Param("name")
	if !backupNameRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备份文件名不合法"})
		return "", false
	}
	p := filepath.Join(h.backupDir(), name)
	if _, err := os.Stat(p); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "备份不存在"})
		return "", false
	}
	return p, true
}

// DownloadBackup GET /system/backups/:name
func (h *Handler) DownloadBackup(c *gin.Context) {
	p, ok := h.backupPathParam(c)
	if !ok {
		return
	}
	c.FileAttachment(p, filepath.Base(p))
}

// DeleteBackup DELETE /system/backups/:name
func (h *Handler) DeleteBackup(c *gin.Context) {
	p, ok := h.backupPathParam(c)
	if !ok {
		return
	}
	if err := os.Remove(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[备份] ○ 已删除备份 %s", filepath.Base(p))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RestoreBackup POST /system/backups/:name/restore：用备份目录里的一份暂存恢复
func (h *Handler) RestoreBackup(c *gin.Context) {
	p, ok := h.backupPathParam(c)
	if !ok {
		return
	}
	// 恢复前备份会按份数清理，可能正好清掉这一份：stageRestore 先解包再备份，不受影响
	info, err := h.stageRestore(p, filepath.Base(p))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

// UploadRestore POST /system/restore（multipart，字段 file）：上传备份包暂存恢复
func (h *Handler) UploadRestore(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, backupUploadMax)
	// 流式读 multipart 直接落盘：FormFile 超过 32MB 的部分会写进系统临时目录，
	// 容器里 /tmp 常是小 tmpfs，几百 MB 的库传到一半就满了
	mr, err := c.Request.MultipartReader()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有收到备份文件"})
		return
	}
	var filename string
	tmpDir := filepath.Join(h.Config.DataDir, ".backup-tmp")
	_ = os.MkdirAll(tmpDir, 0o755)
	tmp := filepath.Join(tmpDir, fmt.Sprintf("upload-%d.zip", time.Now().UnixNano()))
	defer os.Remove(tmp)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传内容失败: " + err.Error()})
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		filename = filepath.Base(part.FileName())
		if !strings.HasSuffix(strings.ToLower(filename), ".zip") {
			part.Close()
			c.JSON(http.StatusBadRequest, gin.H{"error": "只接受 .zip 备份包"})
			return
		}
		out, err := os.Create(tmp)
		if err != nil {
			part.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存上传文件失败: " + err.Error()})
			return
		}
		_, cerr := io.Copy(out, part)
		part.Close()
		if err := out.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "保存上传文件失败: " + cerr.Error()})
			return
		}
		break
	}
	if filename == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有收到备份文件"})
		return
	}
	info, err := h.stageRestore(tmp, filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

// CancelRestore DELETE /system/restore：取消暂存的恢复
func (h *Handler) CancelRestore(c *gin.Context) {
	_ = os.RemoveAll(filepath.Join(h.Config.DataDir, restorePending))
	log.Printf("[备份] ○ 已取消待恢复的备份")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RestartSelf POST /system/restart：优雅退出，由容器的重启策略拉起来
func (h *Handler) RestartSelf(c *gin.Context) {
	if restartHook == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "当前运行方式不支持在网页上重启"})
		return
	}
	log.Printf("[系统] ○ 收到网页重启请求，优雅退出后由容器重启策略拉起")
	c.JSON(http.StatusOK, gin.H{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond) // 先把响应发出去
		restartHook()
	}()
}
