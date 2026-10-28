package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 演职人员：合并重复人物 ====================
//
// 同一个 TMDB 人物在 Emby 里常有两个条目：老片子挂着英文名的「Tom Hanks」，新刮的 NFO 写了中文名，
// Emby 按名字认人，又建了一个「汤姆·汉克斯」。两个都带 Tmdb=31，人物页的作品各算各的；
// 补全想把英文名那个改成中文名也改不了（同名会让 Emby 认人时乱，embyPersonNameTaken）。
//
// 思路参考神医助手 RefreshPersonTask（只读参考，未复制代码）：按 TMDB 编号找出重复的人物条目。
// 但它是把重复的**全部删掉**，等 Emby 下次刷新片目时按名字重建 —— Emby 连不上 TMDB 时
// 重建出来的人物没有头像、没有简介，片目在刷新之前还会少掉这些演员。这里改成只留一个、不靠重建：
//  1. 留哪个：中文名 > 有头像 > 中文简介 > 先建的（keeperScore）；
//  2. 名字核对：组里的名字（归一后）都得是这个 TMDB 人物的原名或中文名，或者全组同一个名字之外
//     再没别的 —— 对不上说明有条目的编号是认错的，整组不动；
//  3. 改关联：列出挂着重复条目的片目（电影 / 剧 / 季 / 集），读完整 DTO，把演职人员表里那一项换成留下的人物，
//     整条写回（与 Emby 网页「编辑元数据」同一种写法）；
//  4. 核实：再查一次，重复条目确实没有任何片目挂着了、留下的人物确实挂上了，才删重复条目。
//     删人物条目只删它在 Emby 元数据目录里的那个文件夹（头像），不碰媒体文件；
//     但那个文件夹按名字建，**同名的两个人物共用一个**，删一个会把另一个的头像也删掉 ——
//     所以同名的重复不合并，路径与组里或库里其他人物重叠的也不删。
//
// 任何一步不对都停在那一步：片目写过的不回滚（换成了同一个人的另一个条目，内容是对的），
// 重复条目留着，下次再来。默认关，只在全库扫描（定时 / 立即运行）开头跑，刮削后补全不跑。
// 零 115 请求；TMDB 只在名字核对时读人物缓存（过期才问）。

const (
	personMergeMaxGroups = 50  // 一次任务最多合并几组
	personMergeMaxWrites = 500 // 一次任务最多改写几个片目（一个人物挂在几百集上时，分几次做完）
	personMergePreview   = 200 // 预览最多列几组
)

// embyPersonBrief 人物列表里合并要用的字段
type embyPersonBrief struct {
	ID          string            `json:"Id"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"`
	Path        string            `json:"Path"`
	Overview    string            `json:"Overview"`
	DateCreated string            `json:"DateCreated"`
	ProviderIds map[string]string `json:"ProviderIds"`
	ImageTags   map[string]string `json:"ImageTags"`
}

func (p embyPersonBrief) tmdbID() int {
	n, _ := strconv.Atoi(providerID(p.ProviderIds, "Tmdb"))
	return n
}

func (p embyPersonBrief) hasImage() bool { return p.ImageTags["Primary"] != "" }

// embyAllPersons 分页列出 Emby 里全部人物（Emby 本地查询）
func embyAllPersons(base, key string) ([]embyPersonBrief, error) {
	var out []embyPersonBrief
	for start := 0; ; start += 1000 {
		q := url.Values{
			"Fields": {"ProviderIds,Overview,DateCreated,Path"}, "StartIndex": {strconv.Itoa(start)}, "Limit": {"1000"},
			"EnableUserData": {"false"}, "EnableImageTypes": {"Primary"}, "ImageTypeLimit": {"1"},
			"SortBy": {"SortName"}, "SortOrder": {"Ascending"},
		}
		var page struct {
			Items []embyPersonBrief `json:"Items"`
			Total int               `json:"TotalRecordCount"`
		}
		if err := embyGetJSON(base, key, "/Persons", q, &page); err != nil {
			return out, err
		}
		out = append(out, page.Items...)
		if len(page.Items) == 0 || start+len(page.Items) >= page.Total {
			return out, nil
		}
	}
}

