package api

import (
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"115-station/internal/model"
)

// ==================== 刮削后补演职人员（personfill.after_scrape）====================
//
// 定时任务按加入时间从续扫断点绕全库，新入库的片要等夜里那一轮扫到才有中文名和头像。
// 打开「刮削后补全」后，每个刮削任务结束时把这次刮过的片目（TMDB 编号）排成一个人物任务，只看这几部：
//   - 不动续扫断点，与定时任务各走各的（去重键也分开）；
//   - 刮削收尾才提交 Emby 刷新，人物要等 Emby 读完新 NFO 才建出来，所以任务开头先等一会儿，
//     找不到条目（或条目上还没有人物）就隔几分钟再找，等满还没有的留给定时任务；
//   - 按 TMDB 编号找 Emby 条目（AnyProviderIdEquals），不按路径：本地路径到 Emby 路径还隔着一层映射，
//     而刚写好的 NFO 里一定带 tmdbid。

// personTarget 一部要补人物的片目
type personTarget struct {
	Kind   string `json:"kind"` // movie / tv
	TmdbID int    `json:"tmdb_id"`
	Title  string `json:"title,omitempty"`
}

func (t personTarget) key() string { return t.Kind + ":" + strconv.Itoa(t.TmdbID) }

// personJobParams 刮削后补全的参数（jobParams.Person）；没有它就是定时 / 手动的全库续扫
type personJobParams struct {
	Targets []personTarget `json:"targets"`
}

// personAfterScrapeWaits 从任务开始算的几个查找时刻：第一次给 Emby 留出读 NFO、建人物的时间，
// 之后找不到的再等。测试替身的缝
var personAfterScrapeWaits = []time.Duration{time.Minute, 3 * time.Minute, 6 * time.Minute, 10 * time.Minute}

// personAfterScrapeMax 一个任务最多带多少部片目（整理后刮削偶尔一次几百部，超出的交给定时任务）
const personAfterScrapeMax = 200

func mergePersonTargets(a, b []personTarget) []personTarget {
	seen := map[string]bool{}
	var out []personTarget
	for _, t := range append(append([]personTarget{}, a...), b...) {
		if t.TmdbID <= 0 || (t.Kind != "movie" && t.Kind != "tv") || seen[t.key()] {
			continue
		}
		seen[t.key()] = true
		out = append(out, t)
	}
	if len(out) > personAfterScrapeMax {
		out = out[len(out)-personAfterScrapeMax:] // 留新的：老的多半已经排过一次
	}
	return out
}

func personTargetsTitle(ts []personTarget) string {
	if len(ts) == 0 {
		return "刮削后补演职人员"
	}
	name := ts[0].Title
	if name == "" {
		name = ts[0].key()
	}
	if len(ts) == 1 {
		return fmt.Sprintf("刮削后补演职人员：《%s》", name)
	}
	return fmt.Sprintf("刮削后补演职人员：《%s》等 %d 部", name, len(ts))
}

// enqueuePersonAfterScrape 刮削任务结束时调用。开关没开 / 没配 Emby 返回 ok=false。
// 排着的还没开始就并进去（片目取并集），在跑的不受影响
func enqueuePersonAfterScrape(h *Handler, targets []personTarget) (job model.TaskJob, ok bool, err error) {
	if !h.loadPersonFillCfg().AfterScrape {
		return job, false, nil
	}
	targets = mergePersonTargets(nil, targets)
	if len(targets) == 0 {
		return job, false, nil
	}
	if base, key, ok := h.embyServerInfo(); !ok || key == "" || base == "" {
		return job, false, nil
	}
	job, err = enqueueJob(h.DB, jobSpec{
		Kind: jobKindPerson, Title: personTargetsTitle(targets), DedupeKey: "personfill:scrape",
		Source: "scrape", Priority: jobPriorityBackground,
		Params: jobParams{Person: &personJobParams{Targets: targets}},
		Merge: func(prev, next jobParams) (jobParams, string) {
			var a, b []personTarget
			if prev.Person != nil {
				a = prev.Person.Targets
			}
			if next.Person != nil {
				b = next.Person.Targets
			}
			m := mergePersonTargets(a, b)
			return jobParams{Person: &personJobParams{Targets: m}}, personTargetsTitle(m)
		},
	})
	return job, err == nil, err
}

