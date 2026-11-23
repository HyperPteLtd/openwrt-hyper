package kernel

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s: mode %04o, want %04o", path, got, want)
	}
}

func TestPrivateUmaskInstallPromotionAndRecovery(t *testing.T) {
	for _, mode := range []string{"single", "tmp", "double"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.mgr
			src, _ := f.bundle(t, "v1.2.3", "fixture")
			// Fake roots use a private test parent. Only the two production
			// application parents are eligible for the extra traversal bit.
			if err := os.Chmod(f.tmp, 0700); err != nil {
				t.Fatal(err)
			}
			old := syscall.Umask(0077)
			defer syscall.Umask(old)
			for _, r := range []string{m.Root, m.Temp} {
				if err := os.MkdirAll(filepath.Join(r, "slots"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			m.Inspect = func(_ context.Context, l Launch, cfg string) error {
				requireMode(t, l.Path, 0755)
				requireMode(t, filepath.Dir(l.Path), 0755)
				if cfg != "" {
					requireMode(t, filepath.Dir(cfg), 0711)
					requireMode(t, cfg, 0600)
				}
				return nil
			}
			ctx := context.Background()
			if err := m.Install(ctx, "v1.2.3", mode, src); err != nil {
				t.Fatal(err)
			}
			requireMode(t, f.tmp, 0700)
			requireMode(t, filepath.Join(m.Root, "meta"), 0700)
			for _, r := range []string{m.Root, m.Temp} {
				requireMode(t, r, 0711)
				requireMode(t, filepath.Join(r, "slots"), 0711)
			}
			l, err := m.BeginAuthorized(ctx, f.cfg, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			l, err = m.Confirm(ctx, l.Token)
			if err != nil {
				t.Fatal(err)
			}
			requireMode(t, l.Path, 0755)
			requireMode(t, filepath.Dir(l.Path), 0755)
			// Repair a slot written with the old mode without widening
			// any unrelated file, or downloading a replacement.
			for _, path := range []string{l.Path, filepath.Dir(l.Path), filepath.Join(m.Root, "configs")} {
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			l, err = m.Recover(ctx)
			if err != nil {
				t.Fatal(err)
			}
			requireMode(t, l.Path, 0755)
			requireMode(t, filepath.Dir(l.Path), 0755)
			requireMode(t, filepath.Dir(l.Config), 0711)
		})
	}
}

func TestArchiveRecordsAreReadableButImmutable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dist")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	files := []Member{{Path: "dist/mihomo", Kind: "core"}, {Path: "dist/NOTICE", Kind: "notice"}, {Path: "dist/capabilities.json", Kind: "capabilities"}}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f.Path), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := slotPermissions(root, Asset{Files: files}); err != nil {
		t.Fatal(err)
	}
	requireMode(t, dir, 0755)
	requireMode(t, filepath.Join(dir, "mihomo"), 0755)
	requireMode(t, filepath.Join(dir, "capabilities.json"), 0644)
	requireMode(t, filepath.Join(dir, "NOTICE"), 0644)
}
