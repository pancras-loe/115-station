package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"115-station/internal/model"
)

// ==================== ISO 的 STRM 直链一律带 .iso ====================
//
// 2026-09-28 起 STRM 名不带视频扩展名（disc.iso → disc.strm，见 strmname.go），本地文件名上就看不出
// 哪个 STRM 是光盘镜像。Emby 提前探测要跳过 ISO（Emby 探不了，探一次白取一次 115 直链，
// 见 embyextract.go 的 probeSources），剩下能认的只有 STRM 里的直链：
//
//	pick_code_name  /d/{pickcode}[.ext]?/{原文件名}   —— 查询串里有原文件名
//	pick_code       /d/{pickcode}[.ext]               —— 关了「保留文件后缀」就什么都没有
//
// 所以 ISO 不看「保留文件后缀」开关，pickcode 段一律带 .iso（writeStrmNamed）。
// 做法对照 qmediasync：STRM 名同样去掉扩展名，同步时校验直链必须以原文件扩展名结尾，
// 不是就重新生成（syncstrm/sync_strm.go），它的直链路由注释写着「支持 iso」。
//
// 存量的补法（启动时一次性，MigrateStrmISOExt）：
//   - 直链查询串里带着原文件名（pick_code_name）：本地直接改写，零 115 请求；
//   - 直链里什么都没有（pick_code 且关了保留后缀）：本地认不出，入队一次全量同步。
//     固定快速模式（万级库十来次请求），不可用时自动降级标准模式。
//     全量写 STRM 时对「还没带 .iso 的 ISO」无视「跳过已存在」照写（strmMissingISOExt）。
//
// 只改 STRM 内容、不改名，Emby 条目不动；Emby 记着的旧直链照样能播（代理按 pickcode 找文件，
// 后缀本来就会剥掉），探测那边读本地 STRM，不等 Emby 重扫

// strmISOMigrateKey 完成标记
const strmISOMigrateKey = "migrate.strm_iso_ext.v1"

func isISOName(name string) bool {
	return strings.EqualFold(pathExt(name), ".iso")
}

// strmMissingISOExt 已落盘的直链是不是还没给 ISO 带上 .iso（只看 pickcode 段，查询串里的文件名不算）
func strmMissingISOExt(old, name string) bool {
	if !isISOName(name) {
		return false
	}
	p, _, _ := strings.Cut(strings.TrimSpace(old), "?")
	return !strings.HasSuffix(strings.ToLower(p), ".iso")
}

// strmISOFix 按直链自己带的信息补 .iso：fixed 是改写后的直链（空 = 不用改），
// unknown = 直链里没有原文件名、也没有后缀，本地判断不了是不是 ISO
func strmISOFix(u string) (fixed string, unknown bool) {
	pathPart, query, hasQuery := strings.Cut(u, "?")
	i := strings.Index(pathPart, "/d/")
	if i < 0 {
		return "", false // 不是本站直链
	}
	seg := pathPart[i+3:]
	if seg == "" || strings.Contains(seg, "/") || strings.Contains(seg, ".") {
		// 旧版 /d/{fid}/名字 的文件名本来就在路径上；已经带了后缀的不用动
		return "", false
	}
	if !hasQuery {
		return "", true
	}
	name := strings.TrimPrefix(query, "/")
	if !isISOName(name) {
		return "", false
	}
	return pathPart + pathExt(name) + "?" + query, false
}

type strmISOStats struct {
	Seen    int // 读到的本地 STRM
	Fixed   int // 本地改写补上 .iso 的
	Unknown int // 直链里看不出原文件名的（要全量同步补）
	Failed  int // 改写失败的（下次启动重试）
}

