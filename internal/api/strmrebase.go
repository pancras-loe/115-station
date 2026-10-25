package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"115-station/internal/model"
)

// ==================== 批量替换 STRM 地址 ====================
//
// 用户反馈（2026-10-24）：早先没填「STRM 直连域名」，STRM 里写的是兜底的 Docker 回环
// http://172.17.0.1:6086，换成 NAS 地址后已生成的上千个 STRM 还是旧地址。原来只能改完域名、
// 「本地已存在时」选覆盖再跑一次全量，代价是整库遍历 115。
//
// 2026-10-25 起挪到扩展功能「STRM 迁移」，改成用户自己填「旧地址开头 → 新地址开头」：
// 不只本站写的，别的项目写的（p115strmhelper 的 http://旧IP:9527/…、挂载路径 /mnt/115/…）
// 换了 IP / 挂载点也能改。所以不再按台账读，而是扫本地媒体根下所有 .strm。
// 只换开头那一段：内容以旧开头起头、且旧开头后面紧跟 / ? # 或到结尾才算命中，
// 不在整条链接里找子串（http://a:95 不会命中 http://a:9527）。零 115 请求；
// Emby 查询与播放都现读 STRM 内容（2026-10-17 实测），不用通知 Emby。
//
// 参考：LitePan「一键替换播放基址」（strm/baseurl_replace.go，只读思路）；SmartStrm「STRM 查找替换」踩过的坑：
// 改写后修改时间要还原，否则 Emby 按加入时间的排序被打乱。
//
// 格式 / 「保留文件后缀」改了不在这里处理：pick_code 改 pick_code_name 要原文件名，
// 台账没存，仍得跑一次覆盖模式的全量；把别的项目的直链换成本站直链是「接管」（adopt.go），不是替换开头

// strmRebaseSamples 预演里给几条示例
const strmRebaseSamples = 5

// strmRebaseMaxPairs 一次最多几组替换
const strmRebaseMaxPairs = 20

// strmRebaseMaxGroups 预演里最多列几种地址开头
const strmRebaseMaxGroups = 30

// strmRebasePair 一组替换：内容以 From 开头的，把这一段换成 To
type strmRebasePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type strmBaseCount struct {
	Base  string `json:"base"`
	Count int    `json:"count"`
	// Station 本站格式的直链（{地址}/d/{pickcode}）：Base 就是 /d/ 之前那段
	Station bool `json:"station,omitempty"`
}

type strmPairCount struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

type strmRebaseSample struct {
	Rel string `json:"rel"`
	Old string `json:"old"`
	New string `json:"new"`
}

// strmRebaseReport 预演与执行共用的报告
type strmRebaseReport struct {
	Root       string `json:"root"`
	Scanned    int    `json:"scanned"`    // 扫到的 .strm
	Unreadable int    `json:"unreadable"` // 读不了 / 太大不像 STRM 的
	// Groups 按地址开头分组（多的在前），给用户挑「旧地址」用
	Groups []strmBaseCount `json:"groups"`
	// Pairs 每组替换命中几个
	Pairs   []strmPairCount    `json:"pairs"`
	Matched int                `json:"matched"`
	Samples []strmRebaseSample `json:"samples"`
	// Domain 已保存的直连域名（给界面预填「新地址」，没填过是空）
	Domain string `json:"domain"`
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

// strmAddrPrefix 一条 STRM 内容按什么分组：本站直链取 /d/ 之前；别的 http 链接取 scheme://主机；
// 本地路径取前两级目录（/mnt/115）。station 标出是不是本站格式
func strmAddrPrefix(line string) (prefix string, station bool) {
	if base, _, ok := splitStrmBase(line); ok {
		return strings.TrimRight(base, "/"), true
	}
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if u, err := url.Parse(line); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host, false
		}
		return "", false
	}
	if strings.HasPrefix(line, "/") {
		segs := strings.Split(strings.Trim(line, "/"), "/")
		if len(segs) > 2 {
			segs = segs[:2]
		} else if len(segs) > 1 {
			segs = segs[:1] // 只有一级目录 + 文件名：取那一级
		}
		return "/" + strings.Join(segs, "/"), false
	}
	return "", false
}

// validRebaseAddr 替换两侧只认 http(s) 地址（带主机）或绝对路径（至少一级目录）：
// 太短的开头（「http」「/」）几乎什么都命中，不让填
func validRebaseAddr(s string) bool {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(s)
		return err == nil && u.Host != ""
	}
	return strings.HasPrefix(s, "/") && strings.Trim(s, "/") != ""
}

