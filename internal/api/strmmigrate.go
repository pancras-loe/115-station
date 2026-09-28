package api

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"115-station/internal/model"
)

// ==================== 存量 STRM 改名迁移（一次性） ====================
//
// STRM 命名从「视频全名.strm」改成「视频去扩展名.strm」（原因见 strmname.go）。
// 已落盘的存量在启动时一次性迁过来：本地文件改名、台账 rel_path 跟着改、
// 本地配套文件（刮削写的 xxx.mp4.nfo、Emby 存的 xxx.mp4-thumb.jpg 之类）一并改名。
//
// 为什么在启动时同步跑、而且排在任何后台任务之前（SetupRoutes 里，调度器启动之前）：
// 迁移期间台账与本地文件短暂不一致，这时增量同步、整理、深度删除任何一个插进来都会读到半截状态。
// 只动本地磁盘与数据库、不发 115 请求，万级文件也就几秒。
//
// 顺序与幂等：逐条「先改文件、再改台账」，台账写失败就把文件改回去。
// 中途崩了下次启动接着迁：没迁的行还是旧名，已迁的行不会再被选中；
// 旧文件已不在、新文件已在的（上次改了文件没来得及改台账）只补台账。
// 有失败就不打完成标记，下次启动再试。
//
// 迁完 Emby 会把每一集看成「删一条、加一条」（观看记录随之丢失，维护者已确认可以接受），
// 随之而来的 library.deleted / library.new 在静默窗口内按回声处理，不推通知、不触发深删，
// 见 strmMigrateEcho。

const strmMigrateKey = "migrate.strm_noext"

// strmMigrateQuietTTL 迁移后的静默窗口。迁完就提交了媒体库刷新，
// 万级库 Emby 扫完一般在一小时内，留足余量
const strmMigrateQuietTTL = 6 * time.Hour

type strmMigrateStats struct {
	Strm       int // 迁移的 STRM（台账已改）
	Companions int // 跟着改名的本地配套文件
	Conflicts  int // 新名字已被占用、保留旧名的（STRM 或配套文件）
	Failed     int // 改名或写台账失败的（下次启动重试）
	oldPaths   []string
	newPaths   []string
	libDirs    map[string]bool // 涉及的媒体库顶层目录（绝对路径），迁完按它刷新 Emby
}

// MigrateStrmNames 启动时调用一次（SetupRoutes 里、后台任务启动之前）
func MigrateStrmNames(db *gorm.DB) {
	if db == nil {
		return
	}
	var done int64
	db.Model(&model.Setting{}).Where("key = ?", strmMigrateKey).Count(&done)
	if done > 0 {
		return
	}
	root := localMediaRoot()
	start := time.Now()
	st, err := migrateStrmNames(db, root)
	if err != nil {
		log.Printf("[迁移] ✗ STRM 改名迁移未执行，下次启动重试: %v", err)
		return
	}
	if st.Failed == 0 {
		if err := db.Create(&model.Setting{Key: strmMigrateKey, Value: "1"}).Error; err != nil {
			log.Printf("[迁移] ○ STRM 改名迁移完成标记写入失败（下次启动再跑一遍，结果不变）: %v", err)
		}
	}
	if st.Strm+st.Companions+st.Conflicts+st.Failed == 0 {
		return
	}
	log.Printf("[迁移] ✓ STRM 改名迁移（xxx.mp4.strm → xxx.strm）：STRM %d 个、配套文件 %d 个、同名冲突保留旧名 %d 个、失败 %d 个，用时 %s",
		st.Strm, st.Companions, st.Conflicts, st.Failed, time.Since(start).Round(time.Millisecond))
	if st.Failed > 0 {
		log.Printf("[迁移] ○ 有 %d 个没迁成（详见上方日志），下次启动重试", st.Failed)
	}
	if st.Strm == 0 {
		return
	}
	armStrmMigrateQuiet(st.oldPaths, st.newPaths)
	// 让 Emby 尽快把新路径扫进来、旧条目清掉。媒体库顶层目录在 Emby 媒体库之上时
	// notifyEmbyPaths 会整库刷新，这正是要的
	libs := make([]string, 0, len(st.libDirs))
	for d := range st.libDirs {
		libs = append(libs, d)
	}
	go notifyEmbyPaths(libs, embyRefreshAdded)
}

