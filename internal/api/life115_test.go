package api

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// fakeLifeAPI 假的 behavior/detail：按 app 分别给一份事件列表，可注入错误
type fakeLifeAPI struct {
	events map[string][]lifeEvent // app → 全量事件（倒序：新 → 旧）
	errs   map[string]error       // app → 固定返回的错误
	calls  []string               // 每次调用记一条 "app:limit:offset"
}

func (f *fakeLifeAPI) page(app string, limit, offset int) ([]lifeEvent, int, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s:%d:%d", app, limit, offset))
	if err := f.errs[app]; err != nil {
		return nil, 0, err
	}
	all := f.events[app]
	if offset >= len(all) {
		return nil, len(all), nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], len(all), nil
}

// newFetcher 造一个走内存 setting 的 fetcher
// noCooldown 测试里不真等翻页冷却
func noCooldown(t *testing.T) {
	t.Helper()
	prev := lifeCooldown
	lifeCooldown = 0
	t.Cleanup(func() { lifeCooldown = prev })
}

func newFetcher(api *fakeLifeAPI) (*lifeFetcher, map[string]string) {
	store := map[string]string{}
	f := &lifeFetcher{
		load:      func(k string) string { return store[k] },
		save:      func(k, v string) { store[k] = v },
		fetchPage: api.page,
	}
	return f, store
}

// ev 造一条事件。id 越大越新
func ev(id int64, typ, fileID string) lifeEvent {
	return lifeEvent{
		ID: strconv.FormatInt(id, 10), Type: typ, FileID: fileID,
		FileName: fileID + ".mkv", Cid: "d1", Time: strconv.FormatInt(id, 10),
	}
}

// ==================== 游标 ====================

