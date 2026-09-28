package api

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"115-station/internal/model"
)

// ==================== ffprobe 结果缓存 ====================
//
// 探测一次要向 115 取一次直链（走节流）再让 ffprobe 读十来 MB，几百集的剧就是几十分钟。
// 同一个文件的轨道不会变（pickcode 跟着文件走，洗版换片就是另一个 pickcode），
// 所以结果按 pickcode 落库：整理时补全画质探测过的，刮削直接用；强制覆盖重刮也不再探一遍。
// 失败不缓存 —— 失败多半是直链 / CDN 的临时问题，下次该重试。

// probeCached 先查缓存，没有再探测并落库。cached=true 表示命中缓存
func probeCached(pickCode string) (res *probeResult, cached bool, errMsg string) {
	if pickCode == "" {
		return nil, false, "缺 pickcode"
	}
	if model.DB != nil {
		var row model.ProbeCache
		if model.DB.Where("pick_code = ?", pickCode).First(&row).Error == nil {
			var p probeResult
			if json.Unmarshal([]byte(row.Result), &p) == nil {
				return &p, true, ""
			}
		}
	}
	p, perr := probeFileNow(pickCode)
	if p == nil {
		if perr == "" {
			perr = "未知错误"
		}
		return nil, false, perr
	}
	if model.DB != nil {
		if b, err := json.Marshal(p); err == nil {
			model.DB.Save(&model.ProbeCache{PickCode: pickCode, Result: string(b), CreatedAt: time.Now()})
		}
	}
	return p, false, ""
}

// probeBrief 一行说清探测结果（日志用）：1080p H265 HDR · AAC 2ch ×2 · 字幕 3 · 45分钟
func probeBrief(p *probeResult) string {
	if p == nil {
		return "无结果"
	}
	var parts []string
	v := strings.TrimSpace(strings.Join([]string{p.Pix, p.Video, p.Effect}, " "))
	if v != "" {
		parts = append(parts, v)
	}
	audio, subs := 0, 0
	for _, s := range p.Streams {
		switch s.Kind {
		case "audio":
			audio++
		case "subtitle":
			subs++
		}
	}
	if audio > 0 {
		a := p.Audio
		if a == "" {
			a = "音轨"
		}
		if audio > 1 {
			a += fmt.Sprintf(" ×%d", audio)
		}
		parts = append(parts, a)
	}
	if subs > 0 {
		parts = append(parts, fmt.Sprintf("字幕 %d", subs))
	}
	if p.Duration > 0 {
		parts = append(parts, fmt.Sprintf("%d分钟", (p.Duration+30)/60))
	}
	if len(parts) == 0 {
		return "没有可用轨道"
	}
	return strings.Join(parts, " · ")
}

// pruneProbeCache 清掉台账里已经没有的文件的探测缓存（随每日清理）
func pruneProbeCache() {
	if model.DB == nil {
		return
	}
	res := model.DB.Where("pick_code NOT IN (?)",
		model.DB.Model(&model.SyncedFile{}).Select("pick_code").Where("pick_code <> ''")).
		Delete(&model.ProbeCache{})
	if res.Error == nil && res.RowsAffected > 0 {
		log.Printf("[系统] ○ 清理 %d 条已失效的媒体探测缓存", res.RowsAffected)
	}
}