// migrateStrmNames 迁移本体（不碰 Setting 标记，便于测试）
func migrateStrmNames(db *gorm.DB, root string) (st strmMigrateStats, err error) {
	st.libDirs = map[string]bool{}
	var rows []model.SyncedFile
	if err := db.Where("kind = ? AND rel_path LIKE ?", "video", "%.strm").Find(&rows).Error; err != nil {
		return st, err
	}
	type todoItem struct {
		row    model.SyncedFile
		video  string // 视频全名（旧 STRM 名去掉 .strm）
		newRel string
	}
	var todo []todoItem
	for _, r := range rows {
		video := strings.TrimSuffix(path.Base(r.RelPath), ".strm")
		if !isVideoName(video) {
			continue // 已经是新写法
		}
		todo = append(todo, todoItem{row: r, video: video, newRel: path.Join(path.Dir(r.RelPath), strmNameOf(video))})
	}
	if len(todo) == 0 {
		return st, nil
	}

	// 本地媒体树必须在：挂载没就绪时旧文件一个都看不到，这时只改台账会让台账与
	// 稍后挂上来的文件对不上 —— 宁可这次不迁
	if strings.TrimSpace(root) == "" {
		return st, fmt.Errorf("未配置本地媒体目录")
	}
	if _, err := os.ReadDir(root); err != nil {
		return st, fmt.Errorf("本地媒体目录不可访问: %w", err)
	}
	present := 0
	for _, it := range todo {
		if fileExists(filepath.Join(root, filepath.FromSlash(it.row.RelPath))) ||
			fileExists(filepath.Join(root, filepath.FromSlash(it.newRel))) {
			present++
			break
		}
	}
	if present == 0 {
		return st, fmt.Errorf("台账里的 %d 个 STRM 在本地一个都找不到（%s），疑似挂载未就绪", len(todo), root)
	}

	// 台账已占用的路径：新名字撞上了别的行（同目录同基名的另一个视频、或同名的附属文件）就保留旧名
	var all []string
	if err := db.Model(&model.SyncedFile{}).Pluck("rel_path", &all).Error; err != nil {
		return st, err
	}
	occupied := make(map[string]bool, len(all))
	for _, p := range all {
		occupied[p] = true
	}
	listing := map[string][]os.DirEntry{} // 目录 → 迁移开始前的内容（配套文件按它找）

	for _, it := range todo {
		oldRel := it.row.RelPath
		if occupied[it.newRel] {
			st.Conflicts++
			continue
		}
		oldAbs := filepath.Join(root, filepath.FromSlash(oldRel))
		newAbs := filepath.Join(root, filepath.FromSlash(it.newRel))
		renamed := false
		if _, err := os.Stat(oldAbs); err == nil {
			if err := os.Rename(oldAbs, newAbs); err != nil {
				log.Printf("[迁移] ✗ 改名失败 %s: %v", oldRel, err)
				st.Failed++
				continue
			}
			renamed = true
		} else if !os.IsNotExist(err) {
			log.Printf("[迁移] ✗ 读不到 %s: %v", oldRel, err)
			st.Failed++
			continue
		}
		if err := db.Model(&model.SyncedFile{}).Where("id = ?", it.row.ID).Update("rel_path", it.newRel).Error; err != nil {
			if renamed {
				_ = os.Rename(newAbs, oldAbs)
			}
			log.Printf("[迁移] ✗ 台账更新失败 %s: %v", oldRel, err)
			st.Failed++
			continue
		}
		delete(occupied, oldRel)
		occupied[it.newRel] = true
		st.Strm++
		st.oldPaths = append(st.oldPaths, oldAbs)
		st.newPaths = append(st.newPaths, newAbs)
		if seg := strings.SplitN(oldRel, "/", 2); len(seg) == 2 {
			st.libDirs[filepath.Join(root, seg[0])] = true
		}

		dirRel := path.Dir(oldRel)
		dirAbs := filepath.Join(root, filepath.FromSlash(dirRel))
		ents, ok := listing[dirAbs]
		if !ok {
			ents, _ = os.ReadDir(dirAbs)
			listing[dirAbs] = ents
		}
		migrateStrmCompanions(db, dirRel, dirAbs, it.video, path.Base(oldRel), ents, occupied, &st)
	}
	return st, nil
}

