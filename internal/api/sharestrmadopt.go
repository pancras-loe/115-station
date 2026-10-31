package api

// 接管别的项目写的分享 STRM（SHARE-STRM-PLAN.md §9）：p115strmhelper 的「分享 STRM」内容是
// {MP}/…/redirect_url?share_code=&receive_code=&id=（utils/strm.py get_share_strm_url，只读）。
// 从它换过来时，把分享 STRM 根设成那些 STRM 所在的目录（不能和媒体库重叠），这里扫一遍：
// 按分享码登记 ShareSource、按文件登记 ShareFile，内容换成本站 /d/s~ 直链，**路径与修改时间都不动**
// （同 adopt.go：Emby 条目与观看记录保留；改了修改时间 Emby 按加入时间的排序会乱）。
// 接管来的分享记 Adopted：之后「原样」检查时已有文件留在原位，只补分享里新出现的。
// 不碰 115、不通知 Emby（Emby 播放时现读 STRM 内容）。执行不拿预演的结果，重新扫

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// shareAdoptFile 扫到的一个分享 STRM
type shareAdoptFile struct {
	Rel     string // 相对分享根
	Code    string
	Receive string
	Fid     string
}

// shareAdoptGroup 预演里的一个分享
type shareAdoptGroup struct {
	ShareCode string `json:"share_code"`
	Dir       string `json:"dir"`   // 第一个文件所在的第一层目录
	Files     int    `json:"files"` // 要接管的文件
	Known     int    `json:"known"` // 已经登记过（本站自己的或接管过的）
	HasCode   bool   `json:"has_code"`
	Listed    bool   `json:"listed"` // 这个分享已经在分享 STRM 里
	Problem   string `json:"problem,omitempty"`
}

type shareAdoptReport struct {
	Root    string            `json:"root"`
	Scanned int               `json:"scanned"` // 扫到的 STRM
	Other   int               `json:"other"`   // 不是分享 STRM（本账号的、认不出的）
	Groups  []shareAdoptGroup `json:"groups"`
	Adopted int               `json:"adopted,omitempty"` // 执行时换了几个
	Errors  []string          `json:"errors,omitempty"`
}

// scanShareAdopt 扫分享根下的 STRM，挑出分享 STRM
func scanShareAdopt(root string) ([]shareAdoptFile, int, int, error) {
	var out []shareAdoptFile
	scanned, other := 0, 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".strm") {
			return nil
		}
		scanned++
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		b, _ := io.ReadAll(io.LimitReader(f, 8<<10))
		f.Close()
		ref := parseForeignStrm(string(b))
		rel, err := filepath.Rel(root, p)
		if err != nil || ref.Kind != "share" || !reShareCodeID.MatchString(ref.ShareCode) || !isAllDigits(ref.ShareFid) || ref.ShareFid == "" {
			other++
			return nil
		}
		out = append(out, shareAdoptFile{Rel: filepath.ToSlash(rel), Code: ref.ShareCode, Receive: ref.ReceiveCode, Fid: ref.ShareFid})
		return nil
	})
	return out, scanned, other, err
}

// planShareAdopt 按分享码分组，标出已登记的、没有提取码的（纯函数：known 查「这个分享 + fid 登记过没有」，listed 查分享在不在）
func planShareAdopt(files []shareAdoptFile, listed func(code string) (bool, string), known func(code, fid string) bool) []shareAdoptGroup {
	by := map[string]*shareAdoptGroup{}
	var order []string
	for _, f := range files {
		g, ok := by[f.Code]
		if !ok {
			isListed, rc := listed(f.Code)
			dir, _, _ := strings.Cut(f.Rel, "/")
			g = &shareAdoptGroup{ShareCode: f.Code, Dir: dir, Listed: isListed, HasCode: rc != ""}
			by[f.Code] = g
			order = append(order, f.Code)
		}
		if f.Receive != "" {
			g.HasCode = true
		}
		if known(f.Code, f.Fid) {
			g.Known++
		} else {
			g.Files++
		}
	}
	sort.Strings(order)
	out := make([]shareAdoptGroup, 0, len(order))
	for _, c := range order {
		g := by[c]
		if !g.HasCode && !g.Listed {
			// 本站的 /d/s~ 不带提取码：分享没登记就不知道提取码，取不了链
			g.Problem = "STRM 里没有提取码，先在「分享」页签按链接添加一次这个分享"
		}
		out = append(out, *g)
	}
	return out
}

func (h *Handler) shareAdoptListed(code string) (bool, string) {
	var s model.ShareSource
	if h.DB.Where("share_code = ?", code).First(&s).Error != nil {
		return false, ""
	}
	return true, s.ReceiveCode
}

