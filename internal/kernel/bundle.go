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
// tag ("" without a bundle). Selecting it is the daemon's, which checks the configuration
// against it first, as for a version the administrator picks.
func (m *Manager) InstallBundled(ctx context.Context, dir string) (string, error) {
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
	if tag == st.Selected || tag == st.Staged || tag == st.Previous || tag == st.Active {
		return tag, nil
	}
	mode := st.Mode
	if mode != "single" && mode != "tmp" {
		mode = "double"
	}
	reservation, err := m.reservePayload()
	if err != nil {
		return "", err
	}
	defer reservation.Close()
	return tag, m.installReserved(ctx, tag, mode, Source{Bundle: dir}, true, &p)
}
