package api

import (
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 库内同集多份体检 ====================
//
// orgdup.go 管的是「这次整理撞上的」；在它之前入库的，已经有并排躺在库里的了：
//   - 两份同名（越狱 S04E22 那种）：网盘上同目录两个一模一样的名字，本地一个是 xxx.strm、一个是 xxx.mkv.strm；
//   - 115 自动改名的：后到的那份成了 xxx(1).mkv，本地是 xxx.strm 与 xxx(1).strm。
// 只读台账（零 115 请求）找出来，有整理记录且记录里两份都在的，可以发起一次「严格」重新整理
// （strictDup：同名并排的也停下来问），之后就是记录上「选择保留」那一套。

// reAutoRenamed 115 遇到同名时给后到的加的「(1)」
var reAutoRenamed = regexp.MustCompile(`\s?\(\d+\)$`)

// ledgerDup 台账里疑似同一集的几份
type ledgerDup struct {
	dir  string // 所在目录（台账口径，含库名）
	kind string // same_name 两份同名 / auto_renamed 115 自动改名的
	rows []model.SyncedFile
}

// findLedgerDups 纯函数：按「目录 + 视频基名」分组（STRM 新旧两种写法、115 的 (1) 都归一），两份以上的就是
func findLedgerDups(rows []model.SyncedFile) []ledgerDup {
	type acc struct {
		rows []model.SyncedFile
		auto bool
	}
	groups := map[string]*acc{}
	var order []string
	for _, sf := range rows {
		if !ledgerIsVideo(sf) || sf.OrphanAt != nil {
			continue
		}
		stem := strmStemOf(ledgerName(sf)) // 旧写法 xxx.mkv.strm 也归到 xxx
		auto := reAutoRenamed.MatchString(stem)
		stem = reAutoRenamed.ReplaceAllString(stem, "")
		key := path.Dir(sf.RelPath) + "/" + stem
		g := groups[key]
		if g == nil {
			g = &acc{}
			groups[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, sf)
		g.auto = g.auto || auto
	}
	var out []ledgerDup
	for _, key := range order {
		g := groups[key]
		if len(g.rows) < 2 {
			continue
		}
		kind := "same_name"
		if g.auto {
			kind = "auto_renamed"
		}
		out = append(out, ledgerDup{dir: path.Dir(key), kind: kind, rows: g.rows})
	}
	return out
}

type dupScanFile struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size,omitempty"`
}

type dupScanItem struct {
	TitleRel string        `json:"title_rel"` // 分类 / 标题目录（整理记录的 target_dir 口径）
	Title    string        `json:"title"`
	Dir      string        `json:"dir"`
	Kind     string        `json:"kind"`
	Files    []dupScanFile `json:"files"`
	// RecordID 能发起严格重新整理的那条整理记录（两份都登记在里面）；0 = 没有，请到网盘文件页对片目「整理」
	RecordID uint `json:"record_id,omitempty"`
}

// DupScan GET /organize/dup-scan 库内疑似同集多份（只读台账）
func (h *Handler) DupScan(c *gin.Context) {
	var rows []model.SyncedFile
	h.DB.Select("id, file_id, rel_path, kind, size, sha1, orphan_at").Find(&rows)
	dups := findLedgerDups(rows)
	layout := loadLibCategoryLayout()
	items := make([]dupScanItem, 0, len(dups))
	titleRels := map[string]bool{}
	for _, d := range dups {
		it := dupScanItem{Dir: d.dir, Kind: d.kind}
		if _, titleDir, category, _, ok := layout.titleOf(d.rows[0].RelPath); ok {
			it.TitleRel, it.Title = category+"/"+titleDir, titleDir
			titleRels[it.TitleRel] = true
		}
		for _, r := range d.rows {
			it.Files = append(it.Files, dupScanFile{FileID: r.FileID, Name: ledgerName(r), Size: r.Size})
		}
		items = append(items, it)
	}
	// 找记录：同一片目最新的、两份都登记在里面的那条
	if len(titleRels) > 0 {
		rels := make([]string, 0, len(titleRels))
		for r := range titleRels {
			rels = append(rels, r)
		}
		var recs []model.OrganizeRecord
		h.DB.Select("id, target_dir, files, status").Where("target_dir IN ? AND status <> ?", rels, orgStatusAwaiting).
			Order("id DESC").Find(&recs)
		for i := range items {
			for _, r := range recs {
				if r.TargetDir != items[i].TitleRel {
					continue
				}
				have := map[string]bool{}
				for _, f := range unmarshalRecordFiles(r.Files) {
					have[f.Fid] = true
				}
				all := true
				for _, f := range items[i].Files {
					all = all && have[f.FileID]
				}
				if all {
					items[i].RecordID = r.ID
					break
				}
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Dir < items[j].Dir })
	c.JSON(http.StatusOK, gin.H{"data": items})
}

// DupScanRedo POST /organize/dup-scan/redo  body: {"record_id":1}
// 按记录现在的条目严格重新整理一次：同名并排的也停下来，在记录上选
func (h *Handler) DupScanRedo(c *gin.Context) {
	var req struct {
		RecordID uint `json:"record_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RecordID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定整理记录"})
		return
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, req.RecordID).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if err := redoPrecheck(&rec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if rec.TmdbID <= 0 || (rec.MediaType != "movie" && rec.MediaType != "tv") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这条记录没有识别结果，请先用「重新整理」指定 TMDB 条目"})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{
		Kind:      "redo",
		Title:     fmt.Sprintf("同集多份体检《%s》", shortTitle(rec.Source)),
		DedupeKey: fmt.Sprintf("record:%d", rec.ID),
		Source:    "web", Priority: jobPriorityManual,
		Params: jobParams{RecordIDs: []uint{rec.ID}, TmdbID: rec.TmdbID, MediaType: rec.MediaType, StrictDup: true},
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "同集多份体检")
}
