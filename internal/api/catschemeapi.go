package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 分类方案的接口（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.4，V2 阶段 4） ====================
//
// 「自动整理 → 二级分类」改成方案列表：新建 / 复制 / 改名 / 改内容 / 删除。每套就是一份 YAML（不再认 library 键：
// 放进哪个库由转存文件夹决定），库卡片上选用哪一套；被库用着的不能删。
// 老的单份接口（GET/POST /scrape/categories）读写第一套，留给还没换掉的调用方。

// GetScheme GET /category/schemes/:id → {id, name, yaml, simple, simple_why, comments, empty}
//
// simple 是简单模式的表单（catsimple.go），认不回来时为 null、simple_why 说原因
func (h *Handler) GetScheme(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	s, ok := schemeByID(uint(id))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "这套分类方案不存在（可能已经删除）"})
		return
	}
	resp := gin.H{"id": s.ID, "name": s.Name, "yaml": s.Config}
	for k, v := range h.catSimpleView(s) {
		resp[k] = v
	}
	c.JSON(http.StatusOK, resp)
}

// checkSchemeName 方案名能不能用（exclude = 改名时的自己）
func checkSchemeName(name string, exclude uint) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("方案名不能为空")
	}
	if len([]rune(name)) > 30 {
		return "", errors.New("方案名太长了（最多 30 个字）")
	}
	for _, s := range loadSchemes() {
		if s.ID != exclude && strings.EqualFold(s.Name, name) {
			return "", fmt.Errorf("已经有一套叫「%s」的方案了", name)
		}
	}
	return name, nil
}

// schemeYAMLRows 解析方案 YAML（去掉作废的 library 键）
func schemeYAMLRows(src string) (string, []model.CategoryRule, error) {
	if out, _, err := rewriteCategoryLibrary(src, func(_, _, _ string) (string, bool) { return "", false }); err == nil {
		src = out
	}
	rows, err := parseCategoryYAML(src)
	if err != nil {
		return "", nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if len(rows) == 0 {
		return "", nil, errors.New("没解析到 movie / tv 分类规则")
	}
	return src, rows, nil
}

// CreateScheme POST /category/schemes {name, copy_from} —— copy_from 为 0 时复制第一套
func (h *Handler) CreateScheme(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		CopyFrom uint   `json:"copy_from"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name, err := checkSchemeName(req.Name, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	srcID := req.CopyFrom
	if srcID == 0 {
		if srcID, err = ensureDefaultScheme(h.DB); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	src, ok := schemeByID(srcID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "要复制的方案不存在"})
		return
	}
	// 要复制的那套还是空的（全新部署没写过分类）：新方案也从空的开始，界面上填默认模板
	yamlSrc, rows := "", []model.CategoryRule(nil)
	if strings.TrimSpace(src.Config) != "" {
		if yamlSrc, rows, err = schemeYAMLRows(src.Config); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "要复制的方案" + err.Error()})
			return
		}
	}
	s := model.CategoryScheme{Name: name, Config: yamlSrc}
	tx := h.DB.Begin()
	if err := tx.Create(&s).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := writeSchemeTx(tx, s.ID, yamlSrc, rows); err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	forgetSchemes()
	forgetClassifyRules()
	log.Printf("[分类] ✓ 新建分类方案「%s」（复制自「%s」）", name, src.Name)
	c.JSON(http.StatusOK, gin.H{"id": s.ID, "schemes": h.mediaLibsNow().Schemes})
}

// UpdateScheme PUT /category/schemes/:id {name?, yaml?, simple?, confirm_cat_change?}
//
// simple（简单模式的表单）给出时由它生成 YAML，yaml 不看。删掉 / 改名的分类原来那个目录里已经有片时
// 返回 409 + cat_changes，用户确认后带 confirm_cat_change 重发（已入库的不会跟着搬）
func (h *Handler) UpdateScheme(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	s, ok := schemeByID(uint(id))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "这套分类方案不存在（可能已经删除）"})
		return
	}
	var req struct {
		Name             *string        `json:"name"`
		Yaml             *string        `json:"yaml"`
		Simple           *catSimpleForm `json:"simple"`
		ConfirmCatChange bool           `json:"confirm_cat_change"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Simple != nil {
		if err := req.Simple.validate(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		src := req.Simple.yaml()
		req.Yaml = &src
	}
	// 写错的拦下，写在中间的兜底挪到最后（catrule.go）
	note := ""
	if req.Yaml != nil {
		src, n, err := prepCategoryYAML(*req.Yaml)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		req.Yaml, note = &src, n
	}
	if req.Yaml != nil && !req.ConfirmCatChange {
		if _, rows, err := schemeYAMLRows(*req.Yaml); err == nil {
			if changes := h.catDirChangesNow(s.ID, rows); len(changes) > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "有分类改了名或删掉了，原来的目录里已经有片", "cat_changes": changes})
				return
			}
		}
	}
	tx := h.DB.Begin()
	msg := "已保存"
	if req.Name != nil {
		name, err := checkSchemeName(*req.Name, s.ID)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := tx.Model(&model.CategoryScheme{}).Where("id = ?", s.ID).Update("name", name).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if req.Yaml != nil {
		yamlSrc, rows, err := schemeYAMLRows(*req.Yaml)
		if err != nil {
			tx.Rollback()
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := writeSchemeTx(tx, s.ID, yamlSrc, rows); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		msg = fmt.Sprintf("保存成功（%d 条分类规则已生效）", len(rows))
		if note != "" {
			msg += "；" + note
		}
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	forgetSchemes()
	forgetClassifyRules()
	log.Printf("[分类] ✓ 分类方案「%s」%s", s.Name, msg)
	resp := gin.H{"message": msg, "schemes": h.mediaLibsNow().Schemes}
	if note != "" {
		resp["moved_fallback"] = true // YAML 被改过了，界面换成这一份
		resp["yaml"] = *req.Yaml
	}
	c.JSON(http.StatusOK, resp)
}

// DeleteScheme DELETE /category/schemes/:id —— 被库用着的、最后一套都不能删
func (h *Handler) DeleteScheme(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	s, ok := schemeByID(uint(id))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "这套分类方案不存在（可能已经删除）"})
		return
	}
	var users []string
	for _, l := range loadLibraries() {
		if l.SchemeID == s.ID {
			users = append(users, l.Name)
		}
	}
	if len(users) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("「%s」还有媒体库在用（%s），先给它们换一套再删", s.Name, strings.Join(users, "、"))})
		return
	}
	if len(loadSchemes()) <= 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "至少要留一套分类方案"})
		return
	}
	tx := h.DB.Begin()
	if err := tx.Where("scheme_id = ?", s.ID).Delete(&model.CategoryRule{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Delete(&model.CategoryScheme{}, s.ID).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	forgetSchemes()
	forgetClassifyRules()
	log.Printf("[分类] ✓ 删除分类方案「%s」", s.Name)
	c.JSON(http.StatusOK, gin.H{"schemes": h.mediaLibsNow().Schemes})
}
