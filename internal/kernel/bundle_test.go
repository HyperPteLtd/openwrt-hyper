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
	if tag, err := f.mgr.InstallBundled(ctx, t.TempDir(), ""); tag != "" || err != nil {
		t.Fatal("a package without a core installed one:", tag, err)
	}
	dir := writeBundle(t, "v1.0.1", "amd64")
	tag, err := f.mgr.InstallBundled(ctx, dir, "")
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
	if tag, err := f.mgr.InstallBundled(ctx, dir, ""); tag != "v1.0.1" || err != nil {
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
	if _, err := f.mgr.InstallBundled(ctx, dir, ""); err == nil {
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

func TestReportsVersion(t *testing.T) {
	for _, c := range []struct {
		reported, tag string
		ok            bool
	}{
		{"v1.0.1", "v1.0.1", true},
		{"line-v1.0.1", "v1.0.1", true},
		{"alpha-0123abc", "v0.0.0-alpha.0123abc", true},
		{"line-v1.0.2", "v1.0.1", false},
		{"Line-v1.0.1", "v1.0.1", false},
		{"a-b-v1.0.1", "v1.0.1", false},
		{"alpha-v1.0.1", "v1.0.1", false},
		{"v1.0.1", "v1.0.2", false},
		{"-v1.0.1", "v1.0.1", false},
	} {
		if got := ReportsVersion(c.reported, c.tag); got != c.ok {
			t.Errorf("ReportsVersion(%q, %q) = %v", c.reported, c.tag, got)
		}
	}
}

// Without room on flash the bundled core runs from RAM, and comes back from the package
// after a reboot empties RAM.
func TestBundledCoreInRAMWhenFlashIsShort(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m := f.mgr
	m.Key = ""
	repo, old := Repository, BundleDir
	Repository = "" // as built: no online releases to fall back on
	BundleDir = writeBundle(t, "v1.0.1", "amd64")
	t.Cleanup(func() { Repository, BundleDir = repo, old })
	base := m.Capacity
	m.Capacity = func(root, temp string) (Capacity, error) {
		c, e := base(root, temp)
		c.Persistent.Total, c.Flash = 64<<20, 64<<20
		return c, e
	}
	if tag, err := m.InstallBundled(ctx, BundleDir, ""); err != nil || tag != "v1.0.1" {
		t.Fatal(tag, err)
	}
	if err := m.Select("v1.0.1", false); err != nil {
		t.Fatal(err)
	}
	l, err := m.BeginAuthorized(ctx, f.cfg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Confirm(ctx, l.Token); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.Status(); st.Mode != "tmp" {
		t.Fatalf("expected the core in RAM: %+v", st)
	}
	slots := filepath.Join(m.Temp, "slots")
	entries, _ := os.ReadDir(slots)
	if len(entries) == 0 {
		t.Fatal("no core in RAM")
	}
	for _, e := range entries {
		os.RemoveAll(filepath.Join(slots, e.Name()))
	}
	bundle := BundleDir
	BundleDir = t.TempDir()
	if _, err := m.Recover(ctx); err == nil {
		t.Fatal("restored without the package's core")
	}
	BundleDir = bundle
	a, err := m.Recover(ctx)
	if err != nil {
		t.Fatal("not restored from the package:", err)
	}
	if _, err = os.Stat(a.Path); err != nil {
		t.Fatal(err)
	}
	// Room for neither: the router keeps what it has.
	m.Capacity = func(root, temp string) (Capacity, error) {
		c, e := base(root, temp)
		c.Persistent.Total, c.Flash, c.RAM = 64<<20, 64<<20, 256<<20
		return c, e
	}
	if _, err := m.InstallBundled(ctx, writeBundle(t, "v1.0.2", "amd64"), ""); err == nil {
		t.Fatal("installed a core the router has no room for")
	}
}

// Status names the package's version; asked for another storage, the package's core moves.
func TestMoveBundledCore(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m := f.mgr
	m.Key = ""
	old := BundleDir
	BundleDir = writeBundle(t, "v1.0.1", "amd64")
	t.Cleanup(func() { BundleDir = old })
	if _, err := m.InstallBundled(ctx, BundleDir, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Select("v1.0.1", false); err != nil {
		t.Fatal(err)
	}
	st, _ := m.Status()
	if st.Bundled != "v1.0.1" || st.Mode != "double" {
		t.Fatalf("%+v", st)
	}
	// Already there: nothing to do.
	if _, err := m.InstallBundled(ctx, BundleDir, "double"); err != nil {
		t.Fatal(err)
	}
	if st, _ = m.Status(); st.Staged != "" {
		t.Fatalf("staged again: %+v", st)
	}
	if _, err := m.InstallBundled(ctx, BundleDir, "tmp"); err != nil {
		t.Fatal(err)
	}
	if st, _ = m.Status(); st.Staged != "v1.0.1" {
		t.Fatalf("not staged in RAM: %+v", st)
	}
	// An upgrade brings a newer core to a router that has run this one.
	BundleDir = writeBundle(t, "v1.0.2", "amd64")
	if tag, err := m.InstallBundled(ctx, BundleDir, ""); err != nil || tag != "v1.0.2" {
		t.Fatal(tag, err)
	}
	if st, _ = m.Status(); st.Staged != "v1.0.2" || st.Bundled != "v1.0.2" {
		t.Fatalf("%+v", st)
	}
}