// normRebasePairs 校验并归一（去空白与结尾斜杠）。新地址允许和别的组的旧地址相同：一条内容只换一次，不会接着换下去
func normRebasePairs(in []strmRebasePair) ([]strmRebasePair, error) {
	if len(in) > strmRebaseMaxPairs {
		return nil, fmt.Errorf("一次最多替换 %d 组", strmRebaseMaxPairs)
	}
	out := make([]strmRebasePair, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		from := strings.TrimRight(strings.TrimSpace(p.From), "/")
		to := strings.TrimRight(strings.TrimSpace(p.To), "/")
		if from == "" && to == "" {
			continue
		}
		if !validRebaseAddr(from) {
			return nil, fmt.Errorf("旧地址「%s」要写成 http(s)://主机[:端口][/路径] 或以 / 开头的路径", p.From)
		}
		if !validRebaseAddr(to) {
			return nil, fmt.Errorf("新地址「%s」要写成 http(s)://主机[:端口][/路径] 或以 / 开头的路径", p.To)
		}
		if strings.EqualFold(from, to) {
			return nil, fmt.Errorf("旧地址和新地址一样：%s", from)
		}
		k := strings.ToLower(from)
		if seen[k] {
			return nil, fmt.Errorf("旧地址重复：%s", from)
		}
		seen[k] = true
		out = append(out, strmRebasePair{From: from, To: to})
	}
	return out, nil
}

// rebaseLine 一行内容命中哪一组（旧地址最长的那组优先），返回替换后的内容与组下标
func rebaseLine(line string, pairs []strmRebasePair) (string, int, bool) {
	best := -1
	for i, p := range pairs {
		n := len(p.From)
		if len(line) < n || !strings.EqualFold(line[:n], p.From) {
			continue
		}
		if len(line) > n && !strings.ContainsRune("/?#", rune(line[n])) {
			continue // 旧地址只是一段更长的主机名 / 目录名的开头
		}
		if best < 0 || n > len(pairs[best].From) {
			best = i
		}
	}
	if best < 0 {
		return "", -1, false
	}
	return pairs[best].To + line[len(pairs[best].From):], best, true
}

// strmFirstLine 内容里第一条有效行（同 firstStrmLine 的认法）在 lines 里的下标与去空白后的值
func strmFirstLine(lines []string) (int, string) {
	for i, l := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(l, "\uFEFF"))
		if t != "" && !strings.HasPrefix(t, "#") {
			return i, t
		}
	}
	return -1, ""
}

// rebaseStrms 扫 root 下所有 .strm。pairs 为空 = 只分组统计；apply=false 只统计（预演），true 才写回
func rebaseStrms(root string, pairs []strmRebasePair, apply bool, progress func(done int)) (strmRebaseReport, error) {
	rep := strmRebaseReport{Root: filepath.ToSlash(root), Groups: []strmBaseCount{}, Pairs: []strmPairCount{}, Samples: []strmRebaseSample{}}
	for _, p := range pairs {
		rep.Pairs = append(rep.Pairs, strmPairCount{From: p.From, To: p.To})
	}
	type groupKey struct {
		key     string
		station bool
	}
	counts := map[groupKey]int{}
	shown := map[string]string{} // 分组 key（小写）→ 第一次见到的写法
	err := walkStrmFiles(root, func(p string, st os.FileInfo) {
		rel := localRelOrEmpty(root, p)
		rep.Scanned++
		if progress != nil && rep.Scanned%500 == 0 {
			progress(rep.Scanned)
		}
		if st.Size() > adoptStrmReadLimit {
			rep.Unreadable++ // 正常的 STRM 不过几百字节，这么大的不是我们要改的
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			rep.Unreadable++
			return
		}
		lines := strings.Split(string(b), "\n")
		idx, line := strmFirstLine(lines)
		if idx < 0 {
			return
		}
		if pre, station := strmAddrPrefix(line); pre != "" {
			k := strings.ToLower(pre)
			if _, ok := shown[k]; !ok {
				shown[k] = pre
			}
			counts[groupKey{k, station}]++
		}
		next, pi, ok := rebaseLine(line, pairs)
		if !ok {
			return
		}
		rep.Matched++
		rep.Pairs[pi].Count++
		if len(rep.Samples) < strmRebaseSamples {
			rep.Samples = append(rep.Samples, strmRebaseSample{Rel: rel, Old: line, New: next})
		}
		if !apply {
			return
		}
		// 只换这一行，别的行（个别播放列表式 STRM 的 #EXTM3U 等）原样留着
		lines[idx] = strings.Replace(lines[idx], line, next, 1)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o666); err != nil {
			log.Printf("[STRM] ✗ 替换地址失败 %s: %v", rel, err)
			rep.Failed++
			return
		}
		// 还原修改时间：Emby 按「加入时间」排序看的就是它（SmartStrm 同款）
		_ = os.Chtimes(p, st.ModTime(), st.ModTime())
		rep.Rewritten++
	})
	if err != nil {
		return rep, err
	}
	for k, n := range counts {
		rep.Groups = append(rep.Groups, strmBaseCount{Base: shown[k.key], Count: n, Station: k.station})
	}
	sort.Slice(rep.Groups, func(i, j int) bool {
		if rep.Groups[i].Count != rep.Groups[j].Count {
			return rep.Groups[i].Count > rep.Groups[j].Count
		}
		return rep.Groups[i].Base < rep.Groups[j].Base
	})
	if len(rep.Groups) > strmRebaseMaxGroups {
		rep.Groups = rep.Groups[:strmRebaseMaxGroups]
	}
	if rep.Scanned == 0 {
		// 挂载没就绪时整棵树都是空的，别报成「没有要改的」
		return rep, fmt.Errorf("%s 下一个 .strm 都没找到（挂载未就绪？）", root)
	}
	return rep, nil
}

