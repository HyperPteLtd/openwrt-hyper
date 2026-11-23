package subscription

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestStartupSnapshotHonorsAccountWithdrawalBeforePurge(t *testing.T) {
	store := OpenStore(t.TempDir())
	src := Source{ID: "plan", Name: "Plan", Kind: "platform", Origin: "https://example.test", UserID: "user", PlanID: 7, Token: "synthetic"}
	if err := store.configure(src, true); err != nil {
		t.Fatal(err)
	}
	ticket, err := store.Begin(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Parse(src.ID, []byte("http://node.example:8080#one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ticket, Candidate{Decision: "ready", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.SnapshotReader()
	if _, err := snapshot.Load(src.ID); err != nil || !snapshot.Unchanged() {
		t.Fatal("failed to capture source", err)
	}
	if err := store.markAccount(&src); err != nil {
		t.Fatal(err)
	}
	if snapshot.Unchanged() {
		t.Fatal("account withdrawal accepted before source file was purged")
	}
	if _, err := store.Load(src.ID); !errors.Is(err, ErrRevoked) {
		t.Fatal("withdrawn account readable", err)
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
