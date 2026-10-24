package api

// 订阅一轮里转存进来的包装目录先压着不整理，等这一轮提交完了再一次整理。
//
// 订阅一轮可能要试几十条资源、跑好几分钟（RE0 一条解锁就十几秒），每条分享 / 离线各进一个
// 包装目录。原来守望者每分钟一看、离线秒传 10 秒一试，各自入队「转存触发」整理，一轮下来
// 同一部剧被拆成四五次整理：入库通知四五条、刮削 / Emby 刷新 / 提前探测各跑四五遍
// （2026-10-17 现场：凡人修仙传缺 20 集，一轮推了 4 条入库通知）。
//
// 只在内存里记：重启就没了，等于放行，守望者照常接管，不会有东西永远卡在转存目录。
// 离线下载这一轮里没下完的不等它：任务结束就放行，下完了由离线监视器照常触发整理。

import (
	"sync"
	"time"
)

var subWrapHold = struct {
	sync.Mutex
	cids map[string]bool
	// landing 刚放行的包装目录 → 放行时刻。115 转存是异步的：提交接口回成功时文件往往还没落进目录，
	// 放行后马上入队的那次整理看到的是空目录，按空壳删掉，文件随后落到转存目录根上，
	// 包装目录名里的片名、年份、季号全丢了（2026-10-24 现场：凡人修仙传 S01E197 英文名的单集文件
	// 光着进来，没认出，移进冗余）。所以放行后一段时间里空着的不删，等它落地
	landing map[string]time.Time
}{cids: map[string]bool{}, landing: map[string]time.Time{}}

// subWrapLandingGrace 放行后多久内空着的包装目录不当空壳删。守望者对它最多空跑两次
// （第一次之后冷却 5 分钟），连续三次才熔断，所以别超过 10 分钟
const subWrapLandingGrace = 10 * time.Minute

func holdSubWrapper(cid string) {
	if cid == "" {
		return
	}
	subWrapHold.Lock()
	subWrapHold.cids[cid] = true
	subWrapHold.Unlock()
}

func releaseSubWrappers(cids ...string) {
	subWrapHold.Lock()
	now := time.Now()
	for c, at := range subWrapHold.landing {
		if now.Sub(at) > subWrapLandingGrace {
			delete(subWrapHold.landing, c)
		}
	}
	for _, c := range cids {
		if subWrapHold.cids[c] {
			subWrapHold.landing[c] = now
		}
		delete(subWrapHold.cids, c)
	}
	subWrapHold.Unlock()
}

// subWrapperLanding 这个目录是不是刚放行、转存的文件可能还没落进来的包装目录
func subWrapperLanding(id string) bool {
	if id == "" {
		return false
	}
	subWrapHold.Lock()
	defer subWrapHold.Unlock()
	at, ok := subWrapHold.landing[id]
	return ok && time.Since(at) <= subWrapLandingGrace
}

// subWrapperHeld 这个目录是不是正在跑的订阅任务刚建的包装目录
func subWrapperHeld(id string) bool {
	if id == "" {
		return false
	}
	subWrapHold.Lock()
	defer subWrapHold.Unlock()
	return subWrapHold.cids[id]
}
