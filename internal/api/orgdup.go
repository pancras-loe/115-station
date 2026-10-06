package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 同一次整理里撞名：交给用户选 ====================
//
// 2026-10-06 现场：转存进来的《越狱》里第 4 季第 22 集有两份——
// 「越狱.Prison.Break.S04E22.粤语.2008….mkv」和「….英语.2008….mkv」。命名模板不带音轨语言，
// 两份改出同一个名字，而 115 遇到同名会把后到的悄悄改成「xxx(1).mkv」：
// 整理记录、台账、STRM 记的是我们算的名字，和网盘对不上；哪份成了 (1) 取决于先后，用户分不出粤语英语。
//
// 两条原则（维护者定的）：
//   - 不替用户去重。洗版规则多半管不到语言，留粤语还是英语只有用户知道；
//   - 不让 115 替我们处理重名。撞名一律在改名 / 搬移之前拦下。
//
// 流程：洗版逐份判定之后（判输的照旧进「已存在」），对还要入库的视频按「落点目录 / 新名」分组：
//   - 同组 sha1 相同 = 真是同一份文件，不问，多余的移冗余；
//   - 剩下两份以上 = 停下来：这几份连同它们的字幕 / 集 NFO 原地不动，登记一条 HoldDup 的待确认记录，
//     同一条目里其他的集照常入库，源目录不收拾（里面还有等着选的文件）；
//   - 用户选完（DupChoice：fid → keep / drop / A..Z）走「确认入库」的执行器，processDir 按选择放行：
//     不要的移冗余，都留的按字母加后缀「xxx#A.mkv」「xxx#B.mkv」。
// 洗版策略是「新的替换旧的」时，问的就是用哪份去替换：只有保留下来的那份（几份）去让旧版让位。

// reVariantTail 文件基名末尾的后缀字母（「xxx#A」）
var reVariantTail = regexp.MustCompile(`#[A-Z]$`)

// withVariant 给文件名加后缀字母：「xxx.mkv」+ A →「xxx#A.mkv」
func withVariant(name, v string) string {
	if v == "" {
		return name
	}
	ext := pathExt(name)
	return strings.TrimSuffix(name, ext) + "#" + v + ext
}

// stripVariant 去掉基名末尾的后缀字母（解析文件名时用：它不是片名，也不是画质串的一部分）
func stripVariant(base string) string { return reVariantTail.ReplaceAllString(base, "") }

// dupFile 撞名组里的一份
type dupFile struct {
	Fid  string `json:"fid"`
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	// Label 和同组其他几份不一样的那几个词（粤语 / 英语），按钮与列表上的主标签；认不出为空
	Label string `json:"label,omitempty"`
	// Rank 命中洗版策略优先级的第几条（0 最优），没命中或没配策略为 -1
	Rank int `json:"rank"`
}

// dupGroup 一组撞名的文件
type dupGroup struct {
	Target  string    `json:"target"`            // 撞上的落点：库内相对目录 / 新文件名
	Episode string    `json:"episode,omitempty"` // S04E22，电影为空
	Files   []dupFile `json:"files"`
	// Recommend 洗版策略分得出高下时最优那份的 fid；分不出（多半是只差语言）为空，不替用户挑
	Recommend string `json:"recommend,omitempty"`
	Reason    string `json:"reason,omitempty"` // 推荐的依据
}

// dupOutcome 撞名处理的结果
type dupOutcome struct {
	keep     []remoteFile      // 照常入库（含已经选过、要加后缀字母的）
	drop     []remoteFile      // 移冗余：sha1 相同的多余副本 + 用户不要的
	held     []dupGroup        // 停下来等用户选的组
	heldFids map[string]bool   // 停下的视频
	variants map[string]string // fid → 后缀字母
	copies   int               // sha1 相同自动去掉的份数（日志用）
}

func (o dupOutcome) changed() bool { return len(o.drop) > 0 || len(o.held) > 0 }

// dupChoiceDrop / dupChoiceKeep DupChoice 里的两种取值，其余是后缀字母 A..Z
const (
	dupChoiceDrop = "drop"
	dupChoiceKeep = "keep"
)

