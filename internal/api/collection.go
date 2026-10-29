package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ---- 合集目录（2026-10-29）----
//
// 用户自己的目录结构里常有「机动战士高达（系列）」这种文件夹：几部 TV、十几部剧场版放在一起。
// 片目层级是按库声明 / 分类目录认的，不推断（libopts.go），所以它整个被当成一部，
// 海报墙上是一部 852 集的剧，剧场版全在「解析不出集号」里。
// 这里让用户把它标记成合集（存在 model.Library.Collections），认片目时往下再认一层（ledger.go 的 descendCollections）。
// 名字只拿来提示（collectionNameHint），不自动拆：「XX全集」多半是一部剧的全部季。

// libCollections 库上标记的合集目录（库内相对路径，去重排序）
func libCollections(lib model.Library) []string {
	var raw []string
	if strings.TrimSpace(lib.Collections) == "" || json.Unmarshal([]byte(lib.Collections), &raw) != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		if c = strings.Trim(strings.ReplaceAll(strings.TrimSpace(c), `\`, "/"), "/"); c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// collectionNameHintRe 名字像合集（LitePan 的合集容器认法里挑了几个词，只用于提示）
var collectionNameHintRe = regexp.MustCompile(`(合集|打包|大全|系列|[二三四五六七八九十两\d]+部曲)[\s)）\]】]*$|合集|打包`)

// collectionNameHint 片目目录名看着像合集：详情里提示用户可以标记
func collectionNameHint(dirName string) bool {
	return collectionNameHintRe.MatchString(strings.TrimSpace(dirName))
}

// collectionLeftovers 以前把合集当成一部时刮在它这一层的产物：留着 Emby 会照它把整个文件夹认成一部
var collectionLeftovers = map[string]bool{
	"tvshow.nfo": true, "movie.nfo": true,
}

func isCollectionLeftover(name string) bool {
	low := strings.ToLower(name)
	if collectionLeftovers[low] {
		return true
	}
	ext := path.Ext(low)
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
		return false
	}
	switch strings.TrimSuffix(low, ext) {
	case "poster", "fanart", "backdrop", "banner", "folder", "cover", "logo", "clearlogo", "clearart", "landscape", "thumb", "disc":
		return true
	}
	return false
}

// cleanCollectionLeftovers 删掉合集目录这一层的片目级 NFO / 图片（只删本地；台账里有的是网盘镜像，不动，计入 kept）
func cleanCollectionLeftovers(dirAbs, key string) (removed []string, kept int) {
	ents, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, 0
	}
	for _, d := range ents {
		if d.IsDir() || !isCollectionLeftover(d.Name()) {
			continue
		}
		var n int64
		model.DB.Model(&model.SyncedFile{}).Where("rel_path = ?", key+"/"+d.Name()).Count(&n)
		if n > 0 {
			kept++
			continue
		}
		if os.Remove(filepath.Join(dirAbs, d.Name())) == nil {
			removed = append(removed, d.Name())
		}
	}
	return removed, kept
}

type collectionReq struct {
	Key string `json:"key"` // 标记：要拆开的片目；取消：合集目录（详情里的 collection）
	On  bool   `json:"on"`
}

// LocalTitleCollection POST /local/titles/collection —— 标记 / 取消合集目录
func (h *Handler) LocalTitleCollection(c *gin.Context) {
	var req collectionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	key := strings.Trim(strings.TrimSpace(req.Key), "/")
	if key == "" || isSoloTitleKey(key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有目录能标记为合集"})
		return
	}
	layout := loadLibCategoryLayout()
	ll, n, hit := layout.libOf(key)
	if !hit {
		c.JSON(http.StatusBadRequest, gin.H{"error": "认不出这个片目在哪个媒体库（媒体库表里没有它）"})
		return
	}
	var lib model.Library
	found := false
	for _, l := range loadLibraries() {
		if normLibLocal(l.Local) == ll.local {
			lib, found = l, true
			break
		}
	}
	if !found {
		c.JSON(http.StatusBadRequest, gin.H{"error": "认不出这个片目在哪个媒体库（媒体库表里没有它）"})
		return
	}
	rel := strings.Join(strings.Split(key, "/")[n:], "/")
	if rel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "媒体库根目录不能标记为合集"})
		return
	}
	colls := libCollections(lib)
	has := false
	for _, x := range colls {
		has = has || x == rel
	}
	if req.On {
		// 只认海报墙上现在的一部片：分类目录、季目录这些不让标
		if scanLedgerTitlesCached()[key] == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "台账里没有这个片目（可能刚被移走或已经拆开了），请刷新列表"})
			return
		}
		if !has {
			colls = append(colls, rel)
		}
	} else {
		if !has {
			c.JSON(http.StatusBadRequest, gin.H{"error": "这个目录没有标记为合集"})
			return
		}
		next := colls[:0]
		for _, x := range colls {
			if x != rel {
				next = append(next, x)
			}
		}
		colls = next
	}
	if err := saveLibCollections(lib.ID, colls); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}

	resp := gin.H{"ok": true}
	if req.On {
		titles := 0
		for k := range scanLedgerTitlesCached() {
			if strings.HasPrefix(k, key+"/") {
				titles++
			}
		}
		if titles == 0 {
			// 拆完一部都没有（里面只有带集号的散文件之类）：撤回，不然这部片从海报墙上消失了
			_ = saveLibCollections(lib.ID, libCollections(lib))
			c.JSON(http.StatusBadRequest, gin.H{"error": "拆开后一部片都认不出来（里面没有子目录，直接放着的视频也都带集号），没有标记"})
			return
		}
		resp["titles"] = titles
		if root := localMediaRoot(); root != "" {
			if abs, ok := underRoot(root, key); ok {
				removed, kept := cleanCollectionLeftovers(abs, key)
				resp["removed"], resp["kept"] = removed, kept
			}
		}
		log.Printf("[海报墙] ✓ 「%s」标记为合集目录，拆成 %d 部", key, titles)
	} else {
		log.Printf("[海报墙] ✓ 「%s」取消合集目录", key)
	}
	forgetLocalTitles()
	c.JSON(http.StatusOK, resp)
}

// saveLibCollections 只改库的合集目录这一列（不经整份保存，别的字段不动），并让各处缓存重算
func saveLibCollections(id uint, colls []string) error {
	val := ""
	if len(colls) > 0 {
		b, _ := json.Marshal(colls)
		val = string(b)
	}
	if err := model.DB.Model(&model.Library{}).Where("id = ?", id).Update("collections", val).Error; err != nil {
		return err
	}
	forgetLibraries()
	forgetLedgerTitles()
	return nil
}
