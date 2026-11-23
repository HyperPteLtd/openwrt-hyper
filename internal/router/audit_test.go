package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditPutsRemovedRulesBackWithinTheRateLimit(t *testing.T) {
	var calls []string
	verified := errors.New("network verify failed at managed_lan_probe")
	repairOut, repairErr := "repaired\n", error(nil)
	var ready error
	var logged []string
	a := &routingAudit{
		helper: func(_ context.Context, verb string) ([]byte, error) {
			calls = append(calls, verb)
			if verb == "verify" {
				return nil, verified
			}
			return []byte(repairOut), repairErr
		},
		ready: func(context.Context) error { return ready },
		limit: &repairLimit{},
		log:   func(m string) { logged = append(logged, m) },
	}
	ctx := context.Background()
	if e := a.check(ctx); e != nil || len(calls) != 2 || calls[1] != "repair" || len(logged) != 1 || logged[0] != "firewall_rules_restored" {
		t.Fatal(e, calls, logged)
	}
	// Nothing to put back: the verify failure stands as the repair's own.
	repairOut, repairErr = "", errors.New("network repair failed at managed_lan_probe")
	if e := a.check(ctx); e != repairErr || len(logged) != 1 {
		t.Fatal(e, logged)
	}
	// A core that does not answer gets no repair.
	ready = errors.New("local core control unavailable")
	calls = nil
	if e := a.check(ctx); e != verified || len(calls) != 1 {
		t.Fatal(e, calls)
	}
	ready = nil
	// Another tool that keeps removing the rules wins after ten repairs in five minutes.
	repairOut, repairErr = "repaired\n", nil
	for range 9 {
		if e := a.check(ctx); e != nil {
			t.Fatal(e)
		}
	}
	calls = nil
	if e := a.check(ctx); e != verified || len(calls) != 1 || len(logged) != 10 {
		t.Fatal(e, calls, len(logged))
	}
	if !a.limit.allow(time.Now().Add(5 * time.Minute)) {
		t.Fatal("the limit never expires")
	}
}

func TestFileChangedNoticesARewriteAndARemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fw3.state")
	changed := fileChanged(path)
	if changed() {
		t.Fatal("no file yet")
	}
	if e := os.WriteFile(path, []byte("zones"), 0600); e != nil {
		t.Fatal(e)
	}
	if !changed() || changed() {
		t.Fatal("creation")
	}
	if e := os.WriteFile(path, []byte("zones, rules"), 0600); e != nil {
		t.Fatal(e)
	}
	if !changed() || changed() {
		t.Fatal("rewrite")
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if !changed() || changed() {
		t.Fatal("removal")
	}
}