// 19 位事件 id 按字符串直接比会把 "9..." 判成大于 "10..."，必须先比长度
func TestEventIDNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"3521409246350542500", "3521409246350542499", true},
		{"3521409246350542499", "3521409246350542500", false},
		{"10000000000000000000", "9999999999999999999", true}, // 位数不同，长的更新
		{"9999999999999999999", "10000000000000000000", false},
		{"100", "100", false}, // 相等不算更新
	}
	for _, c := range cases {
		if got := eventIDNewer(c.a, c.b); got != c.want {
			t.Fatalf("eventIDNewer(%s, %s) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// 游标命中就整轮停：这是「日常一轮 1 次请求」的全部依据
func TestFetchStopsAtCursor(t *testing.T) {
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": {
		ev(105, evUpload, "f5"),
		ev(104, evUpload, "f4"),
		ev(103, evUpload, "f3"), // 游标在这里
		ev(102, evUpload, "f2"),
		ev(101, evUpload, "f1"),
	}}}
	f, _ := newFetcher(api)

	out, next, err := f.fetch(lifeCursor{FromID: "103"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("游标之后只有 2 条新事件，实得 %d", len(out))
	}
	if out[0].ID != "105" || out[1].ID != "104" {
		t.Fatalf("返回的应是 105/104，实得 %s/%s", out[0].ID, out[1].ID)
	}
	if next.FromID != "105" {
		t.Fatalf("游标应推进到最新那条 105，实得 %s", next.FromID)
	}
	if len(api.calls) != 1 {
		t.Fatalf("追平只需 1 次请求，实得 %d 次：%v", len(api.calls), api.calls)
	}
}

// 有游标首批 64、无游标首批 1000（p115client 同款）
func TestFetchFirstBatchSize(t *testing.T) {
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": {ev(100, evUpload, "f1")}}}
	f, _ := newFetcher(api)

	f.fetch(lifeCursor{FromID: "1"}, 1000)
	if api.calls[0] != "android:64:0" {
		t.Fatalf("有游标首批应为 64，实得 %s", api.calls[0])
	}

	api.calls = nil
	f.fetch(lifeCursor{}, 1000)
	if api.calls[0] != "android:1000:0" {
		t.Fatalf("无游标首批应为 1000，实得 %s", api.calls[0])
	}
}

// 没有游标、也没追平时要继续翻页，第二页起 limit 提到 1000
func TestFetchPaginates(t *testing.T) {
	noCooldown(t)
	var all []lifeEvent
	for i := 200; i > 100; i-- { // 100 条，每条不同 file_id
		all = append(all, ev(int64(i), evUpload, fmt.Sprintf("f%d", i)))
	}
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": all}}
	f, _ := newFetcher(api)

	out, _, err := f.fetch(lifeCursor{FromID: "1"}, 1000) // 游标很旧，追不平
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 100 {
		t.Fatalf("应拿到全部 100 条，实得 %d", len(out))
	}
	if len(api.calls) != 2 {
		t.Fatalf("64 + 36 应为 2 次请求，实得 %d 次：%v", len(api.calls), api.calls)
	}
	if api.calls[1] != "android:1000:64" {
		t.Fatalf("第二页 limit 应提到 1000，实得 %s", api.calls[1])
	}
}

// 单轮上限：拿够就停，别把一整年的历史都灌进来
func TestFetchRespectsMax(t *testing.T) {
	var all []lifeEvent
	for i := 200; i > 100; i-- {
		all = append(all, ev(int64(i), evUpload, fmt.Sprintf("f%d", i)))
	}
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": all}}
	f, _ := newFetcher(api)

	out, _, _ := f.fetch(lifeCursor{FromID: "1"}, 10)
	if len(out) != 10 {
		t.Fatalf("上限 10，实得 %d", len(out))
	}
}

// ==================== 过滤与去重 ====================

// 浏览/标星类事件入库前就丢掉：活跃账号里它们占绝对多数
func TestFetchDropsIgnoredTypes(t *testing.T) {
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": {
		ev(105, "8", "f5"),      // browse_video
		ev(104, evUpload, "f4"), // 要
		ev(103, "star_file", "f3"),
		ev(102, "19", "f2"),     // folder_label
		ev(101, evDelete, "f1"), // 要
	}}}
	f, _ := newFetcher(api)

	out, _, _ := f.fetch(lifeCursor{FromID: "1"}, 1000)
	if len(out) != 2 {
		t.Fatalf("只有 2 条有意义的事件，实得 %d", len(out))
	}
	if out[0].FileID != "f4" || out[1].FileID != "f1" {
		t.Fatalf("留下的应是 f4/f1，实得 %s/%s", out[0].FileID, out[1].FileID)
	}
}

// 同一个 file_id 只留最新那条：上传完马上改名的文件没必要处理两遍
func TestFetchYieldsLatestPerFile(t *testing.T) {
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": {
		ev(103, evRename, "f1"), // 最新
		ev(102, evMove, "f1"),
		ev(101, evUpload, "f1"),
	}}}
	f, _ := newFetcher(api)

	out, _, _ := f.fetch(lifeCursor{FromID: "1"}, 1000)
	if len(out) != 1 {
		t.Fatalf("同一文件只该留 1 条，实得 %d", len(out))
	}
	if out[0].Type != evRename {
		t.Fatalf("留下的应是最新的改名事件，实得 %s", out[0].Type)
	}
}

// 忽略类事件同样占去重位：它挡住的那条更早的事件，本来也已经被更晚的状态取代了
func TestIgnoredEventStillOccupiesDedupeSlot(t *testing.T) {
	api := &fakeLifeAPI{events: map[string][]lifeEvent{"android": {
		ev(102, "8", "f1"),      // browse_video，最新
		ev(101, evUpload, "f1"), // 更早的上传
	}}}
	f, _ := newFetcher(api)

	out, _, _ := f.fetch(lifeCursor{FromID: "1"}, 1000)
	if len(out) != 0 {
		t.Fatalf("最新状态是「浏览」，这个文件本轮不该产出事件，实得 %d 条", len(out))
	}
}

