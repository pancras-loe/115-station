package api

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ==================== 新写出的 STRM 登记（入库后只探新入库的集） ====================
//
// 入库确认拿去回查的只是几个样本（embyVerifySample）或刷新目标目录，回答得了「这一轮进没进库」，
// 回答不了「这一轮新进了哪几集」。此前入库确认后一律把所在片目整部排进自动探测：
// 往一部 1665 集、大部分没探过的番剧里加一集，就排进去一千多集（2026-10-04 现场，维护者要求只探新入库的）。
//
// 所以 writeStrm 每真写出一个 STRM 就在这里记一笔，入库确认到某个片目时把这个片目下记着的取走去探。
// 多记几个无妨：内容变了重写的（换了直链域名）也会记上，但探测只碰 Emby 里还缺媒体信息的条目，
// 已有的不会请求。只在内存里：重启后丢了，那一轮就只探确认到的样本本身

type freshStrmSet struct {
	sync.Mutex
	m map[string]time.Time // 本地绝对路径（正斜杠）→ 过期时间
}

var freshStrms = freshStrmSet{m: map[string]time.Time{}}

// freshStrmTTL 记多久：入库确认最晚由 Emby 入库事件补上（回查 10 分钟、刮削队列可能排一阵），给足余量
const freshStrmTTL = 6 * time.Hour

// freshStrmMax 上限：首次全量能一下写出几万个，过了上限先清过期的，还满就不再记（那一轮回退成只探样本）
const freshStrmMax = 200000

func freshKey(local string) string {
	return strings.TrimRight(filepath.ToSlash(filepath.Clean(local)), "/")
}

// markFreshStrm 记下一个刚写出的 STRM
func markFreshStrm(local string) {
	k := freshKey(local)
	if k == "" || k == "." {
		return
	}
	freshStrms.Lock()
	defer freshStrms.Unlock()
	now := time.Now()
	if len(freshStrms.m) >= freshStrmMax {
		for p, t := range freshStrms.m {
			if now.After(t) {
				delete(freshStrms.m, p)
			}
		}
		if len(freshStrms.m) >= freshStrmMax {
			return
		}
	}
	freshStrms.m[k] = now.Add(freshStrmTTL)
}

// takeFreshStrms 取走某个本地目录下记着的 STRM（取走即删，同一批不会被两次入库确认各探一遍）
func takeFreshStrms(localDir string) []string {
	dir := freshKey(localDir)
	if dir == "" {
		return nil
	}
	freshStrms.Lock()
	defer freshStrms.Unlock()
	now := time.Now()
	var out []string
	for p, t := range freshStrms.m {
		if now.After(t) {
			delete(freshStrms.m, p)
			continue
		}
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, filepath.FromSlash(p))
			delete(freshStrms.m, p)
		}
	}
	return out
}
