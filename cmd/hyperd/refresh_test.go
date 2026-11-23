package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

const hyper = subscription.HyperSource

// Saved selections reach the refresh apart from the references it must keep, so that a
// selection of a group the update removes resets instead of blocking it.
func TestRefreshKeepsReferencesAndPassesSelections(t *testing.T) {
	i := router.Intent{Source: "s", Choices: map[string]map[string]string{"s": {"group:old": "node:a", router.Global: "node:b"}}}
	i.Policy.Devices = []router.DeviceRule{{Address: "192.168.1.2/32", Target: "group:dev"}}
	raw, err := protectReferences(json.RawMessage(`{"source":"s"}`), i)
	if err != nil {
		t.Fatal(err)
	}
	var args struct {
		ActiveIDs []string          `json:"active_ids"`
		Choices   map[string]string `json:"choices"`
	}
	if json.Unmarshal(raw, &args) != nil || len(args.ActiveIDs) != 1 || args.ActiveIDs[0] != "group:dev" || args.Choices["group:old"] != "node:a" || args.Choices[router.Global] != "node:b" {
		t.Fatal(string(raw))
	}
}

func TestScheduledRefreshWaitsForFirstManualImport(t *testing.T) {
	now := time.Now().UTC()
	st := subscription.Status{Kind: hyper, UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	if dueForRefresh(st, now, time.Time{}, time.Hour) {
		t.Fatal("scheduled an unimported source")
	}
	st.Ready = true
	if !dueForRefresh(st, now, time.Time{}, time.Hour) {
		t.Fatal("skipped a previously imported, due source")
	}
	st.UpdatedAt = now.Format(time.RFC3339)
	if dueForRefresh(st, now, time.Time{}, time.Hour) {
		t.Fatal("scheduled a recently updated source")
	}
}

// A ready source without an update date, as an update interrupted before its status file left
// one in an earlier version, is due with automatic updates on, after the usual spacing, so that
// the refresh dates it again.
func TestReadySourceWithoutUpdateDateIsDue(t *testing.T) {
	now := time.Now().UTC()
	st := subscription.Status{Kind: hyper, Ready: true}
	if !dueForRefresh(st, now, time.Time{}, 6*time.Hour) {
		t.Fatal("ready source without a date never refreshed")
	}
	if dueForRefresh(st, now, now.Add(-time.Minute), 6*time.Hour) || dueForRefresh(st, now, time.Time{}, 0) {
		t.Fatal("refreshed within the spacing, or with automatic updates off")
	}
	st.Attempts, st.AttemptedAt = 1, now.Add(-time.Minute).Format(time.RFC3339)
	if dueForRefresh(st, now, time.Time{}, 6*time.Hour) || !dueForRefresh(st, now.Add(14*time.Minute), time.Time{}, 6*time.Hour) {
		t.Fatal("back-off of the attempt")
	}
}

func TestMigrateRemoteSourceEvenWithAutomaticUpdatesDisabled(t *testing.T) {
	now := time.Now()
	src := subscription.Status{Kind: hyper, NeedsRefresh: true}
	if !dueForRefresh(src, now, time.Time{}, 0) || dueForRefresh(src, now, now.Add(-time.Minute), 0) {
		t.Fatal("migration must refresh a remote snapshot with bounded retries")
	}
	// Only the Hyper account's source is read on schedule, not one of a kind the router no
	// longer reads.
	for _, kind := range []string{"file", "external", subscription.FixtureSource} {
		src.Kind = kind
		if dueForRefresh(src, now, time.Time{}, 0) {
			t.Fatal("scheduled a source of kind", kind)
		}
	}
}

func TestFailingRefreshBacksOff(t *testing.T) {
	for attempts, want := range []time.Duration{0: 15 * time.Minute, 1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 4: 2 * time.Hour, 5: 4 * time.Hour, 6: 6 * time.Hour, 7: 6 * time.Hour} {
		if got := retryDelay(attempts, 24*time.Hour); got != want {
			t.Fatal(attempts, got)
		}
	}
	if retryDelay(100, 0) != 6*time.Hour || retryDelay(4, time.Hour) != time.Hour || retryDelay(1, time.Hour) != 15*time.Minute {
		t.Fatal("cap must be six hours or the update interval")
	}
	now := time.Now().UTC()
	src := subscription.Status{Kind: hyper, Ready: true, UpdatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339), Attempts: 3, AttemptedAt: now.Add(-50 * time.Minute).Format(time.RFC3339)}
	if dueForRefresh(src, now, time.Time{}, 24*time.Hour) {
		t.Fatal("third failure retried before an hour")
	}
	if !dueForRefresh(src, now.Add(11*time.Minute), time.Time{}, 24*time.Hour) {
		t.Fatal("backoff never ends")
	}
	src.NeedsRefresh, src.Ready = true, false
	if dueForRefresh(src, now, time.Time{}, 0) {
		t.Fatal("migration retries skip the backoff")
	}
	// A start date after now means the clock was set back: only the in-process spacing applies.
	src.AttemptedAt = now.Add(time.Hour).Format(time.RFC3339)
	if !dueForRefresh(src, now, time.Time{}, 0) || dueForRefresh(src, now, now.Add(-time.Minute), 0) {
		t.Fatal("future start date not handled")
	}
	src.Attempts, src.AttemptedAt = 0, ""
	if !dueForRefresh(src, now, now.Add(-16*time.Minute), 0) {
		t.Fatal("successful source delayed")
	}
}

