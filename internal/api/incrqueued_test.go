package api

import (
	"strings"
	"testing"
	"time"
)

func TestIncrQueuedRoundsNote(t *testing.T) {
	h := &incrRoundHist{}
	enq := time.Now()
	h.note(enq.Add(-time.Minute), &incrSummary{Round: 27, EventsPending: 3, Consumed: true, StrmCreated: 9}, "")
	h.note(enq.Add(2*time.Second), &incrSummary{Round: 29, EventsPending: 4, Consumed: true, StrmCreated: 1}, "")

	got := incrQueuedRoundsNote(h.busySince(enq), 30)
	if !strings.Contains(got, "#29") || strings.Contains(got, "#27") || !strings.Contains(got, "新增 STRM 1") {
		t.Fatalf("回报不对: %q", got)
	}
	if s := incrQueuedRoundsNote(h.busySince(enq.Add(time.Hour)), 30); s != "" {
		t.Fatalf("排队期间没有轮次时不该补: %q", s)
	}
}
