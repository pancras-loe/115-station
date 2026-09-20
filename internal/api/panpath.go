package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== cid → 网盘绝对路径解析器 ====================
//
// 生活事件只带 parent_id，每条事件都要先把 cid 还原成路径。
// 此前的做法是逐级打 files/get_info 往上爬父目录链 —— 每层一次请求 × 节流，
// 一个四层深的新目录就是四秒。
//
// 现在改成一次请求拿整条祖先链：
//
//	GET {webapi}/files?cid=..&limit=1&hide_data=1
//
// 响应里的 path 数组就是从根到该目录的每一级（2026-09-19 真机实测）。
// 拿到后把每一级都写进缓存，后续同目录的事件零请求。
//
// 三级回退：内存 → PathCache 表 → 接口。

// ancestor 祖先链上的一级
type ancestor struct {
	cid, pid, name, aid string
}

// errDirGone 目录已不存在（被删、进了回收站、或 id 根本无效）
var errDirGone = errors.New("目录已不存在")

const (
	// pathMemTTL 内存层存活时间。DB 层才是权威，内存只为省一次查询
	pathMemTTL = 30 * time.Minute
	// pathRowTTL DB 行的保险期：失效钩子万一漏了一处，最多陈旧这么久
	pathRowTTL = 7 * 24 * time.Hour
	// pathMemMax 内存层条目上限，超了整体清空（下次从 DB 重新预热）
	pathMemMax = 20000
)

type pathMemEntry struct {
	row model.PathCache
	at  time.Time
}

var (
	pathCacheMu  sync.RWMutex
	pathCacheMem = map[string]pathMemEntry{}
)

// rawStr 把 json.RawMessage 还原成字符串，字符串与数字两种形态都认。
// 115 同一个字段在不同通道下会变形态（data.count 就是字符串/数字各一套），
// 这里统一挡掉
func rawStr(r json.RawMessage) string {
	s := strings.TrimSpace(string(r))
	if s == "" || s == "null" {
		return ""
	}
	if len(s) >= 2 && s[0] == '"' {
		var out string
		if json.Unmarshal(r, &out) == nil {
			return out
		}
	}
	return s
}

