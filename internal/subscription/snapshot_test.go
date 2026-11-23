package subscription

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// A source revoked after the startup read its snapshot, as by a sign-out, is no longer the one
// the startup checked.
func TestStartupSnapshotHonorsRevocation(t *testing.T) {
	store := OpenStore(t.TempDir())
	ticket, err := store.Begin(HyperSource)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Parse(HyperSource, []byte("http://node.example:8080#one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ticket, Candidate{Decision: "ready", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.SnapshotReader()
	if _, err := snapshot.Load(HyperSource); err != nil || !snapshot.Unchanged() {
		t.Fatal("failed to capture source", err)
	}
	if err := store.Revoke(HyperSource); err != nil {
		t.Fatal(err)
	}
	if snapshot.Unchanged() {
		t.Fatal("revocation accepted after the snapshot")
	}
	if _, err := store.Load(HyperSource); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked source readable", err)
	}
	if _, err := store.SnapshotReader().Load(HyperSource); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked source in a startup snapshot", err)
	}
}

func TestStartupSnapshotDetectsSameSizePreservedTimeEdit(t *testing.T) {
	store := OpenStore(t.TempDir())
	ticket, err := store.Begin("primary")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Parse(ticket.Source, []byte("http://one.example:8080#one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ticket, Candidate{Decision: "ready", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.SnapshotReader()
	if _, err := snapshot.Load(ticket.Source); err != nil {
		t.Fatal(err)
	}
	path := store.path(ticket.Source)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.ReplaceAll(data, []byte("one.example"), []byte("two.example"))
	if bytes.Equal(changed, data) {
		t.Fatal("fixture has no hostname")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if snapshot.Unchanged() {
		t.Fatal("in-place source edit accepted")
	}
}