// embyTitlesByTmdb Emby 里 TMDB 编号对得上的电影 / 剧集（带人物）。多版本电影可能是几个条目，都返回
func embyTitlesByTmdb(base, key string, t personTarget) ([]embyTitleItem, error) {
	typ := "Movie"
	if t.Kind == "tv" {
		typ = "Series"
	}
	id := strconv.Itoa(t.TmdbID)
	q := url.Values{
		"Recursive": {"true"}, "IncludeItemTypes": {typ}, "AnyProviderIdEquals": {"tmdb." + id},
		"Fields": {"ProviderIds,People"}, "EnableUserData": {"false"}, "Limit": {"20"},
	}
	var page struct {
		Items []embyTitleItem `json:"Items"`
	}
	if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
		return nil, err
	}
	// 再核一遍编号：参数拼错或老版本 Emby 不认这个参数时会返回整库
	var out []embyTitleItem
	for _, it := range page.Items {
		if providerID(it.ProviderIds, "Tmdb") == id {
			out = append(out, it)
		}
	}
	return out, nil
}

// personWaitUntil 睡到 at；被要求停止返回 false
func personWaitUntil(at time.Time) bool {
	for {
		d := time.Until(at)
		if d <= 0 {
			return true
		}
		if d > time.Second {
			d = time.Second
		}
		select {
		case <-stopCh:
			return false
		case <-time.After(d):
		}
		if personLane.stopRequested() {
			return false
		}
	}
}

// execPersonTargets 刮削后补全：只看点名的几部片目
func execPersonTargets(r *personRunner, job *model.TaskJob, targets []personTarget) (jobOutcome, error) {
	start := time.Now()
	log.Printf("[演职人员] ▶ 刮削后补全：%d 部片目（等 Emby 读完 NFO 再查，记账中暂缓 %d 个人物）", len(targets), len(r.skip))
	pending := targets
	titles := 0
	stopped, capped := false, false
	var lastErr error
	for i, at := range personAfterScrapeWaits {
		if len(pending) == 0 || stopped || capped {
			break
		}
		personLane.set("等待 Emby 入库", titles, len(targets), "")
		if !personWaitUntil(start.Add(at)) {
			stopped = true
			break
		}
		last := i == len(personAfterScrapeWaits)-1
		var rest []personTarget
		for j, tg := range pending {
			if personLane.stopRequested() || stopRequestedGlobal() {
				stopped = true
				rest = append(rest, pending[j:]...)
				break
			}
			if capped {
				rest = append(rest, tg)
				continue
			}
			items, err := embyTitlesByTmdb(r.base, r.key, tg)
			if err != nil {
				lastErr = err
				rest = append(rest, tg)
				continue
			}
			// 条目在但上面还没有人物：Emby 多半还没读新 NFO，最后一次之前再等等
			people := 0
			for _, it := range items {
				people += len(it.People)
			}
			if len(items) == 0 || (people == 0 && !last) {
				rest = append(rest, tg)
				continue
			}
			titles++
			for _, it := range items {
				personLane.set("补全片目", titles, len(targets), it.Name)
				if s, c := r.runTitle(it); s || c {
					stopped, capped = s, c
					break
				}
			}
		}
		pending = rest
	}

	out := summarizePersonRun(r.results, titles)
	msg := personHandledMessage(out)
	if len(pending) > 0 {
		var names []string
		for _, t := range pending {
			n := t.Title
			if n == "" {
				n = t.key()
			}
			names = append(names, "《"+n+"》")
		}
		why := "等了 " + personAfterScrapeWaits[len(personAfterScrapeWaits)-1].String() + " Emby 里还查不到"
		switch {
		case stopped:
			why = "已按要求停止"
		case capped:
			why = "达到单次上限"
		case lastErr != nil:
			why = "读 Emby 出错：" + lastErr.Error()
		}
		msg += fmt.Sprintf("；%d 部没补（%s，交给定时任务）：%s", len(pending), why, truncateStr(strings.Join(names, "、"), 200))
	}
	log.Printf("[演职人员] ■ 刮削后补全：%s", msg)
	// 没查到的不算失败：Emby 没扫完、片目不在 Emby 的库里都会这样，定时任务之后会扫到
	partial := out.States[personStateFailed] > 0
	idle := job.Priority == jobPriorityBackground && out.Handled == 0 && len(pending) == 0
	return jobOutcome{Message: msg, Result: out, Canceled: stopped, Partial: partial, Idle: idle}, nil
}
