package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==================== 生活事件拉取层 ====================
//
// 115 把用户在网盘上的每次操作记成一条生活事件，倒序拉出来就能感知变动。
//
//	app → GET https://proapi.115.com/{app}/behavior/detail   快，但更容易被风控返 405
//	web → GET https://webapi.115.com/behavior/detail         慢一点，风控轻
//
// 参数：type（省略=全部）、limit、offset、date（可选 YYYY-MM-DD）。
//
// 改造前这里是「固定 30 条一页、一路翻到本页没有新事件为止」，一轮最多 34 次请求，
// 而且其中绝大多数是浏览/标星这类跟媒体库无关的事件。现在按 p115client 的做法走：
// 游标命中即停、首批 64/1000、同一 file_id 只留最新、浏览类直接丢。
// 日常一轮 1 次请求。

const (
	lifeAPIProapi = "https://proapi.115.com/android/behavior/detail"
	lifeAPIWeb    = "https://webapi.115.com/behavior/detail"

	// lifeMaxPages 单轮翻页上限，防止游标异常时无限翻
	lifeMaxPages = 50
	// lifeWebFallback proapi 连续 405 后固定走 webapi 的时长
	lifeWebFallback = 24 * time.Hour
	// lifeGateEmptyRuns 连续这么多轮零事件后复检「115 生活」开关
	lifeGateEmptyRuns = 20
)

// lifeIgnoreTypes 浏览/标星/打标签类事件：跟媒体库无关，入库前就丢掉。
// 活跃账号里它们占绝对多数，落库只会拖慢去重查询、挤占单轮上限。
// 取值同 p115client 的 IGNORE_BEHAVIOR_TYPES
var lifeIgnoreTypes = map[string]bool{
	"3": true, "star_image": true,
	"4": true, "star_file": true,
	"7": true, "browse_image": true,
	"8": true, "browse_video": true,
	"9": true, "browse_audio": true,
	"10": true, "browse_document": true,
	"19": true, "folder_label": true,
}

// lifeCooldown 翻页与取页之间的冷却。app 通道对高频很敏感（返 405），
// p115client 与 openStrm 都用 2s。做成变量只为让测试不必真等，生产行为不变
var lifeCooldown = 2 * time.Second

// lifeCursor 增量游标。事件 id 单调递增，命中 id <= FromID 即整轮停止
type lifeCursor struct {
	FromID   string `json:"from_id"`
	FromTime int64  `json:"from_time"`
}

func (c lifeCursor) isZero() bool { return c.FromID == "" && c.FromTime == 0 }

// parseLifeCursor 从 setting 值还原游标；解析不出按「无游标」处理
func parseLifeCursor(raw string) lifeCursor {
	var c lifeCursor
	if raw == "" {
		return c
	}
	_ = json.Unmarshal([]byte(raw), &c)
	return c
}

func encodeLifeCursor(c lifeCursor) string {
	b, _ := json.Marshal(c)
	return string(b)
}