// strmExplicitDomain 用户在配置里**明确填过**的直连域名，没填返回空。
// 没填时 parseStrmConfig 兜底成 Docker 回环 —— 那正是要改掉的地址，不能拿它当新地址，接管也不能拿它写直链
func (h *Handler) strmExplicitDomain() string {
	var cfg struct {
		Domain string `json:"domain"`
	}
	_ = json.Unmarshal([]byte(h.getSettingValue("strm")), &cfg)
	d := strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	if _, _, ok := splitStrmBase(d + "/d/x"); !ok {
		return ""
	}
	return d
}

func strmRebaseRoot() (string, error) {
	root := localMediaRoot()
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", fmt.Errorf("本地媒体库目录不可访问：%s", root)
	}
	return root, nil
}

// StrmRebasePreview POST /sync/strm-rebase/preview  body: {"pairs":[{"from","to"}]}：只读，读本地文件、零 115 请求。
// pairs 为空时只给分组（打开页面时用）
func (h *Handler) StrmRebasePreview(c *gin.Context) {
	var req struct {
		Pairs []strmRebasePair `json:"pairs"`
	}
	_ = c.ShouldBindJSON(&req)
	pairs, err := normRebasePairs(req.Pairs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	root, err := strmRebaseRoot()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rep, err := rebaseStrms(root, pairs, false, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rep.Domain = h.strmExplicitDomain()
	c.JSON(http.StatusOK, rep)
}

// StrmRebase POST /sync/strm-rebase  body: {"pairs":[{"from":"http://172.17.0.1:6086","to":"http://nas:6086"}]} → 入队，202
func (h *Handler) StrmRebase(c *gin.Context) {
	var req struct {
		Pairs []strmRebasePair `json:"pairs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	pairs, err := normRebasePairs(req.Pairs)
	if err == nil && len(pairs) == 0 {
		err = errors.New("至少填一组旧地址 → 新地址")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	title := "替换 STRM 地址"
	if len(pairs) == 1 {
		title += "：" + pairs[0].From + " → " + pairs[0].To
	}
	job, err := enqueueJob(h.DB, jobSpec{Kind: "strmrebase", Title: title,
		DedupeKey: "strmrebase", Source: "web", Priority: jobPriorityManual, Params: jobParams{RebasePairs: pairs}})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "替换")
}

func init() {
	jobExecutors["strmrebase"] = execStrmRebaseJob
}

// execStrmRebaseJob 在主队列里跑（拿着 taskMu）：增量 / 全量也写这些 STRM，不能同时动
func execStrmRebaseJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	params := decodeJobParams(job)
	pairs := params.RebasePairs
	if len(pairs) == 0 && len(params.RebaseFrom) > 0 {
		// 升级前排进队列的老任务：旧地址 → 已保存的直连域名
		target := h.strmExplicitDomain()
		if target == "" {
			return jobOutcome{}, errors.New("还没填「STRM 直连域名」")
		}
		for _, f := range params.RebaseFrom {
			pairs = append(pairs, strmRebasePair{From: f, To: target})
		}
	}
	pairs, err := normRebasePairs(pairs)
	if err != nil {
		return jobOutcome{}, err
	}
	if len(pairs) == 0 {
		return jobOutcome{}, errors.New("没有要替换的地址")
	}
	root, err := strmRebaseRoot()
	if err != nil {
		return jobOutcome{}, err
	}
	rep, err := rebaseStrms(root, pairs, true, func(done int) {
		setJobProgress("替换 STRM 地址", done, progressKeep, "")
	})
	if err != nil {
		return jobOutcome{}, err
	}
	desc := make([]string, 0, len(rep.Pairs))
	for _, p := range rep.Pairs {
		desc = append(desc, fmt.Sprintf("%s → %s %d 个", p.From, p.To, p.Count))
	}
	log.Printf("[STRM] ✓ 替换地址（%s）：改写 %d 个、失败 %d 个，共扫 %d 个 STRM",
		strings.Join(desc, "；"), rep.Rewritten, rep.Failed, rep.Scanned)
	out := jobOutcome{Result: rep}
	switch {
	case rep.Rewritten == 0 && rep.Failed == 0:
		out.Message = fmt.Sprintf("扫了 %d 个 STRM，没有以这些旧地址开头的", rep.Scanned)
	case rep.Failed > 0:
		out.Partial = true
		out.Message = fmt.Sprintf("已替换 %d 个 STRM 的地址，%d 个没改成（看日志 [STRM]）", rep.Rewritten, rep.Failed)
	default:
		out.Message = fmt.Sprintf("已替换 %d 个 STRM 的地址", rep.Rewritten)
	}
	return out, nil
}
