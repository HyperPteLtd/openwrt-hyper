package subscription

import (
	"context"
	"errors"
	"os"
	"testing"
)

// The scheduler spaces out retries by the attempts since the last success. A failure
// repeated unchanged leaves the status as begin wrote it: one write per attempt.
func TestAttemptsSinceSuccess(t *testing.T) {
	store, fd := fixtureSource(t, "sub", basic)
	var afterBegin os.FileInfo
	fd.onRead = func(context.Context) { afterBegin, _ = os.Stat(store.statusPath("sub")) }
	fd.reachable = false
	f := Fetcher{}
	refresh := func(scheduled bool) Status {
		t.Helper()
		_, err := f.Refresh(t.Context(), store, "sub", Policy{Scheduled: scheduled})
		if (err == nil) != fd.reachable {
			t.Fatal(err)
		}
		st, err := store.Status("sub")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := refresh(true); st.Attempts != 1 || st.AttemptedAt == "" || st.LastFailure != "locations_unreachable" {
		t.Fatal(st.Attempts, st.AttemptedAt, st.LastFailure)
	}
	st := refresh(true)
	if now, _ := os.Stat(store.statusPath("sub")); st.Attempts != 2 || !os.SameFile(afterBegin, now) {
		t.Fatal("repeated failure rewrote the status", st.Attempts)
	}
	// An administrator's refresh starts over.
	if st := refresh(false); st.Attempts != 1 {
		t.Fatal(st.Attempts)
	}
	refresh(true)
	if err := store.Configure(Source{ID: "sub", Name: "renamed", Kind: FixtureSource}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Status("sub"); st.Attempts != 0 || st.AttemptedAt != "" || st.Name != "renamed" {
		t.Fatal("changed settings kept the backoff", st.Attempts)
	}
	refresh(true)
	fd.reachable = true
	if st := refresh(true); st.Attempts != 0 || st.AttemptedAt != "" || st.LastFailure != "" || !st.Ready {
		t.Fatal(st.Attempts, st.LastFailure)
	}
}

// A refresh cancelled before or during its commit leaves the approved profile and the last
// diagnostic as they were.
func TestCanceledRefreshPreservesSnapshotAndDiagnostic(t *testing.T) {
	store, fd := fixtureSource(t, "one", "http://127.0.0.1:8080#old")
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "one", Policy{}); err != nil {
		t.Fatal(err)
	}
	fd.body = "http://127.0.0.1:8081#new"
	for _, early := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		if early {
			cancel()
		}
		f := Fetcher{BeforeCommit: func(context.Context, Profile) (func() error, error) {
			return func() error { cancel(); return nil }, nil
		}}
		c, err := f.Refresh(ctx, store, "one", Policy{})
		cancel()
		if !errors.Is(err, context.Canceled) || c.Decision == "ready" {
			t.Fatalf("early=%t: got %s, %v", early, c.Decision, err)
		}
		r, err := store.raw("one")
		if err != nil || r.LastFailure != "" || r.Current.Nodes[0].Name != "old" || r.Previous != nil {
			t.Fatal("cancellation changed the approved profile or diagnostic", err)
		}
	}
}

// A read that needs the account again or finds the subscription suspended marks the source,
// which keeps its profile; the next sign-in renews it.
func TestLoginAndSuspensionMarkTheSource(t *testing.T) {
	store, fd := fixtureSource(t, "sub", basic)
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "sub", Policy{}); err != nil {
		t.Fatal(err)
	}
	fd.reachable, fd.failure = false, ErrLoginRequired
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "sub", Policy{}); !errors.Is(err, ErrLoginRequired) {
		t.Fatal(err)
	}
	if st, _ := store.Status("sub"); !st.LoginRequired || !st.Ready {
		t.Fatal("login requirement", st)
	}
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "sub", Policy{}); !errors.Is(err, ErrLoginRequired) {
		t.Fatal("refresh began without a sign-in", err)
	}
	if err := store.configure(Source{ID: "sub", Name: "sub", Kind: FixtureSource}, true); err != nil {
		t.Fatal(err)
	}
	fd.failure = ErrSuspended
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "sub", Policy{}); !errors.Is(err, ErrSuspended) {
		t.Fatal(err)
	}
	if _, err := store.Load("sub"); !errors.Is(err, ErrSuspended) {
		t.Fatal("suspended source loaded", err)
	}
	fd.reachable = true
	if c, err := (Fetcher{}).Refresh(t.Context(), store, "sub", Policy{}); err != nil || c.Decision != "ready" {
		t.Fatal(err)
	}
	if st, _ := store.Status("sub"); st.Suspended || st.LoginRequired || !st.Ready {
		t.Fatal("resumed source", st)
	}
}
