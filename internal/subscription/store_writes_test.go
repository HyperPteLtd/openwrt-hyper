package subscription

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body, reachable, download := "http://edge.example:8080#Edge", true, 0
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		if !reachable {
			return nil, errReset
		}
		download++
		h := http.Header{"Subscription-Userinfo": {"upload=1; download=" + strconv.Itoa(download) + "; total=100"}}
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	refresh := func(want bool) Candidate {
		t.Helper()
		c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{Scheduled: true})
		if (err == nil) != reachable || reachable && c.Decision != "ready" || reachable && c.Unchanged() != want {
			t.Fatal(err, c.Decision, c.Unchanged())
		}
		return c
	}
	refresh(false)
	body = "http://edge.example:8080#Edge\nhttp://edge2.example:8080#Edge 2"
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
	if err != nil || !st.Ready || !st.HasHistory || st.Attempts != 0 || st.UpdatedAt == "" || st.Usage == nil || st.Usage.Download == first.Usage.Download || st.NodeCount != 2 {
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
	reachable = false
	clear(n)
	refresh(false)
	if n["record"] != 0 || n["status"] != 2 {
		t.Fatal("failed refresh wrote", n)
	}
	if st, _ := store.Status(src.ID); st.LastFailure != "subscription_request_failed" || st.Attempts != 1 {
		t.Fatal(st.LastFailure, st.Attempts)
	}

	// A real update.
	reachable, body = true, "http://edge3.example:8080#Edge 3"
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

// What a refresh finds of the template beside its rules and rule sets (the version, whether the
// router's copy served) is no update: the record file and the rollback snapshot stay, and the
// status shows it. The profile keeps the version it was committed with, which the router reads
// for its routing key, until the template's rules change.
func TestTemplateBookkeepingIsNoUpdate(t *testing.T) {
	b := serveBranch(t, 42)
	store, id := fileSource(t, RoutingTemplate)
	refresh := func(body string, want bool) Candidate {
		t.Helper()
		c, err := b.fetcher().Refresh(t.Context(), store, id, []byte(body), Policy{Scheduled: true})
		if err != nil || c.Decision != "ready" || c.Unchanged() != want || c.Profile.Template == nil {
			t.Fatal(err, c.Decision, c.Unchanged(), c.Notices)
		}
		return c
	}
	refresh(twoLinks, false)
	refresh(twoLinks+jpLink, false)
	before, _ := os.ReadFile(store.path(id))
	stored := func() (current, previous *Profile, st Status) {
		t.Helper()
		r, err := store.read(id)
		if err != nil {
			t.Fatal(err)
		}
		return r.Current, r.Previous, statusOf(r)
	}
	unchanged := func(what string) {
		t.Helper()
		if after, _ := os.ReadFile(store.path(id)); !bytes.Equal(before, after) {
			t.Fatal(what, "rewrote the record file")
		}
		// The router's routing key reads the version of the profile it loads.
		current, previous, _ := stored()
		loaded, err := store.Load(id)
		if err != nil || loaded.Template.Version != 42 || current.Template.Cached || len(previous.Nodes) != len(current.Nodes)-1 {
			t.Fatal(what, "changed the profiles", err, loaded.Template, len(previous.Nodes))
		}
	}

	// No host answers: the router's copy serves.
	n := writes(t, store, id)
	b.down.Store(true)
	if c := refresh(twoLinks+jpLink, true); !c.Profile.Template.Cached {
		t.Fatal(c.Profile.Template)
	}
	unchanged("an offline refresh")
	if _, _, st := stored(); st.Template == nil || !st.Template.Cached || st.Template.Version != 42 || n["record"] != 0 || n["status"] != 2 {
		t.Fatal(st.Template, n)
	}

	// A new version of the same rules and rule sets.
	b.down.Store(false)
	b.version = 43
	clear(n)
	refresh(twoLinks+jpLink, true)
	unchanged("a new version of the same rules")
	if _, _, st := stored(); *st.Template != (TemplateUse{Version: 43, Reason: st.Template.Reason}) || n["record"] != 0 {
		t.Fatal(st.Template, n)
	}

	// The template's rule set changes: an update, the profile taking the version.
	b.version, b.set = 44, branchLAN+"DOMAIN-SUFFIX,new.example\n"
	clear(n)
	refresh(twoLinks+jpLink, false)
	current, previous, st := stored()
	if n["record"] != 1 || current.Template.Version != 44 || previous.Template.Version != 42 || *st.Template != *current.Template {
		t.Fatal(n, current.Template, previous.Template, st.Template)
	}
	if r, _ := store.read(id); r.CheckedTemplate != nil {
		t.Fatal("status kept the template of the profile replaced", r.CheckedTemplate)
	}
}

// A display change (here the subscription's title) reaches the profile without counting as an
// update: the rollback snapshot stays and nobody needs telling.
func TestShownChangeKeepsTheSnapshot(t *testing.T) {
	store := OpenStore(t.TempDir())
	src := Source{ID: "sub", Name: "sub", Kind: "external", URL: "https://example.com/feed"}
	if err := store.Configure(src); err != nil {
		t.Fatal(err)
	}
	body, title := "http://edge.example:8080#Edge", ""
	f := Fetcher{Client: &http.Client{Transport: setFixtureTransport(func(*http.Request) (*http.Response, error) {
		h := http.Header{}
		if title != "" {
			h.Set("Profile-Title", title)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	for _, b := range []string{body, body + "\nhttp://edge2.example:8080#Edge 2"} {
		body = b
		if _, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{}); err != nil {
			t.Fatal(err)
		}
	}
	title = "Renamed"
	c, err := f.Refresh(t.Context(), store, src.ID, nil, Policy{})
	if err != nil || !c.Unchanged() {
		t.Fatal(err, c.Unchanged())
	}
	r, _ := store.read(src.ID)
	if r.Current.Title != "Renamed" || len(r.Previous.Nodes) != 1 {
		t.Fatal(r.Current.Title, len(r.Previous.Nodes))
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
	r.LastFailure, r.Attempts, r.AttemptedAt, r.UpdatedAt = "subscription_request_failed", 2, "2026-11-20T10:00:00Z", "2026-11-19T10:00:00Z"
	r.Generation = 9
	old, _ := json.Marshal(r)
	if err := os.WriteFile(store.path(ticket.Source), old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.statusPath(ticket.Source)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Status(ticket.Source)
	if err != nil || !st.Ready || st.LastFailure != "subscription_request_failed" || st.Attempts != 2 || st.UpdatedAt != "2026-11-19T10:00:00Z" || st.Generation != 9 {
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
	if st, _ := store.Status(ticket.Source); st.LastFailure != "subscription_request_failed" || st.Attempts != 3 || st.UpdatedAt != "2026-11-19T10:00:00Z" || st.Generation != 10 {
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
	if st, _ := store.Status(ticket.Source); st.Generation != 20 || st.Attempts != 5 || st.LastFailure != "subscription_request_failed" || st.UpdatedAt != "2026-11-19T10:00:00Z" || !st.Ready {
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
