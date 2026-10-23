package api

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// ==================== 分类方案：二级分类可以存多套（2026-10-20，MULTI-ACCOUNT-V2-PLAN.md §10.4） ====================
//
// 原来全站只有一份二级分类 YAML（ScrapeRule type=category_config），规则行在 CategoryRule 里。
// 现在每一份是一套「分类方案」（CategoryScheme，Config 就是那份 YAML），规则行按 SchemeID 分开，
// 每个媒体库选一套用（Library.SchemeID，0 = 不分类）。
//
// 老数据：第一次启动时把原来那份 YAML（没存过就按种子规则生成一份）存成方案「默认」，
// 已有规则行归到它名下（ensureDefaultScheme）。原来的 ScrapeRule.category_config 不删、照旧跟着「默认」写，
// 回退到老版本时它还是那份分类。
//
// YAML 仍是唯一事实来源：启动时 SyncCategoryRulesFromYAML 按每套方案的 YAML 重建它的规则行。

const defaultSchemeName = "默认"

// ensureDefaultScheme 一套方案都没有时，把老的那份分类存成「默认」。返回「默认」方案（第一套）的 id
func ensureDefaultScheme(db *gorm.DB) (uint, error) {
	var first model.CategoryScheme
	if err := db.Order("id").First(&first).Error; err == nil {
		return first.ID, nil
	}
	src := ""
	var rule model.ScrapeRule
	if db.Where("type = ?", "category_config").First(&rule).Error == nil {
		src = rule.Config
	}
	if strings.TrimSpace(src) == "" {
		var rows []model.CategoryRule
		db.Where("media_type IN ?", []string{"movie", "tv"}).Order("media_type, priority").Find(&rows)
		src = renderCategoryYAML(rows)
	}
	// library 键作废了：放进哪个库由转存文件夹决定（原样留在 ScrapeRule.category_config 里给回退用）
	if out, _, err := rewriteCategoryLibrary(src, func(_, _, _ string) (string, bool) { return "", false }); err == nil {
		src = out
	} else {
		log.Printf("[分类] ○ 去掉老分类里的 library 失败（%v），方案「默认」里原样保留，它不再起作用", err)
	}
	s := model.CategoryScheme{Name: defaultSchemeName, Config: src}
	if err := db.Create(&s).Error; err != nil {
		return 0, err
	}
	if err := db.Model(&model.CategoryRule{}).Where("scheme_id = 0").Update("scheme_id", s.ID).Error; err != nil {
		return 0, err
	}
	log.Printf("[分类] ✓ 原来的二级分类存成了方案「%s」（id=%d），各媒体库可以在库设置里选它", defaultSchemeName, s.ID)
	forgetSchemes()
	return s.ID, nil
}

// replaceSchemeRules 事务重建一套方案的规则行（整套替换）
func replaceSchemeRules(db *gorm.DB, schemeID uint, rows []model.CategoryRule) error {
	for i := range rows {
		rows[i].ID = 0
		rows[i].SchemeID = schemeID
	}
	tx := db.Begin()
	if err := tx.Where("scheme_id = ? AND media_type IN ?", schemeID, []string{"movie", "tv"}).Delete(&model.CategoryRule{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	forgetClassifyRules()
	return nil
}

// ---- 方案清单缓存 ----

var (
	schemeMu    sync.Mutex
	schemeCache []model.CategoryScheme
	schemeAt    time.Time
	schemeDB    *gorm.DB // 换了数据库不沿用
)

func forgetSchemes() {
	schemeMu.Lock()
	schemeCache, schemeAt = nil, time.Time{}
	schemeMu.Unlock()
}

// loadSchemes 全部分类方案（按 id）。10 秒缓存，改动时 forgetSchemes
func loadSchemes() []model.CategoryScheme {
	schemeMu.Lock()
	defer schemeMu.Unlock()
	if schemeCache != nil && schemeDB == model.DB && time.Since(schemeAt) < 10*time.Second {
		return schemeCache
	}
	var out []model.CategoryScheme
	if model.DB != nil {
		model.DB.Order("id").Find(&out)
	}
	if out == nil {
		out = []model.CategoryScheme{}
	}
	schemeCache, schemeAt, schemeDB = out, time.Now(), model.DB
	return out
}

// primarySchemeID 第一套方案（「默认」）。老的单份分类接口读写的就是它
func primarySchemeID() uint {
	if list := loadSchemes(); len(list) > 0 {
		return list[0].ID
	}
	return 0
}

// schemeByID 按 id 找方案
func schemeByID(id uint) (model.CategoryScheme, bool) {
	for _, s := range loadSchemes() {
		if s.ID == id {
			return s, true
		}
	}
	return model.CategoryScheme{}, false
}

// ---- 规则行 → YAML（没存过 YAML、只有种子规则的部署迁移时用） ----

// renderCategoryYAML 把规则行写回分类 YAML（parseCategoryYAML 的逆运算，只写有值的条件）
func renderCategoryYAML(rows []model.CategoryRule) string {
	byType := map[string][]model.CategoryRule{}
	for _, r := range rows {
		byType[r.MediaType] = append(byType[r.MediaType], r)
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, mt := range []string{"movie", "tv"} {
		list := byType[mt]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Priority < list[j].Priority })
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, r := range list {
			fields := &yaml.Node{Kind: yaml.MappingNode}
			add := func(k, v string) {
				if strings.TrimSpace(v) == "" {
					return
				}
				fields.Content = append(fields.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: k},
					&yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.SingleQuotedStyle})
			}
			add("genre_ids", r.GenreIds)
			add("original_language", r.OriginalLanguage)
			add("origin_country", r.OriginCountry)
			add("production_countries", r.ProductionCountries)
			add("release_year", r.ReleaseYear)
			add("ext", r.Ext)
			add("custom_regex", r.CustomRegex)
			val := fields
			if len(fields.Content) == 0 {
				val = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}
			}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: r.Name}, val)
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: mt}, m)
	}
	if len(root.Content) == 0 {
		return ""
	}
	// 两格缩进：和默认模板、用户手写的一样（简单模式保存的 YAML 也走这里，专家模式里看着不突兀）
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return ""
	}
	_ = enc.Close()
	return buf.String()
}

// schemeName 给日志 / 界面看的方案名（0 = 不分类）
func schemeName(id uint) string {
	if id == 0 {
		return "不分类"
	}
	if s, ok := schemeByID(id); ok {
		return s.Name
	}
	return fmt.Sprintf("方案 %d（已删除）", id)
}

// writeSchemeTx 在事务里改写一套方案：YAML 文本与规则行一起换（老的单份分类接口、媒体库保存改 YAML 时用）。
// 调用方提交事务后要 forgetClassifyRules / forgetSchemes
func writeSchemeTx(tx *gorm.DB, schemeID uint, yamlSrc string, rows []model.CategoryRule) error {
	if schemeID == 0 {
		return fmt.Errorf("还没有分类方案")
	}
	if err := tx.Model(&model.CategoryScheme{}).Where("id = ?", schemeID).Update("config", yamlSrc).Error; err != nil {
		return err
	}
	if err := tx.Where("scheme_id = ? AND media_type IN ?", schemeID, []string{"movie", "tv"}).Delete(&model.CategoryRule{}).Error; err != nil {
		return err
	}
	for i := range rows {
		rows[i].ID = 0
		rows[i].SchemeID = schemeID
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}
