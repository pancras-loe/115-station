package api

// 分享 STRM 的本站刮削（SHARE-STRM-PLAN.md §8）：本地整理认出是哪部的分享，用本站刮削写 NFO 与图片，
// 和整理进库的片目同一套（scrapeSession.scrapeTitleMeta）。片目不在台账里，所以不走刮削队列的执行器
// （它按台账找片目），直接在分享 STRM 的任务里刮：生成完、认出了才刮。
// 只写本地（分享不是自己的，没有网盘落点可传），也不让 Emby 提前探测（每次探测都要取一次直链）。
// 原样放的分享认不出是哪部，不刮，交给 Emby。

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// shareScrapeTitle 认出的分享 → 刮削用的片目。片目 key 用「分享根/标题目录」（媒体根下的相对路径，
// 与台账片目 key 同口径：ScrapeArtGap 按它记）
func shareScrapeTitle(root, rootRel string, media *TmdbMedia, titleDir string, files []model.ShareFile) (scrapeTitle, bool) {
	if media == nil || titleDir == "" {
		return scrapeTitle{}, false
	}
	kind := "movie"
	if media.MediaType == "tv" {
		kind = "tv"
	}
	t := scrapeTitle{Key: path.Join(rootRel, titleDir), Kind: kind, Title: media.Title, Year: media.Year, TmdbID: media.TmdbID,
		Dir: metaDest{Local: filepath.Join(root, filepath.FromSlash(titleDir))}}
	for _, f := range files {
		if f.Kind != "video" || f.Blocked || !(f.RelPath == titleDir || strings.HasPrefix(f.RelPath, titleDir+"/")) {
			continue
		}
		t.Videos = append(t.Videos, scrapeVideo{
			Name: strings.TrimSuffix(path.Base(f.RelPath), ".strm"),
			Dir:  metaDest{Local: filepath.Join(root, filepath.FromSlash(path.Dir(f.RelPath)))},
		})
	}
	return t, len(t.Videos) > 0
}

// shareScrapeReporter 刮削进度写在秒传与分享队列上，错误收进结果
type shareScrapeReporter struct{ errs []string }

func (r *shareScrapeReporter) errf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[分享STRM]   ✗ 刮削：%s", msg)
	if len(r.errs) < 20 {
		r.errs = append(r.errs, msg)
	}
}
func (r *shareScrapeReporter) stopped() bool { return xferLane.stopRequested() }
func (r *shareScrapeReporter) sub(phase string, done, total int, label string) {
	xferLane.set("刮削 · "+phase, done, total, label)
}

// scrapeShareTitle 刮这一轮认出的片目（只补缺失、只写本地）。返回写了几个文件、出错的说明
func (r *shareStrmRun) scrapeShareTitle(tc *TmdbClient, cfg shareStrmCfg) (int, []string) {
	var files []model.ShareFile
	r.db.Where("source_id = ?", r.src.ID).Find(&files)
	t, ok := shareScrapeTitle(r.root, cfg.Root, r.media, r.titleDir, files)
	if !ok {
		return 0, nil
	}
	o := loadScrapeCfg().opts()
	o.Force, o.Upload, o.Probe = false, false, false
	w := newFileScrapeWriter(nil, false, false)
	w.localRoot = mediaRoot()
	rep := &shareScrapeReporter{}
	st := newScrapeSession(tc, o, w, rep).scrapeTitleMeta(t)
	if st == nil {
		return 0, rep.errs
	}
	plogShareStrm.Printf("✦ 刮削《%s》(%s)：%s", t.Title, t.Year, st.summary())
	if len(st.Wrote) > 0 {
		notifyEmbyPathsWith([]string{t.Dir.Local}, embyRefreshAdded, false)
	}
	return len(st.Wrote), rep.errs
}

// ShareStrmScrape POST /sharestrm/sources/:id/scrape —— 给本地整理认出的分享刮一次（只补缺失、只写本地）
func (h *Handler) ShareStrmScrape(c *gin.Context) {
	src, ok := h.shareSourceParam(c)
	if !ok {
		return
	}
	if normShareLayout(src.Layout) != shareLayoutOrganize || src.TmdbID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有「本地整理」并且认出是哪部的分享能刮削；原样放的交给 Emby"})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: jobKindShareStrm, Title: "分享 STRM 刮削：" + firstNonEmpty(src.MediaTitle, src.Title, src.ShareCode),
		DedupeKey: shareJobKey(src.ID), Source: "web", Priority: jobPriorityManual,
		Params: jobParams{ShareStrm: &shareStrmJobParams{SourceID: src.ID, Scrape: true}}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": fmt.Sprintf("已排进队列（任务 #%d）：先检查一遍分享，再刮削", job.ID), "job_id": job.ID})
}
