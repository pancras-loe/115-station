package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// ==================== 回收站：认出被删目录原来在哪 ====================
//
// 网盘上删目录时，delete_file 事件的父目录靠不住（2026-10-10 现场：删「剧名/Season 0」
// 事件里没有可用的父目录），路径缓存里又没记过这个目录（升级前就在库里的老目录），
// 按名字找认不出是哪一部 —— 库里每部剧都有 Season 0。
//
// 回收站列表的每一条带着原来的父目录 id（cid）和父目录名（parent_name），
// 2026-10-10 维护者实测：在 /测试 下建 Season 再删掉，GET webapi.115.com/rb 返回
// {"id":"…","file_name":"Season","type":"2","dtime":"…","cid":"<测试的 cid>","parent_name":"测试"}。
// 父目录多半还在（删的是季目录，剧目录还在），拿 cid 走祖先链就是完整路径。
//
// 参考项目都没这么做：p115strmhelper 只按 file_id 查自己的目录表，查不到就不处理；
// 它注释掉的一版是「回收站里按名字找到 → 还原 → 取路径 → 再删」，会动用户网盘，这里只读列表。

// rbEntry 回收站里的一条
type rbEntry struct {
	id, name, typ, cid, parentName string
	dtime                          int64
}

// rbListLimit 一次读回收站多少条。按删除时间倒序，刚删的在最前面
const rbListLimit = 115

// fetch115RecycleBin 读回收站第一页（只读）
func fetch115RecycleBin(cookie string) ([]rbEntry, error) {
	query := url.Values{
		"aid":    {"7"},
		"cid":    {"0"},
		"offset": {"0"},
		"limit":  {fmt.Sprint(rbListLimit)},
		"format": {"json"},
	}
	body, err := httpGet115UA("https://webapi.115.com/rb", query, cookie, ua115Unified(), 20*time.Second)
	if err != nil {
		return nil, err
	}
	return parseRecycleBin(body)
}

func parseRecycleBin(body []byte) ([]rbEntry, error) {
	var r struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  []struct {
			ID         json.RawMessage `json:"id"`
			FileName   string          `json:"file_name"`
			Type       json.RawMessage `json:"type"`
			Dtime      json.RawMessage `json:"dtime"`
			Cid        json.RawMessage `json:"cid"`
			ParentName string          `json:"parent_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析回收站列表失败: %s", truncateStr(string(body), 200))
	}
	if !r.State {
		return nil, fmt.Errorf("115 拒绝回收站列表请求: %s", r.Error)
	}
	out := make([]rbEntry, 0, len(r.Data))
	for _, e := range r.Data {
		var dt int64
		fmt.Sscan(rawStr(e.Dtime), &dt)
		out = append(out, rbEntry{
			id: rawStr(e.ID), name: e.FileName, typ: rawStr(e.Type),
			cid: rawStr(e.Cid), parentName: e.ParentName, dtime: dt,
		})
	}
	return out, nil
}

// rbDeleteWindow 回收站条目的删除时间与事件时间相差多少以内算同一次删除
const rbDeleteWindow = 10 * 60

// recycledParentOf 在回收站里找被删目录原来的父目录 id，认不准返回 ""。
//
// 回收站条目的 id 是不是目录自己的 id 没实测过，所以两种都认：id 对上直接用；
// 否则按「名字相同 + 是目录 + 删除时间与事件相差 10 分钟内」找，
// 找到几条但父目录不是同一个（几部剧的 Season 0 前后脚删）就认不准，返回 ""
func recycledParentOf(entries []rbEntry, dirID, name string, at int64) string {
	if name == "" {
		return ""
	}
	cid := ""
	for _, e := range entries {
		if e.typ != "2" || e.name != name || e.cid == "" || e.cid == "0" {
			continue
		}
		if dirID != "" && e.id == dirID {
			return e.cid
		}
		if at <= 0 || e.dtime <= 0 || e.dtime-at > rbDeleteWindow || at-e.dtime > rbDeleteWindow {
			continue
		}
		if cid != "" && cid != e.cid {
			return ""
		}
		cid = e.cid
	}
	return cid
}

// recycledParent 一轮增量最多读一次回收站：同一轮删了好几个目录时共用这一份
func (d *realIncrDeps) recycledParent(dirID, name string, at int64) string {
	if !d.rbLoaded {
		d.rbLoaded = true
		entries, err := fetch115RecycleBin(d.cookie)
		if err != nil {
			vlog("[同步] ○ 读回收站失败（%v），被删目录的位置认不出", err)
		}
		d.rbEntries = entries
	}
	return recycledParentOf(d.rbEntries, dirID, name, at)
}
