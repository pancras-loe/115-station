package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"
)

// ==================== 整理落盘（一条龙） ====================
//
// 整理在 115 上搬完文件时，targetDir / fid / pickcode / 重命名后的新名全都在手里。
// 此前这些信息被丢弃，STRM 交给增量同步从生活事件里反推——绕一圈要付 3 秒沉淀、
// 风控敏感的事件轮询、逐层爬目录链算路径、事件不带 pickcode 时整目录重遍历的代价，
// 大批量入库还可能被事件窗口冲掉导致 STRM 静默缺失。
//
// 现在整理自己落盘：识别 → 改名 → 移动 → 写 STRM / 下附属 → 刮削 → 刷 Emby 是一条单向链。
// 增量同步收缩回它本来的职责：只管 115 端的外部变更（手机上传、离线下载、网页端删改）。
//
// 刮削与 Emby 刷新不在每个条目里做，而是整轮跑完统一 flush——一部 52 集的剧
// 只刮一次、只刷一次（MoviePilot 的 scrape_batch 同款思路）。

// scrapeJob 一个待刮削的片目（标题目录粒度）
type scrapeJob struct {
	Key    string // 标题目录相对路径：库名/电影|剧集/分类/标题目录
	Kind   string // movie / tv
	Title  string
	Year   string
	TmdbID int
}

// orgSink 整理产出的落盘出口：整理引擎是一组自由函数（没有 *Handler），
// 落盘所需的配置与本轮累积的刮削/刷新目标都挂在这里逐层传下去
type orgSink struct {
	h         *Handler
	localRoot string // 本地媒体库根（full 配置的 local_path）
	domain    string
	format    string
	keepExt   bool
	skipExist bool
	batchID   string

	scrapeOn  bool
	scrapeCfg scrapeCfg

	// libCid/libName 懒解析：取库名要打一次 115 接口，而绝大多数轮次
	// 待整理目录是空的、根本走不到落盘。别为空转付这次调用
	libCid      string
	libName     string
	libResolved bool

	mu          sync.Mutex
	jobs        map[string]scrapeJob // key 去重：一部剧的多个条目只刮一次
	refreshDirs []string             // 本轮动过的库内目录（含库名前缀）
	records     []*model.OrganizeRecord
}

// newOrgSink 构建落盘出口。libCid 是媒体库根 cid（落盘时才拿它去解析库名）
func (h *Handler) newOrgSink(libCid string) *orgSink {
	domain, format, keepExt, skipExist := h.getStrmConfig()
	sc := loadScrapeCfg()
	return &orgSink{
		h: h, localRoot: localMediaRoot(), libCid: libCid,
		domain: domain, format: format, keepExt: keepExt, skipExist: skipExist,
		batchID:   time.Now().Format("20060102150405"),
		scrapeOn:  sc.AutoAfterOrganize && (sc.WriteNFO || sc.WriteImages),
		scrapeCfg: sc,
		jobs:      map[string]scrapeJob{},
	}
}

// resolveLibName 取媒体库根目录名（STRM 路径第一层）。
// 必须与全量/增量同步算出来的一致，否则同一部片会在本地落到两棵目录树下，
// 所以取法与 executeFullSync 完全相同。只解析一次，且只在真要落盘时才解析
func (s *orgSink) resolveLibName() string {
	if s.libResolved {
		return s.libName
	}
	s.libResolved = true
	if s.h == nil || s.libCid == "" {
		return s.libName
	}
	if cookie, err := s.h.get115Cookie(); err == nil && cookie != "" {
		if info, err := get115DirInfo(cookie, s.libCid); err == nil {
			s.libName = info.n
		}
	}
	return s.libName
}

// libRel 把库内相对路径拼上库名（STRM 台账里的 RelPath 都带库名前缀）
func (s *orgSink) libRel(rel string) string {
	return path.Join(s.resolveLibName(), strings.Trim(rel, "/"))
}