// migrateStrmCompanions 本地配套文件跟着 STRM 改名：xxx.mp4.nfo → xxx.nfo、xxx.mp4-thumb.jpg → xxx-thumb.jpg。
//
// 只动本地生成的：台账里有的（网盘上真有这个名字的文件，同步下来的镜像）不动，
// 改了就和网盘对不上。新名字已存在（网盘带来的 xxx.nfo 已同步下来）也不动 ——
// 那份和网盘一致，旧名那份留着，Emby 不会再读它
func migrateStrmCompanions(db *gorm.DB, dirRel, dirAbs, video, oldStrm string, ents []os.DirEntry,
	occupied map[string]bool, st *strmMigrateStats) {
	stem := strmStemOf(video)
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || name == oldStrm || strings.HasSuffix(strings.ToLower(name), ".strm") {
			continue
		}
		if !strings.HasPrefix(name, video+".") && !strings.HasPrefix(name, video+"-") {
			continue
		}
		oldRel := path.Join(dirRel, name)
		if occupied[oldRel] {
			continue
		}
		newName := stem + name[len(video):]
		newRel := path.Join(dirRel, newName)
		oldAbs, newAbs := filepath.Join(dirAbs, name), filepath.Join(dirAbs, newName)
		if occupied[newRel] || fileExists(newAbs) {
			st.Conflicts++
			continue
		}
		if err := os.Rename(oldAbs, newAbs); err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[迁移] ✗ 配套文件改名失败 %s: %v", oldRel, err)
				st.Failed++
			}
			continue
		}
		// 回传标记跟着搬：不搬的话元数据回传会把改了名的文件当成新产物再传一遍
		db.Model(&model.UploadMark{}).Where("path = ?", oldAbs).Update("path", newAbs)
		st.Companions++
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---- 迁移后的 Emby 回声静默 ----

var strmMigrateQuiet struct {
	mu    sync.Mutex
	until time.Time
	paths map[string]bool // embyDelKey(本地路径)：迁移前后的 STRM 路径
}

func armStrmMigrateQuiet(oldPaths, newPaths []string) {
	strmMigrateQuiet.mu.Lock()
	defer strmMigrateQuiet.mu.Unlock()
	strmMigrateQuiet.until = time.Now().Add(strmMigrateQuietTTL)
	strmMigrateQuiet.paths = make(map[string]bool, len(oldPaths)+len(newPaths))
	for _, ps := range [][]string{oldPaths, newPaths} {
		for _, p := range ps {
			if k := embyDelKey(p); k != "" {
				strmMigrateQuiet.paths[k] = true
			}
		}
	}
}

// strmMigrateEcho 这条 Emby 入库 / 删除事件是不是 STRM 改名迁移的回声（精确到文件，只在静默窗口内）。
// 只认迁移动过的那几个路径：窗口内别处真的新增 / 删除照常通知
func strmMigrateEcho(localPath string) bool {
	k := embyDelKey(localPath)
	if k == "" {
		return false
	}
	strmMigrateQuiet.mu.Lock()
	defer strmMigrateQuiet.mu.Unlock()
	if strmMigrateQuiet.paths == nil || time.Now().After(strmMigrateQuiet.until) {
		strmMigrateQuiet.paths = nil
		return false
	}
	return strmMigrateQuiet.paths[k]
}
