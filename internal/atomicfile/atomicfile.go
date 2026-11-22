// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

// Package atomicfile replaces a file whole or not at all: a reader, or the router after a
// power cut, finds either the old content or the new, never a mix of the two or a torn file.
package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WriteFile replaces path with data. The new file is written in path's directory under a
// temporary name, ".<name>-<random digits>", given exactly mode whatever the umask, synced,
// closed and renamed over path; then the directory is synced, so the rename too survives a
// power cut. When any step up to the rename fails, path is left as it was and the temporary
// file is removed. A crash can leave the temporary file behind: Sweep removes those.
//
// A failure to sync the directory comes after the rename: path holds the new content then,
// but a power cut may still undo the rename.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	if err = SyncDir(dir); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

// SyncDir makes the entries of a directory durable: the files created, renamed into or
// removed from it since.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Sweep removes from dir the temporary files of writes that never reached their rename (a
// kill or power cut in between): regular files named ".<name>-<digits>", for every name when
// names is empty. Only call it while nothing writes in dir.
func Sweep(dir string, names ...string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		i := strings.LastIndexByte(n, '-')
		if !e.Type().IsRegular() || !strings.HasPrefix(n, ".") || i < 2 || !digits(n[i+1:]) {
			continue
		}
		if len(names) > 0 && !contains(names, n[1:i]) {
			continue
		}
		os.Remove(filepath.Join(dir, n))
	}
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func contains(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}
