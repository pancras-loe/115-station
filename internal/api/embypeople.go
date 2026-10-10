package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"115-station/internal/model"

	"github.com/gin-gonic/gin"
)

// ==================== 演职人员补全（影视刮削页，kind=person）====================
//
// Emby 的人物头像存在它自己的元数据目录里（<programdata>/metadata/people/首字母/姓名/poster.jpg，
// 4.9 起只在这里；更老的版本还有散落在 metadata/library/xx/<guid>/ 的），不在媒体目录旁边，
// 而且 Emby 在数据库里记着每个人物的图片标签 —— 直接往那个目录丢文件，要等人物刷新才认。
// 所以这里一律走 Emby API：POST /Items/{人物id}/Images/Primary 写头像，POST /Items/{人物id} 改名字与简介。
//
// 为什么要补：Emby 的人物头像是懒下载的，连不上 image.tmdb.org 的服务器（国内常见）就一直空着；
// 本站此前写的 NFO 演员只有 name + role，Emby 建出来的人物连 TMDB id 都没有，自己去补也只能按名字猜。
//
// 做法（按片目走，与 MoviePilot 官方插件 personmeta 的形态一致；只读参考，未复制代码）：
//  1. 分页列 Emby 里的电影 / 剧集（带 People），挑出缺头像或名字不是中文的人物；
//     导演 / 编剧在剧集里挂在每一集上，另列一次这部剧的集。
//  2. 定 TMDB 人物 id：人物条目自己有 ProviderIds.Tmdb 就用；没有就拿片目的 credits
//     按名字精确对（片目的 TMDB id 是确定的，同一部片里重名的概率极低）。
//     **不用 /search/person 按名字搜**：同名的人太多，认错一次就把别人的脸钉在这个人身上。
//  3. 头像、中文名、中文简介从 TMDB 人物详情取（落 PersonMeta 缓存，刮削写 NFO 的演员名也读它），
//     下载走本站的 TMDB 代理（fetchTMDBImage），上传给 Emby。改名时锁定 Name / Overview 字段，
//     否则 Emby 自己刷新人物时又改回英文名。
//  4. 记账（EmbyPersonMark）：TMDB 上没头像 / 没中文名 / 对不上号的 30 天内不再问。
//
// 零 115 请求，不拿 taskMu，单独一条人物队列（personLane）：跑一晚上也不挡整理、刮削、探测。

const jobKindPerson = "person"

func init() { jobExecutors[jobKindPerson] = execPersonJob }

// ---- 配置 ----

type personFillCfg struct {
	// AfterScrape 刮削任务结束后，把这次刮过的片目单独排一个补全任务（只看这几部，不动续扫断点）。
	// 与定时开关互不影响：新入库的片当天就有中文名和头像，不用等夜里的定时任务扫到
	AfterScrape bool   `json:"after_scrape"`
	Enabled     bool   `json:"enabled"` // 定时开关；「立即运行」不看它
	Cron        string `json:"cron"`
	// Types 要补的人物：actor（含客串 GuestStar）/ director / writer
	Types  []string `json:"types"`
	Image  bool     `json:"image"`   // 补头像
	ZhName bool     `json:"zh_name"` // 名字改成中文
	ZhBio  bool     `json:"zh_bio"`  // 简介改成中文（只在原简介不是中文时）
	// MaxCast 每部片只看前 N 位演员（Emby 按演员表顺序排）。龙套在 TMDB 上多半既没头像也没中文名，
	// 全算上的话大部分请求都花在他们身上
	MaxCast int `json:"max_cast"`
	// MaxPerRun 单次最多处理多少个人物（真正去查 TMDB / 改 Emby 的），剩下的下次接着来
	MaxPerRun int `json:"max_per_run"`
}

const personFillSetting = "personfill"

func defaultPersonFillCfg() personFillCfg {
	return personFillCfg{
		Enabled: false, Cron: "30 3 * * *", Types: []string{"actor", "director", "writer"},
		Image: true, ZhName: true, ZhBio: true, MaxCast: 20, MaxPerRun: 300,
	}
}

