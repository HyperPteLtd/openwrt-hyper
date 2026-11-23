package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func protectReferences(raw json.RawMessage, i router.Intent) (json.RawMessage, error) {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return nil, errors.New("invalid refresh request")
	}
	var source string
	if json.Unmarshal(args["source"], &source) != nil || source == "" {
		return nil, errors.New("source required")
	}
	ids := map[string]bool{}
	protect := func(id string) {
		if strings.HasPrefix(id, "node:") || strings.HasPrefix(id, "group:") {
			ids[id] = true
		}
	}
	// Saved selections go apart: one in a group the update removes resets (Assess).
	if choices := i.Choices[source]; len(choices) > 0 {
		args["choices"], _ = json.Marshal(choices)
	}
	for _, g := range i.Policy.Groups {
		for _, r := range g.Members {
			if r.Source == source {
				protect(r.ID)
			}
		}
	}
	if t := i.Policy.Telegram; t != nil && t.Node.Source == source {
		protect(t.Node.ID)
	}
	if i.Source == source {
		for _, d := range i.Policy.Devices {
			protect(d.Target)
		}
		for _, r := range i.Policy.Rules {
			if r.Enabled == nil || *r.Enabled {
				protect(r.Target)
			}
		}
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	slices.Sort(values)
	args["active_ids"], _ = json.Marshal(values)
	return json.Marshal(args)
}

// A scheduled refresh can be preempted before or during a download. Wait for
// its helper to exit before a manual refresh claims a new subscription ticket.
type refreshWork struct {
	mu       sync.Mutex
	bySource map[string]*refreshSlot
}

type refreshSlot struct {
	users           int
	cancelScheduled context.CancelFunc
	scheduledDone   chan struct{}
}

func (w *refreshWork) acquire(parent context.Context, source string, scheduled bool) (context.Context, <-chan struct{}, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bySource == nil {
		w.bySource = make(map[string]*refreshSlot)
	}
	slot := w.bySource[source]
	if slot != nil && scheduled {
		return nil, nil, nil, errors.New("source already refreshing")
	}
	if slot == nil {
		slot = &refreshSlot{}
		w.bySource[source] = slot
	}
	ctx, cancel := context.WithCancel(parent)
	var wait <-chan struct{}
	if scheduled {
		slot.cancelScheduled = cancel
		slot.scheduledDone = make(chan struct{})
	} else if slot.cancelScheduled != nil {
		slot.cancelScheduled()
		wait = slot.scheduledDone
	}
	slot.users++
	release := func() {
		cancel()
		w.mu.Lock()
		defer w.mu.Unlock()
		if scheduled {
			close(slot.scheduledDone)
			slot.scheduledDone = nil
			slot.cancelScheduled = nil
		}
		slot.users--
		if slot.users == 0 {
			delete(w.bySource, source)
		}
	}
	return ctx, wait, release, nil
}

func (s *app) runRefresh(body json.RawMessage, scheduled bool) (any, error) {
	var args struct{ Source string }
	if json.Unmarshal(body, &args) != nil || args.Source == "" {
		return nil, errors.New("source required")
	}
	lifetime, wait, release, err := s.refreshes.acquire(s.ctx, args.Source, scheduled)
	if err != nil {
		return nil, err
	}
	job, err := s.runJob("", "subscription_write/refresh", func(ctx context.Context) (json.RawMessage, error) {
		defer release()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return child(ctx, "hyper-subscription", []string{"refresh"}, body)
	})
	if err != nil {
		release()
	}
	return job, err
}

// heldUpdate returns the code of the update a refresh downloaded but did not apply
// (subscription.PendingUpdate), so that the log does not report it as done.
func heldUpdate(action string, data json.RawMessage) string {
	var v struct {
		Pending *subscription.PendingUpdate `json:"pending_update"`
	}
	if action != "subscription_write/refresh" || json.Unmarshal(data, &v) != nil || v.Pending == nil {
		return ""
	}
	switch v.Pending.Code {
	case subscription.PendingConfirmation, subscription.PendingBlocked:
		return v.Pending.Code
	}
	return ""
}

// retryDelay spaces out a refresh that keeps failing, or keeps finding an update it cannot
// apply by itself: 15 minutes, doubling up to 6 hours or the update interval if that is
// shorter. Each attempt rewrites the source's status on flash.
func retryDelay(attempts int, interval time.Duration) time.Duration {
	limit := 6 * time.Hour
	if interval > 0 {
		limit = min(limit, interval)
	}
	return min(15*time.Minute<<min(max(attempts-1, 0), 5), limit)
}

// transientFailure reports a refresh that failed on the way to the Hyper account's service
// rather than on what came back (hypersync): no answer or an answer of 408, 429 or 5xx (the
// *_unreachable codes), or every country failing. A later attempt can succeed with nothing
// changed on the router. Credentials, other HTTP answers, content the router cannot use and
// certificates (left to the clock retry) are not transient.
func transientFailure(code string) bool {
	switch code {
	case "subscription_token_unreachable", "locations_unreachable", "nodes_unreachable":
		return true
	}
	return false
}