// personDupGroup 同一个 TMDB 人物的几个条目
type personDupGroup struct {
	TmdbID int               `json:"tmdb_id"`
	Keep   embyPersonBrief   `json:"keep"`
	Drop   []embyPersonBrief `json:"drop"`
	// SameName 组里有和留下的那个同名的：Emby 按名字认人，改关联换不过去，元数据目录也是同一个，不合并
	SameName bool `json:"same_name,omitempty"`
}

// keeperScore 留哪个：中文名 > 有头像 > 中文简介。同分的看谁先建
func keeperScore(p embyPersonBrief) int {
	s := 0
	if chineseNameOK(p.Name) {
		s += 4
	}
	if p.hasImage() {
		s += 2
	}
	if hasHan(p.Overview) {
		s++
	}
	return s
}

// findDupPersons 纯函数：按 TMDB 编号分组，挑出留下的那个。组按 TMDB 编号排
func findDupPersons(all []embyPersonBrief) []personDupGroup {
	by := map[int][]embyPersonBrief{}
	for _, p := range all {
		if id := p.tmdbID(); id > 0 && p.ID != "" && (p.Type == "" || p.Type == "Person") {
			by[id] = append(by[id], p)
		}
	}
	var out []personDupGroup
	for id, ps := range by {
		if len(ps) < 2 {
			continue
		}
		sort.SliceStable(ps, func(i, j int) bool {
			a, b := keeperScore(ps[i]), keeperScore(ps[j])
			if a != b {
				return a > b
			}
			if ps[i].DateCreated != ps[j].DateCreated {
				return ps[i].DateCreated < ps[j].DateCreated
			}
			return ps[i].ID < ps[j].ID
		})
		g := personDupGroup{TmdbID: id, Keep: ps[0], Drop: ps[1:]}
		for _, d := range g.Drop {
			if personNameKey(d.Name) == personNameKey(g.Keep.Name) {
				g.SameName = true
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TmdbID < out[j].TmdbID })
	return out
}

// dupNamesOK 纯函数：组里每个名字都得是这个 TMDB 人物的原名或中文名（归一后比）。
// 对不上的返回那个名字：多半是有条目的 TMDB 编号认错了，合并就是把两个人并成一个
func dupNamesOK(g personDupGroup, tmdbNames ...string) (bad string, ok bool) {
	known := map[string]bool{}
	for _, n := range tmdbNames {
		if k := personNameKey(n); k != "" {
			known[k] = true
		}
	}
	for _, p := range append([]embyPersonBrief{g.Keep}, g.Drop...) {
		if !known[personNameKey(p.Name)] {
			return p.Name, false
		}
	}
	return "", true
}

// relinkPeople 纯函数：片目的演职人员表里把 dropID 那几项换成留下的人物。
// 留下的人物已经以同一种身份（演员 / 导演 …）在表里了就直接去掉重复的那项，不然一部片里同一个人列两次。
// changed 为假表示表里根本没有 dropID（Emby 的查询和条目对不上），调用方不要写回
func relinkPeople(people []any, dropID string, keep embyPersonBrief) (out []any, changed bool) {
	has := map[string]bool{} // 留下的人物已有的身份
	for _, v := range people {
		if m, ok := v.(map[string]any); ok && fmt.Sprint(m["Id"]) == keep.ID {
			has[fmt.Sprint(m["Type"])] = true
		}
	}
	for _, v := range people {
		m, ok := v.(map[string]any)
		if !ok || fmt.Sprint(m["Id"]) != dropID {
			out = append(out, v)
			continue
		}
		changed = true
		typ := fmt.Sprint(m["Type"])
		if has[typ] {
			continue
		}
		has[typ] = true
		n := map[string]any{}
		for k, val := range m {
			n[k] = val
		}
		n["Id"], n["Name"] = keep.ID, keep.Name
		delete(n, "PrimaryImageTag") // 那是重复条目的头像
		out = append(out, n)
	}
	return out, changed
}

// personPathSafe 纯函数：重复条目的元数据目录能不能跟着删。
// 要求路径看得出是人物目录、不与留下的或库里其他人物的目录重叠（同名人物共用一个目录）
func personPathSafe(drop embyPersonBrief, all []embyPersonBrief) bool {
	norm := func(s string) string {
		return strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/"), "/")
	}
	p := norm(drop.Path)
	if p == "" || !strings.Contains(strings.ToLower(p), "/people/") || path.Base(p) == "people" {
		return false
	}
	for _, o := range all {
		if o.ID == drop.ID {
			continue
		}
		q := norm(o.Path)
		if q == "" {
			continue
		}
		if strings.EqualFold(q, p) || strings.HasPrefix(strings.ToLower(q), strings.ToLower(p)+"/") ||
			strings.HasPrefix(strings.ToLower(p), strings.ToLower(q)+"/") {
			return false
		}
	}
	return true
}

// ---- Emby 读写 ----

// embyAdminUser 读片目完整 DTO 要一个用户：取第一个没禁用的管理员
func embyAdminUser(base, key string) (string, error) {
	type embyUser struct {
		ID     string `json:"Id"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
			IsDisabled      bool `json:"IsDisabled"`
		} `json:"Policy"`
	}
	var page struct{ Items []embyUser }
	var err error
	if page.Items, err = embyUserList[embyUser](base, key, url.Values{"IsDisabled": {"false"}}); err != nil {
		return "", err
	}
	for _, u := range page.Items {
		if u.Policy.IsAdministrator && !u.Policy.IsDisabled && u.ID != "" {
			return u.ID, nil
		}
	}
	return "", errors.New("Emby 里没有可用的管理员用户")
}

// embyItemsWithPerson 挂着这个人物的片目 id（不限类型：电影、剧、季、集都可能）
func embyItemsWithPerson(base, key, personID string) ([]string, error) {
	var ids []string
	for start := 0; ; start += 500 {
		q := url.Values{"PersonIds": {personID}, "Recursive": {"true"}, "StartIndex": {strconv.Itoa(start)},
			"Limit": {"500"}, "EnableUserData": {"false"}, "EnableImages": {"false"}}
		var page struct {
			Items []struct {
				ID string `json:"Id"`
			} `json:"Items"`
			Total int `json:"TotalRecordCount"`
		}
		if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
			return ids, err
		}
		for _, it := range page.Items {
			ids = append(ids, it.ID)
		}
		if len(page.Items) == 0 || start+len(page.Items) >= page.Total {
			return ids, nil
		}
	}
}

// embyFullItem 读片目的完整 DTO。按用户读单个条目才返回全部字段（列表查询只给 Fields 点名的，
// 写回时缺的字段会被清空）。这里读的是电影 / 剧集 / 集，不是人物，不会触发人物的按需刷新（见 embyGetPerson）
func embyFullItem(base, key, uid, id string) (embyPersonItem, error) {
	var it embyPersonItem
	if err := embyGetJSON(base, key, "/Users/"+url.PathEscape(uid)+"/Items/"+url.PathEscape(id), nil, &it); err != nil {
		return nil, err
	}
	if it.str("Id") != id {
		return nil, fmt.Errorf("Emby 返回的条目不是 %s", id)
	}
	return it, nil
}

func embyDeletePerson(base, key, id string) error {
	resp, err := embyRequest(http.MethodDelete, base, key, "/Items/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return fmt.Errorf("连接 Emby 失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("删除人物条目失败：HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- 执行 ----

// personMergeStat 一次任务里合并的结果
type personMergeStat struct {
	Groups   int      // 找到的重复组
	Merged   int      // 删掉的重复条目
	Relinked int      // 改写的片目
	Problems []string // 没合并的原因
	capped   bool
}

func (s *personMergeStat) problem(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	s.Problems = append(s.Problems, msg)
	log.Printf("[演职人员] ○ 合并重复人物：%s", msg)
}

// mergeDuplicates 全库扫描开头跑一次。出错只记问题，不影响后面的补全
func (r *personRunner) mergeDuplicates() personMergeStat {
	var st personMergeStat
	all, err := embyAllPersons(r.base, r.key)
	if err != nil {
		st.problem("读取 Emby 人物列表失败: %v", err)
		return st
	}
	groups := findDupPersons(all)
	st.Groups = len(groups)
	if len(groups) == 0 {
		return st
	}
	uid, err := embyAdminUser(r.base, r.key)
	if err != nil {
		st.problem("%v，跳过合并", err)
		return st
	}
	log.Printf("[演职人员] ▶ 合并重复人物：%d 组同一个 TMDB 人物有多个条目（本次最多处理 %d 组）", len(groups), personMergeMaxGroups)
	writes, done := 0, 0
	for _, g := range groups {
		if personLane.stopRequested() || stopRequestedGlobal() {
			break
		}
		if done >= personMergeMaxGroups || writes >= personMergeMaxWrites {
			st.capped = true
			break
		}
		if g.SameName {
			st.problem("%s（TMDB %d）：重复条目同名，Emby 按名字认人，合并不了", g.Keep.Name, g.TmdbID)
			continue
		}
		meta, err := loadPersonMeta(r.tc, g.TmdbID, r.pace)
		if err != nil {
			st.problem("%s（TMDB %d）：读 TMDB 人物失败，本次不合并: %v", g.Keep.Name, g.TmdbID, err)
			continue
		}
		if bad, ok := dupNamesOK(g, meta.Name, meta.ZhName); !ok {
			st.problem("%s（TMDB %d）：「%s」不是这个 TMDB 人物的名字，可能编号认错了，不合并", g.Keep.Name, g.TmdbID, bad)
			continue
		}
		done++
		personLane.setSub("合并重复人物", done, len(groups), g.Keep.Name)
		for _, d := range g.Drop {
			n, err := r.mergeOne(uid, g.Keep, d, all, personMergeMaxWrites-writes)
			writes += n
			st.Relinked += n
			if err != nil {
				st.problem("%s → %s（TMDB %d）：%v", d.Name, g.Keep.Name, g.TmdbID, err)
				continue
			}
			st.Merged++
			r.done[d.ID] = true // 已删，后面的补全别再碰它
			log.Printf("[演职人员] ✓ 合并重复人物：%s → %s（TMDB %d，改写 %d 个片目）", d.Name, g.Keep.Name, g.TmdbID, n)
		}
	}
	return st
}

// errMergeBudget 本次的片目改写名额用完了，重复条目留到下次
var errMergeBudget = errors.New("本次改写片目的名额用完，剩下的下次接着合并")

// mergeOne 把一个重复条目的片目关联换到留下的人物上，核实后删掉它。返回改写了几个片目
func (r *personRunner) mergeOne(uid string, keep, drop embyPersonBrief, all []embyPersonBrief, budget int) (int, error) {
	ids, err := embyItemsWithPerson(r.base, r.key, drop.ID)
	if err != nil {
		return 0, fmt.Errorf("查片目失败: %v", err)
	}
	writes := 0
	for _, id := range ids {
		if personLane.stopRequested() || stopRequestedGlobal() {
			return writes, errors.New("任务停止，重复条目留到下次")
		}
		if writes >= budget {
			return writes, errMergeBudget
		}
		it, err := embyFullItem(r.base, r.key, uid, id)
		if err != nil {
			return writes, fmt.Errorf("读片目 %s 失败: %v", id, err)
		}
		people, _ := it["People"].([]any)
		np, changed := relinkPeople(people, drop.ID, keep)
		if !changed {
			return writes, fmt.Errorf("《%s》的演职人员表里没有这个条目（Emby 查询与条目对不上），不改", it.str("Name"))
		}
		if np == nil {
			np = []any{}
		}
		it["People"] = np
		if err := embyUpdateItem(r.base, r.key, id, it); err != nil {
			return writes, fmt.Errorf("改写《%s》失败: %v", it.str("Name"), err)
		}
		writes++
		time.Sleep(personMergeWriteGap)
	}

	// 核实：重复条目没有任何片目挂着了，留下的人物都挂上了
	left, err := embyItemsWithPerson(r.base, r.key, drop.ID)
	if err != nil {
		return writes, fmt.Errorf("核实失败: %v", err)
	}
	if len(left) > 0 {
		return writes, fmt.Errorf("改写后还有 %d 个片目挂着它（Emby 没换过去），不删", len(left))
	}
	if len(ids) > 0 {
		kept, err := embyItemsWithPerson(r.base, r.key, keep.ID)
		if err != nil {
			return writes, fmt.Errorf("核实失败: %v", err)
		}
		has := map[string]bool{}
		for _, id := range kept {
			has[id] = true
		}
		for _, id := range ids {
			if !has[id] {
				return writes, fmt.Errorf("片目 %s 改写后没挂上留下的人物，不删", id)
			}
		}
	}

	// 删之前再读一次：还是人物条目、还是同一个 TMDB 编号，目录不与别人重叠
	cur, err := embyGetPerson(r.base, r.key, drop.ID)
	if err != nil {
		return writes, fmt.Errorf("删除前复查失败: %v", err)
	}
	if t := cur.str("Type"); t != "" && t != "Person" {
		return writes, fmt.Errorf("条目类型是 %s，不是人物，不删", t)
	}
	if providerID(cur.providerIDs(), "Tmdb") != strconv.Itoa(keep.tmdbID()) {
		return writes, errors.New("TMDB 编号变了，不删")
	}
	if !personPathSafe(drop, all) {
		return writes, fmt.Errorf("元数据目录（%s）与别的人物重叠或认不出，片目已换好，条目留着不删", drop.Path)
	}
	if err := embyDeletePerson(r.base, r.key, drop.ID); err != nil {
		return writes, err
	}
	return writes, nil
}

// personMergeWriteGap 两次片目写回之间歇一下：Emby 每次写回都要存库，可能还要写 NFO
var personMergeWriteGap = 200 * time.Millisecond

// ---- HTTP ----

// PersonFillDups GET /personfill/dups：预览有哪些重复人物、会留哪个。只读 Emby，不问 TMDB
func (h *Handler) PersonFillDups(c *gin.Context) {
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Emby 地址或 API 密钥（系统配置 → Emby）"})
		return
	}
	if why := mediaServerBlock(msFeatPeopleMerge); why != "" {
		c.JSON(http.StatusConflict, gin.H{"error": why})
		return
	}
	all, err := embyAllPersons(base, key)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	groups := findDupPersons(all)
	total := len(groups)
	if len(groups) > personMergePreview {
		groups = groups[:personMergePreview]
	}
	type brief struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Image bool   `json:"image"`
	}
	type row struct {
		TmdbID   int     `json:"tmdb_id"`
		Keep     brief   `json:"keep"`
		Drop     []brief `json:"drop"`
		SameName bool    `json:"same_name,omitempty"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		r := row{TmdbID: g.TmdbID, Keep: brief{g.Keep.ID, g.Keep.Name, g.Keep.hasImage()}, SameName: g.SameName}
		for _, d := range g.Drop {
			r.Drop = append(r.Drop, brief{d.ID, d.Name, d.hasImage()})
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"total": total, "persons": len(all), "groups": out}})
}