func normalizePersonFillCfg(c personFillCfg) personFillCfg {
	if strings.TrimSpace(c.Cron) == "" {
		c.Cron = "30 3 * * *"
	}
	var types []string
	seen := map[string]bool{}
	for _, t := range c.Types {
		if (t == "actor" || t == "director" || t == "writer") && !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	c.Types = types
	if c.MaxCast < 0 {
		c.MaxCast = 0
	}
	if c.MaxCast > 200 {
		c.MaxCast = 200
	}
	if c.MaxPerRun <= 0 {
		c.MaxPerRun = 300
	}
	if c.MaxPerRun > 5000 {
		c.MaxPerRun = 5000
	}
	return c
}

func (h *Handler) loadPersonFillCfg() personFillCfg {
	c := defaultPersonFillCfg()
	if v := h.Config.GetSetting(personFillSetting); v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return normalizePersonFillCfg(c)
}

// personTypeWanted Emby 的人物类型要不要补
func (c personFillCfg) personTypeWanted(embyType string) bool {
	want := func(k string) bool {
		for _, t := range c.Types {
			if t == k {
				return true
			}
		}
		return false
	}
	switch embyType {
	case "Actor", "GuestStar":
		return want("actor")
	case "Director":
		return want("director")
	case "Writer":
		return want("writer")
	}
	return false
}

func (c personFillCfg) wantsCrew() bool {
	return c.personTypeWanted("Director") || c.personTypeWanted("Writer")
}

// ---- Emby 读写 ----

type embyPersonRef struct {
	ID              string `json:"Id"`
	Name            string `json:"Name"`
	Role            string `json:"Role"`
	Type            string `json:"Type"`
	PrimaryImageTag string `json:"PrimaryImageTag"`
}

type embyTitleItem struct {
	ID          string            `json:"Id"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"`
	ProviderIds map[string]string `json:"ProviderIds"`
	People      []embyPersonRef   `json:"People"`
}

// providerID ProviderIds 的键大小写不固定（Tmdb / tmdb 都见过）
func providerID(ids map[string]string, key string) string {
	for k, v := range ids {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func embyGetJSON(base, key, path string, q url.Values, out any) error {
	resp, err := embyRequest(http.MethodGet, base, key, path, q, nil)
	if err != nil {
		return fmt.Errorf("连接 Emby 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("Emby %s 返回 HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// embyTitlesPage 一页电影 / 剧集（带演职人员）
func embyTitlesPage(base, key string, start, limit int) ([]embyTitleItem, int, error) {
	q := url.Values{
		"Recursive": {"true"}, "IncludeItemTypes": {"Movie,Series"}, "Fields": {"ProviderIds,People"},
		"StartIndex": {strconv.Itoa(start)}, "Limit": {strconv.Itoa(limit)},
		// 按加入时间排：新入库的排在最后，续扫断点（片目序号）不会因为新片插到中间而整体错位
		"SortBy": {"DateCreated,SortName"}, "SortOrder": {"Ascending"}, "EnableUserData": {"false"},
	}
	var page struct {
		Items []embyTitleItem `json:"Items"`
		Total int             `json:"TotalRecordCount"`
	}
	if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
		return nil, 0, err
	}
	return page.Items, page.Total, nil
}

// embySeriesCrew 一部剧所有集上的导演 / 编剧（剧集的主创挂在集上，剧本身只有演员）。去重
func embySeriesCrew(base, key, seriesID string) ([]embyPersonRef, error) {
	seen := map[string]bool{}
	var out []embyPersonRef
	for start := 0; ; start += 500 {
		q := url.Values{
			"ParentId": {seriesID}, "Recursive": {"true"}, "IncludeItemTypes": {"Episode"}, "Fields": {"People"},
			"StartIndex": {strconv.Itoa(start)}, "Limit": {"500"}, "EnableUserData": {"false"},
		}
		var page struct {
			Items []embyTitleItem `json:"Items"`
			Total int             `json:"TotalRecordCount"`
		}
		if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
			return out, err
		}
		for _, ep := range page.Items {
			for _, p := range ep.People {
				if (p.Type == "Director" || p.Type == "Writer") && p.ID != "" && !seen[p.ID] {
					seen[p.ID] = true
					out = append(out, p)
				}
			}
		}
		if len(page.Items) == 0 || start+len(page.Items) >= page.Total {
			return out, nil
		}
	}
}

// embyPersonItem 人物的完整条目：原样保留全部字段，改完整个 POST 回去（Emby 的条目更新接口要完整 DTO）
type embyPersonItem map[string]any

func (p embyPersonItem) str(k string) string {
	s, _ := p[k].(string)
	return s
}

func (p embyPersonItem) providerIDs() map[string]string {
	out := map[string]string{}
	if m, ok := p["ProviderIds"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func (p embyPersonItem) hasPrimaryImage() bool {
	m, ok := p["ImageTags"].(map[string]any)
	if !ok {
		return false
	}
	s, _ := m["Primary"].(string)
	return s != ""
}

// lockField 把字段加进 LockedFields：Emby 刷新人物元数据时不再覆盖
func (p embyPersonItem) lockField(f string) {
	var locked []any
	if l, ok := p["LockedFields"].([]any); ok {
		locked = l
	}
	for _, v := range locked {
		if s, _ := v.(string); s == f {
			return
		}
	}
	p["LockedFields"] = append(locked, f)
}

// personItemFields 读人物条目时要带回的字段。改名 / 简介要把整条 DTO POST 回去，
// Emby 的条目更新接口会拿请求里的值覆盖这些字段 —— 少带一个就等于把它清空（生日、出生地、锁定字段 …）。
// Settings 带回 LockData / LockedFields
const personItemFields = "ProviderIds,Overview,SortName,OriginalTitle,Genres,Tags,Studios,Taglines," +
	"ProductionLocations,PremiereDate,EndDate,ProductionYear,DateCreated,CommunityRating,CriticRating," +
	"OfficialRating,CustomRating,ExternalUrls,RemoteTrailers,Settings"

// embyGetPerson 读人物条目。
//
// **别改回 /Users/{uid}/Items/{id}**：Emby 按用户读单个人物条目时，若这个人物从没刷新过元数据，
// 会先当场去 TMDB 拉一遍再返回（UserLibraryService 的按需刷新）。连不上 TMDB 的服务器上这一下
// 就卡到超时 —— 2026-09-30 现场：每个人物都 20 秒超时失败，Emby 日志里同时刻正卡在
// api.themoviedb.org/3/configuration/primary_translations。列表查询 /Items?Ids= 不触发按需刷新
func embyGetPerson(base, key, pid string) (embyPersonItem, error) {
	var page struct {
		Items []embyPersonItem `json:"Items"`
	}
	q := url.Values{"Ids": {pid}, "Fields": {personItemFields}, "EnableUserData": {"false"}}
	if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
		return nil, err
	}
	for _, it := range page.Items {
		if it.str("Id") == pid {
			return it, nil
		}
	}
	return nil, fmt.Errorf("Emby 里没有这个人物条目（id %s）", pid)
}

func embyUpdateItem(base, key, id string, it embyPersonItem) error {
	body, err := json.Marshal(it)
	if err != nil {
		return err
	}
	resp, err := embyRequest(http.MethodPost, base, key, "/Items/"+url.PathEscape(id), nil, body)
	if err != nil {
		return fmt.Errorf("连接 Emby 失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("更新人物信息失败：HTTP %d", resp.StatusCode)
	}
	return nil
}

// embyPersonNameTaken 同名人物是否已经是另一个条目。
// 常见于：新刮的 NFO 已经写了中文名，Emby 按中文名建了新人物，老片子还挂着英文名的那个。
// 这时再把英文名那个改成同名，两个人物同名，Emby 按名字认人时会乱。
// 用 /Persons 列表按关键词查再精确比对名字，不用 /Persons/{名字}（单条目读取，理由同 embyGetPerson）
func embyPersonNameTaken(base, key, name, selfID string) (bool, error) {
	var page struct {
		Items []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Items"`
	}
	q := url.Values{"SearchTerm": {name}, "Limit": {"50"}, "EnableUserData": {"false"}, "EnableImages": {"false"}}
	if err := embyGetJSON(base, key, "/Persons", q, &page); err != nil {
		return false, err
	}
	for _, p := range page.Items {
		if strings.TrimSpace(p.Name) == name && p.ID != selfID {
			return true, nil
		}
	}
	return false, nil
}

func embyUploadPrimary(base, key, id string, data []byte) error {
	u := base + "/Items/" + url.PathEscape(id) + "/Images/Primary?api_key=" + url.QueryEscape(key)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(data))))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", http.DetectContentType(data))
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("连接 Emby 失败: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("上传头像失败：HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- 片目 credits 与按名字对号 ----

// creditEntry 片目演职员表里的一个人。Group：cast / Directing / Writing / 其他部门名
type creditEntry struct {
	ID    int
	Names []string
	Group string
}

type titleCredits struct {
	entries []creditEntry
	zh      map[int]string // 缓存里已知的中文名：Emby 里的人物可能已经是中文名了
}

// fetchTitleCredits 电影取 /movie/{id}/credits；剧集取 aggregate_credits（汇总所有季的演员与主创）
func fetchTitleCredits(tc *TmdbClient, isTV bool, tmdbID int) (*titleCredits, error) {
	ep := "/movie/" + strconv.Itoa(tmdbID) + "/credits"
	if isTV {
		ep = "/tv/" + strconv.Itoa(tmdbID) + "/aggregate_credits"
	}
	body, err := tc.get(ep, nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Cast []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			OriginalName string `json:"original_name"`
		} `json:"cast"`
		Crew []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			OriginalName string `json:"original_name"`
			Department   string `json:"department"`
		} `json:"crew"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("演职员表解析失败")
	}
	tcr := &titleCredits{}
	var ids []int
	for _, c := range d.Cast {
		tcr.entries = append(tcr.entries, creditEntry{ID: c.ID, Names: []string{c.Name, c.OriginalName}, Group: "cast"})
		ids = append(ids, c.ID)
	}
	for _, c := range d.Crew {
		tcr.entries = append(tcr.entries, creditEntry{ID: c.ID, Names: []string{c.Name, c.OriginalName}, Group: c.Department})
		ids = append(ids, c.ID)
	}
	tcr.zh = cachedZhNames(ids)
	return tcr, nil
}

// personNameKey 比较用的名字：去空白与间隔号、小写、转简体（「汤姆 · 汉克斯」「湯姆·漢克斯」算同一个）
func personNameKey(s string) string {
	s = strings.ToLower(toSimplified(strings.TrimSpace(s)))
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '·', '•', '・', '.', '‧', '-', ' ':
			return -1
		}
		return r
	}, s)
}

// creditGroupOf Emby 人物类型对应 TMDB 演职员表的哪一组
func creditGroupOf(embyType string) string {
	switch embyType {
	case "Director":
		return "Directing"
	case "Writer":
		return "Writing"
	}
	return "cast"
}

// matchCredit 在片目演职员表里按名字找 TMDB 人物 id。先在对应的组里找（演员找 cast，导演找 Directing），
// 找到且只有一个人就是他；组里没有，再看整张表里是不是恰好只有一个同名的人。有歧义返回 0，宁可不补
func matchCredit(c *titleCredits, name, embyType string) int {
	if c == nil {
		return 0
	}
	k := personNameKey(name)
	if k == "" {
		return 0
	}
	group := creditGroupOf(embyType)
	inGroup, all := map[int]bool{}, map[int]bool{}
	for _, e := range c.entries {
		hit := false
		for _, n := range e.Names {
			if n != "" && personNameKey(n) == k {
				hit = true
				break
			}
		}
		if !hit && c.zh[e.ID] != "" && personNameKey(c.zh[e.ID]) == k {
			hit = true
		}
		if !hit {
			continue
		}
		all[e.ID] = true
		if e.Group == group {
			inGroup[e.ID] = true
		}
	}
	for _, set := range []map[int]bool{inGroup, all} {
		if len(set) == 1 {
			for id := range set {
				return id
			}
		}
		if len(set) > 1 {
			return 0
		}
	}
	return 0
}

// ---- 单个人物的处理与记账 ----

const (
	personStateNoMatch   = "no_match"   // 对不上 TMDB 人物
	personStateNoImage   = "no_image"   // TMDB 上也没有头像
	personStateNoZh      = "no_zh"      // TMDB 上没有中文名
	personStateNameTaken = "name_taken" // 中文名已被另一个人物条目占用
	personStateNoBio     = "no_bio"     // TMDB 上没有中文简介
	personStateFailed    = "failed"     // 网络 / Emby 出错
)

// personStateText 记账状态的中文说法（日志、任务结果、配置卡统计共用）
var personStateText = map[string]string{
	personStateNoMatch:   "对不上 TMDB 人物",
	personStateNoImage:   "TMDB 无头像",
	personStateNoZh:      "TMDB 无中文名",
	personStateNameTaken: "中文名已被别的人物占用",
	personStateNoBio:     "TMDB 无中文简介",
	personStateFailed:    "出错",
}

// personRetryAfter 记账状态隔多久再试：查无结果的一个月后再看（TMDB 可能有人补上），出错的第二天再来，
// 连错三次以上放宽到一周（多半是这个人物条目本身有问题）
func personRetryAfter(state string, tries int) time.Duration {
	if state == personStateFailed {
		if tries >= 3 {
			return 7 * 24 * time.Hour
		}
		return 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// personResult 一个人物这一轮的结果
type personResult struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	TmdbID  int    `json:"tmdb_id,omitempty"`
	Image   bool   `json:"image,omitempty"`   // 这次补上了头像
	Renamed string `json:"renamed,omitempty"` // 这次改成的中文名
	Bio     bool   `json:"bio,omitempty"`     // 这次补上了中文简介
	State   string `json:"state,omitempty"`   // 还没补全的原因（记账状态），补全了为空
	Note    string `json:"note,omitempty"`
}

// personPlan 由人物现状与 TMDB 数据算出这次要改什么（纯函数，便于测试）
type personPlan struct {
	uploadImage bool
	newName     string
	newBio      string
	setTmdbID   bool
	state       string // 做完之后还缺什么（空 = 补全了）
}

func planPerson(cfg personFillCfg, name, overview string, hasImage, hasTmdbID bool, meta model.PersonMeta, nameTaken bool) personPlan {
	var p personPlan
	missing := []string{}
	if cfg.Image && !hasImage {
		if meta.ProfilePath != "" {
			p.uploadImage = true
		} else {
			missing = append(missing, personStateNoImage)
		}
	}
	if cfg.ZhName && !hasHan(name) {
		switch {
		case meta.ZhName == "":
			missing = append(missing, personStateNoZh)
		case nameTaken:
			missing = append(missing, personStateNameTaken)
		default:
			p.newName = meta.ZhName
		}
	}
	if cfg.ZhBio && !hasHan(overview) {
		if meta.ZhBio != "" {
			p.newBio = meta.ZhBio
		} else {
			missing = append(missing, personStateNoBio)
		}
	}
	p.setTmdbID = !hasTmdbID && meta.TmdbID > 0
	if len(missing) > 0 {
		p.state = missing[0]
	}
	return p
}

// saveMark 记账：补全了删行，没补全按原因定下次什么时候再来
func savePersonMark(pid, name string, tmdbID int, state, note string) {
	if state == "" {
		model.DB.Delete(&model.EmbyPersonMark{}, "person_id = ?", pid)
		return
	}
	var m model.EmbyPersonMark
	if model.DB.First(&m, "person_id = ?", pid).Error != nil {
		m = model.EmbyPersonMark{PersonID: pid}
	}
	if m.State == state {
		m.Tries++
	} else {
		m.Tries = 1
	}
	m.Name, m.TmdbID, m.State, m.Note = truncateStr(name, 250), tmdbID, state, truncateStr(note, 250)
	m.NextAt = time.Now().Add(personRetryAfter(state, m.Tries))
	model.DB.Save(&m)
}

// personRunner 一次任务的上下文
type personRunner struct {
	cfg      personFillCfg
	tc       *TmdbClient
	base     string
	key      string
	skip     map[string]bool // 记账里还没到期的人物
	done     map[string]bool // 本轮已处理过的人物（同一个人出现在很多部片里）
	credits  map[string]*titleCredits
	results  []personResult
	handled  int
	lastTMDB time.Time
}

// tmdbPace TMDB 请求间隔：官方限速约每秒 40 次，这里慢慢来，别跟整理抢配额
const tmdbPace = 300 * time.Millisecond

func (r *personRunner) pace() {
	if d := tmdbPace - time.Since(r.lastTMDB); d > 0 {
		time.Sleep(d)
	}
	r.lastTMDB = time.Now()
}

// creditsOf 片目的演职员表，本轮缓存
func (r *personRunner) creditsOf(t embyTitleItem) *titleCredits {
	id, _ := strconv.Atoi(providerID(t.ProviderIds, "Tmdb"))
	if id <= 0 {
		return nil
	}
	k := t.Type + ":" + strconv.Itoa(id)
	if c, ok := r.credits[k]; ok {
		return c
	}
	r.pace()
	c, err := fetchTitleCredits(r.tc, t.Type == "Series", id)
	if err != nil {
		vlog("[演职人员] ○ 《%s》演职员表取不到: %v", t.Name, err)
	}
	r.credits[k] = c
	return c
}

// eligible 人物在不在这一轮的范围里（类型对、没在记账里暂缓、本轮没处理过）
func (r *personRunner) eligible(p embyPersonRef) bool {
	return p.ID != "" && p.Name != "" && r.cfg.personTypeWanted(p.Type) && !r.skip[p.ID] && !r.done[p.ID]
}

// needsListed 列表里就看得出的缺口：没头像、名字不是中文
func (r *personRunner) needsListed(p embyPersonRef) bool {
	return (r.cfg.Image && p.PrimaryImageTag == "") || (r.cfg.ZhName && !hasHan(p.Name))
}

// candidates 片目里这一轮要处理的人物：前 MaxCast 位演员 + 导演 / 编剧。
// 头像和名字列表里就有；简介列表里没有，只剩「缺中文简介」可能的人物再按 id 批量读一次（Emby 本地列表查询）
func (r *personRunner) candidates(t embyTitleItem) []embyPersonRef {
	var out, rest []embyPersonRef
	actors := 0
	people := t.People
	if t.Type == "Series" && r.cfg.wantsCrew() {
		if crew, err := embySeriesCrew(r.base, r.key, t.ID); err == nil {
			people = append(append([]embyPersonRef{}, people...), crew...)
		} else {
			vlog("[演职人员] ○ 《%s》分集主创读取失败: %v", t.Name, err)
		}
	}
	seen := map[string]bool{}
	for _, p := range people {
		if p.Type == "Actor" || p.Type == "GuestStar" {
			actors++
			if r.cfg.MaxCast > 0 && actors > r.cfg.MaxCast {
				continue
			}
		}
		if seen[p.ID] || !r.eligible(p) {
			continue
		}
		seen[p.ID] = true
		if r.needsListed(p) {
			out = append(out, p)
		} else if r.cfg.ZhBio {
			rest = append(rest, p)
		}
	}
	if len(rest) > 0 {
		ids := make([]string, len(rest))
		for i, p := range rest {
			ids[i] = p.ID
		}
		bios, err := embyOverviews(r.base, r.key, ids)
		if err != nil {
			vlog("[演职人员] ○ 《%s》人物简介读取失败，本轮不补简介: %v", t.Name, err)
			return out
		}
		for _, p := range rest {
			if bio, ok := bios[p.ID]; ok && !hasHan(bio) {
				out = append(out, p)
			}
		}
	}
	return out
}

// embyOverviews 一批人物的简介（id → 简介；Emby 没返回的 id 不在结果里）。
// 走列表查询，理由同 embyGetPerson：按用户读单个条目会触发按需刷新
func embyOverviews(base, key string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(ids); i += 50 {
		j := i + 50
		if j > len(ids) {
			j = len(ids)
		}
		var page struct {
			Items []struct {
				ID       string `json:"Id"`
				Overview string `json:"Overview"`
			} `json:"Items"`
		}
		q := url.Values{"Ids": {strings.Join(ids[i:j], ",")}, "Fields": {"Overview"}, "EnableUserData": {"false"}, "EnableImages": {"false"}}
		if err := embyGetJSON(base, key, "/Items", q, &page); err != nil {
			return out, err
		}
		for _, it := range page.Items {
			out[it.ID] = it.Overview
		}
	}
	return out, nil
}

// handle 处理一个人物：定 TMDB id → 取人物数据 → 改 Emby → 记账
func (r *personRunner) handle(t embyTitleItem, ref embyPersonRef) personResult {
	res := personResult{Name: ref.Name, Title: t.Name}
	fail := func(tmdbID int, err error) personResult {
		res.State, res.Note = personStateFailed, err.Error()
		savePersonMark(ref.ID, ref.Name, tmdbID, res.State, res.Note)
		return res
	}
	item, err := embyGetPerson(r.base, r.key, ref.ID)
	if err != nil {
		return fail(0, err)
	}
	name := item.str("Name")
	if name == "" {
		name = ref.Name
	}
	res.Name = name

	tmdbID, _ := strconv.Atoi(providerID(item.providerIDs(), "Tmdb"))
	hasTmdbID := tmdbID > 0
	if !hasTmdbID {
		tmdbID = matchCredit(r.creditsOf(t), name, ref.Type)
	}
	if tmdbID <= 0 {
		res.State = personStateNoMatch
		res.Note = "《" + t.Name + "》的演职员表里没有同名的人"
		savePersonMark(ref.ID, name, 0, res.State, res.Note)
		return res
	}
	res.TmdbID = tmdbID

	meta, err := loadPersonMeta(r.tc, tmdbID, r.pace)
	if err != nil {
		return fail(tmdbID, fmt.Errorf("TMDB 人物详情: %v", err))
	}

	taken := false
	if r.cfg.ZhName && !hasHan(name) && meta.ZhName != "" && meta.ZhName != name {
		if taken, err = embyPersonNameTaken(r.base, r.key, meta.ZhName, ref.ID); err != nil {
			return fail(tmdbID, fmt.Errorf("查同名人物失败: %v", err))
		}
	}
	plan := planPerson(r.cfg, name, item.str("Overview"), item.hasPrimaryImage(), hasTmdbID, meta, taken)

	if plan.uploadImage {
		data, err := fetchTMDBImage("h632", meta.ProfilePath)
		if err != nil {
			return fail(tmdbID, fmt.Errorf("下载头像失败: %v", err))
		}
		if err := embyUploadPrimary(r.base, r.key, ref.ID, data); err != nil {
			return fail(tmdbID, err)
		}
		res.Image = true
	}
	if plan.newName != "" || plan.newBio != "" || plan.setTmdbID {
		if plan.newName != "" {
			item["Name"] = plan.newName
			item.lockField("Name")
		}
		if plan.newBio != "" {
			item["Overview"] = plan.newBio
			item.lockField("Overview")
		}
		if plan.setTmdbID {
			ids := map[string]any{}
			if m, ok := item["ProviderIds"].(map[string]any); ok {
				ids = m
			}
			ids["Tmdb"] = strconv.Itoa(tmdbID)
			item["ProviderIds"] = ids
		}
		if err := embyUpdateItem(r.base, r.key, ref.ID, item); err != nil {
			// 头像已经传上去了，这一步失败也要记账：下次还缺名字，会再来
			return fail(tmdbID, err)
		}
		res.Renamed, res.Bio = plan.newName, plan.newBio != ""
	}
	res.State = plan.state
	if plan.state != "" {
		res.Note = personStateText[plan.state]
	}
	savePersonMark(ref.ID, name, tmdbID, plan.state, res.Note)
	return res
}

// ---- 任务 ----

// personJobResult 任务结果（TaskJob.Result）。键名与任务详情的通用摘要对齐（jobStatus.ts 的 RESULT_LABEL），
// problems 是字符串列表，详情页直接列出来
type personJobResult struct {
	Titles    int            `json:"titles"`
	Handled   int            `json:"people"`
	Images    int            `json:"images"`
	Renamed   int            `json:"renamed"`
	Bios      int            `json:"bios"`
	States    map[string]int `json:"states,omitempty"`
	Problems  []string       `json:"problems,omitempty"` // 没补全的人物，最多 200 条，出错的在前
	Truncated bool           `json:"truncated,omitempty"`
}

// newPersonRunner 校验配置、取 Emby 与 TMDB 客户端、装上记账里还没到期的人物
func newPersonRunner(h *Handler) (*personRunner, error) {
	cfg := h.loadPersonFillCfg()
	if len(cfg.Types) == 0 {
		return nil, errors.New("没有勾选要补的人物类型（演员 / 导演 / 编剧）")
	}
	if !cfg.Image && !cfg.ZhName && !cfg.ZhBio {
		return nil, errors.New("头像、中文名、中文简介都没开，没有可做的")
	}
	base, key, ok := h.embyServerInfo()
	if !ok || key == "" {
		return nil, errors.New("未配置 Emby 地址或 API 密钥（系统配置 → Emby）")
	}
	tc, err := loadTmdbClient()
	if err != nil {
		return nil, err
	}
	r := &personRunner{
		cfg: cfg, tc: tc, base: base, key: key,
		skip: map[string]bool{}, done: map[string]bool{}, credits: map[string]*titleCredits{},
	}
	var marks []model.EmbyPersonMark
	model.DB.Select("person_id").Where("next_at > ?", time.Now()).Find(&marks)
	for _, m := range marks {
		r.skip[m.PersonID] = true
	}
	return r, nil
}

// runTitle 处理一部片目里这一轮要补的人物；在停止请求或单次上限处停下
func (r *personRunner) runTitle(t embyTitleItem) (stopped, capped bool) {
	for _, p := range r.candidates(t) {
		if personLane.stopRequested() || stopRequestedGlobal() {
			return true, false
		}
		if r.handled >= r.cfg.MaxPerRun {
			return false, true
		}
		r.done[p.ID] = true
		r.handled++
		personLane.setSub("人物", r.handled, r.cfg.MaxPerRun, p.Name)
		res := r.handle(t, p)
		r.results = append(r.results, res)
		logPersonResult(res)
	}
	return false, false
}

func execPersonJob(h *Handler, job *model.TaskJob) (jobOutcome, error) {
	r, err := newPersonRunner(h)
	if err != nil {
		return jobOutcome{}, err
	}
	if p := decodeJobParams(job); p.Person != nil && len(p.Person.Targets) > 0 {
		return execPersonTargets(r, job, p.Person.Targets)
	}
	cfg, base, key := r.cfg, r.base, r.key

	// 片目总数：先问一次（Limit=1），续扫断点超出总数（库里删了片）就从头来
	_, total, err := embyTitlesPage(base, key, 0, 1)
	if err != nil {
		return jobOutcome{}, err
	}
	cur := h.loadPersonFillCursor()
	if cur >= total {
		cur = 0
	}
	log.Printf("[演职人员] ▶ 开始补全：从第 %d/%d 部片目起（记账中暂缓 %d 个人物，单次上限 %d）",
		cur+1, total, len(r.skip), cfg.MaxPerRun)

	// 从断点扫到末尾，再从头扫到断点，绕满一圈。中途因单次上限 / 停止 / 出错退出时，
	// 把停下的那部片目记成新断点（那部片目下次重看一遍：已处理的人物补全了或进了记账，不会重复处理）
	stopped, capped, broken := false, false, false
	titles, stopAt := 0, 0
	const pageSize = 100
	ranges := [][2]int{{cur, total}, {0, cur}}
scan:
	for _, rg := range ranges {
		for start := rg[0]; start < rg[1]; start += pageSize {
			limit := pageSize
			if rg[1]-start < limit {
				limit = rg[1] - start
			}
			items, _, err := embyTitlesPage(base, key, start, limit)
			if err != nil {
				log.Printf("[演职人员] ✗ 读取 Emby 片目中断（第 %d 部起）: %v", start+1, err)
				broken, stopAt = true, start
				break scan
			}
			if len(items) == 0 {
				break
			}
			for i, t := range items {
				idx := start + i
				if personLane.stopRequested() || stopRequestedGlobal() {
					stopped, stopAt = true, idx
					break scan
				}
				titles++
				personLane.set("扫描片目", titles, total, t.Name)
				if s, c := r.runTitle(t); s || c {
					stopped, capped, stopAt = s, c, idx
					break scan
				}
			}
		}
	}
	if stopped || capped || broken {
		h.savePersonFillCursor(stopAt)
	} else {
		h.savePersonFillCursor(0) // 绕满一圈，下次从头
	}
	if broken && titles == 0 {
		return jobOutcome{}, errors.New("读取 Emby 片目失败，详见日志")
	}

	out := summarizePersonRun(r.results, titles)
	msg := personRunMessage(out, total, stopAt, stopped, capped, broken)
	partial := out.States[personStateFailed] > 0
	idle := job.Priority == jobPriorityBackground && out.Handled == 0 && !stopped
	return jobOutcome{Message: msg, Result: out, Canceled: stopped, Partial: partial, Idle: idle}, nil
}

// stopRequestedGlobal 服务正在退出
func stopRequestedGlobal() bool {
	select {
	case <-stopCh:
		return true
	default:
		return false
	}
}

func logPersonResult(res personResult) {
	var did []string
	if res.Image {
		did = append(did, "头像")
	}
	if res.Renamed != "" {
		did = append(did, "中文名 "+res.Renamed)
	}
	if res.Bio {
		did = append(did, "简介")
	}
	switch {
	case res.State == personStateFailed:
		log.Printf("[演职人员] ✗ %s（《%s》）：%s", res.Name, res.Title, res.Note)
	case len(did) > 0 && res.State == "":
		log.Printf("[演职人员] ✓ %s：补上 %s", res.Name, strings.Join(did, "、"))
	case len(did) > 0:
		log.Printf("[演职人员] ✓ %s：补上 %s；%s", res.Name, strings.Join(did, "、"), res.Note)
	default:
		vlog("[演职人员] ○ %s（《%s》）：%s", res.Name, res.Title, res.Note)
	}
}

func summarizePersonRun(results []personResult, titles int) personJobResult {
	out := personJobResult{Titles: titles, Handled: len(results), States: map[string]int{}}
	for _, res := range results {
		if res.Image {
			out.Images++
		}
		if res.Renamed != "" {
			out.Renamed++
		}
		if res.Bio {
			out.Bios++
		}
		if res.State != "" {
			out.States[res.State]++
		}
	}
	// 详情里只列没补全的，出错的在前
	var failed, rest []string
	for _, res := range results {
		if res.State == "" {
			continue
		}
		line := fmt.Sprintf("%s（《%s》）：%s", res.Name, res.Title, res.Note)
		if res.State == personStateFailed {
			failed = append(failed, line)
		} else {
			rest = append(rest, line)
		}
	}
	out.Problems = append(failed, rest...)
	if len(out.Problems) > 200 {
		out.Problems, out.Truncated = out.Problems[:200], true
	}
	return out
}

func personRunMessage(out personJobResult, total, stopAt int, stopped, capped, broken bool) string {
	msg := personHandledMessage(out)
	switch {
	case stopped:
		msg += fmt.Sprintf("；已按要求停止，下次从第 %d/%d 部片目接着补", stopAt+1, total)
	case capped:
		msg += fmt.Sprintf("；达到单次上限，下次从第 %d/%d 部片目接着补", stopAt+1, total)
	case broken:
		msg += fmt.Sprintf("；读取 Emby 片目中断，下次从第 %d/%d 部片目接着补", stopAt+1, total)
	default:
		msg += fmt.Sprintf("（已看完全部 %d 部片目）", total)
	}
	return msg
}

// personHandledMessage 「处理了多少人、补上什么、没补全的各几个」
func personHandledMessage(out personJobResult) string {
	var msg string
	if out.Handled == 0 {
		msg = fmt.Sprintf("看了 %d 部片目，没有要补的人物", out.Titles)
	} else {
		msg = fmt.Sprintf("处理 %d 个人物：头像 %d、中文名 %d、简介 %d", out.Handled, out.Images, out.Renamed, out.Bios)
		var rest []string
		for _, st := range []string{personStateFailed, personStateNoMatch, personStateNoImage, personStateNoZh, personStateNameTaken, personStateNoBio} {
			if n := out.States[st]; n > 0 {
				rest = append(rest, fmt.Sprintf("%s %d", personStateText[st], n))
			}
		}
		if len(rest) > 0 {
			msg += "；" + strings.Join(rest, "、")
		}
	}
	return msg
}

// ---- 续扫断点 ----
//
// 单次上限挡住的时候，下次从停下的那部片目接着看，而不是每次从第一部重来：
// 否则库大、上限小时，名额每次都先花在前面那些「还在记账期外、又总是补不全」的人物上。
// 存的是片目序号（按加入时间排序），不是 id：片目被删了序号前移几位，最多重看或漏看几部，
// 漏的绕一圈回来还会看到

const personFillCursorSetting = "personfill_cursor"

func (h *Handler) loadPersonFillCursor() int {
	n, _ := strconv.Atoi(strings.TrimSpace(h.Config.GetSetting(personFillCursorSetting)))
	if n < 0 {
		return 0
	}
	return n
}

func (h *Handler) savePersonFillCursor(n int) {
	if err := h.Config.SaveSetting(personFillCursorSetting, strconv.Itoa(n)); err != nil {
		log.Printf("[演职人员] ✗ 保存续扫位置失败: %v", err)
	}
}

// enqueuePersonJob 入队。定时与手动共用一个去重键：排着一个就不再排第二个
func enqueuePersonJob(h *Handler, source string, priority int) (model.TaskJob, error) {
	return enqueueJob(h.DB, jobSpec{
		Kind: jobKindPerson, Title: "演职人员补全", DedupeKey: "personfill",
		Source: source, Priority: priority,
	})
}

// ---- 定时 ----

var (
	personFillLastRun string
	personFillMu      sync.Mutex
)

func StartPersonFillScheduler(h *Handler) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-stopCh:
				return
			}
			cfg := h.loadPersonFillCfg()
			now := time.Now()
			if !cfg.Enabled || !CronMatch(cfg.Cron, now) {
				continue
			}
			k := now.Format("2006-01-02 15:04")
			personFillMu.Lock()
			dup := personFillLastRun == k
			personFillLastRun = k
			personFillMu.Unlock()
			if dup {
				continue
			}
			if _, err := enqueuePersonJob(h, "cron", jobPriorityBackground); err != nil {
				log.Printf("[演职人员] ✗ 定时任务入队失败: %v", err)
			}
		}
	}()
}

// ---- HTTP ----

// PersonFillGetConfig GET /personfill/config → 配置 + 记账统计
func (h *Handler) PersonFillGetConfig(c *gin.Context) {
	cfg := h.loadPersonFillCfg()
	type stateCount struct {
		State string
		N     int
	}
	var rows []stateCount
	model.DB.Model(&model.EmbyPersonMark{}).Select("state, count(*) as n").Group("state").Scan(&rows)
	marks := map[string]int{}
	for _, r := range rows {
		marks[r.State] = r.N
	}
	var metas, zh int64
	model.DB.Model(&model.PersonMeta{}).Count(&metas)
	model.DB.Model(&model.PersonMeta{}).Where("zh_name <> ''").Count(&zh)
	next := ""
	if t := nextCronTime(cfg.Cron, time.Now()); !t.IsZero() {
		next = t.Format("01-02 15:04")
	}
	var last model.TaskJob
	lastJob := gin.H(nil)
	if model.DB.Where("kind = ? AND status NOT IN ?", jobKindPerson, []string{jobQueued, jobRunning}).
		Order("id DESC").First(&last).Error == nil {
		lastJob = gin.H{"id": last.ID, "status": last.Status, "message": last.Message, "finished_at": last.FinishedAt}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"config": cfg, "next_run": next, "marks": marks, "state_text": personStateText,
		"cached": metas, "cached_zh": zh, "last_job": lastJob, "cursor": h.loadPersonFillCursor(),
	}})
}

// PersonFillSaveConfig POST /personfill/config
func (h *Handler) PersonFillSaveConfig(c *gin.Context) {
	var req personFillCfg
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效"})
		return
	}
	req = normalizePersonFillCfg(req)
	if nextCronTime(req.Cron, time.Now()).IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cron 表达式无效（5 段式：分 时 日 月 周，如 30 3 * * *）"})
		return
	}
	b, _ := json.Marshal(req)
	if err := h.Config.SaveSetting(personFillSetting, string(b)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败：" + err.Error()})
		return
	}
	log.Printf("[配置] 演职人员补全：刮削后 %v，定时 %v（%s），类型 %v，头像 %v / 中文名 %v / 简介 %v",
		req.AfterScrape, req.Enabled, req.Cron, req.Types, req.Image, req.ZhName, req.ZhBio)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

// PersonFillRun POST /personfill/run → 入队，202
func (h *Handler) PersonFillRun(c *gin.Context) {
	if base, key, ok := h.embyServerInfo(); !ok || key == "" || base == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未配置 Emby 地址或 API 密钥（系统配置 → Emby）"})
		return
	}
	job, err := enqueuePersonJob(h, "web", jobPriorityManual)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.queuedReply(c, job, "演职人员补全")
}

// PersonFillResetMarks POST /personfill/reset：清空记账与续扫位置，下次任务从第一部片目起把暂缓的人物全部重新看一遍
func (h *Handler) PersonFillResetMarks(c *gin.Context) {
	res := model.DB.Where("1 = 1").Delete(&model.EmbyPersonMark{})
	h.savePersonFillCursor(0)
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已清空 %d 条记账，下次从第一部片目开始", res.RowsAffected)})
}
