package api

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

// ==================== 每个库上一次全量的结果（2026-10-20，MULTI-ACCOUNT-PLAN.md §8.2） ====================
//
// 学 LitePan 的 STRM 任务卡片：每个库自己显示上一次同步扫了多少、新增多少、有没有失败。
// 原来一次全量的结果只在任务中心里汇成一行，多个库时看不出是哪个库出了问题；
// 附加账号的库（acctlibs.go）出错只进日志，更需要在卡片上看得见。
//
// 只记全量（增量是按事件逐条处理的，没有「这个库扫了多少」）。存 setting lib_sync_stats，
// 按库 key（主号的库是 cid，附加账号的库是 a{账号}:{cid}），只由全量写，零 115 请求。

const libSyncStatsKey = "lib_sync_stats"

// libSyncStat 一个库上一次全量的结果
type libSyncStat struct {
	At       int64         `json:"at"`                  // 结束时间（unix 秒）
	OK       bool          `json:"ok"`                  // 这个库同步成功了没有
	Error    string        `json:"error,omitempty"`     // 失败原因
	Mode     string        `json:"mode,omitempty"`      // fast / normal（实际用的）
	Seconds  int           `json:"seconds"`             // 这个库用时
	Videos   int           `json:"videos"`              // 网盘上的视频
	Created  int           `json:"created"`             // 新写的 STRM
	Failed   int           `json:"failed"`              // STRM 写失败
	Assets   int           `json:"assets"`              // 新下载的附属文件
	Orphans  int           `json:"orphans"`             // 失效 STRM 待清理（开了检测才有）
	Complete bool          `json:"complete"`            // 清单完整
	LastScan *fullLastScan `json:"last_scan,omitempty"` // 失败或清单不完整时保留上一次的扫描范围
}

func parseLibSyncStats(raw string) map[string]libSyncStat {
	out := map[string]libSyncStat{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// libSyncStatKey 全量目标在统计里的 key（与 lib_opts、库卡片同一套）
func libSyncStatKey(t fullTarget) string {
	if t.acct != acctMain {
		return acctLibKey(t.acct, t.cid)
	}
	return t.cid
}

// saveLibSyncStats 合并这次的结果写回；不在配置里的库顺手去掉
func (h *Handler) saveLibSyncStats(got map[string]libSyncStat) {
	if h.Config == nil || len(got) == 0 {
		return
	}
	all := parseLibSyncStats(h.getSettingValue(libSyncStatsKey))
	for k, v := range got {
		if v.LastScan == nil {
			v.LastScan = all[k].LastScan
		}
		all[k] = v
	}
	keep := map[string]bool{}
	if libs := loadLibraries(); len(libs) > 0 {
		for _, l := range libs {
			keep[libKeyOf(l)] = true
		}
	} else {
		for _, k := range allLibKeys(h.getSettingValue("full"), h.getSettingValue(acctLibsKey)) {
			keep[k] = true
		}
	}
	for k := range all {
		if !keep[k] {
			delete(all, k)
		}
	}
	b, _ := json.Marshal(all)
	if err := h.Config.SaveSetting(libSyncStatsKey, string(b)); err != nil {
		log.Printf("[同步] ○ 记录各库同步结果失败（只影响媒体库卡片上的显示）: %v", err)
	}
}

// libSyncStatOf 一个库这次全量的结果
func libSyncStatOf(r fullLibResult, err error, start time.Time) libSyncStat {
	s := libSyncStat{At: time.Now().Unix(), Seconds: int(time.Since(start).Seconds()), OK: err == nil}
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Mode, s.Complete = r.modeUsed, r.complete
	s.Videos, s.Created, s.Failed = r.videos, r.st.StrmCreated, r.st.StrmFailed
	s.Assets, s.Orphans = r.st.AssetsDownloaded, r.orphans
	return s
}

// fullLastScanKey 上一次全量用的三组后缀（2026-10-23）：开始全量前的确认框拿这次的后缀跟它比，
// 少了的单独标出来 —— 缩减后缀会让原先同步过的文件被判为失效 STRM
const fullLastScanKey = "full_last_scan"

type fullLastScan struct {
	At       int64    `json:"at"`
	VideoExt []string `json:"video_ext"`
	ImageExt []string `json:"image_ext"`
	DataExt  []string `json:"data_ext"`
}

// saveFullLastScan 保留旧任务的全局后缀记录，供还没有逐库记录的老数据参考。
func (h *Handler) saveFullLastScan(p fullParams, stats map[string]libSyncStat) {
	if h.Config == nil || p.AcctOnly || p.Libraries != nil {
		return
	}
	ok := false
	for _, st := range stats {
		ok = ok || st.OK
	}
	if !ok {
		return
	}
	b, _ := json.Marshal(fullLastScan{At: time.Now().Unix(), VideoExt: p.VideoExt, ImageExt: p.ImageExt, DataExt: p.DataExt})
	if err := h.Config.SaveSetting(fullLastScanKey, string(b)); err != nil {
		log.Printf("[同步] ○ 记录本次全量的后缀失败（只影响下次开跑前的提示）: %v", err)
	}
}