// commit 把本条目移动完成后的文件落到本地：视频写 .strm，附属文件（字幕/NFO/封面）下载。
//
//	rootRel  标题目录的库内相对路径（电影/剧集 → 分类 → 标题目录），刮削与 Emby 刷新的单位
//	mediaRel 视频与字幕的实际落点（电影同 rootRel，剧集为 rootRel/Season XX）
//	videos / assets 的 Name 必须是**重命名之后的最终文件名**
func (s *orgSink) commit(ops *pan115Ops, media *TmdbMedia, rootRel, mediaRel string, videos, assets []remoteFile) (strmCreated, downloaded int) {
	if len(videos) == 0 && len(assets) == 0 {
		return 0, 0
	}
	vs := make([]remoteFile, 0, len(videos))
	var handover []string // 整理落不了盘、要交回增量同步兜底的 fid
	for _, f := range videos {
		if f.PickCode == "" {
			// 没有 pickcode 写不出直链。必须同时撤销事件抑制，否则这个文件
			// 两头落空：整理没写 STRM，增量又把它的事件跳过了
			log.Printf("[整理] ○ %s 缺 pickcode，STRM 交由增量同步补齐", f.Name)
			handover = append(handover, f.Fid)
			continue
		}
		f.Path = s.libRel(mediaRel)
		vs = append(vs, f)
	}
	as := make([]remoteFile, 0, len(assets))
	for _, f := range assets {
		if f.PickCode == "" {
			handover = append(handover, f.Fid)
			continue
		}
		// NFO / 封面落在标题目录，字幕跟着视频走
		if isTitleLevelAsset(f.Name) {
			f.Path = s.libRel(rootRel)
		} else {
			f.Path = s.libRel(mediaRel)
		}
		as = append(as, f)
	}

	// 落盘逻辑整套复用全量同步的 applySyncResults：视频写 strm + 批量 upsert 台账，
	// 附属文件 5 并发下载。整理这边不该再有第二份实现
	// 落盘明细只在单文件条目（电影/散文件）时点名：剧集动辄几十集，
	// 逐条打就是几十行，而调用方的汇总行已经给了目录与各类数量
	if len(vs)+len(as) == 1 {
		for _, f := range append(append([]remoteFile{}, vs...), as...) {
			vlog("[整理]     · 落盘 → %s", filepath.Join(s.localRoot, filepath.FromSlash(f.Path), f.Name))
		}
	}
	sc, dl, _, fl := applySyncResults(model.DB, ops, vs, as, s.localRoot, s.domain, s.format, s.keepExt, s.skipExist, rootRel)
	if fl > 0 {
		log.Printf("[整理] ○ %s：%d 个附属文件下载失败（增量同步下轮会重试）", rootRel, fl)
	}
	// 写 STRM 失败的视频同样要交回增量：applySyncResults 内部失败只打日志，
	// 台账里不会有它的行，这里按「登记数少于送进去的数」反推
	if sc < len(vs) {
		for _, f := range vs {
			var n int64
			model.DB.Model(&model.SyncedFile{}).Where("file_id = ?", f.Fid).Count(&n)
			if n == 0 {
				handover = append(handover, f.Fid)
			}
		}
	}
	unmarkSuppressed(handover...)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshDirs = append(s.refreshDirs, s.libRel(rootRel))
	if media != nil && media.TmdbID > 0 {
		key := s.libRel(rootRel)
		if _, ok := s.jobs[key]; !ok {
			s.jobs[key] = scrapeJob{Key: key, Kind: media.MediaType, Title: media.Title, Year: media.Year, TmdbID: media.TmdbID}
		}
	}
	return sc, dl
}

// isTitleLevelAsset NFO 与标准封面图放在标题目录（Emby 按此约定读取），
// 字幕等跟随视频。与 organize.go 的 classifyFile 分流口径保持一致
func isTitleLevelAsset(name string) bool {
	switch classifyFile(name) {
	case FileTypeNFO, FileTypeStdImage:
		return true
	}
	return false
}