// dueForRefresh reports whether the scheduler reads a source again: only the Hyper account's
// is read, never one a sign-out revoked, needing a sign-in or suspended.
func dueForRefresh(src subscription.Status, now, attempt time.Time, interval time.Duration) bool {
	if src.Kind != subscription.HyperSource || src.LoginRequired || src.Revoked || src.Suspended || now.Sub(attempt) < 15*time.Minute {
		return false
	}
	// The record's start date also covers manual attempts and daemon restarts. A date after
	// now (the clock was set back) leaves only the in-process spacing above.
	if last, err := time.Parse(time.RFC3339, src.AttemptedAt); err == nil && src.Attempts > 0 && !last.After(now) && now.Sub(last) < retryDelay(src.Attempts, interval) {
		return false
	}
	if src.NeedsRefresh {
		return true
	}
	// A source that never became ready, such as one signed in to while the service could not be
	// reached, completes its first read once it can be, even with automatic updates off: the
	// administrator asked for it. A source never read waits for its first read.
	if !src.Ready {
		return transientFailure(src.LastFailure)
	}
	if interval <= 0 {
		return false
	}
	updated, err := time.Parse(time.RFC3339, src.UpdatedAt)
	// Persisted timestamps are wall clock dates; an in-process attempt also
	// carries a monotonic reading so a clock step cannot bypass its backoff.
	// A ready source without a date, its status lost, is due: the refresh dates it.
	return err != nil || updated.After(now) || now.Sub(updated) >= interval
}

func recoveredClock(src subscription.Status, now time.Time) bool {
	return src.RetryAfterClock && !src.Revoked && !src.Suspended && !src.LoginRequired &&
		src.Kind == subscription.HyperSource && src.LastTLS.Recheck(now)
}

func (s *app) schedule(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	attempt := map[string]time.Time{}
	previous := time.Now()
	// A helper that observed the old clock can still be finishing its RPC
	// when NTP fires. Include its result on the next minute ticks.
	drainUntil := previous.Add(2 * time.Minute)
	select {
	case s.clockEvents <- struct{}{}:
	default:
	}
	for {
		var now time.Time
		periodic := false
		select {
		case <-ctx.Done():
			return
		case <-s.clockEvents:
			now = time.Now()
			drainUntil = now.Add(2 * time.Minute)
		case now = <-tick.C:
			periodic = true
			// An alternative NTP client may not emit OpenWrt hotplug events.
			delta := now.Round(0).Sub(previous.Round(0)) - now.Sub(previous)
			if delta.Abs() >= time.Second {
				drainUntil = now.Add(2 * time.Minute)
			}
		}
		previous = now
		if ctx.Err() != nil {
			return
		}
		s.retryBootstrapClock(now)
		var interval time.Duration
		if periodic {
			if p, err := router.ReadPreferences(ctx); err == nil {
				interval = time.Duration(p.UpdateHours) * time.Hour
			}
		}
		list, err := subscription.OpenStore(router.Root + "/sources").List()
		if err != nil {
			continue
		}
		clockJobs := 0
		for _, src := range list {
			clock := !now.After(drainUntil) && recoveredClock(src, now)
			if !clock && !dueForRefresh(src, now, attempt[src.Source], interval) {
				continue
			}
			args := map[string]any{"source": src.Source, "scheduled": true}
			if clock {
				if clockJobs >= 2 {
					drainUntil = now.Add(2 * time.Minute)
					continue
				}
				args["clock_retry_generation"] = src.Generation
			}
			body, _ := json.Marshal(args)
			_, err := s.handle(ctx, "subscription_write", request{Action: "refresh", Args: body, scheduled: true})
			if err == nil {
				attempt[src.Source] = now
				if clock {
					clockJobs++
				}
			}
			// Ordinary refreshes keep their existing one-source-per-minute limit.
			if !clock && err == nil {
				break
			}
		}
	}
}

// refreshHyper reads the signed-in Hyper account again, keeping the router's selections as a
// requested refresh does.
func (s *app) refreshHyper() {
	body, _ := json.Marshal(map[string]string{"source": subscription.HyperSource})
	guarded, err := protectReferences(body, s.m.Status().Intent)
	if err != nil {
		return
	}
	_, _ = s.runRefresh(guarded, false)
}

// adoptHyper gives a signed-in router without the account's source that source, with the
// server lists 0.x had switched off (disabled, from migrateLegacy).
func (s *app) adoptHyper(disabled []string) {
	body, _ := json.Marshal(map[string]any{"disabled": disabled})
	out, err := child(s.ctx, "hyper-subscription", []string{"adopt"}, body)
	var res struct {
		OK   bool `json:"ok"`
		Data struct {
			Adopted bool `json:"adopted"`
		} `json:"data"`
	}
	if err == nil && json.Unmarshal(out, &res) == nil && res.OK && res.Data.Adopted {
		s.refreshHyper()
	}
}