// eventIDNewer 判断 a 是否比 b 新。
// 115 的事件 id 是 19 位数字，直接按字符串比会把 "9..." 判成大于 "10..."，
// 必须先比长度
func eventIDNewer(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// reachedCursor 事件是否已经落在游标之前（列表倒序，命中即可整轮停止）
func reachedCursor(ev lifeEvent, cur lifeCursor) bool {
	if cur.FromID != "" && !eventIDNewer(ev.ID, cur.FromID) {
		return true
	}
	if cur.FromTime > 0 {
		if ts, err := strconv.ParseInt(strings.TrimSpace(ev.Time), 10, 64); err == nil && ts < cur.FromTime {
			return true
		}
	}
	return false
}

// ==================== 通道选择与 405 降级 ====================

// lifeEndpointState proapi ⇄ webapi 的降级状态，持久化在 setting "life-endpoint"
type lifeEndpointState struct {
	Proapi405 int   `json:"proapi_405"` // 连续「proapi 405 而 webapi 正常」的次数
	WebUntil  int64 `json:"web_until"`  // 在此之前固定走 webapi（unix 秒）
}

// lifeFetcher 生活事件拉取器。load/save 注入以便单测，
// 生产由 realIncrDeps 用 setting 读写实现
type lifeFetcher struct {
	cookie string
	load   func(key string) string
	save   func(key, val string)
	// fetchPage 可替换的取页函数，测试注入桩；nil 时用真实现
	fetchPage func(app string, limit, offset int) ([]lifeEvent, int, error)
}

func (f *lifeFetcher) state() lifeEndpointState {
	var st lifeEndpointState
	if f.load != nil {
		_ = json.Unmarshal([]byte(f.load("life-endpoint")), &st)
	}
	return st
}

func (f *lifeFetcher) saveState(st lifeEndpointState) {
	if f.save != nil {
		f.save("life-endpoint", encodeEndpointState(st))
	}
}

// currentApp 当前该走哪条通道
func (f *lifeFetcher) currentApp() string {
	st := f.state()
	if st.WebUntil > 0 && time.Now().Unix() < st.WebUntil {
		return "web"
	}
	if st.WebUntil > 0 {
		st.WebUntil = 0
		f.saveState(st)
	}
	return "android"
}

// recordProapi405 记一次「proapi 405 而 webapi 正常」，连续 3 次就固定走 webapi 24h
func (f *lifeFetcher) recordProapi405() {
	st := f.state()
	st.Proapi405++
	if st.Proapi405 >= 3 {
		st = lifeEndpointState{WebUntil: time.Now().Add(lifeWebFallback).Unix()}
		log.Printf("[同步] ⚠ 事件接口连续 3 次被限流，接下来 24 小时改走备用通道")
	}
	f.saveState(st)
}

func (f *lifeFetcher) resetProapi405() {
	if st := f.state(); st.Proapi405 != 0 {
		st.Proapi405 = 0
		f.saveState(st)
	}
}

func (f *lifeFetcher) clearWebFallback() {
	if st := f.state(); st.WebUntil != 0 {
		st.WebUntil = 0
		f.saveState(st)
	}
}

// page 取一页，带 proapi ⇄ webapi 互为兜底。只有 405 才降级，其它错误照常抛
func (f *lifeFetcher) page(limit, offset int) ([]lifeEvent, int, error) {
	get := f.fetchPage
	if get == nil {
		get = func(app string, limit, offset int) ([]lifeEvent, int, error) {
			return fetchLifeEventsPage(f.cookie, app, limit, offset)
		}
	}
	app := f.currentApp()
	evs, count, err := get(app, limit, offset)
	if err == nil {
		if app == "android" {
			f.resetProapi405()
		}
		return evs, count, nil
	}
	if !isHTTPStatus(err, 405) {
		return nil, 0, err
	}
	if app == "web" {
		// webapi 自己 405 了：清掉粘滞，回 proapi 再试
		f.clearWebFallback()
		return get("android", limit, offset)
	}
	evs, count, werr := get("web", limit, offset)
	if werr != nil {
		return nil, 0, err // webapi 也不行，报原始的 405
	}
	f.recordProapi405()
	return evs, count, nil
}

// fetch 拉一轮：从最新往回翻到游标为止。
//
// 返回倒序（新 → 旧）的事件、推进后的游标。
// 已剔除浏览/标星类事件，并按 file_id 去重（同一文件只留最新那条）
func (f *lifeFetcher) fetch(cur lifeCursor, max int) ([]lifeEvent, lifeCursor, error) {
	if max <= 0 {
		max = 1000
	}
	// 有游标时首批拉小一点（日常一轮就几条），无游标是首次运行，一次拉满
	limit := 64
	if cur.isZero() {
		limit = 1000
	}

	out := make([]lifeEvent, 0, limit)
	seen := map[string]bool{}
	next := cur
	offset := 0

	for page := 0; page < lifeMaxPages; page++ {
		evs, count, err := f.page(limit, offset)
		if err != nil {
			return nil, cur, err
		}
		if len(evs) == 0 {
			break
		}
		// 游标推进到本轮最新那条（第一页的第一条）
		if page == 0 && evs[0].ID != "" {
			next.FromID = evs[0].ID
			if ts, e := strconv.ParseInt(strings.TrimSpace(evs[0].Time), 10, 64); e == nil {
				next.FromTime = ts
			}
		}
		for _, ev := range evs {
			if ev.ID == "" {
				continue
			}
			if reachedCursor(ev, cur) {
				return out, next, nil // 追平了，整轮结束
			}
			// 忽略类事件同样占去重位：它挡住的是同一文件更早的事件，
			// 而那条更早的事件本来也已经被更晚的状态取代了（p115client 同款）
			if ev.FileID != "" {
				if seen[ev.FileID] {
					continue
				}
				seen[ev.FileID] = true
			}
			if lifeIgnoreTypes[ev.Type] {
				continue
			}
			out = append(out, ev)
			if len(out) >= max {
				return out, next, nil
			}
		}
		offset += len(evs)
		if count > 0 && offset >= count {
			break
		}
		limit = 1000
		time.Sleep(lifeCooldown)
	}
	return out, next, nil
}

// fetchLifeEventsPage 取一页生活事件明细，返回条目与总数。
//
// ⚠️ data.count 两条通道类型不一样：proapi 返回字符串 "4"，webapi 返回数字 4。
// 声明成 int 会让 proapi 的整个响应解析失败（2026-09-19 实测，见 REFERENCES.md）
func fetchLifeEventsPage(cookie, app string, limit, offset int) ([]lifeEvent, int, error) {
	api := lifeAPIProapi
	if app == "web" {
		api = lifeAPIWeb
	}
	query := url.Values{
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	throttleLife()
	body, err := httpGet115(api, query, cookie, 20*time.Second)
	if err != nil {
		return nil, 0, err
	}
	var result struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Data  struct {
			Count json.RawMessage          `json:"count"`
			List  []map[string]interface{} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, 0, fmt.Errorf("解析生活事件失败: %s", truncateStr(string(body), 150))
	}
	if !result.State {
		msg := result.Error
		if msg == "" {
			msg = "state=false"
		}
		return nil, 0, fmt.Errorf("拉取生活事件被拒: %s", msg)
	}
	count, _ := strconv.Atoi(rawStr(result.Data.Count))
	events := make([]lifeEvent, 0, len(result.Data.List))
	for _, d := range result.Data.List {
		ev := lifeEvent{
			ID:       firstStr(d, "id"),
			Type:     normalizeEventType(fmt.Sprint(d["type"])),
			FileID:   firstStr(d, "file_id", "fid"),
			FileName: firstStr(d, "file_name", "n", "name"),
			Cid:      firstStr(d, "parent_id", "cid", "pid"),
			PickCode: firstStr(d, "pick_code", "pickcode", "pc"),
			Time:     firstStr(d, "update_time", "time", "create_time"),
		}
		if s, ok := d["file_size"].(float64); ok {
			ev.Size = int64(s)
		}
		events = append(events, ev)
	}
	return events, count, nil
}

// ==================== 「115 生活」开关门禁 ====================
//
// 用户若在 115 客户端里关掉了「生活」事件记录，behavior/detail 会一直返回空，
// 增量就此永久静默空转 —— 日志里连一行异常都没有。p115client 的 life_show、
// p115strmhelper、openStrm 都在启动前打开这个开关。

var (
	lifeGateMu    sync.Mutex
	lifeGateDone  bool
	lifeGateOK    bool
	lifeGateMsg   string
	lifeGateAt    time.Time
	lifeEmptyRuns int
)

// lifeGateStatus 门禁当前状态（供状态页展示）
func lifeGateStatus() (ok bool, msg string, at time.Time) {
	lifeGateMu.Lock()
	defer lifeGateMu.Unlock()
	return lifeGateOK, lifeGateMsg, lifeGateAt
}

// noteLifeRound 记一轮的结果；连续多轮零事件时把门禁标成待复检
func noteLifeRound(events int) {
	lifeGateMu.Lock()
	defer lifeGateMu.Unlock()
	if events > 0 {
		lifeEmptyRuns = 0
		return
	}
	lifeEmptyRuns++
	if lifeEmptyRuns >= lifeGateEmptyRuns {
		lifeEmptyRuns = 0
		lifeGateDone = false // 下一轮重新体检
	}
}

// ensureLifeGate 确保「115 生活」事件记录是开着的。
// 进程内只跑一次；连续多轮拉不到事件时会被 noteLifeRound 重新打开
//
// ⚠️ setoption 对失效 cookie 也返回成功，光看它判断不了「请重新登录」，
// 所以必须再真拉一条事件才算通过（openStrm 踩过这个坑）
func (f *lifeFetcher) ensureLifeGate() {
	lifeGateMu.Lock()
	if lifeGateDone {
		lifeGateMu.Unlock()
		return
	}
	lifeGateDone = true
	lifeGateMu.Unlock()

	ok, msg := true, "已连接"
	if err := enable115Life(f.cookie); err != nil {
		ok, msg = false, fmt.Sprintf("开关打开失败：%v", err)
	} else if _, _, err := f.page(1, 0); err != nil {
		ok, msg = false, fmt.Sprintf("事件流不可用：%v", err)
	}

	lifeGateMu.Lock()
	lifeGateOK, lifeGateMsg, lifeGateAt = ok, msg, time.Now()
	lifeGateMu.Unlock()

	if !ok {
		log.Printf("[同步] ⚠ 网盘事件流不可用（%s）。增量同步会一直没有内容可处理，"+
			"请到「账号与媒体库」确认 115 登录状态", msg)
	}
}

// enable115Life 打开「115 生活」事件记录开关。
//
// POST https://life.115.com/api/1.0/web/1.0/calendar/setoption  form: locus=1&open_life=1
// ⚠️ 响应结构是 {state, code, message, data}，不是 webapi 惯用的 {state, errNo, error}
func enable115Life(cookie string) error {
	form := url.Values{"locus": {"1"}, "open_life": {"1"}}
	body, err := httpPostForm115("https://life.115.com/api/1.0/web/1.0/calendar/setoption", form, cookie, 15*time.Second)
	if err != nil {
		return err
	}
	var r struct {
		State   bool   `json:"state"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("响应无法解析: %s", truncateStr(string(body), 120))
	}
	if !r.State || r.Code != 0 {
		return fmt.Errorf("被拒: code=%d %s", r.Code, r.Message)
	}
	return nil
}

// encodeEndpointState 序列化降级状态（测试预置状态时也用它）
func encodeEndpointState(st lifeEndpointState) string {
	b, _ := json.Marshal(st)
	return string(b)
}