// migrateStrmISOExt 纯本地的那一段：按台账视频行读本地 STRM，能补的补上 .iso
func migrateStrmISOExt(db *gorm.DB, root string) (st strmISOStats, err error) {
	var rels []string
	if err := db.Model(&model.SyncedFile{}).Where("kind = ?", "video").Pluck("rel_path", &rels).Error; err != nil {
		return st, err
	}
	if len(rels) == 0 {
		return st, nil
	}
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			continue // 本地没有（失效 STRM、用户手删）：没什么可补的
		}
		st.Seen++
		fixed, unknown := strmISOFix(strings.TrimSpace(string(b)))
		if unknown {
			st.Unknown++
		}
		if fixed == "" {
			continue
		}
		if err := os.WriteFile(p, []byte(fixed), 0o666); err != nil {
			log.Printf("[迁移] ✗ ISO 直链补 .iso 失败 %s: %v", rel, err)
			st.Failed++
			continue
		}
		st.Fixed++
	}
	if st.Seen == 0 {
		// 挂载还没就绪时本地整棵树都读不到，这时打了完成标记，就再也没有机会补了
		return st, fmt.Errorf("台账有 %d 个视频，本地一个 STRM 都读不到（%s 挂载未就绪？）", len(rels), root)
	}
	return st, nil
}

// MigrateStrmISOExt 启动时调用一次（SetupRoutes 里，MigrateStrmNames 之后、后台任务启动之前）
func (h *Handler) MigrateStrmISOExt() {
	db := h.DB
	var done int64
	db.Model(&model.Setting{}).Where("key = ?", strmISOMigrateKey).Count(&done)
	if done > 0 {
		return
	}
	start := time.Now()
	st, err := migrateStrmISOExt(db, localMediaRoot())
	if err != nil {
		log.Printf("[迁移] ✗ ISO 直链补 .iso 未执行，下次启动重试: %v", err)
		return
	}
	if st.Fixed+st.Failed > 0 {
		log.Printf("[迁移] ✓ ISO 直链补 .iso：本地改写 %d 个、失败 %d 个，用时 %s",
			st.Fixed, st.Failed, time.Since(start).Round(time.Millisecond))
	}
	if st.Failed > 0 {
		log.Printf("[迁移] ○ 有 %d 个没改成，下次启动重试", st.Failed)
		return
	}
	if st.Unknown > 0 {
		p := h.fullParamsFromConfig()
		if p.Cid == "" || p.Cid == "0" {
			// 不入队也打标记：以后任何一次全量同步都会补（strmMissingISOExt），不必每次启动都提示
			log.Printf("[迁移] ○ %d 个 STRM 的直链看不出原文件名（pick_code 且未保留后缀），其中 ISO 要全量同步才能补 .iso；"+
				"还没配置全量同步，配好后跑一次即可", st.Unknown)
		} else {
			// 固定快速模式（不看用户配的全量模式）：只为给几个 ISO 补后缀，快速模式万级库十来次请求，
			// 标准模式按目录数算要上千次。快速模式不可用（启用了 OpenAPI / 没绑 Cookie）或中途失败时
			// collectSyncFiles 自己降级回标准模式
			if _, err := enqueueJob(db, jobSpec{Kind: "full", Title: "全量同步（ISO 直链补 .iso）", DedupeKey: "full",
				Source: "auto", Priority: jobPriorityBackground, Params: jobParams{Sync: &syncJobParams{
					Cid: p.Cid, LocalPath: p.LocalPath, VideoExt: p.VideoExt, ImageExt: p.ImageExt, DataExt: p.DataExt, Mode: "fast",
				}}}); err != nil {
				log.Printf("[迁移] ✗ ISO 直链补 .iso 的全量同步入队失败，下次启动重试: %v", err)
				return
			}
			log.Printf("[迁移] ○ %d 个 STRM 的直链看不出原文件名（pick_code 且未保留后缀），已排一次全量同步给其中的 ISO 补 .iso", st.Unknown)
		}
	}
	if err := db.Create(&model.Setting{Key: strmISOMigrateKey, Value: "1"}).Error; err != nil {
		log.Printf("[迁移] ○ ISO 直链迁移完成标记写入失败（下次启动再跑一遍，结果不变）: %v", err)
	}
}
