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
// 已落盘的存量在启动时迁过来，分两段：
//
//  1. 台账：视频行还是旧名（xxx.mkv.strm）的，改成新名（同名冲突的保留旧名）；
//  2. 本地对账（reconcileStrmArtifacts）：以台账为准，把本地还叫旧名的产物收拾掉 ——
//     xxx.mkv.strm → xxx.strm、xxx.mkv.nfo → xxx.nfo、xxx.mkv-thumb.jpg → xxx-thumb.jpg；
//     新名已经有了（网盘同步下来的 xxx.nfo、本地已有 xxx.strm）就删掉旧名那份。
//
// 第 2 段只看台账与磁盘，不关心文件是怎么变成旧名的，所以这些情况都能收拾：
// 第一次迁移（文件与台账都是旧名）、台账已迁但本地从备份拷回了旧名文件、
// 以及迁移之后 Emby 替还没清掉的旧条目补存的 xxx.mkv-thumb.jpg
// （2026-09-28 现场：只有部分集出现，按旧条目的路径命名，网盘上没有）。
// 最后这种会在迁移之后才冒出来，所以迁完一小时再对账一遍（strmMigrateResweep）。
//
// 为什么在启动时同步跑、而且排在任何后台任务之前（SetupRoutes 里，调度器启动之前）：
// 迁移期间台账与本地文件短暂不一致，这时增量同步、整理、深度删除任何一个插进来都会读到半截状态。
// 只动本地磁盘与数据库、不发 115 请求，万级文件也就几秒。
//
// 顺序与幂等：台账行「先改文件、再改台账」，台账写失败就把文件改回去；有失败就不打完成标记，
// 下次启动再跑（两段都幂等）。本地一个 STRM 都看不到（挂载未就绪）时整个跳过。
//
// 迁完 Emby 会把每一集看成「删一条、加一条」（观看记录随之丢失，维护者已确认可以接受），
// 随之而来的 library.deleted / library.new 在静默窗口内按回声处理，不推通知、不触发深删，
// 见 strmMigrateEcho。

// strmMigrateKey 完成标记。v1（migrate.strm_noext）只按台账迁、不对账本地，
// 本地被从备份拷回旧名文件后就再也迁不动了；v2 换了 key，在已迁过的部署上再跑一遍
const strmMigrateKey = "migrate.strm_noext.v2"

// strmMigrateQuietTTL 迁移后的静默窗口。迁完就提交了媒体库刷新，
// 万级库 Emby 扫完一般在一小时内，留足余量
const strmMigrateQuietTTL = 6 * time.Hour

// strmMigrateResweep 迁完之后再对账一遍的延迟：等 Emby 把旧条目清掉、它补存的旧名图片落了地
const strmMigrateResweep = time.Hour

type strmMigrateStats struct {
	Strm       int // 台账改成新名的视频行
	Restored   int // 本地旧名 STRM 改成台账里的名字（或删掉与之重复的旧名 STRM）
	Companions int // 改成新名的本地配套文件
	Stale      int // 新名已存在、删掉的旧名配套文件
	Conflicts  int // 台账新名被占用、保留旧名的视频行
	Failed     int // 改名 / 删除 / 写台账失败的（下次启动重试）
	oldPaths   []string
	newPaths   []string
	libDirs    map[string]bool // 涉及的媒体库顶层目录（绝对路径），迁完按它刷新 Emby
}

