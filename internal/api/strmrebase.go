package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"115-station/internal/model"
)

// ==================== 批量改写 STRM 直链地址 ====================
//
// 用户反馈（2026-10-24）：早先没填「STRM 直连域名」，STRM 里写的是兜底的 Docker 回环
// http://172.17.0.1:6086，换成 NAS 地址后已生成的上千个 STRM 还是旧地址。原来只能改完域名、
// 「本地已存在时」选覆盖再跑一次全量，代价是整库遍历 115。
//
// 这里只动本地：按台账视频行读本地 STRM，本站格式的直链（{地址}/d/{pickcode}…）
// 把 /d/ 之前那一段换成当前配置的域名，pickcode、后缀、?/文件名原样留着。零 115 请求；
// Emby 查询与播放都现读 STRM 内容（2026-10-17 实测），不用通知 Emby。
//
// 参考：LitePan「一键替换播放基址」（strm/baseurl_replace.go，只读思路）—— 它见到 http 开头的
// 就换，这里只认本站格式，别的项目 / 手写的 STRM 不碰；SmartStrm「STRM 查找替换」踩过的坑：
// 改写后修改时间要还原，否则 Emby 按加入时间的排序被打乱。
//
// 格式 / 「保留文件后缀」改了不在这里处理：pick_code 改 pick_code_name 要原文件名，
// 台账没存，仍得跑一次覆盖模式的全量

// strmRebaseSamples 预演里给几条示例
const strmRebaseSamples = 3

type strmBaseCount struct {
	Base  string `json:"base"`
	Count int    `json:"count"`
}

type strmRebaseSample struct {
	Rel string `json:"rel"`
	Old string `json:"old"`
	New string `json:"new"`
}

// strmRebaseReport 预演与执行共用的报告
type strmRebaseReport struct {
	Target  string `json:"target"`  // 改成的地址（当前配置的域名）
	Total   int    `json:"total"`   // 台账里的视频行
	Missing int    `json:"missing"` // 本地读不到的（失效 STRM、用户手删）
	Foreign int    `json:"foreign"` // 不是本站格式的直链，不碰
	Current int    `json:"current"` // 已经是当前地址
	// Stale 写着别的地址的，按旧地址分组（多的在前）
	Stale      []strmBaseCount    `json:"stale"`
	StaleTotal int                `json:"stale_total"`
	Samples    []strmRebaseSample `json:"samples"`
	// 以下只在执行时有
	Rewritten int `json:"rewritten"`
	Failed    int `json:"failed"`
}

// splitStrmBase 把本站直链拆成「地址」与「/d/ 起的其余部分」。
// 地址可以带路径前缀（域名填了 https://x.com/strm 时直链是 https://x.com/strm/d/…），
// 所以找的是主机之后第一个 /d/，不是整串的第一个
func splitStrmBase(u string) (base, rest string, ok bool) {
	u = strings.TrimSpace(u)
	head, _, _ := strings.Cut(u, "?")
	scheme, after, found := strings.Cut(head, "://")
	if !found || (!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https")) {
		return "", "", false
	}
	slash := strings.Index(after, "/")
	if slash <= 0 {
		return "", "", false // 没有路径，或者主机是空的
	}
	hostEnd := len(scheme) + 3 + slash
	i := strings.Index(head[hostEnd:], "/d/")
	if i < 0 || len(head) <= hostEnd+i+3 {
		return "", "", false
	}
	if pu, err := url.Parse(head[:hostEnd]); err != nil || pu.Host == "" {
		return "", "", false
	}
	return head[:hostEnd+i], u[hostEnd+i:], true
}

