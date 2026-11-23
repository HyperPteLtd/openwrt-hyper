package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/router"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

func TestFirstInstallMarkerIsPrivateAndConsumedOnce(t *testing.T) {
	dir := t.TempDir()
	pending := filepath.Join(dir, ".first-install-pending")
	for _, mode := range []os.FileMode{0644, 0600} {
		if err := os.WriteFile(pending, nil, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(pending, mode); err != nil {
			t.Fatal(err)
		}
		claimed, err := claimFirstInstall(dir)
		if mode == 0644 && (claimed || err == nil) || mode == 0600 && (!claimed || err != nil) {
			t.Fatal(mode, claimed, err)
		}
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := firstInstallPending(dir); ready || err != nil {
		t.Fatal("stale pending triggered work", ready, err)
	}
	dir = t.TempDir()
	if err := os.Symlink(pending, filepath.Join(dir, ".first-install-pending")); err != nil {
		t.Fatal(err)
	}
	if ready, err := firstInstallPending(dir); ready || err == nil {
		t.Fatal("symbolic marker accepted", ready, err)
	}
}

func TestFirstKernelRetriesBoundedAndYieldsToManual(t *testing.T) {
	delays := []time.Duration{0, 0}
	permanent := errors.New("release checksum mismatch")
	for _, tc := range []struct {
		errs  []error
		count int
		final error
	}{
		{[]error{kernel.ErrTemporaryDownload, kernel.ErrTemporaryDownload, nil}, 3, nil},
		{[]error{kernel.ErrTemporaryDownload, kernel.ErrTemporaryDownload, kernel.ErrTemporaryDownload}, 3, kernel.ErrTemporaryDownload},
		{[]error{permanent}, 1, permanent},
		{[]error{publicKernelError(kernel.ErrReleaseRateLimit.Error())}, 1, kernel.ErrReleaseRateLimit},
		{[]error{context.Canceled}, 1, context.Canceled},
	} {
		calls := 0
		_, err := retryFirstKernel(context.Background(), delays, func(context.Context) (json.RawMessage, error) { e := tc.errs[calls]; calls++; return nil, e })
		if calls != tc.count || !errors.Is(err, tc.final) {
			t.Fatal(calls, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, running := make(chan struct{}), make(chan struct{})
	s := app{bootstrapCancel: cancel, bootstrapDone: done}
	go func() {
		defer close(done)
		_, err := retryFirstKernel(ctx, []time.Duration{time.Hour}, func(context.Context) (json.RawMessage, error) {
			close(running)
			return nil, kernel.ErrTemporaryDownload
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("auto worker did not yield: %v", err)
		}
	}()
	<-running
	manual, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := s.yieldFirstKernel(manual); err != nil {
		t.Fatal(err)
	}
}

func TestFirstKernelUsesLatestSubscriptionAndNeverStarts(t *testing.T) {
	const tag = "v2.4.1"
	for _, tc := range []struct {
		name, wantReason string
		change, reject   bool
		selectErr        error
	}{
		{name: "source added during download"},
		{name: "intent changed during check", change: true, wantReason: "selection_changed"},
		{name: "incompatible source", reject: true, wantReason: "preflight_failed"},
		{name: "user chose another version", selectErr: errKernelRevision, wantReason: "selection_changed"},
		{name: "storage failure", selectErr: errors.New("disk full")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)
			if _, err := m.Change(nil, func(i *router.Intent) error { i.Source = "new-during-download"; return nil }); err != nil {
				t.Fatal(err)
			}
			commits := 0
			out, err := selectCheckedKernel(context.Background(), m, tag, func(i router.Intent) error {
				if i.Source != "new-during-download" {
					t.Fatalf("checked stale source %q", i.Source)
				}
				if tc.change {
					_, err := m.Change(nil, func(i *router.Intent) error { i.Mode = "global"; return nil })
					return err
				}
				if tc.reject {
					return errors.New("unsupported option")
				}
				return nil
			}, func() error { commits++; return tc.selectErr })
			if tc.selectErr != nil && tc.wantReason == "" {
				if !errors.Is(err, tc.selectErr) {
					t.Fatal(out, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var result map[string]string
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatal(err)
			}
			if result["reason"] != tc.wantReason || m.Status().Intent.Enabled || commits != 0 && (tc.change || tc.reject) {
				t.Fatal(result, commits, m.Status())
			}
			if tc.wantReason != "" || tc.selectErr != nil {
				if result["staged"] != tag {
					t.Fatal(result)
				}
			} else if result["selected"] != tag || commits != 1 {
				t.Fatal(result, commits)
			}
		})
	}
}

func TestPersistentBootstrapResultContainsOnlyPublicState(t *testing.T) {
	const mixed = `{"selected":"v2.4.1","reason":"https://secret.example/","token":"hidden","skipped":"hidden"}`
	if safe := string(publicBootstrapResult(json.RawMessage(mixed))); safe != `{"selected":"v2.4.1"}` {
		t.Fatal(safe)
	}
	if safe := publicBootstrapResult(json.RawMessage(`{"staged":"not a version","other":"secret"}`)); len(safe) != 0 {
		t.Fatal(string(safe))
	}
}

func TestBootstrapNeverSelectsWhenServiceStartedDuringDownload(t *testing.T) {
	m := router.New(router.Disk{Path: filepath.Join(t.TempDir(), "intent.json")}, nil, router.DefaultTiming)
	if _, err := m.Change(nil, func(i *router.Intent) error { i.Enabled = true; i.Source = "active"; return nil }); err != nil {
		t.Fatal(err)
	}
	out, err := selectCheckedKernel(context.Background(), m, "v2.4.1", func(router.Intent) error { t.Fatal("previewed a running configuration"); return nil }, func() error { t.Fatal("selected while running"); return nil })
	if err != nil || string(out) != `{"reason":"intent_changed","staged":"v2.4.1"}` || !m.Status().Intent.Enabled {
		t.Fatal(string(out), err)
	}
}

func TestClockRetriesOnlyLastFailedBootstrapAfterValidityRestored(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := &tlsdiag.Diagnostic{Reason: "expired", CheckedAt: now.Add(24 * time.Hour), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	s := &app{jobs: map[string]job{"a": {Stage: "failed", Action: bootstrapAction, Error: publicKernelError(d.Wire()).Error()}}, order: []string{"a"}}
	if !s.bootstrapClockDue(now) || s.bootstrapClockDue(d.CheckedAt) {
		t.Fatal("wrong bootstrap recovery window")
	}
	s.jobs["b"] = job{Stage: "done", Action: "subscription_write/refresh"}
	s.order = append(s.order, "b")
	if !s.bootstrapClockDue(now) {
		t.Fatal("unrelated operation interrupted bootstrap")
	}
	for _, j := range []job{{Stage: "running", Action: bootstrapAction}, {Stage: "interrupted", Action: bootstrapAction}, {Stage: "failed", Action: "kernel/install-json"}, {Stage: "done", Action: "kernel/select"}} {
		s.jobs["b"] = j
		if s.bootstrapClockDue(now) {
			t.Fatal("overrode newer operation", j)
		}
	}
}