func (s strmMigrateStats) touched() int {
	return s.Strm + s.Restored + s.Companions + s.Stale
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
	if st.touched()+st.Conflicts+st.Failed == 0 {
		return
	}
	log.Printf("[迁移] ✓ STRM 改名迁移（xxx.mkv.strm → xxx.strm）：台账 %d 行、本地 STRM 改名 %d 个、"+
		"配套文件改名 %d 个、删除旧名重复 %d 个、同名冲突保留旧名 %d 个、失败 %d 个，用时 %s",
		st.Strm, st.Restored, st.Companions, st.Stale, st.Conflicts, st.Failed, time.Since(start).Round(time.Millisecond))
	if st.Failed > 0 {
		log.Printf("[迁移] ○ 有 %d 个没迁成（详见上方日志），下次启动重试", st.Failed)
	}
	if st.touched() == 0 {
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
	time.AfterFunc(strmMigrateResweep, func() { resweepStrmArtifacts(db, root) })
}

// resweepStrmArtifacts 迁完一小时后再对账一遍：收拾 Emby 替旧条目补存的旧名图片。
// 这时后台任务都在跑了，拿 taskMu 排队，别和增量 / 整理同时动本地树
func resweepStrmArtifacts(db *gorm.DB, root string) {
	for !taskMu.Acquire("STRM 改名迁移复查", 10*time.Second) {
		select {
		case <-stopCh:
			return
		default:
		}
	}
	defer taskMu.Unlock()
	st := strmMigrateStats{libDirs: map[string]bool{}}
	if err := reconcileStrmArtifacts(db, root, &st); err != nil {
		log.Printf("[迁移] ○ STRM 改名迁移复查失败: %v", err)
		return
	}
	if st.touched()+st.Failed > 0 {
		log.Printf("[迁移] ✓ STRM 改名迁移复查：本地 STRM 改名 %d 个、配套文件改名 %d 个、删除旧名重复 %d 个、失败 %d 个",
			st.Restored, st.Companions, st.Stale, st.Failed)
		armStrmMigrateQuiet(st.oldPaths, st.newPaths)
	}
}

// migrateStrmNames 迁移本体（不碰 Setting 标记，便于测试）
func migrateStrmNames(db *gorm.DB, root string) (st strmMigrateStats, err error) {
	st.libDirs = map[string]bool{}
	var rows []model.SyncedFile
	if err := db.Where("kind = ? AND rel_path LIKE ?", "video", "%.strm").Find(&rows).Error; err != nil {
		return st, err
	}
	if len(rows) == 0 {
		return st, nil
	}
	// 本地媒体树必须在：挂载没就绪时一个 STRM 都看不到，这时改台账 / 删文件都会和
	// 稍后挂上来的内容对不上 —— 宁可这次不迁
	if strings.TrimSpace(root) == "" {
		return st, fmt.Errorf("未配置本地媒体目录")
	}
	if _, err := os.ReadDir(root); err != nil {
		return st, fmt.Errorf("本地媒体目录不可访问: %w", err)
	}
	if !anyLocalStrm(root, rows) {
		return st, fmt.Errorf("台账里的 %d 个 STRM 在本地一个都找不到（%s），疑似挂载未就绪", len(rows), root)
	}
	occupied, err := ledgerPathSet(db)
	if err != nil {
		return st, err
	}

	// ---- 1) 台账：旧名视频行改新名 ----
	for _, r := range rows {
		video := strings.TrimSuffix(path.Base(r.RelPath), ".strm")
		if !isVideoName(video) {
			continue // 已经是新写法
		}
		oldRel := r.RelPath
		newRel := path.Join(path.Dir(oldRel), strmNameOf(video))
		if occupied[newRel] {
			st.Conflicts++ // 同目录同基名的另一个视频占着新名：这个保留旧写法
			continue
		}
		oldAbs := filepath.Join(root, filepath.FromSlash(oldRel))
		newAbs := filepath.Join(root, filepath.FromSlash(newRel))
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
		if err := db.Model(&model.SyncedFile{}).Where("id = ?", r.ID).Update("rel_path", newRel).Error; err != nil {
			if renamed {
				_ = os.Rename(newAbs, oldAbs)
			}
			log.Printf("[迁移] ✗ 台账更新失败 %s: %v", oldRel, err)
			st.Failed++
			continue
		}
		st.Strm++
		st.noteMove(root, oldRel, oldAbs, newAbs)
	}

	// ---- 2) 本地对账 ----
	return st, reconcileStrmArtifacts(db, root, &st)
}

// reconcileStrmArtifacts 以台账为准收拾本地的旧名产物（见文件头第 2 段）。
//
// 对台账里每个新名的视频行 S.strm，在它所在目录找「S.<视频扩展名>」开头的文件：
//   - S.mkv.strm（台账里没有它）：本地旧名 STRM。S.strm 不在就改过去，在就删掉这份重复的；
//   - S.mkv.nfo / S.mkv-thumb.jpg …：新名（S.nfo / S-thumb.jpg）空着就改过去，已有就删掉旧名这份。
//
// 不碰的：台账里有的文件（网盘上真叫这个名字）；同名冲突、保留旧写法的那个视频的产物
// （台账里有 S.mkv.strm）；同目录里基名更长的另一个视频的产物（按最长基名认主人）
func reconcileStrmArtifacts(db *gorm.DB, root string, st *strmMigrateStats) error {
	var rows []model.SyncedFile
	if err := db.Where("kind = ? AND rel_path LIKE ?", "video", "%.strm").Find(&rows).Error; err != nil {
		return err
	}
	occupied, err := ledgerPathSet(db)
	if err != nil {
		return err
	}
	stems := map[string][]string{} // 目录 → 这个目录里新名视频行的基名
	for _, r := range rows {
		stem := strings.TrimSuffix(path.Base(r.RelPath), ".strm")
		if isVideoName(stem) {
			continue // 保留旧写法的行（同名冲突）：它的产物本来就该叫旧名
		}
		dir := path.Dir(r.RelPath)
		stems[dir] = append(stems[dir], stem)
	}
	for dir, ss := range stems {
		dirAbs := filepath.Join(root, filepath.FromSlash(dir))
		ents, err := os.ReadDir(dirAbs)
		if err != nil {
			continue // 目录不在（还没落盘 / 已删）：没什么可收拾的
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			stem, full, tail, ok := oldNamedArtifact(name, ss)
			if !ok {
				continue
			}
			oldRel := path.Join(dir, name)
			if occupied[oldRel] || occupied[path.Join(dir, full+".strm")] {
				continue // 网盘镜像，或保留旧写法的那个视频的产物
			}
			isStrm := tail == ".strm"
			newName := stem + tail
			oldAbs, newAbs := filepath.Join(dirAbs, name), filepath.Join(dirAbs, newName)
			// STRM 的新名在台账里本来就有（那就是它自己的行），只看磁盘上是不是已经有了；
			// 配套文件的新名在台账里 = 网盘同步下来的同名文件，本地没落盘也不能占它的位置
			if fileExists(newAbs) || (!isStrm && occupied[path.Join(dir, newName)]) {
				// 新名已有（网盘同步下来的 S.nfo、已落盘的 S.strm）：旧名这份是重复的
				if err := os.Remove(oldAbs); err != nil && !os.IsNotExist(err) {
					log.Printf("[迁移] ✗ 删除旧名重复文件失败 %s: %v", oldRel, err)
					st.Failed++
					continue
				}
				db.Where("path = ?", oldAbs).Delete(&model.UploadMark{})
				if isStrm {
					st.Restored++
					st.noteMove(root, oldRel, oldAbs, newAbs)
				} else {
					st.Stale++
				}
				continue
			}
			if err := os.Rename(oldAbs, newAbs); err != nil {
				if !os.IsNotExist(err) {
					log.Printf("[迁移] ✗ 改名失败 %s: %v", oldRel, err)
					st.Failed++
				}
				continue
			}
			// 回传标记跟着搬：不搬的话元数据回传会把改了名的文件当成新产物再传一遍
			db.Model(&model.UploadMark{}).Where("path = ?", oldAbs).Update("path", newAbs)
			if isStrm {
				st.Restored++
				st.noteMove(root, oldRel, oldAbs, newAbs)
			} else {
				st.Companions++
			}
		}
	}
	return nil
}

// oldNamedArtifact name 是不是 stems 里某个视频按旧命名留下的产物：
// 「基名.<视频扩展名>」后面紧跟 . 或 -（S.mkv.strm、S.mkv.nfo、S.mkv-thumb.jpg）。
// 多个基名都对得上时取最长的。返回主人基名、带扩展名的视频全名、其后的部分
func oldNamedArtifact(name string, stems []string) (stem, full, tail string, ok bool) {
	for _, s := range stems {
		if len(s) > len(stem) && strings.HasPrefix(name, s+".") {
			stem = s
		}
	}
	if stem == "" {
		return "", "", "", false
	}
	rest := name[len(stem)+1:]
	i := strings.IndexAny(rest, ".-")
	if i <= 0 || !videoExts["."+strings.ToLower(rest[:i])] {
		return "", "", "", false
	}
	tail = rest[i:]
	if tail != ".strm" && strings.HasSuffix(strings.ToLower(tail), ".strm") {
		return "", "", "", false // S.mkv.part2.strm 之类是别的视频的 STRM，不是这个的产物
	}
	return stem, stem + "." + rest[:i], tail, true
}

// anyLocalStrm 台账里的 STRM 本地至少看得到一个（台账里的名字，或同目录里旧名的那份）
func anyLocalStrm(root string, rows []model.SyncedFile) bool {
	for _, r := range rows {
		if fileExists(filepath.Join(root, filepath.FromSlash(r.RelPath))) {
			return true
		}
		stem := strings.TrimSuffix(path.Base(r.RelPath), ".strm")
		ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(path.Dir(r.RelPath))))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if _, _, tail, ok := oldNamedArtifact(e.Name(), []string{stem}); ok && tail == ".strm" {
				return true
			}
		}
	}
	return false
}