// sameStrmBase 地址比较：结尾斜杠不算、大小写不算（主机名不分大小写，路径前缀极少有人靠大小写区分）
func sameStrmBase(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

// rebaseStrms 扫一遍台账视频行对应的本地 STRM。from 为空 = 所有写着别的地址的；
// 否则只改旧地址在 from 里的。apply=false 只统计（预演），true 才写回
func rebaseStrms(db *gorm.DB, root, target string, from []string, apply bool, progress func(done, total int)) (strmRebaseReport, error) {
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	rep := strmRebaseReport{Target: target, Stale: []strmBaseCount{}, Samples: []strmRebaseSample{}}
	var rels []string
	if err := db.Model(&model.SyncedFile{}).Where("kind = ?", "video").Order("rel_path").Pluck("rel_path", &rels).Error; err != nil {
		return rep, err
	}
	rep.Total = len(rels)
	picked := func(base string) bool {
		if len(from) == 0 {
			return true
		}
		for _, f := range from {
			if sameStrmBase(f, base) {
				return true
			}
		}
		return false
	}
	counts := map[string]int{}
	for i, rel := range rels {
		if progress != nil && i%200 == 0 {
			progress(i, len(rels))
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			rep.Missing++
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			rep.Missing++
			continue
		}
		content := string(b)
		first, tail, multiline := strings.Cut(content, "\n")
		base, rest, ok := splitStrmBase(first)
		if !ok {
			rep.Foreign++
			continue
		}
		if sameStrmBase(base, target) {
			rep.Current++
			continue
		}
		// 分组按去掉结尾斜杠的写法，免得 http://a 与 http://a/ 分成两组
		key := strings.TrimRight(base, "/")
		counts[key]++
		rep.StaleTotal++
		if !picked(base) {
			continue
		}
		next := target + rest
		if len(rep.Samples) < strmRebaseSamples {
			rep.Samples = append(rep.Samples, strmRebaseSample{Rel: rel, Old: strings.TrimSpace(first), New: next})
		}
		if !apply {
			continue
		}
		out := next
		if multiline {
			out += "\n" + tail // 本站写的只有一行；万一后面还有东西，原样留着
		}
		if err := os.WriteFile(p, []byte(out), 0o666); err != nil {
			log.Printf("[STRM] ✗ 改写地址失败 %s: %v", rel, err)
			rep.Failed++
			continue
		}
		// 还原修改时间：Emby 按「加入时间」排序看的就是它（SmartStrm 同款）
		_ = os.Chtimes(p, st.ModTime(), st.ModTime())
		rep.Rewritten++
	}
	for base, n := range counts {
		rep.Stale = append(rep.Stale, strmBaseCount{Base: base, Count: n})
	}
	sort.Slice(rep.Stale, func(i, j int) bool {
		if rep.Stale[i].Count != rep.Stale[j].Count {
			return rep.Stale[i].Count > rep.Stale[j].Count
		}
		return rep.Stale[i].Base < rep.Stale[j].Base
	})
	if rep.Total > 0 && rep.Missing == rep.Total {
		// 挂载没就绪时整棵树都读不到，别报成「全部已是新地址」
		return rep, fmt.Errorf("台账有 %d 个视频，本地一个 STRM 都读不到（%s 挂载未就绪？）", rep.Total, root)
	}
	return rep, nil
}

// strmRebaseTarget 改成哪个地址：只认用户在配置里**明确填过**的域名。
// 没填时 parseStrmConfig 兜底成 Docker 回环 —— 那正是要改掉的地址，不能拿它当目标
func (h *Handler) strmRebaseTarget() (string, error) {
	var cfg struct {
		Domain string `json:"domain"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("strm")), &cfg)
	d := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if d == "" {
		return "", fmt.Errorf("还没填「STRM 直连域名」：先填好并保存，再改写已有 STRM")
	}
	if _, _, ok := splitStrmBase(d + "/d/x"); !ok {
		return "", fmt.Errorf("「STRM 直连域名」%s 不是 http(s) 地址", d)
	}
	return d, nil
}

// StrmRebasePreview GET /sync/strm-rebase/preview：只读，读本地文件、零 115 请求
func (h *Handler) StrmRebasePreview(c *gin.Context) {
	target, err := h.strmRebaseTarget()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rep, err := rebaseStrms(h.DB, localMediaRoot(), target, nil, false, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// StrmRebase POST /sync/strm-rebase  body: {"from": ["http://172.17.0.1:6086"]} → 入队，202。
// from 是预演里列出的旧地址（用户可以只勾一部分）；空 = 全部
func (h *Handler) StrmRebase(c *gin.Context) {
	var req struct {
		From []string `json:"from"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	target, err := h.strmRebaseTarget()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "strmrebase", Title: "改写 STRM 地址为 " + target,
		DedupeKey: "strmrebase", Source: "web", Priority: jobPriorityManual, Params: jobParams{RebaseFrom: req.From}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "改写")
}

func init() {
	jobExecutors["strmrebase"] = execStrmRebaseJob
}

// execStrmRebaseJob 在主队列里跑（拿着 taskMu）：增量 / 全量也写这些 STRM，不能同时动。
// 目标地址执行时再读一次：排队期间用户可能又改了域名
func execStrmRebaseJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	target, err := h.strmRebaseTarget()
	if err != nil {
		return jobOutcome{}, err
	}
	root := localMediaRoot()
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return jobOutcome{}, fmt.Errorf("本地媒体库目录不可访问：%s", root)
	}
	rep, err := rebaseStrms(h.DB, root, target, decodeJobParams(job).RebaseFrom, true, func(done, total int) {
		setJobProgress("改写 STRM 地址", done, total, "")
	})
	if err != nil {
		return jobOutcome{}, err
	}
	log.Printf("[STRM] ✓ 改写地址为 %s：改写 %d 个、失败 %d 个（已是新地址 %d、非本站格式 %d、本地没有 %d）",
		target, rep.Rewritten, rep.Failed, rep.Current, rep.Foreign, rep.Missing)
	out := jobOutcome{Result: rep}
	switch {
	case rep.Rewritten == 0 && rep.Failed == 0:
		out.Message = "没有要改写的 STRM，都已经是 " + target
	case rep.Failed > 0:
		out.Partial = true
		out.Message = fmt.Sprintf("已改写 %d 个 STRM 的地址，%d 个没改成（看日志 [STRM]）", rep.Rewritten, rep.Failed)
	default:
		out.Message = fmt.Sprintf("已把 %d 个 STRM 的地址改成 %s", rep.Rewritten, target)
	}
	return out, nil
}
