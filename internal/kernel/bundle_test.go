// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package kernel

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeBundle lays out a package's core as the package build does: the raw release binary
// under its release name and the pin.json naming it.
func writeBundle(t *testing.T, tag, target string) string {
	t.Helper()
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write(tinyCore(t))
	z.Close()
	dir := t.TempDir()
	name := rawAsset(tag, target)
	if err := os.WriteFile(filepath.Join(dir, name), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	pin, _ := json.Marshal(bundlePin{Tag: tag, Name: name, Size: int64(gz.Len()), SHA256: Digest(gz.Bytes())})
	if err := os.WriteFile(filepath.Join(dir, "pin.json"), pin, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The package's core installs offline, from its pin alone, once.
func TestInstallBundled(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.mgr.Key = "" // a package carries no signed manifest
	if tag, err := f.mgr.InstallBundled(ctx, t.TempDir()); tag != "" || err != nil {
		t.Fatal("a package without a core installed one:", tag, err)
	}
	dir := writeBundle(t, "v1.0.1", "amd64")
	tag, err := f.mgr.InstallBundled(ctx, dir)
	if err != nil || tag != "v1.0.1" {
		t.Fatal(tag, err)
	}
	st, err := f.mgr.Status()
	if err != nil || st.Staged != "v1.0.1" {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := f.mgr.Capabilities("v1.0.1"); err != nil {
		t.Fatal("no receipt for the bundled core:", err)
	}
	// Already there: nothing is installed again, even if the bundle went away meanwhile.
	os.Remove(filepath.Join(dir, rawAsset("v1.0.1", "amd64")))
	if tag, err := f.mgr.InstallBundled(ctx, dir); tag != "v1.0.1" || err != nil {
		t.Fatal(tag, err)
	}
}

func TestBundledCoreMustMatchItsPin(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.mgr.Key = ""
	dir := writeBundle(t, "v1.0.2", "amd64")
	// A replaced binary no longer matches the pin.
	if err := os.WriteFile(filepath.Join(dir, rawAsset("v1.0.2", "amd64")), []byte("not the core"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mgr.InstallBundled(ctx, dir); err == nil {
		t.Fatal("installed a binary its pin does not name")
	}
	// A pin for another CPU is not this router's core.
	if _, _, _, err := ReadBundle(writeBundle(t, "v1.0.2", "arm64"), "amd64"); err == nil {
		t.Fatal("accepted another target's core")
	}
}

// First boot installs the package's core and never asks the network.
func TestBootstrapPrefersTheBundledCore(t *testing.T) {
	f := newFixture(t)
	f.mgr.Key = ""
	old := BundleDir
	BundleDir = writeBundle(t, "v1.0.1", "amd64")
	t.Cleanup(func() { BundleDir = old })
	serveReleases(t, map[string][]byte{}) // any release request fails
	got, err := f.mgr.Bootstrap(context.Background())
	if err != nil || got["staged"] != "v1.0.1" {
		t.Fatal(got, err)
	}
}