// settleCollisions 纯计算：videos 按 targetOf 分组，处理撞名。
// targetOf 返回 落点 与 集号标签；rankOf 给文件名按洗版策略排名（-1 = 没命中）；
// choice 是用户的选择（没选过为 nil），只有覆盖了一组里的每一份才算选过 —— 后来又多出一份时照样再问
func settleCollisions(videos []remoteFile, targetOf func(remoteFile) (string, string),
	rankOf func(string) int, choice map[string]string) dupOutcome {
	out := dupOutcome{heldFids: map[string]bool{}, variants: map[string]string{}}
	byTarget := map[string][]remoteFile{}
	episodeOf := map[string]string{}
	var order []string
	for _, v := range videos {
		t, ep := targetOf(v)
		if _, ok := byTarget[t]; !ok {
			order = append(order, t)
			episodeOf[t] = ep
		}
		byTarget[t] = append(byTarget[t], v)
	}
	for _, t := range order {
		group := byTarget[t]
		if len(group) == 1 {
			out.keep = append(out.keep, group[0])
			continue
		}
		// 同一份文件（sha1 相同）：不问，留第一份
		var distinct []remoteFile
		seen := map[string]bool{}
		for _, v := range group {
			if v.Sha1 != "" && seen[strings.ToUpper(v.Sha1)] {
				out.drop = append(out.drop, v)
				out.copies++
				continue
			}
			seen[strings.ToUpper(v.Sha1)] = true
			distinct = append(distinct, v)
		}
		if len(distinct) == 1 {
			out.keep = append(out.keep, distinct[0])
			continue
		}
		g := buildDupGroup(t, episodeOf[t], distinct, rankOf)
		if !choiceCovers(choice, distinct) {
			out.held = append(out.held, g)
			for _, v := range distinct {
				out.heldFids[v.Fid] = true
			}
			continue
		}
		var kept []remoteFile
		for _, v := range distinct {
			if choice[v.Fid] == dupChoiceDrop {
				out.drop = append(out.drop, v)
			} else {
				kept = append(kept, v)
			}
		}
		out.keep = append(out.keep, kept...)
		if len(kept) > 1 {
			for fid, l := range assignVariants(g, kept, choice) {
				out.variants[fid] = l
			}
		}
	}
	return out
}

// choiceCovers 用户的选择是不是覆盖了这一组的每一份
func choiceCovers(choice map[string]string, group []remoteFile) bool {
	if len(choice) == 0 {
		return false
	}
	for _, v := range group {
		if _, ok := choice[v.Fid]; !ok {
			return false
		}
	}
	return true
}

// assignVariants 几份都保留时的后缀字母。用户提交时已经定好字母的（choice 里是 A..Z 且互不相同）照用，
// 否则按 推荐的那份 → 体积从大到小 → 名字 依次给 A、B、C
func assignVariants(g dupGroup, kept []remoteFile, choice map[string]string) map[string]string {
	out := map[string]string{}
	used := map[string]bool{}
	ok := true
	for _, v := range kept {
		l := choice[v.Fid]
		if len(l) != 1 || l[0] < 'A' || l[0] > 'Z' || used[l] {
			ok = false
			break
		}
		used[l] = true
		out[v.Fid] = l
	}
	if ok {
		return out
	}
	out = map[string]string{}
	fids := make([]string, 0, len(kept))
	for _, v := range kept {
		fids = append(fids, v.Fid)
	}
	for i, fid := range variantOrder(g, fids) {
		if i >= 26 {
			break
		}
		out[fid] = string(rune('A' + i))
	}
	return out
}

// variantOrder 一组里要保留的几份排字母的顺序：推荐的那份最前，其余按体积从大到小
func variantOrder(g dupGroup, fids []string) []string {
	info := map[string]dupFile{}
	for _, f := range g.Files {
		info[f.Fid] = f
	}
	out := append([]string(nil), fids...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := info[out[i]], info[out[j]]
		if (a.Fid == g.Recommend) != (b.Fid == g.Recommend) {
			return a.Fid == g.Recommend
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		return a.Name < b.Name
	})
	return out
}

