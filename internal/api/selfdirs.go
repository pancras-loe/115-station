package api

import (
	"strings"
	"sync"
	"time"
)

// ==================== 本站自己建 / 删的目录（内存备忘） ====================
//
// 2026-11-05 现场：订阅提交失败收掉空包装目录「我不是大师 (2026) S01 ·订阅71-14」，
// 增量收到删除事件却报「认不出它在哪」。两处缺口：
//   - pan115Ops.deleteFiles 删成功就 forgetDirSubtree，几秒后删除事件回来时
//     dirGone 按 file_id 查缓存已经什么都没有了；
//   - 包装目录建在转存文件夹下，父目录多半不在路径缓存里，mkdir 时 rememberDirAt 记不上。
// 这里只记本站自己动过的目录：建的时候记父目录与名字，删的时候把缓存里最后的位置抄一份，
// 删除事件回来时 dirGone 先查缓存、再查这里。零 115 请求（父目录位置由调用方按需解析）。
// 用错的风险由 incr 的目录名核对兜住；只在内存里，重启丢了退回原来的认法

const (
	selfDirTTL = 48 * time.Hour
	selfDirMax = 5000
)

type selfDir struct {
	parent, name string // mkdir 时记下
	abs          string // 删的时候缓存里最后的位置（可能为空）
	at           time.Time
}

var (
	selfDirMu  sync.Mutex
	selfDirMem = map[string]selfDir{}
)

func putSelfDirLocked(cid string, e selfDir) {
	now := time.Now()
	if len(selfDirMem) >= selfDirMax {
		for k, v := range selfDirMem {
			if now.Sub(v.at) > selfDirTTL {
				delete(selfDirMem, k)
			}
		}
		if len(selfDirMem) >= selfDirMax {
			selfDirMem = map[string]selfDir{}
		}
	}
	e.at = now
	selfDirMem[cid] = e
}

// noteSelfMkdir 本站建了一个目录
func noteSelfMkdir(parent, name, cid string) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if cid == "" || cid == "0" || name == "" || strings.Contains(name, "/") {
		return
	}
	selfDirMu.Lock()
	putSelfDirLocked(cid, selfDir{parent: parent, name: name})
	selfDirMu.Unlock()
}

// noteSelfDelete 本站删了这些条目：在清缓存之前把目录的位置抄下来
func noteSelfDelete(fids []string) {
	selfDirMu.Lock()
	defer selfDirMu.Unlock()
	for _, fid := range fids {
		if fid == "" || fid == "0" {
			continue
		}
		e, had := selfDirMem[fid]
		abs, ok := lookupDirAbsAnyAge(fid)
		if ok && strings.Trim(abs, "/") != "" {
			e.abs = strings.TrimSuffix(abs, "/")
		} else if !had {
			continue // 既没建过也没缓存：多半是文件，不记
		}
		putSelfDirLocked(fid, e)
	}
}

// selfDirWhere 按 file_id 取备忘：已知位置，或父目录 + 名字。过期的不用
func selfDirWhere(cid string) (abs, parent, name string, ok bool) {
	selfDirMu.Lock()
	e, ok := selfDirMem[cid]
	selfDirMu.Unlock()
	if !ok || time.Since(e.at) > selfDirTTL {
		return "", "", "", false
	}
	return e.abs, e.parent, e.name, true
}
