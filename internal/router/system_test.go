package router

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrialRequiresContinuousHealthAndIndependentHeartbeats(t *testing.T) {
	var checks, pulses atomic.Int32
	var bad time.Time
	start := time.Now()
	c, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	e := confirmWindow(c, confirmation{window: 40 * time.Millisecond, interval: 10 * time.Millisecond, heartbeat: 2 * time.Millisecond,
		check: func(context.Context) error {
			if checks.Add(1) == 3 {
				bad = time.Now()
				return errors.New("LAN path lost")
			}
			return nil
		}, beat: func(context.Context) error { pulses.Add(1); return nil }})
	if e != nil || bad.IsZero() || time.Since(bad) < 40*time.Millisecond || pulses.Load() <= checks.Load() || time.Since(start) > time.Second {
		t.Fatal(e, checks.Load(), pulses.Load())
	}
}
func TestTrialCancellationAndFailedFirstRead(t *testing.T) {
	c, end := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer end()
	down := errors.New("core offline")
	check := func(context.Context) error { return down }
	beat := func(context.Context) error { return down }
	// One failed check, then the deadline before the next: the timeout keeps what failed.
	err := confirmWindow(c, confirmation{window: time.Second, interval: time.Second, heartbeat: time.Millisecond, check: check, beat: beat})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, down) {
		t.Fatal("missing timeout or last failed check", err)
	}
	if !errors.Is(confirmWindow(context.Background(), confirmation{interval: time.Second, heartbeat: time.Second, check: check, beat: beat}), down) {
		t.Fatal("active launch hid failed health")
	}
}

func TestSlowInitialFullProbeKeepsHeartbeatingBeforeWindowBegins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var count atomic.Int32
	pulsed := make(chan struct{})
	beat := func(context.Context) error {
		if count.Add(1) == 3 {
			close(pulsed)
		}
		return nil
	}
	var first time.Time
	full := func(ctx context.Context) error {
		if first.IsZero() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-pulsed:
				first = time.Now()
			}
		}
		return nil
	}
	window := 25 * time.Millisecond
	if err := confirmWindow(ctx, confirmation{window: window, interval: 4 * time.Millisecond, heartbeat: 2 * time.Millisecond, check: full, beat: beat}); err != nil {
		t.Fatal(err)
	}
	if first.IsZero() || time.Since(first) < window {
		t.Fatal("incomplete health window")
	}
}

func TestLostLocalHeartbeatRestartsFullHealthWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var checks int
	requestFailure := make(chan struct{}, 1)
	failedAt := make(chan time.Time, 1)
	var failure time.Time
	full := func(ctx context.Context) error {
		checks++
		if checks == 3 {
			requestFailure <- struct{}{}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case failure = <-failedAt:
			}
		}
		return nil
	}
	beat := func(context.Context) error {
		select {
		case <-requestFailure:
			failedAt <- time.Now()
			return errors.New("local control unavailable")
		default:
			return nil
		}
	}
	window := 35 * time.Millisecond
	if err := confirmWindow(ctx, confirmation{window: window, interval: 6 * time.Millisecond, heartbeat: time.Millisecond, check: full, beat: beat}); err != nil {
		t.Fatal(err)
	}
	if failure.IsZero() || time.Since(failure) < window {
		t.Fatal("kernel confirmed without a new continuous window")
	}
}

// The core exiting fails the confirmation at once, between checks or during one, instead of
// leaving it to retry until the start deadline.
func TestConfirmationFailsAtOnceWhenTheCoreExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	healthy := func(context.Context) error { return nil }
	run := func(name string, exited chan struct{}, check func(context.Context) error) {
		start := time.Now()
		err := confirmWindow(ctx, confirmation{window: time.Second, interval: 5 * time.Millisecond, heartbeat: time.Millisecond,
			check: check, beat: healthy, exited: exited})
		if !errors.Is(err, errCoreExited) || time.Since(start) > 500*time.Millisecond {
			t.Errorf("%s: %v after %v", name, err, time.Since(start))
		}
	}

	gone := make(chan struct{})
	close(gone)
	var checks atomic.Int32
	run("before", gone, func(context.Context) error { checks.Add(1); return nil })
	if checks.Load() != 0 {
		t.Error("checked a core that had already exited")
	}

	between := make(chan struct{})
	time.AfterFunc(20*time.Millisecond, func() { close(between) })
	run("between checks", between, healthy)

	// The second check waits on the core until the exit cancels it.
	during := make(chan struct{})
	var calls atomic.Int32
	run("during a check", during, func(ctx context.Context) error {
		if calls.Add(1) == 2 {
			close(during)
			<-ctx.Done()
			return errors.New("local core control unavailable")
		}
		return nil
	})
}