// buildDupGroup 组装一组撞名文件给用户看：区别词、洗版排名、推荐
func buildDupGroup(target, episode string, files []remoteFile, rankOf func(string) int) dupGroup {
	g := dupGroup{Target: target, Episode: episode}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	labels := dupLabels(names)
	best, bestN := -1, 0
	for i, f := range files {
		r := -1
		if rankOf != nil {
			r = rankOf(f.Name)
		}
		g.Files = append(g.Files, dupFile{Fid: f.Fid, Name: f.Name, Size: f.Size, Label: labels[i], Rank: r})
		switch {
		case r < 0:
		case best < 0 || r < best:
			best, bestN, g.Recommend = r, 1, f.Fid
		case r == best:
			bestN++
		}
	}
	// 只有一份排在最前才推荐：并列第一或都没命中，说明洗版规则分不出这几份（多半只差语言），不替用户挑
	if best < 0 || bestN > 1 {
		g.Recommend = ""
	} else {
		g.Reason = fmt.Sprintf("洗版策略第 %d 条优先级规则", best+1)
	}
	return g
}

// reDupSplit 切文件名的分隔符
var reDupSplit = regexp.MustCompile(`[\s._\-\[\]()【】（）+]+`)

// dupLabels 每个名字里和其他几个不一样的词（最多 3 个，按出现顺序）。
// 「越狱.Prison.Break.S04E22.粤语.2008…」与「….英语.2008…」→「粤语」「英语」
func dupLabels(names []string) []string {
	toks := make([][]string, len(names))
	count := map[string]int{} // 词 → 出现在几个名字里
	for i, n := range names {
		seen := map[string]bool{}
		for _, t := range reDupSplit.Split(baseName(n), -1) {
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			toks[i] = append(toks[i], t)
			count[strings.ToLower(t)]++
		}
	}
	out := make([]string, len(names))
	for i := range names {
		var uniq []string
		for _, t := range toks[i] {
			if count[strings.ToLower(t)] < len(names) {
				uniq = append(uniq, t)
				if len(uniq) == 3 {
					break
				}
			}
		}
		out[i] = strings.Join(uniq, " ")
	}
	return out
}

// dupCompanions files 里跟着 videos 命名的附属文件（字幕、集 NFO、剧照）：视频停下 / 移走时它们一起
func dupCompanions(videos []remoteFile, files []remoteFile) []remoteFile {
	if len(videos) == 0 {
		return nil
	}
	isVideo := map[string]bool{}
	bases := make([]string, len(videos))
	for i, v := range videos {
		bases[i] = baseName(v.Name)
		isVideo[v.Fid] = true
	}
	var out []remoteFile
	for _, f := range files {
		if isVideo[f.Fid] || classifyFile(f.Name) == FileTypeVideo {
			continue
		}
		if assetOwner(baseName(f.Name), bases) >= 0 {
			out = append(out, f)
		}
	}
	return out
}

// washRankIn 文件名命中策略优先级的第几条（0 最优），没命中或没有策略 -1
func washRankIn(st *washStrategy, name string) int {
	if st == nil {
		return -1
	}
	for i, r := range st.PriorityLevel {
		if ruleMatch(name, r) {
			return i
		}
	}
	return -1
}

// dupHoldMessage 待确认记录上给用户看的一句话
func dupHoldMessage(groups []dupGroup) string {
	g := groups[0]
	var labels []string
	for _, f := range g.Files {
		if f.Label != "" {
			labels = append(labels, f.Label)
		}
	}
	what := g.Episode
	if what == "" {
		what = pathBase(g.Target)
	}
	msg := fmt.Sprintf("%s 有 %d 份不同的文件，改名后会重名", what, len(g.Files))
	if len(labels) == len(g.Files) {
		msg += "（" + strings.Join(labels, " / ") + "）"
	}
	if len(groups) > 1 {
		msg += fmt.Sprintf("；共 %d 组", len(groups))
	}
	return msg + "，请选择保留哪份"
}

// ---- 用户的选择 ----

// dupAction 一组的选择。Action：keep（只留 Fid 那份）/ keep_all（都留，加 #A #B）/ drop_all（都不要）
type dupAction struct {
	Action string `json:"action"`
	Fid    string `json:"fid,omitempty"`
}