func (h *Handler) shareAdoptKnown(code, fid string) bool {
	var n int64
	h.DB.Model(&model.ShareFile{}).Joins("JOIN share_sources ON share_sources.id = share_files.source_id").
		Where("share_sources.share_code = ? AND share_files.file_id = ?", code, fid).Count(&n)
	return n > 0
}

// ShareStrmAdopt POST /sharestrm/adopt {apply} —— 预演 / 执行接管
func (h *Handler) ShareStrmAdopt(c *gin.Context) {
	var req struct {
		Apply bool `json:"apply"`
	}
	_ = c.ShouldBindJSON(&req)
	cfg := loadShareStrmCfg()
	if msg := shareRootProblem(cfg.Root, loadLibraries()); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	root := shareRootAbs(cfg)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "分享 STRM 目录 " + root + " 不存在"})
		return
	}
	files, scanned, other, err := scanShareAdopt(root)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rep := shareAdoptReport{Root: root, Scanned: scanned, Other: other,
		Groups: planShareAdopt(files, h.shareAdoptListed, h.shareAdoptKnown)}
	if !req.Apply {
		c.JSON(http.StatusOK, rep)
		return
	}
	// 没填直连域名不许执行：兜底的 Docker 回环写进去，接管完一个都播不了（同 adopt.go）
	domain := h.strmExplicitDomain()
	if domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没填直连域名（Strm 管理 → 配置）：接管要把 STRM 换成本站地址"})
		return
	}
	_, format, keepExt := readStrmLinkConfig(h.DB, h.Config)
	n, errs := h.applyShareAdopt(root, domain, format, keepExt, files)
	rep.Adopted, rep.Errors = n, errs
	plogShareStrm.Printf("✓ 接管分享 STRM：换了 %d 个（扫到 %d 个 STRM，不是分享的 %d 个）", n, scanned, other)
	c.JSON(http.StatusOK, rep)
}

// applyShareAdopt 登记并换内容。返回换了几个、出错说明
func (h *Handler) applyShareAdopt(root, domain, format string, keepExt bool, files []shareAdoptFile) (int, []string) {
	srcs := map[string]*model.ShareSource{}
	n := 0
	var errs []string
	for _, f := range files {
		if h.shareAdoptKnown(f.Code, f.Fid) {
			continue
		}
		src, ok := srcs[f.Code]
		if !ok {
			var s model.ShareSource
			if h.DB.Where("share_code = ?", f.Code).First(&s).Error != nil {
				if f.Receive == "" {
					errs = appendCapped(errs, f.Rel+"：没有提取码，先按链接添加这个分享")
					continue
				}
				dir, _, _ := strings.Cut(f.Rel, "/")
				s = model.ShareSource{ShareCode: f.Code, ReceiveCode: f.Receive, Title: dir, LocalDir: dir,
					Layout: shareLayoutKeep, State: shareStateOK, Source: "接管", Adopted: true}
				if err := h.DB.Create(&s).Error; err != nil {
					errs = appendCapped(errs, f.Rel+"："+err.Error())
					continue
				}
			} else if !s.Adopted {
				h.DB.Model(&model.ShareSource{}).Where("id = ?", s.ID).Update("adopted", true)
				s.Adopted = true
			}
			src = &s
			srcs[f.Code] = src
		}
		abs := filepath.Join(root, filepath.FromSlash(f.Rel))
		st, err := os.Stat(abs)
		if err != nil {
			errs = appendCapped(errs, f.Rel+"："+err.Error())
			continue
		}
		name := strings.TrimSuffix(path.Base(f.Rel), path.Ext(f.Rel))
		content := shareStrmURL(domain, format, keepExt, f.Code, f.Fid, name)
		if err := os.WriteFile(abs, []byte(content), st.Mode().Perm()); err != nil {
			errs = appendCapped(errs, f.Rel+"："+err.Error())
			continue
		}
		_ = os.Chtimes(abs, st.ModTime(), st.ModTime()) // 修改时间不动（Emby 按加入时间排序）
		h.DB.Create(&model.ShareFile{SourceID: src.ID, FileID: f.Fid, Name: name, RelPath: f.Rel, Kind: "video"})
		n++
	}
	for _, s := range srcs {
		var c int64
		h.DB.Model(&model.ShareFile{}).Where("source_id = ? AND kind = ?", s.ID, "video").Count(&c)
		h.DB.Model(&model.ShareSource{}).Where("id = ?", s.ID).Update("files", int(c))
	}
	return n, errs
}

func appendCapped(list []string, s string) []string {
	if len(list) < 50 {
		return append(list, s)
	}
	return list
}
