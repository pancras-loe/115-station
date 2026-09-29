package api

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// 拒收日志：同类一分钟一行，压下的次数并进下一行；不同类互不影响
func TestWebhookRejectLogThrottle(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	defer func() {
		webhookReject.Lock()
		webhookReject.last = map[string]time.Time{}
		webhookReject.suppressed = map[string]int{}
		webhookReject.Unlock()
	}()

	for i := 0; i < 5; i++ {
		webhookRejectLog("token", "拒收 token")
	}
	webhookRejectLog("json", "拒收 json")
	if got := strings.Count(buf.String(), "拒收 token"); got != 1 {
		t.Fatalf("同类一分钟内应只打一行，实际 %d 行:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "拒收 json") {
		t.Fatalf("不同类的拒收不应被压掉:\n%s", buf.String())
	}

	// 窗口过去后再打一行，带上被压下的 4 次
	webhookReject.Lock()
	webhookReject.last["token"] = time.Now().Add(-webhookRejectEvery)
	webhookReject.Unlock()
	buf.Reset()
	webhookRejectLog("token", "拒收 token")
	if !strings.Contains(buf.String(), "另有 4 次未记录") {
		t.Fatalf("应报出被压下的次数:\n%s", buf.String())
	}
}
