package api

// ==================== 识别记忆 ====================
//
// 人工在「待确认」里改指定、或在「重新整理」里选了条目，说明自动识别在这个名字上靠不住。
// 把「归一化片名 + 年份 → 条目」记下来，下次同名的内容（连载的新一集、换了发布组的洗版资源）
// 直接用人工的结论，不再重新搜一遍、也不再错一遍。
// 思路参考 openStrm organize/identify.ts 的 evidence.memory（上次确认过的识别结果）。
//
// 纠错的办法就是再改一次：重新整理选别的条目会覆盖同一个键

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"115-station/internal/model"
)

// recogKey 识别键：归一化片名|年份。片名为空（只能靠目录名、id 标签识别）时不记
func recogKey(p *ParsedName) string {
	if p == nil {
		return ""
	}
	k := titleKey(p.Title)
	if k == "" {
		return ""
	}
	return k + "|" + p.Year
}

// rememberRecognition 记下人工结论
func rememberRecognition(key, sample string, media *TmdbMedia) {
	if model.DB == nil || media == nil || media.TmdbID <= 0 {
		return
	}
	title, year, ok := strings.Cut(key, "|")
	if !ok || title == "" {
		return
	}
	row := model.RecognizeMemory{TitleKey: title, Year: year, TmdbID: media.TmdbID,
		MediaType: media.MediaType, Title: media.Title, Sample: sample}
	err := model.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "title_key"}, {Name: "year"}},
		DoUpdates: clause.AssignmentColumns([]string{"tmdb_id", "media_type", "title", "sample", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		log.Printf("[整理] ○ 识别记忆写入失败（不影响整理）: %v", err)
		return
	}
	log.Printf("[整理] ✓ 已记住人工识别结果：%q%s → %s [%s/%d]，下次同名内容直接采用",
		title, yearSuffix(year), media.Title, media.MediaType, media.TmdbID)
}

func yearSuffix(y string) string {
	if y == "" {
		return ""
	}
	return "（" + y + "）"
}

// recallRecognition 查记忆。优先片名 + 年份都对上的；文件名没年份时，
// 同名只有一条记忆才用（同名不同年的两部片子，没有年份分不出是哪一部）；
// 文件名有年份但只记过无年份的那条，也用
func recallRecognition(p *ParsedName) *model.RecognizeMemory {
	if model.DB == nil || p == nil {
		return nil
	}
	key := titleKey(p.Title)
	if key == "" {
		return nil
	}
	var rows []model.RecognizeMemory
	if model.DB.Where("title_key = ?", key).Limit(10).Find(&rows).Error != nil || len(rows) == 0 {
		return nil
	}
	var pick *model.RecognizeMemory
	for i := range rows {
		if rows[i].Year == p.Year {
			pick = &rows[i]
			break
		}
	}
	if pick == nil {
		if p.Year == "" && len(rows) == 1 {
			pick = &rows[0]
		} else {
			for i := range rows {
				if rows[i].Year == "" {
					pick = &rows[i]
					break
				}
			}
		}
	}
	if pick != nil {
		model.DB.Model(pick).UpdateColumn("hits", pick.Hits+1)
	}
	return pick
}

// recognizeByMemory 按识别记忆取条目；没有记忆或条目已不存在返回 nil
func (tc *TmdbClient) recognizeByMemory(parsed *ParsedName) (*TmdbMedia, error) {
	mem := recallRecognition(parsed)
	if mem == nil {
		return nil, nil
	}
	media, err := tc.getByTmdbID(mem.TmdbID, mem.MediaType == "tv")
	if err != nil || media == nil {
		return nil, err
	}
	log.Printf("[整理] 按识别记忆采用人工结论：%q%s → %s (%s) [%s/%d]",
		parsed.Title, yearSuffix(parsed.Year), media.Title, media.Year, media.MediaType, media.TmdbID)
	return media, nil
}

// ---------- 管理接口（识别规则页「识别记忆」卡片）----------

// ListRecognizeMemory GET /organize/recognize-memory
// 记忆只在人工改指定时写，量级是几十到几百条，一次全给、前端自己筛
func (h *Handler) ListRecognizeMemory(c *gin.Context) {
	var rows []model.RecognizeMemory
	if err := h.DB.Order("updated_at DESC").Limit(2000).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// DeleteRecognizeMemory DELETE /organize/recognize-memory/:id
func (h *Handler) DeleteRecognizeMemory(c *gin.Context) {
	if err := h.DB.Delete(&model.RecognizeMemory{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除，同名内容下次按正常流程识别"})
}

// ClearRecognizeMemory POST /organize/recognize-memory/clear
func (h *Handler) ClearRecognizeMemory(c *gin.Context) {
	res := h.DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.RecognizeMemory{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": res.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清空 %d 条识别记忆", res.RowsAffected), "removed": res.RowsAffected})
}