// A first read that failed on the way to the account's service, as when the router signs in
// while it has no internet, is retried with the back-off until it succeeds; one whose failure
// a retry cannot change waits for the administrator.
func TestFailedFirstImportRetriesOnlyTransientFailures(t *testing.T) {
	now := time.Now().UTC()
	at := now.Format(time.RFC3339)
	for _, code := range []string{"subscription_token_unreachable", "locations_unreachable", "nodes",
		"subscription_token_http_500", "subscription_token_http_502", "subscription_token_http_503", "subscription_token_http_429", "subscription_token_http_408",
		"locations_http_504", "locations_http_429"} {
		src := subscription.Status{Kind: hyper, LastFailure: code, Attempts: 1, AttemptedAt: at}
		for _, interval := range []time.Duration{0, 24 * time.Hour} {
			if dueForRefresh(src, now.Add(14*time.Minute), time.Time{}, interval) || !dueForRefresh(src, now.Add(15*time.Minute), time.Time{}, interval) {
				t.Fatal("transient failure not retried after its back-off", code, interval)
			}
		}
		// Back-off doubles with each attempt; the in-process spacing still applies.
		src.Attempts = 3
		if dueForRefresh(src, now.Add(59*time.Minute), time.Time{}, 0) || !dueForRefresh(src, now.Add(time.Hour), time.Time{}, 0) || dueForRefresh(src, now.Add(time.Hour), now.Add(50*time.Minute), 0) {
			t.Fatal("retry ignored the back-off", code)
		}
	}
	for _, code := range []string{"", "update_failed", "subscription_token_http_400", "subscription_token_http_403", "subscription_token_http_404", "locations_http_410",
		"subscription_token_http_5000", "locations_http_x", "subscription_tls_untrusted", "no_usable_nodes", "no_countries", "decrypt", "invalid_source_kind"} {
		src := subscription.Status{Kind: hyper, LastFailure: code, Attempts: 1, AttemptedAt: now.Add(-7 * time.Hour).Format(time.RFC3339)}
		if dueForRefresh(src, now, time.Time{}, time.Hour) {
			t.Fatal("retried a failure a retry cannot change", code)
		}
	}
	// Sources signed out, needing a sign-in or suspended are never retried.
	for _, src := range []subscription.Status{
		{Kind: hyper, Revoked: true},
		{Kind: hyper, LoginRequired: true},
		{Kind: hyper, Suspended: true},
	} {
		src.LastFailure = "locations_unreachable"
		if dueForRefresh(src, now, time.Time{}, time.Hour) {
			t.Fatalf("retried %+v", src)
		}
	}
}

