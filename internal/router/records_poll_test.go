package router

import (
	"context"
	"testing"
	"time"
)

func TestConnectionPollRunsFastOnlyWhileTheRecordsAreRead(t *testing.T) {
	r := NewRecords()
	ctx, end := context.WithTimeout(context.Background(), 3*time.Second)
	defer end()
	// Idle: the poll waits for the slow interval, and a new reader cuts it short.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_ = r.pollWait(ctx)
		done <- time.Since(start)
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case d := <-done:
		t.Fatal("idle poll returned early", d)
	default:
	}
	r.Viewed()
	if d := <-done; d >= viewedPoll {
		t.Fatal("a new reader did not wake the idle poll", d)
	}
	// A reader already being served queues no further wake-ups; the poll runs every second.
	r.Viewed()
	start := time.Now()
	if e := r.pollWait(ctx); e != nil {
		t.Fatal(e)
	}
	if d := time.Since(start); d < viewedPoll-100*time.Millisecond || d >= idlePoll {
		t.Fatal("viewed poll interval", d)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if r.pollWait(cancelled) == nil {
		t.Fatal("a cancelled poll kept waiting")
	}
}
