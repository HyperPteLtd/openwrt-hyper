package subscription

import (
	"context"
	"errors"
	"testing"
)

func TestCanceledRefreshPreservesSnapshotAndDiagnostic(t *testing.T) {
	store := OpenStore(t.TempDir())
	if err := store.Configure(Source{ID: "one", Kind: "file", Name: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Fetcher{}).Refresh(t.Context(), store, "one", []byte("http://127.0.0.1:8080#old"), Policy{}); err != nil {
		t.Fatal(err)
	}
	for _, early := range []bool{true, false} {
		ctx, cancel := context.WithCancel(t.Context())
		if early {
			cancel()
		}
		f := Fetcher{BeforeCommit: func(context.Context, Profile) (func() error, error) {
			return func() error { cancel(); return nil }, nil
		}}
		c, err := f.Refresh(ctx, store, "one", []byte("http://127.0.0.1:8081#new"), Policy{})
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