// ledgerPathSet 台账里全部 rel_path
func ledgerPathSet(db *gorm.DB) (map[string]bool, error) {
	var all []string
	if err := db.Model(&model.SyncedFile{}).Pluck("rel_path", &all).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, p := range all {
		out[p] = true
	}
	return out, nil
}

// noteMove 记下一次 STRM 路径变化：静默窗口认回声、迁完刷新哪些媒体库
func (s *strmMigrateStats) noteMove(root, oldRel, oldAbs, newAbs string) {
	s.oldPaths = append(s.oldPaths, oldAbs)
	s.newPaths = append(s.newPaths, newAbs)
	if seg := strings.SplitN(oldRel, "/", 2); len(seg) == 2 {
		s.libDirs[filepath.Join(root, seg[0])] = true
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

// armStrmMigrateQuiet 登记（或追加）静默路径并把窗口续到从现在起 strmMigrateQuietTTL
func armStrmMigrateQuiet(oldPaths, newPaths []string) {
	strmMigrateQuiet.mu.Lock()
	defer strmMigrateQuiet.mu.Unlock()
	if strmMigrateQuiet.paths == nil || time.Now().After(strmMigrateQuiet.until) {
		strmMigrateQuiet.paths = make(map[string]bool, len(oldPaths)+len(newPaths))
	}
	strmMigrateQuiet.until = time.Now().Add(strmMigrateQuietTTL)
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
