package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

// The tests stay off the network: no subscription fetches subconv's routing template.
func TestMain(m *testing.M) {
	subscription.TemplateHosts = nil
	os.Exit(m.Run())
}

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
	st := subscription.Status{Kind: "external", UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}
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
	st := subscription.Status{Kind: "external", Ready: true}
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
	src := subscription.Status{Kind: "external", NeedsRefresh: true}
	if !dueForRefresh(src, now, time.Time{}, 0) || dueForRefresh(src, now, now.Add(-time.Minute), 0) {
		t.Fatal("migration must refresh a remote snapshot with bounded retries")
	}
	src.Kind = "file"
	if dueForRefresh(src, now, time.Time{}, 0) {
		t.Fatal("local file needs manual reimport")
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
	src := subscription.Status{Kind: "external", Ready: true, UpdatedAt: now.Add(-48 * time.Hour).Format(time.RFC3339), Attempts: 3, AttemptedAt: now.Add(-50 * time.Minute).Format(time.RFC3339)}
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

// A link whose first import failed on the way, as when it is added while the router has no
// internet, is retried with the back-off until it imports; one whose failure a retry cannot
// change waits for the administrator.
func TestFailedFirstImportRetriesOnlyTransientFailures(t *testing.T) {
	now := time.Now().UTC()
	at := now.Format(time.RFC3339)
	for _, code := range []string{"subscription_request_failed", "subscription_http_500", "subscription_http_502", "subscription_http_503", "subscription_http_429", "subscription_http_408",
		"provider_timeout", "provider_dns_failed", "provider_connection_refused", "provider_unreachable", "provider_incomplete", "provider_http_504", "provider_http_429"} {
		for _, kind := range []string{"external", "platform"} {
			src := subscription.Status{Kind: kind, LastFailure: code, Attempts: 1, AttemptedAt: at}
			for _, interval := range []time.Duration{0, 24 * time.Hour} {
				if dueForRefresh(src, now.Add(14*time.Minute), time.Time{}, interval) || !dueForRefresh(src, now.Add(15*time.Minute), time.Time{}, interval) {
					t.Fatal("transient failure not retried after its back-off", kind, code, interval)
				}
			}
			// Back-off doubles with each attempt; the in-process spacing still applies.
			src.Attempts = 3
			if dueForRefresh(src, now.Add(59*time.Minute), time.Time{}, 0) || !dueForRefresh(src, now.Add(time.Hour), time.Time{}, 0) || dueForRefresh(src, now.Add(time.Hour), now.Add(50*time.Minute), 0) {
				t.Fatal("retry ignored the back-off", kind, code)
			}
		}
	}
	for _, code := range []string{"", "update_failed", "subscription_http_400", "subscription_http_403", "subscription_http_404", "subscription_http_410", "subscription_http_5000", "subscription_http_x",
		"subscription_tls_untrusted", "subscription_tls_not_yet_valid", "subscription_web_page", "subscription_message", "subscription_empty", "unsupported_format", "no_usable_nodes",
		"nonstandard_snell_credential", "invalid_credential", "subscription_link_unavailable", "invalid_plan_detail", "provider_http_404", "provider_tls_untrusted", "private_provider_address",
		"provider_redirect_refused", "provider_download_failed", "provider_size_limit", "invalid_proxy_provider", "provider_cancelled"} {
		src := subscription.Status{Kind: "external", LastFailure: code, Attempts: 1, AttemptedAt: now.Add(-7 * time.Hour).Format(time.RFC3339)}
		if dueForRefresh(src, now, time.Time{}, time.Hour) {
			t.Fatal("retried a failure a retry cannot change", code)
		}
	}
	// Sources removed, needing a login, suspended or read from a file are never retried.
	for _, src := range []subscription.Status{
		{Kind: "external", Revoked: true},
		{Kind: "platform", LoginRequired: true},
		{Kind: "platform", Suspended: true},
		{Kind: "file"},
	} {
		src.LastFailure = "subscription_request_failed"
		if dueForRefresh(src, now, time.Time{}, time.Hour) {
			t.Fatalf("retried %+v", src)
		}
	}
}

// Through the store: an offline first import records a transient failure that the scheduler
// retries with a growing back-off, and the retry that reaches the subscription imports it. A
// permanent answer stops the retries.
func TestOfflineFirstImportCompletesBySchedule(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	src := subscription.Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	offline := true
	status := http.StatusOK
	body := "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['MATCH,DIRECT']"
	f := subscription.Fetcher{Client: &http.Client{Transport: feedTransport(func(*http.Request) (*http.Response, error) {
		if offline {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
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
	// The administrator adds the link while the router has no internet.
	if _, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{}); err == nil || err.Error() != "subscription_request_failed" {
		t.Fatal(err)
	}
	st, err := store.Status(src.ID)
	if err != nil || st.LastFailure != "subscription_request_failed" {
		t.Fatal(err, st.LastFailure)
	}
	due(st, 1)
	// The first scheduled retry is still offline: the next one waits twice as long.
	if _, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{Scheduled: true}); err == nil {
		t.Fatal("offline refresh succeeded")
	}
	st, _ = store.Status(src.ID)
	due(st, 2)
	// The provider is overloaded, then the subscription arrives.
	offline, status = false, http.StatusServiceUnavailable
	if _, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{Scheduled: true}); err == nil {
		t.Fatal("503 imported")
	}
	st, _ = store.Status(src.ID)
	if st.LastFailure != "subscription_http_503" {
		t.Fatal(st.LastFailure)
	}
	due(st, 3)
	status = http.StatusOK
	c, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{Scheduled: true})
	if err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	st, _ = store.Status(src.ID)
	if !st.Ready || st.LastFailure != "" || st.Attempts != 0 || st.PendingUpdate != nil {
		t.Fatalf("%+v", st)
	}
	// A link the provider no longer serves is not retried until the administrator acts.
	other := subscription.Source{ID: "gone", Name: "gone", Kind: "external", URL: "https://example.com/gone"}
	if err := store.Configure(other); err != nil {
		t.Fatal(err)
	}
	status = http.StatusNotFound
	if _, err := f.Refresh(t.Context(), store, other.ID, nil, subscription.Policy{}); err == nil {
		t.Fatal("404 imported")
	}
	st, _ = store.Status(other.ID)
	if st.LastFailure != "subscription_http_404" || dueForRefresh(st, time.Now().Add(7*time.Hour), time.Time{}, 0) {
		t.Fatal("permanent failure retried", st.LastFailure)
	}
}

// stalledBody is a download that stops coming until the request gives up.
type stalledBody struct{ ctx context.Context }

func (b stalledBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }

// A first import that breaks off or times out in the middle of the download failed on the way,
// as one that never connected: the scheduler retries it. A redirect the router refuses is the
// server's answer, which a retry gets again: it waits for the administrator. So does a plan's
// issued link that its download host refuses, unlike one the host could not serve just then.
func TestInterruptedFirstImportRetriesAndRefusedRedirectWaits(t *testing.T) {
	for _, code := range []string{"subscription_incomplete", "subscription_link_unreachable"} {
		if !transientFailure(code) {
			t.Fatal("not retried", code)
		}
	}
	for _, code := range []string{"subscription_redirect_refused", "subscription_link_unavailable", "invalid_response_size"} {
		if transientFailure(code) {
			t.Fatal("retried", code)
		}
	}
	const body = "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['MATCH,DIRECT']"
	ok := func(r io.Reader) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(r)}
	}
	for _, tc := range []struct {
		id, failure string
		retried     bool
		answer      func(*http.Request) *http.Response
	}{
		{"cut-off", "subscription_incomplete", true, func(*http.Request) *http.Response {
			return ok(io.MultiReader(strings.NewReader(body[:20]), iotest.ErrReader(io.ErrUnexpectedEOF)))
		}},
		{"timed-out", "subscription_incomplete", true, func(r *http.Request) *http.Response { return ok(stalledBody{r.Context()}) }},
		{"redirected", "subscription_redirect_refused", false, func(*http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://example.com/feed"}}, Body: http.NoBody}
		}},
	} {
		store := subscription.OpenStore(t.TempDir())
		src := subscription.Source{ID: tc.id, Name: tc.id, Kind: "external", URL: "https://example.com/feed"}
		if err := store.Configure(src); err != nil {
			t.Fatal(err)
		}
		f := subscription.Fetcher{Client: &http.Client{Timeout: 200 * time.Millisecond, Transport: feedTransport(func(r *http.Request) (*http.Response, error) {
			return tc.answer(r), nil
		})}}
		if _, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{}); err == nil || err.Error() != tc.failure {
			t.Fatal(tc.id, err)
		}
		st, err := store.Status(src.ID)
		if err != nil || st.Ready || st.LastFailure != tc.failure {
			t.Fatal(tc.id, err, st.Ready, st.LastFailure)
		}
		at, err := time.Parse(time.RFC3339, st.AttemptedAt)
		if err != nil {
			t.Fatal(err)
		}
		if dueForRefresh(st, at.Add(7*time.Hour), time.Time{}, 0) != tc.retried {
			t.Fatal(tc.id, "retried:", !tc.retried)
		}
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
	src := subscription.Status{Kind: "external", Ready: true, UpdatedAt: now.Add(time.Hour * 24).Format(time.RFC3339)}
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

type feedTransport func(*http.Request) (*http.Response, error)

func (f feedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A scheduled refresh whose update waits for the administrator is not a success: the source
// keeps its back-off, so the scheduler does not download it again every 15 minutes, and the
// log tells the task apart from a done refresh.
func TestPendingUpdateKeepsBackoff(t *testing.T) {
	store := subscription.OpenStore(t.TempDir())
	src := subscription.Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body := "proxies: [{name: Edge, type: http, server: edge.example, port: 8080}]\nrules: ['DOMAIN,ads.example,REJECT', 'MATCH,DIRECT']"
	downloads := 0
	f := subscription.Fetcher{Client: &http.Client{Transport: feedTransport(func(*http.Request) (*http.Response, error) {
		downloads++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	c, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{})
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
		c, err := f.Refresh(t.Context(), store, src.ID, nil, subscription.Policy{Scheduled: true})
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