// flushScrape 本轮整理结束后统一刮削：只刮本轮真的动过的片目。
// 此前是「增量同步动过媒体库 → 扫全台账刮一遍」，新增一部片也要全库过一遍
func (s *orgSink) flushScrape() {
	s.mu.Lock()
	jobs := make([]scrapeJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	if len(jobs) == 0 || !s.scrapeOn {
		return
	}
	if s.scrapeCfg.LocalRoot == "" {
		log.Printf("[影视刮削] ○ 未配置本地媒体库根目录，跳过整理后刮削")
		return
	}
	tc, err := loadTmdbClient()
	if err != nil {
		log.Printf("[影视刮削] ○ TMDB 未配置，跳过整理后刮削: %v", err)
		return
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Key < jobs[j].Key })

	scrapeMu.Lock()
	if scrapeSt.Running {
		scrapeMu.Unlock()
		log.Printf("[影视刮削] ○ 全量刮削进行中，本轮整理的 %d 个片目交由它一并覆盖", len(jobs))
		return
	}
	scrapeSt = scrapeStatus{Running: true, Total: len(jobs), Errors: []string{}}
	scrapeStopFlag = false
	scrapeMu.Unlock()
	defer func() {
		scrapeMu.Lock()
		scrapeSt.Running = false
		scrapeSt.Current = ""
		scrapeMu.Unlock()
	}()

	log.Printf("[影视刮削] ▶ 整理完成，刮削本轮 %d 个片目", len(jobs))
	for _, j := range jobs {
		// 「停止刮削」按钮与优雅退出对这一轮同样有效（口径与 scrapeAll 一致）
		if scrapeStopRequested() {
			log.Printf("[影视刮削] ○ 收到停止请求，整理后刮削提前结束")
			return
		}
		scrapeMu.Lock()
		scrapeSt.Current = j.Title
		scrapeMu.Unlock()
		dir := filepath.Join(s.scrapeCfg.LocalRoot, filepath.FromSlash(strings.Trim(j.Key, "/")))
		s.h.scrapeOne(tc, s.scrapeCfg, dir, j.Key, j.Kind, j.Title, j.Year, j.TmdbID)
		scrapeMu.Lock()
		scrapeSt.Done++
		scrapeMu.Unlock()
		time.Sleep(150 * time.Millisecond) // TMDB 限速保护
	}
	// 用户允许上传时回传 115：等最后一写落盘再跑，不用等分钟级 ticker
	go func() {
		time.Sleep(2 * time.Second)
		monitorOnce(s.h)
		s.h.uploadMetadataOnce()
	}()
}

// flushRefresh 通知 Emby 刷新：只传本轮受影响的最浅目录（传库根等于全刷）
func (s *orgSink) flushRefresh() {
	s.mu.Lock()
	dirs := append([]string(nil), s.refreshDirs...)
	s.mu.Unlock()
	if len(dirs) == 0 {
		return
	}
	shallowest := dirs[0]
	for _, d := range dirs[1:] {
		if len(d) < len(shallowest) {
			shallowest = d
		}
	}
	s.h.notifyEmbyRefresh(filepath.Join(s.localRoot, filepath.FromSlash(shallowest)))
}

// dropLocalByFids 按 fid 删除本地已落盘的 strm / 附属文件及台账行，
// 返回实际删掉的相对路径（调用方打日志用——只报个数出了问题查不了）。
// 洗版让位与「重新整理」回滚旧产出时用：留着就是指向已删文件的死 strm
func dropLocalByFids(localRoot string, fids []string) []string {
	removed, _ := dropLocalByFidsExcept(localRoot, fids, nil)
	return removed
}

// dropLocalByFidsExcept 同上，但 keepPaths[fid] 与台账里的相对路径一致时保留不删。
// 「重新整理到同一个 TMDB 条目」这种原地刷新场景下，文件落点根本没变，
// 删了再原样写回纯属多余，附属文件还要重新下载一遍
func dropLocalByFidsExcept(localRoot string, fids []string, keepPaths map[string]string) (removed []string, kept int) {
	for _, fid := range fids {
		if fid == "" {
			continue
		}
		var sf model.SyncedFile
		if model.DB.Where("file_id = ?", fid).First(&sf).Error != nil {
			continue
		}
		if want, ok := keepPaths[fid]; ok && want == sf.RelPath {
			kept++ // 台账行也留着，commit 会 upsert 覆盖。逐条打没意义，调用方汇总
			continue
		}
		if sf.RelPath != "" {
			full := filepath.Join(localRoot, filepath.FromSlash(sf.RelPath))
			if err := os.Remove(full); err == nil || os.IsNotExist(err) {
				removed = append(removed, sf.RelPath)
				removeEmptyParents(filepath.Dir(full), localRoot)
			} else {
				log.Printf("[整理] ✗ 清理旧文件失败 %s: %v", sf.RelPath, err)
			}
		}
		model.DB.Delete(&model.SyncedFile{}, sf.ID)
	}
	return removed, kept
}

// removeEmptyParents 自下而上删空目录，到 root 为止（不删 root 自身）。
// 重新整理把文件挪走之后，旧的标题目录/季目录会空在那里被 Emby 当成空剧集
func removeEmptyParents(dir, root string) {
	root = filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil {
			return
		}
		rel, rerr := filepath.Rel(root, dir)
		if rerr != nil {
			rel = dir
		}
		log.Printf("[整理] ○ 已删除本地空目录: %s", filepath.ToSlash(rel))
		dir = filepath.Dir(dir)
	}
}

