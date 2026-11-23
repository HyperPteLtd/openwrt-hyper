package subscription

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// killedAfterRecordFile runs an update that stops right after it replaced the record file of
// id, before its status file: what a helper the daemon kills (a manual refresh preempting a
// scheduled one, the daemon stopping) or a power cut leaves.
func killedAfterRecordFile(t *testing.T, s *Store, id string, update func()) {
	t.Helper()
	type killed struct{}
	previous := replaced
	defer func() { replaced = previous }()
	dead, after := false, 0
	replaced = func(path string) {
		previous(path)
		if dead {
			after++
			return
		}
		if path == s.path(id) {
			dead = true
			panic(killed{})
		}
	}
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(killed); !ok {
					panic(v)
				}
			}
		}()
		update()
	}()
	if !dead || after != 0 {
		t.Fatal("no record file written, or files written after it", dead, after)
	}
}

// boundTo reports whether the status file names the record file as it is.
func boundTo(t *testing.T, s *Store, id string) bool {
	t.Helper()
	file, err := os.ReadFile(s.path(id))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.statusPath(id))
	if err != nil {
		t.Fatal(err)
	}
	var v statusFile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(file)
	return v.Record == hex.EncodeToString(sum[:])
}

func feedSource(t *testing.T) (*Store, string, *string, *bool, Fetcher) {
	t.Helper()
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body, reachable := "http://edge.example:8080#Edge", true
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		if !reachable {
			return nil, errReset
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	if c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{Scheduled: true}); err != nil || c.Decision != "ready" {
		t.Fatal(err, c.Decision)
	}
	return store, src.ID, &body, &reachable, f
}

