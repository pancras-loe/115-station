package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 二级分类简单模式的接口（catsimple.go） ====================

// catSchemeSource 方案实际生效的 YAML：存过就是存的那份；全新部署没写过的用规则行拼一份（种子规则）
func (h *Handler) catSchemeSource(s model.CategoryScheme) string {
	if strings.TrimSpace(s.Config) != "" {
		return s.Config
	}
	var rows []model.CategoryRule
	h.DB.Where("scheme_id = ? AND media_type IN ?", s.ID, []string{"movie", "tv"}).Order("media_type, priority").Find(&rows)
	return renderCategoryYAML(rows)
}

// catSimpleView GetScheme 里简单模式那几项：simple 认不回来时为 null，simple_why 说原因
func (h *Handler) catSimpleView(s model.CategoryScheme) gin.H {
	src := h.catSchemeSource(s)
	if strings.TrimSpace(src) == "" {
		return gin.H{"simple": nil, "simple_why": []string{}, "comments": false, "empty": true}
	}
	f, why := catSimpleOf(src)
	if why == nil {
		why = []string{}
	}
	return gin.H{"simple": f, "simple_why": why, "comments": yamlHasComments(s.Config), "empty": false}
}

// catDirChange 删掉 / 改了名的分类，原来那个目录里已经有片
type catDirChange struct {
	MediaType string `json:"media_type"`
	Name      string `json:"name"`
	Titles    int    `json:"titles"`
}

// catDirChanges 旧规则里有、新规则里没了的分类目录（改名在这里就是「旧名没了」），只报台账里有片的。
// 已入库的片不会跟着搬：之后新来的按新规则走，同一类片会分在两个目录里，保存前让用户知道
func catDirChanges(oldRows, newRows []model.CategoryRule, titles func(mediaType, category string) int) []catDirChange {
	type key struct{ mt, name string }
	have := map[key]bool{}
	for _, r := range newRows {
		have[key{r.MediaType, strings.ToLower(libSubPath(r.Name))}] = true
	}
	out := []catDirChange{}
	seen := map[key]bool{}
	for _, r := range oldRows {
		name := libSubPath(r.Name)
		k := key{r.MediaType, strings.ToLower(name)}
		if have[k] || seen[k] || name == "" {
			continue
		}
		seen[k] = true
		if n := titles(r.MediaType, name); n > 0 {
			out = append(out, catDirChange{MediaType: r.MediaType, Name: name, Titles: n})
		}
	}
	return out
}

// catDirChangesNow 拿方案现在的规则比要保存的。零 115 请求：只看台账里用这套方案的库
func (h *Handler) catDirChangesNow(schemeID uint, newRows []model.CategoryRule) []catDirChange {
	libs := map[string]bool{}
	for _, l := range loadLibraries() {
		if l.SchemeID == schemeID {
			libs[libSubPath(l.Local)] = true
		}
	}
	if len(libs) == 0 {
		return nil // 没有库在用：改了也不会分家
	}
	var oldRows []model.CategoryRule
	h.DB.Where("scheme_id = ? AND media_type IN ?", schemeID, []string{"movie", "tv"}).Find(&oldRows)
	entries := scanLedgerTitlesCached()
	return catDirChanges(oldRows, newRows, func(mt, cat string) int {
		n := 0
		for _, e := range entries {
			if e.Category == cat && (e.MediaType == "" || e.MediaType == mt) && libs[e.LibName] {
				n++
			}
		}
		return n
	})
}

// PreviewScheme POST /category/schemes/preview {simple?, yaml?, tmdb_id?, media_type?}
//
// 纯计算，不碰 115：simple 给出时返回生成的 yaml；带了 tmdb_id 时按这份（还没保存的）规则算这部片进哪个目录。
// TMDB 详情要请求一次 TMDB（制片国家只有详情里有）
func (h *Handler) PreviewScheme(c *gin.Context) {
	var req struct {
		Simple    *catSimpleForm `json:"simple"`
		Yaml      string         `json:"yaml"`
		TmdbID    int            `json:"tmdb_id"`
		MediaType string         `json:"media_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	resp := gin.H{}
	var rows []model.CategoryRule
	if req.Simple != nil {
		resp["yaml"] = req.Simple.yaml()
		rows = req.Simple.rows()
		// 目录名空的那条在界面上已经标红；试一试照样能算（空名的会被 validate 挡在保存那一步）
	} else {
		r, err := parseCategoryYAML(req.Yaml)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "YAML 格式错误：" + err.Error()})
			return
		}
		rows = r
	}
	if req.TmdbID > 0 {
		media, err := catTryMedia(req.TmdbID, req.MediaType == "tv")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "读取 TMDB 详情失败：" + err.Error()})
			return
		}
		if media == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "TMDB 上没有这个条目"})
			return
		}
		resp["media"] = gin.H{
			"title": media.Title, "year": media.Year, "media_type": media.MediaType,
			"genre_ids": media.GenreIDs, "original_language": media.OrigLanguage,
			"origin_country": media.OrigCountry, "production_countries": media.ProdCountry,
		}
		resp["try"] = catTry(rows, media)
	}
	c.JSON(http.StatusOK, resp)
}

// 试一试改一个字就重算一次：同一部片的 TMDB 详情记 10 分钟，别每次都请求 TMDB
var (
	catTryMu    sync.Mutex
	catTryCache = map[string]catTryCached{}
)

type catTryCached struct {
	media *TmdbMedia
	at    time.Time
}

func catTryMedia(id int, isTV bool) (*TmdbMedia, error) {
	key := fmt.Sprintf("%v:%d", isTV, id)
	catTryMu.Lock()
	if e, ok := catTryCache[key]; ok && time.Since(e.at) < 10*time.Minute {
		catTryMu.Unlock()
		return e.media, nil
	}
	catTryMu.Unlock()
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	media, err := tc.getByTmdbID(id, isTV)
	if err != nil || media == nil {
		return media, err
	}
	catTryMu.Lock()
	if len(catTryCache) > 200 {
		catTryCache = map[string]catTryCached{}
	}
	catTryCache[key] = catTryCached{media: media, at: time.Now()}
	catTryMu.Unlock()
	return media, nil
}
