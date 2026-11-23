package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// watchSync records the directories Write syncs, and what path held at each sync, until the
// test ends; fail, when not nil, is what the sync returns.
func watchSync(t *testing.T, path string, fail error) *[]string {
	t.Helper()
	var synced []string
	previous := syncDir
	syncDir = func(dir string) error {
		b, _ := os.ReadFile(path)
		synced = append(synced, dir+": "+string(b))
		if fail != nil {
			return fail
		}
		return previous(dir)
	}
	t.Cleanup(func() { syncDir = previous })
	return &synced
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileReplacesWholeWithModeAndSyncsTheDirectory(t *testing.T) {
	// A private umask, as procd and rpcd may give, does not narrow the mode asked for.
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	synced := watchSync(t, path, nil)
	if err := WriteFile(path, []byte("new"), 0644, ".state-"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("content %q %v", b, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode() != 0644 {
		t.Fatal("mode", st.Mode(), err)
	}
	// The directory is synced once the rename made the new content visible.
	if !slices.Equal(*synced, []string{dir + ": new"}) {
		t.Fatal("directory syncs", *synced)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"state.json"}) {
		t.Fatal("left behind", names)
	}
}

func TestWriteNamesTheNewFileByThePatternAndStreams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	var name string
	err := Write(path, 0600, ".snapshot-*", func(f *os.File) error {
		name = filepath.Base(f.Name())
		if filepath.Dir(f.Name()) != dir {
			t.Error("new file outside the directory", f.Name())
		}
		for _, part := range []string{"a", "b", "c"} {
			if _, err := f.WriteString(part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, ".snapshot-") || name == ".snapshot-" {
		t.Fatal("new file name", name)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "abc" {
		t.Fatalf("content %q %v", b, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode() != 0600 {
		t.Fatal("mode", st.Mode(), err)
	}
}

func TestFailedWriteLeavesThePathAsItWas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	synced := watchSync(t, path, nil)
	refused := errors.New("refused")
	err := Write(path, 0644, ".state-", func(f *os.File) error {
		if _, err := f.WriteString("partial"); err != nil {
			return err
		}
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "old" {
		t.Fatalf("content %q %v", b, err)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"state.json"}) {
		t.Fatal("left behind", names)
	}
	if len(*synced) != 0 {
		t.Fatal("synced a directory nothing was published in", *synced)
	}
}

func TestFailedRenameRemovesTheNewFile(t *testing.T) {
	dir := t.TempDir()
	// A directory with an entry cannot be renamed over.
	path := filepath.Join(dir, "busy")
	if err := os.MkdirAll(filepath.Join(path, "entry"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0600, ".busy-"); err == nil {
		t.Fatal("replaced a directory")
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"busy"}) {
		t.Fatal("left behind", names)
	}
}

func TestMissingDirectoryIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.json")
	if err := WriteFile(path, []byte("new"), 0600, ".state-"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestDirectorySyncFailureComesAfterTheRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("injected sync failure")
	watchSync(t, path, failed)
	err := WriteFile(path, []byte("new"), 0600, ".state-")
	if !errors.Is(err, failed) || !strings.Contains(err.Error(), "sync directory") {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("content %q %v", b, err)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"state.json"}) {
		t.Fatal("left behind", names)
	}
}

func TestSyncDir(t *testing.T) {
	if err := SyncDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := SyncDir(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