// Through the store: an offline first read records a transient failure that the scheduler
// retries with a growing back-off, and the retry that reaches the service completes it. A
// permanent answer stops the retries. The store's source is a test fixture, which the
// scheduler takes for the Hyper account's.
func TestOfflineFirstImportCompletesBySchedule(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	var failure error = errors.New("subscription_token_unreachable")
	body := "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['MATCH,DIRECT']"
	serveFixtures(t, func(id string) (string, error) {
		if id == "gone" {
			return "", errors.New("subscription_token_http_404")
		}
		return body, failure
	})
	for _, id := range []string{"sub", "gone"} {
		if err := store.Configure(subscription.Source{ID: id, Name: id, Kind: subscription.FixtureSource}); err != nil {
			t.Fatal(err)
		}
	}
	f := subscription.Fetcher{}
	status := func(id string) subscription.Status {
		t.Helper()
		st, err := store.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		st.Kind = hyper
		return st
	}
	due := func(st subscription.Status, attempts int) {
		t.Helper()
		at, err := time.Parse(time.RFC3339, st.AttemptedAt)
		if err != nil || st.Ready || st.Attempts != attempts {
			t.Fatal(err, st.Ready, st.Attempts)
		}
		wait := retryDelay(attempts, 0)
		if dueForRefresh(st, at.Add(wait-time.Minute), time.Time{}, 0) || !dueForRefresh(st, at.Add(wait), time.Time{}, 0) {
			t.Fatal("not retried after its back-off", st.LastFailure, attempts)
		}
	}
	// The administrator signs in while the router has no internet.
	if _, err := f.Refresh(t.Context(), store, "sub", subscription.Policy{}); err == nil || err.Error() != "subscription_token_unreachable" {
		t.Fatal(err)
	}
	st := status("sub")
	if st.LastFailure != "subscription_token_unreachable" {
		t.Fatal(st.LastFailure)
	}
	due(st, 1)
	// The first scheduled retry is still offline: the next one waits twice as long.
	if _, err := f.Refresh(t.Context(), store, "sub", subscription.Policy{Scheduled: true}); err == nil {
		t.Fatal("offline refresh succeeded")
	}
	due(status("sub"), 2)
	// The service is overloaded, then the subscription arrives.
	failure = errors.New("locations_http_503")
	if _, err := f.Refresh(t.Context(), store, "sub", subscription.Policy{Scheduled: true}); err == nil {
		t.Fatal("503 imported")
	}
	st = status("sub")
	if st.LastFailure != "locations_http_503" {
		t.Fatal(st.LastFailure)
	}
	due(st, 3)
	failure = nil
	c, err := f.Refresh(t.Context(), store, "sub", subscription.Policy{Scheduled: true})
	if err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	if st := status("sub"); !st.Ready || st.LastFailure != "" || st.Attempts != 0 || st.PendingUpdate != nil {
		t.Fatalf("%+v", st)
	}
	// A subscription the service no longer serves is not retried until the administrator acts.
	if _, err := f.Refresh(t.Context(), store, "gone", subscription.Policy{}); err == nil {
		t.Fatal("404 imported")
	}
	if st := status("gone"); st.LastFailure != "subscription_token_http_404" || dueForRefresh(st, time.Now().Add(7*time.Hour), time.Time{}, 0) {
		t.Fatal("permanent failure retried", st.LastFailure)
	}
}

func TestManualRefreshPreemptsOnlySameSource(t *testing.T) {
	var work refreshWork
	parent := context.Background()
	scheduled, _, finishScheduled, err := work.acquire(parent, "a", true)
	if err != nil {
		t.Fatal(err)
	}
	manual, wait, finishManual, err := work.acquire(parent, "a", false)
	if err != nil || wait == nil || scheduled.Err() == nil || manual.Err() != nil {
		t.Fatal("manual refresh did not preempt the same source")
	}
	if _, _, _, err = work.acquire(parent, "a", true); err == nil {
		t.Fatal("scheduler superseded active work")
	}
	other, waitOther, finishOther, err := work.acquire(parent, "b", true)
	if err != nil || waitOther != nil || other.Err() != nil {
		t.Fatal("unrelated source blocked")
	}
	finishOther()
	select {
	case <-wait:
		t.Fatal("new helper could run before the previous helper exited")
	default:
	}
	finishScheduled()
	select {
	case <-wait:
	default:
		t.Fatal("manual refresh still waiting for a completed helper")
	}
	if _, _, _, err = work.acquire(parent, "a", true); err == nil {
		t.Fatal("scheduled refresh superseded manual refresh")
	}
	finishManual()
	if len(work.bySource) != 0 {
		t.Fatal("completed state retained")
	}
}

