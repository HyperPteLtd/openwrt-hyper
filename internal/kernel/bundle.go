// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// BundleDir holds the core the package carries: one raw release binary (rawAsset) and its
// pin.json, written by the package build from scripts/core.lock. The package is the trust
// anchor here (root installs it), so its pin verifies the binary without the network.
var BundleDir = "/usr/share/hyper/core"

// ErrOnlineUnavailable: this build offers no online core releases (Repository is empty), as
// the package carries its core.
var ErrOnlineUnavailable = errors.New("online core releases are not offered")

type bundlePin struct {
	Tag    string `json:"tag"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ReadBundle is the bundled core's tag and pin for target; ok is false without a bundle.
func ReadBundle(dir, target string) (tag string, p pin, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "pin.json"))
	if os.IsNotExist(err) {
		return "", pin{}, false, nil
	}
	if err != nil {
		return "", pin{}, false, err
	}
	var bp bundlePin
	if json.Unmarshal(b, &bp) != nil || len(bp.Tag) > 100 || !TagPattern.MatchString(bp.Tag) || bp.Name != rawAsset(bp.Tag, target) ||
		bp.Size <= 0 || bp.Size > maxAsset || !validHash(bp.SHA256) {
		return "", pin{}, false, errors.New("invalid bundled core")
	}
	return bp.Tag, pin{Name: bp.Name, Size: bp.Size, Hash: bp.SHA256}, true, nil
}

// InstallBundled stages the bundled core unless the router has it already, and returns its
// tag ("" without a bundle). With a storage mode it stages it there even when installed
// elsewhere, to move it. Selecting it is the daemon's, which checks the configuration
// against it first, as for a version the administrator picks.
func (m *Manager) InstallBundled(ctx context.Context, dir, mode string) (string, error) {
	tag, p, ok, err := ReadBundle(dir, m.Target)
	if err != nil || !ok {
		return "", err
	}
	if err := m.setup(); err != nil {
		return "", err
	}
	st, err := m.Status()
	if err != nil {
		return "", err
	}
	if mode == "" && (tag == st.Selected || tag == st.Staged || tag == st.Previous || tag == st.Active) ||
		mode != "" && tag == st.Selected && mode == st.Mode {
		return tag, nil
	}
	reservation, err := m.reservePayload()
	if err != nil {
		return "", err
	}
	defer reservation.Close()
	if mode == "" {
		mode = st.Mode
	}
	return tag, m.installBundle(ctx, tag, mode, dir, p)
}

// installBundle stages the bundled core in mode, or without one on flash (double) where it
// fits and else in RAM (tmp), restored from the package after a reboot (restore). The full
// core is large; a router with room for neither keeps its current core.
func (m *Manager) installBundle(ctx context.Context, tag, mode, dir string, p pin) error {
	if mode == "single" || mode == "tmp" || mode == "double" {
		return m.installReserved(ctx, tag, mode, Source{Bundle: dir}, true, &p)
	}
	err := m.installReserved(ctx, tag, "double", Source{Bundle: dir}, true, &p)
	var short capacityError
	if errors.As(err, &short) {
		if e := m.installReserved(ctx, tag, "tmp", Source{Bundle: dir}, true, &p); !errors.As(e, &short) {
			return e
		}
	}
	return err
}

// bundled is the package's core for tag as a source, if the package carries that one.
func (m *Manager) bundled(tag string) (Source, bool) {
	t, _, ok, err := ReadBundle(BundleDir, m.Target)
	return Source{Bundle: BundleDir}, err == nil && ok && t == tag
}
