package subscription

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// writes counts the files store updates write, by kind, until the test ends.
func writes(t *testing.T, s *Store, id string) map[string]int {
	t.Helper()
	n := map[string]int{}
	previous := replaced
	replaced = func(path string) {
		switch path {
		case s.path(id):
			n["record"]++
		case s.statusPath(id):
			n["status"]++
		default:
			n[path]++
		}
	}
	t.Cleanup(func() { replaced = previous })
	return n
}

func nodeNames(p *Profile) []string {
	if p == nil {
		return nil
	}
	var names []string
	for _, n := range p.Nodes {
		names = append(names, n.Name)
	}
	return names
}

// A scheduled refresh that finds the subscription as it was writes the source's status, a few
// hundred bytes, and leaves the record file with its profiles alone. A real update writes the
// record file once, the profile it replaces becoming the rollback snapshot.
func TestUnchangedRefreshWritesOnlyTheStatus(t *testing.T) {
	store, fd := fixtureSource(t, "sub", "http://edge.example:8080#Edge")
	src := Source{ID: "sub"}
	f := Fetcher{}
	refresh := func(want bool) Candidate {
		t.Helper()
		c, err := f.Refresh(t.Context(), store, src.ID, Policy{Scheduled: true})
		if (err == nil) != fd.reachable || fd.reachable && c.Decision != "ready" || fd.reachable && c.Unchanged() != want {
			t.Fatal(err, c.Decision, c.Unchanged())
		}
		return c
	}
	refresh(false)
	fd.body = "http://edge.example:8080#Edge\nhttp://edge2.example:8080#Edge 2"
	refresh(false)
	before, err := os.ReadFile(store.path(src.ID))
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, _ := os.Stat(store.path(src.ID))
	if bytes.Contains(before, []byte(`"attempts"`)) || bytes.Contains(before, []byte(`"updated_at"`)) {
		t.Fatal("bookkeeping in the record file")
	}
	first, _ := store.Status(src.ID)

	n := writes(t, store, src.ID)
	refresh(true)
	after, _ := os.ReadFile(store.path(src.ID))
	afterInfo, _ := os.Stat(store.path(src.ID))
	if n["record"] != 0 || n["status"] != 2 || len(n) != 1 || !bytes.Equal(before, after) || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("unchanged refresh wrote", n)
	}
	st, err := store.Status(src.ID)
	// The status tells of the refresh: no change this time.
	if err != nil || !st.Ready || !st.HasHistory || st.Attempts != 0 || st.UpdatedAt == "" || first.LastChange == nil || first.LastChange.Added != 1 || st.LastChange == nil || *st.LastChange != (ChangeCounts{}) || st.NodeCount != 2 {
		t.Fatal(err, st)
	}
	status, _ := os.Stat(store.statusPath(src.ID))
	if status.Size() > 512 {
		t.Fatal("status file of", status.Size(), "bytes")
	}
	r, _ := store.read(src.ID)
	if names := nodeNames(r.Previous); len(names) != 1 || names[0] != "Edge" {
		t.Fatal("rollback snapshot replaced", names)
	}

	// A failed attempt is bookkeeping too.
	fd.reachable = false
	clear(n)
	refresh(false)
	if n["record"] != 0 || n["status"] != 2 {
		t.Fatal("failed refresh wrote", n)
	}
	if st, _ := store.Status(src.ID); st.LastFailure != "locations_unreachable" || st.Attempts != 1 {
		t.Fatal(st.LastFailure, st.Attempts)
	}

	// A real update.
	fd.reachable, fd.body = true, "http://edge3.example:8080#Edge 3"
	clear(n)
	refresh(false)
	if n["record"] != 1 || n["status"] != 2 {
		t.Fatal("update wrote", n)
	}
	r, _ = store.read(src.ID)
	if names := nodeNames(r.Previous); len(names) != 2 || names[1] != "Edge 2" || nodeNames(r.Current)[0] != "Edge 3" {
		t.Fatal("rollback snapshot", nodeNames(r.Previous), nodeNames(r.Current))
	}
	if st, _ := store.Status(src.ID); st.LastFailure != "" || st.Attempts != 0 || !st.Ready {
		t.Fatal(st)
	}
}