// A takeover that stays broken fails at the third failed check, in a row or not, with what
// failed, instead of at the start deadline.
func TestConfirmationFailsWithinTheBoundWhenChecksKeepFailing(t *testing.T) {
	lost := errors.New("network verify failed at managed_lan_probe")
	healthy := func(context.Context) error { return nil }
	for name, fails := range map[string]func(n int32) bool{
		"persistent": func(n int32) bool { return n > 1 }, // healthy once, then broken
		"flapping":   func(n int32) bool { return n%2 == 0 },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var checks, failed atomic.Int32
		check := func(context.Context) error {
			if fails(checks.Add(1)) {
				failed.Add(1)
				return lost
			}
			return nil
		}
		start := time.Now()
		err := confirmWindow(ctx, confirmation{window: time.Second, interval: 2 * time.Millisecond, heartbeat: time.Millisecond,
			check: check, beat: healthy})
		cancel()
		if !errors.Is(err, lost) || errors.Is(err, context.DeadlineExceeded) || failed.Load() != confirmFailures || time.Since(start) > 500*time.Millisecond {
			t.Errorf("%s: %v after %d failed checks in %v", name, err, failed.Load(), time.Since(start))
		}
	}

	// Heartbeats that keep failing break every streak the checks start.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	control := errors.New("local core control unavailable")
	start := time.Now()
	err := confirmWindow(ctx, confirmation{window: time.Second, interval: 5 * time.Millisecond, heartbeat: time.Millisecond,
		check: healthy, beat: func(context.Context) error { return control }})
	if !errors.Is(err, control) || time.Since(start) > 500*time.Millisecond {
		t.Errorf("heartbeat: %v after %v", err, time.Since(start))
	}
}

// Failed checks short of the bound only restart the streak, and the countdown with it.
func TestConfirmationRecoversFromTransientFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	window := 30 * time.Millisecond
	var checks int
	var failedAt time.Time
	check := func(context.Context) error {
		// Two in a row, one short of the bound: dnsmasq restarting.
		if checks++; checks == 2 || checks == 3 {
			failedAt = time.Now()
			return errors.New("local core DNS answer unavailable")
		}
		return nil
	}
	var until []time.Time
	start := time.Now()
	err := confirmWindow(ctx, confirmation{window: window, interval: 2 * time.Millisecond, heartbeat: time.Millisecond,
		check: check, beat: func(context.Context) error { return nil }, until: func(t time.Time) { until = append(until, t) }})
	done := time.Now()
	if err != nil || failedAt.IsZero() || done.Sub(failedAt) < window {
		t.Fatal("confirmed without a new continuous window", err)
	}
	if len(until) < 2 || until[0].Before(start.Add(window)) || until[0].After(start.Add(window+100*time.Millisecond)) {
		t.Fatal("countdown did not start at the window", until)
	}
	if last := until[len(until)-1]; last.Before(failedAt.Add(window)) || last.After(done) {
		t.Fatal("countdown did not restart with the streak", until, failedAt, done)
	}
}

func TestStartupReadinessKeepsLastCompletedProbeOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	dns := errors.New("local core DNS answer unavailable")
	first := true
	ready := func(ctx context.Context) error {
		if first {
			first = false
			return dns
		}
		<-ctx.Done()
		return errors.New("control query canceled during shutdown")
	}
	if err := waitCoreReady(ctx, make(chan struct{}), time.Millisecond, ready); !errors.Is(err, dns) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing startup timeout or underlying DNS failure", err)
	}

}
func TestInterfacePrefixesUseRouterAddressIncludingDelegatedIPv6(t *testing.T) {
	var n netifStatus
	n.Up = true
	n.Device = "br-guest.20"
	n.V4 = []netifAddress{{"192.168.5.1", 24}}
	n.Delegated = append(n.Delegated, struct {
		Local netifAddress `json:"local-address"`
	}{netifAddress{"fd00:20::1", 64}})
	p, e := interfacePrefixes(n)
	if e != nil || len(p) != 2 || p[0].String() != "192.168.5.1/24" || p[1].String() != "fd00:20::1/64" {
		t.Fatal(p, e)
	}
	n.Up = false
	if _, e = interfacePrefixes(n); e == nil {
		t.Fatal("accepted offline LAN")
	}
}
