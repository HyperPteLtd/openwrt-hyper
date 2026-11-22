// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package atomicfile

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if err := WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatal("mode follows the umask:", fi.Mode())
	}
	if err := WriteFile(path, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "b" {
		t.Fatal(string(b))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatal("temporary file left:", entries)
	}
	// A directory in the way: the rename fails, nothing is left behind.
	os.Mkdir(filepath.Join(dir, "busy"), 0o700)
	os.WriteFile(filepath.Join(dir, "busy", "x"), nil, 0o600)
	if WriteFile(filepath.Join(dir, "busy"), []byte("c"), 0o600) == nil {
		t.Fatal("replaced a directory")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatal("temporary file left after a failure:", entries)
	}
}

func TestSweep(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{".nodes.json-123", ".session-9", ".hyper-42", "nodes.json", ".nodes.json-12a", ".x-", "a-1", ".cn.new"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	os.Mkdir(filepath.Join(dir, ".dir-1"), 0o700)
	Sweep(dir, "hyper")
	if _, err := os.Stat(filepath.Join(dir, ".hyper-42")); err == nil {
		t.Fatal("named leftover kept")
	}
	if _, err := os.Stat(filepath.Join(dir, ".session-9")); err != nil {
		t.Fatal("another name swept")
	}
	Sweep(dir)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{".cn.new", ".dir-1", ".nodes.json-12a", ".x-", "a-1", "nodes.json"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}
