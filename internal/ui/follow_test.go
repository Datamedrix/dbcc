package ui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"dbcc/internal/exec"
	"dbcc/internal/ui/grid"
)

// TestCloseStopsFollowing: closing the tab of a following result (tail
// -f) stops its polling and waits for it, even while a poll is running.
func TestCloseStopsFollowing(t *testing.T) {
	th := NewTheme(256)
	tabs := NewResultTabs(th, NewMessageLog(th))
	var polls, inPoll atomic.Int32
	r := exec.NewResult("logs", []exec.ColumnInfo{{Name: "id"}}, []exec.Row{{"1"}})
	err := r.SetFollow(exec.FollowSpec{SQL: "unused", Key: "id", Interval: time.Millisecond,
		Conn: func(ctx context.Context) (exec.Querier, error) {
			polls.Add(1)
			inPoll.Add(1)
			defer inPoll.Add(-1)
			<-ctx.Done() // a poll that runs until it is cancelled
			return nil, ctx.Err()
		}})
	if err != nil {
		t.Fatal(err)
	}
	tabs.AddResult(r, grid.Options{})
	r.StartFollow()
	deadline := time.Now().Add(5 * time.Second)
	for polls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no poll")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { tabs.Close(0); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the tab hangs")
	}
	if r.Following() || inPoll.Load() != 0 {
		t.Errorf("following %v, polls running %d after close", r.Following(), inPoll.Load())
	}
	n := polls.Load()
	time.Sleep(20 * time.Millisecond)
	if polls.Load() != n {
		t.Errorf("polled after close: %d -> %d", n, polls.Load())
	}
}
