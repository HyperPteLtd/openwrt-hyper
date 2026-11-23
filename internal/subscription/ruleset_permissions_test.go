package subscription

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The core runs as root and reads and watches the directory as its owner.
func TestRuleDirectoryIsPrivateToRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rulesets")
	for range 2 {
		lock, err := lockSets(dir)
		if err != nil {
			t.Fatal(err)
		}
		unlock(lock)
		parent, err := os.Stat(dir)
		if err != nil || parent.Mode().Perm() != 0700 {
			t.Fatal(parent, err)
		}
		file, err := os.Stat(filepath.Join(dir, "sets.lock"))
		if err != nil || file.Mode().Perm() != 0600 {
			t.Fatal(file, err)
		}
	}
}

func TestCollectSetsRemovesOldCoreFilesAndInterruptedWrites(t *testing.T) {
	store := OpenStore(t.TempDir())
	dir := t.TempDir()
	sum := strings.Repeat("a", 64)
	retired := []string{sum + ".srs", sum + ".json", ".srs-1234", ".rule-set-1234"}
	retained := []string{"user.srs", "abc.json", sum + ".txt"}
	for _, name := range append(append([]string{}, retired...), retained...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CollectSets(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range retired {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale rule file %s: %v", name, err)
		}
	}
	for _, name := range retained {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("unrelated file %s: %v", name, err)
		}
	}
}