// fetch115Ancestors 一次请求取整条祖先链（根在最前，末元素是 cid 自身）。
//
// ⚠️⚠️ 115 对不存在/已删除的 cid **不报错**：返回 HTTP 200 + state:true，
// 但响应体里的 cid 变成 0、path 只剩根那一级、data 是网盘根目录的内容。
// 所以必须自己校验末元素的 cid 等于请求的 cid —— 漏了这一步，
// 一个已删除的父目录会被解析成「网盘根」，路径推导随即指向媒体库根下的同名文件
func fetch115Ancestors(cookie, cid string) ([]ancestor, error) {
	query := url.Values{
		"aid":              {"1"},
		"cid":              {cid},
		"limit":            {"1"},
		"offset":           {"0"},
		"show_dir":         {"1"},
		"hide_data":        {"1"}, // 只要 path，不要 data（实测不影响 path）
		"cur":              {"1"},
		"nf":               {"1"},
		"count_folders":    {"1"},
		"record_open_time": {"1"},
	}
	var lastErr error
	for _, origin := range webapiFileOrigins {
		body, err := httpGet115UA(origin+"/files", query, cookie, ua115Unified(), 20*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		var r struct {
			State bool   `json:"state"`
			Error string `json:"error"`
			Path  []struct {
				Cid  json.RawMessage `json:"cid"`
				Pid  json.RawMessage `json:"pid"`
				Aid  json.RawMessage `json:"aid"`
				Name string          `json:"name"`
			} `json:"path"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			lastErr = fmt.Errorf("解析祖先链失败: %s", truncateStr(string(body), 200))
			continue
		}
		if !r.State {
			lastErr = fmt.Errorf("115 拒绝祖先链请求: %s", r.Error)
			// Cookie 失效换镜像也没用
			if strings.Contains(r.Error, "登录") || strings.Contains(r.Error, "acc") {
				return nil, lastErr
			}
			continue
		}
		chain := make([]ancestor, 0, len(r.Path))
		for _, n := range r.Path {
			chain = append(chain, ancestor{
				cid: rawStr(n.Cid), pid: rawStr(n.Pid), aid: rawStr(n.Aid), name: n.Name,
			})
		}
		if ancestorChainGone(chain, cid) {
			return nil, errDirGone
		}
		return chain, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有镜像域名均不可用")
	}
	return nil, lastErr
}

// ancestorChainGone 判定「这条链说明目录已经不在了」。
//
// 115 对不存在/已删除的 cid 不报错，而是静默按根目录处理（返回 HTTP 200 +
// state:true，path 只剩根那一级）。认不出这一点，已删除的父目录会被解析成
// 「网盘根」，路径推导随即指向媒体库根下的同名文件 —— 删错东西的经典路径
func ancestorChainGone(chain []ancestor, cid string) bool {
	if len(chain) == 0 {
		return true
	}
	last := chain[len(chain)-1]
	if last.cid != cid {
		return true // 115 把我们降级到根目录了
	}
	// aid 非 1 说明不在正常区（回收站等）。回收站的确切返回没实测过，
	// 稳妥起见一并挡掉：宁可判成「已消失」跳过，也不要拿着它的路径去删东西
	return last.aid != "" && last.aid != "1"
}

// chainToRows 把祖先链拼成逐级的缓存行。
// 根元素（cid=0，名字是「根目录」）必须跳过，否则每条路径都会平白多一层
func chainToRows(chain []ancestor) []model.PathCache {
	rows := make([]model.PathCache, 0, len(chain))
	acc := ""
	for _, a := range chain {
		if a.cid == "0" {
			continue
		}
		acc += "/" + a.name
		rows = append(rows, model.PathCache{FileID: a.cid, ParentID: a.pid, Name: a.name, Path: acc})
	}
	return rows
}

// lookupCachedRow 只查缓存（内存 → DB），不打接口
func lookupCachedRow(cid string) (model.PathCache, bool) {
	if cid == "" || cid == "0" {
		return model.PathCache{}, false
	}
	pathCacheMu.RLock()
	e, ok := pathCacheMem[cid]
	pathCacheMu.RUnlock()
	if ok && time.Since(e.at) < pathMemTTL {
		return e.row, true
	}
	if model.DB == nil {
		return model.PathCache{}, false
	}
	var row model.PathCache
	if err := model.DB.Where("file_id = ?", cid).First(&row).Error; err != nil {
		return model.PathCache{}, false
	}
	if time.Since(row.UpdatedAt) > pathRowTTL {
		return model.PathCache{}, false
	}
	putMem(row)
	return row, true
}

// lookupCachedAbs 只查缓存拿绝对路径。
// move/rename 找【旧】路径专用 —— 事件里的 parent_id/file_name 都是新位置，
// 旧位置只可能在缓存里，查不到就是查不到，不该为此发请求
func lookupCachedAbs(cid string) (string, bool) {
	row, ok := lookupCachedRow(cid)
	if !ok {
		return "", false
	}
	return row.Path, true
}

func putMem(row model.PathCache) {
	pathCacheMu.Lock()
	if len(pathCacheMem) >= pathMemMax {
		pathCacheMem = map[string]pathMemEntry{}
	}
	pathCacheMem[row.FileID] = pathMemEntry{row: row, at: time.Now()}
	pathCacheMu.Unlock()
}

// rememberDirPaths 批量写入缓存（DB + 内存）
func rememberDirPaths(rows []model.PathCache) {
	if len(rows) == 0 {
		return
	}
	now := time.Now()
	for i := range rows {
		rows[i].UpdatedAt = now
	}
	if model.DB != nil {
		model.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "file_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"parent_id", "name", "path", "updated_at"}),
		}).CreateInBatches(&rows, 200)
	}
	for _, r := range rows {
		putMem(r)
	}
}

// forgetPathsUnder 让某棵子树的缓存失效（目录被改名/移动/删除后必须调用）。
//
// LIKE 里的 _ / % 不转义：多删几行缓存只是下次重新请求，是安全的方向；
// 少删才会出事
func forgetPathsUnder(abs string) {
	abs = strings.TrimSuffix(abs, "/")
	if abs == "" {
		forgetAllDirPaths()
		return
	}
	if model.DB != nil {
		model.DB.Where("path = ? OR path LIKE ?", abs, abs+"/%").Delete(&model.PathCache{})
	}
	pathCacheMu.Lock()
	for k, e := range pathCacheMem {
		if e.row.Path == abs || strings.HasPrefix(e.row.Path, abs+"/") {
			delete(pathCacheMem, k)
		}
	}
	pathCacheMu.Unlock()
}

// repathSubtree 目录改名/移动后，把缓存里整棵子树的路径前缀换掉。
//
// 为什么是改而不是清：接下来处理这批事件时还要用子孙的【新】路径，
// 清掉就得一个个重新请求回来。
// SQL 里用 length(?) 而不是 Go 的 len()：Go 数字节、SQLite 的 substr 数字符，
// 中文目录名下两者对不上
func repathSubtree(oldAbs, newAbs string) {
	oldAbs = strings.TrimSuffix(oldAbs, "/")
	newAbs = strings.TrimSuffix(newAbs, "/")
	if oldAbs == "" || newAbs == "" || oldAbs == newAbs {
		return
	}
	if model.DB != nil {
		model.DB.Model(&model.PathCache{}).
			Where("path = ? OR path LIKE ?", oldAbs, oldAbs+"/%").
			Updates(map[string]interface{}{
				"path":       gorm.Expr("? || substr(path, length(?) + 1)", newAbs, oldAbs),
				"updated_at": time.Now(),
			})
	}
	pathCacheMu.Lock()
	for k, e := range pathCacheMem {
		if e.row.Path == oldAbs || strings.HasPrefix(e.row.Path, oldAbs+"/") {
			e.row.Path = newAbs + e.row.Path[len(oldAbs):]
			pathCacheMem[k] = e
		}
	}
	pathCacheMu.Unlock()
}

// forgetDirSubtree 按 cid 失效子树。缓存里没有这个 cid 就什么都不用做：
// 子孙只会作为它的祖先链的一部分被缓存，它没缓存过，子孙也不可能有
func forgetDirSubtree(cid string) {
	if cid == "" || cid == "0" {
		return
	}
	if row, ok := lookupCachedRow(cid); ok {
		forgetPathsUnder(row.Path)
	}
}

// forgetAllDirPaths 整体清空。目录结构发生了无法定位的变化时用
func forgetAllDirPaths() {
	pathCacheMu.Lock()
	pathCacheMem = map[string]pathMemEntry{}
	pathCacheMu.Unlock()
	if model.DB != nil {
		model.DB.Where("1 = 1").Delete(&model.PathCache{})
	}
}

// resolveDirAbs 目录绝对路径，走缓存
func resolveDirAbs(cookie, cid string) (string, error) {
	if cid == "" || cid == "0" {
		return "/", nil
	}
	if p, ok := lookupCachedAbs(cid); ok {
		return p, nil
	}
	return resolveDirAbsFresh(cookie, cid)
}

// resolveDirAbsFresh 强制打接口，不吃缓存。
// 安全关键判定（整理的保护子树、增量的排除区与熔断体检）用它：
// 整理会整目录搬移，缓存陈旧一次就可能把库内内容当成待整理素材
func resolveDirAbsFresh(cookie, cid string) (string, error) {
	if cid == "" || cid == "0" {
		return "/", nil
	}
	chain, err := fetch115Ancestors(cookie, cid)
	if err != nil {
		return "", err
	}
	rows := chainToRows(chain)
	if len(rows) == 0 {
		return "/", nil
	}
	rememberDirPaths(rows)
	return rows[len(rows)-1].Path, nil
}
