package subscription

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The scheduler spaces out retries by the attempts since the last success. A failure
// repeated unchanged leaves the status as begin wrote it: one write per attempt.
func TestAttemptsSinceSuccess(t *testing.T) {
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	var afterBegin os.FileInfo
	ok := false
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		afterBegin, _ = os.Stat(store.statusPath(src.ID))
		if ok {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(basic))}, nil
		}
		return nil, errReset
	})}}
	refresh := func(scheduled bool) Status {
		t.Helper()
		_, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{Scheduled: scheduled})
		if (err == nil) != ok {
			t.Fatal(err)
		}
		st, err := store.Status(src.ID)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := refresh(true); st.Attempts != 1 || st.AttemptedAt == "" || st.LastFailure != "subscription_request_failed" {
		t.Fatal(st.Attempts, st.AttemptedAt, st.LastFailure)
	}
	st := refresh(true)
	if now, _ := os.Stat(store.statusPath(src.ID)); st.Attempts != 2 || !os.SameFile(afterBegin, now) {
		t.Fatal("repeated failure rewrote the status", st.Attempts)
	}
	// An administrator's refresh starts over.
	if st := refresh(false); st.Attempts != 1 {
		t.Fatal(st.Attempts)
	}
	refresh(true)
	if err := store.Preferences(src.ID, Preferences{UserAgent: "sing-box"}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Status(src.ID); st.Attempts != 0 || st.AttemptedAt != "" {
		t.Fatal("changed settings kept the backoff", st.Attempts)
	}
	refresh(true)
	ok = true
	if st := refresh(true); st.Attempts != 0 || st.AttemptedAt != "" || st.LastFailure != "" || !st.Ready {
		t.Fatal(st.Attempts, st.LastFailure)
	}
}