// A display change (here the subscription's format: the same nodes as share links, then in a
// Clash document) reaches the profile without counting as an update: the rollback snapshot
// stays and nobody needs telling.
func TestShownChangeKeepsTheSnapshot(t *testing.T) {
	store, fd := fixtureSource(t, "sub", basic)
	f := Fetcher{}
	for _, b := range []string{basic, basic + "\nhttp://edge2.example:8080#Edge 2"} {
		fd.body = b
		if _, err := f.Refresh(t.Context(), store, "sub", Policy{}); err != nil {
			t.Fatal(err)
		}
	}
	fd.body = `proxies:
 - {name: Edge, type: http, server: edge.example, port: 8080, username: demo, password: fixture-only-password}
 - {name: Edge 2, type: http, server: edge2.example, port: 8080}
`
	c, err := f.Refresh(t.Context(), store, "sub", Policy{})
	if err != nil || !c.Unchanged() {
		t.Fatal(err, c.Unchanged())
	}
	r, _ := store.read("sub")
	if r.Current.Format != "clash" || r.Previous.Format != "uri" || len(r.Previous.Nodes) != 1 {
		t.Fatal(r.Current.Format, len(r.Previous.Nodes))
	}
}

// Records of the earlier format, the bookkeeping in the record file, read as they are. The
// first update writes the status file, the record file keeping the bookkeeping until then, and
// the next moves it out. Of a status file that names another record file only what it tells of
// the source counts.
func TestRecordFileOfTheEarlierFormat(t *testing.T) {
	store := OpenStore(t.TempDir())
	ticket, err := store.Begin("primary")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Parse(ticket.Source, []byte(basic))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ticket, Candidate{Decision: "ready", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	r, err := store.read(ticket.Source)
	if err != nil {
		t.Fatal(err)
	}
	// The earlier format: one file, without a status file.
	r.LastFailure, r.Attempts, r.AttemptedAt, r.UpdatedAt = "locations_unreachable", 2, "2026-11-20T10:00:00Z", "2026-11-19T10:00:00Z"
	r.Generation = 9
	old, _ := json.Marshal(r)
	if err := os.WriteFile(store.path(ticket.Source), old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.statusPath(ticket.Source)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Status(ticket.Source)
	if err != nil || !st.Ready || st.LastFailure != "locations_unreachable" || st.Attempts != 2 || st.UpdatedAt != "2026-11-19T10:00:00Z" || st.Generation != 9 {
		t.Fatal(err, st)
	}
	n := writes(t, store, ticket.Source)
	next, _, err := store.begin(t.Context(), ticket.Source, 0, true)
	if err != nil || next.Generation != 10 || n["record"] != 1 || n["status"] != 1 {
		t.Fatal(err, next, n)
	}
	if b, _ := os.ReadFile(store.path(ticket.Source)); !bytes.Contains(b, []byte(`"last_failure"`)) || !bytes.Contains(b, []byte(`"attempts":3`)) {
		t.Fatal("bookkeeping left only to the status file")
	}
	if st, _ := store.Status(ticket.Source); st.LastFailure != "locations_unreachable" || st.Attempts != 3 || st.UpdatedAt != "2026-11-19T10:00:00Z" || st.Generation != 10 {
		t.Fatal(st)
	}
	// The next update moves it out; later ones leave the record file.
	clear(n)
	if _, _, err := store.begin(t.Context(), ticket.Source, 0, true); err != nil || n["record"] != 1 || n["status"] != 1 {
		t.Fatal(err, n)
	}
	if b, _ := os.ReadFile(store.path(ticket.Source)); bytes.Contains(b, []byte("last_failure")) || bytes.Contains(b, []byte(`"attempts"`)) {
		t.Fatal("bookkeeping left in the record file")
	}
	clear(n)
	if _, _, err := store.begin(t.Context(), ticket.Source, 0, true); err != nil || n["record"] != 0 || n["status"] != 1 {
		t.Fatal(err, n)
	}

	// The record file written after the status file, as by an update interrupted between them.
	stale, _ := os.ReadFile(store.statusPath(ticket.Source))
	if _, err := store.Begin(ticket.Source); err != nil {
		t.Fatal(err)
	}
	r, _ = store.read(ticket.Source)
	r.Generation = 20
	file := *r
	file.bookkeeping = bookkeeping{}
	b, _ := json.Marshal(file)
	if err := os.WriteFile(store.path(ticket.Source), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statusPath(ticket.Source), stale, 0600); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.Status(ticket.Source); st.Generation != 20 || st.Attempts != 5 || st.LastFailure != "locations_unreachable" || st.UpdatedAt != "2026-11-19T10:00:00Z" || !st.Ready {
		t.Fatal("stale status", st)
	}
	// A broken status file leaves the record file's.
	if err := os.WriteFile(store.statusPath(ticket.Source), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if st, err := store.Status(ticket.Source); err != nil || st.Generation != 20 || !st.Ready {
		t.Fatal(err, st)
	}
}
