package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReclaimDirKeepsOwnStateAndDropsForeign(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mihomo")
	if err := reclaimDir(dir); err != nil {
		t.Fatal("a missing directory is nothing to reclaim:", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rulesets"), 0o700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache.db")
	if err := os.WriteFile(cache, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reclaimDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("our own state was removed:", err)
	}
	if os.Geteuid() != 0 {
		t.Skip("only root can leave a file another account owns")
	}
	if err := os.Chown(cache, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := reclaimDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a directory holding another account's cache was kept")
	}
}