// ==================== 405 降级 ====================

type statusErr struct{ code int }

func (e *statusErr) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

func TestFallbackToWebOn405(t *testing.T) {
	api := &fakeLifeAPI{
		events: map[string][]lifeEvent{"web": {ev(100, evUpload, "f1")}},
		errs:   map[string]error{"android": &httpStatusError{Code: 405}},
	}
	f, store := newFetcher(api)

	out, _, err := f.fetch(lifeCursor{FromID: "1"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应从 webapi 拿到 1 条，实得 %d", len(out))
	}
	if store["life-endpoint"] == "" {
		t.Fatal("应记下一次 proapi 405")
	}
}

// 连续 3 次「proapi 405 而 webapi 正常」→ 24h 固定走 webapi，不再每轮白试一次
func TestStickyWebFallbackAfterThree405(t *testing.T) {
	api := &fakeLifeAPI{
		events: map[string][]lifeEvent{"web": {ev(100, evUpload, "f1")}},
		errs:   map[string]error{"android": &httpStatusError{Code: 405}},
	}
	f, _ := newFetcher(api)

	for i := 0; i < 3; i++ {
		f.fetch(lifeCursor{FromID: "1"}, 1000)
	}
	if got := f.currentApp(); got != "web" {
		t.Fatalf("连续 3 次 405 后应固定走 web，实得 %s", got)
	}

	api.calls = nil
	f.fetch(lifeCursor{FromID: "1"}, 1000)
	for _, c := range api.calls {
		if len(c) >= 7 && c[:7] == "android" {
			t.Fatalf("粘滞期内不该再试 proapi：%v", api.calls)
		}
	}
}

// 非 405 的错误照常抛，不能当成风控去降级 —— cookie 失效降级多少次都没用
func TestNon405ErrorNotFallback(t *testing.T) {
	api := &fakeLifeAPI{
		events: map[string][]lifeEvent{"web": {ev(100, evUpload, "f1")}},
		errs:   map[string]error{"android": &statusErr{code: 500}},
	}
	f, _ := newFetcher(api)

	if _, _, err := f.fetch(lifeCursor{FromID: "1"}, 1000); err == nil {
		t.Fatal("非 405 错误应原样抛出，而不是降级后当成功")
	}
	for _, c := range api.calls {
		if len(c) >= 3 && c[:3] == "web" {
			t.Fatalf("非 405 不该走 web：%v", api.calls)
		}
	}
}

// webapi 粘滞期内它自己也 405 了：清掉粘滞回 proapi，别把自己锁死在坏通道上
func TestWebFallbackRecoversWhenWebAlso405(t *testing.T) {
	api := &fakeLifeAPI{
		events: map[string][]lifeEvent{"android": {ev(100, evUpload, "f1")}},
		errs:   map[string]error{"web": &httpStatusError{Code: 405}},
	}
	f, store := newFetcher(api)
	store["life-endpoint"] = encodeEndpointState(lifeEndpointState{
		WebUntil: time.Now().Add(time.Hour).Unix(),
	})

	out, _, err := f.fetch(lifeCursor{FromID: "1"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("应回退到 proapi 拿到 1 条，实得 %d", len(out))
	}
	if f.currentApp() != "android" {
		t.Fatal("web 也 405 了，粘滞应被清掉")
	}
}

// ==================== 游标序列化 ====================

func TestCursorRoundTrip(t *testing.T) {
	c := lifeCursor{FromID: "3521409246350542500", FromTime: 1789800694}
	got := parseLifeCursor(encodeLifeCursor(c))
	if got != c {
		t.Fatalf("往返不一致: %+v", got)
	}
	// 坏数据按「无游标」处理，而不是崩掉或用一个乱七八糟的值
	if !parseLifeCursor("不是 json").isZero() {
		t.Fatal("解析不出的游标应视为空")
	}
	if !parseLifeCursor("").isZero() {
		t.Fatal("空串应视为空游标")
	}
}