// buildDupChoice 把各组的选择展开成 fid → keep / drop / 字母。actions 按组的下标对齐
func buildDupChoice(groups []dupGroup, actions []dupAction) (map[string]string, error) {
	if len(actions) != len(groups) {
		return nil, fmt.Errorf("需要为 %d 组各选一项，收到 %d 项", len(groups), len(actions))
	}
	out := map[string]string{}
	for i, g := range groups {
		a := actions[i]
		switch a.Action {
		case "keep":
			found := false
			for _, f := range g.Files {
				if f.Fid == a.Fid {
					found = true
					out[f.Fid] = dupChoiceKeep
				} else {
					out[f.Fid] = dupChoiceDrop
				}
			}
			if !found {
				return nil, fmt.Errorf("第 %d 组里没有这个文件", i+1)
			}
		case "keep_all":
			fids := make([]string, 0, len(g.Files))
			for _, f := range g.Files {
				fids = append(fids, f.Fid)
			}
			for j, fid := range variantOrder(g, fids) {
				if j >= 26 {
					return nil, fmt.Errorf("第 %d 组超过 26 份，没法用字母区分", i+1)
				}
				out[fid] = string(rune('A' + j))
			}
		case "drop_all":
			for _, f := range g.Files {
				out[f.Fid] = dupChoiceDrop
			}
		default:
			return nil, fmt.Errorf("第 %d 组的选择不认识：%q", i+1, a.Action)
		}
	}
	return out, nil
}

func parseDupGroups(s string) []dupGroup {
	var out []dupGroup
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func parseDupChoice(s string) map[string]string {
	var out map[string]string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// submitDupChoice 记下选择并入「确认入库」队列。网页、机器人、超时共用这一个入口。
// override：网页可以在排队期间改选（以最后一次为准，执行时才读记录上的选择）；
// 机器人与超时传 false，只认还没选过的 —— 谁先选算谁的，后到的不能把人刚选的盖掉
func (h *Handler) submitDupChoice(rec *model.OrganizeRecord, actions []dupAction, source string, override bool) (model.TaskJob, error) {
	if !rec.HoldDup {
		return model.TaskJob{}, errors.New("这条记录不在「同集多份待选」状态（可能已经处理过）")
	}
	choice, err := buildDupChoice(parseDupGroups(rec.DupGroups), actions)
	if err != nil {
		return model.TaskJob{}, err
	}
	b, _ := json.Marshal(choice)
	q := h.DB.Model(&model.OrganizeRecord{}).Where("id = ? AND status = ? AND hold_dup = ?", rec.ID, rec.Status, true)
	if !override {
		q = q.Where("dup_choice = '' OR dup_choice IS NULL")
	}
	res := q.Update("dup_choice", string(b))
	if res.Error != nil {
		return model.TaskJob{}, res.Error
	}
	if res.RowsAffected == 0 {
		if !override {
			return model.TaskJob{}, errors.New("这一条刚被别处选过了")
		}
		return model.TaskJob{}, errors.New("这条记录刚被处理过，请刷新")
	}
	rec.DupChoice = string(b)
	var job model.TaskJob
	if rec.Status == orgStatusAwaiting {
		job, err = h.enqueueConfirm(rec, pickReq{})
	} else {
		// 重新整理撞名停下的：按停下时那次要整理成的条目（暂存指定）再来一次，这回带着选择
		pick := pickReq{TmdbID: rec.PendingTmdbID, MediaType: rec.PendingMediaType, Label: rec.PendingLabel}
		if pick.TmdbID <= 0 {
			pick = pickReq{TmdbID: rec.TmdbID, MediaType: rec.MediaType}
		}
		job, err = h.enqueueRedo(rec, pick)
	}
	if err == nil && source != "" && source != "web" {
		h.DB.Model(&model.TaskJob{}).Where("id = ?", job.ID).Update("source", source)
	}
	return job, err
}

// SubmitDupChoice POST /organize/records/:id/dup  body: {"actions":[{"action":"keep","fid":"…"},{"action":"keep_all"}]}
func (h *Handler) SubmitDupChoice(c *gin.Context) {
	var req struct {
		Actions []dupAction `json:"actions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式不对"})
		return
	}
	var rec model.OrganizeRecord
	if h.DB.First(&rec, c.Param("id")).Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	job, err := h.submitDupChoice(&rec, req.Actions, "web", true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "同集多份")
}