// An update stopped between its record file and its status file leaves a record file that the
// status file does not name. The status still dates the source's last update and counts its
// attempts, so the scheduler keeps refreshing it; what it held of the profile replaced (the
// provider receipt, the template last found, the change counts) does not reach the new one.
func TestUpdateKilledBeforeItsStatusFile(t *testing.T) {
	store, id, body, _, f := feedSource(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.update(id, func(r *record) error {
		r.Providers = &providerReceipt{Key: "edge", Checked: now, Expires: now.Add(time.Hour)}
		r.CheckedTemplate = &TemplateUse{Version: 7}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Status(id)
	if before.UpdatedAt == "" || before.LastChange == nil || !boundTo(t, store, id) {
		t.Fatal(before)
	}

	*body = "http://edge.example:8080#Edge\nhttp://edge2.example:8080#Edge 2"
	killedAfterRecordFile(t, store, id, func() {
		f.Refresh(t.Context(), store, id, nil, Policy{Scheduled: true})
	})
	if boundTo(t, store, id) {
		t.Fatal("status file written")
	}
	r, err := store.read(id)
	if err != nil {
		t.Fatal(err)
	}
	st := statusOf(r)
	if !st.Ready || st.NodeCount != 2 || st.Generation != before.Generation+2 {
		t.Fatal("record file of the update not read", st)
	}
	if st.UpdatedAt != before.UpdatedAt || st.Attempts != 1 || st.AttemptedAt == "" {
		t.Fatal("update date or attempts lost", st.UpdatedAt, st.Attempts, st.AttemptedAt)
	}
	if r.Providers != nil || r.CheckedTemplate != nil || r.LastChange != nil {
		t.Fatal("bookkeeping of the profile replaced", r.Providers, r.CheckedTemplate, r.LastChange)
	}
	if names := nodeNames(r.Previous); len(names) != 1 || names[0] != "Edge" {
		t.Fatal("rollback snapshot", names)
	}

	// The next refresh finds the profile as it is and writes its status file again.
	n := writes(t, store, id)
	if c, err := f.Refresh(t.Context(), store, id, nil, Policy{Scheduled: true}); err != nil || c.Decision != "ready" || !c.Unchanged() {
		t.Fatal(err, c.Decision, c.Unchanged())
	}
	st, _ = store.Status(id)
	if n["record"] != 0 || !boundTo(t, store, id) || st.Attempts != 0 || st.UpdatedAt == "" || st.Generation != before.Generation+4 {
		t.Fatal(n, st)
	}
}

// The first update of a record of the earlier single-file format, stopped before the status
// file, leaves the record file with the bookkeeping: there is no status file to hold it yet.
// The update after it moves the bookkeeping out of the record file.
func TestMigrationKilledBeforeItsStatusFile(t *testing.T) {
	store, id, _, reachable, f := feedSource(t)
	r, err := store.read(id)
	if err != nil {
		t.Fatal(err)
	}
	updated := r.UpdatedAt
	r.PendingUpdate = &PendingUpdate{Code: PendingConfirmation}
	r.LastFailure, r.Attempts, r.AttemptedAt = "subscription_request_failed", 3, "2026-11-22T00:00:00Z"
	// As an earlier version wrote it: one file, encoded its own way.
	old, _ := json.MarshalIndent(r, "", " ")
	if err := os.WriteFile(store.path(id), old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.statusPath(id)); err != nil {
		t.Fatal(err)
	}

	*reachable = false
	killedAfterRecordFile(t, store, id, func() {
		f.Refresh(t.Context(), store, id, nil, Policy{Scheduled: true})
	})
	if _, err := os.Stat(store.statusPath(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status file written", err)
	}
	st, err := store.Status(id)
	if err != nil || !st.Ready || st.Attempts != 4 || st.AttemptedAt == "2026-11-22T00:00:00Z" || st.LastFailure != "subscription_request_failed" ||
		st.PendingUpdate == nil || st.PendingUpdate.Code != PendingConfirmation || st.UpdatedAt != updated {
		t.Fatal("bookkeeping lost", err, st)
	}

	// The refresh again, to its end, and the next one: the record file ends without the
	// bookkeeping, and the status file holds it.
	for range 2 {
		if _, err := f.Refresh(t.Context(), store, id, nil, Policy{Scheduled: true}); err == nil {
			t.Fatal("refresh of an unreachable link succeeded")
		}
	}
	if b, _ := os.ReadFile(store.path(id)); bytes.Contains(b, []byte(`"attempts"`)) || bytes.Contains(b, []byte(`"last_failure"`)) || !boundTo(t, store, id) {
		t.Fatal("bookkeeping left in the record file")
	}
	if st, _ := store.Status(id); st.Attempts != 6 || st.LastFailure != "subscription_request_failed" || st.PendingUpdate == nil || st.UpdatedAt != updated {
		t.Fatal(st)
	}
}

// An earlier version, run for a while and replaced again, wrote the record file with its own
// bookkeeping and left the status file as it was, perhaps at a later generation. The record
// file counts: none of what the status file held comes back.
func TestStatusFileLeftByADowngrade(t *testing.T) {
	store, id, _, reachable, f := feedSource(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.update(id, func(r *record) error {
		r.Providers = &providerReceipt{Key: "abandoned", Checked: now, Expires: now.Add(time.Hour)}
		r.CheckedTemplate = &TemplateUse{Version: 7}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	*reachable = false
	for range 4 {
		f.Refresh(t.Context(), store, id, nil, Policy{Scheduled: true})
	}
	raw, _ := os.ReadFile(store.path(id))
	var old record
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Status(id)
	if st.Attempts != 4 || st.LastFailure == "" || st.Generation < old.Generation+4 {
		t.Fatal("status-only writes", st, old.Generation)
	}

	// The earlier version reads the record file alone and updates the source twice.
	old.Generation += 2
	old.LastFailure, old.Attempts, old.AttemptedAt = "", 0, ""
	old.UpdatedAt = "2026-11-22T12:00:00Z"
	old.Providers = &providerReceipt{Key: "earlier", Checked: now, Expires: now.Add(time.Hour)}
	b, _ := json.Marshal(&old)
	if err := os.WriteFile(store.path(id), b, 0600); err != nil {
		t.Fatal(err)
	}

	r, err := store.read(id)
	if err != nil {
		t.Fatal(err)
	}
	st = statusOf(r)
	if !st.Ready || st.Generation != old.Generation || st.UpdatedAt != "2026-11-22T12:00:00Z" || st.Attempts != 0 || st.LastFailure != "" {
		t.Fatal("abandoned status read", st)
	}
	if r.Providers == nil || r.Providers.Key != "earlier" || r.CheckedTemplate != nil {
		t.Fatal("abandoned receipt read", r.Providers, r.CheckedTemplate)
	}
	// The next update carries on from the record file.
	if _, err := store.Begin(id); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Status(id); st.Generation != old.Generation+1 || st.UpdatedAt != "2026-11-22T12:00:00Z" || st.Attempts != 1 || !boundTo(t, store, id) {
		t.Fatal(st)
	}
}

// Status files of the previous version name only their generation. One no older than the
// record file is read as before; the next update names the record file in it.
func TestStatusFileOfThePreviousVersion(t *testing.T) {
	store, id, _, _, _ := feedSource(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.update(id, func(r *record) error {
		r.Providers = &providerReceipt{Key: "edge", Checked: now, Expires: now.Add(time.Hour)}
		r.Attempts = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(store.statusPath(id))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields["record"] == nil {
		t.Fatal(err, string(raw))
	}
	delete(fields, "record")
	delete(fields, "epoch")
	previous, _ := json.Marshal(fields)
	if err := os.WriteFile(store.statusPath(id), previous, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := store.read(id)
	if err != nil || r.Providers == nil || r.Providers.Key != "edge" || r.Attempts != 2 || r.disk.status == nil {
		t.Fatal(err, r.Providers, r.Attempts)
	}
	if _, err := store.Begin(id); err != nil || !boundTo(t, store, id) {
		t.Fatal(err)
	}
	if r, _ := store.read(id); r.Providers == nil || r.Attempts != 1 {
		t.Fatal(r.Providers, r.Attempts)
	}

	// One older than the record file is not read, as before.
	if err := store.update(id, func(r *record) error {
		r.Generation++
		r.Config.Name = "renamed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statusPath(id), previous, 0600); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.read(id); r.Providers != nil || r.Attempts != 0 || r.UpdatedAt != "" {
		t.Fatal("older status file of the previous version read", r.Providers, r.Attempts)
	}
}

func commitBody(t *testing.T, s *Store, body string) {
	t.Helper()
	ticket, err := s.Begin("source")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ticket, Candidate{Decision: "ready", Profile: mustParse(t, body)}); err != nil {
		t.Fatal(err)
	}
}

const (
	oneNode    = "http://edge.example:8080#Edge"
	twoNodes   = oneNode + "\nhttp://edge2.example:8080#Edge 2"
	threeNodes = twoNodes + "\nhttp://edge3.example:8080#Edge 3"
)

// A rollback reads the record without the lock to validate the snapshot it would restore. Read
// while a commit replaces the record file and then its status file, the record file can be the
// one before the commit and the status file the commit's: the rollback is superseded rather than
// restoring a snapshot it did not validate.
func TestRollbackOfATornRead(t *testing.T) {
	store := OpenStore(t.TempDir())
	commitBody(t, store, oneNode)
	commitBody(t, store, twoNodes)
	before, _ := os.ReadFile(store.path("source"))
	commitBody(t, store, threeNodes)
	after, _ := os.ReadFile(store.path("source"))

	// The commit's status file is in place; the reader still finds the record file before it.
	if err := os.WriteFile(store.path("source"), before, 0600); err != nil {
		t.Fatal(err)
	}
	var validated []string
	err := store.RollbackChecked(t.Context(), "source", func(_ context.Context, p Profile) (func() error, error) {
		validated = nodeNames(&p)
		if err := os.WriteFile(store.path("source"), after, 0600); err != nil {
			t.Fatal(err)
		}
		return func() error { return nil }, nil
	})
	if !errors.Is(err, ErrSuperseded) || len(validated) != 1 {
		t.Fatal(err, validated)
	}
	if p, _ := store.Load("source"); len(p.Nodes) != 3 {
		t.Fatal("rolled back to a snapshot not validated", nodeNames(&p))
	}
	if err := store.RollbackChecked(t.Context(), "source", func(context.Context, Profile) (func() error, error) {
		return func() error { return nil }, nil
	}); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Load("source"); len(p.Nodes) != 2 {
		t.Fatal(nodeNames(&p))
	}
}

// Under the lock, the rollback restores only the snapshot it validated.
func TestRollbackRestoresTheSnapshotItValidated(t *testing.T) {
	store := OpenStore(t.TempDir())
	commitBody(t, store, oneNode)
	commitBody(t, store, twoNodes)
	err := store.RollbackChecked(t.Context(), "source", func(_ context.Context, p Profile) (func() error, error) {
		if err := store.update("source", func(r *record) error {
			other := mustParse(t, threeNodes)
			r.Previous = &other
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return func() error { return nil }, nil
	})
	if !errors.Is(err, ErrSuperseded) {
		t.Fatal(err)
	}
	if p, _ := store.Load("source"); len(p.Nodes) != 2 {
		t.Fatal("restored a snapshot not validated", nodeNames(&p))
	}
}
