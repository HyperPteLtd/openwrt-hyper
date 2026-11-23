// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeUpdater stands in for package-update: body is its shell script.
func fakeUpdater(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path, run := updaterPath, updaterRun
	t.Cleanup(func() { updaterPath, updaterRun = path, run })
	updaterPath, updaterRun = filepath.Join(dir, "package-update"), dir
	if err := os.WriteFile(updaterPath, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUpdater(t *testing.T) {
	ctx := context.Background()
	fakeUpdater(t, `echo 'opkg noise' >&2; printf '{"installed":"1.0.0-1","candidate":"1.0.1-1","available":%s}\n' "$([ "$1" = check ] && echo true || echo false)"`)
	s := &app{}
	out, err := runUpdater(ctx, "check")
	s.notePlugin(out, err)
	if p := s.pluginStatus(); err != nil || p == nil || !p.Available || p.Candidate != "1.0.1-1" || p.Installed != "1.0.0-1" {
		t.Fatal(err, p)
	}
	// The installation runs in its own session, its output kept in files.
	dir := fakeUpdater(t, `[ "$(ps -o sid= -p $$ 2>/dev/null | tr -d ' ')" = "$$" ] || [ -z "$(command -v ps)" ] || exit 9; echo installing >&2; echo '{"installed":"1.0.1-1","candidate":"1.0.1-1","available":false}'`)
	if out, err = runUpdater(ctx, "apply"); err != nil {
		t.Fatal(err)
	}
	s.notePlugin(out, err)
	if p := s.pluginStatus(); p.Available || p.Installed != "1.0.1-1" {
		t.Fatal(p)
	}
	if log, _ := os.ReadFile(filepath.Join(dir, "plugin-update.log")); string(log) != "installing\n" {
		t.Fatalf("%q", log)
	}
	// A failure reads as the updater's own reason, and the package manager's words stay private.
	fakeUpdater(t, `echo 'opkg: secret detail' >&2; echo 'Refusing package downgrade' >&2; exit 1`)
	if _, err = runUpdater(ctx, "apply"); err == nil || err.Error() != "Refusing package downgrade" {
		t.Fatal(err)
	}
	fakeUpdater(t, `echo 'opkg: secret detail' >&2; exit 1`)
	if _, err = runUpdater(ctx, "check"); err == nil || err.Error() != "verified plugin update failed" {
		t.Fatal(err)
	}
	s.notePlugin(nil, err)
	if p := s.pluginStatus(); p.Error != "verified plugin update failed" || p.Installed != "1.0.1-1" {
		t.Fatal(p)
	}
}