// orgRecordFile 整理记录里登记的单个文件。fid 在 115 上移动/改名后不变，
// 所以 fid + 最终文件名就是「重新整理」原地捞回所需的全部信息
type orgRecordFile struct {
	Fid      string `json:"fid"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // video / subtitle / meta / junk
	PickCode string `json:"pickcode,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Sha1     string `json:"sha1,omitempty"`
}

func marshalRecordFiles(files []orgRecordFile) string {
	if len(files) == 0 {
		return ""
	}
	b, err := json.Marshal(files)
	if err != nil {
		return ""
	}
	return string(b)
}

func unmarshalRecordFiles(s string) []orgRecordFile {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []orgRecordFile
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// recordFileKind 按文件名给出记录里的归类标签
func recordFileKind(name string) string {
	switch classifyFile(name) {
	case FileTypeVideo:
		return "video"
	case FileTypeSubtitle:
		return "subtitle"
	case FileTypeNFO, FileTypeStdImage:
		return "meta"
	}
	return "junk"
}

// note 落一条整理记录（成功与失败都留痕）
func (s *orgSink) note(rec *model.OrganizeRecord) {
	if rec == nil || model.DB == nil {
		return
	}
	rec.BatchID = s.batchID
	if err := model.DB.Create(rec).Error; err != nil {
		log.Printf("[整理] ○ 整理记录写入失败（不影响整理本身）: %v", err)
		return
	}
	// 回写下载记录：这批内容如果是某条离线/分享链接下来的，把识别结果记到那一行上
	dlLinkClaim(model.DB, rec)
	s.mu.Lock()
	s.records = append(s.records, rec)
	s.mu.Unlock()
}

// noteFail 失败/未识别的快捷登记
func (s *orgSink) noteFail(source, fid, kind, status, stage, msg string, files []orgRecordFile) {
	s.note(&model.OrganizeRecord{
		Source: source, SourceFid: fid, SourceKind: kind,
		Status: status, Stage: stage, Message: msg,
		Files: marshalRecordFiles(files),
	})
}

// summaryLine 本轮记录的一行大白话汇总（日志用）
func (s *orgSink) summaryLine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) == 0 {
		return ""
	}
	var ok, fail, exists int
	strm := 0
	for _, r := range s.records {
		switch r.Status {
		case "success":
			ok++
		case "exists":
			exists++
		default:
			fail++
		}
		strm += r.StrmCreated
	}
	return fmt.Sprintf("成功 %d（生成 STRM %d）· 已存在 %d · 失败 %d", ok, strm, exists, fail)
}

// sumSizes 文件总字节数（整理记录的入库体积）
func sumSizes(files []remoteFile) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// strmTotal 本轮整理直接生成的 STRM 总数
func (s *orgSink) strmTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		n += r.StrmCreated
	}
	return n
}

// appendToRecord 把同前缀兄弟文件并进主记录（散文件批量场景）。
// rec 为 nil（主文件没落成记录）时直接忽略
func (s *orgSink) appendToRecord(rec *model.OrganizeRecord, files []orgRecordFile, strmCreated int, size int64) {
	if rec == nil || rec.ID == 0 || len(files) == 0 {
		return
	}
	s.mu.Lock()
	merged := append(unmarshalRecordFiles(rec.Files), files...)
	rec.Files = marshalRecordFiles(merged)
	rec.VideoCount += len(files)
	rec.StrmCreated += strmCreated
	rec.TotalSize += size
	s.mu.Unlock()
	model.DB.Model(rec).Updates(map[string]interface{}{
		"files": rec.Files, "video_count": rec.VideoCount,
		"strm_created": rec.StrmCreated, "total_size": rec.TotalSize,
	})
}

// localMediaRoot 本地媒体库根目录（全量同步配置的 local_path）。
// 不依赖 *Handler：洗版等自由函数也要按它定位已落盘的 strm
func localMediaRoot() string {
	var cfg struct {
		LocalPath string `json:"local_path"`
	}
	if json.Unmarshal([]byte(settingValueCompat("full")), &cfg) == nil && cfg.LocalPath != "" {
		return cfg.LocalPath
	}
	return defaultLocalPath
}