func TestClockChangeRepairsFutureScheduleWithoutFlooding(t *testing.T) {
	now := time.Now()
	src := subscription.Status{Kind: hyper, Ready: true, UpdatedAt: now.Add(time.Hour * 24).Format(time.RFC3339)}
	if !dueForRefresh(src, now, time.Time{}, time.Hour) || dueForRefresh(src, now, now.Add(-time.Minute), time.Hour) || dueForRefresh(src, now, time.Time{}, 0) {
		t.Fatal("clock rollback should allow one refresh respecting backoff and settings")
	}
	src.RetryAfterClock = true
	src.Ready = false
	src.LastTLS = &tlsdiag.Diagnostic{Reason: "not_yet_valid", CheckedAt: now.Add(-time.Hour * 24), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	if !recoveredClock(src, now) || recoveredClock(src, now.Add(-24*time.Hour)) {
		t.Fatal("invalid initial-import retry")
	}
	src.LoginRequired = true
	if recoveredClock(src, now) {
		t.Fatal("retried after login expired")
	}
}

// A scheduled refresh whose update waits for the administrator is not a success: the source
// keeps its back-off, so the scheduler does not download it again every 15 minutes, and the
// log tells the task apart from a done refresh.
func TestPendingUpdateKeepsBackoff(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	src := subscription.Source{ID: "sub", Name: "sub", Kind: subscription.FixtureSource}
	body := "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,ads.example,REJECT', 'MATCH,DIRECT']"
	downloads := 0
	serveFixtures(t, func(string) (string, error) { downloads++; return body, nil })
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	f := subscription.Fetcher{}
	c, err := f.Refresh(t.Context(), store, src.ID, subscription.Policy{})
	if err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	if data, _ := json.Marshal(map[string]any{"decision": c.Decision}); heldUpdate("subscription_write/refresh", data) != "" {
		t.Fatal("applied update logged as held")
	}
	// Without its REJECT rule the update needs the administrator's confirmation.
	body = strings.Replace(body, "'DOMAIN,ads.example,REJECT'", "'DOMAIN,media.example,DIRECT'", 1)
	const interval = 24 * time.Hour
	for attempt := 1; attempt <= 3; attempt++ {
		c, err := f.Refresh(t.Context(), store, src.ID, subscription.Policy{Scheduled: true})
		if err != nil || c.Decision != "confirmation_required" {
			t.Fatal(err, c.Decision)
		}
		// The data the subscription helper returns for the task.
		data, _ := json.Marshal(map[string]any{"decision": c.Decision, "pending_update": c.Pending()})
		if code := heldUpdate("subscription_write/refresh", data); code != subscription.PendingConfirmation {
			t.Fatalf("logged as %q", code)
		}
		st, err := store.Status(src.ID)
		if err != nil || st.PendingUpdate == nil || st.PendingUpdate.Code != subscription.PendingConfirmation || st.Attempts != attempt {
			t.Fatal(err, st.PendingUpdate, st.Attempts)
		}
		st.Kind = hyper // the scheduler reads the Hyper account's source only
		at, err := time.Parse(time.RFC3339, st.AttemptedAt)
		if err != nil {
			t.Fatal(err)
		}
		// The update interval has long passed: only the back-off holds the next download.
		st.UpdatedAt = at.Add(-2 * interval).Format(time.RFC3339)
		wait := retryDelay(attempt, interval)
		if dueForRefresh(st, at.Add(wait-time.Minute), time.Time{}, interval) || !dueForRefresh(st, at.Add(wait), time.Time{}, interval) {
			t.Fatal("pending update ignored the back-off", attempt)
		}
	}
	if downloads != 4 {
		t.Fatal(downloads)
	}
	for _, data := range []string{`[]`, `{"pending_update":{"code":"something_else"}}`, ``} {
		if heldUpdate("subscription_write/refresh", json.RawMessage(data)) != "" {
			t.Fatal(data)
		}
	}
	if heldUpdate("kernel/install-json", json.RawMessage(`{"pending_update":{"code":"update_blocked"}}`)) != "" {
		t.Fatal("other task logged as held")
	}
}
